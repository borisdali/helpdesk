// Package availability computes the availability ledger: how much of a
// database's downtime budget the incidents in its window have already spent.
//
// The budget (internal/infra) says what the SLO allows. The ledger says what
// has been used, from recorded incidents. It is arithmetic on those records and
// says what they cannot show:
//
//   - Duration runs from detection to recovery: from the start of an incident's
//     entry playbook run to the end of its last run. It includes the wait for an
//     approval, and begins when aiHelpDesk was engaged, not when the failure
//     began. It is not measured downtime.
//   - Only real incidents count; faulttest injections never do.
//   - Only an incident whose worst playbook has an `outage` impact is charged
//     (playbooks.AvailabilityImpact). Degraded, harmless and unclassified
//     incidents are listed beside it, uncharged, so nothing is hidden.
//   - An outage that ended without a confirmed recovery (escalated, needs a
//     human, abandoned) or that has stayed open for a day is not charged either,
//     because how long the database was really down is not known. It is listed
//     as unconfirmed, and RemainingWorstCase says what is left if it all counts.
package availability

import (
	"fmt"
	"sort"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
	"helpdesk/playbooks"
)

// Basis is stated with every ledger.
const Basis = "from detection to recovery: each incident runs from the start of its entry playbook run to the end of its last run, " +
	"so it includes the wait for approvals and begins when aiHelpDesk was engaged, not when the failure began"

// DefaultStaleOpen is how long an incident may stay open before it is treated as
// unconfirmed instead of an outage still running. A run that died leaves its
// incident open for good, and charging that as an ever-growing outage would
// drain the budget on nothing.
const DefaultStaleOpen = 24 * time.Hour

// Impact labels in a ledger: the declared ones, and what has none.
const (
	ImpactUnclassified = "unclassified"
)

// Incident is one incident as the ledger needs it.
type Incident struct {
	ID string
	// SeriesIDs are the playbook series of every run of the incident, the entry
	// run's included. The incident's impact is the worst of them.
	SeriesIDs  []string
	Status     string
	Origin     string
	DetectedAt time.Time
	// ResolvedAt is when the last run ended; zero while the incident is open.
	ResolvedAt  time.Time
	TargetInput string
}

// Classify gives a playbook series' declared impact. playbooks.AvailabilityImpact
// is the real one.
type Classify func(seriesID string) (playbooks.Impact, bool)

// Params is everything Compute reads.
type Params struct {
	Database  string
	Budget    infra.AvailabilityBudget
	Incidents []Incident
	Now       time.Time
	Classify  Classify
	// StaleOpen overrides DefaultStaleOpen when positive.
	StaleOpen time.Duration
	// Truncated says the incident listing was cut off, so the ledger may be
	// missing some. Unattributed is how many incidents in the window could not be
	// tied to any database.
	Truncated    bool
	Unattributed int
}

// Entry is one incident in the ledger.
type Entry struct {
	IncidentID string `json:"incident_id"`
	// Impact is outage, degraded, none or unclassified.
	Impact          string `json:"impact"`
	Status          string `json:"status"`
	DurationSeconds int64  `json:"duration_seconds"`
	// Ongoing: the incident is still open and the duration runs to now.
	Ongoing    bool      `json:"ongoing,omitempty"`
	DetectedAt time.Time `json:"detected_at"`
	// Why is set when the entry is not charged although it is an outage.
	Why string `json:"why,omitempty"`
}

// Ledger is the result. Durations are in whole seconds.
type Ledger struct {
	Database      string    `json:"database"`
	AsOf          time.Time `json:"as_of"`
	WindowStart   time.Time `json:"window_start"`
	WindowSeconds int64     `json:"window_seconds"`
	WindowSource  string    `json:"window_source"`
	Basis         string    `json:"basis"`

	// BudgetSeconds is what the SLO allows per window; nil when no budget could
	// be computed (BudgetDetail says why, or states the budget).
	BudgetSeconds *int64 `json:"budget_seconds,omitempty"`
	BudgetDetail  string `json:"budget_detail"`

	// Charged are the outage incidents that count against the budget.
	Charged        []Entry `json:"charged"`
	ChargedSeconds int64   `json:"charged_seconds"`
	// Unconfirmed are outage incidents not charged, because their real downtime
	// is not known.
	Unconfirmed        []Entry `json:"unconfirmed"`
	UnconfirmedSeconds int64   `json:"unconfirmed_seconds"`
	// Uncharged are the incidents that cost no budget: degraded, harmless, or
	// with no declared impact.
	Degraded     []Entry `json:"degraded"`
	None         []Entry `json:"none"`
	Unclassified []Entry `json:"unclassified"`

	// RemainingSeconds is the budget less what is charged; negative once the
	// budget is overspent. RemainingWorstCaseSeconds also takes off everything
	// unconfirmed. Both are nil with no budget.
	RemainingSeconds          *int64   `json:"remaining_seconds,omitempty"`
	RemainingWorstCaseSeconds *int64   `json:"remaining_worst_case_seconds,omitempty"`
	Overspent                 bool     `json:"overspent,omitempty"`
	UsedShare                 *float64 `json:"used_share,omitempty"`

	Truncated    bool `json:"truncated,omitempty"`
	Unattributed int  `json:"unattributed,omitempty"`
	// ExcludedFaulttest counts the faulttest incidents left out.
	ExcludedFaulttest int      `json:"excluded_faulttest,omitempty"`
	Notes             []string `json:"notes,omitempty"`
}

// Compute builds the ledger. It never fails: what it cannot place it lists.
func Compute(p Params) Ledger {
	window, source := p.Budget.Window, p.Budget.WindowSource
	if window <= 0 {
		window, source = infra.DefaultAvailabilityWindow, infra.SourceBuiltin
	}
	stale := p.StaleOpen
	if stale <= 0 {
		stale = DefaultStaleOpen
	}
	l := Ledger{
		Database:      p.Database,
		AsOf:          p.Now.UTC(),
		WindowStart:   p.Now.UTC().Add(-window),
		WindowSeconds: int64(window / time.Second),
		WindowSource:  source,
		Basis:         Basis,
		BudgetDetail:  p.Budget.Detail,
		Charged:       []Entry{}, Unconfirmed: []Entry{}, Degraded: []Entry{}, None: []Entry{}, Unclassified: []Entry{},
		Truncated: p.Truncated, Unattributed: p.Unattributed,
	}

	var ongoing, oddities int
	for _, inc := range p.Incidents {
		if inc.Origin != "" && inc.Origin != audit.IncidentOriginReal {
			l.ExcludedFaulttest++
			continue
		}
		impact := impactOf(inc.SeriesIDs, p.Classify)
		isOpen := inc.Status == audit.IncidentStatusOpen || inc.Status == ""
		end := inc.ResolvedAt
		switch {
		case isOpen:
			end = p.Now
		case end.IsZero():
			// A closed incident with no end time: there is nothing to measure.
			end = inc.DetectedAt
			oddities++
		}
		from, to := inc.DetectedAt, end
		if from.Before(l.WindowStart) {
			from = l.WindowStart
		}
		if to.After(p.Now) {
			to = p.Now
		}
		if to.Before(l.WindowStart) {
			continue // ended before the window began
		}
		dur := to.Sub(from)
		if dur < 0 {
			dur = 0
		}
		e := Entry{IncidentID: inc.ID, Impact: impact, Status: inc.Status,
			DurationSeconds: int64(dur.Round(time.Second) / time.Second), Ongoing: isOpen, DetectedAt: inc.DetectedAt}

		switch impact {
		case string(playbooks.ImpactOutage):
			switch {
			case isOpen && p.Now.Sub(inc.DetectedAt) > stale:
				e.Why = fmt.Sprintf("open for more than %s: probably a run that never finished, so how long the database was down is not known", infra.FormatSpan(stale))
				l.Unconfirmed = append(l.Unconfirmed, e)
			case isOpen:
				l.Charged = append(l.Charged, e)
				ongoing++
			case inc.Status == audit.IncidentStatusResolved:
				l.Charged = append(l.Charged, e)
			default:
				e.Why = "ended " + inc.Status + ", not as a confirmed recovery, so how long the database was down is not known"
				l.Unconfirmed = append(l.Unconfirmed, e)
			}
		case string(playbooks.ImpactDegraded):
			l.Degraded = append(l.Degraded, e)
		case string(playbooks.ImpactNone):
			l.None = append(l.None, e)
		default:
			l.Unclassified = append(l.Unclassified, e)
		}
	}
	for _, list := range [][]Entry{l.Charged, l.Unconfirmed, l.Degraded, l.None, l.Unclassified} {
		sort.SliceStable(list, func(i, j int) bool { return list[i].DetectedAt.Before(list[j].DetectedAt) })
	}
	l.ChargedSeconds, l.UnconfirmedSeconds = total(l.Charged), total(l.Unconfirmed)

	if p.Budget.Computed {
		allowed := int64(p.Budget.Allowed.Round(time.Second) / time.Second)
		rem, worst := allowed-l.ChargedSeconds, allowed-l.ChargedSeconds-l.UnconfirmedSeconds
		l.BudgetSeconds, l.RemainingSeconds, l.RemainingWorstCaseSeconds = &allowed, &rem, &worst
		l.Overspent = rem < 0
		if allowed > 0 {
			share := float64(l.ChargedSeconds) / float64(allowed)
			l.UsedShare = &share
		}
	}

	l.Notes = notes(l, p, ongoing, oddities)
	return l
}

// impactOf is the worst declared impact among the series, or unclassified.
func impactOf(series []string, classify Classify) string {
	var found []playbooks.Impact
	if classify != nil {
		for _, s := range series {
			if i, ok := classify(s); ok {
				found = append(found, i)
			}
		}
	}
	if worst, ok := playbooks.WorstImpact(found...); ok {
		return string(worst)
	}
	return ImpactUnclassified
}

func total(entries []Entry) int64 {
	var n int64
	for _, e := range entries {
		n += e.DurationSeconds
	}
	return n
}

func notes(l Ledger, p Params, ongoing, oddities int) []string {
	var n []string
	if !p.Budget.Computed {
		n = append(n, "no downtime budget could be computed ("+p.Budget.Detail+"), so nothing is shown as remaining")
	}
	if ongoing > 0 {
		n = append(n, fmt.Sprintf("%d outage incident(s) are still open and are counted up to now", ongoing))
	}
	if len(l.Unconfirmed) > 0 {
		n = append(n, fmt.Sprintf("%d outage incident(s), %s in all, are not charged because their real downtime is unknown; remaining_worst_case counts them",
			len(l.Unconfirmed), time.Duration(l.UnconfirmedSeconds)*time.Second))
	}
	if len(l.Unclassified) > 0 {
		n = append(n, fmt.Sprintf("%d incident(s) ran only playbooks with no declared availability impact; they are not charged", len(l.Unclassified)))
	}
	if l.Unattributed > 0 {
		n = append(n, fmt.Sprintf("%d incident(s) in the window could not be tied to any database, so they are in no database's ledger", l.Unattributed))
	}
	if l.Truncated {
		n = append(n, "the incident listing was cut off at its page limit: the ledger may be missing incidents")
	}
	if l.ExcludedFaulttest > 0 {
		n = append(n, fmt.Sprintf("%d faulttest incident(s) are never counted", l.ExcludedFaulttest))
	}
	if oddities > 0 {
		n = append(n, fmt.Sprintf("%d closed incident(s) have no end time and count as zero", oddities))
	}
	return n
}
