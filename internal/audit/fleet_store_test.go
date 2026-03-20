package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newFleetTestStore(t *testing.T) *FleetStore {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	fs, err := NewFleetStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewFleetStore: %v", err)
	}
	return fs
}

func TestFleetStore_CreateAndGetJob(t *testing.T) {
	fs := newFleetTestStore(t)
	ctx := context.Background()

	job := &FleetJob{
		Name:        "vacuum-all-prod",
		SubmittedBy: "fleet-runner",
		JobDef:      `{"name":"vacuum-all-prod"}`,
	}
	if err := fs.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if job.JobID == "" {
		t.Fatal("expected non-empty job_id")
	}

	got, err := fs.GetJob(ctx, job.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Name != job.Name {
		t.Errorf("name = %q, want %q", got.Name, job.Name)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

func TestFleetStore_UpdateJobStatus(t *testing.T) {
	fs := newFleetTestStore(t)
	ctx := context.Background()

	job := &FleetJob{Name: "test", SubmittedBy: "x", JobDef: "{}"}
	if err := fs.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if err := fs.UpdateJobStatus(ctx, job.JobID, "completed", "done"); err != nil {
		t.Fatalf("UpdateJobStatus: %v", err)
	}

	got, err := fs.GetJob(ctx, job.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
	if got.Summary != "done" {
		t.Errorf("summary = %q, want done", got.Summary)
	}
}

func TestFleetStore_AddAndUpdateServer(t *testing.T) {
	fs := newFleetTestStore(t)
	ctx := context.Background()

	job := &FleetJob{Name: "test", SubmittedBy: "x", JobDef: "{}"}
	if err := fs.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	srv := &FleetJobServer{
		JobID:      job.JobID,
		ServerName: "prod-db-1",
		Stage:      "canary",
		Status:     "pending",
	}
	if err := fs.AddServer(ctx, srv); err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	now := time.Now().UTC()
	if err := fs.UpdateServer(ctx, job.JobID, "prod-db-1", "success", "ok", now, now.Add(2*time.Second)); err != nil {
		t.Fatalf("UpdateServer: %v", err)
	}

	servers, err := fs.GetJobServers(ctx, job.JobID)
	if err != nil {
		t.Fatalf("GetJobServers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if servers[0].Status != "success" {
		t.Errorf("server status = %q, want success", servers[0].Status)
	}
	if servers[0].Output != "ok" {
		t.Errorf("server output = %q, want ok", servers[0].Output)
	}
}

func TestAddServerStep(t *testing.T) {
	fs := newFleetTestStore(t)
	ctx := context.Background()

	job := &FleetJob{Name: "step-test", SubmittedBy: "x", JobDef: "{}"}
	if err := fs.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	step := &FleetJobServerStep{
		JobID:      job.JobID,
		ServerName: "db-1",
		StepIndex:  0,
		Tool:       "check_connection",
		Status:     "pending",
	}
	if err := fs.AddServerStep(ctx, step); err != nil {
		t.Fatalf("AddServerStep: %v", err)
	}

	// Update it.
	now := time.Now().UTC()
	if err := fs.UpdateServerStep(ctx, job.JobID, "db-1", 0, "success", "connected", now, now.Add(time.Second)); err != nil {
		t.Fatalf("UpdateServerStep: %v", err)
	}

	steps, err := fs.GetServerSteps(ctx, job.JobID, "db-1")
	if err != nil {
		t.Fatalf("GetServerSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].Status != "success" {
		t.Errorf("step status = %q, want success", steps[0].Status)
	}
	if steps[0].Tool != "check_connection" {
		t.Errorf("step tool = %q, want check_connection", steps[0].Tool)
	}
	if steps[0].Output != "connected" {
		t.Errorf("step output = %q, want connected", steps[0].Output)
	}
}

func TestGetServerSteps_Empty(t *testing.T) {
	fs := newFleetTestStore(t)
	ctx := context.Background()

	job := &FleetJob{Name: "empty-steps", SubmittedBy: "x", JobDef: "{}"}
	if err := fs.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	steps, err := fs.GetServerSteps(ctx, job.JobID, "no-such-server")
	if err != nil {
		t.Fatalf("GetServerSteps: %v", err)
	}
	if steps != nil {
		t.Errorf("expected nil steps for unknown server, got %v", steps)
	}
}

func TestGetServerSteps_MultipleSteps(t *testing.T) {
	fs := newFleetTestStore(t)
	ctx := context.Background()

	job := &FleetJob{Name: "multi-step", SubmittedBy: "x", JobDef: "{}"}
	if err := fs.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	for i, tool := range []string{"check_connection", "run_sql", "get_table_stats"} {
		step := &FleetJobServerStep{
			JobID:      job.JobID,
			ServerName: "db-2",
			StepIndex:  i,
			Tool:       tool,
			Status:     "pending",
		}
		if err := fs.AddServerStep(ctx, step); err != nil {
			t.Fatalf("AddServerStep[%d]: %v", i, err)
		}
	}

	steps, err := fs.GetServerSteps(ctx, job.JobID, "db-2")
	if err != nil {
		t.Fatalf("GetServerSteps: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	// Verify ordering.
	for i, step := range steps {
		if step.StepIndex != i {
			t.Errorf("steps[%d].StepIndex = %d, want %d", i, step.StepIndex, i)
		}
	}
}

func TestFleetStore_ListJobs(t *testing.T) {
	fs := newFleetTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"job-a", "job-b", "job-c"} {
		j := &FleetJob{Name: name, SubmittedBy: "fleet-runner", JobDef: "{}"}
		if err := fs.CreateJob(ctx, j); err != nil {
			t.Fatalf("CreateJob %s: %v", name, err)
		}
	}
	if err := fs.UpdateJobStatus(ctx, func() string {
		jobs, _ := fs.ListJobs(ctx, FleetJobQueryOptions{Limit: 1})
		if len(jobs) > 0 {
			return jobs[0].JobID
		}
		return ""
	}(), "completed", ""); err != nil {
		t.Fatalf("UpdateJobStatus: %v", err)
	}

	// List pending only.
	jobs, err := fs.ListJobs(ctx, FleetJobQueryOptions{Status: "pending", Limit: 50})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("expected 2 pending jobs, got %d", len(jobs))
	}
}
