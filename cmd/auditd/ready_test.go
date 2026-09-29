package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"helpdesk/internal/audit"
)

func newReadyTestServer(t *testing.T) (*server, *audit.Store) {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &server{store: store}, store
}

func TestHandleReady_OK(t *testing.T) {
	srv, _ := newReadyTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	srv.handleReady(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "ready" {
		t.Errorf("status field = %q, want ready", resp["status"])
	}
}

// TestHandleReady_SchemaMissing is a regression test for the real bug that
// motivated this endpoint: a live database whose schema was never created
// (or was created against a since-replaced database) pings fine — Ping only
// proves the connection works — but every real query fails. handleHealth's
// static response would have reported this instance as perfectly healthy.
func TestHandleReady_SchemaMissing(t *testing.T) {
	srv, store := newReadyTestServer(t)

	if _, err := store.DB().Exec("DROP TABLE audit_events"); err != nil {
		t.Fatalf("DROP TABLE audit_events: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	srv.handleReady(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != "not ready" {
		t.Errorf("status field = %q, want %q", resp["status"], "not ready")
	}
}

func TestHandleReady_DatabaseUnreachable(t *testing.T) {
	srv, store := newReadyTestServer(t)
	store.Close()

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	srv.handleReady(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
}
