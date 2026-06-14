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
	fbs, err := audit.NewRunFeedbackStore(store.DB(), false)
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
		"feedback_type":   "triage",
		"feedback_time":   "post_incident",
		"verdict_correct": true,
		"verdict_notes":   "PID 42 held ShareLock",
		"operator":        "alice",
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
	if got.VerdictCorrect == nil || !*got.VerdictCorrect {
		t.Errorf("VerdictCorrect = %v, want true", got.VerdictCorrect)
	}
	if got.VerdictNotes != "PID 42 held ShareLock" {
		t.Errorf("VerdictNotes = %q", got.VerdictNotes)
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
	fbBody := map[string]any{"feedback_type": "triage", "feedback_time": "post_incident", "verdict_correct": true, "operator": "test"}
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

func TestPlaybookRunHandlers_Feedback_QueryParams(t *testing.T) {
	srv := newPlaybookRunServerWithFeedback(t)

	doRunRequest(t, srv, "pb1", map[string]any{"run_id": "plr_qp01", "series_id": "pbs_lock_chain_triage"})

	submitFeedback := func(t *testing.T, runID, fbType, fbTime string, verdictCorrect bool, notes string) {
		t.Helper()
		body := map[string]any{
			"feedback_type":   fbType,
			"feedback_time":   fbTime,
			"verdict_correct": verdictCorrect,
			"verdict_notes":   notes,
			"operator":        "tester",
		}
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbook-runs/"+runID+"/feedback", bytes.NewReader(data))
		req.SetPathValue("runID", runID)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.handleSubmitFeedback(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("submit feedback (%s/%s): status %d, body: %s", fbType, fbTime, rec.Code, rec.Body.String())
		}
	}

	getFeedback := func(t *testing.T, runID, fbType, fbTime string) (audit.RunFeedback, int) {
		t.Helper()
		url := "/v1/fleet/playbook-runs/" + runID + "/feedback"
		if fbType != "" || fbTime != "" {
			url += "?feedback_type=" + fbType + "&feedback_time=" + fbTime
		}
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.SetPathValue("runID", runID)
		if fbType != "" {
			req.URL.RawQuery = "feedback_type=" + fbType + "&feedback_time=" + fbTime
		}
		rec := httptest.NewRecorder()
		srv.handleGetFeedback(rec, req)
		var fb audit.RunFeedback
		if rec.Code == http.StatusOK {
			json.NewDecoder(rec.Body).Decode(&fb) //nolint:errcheck
		}
		return fb, rec.Code
	}

	// Submit (triage, at_gate) and (triage, post_incident) for the same run.
	submitFeedback(t, "plr_qp01", "triage", "at_gate", true, "looked correct at gate")
	submitFeedback(t, "plr_qp01", "triage", "post_incident", false, "wrong after investigation")

	// Default (no params) → post_incident.
	fb, code := getFeedback(t, "plr_qp01", "", "")
	if code != http.StatusOK {
		t.Fatalf("default GET: status %d", code)
	}
	if fb.FeedbackTime != "post_incident" {
		t.Errorf("default GET: feedback_time = %q, want post_incident", fb.FeedbackTime)
	}
	if fb.VerdictCorrect == nil || *fb.VerdictCorrect != false {
		t.Errorf("default GET: verdict_correct = %v, want false", fb.VerdictCorrect)
	}

	// Explicit at_gate → returns at_gate row.
	fb2, code2 := getFeedback(t, "plr_qp01", "triage", "at_gate")
	if code2 != http.StatusOK {
		t.Fatalf("at_gate GET: status %d", code2)
	}
	if fb2.FeedbackTime != "at_gate" {
		t.Errorf("at_gate GET: feedback_time = %q, want at_gate", fb2.FeedbackTime)
	}
	if fb2.VerdictCorrect == nil || *fb2.VerdictCorrect != true {
		t.Errorf("at_gate GET: verdict_correct = %v, want true", fb2.VerdictCorrect)
	}
	if fb2.VerdictNotes != "looked correct at gate" {
		t.Errorf("at_gate GET: verdict_notes = %q", fb2.VerdictNotes)
	}

	// Non-existent combination → 404.
	_, code3 := getFeedback(t, "plr_qp01", "remediation", "post_incident")
	if code3 != http.StatusNotFound {
		t.Errorf("missing combination: status %d, want 404", code3)
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
	answered := map[string]any{"feedback_type": "triage", "feedback_time": "post_incident", "verdict_correct": true, "operator": "alice"}
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
	if items[0].VerdictCorrect != nil {
		t.Errorf("VerdictCorrect should be nil for pending record")
	}
}

func newPlaybookRunServerWithEvaluation(t *testing.T) *playbookRunServer {
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
	evs, err := audit.NewRunEvaluationStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewRunEvaluationStore: %v", err)
	}
	return &playbookRunServer{store: prs, playbookStore: pbs, evaluationStore: evs}
}

func TestPlaybookRunHandlers_Evaluation_SubmitAndGet(t *testing.T) {
	srv := newPlaybookRunServerWithEvaluation(t)

	body := map[string]any{
		"failure_id":      "db-tx-lock-chain-blocker",
		"failure_name":    "Transaction lock chain blocker",
		"keyword_score":   1.0,
		"tool_score":      0.8,
		"diagnosis_score": 0.9,
		"overall_score":   0.85,
		"judge_used":      true,
		"passed":          true,
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbook-runs/plr_ev01/evaluation", bytes.NewReader(data))
	req.SetPathValue("runID", "plr_ev01")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleSubmitEvaluation(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_ev01/evaluation", nil)
	req2.SetPathValue("runID", "plr_ev01")
	rec2 := httptest.NewRecorder()
	srv.handleGetEvaluation(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", rec2.Code, rec2.Body.String())
	}
	var got audit.RunEvaluation
	if err := json.NewDecoder(rec2.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID != "plr_ev01" {
		t.Errorf("RunID = %q, want plr_ev01", got.RunID)
	}
	if got.FailureID != "db-tx-lock-chain-blocker" {
		t.Errorf("FailureID = %q", got.FailureID)
	}
	if got.KeywordScore != 1.0 {
		t.Errorf("KeywordScore = %v, want 1.0", got.KeywordScore)
	}
	if got.OverallScore != 0.85 {
		t.Errorf("OverallScore = %v, want 0.85", got.OverallScore)
	}
	if !got.JudgeUsed {
		t.Error("JudgeUsed should be true")
	}
	if !got.Passed {
		t.Error("Passed should be true")
	}
}

func TestPlaybookRunHandlers_Evaluation_NotFound(t *testing.T) {
	srv := newPlaybookRunServerWithEvaluation(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_ghost/evaluation", nil)
	req.SetPathValue("runID", "plr_ghost")
	rec := httptest.NewRecorder()
	srv.handleGetEvaluation(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPlaybookRunHandlers_Evaluation_Upsert(t *testing.T) {
	srv := newPlaybookRunServerWithEvaluation(t)

	post := func(overall float64) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"failure_id": "db-oom", "overall_score": overall})
		req := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbook-runs/plr_up01/evaluation", bytes.NewReader(data))
		req.SetPathValue("runID", "plr_up01")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.handleSubmitEvaluation(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("POST status = %d; body: %s", rec.Code, rec.Body.String())
		}
	}
	post(0.5)
	post(0.9)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_up01/evaluation", nil)
	req.SetPathValue("runID", "plr_up01")
	rec := httptest.NewRecorder()
	srv.handleGetEvaluation(rec, req)

	var got audit.RunEvaluation
	json.NewDecoder(rec.Body).Decode(&got) //nolint:errcheck
	if got.OverallScore != 0.9 {
		t.Errorf("OverallScore after upsert = %v, want 0.9", got.OverallScore)
	}
}
