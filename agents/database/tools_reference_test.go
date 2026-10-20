package main

import (
	"testing"

	"helpdesk/agentutil/toolref"
)

// TestToolReference fails when a database tool lacks a capability or action
// class, when a see-also link no longer resolves, or when
// docs/tools/database.md is stale. Regenerate with `make docs-tools`.
func TestToolReference(t *testing.T) {
	tools, err := createTools()
	if err != nil {
		t.Fatalf("createTools: %v", err)
	}
	toolref.Check(t, toolref.Page{
		Agent: "postgres_database_agent",
		Tools: tools,
		Opts:  cardOptions(tools),
		Path:  "../../docs/tools/database.md",
		SeeAlso: map[string][]toolref.Link{
			"get_session_info":           {{Doc: "MUTATION_TOOLS.md", Section: "1.1 `get_session_info`"}},
			"cancel_query":               {{Doc: "MUTATION_TOOLS.md", Section: "1.2 `cancel_query`"}},
			"terminate_connection":       {{Doc: "MUTATION_TOOLS.md", Section: "1.3 `terminate_connection`"}},
			"terminate_idle_connections": {{Doc: "MUTATION_TOOLS.md", Section: "1.4 `terminate_idle_connections`"}},
			"set_archive_command": {
				{Doc: "MUTATION_TOOLS.md", Section: "1.5 `set_archive_command`"},
				{Doc: "BACKUP.md", Section: "1. WAL archiving failure"},
			},
			"resume_wal_replay":     {{Doc: "MUTATION_TOOLS.md", Section: "1.17 `resume_wal_replay`"}},
			"run_vacuum":            {{Doc: "MUTATION_TOOLS.md", Section: "1.18 `run_vacuum`"}},
			"drop_replication_slot": {{Doc: "MUTATION_TOOLS.md", Section: "1.19 `drop_replication_slot`"}},
			"reset_pg_setting":      {{Doc: "MUTATION_TOOLS.md", Section: "1.20 `reset_pg_setting`"}},
			"reset_cache_stats":     {{Doc: "MUTATION_TOOLS.md", Section: "1.21 `reset_cache_stats`"}},
			"get_backup_status":     {{Doc: "BACKUP.md", Section: "1. WAL archiving failure"}},
			"get_baseline":          {{Doc: "PLAYBOOK_OPS.md", Section: "1.1 Schedule regular baselines"}},
			"get_saved_snapshots":   {{Doc: "PLAYBOOK_OPS.md", Section: "1.1 Schedule regular baselines"}},
			"read_uploaded_file":    {{Doc: "API.md", Section: "Operator file uploads"}},
		},
	})
}
