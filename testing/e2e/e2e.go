//go:build e2e

// Package e2e contains end-to-end tests that require the full stack including LLM calls.
//
// Run with: go test -tags e2e -timeout 300s -v ./testing/e2e/...
//
// Prerequisites:
//   - Docker running with full stack deployed
//   - docker compose -f deploy/docker-compose/docker-compose.yaml up -d
//   - HELPDESK_API_KEY environment variable set (or ANTHROPIC_API_KEY)
//   - For Kubernetes tests: kind cluster with test namespace
//
// Environment variables:
//   - E2E_GATEWAY_URL: Gateway REST API URL (default: http://localhost:8080)
//   - E2E_DB_AGENT_URL: Database agent A2A URL (default: http://localhost:1100)
//   - E2E_K8S_AGENT_URL: Kubernetes agent A2A URL (default: http://localhost:1102)
//   - E2E_RESEARCH_AGENT_URL: Research agent A2A URL (default: http://localhost:1106)
//   - E2E_ORCHESTRATOR_URL: Orchestrator A2A URL (optional)
//   - E2E_CONN_STR: PostgreSQL connection string
//   - E2E_KUBE_CONTEXT: Kubernetes context (optional)
//   - E2E_CATEGORIES: Comma-separated failure categories to test (optional)
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Config holds E2E test configuration from environment.
type Config struct {
	GatewayURL       string
	DBAgentURL       string
	K8sAgentURL      string
	ResearchAgentURL string
	OrchestratorURL  string
	ConnStr          string
	KubeContext      string
	Categories       []string
}

// LoadConfig reads configuration from environment variables.
func LoadConfig() *Config {
	cfg := &Config{
		GatewayURL:       getEnvDefault("E2E_GATEWAY_URL", "http://localhost:8080"),
		DBAgentURL:       getEnvDefault("E2E_DB_AGENT_URL", "http://localhost:1100"),
		K8sAgentURL:      getEnvDefault("E2E_K8S_AGENT_URL", "http://localhost:1102"),
		ResearchAgentURL: getEnvDefault("E2E_RESEARCH_AGENT_URL", "http://localhost:1106"),
		OrchestratorURL:  os.Getenv("E2E_ORCHESTRATOR_URL"),
		ConnStr:          getEnvDefault("E2E_CONN_STR", "host=localhost port=15432 dbname=testdb user=postgres password=testpass"),
		KubeContext:      os.Getenv("E2E_KUBE_CONTEXT"),
	}

	if cats := os.Getenv("E2E_CATEGORIES"); cats != "" {
		cfg.Categories = strings.Split(cats, ",")
	}

	return cfg
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// RequireAPIKey skips the test if no LLM API key is available.
func RequireAPIKey(t *testing.T) {
	t.Helper()
	if os.Getenv("HELPDESK_API_KEY") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("HELPDESK_API_KEY or ANTHROPIC_API_KEY not set")
	}
}

// A2AResponse mirrors the gateway JSON response shape.
type A2AResponse struct {
	Agent     string `json:"agent"`
	TaskID    string `json:"task_id,omitempty"`
	State     string `json:"state,omitempty"`
	Text      string `json:"text,omitempty"`
	Artifacts []any  `json:"artifacts,omitempty"`
	Error     string `json:"error,omitempty"`
}

// GatewayClient provides helpers for calling the gateway REST API.
type GatewayClient struct {
	BaseURL string
	Client  *http.Client
}

// NewGatewayClient creates a client for the gateway.
func NewGatewayClient(baseURL string) *GatewayClient {
	return &GatewayClient{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		Client: &http.Client{
			Timeout: 120 * time.Second, // LLM calls can be slow
		},
	}
}

// ListAgents calls GET /api/v1/agents.
func (c *GatewayClient) ListAgents(ctx context.Context) ([]map[string]any, error) {
	body, err := c.get(ctx, "/api/v1/agents")
	if err != nil {
		return nil, err
	}

	var agents []map[string]any
	if err := json.Unmarshal(body, &agents); err != nil {
		return nil, fmt.Errorf("decode agents: %w", err)
	}
	return agents, nil
}

// Query sends a message to an agent via POST /api/v1/query.
func (c *GatewayClient) Query(ctx context.Context, agent, message string) (*A2AResponse, error) {
	return c.post(ctx, "/api/v1/query", map[string]any{
		"agent":   agent,
		"message": message,
	})
}

// DBTool calls a database tool via POST /api/v1/db/{tool}.
func (c *GatewayClient) DBTool(ctx context.Context, tool string, args map[string]any) (*A2AResponse, error) {
	return c.post(ctx, "/api/v1/db/"+tool, args)
}

// K8sTool calls a kubernetes tool via POST /api/v1/k8s/{tool}.
func (c *GatewayClient) K8sTool(ctx context.Context, tool string, args map[string]any) (*A2AResponse, error) {
	return c.post(ctx, "/api/v1/k8s/"+tool, args)
}

// CreateIncident calls POST /api/v1/incidents.
func (c *GatewayClient) CreateIncident(ctx context.Context, args map[string]any) (*A2AResponse, error) {
	return c.post(ctx, "/api/v1/incidents", args)
}

// Research calls POST /api/v1/research.
func (c *GatewayClient) Research(ctx context.Context, query string) (*A2AResponse, error) {
	return c.post(ctx, "/api/v1/research", map[string]any{
		"query": query,
	})
}

func (c *GatewayClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *GatewayClient) post(ctx context.Context, path string, payload map[string]any) (*A2AResponse, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode, string(body))
	}

	var result A2AResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("POST %s: decode: %w", path, err)
	}
	return &result, nil
}

// ContainsAny returns true if text contains any of the keywords (case-insensitive).
func ContainsAny(text string, keywords []string) bool {
	lower := strings.ToLower(text)
	for _, kw := range keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// AssertContainsAny checks that text contains at least one keyword.
func AssertContainsAny(t *testing.T, text string, keywords []string) {
	t.Helper()
	if !ContainsAny(text, keywords) {
		t.Errorf("Response missing expected keywords.\nExpected one of: %v\nGot: %s",
			keywords, truncate(text, 500))
	}
}

// AssertNotContains checks that text does not contain any of the keywords.
func AssertNotContains(t *testing.T, text string, keywords []string) {
	t.Helper()
	lower := strings.ToLower(text)
	for _, kw := range keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			t.Errorf("Response unexpectedly contains %q: %s", kw, truncate(text, 500))
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// IsGatewayReachable checks if the gateway is responding.
func IsGatewayReachable(baseURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/agents", nil)
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
