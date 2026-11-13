package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// callPV calls a handler directly with several path values and a JSON or raw body.
func callPV(h http.HandlerFunc, method, target string, body any, pv map[string]string) *httptest.ResponseRecorder {
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		data, _ := json.Marshal(b)
		rdr = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, rdr)
	for k, v := range pv {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func fleetWithApprovals(t *testing.T) *fleetServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	fs, err := audit.NewFleetStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatal(err)
	}
	as, err := audit.NewApprovalStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatal(err)
	}
	return &fleetServer{store: fs, approvalStore: as}
}

func jobWithServer(t *testing.T, s *fleetServer) (jobID string) {
	t.Helper()
	job := createJobViaHandler(t, s, map[string]any{"name": "j", "submitted_by": "fleet-runner", "job_def": "{}"})
	rec := callPV(s.handleAddServer, http.MethodPost, "/x", map[string]any{"server_name": "db1", "stage": "canary"}, map[string]string{"jobID": job.JobID})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add server: %d %s", rec.Code, rec.Body.String())
	}
	return job.JobID
}

func TestFleetHandlers_AddServer_ValidationAndDefaults(t *testing.T) {
	s := newFleetServer(t)
	job := createJobViaHandler(t, s, map[string]any{"name": "j", "submitted_by": "x", "job_def": "{}"})
	pv := map[string]string{"jobID": job.JobID}
	for name, tc := range map[string]struct {
		body any
		pv   map[string]string
		want string
	}{
		"no job id":      {map[string]any{"server_name": "a", "stage": "s"}, nil, "missing job ID"},
		"not JSON":       {"{nope", pv, "invalid JSON"},
		"no server name": {map[string]any{"stage": "s"}, pv, "server_name is required"},
		"no stage":       {map[string]any{"server_name": "a"}, pv, "stage is required"},
	} {
		rec := callPV(s.handleAddServer, http.MethodPost, "/x", tc.body, tc.pv)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := callPV(s.handleAddServer, http.MethodPost, "/x", map[string]any{"server_name": "a", "stage": "canary"}, pv)
	var got audit.FleetJobServer
	if rec.Code != http.StatusCreated || json.NewDecoder(rec.Body).Decode(&got) != nil || got.Status != "pending" || got.JobID != job.JobID {
		t.Errorf("%d %+v: a server with no status starts pending, under the job in the path", rec.Code, got)
	}
}

func TestFleetHandlers_GetServer(t *testing.T) {
	s := newFleetServer(t)
	jobID := jobWithServer(t, s)
	rec := callPV(s.handleGetServer, http.MethodGet, "/x", nil, map[string]string{"jobID": jobID, "serverName": "db1"})
	var got audit.FleetJobServer
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&got) != nil || got.ServerName != "db1" || got.Stage != "canary" {
		t.Errorf("%d %+v", rec.Code, got)
	}
	if rec := callPV(s.handleGetServer, http.MethodGet, "/x", nil, map[string]string{"jobID": jobID, "serverName": "nope"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown server: %d", rec.Code)
	}
	if rec := callPV(s.handleGetServer, http.MethodGet, "/x", nil, map[string]string{"jobID": jobID}); rec.Code != http.StatusBadRequest {
		t.Errorf("no server name: %d", rec.Code)
	}
}

func TestFleetHandlers_GetServers_EmptyIsAListNotNull(t *testing.T) {
	s := newFleetServer(t)
	job := createJobViaHandler(t, s, map[string]any{"name": "j", "submitted_by": "x", "job_def": "{}"})
	rec := callPV(s.handleGetServers, http.MethodGet, "/x", nil, map[string]string{"jobID": job.JobID})
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("%d %q", rec.Code, rec.Body.String())
	}
	if rec := callPV(s.handleGetServers, http.MethodGet, "/x", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no id: %d", rec.Code)
	}
}

func TestFleetHandlers_UpdateServer_Validation(t *testing.T) {
	s := newFleetServer(t)
	jobID := jobWithServer(t, s)
	pv := map[string]string{"jobID": jobID, "serverName": "db1"}
	for name, tc := range map[string]struct {
		body any
		pv   map[string]string
		want string
	}{
		"no server name": {map[string]any{"status": "running"}, map[string]string{"jobID": jobID}, "missing job ID or server name"},
		"not JSON":       {"{nope", pv, "invalid JSON"},
		"no status":      {map[string]any{"output": "x"}, pv, "status is required"},
	} {
		rec := callPV(s.handleUpdateServer, http.MethodPatch, "/x", tc.body, tc.pv)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// Times are read when they parse, and an unparsable one is dropped, not fatal.
	started, finished := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 9, 10, 5, 0, 0, time.UTC)
	rec := callPV(s.handleUpdateServer, http.MethodPatch, "/x", map[string]any{
		"status": "succeeded", "output": "done", "started_at": started.Format(time.RFC3339Nano), "finished_at": finished.Format(time.RFC3339Nano)}, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got, err := s.store.GetServer(context.Background(), jobID, "db1")
	if err != nil || got.Status != "succeeded" || got.Output != "done" || !got.StartedAt.Equal(started) || !got.FinishedAt.Equal(finished) {
		t.Errorf("server = %+v, %v", got, err)
	}
	rec = callPV(s.handleUpdateServer, http.MethodPatch, "/x", map[string]any{"status": "failed", "started_at": "yesterday-ish"}, pv)
	if rec.Code != http.StatusOK {
		t.Errorf("an unparsable time should be ignored: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFleetHandlers_Steps(t *testing.T) {
	s := newFleetServer(t)
	jobID := jobWithServer(t, s)
	pv := map[string]string{"jobID": jobID, "serverName": "db1"}

	// No steps yet: an empty list.
	if rec := callPV(s.handleGetServerSteps, http.MethodGet, "/x", nil, pv); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty steps: %d %q", rec.Code, rec.Body.String())
	}
	if rec := callPV(s.handleGetServerSteps, http.MethodGet, "/x", nil, map[string]string{"jobID": jobID}); rec.Code != http.StatusBadRequest {
		t.Errorf("no server name: %d", rec.Code)
	}

	// Adding a step.
	for name, tc := range map[string]struct {
		body any
		pv   map[string]string
		want string
	}{
		"no server": {map[string]any{"tool": "t"}, map[string]string{"jobID": jobID}, "missing job ID or server name"},
		"not JSON":  {"{nope", pv, "invalid JSON"},
		"no tool":   {map[string]any{"step_index": 0}, pv, "tool is required"},
	} {
		rec := callPV(s.handleAddServerStep, http.MethodPost, "/x", tc.body, tc.pv)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := callPV(s.handleAddServerStep, http.MethodPost, "/x", map[string]any{"step_index": 0, "tool": "run_vacuum"}, pv)
	var step audit.FleetJobServerStep
	if rec.Code != http.StatusCreated || json.NewDecoder(rec.Body).Decode(&step) != nil || step.Status != "pending" || step.JobID != jobID || step.ServerName != "db1" {
		t.Fatalf("%d %+v: a step starts pending, under the job and server in the path", rec.Code, step)
	}
	rec = callPV(s.handleGetServerSteps, http.MethodGet, "/x", nil, pv)
	var steps []audit.FleetJobServerStep
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&steps) != nil || len(steps) != 1 || steps[0].Tool != "run_vacuum" {
		t.Errorf("steps = %d %+v", rec.Code, steps)
	}

	// Updating it.
	upd := func(index string, body any) *httptest.ResponseRecorder {
		return callPV(s.handleUpdateServerStep, http.MethodPatch, "/x", body, map[string]string{"jobID": jobID, "serverName": "db1", "stepIndex": index})
	}
	for name, tc := range map[string]struct {
		index string
		body  any
		want  string
	}{
		"no index":  {"", map[string]any{"status": "running"}, "missing job ID, server name, or step index"},
		"bad index": {"first", map[string]any{"status": "running"}, "invalid step index"},
		"not JSON":  {"0", "{nope", "invalid JSON"},
		"no status": {"0", map[string]any{"output": "x"}, "status is required"},
	} {
		if rec := upd(tc.index, tc.body); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	started, finished := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 9, 10, 1, 0, 0, time.UTC)
	if rec := upd("0", map[string]any{"status": "succeeded", "output": "VACUUM", "started_at": started.Format(time.RFC3339Nano),
		"finished_at": finished.Format(time.RFC3339Nano)}); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got, _ := s.store.GetServerSteps(context.Background(), jobID, "db1")
	if len(got) != 1 || got[0].Status != "succeeded" || got[0].Output != "VACUUM" || !got[0].FinishedAt.Equal(finished) {
		t.Errorf("step = %+v", got)
	}
	if rec := upd("0", map[string]any{"status": "failed", "finished_at": "not a time"}); rec.Code != http.StatusOK {
		t.Errorf("an unparsable time should be ignored: %d", rec.Code)
	}
}

func TestFleetHandlers_UpdateStatus_Errors(t *testing.T) {
	s := newFleetServer(t)
	if rec := callPV(s.handleUpdateStatus, http.MethodPatch, "/x", map[string]any{"status": "running"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no id: %d", rec.Code)
	}
	if rec := callPV(s.handleUpdateStatus, http.MethodPatch, "/x", "{nope", map[string]string{"jobID": "j"}); rec.Code != http.StatusBadRequest {
		t.Errorf("not JSON: %d", rec.Code)
	}
}

func TestFleetHandlers_ListJobs_FilterBySubmitterAndTrace(t *testing.T) {
	s := newFleetServer(t)
	createJobViaHandler(t, s, map[string]any{"name": "a", "submitted_by": "alice", "job_def": "{}", "plan_trace_id": "tr_1"})
	createJobViaHandler(t, s, map[string]any{"name": "b", "submitted_by": "bob", "job_def": "{}", "plan_trace_id": "tr_2"})
	count := func(query string) int {
		t.Helper()
		rec := callPV(s.handleListJobs, http.MethodGet, "/v1/fleet/jobs"+query, nil, nil)
		var jobs []audit.FleetJob
		if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&jobs) != nil {
			t.Fatalf("%s: %d", query, rec.Code)
		}
		return len(jobs)
	}
	for query, want := range map[string]int{"": 2, "?submitted_by=alice": 1, "?plan_trace_id=tr_2": 1, "?submitted_by=alice&plan_trace_id=tr_2": 0} {
		if got := count(query); got != want {
			t.Errorf("list%s = %d, want %d", query, got, want)
		}
	}
}

func TestFleetHandlers_JobApproval(t *testing.T) {
	s := fleetWithApprovals(t)
	pv := map[string]string{"jobID": "job_1"}

	for name, tc := range map[string]struct {
		body any
		pv   map[string]string
		code int
		want string
	}{
		"no job id":       {map[string]any{"action_class": "write"}, nil, http.StatusBadRequest, "missing job ID"},
		"not JSON":        {"{nope", pv, http.StatusBadRequest, "invalid JSON"},
		"no action class": {map[string]any{"resource_name": "x"}, pv, http.StatusBadRequest, "action_class is required"},
	} {
		rec := callPV(s.handleCreateJobApproval, http.MethodPost, "/x", tc.body, tc.pv)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	// A fleet approval is always a fleet-job approval for the fleet approver, whatever the body says.
	rec := callPV(s.handleCreateJobApproval, http.MethodPost, "/x", map[string]any{
		"action_class": "destructive", "resource_type": "database", "resource_name": "prod-1",
		"context": map[string]any{"servers": 3}, "expires_at": "2026-12-01T00:00:00Z"}, pv)
	var created struct {
		ApprovalID string `json:"approval_id"`
		Status     string `json:"status"`
	}
	if rec.Code != http.StatusCreated || json.NewDecoder(rec.Body).Decode(&created) != nil || created.Status != "pending" || created.ApprovalID == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	stored, err := s.approvalStore.GetRequest(context.Background(), created.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ResourceType != "fleet_job" || stored.AgentName != "fleet-runner" || stored.ApproverRole != "fleet-approver" ||
		stored.TraceID != "job_1" || stored.RequestedBy != "fleet-runner" || stored.ResourceName != "prod-1" ||
		stored.ExpiresAt.Format(time.RFC3339) != "2026-12-01T00:00:00Z" || stored.RequestContext["servers"] != float64(3) {
		t.Errorf("stored = %+v", stored)
	}

	// Read it back through the job route.
	rec = callPV(s.handleGetJobApproval, http.MethodGet, "/x", nil, map[string]string{"jobID": "job_1", "approvalID": created.ApprovalID})
	var got audit.StoredApproval
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&got) != nil || got.ApprovalID != created.ApprovalID {
		t.Errorf("%d %+v", rec.Code, got)
	}
	if rec := callPV(s.handleGetJobApproval, http.MethodGet, "/x", nil, map[string]string{"approvalID": "nope"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown approval: %d", rec.Code)
	}
	if rec := callPV(s.handleGetJobApproval, http.MethodGet, "/x", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no approval id: %d", rec.Code)
	}
}

// With no approval store configured, the two approval routes say so.
func TestFleetHandlers_JobApproval_NotConfigured(t *testing.T) {
	s := newFleetServer(t)
	if rec := callPV(s.handleCreateJobApproval, http.MethodPost, "/x", map[string]any{"action_class": "write"}, map[string]string{"jobID": "j"}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("create: %d", rec.Code)
	}
	if rec := callPV(s.handleGetJobApproval, http.MethodGet, "/x", nil, map[string]string{"approvalID": "a"}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("get: %d", rec.Code)
	}
}
