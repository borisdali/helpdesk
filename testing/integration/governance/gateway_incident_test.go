//go:build integration

package governance

import (
	"fmt"
	"testing"
	"time"
)

// recordRun seeds a PlaybookRun directly in the real auditd instance via
// POST /v1/fleet/playbooks/{playbookID}/runs, and returns its run_id.
// playbookID is a throwaway value — supplying series_id and execution_mode
// explicitly in the body means handleRecord never needs to resolve a real
// playbook catalog entry (cmd/auditd/playbook_run_handlers.go:36-52).
func recordRun(t *testing.T, seriesID string, body map[string]any) string {
	t.Helper()
	playbookID := "pb_" + seriesID
	full := map[string]any{
		"series_id":      seriesID,
		"execution_mode": "agent",
	}
	for k, v := range body {
		full[k] = v
	}
	result := post(t, auditdAddr, "/v1/fleet/playbooks/"+playbookID+"/runs", full)
	runID, _ := result["run_id"].(string)
	if runID == "" {
		t.Fatalf("recordRun(%s): expected run_id in response, got %v", seriesID, result)
	}
	return runID
}

// getIncidentFromGateway fetches the incident narrative through the real
// gateway HTTP API (not a mock), exercising the actual
// GET /api/v1/incidents/{runID} → auditd round trip.
func getIncidentFromGateway(t *testing.T, runID string) map[string]any {
	t.Helper()
	return get(t, gatewayAddr, "/api/v1/incidents/"+runID)
}

// TestIntegration_GatewayIncident_ThreeHopEscalation seeds a real 3-hop chain
// in auditd — triage (ESCALATE_TO) → sysadmin diagnostic hop (TRANSITION_TO)
// → remediation — matching the pbs_db_restart_triage → pbs_sysadmin_docker_inspect
// → pbs_db_restart_action shape from the shipped playbook catalog, then
// verifies the real gateway binary classifies it correctly: the middle hop
// must land in escalations[], not remediation.
//
// This is the integration-level regression test for the original bug, where
// any successor run was unconditionally labeled "remediation" — run against
// the real HTTP stack (gateway process → auditd process), not mocks.
func TestIntegration_GatewayIncident_ThreeHopEscalation(t *testing.T) {
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	// Distinct trace IDs per hop: each hop is a separate agent session in
	// reality, and buildJourneyRefs merges adjacent phases that share a
	// trace ID — using one shared trace ID here would collapse the three
	// expected journeys[] entries into one, defeating the assertion below.
	triageRunID := recordRun(t, "pbs_db_restart_triage", map[string]any{
		"outcome":          "escalated",
		"escalated_to":     "pbs_sysadmin_docker_inspect",
		"findings_summary": "connection refused; cannot reach docker daemon from db agent toolset",
		"trace_id":         "trace-3hop-" + suffix + "-triage",
		"completed_at":     time.Now().UTC().Format(time.RFC3339),
	})

	sysadminRunID := recordRun(t, "pbs_sysadmin_docker_inspect", map[string]any{
		"prior_run_id":     triageRunID,
		"outcome":          "transitioned",
		"transitioned_to":  "pbs_db_restart_action",
		"findings_summary": "container exited cleanly (exitcode=0); safe to restart",
		"trace_id":         "trace-3hop-" + suffix + "-escalate",
		"completed_at":     time.Now().UTC().Format(time.RFC3339),
	})

	remediationRunID := recordRun(t, "pbs_db_restart_action", map[string]any{
		"prior_run_id":     sysadminRunID,
		"outcome":          "resolved",
		"findings_summary": "container restarted successfully",
		"trace_id":         "trace-3hop-" + suffix + "-remediate",
		"completed_at":     time.Now().UTC().Format(time.RFC3339),
	})

	narrative := getIncidentFromGateway(t, triageRunID)

	// Triage chapter.
	triage, _ := narrative["triage"].(map[string]any)
	if triage == nil {
		t.Fatal("narrative missing triage chapter")
	}
	if triage["run_id"] != triageRunID {
		t.Errorf("triage.run_id = %v, want %s", triage["run_id"], triageRunID)
	}

	// Escalations: exactly one entry, the sysadmin hop.
	escalations, _ := narrative["escalations"].([]any)
	if len(escalations) != 1 {
		t.Fatalf("escalations count = %d, want 1; escalations=%v", len(escalations), escalations)
	}
	hop, _ := escalations[0].(map[string]any)
	if hop == nil {
		t.Fatal("escalations[0] is not an object")
	}
	if hop["run_id"] != sysadminRunID {
		t.Errorf("escalations[0].run_id = %v, want %s", hop["run_id"], sysadminRunID)
	}
	if hop["playbook"] != "pbs_sysadmin_docker_inspect" {
		t.Errorf("escalations[0].playbook = %v, want pbs_sysadmin_docker_inspect", hop["playbook"])
	}

	// Remediation: the third hop, NOT the sysadmin hop.
	remediation, _ := narrative["remediation"].(map[string]any)
	if remediation == nil {
		t.Fatal("narrative missing remediation chapter")
	}
	if remediation["run_id"] != remediationRunID {
		t.Errorf("remediation.run_id = %v, want %s (must not be the sysadmin escalation hop %s)",
			remediation["run_id"], remediationRunID, sysadminRunID)
	}
	if remediation["playbook"] != "pbs_db_restart_action" {
		t.Errorf("remediation.playbook = %v, want pbs_db_restart_action", remediation["playbook"])
	}

	// Journeys should list three phases in order: triage, escalation:1, remediation.
	journeys, _ := narrative["journeys"].([]any)
	if len(journeys) != 3 {
		t.Fatalf("journeys count = %d, want 3; journeys=%v", len(journeys), journeys)
	}
	wantPhases := []string{"triage", "escalation:1", "remediation"}
	for i, want := range wantPhases {
		j, _ := journeys[i].(map[string]any)
		if j == nil || j["phase"] != want {
			t.Errorf("journeys[%d].phase = %v, want %q", i, j["phase"], want)
		}
	}

	t.Logf("3-hop escalation OK: triage=%s escalation=%s remediation=%s",
		triageRunID, sysadminRunID, remediationRunID)
}

// TestIntegration_GatewayIncident_EscalationOnly seeds a real 2-hop chain
// where the successor is reached via ESCALATE_TO (not TRANSITION_TO) and
// verifies the real gateway leaves remediation nil — the case that was
// silently wrong even without going 3 hops deep in the original bug.
func TestIntegration_GatewayIncident_EscalationOnly(t *testing.T) {
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	traceID := "trace-escalation-only-" + suffix

	triageRunID := recordRun(t, "pbs_db_restart_triage", map[string]any{
		"outcome":          "escalated",
		"escalated_to":     "pbs_sysadmin_docker_inspect",
		"findings_summary": "connection refused; escalating to sysadmin agent",
		"trace_id":         traceID,
		"completed_at":     time.Now().UTC().Format(time.RFC3339),
	})

	sysadminRunID := recordRun(t, "pbs_sysadmin_docker_inspect", map[string]any{
		"prior_run_id":     triageRunID,
		"outcome":          "unknown",
		"findings_summary": "ambiguous evidence; awaiting further investigation",
		"trace_id":         traceID,
		"completed_at":     time.Now().UTC().Format(time.RFC3339),
	})

	narrative := getIncidentFromGateway(t, triageRunID)

	escalations, _ := narrative["escalations"].([]any)
	if len(escalations) != 1 {
		t.Fatalf("escalations count = %d, want 1; escalations=%v", len(escalations), escalations)
	}
	if hop, _ := escalations[0].(map[string]any); hop["run_id"] != sysadminRunID {
		t.Errorf("escalations[0].run_id = %v, want %s", hop["run_id"], sysadminRunID)
	}

	if remediation := narrative["remediation"]; remediation != nil {
		t.Errorf("remediation = %v, want nil (successor was reached via ESCALATE_TO, not TRANSITION_TO)", remediation)
	}
}
