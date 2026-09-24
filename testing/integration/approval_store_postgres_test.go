//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// TestApprovalStore_Migrate_Postgres verifies approval_store's schema and
// migrations are idempotent on Postgres, and a request written on the first
// open is durable and readable after a fresh second open.
func TestApprovalStore_Migrate_Postgres(t *testing.T) {
	dsn := postgresTestDSN(t)
	approvalID := "apr_pg_migrate_test"

	openPostgresStoreTwice(t, dsn, func(t *testing.T, store *audit.Store, pass int) {
		s, err := audit.NewApprovalStore(store.DB(), store.IsPostgres())
		if err != nil {
			t.Fatalf("NewApprovalStore (pass %d): %v", pass, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if pass == 1 {
			req := &audit.StoredApproval{
				ApprovalID:  approvalID,
				Status:      "pending",
				ActionClass: "write",
				RequestedBy: "migrate-test",
				RequestedAt: time.Now().UTC(),
			}
			if err := s.CreateRequest(ctx, req); err != nil {
				t.Fatalf("CreateRequest: %v", err)
			}
			return
		}

		got, err := s.GetRequest(ctx, approvalID)
		if err != nil {
			t.Fatalf("GetRequest: %v", err)
		}
		if got.Status != "pending" {
			t.Errorf("Status = %q, want pending", got.Status)
		}
	})
}
