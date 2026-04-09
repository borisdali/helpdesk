package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"helpdesk/internal/audit"
)

// uploadServer handles HTTP endpoints for operator file uploads.
type uploadServer struct {
	store *audit.UploadStore
}

// handleCreate accepts a multipart file upload and stores it.
// POST /v1/uploads
func (s *uploadServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(audit.UploadMaxBytes); err != nil {
		http.Error(w, "failed to parse multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing 'file' field in multipart form", http.StatusBadRequest)
		return
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, audit.UploadMaxBytes+1))
	if err != nil {
		http.Error(w, "failed to read file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if int64(len(content)) > audit.UploadMaxBytes {
		http.Error(w, "file exceeds 50 MB limit", http.StatusRequestEntityTooLarge)
		return
	}

	upload, err := s.store.Store(r.Context(), header.Filename, content)
	if err != nil {
		slog.Error("failed to store upload", "filename", header.Filename, "err", err)
		http.Error(w, "failed to store upload", http.StatusInternalServerError)
		return
	}

	slog.Info("file uploaded", "upload_id", upload.UploadID, "filename", upload.Filename, "size", upload.Size)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(upload) //nolint:errcheck
}

// handleGet returns metadata for an upload (no content).
// GET /v1/uploads/{uploadID}
func (s *uploadServer) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uploadID")
	upload, err := s.store.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if upload == nil {
		http.Error(w, "upload not found or expired", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(upload) //nolint:errcheck
}

// handleGetContent returns the raw file content.
// GET /v1/uploads/{uploadID}/content
func (s *uploadServer) handleGetContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uploadID")
	content, filename, err := s.store.GetContent(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if content == nil {
		http.Error(w, "upload not found or expired", http.StatusNotFound)
		return
	}

	ct := "text/plain; charset=utf-8"
	if strings.HasSuffix(filename, ".csv") {
		ct = "text/csv"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	w.Write(content) //nolint:errcheck
}
