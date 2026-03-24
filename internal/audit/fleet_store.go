package audit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// FleetJob represents a fleet runner job: a single change applied across
// a set of infrastructure targets with staged rollout.
type FleetJob struct {
	JobID        string    `json:"job_id"`             // "flj_" + uuid[:8]
	Name         string    `json:"name"`
	SubmittedBy  string    `json:"submitted_by"`
	SubmittedAt  time.Time `json:"submitted_at"`
	Status       string    `json:"status"` // pending, running, completed, failed, aborted
	JobDef       string    `json:"job_def"` // JSON blob of original job definition
	Summary      string    `json:"summary,omitempty"` // filled on completion
	PlanTraceID  string    `json:"plan_trace_id,omitempty"` // links to the NL planner audit event
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// FleetJobServer tracks the per-server execution status within a fleet job.
type FleetJobServer struct {
	ID         int64     `json:"id,omitempty"`
	JobID      string    `json:"job_id"`
	ServerName string    `json:"server_name"`
	Stage      string    `json:"stage"`  // canary, wave-1, wave-2, ...
	Status     string    `json:"status"` // pending, running, success, partial, failed, skipped
	Output     string    `json:"output,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// FleetJobServerStep tracks the execution of one step within a server's run.
type FleetJobServerStep struct {
	ID         int64     `json:"id,omitempty"`
	JobID      string    `json:"job_id"`
	ServerName string    `json:"server_name"`
	StepIndex  int       `json:"step_index"`
	Tool       string    `json:"tool"`
	Status     string    `json:"status"` // pending, success, failed
	Output     string    `json:"output,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// FleetStore persists fleet jobs and per-server execution status.
// It shares the same *sql.DB connection as the audit Store and ApprovalStore.
type FleetStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewFleetStore creates the fleet tables (if absent) and returns a ready-to-use
// FleetStore using the given shared database connection.
func NewFleetStore(db *sql.DB, isPostgres bool) (*FleetStore, error) {
	s := &FleetStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create fleet schema: %w", err)
	}
	return s, nil
}

func (s *FleetStore) createSchema() error {
	pk := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if s.isPostgres {
		pk = "BIGSERIAL PRIMARY KEY"
	}
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS fleet_jobs (
    id             %s,
    job_id         TEXT UNIQUE NOT NULL,
    name           TEXT NOT NULL,
    submitted_by   TEXT NOT NULL,
    submitted_at   TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    job_def        TEXT NOT NULL,
    summary        TEXT,
    plan_trace_id  TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_fleet_jobs_job_id      ON fleet_jobs(job_id)`,
		`CREATE INDEX IF NOT EXISTS idx_fleet_jobs_submitted_by ON fleet_jobs(submitted_by)`,
		`CREATE INDEX IF NOT EXISTS idx_fleet_jobs_status       ON fleet_jobs(status)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS fleet_job_servers (
    id          %s,
    job_id      TEXT NOT NULL REFERENCES fleet_jobs(job_id),
    server_name TEXT NOT NULL,
    stage       TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    output      TEXT,
    started_at  TEXT,
    finished_at TEXT
)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_fleet_servers_job_id ON fleet_job_servers(job_id)`,
		`CREATE INDEX IF NOT EXISTS idx_fleet_servers_status ON fleet_job_servers(status)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS fleet_job_server_steps (
    id          %s,
    job_id      TEXT NOT NULL REFERENCES fleet_jobs(job_id),
    server_name TEXT NOT NULL,
    step_index  INTEGER NOT NULL,
    tool        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    output      TEXT,
    started_at  TEXT,
    finished_at TEXT
)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_fleet_steps_job_server ON fleet_job_server_steps(job_id, server_name)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}

	// Additive column migrations — ignored when the column already exists
	// (SQLite: "duplicate column name"; Postgres: "column already exists").
	migrations := []string{
		`ALTER TABLE fleet_jobs ADD COLUMN plan_trace_id TEXT`,
	}
	for _, stmt := range migrations {
		if _, err := s.db.Exec(stmt); err != nil {
			msg := err.Error()
			if !strings.Contains(msg, "duplicate column") && !strings.Contains(msg, "already exists") {
				return err
			}
		}
	}
	return nil
}

// CreateJob inserts a new fleet job. The JobID is generated if empty.
func (s *FleetStore) CreateJob(ctx context.Context, job *FleetJob) error {
	if job.JobID == "" {
		job.JobID = "flj_" + uuid.New().String()[:8]
	}
	now := time.Now().UTC()
	if job.SubmittedAt.IsZero() {
		job.SubmittedAt = now
	}
	if job.Status == "" {
		job.Status = "pending"
	}
	job.CreatedAt = now
	job.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		INSERT INTO fleet_jobs
			(job_id, name, submitted_by, submitted_at, status, job_def, summary, plan_trace_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`),
		job.JobID,
		job.Name,
		job.SubmittedBy,
		job.SubmittedAt.Format(time.RFC3339Nano),
		job.Status,
		job.JobDef,
		nullableString(job.Summary),
		nullableString(job.PlanTraceID),
		job.CreatedAt.Format(time.RFC3339Nano),
		job.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

// GetJob retrieves a fleet job by ID.
func (s *FleetStore) GetJob(ctx context.Context, jobID string) (*FleetJob, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
		SELECT job_id, name, submitted_by, submitted_at, status, job_def, summary, plan_trace_id, created_at, updated_at
		FROM fleet_jobs WHERE job_id = ?
	`), jobID)

	return scanFleetJob(row)
}

// FleetJobQueryOptions specifies filters for listing fleet jobs.
type FleetJobQueryOptions struct {
	Status       string
	SubmittedBy  string
	PlanTraceID  string // filter by the planner audit event that generated this job
	Limit        int
}

// ListJobs returns fleet jobs matching the filters, newest first.
func (s *FleetStore) ListJobs(ctx context.Context, opts FleetJobQueryOptions) ([]*FleetJob, error) {
	query := `SELECT job_id, name, submitted_by, submitted_at, status, job_def, summary, plan_trace_id, created_at, updated_at
		FROM fleet_jobs WHERE 1=1`
	var args []any

	if opts.Status != "" {
		query += " AND status = ?"
		args = append(args, opts.Status)
	}
	if opts.SubmittedBy != "" {
		query += " AND submitted_by = ?"
		args = append(args, opts.SubmittedBy)
	}
	if opts.PlanTraceID != "" {
		query += " AND plan_trace_id = ?"
		args = append(args, opts.PlanTraceID)
	}
	query += " ORDER BY created_at DESC"
	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}

	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*FleetJob
	for rows.Next() {
		job, err := scanFleetJobFromRows(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// UpdateJobStatus updates the status (and optionally summary) of a fleet job.
func (s *FleetStore) UpdateJobStatus(ctx context.Context, jobID, status, summary string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		UPDATE fleet_jobs SET status = ?, summary = ?, updated_at = ? WHERE job_id = ?
	`), status, nullableString(summary), now.Format(time.RFC3339Nano), jobID)
	return err
}

// AddServer inserts a per-server execution record for a fleet job.
func (s *FleetStore) AddServer(ctx context.Context, srv *FleetJobServer) error {
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		INSERT INTO fleet_job_servers (job_id, server_name, stage, status)
		VALUES (?, ?, ?, ?)
	`), srv.JobID, srv.ServerName, srv.Stage, srv.Status)
	return err
}

// UpdateServer updates the status, output, and timing of a per-server record.
func (s *FleetStore) UpdateServer(ctx context.Context, jobID, serverName, status, output string, startedAt, finishedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		UPDATE fleet_job_servers
		SET status = ?, output = ?, started_at = ?, finished_at = ?
		WHERE job_id = ? AND server_name = ?
	`),
		status,
		nullableString(output),
		formatTimeOrNull(startedAt),
		formatTimeOrNull(finishedAt),
		jobID,
		serverName,
	)
	return err
}

// GetJobServers returns all per-server records for a fleet job, in insertion order.
func (s *FleetStore) GetJobServers(ctx context.Context, jobID string) ([]*FleetJobServer, error) {
	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, `
		SELECT id, job_id, server_name, stage, status, output, started_at, finished_at
		FROM fleet_job_servers WHERE job_id = ? ORDER BY id
	`), jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []*FleetJobServer
	for rows.Next() {
		srv, err := scanFleetJobServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, srv)
	}
	return servers, rows.Err()
}

// GetServer retrieves a single server record for a fleet job by server name.
func (s *FleetStore) GetServer(ctx context.Context, jobID, serverName string) (*FleetJobServer, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
		SELECT id, job_id, server_name, stage, status, output, started_at, finished_at
		FROM fleet_job_servers WHERE job_id = ? AND server_name = ?
	`), jobID, serverName)

	var srv FleetJobServer
	var output sql.NullString
	var startedAt, finishedAt sql.NullString

	if err := row.Scan(
		&srv.ID, &srv.JobID, &srv.ServerName, &srv.Stage, &srv.Status,
		&output, &startedAt, &finishedAt,
	); err != nil {
		return nil, err
	}
	srv.Output = output.String
	if startedAt.Valid {
		srv.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt.String)
	}
	if finishedAt.Valid {
		srv.FinishedAt, _ = time.Parse(time.RFC3339Nano, finishedAt.String)
	}
	return &srv, nil
}

// AddServerStep inserts a pending step record for a server within a fleet job.
func (s *FleetStore) AddServerStep(ctx context.Context, step *FleetJobServerStep) error {
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		INSERT INTO fleet_job_server_steps (job_id, server_name, step_index, tool, status)
		VALUES (?, ?, ?, ?, ?)
	`), step.JobID, step.ServerName, step.StepIndex, step.Tool, step.Status)
	return err
}

// UpdateServerStep updates the status, output, and timing of a per-step record.
func (s *FleetStore) UpdateServerStep(ctx context.Context, jobID, serverName string, stepIndex int, status, output string, startedAt, finishedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		UPDATE fleet_job_server_steps
		SET status = ?, output = ?, started_at = ?, finished_at = ?
		WHERE job_id = ? AND server_name = ? AND step_index = ?
	`),
		status,
		nullableString(output),
		formatTimeOrNull(startedAt),
		formatTimeOrNull(finishedAt),
		jobID,
		serverName,
		stepIndex,
	)
	return err
}

// GetServerSteps returns all step records for a given server within a fleet job,
// ordered by step_index.
func (s *FleetStore) GetServerSteps(ctx context.Context, jobID, serverName string) ([]*FleetJobServerStep, error) {
	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, `
		SELECT id, job_id, server_name, step_index, tool, status, output, started_at, finished_at
		FROM fleet_job_server_steps
		WHERE job_id = ? AND server_name = ?
		ORDER BY step_index
	`), jobID, serverName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var steps []*FleetJobServerStep
	for rows.Next() {
		step, err := scanFleetJobServerStep(rows)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

// --- scan helpers ---

func scanFleetJob(row *sql.Row) (*FleetJob, error) {
	var j FleetJob
	var submittedAt, createdAt, updatedAt string
	var summary, planTraceID sql.NullString

	err := row.Scan(
		&j.JobID, &j.Name, &j.SubmittedBy, &submittedAt,
		&j.Status, &j.JobDef, &summary, &planTraceID,
		&createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("fleet job not found")
		}
		return nil, err
	}

	j.Summary = summary.String
	j.PlanTraceID = planTraceID.String
	j.SubmittedAt, _ = time.Parse(time.RFC3339Nano, submittedAt)
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	j.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &j, nil
}

func scanFleetJobFromRows(rows *sql.Rows) (*FleetJob, error) {
	var j FleetJob
	var submittedAt, createdAt, updatedAt string
	var summary, planTraceID sql.NullString

	err := rows.Scan(
		&j.JobID, &j.Name, &j.SubmittedBy, &submittedAt,
		&j.Status, &j.JobDef, &summary, &planTraceID,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	j.Summary = summary.String
	j.PlanTraceID = planTraceID.String
	j.SubmittedAt, _ = time.Parse(time.RFC3339Nano, submittedAt)
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	j.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &j, nil
}

func scanFleetJobServer(rows *sql.Rows) (*FleetJobServer, error) {
	var srv FleetJobServer
	var output sql.NullString
	var startedAt, finishedAt sql.NullString

	err := rows.Scan(
		&srv.ID, &srv.JobID, &srv.ServerName, &srv.Stage, &srv.Status,
		&output, &startedAt, &finishedAt,
	)
	if err != nil {
		return nil, err
	}

	srv.Output = output.String
	if startedAt.Valid {
		srv.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt.String)
	}
	if finishedAt.Valid {
		srv.FinishedAt, _ = time.Parse(time.RFC3339Nano, finishedAt.String)
	}
	return &srv, nil
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func scanFleetJobServerStep(rows *sql.Rows) (*FleetJobServerStep, error) {
	var step FleetJobServerStep
	var output sql.NullString
	var startedAt, finishedAt sql.NullString

	err := rows.Scan(
		&step.ID, &step.JobID, &step.ServerName, &step.StepIndex, &step.Tool, &step.Status,
		&output, &startedAt, &finishedAt,
	)
	if err != nil {
		return nil, err
	}

	step.Output = output.String
	if startedAt.Valid {
		step.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt.String)
	}
	if finishedAt.Valid {
		step.FinishedAt, _ = time.Parse(time.RFC3339Nano, finishedAt.String)
	}
	return &step, nil
}
