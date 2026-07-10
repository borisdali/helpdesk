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
	if err := fs.Upsert(context.Background(), cert); err != nil {
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
	if err := store.Upsert(ctx, sonnet); err != nil {
		t.Fatalf("Upsert sonnet: %v", err)
	}
	if err := store.Upsert(ctx, haiku); err != nil {
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
	if err := store.Upsert(ctx, sonnet); err != nil {
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
		if err := store.Upsert(ctx, c); err != nil {
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

	if err := store.Upsert(ctx, cert); err != nil {
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
	if err := store.Upsert(ctx, cert); err != nil {
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
	if err := store.Upsert(ctx, cert); err != nil {
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
	if err := fs.Upsert(context.Background(), cert); err != nil {
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

// TestFaultStabilityStore_Migrate_AttributionColumns_Idempotent verifies that
// calling migrate() on a fully-migrated database (all attribution columns
// already present) does not fail.
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
	if err := store.Upsert(ctx, cert); err != nil {
		t.Fatalf("Upsert after idempotent migrate: %v", err)
	}
}
