package audit

import (
	"context"
	"os"
	"path/filepath"
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
