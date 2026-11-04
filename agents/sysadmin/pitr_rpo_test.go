package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/infra"
)

func i64(n int64) *int64 { return &n }

func rpoOf(d time.Duration, source string) infra.ResolvedObjectives {
	return infra.ResolvedObjectives{
		Environment: "prod",
		RPO:         infra.ResolvedDuration{Set: true, Value: d, Source: source},
	}
}

func TestCompareToRPO(t *testing.T) {
	fifteen := rpoOf(15*time.Minute, "environment:prod")

	for _, c := range []struct {
		name   string
		est    *DataLossEstimate
		obj    infra.ResolvedObjectives
		found  bool
		status string
		attn   bool
		detail []string
	}{
		{"exact, inside", &DataLossEstimate{WindowSeconds: i64(600)}, fifteen, true, RPOWithin, false,
			[]string{"10m 00s discarded, within an RPO of 15m 00s (environment:prod)"}},
		{"exact, exactly at the objective", &DataLossEstimate{WindowSeconds: i64(900)}, fifteen, true, RPOWithin, false, nil},
		{"exact, over", &DataLossEstimate{WindowSeconds: i64(4687)}, fifteen, true, RPOExceeds, true,
			[]string{"1h 18m 07s discarded against an RPO of 15m 00s (environment:prod)", "exceeds it by 1h 03m 07s"}},
		{"upper bound, inside even at the bound", &DataLossEstimate{WindowSeconds: i64(600), UpperBound: true}, fifteen, true, RPOWithin, false,
			[]string{"even at that upper bound"}},
		{"upper bound, over: it may or may not be", &DataLossEstimate{WindowSeconds: i64(4687), UpperBound: true}, fifteen, true, RPOMayExceed, true,
			[]string{"upper bound", "real loss may be less", "up to 1h 03m 07s"}},
		{"zero objective, no loss", &DataLossEstimate{WindowSeconds: i64(0)}, rpoOf(0, "database"), true, RPOWithin, false, nil},
		{"zero objective, any loss", &DataLossEstimate{WindowSeconds: i64(1)}, rpoOf(0, "database"), true, RPOExceeds, true,
			[]string{"RPO of 0s (database)"}},
		{"an LSN target has no time span", &DataLossEstimate{WALBytes: func() *uint64 { n := uint64(16 << 20); return &n }()}, fifteen, true, RPONotComparable, false,
			[]string{"cannot be compared", "16.0 MiB of WAL"}},
		{"no estimate to compare", nil, fifteen, true, RPONotComparable, false, nil},
		{"no objective set", &DataLossEstimate{WindowSeconds: i64(600)}, infra.ResolvedObjectives{}, true, RPONoObjective, false,
			[]string{"no recovery point objective is set", "unchecked"}},
		{"an objective that could not be used", &DataLossEstimate{WindowSeconds: i64(600)},
			infra.ResolvedObjectives{Problems: []string{`db_servers.a.objectives: invalid duration "soon"`}}, true, RPOObjectiveInvalid, false,
			[]string{`invalid duration "soon"`, "unchecked"}},
		{"not a configured database", &DataLossEstimate{WindowSeconds: i64(600)}, infra.ResolvedObjectives{}, false, RPONoObjective, false,
			[]string{"not a configured database"}},
	} {
		got := compareToRPO(c.est, c.obj, c.found)
		if got.Status != c.status {
			t.Errorf("%s: status = %s, want %s (%s)", c.name, got.Status, c.status, got.Detail)
		}
		if got.NeedsAttention() != c.attn {
			t.Errorf("%s: NeedsAttention = %v, want %v", c.name, got.NeedsAttention(), c.attn)
		}
		for _, want := range c.detail {
			if !strings.Contains(got.Detail, want) {
				t.Errorf("%s: detail %q lacks %q", c.name, got.Detail, want)
			}
		}
	}

	// The objective and where it came from travel with the verdict.
	got := compareToRPO(&DataLossEstimate{WindowSeconds: i64(600)}, fifteen, true)
	if got.RPOSeconds == nil || *got.RPOSeconds != 900 || got.Source != "environment:prod" || got.Environment != "prod" {
		t.Errorf("comparison = %+v", got)
	}
	var none *RPOComparison
	if none.NeedsAttention() {
		t.Error("a nil comparison needs attention")
	}
}

func objectiveInfra(t *testing.T, rpo string, mutate func(db *infra.DBServer, c *infra.Config)) {
	t.Helper()
	withPgBackRestDockerInfra(t)
	db := infraConfig.DBServers["pgbackrest_db"]
	if rpo != "" {
		d, err := infra.ParseDuration(rpo)
		if err != nil {
			t.Fatal(err)
		}
		infraConfig.Environments = map[string]infra.Environment{"prod": {Objectives: infra.Objectives{RPO: &infra.Duration{Duration: d}}}}
		db.Environment = "prod"
	}
	if mutate != nil {
		mutate(&db, infraConfig)
	}
	infraConfig.DBServers["pgbackrest_db"] = db
}

func TestObjectivesFor(t *testing.T) {
	objectiveInfra(t, "15m", nil)

	if got, ok := objectivesFor("pgbackrest_db"); !ok || got.RPO.Seconds() != 900 || got.RPO.Source != "environment:prod" {
		t.Errorf("by server ID: %+v, %v", got, ok)
	}
	// resolveHost also accepts a connection string; so must this, or the same
	// call would be checked against an objective when named one way and not the
	// other.
	if got, ok := objectivesFor("host=localhost"); !ok || got.RPO.Seconds() != 900 {
		t.Errorf("by connection string: %+v, %v", got, ok)
	}
	if _, ok := objectivesFor("nope"); ok {
		t.Error("an unknown target resolved")
	}
	infraConfig = nil
	if _, ok := objectivesFor("pgbackrest_db"); ok {
		t.Error("resolved without infrastructure config")
	}
}

// The dry run an operator reads carries the comparison. The simulated serving
// instance's history runs 8 days 10 hours past liveTarget.
func TestPlanPITRWithEstimate_ComparesWithTheObjective(t *testing.T) {
	args := PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget}

	for _, c := range []struct {
		name    string
		rpo     string
		mutate  func(db *infra.DBServer, c *infra.Config)
		status  string
		source  string
		problem string
	}{
		{"over the objective", "15m", nil, RPOExceeds, "environment:prod", ""},
		{"inside a generous one", "30d", nil, RPOWithin, "environment:prod", ""},
		{"the database's own objective wins", "30d", func(db *infra.DBServer, _ *infra.Config) {
			d := infra.Duration{Duration: time.Minute}
			db.Objectives = &infra.Objectives{RPO: &d}
		}, RPOExceeds, "database", ""},
		{"no objective anywhere", "", nil, RPONoObjective, "", ""},
		{"a typo in the environment is not the fleet default", "", func(db *infra.DBServer, c *infra.Config) {
			d := infra.Duration{Duration: 30 * 24 * time.Hour}
			c.Defaults = &infra.Defaults{Objectives: infra.Objectives{RPO: &d}}
			db.Environment = "prdo"
		}, RPOObjectiveInvalid, "", `environment "prdo" is not declared`},
	} {
		objectiveInfra(t, c.rpo, c.mutate)
		_, _ = liveEnv(t)
		plan, err := planPITRWithEstimate(context.Background(), args)
		if err != nil || plan.Status != PITRFeasible {
			t.Fatalf("%s: %+v, %v", c.name, plan, err)
		}
		e := plan.DataLossEstimate
		if e == nil || e.Objective == nil {
			t.Fatalf("%s: no comparison in %+v", c.name, e)
		}
		o := e.Objective
		if o.Status != c.status || o.Source != c.source {
			t.Errorf("%s: status=%s source=%q: %s", c.name, o.Status, o.Source, o.Detail)
		}
		if c.problem != "" && !strings.Contains(strings.Join(o.Problems, "|"), c.problem) {
			t.Errorf("%s: problems = %v", c.name, o.Problems)
		}
	}
}

// A down instance's figure is an upper bound: over the objective it can only
// say "may", and inside it says so even at the bound.
func TestPlanPITRWithEstimate_DownInstanceIsAnUpperBound(t *testing.T) {
	args := PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"}
	for rpo, want := range map[string]string{"1m": RPOMayExceed, "3650d": RPOWithin} {
		objectiveInfra(t, rpo, nil)
		sim, _ := liveEnv(t)
		sim.up = false
		plan, err := planPITRWithEstimate(context.Background(), args)
		if err != nil || plan.Status != PITRFeasible {
			t.Fatalf("rpo %s: %+v, %v", rpo, plan, err)
		}
		if o := plan.DataLossEstimate.Objective; o == nil || o.Status != want {
			t.Errorf("rpo %s: %+v, want %s", rpo, o, want)
		}
	}
}

func TestPlanPITRWithEstimate_LSNTargetIsNotComparable(t *testing.T) {
	objectiveInfra(t, "15m", nil)
	_, _ = liveEnv(t)
	plan, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetLSN, RecoveryTargetVal: "0/4000000"})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("%+v, %v", plan, err)
	}
	if o := plan.DataLossEstimate.Objective; o == nil || o.Status != RPONotComparable {
		t.Errorf("objective = %+v", o)
	}
}

// What the restore reports it discarded is checked against the objective too:
// the figure to compare with the one that was approved.
func TestRestoreToPIT_ReportsTheLossAgainstTheObjective(t *testing.T) {
	objectiveInfra(t, "15m", nil)
	_, _ = liveEnv(t)
	plan := approvedLiveDigest(t, liveTarget)
	res, err := restoreToPITImpl(context.Background(), liveArgs(plan))
	if err != nil {
		t.Fatal(err)
	}
	if res.DataLoss == nil || res.DataLoss.Objective == nil || res.DataLoss.Objective.Status != RPOExceeds {
		t.Errorf("data_loss = %+v", res.DataLoss)
	}
}

// Without infrastructure config there is no database to ask about, and the
// answer says so rather than being absent.
func TestEstimate_WithoutInfrastructureConfig(t *testing.T) {
	infraConfig = nil
	est := withObjective(&DataLossEstimate{WindowSeconds: i64(60)}, "anything")
	if est.Objective == nil || est.Objective.Status != RPONoObjective {
		t.Errorf("objective = %+v", est.Objective)
	}
	if withObjective(nil, "x") != nil {
		t.Error("a nil estimate gained a comparison")
	}
}
