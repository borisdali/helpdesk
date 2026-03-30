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
	PlaybookID  string    `json:"playbook_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	TargetHints []string  `json:"target_hints,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PlaybookStore persists fleet playbooks.
// It shares the same *sql.DB connection as the other audit stores.
type PlaybookStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewPlaybookStore creates the playbooks table (if absent) and returns a
// ready-to-use PlaybookStore.
func NewPlaybookStore(db *sql.DB, isPostgres bool) (*PlaybookStore, error) {
	s := &PlaybookStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create playbook schema: %w", err)
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

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO playbooks (playbook_id, name, description, target_hints, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pb.PlaybookID, pb.Name, pb.Description, string(hintsJSON), pb.CreatedBy, pb.CreatedAt, pb.UpdatedAt,
	)
	return err
}

// Get returns a playbook by ID, or sql.ErrNoRows if not found.
func (s *PlaybookStore) Get(ctx context.Context, id string) (*Playbook, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT playbook_id, name, description, target_hints, created_by, created_at, updated_at
		 FROM playbooks WHERE playbook_id = ?`, id)
	return scanPlaybook(row)
}

// List returns all playbooks ordered by created_at descending.
func (s *PlaybookStore) List(ctx context.Context) ([]*Playbook, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT playbook_id, name, description, target_hints, created_by, created_at, updated_at
		 FROM playbooks ORDER BY created_at DESC`)
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
	var hintsJSON string
	var createdAt, updatedAt string
	if err := s.Scan(&pb.PlaybookID, &pb.Name, &pb.Description, &hintsJSON,
		&pb.CreatedBy, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(hintsJSON), &pb.TargetHints); err != nil {
		pb.TargetHints = nil
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		pb.CreatedAt = t
	} else if t, err := time.Parse("2006-01-02T15:04:05Z07:00", createdAt); err == nil {
		pb.CreatedAt = t
	} else if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
		pb.CreatedAt = t.UTC()
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		pb.UpdatedAt = t
	} else if t, err := time.Parse("2006-01-02T15:04:05Z07:00", updatedAt); err == nil {
		pb.UpdatedAt = t
	} else if t, err := time.Parse("2006-01-02 15:04:05", updatedAt); err == nil {
		pb.UpdatedAt = t.UTC()
	}
	return &pb, nil
}
