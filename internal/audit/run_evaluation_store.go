package audit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RunEvaluation holds automated evaluation scores for a single faulttest run
// against a playbook run. The RunID matches the plr_* playbook run ID from
// the gateway, allowing evaluation scores to be joined with operator feedback
// for calibration.
type RunEvaluation struct {
	RunID            string    `json:"run_id"`
	FailureID        string    `json:"failure_id"`
	FailureName      string    `json:"failure_name"`
	KeywordScore     float64   `json:"keyword_score"`
	ToolScore        float64   `json:"tool_score"`
	DiagnosisScore   float64   `json:"diagnosis_score"`
	RemediationScore float64   `json:"remediation_score,omitempty"`
	OverallScore     float64   `json:"overall_score"`
	JudgeUsed        bool      `json:"judge_used,omitempty"`
	Passed           bool      `json:"passed"`
	CreatedAt        time.Time `json:"created_at"`

	// Remediation judge fields — set when faulttest runs with --remediation-judge.
	RemediationJudgeScore     float64 `json:"remediation_judge_score,omitempty"`
	RemediationJudgeReasoning string  `json:"remediation_judge_reasoning,omitempty"`
}

// RunEvaluationStore persists automated faulttest evaluation scores.
type RunEvaluationStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewRunEvaluationStore creates the run_evaluation table if needed and returns
// a ready-to-use RunEvaluationStore.
func NewRunEvaluationStore(db *sql.DB, isPostgres bool) (*RunEvaluationStore, error) {
	s := &RunEvaluationStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create run_evaluation schema: %w", err)
	}
	return s, nil
}

func (s *RunEvaluationStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS run_evaluation (
    run_id                      TEXT    NOT NULL PRIMARY KEY,
    failure_id                  TEXT    NOT NULL DEFAULT '',
    failure_name                TEXT    NOT NULL DEFAULT '',
    keyword_score               REAL    NOT NULL DEFAULT 0,
    tool_score                  REAL    NOT NULL DEFAULT 0,
    diagnosis_score             REAL    NOT NULL DEFAULT 0,
    remediation_score           REAL    NOT NULL DEFAULT 0,
    overall_score               REAL    NOT NULL DEFAULT 0,
    judge_used                  INTEGER NOT NULL DEFAULT 0,
    passed                      INTEGER NOT NULL DEFAULT 0,
    created_at                  TEXT    NOT NULL DEFAULT '',
    remediation_judge_score     REAL    NOT NULL DEFAULT 0,
    remediation_judge_reasoning TEXT    NOT NULL DEFAULT ''
)`)
	if err != nil {
		return err
	}
	// Add new columns to existing tables that predate this schema version.
	for _, col := range []struct {
		name string
		ddl  string
	}{
		{"remediation_judge_score", "ALTER TABLE run_evaluation ADD COLUMN remediation_judge_score REAL NOT NULL DEFAULT 0"},
		{"remediation_judge_reasoning", "ALTER TABLE run_evaluation ADD COLUMN remediation_judge_reasoning TEXT NOT NULL DEFAULT ''"},
	} {
		if !s.isPostgres {
			var cnt int
			s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('run_evaluation') WHERE name=?`, col.name).Scan(&cnt) //nolint:errcheck
			if cnt > 0 {
				continue
			}
		}
		if _, err := s.db.Exec(col.ddl); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add column %s: %w", col.name, err)
		}
	}
	return nil
}

// Upsert writes evaluation scores for a run, overwriting any previous entry.
func (s *RunEvaluationStore) Upsert(ctx context.Context, eval *RunEvaluation) error {
	if eval.CreatedAt.IsZero() {
		eval.CreatedAt = time.Now().UTC()
	}
	judgeInt := 0
	if eval.JudgeUsed {
		judgeInt = 1
	}
	passedInt := 0
	if eval.Passed {
		passedInt = 1
	}
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
INSERT INTO run_evaluation
    (run_id, failure_id, failure_name, keyword_score, tool_score, diagnosis_score,
     remediation_score, overall_score, judge_used, passed, created_at,
     remediation_judge_score, remediation_judge_reasoning)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id) DO UPDATE SET
    failure_id                  = excluded.failure_id,
    failure_name                = excluded.failure_name,
    keyword_score               = excluded.keyword_score,
    tool_score                  = excluded.tool_score,
    diagnosis_score             = excluded.diagnosis_score,
    remediation_score           = excluded.remediation_score,
    overall_score               = excluded.overall_score,
    judge_used                  = excluded.judge_used,
    passed                      = excluded.passed,
    created_at                  = excluded.created_at,
    remediation_judge_score     = excluded.remediation_judge_score,
    remediation_judge_reasoning = excluded.remediation_judge_reasoning`),
		eval.RunID, eval.FailureID, eval.FailureName,
		eval.KeywordScore, eval.ToolScore, eval.DiagnosisScore,
		eval.RemediationScore, eval.OverallScore,
		judgeInt, passedInt,
		eval.CreatedAt.UTC().Format(time.RFC3339Nano),
		eval.RemediationJudgeScore, eval.RemediationJudgeReasoning,
	)
	return err
}

// CalibrationBand summarises accuracy vs. model confidence for one diagnosis-score range.
type CalibrationBand struct {
	Band           string  `json:"band"`            // "90-100%", "70-89%", "<70%"
	Runs           int     `json:"runs"`            // runs with both eval score and operator feedback
	Correct        int     `json:"correct"`         // operator confirmed diagnosis was correct
	ActualAccuracy float64 `json:"actual_accuracy"` // Correct/Runs; 0 when Runs==0
	Calibration    string  `json:"calibration"`     // "OVERCONFIDENT"|"WELL_CALIBRATED"|"UNDERCONFIDENT"|"INSUFFICIENT_DATA"
}

// CalibrationReport aggregates confidence-band calibration across a series (or fleet-wide).
type CalibrationReport struct {
	SeriesID         string             `json:"series_id,omitempty"`
	Bands            []*CalibrationBand `json:"bands"`
	TotalRuns        int                `json:"total_runs"` // total runs counted across all bands
	RemediationBands []*CalibrationBand `json:"remediation_bands,omitempty"`
	RemediationRuns  int                `json:"remediation_runs"`
}

type bandDef struct {
	label    string
	min, max float64
	expected float64
}

var diagBands = []bandDef{
	{"90-100%", 0.90, 1.01, 0.95},
	{"70-89%", 0.70, 0.90, 0.80},
	{"<70%", 0.00, 0.70, 0.50},
}

func calibrationLabel(actual, expected float64, runs int) string {
	if runs < 3 {
		return "INSUFFICIENT_DATA"
	}
	diff := actual - expected
	if diff < -0.10 {
		return "OVERCONFIDENT"
	}
	if diff > 0.10 {
		return "UNDERCONFIDENT"
	}
	return "WELL_CALIBRATED"
}

// CalibrationBands joins run_evaluation diagnosis scores with operator triage
// feedback to compute per-band accuracy. At-gate feedback (captured before
// remediation) is preferred over post-incident feedback; a run with both
// contributes only once using the at-gate verdict.
// Pass seriesID="" for fleet-wide calibration.
func (s *RunEvaluationStore) CalibrationBands(ctx context.Context, seriesID string) (*CalibrationReport, error) {
	gateFilter := ""
	postFilter := ""
	args := []any{}
	if seriesID != "" {
		gateFilter = " AND fb_gate.series_id = ?"
		postFilter = " AND fb_post.series_id = ?"
		args = append(args, seriesID, seriesID)
	}

	q := fmt.Sprintf(`
SELECT ev.diagnosis_score,
       COALESCE(fb_gate.verdict_correct, fb_post.verdict_correct) AS verdict_correct
FROM run_evaluation ev
LEFT JOIN run_feedback fb_gate ON fb_gate.run_id = ev.run_id
  AND fb_gate.feedback_type = 'triage'
  AND fb_gate.feedback_time = 'at_gate'
  AND fb_gate.verdict_correct IS NOT NULL%s
LEFT JOIN run_feedback fb_post ON fb_post.run_id = ev.run_id
  AND fb_post.feedback_type = 'triage'
  AND fb_post.feedback_time = 'post_incident'
  AND fb_post.verdict_correct IS NOT NULL%s
WHERE fb_gate.run_id IS NOT NULL OR fb_post.run_id IS NOT NULL`, gateFilter, postFilter)

	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, q), args...)
	if err != nil {
		return nil, fmt.Errorf("calibration query: %w", err)
	}
	defer rows.Close()

	type accum struct{ runs, correct int }
	counts := make([]accum, len(diagBands))

	for rows.Next() {
		var diagScore float64
		var verdictInt int
		if err := rows.Scan(&diagScore, &verdictInt); err != nil {
			return nil, err
		}
		for i, b := range diagBands {
			if diagScore >= b.min && diagScore < b.max {
				counts[i].runs++
				if verdictInt == 1 {
					counts[i].correct++
				}
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	report := &CalibrationReport{SeriesID: seriesID}
	for i, b := range diagBands {
		c := counts[i]
		band := &CalibrationBand{Band: b.label, Runs: c.runs, Correct: c.correct}
		if c.runs > 0 {
			band.ActualAccuracy = float64(c.correct) / float64(c.runs)
		}
		band.Calibration = calibrationLabel(band.ActualAccuracy, b.expected, c.runs)
		report.Bands = append(report.Bands, band)
		report.TotalRuns += c.runs
	}

	// Second query: remediation_score vs remediation operator feedback.
	remGateFilter := ""
	remPostFilter := ""
	remArgs := []any{}
	if seriesID != "" {
		remGateFilter = " AND rfb_gate.series_id = ?"
		remPostFilter = " AND rfb_post.series_id = ?"
		remArgs = append(remArgs, seriesID, seriesID)
	}
	remQ := fmt.Sprintf(`
SELECT ev.remediation_score,
       COALESCE(rfb_gate.verdict_correct, rfb_post.verdict_correct) AS verdict_correct
FROM run_evaluation ev
LEFT JOIN run_feedback rfb_gate ON rfb_gate.run_id = ev.run_id
  AND rfb_gate.feedback_type = 'remediation'
  AND rfb_gate.feedback_time = 'at_gate'
  AND rfb_gate.verdict_correct IS NOT NULL%s
LEFT JOIN run_feedback rfb_post ON rfb_post.run_id = ev.run_id
  AND rfb_post.feedback_type = 'remediation'
  AND rfb_post.feedback_time = 'post_incident'
  AND rfb_post.verdict_correct IS NOT NULL%s
WHERE ev.remediation_score > 0
  AND (rfb_gate.run_id IS NOT NULL OR rfb_post.run_id IS NOT NULL)`, remGateFilter, remPostFilter)

	remRows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, remQ), remArgs...)
	if err != nil {
		return nil, fmt.Errorf("remediation calibration query: %w", err)
	}
	defer remRows.Close()

	remCounts := make([]accum, len(diagBands))
	for remRows.Next() {
		var remScore float64
		var verdictInt int
		if err := remRows.Scan(&remScore, &verdictInt); err != nil {
			return nil, err
		}
		for i, b := range diagBands {
			if remScore >= b.min && remScore < b.max {
				remCounts[i].runs++
				if verdictInt == 1 {
					remCounts[i].correct++
				}
				break
			}
		}
	}
	if err := remRows.Err(); err != nil {
		return nil, err
	}

	for i, b := range diagBands {
		c := remCounts[i]
		band := &CalibrationBand{Band: b.label, Runs: c.runs, Correct: c.correct}
		if c.runs > 0 {
			band.ActualAccuracy = float64(c.correct) / float64(c.runs)
		}
		band.Calibration = calibrationLabel(band.ActualAccuracy, b.expected, c.runs)
		report.RemediationBands = append(report.RemediationBands, band)
		report.RemediationRuns += c.runs
	}

	return report, nil
}

// GetByRunID retrieves evaluation scores for a specific playbook run.
// Returns sql.ErrNoRows when no evaluation has been recorded.
func (s *RunEvaluationStore) GetByRunID(ctx context.Context, runID string) (*RunEvaluation, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
SELECT run_id, failure_id, failure_name, keyword_score, tool_score, diagnosis_score,
       remediation_score, overall_score, judge_used, passed, created_at,
       remediation_judge_score, remediation_judge_reasoning
FROM run_evaluation WHERE run_id = ?`), runID)

	var (
		eval       RunEvaluation
		judgeInt   int
		passedInt  int
		createdStr string
	)
	if err := row.Scan(
		&eval.RunID, &eval.FailureID, &eval.FailureName,
		&eval.KeywordScore, &eval.ToolScore, &eval.DiagnosisScore,
		&eval.RemediationScore, &eval.OverallScore,
		&judgeInt, &passedInt, &createdStr,
		&eval.RemediationJudgeScore, &eval.RemediationJudgeReasoning,
	); err != nil {
		return nil, err
	}
	eval.JudgeUsed = judgeInt != 0
	eval.Passed = passedInt != 0
	if createdStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, createdStr); err == nil {
			eval.CreatedAt = t
		}
	}
	return &eval, nil
}
