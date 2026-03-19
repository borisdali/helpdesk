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
)

// runnerConfig holds the connection info needed to execute a change against one server.
type runnerConfig struct {
	gatewayURL string
	auditURL   string
	apiKey     string
	jobID      string
}

// executeChange applies the Change to a single server, updating the per-server
// status in auditd before and after the tool call.
func executeChange(ctx context.Context, cfg runnerConfig, serverName, stage string, change Change) (string, error) {
	// Mark server as running.
	if err := patchServerStatus(ctx, cfg, serverName, "running", "", time.Now(), time.Time{}); err != nil {
		slog.Warn("failed to mark server running", "server", serverName, "err", err)
	}

	output, err := callGatewayTool(ctx, cfg, serverName, stage, change)
	now := time.Now()

	status := "success"
	if err != nil {
		status = "failed"
	}
	patchErr := patchServerStatus(ctx, cfg, serverName, status, output, time.Time{}, now)
	if patchErr != nil {
		slog.Warn("failed to update server status", "server", serverName, "err", patchErr)
	}

	return output, err
}

// callGatewayTool sends the tool call to the gateway with fleet_rollout purpose headers.
func callGatewayTool(ctx context.Context, cfg runnerConfig, serverName, stage string, change Change) (string, error) {
	// Inject the target server name into a copy of the args.
	args := make(map[string]any, len(change.Args)+1)
	for k, v := range change.Args {
		args[k] = v
	}
	args["db_server"] = serverName

	body, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("marshal tool args: %w", err)
	}

	path := "/api/v1/db/" + change.Tool
	if change.Agent == "k8s" {
		path = "/api/v1/k8s/" + change.Tool
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.gatewayURL+path, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Purpose", "fleet_rollout")
	req.Header.Set("X-Purpose-Note", fmt.Sprintf("job_id=%s server=%s stage=%s", cfg.jobID, serverName, stage))
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
