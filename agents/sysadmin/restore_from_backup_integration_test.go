//go:build integration

// Integration test for restore_from_backup against the real pgBackRest test
// container — not a mocked CommandRunner. restore_from_backup_test.go's 9
// unit tests all verify dispatch logic against a fake command runner; none
// of them prove the actual shell sequence (clear a stale postmaster.pid via
// pgBackRest's own pg1-path config, then `pgbackrest --delta restore`) still
// works against a real pgBackRest install after a real crash. That exact
// sequence was verified live by hand once (2026-10-06, see docs/BACKUP.md
// §3) before this tool was written — this test turns that one-off manual
// verification into a permanent regression check, the same principle
// pgbackrest_ssh_integration_test.go already established for the SSH
// dispatch path ("rather than relying on this live run being repeated by
// hand").
//
// Requires the pgBackRest overlay (make integration already brings this up):
//
//	docker compose -f testing/docker/docker-compose.yaml \
//	  -f testing/docker/docker-compose.pgbackrest.yaml up -d --wait
//	./testing/docker/setup-pgbackrest-backup.sh
//
// Run with:
//
//	go test -tags integration -timeout 600s ./agents/sysadmin/... -run "RestoreFromBackup|WALCheck"
package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/infra"
	"helpdesk/testing/testutil"
)

const restoreIntegrationContainer = "helpdesk-test-pg-pgbackrest"

// skipIfNoPgBackRestDocker skips the test when the real pgBackRest container
// isn't reachable on its pinned Postgres port.
func skipIfNoPgBackRestDocker(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", "localhost:15435", 2*time.Second)
	if err != nil {
		t.Skip("pgBackRest container not reachable on :15435 — run: " +
			"docker compose -f testing/docker/docker-compose.yaml -f testing/docker/docker-compose.pgbackrest.yaml up -d --wait " +
			"&& ./testing/docker/setup-pgbackrest-backup.sh")
	}
	conn.Close()
}

// restoreIntegrationTeardown independently repairs the container regardless
// of test outcome, mirroring every fault's own teardown discipline — a
// failed restore attempt here must not leave the shared container broken
// for whatever test runs next (e.g. TestIntegration_GetPgBackRestStatus_OverRealSSH,
// which expects a healthy container). Same verified sequence as
// db-pgdata-corrupted's own fault teardown in testing/catalog/failures.yaml.
func restoreIntegrationTeardown(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	script := `
PGDATA=$(grep "^pg1-path=" /etc/pgbackrest.conf | cut -d= -f2)
rm -f "$PGDATA/postmaster.pid"
pgbackrest --stanza=main --delta restore
`
	if _, err := testutil.DockerExec(ctx, restoreIntegrationContainer, "su", "postgres", "-c", script); err != nil {
		t.Logf("teardown: restore repair failed (may be a no-op if already healthy): %v", err)
	}
	if err := exec.CommandContext(ctx, "docker", "restart", restoreIntegrationContainer).Run(); err != nil {
		t.Logf("teardown: docker restart failed: %v", err)
	}
	// Empirically, recovery (WAL replay + "ready to accept connections")
	// completes in ~1-2s after the container reports started; this margin
	// is generous rather than tight, since a slow teardown only costs test
	// time, not correctness.
	time.Sleep(8 * time.Second)
}

func TestIntegration_RestoreFromBackup_RealCrashAndRestore(t *testing.T) {
	skipIfNoPgBackRestDocker(t)

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := infra.Load(filepath.Join(repoRoot, "testing", "testing.infra.json"))
	if err != nil {
		t.Fatalf("loading infra config: %v", err)
	}
	oldInfra := infraConfig
	infraConfig = cfg
	t.Cleanup(func() { infraConfig = oldInfra })
	t.Cleanup(func() { restoreIntegrationTeardown(t) })

	ctx := context.Background()

	// Precondition: confirm the fixture starts in the expected healthy
	// baseline (a real backup already taken) — fail clearly here rather
	// than confusingly deeper in the test if the fixture wasn't set up via
	// setup-pgbackrest-backup.sh first.
	before, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-db"})
	if err != nil {
		t.Fatalf("precondition: getPgBackRestStatusImpl failed: %v", err)
	}
	if before.StatusCode != 0 || before.LastBackupTime == "" {
		t.Fatalf("precondition: fixture is not in the expected healthy baseline (status_code=%d, last_backup_time=%q) — "+
			"run ./testing/docker/setup-pgbackrest-backup.sh first", before.StatusCode, before.LastBackupTime)
	}

	// Corrupt: delete pg_control (same real failure signature
	// db-pgdata-corrupted injects), then restart the container so postgres
	// actually attempts startup and crashes — a bare delete doesn't crash an
	// already-running backend, confirmed live during development.
	if _, err := testutil.DockerExec(ctx, restoreIntegrationContainer,
		"rm", "-f", "/var/lib/postgresql/data/global/pg_control"); err != nil {
		t.Fatalf("corrupting pg_control: %v", err)
	}
	if err := exec.CommandContext(ctx, "docker", "restart", restoreIntegrationContainer).Run(); err != nil {
		t.Fatalf("docker restart (to trigger the crash): %v", err)
	}
	// Empirically, the crash (postgres fails to start, start-with-sshd.sh's
	// keep-alive loop takes over) completes within ~2-3s of the container
	// restarting.
	time.Sleep(5 * time.Second)

	// The real code under test: restoreFromBackupImpl, not a mocked
	// CommandRunner. Must refuse gracefully if postgres is somehow still
	// reachable (it shouldn't be, given the crash above) and otherwise
	// perform the actual pgbackrest restore against the real container.
	restored, err := restoreFromBackupImpl(ctx, RestoreFromBackupArgs{Target: "pgbackrest-db", Stanza: "main"})
	if err != nil {
		t.Fatalf("restoreFromBackupImpl failed against a real crashed container: %v", err)
	}
	if p := restored.Prediction; p == nil || p.RecoverToWAL != before.WALContinuity.ArchiveEndWAL ||
		p.ExpectedTimeline != before.WALContinuity.LatestTimeline+1 {
		t.Errorf("restore prediction = %+v, want recover_to_wal %s and timeline %d from the pre-restore check",
			restored.Prediction, before.WALContinuity.ArchiveEndWAL, before.WALContinuity.LatestTimeline+1)
	}

	// Bring postgres back up against the now-repaired data directory —
	// restore_from_backup deliberately doesn't do this itself (that's
	// restart_container's job in the real remediation playbook).
	mark := time.Now()
	if err := exec.CommandContext(ctx, "docker", "restart", restoreIntegrationContainer).Run(); err != nil {
		t.Fatalf("docker restart (post-restore): %v", err)
	}
	log := waitForRecoveryLog(t, mark, "database system is ready to accept connections")

	// The WAL check said where recovery would get to before the restore ran;
	// PostgreSQL's own log says where it actually got to. They must agree.
	wal := before.WALContinuity
	if got := lastRestoredSegment(log); got != wal.ArchiveEndWAL {
		t.Errorf("PostgreSQL's last restored segment = %q, the WAL check predicted %s", got, wal.ArchiveEndWAL)
	}
	if want := fmt.Sprintf("selected new timeline ID: %d", wal.LatestTimeline+1); !strings.Contains(log, want) {
		t.Errorf("recovery log has no %q: the WAL check's latest timeline (%d) isn't the one PostgreSQL recovered along", want, wal.LatestTimeline)
	}

	// verify_restore_recovery makes the same comparison from the real
	// container's own log, the way the remediation playbook calls it.
	verified, err := verifyRestoreRecoveryImpl(ctx, VerifyRestoreRecoveryArgs{Target: "pgbackrest-db"})
	if err != nil {
		t.Fatalf("verifyRestoreRecoveryImpl: %v", err)
	}
	if verified.Status != RestoreVerified || verified.ReachedWAL != wal.ArchiveEndWAL ||
		verified.NewTimeline != wal.LatestTimeline+1 || verified.RedoDoneLSN == "" {
		t.Errorf("verify_restore_recovery = %+v, want verified at %s on timeline %d with a redo-done LSN",
			verified, wal.ArchiveEndWAL, wal.LatestTimeline+1)
	}

	// Verify via a real connection, not just "the tool returned no error" —
	// matching this project's own "verified, not just claimed" discipline.
	connStr := "host=localhost port=15435 dbname=testdb user=postgres password=testpass"
	if err := testutil.RunSQLString(ctx, connStr, "SELECT 1"); err != nil {
		t.Fatalf("real psql connection failed after restore+restart: %v", err)
	}
}

// fixturePSQL runs sql in the fixture's testdb as postgres and returns its
// unaligned output.
func fixturePSQL(t *testing.T, sql string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", "-u", "postgres", restoreIntegrationContainer,
		"psql", "-d", "testdb", "-Atc", sql).CombinedOutput()
	if err != nil {
		t.Fatalf("psql %q: %v: %s", sql, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestIntegration_RestoreFromBackup_KeepsUnarchivedWAL is the item 13 canary
// test (run by hand 2026-10-19, before the fix: both rows lost, pg_wal
// emptied by --delta restore) made permanent. With archiving broken, row A
// is committed into a segment that completes and waits as .ready, row B into
// the open segment; neither reaches the archive. After the crash and
// restore_from_backup, both rows must be back, verify_restore_recovery must
// confirm recovery replayed the tail as predicted, and the archive must be
// continuous onto the new timeline: the second hazard the canary run found.
func TestIntegration_RestoreFromBackup_KeepsUnarchivedWAL(t *testing.T) {
	skipIfNoPgBackRestDocker(t)

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := infra.Load(filepath.Join(repoRoot, "testing", "testing.infra.json"))
	if err != nil {
		t.Fatalf("loading infra config: %v", err)
	}
	oldInfra := infraConfig
	infraConfig = cfg
	t.Cleanup(func() { infraConfig = oldInfra })
	t.Cleanup(func() { restoreIntegrationTeardown(t) })
	t.Cleanup(func() {
		// Runs first: put archiving back and remove the test's leftovers if
		// the server is up (if it isn't, the teardown restore brings back
		// the backup's postgresql.auto.conf). SET, not RESET: the fixture's
		// archive_command lives in postgresql.auto.conf itself, so RESET
		// would turn archiving off (found running this test).
		exec.Command("docker", "exec", "-u", "postgres", restoreIntegrationContainer, "psql", "-d", "testdb", "-c",
			"ALTER SYSTEM SET archive_command = 'pgbackrest --stanza=main archive-push %p'", "-c", "SELECT pg_reload_conf()",
			"-c", "DROP TABLE IF EXISTS canary_wal_tail").Run() //nolint:errcheck
		exec.Command("docker", "exec", "-u", "postgres", restoreIntegrationContainer, "sh", "-c",
			"rm -rf /var/lib/postgresql/pg_wal.saved-*").Run() //nolint:errcheck
	})
	ctx := context.Background()

	// A fresh backup, so the archive end and the restored
	// postgresql.auto.conf (archiving on) are this test's own.
	if out, err := exec.Command("docker", "exec", "-u", "postgres", restoreIntegrationContainer,
		"pgbackrest", "--stanza=main", "--type=full", "backup").CombinedOutput(); err != nil {
		t.Fatalf("fresh backup: %v: %s", err, out)
	}
	fixturePSQL(t, "ALTER SYSTEM SET archive_command = '/bin/false'")
	fixturePSQL(t, "SELECT pg_reload_conf()")
	fixturePSQL(t, "DROP TABLE IF EXISTS canary_wal_tail")
	fixturePSQL(t, "CREATE TABLE canary_wal_tail (id int PRIMARY KEY, segment text)")
	fixturePSQL(t, "INSERT INTO canary_wal_tail VALUES (1, 'A: completed, .ready, never archived')")
	fixturePSQL(t, "SELECT pg_switch_wal()")
	fixturePSQL(t, "INSERT INTO canary_wal_tail VALUES (2, 'B: open segment')")

	before, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-db"})
	if err != nil || before.WALContinuity == nil || before.WALContinuity.Status != WALContinuous {
		t.Fatalf("precondition: WAL check before the crash = %+v, %v", before.WALContinuity, err)
	}

	if _, err := testutil.DockerExec(ctx, restoreIntegrationContainer,
		"rm", "-f", "/var/lib/postgresql/data/global/pg_control"); err != nil {
		t.Fatalf("corrupting pg_control: %v", err)
	}
	if err := exec.CommandContext(ctx, "docker", "restart", restoreIntegrationContainer).Run(); err != nil {
		t.Fatalf("docker restart (to trigger the crash): %v", err)
	}
	time.Sleep(5 * time.Second)

	restored, err := restoreFromBackupImpl(ctx, RestoreFromBackupArgs{Target: "pgbackrest-db", Stanza: "main"})
	if err != nil {
		t.Fatalf("restoreFromBackupImpl: %v", err)
	}
	tail := restored.PgWalTail
	if tail == nil || tail.SavedTo == "" || tail.ReplayThroughWAL == "" || len(tail.PushedToArchive) == 0 {
		t.Fatalf("pg_wal_tail = %+v, want a saved dir, WAL to replay past the archive end %s, and row A's segment pushed to the archive",
			tail, before.WALContinuity.ArchiveEndWAL)
	}
	if p := restored.Prediction; p == nil || p.ReplayThroughWAL != tail.ReplayThroughWAL {
		t.Errorf("prediction = %+v, want replay_through_wal %s", restored.Prediction, tail.ReplayThroughWAL)
	}

	mark := time.Now()
	if err := exec.CommandContext(ctx, "docker", "restart", restoreIntegrationContainer).Run(); err != nil {
		t.Fatalf("docker restart (post-restore): %v", err)
	}
	waitForRecoveryLog(t, mark, "database system is ready to accept connections")

	if got := fixturePSQL(t, "SELECT count(*) FROM canary_wal_tail"); got != "2" {
		t.Errorf("canary rows after the restore = %s, want 2: commits that were only in pg_wal were lost", got)
	}
	verified, err := verifyRestoreRecoveryImpl(ctx, VerifyRestoreRecoveryArgs{Target: "pgbackrest-db"})
	if err != nil {
		t.Fatalf("verifyRestoreRecoveryImpl: %v", err)
	}
	if verified.Status != RestoreVerified || verified.ReplayedThroughWAL != tail.ReplayThroughWAL {
		t.Errorf("verify_restore_recovery = %+v, want verified, replayed through %s", verified, tail.ReplayThroughWAL)
	}

	// restore_from_backup pushed row A's segment before startup, and
	// PostgreSQL archives the open one as .partial when it promotes. Without
	// both, the new timeline would depend on WAL only this host had.
	deadline := time.Now().Add(60 * time.Second)
	for {
		after, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-db"})
		if err == nil && after.WALContinuity != nil && after.WALContinuity.Status == WALContinuous &&
			after.WALContinuity.LatestTimeline == before.WALContinuity.LatestTimeline+1 {
			break
		}
		if time.Now().After(deadline) {
			var wal *WALContinuity
			if err == nil {
				wal = after.WALContinuity
			}
			t.Fatalf("archive not continuous onto the new timeline 60s after startup: %+v, %v (pushed: %v)", wal, err, tail.PushedToArchive)
		}
		time.Sleep(2 * time.Second)
	}
}

// TestIntegration_RestoreFromBackup_RefusesWhenServerIsUp proves the safety
// refusal against the real pg_isready binary, not a mocked one — the
// unit-level equivalent (TestRestoreFromBackupTool_RefusesWhenServerIsUp)
// only proves the Go logic around a fake exit code.
func TestIntegration_RestoreFromBackup_RefusesWhenServerIsUp(t *testing.T) {
	skipIfNoPgBackRestDocker(t)

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := infra.Load(filepath.Join(repoRoot, "testing", "testing.infra.json"))
	if err != nil {
		t.Fatalf("loading infra config: %v", err)
	}
	oldInfra := infraConfig
	infraConfig = cfg
	t.Cleanup(func() { infraConfig = oldInfra })

	// No corruption, no crash — the container should be healthy and
	// postgres genuinely reachable at this point (make integration's own
	// setup-pgbackrest-backup.sh already confirmed it is).
	_, err = restoreFromBackupImpl(context.Background(), RestoreFromBackupArgs{Target: "pgbackrest-db", Stanza: "main"})
	if err == nil {
		t.Fatal("restoreFromBackupImpl succeeded against a live instance — want a refusal")
	}
}

// TestIntegration_RestoreFromBackup_RefusesOnRealWALGap is the v0.32
// WAL-continuity check end to end, against the real repo: the latest
// backup's own start segment is moved out of the archive, so PostgreSQL
// could never reach consistency from that backup. get_pgbackrest_status must
// report the chain broken, and after a real crash restore_from_backup must
// refuse without touching PGDATA (a canary file placed in PGDATA survives).
// The segment is moved back and the fixture repaired afterwards, whatever
// the outcome.
func TestIntegration_RestoreFromBackup_RefusesOnRealWALGap(t *testing.T) {
	skipIfNoPgBackRestDocker(t)

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := infra.Load(filepath.Join(repoRoot, "testing", "testing.infra.json"))
	if err != nil {
		t.Fatalf("loading infra config: %v", err)
	}
	oldInfra := infraConfig
	infraConfig = cfg
	t.Cleanup(func() { infraConfig = oldInfra })
	// Registered first so it runs last, after the segment is back.
	t.Cleanup(func() { restoreIntegrationTeardown(t) })

	ctx := context.Background()
	before, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-db"})
	if err != nil {
		t.Fatalf("precondition: getPgBackRestStatusImpl failed: %v", err)
	}
	if before.WALContinuity == nil || before.WALContinuity.Status != WALContinuous {
		t.Fatalf("precondition: WAL continuity = %+v, want continuous on the healthy fixture", before.WALContinuity)
	}
	if before.WALContinuity.SegmentSizeSource != SegmentSizeFromWALHeader {
		t.Errorf("SegmentSizeSource = %q, want the size read from a real segment header", before.WALContinuity.SegmentSizeSource)
	}
	seg := before.WALContinuity.BackupStartWAL
	archiveDir := "/var/lib/pgbackrest/archive/main/" + before.WALContinuity.ArchiveID + "/" + seg[:16]

	// Move the backup's start segment aside (same directory, a name the WAL
	// parser ignores), and always move it back.
	hide := `set -e; f=$(ls ` + archiveDir + `/` + seg + `-*); mv "$f" "$f.hidden-by-test"`
	if _, err := testutil.DockerExec(ctx, restoreIntegrationContainer, "sh", "-c", hide); err != nil {
		t.Fatalf("hiding WAL segment %s: %v", seg, err)
	}
	t.Cleanup(func() {
		unhide := `for f in ` + archiveDir + `/*.hidden-by-test; do mv "$f" "${f%.hidden-by-test}"; done`
		if _, err := testutil.DockerExec(context.Background(), restoreIntegrationContainer, "sh", "-c", unhide); err != nil {
			t.Errorf("cleanup: restoring WAL segment %s: %v", seg, err)
		}
	})

	after, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-db"})
	if err != nil {
		t.Fatalf("getPgBackRestStatusImpl with the segment hidden: %v", err)
	}
	wal := after.WALContinuity
	if wal == nil || wal.Status != WALBroken || wal.ConsistencyReachable {
		t.Fatalf("WAL continuity = %+v, want broken with consistency unreachable", wal)
	}
	if len(wal.Gaps) == 0 || wal.Gaps[0].FirstMissing != seg {
		t.Errorf("gaps = %+v, want the first gap at %s", wal.Gaps, seg)
	}

	// Crash the instance the same way the real restore test does, and drop a
	// canary into PGDATA: a refused restore must leave it in place.
	if _, err := testutil.DockerExec(ctx, restoreIntegrationContainer, "sh", "-c",
		"rm -f /var/lib/postgresql/data/global/pg_control && touch /var/lib/postgresql/data/walcheck-canary"); err != nil {
		t.Fatalf("corrupting pg_control: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testutil.DockerExec(context.Background(), restoreIntegrationContainer, "rm", "-f", "/var/lib/postgresql/data/walcheck-canary")
	})
	if err := exec.CommandContext(ctx, "docker", "restart", restoreIntegrationContainer).Run(); err != nil {
		t.Fatalf("docker restart (to trigger the crash): %v", err)
	}
	time.Sleep(5 * time.Second)

	_, err = restoreFromBackupImpl(ctx, RestoreFromBackupArgs{Target: "pgbackrest-db", Stanza: "main"})
	if err == nil {
		t.Fatal("restoreFromBackupImpl succeeded with the backup's own WAL missing, want a refusal")
	}
	if !strings.Contains(err.Error(), "WAL continuity is broken") {
		t.Errorf("error = %v, want a WAL-continuity refusal", err)
	}
	if _, err := testutil.DockerExec(ctx, restoreIntegrationContainer, "test", "-f", "/var/lib/postgresql/data/walcheck-canary"); err != nil {
		t.Errorf("canary file is gone from PGDATA: the refused restore touched the data directory")
	}
}

var restoredSegmentRe = regexp.MustCompile(`restored log file "([0-9A-F]{24})" from archive`)

// lastRestoredSegment returns the last WAL segment (not .history) PostgreSQL
// says it restored from the archive.
func lastRestoredSegment(log string) string {
	m := restoredSegmentRe.FindAllStringSubmatch(log, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// waitForRecoveryLog returns the container's log since mark once it contains
// one of the given lines, failing the test after 60s.
func waitForRecoveryLog(t *testing.T, mark time.Time, until ...string) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		out, _ := exec.Command("docker", "logs", "--since", mark.Add(-time.Second).UTC().Format(time.RFC3339), restoreIntegrationContainer).CombinedOutput()
		log := string(out)
		for _, u := range until {
			if strings.Contains(log, u) {
				return log
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q in the recovery log after 60s:\n%s", until, log)
		}
		time.Sleep(time.Second)
	}
}

// TestIntegration_WALCheck_PredictionMatchesRealRecovery checks the WAL
// check against PostgreSQL itself. For each scenario it hides one archived
// segment, asks the check where recovery would stop, then runs a real
// pgBackRest restore and reads PostgreSQL's log for the last segment it
// actually restored. The restore is run directly, since restore_from_backup
// refuses a broken chain.
//
// Each restore sets a recovery target past the end of any WAL, so recovery
// ends with "recovery ended before configured recovery target was reached"
// instead of promoting: no new timeline or history file reaches the shared
// archive, and the teardown restore leaves the fixture as it found it.
func TestIntegration_WALCheck_PredictionMatchesRealRecovery(t *testing.T) {
	skipIfNoPgBackRestDocker(t)

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := infra.Load(filepath.Join(repoRoot, "testing", "testing.infra.json"))
	if err != nil {
		t.Fatalf("loading infra config: %v", err)
	}
	oldInfra := infraConfig
	infraConfig = cfg
	t.Cleanup(func() { infraConfig = oldInfra })

	ctx := context.Background()

	// Each scenario plans from the archive as it is when it starts: the
	// previous scenario's teardown restore adds a timeline.
	type plan struct {
		hide       string // shell glob, relative to the archive id directory, of the files to hide
		wantStatus string
		wantEndSeg uint64 // segment number the check should say recovery stops at
	}
	scenarios := []struct {
		name string
		plan func(base *WALContinuity, startTLI uint32, startSeg, stopSeg, endSeg uint64) plan
	}{
		// Not needed: WAL from before the backup started.
		{"segment before the backup", func(base *WALContinuity, startTLI uint32, startSeg, _, endSeg uint64) plan {
			return plan{"*/" + segmentName(startTLI, startSeg-1, base.SegmentSizeBytes) + "-*", WALContinuous, endSeg}
		}},
		// Hiding the newest segment only shortens the archive when it's on
		// the newest timeline. On an older timeline that later forked,
		// history shows WAL past it, so it's a real loss
		// (TestEvaluateWAL_TailOfOlderTimelineMissing_Broken).
		{"newest segment", func(base *WALContinuity, _ uint32, _, _, endSeg uint64) plan {
			status := WALContinuous
			if tli, _, _, _ := parseSegmentName(base.ArchiveEndWAL); tli != base.LatestTimeline {
				status = WALBroken
			}
			return plan{"*/" + base.ArchiveEndWAL + "-*", status, endSeg - 1}
		}},
		// Every timeline's copy of the segment after the backup: recovery
		// stops at the backup.
		{"segment after the backup", func(base *WALContinuity, _ uint32, _, stopSeg, _ uint64) plan {
			return plan{"*/????????" + segmentName(0, stopSeg+1, base.SegmentSizeBytes)[8:] + "-*", WALBroken, stopSeg}
		}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			// The scenarios need two whole archived segments after the
			// backup's own. A fresh fixture (make integration) has none, so
			// this writes some.
			base := walAfterBackup(t, ctx, 2)
			size := base.SegmentSizeBytes
			startTLI, _, _, _ := parseSegmentName(base.BackupStartWAL)
			startSeg := segnoOf(t, base.BackupStartWAL, size)
			if startSeg == 0 {
				t.Fatalf("precondition: backup starts at the very first segment %s", base.BackupStartWAL)
			}
			sc := scenario.plan(base, startTLI, startSeg, segnoOf(t, base.BackupStopWAL, size), segnoOf(t, base.ArchiveEndWAL, size))
			archiveRoot := "/var/lib/pgbackrest/archive/main/" + base.ArchiveID

			if _, err := testutil.DockerExec(ctx, restoreIntegrationContainer, "sh", "-c", `cd `+archiveRoot+` && ls `+sc.hide); err != nil {
				t.Skipf("no archived file matches %s (expired by retention?)", sc.hide)
			}
			t.Cleanup(func() { restoreIntegrationTeardown(t) })
			hide := `set -e; cd ` + archiveRoot + `; for f in ` + sc.hide + `; do test -e "$f"; mv "$f" "$f.hidden-by-test"; done`
			if out, err := testutil.DockerExec(ctx, restoreIntegrationContainer, "sh", "-c", hide); err != nil {
				t.Fatalf("hiding %s: %v: %s", sc.hide, err, out)
			}
			t.Cleanup(func() {
				unhide := `find ` + archiveRoot + ` -name '*.hidden-by-test' | while read -r f; do mv "$f" "${f%.hidden-by-test}"; done`
				if _, err := testutil.DockerExec(context.Background(), restoreIntegrationContainer, "sh", "-c", unhide); err != nil {
					t.Errorf("cleanup: restoring %s: %v", sc.hide, err)
				}
			})

			status, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-db"})
			if err != nil {
				t.Fatalf("getPgBackRestStatusImpl: %v", err)
			}
			wal := status.WALContinuity
			if wal == nil || wal.Status != sc.wantStatus || wal.RecoverableToWAL == "" || segnoOf(t, wal.RecoverableToWAL, size) != sc.wantEndSeg {
				t.Fatalf("WAL check = %+v, want %s with recovery reaching segment %X", wal, sc.wantStatus, sc.wantEndSeg)
			}

			// Stop PostgreSQL for good (the container's keep-alive loop keeps
			// it up), then restore and recover for real.
			if _, err := testutil.DockerExec(ctx, restoreIntegrationContainer, "rm", "-f", "/var/lib/postgresql/data/global/pg_control"); err != nil {
				t.Fatalf("corrupting pg_control: %v", err)
			}
			if err := exec.CommandContext(ctx, "docker", "restart", restoreIntegrationContainer).Run(); err != nil {
				t.Fatalf("docker restart (to stop postgres): %v", err)
			}
			time.Sleep(5 * time.Second)
			restore := `PGDATA=$(grep "^pg1-path=" /etc/pgbackrest.conf | cut -d= -f2); rm -f "$PGDATA/postmaster.pid"; ` +
				`pgbackrest --stanza=main --delta --type=lsn --target=FFFFFFFF/0 restore`
			if out, err := testutil.DockerExec(ctx, restoreIntegrationContainer, "su", "postgres", "-c", restore); err != nil {
				t.Fatalf("pgbackrest restore: %v: %s", err, out)
			}
			mark := time.Now()
			if err := exec.CommandContext(ctx, "docker", "restart", restoreIntegrationContainer).Run(); err != nil {
				t.Fatalf("docker restart (to recover): %v", err)
			}
			log := waitForRecoveryLog(t, mark, "recovery ended before configured recovery target was reached",
				"database system is ready to accept connections")
			if strings.Contains(log, "selected new timeline ID") {
				t.Fatalf("recovery promoted instead of stopping at the unreachable target; the archive now has a new timeline:\n%s", log)
			}
			if got := lastRestoredSegment(log); got != wal.RecoverableToWAL {
				t.Errorf("PostgreSQL's last restored segment = %q, the WAL check predicted %s\n%s", got, wal.RecoverableToWAL, log)
			}
		})
	}
}

func segnoOf(t *testing.T, name string, size uint64) uint64 {
	t.Helper()
	_, logID, seg, err := parseSegmentName(name)
	if err != nil {
		t.Fatal(err)
	}
	return logID*segsPerLogID(size) + seg
}

// walAfterBackup returns the fixture's WAL check once at least n whole
// segments past the latest backup's stop segment are archived, writing and
// switching WAL as needed.
func walAfterBackup(t *testing.T, ctx context.Context, n uint64) *WALContinuity {
	t.Helper()
	connStr := "host=localhost port=15435 dbname=testdb user=postgres password=testpass"
	deadline := time.Now().Add(60 * time.Second)
	for {
		status, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-db"})
		if err != nil {
			t.Fatalf("precondition: getPgBackRestStatusImpl failed: %v", err)
		}
		wal := status.WALContinuity
		if wal == nil || wal.Status != WALContinuous {
			t.Fatalf("precondition: WAL continuity = %+v, want continuous on the healthy fixture", wal)
		}
		if segnoOf(t, wal.ArchiveEndWAL, wal.SegmentSizeBytes) >= segnoOf(t, wal.BackupStopWAL, wal.SegmentSizeBytes)+n {
			return wal
		}
		if time.Now().After(deadline) {
			t.Fatalf("precondition: archive still ends at %s, want %d segments past the backup's %s", wal.ArchiveEndWAL, n, wal.BackupStopWAL)
		}
		// pg_switch_wal is a no-op unless WAL was written since the last switch.
		if err := testutil.RunSQLString(ctx, connStr,
			"CREATE TABLE IF NOT EXISTS walcheck_filler (x int); INSERT INTO walcheck_filler SELECT generate_series(1, 1000); SELECT pg_switch_wal();"); err != nil {
			t.Fatalf("generating WAL: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
}
