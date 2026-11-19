//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// The drill table and its hash-covered event on the Postgres backend: the
// column types (BIGINT, TEXT timestamps), the COALESCE in the age filter and the
// primary key all behave as on SQLite, and the event still verifies after it has
// been stored and read back.
func TestRestoreDrills_OnPostgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store, err := audit.NewStore(audit.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	ds, err := audit.NewRestoreDrillStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewRestoreDrillStore: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	mk := func(source string, age time.Duration) audit.DrillRecord {
		return audit.DrillRecord{
			SourceKey: source, CloneKey: source + "-clone",
			SourceSystemID: "7001", CloneSystemID: "7001",
			Method: audit.DrillMethodPgBackRest, Status: audit.DrillVerified,
			StartedAt: now.Add(-age - time.Minute), FinishedAt: now.Add(-age),
			DurationSeconds: 60, DurationSource: audit.DrillDurationReported,
			RestoreBytes: 5 << 30, Ticket: "INC-5",
			Checks: []audit.DrillCheck{{Name: "same_system", Result: "pass"}},
		}
	}
	recent, err := ds.Save(mk("prod-db", 2*24*time.Hour))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := ds.Save(mk("prod-db", 100*24*time.Hour)); err != nil {
		t.Fatalf("Save old: %v", err)
	}

	got, err := ds.Get(recent.DrillID)
	if err != nil || got.RestoreBytes != 5<<30 || !got.FinishedAt.Equal(recent.FinishedAt) ||
		got.Ticket != "INC-5" || len(got.Checks) != 1 {
		t.Fatalf("round trip: %+v (%v)", got, err)
	}
	if _, err := ds.Save(recent); err == nil {
		t.Error("saving the same drill twice must fail on Postgres too")
	}
	within, err := ds.List(audit.DrillQuery{SourceKey: "prod-db", Since: now.Add(-90 * 24 * time.Hour)})
	if err != nil || len(within) != 1 || within[0].DrillID != recent.DrillID {
		t.Fatalf("90 day window: %d rows (%v)", len(within), err)
	}
	bySystem, err := ds.List(audit.DrillQuery{SourceSystemID: "7001"})
	if err != nil || len(bySystem) != 2 {
		t.Fatalf("system id filter: %d rows (%v)", len(bySystem), err)
	}

	// The event: stored, read back, still verifies, and tampering is caught.
	c := recent
	c.AuditEventID = ""
	ev := &audit.Event{EventType: audit.EventTypeRestoreDrill, TraceID: recent.DrillID, RestoreDrill: &c}
	if err := store.Record(ctx, ev); err != nil {
		t.Fatalf("Record: %v", err)
	}
	events, err := store.Query(ctx, audit.QueryOptions{EventType: audit.EventTypeRestoreDrill})
	if err != nil || len(events) != 1 {
		t.Fatalf("restore_drill events: %d (%v)", len(events), err)
	}
	if !audit.VerifyEventHash(&events[0]) {
		t.Error("the event does not verify after a round trip through Postgres")
	}
	events[0].RestoreDrill.DurationSeconds = 1
	if audit.VerifyEventHash(&events[0]) {
		t.Error("editing the recorded duration did not break the hash")
	}
}
