package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A recovery decision recorded before the ticket column existed is found by its
// ticket once the store has opened the database again.
func TestStore_BackfillsTheTicketOfOlderDecisions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "audit.db")
	ctx := context.Background()

	store, err := NewStore(StoreConfig{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(run, ticket string) {
		t.Helper()
		ev := NewRecoveryDecisionEvent(run, "", RecoveryDecision{Stage: RecoveryStageGate, Decision: RecoveryDecisionApproved, RunID: run,
			Approval: &ApprovalFacts{ApprovedBy: "alice", Ticket: ticket}})
		ev.Timestamp = time.Now().UTC()
		if err := store.Record(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	mk("plr_old1", "INC-7")
	mk("plr_old2", "")

	// Make them look like rows written by the version before the column.
	if _, err := store.DB().Exec(`UPDATE audit_events SET ticket = NULL`); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, err = NewStore(StoreConfig{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Query(ctx, QueryOptions{Ticket: "INC-7"})
	if err != nil || len(events) != 1 || events[0].RecoveryDecision.RunID != "plr_old1" {
		t.Fatalf("after the backfill INC-7 found %d events, %v", len(events), err)
	}
	// A row with no ticket was looked at once and is not NULL any more.
	var nulls int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'recovery_decision' AND ticket IS NULL`).Scan(&nulls); err != nil || nulls != 0 {
		t.Errorf("rows still waiting for the backfill: %d, %v", nulls, err)
	}
	// The hash chain does not depend on the column.
	if status, err := store.VerifyIntegrity(ctx); err != nil || !status.Valid {
		t.Errorf("chain = %+v, %v", status, err)
	}
}
