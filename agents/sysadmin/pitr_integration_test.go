//go:build integration

// Integration test for plan_point_in_time_recovery against the real pgBackRest
// test container (see restore_from_backup_integration_test.go for the
// fixture). Read-only: nothing here changes the container's data.
//
//	go test -tags integration -timeout 300s ./agents/sysadmin/... -run PlanPITR
package main

import (
	"context"
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
