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
		user_id TEXT,
		user_query TEXT,
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
		// queryable without json_extract. Denied access is then visible as "deny" in
		// the same column that shows "success"/"error" for tool executions.
		outcomeStatus = event.PolicyDecision.Effect
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
			session_id, user_id, user_query,
			tool_name, tool_json,
			approval_status, approval_json,
			decision_agent, decision_category, decision_confidence, decision_json,
			outcome_status, outcome_error, outcome_duration_ms, raw_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		event.Session.UserID,
		event.Input.UserQuery,
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

	// Chronological order for trace queries, reverse chronological otherwise
	if opts.TraceID != "" {
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
	TraceID        string         // filter by trace ID for end-to-end correlation
	ActionClass    ActionClass    // filter by action class (read, write, destructive)
	ToolName       string         // filter by tool name
	ApprovalStatus ApprovalStatus // filter by approval status
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
