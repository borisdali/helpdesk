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

// TestHandleGetDecision_Gate_ResolvedReason verifies that resolved gates include
// resolved_reason from the gate_acknowledged event when a reason was supplied.
func TestHandleGetDecision_Gate_ResolvedReason(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:          "plr_resolved01",
		SeriesID:       "pbs_lock_chain_triage",
		Outcome:        audit.OutcomeTransitioned,
		TransitionedTo: "pbs_lock_chain_remediate",
		Operator:       "alice",
		StartedAt:      time.Now().UTC(),
	}
	runData, _ := json.Marshal(run)

	// The gate_acknowledged event for this run.
	gateEvent := audit.Event{
		EventID:   "ga_test01",
		EventType: audit.EventTypeGateAcknowledged,
		TraceID:   run.RunID,
		Output:    &audit.Output{Response: "PID confirmed in pg_stat_activity"},
	}
	eventsData, _ := json.Marshal([]audit.Event{gateEvent})

	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/playbook-runs/plr_resolved01") && !strings.Contains(r.URL.Path, "/events"):
			w.Write(runData) //nolint:errcheck
		case r.URL.Path == "/v1/events" && r.URL.Query().Get("event_type") == "gate_acknowledged":
			w.Write(eventsData) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(auditSrv.Close)

	gw := &Gateway{auditURL: auditSrv.URL, baseURL: "http://localhost"}
	rec := getDecision(t, gw, "gate:plr_resolved01")

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var d decisions.Decision
	if err := json.NewDecoder(rec.Body).Decode(&d); err != nil {
		t.Fatalf("decode: %v", err)
	}

	reason, _ := d.Extra["resolved_reason"].(string)
	if reason != "PID confirmed in pg_stat_activity" {
		t.Errorf("resolved_reason = %q, want %q", reason, "PID confirmed in pg_stat_activity")
	}
	if d.Status != audit.OutcomeTransitioned {
		t.Errorf("Status = %q, want %q", d.Status, audit.OutcomeTransitioned)
	}
}

// TestHandleGetDecision_Gate_ResolvedNoReason verifies no resolved_reason key
// is present when the gate event has no reason.
func TestHandleGetDecision_Gate_ResolvedNoReason(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:          "plr_resolved02",
		SeriesID:       "pbs_lock_chain_triage",
		Outcome:        audit.OutcomeTransitioned,
		TransitionedTo: "pbs_lock_chain_remediate",
		Operator:       "alice",
		StartedAt:      time.Now().UTC(),
	}
	runData, _ := json.Marshal(run)

	// Gate event with empty reason.
	gateEvent := audit.Event{
		EventID:   "ga_test02",
		EventType: audit.EventTypeGateAcknowledged,
		TraceID:   run.RunID,
		Output:    &audit.Output{Response: ""},
	}
	eventsData, _ := json.Marshal([]audit.Event{gateEvent})

	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/playbook-runs/plr_resolved02"):
			w.Write(runData) //nolint:errcheck
		case r.URL.Path == "/v1/events" && r.URL.Query().Get("event_type") == "gate_acknowledged":
			w.Write(eventsData) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(auditSrv.Close)

	gw := &Gateway{auditURL: auditSrv.URL, baseURL: "http://localhost"}
	rec := getDecision(t, gw, "gate:plr_resolved02")

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var d decisions.Decision
	if err := json.NewDecoder(rec.Body).Decode(&d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, hasReason := d.Extra["resolved_reason"]; hasReason {
		t.Errorf("resolved_reason should be absent when reason is empty")
	}
}

// TestHandleGetIncident assembles a full incident narrative from triage run,
// gate event, remediation run, and feedback.
func TestHandleGetIncident(t *testing.T) {
	triageRun := &audit.PlaybookRun{
		RunID:           "plr_triage01",
		SeriesID:        "pbs_lock_chain_triage",
		Outcome:         audit.OutcomeTransitioned,
		TransitionedTo:  "pbs_lock_chain_remediate",
		FindingsSummary: "root blocker PID 867",
		Operator:        "alice",
		StartedAt:       time.Now().Add(-30 * time.Second).UTC(),
		CompletedAt:     time.Now().Add(-20 * time.Second).UTC(),
	}
	remRun := &audit.PlaybookRun{
		RunID:       "plr_remed01",
		SeriesID:    "pbs_lock_chain_remediate",
		PriorRunID:  "plr_triage01",
		Outcome:     audit.OutcomeResolved,
		Operator:    "alice",
		StartedAt:   time.Now().Add(-20 * time.Second).UTC(),
		CompletedAt: time.Now().UTC(),
	}
	triageData, _ := json.Marshal(triageRun)
	remListData, _ := json.Marshal(map[string]any{"runs": []*audit.PlaybookRun{remRun}, "count": 1})

	gateEvent := audit.Event{
		EventID:   "ga_inc01",
		Timestamp: time.Now().Add(-25 * time.Second).UTC(),
		EventType: audit.EventTypeGateAcknowledged,
		TraceID:   "plr_triage01",
		Output:    &audit.Output{Response: "confirmed manually"},
		Decision: &audit.Decision{
			ReasoningChain: []string{"operator bob acknowledged informed gate for run plr_triage01"},
		},
	}
	gateEventsData, _ := json.Marshal([]audit.Event{gateEvent})

	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		switch {
		case strings.Contains(r.URL.Path, "/playbook-runs/plr_triage01") && !strings.Contains(r.URL.Path, "/feedback"):
			w.Write(triageData) //nolint:errcheck
		case r.URL.Path == "/v1/fleet/playbook-runs" && q.Get("prior_run_id") == "plr_triage01":
			w.Write(remListData) //nolint:errcheck
		case r.URL.Path == "/v1/events" && q.Get("event_type") == "gate_acknowledged":
			w.Write(gateEventsData) //nolint:errcheck
		case strings.Contains(r.URL.Path, "/feedback"):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(auditSrv.Close)

	gw := &Gateway{auditURL: auditSrv.URL, baseURL: "http://localhost"}
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents/plr_triage01", nil)
	req.SetPathValue("runID", "plr_triage01")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var narrative IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&narrative); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if narrative.IncidentID != "plr_triage01" {
		t.Errorf("IncidentID = %q, want plr_triage01", narrative.IncidentID)
	}
	if narrative.Triage.RunID != "plr_triage01" {
		t.Errorf("Triage.RunID = %q, want plr_triage01", narrative.Triage.RunID)
	}
	if narrative.Gate == nil {
		t.Fatal("Gate chapter should be present")
	}
	if narrative.Gate.Resolution != "approved" {
		t.Errorf("Gate.Resolution = %q, want approved", narrative.Gate.Resolution)
	}
	if narrative.Gate.Reason != "confirmed manually" {
		t.Errorf("Gate.Reason = %q, want confirmed manually", narrative.Gate.Reason)
	}
	if narrative.Gate.ApprovedBy != "bob" {
		t.Errorf("Gate.ApprovedBy = %q, want bob", narrative.Gate.ApprovedBy)
	}
	if narrative.Remediation == nil {
		t.Fatal("Remediation chapter should be present")
	}
	if narrative.Remediation.RunID != "plr_remed01" {
		t.Errorf("Remediation.RunID = %q, want plr_remed01", narrative.Remediation.RunID)
	}
	if narrative.Feedback != nil {
		t.Errorf("Feedback should be nil when not submitted")
	}
	if narrative.DurationSec <= 0 {
		t.Errorf("DurationSec should be positive, got %f", narrative.DurationSec)
	}
}
