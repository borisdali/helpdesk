package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"helpdesk/internal/infra"
)

// The fixtures are PostgreSQL's real log lines from the Docker pgBackRest
// fixture, 2026-10-27 (pgBackRest 2.59.3, PostgreSQL 16).
func pitrFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/pitr/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParsePITRLog_Fixtures(t *testing.T) {
	tm := parsePITRLog(pitrFixture(t, "time_stop_before_promote.log"))
	if !tm.Started || tm.StopKind != "before" || tm.StopTime != "2026-10-27 19:37:23.578405+00" || tm.NotReached {
		t.Fatalf("time: %+v", tm)
	}
	lsn := parsePITRLog(pitrFixture(t, "lsn_stop_after_promote.log"))
	if !lsn.Started || lsn.StopKind != "after" || lsn.StopLSN != "0/19000318" {
		t.Fatalf("lsn: %+v", lsn)
	}
	nr := parsePITRLog(pitrFixture(t, "target_not_reached.log"))
	if !nr.Started || !nr.NotReached || nr.StopLine != "" {
		t.Fatalf("not reached: %+v", nr)
	}
	if got := parsePITRLog("LOG: nothing here"); got.Started {
		t.Fatalf("no PITR in log: %+v", got)
	}
}

func TestComparePITRRecovery_Fixtures(t *testing.T) {
	timePred := &RestorePrediction{TargetType: TargetTime, TargetValue: "2026-10-27 19:37:21.437973+00", ExpectedTimeline: 12}
	lsnPred := &RestorePrediction{TargetType: TargetLSN, TargetValue: "0/19000318", ExpectedTimeline: 13}

	run := func(pred *RestorePrediction, fixture string) (string, string) {
		log := pitrFixture(t, fixture)
		return comparePITRRecovery(pred, parseRecoveryLog(log), parsePITRLog(log))
	}

	if st, d := run(timePred, "time_stop_before_promote.log"); st != RestoreVerified {
		t.Fatalf("time: %s: %s", st, d)
	}
	if st, d := run(lsnPred, "lsn_stop_after_promote.log"); st != RestoreVerified {
		t.Fatalf("lsn: %s: %s", st, d)
	}
	st, d := run(&RestorePrediction{TargetType: TargetLSN, TargetValue: "0/FF000000", ExpectedTimeline: 14}, "target_not_reached.log")
	if st != RestoreTargetNotReached || !strings.Contains(d, "earlier target") {
		t.Fatalf("not reached: %s: %s", st, d)
	}

	// "Last restored segment" says nothing about where a targeted recovery
	// stopped: PostgreSQL reads ahead of the stop point and re-reads a segment
	// just before the timeline switch. In the time fixture redo ended in
	// segment ...17 while the parser's last restored segment is ...16, which
	// is why restore-to-latest's comparison can't be used.
	rec := parseRecoveryLog(pitrFixture(t, "time_stop_before_promote.log"))
	if rec.LastRestoredWAL == "0000000B0000000000000017" || rec.RedoDoneLSN != "0/17000028" {
		t.Fatalf("fixture changed: %+v", rec)
	}

	mismatches := []struct {
		name    string
		pred    *RestorePrediction
		fixture string
		want    string
	}{
		{"stop is not after the time target", &RestorePrediction{TargetType: TargetTime, TargetValue: "2026-10-27 19:37:30+00", ExpectedTimeline: 12}, "time_stop_before_promote.log", "earlier than asked"},
		{"wrong lsn", &RestorePrediction{TargetType: TargetLSN, TargetValue: "0/19000400", ExpectedTimeline: 13}, "lsn_stop_after_promote.log", "not the target"},
		{"wrong timeline", &RestorePrediction{TargetType: TargetTime, TargetValue: "2026-10-27 19:37:21+00", ExpectedTimeline: 11}, "time_stop_before_promote.log", "another recovery"},
	}
	for _, m := range mismatches {
		if st, d := run(m.pred, m.fixture); st != RestoreMismatch || !strings.Contains(d, m.want) {
			t.Errorf("%s: %s: %s", m.name, st, d)
		}
	}
	if st, _ := run(&RestorePrediction{TargetType: TargetTime, TargetValue: "2026-10-27 19:37:21+00", ExpectedTimeline: 13}, "time_stop_before_promote.log"); st != RestoreNotFound {
		t.Errorf("a log older than the prediction should be not_found, got %s", st)
	}
}

// An "after commit" stop later than the target went past it.
func TestComparePITRRecovery_AfterCommitPastTarget(t *testing.T) {
	log := "LOG: starting point-in-time recovery to 2026-10-27 10:00:00+00\n" +
		"LOG: recovery stopping after commit of transaction 5, time 2026-10-27 10:00:05+00\n" +
		"LOG: redo done at 0/5000000\nLOG: selected new timeline ID: 2\n"
	pred := &RestorePrediction{TargetType: TargetTime, TargetValue: "2026-10-27 10:00:00+00", ExpectedTimeline: 2}
	if st, d := comparePITRRecovery(pred, parseRecoveryLog(log), parsePITRLog(log)); st != RestoreMismatch || !strings.Contains(d, "went past") {
		t.Fatalf("%s: %s", st, d)
	}
	ok := strings.Replace(log, "10:00:05", "09:59:59", 1)
	if st, d := comparePITRRecovery(pred, parseRecoveryLog(ok), parsePITRLog(ok)); st != RestoreVerified {
		t.Fatalf("%s: %s", st, d)
	}
}

// Promotion with no stop line means the target did not end the recovery.
func TestComparePITRRecovery_PromotedWithoutStop(t *testing.T) {
	log := "LOG: starting point-in-time recovery to 2026-10-27 10:00:00+00\nLOG: redo done at 0/5000000\nLOG: selected new timeline ID: 2\n"
	pred := &RestorePrediction{TargetType: TargetTime, TargetValue: "2026-10-27 10:00:00+00", ExpectedTimeline: 2}
	if st, _ := comparePITRRecovery(pred, parseRecoveryLog(log), parsePITRLog(log)); st != RestoreMismatch {
		t.Fatalf("got %s", st)
	}
}

// verifyRestoreRecoveryImpl end to end for a point-in-time prediction, on the
// systemd layout (journal has only systemd's lines, recovery is in the log
// file), driven by the real captured logs.
func TestVerifyRestoreRecovery_PITR(t *testing.T) {
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"vm": {Name: "vm", ConnectionString: "host=localhost", VMName: "vm-host",
				SystemdUnit: "postgresql@16-main", PgLogDir: "/var/log/postgresql"},
		},
		VMs: map[string]infra.VM{"vm-host": {Name: "vm-host", Address: "localhost"}},
	}
	t.Cleanup(func() {
		infraConfig = nil
		restorePredictions.Lock()
		delete(restorePredictions.byTarget, "vm")
		restorePredictions.Unlock()
	})

	logText := ""
	old := cmdRunner
	cmdRunner = runnerFunc(func(_ context.Context, name string, args []string, _ []string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "journalctl"):
			return "Oct 27 16:23:41 vm systemd[1]: Started postgresql@16-main.service.", nil
		case strings.Contains(joined, "ls -t"):
			return "postgresql-16-main.log\n", nil
		case strings.Contains(joined, "tail"):
			return logText, nil
		}
		return "", nil
	})
	t.Cleanup(func() { cmdRunner = old })

	cases := []struct {
		name    string
		fixture string
		pred    RestorePrediction
		want    string
		stop    bool
	}{
		{"time verified", "time_stop_before_promote.log",
			RestorePrediction{TargetType: TargetTime, TargetValue: "2026-10-27 19:37:21.437973+00", ExpectedTimeline: 12}, RestoreVerified, true},
		{"lsn verified", "lsn_stop_after_promote.log",
			RestorePrediction{TargetType: TargetLSN, TargetValue: "0/19000318", ExpectedTimeline: 13}, RestoreVerified, true},
		{"target not reached, no timeline line in the log", "target_not_reached.log",
			RestorePrediction{TargetType: TargetLSN, TargetValue: "0/FF000000", ExpectedTimeline: 14}, RestoreTargetNotReached, false},
	}
	for _, c := range cases {
		logText = pitrFixture(t, c.fixture)
		saveRestorePrediction("vm", c.pred)
		res, err := verifyRestoreRecoveryImpl(context.Background(), VerifyRestoreRecoveryArgs{Target: "vm"})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if res.Status != c.want || (res.StopLine != "") != c.stop {
			t.Errorf("%s: status=%s stop_line=%q detail=%s", c.name, res.Status, res.StopLine, res.Detail)
		}
	}

	// A prediction without a target keeps using the restore-to-latest
	// comparison, even when the log is a point-in-time one.
	logText = pitrFixture(t, "time_stop_before_promote.log")
	saveRestorePrediction("vm", RestorePrediction{RecoverToWAL: "0000000B0000000000000018", ExpectedTimeline: 12})
	res, _ := verifyRestoreRecoveryImpl(context.Background(), VerifyRestoreRecoveryArgs{Target: "vm"})
	if res.StopLine != "" || res.Status != RestoreMismatch {
		t.Errorf("restore-to-latest prediction took the PITR path or lost its mismatch: %+v", res)
	}
}

func TestComparePITRRecovery_Unverifiable(t *testing.T) {
	timeLog := "LOG: starting point-in-time recovery to X\nLOG: recovery stopping before commit of transaction 5, time 2026-10-27 10:00:05+00\nLOG: redo done at 0/5000000\nLOG: selected new timeline ID: 2\n"
	run := func(pred *RestorePrediction, log string) string {
		st, _ := comparePITRRecovery(pred, parseRecoveryLog(log), parsePITRLog(log))
		return st
	}
	if st := run(&RestorePrediction{TargetType: TargetTime, TargetValue: "not a time", ExpectedTimeline: 2}, timeLog); st != RestoreUnverified {
		t.Errorf("unparseable target: %s", st)
	}
	if st := run(&RestorePrediction{TargetType: "xid", TargetValue: "1", ExpectedTimeline: 2}, timeLog); st != RestoreUnverified {
		t.Errorf("unknown type: %s", st)
	}
	lsnLog := "LOG: starting point-in-time recovery to WAL location (LSN) \"0/5000000\"\nLOG: recovery stopping after WAL location (LSN) \"0/5000000\"\nLOG: redo done at 0/4000000\nLOG: selected new timeline ID: 2\n"
	if st := run(&RestorePrediction{TargetType: TargetLSN, TargetValue: "0/5000000", ExpectedTimeline: 2}, lsnLog); st != RestoreMismatch {
		t.Errorf("redo done before the LSN target: %s", st)
	}
	if st := run(&RestorePrediction{TargetType: TargetLSN, TargetValue: "garbage", ExpectedTimeline: 2}, lsnLog); st != RestoreUnverified {
		t.Errorf("unparseable LSN target: %s", st)
	}
	// Not yet restarted: nothing in the log.
	if st := run(&RestorePrediction{TargetType: TargetTime, TargetValue: "2026-10-27 10:00:00+00", ExpectedTimeline: 2}, "LOG: ready"); st != RestoreNotFound {
		t.Errorf("no recovery in the log: %s", st)
	}
}

// A not-reached FATAL from an earlier attempt must not mask a later
// successful recovery: only the newest point-in-time start counts.
func TestParsePITRLog_NewestAttemptWins(t *testing.T) {
	log := pitrFixture(t, "target_not_reached.log") + pitrFixture(t, "time_stop_before_promote.log")
	pl := parsePITRLog(log)
	if pl.NotReached || pl.StopKind != "before" {
		t.Fatalf("%+v", pl)
	}
}
