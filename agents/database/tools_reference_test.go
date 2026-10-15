package main

import (
	"testing"

	"helpdesk/agentutil/toolref"
)

// TestToolReference fails when a database tool lacks a capability or action
// class, or when docs/tools/database.md is stale. Regenerate with `make docs-tools`.
func TestToolReference(t *testing.T) {
	tools, err := createTools()
	if err != nil {
		t.Fatalf("createTools: %v", err)
	}
	toolref.Check(t, "postgres_database_agent", tools, cardOptions(tools), "../../docs/tools/database.md")
}
