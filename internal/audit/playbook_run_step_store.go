package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// PlaybookRunStep records one proposed (and optionally executed) action within
// an agent_approve playbook run. Each step is proposed by the re-planning LLM,
// surfaced to the operator for approval, then executed by the gateway.
type PlaybookRunStep struct {
	RunID      string         `json:"run_id"`
	StepIndex  int            `json:"step_index"`
	Agent      string         `json:"agent"`
	Tool       string         `json:"tool"`
	Args       map[string]any `json:"args"`
	Reason     string         `json:"reason,omitempty"`
	// Status lifecycle: proposed → approved|denied → executing → succeeded|failed
	Status     string `json:"status"`
	ApprovalID string `json:"approval_id,omitempty"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// PlaybookRunStepStore persists per-step records for agent_approve runs.
// It shares the same *sql.DB connection as the other audit stores.
type PlaybookRunStepStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewPlaybookRunStepStore creates the playbook_run_steps table if absent and
// returns a ready-to-use store.
func NewPlaybookRunStepStore(db *sql.DB, isPostgres bool) (*PlaybookRunStepStore, error) {
	s := &PlaybookRunStepStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create playbook_run_steps schema: %w", err)
	}
	return s, nil
}

func (s *PlaybookRunStepStore) createSchema() error {
	pk := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if s.isPostgres {
		pk = "BIGSERIAL PRIMARY KEY"
	}
	schema := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS playbook_run_steps (
    id          %s,
    run_id      TEXT NOT NULL,
    step_index  INTEGER NOT NULL,
    agent       TEXT NOT NULL,
    tool        TEXT NOT NULL,
    args        TEXT NOT NULL DEFAULT '{}',
    reason      TEXT,
    status      TEXT NOT NULL DEFAULT 'proposed',
    approval_id TEXT,
    result      TEXT,
    error       TEXT,
    created_at  TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL DEFAULT '',
    UNIQUE(run_id, step_index)
);
CREATE INDEX IF NOT EXISTS idx_run_steps_run    ON playbook_run_steps(run_id);
CREATE INDEX IF NOT EXISTS idx_run_steps_status ON playbook_run_steps(run_id, status);
`, pk)
	_, err := s.db.Exec(schema)
	return err
}

// CreateStep inserts a newly proposed step. StepIndex must be unique per run.
func (s *PlaybookRunStepStore) CreateStep(ctx context.Context, step *PlaybookRunStep) error {
	now := time.Now().UTC()
	step.CreatedAt = now
	step.UpdatedAt = now
	if step.Status == "" {
		step.Status = "proposed"
	}

	argsJSON, err := json.Marshal(step.Args)
	if err != nil {
		argsJSON = []byte("{}")
	}

	_, err = s.db.ExecContext(ctx, rebind(s.isPostgres, `
		INSERT INTO playbook_run_steps
			(run_id, step_index, agent, tool, args, reason, status, approval_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`),
		step.RunID, step.StepIndex, step.Agent, step.Tool,
		string(argsJSON), step.Reason, step.Status,
		nullableString(step.ApprovalID),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	return err
}

// UpdateStep updates status, approval_id, result, and error for a step.
func (s *PlaybookRunStepStore) UpdateStep(ctx context.Context, runID string, stepIndex int, status, approvalID, result, stepErr string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		UPDATE playbook_run_steps
		SET status = ?, approval_id = ?, result = ?, error = ?, updated_at = ?
		WHERE run_id = ? AND step_index = ?
	`),
		status,
		nullableString(approvalID),
		nullableString(result),
		nullableString(stepErr),
		now.Format(time.RFC3339Nano),
		runID, stepIndex,
	)
	return err
}

// GetPendingStep returns the step with status 'proposed' or 'approved' for a run.
// Returns (nil, nil) if no pending step exists.
func (s *PlaybookRunStepStore) GetPendingStep(ctx context.Context, runID string) (*PlaybookRunStep, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
		SELECT run_id, step_index, agent, tool, args, reason, status, approval_id,
		       result, error, created_at, updated_at
		FROM playbook_run_steps
		WHERE run_id = ? AND status IN ('proposed', 'approved', 'executing')
		ORDER BY step_index ASC LIMIT 1
	`), runID)
	return scanRunStep(row)
}

// ListSteps returns all steps for a run ordered by step_index.
func (s *PlaybookRunStepStore) ListSteps(ctx context.Context, runID string) ([]*PlaybookRunStep, error) {
	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, `
		SELECT run_id, step_index, agent, tool, args, reason, status, approval_id,
		       result, error, created_at, updated_at
		FROM playbook_run_steps
		WHERE run_id = ?
		ORDER BY step_index ASC
	`), runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var steps []*PlaybookRunStep
	for rows.Next() {
		step, err := scanRunStepFromRows(rows)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

// NextStepIndex returns the next available step_index for a run (max + 1, or 1).
func (s *PlaybookRunStepStore) NextStepIndex(ctx context.Context, runID string) (int, error) {
	var maxIdx sql.NullInt64
	err := s.db.QueryRowContext(ctx, rebind(s.isPostgres,
		`SELECT MAX(step_index) FROM playbook_run_steps WHERE run_id = ?`,
	), runID).Scan(&maxIdx)
	if err != nil {
		return 0, err
	}
	if !maxIdx.Valid {
		return 1, nil
	}
	return int(maxIdx.Int64) + 1, nil
}

func scanRunStep(row *sql.Row) (*PlaybookRunStep, error) {
	var step PlaybookRunStep
	var argsJSON string
	var reason, approvalID, result, stepErr sql.NullString
	var createdAt, updatedAt string

	err := row.Scan(
		&step.RunID, &step.StepIndex, &step.Agent, &step.Tool,
		&argsJSON, &reason, &step.Status, &approvalID,
		&result, &stepErr, &createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(argsJSON), &step.Args); err != nil {
		step.Args = map[string]any{}
	}
	step.Reason = reason.String
	step.ApprovalID = approvalID.String
	step.Result = result.String
	step.Error = stepErr.String
	step.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	step.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &step, nil
}

func scanRunStepFromRows(rows *sql.Rows) (*PlaybookRunStep, error) {
	var step PlaybookRunStep
	var argsJSON string
	var reason, approvalID, result, stepErr sql.NullString
	var createdAt, updatedAt string

	err := rows.Scan(
		&step.RunID, &step.StepIndex, &step.Agent, &step.Tool,
		&argsJSON, &reason, &step.Status, &approvalID,
		&result, &stepErr, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(argsJSON), &step.Args); err != nil {
		step.Args = map[string]any{}
	}
	step.Reason = reason.String
	step.ApprovalID = approvalID.String
	step.Result = result.String
	step.Error = stepErr.String
	step.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	step.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &step, nil
}

