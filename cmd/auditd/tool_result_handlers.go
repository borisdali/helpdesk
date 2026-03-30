package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"helpdesk/internal/audit"
)

// toolResultServer handles HTTP endpoints for tool result recording and querying.
type toolResultServer struct {
	store *audit.ToolResultStore
}

func (s *toolResultServer) handleRecord(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var result audit.PersistedToolResult
	if err := json.Unmarshal(body, &result); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if result.ServerName == "" {
		http.Error(w, "server_name is required", http.StatusBadRequest)
		return
	}
	if result.ToolName == "" {
		http.Error(w, "tool_name is required", http.StatusBadRequest)
		return
	}
	if err := s.store.Record(r.Context(), &result); err != nil {
		slog.Error("failed to record tool result", "err", err)
		http.Error(w, "failed to record tool result", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(result) //nolint:errcheck
}

func (s *toolResultServer) handleList(w http.ResponseWriter, r *http.Request) {
	q := audit.ToolResultQuery{
		ServerName: r.URL.Query().Get("server"),
		ToolName:   r.URL.Query().Get("tool"),
		JobID:      r.URL.Query().Get("job_id"),
	}

	// Parse limit
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil {
			q.Limit = l
		}
	}

	// Parse since (e.g. "7d", "24h", "30m")
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		q.Since = parseSinceDuration(sinceStr)
	}

	results, err := s.store.List(r.Context(), q)
	if err != nil {
		slog.Error("failed to list tool results", "err", err)
		http.Error(w, "failed to list tool results", http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []*audit.PersistedToolResult{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"results": results, "count": len(results)}) //nolint:errcheck
}

// parseSinceDuration converts strings like "7d", "24h", "30m" to time.Duration.
// Supports d (days), h (hours), m (minutes). Falls back to time.ParseDuration.
func parseSinceDuration(s string) time.Duration {
	if len(s) < 2 {
		return 0
	}
	unit := s[len(s)-1]
	val, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil {
		if d, err := time.ParseDuration(s); err == nil {
			return d
		}
		return 0
	}
	switch unit {
	case 'd':
		return time.Duration(val * float64(24*time.Hour))
	case 'h':
		return time.Duration(val * float64(time.Hour))
	case 'm':
		return time.Duration(val * float64(time.Minute))
	default:
		if d, err := time.ParseDuration(s); err == nil {
			return d
		}
		return 0
	}
}
