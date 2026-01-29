// Package main implements the incident diagnostic bundle agent.
// It collects fresh diagnostic data from multiple infrastructure layers
// (database, Kubernetes, OS, storage) and packages them into a .tar.gz
// bundle for vendor support.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/a2aproject/a2a-go/a2a"
	"google.golang.org/adk/agent/llmagent"

	"helpdesk/agentutil"
	"helpdesk/prompts"
)

func main() {
	cfg := agentutil.MustLoadConfig("localhost:1104")
	ctx := context.Background()

	llmModel, err := agentutil.NewLLM(ctx, cfg)
	if err != nil {
		slog.Error("failed to create LLM model", "err", err)
		os.Exit(1)
	}

	tools, err := createTools()
	if err != nil {
		slog.Error("failed to create tools", "err", err)
		os.Exit(1)
	}

	incidentAgent, err := llmagent.New(llmagent.Config{
		Name:        "incident_agent",
		Description: "Incident diagnostic bundle agent that collects data from database, Kubernetes, OS, and storage layers and packages it into a tarball for vendor support.",
		Instruction: prompts.Incident,
		Model:       llmModel,
		Tools:       tools,
	})
	if err != nil {
		slog.Error("failed to create incident agent", "err", err)
		os.Exit(1)
	}

	cardOpts := agentutil.CardOptions{
		Version:  "1.0.0",
		Provider: &a2a.AgentProvider{Org: "Helpdesk"},
		SkillTags: map[string][]string{
			"incident_agent":                        {"incident", "diagnostics", "bundle"},
			"incident_agent-create_incident_bundle": {"incident", "bundle", "diagnostics", "tarball"},
			"incident_agent-list_incidents":          {"incident", "listing", "history"},
		},
		SkillExamples: map[string][]string{
			"incident_agent-create_incident_bundle": {
				"Create a diagnostic bundle for the production database",
				"Collect incident data for the database running on Kubernetes",
			},
			"incident_agent-list_incidents": {"Show me all previous incident bundles"},
		},
	}

	if err := agentutil.Serve(ctx, incidentAgent, cfg, cardOpts); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
