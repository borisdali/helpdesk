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
	regressed, err := s.store.Upsert(r.Context(), &cert)
	if err != nil {
		slog.Error("failed to upsert fault stability cert", "fault_id", cert.FaultID, "err", err)
		http.Error(w, "failed to store cert", http.StatusInternalServerError)
		return
	}
	if regressed {
		slog.Warn("fault stability cert regressed — previously earned trust (STABLE+CLEAN+attribution-consistent), no longer does",
			"fault_id", cert.FaultID, "diagnosis_model", cert.DiagnosisModel)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]bool{"regressed": regressed}) //nolint:errcheck
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

// handleHistory handles GET /v1/fleet/fault-stability/{faultID}/history.
// diagnosis_model is required — history is scoped per (fault_id, model), same
// as the cert itself. limit defaults to 10 (see FaultStabilityStore.GetHistory).
func (s *faultStabilityServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	faultID := r.PathValue("faultID")
	if faultID == "" {
		http.Error(w, "faultID is required", http.StatusBadRequest)
		return
	}
	model := r.URL.Query().Get("diagnosis_model")
	if model == "" {
		http.Error(w, "diagnosis_model query parameter is required", http.StatusBadRequest)
		return
	}
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	history, err := s.store.GetHistory(r.Context(), faultID, model, limit)
	if err != nil {
		slog.Error("failed to get fault stability cert history", "fault_id", faultID, "diagnosis_model", model, "err", err)
		http.Error(w, "failed to get cert history", http.StatusInternalServerError)
		return
	}
	if history == nil {
		history = []*audit.FaultStabilityCert{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"history": history}) //nolint:errcheck
}

// handleList handles GET /v1/fleet/fault-stability. When both series_id and
// model query params are present, filters to certs for that playbook
// series + diagnosis model (used by the gateway's trust-gate check — a
// series can map to multiple faults, so this can return more than one cert).
// Otherwise returns every cert, unfiltered.
func (s *faultStabilityServer) handleList(w http.ResponseWriter, r *http.Request) {
	seriesID := r.URL.Query().Get("series_id")
	model := r.URL.Query().Get("model")

	var certs []*audit.FaultStabilityCert
	var err error
	if seriesID != "" && model != "" {
		certs, err = s.store.GetBySeriesAndModel(r.Context(), seriesID, model)
	} else {
		certs, err = s.store.ListAll(r.Context())
	}
	if err != nil {
		slog.Error("failed to list fault stability certs", "series_id", seriesID, "model", model, "err", err)
		http.Error(w, "failed to list certs", http.StatusInternalServerError)
		return
	}
	if certs == nil {
		certs = []*audit.FaultStabilityCert{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"certs": certs}) //nolint:errcheck
}
