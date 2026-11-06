package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

func rdI(n int64) *int64     { return &n }
func rdU(n uint64) *uint64   { return &n }
func rdF(n float64) *float64 { return &n }

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
	for _, want := range []string{
		"Objectives in force (environment prod), config 0123456789abcdef:",
		"rpo 15m 00s [environment:prod]", "rto 30m 00s [default]", "availability 99.95% [database]",
		"preference data_first [environment:prod]", "enforcement acknowledge [environment:prod]",
		"14:01:00  plan", "14:02:00  gate", "14:03:00  execution",
		"by alice; acknowledged: data loss, stopping the instance, exceeding the objective",
		"on alice's approval",
		"point_in_time_restore (chosen): loss 18h 12m, 16.0 MiB of WAL (live_instance, exact) → exceeds; needed the breach acknowledged",
		"[time=2026-11-04T01:39:07Z]",
	} {
		if !strings.Contains(out, want) {
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
	if iRem < 0 || iDec < 0 || iEval < 0 || !(iRem < iDec && iDec < iEval) {
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
func TestFormatRecoveryDecisions_ALowerBoundIsSaidAsOne(t *testing.T) {
	render := func(opt audit.DecisionOption) string {
		return formatRecoveryDecisions([]narrativeRecoveryDecision{{At: rdAt(1), Decision: audit.RecoveryDecision{
			Stage: "plan", Decision: "planned", Options: []audit.DecisionOption{opt}}}})
	}
	withBytes := render(audit.DecisionOption{Name: "point_in_time_restore", LossBasis: "live_instance", LossBytes: rdU(448 << 20),
		LossAtLeastSeconds: rdI(16*86400 + 13*3600 + 28*60), Verdict: "exceeds", Gate: "refuse"})
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
