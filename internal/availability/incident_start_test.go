package availability

import (
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

// Found live: when the restore runs, the gate's approval has ended the triage hop
// and marked the incident escalated, so the ledger no longer shows it as open and
// the verdict, made at that moment, read "within" of a budget the incident had
// already used. The approval says when the incident began, and the verdict uses it.

// An incident whose triage hop has ended: escalated, closed at the approval, and so
// neither open nor charged.
func escalatedTriage(id string, detectedAgo, resolvedAgo time.Duration) Incident {
	return Incident{ID: id, Status: audit.IncidentStatusEscalated, Origin: audit.IncidentOriginReal,
		SeriesIDs: []string{"pbs_pgbackrest_pitr_triage"}, DetectedAt: ago(detectedAgo), ResolvedAt: ago(resolvedAgo)}
}

func verdictStarted(t *testing.T, enforcement string, serving bool, start time.Time, incs ...Incident) *BudgetVerdict {
	t.Helper()
	l := Compute(params(incs...))
	return CompareToBudget(BudgetInputs{Found: true, Budget: budget(), Ledger: &l, Enforcement: enforcement, Serving: serving,
		Estimate: planned(480, PlanningLongest, 3), IncidentStart: start})
}

func TestVerdict_UsesTheIncidentStartWhenTheLedgerCannotSeeIt(t *testing.T) {
	// 10m spent leaves 33m12s. The incident began 5m ago, but the ledger holds it as
	// an escalated triage: not open, not charged. Told its start, the verdict takes
	// 5m off, leaving 28m12s.
	spent := outage("spent", 10*time.Minute, time.Hour)
	v := verdictStarted(t, infra.EnforceAcknowledge, true, ago(5*time.Minute), spent, escalatedTriage("inc_pitr_1", 5*time.Minute, time.Minute))
	if v.Status != BudgetWithin {
		t.Fatalf("status %s: %s", v.Status, v.Detail)
	}
	if v.PendingSeconds == nil || *v.PendingSeconds != 300 {
		t.Fatalf("pending = %v", v.PendingSeconds)
	}
	if l := v.LeftSeconds(); l == nil || *l != 28*60+12 {
		t.Errorf("left = %v", l)
	}
	if !strings.Contains(v.Detail, "the incident this restore is for (open for 5m, not charged yet)") {
		t.Errorf("detail: %s", v.Detail)
	}
	if v.Delay == nil || v.Delay.State != DelayLatestStart || v.Delay.SlackSeconds == nil || *v.Delay.SlackSeconds != 28*60+12-8*60 {
		t.Fatalf("delay = %+v", v.Delay)
	}
	if !strings.Contains(v.Delay.Detail, "the incident this restore is for has been open for 5m") || strings.Contains(v.Delay.Detail, "assuming it is the incident") {
		t.Errorf("delay detail: %s", v.Delay.Detail)
	}

	// Without the start, the same ledger says nothing of it: the blindness found live.
	if v := verdictStarted(t, infra.EnforceAcknowledge, true, time.Time{}, spent, escalatedTriage("inc_pitr_1", 5*time.Minute, time.Minute)); v.PendingSeconds != nil {
		t.Errorf("a pending incident from nothing: %v", v.PendingSeconds)
	}
}

// A long wait is a breach, to be acknowledged, and refused for a serving instance
// under refuse, at the moment of the restore too.
func TestVerdict_TheIncidentStartMakesALongWaitABreach(t *testing.T) {
	spent := outage("spent", 10*time.Minute, time.Hour)
	long := escalatedTriage("inc_pitr_1", 40*time.Minute, time.Minute) // 33m12s left, 40m used
	v := verdictStarted(t, infra.EnforceAcknowledge, true, ago(40*time.Minute), spent, long)
	if v.Status != BudgetExceeds || v.Gate != GateAcknowledge {
		t.Fatalf("status %s gate %s: %s", v.Status, v.Gate, v.Detail)
	}
	if v := verdictStarted(t, infra.EnforceRefuse, true, ago(40*time.Minute), spent, long); v.Gate != GateRefuse {
		t.Errorf("serving under refuse: %q", v.Gate)
	}
}

// The ledger's own open incident keeps its name; one already charged is not taken
// off twice; a start in the future is ignored.
func TestVerdict_IncidentStartEdgeCases(t *testing.T) {
	spent := outage("spent", 10*time.Minute, time.Hour)
	if v := verdictStarted(t, infra.EnforceAcknowledge, true, ago(5*time.Minute), spent, openTriage("inc_pitr_1", 5*time.Minute)); v.PendingIncident != "inc_pitr_1" || v.PendingSeconds == nil || *v.PendingSeconds != 300 {
		t.Errorf("the ledger's own incident: %q %v", v.PendingIncident, v.PendingSeconds)
	}
	if v := verdictStarted(t, infra.EnforceAcknowledge, true, ago(5*time.Minute), spent, openOutage("now", 5*time.Minute)); v.PendingSeconds != nil {
		t.Errorf("an incident already charged was taken off again: %v", v.PendingSeconds)
	}
	if v := verdictStarted(t, infra.EnforceAcknowledge, true, now.Add(time.Hour), spent); v.PendingSeconds != nil {
		t.Errorf("a start in the future was used: %v", v.PendingSeconds)
	}
	// Several open incidents: the start says which is this restore's.
	v := verdictStarted(t, infra.EnforceAcknowledge, true, ago(5*time.Minute), spent, openTriage("inc_a", 5*time.Minute), openTriage("inc_b", 9*time.Minute))
	if v.PendingSeconds == nil || *v.PendingSeconds != 300 || v.PendingIncident != "" {
		t.Errorf("several open: %q %v", v.PendingIncident, v.PendingSeconds)
	}
}
