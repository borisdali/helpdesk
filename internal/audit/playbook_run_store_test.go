package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newPlaybookRunStore(t *testing.T) *PlaybookRunStore {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	s, err := NewPlaybookRunStore(store.DB())
	if err != nil {
		t.Fatalf("NewPlaybookRunStore: %v", err)
	}
	return s
}

func TestPlaybookRunStore_RecordAndList(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	run := &PlaybookRun{
		PlaybookID:    "pb_abc123",
		SeriesID:      "pbs_vacuum_triage",
		ExecutionMode: "fleet",
		Outcome:       "resolved",
		Operator:      "alice@example.com",
		StartedAt:     time.Now().UTC(),
	}
	if err := s.Record(ctx, run); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(run.RunID) < 4 || run.RunID[:4] != "plr_" {
		t.Errorf("run_id = %q, want plr_ prefix", run.RunID)
	}

	runs, err := s.ListByPlaybook(ctx, "pb_abc123", 10)
	if err != nil {
		t.Fatalf("ListByPlaybook: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Outcome != "resolved" {
		t.Errorf("outcome = %q, want resolved", runs[0].Outcome)
	}
	if runs[0].Operator != "alice@example.com" {
		t.Errorf("operator = %q, want alice@example.com", runs[0].Operator)
	}
}

func TestPlaybookRunStore_Update(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	run := &PlaybookRun{
		PlaybookID:    "pb_abc123",
		SeriesID:      "pbs_db_restart_triage",
		ExecutionMode: "agent",
		StartedAt:     time.Now().UTC(),
	}
	if err := s.Record(ctx, run); err != nil {
		t.Fatalf("Record: %v", err)
	}

	err := s.Update(ctx, run.RunID, "escalated", "pbs_db_config_recovery", "Logs show FATAL: invalid value for parameter max_connections")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	runs, err := s.ListByPlaybook(ctx, "pb_abc123", 1)
	if err != nil {
		t.Fatalf("ListByPlaybook: %v", err)
	}
	if runs[0].Outcome != "escalated" {
		t.Errorf("outcome = %q, want escalated", runs[0].Outcome)
	}
	if runs[0].EscalatedTo != "pbs_db_config_recovery" {
		t.Errorf("escalated_to = %q", runs[0].EscalatedTo)
	}
	if runs[0].FindingsSummary == "" {
		t.Error("findings_summary should be set after update")
	}
	if runs[0].CompletedAt.IsZero() {
		t.Error("completed_at should be set after update")
	}
}

func TestPlaybookRunStore_Stats(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	for _, outcome := range []string{"resolved", "resolved", "escalated", "abandoned", "unknown"} {
		if err := s.Record(ctx, &PlaybookRun{
			PlaybookID:    "pb_abc",
			SeriesID:      "pbs_test_series",
			ExecutionMode: "fleet",
			Outcome:       outcome,
			StartedAt:     time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Record(%s): %v", outcome, err)
		}
	}

	st, err := s.Stats(ctx, "pbs_test_series")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.TotalRuns != 5 {
		t.Errorf("total_runs = %d, want 5", st.TotalRuns)
	}
	if st.Resolved != 2 {
		t.Errorf("resolved = %d, want 2", st.Resolved)
	}
	if st.Escalated != 1 {
		t.Errorf("escalated = %d, want 1", st.Escalated)
	}
	if st.Abandoned != 1 {
		t.Errorf("abandoned = %d, want 1", st.Abandoned)
	}
	if st.ResolutionRate != 0.4 {
		t.Errorf("resolution_rate = %v, want 0.4", st.ResolutionRate)
	}
	if st.EscalationRate != 0.2 {
		t.Errorf("escalation_rate = %v, want 0.2", st.EscalationRate)
	}
	if st.LastRunAt == "" {
		t.Error("last_run_at should be set")
	}
}

func TestPlaybookRunStore_StatsBatch(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	for _, r := range []struct {
		series, outcome string
	}{
		{"pbs_a", "resolved"},
		{"pbs_a", "escalated"},
		{"pbs_b", "resolved"},
	} {
		if err := s.Record(ctx, &PlaybookRun{
			PlaybookID:    "pb_x",
			SeriesID:      r.series,
			ExecutionMode: "fleet",
			Outcome:       r.outcome,
			StartedAt:     time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	batch, err := s.StatsBatch(ctx, []string{"pbs_a", "pbs_b", "pbs_c"})
	if err != nil {
		t.Fatalf("StatsBatch: %v", err)
	}
	if batch["pbs_a"].TotalRuns != 2 {
		t.Errorf("pbs_a total_runs = %d, want 2", batch["pbs_a"].TotalRuns)
	}
	if batch["pbs_b"].TotalRuns != 1 {
		t.Errorf("pbs_b total_runs = %d, want 1", batch["pbs_b"].TotalRuns)
	}
	if _, ok := batch["pbs_c"]; ok {
		t.Error("pbs_c should not appear in batch result (no runs)")
	}
}

func TestPlaybookRunStore_Stats_Empty(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	st, err := s.Stats(ctx, "pbs_nonexistent")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.TotalRuns != 0 {
		t.Errorf("total_runs = %d, want 0", st.TotalRuns)
	}
	if st.ResolutionRate != 0 || st.EscalationRate != 0 {
		t.Error("rates should be 0 when no runs exist")
	}
}

func TestPlaybookRunStore_DefaultOutcome(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	// Outcome omitted — should default to "unknown".
	run := &PlaybookRun{
		PlaybookID:    "pb_def",
		SeriesID:      "pbs_default_test",
		ExecutionMode: "agent",
		StartedAt:     time.Now().UTC(),
	}
	if err := s.Record(ctx, run); err != nil {
		t.Fatalf("Record: %v", err)
	}
	runs, err := s.ListByPlaybook(ctx, "pb_def", 1)
	if err != nil {
		t.Fatalf("ListByPlaybook: %v", err)
	}
	if runs[0].Outcome != "unknown" {
		t.Errorf("outcome = %q, want unknown", runs[0].Outcome)
	}
}

