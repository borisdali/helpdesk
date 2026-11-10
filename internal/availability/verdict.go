package availability

// The availability budget set against a restore about to be approved (v0.35).
//
// A restore stops the database, or is an outage that is already under way, and
// the SLO allows only so much downtime in its window. This says whether what is
// left of the budget covers the restore, and what the person approving it must
// then do. It mirrors the RPO check: the same verdict names where they mean the
// same, the same shape of gate, and the same rule that a configuration problem
// asks and never refuses, so it cannot trap an operator in an outage.
//
// How long the restore will take is not known. Until there is an estimate from
// history, the declared rto stands in for it, as an upper bound. So a restore
// that does not fit in what is left only MAY exceed the budget (a restore can
// beat its rto), and that never refuses; only a budget that is already spent is
// a certain breach.

import (
	"fmt"
	"strings"
	"time"

	"helpdesk/internal/infra"
)

// Budget verdict statuses.
const (
	// BudgetWithin: what is left of the budget covers a restore of up to the rto.
	BudgetWithin = "within"
	// BudgetExceeds: the budget is already spent, so any further downtime breaches
	// the SLO.
	BudgetExceeds = "exceeds"
	// BudgetMayExceed: some budget is left, but less than the rto, so the restore
	// may use more than that.
	BudgetMayExceed = "may_exceed"
	// BudgetNotComparable: there is budget left but no rto to say how long the
	// restore may take.
	BudgetNotComparable = "not_comparable"
	// BudgetNoObjective: no availability SLO is set, or the target is not a
	// configured database.
	BudgetNoObjective = "no_objective"
	// BudgetObjectiveInvalid: the config tried to set the SLO or its window and
	// got it wrong.
	BudgetObjectiveInvalid = "objective_invalid"
	// BudgetLedgerUnavailable: the budget is known, but how much of it has been
	// spent could not be read.
	BudgetLedgerUnavailable = "ledger_unavailable"
)

// What a restore demands of the approver on a breach.
const (
	GateAcknowledge = "acknowledge"
	GateRefuse      = "refuse"
)

// BudgetInputs is what a verdict is made from.
type BudgetInputs struct {
	// Found is false when the target is not a configured database.
	Found  bool
	Budget infra.AvailabilityBudget
	// Invalid says the SLO or its window could not be read; Problems says why.
	Invalid  bool
	Problems []string
	// Ledger is what has been spent; LedgerErr says why it is absent.
	Ledger    *Ledger
	LedgerErr error
	// Enforcement is the effective availability enforcement: warn, acknowledge
	// or refuse.
	Enforcement string
	// Serving says the instance is up and the restore would be what takes it down.
	// Refusing is only coherent then: an instance that is already down is
	// accruing downtime whether or not it is restored.
	Serving bool
	// Estimate is how long this server's restores have taken, from history, or
	// nil. When it is an estimate (enough samples) the restore is taken to last
	// as long as the estimate plans for (its p90 from ten samples, its longest
	// from three to nine); without one, as long as the declared rto, as an upper
	// bound.
	Estimate *RecoveryEstimate
}

// What a verdict took a restore's duration to be.
const (
	// DurationFromHistoryP90: the p90 of this server's past restores (ten or more).
	DurationFromHistoryP90 = "history_p90"
	// DurationFromHistoryLongest: the longest of this server's past restores
	// (three to nine, too few for a p90 to mean more than that).
	DurationFromHistoryLongest = "history_longest"
	// DurationFromRTO: the declared rto, an upper bound.
	DurationFromRTO = "declared_rto"
)

// BudgetVerdict is the budget set against the restore.
type BudgetVerdict struct {
	Status string `json:"status"`
	// Detail is the comparison in a sentence a person can read at the gate.
	Detail string `json:"detail"`
	// BudgetSeconds, RemainingSeconds and RTOSeconds are the numbers it rests on.
	// RemainingSeconds is negative when the budget is overspent. The worst case
	// also takes off outages whose real length is unknown.
	BudgetSeconds             *int64   `json:"budget_seconds,omitempty"`
	RemainingSeconds          *int64   `json:"remaining_seconds,omitempty"`
	RemainingWorstCaseSeconds *int64   `json:"remaining_worst_case_seconds,omitempty"`
	RTOSeconds                *int64   `json:"rto_seconds,omitempty"`
	WindowSource              string   `json:"window_source,omitempty"`
	Problems                  []string `json:"problems,omitempty"`
	// Enforcement is what the configuration does about a breach.
	Enforcement string `json:"enforcement,omitempty"`
	// Gate is what the restore demands of the approver: "" (nothing beyond the
	// usual), "acknowledge" or "refuse".
	Gate string `json:"gate,omitempty"`
	// GateNote explains a gate that is not what the enforcement alone would say.
	GateNote string `json:"gate_note,omitempty"`
	// DurationSeconds is how long the restore was taken to last, and
	// DurationBasis what that rests on: DurationFromHistoryP90,
	// DurationFromHistoryLongest or DurationFromRTO.
	DurationSeconds *int64 `json:"duration_seconds,omitempty"`
	DurationBasis   string `json:"duration_basis,omitempty"`
	// RecoveryEstimate is how long this server's restores have taken, whatever
	// the verdict: it is worth showing even when no SLO is set.
	RecoveryEstimate *RecoveryEstimate `json:"recovery_estimate,omitempty"`
}

// NeedsAttention says the person approving should be told the restore is, or may
// be, more than the budget allows.
func (v *BudgetVerdict) NeedsAttention() bool {
	return v != nil && (v.Status == BudgetExceeds || v.Status == BudgetMayExceed)
}

// CompareToBudget makes the verdict and the gate it implies.
func CompareToBudget(in BudgetInputs) *BudgetVerdict {
	v := &BudgetVerdict{Enforcement: in.Enforcement, WindowSource: in.Budget.WindowSource, RecoveryEstimate: in.Estimate}
	v.Status, v.Detail = verdict(in, v)
	v.Gate, v.GateNote = gateFor(v.Status, in.Enforcement, in.Serving)
	return v
}

func verdict(in BudgetInputs, v *BudgetVerdict) (status, detail string) {
	switch {
	case !in.Found:
		return BudgetNoObjective, "the target is not a configured database, so there is no availability budget to compare with"
	case in.Invalid:
		v.Problems = in.Problems
		return BudgetObjectiveInvalid, "the availability objective could not be used (" + strings.Join(in.Problems, "; ") +
			"): fix infrastructure.json, and treat the downtime this restore causes as unchecked"
	case !in.Budget.Computed:
		return BudgetNoObjective, "no availability SLO is set for this database, its environment or the fleet: the downtime this restore causes is unchecked"
	}
	allowed := int64(in.Budget.Allowed.Seconds())
	v.BudgetSeconds = &allowed
	if in.Budget.RTOKnown {
		r := int64(in.Budget.RTO.Seconds())
		v.RTOSeconds = &r
	}
	l := in.Ledger
	if l == nil || l.RemainingSeconds == nil {
		reason := "it was not read"
		if in.LedgerErr != nil {
			reason = in.LedgerErr.Error()
		}
		return BudgetLedgerUnavailable, fmt.Sprintf("the budget is %s per window, but how much of it has been spent could not be read (%s): treat the downtime this restore causes as unchecked",
			infra.FormatSpan(in.Budget.Allowed), reason)
	}
	v.RemainingSeconds, v.RemainingWorstCaseSeconds = l.RemainingSeconds, l.RemainingWorstCaseSeconds
	rem := *l.RemainingSeconds
	span := func(s int64) string {
		if s < 0 {
			return "-" + infra.FormatSpan(secondsOf(-s))
		}
		return infra.FormatSpan(secondsOf(s))
	}
	spent := fmt.Sprintf("%s of the %s budget over %s already spent", span(l.ChargedSeconds), span(allowed), infra.FormatSpan(secondsOf(l.WindowSeconds)))
	note := ""
	if len(l.Unconfirmed) > 0 && l.RemainingWorstCaseSeconds != nil {
		note = fmt.Sprintf("; %d outage(s) of unknown length are not counted, and if they count in full %s would be left",
			len(l.Unconfirmed), span(*l.RemainingWorstCaseSeconds))
	}

	// How long the restore is taken to last: what this server's restores have
	// taken when there is enough history (what the estimate plans for: the p90 from
	// ten samples, the longest from three to nine, with the sample size said), and
	// otherwise the declared rto, as an upper bound.
	var dur time.Duration
	haveDur, fromHistory, historyIsP90 := false, false, false
	if e := in.Estimate; e != nil && e.Status == EstimateEstimated && e.PlanningSeconds != nil {
		dur, haveDur, fromHistory = secondsOf(*e.PlanningSeconds), true, true
		d, basis := *e.PlanningSeconds, DurationFromHistoryLongest
		if e.PlanningBasis == PlanningP90 {
			basis, historyIsP90 = DurationFromHistoryP90, true
		}
		v.DurationSeconds, v.DurationBasis = &d, basis
	} else if in.Budget.RTOKnown {
		dur, haveDur = in.Budget.RTO, true
		d, basis := int64(in.Budget.RTO.Seconds()), DurationFromRTO
		v.DurationSeconds, v.DurationBasis = &d, basis
	}

	switch {
	case rem <= 0:
		if allowed == 0 {
			return BudgetExceeds, "the SLO allows no downtime, so any restore breaches it" + note
		}
		return BudgetExceeds, fmt.Sprintf("%s: the budget is spent (%s left), so any further downtime breaches the SLO%s", spent, span(rem), note)
	case !haveDur:
		return BudgetNotComparable, fmt.Sprintf("%s, %s left, but no rto is declared and there is no history of past restores, so how long this restore may take is not known%s", spent, span(rem), note)
	case fromHistory && rem >= int64(dur.Seconds()):
		return BudgetWithin, fmt.Sprintf("%s, %s left: enough for a restore as long as this server's have been (%s)%s",
			spent, span(rem), historyPhrase(infra.FormatSpan(dur), in.Estimate.Samples, historyIsP90), note)
	case fromHistory:
		return BudgetMayExceed, fmt.Sprintf("%s, only %s left, and this server's restores have taken up to %s: this one may use more than is left%s",
			spent, span(rem), historyPhrase(infra.FormatSpan(dur), in.Estimate.Samples, historyIsP90), note)
	case rem >= int64(dur.Seconds()):
		return BudgetWithin, fmt.Sprintf("%s, %s left: enough for a restore that takes up to the rto of %s%s", spent, span(rem), infra.FormatSpan(dur), note)
	default:
		return BudgetMayExceed, fmt.Sprintf("%s, only %s left, and this restore may take up to the rto of %s: it may use more than is left (a restore can finish sooner than its rto)%s",
			spent, span(rem), infra.FormatSpan(dur), note)
	}
}

// historyPhrase says what a duration taken from history rests on, as a p90 when
// there are enough samples for one and as the longest when there are not.
func historyPhrase(span string, samples int, p90 bool) string {
	if p90 {
		return fmt.Sprintf("%s at the p90 of %d past restores", span, samples)
	}
	return fmt.Sprintf("%s, the longest of %d past restores", span, samples)
}

// gateFor is what a verdict demands under an enforcement.
//
// A budget that is already spent is acknowledged or refused as configured. One
// that only MAY be exceeded cannot be proven a breach, so it is never refused:
// under refuse it needs the acknowledgement. The same goes for an objective that
// could not be read and a spend that could not be read: the check cannot be made,
// which is not the same as there being nothing to check, and a typo or a dead
// audit service must not quietly switch off a strict enforcement. They ask, and
// do not refuse, because nothing is known to be breached.
//
// Refusing is only coherent for an instance that is serving: the restore is then
// what causes the downtime. One that is already down accrues it whether or not it
// is restored, so refusing would leave it down and spend the budget faster. There
// a refuse asks for the acknowledgement instead, and says why.
func gateFor(status, enforcement string, serving bool) (gate, note string) {
	asks := enforcement == infra.EnforceAcknowledge || enforcement == infra.EnforceRefuse
	switch status {
	case BudgetObjectiveInvalid, BudgetLedgerUnavailable, BudgetMayExceed:
		if asks {
			return GateAcknowledge, ""
		}
	case BudgetExceeds:
		switch enforcement {
		case infra.EnforceAcknowledge:
			return GateAcknowledge, ""
		case infra.EnforceRefuse:
			if serving {
				return GateRefuse, ""
			}
			return GateAcknowledge, "availability enforcement is refuse, but the instance is already down, so refusing would leave it down and spend the budget faster: acknowledgement is asked instead"
		}
	}
	return "", ""
}

func secondsOf(s int64) time.Duration { return time.Duration(s) * time.Second }
