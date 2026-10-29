package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── a simulated serving instance ─────────────────────────────────────────────
//
// The live path (pitr_live.go) is a sequence in which only the order keeps the
// database safe: ask it about itself, flush its WAL to the archive, plan from
// the repo again, and only then stop it. These tests drive the whole sequence
// against a simulation of a running instance and check, for every way a step
// can fail, what was left running or stopped.

// liveTarget is after the multi_timeline fixture's archive ends (2026-10-11)
// and before now: a recent target on a serving instance, in WAL that has not
// been archived yet.
const liveTarget = "2026-10-20T00:00:00Z"

const healthyPre = "in_recovery=f\nreplicas=0\narchive_mode=on\ndata_directory=/var/lib/postgresql/data\ntimeline=30\nlsn=0/20000000\ntime=2026-10-28T10:00:00Z\nha_manager=none\n"

type liveSim struct {
	mu    sync.Mutex
	calls []string
	up    bool // PostgreSQL accepts connections
	grown bool // the flush put the target's WAL in the archive

	pre, flushOut   string
	preErr, flushEr error
	flushGrows      bool
	stopErr         error
	stillUpAfter    bool
	restoreOut      string
	restoreErr      error
	startErr        error
}

func newLiveSim() *liveSim {
	return &liveSim{
		up: true, pre: healthyPre, flushOut: "flush=done\n", flushGrows: true,
		restoreOut: walTailSavedMarker + "/var/lib/postgresql/pg_wal.saved-20261028T100500Z\nrestore command end: completed successfully\n",
	}
}

func (s *liveSim) log(label string) {
	s.mu.Lock()
	s.calls = append(s.calls, label)
	s.mu.Unlock()
}

// order is the steps in the order they ran. A Kubernetes restore is seen twice
// (the kubectl run call and its script), so repeats collapse.
func (s *liveSim) order() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if len(out) == 0 || out[len(out)-1] != c {
			out = append(out, c)
		}
	}
	return strings.Join(out, ",")
}

func (s *liveSim) did(label string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c == label {
			return true
		}
	}
	return false
}

func (s *liveSim) hook(joined string) (string, error, bool) {
	switch {
	case strings.Contains(joined, "--target-action=promote"):
		s.log("restore")
		return s.restoreOut, s.restoreErr, true
	case strings.Contains(joined, "inspect -f"):
		s.log("inspect")
		return "docker-postgres-pgbackrest:latest\n", nil, true
	case strings.HasPrefix(joined, "docker stop"):
		s.log("stop")
		if s.stopErr == nil && !s.stillUpAfter {
			s.mu.Lock()
			s.up = false
			s.mu.Unlock()
		}
		return "", s.stopErr, true
	case strings.HasPrefix(joined, "docker start"):
		s.log("start")
		return "", s.startErr, true
	case strings.Contains(joined, "in_recovery="):
		s.log("preflight")
		return s.pre, s.preErr, true
	case strings.Contains(joined, "flush=timeout"):
		s.log("flush")
		if s.flushEr == nil && s.flushGrows {
			s.mu.Lock()
			s.grown = true
			s.mu.Unlock()
		}
		return s.flushOut, s.flushEr, true
	case strings.Contains(joined, "stop -m fast"):
		s.log("cleanstop")
		return "", nil, true
	case strings.Contains(joined, "pg_isready"):
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.up {
			return "accepting connections\n", nil, true
		}
		return "no response\n", fmt.Errorf("exit status 2"), true
	}
	return "", nil, false
}

// growTo is the archive after the flush: the newest segment on the newest
// timeline was archived just now.
func growTo(listing string, epoch int64) string {
	var files map[string]map[string]any
	if err := json.Unmarshal([]byte(listing), &files); err != nil {
		return listing
	}
	for k, v := range files {
		if v["type"] == "file" && strings.HasPrefix(k, "0000001E") {
			v["time"] = epoch
		}
	}
	b, _ := json.Marshal(files)
	return string(b)
}

// liveEnv wires the simulation into a pgbackrest router and shortens every wait.
func liveEnv(t *testing.T) (*liveSim, *pgbackrestRouter) {
	t.Helper()
	sim := newLiveSim()
	r := pitrRouter(t, false)
	wal := fakeWALRun(t, readFixture(t, "multi_timeline_000000010000000000000002.gz"), nil)
	r.repoRun = func(args []string) (string, error) {
		out, err := wal(args)
		sim.mu.Lock()
		grown := sim.grown
		sim.mu.Unlock()
		if grown && strings.Contains(strings.Join(args, " "), "--recurse") {
			out = growTo(out, time.Now().Add(-time.Minute).Unix())
		}
		return out, err
	}
	r.custom = sim.hook

	oldA, oldP, oldI, oldS, oldC := archiveFlushTimeout, planAfterFlushTimeout, planPollInterval, stopConfirmWait, containerStopTimeout
	archiveFlushTimeout, planAfterFlushTimeout, planPollInterval, stopConfirmWait, containerStopTimeout = time.Second, 60*time.Millisecond, 5*time.Millisecond, 30*time.Millisecond, time.Second
	t.Cleanup(func() {
		archiveFlushTimeout, planAfterFlushTimeout, planPollInterval, stopConfirmWait, containerStopTimeout = oldA, oldP, oldI, oldS, oldC
	})
	old := cmdRunner
	cmdRunner = r
	t.Cleanup(func() { cmdRunner = old })
	return sim, r
}

// approvedLiveDigest is what the human sees and approves before the restore:
// the plan of the serving instance, whose target is not archived yet.
func approvedLiveDigest(t *testing.T, target string) PITRPlan {
	t.Helper()
	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: target})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("plan: %+v, %v", plan, err)
	}
	return plan
}

func liveArgs(plan PITRPlan) RestoreToPITArgs {
	return RestoreToPITArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget,
		PlanDigest: plan.PlanDigest, AllowStopServing: true}
}

// ── the plan of a serving instance ───────────────────────────────────────────

func TestPlanPITR_ServingInstance_TargetInUnarchivedWAL(t *testing.T) {
	withPgBackRestDockerInfra(t)
	sim, _ := liveEnv(t)

	plan := approvedLiveDigest(t, liveTarget)
	if !plan.InstanceServing || !plan.ArchiveFlushRequired || !strings.Contains(plan.Detail, "flushes the archive first") || plan.PlanDigest == "" {
		t.Errorf("serving plan = %+v", plan)
	}
	// A target inside the archive needs no flush.
	inside := approvedLiveDigest(t, "2026-10-09T00:00:00Z")
	if !inside.InstanceServing || inside.ArchiveFlushRequired {
		t.Errorf("a target inside the archive: %+v", inside)
	}
	// The same target on a stopped instance is not plausible: nothing flushes it.
	sim.up = false
	down, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || down.Status != PITRNotFeasible || down.InstanceServing {
		t.Errorf("down instance, target past the archive end: %+v, %v", down, err)
	}
	// The digest is the decision, not the instance's state.
	if plan.PlanDigest == "" || plan.PlanDigest == inside.PlanDigest {
		t.Errorf("digests: %s vs %s (different targets must differ)", plan.PlanDigest, inside.PlanDigest)
	}
}

// ── the live restore ─────────────────────────────────────────────────────────

func TestRestoreToPIT_Live_Docker_OrderOfOperations(t *testing.T) {
	withPgBackRestDockerInfra(t)
	sim, r := liveEnv(t)
	plan := approvedLiveDigest(t, liveTarget)

	res, err := restoreToPITImpl(context.Background(), liveArgs(plan))
	if err != nil {
		t.Fatalf("restoreToPITImpl: %v", err)
	}
	// Ask, flush, stop (clean, then the container), then the restore in a
	// throwaway container; the instance is never started by this tool.
	if got, want := sim.order(), "preflight,flush,cleanstop,stop,inspect,restore"; got != want {
		t.Errorf("order of operations = %s, want %s", got, want)
	}
	if res.PreStop == nil || res.PreStop.LSN != "0/20000000" || res.PreStop.Timeline != 30 {
		t.Errorf("pre_stop = %+v", res.PreStop)
	}
	if !strings.Contains(res.Stopped, "stopped") || !strings.Contains(res.Stopped, "restart_container") {
		t.Errorf("stopped = %q: want it to say the instance was stopped and how to start it", res.Stopped)
	}
	if res.Plan.ArchiveFlushRequired || res.Plan.PlanDigest != plan.PlanDigest {
		t.Errorf("the plan the restore ran: %+v (approved digest %s)", res.Plan, plan.PlanDigest)
	}
	if res.Prediction == nil || res.Prediction.TargetValue != "2026-10-20 00:00:00+00" || res.SavedWAL == "" {
		t.Errorf("result = %+v", res)
	}
	// The restore ran in a throwaway container on the stopped one's volumes.
	var run string
	for _, c := range r.callsContaining("--volumes-from") {
		run = strings.Join(c, " ")
	}
	if !strings.Contains(run, "run --rm --volumes-from helpdesk-test-pg-pgbackrest --entrypoint su docker-postgres-pgbackrest:latest postgres -c") {
		t.Errorf("restore command = %q", run)
	}
}

func TestRestoreToPIT_Live_Refusals_LeaveTheInstanceRunning(t *testing.T) {
	type tc struct {
		name   string
		setup  func(s *liveSim)
		mutate func(a *RestoreToPITArgs)
		want   string
		// reached is the last step that may have run; nothing after it may have.
		reached string
	}
	cases := []tc{
		{"not authorized to stop it", nil, func(a *RestoreToPITArgs) { a.AllowStopServing = false }, "allow_stop_serving", ""},
		{"the instance cannot be asked", func(s *liveSim) { s.preErr = fmt.Errorf("psql: connection refused") }, nil, "asking the running instance", "preflight"},
		{"garbled answer", func(s *liveSim) { s.pre = "in_recovery=f\n" }, nil, "did not report", "preflight"},
		{"a standby", func(s *liveSim) { s.pre = strings.Replace(healthyPre, "in_recovery=f", "in_recovery=t", 1) }, nil, "standby", "preflight"},
		{"replicas attached", func(s *liveSim) { s.pre = strings.Replace(healthyPre, "replicas=0", "replicas=2", 1) }, nil, "2 replica(s)", "preflight"},
		{"archiving off", func(s *liveSim) { s.pre = strings.Replace(healthyPre, "archive_mode=on", "archive_mode=off", 1) }, nil, "archive_mode", "preflight"},
		{"an HA manager", func(s *liveSim) { s.pre = strings.Replace(healthyPre, "ha_manager=none", "ha_manager=running", 1) }, nil, "HA manager", "preflight"},
		{"the database clock is behind the target", func(s *liveSim) {
			s.pre = strings.Replace(healthyPre, "2026-10-28T10:00:00Z", "2026-10-19T00:00:00Z", 1)
		}, nil, "ahead of the database's own clock", "preflight"},
		{"the flush fails", func(s *liveSim) {
			s.flushEr, s.flushOut = fmt.Errorf("exit status 6"), "flush=failed archiver_failed_count=0->3\n"
		}, nil, "could not confirm the archive was flushed", "flush"},
		{"the flush times out", func(s *liveSim) {
			s.flushEr, s.flushOut = fmt.Errorf("exit status 6"), "flush=timeout segments_still_waiting=2\n"
		}, nil, "segments_still_waiting=2", "flush"},
		{"the flush is reported done but the WAL never reaches the repo", func(s *liveSim) { s.flushGrows = false }, nil, "did not reach the archive", "flush"},
		{"the plan was approved for another target", nil, func(a *RestoreToPITArgs) { a.PlanDigest = "0123456789abcdef" }, "plan changed since it was approved", "flush"},
	}
	for _, c := range cases {
		withPgBackRestDockerInfra(t)
		sim, _ := liveEnv(t)
		plan := approvedLiveDigest(t, liveTarget)
		if c.setup != nil {
			c.setup(sim)
		}
		args := liveArgs(plan)
		if c.mutate != nil {
			c.mutate(&args)
		}
		_, err := restoreToPITImpl(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want)
		}
		for _, after := range []string{"cleanstop", "stop", "inspect", "restore", "start"} {
			if sim.did(after) {
				t.Errorf("%s: %q ran after a refusal (order %s)", c.name, after, sim.order())
			}
		}
		if !sim.up {
			t.Errorf("%s: the instance was taken down", c.name)
		}
		if c.reached == "" && sim.order() != "" {
			t.Errorf("%s: %s ran before the authorization was checked", c.name, sim.order())
		}
		if c.reached == "preflight" && sim.did("flush") {
			t.Errorf("%s: the archive was flushed after a preflight refusal (order %s)", c.name, sim.order())
		}
		if c.reached != "" && !strings.Contains(err.Error(), "not stopped") && c.want != "plan changed since it was approved" {
			t.Errorf("%s: the error should say the instance was not stopped: %v", c.name, err)
		}
	}
}

func TestRestoreToPIT_Live_StopFailures(t *testing.T) {
	t.Run("docker stop fails", func(t *testing.T) {
		withPgBackRestDockerInfra(t)
		sim, _ := liveEnv(t)
		plan := approvedLiveDigest(t, liveTarget)
		sim.stopErr = fmt.Errorf("exit status 1")
		_, err := restoreToPITImpl(context.Background(), liveArgs(plan))
		if err == nil || !strings.Contains(err.Error(), "may still be running") {
			t.Fatalf("err = %v", err)
		}
		if sim.did("restore") || sim.did("start") {
			t.Errorf("order %s: the restore ran, or the instance was started, after a failed stop", sim.order())
		}
	})
	t.Run("the instance still answers after the stop", func(t *testing.T) {
		withPgBackRestDockerInfra(t)
		sim, _ := liveEnv(t)
		plan := approvedLiveDigest(t, liveTarget)
		sim.stillUpAfter = true
		_, err := restoreToPITImpl(context.Background(), liveArgs(plan))
		if err == nil || !strings.Contains(err.Error(), "still accepting connections after it was stopped") || !strings.Contains(err.Error(), "restore was not run") {
			t.Fatalf("err = %v", err)
		}
		if sim.did("restore") {
			t.Errorf("order %s: the restore ran over a database that was still answering", sim.order())
		}
	})
}

// After the stop, a refusal by the script (before it changed anything) puts
// the instance back; a failure of pgBackRest itself does not.
func TestRestoreToPIT_Live_AfterTheStop(t *testing.T) {
	refusal := pitrRefusedMarker + "not enough room beside the data directory to keep the WAL that is in pg_wal (2000000 KB to save, 1000 KB free in /var/lib)\n"

	t.Run("the script refuses: the instance is started again", func(t *testing.T) {
		withPgBackRestDockerInfra(t)
		sim, _ := liveEnv(t)
		plan := approvedLiveDigest(t, liveTarget)
		sim.restoreOut, sim.restoreErr = refusal, fmt.Errorf("exit status 5")
		res, err := restoreToPITImpl(context.Background(), liveArgs(plan))
		if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "not enough room") || !strings.Contains(err.Error(), "started again") {
			t.Fatalf("err = %v", err)
		}
		if !strings.HasSuffix(sim.order(), "restore,start") {
			t.Errorf("order %s: want the instance started after the refusal", sim.order())
		}
		if res.Prediction != nil {
			t.Error("a prediction was recorded for a restore that did not run")
		}
	})
	t.Run("the script refuses and the instance cannot be started", func(t *testing.T) {
		withPgBackRestDockerInfra(t)
		sim, _ := liveEnv(t)
		plan := approvedLiveDigest(t, liveTarget)
		sim.restoreOut, sim.restoreErr, sim.startErr = refusal, fmt.Errorf("exit status 5"), fmt.Errorf("exit status 1")
		res, err := restoreToPITImpl(context.Background(), liveArgs(plan))
		if err == nil || !strings.Contains(err.Error(), "starting the instance again failed") {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(res.HowToRecover, "restart_container") {
			t.Errorf("how_to_recover = %q: it must say how to start the instance", res.HowToRecover)
		}
	})
	t.Run("pgBackRest fails: the instance stays stopped and the WAL is named", func(t *testing.T) {
		withPgBackRestDockerInfra(t)
		sim, _ := liveEnv(t)
		plan := approvedLiveDigest(t, liveTarget)
		sim.restoreOut = walTailSavedMarker + "/var/lib/postgresql/pg_wal.saved-20261028T100500Z\nERROR: [038]: unable to restore\n"
		sim.restoreErr = fmt.Errorf("exit status 38")
		res, err := restoreToPITImpl(context.Background(), liveArgs(plan))
		if err == nil || !strings.Contains(err.Error(), "unable to restore") || !strings.Contains(err.Error(), "still stopped") || !strings.Contains(err.Error(), "pg_wal.saved-20261028T100500Z") {
			t.Fatalf("err = %v", err)
		}
		if sim.did("start") {
			t.Errorf("order %s: the instance was started over a half-restored data directory", sim.order())
		}
		if res.SavedWAL == "" || res.Prediction != nil || res.Stopped == "" {
			t.Errorf("result = %+v", res)
		}
	})
}

// A colocated deployment (no infrastructure config) has nothing that could
// keep it down and bring it back: it is refused before anything is touched.
func TestRestoreToPIT_Live_ColocatedIsRefusedFirst(t *testing.T) {
	infraConfig = nil
	sim, _ := liveEnv(t)
	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("plan: %+v, %v", plan, err)
	}
	_, err = restoreToPITImpl(context.Background(), RestoreToPITArgs{Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget,
		PlanDigest: plan.PlanDigest, AllowStopServing: true})
	if err == nil || !strings.Contains(err.Error(), "colocated") {
		t.Fatalf("err = %v", err)
	}
	if sim.did("preflight") || sim.did("flush") {
		t.Errorf("order %s: a colocated instance was probed and flushed before being refused", sim.order())
	}
}

// A down instance is not affected by any of this: no probing, no flush, no
// stop, and the allow_stop_serving flag is ignored.
func TestRestoreToPIT_Down_DoesNotTouchTheLivePath(t *testing.T) {
	withPgBackRestDockerInfra(t)
	sim, _ := liveEnv(t)
	sim.up = false
	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("plan: %+v, %v", plan, err)
	}
	args := RestoreToPITArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z",
		PlanDigest: plan.PlanDigest, AllowStopServing: true}
	res, err := restoreToPITImpl(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if got := sim.order(); got != "restore" {
		t.Errorf("order of operations = %q, want only the restore", got)
	}
	if res.PreStop != nil || res.Stopped != "" {
		t.Errorf("result = %+v: nothing was stopped", res)
	}
}

// ── Kubernetes ───────────────────────────────────────────────────────────────

func TestRestoreToPIT_Live_Kubernetes(t *testing.T) {
	sim := newLiveSim()
	r := pitrRouter(t, false)
	wal := fakeWALRun(t, readFixture(t, "multi_timeline_000000010000000000000002.gz"), nil)
	r.repoRun = func(args []string) (string, error) {
		out, err := wal(args)
		sim.mu.Lock()
		grown := sim.grown
		sim.mu.Unlock()
		if grown && strings.Contains(strings.Join(args, " "), "--recurse") {
			out = growTo(out, time.Now().Add(-time.Minute).Unix())
		}
		return out, err
	}
	// The pod's state is the cluster fake's; the simulation answers only the
	// scripts (the preflight, the flush, the clean stop, the restore).
	r.custom = func(joined string) (string, error, bool) {
		if strings.Contains(joined, "pg_isready") {
			return "accepting connections\n", nil, true
		}
		return sim.hook(joined)
	}
	oldA, oldP, oldI := archiveFlushTimeout, planAfterFlushTimeout, planPollInterval
	archiveFlushTimeout, planAfterFlushTimeout, planPollInterval = time.Second, 60*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { archiveFlushTimeout, planAfterFlushTimeout, planPollInterval = oldA, oldP, oldI })

	f := newFakeK8sCluster(r, true)
	withFakeK8sCluster(t, f)
	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "prod_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || plan.Status != PITRFeasible || !plan.ArchiveFlushRequired || !plan.InstanceServing {
		t.Fatalf("plan: %+v, %v", plan, err)
	}

	res, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "prod_db", Stanza: "main", RecoveryTargetType: TargetTime,
		RecoveryTargetVal: liveTarget, PlanDigest: plan.PlanDigest, AllowStopServing: true})
	if err != nil {
		t.Fatalf("restoreToPITImpl: %v", err)
	}
	if got := sim.order(); got != "preflight,flush,cleanstop,restore" {
		t.Errorf("order of operations = %s", got)
	}
	// The clean stop came before the scale-down, and both before the restore pod.
	cleanAt, scaleAt, runAt := -1, -1, -1
	for i, c := range f.calls {
		j := strings.Join(c, " ")
		switch {
		case strings.Contains(j, "stop -m fast") && cleanAt < 0:
			cleanAt = i
		case containsSeq(c, "scale", "statefulset") && scaleAt < 0:
			scaleAt = i
		case containsSeq(c, "run") && strings.Contains(f.maintPods[c[1]], "--target-action=promote") && runAt < 0:
			runAt = i
		}
	}
	if cleanAt < 0 || cleanAt >= scaleAt || scaleAt >= runAt {
		t.Errorf("clean stop at %d, scale-down at %d, restore pod at %d: want that order", cleanAt, scaleAt, runAt)
	}
	if f.replicas != 0 || !strings.Contains(res.Quiesced, "restart_container") || res.PreStop == nil {
		t.Errorf("replicas %d, quiesced %q, pre_stop %+v", f.replicas, res.Quiesced, res.PreStop)
	}
}

// ── systemd over SSH ─────────────────────────────────────────────────────────

// A systemd host: the instance is stopped with `sudo systemctl stop <unit>`
// (a grant the VM's sudoers file has to carry), then restored over the same
// SSH connection; this tool never starts it. If the script refuses before
// changing anything, `sudo systemctl restart` starts it again (the grant that
// already exists for restart_service).
func TestRestoreToPIT_Live_Systemd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refuse  bool
		wantEnd string
	}{
		{"restore succeeds", false, "preflight,flush,stop,restore"},
		{"the script refuses", true, "preflight,flush,stop,restore,start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")
			sim := newLiveSim()
			if tc.refuse {
				sim.restoreOut, sim.restoreErr = pitrRefusedMarker+"postmaster 4321 is running\n", fmt.Errorf("exit status 4")
			}
			wal := fakeWALRun(t, "", fmt.Errorf("unable to read"))
			var cmds []string
			srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
				sim.mu.Lock()
				cmds = append(cmds, cmd)
				sim.mu.Unlock()
				switch {
				case strings.Contains(cmd, "'systemctl' 'stop' 'postgresql-16'"):
					sim.log("stop")
					sim.mu.Lock()
					sim.up = false
					sim.mu.Unlock()
					return "", 0
				case strings.Contains(cmd, "'systemctl' 'restart' 'postgresql-16'"):
					sim.log("start")
					return "", 0
				}
				if out, err, ok := sim.hook(cmd); ok {
					if err != nil {
						return out, 1
					}
					return out, 0
				}
				switch {
				case strings.Contains(cmd, "'info'"):
					return readFixture(t, "multi_timeline_info.json"), 0
				case strings.Contains(cmd, ".history"), strings.Contains(cmd, "repo-get"), strings.Contains(cmd, "--recurse"), strings.Contains(cmd, "repo-ls"):
					out, err := wal([]string{cmd})
					if err != nil {
						return "unable to read", 1
					}
					sim.mu.Lock()
					grown := sim.grown
					sim.mu.Unlock()
					if grown && strings.Contains(cmd, "--recurse") {
						out = growTo(out, time.Now().Add(-time.Minute).Unix())
					}
					return out, 0
				}
				return "unexpected command: " + cmd, 1
			})
			oldKH := os.Getenv("SSH_KNOWN_HOSTS")
			t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
			os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck
			addr, port := hostPortOf(t, srv.addr)
			withPgBackRestSSHInfra(t, addr, port, keyPath)

			oldA, oldP, oldI, oldS := archiveFlushTimeout, planAfterFlushTimeout, planPollInterval, stopConfirmWait
			archiveFlushTimeout, planAfterFlushTimeout, planPollInterval, stopConfirmWait = time.Second, 60*time.Millisecond, 5*time.Millisecond, 30*time.Millisecond
			t.Cleanup(func() {
				archiveFlushTimeout, planAfterFlushTimeout, planPollInterval, stopConfirmWait = oldA, oldP, oldI, oldS
			})

			plan, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
			if err != nil || plan.Status != PITRFeasible || !plan.InstanceServing || !plan.ArchiveFlushRequired {
				t.Fatalf("plan over SSH: %+v, %v", plan, err)
			}
			res, err := restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime,
				RecoveryTargetVal: liveTarget, PlanDigest: plan.PlanDigest, AllowStopServing: true})
			if tc.refuse {
				if err == nil || !strings.Contains(err.Error(), "started again") {
					t.Fatalf("err = %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(res.Stopped, "postgresql-16 stopped") || !strings.Contains(res.Stopped, "restart_service") {
					t.Errorf("stopped = %q", res.Stopped)
				}
			}
			if got := sim.order(); got != tc.wantEnd {
				t.Errorf("order of operations = %s, want %s", got, tc.wantEnd)
			}
		})
	}
}

// ── the lifecycle steps on their own ─────────────────────────────────────────

func TestPgInstance_StopAndStartAfterStop_Docker(t *testing.T) {
	withPgBackRestDockerInfra(t)
	var rcalls [][]string
	old := cmdRunner
	cmdRunner = runnerFunc(func(_ context.Context, _ string, args []string, _ []string) (string, error) {
		rcalls = append(rcalls, args)
		return "", nil
	})
	defer func() { cmdRunner = old }()
	oldC := containerStopTimeout
	containerStopTimeout = 7 * time.Second
	defer func() { containerStopTimeout = oldC }()

	inst, err := instanceFor("pgbackrest_db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.stopServing(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A clean PostgreSQL stop as the postgres user first, then the container.
	if len(rcalls) != 2 || !strings.Contains(strings.Join(rcalls[0], " "), "pg_ctl") || !strings.Contains(strings.Join(rcalls[0], " "), "su postgres -c") {
		t.Fatalf("calls = %v", rcalls)
	}
	if got := strings.Join(rcalls[1], " "); got != "stop -t 7 helpdesk-test-pg-pgbackrest" {
		t.Errorf("container stop = %q", got)
	}
	if _, err := inst.afterStop().startAfterStop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rcalls[len(rcalls)-1], " "); got != "start helpdesk-test-pg-pgbackrest" {
		t.Errorf("start = %q", got)
	}
}

func TestPgInstance_StoppedContainerRunsMaintenanceInAThrowawayContainer(t *testing.T) {
	withPgBackRestDockerInfra(t)
	var calls [][]string
	old := cmdRunner
	cmdRunner = runnerFunc(func(_ context.Context, _ string, args []string, _ []string) (string, error) {
		calls = append(calls, args)
		if args[0] == "inspect" {
			return "my/postgres:16\n", nil
		}
		return "ok", nil
	})
	defer func() { cmdRunner = old }()
	inst, _ := instanceFor("pgbackrest_db")

	// A running container is exec'd into, as before.
	if _, err := inst.maintenance(context.Background(), "echo hi", true); err != nil {
		t.Fatal(err)
	}
	if calls[0][0] != "exec" {
		t.Errorf("a running container: %v", calls[0])
	}
	// One this tool stopped has nothing to exec into.
	calls = nil
	if _, err := inst.afterStop().maintenance(context.Background(), "echo hi", true); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0][0] != "inspect" || strings.Join(calls[1], " ") != "run --rm --volumes-from helpdesk-test-pg-pgbackrest --entrypoint su my/postgres:16 postgres -c echo hi" {
		t.Errorf("a stopped container: %v", calls)
	}
	// An image that cannot be read is an error, not an empty `docker run`.
	cmdRunner = runnerFunc(func(_ context.Context, _ string, args []string, _ []string) (string, error) {
		return "", fmt.Errorf("no such container")
	})
	if _, err := inst.afterStop().maintenance(context.Background(), "echo hi", true); err == nil || !strings.Contains(err.Error(), "reading the image") {
		t.Errorf("err = %v", err)
	}
}

func TestPgInstance_StopServing_Local_IsRefused(t *testing.T) {
	infraConfig = nil
	inst, _ := instanceFor("")
	if _, err := inst.stopServing(context.Background()); err == nil || !strings.Contains(err.Error(), "colocated") {
		t.Errorf("stopServing: %v", err)
	}
	if _, err := inst.startAfterStop(context.Background()); err == nil || !strings.Contains(err.Error(), "colocated") {
		t.Errorf("startAfterStop: %v", err)
	}
}

func TestParseLivePreState(t *testing.T) {
	p, err := parseLivePreState(healthyPre)
	if err != nil || p.InRecovery || p.Replicas != 0 || p.ArchiveMode != "on" || p.Timeline != 30 || p.LSN != "0/20000000" || p.HAManager != "none" {
		t.Fatalf("parsed %+v, %v", p, err)
	}
	for name, out := range map[string]string{
		"empty":            "",
		"psql noise only":  "psql: error: connection to server failed\n",
		"replicas garbled": strings.Replace(healthyPre, "replicas=0", "replicas=many", 1),
		"timeline garbled": strings.Replace(healthyPre, "timeline=30", "timeline=x", 1),
	} {
		if _, err := parseLivePreState(out); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// A line of noise around the answers is fine.
	if _, err := parseLivePreState("WARNING: something\n" + healthyPre); err != nil {
		t.Errorf("noise before the answers: %v", err)
	}
	// LSN target ahead of the instance.
	p, _ = parseLivePreState(healthyPre)
	if r := p.refusal(RecoveryTarget{TargetLSN, "1/0"}, "1/0"); !strings.Contains(r, "ahead") {
		t.Errorf("refusal = %q", r)
	}
	if r := p.refusal(RecoveryTarget{TargetLSN, "0/4000000"}, "0/4000000"); r != "" {
		t.Errorf("an LSN the instance has passed was refused: %q", r)
	}
}

func TestFlushArchiveScript_Shape(t *testing.T) {
	s := flushArchiveScript(45 * time.Second)
	for _, want := range []string{"pg_switch_wal", "archive_status", `\.ready$`, "failed_count", "-lt 45", "exit 6", "flush=done"} {
		if !strings.Contains(s, want) {
			t.Errorf("flush script lacks %q:\n%s", want, s)
		}
	}
}

// The HA-manager check is run in a real shell, because the bug it guards
// against only exists there: a `pgrep -f` check matched the command line of
// the shell that was running the script, which contains the patterns (found on
// the Docker fixture, 2026-10-28). The check is now by exact process name.
func TestHAManagerDetect_RealShell(t *testing.T) {
	run := func(t *testing.T, env []string) string {
		t.Helper()
		cmd := exec.Command("sh", "-c", haManagerDetect)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// The check reads the machine's process table, so it can only be asserted
	// on a machine that has nothing that looks like an HA manager (a shell
	// whose command line ends in /patroni counts, which is what a developer's
	// terminal can look like). The Linux case is proven in CI and by hand.
	for _, probe := range [][]string{
		{"-x", "patroni|repmgrd|pg_autoctl|stolon-keeper|stolon-sentinel"},
		{"-f", "[/]patroni( |$)"},
	} {
		if out, _ := exec.Command("pgrep", probe...).Output(); len(out) > 0 {
			t.Skipf("something on this machine already looks like an HA manager (pgrep %v)", probe)
		}
	}
	if got := run(t, nil); got != "ha_manager=none" {
		t.Fatalf("with no HA manager running: %q (the check matched itself?)", got)
	}
	// Mentioning one in the command line is not running one.
	cmd0 := exec.Command("sh", "-c", "# repmgrd stolon pg_autoctl patroni\n"+haManagerDetect)
	if out, _ := cmd0.CombinedOutput(); !strings.Contains(string(out), "ha_manager=none") {
		t.Fatalf("a command line that merely mentions HA managers: %q", out)
	}
	// The same snippet inside `sh -c "<script>"`, the way `su postgres -c`
	// and ssh run a maintenance script, so the script text is in an argv.
	cmd := exec.Command("sh", "-c", "echo start; "+haManagerDetect)
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "ha_manager=none") {
		t.Fatalf("inside a larger script: %q", out)
	}

	// A process that is really named patroni is found.
	dir := t.TempDir()
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	data, err := os.ReadFile(sleepBin)
	if err != nil {
		t.Skip("cannot copy sleep")
	}
	fake := dir + "/patroni"
	if err := os.WriteFile(fake, data, 0o755); err != nil {
		t.Skip("cannot write a fake patroni")
	}
	p := exec.Command(fake, "30")
	if err := p.Start(); err != nil {
		t.Skipf("cannot start a process named patroni: %v", err)
	}
	defer p.Process.Kill() //nolint:errcheck
	time.Sleep(300 * time.Millisecond)
	if out, _ := exec.Command("pgrep", "-x", "patroni").Output(); len(out) == 0 {
		t.Skip("cannot make a process named patroni here (a copied, signed binary is killed at launch on macOS); covered on Linux")
	}
	if got := run(t, nil); got != "ha_manager=running" {
		t.Errorf("with a process named patroni: %q", got)
	}
}

// ── the rest of the failure injection ────────────────────────────────────────

// A plan that stops being feasible once the flushed WAL is in the repo (here:
// a hole appears in the archive) ends the restore before the stop.
func TestRestoreToPIT_Live_PlanBreaksAfterTheFlush(t *testing.T) {
	withPgBackRestDockerInfra(t)
	sim, r := liveEnv(t)
	plan := approvedLiveDigest(t, liveTarget)
	inner := r.repoRun
	r.repoRun = func(args []string) (string, error) {
		out, err := inner(args)
		sim.mu.Lock()
		grown := sim.grown
		sim.mu.Unlock()
		if grown && strings.Contains(strings.Join(args, " "), "--recurse") {
			// Drop the first segment of the newest timeline: a hole in the chain.
			var files map[string]map[string]any
			if json.Unmarshal([]byte(out), &files) == nil {
				for k := range files {
					if strings.HasPrefix(k, "0000001E00000000/0000001E0000000000000004-") {
						delete(files, k)
					}
				}
				b, _ := json.Marshal(files)
				out = string(b)
			}
		}
		return out, err
	}
	_, err := restoreToPITImpl(context.Background(), liveArgs(plan))
	if err == nil || !strings.Contains(err.Error(), "the plan is not_feasible") {
		t.Fatalf("err = %v", err)
	}
	if sim.did("cleanstop") || sim.did("stop") || !sim.up {
		t.Errorf("order %s, up=%v: the instance was stopped over a plan that no longer holds", sim.order(), sim.up)
	}
}

// If the repository cannot be read when the plan is recomputed after the
// flush, the instance is left running.
func TestRestoreToPIT_Live_RepoUnreadableAfterTheFlush(t *testing.T) {
	withPgBackRestDockerInfra(t)
	sim, r := liveEnv(t)
	plan := approvedLiveDigest(t, liveTarget)
	inner := r.custom
	r.custom = func(joined string) (string, error, bool) {
		if sim.did("flush") && strings.Contains(joined, "'info'") || sim.did("flush") && strings.Contains(joined, "--output=json") && strings.Contains(joined, "info") {
			return "", fmt.Errorf("unable to open the repo"), true
		}
		return inner(joined)
	}
	_, err := restoreToPITImpl(context.Background(), liveArgs(plan))
	if err == nil || !strings.Contains(err.Error(), "could not plan the recovery") {
		t.Fatalf("err = %v", err)
	}
	if sim.did("stop") || !sim.up {
		t.Errorf("order %s: stopped with an unreadable repository", sim.order())
	}
}

// Two restores of one target must not overlap: a model that retries a slow
// call would otherwise run two pgbackrest restores on one data directory.
func TestRestoreToPIT_SecondRestoreOfTheSameTargetIsRefusedWhileOneRuns(t *testing.T) {
	withPgBackRestDockerInfra(t)
	sim, r := liveEnv(t)
	plan := approvedLiveDigest(t, liveTarget)
	entered, proceed := make(chan struct{}), make(chan struct{})
	inner := r.custom
	var once sync.Once
	r.custom = func(joined string) (string, error, bool) {
		if strings.Contains(joined, "in_recovery=") {
			first := false
			once.Do(func() { first, _ = true, 0; close(entered) })
			if first {
				<-proceed // only the first restore is held, in its preflight; a second must not hang the test if the guard is missing
			}
		}
		return inner(joined)
	}

	done := make(chan error, 1)
	go func() {
		_, err := restoreToPITImpl(context.Background(), liveArgs(plan))
		done <- err
	}()
	<-entered
	_, err := restoreToPITImpl(context.Background(), liveArgs(plan))
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("the second restore: err = %v", err)
	}
	close(proceed)
	if err := <-done; err != nil {
		t.Fatalf("the first restore: %v", err)
	}
	if n := strings.Count(sim.order(), "restore"); n != 1 {
		t.Errorf("order %s: %d restores ran, want 1", sim.order(), n)
	}
	// And the claim is released: a later restore of the target is not blocked.
	if rel, ok := beginPITRestore("pgbackrest_db"); !ok {
		t.Error("the target is still claimed after the restore finished")
	} else {
		rel()
	}
	// A different target is independent.
	rel, ok := beginPITRestore("another")
	if !ok {
		t.Error("an unrelated target was blocked")
	}
	rel()
}

// systemd: a stop that fails is an error that says the instance may still be
// running, and nothing is restored.
func TestRestoreToPIT_Live_Systemd_StopFails(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")
	sim := newLiveSim()
	wal := fakeWALRun(t, "", fmt.Errorf("unable to read"))
	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		if strings.Contains(cmd, "'systemctl' 'stop'") {
			sim.log("stop")
			return "sudo: a password is required", 1
		}
		if out, err, ok := sim.hook(cmd); ok {
			if err != nil {
				return out, 1
			}
			return out, 0
		}
		switch {
		case strings.Contains(cmd, "'info'"):
			return readFixture(t, "multi_timeline_info.json"), 0
		default:
			out, err := wal([]string{cmd})
			if err != nil {
				return "unable to read", 1
			}
			sim.mu.Lock()
			grown := sim.grown
			sim.mu.Unlock()
			if grown && strings.Contains(cmd, "--recurse") {
				out = growTo(out, time.Now().Add(-time.Minute).Unix())
			}
			return out, 0
		}
	})
	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck
	addr, port := hostPortOf(t, srv.addr)
	withPgBackRestSSHInfra(t, addr, port, keyPath)
	oldA, oldP, oldI := archiveFlushTimeout, planAfterFlushTimeout, planPollInterval
	archiveFlushTimeout, planAfterFlushTimeout, planPollInterval = time.Second, 60*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { archiveFlushTimeout, planAfterFlushTimeout, planPollInterval = oldA, oldP, oldI })

	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("plan: %+v, %v", plan, err)
	}
	_, err = restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "pgbackrest_db", Stanza: "main", RecoveryTargetType: TargetTime,
		RecoveryTargetVal: liveTarget, PlanDigest: plan.PlanDigest, AllowStopServing: true})
	if err == nil || !strings.Contains(err.Error(), "may still be running") || !strings.Contains(err.Error(), "a password is required") {
		t.Fatalf("err = %v", err)
	}
	if sim.did("restore") {
		t.Errorf("order %s: restored after a failed stop", sim.order())
	}
}

// Kubernetes: when the one-off pod's script refuses before changing anything,
// the StatefulSet is scaled back up.
func TestRestoreToPIT_Live_Kubernetes_RefusalScalesBackUp(t *testing.T) {
	sim := newLiveSim()
	sim.restoreOut, sim.restoreErr = pitrRefusedMarker+"not enough room beside the data directory\nmore detail on a second line\n", fmt.Errorf("exit status 5")
	r := pitrRouter(t, false)
	wal := fakeWALRun(t, readFixture(t, "multi_timeline_000000010000000000000002.gz"), nil)
	r.repoRun = func(args []string) (string, error) {
		out, err := wal(args)
		sim.mu.Lock()
		grown := sim.grown
		sim.mu.Unlock()
		if grown && strings.Contains(strings.Join(args, " "), "--recurse") {
			out = growTo(out, time.Now().Add(-time.Minute).Unix())
		}
		return out, err
	}
	r.custom = func(joined string) (string, error, bool) {
		if strings.Contains(joined, "pg_isready") {
			return "accepting connections\n", nil, true
		}
		return sim.hook(joined)
	}
	oldA, oldP, oldI := archiveFlushTimeout, planAfterFlushTimeout, planPollInterval
	archiveFlushTimeout, planAfterFlushTimeout, planPollInterval = time.Second, 60*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { archiveFlushTimeout, planAfterFlushTimeout, planPollInterval = oldA, oldP, oldI })
	f := newFakeK8sCluster(r, true)
	f.resumeReady = true
	withFakeK8sCluster(t, f)

	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{Target: "prod_db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("plan: %+v, %v", plan, err)
	}
	_, err = restoreToPITImpl(context.Background(), RestoreToPITArgs{Target: "prod_db", Stanza: "main", RecoveryTargetType: TargetTime,
		RecoveryTargetVal: liveTarget, PlanDigest: plan.PlanDigest, AllowStopServing: true})
	if err == nil || !strings.Contains(err.Error(), "refused (not enough room beside the data directory)") || !strings.Contains(err.Error(), "started again") {
		t.Fatalf("err = %v (only the first line of the script's reason belongs in the message)", err)
	}
	if f.replicas != 1 {
		t.Errorf("replicas = %d after the refusal, want the StatefulSet back at 1", f.replicas)
	}
}
