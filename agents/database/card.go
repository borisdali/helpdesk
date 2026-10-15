package main

import (
	"github.com/a2aproject/a2a-go/a2a"
	"google.golang.org/adk/tool"

	"helpdesk/agentutil"
	"helpdesk/internal/buildinfo"
	"helpdesk/internal/toolregistry"
)

// cardOptions returns the agent card metadata for the database agent's tools.
// Every tool must declare at least one capability; tools_reference_test.go
// enforces this and keeps docs/tools/database.md in sync.
func cardOptions(tools []tool.Tool) agentutil.CardOptions {
	return agentutil.CardOptions{
		Version:  buildinfo.Version,
		Provider: &a2a.AgentProvider{Org: "Helpdesk"},
		SkillTags: map[string][]string{
			"postgres_database_agent":                            {"postgresql", "database", "diagnostics"},
			"postgres_database_agent-check_connection":           {"postgresql", "connectivity"},
			"postgres_database_agent-get_database_info":          {"postgresql", "metadata"},
			"postgres_database_agent-get_active_connections":     {"postgresql", "performance", "connections"},
			"postgres_database_agent-get_connection_stats":       {"postgresql", "performance", "connections"},
			"postgres_database_agent-get_database_stats":         {"postgresql", "performance", "statistics"},
			"postgres_database_agent-get_status_summary":         {"postgresql", "status", "fleet"},
			"postgres_database_agent-get_config_parameter":       {"postgresql", "configuration"},
			"postgres_database_agent-get_replication_status":     {"postgresql", "replication", "ha"},
			"postgres_database_agent-get_lock_info":              {"postgresql", "locks", "contention"},
			"postgres_database_agent-get_table_stats":            {"postgresql", "tables", "performance"},
			"postgres_database_agent-get_session_info":           {"postgresql", "connections", "inspection"},
			"postgres_database_agent-cancel_query":               {"postgresql", "connections", "remediation"},
			"postgres_database_agent-terminate_connection":       {"postgresql", "connections", "remediation"},
			"postgres_database_agent-terminate_idle_connections": {"postgresql", "connections", "remediation"},
			"postgres_database_agent-reset_cache_stats":          {"postgresql", "performance", "remediation"},
		},
		SkillExamples: map[string][]string{
			"postgres_database_agent-check_connection":       {"Check if the production database is reachable"},
			"postgres_database_agent-get_session_info":       {"How much uncommitted work does the session with pid 38553 have?", "Is it safe to terminate PID 12345?"},
			"postgres_database_agent-get_active_connections": {"Show me all long-running queries"},
			"postgres_database_agent-get_lock_info":          {"Are there any blocking locks on the database?"},
			"postgres_database_agent-get_replication_status": {"What is the replication lag?"},
		},
		SkillFleetEligible: map[string]bool{
			"postgres_database_agent-get_status_summary": true,
		},
		SkillCapabilities: map[string][]string{
			"postgres_database_agent-get_baseline": {
				toolregistry.CapUptime,
				toolregistry.CapVersion,
				toolregistry.CapConfig,
				toolregistry.CapDiskUsage,
				toolregistry.CapExtensions,
			},
			"postgres_database_agent-get_status_summary": {
				toolregistry.CapUptime,
				toolregistry.CapVersion,
				toolregistry.CapConnectionCount,
				toolregistry.CapCacheHitRatio,
			},
			"postgres_database_agent-get_server_info":            {toolregistry.CapUptime, toolregistry.CapVersion},
			"postgres_database_agent-get_connection_stats":       {toolregistry.CapConnectionCount},
			"postgres_database_agent-check_connection":           {toolregistry.CapConnectivity, toolregistry.CapVersion},
			"postgres_database_agent-get_replication_status":     {toolregistry.CapReplication},
			"postgres_database_agent-get_lock_info":              {toolregistry.CapLockInfo},
			"postgres_database_agent-get_blocking_queries":       {toolregistry.CapLockInfo},
			"postgres_database_agent-get_table_stats":            {toolregistry.CapTableStats, toolregistry.CapVacuumStatus},
			"postgres_database_agent-get_database_info":          {toolregistry.CapDatabaseList},
			"postgres_database_agent-get_database_stats":         {toolregistry.CapDatabaseStats, toolregistry.CapCacheHitRatio},
			"postgres_database_agent-get_config_parameter":       {toolregistry.CapConfig},
			"postgres_database_agent-get_pg_settings":            {toolregistry.CapConfig},
			"postgres_database_agent-get_extensions":             {toolregistry.CapExtensions},
			"postgres_database_agent-get_disk_usage":             {toolregistry.CapDiskUsage},
			"postgres_database_agent-get_active_connections":     {toolregistry.CapActiveQueries},
			"postgres_database_agent-get_session_info":           {toolregistry.CapSessionInspect},
			"postgres_database_agent-get_backup_status":          {toolregistry.CapWALArchiving},
			"postgres_database_agent-get_slow_queries":           {toolregistry.CapQueryStats},
			"postgres_database_agent-explain_query":              {toolregistry.CapQueryPlan},
			"postgres_database_agent-get_vacuum_status":          {toolregistry.CapVacuumStatus},
			"postgres_database_agent-get_bgwriter_stats":         {toolregistry.CapCheckpointStats},
			"postgres_database_agent-get_wait_events":            {toolregistry.CapWaitEvents},
			"postgres_database_agent-read_pg_log":                {toolregistry.CapLogs},
			"postgres_database_agent-read_uploaded_file":         {toolregistry.CapLogs},
			"postgres_database_agent-get_saved_snapshots":        {toolregistry.CapSnapshotHistory},
			"postgres_database_agent-cancel_query":               {toolregistry.CapQueryCancel},
			"postgres_database_agent-terminate_connection":       {toolregistry.CapConnectionTerminate},
			"postgres_database_agent-terminate_idle_connections": {toolregistry.CapConnectionTerminate},
			"postgres_database_agent-resume_wal_replay":          {toolregistry.CapReplicationChange},
			"postgres_database_agent-drop_replication_slot":      {toolregistry.CapReplicationChange},
			"postgres_database_agent-run_vacuum":                 {toolregistry.CapVacuumRun},
			"postgres_database_agent-reset_pg_setting":           {toolregistry.CapConfigChange},
			"postgres_database_agent-set_archive_command":        {toolregistry.CapConfigChange},
			"postgres_database_agent-reset_cache_stats":          {toolregistry.CapStatsReset},
		},
		SkillSupersedes: map[string][]string{
			"postgres_database_agent-get_baseline": {
				"get_server_info",
				"get_pg_settings",
				"get_extensions",
				"get_disk_usage",
			},
			"postgres_database_agent-get_status_summary": {
				"get_server_info",
				"get_connection_stats",
			},
		},
		SkillSchemaHash: agentutil.ComputeSchemaFingerprints("postgres_database_agent", tools),
		ToolSchemas:     agentutil.ComputeInputSchemas(tools),
	}
}
