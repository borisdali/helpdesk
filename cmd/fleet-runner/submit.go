package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/fleet"
)

// submitJob creates the fleet job record via the gateway (which proxies to
// auditd and records a journey anchor event) then registers all target servers
// as pending directly in auditd. Returns the generated job ID.
func submitJob(ctx context.Context, gatewayURL, auditURL, apiKey, auditAPIKey, submittedBy string, def *fleet.JobDef, servers []string, stages []stageAssignment) (string, error) {
	if gatewayURL == "" {
		return "", fmt.Errorf("gateway URL not configured (set HELPDESK_GATEWAY_URL)")
	}

	defJSON, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("marshal job def: %w", err)
	}

	job := audit.FleetJob{
		Name:        def.Name,
		SubmittedBy: submittedBy,
		SubmittedAt: time.Now().UTC(),
		Status:      "running",
		JobDef:      string(defJSON),
		PlanTraceID: def.PlanTraceID,
	}

	body, err := json.Marshal(job)
	if err != nil {
		return "", fmt.Errorf("marshal job: %w", err)
	}

	// Create the job via the gateway so that handleFleetCreateJob records
	// a journey anchor event (trace_id = "tr_" + job_id). All subsequent
	// tool calls share that trace ID, making the job visible as a single
	// journey in GET /v1/journeys.
	resp, err := gatewayPost(ctx, gatewayURL+"/api/v1/fleet/jobs", apiKey, body)
	if err != nil {
		return "", fmt.Errorf("create job: %w", err)
	}

	var created audit.FleetJob
	if err := json.Unmarshal(resp, &created); err != nil {
		return "", fmt.Errorf("decode job response: %w", err)
	}
	if created.JobID == "" {
		return "", fmt.Errorf("gateway returned empty job_id")
	}

	// Register each server.
	for _, sa := range stages {
		srv := audit.FleetJobServer{
			JobID:      created.JobID,
			ServerName: sa.server,
			Stage:      sa.stage,
			Status:     "pending",
		}
		srvBody, _ := json.Marshal(srv)
		if _, err := auditPost(ctx, fmt.Sprintf("%s/v1/fleet/jobs/%s/servers", auditURL, created.JobID), auditAPIKey, srvBody); err != nil {
			// Non-fatal: job is created; server record is best-effort.
			_ = err
		}
	}

	// Register per-step records for all servers.
	var serverNames []string
	for _, sa := range stages {
		serverNames = append(serverNames, sa.server)
	}
	if err := registerJobSteps(ctx, auditURL, auditAPIKey, created.JobID, serverNames, def.Change.Steps); err != nil {
		// Non-fatal: step records are best-effort.
		_ = err
	}

	return created.JobID, nil
}

// registerJobSteps registers a pending step record for every (server, step) combination.
func registerJobSteps(ctx context.Context, auditURL, apiKey, jobID string, servers []string, steps []fleet.Step) error {
	for _, serverName := range servers {
		for idx, step := range steps {
			type stepReq struct {
				StepIndex int    `json:"step_index"`
				Tool      string `json:"tool"`
				Status    string `json:"status"`
			}
			req := stepReq{
				StepIndex: idx,
				Tool:      step.Tool,
				Status:    "pending",
			}
			body, err := json.Marshal(req)
			if err != nil {
				return err
			}
			url := fmt.Sprintf("%s/v1/fleet/jobs/%s/servers/%s/steps", auditURL, jobID, serverName)
			if _, err := auditPost(ctx, url, apiKey, body); err != nil {
				// Non-fatal per server/step.
				_ = err
			}
		}
	}
	return nil
}

// finalizeJob updates the job status to completed/failed/aborted with an
// optional summary, and records a terminal audit event when the job did not
// complete successfully so that QueryJourneys reflects the real outcome.
func finalizeJob(ctx context.Context, auditURL, auditAPIKey, jobID, status, summary string) error {
	if auditURL == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"status": status, "summary": summary})
	url := fmt.Sprintf("%s/v1/fleet/jobs/%s/status", auditURL, jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+auditAPIKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	// When the job failed or was denied, append a gateway_request audit event
	// with outcome_status="error" so QueryJourneys elevates the journey outcome
	// from "success" (the anchor event) to "error".
	if status != "completed" {
		recordJobOutcome(ctx, auditURL, auditAPIKey, jobID, summary)
	}
	return nil
}

// recordJobOutcome posts a terminal gateway_request event to auditd so that
// QueryJourneys computes the correct journey outcome for a failed job.
// The event shares the job's trace ID (tr_<jobID>) and has no tool_name,
// so it does not create a spurious tool entry in the journey.
func recordJobOutcome(ctx context.Context, auditURL, auditAPIKey, jobID, errMsg string) {
	if auditURL == "" {
		return
	}
	traceID := "tr_" + jobID
	event := map[string]any{
		"event_id":   "gw_" + jobID[:min(8, len(jobID))] + "_end",
		"event_type": "gateway_request",
		"trace_id":   traceID,
		"timestamp":  time.Now().UTC().Format(time.RFC3339Nano),
		"outcome": map[string]any{
			"status":        "error",
			"error_message": errMsg,
		},
	}
	body, _ := json.Marshal(event)
	auditPost(ctx, auditURL+"/v1/events", auditAPIKey, body) //nolint:errcheck
}

// gatewayPost sends a JSON POST to the gateway with the optional API key.
func gatewayPost(ctx context.Context, url, apiKey string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// auditPost sends a JSON POST to auditd and returns the response body.
func auditPost(ctx context.Context, url, apiKey string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("auditd returned %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// stageAssignment maps a server name to the stage it will run in.
type stageAssignment struct {
	server string
	stage  string
}

// buildStageAssignments returns the stage name for each server given strategy.
func buildStageAssignments(servers []string, strategy fleet.Strategy) []stageAssignment {
	canaryCount := strategy.CanaryCount
	if canaryCount > len(servers) {
		canaryCount = len(servers)
	}

	var assignments []stageAssignment
	for i, s := range servers {
		stage := "canary"
		if i >= canaryCount {
			// Determine wave number.
			waveSize := strategy.WaveSize
			if waveSize <= 0 {
				waveSize = len(servers) - canaryCount
				if waveSize <= 0 {
					waveSize = 1
				}
			}
			waveIdx := (i - canaryCount) / waveSize
			stage = fmt.Sprintf("wave-%d", waveIdx+1)
		}
		assignments = append(assignments, stageAssignment{server: s, stage: stage})
	}
	return assignments
}
