package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rdI64(n int64) *int64     { return &n }
func rdU64(n uint64) *uint64   { return &n }
func rdF64(n float64) *float64 { return &n }

func sampleDecision() RecoveryDecision {
	return RecoveryDecision{
		Stage: RecoveryStageExecution, Decision: RecoveryDecisionRefused,
		Reason: "the data this restore discards exceeds the recovery objective",
		Tool:   "restore_to_point_in_time", Target: "pgbackrest-db",
		RecoveryTarget: "time=2026-11-04T01:39:07Z", PlanDigest: "0123456789abcdef", PlanStatus: "feasible",
		Objectives: &ObjectivesInForce{
			Environment: "prod",
			RPOSeconds:  rdI64(900), RPOSource: "environment:prod",
			RTOSeconds: rdI64(1800), RTOSource: "environment:prod",
			AvailabilitySLO: rdF64(99.95), AvailabilitySource: "environment:prod",
			RecoveryPreference: "data_first", PreferenceSource: "environment:prod",
			Enforcement: "acknowledge", EnforcementSource: "environment:prod",
			Problems:     []string{"defaults.objectives: rto must be greater than zero"},
			ConfigDigest: "ab12cd34ef56ab12",
		},
		Options: []DecisionOption{{
			Name: "point_in_time_restore", Chosen: false,
			LossBasis: "live_instance", LossSeconds: rdI64(65563), LossBytes: rdU64(16 << 20), LossAsOf: "2026-11-04T19:51:50Z",
			LossUnmeasured: []string{"the number of transactions"},
			Verdict:        "exceeds", VerdictDetail: "18h 12m 43s discarded against an RPO of 15m 00s", Gate: "acknowledge",
		}},
		Approval: &ApprovalFacts{ApprovedBy: "alice", AcknowledgedDataLoss: true, AllowedStopServing: true},
	}
}

func newRDStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "audit.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// A decision survives the store whole: every pointer field, the nested
// objectives and options, and the chain still verifies.
func TestRecordRecoveryDecision_RoundTripsThroughTheStore(t *testing.T) {
	store := newRDStore(t)
	ta := NewToolAuditor(store, "sysadmin_agent", "sess_1", "trace_1")
	want := sampleDecision()
	ta.RecordRecoveryDecision(context.Background(), want)

	events, err := store.Query(context.Background(), QueryOptions{EventType: EventTypeRecoveryDecision})
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	ev := events[0]
	if ev.TraceID != "trace_1" || ev.Session.ID != "sess_1" || !strings.HasPrefix(ev.EventID, "rdc_") {
		t.Errorf("event envelope = %+v", ev)
	}
	got := ev.RecoveryDecision
	if got == nil {
		t.Fatalf("no recovery_decision on %+v", ev)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("round trip changed it:\n got %s\nwant %s", gotJSON, wantJSON)
	}
	if status, err := store.VerifyIntegrity(context.Background()); err != nil || !status.Valid {
		t.Errorf("chain = %+v, err = %v", status, err)
	}
	if !VerifyEventHash(&ev) {
		t.Error("the stored event does not verify against its own hash")
	}
}

// The record that someone accepted a loss knowingly must not be editable after
// the fact without breaking the chain.
func TestComputeEventHash_CoversTheRecoveryDecision(t *testing.T) {
	base := NewRecoveryDecisionEvent("trace_1", "sess_1", sampleDecision())
	h := ComputeEventHash(base)

	for _, c := range []struct {
		name string
		edit func(d *RecoveryDecision)
	}{
		{"the decision", func(d *RecoveryDecision) { d.Decision = RecoveryDecisionProceeded }},
		{"the acknowledgement", func(d *RecoveryDecision) { d.Approval.AcknowledgedBreach = true }},
		{"who approved", func(d *RecoveryDecision) { d.Approval.ApprovedBy = "mallory" }},
		{"the objective in force", func(d *RecoveryDecision) { *d.Objectives.RPOSeconds = 99999 }},
		{"the verdict", func(d *RecoveryDecision) { d.Options[0].Verdict = "within" }},
		{"the loss", func(d *RecoveryDecision) { *d.Options[0].LossSeconds = 1 }},
		{"the configuration used", func(d *RecoveryDecision) { d.Objectives.ConfigDigest = "0000000000000000" }},
	} {
		ev := NewRecoveryDecisionEvent("trace_1", "sess_1", sampleDecision())
		ev.EventID, ev.Timestamp = base.EventID, base.Timestamp
		c.edit(ev.RecoveryDecision)
		if ComputeEventHash(ev) == h {
			t.Errorf("editing %s left the event hash unchanged", c.name)
		}
	}
	// And an event's stored hash stops verifying when its payload is edited.
	base.EventHash = h
	if !VerifyEventHash(base) {
		t.Fatal("an untouched event does not verify")
	}
	base.RecoveryDecision.Decision = RecoveryDecisionProceeded
	if VerifyEventHash(base) {
		t.Error("an edited recovery decision still verifies")
	}
}

// Events that carry no recovery decision hash exactly as they did before the
// field existed, so no recorded chain is invalidated by it.
func TestComputeEventHash_UnchangedForEventsWithoutARecoveryDecision(t *testing.T) {
	ev := &Event{
		EventID: "tool_abc12345", Timestamp: time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC), EventType: EventTypeToolExecution,
		TraceID: "trace_1", ActionClass: ActionRead, PrevHash: GenesisHash,
		Session: Session{ID: "sess_1", AgentName: "sysadmin_agent"},
		Tool:    &ToolExecution{Name: "check_host", Parameters: map[string]any{"target": "db"}},
		Outcome: &Outcome{Status: "success"},
	}
	// The hash input as it was defined before RecoveryDecision was added.
	legacy := struct {
		EventID     string         `json:"event_id"`
		Timestamp   string         `json:"timestamp"`
		EventType   EventType      `json:"event_type"`
		TraceID     string         `json:"trace_id,omitempty"`
		ParentID    string         `json:"parent_id,omitempty"`
		ActionClass ActionClass    `json:"action_class,omitempty"`
		PrevHash    string         `json:"prev_hash,omitempty"`
		Session     Session        `json:"session"`
		Input       Input          `json:"input"`
		Output      *Output        `json:"output,omitempty"`
		Tool        *ToolExecution `json:"tool,omitempty"`
		Approval    *Approval      `json:"approval,omitempty"`
		Decision    *Decision      `json:"decision,omitempty"`
		Outcome     *Outcome       `json:"outcome,omitempty"`
	}{
		EventID: ev.EventID, Timestamp: ev.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00"), EventType: ev.EventType,
		TraceID: ev.TraceID, ActionClass: ev.ActionClass, PrevHash: ev.PrevHash, Session: ev.Session,
		Input: ev.Input, Output: ev.Output, Tool: ev.Tool, Approval: ev.Approval, Decision: ev.Decision, Outcome: ev.Outcome,
	}
	raw, _ := json.Marshal(legacy)
	if got, want := ComputeEventHash(ev), sha256Hex(raw); got != want {
		t.Errorf("an event without a recovery decision hashes as %s, it used to hash as %s", got, want)
	}
}

func TestRecordRecoveryDecision_DoesNothingWhenItCannot(t *testing.T) {
	store := newRDStore(t)
	// Auditing off, an unnamed stage, a nil auditor: no event, no panic.
	NewToolAuditor(nil, "a", "s", "t").RecordRecoveryDecision(context.Background(), sampleDecision())
	var none *ToolAuditor
	none.RecordRecoveryDecision(context.Background(), sampleDecision())
	d := sampleDecision()
	d.Stage = ""
	NewToolAuditor(store, "a", "s", "t").RecordRecoveryDecision(context.Background(), d)

	events, err := store.Query(context.Background(), QueryOptions{EventType: EventTypeRecoveryDecision})
	if err != nil || len(events) != 0 {
		t.Errorf("events = %v, err = %v", events, err)
	}
}

// The journey summary says what the trace decided and how it stood against the
// objective, without opening the event.
func TestQueryJourneys_CarriesTheRecoveryDecision(t *testing.T) {
	store := newRDStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	anchor := func(trace string, at time.Duration) *Event {
		return &Event{EventID: "del_" + trace, Timestamp: base.Add(at), EventType: EventTypeDelegation, TraceID: trace,
			Session: Session{ID: "s_" + trace, UserID: "alice"}, Input: Input{UserQuery: "restore"},
			Decision: &Decision{Agent: "sysadmin_agent"}}
	}
	decision := func(trace string, at time.Duration, d RecoveryDecision) *Event {
		ev := NewRecoveryDecisionEvent(trace, "s_"+trace, d)
		ev.Timestamp = base.Add(at)
		return ev
	}
	exceeds := DecisionOption{Name: "a", Verdict: "exceeds"}
	within := DecisionOption{Name: "b", Verdict: "within"}

	events := []*Event{
		// A triage trace that planned: first option's verdict, since none was chosen.
		anchor("tr_plan", 0),
		decision("tr_plan", time.Second, RecoveryDecision{Stage: RecoveryStagePlan, Decision: RecoveryDecisionPlanned, Options: []DecisionOption{exceeds}}),
		// A remediation trace: a plan, then the refusal. The latest event wins.
		anchor("tr_refused", 0),
		decision("tr_refused", time.Second, RecoveryDecision{Stage: RecoveryStagePlan, Decision: RecoveryDecisionPlanned, Options: []DecisionOption{within}}),
		decision("tr_refused", 2*time.Second, RecoveryDecision{Stage: RecoveryStageExecution, Decision: RecoveryDecisionRefused, Options: []DecisionOption{exceeds}}),
		// The chosen option's verdict is the one reported, not the first offered.
		anchor("tr_chosen", 0),
		decision("tr_chosen", time.Second, RecoveryDecision{Stage: RecoveryStageExecution, Decision: RecoveryDecisionProceeded,
			Options: []DecisionOption{within, {Name: "c", Verdict: "may_exceed", Chosen: true}}}),
		// A trace that decided nothing about recovery.
		anchor("tr_none", 0),
		// A decision with no options reports a decision and no verdict.
		anchor("tr_bare", 0),
		decision("tr_bare", time.Second, RecoveryDecision{Stage: RecoveryStageExecution, Decision: RecoveryDecisionRefused}),
	}
	for _, e := range events {
		if err := store.Record(ctx, e); err != nil {
			t.Fatalf("record %s: %v", e.EventID, err)
		}
	}
	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, j := range journeys {
		got[j.TraceID] = [2]string{j.RecoveryDecision, j.RecoveryVerdict}
	}
	want := map[string][2]string{
		"tr_plan":    {"planned", "exceeds"},
		"tr_refused": {"refused", "exceeds"},
		"tr_chosen":  {"proceeded", "may_exceed"},
		"tr_none":    {"", ""},
		"tr_bare":    {"refused", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("journeys = %v", got)
	}
	for trace, w := range want {
		if got[trace] != w {
			t.Errorf("%s: decision/verdict = %v, want %v", trace, got[trace], w)
		}
	}
	// A recovery decision does not change what the journey's outcome is.
	for _, j := range journeys {
		if j.TraceID == "tr_none" && j.Outcome != "" {
			t.Errorf("outcome = %q", j.Outcome)
		}
	}
}

func TestFetchRecoveryDecisionEvents(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		ev := NewRecoveryDecisionEvent("trace_9", "sess", sampleDecision())
		json.NewEncoder(w).Encode([]Event{*ev}) //nolint:errcheck
	}))
	defer srv.Close()

	events := FetchRecoveryDecisionEvents(srv.URL, "key", "trace_9", time.Now().Add(-time.Hour))
	if len(events) != 1 || events[0].RecoveryDecision == nil || events[0].RecoveryDecision.Target != "pgbackrest-db" {
		t.Fatalf("events = %+v", events)
	}
	for _, want := range []string{"event_type=recovery_decision", "trace_id=trace_9"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q lacks %q", gotQuery, want)
		}
	}
	// An unreachable auditd reads as no events.
	srv.Close()
	if got := FetchRecoveryDecisionEvents(srv.URL, "", "trace_9", time.Now()); len(got) != 0 {
		t.Errorf("got %v from a closed server", got)
	}
}
