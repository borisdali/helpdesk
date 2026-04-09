package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newToolResultTestStore(t *testing.T) *ToolResultStore {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	trs, err := NewToolResultStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewToolResultStore: %v", err)
	}
	return trs
}

func TestToolResultStore_RecordAndList(t *testing.T) {
	trs := newToolResultTestStore(t)
	ctx := context.Background()

	r := &PersistedToolResult{
		ResultID:   "res_abcd1234",
		ServerName: "prod-db-1",
		ToolName:   "run_sql",
		ToolArgs:   `{"query":"SELECT 1"}`,
		Output:     "1 row",
		TraceID:    "trace_xyz",
		JobID:      "job_001",
		RecordedBy: "fleet-runner",
		RecordedAt: time.Now().UTC().Truncate(time.Second),
		Success:    true,
	}
	if err := trs.Record(ctx, r); err != nil {
		t.Fatalf("Record: %v", err)
	}

	results, err := trs.List(ctx, ToolResultQuery{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	got := results[0]
	if got.ResultID != r.ResultID {
		t.Errorf("ResultID = %q, want %q", got.ResultID, r.ResultID)
	}
	if got.ServerName != r.ServerName {
		t.Errorf("ServerName = %q, want %q", got.ServerName, r.ServerName)
	}
	if got.ToolName != r.ToolName {
		t.Errorf("ToolName = %q, want %q", got.ToolName, r.ToolName)
	}
	if got.ToolArgs != r.ToolArgs {
		t.Errorf("ToolArgs = %q, want %q", got.ToolArgs, r.ToolArgs)
	}
	if got.Output != r.Output {
		t.Errorf("Output = %q, want %q", got.Output, r.Output)
	}
	if got.TraceID != r.TraceID {
		t.Errorf("TraceID = %q, want %q", got.TraceID, r.TraceID)
	}
	if got.JobID != r.JobID {
		t.Errorf("JobID = %q, want %q", got.JobID, r.JobID)
	}
	if got.RecordedBy != r.RecordedBy {
		t.Errorf("RecordedBy = %q, want %q", got.RecordedBy, r.RecordedBy)
	}
	if got.Success != r.Success {
		t.Errorf("Success = %v, want %v", got.Success, r.Success)
	}
}

func TestToolResultStore_RecordGeneratesID(t *testing.T) {
	trs := newToolResultTestStore(t)
	ctx := context.Background()

	r := &PersistedToolResult{
		ServerName: "prod-db-1",
		ToolName:   "check_connection",
		Success:    true,
	}
	if err := trs.Record(ctx, r); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if r.ResultID == "" {
		t.Fatal("expected ResultID to be generated, got empty string")
	}
	if len(r.ResultID) < 4 || r.ResultID[:4] != "res_" {
		t.Errorf("ResultID = %q, want res_ prefix", r.ResultID)
	}
}

func TestToolResultStore_ListFilterByServer(t *testing.T) {
	trs := newToolResultTestStore(t)
	ctx := context.Background()

	if err := trs.Record(ctx, &PersistedToolResult{ServerName: "prod-db-1", ToolName: "run_sql", Success: true}); err != nil {
		t.Fatalf("Record prod-db-1: %v", err)
	}
	if err := trs.Record(ctx, &PersistedToolResult{ServerName: "prod-db-2", ToolName: "run_sql", Success: true}); err != nil {
		t.Fatalf("Record prod-db-2: %v", err)
	}

	results, err := trs.List(ctx, ToolResultQuery{ServerName: "prod-db-1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ServerName != "prod-db-1" {
		t.Errorf("ServerName = %q, want prod-db-1", results[0].ServerName)
	}
}

func TestToolResultStore_ListFilterByTool(t *testing.T) {
	trs := newToolResultTestStore(t)
	ctx := context.Background()

	if err := trs.Record(ctx, &PersistedToolResult{ServerName: "prod-db-1", ToolName: "run_sql", Success: true}); err != nil {
		t.Fatalf("Record run_sql: %v", err)
	}
	if err := trs.Record(ctx, &PersistedToolResult{ServerName: "prod-db-1", ToolName: "check_connection", Success: true}); err != nil {
		t.Fatalf("Record check_connection: %v", err)
	}

	results, err := trs.List(ctx, ToolResultQuery{ToolName: "check_connection"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ToolName != "check_connection" {
		t.Errorf("ToolName = %q, want check_connection", results[0].ToolName)
	}
}

func TestToolResultStore_ListFilterByJobID(t *testing.T) {
	trs := newToolResultTestStore(t)
	ctx := context.Background()

	if err := trs.Record(ctx, &PersistedToolResult{ServerName: "db-1", ToolName: "run_sql", JobID: "job_aaa", Success: true}); err != nil {
		t.Fatalf("Record job_aaa: %v", err)
	}
	if err := trs.Record(ctx, &PersistedToolResult{ServerName: "db-1", ToolName: "run_sql", JobID: "job_bbb", Success: true}); err != nil {
		t.Fatalf("Record job_bbb: %v", err)
	}

	results, err := trs.List(ctx, ToolResultQuery{JobID: "job_aaa"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].JobID != "job_aaa" {
		t.Errorf("JobID = %q, want job_aaa", results[0].JobID)
	}
}

func TestToolResultStore_ListFilterBySince(t *testing.T) {
	trs := newToolResultTestStore(t)
	ctx := context.Background()

	// Old result: 48 hours ago.
	old := &PersistedToolResult{
		ServerName: "db-1",
		ToolName:   "run_sql",
		RecordedAt: time.Now().UTC().Add(-48 * time.Hour),
		Success:    false,
	}
	if err := trs.Record(ctx, old); err != nil {
		t.Fatalf("Record old: %v", err)
	}

	// Recent result: just now.
	recent := &PersistedToolResult{
		ServerName: "db-1",
		ToolName:   "run_sql",
		RecordedAt: time.Now().UTC(),
		Success:    true,
	}
	if err := trs.Record(ctx, recent); err != nil {
		t.Fatalf("Record recent: %v", err)
	}

	results, err := trs.List(ctx, ToolResultQuery{Since: 24 * time.Hour})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with Since=24h, got %d", len(results))
	}
	if !results[0].Success {
		t.Errorf("expected the recent (Success=true) result, got Success=false")
	}
}

func TestToolResultStore_ListLimit(t *testing.T) {
	trs := newToolResultTestStore(t)
	ctx := context.Background()

	// Insert 5 results with distinct times so ordering is deterministic.
	base := time.Now().UTC().Add(-5 * time.Minute)
	for i := 0; i < 5; i++ {
		r := &PersistedToolResult{
			ServerName: "db-1",
			ToolName:   "run_sql",
			RecordedAt: base.Add(time.Duration(i) * time.Minute),
			Success:    true,
		}
		if err := trs.Record(ctx, r); err != nil {
			t.Fatalf("Record[%d]: %v", i, err)
		}
	}

	results, err := trs.List(ctx, ToolResultQuery{Limit: 3})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results with limit=3, got %d", len(results))
	}
	// Verify most-recent-first ordering: results[0] should be newer than results[1].
	if !results[0].RecordedAt.After(results[1].RecordedAt) {
		t.Errorf("expected results ordered most-recent-first: results[0].RecordedAt=%v, results[1].RecordedAt=%v",
			results[0].RecordedAt, results[1].RecordedAt)
	}
}

func TestToolResultStore_ListEmpty(t *testing.T) {
	trs := newToolResultTestStore(t)
	ctx := context.Background()

	results, err := trs.List(ctx, ToolResultQuery{})
	if err != nil {
		t.Fatalf("List on empty store returned error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty slice, got %d results", len(results))
	}
}
