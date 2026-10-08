package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"helpdesk/internal/infra"
)

// TestCheckHostTool_SSHDispatch, TestGetHostLogsTool_SSHDispatch,
// TestRestartServiceTool_SSHDispatch, and TestRestartContainerTool_SSHDispatch
// are regression tests for a real bug found live 2026-10-08: check_host,
// get_host_logs, restart_container, and restart_service all called
// cmdRunner.Run directly instead of runOnHost (sshexec.go) — meaning they
// always ran their commands on the sysadmin agent's own local host,
// silently ignoring a target's configured SSH credentials. Only the
// pgBackRest-specific tools (get_pgbackrest_status/restore_from_backup/
// run_pgbackrest_backup) were ever wired for real SSH dispatch. These
// prove, over a real in-process SSH server (real TCP, real handshake, real
// exec channel — mirroring pgbackrest_test.go's own
// TestGetPgBackRestStatusTool_SSHDispatch exactly), that the fix actually
// reaches the remote host rather than just compiling against runOnHost's
// signature.

func TestCheckHostTool_SSHDispatch(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		if !strings.Contains(cmd, "systemctl") || !strings.Contains(cmd, "show") || !strings.Contains(cmd, "postgresql-16") {
			return "unexpected command: " + cmd, 1
		}
		return "ActiveState=active\nSubState=running\nResult=success\nMainPID=1234\n", 0
	})
	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck

	addr, port := hostPortOf(t, srv.addr)
	withPgBackRestSSHInfra(t, addr, port, keyPath)

	ctx := mockToolContext{context.Background()}
	result, err := checkHostTool(ctx, CheckHostArgs{Target: "pgbackrest_db"})
	if err != nil {
		t.Fatalf("checkHostTool() error = %v", err)
	}
	if result.Runtime != "systemd" {
		t.Errorf("Runtime = %q, want systemd", result.Runtime)
	}
	if result.Status != "running" {
		t.Errorf("Status = %q, want running — real SSH round trip did not reach the remote host", result.Status)
	}
}

func TestGetHostLogsTool_SSHDispatch(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		if !strings.Contains(cmd, "journalctl") || !strings.Contains(cmd, "postgresql-16") {
			return "unexpected command: " + cmd, 1
		}
		return "Oct 08 12:00:00 host systemd[1]: Started PostgreSQL.\n", 0
	})
	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck

	addr, port := hostPortOf(t, srv.addr)
	withPgBackRestSSHInfra(t, addr, port, keyPath)

	ctx := mockToolContext{context.Background()}
	result, err := getHostLogsTool(ctx, GetHostLogsArgs{Target: "pgbackrest_db"})
	if err != nil {
		t.Fatalf("getHostLogsTool() error = %v", err)
	}
	if !strings.Contains(result.Logs, "Started PostgreSQL") {
		t.Errorf("Logs = %q, want it to contain the remote journalctl output — real SSH round trip did not reach the remote host", result.Logs)
	}
}

func TestRestartServiceTool_SSHDispatch(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		if !strings.Contains(cmd, "systemctl") || !strings.Contains(cmd, "restart") || !strings.Contains(cmd, "postgresql-16") {
			return "unexpected command: " + cmd, 1
		}
		return "", 0
	})
	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck

	addr, port := hostPortOf(t, srv.addr)
	withPgBackRestSSHInfra(t, addr, port, keyPath)

	ctx := mockToolContext{context.Background()}
	result, err := restartServiceTool(ctx, RestartServiceArgs{Target: "pgbackrest_db", Reason: "test"})
	if err != nil {
		t.Fatalf("restartServiceTool() error = %v", err)
	}
	if !result.Success {
		t.Error("Success = false, want true — real SSH round trip did not reach the remote host")
	}
}

// withSSHDockerInfra mirrors withPgBackRestSSHInfra but for a Docker-runtime
// VM reached over SSH — restartContainerTool's own dispatch, unlike the
// other three tested above, is Docker/Podman-only (restart_service covers
// systemd), so it needs a container_name + Runtime=docker VM entry, not
// withPgBackRestSSHInfra's systemd_unit one.
func withSSHDockerInfra(t *testing.T, addr string, port int, keyPath string) {
	t.Helper()
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"docker_db": {
				Name:             "docker_db",
				ConnectionString: "host=localhost",
				VMName:           "docker-vm",
				ContainerName:    "test-container",
			},
		},
		VMs: map[string]infra.VM{
			"docker-vm": {
				Name:       "docker-vm",
				Runtime:    "docker",
				Address:    addr,
				SSHUser:    "testuser",
				SSHPort:    port,
				SSHKeyPath: keyPath,
			},
		},
	}
	t.Cleanup(func() { infraConfig = nil })
}

func TestRestartContainerTool_SSHDispatch(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		if !strings.Contains(cmd, "docker") || !strings.Contains(cmd, "restart") || !strings.Contains(cmd, "test-container") {
			return "unexpected command: " + cmd, 1
		}
		return "test-container\n", 0
	})
	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck

	addr, port := hostPortOf(t, srv.addr)
	withSSHDockerInfra(t, addr, port, keyPath)

	ctx := mockToolContext{context.Background()}
	result, err := restartContainerTool(ctx, RestartContainerArgs{Target: "docker_db", Reason: "test"})
	if err != nil {
		t.Fatalf("restartContainerTool() error = %v", err)
	}
	if !result.Success {
		t.Error("Success = false, want true — real SSH round trip did not reach the remote host")
	}
}

// TestExecInProcess_SystemdTarget_DispatchesViaRunOnHost is a regression
// test for execInProcess's own fix: the systemd default case used to error
// outright ("cannot exec into process") rather than dispatch at all — this
// proves it now reaches a real remote host over real SSH, same as the four
// tools above, closing the gap for read_pg_log_file/check_memory too.
func TestExecInProcess_SystemdTarget_DispatchesViaRunOnHost(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		if !strings.Contains(cmd, "ls") || !strings.Contains(cmd, "-t") {
			return "unexpected command: " + cmd, 1
		}
		return "postgresql-16-main.log\n", 0
	})
	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck

	addr, port := hostPortOf(t, srv.addr)
	host := resolvedHost{
		VMAddress:  addr,
		SSHUser:    "testuser",
		SSHPort:    port,
		SSHKeyPath: keyPath,
	}
	out, err := execInProcess(context.Background(), host, []string{"ls", "-t", "/var/log/postgresql"})
	if err != nil {
		t.Fatalf("execInProcess() error = %v, want it to dispatch via runOnHost instead of erroring", err)
	}
	if !strings.Contains(out, "postgresql-16-main.log") {
		t.Errorf("output = %q, want the remote ls output — real SSH round trip did not reach the remote host", out)
	}
}
