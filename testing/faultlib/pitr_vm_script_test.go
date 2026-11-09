package faultlib

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// db-pitr-dropped-table-vm is run by an operator against a real VM over SSH,
// so its scripts are run here in a real bash against stub psql, pgbackrest,
// systemctl and sudo: the commands the VM would get are recorded, in order,
// and the answers it would give are made up. That checks what a unit test of
// the catalog cannot: that the SQL reaches psql intact, that the target is
// read before the damage, and which branch of the teardown runs when the unit
// is up or down. (The scripts go over `ssh ... bash -s`, so the same bash
// reads them on the VM.)

// A stub psql: records the SQL it was given (as -c or on a -c-less call) and
// answers the few queries the scripts make.
const stubVMPsql = `#!/bin/sh
sql=""
while [ $# -gt 0 ]; do
  case "$1" in -c|-Atqc) sql="$2"; shift;; -Atc) sql="$2"; shift;; esac
  shift
done
echo "SQL: $sql" >> "$STUB_LOG"
case "$sql" in
  *"NOT pg_is_in_recovery"*) echo t;;
  *"to_char(clock_timestamp()"*) echo "2026-11-09T10:11:12Z";;
  *"pg_current_wal_lsn"*) echo "0/4000028";;
  *"SHOW data_directory"*) echo "$STUB_PGDATA";;
esac
exit 0
`

const stubVMCmd = `#!/bin/sh
echo "$(basename "$0") $*" >> "$STUB_LOG"
case "$(basename "$0") $*" in
  "systemctl is-active"*) [ "${STUB_UNIT:-active}" = active ] && exit 0 || exit 3;;
esac
exit 0
`

// vmFixture installs the stubs and returns a runner for a script. Returns the
// output, the stub log and the error.
func vmFixture(t *testing.T, unit string) func(script string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	pgdata := filepath.Join(t.TempDir(), "16", "main")
	if err := os.MkdirAll(pgdata, 0o755); err != nil {
		t.Fatal(err)
	}
	stubs := map[string]string{"psql": stubVMPsql, "pgbackrest": stubVMCmd, "systemctl": stubVMCmd, "sudo": stubVMCmd}
	for name, src := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A saved-WAL copy beside the data directory, which the teardown must remove.
	saved := filepath.Join(filepath.Dir(pgdata), "pg_wal.saved-20261109T101500Z")
	if err := os.MkdirAll(saved, 0o755); err != nil {
		t.Fatal(err)
	}
	// The script is bash text with this path pasted in unquoted, so it must not
	// hold a subtest name's parentheses or colons (t.TempDir does).
	confDir, err := os.MkdirTemp("", "vmconf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(confDir) })
	conf := filepath.Join(confDir, "pgbackrest.conf")
	if err := os.WriteFile(conf, []byte("[main]\npg1-path="+pgdata+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return func(script string) (string, string, error) {
		logPath := filepath.Join(dir, "calls.log")
		os.Remove(logPath)
		// The scripts read /etc/pgbackrest.conf; point them at the fixture's.
		script = strings.ReplaceAll(script, "/etc/pgbackrest.conf", conf)
		cmd := exec.Command("bash", "-s")
		cmd.Stdin = strings.NewReader(script)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "STUB_PGDATA="+pgdata, "STUB_UNIT="+unit, "STUB_LOG="+logPath)
		b, err := cmd.CombinedOutput()
		l, _ := os.ReadFile(logPath)
		log := string(l)
		if _, serr := os.Stat(saved); serr == nil {
			log += "\nSAVED_WAL_STILL_THERE"
		}
		return string(b), log, err
	}
}

func TestDroppedTableVMInject_RealBash(t *testing.T) {
	for _, id := range []string{"db-pitr-dropped-table-vm", "db-pitr-dropped-table-vm-lsn"} {
		t.Run(id, func(t *testing.T) {
			f := pitrFault(t, id)
			run := vmFixture(t, "active")
			out, log, err := run(f.Inject.ScriptInline)
			if err != nil {
				t.Fatalf("inject: %v\n%s", err, out)
			}
			target, err := ParsePITRMarker(out, "time")
			if err != nil || target.Value != "2026-11-09T10:11:12Z" {
				t.Fatalf("target = %+v, %v\noutput: %s", target, err, out)
			}
			if lsn, err := ParsePITRMarker(out, "lsn"); err != nil || lsn.Value != "0/4000028" {
				t.Errorf("lsn = %+v, %v", lsn, err)
			}
			// The order the fault depends on: rows, backup, a write (so the LSN
			// read next is where a record starts, not a segment's bare start),
			// target, then the damage.
			pos := func(needle string) int { return strings.Index(log, needle) }
			order := []string{"INSERT INTO pitr_orders", "pgbackrest --stanza=main --type=incr backup", "SQL: SELECT txid_current()", "to_char(clock_timestamp()", "pg_current_wal_lsn", "CREATE TABLE pitr_after_drop", "DROP TABLE pitr_orders"}
			for i, n := range order {
				if pos(n) < 0 {
					t.Fatalf("the script never ran %q:\n%s", n, log)
				}
				if i > 0 && pos(n) < pos(order[i-1]) {
					t.Errorf("%q came before %q:\n%s", n, order[i-1], log)
				}
			}
			// The SQL arrived whole, with its quotes.
			if !strings.Contains(log, `SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`) {
				t.Errorf("the timestamp query did not arrive intact:\n%s", log)
			}
			// Nothing here needs sudo: the SSH user is postgres.
			if strings.Contains(log, "sudo ") {
				t.Errorf("the inject script used sudo:\n%s", log)
			}
		})
	}
}

func TestDroppedTableVMTeardown_RealBash(t *testing.T) {
	f := pitrFault(t, "db-pitr-dropped-table-vm")
	script := f.Teardown.ScriptInline

	t.Run("the unit is up: this fault's tables are dropped and the saved WAL removed, nothing is restored", func(t *testing.T) {
		run := vmFixture(t, "active")
		out, log, err := run(script)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		for _, want := range []string{"SQL: DROP TABLE IF EXISTS pitr_orders", "SQL: DROP TABLE IF EXISTS pitr_after_drop", "SQL: SELECT pg_switch_wal()"} {
			if !strings.Contains(log, want) {
				t.Errorf("never ran %q:\n%s", want, log)
			}
		}
		if strings.Contains(log, "pgbackrest ") || strings.Contains(log, "systemctl restart") {
			t.Errorf("a healthy instance was restored or restarted:\n%s", log)
		}
		if strings.Contains(log, "SAVED_WAL_STILL_THERE") {
			t.Errorf("the pg_wal.saved copy beside the data directory was not removed:\n%s", log)
		}
	})
	t.Run("the unit is down (a restore that died after the stop): restored by hand and restarted", func(t *testing.T) {
		run := vmFixture(t, "inactive")
		out, log, err := run(script)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		ir := strings.Index(log, "pgbackrest --stanza=main --delta restore")
		is := strings.Index(log, "sudo systemctl restart")
		id := strings.Index(log, "SQL: DROP TABLE IF EXISTS pitr_orders")
		if ir < 0 || is < 0 || id < 0 || ir >= is || is >= id {
			t.Errorf("want the restore, then the restart, then the drops; got positions %d %d %d:\n%s", ir, is, id, log)
		}
	})
}

// The LSN fault is the time fault with another target type: its scripts must
// not drift from it.
func TestDroppedTableVMLSN_IsTheTimeFaultWithAnLSNTarget(t *testing.T) {
	timeF, lsnF := pitrFault(t, "db-pitr-dropped-table-vm"), pitrFault(t, "db-pitr-dropped-table-vm-lsn")
	if lsnF.PITR == nil || lsnF.PITR.Type() != "lsn" || !lsnF.PITR.AllowStopServing {
		t.Errorf("pitr spec = %+v", lsnF.PITR)
	}
	if lsnF.Inject.ScriptInline != timeF.Inject.ScriptInline || lsnF.Teardown.ScriptInline != timeF.Teardown.ScriptInline {
		t.Error("the LSN fault's scripts differ from the time fault's")
	}
	if lsnF.Remediation.VerifySQL != timeF.Remediation.VerifySQL || lsnF.DiagnosisPlaybookSeriesID != timeF.DiagnosisPlaybookSeriesID || lsnF.AgentConnOverride != timeF.AgentConnOverride {
		t.Error("the LSN fault's routing or recovery check differs from the time fault's")
	}
	if lsnF.Inject.Type != "ssh_exec" || lsnF.Teardown.Type != "ssh_exec" {
		t.Errorf("inject/teardown types = %s/%s, want ssh_exec", lsnF.Inject.Type, lsnF.Teardown.Type)
	}
}

// ssh_exec keeps its inject output, like shell_exec, so the VM fault can hand
// the harness its PITR_TARGET line.
func TestInjector_KeepsSSHExecOutput(t *testing.T) {
	dir := t.TempDir()
	// A fake ssh that runs the script it is sent locally.
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nexec bash -s\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	inj := NewInjector(&HarnessConfig{})
	f := Failure{
		Inject:   InjectSpec{Type: "ssh_exec", ExecVia: "postgres@vm", ScriptInline: "echo hello; echo 'PITR_TARGET time=2026-11-09T10:00:00Z lsn=0/1'"},
		Teardown: InjectSpec{Type: "ssh_exec", ExecVia: "postgres@vm", ScriptInline: "echo teardown-output"},
	}
	if err := inj.Inject(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if out := inj.InjectOutput(); !strings.Contains(out, "PITR_TARGET time=2026-11-09T10:00:00Z") {
		t.Errorf("inject output = %q", out)
	}
	if err := inj.Teardown(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if out := inj.InjectOutput(); strings.Contains(out, "teardown-output") || !strings.Contains(out, "PITR_TARGET") {
		t.Errorf("after teardown: %q", out)
	}
}
