package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"regexp"
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
// …0C (open, row B). Recovery replayed through …0C. Both are pushed: leaving
// the newest to PostgreSQL's .partial loses it when the end of WAL is on a
// segment boundary (the VM, 2026-10-19: a clean shutdown had switched WAL).
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
	if !slices.Equal(tail.PushedToArchive, []string{"00000007000000000000000B", "00000007000000000000000C"}) {
		t.Errorf("PushedToArchive = %v, want …0B and …0C, the newest included (never the recycled …0D)", tail.PushedToArchive)
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
				tailSeg("00000007000000000000000D")}, "00000007000000000000000B", []string{"00000007000000000000000B"}},
		{"a gap right after the archive end: recovery can't reach the rest", wal,
			[]tailSegment{tailSeg("00000007000000000000000C"), tailSeg("00000007000000000000000D")}, "", nil},
		{"older timeline segments are not the server's", wal,
			[]tailSegment{tailSeg("00000006000000000000000B"), tailSeg("00000006000000000000000C")}, "", nil},
		{"three past the end: all pushed, the newest too", wal,
			[]tailSegment{tailSeg("00000007000000000000000B"), tailSeg("00000007000000000000000C"), tailSeg("00000007000000000000000D")},
			"00000007000000000000000D", []string{"00000007000000000000000B", "00000007000000000000000C", "00000007000000000000000D"}},
		// The VM, 2026-10-18: archive end on timeline 27, the server on 29. The
		// server's own copy of segment …0C holds WAL the archived one doesn't.
		{"server's copy of the archive end's segment on a newer timeline",
			&WALContinuity{Status: WALContinuous, ArchiveEndWAL: "0000001B000000000000000C", LatestTimeline: 29, SegmentSizeBytes: mib16},
			[]tailSegment{tailSeg("0000001D000000000000000C")}, "0000001D000000000000000C", []string{"0000001D000000000000000C"}},
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
	if !strings.Contains(s, `PARENT=$(dirname "$(cd "$PGDATA" && pwd -P)")`) || !strings.Contains(s, `SAVED="$PARENT/pg_wal.saved-`) {
		t.Error("saved dir must be beside PGDATA, not inside it (--delta restore cleans PGDATA)")
	}
	if !strings.Contains(s, `if [ -n "${HELPDESK_WAL_SAVE_IN_REPO:-}" ]; then
    PARENT=$(grep '^repo1-path=' /etc/pgbackrest.conf | cut -d= -f2)`) {
		t.Error("with HELPDESK_WAL_SAVE_IN_REPO the saved dir must go on the repo volume (Kubernetes)")
	}
	if q := pushToArchiveScript("main", []string{"00000007000000000000000B", "00000007000000000000000C"}); !strings.Contains(q,
		`cd "$PGDATA"`) || !strings.HasSuffix(q, `for n in 00000007000000000000000B 00000007000000000000000C; do pgbackrest --stanza='main' archive-push "pg_wal/$n"; echo "`+walTailPushedMarker+`$n"; done`) {
		t.Errorf("push script = %q", q)
	}
}

func TestParsePushOutput(t *testing.T) {
	out := "P00   INFO: archive-push command begin\n" + walTailPushedMarker + "00000007000000000000000B\n" +
		"P00  ERROR: [042]: unable to write\n"
	pushed, rest := parsePushOutput(out)
	if !slices.Equal(pushed, []string{"00000007000000000000000B"}) || strings.Contains(rest, walTailPushedMarker) || !strings.Contains(rest, "unable to write") {
		t.Errorf("parsePushOutput = %v, %q", pushed, rest)
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
		// Every tail segment pushed: recovery restores all of it from the archive.
		{"whole tail pushed", RestoreVerified, "including the WAL from pg_wal that restore_from_backup pushed",
			&RestorePrediction{RecoverToWAL: "00000007000000000000000C", ReplayThroughWAL: "00000007000000000000000C", ExpectedTimeline: 8, SegmentSizeBytes: mib16},
			recoveryLog{Found: true, LastRestoredWAL: "00000007000000000000000C", NewTimeline: 8, RedoDoneLSN: "0/C0000E0"}},
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

var failedPushRe = regexp.MustCompile(`Pushing \[([^\]]*)\] to the archive FAILED`)

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

	pushed := func(n string) string { return walTailPushedMarker + n + "\n" }
	cases := []struct {
		name       string
		pushOut    string
		pushErr    error
		wantPushed []string
		wantFailed string // segment the detail must name as not pushed
	}{
		{"push succeeds", pushed(next(1)) + pushed(next(2)), nil, []string{next(1), next(2)}, ""},
		{"push fails outright", "ERROR: [042]\n", fmt.Errorf("exit status 1"), nil, next(1)},
		{"push fails partway", pushed(next(1)) + "ERROR: [042]\n", fmt.Errorf("exit status 1"), []string{next(1)}, next(2)},
	}
	for _, c := range cases {
		r := &pgbackrestRouter{
			info:       realPgBackRestInfoJSON,
			readyErr:   fmt.Errorf("exit status 2"),
			restoreOut: walTailSavedMarker + "/var/lib/pgsql/pg_wal.saved-x\nrestore command end: completed successfully\n" + marker(next(1)) + marker(next(2)),
			pushOut:    c.pushOut,
			pushErr:    c.pushErr,
		}
		cmdRunner = r
		res, err := restoreFromBackupImpl(context.Background(), RestoreFromBackupArgs{Stanza: "main"})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		tail := res.PgWalTail
		if tail == nil || tail.SavedTo != "/var/lib/pgsql/pg_wal.saved-x" || tail.ReplayThroughWAL != next(2) {
			t.Fatalf("%s: PgWalTail = %+v, want saved dir and replay through %s", c.name, tail, next(2))
		}
		// Recovery restores what was pushed from the archive (it tries the
		// archive first), so the archive end moves to the last pushed segment;
		// with nothing pushed it stays where the WAL check said.
		wantArchiveEnd := wal.ArchiveEndWAL
		if n := len(c.wantPushed); n > 0 {
			wantArchiveEnd = c.wantPushed[n-1]
		}
		if res.Prediction == nil || res.Prediction.ReplayThroughWAL != next(2) || res.Prediction.RecoverToWAL != wantArchiveEnd {
			t.Errorf("%s: Prediction = %+v, want recover_to_wal %s, replay_through_wal %s", c.name, res.Prediction, wantArchiveEnd, next(2))
		}
		if strings.Contains(res.Output, "helpdesk-wal-tail") {
			t.Errorf("%s: Output still has marker lines: %q", c.name, res.Output)
		}
		// Every tail segment is pushed, the newest included.
		calls := r.callsContaining("archive-push")
		if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "for n in "+next(1)+" "+next(2)+";") {
			t.Errorf("%s: push calls = %v, want one pushing %s and %s", c.name, calls, next(1), next(2))
		}
		if !slices.Equal(tail.PushedToArchive, c.wantPushed) {
			t.Errorf("%s: PushedToArchive = %v, want %v", c.name, tail.PushedToArchive, c.wantPushed)
		}
		// A failed push doesn't fail the restore, but it must say so, and
		// which segments still need pushing.
		if c.pushErr != nil {
			m := failedPushRe.FindStringSubmatch(tail.Detail)
			if m == nil || !strings.Contains(tail.Detail, "archive-push") || !strings.Contains(m[1], c.wantFailed) ||
				(len(c.wantPushed) > 0 && strings.Contains(m[1], c.wantPushed[0])) {
				t.Errorf("%s: detail = %q, want FAILED naming only the unpushed %s", c.name, tail.Detail, c.wantFailed)
			}
		}
	}
}
