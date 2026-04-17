package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout to a buffer for the duration of fn.
func captureStdout(fn func()) string {
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// ── PrintSummary — basic output ───────────────────────────────────────────

func TestPrintSummary_PassFail(t *testing.T) {
	report := BuildReport("run-1", []EvalResult{
		{FailureID: "fault-a", FailureName: "Fault A", Category: "database", Score: 0.9, Passed: true},
		{FailureID: "fault-b", FailureName: "Fault B", Category: "database", Score: 0.3, Passed: false, KeywordPass: false},
	})

	out := captureStdout(func() { report.PrintSummary() })

	if !strings.Contains(out, "[PASS] Fault A") {
		t.Errorf("missing PASS line; got:\n%s", out)
	}
	if !strings.Contains(out, "[FAIL] Fault B") {
		t.Errorf("missing FAIL line; got:\n%s", out)
	}
	if !strings.Contains(out, "Total: 2 | Passed: 1 | Failed: 1") {
		t.Errorf("missing summary counts; got:\n%s", out)
	}
}

func TestPrintSummary_FailDetails(t *testing.T) {
	report := BuildReport("run-1", []EvalResult{
		{
			FailureID:   "fault-x",
			FailureName: "Fault X",
			Category:    "database",
			Score:       0.2,
			Passed:      false,
			KeywordPass: false,
			ToolEvidence: false,
			Error:       "agent timeout",
		},
	})

	out := captureStdout(func() { report.PrintSummary() })

	// Component scores are omitted when an error prevented evaluation.
	// Only the error line should appear.
	if !strings.Contains(out, "Error: agent timeout") {
		t.Errorf("expected error detail in output; got:\n%s", out)
	}
	if strings.Contains(out, "Keywords:") {
		t.Errorf("expected no component scores for error result; got:\n%s", out)
	}
}

// ── PrintSummary — judge fields ───────────────────────────────────────────

func TestPrintSummary_JudgeIndicator(t *testing.T) {
	report := BuildReport("run-j", []EvalResult{
		{
			FailureID:      "fault-j",
			FailureName:    "Lock contention",
			Category:       "database",
			Score:          0.87,
			Passed:         true,
			DiagnosisScore: 0.67,
			JudgeModel:     "claude-haiku",
			JudgeSkipped:   false,
			JudgeReasoning: "Root cause correctly identified",
		},
	})

	out := captureStdout(func() { report.PrintSummary() })

	// Should show [judge: 67%] inline.
	if !strings.Contains(out, "[judge: 67%]") {
		t.Errorf("expected '[judge: 67%%]' in output; got:\n%s", out)
	}

	// Should show judge reasoning on its own line.
	if !strings.Contains(out, `"Root cause correctly identified"`) {
		t.Errorf("expected judge reasoning in output; got:\n%s", out)
	}

	// Should show LLM judge footer.
	if !strings.Contains(out, "LLM judge scored diagnosis for 1 fault(s)") {
		t.Errorf("expected judge footer; got:\n%s", out)
	}
	if !strings.Contains(out, "tool*0.40 + judge*0.40 + keyword*0.20") {
		t.Errorf("expected weight description in footer; got:\n%s", out)
	}
}

func TestPrintSummary_JudgeSkipped_Indicator(t *testing.T) {
	// When JudgeSkipped=true with a reason, [judge skipped: reason] appears and
	// [judge: N%] does not (the score in the header is still the composite score).
	report := BuildReport("run-js", []EvalResult{
		{
			FailureID:      "fault-s",
			FailureName:    "Connection refused",
			Category:       "database",
			Score:          1.0,
			Passed:         true,
			DiagnosisScore: 1.0,
			JudgeSkipped:   true,
			JudgeReasoning: "api key not set",
		},
	})

	out := captureStdout(func() { report.PrintSummary() })

	if strings.Contains(out, "[judge:") {
		t.Errorf("judge header indicator should not appear when JudgeSkipped; got:\n%s", out)
	}
	if !strings.Contains(out, "[judge skipped: api key not set]") {
		t.Errorf("expected '[judge skipped: api key not set]' in output; got:\n%s", out)
	}
	if strings.Contains(out, "LLM judge scored") {
		t.Errorf("judge footer should not appear when no judge ran; got:\n%s", out)
	}
}

func TestPrintSummary_JudgeScore_100Percent(t *testing.T) {
	// Judge score=3 → DiagnosisScore=1.0 → [judge: 100%]
	report := BuildReport("run-100", []EvalResult{
		{
			FailureID:      "f",
			FailureName:    "Max connections",
			Category:       "database",
			Score:          1.0,
			Passed:         true,
			DiagnosisScore: 1.0,
			JudgeModel:     "m",
			JudgeSkipped:   false,
		},
	})

	out := captureStdout(func() { report.PrintSummary() })

	if !strings.Contains(out, "[judge: 100%]") {
		t.Errorf("expected '[judge: 100%%]'; got:\n%s", out)
	}
}

// ── PrintSummary — text_fallback note ─────────────────────────────────────

func TestPrintSummary_TextFallbackNote(t *testing.T) {
	report := BuildReport("run-tf", []EvalResult{
		{
			FailureID:        "fault-tf",
			FailureName:      "Table bloat",
			Category:         "database",
			Score:            0.8,
			Passed:           true,
			ToolEvidenceMode: "text_fallback",
		},
	})

	out := captureStdout(func() { report.PrintSummary() })

	if !strings.Contains(out, "[tool evidence: text match]") {
		t.Errorf("expected text-match marker; got:\n%s", out)
	}
	if !strings.Contains(out, "text-based tool evidence scoring") {
		t.Errorf("expected text-fallback footer note; got:\n%s", out)
	}
}

func TestPrintSummary_AuditMode_NoFallbackNote(t *testing.T) {
	// audit mode should not trigger the text-fallback warning.
	report := BuildReport("run-a", []EvalResult{
		{
			FailureID:        "fault-a",
			FailureName:      "Replication lag",
			Category:         "database",
			Score:            1.0,
			Passed:           true,
			ToolEvidenceMode: "audit",
		},
	})

	out := captureStdout(func() { report.PrintSummary() })

	if strings.Contains(out, "[tool evidence: text match]") {
		t.Errorf("audit mode should not show text-match marker; got:\n%s", out)
	}
	if strings.Contains(out, "text-based tool evidence scoring") {
		t.Errorf("audit mode should not show fallback footer; got:\n%s", out)
	}
}

// ── PrintSummary — dual-score remediation display ─────────────────────────

func TestPrintSummary_RemediationDualScore_Passed(t *testing.T) {
	report := BuildReport("run-r", []EvalResult{
		{
			FailureID:            "fault-r",
			FailureName:          "Max connections",
			Category:             "database",
			Score:                0.92,
			Passed:               true,
			DiagnosisScore:       0.92,
			RemediationAttempted: true,
			RemediationPassed:    true,
			RemediationScore:     1.0,
			RecoveryTimeSecs:     4.2,
			RemediationMethod:    "playbook",
			OverallScore:         0.95,
		},
	})

	out := captureStdout(func() { report.PrintSummary() })

	if !strings.Contains(out, "Category:") {
		t.Errorf("expected Category label in component score line; got:\n%s", out)
	}
	if !strings.Contains(out, "Remediation:") {
		t.Errorf("expected Remediation label in dual-score line; got:\n%s", out)
	}
	if !strings.Contains(out, "Overall:") {
		t.Errorf("expected Overall label in dual-score line; got:\n%s", out)
	}
	if !strings.Contains(out, "playbook") {
		t.Errorf("expected remediation method 'playbook'; got:\n%s", out)
	}
	if !strings.Contains(out, "4.2s") {
		t.Errorf("expected recovery time '4.2s'; got:\n%s", out)
	}
}

func TestPrintSummary_RemediationDualScore_Failed(t *testing.T) {
	report := BuildReport("run-rf", []EvalResult{
		{
			FailureID:            "fault-rf",
			FailureName:          "Lock contention",
			Category:             "database",
			Score:                0.7,
			Passed:               false,
			DiagnosisScore:       0.7,
			RemediationAttempted: true,
			RemediationPassed:    false,
			RemediationScore:     0.0,
			RemediationMethod:    "playbook",
			OverallScore:         0.42,
		},
	})

	out := captureStdout(func() { report.PrintSummary() })

	if !strings.Contains(out, "Remediation: FAILED") {
		t.Errorf("expected 'Remediation: FAILED'; got:\n%s", out)
	}
}

func TestPrintSummary_NoRemediation_NoDualScore(t *testing.T) {
	// When RemediationAttempted=false, the dual-score line should not appear.
	report := BuildReport("run-nr", []EvalResult{
		{
			FailureID:   "fault-nr",
			FailureName: "Connection refused",
			Category:    "database",
			Score:       1.0,
			Passed:      true,
		},
	})

	out := captureStdout(func() { report.PrintSummary() })

	if strings.Contains(out, "Overall:") {
		t.Errorf("dual-score line should not appear when remediation not attempted; got:\n%s", out)
	}
}

// ── BuildReport ────────────────────────────────────────────────────────────

func TestBuildReport_SummaryCountsCorrect(t *testing.T) {
	results := []EvalResult{
		{Category: "database", Passed: true},
		{Category: "database", Passed: true},
		{Category: "database", Passed: false},
		{Category: "host", Passed: true},
	}
	report := BuildReport("r1", results)

	if report.Summary.Total != 4 {
		t.Errorf("Total = %d, want 4", report.Summary.Total)
	}
	if report.Summary.Passed != 3 {
		t.Errorf("Passed = %d, want 3", report.Summary.Passed)
	}
	if report.Summary.Failed != 1 {
		t.Errorf("Failed = %d, want 1", report.Summary.Failed)
	}
	if fmt.Sprintf("%.2f", report.Summary.PassRate) != "0.75" {
		t.Errorf("PassRate = %.2f, want 0.75", report.Summary.PassRate)
	}

	dbStat := report.Summary.Categories["database"]
	if dbStat.Total != 3 || dbStat.Passed != 2 {
		t.Errorf("database: Total=%d Passed=%d, want 3/2", dbStat.Total, dbStat.Passed)
	}
}

// ── EvalResult JSON marshaling ─────────────────────────────────────────────

func TestEvalResult_JSONRoundTrip_JudgeFields(t *testing.T) {
	original := EvalResult{
		FailureID:      "fault-j",
		FailureName:    "Max connections",
		Category:       "database",
		Score:          0.87,
		Passed:         true,
		DiagnosisScore: 0.67,
		JudgeReasoning: "root cause identified correctly",
		JudgeModel:     "claude-haiku",
		JudgeSkipped:   false,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded EvalResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.DiagnosisScore != original.DiagnosisScore {
		t.Errorf("DiagnosisScore: got %.2f, want %.2f", decoded.DiagnosisScore, original.DiagnosisScore)
	}
	if decoded.JudgeReasoning != original.JudgeReasoning {
		t.Errorf("JudgeReasoning: got %q, want %q", decoded.JudgeReasoning, original.JudgeReasoning)
	}
	if decoded.JudgeModel != original.JudgeModel {
		t.Errorf("JudgeModel: got %q, want %q", decoded.JudgeModel, original.JudgeModel)
	}
	if decoded.JudgeSkipped != original.JudgeSkipped {
		t.Errorf("JudgeSkipped: got %v, want %v", decoded.JudgeSkipped, original.JudgeSkipped)
	}
}

func TestEvalResult_JSONRoundTrip_RemediationFields(t *testing.T) {
	original := EvalResult{
		FailureID:            "fault-r",
		RemediationAttempted: true,
		RemediationPassed:    true,
		RemediationScore:     0.75,
		RemediationMethod:    "playbook",
		OverallScore:         0.85,
		RecoveryTimeSecs:     8.3,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded EvalResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.RemediationScore != original.RemediationScore {
		t.Errorf("RemediationScore: got %.2f, want %.2f", decoded.RemediationScore, original.RemediationScore)
	}
	if decoded.RemediationMethod != original.RemediationMethod {
		t.Errorf("RemediationMethod: got %q, want %q", decoded.RemediationMethod, original.RemediationMethod)
	}
	if decoded.OverallScore != original.OverallScore {
		t.Errorf("OverallScore: got %.2f, want %.2f", decoded.OverallScore, original.OverallScore)
	}
}

func TestEvalResult_JSON_OmitsJudgeSkippedWhenFalse(t *testing.T) {
	// JudgeSkipped=false should be omitted from JSON (omitempty).
	r := EvalResult{JudgeSkipped: false, JudgeModel: ""}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "judge_skipped") {
		t.Errorf("judge_skipped should be omitted when false; JSON: %s", data)
	}
}

func TestEvalResult_JSON_DiagnosisScoreAlwaysPresent(t *testing.T) {
	// DiagnosisScore has no omitempty — should appear even when 0.
	r := EvalResult{DiagnosisScore: 0}
	data, _ := json.Marshal(r)
	if !strings.Contains(string(data), "diagnosis_score") {
		t.Errorf("diagnosis_score should always be present in JSON; got: %s", data)
	}
}
