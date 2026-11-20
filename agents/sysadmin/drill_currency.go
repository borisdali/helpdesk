package main

// How recently the server's restore was drilled (v0.36), for the plan and the
// backup posture. The assessment is internal/availability's; this resolves the
// database, its reminder and what is known about the server, and reads the drills.

import (
	"context"
	"time"

	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
)

// drillCurrencyFn reads the drills and assesses them: a variable so a test can
// say what the history holds.
var drillCurrencyFn = func(ctx context.Context, key string, reminder infra.DrillReminder, in availability.DrillInputs, now time.Time) *availability.DrillCurrency {
	return ledgerClient.DrillCurrency(ctx, key, reminder, in, now)
}

// drillCurrencyOf is the reminder for the database a call targets, or nil when
// there is nothing to say: no such database, no objective to attach a reminder
// to, or no audit service (the budget check already says the history is not
// available, and a second sentence about it would add nothing).
func drillCurrencyOf(ctx context.Context, target string) *availability.DrillCurrency {
	infraConfigMu.RLock()
	var (
		res   infra.ResolvedObjectives
		key   string
		found bool
	)
	if infraConfig != nil {
		if k, _, err := lookupServer(infraConfig, target); err == nil {
			res, found = infraConfig.ResolveObjectives(k)
			key = k
		}
	}
	infraConfigMu.RUnlock()
	if !found {
		return nil
	}
	return drillCurrencyAt(ctx, key, res.DrillReminder(), target, restoreClock())
}

// drillCurrencyAt assesses the drills of the database with this key. The
// source's system identifier is read only if there is a verified drill to tie to
// it (see availability.DrillInputs).
func drillCurrencyAt(ctx context.Context, key string, reminder infra.DrillReminder, target string, now time.Time) *availability.DrillCurrency {
	if ledgerClient == nil {
		return nil
	}
	return drillCurrencyFn(ctx, key, reminder, availability.DrillInputs{
		ReadSystemID: func() string { return sourceSystemIDFn(ctx, target) },
	}, now)
}
