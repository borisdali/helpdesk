package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrSystemPlaybook is returned when a mutating operation is attempted on a
// system-managed playbook (IsSystem=true).
var ErrSystemPlaybook = errors.New("system playbooks are read-only")

// Playbook is a saved NL intent: a named description + optional target hints
// that can be run on demand to generate a fresh fleet job plan.
type Playbook struct {
	// Core fields (always present)
	PlaybookID  string    `json:"playbook_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"` // fleet intent — passed verbatim to planner
	TargetHints []string  `json:"target_hints,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Knowledge fields (optional, added in v0.8)
	ProblemClass     string     `json:"problem_class,omitempty"`     // performance|availability|capacity|data_integrity|security
	Symptoms         []string   `json:"symptoms,omitempty"`          // observable indicators that trigger this playbook
	Guidance         string     `json:"guidance,omitempty"`          // expert reasoning injected into planner prompt
	Escalation       []string   `json:"escalation,omitempty"`        // conditions under which the LLM must stop and escalate
	RelatedPlaybooks []string   `json:"related_playbooks,omitempty"` // pb_* IDs of related playbooks
	Author           string     `json:"author,omitempty"`
	LastValidated    *time.Time `json:"last_validated,omitempty"`
	Version          string     `json:"version,omitempty"`

	// Versioning fields (added in Phase 2)
	SeriesID string `json:"series_id,omitempty"` // "pbs_" + uuid[:8]; groups all versions of a playbook concept; stable across renames
	IsActive bool   `json:"is_active"`            // exactly one version per series should be active
	IsSystem bool   `json:"is_system"`            // true = shipped with aiHelpDesk; read-only via API
	Source   string `json:"source"`               // "system" | "imported" | "manual"
}

// PlaybookStore persists fleet playbooks.
// It shares the same *sql.DB connection as the other audit stores.
type PlaybookStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewPlaybookStore creates the playbooks table (if absent), runs any pending
// migrations, and returns a ready-to-use PlaybookStore.
func NewPlaybookStore(db *sql.DB, isPostgres bool) (*PlaybookStore, error) {
	s := &PlaybookStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create playbook schema: %w", err)
	}
	if err := s.migrateSchema(); err != nil {
		return nil, fmt.Errorf("migrate playbook schema: %w", err)
	}
	return s, nil
}

func (s *PlaybookStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS playbooks (
    playbook_id  TEXT        NOT NULL PRIMARY KEY,
    name         TEXT        NOT NULL,
    description  TEXT        NOT NULL,
    target_hints TEXT        NOT NULL DEFAULT '[]',
    created_by   TEXT        NOT NULL DEFAULT '',
    created_at   DATETIME    NOT NULL,
    updated_at   DATETIME    NOT NULL
)`)
	return err
}

// migrateSchema adds columns introduced after the initial schema.
// Each ALTER is idempotent: duplicate-column errors are silently ignored.
func (s *PlaybookStore) migrateSchema() error {
	newCols := []string{
		"ALTER TABLE playbooks ADD COLUMN problem_class  TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN symptoms       TEXT    NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN guidance       TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN escalation     TEXT    NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN related_playbooks TEXT  NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN author         TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN last_validated TEXT",
		"ALTER TABLE playbooks ADD COLUMN version        TEXT    NOT NULL DEFAULT ''",
		// Phase 2: versioning
		"ALTER TABLE playbooks ADD COLUMN series_id      TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN is_active      INTEGER NOT NULL DEFAULT 1",
		"ALTER TABLE playbooks ADD COLUMN is_system      INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE playbooks ADD COLUMN source         TEXT    NOT NULL DEFAULT 'manual'",
	}
	for _, stmt := range newCols {
		if _, err := s.db.Exec(stmt); err != nil {
			// Ignore "duplicate column" errors from SQLite and Postgres
			msg := err.Error()
			if containsAny(msg, "duplicate column", "already exists") {
				continue
			}
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

func containsAny(s string, subs ...string) bool {
	lower := s
	for _, sub := range subs {
		if len(sub) <= len(lower) {
			for i := 0; i <= len(lower)-len(sub); i++ {
				if lower[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// Create inserts a new playbook. PlaybookID and SeriesID are generated if empty.
// IsActive defaults to true; Source defaults to "manual".
func (s *PlaybookStore) Create(ctx context.Context, pb *Playbook) error {
	if pb.PlaybookID == "" {
		pb.PlaybookID = "pb_" + uuid.New().String()[:8]
	}
	seriesWasEmpty := pb.SeriesID == ""
	if seriesWasEmpty {
		pb.SeriesID = "pbs_" + uuid.New().String()[:8]
	}
	if pb.Source == "" {
		pb.Source = "manual"
	}
	// For brand-new series (caller didn't provide SeriesID), default IsActive=true.
	// For callers that set an explicit SeriesID (e.g., the seeder inserting a later version),
	// respect whatever IsActive value they provided.
	if seriesWasEmpty {
		pb.IsActive = true
	}

	now := time.Now().UTC()
	pb.CreatedAt = now
	pb.UpdatedAt = now

	hintsJSON, err := json.Marshal(pb.TargetHints)
	if err != nil {
		return fmt.Errorf("marshal target_hints: %w", err)
	}
	symptomsJSON, err := json.Marshal(pb.Symptoms)
	if err != nil {
		return fmt.Errorf("marshal symptoms: %w", err)
	}
	escalationJSON, err := json.Marshal(pb.Escalation)
	if err != nil {
		return fmt.Errorf("marshal escalation: %w", err)
	}
	relatedJSON, err := json.Marshal(pb.RelatedPlaybooks)
	if err != nil {
		return fmt.Errorf("marshal related_playbooks: %w", err)
	}

	var lastValidatedStr *string
	if pb.LastValidated != nil {
		s := pb.LastValidated.UTC().Format(time.RFC3339Nano)
		lastValidatedStr = &s
	}

	isActiveInt := 0
	if pb.IsActive {
		isActiveInt = 1
	}
	isSystemInt := 0
	if pb.IsSystem {
		isSystemInt = 1
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO playbooks
		    (playbook_id, name, description, target_hints, created_by, created_at, updated_at,
		     problem_class, symptoms, guidance, escalation, related_playbooks, author, last_validated, version,
		     series_id, is_active, is_system, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pb.PlaybookID, pb.Name, pb.Description, string(hintsJSON), pb.CreatedBy, pb.CreatedAt, pb.UpdatedAt,
		pb.ProblemClass, string(symptomsJSON), pb.Guidance, string(escalationJSON), string(relatedJSON),
		pb.Author, lastValidatedStr, pb.Version,
		pb.SeriesID, isActiveInt, isSystemInt, pb.Source,
	)
	return err
}

// Update replaces the mutable fields of an existing playbook.
// PlaybookID, CreatedBy, CreatedAt, IsActive, IsSystem, and Source are not modified.
// Returns ErrSystemPlaybook if the playbook is system-managed.
func (s *PlaybookStore) Update(ctx context.Context, pb *Playbook) error {
	// Fetch-first to check is_system before mutating.
	var isSystem int
	err := s.db.QueryRowContext(ctx,
		`SELECT is_system FROM playbooks WHERE playbook_id = ?`, pb.PlaybookID).Scan(&isSystem)
	if err == sql.ErrNoRows {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	if isSystem != 0 {
		return ErrSystemPlaybook
	}

	pb.UpdatedAt = time.Now().UTC()

	hintsJSON, err := json.Marshal(pb.TargetHints)
	if err != nil {
		return fmt.Errorf("marshal target_hints: %w", err)
	}
	symptomsJSON, err := json.Marshal(pb.Symptoms)
	if err != nil {
		return fmt.Errorf("marshal symptoms: %w", err)
	}
	escalationJSON, err := json.Marshal(pb.Escalation)
	if err != nil {
		return fmt.Errorf("marshal escalation: %w", err)
	}
	relatedJSON, err := json.Marshal(pb.RelatedPlaybooks)
	if err != nil {
		return fmt.Errorf("marshal related_playbooks: %w", err)
	}

	var lastValidatedStr *string
	if pb.LastValidated != nil {
		s := pb.LastValidated.UTC().Format(time.RFC3339Nano)
		lastValidatedStr = &s
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE playbooks SET
		    name=?, description=?, target_hints=?, updated_at=?,
		    problem_class=?, symptoms=?, guidance=?, escalation=?,
		    related_playbooks=?, author=?, last_validated=?, version=?,
		    series_id=?
		 WHERE playbook_id=?`,
		pb.Name, pb.Description, string(hintsJSON), pb.UpdatedAt,
		pb.ProblemClass, string(symptomsJSON), pb.Guidance, string(escalationJSON),
		string(relatedJSON), pb.Author, lastValidatedStr, pb.Version,
		pb.SeriesID,
		pb.PlaybookID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Delete removes a playbook by ID. Returns ErrSystemPlaybook if the playbook is
// system-managed. Returns nil (not an error) if the playbook did not exist.
func (s *PlaybookStore) Delete(ctx context.Context, id string) error {
	var isSystem int
	err := s.db.QueryRowContext(ctx,
		`SELECT is_system FROM playbooks WHERE playbook_id = ?`, id).Scan(&isSystem)
	if err == sql.ErrNoRows {
		return nil // already gone
	}
	if err != nil {
		return err
	}
	if isSystem != 0 {
		return ErrSystemPlaybook
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM playbooks WHERE playbook_id = ?`, id)
	return err
}

// Activate atomically promotes a playbook version: deactivates all other versions
// in the same series and marks the target active. Idempotent.
// Returns sql.ErrNoRows if the playbook does not exist.
// Returns ErrSystemPlaybook if the playbook is system-managed.
func (s *PlaybookStore) Activate(ctx context.Context, playbookID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var seriesID string
	var isSystem int
	err = tx.QueryRowContext(ctx,
		`SELECT series_id, is_system FROM playbooks WHERE playbook_id = ?`, playbookID).
		Scan(&seriesID, &isSystem)
	if err == sql.ErrNoRows {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	if isSystem != 0 {
		return ErrSystemPlaybook
	}

	// Deactivate all other versions in this series.
	if _, err = tx.ExecContext(ctx,
		`UPDATE playbooks SET is_active=0 WHERE series_id=? AND playbook_id != ?`,
		seriesID, playbookID); err != nil {
		return err
	}
	// Activate the target.
	if _, err = tx.ExecContext(ctx,
		`UPDATE playbooks SET is_active=1 WHERE playbook_id=?`, playbookID); err != nil {
		return err
	}

	return tx.Commit()
}

// PlaybookListQuery holds filter parameters for List.
type PlaybookListQuery struct {
	ActiveOnly    bool   // if true, return only is_active=1 rows
	IncludeSystem bool   // if true, include is_system=1 rows
	SeriesID      string // if non-empty, filter to this series (useful to list all versions)
}

// DefaultPlaybookListQuery returns the standard query: active versions only,
// system playbooks included.
func DefaultPlaybookListQuery() PlaybookListQuery {
	return PlaybookListQuery{ActiveOnly: true, IncludeSystem: true}
}

const playbookColumns = `playbook_id, name, description, target_hints, created_by, created_at, updated_at,
	problem_class, symptoms, guidance, escalation, related_playbooks, author, last_validated, version,
	series_id, is_active, is_system, source`

// Get returns a playbook by ID, or sql.ErrNoRows if not found.
func (s *PlaybookStore) Get(ctx context.Context, id string) (*Playbook, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+playbookColumns+` FROM playbooks WHERE playbook_id = ?`, id)
	return scanPlaybook(row)
}

// List returns playbooks matching the query, ordered by created_at descending.
func (s *PlaybookStore) List(ctx context.Context, q PlaybookListQuery) ([]*Playbook, error) {
	var where []string
	var args []any

	if q.ActiveOnly {
		where = append(where, "is_active = 1")
	}
	if !q.IncludeSystem {
		where = append(where, "is_system = 0")
	}
	if q.SeriesID != "" {
		where = append(where, "series_id = ?")
		args = append(args, q.SeriesID)
	}

	query := `SELECT ` + playbookColumns + ` FROM playbooks`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*Playbook
	for rows.Next() {
		pb, err := scanPlaybook(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, pb)
	}
	return result, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanPlaybook(s scanner) (*Playbook, error) {
	var pb Playbook
	var hintsJSON, symptomsJSON, escalationJSON, relatedJSON string
	var createdAt, updatedAt string
	var lastValidatedStr *string
	var isActive, isSystem int // SQLite stores bools as INTEGER; scan into int then convert

	if err := s.Scan(
		&pb.PlaybookID, &pb.Name, &pb.Description, &hintsJSON,
		&pb.CreatedBy, &createdAt, &updatedAt,
		&pb.ProblemClass, &symptomsJSON, &pb.Guidance, &escalationJSON,
		&relatedJSON, &pb.Author, &lastValidatedStr, &pb.Version,
		&pb.SeriesID, &isActive, &isSystem, &pb.Source,
	); err != nil {
		return nil, err
	}

	pb.IsActive = isActive != 0
	pb.IsSystem = isSystem != 0

	// JSON array fields
	if err := json.Unmarshal([]byte(hintsJSON), &pb.TargetHints); err != nil {
		pb.TargetHints = nil
	}
	if symptomsJSON != "" && symptomsJSON != "null" {
		if err := json.Unmarshal([]byte(symptomsJSON), &pb.Symptoms); err != nil {
			pb.Symptoms = nil
		}
	}
	if escalationJSON != "" && escalationJSON != "null" {
		if err := json.Unmarshal([]byte(escalationJSON), &pb.Escalation); err != nil {
			pb.Escalation = nil
		}
	}
	if relatedJSON != "" && relatedJSON != "null" {
		if err := json.Unmarshal([]byte(relatedJSON), &pb.RelatedPlaybooks); err != nil {
			pb.RelatedPlaybooks = nil
		}
	}

	// Timestamps
	pb.CreatedAt = parseFlexTime(createdAt)
	pb.UpdatedAt = parseFlexTime(updatedAt)
	if lastValidatedStr != nil && *lastValidatedStr != "" {
		t := parseFlexTime(*lastValidatedStr)
		if !t.IsZero() {
			pb.LastValidated = &t
		}
	}
	return &pb, nil
}

// parseFlexTime parses a time string in RFC3339Nano, RFC3339, or SQLite
// datetime format. Returns the zero time if parsing fails.
func parseFlexTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z07:00", s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
