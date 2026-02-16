package audit

import "testing"

func TestClassifyTool(t *testing.T) {
	tests := []struct {
		tool     string
		expected ActionClass
	}{
		// Database read operations
		{"check_connection", ActionRead},
		{"get_server_info", ActionRead},
		{"list_databases", ActionRead},
		{"get_running_queries", ActionRead},
		{"explain_query", ActionRead},

		// Database write operations
		{"run_query", ActionWrite},
		{"vacuum_table", ActionWrite},
		{"reindex_table", ActionWrite},

		// Database destructive operations
		{"kill_query", ActionDestructive},
		{"alter_config", ActionDestructive},

		// Kubernetes read operations
		{"get_pods", ActionRead},
		{"get_pod_logs", ActionRead},
		{"describe_pod", ActionRead},

		// Kubernetes write operations
		{"scale_deployment", ActionWrite},
		{"restart_deployment", ActionWrite},

		// Kubernetes destructive operations
		{"delete_pod", ActionDestructive},
		{"drain_node", ActionDestructive},

		// Unknown
		{"unknown_tool", ActionUnknown},
		{"", ActionUnknown},
	}

	for _, tc := range tests {
		got := ClassifyTool(tc.tool)
		if got != tc.expected {
			t.Errorf("ClassifyTool(%q) = %q, want %q", tc.tool, got, tc.expected)
		}
	}
}

func TestClassifyEndpoint(t *testing.T) {
	tests := []struct {
		method   string
		path     string
		expected ActionClass
	}{
		{"GET", "/api/v1/agents", ActionRead},
		{"GET", "/api/v1/infrastructure", ActionRead},
		{"POST", "/api/v1/research", ActionRead},
		{"POST", "/api/v1/incidents", ActionRead},
		{"POST", "/api/v1/db/check_connection", ActionUnknown}, // Depends on tool
		{"POST", "/api/v1/k8s/get_pods", ActionUnknown},        // Depends on tool
		{"POST", "/api/v1/query", ActionUnknown},               // Generic
	}

	for _, tc := range tests {
		got := ClassifyEndpoint(tc.method, tc.path)
		if got != tc.expected {
			t.Errorf("ClassifyEndpoint(%q, %q) = %q, want %q", tc.method, tc.path, got, tc.expected)
		}
	}
}

func TestActionClass_IsApprovalRequired(t *testing.T) {
	tests := []struct {
		ac       ActionClass
		expected bool
	}{
		{ActionRead, false},
		{ActionWrite, true},
		{ActionDestructive, true},
		{ActionUnknown, false},
	}

	for _, tc := range tests {
		got := tc.ac.IsApprovalRequired()
		if got != tc.expected {
			t.Errorf("%q.IsApprovalRequired() = %v, want %v", tc.ac, got, tc.expected)
		}
	}
}

func TestActionClass_RiskLevel(t *testing.T) {
	tests := []struct {
		ac       ActionClass
		expected int
	}{
		{ActionRead, 0},
		{ActionWrite, 1},
		{ActionDestructive, 2},
		{ActionUnknown, -1},
	}

	for _, tc := range tests {
		got := tc.ac.RiskLevel()
		if got != tc.expected {
			t.Errorf("%q.RiskLevel() = %d, want %d", tc.ac, got, tc.expected)
		}
	}

	// Verify ordering
	if ActionRead.RiskLevel() >= ActionWrite.RiskLevel() {
		t.Error("read should be lower risk than write")
	}
	if ActionWrite.RiskLevel() >= ActionDestructive.RiskLevel() {
		t.Error("write should be lower risk than destructive")
	}
}

func TestClassifyDelegation(t *testing.T) {
	tests := []struct {
		name     string
		agent    string
		message  string
		expected ActionClass
	}{
		// Read operations
		{
			name:     "check database connection",
			agent:    "postgres_database_agent",
			message:  "Check if the database is reachable using connection_string: host=localhost",
			expected: ActionRead,
		},
		{
			name:     "get pod status",
			agent:    "k8s_agent",
			message:  "Get the status of pods in namespace default",
			expected: ActionRead,
		},
		{
			name:     "show queries",
			agent:    "postgres_database_agent",
			message:  "Show me all running queries",
			expected: ActionRead,
		},
		{
			name:     "list deployments",
			agent:    "k8s_agent",
			message:  "List all deployments in the cluster",
			expected: ActionRead,
		},
		{
			name:     "when was vacuum ran - inquiry about write operation",
			agent:    "postgres_database_agent",
			message:  "When was vacuum last ran on the users table?",
			expected: ActionRead,
		},
		{
			name:     "last reindex - inquiry about write operation",
			agent:    "postgres_database_agent",
			message:  "When was the last reindex performed?",
			expected: ActionRead,
		},
		{
			name:     "history of restarts - inquiry about write operation",
			agent:    "k8s_agent",
			message:  "What is the restart history for the api-server pod?",
			expected: ActionRead,
		},

		// Write operations
		{
			name:     "scale deployment",
			agent:    "k8s_agent",
			message:  "Scale the web deployment to 5 replicas",
			expected: ActionWrite,
		},
		{
			name:     "restart deployment",
			agent:    "k8s_agent",
			message:  "Restart the api-server deployment",
			expected: ActionWrite,
		},
		{
			name:     "update config",
			agent:    "postgres_database_agent",
			message:  "Update the max_connections parameter",
			expected: ActionWrite,
		},

		// Destructive operations
		{
			name:     "kill query",
			agent:    "postgres_database_agent",
			message:  "Kill the long-running query with PID 12345",
			expected: ActionDestructive,
		},
		{
			name:     "delete pod",
			agent:    "k8s_agent",
			message:  "Delete the stuck pod web-abc123",
			expected: ActionDestructive,
		},
		{
			name:     "drain node",
			agent:    "k8s_agent",
			message:  "Drain node worker-1 for maintenance",
			expected: ActionDestructive,
		},
		{
			name:     "terminate process",
			agent:    "postgres_database_agent",
			message:  "Terminate the backend process blocking the table",
			expected: ActionDestructive,
		},

		// Unknown - no clear keywords
		{
			name:     "ambiguous request",
			agent:    "postgres_database_agent",
			message:  "Help me with the database",
			expected: ActionUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyDelegation(tc.agent, tc.message)
			if got != tc.expected {
				t.Errorf("ClassifyDelegation(%q, %q) = %q, want %q",
					tc.agent, tc.message, got, tc.expected)
			}
		})
	}
}
