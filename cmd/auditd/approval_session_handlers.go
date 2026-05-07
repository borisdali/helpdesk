package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"helpdesk/internal/audit"
)

type approvalSessionServer struct {
	store *audit.ApprovalSessionStore
}

// handleCreate handles POST /v1/approval/sessions.
// Body: {"granted_by":"...", "expires_in_secs":1800, "allowed_classes":["write","destructive"], "scope":"..."}
// Response: {"session_id":"aps_...", "expires_at":"..."}
func (s *approvalSessionServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		GrantedBy      string              `json:"granted_by"`
		ExpiresInSecs  int                 `json:"expires_in_secs"`
		AllowedClasses []audit.ActionClass `json:"allowed_classes"`
		Scope          string              `json:"scope,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.GrantedBy == "" {
		http.Error(w, "granted_by is required", http.StatusBadRequest)
		return
	}
	if body.ExpiresInSecs <= 0 {
		http.Error(w, "expires_in_secs must be positive", http.StatusBadRequest)
		return
	}
	if len(body.AllowedClasses) == 0 {
		http.Error(w, "allowed_classes must not be empty", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	sess := &audit.ApprovalSession{
		GrantedBy:      body.GrantedBy,
		GrantedAt:      now,
		ExpiresAt:      now.Add(time.Duration(body.ExpiresInSecs) * time.Second),
		AllowedClasses: body.AllowedClasses,
		Scope:          body.Scope,
	}
	if err := s.store.Create(r.Context(), sess); err != nil {
		slog.Error("failed to create approval session", "err", err)
		http.Error(w, "failed to create session", http.StatusInternalServerError)
		return
	}

	slog.Info("approval session created",
		"session_id", sess.SessionID,
		"granted_by", sess.GrantedBy,
		"expires_at", sess.ExpiresAt,
		"allowed_classes", body.AllowedClasses,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"session_id": sess.SessionID,
		"expires_at": sess.ExpiresAt.Format(time.RFC3339),
	})
}

// handleGet handles GET /v1/approval/sessions/{sessionID}.
func (s *approvalSessionServer) handleGet(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		http.Error(w, "sessionID is required", http.StatusBadRequest)
		return
	}
	sess, err := s.store.Get(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		slog.Error("failed to get approval session", "session_id", sessionID, "err", err)
		http.Error(w, "failed to get session", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sess) //nolint:errcheck
}

// handleRevoke handles DELETE /v1/approval/sessions/{sessionID}.
func (s *approvalSessionServer) handleRevoke(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		http.Error(w, "sessionID is required", http.StatusBadRequest)
		return
	}
	if err := s.store.Revoke(r.Context(), sessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		slog.Error("failed to revoke approval session", "session_id", sessionID, "err", err)
		http.Error(w, "failed to revoke session", http.StatusInternalServerError)
		return
	}
	slog.Info("approval session revoked", "session_id", sessionID)
	w.WriteHeader(http.StatusNoContent)
}
