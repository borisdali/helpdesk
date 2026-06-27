package audit

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func newApprovalSessionStore(t *testing.T) *ApprovalSessionStore {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s, err := NewApprovalSessionStore(store.DB())
	if err != nil {
		t.Fatalf("NewApprovalSessionStore: %v", err)
	}
	return s
}

func TestApprovalSessionStore_Create_AssignsID(t *testing.T) {
	s := newApprovalSessionStore(t)
	ctx := context.Background()

	sess := &ApprovalSession{
		GrantedBy:      "boris",
		GrantedAt:      time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(30 * time.Minute),
		AllowedClasses: []ActionClass{ActionWrite, ActionDestructive},
		Scope:          "pbs_restart_triage",
	}
	if err := s.Create(ctx, sess); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.SessionID == "" {
		t.Error("SessionID should be assigned on Create")
	}
	if len(sess.SessionID) < 4 || sess.SessionID[:4] != "aps_" {
		t.Errorf("SessionID = %q, want aps_ prefix", sess.SessionID)
	}
}

func TestApprovalSessionStore_Get_RoundTrip(t *testing.T) {
	s := newApprovalSessionStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	sess := &ApprovalSession{
		GrantedBy:      "alice",
		GrantedAt:      now,
		ExpiresAt:      now.Add(time.Hour),
		AllowedClasses: []ActionClass{ActionWrite},
		Scope:          "pbs_db_triage",
	}
	if err := s.Create(ctx, sess); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SessionID != sess.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, sess.SessionID)
	}
	if got.GrantedBy != "alice" {
		t.Errorf("GrantedBy = %q, want alice", got.GrantedBy)
	}
	if len(got.AllowedClasses) != 1 || got.AllowedClasses[0] != ActionWrite {
		t.Errorf("AllowedClasses = %v, want [write]", got.AllowedClasses)
	}
	if got.Scope != "pbs_db_triage" {
		t.Errorf("Scope = %q, want pbs_db_triage", got.Scope)
	}
	if got.Revoked {
		t.Error("Revoked should be false for a fresh session")
	}
}

func TestApprovalSessionStore_Get_NotFound(t *testing.T) {
	s := newApprovalSessionStore(t)
	ctx := context.Background()

	_, err := s.Get(ctx, "aps_nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows for missing session, got %v", err)
	}
}

func TestApprovalSessionStore_Revoke(t *testing.T) {
	s := newApprovalSessionStore(t)
	ctx := context.Background()

	sess := &ApprovalSession{
		GrantedBy:      "bob",
		GrantedAt:      time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
		AllowedClasses: []ActionClass{ActionDestructive},
	}
	if err := s.Create(ctx, sess); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Revoke(ctx, sess.SessionID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	got, err := s.Get(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("Get after Revoke: %v", err)
	}
	if !got.Revoked {
		t.Error("Revoked should be true after Revoke")
	}
}

func TestApprovalSessionStore_Revoke_NotFound(t *testing.T) {
	s := newApprovalSessionStore(t)
	ctx := context.Background()

	err := s.Revoke(ctx, "aps_doesnotexist")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows when revoking nonexistent session, got %v", err)
	}
}

func TestApprovalSessionStore_MultipleAllowedClasses(t *testing.T) {
	s := newApprovalSessionStore(t)
	ctx := context.Background()

	sess := &ApprovalSession{
		GrantedBy:      "charlie",
		GrantedAt:      time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
		AllowedClasses: []ActionClass{ActionWrite, ActionDestructive},
	}
	if err := s.Create(ctx, sess); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.AllowedClasses) != 2 {
		t.Errorf("AllowedClasses len = %d, want 2", len(got.AllowedClasses))
	}
}
