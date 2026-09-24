//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestRollbackStore_Migrate_Postgres verifies rollback_store's schema and
// migrations are idempotent on Postgres, and a record written on the first
// open is durable and readable after a fresh second open.
func TestRollbackStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	rollbackID := "rbk_pg_migrate_test"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewRollbackStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewRollbackStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			r := &audit.RollbackRecord{
				RollbackID:      rollbackID,
				OriginalEventID: "evt_pg_migrate_test",
				OriginalTraceID: "tr_pg_migrate_test",
				Status:          "pending_approval",
				InitiatedBy:     "migrate-test",
				InitiatedAt:     time.Now().UTC(),
				RollbackTraceID: "tr_" + rollbackID,
				PlanJSON:        `{}`,
			}
			if err := s.CreateRollback(ctx, r); err != nil {
				t.Fatalf("CreateRollback: %v", err)
			}
			return
		}

		got, err := s.GetRollback(ctx, rollbackID)
		if err != nil {
			t.Fatalf("GetRollback: %v", err)
		}
		if got.Status != "pending_approval" {
			t.Errorf("Status = %q, want pending_approval", got.Status)
		}
	})
}
