package audit

import (
	"context"
	"path/filepath"
	"testing"
)

func newRunStepStore(t *testing.T) *PlaybookRunStepStore {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	s, err := NewPlaybookRunStepStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookRunStepStore: %v", err)
	}
	return s
}

func TestPlaybookRunStepStore_CreateAndList(t *testing.T) {
	s := newRunStepStore(t)
	ctx := context.Background()
	runID := "plr_test01"

	step := &PlaybookRunStep{
		RunID:     runID,
		StepIndex: 1,
		Agent:     "database",
		Tool:      "get_blocking_queries",
		Args:      map[string]any{"connection_string": "host=localhost"},
		Reason:    "Confirm blocker is present",
	}
	if err := s.CreateStep(ctx, step); err != nil {
		t.Fatalf("CreateStep: %v", err)
	}
	if step.Status != "proposed" {
		t.Errorf("default status = %q, want proposed", step.Status)
	}
	if step.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}

	steps, err := s.ListSteps(ctx, runID)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("len(steps) = %d, want 1", len(steps))
	}
	got := steps[0]
	if got.Tool != "get_blocking_queries" {
		t.Errorf("tool = %q, want get_blocking_queries", got.Tool)
	}
	if got.Reason != "Confirm blocker is present" {
		t.Errorf("reason = %q", got.Reason)
	}
	if got.Args["connection_string"] != "host=localhost" {
		t.Errorf("args[connection_string] = %v", got.Args["connection_string"])
	}
}

func TestPlaybookRunStepStore_UpdateStep(t *testing.T) {
	s := newRunStepStore(t)
	ctx := context.Background()
	runID := "plr_update01"

	if err := s.CreateStep(ctx, &PlaybookRunStep{
		RunID: runID, StepIndex: 1, Agent: "database", Tool: "terminate_connection",
	}); err != nil {
		t.Fatalf("CreateStep: %v", err)
	}

	if err := s.UpdateStep(ctx, runID, 1, "succeeded", "apr_abc", "terminated 1 connection", ""); err != nil {
		t.Fatalf("UpdateStep: %v", err)
	}

	steps, _ := s.ListSteps(ctx, runID)
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	got := steps[0]
	if got.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", got.Status)
	}
	if got.ApprovalID != "apr_abc" {
		t.Errorf("approval_id = %q, want apr_abc", got.ApprovalID)
	}
	if got.Result != "terminated 1 connection" {
		t.Errorf("result = %q", got.Result)
	}
}

func TestPlaybookRunStepStore_GetPendingStep_Found(t *testing.T) {
	s := newRunStepStore(t)
	ctx := context.Background()
	runID := "plr_pending01"

	if err := s.CreateStep(ctx, &PlaybookRunStep{
		RunID: runID, StepIndex: 1, Agent: "database", Tool: "terminate_connection", Status: "proposed",
	}); err != nil {
		t.Fatalf("CreateStep: %v", err)
	}

	step, err := s.GetPendingStep(ctx, runID)
	if err != nil {
		t.Fatalf("GetPendingStep: %v", err)
	}
	if step == nil {
		t.Fatal("expected pending step, got nil")
	}
	if step.Tool != "terminate_connection" {
		t.Errorf("tool = %q, want terminate_connection", step.Tool)
	}
}

func TestPlaybookRunStepStore_GetPendingStep_None(t *testing.T) {
	s := newRunStepStore(t)
	ctx := context.Background()

	step, err := s.GetPendingStep(ctx, "plr_no_steps")
	if err != nil {
		t.Fatalf("GetPendingStep on empty run: %v", err)
	}
	if step != nil {
		t.Errorf("expected nil for run with no steps, got %+v", step)
	}
}

func TestPlaybookRunStepStore_GetPendingStep_SkipsCompleted(t *testing.T) {
	s := newRunStepStore(t)
	ctx := context.Background()
	runID := "plr_skip01"

	// Completed step should not be returned.
	if err := s.CreateStep(ctx, &PlaybookRunStep{
		RunID: runID, StepIndex: 1, Agent: "database", Tool: "get_blocking_queries", Status: "succeeded",
	}); err != nil {
		t.Fatalf("CreateStep step 1: %v", err)
	}
	// Proposed step should be returned.
	if err := s.CreateStep(ctx, &PlaybookRunStep{
		RunID: runID, StepIndex: 2, Agent: "database", Tool: "terminate_connection", Status: "proposed",
	}); err != nil {
		t.Fatalf("CreateStep step 2: %v", err)
	}

	pending, err := s.GetPendingStep(ctx, runID)
	if err != nil {
		t.Fatalf("GetPendingStep: %v", err)
	}
	if pending == nil {
		t.Fatal("expected step 2, got nil")
	}
	if pending.StepIndex != 2 {
		t.Errorf("step_index = %d, want 2", pending.StepIndex)
	}
}

func TestPlaybookRunStepStore_NextStepIndex(t *testing.T) {
	s := newRunStepStore(t)
	ctx := context.Background()
	runID := "plr_next01"

	// Empty run → next index is 1.
	idx, err := s.NextStepIndex(ctx, runID)
	if err != nil {
		t.Fatalf("NextStepIndex on empty: %v", err)
	}
	if idx != 1 {
		t.Errorf("next index = %d, want 1", idx)
	}

	// After inserting step 1, next should be 2.
	if err := s.CreateStep(ctx, &PlaybookRunStep{
		RunID: runID, StepIndex: 1, Agent: "database", Tool: "get_blocking_queries",
	}); err != nil {
		t.Fatalf("CreateStep: %v", err)
	}
	idx, err = s.NextStepIndex(ctx, runID)
	if err != nil {
		t.Fatalf("NextStepIndex after one step: %v", err)
	}
	if idx != 2 {
		t.Errorf("next index = %d, want 2", idx)
	}
}

func TestPlaybookRunStepStore_UniqueConstraint(t *testing.T) {
	s := newRunStepStore(t)
	ctx := context.Background()
	runID := "plr_dup01"

	step := &PlaybookRunStep{RunID: runID, StepIndex: 1, Agent: "database", Tool: "get_blocking_queries"}
	if err := s.CreateStep(ctx, step); err != nil {
		t.Fatalf("first CreateStep: %v", err)
	}
	// Second insert with same run_id + step_index should fail.
	step2 := &PlaybookRunStep{RunID: runID, StepIndex: 1, Agent: "database", Tool: "terminate_connection"}
	if err := s.CreateStep(ctx, step2); err == nil {
		t.Error("expected error for duplicate (run_id, step_index), got nil")
	}
}

func TestPlaybookRunStepStore_ListSteps_OrderedByIndex(t *testing.T) {
	s := newRunStepStore(t)
	ctx := context.Background()
	runID := "plr_order01"

	for _, idx := range []int{3, 1, 2} {
		if err := s.CreateStep(ctx, &PlaybookRunStep{
			RunID: runID, StepIndex: idx, Agent: "database",
			Tool: "tool_" + string(rune('0'+idx)),
		}); err != nil {
			t.Fatalf("CreateStep %d: %v", idx, err)
		}
	}

	steps, err := s.ListSteps(ctx, runID)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("len(steps) = %d, want 3", len(steps))
	}
	if steps[0].StepIndex != 1 || steps[1].StepIndex != 2 || steps[2].StepIndex != 3 {
		t.Errorf("steps not ordered by index: %v", []int{steps[0].StepIndex, steps[1].StepIndex, steps[2].StepIndex})
	}
}
