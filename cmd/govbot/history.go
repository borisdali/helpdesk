package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"helpdesk/internal/audit"
)

// runSnapshot captures the key metrics from one govbot run for persistence.
type runSnapshot struct {
	RunAt                time.Time
	Window               string
	Gateway              string
	Status               string // "healthy" | "warnings" | "alerts"
	AlertCount           int
	WarningCount         int
	AlertsJSON           string // JSON array of strings
	WarningsJSON         string // JSON array of strings
	ChainValid           bool
	PolicyDenies         int
	PolicyNoMatch        int
	MutationsTotal       int
	MutationsDestructive int
	PendingApprovals     int
	StaleApprovals       int
	DecisionsByResource    string // JSON object: resource → {allow,deny,...}
	InvocationsByResource  string // JSON object: "resource:action" → {invoked,checked}
}

// historyClient abstracts compliance-run persistence.
// Two implementations: localHistoryStore (SQLite/PG file) and
// remoteHistoryClient (HTTP POST to auditd).
type historyClient interface {
	save(snap runSnapshot, retain int) error
	recent(window string, n int) ([]runSnapshot, error)
	printTable(window string, n int) error
	close()
}

// ── Local store (SQLite / PostgreSQL) ─────────────────────────────────────────

// localHistoryStore persists govbot run snapshots to SQLite or PostgreSQL.
type localHistoryStore struct {
	db         *sql.DB
	isPostgres bool
}

// openHistory opens (or creates) the govbot history database at path.
// If path starts with "postgres://" or "postgresql://" PostgreSQL is used;
// otherwise the value is treated as a SQLite file path.
func openHistory(path string) (*localHistoryStore, error) {
	isPostgres := strings.HasPrefix(path, "postgres://") || strings.HasPrefix(path, "postgresql://")

	var (
		db  *sql.DB
		err error
	)

	if isPostgres {
		db, err = sql.Open("pgx", path)
		if err != nil {
			return nil, fmt.Errorf("open postgres history database: %w", err)
		}
	} else {
		// SQLite: ensure the parent directory exists.
		dir := filepath.Dir(path)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("create govbot directory: %w", err)
			}
		}
		db, err = sql.Open("sqlite", path)
		if err != nil {
			return nil, fmt.Errorf("open govbot history database: %w", err)
		}
		// WAL mode improves concurrent read performance (SQLite only).
		if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			db.Close()
			return nil, fmt.Errorf("enable WAL mode: %w", err)
		}
	}

	h := &localHistoryStore{db: db, isPostgres: isPostgres}
	if err := h.createSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("create govbot history schema: %w", err)
	}
	return h, nil
}

// rebind rewrites ? placeholders to $N for PostgreSQL (same pattern as
// internal/audit/store.go).
func (h *localHistoryStore) rebind(query string) string {
	if !h.isPostgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, c := range query {
		if c == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (h *localHistoryStore) createSchema() error {
	pk := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if h.isPostgres {
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
	}
	for _, stmt := range stmts {
		if _, err := h.db.Exec(stmt); err != nil {
			return err
		}
	}
	// Migration: add invocations_by_resource to databases created before this
	// column existed. SQLite returns an error on duplicate column; ignore it.
	if h.isPostgres {
		h.db.Exec(`ALTER TABLE govbot_runs ADD COLUMN IF NOT EXISTS invocations_by_resource TEXT`) //nolint:errcheck
	} else {
		h.db.Exec(`ALTER TABLE govbot_runs ADD COLUMN invocations_by_resource TEXT`) //nolint:errcheck
	}
	return nil
}

// save inserts snap into the history database then prunes the oldest rows so
// that at most retain rows are kept. Pass retain ≤ 0 to keep everything.
func (h *localHistoryStore) save(snap runSnapshot, retain int) error {
	q := h.rebind(`INSERT INTO govbot_runs
		(run_at, window, gateway, status,
		 alert_count, warning_count, alerts_json, warnings_json,
		 chain_valid, policy_denies, policy_no_match,
		 mutations_total, mutations_destructive,
		 pending_approvals, stale_approvals,
		 decisions_by_resource, invocations_by_resource)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)

	chainInt := 0
	if snap.ChainValid {
		chainInt = 1
	}
	if _, err := h.db.Exec(q,
		snap.RunAt.UTC().Format(time.RFC3339),
		snap.Window,
		snap.Gateway,
		snap.Status,
		snap.AlertCount,
		snap.WarningCount,
		snap.AlertsJSON,
		snap.WarningsJSON,
		chainInt,
		snap.PolicyDenies,
		snap.PolicyNoMatch,
		snap.MutationsTotal,
		snap.MutationsDestructive,
		snap.PendingApprovals,
		snap.StaleApprovals,
		snap.DecisionsByResource,
		snap.InvocationsByResource,
	); err != nil {
		return fmt.Errorf("insert govbot run: %w", err)
	}

	if retain > 0 {
		pruneQ := h.rebind(`DELETE FROM govbot_runs WHERE id NOT IN (
			SELECT id FROM govbot_runs ORDER BY run_at DESC LIMIT ?
		)`)
		if _, err := h.db.Exec(pruneQ, retain); err != nil {
			return fmt.Errorf("prune govbot runs: %w", err)
		}
	}
	return nil
}

// recent returns the last n snapshots for the given look-back window (e.g.
// "24h"), newest first. Pass window="" to return runs across all windows.
func (h *localHistoryStore) recent(window string, n int) ([]runSnapshot, error) {
	var (
		q    string
		args []any
	)
	cols := `run_at, window, gateway, status,
		alert_count, warning_count, alerts_json, warnings_json,
		chain_valid, policy_denies, policy_no_match,
		mutations_total, mutations_destructive,
		pending_approvals, stale_approvals,
		decisions_by_resource, invocations_by_resource`

	if window != "" {
		q = h.rebind(`SELECT ` + cols + `
			FROM govbot_runs WHERE window = ?
			ORDER BY run_at DESC LIMIT ?`)
		args = []any{window, n}
	} else {
		q = h.rebind(`SELECT ` + cols + `
			FROM govbot_runs
			ORDER BY run_at DESC LIMIT ?`)
		args = []any{n}
	}

	rows, err := h.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query govbot runs: %w", err)
	}
	defer rows.Close()

	var snaps []runSnapshot
	for rows.Next() {
		var s runSnapshot
		var runAtStr string
		var chainInt int
		if err := rows.Scan(
			&runAtStr, &s.Window, &s.Gateway, &s.Status,
			&s.AlertCount, &s.WarningCount, &s.AlertsJSON, &s.WarningsJSON,
			&chainInt, &s.PolicyDenies, &s.PolicyNoMatch,
			&s.MutationsTotal, &s.MutationsDestructive,
			&s.PendingApprovals, &s.StaleApprovals,
			&s.DecisionsByResource, &s.InvocationsByResource,
		); err != nil {
			return nil, fmt.Errorf("scan govbot run: %w", err)
		}
		s.RunAt, _ = time.Parse(time.RFC3339, runAtStr)
		s.ChainValid = chainInt != 0
		snaps = append(snaps, s)
	}
	return snaps, rows.Err()
}

func (h *localHistoryStore) printTable(window string, n int) error {
	snaps, err := h.recent(window, n)
	if err != nil {
		return err
	}
	return printSnapsTable(snaps)
}

func (h *localHistoryStore) close() {
	if h.db != nil {
		h.db.Close()
	}
}

// ── Remote client (HTTP to auditd) ────────────────────────────────────────────

// remoteHistoryClient POSTs govbot run snapshots to the auditd service and
// retrieves them via GET. The gateway URL is used as a filter so that each
// govbot instance only sees its own runs in the trend block.
type remoteHistoryClient struct {
	auditURL   string // trimmed, no trailing slash
	gatewayURL string
	client     *http.Client
}

// openRemoteHistory creates a remoteHistoryClient targeting auditURL. gatewayURL
// is the govbot's own gateway base URL and is included in query filters so that
// a central IT deployment can distinguish runs from different teams.
func openRemoteHistory(auditURL, gatewayURL string) *remoteHistoryClient {
	return &remoteHistoryClient{
		auditURL:   strings.TrimRight(auditURL, "/"),
		gatewayURL: gatewayURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// save POSTs one run snapshot to auditd. When retain > 0 it is forwarded as a
// ?retain=N query parameter so auditd can prune stale rows for this gateway.
func (r *remoteHistoryClient) save(snap runSnapshot, retain int) error {
	run := snapToRun(snap)
	body, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("marshal govbot run: %w", err)
	}
	saveURL := r.auditURL + "/v1/govbot/runs"
	if retain > 0 {
		saveURL += fmt.Sprintf("?retain=%d", retain)
	}
	resp, err := r.client.Post(saveURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("POST /v1/govbot/runs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST /v1/govbot/runs: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// recent retrieves the last n runs from auditd, filtered by window and this
// client's gateway URL.
func (r *remoteHistoryClient) recent(window string, n int) ([]runSnapshot, error) {
	u := fmt.Sprintf("%s/v1/govbot/runs?limit=%d", r.auditURL, n)
	if window != "" {
		u += "&window=" + url.QueryEscape(window)
	}
	if r.gatewayURL != "" {
		u += "&gateway=" + url.QueryEscape(r.gatewayURL)
	}
	resp, err := r.client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("GET /v1/govbot/runs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /v1/govbot/runs: HTTP %d: %s", resp.StatusCode, string(b))
	}
	var runs []audit.GovbotRun
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		return nil, fmt.Errorf("decode govbot runs: %w", err)
	}
	snaps := make([]runSnapshot, len(runs))
	for i, run := range runs {
		snaps[i] = runToSnap(run)
	}
	return snaps, nil
}

func (r *remoteHistoryClient) printTable(window string, n int) error {
	snaps, err := r.recent(window, n)
	if err != nil {
		return err
	}
	return printSnapsTable(snaps)
}

func (r *remoteHistoryClient) close() {}

// ── Shared table rendering ────────────────────────────────────────────────────

func printSnapsTable(snaps []runSnapshot) error {
	if len(snaps) == 0 {
		fmt.Println("No history records found.")
		return nil
	}

	hdr := fmt.Sprintf("%-20s  %-6s  %-8s  %6s  %5s  %5s  %7s  %8s",
		"Run at (UTC)", "Window", "Status", "Denies", "Muts", "Chain", "Alerts", "Warnings")
	sep := strings.Repeat("─", len(hdr))
	fmt.Println(hdr)
	fmt.Println(sep)
	for _, s := range snaps {
		chain := "✓"
		if !s.ChainValid {
			chain = "✗"
		}
		status := strings.ToUpper(s.Status)
		fmt.Printf("%-20s  %-6s  %-8s  %6d  %5d  %5s  %7d  %8d\n",
			s.RunAt.UTC().Format("2006-01-02 15:04"),
			s.Window,
			status,
			s.PolicyDenies,
			s.MutationsTotal,
			chain,
			s.AlertCount,
			s.WarningCount,
		)
	}
	fmt.Println(sep)
	fmt.Printf("%d record(s)\n", len(snaps))
	return nil
}

// ── Conversion helpers ────────────────────────────────────────────────────────

// snapToRun converts a local runSnapshot to an audit.GovbotRun for HTTP transport.
func snapToRun(s runSnapshot) audit.GovbotRun {
	var alerts []string
	json.Unmarshal([]byte(s.AlertsJSON), &alerts)   //nolint:errcheck
	var warnings []string
	json.Unmarshal([]byte(s.WarningsJSON), &warnings) //nolint:errcheck
	return audit.GovbotRun{
		RunAt:                s.RunAt,
		Window:               s.Window,
		Gateway:              s.Gateway,
		Status:               s.Status,
		AlertCount:           s.AlertCount,
		WarningCount:         s.WarningCount,
		Alerts:               alerts,
		Warnings:             warnings,
		ChainValid:           s.ChainValid,
		PolicyDenies:         s.PolicyDenies,
		PolicyNoMatch:        s.PolicyNoMatch,
		MutationsTotal:       s.MutationsTotal,
		MutationsDestructive: s.MutationsDestructive,
		PendingApprovals:     s.PendingApprovals,
		StaleApprovals:       s.StaleApprovals,
		DecisionsByResource:   s.DecisionsByResource,
		InvocationsByResource: s.InvocationsByResource,
	}
}

// runToSnap converts an audit.GovbotRun retrieved from auditd back to runSnapshot.
func runToSnap(r audit.GovbotRun) runSnapshot {
	return runSnapshot{
		RunAt:                r.RunAt,
		Window:               r.Window,
		Gateway:              r.Gateway,
		Status:               r.Status,
		AlertCount:           r.AlertCount,
		WarningCount:         r.WarningCount,
		AlertsJSON:           marshalJSON(r.Alerts),
		WarningsJSON:         marshalJSON(r.Warnings),
		ChainValid:           r.ChainValid,
		PolicyDenies:         r.PolicyDenies,
		PolicyNoMatch:        r.PolicyNoMatch,
		MutationsTotal:       r.MutationsTotal,
		MutationsDestructive: r.MutationsDestructive,
		PendingApprovals:     r.PendingApprovals,
		StaleApprovals:       r.StaleApprovals,
		DecisionsByResource:   r.DecisionsByResource,
		InvocationsByResource: r.InvocationsByResource,
	}
}

// ── Coverage helpers ──────────────────────────────────────────────────────────

// coveragePct parses an InvocationsByResource JSON blob and returns the overall
// coverage percentage (checked/invoked × 100) and whether the data was
// available. Returns (0, false) when the blob is empty or malformed.
func coveragePct(invByResourceJSON string) (float64, bool) {
	if invByResourceJSON == "" || invByResourceJSON == "{}" || invByResourceJSON == "null" {
		return 0, false
	}
	var entries map[string]struct {
		Invoked int `json:"invoked"`
		Checked int `json:"checked"`
	}
	if err := json.Unmarshal([]byte(invByResourceJSON), &entries); err != nil || len(entries) == 0 {
		return 0, false
	}
	totalInvoked, totalChecked := 0, 0
	for _, e := range entries {
		totalInvoked += e.Invoked
		totalChecked += e.Checked
	}
	if totalInvoked == 0 {
		return 0, false
	}
	return float64(totalChecked) * 100 / float64(totalInvoked), true
}

// ── JSON helpers ──────────────────────────────────────────────────────────────

func marshalJSON(v any) string {
	b, _ := json.Marshal(v)
	if b == nil {
		return "null"
	}
	return string(b)
}
