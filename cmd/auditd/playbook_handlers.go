package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"helpdesk/internal/audit"
)

// playbookServer handles HTTP endpoints for fleet playbook management.
type playbookServer struct {
	store    *audit.PlaybookStore
	runStore *audit.PlaybookRunStore
}

func (s *playbookServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var pb audit.Playbook
	if err := json.Unmarshal(body, &pb); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if pb.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if pb.Description == "" {
		http.Error(w, "description is required", http.StatusBadRequest)
		return
	}
	if err := s.store.Create(r.Context(), &pb); err != nil {
		slog.Error("failed to create playbook", "err", err)
		http.Error(w, "failed to create playbook", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(pb) //nolint:errcheck
}

func (s *playbookServer) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	if id == "" {
		http.Error(w, "missing playbook ID", http.StatusBadRequest)
		return
	}
	pb, err := s.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "playbook not found", http.StatusNotFound)
			return
		}
		slog.Error("failed to get playbook", "err", err)
		http.Error(w, "failed to get playbook", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pb) //nolint:errcheck
}

func (s *playbookServer) handleList(w http.ResponseWriter, r *http.Request) {
	q := audit.DefaultPlaybookListQuery()
	if v := r.URL.Query().Get("active_only"); v == "false" {
		q.ActiveOnly = false
	} else if v == "true" {
		q.ActiveOnly = true
	}
	if r.URL.Query().Get("include_system") == "false" {
		q.IncludeSystem = false
	}
	if v := r.URL.Query().Get("series_id"); v != "" {
		q.SeriesID = v
	}
	if v := r.URL.Query().Get("source"); v != "" {
		q.Source = v
	}

	playbooks, err := s.store.List(r.Context(), q)
	if err != nil {
		slog.Error("failed to list playbooks", "err", err)
		http.Error(w, "failed to list playbooks", http.StatusInternalServerError)
		return
	}
	if playbooks == nil {
		playbooks = []*audit.Playbook{}
	}

	// Inject run stats inline so callers don't need a second request per playbook.
	if s.runStore != nil && len(playbooks) > 0 {
		seriesIDs := make([]string, 0, len(playbooks))
		for _, pb := range playbooks {
			if pb.SeriesID != "" {
				seriesIDs = append(seriesIDs, pb.SeriesID)
			}
		}
		if statsMap, err := s.runStore.StatsBatch(r.Context(), seriesIDs); err == nil {
			for _, pb := range playbooks {
				if st, ok := statsMap[pb.SeriesID]; ok {
					pb.Stats = st
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"playbooks": playbooks}) //nolint:errcheck
}

func (s *playbookServer) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	if id == "" {
		http.Error(w, "missing playbook ID", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var pb audit.Playbook
	if err := json.Unmarshal(body, &pb); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if pb.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if pb.Description == "" {
		http.Error(w, "description is required", http.StatusBadRequest)
		return
	}
	pb.PlaybookID = id
	if err := s.store.Update(r.Context(), &pb); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "playbook not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, audit.ErrSystemPlaybook) {
			http.Error(w, "system playbooks are read-only", http.StatusBadRequest)
			return
		}
		slog.Error("failed to update playbook", "err", err)
		http.Error(w, "failed to update playbook", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pb) //nolint:errcheck
}

func (s *playbookServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	if id == "" {
		http.Error(w, "missing playbook ID", http.StatusBadRequest)
		return
	}
	if err := s.store.Delete(r.Context(), id); err != nil {
		if errors.Is(err, audit.ErrSystemPlaybook) {
			http.Error(w, "system playbooks are read-only", http.StatusBadRequest)
			return
		}
		slog.Error("failed to delete playbook", "err", err)
		http.Error(w, "failed to delete playbook", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleActivate promotes a playbook version: deactivates all other versions in its
// series and marks the target active. Returns the updated playbook.
func (s *playbookServer) handleActivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	if id == "" {
		http.Error(w, "missing playbook ID", http.StatusBadRequest)
		return
	}
	if err := s.store.Activate(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "playbook not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, audit.ErrSystemPlaybook) {
			http.Error(w, "system playbooks cannot be activated via API — managed by system seeder", http.StatusBadRequest)
			return
		}
		slog.Error("failed to activate playbook", "id", id, "err", err)
		http.Error(w, "failed to activate playbook", http.StatusInternalServerError)
		return
	}
	pb, err := s.store.Get(r.Context(), id)
	if err != nil {
		slog.Error("failed to re-fetch playbook after activation", "id", id, "err", err)
		http.Error(w, "activation succeeded but failed to fetch result", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pb) //nolint:errcheck
}
