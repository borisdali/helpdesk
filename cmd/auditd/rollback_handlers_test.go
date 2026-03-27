package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// newRollbackTestServer returns a rollbackServer backed by fresh in-process stores.
func newRollbackTestServer(t *testing.T) (*rollbackServer, *audit.Store) {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "rollback_handler_test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	rbkStore, err := audit.NewRollbackStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewRollbackStore: %v", err)
	}
	fleetStore, err := audit.NewFleetStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewFleetStore: %v", err)
	}
	return &rollbackServer{store: rbkStore, auditStore: store, fleetStore: fleetStore}, store
}

// recordScaleEvent records a tool_execution event with a ScalePreState and returns its ID.
func recordScaleEvent(t *testing.T, store *audit.Store, previousReplicas int) string {
	t.Helper()
	pre, _ := json.Marshal(audit.ScalePreState{
		Namespace:        "production",
		DeploymentName:   "api",
		PreviousReplicas: previousReplicas,
	})
	event := &audit.Event{
		EventID:     "tool_test_" + t.Name()[:8],
		EventType:   audit.EventTypeToolExecution,
		TraceID:     "tr_test01",
		ActionClass: audit.ActionDestructive,
		Session:     audit.Session{ID: "sess_test"},
		Timestamp:   time.Now().UTC(),
		Tool: &audit.ToolExecution{
			Name:     "scale_deployment",
			PreState: pre,
		},
		Outcome: &audit.Outcome{Status: "success"},
	}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatalf("Record scale event: %v", err)
	}
	return event.EventID
}

// recordIrreversibleEvent records a tool_execution event for restart_deployment (no rollback).
func recordIrreversibleEvent(t *testing.T, store *audit.Store) string {
	t.Helper()
	event := &audit.Event{
		EventID:     "tool_irrev_" + t.Name()[:6],
		EventType:   audit.EventTypeToolExecution,
		TraceID:     "tr_irrev01",
		ActionClass: audit.ActionDestructive,
		Session:     audit.Session{ID: "sess_irrev"},
		Timestamp:   time.Now().UTC(),
		Tool:        &audit.ToolExecution{Name: "restart_deployment"},
		Outcome:     &audit.Outcome{Status: "success"},
	}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatalf("Record irreversible event: %v", err)
	}
	return event.EventID
}

// --- handleDeriveRollbackPlan ---

func TestRollbackHandlers_DerivePlan_OK(t *testing.T) {
	srv, store := newRollbackTestServer(t)
	eventID := recordScaleEvent(t, store, 3)

	req := httptest.NewRequest(http.MethodPost, "/v1/events/"+eventID+"/rollback-plan", nil)
	req.SetPathValue("eventID", eventID)
	w := httptest.NewRecorder()
	srv.handleDeriveRollbackPlan(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var plan audit.RollbackPlan
	if err := json.NewDecoder(w.Body).Decode(&plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if plan.Reversibility != audit.ReversibilityYes {
		t.Errorf("Reversibility = %q, want yes", plan.Reversibility)
	}
	if plan.InverseOp == nil {
		t.Fatal("InverseOp is nil")
	}
}

func TestRollbackHandlers_DerivePlan_EventNotFound(t *testing.T) {
	srv, _ := newRollbackTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/events/tool_ghost/rollback-plan", nil)
	req.SetPathValue("eventID", "tool_ghost")
	w := httptest.NewRecorder()
	srv.handleDeriveRollbackPlan(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- handleInitiateRollback ---

func TestRollbackHandlers_Initiate_DryRun(t *testing.T) {
	srv, store := newRollbackTestServer(t)
	eventID := recordScaleEvent(t, store, 3)

	body, _ := json.Marshal(map[string]any{
		"original_event_id": eventID,
		"dry_run":           true,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/rollbacks", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleInitiateRollback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (dry run); body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp["dry_run"] != true {
		t.Error("expected dry_run=true in response")
	}
	if resp["plan"] == nil {
		t.Error("expected plan in dry-run response")
	}

	// Verify nothing was persisted.
	existing, err := srv.store.GetRollbackByEventID(context.Background(), eventID)
	if err != nil {
		t.Fatalf("GetRollbackByEventID: %v", err)
	}
	if existing != nil {
		t.Error("dry-run should not persist a rollback record")
	}
}

func TestRollbackHandlers_Initiate_Success(t *testing.T) {
	srv, store := newRollbackTestServer(t)
	eventID := recordScaleEvent(t, store, 3)

	body, _ := json.Marshal(map[string]any{
		"original_event_id": eventID,
		"justification":     "wrong scale",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/rollbacks", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleInitiateRollback(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]json.RawMessage
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp["rollback"] == nil {
		t.Error("expected rollback in response")
	}

	// Verify record was persisted.
	existing, err := srv.store.GetRollbackByEventID(context.Background(), eventID)
	if err != nil {
		t.Fatalf("GetRollbackByEventID: %v", err)
	}
	if existing == nil {
		t.Fatal("rollback record not found after initiation")
	}
	if existing.Status != "pending_approval" {
		t.Errorf("status = %q, want pending_approval", existing.Status)
	}
}

func TestRollbackHandlers_Initiate_NotReversible_Returns422(t *testing.T) {
	srv, store := newRollbackTestServer(t)
	eventID := recordIrreversibleEvent(t, store)

	body, _ := json.Marshal(map[string]any{"original_event_id": eventID})
	req := httptest.NewRequest(http.MethodPost, "/v1/rollbacks", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleInitiateRollback(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for non-reversible op; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if !strings.Contains(resp["error"].(string), "not reversible") {
		t.Errorf("error = %q, want 'not reversible'", resp["error"])
	}
}

func TestRollbackHandlers_Initiate_Duplicate_Returns409(t *testing.T) {
	srv, store := newRollbackTestServer(t)
	eventID := recordScaleEvent(t, store, 3)

	body, _ := json.Marshal(map[string]any{"original_event_id": eventID})

	// First initiation succeeds.
	req1 := httptest.NewRequest(http.MethodPost, "/v1/rollbacks", bytes.NewReader(body))
	w1 := httptest.NewRecorder()
	srv.handleInitiateRollback(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first initiation: status = %d, want 201; body: %s", w1.Code, w1.Body.String())
	}

	// Second initiation for the same event must return 409.
	body, _ = json.Marshal(map[string]any{"original_event_id": eventID})
	req2 := httptest.NewRequest(http.MethodPost, "/v1/rollbacks", bytes.NewReader(body))
	w2 := httptest.NewRecorder()
	srv.handleInitiateRollback(w2, req2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("duplicate initiation: status = %d, want 409; body: %s", w2.Code, w2.Body.String())
	}
}

func TestRollbackHandlers_Initiate_MissingEventID_Returns400(t *testing.T) {
	srv, _ := newRollbackTestServer(t)

	body, _ := json.Marshal(map[string]any{"justification": "oops"})
	req := httptest.NewRequest(http.MethodPost, "/v1/rollbacks", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleInitiateRollback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- handleListRollbacks ---

func TestRollbackHandlers_List_Empty(t *testing.T) {
	srv, _ := newRollbackTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/rollbacks", nil)
	w := httptest.NewRecorder()
	srv.handleListRollbacks(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var records []*audit.RollbackRecord
	json.NewDecoder(w.Body).Decode(&records) //nolint:errcheck
	if len(records) != 0 {
		t.Errorf("expected empty list, got %d records", len(records))
	}
}

// --- handleGetRollback ---

func TestRollbackHandlers_Get_OK(t *testing.T) {
	srv, store := newRollbackTestServer(t)
	eventID := recordScaleEvent(t, store, 5)

	// Create a rollback record first.
	rbk := &audit.RollbackRecord{
		OriginalEventID: eventID,
		InitiatedBy:     "alice",
		PlanJSON:        `{"reversibility":"yes"}`,
	}
	if err := srv.store.CreateRollback(context.Background(), rbk); err != nil {
		t.Fatalf("CreateRollback: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/rollbacks/"+rbk.RollbackID, nil)
	req.SetPathValue("rollbackID", rbk.RollbackID)
	w := httptest.NewRecorder()
	srv.handleGetRollback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]json.RawMessage
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp["rollback"] == nil {
		t.Error("expected rollback field in response")
	}
}

func TestRollbackHandlers_Get_NotFound(t *testing.T) {
	srv, _ := newRollbackTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/rollbacks/rbk_ghost11", nil)
	req.SetPathValue("rollbackID", "rbk_ghost11")
	w := httptest.NewRecorder()
	srv.handleGetRollback(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- handleCancelRollback ---

func TestRollbackHandlers_Cancel_OK(t *testing.T) {
	srv, store := newRollbackTestServer(t)
	eventID := recordScaleEvent(t, store, 3)

	// Create a pending rollback.
	rbk := &audit.RollbackRecord{
		OriginalEventID: eventID,
		InitiatedBy:     "alice",
		Status:          "pending_approval",
		PlanJSON:        `{}`,
	}
	srv.store.CreateRollback(context.Background(), rbk) //nolint:errcheck

	req := httptest.NewRequest(http.MethodPost, "/v1/rollbacks/"+rbk.RollbackID+"/cancel", nil)
	req.SetPathValue("rollbackID", rbk.RollbackID)
	w := httptest.NewRecorder()
	srv.handleCancelRollback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	got, _ := srv.store.GetRollback(context.Background(), rbk.RollbackID)
	if got.Status != "cancelled" {
		t.Errorf("status after cancel = %q, want cancelled", got.Status)
	}
}

func TestRollbackHandlers_Cancel_AlreadyTerminal_Returns409(t *testing.T) {
	srv, store := newRollbackTestServer(t)
	eventID := recordScaleEvent(t, store, 3)

	rbk := &audit.RollbackRecord{
		OriginalEventID: eventID,
		InitiatedBy:     "alice",
		Status:          "success",
		PlanJSON:        `{}`,
	}
	srv.store.CreateRollback(context.Background(), rbk) //nolint:errcheck

	req := httptest.NewRequest(http.MethodPost, "/v1/rollbacks/"+rbk.RollbackID+"/cancel", nil)
	req.SetPathValue("rollbackID", rbk.RollbackID)
	w := httptest.NewRecorder()
	srv.handleCancelRollback(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}
