package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// FaultStabilityCert records the outcome of a --repeat N faulttest run for one
// fault. It captures whether the triage agent produced consistent results across
// N independent inject→triage→teardown cycles. Keyed by (fault_id): the latest
// cert overwrites the previous one, so re-running --repeat always refreshes the
// badge. TestedAt lets callers surface staleness.
type FaultStabilityCert struct {
	FaultID          string    `json:"fault_id"`
	FaultName        string    `json:"fault_name"`
	PlaybookSeriesID string    `json:"playbook_series_id,omitempty"`
	Model            string    `json:"model,omitempty"` // judge/triage model annotation; empty when unknown
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
	return s, nil
}

func (s *FaultStabilityStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS fault_stability_cert (
    fault_id           TEXT    NOT NULL PRIMARY KEY,
    fault_name         TEXT    NOT NULL DEFAULT '',
    playbook_series_id TEXT    NOT NULL DEFAULT '',
    model              TEXT    NOT NULL DEFAULT '',
    n_runs             INTEGER NOT NULL DEFAULT 0,
    pass_rate          REAL    NOT NULL DEFAULT 0,
    conf_range_pp      INTEGER NOT NULL DEFAULT 0,
    is_stable          INTEGER NOT NULL DEFAULT 0,
    tested_at          TEXT    NOT NULL DEFAULT ''
)`)
	return err
}

// Upsert writes a stability cert, overwriting any previous entry for the same fault_id.
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
    (fault_id, fault_name, playbook_series_id, model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(fault_id) DO UPDATE SET
    fault_name         = excluded.fault_name,
    playbook_series_id = excluded.playbook_series_id,
    model              = excluded.model,
    n_runs             = excluded.n_runs,
    pass_rate          = excluded.pass_rate,
    conf_range_pp      = excluded.conf_range_pp,
    is_stable          = excluded.is_stable,
    tested_at          = excluded.tested_at`),
		cert.FaultID, cert.FaultName, cert.PlaybookSeriesID, cert.Model,
		cert.NRuns, cert.PassRate, cert.ConfRangePP,
		stableInt, cert.TestedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

// GetByFaultID returns the latest stability cert for the given fault.
// Returns sql.ErrNoRows when none has been recorded.
func (s *FaultStabilityStore) GetByFaultID(ctx context.Context, faultID string) (*FaultStabilityCert, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
SELECT fault_id, fault_name, playbook_series_id, model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at
FROM fault_stability_cert WHERE fault_id = ?`), faultID)
	return scanCert(row)
}

// ListAll returns all stability certs, ordered by fault_id.
func (s *FaultStabilityStore) ListAll(ctx context.Context) ([]*FaultStabilityCert, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT fault_id, fault_name, playbook_series_id, model, n_runs, pass_rate, conf_range_pp, is_stable, tested_at
FROM fault_stability_cert ORDER BY fault_id`)
	if err != nil {
		return nil, fmt.Errorf("list fault stability certs: %w", err)
	}
	defer rows.Close()

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
		&cert.FaultID, &cert.FaultName, &cert.PlaybookSeriesID, &cert.Model,
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
