package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

func ticketEvent(m int, run, who, ticket, why string) audit.Event {
	return audit.Event{Timestamp: rdAt(m), EventType: audit.EventTypeRecoveryDecision, RecoveryDecision: &audit.RecoveryDecision{
		Stage: audit.RecoveryStageGate, Decision: audit.RecoveryDecisionApproved, DecidedBy: who, RunID: run,
		RecoveryTarget: "time=2026-11-18T02:49:55Z",
		Approval:       &audit.ApprovalFacts{ApprovedBy: who, Ticket: ticket, Justification: why}}}
}

func TestFormatApprovalsByTicket(t *testing.T) {
	out := formatApprovalsByTicket("INC-1239", []audit.Event{
		ticketEvent(9, "plr_b", "bob@example.com", "INC-1239", ""),
		ticketEvent(2, "plr_a", "alice@example.com", "INC-1239", "testing the cost of waiting"),
		ticketEvent(5, "plr_other", "carol", "INC-9999", "not this ticket"), // a stray row is not listed
	})
	if !strings.Contains(out, "Approvals given under ticket INC-1239 (2)") {
		t.Errorf("header:\n%s", out)
	}
	if strings.Contains(out, "plr_other") {
		t.Errorf("another ticket's approval was listed:\n%s", out)
	}
	if strings.Index(out, "plr_a") > strings.Index(out, "plr_b") || !strings.Contains(out, "plr_a") {
		t.Errorf("not oldest first:\n%s", out)
	}
	for _, want := range []string{"alice@example.com", "time=2026-11-18T02:49:55Z", "justification (the approver's words): testing the cost of waiting",
		"recorded, not checked", "faulttest vault incidents <run>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if got := formatApprovalsByTicket("INC-1", nil); !strings.Contains(got, "No approvals were given under ticket INC-1") {
		t.Errorf("empty: %q", got)
	}
}

// The command asks the gateway's events endpoint for recovery decisions under the
// ticket, with the key, and reads the events back.
func TestFetchApprovalsByTicket(t *testing.T) {
	var gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotAuth = r.URL.RawQuery, r.Header.Get("Authorization")
		if r.URL.Path != "/api/v1/governance/events" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		ev := ticketEvent(2, "plr_a", "alice", "INC-1239", "")
		ev.Timestamp = time.Date(2026, 11, 18, 3, 1, 11, 0, time.UTC)
		json.NewEncoder(w).Encode([]audit.Event{ev}) //nolint:errcheck
	}))
	defer srv.Close()

	events, err := fetchApprovalsByTicket(srv.URL+"/", "key-1", "INC-1239", 50)
	if err != nil || len(events) != 1 || events[0].RecoveryDecision.RunID != "plr_a" {
		t.Fatalf("events = %v, err = %v", events, err)
	}
	for _, want := range []string{"ticket=INC-1239", "event_type=recovery_decision", "limit=50"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q is missing %q", gotQuery, want)
		}
	}
	if gotAuth != "Bearer key-1" {
		t.Errorf("auth = %q", gotAuth)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusForbidden) }))
	defer bad.Close()
	if _, err := fetchApprovalsByTicket(bad.URL, "", "INC-1", 0); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a refusal: %v", err)
	}
}
