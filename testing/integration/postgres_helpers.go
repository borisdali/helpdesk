//go:build integration

package integration

import (
	"os"
	"testing"

	"helpdesk/internal/audit"
)

// postgresTestDSN returns the POSTGRES_TEST_DSN env var, skipping the test
// when it isn't set — matches the convention this file's Postgres-backend
// tests have used since the first one was written. Set by `make integration`/
// `make integration-nocache` to the dedicated postgres-auditd service in
// testing/docker/docker-compose.yaml (never the fault-injection target).
func postgresTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set — skipping Postgres-backend test")
	}
	return dsn
}

// openPostgresStoreTwice opens a Store against dsn, runs fn against the
// first open, closes it, reopens fresh, and runs fn again against the
// second open. The second open exercises createSchema()/migrate() a second
// time against a database that already has the full schema — the exact
// condition that caught a real bug during v0.30 Postgres-backend validation
// (a migration step tolerant of SQLite's "duplicate column" error string but
// not Postgres's "already exists" one, breaking every second-and-later
// startup against a real Postgres instance). fn typically writes a record on
// the first call and reads it back on the second, proving both idempotent
// migration and cross-open durability in one pass.
func openPostgresStoreTwice(t *testing.T, dsn string, fn func(t *testing.T, store *audit.Store, pass int)) {
	t.Helper()

	s1, err := audit.NewStore(audit.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore (first open): %v", err)
	}
	fn(t, s1, 1)
	if err := s1.Close(); err != nil {
		t.Fatalf("Close (first open): %v", err)
	}

	s2, err := audit.NewStore(audit.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore (second open — schema/migrate must be idempotent): %v", err)
	}
	defer s2.Close()
	fn(t, s2, 2)
}
