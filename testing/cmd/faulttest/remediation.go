package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
	"helpdesk/testing/faultlib"
	"helpdesk/testing/testutil"
)

// errGateDenied is returned by runGateLoop when the operator explicitly denies
// remediation at the gate. HandlePendingGate treats it as a clean skip — not
// an error — and omits verify_sql polling.
var errGateDenied = errors.New("operator denied remediation at gate")

// RemediationResult holds the outcome of a remediation attempt.
type RemediationResult struct {
	Passed           bool
	RecoveryTimeSecs float64
	Err              error
	Score            float64
	Method           string
	// RunID is the remediation playbook run_id (plr_*). Empty when unavailable
	// (e.g. agent_prompt remediation, or auditd not configured).
	RunID string
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

// HandlePendingGate handles a pending_gate response from the triage playbook.
// It runs the gate loop (interactive TTY or emit-and-wait), after which the
// gateway's proceed-escalation has already started the remediation playbook.
// Recovery is polled independently until the fault clears.
func (r *Remediator) HandlePendingGate(ctx context.Context, f Failure, resp testutil.AgentResponse) RemediationResult {
	gate := faultlib.ApproveRunResponse{
		RunID:                 resp.RunID,
		Status:                resp.Status,
		TransitionTarget:      resp.TransitionTarget,
		EscalationTarget:      resp.EscalationTarget,
		EscalationFindings:    resp.EscalationFindings,
		ConfidenceWarning:     resp.ConfidenceWarning,
		SuggestedApprovalMode: resp.SuggestedMode,
		RemediationPreview:    resp.RemediationPreview,
		DiagnosticReport:      resp.DiagnosticReport,
		GateReason:            resp.GateReason,
	}
	slog.Info("gate pending: operator review required",
		"failure", f.ID,
		"run_id", gate.RunID,
		"escalation_target", gate.EscalationTarget,
	)
	if err := r.runGateLoop(ctx, gate); err != nil {
		if errors.Is(err, errGateDenied) {
			fmt.Println("  Remediation skipped (denied).")
			return RemediationResult{Passed: false, Method: "playbook"}
		}
		return RemediationResult{Err: fmt.Errorf("gate: %w", err), Method: "playbook"}
	}

	// Find the remediation run that was started by proceed-escalation.
	remRunID := r.findChildRunID(ctx, gate.RunID)

	spec := f.Remediation
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
		return RemediationResult{Err: fmt.Errorf("recovery verification: %w", err), Method: "playbook"}
	}
	score := 0.75
	if recoverySecs <= timeout.Seconds()/2 {
		score = 1.0
	}
	if r.cfg.EmitAndWait {
		if resolveURL, err := r.inner.RequestFeedback(ctx, gate.RunID); err != nil {
			slog.Warn("faulttest: failed to request feedback via hub", "run_id", gate.RunID, "err", err)
		} else if resolveURL != "" {
			// If the gateway returned a relative path (HELPDESK_BASE_URL not set),
			// prefix with the gateway URL so the operator has a curl-able address.
			if strings.HasPrefix(resolveURL, "/") && r.cfg.GatewayURL != "" {
				resolveURL = strings.TrimSuffix(r.cfg.GatewayURL, "/") + resolveURL
			}
			fmt.Printf("\n  Feedback pending — resolve at:\n")
			fmt.Printf("  POST %s\n", resolveURL)
			fmt.Printf("  Body fields:\n")
			fmt.Printf("    resolution  : \"approved\" (diagnosis correct) | \"denied\" (diagnosis wrong)\n")
			fmt.Printf("    resolved_by : your email or user ID\n")
			fmt.Printf("    reason      : actual root cause (required when resolution=\"denied\")\n\n")
			r.waitForFeedback(ctx, gate.RunID)
		}
	} else {
		r.submitFeedback(ctx, gate.RunID, remRunID, gate.DiagnosticReport)
	}
	return RemediationResult{
		Passed:           true,
		RecoveryTimeSecs: recoverySecs,
		Score:            score,
		Method:           "playbook",
		RunID:            remRunID,
	}
}

// Remediate triggers remediation for the failure and polls for recovery.
func (r *Remediator) Remediate(ctx context.Context, f Failure, priorRunID string) RemediationResult {
	spec := f.Remediation

	slog.Info("starting remediation", "failure", f.ID,
		"playbook", spec.PlaybookID, "agent_prompt", spec.AgentPrompt != "",
		"prior_run_id", priorRunID)

	var method string
	var remRunID string
	var triggerErr error
	if spec.PlaybookID != "" {
		method = "playbook"
		remRunID, triggerErr = r.triggerPlaybook(ctx, spec.PlaybookID, priorRunID)
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
		RunID:            remRunID,
	}
}

// triggerPlaybook calls RunPlaybook via faultlib and drives an interactive
// approval loop when the run returns pending_approval. Returns the remediation
// run_id so the caller can fetch steps for the remediation judge.
func (r *Remediator) triggerPlaybook(ctx context.Context, seriesID, priorRunID string) (string, error) {
	// Bridge the local trace-ID slot into faultlib's slot so that RunPlaybook
	// and ProceedStep set X-Trace-ID on gateway requests.
	if id, _ := ctx.Value(ctxKeyFaultTraceID{}).(string); id != "" {
		ctx = faultlib.WithFaultTraceID(ctx, id)
	}
	runResp, err := r.inner.RunPlaybook(ctx, seriesID, priorRunID)
	if err != nil {
		return "", err
	}

	for _, w := range runResp.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	if runResp.Status == "pending_approval" {
		return runResp.RunID, r.runApprovalLoop(ctx, *runResp)
	}

	if runResp.Status == "pending_gate" {
		return runResp.RunID, r.runGateLoop(ctx, *runResp)
	}

	slog.Info("playbook triggered", "series_id", seriesID, "status", runResp.Status)
	return runResp.RunID, nil
}

// findChildRunID queries auditd for the remediation run that was started by a
// proceed-escalation call on the given triage run. Returns "" when the run
// cannot be found (e.g. auditd not configured, or run not yet committed).
func (r *Remediator) findChildRunID(ctx context.Context, priorRunID string) string {
	if r.cfg.AuditURL == "" || priorRunID == "" {
		return ""
	}
	url := strings.TrimSuffix(r.cfg.AuditURL, "/") + "/v1/fleet/playbook-runs?prior_run_id=" + priorRunID + "&limit=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	if r.cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()
	var result struct {
		Runs []struct {
			RunID string `json:"run_id"`
		} `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Runs) == 0 {
		return ""
	}
	return result.Runs[0].RunID
}

// printGatePreviewAndReport prints the remediation plan preview and structured
// diagnostic hypotheses (if present) to stdout as part of the gate display.
func printGatePreviewAndReport(preview map[string]any, report map[string]any) {
	if preview != nil {
		name, _ := preview["name"].(string)
		mode, _ := preview["approval_mode"].(string)
		desc, _ := preview["description"].(string)
		if name != "" {
			line := "  Remediation plan  : " + name
			if mode != "" {
				line += " (" + mode + " approval)"
			}
			fmt.Println(line)
			if desc != "" {
				fmt.Printf("                      %s\n", desc)
			}
		}
	}
	if report != nil {
		hyps, _ := report["hypotheses"].([]any)
		if len(hyps) > 0 {
			fmt.Println("  Hypotheses        :")
			for _, h := range hyps {
				hm, _ := h.(map[string]any)
				text, _ := hm["text"].(string)
				conf, _ := hm["confidence"].(float64)
				isPrimary, _ := hm["is_primary"].(bool)
				rejected, _ := hm["rejected_reason"].(string)

				pct := int(conf * 100)
				var label string
				switch {
				case isPrimary:
					label = fmt.Sprintf("[PRIMARY %d%%]", pct)
				case rejected != "":
					label = fmt.Sprintf("[REJECTED %d%%]", pct)
				default:
					label = fmt.Sprintf("[%d%%]", pct)
				}
				if rejected != "" {
					fmt.Printf("    %s %s — %s\n", label, text, rejected)
				} else {
					fmt.Printf("    %s %s\n", label, text)
				}
			}
		}
	}
}

// runGateLoop handles an informed gate interactively: shows the triage findings
// and confidence warning, prompts the operator to approve or deny, and if approved
// asks which approval mode to use for the remediation playbook.
func (r *Remediator) runGateLoop(ctx context.Context, gate faultlib.ApproveRunResponse) error {
	const width = 64
	sep := strings.Repeat("═", width)

	fmt.Printf("\n%s\n", sep)
	if gate.GateReason == "low_confidence" {
		fmt.Println("  INFORMED GATE — LOW CONFIDENCE DIAGNOSIS")
	} else {
		fmt.Println("  INFORMED GATE — review before remediation")
	}
	fmt.Printf("%s\n\n", sep)

	gateTarget := gate.TransitionTarget
	if gateTarget == "" {
		gateTarget = gate.EscalationTarget
	}
	if gateTarget != "" {
		fmt.Printf("  Next playbook     : %s\n", gateTarget)
	}
	if gate.EscalationFindings != "" {
		fmt.Printf("  Findings          : %s\n", gate.EscalationFindings)
	}
	printGatePreviewAndReport(gate.RemediationPreview, gate.DiagnosticReport)

	if gate.ConfidenceWarning != "" {
		warnSep := strings.Repeat("─", width)
		fmt.Printf("\n%s\n", warnSep)
		fmt.Printf("  ⚠  CONFIDENCE WARNING: %s\n", gate.ConfidenceWarning)
		fmt.Printf("%s\n", warnSep)
	}
	fmt.Println()

	// emit-and-wait: poll until operator resolves via the Decision Hub.
	// Checked before TTY so this path works on a developer laptop too.
	if r.cfg.EmitAndWait {
		return r.waitForGateEmitAndWait(ctx, gate)
	}

	// --approval-mode force: skip the gate entirely, auto-approve and proceed.
	if r.cfg.ApprovalMode == "force" {
		slog.Info("force-approving gate", "run_id", gate.RunID)
		_, err := r.inner.ProceedEscalation(ctx, gate.RunID, faultlib.ProceedEscalationRequest{
			Resolution:   "approved",
			ResolvedBy:   r.cfg.OperatorID,
			ApprovalMode: "auto",
		})
		return err
	}

	tty, err := os.Open("/dev/tty")
	if err != nil {
		// Non-interactive without emit-and-wait: auto-approve with the configured mode.
		return r.inner.RunGateLoop(ctx, &gate)
	}
	defer tty.Close()
	reader := bufio.NewReader(tty)

	fmt.Print("  Approve remediation? [y/N]: ")
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		fmt.Println("  Denied.")
		fmt.Println()
		fmt.Println("  Feedback (optional — recorded even though remediation was denied):")
		var denyVerdictCorrect *bool
		var denyVerdictNotes string
		fmt.Print("    Was the triage diagnosis correct? [y/n/skip]: ")
		diagAnswer, _ := reader.ReadString('\n')
		diagAnswer = strings.TrimSpace(strings.ToLower(diagAnswer))
		switch diagAnswer {
		case "y", "yes":
			v := true
			denyVerdictCorrect = &v
		case "n", "no":
			v := false
			denyVerdictCorrect = &v
		}
		if denyVerdictCorrect != nil {
			defaultCause := primaryHypothesisText(gate.DiagnosticReport)
			prompt := "    Root cause"
			if defaultCause != "" {
				prompt += fmt.Sprintf(" (Enter to confirm: %q)", defaultCause)
			}
			fmt.Print(prompt + ": ")
			causeInput, _ := reader.ReadString('\n')
			causeInput = strings.TrimSpace(causeInput)
			if causeInput == "" {
				causeInput = defaultCause
			}
			denyVerdictNotes = causeInput
		}
		var denyRemVerdictCorrect *bool
		var denyRemVerdictNotes string
		fmt.Print("    Was the proposed remediation appropriate? [y/n/skip]: ")
		remAnswer, _ := reader.ReadString('\n')
		remAnswer = strings.TrimSpace(strings.ToLower(remAnswer))
		switch remAnswer {
		case "y", "yes":
			v := true
			denyRemVerdictCorrect = &v
		case "n", "no":
			v := false
			denyRemVerdictCorrect = &v
			fmt.Print("    Notes on remediation plan (optional): ")
			remNotes, _ := reader.ReadString('\n')
			denyRemVerdictNotes = strings.TrimSpace(remNotes)
		}
		r.inner.ProceedEscalation(ctx, gate.RunID, faultlib.ProceedEscalationRequest{ //nolint:errcheck
			Resolution:     "denied",
			ResolvedBy:     r.cfg.OperatorID,
			VerdictCorrect: denyVerdictCorrect,
			VerdictNotes:   denyVerdictNotes,
		})
		if denyRemVerdictCorrect != nil {
			r.postFeedback(ctx, gate.RunID, "remediation", "at_gate", denyRemVerdictCorrect, denyRemVerdictNotes, "")
		}
		return errGateDenied
	}

	suggested := gate.SuggestedApprovalMode
	if suggested == "" {
		suggested = "review"
	}
	validModes := map[string]bool{"manual": true, "review": true, "auto": true}
	var modeInput string
	for {
		fmt.Printf("  Approval remediation mode [manual/review/auto] (default: %s): ", suggested)
		modeInput, _ = reader.ReadString('\n')
		modeInput = strings.TrimSpace(strings.ToLower(modeInput))
		if modeInput == "" {
			modeInput = suggested
			break
		}
		if validModes[modeInput] {
			break
		}
		fmt.Printf("  Invalid mode %q — enter manual, review, or auto (or press Enter for %s).\n", modeInput, suggested)
	}

	fmt.Print("  Approval note — reason for approving remediation (optional): ")
	reasonInput, _ := reader.ReadString('\n')
	reasonInput = strings.TrimSpace(reasonInput)

	// At-gate feedback — captured before remediation runs so the signal is
	// independent of whether the fix worked.
	fmt.Println()
	fmt.Println("  Feedback (optional, but recommended — recorded before remediation runs):")
	var verdictCorrect *bool
	var verdictNotes string
	fmt.Print("    Was the triage diagnosis correct? [y/n/skip]: ")
	diagAnswer, _ := reader.ReadString('\n')
	diagAnswer = strings.TrimSpace(strings.ToLower(diagAnswer))
	switch diagAnswer {
	case "y", "yes":
		v := true
		verdictCorrect = &v
	case "n", "no":
		v := false
		verdictCorrect = &v
	}
	if verdictCorrect != nil {
		defaultCause := primaryHypothesisText(gate.DiagnosticReport)
		prompt := "    Root cause"
		if defaultCause != "" {
			prompt += fmt.Sprintf(" (Enter to confirm: %q)", defaultCause)
		}
		fmt.Print(prompt + ": ")
		causeInput, _ := reader.ReadString('\n')
		causeInput = strings.TrimSpace(causeInput)
		if causeInput == "" {
			causeInput = defaultCause
		}
		verdictNotes = causeInput
	}

	// Remediation approach verdict — asked at the same gate, before remediation
	// runs, so the signal is free of outcome bias (same reason as triage/at_gate).
	var remVerdictCorrect *bool
	var remVerdictNotes string
	fmt.Print("    Was the proposed remediation appropriate? [y/n/skip]: ")
	remAnswer, _ := reader.ReadString('\n')
	remAnswer = strings.TrimSpace(strings.ToLower(remAnswer))
	switch remAnswer {
	case "y", "yes":
		v := true
		remVerdictCorrect = &v
	case "n", "no":
		v := false
		remVerdictCorrect = &v
		fmt.Print("    Notes on remediation plan (optional): ")
		remNotes, _ := reader.ReadString('\n')
		remVerdictNotes = strings.TrimSpace(remNotes)
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
		Reason:           reasonInput,
		VerdictCorrect:   verdictCorrect,
		VerdictNotes:     verdictNotes,
	})
	if err != nil {
		return fmt.Errorf("proceed-escalation: %w", err)
	}
	if remVerdictCorrect != nil {
		r.postFeedback(ctx, gate.RunID, "remediation", "at_gate", remVerdictCorrect, remVerdictNotes, "")
	}
	if resp.Status == "pending_approval" {
		return r.runApprovalLoop(ctx, *resp)
	}
	fmt.Printf("\n  Remediation complete: %s\n\n", resp.Summary)
	return nil
}

// waitForGateEmitAndWait prints the gate summary and polls until the operator
// externally resolves the gate via the Decision Hub or proceed-escalation endpoint.
func (r *Remediator) waitForGateEmitAndWait(ctx context.Context, gate faultlib.ApproveRunResponse) error {
	resolveURL := r.cfg.GatewayURL + "/api/v1/decisions/gate:" + gate.RunID + "/resolve"
	fmt.Printf("\nGate pending — run_id=%s\n", gate.RunID)
	fmt.Printf("  Resolve at        : POST %s\n", resolveURL)
	fmt.Printf("  Body fields:\n")
	fmt.Printf("    resolution        : \"approved\" | \"denied\"\n")
	fmt.Printf("    resolved_by       : your email or user ID\n")
	fmt.Printf("    approval_mode     : \"auto\" | \"review\" | \"manual\" (default: playbook setting)\n")
	fmt.Printf("    reason            : optional — free-text operator comment\n")
	fmt.Printf("    verdict_correct : true | false  (triage/at_gate feedback, before remediation runs)\n")
	fmt.Printf("    verdict_notes   : string        (required when verdict_correct=false)\n")
	feedbackURL := r.cfg.GatewayURL + "/api/v1/fleet/playbook-runs/" + gate.RunID + "/feedback"
	fmt.Printf("\n  Remediation feedback (separate call, same gate window):\n")
	fmt.Printf("    POST %s\n", feedbackURL)
	fmt.Printf("    {\"feedback_type\":\"remediation\",\"feedback_time\":\"at_gate\",\"verdict_correct\":true,\"verdict_notes\":\"...\"}\n\n")

	resp, err := r.inner.WaitForGateResolution(ctx, gate.RunID)
	if err != nil {
		return fmt.Errorf("waiting for gate resolution: %w", err)
	}
	if resp.Status == "pending_approval" {
		return r.runApprovalLoop(ctx, *resp)
	}
	slog.Info("gate resolved externally", "status", resp.Status)
	if resp.Status == "denied" {
		return errGateDenied
	}
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
	const maxSteps = 100
	// Resolve approval mode:
	// 1. Gateway "force" (policy override via approval_override_roles) always wins.
	// 2. CLI --approval-mode flag wins over the playbook's gateway default.
	// 3. Fall back to the gateway's effective mode when no CLI flag was given.
	mode := r.cfg.ApprovalMode
	if initial.EffectiveApprovalMode == "force" {
		mode = "force"
	} else if mode == "" {
		mode = initial.EffectiveApprovalMode
	}

	for i := 0; i < maxSteps && current.Status == "pending_approval"; i++ {
		if current.Step == nil {
			return fmt.Errorf("approval loop: pending_approval response has no step")
		}

		needsPrompt := mode == "manual" ||
			(mode == "review" && current.Step.ActionClass != "read")

		resolution := "approved"
		switch {
		case r.cfg.EmitAndWait && current.ApprovalID != "" &&
			current.Step.ActionClass != "read" &&
			(mode == "manual" || mode == "review"):
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
				resolution, err = r.inner.WaitForStepApprovalViaHub(ctx, current.ApprovalID)
				if err != nil {
					return fmt.Errorf("waiting for step approval via hub (id=%s): %w", current.ApprovalID, err)
				}
			}
			slog.Info("agent_approve: step approval resolved",
				"approval_id", current.ApprovalID,
				"resolution", resolution,
			)
			if resolution != "approved" {
				fmt.Printf("  Step %d %s by operator.\n", current.Step.Index, resolution)
				resolution = "denied"
			}
		case needsPrompt:
			approved, err := r.promptStepApproval(current.Step)
			if err != nil {
				return fmt.Errorf("prompt: %w", err)
			}
			if !approved {
				resolution = "denied"
				fmt.Println("  Denied. Sending denial to gateway...")
			}
		default:
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

// waitForFeedback polls GET /api/v1/decisions/feedback:{runID} until the
// operator submits feedback via the Decision Hub or a 10-minute timeout expires.
func (r *Remediator) waitForFeedback(ctx context.Context, runID string) {
	if r.cfg.GatewayURL == "" || runID == "" {
		return
	}
	pollInterval := 15 * time.Second
	deadline := time.Now().Add(10 * time.Minute)
	fmt.Printf("  Waiting for feedback (10m timeout, Ctrl+C to skip)...\n")
	decisionURL := strings.TrimSuffix(r.cfg.GatewayURL, "/") + "/api/v1/decisions/feedback:" + runID
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
		if time.Now().After(deadline) {
			fmt.Printf("  Feedback timeout — continuing without feedback.\n\n")
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, decisionURL, nil)
		if err != nil {
			return
		}
		if r.cfg.GatewayAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			slog.Warn("faulttest: feedback poll failed, retrying", "run_id", runID, "err", err)
			continue
		}
		var d struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&d)
		resp.Body.Close()
		if d.Status != "" && d.Status != "pending" {
			fmt.Printf("  Feedback received — thank you.\n\n")
			return
		}
	}
}

// submitFeedback prompts the operator for diagnosis and (optionally) remediation
// quality feedback and POSTs to the gateway. Silent no-op when non-interactive
// or gateway not configured. triageRunID anchors both feedback records so they
// are joinable with run_evaluation without a cross-table join. remRunID is only
// used for logging; pass "" when remediation did not run.
func (r *Remediator) submitFeedback(ctx context.Context, triageRunID, remRunID string, diagReport map[string]any) {
	if r.cfg.GatewayURL == "" || triageRunID == "" {
		return
	}
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return
	}
	defer tty.Close()
	reader := bufio.NewReader(tty)

	fmt.Println()
	fmt.Println("  Post-incident feedback (optional, but recommended):")
	fmt.Print("    Was the triage diagnosis correct? [y/n/skip]: ")
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" || answer == "skip" || answer == "s" {
		return
	}
	var diagCorrect *bool
	switch answer {
	case "y", "yes":
		v := true
		diagCorrect = &v
	case "n", "no":
		v := false
		diagCorrect = &v
	default:
		return
	}

	// Suggest the primary hypothesis text as the default root cause.
	defaultRootCause := primaryHypothesisText(diagReport)

	prompt := "    Root cause"
	if defaultRootCause != "" {
		prompt += fmt.Sprintf(" (Enter to confirm: %q)", defaultRootCause)
	}
	fmt.Print(prompt + ": ")
	causeInput, _ := reader.ReadString('\n')
	causeInput = strings.TrimSpace(causeInput)
	if causeInput == "" {
		causeInput = defaultRootCause
	}

	r.postFeedback(ctx, triageRunID, "triage", "post_incident", diagCorrect, causeInput, "")

	// Remediation approach feedback — only when remediation ran.
	if remRunID != "" {
		fmt.Print("    Was the remediation approach appropriate? [y/n/skip]: ")
		remAnswer, _ := reader.ReadString('\n')
		remAnswer = strings.TrimSpace(strings.ToLower(remAnswer))
		switch remAnswer {
		case "y", "yes":
			v := true
			fmt.Print("    Remediation approach notes (optional): ")
			remNotes, _ := reader.ReadString('\n')
			r.postFeedback(ctx, triageRunID, "remediation", "post_incident", &v, strings.TrimSpace(remNotes), "")
		case "n", "no":
			v := false
			fmt.Print("    Notes on remediation approach (optional): ")
			notes, _ := reader.ReadString('\n')
			r.postFeedback(ctx, triageRunID, "remediation", "post_incident", &v, strings.TrimSpace(notes), "")
		}
	}
}

// postFeedback POSTs a single feedback record to the gateway. source is
// "auto_judge" for machine-inferred verdicts (force mode) or "" / "human" for
// operator-provided verdicts; the gateway stores it in feedback_source.
func (r *Remediator) postFeedback(ctx context.Context, runID, feedbackType, feedbackTime string, verdictCorrect *bool, notes, source string) {
	fb := map[string]any{
		"run_id":          runID,
		"verdict_correct": verdictCorrect,
		"feedback_type":   feedbackType,
		"feedback_time":   feedbackTime,
	}
	if notes != "" {
		fb["verdict_notes"] = notes
	}
	if source != "" {
		fb["feedback_source"] = source
	}
	if r.cfg.OperatorID != "" {
		fb["operator"] = r.cfg.OperatorID
	}
	body, _ := json.Marshal(fb)
	url := strings.TrimSuffix(r.cfg.GatewayURL, "/") + "/api/v1/fleet/playbook-runs/" + runID + "/feedback"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if r.cfg.GatewayAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.cfg.GatewayAPIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("faulttest: failed to submit feedback", "run_id", runID, "feedback_type", feedbackType, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Printf("  Feedback submitted (%s/%s run_id=%s)\n", feedbackType, feedbackTime, runID)
	}
}

// primaryHypothesisText extracts the primary hypothesis text from a raw diagnostic
// report map (as received from the gateway JSON response).
func primaryHypothesisText(diagReport map[string]any) string {
	if diagReport == nil {
		return ""
	}
	hyps, _ := diagReport["hypotheses"].([]any)
	for _, h := range hyps {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if isPrimary, _ := hm["is_primary"].(bool); isPrimary {
			text, _ := hm["text"].(string)
			return text
		}
	}
	return ""
}

// printIncidentSummary prints a compact incident narrative link after a successful gate+recovery.
func printIncidentSummary(resp testutil.AgentResponse, recoverySecs float64, gatewayURL string) {
	if resp.RunID == "" {
		return
	}
	fmt.Printf("Incident %s — resolved in %.1fs\n", resp.RunID, recoverySecs)
	if diag := primaryHypothesisText(resp.DiagnosticReport); diag != "" {
		fmt.Printf("  Diagnosis  : %s\n", diag)
	}
	target := resp.TransitionTarget
	if target == "" {
		target = resp.EscalationTarget
	}
	if target != "" {
		fmt.Printf("  Remediation: %s\n", target)
	}
	if gatewayURL != "" {
		base := strings.TrimSuffix(gatewayURL, "/")
		fmt.Printf("  Narrative  : GET %s/api/v1/incidents/%s\n", base, resp.RunID)
	}
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

