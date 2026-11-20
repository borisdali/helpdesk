package infra

import "time"

// DefaultDrillInterval is how long a database may go without a verified restore
// drill before it is reported as overdue for one: a quarter.
const DefaultDrillInterval = 90 * 24 * time.Hour

// DrillReminder says how often a database is meant to be drilled.
type DrillReminder struct {
	// Applies is false when no reminder is wanted: the database has no objective at
	// all (nothing says a restore matters to it, and a development database should
	// not nag), or its drill_interval is "0d".
	Applies bool
	// Interval is the limit. Source says where it came from: a level of the config,
	// or SourceBuiltin for the default.
	Interval time.Duration
	Source   string
	// Off is set when the reminder was turned off on purpose, as opposed to simply
	// not applying.
	Off bool
}

// DrillReminder resolves the reminder for the database these objectives are
// for. It applies to a database with an rpo, an rto or an availability SLO. An
// interval that could not be read does not switch it off: the default applies,
// and Problems carries the reason.
func (r ResolvedObjectives) DrillReminder() DrillReminder {
	hasObjective := r.RPO.Set || r.RTO.Set || r.AvailabilitySLO.Set || r.RPOInvalid || r.AvailabilitySLOInvalid
	if !hasObjective {
		return DrillReminder{}
	}
	if r.DrillInterval.Set {
		if r.DrillInterval.Value == 0 {
			return DrillReminder{Off: true, Source: r.DrillInterval.Source}
		}
		return DrillReminder{Applies: true, Interval: r.DrillInterval.Value, Source: r.DrillInterval.Source}
	}
	return DrillReminder{Applies: true, Interval: DefaultDrillInterval, Source: SourceBuiltin}
}
