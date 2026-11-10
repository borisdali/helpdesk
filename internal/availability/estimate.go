package availability

// The recovery-time estimate (v0.35): how long a restore of a database has taken
// before, from real incidents, so that the gate can say more than "up to the
// declared rto".
//
// It is a summary of history and says what it rests on. What it measures is the
// same thing the outcome stage of a recovery decision records and compares with
// the rto: the duration of the remediation run that did the restore (restore,
// restart, verify), which begins after the approval, so it excludes the wait for
// one. It is not the downtime, which began earlier.
//
// The rules that keep it honest:
//   - only real incidents; a faulttest injection never counts;
//   - only runs that ended resolved: a restore that failed took a time too, but it
//     was not a recovery, and is said to have been left out;
//   - only the same database: another database's restores say little about this
//     one's (data size dominates), so there is no fallback to the fleet, and with
//     too few of its own the estimate says so and the declared rto stands alone;
//   - it is a percentile of what happened, with the sample size beside it, never a
//     promise.

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

// RestoreSeries are the playbook series whose runs are a restore of a database
// from its backups, one kind of recovery by how long it takes. A restore to the
// latest backup and a restore to a point in time are dominated by the same thing
// (the size of the data), so they are estimated together.
var RestoreSeries = []string{"pbs_pgbackrest_pitr_remediate", "pbs_pgbackrest_restore_remediate"}

const (
	// EstimateWindow is how far back the estimate looks.
	EstimateWindow = 90 * 24 * time.Hour
	// MinEstimateSamples is how many resolved restores an estimate needs. Below
	// it a percentile is the one or two numbers there are, and is not offered.
	MinEstimateSamples = 3
)

// Estimate statuses.
const (
	// EstimateEstimated: there are enough resolved restores to summarise.
	EstimateEstimated = "estimated"
	// EstimateTooFew: some, but fewer than MinEstimateSamples.
	EstimateTooFew = "insufficient_history"
	// EstimateNone: none in the window.
	EstimateNone = "no_history"
	// EstimateUnavailable: the history could not be read. That is not "no
	// history": the reader is told it could not be looked at.
	EstimateUnavailable = "unavailable"
)

// UnavailableEstimate is what is reported when the history could not be read.
func UnavailableEstimate(err error) *RecoveryEstimate {
	return &RecoveryEstimate{
		Status:        EstimateUnavailable,
		WindowSeconds: int64(EstimateWindow / time.Second),
		Basis:         "the history of past restores could not be read (" + err.Error() + "), so there is no estimate",
	}
}

// RecoveryEstimate summarises how long this database's restores have taken.
type RecoveryEstimate struct {
	Status  string `json:"status"`
	Samples int    `json:"samples"`
	// The summary, in whole seconds; absent unless Status is estimated.
	P50Seconds *int64 `json:"p50_seconds,omitempty"`
	P90Seconds *int64 `json:"p90_seconds,omitempty"`
	MinSeconds *int64 `json:"min_seconds,omitempty"`
	MaxSeconds *int64 `json:"max_seconds,omitempty"`
	// WindowSeconds is how far back it looked.
	WindowSeconds int64 `json:"window_seconds"`
	// Excluded counts runs of these series in the window that are not counted:
	// they did not end resolved, or had not finished.
	Excluded int `json:"excluded,omitempty"`
	// Truncated says the listing it read was cut off at its page limit, so older
	// restores may be missing.
	Truncated bool `json:"truncated,omitempty"`
	// Basis says what the estimate rests on, in a sentence.
	Basis string `json:"basis"`

	// RTOSeconds is the declared rto it was set against, when there is one.
	// OverRTO is how many of the counted restores took longer than it, and VsRTO
	// says "within" when none did and "exceeds" when some did: the declared rto
	// against what has been demonstrated. Only when estimated and an rto is
	// declared.
	RTOSeconds  *int64 `json:"rto_seconds,omitempty"`
	OverRTO     *int   `json:"over_rto,omitempty"`
	VsRTO       string `json:"vs_rto,omitempty"`
	VsRTODetail string `json:"vs_rto_detail,omitempty"`
}

// EstimateRecovery summarises the resolved runs of the given series in a
// listing of one database's real incidents (fetched with include=runs). rto is
// the declared rto, or nil.
func EstimateRecovery(l *audit.IncidentListing, series []string, now time.Time, window time.Duration, rto *time.Duration) RecoveryEstimate {
	e := RecoveryEstimate{WindowSeconds: int64(window / time.Second)}
	if rto != nil {
		r := int64(rto.Round(time.Second) / time.Second)
		e.RTOSeconds = &r
	}
	from := now.Add(-window)
	var took []time.Duration
	if l != nil {
		e.Truncated = l.Truncated
		for _, inc := range l.Incidents {
			if inc.Incident == nil || (inc.Origin != "" && inc.Origin != audit.IncidentOriginReal) {
				continue
			}
			for _, run := range inc.Runs {
				if !slices.Contains(series, run.SeriesID) || run.StartedAt.Before(from) && run.CompletedAt.Before(from) {
					continue
				}
				d := run.CompletedAt.Sub(run.StartedAt)
				if run.Outcome != audit.OutcomeResolved || run.CompletedAt.IsZero() || d <= 0 {
					e.Excluded++
					continue
				}
				took = append(took, d)
			}
		}
	}
	e.Samples = len(took)
	span := infra.FormatSpan(window)
	left := ""
	if e.Excluded > 0 {
		left = fmt.Sprintf("; %d other run(s) did not recover or had not finished and are not counted", e.Excluded)
	}
	if e.Truncated {
		left += "; the incident listing was cut off at its page limit, so older restores may be missing"
	}

	switch {
	case e.Samples == 0:
		e.Status = EstimateNone
		e.Basis = fmt.Sprintf("no resolved restore of this database in the last %s among real incidents: nothing to estimate from%s", span, left)
		return e
	case e.Samples < MinEstimateSamples:
		e.Status = EstimateTooFew
		e.Basis = fmt.Sprintf("only %d resolved restore(s) of this database in the last %s (real incidents): %d are needed for an estimate%s",
			e.Samples, span, MinEstimateSamples, left)
		return e
	}

	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	secs := func(d time.Duration) *int64 { v := int64(d.Round(time.Second) / time.Second); return &v }
	e.Status = EstimateEstimated
	e.P50Seconds, e.P90Seconds = secs(percentile(took, 0.50)), secs(percentile(took, 0.90))
	e.MinSeconds, e.MaxSeconds = secs(took[0]), secs(took[len(took)-1])
	e.Basis = fmt.Sprintf("%d resolved restores of this database in the last %s (real incidents): p50 %s, p90 %s, longest %s%s",
		e.Samples, span, infra.FormatSpan(percentile(took, 0.50)), infra.FormatSpan(percentile(took, 0.90)), infra.FormatSpan(took[len(took)-1]), left)

	if rto != nil {
		over := 0
		for _, d := range took {
			if d > *rto {
				over++
			}
		}
		e.OverRTO = &over
		if over == 0 {
			e.VsRTO = "within"
			e.VsRTODetail = fmt.Sprintf("none of the %d past restores took longer than the rto of %s (the longest took %s)", e.Samples, infra.FormatSpan(*rto), infra.FormatSpan(took[len(took)-1]))
		} else {
			e.VsRTO = "exceeds"
			e.VsRTODetail = fmt.Sprintf("%d of the %d past restores took longer than the rto of %s (the longest took %s)", over, e.Samples, infra.FormatSpan(*rto), infra.FormatSpan(took[len(took)-1]))
		}
	}
	return e
}

// percentile is the nearest-rank percentile of an ascending, non-empty slice:
// the smallest value that at least p of the samples are at or below.
func percentile(sorted []time.Duration, p float64) time.Duration {
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// RecoveryEstimate reads the resolved restores of the database with this
// infrastructure key from auditd and summarises them. rto is the declared rto,
// or nil.
func (c *Client) RecoveryEstimate(ctx context.Context, key string, series []string, rto *time.Duration, now time.Time) (*RecoveryEstimate, error) {
	since := now.Add(-EstimateWindow).UTC().Format(time.RFC3339)
	l, err := c.Incidents(ctx, url.Values{"target_key": {key}, "since": {since}, "include": {"runs"}})
	if err != nil {
		return nil, err
	}
	e := EstimateRecovery(l, series, now, EstimateWindow, rto)
	return &e, nil
}
