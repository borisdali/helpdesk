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

func TestIncidentStore_SeriesID_RoundTrips(t *testing.T) {
	s := newIncidentStore(t)
	ctx := context.Background()

	inc := &Incident{EntryRunID: "plr_seriestest", SeriesID: "pbs_db_max_connections_triage"}
	if err := s.Create(ctx, inc); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.GetByID(ctx, inc.IncidentID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SeriesID != "pbs_db_max_connections_triage" {
		t.Errorf("SeriesID = %q, want pbs_db_max_connections_triage", got.SeriesID)
	}

	byRun, err := s.GetByEntryRunID(ctx, "plr_seriestest")
	if err != nil {
		t.Fatalf("GetByEntryRunID: %v", err)
	}
	if byRun.SeriesID != "pbs_db_max_connections_triage" {
		t.Errorf("GetByEntryRunID SeriesID = %q, want pbs_db_max_connections_triage", byRun.SeriesID)
	}

	listed, err := s.List(ctx, IncidentListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].SeriesID != "pbs_db_max_connections_triage" {
		t.Errorf("List SeriesID not carried through: %+v", listed)
	}
}

func TestIncidentStore_GetByTraceID(t *testing.T) {
	s := newIncidentStore(t)
	ctx := context.Background()

	inc := &Incident{TraceID: "tr_findme", EntryRunID: "plr_x"}
	if err := s.Create(ctx, inc); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.GetByTraceID(ctx, "tr_findme")
	if err != nil {
		t.Fatalf("GetByTraceID: %v", err)
	}
	if got.IncidentID != inc.IncidentID {
		t.Errorf("GetByTraceID returned %q, want %q", got.IncidentID, inc.IncidentID)
	}

	if _, err := s.GetByTraceID(ctx, "tr_never"); err != sql.ErrNoRows {
		t.Errorf("GetByTraceID for unknown trace = %v, want sql.ErrNoRows", err)
	}
}

func TestIncidentStore_DraftPlaybookID_RoundTrips(t *testing.T) {
	s := newIncidentStore(t)
	ctx := context.Background()

	inc := &Incident{TraceID: "tr_draft_test", EntryRunID: "plr_drafttest"}
	if err := s.Create(ctx, inc); err != nil {
		t.Fatalf("Create: %v", err)
	}

	draftID := "pb_draft_abc"
	if err := s.Update(ctx, inc.IncidentID, IncidentUpdate{DraftPlaybookID: &draftID}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.GetByTraceID(ctx, "tr_draft_test")
	if err != nil {
		t.Fatalf("GetByTraceID: %v", err)
	}
	if got.DraftPlaybookID != "pb_draft_abc" {
		t.Errorf("DraftPlaybookID = %q, want pb_draft_abc", got.DraftPlaybookID)
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

func TestIncidentStore_MigrateIsIdempotent(t *testing.T) {
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := NewIncidentStore(store.DB(), false); err != nil {
		t.Fatalf("first NewIncidentStore: %v", err)
	}
	if _, err := NewIncidentStore(store.DB(), false); err != nil {
		t.Fatalf("second NewIncidentStore (migrate should be idempotent): %v", err)
	}
}

// TestIncidentStore_MigrateAddsSeriesID proves the Phase 2 series_id column
// lands correctly on a genuinely pre-existing (Phase 1-shaped) incidents
// table — not just a freshly created one — and that the pre-existing row
// survives untouched. Mirrors this package's established migration-test
// pattern (see TestRunFeedbackStore_MigrateV1ToV2).
func TestIncidentStore_MigrateAddsSeriesID(t *testing.T) {
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	db := store.DB()

	// Seed a pre-Phase-2 (no series_id column) incidents table directly.
	_, err = db.Exec(`CREATE TABLE incidents (
		incident_id              TEXT     NOT NULL PRIMARY KEY,
		trace_id                 TEXT     NOT NULL DEFAULT '',
		entry_run_id             TEXT     NOT NULL DEFAULT '',
		origin                   TEXT     NOT NULL DEFAULT 'real',
		severity                 TEXT     NOT NULL DEFAULT '',
		status                   TEXT     NOT NULL DEFAULT 'open',
		attribution              TEXT     NOT NULL DEFAULT '',
		external_correlation_id  TEXT     NOT NULL DEFAULT '',
		bundle_path              TEXT     NOT NULL DEFAULT '',
		detected_at              DATETIME NOT NULL,
		resolved_at              DATETIME NOT NULL DEFAULT '',
		created_at               DATETIME NOT NULL,
		updated_at               DATETIME NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create pre-Phase-2 table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO incidents
		(incident_id, trace_id, entry_run_id, detected_at, created_at, updated_at)
		VALUES ('inc_preexisting01', 'tr_old', 'plr_old', '2026-01-01 00:00:00', '2026-01-01 00:00:00', '2026-01-01 00:00:00')`)
	if err != nil {
		t.Fatalf("seed pre-existing row: %v", err)
	}

	// Open the store — should trigger migrate() to add series_id.
	s, err := NewIncidentStore(db, false)
	if err != nil {
		t.Fatalf("NewIncidentStore (migrate): %v", err)
	}

	got, err := s.GetByID(context.Background(), "inc_preexisting01")
	if err != nil {
		t.Fatalf("GetByID after migrate: %v", err)
	}
	if got.TraceID != "tr_old" || got.EntryRunID != "plr_old" {
		t.Errorf("pre-existing row corrupted by migration: %+v", got)
	}
	if got.SeriesID != "" {
		t.Errorf("SeriesID = %q, want empty-string default for a pre-existing row", got.SeriesID)
	}

	// New rows can set series_id without collision.
	newInc := &Incident{EntryRunID: "plr_new", SeriesID: "pbs_new_series"}
	if err := s.Create(context.Background(), newInc); err != nil {
		t.Fatalf("Create after migrate: %v", err)
	}
	got2, err := s.GetByID(context.Background(), newInc.IncidentID)
	if err != nil {
		t.Fatalf("GetByID for new row: %v", err)
	}
	if got2.SeriesID != "pbs_new_series" {
		t.Errorf("SeriesID = %q, want pbs_new_series", got2.SeriesID)
	}
}

func TestIncidentStore_List_LimitClamping(t *testing.T) {
	s := newIncidentStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := s.Create(ctx, &Incident{}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// Explicit limit is honored.
	got, err := s.List(ctx, IncidentListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List (limit=2): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("List (limit=2) = %d, want 2", len(got))
	}

	// Zero/negative limit falls back to the default (50), not 0 rows.
	got, err = s.List(ctx, IncidentListFilter{Limit: 0})
	if err != nil {
		t.Fatalf("List (limit=0): %v", err)
	}
	if len(got) != 5 {
		t.Errorf("List (limit=0) = %d, want all 5 (default fallback)", len(got))
	}

	got, err = s.List(ctx, IncidentListFilter{Limit: -1})
	if err != nil {
		t.Fatalf("List (limit=-1): %v", err)
	}
	if len(got) != 5 {
		t.Errorf("List (limit=-1) = %d, want all 5 (default fallback)", len(got))
	}

	// Over-cap limit (>200) also falls back to the default rather than being
	// honored as-is or erroring.
	got, err = s.List(ctx, IncidentListFilter{Limit: 500})
	if err != nil {
		t.Fatalf("List (limit=500): %v", err)
	}
	if len(got) != 5 {
		t.Errorf("List (limit=500) = %d, want all 5 (under the clamped default)", len(got))
	}
}
