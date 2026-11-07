//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

func i64(n int64) *int64   { return &n }
func u64(n uint64) *uint64 { return &n }

// TestRecoveryDecision_Postgres runs the recovery_decision event through a real
// Postgres-backed audit store: the payload, the journey summary that reads it
// back out of the event's raw JSON, the hash chain, and the tamper evidence.
//
// This backend has had its own bugs the SQLite one could not show (an int4
// column overflowing, placeholders not rewritten). The journey enrichment is a
// new query on that path, and the chain's hash input gained a field, so both
// are checked here against the real thing.
func TestRecoveryDecision_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	store, err := audit.NewStore(audit.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	base := time.Now().UTC().Truncate(time.Second)
	const trace, run = "tr_rd_pg01", "plr_rd_pg01"

	// A request trace needs its anchor event to be a journey.
	if err := store.Record(ctx, &audit.Event{
		EventID: "evt_rd_pg01", Timestamp: base, EventType: audit.EventTypeDelegation, TraceID: trace,
		Session: audit.Session{ID: "s_pg", UserID: "alice"}, Input: audit.Input{UserQuery: "restore to the moment before the drop"},
		Decision: &audit.Decision{Agent: "sysadmin_agent"},
	}); err != nil {
		t.Fatal(err)
	}

	// A terabyte-scale loss and a multi-day span: values that would overflow a
	// 32-bit column, to be sure none of this lands in one.
	plan := audit.RecoveryDecision{
		Stage: audit.RecoveryStagePlan, Decision: audit.RecoveryDecisionPlanned, Tool: "plan_point_in_time_recovery",
		Target:         "host=localhost port=15435 dbname=testdb user=postgres password=testpass",
		RecoveryTarget: "time=2026-11-04T01:39:07Z", PlanDigest: "0123456789abcdef", PlanStatus: "feasible",
		Objectives: &audit.ObjectivesInForce{RPOSeconds: i64(900), RPOSource: "environment:prod", Enforcement: "acknowledge", ConfigDigest: "ab12cd34ef56ab12"},
		Options: []audit.DecisionOption{{Name: "point_in_time_restore", LossBasis: "archive_end", LossUpperBound: true,
			LossBytes: u64(5 << 40), LossAtLeastSeconds: i64(16*86400 + 13*3600), LossAtLeastBasis: "the segment holding the target reached the repository at …",
			Verdict: "exceeds", Gate: "acknowledge"}},
	}
	exec := audit.RecoveryDecision{
		Stage: audit.RecoveryStageExecution, Decision: audit.RecoveryDecisionRefused, Tool: "restore_to_point_in_time",
		Reason:   "needs acknowledge_objective_breach (password=testpass)",
		Approval: &audit.ApprovalFacts{ApprovedBy: "alice", AllowedStopServing: true},
		Options:  []audit.DecisionOption{{Name: "point_in_time_restore", Verdict: "exceeds", Gate: "acknowledge"}},
	}
	ta := audit.NewToolAuditor(store, "sysadmin_agent", "sess_pg", trace)
	for _, d := range []audit.RecoveryDecision{plan, exec} {
		if err := ta.RecordRecoveryDecision(ctx, d); err != nil {
			t.Fatalf("recording %s: %v", d.Stage, err)
		}
	}
	// The gate and the outcome are written by the gateway, keyed to the run.
	secs := int64(41*60 + 12)
	for _, d := range []audit.RecoveryDecision{
		{Stage: audit.RecoveryStageGate, Decision: audit.RecoveryDecisionApproved, DecidedBy: "alice", RunID: run,
			Approval: &audit.ApprovalFacts{ApprovedBy: "alice", AcknowledgedDataLoss: true}},
		{Stage: audit.RecoveryStageOutcome, Decision: audit.OutcomeResolved, RunID: run, DurationSeconds: &secs},
	} {
		if err := store.Record(ctx, audit.NewRecoveryDecisionEvent(run, "", d)); err != nil {
			t.Fatalf("recording %s: %v", d.Stage, err)
		}
	}

	// The payloads come back whole, by type and by trace.
	events, err := store.Query(ctx, audit.QueryOptions{EventType: audit.EventTypeRecoveryDecision})
	if err != nil || len(events) != 4 {
		t.Fatalf("events = %d, %v", len(events), err)
	}
	byStage := map[string]audit.RecoveryDecision{}
	for _, ev := range events {
		if ev.RecoveryDecision == nil {
			t.Fatalf("an event with no payload: %+v", ev)
		}
		byStage[ev.RecoveryDecision.Stage] = *ev.RecoveryDecision
	}
	p := byStage[audit.RecoveryStagePlan]
	if p.Options[0].LossBytes == nil || *p.Options[0].LossBytes != 5<<40 || *p.Options[0].LossAtLeastSeconds != 16*86400+13*3600 ||
		p.Objectives.ConfigDigest != "ab12cd34ef56ab12" || p.PlanDigest != "0123456789abcdef" {
		t.Errorf("plan payload changed on the way: %+v", p)
	}
	if o := byStage[audit.RecoveryStageOutcome]; o.DurationSeconds == nil || *o.DurationSeconds != 41*60+12 {
		t.Errorf("outcome payload: %+v", o)
	}
	byRun, err := store.Query(ctx, audit.QueryOptions{TraceIDPrefix: run, EventType: audit.EventTypeRecoveryDecision})
	if err != nil || len(byRun) != 2 {
		t.Errorf("by the run's key: %d events, %v", len(byRun), err)
	}

	// No password at rest, whichever emitter wrote it.
	stored, _ := json.Marshal(events)
	if strings.Contains(string(stored), "testpass") {
		t.Errorf("the password is in the stored events: %s", stored)
	}

	// The journey summary reads the decision out of the raw JSON. The latest
	// event of the trace is the refusal, and its option's verdict is reported.
	journeys, err := store.QueryJourneys(ctx, audit.JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys on Postgres: %v", err)
	}
	var found bool
	for _, j := range journeys {
		if j.TraceID == trace {
			found = true
			if j.RecoveryDecision != "refused" || j.RecoveryVerdict != "exceeds" {
				t.Errorf("journey decision/verdict = %q/%q, want refused/exceeds", j.RecoveryDecision, j.RecoveryVerdict)
			}
		}
	}
	if !found {
		t.Fatalf("journey %s not found among %d", trace, len(journeys))
	}

	// The chain verifies with the new payload in the hash input.
	status, err := store.VerifyIntegrity(ctx)
	if err != nil || !status.Valid || status.TotalEvents != 5 {
		t.Fatalf("chain = %+v, %v", status, err)
	}

	// And editing a recovery decision at rest breaks it, on this backend too.
	var id, raw string
	if err := store.DB().QueryRowContext(ctx,
		`SELECT event_id, raw_json FROM audit_events WHERE event_type = 'recovery_decision' AND raw_json LIKE '%"refused"%' LIMIT 1`).Scan(&id, &raw); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(raw, `"decision":"refused"`, `"decision":"proceeded"`, 1)
	if edited == raw {
		t.Fatalf("the edit did not apply: %s", raw)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE audit_events SET raw_json = ? WHERE event_id = ?`, edited, id); err != nil {
		t.Fatal(err)
	}
	if status, err := store.VerifyIntegrity(ctx); err != nil || status.Valid {
		t.Errorf("an edited recovery decision verified on Postgres: %+v, %v", status, err)
	}
}
