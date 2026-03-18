package audit

import (
	"context"
	"strings"
	"testing"

	"helpdesk/internal/identity"
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

func TestNewTraceIDWithPrefix(t *testing.T) {
	for _, prefix := range []string{"tr_", "dt_", "chk_", "nl_"} {
		id := NewTraceIDWithPrefix(prefix)
		if !strings.HasPrefix(id, prefix) {
			t.Errorf("prefix %q: got %q", prefix, id)
		}
		if len(id) != len(prefix)+12 {
			t.Errorf("prefix %q: want length %d, got %d: %q", prefix, len(prefix)+12, len(id), id)
		}
		// Uniqueness
		if id2 := NewTraceIDWithPrefix(prefix); id == id2 {
			t.Errorf("prefix %q: IDs should be unique", prefix)
		}
	}
}

func TestNewTraceContext(t *testing.T) {
	p := identity.ResolvedPrincipal{UserID: "user123", AuthMethod: "header"}
	tc := NewTraceContext("gateway", p)

	if tc.TraceID == "" {
		t.Error("trace ID should be set")
	}
	if tc.Origin != "gateway" {
		t.Errorf("origin = %q, want %q", tc.Origin, "gateway")
	}
	if tc.Principal.UserID != "user123" {
		t.Errorf("principal.UserID = %q, want %q", tc.Principal.UserID, "user123")
	}
	if tc.ParentID != "" {
		t.Error("parent ID should be empty for new context")
	}
}

func TestTraceContext_Child(t *testing.T) {
	p := identity.ResolvedPrincipal{UserID: "boris", Roles: []string{"dba"}, AuthMethod: "static"}
	parent := NewTraceContext("orchestrator", p)
	parent.Purpose = "remediation"
	parent.PurposeNote = "INC-1234"
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
	if child.Principal.UserID != parent.Principal.UserID {
		t.Error("child should inherit principal from parent")
	}
	if child.Purpose != "remediation" {
		t.Errorf("child purpose = %q, want %q", child.Purpose, "remediation")
	}
	if child.PurposeNote != "INC-1234" {
		t.Errorf("child purpose_note = %q, want %q", child.PurposeNote, "INC-1234")
	}
}

func TestTraceContextFromContext(t *testing.T) {
	// Empty context
	tc := TraceContextFromContext(context.Background())
	if tc != nil {
		t.Error("expected nil for empty context")
	}

	// Context with trace
	p := identity.ResolvedPrincipal{UserID: "api-key-123", AuthMethod: "api_key"}
	original := NewTraceContext("gateway", p)
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
	tc := NewTraceContext("test", identity.ResolvedPrincipal{UserID: "user", AuthMethod: "header"})
	ctx := WithTraceContext(context.Background(), tc)

	id = TraceIDFromContext(ctx)
	if id != tc.TraceID {
		t.Errorf("trace ID = %q, want %q", id, tc.TraceID)
	}
}

func TestPrincipalFromContext(t *testing.T) {
	// Empty context returns zero value
	p := PrincipalFromContext(context.Background())
	if p.UserID != "" || p.AuthMethod != "" {
		t.Errorf("expected zero principal for empty context, got %+v", p)
	}

	// Context with principal
	want := identity.ResolvedPrincipal{UserID: "alice@example.com", Roles: []string{"dba"}, AuthMethod: "static"}
	tc := NewTraceContext("gateway", want)
	ctx := WithTraceContext(context.Background(), tc)

	got := PrincipalFromContext(ctx)
	if got.UserID != want.UserID {
		t.Errorf("UserID = %q, want %q", got.UserID, want.UserID)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "dba" {
		t.Errorf("Roles = %v, want [dba]", got.Roles)
	}
}

func TestPurposeFromContext(t *testing.T) {
	// Empty context
	purpose, note := PurposeFromContext(context.Background())
	if purpose != "" || note != "" {
		t.Errorf("expected empty for empty context, got purpose=%q note=%q", purpose, note)
	}

	// Context with purpose
	tc := NewTraceContext("gateway", identity.ResolvedPrincipal{})
	tc.Purpose = "remediation"
	tc.PurposeNote = "INC-9999"
	ctx := WithTraceContext(context.Background(), tc)

	purpose, note = PurposeFromContext(ctx)
	if purpose != "remediation" {
		t.Errorf("purpose = %q, want %q", purpose, "remediation")
	}
	if note != "INC-9999" {
		t.Errorf("note = %q, want %q", note, "INC-9999")
	}
}
