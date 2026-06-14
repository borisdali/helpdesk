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
	store         *audit.PlaybookRunStore
	playbookStore *audit.PlaybookStore
	feedbackStore *audit.RunFeedbackStore
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
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats) //nolint:errcheck
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
	fb, err := s.feedbackStore.GetByRunID(r.Context(), runID)
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

// handleListPendingFeedback handles GET /v1/fleet/playbook-runs/feedback-pending.
// Returns post-incident triage RunFeedback placeholder records where verdict_correct
// has not been submitted yet — these are feedback requests awaiting operator resolution.
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
