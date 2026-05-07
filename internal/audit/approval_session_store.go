package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ApprovalSessionStore persists ApprovalSession records in SQLite.
type ApprovalSessionStore struct {
	db *sql.DB
}

// NewApprovalSessionStore creates the approval_sessions table (if absent) and
// returns a ready-to-use store.
func NewApprovalSessionStore(db *sql.DB) (*ApprovalSessionStore, error) {
	s := &ApprovalSessionStore{db: db}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create approval_sessions schema: %w", err)
	}
	return s, nil
}

func (s *ApprovalSessionStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS approval_sessions (
    session_id      TEXT     NOT NULL PRIMARY KEY,
    granted_by      TEXT     NOT NULL DEFAULT '',
    granted_at      DATETIME NOT NULL,
    expires_at      DATETIME NOT NULL,
    allowed_classes TEXT     NOT NULL DEFAULT '[]',
    scope           TEXT     NOT NULL DEFAULT '',
    revoked         INTEGER  NOT NULL DEFAULT 0
)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_approval_sessions_expires
    ON approval_sessions(expires_at)`)
	return err
}

// Create stores a new ApprovalSession. SessionID is generated if empty.
func (s *ApprovalSessionStore) Create(ctx context.Context, sess *ApprovalSession) error {
	if sess.SessionID == "" {
		sess.SessionID = "aps_" + uuid.New().String()[:8]
	}
	if sess.GrantedAt.IsZero() {
		sess.GrantedAt = time.Now().UTC()
	}
	classesJSON, err := json.Marshal(sess.AllowedClasses)
	if err != nil {
		return fmt.Errorf("marshal allowed_classes: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO approval_sessions
		    (session_id, granted_by, granted_at, expires_at, allowed_classes, scope, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, 0)`,
		sess.SessionID,
		sess.GrantedBy,
		sess.GrantedAt.UTC().Format("2006-01-02 15:04:05"),
		sess.ExpiresAt.UTC().Format("2006-01-02 15:04:05"),
		string(classesJSON),
		sess.Scope,
	)
	return err
}

// Get returns an ApprovalSession by ID.
// Returns sql.ErrNoRows if not found.
func (s *ApprovalSessionStore) Get(ctx context.Context, sessionID string) (*ApprovalSession, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT session_id, granted_by, granted_at, expires_at, allowed_classes, scope, revoked
		FROM approval_sessions
		WHERE session_id = ?`, sessionID)
	return scanApprovalSession(row)
}

// Revoke marks a session as revoked. Idempotent.
func (s *ApprovalSessionStore) Revoke(ctx context.Context, sessionID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE approval_sessions SET revoked = 1 WHERE session_id = ?`, sessionID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

type approvalSessionScanner interface {
	Scan(dest ...any) error
}

func scanApprovalSession(row approvalSessionScanner) (*ApprovalSession, error) {
	var sess ApprovalSession
	var grantedStr, expiresStr, classesJSON string
	var revoked int
	if err := row.Scan(
		&sess.SessionID, &sess.GrantedBy, &grantedStr, &expiresStr,
		&classesJSON, &sess.Scope, &revoked,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("scan approval_session: %w", err)
	}
	sess.GrantedAt = parseFlexTime(grantedStr)
	sess.ExpiresAt = parseFlexTime(expiresStr)
	sess.Revoked = revoked != 0
	if classesJSON != "" && classesJSON != "[]" {
		var classes []ActionClass
		if err := json.Unmarshal([]byte(classesJSON), &classes); err == nil {
			sess.AllowedClasses = classes
		}
	}
	return &sess, nil
}

// parseFlexTime is defined in playbook_run_store.go; shared within package.
