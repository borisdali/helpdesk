//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// A Postgres audit database that was written before the ticket column existed
// gets the column when the store opens it, and its older recovery decisions are
// found by their ticket afterwards. The backfill's UPDATE goes through the
// placeholder rewriting this backend needs, which the SQLite test cannot show.
func TestEventTicket_BackfillOnPostgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store, err := audit.NewStore(audit.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for run, ticket := range map[string]string{"plr_pgold1": "INC-7", "plr_pgold2": ""} {
		ev := audit.NewRecoveryDecisionEvent(run, "", audit.RecoveryDecision{
			Stage: audit.RecoveryStageGate, Decision: audit.RecoveryDecisionApproved, RunID: run,
			Approval: &audit.ApprovalFacts{ApprovedBy: "alice", Ticket: ticket}})
		ev.Timestamp = time.Now().UTC()
		if err := store.Record(ctx, ev); err != nil {
			t.Fatalf("recording %s: %v", run, err)
		}
	}
	// Written by the version before the column: its rows have no value in it at
	// all. Dropping the column and letting the store add it again is the same.
	if _, err := store.DB().ExecContext(ctx, `ALTER TABLE audit_events DROP COLUMN ticket`); err != nil {
		t.Fatalf("dropping the column: %v", err)
	}
	store.Close()

	store, err = audit.NewStore(audit.StoreConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer store.Close()

	events, err := store.Query(ctx, audit.QueryOptions{Ticket: "INC-7"})
	if err != nil || len(events) != 1 || events[0].RecoveryDecision == nil || events[0].RecoveryDecision.RunID != "plr_pgold1" {
		t.Fatalf("after reopening, INC-7 found %d events, %v", len(events), err)
	}
	// Looked at once: the one with no ticket is not NULL any more.
	var nulls int
	if err := store.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE event_type = 'recovery_decision' AND ticket IS NULL`).Scan(&nulls); err != nil || nulls != 0 {
		t.Errorf("rows still waiting for the backfill: %d, %v", nulls, err)
	}
	// The column is not part of the hash.
	if status, err := store.VerifyIntegrity(ctx); err != nil || !status.Valid {
		t.Errorf("chain = %+v, %v", status, err)
	}
}
