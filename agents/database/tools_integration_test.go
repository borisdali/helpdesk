//go:build integration

// Integration tests for session inspection tooling.
// They require a real PostgreSQL instance — start with:
//
//	docker compose -f testing/docker/docker-compose.yaml up -d --wait
//
// Run with:
//
//	go test -tags integration -timeout 60s ./agents/database/...
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const integrationConnStr = "host=localhost port=15432 dbname=testdb user=postgres password=testpass"

// skipIfNoPostgres skips the test when the Docker test database is not reachable.
func skipIfNoPostgres(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "psql", integrationConnStr, "-c", "SELECT 1").Run(); err != nil {
		t.Skip("PostgreSQL not reachable — run: docker compose -f testing/docker/docker-compose.yaml up -d --wait")
	}
}

// runIntegrationSQL executes a SQL string via psql against the test database.
func runIntegrationSQL(ctx context.Context, sql string) error {
	cmd := exec.CommandContext(ctx, "psql", integrationConnStr, "-c", sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql: %v\n%s", err, out)
	}
	return nil
}

// runIntegrationSQLOutput executes SQL and returns the first row/column as a trimmed string.
// Uses psql -t -A (tuples-only, unaligned) for clean scalar output.
func runIntegrationSQLOutput(ctx context.Context, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, "psql", integrationConnStr, "-t", "-A", "-c", sql)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("psql: %v", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// waitForSession polls pg_stat_activity until a session with the given
// application_name appears in state 'active'. Returns the PID or 0 on timeout.
func waitForSession(ctx context.Context, appName string) int {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		pidStr, err := runIntegrationSQLOutput(ctx, fmt.Sprintf(
			"SELECT pid FROM pg_stat_activity WHERE application_name = '%s' AND state = 'active' LIMIT 1",
			appName))
		if err == nil && pidStr != "" {
			if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return 0
}

// TestInspectQuery_AllColumnsPresent verifies the inspectionQuery SQL is valid
// and produces all expected column names in psql -x expanded output.
// This catches column rename bugs and PostgreSQL version incompatibilities.
func TestInspectQuery_AllColumnsPresent(t *testing.T) {
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get our own backend PID so we have a valid PID to query.
	pidStr, err := runIntegrationSQLOutput(ctx, "SELECT pg_backend_pid()")
	if err != nil {
		t.Fatalf("get backend pid: %v", err)
	}
	pid, _ := strconv.Atoi(pidStr)

	// Run the actual inspectionQuery with -x (same flags inspectConnection uses).
	query := fmt.Sprintf(inspectionQuery, pid)
	cmd := exec.CommandContext(ctx, "psql", integrationConnStr, "-x", "-c", query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inspection query failed: %v\n%s", err, out)
	}

	for _, col := range []string{
		"pid", "usename", "datname", "client_addr", "state",
		"state_duration_secs", "has_open_tx", "open_tx_secs",
		"has_writes", "total_locks", "row_locks", "locked_tables",
		"current_query",
	} {
		if !strings.Contains(string(out), col) {
			t.Errorf("column %q not found in expanded output:\n%s", col, out)
		}
	}
}

// TestInspectConnection_NonExistentPID verifies that inspectConnection returns
// a "no session found" error when the given PID has no matching backend.
func TestInspectConnection_NonExistentPID(t *testing.T) {
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := inspectConnection(ctx, integrationConnStr, 2147483647)
	if err == nil {
		t.Fatal("expected error for nonexistent PID, got nil")
	}
	if !strings.Contains(err.Error(), "no session found") {
		t.Errorf("error = %q, want to contain 'no session found'", err.Error())
	}
}

// TestInspectConnection_WriteTransaction holds a write transaction open in a
// background psql process and verifies that inspectConnection correctly reports
// HasWrites=true, an open transaction, and the name of the locked table.
func TestInspectConnection_WriteTransaction(t *testing.T) {
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Set up test table.
	if err := runIntegrationSQL(ctx,
		"CREATE TABLE IF NOT EXISTS iit_inspect_test (id INT PRIMARY KEY, val TEXT);"+
			" INSERT INTO iit_inspect_test VALUES (1, 'initial') ON CONFLICT (id) DO NOTHING;",
	); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer runIntegrationSQL(context.Background(), "DROP TABLE IF EXISTS iit_inspect_test") //nolint:errcheck

	// Start a background psql session holding an uncommitted write transaction.
	// The application_name in the connection string lets us find the PID quickly.
	connWithApp := integrationConnStr + " application_name=iit_inspect_write"
	bgCmd := exec.Command("psql", connWithApp, "-c",
		"BEGIN; UPDATE iit_inspect_test SET val = 'held' WHERE id = 1; SELECT pg_sleep(20);")
	if err := bgCmd.Start(); err != nil {
		t.Fatalf("start background psql: %v", err)
	}
	defer bgCmd.Process.Kill() //nolint:errcheck

	pid := waitForSession(ctx, "iit_inspect_write")
	if pid == 0 {
		t.Fatal("background write session did not appear in pg_stat_activity within 8s")
	}

	plan, err := inspectConnection(ctx, integrationConnStr, pid)
	if err != nil {
		t.Fatalf("inspectConnection: %v", err)
	}
	if plan.PID != pid {
		t.Errorf("PID = %d, want %d", plan.PID, pid)
	}
	if !plan.HasOpenTransaction {
		t.Error("HasOpenTransaction = false, want true")
	}
	if !plan.HasWrites {
		t.Error("HasWrites = false, want true (UPDATE was executed)")
	}
	if plan.TotalLocks == 0 {
		t.Error("TotalLocks = 0, want > 0 (UPDATE acquires a RowExclusiveLock)")
	}
	// The locked table list should include our test table.
	found := false
	for _, tbl := range plan.LockedTables {
		if strings.Contains(tbl, "iit_inspect_test") {
			found = true
		}
	}
	if !found {
		t.Errorf("LockedTables = %v, want to include iit_inspect_test", plan.LockedTables)
	}
	// Rollback estimate is set when HasWrites=true and OpenTxAgeSecs > 0.
	if plan.HasWrites && plan.OpenTxAgeSecs > 0 && plan.RollbackMinSecs == 0 {
		t.Error("RollbackMinSecs = 0, want > 0 for write transaction with open TX age > 0")
	}
}

// TestInspectConnection_ReadOnlyTransaction holds an explicit read-only
// transaction open and verifies that HasWrites=false and no rollback estimate
// is produced (read-only transactions roll back instantly).
func TestInspectConnection_ReadOnlyTransaction(t *testing.T) {
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	connWithApp := integrationConnStr + " application_name=iit_inspect_read"
	bgCmd := exec.Command("psql", connWithApp, "-c",
		"BEGIN; SELECT count(*) FROM pg_stat_activity; SELECT pg_sleep(20);")
	if err := bgCmd.Start(); err != nil {
		t.Fatalf("start background psql: %v", err)
	}
	defer bgCmd.Process.Kill() //nolint:errcheck

	pid := waitForSession(ctx, "iit_inspect_read")
	if pid == 0 {
		t.Fatal("background read session did not appear in pg_stat_activity within 8s")
	}

	plan, err := inspectConnection(ctx, integrationConnStr, pid)
	if err != nil {
		t.Fatalf("inspectConnection: %v", err)
	}
	if !plan.HasOpenTransaction {
		t.Error("HasOpenTransaction = false, want true (BEGIN was issued)")
	}
	if plan.HasWrites {
		t.Error("HasWrites = true, want false for a read-only transaction")
	}
	// No rollback estimate for read-only transactions.
	if plan.RollbackMinSecs != 0 || plan.RollbackMaxSecs != 0 {
		t.Errorf("rollback estimate = [%d, %d], want [0, 0] for read-only",
			plan.RollbackMinSecs, plan.RollbackMaxSecs)
	}
}

// TestGetSessionInfoTool_Integration calls getSessionInfoTool against a real
// write transaction and verifies the formatted plan output end-to-end.
func TestGetSessionInfoTool_Integration(t *testing.T) {
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Set up test table.
	if err := runIntegrationSQL(ctx,
		"CREATE TABLE IF NOT EXISTS iit_tool_test (id INT PRIMARY KEY, val TEXT);"+
			" INSERT INTO iit_tool_test VALUES (1, 'initial') ON CONFLICT (id) DO NOTHING;",
	); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer runIntegrationSQL(context.Background(), "DROP TABLE IF EXISTS iit_tool_test") //nolint:errcheck

	connWithApp := integrationConnStr + " application_name=iit_tool_write"
	bgCmd := exec.Command("psql", connWithApp, "-c",
		"BEGIN; UPDATE iit_tool_test SET val = 'held' WHERE id = 1; SELECT pg_sleep(20);")
	if err := bgCmd.Start(); err != nil {
		t.Fatalf("start background psql: %v", err)
	}
	defer bgCmd.Process.Kill() //nolint:errcheck

	pid := waitForSession(ctx, "iit_tool_write")
	if pid == 0 {
		t.Fatal("background session did not appear in pg_stat_activity within 8s")
	}

	result, err := getSessionInfoTool(newTestContext(), GetSessionInfoArgs{
		ConnectionString: integrationConnStr,
		PID:              pid,
	})
	if err != nil {
		t.Fatalf("getSessionInfoTool: %v", err)
	}
	if strings.Contains(result.Output, "ERROR") {
		t.Fatalf("tool returned an error: %s", result.Output)
	}

	out := result.Output
	if !strings.Contains(out, fmt.Sprintf("Session PID %d", pid)) {
		t.Errorf("output missing PID header: %q", out)
	}
	if !strings.Contains(out, "Has writes:    yes") {
		t.Errorf("output missing 'Has writes: yes': %q", out)
	}
	if !strings.Contains(out, "iit_tool_test") {
		t.Errorf("output missing locked table name iit_tool_test: %q", out)
	}
	if !strings.Contains(out, "Rollback estimate") {
		t.Errorf("output missing rollback estimate section: %q", out)
	}
}

func TestGetStatusSummaryTool_Integration(t *testing.T) {
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := getStatusSummaryTool(newTestContext(), GetStatusSummaryArgs{
		ConnectionString: integrationConnStr,
	})
	if err != nil {
		t.Fatalf("getStatusSummaryTool: %v", err)
	}

	// Output must be valid JSON.
	var summary struct {
		Status        string  `json:"status"`
		Version       string  `json:"version"`
		Uptime        string  `json:"uptime"`
		Connections   int     `json:"connections"`
		MaxConns      int     `json:"max_connections"`
		CacheHitRatio float64 `json:"cache_hit_ratio"`
	}
	if err := json.Unmarshal([]byte(result.Output), &summary); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, result.Output)
	}
	if summary.Status != "ok" {
		t.Errorf("status = %q, want ok", summary.Status)
	}
	if !strings.HasPrefix(summary.Version, "PG ") {
		t.Errorf("version = %q, want PG <major>.<minor>", summary.Version)
	}
	if summary.Uptime == "" {
		t.Error("uptime is empty")
	}
	if summary.MaxConns <= 0 {
		t.Errorf("max_connections = %d, want > 0", summary.MaxConns)
	}
	if summary.CacheHitRatio < 0 || summary.CacheHitRatio > 100 {
		t.Errorf("cache_hit_ratio = %v, want 0-100", summary.CacheHitRatio)
	}

	// Cross-check max_connections against pg_settings.
	want, err := runIntegrationSQLOutput(ctx, "SELECT current_setting('max_connections')")
	if err != nil {
		t.Fatalf("read max_connections: %v", err)
	}
	if wantInt, err := strconv.Atoi(strings.TrimSpace(want)); err == nil {
		if summary.MaxConns != wantInt {
			t.Errorf("max_connections = %d, pg_settings says %d", summary.MaxConns, wantInt)
		}
	}
}

// =============================================================================
// Rollback capability detection
// =============================================================================

// TestDetectRollbackCapability_Integration probes a real PostgreSQL instance
// and verifies that DetectRollbackCapability populates the capability fields
// correctly. The test Docker image runs a plain PG 16 with no wal_level override
// (defaults to "replica"), so auto-detection is expected to yield row_capture.
func TestDetectRollbackCapability_Integration(t *testing.T) {
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Open a *sql.DB directly against the test instance.
	db, err := openIntegrationDB(t)
	if err != nil {
		t.Fatalf("open integration db: %v", err)
	}

	cap, err := DetectRollbackCapability(ctx, db, "public", "")
	if err != nil {
		t.Fatalf("DetectRollbackCapability: %v", err)
	}
	if cap == nil {
		t.Fatal("capability is nil")
	}

	// WALLevel must be populated (we get "replica" from the stock PG 16 image).
	if cap.WALLevel == "" {
		t.Error("WALLevel is empty — SHOW wal_level query did not work against real PG")
	}
	t.Logf("WALLevel = %q  HasReplication = %v  Mode = %q",
		cap.WALLevel, cap.HasReplication, cap.Mode)

	// The standard test image does not have wal_level=logical, so auto-detect
	// must fall back to row_capture (not wal_decode).
	if cap.WALLevel != "logical" && cap.Mode != RollbackModeRowCapture {
		t.Errorf("Mode = %q, want row_capture (wal_level=%q is not logical)", cap.Mode, cap.WALLevel)
	}

	// ReplicaIdentity map must be initialised (not nil), even if it's empty for
	// the standard test DB schema.
	if cap.ReplicaIdentity == nil {
		t.Error("ReplicaIdentity map is nil — pg_class query did not initialise the map")
	}
}

// TestDetectRollbackCapability_Integration_Override verifies that the
// modeOverride parameter takes precedence over auto-detection results from
// the real database.
func TestDetectRollbackCapability_Integration_Override(t *testing.T) {
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := openIntegrationDB(t)
	if err != nil {
		t.Fatalf("open integration db: %v", err)
	}

	for _, tc := range []struct {
		override string
		want     DBRollbackMode
	}{
		{"wal_decode", RollbackModeWALDecode},
		{"row_capture", RollbackModeRowCapture},
		{"none", RollbackModeNone},
	} {
		cap, err := DetectRollbackCapability(ctx, db, "public", tc.override)
		if err != nil {
			t.Fatalf("override=%q: DetectRollbackCapability: %v", tc.override, err)
		}
		if cap.Mode != tc.want {
			t.Errorf("override=%q: Mode = %q, want %q", tc.override, cap.Mode, tc.want)
		}
		// WALLevel must still be populated (DB was queried even when override is set).
		if cap.WALLevel == "" {
			t.Errorf("override=%q: WALLevel is empty — DB was not probed", tc.override)
		}
	}
}

// TestWALBracket_Open_FailsWithoutLogicalWAL verifies that WALBracket.Open
// returns a meaningful error when the target DB does not have wal_level=logical.
// This is the expected behaviour for the standard test instance.
func TestWALBracket_Open_FailsWithoutLogicalWAL(t *testing.T) {
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := openIntegrationDB(t)
	if err != nil {
		t.Fatalf("open integration db: %v", err)
	}

	walLevel, _ := runIntegrationSQLOutput(ctx, "SHOW wal_level")
	if walLevel == "logical" {
		t.Skip("wal_level=logical on this instance — skipping non-logical failure test")
	}

	w := NewWALBracket(db, "integration-test-trace")
	if err := w.Open(ctx); err == nil {
		// Open succeeded — unexpected on a non-logical instance, but clean up.
		_, _, _, _ = w.Close(ctx)
		t.Fatal("WALBracket.Open succeeded on non-logical instance — expected an error")
	} else {
		t.Logf("WALBracket.Open correctly failed on wal_level=%q: %v", walLevel, err)
	}
}

// openIntegrationDB opens a *sql.DB connection to the test PostgreSQL instance
// using the pgx/v5 stdlib driver (registered as "pgx").
func openIntegrationDB(t *testing.T) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("pgx", "host=localhost port=15432 dbname=testdb user=postgres password=testpass sslmode=disable")
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	t.Cleanup(func() { db.Close() })
	return db, nil
}
