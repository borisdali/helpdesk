//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestAuditStore_ToolExecutionDuration_Postgres is a regression test for a
// real bug (found live, 2026-10-11): ToolExecution.Duration/Outcome.Duration
// used to be plain time.Duration fields tagged json:"duration_ms" — Go's
// default JSON encoding of time.Duration is its raw nanosecond count, so a
// real ~5.77s tool call (create_incident_bundle) produced duration_ms=
// 5774637750, which overflowed the outcome_duration_ms column — INTEGER
// (Postgres int4, max ~2.1 billion) — with a real "5774637750 is greater
// than maximum value for int4" error from the live Postgres-backed auditd.
// The fix (Milliseconds type, internal/audit/event.go) already has unit
// tests proving the JSON shape is correct, but nothing exercised the actual
// INSERT against a real Postgres int4 column — this test closes that gap by
// writing the exact real duration through Store.Record/RecordOutcome
// against a real Postgres backend and reading it back.
func TestAuditStore_ToolExecutionDuration_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)

	store, err := audit.NewStore(audit.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// The exact real duration from the live incident that overflowed int4.
	realDuration := audit.MillisecondsFromDuration(5774637750 * time.Nanosecond)

	eventID := "evt_pg_duration_test"
	event := &audit.Event{
		EventID:   eventID,
		Timestamp: time.Now().UTC(),
		EventType: audit.EventTypeOutcome,
		Session:   audit.Session{ID: "sess_pg_duration_test", UserID: "testuser"},
		Outcome:   &audit.Outcome{Status: "success", Duration: realDuration},
	}
	if err := store.Record(ctx, event); err != nil {
		t.Fatalf("Record with a real ~5.77s duration: %v (int4 overflow regression)", err)
	}

	// RecordOutcome is a second, independent write path (UPDATE rather than
	// INSERT) onto the same outcome_duration_ms column — exercise it too.
	if err := store.RecordOutcome(ctx, eventID, &audit.Outcome{Status: "success", Duration: realDuration}); err != nil {
		t.Fatalf("RecordOutcome with a real ~5.77s duration: %v (int4 overflow regression)", err)
	}

	got, err := store.Query(ctx, audit.QueryOptions{EventID: eventID})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Query returned %d events, want 1", len(got))
	}
	if got[0].Outcome == nil {
		t.Fatal("Outcome = nil, want the recorded outcome")
	}
	if got[0].Outcome.Duration != realDuration {
		t.Errorf("Outcome.Duration = %d, want %d (ms, not raw nanoseconds)", got[0].Outcome.Duration, realDuration)
	}
}
