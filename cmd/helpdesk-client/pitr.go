package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/term"

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
	AckBreach bool
	// Justification is why the approval is given (sent as the gate's reason) and
	// Ticket the reference it is given under. Both optional here; the gateway can
	// be set to require a justification for a breach acknowledgement.
	Justification string
	Ticket        string
	ResolvedBy    string
	ApprovalMode  string

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
	Justification, Ticket          string
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
		Justification: f.Justification, Ticket: f.Ticket,
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
	// The justification is the gate's own "reason" field; the ticket is its own.
	if j := strings.TrimSpace(o.Justification); j != "" {
		body["reason"] = j
	}
	if t := strings.TrimSpace(o.Ticket); t != "" {
		body["ticket"] = t
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
		// Delay is what putting the decision off costs against the budget: the
		// estimated latest start that stays within it, or why there is none.
		Delay *struct {
			State  string `json:"state"`
			Detail string `json:"detail"`
		} `json:"delay"`
		// RecoveryEstimate is how long this database's restores have taken, from
		// real incidents, and how that stands against its declared rto.
		RecoveryEstimate *struct {
			Status      string `json:"status"`
			Basis       string `json:"basis"`
			VsRTO       string `json:"vs_rto"`
			VsRTODetail string `json:"vs_rto_detail"`
			// PlanningSource is where the planned duration comes from: "restores",
			// or "drills" when there are too few real restores. Drills are restores
			// on a copy the operator brought, shown beside the real ones.
			PlanningSource string `json:"planning_source"`
			Drills         *struct {
				Status      string `json:"status"`
				Samples     int    `json:"samples"`
				Basis       string `json:"basis"`
				VsRTO       string `json:"vs_rto"`
				VsRTODetail string `json:"vs_rto_detail"`
			} `json:"drills"`
		} `json:"recovery_estimate"`
	} `json:"availability_budget"`
	// DataDelay is what putting the decision off does to the data the restore
	// discards: it grows while a serving instance keeps taking writes.
	DataDelay *struct {
		State  string `json:"state"`
		Detail string `json:"detail"`
	} `json:"data_delay"`
	// DrillCurrency is how recently this database's restore was drilled, for a
	// database that has an objective. A reminder: it holds nothing back.
	DrillCurrency *struct {
		State   string `json:"state"`
		Overdue bool   `json:"overdue"`
		Detail  string `json:"detail"`
	} `json:"drill_currency"`
}

// pitrWrapWidth is the line width the plan is wrapped to; 0 leaves lines as they
// are. The plan command sets it from the terminal (or 100 when it is not one).
var pitrWrapWidth int

// pitrIndent is the width of a label and its ": ", where wrapped lines resume.
const pitrIndent = 21

// field writes "label: text" and, when a width is set, wraps the text at word
// boundaries so that continuation lines start under the text, not under the label.
func field(b *strings.Builder, label, text string) {
	b.WriteString(label + ": ")
	words := strings.Fields(text)
	col := pitrIndent
	for i, w := range words {
		if i > 0 {
			if pitrWrapWidth > 0 && col+1+len(w) > pitrWrapWidth {
				b.WriteString("\n" + strings.Repeat(" ", pitrIndent))
				col = pitrIndent
			} else {
				b.WriteString(" ")
				col++
			}
		}
		b.WriteString(w)
		col += len(w)
	}
	b.WriteString("\n")
}

// formatPITRPlan renders the plan for a person, ending with the exact command
// to approve it when it is feasible.
func formatPITRPlan(p pitrPlan, o pitrOpts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan status        : %s\n", p.Status)
	if p.Detail != "" {
		field(&b, "Detail             ", p.Detail)
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
		field(&b, "Will be discarded  ", p.DataLoss)
	}
	if e := p.DataLossEstimate; e != nil && e.Summary != "" {
		field(&b, "Estimated loss     ", e.Summary)
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
			field(&b, label, o.Status+": "+o.Detail)
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
		field(&b, label, bd.Status+": "+bd.Detail)
		switch bd.Gate {
		case "acknowledge":
			fmt.Fprintf(&b, "Approving needs    : --acknowledge-objective-breach (this database's availability enforcement is %s)\n", bd.Enforcement)
		case "refuse":
			fmt.Fprintf(&b, "Approving          : REFUSED (this database's availability enforcement is refuse, and the instance is serving)\n")
		}
		if bd.GateNote != "" {
			field(&b, "Note               ", bd.GateNote)
		}
	}
	// How long this database's restores have taken, and against its declared rto.
	// Shown with or without an SLO: it is what has been demonstrated, from real
	// incidents, and it says what it rests on. Information; it holds nothing.
	if bd := p.AvailabilityBudget; bd != nil && bd.RecoveryEstimate != nil && bd.RecoveryEstimate.Basis != "" {
		e := bd.RecoveryEstimate
		field(&b, "Past restores      ", e.Basis)
		switch e.VsRTO {
		case "exceeds":
			field(&b, "RTO CHECK (!)      ", "exceeds: "+e.VsRTODetail)
		case "within":
			field(&b, "RTO check          ", "within: "+e.VsRTODetail)
		}
		// Restore drills, beside the real restores and never mixed with them. A
		// drill is a restore on a copy, so what plans the restore from drills is
		// said to be that.
		if d := e.Drills; d != nil && d.Basis != "" {
			text := d.Basis
			if e.PlanningSource == "drills" {
				text += ". The planned duration comes from these: a restore demonstrated on a copy, not on this server"
			}
			field(&b, "Restore drills     ", text)
			// A drill that took longer than the rto says the rto is not met, even
			// where no real restore has shown it.
			if d.VsRTO == "exceeds" && e.VsRTO != "exceeds" {
				field(&b, "RTO CHECK (!)      ", "exceeds, by the drills: "+d.VsRTODetail)
			}
		}
	}
	// Whether a restore drill is due. A reminder, apart from the checks above: it
	// holds nothing back, and says how to put it right when it is overdue.
	if d := p.DrillCurrency; d != nil && d.Detail != "" && d.State != "off" {
		if d.Overdue {
			field(&b, "DRILL OVERDUE (!)  ", d.Detail+". Record one with helpdesk-client --drill-record (see docs/RESTORE_OPS.md)")
		} else {
			field(&b, "Drill reminder     ", d.Detail)
		}
	}
	// What putting the decision off costs. Information, not a deadline: the restore
	// measures again just before it stops anything and decides on that.
	if bd := p.AvailabilityBudget; bd != nil && bd.Delay != nil && bd.Delay.Detail != "" {
		field(&b, "Waiting, budget    ", bd.Delay.Detail)
	}
	if d := p.DataDelay; d != nil && d.Detail != "" {
		field(&b, "Waiting, data      ", d.Detail)
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
	pitrWrapWidth = 100
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w >= 60 {
		pitrWrapWidth = w - 1
	}
	fmt.Print(formatPITRPlan(plan, o))
	return nil
}
