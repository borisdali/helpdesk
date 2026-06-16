package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Playbook run outcome constants.
const (
	OutcomeResolved          = "resolved"
	OutcomeEscalated         = "escalated"
	OutcomeTransitioned      = "transitioned" // triage handed off to a remediation playbook in the same series
	OutcomeAbandoned         = "abandoned"
	OutcomeUnknown           = "unknown"
	OutcomeGatePending       = "gate_pending" // triage complete, waiting for operator gate acknowledgment
	OutcomeEscalatedResolved = "escalated+resolved"
)

// PlaybookRun records a single execution of a playbook.
type PlaybookRun struct {
	RunID            string             `json:"run_id"`
	PlaybookID       string             `json:"playbook_id"`
	SeriesID         string             `json:"series_id"`
	ExecutionMode    string             `json:"execution_mode"`            // "fleet" | "agent"
	Outcome          string             `json:"outcome"`                   // "resolved" | "escalated" | "abandoned" | "unknown"
	EscalatedTo      string             `json:"escalated_to,omitempty"`    // series_id for true out-of-scope escalations (ESCALATE_TO)
	TransitionedTo   string             `json:"transitioned_to,omitempty"` // series_id for same-domain triage→remediation transitions (TRANSITION_TO)
	FindingsSummary  string             `json:"findings_summary,omitempty"` // agent summary at handoff
	DiagnosticReport *DiagnosticReport  `json:"diagnostic_report,omitempty"` // structured hypotheses when agent emits HYPOTHESIS_N: lines
	ContextID        string             `json:"context_id,omitempty"`      // A2A session ID
	ConnectionString string             `json:"connection_string,omitempty"` // target DB/service; forwarded to chained runs
	TraceID          string             `json:"trace_id,omitempty"`        // X-Trace-ID of the originating request; links to audit events
	AgentTranscript  string             `json:"agent_transcript,omitempty"` // full agent response text — the chain-of-thought narrative
	PriorRunID       string             `json:"prior_run_id,omitempty"`    // triage run_id that preceded this remediation run
	Operator         string             `json:"operator"`
	StartedAt        time.Time          `json:"started_at"`
	CompletedAt      time.Time          `json:"completed_at,omitempty"`
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
	// Accuracy fields — populated when feedback is available (FeedbackCount > 0).
	// FeedbackCount is the total across at_gate and post_incident.
	FeedbackCount int     `json:"feedback_count"`
	CorrectCount  int     `json:"correct_count"`
	AccuracyRate  float64 `json:"accuracy_rate"` // correct_count / feedback_count; 0 when no feedback

	AtGateCount              int     `json:"at_gate_count"`
	AtGateCorrect            int     `json:"at_gate_correct"`
	AtGateAccuracyRate       float64 `json:"at_gate_accuracy_rate,omitempty"`
	PostIncidentCount        int     `json:"post_incident_count"`
	PostIncidentCorrect      int     `json:"post_incident_correct"`
	PostIncidentAccuracyRate float64 `json:"post_incident_accuracy_rate,omitempty"`

	// Remediation feedback fields — populated when remediation feedback exists.
	RemediationFeedbackCount       int     `json:"remediation_feedback_count"`
	RemediationCorrectCount        int     `json:"remediation_correct_count"`
	RemediationAccuracyRate        float64 `json:"remediation_accuracy_rate,omitempty"`
	RemediationAtGateCount         int     `json:"remediation_at_gate_count"`
	RemediationAtGateCorrect       int     `json:"remediation_at_gate_correct"`
	RemediationPostIncidentCount   int     `json:"remediation_post_incident_count"`
	RemediationPostIncidentCorrect int     `json:"remediation_post_incident_correct"`
}

// PlaybookVersionStats summarises run history broken down by playbook version.
// Each row represents one version of a series, ordered by version string.
type PlaybookVersionStats struct {
	SeriesID        string  `json:"series_id"`
	Version         string  `json:"version"`
	IsActive        bool    `json:"is_active"`         // currently active version for this series
	TotalRuns       int     `json:"total_runs"`
	Resolved        int     `json:"resolved"`
	ResolutionRate  float64 `json:"resolution_rate"`   // resolved / total_runs; 0 when no runs
	AvgStepCount    float64 `json:"avg_step_count"`    // average steps per run; 0 when no step data
	AvgRecoverySecs float64 `json:"avg_recovery_secs"` // average wall-clock seconds for completed runs; 0 when no data
	AvgDiagnosisScore   float64 `json:"avg_diagnosis_score"`   // average diagnosis_score; 0 when no eval data
	DiagEvalCount       int     `json:"diag_eval_count"`       // number of runs with diagnosis scores
	AvgRemediationScore float64 `json:"avg_remediation_score"` // average remediation_score; 0 when no remediation data
	RemedEvalCount      int     `json:"remed_eval_count"`      // number of runs with non-zero remediation scores
}

// PlaybookRunStore persists playbook execution records.
type PlaybookRunStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewPlaybookRunStore creates the playbook_runs table (if absent) and returns a
// ready-to-use PlaybookRunStore.
func NewPlaybookRunStore(db *sql.DB, isPostgres bool) (*PlaybookRunStore, error) {
	s := &PlaybookRunStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create playbook_run schema: %w", err)
	}
	return s, nil
}

func (s *PlaybookRunStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS playbook_runs (
    run_id             TEXT     NOT NULL PRIMARY KEY,
    playbook_id        TEXT     NOT NULL,
    series_id          TEXT     NOT NULL,
    execution_mode     TEXT     NOT NULL DEFAULT 'fleet',
    outcome            TEXT     NOT NULL DEFAULT 'unknown',
    escalated_to       TEXT     NOT NULL DEFAULT '',
    findings_summary   TEXT     NOT NULL DEFAULT '',
    diagnostic_report  TEXT     NOT NULL DEFAULT '',
    context_id         TEXT     NOT NULL DEFAULT '',
    operator           TEXT     NOT NULL DEFAULT '',
    started_at         DATETIME NOT NULL,
    completed_at       DATETIME NOT NULL DEFAULT ''
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
	if err != nil {
		return err
	}
	return s.migrate()
}


// migrate applies additive schema changes to existing databases.
// Each ALTER TABLE is swallowed if the column already exists (idempotent).
func (s *PlaybookRunStore) migrate() error {
	for _, col := range []struct {
		name string
		ddl  string
	}{
		{"diagnostic_report", `ALTER TABLE playbook_runs ADD COLUMN diagnostic_report TEXT NOT NULL DEFAULT ''`},
		{"transitioned_to", `ALTER TABLE playbook_runs ADD COLUMN transitioned_to TEXT NOT NULL DEFAULT ''`},
		{"connection_string", `ALTER TABLE playbook_runs ADD COLUMN connection_string TEXT NOT NULL DEFAULT ''`},
		{"trace_id", `ALTER TABLE playbook_runs ADD COLUMN trace_id TEXT NOT NULL DEFAULT ''`},
		{"agent_transcript", `ALTER TABLE playbook_runs ADD COLUMN agent_transcript TEXT NOT NULL DEFAULT ''`},
		{"prior_run_id", `ALTER TABLE playbook_runs ADD COLUMN prior_run_id TEXT NOT NULL DEFAULT ''`},
	} {
		if _, err := s.db.Exec(col.ddl); err != nil {
			// SQLite returns "duplicate column name" when the column already
			// exists; treat that as a no-op so restarts on new DBs also work.
			if !strings.Contains(err.Error(), "duplicate column name: "+col.name) {
				return fmt.Errorf("migrate playbook_runs.%s: %w", col.name, err)
			}
		}
	}
	_, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_playbook_runs_trace
    ON playbook_runs(trace_id)`)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("migrate idx_playbook_runs_trace: %w", err)
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_playbook_runs_prior
    ON playbook_runs(prior_run_id)`)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("migrate idx_playbook_runs_prior: %w", err)
	}
	return nil
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
	diagJSON := marshalDiagnosticReport(r.DiagnosticReport)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO playbook_runs
		    (run_id, playbook_id, series_id, execution_mode, outcome,
		     escalated_to, transitioned_to, findings_summary, diagnostic_report,
		     context_id, connection_string, trace_id, prior_run_id,
		     operator, started_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RunID, r.PlaybookID, r.SeriesID, r.ExecutionMode, outcome,
		r.EscalatedTo, r.TransitionedTo, r.FindingsSummary, diagJSON,
		r.ContextID, r.ConnectionString, r.TraceID, r.PriorRunID,
		r.Operator,
		r.StartedAt.Format("2006-01-02 15:04:05"),
		formatNullableTime(r.CompletedAt),
	)
	return err
}

// Update sets outcome, escalated_to, transitioned_to, findings_summary, diagnostic_report,
// agent_transcript, and completed_at for an existing run. Used when the agent session concludes.
// traceID, when non-empty, updates the run's trace_id (the agent's own X-Trace-ID from the
// response — distinct from the request trace ID stored at run start).
func (s *PlaybookRunStore) Update(ctx context.Context, runID, outcome, escalatedTo, transitionedTo, findingsSummary, agentTranscript, traceID string, report *DiagnosticReport) error {
	diagJSON := marshalDiagnosticReport(report)
	_, err := s.db.ExecContext(ctx,
		`UPDATE playbook_runs
		 SET outcome = ?, escalated_to = ?, transitioned_to = ?, findings_summary = ?,
		     diagnostic_report = ?, agent_transcript = ?, completed_at = ?,
		     trace_id = CASE WHEN ? != '' THEN ? ELSE trace_id END
		 WHERE run_id = ?`,
		outcome, escalatedTo, transitionedTo, findingsSummary, diagJSON, agentTranscript,
		time.Now().UTC().Format("2006-01-02 15:04:05"),
		traceID, traceID,
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
		       escalated_to, transitioned_to, findings_summary, diagnostic_report,
		       context_id, connection_string, trace_id, prior_run_id, agent_transcript,
		       operator, started_at, completed_at
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
		       escalated_to, transitioned_to, findings_summary, diagnostic_report,
		       context_id, connection_string, trace_id, prior_run_id, agent_transcript,
		       operator, started_at, completed_at
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

// ListByPriorRunID returns runs whose prior_run_id matches the given triage run_id,
// most recent first. Used to find the remediation run for a given triage run.
func (s *PlaybookRunStore) ListByPriorRunID(ctx context.Context, priorRunID string, limit int) ([]*PlaybookRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT run_id, playbook_id, series_id, execution_mode, outcome,
		       escalated_to, transitioned_to, findings_summary, diagnostic_report,
		       context_id, connection_string, trace_id, prior_run_id, agent_transcript,
		       operator, started_at, completed_at
		FROM playbook_runs
		WHERE prior_run_id = ?
		ORDER BY started_at DESC
		LIMIT %d`, limit), priorRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlaybookRuns(rows)
}

// ListByOutcome returns runs with the given outcome, most recent first.
func (s *PlaybookRunStore) ListBySeriesID(ctx context.Context, seriesID string, limit int) ([]*PlaybookRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT run_id, playbook_id, series_id, execution_mode, outcome,
		       escalated_to, transitioned_to, findings_summary, diagnostic_report,
		       context_id, connection_string, trace_id, prior_run_id, agent_transcript,
		       operator, started_at, completed_at
		FROM playbook_runs
		WHERE series_id = ?
		ORDER BY started_at DESC
		LIMIT %d`, limit), seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlaybookRuns(rows)
}

func (s *PlaybookRunStore) ListByOutcome(ctx context.Context, outcome string, limit int) ([]*PlaybookRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT run_id, playbook_id, series_id, execution_mode, outcome,
		       escalated_to, transitioned_to, findings_summary, diagnostic_report,
		       context_id, connection_string, trace_id, prior_run_id, agent_transcript,
		       operator, started_at, completed_at
		FROM playbook_runs
		WHERE outcome = ?
		ORDER BY started_at DESC
		LIMIT %d`, limit), outcome)
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
	var startedStr, completedStr, diagJSON string
	if err := s.Scan(
		&r.RunID, &r.PlaybookID, &r.SeriesID, &r.ExecutionMode, &r.Outcome,
		&r.EscalatedTo, &r.TransitionedTo, &r.FindingsSummary, &diagJSON,
		&r.ContextID, &r.ConnectionString, &r.TraceID, &r.PriorRunID, &r.AgentTranscript,
		&r.Operator, &startedStr, &completedStr,
	); err != nil {
		return nil, err
	}
	r.StartedAt = parseFlexTime(startedStr)
	r.CompletedAt = parseFlexTime(completedStr)
	r.DiagnosticReport = unmarshalDiagnosticReport(diagJSON)
	return &r, nil
}

// marshalDiagnosticReport serialises a DiagnosticReport to JSON for DB storage.
// Returns "" when report is nil (matches the column default).
func marshalDiagnosticReport(r *DiagnosticReport) string {
	if r == nil {
		return ""
	}
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalDiagnosticReport deserialises a JSON string from the DB column.
// Returns nil on empty input or parse error.
func unmarshalDiagnosticReport(s string) *DiagnosticReport {
	if s == "" {
		return nil
	}
	var r DiagnosticReport
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return nil
	}
	return &r
}

// formatNullableTime returns an empty string for the zero value, otherwise the
// formatted time — matches how SQLite stores empty optional datetimes.
func formatNullableTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

// StatsByVersion returns per-version run stats for a playbook series, ordered by version.
// Step counts come from playbook_run_steps; evaluation scores come from run_evaluation.
// Recovery time is computed in Go from started_at/completed_at strings.
func (s *PlaybookRunStore) StatsByVersion(ctx context.Context, seriesID string) ([]*PlaybookVersionStats, error) {
	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, `
		SELECT
		    p.version,
		    p.is_active,
		    r.outcome,
		    r.started_at,
		    r.completed_at,
		    COALESCE(sc.cnt, 0) AS step_count,
		    ev.diagnosis_score,
		    ev.remediation_score
		FROM playbook_runs r
		JOIN playbooks p ON r.playbook_id = p.playbook_id
		LEFT JOIN (
		    SELECT run_id, COUNT(*) AS cnt FROM playbook_run_steps GROUP BY run_id
		) sc ON sc.run_id = r.run_id
		LEFT JOIN run_evaluation ev ON ev.run_id = r.run_id
		WHERE r.series_id = ?
		ORDER BY p.version, r.started_at
	`), seriesID)
	if err != nil {
		return nil, fmt.Errorf("query version stats: %w", err)
	}
	defer rows.Close()

	type versionAccum struct {
		isActive        bool
		totalRuns       int
		resolved        int
		stepSum         float64
		recoverySumSecs float64
		recoveryCount   int
		diagScoreSum    float64
		diagEvalCount   int
		remedScoreSum   float64
		remedEvalCount  int
	}

	acc := map[string]*versionAccum{}
	var orderedVersions []string

	for rows.Next() {
		var version string
		var isActiveInt int
		var outcome, startedStr, completedStr string
		var stepCount int
		var diagScoreNull, remedScoreNull sql.NullFloat64

		if err := rows.Scan(&version, &isActiveInt, &outcome, &startedStr, &completedStr, &stepCount, &diagScoreNull, &remedScoreNull); err != nil {
			return nil, err
		}

		a, ok := acc[version]
		if !ok {
			a = &versionAccum{isActive: isActiveInt != 0}
			acc[version] = a
			orderedVersions = append(orderedVersions, version)
		}
		a.totalRuns++
		if outcome == OutcomeResolved {
			a.resolved++
		}
		a.stepSum += float64(stepCount)

		if completedStr != "" && startedStr != "" {
			started := parseFlexTime(startedStr)
			completed := parseFlexTime(completedStr)
			if !started.IsZero() && !completed.IsZero() && completed.After(started) {
				a.recoverySumSecs += completed.Sub(started).Seconds()
				a.recoveryCount++
			}
		}

		if diagScoreNull.Valid {
			a.diagScoreSum += diagScoreNull.Float64
			a.diagEvalCount++
		}
		if remedScoreNull.Valid && remedScoreNull.Float64 > 0 {
			a.remedScoreSum += remedScoreNull.Float64
			a.remedEvalCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]*PlaybookVersionStats, 0, len(orderedVersions))
	for _, v := range orderedVersions {
		a := acc[v]
		st := &PlaybookVersionStats{
			SeriesID:  seriesID,
			Version:   v,
			IsActive:  a.isActive,
			TotalRuns: a.totalRuns,
			Resolved:  a.resolved,
			DiagEvalCount:  a.diagEvalCount,
			RemedEvalCount: a.remedEvalCount,
		}
		if a.totalRuns > 0 {
			st.ResolutionRate = float64(a.resolved) / float64(a.totalRuns)
			st.AvgStepCount = a.stepSum / float64(a.totalRuns)
		}
		if a.recoveryCount > 0 {
			st.AvgRecoverySecs = a.recoverySumSecs / float64(a.recoveryCount)
		}
		if a.diagEvalCount > 0 {
			st.AvgDiagnosisScore = a.diagScoreSum / float64(a.diagEvalCount)
		}
		if a.remedEvalCount > 0 {
			st.AvgRemediationScore = a.remedScoreSum / float64(a.remedEvalCount)
		}
		out = append(out, st)
	}
	return out, nil
}
