package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	// UploadMaxBytes is the maximum accepted file size for operator uploads.
	UploadMaxBytes = 50 << 20 // 50 MB

	// UploadTTL is how long uploads are retained before expiry.
	UploadTTL = 24 * time.Hour
)

// Upload holds metadata about an operator-uploaded file.
// Content is not included in JSON responses — use GetContent separately.
type Upload struct {
	UploadID   string    `json:"upload_id"`
	Filename   string    `json:"filename"`
	Size       int64     `json:"size"`
	UploadedAt time.Time `json:"uploaded_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// UploadStore persists operator-uploaded files (e.g. PostgreSQL log files).
// It shares the same *sql.DB connection as the other audit stores.
type UploadStore struct {
	db *sql.DB
}

// NewUploadStore creates the uploads table if absent and returns a ready store.
func NewUploadStore(db *sql.DB) (*UploadStore, error) {
	s := &UploadStore{db: db}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create upload schema: %w", err)
	}
	return s, nil
}

func (s *UploadStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS uploads (
    upload_id   TEXT    PRIMARY KEY,
    filename    TEXT    NOT NULL,
    content     BLOB    NOT NULL,
    size        INTEGER NOT NULL,
    uploaded_at TEXT    NOT NULL,
    expires_at  TEXT    NOT NULL
)`)
	return err
}

// Store saves file content and returns the upload metadata.
func (s *UploadStore) Store(ctx context.Context, filename string, content []byte) (*Upload, error) {
	id := "ul_" + uuid.New().String()[:8]
	now := time.Now().UTC()
	exp := now.Add(UploadTTL)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO uploads (upload_id, filename, content, size, uploaded_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, filename, content, int64(len(content)),
		now.Format(time.RFC3339), exp.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("store upload: %w", err)
	}
	return &Upload{
		UploadID:   id,
		Filename:   filename,
		Size:       int64(len(content)),
		UploadedAt: now,
		ExpiresAt:  exp,
	}, nil
}

// Get returns metadata for an upload. Returns nil if not found or expired.
func (s *UploadStore) Get(ctx context.Context, uploadID string) (*Upload, error) {
	var u Upload
	var uploadedAt, expiresAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT upload_id, filename, size, uploaded_at, expires_at
		 FROM uploads WHERE upload_id = ?`, uploadID,
	).Scan(&u.UploadID, &u.Filename, &u.Size, &uploadedAt, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.UploadedAt, _ = time.Parse(time.RFC3339, uploadedAt)
	u.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
	if time.Now().After(u.ExpiresAt) {
		return nil, nil
	}
	return &u, nil
}

// GetContent returns the raw bytes and filename for an upload.
// Returns nil content if not found or expired.
func (s *UploadStore) GetContent(ctx context.Context, uploadID string) (content []byte, filename string, err error) {
	var expiresAt string
	err = s.db.QueryRowContext(ctx,
		`SELECT content, filename, expires_at FROM uploads WHERE upload_id = ?`, uploadID,
	).Scan(&content, &filename, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	exp, _ := time.Parse(time.RFC3339, expiresAt)
	if time.Now().After(exp) {
		return nil, "", nil
	}
	return content, filename, nil
}
