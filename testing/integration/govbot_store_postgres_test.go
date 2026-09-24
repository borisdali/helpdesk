//go:build integration

package integration

import (
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestGovbotStore_Migrate_Postgres verifies govbot_store's schema and
// migrations are idempotent on Postgres, and a run written on the first
// open is durable and readable after a fresh second open. Also the
// regression test for the "window" reserved-keyword bug found live during
// v0.30 Postgres-backend validation — SaveRun/RecentRuns both touch the
// (now double-quoted) "window" column.
func TestGovbotStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	const gateway = "gw-pg-migrate-test"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewGovbotStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewGovbotStore (pass %d): %v", pass, err)
		}

		if pass == 1 {
			run := audit.GovbotRun{
				RunAt:   time.Now().UTC(),
				Window:  "1h",
				Gateway: gateway,
				Status:  "healthy",
			}
			if err := s.SaveRun(run); err != nil {
				t.Fatalf("SaveRun: %v", err)
			}
			return
		}

		runs, err := s.RecentRuns("1h", gateway, 10)
		if err != nil {
			t.Fatalf("RecentRuns: %v", err)
		}
		if len(runs) == 0 {
			t.Fatal("RecentRuns returned no rows")
		}
		if runs[0].Window != "1h" {
			t.Errorf("Window = %q, want 1h", runs[0].Window)
		}
	})
}
