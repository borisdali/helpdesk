package toolregistry

// IntentMap maps well-known job intent labels to the canonical set of tools
// that should be used for that intent. The planner injects this as a hard
// directive: when the request matches a known intent, use exactly these tools.
//
// Keep this in sync with fleet job descriptions and operator documentation.
// Same philosophy as audit.ToolClassification — human-curated, source-of-truth.
var IntentMap = map[string][]string{
	"health_check":       {"get_status_summary"},
	"connectivity_check": {"check_connection"},
	"replication_check":  {"get_replication_status"},
	"lock_check":         {"get_lock_info"},
	"table_bloat":        {"get_table_stats"},
}
