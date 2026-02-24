// Package main implements the PostgreSQL database troubleshooting agent.
// It exposes psql-based tools via the A2A protocol for diagnosing database
// connectivity, performance, and configuration issues.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/google/uuid"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
	"helpdesk/prompts"
)

// infraConfig holds the loaded infrastructure configuration (if available).
// Used by tools to resolve database names to connection strings.
var infraConfig *infra.Config

func main() {
	cfg := agentutil.MustLoadConfig("localhost:1100")
	ctx := context.Background()

	// Enforce governance compliance in fix mode before any other initialization.
	agentutil.EnforceFixMode(ctx, agentutil.CheckFixModeViolations(cfg), "postgres_database_agent", cfg.AuditURL)

	// Load infrastructure config if available (enables database name resolution)
	if infraPath := os.Getenv("HELPDESK_INFRA_CONFIG"); infraPath != "" {
		var err error
		infraConfig, err = infra.Load(infraPath)
		if err != nil {
			slog.Warn("failed to load infrastructure config", "path", infraPath, "err", err)
		} else {
			slog.Info("infrastructure config loaded", "databases", len(infraConfig.DBServers))
		}
	}

	// Initialize audit store if enabled
	auditStore, err := agentutil.InitAuditStore(cfg)
	if err != nil {
		slog.Error("failed to initialize audit store", "err", err)
		os.Exit(1)
	}

	// Create trace store for propagating trace_id from incoming requests
	traceStore := &audit.CurrentTraceStore{}

	if auditStore != nil {
		defer auditStore.Close()
		// Create tool auditor with trace store for dynamic trace_id
		sessionID := "dbagent_" + uuid.New().String()[:8]
		toolAuditor = audit.NewToolAuditorWithTraceStore(auditStore, "postgres_database_agent", sessionID, traceStore)
		slog.Info("tool auditing enabled", "session_id", sessionID)
	}

	// Initialize policy engine if configured
	policyEngine, err := agentutil.InitPolicyEngine(cfg)
	if err != nil {
		slog.Error("failed to initialize policy engine", "err", err)
		os.Exit(1)
	}

	// Initialize approval client for human-in-the-loop workflows
	approvalClient := agentutil.InitApprovalClient(cfg)

	policyEnforcer = agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{
		Engine:          policyEngine,
		TraceStore:      traceStore,
		ApprovalClient:  approvalClient,
		ApprovalTimeout: cfg.ApprovalTimeout,
		AgentName:       "postgres_database_agent",
		ToolAuditor:     toolAuditor,
	})

	slog.Info("governance",
		"audit", auditStore != nil,
		"policy", policyEngine != nil,
		"approval", approvalClient != nil,
	)

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

	instruction := prompts.Database
	if infraConfig != nil {
		instruction += "\n\n## Known Infrastructure\n\n" + infraConfig.Summary()
	}

	dbAgent, err := llmagent.New(llmagent.Config{
		Name:        "postgres_database_agent",
		Description: "PostgreSQL database troubleshooting agent that can check connections, query statistics, configuration, replication status, and diagnose performance issues.",
		Instruction: instruction,
		Model:       llmModel,
		Tools:       tools,
		AfterModelCallbacks: []llmagent.AfterModelCallback{
			agentutil.NewReasoningCallback(toolAuditor),
		},
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

	if err := agentutil.ServeWithTracing(ctx, dbAgent, cfg, traceStore, cardOpts); err != nil {
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
