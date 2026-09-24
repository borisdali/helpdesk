//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestFleetStore_Migrate_Postgres verifies fleet_store's schema and
// migrations are idempotent on Postgres, and a job written on the first
// open is durable and readable after a fresh second open.
func TestFleetStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	jobID := "flj_pg_migrate_test"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewFleetStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewFleetStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			now := time.Now().UTC()
			job := &audit.FleetJob{
				JobID:       jobID,
				Name:        "migrate-test",
				SubmittedBy: "migrate-test",
				SubmittedAt: now,
				Status:      "pending",
				JobDef:      `{}`,
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			if err := s.CreateJob(ctx, job); err != nil {
				t.Fatalf("CreateJob: %v", err)
			}
			return
		}

		got, err := s.GetJob(ctx, jobID)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if got.Status != "pending" {
			t.Errorf("Status = %q, want pending", got.Status)
		}
	})
}
