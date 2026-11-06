package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// Agents write their audit events to auditd over HTTP through the remote store.
// A recovery_decision must survive that whole path: the payload intact, the
// chain still verifying, no password on the wire or at rest, and the journey
// summary reading it back.
func TestAuditd_RecoveryDecisionRoundTripsOverHTTP(t *testing.T) {
	store := newAuthStore(t)
	srv := &server{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", srv.handleRecordEvent)
	mux.HandleFunc("GET /v1/events", srv.handleQueryEvents)
	mux.HandleFunc("GET /v1/verify", srv.handleVerifyChain)
	mux.HandleFunc("GET /v1/journeys", srv.handleQueryJourneys)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx := context.Background()
	remote := audit.NewRemoteStore(ts.URL)
	const trace = "tr_http_rd01"

	// The journey needs its anchor, as a real request has.
	if err := remote.Record(ctx, &audit.Event{
		EventID: "evt_http_rd01", Timestamp: time.Now().UTC(), EventType: audit.EventTypeDelegation, TraceID: trace,
		Session: audit.Session{ID: "sess_http", UserID: "alice"}, Input: audit.Input{UserQuery: "restore"},
		Decision: &audit.Decision{Agent: "sysadmin_agent"},
	}); err != nil {
		t.Fatal(err)
	}

	secs, upper := int64(65563), false
	rpo := int64(900)
	ta := audit.NewToolAuditor(remote, "sysadmin_agent", "sess_http", trace)
	ta.RecordRecoveryDecision(ctx, audit.RecoveryDecision{
		Stage: audit.RecoveryStageExecution, Decision: audit.RecoveryDecisionRefused,
		Reason: "needs acknowledge_objective_breach", Tool: "restore_to_point_in_time",
		Target:         "host=localhost port=15435 dbname=testdb user=postgres password=testpass",
		RecoveryTarget: "time=2026-11-04T01:39:07Z", PlanDigest: "0123456789abcdef", PlanStatus: "feasible",
		Objectives: &audit.ObjectivesInForce{RPOSeconds: &rpo, RPOSource: "environment:prod", Enforcement: "acknowledge", ConfigDigest: "0123456789abcdef"},
		Options: []audit.DecisionOption{{Name: "point_in_time_restore", LossSeconds: &secs, LossUpperBound: upper,
			Verdict: "exceeds", Gate: "acknowledge"}},
		Approval: &audit.ApprovalFacts{ApprovedBy: "alice", AllowedStopServing: true},
	})

	get := func(path string, into any) string {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var raw strings.Builder
		dec := json.NewDecoder(resp.Body)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		raw.Write(v)
		if err := json.Unmarshal(v, into); err != nil {
			t.Fatalf("GET %s: %v: %s", path, err, v)
		}
		return raw.String()
	}

	// The event, by type and trace, with its payload.
	var events []audit.Event
	body := get("/v1/events?event_type=recovery_decision&trace_id="+trace, &events)
	if len(events) != 1 || events[0].RecoveryDecision == nil {
		t.Fatalf("events = %s", body)
	}
	d := events[0].RecoveryDecision
	if d.Decision != "refused" || d.PlanDigest != "0123456789abcdef" || *d.Objectives.RPOSeconds != 900 || d.Objectives.ConfigDigest != "0123456789abcdef" ||
		len(d.Options) != 1 || *d.Options[0].LossSeconds != 65563 || d.Options[0].Gate != "acknowledge" || d.Approval == nil || !d.Approval.AllowedStopServing {
		t.Errorf("payload changed on the way: %+v", d)
	}
	// Nothing secret at rest or on the wire back.
	if strings.Contains(body, "testpass") {
		t.Errorf("the password is in the stored event: %s", body)
	}
	if events[0].EventHash == "" {
		t.Error("the stored event has no hash")
	}

	// The chain, including the new payload, verifies.
	var status audit.ChainStatus
	get("/v1/verify", &status)
	if !status.Valid || status.TotalEvents != 2 {
		t.Errorf("chain = %+v", status)
	}

	// The journey summary carries it.
	var journeys []audit.JourneySummary
	body = get("/v1/journeys?trace_id="+trace, &journeys)
	if len(journeys) != 1 || journeys[0].RecoveryDecision != "refused" || journeys[0].RecoveryVerdict != "exceeds" {
		t.Errorf("journeys = %s", body)
	}
}
