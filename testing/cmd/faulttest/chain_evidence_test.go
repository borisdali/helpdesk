package main

import (
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
