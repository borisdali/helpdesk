package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

func newApprovalSessionSrv(t *testing.T) *approvalSessionServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ss, err := audit.NewApprovalSessionStore(store.DB())
	if err != nil {
		t.Fatalf("NewApprovalSessionStore: %v", err)
	}
	return &approvalSessionServer{store: ss}
}

func doSessionCreate(t *testing.T, srv *approvalSessionServer, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/approval/sessions", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleCreate(rec, req)
	return rec
}

func doSessionGet(t *testing.T, srv *approvalSessionServer, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/approval/sessions/"+sessionID, nil)
	req.SetPathValue("sessionID", sessionID)
	rec := httptest.NewRecorder()
	srv.handleGet(rec, req)
	return rec
}

func doSessionRevoke(t *testing.T, srv *approvalSessionServer, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/v1/approval/sessions/"+sessionID, nil)
	req.SetPathValue("sessionID", sessionID)
	rec := httptest.NewRecorder()
	srv.handleRevoke(rec, req)
	return rec
}

// ── Create ───────────────────────────────────────────────────────────────────

func TestApprovalSessionHandlers_Create_OK(t *testing.T) {
	srv := newApprovalSessionSrv(t)

	rec := doSessionCreate(t, srv, map[string]any{
		"granted_by":      "boris",
		"expires_in_secs": 1800,
		"allowed_classes": []string{"write", "destructive"},
		"scope":           "pbs_restart_triage",
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		SessionID string `json:"session_id"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.SessionID == "" {
		t.Error("session_id should be non-empty")
	}
	if len(resp.SessionID) < 4 || resp.SessionID[:4] != "aps_" {
		t.Errorf("session_id = %q, want aps_ prefix", resp.SessionID)
	}
	if resp.ExpiresAt == "" {
		t.Error("expires_at should be non-empty")
	}
	// Sanity-check that expires_at parses as RFC3339 and is in the future.
	exp, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at not valid RFC3339: %v", err)
	}
	if !exp.After(time.Now()) {
		t.Error("expires_at should be in the future")
	}
}

func TestApprovalSessionHandlers_Create_MissingGrantedBy(t *testing.T) {
	srv := newApprovalSessionSrv(t)

	rec := doSessionCreate(t, srv, map[string]any{
		"expires_in_secs": 1800,
		"allowed_classes": []string{"write"},
	})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestApprovalSessionHandlers_Create_ZeroExpiry(t *testing.T) {
	srv := newApprovalSessionSrv(t)

	rec := doSessionCreate(t, srv, map[string]any{
		"granted_by":      "boris",
		"expires_in_secs": 0,
		"allowed_classes": []string{"write"},
	})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestApprovalSessionHandlers_Create_EmptyAllowedClasses(t *testing.T) {
	srv := newApprovalSessionSrv(t)

	rec := doSessionCreate(t, srv, map[string]any{
		"granted_by":      "boris",
		"expires_in_secs": 1800,
		"allowed_classes": []string{},
	})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// ── Get ──────────────────────────────────────────────────────────────────────

func TestApprovalSessionHandlers_Get_OK(t *testing.T) {
	srv := newApprovalSessionSrv(t)

	createRec := doSessionCreate(t, srv, map[string]any{
		"granted_by":      "alice",
		"expires_in_secs": 3600,
		"allowed_classes": []string{"write"},
	})
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		SessionID string `json:"session_id"`
	}
	json.NewDecoder(createRec.Body).Decode(&created) //nolint:errcheck

	rec := doSessionGet(t, srv, created.SessionID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var sess audit.ApprovalSession
	if err := json.NewDecoder(rec.Body).Decode(&sess); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sess.SessionID != created.SessionID {
		t.Errorf("session_id = %q, want %q", sess.SessionID, created.SessionID)
	}
	if sess.GrantedBy != "alice" {
		t.Errorf("granted_by = %q, want alice", sess.GrantedBy)
	}
	if sess.Revoked {
		t.Error("revoked should be false")
	}
}

func TestApprovalSessionHandlers_Get_NotFound(t *testing.T) {
	srv := newApprovalSessionSrv(t)

	rec := doSessionGet(t, srv, "aps_nonexistent")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// ── Revoke ───────────────────────────────────────────────────────────────────

func TestApprovalSessionHandlers_Revoke_OK(t *testing.T) {
	srv := newApprovalSessionSrv(t)

	createRec := doSessionCreate(t, srv, map[string]any{
		"granted_by":      "bob",
		"expires_in_secs": 1800,
		"allowed_classes": []string{"destructive"},
	})
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		SessionID string `json:"session_id"`
	}
	json.NewDecoder(createRec.Body).Decode(&created) //nolint:errcheck

	revokeRec := doSessionRevoke(t, srv, created.SessionID)
	if revokeRec.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204; body: %s", revokeRec.Code, revokeRec.Body.String())
	}

	// Session should now show as revoked.
	getRec := doSessionGet(t, srv, created.SessionID)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get after revoke status = %d", getRec.Code)
	}
	var sess audit.ApprovalSession
	json.NewDecoder(getRec.Body).Decode(&sess) //nolint:errcheck
	if !sess.Revoked {
		t.Error("session should be marked revoked after DELETE")
	}
}

func TestApprovalSessionHandlers_Revoke_NotFound(t *testing.T) {
	srv := newApprovalSessionSrv(t)

	rec := doSessionRevoke(t, srv, "aps_nonexistent")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
