//go:build integration

package integration

import (
	"bytes"
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestUploadStore_Migrate_Postgres verifies upload_store's schema is
// idempotent on Postgres, and file content written on the first open is
// durable and readable after a fresh second open. Regression test for the
// BLOB-vs-BYTEA bug found live during v0.30 Postgres-backend validation.
func TestUploadStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	content := []byte("postgres migration smoke test content")
	var uploadID string

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewUploadStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewUploadStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			u, err := s.Store(ctx, "migrate-test.log", content)
			if err != nil {
				t.Fatalf("Store: %v", err)
			}
			uploadID = u.UploadID
			return
		}

		got, filename, err := s.GetContent(ctx, uploadID)
		if err != nil {
			t.Fatalf("GetContent: %v", err)
		}
		if filename != "migrate-test.log" {
			t.Errorf("filename = %q, want migrate-test.log", filename)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("content = %q, want %q", got, content)
		}
	})
}
