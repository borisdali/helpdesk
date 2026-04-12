package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/adk/tool"

	"helpdesk/agentutil"
	"helpdesk/agentutil/retryutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/policy"
)

// toolAuditor is set during initialization if auditing is enabled.
var toolAuditor *audit.ToolAuditor

// policyEnforcer is set during initialization for policy enforcement.
var policyEnforcer *agentutil.PolicyEnforcer

// auditBaseURL and auditAPIKey are set during initialization and used by
// read_uploaded_file to retrieve operator-uploaded files from auditd.
var auditBaseURL string
var auditAPIKey string

// currentTraceStore is set during initialization so snapshot tools can include
// the current trace ID when persisting results to the ToolResultStore.
var currentTraceStore *audit.CurrentTraceStore

// verifyRetryConfig controls the re-check retry loop for Level 2 post-mutation
// verification (cancel_query). Overridable in tests to use zero delays.
var verifyRetryConfig = retryutil.Default

// verifyTerminateConfig controls the re-check loop for terminate_connection
// Level 2. Uses a longer initial delay (5 s) because SIGTERM needs time to
// propagate before pg_stat_activity reflects the backend as gone.
// Overridable in tests to use zero delays.
var verifyTerminateConfig = retryutil.Config{
	MaxAttempts:  2,
	InitialDelay: 5 * time.Second,
	MaxDelay:     30 * time.Second,
}

// databaseInfo holds resolved database information for policy checks.
type databaseInfo struct {
	Name              string
	ConnectionStr     string
	Tags              []string
	Sensitivity       []string
	IsFromInfraConfig bool
}

// resolveDatabaseInfo resolves a connection string or database name to full info.
// Returns the resolved info and metadata for policy checks.
// When infraConfig is set and the database is not registered, returns an error
// (hard reject) so callers can fail before any tool execution.
func resolveDatabaseInfo(connStrOrName string) (databaseInfo, error) {
	connStrOrName = strings.TrimSpace(connStrOrName)

	// If it contains "=" it's already a connection string
	if strings.Contains(connStrOrName, "=") {
		// Extract dbname from connection string if present
		dbName := ""
		for _, part := range strings.Split(connStrOrName, " ") {
			if strings.HasPrefix(part, "dbname=") {
				dbName = strings.TrimPrefix(part, "dbname=")
				break
			}
		}

		// Reverse lookup: find which infraConfig entry has this connection string
		if infraConfig != nil {
			for id, db := range infraConfig.DBServers {
				slog.Debug("reverse lookup comparing", "id", id, "db_conn", db.ConnectionString, "input_conn", connStrOrName, "tags", db.Tags)
				if db.ConnectionString == connStrOrName {
					slog.Debug("reverse resolved connection string to database", "id", id, "tags", db.Tags)
					return databaseInfo{
						Name:              id,
						ConnectionStr:     db.ResolvedConnectionString(),
						Tags:              db.Tags,
						Sensitivity:       db.Sensitivity,
						IsFromInfraConfig: true,
					}, nil
				}
			}
			// infraConfig is set but connection string not registered — hard reject.
			known := make([]string, 0, len(infraConfig.DBServers))
			for id := range infraConfig.DBServers {
				known = append(known, id)
			}
			sort.Strings(known)
			return databaseInfo{}, fmt.Errorf(
				"database not registered in infrastructure config; "+
					"contact your IT administrator to add it. Known databases: %s",
				strings.Join(known, ", "))
		}

		slog.Warn("connection string not found in infraConfig; policy will evaluate with no tags",
			"connection_string", connStrOrName,
			"known_databases", 0,
		)
		return databaseInfo{
			Name:          dbName,
			ConnectionStr: connStrOrName,
			Tags:          nil, // No tags - connection string not in infraConfig
		}, nil
	}

	// If we have infrastructure config, try to look up the database name
	if infraConfig != nil {
		if db, ok := infraConfig.DBServers[connStrOrName]; ok {
			slog.Info("resolved database name to connection string", "name", connStrOrName)
			return databaseInfo{
				Name:              connStrOrName,
				ConnectionStr:     db.ResolvedConnectionString(),
				Tags:              db.Tags,
				Sensitivity:       db.Sensitivity,
				IsFromInfraConfig: true,
			}, nil
		}
		// infraConfig is set but database name not registered — hard reject.
		known := make([]string, 0, len(infraConfig.DBServers))
		for id := range infraConfig.DBServers {
			known = append(known, id)
		}
		sort.Strings(known)
		return databaseInfo{}, fmt.Errorf(
			"database %q not registered in infrastructure config; "+
				"contact your IT administrator to add it. Known databases: %s",
			connStrOrName, strings.Join(known, ", "))
	}

	// Dev mode: no infra config — return as-is
	return databaseInfo{
		Name:          connStrOrName,
		ConnectionStr: connStrOrName,
	}, nil
}

// resolveConnectionString checks if the input looks like a database name (no "=" sign)
// and attempts to resolve it using the infrastructure config. Returns an error if the
// database is not registered when infraConfig is set.
func resolveConnectionString(connStrOrName string) (string, error) {
	info, err := resolveDatabaseInfo(connStrOrName)
	if err != nil {
		return "", err
	}
	return info.ConnectionStr, nil
}

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
// If connStr looks like a database name (no "=" sign), it will be resolved
// using the infrastructure config.
func runPsql(ctx context.Context, connStr string, query string) (string, error) {
	return runPsqlWithToolName(ctx, connStr, query, "")
}

// runPsqlWithToolName executes a psql command with auditing and policy enforcement.
// toolName is used for audit logging; if empty, a generic name is used.
// All callers that don't specify an action are read-only.
func runPsqlWithToolName(ctx context.Context, connStr string, query string, toolName string) (string, error) {
	return runPsqlAs(ctx, connStr, query, toolName, policy.ActionRead, "")
}

// runPsqlAs executes a psql command with explicit action class for policy enforcement.
// sessionPlan is optional free-text forwarded to the approver when approval is required
// (e.g. the output of formatConnectionPlan for terminate/cancel operations).
func runPsqlAs(ctx context.Context, connStr string, query string, toolName string, action policy.ActionClass, sessionPlan string) (string, error) {
	// Resolve database info for policy checks
	dbInfo, err := resolveDatabaseInfo(connStr)
	if err != nil {
		return "", err
	}

	// Check policy before executing
	if policyEnforcer != nil {
		note := sessionPlan
		if !dbInfo.IsFromInfraConfig {
			if note != "" {
				note += "\n\n"
			}
			note += "connection string not found in infraConfig; no tags available for policy matching"
		}
		// Carry the tool name in context for policy matching on ResourceMatch.Tool/ToolPattern.
		policyCtx := agentutil.WithToolName(ctx, toolName)
		if err := policyEnforcer.CheckDatabase(policyCtx, dbInfo.Name, action, dbInfo.Tags, note, dbInfo.Sensitivity); err != nil {
			slog.Warn("policy denied database access",
				"tool", toolName,
				"database", dbInfo.Name,
				"action", action,
				"tags", dbInfo.Tags,
				"from_infra_config", dbInfo.IsFromInfraConfig,
				"err", err)
			return "", fmt.Errorf("policy denied: %w", err)
		}
	}

	// Pre-execution blast-radius estimate for DELETE/UPDATE: run EXPLAIN to get
	// the query planner's row count before the mutation commits. Silently skipped
	// for non-DML statements or when EXPLAIN fails (post-execution check is the
	// backstop). The estimate may be imprecise — treat it as an order-of-magnitude
	// check.
	if policyEnforcer != nil && (action == policy.ActionWrite || action == policy.ActionDestructive) {
		if estRows, ok := estimateRowsAffected(ctx, dbInfo.ConnectionStr, query); ok {
			if preErr := policyEnforcer.CheckDatabaseResult(ctx, dbInfo.Name, action, dbInfo.Tags, agentutil.ToolOutcome{
				RowsAffected: estRows,
			}); preErr != nil {
				return "", fmt.Errorf("blast radius check (estimated %d rows): %w", estRows, preErr)
			}
		}
	}

	start := time.Now()
	connStr = dbInfo.ConnectionStr

	args := []string{"-w", "-c", query, "-x"}
	if connStr != "" {
		args = append([]string{connStr}, args...)
	}
	env := []string{"PGCONNECT_TIMEOUT=10"}
	output, err := cmdRunner.Run(ctx, "psql", args, env)
	duration := time.Since(start)

	// Audit the tool execution
	if toolAuditor != nil && toolName != "" {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       toolName,
			Parameters: map[string]any{"connection_string": maskPassword(connStr)},
			RawCommand: query,
		}, audit.ToolResult{
			Output: truncateForAudit(output, 500),
			Error:  errMsg,
		}, duration)
	}

	// Post-execution policy check: enforce blast-radius conditions using the
	// actual row count from the command tag (e.g. "DELETE 1500").
	if policyEnforcer != nil && err == nil {
		rowsAffected := parseRowsAffected(output)
		if postErr := policyEnforcer.CheckDatabaseResult(ctx, dbInfo.Name, action, dbInfo.Tags, agentutil.ToolOutcome{
			RowsAffected: rowsAffected,
			Err:          err,
		}); postErr != nil {
			return "", fmt.Errorf("policy denied after execution: %w", postErr)
		}
	}

	// Log successful tool execution at INFO level
	if err == nil && toolName != "" {
		slog.Info("tool ok", "name", toolName, "ms", duration.Milliseconds())
	}

	if err != nil {
		out := strings.TrimSpace(output)
		if out == "" {
			out = "(no output from psql)"
		}
		slog.Error("psql command failed", "tool", toolName, "ms", duration.Milliseconds(), "err", err, "output", out)
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

// maskPassword removes password from connection string for audit logging.
func maskPassword(connStr string) string {
	// Simple masking - replace password=xxx with password=***
	parts := strings.Split(connStr, " ")
	for i, part := range parts {
		if strings.HasPrefix(part, "password=") {
			parts[i] = "password=***"
		}
	}
	return strings.Join(parts, " ")
}

// truncateForAudit truncates a string for audit logging.
func truncateForAudit(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// parsePgFunctionResult parses the boolean result returned by pg_cancel_backend
// or pg_terminate_backend. These functions return a single-row SELECT with a
// boolean column aliased as "cancelled" or "terminated". psql always runs with
// -x (expanded mode), so each field appears as "column_name | value" on its
// own line. Returns 1 if the function succeeded (value is "t"), 0 otherwise.
func parsePgFunctionResult(output string) int {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// In expanded psql output, the boolean column appears as:
		//   cancelled       | t
		//   terminated      | t
		if (strings.HasPrefix(line, "cancelled") || strings.HasPrefix(line, "terminated")) &&
			strings.Contains(line, "|") {
			parts := strings.SplitN(line, "|", 2)
			if len(parts) == 2 && strings.TrimSpace(parts[1]) == "t" {
				return 1
			}
		}
	}
	return 0
}

// parseTerminatedCount reads the integer value from a "terminated | N" line in
// psql expanded (-x) output. Used by terminate_idle_connections to extract the count
// of connections actually terminated so blast-radius policy can be enforced.
func parseTerminatedCount(output string) int {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "terminated") && strings.Contains(line, "|") {
			parts := strings.SplitN(line, "|", 2)
			if len(parts) == 2 {
				if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

// parseRowsAffected extracts the number of rows affected from psql output.
// PostgreSQL command tags appear as standalone lines in the output:
//
//	DELETE 150
//	UPDATE 42
//	INSERT 0 5   (OID count followed by row count)
func parseRowsAffected(output string) int {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		for _, verb := range []string{"DELETE ", "UPDATE ", "INSERT "} {
			if strings.HasPrefix(line, verb) {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					// INSERT outputs "INSERT 0 N", others "VERB N"
					n, err := strconv.Atoi(parts[len(parts)-1])
					if err == nil {
						return n
					}
				}
			}
		}
	}
	return 0
}

// estimateRowsAffected runs EXPLAIN (FORMAT JSON) on a DELETE or UPDATE query
// and returns the query planner's estimated row count before the statement
// executes. Returns (0, false) if:
//   - the statement is not a DELETE or UPDATE
//   - psql returns an error (e.g. syntax error, connection failure)
//   - the EXPLAIN JSON cannot be parsed
//
// The estimate is approximate; callers should treat it as an order-of-magnitude
// check rather than an exact count. Uses -t -A flags for clean JSON output.
func estimateRowsAffected(ctx context.Context, connStr, query string) (int, bool) {
	upper := strings.ToUpper(strings.TrimSpace(query))
	if !strings.HasPrefix(upper, "DELETE") && !strings.HasPrefix(upper, "UPDATE") {
		return 0, false
	}
	args := []string{"-w", "-t", "-A", "-c", "EXPLAIN (FORMAT JSON) " + query}
	if connStr != "" {
		args = append([]string{connStr}, args...)
	}
	out, err := cmdRunner.Run(ctx, "psql", args, []string{"PGCONNECT_TIMEOUT=10"})
	if err != nil {
		return 0, false
	}
	var plans []struct {
		Plan struct {
			PlanRows int `json:"Plan Rows"`
		} `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &plans); err != nil || len(plans) == 0 {
		return 0, false
	}
	return plans[0].Plan.PlanRows, true
}

// ConnectionPlan holds the result of inspecting a database session before a
// destructive operation. It is returned by get_session_info and used as the
// pre-execution plan step inside terminate_connection and cancel_query.
type ConnectionPlan struct {
	PID               int
	User              string
	Database          string
	ClientAddr        string
	State             string // "idle", "active", "idle in transaction", ...
	StateDurationSecs int

	// Transaction state
	HasOpenTransaction bool
	OpenTxAgeSecs      int

	// Uncommitted-work signals — what would be rolled back on termination.
	// backend_xid IS NOT NULL is the definitive indicator that at least one
	// write has occurred; read-only transactions have a NULL xid and roll back
	// instantly. WAL bytes per backend are not exposed by PostgreSQL, so
	// transaction age is the primary proxy for rollback cost.
	HasWrites    bool     // false → read-only tx, rollback is instant
	TotalLocks   int      // all granted locks held by this backend
	RowLocks     int      // tuple-level (row-level) locks only
	LockedTables []string // table names with any lock held

	// Rollback time estimate (rule of thumb: 0.5× to 2× TX write duration).
	// Both fields are 0 when HasWrites is false.
	RollbackMinSecs int
	RollbackMaxSecs int

	// Context
	CurrentQuery string
}

// parseExpandedRow parses a single record from psql -x (expanded) output into
// a map of column → value. Handles multi-line values produced by long strings.
func parseExpandedRow(output string) map[string]string {
	result := make(map[string]string)
	var lastKey string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip record separator lines: "-[ RECORD 1 ]---+---"
		if trimmed == "" || strings.HasPrefix(trimmed, "-[") {
			continue
		}
		idx := strings.Index(line, "|")
		if idx < 0 {
			continue
		}
		keyPart := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if keyPart != "" {
			result[keyPart] = val
			lastKey = keyPart
		} else if lastKey != "" {
			// Continuation line for a multi-line value (psql wraps long strings).
			result[lastKey] += "\n" + val
		}
	}
	return result
}

// parseConnectionPlan builds a ConnectionPlan from the expanded psql output
// of inspectionQuery.
func parseConnectionPlan(pid int, output string) ConnectionPlan {
	row := parseExpandedRow(output)
	plan := ConnectionPlan{
		PID:        pid,
		User:       row["usename"],
		Database:   row["datname"],
		ClientAddr: row["client_addr"],
		State:      row["state"],
	}
	if v, err := strconv.Atoi(row["state_duration_secs"]); err == nil {
		plan.StateDurationSecs = v
	}
	plan.HasOpenTransaction = row["has_open_tx"] == "t"
	if v, err := strconv.Atoi(row["open_tx_secs"]); err == nil {
		plan.OpenTxAgeSecs = v
	}
	plan.HasWrites = row["has_writes"] == "t"
	if v, err := strconv.Atoi(row["total_locks"]); err == nil {
		plan.TotalLocks = v
	}
	if v, err := strconv.Atoi(row["row_locks"]); err == nil {
		plan.RowLocks = v
	}
	if tables := strings.TrimSpace(row["locked_tables"]); tables != "" {
		for _, t := range strings.Split(tables, ",") {
			if t = strings.TrimSpace(t); t != "" {
				plan.LockedTables = append(plan.LockedTables, t)
			}
		}
	}
	plan.CurrentQuery = strings.TrimSpace(row["current_query"])

	// Rollback estimate: 0.5× to 2× transaction age.
	// Only set when writes have been confirmed (backend_xid IS NOT NULL).
	if plan.HasWrites && plan.OpenTxAgeSecs > 0 {
		plan.RollbackMinSecs = max(1, plan.OpenTxAgeSecs/2)
		plan.RollbackMaxSecs = plan.OpenTxAgeSecs * 2
	}
	return plan
}

// inspectionQuery retrieves session state and uncommitted-work signals for a
// specific backend PID in a single round-trip. Lock subqueries give scope
// (which tables, how many rows) without requiring superuser privileges.
//
// Key signals:
//   - has_open_tx:  xact_start IS NOT NULL — a transaction is open
//   - has_writes:   backend_xid IS NOT NULL — at least one write occurred
//   - open_tx_secs: how long the transaction has been open (rollback proxy)
//   - locked_tables: table names helping estimate blast radius
const inspectionQuery = `SELECT
	a.pid,
	a.usename,
	COALESCE(a.datname, '')           AS datname,
	COALESCE(a.client_addr::text, 'local') AS client_addr,
	COALESCE(a.state, '')             AS state,
	COALESCE(EXTRACT(EPOCH FROM (now() - a.state_change))::int, 0) AS state_duration_secs,
	(a.xact_start IS NOT NULL)        AS has_open_tx,
	COALESCE(EXTRACT(EPOCH FROM (now() - a.xact_start))::int, 0)   AS open_tx_secs,
	(a.backend_xid IS NOT NULL)       AS has_writes,
	COALESCE((
		SELECT count(*)
		FROM pg_locks l
		WHERE l.pid = a.pid AND l.granted
	), 0) AS total_locks,
	COALESCE((
		SELECT count(*)
		FROM pg_locks l
		WHERE l.pid = a.pid AND l.granted AND l.locktype = 'tuple'
	), 0) AS row_locks,
	COALESCE((
		SELECT string_agg(DISTINCT c.relname, ', ' ORDER BY c.relname)
		FROM pg_locks l
		JOIN pg_class c ON l.relation = c.oid
		WHERE l.pid = a.pid AND l.granted AND l.relation IS NOT NULL
	), '') AS locked_tables,
	COALESCE(LEFT(a.query, 200), '')  AS current_query
FROM pg_stat_activity a
WHERE a.pid = %d;`

// inspectConnection runs a read-only inspection against a specific backend PID
// and returns a structured ConnectionPlan. It is called by getSessionInfoTool
// and as the mandatory plan step inside terminateConnectionTool and
// cancelQueryTool so every destructive action is preceded by an audit record
// of what was found.
func inspectConnection(ctx context.Context, connStr string, pid int) (ConnectionPlan, error) {
	query := fmt.Sprintf(inspectionQuery, pid)
	output, err := runPsqlWithToolName(ctx, connStr, query, "get_session_info")
	if err != nil {
		return ConnectionPlan{}, err
	}
	if strings.Contains(output, "(0 rows)") {
		return ConnectionPlan{}, fmt.Errorf("no session found with pid %d", pid)
	}
	return parseConnectionPlan(pid, output), nil
}

// formatDuration converts seconds to a human-readable duration string.
func formatDuration(secs int) string {
	if secs <= 0 {
		return "0s"
	}
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	if secs < 3600 {
		return fmt.Sprintf("%dm %ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%dh %dm", secs/3600, (secs%3600)/60)
}

// formatConnectionPlan renders a ConnectionPlan as a human-readable summary
// for presenting to a user or including in an approval request body.
func formatConnectionPlan(plan ConnectionPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Session PID %d\n", plan.PID)
	fmt.Fprintf(&b, "  User:     %s\n", plan.User)
	fmt.Fprintf(&b, "  Database: %s\n", plan.Database)
	fmt.Fprintf(&b, "  Client:   %s\n", plan.ClientAddr)
	fmt.Fprintf(&b, "  State:    %s (%s in current state)\n",
		plan.State, formatDuration(plan.StateDurationSecs))

	if !plan.HasOpenTransaction {
		fmt.Fprintf(&b, "\n  No open transaction.")
		if plan.CurrentQuery != "" {
			fmt.Fprintf(&b, "\n  Last query: %s", truncateForAudit(plan.CurrentQuery, 120))
		}
		return b.String()
	}

	fmt.Fprintf(&b, "\n  Transaction:\n")
	fmt.Fprintf(&b, "    Open TX age:   %s\n", formatDuration(plan.OpenTxAgeSecs))
	if !plan.HasWrites {
		fmt.Fprintf(&b, "    Has writes:    no  (read-only — rollback is instant)\n")
	} else {
		fmt.Fprintf(&b, "    Has writes:    yes\n")
		if len(plan.LockedTables) > 0 {
			fmt.Fprintf(&b, "    Locked tables: %s\n", strings.Join(plan.LockedTables, ", "))
		}
		if plan.RowLocks > 0 {
			fmt.Fprintf(&b, "    Row locks:     %d\n", plan.RowLocks)
		}
		if plan.TotalLocks > plan.RowLocks {
			fmt.Fprintf(&b, "    Total locks:   %d\n", plan.TotalLocks)
		}
		fmt.Fprintf(&b, "\n    Rollback estimate: ~%s to ~%s\n",
			formatDuration(plan.RollbackMinSecs),
			formatDuration(plan.RollbackMaxSecs))
	}
	if plan.CurrentQuery != "" {
		fmt.Fprintf(&b, "\n  Last query: %s\n", truncateForAudit(plan.CurrentQuery, 120))
	}
	return b.String()
}

// PsqlResult is the standard output type for all psql tools.
type PsqlResult struct {
	Output       string `json:"output"`
	VerifyStatus string `json:"verify_status,omitempty"` // "ok"|"warning"|"failed"|"escalation_required"
	RetryCount   int    `json:"retry_count,omitempty"`   // >0 when Level 2 resolved after re-checks
}

// errorResult formats an error as a PsqlResult that the LLM can see.
// We return errors as output text rather than Go errors because ADK may not
// properly relay Go errors to the orchestrator, causing empty responses.
func errorResult(toolName, connStr string, err error) PsqlResult {
	return PsqlResult{
		Output: fmt.Sprintf("---\nERROR — %s failed for %s\n\n%v\n---", toolName, connStr, err),
	}
}

// CheckConnectionArgs defines arguments for the check_connection tool.
type CheckConnectionArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string (e.g., 'host=localhost port=5432 dbname=postgres user=postgres'). If empty, uses environment defaults."`
}

func checkConnectionImpl(ctx context.Context, args CheckConnectionArgs) (PsqlResult, error) {
	query := "SELECT version(), current_database(), current_user, inet_server_addr(), inet_server_port();"
	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "check_connection")
	if err != nil {
		return errorResult("check_connection", args.ConnectionString, err), nil
	}
	return PsqlResult{Output: fmt.Sprintf("Connection successful!\n%s", output)}, nil
}

func checkConnectionTool(ctx tool.Context, args CheckConnectionArgs) (PsqlResult, error) {
	return checkConnectionImpl(ctx, args)
}

// GetServerInfoArgs defines arguments for the get_server_info tool.
type GetServerInfoArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

func getServerInfoImpl(ctx context.Context, args GetServerInfoArgs) (PsqlResult, error) {
	query := `SELECT
		version() as version,
		pg_postmaster_start_time() as server_started,
		now() - pg_postmaster_start_time() as uptime,
		current_setting('data_directory') as data_directory,
		current_setting('config_file') as config_file,
		current_setting('hba_file') as hba_file,
		current_setting('ident_file') as ident_file,
		current_setting('log_directory') as log_directory,
		current_setting('log_filename') as log_filename,
		pg_size_pretty(pg_database_size(current_database())) as current_db_size,
		CASE WHEN pg_is_in_recovery() THEN 'replica' ELSE 'primary' END as role,
		(SELECT count(*) FROM pg_stat_activity) as total_connections,
		(SELECT count(*) FROM pg_stat_activity WHERE state = 'active') as active_connections,
		current_setting('max_connections') as max_connections;`

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_server_info")
	if err != nil {
		return errorResult("get_server_info", args.ConnectionString, err), nil
	}
	return PsqlResult{Output: output}, nil
}

func getServerInfoTool(ctx tool.Context, args GetServerInfoArgs) (PsqlResult, error) {
	return getServerInfoImpl(ctx, args)
}

// GetDatabaseInfoArgs defines arguments for the get_database_info tool.
type GetDatabaseInfoArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

func getDatabaseInfoImpl(ctx context.Context, args GetDatabaseInfoArgs) (PsqlResult, error) {
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

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_database_info")
	if err != nil {
		return errorResult("get_database_info", args.ConnectionString, err), nil
	}
	return PsqlResult{Output: output}, nil
}

func getDatabaseInfoTool(ctx tool.Context, args GetDatabaseInfoArgs) (PsqlResult, error) {
	return getDatabaseInfoImpl(ctx, args)
}

// GetActiveConnectionsArgs defines arguments for the get_active_connections tool.
type GetActiveConnectionsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	ActiveOnly       bool   `json:"active_only,omitempty" jsonschema:"If true, show only connections currently executing a query (state=active). Default shows all connected sessions including idle ones."`
}

func getActiveConnectionsImpl(ctx context.Context, args GetActiveConnectionsArgs) (PsqlResult, error) {
	// Default: show all user connections including idle sessions (connected but not running a query).
	// Autovacuum workers and background processes have state IS NULL and are excluded automatically.
	stateFilter := "AND state IS NOT NULL"
	if args.ActiveOnly {
		stateFilter = "AND state = 'active'"
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

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_active_connections")
	if err != nil {
		return errorResult("get_active_connections", args.ConnectionString, err), nil
	}
	if strings.TrimSpace(output) == "" || strings.Contains(output, "(0 rows)") {
		return PsqlResult{Output: "No active connections found."}, nil
	}
	return PsqlResult{Output: output}, nil
}

func getActiveConnectionsTool(ctx tool.Context, args GetActiveConnectionsArgs) (PsqlResult, error) {
	return getActiveConnectionsImpl(ctx, args)
}

// GetConnectionStatsArgs defines arguments for the get_connection_stats tool.
type GetConnectionStatsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

func getConnectionStatsImpl(ctx context.Context, args GetConnectionStatsArgs) (PsqlResult, error) {
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

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_connection_stats")
	if err != nil {
		return errorResult("get_connection_stats", args.ConnectionString, err), nil
	}
	return PsqlResult{Output: output}, nil
}

func getConnectionStatsTool(ctx tool.Context, args GetConnectionStatsArgs) (PsqlResult, error) {
	return getConnectionStatsImpl(ctx, args)
}

// GetStatusSummaryArgs defines arguments for the get_status_summary tool.
type GetStatusSummaryArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

// getStatusSummaryImpl returns a compact JSON object with status, version, uptime,
// connection counts, and cache hit ratio — designed for fleet-wide tabular display.
// Unlike get_server_info / get_database_stats, the Output field contains JSON, not
// psql expanded text, so callers can parse individual fields programmatically.
func getStatusSummaryImpl(ctx context.Context, args GetStatusSummaryArgs) (PsqlResult, error) {
	query := `SELECT json_build_object(
		'status',          'ok',
		'version',         regexp_replace(version(), '^PostgreSQL ([0-9]+\.[0-9]+).*', 'PG \1'),
		'uptime',          extract(day  FROM (now() - pg_postmaster_start_time()))::int::text || 'd '
		                || extract(hour FROM (now() - pg_postmaster_start_time()))::int::text || 'h',
		'connections',     (SELECT count(*)::int FROM pg_stat_activity),
		'max_connections', current_setting('max_connections')::int,
		'cache_hit_ratio', COALESCE(
		                     (SELECT ROUND(100.0 * sum(blks_hit) / NULLIF(sum(blks_read + blks_hit), 0), 2)
		                      FROM pg_stat_database WHERE datname NOT LIKE 'template%'),
		                     0)
	) AS status_summary`

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_status_summary")
	if err != nil {
		return PsqlResult{Output: `{"status":"error"}`}, nil
	}

	// Extract the JSON value from the psql -x expanded output.
	row := parseExpandedRow(output)
	jsonStr, ok := row["status_summary"]
	if !ok || jsonStr == "" {
		return PsqlResult{Output: `{"status":"error"}`}, nil
	}
	return PsqlResult{Output: jsonStr}, nil
}

func getStatusSummaryTool(ctx tool.Context, args GetStatusSummaryArgs) (PsqlResult, error) {
	return getStatusSummaryImpl(ctx, args)
}

// GetDatabaseStatsArgs defines arguments for the get_database_stats tool.
type GetDatabaseStatsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

func getDatabaseStatsImpl(ctx context.Context, args GetDatabaseStatsArgs) (PsqlResult, error) {
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

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_database_stats")
	if err != nil {
		return errorResult("get_database_stats", args.ConnectionString, err), nil
	}
	return PsqlResult{Output: output}, nil
}

func getDatabaseStatsTool(ctx tool.Context, args GetDatabaseStatsArgs) (PsqlResult, error) {
	return getDatabaseStatsImpl(ctx, args)
}

// GetConfigParameterArgs defines arguments for the get_config_parameter tool.
type GetConfigParameterArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	ParameterName    string `json:"parameter_name,omitempty" jsonschema:"Specific parameter name to retrieve (e.g., 'max_connections'). If empty, shows common important parameters."`
}

func getConfigParameterImpl(ctx context.Context, args GetConfigParameterArgs) (PsqlResult, error) {
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

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_config_parameter")
	if err != nil {
		return errorResult("get_config_parameter", args.ConnectionString, err), nil
	}
	return PsqlResult{Output: output}, nil
}

func getConfigParameterTool(ctx tool.Context, args GetConfigParameterArgs) (PsqlResult, error) {
	return getConfigParameterImpl(ctx, args)
}

// GetReplicationStatusArgs defines arguments for the get_replication_status tool.
type GetReplicationStatusArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

func getReplicationStatusImpl(ctx context.Context, args GetReplicationStatusArgs) (PsqlResult, error) {
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

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_replication_status")
	if err != nil {
		return errorResult("get_replication_status", args.ConnectionString, err), nil
	}
	return PsqlResult{Output: output}, nil
}

func getReplicationStatusTool(ctx tool.Context, args GetReplicationStatusArgs) (PsqlResult, error) {
	return getReplicationStatusImpl(ctx, args)
}

// GetLockInfoArgs defines arguments for the get_lock_info tool.
type GetLockInfoArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

func getLockInfoImpl(ctx context.Context, args GetLockInfoArgs) (PsqlResult, error) {
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

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_lock_info")
	if err != nil {
		return errorResult("get_lock_info", args.ConnectionString, err), nil
	}
	if strings.TrimSpace(output) == "" || strings.Contains(output, "(0 rows)") {
		return PsqlResult{Output: "No blocking locks found."}, nil
	}
	return PsqlResult{Output: output}, nil
}

func getLockInfoTool(ctx tool.Context, args GetLockInfoArgs) (PsqlResult, error) {
	return getLockInfoImpl(ctx, args)
}

// GetTableStatsArgs defines arguments for the get_table_stats tool.
type GetTableStatsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	SchemaName       string `json:"schema_name,omitempty" jsonschema:"Schema name to filter tables. Default is 'public'."`
	TableName        string `json:"table_name,omitempty" jsonschema:"Specific table name to get stats for."`
}

func getTableStatsImpl(ctx context.Context, args GetTableStatsArgs) (PsqlResult, error) {
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

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_table_stats")
	if err != nil {
		return errorResult("get_table_stats", args.ConnectionString, err), nil
	}
	return PsqlResult{Output: output}, nil
}

func getTableStatsTool(ctx tool.Context, args GetTableStatsArgs) (PsqlResult, error) {
	return getTableStatsImpl(ctx, args)
}

// GetSessionInfoArgs defines arguments for the get_session_info tool.
type GetSessionInfoArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	PID              int    `json:"pid" jsonschema:"required,The process ID of the session to inspect."`
}

// getSessionInfoImpl inspects a specific backend PID and returns its current
// state, transaction status, uncommitted-work signals, and a rollback time
// estimate. Safe to call at any time — read-only, no side effects.
func getSessionInfoImpl(ctx context.Context, args GetSessionInfoArgs) (PsqlResult, error) {
	plan, err := inspectConnection(ctx, args.ConnectionString, args.PID)
	if err != nil {
		return errorResult("get_session_info", args.ConnectionString, err), nil
	}
	return PsqlResult{Output: formatConnectionPlan(plan)}, nil
}

func getSessionInfoTool(ctx tool.Context, args GetSessionInfoArgs) (PsqlResult, error) {
	return getSessionInfoImpl(ctx, args)
}

// CancelQueryArgs defines arguments for the cancel_query tool.
type CancelQueryArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	PID              int    `json:"pid" jsonschema:"required,The process ID (pid) of the backend to cancel. Use get_active_connections to find pids."`
}

func cancelQueryImpl(ctx context.Context, args CancelQueryArgs) (PsqlResult, error) {
	// Step 1: inspect the session — establishes plan context in the audit trail
	// before any destructive action is taken.
	plan, err := inspectConnection(ctx, args.ConnectionString, args.PID)
	if err != nil {
		return errorResult("cancel_query", args.ConnectionString, err), nil
	}

	// Pre-execution transaction age guardrail: if the session has uncommitted
	// writes in a long-running transaction, cancellation may cause a lengthy
	// rollback. Check against max_xact_age_secs policy condition.
	if policyEnforcer != nil {
		dbInfo, _ := resolveDatabaseInfo(args.ConnectionString)
		if ageErr := policyEnforcer.CheckDatabaseSessionAge(ctx, dbInfo.Name,
			policy.ActionWrite, dbInfo.Tags, plan.OpenTxAgeSecs, plan.HasWrites); ageErr != nil {
			return errorResult("cancel_query", args.ConnectionString, ageErr), nil
		}
	}

	// Step 2: cancel the query (policy pre-check happens inside runPsqlAs).
	query := fmt.Sprintf(`SELECT pg_cancel_backend(%d) AS cancelled, pid, usename, datname, state, LEFT(query, 100) AS query_preview
FROM pg_stat_activity WHERE pid = %d;`, args.PID, args.PID)
	output, err := runPsqlAs(ctx, args.ConnectionString, query, "cancel_query", policy.ActionWrite, formatConnectionPlan(plan))
	if err != nil {
		return errorResult("cancel_query", args.ConnectionString, err), nil
	}
	if strings.Contains(output, "(0 rows)") {
		return PsqlResult{Output: fmt.Sprintf("No backend found with pid %d.", args.PID)}, nil
	}
	// runPsqlAs uses parseRowsAffected which only reads DELETE/UPDATE/INSERT tags.
	// cancel_query uses SELECT pg_cancel_backend(), so we must parse the boolean
	// result explicitly and run a second post-execution blast-radius check.
	cancelled := parsePgFunctionResult(output)
	if policyEnforcer != nil {
		dbInfo, err := resolveDatabaseInfo(args.ConnectionString)
		if err != nil {
			return errorResult("cancel_query", args.ConnectionString, err), nil
		}
		if postErr := policyEnforcer.CheckDatabaseResult(ctx, dbInfo.Name, policy.ActionWrite, dbInfo.Tags, agentutil.ToolOutcome{
			RowsAffected: cancelled,
		}); postErr != nil {
			return errorResult("cancel_query", args.ConnectionString, postErr), nil
		}
	}

	// Level 1: pg_cancel_backend returned false — SIGINT was not delivered.
	if cancelled == 0 {
		return PsqlResult{Output: fmt.Sprintf(
			"CANCELLATION FAILED: pg_cancel_backend(%d) returned false.\n"+
				"The backend may have already finished or this role lacks pg_signal_backend privilege.\n\n"+
				"--- Session details ---\n%s", args.PID, formatConnectionPlan(plan))}, nil
	}

	// Level 2: verify the query is no longer active (state must have left 'active').
	// Re-check up to verifyRetryConfig.MaxAttempts times with exponential backoff
	// to handle transient lag between signal delivery and pg_stat_activity update.
	verifyQuery := fmt.Sprintf(`SELECT state FROM pg_stat_activity WHERE pid = %d;`, args.PID)
	resolved, attempts, _ := retryutil.WaitUntilResolved(ctx, verifyRetryConfig,
		func() (bool, error) {
			out, err := runPsql(ctx, args.ConnectionString, verifyQuery)
			if err != nil {
				return true, nil // pid gone → resolved
			}
			return !strings.Contains(out, "state | active"), nil
		},
		func(attempt int, r bool) {
			if toolAuditor != nil {
				toolAuditor.RecordToolRetry(ctx, "cancel_query", attempt, r)
			}
		},
	)
	retryCount := attempts - 1 // first check is not a retry
	if retryCount < 0 {
		retryCount = 0
	}
	if !resolved && toolAuditor != nil {
		toolAuditor.RecordToolVerification(ctx, "cancel_query", "warning")
	}
	if !resolved {
		return PsqlResult{
			Output: fmt.Sprintf(
				"VERIFICATION WARNING: PID %d is still in 'active' state after %d check(s).\n"+
					"The query may be in a non-interruptible kernel call. Monitor pg_stat_activity and consider terminate_connection if it persists.\n\n"+
					"--- Session details ---\n%s\n--- Cancel result ---\n%s",
				args.PID, attempts, formatConnectionPlan(plan), output),
			VerifyStatus: "warning",
			RetryCount:   retryCount,
		}, nil
	}

	// Step 3: return plan context + execution result so the agent can relay both.
	return PsqlResult{
		Output:       formatConnectionPlan(plan) + "\n--- Result ---\n" + output,
		VerifyStatus: "ok",
		RetryCount:   retryCount,
	}, nil
}

func cancelQueryTool(ctx tool.Context, args CancelQueryArgs) (PsqlResult, error) {
	return cancelQueryImpl(ctx, args)
}

// TerminateConnectionArgs defines arguments for the terminate_connection tool.
type TerminateConnectionArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	PID              int    `json:"pid" jsonschema:"required,The process ID (pid) of the backend to terminate. Use get_active_connections to find pids."`
}

func terminateConnectionImpl(ctx context.Context, args TerminateConnectionArgs) (PsqlResult, error) {
	// Step 1: inspect the session — establishes plan context in the audit trail
	// before any destructive action is taken.
	plan, err := inspectConnection(ctx, args.ConnectionString, args.PID)
	if err != nil {
		return errorResult("terminate_connection", args.ConnectionString, err), nil
	}

	// Pre-execution transaction age guardrail: if the session has uncommitted
	// writes in a long-running transaction, termination triggers a rollback that
	// may take as long as the transaction age. Check against max_xact_age_secs.
	if policyEnforcer != nil {
		dbInfo, _ := resolveDatabaseInfo(args.ConnectionString)
		if ageErr := policyEnforcer.CheckDatabaseSessionAge(ctx, dbInfo.Name,
			policy.ActionDestructive, dbInfo.Tags, plan.OpenTxAgeSecs, plan.HasWrites); ageErr != nil {
			return errorResult("terminate_connection", args.ConnectionString, ageErr), nil
		}
	}

	// Step 2: terminate the connection (policy pre-check happens inside runPsqlAs).
	query := fmt.Sprintf(`SELECT pg_terminate_backend(%d) AS terminated, pid, usename, datname, state, LEFT(query, 100) AS query_preview
FROM pg_stat_activity WHERE pid = %d;`, args.PID, args.PID)
	output, err := runPsqlAs(ctx, args.ConnectionString, query, "terminate_connection", policy.ActionDestructive, formatConnectionPlan(plan))
	if err != nil {
		return errorResult("terminate_connection", args.ConnectionString, err), nil
	}
	if strings.Contains(output, "(0 rows)") {
		return PsqlResult{Output: fmt.Sprintf("No backend found with pid %d.", args.PID)}, nil
	}
	// runPsqlAs uses parseRowsAffected which only reads DELETE/UPDATE/INSERT tags.
	// terminate_connection uses SELECT pg_terminate_backend(), so we must parse
	// the boolean result explicitly and run a second post-execution blast-radius check.
	terminated := parsePgFunctionResult(output)
	if policyEnforcer != nil {
		dbInfo, err := resolveDatabaseInfo(args.ConnectionString)
		if err != nil {
			return errorResult("terminate_connection", args.ConnectionString, err), nil
		}
		if postErr := policyEnforcer.CheckDatabaseResult(ctx, dbInfo.Name, policy.ActionDestructive, dbInfo.Tags, agentutil.ToolOutcome{
			RowsAffected: terminated,
		}); postErr != nil {
			return errorResult("terminate_connection", args.ConnectionString, postErr), nil
		}
	}

	// Level 1: pg_terminate_backend returned false — SIGTERM was not delivered.
	if terminated == 0 {
		return PsqlResult{Output: fmt.Sprintf(
			"TERMINATION FAILED: pg_terminate_backend(%d) returned false.\n"+
				"The backend may have already exited or this role lacks pg_signal_backend privilege.\n\n"+
				"--- Session details ---\n%s", args.PID, formatConnectionPlan(plan))}, nil
	}

	// Level 2: verify the backend is actually gone from pg_stat_activity.
	// A single re-check after a short delay handles the common case where the
	// backend is in a brief kernel call. If it is still alive after that, escalate.
	verifyQuery := fmt.Sprintf(`SELECT count(*) AS still_alive FROM pg_stat_activity WHERE pid = %d;`, args.PID)
	resolved, attempts, _ := retryutil.WaitUntilResolved(ctx,
		verifyTerminateConfig,
		func() (bool, error) {
			out, err := runPsql(ctx, args.ConnectionString, verifyQuery)
			return err != nil || !strings.Contains(out, "still_alive | 1"), nil
		},
		func(attempt int, r bool) {
			if toolAuditor != nil {
				toolAuditor.RecordToolRetry(ctx, "terminate_connection", attempt, r)
			}
		},
	)
	retryCount := attempts - 1
	if retryCount < 0 {
		retryCount = 0
	}
	if !resolved && toolAuditor != nil {
		toolAuditor.RecordToolVerification(ctx, "terminate_connection", "escalation_required")
	}
	if !resolved {
		return PsqlResult{
			Output: fmt.Sprintf(
				"VERIFICATION FAILED: PID %d is still present in pg_stat_activity after termination.\n"+
					"ESCALATION REQUIRED: The backend ignored SIGTERM.\n"+
					"Next steps (in order):\n"+
					"  1. Retry as a superuser role (pg_terminate_backend requires pg_signal_backend for non-owned backends).\n"+
					"  2. OS-level: kill -9 <os_pid> on the database host (find os_pid via pg_stat_activity).\n\n"+
					"--- Session details ---\n%s\n--- Termination result ---\n%s",
				args.PID, formatConnectionPlan(plan), output),
			VerifyStatus: "escalation_required",
			RetryCount:   retryCount,
		}, nil
	}

	// Step 3: return plan context + execution result so the agent can relay both.
	return PsqlResult{
		Output:       formatConnectionPlan(plan) + "\n--- Result ---\n" + output,
		VerifyStatus: "ok",
		RetryCount:   retryCount,
	}, nil
}

func terminateConnectionTool(ctx tool.Context, args TerminateConnectionArgs) (PsqlResult, error) {
	return terminateConnectionImpl(ctx, args)
}

// TerminateIdleConnectionsArgs defines arguments for the terminate_idle_connections tool.
type TerminateIdleConnectionsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	IdleMinutes      int    `json:"idle_minutes" jsonschema:"required,Terminate connections idle longer than this many minutes. Minimum 5."`
	Database         string `json:"database,omitempty" jsonschema:"Limit termination to connections in this specific database. If empty, targets all databases."`
	DryRun           bool   `json:"dry_run,omitempty" jsonschema:"If true, only list connections that would be terminated without actually terminating them. Defaults to false."`
}

func terminateIdleConnectionsImpl(ctx context.Context, args TerminateIdleConnectionsArgs) (PsqlResult, error) {
	if args.IdleMinutes < 5 {
		return PsqlResult{Output: "ERROR: idle_minutes must be at least 5 to avoid terminating legitimately short-lived connections."}, nil
	}

	dbFilter := ""
	if args.Database != "" {
		dbFilter = fmt.Sprintf("AND datname = '%s'", strings.ReplaceAll(args.Database, "'", "''"))
	}

	action := policy.ActionDestructive
	if args.DryRun {
		action = policy.ActionRead
	}

	if args.DryRun {
		query := fmt.Sprintf(`SELECT pid, usename, datname, client_addr, state,
	EXTRACT(EPOCH FROM (now() - state_change))::int / 60 AS idle_minutes,
	LEFT(query, 100) AS last_query
FROM pg_stat_activity
WHERE state = 'idle'
  AND state_change < now() - INTERVAL '%d minutes'
  AND pid != pg_backend_pid()
  %s
ORDER BY state_change ASC;`, args.IdleMinutes, dbFilter)
		output, err := runPsqlAs(ctx, args.ConnectionString, query, "terminate_idle_connections", action, "")
		if err != nil {
			return errorResult("terminate_idle_connections", args.ConnectionString, err), nil
		}
		if strings.Contains(output, "(0 rows)") {
			return PsqlResult{Output: fmt.Sprintf("[DRY RUN] No idle connections older than %d minutes found.", args.IdleMinutes)}, nil
		}
		return PsqlResult{Output: "[DRY RUN] Would terminate:\n" + output}, nil
	}

	query := fmt.Sprintf(`SELECT count(*) AS terminated
FROM (
  SELECT pg_terminate_backend(pid) AS terminated
  FROM pg_stat_activity
  WHERE state = 'idle'
    AND state_change < now() - INTERVAL '%d minutes'
    AND pid != pg_backend_pid()
    %s
) t
WHERE terminated IS TRUE;`, args.IdleMinutes, dbFilter)
	output, err := runPsqlAs(ctx, args.ConnectionString, query, "terminate_idle_connections", action, "")
	if err != nil {
		return errorResult("terminate_idle_connections", args.ConnectionString, err), nil
	}

	// runPsqlAs uses parseRowsAffected which only reads DELETE/UPDATE/INSERT tags.
	// terminate_idle_connections uses SELECT count(*), so we must parse the terminated
	// count explicitly and run a second post-execution blast-radius check.
	if policyEnforcer != nil {
		dbInfo, err := resolveDatabaseInfo(args.ConnectionString)
		if err != nil {
			return errorResult("terminate_idle_connections", args.ConnectionString, err), nil
		}
		terminated := parseTerminatedCount(output)
		if postErr := policyEnforcer.CheckDatabaseResult(ctx, dbInfo.Name, action, dbInfo.Tags, agentutil.ToolOutcome{
			RowsAffected: terminated,
		}); postErr != nil {
			return errorResult("terminate_idle_connections", args.ConnectionString, postErr), nil
		}
	}

	return PsqlResult{Output: output}, nil
}

func terminateIdleConnectionsTool(ctx tool.Context, args TerminateIdleConnectionsArgs) (PsqlResult, error) {
	return terminateIdleConnectionsImpl(ctx, args)
}

// argsToStruct converts a map[string]any to a typed struct via JSON round-trip.
// Used by the direct tool registry to adapt gateway requests to typed tool args.
func argsToStruct[T any](args map[string]any) (T, error) {
	var result T
	data, err := json.Marshal(args)
	if err != nil {
		return result, err
	}
	return result, json.Unmarshal(data, &result)
}

// NewDatabaseDirectRegistry builds a DirectToolRegistry for all database tools.
// These handlers are invoked via POST /tool/{name} on the agent HTTP server,
// bypassing the LLM layer for deterministic fleet job execution.
// ---------------------------------------------------------------------------
// get_pg_settings
// ---------------------------------------------------------------------------

// GetPgSettingsArgs defines arguments for the get_pg_settings tool.
type GetPgSettingsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	Category         string `json:"category,omitempty" jsonschema:"Filter by settings category (e.g. 'autovacuum', 'memory'). Empty returns all non-default settings."`
	ShowAll          bool   `json:"show_all,omitempty" jsonschema:"If true, return all settings not just non-default ones. Can produce large output."`
}

func getPgSettingsImpl(ctx context.Context, args GetPgSettingsArgs) (PsqlResult, error) {
	var where string
	if args.ShowAll {
		where = "1=1"
	} else {
		where = "source NOT IN ('default', 'override')"
	}
	categoryFilter := ""
	if args.Category != "" {
		categoryFilter = fmt.Sprintf(" AND category ILIKE '%%%s%%'", strings.ReplaceAll(args.Category, "'", "''"))
	}
	query := fmt.Sprintf(`SELECT
		category,
		name,
		setting,
		unit,
		boot_val as default_value,
		source,
		short_desc
	FROM pg_settings
	WHERE %s%s
	ORDER BY category, name;`, where, categoryFilter)

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_pg_settings")
	if err != nil {
		return errorResult("get_pg_settings", args.ConnectionString, err), nil
	}
	if strings.TrimSpace(output) == "" || strings.Contains(output, "(0 rows)") {
		msg := "All settings are at their default values."
		if args.Category != "" {
			msg = fmt.Sprintf("All %q category settings are at their default values.", args.Category)
		}
		return PsqlResult{Output: msg}, nil
	}
	return PsqlResult{Output: output}, nil
}

func getPgSettingsTool(ctx tool.Context, args GetPgSettingsArgs) (PsqlResult, error) {
	return getPgSettingsImpl(ctx, args)
}

// ---------------------------------------------------------------------------
// get_extensions
// ---------------------------------------------------------------------------

// GetExtensionsArgs defines arguments for the get_extensions tool.
type GetExtensionsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

func getExtensionsImpl(ctx context.Context, args GetExtensionsArgs) (PsqlResult, error) {
	query := `SELECT
		name,
		installed_version,
		default_version,
		comment
	FROM pg_available_extensions
	WHERE installed_version IS NOT NULL
	ORDER BY name;`

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_extensions")
	if err != nil {
		return errorResult("get_extensions", args.ConnectionString, err), nil
	}
	if strings.TrimSpace(output) == "" || strings.Contains(output, "(0 rows)") {
		return PsqlResult{Output: "No extensions installed."}, nil
	}
	return PsqlResult{Output: output}, nil
}

func getExtensionsTool(ctx tool.Context, args GetExtensionsArgs) (PsqlResult, error) {
	return getExtensionsImpl(ctx, args)
}

// ---------------------------------------------------------------------------
// get_baseline
// ---------------------------------------------------------------------------

// GetBaselineArgs defines arguments for the get_baseline tool.
type GetBaselineArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

func getBaselineImpl(ctx context.Context, args GetBaselineArgs) (PsqlResult, error) {
	conn := args.ConnectionString
	sections := []struct {
		name string
		fn   func() (PsqlResult, error)
	}{
		{"Server Info", func() (PsqlResult, error) {
			return getServerInfoImpl(ctx, GetServerInfoArgs{ConnectionString: conn})
		}},
		{"PG Settings (non-default)", func() (PsqlResult, error) {
			return getPgSettingsImpl(ctx, GetPgSettingsArgs{ConnectionString: conn})
		}},
		{"Extensions", func() (PsqlResult, error) {
			return getExtensionsImpl(ctx, GetExtensionsArgs{ConnectionString: conn})
		}},
		{"Disk Usage", func() (PsqlResult, error) {
			return getDiskUsageImpl(ctx, GetDiskUsageArgs{ConnectionString: conn})
		}},
	}

	var sb strings.Builder
	for _, s := range sections {
		sb.WriteString("══════════════════════════════════════════\n")
		sb.WriteString("  " + s.name + "\n")
		sb.WriteString("══════════════════════════════════════════\n")
		result, err := s.fn()
		if err != nil {
			sb.WriteString(fmt.Sprintf("  WARNING: failed to collect %s: %v\n\n", s.name, err))
			continue
		}
		sb.WriteString(result.Output)
		sb.WriteString("\n")
	}
	return PsqlResult{Output: sb.String()}, nil
}

func getBaselineTool(ctx tool.Context, args GetBaselineArgs) (PsqlResult, error) {
	result, err := getBaselineImpl(ctx, args)
	if err == nil && result.Output != "" {
		// Persist the result so get_saved_snapshots can retrieve it later.
		// This covers the A2A/LLM path; the direct-tool path is persisted by the gateway.
		dbInfo, _ := resolveDatabaseInfo(args.ConnectionString)
		serverName := dbInfo.Name
		if serverName == "" {
			serverName = args.ConnectionString
		}
		argsJSON, _ := json.Marshal(args)
		go persistToolResult(context.WithoutCancel(ctx), "get_baseline", serverName, string(argsJSON), result.Output)
	}
	return result, err
}

// ---------------------------------------------------------------------------
// get_slow_queries
// ---------------------------------------------------------------------------

// GetSlowQueriesArgs defines arguments for the get_slow_queries tool.
type GetSlowQueriesArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	Limit            int    `json:"limit,omitempty" jsonschema:"Number of top queries to return by total execution time (default 10)."`
}

func getSlowQueriesImpl(ctx context.Context, args GetSlowQueriesArgs) (PsqlResult, error) {
	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	query := fmt.Sprintf(`SELECT
		LEFT(query, 120) as query,
		calls,
		ROUND(total_exec_time::numeric, 2) as total_ms,
		ROUND(mean_exec_time::numeric, 2) as mean_ms,
		ROUND(max_exec_time::numeric, 2) as max_ms,
		rows
	FROM pg_stat_statements
	ORDER BY total_exec_time DESC
	LIMIT %d;`, limit)

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_slow_queries")
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "42883") || strings.Contains(msg, "42P01") ||
			strings.Contains(msg, "pg_stat_statements") {
			return PsqlResult{Output: "pg_stat_statements extension is not installed. Enable it with: CREATE EXTENSION pg_stat_statements; and add it to shared_preload_libraries in postgresql.conf."}, nil
		}
		return errorResult("get_slow_queries", args.ConnectionString, err), nil
	}
	if strings.TrimSpace(output) == "" || strings.Contains(output, "(0 rows)") {
		return PsqlResult{Output: "No queries recorded yet. pg_stat_statements resets on server restart."}, nil
	}
	return PsqlResult{Output: output}, nil
}

func getSlowQueriesTool(ctx tool.Context, args GetSlowQueriesArgs) (PsqlResult, error) {
	return getSlowQueriesImpl(ctx, args)
}

// ---------------------------------------------------------------------------
// get_vacuum_status
// ---------------------------------------------------------------------------

// GetVacuumStatusArgs defines arguments for the get_vacuum_status tool.
type GetVacuumStatusArgs struct {
	ConnectionString string  `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	MinDeadRatio     float64 `json:"min_dead_ratio,omitempty" jsonschema:"Only show tables with a dead row ratio >= this value (0–100). Default 0 returns all tables."`
}

func getVacuumStatusImpl(ctx context.Context, args GetVacuumStatusArgs) (PsqlResult, error) {
	query := fmt.Sprintf(`SELECT
		schemaname,
		relname as table_name,
		n_live_tup as live_rows,
		n_dead_tup as dead_rows,
		ROUND(100.0 * n_dead_tup / NULLIF(n_live_tup + n_dead_tup, 0), 2) as dead_ratio_pct,
		last_vacuum,
		last_autovacuum,
		last_analyze,
		last_autoanalyze,
		autovacuum_count,
		autoanalyze_count
	FROM pg_stat_user_tables
	WHERE ROUND(100.0 * n_dead_tup / NULLIF(n_live_tup + n_dead_tup, 0), 2) >= %g
	ORDER BY n_dead_tup DESC
	LIMIT 30;`, args.MinDeadRatio)

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_vacuum_status")
	if err != nil {
		return errorResult("get_vacuum_status", args.ConnectionString, err), nil
	}
	if strings.TrimSpace(output) == "" || strings.Contains(output, "(0 rows)") {
		return PsqlResult{Output: fmt.Sprintf("No tables with dead row ratio >= %.0f%%. Vacuum health looks good.", args.MinDeadRatio)}, nil
	}
	return PsqlResult{Output: output}, nil
}

func getVacuumStatusTool(ctx tool.Context, args GetVacuumStatusArgs) (PsqlResult, error) {
	return getVacuumStatusImpl(ctx, args)
}

// ---------------------------------------------------------------------------
// get_disk_usage
// ---------------------------------------------------------------------------

// GetDiskUsageArgs defines arguments for the get_disk_usage tool.
type GetDiskUsageArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	TopN             int    `json:"top_n,omitempty" jsonschema:"Number of largest tables to include (default 10)."`
}

func getDiskUsageImpl(ctx context.Context, args GetDiskUsageArgs) (PsqlResult, error) {
	topN := args.TopN
	if topN <= 0 {
		topN = 10
	}
	// Part 1: database-level sizes
	dbQuery := `SELECT
		datname as database,
		pg_size_pretty(pg_database_size(datname)) as size,
		pg_database_size(datname) as size_bytes
	FROM pg_database
	WHERE datistemplate = false
	ORDER BY pg_database_size(datname) DESC;`

	dbOut, err := runPsqlWithToolName(ctx, args.ConnectionString, dbQuery, "get_disk_usage")
	if err != nil {
		return errorResult("get_disk_usage", args.ConnectionString, err), nil
	}

	// Part 2: top-N tables across all schemas
	tableQuery := fmt.Sprintf(`SELECT
		schemaname,
		relname as table_name,
		pg_size_pretty(pg_total_relation_size(relid)) as total_size,
		pg_size_pretty(pg_relation_size(relid)) as table_size,
		pg_size_pretty(pg_total_relation_size(relid) - pg_relation_size(relid)) as index_size
	FROM pg_stat_user_tables
	ORDER BY pg_total_relation_size(relid) DESC
	LIMIT %d;`, topN)

	tableOut, err := runPsqlWithToolName(ctx, args.ConnectionString, tableQuery, "get_disk_usage")
	if err != nil {
		return errorResult("get_disk_usage", args.ConnectionString, err), nil
	}

	return PsqlResult{Output: "-- Database Sizes --\n" + dbOut + "\n-- Top " + strconv.Itoa(topN) + " Tables by Disk Usage --\n" + tableOut}, nil
}

func getDiskUsageTool(ctx tool.Context, args GetDiskUsageArgs) (PsqlResult, error) {
	return getDiskUsageImpl(ctx, args)
}

// ---------------------------------------------------------------------------
// get_wait_events
// ---------------------------------------------------------------------------

// GetWaitEventsArgs defines arguments for the get_wait_events tool.
type GetWaitEventsArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

func getWaitEventsImpl(ctx context.Context, args GetWaitEventsArgs) (PsqlResult, error) {
	query := `SELECT
		wait_event_type,
		wait_event,
		count(*) as sessions,
		string_agg(state, ', ' ORDER BY state) as states
	FROM pg_stat_activity
	WHERE wait_event IS NOT NULL
	  AND pid <> pg_backend_pid()
	GROUP BY wait_event_type, wait_event
	ORDER BY sessions DESC;`

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_wait_events")
	if err != nil {
		return errorResult("get_wait_events", args.ConnectionString, err), nil
	}
	if strings.TrimSpace(output) == "" || strings.Contains(output, "(0 rows)") {
		return PsqlResult{Output: "No sessions currently waiting."}, nil
	}
	return PsqlResult{Output: output}, nil
}

func getWaitEventsTool(ctx tool.Context, args GetWaitEventsArgs) (PsqlResult, error) {
	return getWaitEventsImpl(ctx, args)
}

// ---------------------------------------------------------------------------
// get_blocking_queries
// ---------------------------------------------------------------------------

// GetBlockingQueriesArgs defines arguments for the get_blocking_queries tool.
type GetBlockingQueriesArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
}

func getBlockingQueriesImpl(ctx context.Context, args GetBlockingQueriesArgs) (PsqlResult, error) {
	query := `SELECT
		blocked_locks.pid AS blocked_pid,
		blocked_activity.usename AS blocked_user,
		blocked_activity.application_name AS blocked_app,
		blocked_activity.wait_event_type AS wait_type,
		blocked_activity.wait_event,
		blocking_locks.pid AS blocking_pid,
		blocking_activity.usename AS blocking_user,
		blocking_locks.mode AS lock_mode,
		COALESCE(rel.relname, '(relation unknown)') AS relation,
		now() - blocked_activity.xact_start AS blocked_duration,
		LEFT(blocked_activity.query, 100) AS blocked_query,
		LEFT(blocking_activity.query, 100) AS blocking_query
	FROM pg_catalog.pg_locks blocked_locks
	JOIN pg_catalog.pg_stat_activity blocked_activity
		ON blocked_activity.pid = blocked_locks.pid
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
	JOIN pg_catalog.pg_stat_activity blocking_activity
		ON blocking_activity.pid = blocking_locks.pid
	LEFT JOIN pg_catalog.pg_class rel
		ON rel.oid = blocked_locks.relation
	WHERE NOT blocked_locks.granted
	ORDER BY blocked_duration DESC NULLS LAST;`

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, query, "get_blocking_queries")
	if err != nil {
		return errorResult("get_blocking_queries", args.ConnectionString, err), nil
	}
	if strings.TrimSpace(output) == "" || strings.Contains(output, "(0 rows)") {
		return PsqlResult{Output: "No blocking queries found."}, nil
	}
	return PsqlResult{Output: output}, nil
}

func getBlockingQueriesTool(ctx tool.Context, args GetBlockingQueriesArgs) (PsqlResult, error) {
	return getBlockingQueriesImpl(ctx, args)
}

// ---------------------------------------------------------------------------
// explain_query
// ---------------------------------------------------------------------------

// ExplainQueryArgs defines arguments for the explain_query tool.
type ExplainQueryArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	Query            string `json:"query" jsonschema:"required,SQL query to explain. Must be a SELECT/WITH statement unless allow_dml is true."`
	AllowDML         bool   `json:"allow_dml,omitempty" jsonschema:"If false (default), rejects non-SELECT/WITH statements. If true, runs EXPLAIN ANALYZE inside BEGIN/ROLLBACK so DML executes but is never committed."`
}

func explainQueryImpl(ctx context.Context, args ExplainQueryArgs) (PsqlResult, error) {
	if args.Query == "" {
		return PsqlResult{Output: "explain_query: query is required"}, nil
	}

	trimmed := strings.TrimSpace(strings.ToUpper(args.Query))
	isDML := !strings.HasPrefix(trimmed, "SELECT") &&
		!strings.HasPrefix(trimmed, "WITH") &&
		!strings.HasPrefix(trimmed, "EXPLAIN") &&
		!strings.HasPrefix(trimmed, "TABLE")

	if isDML && !args.AllowDML {
		return PsqlResult{Output: "explain_query: only SELECT/WITH queries are allowed by default. Set allow_dml=true to EXPLAIN DML statements (they will be wrapped in BEGIN/ROLLBACK and not committed)."}, nil
	}

	var psqlQuery string
	if isDML && args.AllowDML {
		// Wrap in transaction so DML is rolled back after EXPLAIN ANALYZE.
		psqlQuery = fmt.Sprintf("BEGIN; EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) %s; ROLLBACK;", args.Query)
	} else {
		psqlQuery = fmt.Sprintf("EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) %s", args.Query)
	}

	output, err := runPsqlWithToolName(ctx, args.ConnectionString, psqlQuery, "explain_query")
	if err != nil {
		return errorResult("explain_query", args.ConnectionString, err), nil
	}
	return PsqlResult{Output: output}, nil
}

func explainQueryTool(ctx tool.Context, args ExplainQueryArgs) (PsqlResult, error) {
	return explainQueryImpl(ctx, args)
}

// ---------------------------------------------------------------------------
// read_pg_log
// ---------------------------------------------------------------------------

const (
	pgLogReadBytes    = 131072 // 128 KB — enough for ~1000 typical log lines
	pgLogDefaultLines = 100
	pgLogMaxLines     = 500
)

// GetPgLogArgs defines arguments for the read_pg_log tool.
type GetPgLogArgs struct {
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string. If empty, uses environment defaults."`
	Lines            int    `json:"lines,omitempty" jsonschema:"Number of recent log lines to return (default 100, max 500)."`
	Filter           string `json:"filter,omitempty" jsonschema:"Only return lines containing this string (case-insensitive). Useful for: ERROR, FATAL, PANIC, or a specific parameter name."`
}

func getPgLogImpl(ctx context.Context, args GetPgLogArgs) (PsqlResult, error) {
	lines := args.Lines
	if lines <= 0 {
		lines = pgLogDefaultLines
	}
	if lines > pgLogMaxLines {
		lines = pgLogMaxLines
	}

	// pg_ls_logdir() + pg_read_file() require PostgreSQL ≥ 10 and superuser or
	// pg_read_server_files. log_directory may be absolute (/var/log/postgresql)
	// or relative to data_directory (pg_log) — handle both.
	query := fmt.Sprintf(`
WITH latest AS (
  SELECT
    CASE WHEN current_setting('log_directory') LIKE '/%%'
      THEN current_setting('log_directory') || '/' || name
      ELSE current_setting('data_directory') || '/' || current_setting('log_directory') || '/' || name
    END AS path,
    size
  FROM pg_ls_logdir()
  ORDER BY modification DESC
  LIMIT 1
)
SELECT COALESCE(
  pg_read_file(path, GREATEST(0, size - %d), LEAST(size, %d)),
  ''
)
FROM latest;`, pgLogReadBytes, pgLogReadBytes)

	raw, err := runPsqlTuples(ctx, args.ConnectionString, query, "read_pg_log")
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "pg_ls_logdir") && strings.Contains(errStr, "permission denied") {
			return PsqlResult{Output: "read_pg_log: permission denied for pg_ls_logdir — the database user lacks the pg_read_server_files privilege or superuser role.\n\nFor Kubernetes-managed PostgreSQL, use the k8s agent's get_pod_logs tool on the postgres pod instead (e.g. kubectl logs <pod> -n <namespace>)."}, nil
		}
		return errorResult("read_pg_log", args.ConnectionString, err), nil
	}

	content := strings.TrimRight(raw, "\n")
	if content == "" {
		return PsqlResult{Output: "read_pg_log: no log content found. Check that logging_collector=on and log_destination includes stderr or csvlog."}, nil
	}

	allLines := strings.Split(content, "\n")

	filterLower := strings.ToLower(strings.TrimSpace(args.Filter))
	if filterLower != "" {
		filtered := allLines[:0]
		for _, l := range allLines {
			if strings.Contains(strings.ToLower(l), filterLower) {
				filtered = append(filtered, l)
			}
		}
		allLines = filtered
		if len(allLines) == 0 {
			return PsqlResult{Output: fmt.Sprintf("read_pg_log: no log lines matched filter %q.", args.Filter)}, nil
		}
	}

	if len(allLines) > lines {
		allLines = allLines[len(allLines)-lines:]
	}

	header := fmt.Sprintf("=== PostgreSQL Log (last %d lines", len(allLines))
	if filterLower != "" {
		header += fmt.Sprintf(", filter=%q", args.Filter)
	}
	header += ") ===\n"

	return PsqlResult{Output: header + strings.Join(allLines, "\n")}, nil
}

func getPgLogTool(ctx tool.Context, args GetPgLogArgs) (PsqlResult, error) {
	return getPgLogImpl(ctx, args)
}

// runPsqlTuples executes a read-only psql query and returns the raw column
// value without any formatting (-t -A flags, tuples-only unaligned output).
// Use this instead of runPsqlWithToolName when the result is large text that
// would be corrupted by psql's expanded (-x) column wrapping, e.g. pg_read_file.
func runPsqlTuples(ctx context.Context, connStr string, query string, toolName string) (string, error) {
	dbInfo, err := resolveDatabaseInfo(connStr)
	if err != nil {
		return "", err
	}

	if policyEnforcer != nil {
		note := ""
		if !dbInfo.IsFromInfraConfig {
			note = "connection string not found in infraConfig; no tags available for policy matching"
		}
		policyCtx := agentutil.WithToolName(ctx, toolName)
		if err := policyEnforcer.CheckDatabase(policyCtx, dbInfo.Name, policy.ActionRead, dbInfo.Tags, note, dbInfo.Sensitivity); err != nil {
			slog.Warn("policy denied database access", "tool", toolName, "database", dbInfo.Name, "err", err)
			return "", fmt.Errorf("policy denied: %w", err)
		}
	}

	start := time.Now()
	psqlArgs := []string{"-w", "-t", "-A", "-c", query}
	if dbInfo.ConnectionStr != "" {
		psqlArgs = append([]string{dbInfo.ConnectionStr}, psqlArgs...)
	}
	output, err := cmdRunner.Run(ctx, "psql", psqlArgs, []string{"PGCONNECT_TIMEOUT=10"})
	duration := time.Since(start)

	if toolAuditor != nil {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       toolName,
			Parameters: map[string]any{"connection_string": maskPassword(connStr)},
			RawCommand: query,
		}, audit.ToolResult{
			Output: truncateForAudit(output, 500),
			Error:  errMsg,
		}, duration)
	}

	if err != nil {
		out := strings.TrimSpace(output)
		if out == "" {
			out = "(no output from psql)"
		}
		slog.Error("psql command failed", "tool", toolName, "ms", duration.Milliseconds(), "err", err, "output", out)
		if ctx.Err() != nil {
			return "", fmt.Errorf("psql timed out or was cancelled: %v\nOutput: %s", ctx.Err(), out)
		}
		if diagnosis := diagnosePsqlError(out); diagnosis != "" {
			return "", fmt.Errorf("%s\n\nRaw error: %s", diagnosis, out)
		}
		return "", fmt.Errorf("psql failed: %v\nOutput: %s", err, out)
	}

	slog.Info("tool ok", "name", toolName, "ms", duration.Milliseconds())
	return output, nil
}

// ---------------------------------------------------------------------------
// read_uploaded_file
// ---------------------------------------------------------------------------

// ReadUploadedFileArgs defines arguments for the read_uploaded_file tool.
type ReadUploadedFileArgs struct {
	UploadID string `json:"upload_id" jsonschema:"Upload ID returned by the POST /api/v1/fleet/uploads endpoint (ul_ prefix)."`
	Lines    int    `json:"lines,omitempty" jsonschema:"Number of recent lines to return (default 100, max 500)."`
	Filter   string `json:"filter,omitempty" jsonschema:"Only return lines containing this string (case-insensitive). Useful for: ERROR, FATAL, PANIC, or a specific parameter name."`
}

func readUploadedFileImpl(ctx context.Context, args ReadUploadedFileArgs) (PsqlResult, error) {
	if auditBaseURL == "" {
		return PsqlResult{Output: "read_uploaded_file: HELPDESK_AUDIT_URL is not configured — cannot retrieve uploads"}, nil
	}
	if args.UploadID == "" {
		return PsqlResult{Output: "read_uploaded_file: upload_id is required"}, nil
	}

	lines := args.Lines
	if lines <= 0 {
		lines = pgLogDefaultLines
	}
	if lines > pgLogMaxLines {
		lines = pgLogMaxLines
	}

	url := strings.TrimSuffix(auditBaseURL, "/") + "/v1/uploads/" + args.UploadID + "/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("read_uploaded_file ERROR: failed to build request: %v", err)}, nil
	}
	if auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+auditAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("read_uploaded_file ERROR: auditd unreachable: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return PsqlResult{Output: fmt.Sprintf("read_uploaded_file: upload %q not found or expired (uploads expire after 24 hours)", args.UploadID)}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return PsqlResult{Output: fmt.Sprintf("read_uploaded_file ERROR: auditd returned status %d", resp.StatusCode)}, nil
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, pgLogReadBytes))
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("read_uploaded_file ERROR: failed to read response: %v", err)}, nil
	}

	content := strings.TrimRight(string(raw), "\n")
	if content == "" {
		return PsqlResult{Output: "read_uploaded_file: the uploaded file is empty."}, nil
	}

	allLines := strings.Split(content, "\n")

	filterLower := strings.ToLower(strings.TrimSpace(args.Filter))
	if filterLower != "" {
		filtered := allLines[:0]
		for _, l := range allLines {
			if strings.Contains(strings.ToLower(l), filterLower) {
				filtered = append(filtered, l)
			}
		}
		allLines = filtered
		if len(allLines) == 0 {
			return PsqlResult{Output: fmt.Sprintf("read_uploaded_file: no lines matched filter %q.", args.Filter)}, nil
		}
	}

	if len(allLines) > lines {
		allLines = allLines[len(allLines)-lines:]
	}

	header := fmt.Sprintf("=== Uploaded File (last %d lines", len(allLines))
	if filterLower != "" {
		header += fmt.Sprintf(", filter=%q", args.Filter)
	}
	header += ") ===\n"

	return PsqlResult{Output: header + strings.Join(allLines, "\n")}, nil
}

func readUploadedFileTool(ctx tool.Context, args ReadUploadedFileArgs) (PsqlResult, error) {
	return readUploadedFileImpl(ctx, args)
}

// ---------------------------------------------------------------------------
// persistToolResult — best-effort persistence for snapshot tools
// ---------------------------------------------------------------------------

// persistToolResult posts a tool result to auditd's /v1/tool-results endpoint
// so it is available to get_saved_snapshots. Intended for the A2A path where
// the gateway's recordToolResult is not called. Fire-and-forget: failures are
// logged but do not affect the tool response.
func persistToolResult(ctx context.Context, toolName, serverName, argsJSON, output string) {
	if auditBaseURL == "" {
		return
	}
	traceID := ""
	if currentTraceStore != nil {
		traceID = currentTraceStore.Get()
	}
	recordedBy := audit.PrincipalFromContext(ctx).EffectiveID()
	if recordedBy == "" {
		recordedBy = "database-agent"
	}

	body, err := json.Marshal(map[string]any{
		"server_name": serverName,
		"tool_name":   toolName,
		"tool_args":   argsJSON,
		"output":      output,
		"trace_id":    traceID,
		"recorded_by": recordedBy,
		"success":     true,
	})
	if err != nil {
		slog.Warn("persistToolResult: marshal failed", "tool", toolName, "err", err)
		return
	}

	url := strings.TrimSuffix(auditBaseURL, "/") + "/v1/tool-results"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		slog.Warn("persistToolResult: build request failed", "tool", toolName, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+auditAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("persistToolResult: request failed", "tool", toolName, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		slog.Warn("persistToolResult: unexpected status", "tool", toolName, "status", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// get_saved_snapshots
// ---------------------------------------------------------------------------

// maxSnapshotOutputBytes caps the total output returned to avoid filling the
// context window when multiple large baselines are fetched at once.
const maxSnapshotOutputBytes = 32 * 1024 // 32 KB total

// GetSavedSnapshotsArgs defines arguments for the get_saved_snapshots tool.
type GetSavedSnapshotsArgs struct {
	ToolName   string `json:"tool_name" jsonschema:"Name of the tool whose saved outputs to retrieve (e.g. 'get_baseline', 'get_pg_settings')."`
	ServerName string `json:"server_name,omitempty" jsonschema:"Filter to a specific server name. If omitted, results for all servers are returned."`
	Limit      int    `json:"limit,omitempty" jsonschema:"Number of snapshots to return, most recent first (default 3, max 10). Use 1 to extract a value; use 2 to diff; use 5–10 to find when something changed."`
	Since      string `json:"since,omitempty" jsonschema:"How far back to search, e.g. '7d', '30d', '90d' (default '90d')."`
}

func getSavedSnapshotsImpl(ctx context.Context, args GetSavedSnapshotsArgs) (PsqlResult, error) {
	if auditBaseURL == "" {
		return PsqlResult{Output: "get_saved_snapshots: HELPDESK_AUDIT_URL is not configured — snapshot history is unavailable"}, nil
	}
	if args.ToolName == "" {
		return PsqlResult{Output: "get_saved_snapshots: tool_name is required"}, nil
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 3
	}
	if limit > 10 {
		limit = 10
	}
	since := args.Since
	if since == "" {
		since = "90d"
	}

	u := strings.TrimSuffix(auditBaseURL, "/") + "/v1/tool-results"
	u += fmt.Sprintf("?tool=%s&limit=%d&since=%s", args.ToolName, limit, since)
	if args.ServerName != "" {
		u += "&server=" + args.ServerName
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("get_saved_snapshots ERROR: failed to build request: %v", err)}, nil
	}
	if auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+auditAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return PsqlResult{Output: fmt.Sprintf("get_saved_snapshots ERROR: auditd unreachable: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return PsqlResult{Output: fmt.Sprintf("get_saved_snapshots ERROR: auditd returned status %d", resp.StatusCode)}, nil
	}

	var body struct {
		Results []*audit.PersistedToolResult `json:"results"`
		Count   int                          `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return PsqlResult{Output: fmt.Sprintf("get_saved_snapshots ERROR: failed to parse response: %v", err)}, nil
	}

	if body.Count == 0 {
		msg := fmt.Sprintf("get_saved_snapshots: no saved results found for tool %q", args.ToolName)
		if args.ServerName != "" {
			msg += fmt.Sprintf(" on server %q", args.ServerName)
		}
		msg += fmt.Sprintf(" in the last %s", since)
		return PsqlResult{Output: msg}, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "=== %d saved snapshot(s) for tool %q", body.Count, args.ToolName)
	if args.ServerName != "" {
		fmt.Fprintf(&sb, " on %q", args.ServerName)
	}
	fmt.Fprintf(&sb, " (most recent first) ===\n\n")

	for i, r := range body.Results {
		fmt.Fprintf(&sb, "── Snapshot %d ─ recorded: %s UTC ─ server: %s ─ by: %s ──\n",
			i+1, r.RecordedAt.UTC().Format("2006-01-02 15:04:05"), r.ServerName, r.RecordedBy)
		sb.WriteString(r.Output)
		if !strings.HasSuffix(r.Output, "\n") {
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')

		if sb.Len() > maxSnapshotOutputBytes {
			fmt.Fprintf(&sb, "[output truncated — %d of %d snapshots shown]\n", i+1, body.Count)
			break
		}
	}

	return PsqlResult{Output: sb.String()}, nil
}

func getSavedSnapshotsTool(ctx tool.Context, args GetSavedSnapshotsArgs) (PsqlResult, error) {
	return getSavedSnapshotsImpl(ctx, args)
}

func NewDatabaseDirectRegistry() *agentutil.DirectToolRegistry {
	r := agentutil.NewDirectToolRegistry()
	r.Register("check_connection", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[CheckConnectionArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := checkConnectionImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_server_info", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetServerInfoArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getServerInfoImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_database_info", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetDatabaseInfoArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getDatabaseInfoImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_active_connections", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetActiveConnectionsArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getActiveConnectionsImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_connection_stats", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetConnectionStatsArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getConnectionStatsImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_database_stats", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetDatabaseStatsArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getDatabaseStatsImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_config_parameter", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetConfigParameterArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getConfigParameterImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_replication_status", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetReplicationStatusArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getReplicationStatusImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_lock_info", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetLockInfoArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getLockInfoImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_table_stats", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetTableStatsArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getTableStatsImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_session_info", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetSessionInfoArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getSessionInfoImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("cancel_query", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[CancelQueryArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := cancelQueryImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("terminate_connection", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[TerminateConnectionArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := terminateConnectionImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("terminate_idle_connections", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[TerminateIdleConnectionsArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := terminateIdleConnectionsImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_status_summary", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetStatusSummaryArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getStatusSummaryImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_pg_settings", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetPgSettingsArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getPgSettingsImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_extensions", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetExtensionsArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getExtensionsImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_baseline", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetBaselineArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getBaselineImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_slow_queries", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetSlowQueriesArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getSlowQueriesImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_vacuum_status", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetVacuumStatusArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getVacuumStatusImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_disk_usage", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetDiskUsageArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getDiskUsageImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_wait_events", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetWaitEventsArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getWaitEventsImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_blocking_queries", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetBlockingQueriesArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getBlockingQueriesImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("explain_query", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[ExplainQueryArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := explainQueryImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("read_pg_log", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetPgLogArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getPgLogImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("read_uploaded_file", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[ReadUploadedFileArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := readUploadedFileImpl(ctx, a)
		return result.Output, nil
	})
	r.Register("get_saved_snapshots", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetSavedSnapshotsArgs](args)
		if err != nil {
			return "", err
		}
		result, _ := getSavedSnapshotsImpl(ctx, a)
		return result.Output, nil
	})
	return r
}
