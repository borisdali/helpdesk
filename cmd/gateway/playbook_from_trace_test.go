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
  {"step_index":1,"agent":"db-agent","tool":"kill_idle_connections","args":{"min_idle_minutes":5},"reason":"terminate idle","status":"success","result":"terminated 8"}
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
	if !strings.Contains(llmPrompt, "kill_idle_connections") {
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
	if events[1]["tool"] != "kill_idle_connections" {
		t.Errorf("event[1].tool = %q, want kill_idle_connections", events[1]["tool"])
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
