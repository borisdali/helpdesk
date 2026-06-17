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

	prs, err := audit.NewPlaybookRunStore(store.DB(), false)
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

	prs, err := audit.NewPlaybookRunStore(store.DB(), false)
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

	// Retrieve all feedback (unfiltered) → {"feedback":[...]}.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_fb01/feedback", nil)
	req2.SetPathValue("runID", "plr_fb01")
	rec2 := httptest.NewRecorder()
	srv.handleGetFeedback(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("get status = %d, want 200", rec2.Code)
	}
	var listResp struct {
		Feedback []audit.RunFeedback `json:"feedback"`
	}
	if err := json.NewDecoder(rec2.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Feedback) != 1 {
		t.Fatalf("len(feedback) = %d, want 1", len(listResp.Feedback))
	}
	got := listResp.Feedback[0]
	if got.RunID != "plr_fb01" {
		t.Errorf("RunID = %q, want plr_fb01", got.RunID)
	}
	if got.VerdictCorrect == nil || !*got.VerdictCorrect {
		t.Errorf("VerdictCorrect = %v, want true", got.VerdictCorrect)
	}
	if got.VerdictNotes != "PID 42 held ShareLock" {
		t.Errorf("VerdictNotes = %q", got.VerdictNotes)
	}

	// Retrieve via filtered path → single record (original behaviour).
	req3 := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_fb01/feedback?feedback_type=triage&feedback_time=post_incident", nil)
	req3.SetPathValue("runID", "plr_fb01")
	req3.URL.RawQuery = "feedback_type=triage&feedback_time=post_incident"
	rec3 := httptest.NewRecorder()
	srv.handleGetFeedback(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("filtered get status = %d, want 200", rec3.Code)
	}
	var got2 audit.RunFeedback
	if err := json.NewDecoder(rec3.Body).Decode(&got2); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	if got2.RunID != "plr_fb01" {
		t.Errorf("filtered RunID = %q, want plr_fb01", got2.RunID)
	}
}

func TestPlaybookRunHandlers_Feedback_NotFound(t *testing.T) {
	srv := newPlaybookRunServerWithFeedback(t)

	// Unfiltered GET on a run with no records → 200 with empty list.
	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_ghost/feedback", nil)
	req.SetPathValue("runID", "plr_ghost")
	rec := httptest.NewRecorder()
	srv.handleGetFeedback(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("unfiltered status = %d, want 200", rec.Code)
	}
	var listResp struct {
		Feedback []audit.RunFeedback `json:"feedback"`
	}
	json.NewDecoder(rec.Body).Decode(&listResp) //nolint:errcheck
	if len(listResp.Feedback) != 0 {
		t.Errorf("unfiltered: len(feedback) = %d, want 0", len(listResp.Feedback))
	}

	// Filtered GET on a run with no matching record → 404.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_ghost/feedback?feedback_type=triage&feedback_time=post_incident", nil)
	req2.SetPathValue("runID", "plr_ghost")
	req2.URL.RawQuery = "feedback_type=triage&feedback_time=post_incident"
	rec2 := httptest.NewRecorder()
	srv.handleGetFeedback(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("filtered status = %d, want 404", rec2.Code)
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
	// Breakdown: the single run has post_incident feedback only.
	if stats.PostIncidentCount != 1 || stats.PostIncidentCorrect != 1 {
		t.Errorf("PostIncident = %d/%d, want 1/1", stats.PostIncidentCorrect, stats.PostIncidentCount)
	}
	if stats.PostIncidentAccuracyRate != 1.0 {
		t.Errorf("PostIncidentAccuracyRate = %f, want 1.0", stats.PostIncidentAccuracyRate)
	}
	if stats.AtGateCount != 0 {
		t.Errorf("AtGateCount = %d, want 0 (no at_gate feedback submitted)", stats.AtGateCount)
	}

	// Now submit at_gate feedback for the same run (simulating pre-remediation capture).
	fbBody2 := map[string]any{"feedback_type": "triage", "feedback_time": "at_gate", "verdict_correct": false, "operator": "test"}
	fbData2, _ := json.Marshal(fbBody2)
	fbReq2 := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbook-runs/plr_acc_run1/feedback", bytes.NewReader(fbData2))
	fbReq2.SetPathValue("runID", "plr_acc_run1")
	fbReq2.Header.Set("Content-Type", "application/json")
	fbRec2 := httptest.NewRecorder()
	srv.handleSubmitFeedback(fbRec2, fbReq2)
	if fbRec2.Code != http.StatusCreated {
		t.Fatalf("submit at_gate feedback: status %d, body: %s", fbRec2.Code, fbRec2.Body.String())
	}

	// Re-fetch stats — combined total is now 2, breakdown shows both types.
	statsReq2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks/"+createdPB.PlaybookID+"/stats", nil)
	statsReq2.SetPathValue("playbookID", createdPB.PlaybookID)
	statsRec2 := httptest.NewRecorder()
	srv.handleStats(statsRec2, statsReq2)
	if statsRec2.Code != http.StatusOK {
		t.Fatalf("second stats status = %d", statsRec2.Code)
	}
	var stats2 audit.PlaybookRunStats
	if err := json.NewDecoder(statsRec2.Body).Decode(&stats2); err != nil {
		t.Fatalf("decode second stats: %v", err)
	}
	if stats2.FeedbackCount != 2 {
		t.Errorf("FeedbackCount (after at_gate) = %d, want 2", stats2.FeedbackCount)
	}
	if stats2.AtGateCount != 1 || stats2.AtGateCorrect != 0 {
		t.Errorf("AtGate = %d/%d, want 0/1", stats2.AtGateCorrect, stats2.AtGateCount)
	}
	if stats2.AtGateAccuracyRate != 0.0 {
		t.Errorf("AtGateAccuracyRate = %f, want 0.0", stats2.AtGateAccuracyRate)
	}
	if stats2.PostIncidentCount != 1 || stats2.PostIncidentCorrect != 1 {
		t.Errorf("PostIncident (second stats) = %d/%d, want 1/1", stats2.PostIncidentCorrect, stats2.PostIncidentCount)
	}
}

func TestPlaybookRunHandlers_Stats_IncludesRemediationAccuracy(t *testing.T) {
	srv := newPlaybookRunServerWithFeedback(t)

	pbSrv := &playbookServer{store: srv.playbookStore, feedbackStore: srv.feedbackStore}
	pbData, _ := json.Marshal(map[string]any{"name": "Remed Accuracy Test", "description": "test", "series_id": "pbs_remed_accuracy_test"})
	pbReq := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbooks", bytes.NewReader(pbData))
	pbReq.Header.Set("Content-Type", "application/json")
	pbRec := httptest.NewRecorder()
	pbSrv.handleCreate(pbRec, pbReq)
	if pbRec.Code != http.StatusCreated {
		t.Fatalf("create playbook: %d %s", pbRec.Code, pbRec.Body.String())
	}
	var pb audit.Playbook
	json.NewDecoder(pbRec.Body).Decode(&pb) //nolint:errcheck

	doRunRequest(t, srv, pb.PlaybookID, map[string]any{"run_id": "plr_remed1", "series_id": pb.SeriesID})
	doRunRequest(t, srv, pb.PlaybookID, map[string]any{"run_id": "plr_remed2", "series_id": pb.SeriesID})

	submitFeedback := func(runID, fbType, fbTime string, correct bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"feedback_type": fbType, "feedback_time": fbTime,
			"verdict_correct": correct, "operator": "test",
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbook-runs/"+runID+"/feedback", bytes.NewReader(body))
		req.SetPathValue("runID", runID)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.handleSubmitFeedback(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("submit feedback: %d %s", rec.Code, rec.Body.String())
		}
	}

	// remediation/at_gate for run1 (correct), remediation/post_incident for run2 (incorrect).
	submitFeedback("plr_remed1", "remediation", "at_gate", true)
	submitFeedback("plr_remed2", "remediation", "post_incident", false)

	statsReq := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks/"+pb.PlaybookID+"/stats", nil)
	statsReq.SetPathValue("playbookID", pb.PlaybookID)
	statsRec := httptest.NewRecorder()
	srv.handleStats(statsRec, statsReq)
	if statsRec.Code != http.StatusOK {
		t.Fatalf("stats: %d %s", statsRec.Code, statsRec.Body.String())
	}
	var stats audit.PlaybookRunStats
	if err := json.NewDecoder(statsRec.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}

	if stats.RemediationFeedbackCount != 2 {
		t.Errorf("RemediationFeedbackCount = %d, want 2", stats.RemediationFeedbackCount)
	}
	if stats.RemediationCorrectCount != 1 {
		t.Errorf("RemediationCorrectCount = %d, want 1", stats.RemediationCorrectCount)
	}
	wantRate := 0.5
	if stats.RemediationAccuracyRate != wantRate {
		t.Errorf("RemediationAccuracyRate = %f, want %f", stats.RemediationAccuracyRate, wantRate)
	}
	if stats.RemediationAtGateCount != 1 || stats.RemediationAtGateCorrect != 1 {
		t.Errorf("RemediationAtGate = %d/%d, want 1/1", stats.RemediationAtGateCorrect, stats.RemediationAtGateCount)
	}
	if stats.RemediationPostIncidentCount != 1 || stats.RemediationPostIncidentCorrect != 0 {
		t.Errorf("RemediationPostIncident = %d/%d, want 0/1", stats.RemediationPostIncidentCorrect, stats.RemediationPostIncidentCount)
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

	// Default (no params) → all records as {"feedback":[...]}.
	{
		req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_qp01/feedback", nil)
		req.SetPathValue("runID", "plr_qp01")
		rec := httptest.NewRecorder()
		srv.handleGetFeedback(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("default GET: status %d", rec.Code)
		}
		var listResp struct {
			Feedback []audit.RunFeedback `json:"feedback"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&listResp); err != nil {
			t.Fatalf("default GET: decode list: %v", err)
		}
		if len(listResp.Feedback) != 2 {
			t.Errorf("default GET: len(feedback) = %d, want 2", len(listResp.Feedback))
		}
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

	prs, err := audit.NewPlaybookRunStore(store.DB(), false)
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

func TestPlaybookRunHandlers_Evaluation_RemediationJudgeFields(t *testing.T) {
	srv := newPlaybookRunServerWithEvaluation(t)

	body := map[string]any{
		"failure_id":                  "db-conn-limit",
		"overall_score":               0.75,
		"remediation_judge_score":     0.67,
		"remediation_judge_reasoning": "correct approach, one extra step",
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbook-runs/plr_rj01/evaluation", bytes.NewReader(data))
	req.SetPathValue("runID", "plr_rj01")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleSubmitEvaluation(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_rj01/evaluation", nil)
	req2.SetPathValue("runID", "plr_rj01")
	rec2 := httptest.NewRecorder()
	srv.handleGetEvaluation(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", rec2.Code, rec2.Body.String())
	}
	var got audit.RunEvaluation
	if err := json.NewDecoder(rec2.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RemediationJudgeScore != 0.67 {
		t.Errorf("RemediationJudgeScore = %v, want 0.67", got.RemediationJudgeScore)
	}
	if got.RemediationJudgeReasoning != "correct approach, one extra step" {
		t.Errorf("RemediationJudgeReasoning = %q", got.RemediationJudgeReasoning)
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

func TestPlaybookRunHandlers_VersionStats(t *testing.T) {
	ctx := context.Background()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	prs, err := audit.NewPlaybookRunStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookRunStore: %v", err)
	}
	pbs, err := audit.NewPlaybookStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookStore: %v", err)
	}
	// StatsByVersion JOINs playbook_run_steps and run_evaluation — create both tables.
	if _, err := audit.NewPlaybookRunStepStore(store.DB(), false); err != nil {
		t.Fatalf("NewPlaybookRunStepStore: %v", err)
	}
	if _, err := audit.NewRunEvaluationStore(store.DB(), false); err != nil {
		t.Fatalf("NewRunEvaluationStore: %v", err)
	}
	srv := &playbookRunServer{store: prs, playbookStore: pbs}

	const seriesID = "pbs_ver_handler_test"

	// Create two playbook versions.
	pb10 := &audit.Playbook{
		Name:          "Handler Test v1.0",
		SeriesID:      seriesID,
		Version:       "1.0",
		IsActive:      false,
		ExecutionMode: "agent",
		ProblemClass:  "test",
		Guidance:      "v1.0",
	}
	if err := pbs.Create(ctx, pb10); err != nil {
		t.Fatalf("Create v1.0: %v", err)
	}
	pb11 := &audit.Playbook{
		Name:          "Handler Test v1.1",
		SeriesID:      seriesID,
		Version:       "1.1",
		IsActive:      true,
		ExecutionMode: "agent",
		ProblemClass:  "test",
		Guidance:      "v1.1",
	}
	if err := pbs.Create(ctx, pb11); err != nil {
		t.Fatalf("Create v1.1: %v", err)
	}

	// Record one run per version.
	now := time.Now().UTC()
	if err := prs.Record(ctx, &audit.PlaybookRun{
		RunID: "plr_vh10", PlaybookID: pb10.PlaybookID, SeriesID: seriesID,
		Outcome: "resolved", StartedAt: now, CompletedAt: now.Add(30 * time.Second),
	}); err != nil {
		t.Fatalf("Record v1.0 run: %v", err)
	}
	if err := prs.Record(ctx, &audit.PlaybookRun{
		RunID: "plr_vh11", PlaybookID: pb11.PlaybookID, SeriesID: seriesID,
		Outcome: "resolved", StartedAt: now, CompletedAt: now.Add(10 * time.Second),
	}); err != nil {
		t.Fatalf("Record v1.1 run: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/series/"+seriesID+"/version-stats", nil)
	req.SetPathValue("seriesID", seriesID)
	rec := httptest.NewRecorder()
	srv.handleVersionStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		SeriesID string `json:"series_id"`
		Versions []struct {
			Version    string  `json:"version"`
			IsActive   bool    `json:"is_active"`
			TotalRuns  int     `json:"total_runs"`
			Resolved   int     `json:"resolved"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SeriesID != seriesID {
		t.Errorf("series_id = %q, want %q", resp.SeriesID, seriesID)
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("want 2 versions, got %d", len(resp.Versions))
	}
	if resp.Versions[0].Version != "1.0" {
		t.Errorf("versions[0].version = %q, want 1.0", resp.Versions[0].Version)
	}
	if resp.Versions[0].IsActive {
		t.Error("v1.0 should not be active")
	}
	if resp.Versions[1].Version != "1.1" {
		t.Errorf("versions[1].version = %q, want 1.1", resp.Versions[1].Version)
	}
	if !resp.Versions[1].IsActive {
		t.Error("v1.1 should be active")
	}
	if resp.Versions[1].TotalRuns != 1 || resp.Versions[1].Resolved != 1 {
		t.Errorf("v1.1: TotalRuns=%d Resolved=%d, want 1/1",
			resp.Versions[1].TotalRuns, resp.Versions[1].Resolved)
	}

	// Empty series → 200 with empty versions array.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/series/pbs_nonexistent/version-stats", nil)
	req2.SetPathValue("seriesID", "pbs_nonexistent")
	rec2 := httptest.NewRecorder()
	srv.handleVersionStats(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("empty series status = %d, want 200", rec2.Code)
	}
}

func TestPlaybookRunHandlers_Calibration(t *testing.T) {
	ctx := context.Background()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	evalStore, err := audit.NewRunEvaluationStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewRunEvaluationStore: %v", err)
	}
	fbStore, err := audit.NewRunFeedbackStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewRunFeedbackStore: %v", err)
	}
	srv := &playbookRunServer{evaluationStore: evalStore, feedbackStore: fbStore}

	// Seed 3 runs in 90-100% band: 3 correct triage feedbacks + remediation scores and feedback.
	tr := true
	fa := false
	for i, runID := range []string{"plr_cal01", "plr_cal02", "plr_cal03"} {
		_ = i
		if err := evalStore.Upsert(ctx, &audit.RunEvaluation{
			RunID: runID, FailureID: "db-lock", DiagnosisScore: 0.95, OverallScore: 0.95,
			RemediationScore: 0.92,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", runID, err)
		}
		if err := fbStore.Submit(ctx, &audit.RunFeedback{
			RunID: runID, SeriesID: "pbs_calib_handler", FeedbackType: "triage",
			FeedbackTime: "post_incident", VerdictCorrect: &tr,
		}); err != nil {
			t.Fatalf("Submit triage %s: %v", runID, err)
		}
	}
	// All 3 runs get remediation feedback: 1 correct, 2 wrong → 1/3 = 33% vs 95% expected → OVERCONFIDENT.
	for _, pair := range []struct {
		runID string
		ok    *bool
	}{{"plr_cal01", &tr}, {"plr_cal02", &fa}, {"plr_cal03", &fa}} {
		if err := fbStore.Submit(ctx, &audit.RunFeedback{
			RunID: pair.runID, SeriesID: "pbs_calib_handler", FeedbackType: "remediation",
			FeedbackTime: "post_incident", VerdictCorrect: pair.ok,
		}); err != nil {
			t.Fatalf("Submit remediation %s: %v", pair.runID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/calibration", nil)
	rec := httptest.NewRecorder()
	srv.handleCalibration(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	type band struct {
		Band        string `json:"band"`
		Runs        int    `json:"runs"`
		Correct     int    `json:"correct"`
		Calibration string `json:"calibration"`
	}
	var report struct {
		Bands            []band `json:"bands"`
		TotalRuns        int    `json:"total_runs"`
		RemediationBands []band `json:"remediation_bands"`
		RemediationRuns  int    `json:"remediation_runs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Diagnosis bands.
	if report.TotalRuns != 3 {
		t.Errorf("total_runs = %d, want 3", report.TotalRuns)
	}
	if len(report.Bands) != 3 {
		t.Fatalf("want 3 diagnosis bands, got %d", len(report.Bands))
	}
	if report.Bands[0].Band != "90-100%" {
		t.Errorf("Bands[0].Band = %q, want 90-100%%", report.Bands[0].Band)
	}
	if report.Bands[0].Runs != 3 || report.Bands[0].Correct != 3 {
		t.Errorf("90-100%%: Runs=%d Correct=%d, want 3/3", report.Bands[0].Runs, report.Bands[0].Correct)
	}
	// |1.0 - 0.95| = 0.05 ≤ 0.10 → WELL_CALIBRATED
	if report.Bands[0].Calibration != "WELL_CALIBRATED" {
		t.Errorf("90-100%% Calibration = %q, want WELL_CALIBRATED", report.Bands[0].Calibration)
	}

	// Remediation bands: all 3 runs have feedback (1 correct, 2 wrong).
	if report.RemediationRuns != 3 {
		t.Errorf("remediation_runs = %d, want 3", report.RemediationRuns)
	}
	if len(report.RemediationBands) != 3 {
		t.Fatalf("want 3 remediation bands, got %d", len(report.RemediationBands))
	}
	if report.RemediationBands[0].Band != "90-100%" {
		t.Errorf("RemediationBands[0].Band = %q, want 90-100%%", report.RemediationBands[0].Band)
	}
	if report.RemediationBands[0].Runs != 3 || report.RemediationBands[0].Correct != 1 {
		t.Errorf("remediation 90-100%%: Runs=%d Correct=%d, want 3/1", report.RemediationBands[0].Runs, report.RemediationBands[0].Correct)
	}
	// 1/3 = 33% actual vs 95% expected → OVERCONFIDENT
	if report.RemediationBands[0].Calibration != "OVERCONFIDENT" {
		t.Errorf("remediation 90-100%% Calibration = %q, want OVERCONFIDENT", report.RemediationBands[0].Calibration)
	}
}

func TestPlaybookRunHandlers_FaultRunHistory(t *testing.T) {
	ctx := context.Background()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	evalStore, err := audit.NewRunEvaluationStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewRunEvaluationStore: %v", err)
	}
	srv := &playbookRunServer{evaluationStore: evalStore}

	now := time.Now().UTC()
	for _, tc := range []struct {
		runID     string
		failureID string
		passed    bool
		ago       time.Duration
	}{
		{"plr_fh1", "db-lock-contention", true, 5 * 24 * time.Hour},
		{"plr_fh2", "db-lock-contention", false, 2 * 24 * time.Hour},
		{"plr_fh3", "k8s-pod-crashloop", true, 3 * 24 * time.Hour},
		// Outside 30-day window — must not appear.
		{"plr_fh4", "db-lock-contention", true, 60 * 24 * time.Hour},
		// Empty failure_id — must be excluded.
		{"plr_fh5", "", true, 1 * 24 * time.Hour},
	} {
		if err := evalStore.Upsert(ctx, &audit.RunEvaluation{
			RunID: tc.runID, FailureID: tc.failureID, Passed: tc.passed,
			CreatedAt: now.Add(-tc.ago),
		}); err != nil {
			t.Fatalf("Upsert %s: %v", tc.runID, err)
		}
	}

	// All faults, 30-day window.
	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-run-history?since_days=30", nil)
	rec := httptest.NewRecorder()
	srv.handleFaultRunHistory(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Entries []struct {
			RunID     string `json:"run_id"`
			FailureID string `json:"failure_id"`
			Passed    bool   `json:"passed"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Entries) != 3 {
		t.Errorf("entries = %d, want 3 (plr_fh1/fh2/fh3)", len(result.Entries))
	}

	// Filter by fault_id.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-run-history?since_days=30&fault_id=db-lock-contention", nil)
	rec2 := httptest.NewRecorder()
	srv.handleFaultRunHistory(rec2, req2.WithContext(ctx))
	if rec2.Code != http.StatusOK {
		t.Fatalf("filtered status = %d", rec2.Code)
	}
	var result2 struct {
		Entries []struct{ FailureID string `json:"failure_id"` } `json:"entries"`
	}
	if err := json.NewDecoder(rec2.Body).Decode(&result2); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	if len(result2.Entries) != 2 {
		t.Errorf("filtered entries = %d, want 2", len(result2.Entries))
	}
}
