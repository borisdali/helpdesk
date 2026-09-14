package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"helpdesk/internal/audit"
)

// mockRunStartAuditd serves POST .../runs (recordPlaybookRunStart) and POST
// /v1/incidents (createIncidentRecord), recording every request it sees so
// tests can assert which calls were and weren't made.
type mockRunStartAuditd struct {
	mu       sync.Mutex
	requests []capturedRequest
	// incidentStatus lets a test force a non-201 response from /v1/incidents
	// to prove createIncidentRecord's failure is best-effort.
	incidentStatus int
	// playbook/priorRun, when set, let this mock also serve the GET lookups
	// handlePlaybookRun makes before recordPlaybookRunStart — needed to drive
	// this mock through the real HTTP handler (postPlaybookRun) rather than
	// calling recordPlaybookRunStart directly.
	playbook *audit.Playbook
	priorRun *audit.PlaybookRun
}

type capturedRequest struct {
	method string
	path   string
	body   string
}

func (m *mockRunStartAuditd) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf strings.Builder
		io.Copy(&buf, r.Body) //nolint:errcheck
		m.mu.Lock()
		m.requests = append(m.requests, capturedRequest{method: r.Method, path: r.URL.Path, body: buf.String()})
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && m.playbook != nil && strings.Contains(r.URL.Path, "/v1/fleet/playbooks"):
			json.NewEncoder(w).Encode(m.playbook) //nolint:errcheck
		case r.Method == http.MethodGet && m.priorRun != nil && strings.Contains(r.URL.Path, "/v1/fleet/playbook-runs/"):
			json.NewEncoder(w).Encode(m.priorRun) //nolint:errcheck
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/runs"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(audit.PlaybookRun{RunID: "plr_entry01"}) //nolint:errcheck
		case r.Method == http.MethodPost && r.URL.Path == "/v1/incidents":
			status := m.incidentStatus
			if status == 0 {
				status = http.StatusCreated
			}
			w.WriteHeader(status)
			if status == http.StatusCreated {
				json.NewEncoder(w).Encode(audit.Incident{IncidentID: "inc_test01"}) //nolint:errcheck
			}
		case r.Method == http.MethodPatch:
			// recordPlaybookRunComplete's best-effort outcome patch — not under
			// test here, just needs to not 404/connection-refuse noisily.
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (m *mockRunStartAuditd) calls(method, pathSuffix string) []capturedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []capturedRequest
	for _, req := range m.requests {
		if req.method == method && strings.HasSuffix(req.path, pathSuffix) {
			out = append(out, req)
		}
	}
	return out
}

func TestRecordPlaybookRunStart_EntryPointRun_CreatesIncident(t *testing.T) {
	mock := &mockRunStartAuditd{}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	pb := &audit.Playbook{PlaybookID: "pb_test", SeriesID: "series1"}
	runID := gw.recordPlaybookRunStart(context.Background(), pb, "ctx1", "", "", "diagnostic",
		"trace_abc", "" /* priorRunID */, "", "operator1")

	if runID != "plr_entry01" {
		t.Fatalf("runID = %q, want plr_entry01", runID)
	}
	incidentCalls := mock.calls(http.MethodPost, "/v1/incidents")
	if len(incidentCalls) != 1 {
		t.Fatalf("POST /v1/incidents calls = %d, want 1 for an entry-point run (priorRunID empty)", len(incidentCalls))
	}
	var body audit.Incident
	if err := json.Unmarshal([]byte(incidentCalls[0].body), &body); err != nil {
		t.Fatalf("decode incident body: %v", err)
	}
	if body.TraceID != "trace_abc" {
		t.Errorf("incident trace_id = %q, want trace_abc", body.TraceID)
	}
	if body.EntryRunID != "plr_entry01" {
		t.Errorf("incident entry_run_id = %q, want plr_entry01 (the just-created run)", body.EntryRunID)
	}
}

func TestRecordPlaybookRunStart_ContinuationRun_NoIncidentCreated(t *testing.T) {
	mock := &mockRunStartAuditd{}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	pb := &audit.Playbook{PlaybookID: "pb_test", SeriesID: "series1"}
	runID := gw.recordPlaybookRunStart(context.Background(), pb, "ctx1", "", "", "diagnostic",
		"trace_abc", "plr_prior01" /* priorRunID set: chained/remediation hop */, "", "operator1")

	if runID != "plr_entry01" {
		t.Fatalf("runID = %q, want plr_entry01", runID)
	}
	incidentCalls := mock.calls(http.MethodPost, "/v1/incidents")
	if len(incidentCalls) != 0 {
		t.Errorf("POST /v1/incidents calls = %d, want 0 for a continuation hop (priorRunID set) — "+
			"one incident row must cover the whole chain, not one per hop", len(incidentCalls))
	}
}

func TestRecordPlaybookRunStart_IncidentCreateFails_RunStillReturned(t *testing.T) {
	mock := &mockRunStartAuditd{incidentStatus: http.StatusInternalServerError}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	pb := &audit.Playbook{PlaybookID: "pb_test", SeriesID: "series1"}
	runID := gw.recordPlaybookRunStart(context.Background(), pb, "ctx1", "", "", "diagnostic",
		"trace_abc", "", "", "operator1")

	if runID != "plr_entry01" {
		t.Errorf("runID = %q, want plr_entry01 — an incidents-table failure must not affect the run being recorded (best-effort)", runID)
	}
	incidentCalls := mock.calls(http.MethodPost, "/v1/incidents")
	if len(incidentCalls) != 1 {
		t.Fatalf("POST /v1/incidents calls = %d, want 1 (attempted, even though it failed)", len(incidentCalls))
	}
}

func TestCreateIncidentRecord_NoAuditURL_NoOp(t *testing.T) {
	gw := &Gateway{}
	// Should not panic and should simply return — no server configured.
	gw.createIncidentRecord(context.Background(), "plr_x", "trace_x")
}

func TestCreateIncidentRecord_EmptyRunID_NoOp(t *testing.T) {
	mock := &mockRunStartAuditd{}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.createIncidentRecord(context.Background(), "", "trace_x")

	if calls := mock.calls(http.MethodPost, "/v1/incidents"); len(calls) != 0 {
		t.Errorf("POST /v1/incidents calls = %d, want 0 when runID is empty", len(calls))
	}
}

// --- Integration: through the real HTTP handler, not recordPlaybookRunStart directly ---
//
// The tests above call gw.recordPlaybookRunStart directly, which only proves
// that function's own logic. Nothing proved that handlePlaybookRun's real
// request-handling code — the actual POST /api/v1/fleet/playbooks/{id}/run
// path a client hits — wires req.PriorRunID from a real JSON body into it
// correctly. These two tests close that gap by driving the full mux via
// postPlaybookRun (mirrors the reasoning in
// TestHandlePlaybookRun_ProtocolViolation_RecordsAuditEvent above).

func fleetPlannerLLM(t *testing.T) func(context.Context, string) (string, error) {
	t.Helper()
	return func(ctx context.Context, prompt string) (string, error) {
		return `{
			"name": "vacuum-check",
			"change": {"steps": [{"tool": "check_connection", "args": {}}]},
			"targets": ["prod-db-1"],
			"strategy": {}
		}`, nil
	}
}

func TestHandlePlaybookRun_EntryPointRequest_CreatesIncident_ViaRealHTTPHandler(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_inc_entry01",
		SeriesID:      "pbs_inc_entry01",
		Name:          "Incident Wiring Entry-Point Test",
		ExecutionMode: "fleet",
		IsActive:      true,
	}
	mock := &mockRunStartAuditd{playbook: pb}
	srv := mock.start(t)
	gw := makePlaybookRunGateway(srv.URL, fleetPlannerLLM(t))

	rec := postPlaybookRun(t, gw, pb.PlaybookID, `{}`) // no prior_run_id → genuine entry point
	if rec.Code == http.StatusBadGateway {
		t.Fatalf("got 502; body: %s", rec.Body.String())
	}

	incidentCalls := mock.calls(http.MethodPost, "/v1/incidents")
	if len(incidentCalls) != 1 {
		t.Fatalf("POST /v1/incidents calls = %d, want 1 — handlePlaybookRun's real code path "+
			"(not just recordPlaybookRunStart called directly) must create an incident for a "+
			"genuine entry-point request", len(incidentCalls))
	}
	var body audit.Incident
	if err := json.Unmarshal([]byte(incidentCalls[0].body), &body); err != nil {
		t.Fatalf("decode incident body: %v", err)
	}
	if body.EntryRunID != "plr_entry01" {
		t.Errorf("incident entry_run_id = %q, want plr_entry01 (the run_id handlePlaybookRun just recorded)", body.EntryRunID)
	}
}

func TestHandlePlaybookRun_ContinuationRequest_NoIncidentCreated_ViaRealHTTPHandler(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_inc_cont01",
		SeriesID:      "pbs_inc_cont01",
		Name:          "Incident Wiring Continuation Test",
		ExecutionMode: "fleet",
		IsActive:      true,
	}
	priorRun := &audit.PlaybookRun{RunID: "plr_prior01", FindingsSummary: "prior findings"}
	mock := &mockRunStartAuditd{playbook: pb, priorRun: priorRun}
	srv := mock.start(t)
	gw := makePlaybookRunGateway(srv.URL, fleetPlannerLLM(t))

	rec := postPlaybookRun(t, gw, pb.PlaybookID, `{"prior_run_id":"plr_prior01"}`)
	if rec.Code == http.StatusBadGateway {
		t.Fatalf("got 502; body: %s", rec.Body.String())
	}

	if calls := mock.calls(http.MethodPost, "/v1/incidents"); len(calls) != 0 {
		t.Errorf("POST /v1/incidents calls = %d, want 0 for a real HTTP request carrying prior_run_id "+
			"(a continuation hop, not a genuine entry point)", len(calls))
	}
}
