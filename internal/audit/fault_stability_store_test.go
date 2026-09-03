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
		DiagnosisModel:   "claude-sonnet-4-6",
		JudgeModel:       "claude-haiku-4-5-20251001",
		NRuns:            5,
		PassRate:         1.0,
		ConfRangePP:      4,
		IsStable:         true,
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
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
	if got.DiagnosisModel != cert.DiagnosisModel {
		t.Errorf("DiagnosisModel: got %q, want %q", got.DiagnosisModel, cert.DiagnosisModel)
	}
	if got.JudgeModel != cert.JudgeModel {
		t.Errorf("JudgeModel: got %q, want %q", got.JudgeModel, cert.JudgeModel)
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
	if _, err := store.Upsert(ctx, first); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	second := &FaultStabilityCert{
		FaultID:  "db-idle-in-transaction",
		NRuns:    5,
		PassRate: 1.0,
		IsStable: true,
	}
	if _, err := store.Upsert(ctx, second); err != nil {
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
	if _, err := store.Upsert(ctx, cert); err != nil {
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
		if _, err := store.Upsert(ctx, c); err != nil {
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

// TestFaultStabilityStore_Migrate verifies that an existing table created without
// the diagnosis_model column gets the column added by migrate().
func TestFaultStabilityStore_Migrate(t *testing.T) {
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Create the old schema (no diagnosis_model column, single-column PK).
	// This triggers both migration steps: add diagnosis_model column AND
	// recreate table for composite PK.
	if _, err := store.DB().Exec(`
CREATE TABLE fault_stability_cert (
    fault_id           TEXT    NOT NULL PRIMARY KEY,
    fault_name         TEXT    NOT NULL DEFAULT '',
    playbook_series_id TEXT    NOT NULL DEFAULT '',
    model              TEXT    NOT NULL DEFAULT '',
    n_runs             INTEGER NOT NULL DEFAULT 0,
    pass_rate          REAL    NOT NULL DEFAULT 0,
    conf_range_pp      INTEGER NOT NULL DEFAULT 0,
    is_stable          INTEGER NOT NULL DEFAULT 0,
    tested_at          TEXT    NOT NULL DEFAULT ''
)`); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	// Seed a row so we verify data survives table recreation.
	if _, err := store.DB().Exec(
		`INSERT INTO fault_stability_cert (fault_id, fault_name, n_runs, is_stable) VALUES ('db-old-fault', 'old name', 3, 1)`,
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	// migrate() must add diagnosis_model column AND recreate table for composite PK.
	fs := &FaultStabilityStore{db: store.DB(), isPostgres: false}
	if err := fs.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Old row must survive the table recreation.
	old, err := fs.GetByFaultID(context.Background(), "db-old-fault")
	if err != nil {
		t.Fatalf("GetByFaultID (old row after migration): %v", err)
	}
	if old.FaultName != "old name" {
		t.Errorf("old row FaultName = %q, want 'old name'", old.FaultName)
	}
	if old.NRuns != 3 {
		t.Errorf("old row NRuns = %d, want 3", old.NRuns)
	}

	// New rows must round-trip DiagnosisModel correctly through composite PK.
	cert := &FaultStabilityCert{
		FaultID:        "db-new-fault",
		DiagnosisModel: "claude-sonnet-4-6",
		JudgeModel:     "claude-haiku-4-5-20251001",
		NRuns:          5,
		IsStable:       true,
	}
	if _, err := fs.Upsert(context.Background(), cert); err != nil {
		t.Fatalf("Upsert after migrate: %v", err)
	}
	got, err := fs.GetByFaultID(context.Background(), "db-new-fault")
	if err != nil {
		t.Fatalf("GetByFaultID: %v", err)
	}
	if got.DiagnosisModel != "claude-sonnet-4-6" {
		t.Errorf("DiagnosisModel: got %q, want claude-sonnet-4-6", got.DiagnosisModel)
	}
	if got.JudgeModel != "claude-haiku-4-5-20251001" {
		t.Errorf("JudgeModel: got %q, want claude-haiku-4-5-20251001", got.JudgeModel)
	}
}

// TestFaultStabilityStore_MultiModel_Coexist verifies that certs for the same
// fault but different diagnosis models are stored independently — the key
// invariant of the composite PK.
func TestFaultStabilityStore_MultiModel_Coexist(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	sonnet := &FaultStabilityCert{
		FaultID:        "db-lock-contention",
		DiagnosisModel: "claude-sonnet-4-6",
		NRuns:          5,
		PassRate:       1.0,
		IsStable:       true,
	}
	haiku := &FaultStabilityCert{
		FaultID:        "db-lock-contention",
		DiagnosisModel: "claude-haiku-4-5-20251001",
		NRuns:          3,
		PassRate:       0.67,
		IsStable:       false,
	}
	if _, err := store.Upsert(ctx, sonnet); err != nil {
		t.Fatalf("Upsert sonnet: %v", err)
	}
	if _, err := store.Upsert(ctx, haiku); err != nil {
		t.Fatalf("Upsert haiku: %v", err)
	}

	// GetByFaultID returns the most recent (both were upserted seconds apart).
	// Both rows must exist — verify via GetByFaultAndModel.
	gotSonnet, err := store.GetByFaultAndModel(ctx, "db-lock-contention", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("GetByFaultAndModel (sonnet): %v", err)
	}
	if !gotSonnet.IsStable {
		t.Error("sonnet cert: IsStable should be true")
	}
	if gotSonnet.NRuns != 5 {
		t.Errorf("sonnet cert: NRuns = %d, want 5", gotSonnet.NRuns)
	}

	gotHaiku, err := store.GetByFaultAndModel(ctx, "db-lock-contention", "claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatalf("GetByFaultAndModel (haiku): %v", err)
	}
	if gotHaiku.IsStable {
		t.Error("haiku cert: IsStable should be false")
	}
	if gotHaiku.NRuns != 3 {
		t.Errorf("haiku cert: NRuns = %d, want 3", gotHaiku.NRuns)
	}

	// Upserting the sonnet cert again must update only its row, not the haiku row.
	sonnet.PassRate = 0.8
	sonnet.IsStable = false
	if _, err := store.Upsert(ctx, sonnet); err != nil {
		t.Fatalf("Upsert sonnet (update): %v", err)
	}
	updated, err := store.GetByFaultAndModel(ctx, "db-lock-contention", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("GetByFaultAndModel after update: %v", err)
	}
	if updated.PassRate != 0.8 {
		t.Errorf("sonnet PassRate after update = %.2f, want 0.80", updated.PassRate)
	}
	// Haiku cert must be unchanged.
	haikuAfter, err := store.GetByFaultAndModel(ctx, "db-lock-contention", "claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatalf("GetByFaultAndModel (haiku after): %v", err)
	}
	if haikuAfter.PassRate != 0.67 {
		t.Errorf("haiku PassRate changed unexpectedly = %.2f", haikuAfter.PassRate)
	}
}

// TestFaultStabilityStore_GetByFaultAndModel_NotFound verifies sql.ErrNoRows
// is returned when no cert exists for the given (fault, model) pair.
func TestFaultStabilityStore_GetByFaultAndModel_NotFound(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	_, err := store.GetByFaultAndModel(ctx, "db-lock-contention", "claude-sonnet-4-6")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

// TestFaultStabilityStore_ListByFaultID verifies all certs for a fault are
// returned regardless of model, while certs for other faults are excluded.
func TestFaultStabilityStore_ListByFaultID(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	// Two models for "db-lock-contention", one for "db-max-connections".
	for _, c := range []*FaultStabilityCert{
		{FaultID: "db-lock-contention", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5, IsStable: true},
		{FaultID: "db-lock-contention", DiagnosisModel: "claude-haiku-4-5-20251001", NRuns: 3, IsStable: false},
		{FaultID: "db-max-connections", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5, IsStable: true},
	} {
		if _, err := store.Upsert(ctx, c); err != nil {
			t.Fatalf("Upsert %s/%s: %v", c.FaultID, c.DiagnosisModel, err)
		}
	}

	list, err := store.ListByFaultID(ctx, "db-lock-contention")
	if err != nil {
		t.Fatalf("ListByFaultID: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListByFaultID: got %d entries, want 2", len(list))
	}
	models := map[string]bool{}
	for _, c := range list {
		if c.FaultID != "db-lock-contention" {
			t.Errorf("unexpected FaultID %q in list", c.FaultID)
		}
		models[c.DiagnosisModel] = true
	}
	if !models["claude-sonnet-4-6"] || !models["claude-haiku-4-5-20251001"] {
		t.Errorf("expected both models in list, got: %v", models)
	}

	// Other fault must not appear.
	other, err := store.ListByFaultID(ctx, "db-max-connections")
	if err != nil {
		t.Fatalf("ListByFaultID (other): %v", err)
	}
	if len(other) != 1 {
		t.Errorf("db-max-connections: got %d entries, want 1", len(other))
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
	if _, err := store.Upsert(ctx, cert); err != nil {
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

// ── v0.21.0 attribution field tests ──────────────────────────────────────────

func TestFaultStabilityCert_AttributionFields_Roundtrip(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	dist := map[string]int{"connection-pool-saturation": 3, "connection-pool-leak": 2}
	cert := &FaultStabilityCert{
		FaultID:                 "db-max-connections",
		FaultName:               "Max Connections",
		DiagnosisModel:          "claude-sonnet-4-6",
		NRuns:                   5,
		PassRate:                0.8,
		IsStable:                true,
		PrimaryAttribution:      "connection-pool-saturation",
		AttributionConsistent:   false,
		AttributionDistribution: dist,
		JudgeSpread:             0.12,
		TaxonomyVersion:         "1.0",
	}

	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.GetByFaultAndModel(ctx, cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}

	if got.PrimaryAttribution != cert.PrimaryAttribution {
		t.Errorf("PrimaryAttribution: got %q, want %q", got.PrimaryAttribution, cert.PrimaryAttribution)
	}
	if got.AttributionConsistent != cert.AttributionConsistent {
		t.Errorf("AttributionConsistent: got %v, want %v", got.AttributionConsistent, cert.AttributionConsistent)
	}
	if got.TaxonomyVersion != cert.TaxonomyVersion {
		t.Errorf("TaxonomyVersion: got %q, want %q", got.TaxonomyVersion, cert.TaxonomyVersion)
	}
	if len(got.AttributionDistribution) != len(dist) {
		t.Errorf("AttributionDistribution len: got %d, want %d",
			len(got.AttributionDistribution), len(dist))
	}
	for k, v := range dist {
		if got.AttributionDistribution[k] != v {
			t.Errorf("AttributionDistribution[%q]: got %d, want %d",
				k, got.AttributionDistribution[k], v)
		}
	}
	if got.JudgeSpread < 0.11 || got.JudgeSpread > 0.13 {
		t.Errorf("JudgeSpread: got %v, want ~0.12", got.JudgeSpread)
	}
}

func TestFaultStabilityCert_AttributionConsistent_True(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := &FaultStabilityCert{
		FaultID:               "db-lock-contention",
		DiagnosisModel:        "claude-haiku-4-5",
		NRuns:                 3,
		IsStable:              true,
		AttributionConsistent: true,
		PrimaryAttribution:    "row-level-lock-contention",
		TaxonomyVersion:       "1.0",
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultAndModel(ctx, cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if !got.AttributionConsistent {
		t.Error("AttributionConsistent: got false, want true")
	}
	if got.PrimaryAttribution != "row-level-lock-contention" {
		t.Errorf("PrimaryAttribution: got %q, want row-level-lock-contention", got.PrimaryAttribution)
	}
}

func TestFaultStabilityCert_AttributionDistribution_Empty(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := &FaultStabilityCert{
		FaultID:        "db-vacuum-needed",
		DiagnosisModel: "gemini-2.0-flash",
		NRuns:          5,
		IsStable:       false,
		// No attribution fields set.
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultAndModel(ctx, cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if len(got.AttributionDistribution) != 0 {
		t.Errorf("AttributionDistribution: got %v, want empty", got.AttributionDistribution)
	}
	if got.PrimaryAttribution != "" {
		t.Errorf("PrimaryAttribution: got %q, want empty", got.PrimaryAttribution)
	}
	if got.TaxonomyVersion != "" {
		t.Errorf("TaxonomyVersion: got %q, want empty", got.TaxonomyVersion)
	}
}

// TestFaultStabilityStore_Migrate_AttributionColumns verifies that a database
// with the v0.20.0 schema (composite PK, no attribution columns) gets the
// attribution columns added by migrate().
func TestFaultStabilityStore_Migrate_AttributionColumns(t *testing.T) {
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "migrate_attr.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Create a pre-v0.21.0 schema: composite PK but no attribution columns.
	if _, err := store.DB().Exec(`
CREATE TABLE fault_stability_cert (
    fault_id           TEXT    NOT NULL,
    fault_name         TEXT    NOT NULL DEFAULT '',
    playbook_series_id TEXT    NOT NULL DEFAULT '',
    model              TEXT    NOT NULL DEFAULT '',
    diagnosis_model    TEXT    NOT NULL DEFAULT '',
    n_runs             INTEGER NOT NULL DEFAULT 0,
    pass_rate          REAL    NOT NULL DEFAULT 0,
    conf_range_pp      INTEGER NOT NULL DEFAULT 0,
    is_stable          INTEGER NOT NULL DEFAULT 0,
    tested_at          TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (fault_id, diagnosis_model)
)`); err != nil {
		t.Fatalf("create pre-v0.21.0 schema: %v", err)
	}
	// Seed a row to verify data survives.
	if _, err := store.DB().Exec(
		`INSERT INTO fault_stability_cert (fault_id, fault_name, n_runs, is_stable, diagnosis_model)
         VALUES ('db-old-fault', 'Old Fault', 3, 1, 'test-model')`,
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	// migrate() detects pkCols=2 → calls addAttributionColumnsSQLite().
	fs := &FaultStabilityStore{db: store.DB(), isPostgres: false}
	if err := fs.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Old row must survive.
	old, err := fs.GetByFaultAndModel(context.Background(), "db-old-fault", "test-model")
	if err != nil {
		t.Fatalf("GetByFaultAndModel (old row): %v", err)
	}
	if old.FaultName != "Old Fault" {
		t.Errorf("FaultName after migration: got %q, want Old Fault", old.FaultName)
	}

	// New rows can use attribution fields after migration.
	cert := &FaultStabilityCert{
		FaultID:            "db-new-fault",
		DiagnosisModel:     "claude-sonnet-4-6",
		NRuns:              5,
		IsStable:           true,
		PrimaryAttribution: "connection-pool-saturation",
		TaxonomyVersion:    "1.0",
	}
	if _, err := fs.Upsert(context.Background(), cert); err != nil {
		t.Fatalf("Upsert after migration: %v", err)
	}
	got, err := fs.GetByFaultAndModel(context.Background(), cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel after migration: %v", err)
	}
	if got.PrimaryAttribution != "connection-pool-saturation" {
		t.Errorf("PrimaryAttribution: got %q, want connection-pool-saturation", got.PrimaryAttribution)
	}
	if got.TaxonomyVersion != "1.0" {
		t.Errorf("TaxonomyVersion: got %q, want 1.0", got.TaxonomyVersion)
	}
}

// TestFaultStabilityStore_Migrate_VersioningColumns is the realistic upgrade
// path a current customer actually hits: a v0.24.0 database already has the
// attribution and CLEAN columns (unlike the pre-v0.21.0 schema the other two
// migration tests simulate), but not playbook_version/playbook_updated_at.
// TestUpsert_PlaybookVersionRoundTrips alone doesn't cover this — it always
// runs against a freshly created schema via newFaultStabilityStore, so the
// ALTER TABLE ADD COLUMN path for these two specific columns was never
// actually exercised until this test.
func TestFaultStabilityStore_Migrate_VersioningColumns(t *testing.T) {
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "migrate_versioning.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// v0.24.0 schema: composite PK, attribution columns, CLEAN columns —
	// everything except playbook_version/playbook_updated_at.
	if _, err := store.DB().Exec(`
CREATE TABLE fault_stability_cert (
    fault_id                  TEXT    NOT NULL,
    fault_name                TEXT    NOT NULL DEFAULT '',
    playbook_series_id        TEXT    NOT NULL DEFAULT '',
    model                     TEXT    NOT NULL DEFAULT '',
    diagnosis_model           TEXT    NOT NULL DEFAULT '',
    n_runs                    INTEGER NOT NULL DEFAULT 0,
    pass_rate                 REAL    NOT NULL DEFAULT 0,
    conf_range_pp             INTEGER NOT NULL DEFAULT 0,
    is_stable                 INTEGER NOT NULL DEFAULT 0,
    tested_at                 TEXT    NOT NULL DEFAULT '',
    primary_attribution       TEXT    NOT NULL DEFAULT '',
    attribution_consistent    INTEGER NOT NULL DEFAULT 0,
    attribution_distribution  TEXT    NOT NULL DEFAULT '{}',
    judge_spread              REAL    NOT NULL DEFAULT 0,
    taxonomy_version          TEXT    NOT NULL DEFAULT '',
    warning_count             INTEGER NOT NULL DEFAULT 0,
    is_clean                  INTEGER NOT NULL DEFAULT 0,
    warning_distribution      TEXT    NOT NULL DEFAULT '{}',
    PRIMARY KEY (fault_id, diagnosis_model)
)`); err != nil {
		t.Fatalf("create v0.24.0 schema: %v", err)
	}
	// Seed a v0.24.0-era row to verify it survives migration untouched.
	if _, err := store.DB().Exec(
		`INSERT INTO fault_stability_cert (fault_id, fault_name, n_runs, is_stable, diagnosis_model, is_clean)
         VALUES ('db-old-fault', 'Old Fault', 5, 1, 'test-model', 1)`,
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	fs := &FaultStabilityStore{db: store.DB(), isPostgres: false}
	if err := fs.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Pre-existing v0.24.0 row must survive, with the new columns defaulting
	// to empty/unknown rather than erroring or losing the row.
	old, err := fs.GetByFaultAndModel(context.Background(), "db-old-fault", "test-model")
	if err != nil {
		t.Fatalf("GetByFaultAndModel (old row): %v", err)
	}
	if old.FaultName != "Old Fault" {
		t.Errorf("FaultName after migration: got %q, want Old Fault", old.FaultName)
	}
	if !old.IsClean {
		t.Error("IsClean after migration: want true (pre-existing v0.24.0 data)")
	}
	if old.PlaybookVersion != "" {
		t.Errorf("PlaybookVersion on a pre-migration row: got %q, want empty (unknown, not fabricated)", old.PlaybookVersion)
	}
	if old.PlaybookID != "" {
		t.Errorf("PlaybookID on a pre-migration row: got %q, want empty (unknown, not fabricated)", old.PlaybookID)
	}

	// New rows can use the versioning fields after migration.
	updatedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	cert := &FaultStabilityCert{
		FaultID: "db-new-fault", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5, IsStable: true,
		PlaybookVersion: "2.1", PlaybookUpdatedAt: updatedAt, PlaybookID: "pb_31575294",
	}
	if _, err := fs.Upsert(context.Background(), cert); err != nil {
		t.Fatalf("Upsert after migration: %v", err)
	}
	got, err := fs.GetByFaultAndModel(context.Background(), cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel after migration: %v", err)
	}
	if got.PlaybookVersion != "2.1" {
		t.Errorf("PlaybookVersion: got %q, want 2.1", got.PlaybookVersion)
	}
	if !got.PlaybookUpdatedAt.Equal(updatedAt) {
		t.Errorf("PlaybookUpdatedAt: got %v, want %v", got.PlaybookUpdatedAt, updatedAt)
	}
	if got.PlaybookID != "pb_31575294" {
		t.Errorf("PlaybookID: got %q, want pb_31575294", got.PlaybookID)
	}

	// The history table must also exist post-migration (createSchema never
	// ran on this store — only migrate() did, mirroring the standalone-store
	// pattern this whole test file uses).
	history, err := fs.GetHistory(context.Background(), "db-new-fault", "claude-sonnet-4-6", 10)
	if err != nil {
		t.Fatalf("GetHistory after migration: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("history entries after migration: got %d, want 1", len(history))
	}
}

// TestFaultStabilityStore_Migrate_HistoryTableMissingPlaybookID is a
// regression test for a real bug found via live testing (2026-08-20): a
// long-running deployment whose fault_stability_cert_history table was
// first created (via ensureCertHistoryTable's CREATE TABLE IF NOT EXISTS)
// back when the CREATE TABLE literal only had playbook_version/
// playbook_updated_at — before playbook_id was added to that literal —
// never got playbook_id backfilled onto the already-existing table. CREATE
// TABLE IF NOT EXISTS is a no-op against an existing table; only an
// explicit ALTER TABLE actually retrofits a column. Unlike
// TestFaultStabilityStore_Migrate_VersioningColumns (which never seeds a
// pre-existing history table, so ensureCertHistoryTable always creates it
// fresh with every current column — never exercising this path), this test
// specifically pre-seeds a history table missing playbook_id to reproduce
// the exact live failure: "table fault_stability_cert_history has no
// column named playbook_id".
func TestFaultStabilityStore_Migrate_HistoryTableMissingPlaybookID(t *testing.T) {
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "migrate_history_playbook_id.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Composite-PK, fully-attributed, fully-CLEAN cert table already at the
	// v0.25.0 playbook_version/playbook_updated_at stage.
	if _, err := store.DB().Exec(`
CREATE TABLE fault_stability_cert (
    fault_id                  TEXT    NOT NULL,
    fault_name                TEXT    NOT NULL DEFAULT '',
    playbook_series_id        TEXT    NOT NULL DEFAULT '',
    model                     TEXT    NOT NULL DEFAULT '',
    diagnosis_model           TEXT    NOT NULL DEFAULT '',
    n_runs                    INTEGER NOT NULL DEFAULT 0,
    pass_rate                 REAL    NOT NULL DEFAULT 0,
    conf_range_pp             INTEGER NOT NULL DEFAULT 0,
    is_stable                 INTEGER NOT NULL DEFAULT 0,
    tested_at                 TEXT    NOT NULL DEFAULT '',
    primary_attribution       TEXT    NOT NULL DEFAULT '',
    attribution_consistent    INTEGER NOT NULL DEFAULT 0,
    attribution_distribution  TEXT    NOT NULL DEFAULT '{}',
    judge_spread               REAL    NOT NULL DEFAULT 0,
    taxonomy_version          TEXT    NOT NULL DEFAULT '',
    warning_count             INTEGER NOT NULL DEFAULT 0,
    is_clean                  INTEGER NOT NULL DEFAULT 0,
    warning_distribution      TEXT    NOT NULL DEFAULT '{}',
    playbook_version          TEXT    NOT NULL DEFAULT '',
    playbook_updated_at       TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (fault_id, diagnosis_model)
)`); err != nil {
		t.Fatalf("create cert table: %v", err)
	}
	// The history table, pre-existing (created by an older binary), also at
	// the playbook_version/playbook_updated_at stage — missing playbook_id,
	// exactly like the live deployment that surfaced this bug.
	if _, err := store.DB().Exec(`
CREATE TABLE fault_stability_cert_history (
    id                        TEXT    NOT NULL PRIMARY KEY,
    fault_id                  TEXT    NOT NULL,
    fault_name                TEXT    NOT NULL DEFAULT '',
    playbook_series_id        TEXT    NOT NULL DEFAULT '',
    model                     TEXT    NOT NULL DEFAULT '',
    diagnosis_model           TEXT    NOT NULL DEFAULT '',
    n_runs                    INTEGER NOT NULL DEFAULT 0,
    pass_rate                 REAL    NOT NULL DEFAULT 0,
    conf_range_pp             INTEGER NOT NULL DEFAULT 0,
    is_stable                 INTEGER NOT NULL DEFAULT 0,
    tested_at                 TEXT    NOT NULL DEFAULT '',
    primary_attribution       TEXT    NOT NULL DEFAULT '',
    attribution_consistent    INTEGER NOT NULL DEFAULT 0,
    attribution_distribution  TEXT    NOT NULL DEFAULT '{}',
    judge_spread              REAL    NOT NULL DEFAULT 0,
    taxonomy_version          TEXT    NOT NULL DEFAULT '',
    warning_count             INTEGER NOT NULL DEFAULT 0,
    is_clean                  INTEGER NOT NULL DEFAULT 0,
    warning_distribution      TEXT    NOT NULL DEFAULT '{}',
    playbook_version          TEXT    NOT NULL DEFAULT '',
    playbook_updated_at       TEXT    NOT NULL DEFAULT '',
    recorded_at               TEXT    NOT NULL DEFAULT ''
)`); err != nil {
		t.Fatalf("create pre-existing history table (missing playbook_id): %v", err)
	}
	// Seed a pre-existing history row to verify it survives migration untouched.
	if _, err := store.DB().Exec(
		`INSERT INTO fault_stability_cert_history (id, fault_id, fault_name, n_runs, is_stable, diagnosis_model, is_clean, recorded_at)
         VALUES ('csh_old0000', 'db-old-fault', 'Old Fault', 5, 1, 'test-model', 1, '2026-08-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed history row: %v", err)
	}

	fs := &FaultStabilityStore{db: store.DB(), isPostgres: false}
	if err := fs.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The exact live failure: Upsert must succeed (not error with "table
	// fault_stability_cert_history has no column named playbook_id") now
	// that migrate() has backfilled the column.
	cert := &FaultStabilityCert{
		FaultID: "custom-target-drift-nudge", DiagnosisModel: "claude-synthetic-test", NRuns: 5,
		IsStable: true, IsClean: false, AttributionConsistent: true,
		PlaybookVersion: "0.1-stale", PlaybookID: "pb_stale0000",
	}
	if _, err := fs.Upsert(context.Background(), cert); err != nil {
		t.Fatalf("Upsert after migration (this is the exact live-found bug): %v", err)
	}

	history, err := fs.GetHistory(context.Background(), "custom-target-drift-nudge", "claude-synthetic-test", 10)
	if err != nil {
		t.Fatalf("GetHistory after migration: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history entries: got %d, want 1", len(history))
	}
	if history[0].PlaybookID != "pb_stale0000" {
		t.Errorf("history[0].PlaybookID: got %q, want pb_stale0000", history[0].PlaybookID)
	}

	// The pre-existing (unrelated) history row must survive migration with
	// PlaybookID defaulting to empty, not lost or erroring.
	oldHistory, err := fs.GetHistory(context.Background(), "db-old-fault", "test-model", 10)
	if err != nil {
		t.Fatalf("GetHistory (pre-existing row): %v", err)
	}
	if len(oldHistory) != 1 {
		t.Fatalf("pre-existing history entries: got %d, want 1", len(oldHistory))
	}
	if oldHistory[0].PlaybookID != "" {
		t.Errorf("pre-existing history row PlaybookID: got %q, want empty (unknown, not fabricated)", oldHistory[0].PlaybookID)
	}
}

// TestFaultStabilityStore_Migrate_AttributionColumns_Idempotent verifies that
// calling migrate() on a fully-migrated database (all attribution columns
// already present) does not fail.
// TestFaultStabilityStore_MigrateSQLite_NoTable verifies that migrateSQLite is
// a no-op when the fault_stability_cert table does not exist — the table will
// be created by createSchema in normal flow, so this path is only reachable
// when migrate is called directly on a raw DB that skipped createSchema.
func TestFaultStabilityStore_MigrateSQLite_NoTable(t *testing.T) {
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "notable.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	fs := &FaultStabilityStore{db: store.DB(), isPostgres: false}
	if err := fs.migrateSQLite(); err != nil {
		t.Fatalf("migrateSQLite on empty DB: %v", err)
	}
}

func TestFaultStabilityStore_Migrate_AttributionColumns_Idempotent(t *testing.T) {
	// newFaultStabilityStore calls NewFaultStabilityStore which runs createSchema+migrate.
	store := newFaultStabilityStore(t)
	ctx := context.Background()

	// A second migrate() call must be idempotent.
	if err := store.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	// Store must still be usable.
	cert := &FaultStabilityCert{
		FaultID:            "db-max-connections",
		DiagnosisModel:     "claude-sonnet-4-6",
		NRuns:              3,
		PrimaryAttribution: "connection-pool-saturation",
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert after idempotent migrate: %v", err)
	}
}

// ── CLEAN axis (WarningCount / IsClean) ─────────────────────────────────────

func TestFaultStabilityCert_CleanFields_Roundtrip(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := &FaultStabilityCert{
		FaultID:        "k8s-oomkilled",
		DiagnosisModel: "claude-sonnet-4-6",
		NRuns:          5,
		IsStable:       true,
		WarningCount:   2,
		IsClean:        false,
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultAndModel(ctx, cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if got.WarningCount != 2 {
		t.Errorf("WarningCount: got %d, want 2", got.WarningCount)
	}
	if got.IsClean {
		t.Error("IsClean: got true, want false")
	}
}

func TestFaultStabilityCert_CleanFields_ZeroValueIsClean(t *testing.T) {
	// A cert with WarningCount=0 and IsClean=true (the common case) must not
	// be confused with the Go zero-value (WarningCount=0, IsClean=false) —
	// confirms IsClean is actually persisted, not just inferred from
	// WarningCount==0 by the reader.
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := &FaultStabilityCert{
		FaultID:        "db-lock-contention",
		DiagnosisModel: "claude-sonnet-4-6",
		NRuns:          5,
		WarningCount:   0,
		IsClean:        true,
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultAndModel(ctx, cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if !got.IsClean {
		t.Error("IsClean: got false, want true")
	}
}

func TestFaultStabilityCert_WarningDistribution_Roundtrip(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	dist := map[string]int{"objective_evidence": 1, "protocol_violation": 2}
	cert := &FaultStabilityCert{
		FaultID:             "k8s-crashloop",
		DiagnosisModel:      "claude-haiku-4-5-20251001",
		NRuns:               5,
		WarningCount:        3,
		IsClean:             false,
		WarningDistribution: dist,
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultAndModel(ctx, cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if len(got.WarningDistribution) != 2 || got.WarningDistribution["objective_evidence"] != 1 || got.WarningDistribution["protocol_violation"] != 2 {
		t.Errorf("WarningDistribution: got %v, want %v", got.WarningDistribution, dist)
	}
}

func TestFaultStabilityCert_WarningDistribution_Empty(t *testing.T) {
	// Mirrors TestFaultStabilityCert_AttributionDistribution_Empty — a cert
	// with no warnings at all must round-trip as a nil/empty map, not a
	// literal "{}" string leaking into the Go value.
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := &FaultStabilityCert{
		FaultID:        "db-auth-failure",
		DiagnosisModel: "claude-haiku-4-5-20251001",
		NRuns:          5,
		IsClean:        true,
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultAndModel(ctx, cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if len(got.WarningDistribution) != 0 {
		t.Errorf("WarningDistribution: got %v, want empty", got.WarningDistribution)
	}
}

func TestFaultStabilityCert_ConfirmedDistribution_Roundtrip(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	dist := map[string]int{"objective_evidence:oom_killed": 4, "objective_evidence:replica_disconnected": 1}
	cert := &FaultStabilityCert{
		FaultID:               "k8s-crashloop",
		DiagnosisModel:        "claude-haiku-4-5-20251001",
		NRuns:                 5,
		IsClean:               true,
		ConfirmedDistribution: dist,
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultAndModel(ctx, cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if len(got.ConfirmedDistribution) != 2 || got.ConfirmedDistribution["objective_evidence:oom_killed"] != 4 ||
		got.ConfirmedDistribution["objective_evidence:replica_disconnected"] != 1 {
		t.Errorf("ConfirmedDistribution: got %v, want %v", got.ConfirmedDistribution, dist)
	}
}

func TestFaultStabilityCert_ConfirmedDistribution_Empty(t *testing.T) {
	// Mirrors TestFaultStabilityCert_WarningDistribution_Empty — a cert
	// where objective evidence never fired at all must round-trip as a
	// nil/empty map, not a literal "{}" string leaking into the Go value.
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := &FaultStabilityCert{
		FaultID:        "db-auth-failure",
		DiagnosisModel: "claude-haiku-4-5-20251001",
		NRuns:          5,
		IsClean:        true,
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultAndModel(ctx, cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if len(got.ConfirmedDistribution) != 0 {
		t.Errorf("ConfirmedDistribution: got %v, want empty", got.ConfirmedDistribution)
	}
}

// TestFaultStabilityCert_WarningAndConfirmedDistribution_Independent verifies
// a cert can carry both distributions at once, distinctly — a real scenario:
// one run's evidence went unconfirmed (warns) while another run's own
// evidence, or a different signal within the same run, was confirmed. The
// two maps must not collide or overwrite each other in storage.
func TestFaultStabilityCert_WarningAndConfirmedDistribution_Independent(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := &FaultStabilityCert{
		FaultID:               "db-replica-disconnected",
		DiagnosisModel:        "claude-haiku-4-5-20251001",
		NRuns:                 3,
		WarningCount:          1,
		WarningDistribution:   map[string]int{"objective_evidence:replica_disconnected": 1},
		ConfirmedDistribution: map[string]int{"objective_evidence:replica_disconnected": 2},
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultAndModel(ctx, cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if got.WarningDistribution["objective_evidence:replica_disconnected"] != 1 {
		t.Errorf("WarningDistribution = %v, want objective_evidence:replica_disconnected=1", got.WarningDistribution)
	}
	if got.ConfirmedDistribution["objective_evidence:replica_disconnected"] != 2 {
		t.Errorf("ConfirmedDistribution = %v, want objective_evidence:replica_disconnected=2", got.ConfirmedDistribution)
	}
}

// TestFaultStabilityStore_Migrate_ConfirmedDistributionColumn verifies a
// pre-v0.27.0 database (has warning_distribution but not
// confirmed_distribution) migrates cleanly: pre-existing rows survive with
// ConfirmedDistribution defaulting to empty, and new rows can use the field
// immediately after migration. Mirrors
// TestFaultStabilityStore_Migrate_VersioningColumns' structure exactly.
func TestFaultStabilityStore_Migrate_ConfirmedDistributionColumn(t *testing.T) {
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "migrate_confirmed_dist.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// v0.26.0 schema: composite PK, attribution, CLEAN, and versioning
	// columns — everything except confirmed_distribution.
	if _, err := store.DB().Exec(`
CREATE TABLE fault_stability_cert (
    fault_id                  TEXT    NOT NULL,
    fault_name                TEXT    NOT NULL DEFAULT '',
    playbook_series_id        TEXT    NOT NULL DEFAULT '',
    model                     TEXT    NOT NULL DEFAULT '',
    diagnosis_model           TEXT    NOT NULL DEFAULT '',
    n_runs                    INTEGER NOT NULL DEFAULT 0,
    pass_rate                 REAL    NOT NULL DEFAULT 0,
    conf_range_pp             INTEGER NOT NULL DEFAULT 0,
    is_stable                 INTEGER NOT NULL DEFAULT 0,
    tested_at                 TEXT    NOT NULL DEFAULT '',
    primary_attribution       TEXT    NOT NULL DEFAULT '',
    attribution_consistent    INTEGER NOT NULL DEFAULT 0,
    attribution_distribution  TEXT    NOT NULL DEFAULT '{}',
    judge_spread              REAL    NOT NULL DEFAULT 0,
    taxonomy_version          TEXT    NOT NULL DEFAULT '',
    warning_count             INTEGER NOT NULL DEFAULT 0,
    is_clean                  INTEGER NOT NULL DEFAULT 0,
    warning_distribution      TEXT    NOT NULL DEFAULT '{}',
    playbook_version          TEXT    NOT NULL DEFAULT '',
    playbook_updated_at       TEXT    NOT NULL DEFAULT '',
    playbook_id               TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (fault_id, diagnosis_model)
)`); err != nil {
		t.Fatalf("create v0.26.0 schema: %v", err)
	}
	// Seed a pre-v0.27.0 row to verify it survives migration untouched.
	if _, err := store.DB().Exec(
		`INSERT INTO fault_stability_cert (fault_id, fault_name, n_runs, is_stable, diagnosis_model, is_clean)
         VALUES ('db-old-fault', 'Old Fault', 5, 1, 'test-model', 1)`,
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	fs := &FaultStabilityStore{db: store.DB(), isPostgres: false}
	if err := fs.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Pre-existing row must survive, ConfirmedDistribution defaulting to
	// empty rather than erroring or losing the row.
	old, err := fs.GetByFaultAndModel(context.Background(), "db-old-fault", "test-model")
	if err != nil {
		t.Fatalf("GetByFaultAndModel (old row): %v", err)
	}
	if old.FaultName != "Old Fault" {
		t.Errorf("FaultName after migration: got %q, want Old Fault", old.FaultName)
	}
	if len(old.ConfirmedDistribution) != 0 {
		t.Errorf("ConfirmedDistribution on a pre-migration row: got %v, want empty", old.ConfirmedDistribution)
	}

	// New rows can use the field immediately after migration.
	cert := &FaultStabilityCert{
		FaultID: "db-new-fault", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5, IsStable: true,
		ConfirmedDistribution: map[string]int{"objective_evidence:oom_killed": 3},
	}
	if _, err := fs.Upsert(context.Background(), cert); err != nil {
		t.Fatalf("Upsert after migration: %v", err)
	}
	got, err := fs.GetByFaultAndModel(context.Background(), cert.FaultID, cert.DiagnosisModel)
	if err != nil {
		t.Fatalf("GetByFaultAndModel after migration: %v", err)
	}
	if got.ConfirmedDistribution["objective_evidence:oom_killed"] != 3 {
		t.Errorf("ConfirmedDistribution: got %v, want objective_evidence:oom_killed=3", got.ConfirmedDistribution)
	}

	// The history table must also have picked up the column post-migration.
	history, err := fs.GetHistory(context.Background(), "db-new-fault", "claude-sonnet-4-6", 10)
	if err != nil {
		t.Fatalf("GetHistory after migration: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history entries after migration: got %d, want 1", len(history))
	}
	if history[0].ConfirmedDistribution["objective_evidence:oom_killed"] != 3 {
		t.Errorf("history ConfirmedDistribution: got %v, want objective_evidence:oom_killed=3", history[0].ConfirmedDistribution)
	}
}

// ── GetBySeriesAndModel ──────────────────────────────────────────────────────

func TestGetBySeriesAndModel_MultipleFaultsSameSeries(t *testing.T) {
	// A single playbook series can map to multiple faults (e.g. one playbook
	// tested against several distinct fault scenarios). GetBySeriesAndModel
	// must return all of them, not just one.
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	certs := []*FaultStabilityCert{
		{FaultID: "k8s-oomkilled", PlaybookSeriesID: "pbs_k8s_pod_crash_triage", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5, IsStable: true, IsClean: true},
		{FaultID: "k8s-crashloop", PlaybookSeriesID: "pbs_k8s_pod_crash_triage", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5, IsStable: true, IsClean: false, WarningCount: 1},
		// Different series — must not be returned.
		{FaultID: "db-lock-contention", PlaybookSeriesID: "pbs_lock_contention_triage", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5, IsStable: true, IsClean: true},
		// Same series, different model — must not be returned.
		{FaultID: "k8s-oomkilled", PlaybookSeriesID: "pbs_k8s_pod_crash_triage", DiagnosisModel: "claude-opus-4-8", NRuns: 5, IsStable: true, IsClean: true},
	}
	for _, c := range certs {
		if _, err := store.Upsert(ctx, c); err != nil {
			t.Fatalf("Upsert(%s, %s): %v", c.FaultID, c.DiagnosisModel, err)
		}
	}

	got, err := store.GetBySeriesAndModel(ctx, "pbs_k8s_pod_crash_triage", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("GetBySeriesAndModel: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 certs, got %d", len(got))
	}
	byFault := map[string]*FaultStabilityCert{}
	for _, c := range got {
		byFault[c.FaultID] = c
	}
	if byFault["k8s-oomkilled"] == nil || !byFault["k8s-oomkilled"].IsClean {
		t.Error("expected k8s-oomkilled cert, IsClean=true")
	}
	if byFault["k8s-crashloop"] == nil || byFault["k8s-crashloop"].IsClean {
		t.Error("expected k8s-crashloop cert, IsClean=false")
	}
}

func TestGetBySeriesAndModel_NoneRecorded(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	got, err := store.GetBySeriesAndModel(ctx, "pbs_never_tested", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("GetBySeriesAndModel: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 certs for a never-tested series, got %d", len(got))
	}
}

// ── EarnsTrust ───────────────────────────────────────────────────────────────

func TestEarnsTrust(t *testing.T) {
	cases := []struct {
		name string
		cert *FaultStabilityCert
		want bool
	}{
		{"nil cert", nil, false},
		{"all three true", &FaultStabilityCert{IsStable: true, IsClean: true, AttributionConsistent: true}, true},
		{"not stable", &FaultStabilityCert{IsStable: false, IsClean: true, AttributionConsistent: true}, false},
		{"not clean", &FaultStabilityCert{IsStable: true, IsClean: false, AttributionConsistent: true}, false},
		{"not attribution-consistent", &FaultStabilityCert{IsStable: true, IsClean: true, AttributionConsistent: false}, false},
		{"none true", &FaultStabilityCert{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cert.EarnsTrust(); got != tc.want {
				t.Errorf("EarnsTrust() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ── Upsert: playbook-version stamping, regression detection, history ────────

func TestUpsert_PlaybookVersionRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	updatedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	cert := &FaultStabilityCert{
		FaultID:           "k8s-oomkilled",
		DiagnosisModel:    "claude-sonnet-4-6",
		NRuns:             5,
		IsStable:          true,
		PlaybookVersion:   "1.3",
		PlaybookUpdatedAt: updatedAt,
		PlaybookID:        "pb_be8b5667",
	}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.GetByFaultAndModel(ctx, "k8s-oomkilled", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if got.PlaybookVersion != "1.3" {
		t.Errorf("PlaybookVersion = %q, want 1.3", got.PlaybookVersion)
	}
	if !got.PlaybookUpdatedAt.Equal(updatedAt) {
		t.Errorf("PlaybookUpdatedAt = %v, want %v", got.PlaybookUpdatedAt, updatedAt)
	}
	if got.PlaybookID != "pb_be8b5667" {
		t.Errorf("PlaybookID = %q, want pb_be8b5667", got.PlaybookID)
	}
}

func TestUpsert_PlaybookVersionEmpty_TreatedAsUnknownNotZeroValue(t *testing.T) {
	// A cert that never had a version stamped (pre-v0.25.0, or a lookup
	// failure at post time) must round-trip as an empty string / zero
	// time.Time, not panic or silently coerce to some other value — callers
	// (printFaultStabilityCert's staleness warning) depend on being able to
	// distinguish "never set" from "explicitly empty."
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := &FaultStabilityCert{FaultID: "db-lock-contention", DiagnosisModel: "claude-sonnet-4-6", NRuns: 3, IsStable: true}
	if _, err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.GetByFaultAndModel(ctx, "db-lock-contention", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if got.PlaybookVersion != "" {
		t.Errorf("PlaybookVersion = %q, want empty", got.PlaybookVersion)
	}
	if !got.PlaybookUpdatedAt.IsZero() {
		t.Errorf("PlaybookUpdatedAt = %v, want zero value", got.PlaybookUpdatedAt)
	}
	if got.PlaybookID != "" {
		t.Errorf("PlaybookID = %q, want empty", got.PlaybookID)
	}
}

func TestUpsert_Regressed_FalseOnFirstEverCert(t *testing.T) {
	// Never having earned trust isn't a regression *from* trust — there's no
	// prior state to have fallen from. Distinct from trustNotYetEarnedForceGate
	// (gateway-side), which does treat "never certified" as "not earned" —
	// two different questions asked of the same fact.
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := &FaultStabilityCert{FaultID: "db-lock-contention", DiagnosisModel: "claude-sonnet-4-6", NRuns: 3, IsStable: false}
	regressed, err := store.Upsert(ctx, cert)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if regressed {
		t.Error("expected regressed=false on the very first cert for this fault+model")
	}
}

func TestUpsert_Regressed_TrueWhenTrustEarningCertStopsEarningTrust(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	earning := &FaultStabilityCert{
		FaultID: "k8s-oomkilled", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5,
		IsStable: true, IsClean: true, AttributionConsistent: true,
	}
	if regressed, err := store.Upsert(ctx, earning); err != nil {
		t.Fatalf("Upsert (earning): %v", err)
	} else if regressed {
		t.Error("expected regressed=false when the fault first earns trust (no prior state to regress from)")
	}

	noLongerEarning := &FaultStabilityCert{
		FaultID: "k8s-oomkilled", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5,
		IsStable: true, IsClean: false, AttributionConsistent: true, WarningCount: 2,
	}
	regressed, err := store.Upsert(ctx, noLongerEarning)
	if err != nil {
		t.Fatalf("Upsert (no longer earning): %v", err)
	}
	if !regressed {
		t.Error("expected regressed=true when a previously trust-earning cert becomes CLEAN=false")
	}
}

func TestUpsert_Regressed_FalseWhenAlreadyNotEarningTrust(t *testing.T) {
	// A cert that wasn't earning trust yesterday and still isn't today
	// hasn't regressed — it never recovered, which is a different (and, per
	// the design, not separately alerted-on) fact.
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	unstable := &FaultStabilityCert{FaultID: "db-lock-contention", DiagnosisModel: "claude-sonnet-4-6", NRuns: 3, IsStable: false}
	if _, err := store.Upsert(ctx, unstable); err != nil {
		t.Fatalf("Upsert (1st): %v", err)
	}
	stillUnstable := &FaultStabilityCert{FaultID: "db-lock-contention", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5, IsStable: false}
	regressed, err := store.Upsert(ctx, stillUnstable)
	if err != nil {
		t.Fatalf("Upsert (2nd): %v", err)
	}
	if regressed {
		t.Error("expected regressed=false — cert was already not earning trust, nothing changed for the worse")
	}
}

func TestUpsert_Regressed_FalseWhenStayingTrustEarning(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	cert := func(n int) *FaultStabilityCert {
		return &FaultStabilityCert{
			FaultID: "k8s-oomkilled", DiagnosisModel: "claude-sonnet-4-6", NRuns: n,
			IsStable: true, IsClean: true, AttributionConsistent: true,
		}
	}
	if _, err := store.Upsert(ctx, cert(5)); err != nil {
		t.Fatalf("Upsert (1st): %v", err)
	}
	regressed, err := store.Upsert(ctx, cert(10))
	if err != nil {
		t.Fatalf("Upsert (2nd): %v", err)
	}
	if regressed {
		t.Error("expected regressed=false — cert stayed trust-earning across recertification")
	}
}

// TestUpsert_Regressed_FalseOnRecovery covers the improvement direction
// explicitly: a fault that wasn't earning trust and now does is not a
// regression (the opposite of one) — worth its own test, not just an
// implication of the boolean logic, since a naive "did EarnsTrust() change"
// check (rather than the specific true→false direction) would have wrongly
// flagged this as a regression too.
func TestUpsert_Regressed_FalseOnRecovery(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	notEarning := &FaultStabilityCert{FaultID: "k8s-oomkilled", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5, IsStable: false}
	if _, err := store.Upsert(ctx, notEarning); err != nil {
		t.Fatalf("Upsert (not earning): %v", err)
	}

	nowEarning := &FaultStabilityCert{
		FaultID: "k8s-oomkilled", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5,
		IsStable: true, IsClean: true, AttributionConsistent: true,
	}
	regressed, err := store.Upsert(ctx, nowEarning)
	if err != nil {
		t.Fatalf("Upsert (now earning): %v", err)
	}
	if regressed {
		t.Error("expected regressed=false — this is a recovery (not-earning → earning), the opposite of a regression")
	}
}

func TestUpsert_AppendsHistoryRow(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	for i := 1; i <= 3; i++ {
		cert := &FaultStabilityCert{FaultID: "k8s-oomkilled", DiagnosisModel: "claude-sonnet-4-6", NRuns: i, IsStable: true}
		if _, err := store.Upsert(ctx, cert); err != nil {
			t.Fatalf("Upsert #%d: %v", i, err)
		}
	}

	history, err := store.GetHistory(ctx, "k8s-oomkilled", "claude-sonnet-4-6", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 history entries (one per Upsert), got %d", len(history))
	}
	// Latest cert table row still only holds the single most recent snapshot —
	// history is additive, not a replacement for the fast-lookup shape.
	latest, err := store.GetByFaultAndModel(ctx, "k8s-oomkilled", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("GetByFaultAndModel: %v", err)
	}
	if latest.NRuns != 3 {
		t.Errorf("latest cert NRuns = %d, want 3 (most recent Upsert)", latest.NRuns)
	}
}

func TestUpsert_History_DifferentModel_SeparateHistory(t *testing.T) {
	// History is scoped per (fault_id, diagnosis_model), same as the cert
	// itself — a different model's recertifications must not bleed into
	// this model's trend.
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	if _, err := store.Upsert(ctx, &FaultStabilityCert{FaultID: "k8s-oomkilled", DiagnosisModel: "claude-sonnet-4-6", NRuns: 5, IsStable: true}); err != nil {
		t.Fatalf("Upsert (sonnet): %v", err)
	}
	if _, err := store.Upsert(ctx, &FaultStabilityCert{FaultID: "k8s-oomkilled", DiagnosisModel: "claude-haiku-4-5", NRuns: 5, IsStable: true}); err != nil {
		t.Fatalf("Upsert (haiku): %v", err)
	}

	sonnetHistory, err := store.GetHistory(ctx, "k8s-oomkilled", "claude-sonnet-4-6", 10)
	if err != nil {
		t.Fatalf("GetHistory (sonnet): %v", err)
	}
	if len(sonnetHistory) != 1 {
		t.Errorf("sonnet history: got %d entries, want 1 (haiku's Upsert must not appear here)", len(sonnetHistory))
	}
}

func TestGetHistory_RespectsLimit(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	// TestedAt must be distinct per Upsert — GetHistory orders by recorded_at
	// (a direct copy of the cert's own TestedAt, not a server-generated
	// insert timestamp), which has no secondary tiebreaker. Real callers
	// always set TestedAt to a genuine time.Now() when a real faulttest run
	// completes, so this can't collide in production, but a fixture that
	// leaves it at its zero value (as this test originally did) makes every
	// row tie — an ORDER BY on an all-tied column has no guaranteed result
	// order, which is exactly what made this test flaky under heavier
	// concurrent load (passed in isolation, failed under `make test-nocache`).
	base := time.Now()
	for i := 1; i <= 5; i++ {
		cert := &FaultStabilityCert{FaultID: "k8s-oomkilled", DiagnosisModel: "claude-sonnet-4-6", NRuns: i, IsStable: true, TestedAt: base.Add(time.Duration(i) * time.Minute)}
		if _, err := store.Upsert(ctx, cert); err != nil {
			t.Fatalf("Upsert #%d: %v", i, err)
		}
	}
	history, err := store.GetHistory(ctx, "k8s-oomkilled", "claude-sonnet-4-6", 2)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 entries (limit=2), got %d", len(history))
	}
	// Most recent first: the last two Upserts had NRuns=4 and NRuns=5.
	if history[0].NRuns != 5 || history[1].NRuns != 4 {
		t.Errorf("expected most-recent-first order [5, 4], got [%d, %d]", history[0].NRuns, history[1].NRuns)
	}
}

func TestGetHistory_NeverCertified_ReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	store := newFaultStabilityStore(t)

	history, err := store.GetHistory(ctx, "db-never-tested", "claude-sonnet-4-6", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("expected 0 history entries, got %d", len(history))
	}
}
