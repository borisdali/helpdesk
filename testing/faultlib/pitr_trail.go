package faultlib

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"helpdesk/internal/audit"
)

// CheckRecoveryTrail reads the recovery_decision events a point-in-time
// restore left in the audit trail and returns what is wrong with them, nothing
// when the trail is as a completed, approved restore should leave it.
//
// ids are every key the stages can be filed under: the request's trace (the
// agent's plan and execution stages) and the triage run's ID (the gateway's gate
// and outcome stages are keyed to the run). Events are de-duplicated, so
// listing a key twice is harmless.
//
// It asserts the shape rather than the content: all four stages are there, the
// gate approved and the execution proceeded, the outcome carries a duration,
// every stage names the objectives in force, no stage holds a password, and
// the chain over all of it verifies. A trail that is missing is a finding, not
// a skip: this is what the feature exists to leave behind.
func CheckRecoveryTrail(auditURL, apiKey string, ids []string, since time.Time) []string {
	if auditURL == "" {
		return []string{"no audit URL configured, so the recovery decision trail could not be read (set FAULTTEST_AUDIT_URL)"}
	}
	seen := map[string]bool{}
	var events []audit.Event
	for _, id := range ids {
		if id == "" {
			continue
		}
		for _, ev := range audit.FetchRecoveryDecisionEvents(auditURL, apiKey, id, since) {
			if !seen[ev.EventID] && ev.RecoveryDecision != nil {
				seen[ev.EventID] = true
				events = append(events, ev)
			}
		}
	}
	problems := recoveryTrailProblems(events)
	if p := chainProblem(auditURL, apiKey); p != "" {
		problems = append(problems, p)
	}
	return problems
}

// recoveryTrailProblems judges the events themselves.
func recoveryTrailProblems(events []audit.Event) []string {
	if len(events) == 0 {
		return []string{"no recovery_decision events were recorded for this restore"}
	}
	byStage := map[string][]audit.RecoveryDecision{}
	for _, ev := range events {
		byStage[ev.RecoveryDecision.Stage] = append(byStage[ev.RecoveryDecision.Stage], *ev.RecoveryDecision)
	}
	var problems []string
	has := func(stage, decision string) bool {
		for _, d := range byStage[stage] {
			if d.Decision == decision {
				return true
			}
		}
		return false
	}
	for _, want := range []struct{ stage, decision string }{
		{audit.RecoveryStagePlan, audit.RecoveryDecisionPlanned},
		{audit.RecoveryStageGate, audit.RecoveryDecisionApproved},
		{audit.RecoveryStageExecution, audit.RecoveryDecisionProceeded},
	} {
		if !has(want.stage, want.decision) {
			problems = append(problems, fmt.Sprintf("no %s stage with decision %q (have %s)", want.stage, want.decision, describeStages(byStage)))
		}
	}
	if len(byStage[audit.RecoveryStageOutcome]) == 0 {
		problems = append(problems, "no outcome stage: the duration of the recovery was not recorded")
	}
	for _, d := range byStage[audit.RecoveryStageOutcome] {
		if d.DurationSeconds == nil {
			problems = append(problems, "an outcome stage has no duration")
		}
	}
	for stage, ds := range byStage {
		for _, d := range ds {
			if stage != audit.RecoveryStageOutcome && stage != audit.RecoveryStageGate && d.Objectives == nil {
				problems = append(problems, fmt.Sprintf("the %s stage does not record the objectives in force", stage))
			}
		}
	}
	// A restore that went ahead past an exceeded objective under "acknowledge"
	// did so on someone's acknowledgement, and the record must say whose.
	for _, d := range byStage[audit.RecoveryStageExecution] {
		if d.Decision != audit.RecoveryDecisionProceeded || d.Objectives == nil || d.Objectives.Enforcement != "acknowledge" {
			continue
		}
		for _, o := range d.Options {
			if o.Verdict == "exceeds" && (d.Approval == nil || !d.Approval.AcknowledgedBreach) {
				problems = append(problems, "the restore proceeded past an exceeded objective under enforcement=acknowledge with no acknowledgement recorded")
			}
		}
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(strings.ToLower(string(raw)), "password=") && !onlyRedactedPasswords(string(raw)) {
		problems = append(problems, "a recovery decision holds a connection password in the clear")
	}
	return problems
}

// onlyRedactedPasswords reports whether every "password=" in s is followed by
// the mask RedactConnString leaves.
func onlyRedactedPasswords(s string) bool {
	low := strings.ToLower(s)
	for i := 0; ; {
		j := strings.Index(low[i:], "password=")
		if j < 0 {
			return true
		}
		i += j + len("password=")
		if !strings.HasPrefix(s[i:], "***") {
			return false
		}
	}
}

func describeStages(byStage map[string][]audit.RecoveryDecision) string {
	var parts []string
	for stage, ds := range byStage {
		for _, d := range ds {
			parts = append(parts, stage+":"+d.Decision)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// chainProblem asks auditd to verify its hash chain, which now covers the
// recovery decisions themselves.
func chainProblem(auditURL, apiKey string) string {
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(auditURL, "/")+"/v1/verify", nil) //nolint:noctx
	if err != nil {
		return "building the chain verification request: " + err.Error()
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "verifying the audit chain: " + err.Error()
	}
	defer resp.Body.Close()
	var st audit.ChainStatus
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&st) != nil {
		return fmt.Sprintf("verifying the audit chain: auditd answered %d", resp.StatusCode)
	}
	if !st.Valid {
		return fmt.Sprintf("the audit chain does not verify: %s (broken at %d)", st.Error, st.BrokenAt)
	}
	return ""
}
