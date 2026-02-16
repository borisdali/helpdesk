package audit

import "strings"

// ActionClass classifies the type of operation for audit and approval purposes.
type ActionClass string

const (
	// ActionRead is for read-only operations (SELECT, describe, get, list).
	// These are auto-approved and pose no risk to the system.
	ActionRead ActionClass = "read"

	// ActionWrite is for operations that modify state (INSERT, UPDATE, scale).
	// These may require policy-based or human approval depending on context.
	ActionWrite ActionClass = "write"

	// ActionDestructive is for operations that delete or destroy (DELETE, DROP, kill, terminate).
	// These typically require explicit human approval, especially in production.
	ActionDestructive ActionClass = "destructive"

	// ActionUnknown is for operations that haven't been classified.
	ActionUnknown ActionClass = "unknown"
)

// ToolClassification maps tool names to their action class.
// This is used to automatically classify operations for audit purposes.
var ToolClassification = map[string]ActionClass{
	// Database agent tools
	"check_connection":       ActionRead,
	"get_server_info":        ActionRead,
	"list_databases":         ActionRead,
	"get_running_queries":    ActionRead,
	"get_locks":              ActionRead,
	"get_table_stats":        ActionRead,
	"get_index_stats":        ActionRead,
	"get_config":             ActionRead,
	"explain_query":          ActionRead,
	"get_slow_queries":       ActionRead,
	"get_replication_status": ActionRead,
	"get_database_size":      ActionRead,
	"run_query":              ActionWrite, // Could be SELECT or DML - conservative
	"kill_query":             ActionDestructive,
	"alter_config":           ActionDestructive,
	"vacuum_table":           ActionWrite,
	"reindex_table":          ActionWrite,

	// Kubernetes agent tools
	"get_pods":              ActionRead,
	"get_pod_logs":          ActionRead,
	"get_services":          ActionRead,
	"get_endpoints":         ActionRead,
	"get_nodes":             ActionRead,
	"get_events":            ActionRead,
	"get_deployments":       ActionRead,
	"get_statefulsets":      ActionRead,
	"describe_pod":          ActionRead,
	"describe_node":         ActionRead,
	"get_resource_usage":    ActionRead,
	"scale_deployment":      ActionWrite,
	"scale_statefulset":     ActionWrite,
	"restart_deployment":    ActionWrite,
	"delete_pod":            ActionDestructive,
	"drain_node":            ActionDestructive,
	"cordon_node":           ActionWrite,
	"uncordon_node":         ActionWrite,

	// Incident agent tools
	"create_incident_bundle": ActionRead, // Creates a bundle but doesn't modify systems
	"list_incidents":         ActionRead,

	// Research agent tools
	"web_search": ActionRead,
}

// ClassifyTool returns the action class for a given tool name.
// Returns ActionUnknown if the tool is not in the classification map.
func ClassifyTool(toolName string) ActionClass {
	if class, ok := ToolClassification[toolName]; ok {
		return class
	}
	return ActionUnknown
}

// ClassifyEndpoint returns the action class for a gateway endpoint.
// This is used when the specific tool is not known.
func ClassifyEndpoint(method, path string) ActionClass {
	// GET requests are generally read-only
	if method == "GET" {
		return ActionRead
	}

	// POST to specific endpoints
	switch {
	case contains(path, "/research"):
		return ActionRead // Research is read-only
	case contains(path, "/incidents") && method == "POST":
		return ActionRead // Creating incident bundles is read-only
	case contains(path, "/db/"):
		return ActionUnknown // Depends on the specific tool
	case contains(path, "/k8s/"):
		return ActionUnknown // Depends on the specific tool
	case contains(path, "/query"):
		return ActionUnknown // Generic query endpoint
	default:
		return ActionUnknown
	}
}

// IsApprovalRequired returns true if this action class typically requires approval.
func (ac ActionClass) IsApprovalRequired() bool {
	switch ac {
	case ActionWrite, ActionDestructive:
		return true
	default:
		return false
	}
}

// RiskLevel returns a numeric risk level for sorting/comparison.
// Higher values indicate higher risk.
func (ac ActionClass) RiskLevel() int {
	switch ac {
	case ActionRead:
		return 0
	case ActionWrite:
		return 1
	case ActionDestructive:
		return 2
	default:
		return -1 // Unknown
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ClassifyDelegation infers the action class from a delegation message.
// It looks for keywords that indicate read, write, or destructive operations.
func ClassifyDelegation(agent, message string) ActionClass {
	msg := strings.ToLower(message)

	// Check for inquiry context - asking ABOUT something is a read
	// e.g., "when was vacuum last ran" is asking about vacuum history, not running vacuum
	inquiryPrefixes := []string{
		"when was", "when did", "when is", "last time",
		"history of", "previous", "how long ago", "how many times",
		"what is the", "what are the", "what was",
		"is there a", "are there any", "has been", "have been",
	}
	for _, prefix := range inquiryPrefixes {
		if strings.Contains(msg, prefix) {
			return ActionRead
		}
	}

	// Destructive keywords - check first as they're most critical
	destructiveKeywords := []string{
		"kill", "terminate", "delete", "drop", "remove", "destroy",
		"drain", "evict", "force", "truncate", "purge",
	}
	for _, kw := range destructiveKeywords {
		if strings.Contains(msg, kw) {
			return ActionDestructive
		}
	}

	// Write keywords - actions that modify state
	writeKeywords := []string{
		"scale", "restart", "update", "modify", "change", "alter",
		"create", "insert", "set", "patch", "apply", "rollout",
		"cordon", "uncordon", "vacuum", "reindex",
	}
	for _, kw := range writeKeywords {
		if strings.Contains(msg, kw) {
			return ActionWrite
		}
	}

	// Read keywords - information retrieval
	readKeywords := []string{
		"check", "get", "list", "show", "describe", "explain",
		"status", "info", "stats", "logs", "events", "search",
		"find", "query", "select", "count", "monitor",
	}
	for _, kw := range readKeywords {
		if strings.Contains(msg, kw) {
			return ActionRead
		}
	}

	// Default to unknown if we can't determine
	return ActionUnknown
}
