//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestRunFeedbackStore_Migrate_Postgres verifies run_feedback_store's
// schema is created correctly on Postgres (migrate() itself is a deliberate
// no-op on Postgres — see its own isPostgres guard) and a submission
// written on the first open is durable and readable after a fresh second
// open.
func TestRunFeedbackStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	const runID = "plr_pg_migrate_test"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewRunFeedbackStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewRunFeedbackStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			fb := &audit.RunFeedback{
				RunID:        runID,
				FeedbackType: "triage",
				FeedbackTime: "post_incident",
				SeriesID:     "pbs_pg_migrate_test",
				Operator:     "migrate-test",
				SubmittedAt:  time.Now().UTC(),
			}
			if err := s.Submit(ctx, fb); err != nil {
				t.Fatalf("Submit: %v", err)
			}
			return
		}

		got, err := s.GetByRunID(ctx, runID)
		if err != nil {
			t.Fatalf("GetByRunID: %v", err)
		}
		if got.Operator != "migrate-test" {
			t.Errorf("Operator = %q, want migrate-test", got.Operator)
		}
	})
}
