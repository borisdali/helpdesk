package audit

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newFaultStabilityStore(t *testing.T) *FaultStabilityStore {
	t.Helper()
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	fs, err := NewFaultStabilityStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewFaultStabilityStore: %v", err)
	}
	return fs
}

func TestFaultStabilityStore_UpsertAndGet(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := &FaultStabilityCert{
		FaultID:          "db-lock-contention",
		FaultName:        "Lock contention / deadlock",
		PlaybookSeriesID: "pbs_lock_contention_triage",
		Model:            "claude-haiku-4-5-20251001",
		NRuns:            5,
		PassRate:         1.0,
		ConfRangePP:      4,
		IsStable:         true,
	}
	if err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.GetByFaultID(ctx, cert.FaultID)
	if err != nil {
		t.Fatalf("GetByFaultID: %v", err)
	}

	if got.FaultID != cert.FaultID {
		t.Errorf("FaultID: got %q, want %q", got.FaultID, cert.FaultID)
	}
	if got.FaultName != cert.FaultName {
		t.Errorf("FaultName: got %q, want %q", got.FaultName, cert.FaultName)
	}
	if got.PlaybookSeriesID != cert.PlaybookSeriesID {
		t.Errorf("PlaybookSeriesID: got %q, want %q", got.PlaybookSeriesID, cert.PlaybookSeriesID)
	}
	if got.Model != cert.Model {
		t.Errorf("Model: got %q, want %q", got.Model, cert.Model)
	}
	if got.NRuns != cert.NRuns {
		t.Errorf("NRuns: got %d, want %d", got.NRuns, cert.NRuns)
	}
	if got.PassRate != cert.PassRate {
		t.Errorf("PassRate: got %v, want %v", got.PassRate, cert.PassRate)
	}
	if got.ConfRangePP != cert.ConfRangePP {
		t.Errorf("ConfRangePP: got %d, want %d", got.ConfRangePP, cert.ConfRangePP)
	}
	if got.IsStable != cert.IsStable {
		t.Errorf("IsStable: got %v, want %v", got.IsStable, cert.IsStable)
	}
	if got.TestedAt.IsZero() {
		t.Error("TestedAt should not be zero")
	}
}

func TestFaultStabilityStore_Upsert_Overwrites(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	first := &FaultStabilityCert{
		FaultID:  "db-idle-in-transaction",
		NRuns:    3,
		PassRate: 0.67,
		IsStable: false,
	}
	if err := store.Upsert(ctx, first); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	second := &FaultStabilityCert{
		FaultID:  "db-idle-in-transaction",
		NRuns:    5,
		PassRate: 1.0,
		IsStable: true,
	}
	if err := store.Upsert(ctx, second); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := store.GetByFaultID(ctx, "db-idle-in-transaction")
	if err != nil {
		t.Fatalf("GetByFaultID: %v", err)
	}
	if got.NRuns != 5 {
		t.Errorf("NRuns after overwrite: got %d, want 5", got.NRuns)
	}
	if !got.IsStable {
		t.Error("IsStable should be true after overwrite")
	}
}

func TestFaultStabilityStore_GetByFaultID_NotFound(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	_, err := store.GetByFaultID(ctx, "db-nonexistent")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestFaultStabilityStore_IsStable_False_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := &FaultStabilityCert{
		FaultID:  "db-long-running-query",
		NRuns:    3,
		PassRate: 0.33,
		IsStable: false,
	}
	if err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultID(ctx, cert.FaultID)
	if err != nil {
		t.Fatalf("GetByFaultID: %v", err)
	}
	if got.IsStable {
		t.Error("IsStable should be false")
	}
}

func TestFaultStabilityStore_ListAll(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	certs := []*FaultStabilityCert{
		{FaultID: "db-idle-in-transaction", NRuns: 3, IsStable: false},
		{FaultID: "db-lock-contention", NRuns: 5, IsStable: true},
		{FaultID: "db-long-running-query", NRuns: 5, IsStable: true},
	}
	for _, c := range certs {
		if err := store.Upsert(ctx, c); err != nil {
			t.Fatalf("Upsert %s: %v", c.FaultID, err)
		}
	}

	list, err := store.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("ListAll: got %d entries, want 3", len(list))
	}
	// Ordered by fault_id ascending.
	if list[0].FaultID != "db-idle-in-transaction" {
		t.Errorf("list[0].FaultID = %q, want db-idle-in-transaction", list[0].FaultID)
	}
	if list[1].FaultID != "db-lock-contention" {
		t.Errorf("list[1].FaultID = %q, want db-lock-contention", list[1].FaultID)
	}
}

func TestFaultStabilityStore_ListAll_Empty(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	list, err := store.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("ListAll on empty store: got %d entries, want 0", len(list))
	}
}

func TestFaultStabilityStore_TestedAt_Preserved(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	fixed := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cert := &FaultStabilityCert{
		FaultID:  "db-max-connections",
		NRuns:    5,
		IsStable: true,
		TestedAt: fixed,
	}
	if err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultID(ctx, cert.FaultID)
	if err != nil {
		t.Fatalf("GetByFaultID: %v", err)
	}
	// Allow up to 1 second drift from RFC3339Nano round-trip.
	if diff := got.TestedAt.Sub(fixed); diff < -time.Second || diff > time.Second {
		t.Errorf("TestedAt: got %v, want ~%v", got.TestedAt, fixed)
	}
}
