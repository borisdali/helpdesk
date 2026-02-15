package audit

import (
	"context"
	"strings"
	"testing"
)

func TestNewTraceID(t *testing.T) {
	id := NewTraceID()
	if !strings.HasPrefix(id, "tr_") {
		t.Errorf("trace ID should start with 'tr_', got %q", id)
	}
	if len(id) != 15 { // "tr_" + 12 chars
		t.Errorf("trace ID should be 15 chars, got %d: %q", len(id), id)
	}

	// Verify uniqueness
	id2 := NewTraceID()
	if id == id2 {
		t.Error("trace IDs should be unique")
	}
}

func TestNewTraceContext(t *testing.T) {
	tc := NewTraceContext("gateway", "user123")

	if tc.TraceID == "" {
		t.Error("trace ID should be set")
	}
	if tc.Origin != "gateway" {
		t.Errorf("origin = %q, want %q", tc.Origin, "gateway")
	}
	if tc.Principal != "user123" {
		t.Errorf("principal = %q, want %q", tc.Principal, "user123")
	}
	if tc.ParentID != "" {
		t.Error("parent ID should be empty for new context")
	}
}

func TestTraceContext_Child(t *testing.T) {
	parent := NewTraceContext("orchestrator", "boris")
	child := parent.Child("evt_12345678")

	if child.TraceID != parent.TraceID {
		t.Error("child should inherit trace ID from parent")
	}
	if child.ParentID != "evt_12345678" {
		t.Errorf("child parent ID = %q, want %q", child.ParentID, "evt_12345678")
	}
	if child.Origin != parent.Origin {
		t.Error("child should inherit origin from parent")
	}
	if child.Principal != parent.Principal {
		t.Error("child should inherit principal from parent")
	}
}

func TestTraceContextFromContext(t *testing.T) {
	// Empty context
	tc := TraceContextFromContext(context.Background())
	if tc != nil {
		t.Error("expected nil for empty context")
	}

	// Context with trace
	original := NewTraceContext("gateway", "api-key-123")
	ctx := WithTraceContext(context.Background(), original)

	extracted := TraceContextFromContext(ctx)
	if extracted == nil {
		t.Fatal("expected trace context, got nil")
	}
	if extracted.TraceID != original.TraceID {
		t.Errorf("trace ID = %q, want %q", extracted.TraceID, original.TraceID)
	}
}

func TestTraceIDFromContext(t *testing.T) {
	// Empty context
	id := TraceIDFromContext(context.Background())
	if id != "" {
		t.Errorf("expected empty string for empty context, got %q", id)
	}

	// Context with trace
	tc := NewTraceContext("test", "user")
	ctx := WithTraceContext(context.Background(), tc)

	id = TraceIDFromContext(ctx)
	if id != tc.TraceID {
		t.Errorf("trace ID = %q, want %q", id, tc.TraceID)
	}
}
