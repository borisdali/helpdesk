package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"helpdesk/internal/audit"
)

// newFleetServer returns a fleetServer backed by a fresh temp-dir SQLite store.
func newFleetServer(t *testing.T) *fleetServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	fs, err := audit.NewFleetStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewFleetStore: %v", err)
	}
	return &fleetServer{store: fs}
}

// createJobViaHandler POSTs to handleCreateJob and returns the decoded response job.
func createJobViaHandler(t *testing.T, srv *fleetServer, body any) *audit.FleetJob {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/jobs", bytes.NewReader(data))
	w := httptest.NewRecorder()
	srv.handleCreateJob(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("handleCreateJob: status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var job audit.FleetJob
	if err := json.NewDecoder(w.Body).Decode(&job); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return &job
}

// --- handleCreateJob ---

func TestFleetHandlers_CreateJob_OK(t *testing.T) {
	srv := newFleetServer(t)

	job := createJobViaHandler(t, srv, map[string]any{
		"name":         "vacuum-prod",
		"submitted_by": "fleet-runner",
		"job_def":      `{"name":"vacuum-prod"}`,
	})

	if job.JobID == "" {
		t.Fatal("expected non-empty job_id")
	}
	if job.Name != "vacuum-prod" {
		t.Errorf("name = %q, want vacuum-prod", job.Name)
	}
	if job.Status != "pending" {
		t.Errorf("status = %q, want pending", job.Status)
	}
}

func TestFleetHandlers_CreateJob_MissingFields(t *testing.T) {
	srv := newFleetServer(t)

	tests := []struct {
		body map[string]any
		desc string
	}{
		{map[string]any{"submitted_by": "x", "job_def": "{}"}, "missing name"},
		{map[string]any{"name": "x", "job_def": "{}"}, "missing submitted_by"},
		{map[string]any{"name": "x", "submitted_by": "x"}, "missing job_def"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			data, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/v1/fleet/jobs", bytes.NewReader(data))
			w := httptest.NewRecorder()
			srv.handleCreateJob(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", tt.desc, w.Code)
			}
		})
	}
}

func TestFleetHandlers_CreateJob_InvalidJSON(t *testing.T) {
	srv := newFleetServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/jobs", bytes.NewReader([]byte("not-json")))
	w := httptest.NewRecorder()
	srv.handleCreateJob(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- handleListJobs ---

func TestFleetHandlers_ListJobs_Empty(t *testing.T) {
	srv := newFleetServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/jobs", nil)
	w := httptest.NewRecorder()
	srv.handleListJobs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var jobs []*audit.FleetJob
	json.NewDecoder(w.Body).Decode(&jobs) //nolint:errcheck
	if len(jobs) != 0 {
		t.Errorf("expected empty list, got %d", len(jobs))
	}
}

func TestFleetHandlers_ListJobs_FilterByStatus(t *testing.T) {
	srv := newFleetServer(t)
	ctx := context.Background()

	for _, name := range []string{"job-a", "job-b"} {
		j := &audit.FleetJob{Name: name, SubmittedBy: "x", JobDef: "{}"}
		if err := srv.store.CreateJob(ctx, j); err != nil {
			t.Fatalf("CreateJob: %v", err)
		}
	}
	// Complete the first job.
	jobs, _ := srv.store.ListJobs(ctx, audit.FleetJobQueryOptions{Limit: 1})
	srv.store.UpdateJobStatus(ctx, jobs[0].JobID, "completed", "") //nolint:errcheck

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/jobs?status=pending", nil)
	w := httptest.NewRecorder()
	srv.handleListJobs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result []*audit.FleetJob
	json.NewDecoder(w.Body).Decode(&result) //nolint:errcheck
	if len(result) != 1 {
		t.Errorf("expected 1 pending job, got %d", len(result))
	}
}

// --- handleGetJob ---

func TestFleetHandlers_GetJob_OK(t *testing.T) {
	srv := newFleetServer(t)
	created := createJobViaHandler(t, srv, map[string]any{
		"name":         "test",
		"submitted_by": "x",
		"job_def":      "{}",
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/jobs/"+created.JobID, nil)
	req.SetPathValue("jobID", created.JobID)
	w := httptest.NewRecorder()
	srv.handleGetJob(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got audit.FleetJob
	json.NewDecoder(w.Body).Decode(&got) //nolint:errcheck
	if got.JobID != created.JobID {
		t.Errorf("job_id = %q, want %q", got.JobID, created.JobID)
	}
}

func TestFleetHandlers_GetJob_NotFound(t *testing.T) {
	srv := newFleetServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/jobs/flj_nonexistent", nil)
	req.SetPathValue("jobID", "flj_nonexistent")
	w := httptest.NewRecorder()
	srv.handleGetJob(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- handleUpdateStatus ---

func TestFleetHandlers_UpdateStatus_OK(t *testing.T) {
	srv := newFleetServer(t)
	created := createJobViaHandler(t, srv, map[string]any{
		"name":         "test",
		"submitted_by": "x",
		"job_def":      "{}",
	})

	data, _ := json.Marshal(map[string]string{"status": "completed", "summary": "all done"})
	req := httptest.NewRequest(http.MethodPatch, "/v1/fleet/jobs/"+created.JobID+"/status", bytes.NewReader(data))
	req.SetPathValue("jobID", created.JobID)
	w := httptest.NewRecorder()
	srv.handleUpdateStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// Verify via GetJob.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/jobs/"+created.JobID, nil)
	req2.SetPathValue("jobID", created.JobID)
	w2 := httptest.NewRecorder()
	srv.handleGetJob(w2, req2)
	var got audit.FleetJob
	json.NewDecoder(w2.Body).Decode(&got) //nolint:errcheck
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
	if got.Summary != "all done" {
		t.Errorf("summary = %q, want 'all done'", got.Summary)
	}
}

func TestFleetHandlers_UpdateStatus_MissingStatus(t *testing.T) {
	srv := newFleetServer(t)
	data, _ := json.Marshal(map[string]string{"summary": "no status"})
	req := httptest.NewRequest(http.MethodPatch, "/v1/fleet/jobs/flj_x/status", bytes.NewReader(data))
	req.SetPathValue("jobID", "flj_x")
	w := httptest.NewRecorder()
	srv.handleUpdateStatus(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- handleAddServer / handleGetServers ---

func TestFleetHandlers_AddAndGetServers(t *testing.T) {
	srv := newFleetServer(t)
	created := createJobViaHandler(t, srv, map[string]any{
		"name":         "test",
		"submitted_by": "x",
		"job_def":      "{}",
	})

	// Add two servers.
	for _, name := range []string{"prod-db-1", "prod-db-2"} {
		data, _ := json.Marshal(map[string]string{
			"server_name": name,
			"stage":       "canary",
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/fleet/jobs/"+created.JobID+"/servers", bytes.NewReader(data))
		req.SetPathValue("jobID", created.JobID)
		w := httptest.NewRecorder()
		srv.handleAddServer(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("handleAddServer %s: status = %d, want 201", name, w.Code)
		}
	}

	// List servers.
	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/jobs/"+created.JobID+"/servers", nil)
	req.SetPathValue("jobID", created.JobID)
	w := httptest.NewRecorder()
	srv.handleGetServers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("handleGetServers: status = %d, want 200", w.Code)
	}
	var servers []*audit.FleetJobServer
	json.NewDecoder(w.Body).Decode(&servers) //nolint:errcheck
	if len(servers) != 2 {
		t.Errorf("expected 2 servers, got %d", len(servers))
	}
}

// --- handleUpdateServer ---

func TestFleetHandlers_UpdateServer_OK(t *testing.T) {
	srv := newFleetServer(t)
	created := createJobViaHandler(t, srv, map[string]any{
		"name":         "test",
		"submitted_by": "x",
		"job_def":      "{}",
	})

	// Add a server first.
	data, _ := json.Marshal(map[string]string{"server_name": "db-1", "stage": "canary"})
	addReq := httptest.NewRequest(http.MethodPost, "/v1/fleet/jobs/"+created.JobID+"/servers", bytes.NewReader(data))
	addReq.SetPathValue("jobID", created.JobID)
	srv.handleAddServer(httptest.NewRecorder(), addReq)

	// Update its status.
	patchData, _ := json.Marshal(map[string]string{
		"status": "success",
		"output": "VACUUM",
	})
	patchReq := httptest.NewRequest(http.MethodPatch, "/v1/fleet/jobs/"+created.JobID+"/servers/db-1", bytes.NewReader(patchData))
	patchReq.SetPathValue("jobID", created.JobID)
	patchReq.SetPathValue("serverName", "db-1")
	w := httptest.NewRecorder()
	srv.handleUpdateServer(w, patchReq)

	if w.Code != http.StatusOK {
		t.Fatalf("handleUpdateServer: status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// Verify via GetJobServers.
	servers, err := srv.store.GetJobServers(context.Background(), created.JobID)
	if err != nil || len(servers) == 0 {
		t.Fatalf("GetJobServers: %v, len=%d", err, len(servers))
	}
	if servers[0].Status != "success" {
		t.Errorf("server status = %q, want success", servers[0].Status)
	}
	if servers[0].Output != "VACUUM" {
		t.Errorf("server output = %q, want VACUUM", servers[0].Output)
	}
}
