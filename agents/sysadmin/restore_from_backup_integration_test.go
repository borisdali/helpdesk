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
	if _, err := restoreFromBackupImpl(ctx, RestoreFromBackupArgs{Target: "pgbackrest-db", Stanza: "main"}); err != nil {
		t.Fatalf("restoreFromBackupImpl failed against a real crashed container: %v", err)
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

	// Verify via a real connection, not just "the tool returned no error" —
	// matching this project's own "verified, not just claimed" discipline.
	connStr := "host=localhost port=15435 dbname=testdb user=postgres password=testpass"
	if err := testutil.RunSQLString(ctx, connStr, "SELECT 1"); err != nil {
		t.Fatalf("real psql connection failed after restore+restart: %v", err)
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
	baseline, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-db"})
	if err != nil {
		t.Fatalf("precondition: getPgBackRestStatusImpl failed: %v", err)
	}
	base := baseline.WALContinuity
	if base == nil || base.Status != WALContinuous || base.ArchiveEndWAL == base.BackupStopWAL {
		t.Fatalf("precondition: WAL continuity = %+v, want continuous with WAL after the backup", base)
	}
	// The scenarios below are written against the fixture's shape: the
	// backup's own segment on timeline 1, then the newest timeline holding
	// the last two segments.
	end := base.ArchiveEndWAL
	tli, logID, seg, _ := parseSegmentName(end)
	beforeEnd := fmt.Sprintf("%08X%08X%08X", tli, logID, seg-1)
	if seg == 0 || beforeEnd[:8] != end[:8] {
		t.Fatalf("precondition: unexpected archive end %s", end)
	}
	archiveRoot := "/var/lib/pgbackrest/archive/main/" + base.ArchiveID

	scenarios := []struct {
		name       string
		hide       string // segment to hide
		wantStatus string
		wantEnd    string // where the check should say recovery stops
	}{
		// Not needed: WAL from before the backup started.
		{"segment before the backup", "000000010000000000000001", WALContinuous, end},
		// The newest segment. Its timeline isn't the newest one: history
		// shows it ran past this segment before forking, so this is lost
		// WAL, not the archive simply ending one segment earlier.
		{"newest segment", end, WALBroken, beforeEnd},
		// The segment after the backup's own: recovery stops at the backup.
		{"segment after the backup", beforeEnd, WALBroken, base.BackupStopWAL},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Cleanup(func() { restoreIntegrationTeardown(t) })
			dir := archiveRoot + "/" + sc.hide[:16]
			hide := `set -e; f=$(ls ` + dir + `/` + sc.hide + `-*); mv "$f" "$f.hidden-by-test"`
			if _, err := testutil.DockerExec(ctx, restoreIntegrationContainer, "sh", "-c", hide); err != nil {
				t.Fatalf("hiding %s: %v", sc.hide, err)
			}
			t.Cleanup(func() {
				unhide := `for f in ` + dir + `/*.hidden-by-test; do mv "$f" "${f%.hidden-by-test}"; done`
				if _, err := testutil.DockerExec(context.Background(), restoreIntegrationContainer, "sh", "-c", unhide); err != nil {
					t.Errorf("cleanup: restoring %s: %v", sc.hide, err)
				}
			})

			status, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-db"})
			if err != nil {
				t.Fatalf("getPgBackRestStatusImpl: %v", err)
			}
			wal := status.WALContinuity
			if wal == nil || wal.Status != sc.wantStatus || wal.RecoverableToWAL != sc.wantEnd {
				t.Fatalf("WAL check = %+v, want %s with recovery reaching %s", wal, sc.wantStatus, sc.wantEnd)
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
			if got := lastRestoredSegment(log); got != sc.wantEnd {
				t.Errorf("PostgreSQL's last restored segment = %q, the WAL check predicted %s\n%s", got, sc.wantEnd, log)
			}
		})
	}
}
