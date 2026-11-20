package availability

// What waiting costs, on the budget side (v0.36).
//
// The gate prices the restore. It should also say what putting the decision off
// costs, because the person at the gate is deciding between restoring now and
// something else, and the clock does not stop while they do.
//
// How the ledger charges an incident decides the arithmetic. An incident is
// charged from the start of its entry run to the end of its last run (ledger.go),
// and it counts as an outage only once a run that is one has started. For a
// point-in-time restore the triage that waits at the gate is not one (the restore
// is the outage), so while the approval waits the open incident is NOT in what is
// charged, and "what is left" does not shrink. It will be charged in full, from its
// start, the moment the restore runs: the time already spent and any further wait
// both count. So the margin the approver has is what is left, less the age of that
// incident, less the restore's planned duration; the restore must start by "now +
// that". An incident that is already charged as an outage (its entry run is one) is
// already in what is left, and its age is not taken off twice.
//
// Which open incident is the one this restore is for is not known to the plan; it
// is read from the ledger. With exactly one open incident that is not charged yet,
// it is taken to be the one, and the text names it so the approver can check; with
// several it says so and gives only an upper bound.
//
// It is the ledger's clock, not the downtime: an instance that is still serving is
// not down until the restore stops it, which the text says when it matters. It is
// an estimate under the assumptions the verdict was made with, and says so: the
// planned duration is whatever the verdict took the restore to last, from history
// or the declared rto. It is information for the approver, not a deadline imposed
// on them, and the restore decides again at the moment it stops anything.

import (
	"fmt"
	"time"

	"helpdesk/internal/infra"
)

// What the cost of waiting looks like.
const (
	// DelayLatestStart: there is margin, so there is a latest start that stays
	// within the budget.
	DelayLatestStart = "latest_start"
	// DelayNoMargin: what is left is exactly what the restore is taken to need.
	DelayNoMargin = "no_margin"
	// DelayProjectedBreach: the restore is taken to need more than is left, so it
	// is projected to breach the budget whenever it starts. Not a breach yet.
	DelayProjectedBreach = "projected_breach"
	// DelayOverspent: the budget is already spent: a breach that has happened.
	DelayOverspent = "overspent"
	// DelayNotCharging: no open incident is in the ledger, so there is nothing
	// to say the time already spent costs.
	DelayNotCharging = "not_charging"
	// DelayUncertain: several open incidents are not charged yet and which is this
	// one is not known, so the latest start is only an upper bound.
	DelayUncertain = "uncertain"
)

// BudgetDelay is the cost of waiting, set against the budget.
type BudgetDelay struct {
	State string `json:"state"`
	// Detail is the cost of waiting in a sentence a person can read at the gate.
	Detail string `json:"detail"`
	// AsOf is the ledger's clock when this was made: the figures are as old as it.
	AsOf time.Time `json:"as_of"`
	// Charging says an open outage incident is already charged to the ledger, so
	// its time is in what is left.
	Charging bool `json:"charging"`
	// Incident is the open incident that is not charged yet and is taken to be the
	// one this restore is for, with its age. Empty when there is none, or when
	// there are several and which is not known.
	Incident        string `json:"incident,omitempty"`
	IncidentSeconds *int64 `json:"incident_seconds,omitempty"`
	// SlackSeconds is what is left, less the age of that incident, less what the
	// restore is taken to need; negative when it needs more than is left.
	// LatestStart is when the restore must start to stay within the budget (an
	// upper bound when the state is uncertain). Both only with margin.
	SlackSeconds *int64     `json:"slack_seconds,omitempty"`
	LatestStart  *time.Time `json:"latest_start,omitempty"`
	// PlannedSeconds and PlannedBasis are how long the restore was taken to last,
	// and on what (the verdict's own figures).
	PlannedSeconds *int64 `json:"planned_seconds,omitempty"`
	PlannedBasis   string `json:"planned_basis,omitempty"`
}

// openIncidents says whether an open outage incident is already charged to the
// ledger, and otherwise which open incidents are not charged yet: they become an
// outage, and are charged in full, from their start, when the restore runs.
func openIncidents(l *Ledger) (charging bool, pending []Entry) {
	for _, e := range l.Charged {
		if e.Ongoing {
			return true, nil
		}
	}
	for _, list := range [][]Entry{l.None, l.Unclassified, l.Degraded} {
		for _, e := range list {
			if e.Ongoing {
				pending = append(pending, e)
			}
		}
	}
	return false, pending
}

// delayFor says what waiting costs against the budget the verdict was made from.
// It is nil when there is nothing to say: no budget, or a ledger that could not be
// read.
//
// charging and pending are what pendingIncidents said of the ledger and of what the
// caller knew of the restore's own incident.
func delayFor(v *BudgetVerdict, l *Ledger, serving bool, charging bool, pending []Entry) *BudgetDelay {
	if v == nil || l == nil || v.RemainingSeconds == nil {
		return nil
	}
	span := func(s int64) string {
		if s < 0 {
			return "-" + infra.FormatSpan(secondsOf(-s))
		}
		return infra.FormatSpan(secondsOf(s))
	}

	d := &BudgetDelay{AsOf: l.AsOf.UTC(), Charging: charging}
	rem := *v.RemainingSeconds
	// What the time already spent on the open incident does to the margin, said
	// once, whichever state.
	var context, grows string
	switch {
	case charging:
		grows = "every further minute the open incident stays open adds a minute to it"
	case len(pending) == 1:
		e := pending[0]
		age := e.DurationSeconds
		d.Incident, d.IncidentSeconds = e.IncidentID, &age
		rem -= age
		if e.IncidentID == "" {
			// Told by the caller which incident the restore is for: no assumption to state.
			context = fmt.Sprintf("the incident this restore is for has been open for %s and is not charged to the ledger yet: it is charged in full, from its start, when the restore runs, so that time and any further wait count",
				span(age))
		} else {
			context = fmt.Sprintf("incident %s has been open for %s and is not charged to the ledger yet: it is charged in full, from its start, when the restore runs, so that time and any further wait count (assuming it is the incident this restore is for)",
				e.IncidentID, span(age))
		}
		grows = "every further minute the incident stays open adds a minute to it once it is charged"
	case len(pending) > 1:
		context = fmt.Sprintf("%d open incidents on this database are not charged to the ledger yet, and which of them this restore is for is not known; the one it is for is charged in full, from its start, when the restore runs, so the margin is smaller by its age",
			len(pending))
		grows = "any further wait adds to it"
	default:
		grows = "any further downtime adds to it"
	}
	servingNote := ""
	if serving && (charging || len(pending) > 0) {
		servingNote = " The instance is serving, so the real downtime starts only when the restore stops it; the ledger charges the incident's elapsed time."
	}
	tail := ""
	if context != "" {
		tail += "; " + context
	}
	tail += "." + servingNote

	if rem <= 0 {
		if *v.RemainingSeconds <= 0 {
			// The ledger itself shows the budget spent: a breach that has happened.
			d.State = DelayOverspent
			d.Detail = ledgerOverspent(*v.RemainingSeconds, grows, tail, span)
			return d
		}
		// Not spent in the ledger yet, but the open incident, once charged, takes it
		// past: a forecast, not a breach that has happened.
		d.State = DelayProjectedBreach
		over := "uses it up exactly"
		if rem < 0 {
			over = "overspends it by " + span(-rem)
		}
		d.Detail = fmt.Sprintf("projected budget breach: %s is left, and once the open incident is charged (in full, from its start, when the restore runs) that %s whatever the restore takes; %s%s",
			span(*v.RemainingSeconds), over, grows, tail)
		return d
	}
	if v.DurationSeconds == nil {
		return nil // no planned duration, so no slack to speak of
	}
	planned, basis := *v.DurationSeconds, durationPhrase(v)
	d.PlannedSeconds, d.PlannedBasis = &planned, basis
	slack := rem - planned
	d.SlackSeconds = &slack

	switch {
	case slack < 0:
		d.State = DelayProjectedBreach
		d.Detail = fmt.Sprintf("projected budget breach: %s is left and the restore is taken to need %s (%s), so it is projected to exceed the budget by %s whenever it starts; %s%s",
			span(rem), span(planned), basis, span(-slack), grows, tail)
	case slack == 0:
		d.State = DelayNoMargin
		d.Detail = fmt.Sprintf("no remaining margin: %s is left and the restore is taken to need %s (%s), so it only just fits if it starts now; %s%s",
			span(rem), span(planned), basis, grows, tail)
	case !charging && len(pending) == 0:
		d.State = DelayNotCharging
		d.Detail = fmt.Sprintf("no open incident is in the ledger, so there is no time already spent to count (%s of margin, with a restore taken to need %s). If this restore is run as part of an incident, that incident is charged from its start, and if the instance is down the downtime is accruing regardless",
			span(slack), span(planned))
	case len(pending) > 1:
		d.State = DelayUncertain
		at := l.AsOf.UTC().Add(secondsOf(slack))
		d.LatestStart = &at
		d.Detail = fmt.Sprintf("estimated latest start to remain within budget, at the latest and under current assumptions: %s (in %s), assuming a restore of %s (%s)%s",
			at.Format("15:04:05Z"), span(slack), span(planned), basis, tail)
	default:
		d.State = DelayLatestStart
		at := l.AsOf.UTC().Add(secondsOf(slack))
		d.LatestStart = &at
		d.Detail = fmt.Sprintf("estimated latest start to remain within budget, under current assumptions: %s (in %s), assuming a restore of %s (%s)%s",
			at.Format("15:04:05Z"), span(slack), span(planned), basis, tail)
	}
	return d
}

// ledgerOverspent is the text for a budget the ledger already shows as spent, with
// no open incident to add to it.
func ledgerOverspent(rem int64, grows, tail string, span func(int64) string) string {
	if rem == 0 {
		return "no budget left: the budget is used up, and " + grows + tail
	}
	return fmt.Sprintf("budget already overspent by %s: %s%s", span(-rem), grows, tail)
}

// durationPhrase says what the restore's planned duration rests on.
func durationPhrase(v *BudgetVerdict) string {
	n, drills := 0, 0
	if v.RecoveryEstimate != nil {
		n = v.RecoveryEstimate.Samples
		if v.RecoveryEstimate.Drills != nil {
			drills = v.RecoveryEstimate.Drills.Samples
		}
	}
	switch v.DurationBasis {
	case DurationFromHistoryP90:
		return fmt.Sprintf("the p90 of %d past restores", n)
	case DurationFromHistoryLongest:
		return fmt.Sprintf("the longest of %d past restores", n)
	case DurationFromDrillP90:
		return fmt.Sprintf("the p90 of %d drills on a copy", drills)
	case DurationFromDrillLongest:
		return fmt.Sprintf("the longest of %d drills on a copy", drills)
	}
	return "the declared rto, an upper bound"
}
