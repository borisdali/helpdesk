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

// submitJob creates the fleet job record in auditd and registers all target
// servers as pending. Returns the generated job ID.
func submitJob(ctx context.Context, auditURL, submittedBy string, def *fleet.JobDef, servers []string, stages []stageAssignment) (string, error) {
	if auditURL == "" {
		return "", fmt.Errorf("auditd URL not configured (set HELPDESK_AUDIT_URL)")
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
	}

	body, err := json.Marshal(job)
	if err != nil {
		return "", fmt.Errorf("marshal job: %w", err)
	}

	resp, err := auditPost(ctx, auditURL+"/v1/fleet/jobs", body)
	if err != nil {
		return "", fmt.Errorf("create job: %w", err)
	}

	var created audit.FleetJob
	if err := json.Unmarshal(resp, &created); err != nil {
		return "", fmt.Errorf("decode job response: %w", err)
	}
	if created.JobID == "" {
		return "", fmt.Errorf("auditd returned empty job_id")
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
		if _, err := auditPost(ctx, fmt.Sprintf("%s/v1/fleet/jobs/%s/servers", auditURL, created.JobID), srvBody); err != nil {
			// Non-fatal: job is created; server record is best-effort.
			_ = err
		}
	}

	// Register per-step records for all servers.
	var serverNames []string
	for _, sa := range stages {
		serverNames = append(serverNames, sa.server)
	}
	if err := registerJobSteps(ctx, auditURL, created.JobID, serverNames, def.Change.Steps); err != nil {
		// Non-fatal: step records are best-effort.
		_ = err
	}

	return created.JobID, nil
}

// registerJobSteps registers a pending step record for every (server, step) combination.
func registerJobSteps(ctx context.Context, auditURL, jobID string, servers []string, steps []fleet.Step) error {
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
			if _, err := auditPost(ctx, url, body); err != nil {
				// Non-fatal per server/step.
				_ = err
			}
		}
	}
	return nil
}

// finalizeJob updates the job status to completed/failed/aborted with an optional summary.
func finalizeJob(ctx context.Context, auditURL, jobID, status, summary string) error {
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
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// auditPost sends a JSON POST to auditd and returns the response body.
func auditPost(ctx context.Context, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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
