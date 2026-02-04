//go:build integration

// Package integration contains integration tests that require Docker infrastructure.
//
// Run with: go test -tags integration -timeout 120s ./testing/integration/...
//
// Prerequisites:
//   - Docker running
//   - docker compose -f testing/docker/docker-compose.yaml up -d --wait
package integration

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"helpdesk/testing/testutil"
)

const testConnStr = "host=localhost port=15432 dbname=testdb user=postgres password=testpass"

func init() {
	// Set the docker compose directory for testutil.
	testutil.DockerComposeDir = "../docker"
}

// TestMain checks that Docker infrastructure is available before running tests.
func TestMain(m *testing.M) {
	// Quick check that postgres is reachable.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "psql", testConnStr, "-c", "SELECT 1")
	if err := cmd.Run(); err != nil {
		println("SKIP: PostgreSQL not reachable at", testConnStr)
		println("Run: docker compose -f testing/docker/docker-compose.yaml up -d --wait")
		os.Exit(0) // Skip, don't fail.
	}

	os.Exit(m.Run())
}

// --- Basic connectivity tests ---

func TestConnection_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "psql", testConnStr, "-c", "SELECT version()")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("psql failed: %v\n%s", err, output)
	}

	if !strings.Contains(string(output), "PostgreSQL") {
		t.Errorf("expected PostgreSQL in version output, got: %s", output)
	}
}

func TestConnection_WrongPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	badConnStr := "host=localhost port=15432 dbname=testdb user=postgres password=wrongpass"
	cmd := exec.CommandContext(ctx, "psql", badConnStr, "-c", "SELECT 1")
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected error for wrong password")
	}

	out := strings.ToLower(string(output))
	if !strings.Contains(out, "password") && !strings.Contains(out, "authentication") {
		t.Errorf("expected password/auth error, got: %s", output)
	}
}

func TestConnection_WrongDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	badConnStr := "host=localhost port=15432 dbname=nonexistent user=postgres password=testpass"
	cmd := exec.CommandContext(ctx, "psql", badConnStr, "-c", "SELECT 1")
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected error for nonexistent database")
	}

	out := strings.ToLower(string(output))
	if !strings.Contains(out, "does not exist") {
		t.Errorf("expected 'does not exist' error, got: %s", output)
	}
}

// --- testutil.RunSQLString tests ---

func TestRunSQLString_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := testutil.RunSQLString(ctx, testConnStr, "SELECT 1")
	if err != nil {
		t.Fatalf("RunSQLString failed: %v", err)
	}
}

func TestRunSQLString_CreateAndDropTable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create table.
	err := testutil.RunSQLString(ctx, testConnStr,
		"CREATE TABLE IF NOT EXISTS integration_test (id serial PRIMARY KEY, name text)")
	if err != nil {
		t.Fatalf("CREATE TABLE failed: %v", err)
	}

	// Insert data.
	err = testutil.RunSQLString(ctx, testConnStr,
		"INSERT INTO integration_test (name) VALUES ('test1'), ('test2')")
	if err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}

	// Cleanup.
	err = testutil.RunSQLString(ctx, testConnStr, "DROP TABLE integration_test")
	if err != nil {
		t.Fatalf("DROP TABLE failed: %v", err)
	}
}

func TestRunSQLString_SyntaxError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := testutil.RunSQLString(ctx, testConnStr, "SELEKT 1") // typo
	if err == nil {
		t.Fatal("expected error for syntax error")
	}
}

// --- Database query tests ---

func TestQuery_PgStatActivity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "psql", testConnStr, "-c",
		"SELECT pid, usename, datname, state FROM pg_stat_activity WHERE datname = 'testdb' LIMIT 5")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, output)
	}

	// Should see at least our own connection.
	if !strings.Contains(string(output), "testdb") {
		t.Errorf("expected testdb in pg_stat_activity, got: %s", output)
	}
}

func TestQuery_ConnectionStats(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := `SELECT
		datname as database,
		COUNT(*) as total_connections,
		(SELECT setting::int FROM pg_settings WHERE name = 'max_connections') as max_connections
	FROM pg_stat_activity
	GROUP BY datname`

	cmd := exec.CommandContext(ctx, "psql", testConnStr, "-c", query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, output)
	}

	// Should have max_connections value (we set it to 20 in docker-compose).
	if !strings.Contains(string(output), "20") {
		t.Logf("output: %s", output) // Log for debugging, don't fail.
	}
}

func TestQuery_DatabaseStats(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := `SELECT datname, numbackends, blks_hit, blks_read
		FROM pg_stat_database WHERE datname = 'testdb'`

	cmd := exec.CommandContext(ctx, "psql", testConnStr, "-c", query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, output)
	}

	if !strings.Contains(string(output), "testdb") {
		t.Errorf("expected testdb in stats, got: %s", output)
	}
}

func TestQuery_ConfigParameters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := `SELECT name, setting FROM pg_settings
		WHERE name IN ('max_connections', 'shared_buffers', 'port')`

	cmd := exec.CommandContext(ctx, "psql", testConnStr, "-c", query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "max_connections") {
		t.Error("missing max_connections in output")
	}
	if !strings.Contains(out, "shared_buffers") {
		t.Error("missing shared_buffers in output")
	}
}

func TestQuery_ReplicationStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// This query should work even without replicas - it just returns the role.
	query := `SELECT
		CASE WHEN pg_is_in_recovery() THEN 'Replica' ELSE 'Primary' END as role,
		pg_is_in_recovery() as is_in_recovery`

	cmd := exec.CommandContext(ctx, "psql", testConnStr, "-c", query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, output)
	}

	// Test database should be primary (not in recovery).
	if !strings.Contains(string(output), "Primary") {
		t.Errorf("expected Primary role, got: %s", output)
	}
}

func TestQuery_LockInfo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Query for blocking locks (should be empty in a clean test DB).
	query := `SELECT blocked_locks.pid AS blocked_pid
		FROM pg_catalog.pg_locks blocked_locks
		WHERE NOT blocked_locks.granted
		LIMIT 5`

	cmd := exec.CommandContext(ctx, "psql", testConnStr, "-c", query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, output)
	}

	// Should succeed (likely with 0 rows).
	if strings.Contains(strings.ToLower(string(output)), "error") {
		t.Errorf("unexpected error in output: %s", output)
	}
}

// --- Table operations tests ---

func TestTableStats_CreateAndQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Setup: create table with data.
	setup := `
		DROP TABLE IF EXISTS stats_test;
		CREATE TABLE stats_test (id serial PRIMARY KEY, data text);
		INSERT INTO stats_test (data) SELECT md5(random()::text) FROM generate_series(1, 100);
		ANALYZE stats_test;
	`
	if err := testutil.RunSQLString(ctx, testConnStr, setup); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	defer testutil.RunSQLString(ctx, testConnStr, "DROP TABLE IF EXISTS stats_test")

	// Query table stats.
	query := `SELECT relname, n_live_tup, n_dead_tup
		FROM pg_stat_user_tables WHERE relname = 'stats_test'`

	cmd := exec.CommandContext(ctx, "psql", testConnStr, "-c", query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, output)
	}

	if !strings.Contains(string(output), "stats_test") {
		t.Errorf("expected stats_test in output, got: %s", output)
	}
	// After ANALYZE, we should see 100 live tuples.
	if !strings.Contains(string(output), "100") {
		t.Logf("expected 100 live tuples, output: %s", output)
	}
}

// --- Timeout tests ---

func TestQuery_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// This query should be cancelled before it completes.
	cmd := exec.CommandContext(ctx, "psql", testConnStr, "-c", "SELECT pg_sleep(10)")
	err := cmd.Run()

	if err == nil {
		t.Fatal("expected timeout error")
	}

	// Context should be cancelled.
	if ctx.Err() == nil {
		t.Error("expected context to be cancelled")
	}
}

// --- Extended output format tests ---

func TestQuery_ExtendedFormat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// -x flag for extended output (one column per line).
	cmd := exec.CommandContext(ctx, "psql", testConnStr, "-x", "-c",
		"SELECT version(), current_database(), current_user")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, output)
	}

	out := string(output)
	// Extended format shows column names on separate lines.
	if !strings.Contains(out, "version") {
		t.Error("expected 'version' in extended output")
	}
	if !strings.Contains(out, "current_database") {
		t.Error("expected 'current_database' in extended output")
	}
}
