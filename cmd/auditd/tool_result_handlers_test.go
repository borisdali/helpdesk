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

// newToolResultServer returns a toolResultServer backed by a fresh temp-dir SQLite store.
func newToolResultServer(t *testing.T) *toolResultServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	trs, err := audit.NewToolResultStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewToolResultStore: %v", err)
	}
	return &toolResultServer{store: trs}
}

// TestToolResultHandlers_Record_OK posts a valid result and expects 201 with a result_id.
func TestToolResultHandlers_Record_OK(t *testing.T) {
	srv := newToolResultServer(t)

	body, _ := json.Marshal(map[string]any{
		"server_name": "prod-db-1",
		"tool_name":   "run_sql",
		"output":      "1 row affected",
		"success":     true,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/tool-results", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRecord(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}

	var result audit.PersistedToolResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.ResultID == "" {
		t.Error("expected non-empty result_id in response")
	}
	if len(result.ResultID) < 4 || result.ResultID[:4] != "res_" {
		t.Errorf("result_id = %q, want res_ prefix", result.ResultID)
	}
}

// TestToolResultHandlers_Record_MissingServerName expects 400 when server_name is absent.
func TestToolResultHandlers_Record_MissingServerName(t *testing.T) {
	srv := newToolResultServer(t)

	body, _ := json.Marshal(map[string]any{
		"tool_name": "run_sql",
		"success":   true,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/tool-results", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRecord(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestToolResultHandlers_Record_MissingToolName expects 400 when tool_name is absent.
func TestToolResultHandlers_Record_MissingToolName(t *testing.T) {
	srv := newToolResultServer(t)

	body, _ := json.Marshal(map[string]any{
		"server_name": "prod-db-1",
		"success":     true,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/tool-results", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleRecord(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestToolResultHandlers_Record_InvalidJSON expects 400 for malformed JSON.
func TestToolResultHandlers_Record_InvalidJSON(t *testing.T) {
	srv := newToolResultServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/tool-results", bytes.NewReader([]byte("{bad json")))
	w := httptest.NewRecorder()
	srv.handleRecord(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestToolResultHandlers_List_Empty expects 200 and {"results":[],"count":0} on an empty store.
func TestToolResultHandlers_List_Empty(t *testing.T) {
	srv := newToolResultServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/tool-results", nil)
	w := httptest.NewRecorder()
	srv.handleList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	count, ok := resp["count"].(float64)
	if !ok {
		t.Fatalf("count field missing or wrong type: %v", resp["count"])
	}
	if count != 0 {
		t.Errorf("count = %v, want 0", count)
	}
	results, ok := resp["results"].([]any)
	if !ok {
		t.Fatalf("results field missing or wrong type: %v", resp["results"])
	}
	if len(results) != 0 {
		t.Errorf("results length = %d, want 0", len(results))
	}
}

// TestToolResultHandlers_List_WithResults records 2 results then GETs and expects count=2.
func TestToolResultHandlers_List_WithResults(t *testing.T) {
	srv := newToolResultServer(t)

	for _, name := range []string{"prod-db-1", "prod-db-2"} {
		body, _ := json.Marshal(map[string]any{
			"server_name": name,
			"tool_name":   "run_sql",
			"success":     true,
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/tool-results", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.handleRecord(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("handleRecord %s: status = %d, want 201", name, w.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/tool-results", nil)
	w := httptest.NewRecorder()
	srv.handleList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	count, _ := resp["count"].(float64)
	if count != 2 {
		t.Errorf("count = %v, want 2", count)
	}
}

// TestToolResultHandlers_List_FilterByServer records 2 different servers, GETs ?server=X, expects count=1.
func TestToolResultHandlers_List_FilterByServer(t *testing.T) {
	srv := newToolResultServer(t)

	for _, name := range []string{"prod-db-1", "prod-db-2"} {
		body, _ := json.Marshal(map[string]any{
			"server_name": name,
			"tool_name":   "run_sql",
			"success":     true,
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/tool-results", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.handleRecord(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("handleRecord %s: status = %d, want 201", name, w.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/tool-results?server=prod-db-1", nil)
	w := httptest.NewRecorder()
	srv.handleList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	count, _ := resp["count"].(float64)
	if count != 1 {
		t.Errorf("count = %v, want 1", count)
	}
}

// TestToolResultHandlers_List_ParseSinceDuration unit-tests parseSinceDuration.
func TestToolResultHandlers_List_ParseSinceDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"7d", 168 * time.Hour},
		{"24h", 24 * time.Hour},
		{"30m", 30 * time.Minute},
		{"", 0},
		{"bad", 0},
	}
	for _, tt := range tests {
		got := parseSinceDuration(tt.input)
		if got != tt.want {
			t.Errorf("parseSinceDuration(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
