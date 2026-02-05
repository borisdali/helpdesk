//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestMain validates prerequisites before running E2E tests.
func TestMain(m *testing.M) {
	m.Run()
}

// TestGatewayDiscovery verifies the gateway can list registered agents.
func TestGatewayDiscovery(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agents, err := client.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents failed: %v", err)
	}

	if len(agents) == 0 {
		t.Error("No agents discovered")
	}

	t.Logf("Discovered %d agents:", len(agents))
	for _, a := range agents {
		t.Logf("  - %s", a["name"])
	}

	// Verify expected agents are present.
	names := make(map[string]bool)
	for _, a := range agents {
		if name, ok := a["name"].(string); ok {
			names[name] = true
		}
	}

	expectedAgents := []string{"postgres_database_agent"}
	for _, expected := range expectedAgents {
		if !names[expected] {
			t.Errorf("Expected agent %q not found", expected)
		}
	}
}

// TestGatewayHealthCheck tests the database health check workflow.
func TestGatewayHealthCheck(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Call the check_connection tool.
	resp, err := client.DBTool(ctx, "check_connection", map[string]any{
		"connection_string": cfg.ConnStr,
	})
	if err != nil {
		t.Fatalf("check_connection failed: %v", err)
	}

	t.Logf("Response (%d chars): %s", len(resp.Text), truncate(resp.Text, 200))

	// Verify response indicates success or a recognized error.
	successKeywords := []string{"PostgreSQL", "version", "Connection successful", "connected"}
	errorKeywords := []string{"connection refused", "authentication failed", "does not exist", "timeout"}

	if !ContainsAny(resp.Text, successKeywords) && !ContainsAny(resp.Text, errorKeywords) {
		t.Errorf("Response doesn't indicate clear success or failure: %s", truncate(resp.Text, 300))
	}
}

// TestGatewayAIDiagnosis tests the AI-powered diagnosis workflow.
func TestGatewayAIDiagnosis(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	prompt := "Users are reporting slow database queries. The connection_string is `" +
		cfg.ConnStr + "`. Please check the database health and report any issues."

	t.Logf("Sending diagnosis prompt to database agent...")

	resp, err := client.Query(ctx, "database", prompt)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	t.Logf("Agent response (%d chars)", len(resp.Text))

	// Verify the response is substantial and mentions diagnostic concepts.
	if len(resp.Text) < 50 {
		t.Errorf("Response too short: %s", resp.Text)
	}

	// The response should mention something database-related.
	dbKeywords := []string{
		"database", "connection", "query", "performance",
		"PostgreSQL", "cache", "statistics", "health",
	}
	AssertContainsAny(t, resp.Text, dbKeywords)
}

// TestGatewayQueryUnknownAgent tests error handling for unknown agents.
func TestGatewayQueryUnknownAgent(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := client.Query(ctx, "nonexistent_agent", "Hello")
	if err == nil {
		t.Error("Expected error for unknown agent, got nil")
	}

	if !strings.Contains(err.Error(), "400") && !strings.Contains(err.Error(), "unknown") {
		t.Logf("Error (acceptable): %v", err)
	}
}

// TestGatewayIncidentBundle tests the incident bundle creation workflow.
func TestGatewayIncidentBundle(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	// Start a callback server.
	callbackCh := make(chan map[string]any, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	callbackAddr := listener.Addr().String()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /callback", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		select {
		case callbackCh <- payload:
		default:
		}
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)
	defer srv.Close()

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	callbackURL := "http://" + callbackAddr + "/callback"
	t.Logf("Creating incident bundle with callback: %s", callbackURL)

	resp, err := client.CreateIncident(ctx, map[string]any{
		"infra_key":         "e2e-test",
		"description":       "E2E test incident",
		"connection_string": cfg.ConnStr,
		"callback_url":      callbackURL,
	})
	if err != nil {
		t.Fatalf("CreateIncident failed: %v", err)
	}

	t.Logf("Incident agent responded (%d chars)", len(resp.Text))

	// Wait for callback (with timeout).
	select {
	case cb := <-callbackCh:
		t.Log("Callback received!")
		if bundlePath, ok := cb["bundle_path"].(string); ok {
			t.Logf("  bundle_path: %s", bundlePath)
		}
		if incidentID, ok := cb["incident_id"].(string); ok {
			t.Logf("  incident_id: %s", incidentID)
		}
	case <-time.After(60 * time.Second):
		t.Log("Warning: No callback received within 60s (may be expected if incident agent not configured)")
	case <-ctx.Done():
		t.Log("Context cancelled")
	}
}

// TestSREBotWorkflow runs the complete SRE Bot workflow:
// 1. Discovery - list agents
// 2. Health check - call check_connection
// 3. AI diagnosis - send symptom to agent
func TestSREBotWorkflow(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)

	// Phase 1: Discovery
	t.Run("Phase1_Discovery", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		agents, err := client.ListAgents(ctx)
		if err != nil {
			t.Fatalf("Discovery failed: %v", err)
		}
		t.Logf("Found %d agents", len(agents))
	})

	// Phase 2: Health Check
	var anomalyDetected bool
	t.Run("Phase2_HealthCheck", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		resp, err := client.DBTool(ctx, "check_connection", map[string]any{
			"connection_string": cfg.ConnStr,
		})
		if err != nil {
			t.Logf("Health check error (may be expected): %v", err)
			anomalyDetected = true
			return
		}

		anomalyKeywords := []string{
			"error", "fail", "refused", "timeout", "too many",
			"denied", "unreachable", "crash", "oom", "killed",
		}
		anomalyDetected = ContainsAny(resp.Text, anomalyKeywords)
		if anomalyDetected {
			t.Logf("Anomaly detected in health check response")
		} else {
			t.Logf("Health check OK")
		}
	})

	// Phase 3: AI Diagnosis (always run for E2E test)
	t.Run("Phase3_AIDiagnosis", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		symptom := "Users are reporting database connectivity issues."
		if anomalyDetected {
			symptom = "The database health check detected an anomaly."
		}

		prompt := symptom + " The connection_string is `" + cfg.ConnStr +
			"`. Please investigate and report your findings."

		resp, err := client.Query(ctx, "database", prompt)
		if err != nil {
			t.Fatalf("AI diagnosis failed: %v", err)
		}

		t.Logf("Diagnosis response (%d chars)", len(resp.Text))
		if len(resp.Text) < 50 {
			t.Error("Diagnosis response too short")
		}
	})
}
