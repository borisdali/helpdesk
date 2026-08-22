package faultlib

import (
	"strings"
)

// ToolPatterns maps tool names to output patterns that indicate the tool was called.
// Used for tool evidence scoring (broad patterns are fine here).
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
	"get_session_info":     {"session", "pid", "state", "duration", "client_addr"},
	"terminate_connection": {"terminated", "terminate", "pg_terminate_backend"},
	"cancel_query":         {"cancelled", "cancel", "pg_cancel_backend"},
	// New DB diagnostic tools (Phase 1a).
	"get_slow_queries":     {"pg_stat_statements", "total_exec_time", "mean_time"},
	"get_vacuum_status":    {"dead_ratio", "last_autovacuum", "vacuum needed"},
	"get_disk_usage":       {"pg_database_size", "pg_total_relation_size", "database size"},
	"get_wait_events":      {"wait_event_type", "wait_event", "sessions waiting"},
	"get_blocking_queries": {"blocking_pid", "lock_type", "relation_name"},
	"get_bgwriter_stats":   {"maxwritten_clean", "buffers_backend", "checkpoints_req"},
	"get_pg_settings":      {"pg_settings", "non-default", "altered"},
	"get_extensions":       {"installed_version", "pg_available_extensions"},
	"get_baseline":         {"server info", "pg settings", "baseline"},
	"explain_query":        {"seq scan", "index scan", "cost="},
	// New K8s tools (Phase 1a).
	"get_pod_resources": {"cpu request", "memory limit", "requests", "millicores"},
	"get_node_status":   {"memorypressure", "diskpressure", "allocatable", "node condition"},
	// scale_deployment is used in k8s-scale-to-zero; patterns reference output text.
	"scale_deployment": {"scaled", "replicas", "scale"},
	// Sysadmin agent tools.
	"check_host":        {"status", "runtime", "container", "stopped", "running", "exited"},
	"get_host_logs":     {"log", "logs", "stderr", "stdout"},
	"check_disk":        {"disk", "filesystem", "available", "used"},
	"check_memory":      {"memory", "mem", "available", "used"},
	"read_pg_log_file":  {"postgresql", "log", "fatal", "panic", "crash", "error"},
}

// ToolOrderingPatterns overrides ToolPatterns for the tool-ordering check only.
// These patterns are chosen to appear exclusively in tool *output* sections, not
// in the agent's introductory narrative, so that firstPatternIndex reliably finds
// the position where a tool's result was actually printed.
//
// Example problem: ToolPatterns["terminate_connection"] contains "terminate", which
// appears in "I'll terminate the session immediately" before any tool is called,
// causing the ordering check to report terminate_connection before get_session_info
// even when the agent called them in the correct order.
var ToolOrderingPatterns = map[string][]string{
	// "pg_terminate_backend" only appears in the tool's SQL output, never in prose.
	"terminate_connection": {"pg_terminate_backend"},
	// "pg_cancel_backend" same — only in tool output.
	"cancel_query": {"pg_cancel_backend"},
	// "client_addr" is a column name from pg_stat_activity; not used in narrative.
	"get_session_info": {"client_addr", "backend_xid", "idle in transaction"},
}

// CheckToolOrdering verifies that for each [tool_a, tool_b] pair, the earliest
// pattern match for tool_a appears before the earliest match for tool_b in lower
// (lower must already be lowercased — callers own that transform since they
// typically need the lowercased text for other checks too).
// Uses ToolOrderingPatterns when available (more specific than ToolPatterns) so
// that patterns in the agent's introductory narrative don't pollute the check.
//
// Exported for cmd/faulttest's own Evaluate/EvaluateWithJudge (item 7 dedup,
// v0.26) — this and FirstOrderingPatternIndex/ToolPatterns/ToolOrderingPatterns
// are the genuinely shared, reusable pieces of what evaluator.go used to be;
// the orchestration functions that used to live here (Evaluate/EvaluateWithJudge/
// computeComponents) were deleted as dead code — confirmed zero production
// callers (cmd/faulttest's own, richer Evaluate/EvaluateWithJudge are the only
// ones actually wired into the real fault-testing loop; faultlib.Runner.Run
// never called this package's Evaluate internally either). cmd/faulttest's
// EvalResult carries gateway-specific signals (ProtocolViolation, TargetDrift,
// Mismatch, Hypotheses, ...) this package's own (smaller) EvalResult never
// did, so re-pointing cmd/faulttest at a shared Evaluate here was never a
// viable merge direction — only the underlying data/pure-logic was worth
// deduplicating.
func CheckToolOrdering(order [][]string, lower string) bool {
	for _, pair := range order {
		if len(pair) != 2 {
			continue
		}
		posA := FirstOrderingPatternIndex(pair[0], lower)
		posB := FirstOrderingPatternIndex(pair[1], lower)
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

// FirstOrderingPatternIndex returns the earliest position of any ordering pattern
// for toolName in lower. It prefers ToolOrderingPatterns over ToolPatterns so that
// ordering is anchored to tool-output content rather than narrative keywords.
func FirstOrderingPatternIndex(toolName, lower string) int {
	patterns, ok := ToolOrderingPatterns[toolName]
	if !ok {
		patterns, ok = ToolPatterns[toolName]
	}
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
