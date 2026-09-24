//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestFaultStabilityStore_Migrate_Postgres verifies fault_stability_store's
// schema and migrations are idempotent on Postgres, and a cert written on
// the first open is durable and readable after a fresh second open.
func TestFaultStabilityStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	const faultID = "db-pg-migrate-test"
	const model = "claude-sonnet-5"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewFaultStabilityStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewFaultStabilityStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			cert := &audit.FaultStabilityCert{
				FaultID:        faultID,
				FaultName:      "migrate-test",
				DiagnosisModel: model,
				NRuns:          3,
				PassRate:       1.0,
				IsStable:       true,
				TestedAt:       time.Now().UTC(),
			}
			if _, err := s.Upsert(ctx, cert); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			return
		}

		got, err := s.GetByFaultAndModel(ctx, faultID, model)
		if err != nil {
			t.Fatalf("GetByFaultAndModel: %v", err)
		}
		if !got.IsStable {
			t.Error("IsStable = false, want true")
		}
	})
}
