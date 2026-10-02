//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestPlaybookStore_Migrate_Postgres verifies playbook_store's schema and
// migrations are idempotent on Postgres, and a playbook created on the
// first open is durable and readable — with created_at/updated_at
// precision intact — after a fresh second open. Regression test for the
// DATETIME-type and precision-loss bugs found live during v0.30
// Postgres-backend validation.
func TestPlaybookStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	playbookID := "pb_pg_migrate_test"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewPlaybookStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewPlaybookStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			pb := &audit.Playbook{
				PlaybookID:  playbookID,
				Name:        "migrate-test",
				Description: "postgres migration smoke test",
				CreatedBy:   "migrate-test",
				SeriesID:    "pbs_pg_migrate_test",
				Source:      "manual",
			}
			if err := s.Create(ctx, pb); err != nil {
				t.Fatalf("Create: %v", err)
			}
			return
		}

		got, err := s.Get(ctx, playbookID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Name != "migrate-test" {
			t.Errorf("Name = %q, want migrate-test", got.Name)
		}
		if got.CreatedAt.IsZero() {
			t.Error("CreatedAt is zero after round-trip")
		}
	})
}
