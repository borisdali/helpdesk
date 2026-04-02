package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"helpdesk/internal/audit"
)

func newUploadServer(t *testing.T) *uploadServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	us, err := audit.NewUploadStore(store.DB())
	if err != nil {
		t.Fatalf("NewUploadStore: %v", err)
	}
	return &uploadServer{store: us}
}

// buildMultipart creates a multipart/form-data request body with a single "file" field.
func buildMultipart(t *testing.T, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	fw.Write([]byte(content)) //nolint:errcheck
	w.Close()
	return &buf, w.FormDataContentType()
}

// --- handleCreate ---

func TestUploadHandlers_Create_OK(t *testing.T) {
	srv := newUploadServer(t)
	logContent := "2024-01-15 10:00:00 UTC: LOG: database system is ready\n"
	body, ct := buildMultipart(t, "postgresql-2024.log", logContent)

	req := httptest.NewRequest(http.MethodPost, "/v1/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	srv.handleCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("handleCreate status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var u audit.Upload
	if err := json.NewDecoder(w.Body).Decode(&u); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.HasPrefix(u.UploadID, "ul_") {
		t.Errorf("upload_id = %q, want ul_ prefix", u.UploadID)
	}
	if u.Filename != "postgresql-2024.log" {
		t.Errorf("filename = %q, want postgresql-2024.log", u.Filename)
	}
	if u.Size != int64(len(logContent)) {
		t.Errorf("size = %d, want %d", u.Size, len(logContent))
	}
}

func TestUploadHandlers_Create_MissingFile(t *testing.T) {
	srv := newUploadServer(t)
	// Send an empty multipart body (no "file" field).
	var buf bytes.Buffer
	w2 := multipart.NewWriter(&buf)
	w2.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/uploads", &buf)
	req.Header.Set("Content-Type", w2.FormDataContentType())
	w := httptest.NewRecorder()
	srv.handleCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("handleCreate status = %d, want 400", w.Code)
	}
}

func TestUploadHandlers_Create_NotMultipart(t *testing.T) {
	srv := newUploadServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/uploads", strings.NewReader("raw body"))
	req.Header.Set("Content-Type", "application/octet-stream")
	w := httptest.NewRecorder()
	srv.handleCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("handleCreate status = %d, want 400 for non-multipart body", w.Code)
	}
}

// --- handleGet ---

func TestUploadHandlers_Get_OK(t *testing.T) {
	srv := newUploadServer(t)
	body, ct := buildMultipart(t, "pg.log", "some log content")
	req := httptest.NewRequest(http.MethodPost, "/v1/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	srv.handleCreate(w, req)

	var created audit.Upload
	json.NewDecoder(w.Body).Decode(&created) //nolint:errcheck

	req2 := httptest.NewRequest(http.MethodGet, "/v1/uploads/"+created.UploadID, nil)
	req2.SetPathValue("uploadID", created.UploadID)
	w2 := httptest.NewRecorder()
	srv.handleGet(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("handleGet status = %d, want 200", w2.Code)
	}
	var got audit.Upload
	if err := json.NewDecoder(w2.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UploadID != created.UploadID {
		t.Errorf("upload_id = %q, want %q", got.UploadID, created.UploadID)
	}
}

func TestUploadHandlers_Get_NotFound(t *testing.T) {
	srv := newUploadServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/uploads/ul_notexist", nil)
	req.SetPathValue("uploadID", "ul_notexist")
	w := httptest.NewRecorder()
	srv.handleGet(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("handleGet status = %d, want 404", w.Code)
	}
}

// --- handleGetContent ---

func TestUploadHandlers_GetContent_OK(t *testing.T) {
	srv := newUploadServer(t)
	logContent := "FATAL: invalid value for parameter\nLOG: database system is ready\n"
	body, ct := buildMultipart(t, "pg.log", logContent)
	req := httptest.NewRequest(http.MethodPost, "/v1/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	srv.handleCreate(w, req)

	var created audit.Upload
	json.NewDecoder(w.Body).Decode(&created) //nolint:errcheck

	req2 := httptest.NewRequest(http.MethodGet, "/v1/uploads/"+created.UploadID+"/content", nil)
	req2.SetPathValue("uploadID", created.UploadID)
	w2 := httptest.NewRecorder()
	srv.handleGetContent(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("handleGetContent status = %d, want 200", w2.Code)
	}
	if got := w2.Body.String(); got != logContent {
		t.Errorf("content = %q, want %q", got, logContent)
	}
	if ct := w2.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

func TestUploadHandlers_GetContent_NotFound(t *testing.T) {
	srv := newUploadServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/uploads/ul_ghost/content", nil)
	req.SetPathValue("uploadID", "ul_ghost")
	w := httptest.NewRecorder()
	srv.handleGetContent(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("handleGetContent status = %d, want 404", w.Code)
	}
}
