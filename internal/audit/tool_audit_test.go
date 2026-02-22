package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// newToolAuditTestStore creates a temporary SQLite-backed Store for use in tests.
func newToolAuditTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestRecordAgentReasoning_NilAuditor(t *testing.T) {
	ta := NewToolAuditor(nil, "test-agent", "sess-1", "trace-1")
	// Should be a no-op and not panic.
	ta.RecordAgentReasoning(context.Background(), "I will call get_pods", []string{"get_pods"})
}

func TestRecordAgentReasoning_EmptyReasoning(t *testing.T) {
	store := newToolAuditTestStore(t)
	ta := NewToolAuditor(store, "test-agent", "sess-1", "trace-1")
	// Empty reasoning string → no event recorded (no-op).
	ta.RecordAgentReasoning(context.Background(), "", []string{"get_pods"})

	events, err := store.Query(context.Background(), QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events for empty reasoning, got %d", len(events))
	}
}

func TestRecordAgentReasoning_RecordsEvent(t *testing.T) {
	store := newToolAuditTestStore(t)
	ta := NewToolAuditor(store, "db-agent", "sess-rsn", "trace-rsn-42")

	ta.RecordAgentReasoning(
		context.Background(),
		"The user wants connection stats. I'll call get_active_connections first.",
		[]string{"get_active_connections", "get_connection_stats"},
	)

	events, err := store.Query(context.Background(), QueryOptions{EventType: EventTypeAgentReasoning})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 agent_reasoning event, got %d", len(events))
	}

	evt := events[0]
	if evt.EventType != EventTypeAgentReasoning {
		t.Errorf("EventType = %q, want %q", evt.EventType, EventTypeAgentReasoning)
	}
	if evt.TraceID != "trace-rsn-42" {
		t.Errorf("TraceID = %q, want %q", evt.TraceID, "trace-rsn-42")
	}
	if evt.AgentReasoning == nil {
		t.Fatal("AgentReasoning field is nil")
	}
	if evt.AgentReasoning.Reasoning == "" {
		t.Error("Reasoning is empty")
	}
	if len(evt.AgentReasoning.ToolCalls) != 2 {
		t.Errorf("ToolCalls = %v, want 2 entries", evt.AgentReasoning.ToolCalls)
	}
	if evt.AgentReasoning.ToolCalls[0] != "get_active_connections" {
		t.Errorf("ToolCalls[0] = %q, want get_active_connections", evt.AgentReasoning.ToolCalls[0])
	}
}

func TestRecordAgentReasoning_EventIDHasRsnPrefix(t *testing.T) {
	store := newToolAuditTestStore(t)
	ta := NewToolAuditor(store, "k8s-agent", "sess-k8s", "")

	ta.RecordAgentReasoning(context.Background(), "Checking pods.", []string{"get_pods"})

	events, err := store.Query(context.Background(), QueryOptions{EventType: EventTypeAgentReasoning})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events recorded")
	}
	if len(events[0].EventID) < 4 || events[0].EventID[:4] != "rsn_" {
		t.Errorf("EventID = %q, want rsn_ prefix", events[0].EventID)
	}
}

func TestRecordAgentReasoning_TimestampIsRecent(t *testing.T) {
	store := newToolAuditTestStore(t)
	ta := NewToolAuditor(store, "agent", "sess", "")
	before := time.Now().UTC()
	ta.RecordAgentReasoning(context.Background(), "deliberation text", []string{"some_tool"})
	after := time.Now().UTC()

	events, err := store.Query(context.Background(), QueryOptions{EventType: EventTypeAgentReasoning})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	ts := events[0].Timestamp
	if ts.Before(before) || ts.After(after) {
		t.Errorf("Timestamp %v not between %v and %v", ts, before, after)
	}
}

func TestRecordAgentReasoning_DynamicTraceID(t *testing.T) {
	store := newToolAuditTestStore(t)
	traceStore := &CurrentTraceStore{}
	traceStore.Set("dynamic-trace-xyz")
	ta := NewToolAuditorWithTraceStore(store, "agent", "sess", traceStore)

	ta.RecordAgentReasoning(context.Background(), "I need to call get_nodes.", []string{"get_nodes"})

	events, err := store.Query(context.Background(), QueryOptions{EventType: EventTypeAgentReasoning})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	if events[0].TraceID != "dynamic-trace-xyz" {
		t.Errorf("TraceID = %q, want dynamic-trace-xyz", events[0].TraceID)
	}
}
