package main

// Which WAL to read to count what a restore discards (v0.35, Tier B).
//
// The discarded history runs from where recovery would stop to the end of the
// instance's WAL. This works out, from what the plan already knows, the
// segments that hold it: no I/O, so every rule can be tested.
//
// Where recovery stops decides what is lost, so it follows PostgreSQL's rules:
//
//   - a TIME target stops before the first commit or abort record, in WAL
//     order, whose timestamp is later than the target. Everything after that
//     record is lost, including later commits with earlier timestamps (commit
//     times are not strictly ordered in the log).
//   - an LSN target applies the first record that starts at or after the target
//     and stops there; everything after it is lost.
//
// Where to START reading follows from the archive: a segment reaches the
// repository only after everything in it was written, so a commit later than
// the target time cannot be in a segment that arrived before the target. The
// first segment that arrived at or after the target time is therefore the
// earliest that can hold the stop, and nothing before it is read.
//
// Where to END: the archive's newest segment, and, for a serving instance, the
// WAL it has not archived yet, read from its own pg_wal up to its own position
// (bounded: past that, a file in pg_wal may be a recycled one that holds no
// WAL at all, see waldump.go).
//
// Segments are named by timeline, and the archive's replay path already says
// which timeline each segment number is read from, so a range that crosses a
// point-in-time restore is read one timeline at a time.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Where a stretch of WAL is read from.
const (
	WALFromArchive = "archive"
	WALFromPgWal   = "pg_wal"
)

// walReadMaxSegments is how many segments one plan reads before it stops and
// says the count is "at least": 16 segments is 256 MiB of WAL at the default
// size. A starting figure, to be set from a measured read on a real host.
const walReadMaxSegments = 16

// WALReadRange is one pg_waldump invocation: consecutive segments of one
// timeline, from one place.
type WALReadRange struct {
	Source string
	// First and Last are segment names, inclusive.
	First, Last string
	// StartLSN and EndLSN bound the read inside those segments; either may be
	// empty.
	StartLSN, EndLSN string
	Segments         int
	// Names are the segments to fetch from the archive, in order. Empty for a
	// range read in place from pg_wal.
	Names []string
}

// WALReadPlan is what to read, in WAL order.
type WALReadPlan struct {
	// Mode is "time" or "lsn": how the stop point is found.
	Mode       string
	TargetTime time.Time
	TargetLSN  uint64
	// Ranges are read in order.
	Ranges   []WALReadRange
	Segments int
	// Truncated says the read stops short of the end because of the cap on the
	// number of segments: the count is then a lower bound.
	Truncated bool
	// TailSkipped says why the instance's own unarchived WAL is not read when it
	// could have been (its data directory is unknown).
	TailSkipped string
	// Serving says the end of the read is the running instance's own position, so
	// nothing it had not yet written is missing. False: the end is the archive's,
	// and WAL the instance never archived is not counted.
	Serving bool
	// Start says where the read starts and why, in a sentence.
	Start string
}

// Complete says the read reaches the end of what the instance has written (as of
// when it was asked). Anything else is a lower bound.
func (p WALReadPlan) Complete() bool {
	return p.Serving && !p.Truncated && p.TailSkipped == ""
}

// lsnText is an LSN in PostgreSQL's notation.
func lsnText(v uint64) string { return fmt.Sprintf("%X/%X", v>>32, v&0xFFFFFFFF) }

// planWALRead works out the segments to read. live is what the running instance
// reported about itself (nil when it is down or could not be asked). When there
// is nothing that can be read, reason says why and the plan is not usable.
func planWALRead(target RecoveryTarget, normalized string, wal *WALContinuity, live *LivePreState, max int) (WALReadPlan, string) {
	var p WALReadPlan
	if wal == nil || !validSegmentSize(wal.SegmentSizeBytes) {
		return p, "the archive's WAL segment size is not known"
	}
	if wal.segments == nil {
		return p, "the archive's segments and their times were not read"
	}
	size := wal.SegmentSizeBytes
	_, logID, seg, err := parseSegmentName(wal.ArchiveEndWAL)
	if err != nil {
		return p, "the end of the archive is not known"
	}
	endSeg := logID*segsPerLogID(size) + seg

	var (
		liveLSN uint64
		liveOK  bool
	)
	if live != nil {
		if l, err := parseLSN(live.LSN); err == nil {
			liveLSN, liveOK = l, true
		}
	}
	p.Serving = liveOK
	liveSeg := liveLSN / size

	var start uint64
	switch target.Type {
	case TargetTime:
		tt, err := time.Parse(time.RFC3339, strings.TrimSpace(target.Value))
		if err != nil {
			return p, fmt.Sprintf("the target time %q could not be read", target.Value)
		}
		p.Mode, p.TargetTime = "time", tt
		segnos := archivedSegnos(wal, endSeg)
		found := false
		for _, s := range segnos {
			if wal.segments[s].time >= tt.Unix() {
				start, found = s, true
				break
			}
		}
		switch {
		case found:
			p.Start = fmt.Sprintf("segment %s, the first to reach the repository at or after the target time: a commit later than the target cannot be in one that arrived before it",
				segmentName(wal.segments[start].timeline, start, size))
		case liveOK:
			start = endSeg + 1
			p.Start = "no archived segment is newer than the target time, so only WAL the instance has not archived yet can hold a commit after it"
		default:
			return p, "no archived segment reached the repository at or after the target time, and the instance's own WAL is not available"
		}
	case TargetLSN:
		lsn, err := parseLSN(normalized)
		if err != nil {
			return p, fmt.Sprintf("the target LSN %q could not be read", normalized)
		}
		p.Mode, p.TargetLSN = "lsn", lsn
		start = lsn / size
		switch {
		case start <= endSeg:
			if _, ok := wal.segments[start]; !ok {
				return p, "the segment holding the target LSN is not in the archive"
			}
			p.Start = fmt.Sprintf("segment %s, which holds the target LSN %s", segmentName(wal.segments[start].timeline, start, size), lsnText(lsn))
		case liveOK:
			p.Start = fmt.Sprintf("the instance's own WAL: the target LSN %s is past the end of the archive", lsnText(lsn))
		default:
			return p, "the target LSN is past the end of the archive, and the instance's own WAL is not available"
		}
	default:
		return p, "only a time or an LSN target has a stop point to find"
	}
	if max <= 0 {
		max = walReadMaxSegments
	}

	// Archived part: complete segments, each on the timeline the replay path
	// reads it from, grouped into runs of one timeline.
	var cur *WALReadRange
	var curTL uint32
	count := 0
	seen := start
	for ; seen <= endSeg; seen++ {
		if count >= max {
			p.Truncated = true
			break
		}
		e, ok := wal.segments[seen]
		if !ok {
			return p, fmt.Sprintf("segment number %d is missing from the archive between the target and its end", seen)
		}
		name := segmentName(e.timeline, seen, size)
		if cur != nil && e.timeline == curTL {
			cur.Last = name
			cur.Segments++
			cur.Names = append(cur.Names, name)
		} else {
			p.Ranges = append(p.Ranges, WALReadRange{Source: WALFromArchive, First: name, Last: name, Segments: 1, Names: []string{name}})
			cur, curTL = &p.Ranges[len(p.Ranges)-1], e.timeline
		}
		count++
	}

	// The instance's own unarchived WAL, read from its pg_wal up to its position.
	if liveOK && !p.Truncated {
		tailFrom := start
		if endSeg+1 > tailFrom {
			tailFrom = endSeg + 1
		}
		if tailFrom <= liveSeg {
			switch {
			case live.DataDirectory == "":
				p.TailSkipped = "the instance's data directory is not known, so the WAL it has not archived was not read"
			case count >= max:
				p.Truncated = true // the archived part used the whole cap
			default:
				first := tailFrom
				last := liveSeg
				if last-first+1 > uint64(max-count) {
					last = first + uint64(max-count) - 1
					p.Truncated = true
				}
				r := WALReadRange{Source: WALFromPgWal, First: segmentName(live.Timeline, first, size), Last: segmentName(live.Timeline, last, size), Segments: int(last - first + 1)}
				if last == liveSeg {
					r.EndLSN = lsnText(liveLSN)
				}
				p.Ranges = append(p.Ranges, r)
				count += r.Segments
			}
		}
	}
	if len(p.Ranges) == 0 {
		return p, "there is no WAL after the target to read"
	}
	if p.Mode == "lsn" {
		p.Ranges[0].StartLSN = lsnText(p.TargetLSN)
	}
	p.Segments = count
	return p, ""
}

// archivedSegnos are the segment numbers in the archive's replay path up to end,
// ascending.
func archivedSegnos(wal *WALContinuity, end uint64) []uint64 {
	out := make([]uint64, 0, len(wal.segments))
	for s := range wal.segments {
		if s <= end {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
