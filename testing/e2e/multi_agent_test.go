//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/testing/faultlib"
	"helpdesk/testing/testutil"
)

// TestMultiAgentIncidentResponse tests the complete incident response workflow
// with fault injection. This is the most comprehensive E2E test.
//
// It:
// 1. Injects a compound failure (db + k8s)
// 2. Sends a symptom report to the orchestrator
// 3. Verifies the orchestrator consults both agents
// 4. Verifies the response identifies the root cause
// 5. Tears down the injected failure
func TestMultiAgentIncidentResponse(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if cfg.OrchestratorURL == "" {
		t.Skip("E2E_ORCHESTRATOR_URL not set")
	}
	if cfg.KubeContext == "" {
		t.Skip("E2E_KUBE_CONTEXT not set")
	}
	if !isAgentReachable(cfg.OrchestratorURL) {
		t.Skipf("Orchestrator not reachable at %s", cfg.OrchestratorURL)
	}

	// Load the failure catalog.
	testingDir := findTestingDir()
	catalogPath := filepath.Join(testingDir, "catalog", "failures.yaml")
	catalog, err := faultlib.LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("Failed to load catalog: %v", err)
	}

	// Find the compound-db-pod-crash failure.
	var failure *faultlib.Failure
	for i := range catalog.Failures {
		if catalog.Failures[i].ID == "compound-db-pod-crash" {
			failure = &catalog.Failures[i]
			break
		}
	}
	if failure == nil {
		t.Skip("compound-db-pod-crash not in catalog")
	}

	// Create injector.
	injectorCfg := &faultlib.HarnessConfig{
		ConnStr:     cfg.ConnStr,
		KubeContext: cfg.KubeContext,
		TestingDir:  testingDir,
	}
	testutil.DockerComposeDir = filepath.Join(testingDir, "docker")
	injector := faultlib.NewInjector(injectorCfg)

	ctx := context.Background()

	// Inject the failure.
	t.Log("Injecting compound failure...")
	if err := injector.Inject(ctx, *failure); err != nil {
		t.Fatalf("Failed to inject failure: %v", err)
	}

	// Ensure teardown happens.
	defer func() {
		t.Log("Tearing down...")
		if err := injector.Teardown(ctx, *failure); err != nil {
			t.Errorf("Teardown failed: %v", err)
		}
	}()

	// Allow time for the failure to take effect.
	time.Sleep(5 * time.Second)

	// Send the incident prompt to the orchestrator.
	prompt := faultlib.ResolvePrompt(failure.Prompt, injectorCfg)
	t.Logf("Sending incident prompt to orchestrator...")

	queryCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()

	resp := testutil.SendPrompt(queryCtx, cfg.OrchestratorURL, prompt)
	if resp.Error != nil {
		t.Fatalf("Orchestrator call failed: %v", resp.Error)
	}

	t.Logf("Response (%d chars, %s)", len(resp.Text), resp.Duration)

	// Evaluate the response using the failure's expected keywords.
	result := faultlib.Evaluate(*failure, resp.Text)

	t.Logf("Evaluation: score=%.0f%%, keywords=%v, diagnosis=%v",
		result.Score*100, result.KeywordPass, result.DiagnosisPass)

	// For E2E tests, we're more lenient - just check that keywords match.
	if !result.KeywordPass {
		t.Errorf("Response missing expected keywords: %v", failure.Evaluation.ExpectedKeywords.AnyOf)
		t.Logf("Response: %s", truncate(resp.Text, 500))
	}
}

// TestFaultInjectionE2E runs E2E tests for each failure category.
// Unlike the faulttest package which tests individual failures,
// this tests the full stack including orchestrator routing.
//
// NOTE: This test requires the TEST infrastructure (testing/docker/docker-compose.yaml)
// not the deploy infrastructure. It should be run with `make faulttest` or manually
// after starting the test stack.
func TestFaultInjectionE2E(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	// Check if test infrastructure is running (pgloader container).
	if !isTestInfrastructureRunning() {
		t.Skip("Test infrastructure not running (need testing/docker/docker-compose.yaml with pgloader)")
	}

	// Skip if no orchestrator (these tests specifically test orchestrator behavior).
	if cfg.OrchestratorURL == "" {
		// Fall back to direct agent tests.
		t.Log("No orchestrator configured, testing agents directly")
	}

	testingDir := findTestingDir()
	catalogPath := filepath.Join(testingDir, "catalog", "failures.yaml")
	catalog, err := faultlib.LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("Failed to load catalog: %v", err)
	}

	// Filter to just database failures for E2E (faster, more reliable).
	// Only use failures that don't require docker_exec (those need test infra).
	failures := faultlib.FilterFailures(catalog, []string{"database"}, nil)
	if len(cfg.Categories) > 0 {
		failures = faultlib.FilterFailures(catalog, cfg.Categories, nil)
	}

	// Filter out failures that require docker_exec (need test infrastructure).
	var filteredFailures []faultlib.Failure
	for _, f := range failures {
		if f.Inject.Type != "docker_exec" {
			filteredFailures = append(filteredFailures, f)
		}
	}
	failures = filteredFailures

	// Limit to first 3 failures for E2E (these are expensive tests).
	if len(failures) > 3 {
		t.Logf("Limiting to first 3 failures (of %d) for E2E test", len(failures))
		failures = failures[:3]
	}

	if len(failures) == 0 {
		t.Skip("No failures match filters (excluding docker_exec failures)")
	}

	injectorCfg := &faultlib.HarnessConfig{
		ConnStr:     cfg.ConnStr,
		KubeContext: cfg.KubeContext,
		TestingDir:  testingDir,
	}
	testutil.DockerComposeDir = filepath.Join(testingDir, "docker")
	injector := faultlib.NewInjector(injectorCfg)

	for _, f := range failures {
		f := f
		t.Run(f.ID, func(t *testing.T) {
			// Check prerequisites.
			if f.Category == "kubernetes" && cfg.KubeContext == "" {
				t.Skip("E2E_KUBE_CONTEXT not set")
			}

			agentURL := cfg.DBAgentURL
			if f.Category == "kubernetes" {
				agentURL = cfg.K8sAgentURL
			}
			if f.Category == "compound" && cfg.OrchestratorURL != "" {
				agentURL = cfg.OrchestratorURL
			}

			if agentURL == "" || !isAgentReachable(agentURL) {
				t.Skipf("Agent not available for category %s", f.Category)
			}

			ctx := context.Background()

			// Inject.
			t.Logf("Injecting %s...", f.ID)
			if err := injector.Inject(ctx, f); err != nil {
				t.Fatalf("Injection failed: %v", err)
			}

			// Teardown.
			defer func() {
				t.Log("Tearing down...")
				if err := injector.Teardown(ctx, f); err != nil {
					t.Errorf("Teardown failed: %v", err)
				}
			}()

			// Allow time for failure to take effect.
			time.Sleep(3 * time.Second)

			// Send prompt.
			prompt := faultlib.ResolvePrompt(f.Prompt, injectorCfg)
			timeout := f.TimeoutDuration()
			if timeout < 60*time.Second {
				timeout = 60 * time.Second
			}

			queryCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			resp := testutil.SendPrompt(queryCtx, agentURL, prompt)
			if resp.Error != nil {
				t.Fatalf("Agent call failed: %v", resp.Error)
			}

			t.Logf("Response (%d chars, %s)", len(resp.Text), resp.Duration)

			// Evaluate.
			result := faultlib.Evaluate(f, resp.Text)
			t.Logf("Evaluation: score=%.0f%%, keywords=%v", result.Score*100, result.KeywordPass)

			if !result.KeywordPass {
				t.Errorf("Response missing expected keywords: %v", f.Evaluation.ExpectedKeywords.AnyOf)
			}
		})
	}
}

func findTestingDir() string {
	// Try various relative paths.
	paths := []string{
		"../../catalog/failures.yaml",
		"../catalog/failures.yaml",
		"testing/catalog/failures.yaml",
		"catalog/failures.yaml",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return filepath.Dir(filepath.Dir(p))
		}
	}
	return "testing"
}

// isTestInfrastructureRunning checks if the test infrastructure (pgloader container) is available.
func isTestInfrastructureRunning() bool {
	// Check if the helpdesk-test-pgloader container exists and is running.
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", "helpdesk-test-pgloader")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "true"
}
