package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// FaultStabilityCert records the outcome of a --repeat N faulttest run for one
// fault under a specific diagnosis model. Keyed by (fault_id, diagnosis_model):
// running with a new model creates a new row rather than overwriting the old cert.
// TestedAt lets callers surface staleness.
type FaultStabilityCert struct {
	FaultID          string    `json:"fault_id"`
	FaultName        string    `json:"fault_name"`
	PlaybookSeriesID string    `json:"playbook_series_id,omitempty"`
	DiagnosisModel   string    `json:"diagnosis_model,omitempty"` // agent model that produced the diagnoses — part of the PK
	JudgeModel       string    `json:"judge_model,omitempty"`     // eval judge model; empty when no judge was used
	NRuns            int       `json:"n_runs"`
	PassRate         float64   `json:"pass_rate"`     // 0.0–1.0
	ConfRangePP      int       `json:"conf_range_pp"` // primary-confidence range in percentage points (passing runs only)
	IsStable         bool      `json:"is_stable"`
	TestedAt         time.Time `json:"tested_at"`
}

// FaultStabilityStore persists and retrieves fault triage consistency certs.
type FaultStabilityStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewFaultStabilityStore creates the fault_stability_cert table if needed and
// returns a ready-to-use FaultStabilityStore.
func NewFaultStabilityStore(db *sql.DB, isPostgres bool) (*FaultStabilityStore, error) {
	s := &FaultStabilityStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create fault_stability_cert schema: %w", err)
	}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate fault_stability_cert: %w", err)
	}
	return s, nil
}

func (s *FaultStabilityStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS fault_stability_cert (
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
)`)
	return err
}

// migrate applies schema changes to existing databases. Safe to call on every startup.
func (s *FaultStabilityStore) migrate() error {
	if s.isPostgres {
		return s.migratePostgres()
	}
	return s.migrateSQLite()
}

func (s *FaultStabilityStore) migrateSQLite() error {
	// If the table doesn't exist yet, createSchema already set it up correctly.
	var tableCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='fault_stability_cert'`,
	).Scan(&tableCount); err != nil || tableCount == 0 {
		return nil
	}

	// Check if the table already has the composite PK by looking for the index
	// that SQLite creates for a composite primary key (named "sqlite_autoindex_*")
	// or by checking that the old single-column "fault_id" PK is gone.
	// The simplest reliable check: try to insert a duplicate (fault_id, non-empty model)
	// pair. Instead, check for the presence of the new unique constraint via
	// pragma_index_list — if (fault_id, diagnosis_model) is already the PK,
	// pragma_table_info shows both columns with pk > 0.
	var pkCols int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('fault_stability_cert') WHERE pk > 0`,
	).Scan(&pkCols); err != nil {
		return fmt.Errorf("check PK columns: %w", err)
	}
	if pkCols >= 2 {
		// Already composite PK — no migration needed.
		return nil
	}

	// Ensure diagnosis_model column exists before migration (it was added in a prior release).
	var dmColCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('fault_stability_cert') WHERE name='diagnosis_model'`,
	).Scan(&dmColCount); err == nil && dmColCount == 0 {
		if _, err := s.db.Exec(`ALTER TABLE fault_stability_cert ADD COLUMN diagnosis_model TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add diagnosis_model: %w", err)
		}
	}

	// Recreate the table with a composite PK using a transactional rename approach.
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin cert migration: %w", err)
	}
	if _, err := tx.Exec(`CREATE TABLE fault_stability_cert_new (
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
		tx.Rollback() //nolint:errcheck
		return fmt.Errorf("create cert_new: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO fault_stability_cert_new
		SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model,
		       n_runs, pass_rate, conf_range_pp, is_stable, tested_at
		FROM fault_stability_cert`); err != nil {
		tx.Rollback() //nolint:errcheck
		return fmt.Errorf("copy cert data: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE fault_stability_cert`); err != nil {
		tx.Rollback() //nolint:errcheck
		return fmt.Errorf("drop old cert table: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE fault_stability_cert_new RENAME TO fault_stability_cert`); err != nil {
		tx.Rollback() //nolint:errcheck
		return fmt.Errorf("rename cert table: %w", err)
	}
	return tx.Commit()
}

func (s *FaultStabilityStore) migratePostgres() error {
	// Add diagnosis_model column if missing (from a very old schema).
	s.db.Exec(`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS diagnosis_model TEXT NOT NULL DEFAULT ''`) //nolint:errcheck

	// Check whether the current PK is single-column (fault_id only).
	// If so, drop it and add the composite PK.
	var pkColCount int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM information_schema.key_column_usage
		WHERE table_name = 'fault_stability_cert'
		  AND constraint_name = (
			SELECT constraint_name FROM information_schema.table_constraints
			WHERE table_name = 'fault_stability_cert' AND constraint_type = 'PRIMARY KEY'
		  )`).Scan(&pkColCount)
	if err != nil || pkColCount >= 2 {
		return nil // already composite or error checking (non-fatal)
	}
	if _, err := s.db.Exec(`ALTER TABLE fault_stability_cert DROP CONSTRAINT fault_stability_cert_pkey`); err != nil {
		return fmt.Errorf("drop old PK: %w", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE fault_stability_cert ADD PRIMARY KEY (fault_id, diagnosis_model)`); err != nil {
		return fmt.Errorf("add composite PK: %w", err)
	}
	return nil
}

// Upsert writes a stability cert. Each (fault_id, diagnosis_model) pair is a separate
// cert — running with a new model creates a new row rather than overwriting the old one.
func (s *FaultStabilityStore) Upsert(ctx context.Context, cert *FaultStabilityCert) error {
	if cert.TestedAt.IsZero() {
		cert.TestedAt = time.Now().UTC()
	}
	stableInt := 0
	if cert.IsStable {
		stableInt = 1
	}
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
INSERT INTO fault_stability_cert
    (fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(fault_id, diagnosis_model) DO UPDATE SET
    fault_name         = excluded.fault_name,
    playbook_series_id = excluded.playbook_series_id,
    model              = excluded.model,
    n_runs             = excluded.n_runs,
    pass_rate          = excluded.pass_rate,
    conf_range_pp      = excluded.conf_range_pp,
    is_stable          = excluded.is_stable,
    tested_at          = excluded.tested_at`),
		cert.FaultID, cert.FaultName, cert.PlaybookSeriesID, cert.JudgeModel, cert.DiagnosisModel,
		cert.NRuns, cert.PassRate, cert.ConfRangePP,
		stableInt, cert.TestedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

// GetByFaultID returns the most recently tested stability cert for the given fault,
// regardless of model. For model-specific lookup use GetByFaultAndModel.
// Returns sql.ErrNoRows when none has been recorded.
func (s *FaultStabilityStore) GetByFaultID(ctx context.Context, faultID string) (*FaultStabilityCert, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at
FROM fault_stability_cert WHERE fault_id = ? ORDER BY tested_at DESC LIMIT 1`), faultID)
	return scanCert(row)
}

// GetByFaultAndModel returns the stability cert for a specific (fault_id, diagnosis_model) pair.
// Returns sql.ErrNoRows when none has been recorded.
func (s *FaultStabilityStore) GetByFaultAndModel(ctx context.Context, faultID, model string) (*FaultStabilityCert, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at
FROM fault_stability_cert WHERE fault_id = ? AND diagnosis_model = ?`), faultID, model)
	return scanCert(row)
}

// ListByFaultID returns all stability certs for a given fault, ordered most recent first.
// When only one model has ever been used the slice has one element.
func (s *FaultStabilityStore) ListByFaultID(ctx context.Context, faultID string) ([]*FaultStabilityCert, error) {
	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, `
SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at
FROM fault_stability_cert WHERE fault_id = ? ORDER BY tested_at DESC`), faultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCerts(rows)
}

// ListAll returns all stability certs, ordered by fault_id then by tested_at desc.
func (s *FaultStabilityStore) ListAll(ctx context.Context) ([]*FaultStabilityCert, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at
FROM fault_stability_cert ORDER BY fault_id, tested_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list fault stability certs: %w", err)
	}
	defer rows.Close()
	return scanCerts(rows)
}

type certScanner interface {
	Scan(dest ...any) error
}

func scanCert(s certScanner) (*FaultStabilityCert, error) {
	var (
		cert      FaultStabilityCert
		stableInt int
		testedStr string
	)
	if err := s.Scan(
		&cert.FaultID, &cert.FaultName, &cert.PlaybookSeriesID, &cert.JudgeModel, &cert.DiagnosisModel,
		&cert.NRuns, &cert.PassRate, &cert.ConfRangePP, &stableInt, &testedStr,
	); err != nil {
		return nil, err
	}
	cert.IsStable = stableInt != 0
	if testedStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, testedStr); err == nil {
			cert.TestedAt = t
		}
	}
	return &cert, nil
}

func scanCerts(rows *sql.Rows) ([]*FaultStabilityCert, error) {
	var out []*FaultStabilityCert
	for rows.Next() {
		cert, err := scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cert)
	}
	return out, rows.Err()
}
