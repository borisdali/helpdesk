package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	"helpdesk/internal/audit"
)

type govbotServer struct {
	store *audit.GovbotStore
}

// handleSaveRun records one govbot compliance run snapshot.
// POST /v1/govbot/runs
func (s *govbotServer) handleSaveRun(w http.ResponseWriter, r *http.Request) {
	var run audit.GovbotRun
	if err := json.NewDecoder(r.Body).Decode(&run); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.SaveRun(run); err != nil {
		http.Error(w, "failed to save run: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// handleGetRuns returns recent govbot run snapshots.
// GET /v1/govbot/runs?window=24h&gateway=http://...&limit=50
func (s *govbotServer) handleGetRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	window := q.Get("window")
	gateway := q.Get("gateway")
	limit := 50
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	runs, err := s.store.RecentRuns(window, gateway, limit)
	if err != nil {
		http.Error(w, "failed to query runs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if runs == nil {
		runs = []audit.GovbotRun{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(runs) //nolint:errcheck
}
