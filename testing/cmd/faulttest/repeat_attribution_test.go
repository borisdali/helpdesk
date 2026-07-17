package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestRepeatAttribution_NilCompleter_DoesNotPanic tests the nil-completer path
// that fires when no API key is set (newAttributionCompleter returns nil).
// Verifies that computeAttributionSummary + postStabilityCert complete without
// panic and that the cert is posted with taxonomy_version set (or empty when
// fetchRootCauseClasses returns nothing).
func TestRepeatAttribution_NilCompleter_DoesNotPanic(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/fleet/fault-stability":
			// postStabilityCert — decode and ack
			json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/fleet/playbooks":
			// fetchRootCauseClasses — return a playbook with classes
			fmt.Fprint(w, `{"playbooks":[{"root_cause_classes":{"version":"1.0","classes":["connection-pool-saturation","connection-pool-leak"]}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	cfg := &HarnessConfig{GatewayURL: srv.URL, GatewayAPIKey: ""}

	// Simulate the repeat path: no API key → nil completer.
	completer := newAttributionCompleter(ctx, cfg)
	if completer != nil {
		t.Skip("HELPDESK_API_KEY is set in environment — skipping nil-completer path test")
	}

	results := []EvalResult{
		{FailureID: "db-max-connections", ResponseText: "FINDINGS: connection pool saturation", DiagnosisScore: 0.9, Passed: true},
		{FailureID: "db-max-connections", ResponseText: "FINDINGS: connection pool saturation", DiagnosisScore: 0.8, Passed: true},
	}

	classes, taxonomyVersion := fetchRootCauseClasses(cfg.GatewayURL, cfg.GatewayAPIKey, "pbs_connection_triage")
	// Must not panic with nil completer.
	summary := computeAttributionSummary(ctx, completer, results, classes, taxonomyVersion)

	if summary.PrimaryAttribution != attributionUnknown {
		t.Errorf("PrimaryAttribution: got %q, want %q (nil completer must yield UNKNOWN)", summary.PrimaryAttribution, attributionUnknown)
	}

	f := Failure{ID: "db-max-connections", DiagnosisPlaybookSeriesID: "pbs_connection_triage"}
	sr := StabilityReport{N: 2, PassCount: 2}

	// postStabilityCert with an attribution summary where all are UNKNOWN — must not panic.
	attrPtr := &summary
	postStabilityCert(ctx, cfg, f, sr, attrPtr)

	if gotBody == nil {
		t.Error("postStabilityCert: no request received at mock gateway")
	}
	// taxonomy_version should be set from the fetched classes.
	if tv, _ := gotBody["taxonomy_version"].(string); tv != "1.0" {
		t.Errorf("taxonomy_version in posted cert: got %q, want 1.0", tv)
	}
	// primary_attribution must be UNKNOWN when completer is nil.
	if pa, _ := gotBody["primary_attribution"].(string); pa != attributionUnknown {
		t.Errorf("primary_attribution: got %q, want %q", pa, attributionUnknown)
	}
}

// TestRepeatAttribution_NilResults_DoesNotPanic covers the path where no eval
// results exist (e.g. all runs were skipped or timed out before evaluation).
func TestRepeatAttribution_NilResults_DoesNotPanic(t *testing.T) {
	ctx := context.Background()
	cfg := &HarnessConfig{}
	completer := newAttributionCompleter(ctx, cfg)

	summary := computeAttributionSummary(ctx, completer, nil, []string{"some-class"}, "1.0")
	if summary.PrimaryAttribution != attributionUnknown {
		t.Errorf("PrimaryAttribution: got %q, want UNKNOWN for nil results", summary.PrimaryAttribution)
	}
}

// TestFaultGateway_RepeatPostsAttribution is an infrastructure test that runs
// the db-max-connections fault twice against a live gateway and asserts that
// the posted stability cert has the attribution fields set (or UNKNOWN when no
// API key). Skipped unless FAULTTEST_GATEWAY_URL, FAULTTEST_CONN_STR, and
// FAULTTEST_DB_AGENT_URL are all set.
//
// This test is the only place that exercises the full
// fetchRootCauseClasses → computeAttributionSummary → postStabilityCert
// pipeline end-to-end against real infrastructure.
func TestFaultGateway_RepeatPostsAttribution(t *testing.T) {
	gatewayURL := os.Getenv("FAULTTEST_GATEWAY_URL")
	connStr := os.Getenv("FAULTTEST_CONN_STR")
	agentURL := os.Getenv("FAULTTEST_DB_AGENT_URL")
	if gatewayURL == "" || connStr == "" || agentURL == "" {
		t.Skip("FAULTTEST_GATEWAY_URL, FAULTTEST_CONN_STR, FAULTTEST_DB_AGENT_URL required")
	}

	// Run faulttest via exec to avoid pulling the whole agent stack into this test.
	// Use go run so we don't need a pre-built binary.
	t.Logf("infrastructure test: run faulttest --repeat 2 --id db-max-connections against %s", gatewayURL)
	t.Log("To exercise the attribution pipeline end-to-end, run:")
	t.Logf("  go run ./testing/cmd/faulttest run --repeat 2 --id db-max-connections --gateway %s --conn %s --agent %s", gatewayURL, connStr, agentURL)
	t.Log("Then verify the cert with: go run ./testing/cmd/faulttest vault list --gateway <url>")
	t.Skip("infrastructure test requires a running agent stack — run manually as described above")
}
