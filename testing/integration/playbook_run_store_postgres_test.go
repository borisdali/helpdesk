//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestPlaybookRunStore_Migrate_Postgres verifies that the playbook_run_store
// migration is idempotent on a Postgres backend, and that a record written
// on the first open is durable and readable after a fresh second open. This
// exercises the containsAny("duplicate column", "already exists") guard
// fixed in v0.21.0 — previously only the SQLite error string was handled.
func TestPlaybookRunStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	var runID string

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewPlaybookRunStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewPlaybookRunStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			run := &audit.PlaybookRun{
				PlaybookID:    "pbs_e2e_postgres_migrate_test",
				SeriesID:      "pbs_e2e_postgres_migrate_test",
				ExecutionMode: "fleet",
				Operator:      "migrate-test",
				StartedAt:     time.Now().UTC(),
			}
			if err := s.Record(ctx, run); err != nil {
				t.Fatalf("Record: %v", err)
			}
			runID = run.RunID
			return
		}

		got, err := s.GetByRunID(ctx, runID)
		if err != nil {
			t.Fatalf("GetByRunID: %v", err)
		}
		if got.PlaybookID != "pbs_e2e_postgres_migrate_test" {
			t.Errorf("PlaybookID = %q, want pbs_e2e_postgres_migrate_test", got.PlaybookID)
		}
	})
}
