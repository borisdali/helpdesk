package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"helpdesk/internal/client"
)

// Point-in-time recovery from the operator's seat (v0.33).
//
// Two one-shot commands, mirroring the two things an operator does at a
// recovery_target_required gate:
//
//	--pitr-plan     ask the sysadmin agent for the plan (read-only) and print
//	                the facts a person decides on, with the plan digest.
//	--pitr-approve  approve the gate with the target, that digest and the
//	                acknowledgements. Nothing is guessed or defaulted: every
//	                field a restore depends on is required, and is checked here
//	                before anything is sent (the gateway checks it again).

// pitrOpts are the flag values of both commands.
type pitrOpts struct {
	RunID       string // approve: the triage run holding the gate
	TargetType  string // "time" or "lsn"
	Target      string
	PlanDigest  string
	AckDataLoss bool
	AllowStop   bool
	// AckBreach acknowledges that the loss exceeds the database's recovery
	// objective. Only a database whose enforcement is acknowledge needs it, and
	// only when the loss measured at the stop does.
	AckBreach    bool
	ResolvedBy   string
	ApprovalMode string

	// plan only
	SysadminURL string
	SysadminKey string
	Server      string // infrastructure alias the agent takes as its target
	Stanza      string
}

// pitrFlagValues are the parsed command-line values the two commands read.
type pitrFlagValues struct {
	ApproveRunID                   string
	TargetType, Target, PlanDigest string
	AckDataLoss, AllowStop         bool
	AckBreach                      bool
	User, SysadminURL, SysadminKey string
	Server, Stanza                 string
}

// newPITROpts maps the flags to the options. It is the one place a flag is tied
// to a field, so it is the one place a wiring mistake can hide. The approval
// mode is fixed: the operator's decision is the gate itself, and the PITR
// restore has no per-step prompts to answer.
func newPITROpts(f pitrFlagValues) pitrOpts {
	return pitrOpts{
		RunID: f.ApproveRunID, TargetType: f.TargetType, Target: f.Target,
		PlanDigest: f.PlanDigest, AckDataLoss: f.AckDataLoss, AllowStop: f.AllowStop, AckBreach: f.AckBreach,
		ResolvedBy: f.User, ApprovalMode: "auto",
		SysadminURL: f.SysadminURL, SysadminKey: f.SysadminKey, Server: f.Server, Stanza: f.Stanza,
	}
}

var planDigestRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (o pitrOpts) targetSpec() (map[string]string, error) {
	if o.TargetType != "time" && o.TargetType != "lsn" {
		return nil, fmt.Errorf("--recovery-target-type must be \"time\" or \"lsn\"")
	}
	if strings.TrimSpace(o.Target) == "" {
		return nil, fmt.Errorf("--recovery-target is required")
	}
	return map[string]string{"type": o.TargetType, "value": strings.TrimSpace(o.Target)}, nil
}

// approvalBody validates an approval and returns the gateway request body.
func (o pitrOpts) approvalBody(purpose string) ([]byte, error) {
	if o.RunID == "" {
		return nil, fmt.Errorf("a run ID is required")
	}
	target, err := o.targetSpec()
	if err != nil {
		return nil, err
	}
	if !planDigestRE.MatchString(o.PlanDigest) {
		return nil, fmt.Errorf("--plan-digest must be the 16-character plan_digest printed by --pitr-plan")
	}
	if !o.AckDataLoss {
		return nil, fmt.Errorf("--acknowledge-data-loss is required: everything committed after the target is discarded")
	}
	body := map[string]any{
		"resolution":            "approved",
		"approval_mode":         o.ApprovalMode,
		"recovery_target":       target,
		"plan_digest":           o.PlanDigest,
		"acknowledge_data_loss": true,
		"allow_stop_serving":    o.AllowStop,
		// Sent only when given: it is a separate acknowledgement, and most
		// approvals have nothing to acknowledge.
	}
	if o.AckBreach {
		body["acknowledge_objective_breach"] = true
	}
	if o.ResolvedBy != "" {
		body["resolved_by"] = o.ResolvedBy
	}
	if purpose != "" {
		body["purpose"] = purpose
	}
	return json.Marshal(body)
}

// runPITRApprove approves the point-in-time gate of a triage run.
func runPITRApprove(ctx context.Context, cfg client.Config, o pitrOpts) error {
	body, err := o.approvalBody(cfg.Purpose)
	if err != nil {
		return err
	}
	url := strings.TrimRight(cfg.GatewayURL, "/") + "/api/v1/fleet/playbook-runs/" + o.RunID + "/proceed-escalation"
	resp, err := doAuditRequest(ctx, cfg, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("the gateway refused the approval (HTTP %d): %s", resp.StatusCode, gatewayError(raw))
	}
	fmt.Printf("=== Gate approved: restore to %s %s (plan %s) ===\n", o.TargetType, o.Target, o.PlanDigest)
	prettyPrint(raw)
	return nil
}

// gatewayError pulls the message out of an error body, or returns it as is.
func gatewayError(raw []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(raw))
}

// pitrPlan is the part of the agent's plan an operator reads.
type pitrPlan struct {
	Status               string `json:"status"`
	Detail               string `json:"detail"`
	BackupLabel          string `json:"backup_label"`
	NormalizedTarget     string `json:"normalized_target"`
	ArchiveEndTime       string `json:"archive_end_time"`
	DataLoss             string `json:"data_loss"`
	InstanceServing      bool   `json:"instance_serving"`
	ArchiveFlushRequired bool   `json:"archive_flush_required"`
	PlanDigest           string `json:"plan_digest"`
	// DataLossEstimate is how much the restore discards, as far as it is
	// measured without reading WAL.
	DataLossEstimate *struct {
		Summary    string   `json:"summary"`
		AsOf       string   `json:"as_of"`
		Unmeasured []string `json:"unmeasured"`
		// Objective sets the estimate against the database's recovery point
		// objective.
		Objective *struct {
			Status string `json:"status"`
			Detail string `json:"detail"`
			// Gate is what approving will demand given this verdict and the
			// database's enforcement: "", "acknowledge" or "refuse".
			Gate        string `json:"gate"`
			Enforcement string `json:"enforcement"`
		} `json:"objective"`
	} `json:"data_loss_estimate"`
	// AvailabilityBudget sets the downtime the restore costs against what is
	// left of the database's downtime budget, and what approving will demand
	// because of it.
	AvailabilityBudget *struct {
		Status           string `json:"status"`
		Detail           string `json:"detail"`
		Gate             string `json:"gate"`
		GateNote         string `json:"gate_note"`
		Enforcement      string `json:"enforcement"`
		RemainingSeconds *int64 `json:"remaining_seconds"`
		DurationBasis    string `json:"duration_basis"`
		// RecoveryEstimate is how long this database's restores have taken, from
		// real incidents, and how that stands against its declared rto.
		RecoveryEstimate *struct {
			Status      string `json:"status"`
			Basis       string `json:"basis"`
			VsRTO       string `json:"vs_rto"`
			VsRTODetail string `json:"vs_rto_detail"`
		} `json:"recovery_estimate"`
	} `json:"availability_budget"`
}

// formatPITRPlan renders the plan for a person, ending with the exact command
// to approve it when it is feasible.
func formatPITRPlan(p pitrPlan, o pitrOpts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan status        : %s\n", p.Status)
	if p.Detail != "" {
		fmt.Fprintf(&b, "Detail             : %s\n", p.Detail)
	}
	if p.BackupLabel != "" {
		fmt.Fprintf(&b, "Backup to restore  : %s\n", p.BackupLabel)
	}
	if p.NormalizedTarget != "" {
		fmt.Fprintf(&b, "Recovery target    : %s\n", p.NormalizedTarget)
	}
	if p.ArchiveEndTime != "" {
		fmt.Fprintf(&b, "Archive ends       : %s\n", p.ArchiveEndTime)
	}
	if p.DataLoss != "" {
		fmt.Fprintf(&b, "Will be discarded  : %s\n", p.DataLoss)
	}
	if e := p.DataLossEstimate; e != nil && e.Summary != "" {
		fmt.Fprintf(&b, "Estimated loss     : %s\n", e.Summary)
		if len(e.Unmeasured) > 0 {
			fmt.Fprintf(&b, "Not counted        : %s\n", strings.Join(e.Unmeasured, "; "))
		}
		if o := e.Objective; o != nil {
			// Say it loudly when the loss is, or may be, more than the database
			// is supposed to lose: a person approving should not miss it.
			label := "RPO check          "
			if o.Status == "exceeds" || o.Status == "may_exceed" || o.Gate != "" {
				label = "RPO CHECK (!)      "
			}
			fmt.Fprintf(&b, "%s: %s: %s\n", label, o.Status, o.Detail)
			switch o.Gate {
			case "acknowledge":
				fmt.Fprintf(&b, "Approving needs    : --acknowledge-objective-breach (this database's enforcement is %s)\n", o.Enforcement)
			case "refuse":
				fmt.Fprintf(&b, "Approving          : REFUSED (this database's enforcement is refuse)\n")
			}
		}
	}
	// The restore also costs downtime, which the SLO allows only so much of. Said
	// as loudly as the loss is, and apart from it: the two are different breaches.
	if bd := p.AvailabilityBudget; bd != nil && bd.Status != "" {
		label := "Budget check       "
		if bd.Status == "exceeds" || bd.Status == "may_exceed" || bd.Gate != "" {
			label = "BUDGET CHECK (!)   "
		}
		fmt.Fprintf(&b, "%s: %s: %s\n", label, bd.Status, bd.Detail)
		switch bd.Gate {
		case "acknowledge":
			fmt.Fprintf(&b, "Approving needs    : --acknowledge-objective-breach (this database's availability enforcement is %s)\n", bd.Enforcement)
		case "refuse":
			fmt.Fprintf(&b, "Approving          : REFUSED (this database's availability enforcement is refuse, and the instance is serving)\n")
		}
		if bd.GateNote != "" {
			fmt.Fprintf(&b, "Note               : %s\n", bd.GateNote)
		}
	}
	// How long this database's restores have taken, and against its declared rto.
	// Shown with or without an SLO: it is what has been demonstrated, from real
	// incidents, and it says what it rests on. Information; it holds nothing.
	if bd := p.AvailabilityBudget; bd != nil && bd.RecoveryEstimate != nil && bd.RecoveryEstimate.Basis != "" {
		e := bd.RecoveryEstimate
		fmt.Fprintf(&b, "Past restores      : %s\n", e.Basis)
		switch e.VsRTO {
		case "exceeds":
			fmt.Fprintf(&b, "RTO CHECK (!)      : exceeds: %s\n", e.VsRTODetail)
		case "within":
			fmt.Fprintf(&b, "RTO check          : within: %s\n", e.VsRTODetail)
		}
	}
	state := "down"
	if p.InstanceServing {
		state = "SERVING (the restore must stop it: --allow-stop-serving)"
	}
	fmt.Fprintf(&b, "Instance           : %s\n", state)
	if p.ArchiveFlushRequired {
		fmt.Fprintf(&b, "Archive flush      : required first (the target is in WAL not yet archived)\n")
	}
	if p.Status != "feasible" || p.PlanDigest == "" {
		fmt.Fprintf(&b, "\nNot approvable: do not approve around this plan.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Plan digest        : %s\n\n", p.PlanDigest)
	gate, enforcement := "", ""
	if e := p.DataLossEstimate; e != nil && e.Objective != nil {
		gate, enforcement = e.Objective.Gate, e.Objective.Enforcement
	}
	budgetGate, budgetEnforcement := "", ""
	if bd := p.AvailabilityBudget; bd != nil {
		budgetGate, budgetEnforcement = bd.Gate, bd.Enforcement
	}
	if gate == "refuse" {
		// The restore measures the loss again just before it stops anything and
		// would refuse this one. Offering a command that is known to fail would
		// invite approving around the refusal.
		fmt.Fprintf(&b, "Not approvable as it stands: the loss exceeds the recovery objective and enforcement is %s. "+
			"Plan a later target (less to discard), or change the configured enforcement if the objective is not meant to bind.\n", enforcement)
		return b.String()
	}
	if budgetGate == "refuse" {
		fmt.Fprintf(&b, "Not approvable as it stands: restoring would take a serving instance down when the downtime budget is already spent, and availability enforcement is %s. "+
			"Wait for incidents to age out of the window, or change availability_enforcement if the SLO is not meant to bind.\n", budgetEnforcement)
		return b.String()
	}
	stop := ""
	if p.InstanceServing {
		stop = " --allow-stop-serving"
	}
	// One acknowledgement covers every breach shown above.
	if gate == "acknowledge" || budgetGate == "acknowledge" {
		stop += " --acknowledge-objective-breach"
	}
	fmt.Fprintf(&b, "To approve exactly this plan:\n  helpdesk-client --pitr-approve <run_id> --recovery-target-type %s --recovery-target %q \\\n    --plan-digest %s --acknowledge-data-loss%s\n",
		o.TargetType, o.Target, p.PlanDigest, stop)
	return b.String()
}

// runPITRPlan reads the plan from the sysadmin agent and prints it.
func runPITRPlan(ctx context.Context, cfg client.Config, o pitrOpts) error {
	if o.SysadminURL == "" {
		return fmt.Errorf("--sysadmin-url (or HELPDESK_SYSADMIN_URL) is required: the plan is read from the sysadmin agent")
	}
	if o.Server == "" {
		return fmt.Errorf("--pitr-server is required: the infrastructure alias of the database to restore")
	}
	if _, err := o.targetSpec(); err != nil {
		return err
	}
	stanza := o.Stanza
	if stanza == "" {
		stanza = "main"
	}
	reqBody, _ := json.Marshal(map[string]any{
		"trace_id": fmt.Sprintf("tr_client_pitr_%d", time.Now().UnixNano()),
		"purpose":  "diagnostic",
		"args": map[string]any{
			"target": o.Server, "stanza": stanza,
			"recovery_target_type": o.TargetType, "recovery_target_value": strings.TrimSpace(o.Target),
		},
	})
	url := strings.TrimRight(o.SysadminURL, "/") + "/tool/plan_point_in_time_recovery"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.SysadminKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.SysadminKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("the sysadmin agent returned HTTP %d with an unreadable body: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out.Error != "" || resp.StatusCode >= 300 {
		return fmt.Errorf("the sysadmin agent refused (HTTP %d): %s", resp.StatusCode, out.Error)
	}
	var plan pitrPlan
	if err := json.Unmarshal([]byte(out.Output), &plan); err != nil {
		return fmt.Errorf("the plan is unreadable: %w", err)
	}
	fmt.Print(formatPITRPlan(plan, o))
	return nil
}
