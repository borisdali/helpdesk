package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// toolEventsAuditd serves events as the trace's tool_execution events, or
// fails every request with status when status != 0.
func toolEventsAuditd(t *testing.T, events []audit.Event, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if r.URL.Path != "/v1/events" || r.URL.Query().Get("event_type") != "tool_execution" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(events) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func toolEvent(name string, class audit.ActionClass, errMsg string) audit.Event {
	return audit.Event{EventType: audit.EventTypeToolExecution, ActionClass: class, Tool: &audit.ToolExecution{Name: name, Error: errMsg}}
}

func TestFindingsOnlyOutcome(t *testing.T) {
	remediation := &audit.Playbook{SeriesID: "pbs_pgbackrest_restore_remediate", PlaybookType: "remediation"}
	triage := &audit.Playbook{SeriesID: "pbs_db_data_loss_triage", PlaybookType: "triage"}
	since := time.Now().Add(-time.Minute)
	cases := []struct {
		name     string
		pb       *audit.Playbook
		findings string
		events   []audit.Event
		status   int
		noAudit  bool
		want     string
	}{
		{"restore ran and restarted the server", remediation, "restored from backup", []audit.Event{
			toolEvent("get_pgbackrest_status", audit.ActionRead, ""), toolEvent("restore_from_backup", audit.ActionDestructive, ""),
			toolEvent("restart_service", audit.ActionDestructive, "")}, 0, false, audit.OutcomeResolved},
		{"a write counts too", remediation, "terminated idle sessions", []audit.Event{
			toolEvent("terminate_idle_connections", audit.ActionWrite, "")}, 0, false, audit.OutcomeResolved},
		// The 2026-10-11 VM run: stale backup, precondition failed, reads only.
		{"precondition failed: reads only", remediation, "backup is stale; a human must authorize", []audit.Event{
			toolEvent("get_pgbackrest_status", audit.ActionRead, ""), toolEvent("check_host", audit.ActionRead, "")}, 0, false, audit.OutcomeNoAction},
		{"the only write errored", remediation, "restore failed", []audit.Event{
			toolEvent("restore_from_backup", audit.ActionDestructive, "refusing to restore — WAL continuity is broken")}, 0, false, audit.OutcomeNoAction},
		{"no tool ran at all", remediation, "nothing to do", nil, 0, false, audit.OutcomeNoAction},
		{"auditd unreachable: can't tell, keep resolved", remediation, "restored", nil, http.StatusInternalServerError, false, audit.OutcomeResolved},
		{"no auditd configured: keep resolved", remediation, "restored", nil, 0, true, audit.OutcomeResolved},
		{"triage concluded, nothing handed on", triage, "single_instance=true; needs a DBA", nil, 0, false, audit.OutcomeDiagnosed},
		{"triage found nothing to fix", triage, "recommended=monitor", nil, 0, false, audit.OutcomeResolved},
		// A healthy health check (K8s, 2026-10-20: pgbackrest-health-triage
		// Ending D recorded diagnosed / needs_human before its FINDINGS
		// carried the marker).
		{"healthy health check", triage, "status_code=0; backup_stale=false; wal_continuity=continuous; recommended=no_changes_needed", nil, 0, false, audit.OutcomeResolved},
		{"healthy health check without the marker", triage, "status_code=0; backup_stale=false; wal_continuity=continuous", nil, 0, false, audit.OutcomeDiagnosed},
	}
	for _, c := range cases {
		g := &Gateway{}
		if !c.noAudit {
			g.auditURL = toolEventsAuditd(t, c.events, c.status)
		}
		if got := g.findingsOnlyOutcome(c.pb, c.findings, "tr_test", since); got != c.want {
			t.Errorf("%s: outcome = %q, want %q", c.name, got, c.want)
		}
	}
}
