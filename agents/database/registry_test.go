package main

import (
	"context"
	"testing"
)

// expectedDBTools is the canonical list of tools that must be registered in
// NewDatabaseDirectRegistry. Update this list whenever a tool is added or removed.
var expectedDBTools = []string{
	"check_connection",
	"get_server_info",
	"get_database_info",
	"get_active_connections",
	"get_connection_stats",
	"get_database_stats",
	"get_config_parameter",
	"get_replication_status",
	"get_lock_info",
	"get_table_stats",
	"get_session_info",
	"cancel_query",
	"terminate_connection",
	"terminate_idle_connections",
	"get_status_summary",
	"get_pg_settings",
	"get_extensions",
	"get_baseline",
	"get_slow_queries",
	"get_vacuum_status",
	"get_disk_usage",
	"get_wait_events",
	"get_blocking_queries",
	"explain_query",
}

func TestDatabaseDirectRegistry_AllToolsRegistered(t *testing.T) {
	r := NewDatabaseDirectRegistry()
	for _, name := range expectedDBTools {
		if _, ok := r.Get(name); !ok {
			t.Errorf("tool %q not registered in NewDatabaseDirectRegistry()", name)
		}
	}
}

// TestArgsToStruct_RoundTrip verifies that map[string]any is correctly
// decoded into a typed struct via the JSON round-trip helper.
func TestArgsToStruct_RoundTrip(t *testing.T) {
	args := map[string]any{
		"connection_string": "postgres://localhost/test",
	}
	got, err := argsToStruct[CheckConnectionArgs](args)
	if err != nil {
		t.Fatalf("argsToStruct: %v", err)
	}
	if got.ConnectionString != "postgres://localhost/test" {
		t.Errorf("ConnectionString = %q, want postgres://localhost/test", got.ConnectionString)
	}
}

// TestArgsToStruct_EmptyArgs verifies that an empty map produces a zero-value struct.
func TestArgsToStruct_EmptyArgs(t *testing.T) {
	got, err := argsToStruct[CheckConnectionArgs](map[string]any{})
	if err != nil {
		t.Fatalf("argsToStruct empty: %v", err)
	}
	if got.ConnectionString != "" {
		t.Errorf("ConnectionString = %q, want empty", got.ConnectionString)
	}
}

// TestDatabaseDirectRegistry_ToolCallable verifies that a registered tool can
// be called via the registry without panicking. Uses withMockRunner to avoid
// spawning a real psql process.
func TestDatabaseDirectRegistry_ToolCallable(t *testing.T) {
	defer withMockRunner("PostgreSQL 16.1\n", nil)()
	r := NewDatabaseDirectRegistry()
	fn, ok := r.Get("check_connection")
	if !ok {
		t.Fatal("check_connection not registered")
	}
	out, err := fn(context.Background(), map[string]any{
		"connection_string": "postgres://localhost/test",
	})
	if err != nil {
		t.Fatalf("check_connection via registry: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output from check_connection")
	}
}
