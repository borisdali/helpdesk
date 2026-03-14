package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// makeA2ABody builds a minimal A2A message/send JSON body with the given
// metadata map and an optional text part.
func makeA2ABody(t *testing.T, metadata map[string]any, text string) []byte {
	t.Helper()
	type part struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	type message struct {
		Metadata map[string]any `json:"metadata,omitempty"`
		Parts    []part         `json:"parts,omitempty"`
	}
	type params struct {
		ContextID string  `json:"contextId,omitempty"`
		Message   message `json:"message"`
	}
	type body struct {
		Params params `json:"params"`
	}
	var parts []part
	if text != "" {
		parts = append(parts, part{Kind: "text", Text: text})
	}
	b, err := json.Marshal(body{Params: params{Message: message{Metadata: metadata, Parts: parts}}})
	if err != nil {
		t.Fatalf("marshal A2A body: %v", err)
	}
	return b
}

// ── parseA2ARequest unit tests ────────────────────────────────────────────────

func TestParseA2ARequest_Empty(t *testing.T) {
	d := parseA2ARequest([]byte(`{}`))
	if d.traceID != "" || d.userID != "" || d.purpose != "" {
		t.Errorf("unexpected non-empty fields on empty body: %+v", d)
	}
}

func TestParseA2ARequest_TraceIDOnly(t *testing.T) {
	body := makeA2ABody(t, map[string]any{"trace_id": "tr_abc123"}, "")
	d := parseA2ARequest(body)
	if d.traceID != "tr_abc123" {
		t.Errorf("traceID = %q, want tr_abc123", d.traceID)
	}
}

func TestParseA2ARequest_IdentityFields(t *testing.T) {
	body := makeA2ABody(t, map[string]any{
		"trace_id":    "tr_ident",
		"user_id":     "alice@example.com",
		"roles":       []any{"dba", "sre"},
		"auth_method": "jwt",
	}, "What is the schema?")
	d := parseA2ARequest(body)

	if d.traceID != "tr_ident" {
		t.Errorf("traceID = %q, want tr_ident", d.traceID)
	}
	if d.userID != "alice@example.com" {
		t.Errorf("userID = %q, want alice@example.com", d.userID)
	}
	if len(d.roles) != 2 || d.roles[0] != "dba" || d.roles[1] != "sre" {
		t.Errorf("roles = %v, want [dba sre]", d.roles)
	}
	if d.authMethod != "jwt" {
		t.Errorf("authMethod = %q, want jwt", d.authMethod)
	}
	if d.userQuery != "What is the schema?" {
		t.Errorf("userQuery = %q, want 'What is the schema?'", d.userQuery)
	}
}

func TestParseA2ARequest_ServicePrincipal(t *testing.T) {
	body := makeA2ABody(t, map[string]any{
		"trace_id":    "tr_svc",
		"service":     "srebot",
		"auth_method": "api_key",
	}, "")
	d := parseA2ARequest(body)

	if d.service != "srebot" {
		t.Errorf("service = %q, want srebot", d.service)
	}
	if d.authMethod != "api_key" {
		t.Errorf("authMethod = %q, want api_key", d.authMethod)
	}
	if d.userID != "" {
		t.Errorf("userID = %q, want empty for service principal", d.userID)
	}
}

func TestParseA2ARequest_PurposeFields(t *testing.T) {
	body := makeA2ABody(t, map[string]any{
		"trace_id":     "tr_purp",
		"purpose":      "remediation",
		"purpose_note": "INC-4567",
	}, "")
	d := parseA2ARequest(body)

	if d.purpose != "remediation" {
		t.Errorf("purpose = %q, want remediation", d.purpose)
	}
	if d.purposeNote != "INC-4567" {
		t.Errorf("purposeNote = %q, want INC-4567", d.purposeNote)
	}
}

func TestParseA2ARequest_AllFields(t *testing.T) {
	body := makeA2ABody(t, map[string]any{
		"trace_id":     "tr_full",
		"user_id":      "carol@example.com",
		"roles":        []any{"oncall", "sre"},
		"auth_method":  "jwt",
		"purpose":      "emergency",
		"purpose_note": "prod DB down",
	}, "Restart the crashed pod")
	d := parseA2ARequest(body)

	if d.traceID != "tr_full" {
		t.Errorf("traceID = %q", d.traceID)
	}
	if d.userID != "carol@example.com" {
		t.Errorf("userID = %q", d.userID)
	}
	if len(d.roles) != 2 || d.roles[0] != "oncall" {
		t.Errorf("roles = %v", d.roles)
	}
	if d.purpose != "emergency" {
		t.Errorf("purpose = %q", d.purpose)
	}
	if d.purposeNote != "prod DB down" {
		t.Errorf("purposeNote = %q", d.purposeNote)
	}
	if d.userQuery != "Restart the crashed pod" {
		t.Errorf("userQuery = %q", d.userQuery)
	}
}

func TestParseA2ARequest_InvalidJSON(t *testing.T) {
	d := parseA2ARequest([]byte(`not json`))
	// Should not panic; all fields remain zero.
	if d.traceID != "" || d.userID != "" {
		t.Errorf("expected zero fields for invalid JSON, got %+v", d)
	}
}

// ── TraceMiddleware integration tests ─────────────────────────────────────────

// captureHandler records the TraceContext it receives from the request context.
type captureHandler struct {
	gotTC *TraceContext
}

func (h *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.gotTC = TraceContextFromContext(r.Context())
	w.WriteHeader(http.StatusOK)
}

func TestTraceMiddleware_SetsTraceIDInStore(t *testing.T) {
	store := &CurrentTraceStore{}
	capture := &captureHandler{}
	handler := TraceMiddleware(store, capture)

	body := makeA2ABody(t, map[string]any{"trace_id": "tr_mw1"}, "hello")
	req := httptest.NewRequest(http.MethodPost, "/invoke", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if capture.gotTC == nil {
		t.Fatal("TraceContext not set in request context")
	}
	if capture.gotTC.TraceID != "tr_mw1" {
		t.Errorf("TraceID = %q, want tr_mw1", capture.gotTC.TraceID)
	}
}

func TestTraceMiddleware_GeneratesTraceIDWhenMissing(t *testing.T) {
	store := &CurrentTraceStore{}
	capture := &captureHandler{}
	handler := TraceMiddleware(store, capture)

	// No metadata at all — should generate an ar_* trace ID.
	body := makeA2ABody(t, nil, "hello without trace")
	req := httptest.NewRequest(http.MethodPost, "/invoke", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if capture.gotTC == nil {
		t.Fatal("TraceContext not set")
	}
	if !strings.HasPrefix(capture.gotTC.TraceID, "ar_") {
		t.Errorf("generated TraceID = %q, want ar_* prefix", capture.gotTC.TraceID)
	}
}

func TestTraceMiddleware_PrincipalPropagated(t *testing.T) {
	store := &CurrentTraceStore{}
	capture := &captureHandler{}
	handler := TraceMiddleware(store, capture)

	body := makeA2ABody(t, map[string]any{
		"trace_id":    "tr_p1",
		"user_id":     "dave@example.com",
		"roles":       []any{"readonly"},
		"auth_method": "static",
	}, "show me the schema")
	req := httptest.NewRequest(http.MethodPost, "/invoke", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if capture.gotTC == nil {
		t.Fatal("TraceContext not set")
	}
	p := capture.gotTC.Principal
	if p.UserID != "dave@example.com" {
		t.Errorf("Principal.UserID = %q, want dave@example.com", p.UserID)
	}
	if len(p.Roles) != 1 || p.Roles[0] != "readonly" {
		t.Errorf("Principal.Roles = %v, want [readonly]", p.Roles)
	}
	if p.AuthMethod != "static" {
		t.Errorf("Principal.AuthMethod = %q, want static", p.AuthMethod)
	}
}

func TestTraceMiddleware_PurposePropagated(t *testing.T) {
	store := &CurrentTraceStore{}
	capture := &captureHandler{}
	handler := TraceMiddleware(store, capture)

	body := makeA2ABody(t, map[string]any{
		"trace_id":     "tr_purp2",
		"purpose":      "compliance",
		"purpose_note": "SOC2 audit",
	}, "list all users")
	req := httptest.NewRequest(http.MethodPost, "/invoke", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if capture.gotTC == nil {
		t.Fatal("TraceContext not set")
	}
	if capture.gotTC.Purpose != "compliance" {
		t.Errorf("Purpose = %q, want compliance", capture.gotTC.Purpose)
	}
	if capture.gotTC.PurposeNote != "SOC2 audit" {
		t.Errorf("PurposeNote = %q, want 'SOC2 audit'", capture.gotTC.PurposeNote)
	}
}

func TestTraceMiddleware_StoreCleared_AfterRequest(t *testing.T) {
	store := &CurrentTraceStore{}
	handler := TraceMiddleware(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inside the handler the store should be set.
		if store.Get() == "" {
			t.Error("store should have trace_id during request handling")
		}
		w.WriteHeader(http.StatusOK)
	}))

	body := makeA2ABody(t, map[string]any{"trace_id": "tr_clear"}, "")
	req := httptest.NewRequest(http.MethodPost, "/invoke", bytes.NewReader(body))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	// After ServeHTTP returns, store should be cleared.
	if store.Get() != "" {
		t.Errorf("store not cleared after request; got %q", store.Get())
	}
}

func TestTraceMiddleware_ContextHelpers(t *testing.T) {
	// Verify that PrincipalFromContext and PurposeFromContext return the
	// propagated values from within the handler.
	store := &CurrentTraceStore{}
	var gotUserID, gotPurpose, gotNote string

	handler := TraceMiddleware(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := PrincipalFromContext(r.Context())
		gotUserID = p.UserID
		gotPurpose, gotNote = PurposeFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	body := makeA2ABody(t, map[string]any{
		"trace_id":     "tr_ctx",
		"user_id":      "eve@example.com",
		"purpose":      "maintenance",
		"purpose_note": "patching",
	}, "")
	req := httptest.NewRequest(http.MethodPost, "/invoke", bytes.NewReader(body))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotUserID != "eve@example.com" {
		t.Errorf("PrincipalFromContext UserID = %q, want eve@example.com", gotUserID)
	}
	if gotPurpose != "maintenance" {
		t.Errorf("PurposeFromContext purpose = %q, want maintenance", gotPurpose)
	}
	if gotNote != "patching" {
		t.Errorf("PurposeFromContext note = %q, want patching", gotNote)
	}
}

func TestTraceMiddlewareWithAudit_EmitsAnchorEvent(t *testing.T) {
	store := &CurrentTraceStore{}
	recorded := make([]*Event, 0)
	auditor := auditorFunc(func(_ context.Context, e *Event) error {
		recorded = append(recorded, e)
		return nil
	})

	handler := TraceMiddlewareWithAudit(store, auditor, "test-agent", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	body := makeA2ABody(t, map[string]any{"trace_id": "tr_anch"}, "Fix the slow query")
	req := httptest.NewRequest(http.MethodPost, "/invoke", bytes.NewReader(body))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if len(recorded) == 0 {
		t.Fatal("no anchor event emitted")
	}
	e := recorded[0]
	if e.EventType != EventTypeGatewayRequest {
		t.Errorf("EventType = %q, want %q", e.EventType, EventTypeGatewayRequest)
	}
	if e.TraceID != "tr_anch" {
		t.Errorf("TraceID = %q, want tr_anch", e.TraceID)
	}
	if e.Input.UserQuery != "Fix the slow query" {
		t.Errorf("UserQuery = %q, want 'Fix the slow query'", e.Input.UserQuery)
	}
	if e.Tool == nil || e.Tool.Agent != "test-agent" {
		t.Errorf("Tool.Agent = %q, want test-agent", e.Tool.Agent)
	}
}

// auditorFunc is a test helper that adapts a function to the Auditor interface.
type auditorFunc func(context.Context, *Event) error

func (f auditorFunc) Record(ctx context.Context, e *Event) error                          { return f(ctx, e) }
func (f auditorFunc) RecordOutcome(_ context.Context, _ string, _ *Outcome) error         { return nil }
func (f auditorFunc) Query(_ context.Context, _ QueryOptions) ([]Event, error)            { return nil, nil }
func (f auditorFunc) Close() error                                                        { return nil }
