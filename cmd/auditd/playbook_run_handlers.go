package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"helpdesk/internal/audit"
)

type playbookRunServer struct {
	store           *audit.PlaybookRunStore
	playbookStore   *audit.PlaybookStore
	feedbackStore   *audit.RunFeedbackStore
	evaluationStore *audit.RunEvaluationStore
}

// handleRecord handles POST /v1/fleet/playbooks/{playbookID}/runs.
// Records the start of a playbook execution and returns the run_id.
func (s *playbookRunServer) handleRecord(w http.ResponseWriter, r *http.Request) {
	playbookID := r.PathValue("playbookID")
	if playbookID == "" {
		http.Error(w, "playbookID is required", http.StatusBadRequest)
		return
	}

	var run audit.PlaybookRun
	if err := json.NewDecoder(r.Body).Decode(&run); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	run.PlaybookID = playbookID
	if run.SeriesID == "" || run.ExecutionMode == "" {
		// Fetch from playbook store to fill missing fields.
		pb, err := s.playbookStore.Get(r.Context(), playbookID)
		if err == nil {
			if run.SeriesID == "" {
				run.SeriesID = pb.SeriesID
			}
			if run.ExecutionMode == "" {
				run.ExecutionMode = pb.ExecutionMode
			}
		}
	}
	if run.SeriesID == "" {
		http.Error(w, "series_id is required", http.StatusBadRequest)
		return
	}

	if err := s.store.Record(r.Context(), &run); err != nil {
		slog.Error("failed to record playbook run", "playbook_id", playbookID, "err", err)
		http.Error(w, "failed to record run", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(run) //nolint:errcheck
}

// handleUpdate handles PATCH /v1/fleet/playbook-runs/{runID}.
// Updates outcome, escalated_to, and findings_summary when a run concludes.
func (s *playbookRunServer) handleUpdate(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if runID == "" {
		http.Error(w, "runID is required", http.StatusBadRequest)
		return
	}

	var body struct {
		Outcome          string                  `json:"outcome"`
		EscalatedTo      string                  `json:"escalated_to,omitempty"`
		TransitionedTo   string                  `json:"transitioned_to,omitempty"`
		FindingsSummary  string                  `json:"findings_summary,omitempty"`
		AgentTranscript  string                  `json:"agent_transcript,omitempty"`
		TraceID          string                  `json:"trace_id,omitempty"`
		DiagnosticReport *audit.DiagnosticReport `json:"diagnostic_report,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Outcome == "" {
		http.Error(w, "outcome is required", http.StatusBadRequest)
		return
	}

	if err := s.store.Update(r.Context(), runID, body.Outcome, body.EscalatedTo, body.TransitionedTo, body.FindingsSummary, body.AgentTranscript, body.TraceID, body.DiagnosticReport); err != nil {
		slog.Error("failed to update playbook run", "run_id", runID, "err", err)
		http.Error(w, "failed to update run", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleList handles GET /v1/fleet/playbooks/{playbookID}/runs.
func (s *playbookRunServer) handleList(w http.ResponseWriter, r *http.Request) {
	playbookID := r.PathValue("playbookID")
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}

	runs, err := s.store.ListByPlaybook(r.Context(), playbookID, limit)
	if err != nil {
		slog.Error("failed to list playbook runs", "playbook_id", playbookID, "err", err)
		http.Error(w, "failed to list runs", http.StatusInternalServerError)
		return
	}
	if runs == nil {
		runs = []*audit.PlaybookRun{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"runs": runs, "count": len(runs)}) //nolint:errcheck
}

// handleGetRun handles GET /v1/fleet/playbook-runs/{runID}.
func (s *playbookRunServer) handleGetRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if runID == "" {
		http.Error(w, "runID is required", http.StatusBadRequest)
		return
	}
	run, err := s.store.GetByRunID(r.Context(), runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		slog.Error("failed to get playbook run", "run_id", runID, "err", err)
		http.Error(w, "failed to get run", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(run) //nolint:errcheck
}

// handleStats handles GET /v1/fleet/playbooks/{playbookID}/stats.
// Returns aggregated stats for the playbook's series.
// handleListByOutcome handles GET /v1/fleet/playbook-runs.
// Supports ?outcome=<outcome>, ?prior_run_id=<id>, and ?limit=<n>.
func (s *playbookRunServer) handleListByOutcome(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}

	var runs []*audit.PlaybookRun
	var err error

	q := r.URL.Query()
	switch {
	case q.Get("series_id") != "":
		runs, err = s.store.ListBySeriesID(r.Context(), q.Get("series_id"), limit)
		if err != nil {
			slog.Error("failed to list playbook runs by series_id", "series_id", q.Get("series_id"), "err", err)
			http.Error(w, "failed to list runs", http.StatusInternalServerError)
			return
		}
	case q.Get("prior_run_id") != "":
		runs, err = s.store.ListByPriorRunID(r.Context(), q.Get("prior_run_id"), limit)
		if err != nil {
			slog.Error("failed to list playbook runs by prior_run_id", "prior_run_id", q.Get("prior_run_id"), "err", err)
			http.Error(w, "failed to list runs", http.StatusInternalServerError)
			return
		}
	default:
		outcome := q.Get("outcome")
		if outcome == "" {
			http.Error(w, "series_id, prior_run_id, or outcome query parameter is required", http.StatusBadRequest)
			return
		}
		runs, err = s.store.ListByOutcome(r.Context(), outcome, limit)
		if err != nil {
			slog.Error("failed to list playbook runs by outcome", "outcome", outcome, "err", err)
			http.Error(w, "failed to list runs", http.StatusInternalServerError)
			return
		}
	}

	if runs == nil {
		runs = []*audit.PlaybookRun{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"runs": runs, "count": len(runs)}) //nolint:errcheck
}

func (s *playbookRunServer) handleStats(w http.ResponseWriter, r *http.Request) {
	playbookID := r.PathValue("playbookID")

	pb, err := s.playbookStore.Get(r.Context(), playbookID)
	if err != nil {
		http.Error(w, "playbook not found", http.StatusNotFound)
		return
	}

	stats, err := s.store.Stats(r.Context(), pb.SeriesID)
	if err != nil {
		slog.Error("failed to get playbook stats", "playbook_id", playbookID, "err", err)
		http.Error(w, "failed to get stats", http.StatusInternalServerError)
		return
	}

	// Merge accuracy data from feedback store when available.
	if s.feedbackStore != nil {
		if fbStats, err := s.feedbackStore.StatsBySeries(r.Context(), pb.SeriesID); err == nil {
			stats.FeedbackCount = fbStats.FeedbackCount
			stats.CorrectCount = fbStats.CorrectCount
			stats.AccuracyRate = fbStats.AccuracyRate
			stats.AtGateCount = fbStats.AtGateCount
			stats.AtGateCorrect = fbStats.AtGateCorrect
			stats.AtGateAccuracyRate = fbStats.AtGateAccuracyRate
			stats.PostIncidentCount = fbStats.PostIncidentCount
			stats.PostIncidentCorrect = fbStats.PostIncidentCorrect
			stats.PostIncidentAccuracyRate = fbStats.PostIncidentAccuracyRate
			stats.RemediationFeedbackCount = fbStats.RemediationFeedbackCount
			stats.RemediationCorrectCount = fbStats.RemediationCorrectCount
			stats.RemediationAccuracyRate = fbStats.RemediationAccuracyRate
			stats.RemediationAtGateCount = fbStats.RemediationAtGateCount
			stats.RemediationAtGateCorrect = fbStats.RemediationAtGateCorrect
			stats.RemediationPostIncidentCount = fbStats.RemediationPostIncidentCount
			stats.RemediationPostIncidentCorrect = fbStats.RemediationPostIncidentCorrect
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats) //nolint:errcheck
}

// handleVersionStats handles GET /v1/fleet/series/{seriesID}/version-stats.
// Returns per-version run counts, resolution rates, step counts, recovery times,
// and average evaluation scores for a playbook series.
func (s *playbookRunServer) handleVersionStats(w http.ResponseWriter, r *http.Request) {
	seriesID := r.PathValue("seriesID")
	if seriesID == "" {
		http.Error(w, "seriesID path parameter is required", http.StatusBadRequest)
		return
	}

	versions, err := s.store.StatsByVersion(r.Context(), seriesID)
	if err != nil {
		slog.Error("failed to get version stats", "series_id", seriesID, "err", err)
		http.Error(w, "failed to get version stats", http.StatusInternalServerError)
		return
	}

	if versions == nil {
		versions = []*audit.PlaybookVersionStats{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"series_id": seriesID, "versions": versions}) //nolint:errcheck
}

// handleSubmitFeedback handles POST /v1/fleet/playbook-runs/{runID}/feedback.
func (s *playbookRunServer) handleSubmitFeedback(w http.ResponseWriter, r *http.Request) {
	if s.feedbackStore == nil {
		http.Error(w, "feedback store not configured", http.StatusServiceUnavailable)
		return
	}
	runID := r.PathValue("runID")
	if runID == "" {
		http.Error(w, "runID is required", http.StatusBadRequest)
		return
	}
	var fb audit.RunFeedback
	if err := json.NewDecoder(r.Body).Decode(&fb); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	fb.RunID = runID
	if operator := r.Header.Get("X-User"); operator != "" && fb.Operator == "" {
		fb.Operator = operator
	}
	// Populate series_id from the run if not provided in the body.
	if fb.SeriesID == "" {
		if run, err := s.store.GetByRunID(r.Context(), runID); err == nil {
			fb.SeriesID = run.SeriesID
		}
	}
	if err := s.feedbackStore.Submit(r.Context(), &fb); err != nil {
		slog.Error("failed to submit run feedback", "run_id", runID, "err", err)
		http.Error(w, "failed to submit feedback", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(fb) //nolint:errcheck
}

// handleGetFeedback handles GET /v1/fleet/playbook-runs/{runID}/feedback.
// Optional query params ?feedback_type=<type>&feedback_time=<time> select a
// specific row; defaults to feedback_type=triage, feedback_time=post_incident.
func (s *playbookRunServer) handleGetFeedback(w http.ResponseWriter, r *http.Request) {
	if s.feedbackStore == nil {
		http.Error(w, "feedback store not configured", http.StatusServiceUnavailable)
		return
	}
	runID := r.PathValue("runID")
	if runID == "" {
		http.Error(w, "runID is required", http.StatusBadRequest)
		return
	}
	feedbackType := r.URL.Query().Get("feedback_type")
	if feedbackType == "" {
		feedbackType = "triage"
	}
	feedbackTime := r.URL.Query().Get("feedback_time")
	if feedbackTime == "" {
		feedbackTime = "post_incident"
	}
	fb, err := s.feedbackStore.GetByRunIDAndType(r.Context(), runID, feedbackType, feedbackTime)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no feedback for run", http.StatusNotFound)
			return
		}
		slog.Error("failed to get run feedback", "run_id", runID, "err", err)
		http.Error(w, "failed to get feedback", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fb) //nolint:errcheck
}

// handleSubmitEvaluation handles POST /v1/fleet/playbook-runs/{runID}/evaluation.
// Faulttest calls this after each fault to record automated scoring metrics against
// the triage playbook run_id, making them available for calibration alongside feedback.
func (s *playbookRunServer) handleSubmitEvaluation(w http.ResponseWriter, r *http.Request) {
	if s.evaluationStore == nil {
		http.Error(w, "evaluation store not configured", http.StatusServiceUnavailable)
		return
	}
	runID := r.PathValue("runID")
	if runID == "" {
		http.Error(w, "runID is required", http.StatusBadRequest)
		return
	}
	var eval audit.RunEvaluation
	if err := json.NewDecoder(r.Body).Decode(&eval); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	eval.RunID = runID
	if err := s.evaluationStore.Upsert(r.Context(), &eval); err != nil {
		slog.Error("failed to submit evaluation", "run_id", runID, "err", err)
		http.Error(w, "failed to submit evaluation", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetEvaluation handles GET /v1/fleet/playbook-runs/{runID}/evaluation.
func (s *playbookRunServer) handleGetEvaluation(w http.ResponseWriter, r *http.Request) {
	if s.evaluationStore == nil {
		http.Error(w, "evaluation store not configured", http.StatusServiceUnavailable)
		return
	}
	runID := r.PathValue("runID")
	if runID == "" {
		http.Error(w, "runID is required", http.StatusBadRequest)
		return
	}
	eval, err := s.evaluationStore.GetByRunID(r.Context(), runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no evaluation for run", http.StatusNotFound)
			return
		}
		slog.Error("failed to get evaluation", "run_id", runID, "err", err)
		http.Error(w, "failed to get evaluation", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(eval) //nolint:errcheck
}

// handleListPendingFeedback handles GET /v1/fleet/playbook-runs/feedback-pending.
// Returns post-incident triage RunFeedback placeholder records where verdict_correct
// has not been submitted yet — these are feedback requests awaiting operator resolution.
// handleCalibration handles GET /v1/fleet/calibration.
// Optional query param series_id scopes to a single playbook series; omit for fleet-wide.
func (s *playbookRunServer) handleCalibration(w http.ResponseWriter, r *http.Request) {
	if s.evaluationStore == nil {
		http.Error(w, "evaluation store not configured", http.StatusServiceUnavailable)
		return
	}
	seriesID := r.URL.Query().Get("series_id")
	report, err := s.evaluationStore.CalibrationBands(r.Context(), seriesID)
	if err != nil {
		slog.Error("failed to compute calibration", "series_id", seriesID, "err", err)
		http.Error(w, "failed to compute calibration", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report) //nolint:errcheck
}

// handleFaultRunHistory handles GET /v1/fleet/fault-run-history.
// Returns lightweight pass/fail history per fault, used by `vault drift --gateway`.
// Query params:
//
//	since_days int    (default 90) — how far back to look
//	fault_id   string (optional)   — filter to a single fault
func (s *playbookRunServer) handleFaultRunHistory(w http.ResponseWriter, r *http.Request) {
	if s.evaluationStore == nil {
		http.Error(w, "evaluation store not configured", http.StatusServiceUnavailable)
		return
	}
	sinceDays := 90
	if v := r.URL.Query().Get("since_days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			sinceDays = n
		}
	}
	faultID := r.URL.Query().Get("fault_id")
	entries, err := s.evaluationStore.ListHistory(r.Context(), sinceDays, faultID)
	if err != nil {
		slog.Error("failed to list fault run history", "err", err)
		http.Error(w, "failed to list fault run history", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []*audit.FaultRunEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"entries": entries}) //nolint:errcheck
}

func (s *playbookRunServer) handleListPendingFeedback(w http.ResponseWriter, r *http.Request) {
	if s.feedbackStore == nil {
		http.Error(w, "feedback store not configured", http.StatusServiceUnavailable)
		return
	}
	items, err := s.feedbackStore.ListPending(r.Context())
	if err != nil {
		slog.Error("failed to list pending feedback", "err", err)
		http.Error(w, "failed to list pending feedback", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []*audit.RunFeedback{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items) //nolint:errcheck
}
