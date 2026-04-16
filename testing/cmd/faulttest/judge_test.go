package main

import (
	"context"
	"testing"

	"helpdesk/testing/faultlib"
	"helpdesk/testing/testutil"
)

// mockJudgeCompleter returns a fixed JSON response for every call.
func mockJudgeCompleter(jsonResp string) faultlib.TextCompleter {
	return func(_ context.Context, _ string) (string, error) {
		return jsonResp, nil
	}
}

func failureForJudge(narrative string) Failure {
	return Failure{
		ID:          "test-judge",
		Name:        "Judge test failure",
		Description: "A test fault for judge evaluation",
		Category:    "database",
		Evaluation: EvalSpec{
			ExpectedTools:    []string{"check_connection"},
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"refused"}},
			ExpectedDiagnosis: DiagnosisSpec{
				Category:  "connection_refused",
				Narrative: narrative,
			},
		},
	}
}

// ── EvaluateWithJudge ────────────────────────────────────────────────────

func TestEvaluateWithJudge_JudgeRuns_WeightsRebalanced(t *testing.T) {
	// Judge score=3 → DiagnosisScore=1.0
	// tool=1.0 (check_connection matched via text), keyword=1.0 ("refused")
	// Score = 1.0*0.40 + 1.0*0.40 + 1.0*0.20 = 1.0
	f := failureForJudge("The agent should identify that connections are being refused.")
	resp := testutil.AgentResponse{Text: "connection refused — cannot connect"}
	completer := mockJudgeCompleter(`{"score":3,"reasoning":"perfect diagnosis"}`)

	result := EvaluateWithJudge(context.Background(), f, resp, completer, "test-model")

	if result.JudgeSkipped {
		t.Fatal("JudgeSkipped should be false when narrative is set and completer works")
	}
	if result.DiagnosisScore != 1.0 {
		t.Errorf("DiagnosisScore = %.2f, want 1.0 for judge score=3", result.DiagnosisScore)
	}
	if result.JudgeReasoning != "perfect diagnosis" {
		t.Errorf("JudgeReasoning = %q, want %q", result.JudgeReasoning, "perfect diagnosis")
	}
	if result.JudgeModel != "test-model" {
		t.Errorf("JudgeModel = %q, want %q", result.JudgeModel, "test-model")
	}
	if result.Score != 1.0 {
		t.Errorf("Score = %.2f, want 1.0", result.Score)
	}
	if !result.Passed {
		t.Error("Passed should be true")
	}
}

func TestEvaluateWithJudge_NoNarrative_BackwardCompatWeights(t *testing.T) {
	// No narrative → judge skipped → backward-compat 0.50/0.30/0.20 weights.
	// keyword=1.0, diagnosis=1.0 (connection + refused), tool=1.0 (text match)
	// Score = 0.50 + 0.30 + 0.20 = 1.0
	f := failureForJudge("") // empty narrative → judge skips
	resp := testutil.AgentResponse{Text: "connection refused — cannot connect"}
	completer := mockJudgeCompleter(`{"score":3,"reasoning":"x"}`)

	result := EvaluateWithJudge(context.Background(), f, resp, completer, "m")

	if !result.JudgeSkipped {
		t.Error("JudgeSkipped should be true when narrative is empty")
	}
	if result.Score != 1.0 {
		t.Errorf("Score = %.2f, want 1.0 on backward-compat path", result.Score)
	}
}

func TestEvaluateWithJudge_NilCompleter_BackwardCompat(t *testing.T) {
	// nil completer → judge skipped → backward-compat weights.
	f := failureForJudge("The agent should identify connection refused.")
	resp := testutil.AgentResponse{Text: "connection refused"}
	// keyword=1.0 ("refused"), no tools expected by category, no diagnosis category match
	// keyword only: 0.50 + 0.0 + 0.20 (tool score=1.0 because no tools listed... wait)
	// Actually f has ExpectedTools: check_connection and text "connection refused" matches
	// connection pattern → tool=1.0; diagnosis category is "connection_refused" but response
	// has "connection" and "refused" so diagnosis words match.
	result := EvaluateWithJudge(context.Background(), f, resp, nil, "")

	if !result.JudgeSkipped {
		t.Error("JudgeSkipped should be true for nil completer")
	}
	if result.JudgeModel != "" {
		t.Errorf("JudgeModel = %q, want empty when judge skipped", result.JudgeModel)
	}
}

func TestEvaluateWithJudge_PartialJudgeScore(t *testing.T) {
	// Judge score=1 → DiagnosisScore=0.33 → DiagnosisPass=false (< 0.5).
	// keyword=1.0, tool=1.0 (text match), judge=0.33
	// Score = 1.0*0.40 + 0.33*0.40 + 1.0*0.20 = 0.40 + 0.132 + 0.20 = 0.732
	f := failureForJudge("The agent should identify connection refused.")
	resp := testutil.AgentResponse{Text: "connection refused — some symptom observed"}
	completer := mockJudgeCompleter(`{"score":1,"reasoning":"only identified symptom"}`)

	result := EvaluateWithJudge(context.Background(), f, resp, completer, "m")

	if result.DiagnosisScore != 0.33 {
		t.Errorf("DiagnosisScore = %.2f, want 0.33 for judge score=1", result.DiagnosisScore)
	}
	if result.DiagnosisPass {
		t.Error("DiagnosisPass should be false for judge score=0.33 (< 0.5)")
	}
}

func TestEvaluateWithJudge_JudgeFields_Populated(t *testing.T) {
	f := failureForJudge("The agent should identify connection refused.")
	resp := testutil.AgentResponse{Text: "connection refused"}
	completer := mockJudgeCompleter(`{"score":2,"reasoning":"root cause ok"}`)

	result := EvaluateWithJudge(context.Background(), f, resp, completer, "claude-haiku")

	if result.JudgeSkipped {
		t.Error("JudgeSkipped should be false")
	}
	if result.JudgeReasoning != "root cause ok" {
		t.Errorf("JudgeReasoning = %q", result.JudgeReasoning)
	}
	if result.JudgeModel != "claude-haiku" {
		t.Errorf("JudgeModel = %q, want %q", result.JudgeModel, "claude-haiku")
	}
	if result.DiagnosisScore != 0.67 {
		t.Errorf("DiagnosisScore = %.2f, want 0.67 for judge score=2", result.DiagnosisScore)
	}
}

func TestEvaluateWithJudge_AuditTools_Priority(t *testing.T) {
	// Audit tools take priority over text matching.
	f := Failure{
		ID:       "test",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools:    []string{"get_replication_status"},
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"lag"}},
			ExpectedDiagnosis: DiagnosisSpec{
				Narrative: "The agent should diagnose replication lag.",
			},
		},
	}
	// Response has no replication keywords — text match would fail.
	// But audit tools contain the exact tool name → should pass.
	resp := testutil.AgentResponse{Text: "replication lag found"}
	auditTools := []string{"get_replication_status", "get_connection_stats"}
	completer := mockJudgeCompleter(`{"score":3,"reasoning":"correct"}`)

	result := EvaluateWithJudge(context.Background(), f, resp, completer, "m", auditTools)

	if result.ToolEvidenceMode != "audit" {
		t.Errorf("ToolEvidenceMode = %q, want %q", result.ToolEvidenceMode, "audit")
	}
	if !result.ToolEvidence {
		t.Error("ToolEvidence should be true when audit tool list contains the expected tool")
	}
}

// ── DiagnosisScore in cmd/faulttest Evaluate ─────────────────────────────

func TestEvaluate_DiagnosisScoreField(t *testing.T) {
	// DiagnosisScore should be populated on the base Evaluate path.
	f := Failure{
		ID:       "test",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedDiagnosis: DiagnosisSpec{Category: "connection_exhaustion"},
		},
	}
	// "connection" matches 1/2 words → diagnosisScore=0.5, DiagnosisPass=true.
	result := Evaluate(f, testutil.AgentResponse{Text: "connection pool exhausted"})

	if result.DiagnosisScore == 0 {
		t.Error("DiagnosisScore should be non-zero when diagnosis words match")
	}
	if result.DiagnosisScore > 1.0 {
		t.Errorf("DiagnosisScore = %.2f, must be in [0, 1]", result.DiagnosisScore)
	}
}

func TestEvaluate_DiagnosisScoreDefaultsToOne_NoCategory(t *testing.T) {
	// No category → diagnosisScore=1.0 (pass by default).
	f := Failure{
		ID:       "test",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedDiagnosis: DiagnosisSpec{},
		},
	}
	result := Evaluate(f, testutil.AgentResponse{Text: "any response"})
	if result.DiagnosisScore != 1.0 {
		t.Errorf("DiagnosisScore = %.2f, want 1.0 when no category configured", result.DiagnosisScore)
	}
}

// ── OverallScore field ────────────────────────────────────────────────────

func TestEvalResult_OverallScoreZeroByDefault(t *testing.T) {
	// OverallScore is only set by cmdRun (after remediation). EvalResult
	// from Evaluate should leave it at zero (not populated by Evaluate itself).
	f := Failure{ID: "test", Category: "database"}
	result := Evaluate(f, testutil.AgentResponse{Text: "ok"})
	if result.OverallScore != 0 {
		t.Errorf("OverallScore = %.2f, want 0 when not set by cmdRun", result.OverallScore)
	}
}
