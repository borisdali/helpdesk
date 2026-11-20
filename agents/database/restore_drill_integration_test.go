//go:build integration

// Integration test for verify_restore_drill against real PostgreSQL servers.
//
// It starts throwaway containers (a source and three clones taken from it with
// pg_basebackup, so they really share the source's system identifier) and runs
// the tool against them, as a superuser and as an ordinary role. Docker must be
// available. Ports 15451-15454 are used; the containers are removed afterwards.
//
//	go test -tags integration -timeout 300s -run RestoreDrill ./agents/database/
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

const (
	drillSrcPort     = 15451
	drillCloneOff    = 15452
	drillCloneOn     = 15453
	drillCloneStandb = 15454
	drillPass        = "drillpass"
	drillImage       = "postgres:16"
	drillSourceCmd   = "pgbackrest --stanza=main archive-push %p"
)

func drillConn(port int, user, pass string) string {
	return fmt.Sprintf("host=localhost port=%d dbname=postgres user=%s password=%s", port, user, pass)
}

func needDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		if os.Getenv("HELPDESK_REQUIRE_INFRA") != "" {
			t.Fatalf("docker is required (HELPDESK_REQUIRE_INFRA is set): %v", err)
		}
		t.Skip("docker is not available")
	}
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func waitReady(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := exec.CommandContext(ctx, "psql", drillConn(port, "postgres", drillPass), "-c", "SELECT 1").Run()
		cancel()
		if err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("postgres on %d did not become ready", port)
}

func psqlAt(t *testing.T, port int, sql string) {
	t.Helper()
	if out, err := exec.Command("psql", drillConn(port, "postgres", drillPass), "-c", sql).CombinedOutput(); err != nil {
		t.Fatalf("psql %q: %v\n%s", sql, err, out)
	}
}

// startClone takes a base backup of the source into a new container and starts
// it with the given postgres arguments. basebackupExtra "-R" makes a standby.
func startClone(t *testing.T, name string, port int, basebackupExtra string, pgArgs ...string) {
	t.Helper()
	script := `set -e
D=/var/lib/postgresql/data
mkdir -p $D && chown postgres:postgres $D && chmod 700 $D
gosu postgres pg_basebackup -d "host=host.docker.internal port=` + fmt.Sprint(drillSrcPort) + ` user=postgres password=` + drillPass + `" -D $D -X stream -c fast $BB
exec gosu postgres postgres "$@"`
	args := []string{"run", "-d", "--name", name, "-p", fmt.Sprintf("%d:5432", port), "-e", "BB=" + basebackupExtra,
		"--entrypoint", "bash", drillImage, "-c", script, "_"}
	docker(t, append(args, pgArgs...)...)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", "-v", name).Run() })
	waitReady(t, port)
}

type drillFixture struct{}

func startDrillFixture(t *testing.T) drillFixture {
	t.Helper()
	needDocker(t)
	suffix := fmt.Sprintf("%d", os.Getpid())
	src := "drilltest-src-" + suffix
	docker(t, "run", "-d", "--name", src, "-p", fmt.Sprintf("%d:5432", drillSrcPort), "-e", "POSTGRES_PASSWORD="+drillPass, drillImage,
		"-c", "wal_level=replica", "-c", "max_wal_senders=10",
		"-c", "archive_mode=on", "-c", "archive_command="+drillSourceCmd)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", "-v", src).Run() })
	waitReady(t, drillSrcPort)

	docker(t, "exec", src, "sh", "-c", `echo "host replication all all scram-sha-256" >> "$PGDATA/pg_hba.conf"`)
	psqlAt(t, drillSrcPort, "SELECT pg_reload_conf()")
	psqlAt(t, drillSrcPort, "CREATE ROLE drillro LOGIN PASSWORD 'ro'")
	psqlAt(t, drillSrcPort, "CREATE TABLE drill_marker AS SELECT generate_series(1,1000) AS i")
	psqlAt(t, drillSrcPort, "GRANT SELECT ON drill_marker TO drillro")

	startClone(t, "drilltest-off-"+suffix, drillCloneOff, "", "-c", "archive_mode=off")
	startClone(t, "drilltest-on-"+suffix, drillCloneOn, "", "-c", "archive_mode=on", "-c", "archive_command="+drillSourceCmd)
	startClone(t, "drilltest-standby-"+suffix, drillCloneStandb, "-R")
	return drillFixture{}
}

func TestRestoreDrill_RealServers(t *testing.T) {
	startDrillFixture(t)
	oldInfra := infraConfig
	infraConfig = nil
	defer func() { infraConfig = oldInfra }()

	type tc struct {
		name   string
		clone  int
		user   string
		pass   string
		sql    string
		status string
		check  string
		result string
	}
	t.Run("an unrelated server is not a clone", func(t *testing.T) {
		// The shared test database (make test-up) is a different cluster.
		other := "host=localhost port=15432 dbname=testdb user=postgres password=testpass"
		if err := exec.Command("psql", other, "-c", "SELECT 1").Run(); err != nil {
			t.Skip("the shared test database on 15432 is not running")
		}
		res, err := verifyRestoreDrillImpl(context.Background(), VerifyRestoreDrillArgs{Source: drillConn(drillSrcPort, "postgres", drillPass), Clone: other})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != audit.DrillRefused || resultOf(res.Checks, "same_system").Result != "fail" {
			t.Errorf("an unrelated cluster was not refused: %s", res.Output)
		}
	})
	t.Run("the source is not its own clone", func(t *testing.T) {
		res, _ := verifyRestoreDrillImpl(context.Background(), VerifyRestoreDrillArgs{
			Source: drillConn(drillSrcPort, "postgres", drillPass), Clone: drillConn(drillSrcPort, "drillro", "ro")})
		if res.Status != audit.DrillRefused || resultOf(res.Checks, "not_the_source").Result != "fail" {
			t.Errorf("the source as its own clone was not refused: %s", res.Output)
		}
	})
	for _, role := range []struct{ label, user, pass string }{{"superuser", "postgres", drillPass}, {"plain role", "drillro", "ro"}} {
		cases := []tc{
			{"restored clone, archiving off", drillCloneOff, role.user, role.pass, "select count(*) = 1000 from drill_marker", audit.DrillVerified, "verify_sql", "pass"},
			{"clone that archives into the source's stanza", drillCloneOn, role.user, role.pass, "", audit.DrillRefused, "archive_guard", "fail"},
			{"a standby of the source", drillCloneStandb, role.user, role.pass, "", audit.DrillRefused, "", ""},
		}
		for _, c := range cases {
			t.Run(role.label+"/"+c.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				res, err := verifyRestoreDrillImpl(ctx, VerifyRestoreDrillArgs{
					Source:    drillConn(drillSrcPort, role.user, role.pass),
					Clone:     drillConn(c.clone, c.user, c.pass),
					VerifySQL: c.sql,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("%s", res.Output)
				if res.Status != c.status {
					t.Errorf("status %q, want %q", res.Status, c.status)
				}
				if c.check != "" {
					if got := resultOf(res.Checks, c.check); got.Result != c.result {
						t.Errorf("check %s = %q, want %q (%s)", c.check, got.Result, c.result, got.Detail)
					}
				}
				if res.Status != audit.DrillUnverifiable && (res.SourceSystemID == "" || res.SourceSystemID != res.CloneSystemID) && c.clone != 0 {
					t.Errorf("a basebackup clone must share the system identifier: %q vs %q", res.SourceSystemID, res.CloneSystemID)
				}
			})
		}
	}
}
