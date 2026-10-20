package main

import (
	"github.com/a2aproject/a2a-go/a2a"
	"google.golang.org/adk/tool"

	"helpdesk/agentutil"
	"helpdesk/internal/toolregistry"
)

// cardOptions returns the agent card metadata for the research agent's tools.
// Every tool must declare at least one capability; tools_reference_test.go
// enforces this and keeps docs/tools/research.md in sync.
func cardOptions(tools []tool.Tool) agentutil.CardOptions {
	return agentutil.CardOptions{
		Version:  "1.0.0",
		Provider: &a2a.AgentProvider{Org: "Helpdesk"},
		SkillTags: map[string][]string{
			"research_agent": {"research", "search", "web", "documentation"},
		},
		SkillExamples: map[string][]string{
			"research_agent": {
				"Search for the latest PostgreSQL 17 release notes",
				"Find best practices for Kubernetes pod autoscaling",
				"Look up recent CVEs for Redis",
			},
		},
		SkillCapabilities: map[string][]string{
			"research_agent-google_search": {toolregistry.CapWebSearch},
		},
	}
}
