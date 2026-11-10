package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
	"helpdesk/playbooks"
	"helpdesk/testing/faultlib"
)

// ledgerCalls records what the budget check asked the ledger for.
type ledgerCalls struct {
	keys    []string
	budgets []infra.AvailabilityBudget
}

// withLedger makes the ledger say that this much of the downtime budget has been
// spent by one resolved outage, or that it cannot be read.
func withLedger(t *testing.T, spent time.Duration, readErr error) *ledgerCalls {
	t.Helper()
	old := budgetLedgerFn
	t.Cleanup(func() { budgetLedgerFn = old })
	calls := &ledgerCalls{}
	budgetLedgerFn = func(_ context.Context, key string, b infra.AvailabilityBudget, now time.Time) (*availability.Ledger, error) {
		calls.keys, calls.budgets = append(calls.keys, key), append(calls.budgets, b)
		if readErr != nil {
			return nil, readErr
		}
		var incs []availability.Incident
		if spent > 0 {
			incs = append(incs, availability.Incident{ID: "inc_spent", Origin: audit.IncidentOriginReal, Status: audit.IncidentStatusResolved,
				SeriesIDs:  []string{"pbs_pgbackrest_restore_remediate"},
				DetectedAt: now.Add(-3 * time.Hour), ResolvedAt: now.Add(-3*time.Hour + spent)})
		}
		l := availability.Compute(availability.Params{Database: key, Budget: b, Incidents: incs, Now: now, Classify: playbooks.AvailabilityImpact})
		return &l, nil
	}
	return calls
}

// budgetObjectives gives the test database an availability objective: 99.9% over
// the default 30 days (43m12s), a 30m rto, and this availability enforcement.
func budgetObjectives(t *testing.T, rpo, enforcement string) {
	t.Helper()
	objectiveInfra(t, rpo, func(db *infra.DBServer, _ *infra.Config) {
		o := &infra.Objectives{
			RTO:             &infra.Duration{Duration: 30 * time.Minute},
			AvailabilitySLO: &infra.Percent{Value: 99.9},
		}
		if enforcement != "" {
			o.AvailabilityEnforcement = &infra.Choice{Value: enforcement}
		}
		db.Objectives = o
	})
}

func TestBudgetCheckFor_WhatCannotBeChecked(t *testing.T) {
	calls := withLedger(t, 0, nil)
	infraConfig = nil
	if v := budgetCheckFor(context.Background(), "pgbackrest_db", true); v.Status != availability.BudgetNoObjective {
		t.Errorf("no infra config = %s", v.Status)
	}
	objectiveInfra(t, "15m", nil) // an rpo, but no availability objective
	if v := budgetCheckFor(context.Background(), "nope", true); v.Status != availability.BudgetNoObjective || !strings.Contains(v.Detail, "not a configured database") {
		t.Errorf("an unknown target = %s (%s)", v.Status, v.Detail)
	}
	if v := budgetCheckFor(context.Background(), "pgbackrest_db", true); v.Status != availability.BudgetNoObjective || !strings.Contains(v.Detail, "no availability SLO") {
		t.Errorf("no SLO = %s (%s)", v.Status, v.Detail)
	}
	// With nothing to read the spend against, the audit service is not asked.
	if len(calls.keys) != 0 {
		t.Errorf("the ledger was read %d times for a database with no budget", len(calls.keys))
	}
}

func TestBudgetCheckFor_ReadsTheLedgerOfTheDatabaseTheRestoreRunsOn(t *testing.T) {
	calls := withLedger(t, 20*time.Minute, nil)
	budgetObjectives(t, "", "acknowledge")

	// Named by container name, as resolveHost accepts: the same database, the same ledger.
	for _, target := range []string{"pgbackrest_db", "helpdesk-test-pg-pgbackrest"} {
		v := budgetCheckFor(context.Background(), target, true)
		if v.Status != availability.BudgetMayExceed || v.Gate != availability.GateAcknowledge || v.Enforcement != infra.EnforceAcknowledge {
			t.Errorf("%s: %+v", target, v)
		}
		if *v.BudgetSeconds != 2592 || *v.RemainingSeconds != 2592-1200 || *v.RTOSeconds != 1800 {
			t.Errorf("%s: numbers = %v / %v / %v", target, *v.BudgetSeconds, *v.RemainingSeconds, *v.RTOSeconds)
		}
	}
	for _, k := range calls.keys {
		if k != "pgbackrest_db" {
			t.Errorf("the ledger was read for %q", k)
		}
	}
	if !calls.budgets[0].Computed || calls.budgets[0].Window != 30*24*time.Hour || calls.budgets[0].WindowSource != infra.SourceBuiltin {
		t.Errorf("the ledger was given budget %+v", calls.budgets[0])
	}
}

func TestBudgetCheckFor_ServingDecidesWhetherARefuseRefuses(t *testing.T) {
	withLedger(t, 50*time.Minute, nil) // spent: the budget is gone
	budgetObjectives(t, "", "refuse")
	serving := budgetCheckFor(context.Background(), "pgbackrest_db", true)
	if serving.Status != availability.BudgetExceeds || serving.Gate != availability.GateRefuse {
		t.Errorf("serving: %+v", serving)
	}
	down := budgetCheckFor(context.Background(), "pgbackrest_db", false)
	if down.Status != availability.BudgetExceeds || down.Gate != availability.GateAcknowledge || !strings.Contains(down.GateNote, "already down") {
		t.Errorf("down: %+v", down)
	}
}

func TestBudgetCheckFor_ProblemsAreSaidAndTheLedgerIsNotAsked(t *testing.T) {
	calls := withLedger(t, 0, nil)
	objectiveInfra(t, "", func(db *infra.DBServer, _ *infra.Config) {
		db.Objectives = &infra.Objectives{
			AvailabilitySLO:         &infra.Percent{Value: 99.9},
			AvailabilityWindow:      &infra.Duration{Invalid: `invalid duration "a month"`},
			AvailabilityEnforcement: &infra.Choice{Value: "refuse"},
		}
	})
	v := budgetCheckFor(context.Background(), "pgbackrest_db", true)
	if v.Status != availability.BudgetObjectiveInvalid || v.Gate != availability.GateAcknowledge || len(v.Problems) == 0 {
		t.Errorf("%+v", v)
	}
	if len(calls.keys) != 0 {
		t.Error("the ledger was read although the window could not be")
	}
}

func TestBudgetCheckFor_ALedgerThatCannotBeReadAsksAndDoesNotRefuse(t *testing.T) {
	withLedger(t, 0, errors.New("auditd returned 503: starting"))
	budgetObjectives(t, "", "refuse")
	v := budgetCheckFor(context.Background(), "pgbackrest_db", true)
	if v.Status != availability.BudgetLedgerUnavailable || v.Gate != availability.GateAcknowledge || !strings.Contains(v.Detail, "503") {
		t.Errorf("%+v", v)
	}
	// With no audit service configured at all, the same.
	budgetLedgerFn = func(ctx context.Context, key string, b infra.AvailabilityBudget, now time.Time) (*availability.Ledger, error) {
		return ledgerClient.Ledger(ctx, key, b, now) // the real one, with no client
	}
	ledgerClient = nil
	if v := budgetCheckFor(context.Background(), "pgbackrest_db", true); v.Status != availability.BudgetLedgerUnavailable || !strings.Contains(v.Detail, "no audit service") {
		t.Errorf("no client: %+v", v)
	}
}

// ── the gate the restore applies ──────────────────────────────────────────────

func rpoGate(gate string) *RPOComparison {
	return &RPOComparison{Status: RPOExceeds, Detail: "an rpo breach", Enforcement: "x", Gate: gate}
}

func budgetGate(status, gate string) *availability.BudgetVerdict {
	return &availability.BudgetVerdict{Status: status, Detail: "a budget breach", Enforcement: "y", Gate: gate}
}

func TestRestoreGate(t *testing.T) {
	ack, refuse := GateAcknowledge, GateRefuse
	tests := []struct {
		name  string
		rpo   *RPOComparison
		bud   *availability.BudgetVerdict
		acked bool
		want  []string // fragments of the error; nil means it may go ahead
	}{
		{"nothing demanded", nil, nil, false, nil},
		{"nothing demanded by either", rpoGate(""), budgetGate("within", ""), false, nil},
		{"the rpo alone, not acknowledged: its own message, unchanged", rpoGate(ack), nil, false, []string{"discards exceeds the recovery objective", "acknowledge_objective_breach"}},
		{"the rpo alone, acknowledged", rpoGate(ack), nil, true, nil},
		{"the budget alone, not acknowledged", nil, budgetGate("exceeds", ack), false, []string{"the downtime this restore causes exceeds the availability budget", "acknowledge_objective_breach"}},
		{"the budget alone, acknowledged", nil, budgetGate("exceeds", ack), true, nil},
		{"may exceed is worded as may", nil, budgetGate("may_exceed", ack), false, []string{"may exceed the availability budget"}},
		{"an unreadable objective is worded as unreadable", nil, budgetGate("objective_invalid", ack), false, []string{"whose objective could not be read"}},
		{"an unreadable spend is worded as unreadable", nil, budgetGate("ledger_unavailable", ack), false, []string{"what has been spent could not be read"}},
		{"both demand it, neither acknowledged: both are named", rpoGate(ack), budgetGate("exceeds", ack), false, []string{"discards exceeds the recovery objective", "Also: the downtime this restore causes"}},
		{"both demand it, one acknowledgement covers both", rpoGate(ack), budgetGate("exceeds", ack), true, nil},
		{"a budget refusal stands though acknowledged", nil, budgetGate("exceeds", refuse), true, []string{"the restore is refused", "availability enforcement is \"y\""}},
		{"an rpo refusal stands though acknowledged", rpoGate(refuse), nil, true, []string{"the restore is refused"}},
		{"a refusal comes first, even when the other only asks", rpoGate(ack), budgetGate("exceeds", refuse), false, []string{"the restore is refused", "availability enforcement"}},
		{"and the other way round", rpoGate(refuse), budgetGate("exceeds", ack), false, []string{"enforcement is \"x\"", "the restore is refused"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := restoreGate(tc.rpo, tc.bud, tc.acked)
			if tc.want == nil {
				if err != nil {
					t.Errorf("err = %v, want it to go ahead", err)
				}
				return
			}
			if err == nil {
				t.Fatal("it went ahead")
			}
			for _, frag := range tc.want {
				if !strings.Contains(err.Error(), frag) {
					t.Errorf("error %q lacks %q", err, frag)
				}
			}
		})
	}
	// A refusal that names the budget does not name a data-loss breach it did not have.
	if err := restoreGate(nil, budgetGate("exceeds", refuse), true); err == nil || strings.Contains(err.Error(), "discards") {
		t.Errorf("the budget refusal talks about discarded data: %v", err)
	}
}

func TestBreachesToAcknowledge(t *testing.T) {
	got := breachesToAcknowledge(rpoGate(GateAcknowledge), budgetGate("exceeds", GateAcknowledge))
	if strings.Join(got, ",") != "rpo,availability_budget" {
		t.Errorf("both = %v", got)
	}
	if got := breachesToAcknowledge(rpoGate(GateAcknowledge), budgetGate("within", "")); strings.Join(got, ",") != "rpo" {
		t.Errorf("rpo only = %v", got)
	}
	if got := breachesToAcknowledge(nil, budgetGate("may_exceed", GateAcknowledge)); strings.Join(got, ",") != "availability_budget" {
		t.Errorf("budget only = %v", got)
	}
	// A refusal is not something an acknowledgement covers.
	if got := breachesToAcknowledge(rpoGate(GateRefuse), budgetGate("exceeds", GateRefuse)); len(got) != 0 {
		t.Errorf("refusals listed as acknowledgeable: %v", got)
	}
	if got := breachesToAcknowledge(nil, nil); len(got) != 0 {
		t.Errorf("nothing = %v", got)
	}
}

// ── the plan ──────────────────────────────────────────────────────────────────

func TestPlan_CarriesTheAvailabilityBudget(t *testing.T) {
	withLedger(t, 20*time.Minute, nil)
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
	if b == nil || b.Status != availability.BudgetMayExceed || b.Gate != availability.GateAcknowledge || !plan.InstanceServing {
		t.Fatalf("plan budget = %+v (serving %v)", b, plan.InstanceServing)
	}
	// It is in the JSON under the name the CLI and the harness read.
	raw, _ := json.Marshal(plan)
	var view struct {
		Budget *struct {
			Status string `json:"status"`
			Gate   string `json:"gate"`
		} `json:"availability_budget"`
	}
	if json.Unmarshal(raw, &view) != nil || view.Budget == nil || view.Budget.Status != "may_exceed" || view.Budget.Gate != "acknowledge" {
		t.Errorf("json = %s", raw)
	}
	// And it is on the record the plan wrote.
	got := decisions(t, store)[audit.RecoveryStagePlan]
	if len(got) != 1 || len(got[0].Options) != 1 {
		t.Fatalf("plan decisions = %+v", got)
	}
	o := got[0].Options[0]
	if o.BudgetVerdict != "may_exceed" || o.BudgetGate != "acknowledge" || o.BudgetRemainingSeconds == nil || *o.BudgetRemainingSeconds != 2592-1200 || o.BudgetVerdictDetail == "" {
		t.Errorf("recorded option = %+v", o)
	}
	if got[0].Objectives == nil || got[0].Objectives.AvailabilityEnforcement != "acknowledge" {
		t.Errorf("recorded objectives = %+v", got[0].Objectives)
	}
	// The digest is not moved by it: what is left of a budget changes while a plan waits.
	withLedger(t, 5*time.Minute, nil)
	again, _ := planPITRImpl(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if again.PlanDigest != plan.PlanDigest {
		t.Errorf("the plan digest moved with the budget: %s vs %s", plan.PlanDigest, again.PlanDigest)
	}
}

// The names the harness and helpdesk-client read to learn what approving will
// demand for the budget are the ones the agent writes: a renamed field would
// otherwise read as "demands nothing" on the other side.
func TestPlanJSON_CarriesTheBudgetGateTheHarnessAndClientRead(t *testing.T) {
	withLedger(t, 50*time.Minute, nil)
	budgetObjectives(t, "", "acknowledge")
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
	b := view.AvailabilityBudget
	if b == nil || b.Status != availability.BudgetExceeds || b.Gate != availability.GateAcknowledge || b.Enforcement != "acknowledge" || b.Detail == "" {
		t.Fatalf("the harness reads %+v from %s", b, raw)
	}

	// The names the client's own struct reads.
	var m struct {
		B map[string]any `json:"availability_budget"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"status", "detail", "gate", "enforcement", "remaining_seconds"} {
		if _, ok := m.B[k]; !ok {
			t.Errorf("the plan has no availability_budget.%s, which helpdesk-client reads: %s", k, raw)
		}
	}
	// A plan for a database with no availability objective carries a verdict that
	// says so (not a missing key that would read as "fine").
	objectiveInfra(t, "15m", nil)
	again, _ := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if again.AvailabilityBudget == nil || again.AvailabilityBudget.Status != availability.BudgetNoObjective {
		t.Errorf("no objective: %+v", again.AvailabilityBudget)
	}
}

// ── the restore ───────────────────────────────────────────────────────────────

// runRestore runs the restore on the live fixture and returns what it did.
func runRestore(t *testing.T, ack bool) (res RestoreToPITResult, decision audit.RecoveryDecision, stopped bool, err error) {
	t.Helper()
	sim, _ := liveEnv(t)
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()
	plan := approvedLiveDigest(t, liveTarget)
	args := liveArgs(plan)
	args.AcknowledgeObjectiveBreach = ack
	res, err = restoreToPITImpl(context.Background(), args)
	got := decisions(t, store)[audit.RecoveryStageExecution]
	if len(got) != 1 {
		t.Fatalf("%d execution decisions: %+v", len(got), got)
	}
	return res, got[0], sim.did("stop"), err
}

func TestRestore_RefusesASpentBudgetUnderRefuse_EvenAcknowledged(t *testing.T) {
	for _, ack := range []bool{false, true} {
		withLedger(t, 50*time.Minute, nil)
		budgetObjectives(t, "", "refuse")
		res, d, stopped, err := runRestore(t, ack)
		if err == nil || !strings.Contains(err.Error(), "refusing to restore") || !strings.Contains(err.Error(), "the restore is refused") ||
			!strings.Contains(err.Error(), "The instance was not stopped") || stopped {
			t.Errorf("ack=%v: err=%v stopped=%v", ack, err, stopped)
		}
		if res.AvailabilityBudget == nil || res.AvailabilityBudget.Status != availability.BudgetExceeds || res.AvailabilityBudget.Gate != availability.GateRefuse {
			t.Errorf("ack=%v: result budget = %+v", ack, res.AvailabilityBudget)
		}
		o := d.Options[0]
		if d.Decision != audit.RecoveryDecisionRefused || o.Chosen || o.BudgetVerdict != "exceeds" || o.BudgetGate != "refuse" || o.BudgetRemainingSeconds == nil || *o.BudgetRemainingSeconds >= 0 {
			t.Errorf("ack=%v: decision = %+v option = %+v", ack, d, o)
		}
		// A refusal is not an acknowledgeable breach.
		if len(d.Approval.Breaches) != 0 {
			t.Errorf("ack=%v: breaches = %v", ack, d.Approval.Breaches)
		}
	}
}

func TestRestore_AsksForTheAcknowledgementWhenTheBudgetMayBeExceeded(t *testing.T) {
	// Under refuse: a restore that only MAY exceed the budget asks and is never refused.
	for _, enforcement := range []string{"acknowledge", "refuse"} {
		for _, ack := range []bool{false, true} {
			withLedger(t, 20*time.Minute, nil)
			budgetObjectives(t, "", enforcement)
			_, d, stopped, err := runRestore(t, ack)
			if !ack {
				if err == nil || !strings.Contains(err.Error(), "may exceed the availability budget") || !strings.Contains(err.Error(), "acknowledge_objective_breach") || stopped {
					t.Errorf("%s, not acknowledged: err=%v stopped=%v", enforcement, err, stopped)
				}
				if d.Decision != audit.RecoveryDecisionRefused || strings.Join(d.Approval.Breaches, ",") != "availability_budget" {
					t.Errorf("%s: decision = %+v", enforcement, d)
				}
				continue
			}
			if err != nil || !stopped || d.Decision != audit.RecoveryDecisionProceeded || !d.Options[0].Chosen || !d.Approval.AcknowledgedBreach ||
				strings.Join(d.Approval.Breaches, ",") != "availability_budget" || d.Options[0].BudgetVerdict != "may_exceed" {
				t.Errorf("%s, acknowledged: err=%v stopped=%v decision=%+v", enforcement, err, stopped, d)
			}
		}
	}
}

func TestRestore_GoesAheadWhenThereIsRoomOrNothingIsEnforced(t *testing.T) {
	for name, tc := range map[string]struct {
		spent       time.Duration
		enforcement string
		verdict     string
	}{
		"room in the budget, refuse":   {5 * time.Minute, "refuse", "within"},
		"spent, but only warn":         {50 * time.Minute, "warn", "exceeds"},
		"spent, no enforcement is set": {50 * time.Minute, "", "exceeds"},
		"may exceed, only warn":        {20 * time.Minute, "warn", "may_exceed"},
	} {
		withLedger(t, tc.spent, nil)
		budgetObjectives(t, "", tc.enforcement)
		res, d, stopped, err := runRestore(t, false)
		if err != nil || !stopped || d.Decision != audit.RecoveryDecisionProceeded {
			t.Errorf("%s: err=%v stopped=%v decision=%+v", name, err, stopped, d)
			continue
		}
		// What was true is on record, whether or not it was enforced.
		if res.AvailabilityBudget.Status != tc.verdict || d.Options[0].BudgetVerdict != tc.verdict || d.Options[0].BudgetGate != "" || len(d.Approval.Breaches) != 0 {
			t.Errorf("%s: result %+v, option %+v, breaches %v", name, res.AvailabilityBudget, d.Options[0], d.Approval.Breaches)
		}
	}
}

func TestRestore_ASpendThatCannotBeReadAsks(t *testing.T) {
	withLedger(t, 0, errors.New("auditd unreachable"))
	budgetObjectives(t, "", "acknowledge")
	_, d, stopped, err := runRestore(t, false)
	if err == nil || !strings.Contains(err.Error(), "what has been spent could not be read") || stopped {
		t.Errorf("err=%v stopped=%v", err, stopped)
	}
	if d.Options[0].BudgetVerdict != "ledger_unavailable" {
		t.Errorf("option = %+v", d.Options[0])
	}
}

// One acknowledgement covers both breaches, and the record says there were two.
func TestRestore_OneAcknowledgementCoversTheRPOAndTheBudget(t *testing.T) {
	// A 15m rpo that the loss exceeds, and a spent budget, both under acknowledge.
	for _, ack := range []bool{false, true} {
		withLedger(t, 50*time.Minute, nil)
		objectiveInfra(t, "15m", func(db *infra.DBServer, _ *infra.Config) {
			db.Objectives = &infra.Objectives{
				RTO: &infra.Duration{Duration: 30 * time.Minute}, AvailabilitySLO: &infra.Percent{Value: 99.9},
				Enforcement:             &infra.Choice{Value: "acknowledge"},
				AvailabilityEnforcement: &infra.Choice{Value: "acknowledge"},
			}
		})
		_, d, stopped, err := runRestore(t, ack)
		if !ack {
			if err == nil || !strings.Contains(err.Error(), "discards exceeds the recovery objective") || !strings.Contains(err.Error(), "Also: the downtime this restore causes") || stopped {
				t.Errorf("not acknowledged: err=%v stopped=%v", err, stopped)
			}
		} else if err != nil || !stopped {
			t.Errorf("acknowledged: err=%v stopped=%v", err, stopped)
		}
		if strings.Join(d.Approval.Breaches, ",") != "rpo,availability_budget" || d.Options[0].Verdict != RPOExceeds || d.Options[0].BudgetVerdict != "exceeds" {
			t.Errorf("ack=%v: record = %+v / %+v", ack, d.Approval, d.Options[0])
		}
	}
}

// An existing configuration that only says `enforcement: refuse` must not be
// refused for a budget it never named.
func TestRestore_AnExistingRPORefuseDoesNotReachTheBudget(t *testing.T) {
	withLedger(t, 50*time.Minute, nil) // spent
	objectiveInfra(t, "", func(db *infra.DBServer, _ *infra.Config) {
		db.Objectives = &infra.Objectives{
			RTO: &infra.Duration{Duration: 30 * time.Minute}, AvailabilitySLO: &infra.Percent{Value: 99.9},
			Enforcement: &infra.Choice{Value: "refuse"}, // the v0.34 setting, and nothing for the budget
		}
	})
	_, d, stopped, err := runRestore(t, false)
	if err != nil || !stopped || d.Options[0].BudgetVerdict != "exceeds" || d.Options[0].BudgetGate != "" {
		t.Errorf("err=%v stopped=%v option=%+v: the budget was enforced by a setting that never named it", err, stopped, d.Options[0])
	}
}
