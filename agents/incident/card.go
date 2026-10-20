package main

import (
	"github.com/a2aproject/a2a-go/a2a"
	"google.golang.org/adk/tool"

	"helpdesk/agentutil"
	"helpdesk/internal/toolregistry"
)

// cardOptions returns the agent card metadata for the incident agent's tools.
// Every tool must declare at least one capability; tools_reference_test.go
// enforces this and keeps docs/tools/incident.md in sync.
func cardOptions(tools []tool.Tool) agentutil.CardOptions {
	return agentutil.CardOptions{
		Version:  "1.0.0",
		Provider: &a2a.AgentProvider{Org: "Helpdesk"},
		SkillTags: map[string][]string{
			"incident_agent":                        {"incident", "diagnostics", "bundle"},
			"incident_agent-create_incident_bundle": {"incident", "bundle", "diagnostics", "tarball"},
			"incident_agent-list_incidents":         {"incident", "listing", "history"},
		},
		SkillExamples: map[string][]string{
			"incident_agent-create_incident_bundle": {
				"Create a diagnostic bundle for the production database",
				"Collect incident data for the database running on Kubernetes",
			},
			"incident_agent-list_incidents": {"Show me all previous incident bundles"},
		},
		SkillCapabilities: map[string][]string{
			"incident_agent-create_incident_bundle": {toolregistry.CapBundleCreate},
			"incident_agent-list_incidents":         {toolregistry.CapIncidentHistory},
		},
	}
}
