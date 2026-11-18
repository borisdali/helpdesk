package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// Events can be looked up by the ticket a point-in-time approval was given under,
// through the same path the agents and the gateway use: the remote store, auditd's
// HTTP handler, the store's query. The match is exact.
func TestAuditd_EventsAreFoundByTicket(t *testing.T) {
	store := newAuthStore(t)
	srv := &server{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", srv.handleRecordEvent)
	mux.HandleFunc("GET /v1/events", srv.handleQueryEvents)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ctx := context.Background()
	remote := audit.NewRemoteStore(ts.URL)

	gate := func(run, ticket string) {
		t.Helper()
		d := audit.RecoveryDecision{Stage: audit.RecoveryStageGate, Decision: audit.RecoveryDecisionApproved, DecidedBy: "alice", RunID: run,
			Approval: &audit.ApprovalFacts{ApprovedBy: "alice", AcknowledgedDataLoss: true, Ticket: ticket}}
		ev := audit.NewRecoveryDecisionEvent(run, "", d)
		ev.Timestamp = time.Now().UTC()
		if err := remote.Record(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	gate("plr_t1", "INC-1239")
	gate("plr_t2", "INC-12390") // longer, same start
	gate("plr_t3", "INC_1239")  // "_" is not a wildcard
	gate("plr_t4", "")          // approved with no ticket

	runs := func(events []audit.Event) map[string]bool {
		got := map[string]bool{}
		for _, e := range events {
			got[e.RecoveryDecision.RunID] = true
		}
		return got
	}
	find := func(ticket string) map[string]bool {
		t.Helper()
		events, err := remote.Query(ctx, audit.QueryOptions{Ticket: ticket})
		if err != nil {
			t.Fatal(err)
		}
		return runs(events)
	}

	if got := find("INC-1239"); len(got) != 1 || !got["plr_t1"] {
		t.Errorf("INC-1239 found %v, want only plr_t1", got)
	}
	if got := find("INC-123"); len(got) != 0 {
		t.Errorf("a prefix of a ticket matched: %v", got)
	}
	if got := find("INC%"); len(got) != 0 {
		t.Errorf("a wildcard matched: %v", got)
	}
	if got := find("INC_1239"); len(got) != 1 || !got["plr_t3"] {
		t.Errorf("INC_1239 found %v, want only plr_t3", got)
	}
	if got := find("NO-SUCH"); len(got) != 0 {
		t.Errorf("an unknown ticket found %v", got)
	}
	// Without the filter everything comes back, the unticketed one included.
	if events, err := remote.Query(ctx, audit.QueryOptions{EventType: audit.EventTypeRecoveryDecision}); err != nil || len(events) != 4 {
		t.Errorf("unfiltered: %d events, %v", len(events), err)
	}
	// It combines with the other filters.
	if events, err := remote.Query(ctx, audit.QueryOptions{Ticket: "INC-1239", EventType: audit.EventTypeDelegation}); err != nil || len(events) != 0 {
		t.Errorf("ticket and another type: %d events, %v", len(events), err)
	}
}
