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
		Outcome         string `json:"outcome"`
		EscalatedTo     string `json:"escalated_to,omitempty"`
		FindingsSummary string `json:"findings_summary,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Outcome == "" {
		http.Error(w, "outcome is required", http.StatusBadRequest)
		return
	}

	if err := s.store.Update(r.Context(), runID, body.Outcome, body.EscalatedTo, body.FindingsSummary); err != nil {
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats) //nolint:errcheck
}
