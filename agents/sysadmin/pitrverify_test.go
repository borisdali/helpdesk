package main

import (
	"os"
	"strings"
	"testing"
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
