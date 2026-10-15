package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The multi_timeline fixtures are real pgBackRest 2.59.2 output captured
// from the docker test fixture (helpdesk-test-pg-pgbackrest) on 2026-10-15,
// after many restore test runs: one full backup on timeline 1 (WAL
// 000000010000000000000003), and an archive that has since moved through 29
// timeline switches to timeline 30 (0x1E). Most intermediate timelines own
// no whole segment (each forked at 0/4000000 or inside segment 4), a few left
// only .partial segments, and timeline 30 holds segments 4 and 5. A correct
// check must walk all of that and find the chain continuous.

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/walcheck/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func multiTimelineInput(t *testing.T) walCheckInput {
	t.Helper()
	res, err := parsePgBackRestInfo(readFixture(t, "multi_timeline_info.json"), "", 25*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	in, ok := res.walCheckInput()
	if !ok {
		t.Fatal("expected a backup in the fixture")
	}
	return in
}

// withoutListingEntries returns the listing JSON minus every entry whose path
// contains one of the given substrings.
func withoutListingEntries(t *testing.T, listing string, drop ...string) string {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(listing), &raw); err != nil {
		t.Fatal(err)
	}
	for k := range raw {
		for _, d := range drop {
			if strings.Contains(k, d) {
				delete(raw, k)
			}
		}
	}
	b, _ := json.Marshal(raw)
	return string(b)
}

func TestEvaluateWAL_RealMultiTimelineArchive_Continuous(t *testing.T) {
	in := multiTimelineInput(t)
	if in.WALStart != "000000010000000000000003" || in.ArchiveID != "16-1" || in.LSNStart != "0/3000028" {
		t.Fatalf("parsed input = %+v, want the fixture's backup WAL range and archive id", in)
	}
	got := evaluateWALContinuity(in, readFixture(t, "multi_timeline_top.txt"),
		readFixture(t, "multi_timeline_0000001E.history"), readFixture(t, "multi_timeline_listing.json"))

	if got.Status != WALContinuous {
		t.Fatalf("Status = %s (%s), want continuous", got.Status, got.Detail)
	}
	if !got.ConsistencyReachable {
		t.Error("ConsistencyReachable = false, want true")
	}
	if got.LatestTimeline != 30 || len(got.TimelinePath) != 30 || got.TimelinePath[0] != 1 {
		t.Errorf("LatestTimeline = %d, path = %v, want 30 timelines from 1 to 30", got.LatestTimeline, got.TimelinePath)
	}
	if got.SegmentSizeBytes != 16<<20 {
		t.Errorf("SegmentSizeBytes = %d, want 16 MiB", got.SegmentSizeBytes)
	}
	if got.RecoverableToWAL != "0000001E0000000000000005" || got.ArchiveEndWAL != "0000001E0000000000000005" {
		t.Errorf("RecoverableToWAL = %s, ArchiveEndWAL = %s, want both 0000001E0000000000000005", got.RecoverableToWAL, got.ArchiveEndWAL)
	}
	if got.ArchiveEndTime == "" {
		t.Error("ArchiveEndTime is empty, want the repo time of the last segment")
	}
	if len(got.Gaps) != 0 {
		t.Errorf("Gaps = %+v, want none", got.Gaps)
	}
}

// The backup's own WAL is missing: PostgreSQL could not even reach
// consistency, so a restore would destroy PGDATA for nothing.
func TestEvaluateWAL_BackupOwnSegmentMissing_ConsistencyUnreachable(t *testing.T) {
	in := multiTimelineInput(t)
	listing := withoutListingEntries(t, readFixture(t, "multi_timeline_listing.json"), "/000000010000000000000003-")
	got := evaluateWALContinuity(in, readFixture(t, "multi_timeline_top.txt"),
		readFixture(t, "multi_timeline_0000001E.history"), listing)

	if got.Status != WALBroken || got.ConsistencyReachable {
		t.Fatalf("Status = %s, ConsistencyReachable = %v, want broken and unreachable", got.Status, got.ConsistencyReachable)
	}
	if len(got.Gaps) == 0 || got.Gaps[0].FirstMissing != "000000010000000000000003" {
		t.Errorf("Gaps = %+v, want the first gap at 000000010000000000000003", got.Gaps)
	}
	if !strings.Contains(got.Detail, "could not start") {
		t.Errorf("Detail = %q, want it to say PostgreSQL could not start", got.Detail)
	}
}

// A hole after the backup: the restore would succeed but silently stop at
// the hole instead of the end of the archive.
func TestEvaluateWAL_GapAfterConsistency_RecoveryStopsShort(t *testing.T) {
	in := multiTimelineInput(t)
	listing := withoutListingEntries(t, readFixture(t, "multi_timeline_listing.json"), "/0000001E0000000000000004-")
	got := evaluateWALContinuity(in, readFixture(t, "multi_timeline_top.txt"),
		readFixture(t, "multi_timeline_0000001E.history"), listing)

	if got.Status != WALBroken {
		t.Fatalf("Status = %s, want broken", got.Status)
	}
	if !got.ConsistencyReachable {
		t.Error("ConsistencyReachable = false, want true: the backup's own WAL is intact")
	}
	if got.RecoverableToWAL != "000000010000000000000003" {
		t.Errorf("RecoverableToWAL = %s, want 000000010000000000000003 (the last segment before the hole)", got.RecoverableToWAL)
	}
	if got.ArchiveEndWAL != "0000001E0000000000000005" {
		t.Errorf("ArchiveEndWAL = %s, want 0000001E0000000000000005", got.ArchiveEndWAL)
	}
	if len(got.Gaps) != 1 || got.Gaps[0].Timeline != 30 || got.Gaps[0].FirstMissing != "0000001E0000000000000004" || got.Gaps[0].Count != 1 {
		t.Errorf("Gaps = %+v, want one missing segment 0000001E0000000000000004", got.Gaps)
	}
	if !strings.Contains(got.Detail, "recovery would stop at 000000010000000000000003") {
		t.Errorf("Detail = %q, want it to name where recovery stops", got.Detail)
	}
}

// The newest segment simply not being there is the end of the archive, not a
// gap: nothing after it was archived.
func TestEvaluateWAL_MissingTailIsTheEndNotAGap(t *testing.T) {
	in := multiTimelineInput(t)
	listing := withoutListingEntries(t, readFixture(t, "multi_timeline_listing.json"), "/0000001E0000000000000005-")
	got := evaluateWALContinuity(in, readFixture(t, "multi_timeline_top.txt"),
		readFixture(t, "multi_timeline_0000001E.history"), listing)

	if got.Status != WALContinuous {
		t.Fatalf("Status = %s (%s), want continuous", got.Status, got.Detail)
	}
	if got.ArchiveEndWAL != "0000001E0000000000000004" {
		t.Errorf("ArchiveEndWAL = %s, want 0000001E0000000000000004", got.ArchiveEndWAL)
	}
}

// restore_command never fetches .partial segments, so a timeline that only
// has a .partial for a segment it owns is a gap.
func TestEvaluateWAL_PartialSegmentDoesNotCount(t *testing.T) {
	in := walCheckInput{BackupLabel: "b", ArchiveID: "16-1", WALStart: "000000010000000000000003", WALStop: "000000010000000000000003", LSNStart: "0/3000028", LSNStop: "0/3000100"}
	listing := `{"0000000100000000/000000010000000000000003-` + strings.Repeat("a", 40) + `.gz":{"type":"file","time":1},` +
		`"0000000100000000/000000010000000000000004.partial-` + strings.Repeat("b", 40) + `.gz":{"type":"file","time":2},` +
		`"0000000100000000/000000010000000000000005-` + strings.Repeat("c", 40) + `.gz":{"type":"file","time":3}}`
	got := evaluateWALContinuity(in, "0000000100000000\n", "", listing)
	if got.Status != WALBroken || len(got.Gaps) != 1 || got.Gaps[0].FirstMissing != "000000010000000000000004" {
		t.Fatalf("Status = %s, Gaps = %+v, want broken with 000000010000000000000004 missing", got.Status, got.Gaps)
	}
}

// With 16 MiB segments, segment ...000000FF is followed by the next logical
// id's ...00000000: that is contiguous, not a gap.
func TestEvaluateWAL_LogIDBoundaryIsContiguous(t *testing.T) {
	in := walCheckInput{BackupLabel: "b", ArchiveID: "16-1", WALStart: "0000000100000000000000FE", WALStop: "0000000100000000000000FE", LSNStart: "0/FE000028", LSNStop: "0/FE000100"}
	var entries []string
	for _, name := range []string{"0000000100000000000000FE", "0000000100000000000000FF", "000000010000000100000000", "000000010000000100000001"} {
		entries = append(entries, fmt.Sprintf(`"%s/%s-%s.gz":{"type":"file","time":1}`, name[:16], name, strings.Repeat("d", 40)))
	}
	got := evaluateWALContinuity(in, "0000000100000000\n0000000100000001\n", "", "{"+strings.Join(entries, ",")+"}")
	if got.Status != WALContinuous || got.ArchiveEndWAL != "000000010000000100000001" {
		t.Fatalf("Status = %s (%s), ArchiveEndWAL = %s, want continuous to 000000010000000100000001", got.Status, got.Detail, got.ArchiveEndWAL)
	}
}

// A backup taken on timeline 1 after timeline 1 forked to 2 is on a branch
// the newest timeline doesn't descend from: PostgreSQL refuses to recover.
func TestEvaluateWAL_BackupAfterFork_Broken(t *testing.T) {
	in := walCheckInput{BackupLabel: "b", ArchiveID: "16-1", WALStart: "000000010000000000000006", WALStop: "000000010000000000000006", LSNStart: "0/6000028", LSNStop: "0/6000100"}
	got := evaluateWALContinuity(in, "0000000100000000\n00000002.history\n", "1\t0/4000000\tno recovery target specified\n", "{}")
	if got.Status != WALBroken || !strings.Contains(got.Detail, "does not descend from") {
		t.Fatalf("Status = %s, Detail = %q, want broken because the backup is past the fork", got.Status, got.Detail)
	}
}

func TestEvaluateWAL_BackupTimelineNotInHistory_Broken(t *testing.T) {
	in := walCheckInput{BackupLabel: "b", ArchiveID: "16-1", WALStart: "000000050000000000000003", WALStop: "000000050000000000000003", LSNStart: "0/3000028", LSNStop: "0/3000100"}
	got := evaluateWALContinuity(in, "00000007.history\n", "1\t0/2000000\tx\n6\t0/2800000\tx\n", "{}")
	if got.Status != WALBroken || !strings.Contains(got.Detail, "not in the history") {
		t.Fatalf("Status = %s, Detail = %q, want broken: timeline 5 is not an ancestor of 7", got.Status, got.Detail)
	}
}

func TestEvaluateWAL_UnparseableListing_Unverified(t *testing.T) {
	in := multiTimelineInput(t)
	got := evaluateWALContinuity(in, readFixture(t, "multi_timeline_top.txt"), readFixture(t, "multi_timeline_0000001E.history"), "not json")
	if got.Status != WALUnverified {
		t.Fatalf("Status = %s, want unverified", got.Status)
	}
}

func TestInferSegmentSize(t *testing.T) {
	cases := []struct {
		lsn, name string
		want      uint64
	}{
		{"0/3000028", "000000010000000000000003", 16 << 20},
		{"0/C000028", "000000010000000000000003", 64 << 20}, // 3 × 64 MiB
		{"0/C0000028", "000000010000000000000003", 1 << 30}, // 3 × 1 GiB
		{"0/15000028", "000000010000000000000015", 16 << 20},
		{"1/2000028", "000000010000000100000002", 16 << 20},
	}
	for _, c := range cases {
		got, err := inferSegmentSize([]lsnSegment{{c.lsn, c.name}})
		if err != nil || got != c.want {
			t.Errorf("inferSegmentSize(%s, %s) = %d, %v; want %d", c.lsn, c.name, got, err, c.want)
		}
	}
	if _, err := inferSegmentSize([]lsnSegment{{"0/3000028", "000000010000000000000099"}}); err == nil {
		t.Error("expected an error when no segment size maps the LSN to the name")
	}
}

// A backup starting in the first 1 MiB of a logical WAL id is segment 00
// under every size. Another backup's pair resolves it; without one, the
// 16 MiB default is used.
func TestInferSegmentSize_AmbiguousFirstMiB(t *testing.T) {
	ambiguous := lsnSegment{"1/00000028", "000000010000000100000000"}
	if got, err := inferSegmentSize([]lsnSegment{ambiguous}); err != nil || got != 16<<20 {
		t.Errorf("alone: got %d, %v; want the 16 MiB default", got, err)
	}
	other := lsnSegment{"0/C000028", "000000010000000000000003"} // pins 64 MiB
	if got, err := inferSegmentSize([]lsnSegment{ambiguous, other}); err != nil || got != 64<<20 {
		t.Errorf("with a pinning pair: got %d, %v; want 64 MiB", got, err)
	}
}

// checkWALContinuity issues exactly the three repo reads, reads the newest
// timeline's history file, and filters the listing to the path's timelines.
func TestCheckWALContinuity_CommandsAndResult(t *testing.T) {
	res, err := parsePgBackRestInfo(readFixture(t, "multi_timeline_info.json"), "", 25*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	run := func(args []string) (string, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		switch {
		case strings.Contains(joined, "repo-get"):
			return readFixture(t, "multi_timeline_0000001E.history"), nil
		case strings.Contains(joined, "--recurse"):
			return readFixture(t, "multi_timeline_listing.json"), nil
		case strings.Contains(joined, "repo-ls"):
			return readFixture(t, "multi_timeline_top.txt"), nil
		}
		return "", fmt.Errorf("unexpected command %q", joined)
	}
	got := checkWALContinuity(res, run)
	if got.Status != WALContinuous {
		t.Fatalf("Status = %s (%s), want continuous", got.Status, got.Detail)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %v, want 3 repo reads", calls)
	}
	if calls[0] != "--stanza=main repo-ls --output=text archive/main/16-1" {
		t.Errorf("call[0] = %q", calls[0])
	}
	if calls[1] != "--stanza=main repo-get archive/main/16-1/0000001E.history" {
		t.Errorf("call[1] = %q, want the newest timeline's history file", calls[1])
	}
	if !strings.Contains(calls[2], "--filter=^(00000001|00000002|") || !strings.Contains(calls[2], "|0000001E)[0-9A-F]{8}/") {
		t.Errorf("call[2] = %q, want a listing filtered to timelines 1..30", calls[2])
	}
}

func TestCheckWALContinuity_RepoLsFails_Unverified(t *testing.T) {
	res, err := parsePgBackRestInfo(readFixture(t, "multi_timeline_info.json"), "", 25*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got := checkWALContinuity(res, func([]string) (string, error) {
		return "", fmt.Errorf("invalid command 'repo-ls'")
	})
	if got.Status != WALUnverified || !strings.Contains(got.Detail, "repo-ls") {
		t.Fatalf("Status = %s, Detail = %q, want unverified naming repo-ls", got.Status, got.Detail)
	}
}

func TestCheckWALContinuity_NoBackup(t *testing.T) {
	got := checkWALContinuity(GetPgBackRestStatusResult{Stanza: "main"}, func([]string) (string, error) {
		t.Fatal("no repo reads expected when there is no backup")
		return "", nil
	})
	if got.Status != WALNoBackup {
		t.Fatalf("Status = %s, want no_backup", got.Status)
	}
}
