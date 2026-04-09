package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newUploadTestStore(t *testing.T) *UploadStore {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	us, err := NewUploadStore(store.DB())
	if err != nil {
		t.Fatalf("NewUploadStore: %v", err)
	}
	return us
}

func TestUploadStore_StoreAndGet(t *testing.T) {
	us := newUploadTestStore(t)
	ctx := context.Background()

	content := []byte("2024-01-15 10:00:00 UTC: LOG: database system is ready\n")
	u, err := us.Store(ctx, "postgresql-2024.log", content)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	if !hasPrefix(u.UploadID, "ul_") {
		t.Errorf("upload_id = %q, want ul_ prefix", u.UploadID)
	}
	if u.Filename != "postgresql-2024.log" {
		t.Errorf("filename = %q, want postgresql-2024.log", u.Filename)
	}
	if u.Size != int64(len(content)) {
		t.Errorf("size = %d, want %d", u.Size, len(content))
	}
	if u.ExpiresAt.Before(time.Now()) {
		t.Error("expires_at should be in the future")
	}

	got, err := us.Get(ctx, u.UploadID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil for a just-stored upload")
	}
	if got.UploadID != u.UploadID {
		t.Errorf("upload_id = %q, want %q", got.UploadID, u.UploadID)
	}
}

func TestUploadStore_GetContent(t *testing.T) {
	us := newUploadTestStore(t)
	ctx := context.Background()

	content := []byte("FATAL: invalid value for parameter \"max_connections\": \"unlimited\"\n")
	u, err := us.Store(ctx, "pg.log", content)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	gotContent, gotFilename, err := us.GetContent(ctx, u.UploadID)
	if err != nil {
		t.Fatalf("GetContent: %v", err)
	}
	if string(gotContent) != string(content) {
		t.Errorf("content = %q, want %q", gotContent, content)
	}
	if gotFilename != "pg.log" {
		t.Errorf("filename = %q, want pg.log", gotFilename)
	}
}

func TestUploadStore_NotFound(t *testing.T) {
	us := newUploadTestStore(t)
	ctx := context.Background()

	got, err := us.Get(ctx, "ul_doesnotexist")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("Get non-existent: expected nil, got %+v", got)
	}

	content, _, err := us.GetContent(ctx, "ul_doesnotexist")
	if err != nil {
		t.Fatalf("GetContent: unexpected error: %v", err)
	}
	if content != nil {
		t.Error("GetContent non-existent: expected nil content")
	}
}

func TestUploadStore_Expired(t *testing.T) {
	us := newUploadTestStore(t)
	ctx := context.Background()

	// Store a valid upload then manually expire it in the DB.
	u, err := us.Store(ctx, "old.log", []byte("old content"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	past := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05Z07:00")
	if _, err := us.db.ExecContext(ctx,
		`UPDATE uploads SET expires_at = ? WHERE upload_id = ?`, past, u.UploadID,
	); err != nil {
		t.Fatalf("update expires_at: %v", err)
	}

	got, err := us.Get(ctx, u.UploadID)
	if err != nil {
		t.Fatalf("Get after expiry: %v", err)
	}
	if got != nil {
		t.Error("Get expired upload: expected nil (treated as not found)")
	}

	content, _, err := us.GetContent(ctx, u.UploadID)
	if err != nil {
		t.Fatalf("GetContent after expiry: %v", err)
	}
	if content != nil {
		t.Error("GetContent expired upload: expected nil content")
	}
}

func TestUploadStore_IDPrefix(t *testing.T) {
	us := newUploadTestStore(t)
	ctx := context.Background()

	u, err := us.Store(ctx, "test.log", []byte("x"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !hasPrefix(u.UploadID, "ul_") {
		t.Errorf("upload_id %q should have ul_ prefix", u.UploadID)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
