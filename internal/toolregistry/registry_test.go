package toolregistry

import (
	"testing"
)

func TestNew_GetAndList(t *testing.T) {
	entries := []ToolEntry{
		{Name: "check_connection", Agent: "database", Description: "Check DB connectivity", ActionClass: "read"},
		{Name: "cancel_query", Agent: "database", Description: "Cancel a query", ActionClass: "write"},
		{Name: "get_pods", Agent: "k8s", Description: "List pods", ActionClass: "read"},
	}
	r := New(entries)

	if got := r.List(); len(got) != 3 {
		t.Fatalf("List() len = %d, want 3", len(got))
	}

	e, ok := r.Get("check_connection")
	if !ok {
		t.Fatal("Get(check_connection) not found")
	}
	if e.Agent != "database" {
		t.Errorf("Agent = %q, want %q", e.Agent, "database")
	}
	if e.ActionClass != "read" {
		t.Errorf("ActionClass = %q, want %q", e.ActionClass, "read")
	}

	_, ok = r.Get("nonexistent_tool")
	if ok {
		t.Error("Get(nonexistent_tool) returned ok=true, want false")
	}
}

func TestListByAgent(t *testing.T) {
	entries := []ToolEntry{
		{Name: "check_connection", Agent: "database", ActionClass: "read"},
		{Name: "cancel_query", Agent: "database", ActionClass: "write"},
		{Name: "get_pods", Agent: "k8s", ActionClass: "read"},
	}
	r := New(entries)

	dbTools := r.ListByAgent("database")
	if len(dbTools) != 2 {
		t.Fatalf("ListByAgent(database) len = %d, want 2", len(dbTools))
	}

	k8sTools := r.ListByAgent("k8s")
	if len(k8sTools) != 1 {
		t.Fatalf("ListByAgent(k8s) len = %d, want 1", len(k8sTools))
	}
	if k8sTools[0].Name != "get_pods" {
		t.Errorf("k8s tool name = %q, want %q", k8sTools[0].Name, "get_pods")
	}

	none := r.ListByAgent("incident")
	if len(none) != 0 {
		t.Errorf("ListByAgent(incident) len = %d, want 0", len(none))
	}
}

func TestValidate_MissingRequired(t *testing.T) {
	entries := []ToolEntry{
		{
			Name:        "cancel_query",
			Agent:       "database",
			ActionClass: "write",
			InputSchema: map[string]any{
				"required": []any{"pid", "connection_string"},
			},
		},
	}
	r := New(entries)

	// Args missing "pid"
	err := r.Validate("cancel_query", map[string]any{
		"connection_string": "host=localhost",
	})
	if err == nil {
		t.Error("Validate with missing required param returned nil, want error")
	}
}

func TestValidate_OK(t *testing.T) {
	entries := []ToolEntry{
		{
			Name:        "cancel_query",
			Agent:       "database",
			ActionClass: "write",
			InputSchema: map[string]any{
				"required": []any{"pid", "connection_string"},
			},
		},
	}
	r := New(entries)

	err := r.Validate("cancel_query", map[string]any{
		"pid":               12345,
		"connection_string": "host=localhost",
	})
	if err != nil {
		t.Errorf("Validate with all required params returned error: %v", err)
	}
}

func TestValidate_NoRequiredField(t *testing.T) {
	entries := []ToolEntry{
		{
			Name:        "check_connection",
			Agent:       "database",
			ActionClass: "read",
			InputSchema: map[string]any{
				"properties": map[string]any{},
			},
		},
	}
	r := New(entries)

	// No required field in schema → always OK
	err := r.Validate("check_connection", nil)
	if err != nil {
		t.Errorf("Validate with no required field returned error: %v", err)
	}
}

func TestValidate_NilArgs_WithRequired(t *testing.T) {
	entries := []ToolEntry{
		{
			Name:        "terminate_connection",
			Agent:       "database",
			ActionClass: "destructive",
			InputSchema: map[string]any{
				"required": []any{"pid"},
			},
		},
	}
	r := New(entries)

	err := r.Validate("terminate_connection", nil)
	if err == nil {
		t.Error("Validate(nil args, required params) returned nil, want error")
	}
}

func TestValidate_UnknownTool(t *testing.T) {
	r := New(nil)
	err := r.Validate("no_such_tool", nil)
	if err == nil {
		t.Error("Validate(unknown tool) returned nil, want error")
	}
}
