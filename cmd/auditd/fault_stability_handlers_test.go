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
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: got %d, want 200", rec.Code)
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
	if got.DiagnosisModel != "claude-sonnet-4-6" {
		t.Errorf("DiagnosisModel: got %q, want claude-sonnet-4-6", got.DiagnosisModel)
	}
	if got.JudgeModel != "claude-haiku-4-5-20251001" {
		t.Errorf("JudgeModel: got %q, want claude-haiku-4-5-20251001", got.JudgeModel)
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
		if rec.Code != http.StatusOK {
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
		if rec.Code != http.StatusOK {
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

// TestFaultStabilityHandlers_List_MultiModel verifies that handleList returns one
// row per (fault_id, diagnosis_model) pair — the data shape cert-compare depends on.
// If the handler ever deduplicates by fault_id, cert-compare silently loses data.
func TestFaultStabilityHandlers_List_MultiModel(t *testing.T) {
	srv := newFaultStabilityServer(t)

	post := func(faultID, model string, stable bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"fault_id":        faultID,
			"diagnosis_model": model,
			"n_runs":          3,
			"pass_rate":       map[bool]float64{true: 1.0, false: 0.33}[stable],
			"is_stable":       stable,
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/fleet/fault-stability", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.handleUpsert(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s/%s: got %d", faultID, model, rec.Code)
		}
	}

	// Same fault, two models — the regression case for cert-compare.
	post("db-lock-contention", "claude-sonnet-4-5", true)
	post("db-lock-contention", "claude-sonnet-4-6", false)
	// Different fault for good measure.
	post("db-max-connections", "claude-sonnet-4-5", true)

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

	if len(result.Certs) != 3 {
		t.Fatalf("got %d certs, want 3 (two models for db-lock-contention + one for db-max-connections)", len(result.Certs))
	}

	// Verify both model rows for db-lock-contention are present with correct stability.
	stable := map[string]bool{}
	for _, c := range result.Certs {
		if c.FaultID == "db-lock-contention" {
			stable[c.DiagnosisModel] = c.IsStable
		}
	}
	if !stable["claude-sonnet-4-5"] {
		t.Error("claude-sonnet-4-5 cert for db-lock-contention: want IsStable=true")
	}
	if stable["claude-sonnet-4-6"] {
		t.Error("claude-sonnet-4-6 cert for db-lock-contention: want IsStable=false")
	}
}

func upsertCert(t *testing.T, srv *faultStabilityServer, payload map[string]any) {
	t.Helper()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/fault-stability", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleUpsert(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

func TestFaultStabilityHandlers_Upsert_ResponseBody_RegressedField(t *testing.T) {
	srv := newFaultStabilityServer(t)

	// First cert: earns trust.
	body1, _ := json.Marshal(map[string]any{
		"fault_id": "k8s-oomkilled", "diagnosis_model": "claude-sonnet-4-6",
		"n_runs": 5, "is_stable": true, "is_clean": true, "attribution_consistent": true,
	})
	req1 := httptest.NewRequest(http.MethodPost, "/v1/fleet/fault-stability", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	srv.handleUpsert(rec1, req1)
	var resp1 struct {
		Regressed bool `json:"regressed"`
	}
	if err := json.NewDecoder(rec1.Body).Decode(&resp1); err != nil {
		t.Fatalf("decode response 1: %v", err)
	}
	if resp1.Regressed {
		t.Error("first-ever cert: expected regressed=false in response body")
	}

	// Second cert: stops earning trust.
	body2, _ := json.Marshal(map[string]any{
		"fault_id": "k8s-oomkilled", "diagnosis_model": "claude-sonnet-4-6",
		"n_runs": 5, "is_stable": true, "is_clean": false, "attribution_consistent": true, "warning_count": 1,
	})
	req2 := httptest.NewRequest(http.MethodPost, "/v1/fleet/fault-stability", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	srv.handleUpsert(rec2, req2)
	var resp2 struct {
		Regressed bool `json:"regressed"`
	}
	if err := json.NewDecoder(rec2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode response 2: %v", err)
	}
	if !resp2.Regressed {
		t.Error("cert going CLEAN=false after previously earning trust: expected regressed=true in response body")
	}
}

func TestFaultStabilityHandlers_History(t *testing.T) {
	srv := newFaultStabilityServer(t)

	for i := 1; i <= 3; i++ {
		upsertCert(t, srv, map[string]any{
			"fault_id": "k8s-oomkilled", "diagnosis_model": "claude-sonnet-4-6",
			"n_runs": i, "is_stable": true,
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-stability/k8s-oomkilled/history?diagnosis_model=claude-sonnet-4-6", nil)
	req.SetPathValue("faultID", "k8s-oomkilled")
	rec := httptest.NewRecorder()
	srv.handleHistory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET history: got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		History []audit.FaultStabilityCert `json:"history"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.History) != 3 {
		t.Fatalf("got %d history entries, want 3", len(result.History))
	}
	if result.History[0].NRuns != 3 {
		t.Errorf("most recent entry NRuns = %d, want 3", result.History[0].NRuns)
	}
}

func TestFaultStabilityHandlers_History_RespectsCustomLimit(t *testing.T) {
	srv := newFaultStabilityServer(t)
	for i := 1; i <= 5; i++ {
		upsertCert(t, srv, map[string]any{
			"fault_id": "k8s-oomkilled", "diagnosis_model": "claude-sonnet-4-6",
			"n_runs": i, "is_stable": true,
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-stability/k8s-oomkilled/history?diagnosis_model=claude-sonnet-4-6&limit=2", nil)
	req.SetPathValue("faultID", "k8s-oomkilled")
	rec := httptest.NewRecorder()
	srv.handleHistory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		History []audit.FaultStabilityCert `json:"history"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.History) != 2 {
		t.Fatalf("got %d entries with limit=2, want 2", len(result.History))
	}
}

func TestFaultStabilityHandlers_History_InvalidLimit_FallsBackToDefault(t *testing.T) {
	srv := newFaultStabilityServer(t)
	for i := 1; i <= 3; i++ {
		upsertCert(t, srv, map[string]any{
			"fault_id": "k8s-oomkilled", "diagnosis_model": "claude-sonnet-4-6",
			"n_runs": i, "is_stable": true,
		})
	}

	for _, limit := range []string{"abc", "-5", "0"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-stability/k8s-oomkilled/history?diagnosis_model=claude-sonnet-4-6&limit="+limit, nil)
		req.SetPathValue("faultID", "k8s-oomkilled")
		rec := httptest.NewRecorder()
		srv.handleHistory(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("limit=%q: got %d, want 200; body: %s", limit, rec.Code, rec.Body.String())
		}
		var result struct {
			History []audit.FaultStabilityCert `json:"history"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
			t.Fatalf("limit=%q: decode: %v", limit, err)
		}
		// Falls back to the default (10) rather than erroring or returning
		// zero rows — all 3 seeded entries must come back.
		if len(result.History) != 3 {
			t.Errorf("limit=%q: got %d entries, want 3 (default limit should not exclude them)", limit, len(result.History))
		}
	}
}

func TestFaultStabilityHandlers_History_MissingDiagnosisModel(t *testing.T) {
	srv := newFaultStabilityServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-stability/k8s-oomkilled/history", nil)
	req.SetPathValue("faultID", "k8s-oomkilled")
	rec := httptest.NewRecorder()
	srv.handleHistory(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 when diagnosis_model is omitted", rec.Code)
	}
}

func TestFaultStabilityHandlers_History_NeverCertified_ReturnsEmptyArray(t *testing.T) {
	srv := newFaultStabilityServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/fault-stability/db-never-tested/history?diagnosis_model=claude-sonnet-4-6", nil)
	req.SetPathValue("faultID", "db-never-tested")
	rec := httptest.NewRecorder()
	srv.handleHistory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var result struct {
		History []audit.FaultStabilityCert `json:"history"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.History == nil {
		t.Error("expected an empty array, got null (breaks strict JSON clients expecting an array type)")
	}
	if len(result.History) != 0 {
		t.Errorf("got %d entries, want 0", len(result.History))
	}
}
