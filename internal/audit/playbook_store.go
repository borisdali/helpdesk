package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

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
		"ALTER TABLE playbooks ADD COLUMN problem_class  TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN symptoms       TEXT NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN guidance       TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN escalation     TEXT NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN related_playbooks TEXT NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN author         TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN last_validated TEXT",
		"ALTER TABLE playbooks ADD COLUMN version        TEXT NOT NULL DEFAULT ''",
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

// Create inserts a new playbook. PlaybookID is generated if empty.
func (s *PlaybookStore) Create(ctx context.Context, pb *Playbook) error {
	if pb.PlaybookID == "" {
		pb.PlaybookID = "pb_" + uuid.New().String()[:8]
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

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO playbooks (playbook_id, name, description, target_hints, created_by, created_at, updated_at,
		                        problem_class, symptoms, guidance, escalation, related_playbooks, author, last_validated, version)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pb.PlaybookID, pb.Name, pb.Description, string(hintsJSON), pb.CreatedBy, pb.CreatedAt, pb.UpdatedAt,
		pb.ProblemClass, string(symptomsJSON), pb.Guidance, string(escalationJSON), string(relatedJSON),
		pb.Author, lastValidatedStr, pb.Version,
	)
	return err
}

// Update replaces the mutable fields of an existing playbook.
// PlaybookID, CreatedBy, and CreatedAt are not modified.
func (s *PlaybookStore) Update(ctx context.Context, pb *Playbook) error {
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
		    related_playbooks=?, author=?, last_validated=?, version=?
		 WHERE playbook_id=?`,
		pb.Name, pb.Description, string(hintsJSON), pb.UpdatedAt,
		pb.ProblemClass, string(symptomsJSON), pb.Guidance, string(escalationJSON),
		string(relatedJSON), pb.Author, lastValidatedStr, pb.Version,
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

const playbookColumns = `playbook_id, name, description, target_hints, created_by, created_at, updated_at,
	problem_class, symptoms, guidance, escalation, related_playbooks, author, last_validated, version`

// Get returns a playbook by ID, or sql.ErrNoRows if not found.
func (s *PlaybookStore) Get(ctx context.Context, id string) (*Playbook, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+playbookColumns+` FROM playbooks WHERE playbook_id = ?`, id)
	return scanPlaybook(row)
}

// List returns all playbooks ordered by created_at descending.
func (s *PlaybookStore) List(ctx context.Context) ([]*Playbook, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+playbookColumns+` FROM playbooks ORDER BY created_at DESC`)
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

// Delete removes a playbook by ID. Returns nil if the playbook did not exist.
func (s *PlaybookStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM playbooks WHERE playbook_id = ?`, id)
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanPlaybook(s scanner) (*Playbook, error) {
	var pb Playbook
	var hintsJSON, symptomsJSON, escalationJSON, relatedJSON string
	var createdAt, updatedAt string
	var lastValidatedStr *string
	if err := s.Scan(
		&pb.PlaybookID, &pb.Name, &pb.Description, &hintsJSON,
		&pb.CreatedBy, &createdAt, &updatedAt,
		&pb.ProblemClass, &symptomsJSON, &pb.Guidance, &escalationJSON,
		&relatedJSON, &pb.Author, &lastValidatedStr, &pb.Version,
	); err != nil {
		return nil, err
	}

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
