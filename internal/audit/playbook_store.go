package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrSystemPlaybook is returned when a mutating operation is attempted on a
// system-managed playbook (IsSystem=true).
var ErrSystemPlaybook = errors.New("system playbooks are read-only")

// Playbook is a saved NL intent: a named description + optional target hints
// that can be run on demand to generate a fresh fleet job plan.
type Playbook struct {
	// Core fields (always present)
	PlaybookID  string    `json:"playbook_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"` // fleet intent — passed verbatim to planner
	TargetHints []string  `json:"target_hints,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Knowledge fields (optional, added in v0.8)
	ProblemClass     string     `json:"problem_class,omitempty"`     // performance|availability|capacity|data_integrity|security
	Symptoms         []string   `json:"symptoms,omitempty"`          // observable indicators that trigger this playbook
	Guidance         string     `json:"guidance,omitempty"`          // expert reasoning injected into planner prompt
	Escalation       []string   `json:"escalation,omitempty"`        // conditions under which the LLM must stop and escalate
	RelatedPlaybooks []string   `json:"related_playbooks,omitempty"` // pb_* IDs of related playbooks
	Author           string     `json:"author,omitempty"`
	LastValidated    *time.Time `json:"last_validated,omitempty"`
	Version          string     `json:"version,omitempty"`

	// Versioning fields (added in Phase 2)
	SeriesID    string `json:"series_id,omitempty"`    // "pbs_" + uuid[:8]; groups all versions of a playbook concept; stable across renames
	IsActive    bool   `json:"is_active"`              // exactly one version per series should be active
	IsSystem    bool   `json:"is_system"`              // true = shipped with aiHelpDesk; read-only via API
	Source      string `json:"source"`                 // "system" | "imported" | "manual"
	OriginTrace string `json:"origin_trace,omitempty"` // audit trace or playbook run ID that generated this version
	PlaybookType string `json:"playbook_type,omitempty"` // "triage" | "remediation" | "" (unset = no protocol validation)

	// Triage routing fields
	EntryPoint  bool     `json:"entry_point"` // true = preferred starting point for this problem_class
	EscalatesTo []string `json:"escalates_to,omitempty"`
	// EscalatesTo: allow-list for ESCALATE_TO directives — cross-domain handoffs
	// to a different agent / problem class. Validated server-side at emission
	// time; targets outside this list are coerced to requires_operator_approval.
	TransitionsTo []string `json:"transitions_to,omitempty"`
	// TransitionsTo: allow-list for TRANSITION_TO directives — same-domain
	// follow-ons (typically triage → remediation under the same agent).
	// Validated server-side the same way as EscalatesTo.
	RequiresEvidence []string `json:"requires_evidence,omitempty"` // log/error patterns expected before selecting this playbook
	ExecutionMode    string   `json:"execution_mode"`             // "fleet" | "agent" (R/O) | "agent_approve" (mutations+approval) | "agent_auto" (pre-approved mutations)
	PermittedTools   []string `json:"permitted_tools,omitempty"`  // agent_auto: tools allowed to execute without per-step approval
	ApprovalMode     string   `json:"approval_mode,omitempty"`    // "auto"|"session"|"manual"; playbook-level default (overridden per run)
	AgentName        string   `json:"agent_name,omitempty"`       // A2A agent to invoke; defaults to postgres_database_agent

	// Judge verdict (recorded when vault diff --judge is run against a draft).
	// Persisted on the playbook record so vault versions can show whether the
	// judge's prediction matched the observed improvement after activation.
	JudgeVerdict string    `json:"judge_verdict,omitempty"` // "APPROVE" | "NEEDS_REVIEW" | "REJECT"
	JudgeModel   string    `json:"judge_model,omitempty"`   // model that issued the verdict
	JudgeAt      time.Time `json:"judge_at,omitempty"`      // when the verdict was recorded

	// Attribution classification taxonomy (v0.21.0). Triage playbooks carry a
	// closed enum of plausible root-cause labels. faulttest uses these at cert
	// time to classify each eval run's FINDINGS text, enabling conclusion-stability
	// measurement independent of the pass/fail outcome score.
	RootCauseClasses *RootCauseClassification `json:"root_cause_classes,omitempty"`

	// Computed fields (not persisted; populated on demand)
	Stats *PlaybookRunStats `json:"stats,omitempty"` // run statistics, injected by handleList
}

// RootCauseClassification is a versioned closed enum of plausible root-cause
// attribution labels for a triage playbook. Semver-versioned: minor bumps
// (new class added) are backwards-comparable; major bumps (split/merge/rename)
// invalidate attribution comparisons across the version boundary.
type RootCauseClassification struct {
	Version string   `json:"version"`
	Classes []string `json:"classes"`
}

// PlaybookStore persists fleet playbooks.
// It shares the same *sql.DB connection as the other audit stores.
type PlaybookStore struct {
	db         *sql.DB
	isPostgres bool
}

// NewPlaybookStore creates the playbooks table (if absent), runs any pending
// migrations, and returns a ready-to-use PlaybookStore.
func NewPlaybookStore(db *sql.DB, isPostgres bool) (*PlaybookStore, error) {
	s := &PlaybookStore{db: db, isPostgres: isPostgres}
	if err := s.createSchema(); err != nil {
		return nil, fmt.Errorf("create playbook schema: %w", err)
	}
	if err := s.migrateSchema(); err != nil {
		return nil, fmt.Errorf("migrate playbook schema: %w", err)
	}
	return s, nil
}

func (s *PlaybookStore) createSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS playbooks (
    playbook_id  TEXT        NOT NULL PRIMARY KEY,
    name         TEXT        NOT NULL,
    description  TEXT        NOT NULL,
    target_hints TEXT        NOT NULL DEFAULT '[]',
    created_by   TEXT        NOT NULL DEFAULT '',
    created_at   DATETIME    NOT NULL,
    updated_at   DATETIME    NOT NULL
)`)
	return err
}

// migrateSchema adds columns introduced after the initial schema.
// Each ALTER is idempotent: duplicate-column errors are silently ignored.
func (s *PlaybookStore) migrateSchema() error {
	newCols := []string{
		"ALTER TABLE playbooks ADD COLUMN problem_class  TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN symptoms       TEXT    NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN guidance       TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN escalation     TEXT    NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN related_playbooks TEXT  NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN author         TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN last_validated TEXT",
		"ALTER TABLE playbooks ADD COLUMN version        TEXT    NOT NULL DEFAULT ''",
		// Phase 2: versioning
		"ALTER TABLE playbooks ADD COLUMN series_id      TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN is_active      INTEGER NOT NULL DEFAULT 1",
		"ALTER TABLE playbooks ADD COLUMN is_system      INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE playbooks ADD COLUMN source         TEXT    NOT NULL DEFAULT 'manual'",
		// Triage routing
		"ALTER TABLE playbooks ADD COLUMN entry_point        INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE playbooks ADD COLUMN escalates_to       TEXT    NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN requires_evidence  TEXT    NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN execution_mode     TEXT    NOT NULL DEFAULT 'fleet'",
		"ALTER TABLE playbooks ADD COLUMN permitted_tools    TEXT    NOT NULL DEFAULT '[]'",
		// Chaining and agent routing
		"ALTER TABLE playbooks ADD COLUMN approval_mode      TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN agent_name         TEXT    NOT NULL DEFAULT ''",
		// Directive split (v0.16): same-domain follow-ons live in transitions_to,
		// cross-domain handoffs remain in escalates_to.
		"ALTER TABLE playbooks ADD COLUMN transitions_to     TEXT    NOT NULL DEFAULT '[]'",
		"ALTER TABLE playbooks ADD COLUMN origin_trace       TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN playbook_type      TEXT    NOT NULL DEFAULT ''",
		// Judge verdict (v0.20): recorded when vault diff --judge is run against a draft.
		"ALTER TABLE playbooks ADD COLUMN judge_verdict      TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN judge_model        TEXT    NOT NULL DEFAULT ''",
		"ALTER TABLE playbooks ADD COLUMN judge_at           TEXT    NOT NULL DEFAULT ''",
		// Attribution taxonomy (v0.21): closed enum for conclusion-stability measurement.
		"ALTER TABLE playbooks ADD COLUMN root_cause_classes TEXT    NOT NULL DEFAULT '{}'",
	}
	for _, stmt := range newCols {
		if _, err := s.db.Exec(stmt); err != nil {
			// Ignore "duplicate column" errors from SQLite and Postgres
			msg := err.Error()
			if containsAny(msg, "duplicate column", "already exists") {
				continue
			}
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

func containsAny(s string, subs ...string) bool {
	lower := s
	for _, sub := range subs {
		if len(sub) <= len(lower) {
			for i := 0; i <= len(lower)-len(sub); i++ {
				if lower[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// Create inserts a new playbook. PlaybookID and SeriesID are generated if empty.
// IsActive defaults to true; Source defaults to "manual".
func (s *PlaybookStore) Create(ctx context.Context, pb *Playbook) error {
	if pb.PlaybookID == "" {
		pb.PlaybookID = "pb_" + uuid.New().String()[:8]
	}
	seriesWasEmpty := pb.SeriesID == ""
	if seriesWasEmpty {
		pb.SeriesID = "pbs_" + uuid.New().String()[:8]
	}
	if pb.Source == "" {
		pb.Source = "manual"
	}
	if pb.ExecutionMode == "" {
		pb.ExecutionMode = "fleet"
	}
	// For brand-new series (caller didn't provide SeriesID), default IsActive=true
	// so system/manual playbooks are ready immediately.
	// Exception: imported and generated drafts always start inactive regardless of
	// whether the series is new — they require explicit activation via vault activate.
	if seriesWasEmpty && pb.Source != "imported" && pb.Source != "generated" {
		pb.IsActive = true
	}

	now := time.Now().UTC()
	pb.CreatedAt = now
	pb.UpdatedAt = now

	hintsJSON, err := json.Marshal(pb.TargetHints)
	if err != nil {
		return fmt.Errorf("marshal target_hints: %w", err)
	}
	symptomsJSON, err := json.Marshal(pb.Symptoms)
	if err != nil {
		return fmt.Errorf("marshal symptoms: %w", err)
	}
	escalationJSON, err := json.Marshal(pb.Escalation)
	if err != nil {
		return fmt.Errorf("marshal escalation: %w", err)
	}
	relatedJSON, err := json.Marshal(pb.RelatedPlaybooks)
	if err != nil {
		return fmt.Errorf("marshal related_playbooks: %w", err)
	}
	escalatesToJSON, err := json.Marshal(pb.EscalatesTo)
	if err != nil {
		return fmt.Errorf("marshal escalates_to: %w", err)
	}
	transitionsToJSON, err := json.Marshal(pb.TransitionsTo)
	if err != nil {
		return fmt.Errorf("marshal transitions_to: %w", err)
	}
	requiresEvidenceJSON, err := json.Marshal(pb.RequiresEvidence)
	if err != nil {
		return fmt.Errorf("marshal requires_evidence: %w", err)
	}

	permittedToolsJSON, err := json.Marshal(pb.PermittedTools)
	if err != nil {
		return fmt.Errorf("marshal permitted_tools: %w", err)
	}

	rootCauseClassesJSON := []byte("{}")
	if pb.RootCauseClasses != nil {
		rootCauseClassesJSON, err = json.Marshal(pb.RootCauseClasses)
		if err != nil {
			return fmt.Errorf("marshal root_cause_classes: %w", err)
		}
	}

	var lastValidatedStr *string
	if pb.LastValidated != nil {
		s := pb.LastValidated.UTC().Format(time.RFC3339Nano)
		lastValidatedStr = &s
	}

	isActiveInt := 0
	if pb.IsActive {
		isActiveInt = 1
	}
	isSystemInt := 0
	if pb.IsSystem {
		isSystemInt = 1
	}
	entryPointInt := 0
	if pb.EntryPoint {
		entryPointInt = 1
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO playbooks
		    (playbook_id, name, description, target_hints, created_by, created_at, updated_at,
		     problem_class, symptoms, guidance, escalation, related_playbooks, author, last_validated, version,
		     series_id, is_active, is_system, source,
		     entry_point, escalates_to, requires_evidence, execution_mode, permitted_tools,
		     approval_mode, agent_name, transitions_to, origin_trace, playbook_type,
		     judge_verdict, judge_model, judge_at, root_cause_classes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pb.PlaybookID, pb.Name, pb.Description, string(hintsJSON), pb.CreatedBy, pb.CreatedAt, pb.UpdatedAt,
		pb.ProblemClass, string(symptomsJSON), pb.Guidance, string(escalationJSON), string(relatedJSON),
		pb.Author, lastValidatedStr, pb.Version,
		pb.SeriesID, isActiveInt, isSystemInt, pb.Source,
		entryPointInt, string(escalatesToJSON), string(requiresEvidenceJSON), pb.ExecutionMode, string(permittedToolsJSON),
		pb.ApprovalMode, pb.AgentName, string(transitionsToJSON), pb.OriginTrace, pb.PlaybookType,
		pb.JudgeVerdict, pb.JudgeModel, formatNullableTime(pb.JudgeAt), string(rootCauseClassesJSON),
	)
	return err
}

// Update replaces the mutable fields of an existing playbook.
// PlaybookID, CreatedBy, CreatedAt, IsActive, IsSystem, and Source are not modified.
// Returns ErrSystemPlaybook if the playbook is system-managed.
func (s *PlaybookStore) Update(ctx context.Context, pb *Playbook) error {
	// Fetch-first to check is_system before mutating.
	var isSystem int
	err := s.db.QueryRowContext(ctx,
		`SELECT is_system FROM playbooks WHERE playbook_id = ?`, pb.PlaybookID).Scan(&isSystem)
	if err == sql.ErrNoRows {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	if isSystem != 0 {
		return ErrSystemPlaybook
	}

	pb.UpdatedAt = time.Now().UTC()

	hintsJSON, err := json.Marshal(pb.TargetHints)
	if err != nil {
		return fmt.Errorf("marshal target_hints: %w", err)
	}
	symptomsJSON, err := json.Marshal(pb.Symptoms)
	if err != nil {
		return fmt.Errorf("marshal symptoms: %w", err)
	}
	escalationJSON, err := json.Marshal(pb.Escalation)
	if err != nil {
		return fmt.Errorf("marshal escalation: %w", err)
	}
	relatedJSON, err := json.Marshal(pb.RelatedPlaybooks)
	if err != nil {
		return fmt.Errorf("marshal related_playbooks: %w", err)
	}
	escalatesToJSON, err := json.Marshal(pb.EscalatesTo)
	if err != nil {
		return fmt.Errorf("marshal escalates_to: %w", err)
	}
	transitionsToJSON, err := json.Marshal(pb.TransitionsTo)
	if err != nil {
		return fmt.Errorf("marshal transitions_to: %w", err)
	}
	requiresEvidenceJSON, err := json.Marshal(pb.RequiresEvidence)
	if err != nil {
		return fmt.Errorf("marshal requires_evidence: %w", err)
	}

	permittedToolsJSON, err := json.Marshal(pb.PermittedTools)
	if err != nil {
		return fmt.Errorf("marshal permitted_tools: %w", err)
	}

	var lastValidatedStr *string
	if pb.LastValidated != nil {
		s := pb.LastValidated.UTC().Format(time.RFC3339Nano)
		lastValidatedStr = &s
	}

	entryPointInt := 0
	if pb.EntryPoint {
		entryPointInt = 1
	}
	executionMode := pb.ExecutionMode
	if executionMode == "" {
		executionMode = "fleet"
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE playbooks SET
		    name=?, description=?, target_hints=?, updated_at=?,
		    problem_class=?, symptoms=?, guidance=?, escalation=?,
		    related_playbooks=?, author=?, last_validated=?, version=?,
		    series_id=?,
		    entry_point=?, escalates_to=?, requires_evidence=?, execution_mode=?, permitted_tools=?,
		    approval_mode=?, agent_name=?, transitions_to=?, playbook_type=?
		 WHERE playbook_id=?`,
		pb.Name, pb.Description, string(hintsJSON), pb.UpdatedAt,
		pb.ProblemClass, string(symptomsJSON), pb.Guidance, string(escalationJSON),
		string(relatedJSON), pb.Author, lastValidatedStr, pb.Version,
		pb.SeriesID,
		entryPointInt, string(escalatesToJSON), string(requiresEvidenceJSON), executionMode, string(permittedToolsJSON),
		pb.ApprovalMode, pb.AgentName, string(transitionsToJSON), pb.PlaybookType,
		pb.PlaybookID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Delete removes a playbook by ID. Returns ErrSystemPlaybook if the playbook is
// system-managed. Returns nil (not an error) if the playbook did not exist.
func (s *PlaybookStore) Delete(ctx context.Context, id string) error {
	var isSystem int
	err := s.db.QueryRowContext(ctx,
		`SELECT is_system FROM playbooks WHERE playbook_id = ?`, id).Scan(&isSystem)
	if err == sql.ErrNoRows {
		return nil // already gone
	}
	if err != nil {
		return err
	}
	if isSystem != 0 {
		return ErrSystemPlaybook
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM playbooks WHERE playbook_id = ?`, id)
	return err
}

// SetJudgeVerdict records the LLM judge verdict on a draft playbook. Called when
// vault diff --judge is run; stores the verdict so vault versions can later
// correlate whether the judge's prediction matched the observed improvement.
func (s *PlaybookStore) SetJudgeVerdict(ctx context.Context, playbookID, verdict, judgeModel string) error {
	judgeAt := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE playbooks SET judge_verdict = ?, judge_model = ?, judge_at = ? WHERE playbook_id = ?`,
		verdict, judgeModel, judgeAt, playbookID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Activate atomically promotes a playbook version: deactivates all other versions
// in the same series and marks the target active. Idempotent.
// Returns sql.ErrNoRows if the playbook does not exist.
// Returns ErrSystemPlaybook if the playbook is system-managed.
func (s *PlaybookStore) Activate(ctx context.Context, playbookID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var seriesID string
	var isSystem int
	err = tx.QueryRowContext(ctx,
		`SELECT series_id, is_system FROM playbooks WHERE playbook_id = ?`, playbookID).
		Scan(&seriesID, &isSystem)
	if err == sql.ErrNoRows {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	if isSystem != 0 {
		return ErrSystemPlaybook
	}

	// If the draft has no version, auto-assign the next one from the current active.
	var draftVersion string
	if err = tx.QueryRowContext(ctx,
		`SELECT version FROM playbooks WHERE playbook_id = ?`, playbookID).
		Scan(&draftVersion); err != nil {
		return err
	}
	if draftVersion == "" {
		var currentVersion string
		_ = tx.QueryRowContext(ctx,
			`SELECT version FROM playbooks WHERE series_id = ? AND is_active = 1`, seriesID).
			Scan(&currentVersion)
		if next := nextVersion(currentVersion); next != "" {
			if _, err = tx.ExecContext(ctx,
				`UPDATE playbooks SET version = ? WHERE playbook_id = ?`, next, playbookID); err != nil {
				return err
			}
		}
	}

	// Deactivate all other versions in this series.
	if _, err = tx.ExecContext(ctx,
		`UPDATE playbooks SET is_active=0 WHERE series_id=? AND playbook_id != ?`,
		seriesID, playbookID); err != nil {
		return err
	}
	// Activate the target.
	if _, err = tx.ExecContext(ctx,
		`UPDATE playbooks SET is_active=1 WHERE playbook_id=?`, playbookID); err != nil {
		return err
	}

	return tx.Commit()
}

// nextVersion increments the minor component of a "major.minor" version string.
// "1.4" → "1.5", "2" → "2.1", "" → "1.0". Returns "" if the format is unrecognised.
func nextVersion(current string) string {
	if current == "" {
		return "1.0"
	}
	parts := strings.SplitN(current, ".", 2)
	if len(parts) == 1 {
		// Single integer like "2" → "2.1"
		return current + ".1"
	}
	major := parts[0]
	var minor int
	if _, err := fmt.Sscanf(parts[1], "%d", &minor); err != nil {
		return "" // unrecognised format; leave version blank
	}
	return fmt.Sprintf("%s.%d", major, minor+1)
}

// ActivateSystem is like Activate but works for system playbooks. Used by the seeder.
func (s *PlaybookStore) ActivateSystem(ctx context.Context, playbookID, seriesID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.ExecContext(ctx,
		`UPDATE playbooks SET is_active=0 WHERE series_id=? AND playbook_id != ?`,
		seriesID, playbookID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE playbooks SET is_active=1 WHERE playbook_id=?`, playbookID); err != nil {
		return err
	}
	return tx.Commit()
}

// PlaybookListQuery holds filter parameters for List.
type PlaybookListQuery struct {
	ActiveOnly    bool   // if true, return only is_active=1 rows
	IncludeSystem bool   // if true, include is_system=1 rows
	SeriesID      string // if non-empty, filter to this series (useful to list all versions)
	Source        string // if non-empty, filter by source ("generated", "imported", "manual", "system")
}

// DefaultPlaybookListQuery returns the standard query: active versions only,
// system playbooks included.
func DefaultPlaybookListQuery() PlaybookListQuery {
	return PlaybookListQuery{ActiveOnly: true, IncludeSystem: true}
}

const playbookColumns = `playbook_id, name, description, target_hints, created_by, created_at, updated_at,
	problem_class, symptoms, guidance, escalation, related_playbooks, author, last_validated, version,
	series_id, is_active, is_system, source,
	entry_point, escalates_to, requires_evidence, execution_mode, permitted_tools,
	approval_mode, agent_name, transitions_to, origin_trace, playbook_type,
	judge_verdict, judge_model, judge_at,
	root_cause_classes`

// Get returns a playbook by ID, or sql.ErrNoRows if not found.
func (s *PlaybookStore) Get(ctx context.Context, id string) (*Playbook, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+playbookColumns+` FROM playbooks WHERE playbook_id = ?`, id)
	return scanPlaybook(row)
}

// List returns playbooks matching the query, ordered by created_at descending.
func (s *PlaybookStore) List(ctx context.Context, q PlaybookListQuery) ([]*Playbook, error) {
	var where []string
	var args []any

	if q.ActiveOnly {
		where = append(where, "is_active = 1")
	}
	if !q.IncludeSystem {
		where = append(where, "is_system = 0")
	}
	if q.SeriesID != "" {
		where = append(where, "series_id = ?")
		args = append(args, q.SeriesID)
	}
	if q.Source != "" {
		where = append(where, "source = ?")
		args = append(args, q.Source)
	}

	query := `SELECT ` + playbookColumns + ` FROM playbooks`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*Playbook
	for rows.Next() {
		pb, err := scanPlaybook(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, pb)
	}
	return result, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanPlaybook(s scanner) (*Playbook, error) {
	var pb Playbook
	var hintsJSON, symptomsJSON, escalationJSON, relatedJSON string
	var escalatesToJSON, requiresEvidenceJSON, permittedToolsJSON, transitionsToJSON string
	var createdAt, updatedAt string
	var lastValidatedStr *string
	var isActive, isSystem, entryPoint int // SQLite stores bools as INTEGER; scan into int then convert
	var judgeAtStr string
	var rootCauseClassesJSON string

	if err := s.Scan(
		&pb.PlaybookID, &pb.Name, &pb.Description, &hintsJSON,
		&pb.CreatedBy, &createdAt, &updatedAt,
		&pb.ProblemClass, &symptomsJSON, &pb.Guidance, &escalationJSON,
		&relatedJSON, &pb.Author, &lastValidatedStr, &pb.Version,
		&pb.SeriesID, &isActive, &isSystem, &pb.Source,
		&entryPoint, &escalatesToJSON, &requiresEvidenceJSON, &pb.ExecutionMode, &permittedToolsJSON,
		&pb.ApprovalMode, &pb.AgentName, &transitionsToJSON, &pb.OriginTrace, &pb.PlaybookType,
		&pb.JudgeVerdict, &pb.JudgeModel, &judgeAtStr,
		&rootCauseClassesJSON,
	); err != nil {
		return nil, err
	}
	if rootCauseClassesJSON != "" && rootCauseClassesJSON != "{}" {
		var rcc RootCauseClassification
		if err := json.Unmarshal([]byte(rootCauseClassesJSON), &rcc); err == nil && len(rcc.Classes) > 0 {
			pb.RootCauseClasses = &rcc
		}
	}
	if judgeAtStr != "" {
		pb.JudgeAt = parseFlexTime(judgeAtStr)
	}

	pb.IsActive = isActive != 0
	pb.IsSystem = isSystem != 0
	pb.EntryPoint = entryPoint != 0

	// JSON array fields
	if err := json.Unmarshal([]byte(hintsJSON), &pb.TargetHints); err != nil {
		pb.TargetHints = nil
	}
	if symptomsJSON != "" && symptomsJSON != "null" {
		if err := json.Unmarshal([]byte(symptomsJSON), &pb.Symptoms); err != nil {
			pb.Symptoms = nil
		}
	}
	if escalationJSON != "" && escalationJSON != "null" {
		if err := json.Unmarshal([]byte(escalationJSON), &pb.Escalation); err != nil {
			pb.Escalation = nil
		}
	}
	if relatedJSON != "" && relatedJSON != "null" {
		if err := json.Unmarshal([]byte(relatedJSON), &pb.RelatedPlaybooks); err != nil {
			pb.RelatedPlaybooks = nil
		}
	}
	if escalatesToJSON != "" && escalatesToJSON != "null" {
		if err := json.Unmarshal([]byte(escalatesToJSON), &pb.EscalatesTo); err != nil {
			pb.EscalatesTo = nil
		}
	}
	if transitionsToJSON != "" && transitionsToJSON != "null" {
		if err := json.Unmarshal([]byte(transitionsToJSON), &pb.TransitionsTo); err != nil {
			pb.TransitionsTo = nil
		}
	}
	if requiresEvidenceJSON != "" && requiresEvidenceJSON != "null" {
		if err := json.Unmarshal([]byte(requiresEvidenceJSON), &pb.RequiresEvidence); err != nil {
			pb.RequiresEvidence = nil
		}
	}
	if permittedToolsJSON != "" && permittedToolsJSON != "null" {
		if err := json.Unmarshal([]byte(permittedToolsJSON), &pb.PermittedTools); err != nil {
			pb.PermittedTools = nil
		}
	}

	// Timestamps
	pb.CreatedAt = parseFlexTime(createdAt)
	pb.UpdatedAt = parseFlexTime(updatedAt)
	if lastValidatedStr != nil && *lastValidatedStr != "" {
		t := parseFlexTime(*lastValidatedStr)
		if !t.IsZero() {
			pb.LastValidated = &t
		}
	}
	return &pb, nil
}

// parseFlexTime parses a time string in RFC3339Nano, RFC3339, or SQLite
// datetime format. Returns the zero time if parsing fails.
func parseFlexTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z07:00", s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
