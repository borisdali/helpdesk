package availability

// What waiting costs, on the budget side (v0.36).
//
// The gate prices the restore. It should also say what putting the decision off
// costs, because the person at the gate is deciding between restoring now and
// something else, and the clock does not stop while they do.
//
// The ledger charges an open outage incident up to now (ledger.go: an incident
// that is still open is charged as ongoing), so what is left of the budget at the
// gate shrinks second for second while the approval waits. That is the ledger's
// clock, not the downtime: an instance that is still serving is not down until
// the restore stops it, which the text says when it matters. Under that clock the
// arithmetic is simple. The restore is planned to take D after it starts, so it
// fits if it starts no later than "now + (left - D)": the latest start.
//
// It is an estimate under the assumptions the verdict was made with, and it says
// so: D is whatever the verdict took the restore to last, from history or the
// declared rto, and the wording gives both. It is information for the approver,
// not a deadline imposed on them, and the restore decides again at the moment it
// stops anything, exactly as before.

import (
	"fmt"
	"time"

	"helpdesk/internal/infra"
)

// What the cost of waiting looks like.
const (
	// DelayLatestStart: there is margin, and the incident is being charged as it
	// waits, so there is a latest start that stays within the budget.
	DelayLatestStart = "latest_start"
	// DelayNoMargin: what is left is exactly what the restore is taken to need.
	DelayNoMargin = "no_margin"
	// DelayProjectedBreach: the restore is taken to need more than is left, so it
	// is projected to breach the budget whenever it starts. Not a breach yet.
	DelayProjectedBreach = "projected_breach"
	// DelayOverspent: the budget is already spent: a breach that has happened.
	DelayOverspent = "overspent"
	// DelayNotCharging: no open incident is being charged to the ledger, so on its
	// clock waiting does not use the budget.
	DelayNotCharging = "not_charging"
)

// BudgetDelay is the cost of waiting, set against the budget.
type BudgetDelay struct {
	State string `json:"state"`
	// Detail is the cost of waiting in a sentence a person can read at the gate.
	Detail string `json:"detail"`
	// AsOf is the ledger's clock when this was made: the figures are as old as it.
	AsOf time.Time `json:"as_of"`
	// Charging says an open outage incident is being charged to the ledger as it
	// waits.
	Charging bool `json:"charging"`
	// SlackSeconds is what is left less what the restore is taken to need;
	// negative when it needs more than is left. LatestStart is when the restore
	// must start to stay within the budget: only with margin, and only while an
	// incident is being charged.
	SlackSeconds *int64     `json:"slack_seconds,omitempty"`
	LatestStart  *time.Time `json:"latest_start,omitempty"`
	// PlannedSeconds and PlannedBasis are how long the restore was taken to last,
	// and on what (the verdict's own figures).
	PlannedSeconds *int64 `json:"planned_seconds,omitempty"`
	PlannedBasis   string `json:"planned_basis,omitempty"`
}

// delayFor says what waiting costs against the budget the verdict was made from.
// It is nil when there is nothing to say: no budget, or a ledger that could not be
// read.
func delayFor(v *BudgetVerdict, l *Ledger, serving bool) *BudgetDelay {
	if v == nil || l == nil || v.RemainingSeconds == nil {
		return nil
	}
	charging := false
	for _, e := range l.Charged {
		if e.Ongoing {
			charging = true
			break
		}
	}
	d := &BudgetDelay{AsOf: l.AsOf.UTC(), Charging: charging}
	rem := *v.RemainingSeconds
	span := func(s int64) string {
		if s < 0 {
			return "-" + infra.FormatSpan(secondsOf(-s))
		}
		return infra.FormatSpan(secondsOf(s))
	}

	// What the open incident does to the figures, said once, whichever state.
	grows := "any further downtime adds to it"
	if charging {
		grows = "every further minute the open incident stays open adds a minute to it"
	}
	servingNote := ""
	if charging && serving {
		servingNote = " (The instance is serving, so the real downtime starts only when the restore stops it; the ledger charges the incident's elapsed time.)"
	}

	if rem <= 0 {
		d.State = DelayOverspent
		if rem == 0 {
			d.Detail = "no budget left: the budget is used up, and " + grows + servingNote
		} else {
			d.Detail = fmt.Sprintf("budget already overspent by %s: %s%s", span(-rem), grows, servingNote)
		}
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
			span(rem), span(planned), basis, span(-slack), grows, servingNote)
	case slack == 0:
		d.State = DelayNoMargin
		d.Detail = fmt.Sprintf("no remaining margin: %s is left and the restore is taken to need %s (%s), so it only just fits if it starts now; %s%s",
			span(rem), span(planned), basis, grows, servingNote)
	case !charging:
		d.State = DelayNotCharging
		d.Detail = fmt.Sprintf("no open incident is being charged to the ledger, so on its clock waiting does not use the budget (%s of margin, with a restore taken to need %s). If the instance is down, the downtime is accruing regardless",
			span(slack), span(planned))
	default:
		d.State = DelayLatestStart
		at := l.AsOf.UTC().Add(secondsOf(slack))
		d.LatestStart = &at
		d.Detail = fmt.Sprintf("estimated latest start to remain within budget, under current assumptions: %s (in %s), assuming a restore of %s (%s)%s",
			at.Format("15:04:05Z"), span(slack), span(planned), basis, servingNote)
	}
	return d
}

// durationPhrase says what the restore's planned duration rests on.
func durationPhrase(v *BudgetVerdict) string {
	n := 0
	if v.RecoveryEstimate != nil {
		n = v.RecoveryEstimate.Samples
	}
	switch v.DurationBasis {
	case DurationFromHistoryP90:
		return fmt.Sprintf("the p90 of %d past restores", n)
	case DurationFromHistoryLongest:
		return fmt.Sprintf("the longest of %d past restores", n)
	}
	return "the declared rto, an upper bound"
}
