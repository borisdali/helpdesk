package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"helpdesk/internal/audit"
)

func newFaultStabilityServer(t *testing.T) *faultStabilityServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	fs, err := audit.NewFaultStabilityStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewFaultStabilityStore: %v", err)
	}
	return &faultStabilityServer{store: fs}
}

func TestFaultStabilityHandlers_UpsertAndGet(t *testing.T) {
	srv := newFaultStabilityServer(t)

	payload := map[string]any{
		"fault_id":           "db-lock-contention",
		"fault_name":         "Lock contention / deadlock",
		"playbook_series_id": "pbs_lock_contention_triage",
		"diagnosis_model":    "claude-sonnet-4-6",
		"judge_model":        "claude-haiku-4-5-20251001",
		"n_runs":             5,
		"pass_rate":          1.0,
		"conf_range_pp":      4,
		"is_stable":          true,
	}
	body, _ := json.Marshal(payload)

	// POST — upsert.
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/fault-stability", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleUpsert(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST: got %d, want 204", rec.Code)
	}

	// GET by fault ID.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-stability/db-lock-contention", nil)
	req2.SetPathValue("faultID", "db-lock-contention")
	rec2 := httptest.NewRecorder()
	srv.handleGet(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET: got %d, want 200", rec2.Code)
	}

	var got audit.FaultStabilityCert
	if err := json.NewDecoder(rec2.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.FaultID != "db-lock-contention" {
		t.Errorf("FaultID: got %q, want db-lock-contention", got.FaultID)
	}
	if got.NRuns != 5 {
		t.Errorf("NRuns: got %d, want 5", got.NRuns)
	}
	if !got.IsStable {
		t.Error("IsStable: want true")
	}
}

func TestFaultStabilityHandlers_Upsert_MissingFaultID(t *testing.T) {
	srv := newFaultStabilityServer(t)

	body, _ := json.Marshal(map[string]any{"n_runs": 3, "is_stable": false})
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/fault-stability", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleUpsert(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing fault_id: got %d, want 400", rec.Code)
	}
}

func TestFaultStabilityHandlers_Upsert_ZeroRuns(t *testing.T) {
	srv := newFaultStabilityServer(t)

	body, _ := json.Marshal(map[string]any{"fault_id": "db-lock", "n_runs": 0})
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/fault-stability", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleUpsert(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("n_runs=0: got %d, want 400", rec.Code)
	}
}

func TestFaultStabilityHandlers_Get_NotFound(t *testing.T) {
	srv := newFaultStabilityServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-stability/nonexistent", nil)
	req.SetPathValue("faultID", "nonexistent")
	rec := httptest.NewRecorder()
	srv.handleGet(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("not found: got %d, want 404", rec.Code)
	}
}

func TestFaultStabilityHandlers_List(t *testing.T) {
	srv := newFaultStabilityServer(t)

	// Seed two certs.
	for _, faultID := range []string{"db-idle-in-transaction", "db-lock-contention"} {
		body, _ := json.Marshal(map[string]any{
			"fault_id": faultID, "n_runs": 5, "pass_rate": 1.0, "is_stable": true,
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/fleet/fault-stability", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.handleUpsert(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("POST %s: got %d", faultID, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-stability", nil)
	rec := httptest.NewRecorder()
	srv.handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list: got %d, want 200", rec.Code)
	}

	var result struct {
		Certs []audit.FaultStabilityCert `json:"certs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(result.Certs) != 2 {
		t.Errorf("list: got %d certs, want 2", len(result.Certs))
	}
}

func TestFaultStabilityHandlers_List_Empty(t *testing.T) {
	srv := newFaultStabilityServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-stability", nil)
	rec := httptest.NewRecorder()
	srv.handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list empty: got %d, want 200", rec.Code)
	}

	var result struct {
		Certs []audit.FaultStabilityCert `json:"certs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Certs == nil {
		t.Error("Certs should be an empty slice, not null")
	}
	if len(result.Certs) != 0 {
		t.Errorf("got %d certs, want 0", len(result.Certs))
	}
}

func TestFaultStabilityHandlers_Upsert_Overwrites(t *testing.T) {
	srv := newFaultStabilityServer(t)

	post := func(payload map[string]any) {
		t.Helper()
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/v1/fleet/fault-stability", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.handleUpsert(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("POST: got %d", rec.Code)
		}
	}

	post(map[string]any{"fault_id": "db-max-connections", "n_runs": 3, "pass_rate": 0.33, "is_stable": false})
	post(map[string]any{"fault_id": "db-max-connections", "n_runs": 5, "pass_rate": 1.0, "is_stable": true})

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-stability/db-max-connections", nil)
	req.SetPathValue("faultID", "db-max-connections")
	rec := httptest.NewRecorder()
	srv.handleGet(rec, req)

	var got audit.FaultStabilityCert
	json.NewDecoder(rec.Body).Decode(&got) //nolint:errcheck
	if !got.IsStable {
		t.Error("IsStable should be true after overwrite")
	}
	if got.NRuns != 5 {
		t.Errorf("NRuns: got %d, want 5", got.NRuns)
	}
}
