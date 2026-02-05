// Package main implements the PostgreSQL database troubleshooting agent.
// It exposes psql-based tools via the A2A protocol for diagnosing database
// connectivity, performance, and configuration issues.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/a2aproject/a2a-go/a2a"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"helpdesk/agentutil"
	"helpdesk/prompts"
)

func main() {
	cfg := agentutil.MustLoadConfig("localhost:1100")
	ctx := context.Background()

	llmModel, err := agentutil.NewLLM(ctx, cfg)
	if err != nil {
		slog.Error("failed to create LLM model", "err", err)
		os.Exit(1)
	}

	// Create psql tools
	tools, err := createTools()
	if err != nil {
		slog.Error("failed to create tools", "err", err)
		os.Exit(1)
	}

	dbAgent, err := llmagent.New(llmagent.Config{
		Name:        "postgres_database_agent",
		Description: "PostgreSQL database troubleshooting agent that can check connections, query statistics, configuration, replication status, and diagnose performance issues.",
		Instruction: prompts.Database,
		Model:       llmModel,
		Tools:       tools,
	})
	if err != nil {
		slog.Error("failed to create database agent", "err", err)
		os.Exit(1)
	}

	cardOpts := agentutil.CardOptions{
		Version:  "1.0.0",
		Provider: &a2a.AgentProvider{Org: "Helpdesk"},
		SkillTags: map[string][]string{
			"postgres_database_agent":                          {"postgresql", "database", "diagnostics"},
			"postgres_database_agent-check_connection":         {"postgresql", "connectivity"},
			"postgres_database_agent-get_database_info":        {"postgresql", "metadata"},
			"postgres_database_agent-get_active_connections":   {"postgresql", "performance", "connections"},
			"postgres_database_agent-get_connection_stats":     {"postgresql", "performance", "connections"},
			"postgres_database_agent-get_database_stats":       {"postgresql", "performance", "statistics"},
			"postgres_database_agent-get_config_parameter":     {"postgresql", "configuration"},
			"postgres_database_agent-get_replication_status":   {"postgresql", "replication", "ha"},
			"postgres_database_agent-get_lock_info":            {"postgresql", "locks", "contention"},
			"postgres_database_agent-get_table_stats":          {"postgresql", "tables", "performance"},
		},
		SkillExamples: map[string][]string{
			"postgres_database_agent-check_connection":       {"Check if the production database is reachable"},
			"postgres_database_agent-get_active_connections": {"Show me all long-running queries"},
			"postgres_database_agent-get_lock_info":          {"Are there any blocking locks on the database?"},
			"postgres_database_agent-get_replication_status": {"What is the replication lag?"},
		},
	}

	if err := agentutil.Serve(ctx, dbAgent, cfg, cardOpts); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func createTools() ([]tool.Tool, error) {
	checkConnectionToolDef, err := functiontool.New(functiontool.Config{
		Name:        "check_connection",
		Description: "Test database connectivity and get basic server information including version, current database, user, and server address.",
	}, checkConnectionTool)
	if err != nil {
		return nil, err
	}

	getServerInfoToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_server_info",
		Description: "Get PostgreSQL server information including uptime, start time, version, data directory, role (primary/replica), and connection counts.",
	}, getServerInfoTool)
	if err != nil {
		return nil, err
	}

	getDatabaseInfoToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_database_info",
		Description: "List all databases with their sizes, owners, encoding, and whether the server is in recovery mode.",
	}, getDatabaseInfoTool)
	if err != nil {
		return nil, err
	}

	getActiveConnectionsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_active_connections",
		Description: "Show active database connections and running queries from pg_stat_activity. Useful for finding long-running queries.",
	}, getActiveConnectionsTool)
	if err != nil {
		return nil, err
	}

	getConnectionStatsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_connection_stats",
		Description: "Get connection statistics summary: total connections, active, idle, waiting on locks per database.",
	}, getConnectionStatsTool)
	if err != nil {
		return nil, err
	}

	getDatabaseStatsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_database_stats",
		Description: "Get database-level statistics including commits, rollbacks, cache hit ratio, row operations, conflicts, and deadlocks.",
	}, getDatabaseStatsTool)
	if err != nil {
		return nil, err
	}

	getConfigParameterToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_config_parameter",
		Description: "Get PostgreSQL configuration parameters. Can search for specific parameter or show common important settings.",
	}, getConfigParameterTool)
	if err != nil {
		return nil, err
	}

	getReplicationStatusToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_replication_status",
		Description: "Get replication status: primary/replica role, replication slots, and lag information.",
	}, getReplicationStatusTool)
	if err != nil {
		return nil, err
	}

	getLockInfoToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_lock_info",
		Description: "Find blocking locks and which queries are waiting on which other queries.",
	}, getLockInfoTool)
	if err != nil {
		return nil, err
	}

	getTableStatsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_table_stats",
		Description: "Get table-level statistics: size, row counts, dead tuples, vacuum times, and scan types.",
	}, getTableStatsTool)
	if err != nil {
		return nil, err
	}

	return []tool.Tool{
		checkConnectionToolDef,
		getServerInfoToolDef,
		getDatabaseInfoToolDef,
		getActiveConnectionsToolDef,
		getConnectionStatsToolDef,
		getDatabaseStatsToolDef,
		getConfigParameterToolDef,
		getReplicationStatusToolDef,
		getLockInfoToolDef,
		getTableStatsToolDef,
	}, nil
}
