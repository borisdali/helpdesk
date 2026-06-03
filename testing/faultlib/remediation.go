package faultlib

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/testing/testutil"
)

// RemediationResult holds the outcome of a remediation attempt.
type RemediationResult struct {
	Passed           bool
	RecoveryTimeSecs float64
	Err              error
	// Score is 0.0-1.0: 1.0 if recovered within half the verify timeout,
	// 0.75 if recovered within the full timeout, 0.0 if timed out or not attempted.
	Score float64
	// Method records how remediation was triggered: "playbook", "agent_prompt", or "none".
	Method string
}

// ApproveRunResponse mirrors the gateway's ApproveRunResponse for agent_approve playbooks,
// and also carries informed-gate fields when Status=="pending_gate".
type ApproveRunResponse struct {
	RunID                 string          `json:"run_id"`
	Status                string          `json:"status"` // "pending_approval" | "complete" | "denied" | "pending_gate"
	Step                  *ApproveRunStep `json:"step,omitempty"`
	ApprovalID            string          `json:"approval_id,omitempty"`
	Summary               string          `json:"summary,omitempty"`
	Warnings              []string        `json:"warnings,omitempty"`
	EffectiveApprovalMode string          `json:"effective_approval_mode,omitempty"`

	// Informed gate fields — populated when Status=="pending_gate".
	// GateType is "transition" (same-domain triage→remediation) or "escalation" (cross-domain).
	GateType              string `json:"gate_type,omitempty"`
	TransitionTarget      string `json:"transition_target,omitempty"`
	EscalationTarget      string `json:"escalation_target,omitempty"`
	EscalationFindings    string `json:"escalation_findings,omitempty"`
	ConfidenceWarning     string `json:"confidence_warning,omitempty"`
	SuggestedApprovalMode string `json:"suggested_approval_mode,omitempty"`
}

// ProceedEscalationRequest is the request body for
// POST /api/v1/fleet/playbook-runs/{runID}/proceed-escalation.
type ProceedEscalationRequest struct {
	Resolution       string `json:"resolution"`                // "approved" | "denied"
	ResolvedBy       string `json:"resolved_by,omitempty"`
	ApprovalMode     string `json:"approval_mode,omitempty"`   // "manual"|"review"|"auto"|"session"|"force"
	ApprovalSession  string `json:"approval_session,omitempty"`
	ConnectionString string `json:"connection_string,omitempty"`
}

// ApproveRunStep describes a single pending step in an agent_approve run.
type ApproveRunStep struct {
	Index       int            `json:"index"`
	Agent       string         `json:"agent"`
	Tool        string         `json:"tool"`
	Args        map[string]any `json:"args"`
	Reason      string         `json:"reason,omitempty"`
	ActionClass string         `json:"action_class,omitempty"`
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
// priorRunID is the triage playbook run ID from diagnosis; when non-empty it is
// forwarded as prior_run_id so the remediation agent starts with triage context.
func (r *Remediator) Remediate(ctx context.Context, f Failure, priorRunID string) RemediationResult {
	spec := f.Remediation

	slog.Info("starting remediation", "failure", f.ID,
		"playbook", spec.PlaybookID, "agent_prompt", spec.AgentPrompt != "",
		"prior_run_id", priorRunID)

	var method string
	var triggerErr error
	if spec.PlaybookID != "" {
		method = "playbook"
		triggerErr = r.triggerPlaybook(ctx, spec.PlaybookID, priorRunID)
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

	score := 0.75
	if recoverySecs <= timeout.Seconds()/2 {
		score = 1.0
	}

	return RemediationResult{
		Passed:           true,
		RecoveryTimeSecs: recoverySecs,
		Score:            score,
		Method:           method,
	}
}

// resolvePlaybookID resolves a series_id to the active versioned playbook_id.
func (r *Remediator) resolvePlaybookID(ctx context.Context, seriesID string) (string, error) {
	reqURL := r.cfg.GatewayURL + "/api/v1/fleet/playbooks?series_id=" + seriesID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	if r.cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}
	if r.cfg.OperatorID != "" {
		req.Header.Set("X-User", r.cfg.OperatorID)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway returned %d for series %q", resp.StatusCode, seriesID)
	}
	var result struct {
		Playbooks []struct {
			PlaybookID string `json:"playbook_id"`
		} `json:"playbooks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Playbooks) == 0 {
		return "", fmt.Errorf("no active playbook found for series %q", seriesID)
	}
	return result.Playbooks[0].PlaybookID, nil
}

// RunPlaybook resolves seriesID to a versioned playbook_id, POSTs to /run, and
// returns the raw gateway response. It does NOT handle pending_approval —
// callers are responsible for driving any approval loop.
func (r *Remediator) RunPlaybook(ctx context.Context, seriesID, priorRunID string) (*ApproveRunResponse, error) {
	if r.cfg.GatewayURL == "" {
		return nil, fmt.Errorf("gateway URL is required for playbook remediation (--gateway)")
	}

	playbookID, err := r.resolvePlaybookID(ctx, seriesID)
	if err != nil {
		return nil, fmt.Errorf("resolving playbook %q: %w", seriesID, err)
	}

	connStr := r.cfg.ConnStr
	if r.cfg.AgentConnStr != "" {
		connStr = r.cfg.AgentConnStr
	}
	reqBody := map[string]any{"connection_string": connStr}
	if r.cfg.ApprovalMode != "" {
		reqBody["approval_mode"] = r.cfg.ApprovalMode
	}
	if priorRunID != "" {
		reqBody["prior_run_id"] = priorRunID
	}
	if r.cfg.GateEscalation {
		reqBody["gate_escalation"] = true
	}
	body, _ := json.Marshal(reqBody)
	url := r.cfg.GatewayURL + "/api/v1/fleet/playbooks/" + playbookID + "/run"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Purpose", "remediation")
	if r.cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}
	if r.cfg.OperatorID != "" {
		req.Header.Set("X-User", r.cfg.OperatorID)
	}
	if id := FaultTraceID(ctx); id != "" {
		req.Header.Set("X-Trace-ID", id)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("playbook run returned %d: %s", resp.StatusCode, string(respBody))
	}

	var runResp ApproveRunResponse
	if err := json.Unmarshal(respBody, &runResp); err != nil {
		return nil, fmt.Errorf("decoding playbook response: %w", err)
	}
	return &runResp, nil
}

// triggerPlaybook calls RunPlaybook and drives a headless approval loop
// (auto-approve all steps) when the run returns pending_approval.
func (r *Remediator) triggerPlaybook(ctx context.Context, seriesID, priorRunID string) error {
	runResp, err := r.RunPlaybook(ctx, seriesID, priorRunID)
	if err != nil {
		return err
	}
	if runResp.Status == "pending_approval" {
		return r.runApprovalLoop(ctx, runResp)
	}
	if runResp.Status == "pending_gate" {
		return r.RunGateLoop(ctx, runResp)
	}
	slog.Info("playbook triggered", "series_id", seriesID, "status", runResp.Status)
	return nil
}

// RunGateLoop handles a pending_gate response. In emit-and-wait mode it polls
// the gateway until an operator externally resolves the gate; otherwise it
// auto-approves immediately (headless mode for automated test runs).
func (r *Remediator) RunGateLoop(ctx context.Context, gate *ApproveRunResponse) error {
	target := gate.TransitionTarget
	if target == "" {
		target = gate.EscalationTarget
	}
	slog.Info("agent: informed gate pending",
		"run_id", gate.RunID,
		"gate_type", gate.GateType,
		"target", target,
		"confidence_warning", gate.ConfidenceWarning,
	)

	if r.cfg.EmitAndWait && r.cfg.GatewayURL != "" {
		slog.Info("agent: emit-and-wait — polling for gate resolution",
			"run_id", gate.RunID,
			"resolve_url", r.cfg.GatewayURL+"/api/v1/decisions/gate:"+gate.RunID+"/resolve",
		)
		resp, err := r.WaitForGateResolution(ctx, gate.RunID)
		if err != nil {
			return fmt.Errorf("waiting for gate resolution: %w", err)
		}
		if resp.Status == "pending_approval" {
			return r.runApprovalLoop(ctx, resp)
		}
		slog.Info("gate resolved externally", "status", resp.Status)
		return nil
	}

	// Headless: auto-approve immediately.
	approvalMode := r.cfg.ApprovalMode
	if approvalMode == "" {
		approvalMode = "auto"
	}
	connStr := r.cfg.ConnStr
	if r.cfg.AgentConnStr != "" {
		connStr = r.cfg.AgentConnStr
	}
	proceedReq := ProceedEscalationRequest{
		Resolution:       "approved",
		ResolvedBy:       "faulttest",
		ApprovalMode:     approvalMode,
		ConnectionString: connStr,
	}
	resp, err := r.ProceedEscalation(ctx, gate.RunID, proceedReq)
	if err != nil {
		return fmt.Errorf("proceed-escalation: %w", err)
	}
	if resp.Status == "pending_approval" {
		return r.runApprovalLoop(ctx, resp)
	}
	slog.Info("gate approved: remediation triggered", "status", resp.Status, "summary", resp.Summary)
	return nil
}

// WaitForGateResolution polls GET /api/v1/fleet/playbook-runs/{runID} until the
// run's outcome is no longer "gate_pending". It respects ctx cancellation and
// returns an error if the gate is abandoned.
func (r *Remediator) WaitForGateResolution(ctx context.Context, runID string) (*ApproveRunResponse, error) {
	pollInterval := r.cfg.GatewayPollInterval
	if pollInterval <= 0 {
		pollInterval = 15 * time.Second
	}
	url := r.cfg.GatewayURL + "/api/v1/fleet/playbook-runs/" + runID

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		if r.cfg.GatewayAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
		}
		resp, err := r.client.Do(req)
		if err != nil {
			slog.Warn("faultlib: gate poll failed, retrying", "run_id", runID, "err", err)
			continue
		}
		var run struct {
			Outcome string `json:"outcome"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&run)
		resp.Body.Close()

		slog.Info("faultlib: gate poll", "run_id", runID, "outcome", run.Outcome)

		switch run.Outcome {
		case "gate_pending", "":
			continue
		case "abandoned":
			return nil, fmt.Errorf("gate denied by operator (run_id=%s)", runID)
		default:
			// Gate was approved externally. The remediation run has already
			// started; find its first pending step approval so the approval
			// loop can drive it. Retry briefly since the run may not have
			// initialised yet when we detect the outcome change. Use the
			// configured pollInterval (capped at 2s) so tests with fast polling
			// don't sit through the production-sized delay.
			retryDelay := pollInterval
			if retryDelay > 2*time.Second {
				retryDelay = 2 * time.Second
			}
			for attempt := 0; attempt < 6; attempt++ {
				if stepResp := r.findPendingStepApproval(ctx); stepResp != nil {
					return stepResp, nil
				}
				select {
				case <-ctx.Done():
					// Context fired mid-retry; the outcome is still valid, so
					// return it instead of the context error.
					return &ApproveRunResponse{RunID: runID, Status: run.Outcome}, nil
				case <-time.After(retryDelay):
				}
			}
			// No pending step approval found (remediation may have completed
			// immediately or approval_mode=auto). Return the triage run outcome
			// so the caller proceeds to pollRecovery.
			return &ApproveRunResponse{RunID: runID, Status: run.Outcome}, nil
		}
	}
}

// ProceedEscalation calls POST /api/v1/fleet/playbook-runs/{runID}/proceed-escalation.
func (r *Remediator) ProceedEscalation(ctx context.Context, runID string, req ProceedEscalationRequest) (*ApproveRunResponse, error) {
	body, _ := json.Marshal(req)
	reqURL := r.cfg.GatewayURL + "/api/v1/fleet/playbook-runs/" + runID + "/proceed-escalation"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if r.cfg.GatewayAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}
	if r.cfg.OperatorID != "" {
		httpReq.Header.Set("X-User", r.cfg.OperatorID)
	}

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", reqURL, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("proceed-escalation returned %d: %s", resp.StatusCode, string(respBody))
	}
	var result ApproveRunResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decoding proceed-escalation response: %w", err)
	}
	return &result, nil
}

// runApprovalLoop drives an agent_approve loop. In emit-and-wait mode it polls
// the audit service for each step's external resolution; otherwise it
// auto-approves every step (headless mode for automated test runs).
// Interactive callers (cmd/faulttest) implement their own loop using ProceedStep.
func (r *Remediator) runApprovalLoop(ctx context.Context, initial *ApproveRunResponse) error {
	current := initial
	const maxSteps = 20
	for i := 0; i < maxSteps && current.Status == "pending_approval"; i++ {
		if current.Step == nil {
			return fmt.Errorf("approval loop: pending_approval response has no step")
		}

		resolution := "approved"

		if r.cfg.EmitAndWait && current.ApprovalID != "" {
			slog.Info("agent_approve: step approval pending — waiting for external resolution",
				"step_index", current.Step.Index,
				"tool", current.Step.Tool,
				"approval_id", current.ApprovalID,
			)
			if r.cfg.AuditURL != "" {
				ac := audit.NewApprovalClient(r.cfg.AuditURL)
				if r.cfg.GatewayAPIKey != "" {
					ac = ac.WithAPIKey(r.cfg.GatewayAPIKey)
				}
				stored, err := ac.WaitForApproval(ctx, current.ApprovalID, 30*time.Minute)
				if err != nil {
					return fmt.Errorf("waiting for step approval (id=%s): %w", current.ApprovalID, err)
				}
				resolution = stored.Status
			} else if r.cfg.GatewayURL != "" {
				var err error
				resolution, err = r.WaitForStepApprovalViaHub(ctx, current.ApprovalID)
				if err != nil {
					return fmt.Errorf("waiting for step approval via hub (id=%s): %w", current.ApprovalID, err)
				}
			}
			slog.Info("agent_approve: step approval resolved",
				"approval_id", current.ApprovalID,
				"resolution", resolution,
			)
			if resolution != "approved" {
				_, _ = r.ProceedStep(ctx, current.RunID, current.Step.Index, "denied")
				return fmt.Errorf("step %d %s by operator (approval_id=%s)", current.Step.Index, resolution, current.ApprovalID)
			}
		} else {
			slog.Info("agent_approve: auto-approving step",
				"step_index", current.Step.Index,
				"tool", current.Step.Tool,
				"reason", current.Step.Reason,
			)
		}

		next, err := r.ProceedStep(ctx, current.RunID, current.Step.Index, resolution)
		if err != nil {
			return fmt.Errorf("proceed step %d: %w", current.Step.Index, err)
		}
		current = next
	}
	if current.Status == "complete" {
		slog.Info("agent_approve: remediation complete", "summary", current.Summary)
		return nil
	}
	if current.Status == "denied" {
		return fmt.Errorf("step denied")
	}
	return fmt.Errorf("approval loop ended with unexpected status: %s", current.Status)
}

// ProceedStep calls POST /api/v1/fleet/playbook-runs/{runID}/proceed.
// resolution is "approved" or "denied".
func (r *Remediator) ProceedStep(ctx context.Context, runID string, stepIndex int, resolution string) (*ApproveRunResponse, error) {
	connStr := r.cfg.ConnStr
	if r.cfg.AgentConnStr != "" {
		connStr = r.cfg.AgentConnStr
	}
	proceedBody, _ := json.Marshal(map[string]any{
		"resolution":        resolution,
		"resolved_by":       "faulttest",
		"step_index":        stepIndex,
		"connection_string": connStr,
	})
	reqURL := r.cfg.GatewayURL + "/api/v1/fleet/playbook-runs/" + runID + "/proceed"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(proceedBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Purpose", "remediation")
	if r.cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}
	if r.cfg.OperatorID != "" {
		req.Header.Set("X-User", r.cfg.OperatorID)
	}
	if id := FaultTraceID(ctx); id != "" {
		req.Header.Set("X-Trace-ID", id)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", reqURL, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("proceed returned %d: %s", resp.StatusCode, string(respBody))
	}

	var runResp ApproveRunResponse
	if err := json.Unmarshal(respBody, &runResp); err != nil {
		return nil, fmt.Errorf("decode proceed response: %w", err)
	}
	return &runResp, nil
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
	if r.cfg.OperatorID != "" {
		req.Header.Set("X-User", r.cfg.OperatorID)
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

// findPendingStepApproval queries the gateway's decision hub for a pending step
// approval and returns it as an ApproveRunResponse so the approval loop can
// drive it. Returns nil when no pending step approval is found or the gateway
// URL is not configured.
func (r *Remediator) findPendingStepApproval(ctx context.Context) *ApproveRunResponse {
	if r.cfg.GatewayURL == "" {
		return nil
	}
	reqURL := strings.TrimSuffix(r.cfg.GatewayURL, "/") + "/api/v1/decisions?type=step_approval&status=pending&limit=5"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil
	}
	if r.cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}
	resp, err := r.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()
	var result struct {
		Decisions []struct {
			ID    string         `json:"id"`
			Extra map[string]any `json:"extra"`
		} `json:"decisions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Decisions) == 0 {
		return nil
	}
	d := result.Decisions[0]
	approvalID := strings.TrimPrefix(d.ID, "step:")
	runID, _ := d.Extra["run_id"].(string)
	tool, _ := d.Extra["tool"].(string)
	agent, _ := d.Extra["agent"].(string)
	actionClass, _ := d.Extra["action_class"].(string)
	if runID == "" || approvalID == "" {
		return nil
	}
	return &ApproveRunResponse{
		RunID:      runID,
		Status:     "pending_approval",
		ApprovalID: approvalID,
		Step: &ApproveRunStep{
			Tool:        tool,
			Agent:       agent,
			ActionClass: actionClass,
		},
	}
}

// WaitForStepApprovalViaHub polls GET /api/v1/decisions/step:{approvalID} on
// the gateway until the decision status is no longer "pending". Used when
// AuditURL is not configured and the direct long-poll cannot be used.
func (r *Remediator) WaitForStepApprovalViaHub(ctx context.Context, approvalID string) (string, error) {
	pollInterval := r.cfg.GatewayPollInterval
	if pollInterval <= 0 {
		pollInterval = 10 * time.Second
	}
	decisionURL := strings.TrimSuffix(r.cfg.GatewayURL, "/") + "/api/v1/decisions/step:" + approvalID
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, decisionURL, nil)
		if err != nil {
			return "", err
		}
		if r.cfg.GatewayAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
		}
		resp, err := r.client.Do(req)
		if err != nil {
			slog.Warn("faultlib: step approval hub poll failed", "approval_id", approvalID, "err", err)
			continue
		}
		var d struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&d)
		resp.Body.Close()
		slog.Info("faultlib: step approval poll", "approval_id", approvalID, "status", d.Status)
		if d.Status != "pending" && d.Status != "" {
			return d.Status, nil
		}
	}
}

// pollRecovery runs verifySQL against r.cfg.ConnStr every 5 seconds until it
// succeeds or timeout elapses. Returns the elapsed seconds on first success.
func (r *Remediator) pollRecovery(ctx context.Context, verifySQL string, timeout time.Duration) (float64, error) {
	return PollRecovery(ctx, r.cfg.ConnStr, verifySQL, timeout)
}

// PollRecovery runs verifySQL against connStr every 5 seconds until it succeeds
// or timeout elapses. Returns the elapsed seconds on first success.
// Exported so callers that need a different connStr (e.g. after alias resolution)
// can call it directly without constructing a full Remediator.
func PollRecovery(ctx context.Context, connStr, verifySQL string, timeout time.Duration) (float64, error) {
	deadline := time.Now().Add(timeout)
	start := time.Now()

	for {
		err := testutil.RunSQLBool(ctx, connStr, verifySQL)
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
