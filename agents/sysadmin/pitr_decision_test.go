package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
)

// decisions returns the recovery_decision events in the store, by stage.
// The execution record keeps the size of the cluster the restore wrote back, so
// restore times can later be compared across a server's growth. A plan with no
// size records none: nothing is made up.
func TestRecordExecutionDecision_KeepsTheSizeOfTheRestoredCluster(t *testing.T) {
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()
	args := RestoreToPITArgs{Target: "pgbackrest_db", PlanDigest: "0123456789abcdef"}
	if err := recordExecutionDecision(context.Background(), args, PITRPlan{BackupDatabaseBytes: 31048775}, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := recordExecutionDecision(context.Background(), args, PITRPlan{}, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	got := decisions(t, store)[audit.RecoveryStageExecution]
	if len(got) != 2 {
		t.Fatalf("decisions = %+v", got)
	}
	sized, unsized := got[0], got[1]
	if sized.RestoreBytes == nil {
		sized, unsized = got[1], got[0]
	}
	if sized.RestoreBytes == nil || *sized.RestoreBytes != 31048775 {
		t.Errorf("sized = %+v", sized.RestoreBytes)
	}
	if unsized.RestoreBytes != nil {
		t.Errorf("a size was recorded for a plan with none: %d", *unsized.RestoreBytes)
	}
}

// The plan carries the size of the backup it selected, under the name the record
// and the CLI read, and leaves it out when pgBackRest reported none.
func TestAssessPITRPlan_CarriesTheSelectedBackupsSize(t *testing.T) {
	plan := assessPITRPlan("main", RecoveryTarget{}, "2026-10-28 14:05:00+00", PgBackRestBackup{Label: "L", DatabaseBytes: 31048775}, nil)
	raw, _ := json.Marshal(plan)
	if !strings.Contains(string(raw), `"backup_database_bytes":31048775`) {
		t.Errorf("plan json = %s", raw)
	}
	raw, _ = json.Marshal(assessPITRPlan("main", RecoveryTarget{}, "x", PgBackRestBackup{Label: "L"}, nil))
	if strings.Contains(string(raw), "backup_database_bytes") {
		t.Errorf("a size was invented: %s", raw)
	}
}

func decisions(t *testing.T, store *audit.Store) map[string][]audit.RecoveryDecision {
	t.Helper()
	events, err := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypeRecoveryDecision})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]audit.RecoveryDecision{}
	for _, ev := range events {
		if ev.RecoveryDecision == nil {
			t.Fatalf("a recovery_decision event with no payload: %+v", ev)
		}
		out[ev.RecoveryDecision.Stage] = append(out[ev.RecoveryDecision.Stage], *ev.RecoveryDecision)
	}
	return out
}

// The record shows the window and budget the approver saw, including a window
// that was only the built-in default, and nothing when no budget could be
// computed (never a default laid over a setting that could not be read).
func TestSnapshotOf_RecordsTheAvailabilityBudget(t *testing.T) {
	declared := infra.ResolvedObjectives{
		RTO:                infra.ResolvedDuration{Set: true, Value: 30 * time.Minute, Source: "environment:prod"},
		AvailabilitySLO:    infra.ResolvedPercent{Set: true, Value: 99.9, Source: "environment:prod"},
		AvailabilityWindow: infra.ResolvedDuration{Set: true, Value: 90 * 24 * time.Hour, Source: "environment:prod"},
	}
	s := snapshotOf(declared, "")
	if s.AvailabilityWindowSeconds == nil || *s.AvailabilityWindowSeconds != 90*24*3600 ||
		s.AvailabilityWindowSource != "environment:prod" ||
		s.AvailabilityBudgetSeconds == nil || *s.AvailabilityBudgetSeconds != 129*60+36 {
		t.Errorf("declared window: %+v", s)
	}

	// No window set anywhere: the default is recorded, and called what it is.
	defaulted := declared
	defaulted.AvailabilityWindow = infra.ResolvedDuration{}
	s = snapshotOf(defaulted, "")
	if s.AvailabilityWindowSeconds == nil || *s.AvailabilityWindowSeconds != 30*24*3600 ||
		s.AvailabilityWindowSource != infra.SourceBuiltin ||
		*s.AvailabilityBudgetSeconds != 43*60+12 {
		t.Errorf("default window: %+v", s)
	}

	// An unreadable window, or an unreadable slo, leaves no budget in the record.
	bad := declared
	bad.AvailabilityWindow, bad.AvailabilityWindowInvalid = infra.ResolvedDuration{}, true
	if s = snapshotOf(bad, ""); s.AvailabilityWindowSeconds != nil || s.AvailabilityBudgetSeconds != nil || s.AvailabilityWindowSource != "" {
		t.Errorf("an unreadable window was recorded: %+v", s)
	}
	noSLO := infra.ResolvedObjectives{RTO: declared.RTO}
	if s = snapshotOf(noSLO, ""); s.AvailabilityWindowSeconds != nil || s.AvailabilityBudgetSeconds != nil {
		t.Errorf("a budget was recorded with no slo: %+v", s)
	}
}

func TestSnapshotOf_RecordsWhatWasInForceAndWhereItCameFrom(t *testing.T) {
	res := infra.ResolvedObjectives{
		Environment:        "prod",
		RPO:                infra.ResolvedDuration{Set: true, Value: 15 * 60 * 1e9, Source: "environment:prod"},
		RTO:                infra.ResolvedDuration{Set: true, Value: 30 * 60 * 1e9, Source: "default"},
		AvailabilitySLO:    infra.ResolvedPercent{Set: true, Value: 99.95, Source: "database"},
		RecoveryPreference: infra.ResolvedChoice{Set: true, Value: "data_first", Source: "environment:prod"},
		Enforcement:        infra.ResolvedChoice{Set: true, Value: "refuse", Source: "database"},
		Problems:           []string{"defaults.objectives: something"},
	}
	s := snapshotOf(res, "digest0123456789")
	if *s.RPOSeconds != 900 || s.RPOSource != "environment:prod" || *s.RTOSeconds != 1800 || s.RTOSource != "default" ||
		*s.AvailabilitySLO != 99.95 || s.AvailabilitySource != "database" ||
		s.RecoveryPreference != "data_first" || s.PreferenceSource != "environment:prod" ||
		s.Enforcement != "refuse" || s.EnforcementSource != "database" ||
		s.Environment != "prod" || s.ConfigDigest != "digest0123456789" || len(s.Problems) != 1 {
		t.Errorf("snapshot = %+v", s)
	}

	// What is not set is absent, not zero: an rpo of 0 means "no loss tolerated".
	none := snapshotOf(infra.ResolvedObjectives{}, "")
	if none.RPOSeconds != nil || none.RTOSeconds != nil || none.AvailabilitySLO != nil || none.RecoveryPreference != "" || none.EnforcementSource != "" {
		t.Errorf("an unset objective was recorded as a value: %+v", none)
	}
	if none.Enforcement != "warn" {
		t.Errorf("enforcement = %q: unset is warn, and the record says what was applied", none.Enforcement)
	}
	zero := snapshotOf(infra.ResolvedObjectives{RPO: infra.ResolvedDuration{Set: true, Source: "database"}}, "")
	if zero.RPOSeconds == nil || *zero.RPOSeconds != 0 {
		t.Errorf("an rpo of zero must be recorded as zero: %+v", zero)
	}
	// An enforcement that could not be trusted is recorded as what was applied.
	if bad := snapshotOf(infra.ResolvedObjectives{EnforcementInvalid: true}, ""); bad.Enforcement != "acknowledge" {
		t.Errorf("enforcement = %q", bad.Enforcement)
	}
}

func TestPlanDecision_IsRecordedForEveryPlanShown(t *testing.T) {
	enforced(t, "15m", "acknowledge")
	infraConfig.Digest = "0123456789abcdef"
	_, _ = liveEnv(t)
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()

	plan, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("%+v, %v", plan, err)
	}
	got := decisions(t, store)[audit.RecoveryStagePlan]
	if len(got) != 1 {
		t.Fatalf("plan decisions = %d: %+v", len(got), got)
	}
	d := got[0]
	if d.Decision != audit.RecoveryDecisionPlanned || d.Tool != "plan_point_in_time_recovery" || d.Target != "pgbackrest_db" ||
		d.RecoveryTarget != "time="+liveTarget || d.PlanDigest != plan.PlanDigest || d.PlanStatus != PITRFeasible {
		t.Errorf("decision = %+v", d)
	}
	// What was required of this database, from where, and which config.
	o := d.Objectives
	if o == nil || *o.RPOSeconds != 900 || o.RPOSource != "environment:prod" || o.Enforcement != "acknowledge" || o.Environment != "prod" || o.ConfigDigest != "0123456789abcdef" {
		t.Errorf("objectives = %+v", o)
	}
	// What was offered and how it stood.
	if len(d.Options) != 1 {
		t.Fatalf("options = %+v", d.Options)
	}
	opt := d.Options[0]
	if opt.Name != pitrOptionName || opt.Chosen || opt.Verdict != RPOExceeds || opt.Gate != GateAcknowledge ||
		opt.LossBasis != lossBasisLive || opt.LossSeconds == nil || *opt.LossSeconds != int64(8*86400+10*3600) || opt.LossUpperBound {
		t.Errorf("option = %+v", opt)
	}
	// Nothing was chosen or approved by a plan.
	if d.Approval != nil {
		t.Errorf("a plan carries no approval: %+v", d.Approval)
	}
	if status, err := store.VerifyIntegrity(context.Background()); err != nil || !status.Valid {
		t.Errorf("chain = %+v, %v", status, err)
	}
}

// The budget reaches the record through the real path: the config the agent
// loaded, the plan the operator is shown, the event the plan writes. A unit
// test of snapshotOf alone would not notice if the plan stopped passing it.
func TestPlanDecision_RecordsTheAvailabilityBudgetFromTheLoadedConfig(t *testing.T) {
	for _, tc := range []struct {
		name       string
		window     *infra.Duration
		wantWindow int64
		wantBudget int64 // 99.9% of the window, to the nearest second
		wantSource string
	}{
		{"a window the database sets", &infra.Duration{Duration: 7 * 24 * time.Hour}, 7 * 86400, 605, "database"}, // 604.8s
		{"no window: the built-in default, called that", nil, 30 * 86400, 2592, infra.SourceBuiltin},              // 43m12s
	} {
		t.Run(tc.name, func(t *testing.T) {
			objectiveInfra(t, "15m", func(db *infra.DBServer, _ *infra.Config) {
				db.Objectives = &infra.Objectives{
					RTO:                &infra.Duration{Duration: 30 * time.Minute},
					AvailabilitySLO:    &infra.Percent{Value: 99.9},
					AvailabilityWindow: tc.window,
				}
			})
			_, _ = liveEnv(t)
			store, cleanup := withRealToolAuditor(t)
			defer cleanup()

			if _, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
				RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget}); err != nil {
				t.Fatal(err)
			}
			got := decisions(t, store)[audit.RecoveryStagePlan]
			if len(got) != 1 || got[0].Objectives == nil {
				t.Fatalf("plan decisions = %+v", got)
			}
			o := got[0].Objectives
			wantBudget := tc.wantBudget
			if o.AvailabilityWindowSeconds == nil || *o.AvailabilityWindowSeconds != tc.wantWindow ||
				o.AvailabilityWindowSource != tc.wantSource ||
				o.AvailabilityBudgetSeconds == nil || *o.AvailabilityBudgetSeconds != wantBudget {
				t.Errorf("recorded window/budget = %v / %v from %q, want %d / %d from %q",
					deref(o.AvailabilityWindowSeconds), deref(o.AvailabilityBudgetSeconds), o.AvailabilityWindowSource,
					tc.wantWindow, wantBudget, tc.wantSource)
			}
			if status, err := store.VerifyIntegrity(context.Background()); err != nil || !status.Valid {
				t.Errorf("chain = %+v, %v", status, err)
			}
		})
	}
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// A plan that cannot be restored is on record too, with why.
func TestPlanDecision_RecordsAPlanThatIsNotFeasible(t *testing.T) {
	withPgBackRestDockerInfra(t)
	sim, _ := liveEnv(t)
	sim.up = false // down, and a target past the end of the archive: not feasible
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()

	plan, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || plan.Status != PITRNotFeasible {
		t.Fatalf("%+v, %v", plan, err)
	}
	got := decisions(t, store)[audit.RecoveryStagePlan]
	if len(got) != 1 || got[0].PlanStatus != PITRNotFeasible || got[0].Reason == "" || len(got[0].Options) != 0 {
		t.Errorf("decisions = %+v", got)
	}
}

// The configuration a decision read is identified by the digest of the file.
func TestPlanDecision_CarriesTheDigestOfTheLoadedConfig(t *testing.T) {
	body, err := os.ReadFile("../../testing/testing.infra.json")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "infrastructure.json")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := infra.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	infraConfig = loaded
	t.Cleanup(func() { infraConfig = nil })
	if got := objectivesSnapshot("pgbackrest-db"); got == nil || got.ConfigDigest != loaded.Digest || loaded.Digest == "" {
		t.Errorf("snapshot = %+v, loaded digest %q", got, loaded.Digest)
	}
	if objectivesSnapshot("nope") != nil {
		t.Error("an unknown target has a snapshot")
	}
}

// ── a decision that cannot be written ────────────────────────────────────────

// failingAudit is an audit backend that cannot be written to (auditd down).
type failingAudit struct{ audit.Auditor }

func (failingAudit) Record(context.Context, *audit.Event) error {
	return errors.New("auditd unreachable")
}

// withFailingAudit makes every audit write fail for the test.
func withFailingAudit(t *testing.T) {
	t.Helper()
	old := toolAuditor
	toolAuditor = audit.NewToolAuditor(failingAudit{}, "sysadmin_agent", "sess_down", "trace_down")
	t.Cleanup(func() { toolAuditor = old })
}

// The restore is not held for its audit write, as no tool call is. But the
// result must not look as though the decision were on record when it is not.
func TestUnrecordedDecision_PlanAndRestoreSaySo(t *testing.T) {
	enforced(t, "15m", "warn")
	sim, _ := liveEnv(t)
	withFailingAudit(t)

	plan, err := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: liveTarget})
	if err != nil || plan.Status != PITRFeasible {
		t.Fatalf("a plan must not be held for its audit write: %+v, %v", plan, err)
	}
	if !strings.Contains(plan.DecisionUnrecorded, "not in the audit trail") || !strings.Contains(plan.DecisionUnrecorded, "auditd unreachable") {
		t.Errorf("plan.DecisionUnrecorded = %q", plan.DecisionUnrecorded)
	}

	// Proceeding: the instance is stopped and restored all the same.
	res, err := restoreToPITImpl(context.Background(), liveArgs(plan))
	if err != nil || !sim.did("stop") || !sim.did("restore") {
		t.Fatalf("the restore was held up by an audit failure: err=%v order=%s", err, sim.order())
	}
	if !strings.Contains(res.DecisionUnrecorded, "not in the audit trail") {
		t.Errorf("res.DecisionUnrecorded = %q", res.DecisionUnrecorded)
	}
	// And the model, which reads the result's JSON, can see it.
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), `"decision_unrecorded"`) {
		t.Errorf("the result JSON does not carry it: %s", raw)
	}
}

func TestUnrecordedDecision_RefusalsSaySoToo(t *testing.T) {
	// A refusal at the objective check.
	enforced(t, "15m", "acknowledge")
	_, _ = liveEnv(t)
	withFailingAudit(t)
	plan := approvedLiveDigest(t, liveTarget)
	res, err := restoreToPITImpl(context.Background(), liveArgs(plan))
	if err == nil || !strings.Contains(err.Error(), "acknowledge_objective_breach") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(res.DecisionUnrecorded, "not in the audit trail") {
		t.Errorf("objective refusal: DecisionUnrecorded = %q", res.DecisionUnrecorded)
	}

	// A refusal for another reason, recorded by the catch-all.
	args := liveArgs(plan)
	args.PlanDigest = "ffffffffffffffff"
	res, err = restoreToPITImpl(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "changed since it was approved") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(res.DecisionUnrecorded, "not in the audit trail") {
		t.Errorf("catch-all refusal: DecisionUnrecorded = %q", res.DecisionUnrecorded)
	}
}

// Auditing that is off, or that works, is not "unrecorded".
func TestUnrecordedDecision_NotSetWhenAuditingIsOffOrWorks(t *testing.T) {
	enforced(t, "15m", "warn")
	_, _ = liveEnv(t)
	plan := approvedLiveDigest(t, liveTarget)

	old := toolAuditor
	toolAuditor = nil // auditing not configured
	res, err := restoreToPITImpl(context.Background(), liveArgs(plan))
	toolAuditor = old
	if err != nil || res.DecisionUnrecorded != "" {
		t.Errorf("auditing off: err=%v unrecorded=%q", err, res.DecisionUnrecorded)
	}

	_, _ = liveEnv(t)
	_, cleanup := withRealToolAuditor(t)
	defer cleanup()
	plan = approvedLiveDigest(t, liveTarget)
	res, err = restoreToPITImpl(context.Background(), liveArgs(plan))
	if err != nil || res.DecisionUnrecorded != "" {
		t.Errorf("auditing works: err=%v unrecorded=%q", err, res.DecisionUnrecorded)
	}
	if p, _ := planPITRWithEstimate(context.Background(), PlanPITRArgs{Target: "pgbackrest_db", Stanza: "main",
		RecoveryTargetType: TargetTime, RecoveryTargetVal: "2026-10-09T00:00:00Z"}); p.DecisionUnrecorded != "" {
		t.Errorf("plan.DecisionUnrecorded = %q", p.DecisionUnrecorded)
	}
}

// A caller can pass a connection string as the target (the tools accept one), and
// the model is not stopped from doing so: a password in it must not be recorded.
func TestDecisionRecords_NeverCarryAPasswordInTheTarget(t *testing.T) {
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()
	dsn := "host=localhost port=15435 dbname=testdb user=postgres password=testpass"

	if err := recordPlanDecision(context.Background(), dsn, PITRPlan{Status: PITRNotFeasible, Detail: "could not connect: " + dsn}); err != nil {
		t.Fatalf("recording the plan decision: %v", err)
	}
	if err := recordExecutionDecision(context.Background(), RestoreToPITArgs{Target: dsn, PlanDigest: "0123456789abcdef"}, PITRPlan{}, nil, nil, "refusing to restore — "+dsn); err != nil {
		t.Fatalf("recording the execution decision: %v", err)
	}

	events, err := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypeRecoveryDecision})
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %d, %v", len(events), err)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "testpass") {
		t.Errorf("the password is in the audit trail: %s", raw)
	}
}

// ── the restore ──────────────────────────────────────────────────────────────

func TestExecutionDecision_RecordsRefusalAndProceedingAtTheObjectiveCheck(t *testing.T) {
	for _, ack := range []bool{false, true} {
		enforced(t, "15m", "acknowledge")
		infraConfig.Digest = "0123456789abcdef"
		sim, _ := liveEnv(t)
		store, cleanup := withRealToolAuditor(t)
		plan := approvedLiveDigest(t, liveTarget)
		args := liveArgs(plan)
		args.AcknowledgeObjectiveBreach = ack
		_, err := restoreToPITImpl(context.Background(), args)
		got := decisions(t, store)[audit.RecoveryStageExecution]
		cleanup()

		// Exactly one, however it went: the check records itself and the
		// catch-all for other refusals must not add a second.
		if len(got) != 1 {
			t.Fatalf("ack=%v: %d execution decisions: %+v", ack, len(got), got)
		}
		d := got[0]
		if d.Tool != "restore_to_point_in_time" || d.Target != "pgbackrest_db" || d.PlanDigest != plan.PlanDigest ||
			d.RecoveryTarget != "time="+liveTarget || d.PlanStatus != PITRFeasible {
			t.Errorf("ack=%v: decision = %+v", ack, d)
		}
		if d.Objectives == nil || *d.Objectives.RPOSeconds != 900 || d.Objectives.ConfigDigest != "0123456789abcdef" {
			t.Errorf("ack=%v: objectives = %+v", ack, d.Objectives)
		}
		if d.Approval == nil || !d.Approval.AllowedStopServing || d.Approval.AcknowledgedBreach != ack {
			t.Errorf("ack=%v: approval = %+v", ack, d.Approval)
		}
		if len(d.Options) != 1 || d.Options[0].Verdict != RPOExceeds || d.Options[0].Gate != GateAcknowledge || d.Options[0].LossSeconds == nil {
			t.Fatalf("ack=%v: options = %+v", ack, d.Options)
		}
		if ack {
			if err != nil || d.Decision != audit.RecoveryDecisionProceeded || d.Reason != "" || !d.Options[0].Chosen || !sim.did("stop") {
				t.Errorf("acknowledged: err=%v decision=%+v order=%s", err, d, sim.order())
			}
			continue
		}
		if err == nil || d.Decision != audit.RecoveryDecisionRefused || d.Options[0].Chosen || sim.did("stop") {
			t.Errorf("not acknowledged: err=%v decision=%+v order=%s", err, d, sim.order())
		}
		if !strings.Contains(d.Reason, "acknowledge_objective_breach") || !strings.Contains(d.Reason, "The instance was not stopped") {
			t.Errorf("the recorded reason = %q", d.Reason)
		}
	}
}

// A refusal for another reason (here the approved plan no longer matches) is a
// decision about the recovery too, and is recorded once, with its reason.
func TestExecutionDecision_RecordsOtherRefusalsOnce(t *testing.T) {
	enforced(t, "15m", "")
	_, _ = liveEnv(t)
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()
	plan := approvedLiveDigest(t, liveTarget)
	args := liveArgs(plan)
	args.PlanDigest = "ffffffffffffffff" // not the plan that was approved

	_, err := restoreToPITImpl(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "changed since it was approved") {
		t.Fatalf("err = %v", err)
	}
	got := decisions(t, store)[audit.RecoveryStageExecution]
	if len(got) != 1 || got[0].Decision != audit.RecoveryDecisionRefused || !strings.Contains(got[0].Reason, "changed since it was approved") {
		t.Fatalf("decisions = %+v", got)
	}
	if got[0].PlanDigest != "ffffffffffffffff" {
		t.Errorf("it should record the digest it was given, not the plan's: %s", got[0].PlanDigest)
	}
}

// A failure once the restore has begun is not a refusal and is not recorded as one.
func TestExecutionDecision_AFailureAfterTheStopIsNotARefusal(t *testing.T) {
	enforced(t, "15m", "warn")
	sim, _ := liveEnv(t)
	sim.restoreErr = context.DeadlineExceeded // the pgBackRest restore itself fails
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()
	plan := approvedLiveDigest(t, liveTarget)

	_, err := restoreToPITImpl(context.Background(), liveArgs(plan))
	if err == nil {
		t.Fatal("the restore should have failed")
	}
	got := decisions(t, store)[audit.RecoveryStageExecution]
	if len(got) != 1 || got[0].Decision != audit.RecoveryDecisionProceeded {
		t.Errorf("decisions = %+v: it decided to proceed, and then it failed", got)
	}
}

// Through the model-facing tool, the approver and what they acknowledged are
// in the record: the gateway put them in the approval the tool was given.
func TestExecutionDecision_NamesTheApproverAndWhatTheyAcknowledged(t *testing.T) {
	enforced(t, "15m", "acknowledge")
	_, _ = liveEnv(t)
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()
	plan := approvedLiveDigest(t, liveTarget)
	ap := approvalFor(plan, true)
	ap.BreachAcknowledged = true

	if _, err := restoreToPITTool(toolCtxWithApproval(ap), RestoreToPITModelArgs{Target: "pgbackrest_db", Stanza: "main"}); err != nil {
		t.Fatal(err)
	}
	got := decisions(t, store)[audit.RecoveryStageExecution]
	if len(got) != 1 || got[0].Approval == nil {
		t.Fatalf("decisions = %+v", got)
	}
	a := got[0].Approval
	if a.ApprovedBy != "alice" || !a.AcknowledgedDataLoss || !a.AllowedStopServing || !a.AcknowledgedBreach {
		t.Errorf("approval = %+v", a)
	}
}

// An objective that could not be read is on the record as such, with why, and
// the enforcement that was applied.
func TestExecutionDecision_RecordsAnUnreadableObjective(t *testing.T) {
	objectiveInfra(t, "", func(db *infra.DBServer, _ *infra.Config) {
		db.Objectives = &infra.Objectives{
			RPO:         &infra.Duration{Invalid: `invalid duration "soon"`},
			Enforcement: &infra.Choice{Value: "acknowledge"},
		}
	})
	_, _ = liveEnv(t)
	store, cleanup := withRealToolAuditor(t)
	defer cleanup()
	plan := approvedLiveDigest(t, liveTarget)

	if _, err := restoreToPITImpl(context.Background(), liveArgs(plan)); err == nil {
		t.Fatal("it should have asked for the acknowledgement")
	}
	got := decisions(t, store)[audit.RecoveryStageExecution]
	if len(got) != 1 {
		t.Fatalf("decisions = %+v", got)
	}
	d := got[0]
	if d.Objectives == nil || d.Objectives.RPOSeconds != nil || len(d.Objectives.Problems) == 0 || d.Objectives.Enforcement != "acknowledge" {
		t.Errorf("objectives = %+v", d.Objectives)
	}
	if d.Options[0].Verdict != RPOObjectiveInvalid || d.Options[0].Gate != GateAcknowledge {
		t.Errorf("option = %+v", d.Options[0])
	}
}
