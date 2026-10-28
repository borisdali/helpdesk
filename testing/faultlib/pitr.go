package faultlib

// Point-in-time recovery faults (v0.33).
//
// A PITR fault has a moment that is only known once the fault has been
// injected: the inject script commits the rows to keep, notes the database's
// own clock (and WAL position), then makes the damage. The script prints that
// moment as one line,
//
//	PITR_TARGET time=2026-10-28T17:11:10Z lsn=0/3000028
//
// and the harness plays the human who supplies it: the target goes on the
// triage run's request, and at the gate (with the plan digest the operator
// reviewed and the acknowledgements the gateway insists on) on the approval.
// The model never sees a way to choose it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// PITRMarker starts the line an inject script prints to name its target.
const PITRMarker = "PITR_TARGET"

// PITRSpec marks a fault whose remediation is a point-in-time restore, and
// says how the harness approves it.
type PITRSpec struct {
	// TargetType is "time" (the default) or "lsn": which value of the inject
	// script's PITR_TARGET line is the recovery target.
	TargetType string `yaml:"target_type,omitempty"`
	// AllowStopServing is the gate approval's separate authorization to stop
	// a running instance. A fault that leaves the database up (a dropped
	// table) needs it; one that leaves it down does not.
	AllowStopServing bool `yaml:"allow_stop_serving,omitempty"`
}

// Type is the spec's target type with the default applied.
func (s *PITRSpec) Type() string {
	if s == nil || s.TargetType == "" {
		return "time"
	}
	return s.TargetType
}

// PITRTarget is a recovery target as the gateway takes it.
type PITRTarget struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

var pitrTokenRe = regexp.MustCompile(`^(time|lsn)=(\S+)$`)

// ParsePITRMarker finds the newest PITR_TARGET line in an inject script's
// output and returns the value of targetType. A script that is run twice, or
// that echoes a debug line, still resolves to its last statement.
func ParsePITRMarker(output, targetType string) (PITRTarget, error) {
	if targetType != "time" && targetType != "lsn" {
		return PITRTarget{}, fmt.Errorf("pitr target type %q is not time or lsn", targetType)
	}
	var line string
	for _, l := range strings.Split(output, "\n") {
		l = strings.TrimSpace(l)
		if l == PITRMarker || strings.HasPrefix(l, PITRMarker+" ") {
			line = l
		}
	}
	if line == "" {
		return PITRTarget{}, fmt.Errorf("the inject script printed no %s line: it must print `%s time=<RFC3339> lsn=<X/Y>`", PITRMarker, PITRMarker)
	}
	for _, tok := range strings.Fields(strings.TrimPrefix(line, PITRMarker)) {
		if m := pitrTokenRe.FindStringSubmatch(tok); m != nil && m[1] == targetType {
			return PITRTarget{Type: targetType, Value: m[2]}, nil
		}
	}
	return PITRTarget{}, fmt.Errorf("the %s line %q has no %s= value", PITRMarker, line, targetType)
}

// InjectOutputter is implemented by an injector that keeps the output of its
// last Inject.
type InjectOutputter interface {
	InjectOutput() string
}

type ctxKeyInjectOutput struct{}

// WithInjectOutput carries an inject script's output to the runner.
func WithInjectOutput(ctx context.Context, out string) context.Context {
	return context.WithValue(ctx, ctxKeyInjectOutput{}, out)
}

// InjectOutputFromContext returns the output WithInjectOutput carried.
func InjectOutputFromContext(ctx context.Context) string {
	out, _ := ctx.Value(ctxKeyInjectOutput{}).(string)
	return out
}

// PITRTargetFor is the recovery target of f's current injection, from the
// output the cycle carried in ctx, or false for a fault that is not a PITR one.
func PITRTargetFor(ctx context.Context, f Failure) (PITRTarget, bool, error) {
	if f.PITR == nil {
		return PITRTarget{}, false, nil
	}
	t, err := ParsePITRMarker(InjectOutputFromContext(ctx), f.PITR.Type())
	if err != nil {
		return PITRTarget{}, false, err
	}
	return t, true, nil
}

// ── approving the gate ───────────────────────────────────────────────────────

// pitrState is the current PITR fault's target, set by SetPITR.
type pitrState struct {
	target    PITRTarget
	allowStop bool
	// server is the infrastructure alias the sysadmin agent's tools take as
	// their target.
	server string
}

// SetPITR gives the Remediator the current fault's recovery target, read from
// the output of its inject script, or clears it for a fault that is not a PITR
// one. Called once per fault, after the injection.
func (r *Remediator) SetPITR(f Failure, injectOutput string) error {
	r.pitr = nil
	if f.PITR == nil {
		return nil
	}
	t, err := ParsePITRMarker(injectOutput, f.PITR.Type())
	if err != nil {
		return fmt.Errorf("fault %s: %w", f.ID, err)
	}
	server := f.AgentConnOverride
	if server == "" {
		server = r.cfg.AgentConnStr
	}
	r.pitr = &pitrState{target: t, allowStop: f.PITR.AllowStopServing, server: server}
	return nil
}

// PITRPlanView is the part of plan_point_in_time_recovery's result the harness
// uses. Exported so the agent's own tests can hold its result to these JSON
// names: a renamed field would otherwise read as an empty digest here.
type PITRPlanView struct {
	Status               string `json:"status"`
	Detail               string `json:"detail"`
	PlanDigest           string `json:"plan_digest"`
	InstanceServing      bool   `json:"instance_serving"`
	ArchiveFlushRequired bool   `json:"archive_flush_required"`
}

// fetchPITRPlan asks the sysadmin agent for the plan of the current target, as
// an operator running the plan tool would. Read-only.
func (r *Remediator) fetchPITRPlan(ctx context.Context) (PITRPlanView, error) {
	var plan PITRPlanView
	if r.cfg.SysadminAgentURL == "" {
		return plan, fmt.Errorf("a point-in-time fault needs the sysadmin agent's URL to read the plan the operator approves (FAULTTEST_SYSADMIN_AGENT_URL)")
	}
	body, _ := json.Marshal(map[string]any{
		"trace_id": fmt.Sprintf("tr_faulttest_pitr_%d", time.Now().UnixNano()),
		"purpose":  "diagnostic",
		"args": map[string]any{
			"target": r.pitr.server, "stanza": "main",
			"recovery_target_type": r.pitr.target.Type, "recovery_target_value": r.pitr.target.Value,
		},
	})
	url := strings.TrimSuffix(r.cfg.SysadminAgentURL, "/") + "/tool/plan_point_in_time_recovery"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return plan, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The agent's /tool endpoint has its own key (--sysadmin-api-key); where
	// one key serves both, the gateway's is used.
	key := r.cfg.SysadminAPIKey
	if key == "" {
		key = r.cfg.GatewayAPIKey
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := longClient.Do(req)
	if err != nil {
		return plan, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return plan, fmt.Errorf("plan_point_in_time_recovery: HTTP %d, unparseable body %q", resp.StatusCode, raw)
	}
	if out.Error != "" || resp.StatusCode >= 300 {
		return plan, fmt.Errorf("plan_point_in_time_recovery: HTTP %d: %s", resp.StatusCode, out.Error)
	}
	if err := json.Unmarshal([]byte(out.Output), &plan); err != nil {
		return plan, fmt.Errorf("plan_point_in_time_recovery returned an unreadable plan: %w", err)
	}
	return plan, nil
}

// approvePITR adds a point-in-time approval to a gate approval, for a PITR
// fault. It does nothing for a denial, for a request that already carries one,
// or when the fault is not a PITR one. It refuses (an error, no approval) when
// the plan is not feasible: an operator does not approve that either.
func (r *Remediator) approvePITR(ctx context.Context, req *ProceedEscalationRequest) error {
	if r.pitr == nil || req.Resolution != "approved" || req.RecoveryTarget != nil {
		return nil
	}
	plan, err := r.fetchPITRPlan(ctx)
	if err != nil {
		return err
	}
	if plan.Status != "feasible" || plan.PlanDigest == "" {
		return fmt.Errorf("the point-in-time plan is %q, not feasible, so the gate is not approved: %s", plan.Status, plan.Detail)
	}
	target := r.pitr.target
	req.RecoveryTarget = &target
	req.PlanDigest = plan.PlanDigest
	req.AcknowledgeDataLoss = true
	req.AllowStopServing = r.pitr.allowStop
	slog.Info("point-in-time gate: approving the reviewed plan",
		"target_type", target.Type, "target", target.Value, "plan_digest", plan.PlanDigest,
		"instance_serving", plan.InstanceServing, "archive_flush_required", plan.ArchiveFlushRequired, "allow_stop_serving", req.AllowStopServing)
	return nil
}

// validatePITR checks a fault's pitr block at catalog load, so a mistake
// fails there rather than minutes into a run (or, for a customer's catalog,
// on a database): the target type must be one the gateway takes, the inject
// must be a script whose output the injector keeps and which prints the
// target, and the fault must run through a diagnosis playbook, because the
// gateway's triage request is where the target is sent.
func validatePITR(f Failure) error {
	if f.PITR == nil {
		return nil
	}
	if t := f.PITR.TargetType; t != "" && t != "time" && t != "lsn" {
		return fmt.Errorf("fault %q: pitr.target_type %q is not time or lsn", f.ID, t)
	}
	for what, spec := range map[string]InjectSpec{"inject": f.Inject, "external_inject": f.ExternalInject} {
		if what == "external_inject" && spec.Type == "" {
			continue // unset: the inject is used
		}
		if spec.Type != "shell_exec" {
			return fmt.Errorf("fault %q: a pitr fault's %s must be shell_exec (only its output is kept, and it must print %s), not %q", f.ID, what, PITRMarker, spec.Type)
		}
		if !strings.Contains(spec.ScriptInline+spec.Script, PITRMarker) {
			return fmt.Errorf("fault %q: the %s script never prints a %s line", f.ID, what, PITRMarker)
		}
	}
	if f.DiagnosisPlaybookSeriesID == "" {
		return fmt.Errorf("fault %q: a pitr fault needs diagnosis_playbook_series_id: the target is sent on the gateway's triage request", f.ID)
	}
	return nil
}
