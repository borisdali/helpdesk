package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/infra"
	"helpdesk/testing/faultlib"
)

// enforced sets pgbackrest_db's environment rpo and its own enforcement. An
// empty enforcement leaves it unset (which means warn).
func enforced(t *testing.T, rpo, enforcement string) {
	t.Helper()
	objectiveInfra(t, rpo, func(db *infra.DBServer, _ *infra.Config) {
		if enforcement != "" {
			db.Objectives = &infra.Objectives{Enforcement: &infra.Choice{Value: enforcement}}
		}
	})
}

func objWith(rpo time.Duration, enforcement string) infra.ResolvedObjectives {
	o := rpoOf(rpo, "environment:prod")
	if enforcement != "" {
		o.Enforcement = infra.ResolvedChoice{Set: true, Value: enforcement, Source: "database"}
	}
	return o
}

// invalidRPO is an rpo the config tried to set and got wrong, under an enforcement.
func invalidRPO(enforcement string) infra.ResolvedObjectives {
	o := infra.ResolvedObjectives{RPOInvalid: true, Problems: []string{`db_servers.a.objectives: invalid duration "soon"`}}
	if enforcement != "" {
		o.Enforcement = infra.ResolvedChoice{Set: true, Value: enforcement, Source: "database"}
	}
	return o
}

// What each verdict demands of the approver under each enforcement. Verdicts
// with nothing to compare are shown, not enforced.
func TestGateFor_VerdictByEnforcement(t *testing.T) {
	over := &DataLossEstimate{WindowSeconds: i64(4687)}
	overBound := &DataLossEstimate{WindowSeconds: i64(4687), UpperBound: true}
	under := &DataLossEstimate{WindowSeconds: i64(60)}
	lsn := &DataLossEstimate{}
	rpo := 15 * time.Minute

	for _, c := range []struct {
		name string
		est  *DataLossEstimate
		obj  infra.ResolvedObjectives
		enf  string
		gate string
	}{
		{"exceeds, warn", over, objWith(rpo, "warn"), "warn", ""},
		{"exceeds, unset enforcement is warn", over, objWith(rpo, ""), "warn", ""},
		{"exceeds, acknowledge", over, objWith(rpo, "acknowledge"), "acknowledge", GateAcknowledge},
		{"exceeds, refuse", over, objWith(rpo, "refuse"), "refuse", GateRefuse},
		// An upper bound is not proof: refuse never refuses on it, it asks.
		{"may exceed, warn", overBound, objWith(rpo, "warn"), "warn", ""},
		{"may exceed, acknowledge", overBound, objWith(rpo, "acknowledge"), "acknowledge", GateAcknowledge},
		{"may exceed, refuse asks, does not refuse", overBound, objWith(rpo, "refuse"), "refuse", GateAcknowledge},
		{"within, refuse", under, objWith(rpo, "refuse"), "refuse", ""},
		{"not comparable, refuse", lsn, objWith(rpo, "refuse"), "refuse", ""},
		{"no objective, refuse", over, infra.ResolvedObjectives{Enforcement: infra.ResolvedChoice{Set: true, Value: "refuse"}}, "refuse", ""},
		// An objective that could not be read is not "nothing to check": a typo
		// must not switch a strict enforcement off. It asks and never refuses,
		// since nothing is known to be breached.
		{"objective invalid, warn", over, invalidRPO("warn"), "warn", ""},
		{"objective invalid, unset enforcement", over, invalidRPO(""), "warn", ""},
		{"objective invalid, acknowledge", over, invalidRPO("acknowledge"), "acknowledge", GateAcknowledge},
		{"objective invalid, refuse asks, does not refuse", over, invalidRPO("refuse"), "refuse", GateAcknowledge},
		// A problem with another setting, and no rpo at all, remain unenforced.
		{"a problem elsewhere, refuse", over, infra.ResolvedObjectives{Problems: []string{"rto bad"}, Enforcement: infra.ResolvedChoice{Set: true, Value: "refuse"}}, "refuse", ""},
	} {
		got := compareToRPO(c.est, c.obj, true)
		if got.Gate != c.gate || got.Enforcement != c.enf {
			t.Errorf("%s: status=%s enforcement=%s gate=%q, want enforcement=%s gate=%q", c.name, got.Status, got.Enforcement, got.Gate, c.enf, c.gate)
		}
	}
	// A target that is not a configured database has no enforcement at all.
	if got := compareToRPO(over, infra.ResolvedObjectives{}, false); got.Gate != "" || got.Enforcement != "" {
		t.Errorf("not found: %+v", got)
	}
}

func TestObjectiveGate_AcknowledgementOnlyHelpsWhereItCan(t *testing.T) {
	ack := &RPOComparison{Status: RPOExceeds, Enforcement: "acknowledge", Gate: GateAcknowledge, Detail: "d"}
	if ack.objectiveGate(false) == nil || ack.objectiveGate(true) != nil {
		t.Error("acknowledge: refused without the acknowledgement, allowed with it")
	}
	// Refuse is refuse: acknowledging does not unlock it.
	ref := &RPOComparison{Status: RPOExceeds, Enforcement: "refuse", Gate: GateRefuse, Detail: "d"}
	for _, a := range []bool{false, true} {
		err := ref.objectiveGate(a)
		if err == nil || !strings.Contains(err.Error(), "is refused") {
			t.Errorf("refuse with ack=%v: %v", a, err)
		}
	}
	for _, c := range []*RPOComparison{nil, {}, {Status: RPOWithin}} {
		if c.objectiveGate(false) != nil {
			t.Errorf("%+v demanded something", c)
		}
	}
	// The message says which it is, and how to proceed.
	msg := (&RPOComparison{Status: RPOMayExceed, Enforcement: "acknowledge", Gate: GateAcknowledge, Detail: "d"}).objectiveGate(false).Error()
	if !strings.Contains(msg, "may exceed") || !strings.Contains(msg, "--acknowledge-objective-breach") {
		t.Errorf("message = %q", msg)
	}
}

// The plan an operator reads says what approving will demand.
func TestPlanPITRWithEstimate_SaysWhatTheGateWillDemand(t *testing.T) {
	args := PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget}
	for enf, wantGate := range map[string]string{"": "", "warn": "", "acknowledge": GateAcknowledge, "refuse": GateRefuse} {
		enforced(t, "15m", enf)
		_, _ = liveEnv(t)
		plan, err := planPITRWithEstimate(context.Background(), args)
		if err != nil || plan.Status != PITRFeasible {
			t.Fatalf("%q: %+v, %v", enf, plan, err)
		}
		o := plan.DataLossEstimate.Objective
		if o.Status != RPOExceeds || o.Gate != wantGate {
			t.Errorf("enforcement %q: status=%s gate=%q, want exceeds / %q", enf, o.Status, o.Gate, wantGate)
		}
	}
}

// The keys the harness and helpdesk-client read to learn what approving will
// demand are the ones the agent writes: a renamed field would otherwise read as
// "demands nothing" on the other side.
func TestPlanJSON_CarriesTheGateTheHarnessAndClientRead(t *testing.T) {
	enforced(t, "15m", "acknowledge")
	_, _ = liveEnv(t)
	plan, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("%+v, %v", plan, err)
	}
	raw, _ := json.Marshal(plan)

	var view faultlib.PITRPlanView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	o := view.DataLossEstimate.Objective
	if o == nil || o.Status != RPOExceeds || o.Gate != GateAcknowledge || o.Enforcement != "acknowledge" || o.Detail == "" {
		t.Fatalf("the harness reads %+v from %s", o, raw)
	}

	// The names the client's own struct reads.
	var m struct {
		E struct {
			O map[string]any `json:"objective"`
		} `json:"data_loss_estimate"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"status", "detail", "gate", "enforcement"} {
		if _, ok := m.E.O[k]; !ok {
			t.Errorf("objective has no %q: %s", k, raw)
		}
	}
}

// ── the restore ──────────────────────────────────────────────────────────────

func restoreWith(t *testing.T, enforcement string, ack bool) (*liveSim, RestoreToPITResult, error) {
	t.Helper()
	enforced(t, "15m", enforcement)
	sim, _ := liveEnv(t)
	plan := approvedLiveDigest(t, liveTarget)
	args := liveArgs(plan)
	args.AcknowledgeObjectiveBreach = ack
	res, err := restoreToPITImpl(context.Background(), args)
	return sim, res, err
}

// The simulated instance is 8 days 10 hours past liveTarget, against an rpo of
// 15 minutes. The breach is only ever refused BEFORE the instance is stopped.
func TestRestoreToPIT_EnforcesTheObjective(t *testing.T) {
	t.Run("warn: goes ahead without any acknowledgement", func(t *testing.T) {
		sim, res, err := restoreWith(t, "warn", false)
		if err != nil || !sim.did("restore") {
			t.Fatalf("err=%v order=%s", err, sim.order())
		}
		if o := res.DataLoss.Objective; o.Status != RPOExceeds || o.Gate != "" {
			t.Errorf("objective = %+v", o)
		}
	})
	t.Run("unset enforcement is warn", func(t *testing.T) {
		sim, _, err := restoreWith(t, "", false)
		if err != nil || !sim.did("restore") {
			t.Fatalf("err=%v order=%s", err, sim.order())
		}
	})
	t.Run("acknowledge, not acknowledged: refused, instance untouched", func(t *testing.T) {
		sim, res, err := restoreWith(t, "acknowledge", false)
		if err == nil || !strings.Contains(err.Error(), "acknowledge_objective_breach") || !strings.Contains(err.Error(), "The instance was not stopped") {
			t.Fatalf("err = %v", err)
		}
		// It had asked the instance and flushed the archive, and stopped nothing.
		if sim.did("cleanstop") || sim.did("stop") || sim.did("restore") || !sim.up {
			t.Errorf("order=%s up=%v: the instance was touched", sim.order(), sim.up)
		}
		if res.DataLoss == nil || res.DataLoss.Objective.Gate != GateAcknowledge {
			t.Errorf("the refusal should still report what it measured: %+v", res.DataLoss)
		}
	})
	t.Run("acknowledge, acknowledged: goes ahead", func(t *testing.T) {
		sim, res, err := restoreWith(t, "acknowledge", true)
		if err != nil || !sim.did("stop") || !sim.did("restore") {
			t.Fatalf("err=%v order=%s", err, sim.order())
		}
		if o := res.DataLoss.Objective; o.Status != RPOExceeds || o.Gate != GateAcknowledge {
			t.Errorf("objective = %+v", o)
		}
	})
	t.Run("refuse: refused even when acknowledged", func(t *testing.T) {
		for _, ack := range []bool{false, true} {
			sim, _, err := restoreWith(t, "refuse", ack)
			if err == nil || !strings.Contains(err.Error(), "is refused") || sim.did("stop") || sim.did("restore") || !sim.up {
				t.Errorf("ack=%v: err=%v order=%s up=%v", ack, err, sim.order(), sim.up)
			}
		}
	})
}

// A down instance is a destructive restore too, so it is enforced too. Its
// figure is an upper bound, so refuse asks and does not refuse.
func TestRestoreToPIT_DownInstanceIsEnforcedToo(t *testing.T) {
	run := func(enforcement string, ack bool) (*liveSim, error) {
		enforced(t, "1m", enforcement)
		sim, _ := liveEnv(t)
		sim.up = false
		args := PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"}
		plan, err := planPITRImpl(context.Background(), args)
		if err != nil || plan.Status != PITRFeasible {
			t.Fatalf("plan: %+v, %v", plan, err)
		}
		_, err = restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "pgbackrest_db", Stanza: "main",
			RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z", PlanDigest: plan.PlanDigest, AcknowledgeObjectiveBreach: ack})
		return sim, err
	}
	for _, enf := range []string{"acknowledge", "refuse"} {
		sim, err := run(enf, false)
		if err == nil || !strings.Contains(err.Error(), "may exceed") || !strings.Contains(err.Error(), "Nothing was changed") || sim.did("restore") {
			t.Errorf("%s, not acknowledged: err=%v order=%s", enf, err, sim.order())
		}
		if sim, err := run(enf, true); err != nil || !sim.did("restore") {
			t.Errorf("%s, acknowledged: err=%v order=%s", enf, err, sim.order())
		}
	}
	if sim, err := run("warn", false); err != nil || !sim.did("restore") {
		t.Errorf("warn: err=%v order=%s", err, sim.order())
	}
}

// An objective that could not be read is not "nothing to check". Under a strict
// enforcement the restore asks for the acknowledgement (it does not refuse: no
// breach is known), so a typo cannot quietly switch the check off.
func TestRestoreToPIT_AnObjectiveThatCouldNotBeReadStillAsks(t *testing.T) {
	restore := func(setup func(db *infra.DBServer, c *infra.Config), ack bool) (*liveSim, RestoreToPITResult, error) {
		objectiveInfra(t, "", setup)
		sim, _ := liveEnv(t)
		plan := approvedLiveDigest(t, liveTarget)
		args := liveArgs(plan)
		args.AcknowledgeObjectiveBreach = ack
		res, err := restoreToPITImpl(context.Background(), args)
		return sim, res, err
	}
	strict := func(enf string) *infra.Objectives {
		return &infra.Objectives{Enforcement: &infra.Choice{Value: enf}}
	}
	badRPO := func(enf string) func(*infra.DBServer, *infra.Config) {
		return func(db *infra.DBServer, _ *infra.Config) {
			db.Objectives = strict(enf)
			db.Objectives.RPO = &infra.Duration{Invalid: `invalid duration "soon"`}
		}
	}

	for _, enf := range []string{"acknowledge", "refuse"} {
		sim, res, err := restore(badRPO(enf), false)
		if err == nil || !strings.Contains(err.Error(), "cannot be checked against the recovery objective") ||
			!strings.Contains(err.Error(), "acknowledge_objective_breach") || sim.did("stop") || !sim.up {
			t.Fatalf("%s, not acknowledged: err=%v order=%s up=%v", enf, err, sim.order(), sim.up)
		}
		if res.DataLoss.Objective.Status != RPOObjectiveInvalid || res.DataLoss.Objective.Gate != GateAcknowledge {
			t.Errorf("%s: objective = %+v", enf, res.DataLoss.Objective)
		}
		if sim, _, err := restore(badRPO(enf), true); err != nil || !sim.did("restore") {
			t.Errorf("%s, acknowledged: err=%v order=%s", enf, err, sim.order())
		}
	}
	// warn says so and goes ahead.
	if sim, res, err := restore(badRPO("warn"), false); err != nil || !sim.did("restore") || res.DataLoss.Objective.Status != RPOObjectiveInvalid {
		t.Errorf("warn: err=%v order=%s", err, sim.order())
	}

	// A misspelt environment is the same: the database cannot tell what its
	// environment required, and its enforcement falls toward caution on its own.
	typo := func(db *infra.DBServer, _ *infra.Config) { db.Environment = "prdo" }
	sim, _, err := restore(typo, false)
	if err == nil || !strings.Contains(err.Error(), "cannot be checked") || sim.did("stop") {
		t.Fatalf("a misspelt environment: err=%v order=%s", err, sim.order())
	}
	if sim, _, err := restore(typo, true); err != nil || !sim.did("restore") {
		t.Errorf("a misspelt environment, acknowledged: err=%v order=%s", err, sim.order())
	}

	// A problem with some other setting, and no rpo anywhere, is not an invalid
	// rpo: under refuse the restore goes ahead, with nothing to enforce.
	elsewhere := func(db *infra.DBServer, c *infra.Config) {
		db.Objectives = strict("refuse")
		c.Defaults = &infra.Defaults{Objectives: infra.Objectives{RTO: &infra.Duration{}}}
	}
	if sim, res, err := restore(elsewhere, false); err != nil || !sim.did("restore") || res.DataLoss.Objective.Status != RPONoObjective {
		t.Errorf("a problem elsewhere: err=%v order=%s %+v", err, sim.order(), res.DataLoss)
	}
}

// The loss is measured at the stop, not at the plan. A plan that said "within"
// can be approved later, after the instance has gone on committing, and the
// restore must see that.
func TestRestoreToPIT_MeasuresTheLossAtTheStopNotThePlan(t *testing.T) {
	// The simulated instance's clock reads 10:00:00 at the probe. A target ten
	// minutes earlier is within an rpo of 15 minutes then.
	const target = "2026-10-28T09:50:00Z"
	args := PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: target}
	probe := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) // the local clock; only differences matter

	run := func(passed time.Duration, ack bool) (*liveSim, RestoreToPITResult, error) {
		enforced(t, "15m", "acknowledge")
		sim, _ := liveEnv(t)
		plan, err := planPITRWithEstimate(context.Background(), args)
		if err != nil || plan.Status != PITRFeasible {
			t.Fatalf("plan: %+v, %v", plan, err)
		}
		if o := plan.DataLossEstimate.Objective; o.Status != RPOWithin || o.Gate != "" {
			t.Fatalf("at plan time the loss should be within the objective: %+v", o)
		}
		// The first reading is the probe; every later one is the stop.
		n := 0
		old := restoreClock
		restoreClock = func() time.Time {
			n++
			if n == 1 {
				return probe
			}
			return probe.Add(passed)
		}
		t.Cleanup(func() { restoreClock = old })
		res, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "pgbackrest_db", Stanza: "main",
			RecoveryTargetType: TargetTime, RecoveryTargetVal: target, PlanDigest: plan.PlanDigest,
			AllowStopServing: true, AcknowledgeObjectiveBreach: ack})
		return sim, res, err
	}

	// Two minutes pass between probing and stopping (the archive flush): 12
	// minutes of loss, still within.
	if sim, res, err := run(2*time.Minute, false); err != nil || !sim.did("restore") || res.DataLoss.Objective.Status != RPOWithin {
		t.Errorf("within: err=%v order=%s %+v", err, sim.order(), res.DataLoss)
	}
	// Ten minutes pass: 20 minutes of loss. The plan said within; the restore
	// finds it exceeds, and refuses before stopping anything.
	sim, res, err := run(10*time.Minute, false)
	if err == nil || !strings.Contains(err.Error(), "exceeds the recovery objective") || sim.did("stop") || !sim.up {
		t.Fatalf("drifted past the objective: err=%v order=%s up=%v", err, sim.order(), sim.up)
	}
	if res.DataLoss == nil || res.DataLoss.WindowSeconds == nil || *res.DataLoss.WindowSeconds != 20*60 {
		t.Errorf("window = %+v, want 20m (the 10m at the probe plus the 10m since)", res.DataLoss)
	}
	// And acknowledging it lets it through.
	if sim, _, err := run(10*time.Minute, true); err != nil || !sim.did("restore") {
		t.Errorf("acknowledged: err=%v order=%s", err, sim.order())
	}
}

func TestProjectPreStop(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	pre := LivePreState{Time: "2026-10-28T10:00:00Z", LSN: "0/20000000"}
	got := projectPreStop(pre, at, at.Add(90*time.Second))
	if got.Time != "2026-10-28T10:01:30Z" || got.LSN != pre.LSN {
		t.Errorf("projected = %+v", got)
	}
	// Nothing to project from, or a clock that went backwards: left as probed.
	for name, c := range map[string]struct {
		pre      LivePreState
		from, to time.Time
	}{
		"unreadable time": {LivePreState{Time: "garbage"}, at, at.Add(time.Minute)},
		"no probe time":   {pre, time.Time{}, at},
		"clock backwards": {pre, at, at.Add(-time.Minute)},
	} {
		if got := projectPreStop(c.pre, c.from, c.to); got != c.pre {
			t.Errorf("%s: %+v changed to %+v", name, c.pre, got)
		}
	}
}

// The acknowledgement comes from the approval in the request and from nowhere
// else: the model-facing tool takes no such argument.
func TestRestoreToPITTool_BreachAcknowledgementComesOnlyFromTheApproval(t *testing.T) {
	for _, ack := range []bool{true, false} {
		enforced(t, "15m", "acknowledge")
		sim, _ := liveEnv(t)
		plan := approvedLiveDigest(t, liveTarget)
		ap := approvalFor(plan, true)
		ap.BreachAcknowledged = ack
		_, err := restoreToPITTool(toolCtxWithApproval(ap), RestoreToPITModelArgs{Target: "pgbackrest_db", Stanza: "main"})
		if ack {
			if err != nil || !sim.did("restore") {
				t.Errorf("acknowledged: err=%v order=%s", err, sim.order())
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "acknowledge_objective_breach") || sim.did("stop") {
			t.Errorf("not acknowledged: err=%v order=%s", err, sim.order())
		}
	}
}
