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
// (pg_waldump over the discarded segments), which is a later tier; the
// estimate says so rather than leaving it out silently. It also cannot tell
// the bad writes from the good ones: it counts everything after the target.

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
	// Segments is how many whole WAL segments lie after the target's own.
	// Only for an LSN target on the archive.
	Segments *uint64 `json:"segments,omitempty"`
	// UpperBound says the figures are at most this much, not exactly this
	// much (the archive basis).
	UpperBound bool `json:"upper_bound"`
	// Unmeasured lists what the estimate does not count.
	Unmeasured []string `json:"unmeasured,omitempty"`
	// Summary is the estimate in one sentence a person can read at the gate.
	Summary string `json:"summary"`
	// Objective sets the estimate against the database's recovery point
	// objective (pitr_rpo.go). Absent when the estimate was made without
	// asking which database it is for.
	Objective *RPOComparison `json:"objective,omitempty"`
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
		est.Unmeasured = append(est.Unmeasured, "the time span (an LSN has no time until the WAL is read)")

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
	var parts []string
	if e.WindowSeconds != nil {
		parts = append(parts, humanSpan(*e.WindowSeconds)+" of history")
	}
	if e.WALBytes != nil {
		parts = append(parts, humanBytes(*e.WALBytes)+" of WAL")
	}
	what := strings.Join(parts, " and ")
	switch e.Basis {
	case lossBasisLive:
		return fmt.Sprintf("%s discarded, up to the running instance's position at %s; it grows until the instance is stopped. Transactions are not counted.", what, e.AsOf)
	default:
		return fmt.Sprintf("at most %s discarded, up to the end of the archive; WAL the instance had not archived is not counted. Transactions are not counted.", what)
	}
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
