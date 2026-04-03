package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

func newPlaybookRunServer(t *testing.T) *playbookRunServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	prs, err := audit.NewPlaybookRunStore(store.DB())
	if err != nil {
		t.Fatalf("NewPlaybookRunStore: %v", err)
	}
	pbs, err := audit.NewPlaybookStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookStore: %v", err)
	}
	return &playbookRunServer{store: prs, playbookStore: pbs}
}

func doRunRequest(t *testing.T, srv *playbookRunServer, playbookID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbooks/"+playbookID+"/runs", bytes.NewReader(data))
	req.SetPathValue("playbookID", playbookID)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleRecord(rec, req)
	return rec
}

func TestPlaybookRunHandlers_Record_OK(t *testing.T) {
	srv := newPlaybookRunServer(t)

	rec := doRunRequest(t, srv, "pb_abc123", map[string]any{
		"series_id":      "pbs_vacuum_triage",
		"execution_mode": "fleet",
		"operator":       "alice@example.com",
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var run audit.PlaybookRun
	if err := json.NewDecoder(rec.Body).Decode(&run); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if run.RunID == "" {
		t.Error("run_id should be set")
	}
	if run.PlaybookID != "pb_abc123" {
		t.Errorf("playbook_id = %q, want pb_abc123", run.PlaybookID)
	}
}

func TestPlaybookRunHandlers_Record_MissingSeriesID(t *testing.T) {
	srv := newPlaybookRunServer(t)

	// No series_id and no playbook in store to fall back to → 400.
	rec := doRunRequest(t, srv, "pb_missing", map[string]any{
		"execution_mode": "fleet",
	})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPlaybookRunHandlers_Record_MissingPlaybookID(t *testing.T) {
	srv := newPlaybookRunServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbooks//runs", bytes.NewReader([]byte("{}")))
	req.SetPathValue("playbookID", "")
	rec := httptest.NewRecorder()
	srv.handleRecord(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPlaybookRunHandlers_Update_OK(t *testing.T) {
	srv := newPlaybookRunServer(t)

	// Create a run first.
	createRec := doRunRequest(t, srv, "pb_abc", map[string]any{
		"series_id":      "pbs_db_restart_triage",
		"execution_mode": "agent",
	})
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d", createRec.Code)
	}
	var run audit.PlaybookRun
	json.NewDecoder(createRec.Body).Decode(&run) //nolint:errcheck

	// Update it.
	body, _ := json.Marshal(map[string]string{
		"outcome":          "escalated",
		"escalated_to":     "pbs_db_config_recovery",
		"findings_summary": "Logs show invalid parameter value.",
	})
	req := httptest.NewRequest(http.MethodPatch, "/v1/fleet/playbook-runs/"+run.RunID, bytes.NewReader(body))
	req.SetPathValue("runID", run.RunID)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleUpdate(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
}

func TestPlaybookRunHandlers_Update_MissingOutcome(t *testing.T) {
	srv := newPlaybookRunServer(t)

	body, _ := json.Marshal(map[string]string{"escalated_to": "pbs_x"})
	req := httptest.NewRequest(http.MethodPatch, "/v1/fleet/playbook-runs/plr_abc", bytes.NewReader(body))
	req.SetPathValue("runID", "plr_abc")
	rec := httptest.NewRecorder()
	srv.handleUpdate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPlaybookRunHandlers_List_OK(t *testing.T) {
	srv := newPlaybookRunServer(t)

	// Record two runs for the same playbook.
	for i := 0; i < 2; i++ {
		doRunRequest(t, srv, "pb_list_test", map[string]any{
			"series_id":      "pbs_vacuum_triage",
			"execution_mode": "fleet",
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks/pb_list_test/runs", nil)
	req.SetPathValue("playbookID", "pb_list_test")
	rec := httptest.NewRecorder()
	srv.handleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Runs  []audit.PlaybookRun `json:"runs"`
		Count int                 `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 2 {
		t.Errorf("count = %d, want 2", resp.Count)
	}
}

func TestPlaybookRunHandlers_List_Empty(t *testing.T) {
	srv := newPlaybookRunServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks/pb_none/runs", nil)
	req.SetPathValue("playbookID", "pb_none")
	rec := httptest.NewRecorder()
	srv.handleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Count int `json:"count"`
	}
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp.Count != 0 {
		t.Errorf("count = %d, want 0", resp.Count)
	}
}

func TestPlaybookRunHandlers_Stats_OK(t *testing.T) {
	srv := newPlaybookRunServer(t)

	// Seed a playbook so handleStats can resolve series_id.
	pb := &audit.Playbook{
		PlaybookID:    "pb_stats_test",
		SeriesID:      "pbs_stats_series",
		Name:          "Stats Test",
		ExecutionMode: "fleet",
		IsActive:      true,
		Source:        "manual",
		CreatedAt:     time.Now(),
	}
	if err := srv.playbookStore.Create(context.Background(), pb); err != nil {
		t.Fatalf("Create playbook: %v", err)
	}

	// Record runs with different outcomes.
	for _, outcome := range []string{"resolved", "escalated", "unknown"} {
		doRunRequest(t, srv, "pb_stats_test", map[string]any{
			"series_id":      "pbs_stats_series",
			"execution_mode": "fleet",
			"outcome":        outcome,
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks/pb_stats_test/stats", nil)
	req.SetPathValue("playbookID", "pb_stats_test")
	rec := httptest.NewRecorder()
	srv.handleStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var stats audit.PlaybookRunStats
	if err := json.NewDecoder(rec.Body).Decode(&stats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if stats.TotalRuns != 3 {
		t.Errorf("total_runs = %d, want 3", stats.TotalRuns)
	}
	if stats.Resolved != 1 {
		t.Errorf("resolved = %d, want 1", stats.Resolved)
	}
	if stats.Escalated != 1 {
		t.Errorf("escalated = %d, want 1", stats.Escalated)
	}
}

func TestPlaybookRunHandlers_Stats_NotFound(t *testing.T) {
	srv := newPlaybookRunServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks/pb_ghost/stats", nil)
	req.SetPathValue("playbookID", "pb_ghost")
	rec := httptest.NewRecorder()
	srv.handleStats(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
