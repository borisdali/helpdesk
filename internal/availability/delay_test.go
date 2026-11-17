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
	for _, want := range []string{"no open incident is in the ledger", "no time already spent to count", "that incident is charged from its start", "the downtime is accruing regardless"} {
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

// An open triage that is waiting at the gate is not an outage yet (the restore is),
// so it is not charged and what is left does not shrink. It is charged in full, from
// its start, once the restore runs, so its age comes off the margin.
func openTriage(id string, detectedAgo time.Duration) Incident {
	return Incident{ID: id, Status: audit.IncidentStatusOpen, Origin: audit.IncidentOriginReal,
		SeriesIDs: []string{"pbs_pgbackrest_pitr_triage"}, DetectedAt: ago(detectedAgo)}
}

func TestDelay_AnOpenTriageIsCountedFromItsStartOnceTheRestoreRuns(t *testing.T) {
	// 10m spent by a resolved outage: 33m12s left. The triage has been open for 5m,
	// so 28m12s is the margin before the restore; a restore of 8m leaves 20m12s.
	d := delayOf(t, false, planned(480, PlanningLongest, 3), outage("spent", 10*time.Minute, time.Hour), openTriage("inc_pitr_1", 5*time.Minute))
	if d.State != DelayLatestStart || d.Charging || d.Incident != "inc_pitr_1" || d.IncidentSeconds == nil || *d.IncidentSeconds != 300 {
		t.Fatalf("state %s charging %v incident %q %v: %s", d.State, d.Charging, d.Incident, d.IncidentSeconds, d.Detail)
	}
	if d.SlackSeconds == nil || *d.SlackSeconds != 33*60+12-5*60-8*60 {
		t.Errorf("slack = %v", d.SlackSeconds)
	}
	if want := now.Add(20*time.Minute + 12*time.Second); d.LatestStart == nil || !d.LatestStart.Equal(want) {
		t.Errorf("latest start = %v, want %v", d.LatestStart, want)
	}
	for _, want := range []string{"under current assumptions: 12:20:12Z", "incident inc_pitr_1 has been open for 5m and is not charged to the ledger yet",
		"charged in full, from its start, when the restore runs", "assuming it is the incident this restore is for"} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("missing %q in %q", want, d.Detail)
		}
	}
}

// The latest start is what the ledger will actually say: starting at it leaves the
// budget exactly used, and a second later breaches it. Checked against the ledger's
// own arithmetic, with the incident closed as it would be.
func TestDelay_LatestStartIsWhereTheLedgerWillRunOut(t *testing.T) {
	spent := outage("spent", 10*time.Minute, time.Hour)
	d := delayOf(t, false, planned(480, PlanningLongest, 3), spent, openTriage("inc_pitr_1", 5*time.Minute))
	if d.SlackSeconds == nil {
		t.Fatalf("no slack: %s", d.Detail)
	}
	remainingIfStartedAfter := func(wait int64) int64 {
		closeAt := now.Add(secondsOf(wait + 480))
		restored := Incident{ID: "inc_pitr_1", Status: audit.IncidentStatusResolved, Origin: audit.IncidentOriginReal,
			SeriesIDs:  []string{"pbs_pgbackrest_pitr_triage", "pbs_pgbackrest_pitr_remediate"},
			DetectedAt: ago(5 * time.Minute), ResolvedAt: closeAt}
		p := params(spent, restored)
		p.Now = closeAt
		l := Compute(p)
		if l.RemainingSeconds == nil {
			t.Fatal("no remaining")
		}
		return *l.RemainingSeconds
	}
	if got := remainingIfStartedAfter(*d.SlackSeconds); got != 0 {
		t.Errorf("starting at the latest start leaves %ds, want exactly none", got)
	}
	if got := remainingIfStartedAfter(*d.SlackSeconds + 1); got != -1 {
		t.Errorf("a second later leaves %ds, want -1", got)
	}
	if got := remainingIfStartedAfter(0); got != *d.SlackSeconds {
		t.Errorf("starting now leaves %ds, want the slack %d", got, *d.SlackSeconds)
	}
}

// The age can be what pushes it over: a forecast, not a breach that has happened,
// and the ledger itself does not show it as spent.
func TestDelay_AnOpenTriageCanPushTheBudgetPast(t *testing.T) {
	// 40m spent: 3m12s left. A triage open for 5m is more than that.
	d := delayOf(t, false, planned(480, PlanningLongest, 3), outage("spent", 40*time.Minute, time.Hour), openTriage("inc_pitr_1", 5*time.Minute))
	if d.State != DelayProjectedBreach || d.LatestStart != nil {
		t.Fatalf("%s %v: %s", d.State, d.LatestStart, d.Detail)
	}
	for _, want := range []string{"projected budget breach: 3m12s is left", "once the open incident is charged", "overspends it by 1m48s whatever the restore takes"} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("missing %q in %q", want, d.Detail)
		}
	}
	if strings.Contains(d.Detail, "already overspent") {
		t.Errorf("a forecast is called a breach that has happened: %q", d.Detail)
	}
}

// Several open incidents that are not charged: which is this one is not known, so
// the latest start is only an upper bound and says so.
func TestDelay_SeveralOpenIncidentsGiveOnlyAnUpperBound(t *testing.T) {
	d := delayOf(t, true, planned(480, PlanningLongest, 3), outage("spent", 10*time.Minute, time.Hour), openTriage("inc_a", 5*time.Minute), openTriage("inc_b", 9*time.Minute))
	if d.State != DelayUncertain || d.Incident != "" || d.LatestStart == nil {
		t.Fatalf("%s incident %q: %s", d.State, d.Incident, d.Detail)
	}
	// The ledger's own figure, with nothing taken off: 33m12s left, less 8m.
	if want := now.Add(25*time.Minute + 12*time.Second); !d.LatestStart.Equal(want) {
		t.Errorf("upper bound = %v, want %v", d.LatestStart, want)
	}
	for _, want := range []string{"at the latest and under current assumptions", "2 open incidents on this database are not charged to the ledger yet",
		"which of them this restore is for is not known", "The instance is serving"} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("missing %q in %q", want, d.Detail)
		}
	}
}

// With no open incident (a plan asked for outside an incident) nothing is taken off.
func TestDelay_NoOpenIncidentTakesNothingOff(t *testing.T) {
	d := delayOf(t, false, planned(480, PlanningLongest, 3), outage("spent", 10*time.Minute, time.Hour))
	if d.State != DelayNotCharging || d.Incident != "" || d.SlackSeconds == nil || *d.SlackSeconds != 33*60+12-8*60 {
		t.Errorf("%s incident %q slack %v: %s", d.State, d.Incident, d.SlackSeconds, d.Detail)
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
