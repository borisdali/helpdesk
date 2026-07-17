package main

import (
	"context"
	"testing"

	"helpdesk/internal/infra"
)

// expectedSysadminTools is the canonical list of LLM-callable tools that must
// be registered in NewSysadminDirectRegistry. Admin-only tools (register_infra_db)
// are tested separately and intentionally excluded here.
var expectedSysadminTools = []string{
	"check_host",
	"get_host_logs",
	"check_disk",
	"check_memory",
	"read_pg_log_file",
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
				VMName:           "prod-vm",
				ContainerName:    "alloydb-omni",
			},
		},
		VMs: map[string]infra.VM{
			"prod-vm": {
				Name:    "prod-vm",
				Address: "localhost",
				Runtime: "docker",
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

// TestSysadminDirectRegistry_RegisterInfraDB verifies that register_infra_db is
// callable via the registry and that the registered server can subsequently be
// resolved by sysadmin tools.
func TestSysadminDirectRegistry_RegisterInfraDB(t *testing.T) {
	old := infraConfig
	infraConfig = nil
	t.Cleanup(func() { infraConfig = old })

	r := NewSysadminDirectRegistry()
	fn, ok := r.Get("register_infra_db")
	if !ok {
		t.Fatal("register_infra_db not registered in NewSysadminDirectRegistry()")
	}

	out, err := fn(context.Background(), map[string]any{
		"server_id":      "faulttest-auto-15432",
		"container_name": "faulttest-auto-db-abc",
		"runtime":        "docker",
	})
	if err != nil {
		t.Fatalf("register_infra_db via registry: %v", err)
	}
	if out != `{"ok":true}` {
		t.Errorf("output = %q, want {\"ok\":true}", out)
	}

	// Verify the server is now resolvable by sysadmin tools.
	host, err := resolveHost("faulttest-auto-15432")
	if err != nil {
		t.Fatalf("resolveHost after registry registration: %v", err)
	}
	if host.ContainerName != "faulttest-auto-db-abc" {
		t.Errorf("ContainerName = %q, want faulttest-auto-db-abc", host.ContainerName)
	}
}
