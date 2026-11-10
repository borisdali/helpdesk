package infra

// The availability budget (v0.35): what availability_slo allows a database to
// be down in a window, and how many worst-case recoveries that absorbs.
//
// RTO alone says nothing about how often a failure may happen. Availability is
// MTBF/(MTBF+MTTR): the rto bounds the repair-time term, and the SLO and its
// window give the total downtime a database may have, so
//
//	budget            = window x (100 - availability_slo) / 100
//	recoveries absorbed = floor(budget / rto)
//
// 99.9% over 30 days is 43m12s; 99.99% is 4m19s.
//
// This is arithmetic on declared numbers, and says so. It does not measure
// downtime, count past incidents or forecast anything: a recovery that finishes
// faster than its rto uses less than the share shown, and one that fails uses
// more. The window is a rolling one (a calendar month's SLO would need the
// month's start, which no setting carries).

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// DefaultAvailabilityWindow is the window used when an availability_slo is set
// and no availability_window is. It is always reported as the built-in default,
// never passed off as a declared value.
const DefaultAvailabilityWindow = 30 * 24 * time.Hour

// SourceBuiltin is the Source of a value that no level of the config set.
const SourceBuiltin = "built-in default"

// AvailabilityBudget is the budget for one database, with what it says about
// the rto declared beside it.
type AvailabilityBudget struct {
	// Computed is false when there is nothing to compute from (no slo, or a
	// setting that cannot be trusted); Detail then says why.
	Computed bool
	SLO      float64
	Window   time.Duration
	// WindowSource is where the window came from: a level of the config, or
	// SourceBuiltin.
	WindowSource string
	// Allowed is the downtime the SLO permits per window.
	Allowed time.Duration

	RTOKnown bool
	RTO      time.Duration
	// RTOShare is the rto as a fraction of Allowed (0.7 = 70%). Zero when the
	// rto is unknown or Allowed is zero.
	RTOShare float64
	// Tolerated is how many recoveries of exactly the rto fit in the window.
	Tolerated int
	// Contradictory: the rto is longer than the whole budget, so one recovery
	// that takes the full rto breaks the SLO. The two declared numbers cannot
	// both be met.
	Contradictory bool

	// Detail is the whole statement, for a person.
	Detail string
}

// ComputeBudget does the arithmetic. slo is a percentage in (0, 100], window is
// positive; rtoSet says whether rto is a declared value.
func ComputeBudget(slo float64, window time.Duration, windowSource string, rto time.Duration, rtoSet bool) AvailabilityBudget {
	b := AvailabilityBudget{SLO: slo, Window: window, WindowSource: windowSource}
	if math.IsNaN(slo) || slo <= 0 || slo > 100 || window <= 0 {
		b.Detail = "the availability objective is not a valid percentage and window: no budget computed"
		return b
	}
	b.Computed = true
	b.Allowed = time.Duration(math.Round(float64(window) * (100 - slo) / 100)).Round(time.Millisecond)
	b.RTOKnown, b.RTO = rtoSet, rto

	win := FormatSpan(window)
	if windowSource == SourceBuiltin {
		win += " (default window)"
	}
	head := fmt.Sprintf("availability %v%% over %s allows %s of downtime per window", slo, win, FormatSpan(b.Allowed))
	switch {
	case !rtoSet:
		b.Detail = head + "; no rto is set, so the recoveries it absorbs are not computed"
	case b.Allowed == 0:
		b.Contradictory = true
		b.Detail = fmt.Sprintf("availability %v%% over %s allows no downtime, so no recovery fits (rto %s)", slo, win, FormatSpan(rto))
	case rto > b.Allowed:
		b.Contradictory = true
		b.RTOShare = float64(rto) / float64(b.Allowed)
		b.Detail = fmt.Sprintf("%s; rto %s is longer than the whole budget: one recovery that takes the full rto breaks the SLO", head, FormatSpan(rto))
	default:
		b.RTOShare = float64(rto) / float64(b.Allowed)
		b.Tolerated = int(b.Allowed / rto)
		noun := "recoveries"
		if b.Tolerated == 1 {
			noun = "recovery"
		}
		b.Detail = fmt.Sprintf("%s; rto %s uses %.0f%% of it: tolerates %d %s per window at the rto", head, FormatSpan(rto), b.RTOShare*100, b.Tolerated, noun)
	}
	return b
}

// Budget is the availability budget for what this database resolved to. It
// computes nothing, and says why, when the slo is unset or cannot be trusted,
// and when the window cannot be trusted: a typo in either must not quietly
// become "no objective" or the default window.
func (r ResolvedObjectives) Budget() AvailabilityBudget {
	switch {
	case r.AvailabilitySLOInvalid:
		return AvailabilityBudget{Detail: "availability_slo cannot be trusted (see the config problems): no budget computed"}
	case !r.AvailabilitySLO.Set:
		return AvailabilityBudget{Detail: "no availability_slo is set: no budget"}
	case r.AvailabilityWindowInvalid:
		return AvailabilityBudget{SLO: r.AvailabilitySLO.Value, Detail: "availability_window cannot be trusted (see the config problems): no budget computed"}
	}
	window, source := DefaultAvailabilityWindow, SourceBuiltin
	if r.AvailabilityWindow.Set {
		window, source = r.AvailabilityWindow.Value, r.AvailabilityWindow.Source
	}
	return ComputeBudget(r.AvailabilitySLO.Value, window, source, r.RTO.Value, r.RTO.Set)
}

// Consistency lists the databases whose declared availability objectives
// contradict each other: an rto longer than the whole budget the SLO allows.
// It reads only declared numbers, in a stable order. It is not a Validate
// error: the config is readable and nothing is blocked; the numbers just
// cannot all be met.
func (c *Config) Consistency() []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, id := range sortedKeys(c.DBServers) {
		res, ok := c.ResolveObjectives(id)
		if !ok {
			continue
		}
		if b := res.Budget(); b.Contradictory {
			out = append(out, fmt.Sprintf("db_servers.%s: %s", id, b.Detail))
		}
	}
	return out
}

// FormatSpan renders a duration the way the objectives are written: whole days
// as "30d", otherwise "43m12s", "1h", "4m19s", without trailing zero parts.
func FormatSpan(d time.Duration) string {
	if d >= time.Minute {
		d = d.Round(time.Second)
	}
	if d >= 24*time.Hour {
		days, rem := d/(24*time.Hour), d%(24*time.Hour)
		if rem == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%s", days, trimZeros(rem.String()))
	}
	return trimZeros(d.String())
}

// trimZeros drops the zero parts Go's Duration.String leaves: "30m0s" is
// "30m", "1h0m0s" is "1h".
func trimZeros(s string) string {
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
