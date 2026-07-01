package faultlib

import (
	"context"
	"errors"
	"testing"
)

func makePlaybookInput() PlaybookDiffInput {
	return PlaybookDiffInput{
		BeforeID:          "pb_before",
		AfterID:           "pb_after",
		BeforeName:        "Max Connections Diagnosis",
		BeforeDescription: "Diagnoses connection exhaustion faults",
		BeforeGuidance:    "Check pg_stat_activity for idle connections.",
		BeforeSymptoms:    []string{"connection refused", "FATAL: remaining connection slots are reserved"},
		BeforeEscalation:  []string{"idle connections older than 10 minutes"},
		AfterName:         "Max Connections Diagnosis",
		AfterDescription:  "Diagnoses connection exhaustion faults",
		AfterGuidance:     "Check pg_stat_activity; use kill_idle_connections when count exceeds max_connections*0.9.",
		AfterSymptoms:     []string{"connection refused", "FATAL: remaining connection slots are reserved"},
		AfterEscalation:   []string{"idle connections older than 10 minutes", "shared_buffers exhausted"},
	}
}

var approveResponse = `{"verdict":"APPROVE","guidance_quality":"More specific threshold check added.","escalation_safety":"Tighter: added shared_buffers criterion.","reasoning":"The update adds a concrete action threshold, making the guidance more actionable."}`

func TestJudgePlaybookDiff_NilCompleter(t *testing.T) {
	result := JudgePlaybookDiff(context.Background(), makePlaybookInput(), "1.3", "1.4", nil, "")
	if !result.Skipped {
		t.Error("expected Skipped=true when completer is nil")
	}
}

func TestJudgePlaybookDiff_Approve(t *testing.T) {
	result := JudgePlaybookDiff(context.Background(), makePlaybookInput(), "1.3", "1.4",
		mockCompleter(approveResponse, nil), "test-model")
	if result.Skipped {
		t.Fatalf("unexpected Skipped=true: %s", result.Reasoning)
	}
	if result.Verdict != "APPROVE" {
		t.Errorf("Verdict = %q, want APPROVE", result.Verdict)
	}
	if result.GuidanceQuality == "" {
		t.Error("GuidanceQuality should not be empty")
	}
	if result.EscalationSafety == "" {
		t.Error("EscalationSafety should not be empty")
	}
	if result.Model != "test-model" {
		t.Errorf("Model = %q, want test-model", result.Model)
	}
}

func TestJudgePlaybookDiff_NeedsReview(t *testing.T) {
	raw := `{"verdict":"NEEDS_REVIEW","guidance_quality":"Neutral: different wording, same steps.","escalation_safety":"Unchanged.","reasoning":"Change is cosmetic; unclear improvement."}`
	result := JudgePlaybookDiff(context.Background(), makePlaybookInput(), "1.3", "1.4",
		mockCompleter(raw, nil), "m")
	if result.Verdict != "NEEDS_REVIEW" {
		t.Errorf("Verdict = %q, want NEEDS_REVIEW", result.Verdict)
	}
}

func TestJudgePlaybookDiff_Reject(t *testing.T) {
	raw := `{"verdict":"REJECT","guidance_quality":"Worse: removes specific threshold checks.","escalation_safety":"Looser: removed escalation condition.","reasoning":"The new version removes actionable detail."}`
	result := JudgePlaybookDiff(context.Background(), makePlaybookInput(), "1.3", "1.4",
		mockCompleter(raw, nil), "m")
	if result.Verdict != "REJECT" {
		t.Errorf("Verdict = %q, want REJECT", result.Verdict)
	}
}

func TestJudgePlaybookDiff_VerdictCaseNormalized(t *testing.T) {
	raw := `{"verdict":"approve","guidance_quality":"ok","escalation_safety":"ok","reasoning":"fine"}`
	result := JudgePlaybookDiff(context.Background(), makePlaybookInput(), "1.3", "1.4",
		mockCompleter(raw, nil), "m")
	if result.Verdict != "APPROVE" {
		t.Errorf("Verdict = %q, want APPROVE (lowercase normalised)", result.Verdict)
	}
}

func TestJudgePlaybookDiff_UnknownVerdictDefaultsToNeedsReview(t *testing.T) {
	raw := `{"verdict":"MAYBE","guidance_quality":"ok","escalation_safety":"ok","reasoning":"unsure"}`
	result := JudgePlaybookDiff(context.Background(), makePlaybookInput(), "1.3", "1.4",
		mockCompleter(raw, nil), "m")
	if result.Verdict != "NEEDS_REVIEW" {
		t.Errorf("Verdict = %q, want NEEDS_REVIEW for unknown verdict", result.Verdict)
	}
}

func TestJudgePlaybookDiff_CompleterError(t *testing.T) {
	result := JudgePlaybookDiff(context.Background(), makePlaybookInput(), "1.3", "1.4",
		mockCompleter("", errors.New("LLM down")), "m")
	if !result.Skipped {
		t.Error("expected Skipped=true when completer returns error")
	}
	if result.Reasoning == "" {
		t.Error("Reasoning should describe the error")
	}
}

func TestJudgePlaybookDiff_InvalidJSON(t *testing.T) {
	result := JudgePlaybookDiff(context.Background(), makePlaybookInput(), "1.3", "1.4",
		mockCompleter("not json", nil), "m")
	if !result.Skipped {
		t.Error("expected Skipped=true when JSON is unparseable")
	}
}

func TestJudgePlaybookDiff_OperationalDriftPropagated(t *testing.T) {
	input := makePlaybookInput()
	input.OperationalDrift = []string{"execution_mode: fleet → agent_approve"}
	result := JudgePlaybookDiff(context.Background(), input, "1.3", "1.4",
		mockCompleter(approveResponse, nil), "m")
	if len(result.OperationalDrift) != 1 || result.OperationalDrift[0] != "execution_mode: fleet → agent_approve" {
		t.Errorf("OperationalDrift not propagated: %v", result.OperationalDrift)
	}
}

func TestJudgePlaybookDiff_OperationalDriftPropagatedOnSkip(t *testing.T) {
	input := makePlaybookInput()
	input.OperationalDrift = []string{"approval_mode: auto → manual"}
	result := JudgePlaybookDiff(context.Background(), input, "1.3", "1.4", nil, "")
	if len(result.OperationalDrift) != 1 {
		t.Errorf("OperationalDrift should be propagated even on skip: %v", result.OperationalDrift)
	}
}

func TestJudgePlaybookDiff_MarkdownFencedJSON(t *testing.T) {
	raw := "```json\n" + approveResponse + "\n```"
	result := JudgePlaybookDiff(context.Background(), makePlaybookInput(), "1.3", "1.4",
		mockCompleter(raw, nil), "m")
	if result.Skipped {
		t.Fatal("should parse fenced JSON without skipping")
	}
	if result.Verdict != "APPROVE" {
		t.Errorf("Verdict = %q, want APPROVE", result.Verdict)
	}
}

// ── bulletList helper ─────────────────────────────────────────────────────

func TestBulletList_Empty(t *testing.T) {
	if got := bulletList(nil); got != "(none)" {
		t.Errorf("bulletList(nil) = %q, want %q", got, "(none)")
	}
}

func TestBulletList_Items(t *testing.T) {
	got := bulletList([]string{"alpha", "beta"})
	if got != "• alpha\n• beta" {
		t.Errorf("bulletList = %q", got)
	}
}
