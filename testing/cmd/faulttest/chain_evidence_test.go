package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helpdesk/testing/testutil"
)

// The db-pgdata-corrupted-vm fault as the catalog declares it, and the first
// hop's response as the real 2026-10-25 VM run produced it: the entry hop has
// no log tools, so it said nothing about pg_control or the data directory.
func chainTestFault() Failure {
	return Failure{
		ID:       "db-pgdata-corrupted-vm",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools:     []string{"get_pgbackrest_status"},
			ExpectedKeywords:  KeywordSpec{AnyOf: []string{"pg_control", "could not find the database system", "data directory", "corrupt", "restore"}},
			ExpectedDiagnosis: DiagnosisSpec{Category: "pgdata_corrupted"},
		},
	}
}

const chainFirstHop = "I'm beginning triage of an unavailable PostgreSQL instance on bare VM. " +
	"Determination: single_instance: true, backup_tool: pgbackrest, pitr_decided: false. ESCALATE_TO: pbs_sysadmin_host_triage"

func chainTestNarrative() *incidentNarrative {
	n := &incidentNarrative{}
	n.Triage.Findings = "single_instance=true; backup_tool=pgbackrest; pitr_decided=false"
	n.Escalations = []narrativeEscalationHop{
		{Playbook: "pbs_sysadmin_host_triage", Findings: `PostgreSQL data directory is missing pg_control; logs show "could not find the database system"`},
		{Playbook: "pbs_pgbackrest_health_triage", Findings: "pgBackRest backup is healthy with continuous WAL chain to archive end"},
	}
	n.Remediation = &struct {
		RunID                        string          `json:"run_id"`
		Playbook                     string          `json:"playbook"`
		Outcome                      string          `json:"outcome"`
		Findings                     string          `json:"findings,omitempty"`
		Transcript                   string          `json:"transcript,omitempty"`
		Steps                        []narrativeStep `json:"steps,omitempty"`
		TraceID                      string          `json:"trace_id,omitempty"`
		HasMismatch                  bool            `json:"has_mismatch,omitempty"`
		HasTargetDrift               bool            `json:"has_target_drift,omitempty"`
		HasProtocolViolation         bool            `json:"has_protocol_violation,omitempty"`
		HasUnverifiedEvidence        bool            `json:"has_unverified_evidence,omitempty"`
		UnverifiedEvidence           []string        `json:"unverified_evidence,omitempty"`
		UnverifiedEvidenceSecondary  []string        `json:"unverified_evidence_secondary,omitempty"`
		SawSignalLine                bool            `json:"saw_signal_line,omitempty"`
		ObjectiveEvidenceConfirmed   []string        `json:"objective_evidence_confirmed,omitempty"`
		ObjectiveEvidenceUnconfirmed []string        `json:"objective_evidence_unconfirmed,omitempty"`
	}{Playbook: "pbs_pgbackrest_restore_remediate", Outcome: "resolved", Findings: "Restore succeeded"}
	return n
}

func firstHopResult(t *testing.T) EvalResult {
	t.Helper()
	r := Evaluate(chainTestFault(), testutil.AgentResponse{Text: chainFirstHop})
	r.ResponseText = chainFirstHop
	return r
}

// The bug: a chain that found pg_control and restored the database failed its
// diagnostic because only the entry hop was scored.
func TestApplyChainEvidence_LaterHopsMeetTheCriteria(t *testing.T) {
	r := firstHopResult(t)
	if r.Passed || r.KeywordPass {
		t.Fatalf("precondition: the first hop alone must fail the keyword check, got %+v", r)
	}
	chain, hops := chainDiagnosticText(chainTestNarrative())
	if hops != 2 {
		t.Fatalf("hops = %d, want 2 diagnostic hops past the first", hops)
	}
	if !ApplyChainEvidence(chainTestFault(), &r, chain, hops, false) {
		t.Fatal("ApplyChainEvidence changed nothing")
	}
	if !r.KeywordPass || r.KeywordScore != 1 {
		t.Errorf("keyword criterion not met after the chain: %+v", r)
	}
	if !r.Passed || r.Score < 0.6 {
		t.Errorf("Passed=%v Score=%.4f kw=%.2f diag=%.2f tool=%.2f, want a pass (keyword 0.5 + tools 0.2 = 0.7 clears 0.6)", r.Passed, r.Score, r.KeywordScore, r.DiagnosisScore, r.ToolScore)
	}
	if r.ChainEvidenceHops != 2 {
		t.Errorf("ChainEvidenceHops = %d, want 2", r.ChainEvidenceHops)
	}
}

// The remediation hop says "restore" because it restored: that is graded by
// the remediation score, so it must not satisfy the diagnostic keywords.
func TestChainDiagnosticText_ExcludesTheRemediationHop(t *testing.T) {
	n := &incidentNarrative{}
	n.Triage.Findings = "single_instance=true"
	n.Escalations = []narrativeEscalationHop{{Findings: "health looks fine"}}
	nn := chainTestNarrative()
	n.Remediation = nn.Remediation
	text, _ := chainDiagnosticText(n)
	if strings.Contains(strings.ToLower(text), "restore") {
		t.Errorf("chain text includes the remediation hop's findings: %q", text)
	}
	r := firstHopResult(t)
	if ApplyChainEvidence(chainTestFault(), &r, text, 1, false) || r.Passed {
		t.Errorf("a chain whose diagnostic hops never mention the fault must not pass: %+v", r)
	}
}

func TestChainDiagnosticText_SingleHopAddsNothing(t *testing.T) {
	n := &incidentNarrative{}
	n.Triage.Findings = "pg_control mentioned here"
	if text, hops := chainDiagnosticText(n); text != "" || hops != 0 {
		t.Errorf("a single-hop run must add nothing, got %q, %d", text, hops)
	}
	if text, hops := chainDiagnosticText(nil); text != "" || hops != 0 {
		t.Errorf("a nil narrative must add nothing, got %q, %d", text, hops)
	}
}

// A chain can only help: an already-passing first hop is left alone.
func TestApplyChainEvidence_NeverLowersAPass(t *testing.T) {
	f := chainTestFault()
	r := Evaluate(f, testutil.AgentResponse{Text: "pg_control is missing, restore from backup. get_pgbackrest_status"})
	r.ResponseText = "pg_control is missing, restore from backup. get_pgbackrest_status"
	before := r
	chain, hops := chainDiagnosticText(chainTestNarrative())
	ApplyChainEvidence(f, &r, chain, hops, false)
	if r.Score < before.Score || (before.Passed && !r.Passed) || r.KeywordScore < before.KeywordScore {
		t.Errorf("a chain lowered a result: before %+v after %+v", before, r)
	}
}

// An evidence gate that failed the result (the expected objective signal never
// fired or was never confirmed) is not something chain text can override.
func TestApplyChainEvidence_EvidenceGatesStillFail(t *testing.T) {
	for name, set := range map[string]func(*EvalResult){
		"coverage gap": func(r *EvalResult) { r.EvidenceCoverageGap = true },
		"unconfirmed":  func(r *EvalResult) { r.EvidenceRequiredButUnconfirmed = true },
	} {
		t.Run(name, func(t *testing.T) {
			r := firstHopResult(t)
			set(&r)
			chain, hops := chainDiagnosticText(chainTestNarrative())
			ApplyChainEvidence(chainTestFault(), &r, chain, hops, false)
			if r.Passed {
				t.Errorf("an evidence-gated result passed after chain evidence: %+v", r)
			}
		})
	}
}

func TestApplyChainEvidence_ProtocolViolationCapStillApplies(t *testing.T) {
	r := firstHopResult(t)
	r.ProtocolViolation = true
	f := chainTestFault()
	chain, hops := chainDiagnosticText(chainTestNarrative())
	ApplyChainEvidence(f, &r, chain, hops, false)
	if r.Score > 0.75 {
		t.Errorf("Score = %.2f, the protocol-violation cap of 0.75 must still apply", r.Score)
	}
}

// With the LLM judge scoring the diagnosis, the chain re-checks keywords only.
func TestApplyChainEvidence_JudgedKeepsTheJudgesDiagnosis(t *testing.T) {
	r := firstHopResult(t)
	r.DiagnosisScore = 0.67
	r.DiagnosisPass = true
	r.ToolScore = 1
	chain, hops := chainDiagnosticText(chainTestNarrative())
	ApplyChainEvidence(chainTestFault(), &r, chain, hops, true)
	if r.DiagnosisScore != 0.67 {
		t.Errorf("the judge's diagnosis score was replaced: %.2f", r.DiagnosisScore)
	}
	want := 1*0.40 + 0.67*0.40 + 1*0.20
	if r.Score < want-0.001 || r.Score > want+0.001 {
		t.Errorf("Score = %.3f, want judge weights %.3f", r.Score, want)
	}
}

func TestApplyChainEvidence_EmptyChainIsANoOp(t *testing.T) {
	r := firstHopResult(t)
	before := r
	if ApplyChainEvidence(chainTestFault(), &r, "", 0, false) || r.Score != before.Score || r.Passed != before.Passed {
		t.Errorf("an empty chain changed the result: %+v", r)
	}
}

// The gateway's wire format for GET /api/v1/incidents/{run_id}, with the hop
// findings of the real 2026-10-25 VM run (plr_cea87922) as `vault incidents
// --summary` printed them. Decoding it proves the JSON tags the chain text
// relies on, which the struct-built narratives above bypass.
const chainNarrativeJSON = `{
  "incident_id": "plr_cea87922",
  "triage": {
    "run_id": "plr_cea87922",
    "playbook": "pbs_db_data_loss_triage",
    "findings": "single_instance=true; backup_tool=pgbackrest; pitr_decided=false"
  },
  "escalations": [
    {"run_id": "plr_29750e97", "playbook": "pbs_sysadmin_host_triage", "outcome": "transitioned",
     "transitioned_to": "pbs_pgbackrest_health_triage",
     "findings": "The data directory is missing the pg_control file, causing PostgreSQL to fail at startup",
     "diagnostic_report": {"hypotheses": [
        {"rank": 1, "text": "pg_control missing after a storage fault", "confidence": 0.9, "is_primary": true},
        {"rank": 2, "text": "disk full corrupted the WAL", "confidence": 0.1, "is_primary": false, "rejected_reason": "no ENOSPC in the log"}]}},
    {"run_id": "plr_58d7881b", "playbook": "pbs_pgbackrest_health_triage", "outcome": "escalated",
     "escalated_to": "pbs_pgbackrest_restore_remediate",
     "findings": "Prior findings confirmed pg_control is missing (data loss); the most recent backup is healthy"}
  ],
  "remediation": {"run_id": "plr_ea16622e", "playbook": "pbs_pgbackrest_restore_remediate", "outcome": "resolved",
                  "findings": "Restore from backup 20261025-125505F succeeded with recovery verified"}
}`

func narrativeServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/incidents/plr_cea87922" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// End to end through the real fetch and JSON decode: the 25 October failure.
func TestApplyChainEvidenceFromGateway_RealRunShape(t *testing.T) {
	srv := narrativeServer(t, http.StatusOK, chainNarrativeJSON)
	r := firstHopResult(t)
	r.RemediationAttempted, r.RemediationPassed, r.RemediationScore = true, true, 0.75
	r.OverallScore = r.Score*0.6 + r.RemediationScore*0.4

	hops, changed := applyChainEvidenceFromGateway(srv.URL, "", chainTestFault(), &r, "plr_cea87922", false)
	if !changed || hops != 2 {
		t.Fatalf("changed=%v hops=%d, want true and 2", changed, hops)
	}
	if !r.Passed || !r.KeywordPass || r.ChainEvidenceHops != 2 {
		t.Errorf("diagnostic did not pass on the chain: %+v", r)
	}
	if want := r.Score*0.6 + 0.75*0.4; r.OverallScore < want-1e-9 || r.OverallScore > want+1e-9 {
		t.Errorf("OverallScore = %.4f, want %.4f (recomputed from the raised score)", r.OverallScore, want)
	}
}

// A fetch failure must leave the first-hop result exactly as it was.
func TestApplyChainEvidenceFromGateway_FetchFailureLeavesTheResult(t *testing.T) {
	for name, status := range map[string]int{"not found": http.StatusNotFound, "server error": http.StatusInternalServerError} {
		t.Run(name, func(t *testing.T) {
			srv := narrativeServer(t, status, "nope")
			r := firstHopResult(t)
			before := r
			hops, changed := applyChainEvidenceFromGateway(srv.URL, "", chainTestFault(), &r, "plr_cea87922", false)
			if changed || hops != 0 || r.Score != before.Score || r.Passed != before.Passed || r.OverallScore != before.OverallScore {
				t.Errorf("a failed fetch changed the result: hops=%d changed=%v %+v", hops, changed, r)
			}
		})
	}
	t.Run("unparseable body", func(t *testing.T) {
		srv := narrativeServer(t, http.StatusOK, "<html>")
		r := firstHopResult(t)
		if _, changed := applyChainEvidenceFromGateway(srv.URL, "", chainTestFault(), &r, "plr_cea87922", false); changed || r.Passed {
			t.Errorf("an unparseable narrative changed the result: %+v", r)
		}
	})
}

// A single-hop run (the narrative has no later hops) gets nothing added, even
// if the triage findings happen to contain a keyword.
func TestApplyChainEvidenceFromGateway_SingleHopRun(t *testing.T) {
	srv := narrativeServer(t, http.StatusOK, `{"incident_id":"plr_cea87922","triage":{"findings":"pg_control is mentioned"}}`)
	r := firstHopResult(t)
	if hops, changed := applyChainEvidenceFromGateway(srv.URL, "", chainTestFault(), &r, "plr_cea87922", false); changed || hops != 0 || r.Passed {
		t.Errorf("a single-hop narrative changed the result: hops=%d changed=%v %+v", hops, changed, r)
	}
}

// The wire format decodes into the fields the chain text reads, and only the
// primary hypothesis of a hop is counted: a rejected alternative that
// happens to say "corrupt" must not satisfy a keyword.
func TestChainDiagnosticText_PrimaryHypothesisOnly(t *testing.T) {
	srv := narrativeServer(t, http.StatusOK, chainNarrativeJSON)
	n, err := fetchIncidentNarrative(srv.URL, "", "plr_cea87922")
	if err != nil {
		t.Fatal(err)
	}
	text, hops := chainDiagnosticText(n)
	if hops != 2 {
		t.Errorf("hops = %d, want 2", hops)
	}
	for _, want := range []string{"single_instance=true", "missing the pg_control file", "pg_control missing after a storage fault", "the most recent backup is healthy"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Errorf("chain text is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "disk full corrupted the WAL") {
		t.Errorf("a non-primary hypothesis leaked into the chain text:\n%s", text)
	}
	if strings.Contains(text, "Restore from backup") {
		t.Errorf("the remediation hop leaked into the chain text:\n%s", text)
	}
}

// A chain that ended at an escalation with no remediation hop still counts.
func TestChainDiagnosticText_EscalationsWithoutRemediation(t *testing.T) {
	n := &incidentNarrative{}
	n.Escalations = []narrativeEscalationHop{{Findings: "pg_control is missing"}}
	text, hops := chainDiagnosticText(n)
	if hops != 1 || !strings.Contains(text, "pg_control") {
		t.Errorf("got %q, %d hops", text, hops)
	}
}
