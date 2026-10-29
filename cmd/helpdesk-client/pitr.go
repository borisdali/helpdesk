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
	RunID        string // approve: the triage run holding the gate
	TargetType   string // "time" or "lsn"
	Target       string
	PlanDigest   string
	AckDataLoss  bool
	AllowStop    bool
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
		PlanDigest: f.PlanDigest, AckDataLoss: f.AckDataLoss, AllowStop: f.AllowStop,
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
	stop := ""
	if p.InstanceServing {
		stop = " --allow-stop-serving"
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
