// Package main implements the research agent.
// It provides web search capabilities using Google Search for finding
// up-to-date information. This agent is used with Gemini models to work
// around the limitation that GoogleSearch cannot be combined with
// function declarations in the same request.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/a2aproject/a2a-go/a2a"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/geminitool"

	"helpdesk/agentutil"
	"helpdesk/prompts"
)

func main() {
	cfg := agentutil.MustLoadConfig("localhost:1106")
	ctx := context.Background()

	// Enforce governance compliance in fix mode before any other initialization.
	agentutil.EnforceFixMode(ctx, agentutil.CheckFixModeViolations(cfg), "research_agent", cfg.AuditURL)

	llmModel, err := agentutil.NewLLM(ctx, cfg)
	if err != nil {
		slog.Error("failed to create LLM model", "err", err)
		os.Exit(1)
	}

	slog.Info("governance", "audit", false, "policy", false)

	// Only add GoogleSearch for Gemini models
	var tools []tool.Tool
	if cfg.ModelVendor == "google" || cfg.ModelVendor == "gemini" {
		tools = append(tools, geminitool.GoogleSearch{})
	}

	researchAgent, err := llmagent.New(llmagent.Config{
		Name:        "research_agent",
		Description: "Research agent that searches the web for current information, documentation, best practices, and recent developments.",
		Instruction: prompts.Research,
		Model:       llmModel,
		Tools:       tools,
		// No SubAgents - this allows GoogleSearch to work on Gemini
	})
	if err != nil {
		slog.Error("failed to create research agent", "err", err)
		os.Exit(1)
	}

	cardOpts := agentutil.CardOptions{
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
	}

	if err := agentutil.Serve(ctx, researchAgent, cfg, cardOpts); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
