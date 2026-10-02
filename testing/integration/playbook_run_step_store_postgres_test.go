//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestPlaybookRunStepStore_Migrate_Postgres verifies
// playbook_run_step_store's schema and migrations are idempotent on
// Postgres, and a step written on the first open is durable and readable
// after a fresh second open.
func TestPlaybookRunStepStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	const runID = "plr_pg_migrate_test"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewPlaybookRunStepStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewPlaybookRunStepStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			now := time.Now().UTC()
			step := &audit.PlaybookRunStep{
				RunID:     runID,
				StepIndex: 0,
				Agent:     "postgres_database_agent",
				Tool:      "check_connection",
				Status:    "proposed",
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := s.CreateStep(ctx, step); err != nil {
				t.Fatalf("CreateStep: %v", err)
			}
			return
		}

		steps, err := s.ListSteps(ctx, runID)
		if err != nil {
			t.Fatalf("ListSteps: %v", err)
		}
		if len(steps) != 1 {
			t.Fatalf("ListSteps returned %d steps, want 1", len(steps))
		}
		if steps[0].Tool != "check_connection" {
			t.Errorf("Tool = %q, want check_connection", steps[0].Tool)
		}
	})
}
