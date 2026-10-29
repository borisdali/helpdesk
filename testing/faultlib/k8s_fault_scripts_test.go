package faultlib

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// db-pgdata-corrupted-k8s crashes and restores a database, so its scripts are
// run here against a fake kubectl that records every call and answers like a
// cluster in a chosen state. That checks the destructive branches (what the
// teardown does to a pod that is ready, and to one that is not; that nothing
// touches a namespace other than db-destruct) without a cluster.

const fakeKubectl = `#!/bin/bash
echo "$*" >> "$KLOG"
a="$*"
case "$a" in
  *"get pod -l app=pgbackrest-demo -o jsonpath={.items[0].metadata.name}"*) echo pgbackrest-demo-0 ;;
  *"get pod -l app=pgbackrest-demo -o jsonpath={.items[0].status.containerStatuses[0].ready}"*) echo "$READY" ;;
  *"get pod pgbackrest-demo-0 -o jsonpath={.status.containerStatuses[0].ready}"*) echo "$READY" ;;
  *"exec pgbackrest-demo-0 -- true"*) exit "${EXEC_TRUE_RC:-0}" ;;
  *"psql -d testdb"*) echo "${PSQL_OUT-t}" ;; # unset means t; set-but-empty means the query printed nothing
  *"get pod fault-repair -o jsonpath={.status.phase}"*) echo "${REPAIR_PHASE:-Succeeded}" ;;
esac
exit 0
`

func k8sFault(t *testing.T) Failure {
	t.Helper()
	c, err := LoadBuiltinCatalog()
	if err != nil {
		t.Fatalf("LoadBuiltinCatalog: %v", err)
	}
	for _, f := range c.Failures {
		if f.ID == "db-pgdata-corrupted-k8s" {
			return f
		}
	}
	t.Fatal("db-pgdata-corrupted-k8s is not in the built-in catalog")
	return Failure{}
}

// runScript runs script with a fake kubectl (and a sleep that returns at
// once) first on PATH, and returns the recorded kubectl calls and the exit
// error.
func runScript(t *testing.T, script string, env ...string) (calls []string, err error) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"kubectl": fakeKubectl, "sleep": "#!/bin/bash\nexit 0\n"} {
		if werr := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); werr != nil {
			t.Fatal(werr)
		}
	}
	klog := filepath.Join(dir, "calls.log")
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "KLOG="+klog, "FAULTTEST_K8S_CONTEXT=minikube")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	b, _ := os.ReadFile(klog)
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			calls = append(calls, l)
		}
	}
	t.Logf("script output:\n%s", out)
	return calls, err
}

func indexOfCall(calls []string, sub string) int {
	for i, c := range calls {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

// Nothing the fault does may touch a namespace other than db-destruct, and
// the scripts must carry their own refusal for any other.
func TestK8sFaultScripts_OnlyEverTouchDbDestruct(t *testing.T) {
	f := k8sFault(t)
	for name, script := range map[string]string{"inject": f.Inject.ScriptInline, "teardown": f.Teardown.ScriptInline} {
		if !strings.Contains(script, `*-destruct)`) {
			t.Errorf("%s script has no refusal for a namespace that does not end in -destruct", name)
		}
	}
	scenarios := map[string]struct {
		script string
		env    []string
	}{
		"inject":               {f.Inject.ScriptInline, []string{"READY=false", "EXEC_TRUE_RC=1"}},
		"teardown (ready)":     {f.Teardown.ScriptInline, []string{"READY=true"}},
		"teardown (not ready)": {f.Teardown.ScriptInline, []string{"READY=false"}},
		"verify_exec":          {f.Remediation.VerifyExec, nil},
	}
	for name, sc := range scenarios {
		t.Run(name, func(t *testing.T) {
			calls, _ := runScript(t, sc.script, sc.env...)
			if len(calls) == 0 {
				t.Fatal("the script made no kubectl calls")
			}
			for _, c := range calls {
				if !strings.Contains(c, "-n db-destruct") {
					t.Errorf("kubectl call outside db-destruct: %q", c)
				}
				if !strings.Contains(c, "--context=minikube") {
					t.Errorf("kubectl call without the harness's --context: %q", c)
				}
			}
		})
	}
}

func TestK8sFaultInject_DeletesPgControlThenTheAPod(t *testing.T) {
	f := k8sFault(t)
	calls, err := runScript(t, f.Inject.ScriptInline, "READY=false", "EXEC_TRUE_RC=1")
	if err != nil {
		t.Fatalf("inject failed: %v", err)
	}
	rm := indexOfCall(calls, "exec pgbackrest-demo-0 -- rm -f /var/lib/postgresql/data/global/pg_control")
	del := indexOfCall(calls, "delete pod pgbackrest-demo-0")
	if rm < 0 || del < 0 || rm > del {
		t.Errorf("want pg_control removed, then the pod deleted; calls: %q", calls)
	}
}

// A pod that never reaches a crashed state is an injection failure, not a
// fault that silently did nothing.
func TestK8sFaultInject_FailsIfThePodNeverCrashes(t *testing.T) {
	f := k8sFault(t)
	// Ready and exec-able the whole time: the corruption did not take effect.
	if _, err := runScript(t, f.Inject.ScriptInline, "READY=true", "EXEC_TRUE_RC=0"); err == nil {
		t.Error("inject reported success for a pod that never crashed")
	}
}

// After a successful remediation the pod is ready: restoring over it would
// delete a live postmaster.pid (the VM teardown's bug of 2026-10-17). It must
// only wait for recovery to finish and clean up.
func TestK8sFaultTeardown_LeavesAReadyPodAlone(t *testing.T) {
	f := k8sFault(t)
	calls, err := runScript(t, f.Teardown.ScriptInline, "READY=true", "PSQL_OUT=t")
	if err != nil {
		t.Fatalf("teardown of a ready pod failed: %v", err)
	}
	for _, forbidden := range []string{" scale ", " run ", "delete pod", "delete statefulset"} {
		if i := indexOfCall(calls, forbidden); i >= 0 {
			t.Errorf("teardown of a ready pod ran %q: %q", forbidden, calls[i])
		}
	}
}

// A pod that is not ready is repaired the way the agent restores: scale to 0,
// restore in a one-off pod on the StatefulSet's own volumes, scale back up.
func TestK8sFaultTeardown_RepairsADownPodInOrder(t *testing.T) {
	f := k8sFault(t)
	calls, err := runScript(t, f.Teardown.ScriptInline, "READY=false", "PSQL_OUT=t", "REPAIR_PHASE=Succeeded")
	if err != nil {
		t.Fatalf("teardown failed: %v", err)
	}
	down := indexOfCall(calls, "scale statefulset/pgbackrest-demo --replicas=0")
	run := indexOfCall(calls, "run fault-repair")
	up := indexOfCall(calls, "scale statefulset/pgbackrest-demo --replicas=1")
	if down < 0 || run < 0 || up < 0 || down >= run || run >= up {
		t.Fatalf("want scale to 0, then the one-off restore pod, then scale to 1; calls: %q", calls)
	}
	for _, want := range []string{"pgdata-pgbackrest-demo-0", "pgbackrest-repo-pgbackrest-demo-0", "pgbackrest --stanza=main --delta restore", "pg_wal.saved-"} {
		if !strings.Contains(calls[run], want) {
			t.Errorf("the one-off pod's spec lacks %q: %s", want, calls[run])
		}
	}
	if indexOfCall(calls, "wait --for=condition=Ready pod/pgbackrest-demo-0") < up {
		t.Errorf("teardown did not wait for the pod to be ready after scaling up: %q", calls)
	}
}

// A restore that fails must not scale the instance back up over a half-restored
// data directory and report success.
func TestK8sFaultTeardown_FailedRepairDoesNotScaleUp(t *testing.T) {
	f := k8sFault(t)
	calls, err := runScript(t, f.Teardown.ScriptInline, "READY=false", "REPAIR_PHASE=Failed")
	if err == nil {
		t.Error("teardown reported success after the repair restore failed")
	}
	if indexOfCall(calls, "--replicas=1") >= 0 {
		t.Errorf("teardown scaled the StatefulSet up after a failed restore: %q", calls)
	}
}

func TestK8sFaultVerifyExec_RecoveredOnlyWhenPromoted(t *testing.T) {
	f := k8sFault(t)
	if _, err := runScript(t, f.Remediation.VerifyExec, "PSQL_OUT=t"); err != nil {
		t.Errorf("a promoted instance must verify: %v", err)
	}
	// Still in recovery (or the query printed nothing): not recovered.
	for _, out := range []string{"f", ""} {
		if _, err := runScript(t, f.Remediation.VerifyExec, "PSQL_OUT="+out); err == nil {
			t.Errorf("PSQL_OUT=%q must not verify", out)
		}
	}
}

// From a Job inside the cluster there is no kubeconfig and so no --context:
// FAULTTEST_K8S_CONTEXT is empty and kubectl uses the pod's ServiceAccount.
// The scripts must work that way too, not only with a context set: under
// `set -e` a careless `[ -n "$CTX" ] && ...` would abort the script right there.
func TestK8sFaultScripts_WorkWithoutAKubeContext(t *testing.T) {
	f := k8sFault(t)
	scenarios := map[string]struct {
		script string
		env    []string
	}{
		"inject":               {f.Inject.ScriptInline, []string{"READY=false", "EXEC_TRUE_RC=1"}},
		"teardown (ready)":     {f.Teardown.ScriptInline, []string{"READY=true"}},
		"teardown (not ready)": {f.Teardown.ScriptInline, []string{"READY=false", "REPAIR_PHASE=Succeeded"}},
		"verify_exec":          {f.Remediation.VerifyExec, nil},
	}
	for name, sc := range scenarios {
		t.Run(name, func(t *testing.T) {
			// A later duplicate wins, so this empties the context runScript sets.
			calls, err := runScript(t, sc.script, append(sc.env, "FAULTTEST_K8S_CONTEXT=")...)
			if err != nil {
				t.Fatalf("the script failed without a kube context: %v", err)
			}
			if len(calls) == 0 {
				t.Fatal("the script made no kubectl calls")
			}
			for _, c := range calls {
				if strings.Contains(c, "--context") {
					t.Errorf("kubectl call with a --context although none was given: %q", c)
				}
				if !strings.Contains(c, "-n db-destruct") {
					t.Errorf("kubectl call outside db-destruct: %q", c)
				}
			}
		})
	}
}
