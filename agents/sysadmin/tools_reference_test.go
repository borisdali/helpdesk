package main

import (
	"testing"

	"helpdesk/agentutil/toolref"
)

// TestToolReference fails when a sysadmin tool lacks a capability or action
// class, or when docs/tools/sysadmin.md is stale. Regenerate with `make docs-tools`.
func TestToolReference(t *testing.T) {
	tools, err := createTools()
	if err != nil {
		t.Fatalf("createTools: %v", err)
	}
	toolref.Check(t, "sysadmin_agent", tools, cardOptions(tools), "../../docs/tools/sysadmin.md")
}
