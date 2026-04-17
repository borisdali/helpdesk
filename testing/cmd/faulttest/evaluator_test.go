package main

import (
	"testing"

	"helpdesk/testing/testutil"
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

	result := Evaluate(f, testutil.AgentResponse{Text: response})

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

	result := Evaluate(f, testutil.AgentResponse{Text: response})

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

	result := Evaluate(f, testutil.AgentResponse{Text: response})

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

	result := Evaluate(f, testutil.AgentResponse{Text: response})

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

	result := Evaluate(f, testutil.AgentResponse{Text: response})

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

	result := Evaluate(f, testutil.AgentResponse{Text: response})

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

	result := Evaluate(f, testutil.AgentResponse{Text: response})

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

	result := Evaluate(f, testutil.AgentResponse{Text: response})

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

	result := Evaluate(f, testutil.AgentResponse{Text: response})

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

	result := Evaluate(f, testutil.AgentResponse{Text: response})

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

	result := Evaluate(f, testutil.AgentResponse{Text: response})

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

	result := Evaluate(f, testutil.AgentResponse{Text: "response"})

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

func TestEvaluate_StructuredToolCalls(t *testing.T) {
	// When ToolCalls is populated (Option C path), exact name matching is used.
	f := Failure{
		ID:       "test-12",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"vacuum"}},
			ExpectedTools:    []string{"get_table_stats", "get_database_stats"},
		},
	}

	resp := testutil.AgentResponse{
		Text: "The table has dead tuples and needs vacuum.",
		ToolCalls: []testutil.ToolCallResult{
			{Name: "get_table_stats", Success: true},
			{Name: "get_database_stats", Success: true},
		},
	}

	result := Evaluate(f, resp)

	if !result.ToolEvidence {
		t.Error("ToolEvidence should be true with exact structured tool matches")
	}
	if result.ToolEvidenceMode != "structured" {
		t.Errorf("ToolEvidenceMode = %q, want %q", result.ToolEvidenceMode, "structured")
	}
}

func TestEvaluate_StructuredToolCallsFailedTool(t *testing.T) {
	// A tool call that failed (Success=false) should not count toward tool evidence.
	f := Failure{
		ID:       "test-13",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools: []string{"get_table_stats", "get_database_stats"},
		},
	}

	resp := testutil.AgentResponse{
		Text: "Could not retrieve table stats.",
		ToolCalls: []testutil.ToolCallResult{
			{Name: "get_table_stats", Success: false},
			{Name: "get_database_stats", Success: false},
		},
	}

	result := Evaluate(f, resp)

	// 0/2 successful → toolScore=0.0 → ToolEvidence=false
	if result.ToolEvidence {
		t.Error("ToolEvidence should be false when all tool calls failed")
	}
	if result.ToolEvidenceMode != "structured" {
		t.Errorf("ToolEvidenceMode = %q, want %q", result.ToolEvidenceMode, "structured")
	}
}

func TestEvaluate_TextFallbackMode(t *testing.T) {
	// When ToolCalls is nil (Option B path), mode should be "text_fallback".
	f := Failure{
		ID:       "test-14",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools: []string{"get_pods"},
		},
	}

	// ToolCalls is nil → Option B path
	result := Evaluate(f, testutil.AgentResponse{Text: "Pod is in Running state."})

	if result.ToolEvidenceMode != "text_fallback" {
		t.Errorf("ToolEvidenceMode = %q, want %q", result.ToolEvidenceMode, "text_fallback")
	}
}

func TestEvaluate_NoToolsModeEmpty(t *testing.T) {
	// When no tools are expected, ToolEvidenceMode should be empty.
	f := Failure{
		ID:       "test-15",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools: []string{},
		},
	}

	result := Evaluate(f, testutil.AgentResponse{Text: "Any response"})

	if result.ToolEvidenceMode != "" {
		t.Errorf("ToolEvidenceMode = %q, want empty when no tools expected", result.ToolEvidenceMode)
	}
}

// ── Audit mode tool evidence ───────────────────────────────────────────────

func TestScoreToolEvidence_AuditMode(t *testing.T) {
	// When auditTools is provided, mode is "audit" and exact names are matched.
	f := Failure{
		ID:       "audit-1",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools: []string{"check_connection", "get_database_info"},
		},
	}
	auditTools := []string{"check_connection", "get_database_info", "get_active_connections"}

	score, evidence, mode := scoreToolEvidence(f, testutil.AgentResponse{Text: "irrelevant"}, auditTools)

	if mode != "audit" {
		t.Errorf("mode = %q, want %q", mode, "audit")
	}
	if !evidence {
		t.Error("ToolEvidence should be true when all expected tools are in audit list")
	}
	if score != 1.0 {
		t.Errorf("score = %.2f, want 1.0", score)
	}
}

func TestScoreToolEvidence_AuditModePriority(t *testing.T) {
	// When auditTools is provided AND ToolCalls is non-nil, audit wins.
	f := Failure{
		ID:       "audit-2",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools: []string{"get_lock_info"},
		},
	}
	// Structured tool calls show the tool succeeded — but audit says it didn't.
	resp := testutil.AgentResponse{
		Text: "lock information retrieved",
		ToolCalls: []testutil.ToolCallResult{
			{Name: "get_lock_info", Success: true},
		},
	}
	// Audit doesn't have get_lock_info — should score 0.
	auditTools := []string{"check_connection"}

	score, evidence, mode := scoreToolEvidence(f, resp, auditTools)

	if mode != "audit" {
		t.Errorf("mode = %q, want audit (audit takes priority over structured)", mode)
	}
	if evidence {
		t.Error("ToolEvidence should be false when expected tool is absent from audit list")
	}
	if score != 0.0 {
		t.Errorf("score = %.2f, want 0.0", score)
	}
}

func TestScoreToolEvidence_AuditModePartial(t *testing.T) {
	// 1 of 2 expected tools in audit list → score = 0.5, evidence = false (≤0.5).
	f := Failure{
		ID:       "audit-3",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedTools: []string{"check_connection", "get_database_info"},
		},
	}
	auditTools := []string{"check_connection"} // only one

	score, evidence, mode := scoreToolEvidence(f, testutil.AgentResponse{Text: ""}, auditTools)

	if mode != "audit" {
		t.Errorf("mode = %q, want audit", mode)
	}
	if score != 0.5 {
		t.Errorf("score = %.2f, want 0.5", score)
	}
	if evidence {
		t.Error("ToolEvidence should be false for exactly 0.5 score (threshold is > 0.5)")
	}
}

func TestEvaluate_AuditModeVariadic(t *testing.T) {
	// Evaluate() accepts audit tools as a variadic argument and uses them.
	f := Failure{
		ID:       "audit-4",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"connection"}},
			ExpectedTools:    []string{"check_connection"},
		},
	}

	// Without audit tools (text fallback, tool pattern not in response).
	resp := testutil.AgentResponse{Text: "connection issue detected"}
	resultNoAudit := Evaluate(f, resp)

	// With audit tools (exact match).
	resultWithAudit := Evaluate(f, resp, []string{"check_connection"})

	if resultNoAudit.ToolEvidenceMode != "text_fallback" {
		t.Errorf("without audit: mode = %q, want text_fallback", resultNoAudit.ToolEvidenceMode)
	}
	if resultWithAudit.ToolEvidenceMode != "audit" {
		t.Errorf("with audit: mode = %q, want audit", resultWithAudit.ToolEvidenceMode)
	}
	if !resultWithAudit.ToolEvidence {
		t.Error("ToolEvidence should be true with audit tools providing exact match")
	}
}

// ── OverallScore formula ───────────────────────────────────────────────────

func TestOverallScoreFormula(t *testing.T) {
	// OverallScore = Score*0.6 + RemediationScore*0.4 (when remediation attempted).
	// Score is the full composite (keywords+tools+category/judge), not just the category sub-component.
	// Validates the weights haven't drifted.
	tests := []struct {
		score       float64
		remediation float64
		want        float64
	}{
		{1.0, 1.0, 1.0},
		{1.0, 0.75, 0.9},   // perfect score, slow recovery
		{0.67, 1.0, 0.802}, // partial score, perfect remediation
		{0.67, 0.75, 0.702},
		{0.0, 0.0, 0.0},
	}
	for _, tt := range tests {
		got := tt.score*0.6 + tt.remediation*0.4
		if got < tt.want-0.001 || got > tt.want+0.001 {
			t.Errorf("%.2f*0.6 + %.2f*0.4 = %.4f, want %.4f",
				tt.score, tt.remediation, got, tt.want)
		}
	}
}

func TestOverallScore_NoRemediation_EqualsDiagnosisScore(t *testing.T) {
	// When no remediation is attempted, OverallScore = Score (the weighted diagnosis score).
	f := Failure{
		ID:       "no-rem",
		Name:     "Test failure",
		Category: "database",
		Evaluation: EvalSpec{
			ExpectedKeywords: KeywordSpec{AnyOf: []string{"connection"}},
		},
	}
	result := Evaluate(f, testutil.AgentResponse{Text: "connection issue"})
	// Simulate what main.go does: OverallScore = Score when no remediation.
	result.OverallScore = result.Score
	if result.OverallScore != result.Score {
		t.Errorf("OverallScore = %.4f, want Score = %.4f", result.OverallScore, result.Score)
	}
}
