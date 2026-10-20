package main

import (
	"testing"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/geminitool"

	"helpdesk/agentutil/toolref"
)

// TestToolReference fails when a research tool lacks a capability or action
// class, when a see-also link no longer resolves, or when
// docs/tools/research.md is stale. Regenerate with `make docs-tools`.
//
// The research agent's only tool, google_search, is added at runtime for
// Gemini models only, so the test lists it explicitly.
func TestToolReference(t *testing.T) {
	tools := []tool.Tool{geminitool.GoogleSearch{}}
	toolref.Check(t, toolref.Page{
		Agent: "research_agent",
		Tools: tools,
		Opts:  cardOptions(tools),
		Path:  "../../docs/tools/research.md",
		Notes: []string{"`google_search` is available only when the agent runs on a Gemini model. With any other model the research agent publishes no tools and answers from the model alone."},
		SeeAlso: map[string][]toolref.Link{
			"google_search": {{Doc: "API.md", Section: "`POST /api/v1/research`"}},
		},
	})
}
