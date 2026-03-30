package audit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RollbackRecord persists a single-event rollback execution record.
// A RollbackPlan is derived on-the-fly from the original audit event;
// this record is written when the rollback is actually initiated.
type RollbackRecord struct {
	RollbackID      string    `json:"rollback_id"`       // "rbk_" + uuid[:8]
	OriginalEventID string    `json:"original_event_id"` // the tool_execution being undone
	OriginalTraceID string    `json:"original_trace_id"`
	OriginalJobID   string    `json:"original_job_id,omitempty"` // set for fleet rollbacks
	// Status progression: pending_approval → executing → success | failed | cancelled
	Status          string    `json:"status"`
	InitiatedBy     string    `json:"initiated_by"`
	InitiatedAt     time.Time `json:"initiated_at"`
	ApprovalID      string    `json:"approval_id,omitempty"`
	// RollbackTraceID is "tr_" + RollbackID (e.g. "tr_rbk_a1b2c3d4"), mirroring
	// fleet's "tr_flj_<uuid8>" convention so the trace and record IDs are derivable
	// from each other without a lookup.
	RollbackTraceID string    `json:"rollback_trace_id"`
	PlanJSON        string    `json:"plan_json"` // serialised RollbackPlan
	ResultOutput    string    `json:"result_output,omitempty"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// FleetRollbackRecord tracks a fleet-level rollback (reversal of a fleet job).
type FleetRollbackRecord struct {
	FleetRollbackID string    `json:"fleet_rollback_id"` // "frb_" + uuid[:8]
	OriginalJobID   string    `json:"original_job_id"`
	// Status: pending_approval | executing | success | failed | cancelled
	Status      string    `json:"status"`
	InitiatedBy string    `json:"initiated_by"`
	ApprovalID  string    `json:"approval_id,omitempty"`
	// Scope describes which servers to roll back:
	// "all" | "canary_only" | "failed_only" | JSON array of server names
	Scope         string    `json:"scope"`
	RollbackJobID string    `json:"rollback_job_id,omitempty"` // the generated reverse fleet job ID
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// RollbackQueryOptions specifies filters for listing rollback records.
type RollbackQueryOptions struct {
	OriginalEventID string
	OriginalTraceID string
	OriginalJobID   string
	Status          string
	InitiatedBy     string
	Limit           int
}

// RollbackStore persists rollback records.
// It shares the same *sql.DB as the audit Store, ApprovalStore, and FleetStore.
type RollbackStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewRollbackStore creates the rollback tables (if absent) and returns a ready-to-use
// RollbackStore using the given shared database connection.
func NewRollbackStore(db *sql.DB, isPostgres bool) (*RollbackStore, error) {
	s := &RollbackStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create rollback schema: %w", err)
	}
	return s, nil
}

func (s *RollbackStore) createSchema() error {
	pk := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if s.isPostgres {
		pk = "BIGSERIAL PRIMARY KEY"
	}
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS rollback_records (
    id                %s,
    rollback_id       TEXT UNIQUE NOT NULL,
    original_event_id TEXT NOT NULL,
    original_trace_id TEXT,
    original_job_id   TEXT,
    status            TEXT NOT NULL DEFAULT 'pending_approval',
    initiated_by      TEXT NOT NULL,
    initiated_at      TEXT NOT NULL,
    approval_id       TEXT,
    rollback_trace_id TEXT NOT NULL,
    plan_json         TEXT NOT NULL,
    result_output     TEXT,
    completed_at      TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_rollback_rollback_id       ON rollback_records(rollback_id)`,
		`CREATE INDEX IF NOT EXISTS idx_rollback_original_event_id ON rollback_records(original_event_id)`,
		`CREATE INDEX IF NOT EXISTS idx_rollback_original_trace_id ON rollback_records(original_trace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_rollback_status            ON rollback_records(status)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS fleet_rollback_records (
    id                %s,
    fleet_rollback_id TEXT UNIQUE NOT NULL,
    original_job_id   TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending_approval',
    initiated_by      TEXT NOT NULL,
    approval_id       TEXT,
    scope             TEXT NOT NULL DEFAULT 'all',
    rollback_job_id   TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_fleet_rollback_original_job_id ON fleet_rollback_records(original_job_id)`,
		`CREATE INDEX IF NOT EXISTS idx_fleet_rollback_status          ON fleet_rollback_records(status)`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// CreateRollback inserts a new rollback record. RollbackID and RollbackTraceID
// are generated if empty.
func (s *RollbackStore) CreateRollback(ctx context.Context, r *RollbackRecord) error {
	if r.RollbackID == "" {
		r.RollbackID = "rbk_" + uuid.New().String()[:8]
	}
	if r.RollbackTraceID == "" {
		r.RollbackTraceID = "tr_" + r.RollbackID // → "tr_rbk_<uuid8>"; mirrors fleet's "tr_flj_<uuid8>"
	}
	now := time.Now().UTC()
	if r.InitiatedAt.IsZero() {
		r.InitiatedAt = now
	}
	if r.Status == "" {
		r.Status = "pending_approval"
	}
	r.CreatedAt = now
	r.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		INSERT INTO rollback_records
			(rollback_id, original_event_id, original_trace_id, original_job_id,
			 status, initiated_by, initiated_at, approval_id, rollback_trace_id,
			 plan_json, result_output, completed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`),
		r.RollbackID,
		r.OriginalEventID,
		nullableString(r.OriginalTraceID),
		nullableString(r.OriginalJobID),
		r.Status,
		r.InitiatedBy,
		r.InitiatedAt.Format(time.RFC3339Nano),
		nullableString(r.ApprovalID),
		r.RollbackTraceID,
		r.PlanJSON,
		nullableString(r.ResultOutput),
		formatTimeOrNull(r.CompletedAt),
		r.CreatedAt.Format(time.RFC3339Nano),
		r.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

// GetRollback retrieves a rollback record by its rollback_id.
func (s *RollbackStore) GetRollback(ctx context.Context, rollbackID string) (*RollbackRecord, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
		SELECT rollback_id, original_event_id, original_trace_id, original_job_id,
		       status, initiated_by, initiated_at, approval_id, rollback_trace_id,
		       plan_json, result_output, completed_at, created_at, updated_at
		FROM rollback_records WHERE rollback_id = ?
	`), rollbackID)
	return scanRollbackRecord(row)
}

// GetRollbackByEventID retrieves the most recent active rollback for an original event.
// Returns nil, nil when none exists.
func (s *RollbackStore) GetRollbackByEventID(ctx context.Context, originalEventID string) (*RollbackRecord, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
		SELECT rollback_id, original_event_id, original_trace_id, original_job_id,
		       status, initiated_by, initiated_at, approval_id, rollback_trace_id,
		       plan_json, result_output, completed_at, created_at, updated_at
		FROM rollback_records
		WHERE original_event_id = ?
		  AND status NOT IN ('failed', 'cancelled')
		ORDER BY created_at DESC
		LIMIT 1
	`), originalEventID)
	r, err := scanRollbackRecord(row)
	if err != nil && strings.Contains(err.Error(), "not found") {
		return nil, nil
	}
	return r, err
}

// UpdateRollbackStatus transitions the status of a rollback record.
// output is optional (pass "" to leave unchanged). Sets CompletedAt for terminal states.
func (s *RollbackStore) UpdateRollbackStatus(ctx context.Context, rollbackID, status, output string) error {
	now := time.Now().UTC()
	var completedAt interface{}
	if status == "success" || status == "failed" || status == "cancelled" {
		completedAt = now.Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		UPDATE rollback_records
		SET status = ?,
		    result_output = CASE WHEN ? != '' THEN ? ELSE result_output END,
		    completed_at  = CASE WHEN ? IS NOT NULL THEN ? ELSE completed_at END,
		    updated_at    = ?
		WHERE rollback_id = ?
	`),
		status,
		output, output,
		completedAt, completedAt,
		now.Format(time.RFC3339Nano),
		rollbackID,
	)
	return err
}

// SetRollbackApprovalID links an approval record to a pending rollback.
func (s *RollbackStore) SetRollbackApprovalID(ctx context.Context, rollbackID, approvalID string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		UPDATE rollback_records SET approval_id = ?, updated_at = ? WHERE rollback_id = ?
	`), approvalID, now.Format(time.RFC3339Nano), rollbackID)
	return err
}

// ListRollbacks returns rollback records matching the given options, newest first.
func (s *RollbackStore) ListRollbacks(ctx context.Context, opts RollbackQueryOptions) ([]*RollbackRecord, error) {
	query := `SELECT rollback_id, original_event_id, original_trace_id, original_job_id,
		       status, initiated_by, initiated_at, approval_id, rollback_trace_id,
		       plan_json, result_output, completed_at, created_at, updated_at
		FROM rollback_records WHERE 1=1`
	var args []any

	if opts.OriginalEventID != "" {
		query += " AND original_event_id = ?"
		args = append(args, opts.OriginalEventID)
	}
	if opts.OriginalTraceID != "" {
		query += " AND original_trace_id = ?"
		args = append(args, opts.OriginalTraceID)
	}
	if opts.OriginalJobID != "" {
		query += " AND original_job_id = ?"
		args = append(args, opts.OriginalJobID)
	}
	if opts.Status != "" {
		query += " AND status = ?"
		args = append(args, opts.Status)
	}
	if opts.InitiatedBy != "" {
		query += " AND initiated_by = ?"
		args = append(args, opts.InitiatedBy)
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

	var records []*RollbackRecord
	for rows.Next() {
		r, err := scanRollbackRecordFromRows(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// --- Fleet rollback ---

// CreateFleetRollback inserts a new fleet rollback record.
func (s *RollbackStore) CreateFleetRollback(ctx context.Context, r *FleetRollbackRecord) error {
	if r.FleetRollbackID == "" {
		r.FleetRollbackID = "frb_" + uuid.New().String()[:8]
	}
	if r.Status == "" {
		r.Status = "pending_approval"
	}
	if r.Scope == "" {
		r.Scope = "all"
	}
	now := time.Now().UTC()
	r.CreatedAt = now
	r.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		INSERT INTO fleet_rollback_records
			(fleet_rollback_id, original_job_id, status, initiated_by,
			 approval_id, scope, rollback_job_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`),
		r.FleetRollbackID,
		r.OriginalJobID,
		r.Status,
		r.InitiatedBy,
		nullableString(r.ApprovalID),
		r.Scope,
		nullableString(r.RollbackJobID),
		r.CreatedAt.Format(time.RFC3339Nano),
		r.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

// GetFleetRollback retrieves a fleet rollback record by fleet_rollback_id.
func (s *RollbackStore) GetFleetRollback(ctx context.Context, fleetRollbackID string) (*FleetRollbackRecord, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
		SELECT fleet_rollback_id, original_job_id, status, initiated_by,
		       approval_id, scope, rollback_job_id, created_at, updated_at
		FROM fleet_rollback_records WHERE fleet_rollback_id = ?
	`), fleetRollbackID)
	return scanFleetRollbackRecord(row)
}

// GetFleetRollbackByJobID retrieves the most recent active fleet rollback for a job.
func (s *RollbackStore) GetFleetRollbackByJobID(ctx context.Context, originalJobID string) (*FleetRollbackRecord, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
		SELECT fleet_rollback_id, original_job_id, status, initiated_by,
		       approval_id, scope, rollback_job_id, created_at, updated_at
		FROM fleet_rollback_records
		WHERE original_job_id = ?
		  AND status NOT IN ('failed', 'cancelled')
		ORDER BY created_at DESC
		LIMIT 1
	`), originalJobID)
	r, err := scanFleetRollbackRecord(row)
	if err != nil && strings.Contains(err.Error(), "not found") {
		return nil, nil
	}
	return r, err
}

// UpdateFleetRollbackStatus updates the status (and optionally rollback_job_id) of a fleet rollback.
func (s *RollbackStore) UpdateFleetRollbackStatus(ctx context.Context, fleetRollbackID, status, rollbackJobID string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		UPDATE fleet_rollback_records
		SET status = ?,
		    rollback_job_id = CASE WHEN ? != '' THEN ? ELSE rollback_job_id END,
		    updated_at = ?
		WHERE fleet_rollback_id = ?
	`),
		status,
		rollbackJobID, rollbackJobID,
		now.Format(time.RFC3339Nano),
		fleetRollbackID,
	)
	return err
}

// --- scan helpers ---

type rollbackRow interface {
	Scan(dest ...any) error
}

func scanRollbackRecord(row rollbackRow) (*RollbackRecord, error) {
	var r RollbackRecord
	var originalTraceID, originalJobID, approvalID, resultOutput sql.NullString
	var initiatedAt, completedAt, createdAt, updatedAt string
	var completedAtNull sql.NullString

	err := row.Scan(
		&r.RollbackID, &r.OriginalEventID, &originalTraceID, &originalJobID,
		&r.Status, &r.InitiatedBy, &initiatedAt, &approvalID, &r.RollbackTraceID,
		&r.PlanJSON, &resultOutput, &completedAtNull, &createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("rollback record not found")
		}
		return nil, err
	}
	_ = completedAt
	r.OriginalTraceID = originalTraceID.String
	r.OriginalJobID = originalJobID.String
	r.ApprovalID = approvalID.String
	r.ResultOutput = resultOutput.String
	r.InitiatedAt, _ = time.Parse(time.RFC3339Nano, initiatedAt)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if completedAtNull.Valid {
		r.CompletedAt, _ = time.Parse(time.RFC3339Nano, completedAtNull.String)
	}
	return &r, nil
}

func scanRollbackRecordFromRows(rows *sql.Rows) (*RollbackRecord, error) {
	var r RollbackRecord
	var originalTraceID, originalJobID, approvalID, resultOutput sql.NullString
	var initiatedAt, createdAt, updatedAt string
	var completedAtNull sql.NullString

	err := rows.Scan(
		&r.RollbackID, &r.OriginalEventID, &originalTraceID, &originalJobID,
		&r.Status, &r.InitiatedBy, &initiatedAt, &approvalID, &r.RollbackTraceID,
		&r.PlanJSON, &resultOutput, &completedAtNull, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.OriginalTraceID = originalTraceID.String
	r.OriginalJobID = originalJobID.String
	r.ApprovalID = approvalID.String
	r.ResultOutput = resultOutput.String
	r.InitiatedAt, _ = time.Parse(time.RFC3339Nano, initiatedAt)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if completedAtNull.Valid {
		r.CompletedAt, _ = time.Parse(time.RFC3339Nano, completedAtNull.String)
	}
	return &r, nil
}

type fleetRollbackRow interface {
	Scan(dest ...any) error
}

func scanFleetRollbackRecord(row fleetRollbackRow) (*FleetRollbackRecord, error) {
	var r FleetRollbackRecord
	var approvalID, rollbackJobID sql.NullString
	var createdAt, updatedAt string

	err := row.Scan(
		&r.FleetRollbackID, &r.OriginalJobID, &r.Status, &r.InitiatedBy,
		&approvalID, &r.Scope, &rollbackJobID, &createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("fleet rollback record not found")
		}
		return nil, err
	}
	r.ApprovalID = approvalID.String
	r.RollbackJobID = rollbackJobID.String
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &r, nil
}
