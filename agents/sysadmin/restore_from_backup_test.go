package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// sequencedErrRunner is sequencedRunner's sibling: restore_from_backup always
// makes a pg_isready safety check before the real restore, so tests need a
// mock that can fail the first call and succeed the second (or vice versa) —
// unlike sequencedRunner (always nil error) or mockRunner (same err every
// call), neither of which can express that.
type sequencedErrRunner struct {
	outputs []string
	errs    []error
	calls   [][]string
}

func (r *sequencedErrRunner) Run(_ context.Context, _ string, args []string, _ []string) (string, error) {
	r.calls = append(r.calls, args)
	idx := len(r.calls) - 1
	var out string
	var err error
	if idx < len(r.outputs) {
		out = r.outputs[idx]
	}
	if idx < len(r.errs) {
		err = r.errs[idx]
	}
	return out, err
}

// TestRestoreFromBackupTool_Success verifies the bare-local dispatch path
// (no infra config): pg_isready reports the server down (non-zero exit),
// so the tool proceeds to remove any stale postmaster.pid and run the real
// restore — the exact sequence verified live against a real crashed-then-
// restored pgBackRest fixture before this tool was written.
func TestRestoreFromBackupTool_Success(t *testing.T) {
	capture := &sequencedErrRunner{
		outputs: []string{"localhost:5432 - no response\n", "restore command end: completed successfully (200ms)\n"},
		errs:    []error{fmt.Errorf("exit status 2"), nil},
	}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	result, err := restoreFromBackupTool(ctx, RestoreFromBackupArgs{Stanza: "main"})
	if err != nil {
		t.Fatalf("restoreFromBackupTool() error = %v", err)
	}
	if !strings.Contains(result.Output, "completed successfully") {
		t.Errorf("Output = %q, want it to contain completed successfully", result.Output)
	}
	if len(capture.calls) != 2 {
		t.Fatalf("expected 2 calls (pg_isready, then restore), got %d: %v", len(capture.calls), capture.calls)
	}
	restoreScript := strings.Join(capture.calls[1], " ")
	if !strings.Contains(restoreScript, "postmaster.pid") || !strings.Contains(restoreScript, "pg1-path") {
		t.Errorf("restore script = %q, want it to clear a stale postmaster.pid using pgBackRest's own pg1-path config", restoreScript)
	}
	if !strings.Contains(restoreScript, "--stanza='main'") || !strings.Contains(restoreScript, "--delta") || !strings.Contains(restoreScript, "restore") {
		t.Errorf("restore script = %q, want a --stanza='main' --delta restore invocation", restoreScript)
	}
}

// TestRestoreFromBackupTool_RefusesWhenServerIsUp is the core safety test:
// if pg_isready reports the server accepting connections, the tool must
// refuse outright and never touch postmaster.pid or run restore — confirmed
// live this is the one thing standing between restoring a dead instance and
// destroying a live one.
func TestRestoreFromBackupTool_RefusesWhenServerIsUp(t *testing.T) {
	capture := &sequencedErrRunner{
		outputs: []string{"localhost:5432 - accepting connections\n"},
		errs:    []error{nil},
	}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	_, err := restoreFromBackupTool(ctx, RestoreFromBackupArgs{Stanza: "main"})
	if err == nil {
		t.Fatal("restoreFromBackupTool() error = nil, want a refusal when the server is still accepting connections")
	}
	if !strings.Contains(err.Error(), "accepting connections") {
		t.Errorf("error = %v, want it to mention the server is still accepting connections", err)
	}
	if len(capture.calls) != 1 {
		t.Fatalf("expected only the pg_isready call, got %d: %v — restore must never run after a positive pg_isready", len(capture.calls), capture.calls)
	}
}

// TestRestoreFromBackupTool_DockerDispatch verifies the docker exec dispatch
// shape: pg_isready and the restore script both run as the postgres OS user
// via su -c, matching get_pgbackrest_status/run_pgbackrest_backup's own
// dispatch convention.
func TestRestoreFromBackupTool_DockerDispatch(t *testing.T) {
	withPgBackRestDockerInfra(t)
	capture := &sequencedErrRunner{
		outputs: []string{"no response\n", "restore command end: completed successfully (200ms)\n"},
		errs:    []error{fmt.Errorf("exit status 2"), nil},
	}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	if _, err := restoreFromBackupTool(ctx, RestoreFromBackupArgs{Target: "pgbackrest_db", Stanza: "main"}); err != nil {
		t.Fatalf("restoreFromBackupTool() error = %v", err)
	}
	if len(capture.calls) != 2 {
		t.Fatalf("expected 2 docker exec calls (pg_isready, restore), got %d: %v", len(capture.calls), capture.calls)
	}
	readyCall := capture.calls[0]
	if len(readyCall) < 2 || readyCall[0] != "exec" || readyCall[1] != "helpdesk-test-pg-pgbackrest" {
		t.Fatalf("docker args = %v, want [exec helpdesk-test-pg-pgbackrest ...]", readyCall)
	}
	if !strings.Contains(strings.Join(readyCall, " "), "pg_isready") {
		t.Errorf("ready call = %v, want it to run pg_isready", readyCall)
	}
	restoreCall := strings.Join(capture.calls[1], " ")
	if !strings.Contains(restoreCall, "su") || !strings.Contains(restoreCall, "postgres") {
		t.Errorf("restore call = %q, want it to run as the postgres OS user via su", restoreCall)
	}
	if !strings.Contains(restoreCall, "--stanza='main'") || !strings.Contains(restoreCall, "--delta") {
		t.Errorf("restore call = %q, want a --stanza='main' --delta restore invocation", restoreCall)
	}
}

// TestRestoreFromBackupTool_SSHDispatch proves the tool reaches a real
// remote host over a real SSH round trip for both the safety check and the
// restore itself, mirroring TestRunPgBackRestBackupTool_SSHDispatch.
func TestRestoreFromBackupTool_SSHDispatch(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		if strings.Contains(cmd, "pg_isready") {
			return "no response", 1
		}
		if strings.Contains(cmd, "pgbackrest") && strings.Contains(cmd, "restore") && strings.Contains(cmd, "stanza") && strings.Contains(cmd, "main") {
			return "restore command end: completed successfully (200ms)\n", 0
		}
		return "unexpected command: " + cmd, 1
	})
	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck

	addr, port := hostPortOf(t, srv.addr)
	withPgBackRestSSHInfra(t, addr, port, keyPath)

	ctx := mockToolContext{context.Background()}
	result, err := restoreFromBackupTool(ctx, RestoreFromBackupArgs{Target: "pgbackrest_db", Stanza: "main"})
	if err != nil {
		t.Fatalf("restoreFromBackupTool() error = %v", err)
	}
	if !strings.Contains(result.Output, "completed successfully") {
		t.Errorf("Output = %q, want it to contain completed successfully", result.Output)
	}
}

// TestRestoreFromBackupTool_K8sDispatch mirrors
// TestGetPgBackRestStatusTool_K8sDispatch, accounting for restore_from_backup
// making two separate execInProcess calls (pg_isready, then restore) —
// each one independently resolves the pod name via `kubectl get pod`, so
// the full sequence is get-pod/exec/get-pod/exec (4 kubectl invocations),
// not the 2 a single-call tool makes.
func TestRestoreFromBackupTool_K8sDispatch(t *testing.T) {
	withK8sInfra(t)
	capture := &sequencedErrRunner{
		outputs: []string{"pg-prod-db-0\n", "no response\n", "pg-prod-db-0\n", "restore command end: completed successfully (200ms)\n"},
		errs:    []error{nil, fmt.Errorf("exit status 2"), nil, nil},
	}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	result, err := restoreFromBackupTool(ctx, RestoreFromBackupArgs{Target: "prod_db", Stanza: "main"})
	if err != nil {
		t.Fatalf("restoreFromBackupTool() error = %v", err)
	}
	if !strings.Contains(result.Output, "completed successfully") {
		t.Errorf("Output = %q, want it to contain completed successfully", result.Output)
	}
	if len(capture.calls) != 4 {
		t.Fatalf("expected 4 kubectl calls (get pod, exec pg_isready, get pod, exec restore), got %d: %v", len(capture.calls), capture.calls)
	}
	if !containsSeq(capture.calls[1], "exec", "pg-prod-db-0") || !strings.Contains(strings.Join(capture.calls[1], " "), "pg_isready") {
		t.Errorf("call[1] = %v, want a pg_isready exec against pg-prod-db-0", capture.calls[1])
	}
	restoreExec := strings.Join(capture.calls[3], " ")
	if !strings.Contains(restoreExec, "su postgres -c") || !strings.Contains(restoreExec, "--delta") {
		t.Errorf("call[3] = %q, want a su postgres -c ... --delta restore invocation", restoreExec)
	}
}

// TestRestoreFromBackupTool_PolicyDenied verifies restore_from_backup is
// gated as ActionDestructive, not ActionWrite like run_pgbackrest_backup —
// a destructive-only deny rule must block it.
func TestRestoreFromBackupTool_PolicyDenied(t *testing.T) {
	withPgBackRestDockerInfra(t)
	defer withSysadminPolicyEnforcer(newDenyHostDestructiveEnforcer(t))()
	capture := &sequencedErrRunner{}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	_, err := restoreFromBackupImpl(context.Background(), RestoreFromBackupArgs{
		Target: "pgbackrest_db",
		Stanza: "main",
	})
	if err == nil {
		t.Fatal("expected policy denial, got nil error")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("error %q should mention 'not permitted'", err.Error())
	}
	if len(capture.calls) != 0 {
		t.Errorf("expected 0 calls when policy denies before any command runs, got %d: %v", len(capture.calls), capture.calls)
	}
}

// TestRestoreFromBackupTool_EmptyTargetWithInfraConfig_Errors mirrors
// TestRunPgBackRestBackupTool_EmptyTargetWithInfraConfig_Errors.
func TestRestoreFromBackupTool_EmptyTargetWithInfraConfig_Errors(t *testing.T) {
	withPgBackRestDockerInfra(t)
	_, err := restoreFromBackupImpl(context.Background(), RestoreFromBackupArgs{Stanza: "main"})
	if err == nil {
		t.Fatal("restoreFromBackupImpl() error = nil, want an error when infraConfig is loaded but target is empty")
	}
	if !strings.Contains(err.Error(), "target is required") {
		t.Errorf("error = %v, want it to explain target is required when infra config is loaded", err)
	}
}

// TestRestoreFromBackupTool_DefaultsStanzaViaStatus verifies that with no
// stanza given, the tool resolves it via get_pgbackrest_status's own `info`
// call first (pgBackRest's restore subcommand has no auto-detect-the-only-
// stanza behavior, same reasoning as run_pgbackrest_backup's identical
// fallback), before the pg_isready check and the restore itself.
func TestRestoreFromBackupTool_DefaultsStanzaViaStatus(t *testing.T) {
	capture := &sequencedErrRunner{
		outputs: []string{realPgBackRestInfoJSON, "no response\n", "restore command end: completed successfully (200ms)\n"},
		errs:    []error{nil, fmt.Errorf("exit status 2"), nil},
	}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	if _, err := restoreFromBackupTool(ctx, RestoreFromBackupArgs{}); err != nil {
		t.Fatalf("restoreFromBackupTool() error = %v", err)
	}
	if len(capture.calls) != 3 {
		t.Fatalf("expected 3 calls (resolve stanza, pg_isready, restore), got %d: %v", len(capture.calls), capture.calls)
	}
	if !strings.Contains(strings.Join(capture.calls[2], " "), "--stanza='main'") {
		t.Errorf("restore call = %v, want the resolved stanza name from the info call", capture.calls[2])
	}
}

// TestRestoreFromBackupTool_PropagatesRunnerError verifies a real restore
// failure (after pg_isready already confirmed the server down) surfaces
// pgBackRest's own diagnostic output, not just a generic error.
func TestRestoreFromBackupTool_PropagatesRunnerError(t *testing.T) {
	capture := &sequencedErrRunner{
		outputs: []string{"no response\n", "ERROR: [055]: unable to find a backup set to restore"},
		errs:    []error{fmt.Errorf("exit status 2"), fmt.Errorf("exit status 1")},
	}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	_, err := restoreFromBackupTool(ctx, RestoreFromBackupArgs{Stanza: "main"})
	if err == nil {
		t.Fatal("restoreFromBackupTool() error = nil, want an error when pgbackrest restore exits non-zero")
	}
	if !strings.Contains(err.Error(), "unable to find a backup set") {
		t.Errorf("error = %v, want it to include pgbackrest's own diagnostic output", err)
	}
}
