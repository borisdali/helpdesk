package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// mockIncidentAuditd builds a mock auditd that serves the minimum set of
// endpoints needed by handleGetIncident. Each field controls the response for
// one sub-request; nil means "return 404 / empty".
type mockIncidentAuditd struct {
	triageRun   *audit.PlaybookRun
	feedbackRecs []audit.RunFeedback
	evaluation  *audit.RunEvaluation
}

func (m *mockIncidentAuditd) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")

		switch {
		// Triage run lookup: exact path, no trailing segments.
		case strings.HasSuffix(path, "/playbook-runs/"+m.triageRunID()) &&
			r.URL.Query().Get("prior_run_id") == "" &&
			!strings.Contains(path, "/feedback") &&
			!strings.Contains(path, "/evaluation") &&
			!strings.Contains(path, "/steps"):
			if m.triageRun == nil {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(m.triageRun) //nolint:errcheck

		// Remediation run lookup: prior_run_id query param.
		case strings.Contains(path, "/playbook-runs") && r.URL.Query().Get("prior_run_id") != "":
			json.NewEncoder(w).Encode(map[string]any{"runs": []any{}}) //nolint:errcheck

		// Gate event lookup.
		case strings.Contains(path, "/events"):
			json.NewEncoder(w).Encode([]any{}) //nolint:errcheck

		// Feedback — always return array envelope even when empty.
		case strings.HasSuffix(path, "/feedback"):
			recs := m.feedbackRecs
			if recs == nil {
				recs = []audit.RunFeedback{}
			}
			json.NewEncoder(w).Encode(map[string]any{"feedback": recs}) //nolint:errcheck

		// Evaluation.
		case strings.HasSuffix(path, "/evaluation"):
			if m.evaluation == nil {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(m.evaluation) //nolint:errcheck

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (m *mockIncidentAuditd) triageRunID() string {
	if m.triageRun != nil {
		return m.triageRun.RunID
	}
	return "plr_missing"
}

// getIncident sends GET /api/v1/incidents/{runID} through the gateway mux.
func getIncident(t *testing.T, gw *Gateway, runID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents/"+runID, nil)
	req.SetPathValue("runID", runID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestHandleGetIncident_BasicNarrative verifies that a simple triage-only run
// populates the top-level and Triage chapter fields correctly.
func TestHandleGetIncident_BasicNarrative(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:           "plr_narr01",
		SeriesID:        "pbs_db_lock",
		FindingsSummary: "Lock chain detected on pg_locks",
		Outcome:         audit.OutcomeResolved,
		Operator:        "alice",
		StartedAt:       time.Now().UTC(),
	}
	mock := &mockIncidentAuditd{triageRun: run}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_narr01")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if n.IncidentID != "plr_narr01" {
		t.Errorf("IncidentID = %q, want plr_narr01", n.IncidentID)
	}
	if n.Operator != "alice" {
		t.Errorf("Operator = %q, want alice", n.Operator)
	}
	if n.Triage.RunID != "plr_narr01" {
		t.Errorf("Triage.RunID = %q, want plr_narr01", n.Triage.RunID)
	}
	if n.Triage.Playbook != "pbs_db_lock" {
		t.Errorf("Triage.Playbook = %q, want pbs_db_lock", n.Triage.Playbook)
	}
	if n.Triage.Findings != "Lock chain detected on pg_locks" {
		t.Errorf("Triage.Findings = %q", n.Triage.Findings)
	}
	if n.Gate != nil {
		t.Errorf("Gate should be nil for non-gated run, got %+v", n.Gate)
	}
	if n.Remediation != nil {
		t.Errorf("Remediation should be nil, got %+v", n.Remediation)
	}
}

// TestHandleGetIncident_FeedbackSlice verifies that the handler returns multiple
// feedback records as a slice. This was the v0.18 fix: the old code tried to
// decode the {"feedback":[...]} envelope as a singular object, silently returning nil.
func TestHandleGetIncident_FeedbackSlice(t *testing.T) {
	tr := true
	fa := false
	run := &audit.PlaybookRun{
		RunID:     "plr_narr02",
		SeriesID:  "pbs_db_lock",
		Outcome:   audit.OutcomeResolved,
		StartedAt: time.Now().UTC(),
	}
	mock := &mockIncidentAuditd{
		triageRun: run,
		feedbackRecs: []audit.RunFeedback{
			{RunID: "plr_narr02", FeedbackType: "triage", FeedbackTime: "at_gate", VerdictCorrect: &tr},
			{RunID: "plr_narr02", FeedbackType: "triage", FeedbackTime: "post_incident", VerdictCorrect: &fa},
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_narr02")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}

	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(n.Feedback) != 2 {
		t.Fatalf("Feedback len = %d, want 2 (both triage/at_gate and triage/post_incident)", len(n.Feedback))
	}
	if n.Feedback[0].FeedbackTime != "at_gate" {
		t.Errorf("Feedback[0].FeedbackTime = %q, want at_gate", n.Feedback[0].FeedbackTime)
	}
	if n.Feedback[1].FeedbackTime != "post_incident" {
		t.Errorf("Feedback[1].FeedbackTime = %q, want post_incident", n.Feedback[1].FeedbackTime)
	}
}

// TestHandleGetIncident_EvaluationChapter verifies that the eval chapter (added in v0.18)
// is populated when auditd has a run_evaluation record, including primary_confidence.
func TestHandleGetIncident_EvaluationChapter(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:     "plr_narr03",
		SeriesID:  "pbs_db_lock",
		Outcome:   audit.OutcomeResolved,
		StartedAt: time.Now().UTC(),
	}
	mock := &mockIncidentAuditd{
		triageRun: run,
		evaluation: &audit.RunEvaluation{
			RunID:             "plr_narr03",
			FailureID:         "db-lock-contention",
			DiagnosisScore:    0.91,
			PrimaryConfidence: 0.88,
			JudgeUsed:         true,
		},
	}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_narr03")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}

	var n IncidentNarrative
	if err := json.NewDecoder(rec.Body).Decode(&n); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if n.Evaluation == nil {
		t.Fatal("Evaluation is nil, want non-nil evaluation chapter")
	}
	if n.Evaluation.DiagnosisScore != 0.91 {
		t.Errorf("Evaluation.DiagnosisScore = %v, want 0.91", n.Evaluation.DiagnosisScore)
	}
	if n.Evaluation.PrimaryConfidence != 0.88 {
		t.Errorf("Evaluation.PrimaryConfidence = %v, want 0.88", n.Evaluation.PrimaryConfidence)
	}
	if !n.Evaluation.JudgeUsed {
		t.Errorf("Evaluation.JudgeUsed = false, want true")
	}
}

// TestHandleGetIncident_NotFound verifies that a missing triage run returns 404.
func TestHandleGetIncident_NotFound(t *testing.T) {
	mock := &mockIncidentAuditd{triageRun: nil}
	auditSrv := mock.server(t)
	gw := &Gateway{auditURL: auditSrv.URL}

	rec := getIncident(t, gw, "plr_ghost")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
