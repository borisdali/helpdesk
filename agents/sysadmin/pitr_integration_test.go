//go:build integration

// Integration test for plan_point_in_time_recovery against the real pgBackRest
// test container (see restore_from_backup_integration_test.go for the
// fixture). Read-only: nothing here changes the container's data.
//
//	go test -tags integration -timeout 300s ./agents/sysadmin/... -run PlanPITR
package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/infra"
	"helpdesk/testing/testutil"
)

func TestIntegration_PlanPITR_RealRepo(t *testing.T) {
	skipIfNoPgBackRestDocker(t)

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := infra.Load(filepath.Join(repoRoot, "testing", "testing.infra.json"))
	if err != nil {
		t.Fatalf("loading infra config: %v", err)
	}
	oldInfra := infraConfig
	infraConfig = cfg
	t.Cleanup(func() { infraConfig = oldInfra })

	ctx := context.Background()
	connStr := "host=localhost port=15435 dbname=testdb user=postgres password=testpass"

	status, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-db"})
	if err != nil || len(status.Backups) == 0 {
		t.Fatalf("precondition: want a backup in the fixture: %v", err)
	}

	// A target before the oldest backup finished cannot be restored to.
	oldest := status.Backups[0].StopTime
	for _, b := range status.Backups {
		if b.StopTime.Before(oldest) {
			oldest = b.StopTime
		}
	}
	plan, err := planPITRImpl(ctx, PlanPITRArgs{Target: "pgbackrest-db",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: oldest.Add(-time.Minute).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != PITRNotFeasible || !strings.Contains(plan.Detail, "no backup finished before") {
		t.Errorf("target before every backup: %+v", plan)
	}

	// A target taken from the database's own clock, then WAL written and
	// switched after it: once that WAL is archived, the target is inside the
	// archive and the plan is feasible.
	dbNow := fixturePSQL(t, "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')")
	time.Sleep(2 * time.Second)

	deadline := time.Now().Add(90 * time.Second)
	for {
		if err := testutil.RunSQLString(ctx, connStr,
			"CREATE TABLE IF NOT EXISTS walcheck_filler (x int); INSERT INTO walcheck_filler SELECT generate_series(1, 1000); SELECT pg_switch_wal();"); err != nil {
			t.Fatalf("generating WAL: %v", err)
		}
		time.Sleep(3 * time.Second)
		plan, err = planPITRImpl(ctx, PlanPITRArgs{Target: "pgbackrest-db", RecoveryTargetType: TargetTime, RecoveryTargetVal: dbNow})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status == PITRFeasible {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("target %s never became feasible: %+v", dbNow, plan)
		}
	}
	if plan.PlanDigest == "" || plan.BackupLabel == "" || plan.ExpectedTimeline != plan.TargetTimeline+1 || plan.WALContinuity == nil || plan.WALContinuity.Status != WALContinuous {
		t.Errorf("feasible plan is incomplete: %+v", plan)
	}
}

// pitrFixtureSetup loads the infra config and returns a context.
func pitrFixtureSetup(t *testing.T) context.Context {
	t.Helper()
	skipIfNoPgBackRestDocker(t)
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := infra.Load(filepath.Join(repoRoot, "testing", "testing.infra.json"))
	if err != nil {
		t.Fatalf("loading infra config: %v", err)
	}
	oldInfra := infraConfig
	infraConfig = cfg
	t.Cleanup(func() { infraConfig = oldInfra })
	return context.Background()
}

// A real point-in-time restore on the fixture: rows committed before the
// target survive, rows committed after it are gone, PostgreSQL stops at the
// target on a new timeline, and verify_restore_recovery says so from the
// container's own log. A restore while the server is up is refused first.
func TestIntegration_RestoreToPIT_RealRestore(t *testing.T) {
	ctx := pitrFixtureSetup(t)
	t.Cleanup(func() { restoreIntegrationTeardown(t) })
	connStr := "host=localhost port=15435 dbname=testdb user=postgres password=testpass"

	fixturePSQL(t, "DROP TABLE IF EXISTS pitr_e2e; CREATE TABLE pitr_e2e (id int, phase text)")
	fixturePSQL(t, "INSERT INTO pitr_e2e SELECT g, 'keep' FROM generate_series(1, 5) g")
	// A backup that finishes before the target.
	if out, err := exec.Command("docker", "exec", "-u", "postgres", restoreIntegrationContainer,
		"pgbackrest", "--stanza=main", "--type=incr", "backup").CombinedOutput(); err != nil {
		t.Fatalf("incr backup: %v: %s", err, out)
	}
	time.Sleep(2 * time.Second)
	// The database's own clock, whole seconds (rounded down), a moment after
	// the rows to keep and well before the ones to lose.
	time.Sleep(1500 * time.Millisecond)
	target := fixturePSQL(t, "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')")
	time.Sleep(2 * time.Second)
	fixturePSQL(t, "INSERT INTO pitr_e2e SELECT g, 'lose' FROM generate_series(6, 10) g")

	// Wait for the target to be inside the archive.
	var plan PITRPlan
	var err error
	deadline := time.Now().Add(90 * time.Second)
	for {
		fixturePSQL(t, "SELECT pg_switch_wal()")
		time.Sleep(3 * time.Second)
		plan, err = planPITRImpl(ctx, PlanPITRArgs{Target: "pgbackrest-db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: target})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status == PITRFeasible {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("target %s never became feasible: %+v", target, plan)
		}
	}
	args := RestoreToPITArgs{Target: "pgbackrest-db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: target, PlanDigest: plan.PlanDigest}

	// Refused while the server is up; nothing changed.
	if _, err := restoreToPITImpl(ctx, args); err == nil || !strings.Contains(err.Error(), "still accepting connections") {
		t.Fatalf("restore on a live server: err = %v, want a refusal", err)
	}
	if got := fixturePSQL(t, "SELECT count(*) FROM pitr_e2e"); got != "10" {
		t.Fatalf("the refused restore changed the data: %s rows", got)
	}

	// Stop postgres (the fixture's container keeps running).
	if out, err := exec.Command("docker", "exec", "-u", "postgres", restoreIntegrationContainer,
		"pg_ctl", "-D", "/var/lib/postgresql/data", "stop", "-m", "fast", "-w", "-t", "30").CombinedOutput(); err != nil {
		t.Fatalf("stopping postgres: %v: %s", err, out)
	}

	res, err := restoreToPITImpl(ctx, args)
	if err != nil {
		t.Fatalf("restoreToPITImpl: %v", err)
	}
	if res.SavedWAL == "" || res.Prediction == nil || res.Prediction.TargetType != TargetTime || res.Prediction.ExpectedTimeline != plan.TargetTimeline+1 {
		t.Errorf("result = %+v", res)
	}

	mark := time.Now()
	if err := exec.CommandContext(ctx, "docker", "restart", restoreIntegrationContainer).Run(); err != nil {
		t.Fatalf("docker restart: %v", err)
	}
	log := waitForRecoveryLog(t, mark, "database system is ready to accept connections")
	if !strings.Contains(log, "recovery stopping") {
		t.Errorf("PostgreSQL did not stop at a target:\n%s", log)
	}

	verified, err := verifyRestoreRecoveryImpl(ctx, VerifyRestoreRecoveryArgs{Target: "pgbackrest-db"})
	if err != nil {
		t.Fatal(err)
	}
	if verified.Status != RestoreVerified || verified.NewTimeline != plan.TargetTimeline+1 || verified.StopLine == "" {
		t.Errorf("verify_restore_recovery = %+v", verified)
	}

	if got := fixturePSQL(t, "SELECT phase || ':' || count(*) FROM pitr_e2e GROUP BY phase ORDER BY phase"); got != "keep:5" {
		t.Errorf("rows after the point-in-time restore = %q, want only keep:5", got)
	}
	if err := testutil.RunSQLString(ctx, connStr, "DROP TABLE pitr_e2e"); err != nil {
		t.Logf("cleanup: %v", err)
	}
}
