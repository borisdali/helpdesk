package audit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGatewayAuditor_RecordRequest(t *testing.T) {
	// Create temp directory for test database.
	tmpDir, err := os.MkdirTemp("", "audit-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "audit.db")

	store, err := NewStore(StoreConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	auditor := NewGatewayAuditor(store)

	// Record a request.
	req := &GatewayRequest{
		RequestID: "test-123",
		Endpoint:  "/api/v1/research",
		Method:    "POST",
		Agent:     "research_agent",
		Message:   "What is PostgreSQL?",
		Response:  "PostgreSQL is an open-source relational database.",
		StartTime: time.Now(),
		Duration:  150 * time.Millisecond,
		Status:    "success",
		HTTPCode:  200,
	}

	if err := auditor.RecordRequest(context.Background(), req); err != nil {
		t.Fatalf("record request: %v", err)
	}

	// Query to verify it was recorded.
	events, err := store.Query(context.Background(), QueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("query events: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	event := events[0]
	if event.EventType != EventTypeGatewayRequest {
		t.Errorf("event_type = %q, want %q", event.EventType, EventTypeGatewayRequest)
	}
	if event.Decision.Agent != "research_agent" {
		t.Errorf("agent = %q, want %q", event.Decision.Agent, "research_agent")
	}
	if event.Decision.RequestCategory != CategoryResearch {
		t.Errorf("category = %q, want %q", event.Decision.RequestCategory, CategoryResearch)
	}
	if event.Outcome.Status != "success" {
		t.Errorf("status = %q, want %q", event.Outcome.Status, "success")
	}
	if event.Output == nil || event.Output.Response == "" {
		t.Error("response not recorded")
	}
	if event.Output.Response != "PostgreSQL is an open-source relational database." {
		t.Errorf("response = %q, want PostgreSQL description", event.Output.Response)
	}
}

func TestGatewayAuditor_NilStore(t *testing.T) {
	// With nil store, recording should be a no-op.
	auditor := NewGatewayAuditor(nil)

	req := &GatewayRequest{
		RequestID: "test-456",
		Endpoint:  "/api/v1/db/check_connection",
		Method:    "POST",
		Agent:     "postgres_database_agent",
		StartTime: time.Now(),
		Duration:  50 * time.Millisecond,
		Status:    "success",
	}

	// Should not panic or error.
	if err := auditor.RecordRequest(context.Background(), req); err != nil {
		t.Errorf("unexpected error with nil store: %v", err)
	}
}

func TestCategorizeAgent(t *testing.T) {
	tests := []struct {
		agent    string
		expected RequestCategory
	}{
		{"postgres_database_agent", CategoryDatabase},
		{"k8s_agent", CategoryKubernetes},
		{"incident_agent", CategoryIncident},
		{"research_agent", CategoryResearch},
		{"unknown_agent", CategoryUnknown},
		{"", CategoryUnknown},
	}

	for _, tc := range tests {
		got := categorizeAgent(tc.agent)
		if got != tc.expected {
			t.Errorf("categorizeAgent(%q) = %q, want %q", tc.agent, got, tc.expected)
		}
	}
}

func TestGatewayAuditor_ToolExecution(t *testing.T) {
	// Create temp directory for test database.
	tmpDir, err := os.MkdirTemp("", "audit-tool-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "audit.db")

	store, err := NewStore(StoreConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	auditor := NewGatewayAuditor(store)

	// Record a request with tool execution details
	req := &GatewayRequest{
		RequestID: "test-tool-1",
		TraceID:   NewTraceID(),
		Endpoint:  "/api/v1/db/check_connection",
		Agent:     "postgres_database_agent",
		ToolName:  "check_connection",
		ToolParameters: map[string]any{
			"connection_string": "host=localhost dbname=test",
			"timeout":           30,
		},
		Message:   "Check database connection",
		Response:  "Connection successful. PostgreSQL 15.2",
		StartTime: time.Now(),
		Duration:  250 * time.Millisecond,
		Status:    "success",
	}

	if err := auditor.RecordRequest(context.Background(), req); err != nil {
		t.Fatalf("record request: %v", err)
	}

	// Query and verify tool details
	events, err := store.Query(context.Background(), QueryOptions{ToolName: "check_connection"})
	if err != nil {
		t.Fatalf("query by tool: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	event := events[0]
	if event.Tool == nil {
		t.Fatal("expected tool execution, got nil")
	}
	if event.Tool.Name != "check_connection" {
		t.Errorf("tool name = %q, want %q", event.Tool.Name, "check_connection")
	}
	if event.Tool.Parameters["timeout"] != float64(30) { // JSON numbers are float64
		t.Errorf("tool param timeout = %v, want 30", event.Tool.Parameters["timeout"])
	}
	if event.ActionClass != ActionRead {
		t.Errorf("action class = %q, want %q", event.ActionClass, ActionRead)
	}
}

func TestGatewayAuditor_TraceID(t *testing.T) {
	// Create temp directory for test database.
	tmpDir, err := os.MkdirTemp("", "audit-trace-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "audit.db")

	store, err := NewStore(StoreConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	auditor := NewGatewayAuditor(store)
	traceID := NewTraceID()

	// Record two requests with same trace ID
	req1 := &GatewayRequest{
		RequestID: "req-1",
		TraceID:   traceID,
		Endpoint:  "/api/v1/db/check_connection",
		Agent:     "postgres_database_agent",
		Message:   "Check connection",
		Response:  "Connected",
		StartTime: time.Now(),
		Duration:  100 * time.Millisecond,
		Status:    "success",
	}

	req2 := &GatewayRequest{
		RequestID: "req-2",
		TraceID:   traceID,
		ParentID:  "gw_req-1", // child of first request
		Endpoint:  "/api/v1/db/run_query",
		Agent:     "postgres_database_agent",
		Message:   "SELECT 1",
		Response:  "1",
		StartTime: time.Now().Add(100 * time.Millisecond),
		Duration:  50 * time.Millisecond,
		Status:    "success",
	}

	if err := auditor.RecordRequest(context.Background(), req1); err != nil {
		t.Fatalf("record req1: %v", err)
	}
	if err := auditor.RecordRequest(context.Background(), req2); err != nil {
		t.Fatalf("record req2: %v", err)
	}

	// Query by trace ID
	events, err := store.Query(context.Background(), QueryOptions{TraceID: traceID})
	if err != nil {
		t.Fatalf("query by trace: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events for trace, got %d", len(events))
	}

	// Should be in chronological order
	if events[0].TraceID != traceID {
		t.Errorf("event[0] trace ID = %q, want %q", events[0].TraceID, traceID)
	}
	if events[1].TraceID != traceID {
		t.Errorf("event[1] trace ID = %q, want %q", events[1].TraceID, traceID)
	}
}
