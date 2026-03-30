package main

import (
	"bytes"
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
	return &playbookServer{store: ps}
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
