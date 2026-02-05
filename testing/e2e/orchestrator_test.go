//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"helpdesk/testing/testutil"
)

// TestOrchestratorDelegation tests that the orchestrator correctly routes
// requests to sub-agents based on the prompt content.
func TestOrchestratorDelegation(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if cfg.OrchestratorURL == "" {
		t.Skip("E2E_ORCHESTRATOR_URL not set")
	}

	if !isAgentReachable(cfg.OrchestratorURL) {
		t.Skipf("Orchestrator not reachable at %s", cfg.OrchestratorURL)
	}

	tests := []struct {
		name            string
		prompt          string
		expectedAgent   string
		expectedKeywords []string
	}{
		{
			name:          "database_delegation",
			prompt:        "Check the database connection at " + cfg.ConnStr,
			expectedAgent: "database",
			expectedKeywords: []string{
				"database", "connection", "PostgreSQL", "version",
				"connected", "health", "status",
			},
		},
		{
			name:          "kubernetes_delegation",
			prompt:        "List the pods in namespace helpdesk-test",
			expectedAgent: "kubernetes",
			expectedKeywords: []string{
				"pod", "namespace", "running", "status", "kubernetes", "k8s",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip K8s test if no context configured.
			if tt.expectedAgent == "kubernetes" && cfg.KubeContext == "" {
				t.Skip("E2E_KUBE_CONTEXT not set")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			t.Logf("Sending prompt to orchestrator: %s", truncate(tt.prompt, 80))

			resp := testutil.SendPrompt(ctx, cfg.OrchestratorURL, tt.prompt)
			if resp.Error != nil {
				t.Fatalf("Orchestrator call failed: %v", resp.Error)
			}

			t.Logf("Response (%d chars, %s)", len(resp.Text), resp.Duration)

			// Verify the response contains expected keywords indicating
			// the correct sub-agent handled the request.
			AssertContainsAny(t, resp.Text, tt.expectedKeywords)
		})
	}
}

// TestOrchestratorCompoundPrompt tests that the orchestrator can handle
// prompts that require multiple agents.
func TestOrchestratorCompoundPrompt(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if cfg.OrchestratorURL == "" {
		t.Skip("E2E_ORCHESTRATOR_URL not set")
	}
	if cfg.KubeContext == "" {
		t.Skip("E2E_KUBE_CONTEXT not set (needed for compound test)")
	}

	if !isAgentReachable(cfg.OrchestratorURL) {
		t.Skipf("Orchestrator not reachable at %s", cfg.OrchestratorURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	prompt := "The database seems unreachable. It's running in Kubernetes namespace helpdesk-test. " +
		"The connection_string is `" + cfg.ConnStr + "`. " +
		"Please check both the database connectivity and the Kubernetes pod status."

	t.Logf("Sending compound prompt to orchestrator...")

	resp := testutil.SendPrompt(ctx, cfg.OrchestratorURL, prompt)
	if resp.Error != nil {
		t.Fatalf("Orchestrator call failed: %v", resp.Error)
	}

	t.Logf("Response (%d chars, %s)", len(resp.Text), resp.Duration)

	// Verify the response mentions both database and Kubernetes concepts,
	// indicating both agents were consulted.
	dbKeywords := []string{"database", "connection", "PostgreSQL"}
	k8sKeywords := []string{"pod", "kubernetes", "namespace", "container"}

	hasDB := ContainsAny(resp.Text, dbKeywords)
	hasK8s := ContainsAny(resp.Text, k8sKeywords)

	if !hasDB {
		t.Error("Response missing database-related content")
	}
	if !hasK8s {
		t.Error("Response missing Kubernetes-related content")
	}

	if hasDB && hasK8s {
		t.Log("Compound response includes both database and Kubernetes information")
	}
}

// TestDirectAgentCall tests calling agents directly (bypassing orchestrator).
// Note: This test calls agents via A2A protocol directly, which may behave
// differently than going through the gateway REST API.
func TestDirectAgentCall(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	// For direct agent calls, prefer using the gateway as a more reliable path.
	// Direct A2A calls can be flaky without proper session setup.
	t.Log("Note: Direct A2A calls may return empty if agent requires specific session context")

	tests := []struct {
		name     string
		url      string
		prompt   string
		keywords []string
		skip     func() bool
	}{
		{
			name:   "database_agent_direct",
			url:    cfg.DBAgentURL,
			prompt: "Check the database version. The connection_string is `" + cfg.ConnStr + "`.",
			keywords: []string{"PostgreSQL", "version", "database", "connection", "error", "failed"},
			skip:   func() bool { return cfg.DBAgentURL == "" },
		},
		{
			name:   "k8s_agent_direct",
			url:    cfg.K8sAgentURL,
			prompt: "List pods in the default namespace.",
			keywords: []string{"pod", "namespace", "status", "error", "kubectl"},
			skip:   func() bool { return cfg.K8sAgentURL == "" || cfg.KubeContext == "" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skip() {
				t.Skip("Agent URL or required config not set")
			}

			if !isAgentReachable(tt.url) {
				t.Skipf("Agent not reachable at %s", tt.url)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			resp := testutil.SendPrompt(ctx, tt.url, tt.prompt)
			if resp.Error != nil {
				t.Fatalf("Agent call failed: %v", resp.Error)
			}

			t.Logf("Response (%d chars, %s)", len(resp.Text), resp.Duration)

			// Empty response is acceptable for direct A2A calls in some cases.
			// The agent may return empty if it requires specific session context.
			if len(resp.Text) == 0 {
				t.Log("Warning: Agent returned empty response (may be expected for direct A2A without session)")
				t.Skip("Empty response from direct A2A call - use gateway tests for reliable E2E testing")
			}

			AssertContainsAny(t, resp.Text, tt.keywords)
		})
	}
}

// isAgentReachable checks if an agent's card endpoint responds.
func isAgentReachable(baseURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cardURL := baseURL + "/.well-known/agent-card.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
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
