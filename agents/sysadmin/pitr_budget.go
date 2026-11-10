package main

// The availability budget at the gate of a point-in-time restore (v0.35).
//
// The restore is set against the downtime budget the database's SLO allows, next
// to the data-loss estimate set against its rpo (pitr_rpo.go). It is evaluated
// here, in the agent that runs the restore, and not at the gateway, because only
// the agent knows whether the instance is serving (a restore of a serving
// instance is what causes the outage; one that is already down accrues it
// anyway), and it already measures again just before it stops anything, so the
// ledger is read at that moment too. The ledger itself is internal/availability,
// the code the gateway's own endpoint uses.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/availability"
	"helpdesk/internal/infra"
)

// ledgerClient reads the incidents the ledger is built from. It is set at
// startup from the agent's audit URL and key; nil means no audit service, and
// the budget then reads as unavailable, not as unspent.
var ledgerClient *availability.Client

// readLedger reads the downtime budget spent by one database's incidents through
// the configured client.
func readLedger(ctx context.Context, key string, budget infra.AvailabilityBudget, now time.Time) (*availability.Ledger, error) {
	return ledgerClient.Ledger(ctx, key, budget, now)
}

// budgetLedgerFn is how the check reads the ledger. A variable so a test can say
// what the ledger holds; production uses readLedger.
var budgetLedgerFn = readLedger

// budgetCheckFor sets what is left of the downtime budget of the database a call
// targets against a restore about to run on it. serving says the instance is up,
// so that the restore is what takes it down.
//
// The objectives are resolved under the config lock and the lock is let go
// before the ledger is read over the network.
func budgetCheckFor(ctx context.Context, target string, serving bool) *availability.BudgetVerdict {
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
		return availability.CompareToBudget(availability.BudgetInputs{Found: false})
	}
	budget := res.Budget()
	in := availability.BudgetInputs{
		Found: true, Budget: budget, Problems: res.Problems,
		Invalid:     res.AvailabilitySLOInvalid || res.AvailabilityWindowInvalid,
		Enforcement: res.EffectiveAvailabilityEnforcement(), Serving: serving,
	}
	// Only a budget that can be computed has anything to read the spend against.
	if budget.Computed && !in.Invalid {
		in.Ledger, in.LedgerErr = budgetLedgerFn(ctx, key, budget, restoreClock())
	}
	return availability.CompareToBudget(in)
}

// budgetGateError is the check a restore applies for the budget, with the
// approval in hand: nil when it may go ahead, else why not.
func budgetGateError(v *availability.BudgetVerdict, acknowledged bool) error {
	switch {
	case v == nil || v.Gate == "":
		return nil
	case v.Gate == availability.GateRefuse:
		return fmt.Errorf("this restore would take a serving instance down when the downtime budget is already spent (%s), and this database's availability enforcement is %q: the restore is refused. "+
			"Wait for incidents to age out of the window, or change availability_enforcement if the SLO is not meant to bind", v.Detail, v.Enforcement)
	case !acknowledged:
		what := "exceeds the availability budget"
		switch v.Status {
		case availability.BudgetMayExceed:
			what = "may exceed the availability budget"
		case availability.BudgetObjectiveInvalid:
			what = "cannot be checked against the availability budget, whose objective could not be read"
		case availability.BudgetLedgerUnavailable:
			what = "cannot be checked against the availability budget, because what has been spent could not be read"
		}
		msg := fmt.Sprintf("the downtime this restore causes %s (%s), and this database's availability enforcement is %q: "+
			"approve the gate again with acknowledge_objective_breach (helpdesk-client --acknowledge-objective-breach) to accept that", what, v.Detail, v.Enforcement)
		if v.GateNote != "" {
			msg += " (" + v.GateNote + ")"
		}
		return errors.New(msg)
	}
	return nil
}

// restoreGate is the check the restore applies at the last point before anything
// is stopped, for both objectives it can breach: the data it discards (the rpo)
// and the downtime it costs (the availability budget). A refusal from either
// stands whatever was acknowledged. Otherwise one acknowledgement covers every
// breach shown at the gate, and when it is missing the error names each one.
func restoreGate(rpo *RPOComparison, budget *availability.BudgetVerdict, acknowledged bool) error {
	switch {
	case rpo != nil && rpo.Gate == GateRefuse:
		return rpo.objectiveGate(acknowledged)
	case budget != nil && budget.Gate == availability.GateRefuse:
		return budgetGateError(budget, acknowledged)
	}
	rpoErr, budgetErr := rpo.objectiveGate(acknowledged), budgetGateError(budget, acknowledged)
	switch {
	case rpoErr != nil && budgetErr != nil:
		return fmt.Errorf("%w. Also: %v", rpoErr, budgetErr)
	case rpoErr != nil:
		return rpoErr
	}
	return budgetErr
}

// breachesToAcknowledge names the objectives whose gate demands the
// acknowledgement for this restore: what an approval that carried it covered.
func breachesToAcknowledge(rpo *RPOComparison, budget *availability.BudgetVerdict) []string {
	var out []string
	if rpo != nil && rpo.Gate == GateAcknowledge {
		out = append(out, audit.BreachRPO)
	}
	if budget != nil && budget.Gate == availability.GateAcknowledge {
		out = append(out, audit.BreachAvailabilityBudget)
	}
	return out
}
