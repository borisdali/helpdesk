package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
)

// withDrillCurrency makes the history of drills say this, and records what it was
// asked. A client is set so the audit service counts as configured.
type currencyCalls struct {
	keys      []string
	reminders []infra.DrillReminder
	inputs    []availability.DrillInputs
}

func withDrillCurrency(t *testing.T, answer *availability.DrillCurrency) *currencyCalls {
	t.Helper()
	oldFn, oldClient := drillCurrencyFn, ledgerClient
	t.Cleanup(func() { drillCurrencyFn, ledgerClient = oldFn, oldClient })
	ledgerClient = &availability.Client{URL: "http://audit.invalid"}
	calls := &currencyCalls{}
	drillCurrencyFn = func(_ context.Context, key string, r infra.DrillReminder, in availability.DrillInputs, _ time.Time) *availability.DrillCurrency {
		calls.keys, calls.reminders, calls.inputs = append(calls.keys, key), append(calls.reminders, r), append(calls.inputs, in)
		return answer
	}
	return calls
}

func overdueCurrency() *availability.DrillCurrency {
	age := 143
	return &availability.DrillCurrency{State: availability.DrillOverdue, Overdue: true, LimitDays: 90, AgeDays: &age,
		Detail: "last verified drill 143 days ago (limit 90 days)"}
}

// ── the plan ──────────────────────────────────────────────────────────────────

func TestPlan_CarriesTheDrillReminderForADatabaseWithAnObjective(t *testing.T) {
	withLedger(t, 10*time.Minute, nil)
	withEstimate(t, restoresThatTook(40, 45, 50), nil)
	calls := withDrillCurrency(t, overdueCurrency())
	budgetObjectives(t, "", "acknowledge")
	_, _ = liveEnv(t)
	_, cleanup := withRealToolAuditor(t)
	defer cleanup()

	plan, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("%+v, %v", plan, err)
	}
	if plan.DrillCurrency == nil || !plan.DrillCurrency.Overdue || plan.DrillCurrency.Detail != "last verified drill 143 days ago (limit 90 days)" {
		t.Fatalf("drill currency = %+v", plan.DrillCurrency)
	}
	if len(calls.keys) != 1 || calls.keys[0] != "pgbackrest_db" || !calls.reminders[0].Applies || calls.reminders[0].Interval != infra.DefaultDrillInterval {
		t.Errorf("asked %v with %+v", calls.keys, calls.reminders)
	}
	// An overdue drill is information: it does not change what the plan decides.
	if plan.AvailabilityBudget == nil || plan.AvailabilityBudget.Gate != availability.GateAcknowledge {
		t.Errorf("the budget gate moved: %+v", plan.AvailabilityBudget)
	}
	// It is in the JSON the client reads, under the name it reads.
	raw, _ := json.Marshal(plan)
	var view struct {
		D map[string]any `json:"drill_currency"`
	}
	if json.Unmarshal(raw, &view) != nil || view.D["state"] != "overdue" || view.D["overdue"] != true || view.D["detail"] == nil {
		t.Errorf("json = %s", raw)
	}
}

func TestPlan_NoDrillReminderWhereThereIsNothingToRemind(t *testing.T) {
	withLedger(t, 10*time.Minute, nil)
	withEstimate(t, restoresThatTook(40, 45, 50), nil)
	_, _ = liveEnv(t)
	_, cleanup := withRealToolAuditor(t)
	defer cleanup()
	plan := func() PITRPlan {
		p, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
			RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	// The reminder says nothing for a database with no objective: the assessment
	// returns nil and the plan carries nothing.
	calls := withDrillCurrency(t, nil)
	objectiveInfra(t, "", func(db *infra.DBServer, _ *infra.Config) { db.Objectives = nil })
	if p := plan(); p.DrillCurrency != nil {
		t.Errorf("a database with no objective got %+v", p.DrillCurrency)
	}
	if len(calls.reminders) != 1 || calls.reminders[0].Applies {
		t.Errorf("the reminder must not apply with no objective: %+v", calls.reminders)
	}

	// With no audit service there is no history to read: nothing is said, and no read is attempted.
	calls = withDrillCurrency(t, overdueCurrency())
	ledgerClient = nil
	budgetObjectives(t, "", "")
	if p := plan(); p.DrillCurrency != nil || len(calls.keys) != 0 {
		t.Errorf("no audit service: %+v after %d reads", p.DrillCurrency, len(calls.keys))
	}
}

// ── the posture ───────────────────────────────────────────────────────────────

func TestWithObjectiveExposure_AddsTheDrillReminderWithoutChangingTheVerdict(t *testing.T) {
	withExposureClock(t)
	budgetObjectives(t, "15m", "")
	withEstimate(t, restoresThatTook(10, 20, 25), nil)

	// The verdict with no reminder in play.
	withDrillCurrency(t, nil)
	before := statusArchivedAgo(time.Minute)
	withObjectiveExposure(context.Background(), &before, "pgbackrest_db")
	if before.DrillCurrency != nil || before.DrillOverdue {
		t.Fatalf("nothing to say, yet %+v", before.DrillCurrency)
	}

	// The same with an overdue drill: the same verdict, and the reminder beside it.
	calls := withDrillCurrency(t, overdueCurrency())
	after := statusArchivedAgo(time.Minute)
	withObjectiveExposure(context.Background(), &after, "pgbackrest_db")
	if after.DrillCurrency == nil || !after.DrillOverdue || after.DrillCurrency.State != availability.DrillOverdue {
		t.Fatalf("reminder = %+v overdue=%v", after.DrillCurrency, after.DrillOverdue)
	}
	if after.ObjectiveExposure.Status != before.ObjectiveExposure.Status || after.ObjectiveExposure.RTO.Status != before.ObjectiveExposure.RTO.Status {
		t.Errorf("an overdue drill changed the exposure: %+v vs %+v", after.ObjectiveExposure, before.ObjectiveExposure)
	}
	if len(calls.keys) != 1 || calls.keys[0] != "pgbackrest_db" {
		t.Errorf("asked %v", calls.keys)
	}
	// The names a rule or a client reads.
	raw, _ := json.Marshal(after)
	var view map[string]any
	_ = json.Unmarshal(raw, &view)
	if view["drill_overdue"] != true || view["drill_currency"] == nil || view["objective_exposure"] == nil {
		t.Errorf("json = %s", raw)
	}
	// A current drill is reported, and is not overdue.
	age := 12
	withDrillCurrency(t, &availability.DrillCurrency{State: availability.DrillCurrent, LimitDays: 90, AgeDays: &age, Detail: "last verified drill 12 days ago (limit 90 days)"})
	cur := statusArchivedAgo(time.Minute)
	withObjectiveExposure(context.Background(), &cur, "pgbackrest_db")
	if cur.DrillCurrency == nil || cur.DrillOverdue {
		t.Errorf("current: %+v overdue=%v", cur.DrillCurrency, cur.DrillOverdue)
	}
}

// A database with only an availability SLO has no rpo or rto to expose, so it has no
// objective_exposure, but it has an objective and is still reminded to drill.
func TestWithObjectiveExposure_AnSLOAloneStillGetsTheDrillReminder(t *testing.T) {
	withExposureClock(t)
	objectiveInfra(t, "", func(db *infra.DBServer, _ *infra.Config) {
		db.Objectives = &infra.Objectives{AvailabilitySLO: &infra.Percent{Value: 99.9}}
	})
	withEstimate(t, nil, nil)
	calls := withDrillCurrency(t, overdueCurrency())
	res := statusArchivedAgo(time.Minute)
	withObjectiveExposure(context.Background(), &res, "pgbackrest_db")
	if res.ObjectiveExposure != nil || res.DrillCurrency == nil || !res.DrillOverdue {
		t.Errorf("exposure %+v, reminder %+v", res.ObjectiveExposure, res.DrillCurrency)
	}
	if len(calls.reminders) != 1 || !calls.reminders[0].Applies {
		t.Errorf("reminder inputs: %+v", calls.reminders)
	}
}

func TestWithObjectiveExposure_NoReminderForAnUnknownTarget(t *testing.T) {
	withExposureClock(t)
	budgetObjectives(t, "15m", "")
	calls := withDrillCurrency(t, overdueCurrency())
	res := statusArchivedAgo(time.Minute)
	withObjectiveExposure(context.Background(), &res, "no-such-database")
	if res.DrillCurrency != nil || res.DrillOverdue || len(calls.keys) != 0 {
		t.Errorf("an unknown target: %+v, %d reads", res.DrillCurrency, len(calls.keys))
	}
}

// What the reminder is given to read the system identifier with is the lazy
// reader for the target, so a server with no drills never pays for the command.
func TestDrillCurrencyAt_OffersTheIdentifierReaderForTheTarget(t *testing.T) {
	calls := withDrillCurrency(t, overdueCurrency())
	old := sourceSystemIDFn
	t.Cleanup(func() { sourceSystemIDFn = old })
	var asked []string
	sourceSystemIDFn = func(_ context.Context, target string) string { asked = append(asked, target); return "7001" }
	drillCurrencyAt(context.Background(), "pgbackrest_db", infra.DrillReminder{Applies: true, Interval: infra.DefaultDrillInterval}, "helpdesk-test-pg-pgbackrest", time.Now())
	if len(asked) != 0 {
		t.Fatalf("the identifier was read eagerly: %v", asked)
	}
	if got := calls.inputs[0].ReadSystemID(); got != "7001" || len(asked) != 1 || asked[0] != "helpdesk-test-pg-pgbackrest" {
		t.Errorf("reader gave %q for %v", got, asked)
	}
}
