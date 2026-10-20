package main

import (
	"testing"

	"helpdesk/agentutil/toolref"
)

// TestToolReference fails when a Kubernetes tool lacks a capability or action
// class, when a see-also link no longer resolves, or when docs/tools/k8s.md
// is stale. Regenerate with `make docs-tools`.
func TestToolReference(t *testing.T) {
	tools, err := createTools()
	if err != nil {
		t.Fatalf("createTools: %v", err)
	}
	toolref.Check(t, toolref.Page{
		Agent: "k8s_agent",
		Tools: tools,
		Opts:  cardOptions(tools),
		Path:  "../../docs/tools/k8s.md",
		SeeAlso: map[string][]toolref.Link{
			"describe_pod":       {{Doc: "MUTATION_TOOLS.md", Section: "1.6 `describe_pod`"}},
			"delete_pod":         {{Doc: "MUTATION_TOOLS.md", Section: "1.7 `delete_pod`"}},
			"restart_deployment": {{Doc: "MUTATION_TOOLS.md", Section: "1.8 `restart_deployment`"}},
			"scale_deployment": {
				{Doc: "MUTATION_TOOLS.md", Section: "1.9 `scale_deployment`"},
				{Doc: "ROLLBACK.md", Section: "3.1 Kubernetes"},
			},
			"debug_node_dmesg": {{Doc: "MUTATION_TOOLS.md", Section: "1.10 `debug_node_dmesg`"}},
			"patch_deployment_resources": {
				{Doc: "MUTATION_TOOLS.md", Section: "1.22 `patch_deployment_resources`"},
				{Doc: "ROLLBACK.md", Section: "3.1 Kubernetes"},
			},
		},
	})
}
