package main

import (
	"testing"

	"helpdesk/agentutil/toolref"
)

// TestToolReference fails when an incident tool lacks a capability or action
// class, when a see-also link no longer resolves, or when
// docs/tools/incident.md is stale. Regenerate with `make docs-tools`.
func TestToolReference(t *testing.T) {
	tools, err := createTools()
	if err != nil {
		t.Fatalf("createTools: %v", err)
	}
	toolref.Check(t, toolref.Page{
		Agent: "incident_agent",
		Tools: tools,
		Opts:  cardOptions(tools),
		Path:  "../../docs/tools/incident.md",
		SeeAlso: map[string][]toolref.Link{
			"create_incident_bundle": {{Doc: "INCIDENTS.md", Section: "What an Incident Contains"}},
			"list_incidents":         {{Doc: "INCIDENTS.md", Section: "Listing and Retrieving Incidents"}},
		},
	})
}
