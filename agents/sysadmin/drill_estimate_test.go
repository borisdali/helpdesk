package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	if len(calls.drills) != 1 || calls.drills[0].RestoreBytes != 5<<30 || calls.drills[0].ReadSystemID == nil {
		t.Fatalf("drill inputs: %+v", calls.drills)
	}
	// Nothing was read by the call itself: the estimate reads it only if there is a
	// drill to match, so a server nobody has drilled never pays for the command.
	if len(asked) != 0 {
		t.Errorf("the identifier was read eagerly, for %v", asked)
	}
	// When the estimate asks, it is read for the target the call named.
	if got := calls.drills[0].ReadSystemID(); got != "7001" {
		t.Errorf("ReadSystemID = %q", got)
	}
	if len(asked) != 1 || asked[0] != "helpdesk-test-pg-pgbackrest" {
		t.Errorf("the identifier was read for %v, want the target the call named", asked)
	}
	// The check that has no restore in view offers no way to read one.
	budgetCheckFor(context.Background(), "helpdesk-test-pg-pgbackrest", true)
	if got := calls.drills[len(calls.drills)-1].ReadSystemID(); got != "" || len(asked) != 1 {
		t.Errorf("budgetCheckFor read an identifier: %q, asked %v", got, asked)
	}
	// When it cannot be read, the answer is empty and drills are matched by name.
	sourceSystemIDFn = func(context.Context, string) string { return "" }
	budgetCheckForRestore(context.Background(), "helpdesk-test-pg-pgbackrest", true, 5<<30)
	if got := calls.drills[len(calls.drills)-1].ReadSystemID(); got != "" {
		t.Errorf("an unreadable identifier was replaced by %q", got)
	}
}

// ── reading it: the script, and the bound ─────────────────────────────────────

// systemIDRun runs the real script with stub psql and pg_controldata on the PATH,
// so its order of preference and its refusal of garbage are tested on any machine.
func systemIDRun(t *testing.T, psql, controldata string) string {
	t.Helper()
	dir := t.TempDir()
	stub := func(name, body string) {
		if body == "" {
			return
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("psql", psql)
	stub("pg_controldata", controldata)
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "PG_VERSION"), []byte("16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", systemIDScript)
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "PGDATA=" + data}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	return parseSystemID(string(out))
}

func TestSystemIDScript_OrderOfPreferenceAndGarbage(t *testing.T) {
	controldata := `echo "pg_control version number:            1300"; echo "Database system identifier:           2222222222222222222"`
	for name, c := range map[string]struct{ psql, controldata, want string }{
		"the running instance is asked first":     {`echo 1111111111111111111`, controldata, "1111111111111111111"},
		"psql fails: the data directory is read":  {`exit 2`, controldata, "2222222222222222222"},
		"psql prints nothing: the data directory": {`:`, controldata, "2222222222222222222"},
		"neither works":                            {`exit 2`, `exit 1`, ""},
		"neither is installed":                     {``, ``, ""},
		"psql prints an error as its answer":       {`echo "psql: error: connection refused"`, `exit 1`, ""},
		"pg_controldata prints something else":     {`exit 2`, `echo "pg_controldata: error: could not open file"`, ""},
		"pg_controldata with no identifier line":   {`exit 2`, `echo "pg_control version number: 1300"`, ""},
		"psql answers with several lines: the 1st": {`printf '3333333333333333333\n4444\n'`, controldata, "3333333333333333333"},
	} {
		if got := systemIDRun(t, c.psql, c.controldata); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// blockingRunner never answers until its context ends: a docker exec or an ssh
// that has hung.
type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ string, _ []string, _ []string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// A command that hangs does not hold up the plan: the read gives up at its bound
// and says "not known".
func TestReadSourceSystemID_GivesUpAtTheBound(t *testing.T) {
	oldRunner, oldInfra, oldBound := cmdRunner, infraConfig, systemIDTimeout
	t.Cleanup(func() { cmdRunner, infraConfig, systemIDTimeout = oldRunner, oldInfra, oldBound })
	cmdRunner, infraConfig, systemIDTimeout = blockingRunner{}, nil, 100*time.Millisecond

	began := time.Now()
	got := readSourceSystemID(context.Background(), "anything")
	if got != "" {
		t.Errorf("a read that never answered gave %q", got)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("the read waited %s, far past its bound", took)
	}
}

func TestReadSourceSystemID_ErrorsAreNotKnownNotFailures(t *testing.T) {
	oldRunner, oldInfra := cmdRunner, infraConfig
	t.Cleanup(func() { cmdRunner, infraConfig = oldRunner, oldInfra })
	infraConfig = nil
	cmdRunner = mockRunnerFn(func() (string, error) { return "partial output", errors.New("docker: no such container") })
	if got := readSourceSystemID(context.Background(), "x"); got != "" {
		t.Errorf("a failed command gave %q", got)
	}
	cmdRunner = mockRunnerFn(func() (string, error) { return "system_id=424242\n", nil })
	if got := readSourceSystemID(context.Background(), "x"); got != "424242" {
		t.Errorf("a good answer gave %q", got)
	}
}

type mockRunnerFn func() (string, error)

func (f mockRunnerFn) Run(context.Context, string, []string, []string) (string, error) { return f() }

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
