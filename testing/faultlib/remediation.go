package faultlib

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"helpdesk/testing/testutil"
)

// RemediationResult holds the outcome of a remediation attempt.
type RemediationResult struct {
	Passed           bool
	RecoveryTimeSecs float64
	Err              error
	// Score is 0.0-1.0: 1.0 if recovered within half the verify timeout,
	// 0.75 if recovered within the full timeout, 0.0 if timed out or not attempted.
	Score  float64
	// Method records how remediation was triggered: "playbook", "agent_prompt", or "none".
	Method string
}

// Remediator triggers playbook or agent remediation and polls for recovery.
type Remediator struct {
	cfg    *HarnessConfig
	client *http.Client
}

// NewRemediator creates a new Remediator with the given config.
func NewRemediator(cfg *HarnessConfig) *Remediator {
	return &Remediator{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Remediate triggers remediation for the failure and polls for recovery.
func (r *Remediator) Remediate(ctx context.Context, f Failure) RemediationResult {
	spec := f.Remediation

	slog.Info("starting remediation", "failure", f.ID,
		"playbook", spec.PlaybookID, "agent_prompt", spec.AgentPrompt != "")

	// Determine method.
	var method string
	var triggerErr error
	if spec.PlaybookID != "" {
		method = "playbook"
		triggerErr = r.triggerPlaybook(ctx, spec.PlaybookID)
	} else if spec.AgentPrompt != "" {
		method = "agent_prompt"
		agentName := spec.AgentName
		if agentName == "" {
			agentName = "database"
		}
		triggerErr = r.triggerAgent(ctx, agentName, spec.AgentPrompt)
	} else {
		return RemediationResult{Err: fmt.Errorf("no remediation action configured (playbook_id or agent_prompt required)"), Method: "none"}
	}

	if triggerErr != nil {
		return RemediationResult{Err: fmt.Errorf("remediation trigger: %w", triggerErr), Method: method}
	}

	// Poll for recovery.
	verifySQL := spec.VerifySQL
	if verifySQL == "" {
		verifySQL = "SELECT 1"
	}

	timeout := 120 * time.Second
	if spec.VerifyTimeout != "" {
		if d, err := time.ParseDuration(spec.VerifyTimeout); err == nil {
			timeout = d
		}
	}

	recoverySecs, err := r.pollRecovery(ctx, verifySQL, timeout)
	if err != nil {
		return RemediationResult{Err: fmt.Errorf("recovery verification: %w", err), Method: method, Score: 0.0}
	}

	// Score: 1.0 if recovered within half the timeout, 0.75 if within the full timeout.
	score := 0.75
	halfTimeout := timeout.Seconds() / 2
	if recoverySecs <= halfTimeout {
		score = 1.0
	}

	return RemediationResult{
		Passed:           true,
		RecoveryTimeSecs: recoverySecs,
		Score:            score,
		Method:           method,
	}
}

// triggerPlaybook calls POST /api/v1/fleet/playbooks/{id}/run on the gateway.
func (r *Remediator) triggerPlaybook(ctx context.Context, playbookID string) error {
	if r.cfg.GatewayURL == "" {
		return fmt.Errorf("gateway URL is required for playbook remediation (--gateway)")
	}

	body, _ := json.Marshal(map[string]string{"connection_string": r.cfg.ConnStr})
	url := r.cfg.GatewayURL + "/api/v1/fleet/playbooks/" + playbookID + "/run"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Purpose", "remediation")
	if r.cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("playbook run returned %d: %s", resp.StatusCode, string(respBody))
	}

	slog.Info("playbook triggered", "id", playbookID, "status", resp.StatusCode)
	return nil
}

// triggerAgent calls POST /api/v1/query on the gateway with the given prompt.
func (r *Remediator) triggerAgent(ctx context.Context, agentName, prompt string) error {
	if r.cfg.GatewayURL == "" {
		return fmt.Errorf("gateway URL is required for agent remediation (--gateway)")
	}

	body, _ := json.Marshal(map[string]string{
		"agent":   agentName,
		"message": prompt,
	})
	url := r.cfg.GatewayURL + "/api/v1/query"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Purpose", "remediation")
	if r.cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("agent query returned %d: %s", resp.StatusCode, string(respBody))
	}

	slog.Info("agent remediation triggered", "agent", agentName, "status", resp.StatusCode)
	return nil
}

// pollRecovery runs verifySQL every 5 seconds until it succeeds or timeout elapses.
// Returns the elapsed seconds on first success.
func (r *Remediator) pollRecovery(ctx context.Context, verifySQL string, timeout time.Duration) (float64, error) {
	deadline := time.Now().Add(timeout)
	start := time.Now()

	for {
		err := testutil.RunSQLString(ctx, r.cfg.ConnStr, verifySQL)
		if err == nil {
			return time.Since(start).Seconds(), nil
		}

		slog.Info("recovery check failed, retrying", "err", err, "remaining", time.Until(deadline).Round(time.Second))

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(5 * time.Second):
		}

		if time.Now().After(deadline) {
			return 0, fmt.Errorf("recovery timed out after %s", timeout)
		}
	}
}
