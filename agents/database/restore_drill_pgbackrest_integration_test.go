//go:build integration

// Integration test for restore drills whose clones are made by a real
// `pgbackrest restore`, the way a customer's drill is.
//
// It starts a throwaway source from the pgBackRest fixture image (its own data and
// its own repository: the fixture container is never touched), takes a full backup,
// and makes clones by restoring from a COPY of that repository. The copy is the
// point: a clone that archives pushes WAL and a timeline history file into the
// repository it believes is its own, so the experiment that shows the hazard must
// never run against a repository anything real writes to.
//
// The image is the one the fixture builds:
//
//	docker compose -f testing/docker/docker-compose.yaml -f testing/docker/docker-compose.pgbackrest.yaml build
//
//	go test -tags integration -timeout 600s -run RestoreDrillPgBackRest ./agents/database/
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
	"helpdesk/internal/availability"
)

const (
	pgbrImage      = "docker-postgres-pgbackrest:latest"
	pgbrSrcPort    = 15461
	pgbrCleanPort  = 15462
	pgbrArchivePrt = 15463
)

func needPgBackRestImage(t *testing.T) {
	t.Helper()
	needDocker(t)
	if err := exec.Command("docker", "image", "inspect", pgbrImage).Run(); err != nil {
		if os.Getenv("HELPDESK_REQUIRE_INFRA") != "" {
			t.Fatalf("the image %s is required (HELPDESK_REQUIRE_INFRA is set); build it with docker compose -f testing/docker/docker-compose.yaml -f testing/docker/docker-compose.pgbackrest.yaml build", pgbrImage)
		}
		t.Skipf("the image %s is not built; see this file's header", pgbrImage)
	}
}

// inContainer runs a command in a container as root and returns its output.
func inContainer(t *testing.T, name string, args ...string) string {
	t.Helper()
	return docker(t, append([]string{"exec", name}, args...)...)
}

// asPostgres runs a shell command in a container as the postgres OS user.
func asPostgres(t *testing.T, name, script string) string {
	t.Helper()
	return docker(t, "exec", name, "su", "postgres", "-c", script)
}

// startPgBackRestSource starts a source that archives into its own repository and
// holds a table to restore, and takes a full backup of it.
func startPgBackRestSource(t *testing.T, name string) {
	t.Helper()
	docker(t, "run", "-d", "--name", name, "-p", fmt.Sprintf("%d:5432", pgbrSrcPort),
		"-e", "POSTGRES_PASSWORD="+drillPass, pgbrImage, "postgres", "-c", "archive_mode=on")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", "-v", name).Run() })
	waitReady(t, pgbrSrcPort)

	psqlAt(t, pgbrSrcPort, "CREATE ROLE drillro LOGIN PASSWORD 'ro'")
	psqlAt(t, pgbrSrcPort, "CREATE TABLE drill_marker AS SELECT generate_series(1,1000) AS i")
	psqlAt(t, pgbrSrcPort, "GRANT SELECT ON drill_marker TO drillro")
	// Enough data that the backup is a real size, so a clone of it is comparable.
	psqlAt(t, pgbrSrcPort, "CREATE TABLE drill_filler AS SELECT i, md5(i::text) AS v FROM generate_series(1,200000) i")

	// archive_mode is set on the source's command line (the fixture does that), and a
	// command-line flag is not part of the data directory, so a restored clone would
	// not inherit it. A real server has it in its configuration: put it there, so
	// that the backup carries it and a clone restored with the archiving kept has it.
	psqlAt(t, pgbrSrcPort, "ALTER SYSTEM SET archive_mode = 'on'")

	asPostgres(t, name, "pgbackrest --stanza=main --log-level-console=info --start-fast --type=full backup")
	psqlAt(t, pgbrSrcPort, "SELECT pg_switch_wal()")
}

// restoredClone makes a clone by restoring the source's backup from a copy of its
// repository, starts it, and returns once it accepts connections. archiveMode is the
// pgBackRest --archive-mode: "off" for a drill done properly, "preserve" (its
// default) keeps the source's archiving and is the hazard.
func restoredClone(t *testing.T, source, name string, port int, archiveMode string) {
	t.Helper()
	docker(t, "run", "-d", "--name", name, "-p", fmt.Sprintf("%d:5432", port), "--entrypoint", "sleep", pgbrImage, "infinity")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", "-v", name).Run() })
	inContainer(t, name, "sh", "-c",
		"mkdir -p /var/lib/pgbackrest /var/lib/postgresql/data && chown -R postgres:postgres /var/lib/pgbackrest /var/lib/postgresql/data && chmod 700 /var/lib/postgresql/data")
	// The copy of the repository: a tar stream from the source's, never a mount of it.
	copyCmd := fmt.Sprintf("docker exec %s tar -C /var/lib/pgbackrest -cf - . | docker exec -i %s tar -C /var/lib/pgbackrest -xf -", source, name)
	if out, err := exec.Command("sh", "-c", copyCmd).CombinedOutput(); err != nil {
		t.Fatalf("copying the repository: %v\n%s", err, out)
	}
	// Before the clone has run, its copy holds only what the source had: timeline 1.
	if repoHasTimeline2(t, name) {
		t.Fatalf("precondition: the copy of the repository already holds timeline 2 before %s has run", name)
	}
	asPostgres(t, name, "pgbackrest --stanza=main --log-level-console=info --archive-mode="+archiveMode+" restore")
	asPostgres(t, name, "/usr/lib/postgresql/16/bin/pg_ctl -D /var/lib/postgresql/data -l /tmp/clone.log -w -t 90 start")
	waitReady(t, port)
}

// repoHasTimeline2 says whether a container's repository holds WAL or a history
// file of timeline 2: what a promoted clone pushes.
func repoHasTimeline2(t *testing.T, name string) bool {
	t.Helper()
	out := inContainer(t, name, "sh", "-c", "ls -R /var/lib/pgbackrest/archive/main 2>/dev/null | grep -c '^00000002' || true")
	return strings.TrimSpace(out) != "0" && strings.TrimSpace(out) != ""
}

func verifyDrill(t *testing.T, clonePort int, user, pass string) audit.DrillVerification {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := verifyRestoreDrillImpl(ctx, VerifyRestoreDrillArgs{
		Source: drillConn(pgbrSrcPort, user, pass),
		Clone:  drillConn(clonePort, user, pass),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", res.Output)
	return res
}

func TestRestoreDrillPgBackRest_ClonesMadeByARealRestore(t *testing.T) {
	needPgBackRestImage(t)
	oldInfra := infraConfig
	infraConfig = nil
	defer func() { infraConfig = oldInfra }()

	suffix := fmt.Sprintf("%d", os.Getpid())
	src := "drillpbr-src-" + suffix
	startPgBackRestSource(t, src)

	cleanName, archName := "drillpbr-clean-"+suffix, "drillpbr-arch-"+suffix
	restoredClone(t, src, cleanName, pgbrCleanPort, "off")

	// 1. A drill done properly passes, as a superuser and as an ordinary role, and
	// the clone is the same system as the source.
	var clean audit.DrillVerification
	for _, who := range []struct{ label, user, pass string }{{"superuser", "postgres", drillPass}, {"plain role", "drillro", "ro"}} {
		res := verifyDrill(t, pgbrCleanPort, who.user, who.pass)
		if res.Status != audit.DrillVerified {
			t.Errorf("%s: status %q, want verified", who.label, res.Status)
		}
		if got := resultOf(res.Checks, "archive_guard"); got.Result != "pass" {
			t.Errorf("%s: archive_guard = %q (%s)", who.label, got.Result, got.Detail)
		}
		if res.SourceSystemID == "" || res.SourceSystemID != res.CloneSystemID {
			t.Errorf("%s: a restore of the backup must share the system identifier: %q vs %q", who.label, res.SourceSystemID, res.CloneSystemID)
		}
		if who.user == "postgres" {
			clean = res
		}
	}

	// 2. The same clone with a lot more data in it: a different size class. It is
	// verified with a warning, not refused, and the estimate does not count it.
	psqlAt(t, pgbrCleanPort, "CREATE TABLE drill_bulk AS SELECT i, md5(i::text) || md5((i+1)::text) AS v FROM generate_series(1,600000) i")
	big := verifyDrill(t, pgbrCleanPort, "postgres", drillPass)
	if big.Status != audit.DrillVerifiedWithWarnings || resultOf(big.Checks, "size_class").Result != "warn" {
		t.Errorf("a clone of a different size: status %q, size_class %q (%s)", big.Status, resultOf(big.Checks, "size_class").Result, resultOf(big.Checks, "size_class").Detail)
	}

	// The estimate and the verification agree on what "comparable" is, on real sizes.
	now := time.Now().UTC()
	record := func(id string, v audit.DrillVerification) audit.DrillRecord {
		d := audit.DrillRecord{DrillID: id, SourceKey: "src", CloneKey: "clone", SourceSystemID: v.SourceSystemID, CloneSystemID: v.CloneSystemID,
			Method: audit.DrillMethodPgBackRest, Status: v.Status, Checks: v.Checks, FinishedAt: now, RecordedAt: now,
			DurationSeconds: 60, DurationSource: audit.DrillDurationReported, RestoreBytes: v.CloneBytes}
		if err := d.Validate(); err != nil {
			t.Errorf("a real verdict does not make a recordable drill: %v", err)
		}
		return d
	}
	e := availability.EstimateRecovery(nil, availability.RestoreSeries, now, availability.EstimateWindow, nil)
	availability.AddDrills(&e, []audit.DrillRecord{record("drl_clean", clean), record("drl_big", big)},
		availability.DrillInputs{RestoreBytes: clean.SourceBytes}, now, nil)
	if e.Drills.Samples != 1 || e.Drills.NotCounted["size"] != 1 || len(e.Drills.DrillIDs) != 1 || e.Drills.DrillIDs[0] != "drl_clean" {
		t.Errorf("the clean clone should count and the big one not: %+v", e.Drills)
	}

	// 3. The hazard. A clone restored with the source's archiving kept is refused,
	// and the refusal is right: left running, it pushes its own timeline into the
	// repository it was given. That repository is a copy, so the source's own is
	// untouched; this is the proof that the guard stops something real.
	restoredClone(t, src, archName, pgbrArchivePrt, "preserve")
	res := verifyDrill(t, pgbrArchivePrt, "postgres", drillPass)
	if res.Status != audit.DrillRefused || resultOf(res.Checks, "archive_guard").Result != "fail" {
		t.Errorf("a clone that still archives: status %q, archive_guard %q", res.Status, resultOf(res.Checks, "archive_guard").Result)
	}
	// It is promoted when recovery ends and archives from then on; a switch makes sure
	// there is a segment to push.
	psqlAt(t, pgbrArchivePrt, "SELECT pg_switch_wal()")
	deadline := time.Now().Add(60 * time.Second)
	for !repoHasTimeline2(t, archName) && time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
	}
	if !repoHasTimeline2(t, archName) {
		t.Error("the clone that still archives never pushed its timeline: either the hazard is not real or this test is not reproducing it")
	}
	if repoHasTimeline2(t, src) {
		t.Error("the SOURCE's repository holds a timeline the clone made: the copy was not a copy")
	}
}
