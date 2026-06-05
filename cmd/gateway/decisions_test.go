package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/decisions"
)

// mockDecisionAuditd returns an httptest.Server that serves a single
// PlaybookRun at GET /v1/fleet/playbook-runs/{id}.
func mockDecisionAuditd(t *testing.T, run *audit.PlaybookRun) *httptest.Server {
	t.Helper()
	runData, _ := json.Marshal(run)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/playbook-runs/") {
			w.Write(runData) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func getDecision(t *testing.T, gw *Gateway, id string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/decisions/"+id, nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestHandleGetDecision_Gate_Transition verifies that a run whose gate came from
// TRANSITION_TO returns gate_type="transition" and transition_target, not
// escalation_target.
func TestHandleGetDecision_Gate_Transition(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:           "plr_vac01",
		SeriesID:        "pbs_vacuum_triage",
		Outcome:         audit.OutcomeGatePending,
		TransitionedTo:  "pbs_vacuum_remediate",
		FindingsSummary: "dead_ratio=0.35; recommended=manual_vacuum",
		Operator:        "ops-alice",
		StartedAt:       time.Now().UTC(),
	}
	auditSrv := mockDecisionAuditd(t, run)
	gw := &Gateway{auditURL: auditSrv.URL, baseURL: "http://localhost"}

	rec := getDecision(t, gw, "gate:plr_vac01")

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var d decisions.Decision
	if err := json.NewDecoder(rec.Body).Decode(&d); err != nil {
		t.Fatalf("decoding decision: %v", err)
	}

	if d.Type != decisions.DecisionTypeGate {
		t.Errorf("Type = %q, want %q", d.Type, decisions.DecisionTypeGate)
	}
	if d.Status != "pending" {
		t.Errorf("Status = %q, want pending", d.Status)
	}
	if !strings.Contains(d.Summary, "TRANSITION_TO") {
		t.Errorf("Summary should mention TRANSITION_TO; got %q", d.Summary)
	}
	if !strings.Contains(d.Summary, "pbs_vacuum_remediate") {
		t.Errorf("Summary should mention pbs_vacuum_remediate; got %q", d.Summary)
	}

	gateType, _ := d.Extra["gate_type"].(string)
	if gateType != "transition" {
		t.Errorf("extra.gate_type = %q, want transition", gateType)
	}
	transTarget, _ := d.Extra["transition_target"].(string)
	if transTarget != "pbs_vacuum_remediate" {
		t.Errorf("extra.transition_target = %q, want pbs_vacuum_remediate", transTarget)
	}
	if _, hasEsc := d.Extra["escalation_target"]; hasEsc {
		t.Errorf("extra.escalation_target should be absent for a transition gate")
	}
}

// TestHandleGetDecision_Gate_Escalation verifies that a run whose gate came from
// a true ESCALATE_TO returns gate_type="escalation" and escalation_target.
func TestHandleGetDecision_Gate_Escalation(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:           "plr_conn01",
		SeriesID:        "pbs_connection_triage",
		Outcome:         audit.OutcomeGatePending,
		EscalatedTo:     "pbs_sysadmin_docker_inspect",
		FindingsSummary: "connections 198/200; recommended=escalate",
		Operator:        "ops-bob",
		StartedAt:       time.Now().UTC(),
	}
	auditSrv := mockDecisionAuditd(t, run)
	gw := &Gateway{auditURL: auditSrv.URL, baseURL: "http://localhost"}

	rec := getDecision(t, gw, "gate:plr_conn01")

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var d decisions.Decision
	if err := json.NewDecoder(rec.Body).Decode(&d); err != nil {
		t.Fatalf("decoding decision: %v", err)
	}

	if !strings.Contains(d.Summary, "ESCALATE_TO") {
		t.Errorf("Summary should mention ESCALATE_TO; got %q", d.Summary)
	}
	gateType, _ := d.Extra["gate_type"].(string)
	if gateType != "escalation" {
		t.Errorf("extra.gate_type = %q, want escalation", gateType)
	}
	escTarget, _ := d.Extra["escalation_target"].(string)
	if escTarget != "pbs_sysadmin_docker_inspect" {
		t.Errorf("extra.escalation_target = %q, want pbs_sysadmin_docker_inspect", escTarget)
	}
	if _, hasTrans := d.Extra["transition_target"]; hasTrans {
		t.Errorf("extra.transition_target should be absent for a true escalation gate")
	}
}

// TestHandleGetDecision_Gate_NotFound verifies that a missing run ID returns 404.
func TestHandleGetDecision_Gate_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	gw := &Gateway{auditURL: srv.URL, baseURL: "http://localhost"}

	rec := getDecision(t, gw, "gate:plr_missing")

	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404 for missing run", rec.Code)
	}
}

// TestHandleGetDecision_UnknownPrefix verifies that an unrecognised ID prefix
// returns 400.
func TestHandleGetDecision_UnknownPrefix(t *testing.T) {
	gw := &Gateway{auditURL: "http://localhost", baseURL: "http://localhost"}

	rec := getDecision(t, gw, "bogus:abc123")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 for unknown prefix", rec.Code)
	}
}
