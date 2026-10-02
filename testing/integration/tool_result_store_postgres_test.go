//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestToolResultStore_Migrate_Postgres verifies tool_result_store's schema
// is idempotent on Postgres, and a result written on the first open is
// durable and readable after a fresh second open. Regression test for the
// DATETIME-type bug and the recorded_at precision fix found live during
// v0.30 Postgres-backend validation.
func TestToolResultStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	const jobID = "job_pg_migrate_test"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewToolResultStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewToolResultStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			r := &audit.PersistedToolResult{
				ServerName: "database",
				ToolName:   "check_connection",
				Output:     "ok",
				JobID:      jobID,
				RecordedAt: time.Now().UTC(),
				Success:    true,
			}
			if err := s.Record(ctx, r); err != nil {
				t.Fatalf("Record: %v", err)
			}
			return
		}

		results, err := s.List(ctx, audit.ToolResultQuery{JobID: jobID})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("List returned %d results, want 1", len(results))
		}
		if !results[0].Success {
			t.Error("Success = false, want true")
		}
	})
}
