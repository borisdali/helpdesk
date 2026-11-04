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
}

// NeedsAttention says the person approving should be told the loss is, or may
// be, more than the objective allows.
func (c *RPOComparison) NeedsAttention() bool {
	return c != nil && (c.Status == RPOExceeds || c.Status == RPOMayExceed)
}

// objectivesFor returns the objectives of the database a tool call targets.
// target is a server ID, or a connection string as resolveHost also accepts.
// found is false when infrastructure config is not loaded or names no such
// database.
func objectivesFor(target string) (infra.ResolvedObjectives, bool) {
	infraConfigMu.RLock()
	ic := infraConfig
	infraConfigMu.RUnlock()
	if ic == nil {
		return infra.ResolvedObjectives{}, false
	}
	target = strings.TrimSpace(target)
	if res, ok := ic.ResolveObjectives(target); ok {
		return res, true
	}
	if _, key, ok := ic.FindDBByConnStr(target); ok {
		return ic.ResolveObjectives(key)
	}
	return infra.ResolvedObjectives{}, false
}

// compareToRPO sets est against obj. found is false when obj is not a real
// database's objectives.
func compareToRPO(est *DataLossEstimate, obj infra.ResolvedObjectives, found bool) *RPOComparison {
	cmp := &RPOComparison{Environment: obj.Environment}
	switch {
	case !found:
		cmp.Status = RPONoObjective
		cmp.Detail = "the target is not a configured database, so there is no recovery point objective to compare with"
		return cmp
	case !obj.RPO.Set && len(obj.Problems) > 0:
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
		cmp.Status = RPONotComparable
		cmp.Detail = "the loss has no time span until the WAL is read (the target is an LSN), so it cannot be compared with " + against
		if est != nil && est.WALBytes != nil {
			cmp.Detail += fmt.Sprintf("; %s of WAL would be discarded", humanBytes(*est.WALBytes))
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
