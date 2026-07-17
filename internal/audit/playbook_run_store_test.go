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
	s, err := NewPlaybookRunStore(store.DB(), false)
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

	err := s.Update(ctx, run.RunID, "escalated", "pbs_db_config_recovery", "", "Logs show FATAL: invalid value for parameter max_connections", "", "", nil)
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

func TestPlaybookRunStore_GetByRunID(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	run := &PlaybookRun{
		PlaybookID:      "pb_get1",
		SeriesID:        "pbs_get_series",
		ExecutionMode:   "agent",
		Outcome:         "escalated",
		EscalatedTo:     "pbs_pitr",
		FindingsSummary: "WAL corruption detected; recommend PITR recovery.",
		Operator:        "bob@example.com",
		StartedAt:       time.Now().UTC(),
	}
	if err := s.Record(ctx, run); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := s.GetByRunID(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.RunID != run.RunID {
		t.Errorf("run_id = %q, want %q", got.RunID, run.RunID)
	}
	if got.Outcome != "escalated" {
		t.Errorf("outcome = %q, want escalated", got.Outcome)
	}
	if got.EscalatedTo != "pbs_pitr" {
		t.Errorf("escalated_to = %q, want pbs_pitr", got.EscalatedTo)
	}
	if got.FindingsSummary != run.FindingsSummary {
		t.Errorf("findings_summary = %q", got.FindingsSummary)
	}
	if got.Operator != "bob@example.com" {
		t.Errorf("operator = %q", got.Operator)
	}
}

func TestPlaybookRunStore_GetByRunID_NotFound(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	_, err := s.GetByRunID(ctx, "plr_nonexistent")
	if err == nil {
		t.Error("expected error for non-existent run_id, got nil")
	}
}


func TestPlaybookRunStore_DiagnosticReport_RoundTrip(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	report := &DiagnosticReport{
		RootCause:   "Container was stopped by an operator",
		ActionTaken: "none — escalation recommended",
		Hypotheses: []DiagnosticHypothesis{
			{Rank: 1, Text: "Operator stop", Confidence: 0.90, Evidence: "exitcode=0", IsPrimary: true},
			{Rank: 2, Text: "Disk exhaustion", Confidence: 0.20, RejectedReason: "disk 45% used", IsPrimary: false},
		},
	}

	run := &PlaybookRun{
		PlaybookID:      "pb_diag_test",
		SeriesID:        "pbs_diag_test",
		ExecutionMode:   "agent",
		Outcome:         "resolved",
		DiagnosticReport: report,
		Operator:        "test",
		StartedAt:       time.Now().UTC(),
	}
	if err := s.Record(ctx, run); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := s.GetByRunID(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.DiagnosticReport == nil {
		t.Fatal("DiagnosticReport should not be nil after round-trip")
	}
	if got.DiagnosticReport.RootCause != report.RootCause {
		t.Errorf("RootCause = %q, want %q", got.DiagnosticReport.RootCause, report.RootCause)
	}
	if len(got.DiagnosticReport.Hypotheses) != 2 {
		t.Fatalf("Hypotheses len = %d, want 2", len(got.DiagnosticReport.Hypotheses))
	}
	if !got.DiagnosticReport.Hypotheses[0].IsPrimary {
		t.Error("first hypothesis should be primary")
	}
	if got.DiagnosticReport.Hypotheses[1].RejectedReason == "" {
		t.Error("second hypothesis should have rejection reason")
	}

	// Also test Update path.
	report2 := &DiagnosticReport{
		RootCause:  "Updated root cause",
		Hypotheses: []DiagnosticHypothesis{{Rank: 1, Text: "Updated", Confidence: 0.99, IsPrimary: true}},
	}
	if err := s.Update(ctx, run.RunID, "resolved", "", "", "Updated findings", "", "", report2); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got2, err := s.GetByRunID(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetByRunID after Update: %v", err)
	}
	if got2.DiagnosticReport == nil || got2.DiagnosticReport.RootCause != "Updated root cause" {
		t.Errorf("DiagnosticReport not updated correctly: %+v", got2.DiagnosticReport)
	}
}

func TestPlaybookRunStore_NewFields_RoundTrip(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	run := &PlaybookRun{
		PlaybookID:     "pb_newfields",
		SeriesID:       "pbs_new_triage",
		ExecutionMode:  "agent",
		Operator:       "alice",
		TraceID:        "tr_abc123",
		PriorRunID:     "plr_prior001",
		TriggerContext: "ALERT: connection count exceeded 90% threshold",
		StartedAt:      time.Now().UTC().Truncate(time.Second),
	}
	if err := s.Record(ctx, run); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := s.GetByRunID(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetByRunID: %v", err)
	}
	if got.TraceID != "tr_abc123" {
		t.Errorf("TraceID = %q, want %q", got.TraceID, "tr_abc123")
	}
	if got.PriorRunID != "plr_prior001" {
		t.Errorf("PriorRunID = %q, want %q", got.PriorRunID, "plr_prior001")
	}
	if got.TriggerContext != "ALERT: connection count exceeded 90% threshold" {
		t.Errorf("TriggerContext = %q, want alert text", got.TriggerContext)
	}
	if got.AgentTranscript != "" {
		t.Errorf("AgentTranscript should be empty before Update, got %q", got.AgentTranscript)
	}

	if err := s.Update(ctx, run.RunID, "resolved", "", "", "findings text", "full agent reasoning narrative here", "tr_abc123", nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got2, err := s.GetByRunID(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetByRunID after Update: %v", err)
	}
	if got2.AgentTranscript != "full agent reasoning narrative here" {
		t.Errorf("AgentTranscript = %q, want %q", got2.AgentTranscript, "full agent reasoning narrative here")
	}
	// TraceID and PriorRunID are immutable — Update must not clear them.
	if got2.TraceID != "tr_abc123" {
		t.Errorf("TraceID changed after Update: got %q", got2.TraceID)
	}
	if got2.PriorRunID != "plr_prior001" {
		t.Errorf("PriorRunID changed after Update: got %q", got2.PriorRunID)
	}
}

func TestPlaybookRunStore_ListByPriorRunID(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	triage := &PlaybookRun{
		PlaybookID:    "pb_triage1",
		SeriesID:      "pbs_lock_chain_triage",
		ExecutionMode: "agent",
		Operator:      "alice",
		StartedAt:     time.Now().UTC().Truncate(time.Second),
	}
	if err := s.Record(ctx, triage); err != nil {
		t.Fatalf("Record triage: %v", err)
	}

	rem := &PlaybookRun{
		PlaybookID:    "pb_remediate1",
		SeriesID:      "pbs_lock_chain_remediate",
		ExecutionMode: "agent_approve",
		PriorRunID:    triage.RunID,
		Operator:      "alice",
		StartedAt:     time.Now().UTC().Truncate(time.Second),
	}
	if err := s.Record(ctx, rem); err != nil {
		t.Fatalf("Record remediation: %v", err)
	}

	// Unrelated run — must not appear in results.
	other := &PlaybookRun{
		PlaybookID:    "pb_other1",
		SeriesID:      "pbs_other",
		ExecutionMode: "agent",
		PriorRunID:    "plr_unrelated",
		Operator:      "bob",
		StartedAt:     time.Now().UTC().Truncate(time.Second),
	}
	if err := s.Record(ctx, other); err != nil {
		t.Fatalf("Record other: %v", err)
	}

	runs, err := s.ListByPriorRunID(ctx, triage.RunID, 10)
	if err != nil {
		t.Fatalf("ListByPriorRunID: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(runs))
	}
	if runs[0].RunID != rem.RunID {
		t.Errorf("unexpected run_id %q, want %q", runs[0].RunID, rem.RunID)
	}

	// No results for an unknown prior_run_id.
	none, err := s.ListByPriorRunID(ctx, "plr_nonexistent", 10)
	if err != nil {
		t.Fatalf("ListByPriorRunID (empty): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("want 0 runs for unknown prior_run_id, got %d", len(none))
	}
}

func TestPlaybookRunStore_StatsByVersion(t *testing.T) {
	ctx := context.Background()
	raw, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { raw.Close() })

	runStore, err := NewPlaybookRunStore(raw.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookRunStore: %v", err)
	}
	pbStore, err := NewPlaybookStore(raw.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookStore: %v", err)
	}
	stepStore, err := NewPlaybookRunStepStore(raw.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookRunStepStore: %v", err)
	}
	evalStore, err := NewRunEvaluationStore(raw.DB(), false)
	if err != nil {
		t.Fatalf("NewRunEvaluationStore: %v", err)
	}

	const seriesID = "pbs_test_versioned"

	// Insert two playbook versions — v1.0 (inactive) and v1.1 (active).
	// Both use the same SeriesID so they belong to the same series.
	pb10 := &Playbook{
		Name:          "Test Playbook v1.0",
		SeriesID:      seriesID,
		Version:       "1.0",
		IsActive:      false,
		ExecutionMode: "agent",
		ProblemClass:  "test",
		Guidance:      "v1.0 guidance",
	}
	if err := pbStore.Create(ctx, pb10); err != nil {
		t.Fatalf("Create v1.0: %v", err)
	}
	pb11 := &Playbook{
		Name:          "Test Playbook v1.1",
		SeriesID:      seriesID,
		Version:       "1.1",
		IsActive:      true,
		ExecutionMode: "agent",
		ProblemClass:  "test",
		Guidance:      "v1.1 guidance",
	}
	if err := pbStore.Create(ctx, pb11); err != nil {
		t.Fatalf("Create v1.1: %v", err)
	}

	id10 := pb10.PlaybookID
	id11 := pb11.PlaybookID
	if id10 == "" || id11 == "" {
		t.Fatalf("playbook IDs not populated after Create: id10=%q id11=%q", id10, id11)
	}

	now := time.Now().UTC()

	// v1.0: 2 resolved runs, 1 abandoned.
	for i, outcome := range []string{"resolved", "resolved", "abandoned"} {
		r := &PlaybookRun{
			RunID:       "plr_v10_" + string(rune('a'+i)),
			PlaybookID:  id10,
			SeriesID:    seriesID,
			Outcome:     outcome,
			StartedAt:   now.Add(time.Duration(-i*60) * time.Second),
			CompletedAt: now.Add(time.Duration(-i*60+30) * time.Second),
		}
		if err := runStore.Record(ctx, r); err != nil {
			t.Fatalf("Record v1.0 run: %v", err)
		}
		// Add 2 steps to resolved runs.
		if outcome == "resolved" {
			for step := 1; step <= 2; step++ {
				if err := stepStore.CreateStep(ctx, &PlaybookRunStep{
					RunID: r.RunID, StepIndex: step, Agent: "db", Tool: "check_connection", Status: "succeeded",
				}); err != nil {
					t.Fatalf("CreateStep: %v", err)
				}
			}
		}
	}

	// v1.1: 2 resolved runs, avg steps = 4.
	for i, outcome := range []string{"resolved", "resolved"} {
		r := &PlaybookRun{
			RunID:       "plr_v11_" + string(rune('a'+i)),
			PlaybookID:  id11,
			SeriesID:    seriesID,
			Outcome:     outcome,
			StartedAt:   now.Add(time.Duration(-i*60) * time.Second),
			CompletedAt: now.Add(time.Duration(-i*60+10) * time.Second),
		}
		if err := runStore.Record(ctx, r); err != nil {
			t.Fatalf("Record v1.1 run: %v", err)
		}
		for step := 1; step <= 4; step++ {
			if err := stepStore.CreateStep(ctx, &PlaybookRunStep{
				RunID: r.RunID, StepIndex: step, Agent: "db", Tool: "check_connection", Status: "succeeded",
			}); err != nil {
				t.Fatalf("CreateStep: %v", err)
			}
		}
		// Add eval score only to v1.1 runs.
		if err := evalStore.Upsert(ctx, &RunEvaluation{
			RunID:          r.RunID,
			FailureID:      "db-lock-contention",
			DiagnosisScore: 0.9,
			OverallScore:   0.9,
			Passed:         true,
		}); err != nil {
			t.Fatalf("Upsert eval: %v", err)
		}
	}

	versions, err := runStore.StatsByVersion(ctx, seriesID)
	if err != nil {
		t.Fatalf("StatsByVersion: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("want 2 versions, got %d", len(versions))
	}

	v10 := versions[0]
	if v10.Version != "1.0" {
		t.Errorf("versions[0].Version = %q, want 1.0", v10.Version)
	}
	if v10.IsActive {
		t.Error("v1.0 should not be active")
	}
	if v10.TotalRuns != 3 {
		t.Errorf("v1.0 TotalRuns = %d, want 3", v10.TotalRuns)
	}
	if v10.Resolved != 2 {
		t.Errorf("v1.0 Resolved = %d, want 2", v10.Resolved)
	}
	if v10.DiagEvalCount != 0 {
		t.Errorf("v1.0 DiagEvalCount = %d, want 0", v10.DiagEvalCount)
	}
	// avg steps: (2+2+0)/3 = 1.33...
	wantSteps10 := (2.0 + 2.0 + 0.0) / 3.0
	if v10.AvgStepCount < wantSteps10-0.01 || v10.AvgStepCount > wantSteps10+0.01 {
		t.Errorf("v1.0 AvgStepCount = %.2f, want %.2f", v10.AvgStepCount, wantSteps10)
	}

	v11 := versions[1]
	if v11.Version != "1.1" {
		t.Errorf("versions[1].Version = %q, want 1.1", v11.Version)
	}
	if !v11.IsActive {
		t.Error("v1.1 should be active")
	}
	if v11.TotalRuns != 2 {
		t.Errorf("v1.1 TotalRuns = %d, want 2", v11.TotalRuns)
	}
	if v11.Resolved != 2 {
		t.Errorf("v1.1 Resolved = %d, want 2", v11.Resolved)
	}
	if v11.DiagEvalCount != 2 {
		t.Errorf("v1.1 DiagEvalCount = %d, want 2", v11.DiagEvalCount)
	}
	if v11.AvgDiagnosisScore < 0.89 || v11.AvgDiagnosisScore > 0.91 {
		t.Errorf("v1.1 AvgDiagnosisScore = %.2f, want 0.90", v11.AvgDiagnosisScore)
	}
	if v11.RemedEvalCount != 0 {
		t.Errorf("v1.1 RemedEvalCount = %d, want 0 (no remediation runs)", v11.RemedEvalCount)
	}
	// avg steps: 4.0
	if v11.AvgStepCount < 3.99 || v11.AvgStepCount > 4.01 {
		t.Errorf("v1.1 AvgStepCount = %.2f, want 4.0", v11.AvgStepCount)
	}
	// recovery time: ~10s per run
	if v11.AvgRecoverySecs < 9 || v11.AvgRecoverySecs > 11 {
		t.Errorf("v1.1 AvgRecoverySecs = %.1f, want ~10", v11.AvgRecoverySecs)
	}

	// PlaybookID and OriginTrace must be propagated from the playbooks table.
	if v10.PlaybookID != id10 {
		t.Errorf("v1.0 PlaybookID = %q, want %q", v10.PlaybookID, id10)
	}
	if v11.PlaybookID != id11 {
		t.Errorf("v1.1 PlaybookID = %q, want %q", v11.PlaybookID, id11)
	}
	// OriginTrace is empty for manually-created playbooks (no from-trace call).
	if v10.OriginTrace != "" {
		t.Errorf("v1.0 OriginTrace = %q, want empty (no origin trace set)", v10.OriginTrace)
	}

	// Empty series returns empty slice.
	empty, err := runStore.StatsByVersion(ctx, "pbs_nonexistent")
	if err != nil {
		t.Fatalf("StatsByVersion (empty): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("want 0 versions for unknown series, got %d", len(empty))
	}

	// Remediation feedback per version — dual-posted against the remediation run ID.
	fbStore, err := NewRunFeedbackStore(raw.DB(), false)
	if err != nil {
		t.Fatalf("NewRunFeedbackStore: %v", err)
	}
	tr := true
	// Post one "approach appropriate" verdict against a v1.0 run and two against v1.1 runs.
	for _, runID := range []string{"plr_v10_a", "plr_v11_a", "plr_v11_b"} {
		if err := fbStore.Submit(ctx, &RunFeedback{
			RunID:          runID,
			FeedbackType:   "remediation",
			FeedbackTime:   "post_incident",
			VerdictCorrect: &tr,
		}); err != nil {
			t.Fatalf("Submit feedback for %s: %v", runID, err)
		}
	}
	// One "not appropriate" verdict against another v1.0 run.
	fa := false
	if err := fbStore.Submit(ctx, &RunFeedback{
		RunID:          "plr_v10_b",
		FeedbackType:   "remediation",
		FeedbackTime:   "post_incident",
		VerdictCorrect: &fa,
	}); err != nil {
		t.Fatalf("Submit feedback: %v", err)
	}

	versions2, err := runStore.StatsByVersion(ctx, seriesID)
	if err != nil {
		t.Fatalf("StatsByVersion (with feedback): %v", err)
	}
	if len(versions2) != 2 {
		t.Fatalf("want 2 versions, got %d", len(versions2))
	}
	// v1.0: 2 feedback records (1 correct, 1 not) → 50% approach rate.
	v10f := versions2[0]
	if v10f.RemFeedbackCount != 2 {
		t.Errorf("v1.0 RemFeedbackCount = %d, want 2", v10f.RemFeedbackCount)
	}
	if v10f.RemFeedbackRate < 0.49 || v10f.RemFeedbackRate > 0.51 {
		t.Errorf("v1.0 RemFeedbackRate = %.2f, want 0.50", v10f.RemFeedbackRate)
	}
	// v1.1: 2 feedback records, both correct → 100% approach rate.
	v11f := versions2[1]
	if v11f.RemFeedbackCount != 2 {
		t.Errorf("v1.1 RemFeedbackCount = %d, want 2", v11f.RemFeedbackCount)
	}
	if v11f.RemFeedbackRate < 0.99 {
		t.Errorf("v1.1 RemFeedbackRate = %.2f, want 1.0", v11f.RemFeedbackRate)
	}
}

// TestPlaybookRunStore_StatsByVersion_JudgeVerdict verifies that a judge verdict
// set on a playbook via SetJudgeVerdict propagates into StatsByVersion output.
func TestPlaybookRunStore_StatsByVersion_JudgeVerdict(t *testing.T) {
	ctx := context.Background()
	raw, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { raw.Close() })

	runStore, err := NewPlaybookRunStore(raw.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookRunStore: %v", err)
	}
	pbStore, err := NewPlaybookStore(raw.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookStore: %v", err)
	}
	// StatsByVersion JOINs playbook_run_steps and run_evaluation; both tables must exist.
	if _, err := NewPlaybookRunStepStore(raw.DB(), false); err != nil {
		t.Fatalf("NewPlaybookRunStepStore: %v", err)
	}
	if _, err := NewRunEvaluationStore(raw.DB(), false); err != nil {
		t.Fatalf("NewRunEvaluationStore: %v", err)
	}

	const seriesID = "pbs_judge_test"

	pb := &Playbook{
		Name: "Judge Test Playbook", SeriesID: seriesID,
		Version: "1.0", IsActive: false, ExecutionMode: "agent", ProblemClass: "test",
	}
	if err := pbStore.Create(ctx, pb); err != nil {
		t.Fatalf("Create playbook: %v", err)
	}
	if err := runStore.Record(ctx, &PlaybookRun{
		RunID: "plr_judge_a", PlaybookID: pb.PlaybookID, SeriesID: seriesID,
		Outcome: "resolved", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Record run: %v", err)
	}

	// No verdict yet — JudgeVerdict must be empty.
	versions, err := runStore.StatsByVersion(ctx, seriesID)
	if err != nil {
		t.Fatalf("StatsByVersion (before verdict): %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("want 1 version, got %d", len(versions))
	}
	if versions[0].JudgeVerdict != "" {
		t.Errorf("JudgeVerdict before set = %q, want empty", versions[0].JudgeVerdict)
	}

	// Set verdict and re-query — third pass must populate the field.
	if err := pbStore.SetJudgeVerdict(ctx, pb.PlaybookID, "APPROVE", "claude-sonnet-4-6"); err != nil {
		t.Fatalf("SetJudgeVerdict: %v", err)
	}
	versions2, err := runStore.StatsByVersion(ctx, seriesID)
	if err != nil {
		t.Fatalf("StatsByVersion (after verdict): %v", err)
	}
	if len(versions2) != 1 {
		t.Fatalf("want 1 version, got %d", len(versions2))
	}
	if versions2[0].JudgeVerdict != "APPROVE" {
		t.Errorf("JudgeVerdict = %q, want APPROVE", versions2[0].JudgeVerdict)
	}
	if versions2[0].JudgeModel != "claude-sonnet-4-6" {
		t.Errorf("JudgeModel = %q, want claude-sonnet-4-6", versions2[0].JudgeModel)
	}
	if versions2[0].JudgeAt == "" {
		t.Error("JudgeAt should not be empty after verdict set")
	}
}

// TestPlaybookRunStore_Stats_EfficiencyMetrics verifies that augmentEfficiencyStats
// populates AvgStepCount and AvgRecoverySecs in Stats() and StatsBatch() when run
// steps and completed_at timestamps are present.
func TestPlaybookRunStore_ListBySeriesID(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	for _, r := range []*PlaybookRun{
		{PlaybookID: "pb_v1", SeriesID: "pbs_vacuum_triage", ExecutionMode: "agent", Outcome: "resolved", StartedAt: time.Now().UTC()},
		{PlaybookID: "pb_v2", SeriesID: "pbs_vacuum_triage", ExecutionMode: "agent", Outcome: "escalated", StartedAt: time.Now().UTC()},
		{PlaybookID: "pb_other", SeriesID: "pbs_other_triage", ExecutionMode: "agent", Outcome: "resolved", StartedAt: time.Now().UTC()},
	} {
		if err := s.Record(ctx, r); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	runs, err := s.ListBySeriesID(ctx, "pbs_vacuum_triage", 10)
	if err != nil {
		t.Fatalf("ListBySeriesID: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	for _, r := range runs {
		if r.SeriesID != "pbs_vacuum_triage" {
			t.Errorf("unexpected series_id %q", r.SeriesID)
		}
	}

	// Default limit applied for out-of-range value.
	all, err := s.ListBySeriesID(ctx, "pbs_vacuum_triage", 0)
	if err != nil {
		t.Fatalf("ListBySeriesID(limit=0): %v", err)
	}
	if len(all) != 2 {
		t.Errorf("got %d runs with limit=0, want 2", len(all))
	}

	// Unknown series returns empty slice.
	none, err := s.ListBySeriesID(ctx, "pbs_nonexistent", 10)
	if err != nil {
		t.Fatalf("ListBySeriesID(empty): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("got %d runs for unknown series, want 0", len(none))
	}
}

func TestPlaybookRunStore_ListByOutcome(t *testing.T) {
	s := newPlaybookRunStore(t)
	ctx := context.Background()

	for _, r := range []*PlaybookRun{
		{PlaybookID: "pb_a", SeriesID: "pbs_s1", ExecutionMode: "agent", Outcome: "resolved", StartedAt: time.Now().UTC()},
		{PlaybookID: "pb_b", SeriesID: "pbs_s1", ExecutionMode: "agent", Outcome: "resolved", StartedAt: time.Now().UTC()},
		{PlaybookID: "pb_c", SeriesID: "pbs_s2", ExecutionMode: "agent", Outcome: "escalated", StartedAt: time.Now().UTC()},
	} {
		if err := s.Record(ctx, r); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	resolved, err := s.ListByOutcome(ctx, "resolved", 10)
	if err != nil {
		t.Fatalf("ListByOutcome(resolved): %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("got %d resolved runs, want 2", len(resolved))
	}

	// Default limit applied for out-of-range value.
	all, err := s.ListByOutcome(ctx, "resolved", 0)
	if err != nil {
		t.Fatalf("ListByOutcome(limit=0): %v", err)
	}
	if len(all) != 2 {
		t.Errorf("got %d runs with limit=0, want 2", len(all))
	}

	// Unknown outcome returns empty slice.
	none, err := s.ListByOutcome(ctx, "unknown_outcome", 10)
	if err != nil {
		t.Fatalf("ListByOutcome(empty): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("got %d runs for unknown outcome, want 0", len(none))
	}
}

func TestPlaybookRunStore_Stats_EfficiencyMetrics(t *testing.T) {
	ctx := context.Background()
	raw, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { raw.Close() })

	runStore, err := NewPlaybookRunStore(raw.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookRunStore: %v", err)
	}
	stepStore, err := NewPlaybookRunStepStore(raw.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookRunStepStore: %v", err)
	}

	const seriesID = "pbs_eff_test"
	now := time.Now().UTC()

	runs := []struct {
		id          string
		outcome     string
		steps       int
		durationSec int
	}{
		{"plr_eff_a", "resolved", 3, 30},
		{"plr_eff_b", "resolved", 5, 50},
	}
	for _, r := range runs {
		if err := runStore.Record(ctx, &PlaybookRun{
			RunID:       r.id,
			PlaybookID:  "pb_eff",
			SeriesID:    seriesID,
			Outcome:     r.outcome,
			StartedAt:   now,
			CompletedAt: now.Add(time.Duration(r.durationSec) * time.Second),
		}); err != nil {
			t.Fatalf("Record %s: %v", r.id, err)
		}
		for i := 1; i <= r.steps; i++ {
			if err := stepStore.CreateStep(ctx, &PlaybookRunStep{
				RunID: r.id, StepIndex: i, Agent: "db", Tool: "run_query", Status: "succeeded",
			}); err != nil {
				t.Fatalf("CreateStep %s/%d: %v", r.id, i, err)
			}
		}
	}

	st, err := runStore.Stats(ctx, seriesID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	// avg steps: (3+5)/2 = 4.0
	if st.AvgStepCount < 3.99 || st.AvgStepCount > 4.01 {
		t.Errorf("AvgStepCount = %.2f, want 4.0", st.AvgStepCount)
	}
	// avg recovery: (30+50)/2 = 40s
	if st.AvgRecoverySecs < 39 || st.AvgRecoverySecs > 41 {
		t.Errorf("AvgRecoverySecs = %.1f, want 40.0", st.AvgRecoverySecs)
	}

	// StatsBatch should populate the same fields.
	batch, err := runStore.StatsBatch(ctx, []string{seriesID})
	if err != nil {
		t.Fatalf("StatsBatch: %v", err)
	}
	bst, ok := batch[seriesID]
	if !ok {
		t.Fatalf("seriesID missing from StatsBatch result")
	}
	if bst.AvgStepCount < 3.99 || bst.AvgStepCount > 4.01 {
		t.Errorf("StatsBatch AvgStepCount = %.2f, want 4.0", bst.AvgStepCount)
	}
	if bst.AvgRecoverySecs < 39 || bst.AvgRecoverySecs > 41 {
		t.Errorf("StatsBatch AvgRecoverySecs = %.1f, want 40.0", bst.AvgRecoverySecs)
	}
}
