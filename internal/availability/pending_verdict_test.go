package availability

import (
	"strings"
	"testing"
	"time"

	"helpdesk/internal/infra"
)

// The verdict is made on what is left less the open incident that is charged in
// full when the restore runs. Found live: a gate held for five minutes said
// "within" at the moment of the restore, and the incident then came to more than
// the budget had left.

func verdictOf(t *testing.T, enforcement string, serving bool, est *RecoveryEstimate, incs ...Incident) *BudgetVerdict {
	t.Helper()
	l := Compute(params(incs...))
	return CompareToBudget(BudgetInputs{Found: true, Budget: budget(), Ledger: &l, Enforcement: enforcement, Serving: serving, Estimate: est})
}

// 10m spent by a resolved outage leaves 33m12s; a triage open for 5m is charged in
// full when the restore runs, so 28m12s is what the restore has.
func TestVerdict_TakesTheOpenIncidentOffWhatIsLeft(t *testing.T) {
	v := verdictOf(t, infra.EnforceAcknowledge, true, planned(480, PlanningLongest, 3), outage("spent", 10*time.Minute, time.Hour), openTriage("inc_pitr_1", 5*time.Minute))
	if v.Status != BudgetWithin {
		t.Fatalf("status %s: %s", v.Status, v.Detail)
	}
	if v.PendingIncident != "inc_pitr_1" || v.PendingSeconds == nil || *v.PendingSeconds != 300 {
		t.Errorf("pending = %q %v", v.PendingIncident, v.PendingSeconds)
	}
	// The ledger's own figure is kept; what the restore stood to use is LeftSeconds.
	if v.RemainingSeconds == nil || *v.RemainingSeconds != 33*60+12 {
		t.Errorf("ledger remaining = %v", v.RemainingSeconds)
	}
	if l := v.LeftSeconds(); l == nil || *l != 28*60+12 {
		t.Errorf("left = %v", l)
	}
	for _, want := range []string{"28m12s left", "the open incident inc_pitr_1 (open for 5m, not charged yet) is charged in full when the restore runs"} {
		if !strings.Contains(v.Detail, want) {
			t.Errorf("missing %q in %q", want, v.Detail)
		}
	}
}

// The age is what pushes it over: the budget is not spent in the ledger, but the
// restore is certain to overspend it, so it is a breach to acknowledge, and for a
// serving instance under refuse it is refused.
func TestVerdict_AnOpenIncidentCanMakeItExceeded(t *testing.T) {
	// 40m spent leaves 3m12s; the triage open for 5m takes that to -1m48s.
	incs := []Incident{outage("spent", 40*time.Minute, time.Hour), openTriage("inc_pitr_1", 5*time.Minute)}
	est := planned(120, PlanningLongest, 3)
	v := verdictOf(t, infra.EnforceAcknowledge, true, est, incs...)
	if v.Status != BudgetExceeds || v.Gate != GateAcknowledge {
		t.Fatalf("status %s gate %s: %s", v.Status, v.Gate, v.Detail)
	}
	if l := v.LeftSeconds(); l == nil || *l != -(1*60+48) {
		t.Errorf("left = %v", l)
	}
	for _, want := range []string{"once it is charged the budget is spent (-1m48s left)", "so the restore breaches the SLO"} {
		if !strings.Contains(v.Detail, want) {
			t.Errorf("missing %q in %q", want, v.Detail)
		}
	}
	if v := verdictOf(t, infra.EnforceRefuse, true, est, incs...); v.Gate != GateRefuse {
		t.Errorf("serving under refuse: gate %q", v.Gate)
	}
	// An instance that is down is asked, not refused, as for any spent budget.
	if v := verdictOf(t, infra.EnforceRefuse, false, est, incs...); v.Gate != GateAcknowledge || v.GateNote == "" {
		t.Errorf("down under refuse: gate %q note %q", v.Gate, v.GateNote)
	}
}

// Less than the restore needs, once the incident is taken off, only MAY exceed.
func TestVerdict_AnOpenIncidentCanLeaveLessThanTheRestoreNeeds(t *testing.T) {
	// 36m spent leaves 7m12s; the triage open for 6m leaves 1m12s, for a restore of 2m.
	v := verdictOf(t, infra.EnforceAcknowledge, true, planned(120, PlanningLongest, 3), outage("spent", 36*time.Minute, time.Hour), openTriage("inc_pitr_1", 6*time.Minute))
	if v.Status != BudgetMayExceed || v.Gate != GateAcknowledge {
		t.Fatalf("status %s gate %s: %s", v.Status, v.Gate, v.Detail)
	}
}

// Nothing is taken off when there is no such incident, when the incident is
// already charged, or when there are several and which is this restore's is not
// known: the ledger's figure stands, as it did.
func TestVerdict_NothingTakenOffWithoutExactlyOnePendingIncident(t *testing.T) {
	cases := []struct {
		name string
		incs []Incident
	}{
		{"no open incident", []Incident{outage("spent", 10*time.Minute, time.Hour)}},
		{"the open incident is already charged", []Incident{outage("spent", 10*time.Minute, time.Hour), openOutage("now", 5*time.Minute)}},
		{"two open incidents", []Incident{outage("spent", 10*time.Minute, time.Hour), openTriage("inc_a", 5*time.Minute), openTriage("inc_b", 9*time.Minute)}},
	}
	for _, c := range cases {
		v := verdictOf(t, infra.EnforceAcknowledge, true, nil, c.incs...)
		if v.PendingIncident != "" || v.PendingSeconds != nil {
			t.Errorf("%s: pending %q %v", c.name, v.PendingIncident, v.PendingSeconds)
		}
		if l := v.LeftSeconds(); l == nil || v.RemainingSeconds == nil || *l != *v.RemainingSeconds {
			t.Errorf("%s: left %v, ledger %v", c.name, l, v.RemainingSeconds)
		}
		if strings.Contains(v.Detail, "not charged yet") {
			t.Errorf("%s: %q", c.name, v.Detail)
		}
	}
}

// The cost of waiting is not counted twice: it still takes the age off the
// ledger's figure once.
func TestVerdict_TheDelayStillTakesTheAgeOffOnce(t *testing.T) {
	v := verdictOf(t, infra.EnforceAcknowledge, true, planned(480, PlanningLongest, 3), outage("spent", 10*time.Minute, time.Hour), openTriage("inc_pitr_1", 5*time.Minute))
	if v.Delay == nil || v.Delay.SlackSeconds == nil || *v.Delay.SlackSeconds != 28*60+12-8*60 {
		t.Fatalf("delay = %+v", v.Delay)
	}
}

func TestBudgetVerdict_LeftSecondsOfNothing(t *testing.T) {
	var v *BudgetVerdict
	if v.LeftSeconds() != nil || (&BudgetVerdict{}).LeftSeconds() != nil {
		t.Error("a left figure from no budget")
	}
}
