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
	capture := &pgbackrestRouter{
		info:       realPgBackRestInfoJSON,
		readyOut:   "localhost:5432 - no response\n",
		readyErr:   fmt.Errorf("exit status 2"),
		restoreOut: "restore command end: completed successfully (200ms)\n",
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
	// pg_isready, then the WAL-continuity check (info + 2 repo reads), then
	// the restore — in that order, so the restore only runs once both pass.
	if len(capture.calls) != 5 {
		t.Fatalf("expected 5 calls (pg_isready, info, 2 WAL reads, restore), got %d: %v", len(capture.calls), capture.calls)
	}
	if len(capture.calls[0]) != 0 || !strings.Contains(strings.Join(capture.calls[1], " "), "info") {
		t.Errorf("calls = %v, want pg_isready (no args) first, then the info read", capture.calls)
	}
	restoreScript := strings.Join(capture.calls[4], " ")
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
	capture := &pgbackrestRouter{
		info:       realPgBackRestInfoJSON,
		readyOut:   "no response\n",
		readyErr:   fmt.Errorf("exit status 2"),
		restoreOut: "restore command end: completed successfully (200ms)\n",
	}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	if _, err := restoreFromBackupTool(ctx, RestoreFromBackupArgs{Target: "pgbackrest_db", Stanza: "main"}); err != nil {
		t.Fatalf("restoreFromBackupTool() error = %v", err)
	}
	if len(capture.calls) != 5 {
		t.Fatalf("expected 5 docker exec calls (pg_isready, info, 2 WAL reads, restore), got %d: %v", len(capture.calls), capture.calls)
	}
	for _, c := range capture.calls {
		if len(c) < 2 || c[0] != "exec" || c[1] != "helpdesk-test-pg-pgbackrest" {
			t.Errorf("call = %v, want every step via docker exec on the target container", c)
		}
	}
	readyCall := capture.calls[0]
	if len(readyCall) < 2 || readyCall[0] != "exec" || readyCall[1] != "helpdesk-test-pg-pgbackrest" {
		t.Fatalf("docker args = %v, want [exec helpdesk-test-pg-pgbackrest ...]", readyCall)
	}
	if !strings.Contains(strings.Join(readyCall, " "), "pg_isready") {
		t.Errorf("ready call = %v, want it to run pg_isready", readyCall)
	}
	restoreCall := strings.Join(capture.calls[4], " ")
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
		switch {
		case strings.Contains(cmd, "pg_isready"):
			return "no response", 1
		case strings.Contains(cmd, "'info'"):
			return realPgBackRestInfoJSON, 0
		case strings.Contains(cmd, "repo-ls") && strings.Contains(cmd, "--recurse"):
			return realInfoWALListing, 0
		case strings.Contains(cmd, "repo-ls"):
			return realInfoArchiveTop, 0
		case strings.Contains(cmd, "pgbackrest") && strings.Contains(cmd, "restore") && strings.Contains(cmd, "stanza") && strings.Contains(cmd, "main"):
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
// TestGetPgBackRestStatusTool_K8sDispatch. Every execInProcess call resolves
// the pod name via `kubectl get pod` first, so each of the 5 steps
// (pg_isready, info, 2 WAL reads, restore) is a get-pod/exec pair.
func TestRestoreFromBackupTool_K8sDispatch(t *testing.T) {
	withK8sInfra(t)
	capture := &pgbackrestRouter{
		info:       realPgBackRestInfoJSON,
		readyOut:   "no response\n",
		readyErr:   fmt.Errorf("exit status 2"),
		restoreOut: "restore command end: completed successfully (200ms)\n",
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
	if len(capture.calls) != 10 {
		t.Fatalf("expected 10 kubectl calls (get pod + exec for 5 steps), got %d: %v", len(capture.calls), capture.calls)
	}
	if !containsSeq(capture.calls[1], "exec", "pg-prod-db-0") || !strings.Contains(strings.Join(capture.calls[1], " "), "pg_isready") {
		t.Errorf("call[1] = %v, want a pg_isready exec against pg-prod-db-0", capture.calls[1])
	}
	restoreExec := strings.Join(capture.calls[9], " ")
	if !strings.Contains(restoreExec, "su postgres -c") || !strings.Contains(restoreExec, "--delta") {
		t.Errorf("call[9] = %q, want a su postgres -c ... --delta restore invocation", restoreExec)
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
	capture := &pgbackrestRouter{
		info:       realPgBackRestInfoJSON,
		readyOut:   "no response\n",
		readyErr:   fmt.Errorf("exit status 2"),
		restoreOut: "restore command end: completed successfully (200ms)\n",
	}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	if _, err := restoreFromBackupTool(ctx, RestoreFromBackupArgs{}); err != nil {
		t.Fatalf("restoreFromBackupTool() error = %v", err)
	}
	// Stanza resolution is an info-only read (no WAL reads) ahead of policy
	// and pg_isready; the WAL check then reads info again with the stanza.
	if len(capture.calls) != 6 {
		t.Fatalf("expected 6 calls (resolve stanza, pg_isready, info, 2 WAL reads, restore), got %d: %v", len(capture.calls), capture.calls)
	}
	if !strings.Contains(strings.Join(capture.calls[0], " "), "info") || strings.Contains(strings.Join(capture.calls[1], " "), "repo-ls") {
		t.Errorf("calls = %v, want stanza resolution to be a bare info read", capture.calls)
	}
	if !strings.Contains(strings.Join(capture.calls[5], " "), "--stanza='main'") {
		t.Errorf("restore call = %v, want the resolved stanza name from the info call", capture.calls[5])
	}
}

// TestRestoreFromBackupTool_PropagatesRunnerError verifies a real restore
// failure (after pg_isready already confirmed the server down) surfaces
// pgBackRest's own diagnostic output, not just a generic error. The exact
// error text and exit code (75, "[075]: no backup set found to restore")
// are verbatim from a real pgBackRest 2.59.2 restore attempted live against
// a stanza that was never stanza-created (2026-10-06) — not invented. That
// same live test confirmed something more important than the error text
// itself: pgBackRest validates backup existence BEFORE touching the target
// directory at all (a canary file placed in the target survived the failed
// restore untouched) — restoring against a nonexistent backup is already
// safe today with zero code on our side, so this test only needs to prove
// the failure is surfaced, not guard against data loss itself.
func TestRestoreFromBackupTool_PropagatesRunnerError(t *testing.T) {
	capture := &pgbackrestRouter{
		info:       realPgBackRestInfoJSON,
		readyOut:   "no response\n",
		readyErr:   fmt.Errorf("exit status 2"),
		restoreOut: "ERROR: [075]: no backup set found to restore",
		restoreErr: fmt.Errorf("exit status 75"),
	}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	ctx := mockToolContext{context.Background()}
	_, err := restoreFromBackupTool(ctx, RestoreFromBackupArgs{Stanza: "main"})
	if err == nil {
		t.Fatal("restoreFromBackupTool() error = nil, want an error when pgbackrest restore exits non-zero")
	}
	if !strings.Contains(err.Error(), "no backup set found to restore") {
		t.Errorf("error = %v, want it to include pgbackrest's own diagnostic output", err)
	}
	if len(capture.callsContaining("--delta restore")) != 1 {
		t.Errorf("calls = %v, want the restore itself to have run and failed", capture.calls)
	}
}

// TestRestoreFromBackupTool_RefusesOnBrokenWALChain is the core v0.32 safety
// test: the backup's own WAL segment is missing from the repo, so PostgreSQL
// could never reach consistency from it. The tool must refuse after
// pg_isready and before the restore — PGDATA is never touched.
func TestRestoreFromBackupTool_RefusesOnBrokenWALChain(t *testing.T) {
	capture := &pgbackrestRouter{
		info:       realPgBackRestInfoJSON,
		walListing: `{".":{"type":"path"},"0000000100000000":{"type":"path"}}`,
		readyOut:   "no response\n",
		readyErr:   fmt.Errorf("exit status 2"),
		restoreOut: "restore command end: completed successfully (200ms)\n",
	}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	_, err := restoreFromBackupTool(mockToolContext{context.Background()}, RestoreFromBackupArgs{Stanza: "main"})
	if err == nil {
		t.Fatal("restoreFromBackupTool() error = nil, want a refusal on a broken WAL chain")
	}
	for _, want := range []string{"refusing to restore", "WAL continuity is broken", "20260926-040922F", "could not start"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
	if n := len(capture.callsContaining("--delta restore")); n != 0 {
		t.Errorf("restore ran %d time(s) despite the broken WAL chain: %v", n, capture.calls)
	}
}

// TestRestoreFromBackupTool_RefusesWhenWALUnverified: when the check can't
// run (here, an old pgBackRest without repo-ls), the restore fails closed.
func TestRestoreFromBackupTool_RefusesWhenWALUnverified(t *testing.T) {
	inner := &pgbackrestRouter{
		info:       realPgBackRestInfoJSON,
		readyOut:   "no response\n",
		readyErr:   fmt.Errorf("exit status 2"),
		restoreOut: "restore command end: completed successfully (200ms)\n",
	}
	capture := runnerFunc(func(ctx context.Context, name string, args []string, env []string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "repo-ls") {
			inner.calls = append(inner.calls, args)
			return "ERROR: [031]: invalid command 'repo-ls'", fmt.Errorf("exit status 31")
		}
		return inner.Run(ctx, name, args, env)
	})
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	_, err := restoreFromBackupTool(mockToolContext{context.Background()}, RestoreFromBackupArgs{Stanza: "main"})
	if err == nil || !strings.Contains(err.Error(), "WAL continuity is unverified") {
		t.Fatalf("error = %v, want a refusal because WAL continuity is unverified", err)
	}
	if n := len(inner.callsContaining("--delta restore")); n != 0 {
		t.Errorf("restore ran %d time(s) with WAL continuity unverified", n)
	}
}

// runnerFunc adapts a function to the cmdRunner interface.
type runnerFunc func(ctx context.Context, name string, args []string, env []string) (string, error)

func (f runnerFunc) Run(ctx context.Context, name string, args []string, env []string) (string, error) {
	return f(ctx, name, args, env)
}
