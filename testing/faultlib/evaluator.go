package faultlib

import (
	"strings"
)

// ToolPatterns maps tool names to output patterns that indicate the tool was called.
var ToolPatterns = map[string][]string{
	"check_connection":       {"connection", "connect", "reachable", "refused"},
	"get_database_info":      {"version", "server_version", "postgresql"},
	"get_active_connections": {"pg_stat_activity", "active", "idle", "pid", "query"},
	"get_connection_stats":   {"max_connections", "connections", "connection_count", "numbackends"},
	"get_database_stats":     {"cache hit", "blks_hit", "blks_read", "tup_returned", "hit ratio"},
	"get_config_parameter":   {"setting", "parameter", "configuration"},
	"get_replication_status": {"replication", "wal", "replay", "standby", "lag"},
	"get_lock_info":          {"lock", "pg_locks", "granted", "waiting", "blocked"},
	"get_table_stats":        {"n_dead_tup", "n_live_tup", "dead tuples", "autovacuum", "vacuum"},
	"get_pods":               {"pod", "Running", "Pending", "CrashLoopBackOff", "ImagePull"},
	"get_service":            {"ClusterIP", "LoadBalancer", "NodePort", "service"},
	"get_endpoints":          {"endpoint", "address", "subset"},
	"get_events":             {"event", "Warning", "Normal", "FailedScheduling", "BackOff"},
	"describe_pod":           {"Conditions", "Container", "State", "Restart"},
	// Session management tools — patterns reflect what the agent writes in its response.
	"get_session_info":      {"session", "pid", "state", "duration", "client_addr"},
	"terminate_connection":  {"terminated", "terminate", "pg_terminate_backend"},
	"cancel_query":          {"cancelled", "cancel", "pg_cancel_backend"},
}

// Evaluate scores the agent's response against the failure's evaluation criteria.
func Evaluate(f Failure, responseText string) EvalResult {
	result := EvalResult{
		FailureID:   f.ID,
		FailureName: f.Name,
		Category:    f.Category,
	}

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
		// No keywords specified — pass by default.
		keywordScore = 1.0
		result.KeywordPass = true
	}

	// 2. Diagnosis category check (30% weight).
	diagnosisScore := 0.0
	if f.Evaluation.ExpectedDiagnosis.Category != "" {
		words := SplitCategory(f.Evaluation.ExpectedDiagnosis.Category)
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
		toolsFound := 0
		for _, tool := range f.Evaluation.ExpectedTools {
			patterns, ok := ToolPatterns[tool]
			if !ok {
				continue
			}
			for _, p := range patterns {
				if strings.Contains(lower, strings.ToLower(p)) {
					toolsFound++
					break
				}
			}
		}
		toolScore = float64(toolsFound) / float64(len(f.Evaluation.ExpectedTools))
		result.ToolEvidence = toolScore > 0.5
	} else {
		toolScore = 1.0
		result.ToolEvidence = true
	}

	// Weighted total.
	result.Score = keywordScore*0.5 + diagnosisScore*0.3 + toolScore*0.2

	// 4. Tool ordering check (does not affect Score; gates Passed when configured).
	result.OrderingPass = true
	if len(f.Evaluation.ExpectedToolOrder) > 0 {
		result.OrderingPass = checkToolOrdering(f.Evaluation.ExpectedToolOrder, lower)
	}

	// Pass criteria: score >= 0.6 AND keyword check passes AND ordering holds.
	result.Passed = result.Score >= 0.6 && result.KeywordPass && result.OrderingPass

	return result
}

// checkToolOrdering verifies that for each [tool_a, tool_b] pair, the earliest
// pattern match for tool_a appears before the earliest match for tool_b in lower.
func checkToolOrdering(order [][]string, lower string) bool {
	for _, pair := range order {
		if len(pair) != 2 {
			continue
		}
		posA := firstPatternIndex(pair[0], lower)
		posB := firstPatternIndex(pair[1], lower)
		if posA < 0 || posB < 0 {
			// One or both tools have no evidence — ordering cannot be confirmed.
			return false
		}
		if posA >= posB {
			return false
		}
	}
	return true
}

// firstPatternIndex returns the index of the earliest pattern match for toolName
// in lower (already lower-cased), or -1 if no pattern matches.
func firstPatternIndex(toolName, lower string) int {
	patterns, ok := ToolPatterns[toolName]
	if !ok {
		return -1
	}
	earliest := -1
	for _, p := range patterns {
		idx := strings.Index(lower, strings.ToLower(p))
		if idx >= 0 && (earliest < 0 || idx < earliest) {
			earliest = idx
		}
	}
	return earliest
}

// SplitCategory breaks "connection_exhaustion" into ["connection", "exhaustion"].
func SplitCategory(category string) []string {
	return strings.FieldsFunc(category, func(r rune) bool {
		return r == '_' || r == '-' || r == ' '
	})
}
