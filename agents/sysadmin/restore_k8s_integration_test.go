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
	apiKey   string       // bearer token for an agent that enforces identity; empty when it doesn't
	host     resolvedHost // the harness's own view, for kubectl from here
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
	c.waitReady(3 * time.Minute)
	return c
}

// tool calls the in-cluster sysadmin agent's tool and returns its JSON
// output, or its error text.
func (c *k8sCluster) tool(name string, args map[string]any) (string, string) {
	c.t.Helper()
	body, _ := json.Marshal(agentutil.DirectToolRequest{Args: args, Purpose: "remediation"})
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
	return out.Output, out.Error
}

func (c *k8sCluster) kubectl(args ...string) string {
	c.t.Helper()
	out, err := kubectl(context.Background(), c.host, args...)
	if err != nil {
		c.t.Fatalf("%v", err)
	}
	return out
}

// exec runs script as root in the instance pod (it must be up).
func (c *k8sCluster) exec(script string) (string, error) {
	return kubectl(context.Background(), c.host, "exec", c.pod, "--", "sh", "-c", script)
}

// onVolumes runs script as postgres in a one-off pod on the instance's
// volumes: the only way in while it's crash-looping.
func (c *k8sCluster) onVolumes(script string) (string, error) {
	return k8sMaintenance(context.Background(), c.host, script)
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
	if out, err := c.exec("rm -f /var/lib/postgresql/data/global/pg_control" + alsoInPGDATA); err != nil {
		c.t.Fatalf("corrupting pg_control: %v: %s", err, out)
	}
	c.kubectl("delete", "pod", c.pod, "--wait=true")
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		pod, ready, _ := k8sInstancePod(context.Background(), c.host)
		if pod != "" && !ready {
			if _, err := c.exec("true"); err != nil {
				time.Sleep(5 * time.Second) // let it crash at least once more
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

	out, errStr := c.tool("get_pgbackrest_status", map[string]any{"target": c.target})
	var before GetPgBackRestStatusResult
	if errStr != "" || json.Unmarshal([]byte(out), &before) != nil || before.WALContinuity == nil || before.WALContinuity.Status != WALContinuous {
		t.Fatalf("precondition: get_pgbackrest_status = %s %s, want continuous", out, errStr)
	}
	seg := before.WALContinuity.BackupStartWAL
	dir := "/var/lib/pgbackrest/archive/main/" + before.WALContinuity.ArchiveID + "/" + seg[:16]
	if o, err := c.exec("su postgres -c " + shellQuote(`set -e; g=$(ls `+dir+`/`+seg+`-*); mv "$g" "$g.hidden-by-test"`)); err != nil {
		t.Fatalf("hiding %s: %v: %s", seg, err, o)
	}
	c.crash(" && touch /var/lib/postgresql/data/walcheck-canary")

	out, errStr = c.tool("get_pgbackrest_status", map[string]any{"target": c.target})
	var after GetPgBackRestStatusResult
	if errStr != "" || json.Unmarshal([]byte(out), &after) != nil {
		t.Fatalf("get_pgbackrest_status on the crashed instance: %s %s", out, errStr)
	}
	if w := after.WALContinuity; w == nil || w.Status != WALBroken || w.ConsistencyReachable || len(w.Gaps) == 0 || w.Gaps[0].FirstMissing != seg {
		t.Fatalf("WAL continuity = %+v, want broken at %s, consistency unreachable", after.WALContinuity, seg)
	}

	_, errStr = c.tool("restore_from_backup", map[string]any{"target": c.target, "stanza": "main"})
	if !strings.Contains(errStr, "WAL continuity is broken") {
		t.Fatalf("restore_from_backup error = %q, want a WAL-continuity refusal", errStr)
	}
	if r := c.replicas(); r != "1" {
		t.Errorf("StatefulSet replicas = %s after a refused restore, want 1 (nothing quiesced)", r)
	}
	if o, err := c.onVolumes("test -f /var/lib/postgresql/data/walcheck-canary && echo present"); err != nil || !strings.Contains(o, "present") {
		t.Errorf("canary file in PGDATA: %v %q — the refused restore touched the data directory", err, o)
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
