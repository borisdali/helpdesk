package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
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
	// runs, when set, lets a listing carry each incident's playbook series
	// (?include=series). Without it that option is ignored.
	runs *audit.PlaybookRunStore
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

// includeSet reads ?include= values, each possibly a comma-separated list
// ("series,runs"), into a set.
func includeSet(values []string) map[string]bool {
	set := map[string]bool{}
	for _, v := range values {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				set[name] = true
			}
		}
	}
	return set
}

// handleList handles GET /v1/incidents. Supports ?origin=&status=&attribution=&
// series_id=&target_key=&limit=, and for the availability ledger:
//
//	unattributed=true   only incidents with no target_key (the database could not be resolved)
//	since=<RFC3339>     only incidents still open, or resolved at or after that time
//	include=series      each incident also carries series_ids, the series of every run it spanned
//	include=runs        ... and runs, each with its series, outcome and times (a recovery-time
//	                    estimate is made from them); "series,runs" asks for both
//
// "truncated" is true when the page is full, so a caller summing incidents
// knows it may have left some out.
func (s *incidentServer) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := audit.IncidentListFilter{
		Origin:       q.Get("origin"),
		Status:       q.Get("status"),
		Attribution:  q.Get("attribution"),
		SeriesID:     q.Get("series_id"),
		TargetKey:    q.Get("target_key"),
		Unattributed: q.Get("unattributed") == "true",
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "since must be an RFC3339 time, e.g. 2026-11-09T00:00:00Z", http.StatusBadRequest)
			return
		}
		filter.Since = t
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
	truncated := len(incidents) >= audit.EffectiveIncidentLimit(filter.Limit)

	var out any = incidents
	include := includeSet(q["include"])
	if (include["series"] || include["runs"]) && s.runs != nil {
		ids := make([]string, 0, len(incidents))
		for _, inc := range incidents {
			if inc.EntryRunID != "" {
				ids = append(ids, inc.EntryRunID)
			}
		}
		withRuns := make([]audit.IncidentWithSeries, 0, len(incidents))
		if include["runs"] {
			// The runs carry their series, so one query serves both.
			spans, err := s.runs.SpansByEntryRun(r.Context(), ids)
			if err != nil {
				slog.Error("failed to read the runs of listed incidents", "err", err)
				http.Error(w, "failed to list incidents", http.StatusInternalServerError)
				return
			}
			for _, inc := range incidents {
				var series []string
				for _, sp := range spans[inc.EntryRunID] {
					if !slices.Contains(series, sp.SeriesID) {
						series = append(series, sp.SeriesID)
					}
				}
				withRuns = append(withRuns, audit.IncidentWithSeries{Incident: inc, SeriesIDs: series, Runs: spans[inc.EntryRunID]})
			}
		} else {
			series, err := s.runs.SeriesByEntryRun(r.Context(), ids)
			if err != nil {
				slog.Error("failed to read the series of listed incidents", "err", err)
				http.Error(w, "failed to list incidents", http.StatusInternalServerError)
				return
			}
			for _, inc := range incidents {
				withRuns = append(withRuns, audit.IncidentWithSeries{Incident: inc, SeriesIDs: series[inc.EntryRunID]})
			}
		}
		out = withRuns
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"incidents": out, "count": len(incidents), "truncated": truncated}) //nolint:errcheck
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
