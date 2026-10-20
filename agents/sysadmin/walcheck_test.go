package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
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

// realHeaderSegment is the smallest segment in multi_timeline_listing.json,
// the one checkWALContinuity fetches for its header.
const realHeaderSegment = "0000000100000000/000000010000000000000002-c02bf304dc2675e6864e6c858b0a7a326ee14802.gz"

// checkWALContinuity issues exactly the four repo reads, reads the newest
// timeline's history file, filters the listing to the path's timelines, and
// takes the segment size from the smallest segment's header.
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
		case strings.Contains(joined, ".history"):
			return readFixture(t, "multi_timeline_0000001E.history"), nil
		case strings.Contains(joined, "repo-get"):
			return readFixture(t, "multi_timeline_000000010000000000000002.gz"), nil
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
	if len(calls) != 4 {
		t.Fatalf("calls = %v, want 4 repo reads", calls)
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
	if calls[3] != "--stanza=main repo-get archive/main/16-1/"+realHeaderSegment {
		t.Errorf("call[3] = %q, want the smallest segment in the listing", calls[3])
	}
	if got.SegmentSizeBytes != 16<<20 || got.SegmentSizeSource != SegmentSizeFromWALHeader {
		t.Errorf("segment size = %d from %q, want 16 MiB from the WAL header", got.SegmentSizeBytes, got.SegmentSizeSource)
	}
}

// fakeWALRun answers checkWALContinuity's reads from the multi_timeline
// fixtures, with segment as the repo-get output for the header segment.
func fakeWALRun(t *testing.T, segment string, segmentErr error) pgbackrestRunner {
	return func(args []string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, ".history"):
			return readFixture(t, "multi_timeline_0000001E.history"), nil
		case strings.Contains(joined, "repo-get"):
			return segment, segmentErr
		case strings.Contains(joined, "--recurse"):
			return readFixture(t, "multi_timeline_listing.json"), nil
		case strings.Contains(joined, "repo-ls"):
			return readFixture(t, "multi_timeline_top.txt"), nil
		}
		return "", fmt.Errorf("unexpected command %q", joined)
	}
}

// A segment that can't be read leaves the decision to the backups' LSNs,
// which pin 16 MiB on their own here.
func TestCheckWALContinuity_HeaderUnreadable_FallsBackToLSNs(t *testing.T) {
	res, err := parsePgBackRestInfo(readFixture(t, "multi_timeline_info.json"), "", 25*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got := checkWALContinuity(res, fakeWALRun(t, "", fmt.Errorf("permission denied")))
	if got.Status != WALContinuous || got.SegmentSizeBytes != 16<<20 || got.SegmentSizeSource != SegmentSizeFromBackupLSNs {
		t.Fatalf("Status = %s (%s), size = %d from %q, want continuous, 16 MiB from backup_lsns",
			got.Status, got.Detail, got.SegmentSizeBytes, got.SegmentSizeSource)
	}
}

// A header that contradicts the backups' LSNs means something about the repo
// isn't what it seems: neither source is trusted, and the restore refuses.
func TestCheckWALContinuity_HeaderContradictsLSNs_Unverified(t *testing.T) {
	res, err := parsePgBackRestInfo(readFixture(t, "multi_timeline_info.json"), "", 25*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	seg := gzipBytes(t, syntheticSegmentHeader(binary.LittleEndian, "000000010000000000000002", 64<<20))
	got := checkWALContinuity(res, fakeWALRun(t, string(seg), nil))
	if got.Status != WALUnverified || !strings.Contains(got.Detail, "64 MiB") {
		t.Fatalf("Status = %s, Detail = %q, want unverified naming the header's 64 MiB", got.Status, got.Detail)
	}
}

// syntheticSegmentHeader builds the first 8 KiB page of a WAL segment as
// PostgreSQL 16 writes it, in the given byte order.
func syntheticSegmentHeader(order binary.ByteOrder, name string, segSize uint64) []byte {
	_, logID, seg, err := parseSegmentName(name)
	if err != nil {
		panic(err)
	}
	b := make([]byte, 8192)
	order.PutUint16(b[0:2], 0xD113) // xlp_magic, PostgreSQL 16
	order.PutUint16(b[2:4], 0x0006) // XLP_LONG_HEADER | XLP_BKP_REMOVABLE
	order.PutUint32(b[4:8], 1)      // xlp_tli
	per := segsPerLogID(segSize)
	order.PutUint64(b[8:16], (logID*per+seg)*segSize) // xlp_pageaddr
	order.PutUint64(b[24:32], 7693779635926569000)    // xlp_sysid
	order.PutUint32(b[32:36], uint32(segSize))        // xlp_seg_size
	order.PutUint32(b[36:40], 8192)                   // xlp_xlog_blcksz
	return b
}

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The real segment from the test container: a 16 MiB cluster, gzip as
// pgBackRest archives by default, and the same segment recompressed with
// bzip2.
func TestSegmentSizeFromHeader_RealSegment(t *testing.T) {
	const name = "000000010000000000000002"
	for _, c := range []struct{ fixture, compression string }{
		{"multi_timeline_000000010000000000000002.gz", "gz"},
		{"multi_timeline_000000010000000000000002.bz2", "bz2"},
	} {
		data := []byte(readFixture(t, c.fixture))
		file := headerSegment{path: "0000000100000000/" + name + "." + c.compression, name: name, compression: c.compression}
		got, err := segmentSizeFromHeader(data, file)
		if err != nil || got != 16<<20 {
			t.Errorf("%s: got %d, %v; want 16 MiB", c.fixture, got, err)
		}
		// pgBackRest under Kubernetes prints WARN lines on stdout first.
		got, err = segmentSizeFromHeader(append([]byte("WARN: environment contains invalid option 'service-port'\n"), data...), file)
		if err != nil || got != 16<<20 {
			t.Errorf("%s with a WARN preamble: got %d, %v; want 16 MiB", c.fixture, got, err)
		}
	}
}

func TestSegmentSizeFromHeader_Uncompressed(t *testing.T) {
	const name = "0000000100000003000000A1"
	file := headerSegment{path: "0000000100000003/" + name, name: name}
	raw := syntheticSegmentHeader(binary.LittleEndian, name, 16<<20)
	for _, data := range [][]byte{raw, append([]byte("WARN: noise\nWARN: more noise\n"), raw...)} {
		if got, err := segmentSizeFromHeader(data, file); err != nil || got != 16<<20 {
			t.Errorf("got %d, %v; want 16 MiB", got, err)
		}
	}
}

func TestParseWALLongHeader(t *testing.T) {
	cases := []struct {
		order binary.ByteOrder
		name  string
		size  uint64
	}{
		{binary.LittleEndian, "000000010000000000000003", 16 << 20},
		{binary.LittleEndian, "00000001000000050000003F", 64 << 20}, // last segment of a logical id at 64 MiB
		{binary.LittleEndian, "000000020000000000000000", 1 << 30},
		{binary.BigEndian, "000000010000000000000003", 16 << 20}, // e.g. s390x
		{binary.LittleEndian, "0000000100000000000003FF", 1 << 20},
	}
	for _, c := range cases {
		got, err := parseWALLongHeader(syntheticSegmentHeader(c.order, c.name, c.size), c.name)
		if err != nil || got != c.size {
			t.Errorf("%s at %d MiB: got %d, %v", c.name, c.size>>20, got, err)
		}
	}

	// A header copied from another segment: its page address doesn't match.
	if _, err := parseWALLongHeader(syntheticSegmentHeader(binary.LittleEndian, "000000010000000000000004", 16<<20), "000000010000000000000003"); err == nil {
		t.Error("expected an error for a header whose page address belongs to another segment")
	}
	// Not the first page of a segment (no long header).
	short := syntheticSegmentHeader(binary.LittleEndian, "000000010000000000000003", 16<<20)
	binary.LittleEndian.PutUint16(short[2:4], 0x0004)
	if _, err := parseWALLongHeader(short, "000000010000000000000003"); err == nil {
		t.Error("expected an error without XLP_LONG_HEADER")
	}
	// Not WAL at all.
	if _, err := parseWALLongHeader(make([]byte, walLongHeaderLen), "000000010000000000000003"); err == nil {
		t.Error("expected an error for zeros")
	}
}

func TestPickHeaderSegment(t *testing.T) {
	got, ok := pickHeaderSegment(readFixture(t, "multi_timeline_listing.json"))
	if !ok || got.path != realHeaderSegment || got.compression != "gz" || got.name != "000000010000000000000002" {
		t.Fatalf("got %+v, %v; want the 16405-byte %s", got, ok, realHeaderSegment)
	}

	sha := strings.Repeat("a", 40)
	listing := `{"0000000100000000/000000010000000000000003-` + sha + `.zst":{"type":"file","size":10},` +
		`"0000000100000000/000000010000000000000004-` + sha + `.lz4":{"type":"file","size":11},` +
		`"0000000100000000/000000010000000000000005.partial-` + sha + `.gz":{"type":"file","size":12},` +
		`"0000000100000000/000000010000000000000006-` + sha + `":{"type":"file","size":` + fmt.Sprint(int64(maxHeaderSegmentBytes)+1) + `},` +
		`"0000000100000000/000000010000000000000007-` + sha + `.bz2":{"type":"file","size":900}}`
	got, ok = pickHeaderSegment(listing)
	if !ok || got.name != "000000010000000000000007" || got.compression != "bz2" {
		t.Fatalf("got %+v, %v; want the bz2 segment (zst, lz4, .partial and oversized raw skipped)", got, ok)
	}
	if _, ok := pickHeaderSegment(`{"0000000100000000/000000010000000000000003-` + sha + `.zst":{"type":"file","size":10}}`); ok {
		t.Error("want no pick when every segment is zst")
	}
}

func TestResolveSegmentSize(t *testing.T) {
	pinned := []lsnSegment{{"0/3000028", "000000010000000000000003"}}
	ambiguous := []lsnSegment{{"1/00000028", "000000010000000100000000"}}
	cases := []struct {
		name       string
		pairs      []lsnSegment
		header     uint64
		wantSize   uint64
		wantSource string
		wantErr    bool
	}{
		{"header agrees", pinned, 16 << 20, 16 << 20, SegmentSizeFromWALHeader, false},
		{"header contradicts", pinned, 64 << 20, 0, "", true},
		{"header resolves ambiguity", ambiguous, 64 << 20, 64 << 20, SegmentSizeFromWALHeader, false},
		{"lsns pin it", pinned, 0, 16 << 20, SegmentSizeFromBackupLSNs, false},
		{"ambiguous, no header", ambiguous, 0, 16 << 20, SegmentSizeAssumedDefault, false},
	}
	for _, c := range cases {
		size, source, err := resolveSegmentSize(c.pairs, c.header)
		if (err != nil) != c.wantErr || size != c.wantSize || source != c.wantSource {
			t.Errorf("%s: got %d, %q, %v; want %d, %q, err=%v", c.name, size, source, err, c.wantSize, c.wantSource, c.wantErr)
		}
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

// segListing builds a recursive repo-ls JSON listing holding the given
// segments, each as a gzip'd file.
func segListing(names ...string) string {
	entries := make([]string, len(names))
	for i, name := range names {
		entries[i] = fmt.Sprintf(`"%s/%s-%s.gz":{"type":"file","size":100,"time":%d}`, name[:16], name, strings.Repeat("f", 40), 1790000000+i)
	}
	return "{" + strings.Join(entries, ",") + "}"
}

// threeTimelines: a backup in segment 3 of timeline 1, timeline 2 forked at
// 0/5000000 (segment 5) and timeline 3 at 0/8000000 (segment 8). Replay reads
// 1:3-4, 2:5-7, 3:8 onward.
var threeTimelines = struct {
	in      walCheckInput
	top     string
	history string
}{
	in:      walCheckInput{BackupLabel: "b", ArchiveID: "16-1", WALStart: "000000010000000000000003", WALStop: "000000010000000000000003", LSNStart: "0/3000028", LSNStop: "0/3000100"},
	top:     "0000000100000000\n0000000200000000\n0000000300000000\n00000002.history\n00000003.history\n",
	history: "1\t0/5000000\tno recovery target specified\n\n# comment\n2\t0/8000000\tno recovery target specified\n",
}

func TestEvaluateWAL_ThreeTimelines_Continuous(t *testing.T) {
	tl := threeTimelines
	// Timeline 1's copy of segment 5 is never read (timeline 2's is), so its
	// absence is normal; its presence must not hide a missing child copy.
	got := evaluateWALContinuity(tl.in, tl.top, tl.history, segListing(
		"000000010000000000000003", "000000010000000000000004",
		"000000020000000000000005", "000000020000000000000006", "000000020000000000000007",
		"000000030000000000000008", "000000030000000000000009"))
	if got.Status != WALContinuous || got.ArchiveEndWAL != "000000030000000000000009" {
		t.Fatalf("Status = %s (%s), ArchiveEndWAL = %s; want continuous to 000000030000000000000009", got.Status, got.Detail, got.ArchiveEndWAL)
	}
	if fmt.Sprint(got.TimelinePath) != "[1 2 3]" {
		t.Errorf("TimelinePath = %v, want [1 2 3]", got.TimelinePath)
	}
}

// A hole on a timeline in the middle of the path, neither the backup's nor
// the newest.
func TestEvaluateWAL_GapOnMiddleTimeline(t *testing.T) {
	tl := threeTimelines
	got := evaluateWALContinuity(tl.in, tl.top, tl.history, segListing(
		"000000010000000000000003", "000000010000000000000004",
		"000000020000000000000005", "000000020000000000000007",
		"000000030000000000000008"))
	if got.Status != WALBroken || !got.ConsistencyReachable {
		t.Fatalf("Status = %s, ConsistencyReachable = %v; want broken but reachable", got.Status, got.ConsistencyReachable)
	}
	if len(got.Gaps) != 1 || got.Gaps[0].Timeline != 2 || got.Gaps[0].FirstMissing != "000000020000000000000006" {
		t.Errorf("Gaps = %+v, want 000000020000000000000006 on timeline 2", got.Gaps)
	}
	if got.RecoverableToWAL != "000000020000000000000005" || got.ArchiveEndWAL != "000000030000000000000008" {
		t.Errorf("RecoverableToWAL = %s, ArchiveEndWAL = %s; want 000000020000000000000005 and 000000030000000000000008", got.RecoverableToWAL, got.ArchiveEndWAL)
	}
}

// The fork segment must come from the child timeline: the parent's copy
// holds the parent's WAL past the fork point, which replay must not use.
func TestEvaluateWAL_ForkSegmentOnlyOnParent_Broken(t *testing.T) {
	tl := threeTimelines
	got := evaluateWALContinuity(tl.in, tl.top, tl.history, segListing(
		"000000010000000000000003", "000000010000000000000004", "000000010000000000000005",
		"000000020000000000000006", "000000020000000000000007",
		"000000030000000000000008"))
	if got.Status != WALBroken || len(got.Gaps) != 1 || got.Gaps[0].FirstMissing != "000000020000000000000005" {
		t.Fatalf("Status = %s, Gaps = %+v; want broken with timeline 2's copy of segment 5 missing", got.Status, got.Gaps)
	}
}

// A backup spanning several segments needs all of them to reach consistency.
func TestEvaluateWAL_GapInsideMultiSegmentBackup_Unreachable(t *testing.T) {
	in := walCheckInput{BackupLabel: "b", ArchiveID: "16-1", WALStart: "000000010000000000000003", WALStop: "000000010000000000000006", LSNStart: "0/3000028", LSNStop: "0/6000100"}
	got := evaluateWALContinuity(in, "0000000100000000\n", "", segListing(
		"000000010000000000000003", "000000010000000000000004", "000000010000000000000006", "000000010000000000000007"))
	if got.Status != WALBroken || got.ConsistencyReachable {
		t.Fatalf("Status = %s, ConsistencyReachable = %v; want broken and unreachable", got.Status, got.ConsistencyReachable)
	}
	if !strings.Contains(got.Detail, "000000010000000000000005") || !strings.Contains(got.Detail, "could not start") {
		t.Errorf("Detail = %q, want it to name 000000010000000000000005 and say PostgreSQL could not start", got.Detail)
	}
}

func TestEvaluateWAL_GapListIsCapped(t *testing.T) {
	in := walCheckInput{BackupLabel: "b", ArchiveID: "16-1", WALStart: "000000010000000000000001", WALStop: "000000010000000000000001", LSNStart: "0/1000028", LSNStop: "0/1000100"}
	var names []string
	for seg := 1; seg <= 61; seg += 2 { // every other segment: 30 one-segment gaps
		names = append(names, fmt.Sprintf("0000000100000000%08X", seg))
	}
	got := evaluateWALContinuity(in, "0000000100000000\n", "", segListing(names...))
	if got.Status != WALBroken || len(got.Gaps) != maxReportedWALGaps {
		t.Fatalf("Status = %s, %d gaps; want broken with the list capped at %d", got.Status, len(got.Gaps), maxReportedWALGaps)
	}
	if got.RecoverableToWAL != "000000010000000000000001" || got.ArchiveEndWAL != "00000001000000000000003D" {
		t.Errorf("RecoverableToWAL = %s, ArchiveEndWAL = %s", got.RecoverableToWAL, got.ArchiveEndWAL)
	}
}

func TestEvaluateWAL_MalformedHistory_Unverified(t *testing.T) {
	tl := threeTimelines
	for _, history := range []string{"1\tnot-an-lsn\tx\n", "one\t0/5000000\tx\n", "1\n", ""} {
		got := evaluateWALContinuity(tl.in, tl.top, history, "{}")
		if got.Status != WALUnverified {
			t.Errorf("history %q: Status = %s (%s), want unverified", history, got.Status, got.Detail)
		}
	}
}

func TestCheckWALContinuity_ReadFailures_Unverified(t *testing.T) {
	res, err := parsePgBackRestInfo(readFixture(t, "multi_timeline_info.json"), "", 25*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []string{".history", "--recurse"} {
		run := fakeWALRun(t, readFixture(t, "multi_timeline_000000010000000000000002.gz"), nil)
		got := checkWALContinuity(res, func(args []string) (string, error) {
			if strings.Contains(strings.Join(args, " "), fail) {
				return "", fmt.Errorf("permission denied")
			}
			return run(args)
		})
		if got.Status != WALUnverified || !strings.Contains(got.Detail, "permission denied") {
			t.Errorf("failing %s: Status = %s, Detail = %q; want unverified with the error", fail, got.Status, got.Detail)
		}
	}
}

func TestSegmentSizeFromHeader_Unusable(t *testing.T) {
	const name = "000000010000000000000002"
	gz := []byte(readFixture(t, "multi_timeline_000000010000000000000002.gz"))
	file := headerSegment{path: name + ".gz", name: name, compression: "gz"}
	if _, err := segmentSizeFromHeader(gz[:12], file); err == nil {
		t.Error("want an error for a gzip stream cut off before the header")
	}
	if _, err := segmentSizeFromHeader([]byte("ERROR: [039]: HTTP request failed\n"), file); err == nil {
		t.Error("want an error when the output holds no gzip stream")
	}
	// The real segment under another name: its page address doesn't match.
	if _, err := segmentSizeFromHeader(gz, headerSegment{path: "x.gz", name: "000000010000000000000007", compression: "gz"}); err == nil {
		t.Error("want an error for a header that belongs to another segment")
	}
	if _, err := segmentSizeFromHeader([]byte("short"), headerSegment{path: name, name: name}); err == nil {
		t.Error("want an error for uncompressed output shorter than a header")
	}
}

// The last segments of an older timeline missing, with nothing archived
// after them: history shows that timeline ran on to its fork, so this is
// lost WAL, not the end of the archive (found against real PostgreSQL by
// TestIntegration_WALCheck_PredictionMatchesRealRecovery).
func TestEvaluateWAL_TailOfOlderTimelineMissing_Broken(t *testing.T) {
	tl := threeTimelines
	got := evaluateWALContinuity(tl.in, tl.top, tl.history, segListing(
		"000000010000000000000003", "000000010000000000000004",
		"000000020000000000000005", "000000020000000000000006"))
	if got.Status != WALBroken || got.RecoverableToWAL != "000000020000000000000006" || got.ArchiveEndWAL != "000000020000000000000006" {
		t.Fatalf("Status = %s, RecoverableToWAL = %s, ArchiveEndWAL = %s; want broken, both 000000020000000000000006",
			got.Status, got.RecoverableToWAL, got.ArchiveEndWAL)
	}
	if !strings.Contains(got.Detail, "000000020000000000000007 on timeline 2 is missing") || !strings.Contains(got.Detail, "history shows WAL past it") {
		t.Errorf("Detail = %q", got.Detail)
	}
}

// The backup's own WAL missing from a single-timeline archive with nothing
// archived after it: found on Kubernetes (2026-10-20), where a fresh pod's
// archive held only the backup's one segment, start = stop. The verdict was
// right but the gap list came back empty, the walk having stopped at "nothing
// archived on the newest timeline" before recording any gap. The missing
// range is the gap the health triage and the fault's diagnosis point to.
func TestEvaluateWAL_OnlyBackupSegmentMissing_GapRecorded(t *testing.T) {
	in := multiTimelineInput(t) // backup on timeline 1, start = stop = 000000010000000000000003
	var raw map[string]any
	if err := json.Unmarshal([]byte(readFixture(t, "multi_timeline_listing.json")), &raw); err != nil {
		t.Fatal(err)
	}
	for k := range raw {
		// Keep timeline 1's segments from before the backup only.
		if k != "." && k != "0000000100000000" && !strings.Contains(k, "/000000010000000000000001-") && !strings.Contains(k, "/000000010000000000000002-") {
			delete(raw, k)
		}
	}
	listing, _ := json.Marshal(raw)
	got := evaluateWALContinuity(in, "0000000100000000\n", "", string(listing))

	if got.Status != WALBroken || got.ConsistencyReachable {
		t.Fatalf("Status = %s, ConsistencyReachable = %v (%s), want broken and unreachable", got.Status, got.ConsistencyReachable, got.Detail)
	}
	if len(got.Gaps) != 1 || got.Gaps[0].FirstMissing != "000000010000000000000003" || got.Gaps[0].LastMissing != "000000010000000000000003" || got.Gaps[0].Timeline != 1 {
		t.Errorf("Gaps = %+v, want one gap: 000000010000000000000003 on timeline 1", got.Gaps)
	}
	if !strings.Contains(got.Detail, "000000010000000000000003") || !strings.Contains(got.Detail, "could not start") {
		t.Errorf("Detail = %q, want it to name the missing segment and say PostgreSQL could not start", got.Detail)
	}
}
