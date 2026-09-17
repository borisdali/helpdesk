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
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/discovery"

	"github.com/a2aproject/a2a-go/a2aclient"
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
	// incidentByRun/playbookBySeriesID/attributionPatchStatus support Phase 2's
	// classifyIncidentAttribution tests: GET /v1/incidents/by-run/{runID},
	// GET /v1/fleet/playbooks?series_id=, and forcing a non-204 PATCH response.
	incidentByRun          *audit.Incident
	playbookBySeriesID     *audit.Playbook
	attributionPatchStatus int
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
		case r.Method == http.MethodGet && m.playbookBySeriesID != nil && r.URL.Query().Get("series_id") != "":
			json.NewEncoder(w).Encode(map[string]any{"playbooks": []*audit.Playbook{m.playbookBySeriesID}}) //nolint:errcheck
		case r.Method == http.MethodGet && m.incidentByRun != nil && strings.Contains(r.URL.Path, "/v1/incidents/by-run/"):
			json.NewEncoder(w).Encode(m.incidentByRun) //nolint:errcheck
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
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/v1/incidents/"):
			status := m.attributionPatchStatus
			if status == 0 {
				status = http.StatusNoContent
			}
			w.WriteHeader(status)
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
		"trace_abc", "" /* priorRunID */, "", "operator1", "")

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
		"trace_abc", "plr_prior01" /* priorRunID set: chained/remediation hop */, "", "operator1", "")

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
		"trace_abc", "", "", "operator1", "")

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
	gw.createIncidentRecord(context.Background(), "plr_x", "trace_x", "pbs_x", "")
}

func TestCreateIncidentRecord_EmptyRunID_NoOp(t *testing.T) {
	mock := &mockRunStartAuditd{}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.createIncidentRecord(context.Background(), "", "trace_x", "pbs_x", "")

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

// --- Phase 2: root-cause attribution classification for real incidents ---

func TestCreateIncidentRecord_IncludesSeriesID(t *testing.T) {
	mock := &mockRunStartAuditd{}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	pb := &audit.Playbook{PlaybookID: "pb_test", SeriesID: "pbs_max_connections_triage"}
	gw.recordPlaybookRunStart(context.Background(), pb, "ctx1", "", "", "diagnostic",
		"trace_abc", "", "", "operator1", "")

	incidentCalls := mock.calls(http.MethodPost, "/v1/incidents")
	if len(incidentCalls) != 1 {
		t.Fatalf("POST /v1/incidents calls = %d, want 1", len(incidentCalls))
	}
	var body audit.Incident
	if err := json.Unmarshal([]byte(incidentCalls[0].body), &body); err != nil {
		t.Fatalf("decode incident body: %v", err)
	}
	if body.SeriesID != "pbs_max_connections_triage" {
		t.Errorf("incident series_id = %q, want pbs_max_connections_triage", body.SeriesID)
	}
}

func TestFetchIncidentByEntryRunID_Found(t *testing.T) {
	mock := &mockRunStartAuditd{incidentByRun: &audit.Incident{IncidentID: "inc_abc", SeriesID: "pbs_x"}}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	inc, err := gw.fetchIncidentByEntryRunID(context.Background(), "plr_x")
	if err != nil {
		t.Fatalf("fetchIncidentByEntryRunID: %v", err)
	}
	if inc == nil || inc.IncidentID != "inc_abc" {
		t.Errorf("got %+v, want incident inc_abc", inc)
	}
}

func TestFetchIncidentByEntryRunID_NotFound(t *testing.T) {
	mock := &mockRunStartAuditd{} // incidentByRun unset → mock 404s
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	inc, err := gw.fetchIncidentByEntryRunID(context.Background(), "plr_x")
	if err != nil {
		t.Errorf("fetchIncidentByEntryRunID on 404 = %v, want nil error (not-found is not a failure)", err)
	}
	if inc != nil {
		t.Errorf("got %+v, want nil", inc)
	}
}

func TestPatchIncidentAttribution_SendsAttributionBody(t *testing.T) {
	mock := &mockRunStartAuditd{}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.patchIncidentAttribution(context.Background(), "inc_abc", "connection-pool-saturation")

	calls := mock.calls(http.MethodPatch, "/v1/incidents/inc_abc")
	if len(calls) != 1 {
		t.Fatalf("PATCH /v1/incidents/inc_abc calls = %d, want 1", len(calls))
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode patch body: %v", err)
	}
	if body["attribution"] != "connection-pool-saturation" {
		t.Errorf("attribution = %q, want connection-pool-saturation", body["attribution"])
	}
}

func TestPatchIncidentTraceID_SendsTraceIDBody(t *testing.T) {
	mock := &mockRunStartAuditd{}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.patchIncidentTraceID(context.Background(), "inc_abc", "tr_backfilled01")

	calls := mock.calls(http.MethodPatch, "/v1/incidents/inc_abc")
	if len(calls) != 1 {
		t.Fatalf("PATCH /v1/incidents/inc_abc calls = %d, want 1", len(calls))
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode patch body: %v", err)
	}
	if body["trace_id"] != "tr_backfilled01" {
		t.Errorf("trace_id = %q, want tr_backfilled01", body["trace_id"])
	}
}

func TestBackfillIncidentTraceID_NoIncidentFound_NoPatch(t *testing.T) {
	mock := &mockRunStartAuditd{} // incidentByRun unset → 404
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.backfillIncidentTraceID(context.Background(), "plr_x", "tr_real01")

	if calls := mock.calls(http.MethodPatch, "/v1/incidents/"); len(calls) != 0 {
		t.Errorf("PATCH /v1/incidents calls = %d, want 0 when no incident row exists for this run", len(calls))
	}
}

func TestBackfillIncidentTraceID_EmptyTraceID_NoPatch(t *testing.T) {
	mock := &mockRunStartAuditd{incidentByRun: &audit.Incident{IncidentID: "inc_abc"}}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.backfillIncidentTraceID(context.Background(), "plr_x", "")

	if calls := mock.calls(http.MethodPatch, "/v1/incidents/"); len(calls) != 0 {
		t.Errorf("PATCH /v1/incidents calls = %d, want 0 when the new trace_id is empty", len(calls))
	}
}

func TestBackfillIncidentTraceID_AlreadySet_NoPatch(t *testing.T) {
	mock := &mockRunStartAuditd{incidentByRun: &audit.Incident{IncidentID: "inc_abc", TraceID: "tr_existing"}}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.backfillIncidentTraceID(context.Background(), "plr_x", "tr_new")

	if calls := mock.calls(http.MethodPatch, "/v1/incidents/"); len(calls) != 0 {
		t.Errorf("PATCH /v1/incidents calls = %d, want 0 when the incident already has a trace_id — must never overwrite", len(calls))
	}
}

func TestBackfillIncidentTraceID_HappyPath_Patches(t *testing.T) {
	mock := &mockRunStartAuditd{incidentByRun: &audit.Incident{IncidentID: "inc_abc"}} // TraceID unset
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.backfillIncidentTraceID(context.Background(), "plr_x", "tr_real01")

	calls := mock.calls(http.MethodPatch, "/v1/incidents/inc_abc")
	if len(calls) != 1 {
		t.Fatalf("PATCH /v1/incidents/inc_abc calls = %d, want 1", len(calls))
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode patch body: %v", err)
	}
	if body["trace_id"] != "tr_real01" {
		t.Errorf("trace_id = %q, want tr_real01", body["trace_id"])
	}
}

func TestClassifyIncidentAttribution_NoIncidentFound_NoPatch(t *testing.T) {
	mock := &mockRunStartAuditd{} // incidentByRun unset → 404
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL, plannerLLM: fleetPlannerLLM(t)}

	gw.classifyIncidentAttribution(context.Background(), "plr_x", "some diagnostic text")

	if calls := mock.calls(http.MethodPatch, "/v1/incidents/"); len(calls) != 0 {
		t.Errorf("PATCH /v1/incidents calls = %d, want 0 when no incident row exists for this run", len(calls))
	}
}

func TestClassifyIncidentAttribution_IncidentHasNoSeriesID_NoPatch(t *testing.T) {
	mock := &mockRunStartAuditd{incidentByRun: &audit.Incident{IncidentID: "inc_abc"}} // SeriesID unset
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL, plannerLLM: fleetPlannerLLM(t)}

	gw.classifyIncidentAttribution(context.Background(), "plr_x", "some diagnostic text")

	if calls := mock.calls(http.MethodPatch, "/v1/incidents/"); len(calls) != 0 {
		t.Errorf("PATCH /v1/incidents calls = %d, want 0 when the incident has no series_id to classify against", len(calls))
	}
}

func TestClassifyIncidentAttribution_NoPlannerLLM_NoPatch(t *testing.T) {
	mock := &mockRunStartAuditd{
		incidentByRun:      &audit.Incident{IncidentID: "inc_abc", SeriesID: "pbs_x"},
		playbookBySeriesID: &audit.Playbook{SeriesID: "pbs_x", RootCauseClasses: &audit.RootCauseClassification{Classes: []string{"a", "b"}}},
	}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL} // plannerLLM unset

	gw.classifyIncidentAttribution(context.Background(), "plr_x", "some diagnostic text")

	if calls := mock.calls(http.MethodPatch, "/v1/incidents/"); len(calls) != 0 {
		t.Errorf("PATCH /v1/incidents calls = %d, want 0 when no planner LLM is configured", len(calls))
	}
}

func TestClassifyIncidentAttribution_PlaybookHasNoRootCauseClasses_NoPatch(t *testing.T) {
	mock := &mockRunStartAuditd{
		incidentByRun:      &audit.Incident{IncidentID: "inc_abc", SeriesID: "pbs_x"},
		playbookBySeriesID: &audit.Playbook{SeriesID: "pbs_x"}, // RootCauseClasses unset
	}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL, plannerLLM: fleetPlannerLLM(t)}

	gw.classifyIncidentAttribution(context.Background(), "plr_x", "some diagnostic text")

	if calls := mock.calls(http.MethodPatch, "/v1/incidents/"); len(calls) != 0 {
		t.Errorf("PATCH /v1/incidents calls = %d, want 0 when the entry playbook declares no root_cause_classes", len(calls))
	}
}

func TestClassifyIncidentAttribution_HappyPath_PatchesAttribution(t *testing.T) {
	mock := &mockRunStartAuditd{
		incidentByRun: &audit.Incident{IncidentID: "inc_abc", SeriesID: "pbs_x"},
		playbookBySeriesID: &audit.Playbook{
			SeriesID:         "pbs_x",
			RootCauseClasses: &audit.RootCauseClassification{Version: "1.0.0", Classes: []string{"connection-pool-saturation", "connection-pool-leak"}},
		},
	}
	srv := mock.start(t)
	llmCalled := false
	gw := &Gateway{
		auditURL: srv.URL,
		plannerLLM: func(ctx context.Context, prompt string) (string, error) {
			llmCalled = true
			return "connection-pool-saturation", nil
		},
	}

	gw.classifyIncidentAttribution(context.Background(), "plr_x", "pool is saturated, root cause confirmed")

	if !llmCalled {
		t.Error("planner LLM was never invoked for classification")
	}
	calls := mock.calls(http.MethodPatch, "/v1/incidents/inc_abc")
	if len(calls) != 1 {
		t.Fatalf("PATCH /v1/incidents/inc_abc calls = %d, want 1", len(calls))
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode patch body: %v", err)
	}
	if body["attribution"] != "connection-pool-saturation" {
		t.Errorf("attribution = %q, want connection-pool-saturation", body["attribution"])
	}
}

func TestIsAttributableOutcome(t *testing.T) {
	cases := map[string]bool{
		audit.OutcomeResolved:          true,
		audit.OutcomeEscalated:         true,
		audit.OutcomeEscalatedResolved: true,
		audit.OutcomeAbandoned:         false,
		audit.OutcomeUnknown:           false,
		"gate_pending":                 false,
		"":                             false,
	}
	for outcome, want := range cases {
		if got := isAttributableOutcome(outcome); got != want {
			t.Errorf("isAttributableOutcome(%q) = %v, want %v", outcome, got, want)
		}
	}
}

func TestRecordPlaybookRunComplete_ResolvedOutcome_TriggersAttribution(t *testing.T) {
	mock := &mockRunStartAuditd{
		incidentByRun: &audit.Incident{IncidentID: "inc_abc", SeriesID: "pbs_x"},
		playbookBySeriesID: &audit.Playbook{
			SeriesID:         "pbs_x",
			RootCauseClasses: &audit.RootCauseClassification{Classes: []string{"connection-pool-saturation"}},
		},
	}
	srv := mock.start(t)
	gw := &Gateway{
		auditURL: srv.URL,
		plannerLLM: func(ctx context.Context, prompt string) (string, error) {
			return "connection-pool-saturation", nil
		},
	}

	gw.recordPlaybookRunComplete(context.Background(), "plr_x", audit.OutcomeResolved,
		"", "", "pool saturated", "pool is saturated, confirmed root cause", "", nil, false, "")

	// Phase 3 (status/resolved_at) and Phase 2 (attribution) each PATCH the
	// same incident row independently — two calls, not one.
	calls := mock.calls(http.MethodPatch, "/v1/incidents/inc_abc")
	if len(calls) != 2 {
		t.Fatalf("PATCH /v1/incidents/inc_abc calls = %d, want 2 (status+resolved_at, attribution) for a resolved outcome", len(calls))
	}
	var sawStatus, sawAttribution bool
	for _, c := range calls {
		var body map[string]any
		if err := json.Unmarshal([]byte(c.body), &body); err != nil {
			t.Fatalf("decode patch body: %v", err)
		}
		if body["status"] == audit.IncidentStatusResolved {
			sawStatus = true
			if body["resolved_at"] == nil || body["resolved_at"] == "" {
				t.Error("status patch missing resolved_at")
			}
		}
		if body["attribution"] == "connection-pool-saturation" {
			sawAttribution = true
		}
	}
	if !sawStatus {
		t.Error("no PATCH call set status=resolved")
	}
	if !sawAttribution {
		t.Error("no PATCH call set attribution=connection-pool-saturation")
	}
}

func TestRecordPlaybookRunComplete_AbandonedOutcome_StatusOnlyNoAttribution(t *testing.T) {
	mock := &mockRunStartAuditd{
		incidentByRun: &audit.Incident{IncidentID: "inc_abc", SeriesID: "pbs_x"},
		playbookBySeriesID: &audit.Playbook{
			SeriesID:         "pbs_x",
			RootCauseClasses: &audit.RootCauseClassification{Classes: []string{"connection-pool-saturation"}},
		},
	}
	srv := mock.start(t)
	gw := &Gateway{
		auditURL:   srv.URL,
		plannerLLM: fleetPlannerLLM(t),
	}

	gw.recordPlaybookRunComplete(context.Background(), "plr_x", audit.OutcomeAbandoned,
		"", "", "gate denied", "", "", nil, false, "operator denied")

	// Abandoned is terminal (status patch fires — the investigation is over)
	// but not attributable (nothing to classify).
	calls := mock.calls(http.MethodPatch, "/v1/incidents/inc_abc")
	if len(calls) != 1 {
		t.Fatalf("PATCH /v1/incidents/inc_abc calls = %d, want 1 (status only) for an abandoned outcome", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode patch body: %v", err)
	}
	if body["status"] != audit.IncidentStatusAbandoned {
		t.Errorf("status = %v, want abandoned", body["status"])
	}
	if _, hasAttribution := body["attribution"]; hasAttribution {
		t.Error("abandoned outcome's status patch should not carry an attribution field")
	}
}

func TestRecordPlaybookRunComplete_UnknownOutcome_NoIncidentPatchesAtAll(t *testing.T) {
	mock := &mockRunStartAuditd{
		incidentByRun: &audit.Incident{IncidentID: "inc_abc", SeriesID: "pbs_x"},
	}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL, plannerLLM: fleetPlannerLLM(t)}

	gw.recordPlaybookRunComplete(context.Background(), "plr_x", audit.OutcomeUnknown,
		"", "", "", "", "", nil, false, "")

	if calls := mock.calls(http.MethodPatch, "/v1/incidents/inc_abc"); len(calls) != 0 {
		t.Errorf("PATCH /v1/incidents/inc_abc calls = %d, want 0 for outcome=unknown (not terminal, nothing concluded)", len(calls))
	}
}

// TestHandlePlaybookRun_ResolvedOutcome_ViaRealHTTPHandler_ClassifiesAttribution
// closes the gap all the direct-call tests above can't: it drives a genuine
// single-hop agent resolution through the real HTTP handler chain (a real A2A
// agent response, real FINDINGS/outcome computation in handlePlaybookRunAsAgent,
// real async recordPlaybookRunComplete) and proves the resulting PATCH carries
// the classifier's real output — not a hand-constructed outcome/response-text
// pair passed directly to recordPlaybookRunComplete, which only proves that
// function's own parameter handling (already covered by the direct-call tests
// above).
func TestHandlePlaybookRun_ResolvedOutcome_ViaRealHTTPHandler_ClassifiesAttribution(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_inc_attr01",
		SeriesID:      "pbs_inc_attr01",
		Name:          "Incident Attribution Wiring Test",
		ExecutionMode: "agent",
		AgentName:     "attr_test_agent",
		IsActive:      true,
	}
	mock := &mockRunStartAuditd{
		playbook:      pb,
		incidentByRun: &audit.Incident{IncidentID: "inc_attr01", SeriesID: pb.SeriesID},
		playbookBySeriesID: &audit.Playbook{
			SeriesID:         pb.SeriesID,
			RootCauseClasses: &audit.RootCauseClassification{Classes: []string{"connection-pool-saturation"}},
		},
	}
	srv := mock.start(t)

	// No ESCALATE_TO/TRANSITION_TO → handlePlaybookRunAsAgent's own signal-line
	// parsing sets outcome="resolved" (playbooks.go:636) — not something this
	// test fabricates.
	_, card := mockA2AServerWithText(t, "attr_test_agent",
		"FINDINGS: pool is saturated, root cause confirmed.\n")
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("create A2A client: %v", err)
	}

	gw := makePlaybookRunGateway(srv.URL, nil)
	gw.clients = map[string]*a2aclient.Client{"attr_test_agent": client}
	gw.plannerLLM = func(ctx context.Context, prompt string) (string, error) {
		return "connection-pool-saturation", nil
	}

	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"pool alert"}`)
	if rec.Code == http.StatusBadGateway {
		t.Fatalf("got 502; body: %s", rec.Body.String())
	}

	// recordPlaybookRunComplete (and the classification/status-close it now
	// triggers — Phase 2 and Phase 3 of the v0.29 incident-entity design each
	// PATCH this row independently) fires via "go" (fire-and-forget) — poll
	// briefly instead of racing it, same pattern used elsewhere in this file.
	var sawAttribution bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range mock.calls(http.MethodPatch, "/v1/incidents/inc_attr01") {
			var body map[string]string
			if err := json.Unmarshal([]byte(c.body), &body); err == nil && body["attribution"] == "connection-pool-saturation" {
				sawAttribution = true
			}
		}
		if sawAttribution {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !sawAttribution {
		t.Fatal("no PATCH /v1/incidents/inc_attr01 carried attribution=connection-pool-saturation — a real HTTP-driven resolution did not trigger attribution classification")
	}
}

// --- Phase 3: status/resolved_at close-out + bundle auto-trigger ---

func TestIsTerminalIncidentOutcome(t *testing.T) {
	cases := map[string]bool{
		audit.OutcomeResolved:          true,
		audit.OutcomeEscalated:         true,
		audit.OutcomeEscalatedResolved: true,
		audit.OutcomeAbandoned:         true,
		audit.OutcomeUnknown:           false,
		"gate_pending":                 false,
		"":                             false,
	}
	for outcome, want := range cases {
		if got := isTerminalIncidentOutcome(outcome); got != want {
			t.Errorf("isTerminalIncidentOutcome(%q) = %v, want %v", outcome, got, want)
		}
	}
}

func TestIncidentStatusForOutcome(t *testing.T) {
	cases := map[string]string{
		audit.OutcomeResolved:          audit.IncidentStatusResolved,
		audit.OutcomeEscalatedResolved: audit.IncidentStatusResolved,
		audit.OutcomeEscalated:         audit.IncidentStatusEscalated,
		audit.OutcomeAbandoned:         audit.IncidentStatusAbandoned,
	}
	for outcome, want := range cases {
		if got := incidentStatusForOutcome(outcome); got != want {
			t.Errorf("incidentStatusForOutcome(%q) = %q, want %q", outcome, got, want)
		}
	}
}

func TestCloseIncidentRecord_NoIncidentFound_NoPatch(t *testing.T) {
	mock := &mockRunStartAuditd{} // incidentByRun unset -> 404
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.closeIncidentRecord(context.Background(), "plr_x", audit.OutcomeResolved)

	if calls := mock.calls(http.MethodPatch, "/v1/incidents/"); len(calls) != 0 {
		t.Errorf("PATCH /v1/incidents calls = %d, want 0 when no incident row exists", len(calls))
	}
}

func TestCloseIncidentRecord_Found_PatchesStatusAndResolvedAt(t *testing.T) {
	mock := &mockRunStartAuditd{incidentByRun: &audit.Incident{IncidentID: "inc_close01"}}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.closeIncidentRecord(context.Background(), "plr_x", audit.OutcomeEscalated)

	calls := mock.calls(http.MethodPatch, "/v1/incidents/inc_close01")
	if len(calls) != 1 {
		t.Fatalf("PATCH calls = %d, want 1", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != audit.IncidentStatusEscalated {
		t.Errorf("status = %v, want escalated", body["status"])
	}
	if body["resolved_at"] == nil || body["resolved_at"] == "" {
		t.Error("resolved_at missing or empty")
	}
}

// mockIncidentDirectAgent serves POST /tool/{name} the way agents/incident's
// direct-tool registry does, recording requests and returning a canned
// directToolResp so triggerIncidentBundle's HTTP mechanics can be tested
// without a real incident agent process.
type mockIncidentDirectAgent struct {
	mu       sync.Mutex
	requests []capturedRequest
	status   int    // 0 = 200
	output   string // JSON-encoded incidentBundleResult; empty uses a default
	toolErr  string
}

func (m *mockIncidentDirectAgent) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf strings.Builder
		io.Copy(&buf, r.Body) //nolint:errcheck
		m.mu.Lock()
		m.requests = append(m.requests, capturedRequest{method: r.Method, path: r.URL.Path, body: buf.String()})
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		status := m.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		output := m.output
		if output == "" && m.toolErr == "" {
			output = `{"incident_id":"a1b2c3d4","bundle_path":"/data/incidents/a1b2c3d4.tar.gz","timestamp":"20260101-000000","layers":["os","storage"]}`
		}
		json.NewEncoder(w).Encode(map[string]string{"output": output, "error": m.toolErr}) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (m *mockIncidentDirectAgent) calls() []capturedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]capturedRequest(nil), m.requests...)
}

func TestTriggerIncidentBundle_FlagOff_NoCall(t *testing.T) {
	agentMock := &mockIncidentDirectAgent{}
	agentSrv := agentMock.start(t)
	auditMock := &mockRunStartAuditd{incidentByRun: &audit.Incident{IncidentID: "inc_x"}}
	auditSrv := auditMock.start(t)

	gw := &Gateway{
		auditURL:           auditSrv.URL,
		autoIncidentBundle: false,
		agents:             map[string]*discovery.Agent{agentNameIncident: {InvokeURL: agentSrv.URL + "/invoke"}},
	}

	gw.triggerIncidentBundle(context.Background(), "plr_x", audit.OutcomeResolved, "findings")

	if len(agentMock.calls()) != 0 {
		t.Error("incident agent should not be called when autoIncidentBundle is off")
	}
}

func TestTriggerIncidentBundle_AgentNotWired_NoCall(t *testing.T) {
	auditMock := &mockRunStartAuditd{incidentByRun: &audit.Incident{IncidentID: "inc_x"}}
	auditSrv := auditMock.start(t)

	gw := &Gateway{
		auditURL:           auditSrv.URL,
		autoIncidentBundle: true,
		agents:             map[string]*discovery.Agent{}, // incident agent absent
	}

	// Should not panic even though there's nowhere to call.
	gw.triggerIncidentBundle(context.Background(), "plr_x", audit.OutcomeResolved, "findings")
}

func TestTriggerIncidentBundle_NoIncidentFound_NoCall(t *testing.T) {
	agentMock := &mockIncidentDirectAgent{}
	agentSrv := agentMock.start(t)
	auditMock := &mockRunStartAuditd{} // incidentByRun unset -> 404
	auditSrv := auditMock.start(t)

	gw := &Gateway{
		auditURL:           auditSrv.URL,
		autoIncidentBundle: true,
		agents:             map[string]*discovery.Agent{agentNameIncident: {InvokeURL: agentSrv.URL + "/invoke"}},
	}

	gw.triggerIncidentBundle(context.Background(), "plr_x", audit.OutcomeResolved, "findings")

	if len(agentMock.calls()) != 0 {
		t.Error("incident agent should not be called when no incident row exists for this run")
	}
}

func TestTriggerIncidentBundle_HappyPath_CallsDirectToolWithArgs(t *testing.T) {
	agentMock := &mockIncidentDirectAgent{}
	agentSrv := agentMock.start(t)
	auditMock := &mockRunStartAuditd{
		incidentByRun: &audit.Incident{IncidentID: "inc_bundle01", TraceID: "tr_bundle01"},
		priorRun:      &audit.PlaybookRun{RunID: "plr_x", ConnectionString: "postgres://localhost/test"},
	}
	auditSrv := auditMock.start(t)

	gw := &Gateway{
		auditURL:           auditSrv.URL,
		autoIncidentBundle: true,
		agents:             map[string]*discovery.Agent{agentNameIncident: {InvokeURL: agentSrv.URL + "/invoke"}},
	}

	gw.triggerIncidentBundle(context.Background(), "plr_x", audit.OutcomeResolved, "pool exhausted")

	calls := agentMock.calls()
	if len(calls) != 1 {
		t.Fatalf("direct tool calls = %d, want 1", len(calls))
	}
	if calls[0].path != "/tool/create_incident_bundle" {
		t.Errorf("path = %q, want /tool/create_incident_bundle", calls[0].path)
	}
	var req directToolReq
	if err := json.Unmarshal([]byte(calls[0].body), &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if req.TraceID != "tr_bundle01" {
		t.Errorf("trace_id = %q, want tr_bundle01", req.TraceID)
	}
	if req.Args["incident_id"] != "inc_bundle01" {
		t.Errorf("args.incident_id = %v, want inc_bundle01", req.Args["incident_id"])
	}
	if req.Args["connection_string"] != "postgres://localhost/test" {
		t.Errorf("args.connection_string = %v, want the fetched PlaybookRun's ConnectionString", req.Args["connection_string"])
	}
	if req.Args["outcome"] != audit.OutcomeResolved {
		t.Errorf("args.outcome = %v, want resolved", req.Args["outcome"])
	}
}

func TestTriggerIncidentBundle_SeriesIDSet_PassedForImprovementMode(t *testing.T) {
	agentMock := &mockIncidentDirectAgent{}
	agentSrv := agentMock.start(t)
	auditMock := &mockRunStartAuditd{
		incidentByRun: &audit.Incident{IncidentID: "inc_bundle02", TraceID: "tr_bundle02", SeriesID: "pbs_vacuum_triage"},
	}
	auditSrv := auditMock.start(t)

	gw := &Gateway{
		auditURL:           auditSrv.URL,
		autoIncidentBundle: true,
		agents:             map[string]*discovery.Agent{agentNameIncident: {InvokeURL: agentSrv.URL + "/invoke"}},
	}

	gw.triggerIncidentBundle(context.Background(), "plr_x", audit.OutcomeResolved, "pool exhausted")

	calls := agentMock.calls()
	if len(calls) != 1 {
		t.Fatalf("direct tool calls = %d, want 1", len(calls))
	}
	var req directToolReq
	if err := json.Unmarshal([]byte(calls[0].body), &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if req.Args["series_id"] != "pbs_vacuum_triage" {
		t.Errorf("args.series_id = %v, want pbs_vacuum_triage (the entry incident's own series, for improvement mode)", req.Args["series_id"])
	}
}

func TestTriggerIncidentBundle_NoSeriesID_OmittedFromArgs(t *testing.T) {
	agentMock := &mockIncidentDirectAgent{}
	agentSrv := agentMock.start(t)
	auditMock := &mockRunStartAuditd{
		incidentByRun: &audit.Incident{IncidentID: "inc_bundle03", TraceID: "tr_bundle03"},
	}
	auditSrv := auditMock.start(t)

	gw := &Gateway{
		auditURL:           auditSrv.URL,
		autoIncidentBundle: true,
		agents:             map[string]*discovery.Agent{agentNameIncident: {InvokeURL: agentSrv.URL + "/invoke"}},
	}

	gw.triggerIncidentBundle(context.Background(), "plr_x", audit.OutcomeResolved, "pool exhausted")

	calls := agentMock.calls()
	if len(calls) != 1 {
		t.Fatalf("direct tool calls = %d, want 1", len(calls))
	}
	var req directToolReq
	if err := json.Unmarshal([]byte(calls[0].body), &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if _, ok := req.Args["series_id"]; ok {
		t.Errorf("args.series_id should be omitted when the incident has no SeriesID, got %v", req.Args["series_id"])
	}
}

func TestTriggerIncidentBundle_PlaybookRunFetchFails_StillCallsWithoutConnectionString(t *testing.T) {
	agentMock := &mockIncidentDirectAgent{}
	agentSrv := agentMock.start(t)
	// priorRun unset -> fetchPlaybookRun 404s; triggerIncidentBundle must
	// still proceed (best-effort degrade to no connection_string, matching
	// create_incident_bundle's own "layer skipped if absent" semantics).
	auditMock := &mockRunStartAuditd{incidentByRun: &audit.Incident{IncidentID: "inc_bundle02"}}
	auditSrv := auditMock.start(t)

	gw := &Gateway{
		auditURL:           auditSrv.URL,
		autoIncidentBundle: true,
		agents:             map[string]*discovery.Agent{agentNameIncident: {InvokeURL: agentSrv.URL + "/invoke"}},
	}

	gw.triggerIncidentBundle(context.Background(), "plr_x", audit.OutcomeResolved, "findings")

	calls := agentMock.calls()
	if len(calls) != 1 {
		t.Fatalf("direct tool calls = %d, want 1 (best-effort even when the PlaybookRun fetch fails)", len(calls))
	}
	var req directToolReq
	if err := json.Unmarshal([]byte(calls[0].body), &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if _, has := req.Args["connection_string"]; has {
		t.Error("connection_string should be absent when the PlaybookRun fetch failed")
	}
}

func TestTriggerIncidentBundle_AgentToolError_DoesNotPanic(t *testing.T) {
	agentMock := &mockIncidentDirectAgent{toolErr: "psql: connection refused"}
	agentSrv := agentMock.start(t)
	auditMock := &mockRunStartAuditd{incidentByRun: &audit.Incident{IncidentID: "inc_bundle03"}}
	auditSrv := auditMock.start(t)

	gw := &Gateway{
		auditURL:           auditSrv.URL,
		autoIncidentBundle: true,
		agents:             map[string]*discovery.Agent{agentNameIncident: {InvokeURL: agentSrv.URL + "/invoke"}},
	}

	// Best-effort: an agent-side tool error must not panic or propagate.
	gw.triggerIncidentBundle(context.Background(), "plr_x", audit.OutcomeResolved, "findings")
}

func TestCallIncidentDirectTool_DecodesResult(t *testing.T) {
	agentMock := &mockIncidentDirectAgent{
		output: `{"incident_id":"xyz","bundle_path":"/data/incidents/xyz.tar.gz","layers":["database","os"]}`,
	}
	agentSrv := agentMock.start(t)
	gw := &Gateway{}

	result, err := gw.callIncidentDirectTool(context.Background(), agentSrv.URL+"/invoke", "create_incident_bundle", map[string]any{"incident_id": "inc_x"}, "tr_x")
	if err != nil {
		t.Fatalf("callIncidentDirectTool: %v", err)
	}
	if result.BundlePath != "/data/incidents/xyz.tar.gz" {
		t.Errorf("BundlePath = %q, want /data/incidents/xyz.tar.gz", result.BundlePath)
	}
	if len(result.Layers) != 2 {
		t.Errorf("Layers = %v, want 2 entries", result.Layers)
	}
}

func TestCallIncidentDirectTool_ErrorStatus_ReturnsError(t *testing.T) {
	agentMock := &mockIncidentDirectAgent{status: http.StatusUnprocessableEntity, toolErr: "psql: connection refused"}
	agentSrv := agentMock.start(t)
	gw := &Gateway{}

	_, err := gw.callIncidentDirectTool(context.Background(), agentSrv.URL+"/invoke", "create_incident_bundle", map[string]any{}, "")
	if err == nil {
		t.Fatal("expected an error for a 422 tool response, got nil")
	}
	if !strings.Contains(err.Error(), "psql: connection refused") {
		t.Errorf("error = %v, want it to include the agent's tool error", err)
	}
}

// TestHandlePlaybookRun_ResolvedOutcome_ViaRealHTTPHandler_TriggersIncidentBundle
// is the bundle-trigger sibling of ..._ClassifiesAttribution above — same gap,
// same fix: every TestTriggerIncidentBundle_* test calls gw.triggerIncidentBundle
// directly, which only proves that function's own parameter handling, not that
// a real HTTP-driven resolution (real A2A response, real outcome computation,
// real async recordPlaybookRunComplete) actually reaches it with
// autoIncidentBundle=true.
func TestHandlePlaybookRun_ResolvedOutcome_ViaRealHTTPHandler_TriggersIncidentBundle(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_inc_bundle01",
		SeriesID:      "pbs_inc_bundle01",
		Name:          "Incident Bundle Trigger Wiring Test",
		ExecutionMode: "agent",
		AgentName:     "bundle_test_agent",
		IsActive:      true,
	}
	// SeriesID deliberately has no matching playbookBySeriesID/RootCauseClasses
	// on the mock — classification cleanly no-ops (SeriesID lookup finds
	// nothing to classify against), keeping this test focused on the bundle
	// trigger alone.
	auditMock := &mockRunStartAuditd{
		playbook:      pb,
		incidentByRun: &audit.Incident{IncidentID: "inc_bundle_e2e01", TraceID: "tr_bundle_e2e01"},
		priorRun:      &audit.PlaybookRun{RunID: "plr_entry01", ConnectionString: "postgres://localhost/test"},
	}
	auditSrv := auditMock.start(t)

	agentMock := &mockIncidentDirectAgent{}
	incidentAgentSrv := agentMock.start(t)

	_, card := mockA2AServerWithText(t, "bundle_test_agent",
		"FINDINGS: disk is full, root cause confirmed.\n")
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("create A2A client: %v", err)
	}

	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	gw.clients = map[string]*a2aclient.Client{"bundle_test_agent": client}
	gw.autoIncidentBundle = true
	gw.agents = map[string]*discovery.Agent{agentNameIncident: {InvokeURL: incidentAgentSrv.URL + "/invoke"}}

	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"disk alert"}`)
	if rec.Code == http.StatusBadGateway {
		t.Fatalf("got 502; body: %s", rec.Body.String())
	}

	// Fires via the same fire-and-forget recordPlaybookRunComplete path —
	// poll instead of racing it.
	var calls []capturedRequest
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		calls = agentMock.calls()
		if len(calls) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(calls) == 0 {
		t.Fatal("incident agent's direct-tool endpoint was never called — a real HTTP-driven resolution did not trigger the incident bundle")
	}
	if calls[0].path != "/tool/create_incident_bundle" {
		t.Errorf("path = %q, want /tool/create_incident_bundle", calls[0].path)
	}
	var req directToolReq
	if err := json.Unmarshal([]byte(calls[0].body), &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if req.Args["incident_id"] != "inc_bundle_e2e01" {
		t.Errorf("args.incident_id = %v, want inc_bundle_e2e01", req.Args["incident_id"])
	}
	if req.Args["connection_string"] != "postgres://localhost/test" {
		t.Errorf("args.connection_string = %v, want postgres://localhost/test", req.Args["connection_string"])
	}
}

// --- Phase 4: faulttest origin tagging ---

func TestCreateIncidentRecord_FaulttestOrigin_SetOnIncident(t *testing.T) {
	mock := &mockRunStartAuditd{}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.createIncidentRecord(context.Background(), "plr_x", "trace_x", "pbs_x", audit.IncidentOriginFaulttest)

	calls := mock.calls(http.MethodPost, "/v1/incidents")
	if len(calls) != 1 {
		t.Fatalf("POST /v1/incidents calls = %d, want 1", len(calls))
	}
	var body audit.Incident
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Origin != audit.IncidentOriginFaulttest {
		t.Errorf("origin = %q, want %q", body.Origin, audit.IncidentOriginFaulttest)
	}
}

func TestCreateIncidentRecord_UnrecognizedOrigin_TreatedAsUnset(t *testing.T) {
	mock := &mockRunStartAuditd{}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	// A caller (or a bug) sending an arbitrary origin string must not land in
	// the incidents table verbatim — only the recognized "faulttest" value
	// survives; anything else is treated the same as unset (IncidentStore.Create
	// then defaults it to "real" server-side).
	gw.createIncidentRecord(context.Background(), "plr_x", "trace_x", "pbs_x", "not-a-real-origin-value")

	calls := mock.calls(http.MethodPost, "/v1/incidents")
	if len(calls) != 1 {
		t.Fatalf("POST /v1/incidents calls = %d, want 1", len(calls))
	}
	var body audit.Incident
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Origin != "" {
		t.Errorf("origin = %q, want empty (unrecognized value must not pass through)", body.Origin)
	}
}

func TestRecordPlaybookRunStart_EntryPointRun_FaulttestOrigin_ThreadedThrough(t *testing.T) {
	mock := &mockRunStartAuditd{}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	pb := &audit.Playbook{PlaybookID: "pb_test", SeriesID: "series1"}
	gw.recordPlaybookRunStart(context.Background(), pb, "ctx1", "", "", "diagnostic",
		"trace_abc", "", "", "operator1", audit.IncidentOriginFaulttest)

	calls := mock.calls(http.MethodPost, "/v1/incidents")
	if len(calls) != 1 {
		t.Fatalf("POST /v1/incidents calls = %d, want 1", len(calls))
	}
	var body audit.Incident
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Origin != audit.IncidentOriginFaulttest {
		t.Errorf("origin = %q, want %q — recordPlaybookRunStart must thread its origin param through to createIncidentRecord", body.Origin, audit.IncidentOriginFaulttest)
	}
}

// TestHandlePlaybookRun_FaulttestOriginInRequestBody_ViaRealHTTPHandler proves
// the full real-HTTP-driven path: a request body carrying "origin":"faulttest"
// (exactly what testing/faultlib/runner.go now sends on every request) results
// in an incidents-table row tagged origin="faulttest" — not just that
// recordPlaybookRunStart's own parameter handling is correct in isolation.
func TestHandlePlaybookRun_FaulttestOriginInRequestBody_ViaRealHTTPHandler(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_origin01",
		SeriesID:      "pbs_origin01",
		ExecutionMode: "fleet",
		IsActive:      true,
	}
	mock := &mockRunStartAuditd{playbook: pb}
	srv := mock.start(t)
	gw := makePlaybookRunGateway(srv.URL, fleetPlannerLLM(t))

	rec := postPlaybookRun(t, gw, pb.PlaybookID, `{"origin":"faulttest"}`)
	if rec.Code == http.StatusBadGateway {
		t.Fatalf("got 502; body: %s", rec.Body.String())
	}

	calls := mock.calls(http.MethodPost, "/v1/incidents")
	if len(calls) != 1 {
		t.Fatalf("POST /v1/incidents calls = %d, want 1", len(calls))
	}
	var body audit.Incident
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Origin != audit.IncidentOriginFaulttest {
		t.Errorf("origin = %q, want %q — a real request body's origin field did not reach the incidents table", body.Origin, audit.IncidentOriginFaulttest)
	}
}
