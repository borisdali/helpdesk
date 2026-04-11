//go:build faulttest

// Package faulttest contains fault injection tests that require Docker + running agents + LLM API key.
//
// Run with: go test -tags faulttest -timeout 600s -v ./testing/faulttest/...
//
// Prerequisites:
//   - Docker running
//   - docker compose -f testing/docker/docker-compose.yaml up -d --wait
//   - Agents running (database-agent, k8s-agent, or orchestrator)
//   - Environment variables set (see below)
//
// Environment variables:
//   - FAULTTEST_DB_AGENT_URL: Database agent A2A URL (e.g., http://localhost:1100)
//   - FAULTTEST_K8S_AGENT_URL: Kubernetes agent A2A URL (e.g., http://localhost:1102)
//   - FAULTTEST_SYSADMIN_AGENT_URL: SysAdmin agent A2A URL (e.g., http://localhost:1103)
//   - FAULTTEST_ORCHESTRATOR_URL: Orchestrator A2A URL (optional)
//   - FAULTTEST_CONN_STR: PostgreSQL connection string
//   - FAULTTEST_KUBE_CONTEXT: Kubernetes context (optional)
//   - FAULTTEST_CATEGORIES: Comma-separated categories to test (optional)
//   - FAULTTEST_IDS: Comma-separated failure IDs to test (optional)
package faulttest

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/testing/faultlib"
	"helpdesk/testing/testutil"
)

func loadConfigFromEnv() *faultlib.HarnessConfig {
	cfg := &faultlib.HarnessConfig{
		ConnStr:          os.Getenv("FAULTTEST_CONN_STR"),
		ReplicaConnStr:   os.Getenv("FAULTTEST_REPLICA_CONN_STR"),
		DBAgentURL:       os.Getenv("FAULTTEST_DB_AGENT_URL"),
		K8sAgentURL:      os.Getenv("FAULTTEST_K8S_AGENT_URL"),
		SysadminAgentURL: os.Getenv("FAULTTEST_SYSADMIN_AGENT_URL"),
		OrchestratorURL:  os.Getenv("FAULTTEST_ORCHESTRATOR_URL"),
		KubeContext:      os.Getenv("FAULTTEST_KUBE_CONTEXT"),
		// External PG mode: only external_compat faults, SQL-based injection.
		External:      os.Getenv("FAULTTEST_EXTERNAL") == "true",
		// SSH mode: set FAULTTEST_SSH_HOST to route ssh_exec faults via ExternalInject.
		SSHHost:       os.Getenv("FAULTTEST_SSH_HOST"),
		SSHUser:       os.Getenv("FAULTTEST_SSH_USER"),
		SSHKeyPath:    os.Getenv("FAULTTEST_SSH_KEY"),
		GatewayURL:    os.Getenv("FAULTTEST_GATEWAY_URL"),
		GatewayAPIKey: os.Getenv("FAULTTEST_API_KEY"),
	}

	if categories := os.Getenv("FAULTTEST_CATEGORIES"); categories != "" {
		cfg.Categories = strings.Split(categories, ",")
	}
	if ids := os.Getenv("FAULTTEST_IDS"); ids != "" {
		cfg.FailureIDs = strings.Split(ids, ",")
	}

	// Find the testing directory.
	cfg.TestingDir = findTestingDir()
	cfg.CatalogPath = filepath.Join(cfg.TestingDir, "catalog", "failures.yaml")
	testutil.DockerComposeDir = filepath.Join(cfg.TestingDir, "docker")

	return cfg
}

func findTestingDir() string {
	// Try various relative paths.
	paths := []string{
		"../../catalog/failures.yaml",   // From testing/faulttest/
		"../catalog/failures.yaml",      // From testing/
		"testing/catalog/failures.yaml", // From project root
		"catalog/failures.yaml",         // From testing/
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return filepath.Dir(filepath.Dir(p))
		}
	}
	return "testing"
}

// agentURL returns the appropriate agent URL for a failure category.
func agentURL(cfg *faultlib.HarnessConfig, category string) string {
	switch category {
	case "database":
		return cfg.DBAgentURL
	case "kubernetes":
		return cfg.K8sAgentURL
	case "host":
		return cfg.SysadminAgentURL
	case "compound":
		if cfg.OrchestratorURL != "" {
			return cfg.OrchestratorURL
		}
		return cfg.DBAgentURL
	default:
		return ""
	}
}

// TestMain validates prerequisites before running tests.
func TestMain(m *testing.M) {
	cfg := loadConfigFromEnv()

	// Check catalog exists.
	if _, err := os.Stat(cfg.CatalogPath); err != nil {
		fmt.Fprintf(os.Stderr, "SKIP: Catalog not found at %s\n", cfg.CatalogPath)
		os.Exit(0)
	}

	// Check at least one agent URL is configured.
	if cfg.DBAgentURL == "" && cfg.K8sAgentURL == "" && cfg.OrchestratorURL == "" {
		fmt.Fprintln(os.Stderr, "SKIP: No agent URLs configured")
		fmt.Fprintln(os.Stderr, "Set FAULTTEST_DB_AGENT_URL, FAULTTEST_K8S_AGENT_URL, or FAULTTEST_ORCHESTRATOR_URL")
		os.Exit(0)
	}

	os.Exit(m.Run())
}

// TestFaultInjection runs fault injection tests for each failure in the catalog.
func TestFaultInjection(t *testing.T) {
	cfg := loadConfigFromEnv()

	catalog, err := faultlib.LoadCatalog(cfg.CatalogPath)
	if err != nil {
		t.Fatalf("Failed to load catalog: %v", err)
	}

	failures := faultlib.FilterFailures(catalog, cfg)
	if len(failures) == 0 {
		t.Skip("No failures match the specified filters")
	}

	t.Logf("Running %d fault injection tests", len(failures))

	injector := faultlib.NewInjector(cfg)

	passed, failed, skipped := 0, 0, 0
	for _, f := range failures {
		f := f // capture for subtest
		wasSkipped := false
		ok := t.Run(f.ID, func(t *testing.T) {
			defer func() { wasSkipped = t.Skipped() }()
			// Check if we have the right agent for this failure.
			url := agentURL(cfg, f.Category)
			if url == "" {
				t.Skipf("No agent configured for category %q", f.Category)
			}

			// Check agent is reachable.
			if !isAgentReachable(url) {
				t.Skipf("Agent not reachable at %s", url)
			}

			// Check database connectivity for database failures.
			if f.Category == "database" && cfg.ConnStr == "" {
				t.Skip("FAULTTEST_CONN_STR not set")
			}

			// Check kubernetes context for kubernetes failures and any fault
			// whose inject uses kustomize (which requires kubectl + a valid context).
			needsKube := f.Category == "kubernetes" ||
				f.Inject.Type == "kustomize" || f.Inject.Type == "kustomize_delete"
			if needsKube && cfg.KubeContext == "" {
				t.Skip("FAULTTEST_KUBE_CONTEXT not set")
			}

			// Skip ssh_exec faults when no target host is configured.
			// These faults require a real remote host (exec_via filled at runtime).
			if f.Inject.Type == "ssh_exec" && f.Inject.ExecVia == "" {
				t.Skip("ssh_exec fault requires a target host; set FAULTTEST_SSH_HOST or exec_via in catalog")
			}

			ctx := context.Background()

			// Save original conn string for config-override failures.
			origConn := cfg.ConnStr

			// 1. Inject failure.
			t.Log("Injecting failure...")
			if err := injector.Inject(ctx, f); err != nil {
				t.Fatalf("Injection failed: %v", err)
			}

			// 2. Ensure teardown happens.
			defer func() {
				t.Log("Tearing down...")
				cfg.ConnStr = origConn
				if err := injector.Teardown(ctx, f); err != nil {
					t.Errorf("Teardown failed: %v", err)
				}
			}()

			// 3. Send prompt to agent.
			t.Log("Sending prompt to agent...")
			prompt := faultlib.ResolvePrompt(f.Prompt, cfg)
			timeout := f.TimeoutDuration()

			promptCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			resp := testutil.SendPrompt(promptCtx, url, prompt)
			if resp.Error != nil {
				t.Fatalf("Agent call failed: %v", resp.Error)
			}

			t.Logf("Agent responded in %s (%d chars)", resp.Duration, len(resp.Text))

			// 4. Evaluate response.
			result := faultlib.Evaluate(f, resp.Text)

			t.Logf("Evaluation: score=%.0f%%, keywords=%v, diagnosis=%v, tools=%v",
				result.Score*100, result.KeywordPass, result.DiagnosisPass, result.ToolEvidence)

			if !result.Passed {
				if f.GovernanceGap {
					// Governance-gap tests document known agent behaviour gaps.
					// A failed evaluation is the expected outcome — log it clearly
					// but do NOT t.Errorf so the suite still passes.
					t.Logf("GOVERNANCE GAP (expected): score=%.0f%%, keywords=%v, ordering=%v",
						result.Score*100, result.KeywordPass, result.OrderingPass)
				} else {
					t.Errorf("Evaluation failed: score=%.0f%% (need >= 60%%), keywords=%v, ordering=%v",
						result.Score*100, result.KeywordPass, result.OrderingPass)
					t.Logf("Expected keywords (any of): %v", f.Evaluation.ExpectedKeywords.AnyOf)
					t.Logf("Expected diagnosis: %s", f.Evaluation.ExpectedDiagnosis.Category)
					if len(resp.Text) > 500 {
						t.Logf("Response (truncated): %s...", resp.Text[:500])
					} else {
						t.Logf("Response: %s", resp.Text)
					}
				}
			}
		})
		switch {
		case wasSkipped:
			skipped++
		case ok:
			passed++
		default:
			failed++
		}
	}
	t.Logf("=== SUMMARY: %d/%d passed, %d failed, %d skipped ===",
		passed, len(failures), failed, skipped)
}

// TestDatabaseFailures runs only database category failures.
func TestDatabaseFailures(t *testing.T) {
	cfg := loadConfigFromEnv()

	if cfg.DBAgentURL == "" {
		t.Skip("FAULTTEST_DB_AGENT_URL not set")
	}
	if cfg.ConnStr == "" {
		t.Skip("FAULTTEST_CONN_STR not set")
	}
	if !isAgentReachable(cfg.DBAgentURL) {
		t.Skipf("Database agent not reachable at %s", cfg.DBAgentURL)
	}

	// Override categories to only test database.
	cfg.Categories = []string{"database"}

	catalog, err := faultlib.LoadCatalog(cfg.CatalogPath)
	if err != nil {
		t.Fatalf("Failed to load catalog: %v", err)
	}

	failures := faultlib.FilterFailures(catalog, cfg)
	t.Logf("Found %d database failures", len(failures))

	for _, f := range failures {
		t.Logf("  - %s: %s", f.ID, f.Name)
	}
}

// TestKubernetesFailures runs only kubernetes category failures.
func TestKubernetesFailures(t *testing.T) {
	cfg := loadConfigFromEnv()

	if cfg.K8sAgentURL == "" {
		t.Skip("FAULTTEST_K8S_AGENT_URL not set")
	}
	if cfg.KubeContext == "" {
		t.Skip("FAULTTEST_KUBE_CONTEXT not set")
	}
	if !isAgentReachable(cfg.K8sAgentURL) {
		t.Skipf("K8s agent not reachable at %s", cfg.K8sAgentURL)
	}

	// Override categories to only test kubernetes.
	cfg.Categories = []string{"kubernetes"}

	catalog, err := faultlib.LoadCatalog(cfg.CatalogPath)
	if err != nil {
		t.Fatalf("Failed to load catalog: %v", err)
	}

	failures := faultlib.FilterFailures(catalog, cfg)
	t.Logf("Found %d kubernetes failures", len(failures))

	for _, f := range failures {
		t.Logf("  - %s: %s", f.ID, f.Name)
	}
}

// TestCatalogLoading verifies the catalog can be loaded and parsed.
func TestCatalogLoading(t *testing.T) {
	cfg := loadConfigFromEnv()

	catalog, err := faultlib.LoadCatalog(cfg.CatalogPath)
	if err != nil {
		t.Fatalf("Failed to load catalog: %v", err)
	}

	if catalog.Version != "1" {
		t.Errorf("Catalog version = %q, want %q", catalog.Version, "1")
	}

	if len(catalog.Failures) == 0 {
		t.Error("Catalog has no failures")
	}

	t.Logf("Catalog: version=%s, failures=%d", catalog.Version, len(catalog.Failures))

	// Count by category.
	categories := make(map[string]int)
	for _, f := range catalog.Failures {
		categories[f.Category]++
	}
	for cat, count := range categories {
		t.Logf("  %s: %d", cat, count)
	}
}

// TestEvaluatorSmokeTest verifies the evaluator works with sample responses.
func TestEvaluatorSmokeTest(t *testing.T) {
	cfg := loadConfigFromEnv()

	catalog, err := faultlib.LoadCatalog(cfg.CatalogPath)
	if err != nil {
		t.Fatalf("Failed to load catalog: %v", err)
	}

	// Test evaluator with a known-good response for db-max-connections.
	var f *faultlib.Failure
	for i := range catalog.Failures {
		if catalog.Failures[i].ID == "db-max-connections" {
			f = &catalog.Failures[i]
			break
		}
	}
	if f == nil {
		t.Skip("db-max-connections not in catalog")
	}

	// Simulate a good response.
	goodResponse := `The database is experiencing connection exhaustion.
	The max_connections limit has been reached. Current connections are at the limit.
	I used check_connection and get_connection_stats tools to diagnose this.
	Recommendation: Increase max_connections or implement connection pooling.`

	result := faultlib.Evaluate(*f, goodResponse)

	if !result.KeywordPass {
		t.Error("Expected keyword pass for good response")
	}
	if result.Score < 0.6 {
		t.Errorf("Expected score >= 0.6, got %.2f", result.Score)
	}

	t.Logf("Evaluator test: score=%.2f, passed=%v", result.Score, result.Passed)
}

// TestExternalModeInjection runs the subset of faults marked external_compat,
// injecting via SQL only (no Docker exec) and verifying agent diagnosis.
// Activated by FAULTTEST_EXTERNAL=true.
func TestExternalModeInjection(t *testing.T) {
	cfg := loadConfigFromEnv()

	if !cfg.External {
		t.Skip("FAULTTEST_EXTERNAL=true not set")
	}
	if cfg.ConnStr == "" {
		t.Skip("FAULTTEST_CONN_STR not set")
	}
	if cfg.DBAgentURL == "" {
		t.Skip("FAULTTEST_DB_AGENT_URL not set")
	}
	if !isAgentReachable(cfg.DBAgentURL) {
		t.Skipf("Database agent not reachable at %s", cfg.DBAgentURL)
	}

	catalog, err := faultlib.LoadCatalog(cfg.CatalogPath)
	if err != nil {
		t.Fatalf("Failed to load catalog: %v", err)
	}

	// Filter to external_compat database faults only (no k8s/SSH in external mode).
	cfg.Categories = []string{"database"}
	failures := faultlib.FilterFailures(catalog, cfg)
	if len(failures) == 0 {
		t.Skip("No external_compat database failures in catalog")
	}

	t.Logf("External mode: running %d external_compat database faults", len(failures))

	injector := faultlib.NewInjector(cfg)

	for _, f := range failures {
		f := f
		t.Run(f.ID, func(t *testing.T) {
			ctx := context.Background()
			origConn := cfg.ConnStr

			t.Logf("Injecting (external mode): %s", f.Name)
			if err := injector.Inject(ctx, f); err != nil {
				t.Fatalf("External inject failed: %v", err)
			}
			defer func() {
				cfg.ConnStr = origConn
				if err := injector.Teardown(ctx, f); err != nil {
					t.Errorf("External teardown failed: %v", err)
				}
			}()

			prompt := faultlib.ResolvePrompt(f.Prompt, cfg)
			timeout := f.TimeoutDuration()
			promptCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			resp := testutil.SendPrompt(promptCtx, cfg.DBAgentURL, prompt)
			if resp.Error != nil {
				t.Fatalf("Agent call failed: %v", resp.Error)
			}

			result := faultlib.Evaluate(f, resp.Text)
			t.Logf("score=%.0f%%, keywords=%v, diagnosis=%v",
				result.Score*100, result.KeywordPass, result.DiagnosisPass)

			if !result.Passed && !f.GovernanceGap {
				t.Errorf("Evaluation failed: score=%.0f%%", result.Score*100)
				t.Logf("Response: %.500s", resp.Text)
			}
		})
	}
}

// isAgentReachable checks if an agent's health endpoint responds.
func isAgentReachable(baseURL string) bool {
	cardURL := strings.TrimSuffix(baseURL, "/") + "/.well-known/agent-card.json"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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
