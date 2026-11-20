package availability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
	"helpdesk/playbooks"
)

func ticketLedger(t *testing.T) Ledger {
	t.Helper()
	now := time.Date(2026, 11, 18, 4, 0, 0, 0, time.UTC)
	restart := []string{"pbs_pgbackrest_pitr_triage", "pbs_pgbackrest_pitr_remediate"}
	mk := func(id, run, status string, start, end time.Duration, series []string) Incident {
		return Incident{ID: id, EntryRunID: run, Status: status, Origin: audit.IncidentOriginReal, SeriesIDs: series,
			DetectedAt: now.Add(-start), ResolvedAt: now.Add(-end)}
	}
	return Compute(Params{
		Database: "db", Now: now, Classify: playbooks.AvailabilityImpact,
		Budget: infra.ComputeBudget(99.9, 30*24*time.Hour, "environment:prod", 2*time.Minute, true),
		Incidents: []Incident{
			mk("inc_a", "plr_a", audit.IncidentStatusResolved, 5*time.Hour, 5*time.Hour-10*time.Minute, restart),
			mk("inc_b", "plr_b", audit.IncidentStatusNeedsHuman, 3*time.Hour, 3*time.Hour-5*time.Minute, restart),
			mk("inc_c", "plr_c", audit.IncidentStatusResolved, 2*time.Hour, 2*time.Hour-3*time.Minute, restart),
			mk("inc_d", "", audit.IncidentStatusResolved, time.Hour, time.Hour-3*time.Minute, restart),
		},
	})
}

// The ticket an incident's restore was approved under is put on its entry, charged
// or not; an incident with no run, or whose run has no ticket, is left alone; the
// arithmetic does not change.
func TestLedger_AttachTickets(t *testing.T) {
	l := ticketLedger(t)
	spent := l.ChargedSeconds
	if l.Charged[0].EntryRunID != "plr_a" {
		t.Fatalf("the entry run is not on the entry: %+v", l.Charged[0])
	}
	l.AttachTickets(map[string]string{"plr_a": "INC-1239", "plr_b": "CHG-7", "": "INC-NOBODY"})

	byID := map[string]Entry{}
	for _, list := range [][]Entry{l.Charged, l.Unconfirmed} {
		for _, e := range list {
			byID[e.IncidentID] = e
		}
	}
	if byID["inc_a"].Ticket != "INC-1239" {
		t.Errorf("charged entry: %+v", byID["inc_a"])
	}
	if byID["inc_b"].Ticket != "CHG-7" {
		t.Errorf("unconfirmed entry: %+v", byID["inc_b"])
	}
	if byID["inc_c"].Ticket != "" {
		t.Errorf("a run with no ticket got one: %+v", byID["inc_c"])
	}
	if byID["inc_d"].Ticket != "" {
		t.Errorf("an incident with no entry run got a ticket: %+v", byID["inc_d"])
	}
	if l.ChargedSeconds != spent {
		t.Errorf("attaching tickets changed the spend: %d -> %d", spent, l.ChargedSeconds)
	}
	l.AttachTickets(nil) // nothing to attach is not an error
}

// The client reads the gate-stage decisions that carry a ticket, by run, and only
// those; it forwards the key and the window.
func TestClient_ApprovalTickets(t *testing.T) {
	gate := func(run, ticket string) audit.Event {
		return audit.Event{EventType: audit.EventTypeRecoveryDecision, RecoveryDecision: &audit.RecoveryDecision{
			Stage: audit.RecoveryStageGate, Decision: audit.RecoveryDecisionApproved, RunID: run,
			Approval: &audit.ApprovalFacts{ApprovedBy: "alice", Ticket: ticket}}}
	}
	exec := gate("plr_exec", "INC-EXEC")
	exec.RecoveryDecision.Stage = audit.RecoveryStageExecution // not the approval itself
	events := []audit.Event{gate("plr_a", "INC-1239"), gate("plr_none", ""), exec,
		{EventType: audit.EventTypeRecoveryDecision, RecoveryDecision: &audit.RecoveryDecision{Stage: audit.RecoveryStageGate, RunID: "plr_noapproval"}}}

	var query, auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, auth, path = r.URL.RawQuery, r.Header.Get("Authorization"), r.URL.Path
		json.NewEncoder(w).Encode(events) //nolint:errcheck
	}))
	defer srv.Close()

	since := time.Date(2026, 10, 19, 4, 0, 0, 0, time.UTC)
	got, err := (&Client{URL: srv.URL + "/", APIKey: "k"}).ApprovalTickets(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["plr_a"] != "INC-1239" {
		t.Errorf("tickets = %v, want only plr_a", got)
	}
	if path != "/v1/events" || auth != "Bearer k" || !strings.Contains(query, "event_type=recovery_decision") ||
		!strings.Contains(query, "since=2026-10-19T04%3A00%3A00Z") {
		t.Errorf("asked %s?%s with %q", path, query, auth)
	}

	if _, err := (&Client{}).ApprovalTickets(context.Background(), since); err != ErrNoAudit {
		t.Errorf("no auditd: %v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) }))
	defer bad.Close()
	if _, err := (&Client{URL: bad.URL}).ApprovalTickets(context.Background(), since); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("a failing auditd: %v", err)
	}
}
