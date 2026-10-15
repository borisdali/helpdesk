package main

import (
	"testing"

	"helpdesk/agentutil/toolref"
)

// TestToolReference fails when a Kubernetes tool lacks a capability or action
// class, or when docs/tools/k8s.md is stale. Regenerate with `make docs-tools`.
func TestToolReference(t *testing.T) {
	tools, err := createTools()
	if err != nil {
		t.Fatalf("createTools: %v", err)
	}
	toolref.Check(t, "k8s_agent", tools, cardOptions(tools), "../../docs/tools/k8s.md")
}
