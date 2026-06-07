package audit

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func newRunFeedbackStore(t *testing.T) (*RunFeedbackStore, *sql.DB) {
	t.Helper()
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	fb, err := NewRunFeedbackStore(store.DB())
	if err != nil {
		t.Fatalf("NewRunFeedbackStore: %v", err)
	}
	return fb, store.DB()
}

func boolPtr(b bool) *bool { return &b }

func TestRunFeedbackStore_SubmitAndGet(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	fb := &RunFeedback{
		RunID:            "plr_test01",
		SeriesID:         "pbs_lock_chain_triage",
		DiagnosisCorrect: boolPtr(true),
		ActualRootCause:  "PID 867 held ShareLock",
		Operator:         "alice",
		SubmittedAt:      time.Now().UTC().Truncate(time.Second),
	}
	if err := store.Submit(ctx, fb); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	got, err := store.GetByRunID(ctx, fb.RunID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.RunID != fb.RunID {
		t.Errorf("RunID: got %q, want %q", got.RunID, fb.RunID)
	}
	if got.SeriesID != fb.SeriesID {
		t.Errorf("SeriesID: got %q, want %q", got.SeriesID, fb.SeriesID)
	}
	if got.DiagnosisCorrect == nil || *got.DiagnosisCorrect != true {
		t.Errorf("DiagnosisCorrect: got %v, want true", got.DiagnosisCorrect)
	}
	if got.ActualRootCause != fb.ActualRootCause {
		t.Errorf("ActualRootCause: got %q, want %q", got.ActualRootCause, fb.ActualRootCause)
	}
	if got.Operator != fb.Operator {
		t.Errorf("Operator: got %q, want %q", got.Operator, fb.Operator)
	}
}

func TestRunFeedbackStore_Upsert(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	fb := &RunFeedback{
		RunID:            "plr_upsert",
		SeriesID:         "pbs_triage",
		DiagnosisCorrect: boolPtr(true),
		Operator:         "bob",
	}
	if err := store.Submit(ctx, fb); err != nil {
		t.Fatalf("first Submit: %v", err)
	}

	// Overwrite with different values.
	fb.DiagnosisCorrect = boolPtr(false)
	fb.ActualRootCause = "actually a different blocker"
	if err := store.Submit(ctx, fb); err != nil {
		t.Fatalf("second Submit: %v", err)
	}

	got, err := store.GetByRunID(ctx, fb.RunID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.DiagnosisCorrect == nil || *got.DiagnosisCorrect != false {
		t.Errorf("after upsert DiagnosisCorrect: got %v, want false", got.DiagnosisCorrect)
	}
	if got.ActualRootCause != "actually a different blocker" {
		t.Errorf("after upsert ActualRootCause: got %q", got.ActualRootCause)
	}
}

func TestRunFeedbackStore_GetByRunID_NotFound(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	_, err := store.GetByRunID(ctx, "plr_nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestRunFeedbackStore_NilDiagnosisCorrect(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	fb := &RunFeedback{
		RunID:    "plr_nil_diag",
		SeriesID: "pbs_triage",
		Operator: "carol",
		// DiagnosisCorrect intentionally nil
	}
	if err := store.Submit(ctx, fb); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got, err := store.GetByRunID(ctx, fb.RunID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.DiagnosisCorrect != nil {
		t.Errorf("DiagnosisCorrect should be nil, got %v", got.DiagnosisCorrect)
	}
}

func TestRunFeedbackStore_StatsBySeries(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	seriesID := "pbs_lock_chain_triage"
	// 3 feedbacks: 2 correct, 1 incorrect.
	entries := []struct {
		runID   string
		correct bool
	}{
		{"plr_a", true},
		{"plr_b", true},
		{"plr_c", false},
	}
	for _, e := range entries {
		fb := &RunFeedback{
			RunID:            e.runID,
			SeriesID:         seriesID,
			DiagnosisCorrect: boolPtr(e.correct),
			Operator:         "test",
		}
		if err := store.Submit(ctx, fb); err != nil {
			t.Fatalf("Submit %s: %v", e.runID, err)
		}
	}

	stats, err := store.StatsBySeries(ctx, seriesID)
	if err != nil {
		t.Fatalf("StatsBySeries: %v", err)
	}
	if stats.FeedbackCount != 3 {
		t.Errorf("FeedbackCount: got %d, want 3", stats.FeedbackCount)
	}
	if stats.CorrectCount != 2 {
		t.Errorf("CorrectCount: got %d, want 2", stats.CorrectCount)
	}
	want := 2.0 / 3.0
	if diff := stats.AccuracyRate - want; diff < -0.001 || diff > 0.001 {
		t.Errorf("AccuracyRate: got %.4f, want %.4f", stats.AccuracyRate, want)
	}
}

func TestRunFeedbackStore_StatsBySeries_NoFeedback(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	stats, err := store.StatsBySeries(ctx, "pbs_no_feedback_series")
	if err != nil {
		t.Fatalf("StatsBySeries: %v", err)
	}
	if stats.FeedbackCount != 0 {
		t.Errorf("FeedbackCount: got %d, want 0", stats.FeedbackCount)
	}
	if stats.AccuracyRate != 0.0 {
		t.Errorf("AccuracyRate: got %f, want 0.0", stats.AccuracyRate)
	}
}

func TestRunFeedbackStore_StatsBySeries_NilDiagNotCounted(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	seriesID := "pbs_mixed"
	// One confirmed correct, one nil (unset) — nil should not count as correct.
	if err := store.Submit(ctx, &RunFeedback{
		RunID: "plr_x1", SeriesID: seriesID, DiagnosisCorrect: boolPtr(true), Operator: "t",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Submit(ctx, &RunFeedback{
		RunID: "plr_x2", SeriesID: seriesID, DiagnosisCorrect: nil, Operator: "t",
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := store.StatsBySeries(ctx, seriesID)
	if err != nil {
		t.Fatalf("StatsBySeries: %v", err)
	}
	// total=2, correct=1 (nil doesn't count)
	if stats.FeedbackCount != 2 {
		t.Errorf("FeedbackCount: got %d, want 2", stats.FeedbackCount)
	}
	if stats.CorrectCount != 1 {
		t.Errorf("CorrectCount: got %d, want 1", stats.CorrectCount)
	}
}
