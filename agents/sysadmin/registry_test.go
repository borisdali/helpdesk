package main

import (
	"context"
	"testing"

	"helpdesk/internal/infra"
)

// expectedSysadminTools is the canonical list of tools that must be registered
// in NewSysadminDirectRegistry. Update whenever a tool is added or removed.
var expectedSysadminTools = []string{
	"check_host",
	"get_host_logs",
	"check_disk",
	"check_memory",
	"restart_container",
	"restart_service",
}

func TestSysadminDirectRegistry_AllToolsRegistered(t *testing.T) {
	r := NewSysadminDirectRegistry()
	for _, name := range expectedSysadminTools {
		if _, ok := r.Get(name); !ok {
			t.Errorf("tool %q not registered in NewSysadminDirectRegistry()", name)
		}
	}
}

// TestArgsToStruct_RoundTrip verifies the JSON round-trip helper.
func TestArgsToStruct_RoundTrip(t *testing.T) {
	args := map[string]any{
		"target": "prod_db",
		"lines":  float64(50),
	}
	got, err := argsToStruct[GetHostLogsArgs](args)
	if err != nil {
		t.Fatalf("argsToStruct: %v", err)
	}
	if got.Target != "prod_db" {
		t.Errorf("Target = %q, want prod_db", got.Target)
	}
	if got.Lines != 50 {
		t.Errorf("Lines = %d, want 50", got.Lines)
	}
}

// TestArgsToStruct_EmptyArgs verifies that an empty map produces a zero-value struct.
func TestArgsToStruct_EmptyArgs(t *testing.T) {
	got, err := argsToStruct[CheckHostArgs](map[string]any{})
	if err != nil {
		t.Fatalf("argsToStruct empty: %v", err)
	}
	if got.Target != "" {
		t.Errorf("Target = %q, want empty", got.Target)
	}
}

// TestSysadminDirectRegistry_CheckHost verifies that check_host can be called
// via the registry using a mock runner and a mock infra config.
func TestSysadminDirectRegistry_CheckHost(t *testing.T) {
	defer withMockRunner("running (running=true, restarting=false, oomkilled=false, dead=false, exitcode=0)", nil)()
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod_db": {
				Name:             "prod_db",
				ConnectionString: "host=localhost",
				Host: &infra.HostConfig{
					ContainerRuntime: "docker",
					ContainerName:    "alloydb-omni",
				},
			},
		},
	}
	t.Cleanup(func() { infraConfig = nil })

	r := NewSysadminDirectRegistry()
	fn, ok := r.Get("check_host")
	if !ok {
		t.Fatal("check_host not registered")
	}
	out, err := fn(context.Background(), map[string]any{"target": "prod_db"})
	if err != nil {
		t.Fatalf("check_host via registry: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output from check_host")
	}
}
