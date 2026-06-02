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
	"helpdesk/testing/faultlib"
)

// RemediationResult holds the outcome of a remediation attempt.
type RemediationResult struct {
	Passed           bool
	RecoveryTimeSecs float64
	Err              error
	Score            float64
	Method           string
}

// Remediator wraps faultlib.Remediator, adding interactive approval prompts
// and infrastructure alias resolution for the CLI context.
type Remediator struct {
	inner *faultlib.Remediator
	cfg   *HarnessConfig
}

// NewRemediator creates a Remediator backed by cfg.
func NewRemediator(cfg *HarnessConfig) *Remediator {
	return &Remediator{
		inner: faultlib.NewRemediator(toLFConfig(cfg)),
		cfg:   cfg,
	}
}

// Remediate triggers remediation for the failure and polls for recovery.
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

// triggerPlaybook calls RunPlaybook via faultlib and drives an interactive
// approval loop when the run returns pending_approval.
func (r *Remediator) triggerPlaybook(ctx context.Context, seriesID, priorRunID string) error {
	// Bridge the local trace-ID slot into faultlib's slot so that RunPlaybook
	// and ProceedStep set X-Trace-ID on gateway requests.
	if id, _ := ctx.Value(ctxKeyFaultTraceID{}).(string); id != "" {
		ctx = faultlib.WithFaultTraceID(ctx, id)
	}
	runResp, err := r.inner.RunPlaybook(ctx, seriesID, priorRunID)
	if err != nil {
		return err
	}

	for _, w := range runResp.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	if runResp.Status == "pending_approval" {
		return r.runApprovalLoop(ctx, *runResp)
	}

	if runResp.Status == "pending_gate" {
		return r.runGateLoop(ctx, *runResp)
	}

	slog.Info("playbook triggered", "series_id", seriesID, "status", runResp.Status)
	return nil
}

// runGateLoop handles an informed gate interactively: shows the triage findings
// and confidence warning, prompts the operator to approve or deny, and if approved
// asks which approval mode to use for the remediation playbook.
func (r *Remediator) runGateLoop(ctx context.Context, gate faultlib.ApproveRunResponse) error {
	const width = 64
	sep := strings.Repeat("═", width)

	fmt.Printf("\n%s\n", sep)
	fmt.Println("  INFORMED GATE — review before remediation")
	fmt.Printf("%s\n\n", sep)

	fmt.Printf("  Escalation target : %s\n", gate.EscalationTarget)
	if gate.EscalationFindings != "" {
		fmt.Printf("  Findings          : %s\n", gate.EscalationFindings)
	}

	if gate.ConfidenceWarning != "" {
		warnSep := strings.Repeat("─", width)
		fmt.Printf("\n%s\n", warnSep)
		fmt.Printf("  ⚠  CONFIDENCE WARNING: %s\n", gate.ConfidenceWarning)
		fmt.Printf("%s\n", warnSep)
	}
	fmt.Println()

	tty, err := os.Open("/dev/tty")
	if err != nil {
		// Non-interactive: auto-approve with the configured mode.
		return r.inner.RunGateLoop(ctx, &gate)
	}
	defer tty.Close()
	reader := bufio.NewReader(tty)

	fmt.Print("  Approve remediation? [y/N]: ")
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		fmt.Println("  Denied.")
		_, err := r.inner.ProceedEscalation(ctx, gate.RunID, faultlib.ProceedEscalationRequest{
			Resolution: "denied",
			ResolvedBy: r.cfg.OperatorID,
		})
		return err
	}

	suggested := gate.SuggestedApprovalMode
	if suggested == "" {
		suggested = "review"
	}
	fmt.Printf("  Approval mode [manual/review/auto] (default: %s): ", suggested)
	modeInput, _ := reader.ReadString('\n')
	modeInput = strings.TrimSpace(strings.ToLower(modeInput))
	if modeInput == "" {
		modeInput = suggested
	}

	connStr := r.cfg.ConnStr
	if r.cfg.AgentConnStr != "" {
		connStr = r.cfg.AgentConnStr
	}
	resp, err := r.inner.ProceedEscalation(ctx, gate.RunID, faultlib.ProceedEscalationRequest{
		Resolution:       "approved",
		ResolvedBy:       r.cfg.OperatorID,
		ApprovalMode:     modeInput,
		ConnectionString: connStr,
	})
	if err != nil {
		return fmt.Errorf("proceed-escalation: %w", err)
	}
	if resp.Status == "pending_approval" {
		return r.runApprovalLoop(ctx, *resp)
	}
	fmt.Printf("\n  Remediation complete: %s\n\n", resp.Summary)
	return nil
}

// runApprovalLoop drives the agent_approve step-by-step loop interactively.
// --approval-mode force:  auto-approves every step.
// --approval-mode review: auto-approves read-only steps, prompts for write/destructive.
// --approval-mode manual: prompts for every step.
func (r *Remediator) runApprovalLoop(ctx context.Context, initial faultlib.ApproveRunResponse) error {
	for _, w := range initial.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	current := initial
	const maxSteps = 20
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

		next, err := r.inner.ProceedStep(ctx, current.RunID, current.Step.Index, resolution)
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
// injection.
func (r *Remediator) promptStepApproval(step *faultlib.ApproveRunStep) (bool, error) {
	const width = 64
	sep := strings.Repeat("─", width)

	fmt.Printf("\n%s\n", sep)
	fmt.Printf("  Step %d — %s\n", step.Index, step.Tool)

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
		for _, line := range wrapText(step.Reason, width-4) {
			fmt.Printf("  %s\n", line)
		}
	}
	fmt.Printf("%s\n", sep)
	fmt.Print("  Approve? [y/n]: ")

	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		tty = os.Stdin
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

// triggerAgent calls POST /api/v1/query on the gateway.
func (r *Remediator) triggerAgent(ctx context.Context, agentName, prompt string) error {
	if r.cfg.GatewayURL == "" {
		return fmt.Errorf("gateway URL is required for agent remediation (--gateway)")
	}

	body, _ := json.Marshal(map[string]string{"agent": agentName, "message": prompt})
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
	if id, _ := ctx.Value(ctxKeyFaultTraceID{}).(string); id != "" {
		req.Header.Set("X-Trace-ID", id)
	}

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
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

// pollRecovery uses the resolved connection string (infra alias expansion) and
// delegates to faultlib.PollRecovery.
func (r *Remediator) pollRecovery(ctx context.Context, verifySQL string, timeout time.Duration) (float64, error) {
	return faultlib.PollRecovery(ctx, r.resolvedConnStr(), verifySQL, timeout)
}

// resolvedConnStr resolves cfg.ConnStr through the infrastructure config so
// that named aliases are expanded to a real DSN before being passed to psql.
func (r *Remediator) resolvedConnStr() string {
	if r.cfg.InfraConfigPath != "" {
		if cfg, err := infra.Load(r.cfg.InfraConfigPath); err == nil {
			if db, ok := cfg.DBServers[r.cfg.ConnStr]; ok {
				return db.ResolvedConnectionString()
			}
		}
	}
	return r.cfg.ConnStr
}

// logicalArgs returns a copy of args with connection plumbing keys removed.
func logicalArgs(args map[string]any) map[string]any {
	skip := map[string]bool{
		"connection_string": true,
		"host": true, "port": true, "dbname": true,
		"user": true, "password": true,
		"reason": true,
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

