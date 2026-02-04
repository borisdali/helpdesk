package main

import (
	"testing"
)

func TestSplitCategory(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"connection_exhaustion", []string{"connection", "exhaustion"}},
		{"pod-crash-loop", []string{"pod", "crash", "loop"}},
		{"single", []string{"single"}},
		{"mixed_and-separated", []string{"mixed", "and", "separated"}},
		{"", []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitCategory(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("splitCategory(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i, w := range tt.want {
				if got[i] != w {
					t.Errorf("splitCategory(%q)[%d] = %q, want %q", tt.input, i, got[i], w)
				}
			}
		})
	}
}

func TestEvaluate_AllPass(t *testing.T) {
	f := Failure{
		ID:       "test-1",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools:     []string{"check_connection"},
			ExpectedKeywords:  KeywordSpec{AnyOf: []string{"connection", "refused"}},
			ExpectedDiagnosis: DiagnosisSpec{Category: "connection_refused"},
		},
	}

	// Response contains keyword "refused", diagnosis words "connection" and "refused",
	// and tool evidence for check_connection (contains "connect").
	response := "The connection was refused. Cannot connect to the server."

	result := Evaluate(f, response)

	if !result.Passed {
		t.Errorf("Evaluate should pass, got Passed=%v, Score=%.2f", result.Passed, result.Score)
	}
	if !result.KeywordPass {
		t.Error("KeywordPass should be true")
	}
	if !result.DiagnosisPass {
		t.Error("DiagnosisPass should be true")
	}
	if !result.ToolEvidence {
		t.Error("ToolEvidence should be true")
	}
	if result.Score < 0.6 {
		t.Errorf("Score = %.2f, want >= 0.6", result.Score)
	}
}

func TestEvaluate_KeywordFail(t *testing.T) {
	f := Failure{
		ID:       "test-2",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"max_connections", "too many"}},
		},
	}

	// Response missing all expected keywords.
	response := "The database is healthy and running normally."

	result := Evaluate(f, response)

	if result.KeywordPass {
		t.Error("KeywordPass should be false")
	}
	if result.Passed {
		t.Error("Evaluate should fail when keywords don't match")
	}
}

func TestEvaluate_NoKeywords(t *testing.T) {
	f := Failure{
		ID:       "test-3",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords: KeywordSpec{AnyOf: []string{}}, // Empty
		},
	}

	response := "Any response text"

	result := Evaluate(f, response)

	if !result.KeywordPass {
		t.Error("KeywordPass should be true by default when no keywords specified")
	}
}

func TestEvaluate_DiagnosisPartial(t *testing.T) {
	f := Failure{
		ID:       "test-4",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedDiagnosis: DiagnosisSpec{Category: "connection_exhaustion"},
		},
	}

	// Response has "connection" but not "exhaustion" - 50% match.
	response := "The connection pool is having issues."

	result := Evaluate(f, response)

	// 1/2 words = 0.5 ratio, which is >= 0.5 threshold.
	if !result.DiagnosisPass {
		t.Error("DiagnosisPass should be true for 50% match")
	}
}

func TestEvaluate_DiagnosisFail(t *testing.T) {
	f := Failure{
		ID:       "test-5",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedDiagnosis: DiagnosisSpec{Category: "table_bloat_vacuum"},
		},
	}

	// Response has 0/3 words.
	response := "The connection is working fine."

	result := Evaluate(f, response)

	if result.DiagnosisPass {
		t.Error("DiagnosisPass should be false for 0% match")
	}
}

func TestEvaluate_ToolEvidence(t *testing.T) {
	f := Failure{
		ID:       "test-6",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools: []string{"get_pods", "get_events"},
		},
	}

	// Response contains patterns from get_pods ("Running") and get_events ("Warning").
	response := "Pod is in Running state. Warning: high memory usage detected."

	result := Evaluate(f, response)

	if !result.ToolEvidence {
		t.Error("ToolEvidence should be true when tool patterns are found")
	}
}

func TestEvaluate_ToolEvidencePartial(t *testing.T) {
	f := Failure{
		ID:       "test-7",
		Name:     "Test failure",
		Category: "kubernetes",
		Evaluation: EvalSpec{
			ExpectedTools: []string{"get_pods", "get_events", "describe_pod", "get_service"},
		},
	}

	// Response has patterns for only 1/4 tools (get_pods: "Running").
	response := "The pod is Running."

	result := Evaluate(f, response)

	// 1/4 = 0.25 < 0.5 threshold.
	if result.ToolEvidence {
		t.Error("ToolEvidence should be false for < 50% tool coverage")
	}
}

func TestEvaluate_NoTools(t *testing.T) {
	f := Failure{
		ID:       "test-8",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools: []string{}, // Empty
		},
	}

	response := "Any response"

	result := Evaluate(f, response)

	if !result.ToolEvidence {
		t.Error("ToolEvidence should be true by default when no tools specified")
	}
}

func TestEvaluate_CaseInsensitive(t *testing.T) {
	f := Failure{
		ID:       "test-9",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"CONNECTION REFUSED"}},
		},
	}

	// Lowercase in response, uppercase in expected.
	response := "connection refused by server"

	result := Evaluate(f, response)

	if !result.KeywordPass {
		t.Error("Keyword matching should be case-insensitive")
	}
}

func TestEvaluate_ScoreWeighting(t *testing.T) {
	// Perfect score: keyword=1.0, diagnosis=1.0, tool=1.0
	// Score = 1.0*0.5 + 1.0*0.3 + 1.0*0.2 = 1.0

	f := Failure{
		ID:       "test-10",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords:  KeywordSpec{AnyOf: []string{"test"}},
			ExpectedDiagnosis: DiagnosisSpec{Category: "test"},
			ExpectedTools:     []string{"check_connection"},
		},
	}

	response := "test connection check"

	result := Evaluate(f, response)

	if result.Score != 1.0 {
		t.Errorf("Perfect score = %.2f, want 1.0", result.Score)
	}
}

func TestEvaluate_PassThreshold(t *testing.T) {
	// Test that score < 0.6 doesn't pass.

	f := Failure{
		ID:       "test-11",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords:  KeywordSpec{AnyOf: []string{"match"}},
			ExpectedDiagnosis: DiagnosisSpec{Category: "xyz_abc"},
			ExpectedTools:     []string{"unknown_tool"},
		},
	}

	// Keyword: 1.0 (matches "match")
	// Diagnosis: 0.0 (no match for "xyz" or "abc")
	// Tools: 0.0 (unknown_tool not in toolPatterns)
	// Score = 1.0*0.5 + 0.0*0.3 + 0.0*0.2 = 0.5
	response := "match only text"

	result := Evaluate(f, response)

	if result.Score != 0.5 {
		t.Errorf("Score = %.2f, want 0.5", result.Score)
	}
	if result.Passed {
		t.Error("Score 0.5 should not pass (< 0.6)")
	}
}

func TestEvaluate_ResultFields(t *testing.T) {
	f := Failure{
		ID:       "db-test-id",
		Name:     "Test Failure Name",
		Category: "database",
	}

	result := Evaluate(f, "response")

	if result.FailureID != "db-test-id" {
		t.Errorf("FailureID = %q, want %q", result.FailureID, "db-test-id")
	}
	if result.FailureName != "Test Failure Name" {
		t.Errorf("FailureName = %q, want %q", result.FailureName, "Test Failure Name")
	}
	if result.Category != "database" {
		t.Errorf("Category = %q, want %q", result.Category, "database")
	}
}
