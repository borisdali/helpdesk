package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

func gateDecisionStore(t *testing.T) (*audit.Store, *Gateway) {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "gate.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, &Gateway{auditor: audit.NewGatewayAuditor(store)}
}

func gateDecisions(t *testing.T, store *audit.Store, runID string) []audit.RecoveryDecision {
	t.Helper()
	events, err := store.Query(context.Background(), audit.QueryOptions{
		TraceIDPrefix: runID, EventTypes: []audit.EventType{audit.EventTypeRecoveryDecision}})
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.RecoveryDecision
	for _, ev := range events {
		if ev.RecoveryDecision == nil {
			t.Fatalf("an event with no payload: %+v", ev)
		}
		out = append(out, *ev.RecoveryDecision)
	}
	return out
}

// The gate stage names who decided, what they approved and what they
// acknowledged, separately.
func TestRecordRecoveryGateDecision_Approved(t *testing.T) {
	store, g := gateDecisionStore(t)
	run := &audit.PlaybookRun{RunID: "plr_gate_rec01", ConnectionString: "pgbackrest-db", RecoveryTarget: "time=2026-10-27T14:05:00Z"}
	ap, status, msg := recoveryApprovalFromGate(ProceedEscalationRequest{
		RecoveryTarget: &RecoveryTargetSpec{"time", "2026-10-27T14:05:00Z"}, PlanDigest: "0123456789abcdef",
		AcknowledgeDataLoss: true, AllowStopServing: true, AcknowledgeObjectiveBreach: true,
	}, run.RecoveryTarget, "alice")
	if status != 0 {
		t.Fatalf("%d %s", status, msg)
	}
	g.recordRecoveryGateDecision(context.Background(), run, ap, "alice", "")

	got := gateDecisions(t, store, run.RunID)
	if len(got) != 1 {
		t.Fatalf("decisions = %+v", got)
	}
	d := got[0]
	if d.Stage != audit.RecoveryStageGate || d.Decision != audit.RecoveryDecisionApproved || d.DecidedBy != "alice" ||
		d.RunID != run.RunID || d.Target != "pgbackrest-db" || d.RecoveryTarget != "time=2026-10-27T14:05:00Z" || d.PlanDigest != "0123456789abcdef" {
		t.Errorf("decision = %+v", d)
	}
	if a := d.Approval; a == nil || a.ApprovedBy != "alice" || !a.AcknowledgedDataLoss || !a.AllowedStopServing || !a.AcknowledgedBreach {
		t.Errorf("approval = %+v", d.Approval)
	}
	// Correlated by the run, like gate_acknowledged, so it is not part of the
	// triage journey's own timeline.
	events, _ := store.Query(context.Background(), audit.QueryOptions{TraceIDPrefix: run.RunID, EventTypes: []audit.EventType{audit.EventTypeRecoveryDecision}})
	if len(events) != 1 || events[0].TraceID != run.RunID {
		t.Errorf("trace = %+v", events)
	}
}

// What an approval did NOT acknowledge is recorded as not acknowledged, not
// left out: the absence is the fact.
func TestRecordRecoveryGateDecision_ApprovalThatAcknowledgedLess(t *testing.T) {
	store, g := gateDecisionStore(t)
	run := &audit.PlaybookRun{RunID: "plr_gate_rec02", RecoveryTarget: "time=2026-10-27T14:05:00Z"}
	ap, _, _ := recoveryApprovalFromGate(ProceedEscalationRequest{
		RecoveryTarget: &RecoveryTargetSpec{"time", "2026-10-27T14:05:00Z"}, PlanDigest: "0123456789abcdef", AcknowledgeDataLoss: true,
	}, "", "bob")
	g.recordRecoveryGateDecision(context.Background(), run, ap, "bob", "")
	d := gateDecisions(t, store, run.RunID)[0]
	if a := d.Approval; a == nil || !a.AcknowledgedDataLoss || a.AllowedStopServing || a.AcknowledgedBreach {
		t.Errorf("approval = %+v", d.Approval)
	}
}

func TestRecordRecoveryGateDecision_Denied(t *testing.T) {
	store, g := gateDecisionStore(t)
	run := &audit.PlaybookRun{RunID: "plr_gate_rec03", RecoveryTarget: "time=2026-10-27T14:05:00Z"}
	g.recordRecoveryGateDecision(context.Background(), run, nil, "carol", "wrong target")
	d := gateDecisions(t, store, run.RunID)[0]
	if d.Decision != audit.RecoveryDecisionDenied || d.DecidedBy != "carol" || d.Reason != "wrong target" ||
		d.Approval != nil || d.RecoveryTarget != "time=2026-10-27T14:05:00Z" {
		t.Errorf("decision = %+v", d)
	}
}

// The run's own connection string carries the real password (the faulttest
// harness passes a full DSN), and it must not reach the audit trail.
func TestRecordRecoveryGateDecision_NeverRecordsAPassword(t *testing.T) {
	store, g := gateDecisionStore(t)
	run := &audit.PlaybookRun{
		RunID:            "plr_gate_rec04",
		ConnectionString: "host=localhost port=15435 dbname=testdb user=postgres password=testpass",
		RecoveryTarget:   "time=2026-10-27T14:05:00Z",
	}
	ap, _, _ := recoveryApprovalFromGate(ProceedEscalationRequest{
		RecoveryTarget: &RecoveryTargetSpec{"time", "2026-10-27T14:05:00Z"}, PlanDigest: "0123456789abcdef", AcknowledgeDataLoss: true,
	}, "", "alice")
	g.recordRecoveryGateDecision(context.Background(), run, ap, "alice", "")
	g.recordRecoveryGateDecision(context.Background(), run, nil, "alice", "could not reach password=testpass")

	events, err := store.Query(context.Background(), audit.QueryOptions{TraceIDPrefix: run.RunID})
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %d, %v", len(events), err)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "testpass") {
		t.Errorf("the password is in the audit trail: %s", raw)
	}
	if got := gateDecisions(t, store, run.RunID); len(got) != 2 || !strings.Contains(got[0].Target+got[1].Target, "user=postgres password=***") {
		t.Errorf("the target should keep what is not secret: %+v", got)
	}
}

func TestRecordRecoveryGateDecision_NoAuditorIsNotAPanic(t *testing.T) {
	(&Gateway{}).recordRecoveryGateDecision(context.Background(), &audit.PlaybookRun{RunID: "plr_x"}, nil, "x", "")
}

// Through the real handler, for both ways a gate can be resolved.
func TestHandleProceedEscalation_PITR_RecordsTheGateDecision(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	approved := strings.Replace(pitrApprovalBody, `"acknowledge_data_loss":true`,
		`"acknowledge_data_loss":true,"allow_stop_serving":true,"acknowledge_objective_breach":true`, 1)

	run := gatedPITRRun("time=2026-10-27T14:05:00Z")
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	store, withAuditor := gateDecisionStore(t)
	gw.auditor = withAuditor.auditor
	addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")
	if rec := postProceedEscalation(t, gw, run.RunID, approved); rec.Code != http.StatusOK {
		t.Fatalf("approve: got %d; %s", rec.Code, rec.Body.String())
	}
	got := gateDecisions(t, store, run.RunID)
	if len(got) != 1 || got[0].Decision != audit.RecoveryDecisionApproved || got[0].DecidedBy != "alice" ||
		got[0].Approval == nil || !got[0].Approval.AllowedStopServing || !got[0].Approval.AcknowledgedBreach {
		t.Fatalf("approved gate: %+v", got)
	}

	// A denial of the same kind of gate is a decision too.
	run2 := gatedPITRRun("time=2026-10-27T14:05:00Z")
	run2.RunID = "plr_pitrgate02"
	auditd2 := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run2.RunID: run2},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw2 := makePlaybookRunGateway(auditd2.URL, nil)
	store2, withAuditor2 := gateDecisionStore(t)
	gw2.auditor = withAuditor2.auditor
	deny := `{"resolution":"denied","resolved_by":"bob","reason":"restore to the wrong moment"}`
	if rec := postProceedEscalation(t, gw2, run2.RunID, deny); rec.Code != http.StatusOK {
		t.Fatalf("deny: got %d; %s", rec.Code, rec.Body.String())
	}
	got = gateDecisions(t, store2, run2.RunID)
	if len(got) != 1 || got[0].Decision != audit.RecoveryDecisionDenied || got[0].DecidedBy != "bob" || got[0].Reason != "restore to the wrong moment" {
		t.Fatalf("denied gate: %+v", got)
	}
}

// ── the outcome stage ────────────────────────────────────────────────────────

// outcomeGateway is a gateway whose auditd knows runs, and whose audit store
// records what the gateway writes.
func outcomeGateway(t *testing.T, runs map[string]*audit.PlaybookRun) (*Gateway, *audit.Store) {
	t.Helper()
	triage, remediate := pitrPlaybooks()
	auditd := newRecoveryAuditd(t, runs,
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	store, withAuditor := gateDecisionStore(t)
	gw.auditor = withAuditor.auditor
	return gw, store
}

func remediationRun() *audit.PlaybookRun {
	start := time.Date(2026, 11, 4, 14, 3, 0, 0, time.UTC)
	return &audit.PlaybookRun{
		RunID: "plr_out_r1", EntryRunID: "plr_out_t1", SeriesID: pitrRemediateSeries,
		// A full DSN, as the harness passes: its password must not be recorded.
		ConnectionString: "host=localhost port=15435 dbname=testdb user=postgres password=testpass",
		RecoveryTarget:   "time=2026-11-04T01:39:07Z",
		StartedAt:        start, CompletedAt: start.Add(41*time.Minute + 12*time.Second),
	}
}

func TestRecordRecoveryOutcome_RecordsHowItEndedAndHowLong(t *testing.T) {
	run := remediationRun()
	gw, store := outcomeGateway(t, map[string]*audit.PlaybookRun{run.RunID: run})

	gw.recordPlaybookRunComplete(context.Background(), run.RunID, audit.OutcomeResolved, "", "", "restored", "", "", nil, false, "")

	// Keyed to the incident's entry run, so the narrative finds it.
	got := gateDecisions(t, store, "plr_out_t1")
	if len(got) != 1 {
		t.Fatalf("outcome decisions = %+v", got)
	}
	d := got[0]
	if d.Stage != audit.RecoveryStageOutcome || d.Decision != audit.OutcomeResolved || d.RunID != run.RunID ||
		d.RecoveryTarget != "time=2026-11-04T01:39:07Z" {
		t.Errorf("decision = %+v", d)
	}
	if d.DurationSeconds == nil || *d.DurationSeconds != 41*60+12 {
		t.Errorf("duration = %v, want 2472s", d.DurationSeconds)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "testpass") {
		t.Errorf("the password is in the outcome: %s", raw)
	}
}

func TestRecordRecoveryOutcome_EveryTerminalOutcomeIsRecordedAsItIs(t *testing.T) {
	for _, outcome := range []string{audit.OutcomeResolved, audit.OutcomeEscalated, audit.OutcomeAbandoned, audit.OutcomeDiagnosed} {
		run := remediationRun()
		gw, store := outcomeGateway(t, map[string]*audit.PlaybookRun{run.RunID: run})
		gw.recordPlaybookRunComplete(context.Background(), run.RunID, outcome, "", "", "", "", "", nil, false, "")
		got := gateDecisions(t, store, "plr_out_t1")
		if len(got) != 1 || got[0].Decision != outcome {
			t.Errorf("%s: decisions = %+v", outcome, got)
		}
	}
}

// Only the hop that carries out a point-in-time restore, and only when it
// ends; nothing else leaves a recovery outcome.
func TestRecordRecoveryOutcome_OnlyForThePITRRemediationWhenItEnds(t *testing.T) {
	other := remediationRun()
	other.SeriesID = "pbs_db_restart_action"
	gw, store := outcomeGateway(t, map[string]*audit.PlaybookRun{other.RunID: other})
	gw.recordPlaybookRunComplete(context.Background(), other.RunID, audit.OutcomeResolved, "", "", "", "", "", nil, false, "")
	if got := gateDecisions(t, store, "plr_out_t1"); len(got) != 0 {
		t.Errorf("another playbook left a recovery outcome: %+v", got)
	}

	run := remediationRun()
	gw, store = outcomeGateway(t, map[string]*audit.PlaybookRun{run.RunID: run})
	gw.recordPlaybookRunComplete(context.Background(), run.RunID, audit.OutcomeGatePending, "", "", "", "", "", nil, false, "")
	if got := gateDecisions(t, store, "plr_out_t1"); len(got) != 0 {
		t.Errorf("a run that is not finished left a recovery outcome: %+v", got)
	}
}

func TestRecordRecoveryOutcome_EdgeCases(t *testing.T) {
	// No entry run recorded: keyed to the run itself.
	run := remediationRun()
	run.EntryRunID = ""
	gw, store := outcomeGateway(t, map[string]*audit.PlaybookRun{run.RunID: run})
	gw.recordPlaybookRunComplete(context.Background(), run.RunID, audit.OutcomeResolved, "", "", "", "", "", nil, false, "")
	if got := gateDecisions(t, store, run.RunID); len(got) != 1 {
		t.Errorf("keyed to its own run: %+v", got)
	}

	// Not completed yet when read back: the duration runs to now, never negative.
	run = remediationRun()
	run.CompletedAt = time.Time{}
	run.StartedAt = time.Now().UTC().Add(-90 * time.Second)
	gw, store = outcomeGateway(t, map[string]*audit.PlaybookRun{run.RunID: run})
	gw.recordPlaybookRunComplete(context.Background(), run.RunID, audit.OutcomeResolved, "", "", "", "", "", nil, false, "")
	if got := gateDecisions(t, store, "plr_out_t1"); len(got) != 1 || got[0].DurationSeconds == nil || *got[0].DurationSeconds < 89 || *got[0].DurationSeconds > 100 {
		t.Errorf("duration = %+v", got)
	}

	// A start time after the completion time (clock trouble) records no duration
	// rather than a negative one.
	run = remediationRun()
	run.StartedAt = run.CompletedAt.Add(time.Minute)
	gw, store = outcomeGateway(t, map[string]*audit.PlaybookRun{run.RunID: run})
	gw.recordPlaybookRunComplete(context.Background(), run.RunID, audit.OutcomeResolved, "", "", "", "", "", nil, false, "")
	if got := gateDecisions(t, store, "plr_out_t1"); len(got) != 1 || got[0].DurationSeconds != nil {
		t.Errorf("a negative duration was recorded: %+v", got)
	}

	// No audit store: nothing to record and nothing to panic over.
	gw.auditor = nil
	gw.recordRecoveryOutcome(context.Background(), run.RunID, audit.OutcomeResolved)
}

// Through the real approval flow, not a direct call: approving the gate starts
// the remediation hop, the hop completes, and its outcome is on record, once,
// under the incident's entry run.
func TestHandleProceedEscalation_PITR_RecordsTheOutcomeOfTheRemediationHop(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	gated := gatedPITRRun("time=2026-10-27T14:05:00Z")
	// The fake auditd names the run it creates plr_rec1; give it a history as
	// auditd would, so the gateway can read it back when it completes.
	hop := &audit.PlaybookRun{
		RunID: "plr_rec1", EntryRunID: gated.RunID, SeriesID: pitrRemediateSeries,
		ConnectionString: gated.ConnectionString, RecoveryTarget: gated.RecoveryTarget,
		StartedAt: time.Now().UTC().Add(-90 * time.Second), CompletedAt: time.Now().UTC(),
	}
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{gated.RunID: gated, hop.RunID: hop},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	store, withAuditor := gateDecisionStore(t)
	gw.auditor = withAuditor.auditor
	addCapturingAgent(t, gw, "pitr_sysadmin_agent", "FINDINGS: restored\n")

	if rec := postProceedEscalation(t, gw, gated.RunID, pitrApprovalBody); rec.Code != http.StatusOK {
		t.Fatalf("approve: got %d; %s", rec.Code, rec.Body.String())
	}
	// A hop's completion is recorded in the background, after the response.
	var outcomes []audit.RecoveryDecision
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		outcomes = outcomes[:0]
		for _, d := range gateDecisions(t, store, gated.RunID) {
			if d.Stage == audit.RecoveryStageOutcome {
				outcomes = append(outcomes, d)
			}
		}
		if len(outcomes) > 0 {
			break
		}
	}
	// Give a duplicate time to show itself.
	time.Sleep(200 * time.Millisecond)
	outcomes = outcomes[:0]
	for _, d := range gateDecisions(t, store, gated.RunID) {
		if d.Stage == audit.RecoveryStageOutcome {
			outcomes = append(outcomes, d)
		}
	}
	if len(outcomes) != 1 {
		t.Fatalf("outcome events = %d: %+v", len(outcomes), gateDecisions(t, store, gated.RunID))
	}
	o := outcomes[0]
	if o.RunID != hop.RunID || o.RecoveryTarget != gated.RecoveryTarget || o.DurationSeconds == nil || *o.DurationSeconds < 80 || *o.DurationSeconds > 100 {
		t.Errorf("outcome = %+v (duration %v)", o, o.DurationSeconds)
	}
}

// A gate that is not about a recovery target leaves no recovery decision.
func TestHandleProceedEscalation_OtherGateLeavesNoRecoveryDecision(t *testing.T) {
	triage, remediate := pitrPlaybooks()
	run := gatedPITRRun("") // no recovery target: not a point-in-time run
	auditd := newRecoveryAuditd(t, map[string]*audit.PlaybookRun{run.RunID: run},
		map[string]*audit.Playbook{triage.PlaybookID: triage}, map[string]*audit.Playbook{remediate.SeriesID: remediate})
	gw := makePlaybookRunGateway(auditd.URL, nil)
	store, withAuditor := gateDecisionStore(t)
	gw.auditor = withAuditor.auditor
	if rec := postProceedEscalation(t, gw, run.RunID, `{"resolution":"denied","resolved_by":"bob"}`); rec.Code != http.StatusOK {
		t.Fatalf("got %d; %s", rec.Code, rec.Body.String())
	}
	if got := gateDecisions(t, store, run.RunID); len(got) != 0 {
		t.Errorf("a gate that is not about a recovery target recorded %+v", got)
	}
}
