package main

import (
	"testing"

	"helpdesk/internal/availability"
)

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
