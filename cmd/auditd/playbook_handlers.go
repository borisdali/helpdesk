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
	store *audit.PlaybookStore
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
	json.NewEncoder(w).Encode(pb)
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
	json.NewEncoder(w).Encode(pb)
}

func (s *playbookServer) handleList(w http.ResponseWriter, r *http.Request) {
	playbooks, err := s.store.List(r.Context())
	if err != nil {
		slog.Error("failed to list playbooks", "err", err)
		http.Error(w, "failed to list playbooks", http.StatusInternalServerError)
		return
	}
	if playbooks == nil {
		playbooks = []*audit.Playbook{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"playbooks": playbooks})
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
		slog.Error("failed to update playbook", "err", err)
		http.Error(w, "failed to update playbook", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pb)
}

func (s *playbookServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("playbookID")
	if id == "" {
		http.Error(w, "missing playbook ID", http.StatusBadRequest)
		return
	}
	if err := s.store.Delete(r.Context(), id); err != nil {
		slog.Error("failed to delete playbook", "err", err)
		http.Error(w, "failed to delete playbook", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
