//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io"
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
	errorKeywords := []string{"connection refused", "authentication failed", "does not exist", "timeout", "policy denied", "DENIED"}

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
		SkipIfLLMKeyInvalid(t, err.Error())
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
		SkipIfLLMKeyInvalid(t, err.Error())
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

// TestGatewayResearch tests the research agent endpoint.
func TestGatewayResearch(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	query := "What is the latest stable version of PostgreSQL?"
	t.Logf("Sending research query: %s", query)

	resp, err := client.Research(ctx, query)
	if err != nil {
		SkipIfLLMKeyInvalid(t, err.Error())
		t.Fatalf("Research failed: %v", err)
	}

	t.Logf("Research response (%d chars): %s", len(resp.Text), truncate(resp.Text, 300))

	// Verify the response is substantial.
	if len(resp.Text) < 50 {
		t.Errorf("Response too short: %s", resp.Text)
	}

	// The response should mention PostgreSQL or version.
	researchKeywords := []string{"postgresql", "postgres", "version", "release", "17", "16", "15"}
	AssertContainsAny(t, resp.Text, researchKeywords)
}

// TestGatewayResearchMissingQuery tests error handling for empty query.
func TestGatewayResearchMissingQuery(t *testing.T) {
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.Research(ctx, "")
	if err == nil {
		t.Error("Expected error for empty query, got nil")
	}

	if !strings.Contains(err.Error(), "400") {
		t.Logf("Error (acceptable): %v", err)
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
			SkipIfLLMKeyInvalid(t, err.Error())
			t.Fatalf("AI diagnosis failed: %v", err)
		}

		t.Logf("Diagnosis response (%d chars)", len(resp.Text))
		if len(resp.Text) < 50 {
			t.Error("Diagnosis response too short")
		}
	})
}

// TestGatewayFaultStabilityRoundtrip exercises the full POST→GET→LIST path for
// the fault-stability cert endpoint via the gateway proxy (no LLM call required).
// This test is separate from the unit tests because it catches proxy misconfiguration
// (wrong path forwarding, missing auth headers) that in-process handler tests cannot catch.
func TestGatewayFaultStabilityRoundtrip(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const faultID = "e2e-test-fault-stability"
	cert := map[string]any{
		"fault_id":           faultID,
		"fault_name":         "E2E test fault",
		"playbook_series_id": "pbs_e2e_test",
		"diagnosis_model":    "claude-sonnet-4-6",
		"judge_model":        "claude-haiku-4-5-20251001",
		"n_runs":             5,
		"pass_rate":          1.0,
		"conf_range_pp":      3,
		"is_stable":          true,
	}

	// POST — upsert. v0.25.0 changed the success response from 204 No
	// Content to 200 OK with a JSON body (carrying the new "regressed"
	// field) — the endpoint stayed backward-compatible in spirit (still a
	// plain success), just with a body now.
	code, err := client.FaultStabilityUpsert(ctx, cert)
	if err != nil {
		t.Fatalf("FaultStabilityUpsert: %v", err)
	}
	if code != http.StatusOK {
		t.Fatalf("POST fault-stability: got HTTP %d, want 200", code)
	}

	// GET — verify round-trip through gateway→auditd.
	got, err := client.FaultStabilityGet(ctx, faultID)
	if err != nil {
		t.Fatalf("FaultStabilityGet: %v", err)
	}
	if id, _ := got["fault_id"].(string); id != faultID {
		t.Errorf("fault_id: got %q, want %q", id, faultID)
	}
	if stable, _ := got["is_stable"].(bool); !stable {
		t.Error("is_stable: want true")
	}
	if runs, _ := got["n_runs"].(float64); int(runs) != 5 {
		t.Errorf("n_runs: got %v, want 5", runs)
	}
	if dm, _ := got["diagnosis_model"].(string); dm != "claude-sonnet-4-6" {
		t.Errorf("diagnosis_model: got %q, want claude-sonnet-4-6", dm)
	}
	if jm, _ := got["judge_model"].(string); jm != "claude-haiku-4-5-20251001" {
		t.Errorf("judge_model: got %q, want claude-haiku-4-5-20251001", jm)
	}

	// LIST — cert must appear in the list.
	certs, err := client.FaultStabilityList(ctx)
	if err != nil {
		t.Fatalf("FaultStabilityList: %v", err)
	}
	found := false
	for _, c := range certs {
		if id, _ := c["fault_id"].(string); id == faultID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("fault_id %q not found in list of %d certs", faultID, len(certs))
	}

	// Upsert again with updated values — verify overwrite.
	updated := map[string]any{
		"fault_id":  faultID,
		"n_runs":    10,
		"pass_rate": 0.9,
		"is_stable": true,
	}
	if code, err = client.FaultStabilityUpsert(ctx, updated); err != nil || code != http.StatusOK {
		t.Fatalf("second upsert: code=%d err=%v", code, err)
	}
	got2, err := client.FaultStabilityGet(ctx, faultID)
	if err != nil {
		t.Fatalf("FaultStabilityGet after update: %v", err)
	}
	if runs, _ := got2["n_runs"].(float64); int(runs) != 10 {
		t.Errorf("n_runs after overwrite: got %v, want 10", runs)
	}
}

// TestGatewayFaultStability_AttributionRoundtrip verifies that all 5 v0.21.0
// attribution fields survive a POST → GET round-trip through gateway→auditd.
func TestGatewayFaultStability_AttributionRoundtrip(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const faultID = "e2e-test-fault-attribution"
	cert := map[string]any{
		"fault_id":                 faultID,
		"fault_name":               "E2E attribution test",
		"diagnosis_model":          "claude-sonnet-4-6",
		"n_runs":                   3,
		"pass_rate":                1.0,
		"is_stable":                true,
		"primary_attribution":      "connection-pool-saturation",
		"attribution_consistent":   true,
		"attribution_distribution": map[string]any{"connection-pool-saturation": 3, "connection-pool-leak": 0},
		"judge_spread":             0.12,
		"taxonomy_version":         "1.0",
	}

	code, err := client.FaultStabilityUpsert(ctx, cert)
	if err != nil {
		t.Fatalf("FaultStabilityUpsert: %v", err)
	}
	// v0.25.0 changed this endpoint's success response from a bare 204 to 200
	// with a {"regressed": bool} body (see Upsert's regression-detection
	// return value) — 204 was the pre-v0.25.0 contract.
	if code != http.StatusOK {
		t.Fatalf("POST fault-stability: got HTTP %d, want 200", code)
	}

	got, err := client.FaultStabilityGet(ctx, faultID)
	if err != nil {
		t.Fatalf("FaultStabilityGet: %v", err)
	}

	// Skip attribution assertions if the gateway doesn't have v0.21.0 schema yet.
	// This makes the test CI-safe on deployments that haven't been updated.
	pa, _ := got["primary_attribution"].(string)
	if pa == "" {
		t.Skip("gateway does not return attribution fields — deploy v0.21.0 and restart to exercise this test")
	}

	if pa != "connection-pool-saturation" {
		t.Errorf("primary_attribution: got %q, want connection-pool-saturation", pa)
	}
	if ac, _ := got["attribution_consistent"].(bool); !ac {
		t.Errorf("attribution_consistent: got %v, want true", got["attribution_consistent"])
	}
	if tv, _ := got["taxonomy_version"].(string); tv != "1.0" {
		t.Errorf("taxonomy_version: got %q, want 1.0", tv)
	}
	if js, _ := got["judge_spread"].(float64); js < 0.11 || js > 0.13 {
		t.Errorf("judge_spread: got %v, want ~0.12", got["judge_spread"])
	}
	if ad, ok := got["attribution_distribution"].(map[string]any); !ok || len(ad) == 0 {
		t.Errorf("attribution_distribution: got %v, want non-empty map", got["attribution_distribution"])
	}
}

// TestGatewayFaultStability_VersioningHistoryRegression_Roundtrip verifies
// the v0.25.0 additions through a real gateway→auditd round-trip:
// playbook_version/playbook_updated_at/playbook_id survive POST→GET, an
// append-only history entry is recorded on every upsert (not just the
// latest-snapshot row), and Upsert's "regressed" field correctly reports the
// transition from earning trust (STABLE+CLEAN+attribution-consistent) to not.
func TestGatewayFaultStability_VersioningHistoryRegression_Roundtrip(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("Gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const faultID = "e2e-test-fault-versioning"
	const model = "claude-sonnet-4-6"

	earning := map[string]any{
		"fault_id": faultID, "fault_name": "E2E versioning test",
		"diagnosis_model": model, "n_runs": 5, "pass_rate": 1.0, "is_stable": true,
		"is_clean": true, "attribution_consistent": true,
		"playbook_version": "1.4", "playbook_updated_at": "2026-08-01T00:00:00Z",
		"playbook_id": "pb_e2e_versioning",
	}
	code, resp1, err := client.FaultStabilityUpsertWithBody(ctx, earning)
	if err != nil {
		t.Fatalf("FaultStabilityUpsertWithBody (1st): %v", err)
	}
	if code != http.StatusOK {
		t.Fatalf("POST fault-stability: got HTTP %d, want 200", code)
	}

	// Skip the rest if the gateway/auditd doesn't have the v0.25.0 schema
	// yet — same CI-safety pattern as the attribution round-trip test.
	regressed1, hasRegressed := resp1["regressed"]
	if !hasRegressed {
		t.Skip("gateway does not return a 'regressed' field — deploy v0.25.0 and restart to exercise this test")
	}
	if regressed1 != false {
		t.Errorf("regressed on first-ever cert: got %v, want false (no prior state to regress from)", regressed1)
	}

	// playbook_version / playbook_updated_at round-trip.
	got, err := client.FaultStabilityGet(ctx, faultID)
	if err != nil {
		t.Fatalf("FaultStabilityGet: %v", err)
	}
	if pv, _ := got["playbook_version"].(string); pv != "1.4" {
		t.Errorf("playbook_version: got %q, want 1.4", pv)
	}
	if pu, _ := got["playbook_updated_at"].(string); pu == "" {
		t.Error("playbook_updated_at: got empty, want a timestamp")
	}
	if pid, _ := got["playbook_id"].(string); pid != "pb_e2e_versioning" {
		t.Errorf("playbook_id: got %q, want pb_e2e_versioning", pid)
	}

	// Second upsert: same fault+model, now failing CLEAN — must report
	// regressed=true, and it must be visible in the history log.
	noLongerEarning := map[string]any{
		"fault_id": faultID, "diagnosis_model": model, "n_runs": 5,
		"is_stable": true, "is_clean": false, "warning_count": 2, "attribution_consistent": true,
		"playbook_version": "1.4", "playbook_updated_at": "2026-08-01T00:00:00Z",
	}
	_, resp2, err := client.FaultStabilityUpsertWithBody(ctx, noLongerEarning)
	if err != nil {
		t.Fatalf("FaultStabilityUpsertWithBody (2nd): %v", err)
	}
	if regressed2, _ := resp2["regressed"].(bool); !regressed2 {
		t.Errorf("regressed on the CLEAN=false upsert: got %v, want true", resp2["regressed"])
	}

	// History must show both upserts, most recent first.
	history, err := client.FaultStabilityHistory(ctx, faultID, model, 10)
	if err != nil {
		t.Fatalf("FaultStabilityHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history entries: got %d, want 2 (one per upsert)", len(history))
	}
	if clean, _ := history[0]["is_clean"].(bool); clean {
		t.Error("most recent history entry: want is_clean=false")
	}
	if clean, _ := history[1]["is_clean"].(bool); !clean {
		t.Error("oldest history entry: want is_clean=true")
	}
}

// TestGatewayMetricsEndpoint verifies that GET /metrics on the gateway returns
// Prometheus text format with the gateway_fabrication_mismatches_total counter
// declared. The counter value may be zero if no mismatches have occurred.
func TestGatewayMetricsEndpoint(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	resp, err := http.Get(cfg.GatewayURL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: status %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, "gateway_fabrication_mismatches_total") {
		t.Errorf("/metrics missing gateway_fabrication_mismatches_total; body:\n%s", body)
	}
}
