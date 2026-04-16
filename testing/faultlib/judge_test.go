package faultlib

import (
	"context"
	"errors"
	"testing"
)

// mockCompleter returns a fixed response string for every call.
func mockCompleter(response string, err error) TextCompleter {
	return func(_ context.Context, _ string) (string, error) {
		return response, err
	}
}

// ── Judge ────────────────────────────────────────────────────────────────

func TestJudge_NilCompleter(t *testing.T) {
	f := failureWithNarrative("Test fault", "The agent should identify connection exhaustion.")
	result := Judge(context.Background(), f, "some response", nil, "")
	if !result.Skipped {
		t.Error("Judge with nil completer should return Skipped=true")
	}
	if result.Score != 0 {
		t.Errorf("Score = %.2f, want 0 when skipped", result.Score)
	}
}

func TestJudge_EmptyNarrative(t *testing.T) {
	f := Failure{
		ID:   "test",
		Name: "Test",
		Evaluation: EvalSpec{
			ExpectedDiagnosis: DiagnosisSpec{Category: "some_category", Narrative: ""},
		},
	}
	result := Judge(context.Background(), f, "response", mockCompleter(`{"score":3,"reasoning":"ok"}`, nil), "model")
	if !result.Skipped {
		t.Error("Judge with empty narrative should return Skipped=true")
	}
}

func TestJudge_Score3(t *testing.T) {
	f := failureWithNarrative("Max connections", "Agent identifies max_connections exhaustion.")
	result := Judge(context.Background(), f, "max_connections reached", mockCompleter(`{"score":3,"reasoning":"correct"}`, nil), "test-model")
	if result.Skipped {
		t.Fatal("Judge should not be skipped")
	}
	if result.Score != 1.0 {
		t.Errorf("Score = %.2f, want 1.0 for score=3", result.Score)
	}
	if result.Reasoning != "correct" {
		t.Errorf("Reasoning = %q, want %q", result.Reasoning, "correct")
	}
	if result.Model != "test-model" {
		t.Errorf("Model = %q, want %q", result.Model, "test-model")
	}
}

func TestJudge_Score2(t *testing.T) {
	f := failureWithNarrative("Lock contention", "Agent identifies deadlock.")
	result := Judge(context.Background(), f, "deadlock detected", mockCompleter(`{"score":2,"reasoning":"root cause ok, no fix suggested"}`, nil), "m")
	if result.Score != 0.67 {
		t.Errorf("Score = %.2f, want 0.67 for score=2", result.Score)
	}
}

func TestJudge_Score1(t *testing.T) {
	f := failureWithNarrative("Table bloat", "Agent identifies dead tuple bloat.")
	result := Judge(context.Background(), f, "slow queries observed", mockCompleter(`{"score":1,"reasoning":"symptom only"}`, nil), "m")
	if result.Score != 0.33 {
		t.Errorf("Score = %.2f, want 0.33 for score=1", result.Score)
	}
}

func TestJudge_Score0(t *testing.T) {
	f := failureWithNarrative("Auth failure", "Agent identifies wrong password.")
	result := Judge(context.Background(), f, "everything is fine", mockCompleter(`{"score":0,"reasoning":"completely wrong"}`, nil), "m")
	if result.Score != 0.0 {
		t.Errorf("Score = %.2f, want 0.0 for score=0", result.Score)
	}
}

func TestJudge_CompleterError(t *testing.T) {
	f := failureWithNarrative("Test", "narrative")
	result := Judge(context.Background(), f, "response", mockCompleter("", errors.New("LLM unavailable")), "m")
	if !result.Skipped {
		t.Error("Judge should be skipped when completer returns an error")
	}
	if result.Reasoning == "" {
		t.Error("Reasoning should describe the error when judge call fails")
	}
}

func TestJudge_InvalidJSON(t *testing.T) {
	f := failureWithNarrative("Test", "narrative")
	result := Judge(context.Background(), f, "response", mockCompleter("not json at all", nil), "m")
	if !result.Skipped {
		t.Error("Judge should be skipped when LLM returns unparseable JSON")
	}
	if result.Reasoning == "" {
		t.Error("Reasoning should describe the parse failure")
	}
}

func TestJudge_OutOfRangeScore(t *testing.T) {
	f := failureWithNarrative("Test", "narrative")
	// score=5 is not in [0,3] — should default to 0.0.
	result := Judge(context.Background(), f, "response", mockCompleter(`{"score":5,"reasoning":"oob"}`, nil), "m")
	if result.Skipped {
		t.Fatal("should not be skipped for valid JSON with out-of-range score")
	}
	if result.Score != 0.0 {
		t.Errorf("Score = %.2f, want 0.0 for out-of-range input", result.Score)
	}
}

func TestJudge_MarkdownFencedJSON(t *testing.T) {
	f := failureWithNarrative("Test", "narrative")
	// LLM wraps response in markdown fences — extractJSON should strip them.
	raw := "```json\n{\"score\":3,\"reasoning\":\"perfect\"}\n```"
	result := Judge(context.Background(), f, "response", mockCompleter(raw, nil), "m")
	if result.Skipped {
		t.Fatal("Judge should parse fenced JSON successfully")
	}
	if result.Score != 1.0 {
		t.Errorf("Score = %.2f, want 1.0", result.Score)
	}
}

// ── extractJSON ───────────────────────────────────────────────────────────

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bare JSON",
			input: `{"score":3,"reasoning":"ok"}`,
			want:  `{"score":3,"reasoning":"ok"}`,
		},
		{
			name:  "fenced with backticks",
			input: "```json\n{\"score\":2,\"reasoning\":\"partial\"}\n```",
			want:  `{"score":2,"reasoning":"partial"}`,
		},
		{
			name:  "trailing prose after JSON",
			input: `{"score":1,"reasoning":"symptom"} additional text`,
			want:  `{"score":1,"reasoning":"symptom"}`,
		},
		{
			name:  "leading prose before JSON",
			input: `Here is my evaluation: {"score":0,"reasoning":"wrong"}`,
			want:  `{"score":0,"reasoning":"wrong"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractJSON(tc.input)
			if got != tc.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ── EvaluateWithJudge ────────────────────────────────────────────────────

func TestEvaluateWithJudge_JudgeEnabled_WeightsShift(t *testing.T) {
	// Judge returns score=3 (1.0). With judge-enabled weights:
	// tool=1.0 (check_connection matched), judge=1.0, keyword=1.0
	// Score = 1.0*0.40 + 1.0*0.40 + 1.0*0.20 = 1.0
	f := Failure{
		ID:       "test",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools:    []string{"check_connection"},
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"refused"}},
			ExpectedDiagnosis: DiagnosisSpec{
				Category:  "connection_refused",
				Narrative: "The agent should identify that connections are being refused.",
			},
		},
	}
	response := "connection refused — cannot connect to server"
	completer := mockCompleter(`{"score":3,"reasoning":"correct root cause"}`, nil)

	result := EvaluateWithJudge(context.Background(), f, response, completer, "test-model")

	if result.JudgeSkipped {
		t.Error("JudgeSkipped should be false when narrative is set and completer works")
	}
	if result.DiagnosisScore != 1.0 {
		t.Errorf("DiagnosisScore = %.2f, want 1.0 (judge score=3)", result.DiagnosisScore)
	}
	if result.JudgeReasoning != "correct root cause" {
		t.Errorf("JudgeReasoning = %q, want %q", result.JudgeReasoning, "correct root cause")
	}
	if result.JudgeModel != "test-model" {
		t.Errorf("JudgeModel = %q, want %q", result.JudgeModel, "test-model")
	}
	if result.Score != 1.0 {
		t.Errorf("Score = %.2f, want 1.0 with all-pass judge run", result.Score)
	}
	if !result.Passed {
		t.Error("Passed should be true")
	}
}

func TestEvaluateWithJudge_JudgeSkipped_BackwardCompatWeights(t *testing.T) {
	// When judge is skipped (no narrative), weights stay 0.50/0.30/0.20.
	// keyword=1.0, diagnosis=1.0 (connection+refused matched), tool=1.0
	// Score = 0.50 + 0.30 + 0.20 = 1.0
	f := Failure{
		ID:       "test",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools:    []string{"check_connection"},
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"refused"}},
			ExpectedDiagnosis: DiagnosisSpec{
				Category:  "connection_refused",
				Narrative: "", // empty → judge skipped
			},
		},
	}
	response := "connection refused — cannot connect to server"
	completer := mockCompleter(`{"score":3,"reasoning":"x"}`, nil)

	result := EvaluateWithJudge(context.Background(), f, response, completer, "m")

	if !result.JudgeSkipped {
		t.Error("JudgeSkipped should be true when narrative is empty")
	}
	if result.Score != 1.0 {
		t.Errorf("Score = %.2f, want 1.0 (backward-compat path)", result.Score)
	}
}

func TestEvaluateWithJudge_NilCompleter_BackwardCompatWeights(t *testing.T) {
	// nil completer → judge skipped → backward-compat weights apply.
	f := Failure{
		ID:       "test",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"refused"}},
			ExpectedDiagnosis: DiagnosisSpec{
				Narrative: "The agent should identify connection refused.",
			},
		},
	}
	response := "connection refused"
	result := EvaluateWithJudge(context.Background(), f, response, nil, "")

	if !result.JudgeSkipped {
		t.Error("JudgeSkipped should be true when completer is nil")
	}
	// keyword=1.0, diagnosis=1.0 (no category → defaults to pass), tool=1.0
	// Score = 1.0*0.50 + 1.0*0.30 + 1.0*0.20 = 1.0
	if result.Score != 1.0 {
		t.Errorf("Score = %.2f, want 1.0 (no diagnosis category defaults to pass)", result.Score)
	}
}

func TestEvaluateWithJudge_LowJudgeScore_MayFail(t *testing.T) {
	// Judge returns score=0 (0.0). Keyword fails too.
	// tool=1.0, judge=0.0, keyword=0.0
	// Score = 1.0*0.40 + 0.0*0.40 + 0.0*0.20 = 0.40 < 0.60 → Passed=false
	f := Failure{
		ID:       "test",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools:    []string{"check_connection"},
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"refused"}},
			ExpectedDiagnosis: DiagnosisSpec{
				Narrative: "The agent should identify connection refused.",
			},
		},
	}
	response := "connection open, everything works" // no "refused" keyword
	completer := mockCompleter(`{"score":0,"reasoning":"completely wrong"}`, nil)

	result := EvaluateWithJudge(context.Background(), f, response, completer, "m")

	if result.JudgeSkipped {
		t.Fatal("JudgeSkipped should be false")
	}
	if result.Passed {
		t.Errorf("Passed should be false when judge=0 and keyword fails; Score=%.2f", result.Score)
	}
	if result.DiagnosisScore != 0.0 {
		t.Errorf("DiagnosisScore = %.2f, want 0.0 for judge score=0", result.DiagnosisScore)
	}
}

func TestEvaluateWithJudge_DiagnosisPassThreshold(t *testing.T) {
	// Judge score=0.67 (score=2): DiagnosisPass = score >= 0.5 → true.
	// Judge score=0.33 (score=1): DiagnosisPass = score >= 0.5 → false.
	f := func(narrative string) Failure {
		return Failure{
			ID:       "test",
			Category: "database",
			Evaluation: EvalSpec{
				ExpectedDiagnosis: DiagnosisSpec{Narrative: narrative},
			},
		}
	}

	r2 := EvaluateWithJudge(context.Background(), f("narrative"),
		"response", mockCompleter(`{"score":2,"reasoning":"partial"}`, nil), "m")
	if !r2.DiagnosisPass {
		t.Error("DiagnosisPass should be true for judge score=0.67 (>=0.5)")
	}

	r1 := EvaluateWithJudge(context.Background(), f("narrative"),
		"response", mockCompleter(`{"score":1,"reasoning":"symptom only"}`, nil), "m")
	if r1.DiagnosisPass {
		t.Error("DiagnosisPass should be false for judge score=0.33 (<0.5)")
	}
}

// ── DiagnosisScore in base Evaluate ──────────────────────────────────────

func TestEvaluate_DiagnosisScorePopulated(t *testing.T) {
	f := Failure{
		ID:       "test",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedDiagnosis: DiagnosisSpec{Category: "connection_exhaustion"},
		},
	}

	// "connection" matches (1/2 words = 0.5 ratio).
	result := Evaluate(f, "The connection pool is saturated.")

	if result.DiagnosisScore == 0 && result.DiagnosisPass {
		t.Error("DiagnosisScore should be non-zero when DiagnosisPass is true")
	}
	if result.DiagnosisScore > 1.0 {
		t.Errorf("DiagnosisScore = %.2f, must be <= 1.0", result.DiagnosisScore)
	}
}

func TestEvaluate_DiagnosisScoreZeroNoCategory(t *testing.T) {
	// No category → diagnosisScore defaults to 1.0 (pass by default).
	f := Failure{
		ID:       "test",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedDiagnosis: DiagnosisSpec{Category: ""},
		},
	}
	result := Evaluate(f, "any response")
	if result.DiagnosisScore != 1.0 {
		t.Errorf("DiagnosisScore = %.2f, want 1.0 when no category configured", result.DiagnosisScore)
	}
	if !result.DiagnosisPass {
		t.Error("DiagnosisPass should be true when no category configured")
	}
}

// ── Catalog: narratives present for db + host faults ─────────────────────

func TestCatalog_DbAndHostFaultsHaveNarratives(t *testing.T) {
	catalogPath := findCatalog()
	if catalogPath == "" {
		t.Skip("Could not find catalog/failures.yaml")
	}

	catalog, err := LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}

	missing := 0
	for _, f := range catalog.Failures {
		if f.Category != "database" && f.Category != "host" {
			continue
		}
		if f.Evaluation.ExpectedDiagnosis.Narrative == "" {
			t.Errorf("fault %q (category=%s) has no narrative in expected_diagnosis", f.ID, f.Category)
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d db/host fault(s) are missing narratives", missing)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func failureWithNarrative(name, narrative string) Failure {
	return Failure{
		ID:          "test-" + name,
		Name:        name,
		Description: "A test fault",
		Evaluation: EvalSpec{
			ExpectedDiagnosis: DiagnosisSpec{
				Narrative: narrative,
			},
		},
	}
}
