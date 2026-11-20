package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

func rdI(n int64) *int64     { return &n }
func rdU(n uint64) *uint64   { return &n }
func rdF(n float64) *float64 { return &n }

// rdUnwrap reads wrapped text as the one line it was before it was wrapped.
func rdUnwrap(s string) string { return strings.Join(strings.Fields(s), " ") }

func rdAt(m int) time.Time { return time.Date(2026, 11, 4, 14, m, 0, 0, time.UTC) }

func rdObjectivesFixture(digest string) *audit.ObjectivesInForce {
	return &audit.ObjectivesInForce{
		Environment: "prod", ConfigDigest: digest,
		RPOSeconds: rdI(900), RPOSource: "environment:prod",
		RTOSeconds: rdI(1800), RTOSource: "default",
		AvailabilitySLO: rdF(99.95), AvailabilitySource: "database",
		RecoveryPreference: "data_first", PreferenceSource: "environment:prod",
		Enforcement: "acknowledge", EnforcementSource: "environment:prod",
	}
}

func rdOptionFixture(chosen bool) audit.DecisionOption {
	return audit.DecisionOption{
		Name: "point_in_time_restore", Chosen: chosen,
		LossBasis: "live_instance", LossSeconds: rdI(65563), LossBytes: rdU(16 << 20),
		Verdict: "exceeds", Gate: "acknowledge",
	}
}

// The budget line comes from the numbers the event recorded, in the config
// check's own words, and only when the event recorded a window: an event from
// before v0.35 is not given the default after the fact.
func TestRDBudget(t *testing.T) {
	withBudget := func(slo float64, window int64, source string, rto *int64) *audit.ObjectivesInForce {
		return &audit.ObjectivesInForce{
			AvailabilitySLO: rdF(slo), AvailabilityWindowSeconds: rdI(window), AvailabilityWindowSource: source,
			AvailabilityBudgetSeconds: rdI(1), RTOSeconds: rto,
		}
	}
	const thirtyDays = 30 * 24 * 3600
	tests := []struct {
		name string
		o    *audit.ObjectivesInForce
		want string
	}{
		{"consistent", withBudget(99.9, thirtyDays, "environment:prod", rdI(1800)),
			"  budget: availability 99.9% over 30d allows 43m12s of downtime per window; rto 30m uses 69% of it: tolerates 1 recovery per window at the rto"},
		{"contradictory is flagged", withBudget(99.99, thirtyDays, "environment:prod", rdI(1800)),
			"  ⚠ budget: availability 99.99% over 30d allows 4m19s of downtime per window; rto 30m is longer than the whole budget: one recovery that takes the full rto breaks the SLO"},
		{"the default window is called a default", withBudget(99.9, thirtyDays, infra.SourceBuiltin, rdI(300)),
			"  budget: availability 99.9% over 30d (default window) allows 43m12s of downtime per window; rto 5m uses 12% of it: tolerates 8 recoveries per window at the rto"},
		{"no rto", withBudget(99.9, thirtyDays, "database", nil),
			"  budget: availability 99.9% over 30d allows 43m12s of downtime per window; no rto is set, so the recoveries it absorbs are not computed"},
		{"an event from before v0.35 has no window: no line, no assumed default",
			&audit.ObjectivesInForce{AvailabilitySLO: rdF(99.95), RTOSeconds: rdI(1800)}, ""},
		{"no slo", &audit.ObjectivesInForce{AvailabilityWindowSeconds: rdI(thirtyDays), RTOSeconds: rdI(1800)}, ""},
		{"a recorded window that is nonsense", withBudget(99.9, 0, "database", rdI(1800)), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rdBudget(tc.o); rdUnwrap(got) != rdUnwrap(tc.want) {
				t.Errorf("\n got  %q\n want %q", got, tc.want)
			}
		})
	}
}

func TestRDObjectives_ShowsTheBudgetLineBeneathTheObjectives(t *testing.T) {
	o := rdObjectivesFixture("0123456789abcdef")
	o.AvailabilitySLO = rdF(99.9)
	o.AvailabilityWindowSeconds, o.AvailabilityWindowSource = rdI(30*24*3600), "default"
	out := strings.Join(rdObjectives(o), "\n")
	objectives := strings.Index(out, "availability 99.9% [database]")
	budget := strings.Index(out, "  budget: availability 99.9% over 30d allows 43m12s")
	policy := strings.Index(out, "enforcement acknowledge")
	if objectives < 0 || budget < objectives || policy < budget {
		t.Errorf("want objectives, then the budget, then the policy:\n%s", out)
	}
	// The unchanged fixture has no window: its rendering is as it was.
	if old := strings.Join(rdObjectives(rdObjectivesFixture("0123456789abcdef")), "\n"); strings.Contains(old, "budget") {
		t.Errorf("an event with no recorded window got a budget line:\n%s", old)
	}
}

func TestFormatRecoveryDecisions_PlanGateAndExecution(t *testing.T) {
	out := formatRecoveryDecisions([]narrativeRecoveryDecision{
		{At: rdAt(1), Decision: audit.RecoveryDecision{Stage: "plan", Decision: "planned", RecoveryTarget: "time=2026-11-04T01:39:07Z",
			Objectives: rdObjectivesFixture("0123456789abcdef"), Options: []audit.DecisionOption{rdOptionFixture(false)}}},
		{At: rdAt(2), Decision: audit.RecoveryDecision{Stage: "gate", Decision: "approved", DecidedBy: "alice",
			RecoveryTarget: "time=2026-11-04T01:39:07Z",
			Approval:       &audit.ApprovalFacts{ApprovedBy: "alice", AcknowledgedDataLoss: true, AllowedStopServing: true, AcknowledgedBreach: true}}},
		{At: rdAt(3), Decision: audit.RecoveryDecision{Stage: "execution", Decision: "proceeded",
			Objectives: rdObjectivesFixture("0123456789abcdef"),
			Approval:   &audit.ApprovalFacts{ApprovedBy: "alice"}, Options: []audit.DecisionOption{rdOptionFixture(true)}}},
	})
	t.Log("\n" + out) // shown with -v: how the section reads
	flat := rdUnwrap(out)
	for _, want := range []string{
		"Objectives in force (environment prod), config 0123456789abcdef:",
		"rpo 15m 00s [environment:prod]", "rto 30m 00s [default]", "availability 99.95% [database]",
		"preference data_first [environment:prod]", "enforcement acknowledge [environment:prod]",
		"14:01:00  plan", "14:02:00  gate", "14:03:00  execution",
		"by alice; acknowledged: data loss, stopping the instance, exceeding an objective if it comes to that",
		"on alice's approval",
		"point_in_time_restore (chosen): loss 18h 12m, 16.0 MiB of WAL (live_instance, exact) → exceeds; needed the breach acknowledged",
		"[time=2026-11-04T01:39:07Z]",
	} {
		if !strings.Contains(flat, rdUnwrap(want)) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// In time order, whatever order they came in.
	if strings.Index(out, "14:01:00") > strings.Index(out, "14:02:00") || strings.Index(out, "14:02:00") > strings.Index(out, "14:03:00") {
		t.Errorf("not in time order:\n%s", out)
	}
	// The objectives are shown once, not once per stage.
	if strings.Count(out, "Objectives in force") != 1 {
		t.Errorf("objectives repeated:\n%s", out)
	}
	if strings.Contains(out, "⚠") {
		t.Errorf("a clean decision carries a warning:\n%s", out)
	}
}

// The budget verdict is on the option line, apart from the loss verdict, and an
// older record (no budget fields) reads exactly as it did.
func TestRDOption_ShowsTheBudgetVerdictApartFromTheLossVerdict(t *testing.T) {
	spent := int64(-408)
	left := int64(1392)
	both := rdOptionFixture(true)
	both.BudgetVerdict, both.BudgetGate, both.BudgetRemainingSeconds = "may_exceed", "acknowledge", &left
	if got := rdOption(both); !strings.Contains(got, "→ exceeds; needed the breach acknowledged; budget → may_exceed (23m 12s left); the budget needed the breach acknowledged") {
		t.Errorf("both verdicts: %q", got)
	}
	// A spent budget shows as negative, and a refusal as a refusal of the budget, not the loss.
	over := audit.DecisionOption{Name: "point_in_time_restore", BudgetVerdict: "exceeds", BudgetGate: "refuse", BudgetRemainingSeconds: &spent}
	if got := rdOption(over); got != "point_in_time_restore; budget → exceeds (-6m 48s left); budget enforcement refuses it" {
		t.Errorf("a spent budget: %q", got)
	}
	// Nothing demanded: the verdict alone, with no gate text.
	room := audit.DecisionOption{Name: "point_in_time_restore", BudgetVerdict: "within", BudgetRemainingSeconds: &left}
	if got := rdOption(room); got != "point_in_time_restore; budget → within (23m 12s left)" {
		t.Errorf("room: %q", got)
	}
	// A record from before v0.35 has no budget, and says nothing about one.
	if got := rdOption(rdOptionFixture(false)); strings.Contains(got, "budget") {
		t.Errorf("an old record mentions a budget: %q", got)
	}
}

// The option line says what the budget verdict took the restore to last, and what
// history said; with no history it says nothing about it.
// The transactions counted from the WAL are said as what they are: a count, a lower
// bound, or why there is none. Nothing is shown for a record that predates them.
func TestRDOption_SaysHowManyTransactionsWereAtStake(t *testing.T) {
	n := 41
	o := audit.DecisionOption{Name: "point_in_time_restore", LossCommitsStatus: "measured", LossCommits: &n}
	if got := rdOption(o); !strings.Contains(got, "; 41 transaction(s) discarded (counted from the WAL)") || strings.Contains(got, "at least") {
		t.Errorf("exact: %q", got)
	}
	o.LossCommitsAtLeast = true
	if got := rdOption(o); !strings.Contains(got, "; at least 41 transaction(s) discarded (counted from the WAL)") {
		t.Errorf("a lower bound: %q", got)
	}
	zero := 0
	o.LossCommits, o.LossCommitsAtLeast = &zero, false
	if got := rdOption(o); !strings.Contains(got, "; 0 transaction(s) discarded") {
		t.Errorf("none: %q", got)
	}
	o = audit.DecisionOption{Name: "point_in_time_restore", LossCommitsStatus: "unavailable", LossCommitsReason: "pg_waldump was not found on the instance"}
	if got := rdOption(o); !strings.Contains(got, "; transactions not counted (pg_waldump was not found on the instance)") || strings.Contains(got, "discarded") {
		t.Errorf("unavailable: %q", got)
	}
	// A record from before the count, and a measured one with no number, say nothing.
	for _, o := range []audit.DecisionOption{{Name: "x"}, {Name: "x", LossCommitsStatus: "measured"}} {
		if got := rdOption(o); strings.Contains(got, "transaction") {
			t.Errorf("%+v said something about transactions: %q", o, got)
		}
	}
}

func TestRDOption_SaysWhatTheRestoreWasTakenToLastAndWhatHistorySaid(t *testing.T) {
	left, dur := int64(1992), int64(3000)
	n, over, p90, p50 := 5, 2, int64(3000), int64(1200)
	o := audit.DecisionOption{Name: "point_in_time_restore", BudgetVerdict: "may_exceed", BudgetGate: "acknowledge", BudgetRemainingSeconds: &left,
		BudgetDurationSeconds: &dur, BudgetDurationBasis: "history_p90",
		EstimateStatus: "estimated", EstimateSamples: &n, EstimateP50Seconds: &p50, EstimateP90Seconds: &p90, EstimateVsRTO: "exceeds", EstimateOverRTO: &over}
	got := rdOption(o)
	for _, want := range []string{
		"budget → may_exceed (33m 12s left; restore taken as 50m 00s, the p90 of past restores)",
		"; the budget needed the breach acknowledged",
		"; history: p90 50m 00s over 5 past restores, 2 took longer than the rto",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	// Too few restores for a p90: the longest is said as the longest, never as a p90.
	max := int64(3000)
	o.BudgetDurationBasis, o.EstimateP90Seconds, o.EstimateMaxSeconds = "history_longest", nil, &max
	got = rdOption(o)
	for _, want := range []string{
		"restore taken as 50m 00s, the longest past restore (too few for a p90)",
		"; history: longest 50m 00s of 5 past restores (too few for a p90), 2 took longer than the rto",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("longest: missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "history: p90") {
		t.Errorf("a longest called a p90: %q", got)
	}
	// A record from before the p90 needed ten samples says "history", which meant the p90.
	o.BudgetDurationBasis = "history"
	if got := rdOption(o); !strings.Contains(got, "the p90 of past restores") {
		t.Errorf("an old record's basis: %q", got)
	}
	// By the declared rto, and with history within it.
	o.EstimateP90Seconds = &p90
	o.BudgetDurationBasis, o.EstimateVsRTO, o.EstimateOverRTO = "declared_rto", "within", nil
	got = rdOption(o)
	if !strings.Contains(got, "restore taken as 50m 00s, from the declared rto") || strings.Contains(got, "took longer than the rto") {
		t.Errorf("by the rto: %q", got)
	}
	// Too little history is said as it is; none at all says nothing.
	o.EstimateStatus, o.EstimateSamples, o.EstimateP90Seconds = "insufficient_history", &n, nil
	if got := rdOption(o); !strings.Contains(got, "; history: insufficient_history") || strings.Contains(got, "p90 50m") {
		t.Errorf("too few: %q", got)
	}
	o.EstimateStatus = ""
	if got := rdOption(o); strings.Contains(got, "history:") {
		t.Errorf("no estimate: %q", got)
	}
}

func TestFormatRecoveryDecisions_SaysWhichObjectivesStoodInTheWay(t *testing.T) {
	out := formatRecoveryDecisions([]narrativeRecoveryDecision{{At: rdAt(3), Decision: audit.RecoveryDecision{
		Stage: "execution", Decision: "proceeded",
		Approval: &audit.ApprovalFacts{ApprovedBy: "alice", AcknowledgedBreach: true, Breaches: []string{"rpo", "availability_budget"}},
	}}})
	if !strings.Contains(out, "on alice's approval; objectives in the way: rpo, availability_budget") {
		t.Errorf("%s", out)
	}
	none := formatRecoveryDecisions([]narrativeRecoveryDecision{{At: rdAt(3), Decision: audit.RecoveryDecision{
		Stage: "execution", Decision: "proceeded", Approval: &audit.ApprovalFacts{ApprovedBy: "alice"},
	}}})
	if strings.Contains(none, "objectives in the way") {
		t.Errorf("a restore with nothing in the way says so: %s", none)
	}
}

func TestFormatRecoveryDecisions_ARefusalSaysWhy(t *testing.T) {
	out := formatRecoveryDecisions([]narrativeRecoveryDecision{{At: rdAt(5), Decision: audit.RecoveryDecision{
		Stage: "execution", Decision: "refused", Reason: "the data this restore discards exceeds the recovery objective; approve again with acknowledge_objective_breach",
		Objectives: rdObjectivesFixture("0123456789abcdef"), Options: []audit.DecisionOption{rdOptionFixture(false)},
	}}})
	if !strings.Contains(out, "refused") || !strings.Contains(out, "reason: ") || !strings.Contains(out, "acknowledge_objective_breach") {
		t.Errorf("a refusal without its reason:\n%s", out)
	}
	if strings.Contains(out, "(chosen)") {
		t.Errorf("a refused option is shown as chosen:\n%s", out)
	}
}

func TestFormatRecoveryDecisions_ADenialNamesWhoDenied(t *testing.T) {
	out := formatRecoveryDecisions([]narrativeRecoveryDecision{{At: rdAt(2), Decision: audit.RecoveryDecision{
		Stage: "gate", Decision: "denied", DecidedBy: "carol", Reason: "wrong target",
	}}})
	if !strings.Contains(out, "denied") || !strings.Contains(out, "by carol") || !strings.Contains(out, "reason: wrong target") {
		t.Errorf("denial:\n%s", out)
	}
	if strings.Contains(out, "acknowledged:") {
		t.Errorf("a denial acknowledges nothing:\n%s", out)
	}
}

// The stages must have been made against the same requirements; when the
// configuration changed between them, the section says so.
func TestFormatRecoveryDecisions_FlagsAConfigChangedBetweenStages(t *testing.T) {
	out := formatRecoveryDecisions([]narrativeRecoveryDecision{
		{At: rdAt(1), Decision: audit.RecoveryDecision{Stage: "plan", Decision: "planned", Objectives: rdObjectivesFixture("aaaaaaaaaaaaaaaa")}},
		{At: rdAt(3), Decision: audit.RecoveryDecision{Stage: "execution", Decision: "proceeded", Objectives: rdObjectivesFixture("bbbbbbbbbbbbbbbb")}},
	})
	if !strings.Contains(out, "the configuration changed between stages") {
		t.Errorf("no warning for a changed configuration:\n%s", out)
	}
	// The objectives shown are the ones in force at the last stage.
	if !strings.Contains(out, "config bbbbbbbbbbbbbbbb") {
		t.Errorf("shows the wrong stage's objectives:\n%s", out)
	}
	same := formatRecoveryDecisions([]narrativeRecoveryDecision{
		{At: rdAt(1), Decision: audit.RecoveryDecision{Stage: "plan", Decision: "planned", Objectives: rdObjectivesFixture("aaaaaaaaaaaaaaaa")}},
		{At: rdAt(3), Decision: audit.RecoveryDecision{Stage: "execution", Decision: "proceeded", Objectives: rdObjectivesFixture("aaaaaaaaaaaaaaaa")}},
	})
	if strings.Contains(same, "changed between stages") {
		t.Errorf("a warning with nothing changed:\n%s", same)
	}
}

// The availability budget has its own enforcement. A narrative that printed only the
// rpo's ("enforcement warn") over a database that refuses on its budget hid the
// setting that explained the refusal.
func TestRDObjectives_ShowsTheBudgetsEnforcementWithAnAvailabilityObjective(t *testing.T) {
	slo := 99.95
	o := &audit.ObjectivesInForce{
		AvailabilitySLO: &slo, AvailabilitySource: "database",
		Enforcement: "warn", EnforcementSource: "database",
		AvailabilityEnforcement: "refuse", AvailabilityEnforcementSource: "database",
	}
	got := strings.Join(rdObjectives(o), "\n")
	if !strings.Contains(got, "enforcement warn [database]   availability enforcement refuse [database]") {
		t.Errorf("the budget's enforcement is not shown:\n%s", got)
	}
	// With no availability objective there is no budget to enforce: no extra words.
	o.AvailabilitySLO = nil
	if got := strings.Join(rdObjectives(o), "\n"); strings.Contains(got, "availability enforcement") {
		t.Errorf("shown without an availability objective:\n%s", got)
	}
	// An old record without the field prints as before.
	o.AvailabilitySLO, o.AvailabilityEnforcement = &slo, ""
	if got := strings.Join(rdObjectives(o), "\n"); strings.Contains(got, "availability enforcement") {
		t.Errorf("shown for a record that has none:\n%s", got)
	}
}

func TestFormatRecoveryDecisions_UnsetAndUnreadableObjectives(t *testing.T) {
	none := formatRecoveryDecisions([]narrativeRecoveryDecision{{At: rdAt(1), Decision: audit.RecoveryDecision{
		Stage: "plan", Decision: "planned", Objectives: &audit.ObjectivesInForce{Enforcement: "warn"},
	}}})
	if !strings.Contains(none, "no objective set") || !strings.Contains(none, "enforcement warn") {
		t.Errorf("no objective:\n%s", none)
	}
	bad := formatRecoveryDecisions([]narrativeRecoveryDecision{{At: rdAt(1), Decision: audit.RecoveryDecision{
		Stage: "plan", Decision: "planned",
		Objectives: &audit.ObjectivesInForce{Enforcement: "acknowledge", Problems: []string{`db_servers.a.objectives: invalid duration "soon"`}},
	}}})
	if !strings.Contains(bad, `⚠ config problem: db_servers.a.objectives: invalid duration "soon"`) {
		t.Errorf("an unreadable objective is not flagged:\n%s", bad)
	}
}

// Through the whole `vault incidents <run>` printer, from a gateway's JSON: the
// section appears between REMEDIATION and EVALUATION, an incident without a
// recovery decision shows no section, and a password in a target cannot be
// printed because it was never stored.
func TestPrintIncidentJourney_ShowsTheRecoveryDecisionBetweenRemediationAndEvaluation(t *testing.T) {
	serve := func(extra map[string]any) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := map[string]any{
				"incident_id": "plr_rd", "started_at": "2026-11-04T14:00:00Z", "operator": "alice",
				"triage":      map[string]any{"run_id": "plr_rd", "playbook": "pbs_pgbackrest_pitr_triage", "findings": "plan feasible"},
				"remediation": map[string]any{"run_id": "plr_rd2", "playbook": "pbs_pgbackrest_pitr_remediate", "outcome": "resolved"},
				"evaluation":  map[string]any{"overall_score": 0.9},
			}
			for k, v := range extra {
				n[k] = v
			}
			json.NewEncoder(w).Encode(n) //nolint:errcheck
		}))
	}
	with := serve(map[string]any{"recovery_decisions": []map[string]any{{
		"at": "2026-11-04T14:03:00Z", "event_id": "rdc_1",
		"decision": map[string]any{"stage": "execution", "decision": "refused", "reason": "needs acknowledge_objective_breach",
			"options": []map[string]any{{"name": "point_in_time_restore", "verdict": "exceeds", "gate": "acknowledge"}}},
	}}})
	defer with.Close()
	out := captureStdout(func() { printIncidentJourney(with.URL, "", "plr_rd", false) })

	iRem, iDec, iEval := strings.Index(out, "REMEDIATION"), strings.Index(out, "RECOVERY DECISION"), strings.Index(out, "EVALUATION")
	if iRem < 0 || iDec < 0 || iEval < 0 || iRem >= iDec || iDec >= iEval {
		t.Fatalf("section order wrong (remediation %d, recovery decision %d, evaluation %d):\n%s", iRem, iDec, iEval, out)
	}
	if !strings.Contains(out, "refused") || !strings.Contains(out, "needs acknowledge_objective_breach") {
		t.Errorf("the decision is not shown:\n%s", out)
	}

	without := serve(nil)
	defer without.Close()
	out = captureStdout(func() { printIncidentJourney(without.URL, "", "plr_rd", false) })
	if strings.Contains(out, "RECOVERY DECISION") {
		t.Errorf("an incident with no recovery decision has the section:\n%s", out)
	}
}

// The outcome line sets the recovery duration against the rto that was in force,
// from the objectives recorded in the same decision.
func TestFormatRecoveryDecisions_OutcomeAgainstTheRTO(t *testing.T) {
	outcome := func(secs *int64) narrativeRecoveryDecision {
		return narrativeRecoveryDecision{At: rdAt(44), Decision: audit.RecoveryDecision{
			Stage: "outcome", Decision: "resolved", RunID: "plr_r1", DurationSeconds: secs, RecoveryTarget: "time=2026-11-04T01:39:07Z"}}
	}
	exec := narrativeRecoveryDecision{At: rdAt(3), Decision: audit.RecoveryDecision{
		Stage: "execution", Decision: "proceeded", Objectives: rdObjectivesFixture("0123456789abcdef")}} // rto 30m, from "default"

	exceeded := formatRecoveryDecisions([]narrativeRecoveryDecision{exec, outcome(rdI(41*60 + 12))})
	for _, want := range []string{"resolved", "recovery duration 41m 12s", "(rto 30m 00s [default]: exceeded by 11m 12s)",
		"it is not the downtime"} {
		if !strings.Contains(exceeded, want) {
			t.Errorf("missing %q in:\n%s", want, exceeded)
		}
	}
	within := formatRecoveryDecisions([]narrativeRecoveryDecision{exec, outcome(rdI(20 * 60))})
	if !strings.Contains(within, "recovery duration 20m 00s (rto 30m 00s [default]: within)") {
		t.Errorf("within:\n%s", within)
	}
	exactly := formatRecoveryDecisions([]narrativeRecoveryDecision{exec, outcome(rdI(1800))})
	if !strings.Contains(exactly, ": within)") {
		t.Errorf("exactly at the rto is within it:\n%s", exactly)
	}

	// No rto in force, no objectives at all, no duration: said plainly, never guessed.
	noRTO := exec
	noRTO.Decision.Objectives = &audit.ObjectivesInForce{Enforcement: "warn"}
	if got := formatRecoveryDecisions([]narrativeRecoveryDecision{noRTO, outcome(rdI(600))}); !strings.Contains(got, "recovery duration 10m 00s (no rto set)") {
		t.Errorf("no rto:\n%s", got)
	}
	got := formatRecoveryDecisions([]narrativeRecoveryDecision{outcome(rdI(600))})
	if !strings.Contains(got, "recovery duration 10m 00s") || strings.Contains(got, "rto") && !strings.Contains(got, "not the downtime") {
		t.Errorf("no objectives recorded:\n%s", got)
	}
	if strings.Contains(got, "(rto") || strings.Contains(got, "(no rto") {
		t.Errorf("a verdict without objectives to base it on:\n%s", got)
	}
	if got := formatRecoveryDecisions([]narrativeRecoveryDecision{exec, outcome(nil)}); !strings.Contains(got, "recovery duration unknown") {
		t.Errorf("unknown duration:\n%s", got)
	}
	// The caveat appears only when there is an outcome to caveat.
	if strings.Contains(formatRecoveryDecisions([]narrativeRecoveryDecision{exec}), "not the downtime") {
		t.Error("the downtime caveat appears with no outcome")
	}
}

// A lower bound is said as one: never labelled exact, and never lost.
// The section sets what the plan said against what happened, from the figures the
// events recorded, and says nothing for a figure that was not recorded on both
// sides on the same basis.
func TestRDPlanVsActual(t *testing.T) {
	commits := func(n int, atLeast bool) (string, *int, bool) { return "measured", &n, atLeast }
	opt := func(chosen bool, loss int64, basis string, n int, atLeast bool, dur int64, durBasis string) audit.DecisionOption {
		st, c, al := commits(n, atLeast)
		return audit.DecisionOption{Name: "point_in_time_restore", Chosen: chosen,
			LossBasis: basis, LossSeconds: rdI(loss), LossCommitsStatus: st, LossCommits: c, LossCommitsAtLeast: al,
			BudgetDurationSeconds: rdI(dur), BudgetDurationBasis: durBasis}
	}
	stage := func(m int, s string, opts ...audit.DecisionOption) narrativeRecoveryDecision {
		decision := map[string]string{"plan": "planned", "gate": "approved", "execution": "proceeded"}[s]
		return narrativeRecoveryDecision{At: rdAt(m), Decision: audit.RecoveryDecision{Stage: s, Decision: decision, Options: opts}}
	}
	outcomeAs := func(m int, decision string, secs *int64) narrativeRecoveryDecision {
		return narrativeRecoveryDecision{At: rdAt(m), Decision: audit.RecoveryDecision{Stage: "outcome", Decision: decision, DurationSeconds: secs}}
	}
	outcome := func(m int, secs *int64) narrativeRecoveryDecision { return outcomeAs(m, "resolved", secs) }

	plan := stage(1, "plan", opt(false, 20, "live_instance", 0, false, 101, "history_longest"))
	exec := stage(3, "execution", opt(true, 65, "live_instance", 3, false, 101, "history_longest"))

	got := strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{plan, stage(2, "gate"), exec, outcome(5, rdI(89))}), "\n")
	for _, want := range []string{
		"═══ PLAN VS ACTUAL ═══  the plan of 14:01:00 against what happened",
		"recovery time  planned 1m 41s (the longest past restore (too few for a p90)); took 1m 29s (12s under)",
		"data loss      planned 20s; at the stop 1m 05s (45s more)",
		"transactions   planned 0; at the stop 3 (3 more)",
		"not counted afterwards",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// The declared rto is an upper bound, and the section says that is what it was.
	declared := stage(1, "plan", opt(false, 20, "live_instance", 0, false, 1800, "declared_rto"))
	got = strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{declared, outcome(5, rdI(89))}), "\n")
	if !strings.Contains(got, "planned 30m 00s (from the declared rto); took 1m 29s (28m 31s under)") {
		t.Errorf("declared rto:\n%s", got)
	}
	// A plan made from restore drills says the figure was demonstrated on a copy,
	// so it is never read as this server's own history.
	for basis, want := range map[string]string{
		"drill_longest": "planned 1m 41s (the longest restore drill, demonstrated on a copy (too few for a p90)); took 1m 29s (12s under)",
		"drill_p90":     "planned 1m 41s (the p90 of restore drills, demonstrated on a copy); took 1m 29s (12s under)",
	} {
		fromDrills := stage(1, "plan", opt(false, 20, "live_instance", 0, false, 101, basis))
		got := strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{fromDrills, outcome(5, rdI(89))}), "\n")
		if !strings.Contains(strings.Join(strings.Fields(got), " "), want) {
			t.Errorf("%s:\n%s", basis, got)
		}
	}
	// Slower than planned, and exactly as planned.
	if got := strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{plan, exec, outcome(5, rdI(150))}), "\n"); !strings.Contains(got, "took 2m 30s (49s over)") {
		t.Errorf("over:\n%s", got)
	}
	if got := strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{plan, exec, outcome(5, rdI(101))}), "\n"); !strings.Contains(got, "took 1m 41s (as planned)") {
		t.Errorf("as planned:\n%s", got)
	}

	// Nothing to compare: no outcome, no plan, an unknown duration.
	if got := rdPlanVsActual([]narrativeRecoveryDecision{plan, exec}); got != nil {
		t.Errorf("no outcome: %v", got)
	}
	if got := rdPlanVsActual([]narrativeRecoveryDecision{exec, outcome(5, rdI(89))}); got != nil {
		t.Errorf("no plan: %v", got)
	}
	unknown := strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{plan, exec, outcome(5, nil)}), "\n")
	if strings.Contains(unknown, "recovery time") || !strings.Contains(unknown, "data loss") {
		t.Errorf("unknown duration leaves the loss line only:\n%s", unknown)
	}

	// The verdict against the rpo is shown as each stage recorded it, and a verdict
	// that changed is said to have; a stage with no verdict (an older record) shows none.
	withVerdict := func(o audit.DecisionOption, v string) audit.DecisionOption { o.Verdict = v; return o }
	flipPlan := stage(1, "plan", withVerdict(opt(false, 20, "live_instance", 0, false, 101, "history_longest"), "within"))
	flipExec := stage(3, "execution", withVerdict(opt(true, 4000, "live_instance", 3, false, 101, "history_longest"), "exceeds"))
	got = strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{flipPlan, flipExec, outcome(5, rdI(89))}), "\n")
	if !strings.Contains(got, "data loss      planned 20s → within; at the stop 1h 06m → exceeds (1h 06m more; the verdict changed)") {
		t.Errorf("a verdict that changed:\n%s", got)
	}
	same := stage(3, "execution", withVerdict(opt(true, 30, "live_instance", 3, false, 101, "history_longest"), "within"))
	got = strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{flipPlan, same, outcome(5, rdI(89))}), "\n")
	if !strings.Contains(got, "planned 20s → within; at the stop 30s → within (10s more)") || strings.Contains(got, "verdict changed") {
		t.Errorf("an unchanged verdict:\n%s", got)
	}
	oneSided := strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{plan, flipExec, outcome(5, rdI(89))}), "\n")
	if !strings.Contains(oneSided, "planned 20s; at the stop 1h 06m → exceeds (1h 06m more)") || strings.Contains(oneSided, "verdict changed") {
		t.Errorf("a plan with no recorded verdict:\n%s", oneSided)
	}

	// A refused restore never stopped anything: no "at the stop", and the outcome's
	// duration (the time to escalate) is not a restore time.
	refused := exec
	refused.Decision.Decision = "refused"
	if got := rdPlanVsActual([]narrativeRecoveryDecision{plan, refused, outcomeAs(5, "escalated", rdI(12))}); got != nil {
		t.Errorf("a refused restore was compared: %v", got)
	}
	// A restore that ran but did not end resolved has a loss at the stop and no restore time.
	failed := strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{plan, exec, outcomeAs(5, "escalated", rdI(300))}), "\n")
	if strings.Contains(failed, "recovery time") || !strings.Contains(failed, "data loss      planned 20s; at the stop 1m 05s") {
		t.Errorf("an unresolved restore:\n%s", failed)
	}

	// A loss measured on a different basis is not compared, and a count that is a
	// lower bound on either side is not given a difference.
	archive := stage(1, "plan", opt(false, 20, "archive_end", 4, true, 101, "history_longest"))
	got = strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{archive, exec, outcome(5, rdI(89))}), "\n")
	if strings.Contains(got, "data loss") {
		t.Errorf("loss on different bases was compared:\n%s", got)
	}
	if !strings.Contains(got, "transactions   planned at least 4; at the stop 3") || strings.Contains(got, "fewer)") || strings.Contains(got, "more)") {
		t.Errorf("a lower bound is not given a difference:\n%s", got)
	}

	// The plan is the one made before the execution, not a later diagnostic one.
	later := stage(9, "plan", opt(false, 999, "live_instance", 99, false, 7, "history_longest"))
	got = strings.Join(rdPlanVsActual([]narrativeRecoveryDecision{plan, exec, outcome(5, rdI(89)), later}), "\n")
	if strings.Contains(got, "999") || !strings.Contains(got, "planned 20s") {
		t.Errorf("used a plan made after the execution:\n%s", got)
	}

	// And it is part of the section, under the downtime caveat.
	full := formatRecoveryDecisions([]narrativeRecoveryDecision{plan, exec, outcome(5, rdI(89))})
	if strings.Index(full, "PLAN VS ACTUAL") < strings.Index(full, "it is not the downtime") {
		t.Errorf("plan vs actual should follow the caveat:\n%s", full)
	}
	if strings.Contains(formatRecoveryDecisions([]narrativeRecoveryDecision{plan, exec}), "PLAN VS ACTUAL") {
		t.Error("plan vs actual appears with no outcome")
	}
}

// The downtime budget row sets what the plan said was left against what the
// incident was charged: the wait at the gate is in the charge, so a plan that said
// "within" can end overspent, and that is said as a changed verdict.
func TestRDPlanVsActual_Budget(t *testing.T) {
	planOpt := func(verdict string, left int64, pending *int64) audit.DecisionOption {
		return audit.DecisionOption{Name: "point_in_time_restore", BudgetVerdict: verdict,
			BudgetRemainingSeconds: rdI(left), BudgetPendingSeconds: pending}
	}
	stages := func(plan audit.DecisionOption, incident *int64) []narrativeRecoveryDecision {
		return []narrativeRecoveryDecision{
			{At: rdAt(1), Decision: audit.RecoveryDecision{Stage: "plan", Decision: "planned", Options: []audit.DecisionOption{plan}}},
			{At: rdAt(3), Decision: audit.RecoveryDecision{Stage: "execution", Decision: "proceeded", Options: []audit.DecisionOption{{Name: "point_in_time_restore", Chosen: true}}}},
			{At: rdAt(5), Decision: audit.RecoveryDecision{Stage: "outcome", Decision: "resolved", DurationSeconds: rdI(90), IncidentSeconds: incident}},
		}
	}
	join := func(ds []narrativeRecoveryDecision) string { return strings.Join(rdPlanVsActual(ds), "\n") }

	// The case found live: 6m 23s left at the plan, an incident that came to 7m 03s.
	got := join(stages(planOpt("within", 383, nil), rdI(423)))
	if !strings.Contains(got, "budget         planned 6m 23s left → within; the incident was charged 7m 03s, so overspent by 40s → exceeds; the verdict changed") {
		t.Errorf("an overspent budget:\n%s", got)
	}
	// Within and staying within.
	got = join(stages(planOpt("within", 1500, nil), rdI(200)))
	if !strings.Contains(got, "planned 25m 00s left → within; the incident was charged 3m 20s, so 21m 40s left → within") || strings.Contains(got, "verdict changed") {
		t.Errorf("a budget that held:\n%s", got)
	}
	// A plan that took the open incident off already: the ledger's own figure is
	// what the charge is set against (5m 00s effective + 1m 00s pending = 6m 00s).
	got = join(stages(planOpt("within", 300, rdI(60)), rdI(400)))
	if !strings.Contains(got, "planned 6m 00s left → within; the incident was charged 6m 40s, so overspent by 40s → exceeds") {
		t.Errorf("a plan with a pending incident:\n%s", got)
	}
	// "may exceed" that came to fit is not a changed verdict; one that did not is.
	if got := join(stages(planOpt("may_exceed", 100, nil), rdI(50))); strings.Contains(got, "verdict changed") {
		t.Errorf("may_exceed that fit:\n%s", got)
	}
	if got := join(stages(planOpt("may_exceed", 100, nil), rdI(150))); !strings.Contains(got, "verdict changed") {
		t.Errorf("may_exceed that did not:\n%s", got)
	}
	// Nothing to set against: no incident length, no verdict (no SLO).
	if got := join(stages(planOpt("within", 383, nil), nil)); strings.Contains(got, "budget  ") {
		t.Errorf("a budget row with no incident length:\n%s", got)
	}
	if got := join(stages(planOpt("no_objective", 383, nil), rdI(423))); strings.Contains(got, "budget  ") {
		t.Errorf("a budget row with no objective:\n%s", got)
	}
	// The open incident the verdict took off is said in the option.
	opt := rdOption(audit.DecisionOption{Name: "point_in_time_restore", BudgetVerdict: "exceeds", BudgetRemainingSeconds: rdI(-40), BudgetPendingSeconds: rdI(423)})
	if !strings.Contains(opt, "(-40s left after the 7m 03s the open incident had used, charged when the restore runs)") {
		t.Errorf("pending incident in the option: %s", opt)
	}
}

// The gate line carries the ticket the approval was given under, and the
// approver's justification sits under it as their own words.
func TestFormatRecoveryDecisions_GateShowsTicketAndJustification(t *testing.T) {
	gate := func(a audit.ApprovalFacts) narrativeRecoveryDecision {
		return narrativeRecoveryDecision{At: rdAt(2), Decision: audit.RecoveryDecision{
			Stage: "gate", Decision: "approved", DecidedBy: "alice", Approval: &a}}
	}
	const why = "batch corrupted the ledger; the on-call lead accepted the loss on the incident call at 14:10"
	got := formatRecoveryDecisions([]narrativeRecoveryDecision{gate(audit.ApprovalFacts{
		AcknowledgedDataLoss: true, AcknowledgedBreach: true, Justification: why, Ticket: "INC-1234"})})
	for _, want := range []string{
		"by alice; acknowledged: data loss, exceeding an objective if it comes to that; ticket INC-1234",
		"justification (the approver's words): batch corrupted the ledger;",
		"incident call at 14:10",
	} {
		// The gate line wraps, so it is read as one line.
		if !strings.Contains(rdUnwrap(got), rdUnwrap(want)) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(got), " "), "the on-call lead accepted the loss on the incident call at 14:10") {
		t.Errorf("the justification was cut:\n%s", got)
	}

	// With neither, the gate line is what it was.
	plain := formatRecoveryDecisions([]narrativeRecoveryDecision{gate(audit.ApprovalFacts{AcknowledgedDataLoss: true})})
	if strings.Contains(plain, "ticket") || strings.Contains(plain, "justification") {
		t.Errorf("a ticket or justification is shown when none was given:\n%s", plain)
	}
	// A justification is shown on the gate stage only: an execution event that
	// happens to carry one does not repeat it.
	exec := narrativeRecoveryDecision{At: rdAt(3), Decision: audit.RecoveryDecision{
		Stage: "execution", Decision: "proceeded", Approval: &audit.ApprovalFacts{Justification: why}}}
	if strings.Contains(formatRecoveryDecisions([]narrativeRecoveryDecision{exec}), "justification") {
		t.Error("the justification is repeated on the execution stage")
	}
}

func TestFormatRecoveryDecisions_ALowerBoundIsSaidAsOne(t *testing.T) {
	render := func(opt audit.DecisionOption) string {
		return formatRecoveryDecisions([]narrativeRecoveryDecision{{At: rdAt(1), Decision: audit.RecoveryDecision{
			Stage: "plan", Decision: "planned", Options: []audit.DecisionOption{opt}}}})
	}
	withBytes := render(audit.DecisionOption{Name: "point_in_time_restore", LossBasis: "live_instance", LossBytes: rdU(448 << 20),
		LossAtLeastSeconds: rdI(16*86400 + 13*3600 + 28*60), Verdict: "exceeds", Gate: "refuse"})
	withBytes = rdUnwrap(withBytes)
	for _, want := range []string{"loss 448.0 MiB of WAL (live_instance, exact); at least 16d 13h 28m of history", "→ exceeds", "enforcement refuses it"} {
		if !strings.Contains(withBytes, want) {
			t.Errorf("missing %q in:\n%s", want, withBytes)
		}
	}
	if strings.Contains(withBytes, "at least 16d 13h 28m of history (live_instance, exact)") {
		t.Errorf("the lower bound is labelled exact:\n%s", withBytes)
	}
	only := render(audit.DecisionOption{Name: "point_in_time_restore", LossAtLeastSeconds: rdI(600), Verdict: "not_comparable"})
	if !strings.Contains(only, "point_in_time_restore: loss at least 10m 00s of history → not_comparable") {
		t.Errorf("a bound on its own:\n%s", only)
	}
	upper := render(audit.DecisionOption{Name: "x", LossBasis: "archive_end", LossBytes: rdU(32 << 20), LossUpperBound: true, LossAtLeastSeconds: rdI(600)})
	if !strings.Contains(upper, "loss 32.0 MiB of WAL (archive_end, at most); at least 10m 00s of history") {
		t.Errorf("upper-bounded bytes beside a lower bound:\n%s", upper)
	}
}

func TestRecoverySummaryLine(t *testing.T) {
	if got := recoverySummaryLine("refused", "exceeds"); got != "refused (exceeds the recovery objective)" {
		t.Errorf("got %q", got)
	}
	if got := recoverySummaryLine("planned", ""); got != "planned" {
		t.Errorf("got %q", got)
	}
}

// The journey's --detail view takes the decisions from the run's events, in
// order, and ignores every other event type.
func TestRecoveryDecisionsFromEvents(t *testing.T) {
	d := audit.RecoveryDecision{Stage: "execution", Decision: "refused"}
	events := []journeyEvent{
		{EventID: "e1", EventType: "tool_execution", Timestamp: "2026-11-04T14:00:00Z"},
		{EventID: "e2", EventType: "recovery_decision", Timestamp: "2026-11-04T14:03:00.123456789Z", RecoveryDecision: &d},
		{EventID: "e3", EventType: "recovery_decision", Timestamp: "2026-11-04T14:04:00Z"}, // no payload: skipped
		{EventID: "e4", EventType: "agent_reasoning", Timestamp: "2026-11-04T14:05:00Z"},
	}
	got := recoveryDecisionsFromEvents(events)
	if len(got) != 1 || got[0].EventID != "e2" || got[0].Decision.Decision != "refused" || got[0].At.Minute() != 3 {
		t.Errorf("got %+v", got)
	}
	if recoveryDecisionsFromEvents(nil) != nil {
		t.Error("no events gave decisions")
	}
	// What the gateway sends for a run's events decodes into journeyEvent.
	var ev journeyEvent
	body := `{"event_id":"rdc_1","timestamp":"2026-11-04T14:03:00Z","event_type":"recovery_decision",
	  "recovery_decision":{"stage":"plan","decision":"planned","options":[{"name":"point_in_time_restore","verdict":"exceeds"}]}}`
	if err := json.Unmarshal([]byte(body), &ev); err != nil || ev.RecoveryDecision == nil || ev.RecoveryDecision.Options[0].Verdict != "exceeds" {
		t.Errorf("decode: %+v, %v", ev, err)
	}
}

// vault journey --detail asks the gateway for recovery decisions too.
func TestFetchRunEvents_AsksForRecoveryDecisions(t *testing.T) {
	var gotTypes string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTypes = r.URL.Query().Get("types")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()
	if _, err := fetchRunEvents(srv.URL, "", "plr_x"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotTypes, "recovery_decision") {
		t.Errorf("types = %q", gotTypes)
	}
}

func TestFormatRecoveryDecisions_NothingToShow(t *testing.T) {
	if got := formatRecoveryDecisions(nil); got != "" {
		t.Errorf("got %q", got)
	}
}

// The gateway's narrative decodes into the CLI's struct: the JSON names agree.
func TestIncidentNarrative_DecodesRecoveryDecisionsFromTheGateway(t *testing.T) {
	body := `{"incident_id":"plr_x","recovery_decisions":[{"at":"2026-11-04T14:03:00Z","event_id":"rdc_1",
	  "decision":{"stage":"execution","decision":"refused","reason":"r","target":"db",
	  "objectives":{"rpo_seconds":900,"rpo_source":"environment:prod","enforcement":"acknowledge","config_digest":"abcd"},
	  "options":[{"name":"point_in_time_restore","loss_seconds":65563,"verdict":"exceeds","gate":"acknowledge"}],
	  "approval":{"approved_by":"alice","acknowledged_breach":true}}}]}`
	var n incidentNarrative
	if err := json.Unmarshal([]byte(body), &n); err != nil {
		t.Fatal(err)
	}
	if len(n.RecoveryDecisions) != 1 {
		t.Fatalf("decisions = %+v", n.RecoveryDecisions)
	}
	d := n.RecoveryDecisions[0]
	if d.EventID != "rdc_1" || d.Decision.Decision != "refused" || *d.Decision.Objectives.RPOSeconds != 900 ||
		d.Decision.Options[0].Verdict != "exceeds" || !d.Decision.Approval.AcknowledgedBreach || d.At.Minute() != 3 {
		t.Errorf("decoded = %+v", d)
	}
	out := formatRecoveryDecisions(n.RecoveryDecisions)
	if !strings.Contains(out, "refused") || !strings.Contains(out, "rpo 15m 00s [environment:prod]") {
		t.Errorf("rendered:\n%s", out)
	}
}

// A duration is never split across lines, and a long stage line continues under
// its own text.
func TestRDWrap_KeepsDurationsWhole(t *testing.T) {
	got := rdWrap("took 1m 29s (3s under) and then 11m 55s the open incident had used, 1h 06m more", 20, "  ")
	for _, bad := range []string{"1m\n", "11m\n", "1h\n"} {
		if strings.Contains(got, bad) {
			t.Errorf("a duration was split (%q):\n%s", bad, got)
		}
	}
	if rdUnwrap(got) != "took 1m 29s (3s under) and then 11m 55s the open incident had used, 1h 06m more" {
		t.Errorf("text changed:\n%s", got)
	}

	out := formatRecoveryDecisions([]narrativeRecoveryDecision{{At: rdAt(2), Decision: audit.RecoveryDecision{
		Stage: "gate", Decision: "approved", DecidedBy: "alice@example.com", RecoveryTarget: "time=2026-11-18T02:49:55Z",
		Approval: &audit.ApprovalFacts{ApprovedBy: "alice", AcknowledgedDataLoss: true, AllowedStopServing: true, AcknowledgedBreach: true, Ticket: "INC-1239"}}}})
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if len([]rune(l)) > 29+1+80 {
			t.Errorf("stage line too wide (%d): %q", len([]rune(l)), l)
		}
	}
	if !strings.Contains(out, "\n                              ") {
		t.Errorf("no continuation under the text:\n%s", out)
	}
}

func TestRDWrap_EdgeCases(t *testing.T) {
	// A word longer than the width is kept whole on a line of its own.
	long := strings.Repeat("x", 30)
	if got := rdWrap("a "+long+" b", 10, "  "); got != "a\n  "+long+"\n  b" {
		t.Errorf("long word: %q", got)
	}
	// A unit followed by a number with no unit of its own is not a duration.
	if got := rdWrap("after 5m 12 commits", 4, ""); got != "after\n5m\n12\ncommits" {
		t.Errorf("a number that is not a duration was glued: %q", got)
	}
	// Three-part spans and a span with trailing punctuation stay whole.
	if got := rdWrap("lost 16d 13h 28m, then", 12, ""); got != "lost\n16d 13h 28m,\nthen" {
		t.Errorf("a long span was split: %q", got)
	}
	// Nothing, and only spaces.
	if rdWrap("", 10, "  ") != "" || rdWrap("   ", 10, "  ") != "" {
		t.Error("empty text is not empty")
	}

	// An event with no time is stamped "–", one column narrower than a time; the
	// continuation still sits under the text.
	out := formatRecoveryDecisions([]narrativeRecoveryDecision{{Decision: audit.RecoveryDecision{
		Stage: "gate", Decision: "approved", DecidedBy: "alice@example.com",
		Approval: &audit.ApprovalFacts{ApprovedBy: "alice", AcknowledgedDataLoss: true, AllowedStopServing: true, AcknowledgedBreach: true, Ticket: "INC-1239"}}}})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "      –  gate") {
		t.Fatalf("unexpected rendering:\n%s", out)
	}
	textCol := len([]rune("      –  gate      approved ")) // the text starts after this and a space
	if want := strings.Repeat(" ", textCol+1); !strings.HasPrefix(lines[1], want) || strings.TrimSpace(lines[1][len(want)-1:]) == "" {
		t.Errorf("continuation is not under the text:\n%s", out)
	}
}

// A long row continues under its own text, never back under the label.
func TestRDPlanVsActual_RowsWrapUnderTheText(t *testing.T) {
	ds := []narrativeRecoveryDecision{
		{At: rdAt(1), Decision: audit.RecoveryDecision{Stage: "plan", Decision: "planned", Options: []audit.DecisionOption{{
			Name: "point_in_time_restore", BudgetVerdict: "within", BudgetRemainingSeconds: rdI(287)}}}},
		{At: rdAt(3), Decision: audit.RecoveryDecision{Stage: "execution", Decision: "proceeded", Options: []audit.DecisionOption{{Name: "point_in_time_restore", Chosen: true}}}},
		{At: rdAt(5), Decision: audit.RecoveryDecision{Stage: "outcome", Decision: "resolved", DurationSeconds: rdI(89), IncidentSeconds: rdI(798)}},
	}
	var lines []string
	for _, l := range rdWrapRows(rdPlanVsActual(ds)) {
		lines = append(lines, strings.Split(l, "\n")...)
	}
	rows := 0
	for i, l := range lines {
		if !strings.HasPrefix(l, "  budget         ") {
			continue
		}
		rows++
		if len(l) > 17+70 {
			t.Errorf("row too wide (%d): %q", len(l), l)
		}
		next := lines[i+1]
		if !strings.HasPrefix(next, strings.Repeat(" ", 17)) || strings.TrimSpace(next) == "" {
			t.Errorf("continuation is not under the text: %q", next)
		}
	}
	if rows != 1 {
		t.Fatalf("budget rows = %d:\n%s", rows, strings.Join(lines, "\n"))
	}
	// The footnote and the header are not touched.
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "\n                 \"at the stop\" is measured") || !strings.Contains(out, "\n                 instance; what was discarded is not counted afterwards") || !strings.Contains(out, "  ═══ PLAN VS ACTUAL ═══  the plan of 14:01:00 against what happened") {
		t.Errorf("header or footnote changed:\n%s", out)
	}
}
