package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RunFeedback captures operator confirmation of diagnosis quality after an incident.
type RunFeedback struct {
	RunID            string    `json:"run_id"`
	SeriesID         string    `json:"series_id"`
	DiagnosisCorrect *bool     `json:"diagnosis_correct,omitempty"` // nil = not submitted
	ActualRootCause  string    `json:"actual_root_cause,omitempty"`
	Operator         string    `json:"operator"`
	SubmittedAt      time.Time `json:"submitted_at"`
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
    run_id             TEXT     NOT NULL PRIMARY KEY,
    series_id          TEXT     NOT NULL DEFAULT '',
    diagnosis_correct  INTEGER,
    actual_root_cause  TEXT     NOT NULL DEFAULT '',
    operator           TEXT     NOT NULL DEFAULT '',
    submitted_at       DATETIME NOT NULL
)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_run_feedback_series
    ON run_feedback(series_id)`)
	return err
}

// Submit upserts feedback for a run. Calling Submit twice for the same run_id
// overwrites the previous entry.
func (s *RunFeedbackStore) Submit(ctx context.Context, fb *RunFeedback) error {
	if fb.SubmittedAt.IsZero() {
		fb.SubmittedAt = time.Now().UTC()
	}
	var diagCorrect *int
	if fb.DiagnosisCorrect != nil {
		v := 0
		if *fb.DiagnosisCorrect {
			v = 1
		}
		diagCorrect = &v
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO run_feedback (run_id, series_id, diagnosis_correct, actual_root_cause, operator, submitted_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id) DO UPDATE SET
    series_id         = excluded.series_id,
    diagnosis_correct = excluded.diagnosis_correct,
    actual_root_cause = excluded.actual_root_cause,
    operator          = excluded.operator,
    submitted_at      = excluded.submitted_at`,
		fb.RunID, fb.SeriesID, diagCorrect, fb.ActualRootCause, fb.Operator,
		fb.SubmittedAt.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return fmt.Errorf("submit run feedback: %w", err)
	}
	return nil
}

// GetByRunID returns the feedback for the given run, or sql.ErrNoRows if none.
func (s *RunFeedbackStore) GetByRunID(ctx context.Context, runID string) (*RunFeedback, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT run_id, series_id, diagnosis_correct, actual_root_cause, operator, submitted_at
FROM run_feedback WHERE run_id = ?`, runID)
	return scanRunFeedback(row)
}

// StatsBySeries returns accuracy aggregates for a playbook series.
func (s *RunFeedbackStore) StatsBySeries(ctx context.Context, seriesID string) (*FeedbackStats, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT
    COUNT(*)                                     AS feedback_count,
    SUM(CASE WHEN diagnosis_correct = 1 THEN 1 ELSE 0 END) AS correct_count
FROM run_feedback WHERE series_id = ?`, seriesID)
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

func scanRunFeedback(row *sql.Row) (*RunFeedback, error) {
	var (
		fb        RunFeedback
		diagInt   *int
		submittedStr string
	)
	err := row.Scan(&fb.RunID, &fb.SeriesID, &diagInt, &fb.ActualRootCause, &fb.Operator, &submittedStr)
	if err != nil {
		return nil, err
	}
	if diagInt != nil {
		b := *diagInt != 0
		fb.DiagnosisCorrect = &b
	}
	if submittedStr != "" {
		t, err := time.Parse(sqliteTimeFormat, submittedStr)
		if err == nil {
			fb.SubmittedAt = t
		}
	}
	return &fb, nil
}

