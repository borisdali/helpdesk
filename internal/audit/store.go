package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Store persists audit events to SQLite and notifies listeners.
type Store struct {
	db         *sql.DB
	socketPath string
	listeners  []net.Conn
	mu         sync.RWMutex
}

// StoreConfig configures the audit store.
type StoreConfig struct {
	// DBPath is the path to the SQLite database file.
	// If empty, defaults to "audit.db" in the current directory.
	DBPath string

	// SocketPath is the path to the Unix socket for real-time notifications.
	// If empty, notifications are disabled.
	SocketPath string
}

// NewStore creates a new audit store with the given configuration.
func NewStore(cfg StoreConfig) (*Store, error) {
	if cfg.DBPath == "" {
		cfg.DBPath = "audit.db"
	}

	// Ensure directory exists.
	dir := filepath.Dir(cfg.DBPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create audit directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open audit database: %w", err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}

	// Create tables.
	if err := createTables(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("create tables: %w", err)
	}

	s := &Store{
		db:         db,
		socketPath: cfg.SocketPath,
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

func createTables(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT UNIQUE NOT NULL,
		timestamp TEXT NOT NULL,
		event_type TEXT NOT NULL,
		session_id TEXT NOT NULL,
		user_id TEXT,
		user_query TEXT,
		decision_agent TEXT,
		decision_category TEXT,
		decision_confidence REAL,
		decision_json TEXT,
		outcome_status TEXT,
		outcome_error TEXT,
		outcome_duration_ms INTEGER,
		raw_json TEXT NOT NULL,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_events_timestamp ON audit_events(timestamp);
	CREATE INDEX IF NOT EXISTS idx_events_session ON audit_events(session_id);
	CREATE INDEX IF NOT EXISTS idx_events_type ON audit_events(event_type);
	CREATE INDEX IF NOT EXISTS idx_events_agent ON audit_events(decision_agent);
	`
	_, err := db.Exec(schema)
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

	rawJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	var decisionJSON []byte
	var decisionAgent, decisionCategory string
	var decisionConfidence float64
	if event.Decision != nil {
		decisionJSON, _ = json.Marshal(event.Decision)
		decisionAgent = event.Decision.Agent
		decisionCategory = string(event.Decision.RequestCategory)
		decisionConfidence = event.Decision.Confidence
	}

	var outcomeStatus, outcomeError string
	var outcomeDurationMs int64
	if event.Outcome != nil {
		outcomeStatus = event.Outcome.Status
		outcomeError = event.Outcome.ErrorMessage
		outcomeDurationMs = event.Outcome.Duration.Milliseconds()
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audit_events (
			event_id, timestamp, event_type, session_id, user_id, user_query,
			decision_agent, decision_category, decision_confidence, decision_json,
			outcome_status, outcome_error, outcome_duration_ms, raw_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.EventID,
		event.Timestamp.Format(time.RFC3339Nano),
		string(event.EventType),
		event.Session.ID,
		event.Session.UserID,
		event.Input.UserQuery,
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

	// Notify listeners.
	s.notifyListeners(rawJSON)

	return nil
}

// RecordOutcome updates an existing delegation event with its outcome.
func (s *Store) RecordOutcome(ctx context.Context, eventID string, outcome *Outcome) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE audit_events
		SET outcome_status = ?, outcome_error = ?, outcome_duration_ms = ?
		WHERE event_id = ?
	`,
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

	query += " ORDER BY timestamp DESC"

	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
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
	SessionID     string
	EventType     EventType
	Agent         string
	Since         time.Time
	MinConfidence float64
	MaxConfidence float64
	Limit         int
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
func (s *Store) notifyListeners(eventJSON []byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Append newline for line-based reading.
	data := append(eventJSON, '\n')

	for i := len(s.listeners) - 1; i >= 0; i-- {
		conn := s.listeners[i]
		conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := conn.Write(data); err != nil {
			// Remove dead connection.
			conn.Close()
			s.mu.RUnlock()
			s.mu.Lock()
			s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
			s.mu.Unlock()
			s.mu.RLock()
		}
	}
}
