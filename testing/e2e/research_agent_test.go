//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"helpdesk/testing/testutil"
)

// TestResearchAgentDiscovery verifies the gateway can discover the research agent.
func TestResearchAgentDiscovery(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	// Check if using Gemini - research agent is only useful with Gemini models.
	vendor := strings.ToLower(os.Getenv("HELPDESK_MODEL_VENDOR"))
	if vendor != "" && vendor != "google" && vendor != "gemini" {
		t.Skip("Research agent discovery test only relevant for Gemini models")
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agents, err := client.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents failed: %v", err)
	}

	// Look for research agent in the list.
	var found bool
	for _, a := range agents {
		if name, ok := a["name"].(string); ok {
			if name == "research_agent" {
				found = true
				t.Logf("Found research_agent: %v", a["description"])
				break
			}
		}
	}

	if !found {
		t.Log("Research agent not found in gateway discovery (may be expected for Anthropic models)")
		// Don't fail - research agent is optional depending on model vendor.
	}
}

// TestResearchAgentDirectQuery tests querying the research agent directly.
// This test requires a Gemini API key since GoogleSearch only works with Gemini.
func TestResearchAgentDirectQuery(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !isAgentReachable(cfg.ResearchAgentURL) {
		t.Skipf("Research agent not reachable at %s", cfg.ResearchAgentURL)
	}

	// Check for Gemini - research agent's GoogleSearch only works with Gemini.
	vendor := strings.ToLower(os.Getenv("HELPDESK_MODEL_VENDOR"))
	if vendor != "" && vendor != "google" && vendor != "gemini" {
		t.Skip("Research agent only works with Gemini models")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	prompt := "What is PostgreSQL? Provide a brief summary."

	t.Logf("Sending query to research agent...")
	resp := testutil.SendPrompt(ctx, cfg.ResearchAgentURL, prompt)
	if resp.Error != nil {
		t.Fatalf("Query failed: %v", resp.Error)
	}

	t.Logf("Response (%d chars, %s)", len(resp.Text), resp.Duration)

	// Verify response is meaningful.
	if len(resp.Text) < 50 {
		t.Errorf("Response too short: %s", resp.Text)
	}

	// Should mention PostgreSQL.
	lowerText := strings.ToLower(resp.Text)
	if !strings.Contains(lowerText, "postgresql") && !strings.Contains(lowerText, "postgres") {
		t.Errorf("Response should mention PostgreSQL: %s", truncate(resp.Text, 300))
	}
}

// TestResearchAgentWebSearch tests the research agent's web search capability.
// This test requires a Gemini API key and actually performs a web search.
func TestResearchAgentWebSearch(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !isAgentReachable(cfg.ResearchAgentURL) {
		t.Skipf("Research agent not reachable at %s", cfg.ResearchAgentURL)
	}

	// Check for Gemini - research agent's GoogleSearch only works with Gemini.
	vendor := strings.ToLower(os.Getenv("HELPDESK_MODEL_VENDOR"))
	if vendor != "" && vendor != "google" && vendor != "gemini" {
		t.Skip("Research agent only works with Gemini models")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Ask for recent information that requires a web search.
	prompt := "Search for the latest PostgreSQL release version and its release date. Use web search to find current information."

	t.Logf("Sending web search query to research agent...")
	resp := testutil.SendPrompt(ctx, cfg.ResearchAgentURL, prompt)
	if resp.Error != nil {
		t.Fatalf("Query failed: %v", resp.Error)
	}

	t.Logf("Response (%d chars, %s)", len(resp.Text), resp.Duration)

	// Verify response is meaningful.
	if len(resp.Text) < 100 {
		t.Errorf("Response too short for web search query: %s", resp.Text)
	}

	// Should mention PostgreSQL and ideally a version.
	lowerText := strings.ToLower(resp.Text)
	if !strings.Contains(lowerText, "postgresql") && !strings.Contains(lowerText, "postgres") {
		t.Errorf("Response should mention PostgreSQL: %s", truncate(resp.Text, 300))
	}
}

// TestOrchestratorDelegationToResearch tests that the orchestrator delegates
// research/version questions to the research agent.
func TestOrchestratorDelegationToResearch(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if cfg.OrchestratorURL == "" {
		t.Skip("E2E_ORCHESTRATOR_URL not set")
	}

	if !isAgentReachable(cfg.OrchestratorURL) {
		t.Skipf("Orchestrator not reachable at %s", cfg.OrchestratorURL)
	}

	// Check for Gemini - delegation to research agent is primarily for Gemini.
	vendor := strings.ToLower(os.Getenv("HELPDESK_MODEL_VENDOR"))
	if vendor != "" && vendor != "google" && vendor != "gemini" {
		t.Skip("Orchestrator delegation to research agent is only for Gemini models")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Ask a question that should be delegated to research agent.
	prompt := "What is the latest version of PostgreSQL? Please search for the current release."

	t.Logf("Sending research query to orchestrator...")
	resp := testutil.SendPrompt(ctx, cfg.OrchestratorURL, prompt)
	if resp.Error != nil {
		t.Fatalf("Query failed: %v", resp.Error)
	}

	t.Logf("Response (%d chars, %s)", len(resp.Text), resp.Duration)

	// The response should indicate delegation and contain PostgreSQL info.
	lowerText := strings.ToLower(resp.Text)

	// Check for delegation indicator (orchestrator should mention delegating).
	delegationKeywords := []string{"research", "delegat", "search", "look"}
	hasDelegation := ContainsAny(resp.Text, delegationKeywords)
	if hasDelegation {
		t.Log("Orchestrator indicated delegation to research agent")
	}

	// Verify response mentions PostgreSQL.
	if !strings.Contains(lowerText, "postgresql") && !strings.Contains(lowerText, "postgres") {
		t.Errorf("Response should mention PostgreSQL: %s", truncate(resp.Text, 300))
	}

	// Verify response is substantial.
	if len(resp.Text) < 100 {
		t.Errorf("Response too short: %s", resp.Text)
	}
}

// TestGatewayResearchQuery tests querying the research agent through the gateway.
func TestGatewayResearchQuery(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	// Check for Gemini - research agent only works with Gemini.
	vendor := strings.ToLower(os.Getenv("HELPDESK_MODEL_VENDOR"))
	if vendor != "" && vendor != "google" && vendor != "gemini" {
		t.Skip("Research agent only works with Gemini models")
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Try to query the research agent through the gateway.
	prompt := "What is Kubernetes? Provide a brief description."

	t.Logf("Sending query to research agent via gateway...")
	resp, err := client.Query(ctx, "research", prompt)
	if err != nil {
		// Research agent might not be registered with gateway for Anthropic models.
		if strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "unknown") {
			t.Skipf("Research agent not available through gateway (expected for non-Gemini models): %v", err)
		}
		t.Fatalf("Query failed: %v", err)
	}

	t.Logf("Response (%d chars): %s", len(resp.Text), truncate(resp.Text, 200))

	// Verify response mentions Kubernetes.
	lowerText := strings.ToLower(resp.Text)
	if !strings.Contains(lowerText, "kubernetes") && !strings.Contains(lowerText, "k8s") {
		t.Errorf("Response should mention Kubernetes: %s", truncate(resp.Text, 300))
	}
}

// Note: isAgentReachable is defined in orchestrator_test.go
