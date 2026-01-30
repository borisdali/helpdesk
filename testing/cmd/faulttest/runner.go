package main

import (
	"context"
	"fmt"
	"log/slog"

	"helpdesk/testing/testutil"
)

// Runner sends prompts to agents and captures responses.
type Runner struct {
	cfg *HarnessConfig
}

// NewRunner creates a new Runner with the given config.
func NewRunner(cfg *HarnessConfig) *Runner {
	return &Runner{cfg: cfg}
}

// Run sends the failure's prompt to the appropriate agent and returns the response.
func (r *Runner) Run(ctx context.Context, f Failure) testutil.AgentResponse {
	prompt := ResolvePrompt(f.Prompt, r.cfg)
	agentURL := r.agentURL(f.Category)

	if agentURL == "" {
		return testutil.AgentResponse{
			Error: fmt.Errorf("no agent URL configured for category %q", f.Category),
		}
	}

	slog.Info("sending prompt to agent",
		"failure", f.ID,
		"category", f.Category,
		"agent", agentURL,
		"prompt_len", len(prompt),
	)

	timeout := f.TimeoutDuration()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return testutil.SendPrompt(ctx, agentURL, prompt)
}

func (r *Runner) agentURL(category string) string {
	switch category {
	case "database":
		return r.cfg.DBAgentURL
	case "kubernetes":
		return r.cfg.K8sAgentURL
	case "compound":
		if r.cfg.OrchestratorURL != "" {
			return r.cfg.OrchestratorURL
		}
		// Fall back to DB agent for compound failures.
		return r.cfg.DBAgentURL
	default:
		return ""
	}
}
