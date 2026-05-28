package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"helpdesk/internal/infra"
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

// resolvePlaybookID resolves a series_id to the active playbook_id via the gateway list endpoint.
func (r *Remediator) resolvePlaybookID(ctx context.Context, seriesID string) (string, error) {
	reqURL := r.cfg.GatewayURL + "/api/v1/fleet/playbooks?series_id=" + seriesID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	if r.cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway returned %d", resp.StatusCode)
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

// approveRunResponse mirrors the gateway's ApproveRunResponse for agent_approve playbooks.
type approveRunResponse struct {
	RunID                 string          `json:"run_id"`
	Status                string          `json:"status"` // "pending_approval" | "complete" | "denied"
	Step                  *approveRunStep `json:"step,omitempty"`
	ApprovalID            string          `json:"approval_id,omitempty"`
	Summary               string          `json:"summary,omitempty"`
	Warnings              []string        `json:"warnings,omitempty"`
	EffectiveApprovalMode string          `json:"effective_approval_mode,omitempty"`
}

type approveRunStep struct {
	Index       int            `json:"index"`
	Agent       string         `json:"agent"`
	Tool        string         `json:"tool"`
	Args        map[string]any `json:"args"`
	Reason      string         `json:"reason,omitempty"`
	ActionClass string         `json:"action_class,omitempty"`
}

func (r *Remediator) triggerPlaybook(ctx context.Context, seriesID string) error {
	if r.cfg.GatewayURL == "" {
		return fmt.Errorf("gateway URL is required for playbook remediation (--gateway)")
	}

	// The catalog stores the series_id (e.g. "pbs_db_restart_triage"), but the
	// /run endpoint requires the versioned playbook_id (e.g. "pb_f49b5eac").
	// Resolve via the list endpoint before running.
	playbookID, err := r.resolvePlaybookID(ctx, seriesID)
	if err != nil {
		return fmt.Errorf("resolving playbook %q: %w", seriesID, err)
	}

	connStrForPlaybook := r.cfg.AgentConnStr
	if connStrForPlaybook == "" {
		connStrForPlaybook = r.cfg.ConnStr
	}
	remediationReq := map[string]any{"connection_string": connStrForPlaybook}
	if r.cfg.ApprovalMode != "" {
		remediationReq["approval_mode"] = r.cfg.ApprovalMode
	}
	body, _ := json.Marshal(remediationReq)
	reqURL := r.cfg.GatewayURL + "/api/v1/fleet/playbooks/" + playbookID + "/run"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
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
	if id, _ := ctx.Value(ctxKeyFaultTraceID{}).(string); id != "" {
		req.Header.Set("X-Trace-ID", id)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", reqURL, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("playbook run returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Check if this is an agent_approve response (step-by-step approval loop).
	var runResp approveRunResponse
	if err := json.Unmarshal(respBody, &runResp); err == nil && runResp.Status == "pending_approval" {
		return r.runApprovalLoop(ctx, runResp)
	}

	// Surface any gateway warnings (e.g. approval_mode clamped) for agent-mode runs.
	for _, w := range runResp.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	slog.Info("playbook triggered", "id", playbookID, "status", resp.StatusCode)
	return nil
}

// runApprovalLoop drives the agent_approve step-by-step loop.
// --approval-mode force:  auto-approves every step, logs via slog.
// --approval-mode review: auto-approves read-only steps, prompts for write/destructive.
// --approval-mode manual: prompts for every step.
func (r *Remediator) runApprovalLoop(ctx context.Context, initial approveRunResponse) error {
	for _, w := range initial.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	current := initial
	const maxSteps = 20
	// Use the effective mode returned by the gateway (post-clamping) so that
	// approval_override_roles enforcement is honoured on the harness side too.
	// Fall back to the locally configured mode if the gateway didn't return one
	// (older gateway versions, non-agent_approve paths).
	mode := r.cfg.ApprovalMode
	if initial.EffectiveApprovalMode != "" {
		mode = initial.EffectiveApprovalMode
	}

	for i := 0; i < maxSteps && current.Status == "pending_approval"; i++ {
		if current.Step == nil {
			return fmt.Errorf("approval loop: pending_approval response has no step")
		}

		needsPrompt := mode == "manual" ||
			(mode == "review" && current.Step.ActionClass != "read")

		resolution := "approved"
		if needsPrompt {
			approved, err := r.promptStepApproval(current.Step)
			if err != nil {
				return fmt.Errorf("prompt: %w", err)
			}
			if !approved {
				resolution = "denied"
				fmt.Println("  Denied. Sending denial to gateway...")
			}
		} else {
			slog.Info("agent_approve: pending step",
				"step_index", current.Step.Index,
				"tool", current.Step.Tool,
				"action_class", current.Step.ActionClass,
				"reason", current.Step.Reason,
				"approval_id", current.ApprovalID,
			)
		}

		next, err := r.proceedStep(ctx, current.RunID, current.Step.Index, resolution)
		if err != nil {
			return fmt.Errorf("proceed step %d: %w", current.Step.Index, err)
		}
		current = *next
	}

	if current.Status == "complete" {
		if mode == "manual" || mode == "review" {
			fmt.Printf("\n  Remediation complete: %s\n\n", current.Summary)
		} else {
			slog.Info("agent_approve: remediation complete", "summary", current.Summary)
		}
		return nil
	}
	if current.Status == "denied" {
		return fmt.Errorf("step denied by operator")
	}
	return fmt.Errorf("approval loop ended with unexpected status: %s", current.Status)
}

// promptStepApproval prints a proposed step to stdout and reads y/n from the
// controlling terminal (/dev/tty). Using /dev/tty rather than os.Stdin avoids
// reading a stale EOF left on fd 0 by background bash processes spawned during
// injection (exec.Command("bash", "-s") with a pipe stdin that closes on exit).
func (r *Remediator) promptStepApproval(step *approveRunStep) (bool, error) {
	const width = 64
	sep := strings.Repeat("─", width)

	fmt.Printf("\n%s\n", sep)
	fmt.Printf("  Step %d — %s\n", step.Index, step.Tool)

	// Show logical args (strip connection plumbing).
	if display := logicalArgs(step.Args); len(display) > 0 {
		fmt.Println()
		keys := make([]string, 0, len(display))
		for k := range display {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %-10s %v\n", k+":", display[k])
		}
	}

	if step.Reason != "" {
		fmt.Println()
		// Word-wrap reason at width-4 (leave room for the "  " indent).
		for _, line := range wrapText(step.Reason, width-4) {
			fmt.Printf("  %s\n", line)
		}
	}
	fmt.Printf("%s\n", sep)
	fmt.Print("  Approve? [y/n]: ")

	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		tty = os.Stdin // fallback for non-Unix or when no controlling terminal
	} else {
		defer tty.Close()
	}
	reader := bufio.NewReader(tty)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes", nil
}

// logicalArgs returns a copy of args with connection plumbing keys removed.
func logicalArgs(args map[string]any) map[string]any {
	skip := map[string]bool{
		"connection_string": true,
		"host": true, "port": true, "dbname": true,
		"user": true, "password": true,
		"reason": true, // shown separately via step.Reason
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if !skip[k] {
			out[k] = v
		}
	}
	return out
}

// wrapText breaks s into lines of at most width runes, splitting on spaces.
func wrapText(s string, width int) []string {
	var lines []string
	for len(s) > width {
		cut := strings.LastIndex(s[:width], " ")
		if cut <= 0 {
			cut = width
		}
		lines = append(lines, s[:cut])
		s = strings.TrimLeft(s[cut:], " ")
	}
	if s != "" {
		lines = append(lines, s)
	}
	return lines
}

// proceedStep calls POST /api/v1/fleet/playbook-runs/{runID}/proceed.
// resolution is "approved" or "denied".
func (r *Remediator) proceedStep(ctx context.Context, runID string, stepIndex int, resolution string) (*approveRunResponse, error) {
	proceedBody, _ := json.Marshal(map[string]any{
		"resolution":        resolution,
		"resolved_by":       "faulttest",
		"step_index":        stepIndex,
		"connection_string": r.cfg.ConnStr,
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
	if id, _ := ctx.Value(ctxKeyFaultTraceID{}).(string); id != "" {
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

	var runResp approveRunResponse
	if err := json.Unmarshal(respBody, &runResp); err != nil {
		return nil, fmt.Errorf("decode proceed response: %w", err)
	}
	return &runResp, nil
}

func (r *Remediator) triggerAgent(ctx context.Context, agentName, prompt string) error {
	if r.cfg.GatewayURL == "" {
		return fmt.Errorf("gateway URL is required for agent remediation (--gateway)")
	}

	body, _ := json.Marshal(map[string]string{
		"agent":   agentName,
		"message": prompt,
	})
	reqURL := r.cfg.GatewayURL + "/api/v1/query"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
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
		return fmt.Errorf("POST %s: %w", reqURL, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("agent query returned %d: %s", resp.StatusCode, string(respBody))
	}

	slog.Info("agent remediation triggered", "agent", agentName, "status", resp.StatusCode)
	return nil
}

// resolvedConnStr resolves cfg.ConnStr through the infrastructure config so
// that named aliases (e.g. "alloydb-on-vm") are expanded to a real DSN before
// being passed to psql. Falls back to cfg.ConnStr when no infra config is set
// or the alias is not found.
func (r *Remediator) resolvedConnStr() string {
	if r.cfg.InfraConfigPath != "" {
		if cfg, err := infra.Load(r.cfg.InfraConfigPath); err == nil {
			if db, ok := cfg.DBServers[r.cfg.ConnStr]; ok {
				if db.PasswordEnv != "" {
					if pw := os.Getenv(db.PasswordEnv); pw != "" {
						_ = pw // psql reads PGPASSWORD from env; caller sets it
					}
				}
				return db.ResolvedConnectionString()
			}
		}
	}
	return r.cfg.ConnStr
}

func (r *Remediator) pollRecovery(ctx context.Context, verifySQL string, timeout time.Duration) (float64, error) {
	deadline := time.Now().Add(timeout)
	start := time.Now()
	connStr := r.resolvedConnStr()

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
