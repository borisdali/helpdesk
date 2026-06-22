package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"helpdesk/internal/audit"
)

type faultStabilityServer struct {
	store *audit.FaultStabilityStore
}

// handleUpsert handles POST /v1/fleet/fault-stability.
func (s *faultStabilityServer) handleUpsert(w http.ResponseWriter, r *http.Request) {
	var cert audit.FaultStabilityCert
	if err := json.NewDecoder(r.Body).Decode(&cert); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if cert.FaultID == "" {
		http.Error(w, "fault_id is required", http.StatusBadRequest)
		return
	}
	if cert.NRuns < 1 {
		http.Error(w, "n_runs must be >= 1", http.StatusBadRequest)
		return
	}
	if err := s.store.Upsert(r.Context(), &cert); err != nil {
		slog.Error("failed to upsert fault stability cert", "fault_id", cert.FaultID, "err", err)
		http.Error(w, "failed to store cert", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGet handles GET /v1/fleet/fault-stability/{faultID}.
func (s *faultStabilityServer) handleGet(w http.ResponseWriter, r *http.Request) {
	faultID := r.PathValue("faultID")
	if faultID == "" {
		http.Error(w, "faultID is required", http.StatusBadRequest)
		return
	}
	cert, err := s.store.GetByFaultID(r.Context(), faultID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no stability cert for fault", http.StatusNotFound)
			return
		}
		slog.Error("failed to get fault stability cert", "fault_id", faultID, "err", err)
		http.Error(w, "failed to get cert", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cert) //nolint:errcheck
}

// handleList handles GET /v1/fleet/fault-stability.
func (s *faultStabilityServer) handleList(w http.ResponseWriter, r *http.Request) {
	certs, err := s.store.ListAll(r.Context())
	if err != nil {
		slog.Error("failed to list fault stability certs", "err", err)
		http.Error(w, "failed to list certs", http.StatusInternalServerError)
		return
	}
	if certs == nil {
		certs = []*audit.FaultStabilityCert{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"certs": certs}) //nolint:errcheck
}
