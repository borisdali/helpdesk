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

func newIncidentServer(t *testing.T) *incidentServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	is, err := audit.NewIncidentStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewIncidentStore: %v", err)
	}
	return &incidentServer{store: is}
}

func TestIncidentHandlers_Create_OK(t *testing.T) {
	srv := newIncidentServer(t)

	body, _ := json.Marshal(map[string]any{"trace_id": "tr_abc", "entry_run_id": "plr_xyz"})
	req := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleCreate(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var inc audit.Incident
	if err := json.NewDecoder(rec.Body).Decode(&inc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if inc.IncidentID == "" {
		t.Error("incident_id should be set")
	}
	if inc.Origin != audit.IncidentOriginReal {
		t.Errorf("origin = %q, want %q (default)", inc.Origin, audit.IncidentOriginReal)
	}
	if inc.Status != audit.IncidentStatusOpen {
		t.Errorf("status = %q, want %q (default)", inc.Status, audit.IncidentStatusOpen)
	}
}

func TestIncidentHandlers_Create_SeriesIDSurvivesRoundTrip(t *testing.T) {
	srv := newIncidentServer(t)

	body, _ := json.Marshal(map[string]any{"entry_run_id": "plr_xyz", "series_id": "pbs_db_max_connections_triage"})
	createReq := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(body))
	createRec := httptest.NewRecorder()
	srv.handleCreate(createRec, createReq)
	var created audit.Incident
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.SeriesID != "pbs_db_max_connections_triage" {
		t.Errorf("create response series_id = %q, want pbs_db_max_connections_triage", created.SeriesID)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/incidents/"+created.IncidentID, nil)
	getReq.SetPathValue("incidentID", created.IncidentID)
	getRec := httptest.NewRecorder()
	srv.handleGet(getRec, getReq)
	var fetched audit.Incident
	if err := json.NewDecoder(getRec.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.SeriesID != "pbs_db_max_connections_triage" {
		t.Errorf("GET series_id = %q, want pbs_db_max_connections_triage", fetched.SeriesID)
	}
}

func TestIncidentHandlers_Create_InvalidJSON(t *testing.T) {
	srv := newIncidentServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	srv.handleCreate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestIncidentHandlers_GetAndUpdate_RoundTrip(t *testing.T) {
	srv := newIncidentServer(t)

	body, _ := json.Marshal(map[string]any{"trace_id": "tr_abc", "origin": "faulttest"})
	createReq := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(body))
	createRec := httptest.NewRecorder()
	srv.handleCreate(createRec, createReq)
	var created audit.Incident
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	// GET before update.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/incidents/"+created.IncidentID, nil)
	getReq.SetPathValue("incidentID", created.IncidentID)
	getRec := httptest.NewRecorder()
	srv.handleGet(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", getRec.Code, getRec.Body.String())
	}
	var fetched audit.Incident
	if err := json.NewDecoder(getRec.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.Origin != "faulttest" {
		t.Errorf("origin = %q, want faulttest", fetched.Origin)
	}

	// PATCH status + attribution.
	patchBody, _ := json.Marshal(map[string]any{"status": "resolved", "attribution": "oom-kill"})
	patchReq := httptest.NewRequest(http.MethodPatch, "/v1/incidents/"+created.IncidentID, bytes.NewReader(patchBody))
	patchReq.SetPathValue("incidentID", created.IncidentID)
	patchRec := httptest.NewRecorder()
	srv.handleUpdate(patchRec, patchReq)
	if patchRec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status = %d, want 204; body: %s", patchRec.Code, patchRec.Body.String())
	}

	// GET after update confirms the patch landed and origin (untouched) survived.
	getReq2 := httptest.NewRequest(http.MethodGet, "/v1/incidents/"+created.IncidentID, nil)
	getReq2.SetPathValue("incidentID", created.IncidentID)
	getRec2 := httptest.NewRecorder()
	srv.handleGet(getRec2, getReq2)
	var after audit.Incident
	if err := json.NewDecoder(getRec2.Body).Decode(&after); err != nil {
		t.Fatalf("decode second get response: %v", err)
	}
	if after.Status != "resolved" || after.Attribution != "oom-kill" {
		t.Errorf("after patch: status=%q attribution=%q, want resolved/oom-kill", after.Status, after.Attribution)
	}
	if after.Origin != "faulttest" {
		t.Errorf("origin = %q after unrelated patch, want unchanged faulttest", after.Origin)
	}
}

func TestIncidentHandlers_Get_NotFound(t *testing.T) {
	srv := newIncidentServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/incidents/inc_missing", nil)
	req.SetPathValue("incidentID", "inc_missing")
	rec := httptest.NewRecorder()
	srv.handleGet(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestIncidentHandlers_Update_NotFound(t *testing.T) {
	srv := newIncidentServer(t)
	body, _ := json.Marshal(map[string]any{"status": "resolved"})
	req := httptest.NewRequest(http.MethodPatch, "/v1/incidents/inc_missing", bytes.NewReader(body))
	req.SetPathValue("incidentID", "inc_missing")
	rec := httptest.NewRecorder()
	srv.handleUpdate(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestIncidentHandlers_Update_InvalidResolvedAt(t *testing.T) {
	srv := newIncidentServer(t)

	createReq := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader([]byte("{}")))
	createRec := httptest.NewRecorder()
	srv.handleCreate(createRec, createReq)
	var created audit.Incident
	json.NewDecoder(createRec.Body).Decode(&created) //nolint:errcheck

	body, _ := json.Marshal(map[string]any{"resolved_at": "not-a-timestamp"})
	req := httptest.NewRequest(http.MethodPatch, "/v1/incidents/"+created.IncidentID, bytes.NewReader(body))
	req.SetPathValue("incidentID", created.IncidentID)
	rec := httptest.NewRecorder()
	srv.handleUpdate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unparseable resolved_at", rec.Code)
	}
}

func TestIncidentHandlers_List_Filters(t *testing.T) {
	srv := newIncidentServer(t)

	for _, body := range []map[string]any{
		{"origin": "real"},
		{"origin": "faulttest"},
		{"origin": "faulttest"},
	} {
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(data))
		rec := httptest.NewRecorder()
		srv.handleCreate(rec, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/incidents?origin=faulttest", nil)
	rec := httptest.NewRecorder()
	srv.handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Incidents []audit.Incident `json:"incidents"`
		Count     int              `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("count = %d, want 2", result.Count)
	}
}

func TestIncidentHandlers_List_LimitQueryParam(t *testing.T) {
	srv := newIncidentServer(t)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader([]byte("{}")))
		rec := httptest.NewRecorder()
		srv.handleCreate(rec, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/incidents?limit=2", nil)
	rec := httptest.NewRecorder()
	srv.handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Incidents []audit.Incident `json:"incidents"`
		Count     int              `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("count = %d, want 2 (?limit=2 should be forwarded to the store)", result.Count)
	}

	// A non-numeric limit is ignored rather than rejected — falls back to the
	// store's default rather than erroring.
	reqBad := httptest.NewRequest(http.MethodGet, "/v1/incidents?limit=notanumber", nil)
	recBad := httptest.NewRecorder()
	srv.handleList(recBad, reqBad)
	if recBad.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a non-numeric limit (should be ignored, not rejected)", recBad.Code)
	}
	var resultBad struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(recBad.Body).Decode(&resultBad); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resultBad.Count != 5 {
		t.Errorf("count = %d, want all 5 when limit is unparseable", resultBad.Count)
	}
}

func TestIncidentHandlers_GetByEntryRunID(t *testing.T) {
	srv := newIncidentServer(t)

	body, _ := json.Marshal(map[string]any{"entry_run_id": "plr_findme"})
	createReq := httptest.NewRequest(http.MethodPost, "/v1/incidents", bytes.NewReader(body))
	createRec := httptest.NewRecorder()
	srv.handleCreate(createRec, createReq)
	var created audit.Incident
	json.NewDecoder(createRec.Body).Decode(&created) //nolint:errcheck

	req := httptest.NewRequest(http.MethodGet, "/v1/incidents/by-run/plr_findme", nil)
	req.SetPathValue("runID", "plr_findme")
	rec := httptest.NewRecorder()
	srv.handleGetByEntryRunID(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var got audit.Incident
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.IncidentID != created.IncidentID {
		t.Errorf("incident_id = %q, want %q", got.IncidentID, created.IncidentID)
	}

	notFoundReq := httptest.NewRequest(http.MethodGet, "/v1/incidents/by-run/plr_never", nil)
	notFoundReq.SetPathValue("runID", "plr_never")
	notFoundRec := httptest.NewRecorder()
	srv.handleGetByEntryRunID(notFoundRec, notFoundReq)
	if notFoundRec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", notFoundRec.Code)
	}
}
