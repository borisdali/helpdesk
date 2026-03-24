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

// jobActionClass returns the highest-risk ActionClass across all steps.
// Uses audit.ClassifyTool for each step's tool name.
func jobActionClass(steps []fleet.Step) audit.ActionClass {
	highest := audit.ActionRead
	for _, step := range steps {
		class := audit.ClassifyTool(step.Tool)
		if class.RiskLevel() > highest.RiskLevel() {
			highest = class
		}
	}
	return highest
}

// approvalRequest is the JSON body posted to auditd to request approval.
type approvalRequest struct {
	ActionClass  string         `json:"action_class"`
	ResourceType string         `json:"resource_type"`
	ResourceName string         `json:"resource_name"`
	RequestedBy  string         `json:"requested_by"`
	Context      map[string]any `json:"context"`
	ExpiresAt    string         `json:"expires_at"`
}

// approvalResponse is the JSON body returned from the approval creation endpoint.
type approvalResponse struct {
	ApprovalID string `json:"approval_id"`
	Status     string `json:"status"`
}

// approvalStatusResponse is the JSON body returned from the approval status endpoint.
type approvalStatusResponse struct {
	ApprovalID string `json:"approval_id"`
	Status     string `json:"status"`
}

// requestFleetJobApproval posts an approval request to auditd for the fleet job.
// Returns the approval ID.
func requestFleetJobApproval(ctx context.Context, rcfg runnerConfig, def *fleet.JobDef, serverCount int) (string, error) {
	if rcfg.auditURL == "" {
		return "", fmt.Errorf("auditd URL not configured")
	}

	actionClass := jobActionClass(def.Change.Steps)

	timeoutSecs := def.Strategy.ApprovalTimeoutSeconds
	if timeoutSecs <= 0 {
		timeoutSecs = 30 * 60 // 30 minutes
	}
	expiresAt := time.Now().UTC().Add(time.Duration(timeoutSecs) * time.Second)

	// Collect step tool names for context.
	var stepTools []string
	for _, s := range def.Change.Steps {
		stepTools = append(stepTools, s.Tool)
	}

	submittedBy := rcfg.submittedBy
	if submittedBy == "" {
		submittedBy = "fleet-runner"
	}

	reqBody := approvalRequest{
		ActionClass:  string(actionClass),
		ResourceType: "fleet_job",
		ResourceName: def.Name,
		RequestedBy:  submittedBy,
		Context: map[string]any{
			"job_id":       rcfg.jobID,
			"steps":        stepTools,
			"server_count": serverCount,
			"highest_risk": string(actionClass),
		},
		ExpiresAt: expiresAt.Format(time.RFC3339),
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal approval request: %w", err)
	}

	url := fmt.Sprintf("%s/v1/fleet/jobs/%s/approval", rcfg.auditURL, rcfg.jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create approval request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("post approval request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("auditd returned %d: %s", resp.StatusCode, string(respBody))
	}

	var ar approvalResponse
	if err := json.Unmarshal(respBody, &ar); err != nil {
		return "", fmt.Errorf("decode approval response: %w", err)
	}
	if ar.ApprovalID == "" {
		return "", fmt.Errorf("auditd returned empty approval_id")
	}

	return ar.ApprovalID, nil
}

// waitForFleetApproval polls the approval status endpoint every pollInterval
// until the approval is resolved or the timeout expires.
// Returns true if approved, false if denied/expired/cancelled.
func waitForFleetApproval(ctx context.Context, rcfg runnerConfig, approvalID string, timeoutSecs int, pollInterval time.Duration) (bool, error) {
	if timeoutSecs <= 0 {
		timeoutSecs = 30 * 60 // 30 minutes
	}

	deadline := time.Now().Add(time.Duration(timeoutSecs) * time.Second)
	timer := time.NewTimer(pollInterval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
		}

		status, err := getApprovalStatus(ctx, rcfg, approvalID)
		if err != nil {
			// Non-fatal: log and retry.
			_ = err
		} else {
			switch status {
			case "approved":
				return true, nil
			case "denied":
				return false, fmt.Errorf("approval %s was denied", approvalID)
			case "expired":
				return false, fmt.Errorf("approval %s expired", approvalID)
			case "cancelled":
				return false, fmt.Errorf("approval %s was cancelled", approvalID)
			}
			// "pending" → continue polling.
		}

		if time.Now().After(deadline) {
			return false, fmt.Errorf("approval timed out after %d seconds", timeoutSecs)
		}

		timer.Reset(pollInterval)
	}
}

// getApprovalStatus calls GET {auditURL}/v1/fleet/jobs/{jobID}/approval/{approvalID}
// and returns the status string.
func getApprovalStatus(ctx context.Context, rcfg runnerConfig, approvalID string) (string, error) {
	url := fmt.Sprintf("%s/v1/fleet/jobs/%s/approval/%s", rcfg.auditURL, rcfg.jobID, approvalID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create status request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get approval status: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("auditd returned %d: %s", resp.StatusCode, string(respBody))
	}

	var sr approvalStatusResponse
	if err := json.Unmarshal(respBody, &sr); err != nil {
		return "", fmt.Errorf("decode status response: %w", err)
	}

	return sr.Status, nil
}
