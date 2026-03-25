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

func TestListFleetEligible(t *testing.T) {
	entries := []ToolEntry{
		{Name: "get_status_summary", Agent: "database", FleetEligible: true},
		{Name: "check_connection", Agent: "database", FleetEligible: false},
		{Name: "get_server_info", Agent: "database"},
	}
	r := New(entries)
	fleet := r.ListFleetEligible()
	if len(fleet) != 1 {
		t.Fatalf("ListFleetEligible() len = %d, want 1", len(fleet))
	}
	if fleet[0].Name != "get_status_summary" {
		t.Errorf("ListFleetEligible()[0].Name = %q, want %q", fleet[0].Name, "get_status_summary")
	}
}

func TestListByCapability(t *testing.T) {
	entries := []ToolEntry{
		{Name: "get_status_summary", Capabilities: []string{CapUptime, CapConnectionCount}},
		{Name: "get_server_info", Capabilities: []string{CapUptime, CapVersion}},
		{Name: "check_connection", Capabilities: []string{CapConnectivity}},
	}
	r := New(entries)

	uptimeTools := r.ListByCapability(CapUptime)
	if len(uptimeTools) != 2 {
		t.Fatalf("ListByCapability(uptime) len = %d, want 2", len(uptimeTools))
	}

	connTools := r.ListByCapability(CapConnectivity)
	if len(connTools) != 1 || connTools[0].Name != "check_connection" {
		t.Errorf("ListByCapability(connectivity) = %v, want [check_connection]", connTools)
	}

	none := r.ListByCapability(CapLogs)
	if len(none) != 0 {
		t.Errorf("ListByCapability(logs) len = %d, want 0", len(none))
	}
}

func TestResolveSuperseded_BasicCase(t *testing.T) {
	entries := []ToolEntry{
		{Name: "get_status_summary", Supersedes: []string{"get_server_info", "get_connection_stats"}},
		{Name: "get_server_info"},
		{Name: "get_connection_stats"},
	}
	r := New(entries)

	input := []string{"get_server_info", "get_connection_stats", "get_status_summary"}
	got := r.ResolveSuperseded(input)
	if len(got) != 1 || got[0] != "get_status_summary" {
		t.Errorf("ResolveSuperseded = %v, want [get_status_summary]", got)
	}
}

func TestResolveSuperseded_NoSuperior(t *testing.T) {
	entries := []ToolEntry{
		{Name: "get_server_info"},
		{Name: "get_connection_stats"},
	}
	r := New(entries)

	input := []string{"get_server_info", "get_connection_stats"}
	got := r.ResolveSuperseded(input)
	if len(got) != 2 {
		t.Errorf("ResolveSuperseded = %v, want unchanged [get_server_info, get_connection_stats]", got)
	}
}

func TestResolveSuperseded_DisjointSet(t *testing.T) {
	// Superior is in the registry but NOT in the input — subordinates should stay.
	entries := []ToolEntry{
		{Name: "get_status_summary", Supersedes: []string{"get_server_info", "get_connection_stats"}},
		{Name: "get_server_info"},
		{Name: "get_connection_stats"},
	}
	r := New(entries)

	// get_status_summary not in input → subordinates not removed
	input := []string{"get_server_info", "get_connection_stats"}
	got := r.ResolveSuperseded(input)
	if len(got) != 2 {
		t.Errorf("ResolveSuperseded = %v, want unchanged [get_server_info, get_connection_stats]", got)
	}
}

func TestParseSkillTags(t *testing.T) {
	tags := []string{
		"postgresql",
		"fleet:true",
		"cap:uptime",
		"cap:connection_count",
		"supersedes:get_server_info",
		"supersedes:get_connection_stats",
	}
	fleet, caps, supersedes := parseSkillTags(tags)
	if !fleet {
		t.Error("fleet = false, want true")
	}
	if len(caps) != 2 || caps[0] != "uptime" || caps[1] != "connection_count" {
		t.Errorf("caps = %v, want [uptime connection_count]", caps)
	}
	if len(supersedes) != 2 || supersedes[0] != "get_server_info" || supersedes[1] != "get_connection_stats" {
		t.Errorf("supersedes = %v, want [get_server_info get_connection_stats]", supersedes)
	}
}

func TestParseSkillTags_Empty(t *testing.T) {
	fleet, caps, supersedes := parseSkillTags([]string{"postgresql", "database"})
	if fleet || len(caps) != 0 || len(supersedes) != 0 {
		t.Errorf("parseSkillTags with no taxonomy tags: fleet=%v caps=%v supersedes=%v", fleet, caps, supersedes)
	}
}
