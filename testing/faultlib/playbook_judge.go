package faultlib

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PlaybookDiffInput holds the two playbook versions to compare.
type PlaybookDiffInput struct {
	// BeforeID / AfterID — playbook_id strings (for logging only)
	BeforeID string
	AfterID  string

	// Fields from the "before" (active) version
	BeforeName        string
	BeforeDescription string
	BeforeGuidance    string
	BeforeSymptoms    []string
	BeforeEscalation  []string

	// Fields from the "after" (proposed) version
	AfterName        string
	AfterDescription string
	AfterGuidance    string
	AfterSymptoms    []string
	AfterEscalation  []string

	// Pre-computed list of operational fields that changed (e.g. "execution_mode: fleet → agent_approve").
	// When non-empty, the judge flags them automatically regardless of its own analysis.
	OperationalDrift []string
}

// PlaybookJudgeResult is the structured output of JudgePlaybookDiff.
type PlaybookJudgeResult struct {
	// Verdict is the overall recommendation.
	// APPROVE = the new version is clearly better, safe to activate.
	// NEEDS_REVIEW = uncertain or mixed; human review recommended.
	// REJECT = the new version is worse or introduces risk.
	Verdict string

	// GuidanceQuality describes whether the guidance change is an improvement.
	GuidanceQuality string

	// EscalationSafety describes whether escalation criteria are tighter, looser, or unchanged.
	EscalationSafety string

	// Reasoning is a single sentence overall justification.
	Reasoning string

	// OperationalDrift echoes back the pre-computed drift list (from the caller).
	OperationalDrift []string

	// Model is the judge model name (for provenance).
	Model string

	// Skipped is true when the judge was not run (nil completer, etc.).
	Skipped bool
}

const playbookJudgePromptTemplate = `You are reviewing a proposed update to an AI operations playbook.

Your job: assess whether the new version improves on the old one for guiding an AI database operations agent.
Focus only on knowledge fields (name, description, guidance, symptoms, escalation).
Operational fields (execution_mode, approval_mode, routing) are managed separately and are NOT part of your review.

== BEFORE (currently active, version %s) ==
Name: %s
Description: %s

Guidance:
%s

Symptoms:
%s

Escalation criteria:
%s

== AFTER (proposed, version %s) ==
Name: %s
Description: %s

Guidance:
%s

Symptoms:
%s

Escalation criteria:
%s

Evaluate:
1. guidance_quality: Is the guidance change an improvement? (more specific, better sequencing, clearer thresholds, etc.) Or is it worse/neutral?
2. escalation_safety: Are escalation criteria tighter (safer), looser (riskier), or unchanged?
3. verdict: APPROVE if the update is clearly beneficial; NEEDS_REVIEW if uncertain or mixed; REJECT if the new version is worse.
4. reasoning: One concise sentence summarising the overall assessment.

Respond with JSON only, no other text:
{"verdict": "APPROVE"|"NEEDS_REVIEW"|"REJECT", "guidance_quality": "<sentence>", "escalation_safety": "<sentence>", "reasoning": "<sentence>"}`

// JudgePlaybookDiff asks an LLM judge to evaluate a playbook version update.
// When completer is nil, returns a skipped result.
func JudgePlaybookDiff(ctx context.Context, input PlaybookDiffInput, beforeVer, afterVer string, completer TextCompleter, model string) PlaybookJudgeResult {
	if completer == nil {
		return PlaybookJudgeResult{Skipped: true, OperationalDrift: input.OperationalDrift}
	}

	prompt := fmt.Sprintf(playbookJudgePromptTemplate,
		beforeVer,
		input.BeforeName, input.BeforeDescription,
		blankIfEmpty(input.BeforeGuidance, "(none)"),
		bulletList(input.BeforeSymptoms),
		bulletList(input.BeforeEscalation),
		afterVer,
		input.AfterName, input.AfterDescription,
		blankIfEmpty(input.AfterGuidance, "(none)"),
		bulletList(input.AfterSymptoms),
		bulletList(input.AfterEscalation),
	)

	raw, err := completer(ctx, prompt)
	if err != nil {
		return PlaybookJudgeResult{
			Skipped:          true,
			Reasoning:        fmt.Sprintf("judge call failed: %v", err),
			OperationalDrift: input.OperationalDrift,
		}
	}

	jsonStr := extractJSON(raw)
	var parsed struct {
		Verdict          string `json:"verdict"`
		GuidanceQuality  string `json:"guidance_quality"`
		EscalationSafety string `json:"escalation_safety"`
		Reasoning        string `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return PlaybookJudgeResult{
			Skipped:          true,
			Reasoning:        fmt.Sprintf("judge parse failed: %v (raw: %s)", err, raw),
			OperationalDrift: input.OperationalDrift,
		}
	}

	// Normalise verdict to uppercase.
	verdict := strings.ToUpper(strings.TrimSpace(parsed.Verdict))
	switch verdict {
	case "APPROVE", "NEEDS_REVIEW", "REJECT":
	default:
		verdict = "NEEDS_REVIEW"
	}

	return PlaybookJudgeResult{
		Verdict:          verdict,
		GuidanceQuality:  parsed.GuidanceQuality,
		EscalationSafety: parsed.EscalationSafety,
		Reasoning:        parsed.Reasoning,
		OperationalDrift: input.OperationalDrift,
		Model:            model,
	}
}

func blankIfEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func bulletList(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, item := range items {
		b.WriteString("• ")
		b.WriteString(item)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
