package audit

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// GovbotRun represents one govbot compliance-run snapshot persisted to the
// audit store. All govbot instances (across teams/gateways) can write to the
// same database; the Gateway field distinguishes their origin.
type GovbotRun struct {
	ID                   int64     `json:"id,omitempty"`
	RunAt                time.Time `json:"run_at"`
	Window               string    `json:"window"`
	Gateway              string    `json:"gateway"`
	Status               string    `json:"status"` // healthy | warnings | alerts
	AlertCount           int       `json:"alert_count"`
	WarningCount         int       `json:"warning_count"`
	Alerts               []string  `json:"alerts"`
	Warnings             []string  `json:"warnings"`
	ChainValid           bool      `json:"chain_valid"`
	PolicyDenies         int       `json:"policy_denies"`
	PolicyNoMatch        int       `json:"policy_no_match"`
	MutationsTotal       int       `json:"mutations_total"`
	MutationsDestructive int       `json:"mutations_destructive"`
	PendingApprovals     int       `json:"pending_approvals"`
	StaleApprovals         int    `json:"stale_approvals"`
	DecisionsByResource    string `json:"decisions_by_resource,omitempty"`
	InvocationsByResource  string `json:"invocations_by_resource,omitempty"`
}

// GovbotStore persists GovbotRun snapshots. It shares the same *sql.DB
// connection as the audit Store and ApprovalStore.
type GovbotStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewGovbotStore creates the govbot_runs table (if absent) and returns a
// ready-to-use GovbotStore using the given shared database connection.
func NewGovbotStore(db *sql.DB, isPostgres bool) (*GovbotStore, error) {
	s := &GovbotStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create govbot schema: %w", err)
	}
	return s, nil
}

func (s *GovbotStore) createSchema() error {
	pk := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if s.isPostgres {
		pk = "BIGSERIAL PRIMARY KEY"
	}
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS govbot_runs (
    id                    %s,
    run_at                TEXT    NOT NULL,
    window                TEXT    NOT NULL,
    gateway               TEXT    NOT NULL,
    status                TEXT    NOT NULL,
    alert_count           INTEGER NOT NULL DEFAULT 0,
    warning_count         INTEGER NOT NULL DEFAULT 0,
    alerts_json           TEXT,
    warnings_json         TEXT,
    chain_valid           INTEGER NOT NULL DEFAULT 1,
    policy_denies         INTEGER NOT NULL DEFAULT 0,
    policy_no_match       INTEGER NOT NULL DEFAULT 0,
    mutations_total       INTEGER NOT NULL DEFAULT 0,
    mutations_destructive INTEGER NOT NULL DEFAULT 0,
    pending_approvals     INTEGER NOT NULL DEFAULT 0,
    stale_approvals       INTEGER NOT NULL DEFAULT 0,
    decisions_by_resource   TEXT,
    invocations_by_resource TEXT
)`, pk),
		`CREATE INDEX IF NOT EXISTS idx_govbot_runs_run_at ON govbot_runs(run_at)`,
		`CREATE INDEX IF NOT EXISTS idx_govbot_runs_window  ON govbot_runs(window)`,
		`CREATE INDEX IF NOT EXISTS idx_govbot_runs_gateway ON govbot_runs(gateway)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	// Migration: add invocations_by_resource to databases created before this
	// column existed. SQLite returns an error on duplicate column; ignore it.
	if s.isPostgres {
		s.db.Exec(`ALTER TABLE govbot_runs ADD COLUMN IF NOT EXISTS invocations_by_resource TEXT`) //nolint:errcheck
	} else {
		s.db.Exec(`ALTER TABLE govbot_runs ADD COLUMN invocations_by_resource TEXT`) //nolint:errcheck
	}
	return nil
}

// SaveRun inserts one govbot run snapshot.
func (s *GovbotStore) SaveRun(run GovbotRun) error {
	alertsJSON, _ := json.Marshal(run.Alerts)
	warningsJSON, _ := json.Marshal(run.Warnings)
	chainInt := 0
	if run.ChainValid {
		chainInt = 1
	}
	q := rebind(s.isPostgres, `INSERT INTO govbot_runs
		(run_at, window, gateway, status,
		 alert_count, warning_count, alerts_json, warnings_json,
		 chain_valid, policy_denies, policy_no_match,
		 mutations_total, mutations_destructive,
		 pending_approvals, stale_approvals,
		 decisions_by_resource, invocations_by_resource)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := s.db.Exec(q,
		run.RunAt.UTC().Format(time.RFC3339),
		run.Window, run.Gateway, run.Status,
		run.AlertCount, run.WarningCount,
		string(alertsJSON), string(warningsJSON),
		chainInt,
		run.PolicyDenies, run.PolicyNoMatch,
		run.MutationsTotal, run.MutationsDestructive,
		run.PendingApprovals, run.StaleApprovals,
		run.DecisionsByResource, run.InvocationsByResource,
	)
	return err
}

// RecentRuns returns the last limit runs, newest first. Pass window="" to
// return runs across all windows. Pass gateway="" to return all gateways.
func (s *GovbotStore) RecentRuns(window, gateway string, limit int) ([]GovbotRun, error) {
	cols := `id, run_at, window, gateway, status,
		alert_count, warning_count, alerts_json, warnings_json,
		chain_valid, policy_denies, policy_no_match,
		mutations_total, mutations_destructive,
		pending_approvals, stale_approvals,
		decisions_by_resource, invocations_by_resource`

	base := "SELECT " + cols + " FROM govbot_runs"
	var where []string
	var args []any
	if window != "" {
		where = append(where, "window = ?")
		args = append(args, window)
	}
	if gateway != "" {
		where = append(where, "gateway = ?")
		args = append(args, gateway)
	}
	q := base
	if len(where) > 0 {
		q += " WHERE " + where[0]
		for _, w := range where[1:] {
			q += " AND " + w
		}
	}
	q += " ORDER BY run_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(rebind(s.isPostgres, q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []GovbotRun
	for rows.Next() {
		var r GovbotRun
		var runAtStr, alertsJSON, warningsJSON string
		var chainInt int
		if err := rows.Scan(
			&r.ID, &runAtStr, &r.Window, &r.Gateway, &r.Status,
			&r.AlertCount, &r.WarningCount, &alertsJSON, &warningsJSON,
			&chainInt, &r.PolicyDenies, &r.PolicyNoMatch,
			&r.MutationsTotal, &r.MutationsDestructive,
			&r.PendingApprovals, &r.StaleApprovals,
			&r.DecisionsByResource, &r.InvocationsByResource,
		); err != nil {
			return nil, err
		}
		r.RunAt, _ = time.Parse(time.RFC3339, runAtStr)
		r.ChainValid = chainInt != 0
		json.Unmarshal([]byte(alertsJSON), &r.Alerts)   //nolint:errcheck
		json.Unmarshal([]byte(warningsJSON), &r.Warnings) //nolint:errcheck
		runs = append(runs, r)
	}
	return runs, rows.Err()
}
