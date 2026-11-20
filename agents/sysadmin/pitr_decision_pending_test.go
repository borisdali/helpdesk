package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/availability"
)

// Under an approval the budget check is told when the incident began: at the
// moment of the restore the ledger no longer shows it as open, but the wait is
// still charged to it when the restore ends.
func TestBudgetCheckFor_UsesTheIncidentStartOfTheApproval(t *testing.T) {
	fixed := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)
	oldClock := restoreClock
	t.Cleanup(func() { restoreClock = oldClock })
	restoreClock = func() time.Time { return fixed }
	withLedger(t, 20*time.Minute, nil) // 23m12s left; the rto of 30m is what the restore is taken to need
	budgetObjectives(t, "", "acknowledge")

	ctx := audit.WithTraceContext(context.Background(), &audit.TraceContext{TraceID: "tr_test", RecoveryApproval: &audit.RecoveryApproval{
		Type: "time", Value: "v", PlanDigest: "d", ApprovedBy: "alice", Acknowledged: true,
		IncidentStartedAt: fixed.Add(-30 * time.Minute)}})
	v := budgetCheckFor(ctx, "pgbackrest_db", true)
	if v.PendingSeconds == nil || *v.PendingSeconds != 1800 {
		t.Fatalf("pending = %v: %s", v.PendingSeconds, v.Detail)
	}
	if l := v.LeftSeconds(); l == nil || *l != 2592-1200-1800 {
		t.Errorf("left = %v", l)
	}
	if v.Status != availability.BudgetExceeds || v.Gate != availability.GateAcknowledge || !strings.Contains(v.Detail, "the incident this restore is for") {
		t.Errorf("verdict %s gate %s: %s", v.Status, v.Gate, v.Detail)
	}

	// The same call with no approval, or one that does not say, is as before.
	if v := budgetCheckFor(context.Background(), "pgbackrest_db", true); v.PendingSeconds != nil {
		t.Errorf("a pending incident from no approval: %v", v.PendingSeconds)
	}
	plain := audit.WithTraceContext(context.Background(), &audit.TraceContext{TraceID: "tr_test", RecoveryApproval: &audit.RecoveryApproval{Type: "time", Value: "v"}})
	if v := budgetCheckFor(plain, "pgbackrest_db", true); v.PendingSeconds != nil {
		t.Errorf("a pending incident from an approval with no start: %v", v.PendingSeconds)
	}
}

// The decision records what the verdict was made on: what was left after the open
// incident that is charged when the restore runs, with that incident's age, so the
// ledger's own figure can be read back as the two added.
func TestPitrOption_RecordsWhatWasLeftAfterTheOpenIncident(t *testing.T) {
	ledger, pending := int64(383), int64(300)
	v := &availability.BudgetVerdict{
		Status: availability.BudgetWithin, RemainingSeconds: &ledger, PendingIncident: "inc_x", PendingSeconds: &pending,
	}
	o := pitrOption(nil, v, true)
	if o.BudgetRemainingSeconds == nil || *o.BudgetRemainingSeconds != 83 {
		t.Errorf("remaining = %v", o.BudgetRemainingSeconds)
	}
	if o.BudgetPendingSeconds == nil || *o.BudgetPendingSeconds != 300 {
		t.Errorf("pending = %v", o.BudgetPendingSeconds)
	}

	// With no such incident it is the ledger's figure, as before.
	v = &availability.BudgetVerdict{Status: availability.BudgetWithin, RemainingSeconds: &ledger}
	o = pitrOption(nil, v, true)
	if o.BudgetRemainingSeconds == nil || *o.BudgetRemainingSeconds != 383 || o.BudgetPendingSeconds != nil {
		t.Errorf("no pending incident: %v %v", o.BudgetRemainingSeconds, o.BudgetPendingSeconds)
	}
}
