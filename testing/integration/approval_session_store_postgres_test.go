//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestApprovalSessionStore_Migrate_Postgres verifies approval_session_store's
// schema is created correctly on Postgres and a session written on the
// first open is durable and readable after a fresh second open. This store
// carries no isPostgres of its own — its correctness on Postgres depends
// entirely on the rebindDB wrapper (internal/audit/rebind_db.go).
func TestApprovalSessionStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	sessionID := "aps_pg_migrate_test"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewApprovalSessionStore(store.DB())
		if err != nil {
			t.Fatalf("NewApprovalSessionStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			sess := &audit.ApprovalSession{
				SessionID:      sessionID,
				GrantedBy:      "migrate-test",
				GrantedAt:      time.Now().UTC(),
				ExpiresAt:      time.Now().UTC().Add(time.Hour),
				AllowedClasses: []audit.ActionClass{audit.ActionWrite},
			}
			if err := s.Create(ctx, sess); err != nil {
				t.Fatalf("Create: %v", err)
			}
			return
		}

		got, err := s.Get(ctx, sessionID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.GrantedBy != "migrate-test" {
			t.Errorf("GrantedBy = %q, want migrate-test", got.GrantedBy)
		}
	})
}
