package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"helpdesk/internal/audit"
)

// `faulttest vault incidents --ticket` asks the gateway's events endpoint, which
// hands the query to auditd as it is, with the service key, and returns what
// auditd answers. The ticket filter must get through the proxy unchanged.
func TestGovernanceEvents_ForwardsTheTicketFilter(t *testing.T) {
	var gotPath, gotAuth string
	var gotQuery url.Values
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotQuery = r.URL.Path, r.Header.Get("Authorization"), r.URL.Query()
		json.NewEncoder(w).Encode([]audit.Event{{EventID: "evt_gate", EventType: audit.EventTypeRecoveryDecision, //nolint:errcheck
			RecoveryDecision: &audit.RecoveryDecision{Stage: audit.RecoveryStageGate, RunID: "plr_a",
				Approval: &audit.ApprovalFacts{ApprovedBy: "alice", Ticket: "INC-1239"}}}})
	}))
	defer auditd.Close()

	gw := &Gateway{auditURL: auditd.URL, auditAPIKey: "svc-key"}
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/governance/events?event_type=recovery_decision&ticket=INC-1239&limit=50", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/events" || gotAuth != "Bearer svc-key" {
		t.Errorf("auditd was asked %s with %q", gotPath, gotAuth)
	}
	if gotQuery.Get("ticket") != "INC-1239" || gotQuery.Get("event_type") != "recovery_decision" || gotQuery.Get("limit") != "50" {
		t.Errorf("the query reached auditd as %v", gotQuery)
	}
	var events []audit.Event
	if err := json.NewDecoder(rec.Body).Decode(&events); err != nil || len(events) != 1 ||
		events[0].RecoveryDecision == nil || events[0].RecoveryDecision.Approval.Ticket != "INC-1239" {
		t.Errorf("the client got %v, %v", events, err)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Errorf("content type = %q", rec.Header().Get("Content-Type"))
	}
}
