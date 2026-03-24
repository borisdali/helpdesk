package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/client"
	"helpdesk/internal/fleet"
)

// runnerConfig holds the connection info needed to execute a change against one server.
type runnerConfig struct {
	gatewayURL           string
	auditURL             string
	apiKey               string
	jobID                string
	submittedBy          string
	approvalPollInterval time.Duration
}

// stepResult holds the outcome of one step for a single server.
type stepResult struct {
	stepIdx  int
	tool     string
	output   string
	err      error
	verified *client.TraceVerification // nil when auditURL not set or verification failed
}

// serverResult holds the overall outcome for one server across all steps.
type serverResult struct {
	server string
	steps  []stepResult
	err    error // set if server is overall "failed" (stop-on-failure triggered)
}

// executeSteps applies all steps to a single server, updating per-server and
// per-step status in auditd before and after each tool call.
func executeSteps(ctx context.Context, cfg runnerConfig, serverName, stage string, steps []fleet.Step) serverResult {
	res := serverResult{server: serverName}

	// Mark server as running.
	if err := patchServerStatus(ctx, cfg, serverName, "running", "", time.Now(), time.Time{}); err != nil {
		slog.Warn("failed to mark server running", "server", serverName, "err", err)
	}

	anyPartialFailure := false

	for idx, step := range steps {
		stepStart := time.Now()
		output, err := callGatewayTool(ctx, cfg, serverName, stage, step)
		sr := stepResult{
			stepIdx: idx,
			tool:    step.Tool,
			output:  output,
			err:     err,
		}
		if err == nil {
			sr.verified = verifyStep(ctx, cfg, step.Tool, stepStart)
		}
		res.steps = append(res.steps, sr)

		if err != nil {
			onFailure := step.OnFailure
			if onFailure == "" {
				onFailure = "stop"
			}

			// Update step status in auditd.
			patchStepStatus(ctx, cfg, serverName, idx, "failed", output)

			if onFailure == "continue" {
				slog.Warn("fleet: step failed (continue)", "server", serverName, "step", idx, "tool", step.Tool, "err", err)
				anyPartialFailure = true
				continue
			}

			// Default: stop on failure.
			patchErr := patchServerStatus(ctx, cfg, serverName, "failed", output, time.Time{}, time.Now())
			if patchErr != nil {
				slog.Warn("failed to update server status", "server", serverName, "err", patchErr)
			}
			res.err = fmt.Errorf("step %d (%s) failed: %w", idx, step.Tool, err)
			return res
		}

		patchStepStatus(ctx, cfg, serverName, idx, "success", output)
	}

	// All steps completed. Determine final server status.
	finalStatus := "success"
	if anyPartialFailure {
		finalStatus = "partial"
	}
	var lastOutput string
	for _, sr := range res.steps {
		if sr.output != "" {
			lastOutput = sr.output
		}
	}
	patchErr := patchServerStatus(ctx, cfg, serverName, finalStatus, lastOutput, time.Time{}, time.Now())
	if patchErr != nil {
		slog.Warn("failed to update server status", "server", serverName, "err", patchErr)
	}

	return res
}

// verifyStep queries auditd to confirm the tool execution for a single step.
// Uses the job-level trace ID with stepStart as the lower bound so that only
// events from this step are matched (not earlier steps in the same job).
func verifyStep(ctx context.Context, cfg runnerConfig, toolName string, stepStart time.Time) *client.TraceVerification {
	if cfg.auditURL == "" {
		return nil
	}
	vc := client.New(client.Config{AuditURL: cfg.auditURL})
	verif, err := vc.VerifyTrace(ctx, "tr_"+cfg.jobID, stepStart)
	if err != nil {
		slog.Debug("fleet: step verification failed", "tool", toolName, "err", err)
		return nil
	}
	if verif != nil {
		logStepVerification(cfg.jobID, toolName, verif)
	}
	return verif
}

// logStepVerification logs the audit result for a fleet step. It warns when a
// write or destructive tool is not confirmed in the audit trail.
func logStepVerification(jobID, toolName string, v *client.TraceVerification) {
	expected := audit.ClassifyTool(toolName)
	confirmed := len(v.ToolsConfirmed)

	if confirmed == 0 {
		if expected == audit.ActionWrite || expected == audit.ActionDestructive {
			slog.Warn("fleet: step verification mismatch — tool not confirmed in audit trail",
				"job_id", jobID,
				"tool", toolName,
				"expected_action", expected,
				"mismatch", true)
		}
		return
	}

	mismatch := false
	switch expected {
	case audit.ActionDestructive:
		mismatch = len(v.DestructiveConfirmed) == 0
	case audit.ActionWrite:
		mismatch = len(v.WriteConfirmed) == 0 && len(v.DestructiveConfirmed) == 0
	}

	if mismatch {
		slog.Warn("fleet: step verification mismatch — expected action not confirmed",
			"job_id", jobID,
			"tool", toolName,
			"expected_action", expected,
			"confirmed_tools", v.ToolsConfirmed,
			"mismatch", true)
	} else {
		slog.Debug("fleet: step verification clean",
			"job_id", jobID,
			"tool", toolName,
			"confirmed_tools", confirmed)
	}
}

// callGatewayTool sends the tool call to the gateway with fleet_rollout purpose headers.
func callGatewayTool(ctx context.Context, cfg runnerConfig, serverName, stage string, step fleet.Step) (string, error) {
	// Inject the target server name into a copy of the args.
	args := make(map[string]any, len(step.Args)+1)
	for k, v := range step.Args {
		args[k] = v
	}
	// Database tools use "connection_string" to identify the target server;
	// k8s tools use "cluster". Both resolve infrastructure IDs via the agent's
	// resolveConnectionString / infrastructure config lookup.
	if step.Agent == "k8s" {
		args["context"] = serverName
	} else {
		args["connection_string"] = serverName
	}

	body, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("marshal tool args: %w", err)
	}

	path := "/api/v1/db/" + step.Tool
	if step.Agent == "k8s" {
		path = "/api/v1/k8s/" + step.Tool
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.gatewayURL+path, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Purpose", "fleet_rollout")
	req.Header.Set("X-Purpose-Note", fmt.Sprintf("job_id=%s server=%s stage=%s", cfg.jobID, serverName, stage))
	// All tool calls for a job share one trace ID so the job appears as a
	// single journey in GET /api/v1/governance/journeys.
	req.Header.Set("X-Trace-ID", "tr_"+cfg.jobID)
	if cfg.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.apiKey)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gateway unreachable: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	output := string(respBody)

	if resp.StatusCode != http.StatusOK {
		return output, fmt.Errorf("tool call returned %d: %s", resp.StatusCode, output)
	}

	// Extract text field from response if present.
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &result); err == nil && result.Text != "" {
		output = result.Text
	}

	return output, nil
}

// patchServerStatus updates the per-server record in auditd via direct HTTP.
func patchServerStatus(ctx context.Context, cfg runnerConfig, serverName, status, output string, startedAt, finishedAt time.Time) error {
	if cfg.auditURL == "" {
		return nil
	}

	type patchReq struct {
		Status     string `json:"status"`
		Output     string `json:"output,omitempty"`
		StartedAt  string `json:"started_at,omitempty"`
		FinishedAt string `json:"finished_at,omitempty"`
	}

	pr := patchReq{Status: status, Output: output}
	if !startedAt.IsZero() {
		pr.StartedAt = startedAt.UTC().Format(time.RFC3339Nano)
	}
	if !finishedAt.IsZero() {
		pr.FinishedAt = finishedAt.UTC().Format(time.RFC3339Nano)
	}

	body, err := json.Marshal(pr)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/v1/fleet/jobs/%s/servers/%s", cfg.auditURL, cfg.jobID, serverName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// patchStepStatus updates an individual step record in auditd via PATCH.
func patchStepStatus(ctx context.Context, cfg runnerConfig, serverName string, stepIdx int, status, output string) {
	if cfg.auditURL == "" {
		return
	}

	type stepPatch struct {
		Status string `json:"status"`
		Output string `json:"output,omitempty"`
	}
	body, err := json.Marshal(stepPatch{Status: status, Output: output})
	if err != nil {
		return
	}

	url := fmt.Sprintf("%s/v1/fleet/jobs/%s/servers/%s/steps/%d", cfg.auditURL, cfg.jobID, serverName, stepIdx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("failed to patch step status", "server", serverName, "step", stepIdx, "err", err)
		return
	}
	resp.Body.Close()
}
