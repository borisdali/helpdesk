package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
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

	// Attribution fields (v0.21.0): conclusion-stability measurement.
	// PrimaryAttribution is the plurality root-cause label across all cert runs;
	// "UNKNOWN" when the classifier could not match any run's FINDINGS.
	PrimaryAttribution      string         `json:"primary_attribution,omitempty"`
	AttributionConsistent   bool           `json:"attribution_consistent"`
	AttributionDistribution map[string]int `json:"attribution_distribution,omitempty"`
	JudgeSpread             float64        `json:"judge_spread,omitempty"`
	TaxonomyVersion         string         `json:"taxonomy_version,omitempty"`

	// CLEAN axis fields: rate of verified (code-derived, not self-reported)
	// warning signals across the cert's N runs — see hasCleanWarning in
	// testing/cmd/faulttest/evaluator.go for exactly which three signals count.
	// A distinct axis from IsStable: a playbook can be perfectly stable while
	// still consistently missing evidence it should have acted on.
	WarningCount int  `json:"warning_count"`
	IsClean      bool `json:"is_clean"`
	// WarningDistribution counts occurrences of each warning type across the
	// cert's N runs (e.g. {"objective_evidence": 1, "protocol_violation": 1}),
	// mirroring AttributionDistribution's shape/purpose exactly — WarningCount
	// alone can't tell an operator which kind of warning tripped a cert.
	WarningDistribution map[string]int `json:"warning_distribution,omitempty"`

	// Versioning fields (v0.25.0): which version of the playbook this cert was
	// earned against. Deliberately NOT part of the composite key — a mismatch
	// between PlaybookVersion and the playbook's current version is a signal
	// ("this cert may be stale, recertify") not a reason to fork the cert row.
	// Empty PlaybookVersion means the cert predates this field (pre-v0.25.0)
	// and staleness can't be determined — callers should treat that as
	// "unknown," not "fresh."
	PlaybookVersion   string    `json:"playbook_version,omitempty"`
	PlaybookUpdatedAt time.Time `json:"playbook_updated_at,omitempty"`
	// PlaybookID is the concrete pb_* version ID (as opposed to PlaybookVersion's
	// human version number, e.g. "1.3") the cert was earned against — captured
	// so a staleness warning can point directly at `vault diff <id-then>
	// <id-now>` instead of just asserting "the playbook changed" with no way
	// to act on it. Same non-key treatment as PlaybookVersion; empty means
	// unknown (pre-this-field cert), not "no playbook."
	PlaybookID string `json:"playbook_id,omitempty"`
}

// EarnsTrust reports whether this cert clears the same three-condition bar
// cmd/gateway/playbooks.go's trustNotYetEarnedForceGate requires before a
// real (non-faulttest) incident is allowed to auto-chain unattended:
// IsStable, IsClean, and AttributionConsistent, all true. Used both by that
// gate (indirectly, via the same three fields) and by Upsert's regression
// detection — "did this cert stop earning trust" is the same question asked
// at two different times (per-request vs. per-recertification).
func (c *FaultStabilityCert) EarnsTrust() bool {
	if c == nil {
		return false
	}
	return c.IsStable && c.IsClean && c.AttributionConsistent
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
)`)
	if err != nil {
		return err
	}
	return s.ensureCertHistoryTable()
}

// ensureCertHistoryTable creates fault_stability_cert_history if it doesn't
// already exist — an append-only log of every Upsert. The cert table itself
// only ever holds the latest snapshot per (fault_id, diagnosis_model), so
// without this there is no way to answer "was this cert STABLE+CLEAN a
// month ago, and when did that change," only "what is it right now."
// Deliberately a separate table rather than a row-versioning scheme on
// fault_stability_cert itself — the cert table's fast-lookup shape (one row
// per fault+model) stays untouched; this is purely additive. id is
// Go-generated ("csh_" + uuid[:8]), matching this codebase's existing
// ID-generation convention (e.g. audit event IDs), rather than relying on
// backend-specific AUTOINCREMENT/SERIAL semantics that would otherwise have
// to be reconciled between the SQLite and Postgres schemas.
//
// Called from both createSchema (the normal NewFaultStabilityStore path)
// and migrate (which some callers — including this package's own migration
// tests — invoke standalone, on a *FaultStabilityStore built directly
// without going through NewFaultStabilityStore/createSchema first). Both
// statements are idempotent (IF NOT EXISTS), so calling this twice on a
// normal startup is harmless.
func (s *FaultStabilityStore) ensureCertHistoryTable() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS fault_stability_cert_history (
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
    playbook_id               TEXT    NOT NULL DEFAULT '',
    recorded_at               TEXT    NOT NULL DEFAULT ''
)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_cert_history_lookup
    ON fault_stability_cert_history (fault_id, diagnosis_model, recorded_at DESC)`)
	return err
}

// migrate applies schema changes to existing databases. Safe to call on every startup.
func (s *FaultStabilityStore) migrate() error {
	if err := s.ensureCertHistoryTable(); err != nil {
		return err
	}
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
		// Composite PK already in place — only need the additive columns.
		if err := s.addAttributionColumnsSQLite(); err != nil {
			return err
		}
		if err := s.addCleanColumnsSQLite(); err != nil {
			return err
		}
		return s.addVersioningColumnsSQLite()
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
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := s.addAttributionColumnsSQLite(); err != nil {
		return err
	}
	if err := s.addCleanColumnsSQLite(); err != nil {
		return err
	}
	return s.addVersioningColumnsSQLite()
}

// addAttributionColumnsSQLite adds the v0.21.0 attribution columns to existing
// SQLite databases. Duplicate-column errors are silently ignored.
func (s *FaultStabilityStore) addAttributionColumnsSQLite() error {
	attrCols := []string{
		`ALTER TABLE fault_stability_cert ADD COLUMN primary_attribution      TEXT    NOT NULL DEFAULT ''`,
		`ALTER TABLE fault_stability_cert ADD COLUMN attribution_consistent   INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE fault_stability_cert ADD COLUMN attribution_distribution TEXT    NOT NULL DEFAULT '{}'`,
		`ALTER TABLE fault_stability_cert ADD COLUMN judge_spread             REAL    NOT NULL DEFAULT 0`,
		`ALTER TABLE fault_stability_cert ADD COLUMN taxonomy_version         TEXT    NOT NULL DEFAULT ''`,
	}
	for _, stmt := range attrCols {
		if _, err := s.db.Exec(stmt); err != nil {
			if !containsAny(err.Error(), "duplicate column", "already exists") {
				return fmt.Errorf("cert migrate: %s: %w", stmt, err)
			}
		}
	}
	return nil
}

// addCleanColumnsSQLite adds the CLEAN-axis columns to existing SQLite
// databases. Duplicate-column errors are silently ignored, same as
// addAttributionColumnsSQLite.
func (s *FaultStabilityStore) addCleanColumnsSQLite() error {
	cleanCols := []string{
		`ALTER TABLE fault_stability_cert ADD COLUMN warning_count        INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE fault_stability_cert ADD COLUMN is_clean             INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE fault_stability_cert ADD COLUMN warning_distribution TEXT    NOT NULL DEFAULT '{}'`,
	}
	for _, stmt := range cleanCols {
		if _, err := s.db.Exec(stmt); err != nil {
			if !containsAny(err.Error(), "duplicate column", "already exists") {
				return fmt.Errorf("cert migrate: %s: %w", stmt, err)
			}
		}
	}
	return nil
}

// addVersioningColumnsSQLite adds the v0.25.0 playbook-version staleness
// columns to existing SQLite databases. Duplicate-column errors are silently
// ignored, same as addAttributionColumnsSQLite/addCleanColumnsSQLite.
func (s *FaultStabilityStore) addVersioningColumnsSQLite() error {
	versionCols := []string{
		`ALTER TABLE fault_stability_cert ADD COLUMN playbook_version    TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE fault_stability_cert ADD COLUMN playbook_updated_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE fault_stability_cert ADD COLUMN playbook_id         TEXT NOT NULL DEFAULT ''`,
	}
	for _, stmt := range versionCols {
		if _, err := s.db.Exec(stmt); err != nil {
			if !containsAny(err.Error(), "duplicate column", "already exists") {
				return fmt.Errorf("cert migrate: %s: %w", stmt, err)
			}
		}
	}
	return nil
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
	if err == nil && pkColCount < 2 {
		if _, err := s.db.Exec(`ALTER TABLE fault_stability_cert DROP CONSTRAINT fault_stability_cert_pkey`); err != nil {
			return fmt.Errorf("drop old PK: %w", err)
		}
		if _, err := s.db.Exec(`ALTER TABLE fault_stability_cert ADD PRIMARY KEY (fault_id, diagnosis_model)`); err != nil {
			return fmt.Errorf("add composite PK: %w", err)
		}
	}

	// v0.21.0 attribution columns.
	attrCols := []string{
		`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS primary_attribution      TEXT    NOT NULL DEFAULT ''`,
		`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS attribution_consistent   INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS attribution_distribution TEXT    NOT NULL DEFAULT '{}'`,
		`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS judge_spread             REAL    NOT NULL DEFAULT 0`,
		`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS taxonomy_version         TEXT    NOT NULL DEFAULT ''`,
	}
	for _, stmt := range attrCols {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("cert migrate postgres: %s: %w", stmt, err)
		}
	}

	// CLEAN-axis columns.
	cleanCols := []string{
		`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS warning_count        INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS is_clean             INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS warning_distribution TEXT    NOT NULL DEFAULT '{}'`,
	}
	for _, stmt := range cleanCols {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("cert migrate postgres: %s: %w", stmt, err)
		}
	}

	// v0.25.0 playbook-version staleness columns.
	versionCols := []string{
		`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS playbook_version    TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS playbook_updated_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE fault_stability_cert ADD COLUMN IF NOT EXISTS playbook_id         TEXT NOT NULL DEFAULT ''`,
	}
	for _, stmt := range versionCols {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("cert migrate postgres: %s: %w", stmt, err)
		}
	}
	return nil
}

// Upsert writes a stability cert and appends it to fault_stability_cert_history,
// atomically. Each (fault_id, diagnosis_model) pair is a separate cert —
// running with a new model creates a new row rather than overwriting the old
// one. Returns regressed=true when the fault+model previously earned trust
// (see FaultStabilityCert.EarnsTrust) and this new cert does not — the read
// of the prior state and the write of the new one happen inside the same
// transaction, so this is race-free against concurrent recertification of
// the same fault+model (which would be unusual, but not impossible if two
// faulttest runs overlap).
func (s *FaultStabilityStore) Upsert(ctx context.Context, cert *FaultStabilityCert) (regressed bool, err error) {
	if cert.TestedAt.IsZero() {
		cert.TestedAt = time.Now().UTC()
	}
	stableInt := 0
	if cert.IsStable {
		stableInt = 1
	}
	attrConsistentInt := 0
	if cert.AttributionConsistent {
		attrConsistentInt = 1
	}
	attrDistJSON := []byte("{}")
	if len(cert.AttributionDistribution) > 0 {
		if b, jerr := json.Marshal(cert.AttributionDistribution); jerr == nil {
			attrDistJSON = b
		}
	}
	cleanInt := 0
	if cert.IsClean {
		cleanInt = 1
	}
	warnDistJSON := []byte("{}")
	if len(cert.WarningDistribution) > 0 {
		if b, jerr := json.Marshal(cert.WarningDistribution); jerr == nil {
			warnDistJSON = b
		}
	}
	playbookUpdatedAtStr := ""
	if !cert.PlaybookUpdatedAt.IsZero() {
		playbookUpdatedAtStr = cert.PlaybookUpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	testedAtStr := cert.TestedAt.UTC().Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin cert upsert: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	prior, err := scanCert(tx.QueryRowContext(ctx, rebind(s.isPostgres, `
SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at, primary_attribution, attribution_consistent, attribution_distribution, judge_spread, taxonomy_version, warning_count, is_clean, warning_distribution, playbook_version, playbook_updated_at, playbook_id
FROM fault_stability_cert WHERE fault_id = ? AND diagnosis_model = ?`), cert.FaultID, cert.DiagnosisModel))
	switch {
	case err == nil:
		regressed = prior.EarnsTrust() && !cert.EarnsTrust()
	case errors.Is(err, sql.ErrNoRows):
		// No prior cert — never having earned trust isn't a regression from it.
	default:
		return false, fmt.Errorf("read prior cert: %w", err)
	}

	if _, err := tx.ExecContext(ctx, rebind(s.isPostgres, `
INSERT INTO fault_stability_cert
    (fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at,
     primary_attribution, attribution_consistent, attribution_distribution, judge_spread, taxonomy_version,
     warning_count, is_clean, warning_distribution, playbook_version, playbook_updated_at, playbook_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(fault_id, diagnosis_model) DO UPDATE SET
    fault_name                = excluded.fault_name,
    playbook_series_id        = excluded.playbook_series_id,
    model                     = excluded.model,
    n_runs                    = excluded.n_runs,
    pass_rate                 = excluded.pass_rate,
    conf_range_pp             = excluded.conf_range_pp,
    is_stable                 = excluded.is_stable,
    tested_at                 = excluded.tested_at,
    primary_attribution       = excluded.primary_attribution,
    attribution_consistent    = excluded.attribution_consistent,
    attribution_distribution  = excluded.attribution_distribution,
    judge_spread              = excluded.judge_spread,
    taxonomy_version          = excluded.taxonomy_version,
    warning_count             = excluded.warning_count,
    is_clean                  = excluded.is_clean,
    warning_distribution      = excluded.warning_distribution,
    playbook_version          = excluded.playbook_version,
    playbook_updated_at       = excluded.playbook_updated_at,
    playbook_id               = excluded.playbook_id`),
		cert.FaultID, cert.FaultName, cert.PlaybookSeriesID, cert.JudgeModel, cert.DiagnosisModel,
		cert.NRuns, cert.PassRate, cert.ConfRangePP,
		stableInt, testedAtStr,
		cert.PrimaryAttribution, attrConsistentInt, string(attrDistJSON),
		cert.JudgeSpread, cert.TaxonomyVersion,
		cert.WarningCount, cleanInt, string(warnDistJSON),
		cert.PlaybookVersion, playbookUpdatedAtStr, cert.PlaybookID,
	); err != nil {
		return false, fmt.Errorf("upsert cert: %w", err)
	}

	historyID := "csh_" + uuid.New().String()[:8]
	if _, err := tx.ExecContext(ctx, rebind(s.isPostgres, `
INSERT INTO fault_stability_cert_history
    (id, fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at,
     primary_attribution, attribution_consistent, attribution_distribution, judge_spread, taxonomy_version,
     warning_count, is_clean, warning_distribution, playbook_version, playbook_updated_at, playbook_id, recorded_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		historyID, cert.FaultID, cert.FaultName, cert.PlaybookSeriesID, cert.JudgeModel, cert.DiagnosisModel,
		cert.NRuns, cert.PassRate, cert.ConfRangePP,
		stableInt, testedAtStr,
		cert.PrimaryAttribution, attrConsistentInt, string(attrDistJSON),
		cert.JudgeSpread, cert.TaxonomyVersion,
		cert.WarningCount, cleanInt, string(warnDistJSON),
		cert.PlaybookVersion, playbookUpdatedAtStr, cert.PlaybookID, testedAtStr,
	); err != nil {
		return false, fmt.Errorf("insert cert history: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit cert upsert: %w", err)
	}
	return regressed, nil
}

// GetByFaultID returns the most recently tested stability cert for the given fault,
// regardless of model. For model-specific lookup use GetByFaultAndModel.
// Returns sql.ErrNoRows when none has been recorded.
func (s *FaultStabilityStore) GetByFaultID(ctx context.Context, faultID string) (*FaultStabilityCert, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at, primary_attribution, attribution_consistent, attribution_distribution, judge_spread, taxonomy_version, warning_count, is_clean, warning_distribution, playbook_version, playbook_updated_at, playbook_id
FROM fault_stability_cert WHERE fault_id = ? ORDER BY tested_at DESC LIMIT 1`), faultID)
	return scanCert(row)
}

// GetByFaultAndModel returns the stability cert for a specific (fault_id, diagnosis_model) pair.
// Returns sql.ErrNoRows when none has been recorded.
func (s *FaultStabilityStore) GetByFaultAndModel(ctx context.Context, faultID, model string) (*FaultStabilityCert, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at, primary_attribution, attribution_consistent, attribution_distribution, judge_spread, taxonomy_version, warning_count, is_clean, warning_distribution, playbook_version, playbook_updated_at, playbook_id
FROM fault_stability_cert WHERE fault_id = ? AND diagnosis_model = ?`), faultID, model)
	return scanCert(row)
}

// GetBySeriesAndModel returns every stability cert for a given playbook
// series + diagnosis model — a series can map to multiple faults (e.g. one
// playbook tested against several distinct fault scenarios), so this returns
// a slice, not a single cert. Used by the gateway's trust-gate check: a
// playbook has "earned" unattended trust for a model only when every known
// cert for that series+model is both IsStable and IsClean. An empty slice
// means no cert has ever been recorded for this series+model — callers
// should treat that the same as "not earned" (fail closed), not as "passing
// vacuously."
func (s *FaultStabilityStore) GetBySeriesAndModel(ctx context.Context, seriesID, model string) ([]*FaultStabilityCert, error) {
	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, `
SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at, primary_attribution, attribution_consistent, attribution_distribution, judge_spread, taxonomy_version, warning_count, is_clean, warning_distribution, playbook_version, playbook_updated_at, playbook_id
FROM fault_stability_cert WHERE playbook_series_id = ? AND diagnosis_model = ?`), seriesID, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCerts(rows)
}

// ListByFaultID returns all stability certs for a given fault, ordered most recent first.
// When only one model has ever been used the slice has one element.
func (s *FaultStabilityStore) ListByFaultID(ctx context.Context, faultID string) ([]*FaultStabilityCert, error) {
	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, `
SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at, primary_attribution, attribution_consistent, attribution_distribution, judge_spread, taxonomy_version, warning_count, is_clean, warning_distribution, playbook_version, playbook_updated_at, playbook_id
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
SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at, primary_attribution, attribution_consistent, attribution_distribution, judge_spread, taxonomy_version, warning_count, is_clean, warning_distribution, playbook_version, playbook_updated_at, playbook_id
FROM fault_stability_cert ORDER BY fault_id, tested_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list fault stability certs: %w", err)
	}
	defer rows.Close()
	return scanCerts(rows)
}

// GetHistory returns up to limit past cert snapshots for a fault+model,
// most recent first, from the append-only fault_stability_cert_history
// table — this is the trend fault_stability_cert alone can't answer, since
// that table only ever holds the latest snapshot. Every Upsert call appends
// exactly one history row (including the very first cert for a fault+model),
// so a single entry is a normal "just certified once" state, not an error;
// an empty slice means this fault+model has never been certified at all.
func (s *FaultStabilityStore) GetHistory(ctx context.Context, faultID, model string, limit int) ([]*FaultStabilityCert, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, `
SELECT fault_id, fault_name, playbook_series_id, model, diagnosis_model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at, primary_attribution, attribution_consistent, attribution_distribution, judge_spread, taxonomy_version, warning_count, is_clean, warning_distribution, playbook_version, playbook_updated_at, playbook_id
FROM fault_stability_cert_history WHERE fault_id = ? AND diagnosis_model = ? ORDER BY recorded_at DESC LIMIT ?`), faultID, model, limit)
	if err != nil {
		return nil, fmt.Errorf("get cert history: %w", err)
	}
	defer rows.Close()
	return scanCerts(rows)
}

type certScanner interface {
	Scan(dest ...any) error
}

func scanCert(s certScanner) (*FaultStabilityCert, error) {
	var (
		cert                 FaultStabilityCert
		stableInt            int
		attrConsistentInt    int
		attrDistJSON         string
		testedStr            string
		cleanInt             int
		warnDistJSON         string
		playbookUpdatedAtStr string
	)
	if err := s.Scan(
		&cert.FaultID, &cert.FaultName, &cert.PlaybookSeriesID, &cert.JudgeModel, &cert.DiagnosisModel,
		&cert.NRuns, &cert.PassRate, &cert.ConfRangePP, &stableInt, &testedStr,
		&cert.PrimaryAttribution, &attrConsistentInt, &attrDistJSON,
		&cert.JudgeSpread, &cert.TaxonomyVersion,
		&cert.WarningCount, &cleanInt, &warnDistJSON,
		&cert.PlaybookVersion, &playbookUpdatedAtStr, &cert.PlaybookID,
	); err != nil {
		return nil, err
	}
	cert.IsStable = stableInt != 0
	cert.AttributionConsistent = attrConsistentInt != 0
	cert.IsClean = cleanInt != 0
	if attrDistJSON != "" && attrDistJSON != "{}" {
		var dist map[string]int
		if err := json.Unmarshal([]byte(attrDistJSON), &dist); err == nil {
			cert.AttributionDistribution = dist
		}
	}
	if warnDistJSON != "" && warnDistJSON != "{}" {
		var dist map[string]int
		if err := json.Unmarshal([]byte(warnDistJSON), &dist); err == nil {
			cert.WarningDistribution = dist
		}
	}
	if testedStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, testedStr); err == nil {
			cert.TestedAt = t
		}
	}
	if playbookUpdatedAtStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, playbookUpdatedAtStr); err == nil {
			cert.PlaybookUpdatedAt = t
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
