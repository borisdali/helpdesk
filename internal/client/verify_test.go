package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockAuditEvents returns a minimal audit event JSON array for testing.
func mockAuditEvents(events []map[string]any) []byte {
	b, _ := json.Marshal(events)
	return b
}

func TestVerifyTrace_NoAuditURL(t *testing.T) {
	c := New(Config{GatewayURL: "http://example.com"})
	v, err := c.VerifyTrace(context.Background(), "tr_abc", time.Now())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if v != nil {
		t.Fatalf("expected nil TraceVerification when AuditURL unset, got %+v", v)
	}
}

func TestVerifyTrace_ToolsConfirmed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		events := []map[string]any{
			{"action_class": "write", "tool": map[string]any{"name": "cancel_query"}},
			{"action_class": "read", "tool": map[string]any{"name": "get_session_info"}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(mockAuditEvents(events)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(Config{AuditURL: srv.URL})
	v, err := c.VerifyTrace(context.Background(), "tr_abc", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("expected TraceVerification, got nil")
	}
	if len(v.ToolsConfirmed) != 2 {
		t.Errorf("ToolsConfirmed = %d, want 2", len(v.ToolsConfirmed))
	}
	if len(v.WriteConfirmed) != 1 || v.WriteConfirmed[0] != "cancel_query" {
		t.Errorf("WriteConfirmed = %v, want [cancel_query]", v.WriteConfirmed)
	}
	if len(v.DestructiveConfirmed) != 0 {
		t.Errorf("DestructiveConfirmed = %v, want []", v.DestructiveConfirmed)
	}
	if !v.HasMutations() {
		t.Error("HasMutations() = false, want true")
	}
}

func TestVerifyTrace_DestructiveConfirmed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		events := []map[string]any{
			{"action_class": "destructive", "tool": map[string]any{"name": "terminate_connection"}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(mockAuditEvents(events)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(Config{AuditURL: srv.URL})
	v, err := c.VerifyTrace(context.Background(), "tr_abc", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(v.DestructiveConfirmed) != 1 || v.DestructiveConfirmed[0] != "terminate_connection" {
		t.Errorf("DestructiveConfirmed = %v, want [terminate_connection]", v.DestructiveConfirmed)
	}
	if !v.HasMutations() {
		t.Error("HasMutations() = false, want true")
	}
}

func TestVerifyTrace_Empty_ThenRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			// First call: empty — simulate async propagation delay.
			w.Write([]byte("[]")) //nolint:errcheck
			return
		}
		events := []map[string]any{
			{"action_class": "write", "tool": map[string]any{"name": "cancel_query"}},
		}
		w.Write(mockAuditEvents(events)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(Config{AuditURL: srv.URL})
	v, err := c.VerifyTrace(context.Background(), "tr_abc", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 auditd calls (retry), got %d", calls)
	}
	if v == nil || len(v.WriteConfirmed) != 1 {
		t.Errorf("WriteConfirmed = %v, want [cancel_query]", v.WriteConfirmed)
	}
}

func TestVerifyTrace_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(Config{AuditURL: srv.URL})
	v, err := c.VerifyTrace(context.Background(), "tr_abc", time.Now())
	if err == nil {
		t.Fatalf("expected error, got nil (v=%+v)", v)
	}
}

func TestTraceVerification_HasMutations(t *testing.T) {
	tests := []struct {
		name     string
		v        TraceVerification
		expected bool
	}{
		{"write only", TraceVerification{WriteConfirmed: []string{"cancel_query"}}, true},
		{"destructive only", TraceVerification{DestructiveConfirmed: []string{"terminate_connection"}}, true},
		{"both", TraceVerification{WriteConfirmed: []string{"x"}, DestructiveConfirmed: []string{"y"}}, true},
		{"neither", TraceVerification{}, false},
		{"read only", TraceVerification{ToolsConfirmed: []ConfirmedTool{{Name: "check_connection", ActionClass: "read"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.v.HasMutations(); got != tt.expected {
				t.Errorf("HasMutations() = %v, want %v", got, tt.expected)
			}
		})
	}
}
