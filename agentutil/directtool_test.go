package agentutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helpdesk/internal/audit"
	"helpdesk/internal/identity"
)

// --- DirectToolRegistry ---

func TestDirectToolRegistry_RegisterAndGet(t *testing.T) {
	r := NewDirectToolRegistry()
	fn := DirectToolFunc(func(ctx context.Context, args map[string]any) (string, error) {
		return "ok", nil
	})
	r.Register("my_tool", fn)

	got, ok := r.Get("my_tool")
	if !ok {
		t.Fatal("Get returned false for registered tool")
	}
	out, err := got(context.Background(), nil)
	if err != nil || out != "ok" {
		t.Errorf("fn() = (%q, %v), want (ok, nil)", out, err)
	}
}

func TestDirectToolRegistry_GetUnknown(t *testing.T) {
	r := NewDirectToolRegistry()
	_, ok := r.Get("no_such_tool")
	if ok {
		t.Fatal("Get returned true for unregistered tool")
	}
}

// --- registerDirectToolRoutes HTTP handler ---

// makeDirectToolMux builds a ServeMux with the given registry wired in.
func makeDirectToolMux(r *DirectToolRegistry, ts *audit.CurrentTraceStore) *http.ServeMux {
	mux := http.NewServeMux()
	registerDirectToolRoutes(mux, r, ts)
	return mux
}

func TestDirectToolRoutes_UnknownTool(t *testing.T) {
	mux := makeDirectToolMux(NewDirectToolRegistry(), nil)

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
	r := NewDirectToolRegistry()
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
	r := NewDirectToolRegistry()
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
	var resp DirectToolResponse
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
	r := NewDirectToolRegistry()
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
	var resp DirectToolResponse
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
	r := NewDirectToolRegistry()
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

// TestDirectToolRoutes_TraceContextPropagated verifies that trace_id, principal,
// and purpose from the request body are injected into the tool's context.Context,
// and that the CurrentTraceStore is updated with the trace ID.
func TestDirectToolRoutes_TraceContextPropagated(t *testing.T) {
	var capturedTraceID string
	var capturedUserID string
	var capturedPurpose string

	r := NewDirectToolRegistry()
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

	// CurrentTraceStore must also be updated so the PolicyEnforcer can read it.
	if got := traceStore.Get(); got != "tr_test_abc" {
		t.Errorf("traceStore.Get() = %q, want tr_test_abc", got)
	}
}

// TestDirectToolRoutes_TraceStoreNotUpdatedWhenTraceIDEmpty verifies that
// an empty trace_id in the request does not overwrite the store.
func TestDirectToolRoutes_TraceStoreNotUpdatedWhenTraceIDEmpty(t *testing.T) {
	r := NewDirectToolRegistry()
	r.Register("noop", func(ctx context.Context, args map[string]any) (string, error) {
		return "ok", nil
	})
	traceStore := &audit.CurrentTraceStore{}
	traceStore.Set("pre-existing-trace")
	mux := makeDirectToolMux(r, traceStore)

	// Request with no trace_id.
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

// TestDirectToolRoutes_PurposeNoteAndExplicitPropagated verifies the
// purpose_note and purpose_explicit fields are also propagated.
func TestDirectToolRoutes_PurposeNoteAndExplicitPropagated(t *testing.T) {
	var capturedPurposeNote string
	var capturedPurposeExplicit bool

	r := NewDirectToolRegistry()
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

// TestDirectToolRoutes_ArgsPassedThrough verifies that complex nested args are
// faithfully passed to the tool function without modification.
func TestDirectToolRoutes_ArgsPassedThrough(t *testing.T) {
	var capturedArgs map[string]any

	r := NewDirectToolRegistry()
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

// TestDirectToolRoutes_ServicePrincipalPropagated verifies that a service
// principal (no user_id) is correctly propagated through the trace context.
func TestDirectToolRoutes_ServicePrincipalPropagated(t *testing.T) {
	var capturedPrincipal identity.ResolvedPrincipal

	r := NewDirectToolRegistry()
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
