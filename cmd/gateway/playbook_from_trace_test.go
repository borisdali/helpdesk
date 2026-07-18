package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helpdesk/internal/audit"
)

// newFromTraceGateway returns a minimal Gateway for from-trace handler tests.
func newFromTraceGateway(llmFn func(ctx context.Context, prompt string) (string, error), auditURL, auditAPIKey string) *Gateway {
	g := &Gateway{
		auditURL:    auditURL,
		auditAPIKey: auditAPIKey,
	}
	if llmFn != nil {
		g.plannerLLM = llmFn
	}
	return g
}

func doFromTraceRequest(t *testing.T, g *Gateway, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/playbooks/from-trace", bytes.NewReader(data))
	w := httptest.NewRecorder()
	g.handlePlaybookFromTrace(w, req)
	return w
}

// ── handlePlaybookFromTrace ───────────────────────────────────────────────

func TestHandlePlaybookFromTrace_MissingTraceID(t *testing.T) {
	g := newFromTraceGateway(nil, "", "")
	w := doFromTraceRequest(t, g, map[string]string{"outcome": "resolved"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandlePlaybookFromTrace_NoLLM(t *testing.T) {
	// No LLM configured → 503 before even reaching trace fetch.
	g := newFromTraceGateway(nil, "", "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_abc"})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandlePlaybookFromTrace_NoAuditd_Returns503(t *testing.T) {
	// No auditd → cannot fetch trace → 503 rather than hallucinating a draft.
	llm := func(_ context.Context, _ string) (string, error) {
		return "name: Should not be called\n", nil
	}
	g := newFromTraceGateway(llm, "", "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_123", "outcome": "resolved"})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body: %s", w.Code, w.Body.String())
	}
}

func TestHandlePlaybookFromTrace_EmptyTrace_Returns422(t *testing.T) {
	// auditd reachable but returns no events for the trace → 422.
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer auditd.Close()

	llm := func(_ context.Context, _ string) (string, error) {
		return "name: Should not be called\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_empty"})
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422; body: %s", w.Code, w.Body.String())
	}
}

func TestHandlePlaybookFromTrace_LLMError(t *testing.T) {
	// auditd returns events, but LLM call fails → 500.
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
	}))
	defer auditd.Close()

	llm := func(_ context.Context, _ string) (string, error) {
		return "", context.DeadlineExceeded
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_abc"})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestHandlePlaybookFromTrace_DefaultOutcome(t *testing.T) {
	// outcome defaults to "resolved" when omitted; verify prompt contains it.
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
	}))
	defer auditd.Close()

	var gotPrompt string
	llm := func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "name: Test\ndescription: Test desc\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_xyz"})
	if !strings.Contains(gotPrompt, "resolved") {
		t.Errorf("prompt should contain 'resolved'; got: %s", gotPrompt[:min(200, len(gotPrompt))])
	}
}

func TestHandlePlaybookFromTrace_StripMarkdownFences(t *testing.T) {
	// LLM wraps YAML in markdown fences — should be stripped.
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
	}))
	defer auditd.Close()

	llm := func(_ context.Context, _ string) (string, error) {
		return "```yaml\nname: Test\ndescription: desc\n```", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_fence"})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp PlaybookFromTraceResponse
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if strings.Contains(resp.Draft, "```") {
		t.Errorf("draft should not contain markdown fences; got: %s", resp.Draft)
	}
}

func TestHandlePlaybookFromTrace_SuccessWithAuditd(t *testing.T) {
	// auditd returns events → LLM synthesizes → draft persisted → playbook_id returned.
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
			return
		}
		// POST /v1/fleet/playbooks — persist the draft.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(audit.Playbook{PlaybookID: "pb_generated_abc123"}) //nolint:errcheck
	}))
	defer auditd.Close()

	llm := func(_ context.Context, _ string) (string, error) {
		return "name: Test Playbook\ndescription: Restart triage.\nproblem_class: availability\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_persist", "outcome": "resolved"})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp PlaybookFromTraceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.PlaybookID == "" {
		t.Error("expected non-empty playbook_id when auditd is configured")
	}
	if resp.Draft == "" {
		t.Error("expected non-empty draft")
	}
}

func TestHandlePlaybookFromTrace_AuditdPersistFails_DraftStillReturned(t *testing.T) {
	// auditd fetch succeeds but persist POST fails → draft still returned, playbook_id empty.
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer auditd.Close()

	llm := func(_ context.Context, _ string) (string, error) {
		return "name: Test\ndescription: desc\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_err"})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp PlaybookFromTraceResponse
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp.Draft == "" {
		t.Error("draft should be non-empty even when auditd persist fails")
	}
	if resp.PlaybookID != "" {
		t.Errorf("playbook_id should be empty when persist fails, got %q", resp.PlaybookID)
	}
}

// fakeTraceEvents is a minimal non-empty tool execution trace for tests that
// need auditd to return real content so the handler proceeds past the empty-trace guard.
const fakeTraceEvents = `[{"event_type":"tool_execution","tool_name":"get_active_connections","result":"42 connections active","trace_id":"tr_test"}]`

// ── persistPlaybookDraft ──────────────────────────────────────────────────

func TestPersistPlaybookDraft_Success(t *testing.T) {
	var gotBody []byte
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = readAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(audit.Playbook{PlaybookID: "pb_new_001"}) //nolint:errcheck
	}))
	defer srv.Close()

	g := &Gateway{auditURL: srv.URL, auditAPIKey: "svc-key"}
	pb := &audit.Playbook{
		Name:        "Test",
		Description: "desc",
		Source:      "generated",
	}
	id, err := g.persistPlaybookDraft(context.Background(), pb)
	if err != nil {
		t.Fatalf("persistPlaybookDraft: %v", err)
	}
	if id != "pb_new_001" {
		t.Errorf("playbook_id = %q, want pb_new_001", id)
	}
	if gotAuth != "Bearer svc-key" {
		t.Errorf("Authorization = %q, want Bearer svc-key", gotAuth)
	}
	// Verify the body was valid JSON with the playbook.
	var decoded audit.Playbook
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Errorf("request body was not valid JSON: %v", err)
	}
}

func TestPersistPlaybookDraft_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid")) //nolint:errcheck
	}))
	defer srv.Close()

	g := &Gateway{auditURL: srv.URL}
	_, err := g.persistPlaybookDraft(context.Background(), &audit.Playbook{})
	if err == nil {
		t.Error("expected error for 400 response, got nil")
	}
}

func TestPersistPlaybookDraft_NoAuditURL(t *testing.T) {
	g := &Gateway{auditURL: ""}
	_, err := g.persistPlaybookDraft(context.Background(), &audit.Playbook{})
	if err == nil {
		t.Error("expected error when auditURL is empty, got nil")
	}
}

func TestPersistPlaybookDraft_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json")) //nolint:errcheck
	}))
	defer srv.Close()

	g := &Gateway{auditURL: srv.URL}
	_, err := g.persistPlaybookDraft(context.Background(), &audit.Playbook{})
	if err == nil {
		t.Error("expected error for invalid JSON response, got nil")
	}
}

// ── fetchStepsAsTraceEvents fallback ─────────────────────────────────────

// fakeStepsResponse is a minimal /v1/fleet/playbook-runs/{id}/steps response.
const fakeStepsResponse = `{"steps":[
  {"step_index":0,"agent":"db-agent","tool":"get_active_connections","args":{},"reason":"check connections","status":"success","result":"42 connections"},
  {"step_index":1,"agent":"db-agent","tool":"terminate_idle_connections","args":{"min_idle_minutes":5},"reason":"terminate idle","status":"success","result":"terminated 8"}
]}`

func TestHandlePlaybookFromTrace_FallsBackToStepsWhenEventsEmpty(t *testing.T) {
	// audit_events returns [] for a plr_* trace ID → gateway must call the steps
	// endpoint and format results as tool_execution events for the LLM.
	var llmPrompt string
	stepsEndpointCalled := false

	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/events"):
			// audit_events: return empty array (the plr_* case).
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]")) //nolint:errcheck
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/steps"):
			stepsEndpointCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fakeStepsResponse)) //nolint:errcheck
		case r.Method == http.MethodPost:
			// persist draft
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(audit.Playbook{PlaybookID: "pb_steps_001"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer auditd.Close()

	llm := func(_ context.Context, prompt string) (string, error) {
		llmPrompt = prompt
		return "name: Steps-derived Playbook\ndescription: from steps\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "plr_abc123", "outcome": "resolved"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !stepsEndpointCalled {
		t.Error("expected steps endpoint to be called when audit_events is empty for plr_* trace")
	}
	if !strings.Contains(llmPrompt, "terminate_idle_connections") {
		t.Errorf("LLM prompt should contain step tool names; got: %s", llmPrompt)
	}
	if !strings.Contains(llmPrompt, "tool_execution") {
		t.Errorf("LLM prompt should contain formatted trace events; got: %s", llmPrompt)
	}
}

func TestHandlePlaybookFromTrace_NoFallbackForNonPlrPrefix(t *testing.T) {
	// audit_events returns [] for an ar_* or tr_* trace ID — must NOT fall back
	// to steps (those IDs are not run IDs and the steps endpoint would 404).
	stepsEndpointCalled := false
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/steps") {
			stepsEndpointCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]")) //nolint:errcheck
	}))
	defer auditd.Close()

	// Must provide a non-nil LLM so the handler passes the nil-LLM guard and
	// reaches the trace-fetch path where the no-fallback check lives.
	llm := func(_ context.Context, _ string) (string, error) {
		return "", fmt.Errorf("should not be called")
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "ar_no_fallback"})
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (empty trace, no fallback)", w.Code)
	}
	if stepsEndpointCalled {
		t.Error("steps endpoint must not be called for non-plr_ trace IDs")
	}
}

func TestFetchStepsAsTraceEvents_FormatsAsToolExecutionEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/steps") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fakeStepsResponse)) //nolint:errcheck
	}))
	defer srv.Close()

	g := &Gateway{auditURL: srv.URL}
	result, err := g.fetchStepsAsTraceEvents("plr_test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var events []map[string]any
	if err := json.Unmarshal([]byte(result), &events); err != nil {
		t.Fatalf("result is not valid JSON: %v\nraw: %s", err, result)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	for i, ev := range events {
		if ev["event_type"] != "tool_execution" {
			t.Errorf("event[%d].event_type = %q, want tool_execution", i, ev["event_type"])
		}
	}
	if events[0]["tool"] != "get_active_connections" {
		t.Errorf("event[0].tool = %q, want get_active_connections", events[0]["tool"])
	}
	if events[1]["tool"] != "terminate_idle_connections" {
		t.Errorf("event[1].tool = %q, want terminate_idle_connections", events[1]["tool"])
	}
}

func TestFetchStepsAsTraceEvents_ErrorOnServerFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	g := &Gateway{auditURL: srv.URL}
	_, err := g.fetchStepsAsTraceEvents("plr_fail")
	if err == nil {
		t.Error("expected error when steps endpoint returns 500, got nil")
	}
}

// ── series_id / version pinning (suggest-update path) ────────────────────

func TestHandlePlaybookFromTrace_PinsSeriesAndVersion(t *testing.T) {
	// When the request includes series_id and version, those values must be
	// written to the draft posted to auditd — not whatever the LLM wrote.
	var gotDraft audit.Playbook
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
			return
		}
		// POST — capture what was persisted.
		body, _ := readAll(r.Body)
		json.Unmarshal(body, &gotDraft) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(audit.Playbook{PlaybookID: "pb_pinned_001"}) //nolint:errcheck
	}))
	defer auditd.Close()

	llm := func(_ context.Context, _ string) (string, error) {
		// LLM returns a draft with a different series_id and version — caller's
		// values must win.
		return "name: Test\ndescription: desc\nseries_id: pbs_llm_invented\nversion: 9.9\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{
		"trace_id":  "tr_pin",
		"outcome":   "resolved",
		"series_id": "pbs_connection_remediate",
		"version":   "1.4",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if gotDraft.SeriesID != "pbs_connection_remediate" {
		t.Errorf("persisted series_id = %q, want pbs_connection_remediate", gotDraft.SeriesID)
	}
	if gotDraft.Version != "1.4" {
		t.Errorf("persisted version = %q, want 1.4", gotDraft.Version)
	}
	if gotDraft.OriginTrace != "tr_pin" {
		t.Errorf("persisted origin_trace = %q, want tr_pin", gotDraft.OriginTrace)
	}
}

func TestHandlePlaybookFromTrace_NoPinUsesGeneratedSeries(t *testing.T) {
	// When series_id is omitted, a pbs_generated_* series must be assigned
	// (existing behaviour for direct from-trace calls without suggest-update).
	var gotDraft audit.Playbook
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
			return
		}
		body, _ := readAll(r.Body)
		json.Unmarshal(body, &gotDraft) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(audit.Playbook{PlaybookID: "pb_gen_001"}) //nolint:errcheck
	}))
	defer auditd.Close()

	llm := func(_ context.Context, _ string) (string, error) {
		return "name: Test\ndescription: desc\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_nopin", "outcome": "resolved"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.HasPrefix(gotDraft.SeriesID, "pbs_generated_") {
		t.Errorf("persisted series_id = %q, want pbs_generated_* prefix", gotDraft.SeriesID)
	}
}

// ── operational field preservation (suggest-update path) ─────────────────

func TestHandlePlaybookFromTrace_PreservesOperationalFieldsFromActive(t *testing.T) {
	// When series_id is supplied, the from-trace handler must fetch the currently-
	// active version and carry over operational fields (execution_mode, approval_mode,
	// agent_name, transitions_to, escalates_to, entry_point, requires_evidence,
	// permitted_tools, target_hints) so the LLM cannot inadvertently change them.
	var gotDraft audit.Playbook

	activePlaybook := audit.Playbook{
		PlaybookID:       "pb_active_001",
		SeriesID:         "pbs_conn_triage",
		Version:          "1.3",
		IsActive:         true,
		ExecutionMode:    "agent_approve",
		ApprovalMode:     "review",
		AgentName:        "postgres_database_agent",
		TransitionsTo:    []string{"pbs_conn_remediation"},
		EscalatesTo:      []string{"pbs_sysadmin_infra"},
		EntryPoint:       true,
		RequiresEvidence: []string{"connection refused"},
		PermittedTools:   []string{"get_active_connections", "terminate_idle_connections"},
		TargetHints:      []string{"primary"},
		PlaybookType:     "triage",
	}

	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/events"):
			w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/fleet/playbooks"):
			// fetchPlaybookBySeriesID response
			json.NewEncoder(w).Encode(struct { //nolint:errcheck
				Playbooks []audit.Playbook `json:"playbooks"`
			}{Playbooks: []audit.Playbook{activePlaybook}})
		case r.Method == http.MethodPost:
			body, _ := readAll(r.Body)
			json.Unmarshal(body, &gotDraft) //nolint:errcheck
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(audit.Playbook{PlaybookID: "pb_new_draft"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer auditd.Close()

	llm := func(_ context.Context, _ string) (string, error) {
		// LLM tries to change operational fields — they should be overridden.
		return "name: Updated Triage\ndescription: desc\nexecution_mode: fleet\napproval_mode: auto\nagent_name: some_other_agent\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{
		"trace_id":  "tr_opfields",
		"series_id": "pbs_conn_triage",
		"version":   "1.4",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// Knowledge fields: come from LLM.
	if gotDraft.Name == "" {
		t.Error("expected non-empty name from LLM")
	}

	// Operational fields: must come from the active version, not the LLM.
	if gotDraft.ExecutionMode != "agent_approve" {
		t.Errorf("ExecutionMode = %q, want agent_approve (from active)", gotDraft.ExecutionMode)
	}
	if gotDraft.ApprovalMode != "review" {
		t.Errorf("ApprovalMode = %q, want review (from active)", gotDraft.ApprovalMode)
	}
	if gotDraft.AgentName != "postgres_database_agent" {
		t.Errorf("AgentName = %q, want postgres_database_agent (from active)", gotDraft.AgentName)
	}
	if len(gotDraft.TransitionsTo) != 1 || gotDraft.TransitionsTo[0] != "pbs_conn_remediation" {
		t.Errorf("TransitionsTo = %v, want [pbs_conn_remediation] (from active)", gotDraft.TransitionsTo)
	}
	if len(gotDraft.EscalatesTo) != 1 || gotDraft.EscalatesTo[0] != "pbs_sysadmin_infra" {
		t.Errorf("EscalatesTo = %v, want [pbs_sysadmin_infra] (from active)", gotDraft.EscalatesTo)
	}
	if !gotDraft.EntryPoint {
		t.Error("EntryPoint should be true (from active)")
	}
	if len(gotDraft.RequiresEvidence) != 1 || gotDraft.RequiresEvidence[0] != "connection refused" {
		t.Errorf("RequiresEvidence = %v, want [connection refused] (from active)", gotDraft.RequiresEvidence)
	}
	if len(gotDraft.PermittedTools) != 2 {
		t.Errorf("PermittedTools = %v, want 2 entries (from active)", gotDraft.PermittedTools)
	}
	if len(gotDraft.TargetHints) != 1 || gotDraft.TargetHints[0] != "primary" {
		t.Errorf("TargetHints = %v, want [primary] (from active)", gotDraft.TargetHints)
	}
	if gotDraft.PlaybookType != "triage" {
		t.Errorf("PlaybookType = %q, want triage (from active)", gotDraft.PlaybookType)
	}
}

func TestHandlePlaybookFromTrace_OperationalFieldFetchFailureIsSilent(t *testing.T) {
	// If fetchPlaybookBySeriesID fails (series not yet active, etc.), the draft is
	// still created — the handler logs a warning but does not abort.
	var drafted bool
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/events"):
			w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/fleet/playbooks"):
			// Return empty list — no active version yet.
			json.NewEncoder(w).Encode(struct { //nolint:errcheck
				Playbooks []audit.Playbook `json:"playbooks"`
			}{})
		case r.Method == http.MethodPost:
			drafted = true
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(audit.Playbook{PlaybookID: "pb_no_active"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer auditd.Close()

	llm := func(_ context.Context, _ string) (string, error) {
		return "name: Fallback Draft\ndescription: desc\nexecution_mode: fleet\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{
		"trace_id":  "tr_noactive",
		"series_id": "pbs_brand_new",
		"version":   "1.0",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !drafted {
		t.Error("draft should still be persisted even when active-version fetch returns empty list")
	}
}

// ── validatePlaybookProtocol ──────────────────────────────────────────────

func TestValidatePlaybookProtocol_Triage_Valid(t *testing.T) {
	pb := audit.Playbook{
		PlaybookType:  "triage",
		Name:          "Conn Triage",
		SeriesID:      "pbs_conn_triage",
		Description:   "Investigates connection overload.",
		ExecutionMode: "agent",
		Symptoms:      []string{"too many connections"},
		Escalation:    []string{"pg_hba rejection"},
		Guidance: "Step 1: check connections.\n\nFINDINGS: conn=<N>/<max>\nTRANSITION_TO: pbs_conn_remediate",
	}
	if warns := validatePlaybookProtocol(pb); len(warns) != 0 {
		t.Errorf("expected no warnings for valid triage, got: %v", warns)
	}
}

func TestValidatePlaybookProtocol_Triage_MissingFields(t *testing.T) {
	pb := audit.Playbook{PlaybookType: "triage"} // all required fields empty
	warns := validatePlaybookProtocol(pb)
	wantSubstrings := []string{"name:", "series_id:", "description:", "guidance:", "symptoms:", "escalation:"}
	for _, sub := range wantSubstrings {
		found := false
		for _, w := range warns {
			if strings.Contains(w, sub) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected warning containing %q, got: %v", sub, warns)
		}
	}
}

func TestValidatePlaybookProtocol_Triage_WrongExecutionMode(t *testing.T) {
	pb := audit.Playbook{
		PlaybookType:  "triage",
		Name:          "T", SeriesID: "pbs_t", Description: "d",
		ExecutionMode: "agent_approve",
		Symptoms:      []string{"s"}, Escalation: []string{"e"},
		Guidance: "FINDINGS: x\nTRANSITION_TO: pbs_r",
	}
	warns := validatePlaybookProtocol(pb)
	if !containsWarning(warns, "execution_mode") {
		t.Errorf("expected execution_mode warning, got: %v", warns)
	}
}

func TestValidatePlaybookProtocol_Triage_MissingFINDINGS(t *testing.T) {
	pb := audit.Playbook{
		PlaybookType:  "triage",
		Name:          "T", SeriesID: "pbs_t", Description: "d",
		ExecutionMode: "agent",
		Symptoms:      []string{"s"}, Escalation: []string{"e"},
		Guidance: "Step 1: do stuff.\nTRANSITION_TO: pbs_r",
	}
	warns := validatePlaybookProtocol(pb)
	if !containsWarning(warns, "FINDINGS:") {
		t.Errorf("expected FINDINGS warning, got: %v", warns)
	}
}

func TestValidatePlaybookProtocol_Triage_MissingSignalLine(t *testing.T) {
	pb := audit.Playbook{
		PlaybookType:  "triage",
		Name:          "T", SeriesID: "pbs_t", Description: "d",
		ExecutionMode: "agent",
		Symptoms:      []string{"s"}, Escalation: []string{"e"},
		Guidance: "Step 1: do stuff.\nFINDINGS: x=1",
	}
	warns := validatePlaybookProtocol(pb)
	if !containsWarning(warns, "signal line") {
		t.Errorf("expected signal line warning, got: %v", warns)
	}
}

func TestValidatePlaybookProtocol_Triage_EscalateToAccepted(t *testing.T) {
	pb := audit.Playbook{
		PlaybookType:  "triage",
		Name:          "T", SeriesID: "pbs_t", Description: "d",
		ExecutionMode: "agent",
		Symptoms:      []string{"s"}, Escalation: []string{"e"},
		Guidance: "FINDINGS: x\nESCALATE_TO: none",
	}
	if warns := validatePlaybookProtocol(pb); len(warns) != 0 {
		t.Errorf("ESCALATE_TO should satisfy signal line requirement, got: %v", warns)
	}
}

func TestValidatePlaybookProtocol_Remediation_Valid(t *testing.T) {
	pb := audit.Playbook{
		PlaybookType:  "remediation",
		Name:          "Conn Remediate",
		SeriesID:      "pbs_conn_remediate",
		Description:   "Terminates idle sessions.",
		ExecutionMode: "agent_approve",
		Symptoms:      []string{"connection overload"},
		Escalation:    []string{"active tx with writes"},
		Guidance:      "Step 1: confirm state.\nStep 2: terminate idle sessions.\nStep 3: verify.",
	}
	if warns := validatePlaybookProtocol(pb); len(warns) != 0 {
		t.Errorf("expected no warnings for valid remediation, got: %v", warns)
	}
}

func TestValidatePlaybookProtocol_Remediation_WrongExecutionMode(t *testing.T) {
	pb := audit.Playbook{
		PlaybookType:  "remediation",
		Name:          "R", SeriesID: "pbs_r", Description: "d",
		ExecutionMode: "agent",
		Symptoms:      []string{"s"}, Escalation: []string{"e"},
		Guidance: "Step 1: do stuff.",
	}
	warns := validatePlaybookProtocol(pb)
	if !containsWarning(warns, "execution_mode") {
		t.Errorf("expected execution_mode warning, got: %v", warns)
	}
}

func TestValidatePlaybookProtocol_Remediation_HasTransitionTo(t *testing.T) {
	pb := audit.Playbook{
		PlaybookType:  "remediation",
		Name:          "R", SeriesID: "pbs_r", Description: "d",
		ExecutionMode: "agent_approve",
		Symptoms:      []string{"s"}, Escalation: []string{"e"},
		Guidance: "Step 1: fix.\nTRANSITION_TO: pbs_other",
	}
	warns := validatePlaybookProtocol(pb)
	if !containsWarning(warns, "TRANSITION_TO") {
		t.Errorf("expected TRANSITION_TO warning for remediation, got: %v", warns)
	}
}

func TestValidatePlaybookProtocol_NoType_NoWarnings(t *testing.T) {
	// Untyped playbooks (legacy or runbook-style) must not generate warnings.
	pb := audit.Playbook{PlaybookType: "", Name: "Legacy", ExecutionMode: "agent"}
	if warns := validatePlaybookProtocol(pb); len(warns) != 0 {
		t.Errorf("untyped playbook should produce no warnings, got: %v", warns)
	}
}

func TestHandlePlaybookFromTrace_WarningsInResponse(t *testing.T) {
	// A triage draft that is missing FINDINGS and the signal line should have
	// warnings surfaced in the response JSON.
	var gotDraft audit.Playbook

	activePlaybook := audit.Playbook{
		PlaybookID:    "pb_warn_active",
		SeriesID:      "pbs_warn_triage",
		Version:       "1.0",
		IsActive:      true,
		ExecutionMode: "agent",
		PlaybookType:  "triage",
		// Guidance has no "Required output" section — so trailer preservation won't add FINDINGS.
		Guidance: "Investigate carefully.",
	}

	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/events"):
			w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/fleet/playbooks"):
			json.NewEncoder(w).Encode(struct { //nolint:errcheck
				Playbooks []audit.Playbook `json:"playbooks"`
			}{Playbooks: []audit.Playbook{activePlaybook}})
		case r.Method == http.MethodPost:
			body, _ := readAll(r.Body)
			json.Unmarshal(body, &gotDraft) //nolint:errcheck
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(audit.Playbook{PlaybookID: "pb_warn_draft"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer auditd.Close()

	// LLM produces guidance with no FINDINGS or signal line.
	llm := func(_ context.Context, _ string) (string, error) {
		return "name: Warn Test\ndescription: desc\nguidance: Just look at things.\nsymptoms:\n  - slow\nescalation:\n  - never\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{
		"trace_id":  "tr_warn",
		"series_id": "pbs_warn_triage",
		"version":   "1.1",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp PlaybookFromTraceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Warnings) == 0 {
		t.Error("expected warnings in response for triage draft missing FINDINGS and signal line")
	}
	if !containsWarning(resp.Warnings, "FINDINGS:") {
		t.Errorf("expected FINDINGS warning, got: %v", resp.Warnings)
	}
	if !containsWarning(resp.Warnings, "signal line") {
		t.Errorf("expected signal line warning, got: %v", resp.Warnings)
	}
	// PlaybookType must be preserved from active.
	if gotDraft.PlaybookType != "triage" {
		t.Errorf("PlaybookType = %q, want triage (from active)", gotDraft.PlaybookType)
	}
}

// containsWarning returns true if any element of warns contains substr.
func containsWarning(warns []string, substr string) bool {
	for _, w := range warns {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// ── "Required output" trailer preservation ────────────────────────────────

func TestHandlePlaybookFromTrace_PreservesRequiredOutputTrailer(t *testing.T) {
	// When the active version's guidance contains a "Required output" section, it must
	// be appended to the synthesized guidance — the LLM has no basis to reconstruct
	// the structured output protocol (HYPOTHESIS_N / FINDINGS / TRANSITION_TO).
	var gotDraft audit.Playbook

	const activeGuidance = "Investigate the connection pool.\n\nRequired output\n\nWrite these exact lines:\nFINDINGS: ...\nTRANSITION_TO: ..."
	activePlaybook := audit.Playbook{
		PlaybookID:    "pb_trailer_active",
		SeriesID:      "pbs_trailer_test",
		Version:       "1.3",
		IsActive:      true,
		ExecutionMode: "agent_approve",
		Guidance:      activeGuidance,
	}

	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/events"):
			w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/fleet/playbooks"):
			json.NewEncoder(w).Encode(struct { //nolint:errcheck
				Playbooks []audit.Playbook `json:"playbooks"`
			}{Playbooks: []audit.Playbook{activePlaybook}})
		case r.Method == http.MethodPost:
			body, _ := readAll(r.Body)
			json.Unmarshal(body, &gotDraft) //nolint:errcheck
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(audit.Playbook{PlaybookID: "pb_trailer_draft"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer auditd.Close()

	// LLM synthesizes guidance without the trailer.
	llm := func(_ context.Context, _ string) (string, error) {
		return "name: Trailer Test\ndescription: desc\nguidance: Check the pool.\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{
		"trace_id":  "tr_trailer",
		"series_id": "pbs_trailer_test",
		"version":   "1.4",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	const wantTrailer = "\nRequired output\n\nWrite these exact lines:\nFINDINGS: ...\nTRANSITION_TO: ..."
	if !strings.Contains(gotDraft.Guidance, "Required output") {
		t.Errorf("draft Guidance missing Required output trailer; got: %q", gotDraft.Guidance)
	}
	if !strings.HasSuffix(strings.TrimRight(gotDraft.Guidance, "\n"), strings.TrimRight(wantTrailer, "\n")) {
		t.Errorf("draft Guidance does not end with active trailer; got: %q", gotDraft.Guidance)
	}
}

func TestHandlePlaybookFromTrace_NoTrailerWhenActiveHasNone(t *testing.T) {
	// When the active version's guidance has no "Required output" section, the
	// synthesized guidance should not have one appended either.
	var gotDraft audit.Playbook

	activePlaybook := audit.Playbook{
		PlaybookID:    "pb_notrailer_active",
		SeriesID:      "pbs_notrailer_test",
		Version:       "1.3",
		IsActive:      true,
		ExecutionMode: "agent_approve",
		Guidance:      "Investigate the connection pool. No structured output section here.",
	}

	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/events"):
			w.Write([]byte(fakeTraceEvents)) //nolint:errcheck
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/fleet/playbooks"):
			json.NewEncoder(w).Encode(struct { //nolint:errcheck
				Playbooks []audit.Playbook `json:"playbooks"`
			}{Playbooks: []audit.Playbook{activePlaybook}})
		case r.Method == http.MethodPost:
			body, _ := readAll(r.Body)
			json.Unmarshal(body, &gotDraft) //nolint:errcheck
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(audit.Playbook{PlaybookID: "pb_notrailer_draft"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer auditd.Close()

	llm := func(_ context.Context, _ string) (string, error) {
		return "name: No Trailer Test\ndescription: desc\nguidance: Check the pool.\n", nil
	}
	g := newFromTraceGateway(llm, auditd.URL, "")
	w := doFromTraceRequest(t, g, map[string]string{
		"trace_id":  "tr_notrailer",
		"series_id": "pbs_notrailer_test",
		"version":   "1.4",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if strings.Contains(gotDraft.Guidance, "Required output") {
		t.Errorf("draft Guidance unexpectedly contains Required output trailer; got: %q", gotDraft.Guidance)
	}
}

// readAll is a helper for test to consume an io.Reader body.
func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf bytes.Buffer
	b := make([]byte, 4096)
	for {
		n, err := r.Read(b)
		buf.Write(b[:n])
		if err != nil {
			break
		}
	}
	return buf.Bytes(), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
