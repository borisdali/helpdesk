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

	"helpdesk/internal/audit"
)

// newPlaybookServer returns a playbookServer backed by a fresh temp-dir SQLite store.
func newPlaybookServer(t *testing.T) *playbookServer {
	t.Helper()
	srv, _ := newPlaybookServerWithRuns(t)
	return srv
}

// newPlaybookServerWithRuns returns a playbookServer wired with both the playbook
// store and the run store so that inline stats are populated on handleList.
// The second return value is the run store for seeding test data.
func newPlaybookServerWithRuns(t *testing.T) (*playbookServer, *audit.PlaybookRunStore) {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ps, err := audit.NewPlaybookStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewPlaybookStore: %v", err)
	}
	rs, err := audit.NewPlaybookRunStore(store.DB())
	if err != nil {
		t.Fatalf("NewPlaybookRunStore: %v", err)
	}
	return &playbookServer{store: ps, runStore: rs}, rs
}

// createPlaybookViaHandler POSTs to handleCreate and returns the decoded response playbook.
func createPlaybookViaHandler(t *testing.T, srv *playbookServer, body any) *audit.Playbook {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/playbooks", bytes.NewReader(data))
	w := httptest.NewRecorder()
	srv.handleCreate(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("handleCreate: status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var pb audit.Playbook
	if err := json.NewDecoder(w.Body).Decode(&pb); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return &pb
}

// --- handleCreate ---

func TestPlaybookHandlers_Create_OK(t *testing.T) {
	srv := newPlaybookServer(t)

	pb := createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "vacuum-runbook",
		"description": "Describes how to vacuum tables",
	})

	if pb.PlaybookID == "" {
		t.Fatal("expected non-empty playbook_id")
	}
	if !strings.HasPrefix(pb.PlaybookID, "pb_") {
		t.Errorf("playbook_id = %q, want pb_ prefix", pb.PlaybookID)
	}
	if pb.Name != "vacuum-runbook" {
		t.Errorf("name = %q, want vacuum-runbook", pb.Name)
	}
}

func TestPlaybookHandlers_Create_WithKnowledgeFields(t *testing.T) {
	srv := newPlaybookServer(t)

	pb := createPlaybookViaHandler(t, srv, map[string]any{
		"name":              "bloat-remediation",
		"description":       "Handles table bloat incidents",
		"problem_class":     "performance",
		"symptoms":          []string{"high dead tuples", "slow queries"},
		"guidance":          "Run VACUUM ANALYZE; check autovacuum settings.",
		"escalation":        []string{"DBA on call if bloat > 50%"},
		"related_playbooks": []string{"pb_abc123"},
		"author":            "alice",
		"version":           "1.0",
	})

	if !strings.HasPrefix(pb.PlaybookID, "pb_") {
		t.Errorf("playbook_id = %q, want pb_ prefix", pb.PlaybookID)
	}
	if pb.ProblemClass != "performance" {
		t.Errorf("problem_class = %q, want performance", pb.ProblemClass)
	}
	if len(pb.Symptoms) != 2 {
		t.Errorf("symptoms len = %d, want 2", len(pb.Symptoms))
	}
	if pb.Guidance != "Run VACUUM ANALYZE; check autovacuum settings." {
		t.Errorf("guidance = %q", pb.Guidance)
	}
	if len(pb.Escalation) != 1 {
		t.Errorf("escalation len = %d, want 1", len(pb.Escalation))
	}
	if len(pb.RelatedPlaybooks) != 1 || pb.RelatedPlaybooks[0] != "pb_abc123" {
		t.Errorf("related_playbooks = %v, want [pb_abc123]", pb.RelatedPlaybooks)
	}
	if pb.Author != "alice" {
		t.Errorf("author = %q, want alice", pb.Author)
	}
	if pb.Version != "1.0" {
		t.Errorf("version = %q, want 1.0", pb.Version)
	}
}

func TestPlaybookHandlers_Create_MissingName(t *testing.T) {
	srv := newPlaybookServer(t)

	data, _ := json.Marshal(map[string]any{"description": "some description"})
	req := httptest.NewRequest(http.MethodPost, "/v1/playbooks", bytes.NewReader(data))
	w := httptest.NewRecorder()
	srv.handleCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPlaybookHandlers_Create_MissingDescription(t *testing.T) {
	srv := newPlaybookServer(t)

	data, _ := json.Marshal(map[string]any{"name": "some-name"})
	req := httptest.NewRequest(http.MethodPost, "/v1/playbooks", bytes.NewReader(data))
	w := httptest.NewRecorder()
	srv.handleCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- handleGet ---

func TestPlaybookHandlers_Get_OK(t *testing.T) {
	srv := newPlaybookServer(t)
	created := createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "get-test",
		"description": "playbook for get test",
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/playbooks/"+created.PlaybookID, nil)
	req.SetPathValue("playbookID", created.PlaybookID)
	w := httptest.NewRecorder()
	srv.handleGet(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got audit.Playbook
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Name != "get-test" {
		t.Errorf("name = %q, want get-test", got.Name)
	}
	if got.PlaybookID != created.PlaybookID {
		t.Errorf("playbook_id = %q, want %q", got.PlaybookID, created.PlaybookID)
	}
}

func TestPlaybookHandlers_Get_NotFound(t *testing.T) {
	srv := newPlaybookServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/playbooks/pb_nonexistent", nil)
	req.SetPathValue("playbookID", "pb_nonexistent")
	w := httptest.NewRecorder()
	srv.handleGet(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- handleList ---

func TestPlaybookHandlers_List_Empty(t *testing.T) {
	srv := newPlaybookServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/playbooks", nil)
	w := httptest.NewRecorder()
	srv.handleList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"playbooks":[]`) {
		t.Errorf("body does not contain empty playbooks list; got: %s", body)
	}
}

func TestPlaybookHandlers_List_WithItems(t *testing.T) {
	srv := newPlaybookServer(t)

	createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "playbook-alpha",
		"description": "first playbook",
	})
	createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "playbook-beta",
		"description": "second playbook",
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/playbooks", nil)
	w := httptest.NewRecorder()
	srv.handleList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result struct {
		Playbooks []*audit.Playbook `json:"playbooks"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Playbooks) != 2 {
		t.Errorf("expected 2 playbooks, got %d", len(result.Playbooks))
	}

	names := map[string]bool{}
	for _, pb := range result.Playbooks {
		names[pb.Name] = true
	}
	if !names["playbook-alpha"] || !names["playbook-beta"] {
		t.Errorf("expected both playbooks in list; got names: %v", names)
	}
}

// --- handleUpdate ---

func TestPlaybookHandlers_Update_OK(t *testing.T) {
	srv := newPlaybookServer(t)
	created := createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "original-name",
		"description": "original description",
	})

	data, _ := json.Marshal(map[string]any{
		"name":        "updated-name",
		"description": "updated description",
		"guidance":    "new expert guidance",
	})
	req := httptest.NewRequest(http.MethodPut, "/v1/playbooks/"+created.PlaybookID, bytes.NewReader(data))
	req.SetPathValue("playbookID", created.PlaybookID)
	w := httptest.NewRecorder()
	srv.handleUpdate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var updated audit.Playbook
	if err := json.NewDecoder(w.Body).Decode(&updated); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if updated.Name != "updated-name" {
		t.Errorf("name = %q, want updated-name", updated.Name)
	}
	if updated.Guidance != "new expert guidance" {
		t.Errorf("guidance = %q, want 'new expert guidance'", updated.Guidance)
	}
	if updated.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set")
	}
}

func TestPlaybookHandlers_Update_NotFound(t *testing.T) {
	srv := newPlaybookServer(t)

	data, _ := json.Marshal(map[string]any{
		"name":        "some-name",
		"description": "some description",
	})
	req := httptest.NewRequest(http.MethodPut, "/v1/playbooks/pb_nonexistent", bytes.NewReader(data))
	req.SetPathValue("playbookID", "pb_nonexistent")
	w := httptest.NewRecorder()
	srv.handleUpdate(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestPlaybookHandlers_Update_MissingName(t *testing.T) {
	srv := newPlaybookServer(t)
	created := createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "original-name",
		"description": "original description",
	})

	data, _ := json.Marshal(map[string]any{"description": "updated description"})
	req := httptest.NewRequest(http.MethodPut, "/v1/playbooks/"+created.PlaybookID, bytes.NewReader(data))
	req.SetPathValue("playbookID", created.PlaybookID)
	w := httptest.NewRecorder()
	srv.handleUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPlaybookHandlers_Update_SystemPlaybook(t *testing.T) {
	srv := newPlaybookServer(t)

	sys := &audit.Playbook{
		Name:        "system-vacuum",
		Description: "System vacuum playbook",
		IsSystem:    true,
		Source:      "system",
	}
	if err := srv.store.Create(context.Background(), sys); err != nil {
		t.Fatalf("Create: %v", err)
	}

	data, _ := json.Marshal(map[string]any{
		"name":        "hacked-name",
		"description": "attempted override",
	})
	req := httptest.NewRequest(http.MethodPut, "/v1/playbooks/"+sys.PlaybookID, bytes.NewReader(data))
	req.SetPathValue("playbookID", sys.PlaybookID)
	w := httptest.NewRecorder()
	srv.handleUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- handleDelete ---

func TestPlaybookHandlers_Delete_SystemPlaybook(t *testing.T) {
	srv := newPlaybookServer(t)

	sys := &audit.Playbook{
		Name:        "system-vacuum",
		Description: "System vacuum playbook",
		IsSystem:    true,
		Source:      "system",
	}
	if err := srv.store.Create(context.Background(), sys); err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/playbooks/"+sys.PlaybookID, nil)
	req.SetPathValue("playbookID", sys.PlaybookID)
	w := httptest.NewRecorder()
	srv.handleDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- handleList query params ---

func TestPlaybookHandlers_List_ActiveOnly(t *testing.T) {
	srv := newPlaybookServer(t)

	// Create two versions in the same series; v2 is inactive.
	v1 := createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "my-playbook v1",
		"description": "version one",
	})
	createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "my-playbook v2",
		"description": "version two",
		"series_id":   v1.SeriesID,
	})

	// Default (active_only=true) → only v1.
	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks", nil)
	w := httptest.NewRecorder()
	srv.handleList(w, req)

	var result struct {
		Playbooks []*audit.Playbook `json:"playbooks"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Playbooks) != 1 {
		t.Errorf("default list: got %d playbooks, want 1 (active only)", len(result.Playbooks))
	}

	// active_only=false → both versions.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks?active_only=false", nil)
	w2 := httptest.NewRecorder()
	srv.handleList(w2, req2)

	var result2 struct {
		Playbooks []*audit.Playbook `json:"playbooks"`
	}
	if err := json.NewDecoder(w2.Body).Decode(&result2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result2.Playbooks) != 2 {
		t.Errorf("active_only=false: got %d playbooks, want 2", len(result2.Playbooks))
	}
}

func TestPlaybookHandlers_List_IncludeSystem(t *testing.T) {
	srv := newPlaybookServer(t)

	// Insert a system playbook directly.
	sys := &audit.Playbook{
		Name:        "system-vacuum",
		Description: "System vacuum playbook",
		IsSystem:    true,
		IsActive:    true,
		Source:      "system",
	}
	if err := srv.store.Create(context.Background(), sys); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Also create a normal playbook.
	createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "user-playbook",
		"description": "user-authored",
	})

	// Default (include_system=true) → both.
	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks", nil)
	w := httptest.NewRecorder()
	srv.handleList(w, req)

	var result struct {
		Playbooks []*audit.Playbook `json:"playbooks"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Playbooks) != 2 {
		t.Errorf("default list: got %d playbooks, want 2", len(result.Playbooks))
	}

	// include_system=false → only the user playbook.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks?include_system=false", nil)
	w2 := httptest.NewRecorder()
	srv.handleList(w2, req2)

	var result2 struct {
		Playbooks []*audit.Playbook `json:"playbooks"`
	}
	if err := json.NewDecoder(w2.Body).Decode(&result2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result2.Playbooks) != 1 {
		t.Errorf("include_system=false: got %d playbooks, want 1", len(result2.Playbooks))
	}
	if result2.Playbooks[0].IsSystem {
		t.Error("expected no system playbooks when include_system=false")
	}
}

func TestPlaybookHandlers_List_SeriesID(t *testing.T) {
	srv := newPlaybookServer(t)

	// Two unrelated playbooks (each in their own series).
	pb1 := createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "alpha",
		"description": "alpha playbook",
	})
	createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "beta",
		"description": "beta playbook",
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/playbooks?series_id="+pb1.SeriesID, nil)
	w := httptest.NewRecorder()
	srv.handleList(w, req)

	var result struct {
		Playbooks []*audit.Playbook `json:"playbooks"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Playbooks) != 1 {
		t.Errorf("series_id filter: got %d playbooks, want 1", len(result.Playbooks))
	}
	if result.Playbooks[0].SeriesID != pb1.SeriesID {
		t.Errorf("series_id = %q, want %q", result.Playbooks[0].SeriesID, pb1.SeriesID)
	}
}

// --- handleActivate ---

func TestHandleActivate_Success(t *testing.T) {
	srv := newPlaybookServer(t)

	// Create two versions in the same series.
	v1 := createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "my-playbook v1",
		"description": "version one",
	})
	// v1 got a series_id auto-generated; create v2 in the same series.
	v2 := createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "my-playbook v2",
		"description": "version two",
		"series_id":   v1.SeriesID,
	})

	// v2 was created with an explicit series_id so IsActive defaults to false.
	// Activate v2.
	req := httptest.NewRequest(http.MethodPost, "/v1/playbooks/"+v2.PlaybookID+"/activate", nil)
	req.SetPathValue("playbookID", v2.PlaybookID)
	w := httptest.NewRecorder()
	srv.handleActivate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("handleActivate: status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var activated audit.Playbook
	if err := json.NewDecoder(w.Body).Decode(&activated); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !activated.IsActive {
		t.Error("expected activated playbook to have is_active=true")
	}
	if activated.PlaybookID != v2.PlaybookID {
		t.Errorf("playbook_id = %q, want %q", activated.PlaybookID, v2.PlaybookID)
	}

	// Verify v1 is now inactive via GET.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/playbooks/"+v1.PlaybookID, nil)
	getReq.SetPathValue("playbookID", v1.PlaybookID)
	getW := httptest.NewRecorder()
	srv.handleGet(getW, getReq)
	var v1Got audit.Playbook
	if err := json.NewDecoder(getW.Body).Decode(&v1Got); err != nil {
		t.Fatalf("decode v1: %v", err)
	}
	if v1Got.IsActive {
		t.Error("expected v1 to be inactive after activating v2")
	}
}

func TestHandleActivate_NotFound(t *testing.T) {
	srv := newPlaybookServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/playbooks/pb_nonexistent/activate", nil)
	req.SetPathValue("playbookID", "pb_nonexistent")
	w := httptest.NewRecorder()
	srv.handleActivate(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleActivate_SystemPlaybook(t *testing.T) {
	srv := newPlaybookServer(t)

	// Directly insert a system playbook into the store.
	sysPB := &audit.Playbook{
		Name:        "system-vacuum",
		Description: "System-provided vacuum playbook",
		IsSystem:    true,
		Source:      "system",
	}
	if err := srv.store.Create(context.Background(), sysPB); err != nil {
		t.Fatalf("Create system playbook: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/playbooks/"+sysPB.PlaybookID+"/activate", nil)
	req.SetPathValue("playbookID", sysPB.PlaybookID)
	w := httptest.NewRecorder()
	srv.handleActivate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleActivate_Idempotent(t *testing.T) {
	srv := newPlaybookServer(t)

	pb := createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "solo-playbook",
		"description": "only one in its series",
	})
	// Activate an already-active playbook — should succeed silently.
	req := httptest.NewRequest(http.MethodPost, "/v1/playbooks/"+pb.PlaybookID+"/activate", nil)
	req.SetPathValue("playbookID", pb.PlaybookID)
	w := httptest.NewRecorder()
	srv.handleActivate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// --- handleDelete ---

func TestPlaybookHandlers_Delete_OK(t *testing.T) {
	srv := newPlaybookServer(t)
	created := createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "to-be-deleted",
		"description": "this playbook will be deleted",
	})

	// Delete the playbook.
	delReq := httptest.NewRequest(http.MethodDelete, "/v1/playbooks/"+created.PlaybookID, nil)
	delReq.SetPathValue("playbookID", created.PlaybookID)
	delW := httptest.NewRecorder()
	srv.handleDelete(delW, delReq)

	if delW.Code != http.StatusNoContent {
		t.Fatalf("handleDelete: status = %d, want 204; body: %s", delW.Code, delW.Body.String())
	}

	// Verify it's gone via GET.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/playbooks/"+created.PlaybookID, nil)
	getReq.SetPathValue("playbookID", created.PlaybookID)
	getW := httptest.NewRecorder()
	srv.handleGet(getW, getReq)

	if getW.Code != http.StatusNotFound {
		t.Errorf("after delete: status = %d, want 404", getW.Code)
	}
}

// --- handleList with inline stats ---

func TestPlaybookHandlers_List_InlineStats(t *testing.T) {
	srv, runStore := newPlaybookServerWithRuns(t)
	ctx := context.Background()

	pb := createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "vacuum-triage",
		"description": "check vacuum status",
	})
	// Re-fetch to get the series_id assigned by the store.
	req := httptest.NewRequest(http.MethodGet, "/v1/playbooks/"+pb.PlaybookID, nil)
	req.SetPathValue("playbookID", pb.PlaybookID)
	w := httptest.NewRecorder()
	srv.handleGet(w, req)
	var fetched audit.Playbook
	if err := json.NewDecoder(w.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}

	// Seed two runs against the playbook's series.
	for _, outcome := range []string{"resolved", "escalated"} {
		if err := runStore.Record(ctx, &audit.PlaybookRun{
			PlaybookID:    fetched.PlaybookID,
			SeriesID:      fetched.SeriesID,
			ExecutionMode: "fleet",
			Outcome:       outcome,
		}); err != nil {
			t.Fatalf("Record run: %v", err)
		}
	}

	// List playbooks — stats should be inline.
	listReq := httptest.NewRequest(http.MethodGet, "/v1/playbooks", nil)
	listW := httptest.NewRecorder()
	srv.handleList(listW, listReq)

	if listW.Code != http.StatusOK {
		t.Fatalf("handleList: status = %d, want 200", listW.Code)
	}

	var result struct {
		Playbooks []*audit.Playbook `json:"playbooks"`
	}
	if err := json.NewDecoder(listW.Body).Decode(&result); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(result.Playbooks) != 1 {
		t.Fatalf("expected 1 playbook, got %d", len(result.Playbooks))
	}
	stats := result.Playbooks[0].Stats
	if stats == nil {
		t.Fatal("expected inline stats to be populated, got nil")
	}
	if stats.TotalRuns != 2 {
		t.Errorf("total_runs = %d, want 2", stats.TotalRuns)
	}
	if stats.Resolved != 1 {
		t.Errorf("resolved = %d, want 1", stats.Resolved)
	}
	if stats.Escalated != 1 {
		t.Errorf("escalated = %d, want 1", stats.Escalated)
	}
}

func TestPlaybookHandlers_List_InlineStats_NoRuns(t *testing.T) {
	// When a playbook has no runs, its Stats field should be omitted (nil).
	srv := newPlaybookServer(t)
	createPlaybookViaHandler(t, srv, map[string]any{
		"name":        "new-playbook",
		"description": "never run",
	})

	listReq := httptest.NewRequest(http.MethodGet, "/v1/playbooks", nil)
	listW := httptest.NewRecorder()
	srv.handleList(listW, listReq)

	var result struct {
		Playbooks []*audit.Playbook `json:"playbooks"`
	}
	json.NewDecoder(listW.Body).Decode(&result) //nolint:errcheck
	if len(result.Playbooks) != 1 {
		t.Fatalf("expected 1 playbook, got %d", len(result.Playbooks))
	}
	if result.Playbooks[0].Stats != nil {
		t.Error("Stats should be nil (omitempty) when the playbook has no runs")
	}
}
