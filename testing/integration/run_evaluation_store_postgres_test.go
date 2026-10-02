//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestRunEvaluationStore_Migrate_Postgres verifies run_evaluation_store's
// schema and migrations are idempotent on Postgres. Regression test for the
// migration-tolerance bug found live during v0.30 Postgres-backend
// validation: the column-add loop only recognized SQLite's "duplicate
// column" error string, so a column already present in the base schema
// broke every startup against Postgres.
func TestRunEvaluationStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	const runID = "plr_pg_migrate_test"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewRunEvaluationStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewRunEvaluationStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			eval := &audit.RunEvaluation{
				RunID:          runID,
				FailureID:      "db-max-connections",
				FailureName:    "migrate-test",
				KeywordScore:   1,
				ToolScore:      1,
				DiagnosisScore: 1,
				OverallScore:   1,
				Passed:         true,
				CreatedAt:      time.Now().UTC(),
			}
			if err := s.Upsert(ctx, eval); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			return
		}

		got, err := s.GetByRunID(ctx, runID)
		if err != nil {
			t.Fatalf("GetByRunID: %v", err)
		}
		if !got.Passed {
			t.Error("Passed = false, want true")
		}
	})
}
