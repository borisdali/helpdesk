//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Playbook API smoke tests (no API key required — no LLM calls)
// =============================================================================

// TestPlaybooks_SystemPlaybooksSeededAtStartup verifies that auditd seeds the
// 4 built-in system playbooks on startup and exposes them via the gateway.
// These appear in the default list (is_active=true, is_system=true).
func TestPlaybooks_SystemPlaybooksSeededAtStartup(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	if len(playbooks) == 0 {
		t.Fatal("no playbooks returned — system playbooks may not have been seeded")
	}

	// All default-listed playbooks must be active.
	for _, pb := range playbooks {
		if active, _ := pb["is_active"].(bool); !active {
			t.Errorf("playbook %q: is_active=false in default (active-only) list", pb["name"])
		}
	}

	// All known system series must be present.
	expectedSeries := []string{
		"pbs_vacuum_triage",
		"pbs_slow_query_triage",
		"pbs_connection_triage",
		"pbs_lock_chain_triage",
		"pbs_lock_chain_remediate",
		"pbs_replication_lag",
		"pbs_db_restart_triage",
		"pbs_db_config_recovery",
		"pbs_db_pitr_recovery",
	}
	seriesFound := map[string]bool{}
	for _, pb := range playbooks {
		if sid, ok := pb["series_id"].(string); ok {
			seriesFound[sid] = true
		}
	}
	for _, sid := range expectedSeries {
		if !seriesFound[sid] {
			t.Errorf("expected system series %q not found in playbook list", sid)
		}
	}

	// Count system playbooks.
	systemCount := 0
	for _, pb := range playbooks {
		if sys, _ := pb["is_system"].(bool); sys {
			systemCount++
		}
	}
	if systemCount < len(expectedSeries) {
		t.Errorf("expected at least %d system playbooks, got %d", len(expectedSeries), systemCount)
	}

	t.Logf("playbook list: total=%d system=%d series_found=%v",
		len(playbooks), systemCount, seriesFound)
}

// TestPlaybooks_SystemPlaybooksAreReadOnly verifies that PUT and DELETE on a
// system playbook return 400 Bad Request through the gateway→auditd path.
func TestPlaybooks_SystemPlaybooksAreReadOnly(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}

	// Find a system playbook to attempt mutation on.
	var sysID string
	for _, pb := range playbooks {
		if sys, _ := pb["is_system"].(bool); sys {
			sysID, _ = pb["playbook_id"].(string)
			break
		}
	}
	if sysID == "" {
		t.Skip("no system playbook found to test read-only protection")
	}
	t.Logf("testing read-only protection on system playbook %s", sysID)

	// PUT should return 400.
	putStatus, err := client.PlaybookUpdate(ctx, sysID, map[string]any{
		"name":        "attempted-override",
		"description": "should be rejected",
	})
	if err != nil {
		t.Logf("PlaybookUpdate err (may include status in message): %v", err)
	}
	if putStatus != 400 {
		t.Errorf("PUT system playbook: status = %d, want 400", putStatus)
	}

	// DELETE should also return 400.
	deleteStatus, err := client.PlaybookDelete(ctx, sysID)
	if err != nil {
		t.Logf("PlaybookDelete err: %v", err)
	}
	if deleteStatus != 400 {
		t.Errorf("DELETE system playbook: status = %d, want 400", deleteStatus)
	}
	t.Logf("system playbook read-only OK: PUT→%d DELETE→%d", putStatus, deleteStatus)
}

// TestPlaybooks_CRUDLifecycle exercises the full create→get→list→delete cycle
// for a user-authored playbook through the gateway.
func TestPlaybooks_CRUDLifecycle(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create.
	uniqueName := fmt.Sprintf("e2e-test-playbook-%d", time.Now().UnixNano())
	created, err := client.PlaybookCreate(ctx, map[string]any{
		"name":          uniqueName,
		"description":   "E2E test playbook — safe to delete",
		"problem_class": "performance",
		"symptoms":      []string{"e2e symptom"},
		"guidance":      "e2e guidance",
	})
	if err != nil {
		t.Fatalf("PlaybookCreate: %v", err)
	}
	pbID, _ := created["playbook_id"].(string)
	if pbID == "" {
		t.Fatalf("playbook_id missing from create response: %v", created)
	}
	seriesID, _ := created["series_id"].(string)
	if !strings.HasPrefix(seriesID, "pbs_") {
		t.Errorf("series_id = %q, want pbs_ prefix", seriesID)
	}
	if source, _ := created["source"].(string); source != "manual" {
		t.Errorf("source = %q, want manual", source)
	}
	if active, _ := created["is_active"].(bool); !active {
		t.Error("newly created playbook should have is_active=true")
	}
	t.Logf("created playbook: id=%s series=%s", pbID, seriesID)

	// Get.
	got, err := client.PlaybookGet(ctx, pbID)
	if err != nil {
		t.Fatalf("PlaybookGet: %v", err)
	}
	if got["name"] != uniqueName {
		t.Errorf("name = %q, want %q", got["name"], uniqueName)
	}

	// Appears in list filtered by series_id.
	listed, err := client.PlaybookList(ctx, "series_id="+seriesID)
	if err != nil {
		t.Fatalf("PlaybookList by series: %v", err)
	}
	if len(listed) != 1 || listed[0]["playbook_id"] != pbID {
		t.Errorf("series_id filter: got %d playbooks, want exactly our new one", len(listed))
	}

	// Delete.
	delStatus, err := client.PlaybookDelete(ctx, pbID)
	if err != nil {
		t.Fatalf("PlaybookDelete: %v", err)
	}
	if delStatus != 204 {
		t.Errorf("DELETE status = %d, want 204", delStatus)
	}

	// No longer appears in list.
	afterDelete, err := client.PlaybookList(ctx, "series_id="+seriesID+"&active_only=false")
	if err != nil {
		t.Fatalf("PlaybookList after delete: %v", err)
	}
	for _, pb := range afterDelete {
		if pb["playbook_id"] == pbID {
			t.Error("deleted playbook still appears in list")
		}
	}
	t.Logf("CRUD lifecycle OK: create→get→list→delete for playbook %s", pbID)
}

// TestPlaybooks_ActivateVersion verifies the activate endpoint atomically
// promotes a new version and deactivates the previous one.
func TestPlaybooks_ActivateVersion(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	uniqueName := fmt.Sprintf("e2e-activate-test-%d", time.Now().UnixNano())

	// Create v1 (gets its own series, starts active).
	v1, err := client.PlaybookCreate(ctx, map[string]any{
		"name":        uniqueName + " v1",
		"description": "version one",
	})
	if err != nil {
		t.Fatalf("create v1: %v", err)
	}
	v1ID, _ := v1["playbook_id"].(string)
	seriesID, _ := v1["series_id"].(string)

	// Create v2 in the same series (inactive by default since series_id is set).
	v2, err := client.PlaybookCreate(ctx, map[string]any{
		"name":        uniqueName + " v2",
		"description": "version two",
		"series_id":   seriesID,
	})
	if err != nil {
		t.Fatalf("create v2: %v", err)
	}
	v2ID, _ := v2["playbook_id"].(string)
	if active, _ := v2["is_active"].(bool); active {
		t.Error("v2 should start inactive when series_id is explicitly provided")
	}
	t.Logf("created v1=%s v2=%s series=%s", v1ID, v2ID, seriesID)

	// Activate v2.
	activated, err := client.PlaybookActivate(ctx, v2ID)
	if err != nil {
		t.Fatalf("PlaybookActivate(v2): %v", err)
	}
	if active, _ := activated["is_active"].(bool); !active {
		t.Error("activated playbook should have is_active=true")
	}

	// Fetch v1 — should now be inactive.
	v1After, err := client.PlaybookGet(ctx, v1ID)
	if err != nil {
		t.Fatalf("PlaybookGet(v1 after activate): %v", err)
	}
	if active, _ := v1After["is_active"].(bool); active {
		t.Error("v1 should be inactive after v2 was activated")
	}

	// Default list (active_only) for this series should return only v2.
	listed, err := client.PlaybookList(ctx, "series_id="+seriesID)
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	if len(listed) != 1 {
		t.Errorf("active-only list for series: got %d, want 1", len(listed))
	} else if listed[0]["playbook_id"] != v2ID {
		t.Errorf("active playbook = %v, want %s", listed[0]["playbook_id"], v2ID)
	}

	// Cleanup.
	client.PlaybookDelete(ctx, v1ID) //nolint:errcheck
	client.PlaybookDelete(ctx, v2ID) //nolint:errcheck
	t.Logf("activate version OK: v2 is now active, v1 is inactive")
}

// TestPlaybooks_ImportYAML exercises the import endpoint with format=yaml,
// which takes the direct parse path (no LLM required).
func TestPlaybooks_ImportYAML(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	yamlText := `
series_id: ""
name: E2E Import Test
version: "1.0"
problem_class: performance
author: e2e-test
description: Investigate slow queries on the primary replica.
symptoms:
  - slow queries detected
  - p99 latency elevated
guidance: |
  Start with get_slow_queries. Cross-check with get_wait_events.
  Escalate if any query has been running for more than 30 minutes.
escalation:
  - any query running > 30 minutes
target_hints: []
`

	resp, err := client.PlaybookImport(ctx, map[string]any{
		"text":   yamlText,
		"format": "yaml",
	})
	if err != nil {
		t.Fatalf("PlaybookImport: %v", err)
	}

	draft, _ := resp["draft"].(map[string]any)
	if draft == nil {
		t.Fatalf("import response missing draft field: %v", resp)
	}
	if draft["name"] != "E2E Import Test" {
		t.Errorf("draft.name = %q, want E2E Import Test", draft["name"])
	}
	if draft["source"] != "imported" {
		t.Errorf("draft.source = %q, want imported", draft["source"])
	}
	if draft["problem_class"] != "performance" {
		t.Errorf("draft.problem_class = %q, want performance", draft["problem_class"])
	}

	confidence, _ := resp["confidence"].(float64)
	if confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0 for valid YAML with all required fields", confidence)
	}

	// Draft is not persisted — verify it does NOT appear in the playbook list.
	playbooks, err := client.PlaybookList(ctx, "active_only=false")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	for _, pb := range playbooks {
		if pb["name"] == "E2E Import Test" {
			t.Error("imported draft should not be auto-persisted; found in playbook list")
		}
	}
	t.Logf("YAML import OK: confidence=%.1f source=%v name=%v", confidence, draft["source"], draft["name"])
}

// TestPlaybooks_ListQueryParams verifies the list endpoint honours
// active_only and include_system query parameters.
func TestPlaybooks_ListQueryParams(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// include_system=false should return fewer (or equal) playbooks than the default.
	allDefault, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList default: %v", err)
	}
	noSystem, err := client.PlaybookList(ctx, "include_system=false")
	if err != nil {
		t.Fatalf("PlaybookList include_system=false: %v", err)
	}
	if len(noSystem) > len(allDefault) {
		t.Errorf("include_system=false returned more playbooks (%d) than default (%d)",
			len(noSystem), len(allDefault))
	}
	// None of the no-system results should be system playbooks.
	for _, pb := range noSystem {
		if sys, _ := pb["is_system"].(bool); sys {
			t.Errorf("include_system=false: got system playbook %q in results", pb["name"])
		}
	}

	// Create a second version in an existing series to test active_only=false.
	if len(allDefault) == 0 {
		t.Skip("no playbooks available to test active_only param")
	}
	firstSeries, _ := allDefault[0]["series_id"].(string)
	if firstSeries == "" {
		t.Skip("first playbook has no series_id")
	}

	// Create an inactive second version.
	v2, err := client.PlaybookCreate(ctx, map[string]any{
		"name":        fmt.Sprintf("e2e-v2-%d", time.Now().UnixNano()),
		"description": "inactive second version for list test",
		"series_id":   firstSeries,
	})
	if err != nil {
		t.Fatalf("create v2 for list test: %v", err)
	}
	v2ID, _ := v2["playbook_id"].(string)
	defer client.PlaybookDelete(ctx, v2ID) //nolint:errcheck

	// active_only=false should return more (v1 + v2 at minimum).
	withInactive, err := client.PlaybookList(ctx, "series_id="+firstSeries+"&active_only=false")
	if err != nil {
		t.Fatalf("PlaybookList active_only=false: %v", err)
	}
	if len(withInactive) < 2 {
		t.Errorf("active_only=false for series %s: got %d, want at least 2", firstSeries, len(withInactive))
	}
	t.Logf("list params OK: default=%d no_system=%d series_with_inactive=%d",
		len(allDefault), len(noSystem), len(withInactive))
}

// TestPlaybooks_RunFleetMode calls POST /run on any fleet-mode playbook and
// verifies the response has the fleet plan shape. Skipped when no fleet-mode
// playbook is seeded (the operational triage playbooks were converted from
// fleet to agent in commit d013e48). Requires LLM configuration.
func TestPlaybooks_RunFleetMode(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	var fleetID, fleetSeries string
	for _, pb := range playbooks {
		if mode, _ := pb["execution_mode"].(string); mode == "fleet" {
			fleetID, _ = pb["playbook_id"].(string)
			fleetSeries, _ = pb["series_id"].(string)
			break
		}
	}
	if fleetID == "" {
		t.Skip("no fleet-mode playbook seeded — operational triage playbooks are now agent mode")
	}

	resp, err := client.PlaybookRun(ctx, fleetID, map[string]any{
		"connection_string": cfg.ConnStr,
	})
	if err != nil {
		SkipIfLLMKeyInvalid(t, err.Error())
		if strings.Contains(err.Error(), "infrastructure config") || strings.Contains(err.Error(), "HELPDESK_INFRA_CONFIG") {
			t.Skipf("fleet planner requires infrastructure config (HELPDESK_INFRA_CONFIG) — not configured in this e2e stack: %v", err)
		}
		t.Fatalf("PlaybookRun: %v", err)
	}

	// Fleet response must contain job_def_raw (plan shape), not agent text.
	if resp["job_def_raw"] == nil && resp["job_def"] == nil {
		t.Errorf("fleet-mode run response missing job_def_raw/job_def: %v", resp)
	}
	if resp["text"] != nil {
		t.Error("fleet-mode run should not return agent 'text' field")
	}
	t.Logf("fleet run OK: playbook_id=%s series=%s has_job_def=%v", fleetID, fleetSeries, resp["job_def_raw"] != nil)
}

// TestPlaybooks_RunRecording verifies that the gateway records a playbook run
// in auditd (via recordPlaybookRunStart) before the LLM is invoked, so that
// the run audit trail is captured even when the LLM call fails or is skipped.
//
// This test does NOT require an API key — it finds a playbook, calls /run
// (which may 503 for fleet-mode due to missing infra config), and then verifies
// that the run appears in /runs and /stats. Recording is synchronous and happens
// before routing to the fleet planner or database agent.
func TestPlaybooks_RunRecording(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find any active system playbook to use as the target.
	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	if len(playbooks) == 0 {
		t.Skip("no playbooks available — stack may not be seeded")
	}
	// Prefer a fleet-mode playbook (vacuum triage) since it avoids the agent dependency.
	var pbID, pbSeries string
	for _, pb := range playbooks {
		if sid, _ := pb["series_id"].(string); sid == "pbs_vacuum_triage" {
			pbID, _ = pb["playbook_id"].(string)
			pbSeries = sid
			break
		}
	}
	// Fall back to any playbook if vacuum triage is not found.
	if pbID == "" {
		pbID, _ = playbooks[0]["playbook_id"].(string)
		pbSeries, _ = playbooks[0]["series_id"].(string)
	}
	if pbID == "" {
		t.Skip("no playbook_id available")
	}
	t.Logf("using playbook id=%s series=%s", pbID, pbSeries)

	// Trigger a run. This may fail with 503 (fleet planner not configured) or
	// 500/502 (agent not reachable) — that's OK. What matters is that the run
	// was recorded before the error was returned.
	//
	// We call /run directly (without RequireAPIKey) since recording is
	// synchronous and does not require LLM credentials.
	_, runErr := client.PlaybookRun(ctx, pbID, map[string]any{
		"connection_string": cfg.ConnStr,
	})
	if runErr != nil {
		t.Logf("PlaybookRun returned error (expected for unconfigured stack): %v", runErr)
	}

	// Verify the run was recorded — the /runs endpoint should return at least 1 run.
	runsResp, err := client.PlaybookRuns(ctx, pbID)
	if err != nil {
		t.Fatalf("PlaybookRuns: %v", err)
	}
	count, _ := runsResp["count"].(float64)
	if count == 0 {
		t.Errorf("expected at least 1 run recorded for playbook %s, got count=0", pbID)
	}
	t.Logf("run recording: count=%.0f", count)

	// Verify stats are available via /stats.
	statsResp, err := client.PlaybookStats(ctx, pbID)
	if err != nil {
		t.Fatalf("PlaybookStats: %v", err)
	}
	totalRuns, _ := statsResp["total_runs"].(float64)
	if totalRuns == 0 {
		t.Errorf("expected total_runs > 0 in stats for playbook %s", pbID)
	}
	t.Logf("stats: total_runs=%.0f series_id=%v", totalRuns, statsResp["series_id"])
}

// TestPlaybooks_InlineStatsInList verifies that after at least one run has been
// recorded, GET /fleet/playbooks returns a stats object inline on the relevant
// playbook — no second request to /stats needed.
func TestPlaybooks_InlineStatsInList(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find a fleet-mode playbook to use as the target.
	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	var pbID string
	for _, pb := range playbooks {
		if mode, _ := pb["execution_mode"].(string); mode == "fleet" {
			pbID, _ = pb["playbook_id"].(string)
			break
		}
	}
	if pbID == "" {
		t.Skip("no fleet-mode playbook available")
	}

	// Trigger a run to ensure at least one run is recorded (ignore execution errors).
	client.PlaybookRun(ctx, pbID, map[string]any{"connection_string": cfg.ConnStr}) //nolint:errcheck

	// Re-list playbooks and find the one we just ran.
	playbooks, err = client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList (second): %v", err)
	}
	var found map[string]any
	for _, pb := range playbooks {
		if id, _ := pb["playbook_id"].(string); id == pbID {
			found = pb
			break
		}
	}
	if found == nil {
		t.Fatalf("playbook %s not found in list after run", pbID)
	}

	stats, hasStats := found["stats"]
	if !hasStats || stats == nil {
		t.Fatalf("playbook %s missing inline 'stats' field in list response after a run was recorded", pbID)
	}
	statsMap, ok := stats.(map[string]any)
	if !ok {
		t.Fatalf("stats field is not a JSON object: %T", stats)
	}
	totalRuns, _ := statsMap["total_runs"].(float64)
	if totalRuns == 0 {
		t.Errorf("stats.total_runs = 0 for playbook %s after recording a run", pbID)
	}
	t.Logf("inline stats OK: playbook_id=%s total_runs=%.0f", pbID, totalRuns)
}

// TestPlaybooks_GetRunByID verifies that a run recorded via POST /run can be
// fetched individually via GET /fleet/playbook-runs/{runID}.
func TestPlaybooks_GetRunByID(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find any active playbook.
	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	if len(playbooks) == 0 {
		t.Skip("no playbooks available")
	}
	pbID, _ := playbooks[0]["playbook_id"].(string)
	if pbID == "" {
		t.Skip("no playbook_id available")
	}

	// Trigger a run using a short independent context. POST /run invokes the LLM
	// agent which can take 30+ seconds; the run start is recorded in auditd before
	// the agent call, so we cancel early without consuming the main test budget.
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer triggerCancel()
	client.PlaybookRun(triggerCtx, pbID, map[string]any{"connection_string": cfg.ConnStr}) //nolint:errcheck

	// List runs to get the latest run_id.
	runsResp, err := client.PlaybookRuns(ctx, pbID)
	if err != nil {
		t.Fatalf("PlaybookRuns: %v", err)
	}
	runs, _ := runsResp["runs"].([]any)
	if len(runs) == 0 {
		t.Skip("no runs recorded — stack may not have auditd configured")
	}
	latestRun, _ := runs[0].(map[string]any)
	runID, _ := latestRun["run_id"].(string)
	if runID == "" {
		t.Fatalf("latest run has no run_id: %v", latestRun)
	}
	t.Logf("fetching run_id=%s", runID)

	// Fetch the run by ID via the new GET endpoint.
	run, err := client.PlaybookRunGet(ctx, runID)
	if err != nil {
		t.Fatalf("PlaybookRunGet(%s): %v", runID, err)
	}
	if gotID, _ := run["run_id"].(string); gotID != runID {
		t.Errorf("run_id = %q, want %q", gotID, runID)
	}
	if run["playbook_id"] == nil {
		t.Error("run missing playbook_id field")
	}
	if run["started_at"] == nil {
		t.Error("run missing started_at field")
	}
	t.Logf("GET run OK: run_id=%s playbook_id=%v outcome=%v", runID, run["playbook_id"], run["outcome"])
}

// TestPlaybooks_DBDownPlaybooksHaveAgentFields verifies that the three DB-down
// system playbooks are seeded with the correct execution_mode, entry_point,
// escalates_to, and requires_evidence values. Does not require an API key.
func TestPlaybooks_DBDownPlaybooksHaveAgentFields(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}

	// Build series_id → playbook_id map.
	idBySeries := map[string]string{}
	for _, pb := range playbooks {
		if sid, ok := pb["series_id"].(string); ok {
			if id, ok := pb["playbook_id"].(string); ok {
				idBySeries[sid] = id
			}
		}
	}

	// Helper: fetch a playbook by series ID.
	getBySeriesID := func(t *testing.T, sid string) map[string]any {
		t.Helper()
		pbID, ok := idBySeries[sid]
		if !ok {
			t.Skipf("series %q not found in playbook list — stack may not be seeded", sid)
		}
		pb, err := client.PlaybookGet(ctx, pbID)
		if err != nil {
			t.Fatalf("PlaybookGet(%s): %v", sid, err)
		}
		return pb
	}

	t.Run("restart_triage_is_entry_point_agent", func(t *testing.T) {
		pb := getBySeriesID(t, "pbs_db_restart_triage")
		if mode, _ := pb["execution_mode"].(string); mode != "agent" {
			t.Errorf("execution_mode = %q, want agent", mode)
		}
		if ep, _ := pb["entry_point"].(bool); !ep {
			t.Error("entry_point = false, want true")
		}
		escalates, _ := pb["escalates_to"].([]any)
		if len(escalates) == 0 {
			t.Error("escalates_to is empty, want at least one series ID")
		}
		t.Logf("restart_triage: execution_mode=agent entry_point=true escalates_to=%v", escalates)
	})

	t.Run("config_recovery_is_agent_with_evidence", func(t *testing.T) {
		pb := getBySeriesID(t, "pbs_db_config_recovery")
		if mode, _ := pb["execution_mode"].(string); mode != "agent" {
			t.Errorf("execution_mode = %q, want agent", mode)
		}
		if ep, _ := pb["entry_point"].(bool); ep {
			t.Error("entry_point = true, want false (config recovery is not an entry point)")
		}
		// config recovery uses transitions_to (same-domain follow-on to pitr_recovery),
		// not escalates_to (cross-domain handoff). Directive split added in v0.16.
		transitions, _ := pb["transitions_to"].([]any)
		if len(transitions) == 0 {
			t.Error("transitions_to is empty, want at least one series ID")
		}
		evidence, _ := pb["requires_evidence"].([]any)
		if len(evidence) == 0 {
			t.Error("requires_evidence is empty, want at least one pattern")
		}
		t.Logf("config_recovery: transitions_to=%v requires_evidence=%v", transitions, evidence)
	})

	t.Run("pitr_recovery_is_agent_with_evidence", func(t *testing.T) {
		pb := getBySeriesID(t, "pbs_db_pitr_recovery")
		if mode, _ := pb["execution_mode"].(string); mode != "agent" {
			t.Errorf("execution_mode = %q, want agent", mode)
		}
		evidence, _ := pb["requires_evidence"].([]any)
		if len(evidence) == 0 {
			t.Error("requires_evidence is empty, want at least one pattern")
		}
		t.Logf("pitr_recovery: requires_evidence=%v", evidence)
	})

	t.Run("sysadmin_docker_inspect_is_seeded", func(t *testing.T) {
		pb := getBySeriesID(t, "pbs_sysadmin_docker_inspect")
		if mode, _ := pb["execution_mode"].(string); mode != "agent" {
			t.Errorf("execution_mode = %q, want agent", mode)
		}
		if ep, _ := pb["entry_point"].(bool); ep {
			t.Error("entry_point = true, want false (docker inspect is not an entry point)")
		}
		if am, _ := pb["approval_mode"].(string); am != "manual" {
			t.Errorf("approval_mode = %q, want manual", am)
		}
		t.Logf("sysadmin_docker_inspect: execution_mode=agent entry_point=false approval_mode=manual")
	})

	t.Run("restart_triage_escalates_to_sysadmin_docker_inspect", func(t *testing.T) {
		pb := getBySeriesID(t, "pbs_db_restart_triage")
		escalates, _ := pb["escalates_to"].([]any)
		found := false
		for _, e := range escalates {
			if s, _ := e.(string); s == "pbs_sysadmin_docker_inspect" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("pbs_db_restart_triage.escalates_to does not include pbs_sysadmin_docker_inspect; got %v", escalates)
		}
	})

	t.Run("lock_chain_remediate_is_agent_approve", func(t *testing.T) {
		pb := getBySeriesID(t, "pbs_lock_chain_remediate")
		if mode, _ := pb["execution_mode"].(string); mode != "agent_approve" {
			t.Errorf("execution_mode = %q, want agent_approve", mode)
		}
		if am, _ := pb["approval_mode"].(string); am != "manual" {
			t.Errorf("approval_mode = %q, want manual", am)
		}
		if sys, _ := pb["is_system"].(bool); !sys {
			t.Error("is_system = false, want true for seeded system playbook")
		}
		if ep, _ := pb["entry_point"].(bool); ep {
			t.Error("entry_point = true; remediation playbook should not be an entry point")
		}
		t.Logf("lock_chain_remediate: execution_mode=agent_approve approval_mode=manual")
	})

	t.Run("operational_playbooks_are_agent", func(t *testing.T) {
		// Converted from fleet to agent in commit d013e48 — the triage flow now
		// runs directly through the DB agent instead of the fleet planner.
		for _, sid := range []string{
			"pbs_vacuum_triage",
			"pbs_slow_query_triage",
			"pbs_connection_triage",
			"pbs_replication_lag",
		} {
			pbID, ok := idBySeries[sid]
			if !ok {
				t.Logf("series %q not found (skipping)", sid)
				continue
			}
			pb, err := client.PlaybookGet(ctx, pbID)
			if err != nil {
				t.Errorf("PlaybookGet(%s): %v", sid, err)
				continue
			}
			if mode, _ := pb["execution_mode"].(string); mode != "agent" {
				t.Errorf("%s: execution_mode = %q, want agent", sid, mode)
			}
		}
	})
}

// TestPlaybooks_RunAgentMode calls POST /run on an agent-mode system playbook
// (Database Down — Restart Triage) and verifies the response has the agent shape.
// Requires LLM + a running database agent.
func TestPlaybooks_RunAgentMode(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	var restartID string
	for _, pb := range playbooks {
		if sid, _ := pb["series_id"].(string); sid == "pbs_db_restart_triage" {
			restartID, _ = pb["playbook_id"].(string)
			break
		}
	}
	if restartID == "" {
		t.Skip("pbs_db_restart_triage system playbook not found")
	}

	resp, err := client.PlaybookRun(ctx, restartID, map[string]any{
		"connection_string": cfg.ConnStr,
		"context":           "e2e test: checking agent-mode routing",
	})
	if err != nil {
		SkipIfLLMKeyInvalid(t, err.Error())
		t.Fatalf("PlaybookRun (agent mode): %v", err)
	}

	// Agent response must contain text, not job_def_raw.
	if resp["text"] == nil {
		t.Errorf("agent-mode run response missing 'text' field: %v", resp)
	}
	if resp["job_def_raw"] != nil {
		t.Error("agent-mode run should not return fleet 'job_def_raw' field")
	}
	t.Logf("agent run OK: playbook_id=%s text_len=%d", restartID, len(fmt.Sprintf("%v", resp["text"])))
}

// TestPlaybooks_RunAgentApproveMode calls POST /run on the pbs_lock_chain_remediate
// system playbook (execution_mode=agent_approve) and verifies the gateway returns
// HTTP 202 with status="pending_approval" and a non-nil step proposal. The LLM is
// called to propose the first remediation step; no database agent or live database
// connection is required beyond the LLM API key.
func TestPlaybooks_RunAgentApproveMode(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	var remedID string
	for _, pb := range playbooks {
		if sid, _ := pb["series_id"].(string); sid == "pbs_lock_chain_remediate" {
			remedID, _ = pb["playbook_id"].(string)
			break
		}
	}
	if remedID == "" {
		t.Skip("pbs_lock_chain_remediate system playbook not found — stack may not be seeded")
	}

	resp, err := client.PlaybookRun(ctx, remedID, map[string]any{
		"connection_string": cfg.ConnStr,
	})
	if err != nil {
		SkipIfLLMKeyInvalid(t, err.Error())
		t.Fatalf("PlaybookRun (agent_approve mode): %v", err)
	}

	// agent_approve response: status must be "pending_approval", not agent text or fleet plan.
	if resp["status"] != "pending_approval" {
		t.Errorf("status = %q, want pending_approval; full response: %v", resp["status"], resp)
	}
	if resp["run_id"] == nil || resp["run_id"] == "" {
		t.Error("run_id should be set in agent_approve response")
	}
	step, _ := resp["step"].(map[string]any)
	if step == nil {
		t.Fatal("step field should be present in pending_approval response")
	}
	if step["tool"] == nil || step["tool"] == "" {
		t.Error("step.tool should be non-empty — LLM should propose a concrete tool call")
	}
	if resp["text"] != nil {
		t.Error("agent_approve run should not return agent 'text' field")
	}
	if resp["job_def_raw"] != nil {
		t.Error("agent_approve run should not return fleet 'job_def_raw' field")
	}
	t.Logf("agent_approve run OK: playbook_id=%s run_id=%v step.tool=%v approval_id=%v",
		remedID, resp["run_id"], step["tool"], resp["approval_id"])
}

// TestPlaybooks_GateEscalation verifies the informed gate end-to-end:
//   - gate_escalation:true on a triage run intercepts ESCALATE_TO and returns
//     status="pending_gate" with escalation_target and run_id populated
//   - the run record outcome is "gate_pending" in auditd
//   - proceed-escalation with resolution="denied" returns status="denied" and
//     transitions the run to outcome="abandoned"
//
// If the triage agent completes without emitting ESCALATE_TO (non-deterministic
// LLM), the test logs the situation and passes — the absence of escalation is a
// valid outcome and the unit tests already cover the gate intercept logic.
func TestPlaybooks_GateEscalation(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Find a triage playbook that escalates — pbs_db_restart_triage is designed to
	// diagnose and emit ESCALATE_TO when it can't recover the database itself.
	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	var triageID string
	for _, pb := range playbooks {
		if sid, _ := pb["series_id"].(string); sid == "pbs_db_restart_triage" {
			triageID, _ = pb["playbook_id"].(string)
			break
		}
	}
	if triageID == "" {
		t.Skip("pbs_db_restart_triage system playbook not found")
	}

	resp, err := client.PlaybookRun(ctx, triageID, map[string]any{
		"connection_string": cfg.ConnStr,
		"context":           "e2e test: gate_escalation flow",
		"gate_escalation":   true,
	})
	if err != nil {
		SkipIfLLMKeyInvalid(t, err.Error())
		t.Fatalf("PlaybookRun with gate_escalation: %v", err)
	}

	status, _ := resp["status"].(string)

	if status != "pending_gate" {
		// The agent completed without escalating (e.g. it resolved the issue or
		// timed out). This is a valid outcome — gate_escalation is a no-op when
		// ESCALATE_TO is not emitted. Log and pass; the intercept logic is covered
		// by unit tests.
		t.Logf("gate not triggered (agent did not escalate): status=%q — gate_escalation flag did not break the run", status)
		return
	}

	// --- gate fired: verify pending_gate response shape ---

	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatal("pending_gate response missing run_id")
	}
	escalationTarget, _ := resp["escalation_target"].(string)
	if escalationTarget == "" {
		t.Error("pending_gate response missing escalation_target")
	}
	t.Logf("gate pending: run_id=%s escalation_target=%s confidence_warning=%v",
		runID, escalationTarget, resp["confidence_warning"])

	// Verify the run record is stored with outcome=gate_pending.
	runRecord, err := client.PlaybookRunGet(ctx, runID)
	if err != nil {
		t.Fatalf("PlaybookRunGet: %v", err)
	}
	if outcome, _ := runRecord["outcome"].(string); outcome != "gate_pending" {
		t.Errorf("run record outcome = %q, want gate_pending", outcome)
	}

	// Deny the gate and verify the response and final run state.
	denyResp, err := client.ProceedEscalation(ctx, runID, map[string]any{
		"resolution":  "denied",
		"resolved_by": "e2e-test",
	})
	if err != nil {
		t.Fatalf("ProceedEscalation (denied): %v", err)
	}
	if denyResp["status"] != "denied" {
		t.Errorf("proceed-escalation status = %q, want denied", denyResp["status"])
	}

	// Run should now be abandoned.
	runRecord, err = client.PlaybookRunGet(ctx, runID)
	if err != nil {
		t.Fatalf("PlaybookRunGet after deny: %v", err)
	}
	if outcome, _ := runRecord["outcome"].(string); outcome != "abandoned" {
		t.Errorf("run record outcome after deny = %q, want abandoned", outcome)
	}

	t.Logf("gate_escalation e2e OK: run_id=%s escalation_target=%s denied→abandoned",
		runID, escalationTarget)
}

// =============================================================================
// Feedback, events, accuracy, and incident narrative — no API key required
// =============================================================================

// TestPlaybooks_FeedbackRoundtrip verifies the operator feedback endpoints work
// end-to-end via the gateway. No LLM is needed — the feedback store accepts any
// run_id when series_id is provided in the body.
func TestPlaybooks_FeedbackRoundtrip(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	runID := fmt.Sprintf("plr_e2e_fb_%d", time.Now().UnixNano())

	// Submit initial feedback (verdict_correct=true).
	fb, err := client.SubmitFeedback(ctx, runID, map[string]any{
		"series_id":       "pbs_lock_chain_triage",
		"feedback_type":   "triage",
		"feedback_time":   "post_incident",
		"verdict_correct": true,
		"verdict_notes":   "PID 867 held ShareLock on tx 9823",
		"operator":        "e2e-test",
	})
	if err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}
	if fb["run_id"] != runID {
		t.Errorf("submit response run_id = %q, want %q", fb["run_id"], runID)
	}
	if fb["series_id"] != "pbs_lock_chain_triage" {
		t.Errorf("submit response series_id = %q", fb["series_id"])
	}

	// GET feedback and verify fields round-trip.
	got, err := client.GetFeedback(ctx, runID)
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	if got["run_id"] != runID {
		t.Errorf("feedback run_id = %q, want %q", got["run_id"], runID)
	}
	if got["series_id"] != "pbs_lock_chain_triage" {
		t.Errorf("feedback series_id = %q", got["series_id"])
	}
	if dc, _ := got["verdict_correct"].(bool); !dc {
		t.Errorf("feedback verdict_correct = %v, want true", got["verdict_correct"])
	}
	if got["verdict_notes"] != "PID 867 held ShareLock on tx 9823" {
		t.Errorf("feedback verdict_notes = %q", got["verdict_notes"])
	}

	// Upsert: re-submit the same (run_id, feedback_type, feedback_time) with corrected values.
	_, err = client.SubmitFeedback(ctx, runID, map[string]any{
		"series_id":       "pbs_lock_chain_triage",
		"feedback_type":   "triage",
		"feedback_time":   "post_incident",
		"verdict_correct": false,
		"verdict_notes":   "wrong hypothesis — actual blocker was autovacuum",
		"operator":        "e2e-test",
	})
	if err != nil {
		t.Fatalf("SubmitFeedback (upsert): %v", err)
	}

	got2, err := client.GetFeedback(ctx, runID)
	if err != nil {
		t.Fatalf("GetFeedback after upsert: %v", err)
	}
	if dc, _ := got2["verdict_correct"].(bool); dc {
		t.Errorf("after upsert verdict_correct = true, want false")
	}
	if got2["verdict_notes"] != "wrong hypothesis — actual blocker was autovacuum" {
		t.Errorf("after upsert verdict_notes = %q", got2["verdict_notes"])
	}

	t.Logf("feedback roundtrip OK: run_id=%s", runID)
}

// TestPlaybooks_FeedbackByType verifies that GET /feedback?feedback_type=&feedback_time=
// returns the correct row for each combination, and that at_gate and post_incident
// feedback for the same run_id are stored independently (no collision).
func TestPlaybooks_FeedbackByType(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	runID := fmt.Sprintf("plr_e2e_fbtype_%d", time.Now().UnixNano())

	// Submit (triage, at_gate) — mirroring what the gateway does at proceed-escalation time.
	_, err := client.SubmitFeedback(ctx, runID, map[string]any{
		"series_id":       "pbs_lock_chain_triage",
		"feedback_type":   "triage",
		"feedback_time":   "at_gate",
		"verdict_correct": true,
		"verdict_notes":   "diagnosis looked right at gate",
		"operator":        "e2e-test",
	})
	if err != nil {
		t.Fatalf("SubmitFeedback (at_gate): %v", err)
	}

	// Submit (triage, post_incident) for the same run.
	_, err = client.SubmitFeedback(ctx, runID, map[string]any{
		"series_id":       "pbs_lock_chain_triage",
		"feedback_type":   "triage",
		"feedback_time":   "post_incident",
		"verdict_correct": false,
		"verdict_notes":   "autovacuum was the real cause",
		"operator":        "e2e-test",
	})
	if err != nil {
		t.Fatalf("SubmitFeedback (post_incident): %v", err)
	}

	// Default GET (no params) → returns post_incident row.
	defaultFB, err := client.GetFeedback(ctx, runID)
	if err != nil {
		t.Fatalf("GetFeedback (default): %v", err)
	}
	if defaultFB["feedback_time"] != "post_incident" {
		t.Errorf("default GET: feedback_time = %q, want post_incident", defaultFB["feedback_time"])
	}
	if dc, _ := defaultFB["verdict_correct"].(bool); dc {
		t.Errorf("default GET: verdict_correct = true, want false")
	}
	if defaultFB["verdict_notes"] != "autovacuum was the real cause" {
		t.Errorf("default GET: verdict_notes = %q", defaultFB["verdict_notes"])
	}

	// Explicit at_gate GET → returns at_gate row.
	atGateFB, err := client.GetFeedbackByType(ctx, runID, "triage", "at_gate")
	if err != nil {
		t.Fatalf("GetFeedbackByType (at_gate): %v", err)
	}
	if atGateFB["feedback_time"] != "at_gate" {
		t.Errorf("at_gate GET: feedback_time = %q, want at_gate", atGateFB["feedback_time"])
	}
	if dc, _ := atGateFB["verdict_correct"].(bool); !dc {
		t.Errorf("at_gate GET: verdict_correct = false, want true")
	}
	if atGateFB["verdict_notes"] != "diagnosis looked right at gate" {
		t.Errorf("at_gate GET: verdict_notes = %q", atGateFB["verdict_notes"])
	}

	// Explicit post_incident GET → same as default.
	postFB, err := client.GetFeedbackByType(ctx, runID, "triage", "post_incident")
	if err != nil {
		t.Fatalf("GetFeedbackByType (post_incident): %v", err)
	}
	if postFB["feedback_time"] != "post_incident" {
		t.Errorf("explicit post_incident GET: feedback_time = %q", postFB["feedback_time"])
	}

	// Non-existent combination → 404.
	_, err = client.GetFeedbackByType(ctx, runID, "remediation", "post_incident")
	if err == nil {
		t.Error("expected 404 for remediation/post_incident, got nil error")
	} else if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404, got: %v", err)
	}

	t.Logf("feedback by-type OK: run_id=%s", runID)
}

// TestPlaybooks_FeedbackNotFound verifies that GET feedback for a run with no
// feedback returns HTTP 404 via the gateway.
func TestPlaybooks_FeedbackNotFound(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.GetFeedback(ctx, "plr_e2e_no_feedback_run")
	if err == nil {
		t.Fatal("expected error for run with no feedback, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected HTTP 404 error, got: %v", err)
	}
}

// TestPlaybooks_RequestFeedback_DecisionHubFlow verifies the full emit-and-wait
// feedback path end-to-end: request-feedback creates a placeholder that appears
// as a pending "feedback" decision in the hub, and resolving it via
// POST /decisions/feedback:{runID}/resolve persists verdict_correct.
// No LLM is needed — request-feedback works against any run_id.
func TestPlaybooks_RequestFeedback_DecisionHubFlow(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	runID := fmt.Sprintf("plr_e2e_hub_%d", time.Now().UnixNano())

	// Step 1: request-feedback creates a placeholder and returns resolve_url.
	rfResp, err := client.RequestFeedback(ctx, runID)
	if err != nil {
		t.Fatalf("RequestFeedback: %v", err)
	}
	resolveURL, _ := rfResp["resolve_url"].(string)
	if resolveURL == "" {
		t.Fatalf("RequestFeedback returned no resolve_url; got %v", rfResp)
	}
	expectedResolveURL := cfg.GatewayURL + "/api/v1/decisions/feedback:" + runID + "/resolve"
	// Normalize trailing slash for comparison.
	if strings.TrimSuffix(resolveURL, "/") != strings.TrimSuffix(expectedResolveURL, "/") {
		t.Errorf("resolve_url = %q, want %q", resolveURL, expectedResolveURL)
	}

	// Step 2: the run should appear in GET /api/v1/decisions?type=feedback.
	decisions, err := client.GetDecisions(ctx, map[string]string{"type": "feedback"})
	if err != nil {
		t.Fatalf("GetDecisions: %v", err)
	}
	items, _ := decisions["decisions"].([]any)
	var found bool
	for _, item := range items {
		d, _ := item.(map[string]any)
		if d["id"] == "feedback:"+runID {
			found = true
			if d["status"] != "pending" {
				t.Errorf("decision status = %q, want pending", d["status"])
			}
			break
		}
	}
	if !found {
		t.Errorf("feedback decision for run_id=%s not found in decisions list (got %d items)", runID, len(items))
	}

	// Step 3: GET /api/v1/decisions/feedback:{runID} returns pending state.
	d, err := client.GetDecision(ctx, "feedback:"+runID)
	if err != nil {
		t.Fatalf("GetDecision: %v", err)
	}
	if d["status"] != "pending" {
		t.Errorf("decision status = %q, want pending", d["status"])
	}

	// Step 4: resolve via the hub — approved maps to verdict_correct=true.
	resolved, err := client.ResolveDecision(ctx, "feedback:"+runID, map[string]any{
		"resolution":  "approved",
		"resolved_by": "e2e-test",
		"reason":      "PID 236 confirmed idle-in-transaction — diagnosis correct",
	})
	if err != nil {
		t.Fatalf("ResolveDecision: %v", err)
	}
	if resolved["status"] != "resolved" {
		t.Errorf("resolve response status = %q, want resolved", resolved["status"])
	}
	if resolved["verdict_correct"] != true {
		t.Errorf("resolve response verdict_correct = %v, want true", resolved["verdict_correct"])
	}

	// Step 5: feedback is persisted — GET .../feedback returns verdict_correct=true.
	fb, err := client.GetFeedback(ctx, runID)
	if err != nil {
		t.Fatalf("GetFeedback after resolve: %v", err)
	}
	if dc, _ := fb["verdict_correct"].(bool); !dc {
		t.Errorf("feedback verdict_correct = %v, want true", fb["verdict_correct"])
	}
	if fb["verdict_notes"] != "PID 236 confirmed idle-in-transaction — diagnosis correct" {
		t.Errorf("verdict_notes = %q", fb["verdict_notes"])
	}

	t.Logf("feedback hub flow OK: run_id=%s resolve_url=%s", runID, resolveURL)
}

// TestPlaybooks_RunEventsEndpoint verifies the run events proxy route is
// registered and returns 404 for a nonexistent run (not 500 or route-not-found).
func TestPlaybooks_RunEventsEndpoint(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.GetRunEvents(ctx, "plr_e2e_no_events_run")
	if err == nil {
		t.Fatal("expected 404 for nonexistent run, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected HTTP 404 error, got: %v", err)
	}
}

// TestPlaybooks_IncidentNarrative_NotFound verifies the incident narrative
// endpoint is routed and returns 404 for an unknown run_id.
func TestPlaybooks_IncidentNarrative_NotFound(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.GetIncident(ctx, "plr_e2e_no_incident")
	if err == nil {
		t.Fatal("expected 404 for nonexistent incident, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected HTTP 404 error, got: %v", err)
	}
}

// TestPlaybooks_StatsIncludeAccuracy verifies that operator feedback submitted
// for a playbook series appears in the stats response. No LLM is required —
// feedback records are created directly without corresponding run records.
func TestPlaybooks_StatsIncludeAccuracy(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create a throwaway playbook so we get a unique series_id.
	uniqueName := fmt.Sprintf("e2e-accuracy-test-%d", time.Now().UnixNano())
	created, err := client.PlaybookCreate(ctx, map[string]any{
		"name":        uniqueName,
		"description": "e2e accuracy stats test — safe to delete",
	})
	if err != nil {
		t.Fatalf("PlaybookCreate: %v", err)
	}
	pbID, _ := created["playbook_id"].(string)
	seriesID, _ := created["series_id"].(string)
	if pbID == "" || seriesID == "" {
		t.Fatalf("create response missing playbook_id or series_id: %v", created)
	}
	t.Cleanup(func() {
		client.PlaybookDelete(context.Background(), pbID) //nolint:errcheck
	})

	// Submit 3 feedback records: 2 correct, 1 incorrect.
	feedbacks := []struct {
		runID   string
		correct bool
	}{
		{fmt.Sprintf("plr_acc_a_%d", time.Now().UnixNano()), true},
		{fmt.Sprintf("plr_acc_b_%d", time.Now().UnixNano()+1), true},
		{fmt.Sprintf("plr_acc_c_%d", time.Now().UnixNano()+2), false},
	}
	for _, f := range feedbacks {
		_, err := client.SubmitFeedback(ctx, f.runID, map[string]any{
			"series_id":       seriesID,
			"feedback_type":   "triage",
			"feedback_time":   "post_incident",
			"verdict_correct": f.correct,
			"operator":        "e2e-test",
		})
		if err != nil {
			t.Fatalf("SubmitFeedback %s: %v", f.runID, err)
		}
	}

	// GET stats and verify accuracy fields are present and correct.
	stats, err := client.PlaybookStats(ctx, pbID)
	if err != nil {
		t.Fatalf("PlaybookStats: %v", err)
	}

	fbCount, _ := stats["feedback_count"].(float64)
	if int(fbCount) != 3 {
		t.Errorf("feedback_count = %v, want 3", stats["feedback_count"])
	}
	correctCount, _ := stats["correct_count"].(float64)
	if int(correctCount) != 2 {
		t.Errorf("correct_count = %v, want 2", stats["correct_count"])
	}
	accuracyRate, _ := stats["accuracy_rate"].(float64)
	want := 2.0 / 3.0
	if diff := accuracyRate - want; diff < -0.001 || diff > 0.001 {
		t.Errorf("accuracy_rate = %.4f, want %.4f", accuracyRate, want)
	}

	t.Logf("stats accuracy OK: playbook=%s series=%s feedback=%d correct=%d rate=%.2f",
		pbID, seriesID, int(fbCount), int(correctCount), accuracyRate)
}

// =============================================================================
// Gate reason roundtrip (API key required — LLM dependent)
// =============================================================================

// TestPlaybooks_GateWithReason verifies the gate reason field flows through the
// full stack: ProceedEscalation with reason → resolved_reason in GET /decisions.
//
// If the triage agent completes without escalating (non-deterministic LLM), the
// test logs the situation and passes — gate intercept logic is covered by unit
// tests.
func TestPlaybooks_GateWithReason(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	var triageID string
	for _, pb := range playbooks {
		if sid, _ := pb["series_id"].(string); sid == "pbs_db_restart_triage" {
			triageID, _ = pb["playbook_id"].(string)
			break
		}
	}
	if triageID == "" {
		t.Skip("pbs_db_restart_triage system playbook not found")
	}

	resp, err := client.PlaybookRun(ctx, triageID, map[string]any{
		"connection_string": cfg.ConnStr,
		"context":           "e2e test: gate_with_reason flow",
		"gate_escalation":   true,
	})
	if err != nil {
		SkipIfLLMKeyInvalid(t, err.Error())
		t.Fatalf("PlaybookRun with gate_escalation: %v", err)
	}

	status, _ := resp["status"].(string)
	if status != "pending_gate" {
		t.Logf("gate not triggered (agent did not escalate): status=%q — gate_reason field covered by unit tests", status)
		return
	}

	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatal("pending_gate response missing run_id")
	}

	// Deny with a reason (avoids triggering remediation playbook).
	const testReason = "e2e-test: denied with gate reason verification"
	denyResp, err := client.ProceedEscalation(ctx, runID, map[string]any{
		"resolution":  "denied",
		"resolved_by": "e2e-test",
		"reason":      testReason,
	})
	if err != nil {
		t.Fatalf("ProceedEscalation (denied with reason): %v", err)
	}
	if denyResp["status"] != "denied" {
		t.Errorf("proceed-escalation status = %q, want denied", denyResp["status"])
	}

	// GET decision and verify resolved_reason is present.
	decision, err := client.GetDecision(ctx, "gate:"+runID)
	if err != nil {
		t.Fatalf("GetDecision: %v", err)
	}
	extra, _ := decision["extra"].(map[string]any)
	if extra == nil {
		t.Fatal("decision response missing extra field")
	}
	resolvedReason, _ := extra["resolved_reason"].(string)
	if resolvedReason != testReason {
		t.Errorf("resolved_reason = %q, want %q", resolvedReason, testReason)
	}

	t.Logf("gate reason e2e OK: run_id=%s resolved_reason=%q", runID, resolvedReason)
}

// TestPlaybooks_IncidentNarrative_Full verifies the full incident lifecycle:
// triage → gate approve with reason → remediation run recorded → feedback submitted →
// GET /incidents/{id} returns all chapters.
//
// Requires LLM (RequireAPIKey). If the triage agent doesn't escalate (non-deterministic),
// the test logs the situation and passes — gate logic is covered by unit tests.
func TestPlaybooks_IncidentNarrative_Full(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Find pbs_db_restart_triage — it's designed to emit ESCALATE_TO.
	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	var triageID string
	for _, pb := range playbooks {
		if sid, _ := pb["series_id"].(string); sid == "pbs_db_restart_triage" {
			triageID, _ = pb["playbook_id"].(string)
			break
		}
	}
	if triageID == "" {
		t.Skip("pbs_db_restart_triage system playbook not found")
	}

	resp, err := client.PlaybookRun(ctx, triageID, map[string]any{
		"connection_string": cfg.ConnStr,
		"context":           "e2e test: incident_narrative_full",
		"gate_escalation":   true,
	})
	if err != nil {
		SkipIfLLMKeyInvalid(t, err.Error())
		t.Fatalf("PlaybookRun: %v", err)
	}

	status, _ := resp["status"].(string)
	if status != "pending_gate" {
		t.Logf("gate not triggered (agent did not escalate): status=%q — narrative chapters covered by unit tests", status)
		return
	}

	triageRunID, _ := resp["run_id"].(string)
	if triageRunID == "" {
		t.Fatal("pending_gate response missing run_id")
	}
	t.Logf("gate pending: triage_run_id=%s", triageRunID)

	// Approve the gate with a reason, which chains to the remediation playbook.
	const gateReason = "e2e test: approved for incident narrative verification"
	approveResp, err := client.ProceedEscalation(ctx, triageRunID, map[string]any{
		"resolution":  "approved",
		"resolved_by": "e2e-test",
		"reason":      gateReason,
	})
	if err != nil {
		t.Fatalf("ProceedEscalation (approved): %v", err)
	}
	t.Logf("gate approved: proceed response status=%v", approveResp["status"])

	// Submit feedback on the triage run.
	_, err = client.SubmitFeedback(ctx, triageRunID, map[string]any{
		"series_id":       "pbs_db_restart_triage",
		"feedback_type":   "triage",
		"feedback_time":   "post_incident",
		"verdict_correct": true,
		"verdict_notes":   "e2e test confirmed",
		"operator":        "e2e-test",
	})
	if err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}

	// Poll for the incident narrative to have all expected chapters.
	// The remediation run is recorded asynchronously via recordPlaybookRunComplete,
	// so a brief wait may be needed.
	var narrative map[string]any
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		narrative, err = client.GetIncident(ctx, triageRunID)
		if err != nil {
			t.Fatalf("GetIncident: %v", err)
		}
		// Wait until remediation chapter appears.
		if narrative["remediation"] != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// ── triage chapter ────────────────────────────────────────────────────
	triage, _ := narrative["triage"].(map[string]any)
	if triage == nil {
		t.Fatal("incident narrative missing triage chapter")
	}
	if triage["run_id"] != triageRunID {
		t.Errorf("triage.run_id = %q, want %q", triage["run_id"], triageRunID)
	}
	if triage["playbook"] == "" {
		t.Error("triage.playbook is empty")
	}

	// ── gate chapter ──────────────────────────────────────────────────────
	gate, _ := narrative["gate"].(map[string]any)
	if gate == nil {
		t.Fatal("incident narrative missing gate chapter")
	}
	if gate["resolution"] != "approved" {
		t.Errorf("gate.resolution = %q, want approved", gate["resolution"])
	}
	if gate["reason"] != gateReason {
		t.Errorf("gate.reason = %q, want %q", gate["reason"], gateReason)
	}

	// ── remediation chapter ───────────────────────────────────────────────
	remediation, _ := narrative["remediation"].(map[string]any)
	if remediation == nil {
		t.Log("remediation chapter not present (may not have chained in time) — gate + triage verified")
	} else {
		if remediation["run_id"] == "" {
			t.Error("remediation.run_id is empty")
		}
		if remediation["playbook"] == "" {
			t.Error("remediation.playbook is empty")
		}
		t.Logf("remediation chapter: run_id=%s playbook=%s outcome=%s",
			remediation["run_id"], remediation["playbook"], remediation["outcome"])
	}

	// ── feedback chapter ──────────────────────────────────────────────────
	// Feedback is a []RunFeedback slice since v0.18.
	feedbackSlice, _ := narrative["feedback"].([]any)
	if len(feedbackSlice) == 0 {
		t.Fatal("incident narrative missing feedback chapter (empty slice)")
	}
	// Find the triage/post_incident record we submitted.
	var feedbackCorrect *bool
	for _, item := range feedbackSlice {
		fb, _ := item.(map[string]any)
		if fb == nil {
			continue
		}
		if fb["feedback_type"] == "triage" && fb["feedback_time"] == "post_incident" {
			if dc, ok := fb["verdict_correct"].(bool); ok {
				feedbackCorrect = &dc
			}
			break
		}
	}
	if feedbackCorrect == nil {
		t.Error("triage/post_incident feedback record not found in narrative")
	} else if !*feedbackCorrect {
		t.Errorf("feedback.verdict_correct = false, want true")
	}

	t.Logf("incident narrative full e2e OK: incident_id=%s triage=%s gate_reason=%q",
		narrative["incident_id"], triageRunID, gate["reason"])
}

// TestPlaybooks_EvaluationRoundtrip verifies that POST .../evaluation stores scores
// and GET .../evaluation retrieves them with all fields intact.
func TestPlaybooks_EvaluationRoundtrip(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	runID := fmt.Sprintf("plr_e2e_ev_%d", time.Now().UnixNano())

	status, err := client.SubmitEvaluation(ctx, runID, map[string]any{
		"failure_id":       "db-tx-lock-chain-blocker",
		"failure_name":     "Transaction lock chain blocker",
		"keyword_score":    1.0,
		"tool_score":       0.8,
		"diagnosis_score":  0.9,
		"remediation_score": 0.0,
		"overall_score":    0.85,
		"judge_used":       true,
		"passed":           true,
	})
	if err != nil {
		t.Fatalf("SubmitEvaluation: %v", err)
	}
	if status != 204 {
		t.Fatalf("SubmitEvaluation status = %d, want 204", status)
	}

	got, err := client.GetEvaluation(ctx, runID)
	if err != nil {
		t.Fatalf("GetEvaluation: %v", err)
	}
	if got["run_id"] != runID {
		t.Errorf("run_id = %q, want %q", got["run_id"], runID)
	}
	if got["failure_id"] != "db-tx-lock-chain-blocker" {
		t.Errorf("failure_id = %q", got["failure_id"])
	}
	if ks, _ := got["keyword_score"].(float64); ks != 1.0 {
		t.Errorf("keyword_score = %v, want 1.0", got["keyword_score"])
	}
	if os, _ := got["overall_score"].(float64); os != 0.85 {
		t.Errorf("overall_score = %v, want 0.85", got["overall_score"])
	}
	if ju, _ := got["judge_used"].(bool); !ju {
		t.Errorf("judge_used = %v, want true", got["judge_used"])
	}
	if p, _ := got["passed"].(bool); !p {
		t.Errorf("passed = %v, want true", got["passed"])
	}

	// Upsert: re-submit should overwrite.
	status2, err := client.SubmitEvaluation(ctx, runID, map[string]any{
		"failure_id":    "db-tx-lock-chain-blocker",
		"overall_score": 0.95,
		"passed":        true,
	})
	if err != nil || status2 != 204 {
		t.Fatalf("SubmitEvaluation (upsert) status=%d err=%v", status2, err)
	}
	got2, err := client.GetEvaluation(ctx, runID)
	if err != nil {
		t.Fatalf("GetEvaluation after upsert: %v", err)
	}
	if os2, _ := got2["overall_score"].(float64); os2 != 0.95 {
		t.Errorf("after upsert overall_score = %v, want 0.95", got2["overall_score"])
	}

	// Not-found: separate run_id should return 404.
	_, err = client.GetEvaluation(ctx, "plr_nonexistent_ev")
	if err == nil {
		t.Error("GetEvaluation for nonexistent run should return error (404)")
	}

	t.Logf("evaluation roundtrip OK: run_id=%s", runID)
}
