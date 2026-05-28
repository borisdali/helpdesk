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

type playbookRunStepServer struct {
	store *audit.PlaybookRunStepStore
}

// handleCreateStep handles POST /v1/fleet/playbook-runs/{runID}/steps.
func (s *playbookRunStepServer) handleCreateStep(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if runID == "" {
		http.Error(w, "runID is required", http.StatusBadRequest)
		return
	}

	var step audit.PlaybookRunStep
	if err := json.NewDecoder(r.Body).Decode(&step); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	step.RunID = runID

	if step.Tool == "" || step.Agent == "" {
		http.Error(w, "agent and tool are required", http.StatusBadRequest)
		return
	}

	// Auto-assign step_index if not provided.
	if step.StepIndex == 0 {
		next, err := s.store.NextStepIndex(r.Context(), runID)
		if err != nil {
			slog.Error("failed to get next step index", "run_id", runID, "err", err)
			http.Error(w, "failed to assign step index", http.StatusInternalServerError)
			return
		}
		step.StepIndex = next
	}

	if err := s.store.CreateStep(r.Context(), &step); err != nil {
		slog.Error("failed to create run step", "run_id", runID, "err", err)
		http.Error(w, "failed to create step", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(step) //nolint:errcheck
}

// handleUpdateStep handles PATCH /v1/fleet/playbook-runs/{runID}/steps/{stepIndex}.
func (s *playbookRunStepServer) handleUpdateStep(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	stepIndexStr := r.PathValue("stepIndex")
	if runID == "" || stepIndexStr == "" {
		http.Error(w, "runID and stepIndex are required", http.StatusBadRequest)
		return
	}
	stepIndex, err := strconv.Atoi(stepIndexStr)
	if err != nil {
		http.Error(w, "invalid stepIndex", http.StatusBadRequest)
		return
	}

	var body struct {
		Status     string `json:"status"`
		ApprovalID string `json:"approval_id,omitempty"`
		Result     string `json:"result,omitempty"`
		Error      string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Status == "" {
		http.Error(w, "status is required", http.StatusBadRequest)
		return
	}

	if err := s.store.UpdateStep(r.Context(), runID, stepIndex, body.Status, body.ApprovalID, body.Result, body.Error); err != nil {
		slog.Error("failed to update run step", "run_id", runID, "step_index", stepIndex, "err", err)
		http.Error(w, "failed to update step", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListSteps handles GET /v1/fleet/playbook-runs/{runID}/steps.
func (s *playbookRunStepServer) handleListSteps(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if runID == "" {
		http.Error(w, "runID is required", http.StatusBadRequest)
		return
	}

	steps, err := s.store.ListSteps(r.Context(), runID)
	if err != nil {
		slog.Error("failed to list run steps", "run_id", runID, "err", err)
		http.Error(w, "failed to list steps", http.StatusInternalServerError)
		return
	}
	if steps == nil {
		steps = []*audit.PlaybookRunStep{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"steps": steps, "count": len(steps)}) //nolint:errcheck
}

// handleGetPendingStep handles GET /v1/fleet/playbook-runs/{runID}/pending-step.
func (s *playbookRunStepServer) handleGetPendingStep(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if runID == "" {
		http.Error(w, "runID is required", http.StatusBadRequest)
		return
	}

	step, err := s.store.GetPendingStep(r.Context(), runID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Error("failed to get pending step", "run_id", runID, "err", err)
		http.Error(w, "failed to get pending step", http.StatusInternalServerError)
		return
	}
	if step == nil {
		http.Error(w, "no pending step", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(step) //nolint:errcheck
}
