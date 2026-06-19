package audit

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)


func newRunEvaluationStore(t *testing.T) *RunEvaluationStore {
	t.Helper()
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	ev, err := NewRunEvaluationStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewRunEvaluationStore: %v", err)
	}
	return ev
}

func TestRunEvaluationStore_UpsertAndGet(t *testing.T) {
	ctx := context.Background()
	store := newRunEvaluationStore(t)

	eval := &RunEvaluation{
		RunID:          "plr_eval01",
		FailureID:      "db-tx-lock-chain-blocker",
		FailureName:    "Transaction lock chain blocker",
		KeywordScore:   1.0,
		ToolScore:      0.8,
		DiagnosisScore: 0.9,
		OverallScore:   0.85,
		JudgeUsed:      true,
		Passed:         true,
	}
	if err := store.Upsert(ctx, eval); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.GetByRunID(ctx, eval.RunID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.RunID != eval.RunID {
		t.Errorf("RunID: got %q, want %q", got.RunID, eval.RunID)
	}
	if got.FailureID != eval.FailureID {
		t.Errorf("FailureID: got %q, want %q", got.FailureID, eval.FailureID)
	}
	if got.KeywordScore != eval.KeywordScore {
		t.Errorf("KeywordScore: got %v, want %v", got.KeywordScore, eval.KeywordScore)
	}
	if got.ToolScore != eval.ToolScore {
		t.Errorf("ToolScore: got %v, want %v", got.ToolScore, eval.ToolScore)
	}
	if got.DiagnosisScore != eval.DiagnosisScore {
		t.Errorf("DiagnosisScore: got %v, want %v", got.DiagnosisScore, eval.DiagnosisScore)
	}
	if got.OverallScore != eval.OverallScore {
		t.Errorf("OverallScore: got %v, want %v", got.OverallScore, eval.OverallScore)
	}
	if got.JudgeUsed != eval.JudgeUsed {
		t.Errorf("JudgeUsed: got %v, want %v", got.JudgeUsed, eval.JudgeUsed)
	}
	if got.Passed != eval.Passed {
		t.Errorf("Passed: got %v, want %v", got.Passed, eval.Passed)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
}

func TestRunEvaluationStore_Upsert_Overwrites(t *testing.T) {
	ctx := context.Background()
	store := newRunEvaluationStore(t)

	first := &RunEvaluation{
		RunID:        "plr_over01",
		FailureID:    "db-conn-limit",
		OverallScore: 0.5,
		Passed:       false,
	}
	if err := store.Upsert(ctx, first); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	second := &RunEvaluation{
		RunID:        "plr_over01",
		FailureID:    "db-conn-limit",
		OverallScore: 0.9,
		Passed:       true,
	}
	if err := store.Upsert(ctx, second); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := store.GetByRunID(ctx, "plr_over01")
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.OverallScore != 0.9 {
		t.Errorf("OverallScore after overwrite: got %v, want 0.9", got.OverallScore)
	}
	if !got.Passed {
		t.Error("Passed should be true after overwrite")
	}
}

func TestRunEvaluationStore_GetByRunID_NotFound(t *testing.T) {
	ctx := context.Background()
	store := newRunEvaluationStore(t)

	_, err := store.GetByRunID(ctx, "plr_nonexistent")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestRunEvaluationStore_CalibrationBands(t *testing.T) {
	ctx := context.Background()

	// Need both run_evaluation and run_feedback tables in the same DB.
	mainStore, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { mainStore.Close() })

	evalStore, err := NewRunEvaluationStore(mainStore.DB(), false)
	if err != nil {
		t.Fatalf("NewRunEvaluationStore: %v", err)
	}
	fbStore, err := NewRunFeedbackStore(mainStore.DB(), false)
	if err != nil {
		t.Fatalf("NewRunFeedbackStore: %v", err)
	}

	const seriesID = "pbs_calib_test"

	// Seed: 3 runs in 90-100% band (2 correct, 1 wrong) + 3 runs in 70-89% band (3 correct).
	type seed struct {
		runID string
		score float64
		ok    bool
	}
	seeds := []seed{
		{"plr_c01", 0.95, true},
		{"plr_c02", 0.92, true},
		{"plr_c03", 0.91, false},
		{"plr_c04", 0.80, true},
		{"plr_c05", 0.75, true},
		{"plr_c06", 0.72, true},
	}
	tr := true
	fa := false
	for _, s := range seeds {
		if err := evalStore.Upsert(ctx, &RunEvaluation{
			RunID: s.runID, FailureID: "db-lock", DiagnosisScore: s.score, OverallScore: s.score,
			PrimaryConfidence: s.score,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", s.runID, err)
		}
		v := &tr
		if !s.ok {
			v = &fa
		}
		if err := fbStore.Submit(ctx, &RunFeedback{
			RunID: s.runID, SeriesID: seriesID, FeedbackType: "triage", FeedbackTime: "post_incident",
			VerdictCorrect: v,
		}); err != nil {
			t.Fatalf("Submit %s: %v", s.runID, err)
		}
	}

	// Fleet-wide report.
	report, err := evalStore.CalibrationBands(ctx, "")
	if err != nil {
		t.Fatalf("CalibrationBands: %v", err)
	}
	if report.TotalRuns != 6 {
		t.Errorf("TotalRuns = %d, want 6", report.TotalRuns)
	}
	if len(report.Bands) != 3 {
		t.Fatalf("want 3 bands, got %d", len(report.Bands))
	}

	// Band 0: 90-100%, 3 runs, 2 correct → 66% actual vs 95% expected → OVERCONFIDENT
	b90 := report.Bands[0]
	if b90.Band != "90-100%" {
		t.Errorf("Bands[0].Band = %q, want 90-100%%", b90.Band)
	}
	if b90.Runs != 3 || b90.Correct != 2 {
		t.Errorf("90-100%%: Runs=%d Correct=%d, want Runs=3 Correct=2", b90.Runs, b90.Correct)
	}
	if b90.Calibration != "OVERCONFIDENT" {
		t.Errorf("90-100%% Calibration = %q, want OVERCONFIDENT", b90.Calibration)
	}

	// Band 1: 70-89%, 3 runs, 3 correct → 100% actual vs 80% expected → UNDERCONFIDENT
	b70 := report.Bands[1]
	if b70.Band != "70-89%" {
		t.Errorf("Bands[1].Band = %q, want 70-89%%", b70.Band)
	}
	if b70.Runs != 3 || b70.Correct != 3 {
		t.Errorf("70-89%%: Runs=%d Correct=%d, want Runs=3 Correct=3", b70.Runs, b70.Correct)
	}
	if b70.Calibration != "UNDERCONFIDENT" {
		t.Errorf("70-89%% Calibration = %q, want UNDERCONFIDENT", b70.Calibration)
	}

	// Band 2: <70%, 0 runs → INSUFFICIENT_DATA
	bLow := report.Bands[2]
	if bLow.Runs != 0 {
		t.Errorf("<70%% Runs = %d, want 0", bLow.Runs)
	}
	if bLow.Calibration != "INSUFFICIENT_DATA" {
		t.Errorf("<70%% Calibration = %q, want INSUFFICIENT_DATA", bLow.Calibration)
	}

	// Series-scoped report: same data, filtered by seriesID.
	scoped, err := evalStore.CalibrationBands(ctx, seriesID)
	if err != nil {
		t.Fatalf("CalibrationBands (scoped): %v", err)
	}
	if scoped.TotalRuns != 6 {
		t.Errorf("scoped TotalRuns = %d, want 6", scoped.TotalRuns)
	}

	// Runs without feedback are excluded.
	noFeedbackRun := &RunEvaluation{
		RunID: "plr_c99", FailureID: "db-lock", DiagnosisScore: 0.93, OverallScore: 0.93,
		PrimaryConfidence: 0.93,
	}
	if err := evalStore.Upsert(ctx, noFeedbackRun); err != nil {
		t.Fatalf("Upsert no-feedback run: %v", err)
	}
	reportAfter, err := evalStore.CalibrationBands(ctx, "")
	if err != nil {
		t.Fatalf("CalibrationBands after extra run: %v", err)
	}
	if reportAfter.TotalRuns != 6 {
		t.Errorf("TotalRuns after adding eval-only run = %d, want 6 (no feedback → excluded)", reportAfter.TotalRuns)
	}

	// at_gate feedback counts — calibration now includes both at_gate and post_incident,
	// preferring at_gate. plr_c_gate has score 0.93 (90-100% band) and correct=true.
	atGateRun := &RunEvaluation{
		RunID: "plr_c_gate", FailureID: "db-lock", DiagnosisScore: 0.93, OverallScore: 0.93,
		PrimaryConfidence: 0.93,
	}
	if err := evalStore.Upsert(ctx, atGateRun); err != nil {
		t.Fatalf("Upsert at_gate run: %v", err)
	}
	tr2 := true
	if err := fbStore.Submit(ctx, &RunFeedback{
		RunID: "plr_c_gate", SeriesID: seriesID, FeedbackType: "triage", FeedbackTime: "at_gate",
		VerdictCorrect: &tr2,
	}); err != nil {
		t.Fatalf("Submit at_gate feedback: %v", err)
	}
	reportGate, err := evalStore.CalibrationBands(ctx, "")
	if err != nil {
		t.Fatalf("CalibrationBands after at_gate: %v", err)
	}
	// at_gate runs now count, so TotalRuns grows to 7.
	if reportGate.TotalRuns != 7 {
		t.Errorf("TotalRuns after at_gate feedback = %d, want 7 (at_gate counted)", reportGate.TotalRuns)
	}
	// 90-100% band: previously 3 runs / 2 correct; now 4 runs / 3 correct (plr_c_gate added).
	b90Gate := reportGate.Bands[0]
	if b90Gate.Runs != 4 || b90Gate.Correct != 3 {
		t.Errorf("90-100%% after at_gate: Runs=%d Correct=%d, want Runs=4 Correct=3", b90Gate.Runs, b90Gate.Correct)
	}

	// Verify at_gate is preferred when a run has BOTH at_gate and post_incident feedback.
	// plr_c_gate already has at_gate=true; add post_incident=false — at_gate must win.
	fa2 := false
	if err := fbStore.Submit(ctx, &RunFeedback{
		RunID: "plr_c_gate", SeriesID: seriesID, FeedbackType: "triage", FeedbackTime: "post_incident",
		VerdictCorrect: &fa2,
	}); err != nil {
		t.Fatalf("Submit post_incident feedback for plr_c_gate: %v", err)
	}
	reportBoth, err := evalStore.CalibrationBands(ctx, "")
	if err != nil {
		t.Fatalf("CalibrationBands with both feedbacks: %v", err)
	}
	// at_gate wins → correct count unchanged (still 3, not 2).
	b90Both := reportBoth.Bands[0]
	if b90Both.Runs != 4 || b90Both.Correct != 3 {
		t.Errorf("90-100%% (both feedbacks): Runs=%d Correct=%d, want Runs=4 Correct=3 (at_gate preferred)", b90Both.Runs, b90Both.Correct)
	}

	// Pending feedback (verdict_correct = NULL) must NOT count.
	pendingRun := &RunEvaluation{
		RunID: "plr_c_pend", FailureID: "db-lock", DiagnosisScore: 0.94, OverallScore: 0.94,
		PrimaryConfidence: 0.94,
	}
	if err := evalStore.Upsert(ctx, pendingRun); err != nil {
		t.Fatalf("Upsert pending run: %v", err)
	}
	if err := fbStore.Submit(ctx, &RunFeedback{
		RunID: "plr_c_pend", SeriesID: seriesID, FeedbackType: "triage", FeedbackTime: "post_incident",
		VerdictCorrect: nil, // pending
	}); err != nil {
		t.Fatalf("Submit pending feedback: %v", err)
	}
	reportPend, err := evalStore.CalibrationBands(ctx, "")
	if err != nil {
		t.Fatalf("CalibrationBands after pending: %v", err)
	}
	if reportPend.TotalRuns != 7 {
		t.Errorf("TotalRuns after pending feedback = %d, want 7 (NULL verdict excluded)", reportPend.TotalRuns)
	}
}

func TestRunEvaluationStore_RemediationCalibrationBands(t *testing.T) {
	ctx := context.Background()

	mainStore, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { mainStore.Close() })

	evalStore, err := NewRunEvaluationStore(mainStore.DB(), false)
	if err != nil {
		t.Fatalf("NewRunEvaluationStore: %v", err)
	}
	fbStore, err := NewRunFeedbackStore(mainStore.DB(), false)
	if err != nil {
		t.Fatalf("NewRunFeedbackStore: %v", err)
	}

	const seriesID = "pbs_remed_calib"

	// Seed: 2 runs with high remediation score (90-100%) — 1 operator says correct, 1 wrong.
	//       1 run with mid score (70-89%) — operator says correct.
	//       1 run with no remediation feedback (should be excluded from RemediationBands).
	//       1 run with zero remediation_score (should be excluded regardless).
	tr := true
	fa := false
	seeds := []struct {
		runID    string
		diagSc   float64
		remedSc  float64
		feedback *bool // nil = no remediation feedback
	}{
		{"plr_r01", 0.9, 0.95, &tr},
		{"plr_r02", 0.9, 0.92, &fa},
		{"plr_r03", 0.8, 0.80, &tr},
		{"plr_r04", 0.9, 0.91, nil}, // no remediation feedback → excluded
		{"plr_r05", 0.9, 0.00, &tr}, // zero remediation score → excluded by WHERE clause
	}
	for _, s := range seeds {
		if err := evalStore.Upsert(ctx, &RunEvaluation{
			RunID: s.runID, FailureID: "db-lock",
			DiagnosisScore:        s.diagSc,
			PrimaryConfidence:     s.diagSc,
			RemediationJudgeScore: s.remedSc,
			OverallScore:          s.diagSc,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", s.runID, err)
		}
		// All runs get triage feedback (so they'd show in diagnosis bands).
		if err := fbStore.Submit(ctx, &RunFeedback{
			RunID: s.runID, SeriesID: seriesID, FeedbackType: "triage", FeedbackTime: "post_incident",
			VerdictCorrect: &tr,
		}); err != nil {
			t.Fatalf("Submit triage feedback %s: %v", s.runID, err)
		}
		if s.feedback != nil {
			if err := fbStore.Submit(ctx, &RunFeedback{
				RunID: s.runID, SeriesID: seriesID, FeedbackType: "remediation", FeedbackTime: "post_incident",
				VerdictCorrect: s.feedback,
			}); err != nil {
				t.Fatalf("Submit remediation feedback %s: %v", s.runID, err)
			}
		}
	}

	report, err := evalStore.CalibrationBands(ctx, "")
	if err != nil {
		t.Fatalf("CalibrationBands: %v", err)
	}

	// Diagnosis bands: 5 triage feedbacks across 5 runs.
	if report.TotalRuns != 5 {
		t.Errorf("TotalRuns = %d, want 5", report.TotalRuns)
	}

	// Remediation bands: only 3 runs (plr_r01-r03) qualify:
	//   plr_r04 has no remediation feedback → excluded
	//   plr_r05 has remediation_judge_score=0 → excluded by WHERE ev.remediation_judge_score > 0
	if report.RemediationRuns != 3 {
		t.Errorf("RemediationRuns = %d, want 3", report.RemediationRuns)
	}
	if len(report.RemediationBands) != 3 {
		t.Fatalf("RemediationBands len = %d, want 3", len(report.RemediationBands))
	}

	// 90-100% band: plr_r01 (correct) + plr_r02 (wrong) = 2 runs, 1 correct.
	b90 := report.RemediationBands[0]
	if b90.Band != "90-100%" {
		t.Errorf("RemediationBands[0].Band = %q, want 90-100%%", b90.Band)
	}
	if b90.Runs != 2 || b90.Correct != 1 {
		t.Errorf("90-100%% remed: Runs=%d Correct=%d, want Runs=2 Correct=1", b90.Runs, b90.Correct)
	}

	// 70-89% band: plr_r03 (correct) = 1 run, 1 correct.
	b70 := report.RemediationBands[1]
	if b70.Band != "70-89%" {
		t.Errorf("RemediationBands[1].Band = %q, want 70-89%%", b70.Band)
	}
	if b70.Runs != 1 || b70.Correct != 1 {
		t.Errorf("70-89%% remed: Runs=%d Correct=%d, want Runs=1 Correct=1", b70.Runs, b70.Correct)
	}

	// Series-scoped: same 3 remediation runs.
	scoped, err := evalStore.CalibrationBands(ctx, seriesID)
	if err != nil {
		t.Fatalf("CalibrationBands (scoped): %v", err)
	}
	if scoped.RemediationRuns != 3 {
		t.Errorf("scoped RemediationRuns = %d, want 3", scoped.RemediationRuns)
	}

	// Post-incident feedback is preferred over at-gate for remediation (actual outcome > plan review).
	// plr_r02 has post_incident=wrong; add at_gate=correct → post_incident still wins → Correct stays 1.
	if err := fbStore.Submit(ctx, &RunFeedback{
		RunID: "plr_r02", SeriesID: seriesID, FeedbackType: "remediation", FeedbackTime: "at_gate",
		VerdictCorrect: &tr,
	}); err != nil {
		t.Fatalf("Submit at_gate remediation feedback: %v", err)
	}
	reportGate, err := evalStore.CalibrationBands(ctx, "")
	if err != nil {
		t.Fatalf("CalibrationBands after at_gate: %v", err)
	}
	b90Gate := reportGate.RemediationBands[0]
	if b90Gate.Runs != 2 || b90Gate.Correct != 1 {
		t.Errorf("90-100%% after at_gate remed: Runs=%d Correct=%d, want Runs=2 Correct=1 (post_incident preferred)", b90Gate.Runs, b90Gate.Correct)
	}
}

func TestRunEvaluationStore_RemediationScore(t *testing.T) {
	ctx := context.Background()
	store := newRunEvaluationStore(t)

	eval := &RunEvaluation{
		RunID:            "plr_remed01",
		FailureID:        "db-oom-killer",
		DiagnosisScore:   0.7,
		RemediationScore: 0.8,
		OverallScore:     0.74, // 0.7*0.6 + 0.8*0.4
		Passed:           true,
	}
	if err := store.Upsert(ctx, eval); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.GetByRunID(ctx, eval.RunID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.RemediationScore != 0.8 {
		t.Errorf("RemediationScore: got %v, want 0.8", got.RemediationScore)
	}
}

func TestRunEvaluationStore_ListHistory(t *testing.T) {
	ctx := context.Background()
	store := newRunEvaluationStore(t)

	now := time.Now().UTC()
	seed := func(runID, failureID string, passed bool, ago time.Duration) {
		t.Helper()
		eval := &RunEvaluation{
			RunID:     runID,
			FailureID: failureID,
			Passed:    passed,
			CreatedAt: now.Add(-ago),
		}
		if err := store.Upsert(ctx, eval); err != nil {
			t.Fatalf("Upsert %s: %v", runID, err)
		}
	}

	seed("plr_h1", "db-lock-contention", true, 10*24*time.Hour)
	seed("plr_h2", "db-lock-contention", false, 5*24*time.Hour)
	seed("plr_h3", "k8s-pod-crashloop", true, 3*24*time.Hour)
	seed("plr_h4", "db-lock-contention", true, 1*24*time.Hour)
	// This one is outside the 30-day window.
	seed("plr_h5", "db-lock-contention", true, 60*24*time.Hour)
	// This one has empty failure_id (legacy run) — must be excluded.
	if err := store.Upsert(ctx, &RunEvaluation{RunID: "plr_legacy", FailureID: ""}); err != nil {
		t.Fatalf("Upsert legacy: %v", err)
	}

	// All faults, 30-day window.
	entries, err := store.ListHistory(ctx, 30, "")
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(entries) != 4 {
		t.Errorf("ListHistory all: got %d entries, want 4", len(entries))
	}

	// Filter by fault_id.
	entries2, err := store.ListHistory(ctx, 30, "db-lock-contention")
	if err != nil {
		t.Fatalf("ListHistory fault_id: %v", err)
	}
	if len(entries2) != 3 {
		t.Errorf("ListHistory db-lock-contention: got %d entries, want 3", len(entries2))
	}
	for _, e := range entries2 {
		if e.FailureID != "db-lock-contention" {
			t.Errorf("unexpected failure_id %q in filtered result", e.FailureID)
		}
	}

	// Narrower window excludes older entries.
	entries3, err := store.ListHistory(ctx, 2, "")
	if err != nil {
		t.Fatalf("ListHistory 2 days: %v", err)
	}
	if len(entries3) != 1 {
		t.Errorf("ListHistory 2 days: got %d entries, want 1", len(entries3))
	}
}

func TestRunEvaluationStore_PrimaryConfidence_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newRunEvaluationStore(t)

	eval := &RunEvaluation{
		RunID:             "plr_conf01",
		FailureID:         "db-lock",
		DiagnosisScore:    0.87,
		PrimaryConfidence: 0.87,
		OverallScore:      0.87,
		Passed:            true,
	}
	if err := store.Upsert(ctx, eval); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.GetByRunID(ctx, eval.RunID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.PrimaryConfidence != 0.87 {
		t.Errorf("PrimaryConfidence: got %v, want 0.87", got.PrimaryConfidence)
	}

	// Zero is stored and returned correctly.
	evalZero := &RunEvaluation{
		RunID: "plr_conf02", FailureID: "db-lock", PrimaryConfidence: 0.0,
	}
	if err := store.Upsert(ctx, evalZero); err != nil {
		t.Fatalf("Upsert zero: %v", err)
	}
	gotZero, err := store.GetByRunID(ctx, evalZero.RunID)
	if err != nil {
		t.Fatalf("GetByRunID zero: %v", err)
	}
	if gotZero.PrimaryConfidence != 0.0 {
		t.Errorf("PrimaryConfidence (zero): got %v, want 0.0", gotZero.PrimaryConfidence)
	}
}

func TestRunEvaluationStore_CalibrationBands_HeuristicCount(t *testing.T) {
	ctx := context.Background()

	mainStore, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { mainStore.Close() })

	evalStore, err := NewRunEvaluationStore(mainStore.DB(), false)
	if err != nil {
		t.Fatalf("NewRunEvaluationStore: %v", err)
	}
	fbStore, err := NewRunFeedbackStore(mainStore.DB(), false)
	if err != nil {
		t.Fatalf("NewRunFeedbackStore: %v", err)
	}

	tr := true
	// Runs with primary_confidence > 0 — counted in TotalRuns.
	for _, runID := range []string{"plr_hc01", "plr_hc02"} {
		if err := evalStore.Upsert(ctx, &RunEvaluation{
			RunID: runID, FailureID: "db-lock", PrimaryConfidence: 0.92,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", runID, err)
		}
		if err := fbStore.Submit(ctx, &RunFeedback{
			RunID: runID, FeedbackType: "triage", FeedbackTime: "post_incident",
			VerdictCorrect: &tr,
		}); err != nil {
			t.Fatalf("Submit %s: %v", runID, err)
		}
	}
	// Runs with primary_confidence == 0 — excluded from TotalRuns, counted in HeuristicCount.
	for _, runID := range []string{"plr_hc03", "plr_hc04"} {
		if err := evalStore.Upsert(ctx, &RunEvaluation{
			RunID: runID, FailureID: "db-lock", PrimaryConfidence: 0.0, DiagnosisScore: 0.85,
		}); err != nil {
			t.Fatalf("Upsert heuristic %s: %v", runID, err)
		}
		if err := fbStore.Submit(ctx, &RunFeedback{
			RunID: runID, FeedbackType: "triage", FeedbackTime: "post_incident",
			VerdictCorrect: &tr,
		}); err != nil {
			t.Fatalf("Submit heuristic %s: %v", runID, err)
		}
	}

	report, err := evalStore.CalibrationBands(ctx, "")
	if err != nil {
		t.Fatalf("CalibrationBands: %v", err)
	}
	if report.TotalRuns != 2 {
		t.Errorf("TotalRuns = %d, want 2 (only primary_confidence > 0 runs)", report.TotalRuns)
	}
	if report.HeuristicCount != 2 {
		t.Errorf("HeuristicCount = %d, want 2 (runs with feedback but no confidence signal)", report.HeuristicCount)
	}
}
