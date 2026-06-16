package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RunFeedback captures operator assessment of a playbook run quality.
// FeedbackType distinguishes what is being assessed: "triage" or "remediation".
// FeedbackTime distinguishes when: "at_gate" (before remediation) or "post_incident" (after recovery).
type RunFeedback struct {
	RunID          string    `json:"run_id"`
	FeedbackType   string    `json:"feedback_type"`          // "triage" | "remediation"
	FeedbackTime   string    `json:"feedback_time"`          // "at_gate" | "post_incident"
	SeriesID       string    `json:"series_id"`
	VerdictCorrect *bool     `json:"verdict_correct,omitempty"` // nil = not yet submitted
	VerdictNotes   string    `json:"verdict_notes,omitempty"`
	Operator       string    `json:"operator"`
	SubmittedAt    time.Time `json:"submitted_at"`
}

// FeedbackStats aggregates feedback quality metrics for a playbook series.
// Both at-gate and post-incident feedback are counted per type.
// AccuracyRate covers triage only; AtGate* and PostIncident* give the per-time
// breakdown for triage. Remediation* fields cover remediation feedback.
type FeedbackStats struct {
	SeriesID      string  `json:"series_id"`
	FeedbackCount int     `json:"feedback_count"` // triage total across both feedback times
	CorrectCount  int     `json:"correct_count"`
	AccuracyRate  float64 `json:"accuracy_rate"`

	AtGateCount        int     `json:"at_gate_count"`
	AtGateCorrect      int     `json:"at_gate_correct"`
	AtGateAccuracyRate float64 `json:"at_gate_accuracy_rate,omitempty"`

	PostIncidentCount        int     `json:"post_incident_count"`
	PostIncidentCorrect      int     `json:"post_incident_correct"`
	PostIncidentAccuracyRate float64 `json:"post_incident_accuracy_rate,omitempty"`

	// Remediation feedback (feedback_type='remediation').
	RemediationFeedbackCount      int     `json:"remediation_feedback_count"`
	RemediationCorrectCount       int     `json:"remediation_correct_count"`
	RemediationAccuracyRate       float64 `json:"remediation_accuracy_rate,omitempty"`
	RemediationAtGateCount        int     `json:"remediation_at_gate_count"`
	RemediationAtGateCorrect      int     `json:"remediation_at_gate_correct"`
	RemediationPostIncidentCount  int     `json:"remediation_post_incident_count"`
	RemediationPostIncidentCorrect int    `json:"remediation_post_incident_correct"`
}

// RunFeedbackStore persists operator feedback on playbook run quality.
type RunFeedbackStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewRunFeedbackStore creates (or migrates) the run_feedback table and returns a
// ready-to-use RunFeedbackStore.
func NewRunFeedbackStore(db *sql.DB, isPostgres bool) (*RunFeedbackStore, error) {
	s := &RunFeedbackStore{db: db, isPostgres: isPostgres}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate run_feedback schema: %w", err)
	}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create run_feedback schema: %w", err)
	}
	return s, nil
}

// migrate upgrades the run_feedback table from the v1 schema (single PK on run_id,
// columns diagnosis_correct/actual_root_cause) to the v2 schema (composite PK on
// run_id/feedback_type/feedback_time, columns verdict_correct/verdict_notes).
// SQLite does not support altering a PRIMARY KEY, so migration recreates the table.
// The function is idempotent: it is a no-op when the table is absent, already on v2,
// or when using PostgreSQL (no old schema would exist there).
func (s *RunFeedbackStore) migrate() error {
	if s.isPostgres {
		return nil
	}

	// Nothing to migrate if the table does not exist yet.
	var tableCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='run_feedback'`,
	).Scan(&tableCount); err != nil || tableCount == 0 {
		return nil
	}

	// Already on v2 if feedback_type column is present.
	var colCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('run_feedback') WHERE name='feedback_type'`,
	).Scan(&colCount); err != nil || colCount > 0 {
		return nil
	}

	// v1 schema detected. Recreate in a single transaction with the new composite PK
	// and renamed columns (diagnosis_correct→verdict_correct, actual_root_cause→verdict_notes).
	// Existing rows are migrated as (feedback_type='triage', feedback_time='post_incident').
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, stmt := range []string{
		`DROP TABLE IF EXISTS run_feedback_new`,
		`CREATE TABLE run_feedback_new (
		    run_id          TEXT     NOT NULL,
		    feedback_type   TEXT     NOT NULL DEFAULT 'triage',
		    feedback_time   TEXT     NOT NULL DEFAULT 'post_incident',
		    series_id       TEXT     NOT NULL DEFAULT '',
		    verdict_correct INTEGER,
		    verdict_notes   TEXT     NOT NULL DEFAULT '',
		    operator        TEXT     NOT NULL DEFAULT '',
		    submitted_at    TEXT     NOT NULL DEFAULT '',
		    PRIMARY KEY (run_id, feedback_type, feedback_time)
		)`,
		`INSERT OR IGNORE INTO run_feedback_new
		    (run_id, feedback_type, feedback_time, series_id, verdict_correct, verdict_notes, operator, submitted_at)
		 SELECT run_id, 'triage', 'post_incident',
		        COALESCE(series_id, ''),
		        diagnosis_correct,
		        COALESCE(actual_root_cause, ''),
		        COALESCE(operator, ''),
		        submitted_at
		 FROM run_feedback`,
		`DROP TABLE run_feedback`,
		`ALTER TABLE run_feedback_new RENAME TO run_feedback`,
		`CREATE INDEX IF NOT EXISTS idx_run_feedback_series ON run_feedback(series_id)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("stmt %q: %w", stmt[:min(40, len(stmt))], err)
		}
	}
	return tx.Commit()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *RunFeedbackStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS run_feedback (
    run_id          TEXT     NOT NULL,
    feedback_type   TEXT     NOT NULL DEFAULT 'triage',
    feedback_time   TEXT     NOT NULL DEFAULT 'post_incident',
    series_id       TEXT     NOT NULL DEFAULT '',
    verdict_correct INTEGER,
    verdict_notes   TEXT     NOT NULL DEFAULT '',
    operator        TEXT     NOT NULL DEFAULT '',
    submitted_at    TEXT     NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, feedback_type, feedback_time)
)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_run_feedback_series
    ON run_feedback(series_id)`)
	return err
}

// Submit upserts feedback for a run. Calling Submit twice for the same
// (run_id, feedback_type, feedback_time) overwrites the previous entry;
// different feedback_type/feedback_time combinations are stored as separate rows.
func (s *RunFeedbackStore) Submit(ctx context.Context, fb *RunFeedback) error {
	if fb.SubmittedAt.IsZero() {
		fb.SubmittedAt = time.Now().UTC()
	}
	if fb.FeedbackType == "" {
		fb.FeedbackType = "triage"
	}
	if fb.FeedbackTime == "" {
		fb.FeedbackTime = "post_incident"
	}
	var verdictInt *int
	if fb.VerdictCorrect != nil {
		v := 0
		if *fb.VerdictCorrect {
			v = 1
		}
		verdictInt = &v
	}
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
INSERT INTO run_feedback (run_id, feedback_type, feedback_time, series_id, verdict_correct, verdict_notes, operator, submitted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id, feedback_type, feedback_time) DO UPDATE SET
    series_id       = excluded.series_id,
    verdict_correct = excluded.verdict_correct,
    verdict_notes   = excluded.verdict_notes,
    operator        = excluded.operator,
    submitted_at    = excluded.submitted_at`),
		fb.RunID, fb.FeedbackType, fb.FeedbackTime, fb.SeriesID, verdictInt, fb.VerdictNotes, fb.Operator,
		fb.SubmittedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("submit run feedback: %w", err)
	}
	return nil
}

// GetByRunIDAndType returns a specific feedback record by run_id, type, and time.
// Returns sql.ErrNoRows when no matching record exists.
func (s *RunFeedbackStore) GetByRunIDAndType(ctx context.Context, runID, feedbackType, feedbackTime string) (*RunFeedback, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
SELECT run_id, feedback_type, feedback_time, series_id, verdict_correct, verdict_notes, operator, submitted_at
FROM run_feedback WHERE run_id = ? AND feedback_type = ? AND feedback_time = ?`),
		runID, feedbackType, feedbackTime)
	return scanRunFeedback(row)
}

// GetByRunID returns the post-incident triage feedback for a run.
// This is the feedback type managed via the Decision Hub pending queue.
// Returns sql.ErrNoRows when no record exists.
func (s *RunFeedbackStore) GetByRunID(ctx context.Context, runID string) (*RunFeedback, error) {
	return s.GetByRunIDAndType(ctx, runID, "triage", "post_incident")
}

// ListPending returns RunFeedback records for post-incident triage feedback
// where verdict_correct has not been set yet (placeholder records created by
// request-feedback calls, awaiting operator resolution via the Decision Hub).
func (s *RunFeedbackStore) ListPending(ctx context.Context) ([]*RunFeedback, error) {
	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, `
SELECT run_id, feedback_type, feedback_time, series_id, verdict_correct, verdict_notes, operator, submitted_at
FROM run_feedback
WHERE verdict_correct IS NULL
  AND feedback_type = 'triage'
  AND feedback_time = 'post_incident'
ORDER BY submitted_at DESC`))
	if err != nil {
		return nil, fmt.Errorf("list pending feedback: %w", err)
	}
	defer rows.Close()
	var out []*RunFeedback
	for rows.Next() {
		fb, err := scanRunFeedbackRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fb)
	}
	return out, rows.Err()
}

// StatsBySeries returns accuracy aggregates for triage feedback for a playbook
// series, broken down by feedback_time (at_gate / post_incident). Only counts
// records where verdict_correct is set; placeholder records are excluded.
func (s *RunFeedbackStore) StatsBySeries(ctx context.Context, seriesID string) (*FeedbackStats, error) {
	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, `
SELECT
    feedback_time,
    COUNT(*) AS total,
    COALESCE(SUM(CASE WHEN verdict_correct = 1 THEN 1 ELSE 0 END), 0) AS correct
FROM run_feedback
WHERE series_id = ?
  AND feedback_type = 'triage'
  AND verdict_correct IS NOT NULL
GROUP BY feedback_time`), seriesID)
	if err != nil {
		return nil, fmt.Errorf("stats by series: %w", err)
	}
	defer rows.Close()

	stats := &FeedbackStats{SeriesID: seriesID}
	for rows.Next() {
		var fbTime string
		var total, correct int
		if err := rows.Scan(&fbTime, &total, &correct); err != nil {
			return nil, fmt.Errorf("scan stats row: %w", err)
		}
		stats.FeedbackCount += total
		stats.CorrectCount += correct
		switch fbTime {
		case "at_gate":
			stats.AtGateCount = total
			stats.AtGateCorrect = correct
			if total > 0 {
				stats.AtGateAccuracyRate = float64(correct) / float64(total)
			}
		case "post_incident":
			stats.PostIncidentCount = total
			stats.PostIncidentCorrect = correct
			if total > 0 {
				stats.PostIncidentAccuracyRate = float64(correct) / float64(total)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if stats.FeedbackCount > 0 {
		stats.AccuracyRate = float64(stats.CorrectCount) / float64(stats.FeedbackCount)
	}

	// Second pass: remediation feedback for the same series.
	remRows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, `
SELECT
    feedback_time,
    COUNT(*) AS total,
    COALESCE(SUM(CASE WHEN verdict_correct = 1 THEN 1 ELSE 0 END), 0) AS correct
FROM run_feedback
WHERE series_id = ?
  AND feedback_type = 'remediation'
  AND verdict_correct IS NOT NULL
GROUP BY feedback_time`), seriesID)
	if err != nil {
		return nil, fmt.Errorf("remediation stats by series: %w", err)
	}
	defer remRows.Close()
	for remRows.Next() {
		var fbTime string
		var total, correct int
		if err := remRows.Scan(&fbTime, &total, &correct); err != nil {
			return nil, fmt.Errorf("scan remediation stats row: %w", err)
		}
		stats.RemediationFeedbackCount += total
		stats.RemediationCorrectCount += correct
		switch fbTime {
		case "at_gate":
			stats.RemediationAtGateCount = total
			stats.RemediationAtGateCorrect = correct
		case "post_incident":
			stats.RemediationPostIncidentCount = total
			stats.RemediationPostIncidentCorrect = correct
		}
	}
	if err := remRows.Err(); err != nil {
		return nil, err
	}
	if stats.RemediationFeedbackCount > 0 {
		stats.RemediationAccuracyRate = float64(stats.RemediationCorrectCount) / float64(stats.RemediationFeedbackCount)
	}

	return stats, nil
}

type feedbackScanner interface {
	Scan(dest ...any) error
}

func scanRunFeedback(row *sql.Row) (*RunFeedback, error) {
	return scanRunFeedbackRow(row)
}

func scanRunFeedbackRow(s feedbackScanner) (*RunFeedback, error) {
	var (
		fb           RunFeedback
		verdictInt   *int
		submittedStr string
	)
	if err := s.Scan(&fb.RunID, &fb.FeedbackType, &fb.FeedbackTime, &fb.SeriesID, &verdictInt, &fb.VerdictNotes, &fb.Operator, &submittedStr); err != nil {
		return nil, err
	}
	if verdictInt != nil {
		b := *verdictInt != 0
		fb.VerdictCorrect = &b
	}
	if submittedStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, submittedStr); err == nil {
			fb.SubmittedAt = t
		}
	}
	return &fb, nil
}
