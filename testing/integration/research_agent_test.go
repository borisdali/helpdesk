//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"helpdesk/testing/testutil"
)

const researchAgentURL = "http://localhost:1106"

// TestResearchAgent_AgentCard verifies the research agent serves its agent card.
func TestResearchAgent_AgentCard(t *testing.T) {
	if !isResearchAgentRunning() {
		t.Skip("Research agent not running at " + researchAgentURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cardURL := researchAgentURL + "/.well-known/agent-card.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to fetch agent card: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected HTTP 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	var card map[string]any
	if err := json.Unmarshal(body, &card); err != nil {
		t.Fatalf("Failed to parse agent card: %v", err)
	}

	// Verify expected fields.
	name, ok := card["name"].(string)
	if !ok || name != "research_agent" {
		t.Errorf("Expected name 'research_agent', got %v", card["name"])
	}

	desc, ok := card["description"].(string)
	if !ok || desc == "" {
		t.Error("Expected non-empty description")
	}

	// Should have skills field.
	if _, ok := card["skills"]; !ok {
		t.Error("Expected 'skills' field in agent card")
	}

	t.Logf("Agent card: name=%s, description=%s", name, truncateStr(desc, 50))
}

// TestResearchAgent_BasicQuery tests that the research agent responds to queries.
// This test requires a Gemini API key since the research agent uses GoogleSearch
// which only works with Gemini models.
func TestResearchAgent_BasicQuery(t *testing.T) {
	if !isResearchAgentRunning() {
		t.Skip("Research agent not running at " + researchAgentURL)
	}

	// Check for Gemini API key.
	if os.Getenv("GOOGLE_API_KEY") == "" && os.Getenv("HELPDESK_API_KEY") == "" {
		t.Skip("GOOGLE_API_KEY or HELPDESK_API_KEY not set (required for Gemini)")
	}

	// Check model vendor - research agent only works with Gemini.
	vendor := strings.ToLower(os.Getenv("HELPDESK_MODEL_VENDOR"))
	if vendor != "" && vendor != "google" && vendor != "gemini" {
		t.Skip("Research agent only works with Gemini models (HELPDESK_MODEL_VENDOR=" + vendor + ")")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Use testutil to send a prompt.
	resp := sendPromptToAgent(ctx, t, researchAgentURL, "What is PostgreSQL?")
	if resp.Error != nil {
		t.Fatalf("Query failed: %v", resp.Error)
	}

	t.Logf("Response (%d chars, %s): %s", len(resp.Text), resp.Duration, truncateStr(resp.Text, 200))

	// Verify response mentions PostgreSQL.
	if !strings.Contains(strings.ToLower(resp.Text), "postgresql") &&
		!strings.Contains(strings.ToLower(resp.Text), "postgres") {
		t.Errorf("Response should mention PostgreSQL: %s", truncateStr(resp.Text, 300))
	}
}

// TestResearchAgent_WebSearchQuery tests that the research agent can perform web searches.
// This test requires a Gemini API key since GoogleSearch only works with Gemini.
func TestResearchAgent_WebSearchQuery(t *testing.T) {
	if !isResearchAgentRunning() {
		t.Skip("Research agent not running at " + researchAgentURL)
	}

	// Check for Gemini API key.
	if os.Getenv("GOOGLE_API_KEY") == "" && os.Getenv("HELPDESK_API_KEY") == "" {
		t.Skip("GOOGLE_API_KEY or HELPDESK_API_KEY not set (required for Gemini)")
	}

	// Check model vendor - research agent only works with Gemini.
	vendor := strings.ToLower(os.Getenv("HELPDESK_MODEL_VENDOR"))
	if vendor != "" && vendor != "google" && vendor != "gemini" {
		t.Skip("Research agent only works with Gemini models (HELPDESK_MODEL_VENDOR=" + vendor + ")")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Ask for recent information that requires web search.
	prompt := "Search for the latest PostgreSQL version and its release date."
	resp := sendPromptToAgent(ctx, t, researchAgentURL, prompt)
	if resp.Error != nil {
		t.Fatalf("Query failed: %v", resp.Error)
	}

	t.Logf("Response (%d chars, %s)", len(resp.Text), resp.Duration)

	// Verify response has meaningful content.
	if len(resp.Text) < 50 {
		t.Errorf("Response too short: %s", resp.Text)
	}

	// Should mention PostgreSQL and likely a version number.
	lowerText := strings.ToLower(resp.Text)
	if !strings.Contains(lowerText, "postgresql") && !strings.Contains(lowerText, "postgres") {
		t.Errorf("Response should mention PostgreSQL: %s", truncateStr(resp.Text, 300))
	}
}

// isResearchAgentRunning checks if the research agent is responding.
func isResearchAgentRunning() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		researchAgentURL+"/.well-known/agent-card.json", nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// sendPromptToAgent sends a prompt to an A2A agent and returns the response.
func sendPromptToAgent(ctx context.Context, t *testing.T, agentURL, prompt string) testutil.AgentResponse {
	t.Helper()
	return testutil.SendPrompt(ctx, agentURL, prompt)
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
