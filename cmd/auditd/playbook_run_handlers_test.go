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

func newPlaybookRunServerWithFeedback(t *testing.T) *playbookRunServer {
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
	fbs, err := audit.NewRunFeedbackStore(store.DB())
	if err != nil {
		t.Fatalf("NewRunFeedbackStore: %v", err)
	}
	return &playbookRunServer{store: prs, playbookStore: pbs, feedbackStore: fbs}
}

func TestPlaybookRunHandlers_Feedback_SubmitAndGet(t *testing.T) {
	srv := newPlaybookRunServerWithFeedback(t)

	// First create a run so GetByRunID in handleSubmitFeedback can populate series_id.
	doRunRequest(t, srv, "pb1", map[string]any{
		"run_id":    "plr_fb01",
		"series_id": "pbs_lock_chain_triage",
	})

	// Submit feedback.
	body := map[string]any{
		"diagnosis_correct": true,
		"actual_root_cause": "PID 42 held ShareLock",
		"operator":          "alice",
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbook-runs/plr_fb01/feedback", bytes.NewReader(data))
	req.SetPathValue("runID", "plr_fb01")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleSubmitFeedback(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("submit status = %d, want 201", rec.Code)
	}

	// Retrieve feedback.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_fb01/feedback", nil)
	req2.SetPathValue("runID", "plr_fb01")
	rec2 := httptest.NewRecorder()
	srv.handleGetFeedback(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("get status = %d, want 200", rec2.Code)
	}
	var got audit.RunFeedback
	if err := json.NewDecoder(rec2.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID != "plr_fb01" {
		t.Errorf("RunID = %q, want plr_fb01", got.RunID)
	}
	if got.DiagnosisCorrect == nil || !*got.DiagnosisCorrect {
		t.Errorf("DiagnosisCorrect = %v, want true", got.DiagnosisCorrect)
	}
	if got.ActualRootCause != "PID 42 held ShareLock" {
		t.Errorf("ActualRootCause = %q", got.ActualRootCause)
	}
}

func TestPlaybookRunHandlers_Feedback_NotFound(t *testing.T) {
	srv := newPlaybookRunServerWithFeedback(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_ghost/feedback", nil)
	req.SetPathValue("runID", "plr_ghost")
	rec := httptest.NewRecorder()
	srv.handleGetFeedback(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPlaybookRunHandlers_Stats_IncludesAccuracy(t *testing.T) {
	srv := newPlaybookRunServerWithFeedback(t)

	// Create playbook via the handler (handler assigns IDs).
	pbSrv := &playbookServer{store: srv.playbookStore, feedbackStore: srv.feedbackStore}
	pbData, _ := json.Marshal(map[string]any{"name": "Accuracy Test", "description": "accuracy test playbook", "series_id": "pbs_accuracy_test"})
	pbReq := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbooks", bytes.NewReader(pbData))
	pbReq.Header.Set("Content-Type", "application/json")
	pbRec := httptest.NewRecorder()
	pbSrv.handleCreate(pbRec, pbReq)
	if pbRec.Code != http.StatusCreated {
		t.Fatalf("create playbook: status %d, body: %s", pbRec.Code, pbRec.Body.String())
	}
	var createdPB audit.Playbook
	if err := json.NewDecoder(pbRec.Body).Decode(&createdPB); err != nil {
		t.Fatalf("decode playbook: %v", err)
	}

	// Record a run for the series.
	doRunRequest(t, srv, createdPB.PlaybookID, map[string]any{
		"run_id":    "plr_acc_run1",
		"series_id": createdPB.SeriesID,
	})

	// Submit feedback for that run.
	fbBody := map[string]any{"diagnosis_correct": true, "operator": "test"}
	fbData, _ := json.Marshal(fbBody)
	fbReq := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbook-runs/plr_acc_run1/feedback", bytes.NewReader(fbData))
	fbReq.SetPathValue("runID", "plr_acc_run1")
	fbReq.Header.Set("Content-Type", "application/json")
	fbRec := httptest.NewRecorder()
	srv.handleSubmitFeedback(fbRec, fbReq)
	if fbRec.Code != http.StatusCreated {
		t.Fatalf("submit feedback: status %d, body: %s", fbRec.Code, fbRec.Body.String())
	}

	// Fetch stats — should include accuracy.
	statsReq := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks/"+createdPB.PlaybookID+"/stats", nil)
	statsReq.SetPathValue("playbookID", createdPB.PlaybookID)
	statsRec := httptest.NewRecorder()
	srv.handleStats(statsRec, statsReq)

	if statsRec.Code != http.StatusOK {
		t.Fatalf("stats status = %d, body: %s", statsRec.Code, statsRec.Body.String())
	}
	var stats audit.PlaybookRunStats
	if err := json.NewDecoder(statsRec.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.FeedbackCount != 1 {
		t.Errorf("FeedbackCount = %d, want 1", stats.FeedbackCount)
	}
	if stats.CorrectCount != 1 {
		t.Errorf("CorrectCount = %d, want 1", stats.CorrectCount)
	}
	if stats.AccuracyRate != 1.0 {
		t.Errorf("AccuracyRate = %f, want 1.0", stats.AccuracyRate)
	}
}

func TestPlaybookRunHandlers_ListPendingFeedback(t *testing.T) {
	srv := newPlaybookRunServerWithFeedback(t)

	// Create two runs so series_id can be populated on submit.
	doRunRequest(t, srv, "pb1", map[string]any{"run_id": "plr_pf01", "series_id": "pbs_triage"})
	doRunRequest(t, srv, "pb1", map[string]any{"run_id": "plr_pf02", "series_id": "pbs_triage"})

	// Submit a placeholder (no diagnosis_correct) for plr_pf01.
	placeholder := map[string]any{"operator": "faulttest"}
	d, _ := json.Marshal(placeholder)
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbook-runs/plr_pf01/feedback", bytes.NewReader(d))
	req.SetPathValue("runID", "plr_pf01")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleSubmitFeedback(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit placeholder: status %d, body: %s", rec.Code, rec.Body.String())
	}

	// Submit answered feedback for plr_pf02 — should NOT appear in pending list.
	answered := map[string]any{"diagnosis_correct": true, "operator": "alice"}
	d2, _ := json.Marshal(answered)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbook-runs/plr_pf02/feedback", bytes.NewReader(d2))
	req2.SetPathValue("runID", "plr_pf02")
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	srv.handleSubmitFeedback(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("submit answered: status %d", rec2.Code)
	}

	// List pending — should return only plr_pf01.
	req3 := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/feedback-pending", nil)
	rec3 := httptest.NewRecorder()
	srv.handleListPendingFeedback(rec3, req3)

	if rec3.Code != http.StatusOK {
		t.Fatalf("list pending status = %d, body: %s", rec3.Code, rec3.Body.String())
	}
	var items []audit.RunFeedback
	if err := json.NewDecoder(rec3.Body).Decode(&items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].RunID != "plr_pf01" {
		t.Errorf("RunID = %q, want plr_pf01", items[0].RunID)
	}
	if items[0].DiagnosisCorrect != nil {
		t.Errorf("DiagnosisCorrect should be nil for pending record")
	}
}
