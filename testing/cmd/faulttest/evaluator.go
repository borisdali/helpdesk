package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"helpdesk/testing/faultlib"
	"helpdesk/testing/testutil"
)

// EvalResult contains the evaluation outcome for a single failure test.
type EvalResult struct {
	FailureID    string  `json:"failure_id"`
	FailureName  string  `json:"failure_name"`
	Category     string  `json:"category"`
	Score        float64 `json:"score"`
	Passed       bool    `json:"passed"`
	// RunID is the gateway playbook run_id (plr_*) for this fault's triage run.
	// Set from resp.RunID after the agent call; empty for injection failures.
	RunID        string  `json:"run_id,omitempty"`
	KeywordPass  bool    `json:"keyword_pass"`
	DiagnosisPass bool   `json:"diagnosis_pass"`
	ToolEvidence bool    `json:"tool_evidence"`
	// ToolEvidenceMode records how tool evidence was determined:
	//   "structured"   — exact name matching from the tool_call_summary DataPart (Option C, ADK agents)
	//   "text_fallback" — keyword matching against response text (Option B, non-ADK or gateway path)
	//   ""              — no expected tools; field not applicable
	ToolEvidenceMode string `json:"tool_evidence_mode,omitempty"`
	ResponseText string  `json:"response_text"`
	Duration     string  `json:"duration"`
	Error        string  `json:"error,omitempty"`

	// Component scores — always populated, allow operators to see exactly why
	// the composite score came out as it did without reverse-engineering.
	KeywordScore   float64 `json:"keyword_score"`             // 0.0 or 1.0 (any-of match)
	ToolScore      float64 `json:"tool_score"`                // 0.0-1.0 (fraction of expected tools found)
	DiagnosisScore float64 `json:"diagnosis_score"`           // 0.0-1.0 from judge or category match
	JudgeReasoning  string `json:"judge_reasoning,omitempty"`
	JudgeModel      string `json:"judge_model,omitempty"`
	JudgeSkipped    bool   `json:"judge_skipped,omitempty"`
	JudgeFatalError bool   `json:"judge_fatal_error,omitempty"` // 401/403 — will not recover on retry

	// CrystalBall is true when the gateway ran without playbook scaffolding.
	// Set only on --via-gateway runs; false on direct A2A calls.
	CrystalBall bool `json:"crystal_ball,omitempty"`

	// ProtocolViolation is true when the triage agent omitted the required
	// TRANSITION_TO/ESCALATE_TO handoff signal. Set from resp.Warnings when the
	// gateway reports the omission via the fallback gate. A protocol violation
	// caps the diagnosis score at 0.75 regardless of other eval components.
	ProtocolViolation bool `json:"protocol_violation,omitempty"`
	// GatewayWarnings holds warnings returned by the gateway for this run.
	GatewayWarnings []string `json:"gateway_warnings,omitempty"`

	// Remediation outcome (populated only when --remediate is set).
	RemediationAttempted bool    `json:"remediation_attempted,omitempty"`
	RemediationPassed    bool    `json:"remediation_passed,omitempty"`
	RecoveryTimeSecs     float64 `json:"recovery_time_seconds,omitempty"`
	RemediationError     string  `json:"remediation_error,omitempty"`

	// Phase 2 scoring fields.
	// RemediationScore is 0.0-1.0: 1.0 if recovered within half the verify timeout,
	// 0.75 if recovered within the full timeout, 0.0 if timed out or not attempted.
	RemediationScore  float64 `json:"remediation_score,omitempty"`
	// RemediationMethod records how remediation was triggered: "playbook", "agent_prompt", or "none".
	RemediationMethod string  `json:"remediation_method,omitempty"`
	// OverallScore combines composite score and remediation: Score*0.6 + RemediationScore*0.4
	// when remediation was attempted; equals Score when not attempted.
	OverallScore float64 `json:"overall_score,omitempty"`

	// Remediation judge fields — populated when --remediation-judge is set.
	RemediationJudgeScore     float64 `json:"remediation_judge_score,omitempty"`
	RemediationJudgeReasoning string  `json:"remediation_judge_reasoning,omitempty"`
	RemediationJudgeSkipped   bool    `json:"remediation_judge_skipped,omitempty"`

	// PrimaryConfidence is the triage agent's self-reported confidence on its
	// primary hypothesis. Derived from Hypotheses[primary].Confidence.
	// Zero when the agent did not emit structured hypotheses.
	PrimaryConfidence float64 `json:"primary_confidence,omitempty"`

	// PrimaryHypothesis is the label text of the primary hypothesis.
	// Derived from Hypotheses[primary].Text. Empty when not emitted.
	PrimaryHypothesis string `json:"primary_hypothesis,omitempty"`

	// SecondaryHypothesis and SecondaryConfidence are from the first non-primary
	// hypothesis (runner-up). Empty/zero when not present.
	SecondaryHypothesis string  `json:"secondary_hypothesis,omitempty"`
	SecondaryConfidence float64 `json:"secondary_confidence,omitempty"`

	// Hypotheses holds all structured hypotheses from the triage response, in
	// emission order. Populated from DiagnosticReport when the gateway returns
	// structured data; falls back to text-parsed H1/H2 otherwise.
	Hypotheses []HypothesisEntry `json:"hypotheses,omitempty"`
}

// HypothesisEntry represents one hypothesis from the agent's diagnostic report.
type HypothesisEntry struct {
	Text           string  `json:"text"`
	Confidence     float64 `json:"confidence"`
	IsPrimary      bool    `json:"is_primary"`
	RejectedReason string  `json:"rejected_reason,omitempty"`
}

// toolPatterns maps tool names to output patterns that indicate the tool was called.
var toolPatterns = map[string][]string{
	"check_connection":     {"connection", "connect", "reachable", "refused"},
	"get_database_info":    {"version", "server_version", "postgresql"},
	"get_active_connections": {"pg_stat_activity", "active", "idle", "pid", "query"},
	"get_connection_stats":  {"max_connections", "connections", "connection_count", "numbackends"},
	"get_database_stats":    {"cache hit", "blks_hit", "blks_read", "tup_returned", "hit ratio"},
	"get_bgwriter_stats":    {"maxwritten_clean", "buffers_backend", "checkpoints_req"},
	"get_config_parameter":  {"setting", "parameter", "configuration"},
	"get_replication_status": {"replication", "wal", "replay", "standby", "lag"},
	"get_lock_info":         {"lock", "pg_locks", "granted", "waiting", "blocked"},
	"get_table_stats":       {"n_dead_tup", "n_live_tup", "dead tuples", "autovacuum", "vacuum"},
	"get_pods":              {"pod", "Running", "Pending", "CrashLoopBackOff", "ImagePull"},
	"get_service":           {"ClusterIP", "LoadBalancer", "NodePort", "service"},
	"get_endpoints":         {"endpoint", "address", "subset"},
	"get_events":            {"event", "Warning", "Normal", "FailedScheduling", "BackOff"},
	"describe_pod":          {"Conditions", "Container", "State", "Restart"},
}

// scoreToolEvidence returns (toolScore float64, toolEvidence bool, toolEvidenceMode string)
// using audit tool names when available, then structured tool calls, then text matching.
//
// auditTools is the list of tool names retrieved from the audit service for the
// current agent call window. When non-nil, it is used as the authoritative source
// and the mode is "audit". When nil, falls back to structured tool calls or text matching.
func scoreToolEvidence(f Failure, resp testutil.AgentResponse, auditTools []string) (float64, bool, string) {
	if len(f.Evaluation.ExpectedTools) == 0 {
		return 1.0, true, ""
	}

	// Audit path (highest priority): exact name matching from the audit trail.
	if len(auditTools) > 0 {
		auditSet := make(map[string]bool, len(auditTools))
		for _, name := range auditTools {
			auditSet[name] = true
		}
		toolsFound := 0
		for _, expected := range f.Evaluation.ExpectedTools {
			if auditSet[expected] {
				toolsFound++
			}
		}
		toolScore := float64(toolsFound) / float64(len(f.Evaluation.ExpectedTools))
		return toolScore, toolScore > 0.5, "audit"
	}

	if resp.ToolCalls != nil {
		// Structured path (Option C): exact name matching against the tool call
		// summary DataPart emitted by agentutil.
		toolsFound := 0
		for _, expected := range f.Evaluation.ExpectedTools {
			for _, tc := range resp.ToolCalls {
				if tc.Name == expected && tc.Success {
					toolsFound++
					break
				}
			}
		}
		toolScore := float64(toolsFound) / float64(len(f.Evaluation.ExpectedTools))
		return toolScore, toolScore > 0.5, "structured"
	}

	// Fallback path (Option B): section-aware text matching.
	// Fires for gateway responses and non-ADK agents.
	slog.Warn("tool evidence using text-based detection; structured tool call data unavailable",
		"failure", f.ID,
		"reason", "agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)",
	)
	responseText := resp.Text
	sections := strings.Split(responseText, "\n---\n")
	toolsFound := 0
	for _, expected := range f.Evaluation.ExpectedTools {
		patterns, ok := toolPatterns[expected]
		if !ok {
			continue
		}
		for _, section := range sections {
			if strings.HasPrefix(strings.TrimSpace(section), "ERROR") {
				continue // skip error sections
			}
			sectionLower := strings.ToLower(section)
			for _, p := range patterns {
				if strings.Contains(sectionLower, strings.ToLower(p)) {
					toolsFound++
					goto nextTool
				}
			}
		}
	nextTool:
	}
	toolScore := float64(toolsFound) / float64(len(f.Evaluation.ExpectedTools))
	return toolScore, toolScore > 0.5, "text_fallback"
}

// Evaluate scores the agent's response against the failure's evaluation criteria.
// When resp.ToolCalls is non-nil (structured data from agentutil), tool evidence
// uses exact name matching. When nil (gateway path or non-ADK agent), falls back
// to section-aware text matching with a warning logged.
//
// auditTools is an optional list of tool names from the audit trail (pass nil to skip).
// When non-nil, audit-based matching takes priority over structured or text evidence.
func Evaluate(f Failure, resp testutil.AgentResponse, auditTools ...[]string) EvalResult {
	result := EvalResult{
		FailureID:   f.ID,
		FailureName: f.Name,
		Category:    f.Category,
	}

	var at []string
	if len(auditTools) > 0 {
		at = auditTools[0]
	}

	responseText := resp.Text
	lower := strings.ToLower(responseText)

	// 1. Keyword check (50% weight).
	keywordScore := 0.0
	if len(f.Evaluation.ExpectedKeywords.AnyOf) > 0 {
		for _, kw := range f.Evaluation.ExpectedKeywords.AnyOf {
			if strings.Contains(lower, strings.ToLower(kw)) {
				keywordScore = 1.0
				result.KeywordPass = true
				break
			}
		}
	} else {
		keywordScore = 1.0
		result.KeywordPass = true
	}

	// 2. Diagnosis category check (30% weight).
	diagnosisScore := 0.0
	if f.Evaluation.ExpectedDiagnosis.Category != "" {
		words := splitCategory(f.Evaluation.ExpectedDiagnosis.Category)
		matched := 0
		for _, w := range words {
			if strings.Contains(lower, strings.ToLower(w)) {
				matched++
			}
		}
		if len(words) > 0 {
			ratio := float64(matched) / float64(len(words))
			if ratio >= 0.5 {
				diagnosisScore = ratio
				result.DiagnosisPass = true
			}
		}
	} else {
		diagnosisScore = 1.0
		result.DiagnosisPass = true
	}
	result.DiagnosisScore = diagnosisScore

	// 3. Tool evidence check (20% weight).
	toolScore, toolEvidence, toolEvidenceMode := scoreToolEvidence(f, resp, at)
	result.ToolEvidence = toolEvidence
	result.ToolEvidenceMode = toolEvidenceMode

	// Store individual component scores so the reporter can surface them.
	result.KeywordScore = keywordScore
	result.ToolScore = toolScore

	// Weighted total.
	result.Score = keywordScore*0.5 + diagnosisScore*0.3 + toolScore*0.2

	// Pass criteria: score >= 0.6 AND keyword check passes.
	result.Passed = result.Score >= 0.6 && result.KeywordPass

	populateHypotheses(&result, resp, responseText)
	return result
}

// EvaluateWithJudge runs the standard evaluation and applies the LLM judge
// for semantic diagnosis scoring when completer is non-nil.
// When judge is enabled, weights shift to: tool*0.40 + judge*0.40 + keyword*0.20.
// Falls back to standard scoring when judge is skipped (no narrative or nil completer).
//
// auditTools is an optional list of tool names from the audit trail (pass nil to skip).
// When non-nil, audit-based matching takes priority over structured or text evidence.
func EvaluateWithJudge(ctx context.Context, f Failure, resp testutil.AgentResponse, completer faultlib.TextCompleter, model string, auditTools ...[]string) EvalResult {
	result := EvalResult{
		FailureID:   f.ID,
		FailureName: f.Name,
		Category:    f.Category,
	}

	var at []string
	if len(auditTools) > 0 {
		at = auditTools[0]
	}

	responseText := resp.Text
	lower := strings.ToLower(responseText)

	// 1. Keyword check.
	keywordScore := 0.0
	if len(f.Evaluation.ExpectedKeywords.AnyOf) > 0 {
		for _, kw := range f.Evaluation.ExpectedKeywords.AnyOf {
			if strings.Contains(lower, strings.ToLower(kw)) {
				keywordScore = 1.0
				result.KeywordPass = true
				break
			}
		}
	} else {
		keywordScore = 1.0
		result.KeywordPass = true
	}

	// 2. Diagnosis category check (used when judge is skipped).
	diagnosisScore := 0.0
	diagnosisPass := false
	if f.Evaluation.ExpectedDiagnosis.Category != "" {
		words := splitCategory(f.Evaluation.ExpectedDiagnosis.Category)
		matched := 0
		for _, w := range words {
			if strings.Contains(lower, strings.ToLower(w)) {
				matched++
			}
		}
		if len(words) > 0 {
			ratio := float64(matched) / float64(len(words))
			if ratio >= 0.5 {
				diagnosisScore = ratio
				diagnosisPass = true
			}
		}
	} else {
		diagnosisScore = 1.0
		diagnosisPass = true
	}

	// 3. Tool evidence check.
	toolScore, toolEvidence, toolEvidenceMode := scoreToolEvidence(f, resp, at)
	result.ToolEvidence = toolEvidence
	result.ToolEvidenceMode = toolEvidenceMode

	// Store individual component scores.
	result.KeywordScore = keywordScore
	result.ToolScore = toolScore

	// Convert Failure to faultlib.Failure for the judge call.
	// Pass structured tool names (when available) so the judge does not
	// incorrectly penalise tools the agent called but didn't name in prose.
	flibFailure := toFaultlibFailure(f)
	var structuredToolNames []string
	for _, tc := range resp.ToolCalls {
		structuredToolNames = append(structuredToolNames, tc.Name)
	}
	judgeResult := faultlib.Judge(ctx, flibFailure, responseText, completer, model, structuredToolNames...)
	result.JudgeSkipped = judgeResult.Skipped
	result.JudgeReasoning = judgeResult.Reasoning
	result.JudgeModel = judgeResult.Model
	result.JudgeFatalError = judgeResult.FatalError

	// Log when judge skips unexpectedly (error, not just missing narrative).
	if judgeResult.Skipped && judgeResult.Reasoning != "" {
		slog.Warn("LLM judge skipped", "failure", f.ID, "reason", judgeResult.Reasoning)
	}

	if judgeResult.Skipped {
		// Backward-compat weights: keyword*0.50 + diagnosis*0.30 + tool*0.20
		result.DiagnosisScore = diagnosisScore
		result.DiagnosisPass = diagnosisPass
		result.Score = keywordScore*0.5 + diagnosisScore*0.3 + toolScore*0.2
	} else {
		// Judge-enabled weights: tool*0.40 + judge*0.40 + keyword*0.20
		result.DiagnosisScore = judgeResult.Score
		result.DiagnosisPass = judgeResult.Score >= 0.5
		result.Score = toolScore*0.40 + judgeResult.Score*0.40 + keywordScore*0.20
	}

	// Pass criteria: score >= 0.6 AND keyword check passes.
	// When judge is active, also require judgeScore >= 0.33 (score 1/3 minimum —
	// agent must at least identify the symptom). A 0/3 judge means the agent
	// completely missed the fault; keywords+tools alone cannot override that.
	judgeVeto := !judgeResult.Skipped && judgeResult.Score < 0.33
	result.Passed = result.Score >= 0.6 && result.KeywordPass && !judgeVeto

	populateHypotheses(&result, resp, responseText)
	return result
}

// populateHypotheses fills result.Hypotheses from resp.DiagnosticReport when
// structured data is available, falling back to text-parsing H1/H2 otherwise.
// It also derives the convenience fields PrimaryHypothesis, PrimaryConfidence,
// SecondaryHypothesis, and SecondaryConfidence from the populated slice.
func populateHypotheses(result *EvalResult, resp testutil.AgentResponse, responseText string) {
	if hyps, _ := resp.DiagnosticReport["hypotheses"].([]any); len(hyps) > 0 {
		for _, h := range hyps {
			hm, _ := h.(map[string]any)
			text, _ := hm["text"].(string)
			conf, _ := hm["confidence"].(float64)
			isPrimary, _ := hm["is_primary"].(bool)
			rejected, _ := hm["rejected_reason"].(string)
			result.Hypotheses = append(result.Hypotheses, HypothesisEntry{
				Text: text, Confidence: conf, IsPrimary: isPrimary, RejectedReason: rejected,
			})
		}
	} else {
		// Text-parse fallback: extract H1 and H2 from the response narrative.
		if label, conf := extractHypothesisN(responseText, 1); label != "" {
			result.Hypotheses = append(result.Hypotheses, HypothesisEntry{
				Text: label, Confidence: conf, IsPrimary: true,
			})
		}
		if label, conf := extractHypothesisN(responseText, 2); label != "" {
			result.Hypotheses = append(result.Hypotheses, HypothesisEntry{
				Text: label, Confidence: conf,
			})
		}
	}

	// Derive convenience fields from the populated slice.
	for _, h := range result.Hypotheses {
		if h.IsPrimary {
			result.PrimaryHypothesis = h.Text
			result.PrimaryConfidence = h.Confidence
			break
		}
	}
	for _, h := range result.Hypotheses {
		if !h.IsPrimary && result.SecondaryHypothesis == "" {
			result.SecondaryHypothesis = h.Text
			result.SecondaryConfidence = h.Confidence
			break
		}
	}
}

// extractHypothesisN scans the agent response text for the Nth HYPOTHESIS_N: line
// and returns the label text and CONFIDENCE value.
//
// Two formats are accepted:
//
//	Inline:     HYPOTHESIS_N: <text> | CONFIDENCE: 0.95 | EVIDENCE: ...
//	Multi-line: HYPOTHESIS_N: <text>\nCONFIDENCE: 0.95\nEVIDENCE: ...
//
// Bold markdown wrappers (**) are stripped. Returns ("", 0.0) when not found.
func extractHypothesisN(text string, n int) (label string, conf float64) {
	prefix := fmt.Sprintf("HYPOTHESIS_%d:", n)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		clean := strings.TrimSpace(strings.Trim(line, "* \t"))
		if !strings.HasPrefix(strings.ToUpper(clean), prefix) {
			continue
		}
		body := strings.TrimSpace(clean[len(prefix):])
		// Extract label: everything before the first " | ".
		labelEnd := strings.Index(body, " | ")
		if labelEnd >= 0 {
			label = strings.TrimSpace(strings.Trim(body[:labelEnd], "* \t"))
		} else {
			label = strings.TrimSpace(strings.Trim(body, "* \t"))
		}
		// Extract confidence from pipe-separated inline parts first.
		for _, part := range strings.Split(body, " | ") {
			part = strings.TrimSpace(strings.Trim(part, "*"))
			if strings.HasPrefix(strings.ToUpper(part), "CONFIDENCE:") {
				val := strings.TrimSpace(part[len("CONFIDENCE:"):])
				if v, err := strconv.ParseFloat(val, 64); err == nil {
					conf = v
				}
				return
			}
		}
		// Fall back: scan up to 5 following lines for a standalone CONFIDENCE: line.
		for j := i + 1; j < len(lines) && j <= i+5; j++ {
			next := strings.TrimSpace(strings.Trim(lines[j], "* \t"))
			if strings.HasPrefix(strings.ToUpper(next), "CONFIDENCE:") {
				val := strings.TrimSpace(next[len("CONFIDENCE:"):])
				if v, err := strconv.ParseFloat(val, 64); err == nil {
					conf = v
				}
				return
			}
			if strings.HasPrefix(strings.ToUpper(next), "HYPOTHESIS_") {
				break
			}
		}
		return
	}
	return
}

func extractPrimaryConfidence(text string) float64 { _, c := extractHypothesisN(text, 1); return c }

// toFaultlibFailure converts a local Failure to a faultlib.Failure for judge calls.
func toFaultlibFailure(f Failure) faultlib.Failure {
	return faultlib.Failure{
		ID:          f.ID,
		Name:        f.Name,
		Category:    f.Category,
		Description: f.Description,
		Evaluation: faultlib.EvalSpec{
			ExpectedDiagnosis: faultlib.DiagnosisSpec{
				Category:  f.Evaluation.ExpectedDiagnosis.Category,
				Narrative: f.Evaluation.ExpectedDiagnosis.Narrative,
			},
		},
	}
}

// splitCategory breaks "connection_exhaustion" into ["connection", "exhaustion"].
func splitCategory(category string) []string {
	return strings.FieldsFunc(category, func(r rune) bool {
		return r == '_' || r == '-' || r == ' '
	})
}
