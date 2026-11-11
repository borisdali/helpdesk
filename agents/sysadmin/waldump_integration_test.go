//go:build integration

// Integration test for counting discarded transactions from real WAL (Tier B),
// against the real pgBackRest test container (see
// restore_from_backup_integration_test.go for the fixture): real pg_waldump,
// real pgbackrest archive-get, the real script run by docker exec as postgres.
//
// What it writes to the container is a few commits (SELECT txid_current() gives
// a transaction an id and so a commit record) and some filler to move WAL into
// the archive, like the other plan tests; it restores nothing.
//
//	go test -tags integration -timeout 300s ./agents/sysadmin/... -run CountsTransactions
package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"helpdesk/testing/testutil"
)

func TestIntegration_CountsTransactionsFromRealWAL(t *testing.T) {
	ctx := pitrFixtureSetup(t)
	connStr := "host=localhost port=15435 dbname=testdb user=postgres password=testpass"

	// A transaction id is what a commit record is named by in the WAL. Each
	// commit writes a row, as an application's does: a transaction that only takes
	// an id (SELECT txid_current()) writes no WAL of its own, and PostgreSQL commits
	// it asynchronously even with synchronous_commit on, so its record can still be
	// in the WAL buffers when the instance is asked where its WAL ends. A real commit
	// that wrote data is flushed before it returns.
	fixturePSQL(t, "CREATE TABLE IF NOT EXISTS walcount_marker (x int)")
	txid := func() uint64 {
		n, err := strconv.ParseUint(fixturePSQL(t, "WITH w AS (INSERT INTO walcount_marker VALUES (1)) SELECT txid_current()"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return n & 0xFFFFFFFF // the epoch is not in the WAL record
	}
	commits := func(n int) []uint64 {
		var ids []uint64
		for i := 0; i < n; i++ {
			ids = append(ids, txid())
		}
		return ids
	}

	// A fresh segment first, so that the commits before the target and the ones after
	// it share one segment, and the one before it reached the repository before the target.
	fixturePSQL(t, "SELECT pg_switch_wal()")
	time.Sleep(4 * time.Second)

	before := commits(3)
	time.Sleep(1500 * time.Millisecond)
	target := fixturePSQL(t, "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')")
	targetAt, err := time.Parse(time.RFC3339, target)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	after := commits(5)

	// Get that segment into the archive, and the plan feasible.
	var plan PITRPlan
	deadline := time.Now().Add(120 * time.Second)
	for {
		if err := testutil.RunSQLString(ctx, connStr,
			"CREATE TABLE IF NOT EXISTS walcheck_filler (x int); INSERT INTO walcheck_filler SELECT generate_series(1, 1000); SELECT pg_switch_wal();"); err != nil {
			t.Fatalf("generating WAL: %v", err)
		}
		time.Sleep(3 * time.Second)
		plan, err = planPITRImpl(ctx, PlanPITRArgs{Target: "pgbackrest-db", RecoveryTargetType: TargetTime, RecoveryTargetVal: target})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status == PITRFeasible && !plan.ArchiveFlushRequired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("target %s never became feasible: %+v", target, plan)
		}
	}

	// Commits that exist only in the instance's pg_wal: never archived.
	tail := commits(2)

	inst, err := instanceFor("pgbackrest-db")
	if err != nil {
		t.Fatal(err)
	}
	live, err := livePreflight(ctx, inst)
	if err != nil {
		t.Fatalf("asking the running instance about itself: %v", err)
	}

	// read runs the real script and returns the commits recovery would discard.
	var lastSeen map[uint64]WALTxEnd
	read := func(live *LivePreState) (WALReadPlan, map[uint64]WALCommit, walReadOutput) {
		t.Helper()
		rp, why := planWALRead(plan.Target, plan.NormalizedTarget, plan.WALContinuity, live, walReadMaxSegments)
		if why != "" {
			t.Fatalf("no plan to read: %s", why)
		}
		dir := ""
		if live != nil {
			dir = live.DataDirectory
		}
		script, err := walReadScript(plan.Stanza, dir, rp)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		out, err := inst.maintenance(ctx, script, false)
		if err != nil {
			t.Fatalf("the script failed: %v\n%s", err, out)
		}
		t.Logf("read %d segment(s) in %s (serving=%v): ranges %+v", rp.Segments, time.Since(started).Round(time.Millisecond), live != nil, rp.Ranges)
		parsed, err := parseWALReadOutput(out)
		if err != nil {
			t.Fatal(err)
		}
		if why := parsed.failure(rp); why != "" {
			t.Fatalf("the read failed: %s\n%s", why, out)
		}
		var ends []WALTxEnd
		seen := map[uint64]WALTxEnd{}
		for _, r := range parsed.Ranges {
			for _, e := range r.Parsed.Ends {
				ends = append(ends, e)
				if e.TimeUnread {
					t.Errorf("a commit time was not read as UTC (TZ=UTC should make that so): %+v", e)
				}
				seen[e.XID] = e
			}
			if r.Parsed.Unread != 0 {
				t.Errorf("%d record(s) not understood in %+v", r.Parsed.Unread, r.Parsed.EndError)
			}
		}
		lastSeen = seen
		discarded, how, failed := discardedCommits(rp, ends, nil)
		if failed != nil {
			t.Fatalf("the stop was not found: %+v", failed)
		}
		t.Logf("stop: %s; %d commit(s) discarded", how, len(discarded))
		got := map[uint64]WALCommit{}
		for _, c := range discarded {
			got[c.XID] = c
		}
		// What was read includes the commits before the target, so that excluding
		// them below is a decision and not an accident of where the read began.
		for _, id := range before {
			if _, ok := seen[id]; !ok {
				t.Errorf("commit %d (before the target) was never read: the read began too late to test the stop", id)
			}
		}
		return rp, got, parsed
	}

	// The archive alone (as for a down instance): everything archived after the
	// target, nothing before it.
	_, archived, _ := read(nil)
	for _, id := range before {
		if _, ok := archived[id]; ok {
			t.Errorf("commit %d was before the target %s and was counted as discarded", id, target)
		}
	}
	for _, id := range after {
		c, ok := archived[id]
		if !ok {
			t.Errorf("commit %d was after the target %s and was not counted", id, target)
			continue
		}
		if !c.Time.After(targetAt) {
			t.Errorf("commit %d is counted but its time %s is not after the target %s", id, c.Time, targetAt)
		}
	}

	// A serving instance's own pg_wal as well, read up to its position: now the
	// commits that were never archived are counted too.
	rp, serving, _ := read(&live)
	if !rp.Complete() {
		t.Errorf("a serving instance's read should reach its own position: %+v", rp)
	}
	for _, id := range append(append([]uint64(nil), after...), tail...) {
		if _, ok := serving[id]; !ok {
			e, wasRead := lastSeen[id]
			t.Errorf("commit %d was discarded by the restore and was not counted from the instance's own WAL: read=%v (%+v), ranges=%+v, live lsn=%s timeline=%d, archive end=%s, target=%s, plan archive end time=%s",
				id, wasRead, e, rp.Ranges, live.LSN, live.Timeline, plan.WALContinuity.ArchiveEndWAL, target, plan.WALContinuity.ArchiveEndTime)
		}
	}
	for _, id := range before {
		if _, ok := serving[id]; ok {
			t.Errorf("commit %d was before the target and was counted (serving read)", id)
		}
	}

	// The same through the function the plan calls: a count, exact for the serving
	// read and a lower bound for the archive-only one.
	got := measureTransactionLoss(ctx, inst, plan, &live)
	if got.Status != TxLossMeasured || got.Commits == nil || *got.Commits < len(after)+len(tail) || got.AtLeast {
		t.Fatalf("serving: %+v", got)
	}
	if got.ReadMillis <= 0 || got.SegmentsRead < 1 || got.FirstAt == "" || got.FirstLSN == "" {
		t.Errorf("what the read cost and where it began were not recorded: %+v", got)
	}
	down := measureTransactionLoss(ctx, inst, plan, nil)
	if down.Status != TxLossMeasured || !down.AtLeast || *down.Commits < len(after) {
		t.Fatalf("down: %+v", down)
	}
	t.Logf("serving: %s | %d ms over %d segment(s)", got.Basis, got.ReadMillis, got.SegmentsRead)
	t.Logf("sentence: %s", got.Sentence())
	if !strings.Contains(got.Sentence(), "committed after the target would be discarded") {
		t.Errorf("sentence = %q", got.Sentence())
	}
}
