package main

// A data-loss estimate set against the database's recovery point objective
// (v0.34). The estimate says how much a restore throws away; this says whether
// that is more than the database is supposed to lose.
//
// It is a flag, not a refusal: a point-in-time restore loses data by choice,
// and the person at the gate may well accept more than the objective. What it
// must not do is let them accept it without being told, so every case says
// something, including "no objective" and "cannot compare".

import (
	"fmt"
	"strings"

	"helpdesk/internal/infra"
)

// RPO comparison statuses.
const (
	// RPOWithin: the loss is within the objective (for an upper-bound estimate,
	// even at that bound).
	RPOWithin = "within"
	// RPOExceeds: the loss is more than the objective.
	RPOExceeds = "exceeds"
	// RPOMayExceed: the estimate is an upper bound and the bound is more than
	// the objective, so the real loss may or may not be.
	RPOMayExceed = "may_exceed"
	// RPONotComparable: the estimate has no time span (an LSN target).
	RPONotComparable = "not_comparable"
	// RPONoObjective: the database has no rpo, or is not a configured database.
	RPONoObjective = "no_objective"
	// RPOObjectiveInvalid: the config tried to set an rpo and got it wrong.
	RPOObjectiveInvalid = "objective_invalid"
)

// RPOComparison is the data-loss estimate set against the objective.
type RPOComparison struct {
	Status string `json:"status"`
	// RPOSeconds and Source are the objective compared with, and where it came
	// from (database, environment:<name>, default).
	RPOSeconds *int64 `json:"rpo_seconds,omitempty"`
	Source     string `json:"source,omitempty"`
	// Environment is the database's environment, when it has one.
	Environment string `json:"environment,omitempty"`
	// Detail is the comparison in one sentence a person can read at the gate.
	Detail string `json:"detail"`
	// Problems says why the objective could not be used (invalid status).
	Problems []string `json:"problems,omitempty"`
	// Enforcement is what the database's configuration does about a breach:
	// warn, acknowledge or refuse.
	Enforcement string `json:"enforcement,omitempty"`
	// Gate is what the restore will demand of the person approving, given this
	// verdict and that enforcement: "" (nothing beyond the usual), "acknowledge"
	// (acknowledge_objective_breach) or "refuse" (the restore will not run).
	Gate string `json:"gate,omitempty"`
}

// What a restore demands of the approver when the loss breaches the objective.
const (
	GateAcknowledge = "acknowledge"
	GateRefuse      = "refuse"
)

// gateFor is what a verdict demands under an enforcement.
//
// A loss that exceeds the objective is acknowledged or refused as configured.
// One that only MAY exceed it (an upper bound on a down instance) cannot be
// proven a breach, so it is never refused: under refuse it needs the
// acknowledgement. The same goes for an objective that could not be read: the
// check cannot be made, which is not the same as there being nothing to check,
// and a typo must not quietly switch off a strict enforcement. It asks, and
// does not refuse, because nothing is known to be breached. Verdicts with
// nothing to compare (no objective at all, an LSN target) are shown, not
// enforced.
func gateFor(status, enforcement string) string {
	switch status {
	case RPOObjectiveInvalid:
		if enforcement == infra.EnforceAcknowledge || enforcement == infra.EnforceRefuse {
			return GateAcknowledge
		}
	case RPOExceeds:
		switch enforcement {
		case infra.EnforceAcknowledge:
			return GateAcknowledge
		case infra.EnforceRefuse:
			return GateRefuse
		}
	case RPOMayExceed:
		if enforcement == infra.EnforceAcknowledge || enforcement == infra.EnforceRefuse {
			return GateAcknowledge
		}
	}
	return ""
}

// objectiveGate is the check the restore applies, with the approval in hand: nil
// when it may go ahead, else why not.
func (c *RPOComparison) objectiveGate(acknowledged bool) error {
	switch {
	case c == nil || c.Gate == "":
		return nil
	case c.Gate == GateRefuse:
		return fmt.Errorf("the data this restore discards exceeds the recovery objective (%s), and this database's enforcement is %q: the restore is refused. "+
			"Choose a later target, or change the configured enforcement if the objective is not meant to bind", c.Detail, c.Enforcement)
	case !acknowledged:
		what := "exceeds the recovery objective"
		switch c.Status {
		case RPOMayExceed:
			what = "may exceed the recovery objective"
		case RPOObjectiveInvalid:
			what = "cannot be checked against the recovery objective, which could not be read"
		}
		return fmt.Errorf("the data this restore discards %s (%s), and this database's enforcement is %q: "+
			"approve the gate again with acknowledge_objective_breach (helpdesk-client --acknowledge-objective-breach) to accept that", what, c.Detail, c.Enforcement)
	}
	return nil
}

// NeedsAttention says the person approving should be told the loss is, or may
// be, more than the objective allows.
func (c *RPOComparison) NeedsAttention() bool {
	return c != nil && (c.Status == RPOExceeds || c.Status == RPOMayExceed)
}

// objectivesFor returns the objectives of the database a tool call targets.
// target is whatever resolveHost accepts: a server ID, a connection string or a
// container name. It is looked up by lookupServer, the same code that decides
// which database the restore itself runs on, so the objective checked is always
// that database's. found is false when infrastructure config is not loaded or
// names no such database (or more than one: lookupServer refuses to guess).
//
// The lookup and the resolution both happen under the lock: register_infra_db
// can add a database while a plan is being made.
func objectivesFor(target string) (infra.ResolvedObjectives, bool) {
	infraConfigMu.RLock()
	defer infraConfigMu.RUnlock()
	if infraConfig == nil {
		return infra.ResolvedObjectives{}, false
	}
	key, _, err := lookupServer(infraConfig, target)
	if err != nil {
		return infra.ResolvedObjectives{}, false
	}
	return infraConfig.ResolveObjectives(key)
}

// compareToRPO sets est against obj. found is false when obj is not a real
// database's objectives.
func compareToRPO(est *DataLossEstimate, obj infra.ResolvedObjectives, found bool) *RPOComparison {
	cmp := compareToRPOVerdict(est, obj, found)
	if found {
		cmp.Enforcement = obj.EffectiveEnforcement()
		cmp.Gate = gateFor(cmp.Status, cmp.Enforcement)
	}
	return cmp
}

// compareToRPOVerdict is the comparison without what enforcement makes of it.
func compareToRPOVerdict(est *DataLossEstimate, obj infra.ResolvedObjectives, found bool) *RPOComparison {
	cmp := &RPOComparison{Environment: obj.Environment}
	switch {
	case !found:
		cmp.Status = RPONoObjective
		cmp.Detail = "the target is not a configured database, so there is no recovery point objective to compare with"
		return cmp
	case !obj.RPO.Set && obj.RPOInvalid:
		cmp.Status = RPOObjectiveInvalid
		cmp.Problems = obj.Problems
		cmp.Detail = "the recovery point objective could not be used (" + strings.Join(obj.Problems, "; ") + "): fix infrastructure.json, and treat this loss as unchecked"
		return cmp
	case !obj.RPO.Set:
		cmp.Status = RPONoObjective
		cmp.Detail = "no recovery point objective is set for this database, its environment or the fleet: this loss is unchecked"
		return cmp
	}
	rpo := obj.RPO.Seconds()
	cmp.RPOSeconds, cmp.Source = &rpo, obj.RPO.Source
	against := fmt.Sprintf("an RPO of %s (%s)", humanSpan(rpo), obj.RPO.Source)

	if est == nil || est.WindowSeconds == nil {
		// An LSN target has no exact time span, but it can have a lower bound
		// (when its archive segment arrived). A lower bound that already exceeds
		// the rpo PROVES a breach, so it is one, and enforcement applies to it as
		// to any exact one. A lower bound within the rpo proves nothing: the real
		// loss may be more.
		var atLeast int64 = -1
		if est != nil && est.WindowAtLeastSeconds != nil {
			atLeast = *est.WindowAtLeastSeconds
		}
		if atLeast > rpo {
			cmp.Status = RPOExceeds
			cmp.Detail = fmt.Sprintf("at least %s discarded against %s: exceeds it by at least %s (a lower bound, from when the target's WAL segment reached the archive)",
				humanSpan(atLeast), against, humanSpan(atLeast-rpo))
			return cmp
		}
		cmp.Status = RPONotComparable
		cmp.Detail = "the loss has no exact time span until the WAL is read (the target is an LSN), so it cannot be compared with " + against
		if est != nil && est.WALBytes != nil {
			cmp.Detail += fmt.Sprintf("; %s of WAL would be discarded", humanBytes(*est.WALBytes))
		}
		if atLeast >= 0 {
			cmp.Detail += fmt.Sprintf("; at least %s of history is discarded, which is within the objective, but the real loss may be more", humanSpan(atLeast))
		}
		return cmp
	}
	window := *est.WindowSeconds
	switch {
	case window <= rpo && est.UpperBound:
		cmp.Status = RPOWithin
		cmp.Detail = fmt.Sprintf("at most %s discarded, within %s, even at that upper bound", humanSpan(window), against)
	case window <= rpo:
		cmp.Status = RPOWithin
		cmp.Detail = fmt.Sprintf("%s discarded, within %s", humanSpan(window), against)
	case est.UpperBound:
		cmp.Status = RPOMayExceed
		cmp.Detail = fmt.Sprintf("at most %s discarded against %s: the figure is an upper bound (the end of the archive), so the real loss may be less, but it may exceed the objective by up to %s", humanSpan(window), against, humanSpan(window-rpo))
	default:
		cmp.Status = RPOExceeds
		cmp.Detail = fmt.Sprintf("%s discarded against %s: exceeds it by %s", humanSpan(window), against, humanSpan(window-rpo))
	}
	return cmp
}

// withObjective attaches the comparison for target to est. A nil estimate stays
// nil.
func withObjective(est *DataLossEstimate, target string) *DataLossEstimate {
	if est == nil {
		return nil
	}
	obj, found := objectivesFor(target)
	est.Objective = compareToRPO(est, obj, found)
	return est
}
