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

// TestPlaybooks_RunFleetMode calls POST /run on a fleet-mode system playbook
// (Vacuum & Bloat Triage) and verifies the response has the fleet plan shape.
// Requires LLM configuration — skipped if no API key present.
func TestPlaybooks_RunFleetMode(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Find the vacuum triage system playbook (fleet mode, entry_point=false).
	playbooks, err := client.PlaybookList(ctx, "")
	if err != nil {
		t.Fatalf("PlaybookList: %v", err)
	}
	var vacuumID string
	for _, pb := range playbooks {
		if sid, _ := pb["series_id"].(string); sid == "pbs_vacuum_triage" {
			vacuumID, _ = pb["playbook_id"].(string)
			break
		}
	}
	if vacuumID == "" {
		t.Skip("pbs_vacuum_triage system playbook not found — stack may not be seeded")
	}

	resp, err := client.PlaybookRun(ctx, vacuumID, map[string]any{
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
	t.Logf("fleet run OK: playbook_id=%s has_job_def=%v", vacuumID, resp["job_def_raw"] != nil)
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

	// Trigger a run (ignore execution errors — recording is best-effort synchronous).
	client.PlaybookRun(ctx, pbID, map[string]any{"connection_string": cfg.ConnStr}) //nolint:errcheck

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
