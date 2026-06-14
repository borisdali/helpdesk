package audit

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
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
