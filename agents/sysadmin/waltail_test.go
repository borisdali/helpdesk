package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"
)

const mib16 = 16 << 20

// tailSeg is a copied-back segment with a real first page header.
func tailSeg(name string) tailSegment {
	return tailSegment{Name: name, Header: syntheticSegmentHeader(binary.LittleEndian, name, mib16)[:walLongHeaderLen]}
}

// recycledSeg is a segment PostgreSQL pre-created by renaming an old one: its
// header still belongs to oldName.
func recycledSeg(name, oldName string) tailSegment {
	return tailSegment{Name: name, Header: syntheticSegmentHeader(binary.LittleEndian, oldName, mib16)[:walLongHeaderLen]}
}

// The canary run on the Docker fixture (2026-10-19), second half: archive end
// …0A on timeline 7; pg_wal held …09 and …0A (archived), …0B (.ready, row A),
// …0C (open, row B). Recovery replayed through …0C; only …0B needed pushing to the archive —
// PostgreSQL archived …0C itself as .partial at promotion.
func TestPlanWALTail_CanaryRun(t *testing.T) {
	wal := &WALContinuity{Status: WALContinuous, ArchiveEndWAL: "00000007000000000000000A", LatestTimeline: 7, SegmentSizeBytes: mib16}
	segs := []tailSegment{
		tailSeg("000000070000000000000009"), tailSeg("00000007000000000000000A"),
		tailSeg("00000007000000000000000B"), tailSeg("00000007000000000000000C"),
		recycledSeg("00000007000000000000000D", "000000070000000000000005"),
	}
	tail := planWALTail("/var/lib/postgresql/pg_wal.saved-x", segs, wal)
	if tail.ReplayThroughWAL != "00000007000000000000000C" {
		t.Errorf("ReplayThroughWAL = %q, want …0C", tail.ReplayThroughWAL)
	}
	if !slices.Equal(tail.PushedToArchive, []string{"00000007000000000000000B"}) {
		t.Errorf("PushedToArchive = %v, want only …0B (…0C is archived as .partial by PostgreSQL, the recycled …0D never)", tail.PushedToArchive)
	}
	if len(tail.CopiedBack) != 5 || tail.SavedTo == "" {
		t.Errorf("CopiedBack/SavedTo = %v/%q, want all 5 segments and the saved dir", tail.CopiedBack, tail.SavedTo)
	}
}

func TestPlanWALTail_WhatCounts(t *testing.T) {
	wal := &WALContinuity{Status: WALContinuous, ArchiveEndWAL: "00000007000000000000000A", LatestTimeline: 7, SegmentSizeBytes: mib16}
	cases := []struct {
		name        string
		wal         *WALContinuity
		segs        []tailSegment
		wantThrough string
		wantPushed  []string
	}{
		{"nothing newer than the archive", wal,
			[]tailSegment{tailSeg("000000070000000000000009"), tailSeg("00000007000000000000000A")}, "", nil},
		{"a recycled segment ends the tail", wal,
			[]tailSegment{tailSeg("00000007000000000000000B"), recycledSeg("00000007000000000000000C", "000000070000000000000004"),
				tailSeg("00000007000000000000000D")}, "00000007000000000000000B", nil},
		{"a gap right after the archive end: recovery can't reach the rest", wal,
			[]tailSegment{tailSeg("00000007000000000000000C"), tailSeg("00000007000000000000000D")}, "", nil},
		{"older timeline segments are not the server's", wal,
			[]tailSegment{tailSeg("00000006000000000000000B"), tailSeg("00000006000000000000000C")}, "", nil},
		{"three past the end: all but the newest pushed", wal,
			[]tailSegment{tailSeg("00000007000000000000000B"), tailSeg("00000007000000000000000C"), tailSeg("00000007000000000000000D")},
			"00000007000000000000000D", []string{"00000007000000000000000B", "00000007000000000000000C"}},
		// The VM, 2026-10-18: archive end on timeline 27, the server on 29. The
		// server's own copy of segment …0C holds WAL the archived one doesn't.
		{"server's copy of the archive end's segment on a newer timeline",
			&WALContinuity{Status: WALContinuous, ArchiveEndWAL: "0000001B000000000000000C", LatestTimeline: 29, SegmentSizeBytes: mib16},
			[]tailSegment{tailSeg("0000001D000000000000000C")}, "0000001D000000000000000C", nil},
		{"wrong segment size in the header", &WALContinuity{Status: WALContinuous, ArchiveEndWAL: "00000007000000000000000A", LatestTimeline: 7, SegmentSizeBytes: 64 << 20},
			[]tailSegment{tailSeg("00000007000000000000000B")}, "", nil},
	}
	for _, c := range cases {
		tail := planWALTail("/saved", c.segs, c.wal)
		if tail.ReplayThroughWAL != c.wantThrough || !slices.Equal(tail.PushedToArchive, c.wantPushed) {
			t.Errorf("%s: through %q pushed %v, want %q %v (detail: %s)", c.name, tail.ReplayThroughWAL, tail.PushedToArchive, c.wantThrough, c.wantPushed, tail.Detail)
		}
	}

	if planWALTail("", nil, wal) != nil {
		t.Error("no saved dir (no pg_wal at all): want nil")
	}
	if tail := planWALTail("/saved", nil, wal); tail == nil || tail.ReplayThroughWAL != "" || !strings.Contains(tail.Detail, "no WAL segments") {
		t.Errorf("empty pg_wal: %+v", tail)
	}
}

func TestParseWALTailOutput(t *testing.T) {
	h := syntheticSegmentHeader(binary.LittleEndian, "00000007000000000000000B", mib16)[:walLongHeaderLen]
	out := walTailSavedMarker + "/var/lib/postgresql/pg_wal.saved-20261019T153300Z\n" +
		"P00   INFO: restore command begin\n" +
		"P00   INFO: restore command end: completed successfully\n" +
		walTailSegmentMarker + "00000007000000000000000B " + hex.EncodeToString(h) + "\n"
	saved, segs, rest := parseWALTailOutput(out)
	if saved != "/var/lib/postgresql/pg_wal.saved-20261019T153300Z" {
		t.Errorf("saved = %q", saved)
	}
	if len(segs) != 1 || segs[0].Name != "00000007000000000000000B" || string(segs[0].Header) != string(h) {
		t.Errorf("segs = %+v", segs)
	}
	if strings.Contains(rest, "helpdesk-wal-tail") || !strings.Contains(rest, "completed successfully") {
		t.Errorf("rest = %q, want pgBackRest's output without the marker lines", rest)
	}
}

// The order is the fix: the tail leaves pg_wal before --delta restore can
// delete it, to a directory outside PGDATA, and comes back before startup.
func TestRestoreScriptWithWALTail_Order(t *testing.T) {
	s := restoreScriptWithWALTail("main")
	idx := func(sub string) int {
		i := strings.Index(s, sub)
		if i < 0 {
			t.Fatalf("script has no %q:\n%s", sub, s)
		}
		return i
	}
	if !strings.HasPrefix(s, "set -e") {
		t.Error("script must stop before the restore if the tail can't be saved")
	}
	if !(idx("rm -f \"$PGDATA/postmaster.pid\"") < idx("mkdir \"$SAVED\"") && idx("mkdir \"$SAVED\"") < idx("mv \"$f\" \"$SAVED/\"") &&
		idx("mv \"$f\" \"$SAVED/\"") < idx("--stanza='main' --delta restore") && idx("--delta restore") < idx("cp -p \"$f\" \"$WAL/$n\"")) {
		t.Errorf("wrong order: save, restore, copy back:\n%s", s)
	}
	if !strings.Contains(s, `$(dirname "$(cd "$PGDATA" && pwd -P)")/pg_wal.saved-`) {
		t.Error("saved dir must be beside PGDATA, not inside it (--delta restore cleans PGDATA)")
	}
	if q := pushToArchiveScript("main", []string{"00000007000000000000000B", "00000007000000000000000C"}); !strings.Contains(q,
		`cd "$PGDATA"`) || !strings.HasSuffix(q, `for n in 00000007000000000000000B 00000007000000000000000C; do pgbackrest --stanza='main' archive-push "pg_wal/$n"; done`) {
		t.Errorf("push script = %q", q)
	}
}

func TestCompareRecovery_PgWalTail(t *testing.T) {
	pred := &RestorePrediction{RecoverToWAL: "00000007000000000000000A", ReplayThroughWAL: "00000007000000000000000C",
		ExpectedTimeline: 8, SegmentSizeBytes: mib16}
	// The canary run's real recovery: archive through …0A, redo done in …0C.
	canary := recoveryLog{Found: true, LastRestoredWAL: "00000007000000000000000A", NewTimeline: 8, RedoDoneLSN: "0/C0000E0"}
	cases := []struct {
		name, wantStatus, wantDetail string
		pred                         *RestorePrediction
		rec                          recoveryLog
	}{
		{"canary run: replayed the tail", RestoreVerified, "then the WAL put back into pg_wal through 00000007000000000000000C", pred, canary},
		{"stopped at the archive end anyway", RestoreMismatch, "stopped early in the pg_wal tail",
			pred, recoveryLog{Found: true, LastRestoredWAL: "00000007000000000000000A", NewTimeline: 8, RedoDoneLSN: "0/A000138"}},
		{"no tail, but redo went further", RestoreMismatch, "went past the predicted end: redo done at 0/C0000E0",
			&RestorePrediction{RecoverToWAL: "00000007000000000000000A", ExpectedTimeline: 8, SegmentSizeBytes: mib16}, canary},
		// The real VM restore: archive end on timeline 27 (…1B), redo in the
		// same segment number: verified by number, not by name.
		{"real VM, with the segment size recorded", RestoreVerified, "redo done at 0/C000138",
			&RestorePrediction{RecoverToWAL: "0000001B000000000000000C", ExpectedTimeline: 30, SegmentSizeBytes: mib16}, parseRecoveryLog(vmRecoveryLog)},
	}
	for _, c := range cases {
		status, detail := compareRecovery(c.pred, c.rec)
		if status != c.wantStatus || !strings.Contains(detail, c.wantDetail) {
			t.Errorf("%s: compareRecovery = (%s, %q), want %s containing %q", c.name, status, detail, c.wantStatus, c.wantDetail)
		}
	}
}

// restoreFromBackupImpl end to end against fake commands: the restore
// script's markers become PgWalTail, the segments to push are archive-pushed
// with a second call, and the prediction carries the tail's end.
func TestRestoreFromBackup_PgWalTail(t *testing.T) {
	probe := &pgbackrestRouter{info: realPgBackRestInfoJSON}
	old := cmdRunner
	cmdRunner = probe
	defer func() { cmdRunner = old }()
	status, err := getPgBackRestStatusImpl(context.Background(), GetPgBackRestStatusArgs{Stanza: "main"})
	if err != nil || status.WALContinuity == nil || status.WALContinuity.Status != WALContinuous {
		t.Fatalf("fixture WAL check: %+v, %v", status.WALContinuity, err)
	}
	wal := status.WALContinuity
	_, logID, seg, _ := parseSegmentName(wal.ArchiveEndWAL)
	endNo := logID*segsPerLogID(wal.SegmentSizeBytes) + seg
	next := func(n uint64) string { return segmentName(wal.LatestTimeline, endNo+n, wal.SegmentSizeBytes) }
	marker := func(name string) string {
		return walTailSegmentMarker + name + " " +
			hex.EncodeToString(syntheticSegmentHeader(binary.LittleEndian, name, wal.SegmentSizeBytes)[:walLongHeaderLen]) + "\n"
	}

	for _, pushErr := range []error{nil, fmt.Errorf("exit status 1")} {
		r := &pgbackrestRouter{
			info:       realPgBackRestInfoJSON,
			readyErr:   fmt.Errorf("exit status 2"),
			restoreOut: walTailSavedMarker + "/var/lib/pgsql/pg_wal.saved-x\nrestore command end: completed successfully\n" + marker(next(1)) + marker(next(2)),
			pushErr:    pushErr,
		}
		cmdRunner = r
		res, err := restoreFromBackupImpl(context.Background(), RestoreFromBackupArgs{Stanza: "main"})
		if err != nil {
			t.Fatalf("pushErr=%v: %v", pushErr, err)
		}
		tail := res.PgWalTail
		if tail == nil || tail.SavedTo != "/var/lib/pgsql/pg_wal.saved-x" || tail.ReplayThroughWAL != next(2) {
			t.Fatalf("pushErr=%v: PgWalTail = %+v, want saved dir and replay through %s", pushErr, tail, next(2))
		}
		// Once pushed, recovery restores next(1) from the archive; if the push
		// failed, the archive still ends where the WAL check said.
		wantArchiveEnd := next(1)
		if pushErr != nil {
			wantArchiveEnd = wal.ArchiveEndWAL
		}
		if res.Prediction == nil || res.Prediction.ReplayThroughWAL != next(2) || res.Prediction.RecoverToWAL != wantArchiveEnd {
			t.Errorf("pushErr=%v: Prediction = %+v, want recover_to_wal %s, replay_through_wal %s", pushErr, res.Prediction, wantArchiveEnd, next(2))
		}
		if strings.Contains(res.Output, "helpdesk-wal-tail") {
			t.Errorf("Output still has marker lines: %q", res.Output)
		}
		calls := r.callsContaining("archive-push")
		if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "for n in "+next(1)+";") || strings.Contains(strings.Join(calls[0], " "), next(2)) {
			t.Errorf("pushErr=%v: push calls = %v, want one pushing only %s", pushErr, calls, next(1))
		}
		if pushErr == nil && !slices.Equal(tail.PushedToArchive, []string{next(1)}) {
			t.Errorf("PushedToArchive = %v", tail.PushedToArchive)
		}
		// A failed push doesn't fail the restore, but it must say so.
		if pushErr != nil && (tail.PushedToArchive != nil || !strings.Contains(tail.Detail, "FAILED") || !strings.Contains(tail.Detail, "archive-push")) {
			t.Errorf("failed push: %+v", tail)
		}
	}
}
