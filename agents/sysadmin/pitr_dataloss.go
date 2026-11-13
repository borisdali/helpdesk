package main

// How much a point-in-time restore throws away (v0.34, Tier A).
//
// A PITR always loses data: everything committed after the target. The human
// at the gate has to weigh that, so the plan says how much, in the units that
// can be known without reading WAL:
//
//   - the span of time between the target and the end of the history that is
//     discarded, and
//   - the WAL volume in that span, when the target is an LSN (or the
//     instance's own position is known).
//
// "End of the history" is the instance's own clock and WAL position when it
// is serving (exact), or the end of the archive when it is down (the newest
// archived segment: an upper bound, since its time is when the segment
// reached the repo, and anything the instance had not yet archived when it
// died is not in the archive at all).
//
// Not measured here: the number of transactions. That needs the WAL decoded
// (pg_waldump over the discarded segments), which is walread.go: it is added
// to the estimate by withTransactions when it could be made, and when it could
// not the estimate says so rather than leaving it out silently. Neither can
// tell the bad writes from the good ones: it counts everything after the target.

import (
	"fmt"
	"strings"
	"time"
)

// Where the end of the discarded history was taken from.
const (
	lossBasisLive    = "live_instance"
	lossBasisArchive = "archive_end"
)

// DataLossEstimate is what a restore to a target discards, as far as it can be
// measured without reading WAL.
type DataLossEstimate struct {
	// Basis is live_instance (the running instance's clock and WAL position:
	// exact) or archive_end (the newest archived segment: an upper bound).
	Basis string `json:"basis"`
	// AsOf is when the estimate was made. On a serving instance the loss grows
	// until the instance is stopped, so an estimate is only as old as this.
	AsOf string `json:"as_of"`
	// To is the end of the discarded history: the instance's clock, or the
	// time the newest archived segment reached the repository.
	To string `json:"to,omitempty"`
	// WindowSeconds is the span between a time target and To. Absent for an
	// LSN target: an LSN has no time attached without reading WAL.
	WindowSeconds *int64 `json:"window_seconds,omitempty"`
	// WALBytes is the WAL between the target and the end of the history.
	// Absent for a time target, which has no position without reading WAL.
	WALBytes *uint64 `json:"wal_bytes,omitempty"`
	// WindowAtLeastSeconds is, for an LSN target, a lower bound on the history
	// discarded: the time between the moment the segment holding the target
	// reached the archive and the end of the history (less an allowance for
	// clock difference when that end is the running instance's clock). The
	// record at the target was written before its segment was archived, so the
	// loss is at least this much, though it may be more. Absent when the target
	// is not in an archived segment, or the bound is not positive. It can prove
	// a breach of the rpo; it cannot show there is none.
	WindowAtLeastSeconds *int64 `json:"window_at_least_seconds,omitempty"`
	// AtLeastBasis says what that bound rests on.
	AtLeastBasis string `json:"at_least_basis,omitempty"`
	// Segments is how many whole WAL segments lie after the target's own.
	// Only for an LSN target on the archive.
	Segments *uint64 `json:"segments,omitempty"`
	// UpperBound says the figures are at most this much, not exactly this
	// much (the archive basis).
	UpperBound bool `json:"upper_bound"`
	// Transactions is the number of transactions discarded, counted by decoding the
	// WAL with pg_waldump (walread.go). Absent when no attempt was made (the
	// estimate was made without an instance to ask); when it was made and failed
	// it says why, and the figures above stand alone.
	Transactions *TransactionLoss `json:"transactions,omitempty"`
	// Unmeasured lists what the estimate does not count.
	Unmeasured []string `json:"unmeasured,omitempty"`
	// Summary is the estimate in one sentence a person can read at the gate.
	Summary string `json:"summary"`
	// Objective sets the estimate against the database's recovery point
	// objective (pitr_rpo.go). Absent when the estimate was made without
	// asking which database it is for.
	Objective *RPOComparison `json:"objective,omitempty"`
}

// restoreClock is the clock the restore measures its loss against; a variable
// so a test can let time pass between the probe and the stop.
var restoreClock = time.Now

// projectPreStop carries a running instance's reading forward to now: its
// clock at the probe plus the time that has passed since (measured here, not
// asked again, so the restore's order of operations does not change). Its WAL
// position is left as it was probed: it can only have grown, so a byte figure
// from it is at least that much.
func projectPreStop(pre LivePreState, probeAt, now time.Time) LivePreState {
	t, err := time.Parse(time.RFC3339, pre.Time)
	if err != nil || probeAt.IsZero() || now.Before(probeAt) {
		return pre
	}
	pre.Time = t.Add(now.Sub(probeAt)).UTC().Format(time.RFC3339)
	return pre
}

// clockSkewMargin is taken off a lower bound that measures from the running
// instance's clock to a time on the repository's: two machines' clocks agree
// only so far. A bound measured entirely on the repository's own clock needs no
// allowance. The bound assumes the clocks agree to within this.
var clockSkewMargin = 60 * time.Second

// lossAtLeast bounds from below the history a restore to lsn discards, from
// when the archive segment holding lsn arrived. See WindowAtLeastSeconds.
func lossAtLeast(wal *WALContinuity, lsn uint64, live *LivePreState) (secs int64, basis string, ok bool) {
	tli, at, segno, found := wal.archivedAt(lsn)
	if !found {
		return 0, "", false
	}
	var (
		end    time.Time
		margin time.Duration
		to     string
	)
	if live != nil {
		if t, err := time.Parse(time.RFC3339, live.Time); err == nil {
			end, margin, to = t, clockSkewMargin, "the running instance's clock, less a "+clockSkewMargin.String()+" allowance for clock difference"
		}
	}
	if end.IsZero() {
		t, err := time.Parse(time.RFC3339, wal.ArchiveEndTime)
		if err != nil {
			return 0, "", false
		}
		end, to = t, "the end of the archive"
	}
	d := end.Sub(at) - margin
	if d <= 0 {
		return 0, "", false
	}
	return int64(d / time.Second), fmt.Sprintf("the segment holding the target (%s) reached the repository at %s and nothing is archived before it is written; measured to %s",
		segmentName(tli, segno, wal.SegmentSizeBytes), at.UTC().Format(time.RFC3339), to), true
}

// estimateDataLoss measures what restoring to target discards. normalized is
// the target as PostgreSQL takes it (the LSN for an LSN target). wal is the
// continuity check of the backup to be restored; live is what the running
// instance reported about itself, nil when it is down or could not be asked.
func estimateDataLoss(target RecoveryTarget, normalized string, wal *WALContinuity, live *LivePreState, asOf time.Time) *DataLossEstimate {
	est := &DataLossEstimate{
		AsOf:       asOf.UTC().Format(time.RFC3339),
		Unmeasured: []string{"the number of transactions (needs the WAL decoded)", "which of the discarded writes were wanted: everything after the target counts"},
	}

	switch target.Type {
	case TargetTime:
		tt, err := time.Parse(time.RFC3339, strings.TrimSpace(target.Value))
		if err != nil {
			return nil
		}
		end, basis, ok := lossEndTime(wal, live)
		if !ok {
			return nil
		}
		est.Basis, est.To = basis, end.UTC().Format(time.RFC3339)
		est.UpperBound = basis == lossBasisArchive
		secs := int64(end.Sub(tt).Round(time.Second) / time.Second)
		if secs < 0 {
			secs = 0
		}
		est.WindowSeconds = &secs
		est.Unmeasured = append(est.Unmeasured, "the WAL volume (a time has no position until the WAL is read)")

	case TargetLSN:
		lsn, err := parseLSN(normalized)
		if err != nil {
			return nil
		}
		if live != nil {
			cur, err := parseLSN(live.LSN)
			if err != nil {
				return nil
			}
			est.Basis = lossBasisLive
			var n uint64
			if cur > lsn {
				n = cur - lsn
			}
			est.WALBytes = &n
			if t, err := time.Parse(time.RFC3339, live.Time); err == nil {
				est.To = t.UTC().Format(time.RFC3339)
			}
		} else {
			if wal == nil {
				return nil
			}
			_, logID, seg, err := parseSegmentName(wal.ArchiveEndWAL)
			if err != nil || !validSegmentSize(wal.SegmentSizeBytes) {
				return nil
			}
			endSeg := logID*segsPerLogID(wal.SegmentSizeBytes) + seg
			targetSeg := lsn / wal.SegmentSizeBytes
			est.Basis, est.UpperBound = lossBasisArchive, true
			est.To = wal.ArchiveEndTime
			var n, segs uint64
			if targetSeg <= endSeg {
				n = (endSeg+1)*wal.SegmentSizeBytes - lsn
				segs = endSeg - targetSeg
			}
			est.WALBytes, est.Segments = &n, &segs
		}
		if secs, basis, ok := lossAtLeast(wal, lsn, live); ok {
			est.WindowAtLeastSeconds, est.AtLeastBasis = &secs, basis
			est.Unmeasured = append(est.Unmeasured, "the exact time span (an LSN has no time until the WAL is read: only a lower bound is known)")
		} else {
			est.Unmeasured = append(est.Unmeasured, "the time span (an LSN has no time until the WAL is read)")
		}

	default:
		return nil
	}
	est.Summary = est.summarize()
	return est
}

// lossEndTime is the end of the history a time-target restore discards.
func lossEndTime(wal *WALContinuity, live *LivePreState) (time.Time, string, bool) {
	if live != nil {
		if t, err := time.Parse(time.RFC3339, live.Time); err == nil {
			return t, lossBasisLive, true
		}
	}
	if wal != nil {
		if t, err := time.Parse(time.RFC3339, wal.ArchiveEndTime); err == nil {
			return t, lossBasisArchive, true
		}
	}
	return time.Time{}, "", false
}

func (e *DataLossEstimate) summarize() string {
	// Each figure says which way it can be wrong: "at most" for an upper bound,
	// "at least" for a lower bound, nothing for an exact one.
	atMost := func(s string) string {
		if e.UpperBound {
			return "at most " + s
		}
		return s
	}
	var parts []string
	if e.WindowSeconds != nil {
		parts = append(parts, atMost(humanSpan(*e.WindowSeconds)+" of history"))
	}
	if e.WALBytes != nil {
		parts = append(parts, atMost(humanBytes(*e.WALBytes)+" of WAL"))
	}
	if e.WindowAtLeastSeconds != nil {
		parts = append(parts, "at least "+humanSpan(*e.WindowAtLeastSeconds)+" of history")
	}
	what := strings.Join(parts, " and ")
	txns := "Transactions are not counted."
	if e.Transactions != nil {
		txns = e.Transactions.Sentence()
	}
	switch e.Basis {
	case lossBasisLive:
		return fmt.Sprintf("%s discarded, up to the running instance's position at %s; it grows until the instance is stopped. %s", what, e.AsOf, txns)
	default:
		return fmt.Sprintf("%s discarded, up to the end of the archive; WAL the instance had not archived is not counted. %s", what, txns)
	}
}

// withTransactions attaches the count of discarded transactions and brings the
// rest of the estimate into line with it: a count that was made is no longer
// "unmeasured", and the summary says it. A nil count changes nothing.
func (e *DataLossEstimate) withTransactions(t *TransactionLoss) *DataLossEstimate {
	if e == nil || t == nil {
		return e
	}
	e.Transactions = t
	if t.Status == TxLossMeasured {
		kept := e.Unmeasured[:0:0]
		for _, u := range e.Unmeasured {
			if !strings.HasPrefix(u, "the number of transactions") {
				kept = append(kept, u)
			}
		}
		if t.AtLeast {
			kept = append(kept, "all of the transactions (the count is a lower bound: see transactions.basis)")
		}
		e.Unmeasured = kept
	}
	e.Summary = e.summarize()
	return e
}

// humanSpan renders seconds as "1h 05m 12s".
func humanSpan(s int64) string {
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh %02dm %02ds", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%dd %02dh %02dm", s/86400, s%86400/3600, s%3600/60)
}

// humanBytes renders a byte count with a binary unit.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
