package main

import (
	"strings"
	"testing"
	"time"
)

var lossTarget = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// recs parses constructed Transaction records ("<kind> <lsn> <hh:mm:ss> [zone]").
func recs(t *testing.T, lines ...string) WALCommits {
	t.Helper()
	var in []string
	for i, l := range lines {
		f := strings.Fields(l)
		zone := "UTC"
		if len(f) > 3 {
			zone = f[3]
		}
		in = append(in, commitLine(100+i, f[1], f[0]+" 2026-11-09 "+f[2]+" "+zone))
	}
	got, err := ParseWALCommits(strings.NewReader(strings.Join(in, "\n") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func timePlan(complete bool) WALReadPlan {
	return WALReadPlan{Mode: "time", TargetTime: lossTarget, Serving: complete, Segments: 3, Start: "segment X"}
}

func commitsOf(t *testing.T, l *TransactionLoss) int {
	t.Helper()
	if l == nil || l.Status != TxLossMeasured || l.Commits == nil {
		t.Fatalf("not measured: %+v", l)
	}
	return *l.Commits
}

// Recovery stops before the first commit or abort later than the target, and
// everything after it is lost: counting commits with a later time would miss a
// commit that was written after the stop with an earlier timestamp.
func TestCountDiscarded_TimeTarget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  int
		first string // LSN of the first discarded commit
	}{
		{"the plain case",
			[]string{"COMMIT 0/10 11:59:58", "COMMIT 0/20 11:59:59", "COMMIT 0/30 12:00:01", "COMMIT 0/40 12:00:02"}, 2, "0/30"},
		{"a commit after the stop with an earlier time is still lost",
			[]string{"COMMIT 0/10 11:59:58", "COMMIT 0/30 12:00:01", "COMMIT 0/40 11:59:59", "COMMIT 0/50 12:00:02"}, 3, "0/30"},
		{"an abort later than the target is the stop, and is not itself counted",
			[]string{"COMMIT 0/10 11:59:59", "ABORT 0/20 12:00:01", "COMMIT 0/30 12:00:00", "COMMIT 0/40 12:00:03"}, 2, "0/30"},
		{"a commit exactly at the target is kept (inclusive)",
			[]string{"COMMIT 0/10 12:00:00", "COMMIT 0/20 12:00:01"}, 1, "0/20"},
		{"nothing later than the target: nothing lost",
			[]string{"COMMIT 0/10 11:00:00", "COMMIT 0/20 12:00:00", "ABORT 0/30 11:59:59"}, 0, ""},
		{"no records at all", nil, 0, ""},
		{"an abort earlier than the target does not stop it",
			[]string{"ABORT 0/10 11:59:00", "COMMIT 0/20 12:00:05"}, 1, "0/20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := countDiscarded(timePlan(true), []WALCommits{recs(t, tc.lines...)}, nil)
			if n := commitsOf(t, got); n != tc.want || got.FirstLSN != tc.first {
				t.Errorf("%d commits, first %q; want %d, %q", n, got.FirstLSN, tc.want, tc.first)
			}
			if got.AtLeast {
				t.Errorf("a complete read of a serving instance is a lower bound: %s", got.Basis)
			}
		})
	}
}

// The ranges are read in order and are one run of records: the stop may be in an
// earlier range and its commits in a later one.
func TestCountDiscarded_AcrossRanges(t *testing.T) {
	a := recs(t, "COMMIT 0/10 11:59:00", "COMMIT 0/20 12:00:01")
	b := recs(t, "COMMIT 0/30 12:00:02", "COMMIT 0/40 12:00:03")
	got := countDiscarded(timePlan(true), []WALCommits{a, b}, nil)
	if commitsOf(t, got) != 3 || got.FirstLSN != "0/20" || got.LastLSN != "0/40" {
		t.Errorf("%+v", got)
	}
	if got.FirstAt != "2026-11-09T12:00:01Z" || got.LastAt != "2026-11-09T12:00:03Z" {
		t.Errorf("times = %q %q", got.FirstAt, got.LastAt)
	}
	if got.SegmentsRead != 3 {
		t.Errorf("segments = %d", got.SegmentsRead)
	}
}

// A record whose time could not be read before the stop is found cannot be
// placed, and the count is not guessed. After the stop it does not matter.
func TestCountDiscarded_AnUnreadableTimeBeforeTheStopIsNotGuessed(t *testing.T) {
	before := recs(t, "COMMIT 0/10 11:59:00", "COMMIT 0/20 12:30:00 EST", "COMMIT 0/30 12:00:01")
	if got := countDiscarded(timePlan(true), []WALCommits{before}, nil); got.Status != TxLossUnavailable || !strings.Contains(got.Reason, "could not be read as UTC") || !strings.Contains(got.Reason, "0/20") {
		t.Errorf("%+v", got)
	}
	after := recs(t, "COMMIT 0/10 11:59:00", "COMMIT 0/20 12:00:01", "COMMIT 0/30 12:30:00 EST")
	got := countDiscarded(timePlan(true), []WALCommits{after}, nil)
	if commitsOf(t, got) != 2 || got.LastAt != "" || got.LastLSN != "0/30" || got.FirstAt == "" {
		t.Errorf("after the stop: %+v", got)
	}
}

// What the count rests on, and whether it is a lower bound, follow from how far
// the read got.
func TestCountDiscarded_ALowerBoundSaysSo(t *testing.T) {
	r := []WALCommits{recs(t, "COMMIT 0/10 12:00:01")}
	for _, tc := range []struct {
		name    string
		plan    WALReadPlan
		unread  int
		atLeast bool
		basis   string
	}{
		{"serving, to the instance's own position", func() WALReadPlan { p := timePlan(true); return p }(), 0, false, "up to the running instance's own position; it grows until the instance is stopped"},
		{"a down instance: the end of the archive", timePlan(false), 0, true, "WAL the instance had not archived is not counted"},
		{"cut at the cap", func() WALReadPlan { p := timePlan(true); p.Truncated = true; p.Segments = 16; return p }(), 0, true, "stopped after 16 segments"},
		{"no data directory", func() WALReadPlan {
			p := timePlan(true)
			p.TailSkipped = "the instance's data directory is not known"
			return p
		}(), 0, true, "data directory is not known, so there may be more"},
		{"a record not understood", timePlan(true), 2, true, "2 transaction record(s) were not understood"},
	} {
		parts := r
		if tc.unread > 0 {
			parts = []WALCommits{{Ends: r[0].Ends, Commits: r[0].Commits, Unread: tc.unread}}
		}
		got := countDiscarded(tc.plan, parts, nil)
		if commitsOf(t, got) != 1 || got.AtLeast != tc.atLeast || !strings.Contains(got.Basis, tc.basis) || !strings.Contains(got.Basis, "segment X") {
			t.Errorf("%s: at least %v, basis %q", tc.name, got.AtLeast, got.Basis)
		}
	}
}

// An LSN target applies the first record at or after it and stops after it: a
// commit that is that record is kept.
func TestCountDiscarded_LSNTarget(t *testing.T) {
	plan := WALReadPlan{Mode: "lsn", TargetLSN: 0x1000, Serving: true, Segments: 1, Start: "segment Y"}
	rs := []WALCommits{recs(t, "COMMIT 0/900 11:00:00", "COMMIT 0/1000 11:30:00", "COMMIT 0/1100 12:00:00", "COMMIT 0/1200 12:30:00")}
	first := func(v uint64) *uint64 { return &v }

	// The target is a record start and that record is a commit: it is kept.
	got := countDiscarded(plan, rs, first(0x1000))
	if commitsOf(t, got) != 2 || got.FirstLSN != "0/1100" || !strings.Contains(got.Basis, "first record at or after the target LSN (0/1000)") {
		t.Errorf("%+v", got)
	}
	// The target is not a record start: the next record (not a commit) is applied.
	if got := countDiscarded(plan, rs, first(0x1080)); commitsOf(t, got) != 2 || got.FirstLSN != "0/1100" {
		t.Errorf("a boundary record that is not a commit: %+v", got)
	}
	// The boundary record is itself a commit: kept, so one fewer is lost.
	if got := countDiscarded(plan, rs, first(0x1100)); commitsOf(t, got) != 1 || got.FirstLSN != "0/1200" {
		t.Errorf("a boundary record that is a commit: %+v", got)
	}
	// Not identified: fall back to commits after the target, and say it may be one too many.
	got = countDiscarded(plan, rs, nil)
	if commitsOf(t, got) != 2 || !strings.Contains(got.Basis, "may include one commit that recovery keeps") {
		t.Errorf("no boundary: %+v", got)
	}
	// Nothing after.
	if got := countDiscarded(plan, []WALCommits{recs(t, "COMMIT 0/900 11:00:00")}, first(0x1000)); commitsOf(t, got) != 0 || got.FirstLSN != "" {
		t.Errorf("nothing after: %+v", got)
	}
	if got := countDiscarded(WALReadPlan{}, rs, nil); got.Status != TxLossUnavailable {
		t.Errorf("no mode: %+v", got)
	}
}

func TestTransactionLossSentence(t *testing.T) {
	n := func(v int) *int { return &v }
	for _, tc := range []struct {
		in   *TransactionLoss
		want string
	}{
		{nil, ""},
		{&TransactionLoss{Status: TxLossMeasured, Commits: n(0)}, "0 transactions committed after the target would be discarded (counted from the WAL)."},
		{&TransactionLoss{Status: TxLossMeasured, Commits: n(1)}, "1 transaction committed after the target would be discarded (counted from the WAL)."},
		{&TransactionLoss{Status: TxLossMeasured, Commits: n(41), AtLeast: true}, "At least 41 transactions committed after the target would be discarded (counted from the WAL)."},
		{&TransactionLoss{Status: TxLossUnavailable, Reason: "pg_waldump is not installed"}, "Transactions could not be counted (pg_waldump is not installed)."},
		{&TransactionLoss{Status: TxLossMeasured}, "Transactions could not be counted ()."},
	} {
		if got := tc.in.Sentence(); got != tc.want {
			t.Errorf("%q, want %q", got, tc.want)
		}
	}
}
