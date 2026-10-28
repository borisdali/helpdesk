//go:build integration

// In-cluster restore tests: the tools under test run in the sysadmin agent
// deployed in the cluster, called through its POST /tool/{name} endpoint, so
// they use its own service account, RBAC and in-cluster kubectl — the
// deployment as it really runs, against a real crash: the demo pod runs the
// stock postgres entrypoint, so a missing pg_control leaves it in
// CrashLoopBackOff and nothing can exec into it.
//
// The harness side (hiding a WAL segment, the crash, the canary, checking
// the data volume) uses kubectl from where the test runs: exec while the pod
// is up, a one-off pod on the same volumes while it's crash-looping.
//
// Opt-in and destructive: make integration-k8s deploys the pod in its own
// namespace (default db-destruct), takes a fresh backup, port-forwards the
// sysadmin agent and runs these. The agent's target for that namespace is
// HELPDESK_K8S_SYSADMIN_TARGET (default pgbackrest-k8s-destruct, from
// testing/k8s/helm-values-pgbackrest.yaml); its name and the namespace must
// both say "destruct".
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"helpdesk/agentutil"
	"helpdesk/internal/infra"
)

const k8sPgBackRestPodSelector = "app=pgbackrest-demo"

type k8sCluster struct {
	t        *testing.T
	ctxName  string
	ns       string
	pod      string
	agentURL string
	target   string
	apiKey   string // bearer token for an agent that enforces identity; empty when it doesn't
	// lastPlanRaw is the last plan_point_in_time_recovery output, as the agent
	// sent it (to tell an agent that predates a field from one that left it out).
	lastPlanRaw string
	host        resolvedHost // the harness's own view, for kubectl from here
}

func k8sInCluster(t *testing.T) *k8sCluster {
	t.Helper()
	agentURL := os.Getenv("HELPDESK_K8S_SYSADMIN_URL")
	ctxName := os.Getenv("HELPDESK_K8S_PGBACKREST_CONTEXT")
	if agentURL == "" || ctxName == "" {
		t.Skip("HELPDESK_K8S_SYSADMIN_URL / HELPDESK_K8S_PGBACKREST_CONTEXT not set — opt-in, run via make integration-k8s: these tests crash and restore a K8s pod")
	}
	ns := os.Getenv("HELPDESK_K8S_PGBACKREST_NAMESPACE")
	if ns == "" {
		ns = "db-destruct"
	}
	target := os.Getenv("HELPDESK_K8S_SYSADMIN_TARGET")
	if target == "" {
		target = "pgbackrest-k8s-destruct"
	}
	if !strings.HasSuffix(ns, "-destruct") || !strings.Contains(target, "destruct") {
		t.Fatalf("namespace %q must end in -destruct and target %q must contain destruct: these tests crash and restore its database", ns, target)
	}
	c := &k8sCluster{t: t, ctxName: ctxName, ns: ns, agentURL: strings.TrimSuffix(agentURL, "/"), target: target,
		apiKey: os.Getenv("HELPDESK_K8S_SYSADMIN_API_KEY"),
		host:   resolvedHost{K8sContext: ctxName, K8sNamespace: ns, K8sPodSelector: k8sPgBackRestPodSelector}}
	// The harness reuses the lifecycle's own kubectl helpers locally; the
	// tools under test run in the cluster.
	old := infraConfig
	infraConfig = &infra.Config{}
	t.Cleanup(func() { infraConfig = old })
	c.pod = "pgbackrest-demo-0"
	// A previous run that could not repair its pod (a missing API key, an
	// interrupted run) leaves it crash-looping with pg_control gone. Bring it
	// back before testing anything, so one bad run does not fail every later one.
	if !c.tryReady(45 * time.Second) {
		c.step("the instance pod is not ready: a previous run left it broken; repairing it before testing")
		c.requireAuth()
		c.repair()
	}
	c.waitReady(3 * time.Minute)
	// Every test below crashes the pod on purpose. Prove the agent will answer
	// this harness (reachable, authenticated) BEFORE that: a test that crashed
	// the pod and then could not call the restore left the cluster broken.
	c.requireAuth()
	return c
}

// requireAuth fails the test, before anything is changed, when the in-cluster
// agent does not accept this harness's calls.
func (c *k8sCluster) requireAuth() {
	c.t.Helper()
	if msg := c.authProblem(); msg != "" {
		c.t.Fatal(msg)
	}
}

// authProblem is requireAuth's check: "" when the in-cluster agent accepts this
// harness's calls, else what to do about it.
func (c *k8sCluster) authProblem() string {
	c.t.Helper()
	_, errStr := c.tool("get_pgbackrest_status", map[string]any{"target": c.target})
	if strings.Contains(errStr, "authentication required") || strings.Contains(errStr, "401") {
		return fmt.Sprintf("the sysadmin agent rejected the call (%s): this release enforces identity, so run `make integration-k8s K8S_SYSADMIN_API_KEY=<a key the agent accepts>`. Nothing was changed", errStr)
	}
	return ""
}

// tryReady reports whether the instance pod becomes ready within timeout.
func (c *k8sCluster) tryReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if pod, ready, err := k8sInstancePod(context.Background(), c.host); err == nil && pod != "" && ready {
			c.pod = pod
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(3 * time.Second)
	}
}

// step logs one timestamped line of what the harness is doing, so a passing
// run under -v reads as a record of the commands, their output and the checks.
func (c *k8sCluster) step(format string, a ...any) {
	c.t.Helper()
	c.t.Logf("[%s] %s", time.Now().UTC().Format("15:04:05.000"), fmt.Sprintf(format, a...))
}

// clipLog trims s to n bytes for a log line.
func clipLog(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + fmt.Sprintf("… (%d more bytes)", len(s)-n)
	}
	return s
}

// tool calls the in-cluster sysadmin agent's tool and returns its JSON
// output, or its error text.
func (c *k8sCluster) tool(name string, args map[string]any) (string, string) {
	c.t.Helper()
	// An agent that enforces policy through auditd needs a trace_id on every
	// call (the gateway supplies one in production); without it auditd answers
	// 400 "agent requests must include trace_id".
	body, _ := json.Marshal(agentutil.DirectToolRequest{
		TraceID: fmt.Sprintf("tr_k8sit_%d", time.Now().UnixNano()),
		Args:    args,
		Purpose: "remediation",
	})
	argsJSON, _ := json.Marshal(args)
	c.step("POST %s/tool/%s args=%s", c.agentURL, name, argsJSON)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.agentURL+"/tool/"+name, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("POST /tool/%s: %v", name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out agentutil.DirectToolResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		c.t.Fatalf("/tool/%s: HTTP %d, unparseable body %q", name, resp.StatusCode, raw)
	}
	c.step("  -> HTTP %d in %s, error=%q, output=%s", resp.StatusCode, time.Since(start).Round(time.Millisecond), out.Error, clipLog(out.Output, 700))
	return out.Output, out.Error
}

func (c *k8sCluster) kubectl(args ...string) string {
	c.t.Helper()
	c.step("kubectl %s", strings.Join(args, " "))
	out, err := kubectl(context.Background(), c.host, args...)
	c.step("  -> err=%v out=%s", err, clipLog(out, 300))
	if err != nil {
		c.t.Fatalf("%v", err)
	}
	return out
}

// execRaw runs script as root in the instance pod without logging it: for
// polling loops.
func (c *k8sCluster) execRaw(script string) (string, error) {
	return kubectl(context.Background(), c.host, "exec", c.pod, "--", "sh", "-c", script)
}

// exec runs script as root in the instance pod (it must be up).
func (c *k8sCluster) exec(script string) (string, error) {
	c.t.Helper()
	c.step("exec in %s: %s", c.pod, clipLog(script, 400))
	out, err := c.execRaw(script)
	c.step("  -> err=%v out=%s", err, clipLog(out, 300))
	return out, err
}

// onVolumes runs script as postgres in a one-off pod on the instance's
// volumes: the only way in while it's crash-looping.
func (c *k8sCluster) onVolumes(script string) (string, error) {
	c.t.Helper()
	c.step("one-off pod on the instance's volumes: %s", clipLog(script, 400))
	out, err := k8sMaintenance(context.Background(), c.host, script)
	c.step("  -> err=%v out=%s", err, clipLog(out, 300))
	return out, err
}

func (c *k8sCluster) psql(sql string) string {
	c.t.Helper()
	out, err := c.exec("su postgres -c " + shellQuote("psql -d testdb -Atc "+shellQuote(sql)))
	if err != nil {
		c.t.Fatalf("psql %q: %v", sql, err)
	}
	return strings.TrimSpace(out)
}

func (c *k8sCluster) waitReady(timeout time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		pod, ready, err := k8sInstancePod(context.Background(), c.host)
		if err == nil && pod != "" && ready {
			c.pod = pod
			c.step("instance pod %s is ready", pod)
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("instance pod not ready after %s (pod %q, %v)", timeout, pod, err)
		}
		time.Sleep(3 * time.Second)
	}
}

// crash removes pg_control and restarts the pod: postgres can't start, the
// container exits, CrashLoopBackOff. Waits until the pod is there but not
// ready and exec into it fails, the state a real crash leaves.
func (c *k8sCluster) crash(alsoInPGDATA string) {
	c.t.Helper()
	c.step("crash: removing pg_control, then deleting the pod so postgres cannot start")
	if out, err := c.exec("rm -f /var/lib/postgresql/data/global/pg_control" + alsoInPGDATA); err != nil {
		c.t.Fatalf("corrupting pg_control: %v: %s", err, out)
	}
	c.kubectl("delete", "pod", c.pod, "--wait=true")
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		pod, ready, _ := k8sInstancePod(context.Background(), c.host)
		if pod != "" && !ready {
			if _, err := c.execRaw("true"); err != nil {
				time.Sleep(5 * time.Second) // let it crash at least once more
				c.step("crash: pod %s is not ready and cannot be exec'd into (CrashLoopBackOff)", pod)
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	c.t.Fatal("pod did not reach a crashed, un-exec-able state within 2m")
}

// repair brings the instance back whatever state a test left it in: the
// segments a test hid moved back, then, if postgres isn't serving, a restore
// through the agent (the lifecycle under test) and a resume.
func (c *k8sCluster) repair() {
	c.t.Helper()
	c.step("repair: unhide any hidden segments and bring the instance back")
	unhide := `for g in $(find /var/lib/pgbackrest/archive -name '*.hidden-by-test'); do mv "$g" "${g%.hidden-by-test}"; done; rm -rf /var/lib/pgbackrest/pg_wal.saved-*`
	if _, ready, _ := k8sInstancePod(context.Background(), c.host); ready {
		_, _ = c.exec("su postgres -c " + shellQuote(unhide))
		return
	}
	if out, err := c.onVolumes(unhide); err != nil {
		c.t.Logf("repair: unhide: %v: %s", err, out)
	}
	if _, errStr := c.tool("restore_from_backup", map[string]any{"target": c.target, "stanza": "main"}); errStr != "" {
		c.t.Logf("repair: restore_from_backup: %s", errStr)
	}
	if _, errStr := c.tool("restart_container", map[string]any{"target": c.target, "reason": "test repair"}); errStr != "" {
		c.t.Logf("repair: restart_container: %s", errStr)
	}
	c.waitReady(5 * time.Minute)
}

func (c *k8sCluster) replicas() string {
	return strings.TrimSpace(c.kubectl("get", "statefulset", "pgbackrest-demo", "-o", "jsonpath={.spec.replicas}"))
}

// Item 1's refusal, in-cluster, on a real crash: the latest backup's start
// segment hidden, get_pgbackrest_status (run by the agent in a maintenance
// pod, the instance pod being un-exec-able) reports the chain broken, and
// restore_from_backup refuses before scaling anything down or touching PGDATA.
func TestIntegration_K8sCluster_RestoreRefusesOnRealWALGap(t *testing.T) {
	c := k8sInCluster(t)
	t.Cleanup(c.repair)

	c.step("1. precondition: the WAL chain must be continuous before anything is hidden")
	out, errStr := c.tool("get_pgbackrest_status", map[string]any{"target": c.target})
	var before GetPgBackRestStatusResult
	if errStr != "" || json.Unmarshal([]byte(out), &before) != nil || before.WALContinuity == nil || before.WALContinuity.Status != WALContinuous {
		t.Fatalf("precondition: get_pgbackrest_status = %s %s, want continuous", out, errStr)
	}
	seg := before.WALContinuity.BackupStartWAL
	c.step("   ok: continuous, backup %s starts at segment %s", before.LastBackupLabel, seg)
	c.step("2. hide the backup's start segment %s in the repo", seg)
	dir := "/var/lib/pgbackrest/archive/main/" + before.WALContinuity.ArchiveID + "/" + seg[:16]
	if o, err := c.exec("su postgres -c " + shellQuote(`set -e; g=$(ls `+dir+`/`+seg+`-*); mv "$g" "$g.hidden-by-test"`)); err != nil {
		t.Fatalf("hiding %s: %v: %s", seg, err, o)
	}
	c.step("3. crash the instance, leaving a canary file in PGDATA")
	c.crash(" && touch /var/lib/postgresql/data/walcheck-canary")

	c.step("4. the WAL check on the crashed instance (the agent runs it in a one-off pod) must say broken at %s", seg)
	out, errStr = c.tool("get_pgbackrest_status", map[string]any{"target": c.target})
	var after GetPgBackRestStatusResult
	if errStr != "" || json.Unmarshal([]byte(out), &after) != nil {
		t.Fatalf("get_pgbackrest_status on the crashed instance: %s %s", out, errStr)
	}
	if w := after.WALContinuity; w == nil || w.Status != WALBroken || w.ConsistencyReachable || len(w.Gaps) == 0 || w.Gaps[0].FirstMissing != seg {
		t.Fatalf("WAL continuity = %+v, want broken at %s, consistency unreachable", after.WALContinuity, seg)
	}
	c.step("   ok: status=%s consistency_reachable=%v gaps=%+v", after.WALContinuity.Status, after.WALContinuity.ConsistencyReachable, after.WALContinuity.Gaps)

	c.step("5. restore_from_backup must refuse on the broken chain")
	_, errStr = c.tool("restore_from_backup", map[string]any{"target": c.target, "stanza": "main"})
	if !strings.Contains(errStr, "WAL continuity is broken") {
		t.Fatalf("restore_from_backup error = %q, want a WAL-continuity refusal", errStr)
	}
	c.step("   ok: refused: %s", clipLog(errStr, 400))
	c.step("6. nothing may have been touched: the StatefulSet stays at 1 replica and the canary file survives")
	if r := c.replicas(); r != "1" {
		t.Errorf("StatefulSet replicas = %s after a refused restore, want 1 (nothing quiesced)", r)
	} else {
		c.step("   ok: replicas=%s", r)
	}
	if o, err := c.onVolumes("test -f /var/lib/postgresql/data/walcheck-canary && echo present"); err != nil || !strings.Contains(o, "present") {
		t.Errorf("canary file in PGDATA: %v %q — the refused restore touched the data directory", err, o)
	} else {
		c.step("   ok: canary file present")
	}
}

// Items 13 and 9, in-cluster, on a real crash: with archiving broken, row A
// is committed into a segment left .ready and row B into the open one; after
// the crash restore_from_backup (scale to 0, restore in a maintenance pod
// with the pg_wal copy on the repo volume, tail pushed) and restart_container
// (scale back) bring both rows back, verify_restore_recovery reads the
// recovery from the pod's logs and reports it verified through the tail, and
// every tail segment is in the archive.
func TestIntegration_K8sCluster_RestoreKeepsUnarchivedWAL(t *testing.T) {
	c := k8sInCluster(t)
	t.Cleanup(c.repair)
	t.Cleanup(func() {
		if _, ready, _ := k8sInstancePod(context.Background(), c.host); ready {
			_, _ = c.exec("su postgres -c " + shellQuote(`psql -d testdb -c "ALTER SYSTEM SET archive_command = 'pgbackrest --stanza=main archive-push %p'" -c "SELECT pg_reload_conf()" -c "DROP TABLE IF EXISTS canary_wal_tail" -c "SELECT pg_switch_wal()"`))
		}
	})

	c.psql("ALTER SYSTEM SET archive_command = '/bin/false'")
	c.psql("SELECT pg_reload_conf()")
	time.Sleep(2 * time.Second) // the reload is asynchronous
	c.psql("DROP TABLE IF EXISTS canary_wal_tail")
	c.psql("CREATE TABLE canary_wal_tail (id int PRIMARY KEY, segment text)")
	c.psql("INSERT INTO canary_wal_tail VALUES (1, 'A: completed, .ready, never archived')")
	c.psql("SELECT pg_switch_wal()")
	c.psql("INSERT INTO canary_wal_tail VALUES (2, 'B: open segment')")
	c.crash("")

	out, errStr := c.tool("restore_from_backup", map[string]any{"target": c.target, "stanza": "main"})
	var restored RestoreFromBackupResult
	if errStr != "" || json.Unmarshal([]byte(out), &restored) != nil {
		t.Fatalf("restore_from_backup: %s %s", out, errStr)
	}
	tail := restored.PgWalTail
	if tail == nil || !strings.HasPrefix(tail.SavedTo, "/var/lib/pgbackrest/pg_wal.saved-") || tail.ReplayThroughWAL == "" || len(tail.PushedToArchive) == 0 {
		t.Fatalf("pg_wal_tail = %+v, want the copy saved on the repo volume, a tail to replay and pushed", tail)
	}
	if r := c.replicas(); r != "0" || !strings.Contains(restored.Quiesced, "restart_container") {
		t.Fatalf("replicas = %s, quiesced = %q: want the StatefulSet left at 0 for restart_container", r, restored.Quiesced)
	}

	if _, errStr := c.tool("restart_container", map[string]any{"target": c.target, "reason": "resume after restore"}); errStr != "" {
		t.Fatalf("restart_container: %s", errStr)
	}
	c.waitReady(5 * time.Minute)
	if got := c.psql("SELECT count(*) FROM canary_wal_tail"); got != "2" {
		t.Errorf("canary rows after the restore = %s, want 2: commits that were only in pg_wal were lost", got)
	}

	out, errStr = c.tool("verify_restore_recovery", map[string]any{"target": c.target})
	var verified VerifyRestoreRecoveryResult
	if errStr != "" || json.Unmarshal([]byte(out), &verified) != nil {
		t.Fatalf("verify_restore_recovery: %s %s", out, errStr)
	}
	if verified.Status != RestoreVerified || verified.ReplayedThroughWAL != tail.ReplayThroughWAL || verified.LogSource != "kubectl logs" {
		t.Errorf("verify_restore_recovery = %+v, want verified through %s from kubectl logs", verified, tail.ReplayThroughWAL)
	}
	repo, err := c.exec("ls -R /var/lib/pgbackrest/archive/main")
	if err != nil {
		t.Fatalf("listing the archive: %v", err)
	}
	for _, n := range tail.PushedToArchive {
		if !strings.Contains(repo, n+"-") {
			t.Errorf("tail segment %s is not in the archive", n)
		}
	}
}

// planPITR calls plan_point_in_time_recovery in the cluster until the plan for
// the target is feasible (the WAL holding it is archived), switching WAL while
// the instance pod is up. It skips the test when the deployed agent image
// predates the tool.
func (c *k8sCluster) planPITR(typ, val string, wantFeasible bool) PITRPlan {
	c.t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, ready, _ := k8sInstancePod(context.Background(), c.host); ready {
			_, _ = c.execRaw("su postgres -c " + shellQuote(`psql -d testdb -Atc "SELECT pg_switch_wal()"`))
		}
		time.Sleep(3 * time.Second)
		out, errStr := c.tool("plan_point_in_time_recovery", map[string]any{"target": c.target, "stanza": "main",
			"recovery_target_type": typ, "recovery_target_value": val})
		if strings.Contains(errStr, "unknown tool") {
			c.t.Skip("the deployed sysadmin agent does not have plan_point_in_time_recovery: rebuild the image, load it into the cluster and helm upgrade")
		}
		var plan PITRPlan
		if errStr != "" || json.Unmarshal([]byte(out), &plan) != nil {
			c.t.Fatalf("plan_point_in_time_recovery: %s %s", out, errStr)
		}
		c.lastPlanRaw = out
		// The pod is up while this polls: a target in WAL it has not archived
		// yet is feasible but still needs a flush, and the restore that
		// follows (a crashed, stopped instance) cannot flush.
		if !wantFeasible || (plan.Status == PITRFeasible && !plan.ArchiveFlushRequired) {
			return plan
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("target %s never became feasible: %+v", val, plan)
		}
	}
}

// A point-in-time restore of the demo pod, in-cluster, on a real crash: the
// agent plans the target (first with the pod up, then again from a one-off
// pod once it is crash-looping, with the same digest), refuses to restore
// while the pod is serving, and after the crash scales the StatefulSet to 0,
// restores in a one-off pod to the target and leaves the pod for
// restart_container. After the resume, rows committed before the target are
// there, rows after it are not, and verify_restore_recovery reads the stop
// line from the pod's logs and reports it verified on the next timeline.
func TestIntegration_K8sCluster_RestoreToPointInTime(t *testing.T) {
	c := k8sInCluster(t)
	t.Cleanup(c.repair)
	t.Cleanup(func() {
		if _, ready, _ := k8sInstancePod(context.Background(), c.host); ready {
			_, _ = c.exec("su postgres -c " + shellQuote(`psql -d testdb -c "DROP TABLE IF EXISTS pitr_k8s"`))
		}
	})
	clock := func() string {
		return c.psql("SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')")
	}

	c.step("1. data: rows to keep, a backup after them, the target from the database's own clock, rows to lose after it")
	c.psql("DROP TABLE IF EXISTS pitr_k8s")
	c.psql("CREATE TABLE pitr_k8s (id int, phase text)")
	c.psql("INSERT INTO pitr_k8s SELECT g, 'keep' FROM generate_series(1, 5) g")
	if out, err := c.exec("su postgres -c " + shellQuote("pgbackrest --stanza=main --type=incr backup")); err != nil {
		t.Fatalf("incr backup: %v: %s", err, out)
	}
	time.Sleep(3 * time.Second)
	target := clock()
	time.Sleep(2 * time.Second)
	c.psql("INSERT INTO pitr_k8s SELECT g, 'lose' FROM generate_series(6, 10) g")

	c.step("2. plan the target (pod up): feasible, with a digest")
	plan := c.planPITR(TargetTime, target, true)
	if plan.PlanDigest == "" || plan.BackupLabel == "" || plan.ExpectedTimeline != plan.TargetTimeline+1 {
		t.Fatalf("plan = %+v", plan)
	}
	c.step("   ok: backup %s, timeline %d -> %d, digest %s", plan.BackupLabel, plan.TargetTimeline, plan.ExpectedTimeline, plan.PlanDigest)

	restoreArgs := map[string]any{"target": c.target, "stanza": "main",
		"recovery_target_type": TargetTime, "recovery_target_value": target, "plan_digest": plan.PlanDigest}

	c.step("3. restore while the pod is serving must be refused, and nothing scaled")
	if _, errStr := c.tool("restore_to_point_in_time", restoreArgs); !strings.Contains(errStr, "still accepting connections") {
		t.Fatalf("restore_to_point_in_time on a serving pod: error = %q, want a refusal", errStr)
	}
	if r := c.replicas(); r != "1" {
		t.Fatalf("replicas = %s after a refused restore, want 1", r)
	}
	if got := c.psql("SELECT count(*) FROM pitr_k8s"); got != "10" {
		t.Fatalf("the refused restore changed the data: %s rows", got)
	}

	c.step("4. crash the instance (the pod crash-loops and cannot be exec'd into)")
	c.crash("")

	c.step("5. the same plan from the crashed instance (the agent reads the repo from a one-off pod): same digest")
	again := c.planPITR(TargetTime, target, false)
	if again.Status != PITRFeasible || again.PlanDigest != plan.PlanDigest {
		t.Fatalf("plan on the crashed instance = %+v, want feasible with digest %s", again, plan.PlanDigest)
	}

	c.step("6. a wrong digest is refused before anything is scaled down")
	bad := map[string]any{}
	for k, v := range restoreArgs {
		bad[k] = v
	}
	bad["plan_digest"] = "0000000000000000"
	if _, errStr := c.tool("restore_to_point_in_time", bad); !strings.Contains(errStr, "plan changed since it was approved") {
		t.Fatalf("a wrong digest: error = %q, want a refusal", errStr)
	}
	if r := c.replicas(); r != "1" {
		t.Fatalf("replicas = %s after a refused restore, want 1 (nothing quiesced)", r)
	}

	c.step("7. restore_to_point_in_time: scale to 0, restore in a one-off pod, leave it down for restart_container")
	out, errStr := c.tool("restore_to_point_in_time", restoreArgs)
	var res RestoreToPITResult
	if errStr != "" || json.Unmarshal([]byte(out), &res) != nil {
		t.Fatalf("restore_to_point_in_time: %s %s", out, errStr)
	}
	if res.Prediction == nil || res.Prediction.TargetType != TargetTime || res.Prediction.ExpectedTimeline != plan.ExpectedTimeline {
		t.Errorf("prediction = %+v", res.Prediction)
	}
	if !strings.HasPrefix(res.SavedWAL, "/var/lib/pgbackrest/pg_wal.saved-") {
		t.Errorf("saved_wal = %q, want the copy on the repo volume (the one-off pod's own filesystem goes with it)", res.SavedWAL)
	}
	if r := c.replicas(); r != "0" || !strings.Contains(res.Quiesced, "restart_container") {
		t.Fatalf("replicas = %s, quiesced = %q: want the StatefulSet left at 0 for restart_container", r, res.Quiesced)
	}

	c.step("8. resume, and check the data")
	if _, errStr := c.tool("restart_container", map[string]any{"target": c.target, "reason": "resume after point-in-time restore"}); errStr != "" {
		t.Fatalf("restart_container: %s", errStr)
	}
	c.waitReady(5 * time.Minute)
	if got := c.psql("SELECT phase || ':' || count(*) FROM pitr_k8s GROUP BY phase ORDER BY phase"); got != "keep:5" {
		t.Errorf("rows after the point-in-time restore = %q, want only keep:5", got)
	}

	c.step("9. verify_restore_recovery reads the stop line from the pod's logs")
	out, errStr = c.tool("verify_restore_recovery", map[string]any{"target": c.target})
	var verified VerifyRestoreRecoveryResult
	if errStr != "" || json.Unmarshal([]byte(out), &verified) != nil {
		t.Fatalf("verify_restore_recovery: %s %s", out, errStr)
	}
	if verified.Status != RestoreVerified || verified.NewTimeline != plan.ExpectedTimeline || verified.StopLine == "" || verified.LogSource != "kubectl logs" {
		t.Errorf("verify_restore_recovery = %+v, want verified on timeline %d from kubectl logs with a stop line", verified, plan.ExpectedTimeline)
	}
}

// A point-in-time restore of the demo pod while it is RUNNING (no crash), as
// the gate approval with allow_stop_serving would run it, in-cluster: the
// agent asks the running pod about itself, flushes its WAL to the archive,
// plans again from the repository, stops it cleanly and scales the
// StatefulSet to 0, restores in a one-off pod to the target and leaves it for
// restart_container. The target is in WAL the pod had not archived when it was
// chosen. Without allow_stop_serving the same call is refused and the pod is
// untouched.
func TestIntegration_K8sCluster_RestoreToPointInTime_Live(t *testing.T) {
	c := k8sInCluster(t)
	t.Cleanup(c.repair)
	t.Cleanup(func() {
		if _, ready, _ := k8sInstancePod(context.Background(), c.host); ready {
			_, _ = c.exec("su postgres -c " + shellQuote(`psql -d testdb -c "DROP TABLE IF EXISTS pitr_k8s_live"`))
		}
	})
	clock := func() string {
		return c.psql("SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')")
	}

	c.step("1. data: rows to keep, a backup after them, the target, rows to lose (WAL left unarchived on purpose)")
	c.psql("DROP TABLE IF EXISTS pitr_k8s_live")
	c.psql("CREATE TABLE pitr_k8s_live (id int, phase text)")
	c.psql("INSERT INTO pitr_k8s_live SELECT g, 'keep' FROM generate_series(1, 5) g")
	if out, err := c.exec("su postgres -c " + shellQuote("pgbackrest --stanza=main --type=incr backup")); err != nil {
		t.Fatalf("incr backup: %v: %s", err, out)
	}
	time.Sleep(3 * time.Second)
	target := clock()
	time.Sleep(2 * time.Second)
	c.psql("INSERT INTO pitr_k8s_live SELECT g, 'lose' FROM generate_series(6, 10) g")

	c.step("2. plan the serving pod: feasible, with a digest")
	plan := c.planPITR(TargetTime, target, false)
	// An agent built before the live-instance support does not report whether
	// the instance is serving, and would refuse (or mishandle) the stop below.
	if !strings.Contains(c.lastPlanRaw, `"instance_serving"`) {
		t.Skip("the deployed sysadmin agent predates the live-instance restore (its plan has no instance_serving): rebuild the image, load it into the cluster and helm upgrade, as for the first PITR test")
	}
	if plan.Status != PITRFeasible || !plan.InstanceServing || plan.PlanDigest == "" {
		t.Fatalf("plan = %+v", plan)
	}
	c.step("   ok: archive_flush_required=%v, digest %s", plan.ArchiveFlushRequired, plan.PlanDigest)
	args := map[string]any{"target": c.target, "stanza": "main",
		"recovery_target_type": TargetTime, "recovery_target_value": target, "plan_digest": plan.PlanDigest}

	c.step("3. without allow_stop_serving the serving pod is refused and untouched")
	if _, errStr := c.tool("restore_to_point_in_time", args); !strings.Contains(errStr, "allow_stop_serving") {
		t.Fatalf("error = %q, want a refusal naming allow_stop_serving", errStr)
	}
	if r := c.replicas(); r != "1" {
		t.Fatalf("replicas = %s after a refusal, want 1", r)
	}
	if got := c.psql("SELECT count(*) FROM pitr_k8s_live"); got != "10" {
		t.Fatalf("the refused restore changed the data: %s rows", got)
	}

	c.step("4. with it: flush, stop, scale to 0, restore in a one-off pod")
	args["allow_stop_serving"] = true
	out, errStr := c.tool("restore_to_point_in_time", args)
	var res RestoreToPITResult
	if errStr != "" || json.Unmarshal([]byte(out), &res) != nil {
		t.Fatalf("restore_to_point_in_time: %s %s", out, errStr)
	}
	if res.PreStop == nil || res.PreStop.LSN == "" || res.PreStop.Replicas != 0 || res.Plan.ArchiveFlushRequired || res.Plan.PlanDigest != plan.PlanDigest {
		t.Errorf("result = %+v", res)
	}
	if !strings.HasPrefix(res.SavedWAL, "/var/lib/pgbackrest/pg_wal.saved-") {
		t.Errorf("saved_wal = %q", res.SavedWAL)
	}
	if r := c.replicas(); r != "0" || !strings.Contains(res.Quiesced, "restart_container") {
		t.Fatalf("replicas = %s, quiesced = %q: want the StatefulSet left at 0", r, res.Quiesced)
	}

	c.step("5. resume, check the data and the recovery point")
	if _, errStr := c.tool("restart_container", map[string]any{"target": c.target, "reason": "resume after point-in-time restore"}); errStr != "" {
		t.Fatalf("restart_container: %s", errStr)
	}
	c.waitReady(5 * time.Minute)
	if got := c.psql("SELECT phase || ':' || count(*) FROM pitr_k8s_live GROUP BY phase ORDER BY phase"); got != "keep:5" {
		t.Errorf("rows after the live point-in-time restore = %q, want only keep:5", got)
	}
	out, errStr = c.tool("verify_restore_recovery", map[string]any{"target": c.target})
	var verified VerifyRestoreRecoveryResult
	if errStr != "" || json.Unmarshal([]byte(out), &verified) != nil || verified.Status != RestoreVerified || verified.StopLine == "" {
		t.Errorf("verify_restore_recovery = %s %s", out, errStr)
	}
}
