package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// commitLine builds a Transaction record in the format pg_waldump prints. The
// forms other than the captured one are CONSTRUCTED from that format and from
// PostgreSQL's source, not captured on a host: the tests say which is which.
func commitLine(xid int, lsn, desc string) string {
	return "rmgr: Transaction len (rec/tot):     34/    34, tx:        " + itoa(xid) + ", lsn: " + lsn +
		", prev 0/42008768, desc: " + desc
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Replace(time.Duration(n).String(), "ns", "", 1))
}

// CAPTURED on Ubuntu 24.04 / PostgreSQL 16.15 (2026-11-10): one DDL commit, with
// the zone abbreviation of the host's own TZ and 1245 bytes of invalidation
// messages. The commit is counted, its LSN and xid are exact, and its time is
// NOT read: "EST" is an abbreviation, and none is guessed.
func TestParseWALCommits_CapturedDDLCommitInTheHostsOwnZone(t *testing.T) {
	line := readFixture(t, "waldump_commit_est.txt")
	if len(line) < 1000 {
		t.Fatalf("the fixture is %d bytes: it should be the long real line", len(line))
	}
	got, err := ParseWALCommits(strings.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commits) != 1 || got.Aborts != 0 || got.Unread != 0 {
		t.Fatalf("%+v", got)
	}
	c := got.Commits[0]
	if c.XID != 784 || c.LSN != 0x420087A0 {
		t.Errorf("xid %d lsn %#x, want 784 and 0x420087A0 (0/420087A0)", c.XID, c.LSN)
	}
	if !c.TimeUnread || !c.Time.IsZero() {
		t.Errorf("an EST timestamp was read as %v: abbreviations are not guessed", c.Time)
	}
}

// CAPTURED: what pg_waldump prints on a segment it cannot read as WAL (a recycled
// file in pg_wal), on stderr, with a non-zero exit. It is kept as the end error.
func TestParseWALCommits_CapturedEndError(t *testing.T) {
	got, err := ParseWALCommits(strings.NewReader("pg_waldump: error: could not find a valid record after 0/44000000\n"))
	if err != nil || len(got.Commits) != 0 || got.EndError != "could not find a valid record after 0/44000000" {
		t.Errorf("%+v, %v", got, err)
	}
}

// CONSTRUCTED from the format: with TZ=UTC the zone reads "UTC" and the commit
// has an exact time. The same instant in a numeric zone reads the same.
func TestParseWALCommits_TimeInUTCAndNumericZones(t *testing.T) {
	want := time.Date(2026, 11, 9, 17, 18, 25, 702361000, time.UTC) // the captured commit, in UTC
	for name, desc := range map[string]string{
		"UTC":               "COMMIT 2026-11-09 17:18:25.702361 UTC; rels: base/5/1",
		"a numeric zone":    "COMMIT 2026-11-09 12:18:25.702361 -05; rels: base/5/1",
		"with minutes":      "COMMIT 2026-11-09 22:48:25.702361 +05:30; rels: base/5/1",
		"no fraction":       "COMMIT 2026-11-09 17:18:25 UTC",
		"nothing after":     "COMMIT 2026-11-09 17:18:25.702361 UTC",
		"GMT":               "COMMIT 2026-11-09 17:18:25.702361 GMT;",
		"four digit offset": "COMMIT 2026-11-09 11:48:25.702361 -0530;",
	} {
		got, err := ParseWALCommits(strings.NewReader(commitLine(1, "0/420087A0", desc) + "\n"))
		if err != nil || len(got.Commits) != 1 {
			t.Fatalf("%s: %+v, %v", name, got, err)
		}
		c := got.Commits[0]
		w := want
		if name == "no fraction" {
			w = want.Truncate(time.Second)
		}
		if c.TimeUnread || !c.Time.Equal(w) {
			t.Errorf("%s: time %v (unread %v), want %v", name, c.Time, c.TimeUnread, w)
		}
	}
}

// Abbreviations other than UTC are refused whatever they are: EST, IST and CST
// each name more than one zone.
func TestParseWALTime_RefusesAbbreviationsItCannotRead(t *testing.T) {
	for _, zone := range []string{"EST", "EDT", "IST", "CST", "PST8PDT", "CET", "", "+", "+5", "+25", "+0575", "x05"} {
		if tm, ok := parseWALTime("2026-11-09 12:18:25.702361", zone); ok {
			t.Errorf("zone %q was read as %v", zone, tm)
		}
	}
	if _, ok := parseWALTime("not a time", "UTC"); ok {
		t.Error("an unreadable timestamp was accepted")
	}
	if _, ok := parseWALTime("2026-13-45 99:99:99", "UTC"); ok {
		t.Error("an impossible timestamp was accepted")
	}
}

// A commit that cannot be timed is still a commit: the count must not depend on
// the zone.
func TestParseWALCommits_AnUntimedCommitIsStillCounted(t *testing.T) {
	in := commitLine(1, "0/10", "COMMIT 2026-11-09 12:18:25 UTC") + "\n" +
		commitLine(2, "0/20", "COMMIT 2026-11-09 12:18:26 EST") + "\n" +
		commitLine(3, "0/30", "COMMIT 2026-11-09 12:18:27 UTC") + "\n"
	got, _ := ParseWALCommits(strings.NewReader(in))
	if len(got.Commits) != 3 || got.Commits[1].TimeUnread != true || got.Commits[0].TimeUnread || got.Commits[2].TimeUnread {
		t.Fatalf("%+v", got.Commits)
	}
	// In order, with their own LSNs and xids.
	for i, want := range []uint64{0x10, 0x20, 0x30} {
		if got.Commits[i].LSN != want || got.Commits[i].XID != uint64(i+1) {
			t.Errorf("commit %d = %+v", i, got.Commits[i])
		}
	}
}

// The lines that matter most are the long ones: a DDL commit's invalidation
// messages. A reader with a line limit loses them, and with them the commits
// after, so lines far longer than any buffer must parse and must not swallow
// the next one.
func TestParseWALCommits_LinesFarLongerThanAnyBuffer(t *testing.T) {
	for _, size := range []int{2000, 64*1024 - 10, 64 * 1024, 64*1024 + 10, 1 << 20, 5 << 20} {
		long := commitLine(7, "0/AAAA", "COMMIT 2026-11-09 17:18:25.702361 UTC; inval msgs: "+strings.Repeat("catcache 7 ", size/11))
		in := commitLine(6, "0/9999", "COMMIT 2026-11-09 17:18:24 UTC") + "\n" + long + "\n" + commitLine(8, "0/BBBB", "COMMIT 2026-11-09 17:18:26 UTC") + "\n"
		got, err := ParseWALCommits(strings.NewReader(in))
		if err != nil || len(got.Commits) != 3 || got.Commits[1].XID != 7 || got.Commits[1].LSN != 0xAAAA || got.Commits[2].XID != 8 {
			t.Errorf("a %d byte line: %d commits, err %v", size, len(got.Commits), err)
		}
	}
}

// CONSTRUCTED: the other endings, kept apart so that the count of commits is only
// commits, and what was not understood is said, not dropped.
func TestParseWALCommits_AbortsPreparedAndWhatIsNotUnderstood(t *testing.T) {
	in := strings.Join([]string{
		commitLine(1, "0/10", "COMMIT 2026-11-09 17:18:25 UTC"),
		commitLine(2, "0/20", "ABORT 2026-11-09 17:18:26 UTC"),
		commitLine(3, "0/30", "ABORT_PREPARED 99: 2026-11-09 17:18:27 UTC"),
		commitLine(4, "0/40", "COMMIT_PREPARED 98: 2026-11-09 17:18:28 UTC"),
		commitLine(5, "0/50", "COMMIT somethingelse entirely"), // a COMMIT it cannot read
		commitLine(6, "0/60", "ASSIGNMENT xtop 5: subxacts: 6"),
		"rmgr: Heap        len (rec/tot):     59/    59, tx:          9, lsn: 0/70, prev 0/60, desc: INSERT off: 1, flags: 0x00",
		"",
		"some other line",
	}, "\n")
	got, err := ParseWALCommits(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commits) != 2 || got.Commits[0].XID != 1 || got.Commits[1].XID != 4 || got.Commits[1].TimeUnread {
		t.Errorf("commits = %+v", got.Commits)
	}
	if got.Aborts != 2 {
		t.Errorf("aborts = %d, want 2 (ABORT and ABORT_PREPARED)", got.Aborts)
	}
	if got.Unread != 1 {
		t.Errorf("unread = %d, want 1 (the COMMIT it could not read), and not the assignment or the heap record", got.Unread)
	}
}

// A reader that fails part-way returns what it had and the error, not a clean
// result that looks complete.
func TestParseWALCommits_AFailingReaderIsAnError(t *testing.T) {
	in := commitLine(1, "0/10", "COMMIT 2026-11-09 17:18:25 UTC") + "\n"
	r := io.MultiReader(strings.NewReader(in), iotest.ErrReader(errors.New("connection reset")))
	got, err := ParseWALCommits(r)
	if err == nil || !strings.Contains(err.Error(), "connection reset") || len(got.Commits) != 1 {
		t.Errorf("%+v, %v", got, err)
	}
	// And empty input is no commits and no error.
	if got, err := ParseWALCommits(strings.NewReader("")); err != nil || len(got.Commits) != 0 {
		t.Errorf("empty: %+v, %v", got, err)
	}
}

// A commit whose LSN is out of range is not made up into one: it is unread, and a
// caller that needs an exact count is told the count may be short.
func TestParseWALCommits_AnLSNOutOfRangeIsUnread(t *testing.T) {
	in := commitLine(1, "123456789/1", "COMMIT 2026-11-09 17:18:25 UTC") + "\n" + commitLine(2, "0/20", "COMMIT 2026-11-09 17:18:25 UTC") + "\n"
	got, _ := ParseWALCommits(strings.NewReader(in))
	if len(got.Commits) != 1 || got.Commits[0].XID != 2 || got.Unread != 1 {
		t.Errorf("%+v", got)
	}
}

func TestParseNumericZone(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"+00": 0, "+05": 5 * time.Hour, "-05": -5 * time.Hour, "+0530": 5*time.Hour + 30*time.Minute,
		"-05:30": -(5*time.Hour + 30*time.Minute), "+14": 14 * time.Hour, "-12": -12 * time.Hour,
	} {
		if got, ok := parseNumericZone(in); !ok || got != want {
			t.Errorf("%q = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "+", "05", "+5", "+123", "+15", "+0560", "+ab", "+05zz", "UTC"} {
		if _, ok := parseNumericZone(bad); ok {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// The only text a caller supplies that reaches a command line is an LSN, and it
// must be one, exactly.
func TestWalDumpArgs(t *testing.T) {
	args, err := walDumpArgs("/tmp/x/000000330000000000000042", "0/42000028", "0/42FFFFF8")
	want := []string{"-r", "Transaction", "-s", "0/42000028", "-e", "0/42FFFFF8", "/tmp/x/000000330000000000000042"}
	if err != nil || strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("%v, %v", args, err)
	}
	// Either end may be left out.
	if args, _ := walDumpArgs("/s", "", ""); strings.Join(args, " ") != "-r Transaction /s" {
		t.Errorf("no range: %v", args)
	}
	if args, _ := walDumpArgs("/s", "A/B", ""); strings.Join(args, " ") != "-r Transaction -s A/B /s" {
		t.Errorf("start only: %v", args)
	}
	for _, bad := range []string{"0/4200; rm -rf /", " 0/42", "0/42 ", "0x0/42", "0/", "/42", "0", "0/42\n", "123456789/1", "0/$(id)", "-s", "0/42 -e 0/43"} {
		if _, err := walDumpArgs("/s", bad, ""); err == nil {
			t.Errorf("start %q was accepted", bad)
		}
		if _, err := walDumpArgs("/s", "", bad); err == nil {
			t.Errorf("end %q was accepted", bad)
		}
	}
	for _, path := range []string{"", "/a\nb", "/a\x00b"} {
		if _, err := walDumpArgs(path, "", ""); err == nil {
			t.Errorf("path %q was accepted", path)
		}
	}
}

// pg_waldump prints times in the zone of the process that runs it, so it is run in
// UTC, always, and not in whatever the host is set to.
func TestWalDumpEnvIsUTC(t *testing.T) {
	if len(walDumpEnv) != 1 || walDumpEnv[0] != "TZ=UTC" {
		t.Errorf("walDumpEnv = %v", walDumpEnv)
	}
}
