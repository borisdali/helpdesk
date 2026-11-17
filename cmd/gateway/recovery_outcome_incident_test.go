package main

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// The outcome says how long the incident had run: from the start of its entry run,
// which is what the availability ledger charges, so the wait at the gate is in it.
// That is what a plan's budget check is set against afterwards.
func TestRecordRecoveryOutcome_RecordsHowLongTheIncidentHadRun(t *testing.T) {
	hop := remediationRun()
	entry := &audit.PlaybookRun{
		RunID: hop.EntryRunID, SeriesID: "pbs_pgbackrest_pitr_triage",
		StartedAt: hop.StartedAt.Add(-5 * time.Minute),
	}
	gw, store := outcomeGateway(t, map[string]*audit.PlaybookRun{hop.RunID: hop, entry.RunID: entry})
	gw.recordPlaybookRunComplete(context.Background(), hop.RunID, audit.OutcomeResolved, "", "", "", "", "", nil, false, "")

	got := gateDecisions(t, store, entry.RunID)
	if len(got) != 1 {
		t.Fatalf("outcomes: %+v", got)
	}
	// The hop ran 41m12s, after 5m at the gate: 46m12s charged.
	if d := got[0].DurationSeconds; d == nil || *d != 41*60+12 {
		t.Errorf("hop duration = %v", d)
	}
	if s := got[0].IncidentSeconds; s == nil || *s != 46*60+12 {
		t.Errorf("incident seconds = %v", s)
	}
}

// With no entry run recorded the hop is its own entry, and when the entry run
// cannot be read there is no incident length rather than a wrong one.
func TestRecordRecoveryOutcome_IncidentLengthEdgeCases(t *testing.T) {
	hop := remediationRun()
	hop.EntryRunID = ""
	gw, store := outcomeGateway(t, map[string]*audit.PlaybookRun{hop.RunID: hop})
	gw.recordPlaybookRunComplete(context.Background(), hop.RunID, audit.OutcomeResolved, "", "", "", "", "", nil, false, "")
	if got := gateDecisions(t, store, hop.RunID); len(got) != 1 || got[0].IncidentSeconds == nil || *got[0].IncidentSeconds != 41*60+12 {
		t.Errorf("its own entry: %+v", got)
	}

	hop = remediationRun() // names an entry run the auditd does not have
	gw, store = outcomeGateway(t, map[string]*audit.PlaybookRun{hop.RunID: hop})
	gw.recordPlaybookRunComplete(context.Background(), hop.RunID, audit.OutcomeResolved, "", "", "", "", "", nil, false, "")
	if got := gateDecisions(t, store, hop.EntryRunID); len(got) != 1 || got[0].IncidentSeconds != nil {
		t.Errorf("an unreadable entry run: %+v", got)
	}
}
