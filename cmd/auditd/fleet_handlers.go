package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"helpdesk/internal/audit"
)

// fleetServer handles HTTP endpoints for fleet job management.
type fleetServer struct {
	store *audit.FleetStore
}

func (s *fleetServer) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var job audit.FleetJob
	if err := json.Unmarshal(body, &job); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if job.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if job.SubmittedBy == "" {
		http.Error(w, "submitted_by is required", http.StatusBadRequest)
		return
	}
	if job.JobDef == "" {
		http.Error(w, "job_def is required", http.StatusBadRequest)
		return
	}

	if err := s.store.CreateJob(r.Context(), &job); err != nil {
		slog.Error("failed to create fleet job", "err", err)
		http.Error(w, "failed to create fleet job", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(job) //nolint:errcheck
}

func (s *fleetServer) handleListJobs(w http.ResponseWriter, r *http.Request) {
	opts := audit.FleetJobQueryOptions{Limit: 50}
	q := r.URL.Query()
	if v := q.Get("status"); v != "" {
		opts.Status = v
	}
	if v := q.Get("submitted_by"); v != "" {
		opts.SubmittedBy = v
	}

	jobs, err := s.store.ListJobs(r.Context(), opts)
	if err != nil {
		slog.Error("failed to list fleet jobs", "err", err)
		http.Error(w, "failed to list fleet jobs", http.StatusInternalServerError)
		return
	}

	if jobs == nil {
		jobs = []*audit.FleetJob{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jobs) //nolint:errcheck
}

func (s *fleetServer) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	if jobID == "" {
		http.Error(w, "missing job ID", http.StatusBadRequest)
		return
	}

	job, err := s.store.GetJob(r.Context(), jobID)
	if err != nil {
		http.Error(w, "fleet job not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job) //nolint:errcheck
}

func (s *fleetServer) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	if jobID == "" {
		http.Error(w, "missing job ID", http.StatusBadRequest)
		return
	}

	var req struct {
		Status  string `json:"status"`
		Summary string `json:"summary,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Status == "" {
		http.Error(w, "status is required", http.StatusBadRequest)
		return
	}

	if err := s.store.UpdateJobStatus(r.Context(), jobID, req.Status, req.Summary); err != nil {
		slog.Error("failed to update fleet job status", "err", err, "job_id", jobID)
		http.Error(w, "failed to update fleet job status", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *fleetServer) handleAddServer(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	if jobID == "" {
		http.Error(w, "missing job ID", http.StatusBadRequest)
		return
	}

	var srv audit.FleetJobServer
	if err := json.NewDecoder(r.Body).Decode(&srv); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	srv.JobID = jobID
	if srv.ServerName == "" {
		http.Error(w, "server_name is required", http.StatusBadRequest)
		return
	}
	if srv.Stage == "" {
		http.Error(w, "stage is required", http.StatusBadRequest)
		return
	}
	if srv.Status == "" {
		srv.Status = "pending"
	}

	if err := s.store.AddServer(r.Context(), &srv); err != nil {
		slog.Error("failed to add fleet job server", "err", err, "job_id", jobID)
		http.Error(w, "failed to add fleet job server", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(srv) //nolint:errcheck
}

func (s *fleetServer) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	serverName := r.PathValue("serverName")
	if jobID == "" || serverName == "" {
		http.Error(w, "missing job ID or server name", http.StatusBadRequest)
		return
	}

	var req struct {
		Status     string `json:"status"`
		Output     string `json:"output,omitempty"`
		StartedAt  string `json:"started_at,omitempty"`
		FinishedAt string `json:"finished_at,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Status == "" {
		http.Error(w, "status is required", http.StatusBadRequest)
		return
	}

	var startedAt, finishedAt time.Time
	if req.StartedAt != "" {
		startedAt, _ = time.Parse(time.RFC3339Nano, req.StartedAt)
	}
	if req.FinishedAt != "" {
		finishedAt, _ = time.Parse(time.RFC3339Nano, req.FinishedAt)
	}

	if err := s.store.UpdateServer(r.Context(), jobID, serverName, req.Status, req.Output, startedAt, finishedAt); err != nil {
		slog.Error("failed to update fleet job server", "err", err, "job_id", jobID, "server", serverName)
		http.Error(w, "failed to update fleet job server", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *fleetServer) handleGetServers(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	if jobID == "" {
		http.Error(w, "missing job ID", http.StatusBadRequest)
		return
	}

	servers, err := s.store.GetJobServers(r.Context(), jobID)
	if err != nil {
		slog.Error("failed to get fleet job servers", "err", err, "job_id", jobID)
		http.Error(w, "failed to get fleet job servers", http.StatusInternalServerError)
		return
	}

	if servers == nil {
		servers = []*audit.FleetJobServer{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(servers) //nolint:errcheck
}
