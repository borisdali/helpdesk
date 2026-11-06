package faultlib

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

func trailEvent(id string, d audit.RecoveryDecision) audit.Event {
	return audit.Event{EventID: id, RecoveryDecision: &d}
}

func completeTrail() []audit.Event {
	obj := &audit.ObjectivesInForce{Enforcement: "acknowledge"}
	secs := int64(90)
	return []audit.Event{
		trailEvent("e1", audit.RecoveryDecision{Stage: audit.RecoveryStagePlan, Decision: audit.RecoveryDecisionPlanned, Objectives: obj}),
		trailEvent("e2", audit.RecoveryDecision{Stage: audit.RecoveryStageGate, Decision: audit.RecoveryDecisionApproved}),
		trailEvent("e3", audit.RecoveryDecision{Stage: audit.RecoveryStageExecution, Decision: audit.RecoveryDecisionProceeded, Objectives: obj}),
		trailEvent("e4", audit.RecoveryDecision{Stage: audit.RecoveryStageOutcome, Decision: audit.OutcomeResolved, DurationSeconds: &secs}),
	}
}

func TestRecoveryTrailProblems(t *testing.T) {
	if got := recoveryTrailProblems(completeTrail()); len(got) != 0 {
		t.Fatalf("a complete trail has problems: %v", got)
	}

	without := func(stage string) []audit.Event {
		var out []audit.Event
		for _, ev := range completeTrail() {
			if ev.RecoveryDecision.Stage != stage {
				out = append(out, ev)
			}
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		events []audit.Event
		want   string
	}{
		{"nothing at all", nil, "no recovery_decision events"},
		{"no plan", without(audit.RecoveryStagePlan), "no plan stage"},
		{"no gate", without(audit.RecoveryStageGate), "no gate stage"},
		{"no execution", without(audit.RecoveryStageExecution), "no execution stage"},
		{"no outcome", without(audit.RecoveryStageOutcome), "no outcome stage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(recoveryTrailProblems(tc.events), "; ")
			if !strings.Contains(got, tc.want) {
				t.Errorf("problems = %q, want one containing %q", got, tc.want)
			}
		})
	}

	t.Run("a refused execution is not a proceeded one", func(t *testing.T) {
		ev := completeTrail()
		ev[2].RecoveryDecision.Decision = audit.RecoveryDecisionRefused
		if got := strings.Join(recoveryTrailProblems(ev), ";"); !strings.Contains(got, "no execution stage with decision") {
			t.Errorf("problems = %q", got)
		}
	})
	t.Run("an outcome without a duration", func(t *testing.T) {
		ev := completeTrail()
		ev[3].RecoveryDecision.DurationSeconds = nil
		if got := strings.Join(recoveryTrailProblems(ev), ";"); !strings.Contains(got, "no duration") {
			t.Errorf("problems = %q", got)
		}
	})
	t.Run("objectives missing from a stage that decides", func(t *testing.T) {
		ev := completeTrail()
		ev[0].RecoveryDecision.Objectives = nil
		if got := strings.Join(recoveryTrailProblems(ev), ";"); !strings.Contains(got, "objectives in force") {
			t.Errorf("problems = %q", got)
		}
	})
	t.Run("proceeding past an exceeded objective needs the acknowledgement on record", func(t *testing.T) {
		ev := completeTrail()
		ev[2].RecoveryDecision.Options = []audit.DecisionOption{{Name: "point_in_time_restore", Verdict: "exceeds"}}
		if got := strings.Join(recoveryTrailProblems(ev), ";"); !strings.Contains(got, "no acknowledgement recorded") {
			t.Errorf("problems = %q", got)
		}
		ev[2].RecoveryDecision.Approval = &audit.ApprovalFacts{AcknowledgedBreach: true}
		if got := recoveryTrailProblems(ev); len(got) != 0 {
			t.Errorf("an acknowledged breach was reported: %v", got)
		}
		// Within the objective, nothing to acknowledge.
		ev[2].RecoveryDecision.Approval = nil
		ev[2].RecoveryDecision.Options[0].Verdict = "within"
		if got := recoveryTrailProblems(ev); len(got) != 0 {
			t.Errorf("a restore within the objective was reported: %v", got)
		}
	})
	t.Run("a password in the clear, and a masked one", func(t *testing.T) {
		ev := completeTrail()
		ev[0].RecoveryDecision.Target = "host=db port=5432 password=hunter2"
		if got := strings.Join(recoveryTrailProblems(ev), ";"); !strings.Contains(got, "password in the clear") {
			t.Errorf("a clear password was not found: %q", got)
		}
		ev[0].RecoveryDecision.Target = audit.RedactConnString("host=db port=5432 password=hunter2")
		if got := recoveryTrailProblems(ev); len(got) != 0 {
			t.Errorf("a masked password was reported: %v", got)
		}
	})
}

// The fetch, the de-duplication across keys, and the chain verification, against
// a stand-in auditd.
func TestCheckRecoveryTrail(t *testing.T) {
	valid := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/verify":
			_ = json.NewEncoder(w).Encode(audit.ChainStatus{Valid: valid, Error: "mismatch"})
		case r.URL.Path == "/v1/events" && r.URL.Query().Get("event_type") == "recovery_decision":
			evs := completeTrail()
			// Each key returns the whole trail: the same events reached by two keys.
			_ = json.NewEncoder(w).Encode(evs)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	since := time.Now().Add(-time.Hour)
	if got := CheckRecoveryTrail(srv.URL, "", []string{"trace-a", "run-b", ""}, since); len(got) != 0 {
		t.Errorf("a complete trail reached by two keys: %v", got)
	}
	valid = false
	if got := strings.Join(CheckRecoveryTrail(srv.URL, "", []string{"trace-a"}, since), ";"); !strings.Contains(got, "does not verify") {
		t.Errorf("a broken chain was not reported: %q", got)
	}
	if got := CheckRecoveryTrail("", "", []string{"x"}, since); len(got) != 1 || !strings.Contains(got[0], "FAULTTEST_AUDIT_URL") {
		t.Errorf("no audit URL: %v", got)
	}
}
