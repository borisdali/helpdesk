package audit

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func newIncidentStore(t *testing.T) *IncidentStore {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	s, err := NewIncidentStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewIncidentStore: %v", err)
	}
	return s
}

func TestIncidentStore_CreateAndGetByID(t *testing.T) {
	s := newIncidentStore(t)
	ctx := context.Background()

	inc := &Incident{
		TraceID:    "tr_abc123",
		EntryRunID: "plr_deadbeef",
	}
	if err := s.Create(ctx, inc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(inc.IncidentID) < 4 || inc.IncidentID[:4] != "inc_" {
		t.Errorf("incident_id = %q, want inc_ prefix", inc.IncidentID)
	}
	// Defaults applied on Create.
	if inc.Origin != IncidentOriginReal {
		t.Errorf("Origin = %q, want %q (default)", inc.Origin, IncidentOriginReal)
	}
	if inc.Status != IncidentStatusOpen {
		t.Errorf("Status = %q, want %q (default)", inc.Status, IncidentStatusOpen)
	}
	if inc.DetectedAt.IsZero() {
		t.Error("DetectedAt should have been defaulted to now, got zero value")
	}

	got, err := s.GetByID(ctx, inc.IncidentID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.TraceID != "tr_abc123" || got.EntryRunID != "plr_deadbeef" {
		t.Errorf("GetByID round-trip mismatch: %+v", got)
	}
	if got.Origin != IncidentOriginReal || got.Status != IncidentStatusOpen {
		t.Errorf("GetByID defaults mismatch: origin=%q status=%q", got.Origin, got.Status)
	}
}

func TestIncidentStore_Create_ExplicitOrigin(t *testing.T) {
	s := newIncidentStore(t)
	ctx := context.Background()

	inc := &Incident{TraceID: "faulttest-abc-db-replica-disconnected-r1", Origin: IncidentOriginFaulttest}
	if err := s.Create(ctx, inc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.GetByID(ctx, inc.IncidentID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Origin != IncidentOriginFaulttest {
		t.Errorf("Origin = %q, want %q — explicit origin must not be overridden by the default", got.Origin, IncidentOriginFaulttest)
	}
}

func TestIncidentStore_GetByID_NotFound(t *testing.T) {
	s := newIncidentStore(t)
	_, err := s.GetByID(context.Background(), "inc_doesnotexist")
	if err != sql.ErrNoRows {
		t.Errorf("GetByID for missing incident = %v, want sql.ErrNoRows", err)
	}
}

func TestIncidentStore_GetByEntryRunID(t *testing.T) {
	s := newIncidentStore(t)
	ctx := context.Background()

	inc := &Incident{EntryRunID: "plr_findme"}
	if err := s.Create(ctx, inc); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.GetByEntryRunID(ctx, "plr_findme")
	if err != nil {
		t.Fatalf("GetByEntryRunID: %v", err)
	}
	if got.IncidentID != inc.IncidentID {
		t.Errorf("GetByEntryRunID returned %q, want %q", got.IncidentID, inc.IncidentID)
	}

	if _, err := s.GetByEntryRunID(ctx, "plr_neverexisted"); err != sql.ErrNoRows {
		t.Errorf("GetByEntryRunID for unlinked run_id = %v, want sql.ErrNoRows", err)
	}
}

func TestIncidentStore_Update_PartialPatchDoesNotClobberOtherFields(t *testing.T) {
	s := newIncidentStore(t)
	ctx := context.Background()

	inc := &Incident{TraceID: "tr_1", Severity: "sev2"}
	if err := s.Create(ctx, inc); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Patch only Status — Severity must survive untouched.
	resolved := IncidentStatusResolved
	if err := s.Update(ctx, inc.IncidentID, IncidentUpdate{Status: &resolved}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := s.GetByID(ctx, inc.IncidentID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != IncidentStatusResolved {
		t.Errorf("Status = %q, want %q", got.Status, IncidentStatusResolved)
	}
	if got.Severity != "sev2" {
		t.Errorf("Severity = %q, want unchanged %q — partial update clobbered an untouched field", got.Severity, "sev2")
	}

	// Patch bundle_path + attribution together, in the shape Phase 2/3 will use.
	attribution := "replica-disconnected"
	bundlePath := "/data/incidents/incident-inc_xyz-20260101.tar.gz"
	resolvedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := s.Update(ctx, inc.IncidentID, IncidentUpdate{
		Attribution: &attribution,
		BundlePath:  &bundlePath,
		ResolvedAt:  &resolvedAt,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err = s.GetByID(ctx, inc.IncidentID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Attribution != attribution || got.BundlePath != bundlePath {
		t.Errorf("second patch mismatch: attribution=%q bundle_path=%q", got.Attribution, got.BundlePath)
	}
	if !got.ResolvedAt.Equal(resolvedAt) {
		t.Errorf("ResolvedAt = %v, want %v", got.ResolvedAt, resolvedAt)
	}
	// Status from the FIRST patch must still hold — proves patches compose.
	if got.Status != IncidentStatusResolved {
		t.Errorf("Status = %q, want %q to have survived the second, unrelated patch", got.Status, IncidentStatusResolved)
	}
}

func TestIncidentStore_Update_NotFound(t *testing.T) {
	s := newIncidentStore(t)
	resolved := IncidentStatusResolved
	err := s.Update(context.Background(), "inc_doesnotexist", IncidentUpdate{Status: &resolved})
	if err != sql.ErrNoRows {
		t.Errorf("Update for missing incident = %v, want sql.ErrNoRows", err)
	}
}

func TestIncidentStore_List_Filters(t *testing.T) {
	s := newIncidentStore(t)
	ctx := context.Background()

	seed := []*Incident{
		{Origin: IncidentOriginReal, Status: IncidentStatusOpen},
		{Origin: IncidentOriginReal, Status: IncidentStatusResolved, Attribution: "oom-kill"},
		{Origin: IncidentOriginFaulttest, Status: IncidentStatusResolved, Attribution: "oom-kill"},
	}
	for _, inc := range seed {
		if err := s.Create(ctx, inc); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	all, err := s.List(ctx, IncidentListFilter{})
	if err != nil {
		t.Fatalf("List (no filter): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List (no filter) = %d incidents, want 3", len(all))
	}

	real, err := s.List(ctx, IncidentListFilter{Origin: IncidentOriginReal})
	if err != nil {
		t.Fatalf("List (origin=real): %v", err)
	}
	if len(real) != 2 {
		t.Errorf("List (origin=real) = %d, want 2", len(real))
	}

	resolved, err := s.List(ctx, IncidentListFilter{Status: IncidentStatusResolved})
	if err != nil {
		t.Fatalf("List (status=resolved): %v", err)
	}
	if len(resolved) != 2 {
		t.Errorf("List (status=resolved) = %d, want 2", len(resolved))
	}

	oomKill, err := s.List(ctx, IncidentListFilter{Attribution: "oom-kill"})
	if err != nil {
		t.Fatalf("List (attribution=oom-kill): %v", err)
	}
	if len(oomKill) != 2 {
		t.Errorf("List (attribution=oom-kill) = %d, want 2", len(oomKill))
	}

	combined, err := s.List(ctx, IncidentListFilter{Origin: IncidentOriginFaulttest, Status: IncidentStatusResolved})
	if err != nil {
		t.Fatalf("List (origin=faulttest, status=resolved): %v", err)
	}
	if len(combined) != 1 {
		t.Errorf("List (origin=faulttest, status=resolved) = %d, want 1", len(combined))
	}
}

func TestIncidentStore_List_MostRecentFirst(t *testing.T) {
	s := newIncidentStore(t)
	ctx := context.Background()

	older := &Incident{DetectedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	newer := &Incident{DetectedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}
	if err := s.Create(ctx, older); err != nil {
		t.Fatalf("Create older: %v", err)
	}
	if err := s.Create(ctx, newer); err != nil {
		t.Fatalf("Create newer: %v", err)
	}

	got, err := s.List(ctx, IncidentListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List = %d incidents, want 2", len(got))
	}
	if got[0].IncidentID != newer.IncidentID {
		t.Errorf("List[0] = %q, want the more recently detected incident %q first", got[0].IncidentID, newer.IncidentID)
	}
}
