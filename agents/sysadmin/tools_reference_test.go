package main

import (
	"testing"

	"helpdesk/agentutil/toolref"
)

// TestToolReference fails when a sysadmin tool lacks a capability or action
// class, when a see-also link no longer resolves, or when
// docs/tools/sysadmin.md is stale. Regenerate with `make docs-tools`.
func TestToolReference(t *testing.T) {
	tools, err := createTools()
	if err != nil {
		t.Fatalf("createTools: %v", err)
	}
	toolref.Check(t, toolref.Page{
		Agent: agentName,
		Tools: tools,
		Opts:  cardOptions(tools),
		Path:  "../../docs/tools/sysadmin.md",
		SeeAlso: map[string][]toolref.Link{
			"check_host":       {{Doc: "SYSADMIN_AGENT.md", Section: "`check_host`"}},
			"get_host_logs":    {{Doc: "SYSADMIN_AGENT.md", Section: "`get_host_logs`"}},
			"check_disk":       {{Doc: "SYSADMIN_AGENT.md", Section: "`check_disk`"}},
			"check_memory":     {{Doc: "SYSADMIN_AGENT.md", Section: "`check_memory`"}},
			"read_pg_log_file": {{Doc: "SYSADMIN_AGENT.md", Section: "`read_pg_log_file`"}},
			"restart_container": {
				{Doc: "MUTATION_TOOLS.md", Section: "1.11 `restart_container`"},
				{Doc: "SYSADMIN_AGENT.md", Section: "`restart_container`"},
			},
			"restart_service": {
				{Doc: "MUTATION_TOOLS.md", Section: "1.12 `restart_service`"},
				{Doc: "SYSADMIN_AGENT.md", Section: "`restart_service`"},
			},
			"get_pgbackrest_status": {
				{Doc: "MUTATION_TOOLS.md", Section: "1.13 `get_pgbackrest_status`"},
				{Doc: "SYSADMIN_AGENT.md", Section: "`get_pgbackrest_status`"},
				{Doc: "BACKUP.md", Section: "2. pgBackRest job-level backup health"},
			},
			"run_pgbackrest_backup": {
				{Doc: "MUTATION_TOOLS.md", Section: "1.14 `run_pgbackrest_backup`"},
				{Doc: "SYSADMIN_AGENT.md", Section: "`run_pgbackrest_backup`"},
				{Doc: "BACKUP.md", Section: "2. pgBackRest job-level backup health"},
			},
			"restore_from_backup": {
				{Doc: "MUTATION_TOOLS.md", Section: "1.15 `restore_from_backup`"},
				{Doc: "BACKUP.md", Section: "3. Restore from backup after data loss"},
			},
			"restore_to_point_in_time": {
				{Doc: "MUTATION_TOOLS.md", Section: "1.15.1 `restore_to_point_in_time`"},
				{Doc: "BACKUP.md", Section: "3. Restore from backup after data loss"},
			},
		},
	})
}
