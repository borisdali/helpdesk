package audit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ToolResult records the outcome of a single tool execution.
type PersistedToolResult struct {
	ResultID   string    `json:"result_id"`
	ServerName string    `json:"server_name"`
	ToolName   string    `json:"tool_name"`
	ToolArgs   string    `json:"tool_args"`   // JSON-encoded args
	Output     string    `json:"output"`      // raw tool output
	TraceID    string    `json:"trace_id,omitempty"`
	JobID      string    `json:"job_id,omitempty"`
	RecordedBy string    `json:"recorded_by"`
	RecordedAt time.Time `json:"recorded_at"`
	Success    bool      `json:"success"`
}

// ToolResultStore persists tool execution results for trend analysis and triage.
type ToolResultStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewToolResultStore creates the tool_results table (if absent) and returns a
// ready-to-use ToolResultStore.
func NewToolResultStore(db *sql.DB, isPostgres bool) (*ToolResultStore, error) {
	s := &ToolResultStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create tool_result schema: %w", err)
	}
	return s, nil
}

func (s *ToolResultStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS tool_results (
    result_id   TEXT     NOT NULL PRIMARY KEY,
    server_name TEXT     NOT NULL,
    tool_name   TEXT     NOT NULL,
    tool_args   TEXT     NOT NULL DEFAULT '{}',
    output      TEXT     NOT NULL DEFAULT '',
    trace_id    TEXT     NOT NULL DEFAULT '',
    job_id      TEXT     NOT NULL DEFAULT '',
    recorded_by TEXT     NOT NULL DEFAULT '',
    recorded_at DATETIME NOT NULL,
    success     INTEGER  NOT NULL DEFAULT 1
)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_tool_results_server_tool_time
    ON tool_results(server_name, tool_name, recorded_at)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_tool_results_job
    ON tool_results(job_id)`)
	return err
}

// Record inserts a new ToolResult. ResultID is generated if empty.
func (s *ToolResultStore) Record(ctx context.Context, r *PersistedToolResult) error {
	if r.ResultID == "" {
		r.ResultID = "res_" + uuid.New().String()[:8]
	}
	if r.RecordedAt.IsZero() {
		r.RecordedAt = time.Now().UTC()
	}
	successInt := 0
	if r.Success {
		successInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tool_results
		    (result_id, server_name, tool_name, tool_args, output, trace_id, job_id, recorded_by, recorded_at, success)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ResultID, r.ServerName, r.ToolName, r.ToolArgs, r.Output,
		r.TraceID, r.JobID, r.RecordedBy, r.RecordedAt, successInt,
	)
	return err
}

// ToolResultQuery holds filter parameters for listing tool results.
type ToolResultQuery struct {
	ServerName string
	ToolName   string
	JobID      string
	Since      time.Duration // e.g. 7*24*time.Hour
	Limit      int
}

// List queries tool results matching the given filters, ordered by
// recorded_at descending (most recent first).
func (s *ToolResultStore) List(ctx context.Context, q ToolResultQuery) ([]*PersistedToolResult, error) {
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	var where []string
	var args []any

	if q.ServerName != "" {
		where = append(where, "server_name = ?")
		args = append(args, q.ServerName)
	}
	if q.ToolName != "" {
		where = append(where, "tool_name = ?")
		args = append(args, q.ToolName)
	}
	if q.JobID != "" {
		where = append(where, "job_id = ?")
		args = append(args, q.JobID)
	}
	if q.Since > 0 {
		since := time.Now().UTC().Add(-q.Since)
		where = append(where, "recorded_at >= ?")
		args = append(args, since.Format(time.RFC3339Nano))
	}

	query := `SELECT result_id, server_name, tool_name, tool_args, output,
	                 trace_id, job_id, recorded_by, recorded_at, success
	          FROM tool_results`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += fmt.Sprintf(" ORDER BY recorded_at DESC LIMIT %d", limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*PersistedToolResult
	for rows.Next() {
		r, err := scanToolResult(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

type toolResultScanner interface {
	Scan(dest ...any) error
}

func scanToolResult(s toolResultScanner) (*PersistedToolResult, error) {
	var r PersistedToolResult
	var recordedAtStr string
	var successInt int
	if err := s.Scan(
		&r.ResultID, &r.ServerName, &r.ToolName, &r.ToolArgs, &r.Output,
		&r.TraceID, &r.JobID, &r.RecordedBy, &recordedAtStr, &successInt,
	); err != nil {
		return nil, err
	}
	r.RecordedAt = parseFlexTime(recordedAtStr)
	r.Success = successInt != 0
	return &r, nil
}
