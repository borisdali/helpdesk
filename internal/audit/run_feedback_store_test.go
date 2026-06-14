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
		RunID:          "plr_test01",
		FeedbackType:   "triage",
		FeedbackTime:   "post_incident",
		SeriesID:       "pbs_lock_chain_triage",
		VerdictCorrect: boolPtr(true),
		VerdictNotes:   "PID 867 held ShareLock",
		Operator:       "alice",
		SubmittedAt:    time.Now().UTC().Truncate(time.Second),
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
	if got.FeedbackType != "triage" {
		t.Errorf("FeedbackType: got %q, want triage", got.FeedbackType)
	}
	if got.FeedbackTime != "post_incident" {
		t.Errorf("FeedbackTime: got %q, want post_incident", got.FeedbackTime)
	}
	if got.SeriesID != fb.SeriesID {
		t.Errorf("SeriesID: got %q, want %q", got.SeriesID, fb.SeriesID)
	}
	if got.VerdictCorrect == nil || *got.VerdictCorrect != true {
		t.Errorf("VerdictCorrect: got %v, want true", got.VerdictCorrect)
	}
	if got.VerdictNotes != fb.VerdictNotes {
		t.Errorf("VerdictNotes: got %q, want %q", got.VerdictNotes, fb.VerdictNotes)
	}
	if got.Operator != fb.Operator {
		t.Errorf("Operator: got %q, want %q", got.Operator, fb.Operator)
	}
}

// TestRunFeedbackStore_AtGateAndPostIncident verifies at_gate and post_incident
// are stored as separate rows for the same run_id — the collision that the old
// single-PK schema had.
func TestRunFeedbackStore_AtGateAndPostIncident(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	atGate := &RunFeedback{
		RunID:          "plr_gate01",
		FeedbackType:   "triage",
		FeedbackTime:   "at_gate",
		SeriesID:       "pbs_triage",
		VerdictCorrect: boolPtr(true),
		VerdictNotes:   "hypothesis looked right at gate",
		Operator:       "alice",
	}
	if err := store.Submit(ctx, atGate); err != nil {
		t.Fatalf("Submit at_gate: %v", err)
	}

	postIncident := &RunFeedback{
		RunID:          "plr_gate01",
		FeedbackType:   "triage",
		FeedbackTime:   "post_incident",
		SeriesID:       "pbs_triage",
		VerdictCorrect: boolPtr(false),
		VerdictNotes:   "autovacuum was the real culprit",
		Operator:       "alice",
	}
	if err := store.Submit(ctx, postIncident); err != nil {
		t.Fatalf("Submit post_incident: %v", err)
	}

	gotAtGate, err := store.GetByRunIDAndType(ctx, "plr_gate01", "triage", "at_gate")
	if err != nil {
		t.Fatalf("GetByRunIDAndType at_gate: %v", err)
	}
	if gotAtGate.VerdictCorrect == nil || !*gotAtGate.VerdictCorrect {
		t.Errorf("at_gate VerdictCorrect: got %v, want true", gotAtGate.VerdictCorrect)
	}
	if gotAtGate.VerdictNotes != "hypothesis looked right at gate" {
		t.Errorf("at_gate VerdictNotes: got %q", gotAtGate.VerdictNotes)
	}

	gotPost, err := store.GetByRunID(ctx, "plr_gate01")
	if err != nil {
		t.Fatalf("GetByRunID post_incident: %v", err)
	}
	if gotPost.VerdictCorrect == nil || *gotPost.VerdictCorrect {
		t.Errorf("post_incident VerdictCorrect: got %v, want false", gotPost.VerdictCorrect)
	}
	if gotPost.VerdictNotes != "autovacuum was the real culprit" {
		t.Errorf("post_incident VerdictNotes: got %q", gotPost.VerdictNotes)
	}
}

// TestRunFeedbackStore_Upsert verifies that a second Submit to the same
// (run_id, feedback_type, feedback_time) overwrites the previous entry.
func TestRunFeedbackStore_Upsert(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	fb := &RunFeedback{
		RunID:          "plr_upsert",
		FeedbackType:   "triage",
		FeedbackTime:   "post_incident",
		SeriesID:       "pbs_triage",
		VerdictCorrect: boolPtr(true),
		Operator:       "bob",
	}
	if err := store.Submit(ctx, fb); err != nil {
		t.Fatalf("first Submit: %v", err)
	}

	fb.VerdictCorrect = boolPtr(false)
	fb.VerdictNotes = "actually a different blocker"
	if err := store.Submit(ctx, fb); err != nil {
		t.Fatalf("second Submit: %v", err)
	}

	got, err := store.GetByRunID(ctx, fb.RunID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.VerdictCorrect == nil || *got.VerdictCorrect != false {
		t.Errorf("after upsert VerdictCorrect: got %v, want false", got.VerdictCorrect)
	}
	if got.VerdictNotes != "actually a different blocker" {
		t.Errorf("after upsert VerdictNotes: got %q", got.VerdictNotes)
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

func TestRunFeedbackStore_NilVerdictCorrect(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	fb := &RunFeedback{
		RunID:        "plr_nil_verdict",
		FeedbackType: "triage",
		FeedbackTime: "post_incident",
		SeriesID:     "pbs_triage",
		Operator:     "carol",
		// VerdictCorrect intentionally nil
	}
	if err := store.Submit(ctx, fb); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got, err := store.GetByRunID(ctx, fb.RunID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.VerdictCorrect != nil {
		t.Errorf("VerdictCorrect should be nil, got %v", got.VerdictCorrect)
	}
}

func TestRunFeedbackStore_StatsBySeries(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	seriesID := "pbs_lock_chain_triage"
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
			RunID:          e.runID,
			FeedbackType:   "triage",
			FeedbackTime:   "post_incident",
			SeriesID:       seriesID,
			VerdictCorrect: boolPtr(e.correct),
			Operator:       "test",
		}
		if err := store.Submit(ctx, fb); err != nil {
			t.Fatalf("Submit %s: %v", e.runID, err)
		}
	}

	// at_gate rows for same runs should NOT be counted in StatsBySeries.
	if err := store.Submit(ctx, &RunFeedback{
		RunID: "plr_a", FeedbackType: "triage", FeedbackTime: "at_gate",
		SeriesID: seriesID, VerdictCorrect: boolPtr(true), Operator: "test",
	}); err != nil {
		t.Fatalf("Submit at_gate: %v", err)
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

func TestRunFeedbackStore_StatsBySeries_NilVerdictNotCounted(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	seriesID := "pbs_mixed"
	if err := store.Submit(ctx, &RunFeedback{
		RunID: "plr_x1", FeedbackType: "triage", FeedbackTime: "post_incident",
		SeriesID: seriesID, VerdictCorrect: boolPtr(true), Operator: "t",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Submit(ctx, &RunFeedback{
		RunID: "plr_x2", FeedbackType: "triage", FeedbackTime: "post_incident",
		SeriesID: seriesID, VerdictCorrect: nil, Operator: "t",
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := store.StatsBySeries(ctx, seriesID)
	if err != nil {
		t.Fatalf("StatsBySeries: %v", err)
	}
	if stats.FeedbackCount != 1 {
		t.Errorf("FeedbackCount: got %d, want 1", stats.FeedbackCount)
	}
	if stats.CorrectCount != 1 {
		t.Errorf("CorrectCount: got %d, want 1", stats.CorrectCount)
	}
}

func TestRunFeedbackStore_ListPending(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	entries := []*RunFeedback{
		{RunID: "plr_p1", FeedbackType: "triage", FeedbackTime: "post_incident", SeriesID: "pbs_s1", VerdictCorrect: nil, Operator: "faulttest"},
		{RunID: "plr_p2", FeedbackType: "triage", FeedbackTime: "post_incident", SeriesID: "pbs_s2", VerdictCorrect: boolPtr(true), Operator: "alice"},
		{RunID: "plr_p3", FeedbackType: "triage", FeedbackTime: "post_incident", SeriesID: "pbs_s1", VerdictCorrect: boolPtr(false), Operator: "bob"},
		// at_gate pending should NOT appear in ListPending.
		{RunID: "plr_p4", FeedbackType: "triage", FeedbackTime: "at_gate", SeriesID: "pbs_s1", VerdictCorrect: nil, Operator: "carol"},
	}
	for _, fb := range entries {
		if err := store.Submit(ctx, fb); err != nil {
			t.Fatalf("Submit %s: %v", fb.RunID, err)
		}
	}

	pending, err := store.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("len(pending) = %d, want 1", len(pending))
	}
	if pending[0].RunID != "plr_p1" {
		t.Errorf("RunID = %q, want plr_p1", pending[0].RunID)
	}
	if pending[0].VerdictCorrect != nil {
		t.Errorf("VerdictCorrect should be nil for pending record")
	}
}

func TestRunFeedbackStore_ListPending_Empty(t *testing.T) {
	ctx := context.Background()
	store, _ := newRunFeedbackStore(t)

	pending, err := store.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected empty slice, got %d items", len(pending))
	}
}
