package agentutil

import (
	"context"
	"testing"
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

func TestDirectToolRegistry_Len(t *testing.T) {
	r := NewDirectToolRegistry()
	if r.Len() != 0 {
		t.Errorf("Len() = %d, want 0 for empty registry", r.Len())
	}
	r.Register("tool_a", func(_ context.Context, _ map[string]any) (string, error) { return "", nil })
	r.Register("tool_b", func(_ context.Context, _ map[string]any) (string, error) { return "", nil })
	if r.Len() != 2 {
		t.Errorf("Len() = %d, want 2 after two registrations", r.Len())
	}
}
