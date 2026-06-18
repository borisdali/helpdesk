package faultlib

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// RemediationStep summarises one executed step from the remediation playbook run.
type RemediationStep struct {
	Tool   string
	Args   map[string]any
	Status string // "succeeded" | "failed" | "denied"
	Result string
}

// RemediationJudgeInput holds the context the LLM judge needs to evaluate a
// remediation attempt.
type RemediationJudgeInput struct {
	FaultName        string
	FaultDescription string
	// PlaybookGuidance is the remediation agent prompt or playbook description.
	// Empty when the fault uses a structured playbook without a free-text prompt.
	PlaybookGuidance string
	Steps            []RemediationStep
	RecoveryTimeSecs float64
	Passed           bool
}

const remediationJudgePrompt = `You are evaluating an AI operations agent's remediation approach.

FAULT: %s — %s
%s
RECOVERY: %s in %.1fs

STEPS TAKEN (%d):
%s

Score the remediation approach on a scale of 0–3:
3 = Most targeted tool choice; correct sequence; no redundant or unnecessary steps
2 = Correct approach, but includes one unnecessary step or mild sequencing issue
1 = Eventually recovered, but with excessive blast radius or a significant number of redundant calls
0 = Wrong tool choice, approach would cause unnecessary side effects, or no meaningful steps recorded

Respond with JSON only, no other text: {"score": <0|1|2|3>, "reasoning": "<one concise sentence>"}`

// JudgeRemediation evaluates a remediation attempt using an LLM judge.
// Returns a JudgeResult (same type as the diagnosis Judge function).
// When completer is nil or no steps are recorded, returns a skipped result.
func JudgeRemediation(ctx context.Context, input RemediationJudgeInput, completer TextCompleter, model string) JudgeResult {
	if completer == nil || len(input.Steps) == 0 {
		return JudgeResult{Skipped: true}
	}

	guidanceSection := ""
	if input.PlaybookGuidance != "" {
		guidanceSection = fmt.Sprintf("PLAYBOOK GUIDANCE: %s\n", input.PlaybookGuidance)
	}

	recoveryStatus := "SUCCEEDED"
	if !input.Passed {
		recoveryStatus = "FAILED"
	}

	var stepLines []string
	for i, s := range input.Steps {
		argStr := formatArgs(s.Args)
		line := fmt.Sprintf("%d. %s(%s) → [%s]", i+1, s.Tool, argStr, s.Status)
		if s.Result != "" {
			preview := s.Result
			if len(preview) > 120 {
				preview = preview[:120] + "…"
			}
			line += " " + preview
		}
		stepLines = append(stepLines, line)
	}

	prompt := fmt.Sprintf(remediationJudgePrompt,
		input.FaultName, input.FaultDescription,
		guidanceSection,
		recoveryStatus, input.RecoveryTimeSecs,
		len(input.Steps),
		strings.Join(stepLines, "\n"),
	)

	raw, err := completer(ctx, prompt)
	if err != nil {
		return JudgeResult{Skipped: true, Reasoning: fmt.Sprintf("judge call failed: %v", err)}
	}

	jsonStr := extractJSON(raw)
	var parsed struct {
		Score     int    `json:"score"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return JudgeResult{Skipped: true, Reasoning: fmt.Sprintf("judge parse failed: %v (raw: %s)", err, raw)}
	}

	scoreMap := map[int]float64{0: 0.0, 1: 0.33, 2: 0.67, 3: 1.0}
	score, ok := scoreMap[parsed.Score]
	if !ok {
		score = 0.0
	}
	return JudgeResult{
		Score:     score,
		Reasoning: parsed.Reasoning,
		Model:     model,
	}
}

// formatArgs renders tool args as a compact key=value string, omitting
// connection plumbing keys that add noise without diagnostic value.
func formatArgs(args map[string]any) string {
	skip := map[string]bool{
		"connection_string": true, "host": true, "port": true,
		"dbname": true, "user": true, "password": true,
	}
	var parts []string
	for k, v := range args {
		if skip[k] {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, ", ")
}
