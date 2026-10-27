package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// The multi_timeline fixtures: one full backup 20261007-043435F (stop
// 2026-10-07T04:34:37Z, LSN 0/3000100) and an archive that runs through
// timeline 30 to 2026-10-11, continuous.
func fixtureRunner(t *testing.T) runnerFunc {
	t.Helper()
	wal := fakeWALRun(t, readFixture(t, "multi_timeline_000000010000000000000002.gz"), nil)
	return func(_ context.Context, name string, args []string, _ []string) (string, error) {
		if name != "pgbackrest" {
			return "", fmt.Errorf("unexpected command %s", name)
		}
		if len(args) > 0 && args[0] == "info" {
			return readFixture(t, "multi_timeline_info.json"), nil
		}
		return wal(args)
	}
}

func TestPlanPITR_EndToEnd(t *testing.T) {
	old := cmdRunner
	cmdRunner = fixtureRunner(t)
	t.Cleanup(func() { cmdRunner = old })

	cases := []struct {
		name     string
		typ, val string
		status   string
		contains string
	}{
		{"time inside the archive", TargetTime, "2026-10-09T00:00:00Z", PITRFeasible, "plausible"},
		{"time after the archive end", TargetTime, "2026-10-12T00:00:00Z", PITRNotFeasible, "never archived"},
		{"time before every backup", TargetTime, "2026-10-01T00:00:00Z", PITRNotFeasible, "no backup finished before"},
		{"lsn inside the archive", TargetLSN, "0/4000000", PITRFeasible, "plausible"},
		{"lsn after the archive end", TargetLSN, "1/0", PITRNotFeasible, "never archived"},
		{"lsn before the backup", TargetLSN, "0/1000000", PITRNotFeasible, "no backup finished before"},
	}
	for _, c := range cases {
		plan, err := planPITRImpl(context.Background(), PlanPITRArgs{RecoveryTargetType: c.typ, RecoveryTargetVal: c.val})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if plan.Status != c.status || !strings.Contains(plan.Detail, c.contains) {
			t.Errorf("%s: status=%s detail=%s", c.name, plan.Status, plan.Detail)
		}
		if (plan.Status == PITRFeasible) != (plan.PlanDigest != "") {
			t.Errorf("%s: digest %q for status %s", c.name, plan.PlanDigest, plan.Status)
		}
	}

	plan, _ := planPITRImpl(context.Background(), PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"})
	if plan.BackupLabel != "20261007-043435F" || plan.TargetTimeline != 30 || plan.ExpectedTimeline != 31 ||
		plan.NormalizedTarget != "2026-10-09 00:00:00+00" || !strings.Contains(plan.DataLoss, "discarded") {
		t.Errorf("plan = %+v", plan)
	}
	// The same decision gives the same digest; a different target does not.
	again, _ := planPITRImpl(context.Background(), PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"})
	other, _ := planPITRImpl(context.Background(), PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:01Z"})
	if again.PlanDigest != plan.PlanDigest || other.PlanDigest == plan.PlanDigest {
		t.Errorf("digests: %s %s %s", plan.PlanDigest, again.PlanDigest, other.PlanDigest)
	}
}

func TestPlanPITR_InvalidAndUnhealthy(t *testing.T) {
	old := cmdRunner
	t.Cleanup(func() { cmdRunner = old })
	cmdRunner = fixtureRunner(t)

	for _, bad := range []PlanPITRArgs{
		{RecoveryTargetType: TargetTime, RecoveryTargetVal: "yesterday"},
		{RecoveryTargetType: "xid", RecoveryTargetVal: "5"},
		{RecoveryTargetType: TargetTime, RecoveryTargetVal: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
	} {
		if _, err := planPITRImpl(context.Background(), bad); err == nil {
			t.Errorf("%+v: expected an error", bad)
		}
	}

	// A stanza pgBackRest calls unhealthy is not a plan.
	cmdRunner = runnerFunc(func(_ context.Context, _ string, args []string, _ []string) (string, error) {
		return `[{"name":"main","status":{"code":2,"message":"missing stanza path"},"backup":[],"archive":[],"db":[]}]`, nil
	})
	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"})
	if err != nil || plan.Status != PITRNotFeasible || !strings.Contains(plan.Detail, "unhealthy") {
		t.Errorf("unhealthy stanza: %+v, %v", plan, err)
	}
}

func TestAssessPITRPlan_WALProblems(t *testing.T) {
	b := PgBackRestBackup{Label: "L", StopTime: time.Date(2026, 10, 7, 4, 34, 37, 0, time.UTC)}
	tgt := RecoveryTarget{TargetTime, "2026-10-09T00:00:00Z"}
	norm := "2026-10-09 00:00:00+00"
	good := WALContinuity{Status: WALContinuous, LatestTimeline: 30, ArchiveEndWAL: "0000001E0000000000000005", ArchiveEndTime: "2026-10-11T20:30:09Z", SegmentSizeBytes: 16 << 20}

	broken := good
	broken.Status, broken.Detail = WALBroken, "WAL segment X is missing"
	unv := good
	unv.Status, unv.Detail = WALUnverified, "cannot list"
	noTLI := good
	noTLI.LatestTimeline = 0
	badEnd := good
	badEnd.ArchiveEndTime = "garbage"
	noSize := good
	noSize.SegmentSizeBytes = 0

	for _, c := range []struct {
		name   string
		tgt    RecoveryTarget
		norm   string
		wal    *WALContinuity
		status string
	}{
		{"nil check", tgt, norm, nil, PITRUnverified},
		{"broken chain", tgt, norm, &broken, PITRNotFeasible},
		{"unverified chain", tgt, norm, &unv, PITRUnverified},
		{"no timeline", tgt, norm, &noTLI, PITRUnverified},
		{"bad archive end time", tgt, norm, &badEnd, PITRUnverified},
		{"lsn without segment size", RecoveryTarget{TargetLSN, "0/4000000"}, "0/4000000", &noSize, PITRUnverified},
		{"unknown type", RecoveryTarget{"xid", "1"}, "1", &good, PITRUnverified},
	} {
		if p := assessPITRPlan("main", c.tgt, c.norm, b, c.wal); p.Status != c.status || p.PlanDigest != "" {
			t.Errorf("%s: %+v", c.name, p)
		}
	}
}

// The audited tool call carries the target and the plan, so the approver's
// plan_digest can be matched to what was shown.
func TestPlanPITRTool_RecordsToolExecution(t *testing.T) {
	old := cmdRunner
	cmdRunner = fixtureRunner(t)
	defer func() { cmdRunner = old }()
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()

	plan, err := planPITRTool(newSysadminTestContext(), PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("planPITRTool: %+v, %v", plan, err)
	}
	events, err := store.Query(context.Background(), audit.QueryOptions{ToolName: "plan_point_in_time_recovery", EventType: audit.EventTypeToolExecution})
	if err != nil || len(events) == 0 {
		t.Fatalf("no tool_execution event: %v", err)
	}
	if out := events[0].Tool.Result; !strings.Contains(out, plan.PlanDigest) {
		t.Errorf("audited output lacks the plan digest %s: %s", plan.PlanDigest, out)
	}
}

// POST /tool/plan_point_in_time_recovery (faulttest, the gateway) passes JSON
// maps, not the Go struct: the argument names are part of the contract.
func TestPlanPITR_DirectRegistry(t *testing.T) {
	old := cmdRunner
	cmdRunner = fixtureRunner(t)
	defer func() { cmdRunner = old }()

	fn, ok := NewSysadminDirectRegistry().Get("plan_point_in_time_recovery")
	if !ok {
		t.Fatal("plan_point_in_time_recovery is not in the direct registry")
	}
	out, err := fn(context.Background(), map[string]any{
		"recovery_target_type":  "time",
		"recovery_target_value": "2026-10-09T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	var plan PITRPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("result is not a PITRPlan: %v: %s", err, out)
	}
	if plan.Status != PITRFeasible || plan.PlanDigest == "" {
		t.Errorf("plan = %+v", plan)
	}
	if _, err := fn(context.Background(), map[string]any{"recovery_target_type": "time", "recovery_target_value": "tomorrow"}); err == nil {
		t.Error("an invalid target must be an error through the registry too")
	}
}

// When the newest backup is after the target, the WAL check must be run on
// the backup that was selected (its own start segment and archive), not on
// the newest one.
func TestPlanPITR_ChecksTheSelectedBackup(t *testing.T) {
	// The fixture's one backup (20261007-043435F) plus a newer one that
	// finished after the target. Its start segment would give a different
	// answer: it points at a segment that is not in the archive.
	info := readFixture(t, "multi_timeline_info.json")
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(info), &parsed); err != nil {
		t.Fatal(err)
	}
	backups := parsed[0]["backup"].([]any)
	newer := map[string]any{}
	for k, v := range backups[0].(map[string]any) {
		newer[k] = v
	}
	newer["label"] = "20261010-120000F"
	newer["timestamp"] = map[string]any{"start": 1791640000, "stop": 1791640010} // 2026-10-10
	newer["archive"] = map[string]any{"start": "0000000100000000000000FF", "stop": "0000000100000000000000FF"}
	newer["lsn"] = map[string]any{"start": "0/FFFF0028", "stop": "0/FFFF0100"}
	parsed[0]["backup"] = append(backups, newer)
	patched, _ := json.Marshal(parsed)

	wal := fakeWALRun(t, readFixture(t, "multi_timeline_000000010000000000000002.gz"), nil)
	old := cmdRunner
	cmdRunner = runnerFunc(func(_ context.Context, _ string, args []string, _ []string) (string, error) {
		if len(args) > 0 && args[0] == "info" {
			return string(patched), nil
		}
		return wal(args)
	})
	defer func() { cmdRunner = old }()

	// Before the newer backup: the original is selected and is continuous.
	plan, err := planPITRImpl(context.Background(), PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"})
	if err != nil || plan.BackupLabel != "20261007-043435F" || plan.Status != PITRFeasible {
		t.Fatalf("target before the newer backup: %+v, %v", plan, err)
	}
	// After it: the newer one is selected, and its missing WAL is what the
	// plan reports (proving the check ran on the selected backup).
	plan, err = planPITRImpl(context.Background(), PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-10T18:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.BackupLabel != "20261010-120000F" || plan.Status == PITRFeasible {
		t.Errorf("target after the newer backup: %+v", plan)
	}
}

func TestPlanPITR_StatusReadFails(t *testing.T) {
	old := cmdRunner
	cmdRunner = runnerFunc(func(context.Context, string, []string, []string) (string, error) {
		return "", fmt.Errorf("pgbackrest: command not found")
	})
	defer func() { cmdRunner = old }()

	if _, err := planPITRImpl(context.Background(), PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"}); err == nil ||
		!strings.Contains(err.Error(), "reading the backup state") {
		t.Fatalf("err = %v", err)
	}
	// Through the tool wrapper the failure is audited as an error.
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()
	if _, err := planPITRTool(newSysadminTestContext(), PlanPITRArgs{RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"}); err == nil {
		t.Fatal("expected an error")
	}
	events, qerr := store.Query(context.Background(), audit.QueryOptions{ToolName: "plan_point_in_time_recovery", EventType: audit.EventTypeToolExecution})
	if qerr != nil || len(events) == 0 || events[0].Tool.Error == "" {
		t.Fatalf("failed call not audited with its error: %v %+v", qerr, events)
	}
}
