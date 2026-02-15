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
