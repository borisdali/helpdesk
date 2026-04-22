package main

import (
	"context"
	"log/slog"
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
	JudgeReasoning string  `json:"judge_reasoning,omitempty"`
	JudgeModel     string  `json:"judge_model,omitempty"`
	JudgeSkipped   bool    `json:"judge_skipped,omitempty"`

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
}

// toolPatterns maps tool names to output patterns that indicate the tool was called.
var toolPatterns = map[string][]string{
	"check_connection":     {"connection", "connect", "reachable", "refused"},
	"get_database_info":    {"version", "server_version", "postgresql"},
	"get_active_connections": {"pg_stat_activity", "active", "idle", "pid", "query"},
	"get_connection_stats":  {"max_connections", "connections", "connection_count", "numbackends"},
	"get_database_stats":    {"cache hit", "blks_hit", "blks_read", "tup_returned", "hit ratio"},
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
	flibFailure := toFaultlibFailure(f)
	judgeResult := faultlib.Judge(ctx, flibFailure, responseText, completer, model)
	result.JudgeSkipped = judgeResult.Skipped
	result.JudgeReasoning = judgeResult.Reasoning
	result.JudgeModel = judgeResult.Model

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

	return result
}

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
