package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// The live path's scripts run on the instance, under `su postgres -c`, in a
// maintenance pod, or through ssh. These tests run them in a real shell, with a
// stub psql that behaves like an instance and its archiver, so the shell logic
// (and its failure branches) is exercised, not just the output the Go side
// expects. Each script is run twice: directly, and wrapped in the quoting ssh
// uses (shellCommand), which is the form a systemd host receives it in.

const stubPsql = `#!/bin/sh
for a; do sql="$a"; done
D="$STUB_DIR"
case "$sql" in
 *"SHOW data_directory"*) echo "$STUB_PGDATA";;
 *failed_count*)
   n=$(cat "$D/calls" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$D/calls"
   if [ -n "$STUB_ARCHIVE_AFTER" ] && [ "$n" -ge "$STUB_ARCHIVE_AFTER" ]; then rm -f "$STUB_PGDATA"/pg_wal/archive_status/*.ready; fi
   if [ -n "$STUB_FAIL_AFTER" ] && [ "$n" -ge "$STUB_FAIL_AFTER" ]; then echo 3; else echo 0; fi;;
 *pg_switch_wal*) [ -n "$STUB_READY" ] && touch "$STUB_PGDATA/pg_wal/archive_status/000000010000000000000001.ready"; echo "0/1000000";;
 *pg_is_in_recovery*) echo f;;
 *"count(*) FROM pg_stat_replication"*) echo 0;;
 *archive_mode*) echo on;;
 *timeline_id*) echo 30;;
 *pg_current_wal_lsn*) echo 0/20000000;;
 *clock_timestamp*) echo 2026-10-28T10:00:00Z;;
esac
`

// runLiveScript runs script with the stub psql first on PATH. viaSSH wraps it
// the way runOnHost sends it to a systemd host.
func runLiveScript(t *testing.T, script string, viaSSH bool, env ...string) (string, int) {
	t.Helper()
	bin, stub, pgdata := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(pgdata, "pg_wal", "archive_status"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "psql"), []byte(stubPsql), 0o755); err != nil {
		t.Fatal(err)
	}
	command := script
	var cmd *exec.Cmd
	if viaSSH {
		cmd = exec.Command("sh", "-c", shellCommand("sh", []string{"-c", command}, nil))
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	cmd.Env = append(os.Environ(), append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "STUB_DIR=" + stub, "STUB_PGDATA=" + pgdata}, env...)...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestFlushArchiveScript_RealShell(t *testing.T) {
	for _, viaSSH := range []bool{false, true} {
		name := map[bool]string{false: "direct", true: "via ssh quoting"}[viaSSH]
		t.Run(name, func(t *testing.T) {
			cases := []struct {
				name     string
				timeout  time.Duration
				env      []string
				wantOut  string
				wantCode int
			}{
				{"nothing was waiting", 3 * time.Second, nil, "flush=done", 0},
				{"the segment is archived while we wait", 6 * time.Second, []string{"STUB_READY=1", "STUB_ARCHIVE_AFTER=3"}, "flush=done", 0},
				{"the archiver never catches up", 2 * time.Second, []string{"STUB_READY=1"}, "flush=timeout segments_still_waiting=1", 6},
				{"the archiver starts failing", 6 * time.Second, []string{"STUB_READY=1", "STUB_FAIL_AFTER=2"}, "flush=failed archiver_failed_count=0->3", 6},
			}
			for _, c := range cases {
				start := time.Now()
				out, code := runLiveScript(t, flushArchiveScript(c.timeout), viaSSH, c.env...)
				if code != c.wantCode || !strings.Contains(out, c.wantOut) {
					t.Errorf("%s: exit %d, output %q; want exit %d containing %q", c.name, code, strings.TrimSpace(out), c.wantCode, c.wantOut)
				}
				if c.wantCode == 6 && c.name == "the archiver starts failing" && time.Since(start) > 4*time.Second {
					t.Errorf("%s: took %s; a failing archiver must end the wait at once", c.name, time.Since(start))
				}
			}
		})
	}
}

func TestLivePreflightScript_RealShell(t *testing.T) {
	for _, viaSSH := range []bool{false, true} {
		out, code := runLiveScript(t, livePreflightScript, viaSSH)
		if code != 0 {
			t.Fatalf("viaSSH=%v: exit %d: %s", viaSSH, code, out)
		}
		p, err := parseLivePreState(out)
		if err != nil {
			t.Fatalf("viaSSH=%v: the real shell's output does not parse: %v\n%s", viaSSH, err, out)
		}
		// The stub answers the instance's questions; the HA check reads the
		// machine's own process table, so it is not asserted here.
		if p.InRecovery || p.Replicas != 0 || p.ArchiveMode != "on" || p.Timeline != 30 || p.LSN != "0/20000000" || p.Time != "2026-10-28T10:00:00Z" || p.DataDirectory == "" {
			t.Errorf("viaSSH=%v: parsed %+v", viaSSH, p)
		}
	}
}

// The restore script through ssh's quoting, with a stub pgbackrest: it parses,
// refuses a standby with the marker and its exit status, and otherwise saves
// the WAL aside and restores.
func TestPITRRestoreScript_ThroughSSHQuoting(t *testing.T) {
	for _, standby := range []bool{false, true} {
		parent := t.TempDir()
		pgdata, repo, bin := parent+"/data", parent+"/repo", t.TempDir()
		for _, d := range []string{pgdata + "/pg_wal", repo} {
			os.MkdirAll(d, 0o755) //nolint:errcheck
		}
		os.WriteFile(pgdata+"/pg_wal/000000010000000000000001", []byte("wal"), 0o644) //nolint:errcheck
		if standby {
			os.WriteFile(pgdata+"/standby.signal", nil, 0o644) //nolint:errcheck
		}
		conf := bin + "/pgbackrest.conf"
		os.WriteFile(conf, []byte("[main]\npg1-path="+pgdata+"\nrepo1-path="+repo+"\n"), 0o644)          //nolint:errcheck
		os.WriteFile(bin+"/pgbackrest", []byte("#!/bin/sh\necho \"ran: $@\" >> "+bin+"/calls\n"), 0o755) //nolint:errcheck
		script := strings.ReplaceAll(pitrRestoreScript(pitrRestoreCommand("main", "20261007-043435F",
			RecoveryTarget{TargetTime, "2026-10-09T00:00:00Z"}, "2026-10-09 00:00:00+00", 30)), "/etc/pgbackrest.conf", conf)
		cmd := exec.Command("sh", "-c", shellCommand("sh", []string{"-c", script}, nil))
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		calls, _ := os.ReadFile(bin + "/calls")
		if standby {
			if code != 3 || !strings.Contains(string(out), pitrRefusedMarker) || len(calls) != 0 {
				t.Errorf("standby: exit %d, out %q, pgbackrest calls %q", code, out, calls)
			}
			continue
		}
		if code != 0 || !strings.Contains(string(calls), "--target-action=promote restore") && !strings.Contains(string(calls), "--target-action=promote") {
			t.Errorf("restore: exit %d, out %q, calls %q", code, out, calls)
		}
		if saved, _, _ := parseWALTailOutput(string(out)); saved == "" {
			t.Errorf("the saved WAL directory was not reported through the quoting: %q", out)
		}
	}
}

// The model-facing wrapper hands the operator's allow_stop_serving to the live
// path, and only that: an approval without it leaves a serving instance alone.
func TestRestoreToPITTool_StopServingComesOnlyFromTheApproval(t *testing.T) {
	for _, allow := range []bool{true, false} {
		withPgBackRestDockerInfra(t)
		sim, _ := liveEnv(t)
		plan := approvedLiveDigest(t, liveTarget)
		ap := toolCtxWithApproval(approvalFor(plan, allow))
		_, err := restoreToPITTool(ap, RestoreToPITModelArgs{Target: "pgbackrest_db", Stanza: "main"})
		if allow {
			if err != nil || !sim.did("stop") || !sim.did("restore") {
				t.Errorf("allowed: err=%v order=%s", err, sim.order())
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "allow_stop_serving") || sim.order() != "" || !sim.up {
			t.Errorf("not allowed: err=%v order=%q up=%v", err, sim.order(), sim.up)
		}
	}
	_ = context.Background
}

func approvalFor(plan PITRPlan, stopServing bool) *audit.RecoveryApproval {
	return &audit.RecoveryApproval{Type: TargetTime, Value: liveTarget, PlanDigest: plan.PlanDigest, ApprovedBy: "alice", Acknowledged: true, StopServing: stopServing}
}
