package audit

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// EventTypeRestoreDrill is emitted once per recorded restore drill. The event
// is hash-covered (see ComputeEventHash) and carries the full DrillRecord.
const EventTypeRestoreDrill EventType = "restore_drill"

// Drill verdicts. A drill is counted (feeds the estimate, clears the overdue
// alert) only when it is verified or verified with warnings.
const (
	DrillVerified             = "verified"
	DrillVerifiedWithWarnings = "verified_with_warnings"
	DrillRefused              = "refused"      // a guard failed (not the same system, archive guard, ...)
	DrillUnverifiable         = "unverifiable" // the clone could not be reached
)

// Drill methods and duration sources.
const (
	DrillMethodPgBackRest = "pgbackrest"
	DrillMethodSnapshot   = "snapshot"
	DrillMethodCloud      = "cloud"
	DrillMethodOther      = "other"

	DrillDurationReported = "reported" // the operator said so
	DrillDurationObserved = "observed" // aiHelpDesk timed it
)

// DrillCheck is the outcome of one verification check on the clone.
type DrillCheck struct {
	Name   string `json:"name"`
	Result string `json:"result"` // pass | warn | fail
	Detail string `json:"detail,omitempty"`
}

// DrillRecord is one restore drill: a restore of the source's backup onto a
// clone the customer brought, with what aiHelpDesk verified about it.
//
// A record is a self-contained snapshot. The source/clone keys are only labels
// (infra.json can rename or repoint them later); the proof of the link is
// SourceSystemID == CloneSystemID, read from both servers at verification
// time. Rows are immutable. A re-verification is a new row whose Supersedes
// points at the earlier one.
type DrillRecord struct {
	DrillID string `json:"drill_id"`

	SourceKey      string `json:"source_key"`
	CloneKey       string `json:"clone_key"`
	SourceSystemID string `json:"source_system_id,omitempty"`
	CloneSystemID  string `json:"clone_system_id,omitempty"`
	// Endpoints are host:port, never credentials.
	SourceEndpoint string `json:"source_endpoint,omitempty"`
	CloneEndpoint  string `json:"clone_endpoint,omitempty"`
	SourceHostType string `json:"source_host_type,omitempty"`
	Stanza         string `json:"stanza,omitempty"`
	// InfraDigest is a digest of the two infra entries with secrets stripped,
	// so a later edit of infra.json is detectable.
	InfraDigest string `json:"infra_digest,omitempty"`

	Method          string    `json:"method"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	DurationSeconds int64     `json:"duration_seconds,omitempty"`
	DurationSource  string    `json:"duration_source,omitempty"`
	RestoreBytes    int64     `json:"restore_bytes,omitempty"`
	BackupLabel     string    `json:"backup_label,omitempty"`
	RecoveryTarget  string    `json:"recovery_target,omitempty"`

	Status     string       `json:"status"`
	Checks     []DrillCheck `json:"checks,omitempty"`
	Supersedes string       `json:"supersedes,omitempty"`

	RecordedBy string    `json:"recorded_by,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
	Ticket     string    `json:"ticket,omitempty"`
	Note       string    `json:"note,omitempty"` // the operator's words, recorded not verified
	// AuditEventID is the hash-covered restore_drill event written for this
	// record; the table is the query path, the event is the authority.
	AuditEventID string `json:"audit_event_id,omitempty"`
}

// Counted reports whether the drill is a verified one.
func (d DrillRecord) Counted() bool {
	return d.Status == DrillVerified || d.Status == DrillVerifiedWithWarnings
}

// DrillQuery filters ListDrills. Zero values do not filter.
type DrillQuery struct {
	SourceKey      string
	SourceSystemID string
	Status         string
	Method         string
	Since          time.Time // finished_at (recorded_at when unknown) at or after
	Limit          int       // default 100
}

// RestoreDrillStore persists DrillRecords. It shares the audit Store's
// connection.
type RestoreDrillStore struct {
	db         *rebindDB
	isPostgres bool
}

// NewRestoreDrillStore creates the restore_drills table (if absent).
func NewRestoreDrillStore(db *rebindDB, isPostgres bool) (*RestoreDrillStore, error) {
	s := &RestoreDrillStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create restore_drills schema: %w", err)
	}
	return s, nil
}

func (s *RestoreDrillStore) createSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS restore_drills (
    drill_id          TEXT PRIMARY KEY,
    source_key        TEXT NOT NULL,
    clone_key         TEXT NOT NULL,
    source_system_id  TEXT,
    clone_system_id   TEXT,
    source_endpoint   TEXT,
    clone_endpoint    TEXT,
    source_host_type  TEXT,
    stanza            TEXT,
    infra_digest      TEXT,
    method            TEXT NOT NULL,
    started_at        TEXT,
    finished_at       TEXT,
    duration_seconds  BIGINT NOT NULL DEFAULT 0,
    duration_source   TEXT,
    restore_bytes     BIGINT NOT NULL DEFAULT 0,
    backup_label      TEXT,
    recovery_target   TEXT,
    status            TEXT NOT NULL,
    checks_json       TEXT,
    supersedes        TEXT,
    recorded_by       TEXT,
    recorded_at       TEXT NOT NULL,
    ticket            TEXT,
    note              TEXT,
    audit_event_id    TEXT
)`,
		`CREATE INDEX IF NOT EXISTS idx_restore_drills_source_key ON restore_drills(source_key, finished_at)`,
		`CREATE INDEX IF NOT EXISTS idx_restore_drills_source_sys ON restore_drills(source_system_id, finished_at)`,
		`CREATE INDEX IF NOT EXISTS idx_restore_drills_status ON restore_drills(status)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// NewDrillID returns a fresh drill ID ("drl_" + 8 hex).
func NewDrillID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "drl_" + hex.EncodeToString(b)
}

func validDrillMethod(m string) bool {
	switch m {
	case DrillMethodPgBackRest, DrillMethodSnapshot, DrillMethodCloud, DrillMethodOther:
		return true
	}
	return false
}

func validDrillStatus(st string) bool {
	switch st {
	case DrillVerified, DrillVerifiedWithWarnings, DrillRefused, DrillUnverifiable:
		return true
	}
	return false
}

// Validate checks a record before it is stored. It does not need the database.
func (d DrillRecord) Validate() error {
	if strings.TrimSpace(d.SourceKey) == "" {
		return fmt.Errorf("source_key is required")
	}
	if strings.TrimSpace(d.CloneKey) == "" {
		return fmt.Errorf("clone_key is required")
	}
	if d.SourceKey == d.CloneKey {
		return fmt.Errorf("the clone cannot be the source (%q)", d.SourceKey)
	}
	if !validDrillMethod(d.Method) {
		return fmt.Errorf("method %q is not one of pgbackrest, snapshot, cloud, other", d.Method)
	}
	if !validDrillStatus(d.Status) {
		return fmt.Errorf("status %q is not one of verified, verified_with_warnings, refused, unverifiable", d.Status)
	}
	if d.DurationSeconds < 0 || d.RestoreBytes < 0 {
		return fmt.Errorf("duration_seconds and restore_bytes cannot be negative")
	}
	if d.DurationSource != "" && d.DurationSource != DrillDurationReported && d.DurationSource != DrillDurationObserved {
		return fmt.Errorf("duration_source %q is not reported or observed", d.DurationSource)
	}
	if !d.StartedAt.IsZero() && !d.FinishedAt.IsZero() && d.FinishedAt.Before(d.StartedAt) {
		return fmt.Errorf("finished_at is before started_at")
	}
	// A counted drill must prove the link: both identifiers read and equal.
	if d.Counted() {
		if d.SourceSystemID == "" || d.CloneSystemID == "" {
			return fmt.Errorf("a %s drill needs both system identifiers", d.Status)
		}
		if d.SourceSystemID != d.CloneSystemID {
			return fmt.Errorf("a %s drill needs the same system identifier on both servers", d.Status)
		}
	}
	return nil
}

func tsText(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// Save validates and inserts a drill record. An empty DrillID is generated and
// an empty RecordedAt is set to now. The stored record is returned. Saving an
// existing DrillID is an error: records are immutable.
func (s *RestoreDrillStore) Save(d DrillRecord) (DrillRecord, error) {
	if d.DrillID == "" {
		d.DrillID = NewDrillID()
	}
	if d.RecordedAt.IsZero() {
		d.RecordedAt = time.Now().UTC()
	}
	if err := d.Validate(); err != nil {
		return DrillRecord{}, err
	}
	if d.Supersedes != "" {
		prev, err := s.Get(d.Supersedes)
		if err != nil {
			return DrillRecord{}, fmt.Errorf("supersedes %s: %w", d.Supersedes, err)
		}
		if prev.SourceKey != d.SourceKey {
			return DrillRecord{}, fmt.Errorf("supersedes %s, which is a drill of %q, not %q", d.Supersedes, prev.SourceKey, d.SourceKey)
		}
	}
	checks, _ := json.Marshal(d.Checks)
	q := rebind(s.isPostgres, `INSERT INTO restore_drills
		(drill_id, source_key, clone_key, source_system_id, clone_system_id,
		 source_endpoint, clone_endpoint, source_host_type, stanza, infra_digest,
		 method, started_at, finished_at, duration_seconds, duration_source,
		 restore_bytes, backup_label, recovery_target, status, checks_json,
		 supersedes, recorded_by, recorded_at, ticket, note, audit_event_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := s.db.Exec(q,
		d.DrillID, d.SourceKey, d.CloneKey, d.SourceSystemID, d.CloneSystemID,
		d.SourceEndpoint, d.CloneEndpoint, d.SourceHostType, d.Stanza, d.InfraDigest,
		d.Method, tsText(d.StartedAt), tsText(d.FinishedAt), d.DurationSeconds, d.DurationSource,
		d.RestoreBytes, d.BackupLabel, d.RecoveryTarget, d.Status, string(checks),
		d.Supersedes, d.RecordedBy, d.RecordedAt.UTC().Format(time.RFC3339), d.Ticket, d.Note, d.AuditEventID,
	)
	if err != nil {
		return DrillRecord{}, fmt.Errorf("save drill: %w", err)
	}
	return d, nil
}

const drillCols = `drill_id, source_key, clone_key, source_system_id, clone_system_id,
	source_endpoint, clone_endpoint, source_host_type, stanza, infra_digest,
	method, started_at, finished_at, duration_seconds, duration_source,
	restore_bytes, backup_label, recovery_target, status, checks_json,
	supersedes, recorded_by, recorded_at, ticket, note, audit_event_id`

type drillScanner interface {
	Scan(dest ...any) error
}

func scanDrill(sc drillScanner) (DrillRecord, error) {
	var d DrillRecord
	var srcSys, cloneSys, srcEP, cloneEP, hostType, stanza, digest sql.NullString
	var started, finished, durSrc, label, target, checks, supersedes sql.NullString
	var by, ticket, note, evID sql.NullString
	var recorded string
	if err := sc.Scan(&d.DrillID, &d.SourceKey, &d.CloneKey, &srcSys, &cloneSys,
		&srcEP, &cloneEP, &hostType, &stanza, &digest,
		&d.Method, &started, &finished, &d.DurationSeconds, &durSrc,
		&d.RestoreBytes, &label, &target, &d.Status, &checks,
		&supersedes, &by, &recorded, &ticket, &note, &evID); err != nil {
		return DrillRecord{}, err
	}
	d.SourceSystemID, d.CloneSystemID = srcSys.String, cloneSys.String
	d.SourceEndpoint, d.CloneEndpoint = srcEP.String, cloneEP.String
	d.SourceHostType, d.Stanza, d.InfraDigest = hostType.String, stanza.String, digest.String
	d.StartedAt, _ = time.Parse(time.RFC3339, started.String)
	d.FinishedAt, _ = time.Parse(time.RFC3339, finished.String)
	d.DurationSource, d.BackupLabel, d.RecoveryTarget = durSrc.String, label.String, target.String
	d.Supersedes, d.RecordedBy = supersedes.String, by.String
	d.Ticket, d.Note, d.AuditEventID = ticket.String, note.String, evID.String
	d.RecordedAt, _ = time.Parse(time.RFC3339, recorded)
	if checks.String != "" {
		_ = json.Unmarshal([]byte(checks.String), &d.Checks)
	}
	return d, nil
}

// ErrDrillNotFound is returned by Get for an unknown drill ID.
var ErrDrillNotFound = fmt.Errorf("drill not found")

// Get returns one drill by ID.
func (s *RestoreDrillStore) Get(drillID string) (DrillRecord, error) {
	row := s.db.QueryRow(rebind(s.isPostgres, `SELECT `+drillCols+` FROM restore_drills WHERE drill_id = ?`), drillID)
	d, err := scanDrill(row)
	if err == sql.ErrNoRows {
		return DrillRecord{}, ErrDrillNotFound
	}
	return d, err
}

// List returns drills matching the query, newest first (by finished_at, then
// recorded_at). Superseded drills are included; callers that want only the
// current verdict use Current.
func (s *RestoreDrillStore) List(q DrillQuery) ([]DrillRecord, error) {
	var where []string
	var args []any
	if q.SourceKey != "" {
		where = append(where, "source_key = ?")
		args = append(args, q.SourceKey)
	}
	if q.SourceSystemID != "" {
		where = append(where, "source_system_id = ?")
		args = append(args, q.SourceSystemID)
	}
	if q.Status != "" {
		where = append(where, "status = ?")
		args = append(args, q.Status)
	}
	if q.Method != "" {
		where = append(where, "method = ?")
		args = append(args, q.Method)
	}
	if !q.Since.IsZero() {
		where = append(where, "COALESCE(finished_at, recorded_at) >= ?")
		args = append(args, q.Since.UTC().Format(time.RFC3339))
	}
	query := `SELECT ` + drillCols + ` FROM restore_drills`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	query += " ORDER BY COALESCE(finished_at, recorded_at) DESC, recorded_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(rebind(s.isPostgres, query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DrillRecord
	for rows.Next() {
		d, err := scanDrill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Current drops every drill that a later record supersedes.
func Current(drills []DrillRecord) []DrillRecord {
	gone := map[string]bool{}
	for _, d := range drills {
		if d.Supersedes != "" {
			gone[d.Supersedes] = true
		}
	}
	var out []DrillRecord
	for _, d := range drills {
		if !gone[d.DrillID] {
			out = append(out, d)
		}
	}
	return out
}
