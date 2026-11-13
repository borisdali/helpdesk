package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── the script's own rules ────────────────────────────────────────────────────

func twoRangePlan(t *testing.T, mode string) WALReadPlan {
	t.Helper()
	wal := archiveOf(0x40, 0x42, 0, 3, 3)
	live := liveAt("0/45000100", 3, "/var/lib/postgresql/16/main")
	if mode == "lsn" {
		p, why := planWALRead(RecoveryTarget{Type: TargetLSN}, "0/42000A28", wal, live, 16)
		if why != "" {
			t.Fatal(why)
		}
		return p
	}
	p, why := planWALRead(timeTarget(rangeT0.Add(90*time.Second)), "", wal, live, 16)
	if why != "" {
		t.Fatal(why)
	}
	return p
}

func TestWALReadScript_ShapeAndRules(t *testing.T) {
	p := twoRangePlan(t, "time")
	s, err := walReadScript("main", "/var/lib/postgresql/16/main", p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"DD='/var/lib/postgresql/16/main'",
		"grep '^pg1-path=' /etc/pgbackrest.conf", // the fallback when the data directory is not given
		"/usr/lib/postgresql/$MAJOR/bin/pg_waldump", "/usr/pgsql-$MAJOR/bin/pg_waldump", "command -v pg_waldump",
		"export TZ=UTC",
		"pgbackrest --stanza=main archive-get",
		`-p "$T" -r Transaction 000000030000000000000042 000000030000000000000042`,
		`-p "$DD/pg_wal" -r Transaction -e 0/45000100 000000030000000000000043 000000030000000000000045`,
		"range_begin=0", "range_rc=", "range_end=1", "cut -c1-220", "head -c 50331648",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the script lacks %q:\n%s", want, s)
		}
	}
	// No LSN mode: no first record is asked for.
	if strings.Contains(s, "first_record") || strings.Contains(s, " -s ") {
		t.Errorf("a time-target script asks for an LSN start:\n%s", s)
	}
	// An LSN target starts from the LSN and finds the first record there, once.
	s, err = walReadScript("main", "", twoRangePlan(t, "lsn"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(s, "first_record=") != 1 || !strings.Contains(s, "-s 0/42000A28") || !strings.Contains(s, "DD=\n") {
		t.Errorf("lsn script:\n%s", s)
	}
}

// Everything that reaches the command line is checked: a segment name, an LSN, the
// stanza and the data directory are never taken on trust.
func TestWALReadScript_RefusesWhatItCouldNotPassToAShell(t *testing.T) {
	ok := twoRangePlan(t, "time")
	edit := func(f func(p *WALReadPlan)) WALReadPlan {
		cp := ok
		cp.Ranges = append([]WALReadRange(nil), ok.Ranges...)
		for i := range cp.Ranges {
			cp.Ranges[i].Names = append([]string(nil), ok.Ranges[i].Names...)
		}
		f(&cp)
		return cp
	}
	for name, tc := range map[string]struct {
		stanza, dir string
		plan        WALReadPlan
	}{
		"a stanza with a command":   {"main; rm -rf /", "/d", ok},
		"a stanza with a space":     {"my stanza", "/d", ok},
		"an empty stanza":           {"", "/d", ok},
		"a leading dash":            {"-x", "/d", ok},
		"a data dir with a command": {"main", "/d; id", ok},
		"a data dir with $()":       {"main", "/d/$(id)", ok},
		"a data dir with a quote":   {"main", "/d/'x", ok},
		"a relative data dir":       {"main", "d/e", ok},
		"a data dir with a space":   {"main", "/d e", ok},
		"nothing to read":           {"main", "/d", WALReadPlan{}},
		"a bad first segment":       {"main", "/d", edit(func(p *WALReadPlan) { p.Ranges[0].First = "x; id" })},
		"a lowercase segment":       {"main", "/d", edit(func(p *WALReadPlan) { p.Ranges[0].Last = "00000003000000000000004a" })},
		"a short segment name":      {"main", "/d", edit(func(p *WALReadPlan) { p.Ranges[0].First = "0000000300000000000000" })},
		"a bad fetched name":        {"main", "/d", edit(func(p *WALReadPlan) { p.Ranges[0].Names[0] = "$(id)" })},
		"no names to fetch":         {"main", "/d", edit(func(p *WALReadPlan) { p.Ranges[0].Names = nil })},
		"a bad start LSN":           {"main", "/d", edit(func(p *WALReadPlan) { p.Ranges[0].StartLSN = "0/1; id" })},
		"a bad end LSN":             {"main", "/d", edit(func(p *WALReadPlan) { p.Ranges[1].EndLSN = " 0/1" })},
		"an unknown source":         {"main", "/d", edit(func(p *WALReadPlan) { p.Ranges[0].Source = "ftp" })},
	} {
		if s, err := walReadScript(tc.stanza, tc.dir, tc.plan); err == nil {
			t.Errorf("%s: a script was built:\n%s", name, s)
		}
	}
}

// ── running the generated script for real ─────────────────────────────────────

type walSandbox struct {
	t    *testing.T
	work string
	env  []string
	sh   string
}

// newWALSandbox puts fake pg_waldump and pgbackrest on a PATH that has only the
// tools the script uses, so a pg_waldump on the machine running the tests cannot
// be found by mistake.
func newWALSandbox(t *testing.T, withDump bool) *walSandbox {
	t.Helper()
	root := t.TempDir()
	bin, work := filepath.Join(root, "bin"), filepath.Join(root, "work")
	for _, d := range []string{bin, work, filepath.Join(work, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"grep", "cut", "head", "cat", "mktemp", "rm", "timeout"} {
		if p, err := exec.LookPath(tool); err == nil {
			if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
				t.Fatal(err)
			}
		} else if tool != "timeout" {
			t.Skipf("%s is not available", tool)
		}
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if withDump {
		write("pg_waldump", `[ "$1" = "--version" ] && { echo "pg_waldump (PostgreSQL) 16.15"; exit 0; }
echo "TZ=$TZ args=$*" >> "$WORK/dump.log"
case " $* " in *" -n "*) cat "$WORK/first.out" 2>/dev/null; exit 0;; esac
[ -f "$WORK/dump.err" ] && cat "$WORK/dump.err" >&2
cat "$WORK/dump.out" 2>/dev/null
exit "$(cat "$WORK/dump.rc" 2>/dev/null || echo 0)"
`)
	}
	write("pgbackrest", `echo "pgbackrest $*" >> "$WORK/fetch.log"
case " $FAKE_FETCH_FAIL " in *" $3 "*) exit 1;; esac
echo seg > "$4"
`)
	return &walSandbox{t: t, work: work, sh: sh, env: []string{"PATH=" + bin, "WORK=" + work, "TMPDIR=" + filepath.Join(work, "tmp"), "HOME=" + work}}
}

func (s *walSandbox) put(name, body string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.work, name), []byte(body), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *walSandbox) log(name string) string {
	b, _ := os.ReadFile(filepath.Join(s.work, name))
	return string(b)
}

func (s *walSandbox) run(script string, extraEnv ...string) string {
	s.t.Helper()
	cmd := exec.Command(s.sh, "-c", script)
	cmd.Env = append(append([]string(nil), s.env...), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("the script failed: %v\n%s", err, out)
	}
	return string(out)
}

func (s *walSandbox) leftovers() []string {
	entries, _ := os.ReadDir(filepath.Join(s.work, "tmp"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// The script runs: it fetches what the plan names, runs pg_waldump once per range
// with TZ=UTC and the bounds the plan set, prints its markers, and leaves nothing
// behind. Its output reads back into the same commits.
func TestWALReadScript_RunsAndItsOutputReadsBack(t *testing.T) {
	sb := newWALSandbox(t, true)
	dd := t.TempDir()
	if err := os.WriteFile(filepath.Join(dd, "PG_VERSION"), []byte("16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	live := liveAt("0/45000100", 3, dd)
	p, why := planWALRead(timeTarget(rangeT0.Add(90*time.Second)), "", archiveOf(0x40, 0x42, 0, 3, 3), live, 16)
	if why != "" {
		t.Fatal(why)
	}
	script, err := walReadScript("main", dd, p)
	if err != nil {
		t.Fatal(err)
	}
	// The error line first: stderr is unbuffered and stdout is not, so on a real
	// host it comes out before the records it follows.
	sb.put("dump.err", "pg_waldump: error: warning line that is not an end of WAL\n")
	sb.put("dump.out", readFixture(t, "waldump_live_utc.txt")[:strings.Index(readFixture(t, "waldump_live_utc.txt"), "pg_waldump: error")])

	out := sb.run(script)
	got, err := parseWALReadOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if got.Missing || got.TmpFailed || got.Capped || !strings.Contains(got.Version, "16.15") || len(got.Ranges) != 2 {
		t.Fatalf("%+v\n%s", got, out)
	}
	for i, r := range got.Ranges {
		if r.RC == nil || *r.RC != 0 || r.FetchFailed != "" || len(r.Parsed.Commits) != 3 || r.Parsed.Commits[0].XID != 785 {
			t.Errorf("range %d = %+v", i, r)
		}
	}
	// What it ran: the archive range fetched, both ranges decoded in UTC, bounded as planned.
	if f := sb.log("fetch.log"); strings.Count(f, "archive-get") != 1 || !strings.Contains(f, "--stanza=main archive-get 000000030000000000000042 ") {
		t.Errorf("fetches:\n%s", f)
	}
	d := sb.log("dump.log")
	if strings.Count(d, "TZ=UTC") != 2 || !strings.Contains(d, "-r Transaction 000000030000000000000042 000000030000000000000042") ||
		!strings.Contains(d, "-p "+filepath.Join(dd, "pg_wal")+" -r Transaction -e 0/45000100 000000030000000000000043 000000030000000000000045") {
		t.Errorf("pg_waldump calls:\n%s", d)
	}
	// The temp directory was removed.
	if l := sb.leftovers(); len(l) != 0 {
		t.Errorf("left behind in the temp directory: %v", l)
	}
}

// Over SSH the script goes out as one quoted string (shellCommand), which the
// remote shell parses again. The script has quotes, $(), pipes and newlines in it,
// so what the remote shell runs must be what a direct run runs: the same output.
func TestWALReadScript_SurvivesTheSSHCommandLine(t *testing.T) {
	sb := newWALSandbox(t, true)
	dd := t.TempDir()
	live := liveAt("0/45000100", 3, dd)
	p, why := planWALRead(timeTarget(rangeT0.Add(90*time.Second)), "", archiveOf(0x40, 0x42, 0, 3, 3), live, 16)
	if why != "" {
		t.Fatal(why)
	}
	script, err := walReadScript("main", dd, p)
	if err != nil {
		t.Fatal(err)
	}
	captured := readFixture(t, "waldump_live_utc.txt")
	sb.put("dump.out", captured[:strings.Index(captured, "pg_waldump: error")]) // the commits, without the end-of-WAL line

	direct := sb.run(script)
	// The sandbox's PATH has no sh (on purpose, so a real pg_waldump cannot be found
	// by mistake): name it by path, as a login shell's PATH would resolve it.
	oneString := shellCommand(sb.sh, []string{"-c", script}, nil)
	viaSSH := sb.run(oneString)
	if direct != viaSSH {
		t.Errorf("the script behaves differently through the SSH command line.\ndirect:\n%s\nvia ssh:\n%s", direct, viaSSH)
	}
	got, err := parseWALReadOutput(viaSSH)
	if err != nil || len(got.Ranges) != 2 || got.Ranges[0].RC == nil || len(got.Ranges[1].Parsed.Commits) != 3 {
		t.Errorf("%+v, %v\n%s", got, err, viaSSH)
	}
	// The data directory with a quote is refused before it could reach the string at all.
	if _, err := walReadScript("main", "/d/it's", p); err == nil {
		t.Error("a data directory with a quote was accepted")
	}
}

// A segment the archive will not give up is a failure of that range, named, and
// pg_waldump is not run on what was not fetched.
func TestWALReadScript_AFailedFetchIsNamedAndNothingIsDecoded(t *testing.T) {
	sb := newWALSandbox(t, true)
	p, _ := planWALRead(timeTarget(rangeT0.Add(-time.Hour)), "", archiveOf(0x40, 0x42, 0, 3, 3), nil, 16)
	script, _ := walReadScript("main", "", p)
	out := sb.run(script, "FAKE_FETCH_FAIL=000000030000000000000041")
	got, err := parseWALReadOutput(out)
	if err != nil || len(got.Ranges) != 1 || got.Ranges[0].FetchFailed != "000000030000000000000041" || got.Ranges[0].RC != nil {
		t.Fatalf("%+v, %v\n%s", got, err, out)
	}
	if sb.log("dump.log") != "" {
		t.Errorf("pg_waldump ran on segments that were not fetched:\n%s", sb.log("dump.log"))
	}
	if why := got.failure(p); !strings.Contains(why, "would not give up segment 000000030000000000000041") {
		t.Errorf("failure = %q", why)
	}
}

// How pg_waldump ended is reported, because a read cut short by a timeout looks
// like a short successful one.
func TestWALReadScript_ReportsHowPgWaldumpEnded(t *testing.T) {
	sb := newWALSandbox(t, true)
	p, _ := planWALRead(timeTarget(rangeT0.Add(-time.Hour)), "", archiveOf(0x40, 0x40, 0, 3, 3), nil, 16)
	script, _ := walReadScript("main", "", p)
	// Killed mid-line, as timeout(1) would: the last record has no newline, and the
	// marker after it must not be lost to it.
	sb.put("dump.out", readFixture(t, "waldump_live_utc.txt")[:120])
	sb.put("dump.rc", "124\n") // what timeout(1) exits with
	got, _ := parseWALReadOutput(sb.run(script))
	if len(got.Ranges) != 1 || got.Ranges[0].RC == nil || *got.Ranges[0].RC != 124 {
		t.Fatalf("the exit status was lost to the unterminated line: %+v", got)
	}
	if why := got.failure(p); !strings.Contains(why, "pg_waldump failed on range 1 (exit 124)") {
		t.Errorf("failure = %q", why)
	}
	// With the captured error line, the failure says what pg_waldump said.
	sb.put("dump.err", "pg_waldump: error: could not find a valid record after 0/44000000\n")
	sb.put("dump.rc", "1\n")
	got, _ = parseWALReadOutput(sb.run(script))
	if why := got.failure(p); !strings.Contains(why, "exit 1): could not find a valid record after 0/44000000") {
		t.Errorf("failure = %q", why)
	}
}

func TestWALReadScript_NoBinaryOnTheInstance(t *testing.T) {
	sb := newWALSandbox(t, false)
	p, _ := planWALRead(timeTarget(rangeT0.Add(-time.Hour)), "", archiveOf(0x40, 0x40, 0, 3, 3), nil, 16)
	script, _ := walReadScript("main", "", p)
	got, err := parseWALReadOutput(sb.run(script))
	if err != nil || !got.Missing || len(got.Ranges) != 0 {
		t.Fatalf("%+v, %v", got, err)
	}
	if why := got.failure(p); !strings.Contains(why, "pg_waldump was not found on the instance") {
		t.Errorf("failure = %q", why)
	}
	if sb.log("fetch.log") != "" {
		t.Error("segments were fetched with nothing to read them")
	}
}

// For an LSN target the first record at or after it is asked for, and a record
// longer than the cut is shortened without losing its LSN.
func TestWALReadScript_FirstRecordForAnLSNTargetAndLongLines(t *testing.T) {
	sb := newWALSandbox(t, true)
	p, _ := planWALRead(RecoveryTarget{Type: TargetLSN}, "0/42000A28", archiveOf(0x40, 0x42, 0, 3, 3), nil, 16)
	script, _ := walReadScript("main", "", p)
	sb.put("first.out", "rmgr: Heap        len (rec/tot):     59/    59, tx:        790, lsn: 0/42000A30, prev 0/42000A28, desc: INSERT off: 1\n")
	sb.put("dump.out", readFixture(t, "waldump_commit_est.txt")+strings.Replace(readFixture(t, "waldump_commit_est.txt"), "EST", "UTC", 1))
	got, err := parseWALReadOutput(sb.run(script))
	if err != nil || got.FirstRecord == nil || *got.FirstRecord != 0x42000A30 {
		t.Fatalf("%+v, %v", got, err)
	}
	if len(got.Ranges) != 1 || len(got.Ranges[0].Parsed.Commits) != 2 {
		t.Fatalf("%+v", got.Ranges)
	}
	if got.Ranges[0].Parsed.Commits[0].LSN != 0x420087A0 || !got.Ranges[0].Parsed.Commits[0].TimeUnread || got.Ranges[0].Parsed.Commits[1].TimeUnread {
		t.Errorf("%+v", got.Ranges[0].Parsed.Commits)
	}
	if !strings.Contains(sb.log("dump.log"), "-p ") || !strings.Contains(sb.log("dump.log"), "-s 0/42000A28 -n 1") {
		t.Errorf("the first record was not asked for:\n%s", sb.log("dump.log"))
	}
}

// ── reading the output back ───────────────────────────────────────────────────

func TestParseWALReadOutput_Markers(t *testing.T) {
	out := strings.Join([]string{
		"stray line before anything",
		"waldump_version=pg_waldump (PostgreSQL) 16.15 (Ubuntu 16.15-1.pgdg24.04+2)",
		"range_begin=0",
		"first_record=rmgr: Heap len (rec/tot): 59/59, tx: 1, lsn: 0/A0, prev 0/90, desc: INSERT",
		commitLine(1, "0/B0", "COMMIT 2026-11-09 17:18:25 UTC"),
		"range_rc=0",
		"range_end=0",
		"range_begin=1",
		"fetch_failed=000000030000000000000043",
		"range_end=1",
		"range_begin=2",
		commitLine(2, "0/C0", "COMMIT 2026-11-09 17:18:26 UTC"),
		"range_rc=oops",
		"range_end=2",
		"range_begin=3",
		commitLine(3, "0/D0", "COMMIT 2026-11-09 17:18:27 UTC"),
		"range_end=3", // no rc, as when the script was cut short
		"",
	}, "\n")
	got, err := parseWALReadOutput(out)
	if err != nil || len(got.Ranges) != 4 {
		t.Fatalf("%+v, %v", got, err)
	}
	if got.FirstRecord == nil || *got.FirstRecord != 0xA0 || !strings.HasPrefix(got.Version, "pg_waldump (PostgreSQL) 16.15") {
		t.Errorf("first %v version %q", got.FirstRecord, got.Version)
	}
	if r := got.Ranges[0]; r.RC == nil || *r.RC != 0 || len(r.Parsed.Commits) != 1 || r.Parsed.Commits[0].XID != 1 {
		t.Errorf("range 0 = %+v", r)
	}
	if got.Ranges[1].FetchFailed != "000000030000000000000043" || got.Ranges[1].RC != nil {
		t.Errorf("range 1 = %+v", got.Ranges[1])
	}
	if got.Ranges[2].RC != nil || len(got.Ranges[2].Parsed.Commits) != 1 || got.Ranges[3].RC != nil || len(got.Ranges[3].Parsed.Commits) != 1 {
		t.Errorf("ranges 2, 3 = %+v %+v", got.Ranges[2], got.Ranges[3])
	}
	// Markers outside any range, and a stray rc, do no harm.
	if got, err := parseWALReadOutput("fetch_failed=X\nrange_rc=0\nrange_end=0\nfirst_record=\nfirst_record=junk\n"); err != nil || len(got.Ranges) != 0 || got.FirstRecord != nil {
		t.Errorf("%+v, %v", got, err)
	}
	// An output at the cap is not trusted.
	big := strings.Repeat("x", walReadOutputCap-100)
	if got, _ := parseWALReadOutput(big); !got.Capped {
		t.Error("an output at the cap was trusted")
	}
	if got, _ := parseWALReadOutput("range_begin=0\nrange_rc=0\nrange_end=0\n"); got.Capped {
		t.Error("a small output was called capped")
	}
}

func TestParseWALReadOutput_NoTempDirectory(t *testing.T) {
	got, err := parseWALReadOutput("waldump_tmp_failed=1\n")
	if err != nil || !got.TmpFailed || len(got.Ranges) != 0 {
		t.Fatalf("%+v, %v", got, err)
	}
	if why := got.failure(WALReadPlan{Ranges: []WALReadRange{{}}}); !strings.Contains(why, "no temporary directory") {
		t.Errorf("failure = %q", why)
	}
}

// ctxRunner records the deadline the command was run under.
type ctxRunner struct {
	deadline time.Time
	has      bool
	out      string
	err      error
}

func (r *ctxRunner) Run(ctx context.Context, _ string, _ []string, _ []string) (string, error) {
	r.deadline, r.has = ctx.Deadline()
	return r.out, r.err
}

// The real runner sends the script through the instance's own maintenance path and
// bounds the whole read: a hung host cannot hold a plan forever.
func TestWalReadFn_RunsThroughMaintenanceUnderADeadline(t *testing.T) {
	r := &ctxRunner{out: "range_begin=0\n"}
	old := cmdRunner
	cmdRunner = r
	defer func() { cmdRunner = old }()

	before := time.Now()
	out, err := walReadFn(context.Background(), pgInstance{local: true}, "echo hi")
	if err != nil || out != "range_begin=0\n" {
		t.Fatalf("%q, %v", out, err)
	}
	if !r.has || r.deadline.Before(before.Add(walReadTimeout-5*time.Second)) || r.deadline.After(before.Add(walReadTimeout+5*time.Second)) {
		t.Errorf("deadline = %v (has %v), want about %s from now", r.deadline, r.has, walReadTimeout)
	}
	// A failure comes back as one.
	r.err = errors.New("boom")
	if _, err := walReadFn(context.Background(), pgInstance{local: true}, "echo hi"); err == nil || err.Error() != "boom" {
		t.Errorf("err = %v", err)
	}
}

// A serving instance's data directory reaches the script, and its own WAL is read.
func TestMeasureTransactionLoss_ServingInstanceReadsItsOwnWAL(t *testing.T) {
	out := strings.Join([]string{
		"range_begin=0", commitLine(1, "0/42000010", "COMMIT 2026-11-10 12:01:40 UTC"), "", "range_rc=0", "range_end=0",
		"range_begin=1", commitLine(2, "0/43000010", "COMMIT 2026-11-10 12:02:40 UTC"), "", "range_rc=0", "range_end=1", "",
	}, "\n")
	scripts := withWALRead(t, out, nil)
	plan := measurePlan(t, timeTarget(rangeT0.Add(90*time.Second)), "")
	live := liveAt("0/44000100", 3, "/var/lib/postgresql/16/main")
	got := measureTransactionLoss(context.Background(), pgInstance{local: true}, plan, live)
	if got.Status != TxLossMeasured || *got.Commits != 2 || got.AtLeast {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Basis, "up to the running instance's own position") {
		t.Errorf("basis = %q", got.Basis)
	}
	if s := (*scripts)[0]; !strings.Contains(s, "DD='/var/lib/postgresql/16/main'") || !strings.Contains(s, `-p "$DD/pg_wal" -r Transaction -e 0/44000100`) {
		t.Errorf("script:\n%s", s)
	}
	// An instance whose data directory is a path a shell could not take safely is not read.
	bad := liveAt("0/44000100", 3, "/data; id")
	if got := measureTransactionLoss(context.Background(), pgInstance{local: true}, plan, bad); got.Status != TxLossUnavailable || !strings.Contains(got.Reason, "not a path this can pass to a shell") {
		t.Errorf("%+v", got)
	}
}

// ── through the real plan ─────────────────────────────────────────────────────

// planWithWALRead plans the live fixture's restore with the WAL read answered by
// answer, which is given the script and says what the instance printed.
func planWithWALRead(t *testing.T, answer func(script string) (string, error)) PITRPlan {
	t.Helper()
	withLedger(t, 10*time.Minute, nil)
	withEstimate(t, restoresThatTook(40, 45, 50), nil)
	budgetObjectives(t, "", "warn")
	_, _ = liveEnv(t)
	old := walReadFn
	walReadFn = func(_ context.Context, _ pgInstance, script string) (string, error) { return answer(script) }
	t.Cleanup(func() { walReadFn = old })
	plan, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || plan.Status != PITRFeasible || plan.DataLossEstimate == nil {
		t.Fatalf("%+v, %v", plan, err)
	}
	return plan
}

// printsACommitPerRange answers a script as an instance would: one commit, long
// after any target, in each range the script reads.
func printsACommitPerRange(script string) (string, error) {
	var b strings.Builder
	for i := 0; i < strings.Count(script, `echo "range_begin=`); i++ {
		b.WriteString("range_begin=" + itoa(i) + "\n" +
			commitLine(10+i, "0/50000010", "COMMIT 2099-01-01 00:00:00 UTC") + "\n\nrange_rc=0\nrange_end=" + itoa(i) + "\n")
	}
	return b.String(), nil
}

// The plan carries the count: in the estimate, in the sentence the person approving
// reads, and no longer among what is "unmeasured".
func TestPlan_CarriesTheTransactionCount(t *testing.T) {
	plan := planWithWALRead(t, printsACommitPerRange)
	e := plan.DataLossEstimate
	if e.Transactions == nil || e.Transactions.Status != TxLossMeasured || e.Transactions.Commits == nil || *e.Transactions.Commits < 1 {
		t.Fatalf("transactions = %+v", e.Transactions)
	}
	if !strings.Contains(e.Summary, "committed after the target would be discarded (counted from the WAL)") || strings.Contains(e.Summary, "Transactions are not counted") {
		t.Errorf("summary = %q", e.Summary)
	}
	for _, u := range e.Unmeasured {
		if strings.HasPrefix(u, "the number of transactions") {
			t.Errorf("still listed as unmeasured: %q", u)
		}
	}
	raw, _ := json.Marshal(plan)
	if !strings.Contains(string(raw), `"transactions":{"status":"measured","commits":`) {
		t.Errorf("plan json lacks the count: %s", raw)
	}
}

// When the WAL cannot be read the plan is still the plan, the estimate says so with
// the reason, and the figures it had stand alone.
func TestPlan_SaysWhyTheTransactionsWereNotCounted(t *testing.T) {
	plan := planWithWALRead(t, func(string) (string, error) { return "waldump_missing=1\n", nil })
	e := plan.DataLossEstimate
	if e.Transactions == nil || e.Transactions.Status != TxLossUnavailable || !strings.Contains(e.Transactions.Reason, "pg_waldump was not found") {
		t.Fatalf("transactions = %+v", e.Transactions)
	}
	if !strings.Contains(e.Summary, "Transactions could not be counted (pg_waldump was not found") {
		t.Errorf("summary = %q", e.Summary)
	}
	// Not counted, so still listed as not counted.
	found := false
	for _, u := range e.Unmeasured {
		found = found || strings.HasPrefix(u, "the number of transactions")
	}
	if !found {
		t.Errorf("a failed count dropped the note: %v", e.Unmeasured)
	}
	// The rest of the estimate is untouched.
	if e.WindowSeconds == nil && e.WALBytes == nil {
		t.Errorf("the figures the estimate had were lost: %+v", e)
	}
}

// The switch stops the read from being made at all.
func TestPlan_CountingCanBeTurnedOff(t *testing.T) {
	for _, v := range []string{"0", "false", "OFF", " no "} {
		t.Setenv(walCountEnv, v)
		called := 0
		plan := planWithWALRead(t, func(string) (string, error) { called++; return "", nil })
		tx := plan.DataLossEstimate.Transactions
		if called != 0 || tx == nil || tx.Status != TxLossUnavailable || !strings.Contains(tx.Reason, "turned off ("+walCountEnv+")") {
			t.Errorf("%q: called %d times, %+v", v, called, tx)
		}
	}
	for _, v := range []string{"", "1", "true", "on", "anything else"} {
		t.Setenv(walCountEnv, v)
		if !transactionCountEnabled() {
			t.Errorf("%q turned it off", v)
		}
	}
}

// What the plan measured is what the audit record keeps: the count, that it was a
// lower bound, and when there is none, why. A plan with no estimate keeps nothing.
func TestPITROption_KeepsTheTransactionCount(t *testing.T) {
	n := 41
	est := &DataLossEstimate{Basis: lossBasisArchive, Transactions: &TransactionLoss{Status: TxLossMeasured, Commits: &n, AtLeast: true}}
	o := pitrOption(est, nil, true)
	if o.LossCommitsStatus != "measured" || o.LossCommits == nil || *o.LossCommits != 41 || !o.LossCommitsAtLeast || o.LossCommitsReason != "" {
		t.Errorf("%+v", o)
	}
	// The record holds its own copy: changing the estimate later cannot change it.
	n = 7
	if *o.LossCommits != 41 {
		t.Errorf("the record shares the estimate's number: %d", *o.LossCommits)
	}
	est.Transactions = &TransactionLoss{Status: TxLossUnavailable, Reason: "pg_waldump was not found on the instance"}
	o = pitrOption(est, nil, false)
	if o.LossCommitsStatus != "unavailable" || o.LossCommits != nil || o.LossCommitsReason != "pg_waldump was not found on the instance" {
		t.Errorf("%+v", o)
	}
	est.Transactions = nil
	if o = pitrOption(est, nil, false); o.LossCommitsStatus != "" || o.LossCommits != nil {
		t.Errorf("an attempt that was not made was recorded: %+v", o)
	}
}

func TestWithTransactions_LeavesAnEstimateAloneWhenThereIsNothingToAdd(t *testing.T) {
	var e *DataLossEstimate
	if e.withTransactions(&TransactionLoss{Status: TxLossMeasured}) != nil {
		t.Error("a nil estimate became something")
	}
	est := &DataLossEstimate{Basis: lossBasisArchive, Unmeasured: []string{"the number of transactions (needs the WAL decoded)", "other"}}
	est.Summary = est.summarize()
	before := est.Summary
	if est.withTransactions(nil) != est || est.Transactions != nil || est.Summary != before || len(est.Unmeasured) != 2 {
		t.Errorf("a nil count changed the estimate: %+v", est)
	}
	// A lower bound is listed as one.
	n := 3
	est.withTransactions(&TransactionLoss{Status: TxLossMeasured, Commits: &n, AtLeast: true})
	if len(est.Unmeasured) != 2 || est.Unmeasured[0] != "other" || !strings.Contains(est.Unmeasured[1], "lower bound") || !strings.Contains(est.Summary, "At least 3 transactions") {
		t.Errorf("%+v", est)
	}
}

func TestWALReadOutputFailure(t *testing.T) {
	rc := func(n int) *int { return &n }
	plan := WALReadPlan{Ranges: []WALReadRange{{Source: WALFromArchive}, {Source: WALFromPgWal}}}
	ok := walRangeResult{RC: rc(0)}
	for name, tc := range map[string]struct {
		out  walReadOutput
		want string
	}{
		"a clean read":            {walReadOutput{Ranges: []walRangeResult{ok, ok}}, ""},
		"no binary":               {walReadOutput{Missing: true}, "pg_waldump was not found"},
		"no temp directory":       {walReadOutput{TmpFailed: true}, "no temporary directory"},
		"at the output cap":       {walReadOutput{Capped: true, Ranges: []walRangeResult{ok, ok}}, "larger than the 48 MiB"},
		"a range missing":         {walReadOutput{Ranges: []walRangeResult{ok}}, "ended after 1 of 2 ranges"},
		"an archive without rc":   {walReadOutput{Ranges: []walRangeResult{{}, ok}}, "did not report how it ended"},
		"pg_wal without rc":       {walReadOutput{Ranges: []walRangeResult{ok, {}}}, "data directory could not be found"},
		"a nonzero exit, no line": {walReadOutput{Ranges: []walRangeResult{ok, {RC: rc(2)}}}, "range 2 (exit 2): no error line"},
	} {
		if got := tc.out.failure(plan); (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// ── measuring ─────────────────────────────────────────────────────────────────

func measurePlan(t *testing.T, tgt RecoveryTarget, norm string) PITRPlan {
	t.Helper()
	return PITRPlan{Stanza: "main", Target: tgt, NormalizedTarget: norm, WALContinuity: archiveOf(0x40, 0x42, 0, 3, 3)}
}

func withWALRead(t *testing.T, out string, err error) *[]string {
	t.Helper()
	old, oldClock := walReadFn, walReadClock
	var scripts []string
	walReadFn = func(_ context.Context, _ pgInstance, script string) (string, error) {
		scripts = append(scripts, script)
		return out, err
	}
	at := time.Unix(1000, 0)
	walReadClock = func() time.Time { at = at.Add(1500 * time.Millisecond); return at }
	t.Cleanup(func() { walReadFn, walReadClock = old, oldClock })
	return &scripts
}

func TestMeasureTransactionLoss_CountsFromWhatTheInstancePrinted(t *testing.T) {
	out := strings.Join([]string{
		"waldump_version=pg_waldump (PostgreSQL) 16.15",
		"range_begin=0",
		commitLine(1, "0/42000010", "COMMIT 2026-11-10 12:00:30 UTC"),
		commitLine(2, "0/42000020", "COMMIT 2026-11-10 12:01:40 UTC"),
		commitLine(3, "0/42000030", "COMMIT 2026-11-10 12:01:50 UTC"),
		"range_rc=0", "range_end=0", "",
	}, "\n")
	scripts := withWALRead(t, out, nil)
	// Target 12:01:30 (a segment arrived at 12:02:00 is the first at or after it).
	plan := measurePlan(t, timeTarget(rangeT0.Add(90*time.Second)), "")
	got := measureTransactionLoss(context.Background(), pgInstance{local: true}, plan, nil)
	if got == nil || got.Status != TxLossMeasured || *got.Commits != 2 || got.FirstLSN != "0/42000020" || !got.AtLeast {
		t.Fatalf("%+v", got)
	}
	// Which pg_waldump decoded it is kept with the count.
	if got.Decoder != "pg_waldump (PostgreSQL) 16.15" {
		t.Errorf("decoder = %q", got.Decoder)
	}
	// A down instance reads to the end of the archive: a lower bound, and it says so.
	if !strings.Contains(got.Basis, "WAL the instance had not archived is not counted") {
		t.Errorf("basis = %q", got.Basis)
	}
	if got.ReadMillis != 1500 || got.SegmentsRead != 1 {
		t.Errorf("read %d ms over %d segments", got.ReadMillis, got.SegmentsRead)
	}
	if len(*scripts) != 1 || !strings.Contains((*scripts)[0], "pgbackrest --stanza=main archive-get") {
		t.Errorf("scripts = %v", *scripts)
	}
}

func TestMeasureTransactionLoss_LSNTargetUsesTheFirstRecord(t *testing.T) {
	out := strings.Join([]string{
		"first_record=rmgr: Transaction len (rec/tot): 34/34, tx: 5, lsn: 0/42000A30, prev 0/42000A28, desc: COMMIT 2026-11-10 12:00:30 UTC",
		"range_begin=0",
		commitLine(5, "0/42000A30", "COMMIT 2026-11-10 12:00:30 UTC"),
		commitLine(6, "0/42000B00", "COMMIT 2026-11-10 12:00:31 UTC"),
		"range_rc=0", "range_end=0", "",
	}, "\n")
	withWALRead(t, out, nil)
	got := measureTransactionLoss(context.Background(), pgInstance{local: true}, measurePlan(t, RecoveryTarget{Type: TargetLSN}, "0/42000A28"), nil)
	// The first record at or after the target is a commit (0/42000A30): it is kept.
	if got.Status != TxLossMeasured || *got.Commits != 1 || got.FirstLSN != "0/42000B00" {
		t.Errorf("%+v", got)
	}
}

// What cannot be read is said, with the reason, and is never "nothing lost".
func TestMeasureTransactionLoss_WhatCannotBeCounted(t *testing.T) {
	good := measurePlan(t, timeTarget(rangeT0), "")
	for name, tc := range map[string]struct {
		plan PITRPlan
		out  string
		err  error
		want string
	}{
		"no plan to read":        {PITRPlan{Target: timeTarget(rangeT0)}, "", nil, "segment size is not known"},
		"a stanza it cannot use": {PITRPlan{Stanza: "a b", Target: good.Target, WALContinuity: good.WALContinuity}, "", nil, "not a name this can pass"},
		"the instance failed":    {good, "", errors.New("exec failed"), "reading the WAL on the instance failed after 1.5s: exec failed"},
		"no binary":              {good, "waldump_missing=1\n", nil, "pg_waldump was not found"},
		"a fetch failure":        {good, "range_begin=0\nfetch_failed=000000030000000000000040\nrange_end=0\n", nil, "would not give up segment"},
		"a cut-short read":       {good, "range_begin=0\nrange_end=0\n", nil, "may have been cut short"},
	} {
		withWALRead(t, tc.out, tc.err)
		got := measureTransactionLoss(context.Background(), pgInstance{local: true}, tc.plan, nil)
		if got == nil || got.Status != TxLossUnavailable || got.Commits != nil || !strings.Contains(got.Reason, tc.want) {
			t.Errorf("%s: %+v, want a reason containing %q", name, got, tc.want)
		}
	}
}
