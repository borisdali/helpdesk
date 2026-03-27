package main

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// openSQLiteDB opens an in-process SQLite database for testing.
// DetectRollbackCapability issues PostgreSQL-specific queries; all of them
// silently fail on SQLite (best-effort design) so only the mode-selection
// logic and the override path are exercised here.
func openSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// --- ReplicaIdentityFull ---

func TestReplicaIdentityFull_Nil(t *testing.T) {
	var cap *DBRollbackCapability
	if cap.ReplicaIdentityFull("public", "orders") {
		t.Error("nil receiver should return false")
	}
}

func TestReplicaIdentityFull_NilMap(t *testing.T) {
	cap := &DBRollbackCapability{} // ReplicaIdentity is nil
	if cap.ReplicaIdentityFull("public", "orders") {
		t.Error("nil map should return false")
	}
}

func TestReplicaIdentityFull_PublicSchema(t *testing.T) {
	cap := &DBRollbackCapability{
		ReplicaIdentity: map[string]string{
			"orders": "f", // FULL
		},
	}
	if !cap.ReplicaIdentityFull("public", "orders") {
		t.Error("expected true for FULL identity on public.orders")
	}
	if !cap.ReplicaIdentityFull("", "orders") {
		t.Error("empty schema should behave like public")
	}
}

func TestReplicaIdentityFull_NonPublicSchema(t *testing.T) {
	cap := &DBRollbackCapability{
		ReplicaIdentity: map[string]string{
			"billing.invoices": "f",
			"invoices":         "d", // default (PK-only) in public schema
		},
	}
	if !cap.ReplicaIdentityFull("billing", "invoices") {
		t.Error("expected true for billing.invoices with FULL identity")
	}
	if cap.ReplicaIdentityFull("public", "invoices") {
		t.Error("public.invoices has default identity, expected false")
	}
}

func TestReplicaIdentityFull_DefaultIdentity(t *testing.T) {
	cap := &DBRollbackCapability{
		ReplicaIdentity: map[string]string{
			"orders": "d", // default — PK only
		},
	}
	if cap.ReplicaIdentityFull("public", "orders") {
		t.Error("default identity should return false")
	}
}

// --- NewWALBracket ---

func TestNewWALBracket_SlotName_ShortTraceID(t *testing.T) {
	w := NewWALBracket(nil, "abc")
	if w.slotName != "helpdesk_rbk_abc" {
		t.Errorf("slotName = %q, want helpdesk_rbk_abc", w.slotName)
	}
}

func TestNewWALBracket_SlotName_TruncatesTo8(t *testing.T) {
	w := NewWALBracket(nil, "trace_id_very_long_suffix")
	if w.slotName != "helpdesk_rbk_trace_id" {
		t.Errorf("slotName = %q, want helpdesk_rbk_trace_id (first 8 chars)", w.slotName)
	}
}

func TestNewWALBracket_SlotName_ExactlyEight(t *testing.T) {
	w := NewWALBracket(nil, "12345678")
	if w.slotName != "helpdesk_rbk_12345678" {
		t.Errorf("slotName = %q, want helpdesk_rbk_12345678", w.slotName)
	}
}

// --- DetectRollbackCapability: mode-override ---

func TestDetectRollbackCapability_Override_WALDecode(t *testing.T) {
	db := openSQLiteDB(t)
	cap, err := DetectRollbackCapability(context.Background(), db, "public", "wal_decode")
	if err != nil {
		t.Fatalf("DetectRollbackCapability: %v", err)
	}
	if cap.Mode != RollbackModeWALDecode {
		t.Errorf("Mode = %q, want wal_decode", cap.Mode)
	}
}

func TestDetectRollbackCapability_Override_RowCapture(t *testing.T) {
	db := openSQLiteDB(t)
	cap, err := DetectRollbackCapability(context.Background(), db, "public", "row_capture")
	if err != nil {
		t.Fatalf("DetectRollbackCapability: %v", err)
	}
	if cap.Mode != RollbackModeRowCapture {
		t.Errorf("Mode = %q, want row_capture", cap.Mode)
	}
}

func TestDetectRollbackCapability_Override_None(t *testing.T) {
	db := openSQLiteDB(t)
	cap, err := DetectRollbackCapability(context.Background(), db, "public", "none")
	if err != nil {
		t.Fatalf("DetectRollbackCapability: %v", err)
	}
	if cap.Mode != RollbackModeNone {
		t.Errorf("Mode = %q, want none", cap.Mode)
	}
}

// TestDetectRollbackCapability_AutoDetect_FallsBackToRowCapture verifies that
// when wal_level and REPLICATION queries fail (as they do on SQLite), the
// auto-detection falls back to Tier 1 row_capture.
func TestDetectRollbackCapability_AutoDetect_FallsBackToRowCapture(t *testing.T) {
	db := openSQLiteDB(t)
	cap, err := DetectRollbackCapability(context.Background(), db, "", "")
	if err != nil {
		t.Fatalf("DetectRollbackCapability: %v", err)
	}
	if cap.Mode != RollbackModeRowCapture {
		t.Errorf("Mode = %q, want row_capture (auto-detect fallback)", cap.Mode)
	}
	// Capability fields should be at their zero values since queries failed.
	if cap.WALLevel != "" {
		t.Errorf("WALLevel = %q, want empty (SQLite has no wal_level)", cap.WALLevel)
	}
	if cap.HasReplication {
		t.Error("HasReplication = true, want false (SQLite has no pg_roles)")
	}
}

func TestDetectRollbackCapability_ReturnsNonNilCapability(t *testing.T) {
	db := openSQLiteDB(t)
	cap, err := DetectRollbackCapability(context.Background(), db, "myschema", "")
	if err != nil {
		t.Fatalf("DetectRollbackCapability: %v", err)
	}
	if cap == nil {
		t.Fatal("capability should never be nil")
	}
	if cap.ReplicaIdentity == nil {
		t.Error("ReplicaIdentity map should be initialised (not nil)")
	}
}
