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
//	go test -tags integration -timeout 120s ./agents/sysadmin/... -run RestoreFromBackup
package main

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
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
	if err := exec.CommandContext(ctx, "docker", "restart", restoreIntegrationContainer).Run(); err != nil {
		t.Fatalf("docker restart (post-restore): %v", err)
	}
	time.Sleep(8 * time.Second)

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
