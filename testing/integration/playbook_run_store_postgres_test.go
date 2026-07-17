//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestPlaybookRunStore_Migrate_Postgres verifies that the playbook_run_store
// migration is idempotent on a Postgres backend. This exercises the
// containsAny("duplicate column", "already exists") guard fixed in v0.21.0 —
// previously only the SQLite error string was handled.
//
// Skipped unless POSTGRES_TEST_DSN is set to a valid Postgres DSN.
func TestPlaybookRunStore_Migrate_Postgres(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set — skipping Postgres migration test")
	}

	// First open: creates schema + runs migrate().
	s1, err := audit.NewStore(audit.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore (first open): %v", err)
	}
	store1, err := audit.NewPlaybookRunStore(s1.DB(), s1.IsPostgres())
	if err != nil {
		t.Fatalf("NewPlaybookRunStore (first open): %v", err)
	}

	// Insert a run so we confirm the schema is usable.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run := &audit.PlaybookRun{
		PlaybookID:    "pbs_e2e_postgres_migrate_test",
		SeriesID:      "pbs_e2e_postgres_migrate_test",
		ExecutionMode: "fleet",
		Operator:      "migrate-test",
		StartedAt:     time.Now().UTC(),
	}
	if err := store1.Record(ctx, run); err != nil {
		t.Fatalf("Record (first open): %v", err)
	}
	runID := run.RunID
	s1.Close()

	// Second open: migrate() must not error even though all columns already exist.
	s2, err := audit.NewStore(audit.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore (second open): %v", err)
	}
	defer s2.Close()

	store2, err := audit.NewPlaybookRunStore(s2.DB(), s2.IsPostgres())
	if err != nil {
		t.Fatalf("NewPlaybookRunStore (second open — migrate() must be idempotent): %v", err)
	}

	// Confirm the previously inserted run is readable.
	got, err := store2.GetByRunID(ctx, runID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.PlaybookID != run.PlaybookID {
		t.Errorf("PlaybookID: got %q, want %q", got.PlaybookID, run.PlaybookID)
	}
}
