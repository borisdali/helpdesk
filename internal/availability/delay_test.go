package availability

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

var errTest = errors.New("audit service down")

// An outage incident that is still open, detected `ago` before now.
func openOutage(id string, detectedAgo time.Duration) Incident {
	return Incident{ID: id, Status: audit.IncidentStatusOpen, Origin: audit.IncidentOriginReal,
		SeriesIDs:  []string{"pbs_db_restart_triage", "pbs_pgbackrest_restore_remediate"},
		DetectedAt: ago(detectedAgo)}
}

// planned80 is an estimate that plans a restore of 8 minutes from three samples.
func planned(secs int64, basis string, samples int) *RecoveryEstimate {
	return &RecoveryEstimate{Status: EstimateEstimated, Samples: samples, PlanningSeconds: &secs, PlanningBasis: basis}
}

func delayOf(t *testing.T, serving bool, est *RecoveryEstimate, incs ...Incident) *BudgetDelay {
	t.Helper()
	p := params(incs...)
	l := Compute(p)
	in := BudgetInputs{Found: true, Budget: budget(), Ledger: &l, Enforcement: infra.EnforceAcknowledge, Serving: serving, Estimate: est}
	v := CompareToBudget(in)
	if v.Delay == nil {
		t.Fatalf("no delay on the verdict %s (%s)", v.Status, v.Detail)
	}
	return v.Delay
}

// While an open outage incident is being charged, the budget left shrinks as the
// approval waits, so there is a latest start that stays within it.
func TestDelay_LatestStartWhileTheIncidentIsCharged(t *testing.T) {
	// 10m spent by a resolved outage, 5m by the open one: 15m of 43m12s, so 28m12s
	// left; a restore taken to need 8m leaves 20m12s of margin.
	d := delayOf(t, false, planned(480, PlanningLongest, 3), outage("spent", 10*time.Minute, time.Hour), openOutage("now", 5*time.Minute))
	if d.State != DelayLatestStart || !d.Charging {
		t.Fatalf("state %s charging %v: %s", d.State, d.Charging, d.Detail)
	}
	if d.SlackSeconds == nil || *d.SlackSeconds != 28*60+12-8*60 {
		t.Errorf("slack = %v", d.SlackSeconds)
	}
	if want := now.Add(20*time.Minute + 12*time.Second); d.LatestStart == nil || !d.LatestStart.Equal(want) {
		t.Errorf("latest start = %v, want %v", d.LatestStart, want)
	}
	for _, want := range []string{
		"estimated latest start to remain within budget, under current assumptions: 12:20:12Z (in 20m12s)",
		"assuming a restore of 8m (the longest of 3 past restores)",
	} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("missing %q in %q", want, d.Detail)
		}
	}
	if strings.Contains(d.Detail, "serving") {
		t.Errorf("a down instance is told it is serving: %q", d.Detail)
	}
}

// A serving instance is not down while the incident waits: the line says whose
// clock is ticking.
func TestDelay_ServingInstanceSaysWhoseClockItIs(t *testing.T) {
	d := delayOf(t, true, planned(480, PlanningP90, 12), openOutage("now", 5*time.Minute))
	if d.State != DelayLatestStart {
		t.Fatalf("state %s: %s", d.State, d.Detail)
	}
	for _, want := range []string{"the p90 of 12 past restores", "The instance is serving, so the real downtime starts only when the restore stops it",
		"the ledger charges the incident's elapsed time"} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("missing %q in %q", want, d.Detail)
		}
	}
}

// The states the review asked to keep apart: a margin of zero, a restore that is
// projected to exceed what is left, and a breach that has already happened.
func TestDelay_NoMarginProjectedBreachAndOverspent(t *testing.T) {
	// 2592s allowed. Open for 15m18s (918s): 1674s left. A restore planned at 1674s fits exactly.
	exact := delayOf(t, false, planned(1674, PlanningLongest, 3), openOutage("now", 15*time.Minute+18*time.Second))
	if exact.State != DelayNoMargin || exact.LatestStart != nil || !strings.Contains(exact.Detail, "no remaining margin") {
		t.Errorf("exact: %s %v %q", exact.State, exact.LatestStart, exact.Detail)
	}
	// One second more than is left: projected, not a breach yet.
	proj := delayOf(t, false, planned(1675, PlanningLongest, 3), openOutage("now", 15*time.Minute+18*time.Second))
	if proj.State != DelayProjectedBreach || proj.LatestStart != nil ||
		!strings.Contains(proj.Detail, "projected budget breach") || !strings.Contains(proj.Detail, "exceed the budget by 1s whenever it starts") ||
		!strings.Contains(proj.Detail, "adds a minute to it") {
		t.Errorf("projected: %s %q", proj.State, proj.Detail)
	}
	if proj.SlackSeconds == nil || *proj.SlackSeconds != -1 {
		t.Errorf("slack = %v", proj.SlackSeconds)
	}
	// Spent: 50m against 43m12s. A breach that has happened, not a projection.
	over := delayOf(t, false, nil, outage("big", 50*time.Minute, time.Hour))
	if over.State != DelayOverspent || over.LatestStart != nil || over.SlackSeconds != nil ||
		!strings.Contains(over.Detail, "budget already overspent by 6m48s") || strings.Contains(over.Detail, "projected") {
		t.Errorf("overspent: %s %q", over.State, over.Detail)
	}
	if !strings.Contains(over.Detail, "any further downtime adds to it") {
		t.Errorf("with nothing open the cost is not stated as downtime: %q", over.Detail)
	}
	// Exactly used up.
	used := delayOf(t, false, nil, outage("exact", 43*time.Minute+12*time.Second, time.Hour))
	if used.State != DelayOverspent || !strings.Contains(used.Detail, "no budget left") {
		t.Errorf("used up: %s %q", used.State, used.Detail)
	}
}

// With no open incident the ledger is not charging anything as time passes, and
// the line says so instead of inventing a deadline.
func TestDelay_NothingIsBeingCharged(t *testing.T) {
	d := delayOf(t, false, planned(480, PlanningLongest, 3), outage("spent", 10*time.Minute, time.Hour))
	if d.State != DelayNotCharging || d.Charging || d.LatestStart != nil {
		t.Fatalf("%s charging=%v latest=%v: %s", d.State, d.Charging, d.LatestStart, d.Detail)
	}
	for _, want := range []string{"no open incident is being charged", "waiting does not use the budget", "the downtime is accruing regardless"} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("missing %q in %q", want, d.Detail)
		}
	}
	// An open incident that has stayed open for a day is unconfirmed, not charged.
	stale := delayOf(t, false, planned(480, PlanningLongest, 3), openOutage("stuck", 30*time.Hour))
	if stale.State != DelayNotCharging {
		t.Errorf("a stale open incident was treated as charging: %s", stale.State)
	}
}

// With no estimate the restore is taken to last as long as the declared rto, and
// the line says it is an upper bound: which here is longer than what is left.
func TestDelay_DeclaredRTOIsSaidToBeAnUpperBound(t *testing.T) {
	d := delayOf(t, false, nil, openOutage("now", 20*time.Minute)) // 23m12s left, rto 30m
	if d.State != DelayProjectedBreach || !strings.Contains(d.Detail, "the declared rto, an upper bound") {
		t.Errorf("%s %q", d.State, d.Detail)
	}
	if d.PlannedSeconds == nil || *d.PlannedSeconds != 1800 || d.PlannedBasis != "the declared rto, an upper bound" {
		t.Errorf("planned = %v %q", d.PlannedSeconds, d.PlannedBasis)
	}
}

// Nothing is said when there is nothing to say it from.
func TestDelay_AbsentWithoutABudgetOrALedger(t *testing.T) {
	if got := delayFor(nil, nil, false); got != nil {
		t.Errorf("nil verdict: %+v", got)
	}
	l := Compute(params())
	if got := delayFor(&BudgetVerdict{}, &l, false); got != nil {
		t.Errorf("no remaining: %+v", got)
	}
	// An SLO that could not be read: the verdict has no budget and no delay.
	v := CompareToBudget(BudgetInputs{Found: true, Invalid: true, Problems: []string{"availability_slo: not a number"}, Enforcement: infra.EnforceWarn})
	if v.Delay != nil {
		t.Errorf("invalid objective: %+v", v.Delay)
	}
	// A ledger that could not be read.
	v = CompareToBudget(BudgetInputs{Found: true, Budget: budget(), LedgerErr: errTest, Enforcement: infra.EnforceWarn})
	if v.Delay != nil {
		t.Errorf("ledger unavailable: %+v", v.Delay)
	}
	// No planned duration (no estimate, no rto): margin is not known, so no deadline.
	noRTO := noRTOBudget()
	lp := params(openOutage("now", 5*time.Minute))
	lp.Budget = noRTO
	l2 := Compute(lp)
	v = CompareToBudget(BudgetInputs{Found: true, Budget: noRTO, Ledger: &l2, Enforcement: infra.EnforceWarn})
	if v.Delay != nil {
		t.Errorf("no planned duration: %+v", v.Delay)
	}
}

func TestDelay_IsInTheVerdictJSON(t *testing.T) {
	l := Compute(params(openOutage("now", 5*time.Minute)))
	v := CompareToBudget(BudgetInputs{Found: true, Budget: budget(), Ledger: &l, Enforcement: infra.EnforceWarn, Estimate: planned(480, PlanningLongest, 3)})
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"delay":{"state":"latest_start"`, `"latest_start":"2026-11-10T12:`, `"charging":true`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}
