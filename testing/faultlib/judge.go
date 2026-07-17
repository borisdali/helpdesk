package faultlib

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TextCompleter sends a single-turn prompt to an LLM and returns the text response.
// This matches the signature of agentutil.TextCompleter.
type TextCompleter func(ctx context.Context, prompt string) (string, error)

// JudgeResult holds the output of the LLM judge.
type JudgeResult struct {
	Score      float64 // 0.0, 0.33, 0.67, or 1.0
	Reasoning  string
	Model      string
	Skipped    bool // true when narrative is empty or completer is nil
	FatalError bool // true when the error is non-transient (e.g. 401/403 auth failure)
}

const judgePromptTemplate = `You are evaluating an AI database operations agent's diagnostic response.

FAULT INJECTED: %s — %s

EXPECTED DIAGNOSIS:
%s
%s
AGENT RESPONSE:
%s

Score the agent's diagnosis on a scale of 0–3:
3 = Correct root cause AND all key points in EXPECTED DIAGNOSIS addressed (including any specific tool, mechanism, or nuance called out there)
2 = Correct root cause identified, but one or more key points from EXPECTED DIAGNOSIS are missing or incomplete
1 = Identified the symptom but missed the underlying root cause
0 = Wrong diagnosis, irrelevant response, or no meaningful diagnosis

Respond with JSON only, no other text: {"score": <0|1|2|3>, "reasoning": "<one concise sentence>"}`

// Judge evaluates an agent's response using an LLM judge.
// When completer is nil or narrative is empty, returns a skipped result.
// toolCalls is the authoritative structured list of tool names called by the agent;
// when non-empty it is included in the prompt so the judge does not incorrectly
// penalise the agent for "not calling" tools that appear in the structured data.
func Judge(ctx context.Context, f Failure, responseText string, completer TextCompleter, model string, toolCalls ...string) JudgeResult {
	if completer == nil || f.Evaluation.ExpectedDiagnosis.Narrative == "" {
		return JudgeResult{Skipped: true}
	}

	toolSection := ""
	if len(toolCalls) > 0 {
		toolSection = fmt.Sprintf("\nTOOLS CALLED (structured, authoritative — do not second-guess this list):\n%s\n", strings.Join(toolCalls, ", "))
	}

	prompt := fmt.Sprintf(judgePromptTemplate,
		f.Name, f.Description,
		f.Evaluation.ExpectedDiagnosis.Narrative,
		toolSection,
		responseText,
	)

	raw, err := completer(ctx, prompt)
	if err != nil {
		return JudgeResult{
			Skipped:    true,
			Reasoning:  fmt.Sprintf("judge call failed: %v", err),
			FatalError: isAuthError(err),
		}
	}

	// Extract JSON from response (model may wrap it in markdown fences).
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

// isAuthError returns true when err looks like a non-transient authentication
// failure (401 Unauthorized, 403 Forbidden). These will not resolve on retry
// and should cause the caller to fail fast rather than silently skip scoring.
func isAuthError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "401") ||
		strings.Contains(s, "403") ||
		strings.Contains(s, "Unauthorized") ||
		strings.Contains(s, "authentication_error")
}

// extractJSON pulls the first {...} block from s, handling markdown code fences.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	// Strip markdown fences if present.
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j >= i {
			return s[i : j+1]
		}
	}
	return s
}
