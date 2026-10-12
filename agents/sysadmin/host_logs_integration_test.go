//go:build integration

// Integration test for get_host_logs against a real stderr-only failure
// signature — not a mocked CommandRunner. Every unit test for
// getHostLogsImpl uses a mockRunner that returns the same canned string
// regardless of which stream it claims to be, so none of them could have
// caught this: cmdRunner.Run returns stdout alone on success (correct for
// get_pgbackrest_status, where stderr is non-fatal WARN noise that would
// corrupt JSON parsing; wrong here). PostgreSQL's own "could not find the
// database system" message, emitted before its logging subsystem
// initializes, goes to stderr, not stdout — confirmed live, 2026-10-07, by
// comparing `docker logs 2>/dev/null` (empty) against `docker logs 1>/dev/null`
// (the message) for the exact same crash. get_host_logs was silently and
// unconditionally dropping it, with no error to signal it — found only
// because a live multi-hop diagnosis kept failing to recognize a signature
// that was genuinely never visible to it, no matter how large the line
// window or how long diagnosis waited. Fixed by merging streams with an
// explicit shell `2>&1` before cmdRunner.Run ever sees the output, sidestepping
// the stdout/stderr split entirely without touching the shared helper other
// callers correctly rely on. This test turns that one-off live finding into
// a permanent regression check, the same principle
// restore_from_backup_integration_test.go already established.
//
// Requires the pgBackRest overlay (make integration already brings this up):
//
//	docker compose -f testing/docker/docker-compose.yaml \
//	  -f testing/docker/docker-compose.pgbackrest.yaml up -d --wait
//	./testing/docker/setup-pgbackrest-backup.sh
//
// Run with:
//
//	go test -tags integration -timeout 60s ./agents/sysadmin/... -run GetHostLogs_RealStderr
package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/infra"
)

func TestIntegration_GetHostLogs_RealStderrOnlyFailure(t *testing.T) {
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

	// Corrupt and crash, same real signature db-pgdata-corrupted injects.
	if err := exec.CommandContext(ctx, "docker", "exec", restoreIntegrationContainer,
		"rm", "-f", "/var/lib/postgresql/data/global/pg_control").Run(); err != nil {
		t.Fatalf("corrupting pg_control: %v", err)
	}
	if err := exec.CommandContext(ctx, "docker", "restart", restoreIntegrationContainer).Run(); err != nil {
		t.Fatalf("docker restart (to trigger the crash): %v", err)
	}
	time.Sleep(5 * time.Second)

	result, err := getHostLogsImpl(ctx, GetHostLogsArgs{Target: "pgbackrest-db", Lines: 300})
	if err != nil {
		t.Fatalf("getHostLogsImpl: %v", err)
	}
	if !strings.Contains(result.Logs, "could not find the database system") {
		t.Fatalf("get_host_logs result does not contain the real stderr-only crash signature — "+
			"the stdout/stderr merge regressed. Got:\n%s", result.Logs)
	}
}
