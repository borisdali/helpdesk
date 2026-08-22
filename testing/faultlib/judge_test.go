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
//
// The tests formerly here (weight-shift, backward-compat, nil-completer,
// low-judge-score, diagnosis-pass-threshold, plus the two base-Evaluate
// DiagnosisScore tests below) tested this package's own EvaluateWithJudge/
// Evaluate — deleted along with them (item 7 dedup, v0.26: confirmed zero
// production callers; cmd/faulttest's own EvaluateWithJudge/Evaluate are the
// only ones actually exercised) — already covered under equivalent or
// stronger names in testing/cmd/faulttest/judge_test.go (which additionally
// covers judgeVeto, a refinement this package's version never had).

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
