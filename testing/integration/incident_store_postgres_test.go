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

// UNKNOWN never replaces a real root-cause class, decided inside the UPDATE
// (a CASE with a CAST on a bound parameter), so the SQL has to be right on
// Postgres, not only on SQLite: Postgres must be able to infer the parameter
// types and compare them.
func TestIncidentStore_Update_UnknownNeverReplacesARealAttribution_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	store, err := audit.NewStore(audit.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	s, err := audit.NewIncidentStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewIncidentStore: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inc := &audit.Incident{TraceID: "tr_pg_attribution", Origin: audit.IncidentOriginReal, Status: audit.IncidentStatusOpen, DetectedAt: time.Now().UTC()}
	if err := s.Create(ctx, inc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	steps := []struct{ set, want string }{
		{"UNKNOWN", "UNKNOWN"},                               // unset -> UNKNOWN
		{"missing-data-directory", "missing-data-directory"}, // UNKNOWN -> real
		{"UNKNOWN", "missing-data-directory"},                // real survives a later UNKNOWN
		{"checkpoint-corruption", "checkpoint-corruption"},   // real -> another real (a correction)
	}
	for _, st := range steps {
		attribution := st.set
		if err := s.Update(ctx, inc.IncidentID, audit.IncidentUpdate{Attribution: &attribution}); err != nil {
			t.Fatalf("Update(%q): %v", st.set, err)
		}
		got, err := s.GetByID(ctx, inc.IncidentID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got.Attribution != st.want {
			t.Fatalf("after setting %q: attribution = %q, want %q", st.set, got.Attribution, st.want)
		}
	}
}
