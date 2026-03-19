package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- callGatewayTool ---

// TestCallGatewayTool_HeadersInjected verifies that every gateway call carries
// X-Purpose: fleet_rollout and X-Purpose-Note with the job_id/server/stage.
func TestCallGatewayTool_HeadersInjected(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"text": "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	cfg := runnerConfig{
		gatewayURL: srv.URL,
		jobID:      "flj_abc123",
	}
	change := Change{Agent: "database", Tool: "run_sql", Args: map[string]any{"sql": "SELECT 1"}}

	_, err := callGatewayTool(context.Background(), cfg, "prod-db-1", "canary", change)
	if err != nil {
		t.Fatalf("callGatewayTool: %v", err)
	}

	if got := gotHeaders.Get("X-Purpose"); got != "fleet_rollout" {
		t.Errorf("X-Purpose = %q, want fleet_rollout", got)
	}
	note := gotHeaders.Get("X-Purpose-Note")
	for _, want := range []string{"flj_abc123", "prod-db-1", "canary"} {
		if !strings.Contains(note, want) {
			t.Errorf("X-Purpose-Note = %q, missing %q", note, want)
		}
	}
}

// TestCallGatewayTool_ServerInjected verifies that db_server is injected into
// the request body automatically.
func TestCallGatewayTool_ServerInjected(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"text": "done"}) //nolint:errcheck
	}))
	defer srv.Close()

	cfg := runnerConfig{gatewayURL: srv.URL, jobID: "flj_x"}
	change := Change{Agent: "database", Tool: "vacuum_analyze", Args: map[string]any{}}

	_, err := callGatewayTool(context.Background(), cfg, "my-db", "wave-1", change)
	if err != nil {
		t.Fatalf("callGatewayTool: %v", err)
	}

	if gotBody["db_server"] != "my-db" {
		t.Errorf("db_server = %v, want my-db", gotBody["db_server"])
	}
}

// TestCallGatewayTool_K8sRoutesCorrectly verifies k8s agent uses /api/v1/k8s/ path.
func TestCallGatewayTool_K8sRoutesCorrectly(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"text": "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	cfg := runnerConfig{gatewayURL: srv.URL, jobID: "flj_y"}
	change := Change{Agent: "k8s", Tool: "get_pods", Args: map[string]any{}}

	callGatewayTool(context.Background(), cfg, "cluster-1", "canary", change) //nolint:errcheck

	if !strings.HasPrefix(gotPath, "/api/v1/k8s/") {
		t.Errorf("path = %q, want /api/v1/k8s/...", gotPath)
	}
}

// TestCallGatewayTool_Non200ReturnsError verifies that non-200 responses are errors.
func TestCallGatewayTool_Non200ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "policy denied", http.StatusForbidden)
	}))
	defer srv.Close()

	cfg := runnerConfig{gatewayURL: srv.URL, jobID: "flj_z"}
	change := Change{Agent: "database", Tool: "run_sql", Args: map[string]any{}}

	_, err := callGatewayTool(context.Background(), cfg, "db-1", "canary", change)
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, expected to contain 403", err.Error())
	}
}

// TestCallGatewayTool_APIKeyHeader verifies that the API key is sent as Bearer token.
func TestCallGatewayTool_APIKeyHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"text": "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	cfg := runnerConfig{gatewayURL: srv.URL, jobID: "flj_k", apiKey: "test-secret-key"}
	change := Change{Agent: "database", Tool: "run_sql", Args: map[string]any{}}

	callGatewayTool(context.Background(), cfg, "db-1", "canary", change) //nolint:errcheck

	if gotAuth != "Bearer test-secret-key" {
		t.Errorf("Authorization = %q, want Bearer test-secret-key", gotAuth)
	}
}

// --- patchServerStatus ---

func TestPatchServerStatus_SendsCorrectPayload(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))
	defer auditSrv.Close()

	cfg := runnerConfig{auditURL: auditSrv.URL, jobID: "flj_p1"}
	err := patchServerStatus(context.Background(), cfg, "prod-db-1", "success", "VACUUM", time.Time{}, time.Now())
	if err != nil {
		t.Fatalf("patchServerStatus: %v", err)
	}

	wantPath := "/v1/fleet/jobs/flj_p1/servers/prod-db-1"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotBody["status"] != "success" {
		t.Errorf("status = %v, want success", gotBody["status"])
	}
	if gotBody["output"] != "VACUUM" {
		t.Errorf("output = %v, want VACUUM", gotBody["output"])
	}
}

func TestPatchServerStatus_NoAuditURL(t *testing.T) {
	// When auditURL is empty, patchServerStatus should be a no-op (no panic, no error).
	cfg := runnerConfig{auditURL: "", jobID: "flj_x"}
	err := patchServerStatus(context.Background(), cfg, "db-1", "running", "", time.Time{}, time.Time{})
	if err != nil {
		t.Errorf("expected nil error for empty auditURL, got %v", err)
	}
}

// --- executeChange ---

func TestExecuteChange_SuccessUpdatesStatus(t *testing.T) {
	// Gateway returns success; auditd receives two PATCH calls (running → success).
	patchCount := 0
	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patchCount++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer auditSrv.Close()

	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"text": "ok"}) //nolint:errcheck
	}))
	defer gatewaySrv.Close()

	cfg := runnerConfig{gatewayURL: gatewaySrv.URL, auditURL: auditSrv.URL, jobID: "flj_e1"}
	change := Change{Agent: "database", Tool: "run_sql", Args: map[string]any{}}

	output, err := executeChange(context.Background(), cfg, "db-1", "canary", change)
	if err != nil {
		t.Fatalf("executeChange: %v", err)
	}
	if output == "" {
		t.Error("expected non-empty output")
	}
	if patchCount != 2 {
		t.Errorf("expected 2 PATCH calls to auditd (running + success), got %d", patchCount)
	}
}

func TestExecuteChange_FailureUpdatesStatusFailed(t *testing.T) {
	var lastStatus string
	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			if s, ok := body["status"].(string); ok {
				lastStatus = s
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer auditSrv.Close()

	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "tool failed", http.StatusInternalServerError)
	}))
	defer gatewaySrv.Close()

	cfg := runnerConfig{gatewayURL: gatewaySrv.URL, auditURL: auditSrv.URL, jobID: "flj_e2"}
	change := Change{Agent: "database", Tool: "run_sql", Args: map[string]any{}}

	_, err := executeChange(context.Background(), cfg, "db-1", "canary", change)
	if err == nil {
		t.Fatal("expected error for failed tool call")
	}
	if lastStatus != "failed" {
		t.Errorf("final auditd status = %q, want failed", lastStatus)
	}
}

// --- runStages: canary abort ---

func TestRunStages_CanaryFailureAbortsJob(t *testing.T) {
	// Gateway always returns 500 — canary should fail and abort.
	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer gatewaySrv.Close()

	callCount := 0
	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer auditSrv.Close()

	cfg := runnerConfig{gatewayURL: gatewaySrv.URL, auditURL: auditSrv.URL, jobID: "flj_canary"}

	servers := []string{"db-1", "db-2", "db-3", "db-4"}
	def := &JobDef{
		Change:   Change{Agent: "database", Tool: "run_sql", Args: map[string]any{}},
		Strategy: Strategy{CanaryCount: 1, WaveSize: 3, FailureThreshold: 0.5},
	}

	gwCallCount := 0
	countSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gwCallCount++
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer countSrv.Close()
	cfg.gatewayURL = countSrv.URL

	err := runStages(context.Background(), cfg, def, servers)
	if err == nil {
		t.Fatal("expected error when canary fails")
	}
	if !strings.Contains(err.Error(), "canary") {
		t.Errorf("error = %q, expected to mention canary", err.Error())
	}
	// Only the canary server (db-1) should have been contacted — not db-2/3/4.
	if gwCallCount != 1 {
		t.Errorf("gateway called %d times, want 1 (canary only)", gwCallCount)
	}
}

// --- runStages: circuit breaker ---

func TestRunStages_CircuitBreakerAbortsWaves(t *testing.T) {
	// Canary passes; first wave has 100% failure → circuit breaker trips.
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		if callNum == 1 {
			// Canary succeeds.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"text": "ok"}) //nolint:errcheck
			return
		}
		// All wave servers fail.
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer auditSrv.Close()

	cfg := runnerConfig{gatewayURL: srv.URL, auditURL: auditSrv.URL, jobID: "flj_cb"}
	servers := []string{"db-canary", "db-w1", "db-w2"}
	def := &JobDef{
		Change:   Change{Agent: "database", Tool: "run_sql", Args: map[string]any{}},
		Strategy: Strategy{CanaryCount: 1, WaveSize: 2, FailureThreshold: 0.5},
	}

	err := runStages(context.Background(), cfg, def, servers)
	if err == nil {
		t.Fatal("expected circuit breaker to trip")
	}
	if !strings.Contains(err.Error(), "circuit breaker") {
		t.Errorf("error = %q, expected circuit breaker message", err.Error())
	}
}

// --- preflight ---

func TestRunPreflight_AllHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"connected"}`)
	}))
	defer srv.Close()

	cfg := preflightConfig{gatewayURL: srv.URL, jobID: "flj_pre"}
	failures := runPreflight(context.Background(), cfg, []string{"db-1", "db-2"})
	if len(failures) != 0 {
		t.Errorf("expected no preflight failures, got %v", failures)
	}
}

func TestRunPreflight_SomeFail(t *testing.T) {
	callNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		if callNum == 2 {
			http.Error(w, "unreachable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"ok"}`)
	}))
	defer srv.Close()

	cfg := preflightConfig{gatewayURL: srv.URL}
	failures := runPreflight(context.Background(), cfg, []string{"db-1", "db-2", "db-3"})
	if len(failures) != 1 {
		t.Errorf("expected 1 preflight failure, got %d", len(failures))
	}
}

func TestPreflightServer_HeadersInjected(t *testing.T) {
	var gotPurpose, gotNote string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPurpose = r.Header.Get("X-Purpose")
		gotNote = r.Header.Get("X-Purpose-Note")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"ok"}`)
	}))
	defer srv.Close()

	cfg := preflightConfig{gatewayURL: srv.URL, jobID: "flj_hdr"}
	preflightServer(context.Background(), cfg, "prod-db-1") //nolint:errcheck

	if gotPurpose != "fleet_rollout" {
		t.Errorf("X-Purpose = %q, want fleet_rollout", gotPurpose)
	}
	if !strings.Contains(gotNote, "flj_hdr") || !strings.Contains(gotNote, "prod-db-1") {
		t.Errorf("X-Purpose-Note = %q, missing job_id or server name", gotNote)
	}
}
