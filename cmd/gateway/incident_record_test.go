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
	gw.createIncidentRecord(context.Background(), "plr_x", "trace_x", "pbs_x")
}

func TestCreateIncidentRecord_EmptyRunID_NoOp(t *testing.T) {
	mock := &mockRunStartAuditd{}
	srv := mock.start(t)
	gw := &Gateway{auditURL: srv.URL}

	gw.createIncidentRecord(context.Background(), "", "trace_x", "pbs_x")

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
		"trace_abc", "", "", "operator1")

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

	if calls := mock.calls(http.MethodPatch, "/v1/incidents/inc_abc"); len(calls) != 1 {
		t.Errorf("PATCH /v1/incidents/inc_abc calls = %d, want 1 for a resolved outcome", len(calls))
	}
}

func TestRecordPlaybookRunComplete_AbandonedOutcome_NoAttribution(t *testing.T) {
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

	if calls := mock.calls(http.MethodPatch, "/v1/incidents/inc_abc"); len(calls) != 0 {
		t.Errorf("PATCH /v1/incidents/inc_abc calls = %d, want 0 for an abandoned outcome (nothing to classify)", len(calls))
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

	// recordPlaybookRunComplete (and the classification it now triggers) fires
	// via "go" (fire-and-forget) — poll briefly instead of racing it, same
	// pattern used elsewhere in this file for the same reason.
	var patchBody string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls := mock.calls(http.MethodPatch, "/v1/incidents/inc_attr01"); len(calls) > 0 {
			patchBody = calls[0].body
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if patchBody == "" {
		t.Fatal("PATCH /v1/incidents/inc_attr01 never arrived — a real HTTP-driven resolution did not trigger attribution classification")
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(patchBody), &body); err != nil {
		t.Fatalf("decode patch body: %v", err)
	}
	if body["attribution"] != "connection-pool-saturation" {
		t.Errorf("attribution = %q, want connection-pool-saturation", body["attribution"])
	}
}
