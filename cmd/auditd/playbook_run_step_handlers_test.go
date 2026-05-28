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

func newRunStepHandlerServer(t *testing.T) *playbookRunStepServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	stepStore, err := audit.NewPlaybookRunStepStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookRunStepStore: %v", err)
	}
	return &playbookRunStepServer{store: stepStore}
}

func doCreateStep(t *testing.T, srv *playbookRunStepServer, runID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/playbook-runs/"+runID+"/steps", bytes.NewReader(data))
	req.SetPathValue("runID", runID)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleCreateStep(rec, req)
	return rec
}

func TestPlaybookRunStepHandlers_Create_OK(t *testing.T) {
	srv := newRunStepHandlerServer(t)

	rec := doCreateStep(t, srv, "plr_create01", map[string]any{
		"agent":  "database",
		"tool":   "terminate_connection",
		"args":   map[string]any{"pid": 1234},
		"reason": "Terminate root blocker",
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var step audit.PlaybookRunStep
	if err := json.NewDecoder(rec.Body).Decode(&step); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if step.Tool != "terminate_connection" {
		t.Errorf("tool = %q, want terminate_connection", step.Tool)
	}
	if step.StepIndex == 0 {
		t.Error("step_index should be auto-assigned (non-zero)")
	}
}

func TestPlaybookRunStepHandlers_Create_MissingAgentTool(t *testing.T) {
	srv := newRunStepHandlerServer(t)

	rec := doCreateStep(t, srv, "plr_bad01", map[string]any{"agent": "database"}) // no tool
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when tool is missing", rec.Code)
	}

	rec = doCreateStep(t, srv, "plr_bad02", map[string]any{"tool": "terminate_connection"}) // no agent
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when agent is missing", rec.Code)
	}
}

func TestPlaybookRunStepHandlers_Create_AutoAssignsIndex(t *testing.T) {
	srv := newRunStepHandlerServer(t)
	runID := "plr_auto01"

	// Create first step (no step_index supplied).
	rec := doCreateStep(t, srv, runID, map[string]any{"agent": "database", "tool": "get_blocking_queries"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("step 1: status = %d", rec.Code)
	}
	var s1 audit.PlaybookRunStep
	json.NewDecoder(rec.Body).Decode(&s1) //nolint:errcheck

	// Create second step — should be index 2.
	rec = doCreateStep(t, srv, runID, map[string]any{"agent": "database", "tool": "terminate_connection"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("step 2: status = %d", rec.Code)
	}
	var s2 audit.PlaybookRunStep
	json.NewDecoder(rec.Body).Decode(&s2) //nolint:errcheck

	if s1.StepIndex != 1 {
		t.Errorf("first step_index = %d, want 1", s1.StepIndex)
	}
	if s2.StepIndex != 2 {
		t.Errorf("second step_index = %d, want 2", s2.StepIndex)
	}
}

func TestPlaybookRunStepHandlers_Update_OK(t *testing.T) {
	srv := newRunStepHandlerServer(t)
	runID := "plr_upd01"

	doCreateStep(t, srv, runID, map[string]any{"agent": "database", "tool": "terminate_connection"}) //nolint:errcheck

	body, _ := json.Marshal(map[string]string{
		"status":      "succeeded",
		"approval_id": "apr_xyz",
		"result":      "terminated 1 connection",
	})
	req := httptest.NewRequest(http.MethodPatch, "/v1/fleet/playbook-runs/"+runID+"/steps/1", bytes.NewReader(body))
	req.SetPathValue("runID", runID)
	req.SetPathValue("stepIndex", "1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleUpdateStep(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
}

func TestPlaybookRunStepHandlers_Update_MissingStatus(t *testing.T) {
	srv := newRunStepHandlerServer(t)

	body, _ := json.Marshal(map[string]string{"result": "ok"})
	req := httptest.NewRequest(http.MethodPatch, "/v1/fleet/playbook-runs/plr_x/steps/1", bytes.NewReader(body))
	req.SetPathValue("runID", "plr_x")
	req.SetPathValue("stepIndex", "1")
	rec := httptest.NewRecorder()
	srv.handleUpdateStep(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when status is missing", rec.Code)
	}
}

func TestPlaybookRunStepHandlers_List_OK(t *testing.T) {
	srv := newRunStepHandlerServer(t)
	runID := "plr_list01"

	doCreateStep(t, srv, runID, map[string]any{"agent": "database", "tool": "get_blocking_queries"})   //nolint:errcheck
	doCreateStep(t, srv, runID, map[string]any{"agent": "database", "tool": "terminate_connection"})    //nolint:errcheck

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/"+runID+"/steps", nil)
	req.SetPathValue("runID", runID)
	rec := httptest.NewRecorder()
	srv.handleListSteps(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Steps []audit.PlaybookRunStep `json:"steps"`
		Count int                     `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 2 {
		t.Errorf("count = %d, want 2", resp.Count)
	}
}

func TestPlaybookRunStepHandlers_List_Empty(t *testing.T) {
	srv := newRunStepHandlerServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_empty/steps", nil)
	req.SetPathValue("runID", "plr_empty")
	rec := httptest.NewRecorder()
	srv.handleListSteps(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Count int `json:"count"`
	}
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp.Count != 0 {
		t.Errorf("count = %d, want 0", resp.Count)
	}
}

func TestPlaybookRunStepHandlers_GetPending_Found(t *testing.T) {
	srv := newRunStepHandlerServer(t)
	runID := "plr_pending01"

	doCreateStep(t, srv, runID, map[string]any{"agent": "database", "tool": "terminate_connection"}) //nolint:errcheck

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/"+runID+"/pending-step", nil)
	req.SetPathValue("runID", runID)
	rec := httptest.NewRecorder()
	srv.handleGetPendingStep(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var step audit.PlaybookRunStep
	if err := json.NewDecoder(rec.Body).Decode(&step); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if step.Tool != "terminate_connection" {
		t.Errorf("tool = %q, want terminate_connection", step.Tool)
	}
}

func TestPlaybookRunStepHandlers_GetPending_NotFound(t *testing.T) {
	srv := newRunStepHandlerServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbook-runs/plr_empty/pending-step", nil)
	req.SetPathValue("runID", "plr_empty")
	rec := httptest.NewRecorder()
	srv.handleGetPendingStep(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
