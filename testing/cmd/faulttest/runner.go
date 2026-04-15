package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"helpdesk/testing/testutil"
)

// Runner sends prompts to agents and captures responses.
type Runner struct {
	cfg *HarnessConfig

	// gatewayCache memoises IsGatewayURL results per URL so we only probe once.
	gatewayCache   map[string]bool
	gatewayCacheMu sync.Mutex
}

// NewRunner creates a new Runner with the given config.
func NewRunner(cfg *HarnessConfig) *Runner {
	return &Runner{
		cfg:          cfg,
		gatewayCache: make(map[string]bool),
	}
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

	if r.isGateway(ctx, agentURL) {
		agentName := categoryToGatewayAgent(f.Category)
		if r.cfg.GatewayAPIKey == "" {
			slog.Warn("gateway detected but no API key set — requests may return 401; pass --api-key or set HELPDESK_CLIENT_API_KEY")
		}
		slog.Info("using gateway REST API", "agent_name", agentName, "purpose", r.cfg.GatewayPurpose)
		return testutil.SendPromptViaGateway(ctx, agentURL, r.cfg.GatewayAPIKey, agentName, prompt, r.cfg.GatewayPurpose)
	}
	return testutil.SendPrompt(ctx, agentURL, prompt)
}

// isGateway returns true if url is a helpdesk gateway, caching the result.
func (r *Runner) isGateway(ctx context.Context, url string) bool {
	r.gatewayCacheMu.Lock()
	defer r.gatewayCacheMu.Unlock()
	if cached, ok := r.gatewayCache[url]; ok {
		return cached
	}
	result := testutil.IsGatewayURL(ctx, url)
	r.gatewayCache[url] = result
	return result
}

func (r *Runner) agentURL(category string) string {
	switch category {
	case "database":
		return r.cfg.DBAgentURL
	case "kubernetes":
		return r.cfg.K8sAgentURL
	case "host":
		return r.cfg.SysadminAgentURL
	case "compound":
		if r.cfg.OrchestratorURL != "" {
			return r.cfg.OrchestratorURL
		}
		return r.cfg.DBAgentURL
	default:
		return ""
	}
}

// categoryToGatewayAgent maps a fault category to the gateway's agent name.
func categoryToGatewayAgent(category string) string {
	switch category {
	case "database":
		return "database"
	case "kubernetes":
		return "kubernetes"
	case "host":
		return "sysadmin"
	case "compound":
		return "database"
	default:
		return category
	}
}
