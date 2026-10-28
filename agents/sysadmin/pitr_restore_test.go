package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// pitrRouter is a router over the multi_timeline fixtures (backup
// 20261007-043435F, archive to 2026-10-11 on timeline 30, continuous), so
// 2026-10-09T00:00:00Z is a feasible target. down says pg_isready fails.
func pitrRouter(t *testing.T, down bool) *pgbackrestRouter {
	t.Helper()
	r := &pgbackrestRouter{
		info:       readFixture(t, "multi_timeline_info.json"),
		restoreOut: "restore command end: completed successfully (200ms)\n",
		repoRun:    fakeWALRun(t, readFixture(t, "multi_timeline_000000010000000000000002.gz"), nil),
	}
	if down {
		r.readyOut, r.readyErr = "no response\n", fmt.Errorf("exit status 2")
	} else {
		r.readyOut = "accepting connections\n"
	}
	return r
}

const pitrTestTarget = "2026-10-09T00:00:00Z"

// approvedDigest plans the target the way a human's approval would.
func approvedDigest(t *testing.T, target string) string {
	t.Helper()
	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: target})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("plan for %s: %+v, %v", target, plan, err)
	}
	return plan.PlanDigest
}

func pitrScriptCall(r *pgbackrestRouter) string {
	var out []string
	for _, c := range r.callsContaining("--target-action=promote restore") {
		out = append(out, strings.Join(c, " "))
	}
	return strings.Join(out, "\n")
}

func TestRestoreToPIT_Success(t *testing.T) {
	r := pitrRouter(t, true)
	old := cmdRunner
	cmdRunner = r
	defer func() { cmdRunner = old }()
	t.Cleanup(func() {
		restorePredictions.Lock()
		delete(restorePredictions.byTarget, "")
		restorePredictions.Unlock()
	})

	digest := approvedDigest(t, pitrTestTarget)
	res, err := restoreToPITTool(mockToolContext{context.Background()}, RestoreToPITArgs{
		Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget, PlanDigest: digest})
	if err != nil {
		t.Fatalf("restoreToPITTool: %v", err)
	}
	script := pitrScriptCall(r)
	for _, want := range []string{
		"--stanza='main' --delta --set='20261007-043435F' --type=time --target='2026-10-09 00:00:00+00' --target-timeline=30 --target-action=promote restore",
		"standby.signal", "postmaster.pid", "pg_wal.saved-",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("restore script lacks %q:\n%s", want, script)
		}
	}
	// The WAL past the target must not come back or reach the archive.
	for _, banned := range []string{"cp -p", "archive-push"} {
		if strings.Contains(script, banned) {
			t.Errorf("restore script contains %q: the saved WAL must stay aside:\n%s", banned, script)
		}
	}
	if res.Prediction == nil || res.Prediction.TargetType != TargetTime || res.Prediction.TargetValue != "2026-10-09 00:00:00+00" ||
		res.Prediction.ExpectedTimeline != 31 || res.Prediction.BackupLabel != "20261007-043435F" {
		t.Errorf("prediction = %+v", res.Prediction)
	}
	if p, ok := loadRestorePrediction(""); !ok || p.TargetType != TargetTime {
		t.Errorf("the prediction for verify_restore_recovery was not recorded: %+v %v", p, ok)
	}
	if !strings.Contains(res.HowToRecover, "restore_from_backup") || res.Plan.PlanDigest != digest {
		t.Errorf("result = %+v", res)
	}
}

func TestRestoreToPIT_Refusals(t *testing.T) {
	good := RestoreToPITArgs{Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget}
	cases := []struct {
		name   string
		down   bool
		mutate func(a *RestoreToPITArgs, digest string)
		want   string
	}{
		{"serving instance", false, func(a *RestoreToPITArgs, d string) { a.PlanDigest = d }, "still accepting connections"},
		{"no digest", true, func(a *RestoreToPITArgs, d string) {}, "plan_digest is required"},
		{"blank digest", true, func(a *RestoreToPITArgs, d string) { a.PlanDigest = "  " }, "plan_digest is required"},
		{"digest of another plan", true, func(a *RestoreToPITArgs, d string) { a.PlanDigest = "0123456789abcdef" }, "plan changed since it was approved"},
		{"target changed after approval", true, func(a *RestoreToPITArgs, d string) {
			a.PlanDigest, a.RecoveryTargetVal = d, "2026-10-09T00:00:01Z"
		}, "plan changed since it was approved"},
		{"target past the archive end", true, func(a *RestoreToPITArgs, d string) {
			a.PlanDigest, a.RecoveryTargetVal = d, "2026-10-12T00:00:00Z"
		}, "plan is not_feasible"},
		{"target before every backup", true, func(a *RestoreToPITArgs, d string) {
			a.PlanDigest, a.RecoveryTargetVal = d, "2026-10-01T00:00:00Z"
		}, "plan is not_feasible"},
		{"malformed target", true, func(a *RestoreToPITArgs, d string) { a.PlanDigest, a.RecoveryTargetVal = d, "tomorrow" }, "RFC3339"},
		{"unsupported type", true, func(a *RestoreToPITArgs, d string) { a.PlanDigest, a.RecoveryTargetType = d, "xid" }, "not supported"},
	}
	for _, c := range cases {
		r := pitrRouter(t, c.down)
		old := cmdRunner
		cmdRunner = r
		args := good
		c.mutate(&args, approvedDigest(t, pitrTestTarget))
		_, err := restoreToPITImpl(context.Background(), args)
		cmdRunner = old
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want)
		}
		if script := pitrScriptCall(r); script != "" {
			t.Errorf("%s: the restore ran after a refusal:\n%s", c.name, script)
		}
	}
}

// A pgBackRest failure partway through leaves PGDATA unusable: the error
// must say so, name the saved WAL, and say what to do.
func TestRestoreToPIT_RestoreFailure(t *testing.T) {
	r := pitrRouter(t, true)
	r.restoreOut = walTailSavedMarker + "/var/lib/pg_wal.saved-20261027T150000Z\nERROR: [038]: unable to restore\n"
	r.restoreErr = fmt.Errorf("exit status 38")
	old := cmdRunner
	cmdRunner = r
	defer func() { cmdRunner = old }()
	digest := approvedDigest(t, pitrTestTarget)

	res, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{
		Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget, PlanDigest: digest})
	if err == nil || !strings.Contains(err.Error(), "unable to restore") || !strings.Contains(err.Error(), "pg_wal.saved-20261027T150000Z") {
		t.Fatalf("err = %v", err)
	}
	if res.SavedWAL != "/var/lib/pg_wal.saved-20261027T150000Z" || res.Prediction != nil {
		t.Errorf("result = %+v: want the saved path and no prediction", res)
	}
}

func TestRestoreToPIT_PolicyDenied(t *testing.T) {
	withPgBackRestDockerInfra(t)
	defer withSysadminPolicyEnforcer(newDenyHostDestructiveEnforcer(t))()
	capture := &sequencedErrRunner{}
	old := cmdRunner
	cmdRunner = capture
	defer func() { cmdRunner = old }()

	_, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget, PlanDigest: "x"})
	if err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("err = %v, want a policy denial", err)
	}
	if len(capture.calls) != 0 {
		t.Errorf("%d commands ran before the policy denied: %v", len(capture.calls), capture.calls)
	}
}

func TestRestoreToPIT_DockerDispatch(t *testing.T) {
	withPgBackRestDockerInfra(t)
	r := pitrRouter(t, true)
	old := cmdRunner
	cmdRunner = r
	defer func() { cmdRunner = old }()
	digest := func() string {
		plan, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget})
		if err != nil || plan.Status != PITRFeasible {
			t.Fatalf("plan: %+v %v", plan, err)
		}
		return plan.PlanDigest
	}()
	if _, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget, PlanDigest: digest}); err != nil {
		t.Fatal(err)
	}
	calls := r.callsContaining("--target-action=promote restore")
	if len(calls) != 1 {
		t.Fatalf("restore calls = %v", calls)
	}
	c := calls[0]
	if c[0] != "exec" || c[1] != "helpdesk-test-pg-pgbackrest" || !containsSeq(c, "su", "postgres", "-c") {
		t.Errorf("call = %v, want docker exec as the postgres user in the target container", c)
	}
}

// Kubernetes: scale to 0 first, the script in a maintenance pod with the
// saved WAL in the repo volume, left scaled down for restart_container.
func TestRestoreToPIT_K8s(t *testing.T) {
	r := pitrRouter(t, true)
	f := newFakeK8sCluster(r, false)
	withFakeK8sCluster(t, f)
	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "prod_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	res, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "prod_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget, PlanDigest: plan.PlanDigest})
	if err != nil {
		t.Fatal(err)
	}
	last := f.maintScript[len(f.maintScript)-1]
	if !strings.HasPrefix(last, "HELPDESK_WAL_SAVE_IN_REPO=1; export HELPDESK_WAL_SAVE_IN_REPO\n") || !strings.Contains(last, "--target-action=promote restore") {
		t.Errorf("maintenance script = %q", last)
	}
	if f.replicas != 0 || !strings.Contains(res.Quiesced, "restart_container") {
		t.Errorf("replicas %d, quiesced %q: want scaled to 0 and a restart_container hint", f.replicas, res.Quiesced)
	}
	if f.execs != 0 {
		t.Errorf("exec'd into the crash-looping pod %d times", f.execs)
	}
}

func TestRestoreToPIT_DirectRegistryAndAudit(t *testing.T) {
	r := pitrRouter(t, true)
	old := cmdRunner
	cmdRunner = r
	defer func() { cmdRunner = old }()
	digest := approvedDigest(t, pitrTestTarget)

	fn, ok := NewSysadminDirectRegistry().Get("restore_to_point_in_time")
	if !ok {
		t.Fatal("restore_to_point_in_time is not in the direct registry")
	}
	if _, err := fn(context.Background(), map[string]any{"stanza": "main", "recovery_target_type": "time",
		"recovery_target_value": pitrTestTarget, "plan_digest": digest}); err != nil {
		t.Fatal(err)
	}
	if _, err := fn(context.Background(), map[string]any{"stanza": "main", "recovery_target_type": "time",
		"recovery_target_value": pitrTestTarget}); err == nil {
		t.Error("a registry call without plan_digest must be refused")
	}
}

// With no stanza given it is resolved from pgBackRest's own status, as
// restore_from_backup does.
func TestRestoreToPIT_DefaultsStanza(t *testing.T) {
	r := pitrRouter(t, true)
	old := cmdRunner
	cmdRunner = r
	defer func() { cmdRunner = old }()
	digest := approvedDigest(t, pitrTestTarget)
	if _, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{
		RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget, PlanDigest: digest}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pitrScriptCall(r), "--stanza='main'") {
		t.Errorf("restore did not use the stanza from the status:\n%s", pitrScriptCall(r))
	}
}

func TestRestoreToPIT_EmptyTargetWithInfraConfig_Errors(t *testing.T) {
	withPgBackRestDockerInfra(t)
	r := pitrRouter(t, true)
	old := cmdRunner
	cmdRunner = r
	defer func() { cmdRunner = old }()
	_, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget, PlanDigest: "x"})
	if err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("err = %v", err)
	}
}

// If the repo cannot be read when the plan is recomputed, nothing is restored.
func TestRestoreToPIT_PlanCannotBeComputed(t *testing.T) {
	old := cmdRunner
	cmdRunner = runnerFunc(func(_ context.Context, name string, _ []string, _ []string) (string, error) {
		if name == "pg_isready" {
			return "no response", fmt.Errorf("exit status 2")
		}
		return "", fmt.Errorf("pgbackrest: unable to open repo")
	})
	defer func() { cmdRunner = old }()
	_, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget, PlanDigest: "x"})
	if err == nil || !strings.Contains(err.Error(), "could not plan the recovery") {
		t.Fatalf("err = %v", err)
	}
}

// A StatefulSet that cannot be scaled down is not restored over.
func TestRestoreToPIT_K8s_QuiesceFailureStopsTheRestore(t *testing.T) {
	f := newFakeK8sCluster(pitrRouter(t, true), false)
	withFakeK8sCluster(t, f)
	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "prod_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	inner := cmdRunner
	cmdRunner = runnerFunc(func(ctx context.Context, name string, args []string, env []string) (string, error) {
		if containsSeq(args, "scale", "statefulset") {
			return "error: forbidden", fmt.Errorf("exit status 1")
		}
		return inner.Run(ctx, name, args, env)
	})
	_, err = restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "prod_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget, PlanDigest: plan.PlanDigest})
	if err == nil || !strings.Contains(err.Error(), "stopping the instance before the restore") {
		t.Fatalf("err = %v", err)
	}
	// The plan's read-only pgBackRest reads run in maintenance pods while the
	// instance is down; the restore itself must not.
	for _, script := range f.maintScript {
		if strings.Contains(script, "restore") && strings.Contains(script, "--target-action=promote") {
			t.Errorf("the restore ran after the quiesce failed: %s", script)
		}
	}
}

func TestRestoreToPITTool_RecordsToolExecution(t *testing.T) {
	r := pitrRouter(t, true)
	old := cmdRunner
	cmdRunner = r
	defer func() { cmdRunner = old }()
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()
	digest := approvedDigest(t, pitrTestTarget)

	args := RestoreToPITArgs{Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget, PlanDigest: digest}
	if _, err := restoreToPITTool(newSysadminTestContext(), args); err != nil {
		t.Fatal(err)
	}
	args.PlanDigest = "0000000000000000"
	if _, err := restoreToPITTool(newSysadminTestContext(), args); err == nil {
		t.Fatal("a wrong digest must fail")
	}
	events, err := store.Query(context.Background(), audit.QueryOptions{ToolName: "restore_to_point_in_time", EventType: audit.EventTypeToolExecution})
	if err != nil || len(events) != 2 {
		t.Fatalf("want 2 audited calls (one ok, one refused), got %d: %v", len(events), err)
	}
	var okSeen, errSeen bool
	for _, e := range events {
		if e.Tool.Error == "" && strings.Contains(e.Tool.Result, digest) {
			okSeen = true
		}
		if strings.Contains(e.Tool.Error, "plan changed") {
			errSeen = true
		}
		if e.Tool.Parameters["plan_digest"] == nil {
			t.Errorf("the audited parameters lack the approved plan_digest: %v", e.Tool.Parameters)
		}
	}
	if !okSeen || !errSeen {
		t.Errorf("ok=%v refused=%v: the audit trail must record both the restore and the refusal", okSeen, errSeen)
	}
}

// runPITRScript runs pitrRestoreScript under sh against a temp PGDATA, with
// the config path redirected and a stub pgbackrest that records its
// arguments. It returns the combined output, the exit code and the dirs.
func runPITRScript(t *testing.T, setup func(pgdata string)) (out string, code int, pgdata, parent, calls string) {
	t.Helper()
	parent = t.TempDir()
	pgdata = parent + "/data"
	repo := parent + "/repo"
	bin := t.TempDir()
	for _, d := range []string{pgdata + "/pg_wal", repo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	conf := bin + "/pgbackrest.conf"
	if err := os.WriteFile(conf, []byte("[main]\npg1-path="+pgdata+"\nrepo1-path="+repo+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls = bin + "/calls"
	stub := "#!/bin/sh\necho \"$@\" >> " + calls + "\n"
	if err := os.WriteFile(bin+"/pgbackrest", []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	setup(pgdata)
	script := strings.ReplaceAll(pitrRestoreScript("pgbackrest --stanza='main' --delta restore"), "/etc/pgbackrest.conf", conf)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	b, err := cmd.CombinedOutput()
	out = string(b)
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out, code, pgdata, parent, calls
}

func TestPITRRestoreScript_RefusesAStandby(t *testing.T) {
	out, code, _, _, calls := runPITRScript(t, func(pgdata string) {
		os.WriteFile(pgdata+"/standby.signal", nil, 0o644)
		os.WriteFile(pgdata+"/pg_wal/000000010000000000000001", []byte("wal"), 0o644)
	})
	if code != 3 || !strings.Contains(out, "standby") {
		t.Errorf("exit %d, output %q: want exit 3 naming the standby", code, out)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Error("pgbackrest ran against a standby")
	}
}

func TestPITRRestoreScript_SavesWALAsideThenRestores(t *testing.T) {
	out, code, pgdata, parent, calls := runPITRScript(t, func(pgdata string) {
		os.WriteFile(pgdata+"/postmaster.pid", []byte("999999\n"), 0o644) // stale: no such process
		os.WriteFile(pgdata+"/pg_wal/000000010000000000000001", []byte("wal"), 0o644)
		os.WriteFile(pgdata+"/pg_wal/000000010000000000000002.partial", []byte("p"), 0o644)
		os.WriteFile(pgdata+"/pg_wal/00000001.history", []byte("h"), 0o644)
		os.MkdirAll(pgdata+"/pg_wal/archive_status", 0o755)
	})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	savedTo, _, _ := parseWALTailOutput(out)
	// The script resolves symlinks (pwd -P); so does the temp dir on macOS.
	realParent, _ := filepath.EvalSymlinks(parent)
	if savedTo == "" || !strings.HasPrefix(savedTo, realParent+"/pg_wal.saved-") {
		t.Fatalf("saved dir %q, want one beside PGDATA (outside it, so --delta cannot delete it):\n%s", savedTo, out)
	}
	if _, err := os.Stat(savedTo + "/000000010000000000000001"); err != nil {
		t.Errorf("the segment was not saved: %v", err)
	}
	for _, name := range []string{"000000010000000000000002.partial", "00000001.history"} {
		if _, err := os.Stat(pgdata + "/pg_wal/" + name); err != nil {
			t.Errorf("%s must stay where it is (only whole 24-hex segments move): %v", name, err)
		}
	}
	if _, err := os.Stat(pgdata + "/pg_wal/000000010000000000000001"); err == nil {
		t.Error("the segment is still in pg_wal: --delta would delete it")
	}
	if _, err := os.Stat(pgdata + "/postmaster.pid"); err == nil {
		t.Error("a stale postmaster.pid was not removed")
	}
	if b, err := os.ReadFile(calls); err != nil || !strings.Contains(string(b), "--delta restore") {
		t.Errorf("pgbackrest calls: %q, %v", b, err)
	}
	if entries, _ := os.ReadDir(savedTo); len(entries) != 1 {
		t.Errorf("saved dir holds %d entries, want only the one segment", len(entries))
	}
}

// A live postmaster (Linux: /proc/<pid>/comm says postgres) is never
// restored over, whatever pg_isready said.
func TestPITRRestoreScript_RefusesALivePostmaster(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc")
	}
	// A process whose comm is "postgres".
	bin := t.TempDir()
	if err := os.WriteFile(bin+"/postgres", []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := exec.Command(bin + "/postgres")
	if err := fake.Start(); err != nil {
		t.Fatal(err)
	}
	defer fake.Process.Kill()
	time.Sleep(200 * time.Millisecond)
	out, code, _, _, calls := runPITRScript(t, func(pgdata string) {
		os.WriteFile(pgdata+"/postmaster.pid", []byte(fmt.Sprintf("%d\n", fake.Process.Pid)), 0o644)
	})
	if comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", fake.Process.Pid)); strings.TrimSpace(string(comm)) != "postgres" {
		t.Skipf("could not make a process named postgres (comm %q)", comm)
	}
	if code != 4 || !strings.Contains(out, "is running") {
		t.Errorf("exit %d, output %q: want exit 4", code, out)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Error("pgbackrest ran with a postmaster alive")
	}
}

// Over real SSH (a systemd VM): the plan's reads and the restore script both
// reach the host, the script as one `sh -c` with the pinned flags. The
// segment-header read is refused here, so the plan falls back to the
// backups' LSNs for the segment size, which is the path a host without
// readable header segments takes.
func TestRestoreToPIT_SSHDispatch(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	var scripts []string
	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		switch {
		case strings.Contains(cmd, "--target-action=promote"):
			scripts = append(scripts, cmd)
			return walTailSavedMarker + "/var/lib/postgresql/pg_wal.saved-20261027T150000Z\nrestore command end: completed successfully\n", 0
		case strings.Contains(cmd, "pg_isready"):
			return "no response", 1
		case strings.Contains(cmd, "'info'"):
			return readFixture(t, "multi_timeline_info.json"), 0
		case strings.Contains(cmd, ".history"):
			return readFixture(t, "multi_timeline_0000001E.history"), 0
		case strings.Contains(cmd, "repo-get"):
			return "unable to read", 1
		case strings.Contains(cmd, "repo-ls") && strings.Contains(cmd, "--recurse"):
			return readFixture(t, "multi_timeline_listing.json"), 0
		case strings.Contains(cmd, "repo-ls"):
			return readFixture(t, "multi_timeline_top.txt"), 0
		}
		return "unexpected command: " + cmd, 1
	})
	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck
	addr, port := hostPortOf(t, srv.addr)
	withPgBackRestSSHInfra(t, addr, port, keyPath)

	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("plan over SSH: %+v, %v", plan, err)
	}
	if plan.WALContinuity.SegmentSizeSource == SegmentSizeFromWALHeader {
		t.Errorf("segment size came from a header the server refused to serve")
	}
	res, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: pitrTestTarget, PlanDigest: plan.PlanDigest})
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 1 || !strings.Contains(scripts[0], "--set=") || !strings.Contains(scripts[0], "--target-timeline=30") {
		t.Fatalf("scripts sent over SSH: %v", scripts)
	}
	if res.SavedWAL != "/var/lib/postgresql/pg_wal.saved-20261027T150000Z" || res.Quiesced != "" {
		t.Errorf("result = %+v: want the saved path and no quiesce on a systemd host", res)
	}
}

// The approved digest does not depend on how far the archive has grown: a
// live instance keeps archiving between the plan and the restore.
func TestPITRPlanDigest_IgnoresArchiveGrowth(t *testing.T) {
	b := PgBackRestBackup{Label: "L", StopTime: time.Date(2026, 10, 7, 4, 34, 37, 0, time.UTC)}
	tgt := RecoveryTarget{TargetTime, "2026-10-09T00:00:00Z"}
	mk := func(endWAL, endTime string) *WALContinuity {
		return &WALContinuity{Status: WALContinuous, LatestTimeline: 30, ArchiveEndWAL: endWAL, ArchiveEndTime: endTime, SegmentSizeBytes: 16 << 20}
	}
	a := assessPITRPlan("main", tgt, "2026-10-09 00:00:00+00", b, mk("0000001E0000000000000005", "2026-10-11T20:30:09Z"))
	later := assessPITRPlan("main", tgt, "2026-10-09 00:00:00+00", b, mk("0000001E0000000000000099", "2026-10-12T08:00:00Z"))
	if a.Status != PITRFeasible || later.Status != PITRFeasible || a.PlanDigest != later.PlanDigest {
		t.Errorf("digests differ as the archive grew: %s vs %s", a.PlanDigest, later.PlanDigest)
	}
	// ...but a different backup, target or timeline is a different decision.
	other := mk("0000001E0000000000000005", "2026-10-11T20:30:09Z")
	other.LatestTimeline = 31
	for name, p := range map[string]PITRPlan{
		"timeline": assessPITRPlan("main", tgt, "2026-10-09 00:00:00+00", b, other),
		"backup":   assessPITRPlan("main", tgt, "2026-10-09 00:00:00+00", PgBackRestBackup{Label: "M", StopTime: b.StopTime}, mk("0000001E0000000000000005", "2026-10-11T20:30:09Z")),
		"stanza":   assessPITRPlan("other", tgt, "2026-10-09 00:00:00+00", b, mk("0000001E0000000000000005", "2026-10-11T20:30:09Z")),
	} {
		if p.PlanDigest == a.PlanDigest {
			t.Errorf("a different %s kept the same digest", name)
		}
	}
}

// With no pg_wal (the data directory is what was lost) the script has
// nothing to save and still restores; on Kubernetes the saved copy goes to
// the repo volume, because the one-off pod's own filesystem goes with it.
func TestPITRRestoreScript_NoPgWAL_AndKubernetesSaveLocation(t *testing.T) {
	out, code, _, _, calls := runPITRScript(t, func(pgdata string) { os.RemoveAll(pgdata + "/pg_wal") })
	if code != 0 || strings.Contains(out, walTailSavedMarker) {
		t.Errorf("exit %d, output %q: want a restore and no saved dir", code, out)
	}
	if b, err := os.ReadFile(calls); err != nil || !strings.Contains(string(b), "--delta restore") {
		t.Errorf("pgbackrest was not run: %q %v", b, err)
	}

	t.Setenv("HELPDESK_WAL_SAVE_IN_REPO", "1")
	out, code, _, parent, _ := runPITRScript(t, func(pgdata string) {
		os.WriteFile(pgdata+"/pg_wal/000000010000000000000001", []byte("wal"), 0o644)
	})
	savedTo, _, _ := parseWALTailOutput(out)
	// The repo path is read from the config as written, not resolved.
	if code != 0 || !strings.HasPrefix(savedTo, parent+"/repo/pg_wal.saved-") {
		t.Errorf("exit %d, saved to %q: want it under the repo path", code, savedTo)
	}
}

// A PITR log with no prediction on record (the agent restarted since the
// restore) is reported as unverified, never as a pass.
func TestVerifyRestoreRecovery_PITRWithoutPrediction(t *testing.T) {
	log := pitrFixture(t, "time_stop_before_promote.log")
	st, _ := compareRecovery(nil, parseRecoveryLog(log))
	if st != RestoreUnverified {
		t.Errorf("status = %s, want unverified", st)
	}
}
