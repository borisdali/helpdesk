//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"helpdesk/internal/audit"
)

// postgresTestDSN returns a DSN for a fresh, empty database created for this
// test on the server POSTGRES_TEST_DSN points at, and drops it when the test
// ends. It skips the test when POSTGRES_TEST_DSN isn't set — the convention
// this file's Postgres-backend tests have used since the first one was
// written. `make integration`/`make integration-nocache` set it to the
// dedicated postgres-auditd service in testing/docker/docker-compose.yaml
// (never the fault-injection target).
//
// A database per test, not the DSN's own database: the tests write records
// with fixed IDs into tables with unique keys, and that server outlives a
// single run (a developer's local auditd uses the same one). Sharing one
// database made every Postgres test after the first run fail with
// "duplicate key value violates unique constraint" (found 2026-10-16).
func postgresTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set — skipping Postgres-backend test")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatalf("POSTGRES_TEST_DSN must be a postgres:// URL, got %q", dsn)
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("opening POSTGRES_TEST_DSN: %v", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := fmt.Sprintf("auditd_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("creating test database %s: %v", name, err)
	}
	t.Cleanup(func() {
		admin, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Logf("cleanup: opening POSTGRES_TEST_DSN: %v", err)
			return
		}
		defer admin.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// FORCE (PostgreSQL 13+) ends any connection a store left open.
		if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("cleanup: dropping test database %s: %v", name, err)
		}
	})

	u.Path = "/" + name
	return strings.TrimSpace(u.String())
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
