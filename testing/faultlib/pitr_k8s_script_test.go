package faultlib

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// db-pitr-dropped-table-k8s is run by an operator against a cluster, so its
// inject script is run here in a real bash against a stub kubectl: the
// commands it would send to the pod are recorded, in order, and the answers a
// pod would give are made up. That checks what a unit test of the catalog
// cannot: that the quoting reaches kubectl intact, that the target is read
// before the damage, and that the script refuses any other namespace.
const stubKubectl = `#!/bin/sh
echo "$@" >> "$STUB_LOG"
case "$*" in
 *"get pod"*"jsonpath"*) echo pgbackrest-demo-0; exit 0;;
esac
case "$*" in
 *"exec -i"*)
   sql=$(cat)
   echo "SQL: $sql" >> "$STUB_LOG"
   case "$sql" in
     *"NOT pg_is_in_recovery"*) echo t;;
     *"to_char(clock_timestamp()"*) echo "2026-10-28T17:11:10Z";;
     *"pg_current_wal_lsn"*) echo "0/3000028";;
   esac;;
esac
exit 0
`

func TestDroppedTableK8sInject_RealBash(t *testing.T) {
	cat, err := LoadBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var f Failure
	for _, c := range cat.Failures {
		if c.ID == "db-pitr-dropped-table-k8s" {
			f = c
		}
	}
	if f.ID == "" {
		t.Fatal("fault not in the catalog")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(stubKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "calls.log")

	run := func(script string) (string, error) {
		cmd := exec.Command("bash", "-s")
		cmd.Stdin = strings.NewReader(script)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "STUB_LOG="+logPath, "FAULTTEST_K8S_CONTEXT=minikube")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run(f.Inject.ScriptInline)
	if err != nil {
		t.Fatalf("inject: %v\n%s", err, out)
	}
	target, err := ParsePITRMarker(out, "time")
	if err != nil || target.Value != "2026-10-28T17:11:10Z" {
		t.Fatalf("target = %+v, %v\noutput: %s", target, err, out)
	}
	if lsn, err := ParsePITRMarker(out, "lsn"); err != nil || lsn.Value != "0/3000028" {
		t.Errorf("lsn = %+v, %v", lsn, err)
	}

	logBytes, _ := os.ReadFile(logPath)
	log := string(logBytes)
	// Every call is aimed at the destruct namespace and the chosen context.
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if strings.HasPrefix(line, "SQL:") || line == "" {
			continue
		}
		if !strings.Contains(line, "--context=minikube") || !strings.Contains(line, "-n db-destruct") {
			t.Errorf("a kubectl call is not aimed at db-destruct / minikube: %q", line)
		}
	}
	// The order the fault depends on: rows, backup, target, then the damage.
	pos := func(needle string) int { return strings.Index(log, needle) }
	order := []string{"INSERT INTO pitr_orders", "pgbackrest --stanza=main --type=incr backup", "to_char(clock_timestamp()", "CREATE TABLE pitr_after_drop", "DROP TABLE pitr_orders"}
	for i, n := range order {
		if pos(n) < 0 {
			t.Fatalf("the script never sent %q:\n%s", n, log)
		}
		if i > 0 && pos(n) < pos(order[i-1]) {
			t.Errorf("%q came before %q:\n%s", n, order[i-1], log)
		}
	}
	// The SQL arrived whole, with its quotes (it goes in on stdin).
	if !strings.Contains(log, `SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`) {
		t.Errorf("the timestamp query did not arrive intact:\n%s", log)
	}

	// Any namespace but a -destruct one is refused before anything is touched.
	os.Remove(logPath)
	bad := strings.Replace(f.Inject.ScriptInline, "NS=db-destruct", "NS=production", 1)
	if out, err := run(bad); err == nil || !strings.Contains(out, "must end in -destruct") {
		t.Errorf("a non-destruct namespace: err=%v out=%q", err, out)
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "exec") {
		t.Errorf("the script touched the pod of a non-destruct namespace:\n%s", b)
	}
	_ = context.Background
}
