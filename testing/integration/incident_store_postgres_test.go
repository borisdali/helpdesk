//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestIncidentStore_Migrate_Postgres verifies incident_store's schema and
// migrations are idempotent on Postgres, and a row written on the first
// open is durable and readable after a fresh second open.
func TestIncidentStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	var incidentID string
	traceID := "tr_pg_migrate_test"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewIncidentStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewIncidentStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			inc := &audit.Incident{
				TraceID:    traceID,
				Origin:     audit.IncidentOriginReal,
				Status:     audit.IncidentStatusOpen,
				DetectedAt: time.Now().UTC(),
			}
			if err := s.Create(ctx, inc); err != nil {
				t.Fatalf("Create: %v", err)
			}
			incidentID = inc.IncidentID
			return
		}

		got, err := s.GetByID(ctx, incidentID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got.TraceID != traceID {
			t.Errorf("TraceID = %q, want %q", got.TraceID, traceID)
		}
		if got.Origin != audit.IncidentOriginReal {
			t.Errorf("Origin = %q, want real", got.Origin)
		}
	})
}
