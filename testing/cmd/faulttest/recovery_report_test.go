package main

import (
	"strings"
	"testing"

	"helpdesk/testing/testutil"
)

// The live VM runs (2026-10-19) printed "Incident … — resolved in 0.1s" and
// "Remediation: pbs_sysadmin_host_triage" for a 4-hop chain that took 140s
// and remediated with pbs_pgbackrest_restore_remediate.
func TestPrintIncidentSummary_RealRemediationAndTime(t *testing.T) {
	resp := testutil.AgentResponse{RunID: "plr_c18ee2b4", EscalationTarget: "pbs_sysadmin_host_triage"}
	out := captureStdout(func() {
		printIncidentSummary(resp, 152.3, "pbs_pgbackrest_restore_remediate", "resolved", "")
	})
	if !strings.Contains(out, "recovered 152.3s after the fault was injected") ||
		!strings.Contains(out, "Remediation: pbs_pgbackrest_restore_remediate (resolved)") || strings.Contains(out, "pbs_sysadmin_host_triage") {
		t.Errorf("summary =\n%s", out)
	}
	// Without the last hop, the next hop is labelled for what it is.
	out = captureStdout(func() { printIncidentSummary(resp, 1, "", "", "") })
	if !strings.Contains(out, "pbs_sysadmin_host_triage (next hop; the chain's last hop is unknown)") {
		t.Errorf("summary without the last hop =\n%s", out)
	}
}
