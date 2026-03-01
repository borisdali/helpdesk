package audit

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStore_HashChain(t *testing.T) {
	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "audit_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "audit.db")
	store, err := NewStore(StoreConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initial hash should be genesis
	if store.GetLastHash() != GenesisHash {
		t.Errorf("initial hash = %q, want genesis", store.GetLastHash())
	}

	// Record first event
	event1 := &Event{
		EventID:   "evt_001",
		Timestamp: time.Now(),
		EventType: EventTypeDelegation,
		Session: Session{
			ID:     "sess_test",
			UserID: "testuser",
		},
		Input: Input{
			UserQuery: "first query",
		},
	}

	if err := store.Record(ctx, event1); err != nil {
		t.Fatalf("failed to record event1: %v", err)
	}

	// Event should have hash set
	if event1.EventHash == "" {
		t.Error("event1.EventHash should be set after Record")
	}
	if event1.PrevHash != GenesisHash {
		t.Errorf("event1.PrevHash = %q, want genesis", event1.PrevHash)
	}

	// Store should track last hash
	if store.GetLastHash() != event1.EventHash {
		t.Error("store.GetLastHash() should match event1.EventHash")
	}

	// Record second event
	event2 := &Event{
		EventID:   "evt_002",
		Timestamp: time.Now().Add(time.Second),
		EventType: EventTypeDelegation,
		Session: Session{
			ID:     "sess_test",
			UserID: "testuser",
		},
		Input: Input{
			UserQuery: "second query",
		},
	}

	if err := store.Record(ctx, event2); err != nil {
		t.Fatalf("failed to record event2: %v", err)
	}

	// Second event should link to first
	if event2.PrevHash != event1.EventHash {
		t.Error("event2.PrevHash should equal event1.EventHash")
	}
	if event2.EventHash == event1.EventHash {
		t.Error("event2 should have different hash than event1")
	}

	// Store should track last hash
	if store.GetLastHash() != event2.EventHash {
		t.Error("store.GetLastHash() should match event2.EventHash")
	}
}

func TestStore_VerifyIntegrity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "audit_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "audit.db")
	store, err := NewStore(StoreConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Record several events
	for i := 0; i < 5; i++ {
		event := &Event{
			EventID:   "evt_" + string(rune('a'+i)),
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			EventType: EventTypeDelegation,
			Session: Session{
				ID:     "sess_verify",
				UserID: "user",
			},
			Input: Input{
				UserQuery: "query " + string(rune('a'+i)),
			},
		}
		if err := store.Record(ctx, event); err != nil {
			t.Fatalf("failed to record event %d: %v", i, err)
		}
	}

	// Verify integrity
	status, err := store.VerifyIntegrity(ctx)
	if err != nil {
		t.Fatalf("VerifyIntegrity failed: %v", err)
	}

	if !status.Valid {
		t.Errorf("chain should be valid: %s", status.Error)
	}
	if status.TotalEvents != 5 {
		t.Errorf("TotalEvents = %d, want 5", status.TotalEvents)
	}
	if status.HashedEvents != 5 {
		t.Errorf("HashedEvents = %d, want 5", status.HashedEvents)
	}
	if status.BrokenAt != -1 {
		t.Errorf("BrokenAt = %d, want -1", status.BrokenAt)
	}
}

func TestStore_InitLastHash(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "audit_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "audit.db")

	// Create store and record some events
	store1, err := NewStore(StoreConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("failed to create store1: %v", err)
	}

	ctx := context.Background()
	event := &Event{
		EventID:   "evt_persist",
		Timestamp: time.Now(),
		EventType: EventTypeDelegation,
		Session:   Session{ID: "sess_persist", UserID: "user"},
		Input:     Input{UserQuery: "persist test"},
	}
	if err := store1.Record(ctx, event); err != nil {
		t.Fatalf("failed to record event: %v", err)
	}

	lastHash := store1.GetLastHash()
	store1.Close()

	// Reopen store - should recover last hash
	store2, err := NewStore(StoreConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("failed to create store2: %v", err)
	}
	defer store2.Close()

	if store2.GetLastHash() != lastHash {
		t.Errorf("store2.GetLastHash() = %q, want %q", store2.GetLastHash(), lastHash)
	}
}

// waitForSocket dials the Unix socket until it responds or the deadline passes.
func waitForSocket(t *testing.T, path string, deadline time.Duration) bool {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// newStoreWithSocket creates a Store backed by a temp dir and a Unix socket.
func newStoreWithSocket(t *testing.T) (*Store, string) {
	t.Helper()
	tmpDir := t.TempDir()
	socketPath := fmt.Sprintf("/tmp/audit_test_%d.sock", time.Now().UnixNano()%1e9)
	store, err := NewStore(StoreConfig{
		DBPath:     filepath.Join(tmpDir, "test.db"),
		SocketPath: socketPath,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		os.Remove(socketPath)
	})
	if !waitForSocket(t, socketPath, 2*time.Second) {
		t.Fatal("audit socket did not become ready within 2s")
	}
	return store, socketPath
}

// TestStore_NotifyListeners_SlowConsumerDoesNotBlock is a regression test for
// the 100ms write-deadline + lock-upgrade race in notifyListeners:
//
//   - Old behaviour: a slow listener caused Record() to block (lock upgrade from
//     RLock→Lock mid-loop), or the connection was silently dropped after 100 ms
//     even when the consumer was legitimately busy (e.g. secbot creating an incident).
//
//   - New behaviour: writes are dispatched in a background goroutine with a 5 s
//     deadline; Record() returns immediately regardless of listener speed.
func TestStore_NotifyListeners_SlowConsumerDoesNotBlock(t *testing.T) {
	store, socketPath := newStoreWithSocket(t)

	// Fast listener: actively reads each event.
	fastConn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("connect fast listener: %v", err)
	}
	defer fastConn.Close()

	// Slow listener: never reads; its kernel receive buffer will eventually fill.
	slowConn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("connect slow listener: %v", err)
	}
	defer slowConn.Close()

	time.Sleep(20 * time.Millisecond) // let store register both connections

	const numEvents = 10
	ctx := context.Background()

	// Record events in a goroutine; the main goroutine drains the fast listener.
	// Record() must return promptly — slow listener must not delay it.
	done := make(chan error, 1)
	go func() {
		for i := range numEvents {
			err := store.Record(ctx, &Event{
				EventID:   fmt.Sprintf("evt_%03d", i),
				EventType: EventTypeDelegation,
				Timestamp: time.Now(),
				Session:   Session{ID: "sess_slow", UserID: "testuser"},
				Input:     Input{UserQuery: fmt.Sprintf("query %d", i)},
			})
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Record failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Record() blocked for >10 s — slow listener is preventing audit ingestion")
	}

	// Fast listener should receive all events without being starved by the slow one.
	fastConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	scanner := bufio.NewScanner(fastConn)
	received := 0
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			received++
		}
		if received >= numEvents {
			break
		}
	}
	if received < numEvents {
		t.Errorf("fast listener received %d/%d events", received, numEvents)
	}
}

// TestStore_NotifyListeners_DeadListenerIsPruned verifies that a connection
// closed by the remote end is removed from the listener list after the next
// notification pass so it does not accumulate indefinitely.
func TestStore_NotifyListeners_DeadListenerIsPruned(t *testing.T) {
	store, socketPath := newStoreWithSocket(t)

	// Connect a listener and immediately close it (simulates a crashed consumer).
	dead, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("connect dead listener: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // let store register it
	dead.Close()

	// Connect a healthy listener so we can confirm delivery still works.
	good, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("connect good listener: %v", err)
	}
	defer good.Close()
	time.Sleep(20 * time.Millisecond)

	ctx := context.Background()
	if err := store.Record(ctx, &Event{
		EventID:   "evt_prune",
		EventType: EventTypeDelegation,
		Timestamp: time.Now(),
		Session:   Session{ID: "sess_prune", UserID: "user"},
		Input:     Input{UserQuery: "prune test"},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Good listener receives the event.
	good.SetReadDeadline(time.Now().Add(5 * time.Second))
	sc := bufio.NewScanner(good)
	if !sc.Scan() {
		t.Error("good listener did not receive event")
	}

	// Wait for the async pruning goroutine to finish, then check listener count.
	time.Sleep(200 * time.Millisecond)
	store.mu.RLock()
	n := len(store.listeners)
	store.mu.RUnlock()
	if n != 1 {
		t.Errorf("after pruning dead connection: want 1 live listener, got %d", n)
	}
}

func TestStore_Query(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "audit_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "audit.db")
	store, err := NewStore(StoreConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Record events with different attributes
	events := []*Event{
		{
			EventID:     "evt_001",
			Timestamp:   time.Now(),
			EventType:   EventTypeDelegation,
			ActionClass: ActionRead,
			Session:     Session{ID: "sess_a", UserID: "alice"},
			Input:       Input{UserQuery: "read query"},
		},
		{
			EventID:     "evt_002",
			Timestamp:   time.Now().Add(time.Second),
			EventType:   EventTypeDelegation,
			ActionClass: ActionWrite,
			Session:     Session{ID: "sess_b", UserID: "bob"},
			Input:       Input{UserQuery: "write query"},
		},
		{
			EventID:     "evt_003",
			Timestamp:   time.Now().Add(2 * time.Second),
			EventType:   EventTypeGatewayRequest,
			ActionClass: ActionRead,
			Session:     Session{ID: "sess_a", UserID: "alice"},
			Input:       Input{UserQuery: "gateway query"},
		},
	}

	for _, e := range events {
		if err := store.Record(ctx, e); err != nil {
			t.Fatalf("failed to record event: %v", err)
		}
	}

	// Query by session
	results, err := store.Query(ctx, QueryOptions{SessionID: "sess_a"})
	if err != nil {
		t.Fatalf("query by session failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("query by session: got %d results, want 2", len(results))
	}

	// Query by action class
	results, err = store.Query(ctx, QueryOptions{ActionClass: ActionWrite})
	if err != nil {
		t.Fatalf("query by action class failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("query by action class: got %d results, want 1", len(results))
	}

	// Query by event type
	results, err = store.Query(ctx, QueryOptions{EventType: EventTypeGatewayRequest})
	if err != nil {
		t.Fatalf("query by event type failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("query by event type: got %d results, want 1", len(results))
	}
}

// TestQueryJourneys verifies that QueryJourneys groups events by trace_id and
// surfaces the right user_query, agent, tools_used, and outcome.
func TestQueryJourneys(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "audit_journeys_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(StoreConfig{DBPath: filepath.Join(tmpDir, "audit.db")})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	// Journey 1 (alice, trace tr_aaa): delegation + 2 tool calls
	j1events := []*Event{
		{
			EventID:   "evt_j1_del",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_aaa",
			Session:   Session{ID: "sess_1", UserID: "alice"},
			Input:     Input{UserQuery: "show active connections"},
			Decision:  &Decision{Agent: "postgres_database_agent"},
		},
		{
			EventID:   "tool_j1_t1",
			Timestamp: base.Add(500 * time.Millisecond),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_aaa",
			Session:   Session{ID: "dbagent_1"},
			Tool:      &ToolExecution{Name: "get_active_connections"},
			Outcome:   &Outcome{Status: "success"},
		},
		{
			EventID:   "tool_j1_t2",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_aaa",
			Session:   Session{ID: "dbagent_1"},
			Tool:      &ToolExecution{Name: "get_connection_stats"},
			Outcome:   &Outcome{Status: "success"},
		},
	}

	// Journey 2 (bob, trace tr_bbb): delegation + 1 tool call that errored
	j2events := []*Event{
		{
			EventID:   "evt_j2_del",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeDelegation,
			TraceID:   "tr_bbb",
			Session:   Session{ID: "sess_2", UserID: "bob"},
			Input:     Input{UserQuery: "restart the web pod"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		{
			EventID:   "tool_j2_t1",
			Timestamp: base.Add(3 * time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_bbb",
			Session:   Session{ID: "k8sagent_1"},
			Tool:      &ToolExecution{Name: "get_pods"},
			Outcome:   &Outcome{Status: "error", ErrorMessage: "timeout"},
		},
	}

	for _, e := range append(j1events, j2events...) {
		if err := store.Record(ctx, e); err != nil {
			t.Fatalf("record event %s: %v", e.EventID, err)
		}
	}

	// No filters — both journeys returned, newest first.
	all, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("QueryJourneys() = %d journeys, want 2", len(all))
	}
	// Ordered newest-first by started_at of delegation_decision.
	if all[0].TraceID != "tr_bbb" {
		t.Errorf("all[0].TraceID = %q, want tr_bbb", all[0].TraceID)
	}
	if all[1].TraceID != "tr_aaa" {
		t.Errorf("all[1].TraceID = %q, want tr_aaa", all[1].TraceID)
	}

	j1 := all[1]
	if j1.UserID != "alice" {
		t.Errorf("j1.UserID = %q, want alice", j1.UserID)
	}
	if j1.UserQuery != "show active connections" {
		t.Errorf("j1.UserQuery = %q", j1.UserQuery)
	}
	if j1.Agent != "postgres_database_agent" {
		t.Errorf("j1.Agent = %q", j1.Agent)
	}
	if len(j1.ToolsUsed) != 2 {
		t.Errorf("j1.ToolsUsed = %v, want 2 tools", j1.ToolsUsed)
	}
	if j1.Outcome != "success" {
		t.Errorf("j1.Outcome = %q, want success", j1.Outcome)
	}
	if j1.EventCount != 3 {
		t.Errorf("j1.EventCount = %d, want 3", j1.EventCount)
	}
	if j1.DurationMs <= 0 {
		t.Errorf("j1.DurationMs = %d, want > 0", j1.DurationMs)
	}

	j2 := all[0]
	if j2.Outcome != "error" {
		t.Errorf("j2.Outcome = %q, want error", j2.Outcome)
	}

	// Filter by user — only alice's journey.
	alice, err := store.QueryJourneys(ctx, JourneyOptions{UserID: "alice"})
	if err != nil {
		t.Fatalf("QueryJourneys(alice): %v", err)
	}
	if len(alice) != 1 || alice[0].TraceID != "tr_aaa" {
		t.Errorf("QueryJourneys(alice) = %v, want [tr_aaa]", alice)
	}

	// Filter by time — only events after j2's delegation_decision.
	fromJ2 := base.Add(2 * time.Second)
	windowed, err := store.QueryJourneys(ctx, JourneyOptions{From: fromJ2})
	if err != nil {
		t.Fatalf("QueryJourneys(from): %v", err)
	}
	if len(windowed) != 1 || windowed[0].TraceID != "tr_bbb" {
		t.Errorf("QueryJourneys(from=j2) = %v, want [tr_bbb]", windowed)
	}

	// Empty result when no journeys in range.
	empty, err := store.QueryJourneys(ctx, JourneyOptions{
		From:  base.Add(10 * time.Hour),
		Until: base.Add(11 * time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryJourneys(empty window): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("QueryJourneys(empty window) = %d, want 0", len(empty))
	}
}

// TestRebind verifies that rebind rewrites ? placeholders to $N for PostgreSQL
// and leaves queries unchanged for SQLite.
func TestRebind(t *testing.T) {
	tests := []struct {
		name       string
		isPostgres bool
		input      string
		want       string
	}{
		{
			name:       "SQLite: query unchanged",
			isPostgres: false,
			input:      "SELECT * FROM t WHERE a = ? AND b = ?",
			want:       "SELECT * FROM t WHERE a = ? AND b = ?",
		},
		{
			name:       "PostgreSQL: single placeholder",
			isPostgres: true,
			input:      "SELECT * FROM t WHERE id = ?",
			want:       "SELECT * FROM t WHERE id = $1",
		},
		{
			name:       "PostgreSQL: multiple placeholders",
			isPostgres: true,
			input:      "INSERT INTO t (a, b, c) VALUES (?, ?, ?)",
			want:       "INSERT INTO t (a, b, c) VALUES ($1, $2, $3)",
		},
		{
			name:       "PostgreSQL: no placeholders",
			isPostgres: true,
			input:      "SELECT * FROM t",
			want:       "SELECT * FROM t",
		},
		{
			name:       "PostgreSQL: placeholder in string literal",
			isPostgres: true,
			input:      "UPDATE t SET status = 'pending' WHERE id = ?",
			want:       "UPDATE t SET status = 'pending' WHERE id = $1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rebind(tc.isPostgres, tc.input)
			if got != tc.want {
				t.Errorf("rebind(%v, %q)\n got  %q\n want %q", tc.isPostgres, tc.input, got, tc.want)
			}
		})
	}
}

// TestStoreConfig_DSNRouting verifies that DSN prefixes select the correct backend.
func TestStoreConfig_DSNRouting(t *testing.T) {
	t.Run("file path uses SQLite backend", func(t *testing.T) {
		store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "test.db")})
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		defer store.Close()

		if store.IsPostgres() {
			t.Error("expected IsPostgres()=false for file-path DSN")
		}
	})

	t.Run("DSN field takes precedence over DBPath", func(t *testing.T) {
		// When DSN is a file path it should still open successfully.
		dbPath := filepath.Join(t.TempDir(), "dsn_test.db")
		store, err := NewStore(StoreConfig{
			DBPath: filepath.Join(t.TempDir(), "ignored.db"),
			DSN:    dbPath,
		})
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		defer store.Close()

		if store.IsPostgres() {
			t.Error("expected IsPostgres()=false for SQLite DSN")
		}
	})

	t.Run("postgres:// DSN selects pgx driver", func(t *testing.T) {
		// No real Postgres server is required — the open succeeds because sql.Open
		// is lazy. createTables will fail when it tries to execute a query, but the
		// error must NOT be "unknown driver 'pgx'", which would mean the pgx driver
		// import is missing.
		_, err := NewStore(StoreConfig{DSN: "postgres://localhost:5432/nonexistent_db_for_test"})
		if err == nil {
			// Surprisingly connected — skip (CI may have a PG server).
			t.Skip("unexpectedly connected to postgres; skipping driver-detection assertion")
		}
		if strings.Contains(err.Error(), "unknown driver") {
			t.Errorf("pgx driver not registered: %v", err)
		}
	})

	t.Run("postgresql:// prefix also selects pgx driver", func(t *testing.T) {
		_, err := NewStore(StoreConfig{DSN: "postgresql://localhost:5432/nonexistent_db_for_test"})
		if err == nil {
			t.Skip("unexpectedly connected to postgres; skipping driver-detection assertion")
		}
		if strings.Contains(err.Error(), "unknown driver") {
			t.Errorf("pgx driver not registered: %v", err)
		}
	})
}
