package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"google.golang.org/adk/tool"
)

// CommandRunner abstracts command execution for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, env []string) (string, error)
}

// execRunner is the production implementation that calls os/exec.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// cmdRunner is the active command runner. Override in tests.
var cmdRunner CommandRunner = execRunner{}

// diagnosePsqlError examines psql output for common failure patterns and returns
// a clear, actionable error message alongside the raw output.
func diagnosePsqlError(output string) string {
	out := strings.ToLower(output)

	switch {
	case strings.Contains(out, "does not exist"):
		return "The requested database does not exist on this server. " +
			"Verify the 'dbname' in the connection string is correct, or create the database first (e.g., CREATE DATABASE <name>)."

	case strings.Contains(out, "connection refused"):
		return "Connection refused. The PostgreSQL server may not be running, " +
			"or the host/port in the connection string is wrong. " +
			"Check that the server is started and listening on the expected address and port."

	case strings.Contains(out, "could not translate host name"):
		return "The hostname in the connection string could not be resolved. " +
			"Check for typos in the 'host' parameter and ensure DNS is working."

	case strings.Contains(out, "password authentication failed"):
		return "Authentication failed. The username or password is incorrect. " +
			"Verify the 'user' and 'password' in the connection string and the server's pg_hba.conf."

	case strings.Contains(out, "no pg_hba.conf entry"):
		return "Connection rejected by pg_hba.conf. The server does not allow connections " +
			"from this host/user/database combination. Update pg_hba.conf and reload the server."

	case strings.Contains(out, "timeout expired"), strings.Contains(out, "could not connect"):
		return "Connection timed out. The server may be unreachable due to network issues, " +
			"firewall rules, or an incorrect host/port."

	case strings.Contains(out, "role") && strings.Contains(out, "does not exist"):
		return "The specified user role does not exist on this server. " +
			"Verify the 'user' in the connection string or create the role first."

	case strings.Contains(out, "ssl") && (strings.Contains(out, "unsupported") || strings.Contains(out, "required")):
		return "SSL configuration mismatch. The server and client disagree on SSL requirements. " +
			"Check the 'sslmode' parameter in the connection string."

	default:
		return ""
	}
}

// runPsql executes a psql command and returns the output.
// The provided ctx controls cancellation — if it expires, psql is killed.
func runPsql(ctx context.Context, connStr string, query string) (string, error) {
	args := []string{"-c", query, "-x"}
	if connStr != "" {
		args = append([]string{connStr}, args...)
	}
	env := []string{"PGCONNECT_TIMEOUT=10"}
	output, err := cmdRunner.Run(ctx, "psql", args, env)
	if err != nil {
		out := strings.TrimSpace(output)
		if out == "" {
			out = "(no output from psql)"
		}
		slog.Error("psql command failed", "connStr", connStr, "err", err, "output", out)
		if ctx.Err() != nil {
			return "", fmt.Errorf("psql timed out or was cancelled: %v\nOutput: %s", ctx.Err(), out)
		}
		if diagnosis := diagnosePsqlError(out); diagnosis != "" {
			return "", fmt.Errorf("%s\n\nRaw error: %s", diagnosis, out)
		}
		return "", fmt.Errorf("psql failed: %v\nOutput: %s", err, out)
	}
	return output, nil
}

// PsqlResult is the standard output type for all psql tools.
type PsqlResult struct {
	Output string `json:"output"`
}

// CheckConnectionArgs defines arguments for the check_connection tool.
type CheckConnectionArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string (e.g., 'host=localhost port=5432 dbname=postgres user=postgres'). If empty, uses environment defaults."`
}

func checkConnectionTool(ctx tool.Context, args CheckConnectionArgs) (PsqlResult, error) {
	query := "SELECT version(), current_database(), current_user, inet_server_addr(), inet_server_port();"
	output, err := runPsql(ctx, args.ConnectionString, query)
	if err != nil {
		return PsqlResult{}, fmt.Errorf("connection failed: %v", err)
	}
	return PsqlResult{Output: fmt.Sprintf("Connection successful!\n%s", output)}, nil
}

// GetDatabaseInfoArgs defines arguments for the get_database_info tool.
type GetDatabaseInfoArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

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

	output, err := runPsql(ctx, args.ConnectionString, query)
	if err != nil {
		return PsqlResult{}, fmt.Errorf("error getting database info: %v", err)
	}
	return PsqlResult{Output: output}, nil
}

// GetActiveConnectionsArgs defines arguments for the get_active_connections tool.
type GetActiveConnectionsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	IncludeIdle      bool   `json:"include_idle,omitempty" jsonschema:"If true, include idle connections. Default shows only active queries."`
}

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

	output, err := runPsql(ctx, args.ConnectionString, query)
	if err != nil {
		return PsqlResult{}, fmt.Errorf("error getting active connections: %v", err)
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

	output, err := runPsql(ctx, args.ConnectionString, query)
	if err != nil {
		return PsqlResult{}, fmt.Errorf("error getting connection stats: %v", err)
	}
	return PsqlResult{Output: output}, nil
}

// GetDatabaseStatsArgs defines arguments for the get_database_stats tool.
type GetDatabaseStatsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

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

	output, err := runPsql(ctx, args.ConnectionString, query)
	if err != nil {
		return PsqlResult{}, fmt.Errorf("error getting database stats: %v", err)
	}
	return PsqlResult{Output: output}, nil
}

// GetConfigParameterArgs defines arguments for the get_config_parameter tool.
type GetConfigParameterArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	ParameterName    string `json:"parameter_name,omitempty" jsonschema:"Specific parameter name to retrieve (e.g., 'max_connections'). If empty, shows common important parameters."`
}

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

	output, err := runPsql(ctx, args.ConnectionString, query)
	if err != nil {
		return PsqlResult{}, fmt.Errorf("error getting config parameters: %v", err)
	}
	return PsqlResult{Output: output}, nil
}

// GetReplicationStatusArgs defines arguments for the get_replication_status tool.
type GetReplicationStatusArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

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

	output, err := runPsql(ctx, args.ConnectionString, query)
	if err != nil {
		return PsqlResult{}, fmt.Errorf("error getting replication status: %v", err)
	}
	return PsqlResult{Output: output}, nil
}

// GetLockInfoArgs defines arguments for the get_lock_info tool.
type GetLockInfoArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

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

	output, err := runPsql(ctx, args.ConnectionString, query)
	if err != nil {
		return PsqlResult{}, fmt.Errorf("error getting lock info: %v", err)
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

	output, err := runPsql(ctx, args.ConnectionString, query)
	if err != nil {
		return PsqlResult{}, fmt.Errorf("error getting table stats: %v", err)
	}
	return PsqlResult{Output: output}, nil
}
