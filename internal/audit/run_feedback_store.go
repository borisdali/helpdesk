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
type FeedbackStats struct {
	SeriesID      string  `json:"series_id"`
	FeedbackCount int     `json:"feedback_count"`
	CorrectCount  int     `json:"correct_count"`
	AccuracyRate  float64 `json:"accuracy_rate"` // correct_count / feedback_count; 0 when no feedback
}

// RunFeedbackStore persists operator feedback on playbook run quality.
type RunFeedbackStore struct {
	db *sql.DB
}

// NewRunFeedbackStore creates the run_feedback table (if absent) and returns a
// ready-to-use RunFeedbackStore.
func NewRunFeedbackStore(db *sql.DB) (*RunFeedbackStore, error) {
	s := &RunFeedbackStore{db: db}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create run_feedback schema: %w", err)
	}
	return s, nil
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
    submitted_at    DATETIME NOT NULL,
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
	_, err := s.db.ExecContext(ctx, `
INSERT INTO run_feedback (run_id, feedback_type, feedback_time, series_id, verdict_correct, verdict_notes, operator, submitted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id, feedback_type, feedback_time) DO UPDATE SET
    series_id       = excluded.series_id,
    verdict_correct = excluded.verdict_correct,
    verdict_notes   = excluded.verdict_notes,
    operator        = excluded.operator,
    submitted_at    = excluded.submitted_at`,
		fb.RunID, fb.FeedbackType, fb.FeedbackTime, fb.SeriesID, verdictInt, fb.VerdictNotes, fb.Operator,
		fb.SubmittedAt.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return fmt.Errorf("submit run feedback: %w", err)
	}
	return nil
}

// GetByRunIDAndType returns a specific feedback record by run_id, type, and time.
// Returns sql.ErrNoRows when no matching record exists.
func (s *RunFeedbackStore) GetByRunIDAndType(ctx context.Context, runID, feedbackType, feedbackTime string) (*RunFeedback, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT run_id, feedback_type, feedback_time, series_id, verdict_correct, verdict_notes, operator, submitted_at
FROM run_feedback WHERE run_id = ? AND feedback_type = ? AND feedback_time = ?`,
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
	rows, err := s.db.QueryContext(ctx, `
SELECT run_id, feedback_type, feedback_time, series_id, verdict_correct, verdict_notes, operator, submitted_at
FROM run_feedback
WHERE verdict_correct IS NULL
  AND feedback_type = 'triage'
  AND feedback_time = 'post_incident'
ORDER BY submitted_at DESC`)
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

// StatsBySeries returns accuracy aggregates for post-incident triage feedback
// for a playbook series. Only counts records where verdict_correct is set;
// placeholder records (verdict_correct IS NULL) are excluded.
func (s *RunFeedbackStore) StatsBySeries(ctx context.Context, seriesID string) (*FeedbackStats, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT
    COUNT(*) AS feedback_count,
    COALESCE(SUM(CASE WHEN verdict_correct = 1 THEN 1 ELSE 0 END), 0) AS correct_count
FROM run_feedback
WHERE series_id = ?
  AND feedback_type = 'triage'
  AND feedback_time = 'post_incident'
  AND verdict_correct IS NOT NULL`, seriesID)
	var total, correct int
	if err := row.Scan(&total, &correct); err != nil {
		return nil, fmt.Errorf("stats by series: %w", err)
	}
	rate := 0.0
	if total > 0 {
		rate = float64(correct) / float64(total)
	}
	return &FeedbackStats{
		SeriesID:      seriesID,
		FeedbackCount: total,
		CorrectCount:  correct,
		AccuracyRate:  rate,
	}, nil
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
		if t, err := time.Parse(sqliteTimeFormat, submittedStr); err == nil {
			fb.SubmittedAt = t
		}
	}
	return &fb, nil
}
