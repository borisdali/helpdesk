package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	g := newFromTraceGateway(nil, "", "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_abc"})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandlePlaybookFromTrace_LLMError(t *testing.T) {
	llm := func(_ context.Context, _ string) (string, error) {
		return "", context.DeadlineExceeded
	}
	g := newFromTraceGateway(llm, "", "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_abc"})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestHandlePlaybookFromTrace_SuccessNoAuditd(t *testing.T) {
	// LLM returns valid YAML; no auditd → draft returned, playbook_id empty.
	llm := func(_ context.Context, _ string) (string, error) {
		return `
name: DB Restart Triage
description: Restart the database when it is unresponsive.
problem_class: availability
guidance: Check pg_stat_activity first.
`, nil
	}
	g := newFromTraceGateway(llm, "", "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_123", "outcome": "resolved"})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp PlaybookFromTraceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Draft == "" {
		t.Error("expected non-empty draft")
	}
	if resp.Source != "tr_123" {
		t.Errorf("source = %q, want tr_123", resp.Source)
	}
	if resp.PlaybookID != "" {
		t.Errorf("playbook_id should be empty when auditd not configured, got %q", resp.PlaybookID)
	}
}

func TestHandlePlaybookFromTrace_DefaultOutcome(t *testing.T) {
	// outcome defaults to "resolved" when omitted.
	var gotPrompt string
	llm := func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "name: Test\ndescription: Test desc\n", nil
	}
	g := newFromTraceGateway(llm, "", "")
	doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_xyz"})
	if !strings.Contains(gotPrompt, "resolved") {
		t.Errorf("prompt should contain 'resolved'; got: %s", gotPrompt[:min(200, len(gotPrompt))])
	}
}

func TestHandlePlaybookFromTrace_StripMarkdownFences(t *testing.T) {
	// LLM wraps YAML in markdown fences — should be stripped.
	llm := func(_ context.Context, _ string) (string, error) {
		return "```yaml\nname: Test\ndescription: desc\n```", nil
	}
	g := newFromTraceGateway(llm, "", "")
	w := doFromTraceRequest(t, g, map[string]string{"trace_id": "tr_fence"})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp PlaybookFromTraceResponse
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if strings.Contains(resp.Draft, "```") {
		t.Errorf("draft should not contain markdown fences; got: %s", resp.Draft)
	}
}

func TestHandlePlaybookFromTrace_SuccessWithAuditd(t *testing.T) {
	// auditd is configured → persistPlaybookDraft is called → playbook_id returned.
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Respond to auditd event fetch (GET /v1/events?...).
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`)) //nolint:errcheck
			return
		}
		// Respond to playbook create (POST /v1/fleet/playbooks).
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
	// auditd returns error → draft is still returned, playbook_id is empty.
	auditd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`[]`)) //nolint:errcheck
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
		t.Error("draft should be non-empty even when auditd fails")
	}
	if resp.PlaybookID != "" {
		t.Errorf("playbook_id should be empty when persist fails, got %q", resp.PlaybookID)
	}
}

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
