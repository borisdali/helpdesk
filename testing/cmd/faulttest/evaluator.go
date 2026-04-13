package main

import (
	"log/slog"
	"strings"

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

	// Remediation outcome (populated only when --remediate is set).
	RemediationAttempted bool    `json:"remediation_attempted,omitempty"`
	RemediationPassed    bool    `json:"remediation_passed,omitempty"`
	RecoveryTimeSecs     float64 `json:"recovery_time_seconds,omitempty"`
	RemediationError     string  `json:"remediation_error,omitempty"`
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

// Evaluate scores the agent's response against the failure's evaluation criteria.
// When resp.ToolCalls is non-nil (structured data from agentutil), tool evidence
// uses exact name matching. When nil (gateway path or non-ADK agent), falls back
// to section-aware text matching with a warning logged.
func Evaluate(f Failure, resp testutil.AgentResponse) EvalResult {
	result := EvalResult{
		FailureID:   f.ID,
		FailureName: f.Name,
		Category:    f.Category,
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

	// 3. Tool evidence check (20% weight).
	toolScore := 0.0
	if len(f.Evaluation.ExpectedTools) > 0 {
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
			toolScore = float64(toolsFound) / float64(len(f.Evaluation.ExpectedTools))
			result.ToolEvidence = toolScore > 0.5
			result.ToolEvidenceMode = "structured"
		} else {
			// Fallback path (Option B): section-aware text matching.
			// Fires for gateway responses and non-ADK agents.
			slog.Warn("tool evidence using text-based detection; structured tool call data unavailable",
				"failure", f.ID,
				"reason", "agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)",
			)
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
			toolScore = float64(toolsFound) / float64(len(f.Evaluation.ExpectedTools))
			result.ToolEvidence = toolScore > 0.5
			result.ToolEvidenceMode = "text_fallback"
		}
	} else {
		toolScore = 1.0
		result.ToolEvidence = true
	}

	// Weighted total.
	result.Score = keywordScore*0.5 + diagnosisScore*0.3 + toolScore*0.2

	// Pass criteria: score >= 0.6 AND keyword check passes.
	result.Passed = result.Score >= 0.6 && result.KeywordPass

	return result
}

// splitCategory breaks "connection_exhaustion" into ["connection", "exhaustion"].
func splitCategory(category string) []string {
	return strings.FieldsFunc(category, func(r rune) bool {
		return r == '_' || r == '-' || r == ' '
	})
}
