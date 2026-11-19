package availability

// Is the server's restore drilled often enough? (v0.36)
//
// A backup nobody has restored is a hope, and a restore nobody has practised is
// a first attempt on the worst day. A database that has an objective is meant to
// be drilled now and then, so it is reported when it has not been: the newest
// verified drill is older than the interval the config gives, or there is none.
//
// This is a reminder and nothing else. It never blocks a restore, never changes
// a plan's figures and is not an objective: it is information beside the others.
// It counts ANY verified drill, whatever its method or size, because what it
// asks is whether the procedure has been practised and the backup shown to
// restore; whether a drill is also comparable enough to plan a restore from is a
// stricter question the estimate asks (drills.go).
//
// A refused or unverifiable drill does not count, and the report names it: an
// attempt that failed is worth more attention than a silence.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

// DrillCurrencyLookback is how far back the drills are read for this: far enough
// to say "last verified drill 143 days ago" for any sensible interval.
const DrillCurrencyLookback = 730 * 24 * time.Hour

// Drill currency states.
const (
	// DrillCurrent: a verified drill within the interval.
	DrillCurrent = "current"
	// DrillOverdue: the newest verified drill is older than the interval.
	DrillOverdue = "overdue"
	// DrillNever: no verified drill in the lookback.
	DrillNever = "never"
	// DrillReminderOff: the reminder was turned off for this database.
	DrillReminderOff = "off"
	// DrillUnreadable: the drills could not be read. Not "never": a reader told
	// nothing could be looked at is not told nothing exists.
	DrillUnreadable = "unavailable"
)

// DrillAttempt is a drill that did not count, named in the report.
type DrillAttempt struct {
	DrillID string    `json:"drill_id"`
	Status  string    `json:"status"`
	At      time.Time `json:"at"`
	// Failed names the checks that failed, so the report says why.
	Failed []string `json:"failed,omitempty"`
}

// DrillCurrency is how recently this database was drilled, against its interval.
type DrillCurrency struct {
	State string `json:"state"`
	// Overdue is set for DrillOverdue and DrillNever: a reminder is due.
	Overdue   bool `json:"overdue,omitempty"`
	LimitDays int  `json:"limit_days,omitempty"`
	// Source says where the limit came from: a level of the config, or "builtin".
	Source         string     `json:"source,omitempty"`
	LastVerifiedAt *time.Time `json:"last_verified_at,omitempty"`
	LastDrillID    string     `json:"last_drill_id,omitempty"`
	AgeDays        *int       `json:"age_days,omitempty"`
	// LastAttempt is the newest refused or unverifiable drill that is newer than
	// the last verified one, or any when there is none: a failed attempt since.
	LastAttempt *DrillAttempt `json:"last_attempt,omitempty"`
	// MatchedBy is how drills were tied to this server, as for the estimate.
	MatchedBy string `json:"matched_by,omitempty"`
	Detail    string `json:"detail"`
}

// DrillCurrencyOff is what a database with the reminder off, or no objective to
// attach one to, reports: nothing.
func DrillCurrencyOff(reminder infra.DrillReminder) *DrillCurrency {
	if !reminder.Off {
		return nil
	}
	return &DrillCurrency{State: DrillReminderOff, Source: reminder.Source,
		Detail: "the drill reminder is turned off for this database (drill_interval 0d)"}
}

// UnavailableDrillCurrency is what is reported when the drills could not be read.
func UnavailableDrillCurrency(reminder infra.DrillReminder, err error) *DrillCurrency {
	return &DrillCurrency{State: DrillUnreadable, LimitDays: days(reminder.Interval), Source: reminder.Source,
		Detail: "the recorded restore drills could not be read (" + err.Error() + "), so it is not known whether a drill is due"}
}

func days(d time.Duration) int { return int(d / (24 * time.Hour)) }

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// AssessDrillCurrency sets the drills of one database against its reminder. It
// returns nil when no reminder applies. in.ReadSystemID is called only when there
// is a verified drill to tie to the server, as for the estimate.
func AssessDrillCurrency(drills []audit.DrillRecord, reminder infra.DrillReminder, in DrillInputs, now time.Time) *DrillCurrency {
	if !reminder.Applies {
		return DrillCurrencyOff(reminder)
	}
	drills = audit.Current(drills)
	c := &DrillCurrency{LimitDays: days(reminder.Interval), Source: reminder.Source, MatchedBy: "name"}

	target := in.SystemID
	if target == "" && in.ReadSystemID != nil {
		for _, r := range drills {
			if r.Counted() {
				target = in.ReadSystemID()
				break
			}
		}
	}
	if target != "" {
		c.MatchedBy = "system_identifier"
	} else {
		// By name: take the newest verified drill's system as this one, as the
		// estimate does, so that a rebuilt server's old drills stop counting once
		// the new one has been drilled.
		var newest time.Time
		for _, r := range drills {
			if r.Counted() && r.SourceSystemID != "" && !drillWhen(r).Before(newest) {
				newest, target = drillWhen(r), r.SourceSystemID
			}
		}
	}

	var last *audit.DrillRecord
	var attempt *audit.DrillRecord
	for i := range drills {
		r := &drills[i]
		switch {
		case r.Counted():
			if target != "" && r.SourceSystemID != target {
				continue
			}
			if last == nil || drillWhen(*r).After(drillWhen(*last)) {
				last = r
			}
		default:
			if attempt == nil || drillWhen(*r).After(drillWhen(*attempt)) {
				attempt = r
			}
		}
	}
	// A failed attempt is named only when it came after the last good drill (or
	// there is none): one that was since put right is history.
	if attempt != nil && (last == nil || drillWhen(*attempt).After(drillWhen(*last))) {
		a := &DrillAttempt{DrillID: attempt.DrillID, Status: attempt.Status, At: drillWhen(*attempt)}
		for _, ch := range attempt.Checks {
			if ch.Result == "fail" {
				a.Failed = append(a.Failed, ch.Name)
			}
		}
		c.LastAttempt = a
	}

	limit := plural(c.LimitDays, "day", "days")
	switch {
	case last == nil:
		c.State, c.Overdue = DrillNever, true
		c.Detail = fmt.Sprintf("no verified restore drill of this server is recorded (limit %s)", limit)
	default:
		at := drillWhen(*last)
		age := int(now.Sub(at) / (24 * time.Hour))
		if age < 0 {
			age = 0
		}
		c.LastVerifiedAt, c.LastDrillID, c.AgeDays = &at, last.DrillID, &age
		if now.Sub(at) > reminder.Interval {
			c.State, c.Overdue = DrillOverdue, true
			c.Detail = fmt.Sprintf("last verified drill %s ago (limit %s)", plural(age, "day", "days"), limit)
		} else {
			c.State = DrillCurrent
			c.Detail = fmt.Sprintf("last verified drill %s ago (limit %s)", plural(age, "day", "days"), limit)
		}
	}
	if a := c.LastAttempt; a != nil {
		ago := int(now.Sub(a.At) / (24 * time.Hour))
		if ago < 0 {
			ago = 0
		}
		why := ""
		if len(a.Failed) > 0 {
			why = " on " + strings.Join(a.Failed, ", ")
		}
		c.Detail += fmt.Sprintf("; the latest attempt, %s (%s ago), was %s%s and does not count", a.DrillID, plural(ago, "day", "days"), strings.ReplaceAll(a.Status, "_", " "), why)
	}
	if c.MatchedBy == "name" && c.State != DrillNever {
		c.Detail += "; matched by name, the source's system identifier was not checked"
	}
	return c
}

// DrillCurrency reads the drills of the database with this infrastructure key
// and assesses them against its reminder. Drills that cannot be read are reported
// as unavailable, never as none.
func (c *Client) DrillCurrency(ctx context.Context, key string, reminder infra.DrillReminder, in DrillInputs, now time.Time) *DrillCurrency {
	if !reminder.Applies {
		return DrillCurrencyOff(reminder)
	}
	drills, err := c.Drills(ctx, key, now.Add(-DrillCurrencyLookback))
	if err != nil {
		return UnavailableDrillCurrency(reminder, err)
	}
	return AssessDrillCurrency(drills, reminder, in, now)
}
