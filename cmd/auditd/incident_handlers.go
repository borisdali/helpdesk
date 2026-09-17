package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"helpdesk/internal/audit"
)

// incidentServer holds the incidentStore for the /v1/incidents endpoints —
// Phase 1 of the v0.29 incident-entity design (see docs/INCIDENTS.md and the
// design recorded in memory as project_incident_entity_gap.md). This table is
// the missing layer ABOVE playbook_runs: one row per user-facing incident
// (real or faulttest-injected), not one row per hop.
type incidentServer struct {
	store *audit.IncidentStore
}

// handleCreate handles POST /v1/incidents. Called by the gateway when a
// genuine entry-point playbook run starts (prior_run_id == "") — see
// recordPlaybookRunStart in cmd/gateway/playbooks.go.
func (s *incidentServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	var inc audit.Incident
	if err := json.NewDecoder(r.Body).Decode(&inc); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.Create(r.Context(), &inc); err != nil {
		slog.Error("failed to create incident", "entry_run_id", inc.EntryRunID, "err", err)
		http.Error(w, "failed to create incident", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(inc) //nolint:errcheck
}

// handleUpdate handles PATCH /v1/incidents/{incidentID}. Partial update —
// only fields present in the request body are touched (see
// audit.IncidentUpdate). Used to record status/attribution/bundle_path as an
// incident progresses, from multiple independent callers over its lifetime
// (the gateway on terminal outcome, the incident agent once a bundle is
// created, a future attribution-classification pass).
func (s *incidentServer) handleUpdate(w http.ResponseWriter, r *http.Request) {
	incidentID := r.PathValue("incidentID")
	if incidentID == "" {
		http.Error(w, "incidentID is required", http.StatusBadRequest)
		return
	}

	var body struct {
		Status                *string `json:"status,omitempty"`
		Attribution           *string `json:"attribution,omitempty"`
		Severity              *string `json:"severity,omitempty"`
		ExternalCorrelationID *string `json:"external_correlation_id,omitempty"`
		BundlePath            *string `json:"bundle_path,omitempty"`
		DraftPlaybookID       *string `json:"draft_playbook_id,omitempty"`
		TraceID               *string `json:"trace_id,omitempty"`
		ResolvedAt            *string `json:"resolved_at,omitempty"` // RFC3339; parsed below
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	update := audit.IncidentUpdate{
		Status:                body.Status,
		Attribution:           body.Attribution,
		Severity:              body.Severity,
		ExternalCorrelationID: body.ExternalCorrelationID,
		BundlePath:            body.BundlePath,
		DraftPlaybookID:       body.DraftPlaybookID,
		TraceID:               body.TraceID,
	}
	if body.ResolvedAt != nil {
		t, err := time.Parse(time.RFC3339, *body.ResolvedAt)
		if err != nil {
			http.Error(w, "invalid resolved_at (want RFC3339): "+err.Error(), http.StatusBadRequest)
			return
		}
		update.ResolvedAt = &t
	}

	if err := s.store.Update(r.Context(), incidentID, update); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "incident not found", http.StatusNotFound)
			return
		}
		slog.Error("failed to update incident", "incident_id", incidentID, "err", err)
		http.Error(w, "failed to update incident", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGet handles GET /v1/incidents/{incidentID}.
func (s *incidentServer) handleGet(w http.ResponseWriter, r *http.Request) {
	incidentID := r.PathValue("incidentID")
	if incidentID == "" {
		http.Error(w, "incidentID is required", http.StatusBadRequest)
		return
	}
	inc, err := s.store.GetByID(r.Context(), incidentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "incident not found", http.StatusNotFound)
			return
		}
		slog.Error("failed to get incident", "incident_id", incidentID, "err", err)
		http.Error(w, "failed to get incident", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(inc) //nolint:errcheck
}

// handleList handles GET /v1/incidents. Supports ?origin=&status=&attribution=&limit=.
func (s *incidentServer) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := audit.IncidentListFilter{
		Origin:      q.Get("origin"),
		Status:      q.Get("status"),
		Attribution: q.Get("attribution"),
	}
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			filter.Limit = n
		}
	}

	incidents, err := s.store.List(r.Context(), filter)
	if err != nil {
		slog.Error("failed to list incidents", "err", err)
		http.Error(w, "failed to list incidents", http.StatusInternalServerError)
		return
	}
	if incidents == nil {
		incidents = []*audit.Incident{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"incidents": incidents, "count": len(incidents)}) //nolint:errcheck
}

// handleGetByEntryRunID handles GET /v1/incidents/by-run/{runID} — lets a
// caller that only has a playbook_runs.run_id (e.g. the gateway, mid-request)
// find the incident row it belongs to without needing to have separately
// tracked the incident_id itself.
func (s *incidentServer) handleGetByEntryRunID(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if runID == "" {
		http.Error(w, "runID is required", http.StatusBadRequest)
		return
	}
	inc, err := s.store.GetByEntryRunID(r.Context(), runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no incident for run_id", http.StatusNotFound)
			return
		}
		slog.Error("failed to get incident by entry_run_id", "run_id", runID, "err", err)
		http.Error(w, "failed to get incident", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(inc) //nolint:errcheck
}

// handleGetByTraceID handles GET /v1/incidents/by-trace/{traceID} — lets the
// /from-trace draft-synthesis handler (the one place both its callers,
// faulttest's own and create_incident_bundle's, converge) find the incident
// row to attach draft_playbook_id to, given only a trace_id.
func (s *incidentServer) handleGetByTraceID(w http.ResponseWriter, r *http.Request) {
	traceID := r.PathValue("traceID")
	if traceID == "" {
		http.Error(w, "traceID is required", http.StatusBadRequest)
		return
	}
	inc, err := s.store.GetByTraceID(r.Context(), traceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no incident for trace_id", http.StatusNotFound)
			return
		}
		slog.Error("failed to get incident by trace_id", "trace_id", traceID, "err", err)
		http.Error(w, "failed to get incident", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(inc) //nolint:errcheck
}
