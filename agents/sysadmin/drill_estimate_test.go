package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
)

// drillsThatTook is the estimate of a server with no real restore and one
// verified pgBackRest drill per given duration (in minutes), against a declared
// rto of 30 minutes.
func drillsThatTook(minutes ...int) *availability.RecoveryEstimate {
	now := time.Now().UTC()
	rto := 30 * time.Minute
	e := availability.EstimateRecovery(nil, availability.RestoreSeries, now, availability.EstimateWindow, &rto)
	availability.AddDrills(&e, drillRecords(minutes...), availability.DrillInputs{RestoreBytes: 10 << 30}, now, &rto)
	return &e
}

// drillRecords are verified pgBackRest drills of pgbackrest_db, one per given
// duration in minutes, of 10 GiB, a day apart.
func drillRecords(minutes ...int) []audit.DrillRecord {
	now := time.Now().UTC()
	var drills []audit.DrillRecord
	for i, m := range minutes {
		fin := now.Add(-time.Duration(i+1) * 24 * time.Hour)
		drills = append(drills, audit.DrillRecord{
			DrillID: "drl_" + string(rune('a'+i)), SourceKey: "pgbackrest_db", CloneKey: "clone", SourceSystemID: "7001", CloneSystemID: "7001",
			Method: audit.DrillMethodPgBackRest, Status: audit.DrillVerified, FinishedAt: fin, RecordedAt: fin,
			DurationSeconds: int64(m) * 60, DurationSource: audit.DrillDurationReported, RestoreBytes: 10 << 30,
		})
	}
	return drills
}

// ── the system identifier ─────────────────────────────────────────────────────

func TestParseSystemID(t *testing.T) {
	for out, want := range map[string]string{
		"system_id=7709662342314786857\n":           "7709662342314786857",
		"noise\nsystem_id=42\nmore":                 "42",
		"  system_id=  123  ":                       "123",
		"system_id=\n":                              "",
		"system_id=psql: error: connection refused": "", // an error message is not an identifier
		"system_id=12abc":                           "",
		"":                                          "",
		"no such line":                              "",
		"system_id=" + strings.Repeat("9", 40):      "", // longer than any identifier
	} {
		if got := parseSystemID(out); got != want {
			t.Errorf("parseSystemID(%q) = %q, want %q", out, got, want)
		}
	}
}

// The restore estimate is told the identifier the instance reports, so drills are
// tied to this server and not to its name.
func TestBudgetCheckForRestore_ReadsTheSourceSystemID(t *testing.T) {
	calls := withEstimate(t, restoresThatTook(10, 20, 30), nil)
	withLedger(t, 0, nil)
	objectiveInfra(t, "", func(db *infra.DBServer, _ *infra.Config) {
		db.Objectives = &infra.Objectives{RTO: &infra.Duration{Duration: 45 * time.Minute}}
	})
	old := sourceSystemIDFn
	t.Cleanup(func() { sourceSystemIDFn = old })
	var asked []string
	sourceSystemIDFn = func(_ context.Context, target string) string {
		asked = append(asked, target)
		return "7001"
	}
	budgetCheckForRestore(context.Background(), "helpdesk-test-pg-pgbackrest", true, 5<<30)
	if len(calls.drills) != 1 || calls.drills[0].SystemID != "7001" || calls.drills[0].RestoreBytes != 5<<30 {
		t.Errorf("drill inputs: %+v", calls.drills)
	}
	if len(asked) != 1 || asked[0] != "helpdesk-test-pg-pgbackrest" {
		t.Errorf("the identifier was read for %v, want the target the call named", asked)
	}
	// A restore with no size in view and the check that has none read nothing.
	budgetCheckFor(context.Background(), "helpdesk-test-pg-pgbackrest", true)
	if len(asked) != 1 {
		t.Errorf("budgetCheckFor read an identifier: %v", asked)
	}
	// When it cannot be read, drills are matched by name: an empty identifier.
	sourceSystemIDFn = func(context.Context, string) string { return "" }
	budgetCheckForRestore(context.Background(), "helpdesk-test-pg-pgbackrest", true, 5<<30)
	if calls.drills[len(calls.drills)-1].SystemID != "" {
		t.Errorf("an unreadable identifier was replaced by something: %+v", calls.drills[len(calls.drills)-1])
	}
}

// Through the real estimate: a server rebuilt under the same name is told apart
// from the one the drills were of, even before any drill of the new one exists.
func TestDrillsOfARebuiltServerDoNotCountWhenItsIdentifierIsRead(t *testing.T) {
	now := time.Now().UTC()
	rto := 30 * time.Minute
	e := availability.EstimateRecovery(nil, availability.RestoreSeries, now, availability.EstimateWindow, &rto)
	availability.AddDrills(&e, drillRecords(10, 20, 25), availability.DrillInputs{RestoreBytes: 10 << 30, SystemID: "999-rebuilt"}, now, &rto)
	if e.Drills.Samples != 0 || e.Drills.NotCounted["other_system"] != 3 || e.Drills.MatchedBy != "system_identifier" {
		t.Errorf("the old server's drills counted for the rebuilt one: %+v", e.Drills)
	}
	if e.PlanningSeconds != nil {
		t.Error("a plan was made from the drills of a server that no longer exists")
	}
	// By name alone they would have counted: this is what reading it buys.
	e = availability.EstimateRecovery(nil, availability.RestoreSeries, now, availability.EstimateWindow, &rto)
	availability.AddDrills(&e, drillRecords(10, 20, 25), availability.DrillInputs{RestoreBytes: 10 << 30}, now, &rto)
	if e.Drills.Samples != 3 {
		t.Errorf("matched by name: %+v", e.Drills)
	}
}

// ── the plan and the record ───────────────────────────────────────────────────

// A plan that rests on drills says so on the record, and says which drills: the
// stored decision is what an audit reads, and "the longest of 3 drills" has to be
// traceable to the three.
func TestPlan_ARestoreFromDrillsIsOnTheRecordWithTheDrillsItRestedOn(t *testing.T) {
	withLedger(t, 10*time.Minute, nil) // 33m12s left; rto 30m
	withEstimate(t, drillsThatTook(40, 45, 50), nil)
	budgetObjectives(t, "", "acknowledge")
	_, _ = liveEnv(t)
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()

	plan, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("%+v, %v", plan, err)
	}
	b := plan.AvailabilityBudget
	if b.DurationBasis != availability.DurationFromDrillLongest || *b.DurationSeconds != 50*60 || b.Status != availability.BudgetMayExceed {
		t.Fatalf("budget = %+v", b)
	}
	got := decisions(t, store)[audit.RecoveryStagePlan]
	if len(got) != 1 || len(got[0].Options) != 1 {
		t.Fatalf("decisions = %+v", got)
	}
	o := got[0].Options[0]
	if o.BudgetDurationBasis != "drill_longest" || *o.BudgetDurationSeconds != 3000 || o.PlanningSource != "drills" {
		t.Errorf("recorded basis %q (%v s) from %q", o.BudgetDurationBasis, o.BudgetDurationSeconds, o.PlanningSource)
	}
	if o.DrillSamples == nil || *o.DrillSamples != 3 || len(o.DrillIDs) != 3 {
		t.Errorf("the drills it rested on are not recorded: samples %v ids %v", o.DrillSamples, o.DrillIDs)
	}
	// Real restores were absent, and the record says that too, not "estimated".
	if o.EstimateStatus != availability.EstimateNone || *o.EstimateSamples != 0 {
		t.Errorf("the real-restore figures: %q, %v", o.EstimateStatus, o.EstimateSamples)
	}
	// Narrative wording: the faulttest reads this basis and says it was a copy.
	if !strings.Contains(strings.ToLower(o.BudgetVerdictDetail), "drills") || !strings.Contains(o.BudgetVerdictDetail, "demonstrated on a copy") {
		t.Errorf("verdict detail: %q", o.BudgetVerdictDetail)
	}
}

// A plan from real restores records them and no drills, even when drills exist.
func TestPlan_RealRestoresRecordNoDrillTrace(t *testing.T) {
	withLedger(t, 10*time.Minute, nil)
	est := restoresThatTook(40, 45, 50)
	availability.AddDrills(est, drillRecords(1, 2, 3), availability.DrillInputs{RestoreBytes: 10 << 30}, time.Now().UTC(), nil)
	withEstimate(t, est, nil)
	budgetObjectives(t, "", "acknowledge")
	_, _ = liveEnv(t)
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()
	if _, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget}); err != nil {
		t.Fatal(err)
	}
	o := decisions(t, store)[audit.RecoveryStagePlan][0].Options[0]
	if o.PlanningSource != "restores" || o.DrillSamples != nil || len(o.DrillIDs) != 0 || o.BudgetDurationBasis != "history_longest" {
		t.Errorf("recorded %q from %q, drill samples %v ids %v", o.BudgetDurationBasis, o.PlanningSource, o.DrillSamples, o.DrillIDs)
	}
}

// ── the gate ──────────────────────────────────────────────────────────────────

// The gate reads a plan from drills like any other: one that may not fit still
// asks for the acknowledgement, and one that fits asks for nothing.
func TestPlan_AGateOnADrillBasedPlanBehavesLikeAnyOther(t *testing.T) {
	withLedger(t, 10*time.Minute, nil) // 33m12s left
	budgetObjectives(t, "", "acknowledge")
	_, _ = liveEnv(t)
	_, cleanup := withRealToolAuditor(t)
	defer cleanup()
	plan := func() *availability.BudgetVerdict {
		p, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
			RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
		if err != nil {
			t.Fatal(err)
		}
		return p.AvailabilityBudget
	}

	withEstimate(t, drillsThatTook(40, 45, 50), nil)
	if b := plan(); b.Status != availability.BudgetMayExceed || b.Gate != availability.GateAcknowledge {
		t.Errorf("a drill that took 50m with 33m left: %+v", b)
	}
	if err := budgetGateError(plan(), false); err == nil || !strings.Contains(err.Error(), "may exceed the availability budget") {
		t.Errorf("approving without the acknowledgement must be stopped: %v", err)
	}
	if err := budgetGateError(plan(), true); err != nil {
		t.Errorf("an acknowledged breach was stopped: %v", err)
	}

	withEstimate(t, drillsThatTook(5, 6, 7), nil)
	if b := plan(); b.Status != availability.BudgetWithin || b.Gate != "" {
		t.Errorf("quick drills: %+v", b)
	}
}

// Under refuse, a serving instance whose budget is spent is refused whatever the
// basis of the duration, and drills do not soften that.
func TestPlan_RefuseStandsWithADrillBasedEstimate(t *testing.T) {
	withLedger(t, 60*time.Minute, nil) // overspent
	withEstimate(t, drillsThatTook(1, 2, 3), nil)
	budgetObjectives(t, "", "refuse")
	_, _ = liveEnv(t)
	_, cleanup := withRealToolAuditor(t)
	defer cleanup()
	p, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil {
		t.Fatal(err)
	}
	if b := p.AvailabilityBudget; b.Status != availability.BudgetExceeds || b.Gate != availability.GateRefuse {
		t.Errorf("an overspent budget with quick drills: %+v", b)
	}
}

// ── the posture signal ────────────────────────────────────────────────────────

func TestRtoExposure_FromDrillsWhenThereAreTooFewRealRestores(t *testing.T) {
	obj := infra.ResolvedObjectives{RTO: dur(30*time.Minute, "d")}

	over := rtoExposure(obj, drillsThatTook(10, 20, 45))
	if over.Status != ExposureExceeds || *over.MeasuredSeconds != 45*60 ||
		!strings.Contains(over.Detail, "by restore drills, restores on a copy and not on this server") ||
		!strings.Contains(over.Detail, "1 of the 3 drills took longer than the rto of 30m") {
		t.Errorf("an overrun drill: %+v", over)
	}
	within := rtoExposure(obj, drillsThatTook(10, 20, 25))
	if within.Status != ExposureWithin || *within.MeasuredSeconds != 25*60 || !strings.Contains(within.Detail, "none of the 3 drills") {
		t.Errorf("drills within the rto: %+v", within)
	}
	// Too few drills say so, with why, like too few restores.
	few := rtoExposure(obj, drillsThatTook(10, 20))
	if few.Status != ExposureNotComparable || few.MeasuredSeconds != nil ||
		!strings.Contains(few.Detail, "not enough history of restores") || !strings.Contains(few.Detail, "restore drills: only 2 restore drill(s)") {
		t.Errorf("two drills: %+v", few)
	}
	// Real restores are what is said when there are enough of them, drills or no.
	real3 := restoresThatTook(10, 12, 14)
	availability.AddDrills(real3, drillRecords(50, 55, 60), availability.DrillInputs{RestoreBytes: 10 << 30}, time.Now().UTC(), nil)
	c := rtoExposure(obj, real3)
	if c.Status != ExposureWithin || *c.MeasuredSeconds != 14*60 || strings.Contains(c.Detail, "drill") {
		t.Errorf("real restores must outrank drills: %+v", c)
	}
}

func TestWithObjectiveExposure_SizesDrillsAgainstTheNewestOne(t *testing.T) {
	withExposureClock(t)
	budgetObjectives(t, "15m", "")
	calls := withEstimate(t, drillsThatTook(10, 20, 45), nil)
	res := statusArchivedAgo(time.Minute)
	withObjectiveExposure(context.Background(), &res, "pgbackrest_db")
	if len(calls.drills) != 1 || !calls.drills[0].SizeFromNewestDrill || calls.drills[0].RestoreBytes != 0 {
		t.Errorf("the posture check has no restore in view, so it must size drills against the newest: %+v", calls.drills)
	}
	if x := res.ObjectiveExposure; x == nil || x.RTO.Status != ExposureExceeds {
		t.Errorf("exposure = %+v", x)
	}
}
