package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Store persists audit events to SQLite or PostgreSQL and notifies listeners.
type Store struct {
	db         *sql.DB
	isPostgres bool   // true when connected to PostgreSQL
	socketPath string
	listeners  []net.Conn
	mu         sync.RWMutex
	lastHash   string     // hash of the last recorded event (for chain)
	hashMu     sync.Mutex // protects lastHash
}

// StoreConfig configures the audit store.
type StoreConfig struct {
	// DBPath is the path to the SQLite database file.
	// Kept for backward compatibility; takes effect when DSN is empty.
	DBPath string

	// DSN is the data-source name. When it starts with "postgres://" or
	// "postgresql://", the PostgreSQL backend (pgx) is used; otherwise the
	// value is treated as a SQLite file path.  If both DSN and DBPath are
	// provided, DSN takes precedence.
	DSN string

	// SocketPath is the path to the Unix socket for real-time notifications.
	// If empty, notifications are disabled.
	SocketPath string
}

// IsPostgres reports whether the store is backed by PostgreSQL.
func (s *Store) IsPostgres() bool { return s.isPostgres }

// rebind rewrites a query that uses ? placeholders into one using $N
// placeholders when the store is backed by PostgreSQL.
func rebind(isPostgres bool, query string) string {
	if !isPostgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, c := range query {
		if c == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// NewStore creates a new audit store with the given configuration.
func NewStore(cfg StoreConfig) (*Store, error) {
	// Resolve the effective DSN: prefer cfg.DSN, fall back to cfg.DBPath.
	dsn := cfg.DSN
	if dsn == "" {
		dsn = cfg.DBPath
	}
	if dsn == "" {
		dsn = "audit.db"
	}

	// Detect backend from DSN prefix.
	isPostgres := strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")

	var db *sql.DB
	var err error

	if isPostgres {
		db, err = sql.Open("pgx", dsn)
		if err != nil {
			return nil, fmt.Errorf("open postgres database: %w", err)
		}
	} else {
		// SQLite: ensure directory exists.
		dir := filepath.Dir(dsn)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("create audit directory: %w", err)
			}
		}
		db, err = sql.Open("sqlite", dsn)
		if err != nil {
			return nil, fmt.Errorf("open audit database: %w", err)
		}
		// Enable WAL mode for better concurrent read performance (SQLite only).
		if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			db.Close()
			return nil, fmt.Errorf("enable WAL mode: %w", err)
		}
	}

	// Create tables.
	if err := createTables(db, isPostgres); err != nil {
		db.Close()
		return nil, fmt.Errorf("create tables: %w", err)
	}

	s := &Store{
		db:         db,
		isPostgres: isPostgres,
		socketPath: cfg.SocketPath,
		lastHash:   GenesisHash,
	}

	// Initialize lastHash from the most recent event
	if err := s.initLastHash(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init last hash: %w", err)
	}

	// Start Unix socket listener if configured.
	if cfg.SocketPath != "" {
		if err := s.startSocketListener(); err != nil {
			db.Close()
			return nil, fmt.Errorf("start socket listener: %w", err)
		}
	}

	return s, nil
}

// initLastHash loads the hash of the most recent event from the database.
func (s *Store) initLastHash() error {
	var hash sql.NullString
	err := s.db.QueryRow(`
		SELECT event_hash FROM audit_events
		ORDER BY id DESC LIMIT 1
	`).Scan(&hash)

	if err == sql.ErrNoRows {
		s.lastHash = GenesisHash
		return nil
	}
	if err != nil {
		return err
	}

	if hash.Valid && hash.String != "" {
		s.lastHash = hash.String
	} else {
		s.lastHash = GenesisHash
	}
	return nil
}

func createTables(db *sql.DB, isPostgres bool) error {
	// Primary-key definition differs between SQLite and PostgreSQL.
	pkDef := "INTEGER PRIMARY KEY AUTOINCREMENT"
	createdAt := "TEXT DEFAULT CURRENT_TIMESTAMP"
	if isPostgres {
		pkDef = "BIGSERIAL PRIMARY KEY"
		createdAt = "TIMESTAMPTZ DEFAULT NOW()"
	}

	schema := fmt.Sprintf(`
	CREATE TABLE IF NOT EXISTS audit_events (
		id %s,
		event_id TEXT UNIQUE NOT NULL,
		timestamp TEXT NOT NULL,
		event_type TEXT NOT NULL,
		trace_id TEXT,
		parent_id TEXT,
		action_class TEXT,
		prev_hash TEXT,
		event_hash TEXT,
		session_id TEXT NOT NULL,
		session_agent TEXT,
		user_id TEXT,
		user_query TEXT,
		purpose TEXT,
		purpose_note TEXT,
		tool_name TEXT,
		tool_json TEXT,
		approval_status TEXT,
		approval_json TEXT,
		decision_agent TEXT,
		decision_category TEXT,
		decision_confidence REAL,
		decision_json TEXT,
		outcome_status TEXT,
		outcome_error TEXT,
		outcome_duration_ms INTEGER,
		raw_json TEXT NOT NULL,
		created_at %s
	);
	`, pkDef, createdAt)

	if _, err := db.Exec(schema); err != nil {
		return err
	}

	// Migrate existing tables: add columns that may not exist.
	// PostgreSQL supports IF NOT EXISTS for ALTER TABLE ADD COLUMN; SQLite does not,
	// so we simply ignore errors (duplicate-column) on both backends.
	ifNotExists := ""
	if isPostgres {
		ifNotExists = "IF NOT EXISTS "
	}
	migrations := []string{
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "trace_id TEXT",
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "parent_id TEXT",
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "action_class TEXT",
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "prev_hash TEXT",
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "event_hash TEXT",
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "tool_name TEXT",
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "tool_json TEXT",
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "approval_status TEXT",
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "approval_json TEXT",
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "session_agent TEXT",
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "purpose TEXT",
		"ALTER TABLE audit_events ADD COLUMN " + ifNotExists + "purpose_note TEXT",
	}
	for _, m := range migrations {
		db.Exec(m) //nolint:errcheck
	}

	// Create indexes (safe to run multiple times with IF NOT EXISTS).
	indexes := `
	CREATE INDEX IF NOT EXISTS idx_events_timestamp ON audit_events(timestamp);
	CREATE INDEX IF NOT EXISTS idx_events_session ON audit_events(session_id);
	CREATE INDEX IF NOT EXISTS idx_events_type ON audit_events(event_type);
	CREATE INDEX IF NOT EXISTS idx_events_agent ON audit_events(decision_agent);
	CREATE INDEX IF NOT EXISTS idx_events_trace ON audit_events(trace_id);
	CREATE INDEX IF NOT EXISTS idx_events_parent ON audit_events(parent_id);
	CREATE INDEX IF NOT EXISTS idx_events_action_class ON audit_events(action_class);
	CREATE INDEX IF NOT EXISTS idx_events_tool ON audit_events(tool_name);
	CREATE INDEX IF NOT EXISTS idx_events_approval ON audit_events(approval_status);
	`
	_, err := db.Exec(indexes)
	return err
}

// Record persists an audit event and notifies listeners.
func (s *Store) Record(ctx context.Context, event *Event) error {
	// Generate event ID if not set.
	if event.EventID == "" {
		event.EventID = "evt_" + uuid.New().String()[:8]
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	// Compute hash chain - hold lock through DB write to prevent race conditions
	s.hashMu.Lock()
	defer s.hashMu.Unlock()

	event.PrevHash = s.lastHash
	event.EventHash = ComputeEventHash(event)

	rawJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	var decisionJSON []byte
	var decisionAgent, decisionCategory string
	var decisionConfidence sql.NullFloat64
	if event.Decision != nil {
		decisionJSON, _ = json.Marshal(event.Decision)
		decisionAgent = event.Decision.Agent
		decisionCategory = string(event.Decision.RequestCategory)
		decisionConfidence = sql.NullFloat64{Float64: event.Decision.Confidence, Valid: true}
	}

	var outcomeStatus, outcomeError string
	var outcomeDurationMs int64
	if event.Outcome != nil {
		outcomeStatus = event.Outcome.Status
		outcomeError = event.Outcome.ErrorMessage
		outcomeDurationMs = event.Outcome.Duration.Milliseconds()
	} else if event.PolicyDecision != nil {
		// For policy decision events, surface the effect in outcome_status so it is
		// queryable without json_extract. Normalize "deny" → "denied" so the stored
		// value matches the canonical journey outcome vocabulary.
		switch event.PolicyDecision.Effect {
		case "deny":
			outcomeStatus = "denied"
		default:
			outcomeStatus = event.PolicyDecision.Effect
		}
	} else if event.DelegationVerification != nil {
		// Surface mismatch as "unverified_claim" so QueryJourneys can elevate the
		// journey outcome. Clean verifications are recorded as "verified".
		if event.DelegationVerification.Mismatch {
			outcomeStatus = "unverified_claim"
		} else {
			outcomeStatus = "verified"
		}
	}

	// Extract purpose at the top level for indexed querying.
	// Top-level Event.Purpose is set on gateway_request anchor events.
	// PolicyDecision.Purpose is set on policy_decision events.
	// Both sources are consolidated into a single queryable column.
	purposeVal := event.Purpose
	purposeNoteVal := event.PurposeNote
	if purposeVal == "" && event.PolicyDecision != nil {
		purposeVal = event.PolicyDecision.Purpose
		purposeNoteVal = event.PolicyDecision.PurposeNote
	}

	var toolName, toolAgent string
	var toolJSON []byte
	if event.Tool != nil {
		toolName = event.Tool.Name
		toolAgent = event.Tool.Agent
		toolJSON, _ = json.Marshal(event.Tool)
	}

	// For tool executions, use Tool.Agent as fallback for decision_agent
	if decisionAgent == "" && toolAgent != "" {
		decisionAgent = toolAgent
	}

	var approvalStatus string
	var approvalJSON []byte
	if event.Approval != nil {
		approvalStatus = string(event.Approval.Status)
		approvalJSON, _ = json.Marshal(event.Approval)
	}

	_, err = s.db.ExecContext(ctx, rebind(s.isPostgres, `
		INSERT INTO audit_events (
			event_id, timestamp, event_type, trace_id, parent_id, action_class,
			prev_hash, event_hash,
			session_id, session_agent, user_id, user_query,
			purpose, purpose_note,
			tool_name, tool_json,
			approval_status, approval_json,
			decision_agent, decision_category, decision_confidence, decision_json,
			outcome_status, outcome_error, outcome_duration_ms, raw_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`),
		event.EventID,
		event.Timestamp.Format(time.RFC3339Nano),
		string(event.EventType),
		event.TraceID,
		event.ParentID,
		string(event.ActionClass),
		event.PrevHash,
		event.EventHash,
		event.Session.ID,
		event.Session.AgentName,
		event.Session.UserID,
		event.Input.UserQuery,
		purposeVal,
		purposeNoteVal,
		toolName,
		string(toolJSON),
		approvalStatus,
		string(approvalJSON),
		decisionAgent,
		decisionCategory,
		decisionConfidence,
		string(decisionJSON),
		outcomeStatus,
		outcomeError,
		outcomeDurationMs,
		string(rawJSON),
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}

	// Update lastHash only after successful write
	s.lastHash = event.EventHash

	// Notify listeners.
	s.notifyListeners(rawJSON)

	return nil
}

// RecordOutcome updates an existing delegation event with its outcome.
func (s *Store) RecordOutcome(ctx context.Context, eventID string, outcome *Outcome) error {
	_, err := s.db.ExecContext(ctx, rebind(s.isPostgres, `
		UPDATE audit_events
		SET outcome_status = ?, outcome_error = ?, outcome_duration_ms = ?
		WHERE event_id = ?
	`),
		outcome.Status,
		outcome.ErrorMessage,
		outcome.Duration.Milliseconds(),
		eventID,
	)
	return err
}

// Query returns recent events matching the given filters.
func (s *Store) Query(ctx context.Context, opts QueryOptions) ([]Event, error) {
	query := `SELECT raw_json FROM audit_events WHERE 1=1`
	var args []any

	if opts.EventID != "" {
		query += " AND event_id = ?"
		args = append(args, opts.EventID)
	}
	if opts.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, opts.SessionID)
	}
	if opts.EventType != "" {
		query += " AND event_type = ?"
		args = append(args, string(opts.EventType))
	}
	if opts.Agent != "" {
		query += " AND decision_agent = ?"
		args = append(args, opts.Agent)
	}
	if !opts.Since.IsZero() {
		query += " AND timestamp >= ?"
		args = append(args, opts.Since.Format(time.RFC3339Nano))
	}
	if opts.MinConfidence > 0 {
		query += " AND decision_confidence >= ?"
		args = append(args, opts.MinConfidence)
	}
	if opts.MaxConfidence > 0 {
		query += " AND decision_confidence <= ?"
		args = append(args, opts.MaxConfidence)
	}
	if opts.TraceID != "" {
		query += " AND trace_id = ?"
		args = append(args, opts.TraceID)
	}
	if opts.TraceIDPrefix != "" {
		query += " AND trace_id LIKE ?"
		args = append(args, opts.TraceIDPrefix+"%")
	}
	if opts.ActionClass != "" {
		query += " AND action_class = ?"
		args = append(args, string(opts.ActionClass))
	}
	if opts.ToolName != "" {
		query += " AND tool_name = ?"
		args = append(args, opts.ToolName)
	}
	if opts.ApprovalStatus != "" {
		query += " AND approval_status = ?"
		args = append(args, string(opts.ApprovalStatus))
	}

	// Chronological order for trace/prefix queries, reverse chronological otherwise
	if opts.TraceID != "" || opts.TraceIDPrefix != "" {
		query += " ORDER BY timestamp ASC"
	} else {
		query += " ORDER BY timestamp DESC"
	}

	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}

	rows, err := s.db.QueryContext(ctx, rebind(s.isPostgres, query), args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var rawJSON string
		if err := rows.Scan(&rawJSON); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		var event Event
		if err := json.Unmarshal([]byte(rawJSON), &event); err != nil {
			return nil, fmt.Errorf("unmarshal event: %w", err)
		}
		events = append(events, event)
	}

	return events, rows.Err()
}

// QueryOptions specifies filters for querying events.
type QueryOptions struct {
	EventID        string         // filter by exact event ID (returns at most one event)
	SessionID      string
	EventType      EventType
	Agent          string
	Since          time.Time
	MinConfidence  float64
	MaxConfidence  float64
	Limit          int
	TraceID        string         // filter by exact trace ID for end-to-end correlation
	TraceIDPrefix  string         // filter by trace ID prefix (e.g. "chk_", "sess_")
	ActionClass    ActionClass    // filter by action class (read, write, destructive)
	ToolName       string         // filter by tool name
	ApprovalStatus ApprovalStatus // filter by approval status
}

// JourneyOptions specifies filters for QueryJourneys.
type JourneyOptions struct {
	UserID     string        // optional; empty = all users
	Purpose    string        // optional; filter by declared purpose (e.g. "diagnostic")
	From       time.Time     // inclusive lower bound on timestamp
	Until      time.Time     // exclusive upper bound on timestamp
	Limit      int           // max journeys returned; default 50
	Since      time.Duration // if non-zero, overrides From = time.Now().Add(-Since)
	Category   string        // filter by decision_category (e.g. "database", "kubernetes")
	Outcome    string        // filter by computed journey outcome (post-aggregation)
	HasRetries bool          // only journeys with retry_count > 0 (post-aggregation)
	TraceID    string        // filter by exact trace ID; returns at most one journey
}

// DelegationSummary captures one orchestrator-to-sub-agent delegation turn:
// the intent that drove it and the tools the sub-agent actually called.
type DelegationSummary struct {
	Intent string   `json:"intent"`
	Tools  []string `json:"tools"`
}

// JourneySummary summarises a single end-to-end user request (one trace_id).
type JourneySummary struct {
	TraceID     string              `json:"trace_id"`
	StartedAt   string              `json:"started_at"`
	EndedAt     string              `json:"ended_at"`
	DurationMs  int64               `json:"duration_ms"`
	UserID      string              `json:"user_id,omitempty"`
	UserQuery   string              `json:"user_query,omitempty"`
	Purpose     string              `json:"purpose,omitempty"`
	PurposeNote string              `json:"purpose_note,omitempty"`
	Agent       string              `json:"agent,omitempty"`
	Category    string              `json:"category,omitempty"` // decision_category from delegation_decision event
	Delegations []DelegationSummary `json:"delegations,omitempty"`
	ToolsUsed   []string            `json:"tools_used"`
	Outcome     string              `json:"outcome,omitempty"`
	EventCount  int                 `json:"event_count"`
	// RetryCount is the number of post-mutation verification re-check attempts
	// recorded for this journey (tool_retry events). Non-zero means a mutation
	// tool had to wait for state to propagate but eventually confirmed success.
	RetryCount int `json:"retry_count,omitempty"`
}

// QueryJourneys returns journey summaries for traces anchored by a
// delegation_decision event (orchestrator mode) or a gateway_request event
// without a tool_name (gateway NL-query mode).
//
// Two queries are issued: one to find matching trace IDs from anchor events,
// and one to fetch all events for those traces so that tool names, outcome,
// and wall-clock duration can be computed in Go.
func (s *Store) QueryJourneys(ctx context.Context, opts JourneyOptions) ([]JourneySummary, error) {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}

	// Since overrides From when set. Use UTC so the resulting timestamp is
	// lexicographically comparable with UTC timestamps stored in the database.
	if opts.Since > 0 {
		opts.From = time.Now().UTC().Add(-opts.Since)
	}

	// Step 1: find trace IDs from anchor events.
	// Anchors are either delegation_decision events (old orchestrator path) or
	// gateway_request events with no tool_name (gateway NL-query path).
	// Direct tool calls (gateway_request with tool_name set) are excluded —
	// they use dt_ prefixed trace IDs and are not surfaced as journeys.
	q1 := `SELECT trace_id, MIN(timestamp) AS first_event
		FROM audit_events
		WHERE (event_type = 'delegation_decision'
		    OR (event_type = 'gateway_request' AND (tool_name IS NULL OR tool_name = '')))
		  AND trace_id != ''`
	var args1 []any
	if opts.TraceID != "" {
		q1 += " AND trace_id = ?"
		args1 = append(args1, opts.TraceID)
	}
	if opts.UserID != "" {
		q1 += " AND user_id = ?"
		args1 = append(args1, opts.UserID)
	}
	if opts.Purpose != "" {
		q1 += " AND purpose = ?"
		args1 = append(args1, opts.Purpose)
	}
	if !opts.From.IsZero() {
		q1 += " AND timestamp >= ?"
		args1 = append(args1, opts.From.Format(time.RFC3339Nano))
	}
	if !opts.Until.IsZero() {
		q1 += " AND timestamp < ?"
		args1 = append(args1, opts.Until.Format(time.RFC3339Nano))
	}
	q1 += " GROUP BY trace_id ORDER BY first_event DESC LIMIT ?"
	args1 = append(args1, opts.Limit)

	rows1, err := s.db.QueryContext(ctx, rebind(s.isPostgres, q1), args1...)
	if err != nil {
		return nil, fmt.Errorf("query journey trace ids: %w", err)
	}
	defer rows1.Close()

	var traceIDs []string
	for rows1.Next() {
		var traceID, firstEvent string
		if err := rows1.Scan(&traceID, &firstEvent); err != nil {
			return nil, fmt.Errorf("scan trace id: %w", err)
		}
		traceIDs = append(traceIDs, traceID)
	}
	if err := rows1.Err(); err != nil {
		return nil, err
	}
	if len(traceIDs) == 0 {
		return []JourneySummary{}, nil
	}

	// Step 2: fetch all events for those trace IDs in one query.
	placeholders := strings.Repeat("?,", len(traceIDs))
	placeholders = placeholders[:len(placeholders)-1]
	q2 := fmt.Sprintf(
		`SELECT trace_id, event_type, user_id, user_query, session_agent, decision_agent,
		        decision_category, tool_name, outcome_status, approval_status, timestamp,
		        purpose, purpose_note
		 FROM audit_events
		 WHERE trace_id IN (%s)
		 ORDER BY trace_id, timestamp ASC`, placeholders)
	args2 := make([]any, len(traceIDs))
	for i, id := range traceIDs {
		args2[i] = id
	}

	rows2, err := s.db.QueryContext(ctx, rebind(s.isPostgres, q2), args2...)
	if err != nil {
		return nil, fmt.Errorf("query journey events: %w", err)
	}
	defer rows2.Close()

	type traceData struct {
		startedAt          string
		endedAt            string
		userID             string
		userQuery          string
		purpose            string
		purposeNote        string
		agent              string // name of the owning agent (orchestrator name when session_agent is set, else sub-agent)
		category           string
		tools              []string
		delegations        []DelegationSummary
		currentDelegIdx    int // index into delegations for the in-progress delegation; -1 = none
		outcome            string
		count              int
		retryCount         int  // number of tool_retry events in this trace
		sawRequireApproval bool // true if a require_approval policy decision was seen
	}

	// Preserve the order returned by step 1.
	byTrace := make(map[string]*traceData, len(traceIDs))
	for _, id := range traceIDs {
		byTrace[id] = &traceData{currentDelegIdx: -1}
	}

	for rows2.Next() {
		var (
			traceID, eventType                                           string
			userID, userQuery, sessionAgent, agent, decisionCategory    sql.NullString
			toolName, outcomeStatus, approvalStatus                     sql.NullString
			ts                                                           string
			purposeCol, purposeNoteCol                                  sql.NullString
		)
		if err := rows2.Scan(&traceID, &eventType, &userID, &userQuery, &sessionAgent, &agent,
			&decisionCategory, &toolName, &outcomeStatus, &approvalStatus, &ts,
			&purposeCol, &purposeNoteCol); err != nil {
			return nil, fmt.Errorf("scan journey event: %w", err)
		}
		d := byTrace[traceID]
		if d == nil {
			continue
		}
		if d.startedAt == "" || ts < d.startedAt {
			d.startedAt = ts
		}
		if ts > d.endedAt {
			d.endedAt = ts
		}

		// verification_outcome and delegation_verification events are internal plumbing:
		// they contribute to the outcome priority but are NOT counted in event_count
		// and NOT added to tools_used.
		if eventType == string(EventTypeVerificationOutcome) || eventType == string(EventTypeDelegationVerification) {
			if outcomeStatus.Valid && outcomeStatus.String != "" {
				if outcomePriority(outcomeStatus.String) > outcomePriority(d.outcome) {
					d.outcome = outcomeStatus.String
				}
			}
			continue
		}

		d.count++

		if eventType == string(EventTypeDelegation) {
			// delegation_decision is authoritative — overwrite any previously set
			// gateway_request values with the richer orchestrator-side metadata.
			if userID.Valid && userID.String != "" {
				d.userID = userID.String
			}
			if userQuery.Valid && userQuery.String != "" {
				d.userQuery = userQuery.String
			}
			if purposeCol.Valid && purposeCol.String != "" {
				d.purpose = purposeCol.String
			}
			if purposeNoteCol.Valid && purposeNoteCol.String != "" {
				d.purposeNote = purposeNoteCol.String
			}
			// Prefer the session's owning agent name (e.g. "helpdesk_orchestrator")
			// over the sub-agent being delegated to. This makes orchestrator-mediated
			// journeys show the orchestrator as the top-level agent.
			if sessionAgent.Valid && sessionAgent.String != "" {
				d.agent = sessionAgent.String
			} else if agent.Valid && agent.String != "" {
				d.agent = agent.String
			}
			if d.category == "" && decisionCategory.Valid && decisionCategory.String != "" {
				d.category = decisionCategory.String
			}
			// Start a new delegation entry. Tools recorded after this event and
			// before the next delegation_decision will be attached to this entry.
			intent := ""
			if userQuery.Valid {
				intent = userQuery.String
			}
			d.delegations = append(d.delegations, DelegationSummary{Intent: intent, Tools: []string{}})
			d.currentDelegIdx = len(d.delegations) - 1
		} else if eventType == string(EventTypeGatewayRequest) {
			// gateway_request is the anchor in gateway NL-query mode.
			// Only use it as a fallback — don't overwrite delegation_decision data.
			if userID.Valid && userID.String != "" && d.userID == "" {
				d.userID = userID.String
			}
			if userQuery.Valid && userQuery.String != "" && d.userQuery == "" {
				d.userQuery = userQuery.String
			}
			if purposeCol.Valid && purposeCol.String != "" && d.purpose == "" {
				d.purpose = purposeCol.String
			}
			if purposeNoteCol.Valid && purposeNoteCol.String != "" && d.purposeNote == "" {
				d.purposeNote = purposeNoteCol.String
			}
			if agent.Valid && agent.String != "" && d.agent == "" {
				d.agent = agent.String
			}
		}

		// Append every tool call in timestamp order, including repeats.
		if toolName.Valid && toolName.String != "" {
			d.tools = append(d.tools, toolName.String)
			if d.currentDelegIdx >= 0 {
				d.delegations[d.currentDelegIdx].Tools = append(d.delegations[d.currentDelegIdx].Tools, toolName.String)
			}
		}

		// tool_retry events: count retries but never let their outcome_status
		// ("retrying" / "resolved") overwrite the journey's real outcome.
		if eventType == string(EventTypeToolRetry) {
			d.retryCount++
		} else if outcomeStatus.Valid && outcomeStatus.String != "" {
			status := outcomeStatus.String
			if status == "require_approval" {
				// Record that a human approval was required; we'll upgrade the
				// journey outcome to "approved" at the end if the tool succeeded.
				d.sawRequireApproval = true
			} else if outcomePriority(status) > outcomePriority(d.outcome) {
				d.outcome = status
			}
		}

		// Approval events with status="approved" carry the grant signal.
		if approvalStatus.Valid && approvalStatus.String == "approved" {
			d.sawRequireApproval = true
		}
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}

	summaries := make([]JourneySummary, 0, len(traceIDs))
	for _, id := range traceIDs {
		d := byTrace[id]
		var durationMs int64
		if d.startedAt != "" && d.endedAt != "" {
			t1, e1 := time.Parse(time.RFC3339Nano, d.startedAt)
			t2, e2 := time.Parse(time.RFC3339Nano, d.endedAt)
			if e1 == nil && e2 == nil {
				durationMs = t2.Sub(t1).Milliseconds()
			}
		}
		tools := d.tools
		if tools == nil {
			tools = []string{}
		}
		// If an approval was required and the tool ran successfully (any positive
		// outcome), upgrade to "approved" so human involvement is visible.
		if d.sawRequireApproval && (d.outcome == "success" || d.outcome == "verified_ok") {
			d.outcome = "approved"
		}
		summaries = append(summaries, JourneySummary{
			TraceID:     id,
			StartedAt:   d.startedAt,
			EndedAt:     d.endedAt,
			DurationMs:  durationMs,
			UserID:      d.userID,
			UserQuery:   d.userQuery,
			Purpose:     d.purpose,
			PurposeNote: d.purposeNote,
			Agent:       d.agent,
			Category:    d.category,
			Delegations: d.delegations,
			ToolsUsed:   tools,
			Outcome:     d.outcome,
			EventCount:  d.count,
			RetryCount:  d.retryCount,
		})
	}

	// Post-aggregation filters (applied in Go after SQL aggregation).
	if opts.Category != "" {
		summaries = filterJourneys(summaries, func(j JourneySummary) bool {
			return j.Category == opts.Category
		})
	}
	if opts.Outcome != "" {
		summaries = filterJourneys(summaries, func(j JourneySummary) bool {
			return j.Outcome == opts.Outcome
		})
	}
	if opts.HasRetries {
		summaries = filterJourneys(summaries, func(j JourneySummary) bool {
			return j.RetryCount > 0
		})
	}

	return summaries, nil
}

// outcomePriority returns the severity rank for a journey outcome string.
// Higher priority outcomes win when aggregating events within a trace.
//
//	unverified_claim(9) > error(8) > denied(7) > escalation_required(6) > verified_failed(5)
//	> verified_warning(4) > approved(3) > verified_ok(2) > success(1) > verified(0.5) > unknown(0)
func outcomePriority(o string) int {
	switch o {
	case "unverified_claim":    return 9
	case "error":               return 8
	case "denied":              return 7
	case "escalation_required": return 6
	case "verified_failed":     return 5
	case "verified_warning":    return 4
	case "approved":            return 3
	case "verified_ok":         return 2
	case "success":             return 1
	case "verified":            return 0 // clean verification doesn't override a real outcome
	default:                    return 0
	}
}

// filterJourneys returns a new slice containing only journeys for which keep returns true.
func filterJourneys(journeys []JourneySummary, keep func(JourneySummary) bool) []JourneySummary {
	out := journeys[:0]
	for _, j := range journeys {
		if keep(j) {
			out = append(out, j)
		}
	}
	return out
}

// VerifyIntegrity verifies the hash chain integrity of the audit log.
func (s *Store) VerifyIntegrity(ctx context.Context) (ChainStatus, error) {
	// Query events in insertion order (id ASC).
	// The hash chain links events in the order they were inserted into the DB,
	// NOT by their Timestamp field. Events can arrive out of timestamp order
	// (e.g., a gateway event has Timestamp=request-start but is inserted AFTER
	// the agent event that was recorded mid-request), so sorting by timestamp
	// would produce a different order than the chain was built in.
	rows, err := s.db.QueryContext(ctx, `SELECT raw_json FROM audit_events ORDER BY id ASC`)
	if err != nil {
		return ChainStatus{}, fmt.Errorf("query events for verify: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var rawJSON string
		if err := rows.Scan(&rawJSON); err != nil {
			return ChainStatus{}, fmt.Errorf("scan event: %w", err)
		}
		var event Event
		if err := json.Unmarshal([]byte(rawJSON), &event); err != nil {
			return ChainStatus{}, fmt.Errorf("unmarshal event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return ChainStatus{}, err
	}

	return VerifyChainStatus(events), nil
}

// GetLastHash returns the hash of the most recent event.
func (s *Store) GetLastHash() string {
	s.hashMu.Lock()
	defer s.hashMu.Unlock()
	return s.lastHash
}

// DB returns the underlying database connection for shared access.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close closes the store and releases resources.
func (s *Store) Close() error {
	s.mu.Lock()
	for _, conn := range s.listeners {
		conn.Close()
	}
	s.listeners = nil
	s.mu.Unlock()

	if s.socketPath != "" {
		os.Remove(s.socketPath)
	}

	return s.db.Close()
}

// startSocketListener starts a Unix socket for real-time event notifications.
func (s *Store) startSocketListener() error {
	// Remove existing socket file.
	os.Remove(s.socketPath)

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // Listener closed
			}

			s.mu.Lock()
			s.listeners = append(s.listeners, conn)
			s.mu.Unlock()
		}
	}()

	return nil
}

// notifyListeners sends an event to all connected listeners.
// Writes are dispatched in a goroutine so Record() returns immediately and
// slow consumers don't block audit ingestion. Dead connections are pruned
// after each notification pass.
func (s *Store) notifyListeners(eventJSON []byte) {
	// Snapshot the listener list under a read lock — no I/O while holding the lock,
	// and no lock-upgrade race from the old RLock→Lock pattern.
	s.mu.RLock()
	if len(s.listeners) == 0 {
		s.mu.RUnlock()
		return
	}
	conns := make([]net.Conn, len(s.listeners))
	copy(conns, s.listeners)
	s.mu.RUnlock()

	// Copy the payload; eventJSON is owned by the caller and must not be mutated.
	data := make([]byte, len(eventJSON)+1)
	copy(data, eventJSON)
	data[len(eventJSON)] = '\n'

	go func() {
		var dead []net.Conn
		for _, conn := range conns {
			// Allow clients up to 5 s to drain their receive buffer before
			// we consider them dead (e.g. secbot may be mid-incident-creation).
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(data); err != nil {
				conn.Close()
				dead = append(dead, conn)
			}
		}
		if len(dead) == 0 {
			return
		}
		deadSet := make(map[net.Conn]bool, len(dead))
		for _, c := range dead {
			deadSet[c] = true
		}
		s.mu.Lock()
		live := make([]net.Conn, 0, len(s.listeners))
		for _, c := range s.listeners {
			if !deadSet[c] {
				live = append(live, c)
			}
		}
		s.listeners = live
		s.mu.Unlock()
	}()
}
