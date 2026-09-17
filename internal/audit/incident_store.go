package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Incident origin/status constants.
const (
	IncidentOriginReal      = "real"      // triggered by a real production signal (human, srebot, alerting webhook)
	IncidentOriginFaulttest = "faulttest" // triggered by a controlled faulttest injection run

	IncidentStatusOpen      = "open" // entry-point hop has started, not yet terminal
	IncidentStatusResolved  = "resolved"
	IncidentStatusEscalated = "escalated"
	IncidentStatusAbandoned = "abandoned"
)

// Incident records one user-facing incident — one row per real-world event or
// faulttest-injected run, spanning however many playbook_runs hops it takes to
// resolve. Distinct from PlaybookRun (per-hop execution detail): Incident sits
// one level above it. TraceID/EntryRunID are the join keys back down into the
// existing hop-level detail (playbook_runs, audit_events) — this table is
// deliberately not a replacement for either, only the missing layer above them.
// See docs/INCIDENTS.md and the v0.29 incident-entity design this table
// implements Phase 1 of.
type Incident struct {
	IncidentID            string    `json:"incident_id"`
	TraceID               string    `json:"trace_id,omitempty"`
	EntryRunID            string    `json:"entry_run_id,omitempty"`
	SeriesID              string    `json:"series_id,omitempty"`               // entry playbook's SeriesID, recorded at creation time
	Origin                string    `json:"origin"`                            // "real" | "faulttest"
	Severity              string    `json:"severity,omitempty"`                // operator-settable, optional
	Status                string    `json:"status"`                            // open | resolved | escalated | abandoned
	Attribution           string    `json:"attribution,omitempty"`             // root-cause class; same classifier faulttest already uses
	ExternalCorrelationID string    `json:"external_correlation_id,omitempty"` // PagerDuty/Alertmanager id, optional
	BundlePath            string    `json:"bundle_path,omitempty"`             // tarball path once create_incident_bundle records one
	DraftPlaybookID       string    `json:"draft_playbook_id,omitempty"`       // Vault playbook_id synthesized via /from-trace for this incident, if any
	DetectedAt            time.Time `json:"detected_at"`
	ResolvedAt            time.Time `json:"resolved_at,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// IncidentUpdate carries the mutable subset of Incident fields for a partial
// (PATCH-style) update — every field is a pointer so a caller can update just
// one or two without clobbering the rest. Mirrors this package's existing
// PATCH-shaped update patterns (see playbook_run_handlers.go's use of
// PlaybookRunStore.Update) rather than requiring a full Incident round-trip
// for e.g. attaching a bundle_path after the fact.
type IncidentUpdate struct {
	Status                *string
	Attribution           *string
	Severity              *string
	ExternalCorrelationID *string
	BundlePath            *string
	DraftPlaybookID       *string
	ResolvedAt            *time.Time
	// TraceID backfills the trace_id column when it was empty at creation
	// time — genuine for execution_mode="agent" entry playbooks, where the
	// real trace_id isn't minted until the agent's first response comes back
	// (see handlePlaybookRunAsAgent's backfillIncidentTraceID). Never
	// overwrites a non-empty existing value; callers only set this when they
	// already confirmed the row's own TraceID was "".
	TraceID *string
}

// IncidentStore persists Incident rows.
type IncidentStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewIncidentStore creates the incidents table (if absent) and returns a
// ready-to-use IncidentStore.
func NewIncidentStore(db *sql.DB, isPostgres bool) (*IncidentStore, error) {
	s := &IncidentStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create incidents schema: %w", err)
	}
	return s, nil
}

func (s *IncidentStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS incidents (
    incident_id              TEXT     NOT NULL PRIMARY KEY,
    trace_id                 TEXT     NOT NULL DEFAULT '',
    entry_run_id              TEXT     NOT NULL DEFAULT '',
    origin                    TEXT     NOT NULL DEFAULT 'real',
    severity                  TEXT     NOT NULL DEFAULT '',
    status                    TEXT     NOT NULL DEFAULT 'open',
    attribution               TEXT     NOT NULL DEFAULT '',
    external_correlation_id   TEXT     NOT NULL DEFAULT '',
    bundle_path               TEXT     NOT NULL DEFAULT '',
    detected_at               DATETIME NOT NULL,
    resolved_at               DATETIME NOT NULL DEFAULT '',
    created_at                DATETIME NOT NULL,
    updated_at                DATETIME NOT NULL
)`)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_incidents_trace ON incidents(trace_id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_incidents_origin_status ON incidents(origin, status)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_incidents_attribution ON incidents(attribution)`); err != nil {
		return err
	}
	return s.migrate()
}

// migrate applies additive schema changes to existing databases, following
// this package's standing convention (see PlaybookRunStore.migrate).
func (s *IncidentStore) migrate() error {
	for _, col := range []struct {
		name string
		ddl  string
	}{
		// Phase 2 (v0.29): recorded at incident-creation time from the entry
		// playbook's own SeriesID, so attribution classification at resolution
		// time can re-fetch that series' current root_cause_classes without an
		// extra playbook_runs round-trip just to relearn which series this was.
		{"series_id", `ALTER TABLE incidents ADD COLUMN series_id TEXT NOT NULL DEFAULT ''`},
		// v0.29 follow-up: links an incident to the Vault draft synthesized
		// from it (if any), so a listing can show draft status (pending
		// review vs. activated) without a separate join at query time —
		// populated by whichever /from-trace call succeeds for this
		// incident's trace_id (see handlePlaybookFromTrace).
		{"draft_playbook_id", `ALTER TABLE incidents ADD COLUMN draft_playbook_id TEXT NOT NULL DEFAULT ''`},
	} {
		if _, err := s.db.Exec(col.ddl); err != nil {
			// SQLite says "duplicate column name: X"; Postgres says
			// "column X of relation ... already exists". Accept both.
			if !containsAny(err.Error(), "duplicate column", "already exists") {
				return fmt.Errorf("migrate incidents.%s: %w", col.name, err)
			}
		}
	}
	return nil
}

// Create inserts a new Incident. IncidentID is generated if empty. Origin
// defaults to IncidentOriginReal if empty — callers that know they're
// faulttest-driven (Phase 4 of the v0.29 design) must set it explicitly.
func (s *IncidentStore) Create(ctx context.Context, inc *Incident) error {
	if inc.IncidentID == "" {
		inc.IncidentID = "inc_" + uuid.New().String()[:8]
	}
	if inc.Origin == "" {
		inc.Origin = IncidentOriginReal
	}
	if inc.Status == "" {
		inc.Status = IncidentStatusOpen
	}
	if inc.DetectedAt.IsZero() {
		inc.DetectedAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	inc.CreatedAt = now
	inc.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		INSERT INTO incidents
		    (incident_id, trace_id, entry_run_id, series_id, origin, severity, status,
		     attribution, external_correlation_id, bundle_path, draft_playbook_id,
		     detected_at, resolved_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		inc.IncidentID, inc.TraceID, inc.EntryRunID, inc.SeriesID, inc.Origin, inc.Severity, inc.Status,
		inc.Attribution, inc.ExternalCorrelationID, inc.BundlePath, inc.DraftPlaybookID,
		inc.DetectedAt.Format("2006-01-02 15:04:05"),
		formatNullableTime(inc.ResolvedAt),
		inc.CreatedAt.Format("2006-01-02 15:04:05"),
		inc.UpdatedAt.Format("2006-01-02 15:04:05"),
	)
	return err
}

// Update applies a partial patch to an existing incident, touching only the
// non-nil fields in u. Always bumps updated_at. Returns sql.ErrNoRows if
// incidentID doesn't exist.
func (s *IncidentStore) Update(ctx context.Context, incidentID string, u IncidentUpdate) error {
	sets := []string{"updated_at = ?"}
	args := []any{time.Now().UTC().Format("2006-01-02 15:04:05")}

	if u.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, *u.Status)
	}
	if u.Attribution != nil {
		sets = append(sets, "attribution = ?")
		args = append(args, *u.Attribution)
	}
	if u.Severity != nil {
		sets = append(sets, "severity = ?")
		args = append(args, *u.Severity)
	}
	if u.ExternalCorrelationID != nil {
		sets = append(sets, "external_correlation_id = ?")
		args = append(args, *u.ExternalCorrelationID)
	}
	if u.BundlePath != nil {
		sets = append(sets, "bundle_path = ?")
		args = append(args, *u.BundlePath)
	}
	if u.DraftPlaybookID != nil {
		sets = append(sets, "draft_playbook_id = ?")
		args = append(args, *u.DraftPlaybookID)
	}
	if u.ResolvedAt != nil {
		sets = append(sets, "resolved_at = ?")
		args = append(args, formatNullableTime(*u.ResolvedAt))
	}
	if u.TraceID != nil {
		sets = append(sets, "trace_id = ?")
		args = append(args, *u.TraceID)
	}

	query := "UPDATE incidents SET "
	for i, set := range sets {
		if i > 0 {
			query += ", "
		}
		query += set
	}
	query += " WHERE incident_id = ?"
	args = append(args, incidentID)

	res, err := s.db.ExecContext(ctx, rebind(s.isPostgres, query), args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		// Some drivers don't support RowsAffected reliably; not worth failing
		// the update over — matches this package's general tolerance for
		// best-effort row-count checks elsewhere.
		return nil
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetByID returns a single Incident by its incident_id. Returns sql.ErrNoRows
// if not found.
func (s *IncidentStore) GetByID(ctx context.Context, incidentID string) (*Incident, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
		SELECT incident_id, trace_id, entry_run_id, series_id, origin, severity, status,
		       attribution, external_correlation_id, bundle_path, draft_playbook_id,
		       detected_at, resolved_at, created_at, updated_at
		FROM incidents
		WHERE incident_id = ?`), incidentID)
	return scanIncident(row)
}

// GetByEntryRunID returns the Incident whose entry_run_id matches the given
// run_id, if any. Returns sql.ErrNoRows if not found — most callers should
// treat that as "no incident row for this run" (e.g. a chained hop, or a run
// that predates this table) rather than an error.
func (s *IncidentStore) GetByEntryRunID(ctx context.Context, runID string) (*Incident, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
		SELECT incident_id, trace_id, entry_run_id, series_id, origin, severity, status,
		       attribution, external_correlation_id, bundle_path, draft_playbook_id,
		       detected_at, resolved_at, created_at, updated_at
		FROM incidents
		WHERE entry_run_id = ?`), runID)
	return scanIncident(row)
}

// GetByTraceID returns the Incident whose trace_id matches the given trace,
// if any. Returns sql.ErrNoRows if not found. Needed because /from-trace
// (the draft-synthesis endpoint) is keyed by trace_id, not run_id — both of
// its callers (faulttest's own direct call, and create_incident_bundle's)
// only ever have the trace_id in hand, not the entry run_id.
func (s *IncidentStore) GetByTraceID(ctx context.Context, traceID string) (*Incident, error) {
	row := s.db.QueryRowContext(ctx, rebind(s.isPostgres, `
		SELECT incident_id, trace_id, entry_run_id, series_id, origin, severity, status,
		       attribution, external_correlation_id, bundle_path, draft_playbook_id,
		       detected_at, resolved_at, created_at, updated_at
		FROM incidents
		WHERE trace_id = ?`), traceID)
	return scanIncident(row)
}

// IncidentListFilter narrows List's results. Zero-value fields are ignored
// (no filter applied for that field).
type IncidentListFilter struct {
	Origin      string
	Status      string
	Attribution string
	Limit       int
}

// List returns incidents matching filter, most recently detected first.
func (s *IncidentStore) List(ctx context.Context, filter IncidentListFilter) ([]*Incident, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `
		SELECT incident_id, trace_id, entry_run_id, series_id, origin, severity, status,
		       attribution, external_correlation_id, bundle_path, draft_playbook_id,
		       detected_at, resolved_at, created_at, updated_at
		FROM incidents
		WHERE 1=1`
	var args []any
	if filter.Origin != "" {
		query += " AND origin = ?"
		args = append(args, filter.Origin)
	}
	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
	}
	if filter.Attribution != "" {
		query += " AND attribution = ?"
		args = append(args, filter.Attribution)
	}
	query += fmt.Sprintf(" ORDER BY detected_at DESC LIMIT %d", limit)

	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var incidents []*Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		incidents = append(incidents, inc)
	}
	return incidents, rows.Err()
}

type incidentScanner interface {
	Scan(dest ...any) error
}

func scanIncident(s incidentScanner) (*Incident, error) {
	var inc Incident
	var detectedStr, resolvedStr, createdStr, updatedStr string
	if err := s.Scan(
		&inc.IncidentID, &inc.TraceID, &inc.EntryRunID, &inc.SeriesID, &inc.Origin, &inc.Severity, &inc.Status,
		&inc.Attribution, &inc.ExternalCorrelationID, &inc.BundlePath, &inc.DraftPlaybookID,
		&detectedStr, &resolvedStr, &createdStr, &updatedStr,
	); err != nil {
		return nil, err
	}
	inc.DetectedAt = parseFlexTime(detectedStr)
	inc.ResolvedAt = parseFlexTime(resolvedStr)
	inc.CreatedAt = parseFlexTime(createdStr)
	inc.UpdatedAt = parseFlexTime(updatedStr)
	return &inc, nil
}
