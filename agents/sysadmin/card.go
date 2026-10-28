package main

import (
	"github.com/a2aproject/a2a-go/a2a"
	"google.golang.org/adk/tool"

	"helpdesk/agentutil"
	"helpdesk/internal/buildinfo"
	"helpdesk/internal/toolregistry"
)

const agentName = "sysadmin_agent"

// cardOptions returns the agent card metadata for the sysadmin agent's tools.
// Every tool must declare at least one capability; tools_reference_test.go
// enforces this and keeps docs/tools/sysadmin.md in sync.
func cardOptions(tools []tool.Tool) agentutil.CardOptions {
	return agentutil.CardOptions{
		Version:  buildinfo.Version,
		Provider: &a2a.AgentProvider{Org: "Helpdesk"},
		SkillTags: map[string][]string{
			agentName:                                  {"host", "infrastructure", "diagnostics"},
			agentName + "-check_host":                  {"host", "container", "status"},
			agentName + "-get_host_logs":               {"host", "container", "logs"},
			agentName + "-check_disk":                  {"host", "storage", "diagnostics"},
			agentName + "-check_memory":                {"host", "memory", "diagnostics"},
			agentName + "-read_pg_log_file":            {"host", "postgres", "logs", "diagnostics"},
			agentName + "-restart_container":           {"host", "container", "remediation"},
			agentName + "-restart_service":             {"host", "systemd", "remediation"},
			agentName + "-get_pgbackrest_status":       {"host", "backup", "diagnostics"},
			agentName + "-run_pgbackrest_backup":       {"host", "backup", "remediation"},
			agentName + "-restore_from_backup":         {"host", "backup", "remediation"},
			agentName + "-restore_to_point_in_time":    {"host", "backup", "remediation"},
			agentName + "-plan_point_in_time_recovery": {"host", "backup", "diagnostics"},
			agentName + "-verify_restore_recovery":     {"host", "backup", "postgres", "logs", "diagnostics"},
		},
		SkillExamples: map[string][]string{
			agentName + "-check_host":            {"Is the alloydb-omni container running?"},
			agentName + "-get_host_logs":         {"Show the last 200 log lines from the prod_db container"},
			agentName + "-restart_container":     {"Restart the prod_db container — crash loop detected"},
			agentName + "-get_pgbackrest_status": {"Check whether pgBackRest's last backup for prod_db is stale"},
		},
		SkillAutoRemediationEligible: map[string]bool{
			agentName + "-restart_container": true,
			agentName + "-restart_service":   true,
		},
		SkillSchemaHash: agentutil.ComputeSchemaFingerprints(agentName, tools),
		ToolSchemas:     agentutil.ComputeInputSchemas(tools),
		SkillCapabilities: map[string][]string{
			agentName + "-check_host":                  {toolregistry.CapProcessStatus},
			agentName + "-get_host_logs":               {toolregistry.CapLogs},
			agentName + "-check_disk":                  {toolregistry.CapHostDisk},
			agentName + "-check_memory":                {toolregistry.CapHostMemory},
			agentName + "-read_pg_log_file":            {toolregistry.CapLogs},
			agentName + "-get_pgbackrest_status":       {toolregistry.CapBackupStatus},
			agentName + "-restart_container":           {toolregistry.CapProcessRestart},
			agentName + "-restart_service":             {toolregistry.CapProcessRestart},
			agentName + "-run_pgbackrest_backup":       {toolregistry.CapBackupRun},
			agentName + "-restore_from_backup":         {toolregistry.CapDataRestore},
			agentName + "-restore_to_point_in_time":    {toolregistry.CapDataRestore},
			agentName + "-plan_point_in_time_recovery": {toolregistry.CapRecoveryPlan},
			agentName + "-verify_restore_recovery":     {toolregistry.CapRecoveryResult},
		},
	}
}
