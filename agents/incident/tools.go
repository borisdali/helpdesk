package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// --- Command helpers ---

// runPsql executes a psql command and returns the output.
func runPsql(ctx context.Context, connStr string, query string) (string, error) {
	args := []string{"-c", query, "-x"}
	if connStr != "" {
		args = append([]string{connStr}, args...)
	}
	cmd := exec.CommandContext(ctx, "psql", args...)
	cmd.Env = append(os.Environ(), "PGCONNECT_TIMEOUT=10")
	output, err := cmd.CombinedOutput()
	if err != nil {
		out := strings.TrimSpace(string(output))
		if out == "" {
			out = "(no output from psql)"
		}
		slog.Error("psql command failed", "err", err, "output", out)
		return "", fmt.Errorf("psql failed: %v\nOutput: %s", err, out)
	}
	return string(output), nil
}

// runKubectl executes a kubectl command and returns the output.
func runKubectl(ctx context.Context, kubeContext string, args ...string) (string, error) {
	prefix := []string{"--request-timeout=10s"}
	if kubeContext != "" {
		prefix = append(prefix, "--context", kubeContext)
	}
	args = append(prefix, args...)
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		out := strings.TrimSpace(string(output))
		if out == "" {
			out = "(no output from kubectl)"
		}
		slog.Error("kubectl command failed", "args", args, "err", err, "output", out)
		return "", fmt.Errorf("kubectl failed: %v\nOutput: %s", err, out)
	}
	return string(output), nil
}

// runCommand executes a generic command and returns the output.
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		out := strings.TrimSpace(string(output))
		if out == "" {
			out = "(no output)"
		}
		slog.Error("command failed", "cmd", name, "args", args, "err", err, "output", out)
		return "", fmt.Errorf("%s failed: %v\nOutput: %s", name, err, out)
	}
	return string(output), nil
}

// --- Layer collectors ---
// Each returns (filename→content, errors).

func collectDatabaseLayer(ctx context.Context, connStr string) (map[string]string, []string) {
	files := make(map[string]string)
	var errs []string

	queries := []struct {
		filename string
		query    string
	}{
		{"version.txt", "SELECT version(), current_database(), current_user, inet_server_addr(), inet_server_port();"},
		{"databases.txt", `SELECT
			d.datname as database,
			pg_size_pretty(pg_database_size(d.datname)) as size,
			pg_catalog.pg_get_userbyid(d.datdba) as owner,
			pg_catalog.pg_encoding_to_char(d.encoding) as encoding,
			d.datcollate as collation,
			d.datconnlimit as connection_limit,
			CASE WHEN pg_is_in_recovery() THEN 'Yes' ELSE 'No' END as in_recovery
		FROM pg_database d
		WHERE d.datistemplate = false
		ORDER BY pg_database_size(d.datname) DESC;`},
		{"active_connections.txt", `SELECT
			pid, usename as user, datname as database, client_addr, state,
			wait_event_type, wait_event,
			EXTRACT(EPOCH FROM (now() - query_start))::int as query_seconds,
			LEFT(query, 200) as query_preview
		FROM pg_stat_activity
		WHERE pid != pg_backend_pid() AND state != 'idle'
		ORDER BY query_start ASC NULLS LAST
		LIMIT 100;`},
		{"connection_stats.txt", `SELECT
			datname as database,
			COUNT(*) as total_connections,
			COUNT(*) FILTER (WHERE state = 'active') as active,
			COUNT(*) FILTER (WHERE state = 'idle') as idle,
			COUNT(*) FILTER (WHERE state = 'idle in transaction') as idle_in_transaction,
			COUNT(*) FILTER (WHERE wait_event_type = 'Lock') as waiting_on_lock,
			(SELECT setting::int FROM pg_settings WHERE name = 'max_connections') as max_connections
		FROM pg_stat_activity
		GROUP BY datname
		ORDER BY total_connections DESC;`},
		{"database_stats.txt", `SELECT
			datname as database, numbackends as connections,
			xact_commit as commits, xact_rollback as rollbacks,
			blks_read as blocks_read, blks_hit as cache_hits,
			ROUND(100.0 * blks_hit / NULLIF(blks_read + blks_hit, 0), 2) as cache_hit_ratio,
			tup_returned as rows_returned, tup_fetched as rows_fetched,
			tup_inserted as rows_inserted, tup_updated as rows_updated, tup_deleted as rows_deleted,
			conflicts, deadlocks
		FROM pg_stat_database
		WHERE datname NOT LIKE 'template%'
		ORDER BY numbackends DESC;`},
		{"config_params.txt", `SELECT name, setting, unit, short_desc
		FROM pg_settings
		WHERE name IN (
			'max_connections', 'shared_buffers', 'effective_cache_size',
			'work_mem', 'maintenance_work_mem', 'wal_level',
			'max_wal_senders', 'max_replication_slots', 'hot_standby',
			'listen_addresses', 'port', 'log_min_duration_statement',
			'statement_timeout', 'lock_timeout', 'idle_in_transaction_session_timeout'
		)
		ORDER BY name;`},
		{"replication_status.txt", `SELECT
			CASE WHEN pg_is_in_recovery() THEN 'Replica' ELSE 'Primary' END as role,
			pg_is_in_recovery() as is_in_recovery;
		SELECT client_addr, usename as user, application_name, state, sync_state,
			pg_wal_lsn_diff(sent_lsn, write_lsn) as write_lag_bytes,
			pg_wal_lsn_diff(sent_lsn, flush_lsn) as flush_lag_bytes,
			pg_wal_lsn_diff(sent_lsn, replay_lsn) as replay_lag_bytes
		FROM pg_stat_replication;
		SELECT slot_name, slot_type, active,
			pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn) as lag_bytes
		FROM pg_replication_slots;`},
		{"locks.txt", `SELECT
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
		WHERE NOT blocked_locks.granted;`},
		{"table_stats.txt", `SELECT
			schemaname, relname as table_name,
			pg_size_pretty(pg_total_relation_size(relid)) as total_size,
			n_live_tup as live_rows, n_dead_tup as dead_rows,
			ROUND(100.0 * n_dead_tup / NULLIF(n_live_tup + n_dead_tup, 0), 2) as dead_ratio,
			last_vacuum, last_autovacuum, last_analyze, last_autoanalyze,
			seq_scan, idx_scan
		FROM pg_stat_user_tables
		ORDER BY pg_total_relation_size(relid) DESC
		LIMIT 30;`},
	}

	for _, q := range queries {
		output, err := runPsql(ctx, connStr, q.query)
		if err != nil {
			errs = append(errs, fmt.Sprintf("database/%s: %v", q.filename, err))
			files[q.filename] = fmt.Sprintf("ERROR: %v", err)
		} else {
			files[q.filename] = output
		}
	}

	return files, errs
}

func collectKubernetesLayer(ctx context.Context, kubeContext, namespace string) (map[string]string, []string) {
	files := make(map[string]string)
	var errs []string

	commands := []struct {
		filename string
		args     []string
	}{
		{"pods.txt", []string{"get", "pods", "-o", "wide", "-n", namespace}},
		{"pods_all.txt", []string{"get", "pods", "-o", "wide", "--all-namespaces"}},
		{"services.txt", []string{"get", "svc", "-o", "wide", "-n", namespace}},
		{"endpoints.txt", []string{"get", "endpoints", "-o", "wide", "-n", namespace}},
		{"events.txt", []string{"get", "events", "--sort-by=.lastTimestamp", "-n", namespace}},
		{"nodes.txt", []string{"get", "nodes", "-o", "wide"}},
		{"top_nodes.txt", []string{"top", "nodes"}},
		{"top_pods.txt", []string{"top", "pods", "-n", namespace}},
	}

	for _, c := range commands {
		output, err := runKubectl(ctx, kubeContext, c.args...)
		if err != nil {
			errs = append(errs, fmt.Sprintf("kubernetes/%s: %v", c.filename, err))
			files[c.filename] = fmt.Sprintf("ERROR: %v", err)
		} else {
			files[c.filename] = output
		}
	}

	return files, errs
}

func collectOSLayer(ctx context.Context) (map[string]string, []string) {
	files := make(map[string]string)
	var errs []string

	commands := []struct {
		filename string
		name     string
		args     []string
	}{
		{"uname.txt", "uname", []string{"-a"}},
		{"uptime.txt", "uptime", nil},
		{"hostname.txt", "hostname", nil},
		{"top.txt", "top", []string{"-b", "-n", "1"}},
		{"ps.txt", "ps", []string{"aux", "--sort=-pcpu"}},
		{"free.txt", "free", []string{"-h"}},
		{"vmstat.txt", "vmstat", []string{"1", "3"}},
		{"dmesg.txt", "dmesg", []string{"--time-format=iso", "-T"}},
		{"sysctl.txt", "sysctl", []string{"-a"}},
	}

	for _, c := range commands {
		output, err := runCommand(ctx, c.name, c.args...)
		if err != nil {
			errs = append(errs, fmt.Sprintf("os/%s: %v", c.filename, err))
			files[c.filename] = fmt.Sprintf("ERROR: %v", err)
		} else {
			files[c.filename] = output
		}
	}

	return files, errs
}

func collectStorageLayer(ctx context.Context) (map[string]string, []string) {
	files := make(map[string]string)
	var errs []string

	commands := []struct {
		filename string
		name     string
		args     []string
	}{
		{"df.txt", "df", []string{"-h"}},
		{"df_inodes.txt", "df", []string{"-i"}},
		{"mount.txt", "mount", nil},
		{"lsblk.txt", "lsblk", []string{"-f"}},
		{"iostat.txt", "iostat", []string{"-x", "1", "3"}},
	}

	for _, c := range commands {
		output, err := runCommand(ctx, c.name, c.args...)
		if err != nil {
			errs = append(errs, fmt.Sprintf("storage/%s: %v", c.filename, err))
			files[c.filename] = fmt.Sprintf("ERROR: %v", err)
		} else {
			files[c.filename] = output
		}
	}

	return files, errs
}

// --- Tool definition ---

// CreateIncidentBundleArgs defines arguments for the create_incident_bundle tool.
type CreateIncidentBundleArgs struct {
	InfraKey         string `json:"infra_key,omitempty" jsonschema:"Identifier for the infrastructure being diagnosed (e.g., 'global-corp-db'). Defaults to 'unknown'."`
	Description      string `json:"description,omitempty" jsonschema:"Brief description of the incident or reason for the bundle. Defaults to 'Diagnostic bundle'."`
	ConnectionString string `json:"connection_string,omitempty" jsonschema:"PostgreSQL connection string for database layer collection. If empty, database layer is skipped."`
	K8sContext       string `json:"k8s_context,omitempty" jsonschema:"Kubernetes context for k8s layer collection. If empty, k8s layer is skipped."`
	K8sNamespace     string `json:"k8s_namespace,omitempty" jsonschema:"Kubernetes namespace for k8s commands. Defaults to 'default'."`
}

// IncidentBundleResult is the output of create_incident_bundle.
type IncidentBundleResult struct {
	IncidentID string   `json:"incident_id"`
	BundlePath string   `json:"bundle_path"`
	Timestamp  string   `json:"timestamp"`
	Layers     []string `json:"layers"`
	Errors     []string `json:"errors,omitempty"`
}

func createIncidentBundleTool(ctx tool.Context, args CreateIncidentBundleArgs) (IncidentBundleResult, error) {
	now := time.Now()
	incidentID := generateShortID()

	if args.InfraKey == "" {
		args.InfraKey = "unknown"
	}
	if args.Description == "" {
		args.Description = "Diagnostic bundle"
	}

	namespace := args.K8sNamespace
	if namespace == "" {
		namespace = "default"
	}

	outputDir := os.Getenv("HELPDESK_INCIDENT_DIR")
	if outputDir == "" {
		outputDir = "."
	}

	slog.Info("creating incident bundle",
		"incident_id", incidentID,
		"infra_key", args.InfraKey,
		"description", args.Description,
	)

	layers := make(map[string]map[string]string)
	var collectedLayers []string
	var allErrors []string

	// Database layer
	if args.ConnectionString != "" {
		slog.Info("collecting database layer", "incident_id", incidentID)
		files, errs := collectDatabaseLayer(ctx, args.ConnectionString)
		layers["database"] = files
		collectedLayers = append(collectedLayers, "database")
		allErrors = append(allErrors, errs...)
	}

	// Kubernetes layer
	if args.K8sContext != "" {
		slog.Info("collecting kubernetes layer", "incident_id", incidentID)
		files, errs := collectKubernetesLayer(ctx, args.K8sContext, namespace)
		layers["kubernetes"] = files
		collectedLayers = append(collectedLayers, "kubernetes")
		allErrors = append(allErrors, errs...)
	}

	// OS layer (always collected)
	slog.Info("collecting os layer", "incident_id", incidentID)
	{
		files, errs := collectOSLayer(ctx)
		layers["os"] = files
		collectedLayers = append(collectedLayers, "os")
		allErrors = append(allErrors, errs...)
	}

	// Storage layer (always collected)
	slog.Info("collecting storage layer", "incident_id", incidentID)
	{
		files, errs := collectStorageLayer(ctx)
		layers["storage"] = files
		collectedLayers = append(collectedLayers, "storage")
		allErrors = append(allErrors, errs...)
	}

	manifest := Manifest{
		IncidentID:  incidentID,
		InfraKey:    args.InfraKey,
		Description: args.Description,
		Timestamp:   now,
		Layers:      collectedLayers,
		Errors:      allErrors,
	}

	bundlePath, err := assembleTarball(manifest, layers, outputDir)
	if err != nil {
		return IncidentBundleResult{}, fmt.Errorf("failed to assemble tarball: %v", err)
	}

	// Record in incidents.json index.
	if err := appendToIndex(outputDir, manifest, bundlePath); err != nil {
		slog.Warn("failed to update incidents.json index", "err", err)
		// Non-fatal: the tarball was already written successfully.
	}

	slog.Info("incident bundle created",
		"incident_id", incidentID,
		"bundle_path", bundlePath,
		"layers", collectedLayers,
		"error_count", len(allErrors),
	)

	return IncidentBundleResult{
		IncidentID: incidentID,
		BundlePath: bundlePath,
		Timestamp:  now.Format("20060102-150405"),
		Layers:     collectedLayers,
		Errors:     allErrors,
	}, nil
}

// generateShortID returns an 8-character hex string using crypto/rand.
func generateShortID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp if crypto/rand fails (should never happen).
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b)
}

// --- Index file (incidents.json) ---

const indexFileName = "incidents.json"

// IndexEntry is one record in the incidents.json append-only log.
type IndexEntry struct {
	IncidentID  string   `json:"incident_id"`
	InfraKey    string   `json:"infra_key"`
	Description string   `json:"description"`
	Timestamp   string   `json:"timestamp"`
	BundlePath  string   `json:"bundle_path"`
	Layers      []string `json:"layers"`
	ErrorCount  int      `json:"error_count"`
}

// appendToIndex reads the existing incidents.json (if any), appends a new entry, and writes it back.
func appendToIndex(outputDir string, m Manifest, bundlePath string) error {
	indexPath := filepath.Join(outputDir, indexFileName)

	var entries []IndexEntry
	if data, err := os.ReadFile(indexPath); err == nil {
		if err := json.Unmarshal(data, &entries); err != nil {
			slog.Warn("incidents.json is corrupted, starting fresh", "err", err)
			entries = nil
		}
	}

	entries = append(entries, IndexEntry{
		IncidentID:  m.IncidentID,
		InfraKey:    m.InfraKey,
		Description: m.Description,
		Timestamp:   m.Timestamp.Format("20060102-150405"),
		BundlePath:  bundlePath,
		Layers:      m.Layers,
		ErrorCount:  len(m.Errors),
	})

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal index: %v", err)
	}
	return os.WriteFile(indexPath, data, 0644)
}

// --- list_incidents tool ---

// ListIncidentsArgs defines arguments for the list_incidents tool.
type ListIncidentsArgs struct{}

// ListIncidentsResult is the output of list_incidents.
type ListIncidentsResult struct {
	Incidents []IndexEntry `json:"incidents"`
}

func listIncidentsTool(ctx tool.Context, args ListIncidentsArgs) (ListIncidentsResult, error) {
	outputDir := os.Getenv("HELPDESK_INCIDENT_DIR")
	if outputDir == "" {
		outputDir = "."
	}

	indexPath := filepath.Join(outputDir, indexFileName)
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ListIncidentsResult{}, nil
		}
		return ListIncidentsResult{}, fmt.Errorf("failed to read incidents index: %v", err)
	}

	var entries []IndexEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return ListIncidentsResult{}, fmt.Errorf("failed to parse incidents index: %v", err)
	}

	return ListIncidentsResult{Incidents: entries}, nil
}

// --- Tool registration ---

func createTools() ([]tool.Tool, error) {
	bundleTool, err := functiontool.New(functiontool.Config{
		Name:        "create_incident_bundle",
		Description: "Collect diagnostic data from database, Kubernetes, OS, and storage layers, then package everything into a timestamped .tar.gz bundle for vendor support.",
	}, createIncidentBundleTool)
	if err != nil {
		return nil, err
	}

	listTool, err := functiontool.New(functiontool.Config{
		Name:        "list_incidents",
		Description: "List all previously created incident bundles with their IDs, timestamps, infrastructure keys, and bundle file paths.",
	}, listIncidentsTool)
	if err != nil {
		return nil, err
	}

	return []tool.Tool{bundleTool, listTool}, nil
}
