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
	// ToolsUsed is now ordered by insertion time with repeats preserved (no dedup/sort).
	wantTools := []string{"get_active_connections", "get_connection_stats"}
	if len(j1.ToolsUsed) != len(wantTools) {
		t.Errorf("j1.ToolsUsed = %v, want %v", j1.ToolsUsed, wantTools)
	} else {
		for i, want := range wantTools {
			if j1.ToolsUsed[i] != want {
				t.Errorf("j1.ToolsUsed[%d] = %q, want %q (wrong order or content)", i, j1.ToolsUsed[i], want)
			}
		}
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

// TestQueryJourneys_RetryCountPopulated verifies that tool_retry events increment
// JourneySummary.RetryCount without corrupting the journey outcome.
func TestQueryJourneys_RetryCountPopulated(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "audit_retry_count_test")
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

	events := []*Event{
		{
			EventID:   "del_retry1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_retry",
			Session:   Session{ID: "sess_r", UserID: "carol"},
			Input:     Input{UserQuery: "cancel query 42"},
			Decision:  &Decision{Agent: "postgres_database_agent"},
		},
		{
			EventID:   "tool_retry1",
			Timestamp: base.Add(500 * time.Millisecond),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_retry",
			Session:   Session{ID: "dbagent_r"},
			Tool:      &ToolExecution{Name: "cancel_query"},
			Outcome:   &Outcome{Status: "success"},
		},
		// Two tool_retry re-check events — these should not change Outcome to "retrying".
		{
			EventID:   "rty_retry1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolRetry,
			TraceID:   "tr_retry",
			Session:   Session{ID: "dbagent_r"},
			Tool:      &ToolExecution{Name: "cancel_query"},
			Outcome:   &Outcome{Status: "retrying"},
		},
		{
			EventID:   "rty_retry2",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeToolRetry,
			TraceID:   "tr_retry",
			Session:   Session{ID: "dbagent_r"},
			Tool:      &ToolExecution{Name: "cancel_query"},
			Outcome:   &Outcome{Status: "resolved"},
		},
	}

	for _, e := range events {
		if err := store.Record(ctx, e); err != nil {
			t.Fatalf("record event %s: %v", e.EventID, err)
		}
	}

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys() = %d journeys, want 1", len(journeys))
	}

	j := journeys[0]
	if j.RetryCount != 2 {
		t.Errorf("RetryCount = %d, want 2", j.RetryCount)
	}
	// tool_retry events must NOT flip the outcome from success to "retrying".
	if j.Outcome != "success" {
		t.Errorf("Outcome = %q, want success (retry events must not corrupt outcome)", j.Outcome)
	}
	// EventCount includes the tool_retry events.
	if j.EventCount != 4 {
		t.Errorf("EventCount = %d, want 4", j.EventCount)
	}
}

// TestQueryJourneys_RetryCountZeroOmitted verifies that a journey with no tool_retry
// events has RetryCount == 0 (which is JSON-omitted via omitempty).
func TestQueryJourneys_RetryCountZeroOmitted(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "audit_retry_zero_test")
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

	events := []*Event{
		{
			EventID:   "del_noretry",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_noretry",
			Session:   Session{ID: "sess_n", UserID: "dave"},
			Input:     Input{UserQuery: "get pods"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		{
			EventID:   "tool_noretry",
			Timestamp: base.Add(500 * time.Millisecond),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_noretry",
			Session:   Session{ID: "k8sagent_n"},
			Tool:      &ToolExecution{Name: "get_pods"},
			Outcome:   &Outcome{Status: "success"},
		},
	}

	for _, e := range events {
		if err := store.Record(ctx, e); err != nil {
			t.Fatalf("record event %s: %v", e.EventID, err)
		}
	}

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys() = %d journeys, want 1", len(journeys))
	}
	if journeys[0].RetryCount != 0 {
		t.Errorf("RetryCount = %d, want 0 for journey with no retries", journeys[0].RetryCount)
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

// newJourneyStore is a test helper that creates a temp SQLite store and registers
// a cleanup to close it.
// TestQueryJourneys_UnverifiedClaimOutcome verifies that a delegation_verification
// event with Mismatch=true elevates the journey outcome to "unverified_claim".
func TestQueryJourneys_UnverifiedClaimOutcome(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_uc1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_uc1",
			Session:   Session{ID: "sess_uc1", UserID: "alice"},
			Input:     Input{UserQuery: "terminate connection pid 5292"},
			Decision:  &Decision{Agent: "postgres_database_agent"},
		},
		{
			EventID:   "tool_uc1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_uc1",
			Session:   Session{ID: "dbagent_uc"},
			Tool:      &ToolExecution{Name: "get_session_info", Agent: "postgres_database_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		// delegation_verification: destructive delegation but no destructive tool called.
		{
			EventID:   "dv_uc1",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeDelegationVerification,
			TraceID:   "tr_uc1",
			Session:   Session{ID: "sess_uc1"},
			DelegationVerification: &DelegationVerification{
				DelegationEventID:    "del_uc1",
				Agent:                "postgres_database_agent",
				ToolsConfirmed:       []string{"get_session_info"},
				DestructiveConfirmed: nil,
				Mismatch:             true,
			},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("got %d journeys, want 1", len(journeys))
	}
	if journeys[0].Outcome != "unverified_claim" {
		t.Errorf("Outcome = %q, want unverified_claim", journeys[0].Outcome)
	}
}

// TestQueryJourneys_DelegationVerification_NotInToolsUsedOrEventCount verifies that
// delegation_verification events do not inflate tools_used or event_count.
func TestQueryJourneys_DelegationVerification_NotInToolsUsedOrEventCount(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_dv2",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_dv2",
			Session:   Session{ID: "sess_dv2", UserID: "bob"},
			Input:     Input{UserQuery: "terminate connection"},
			Decision:  &Decision{Agent: "postgres_database_agent"},
		},
		{
			EventID:   "tool_dv2",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_dv2",
			Session:   Session{ID: "dbagent_dv2"},
			Tool:      &ToolExecution{Name: "get_session_info", Agent: "postgres_database_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		{
			EventID:   "dv_dv2",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeDelegationVerification,
			TraceID:   "tr_dv2",
			Session:   Session{ID: "sess_dv2"},
			DelegationVerification: &DelegationVerification{
				DelegationEventID: "del_dv2",
				Agent:             "postgres_database_agent",
				ToolsConfirmed:    []string{"get_session_info"},
				Mismatch:          true,
			},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("got %d journeys, want 1", len(journeys))
	}
	// delegation_decision + tool_execution = 2; delegation_verification excluded.
	if journeys[0].EventCount != 2 {
		t.Errorf("EventCount = %d, want 2 (delegation_verification must not be counted)", journeys[0].EventCount)
	}
	// tools_used must only contain get_session_info, not anything from the verification event.
	if len(journeys[0].ToolsUsed) != 1 || journeys[0].ToolsUsed[0] != "get_session_info" {
		t.Errorf("ToolsUsed = %v, want [get_session_info]", journeys[0].ToolsUsed)
	}
}

// TestQueryJourneys_UnverifiedClaimWinsOverError verifies that "unverified_claim"
// (priority 9) beats "error" (priority 8) when both appear in the same trace.
func TestQueryJourneys_UnverifiedClaimWinsOverError(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_uw1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_uw1",
			Session:   Session{ID: "sess_uw1", UserID: "carol"},
			Input:     Input{UserQuery: "terminate connection"},
			Decision:  &Decision{Agent: "postgres_database_agent"},
		},
		// A tool that errors.
		{
			EventID:   "tool_uw1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_uw1",
			Session:   Session{ID: "dbagent_uw"},
			Tool:      &ToolExecution{Name: "check_connection", Agent: "postgres_database_agent"},
			Outcome:   &Outcome{Status: "error", ErrorMessage: "connection refused"},
		},
		// delegation_verification: mismatch → unverified_claim must win over error.
		{
			EventID:   "dv_uw1",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeDelegationVerification,
			TraceID:   "tr_uw1",
			Session:   Session{ID: "sess_uw1"},
			DelegationVerification: &DelegationVerification{
				DelegationEventID: "del_uw1",
				Agent:             "postgres_database_agent",
				Mismatch:          true,
			},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("got %d journeys, want 1", len(journeys))
	}
	if journeys[0].Outcome != "unverified_claim" {
		t.Errorf("Outcome = %q, want unverified_claim (must beat error)", journeys[0].Outcome)
	}
}

// TestQueryJourneys_CleanVerification_DoesNotOverrideSuccess verifies that a
// delegation_verification with Mismatch=false ("verified") does not replace a
// "success" outcome — clean verifications are neutral.
func TestQueryJourneys_CleanVerification_DoesNotOverrideSuccess(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_cv1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_cv1",
			Session:   Session{ID: "sess_cv1", UserID: "dave"},
			Input:     Input{UserQuery: "terminate connection"},
			Decision:  &Decision{Agent: "postgres_database_agent"},
		},
		{
			EventID:   "tool_cv1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_cv1",
			Session:   Session{ID: "dbagent_cv"},
			Tool:      &ToolExecution{Name: "terminate_connection", Agent: "postgres_database_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		// Clean verification — Mismatch=false → outcome_status="verified" (priority 0).
		{
			EventID:   "dv_cv1",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeDelegationVerification,
			TraceID:   "tr_cv1",
			Session:   Session{ID: "sess_cv1"},
			DelegationVerification: &DelegationVerification{
				DelegationEventID:    "del_cv1",
				Agent:                "postgres_database_agent",
				ToolsConfirmed:       []string{"terminate_connection"},
				DestructiveConfirmed: []string{"terminate_connection"},
				Mismatch:             false,
			},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("got %d journeys, want 1", len(journeys))
	}
	if journeys[0].Outcome != "success" {
		t.Errorf("Outcome = %q, want success (clean verification must not overwrite success)", journeys[0].Outcome)
	}
}

func newJourneyStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(StoreConfig{DBPath: filepath.Join(t.TempDir(), "audit.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// recordAll records all events and fatally fails the test on any error.
func recordAll(t *testing.T, store *Store, events []*Event) {
	t.Helper()
	for _, e := range events {
		if err := store.Record(context.Background(), e); err != nil {
			t.Fatalf("record event %s: %v", e.EventID, err)
		}
	}
}

// TestQueryJourneys_VerifiedWarningOutcome verifies that a verification_outcome event
// with outcome_status="verified_warning" overrides the journey outcome from "success".
func TestQueryJourneys_VerifiedWarningOutcome(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_vw1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_vw",
			Session:   Session{ID: "sess_vw", UserID: "alice"},
			Input:     Input{UserQuery: "delete the stuck pod"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		{
			EventID:   "tool_vw1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_vw",
			Session:   Session{ID: "k8sagent_vw"},
			Tool:      &ToolExecution{Name: "delete_pod", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		{
			EventID:   "vfy_vw1",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeVerificationOutcome,
			TraceID:   "tr_vw",
			Session:   Session{ID: "k8sagent_vw"},
			Tool:      &ToolExecution{Name: "delete_pod", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "verified_warning"},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys() = %d, want 1", len(journeys))
	}
	if journeys[0].Outcome != "verified_warning" {
		t.Errorf("Outcome = %q, want verified_warning (verification_outcome must override success)", journeys[0].Outcome)
	}
}

// TestQueryJourneys_OutcomePriority_ErrorWins verifies that "error" beats
// "verified_warning" in the priority ordering.
func TestQueryJourneys_OutcomePriority_ErrorWins(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_pw1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_pw",
			Session:   Session{ID: "sess_pw", UserID: "bob"},
			Input:     Input{UserQuery: "restart and check"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		// First tool succeeds but verification is warning.
		{
			EventID:   "tool_pw1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_pw",
			Session:   Session{ID: "k8sagent_pw"},
			Tool:      &ToolExecution{Name: "restart_deployment", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		{
			EventID:   "vfy_pw1",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeVerificationOutcome,
			TraceID:   "tr_pw",
			Session:   Session{ID: "k8sagent_pw"},
			Tool:      &ToolExecution{Name: "restart_deployment", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "verified_warning"},
		},
		// Second tool errors — error(6) > verified_warning(2).
		{
			EventID:   "tool_pw2",
			Timestamp: base.Add(3 * time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_pw",
			Session:   Session{ID: "k8sagent_pw"},
			Tool:      &ToolExecution{Name: "get_pods", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "error", ErrorMessage: "connection refused"},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys() = %d, want 1", len(journeys))
	}
	if journeys[0].Outcome != "error" {
		t.Errorf("Outcome = %q, want error (error must beat verified_warning)", journeys[0].Outcome)
	}
}

// TestQueryJourneys_ToolsUsedOrdered_WithRepeats verifies that the same tool
// appearing twice is listed twice, in insertion order.
func TestQueryJourneys_ToolsUsedOrdered_WithRepeats(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_rep1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_rep",
			Session:   Session{ID: "sess_rep", UserID: "carol"},
			Input:     Input{UserQuery: "check session then cancel then check again"},
			Decision:  &Decision{Agent: "postgres_database_agent"},
		},
		// get_session_info called first
		{
			EventID:   "tool_rep1",
			Timestamp: base.Add(500 * time.Millisecond),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_rep",
			Session:   Session{ID: "dbagent_rep"},
			Tool:      &ToolExecution{Name: "get_session_info", Agent: "postgres_database_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		// cancel_query called
		{
			EventID:   "tool_rep2",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_rep",
			Session:   Session{ID: "dbagent_rep"},
			Tool:      &ToolExecution{Name: "cancel_query", Agent: "postgres_database_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		// get_session_info called again to confirm
		{
			EventID:   "tool_rep3",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_rep",
			Session:   Session{ID: "dbagent_rep"},
			Tool:      &ToolExecution{Name: "get_session_info", Agent: "postgres_database_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys() = %d, want 1", len(journeys))
	}
	want := []string{"get_session_info", "cancel_query", "get_session_info"}
	got := journeys[0].ToolsUsed
	if len(got) != len(want) {
		t.Fatalf("ToolsUsed = %v, want %v (repeats must be preserved)", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("ToolsUsed[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestQueryJourneys_CategoryPopulated verifies that the decision_category from a
// delegation_decision event is surfaced in JourneySummary.Category.
func TestQueryJourneys_CategoryPopulated(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_cat1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_cat",
			Session:   Session{ID: "sess_cat", UserID: "dave"},
			Input:     Input{UserQuery: "check database connections"},
			Decision:  &Decision{Agent: "postgres_database_agent", RequestCategory: CategoryDatabase},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys() = %d, want 1", len(journeys))
	}
	if journeys[0].Category != "database" {
		t.Errorf("Category = %q, want database", journeys[0].Category)
	}
}

// TestQueryJourneys_VerificationOutcome_NotInToolsUsed verifies that a
// verification_outcome event does NOT appear in ToolsUsed.
func TestQueryJourneys_VerificationOutcome_NotInToolsUsed(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_nt1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_nt",
			Session:   Session{ID: "sess_nt", UserID: "eve"},
			Input:     Input{UserQuery: "delete pod"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		{
			EventID:   "tool_nt1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_nt",
			Session:   Session{ID: "k8sagent_nt"},
			Tool:      &ToolExecution{Name: "delete_pod", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		{
			EventID:   "vfy_nt1",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeVerificationOutcome,
			TraceID:   "tr_nt",
			Session:   Session{ID: "k8sagent_nt"},
			Tool:      &ToolExecution{Name: "delete_pod", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "verified_warning"},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys() = %d, want 1", len(journeys))
	}
	// Only "delete_pod" (from tool_execution) should appear; the verification_outcome
	// event also has tool_name="delete_pod" but must not add another entry.
	if len(journeys[0].ToolsUsed) != 1 {
		t.Errorf("ToolsUsed = %v, want [delete_pod] (verification_outcome must not add to tools_used)", journeys[0].ToolsUsed)
	}
}

// TestQueryJourneys_VerificationOutcome_NotInEventCount verifies that a
// verification_outcome event does NOT increment EventCount.
func TestQueryJourneys_VerificationOutcome_NotInEventCount(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_nec1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_nec",
			Session:   Session{ID: "sess_nec", UserID: "frank"},
			Input:     Input{UserQuery: "delete pod"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		{
			EventID:   "tool_nec1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_nec",
			Session:   Session{ID: "k8sagent_nec"},
			Tool:      &ToolExecution{Name: "delete_pod", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		// This verification_outcome event must NOT count toward EventCount.
		{
			EventID:   "vfy_nec1",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeVerificationOutcome,
			TraceID:   "tr_nec",
			Session:   Session{ID: "k8sagent_nec"},
			Tool:      &ToolExecution{Name: "delete_pod", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "verified_warning"},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys() = %d, want 1", len(journeys))
	}
	// 1 delegation_decision + 1 tool_execution = 2; verification_outcome excluded.
	if journeys[0].EventCount != 2 {
		t.Errorf("EventCount = %d, want 2 (verification_outcome must not increment event_count)", journeys[0].EventCount)
	}
}

// TestQueryJourneys_FilterBySince verifies that opts.Since excludes journeys
// whose anchor event is older than Since.
func TestQueryJourneys_FilterBySince(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()

	oldBase := time.Now().UTC().Add(-2 * time.Hour)
	newBase := time.Now().UTC()

	recordAll(t, store, []*Event{
		// Old journey — anchor 2 hours ago.
		{
			EventID:   "del_since_old",
			Timestamp: oldBase,
			EventType: EventTypeDelegation,
			TraceID:   "tr_since_old",
			Session:   Session{ID: "sess_so", UserID: "grace"},
			Input:     Input{UserQuery: "old request"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		// New journey — anchor just now.
		{
			EventID:   "del_since_new",
			Timestamp: newBase,
			EventType: EventTypeDelegation,
			TraceID:   "tr_since_new",
			Session:   Session{ID: "sess_sn", UserID: "grace"},
			Input:     Input{UserQuery: "new request"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
	})

	// Query with Since=5 minutes — only the new journey should be returned.
	journeys, err := store.QueryJourneys(ctx, JourneyOptions{Since: 5 * time.Minute})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys(Since=5m) = %d, want 1 (old journey must be excluded)", len(journeys))
	}
	if journeys[0].TraceID != "tr_since_new" {
		t.Errorf("TraceID = %q, want tr_since_new", journeys[0].TraceID)
	}
}

// TestQueryJourneys_FilterByCategory verifies that opts.Category post-filters
// journeys by their decision_category.
func TestQueryJourneys_FilterByCategory(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		// Database journey.
		{
			EventID:   "del_fc_db",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_fc_db",
			Session:   Session{ID: "sess_fc1", UserID: "henry"},
			Input:     Input{UserQuery: "check replication lag"},
			Decision:  &Decision{Agent: "postgres_database_agent", RequestCategory: CategoryDatabase},
		},
		// Kubernetes journey.
		{
			EventID:   "del_fc_k8s",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeDelegation,
			TraceID:   "tr_fc_k8s",
			Session:   Session{ID: "sess_fc2", UserID: "henry"},
			Input:     Input{UserQuery: "list unhealthy pods"},
			Decision:  &Decision{Agent: "k8s_agent", RequestCategory: CategoryKubernetes},
		},
	})

	// Only database journeys.
	journeys, err := store.QueryJourneys(ctx, JourneyOptions{Category: "database"})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys(Category=database) = %d, want 1", len(journeys))
	}
	if journeys[0].TraceID != "tr_fc_db" {
		t.Errorf("TraceID = %q, want tr_fc_db", journeys[0].TraceID)
	}
}

// TestQueryJourneys_FilterByOutcome verifies that opts.Outcome post-filters
// journeys by their computed outcome.
func TestQueryJourneys_FilterByOutcome(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		// Journey 1: ends with verified_warning (via verification_outcome event).
		{
			EventID:   "del_fo1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_fo1",
			Session:   Session{ID: "sess_fo1", UserID: "iris"},
			Input:     Input{UserQuery: "delete stuck pod"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		{
			EventID:   "tool_fo1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_fo1",
			Session:   Session{ID: "k8sagent_fo"},
			Tool:      &ToolExecution{Name: "delete_pod", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		{
			EventID:   "vfy_fo1",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeVerificationOutcome,
			TraceID:   "tr_fo1",
			Session:   Session{ID: "k8sagent_fo"},
			Tool:      &ToolExecution{Name: "delete_pod", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "verified_warning"},
		},
		// Journey 2: clean success.
		{
			EventID:   "del_fo2",
			Timestamp: base.Add(3 * time.Second),
			EventType: EventTypeDelegation,
			TraceID:   "tr_fo2",
			Session:   Session{ID: "sess_fo2", UserID: "iris"},
			Input:     Input{UserQuery: "get pods"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		{
			EventID:   "tool_fo2",
			Timestamp: base.Add(4 * time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_fo2",
			Session:   Session{ID: "k8sagent_fo"},
			Tool:      &ToolExecution{Name: "get_pods", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{Outcome: "verified_warning"})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys(Outcome=verified_warning) = %d, want 1", len(journeys))
	}
	if journeys[0].TraceID != "tr_fo1" {
		t.Errorf("TraceID = %q, want tr_fo1", journeys[0].TraceID)
	}
}

// TestQueryJourneys_FilterByHasRetries verifies that opts.HasRetries=true excludes
// journeys with RetryCount == 0.
func TestQueryJourneys_FilterByHasRetries(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		// Journey 1: has retries.
		{
			EventID:   "del_hr1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_hr1",
			Session:   Session{ID: "sess_hr1", UserID: "jack"},
			Input:     Input{UserQuery: "cancel long query"},
			Decision:  &Decision{Agent: "postgres_database_agent"},
		},
		{
			EventID:   "tool_hr1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_hr1",
			Session:   Session{ID: "dbagent_hr"},
			Tool:      &ToolExecution{Name: "cancel_query", Agent: "postgres_database_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		{
			EventID:   "rty_hr1",
			Timestamp: base.Add(2 * time.Second),
			EventType: EventTypeToolRetry,
			TraceID:   "tr_hr1",
			Session:   Session{ID: "dbagent_hr"},
			Tool:      &ToolExecution{Name: "cancel_query", Agent: "postgres_database_agent"},
			Outcome:   &Outcome{Status: "resolved"},
		},
		// Journey 2: no retries.
		{
			EventID:   "del_hr2",
			Timestamp: base.Add(3 * time.Second),
			EventType: EventTypeDelegation,
			TraceID:   "tr_hr2",
			Session:   Session{ID: "sess_hr2", UserID: "jack"},
			Input:     Input{UserQuery: "get pods"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		{
			EventID:   "tool_hr2",
			Timestamp: base.Add(4 * time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_hr2",
			Session:   Session{ID: "k8sagent_hr"},
			Tool:      &ToolExecution{Name: "get_pods", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{HasRetries: true})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("QueryJourneys(HasRetries=true) = %d, want 1", len(journeys))
	}
	if journeys[0].TraceID != "tr_hr1" {
		t.Errorf("TraceID = %q, want tr_hr1", journeys[0].TraceID)
	}
	if journeys[0].RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", journeys[0].RetryCount)
	}
}

// TestQueryJourneys_DeniedOutcome verifies that a policy_decision with
// Effect="deny" surfaces as outcome="denied" in the journey summary.
func TestQueryJourneys_DeniedOutcome(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_dn1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_dn1",
			Session:   Session{ID: "sess_dn1", UserID: "alice"},
			Input:     Input{UserQuery: "drop production table"},
			Decision:  &Decision{Agent: "postgres_database_agent"},
		},
		{
			EventID:   "pol_dn1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypePolicyDecision,
			TraceID:   "tr_dn1",
			Session:   Session{ID: "dbagent_dn"},
			PolicyDecision: &PolicyDecision{
				ResourceType: "database",
				ResourceName: "prod-db",
				Action:       "destructive",
				Effect:       "deny",
				PolicyName:   "no-drops",
			},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("got %d journeys, want 1", len(journeys))
	}
	if journeys[0].Outcome != "denied" {
		t.Errorf("Outcome = %q, want denied", journeys[0].Outcome)
	}
}

// TestQueryJourneys_ApprovedOutcome verifies that a require_approval policy decision
// followed by a successful tool execution produces outcome="approved".
func TestQueryJourneys_ApprovedOutcome(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_ap1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_ap1",
			Session:   Session{ID: "sess_ap1", UserID: "bob"},
			Input:     Input{UserQuery: "restart the payment deployment"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		{
			EventID:   "pol_ap1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypePolicyDecision,
			TraceID:   "tr_ap1",
			Session:   Session{ID: "k8sagent_ap"},
			PolicyDecision: &PolicyDecision{
				ResourceType: "kubernetes",
				ResourceName: "payment",
				Action:       "write",
				Effect:       "require_approval",
				PolicyName:   "prod-writes-need-approval",
			},
		},
		{
			EventID:   "tool_ap1",
			Timestamp: base.Add(5 * time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_ap1",
			Session:   Session{ID: "k8sagent_ap"},
			Tool:      &ToolExecution{Name: "restart_deployment", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("got %d journeys, want 1", len(journeys))
	}
	if journeys[0].Outcome != "approved" {
		t.Errorf("Outcome = %q, want approved", journeys[0].Outcome)
	}
}

// TestQueryJourneys_VerifiedOkOutcome verifies that a verification_outcome event
// with outcome_status="verified_ok" produces outcome="verified_ok" in the journey,
// which is distinct from a plain "success" (no verification loop).
func TestQueryJourneys_VerifiedOkOutcome(t *testing.T) {
	store := newJourneyStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	recordAll(t, store, []*Event{
		{
			EventID:   "del_vok1",
			Timestamp: base,
			EventType: EventTypeDelegation,
			TraceID:   "tr_vok1",
			Session:   Session{ID: "sess_vok1", UserID: "carol"},
			Input:     Input{UserQuery: "delete the stuck pod"},
			Decision:  &Decision{Agent: "k8s_agent"},
		},
		{
			EventID:   "tool_vok1",
			Timestamp: base.Add(time.Second),
			EventType: EventTypeToolExecution,
			TraceID:   "tr_vok1",
			Session:   Session{ID: "k8sagent_vok"},
			Tool:      &ToolExecution{Name: "delete_pod", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "success"},
		},
		{
			EventID:   "vfy_vok1",
			Timestamp: base.Add(3 * time.Second),
			EventType: EventTypeVerificationOutcome,
			TraceID:   "tr_vok1",
			Session:   Session{ID: "k8sagent_vok"},
			Tool:      &ToolExecution{Name: "delete_pod", Agent: "k8s_agent"},
			Outcome:   &Outcome{Status: "verified_ok"},
		},
	})

	journeys, err := store.QueryJourneys(ctx, JourneyOptions{})
	if err != nil {
		t.Fatalf("QueryJourneys: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("got %d journeys, want 1", len(journeys))
	}
	if journeys[0].Outcome != "verified_ok" {
		t.Errorf("Outcome = %q, want verified_ok", journeys[0].Outcome)
	}
	// verification_outcome events must not inflate event_count or tools_used.
	if journeys[0].EventCount != 2 {
		t.Errorf("EventCount = %d, want 2 (delegation + tool_execution only)", journeys[0].EventCount)
	}
	if len(journeys[0].ToolsUsed) != 1 || journeys[0].ToolsUsed[0] != "delete_pod" {
		t.Errorf("ToolsUsed = %v, want [delete_pod]", journeys[0].ToolsUsed)
	}
}
