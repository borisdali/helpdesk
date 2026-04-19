package audit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PlaybookRun records a single execution of a playbook.
type PlaybookRun struct {
	RunID          string    `json:"run_id"`
	PlaybookID     string    `json:"playbook_id"`
	SeriesID       string    `json:"series_id"`
	ExecutionMode  string    `json:"execution_mode"` // "fleet" | "agent"
	Outcome        string    `json:"outcome"`        // "resolved" | "escalated" | "abandoned" | "unknown"
	EscalatedTo    string    `json:"escalated_to,omitempty"`  // series_id of next playbook
	FindingsSummary string   `json:"findings_summary,omitempty"` // agent summary at handoff
	ContextID      string    `json:"context_id,omitempty"`   // A2A session ID
	Operator       string    `json:"operator"`
	StartedAt      time.Time `json:"started_at"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
}

// PlaybookRunStats summarises run history for a playbook series.
type PlaybookRunStats struct {
	SeriesID       string  `json:"series_id"`
	TotalRuns      int     `json:"total_runs"`
	Resolved       int     `json:"resolved"`
	Escalated      int     `json:"escalated"`
	Abandoned      int     `json:"abandoned"`
	EscalationRate float64 `json:"escalation_rate"` // escalated / total_runs
	ResolutionRate float64 `json:"resolution_rate"` // resolved / total_runs
	LastRunAt      string  `json:"last_run_at,omitempty"`
}

// PlaybookRunStore persists playbook execution records.
type PlaybookRunStore struct {
	db *sql.DB
}

// NewPlaybookRunStore creates the playbook_runs table (if absent) and returns a
// ready-to-use PlaybookRunStore.
func NewPlaybookRunStore(db *sql.DB) (*PlaybookRunStore, error) {
	s := &PlaybookRunStore{db: db}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create playbook_run schema: %w", err)
	}
	return s, nil
}

func (s *PlaybookRunStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS playbook_runs (
    run_id           TEXT     NOT NULL PRIMARY KEY,
    playbook_id      TEXT     NOT NULL,
    series_id        TEXT     NOT NULL,
    execution_mode   TEXT     NOT NULL DEFAULT 'fleet',
    outcome          TEXT     NOT NULL DEFAULT 'unknown',
    escalated_to     TEXT     NOT NULL DEFAULT '',
    findings_summary TEXT     NOT NULL DEFAULT '',
    context_id       TEXT     NOT NULL DEFAULT '',
    operator         TEXT     NOT NULL DEFAULT '',
    started_at       DATETIME NOT NULL,
    completed_at     DATETIME NOT NULL DEFAULT ''
)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_playbook_runs_series_time
    ON playbook_runs(series_id, started_at)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_playbook_runs_playbook
    ON playbook_runs(playbook_id)`)
	return err
}

// Record inserts a new PlaybookRun. RunID is generated if empty.
func (s *PlaybookRunStore) Record(ctx context.Context, r *PlaybookRun) error {
	if r.RunID == "" {
		r.RunID = "plr_" + uuid.New().String()[:8]
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	outcome := r.Outcome
	if outcome == "" {
		outcome = "unknown"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO playbook_runs
		    (run_id, playbook_id, series_id, execution_mode, outcome,
		     escalated_to, findings_summary, context_id, operator, started_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RunID, r.PlaybookID, r.SeriesID, r.ExecutionMode, outcome,
		r.EscalatedTo, r.FindingsSummary, r.ContextID, r.Operator,
		r.StartedAt.Format("2006-01-02 15:04:05"),
		formatNullableTime(r.CompletedAt),
	)
	return err
}

// Update sets outcome, escalated_to, findings_summary, and completed_at for an
// existing run. Used when the agent session concludes.
func (s *PlaybookRunStore) Update(ctx context.Context, runID, outcome, escalatedTo, findingsSummary string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE playbook_runs
		 SET outcome = ?, escalated_to = ?, findings_summary = ?, completed_at = ?
		 WHERE run_id = ?`,
		outcome, escalatedTo, findingsSummary,
		time.Now().UTC().Format("2006-01-02 15:04:05"),
		runID,
	)
	return err
}

// Stats returns aggregated run statistics for a playbook series.
func (s *PlaybookRunStore) Stats(ctx context.Context, seriesID string) (*PlaybookRunStats, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
		    COUNT(*)                                                        AS total,
		    COALESCE(SUM(CASE WHEN outcome = 'resolved'  THEN 1 ELSE 0 END), 0) AS resolved,
		    COALESCE(SUM(CASE WHEN outcome = 'escalated' THEN 1 ELSE 0 END), 0) AS escalated,
		    COALESCE(SUM(CASE WHEN outcome = 'abandoned' THEN 1 ELSE 0 END), 0) AS abandoned,
		    MAX(started_at)                                                 AS last_run_at
		FROM playbook_runs
		WHERE series_id = ?`, seriesID)

	var total, resolved, escalated, abandoned int
	var lastRunAt sql.NullString
	if err := row.Scan(&total, &resolved, &escalated, &abandoned, &lastRunAt); err != nil {
		return nil, err
	}

	st := &PlaybookRunStats{
		SeriesID:  seriesID,
		TotalRuns: total,
		Resolved:  resolved,
		Escalated: escalated,
		Abandoned: abandoned,
	}
	if total > 0 {
		st.ResolutionRate = float64(resolved) / float64(total)
		st.EscalationRate = float64(escalated) / float64(total)
	}
	if lastRunAt.Valid && lastRunAt.String != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", lastRunAt.String); err == nil {
			st.LastRunAt = t.UTC().Format(time.RFC3339)
		} else {
			st.LastRunAt = lastRunAt.String
		}
	}
	return st, nil
}

// StatsBatch returns stats for multiple series IDs in one query.
func (s *PlaybookRunStore) StatsBatch(ctx context.Context, seriesIDs []string) (map[string]*PlaybookRunStats, error) {
	if len(seriesIDs) == 0 {
		return map[string]*PlaybookRunStats{}, nil
	}

	placeholders := strings.Repeat("?,", len(seriesIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(seriesIDs))
	for i, id := range seriesIDs {
		args[i] = id
	}

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT
		    series_id,
		    COUNT(*)                                              AS total,
		    SUM(CASE WHEN outcome = 'resolved'  THEN 1 ELSE 0 END) AS resolved,
		    SUM(CASE WHEN outcome = 'escalated' THEN 1 ELSE 0 END) AS escalated,
		    SUM(CASE WHEN outcome = 'abandoned' THEN 1 ELSE 0 END) AS abandoned,
		    MAX(started_at)                                       AS last_run_at
		FROM playbook_runs
		WHERE series_id IN (%s)
		GROUP BY series_id`, placeholders), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*PlaybookRunStats, len(seriesIDs))
	for rows.Next() {
		var st PlaybookRunStats
		var lastRunAt sql.NullString
		if err := rows.Scan(&st.SeriesID, &st.TotalRuns, &st.Resolved, &st.Escalated, &st.Abandoned, &lastRunAt); err != nil {
			return nil, err
		}
		if st.TotalRuns > 0 {
			st.ResolutionRate = float64(st.Resolved) / float64(st.TotalRuns)
			st.EscalationRate = float64(st.Escalated) / float64(st.TotalRuns)
		}
		if lastRunAt.Valid && lastRunAt.String != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", lastRunAt.String); err == nil {
				st.LastRunAt = t.UTC().Format(time.RFC3339)
			} else {
				st.LastRunAt = lastRunAt.String
			}
		}
		result[st.SeriesID] = &st
	}
	return result, rows.Err()
}

// GetByRunID returns a single PlaybookRun by its run_id.
// Returns sql.ErrNoRows if not found.
func (s *PlaybookRunStore) GetByRunID(ctx context.Context, runID string) (*PlaybookRun, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT run_id, playbook_id, series_id, execution_mode, outcome,
		       escalated_to, findings_summary, context_id, operator, started_at, completed_at
		FROM playbook_runs
		WHERE run_id = ?`, runID)
	return scanPlaybookRun(row)
}

// ListByPlaybook returns runs for a specific playbook_id, most recent first.
func (s *PlaybookRunStore) ListByPlaybook(ctx context.Context, playbookID string, limit int) ([]*PlaybookRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT run_id, playbook_id, series_id, execution_mode, outcome,
		       escalated_to, findings_summary, context_id, operator, started_at, completed_at
		FROM playbook_runs
		WHERE playbook_id = ?
		ORDER BY started_at DESC
		LIMIT %d`, limit), playbookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlaybookRuns(rows)
}

func scanPlaybookRuns(rows *sql.Rows) ([]*PlaybookRun, error) {
	var runs []*PlaybookRun
	for rows.Next() {
		r, err := scanPlaybookRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

type playbookRunScanner interface {
	Scan(dest ...any) error
}

func scanPlaybookRun(s playbookRunScanner) (*PlaybookRun, error) {
	var r PlaybookRun
	var startedStr, completedStr string
	if err := s.Scan(
		&r.RunID, &r.PlaybookID, &r.SeriesID, &r.ExecutionMode, &r.Outcome,
		&r.EscalatedTo, &r.FindingsSummary, &r.ContextID, &r.Operator,
		&startedStr, &completedStr,
	); err != nil {
		return nil, err
	}
	r.StartedAt = parseFlexTime(startedStr)
	r.CompletedAt = parseFlexTime(completedStr)
	return &r, nil
}

// formatNullableTime returns an empty string for the zero value, otherwise the
// formatted time — matches how SQLite stores empty optional datetimes.
func formatNullableTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}
