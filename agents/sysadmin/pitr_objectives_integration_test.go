//go:build integration

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

// pitrSetObjectives changes the fixture's objectives for the rest of the test.
func pitrSetObjectives(t *testing.T, rpo time.Duration, enforcement string) {
	t.Helper()
	db := infraConfig.DBServers["pgbackrest-db"]
	old := db.Objectives
	db.Objectives = &infra.Objectives{RPO: &infra.Duration{Duration: rpo}, Enforcement: &infra.Choice{Value: enforcement}}
	infraConfig.DBServers["pgbackrest-db"] = db
	t.Cleanup(func() {
		d := infraConfig.DBServers["pgbackrest-db"]
		d.Objectives = old
		infraConfig.DBServers["pgbackrest-db"] = d
	})
}

// pitrWithObjectives gives the fixture an rpo and an enforcement, and a real
// audit store for the decisions, for the length of the test.
func pitrWithObjectives(t *testing.T, rpo time.Duration, enforcement string) *audit.Store {
	t.Helper()
	pitrSetObjectives(t, rpo, enforcement)
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "audit.db")})
	if err != nil {
		t.Fatal(err)
	}
	oldAuditor := toolAuditor
	toolAuditor = audit.NewToolAuditor(store, "sysadmin_agent", "sess_it", "trace_it")
	t.Cleanup(func() { toolAuditor = oldAuditor; store.Close() })
	return store
}

// A real restore, the objective in force, no model: a refusal leaves the
// instance serving with its data, an acknowledgement lets the same plan through,
// and both decisions are in a real audit store whose chain verifies and which
// holds no password. This is what the faulttest asserts through a model, minus
// the model.
func TestIntegration_RestoreToPIT_ObjectiveEnforcement(t *testing.T) {
	ctx := pitrFixtureSetup(t)
	t.Cleanup(func() { restoreIntegrationTeardown(t) })
	oldStop := containerStopTimeout
	containerStopTimeout = 3 * time.Second // the fixture's PID 1 ignores SIGTERM
	t.Cleanup(func() { containerStopTimeout = oldStop })
	store := pitrWithObjectives(t, time.Second, "refuse")

	fixturePSQL(t, "DROP TABLE IF EXISTS pitr_obj; CREATE TABLE pitr_obj (id int, phase text)")
	fixturePSQL(t, "INSERT INTO pitr_obj SELECT g, 'keep' FROM generate_series(1, 5) g")
	pitrIncrBackup(t)
	time.Sleep(3 * time.Second)
	target := fixturePSQL(t, "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')")
	time.Sleep(2 * time.Second)
	fixturePSQL(t, "INSERT INTO pitr_obj SELECT g, 'lose' FROM generate_series(6, 10) g")
	time.Sleep(2 * time.Second) // the loss is now a few seconds against a one-second rpo

	plan, err := planPITRWithEstimate(ctx, PlanPITRArgs{Target: "pgbackrest-db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: target})
	if err != nil || plan.Status != PITRFeasible || !plan.InstanceServing {
		t.Fatalf("plan: %+v, %v", plan, err)
	}
	if plan.DataLossEstimate == nil || plan.DataLossEstimate.Objective == nil || plan.DataLossEstimate.Objective.Status != RPOExceeds {
		t.Fatalf("the plan should show the rpo exceeded: %+v", plan.DataLossEstimate)
	}
	args := RestoreToPITArgs{Target: "pgbackrest-db", Stanza: "main", RecoveryTargetType: TargetTime, RecoveryTargetVal: target,
		PlanDigest: plan.PlanDigest, AllowStopServing: true}

	// Refused: stop authorized, breach not acknowledged. Nothing was touched.
	res, err := restoreToPITImpl(ctx, args)
	if err == nil || !strings.Contains(err.Error(), "refusing to restore") || !strings.Contains(err.Error(), "The instance was not stopped") {
		t.Fatalf("an exceeded rpo without the acknowledgement: err = %v", err)
	}
	if res.Stopped != "" || res.SavedWAL != "" {
		t.Errorf("the refused restore did something: %+v", res)
	}
	if got := fixturePSQL(t, "SELECT count(*) FROM pitr_obj"); got != "10" {
		t.Fatalf("the refusal changed the data or stopped the instance: %q rows", got)
	}

	// "refuse" is unconditional: an acknowledgement does not open it.
	args.AcknowledgeObjectiveBreach = true
	if _, err := restoreToPITImpl(ctx, args); err == nil || !strings.Contains(err.Error(), "refusing to restore") {
		t.Fatalf("an acknowledgement got past enforcement=refuse: err = %v", err)
	}
	if got := fixturePSQL(t, "SELECT count(*) FROM pitr_obj"); got != "10" {
		t.Fatalf("the refusal changed the data: %q rows", got)
	}

	// With "acknowledge" the same plan needs the acknowledgement, and with it goes through.
	pitrSetObjectives(t, time.Second, "acknowledge")
	args.AcknowledgeObjectiveBreach = false
	if _, err := restoreToPITImpl(ctx, args); err == nil || !strings.Contains(err.Error(), "refusing to restore") {
		t.Fatalf("enforcement=acknowledge without the acknowledgement: err = %v", err)
	}
	args.AcknowledgeObjectiveBreach = true
	res, err = restoreToPITImpl(ctx, args)
	if err != nil {
		t.Fatalf("acknowledged restore: %v", err)
	}
	if res.DataLoss == nil || res.DataLoss.Objective == nil || res.DataLoss.Objective.Status != RPOExceeds || res.SavedWAL == "" {
		t.Errorf("result = %+v", res)
	}
	mark := time.Now()
	if rr, err := restartContainerImpl(ctx, RestartContainerArgs{Target: "pgbackrest-db", Reason: "start after point-in-time restore"}); err != nil || !rr.Success {
		t.Fatalf("restart_container: %+v, %v", rr, err)
	}
	waitForRecoveryLog(t, mark, "database system is ready to accept connections")
	if got := fixturePSQL(t, "SELECT phase || ':' || count(*) FROM pitr_obj GROUP BY phase ORDER BY phase"); got != "keep:5" {
		t.Errorf("rows after the acknowledged restore = %q, want only keep:5", got)
	}
	fixturePSQL(t, "DROP TABLE pitr_obj")

	// The record: plan, then the refusal, then the decision to proceed.
	events, err := store.Query(ctx, audit.QueryOptions{EventType: audit.EventTypeRecoveryDecision})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range events {
		d := ev.RecoveryDecision
		got = append(got, d.Stage+":"+d.Decision)
		if d.Objectives == nil || d.Objectives.RPOSeconds == nil || *d.Objectives.RPOSeconds != 1 || d.Objectives.Enforcement == "" {
			t.Errorf("%s: objectives in force not recorded: %+v", d.Stage, d.Objectives)
		}
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "execution:refused") || !strings.Contains(joined, "execution:proceeded") {
		t.Errorf("recorded decisions = %v, want an execution refusal and an execution proceed", got)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "testpass") {
		t.Errorf("the connection password reached the audit store: %s", raw)
	}
	if st, err := store.VerifyIntegrity(ctx); err != nil || !st.Valid {
		t.Errorf("the chain over real decisions does not verify: %+v, %v", st, err)
	}
}

// The lower bound must never exceed the truth. Whatever an LSN-target plan
// claims the loss is "at least", the real time since the commit it would
// discard cannot be less. A simulator can only repeat the author's assumptions;
// a real archive and a real clock cannot.
func TestIntegration_PlanPITR_LowerBoundIsSound(t *testing.T) {
	ctx := pitrFixtureSetup(t)
	t.Cleanup(func() { restoreIntegrationTeardown(t) })
	pitrWithObjectives(t, time.Hour, "warn")

	fixturePSQL(t, "DROP TABLE IF EXISTS pitr_lb; CREATE TABLE pitr_lb (id int)")
	fixturePSQL(t, "INSERT INTO pitr_lb VALUES (1)")
	pitrIncrBackup(t)
	lsn := fixturePSQL(t, "SELECT pg_current_wal_lsn()")
	committed := time.Now()
	fixturePSQL(t, "INSERT INTO pitr_lb VALUES (2)")
	for i := 0; i < 3; i++ { // let the archive take the WAL holding the target
		fixturePSQL(t, "SELECT pg_switch_wal()")
		time.Sleep(3 * time.Second)
	}
	time.Sleep(5 * time.Second)

	plan, err := planPITRWithEstimate(ctx, PlanPITRArgs{Target: "pgbackrest-db", Stanza: "main", RecoveryTargetType: TargetLSN, RecoveryTargetVal: lsn})
	if err != nil {
		t.Fatal(err)
	}
	est := plan.DataLossEstimate
	if est == nil {
		t.Fatalf("no estimate: %+v", plan)
	}
	elapsed := int64(time.Since(committed).Seconds())
	if est.WindowAtLeastSeconds == nil {
		t.Logf("no lower bound on this fixture (segments seconds apart, inside the %s skew margin): nothing to check, which is the safe answer", clockSkewMargin)
	} else if *est.WindowAtLeastSeconds > elapsed {
		t.Errorf("the lower bound %ds is larger than the %ds that really passed since the commit: it would block a restore on a lie (%s)",
			*est.WindowAtLeastSeconds, elapsed, est.AtLeastBasis)
	}
	fixturePSQL(t, "DROP TABLE pitr_lb")
}
