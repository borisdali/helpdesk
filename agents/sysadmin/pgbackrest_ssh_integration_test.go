//go:build integration

// Integration test for the SSH dispatch path (runOnHost -> sshRun,
// agents/sysadmin/sshexec.go) against the real pgBackRest test container —
// not the synthetic in-process SSH server sshexec_test.go/pgbackrest_test.go
// already cover. Exercises real sshd (StrictModes, real host-key-algorithm
// negotiation) end to end, which is exactly where live verification found
// two real bugs (a world-writable home dir silently failing StrictModes, and
// extra auto-generated RSA/ECDSA host keys winning algorithm negotiation
// over the pinned ed25519 key) that the synthetic server could never have
// caught.
//
// Requires the pgBackRest overlay (not part of `make integration`'s base
// docker-compose.yaml bring-up, by design — see docker-compose.pgbackrest.yaml's
// own comment on why it's a separate opt-in target). Start with:
//
//	docker compose -f testing/docker/docker-compose.yaml \
//	  -f testing/docker/docker-compose.pgbackrest.yaml up -d --wait
//
// Run with:
//
//	go test -tags integration -timeout 60s ./agents/sysadmin/... -run SSH
package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"helpdesk/internal/infra"
)

// skipIfNoPgBackRestSSH skips the test when the real SSH-enabled pgBackRest
// container isn't reachable on its pinned port.
func skipIfNoPgBackRestSSH(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", "localhost:15436", 2*time.Second)
	if err != nil {
		t.Skip("pgBackRest SSH container not reachable on :15436 — run: " +
			"docker compose -f testing/docker/docker-compose.yaml -f testing/docker/docker-compose.pgbackrest.yaml up -d --wait")
	}
	conn.Close()
}

func TestIntegration_GetPgBackRestStatus_OverRealSSH(t *testing.T) {
	skipIfNoPgBackRestSSH(t)

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

	oldKnownHosts := os.Getenv("SSH_KNOWN_HOSTS")
	os.Setenv("SSH_KNOWN_HOSTS", filepath.Join(repoRoot, "testing", "docker", "ssh", "pgbackrest_test_known_hosts"))
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKnownHosts) })

	// VM.SSHKeyPath ("testing/docker/ssh/pgbackrest_test_client_key") is
	// repo-root-relative, read fresh from disk on every connection
	// (sshexec.go's loadSSHAuth) — run from repo root so that resolves.
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(oldWd) })

	result, err := getPgBackRestStatusImpl(context.Background(), GetPgBackRestStatusArgs{
		Target: "pgbackrest-db-ssh",
		Stanza: "main",
	})
	if err != nil {
		t.Fatalf("getPgBackRestStatusImpl over real SSH failed: %v", err)
	}
	if result.StatusCode != 0 {
		t.Errorf("expected healthy stanza status 0, got %d (output: %s)", result.StatusCode, result.Output)
	}
	if result.LastBackupTime == "" {
		t.Error("expected a real prior backup on the test container, got none — " +
			"run testing/docker/setup-pgbackrest-backup.sh first")
	}
}
