package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// FleetJob represents a fleet runner job: a single change applied across
// a set of infrastructure targets with staged rollout.
type FleetJob struct {
	JobID       string    `json:"job_id"`            // "flj_" + uuid[:8]
	Name        string    `json:"name"`
	SubmittedBy string    `json:"submitted_by"`
	SubmittedAt time.Time `json:"submitted_at"`
	Status      string    `json:"status"` // pending, running, completed, failed, aborted
	JobDef      string    `json:"job_def"` // JSON blob of original job definition
	Summary     string    `json:"summary,omitempty"` // filled on completion
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// FleetJobServer tracks the per-server execution status within a fleet job.
type FleetJobServer struct {
	ID         int64     `json:"id,omitempty"`
	JobID      string    `json:"job_id"`
	ServerName string    `json:"server_name"`
	Stage      string    `json:"stage"`  // canary, wave-1, wave-2, ...
	Status     string    `json:"status"` // pending, running, success, failed, skipped
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
    id           %s,
    job_id       TEXT UNIQUE NOT NULL,
    name         TEXT NOT NULL,
    submitted_by TEXT NOT NULL,
    submitted_at TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    job_def      TEXT NOT NULL,
    summary      TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
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
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
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
			(job_id, name, submitted_by, submitted_at, status, job_def, summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`),
		job.JobID,
		job.Name,
		job.SubmittedBy,
		job.SubmittedAt.Format(time.RFC3339Nano),
		job.Status,
		job.JobDef,
		nullableString(job.Summary),
		job.CreatedAt.Format(time.RFC3339Nano),
		job.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

// GetJob retrieves a fleet job by ID.
func (s *FleetStore) GetJob(ctx context.Context, jobID string) (*FleetJob, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
		SELECT job_id, name, submitted_by, submitted_at, status, job_def, summary, created_at, updated_at
		FROM fleet_jobs WHERE job_id = ?
	`), jobID)

	return scanFleetJob(row)
}

// FleetJobQueryOptions specifies filters for listing fleet jobs.
type FleetJobQueryOptions struct {
	Status      string
	SubmittedBy string
	Limit       int
}

// ListJobs returns fleet jobs matching the filters, newest first.
func (s *FleetStore) ListJobs(ctx context.Context, opts FleetJobQueryOptions) ([]*FleetJob, error) {
	query := `SELECT job_id, name, submitted_by, submitted_at, status, job_def, summary, created_at, updated_at
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

// --- scan helpers ---

func scanFleetJob(row *sql.Row) (*FleetJob, error) {
	var j FleetJob
	var submittedAt, createdAt, updatedAt string
	var summary sql.NullString

	err := row.Scan(
		&j.JobID, &j.Name, &j.SubmittedBy, &submittedAt,
		&j.Status, &j.JobDef, &summary,
		&createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("fleet job not found")
		}
		return nil, err
	}

	j.Summary = summary.String
	j.SubmittedAt, _ = time.Parse(time.RFC3339Nano, submittedAt)
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	j.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &j, nil
}

func scanFleetJobFromRows(rows *sql.Rows) (*FleetJob, error) {
	var j FleetJob
	var submittedAt, createdAt, updatedAt string
	var summary sql.NullString

	err := rows.Scan(
		&j.JobID, &j.Name, &j.SubmittedBy, &submittedAt,
		&j.Status, &j.JobDef, &summary,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	j.Summary = summary.String
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
