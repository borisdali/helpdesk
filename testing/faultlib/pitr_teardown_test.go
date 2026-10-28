package faultlib

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The teardown is what puts the target back when a run died halfway: the
// container stopped by a live restore that then failed, the pod scaled to 0.
// The up case ran for real; the branches that repair a stopped or broken
// instance are the ones that matter most and cannot be provoked on demand, so
// they run here in a real bash against stubs that record what they were asked.

func pitrFault(t *testing.T, id string) Failure {
	t.Helper()
	cat, err := LoadBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range cat.Failures {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("%s is not in the catalog", id)
	return Failure{}
}

// runWithStub runs script in bash with stubSrc installed as `name` first on PATH.
func runWithStub(t *testing.T, name, stubSrc, script string, env ...string) (out string, log string, err error) {
	t.Helper()
	dir, state := t.TempDir(), t.TempDir()
	if werr := os.WriteFile(filepath.Join(dir, name), []byte(stubSrc), 0o755); werr != nil {
		t.Fatal(werr)
	}
	logPath := filepath.Join(dir, "calls.log")
	cmd := exec.Command("bash", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), append([]string{"PATH=" + dir + ":" + os.Getenv("PATH"), "STUB_LOG=" + logPath, "STUB_STATE=" + state, "FAULTTEST_K8S_CONTEXT=minikube"}, env...)...)
	b, rerr := cmd.CombinedOutput()
	l, _ := os.ReadFile(logPath)
	return string(b), string(l), rerr
}

const stubDocker = `#!/bin/sh
echo "$@" >> "$STUB_LOG"
case "$*" in
 *"State.Running"*) if [ -f "$STUB_STATE/started" ]; then echo true; else echo "${STUB_RUNNING:-true}"; fi; exit 0;;
 "start "*)   touch "$STUB_STATE/started"; exit 0;;
 "restart "*) touch "$STUB_STATE/restarted"; exit 0;;
 *"pg_isready"*) if [ "${STUB_PG:-up}" = up ] || [ -f "$STUB_STATE/restarted" ]; then exit 0; else exit 1; fi;;
 *"NOT pg_is_in_recovery"*) echo t; exit 0;;
esac
exit 0
`

func TestDroppedTableTeardown_Docker(t *testing.T) {
	f := pitrFault(t, "db-pitr-dropped-table")
	script := f.Teardown.ScriptInline

	t.Run("running and serving: drops its tables, touches nothing else", func(t *testing.T) {
		out, log, err := runWithStub(t, "docker", stubDocker, script)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		for _, want := range []string{"DROP TABLE IF EXISTS pitr_orders", "DROP TABLE IF EXISTS pitr_after_drop", "pg_switch_wal"} {
			if !strings.Contains(log, want) {
				t.Errorf("never ran %q:\n%s", want, log)
			}
		}
		for _, banned := range []string{"start ", "restart ", "--delta restore"} {
			if strings.Contains(log, banned) {
				t.Errorf("a healthy instance was %q:\n%s", banned, log)
			}
		}
	})
	t.Run("the container was left stopped: it is started, then cleaned", func(t *testing.T) {
		out, log, err := runWithStub(t, "docker", stubDocker, script, "STUB_RUNNING=false")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(log, "start helpdesk-test-pg-pgbackrest") || !strings.Contains(log, "DROP TABLE IF EXISTS pitr_orders") {
			t.Errorf("a stopped container was not started and cleaned:\n%s", log)
		}
		if strings.Contains(log, "--delta restore") {
			t.Errorf("it restored over a database that came back by itself:\n%s", log)
		}
	})
	t.Run("PostgreSQL will not answer: restored from the latest backup and restarted", func(t *testing.T) {
		out, log, err := runWithStub(t, "docker", stubDocker, script, "STUB_RUNNING=false", "STUB_PG=down")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(log, "pgbackrest --stanza=main --delta restore") || !strings.Contains(log, "restart helpdesk-test-pg-pgbackrest") {
			t.Errorf("a database that would not start was not repaired:\n%s", log)
		}
		if strings.Index(log, "--delta restore") > strings.Index(log, "restart helpdesk-test-pg-pgbackrest") {
			t.Errorf("restarted before it restored:\n%s", log)
		}
	})
}

const stubKubectlTeardown = `#!/bin/sh
echo "$@" >> "$STUB_LOG"
case "$*" in
 *"get pod fault-repair"*) echo Succeeded; exit 0;;
 *"get pod -l app=pgbackrest-demo"*"containerStatuses"*) echo "$STUB_READY"; exit 0;;
 *"exec -i"*) sed 's/^/SQL: /' >> "$STUB_LOG"; exit 0;;
 *"NOT pg_is_in_recovery"*) echo t; exit 0;;
esac
exit 0
`

func TestDroppedTableTeardown_Kubernetes(t *testing.T) {
	f := pitrFault(t, "db-pitr-dropped-table-k8s")
	script := f.Teardown.ScriptInline

	t.Run("the pod is ready: this fault's tables are dropped, nothing is scaled", func(t *testing.T) {
		out, log, err := runWithStub(t, "kubectl", stubKubectlTeardown, script, "STUB_READY=true")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		for _, want := range []string{"SQL: DROP TABLE IF EXISTS pitr_orders;", "SQL: DROP TABLE IF EXISTS pitr_after_drop;", "SQL: SELECT pg_switch_wal();"} {
			if !strings.Contains(log, want) {
				t.Errorf("the SQL never reached the pod: %q\n%s", want, log)
			}
		}
		if strings.Contains(log, "scale statefulset") || strings.Contains(log, "run fault-repair") {
			t.Errorf("a healthy pod was scaled or restored:\n%s", log)
		}
		if !strings.Contains(out, "dropped this fault's tables") {
			t.Errorf("output = %q", out)
		}
	})
	t.Run("the pod is not ready (a restore that died after the scale-down): restored by hand and scaled back", func(t *testing.T) {
		out, log, err := runWithStub(t, "kubectl", stubKubectlTeardown, script, "STUB_READY=")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		i0 := strings.Index(log, "scale statefulset/pgbackrest-demo --replicas=0")
		ir := strings.Index(log, "run fault-repair")
		i1 := strings.Index(log, "scale statefulset/pgbackrest-demo --replicas=1")
		if i0 < 0 || ir < 0 || i1 < 0 || !(i0 < ir && ir < i1) {
			t.Errorf("want scale to 0, then the repair pod, then scale to 1; got positions %d %d %d:\n%s", i0, ir, i1, log)
		}
		if strings.Contains(log, "SQL: DROP TABLE") {
			t.Errorf("tables were dropped on a pod that was restored from backup:\n%s", log)
		}
	})
	t.Run("a namespace that is not a -destruct one is refused", func(t *testing.T) {
		bad := strings.Replace(script, "NS=db-destruct", "NS=production", 1)
		out, log, err := runWithStub(t, "kubectl", stubKubectlTeardown, bad, "STUB_READY=true")
		if err == nil || !strings.Contains(out, "must end in -destruct") || strings.Contains(log, "exec") {
			t.Errorf("err=%v out=%q log=%q", err, out, log)
		}
	})
}
