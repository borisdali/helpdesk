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
