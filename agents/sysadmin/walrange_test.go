package main

import (
	"strings"
	"testing"
	"time"
)

const rangeSeg = uint64(16 << 20)

var rangeT0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// archiveOf builds an archive whose segments first..last arrive a minute apart
// from rangeT0, on timeline tl until fork (exclusive) and fork's timeline after.
func archiveOf(first, last, fork uint64, tlBefore, tlAfter uint32) *WALContinuity {
	segs := map[uint64]archivedSegment{}
	for s := first; s <= last; s++ {
		tl := tlBefore
		if fork != 0 && s >= fork {
			tl = tlAfter
		}
		segs[s] = archivedSegment{timeline: tl, time: rangeT0.Add(time.Duration(s-first) * time.Minute).Unix()}
	}
	end := segs[last]
	return &WALContinuity{
		SegmentSizeBytes: rangeSeg, ArchiveEndWAL: segmentName(end.timeline, last, rangeSeg), segments: segs,
	}
}

func timeTarget(t time.Time) RecoveryTarget {
	return RecoveryTarget{Type: TargetTime, Value: t.Format(time.RFC3339)}
}

func liveAt(lsn string, tl uint32, dir string) *LivePreState {
	return &LivePreState{LSN: lsn, Timeline: tl, DataDirectory: dir}
}

func names(p WALReadPlan) string {
	var parts []string
	for _, r := range p.Ranges {
		s := r.Source + ":" + r.First[8:] + "-" + r.Last[8:]
		if r.StartLSN != "" {
			s += " from " + r.StartLSN
		}
		if r.EndLSN != "" {
			s += " to " + r.EndLSN
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " | ")
}

// A time target starts at the first segment that reached the repository at or
// after it: nothing that arrived earlier can hold a later commit. A range that
// crosses a point-in-time restore is read one timeline at a time.
func TestPlanWALRead_TimeTargetStartsAtTheFirstSegmentArrivingAfterIt(t *testing.T) {
	// Segments 0x40..0x45, a minute apart; timeline 2 up to 0x42, timeline 3 from 0x43.
	wal := archiveOf(0x40, 0x45, 0x43, 2, 3)
	// Between the arrival of 0x41 (12:01) and 0x42 (12:02): the stop is in 0x42 or later.
	p, why := planWALRead(timeTarget(rangeT0.Add(90*time.Second)), "", wal, nil, 16)
	if why != "" {
		t.Fatal(why)
	}
	if got := names(p); got != "archive:0000000000000042-0000000000000042 | archive:0000000000000043-0000000000000045" {
		t.Errorf("ranges = %s", got)
	}
	if p.Segments != 4 || p.Truncated || p.Mode != "time" || p.Serving || p.Complete() {
		t.Errorf("%+v", p)
	}
	if !strings.Contains(p.Start, "00000002") || !strings.Contains(p.Start, "0000000000000042") {
		t.Errorf("start = %q", p.Start)
	}
	// Exactly when a segment arrived: that segment counts (it may hold the stop).
	p, _ = planWALRead(timeTarget(rangeT0.Add(2*time.Minute)), "", wal, nil, 16)
	if !strings.HasPrefix(names(p), "archive:0000000000000042-") {
		t.Errorf("at the arrival time of 0x42: %s", names(p))
	}
	// A second later: 0x43 is the first that arrived after.
	p, _ = planWALRead(timeTarget(rangeT0.Add(2*time.Minute+time.Second)), "", wal, nil, 16)
	if got := names(p); got != "archive:0000000000000043-0000000000000045" {
		t.Errorf("a second after: %s", got)
	}
	// Before everything: from the first segment.
	p, _ = planWALRead(timeTarget(rangeT0.Add(-time.Hour)), "", wal, nil, 16)
	if !strings.HasPrefix(names(p), "archive:0000000000000040-0000000000000042") || p.Segments != 6 {
		t.Errorf("before everything: %s (%d)", names(p), p.Segments)
	}
}

// A serving instance adds the WAL it has not archived, read from its own pg_wal
// and bounded at its own position; the read is then complete.
func TestPlanWALRead_ServingInstanceAddsItsOwnUnarchivedWAL(t *testing.T) {
	wal := archiveOf(0x40, 0x42, 0, 3, 3)
	live := liveAt("0/45000100", 3, "/var/lib/postgresql/16/main")
	p, why := planWALRead(timeTarget(rangeT0.Add(90*time.Second)), "", wal, live, 16)
	if why != "" {
		t.Fatal(why)
	}
	if got := names(p); got != "archive:0000000000000042-0000000000000042 | pg_wal:0000000000000043-0000000000000045 to 0/45000100" {
		t.Errorf("ranges = %s", got)
	}
	if !p.Serving || !p.Complete() || p.Segments != 4 {
		t.Errorf("%+v", p)
	}
	// Only the last pg_wal range is bounded: whole segments before it need no bound.
	if p.Ranges[0].EndLSN != "" {
		t.Errorf("an archived range was bounded: %+v", p.Ranges[0])
	}

	// A target newer than every archived segment: only the tail can hold the stop.
	p, why = planWALRead(timeTarget(rangeT0.Add(time.Hour)), "", wal, live, 16)
	if why != "" || names(p) != "pg_wal:0000000000000043-0000000000000045 to 0/45000100" {
		t.Errorf("%s (%s)", names(p), why)
	}
	if !strings.Contains(p.Start, "only WAL the instance has not archived yet") {
		t.Errorf("start = %q", p.Start)
	}
	// Without a live instance there is nothing it could read.
	if _, why := planWALRead(timeTarget(rangeT0.Add(time.Hour)), "", wal, nil, 16); !strings.Contains(why, "no archived segment reached the repository at or after the target time") {
		t.Errorf("why = %q", why)
	}
}

// An LSN target starts in the segment that holds it, from the LSN itself.
func TestPlanWALRead_LSNTarget(t *testing.T) {
	wal := archiveOf(0x40, 0x45, 0, 3, 3)
	p, why := planWALRead(RecoveryTarget{Type: TargetLSN}, "0/42000A28", wal, nil, 16)
	if why != "" {
		t.Fatal(why)
	}
	if got := names(p); got != "archive:0000000000000042-0000000000000045 from 0/42000A28" || p.Mode != "lsn" || p.TargetLSN != 0x42000A28 {
		t.Errorf("%s %+v", got, p)
	}
	// In the instance's own WAL, past the archive.
	live := liveAt("0/47000010", 3, "/data")
	p, why = planWALRead(RecoveryTarget{Type: TargetLSN}, "0/46000100", wal, live, 16)
	if why != "" || names(p) != "pg_wal:0000000000000046-0000000000000047 from 0/46000100 to 0/47000010" {
		t.Errorf("%s (%s)", names(p), why)
	}
	if _, why := planWALRead(RecoveryTarget{Type: TargetLSN}, "0/46000100", wal, nil, 16); !strings.Contains(why, "past the end of the archive") {
		t.Errorf("why = %q", why)
	}
	// In a gap or before the archive: not in it.
	if _, why := planWALRead(RecoveryTarget{Type: TargetLSN}, "0/10000000", wal, nil, 16); !strings.Contains(why, "not in the archive") {
		t.Errorf("why = %q", why)
	}
}

// The cap stops a read short and says so: the count is then a lower bound, and a
// serving instance's tail is not reached.
func TestPlanWALRead_TheCapMakesTheCountALowerBound(t *testing.T) {
	wal := archiveOf(0x40, 0x49, 0, 3, 3) // ten segments
	live := liveAt("0/4B000000", 3, "/data")
	p, why := planWALRead(timeTarget(rangeT0.Add(-time.Hour)), "", wal, live, 4)
	if why != "" || p.Segments != 4 || !p.Truncated || p.Complete() || names(p) != "archive:0000000000000040-0000000000000043" {
		t.Errorf("%s %+v (%s)", names(p), p, why)
	}
	// The archive fits exactly, and the tail does not: the tail is what is cut.
	wal = archiveOf(0x40, 0x43, 0, 3, 3)
	p, _ = planWALRead(timeTarget(rangeT0.Add(-time.Hour)), "", wal, liveAt("0/46000000", 3, "/data"), 4)
	if !p.Truncated || p.Segments != 4 || len(p.Ranges) != 1 {
		t.Errorf("the whole cap used by the archive: %s %+v", names(p), p)
	}
	// The tail alone is cut at the cap, and is then not bounded at the instance's position.
	p, _ = planWALRead(timeTarget(rangeT0.Add(time.Hour)), "", archiveOf(0x40, 0x41, 0, 3, 3), liveAt("0/4F000000", 3, "/data"), 3)
	if !p.Truncated || p.Segments != 3 || names(p) != "pg_wal:0000000000000042-0000000000000044" {
		t.Errorf("tail cut: %s %+v", names(p), p)
	}
	// Within the cap nothing is truncated, and a zero cap means the default.
	p, _ = planWALRead(timeTarget(rangeT0.Add(-time.Hour)), "", archiveOf(0x40, 0x42, 0, 3, 3), nil, 0)
	if p.Truncated || p.Segments != 3 {
		t.Errorf("default cap: %+v", p)
	}
}

// Without a data directory the tail cannot be read, and the read says it is short.
func TestPlanWALRead_NoDataDirectorySkipsTheTailAndSaysSo(t *testing.T) {
	wal := archiveOf(0x40, 0x42, 0, 3, 3)
	p, why := planWALRead(timeTarget(rangeT0.Add(-time.Hour)), "", wal, liveAt("0/44000000", 3, ""), 16)
	if why != "" || names(p) != "archive:0000000000000040-0000000000000042" || !strings.Contains(p.TailSkipped, "data directory is not known") || p.Complete() {
		t.Errorf("%s %+v (%s)", names(p), p, why)
	}
}

func TestPlanWALRead_WhatCannotBePlanned(t *testing.T) {
	wal := archiveOf(0x40, 0x42, 0, 3, 3)
	tgt := timeTarget(rangeT0)
	noSegs := &WALContinuity{SegmentSizeBytes: rangeSeg, ArchiveEndWAL: segmentName(3, 0x42, rangeSeg)}
	badEnd := &WALContinuity{SegmentSizeBytes: rangeSeg, ArchiveEndWAL: "junk", segments: wal.segments}
	gap := archiveOf(0x40, 0x44, 0, 3, 3)
	delete(gap.segments, 0x42)
	for name, tc := range map[string]struct {
		target RecoveryTarget
		norm   string
		wal    *WALContinuity
		live   *LivePreState
		want   string
	}{
		"no continuity check": {tgt, "", nil, nil, "segment size is not known"},
		"a bad segment size":  {tgt, "", &WALContinuity{SegmentSizeBytes: 12345, segments: wal.segments}, nil, "segment size is not known"},
		"no segment times":    {tgt, "", noSegs, nil, "segments and their times were not read"},
		"no archive end":      {tgt, "", badEnd, nil, "end of the archive is not known"},
		"a bad time":          {RecoveryTarget{Type: TargetTime, Value: "yesterday"}, "", wal, nil, "could not be read"},
		"a bad LSN":           {RecoveryTarget{Type: TargetLSN}, "nonsense", wal, nil, "could not be read"},
		"a named point":       {RecoveryTarget{Type: "name"}, "", wal, nil, "only a time or an LSN target"},
		"a gap":               {timeTarget(rangeT0.Add(-time.Hour)), "", gap, nil, "missing from the archive"},
		"no WAL after":        {RecoveryTarget{Type: TargetLSN}, "0/40000000", archiveOf(0x40, 0x40, 0, 3, 3), nil, ""},
	} {
		p, why := planWALRead(tc.target, tc.norm, tc.wal, tc.live, 16)
		if tc.want == "" {
			if why != "" || len(p.Ranges) != 1 {
				t.Errorf("%s: %s %+v", name, why, p)
			}
			continue
		}
		if !strings.Contains(why, tc.want) || len(p.Ranges) != 0 && name != "a gap" {
			t.Errorf("%s: why = %q (ranges %d), want %q", name, why, len(p.Ranges), tc.want)
		}
	}
	// A target LSN after the instance's own position leaves nothing to read.
	if _, why := planWALRead(RecoveryTarget{Type: TargetLSN}, "0/50000000", wal, liveAt("0/44000000", 3, "/d"), 16); !strings.Contains(why, "no WAL after the target") {
		t.Errorf("why = %q", why)
	}
}

// The planner's assumptions about the segment map (keyed by segment number, each
// on the timeline the replay path reads it from, with the time it reached the
// repository) hold for the map the real continuity check builds from a real
// repository listing, here the captured one that spans 30 timelines.
func TestPlanWALRead_FromTheRealContinuityOfACapturedRepository(t *testing.T) {
	wal := evaluateWALContinuity(multiTimelineInput(t), readFixture(t, "multi_timeline_top.txt"),
		readFixture(t, "multi_timeline_0000001E.history"), readFixture(t, "multi_timeline_listing.json"))
	if wal.Status != WALContinuous || len(wal.segments) == 0 {
		t.Fatalf("the captured repository did not give a segment map: %+v", wal)
	}
	size := wal.SegmentSizeBytes
	all := archivedSegnos(&wal, ^uint64(0))
	first, last := all[0], all[len(all)-1]

	// Every name the plan produces is one the repository has: the timeline in the
	// name is the one the map holds for that segment number.
	check := func(p WALReadPlan) {
		t.Helper()
		n := 0
		for _, r := range p.Ranges {
			if r.Source != WALFromArchive {
				t.Fatalf("a captured archive gave a %s range", r.Source)
			}
			for _, name := range r.Names {
				tli, logID, seg, err := parseSegmentName(name)
				if err != nil {
					t.Fatal(err)
				}
				segno := logID*segsPerLogID(size) + seg
				if wal.segments[segno].timeline != tli {
					t.Errorf("%s: the map has segment %d on timeline %d", name, segno, wal.segments[segno].timeline)
				}
				n++
			}
		}
		if n != p.Segments {
			t.Errorf("%d names for %d segments", n, p.Segments)
		}
	}

	// From the first segment's own LSN: every segment of the archive, in order, on
	// the timelines the map gives, which is more than one run when the timeline changes.
	p, why := planWALRead(RecoveryTarget{Type: TargetLSN}, lsnText(first*size), &wal, nil, 1000)
	if why != "" {
		t.Fatal(why)
	}
	check(p)
	if p.Segments != len(all) || p.Truncated {
		t.Errorf("%d segments of %d (truncated %v)", p.Segments, len(all), p.Truncated)
	}
	var runs []string
	for _, r := range p.Ranges {
		runs = append(runs, r.First[:8]+"-"+r.Last[:8])
	}
	t.Logf("%d segment(s) in %d run(s): %v", p.Segments, len(p.Ranges), runs)

	// From the time the last segment reached the repository: that segment and no
	// earlier one, since nothing earlier can hold a later commit.
	endTime := time.Unix(wal.segments[last].time, 0).UTC()
	p, why = planWALRead(timeTarget(endTime), "", &wal, nil, 1000)
	if why != "" {
		t.Fatal(why)
	}
	check(p)
	if p.Ranges[0].First != segmentName(wal.segments[last].timeline, last, size) {
		t.Errorf("started at %s, want the last segment %s", p.Ranges[0].First, segmentName(wal.segments[last].timeline, last, size))
	}
}

func TestLSNText(t *testing.T) {
	for in, want := range map[uint64]string{0: "0/0", 0x42000A28: "0/42000A28", 0x1_0000_0010: "1/10", 0xABCD_1234_5678_9ABC: "ABCD1234/56789ABC"} {
		if got := lsnText(in); got != want {
			t.Errorf("lsnText(%#x) = %q, want %q", in, got, want)
		}
	}
	// And it parses back to the same value.
	for _, v := range []uint64{0, 0x42000A28, 0x1_0000_0010, 0xABCD_1234_5678_9ABC} {
		if got, err := parseLSN(lsnText(v)); err != nil || got != v {
			t.Errorf("round trip of %#x = %#x, %v", v, got, err)
		}
	}
}
