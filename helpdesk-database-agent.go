// Package main implements a PostgreSQL database troubleshooting agent for the helpdesk system.
// It exposes psql-based tools via the A2A protocol for diagnosing database
// connectivity, performance, and configuration issues.

package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"
	"google.golang.org/genai"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/server/adka2a"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const databaseAgentInstruction = `You are a PostgreSQL database troubleshooting expert. You help diagnose issues with
PostgreSQL databases and their derivatives (like AlloyDB Omni).

When investigating database issues:
1. First check if the database is reachable (check_connection)
2. Get basic database information (get_database_info)
3. Check active connections and running queries (get_active_connections)
4. Look at database statistics (get_database_stats)
5. Check for configuration issues (get_config_parameter)
6. Review replication status if applicable (get_replication_status)

For connection issues, check:
- Is the database accepting connections?
- Are there too many active connections?
- Is the database in recovery mode?

For performance issues, examine:
- Long-running queries in pg_stat_activity
- Lock contention
- Table and index statistics

Always explain your findings clearly and suggest actionable next steps.`

// runPsql executes a psql command and returns the output.
// Connection parameters are taken from environment or provided in args.
func runPsql(connStr string, query string) (string, error) {
	args := []string{"-c", query, "-x"}
	if connStr != "" {
		args = append([]string{connStr}, args...)
	}
	cmd := exec.Command("psql", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("psql error: %v, output: %s", err, string(output))
	}
	return string(output), nil
}

// PsqlResult is the standard output type for all psql tools.
type PsqlResult struct {
	Output string `json:"output"`
}

// CheckConnectionArgs defines arguments for the check_connection tool.
type CheckConnectionArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string (e.g., 'host=localhost port=5432 dbname=postgres user=postgres'). If empty, uses environment defaults."`
}

// checkConnectionTool tests database connectivity.
func checkConnectionTool(ctx tool.Context, args CheckConnectionArgs) (PsqlResult, error) {
	query := "SELECT version(), current_database(), current_user, inet_server_addr(), inet_server_port();"
	output, err := runPsql(args.ConnectionString, query)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("Connection failed: %v", err)}, nil
	}
	return PsqlResult{Output: fmt.Sprintf("Connection successful!\n%s", output)}, nil
}

// GetDatabaseInfoArgs defines arguments for the get_database_info tool.
type GetDatabaseInfoArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

// getDatabaseInfoTool retrieves basic database information.
func getDatabaseInfoTool(ctx tool.Context, args GetDatabaseInfoArgs) (PsqlResult, error) {
	query := `SELECT
		d.datname as database,
		pg_size_pretty(pg_database_size(d.datname)) as size,
		pg_catalog.pg_get_userbyid(d.datdba) as owner,
		pg_catalog.pg_encoding_to_char(d.encoding) as encoding,
		d.datcollate as collation,
		d.datconnlimit as connection_limit,
		CASE WHEN pg_is_in_recovery() THEN 'Yes' ELSE 'No' END as in_recovery
	FROM pg_database d
	WHERE d.datistemplate = false
	ORDER BY pg_database_size(d.datname) DESC;`

	output, err := runPsql(args.ConnectionString, query)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("Error getting database info: %v", err)}, nil
	}
	return PsqlResult{Output: output}, nil
}

// GetActiveConnectionsArgs defines arguments for the get_active_connections tool.
type GetActiveConnectionsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	IncludeIdle      bool   `json:"include_idle,omitempty" jsonschema:"If true, include idle connections. Default shows only active queries."`
}

// getActiveConnectionsTool retrieves active database connections and queries.
func getActiveConnectionsTool(ctx tool.Context, args GetActiveConnectionsArgs) (PsqlResult, error) {
	stateFilter := "AND state != 'idle'"
	if args.IncludeIdle {
		stateFilter = ""
	}

	query := fmt.Sprintf(`SELECT
		pid,
		usename as user,
		datname as database,
		client_addr,
		state,
		wait_event_type,
		wait_event,
		EXTRACT(EPOCH FROM (now() - query_start))::int as query_seconds,
		LEFT(query, 100) as query_preview
	FROM pg_stat_activity
	WHERE pid != pg_backend_pid()
	%s
	ORDER BY query_start ASC NULLS LAST
	LIMIT 50;`, stateFilter)

	output, err := runPsql(args.ConnectionString, query)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("Error getting active connections: %v", err)}, nil
	}
	if strings.TrimSpace(output) == "" || strings.Contains(output, "(0 rows)") {
		return PsqlResult{Output: "No active connections found."}, nil
	}
	return PsqlResult{Output: output}, nil
}

// GetConnectionStatsArgs defines arguments for the get_connection_stats tool.
type GetConnectionStatsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

// getConnectionStatsTool retrieves connection statistics summary.
func getConnectionStatsTool(ctx tool.Context, args GetConnectionStatsArgs) (PsqlResult, error) {
	query := `SELECT
		datname as database,
		COUNT(*) as total_connections,
		COUNT(*) FILTER (WHERE state = 'active') as active,
		COUNT(*) FILTER (WHERE state = 'idle') as idle,
		COUNT(*) FILTER (WHERE state = 'idle in transaction') as idle_in_transaction,
		COUNT(*) FILTER (WHERE wait_event_type = 'Lock') as waiting_on_lock,
		(SELECT setting::int FROM pg_settings WHERE name = 'max_connections') as max_connections
	FROM pg_stat_activity
	GROUP BY datname
	ORDER BY total_connections DESC;`

	output, err := runPsql(args.ConnectionString, query)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("Error getting connection stats: %v", err)}, nil
	}
	return PsqlResult{Output: output}, nil
}

// GetDatabaseStatsArgs defines arguments for the get_database_stats tool.
type GetDatabaseStatsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

// getDatabaseStatsTool retrieves database-level statistics.
func getDatabaseStatsTool(ctx tool.Context, args GetDatabaseStatsArgs) (PsqlResult, error) {
	query := `SELECT
		datname as database,
		numbackends as connections,
		xact_commit as commits,
		xact_rollback as rollbacks,
		blks_read as blocks_read,
		blks_hit as cache_hits,
		ROUND(100.0 * blks_hit / NULLIF(blks_read + blks_hit, 0), 2) as cache_hit_ratio,
		tup_returned as rows_returned,
		tup_fetched as rows_fetched,
		tup_inserted as rows_inserted,
		tup_updated as rows_updated,
		tup_deleted as rows_deleted,
		conflicts,
		deadlocks
	FROM pg_stat_database
	WHERE datname NOT LIKE 'template%'
	ORDER BY numbackends DESC;`

	output, err := runPsql(args.ConnectionString, query)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("Error getting database stats: %v", err)}, nil
	}
	return PsqlResult{Output: output}, nil
}

// GetConfigParameterArgs defines arguments for the get_config_parameter tool.
type GetConfigParameterArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	ParameterName    string `json:"parameter_name,omitempty" jsonschema:"Specific parameter name to retrieve (e.g., 'max_connections'). If empty, shows common important parameters."`
}

// getConfigParameterTool retrieves PostgreSQL configuration parameters.
func getConfigParameterTool(ctx tool.Context, args GetConfigParameterArgs) (PsqlResult, error) {
	var query string
	if args.ParameterName != "" {
		query = fmt.Sprintf(`SELECT name, setting, unit, short_desc
			FROM pg_settings
			WHERE name ILIKE '%%%s%%'
			ORDER BY name;`, args.ParameterName)
	} else {
		query = `SELECT name, setting, unit, short_desc
			FROM pg_settings
			WHERE name IN (
				'max_connections', 'shared_buffers', 'effective_cache_size',
				'work_mem', 'maintenance_work_mem', 'wal_level',
				'max_wal_senders', 'max_replication_slots', 'hot_standby',
				'listen_addresses', 'port', 'log_min_duration_statement',
				'statement_timeout', 'lock_timeout', 'idle_in_transaction_session_timeout'
			)
			ORDER BY name;`
	}

	output, err := runPsql(args.ConnectionString, query)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("Error getting config parameters: %v", err)}, nil
	}
	return PsqlResult{Output: output}, nil
}

// GetReplicationStatusArgs defines arguments for the get_replication_status tool.
type GetReplicationStatusArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

// getReplicationStatusTool retrieves replication status information.
func getReplicationStatusTool(ctx tool.Context, args GetReplicationStatusArgs) (PsqlResult, error) {
	query := `SELECT
		CASE WHEN pg_is_in_recovery() THEN 'Replica' ELSE 'Primary' END as role,
		pg_is_in_recovery() as is_in_recovery;

	SELECT
		client_addr,
		usename as user,
		application_name,
		state,
		sync_state,
		pg_wal_lsn_diff(sent_lsn, write_lsn) as write_lag_bytes,
		pg_wal_lsn_diff(sent_lsn, flush_lsn) as flush_lag_bytes,
		pg_wal_lsn_diff(sent_lsn, replay_lsn) as replay_lag_bytes
	FROM pg_stat_replication;

	SELECT
		slot_name,
		slot_type,
		active,
		pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn) as lag_bytes
	FROM pg_replication_slots;`

	output, err := runPsql(args.ConnectionString, query)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("Error getting replication status: %v", err)}, nil
	}
	return PsqlResult{Output: output}, nil
}

// GetLockInfoArgs defines arguments for the get_lock_info tool.
type GetLockInfoArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

// getLockInfoTool retrieves information about locks and blocking queries.
func getLockInfoTool(ctx tool.Context, args GetLockInfoArgs) (PsqlResult, error) {
	query := `SELECT
		blocked_locks.pid AS blocked_pid,
		blocked_activity.usename AS blocked_user,
		blocking_locks.pid AS blocking_pid,
		blocking_activity.usename AS blocking_user,
		blocked_activity.query AS blocked_query,
		blocking_activity.query AS blocking_query
	FROM pg_catalog.pg_locks blocked_locks
	JOIN pg_catalog.pg_stat_activity blocked_activity ON blocked_activity.pid = blocked_locks.pid
	JOIN pg_catalog.pg_locks blocking_locks
		ON blocking_locks.locktype = blocked_locks.locktype
		AND blocking_locks.database IS NOT DISTINCT FROM blocked_locks.database
		AND blocking_locks.relation IS NOT DISTINCT FROM blocked_locks.relation
		AND blocking_locks.page IS NOT DISTINCT FROM blocked_locks.page
		AND blocking_locks.tuple IS NOT DISTINCT FROM blocked_locks.tuple
		AND blocking_locks.virtualxid IS NOT DISTINCT FROM blocked_locks.virtualxid
		AND blocking_locks.transactionid IS NOT DISTINCT FROM blocked_locks.transactionid
		AND blocking_locks.classid IS NOT DISTINCT FROM blocked_locks.classid
		AND blocking_locks.objid IS NOT DISTINCT FROM blocked_locks.objid
		AND blocking_locks.objsubid IS NOT DISTINCT FROM blocked_locks.objsubid
		AND blocking_locks.pid != blocked_locks.pid
	JOIN pg_catalog.pg_stat_activity blocking_activity ON blocking_activity.pid = blocking_locks.pid
	WHERE NOT blocked_locks.granted;`

	output, err := runPsql(args.ConnectionString, query)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("Error getting lock info: %v", err)}, nil
	}
	if strings.TrimSpace(output) == "" || strings.Contains(output, "(0 rows)") {
		return PsqlResult{Output: "No blocking locks found."}, nil
	}
	return PsqlResult{Output: output}, nil
}

// GetTableStatsArgs defines arguments for the get_table_stats tool.
type GetTableStatsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	SchemaName       string `json:"schema_name,omitempty" jsonschema:"Schema name to filter tables. Default is 'public'."`
	TableName        string `json:"table_name,omitempty" jsonschema:"Specific table name to get stats for."`
}

// getTableStatsTool retrieves table-level statistics.
func getTableStatsTool(ctx tool.Context, args GetTableStatsArgs) (PsqlResult, error) {
	schemaFilter := "public"
	if args.SchemaName != "" {
		schemaFilter = args.SchemaName
	}

	var query string
	if args.TableName != "" {
		query = fmt.Sprintf(`SELECT
			schemaname,
			relname as table_name,
			pg_size_pretty(pg_total_relation_size(relid)) as total_size,
			n_live_tup as live_rows,
			n_dead_tup as dead_rows,
			ROUND(100.0 * n_dead_tup / NULLIF(n_live_tup + n_dead_tup, 0), 2) as dead_ratio,
			last_vacuum,
			last_autovacuum,
			last_analyze,
			last_autoanalyze,
			seq_scan,
			idx_scan
		FROM pg_stat_user_tables
		WHERE schemaname = '%s' AND relname = '%s';`, schemaFilter, args.TableName)
	} else {
		query = fmt.Sprintf(`SELECT
			relname as table_name,
			pg_size_pretty(pg_total_relation_size(relid)) as total_size,
			n_live_tup as live_rows,
			n_dead_tup as dead_rows,
			seq_scan,
			idx_scan
		FROM pg_stat_user_tables
		WHERE schemaname = '%s'
		ORDER BY pg_total_relation_size(relid) DESC
		LIMIT 20;`, schemaFilter)
	}

	output, err := runPsql(args.ConnectionString, query)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("Error getting table stats: %v", err)}, nil
	}
	return PsqlResult{Output: output}, nil
}

// newDatabaseAgent creates the PostgreSQL troubleshooting agent with psql tools.
func newDatabaseAgent(ctx context.Context, modelVendor, modelName, apiKey string) (agent.Agent, error) {
	// Create psql tools
	checkConnectionToolDef, err := functiontool.New(functiontool.Config{
		Name:        "check_connection",
		Description: "Test database connectivity and get basic server information including version, current database, user, and server address.",
	}, checkConnectionTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create check_connection tool: %v", err)
	}

	getDatabaseInfoToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_database_info",
		Description: "List all databases with their sizes, owners, encoding, and whether the server is in recovery mode.",
	}, getDatabaseInfoTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_database_info tool: %v", err)
	}

	getActiveConnectionsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_active_connections",
		Description: "Show active database connections and running queries from pg_stat_activity. Useful for finding long-running queries.",
	}, getActiveConnectionsTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_active_connections tool: %v", err)
	}

	getConnectionStatsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_connection_stats",
		Description: "Get connection statistics summary: total connections, active, idle, waiting on locks per database.",
	}, getConnectionStatsTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_connection_stats tool: %v", err)
	}

	getDatabaseStatsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_database_stats",
		Description: "Get database-level statistics including commits, rollbacks, cache hit ratio, row operations, conflicts, and deadlocks.",
	}, getDatabaseStatsTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_database_stats tool: %v", err)
	}

	getConfigParameterToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_config_parameter",
		Description: "Get PostgreSQL configuration parameters. Can search for specific parameter or show common important settings.",
	}, getConfigParameterTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_config_parameter tool: %v", err)
	}

	getReplicationStatusToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_replication_status",
		Description: "Get replication status: primary/replica role, replication slots, and lag information.",
	}, getReplicationStatusTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_replication_status tool: %v", err)
	}

	getLockInfoToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_lock_info",
		Description: "Find blocking locks and which queries are waiting on which other queries.",
	}, getLockInfoTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_lock_info tool: %v", err)
	}

	getTableStatsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_table_stats",
		Description: "Get table-level statistics: size, row counts, dead tuples, vacuum times, and scan types.",
	}, getTableStatsTool)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_table_stats tool: %v", err)
	}

	// Create the LLM model based on vendor
	var llmModel model.LLM
	switch strings.ToLower(modelVendor) {
	case "google", "gemini":
		llmModel, err = gemini.NewModel(ctx, modelName, &genai.ClientConfig{APIKey: apiKey})
		if err != nil {
			return nil, fmt.Errorf("failed to create Gemini model: %v", err)
		}
		log.Printf("Using Google/Gemini model: %s", modelName)
	case "anthropic":
		llmModel, err = NewAnthropicModel(ctx, modelName, apiKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create Anthropic model: %v", err)
		}
		log.Printf("Using Anthropic model: %s", modelName)
	default:
		return nil, fmt.Errorf("unknown LLM model vendor: %s (supported: google, gemini, anthropic)", modelVendor)
	}

	// Create the database agent with all tools
	dbAgent, err := llmagent.New(llmagent.Config{
		Name:        "postgres_database_agent",
		Description: "PostgreSQL database troubleshooting agent that can check connections, query statistics, configuration, replication status, and diagnose performance issues.",
		Instruction: databaseAgentInstruction,
		Model:       llmModel,
		Tools: []tool.Tool{
			checkConnectionToolDef,
			getDatabaseInfoToolDef,
			getActiveConnectionsToolDef,
			getConnectionStatsToolDef,
			getDatabaseStatsToolDef,
			getConfigParameterToolDef,
			getReplicationStatusToolDef,
			getLockInfoToolDef,
			getTableStatsToolDef,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create database agent: %v", err)
	}

	return dbAgent, nil
}

// startDatabaseAgentServer starts an HTTP server exposing the database-agent via A2A protocol.
func startDatabaseAgentServer(ctx context.Context, listenAddr, modelVendor, modelName, apiKey string) (string, error) {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return "", fmt.Errorf("failed to bind to port: %v", err)
	}

	baseURL := &url.URL{Scheme: "http", Host: listener.Addr().String()}

	log.Printf("Starting Database A2A server on %s", baseURL.String())

	dbAgent, err := newDatabaseAgent(ctx, modelVendor, modelName, apiKey)
	if err != nil {
		return "", fmt.Errorf("failed to create database agent: %v", err)
	}

	agentPath := "/invoke"
	agentCard := &a2a.AgentCard{
		Name:               dbAgent.Name(),
		Description:        "PostgreSQL database troubleshooting agent with psql tools for diagnosing connectivity, performance, and configuration issues.",
		Skills:             adka2a.BuildAgentSkills(dbAgent),
		PreferredTransport: a2a.TransportProtocolJSONRPC,
		URL:                baseURL.JoinPath(agentPath).String(),
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
	}

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(agentCard))

	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        dbAgent.Name(),
			Agent:          dbAgent,
			SessionService: session.InMemoryService(),
		},
	})
	requestHandler := a2asrv.NewHandler(executor)
	mux.Handle(agentPath, a2asrv.NewJSONRPCHandler(requestHandler))

	log.Printf("Agent card available at: %s/.well-known/agent-card.json", baseURL.String())

	err = http.Serve(listener, mux)

	log.Printf("Database A2A server stopped: %v", err)
	return baseURL.String(), nil
}

func main() {
	ctx := context.Background()
	modelVendor := os.Getenv("HELPDESK_MODEL_VENDOR")
	modelName := os.Getenv("HELPDESK_MODEL_NAME")
	apiKey := os.Getenv("HELPDESK_API_KEY")
	if modelVendor == "" || modelName == "" || apiKey == "" {
		log.Fatalf("Please set the HELPDESK_MODEL_VENDOR (e.g. Google/Gemini, Anthropic, etc.), HELPDESK_MODEL_NAME and HELPDESK_API_KEY env variables.")
	}

	// Listen address: defaults to localhost:1100
	listenAddr := os.Getenv("HELPDESK_AGENT_ADDR")
	if listenAddr == "" {
		listenAddr = "localhost:1100"
	}

	serverURL, err := startDatabaseAgentServer(ctx, listenAddr, modelVendor, modelName, apiKey)
	if err != nil {
		log.Fatalf("Failed to start Database A2A server: %v", err)
	}
	log.Printf("Database A2A server started on URL: %s", serverURL)
}
