package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"
	"google.golang.org/adk/server/adka2a"
	"google.golang.org/adk/session"
	"google.golang.org/genai"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/authz"
	"helpdesk/internal/identity"
)

// ---------------------------------------------------------------------------
// Tool call tracking (newToolCallCallbacks)
// ---------------------------------------------------------------------------

// mockExecutorContext wraps a context.Context to satisfy adka2a.ExecutorContext.
// Only the context embedding is used by our callback; all other methods return zero values.
type mockExecutorContext struct {
	context.Context
}

func (m mockExecutorContext) SessionID() string                     { return "" }
func (m mockExecutorContext) UserID() string                        { return "" }
func (m mockExecutorContext) AgentName() string                     { return "" }
func (m mockExecutorContext) ReadonlyState() session.ReadonlyState  { return nil }
func (m mockExecutorContext) Events() session.Events                { return nil }
func (m mockExecutorContext) UserContent() *genai.Content           { return nil }
func (m mockExecutorContext) RequestContext() *a2asrv.RequestContext { return nil }

var _ adka2a.ExecutorContext = mockExecutorContext{}

func TestToolCallStore_AddSnapshot(t *testing.T) {
	s := &toolCallStore{}
	if got := s.snapshot(); got != nil {
		t.Errorf("snapshot of empty store = %v, want nil", got)
	}

	s.add("check_connection")
	s.add("get_table_stats")
	s.add("get_table_stats") // duplicate — order preserved, deduplication not expected

	got := s.snapshot()
	want := []string{"check_connection", "get_table_stats", "get_table_stats"}
	if len(got) != len(want) {
		t.Fatalf("snapshot = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("snapshot[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestToolCallStore_SnapshotIsACopy(t *testing.T) {
	s := &toolCallStore{}
	s.add("tool-a")
	snap := s.snapshot()
	snap[0] = "mutated"
	if s.names[0] != "tool-a" {
		t.Error("snapshot mutation affected the underlying store")
	}
}

func TestToolCallStoreFromContext_Present(t *testing.T) {
	store := &toolCallStore{}
	ctx := context.WithValue(context.Background(), toolCallStoreKey{}, store)
	got := toolCallStoreFromContext(ctx)
	if got != store {
		t.Error("toolCallStoreFromContext did not return the injected store")
	}
}

func TestToolCallStoreFromContext_Absent(t *testing.T) {
	got := toolCallStoreFromContext(context.Background())
	if got != nil {
		t.Errorf("toolCallStoreFromContext with no store = %v, want nil", got)
	}
}

func TestNewToolCallCallbacks_BeforeInjectsStore(t *testing.T) {
	before, _ := newToolCallCallbacks()
	ctx, err := before(context.Background(), nil)
	if err != nil {
		t.Fatalf("before callback error: %v", err)
	}
	store := toolCallStoreFromContext(ctx)
	if store == nil {
		t.Fatal("before callback did not inject a toolCallStore into context")
	}
}

// makeFunctionCallEvent returns a session.Event whose Content contains one
// FunctionCall with the given name. Such events are NOT final responses.
func makeFunctionCallEvent(name string) *session.Event {
	evt := &session.Event{}
	evt.Content = &genai.Content{
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: name}},
		},
	}
	return evt
}

// makeFinalResponseEvent returns a session.Event with plain text and no
// FunctionCalls, so IsFinalResponse() returns true.
func makeFinalResponseEvent(text string) *session.Event {
	evt := &session.Event{}
	evt.Content = &genai.Content{
		Parts: []*genai.Part{{Text: text}},
	}
	return evt
}

func TestNewToolCallCallbacks_AfterCollectsNames(t *testing.T) {
	before, after := newToolCallCallbacks()
	ctx, _ := before(context.Background(), nil)
	execCtx := mockExecutorContext{ctx}

	if err := after(execCtx, makeFunctionCallEvent("check_connection"), nil); err != nil {
		t.Fatalf("after callback error: %v", err)
	}
	if err := after(execCtx, makeFunctionCallEvent("get_table_stats"), nil); err != nil {
		t.Fatalf("after callback error: %v", err)
	}

	store := toolCallStoreFromContext(ctx)
	got := store.snapshot()
	if len(got) != 2 || got[0] != "check_connection" || got[1] != "get_table_stats" {
		t.Errorf("store after two tool events = %v, want [check_connection get_table_stats]", got)
	}
}

func TestNewToolCallCallbacks_AfterInjectsSummaryOnFinalResponse(t *testing.T) {
	before, after := newToolCallCallbacks()
	ctx, _ := before(context.Background(), nil)
	execCtx := mockExecutorContext{ctx}

	_ = after(execCtx, makeFunctionCallEvent("get_database_stats"), nil)

	artifact := &a2a.Artifact{ID: "art-1"}
	processed := &a2a.TaskArtifactUpdateEvent{Artifact: artifact}
	if err := after(execCtx, makeFinalResponseEvent("summary text"), processed); err != nil {
		t.Fatalf("after callback error on final response: %v", err)
	}

	if len(artifact.Parts) == 0 {
		t.Fatal("no parts injected into artifact")
	}
	dp, ok := artifact.Parts[len(artifact.Parts)-1].(a2a.DataPart)
	if !ok {
		t.Fatalf("last artifact part is %T, want a2a.DataPart", artifact.Parts[len(artifact.Parts)-1])
	}
	if dp.Metadata[HelpdeskToolCallSummaryMetaKey] != HelpdeskToolCallSummaryMetaValue {
		t.Errorf("DataPart metadata[%q] = %q, want %q",
			HelpdeskToolCallSummaryMetaKey, dp.Metadata[HelpdeskToolCallSummaryMetaKey], HelpdeskToolCallSummaryMetaValue)
	}
	names, ok := dp.Data["tool_calls"].([]string)
	if !ok {
		t.Fatalf("DataPart data[tool_calls] is %T, want []string", dp.Data["tool_calls"])
	}
	if len(names) != 1 || names[0] != "get_database_stats" {
		t.Errorf("tool_calls = %v, want [get_database_stats]", names)
	}
}

func TestNewToolCallCallbacks_AfterNoSummaryWhenNoToolCalls(t *testing.T) {
	before, after := newToolCallCallbacks()
	ctx, _ := before(context.Background(), nil)
	execCtx := mockExecutorContext{ctx}

	artifact := &a2a.Artifact{ID: "art-empty"}
	processed := &a2a.TaskArtifactUpdateEvent{Artifact: artifact}
	if err := after(execCtx, makeFinalResponseEvent("no tools used"), processed); err != nil {
		t.Fatalf("after callback error: %v", err)
	}

	for _, p := range artifact.Parts {
		if dp, ok := p.(a2a.DataPart); ok {
			if dp.Metadata[HelpdeskToolCallSummaryMetaKey] == HelpdeskToolCallSummaryMetaValue {
				t.Error("unexpected tool_call_summary DataPart when no tool calls were made")
			}
		}
	}
}

func TestNewToolCallCallbacks_AfterNoSummaryWhenProcessedNil(t *testing.T) {
	before, after := newToolCallCallbacks()
	ctx, _ := before(context.Background(), nil)
	execCtx := mockExecutorContext{ctx}

	_ = after(execCtx, makeFunctionCallEvent("check_connection"), nil)
	if err := after(execCtx, makeFinalResponseEvent("done"), nil); err != nil {
		t.Fatalf("after callback error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// registerDirectToolRoutes HTTP handler
// ---------------------------------------------------------------------------

// makeDirectToolMux builds a ServeMux with the given registry wired in (no auth).
func makeDirectToolMux(r *agentutil.DirectToolRegistry, ts *audit.CurrentTraceStore) *http.ServeMux {
	mux := http.NewServeMux()
	idProvider := &identity.NoAuthProvider{}
	authzr := authz.NewAuthorizer(authz.DefaultAgentPermissions, false)
	registerDirectToolRoutes(mux, r, ts, idProvider, authzr)
	return mux
}

// makeAuthToolMux builds a ServeMux with auth enforced using the given users.yaml content.
func makeAuthToolMux(t *testing.T, r *agentutil.DirectToolRegistry, usersYAML string) *http.ServeMux {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.yaml")
	if err := os.WriteFile(path, []byte(usersYAML), 0600); err != nil {
		t.Fatalf("write users.yaml: %v", err)
	}
	idProvider, err := identity.NewStaticProvider(path)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}
	authzr := authz.NewAuthorizer(authz.DefaultAgentPermissions, true)
	mux := http.NewServeMux()
	registerDirectToolRoutes(mux, r, nil, idProvider, authzr)
	return mux
}

func TestDirectToolRoutes_UnknownTool(t *testing.T) {
	mux := makeDirectToolMux(agentutil.NewDirectToolRegistry(), nil)

	req := httptest.NewRequest(http.MethodPost, "/tool/no_such_tool", strings.NewReader(`{"args":{}}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown tool") {
		t.Errorf("body = %q, want mention of unknown tool", rec.Body.String())
	}
}

func TestDirectToolRoutes_BadJSON(t *testing.T) {
	r := agentutil.NewDirectToolRegistry()
	r.Register("mytool", func(ctx context.Context, args map[string]any) (string, error) {
		return "ok", nil
	})
	mux := makeDirectToolMux(r, nil)

	req := httptest.NewRequest(http.MethodPost, "/tool/mytool", strings.NewReader(`not valid json`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid JSON") {
		t.Errorf("body = %q, want invalid JSON error", rec.Body.String())
	}
}

func TestDirectToolRoutes_ToolError_Returns422(t *testing.T) {
	r := agentutil.NewDirectToolRegistry()
	r.Register("fail_tool", func(ctx context.Context, args map[string]any) (string, error) {
		return "", errors.New("psql: connection refused")
	})
	mux := makeDirectToolMux(r, nil)

	req := httptest.NewRequest(http.MethodPost, "/tool/fail_tool", strings.NewReader(`{"args":{}}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
	var resp agentutil.DirectToolResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "psql: connection refused" {
		t.Errorf("Error = %q, want 'psql: connection refused'", resp.Error)
	}
	if resp.Output != "" {
		t.Errorf("Output = %q, want empty on error", resp.Output)
	}
}

func TestDirectToolRoutes_Success(t *testing.T) {
	r := agentutil.NewDirectToolRegistry()
	r.Register("echo_tool", func(ctx context.Context, args map[string]any) (string, error) {
		return fmt.Sprintf("got key=%v", args["key"]), nil
	})
	mux := makeDirectToolMux(r, nil)

	req := httptest.NewRequest(http.MethodPost, "/tool/echo_tool", strings.NewReader(`{"args":{"key":"hello"}}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp agentutil.DirectToolResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Output != "got key=hello" {
		t.Errorf("Output = %q, want 'got key=hello'", resp.Output)
	}
	if resp.Error != "" {
		t.Errorf("Error = %q, want empty on success", resp.Error)
	}
}

func TestDirectToolRoutes_ContentTypeIsJSON(t *testing.T) {
	r := agentutil.NewDirectToolRegistry()
	r.Register("ok_tool", func(ctx context.Context, args map[string]any) (string, error) {
		return "done", nil
	})
	mux := makeDirectToolMux(r, nil)

	req := httptest.NewRequest(http.MethodPost, "/tool/ok_tool", strings.NewReader(`{"args":{}}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestDirectToolRoutes_TraceContextPropagated(t *testing.T) {
	var capturedTraceID string
	var capturedUserID string
	var capturedPurpose string

	r := agentutil.NewDirectToolRegistry()
	r.Register("inspect_tool", func(ctx context.Context, args map[string]any) (string, error) {
		tc := audit.TraceContextFromContext(ctx)
		if tc != nil {
			capturedTraceID = tc.TraceID
			capturedUserID = tc.Principal.UserID
			capturedPurpose = tc.Purpose
		}
		return "ok", nil
	})

	traceStore := &audit.CurrentTraceStore{}
	mux := makeDirectToolMux(r, traceStore)

	body := `{
		"trace_id": "tr_test_abc",
		"principal": {"user_id": "alice@example.com", "auth_method": "api_key"},
		"purpose": "remediation",
		"args": {}
	}`
	req := httptest.NewRequest(http.MethodPost, "/tool/inspect_tool", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if capturedTraceID != "tr_test_abc" {
		t.Errorf("TraceID = %q, want tr_test_abc", capturedTraceID)
	}
	if capturedUserID != "alice@example.com" {
		t.Errorf("Principal.UserID = %q, want alice@example.com", capturedUserID)
	}
	if capturedPurpose != "remediation" {
		t.Errorf("Purpose = %q, want remediation", capturedPurpose)
	}
	if got := traceStore.Get(); got != "tr_test_abc" {
		t.Errorf("traceStore.Get() = %q, want tr_test_abc", got)
	}
}

func TestDirectToolRoutes_TraceStoreNotUpdatedWhenTraceIDEmpty(t *testing.T) {
	r := agentutil.NewDirectToolRegistry()
	r.Register("noop", func(ctx context.Context, args map[string]any) (string, error) {
		return "ok", nil
	})
	traceStore := &audit.CurrentTraceStore{}
	traceStore.Set("pre-existing-trace")
	mux := makeDirectToolMux(r, traceStore)

	req := httptest.NewRequest(http.MethodPost, "/tool/noop", strings.NewReader(`{"args":{}}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := traceStore.Get(); got != "pre-existing-trace" {
		t.Errorf("traceStore.Get() = %q, want pre-existing-trace (should not be overwritten)", got)
	}
}

func TestDirectToolRoutes_PurposeNoteAndExplicitPropagated(t *testing.T) {
	var capturedPurposeNote string
	var capturedPurposeExplicit bool

	r := agentutil.NewDirectToolRegistry()
	r.Register("purpose_tool", func(ctx context.Context, args map[string]any) (string, error) {
		tc := audit.TraceContextFromContext(ctx)
		if tc != nil {
			capturedPurposeNote = tc.PurposeNote
			capturedPurposeExplicit = tc.PurposeExplicit
		}
		return "ok", nil
	})
	mux := makeDirectToolMux(r, nil)

	body := `{
		"trace_id": "tr_note_test",
		"principal": {"user_id": "bob"},
		"purpose": "remediation",
		"purpose_note": "fixing INC-4567",
		"purpose_explicit": true,
		"args": {}
	}`
	req := httptest.NewRequest(http.MethodPost, "/tool/purpose_tool", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if capturedPurposeNote != "fixing INC-4567" {
		t.Errorf("PurposeNote = %q, want 'fixing INC-4567'", capturedPurposeNote)
	}
	if !capturedPurposeExplicit {
		t.Error("PurposeExplicit = false, want true")
	}
}

func TestDirectToolRoutes_FlatArgs(t *testing.T) {
	var capturedArgs map[string]any

	r := agentutil.NewDirectToolRegistry()
	r.Register("flat_tool", func(ctx context.Context, args map[string]any) (string, error) {
		capturedArgs = args
		return "ok", nil
	})
	mux := makeDirectToolMux(r, nil)

	req := httptest.NewRequest(http.MethodPost, "/tool/flat_tool", strings.NewReader(`{"target":"prod-db"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if capturedArgs["target"] != "prod-db" {
		t.Errorf("target = %v, want prod-db", capturedArgs["target"])
	}
}

func TestDirectToolRoutes_ArgsPassedThrough(t *testing.T) {
	var capturedArgs map[string]any

	r := agentutil.NewDirectToolRegistry()
	r.Register("capture_args", func(ctx context.Context, args map[string]any) (string, error) {
		capturedArgs = args
		return "ok", nil
	})
	mux := makeDirectToolMux(r, nil)

	body := `{"args":{"connection_string":"postgres://prod:5432/mydb","idle_minutes":15,"dry_run":true}}`
	req := httptest.NewRequest(http.MethodPost, "/tool/capture_args", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if capturedArgs["connection_string"] != "postgres://prod:5432/mydb" {
		t.Errorf("connection_string = %v, want postgres://prod:5432/mydb", capturedArgs["connection_string"])
	}
	if capturedArgs["idle_minutes"] != float64(15) {
		t.Errorf("idle_minutes = %v, want 15", capturedArgs["idle_minutes"])
	}
	if capturedArgs["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", capturedArgs["dry_run"])
	}
}

func TestDirectToolRoutes_ServicePrincipalPropagated(t *testing.T) {
	var capturedPrincipal identity.ResolvedPrincipal

	r := agentutil.NewDirectToolRegistry()
	r.Register("svc_tool", func(ctx context.Context, args map[string]any) (string, error) {
		tc := audit.TraceContextFromContext(ctx)
		if tc != nil {
			capturedPrincipal = tc.Principal
		}
		return "ok", nil
	})
	mux := makeDirectToolMux(r, nil)

	body := `{
		"trace_id": "tr_svc",
		"principal": {"service": "fleet-runner", "auth_method": "api_key"},
		"args": {}
	}`
	req := httptest.NewRequest(http.MethodPost, "/tool/svc_tool", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if capturedPrincipal.Service != "fleet-runner" {
		t.Errorf("Principal.Service = %q, want fleet-runner", capturedPrincipal.Service)
	}
	if capturedPrincipal.UserID != "" {
		t.Errorf("Principal.UserID = %q, want empty for service principal", capturedPrincipal.UserID)
	}
}

// ── Auth enforcement tests ────────────────────────────────────────────────────

const testUsersYAML = `
service_accounts:
  - id: gateway
    roles: [service]
    api_key_hash: "%s"
`

func TestDirectToolRoutes_Auth_ServiceAccountAllowed(t *testing.T) {
	r := agentutil.NewDirectToolRegistry()
	r.Register("ok_tool", func(_ context.Context, _ map[string]any) (string, error) {
		return "ok", nil
	})

	apiKey, keyHash := mustGenerateAPIKey(t)
	yaml := fmt.Sprintf(testUsersYAML, keyHash)
	mux := makeAuthToolMux(t, r, yaml)

	req := httptest.NewRequest(http.MethodPost, "/tool/ok_tool", strings.NewReader(`{"args":{}}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("valid service account: status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

func TestDirectToolRoutes_Auth_AnonymousRejected(t *testing.T) {
	r := agentutil.NewDirectToolRegistry()
	r.Register("ok_tool", func(_ context.Context, _ map[string]any) (string, error) {
		return "ok", nil
	})
	mux := makeAuthToolMux(t, r, "service_accounts: []\nusers: []\n")

	req := httptest.NewRequest(http.MethodPost, "/tool/ok_tool", strings.NewReader(`{"args":{}}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous caller: status = %d, want 401", rec.Code)
	}
}

func TestDirectToolRoutes_Auth_HumanRejected(t *testing.T) {
	r := agentutil.NewDirectToolRegistry()
	r.Register("ok_tool", func(_ context.Context, _ map[string]any) (string, error) {
		return "ok", nil
	})
	yaml := "users:\n  - id: alice@example.com\n    roles: [dba]\n"
	mux := makeAuthToolMux(t, r, yaml)

	req := httptest.NewRequest(http.MethodPost, "/tool/ok_tool", strings.NewReader(`{"args":{}}`))
	req.Header.Set("X-User", "alice@example.com")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("human caller on ServiceOnly endpoint: status = %d, want 403", rec.Code)
	}
}

func mustGenerateAPIKey(t *testing.T) (rawKey, hash string) {
	t.Helper()
	rawKey = "test-gateway-key"
	var err error
	hash, err = identity.HashAPIKey(rawKey)
	if err != nil {
		t.Fatalf("HashAPIKey: %v", err)
	}
	return rawKey, hash
}
