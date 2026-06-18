package faultlib

import (
	"context"
	"errors"
	"testing"
)

func steps(tools ...string) []RemediationStep {
	s := make([]RemediationStep, len(tools))
	for i, t := range tools {
		s[i] = RemediationStep{Tool: t, Status: "succeeded"}
	}
	return s
}

func basicInput(passed bool, ss []RemediationStep) RemediationJudgeInput {
	return RemediationJudgeInput{
		FaultName:        "connection-exhaustion",
		FaultDescription: "Too many connections saturate pg_max_connections.",
		Steps:            ss,
		RecoveryTimeSecs: 12.5,
		Passed:           passed,
	}
}

func TestJudgeRemediation_NilCompleter(t *testing.T) {
	result := JudgeRemediation(context.Background(), basicInput(true, steps("kill_idle_connections")), nil, "")
	if !result.Skipped {
		t.Error("expected Skipped=true when completer is nil")
	}
}

func TestJudgeRemediation_NoSteps(t *testing.T) {
	result := JudgeRemediation(context.Background(), basicInput(true, nil), mockCompleter(`{"score":3,"reasoning":"ok"}`, nil), "m")
	if !result.Skipped {
		t.Error("expected Skipped=true when no steps provided")
	}
}

func TestJudgeRemediation_Score3(t *testing.T) {
	result := JudgeRemediation(context.Background(),
		basicInput(true, steps("kill_idle_connections")),
		mockCompleter(`{"score":3,"reasoning":"optimal approach"}`, nil), "test-model")
	if result.Skipped {
		t.Fatal("should not be skipped")
	}
	if result.Score != 1.0 {
		t.Errorf("Score = %.2f, want 1.0 for score=3", result.Score)
	}
	if result.Reasoning != "optimal approach" {
		t.Errorf("Reasoning = %q", result.Reasoning)
	}
	if result.Model != "test-model" {
		t.Errorf("Model = %q, want %q", result.Model, "test-model")
	}
}

func TestJudgeRemediation_Score2(t *testing.T) {
	result := JudgeRemediation(context.Background(),
		basicInput(true, steps("get_active_connections", "kill_idle_connections")),
		mockCompleter(`{"score":2,"reasoning":"extra read step"}`, nil), "m")
	if result.Score != 0.67 {
		t.Errorf("Score = %.2f, want 0.67 for score=2", result.Score)
	}
}

func TestJudgeRemediation_Score1(t *testing.T) {
	result := JudgeRemediation(context.Background(),
		basicInput(true, steps("terminate_connection", "terminate_connection", "terminate_connection")),
		mockCompleter(`{"score":1,"reasoning":"excessive individual kills"}`, nil), "m")
	if result.Score != 0.33 {
		t.Errorf("Score = %.2f, want 0.33 for score=1", result.Score)
	}
}

func TestJudgeRemediation_Score0(t *testing.T) {
	result := JudgeRemediation(context.Background(),
		basicInput(false, steps("restart_deployment")),
		mockCompleter(`{"score":0,"reasoning":"wrong tool for a DB fault"}`, nil), "m")
	if result.Score != 0.0 {
		t.Errorf("Score = %.2f, want 0.0 for score=0", result.Score)
	}
}

func TestJudgeRemediation_CompleterError(t *testing.T) {
	result := JudgeRemediation(context.Background(),
		basicInput(true, steps("kill_idle_connections")),
		mockCompleter("", errors.New("timeout")), "m")
	if !result.Skipped {
		t.Error("expected Skipped=true on completer error")
	}
	if result.Reasoning == "" {
		t.Error("Reasoning should explain the error")
	}
}

func TestJudgeRemediation_InvalidJSON(t *testing.T) {
	result := JudgeRemediation(context.Background(),
		basicInput(true, steps("kill_idle_connections")),
		mockCompleter("sorry I cannot help with that", nil), "m")
	if !result.Skipped {
		t.Error("expected Skipped=true on unparseable response")
	}
}

func TestJudgeRemediation_PlaybookGuidanceIncluded(t *testing.T) {
	var capturedPrompt string
	completer := func(_ context.Context, prompt string) (string, error) {
		capturedPrompt = prompt
		return `{"score":3,"reasoning":"ok"}`, nil
	}
	input := RemediationJudgeInput{
		FaultName:        "test-fault",
		FaultDescription: "desc",
		PlaybookGuidance: "Always use kill_idle_connections first.",
		Steps:            steps("kill_idle_connections"),
		Passed:           true,
	}
	JudgeRemediation(context.Background(), input, completer, "m") //nolint:errcheck
	if capturedPrompt == "" {
		t.Fatal("completer was not called")
	}
	if !containsStr(capturedPrompt, "Always use kill_idle_connections first.") {
		t.Error("prompt should include PlaybookGuidance")
	}
}

func TestJudgeRemediation_ArgsOmitConnPlumbing(t *testing.T) {
	var capturedPrompt string
	completer := func(_ context.Context, prompt string) (string, error) {
		capturedPrompt = prompt
		return `{"score":2,"reasoning":"ok"}`, nil
	}
	input := RemediationJudgeInput{
		FaultName:        "test-fault",
		FaultDescription: "desc",
		Steps: []RemediationStep{
			{
				Tool: "kill_idle_connections",
				Args: map[string]any{
					"connection_string": "postgres://user:pass@host/db",
					"idle_threshold":    "5m",
				},
				Status: "succeeded",
			},
		},
		Passed: true,
	}
	JudgeRemediation(context.Background(), input, completer, "m") //nolint:errcheck
	if containsStr(capturedPrompt, "connection_string") {
		t.Error("prompt should not include connection_string (plumbing key)")
	}
	if !containsStr(capturedPrompt, "idle_threshold") {
		t.Error("prompt should include idle_threshold (diagnostic arg)")
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && findStr(s, sub))
}

func findStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
