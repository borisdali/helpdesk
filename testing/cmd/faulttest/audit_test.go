package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuditQueryTools_EmptyURL(t *testing.T) {
	result, _ := auditQueryTools(context.Background(), "", "", time.Now())
	if result != nil {
		t.Errorf("expected nil for empty URL, got %v", result)
	}
}

func TestAuditQueryTools_Success(t *testing.T) {
	var gotSince string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSince = r.URL.Query().Get("since")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"event_type": "tool_execution", "tool": {"name": "check_connection"}},
			{"event_type": "tool_execution", "tool": {"name": "get_database_info"}},
			{"event_type": "other_event"}
		]`))
	}))
	defer srv.Close()

	since := time.Now().Add(-time.Minute)
	result, _ := auditQueryTools(context.Background(), srv.URL, "", since)

	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d: %v", len(result), result)
	}
	if result[0] != "check_connection" || result[1] != "get_database_info" {
		t.Errorf("unexpected tools: %v", result)
	}
	// Verify the since parameter was sent in RFC3339 format.
	if gotSince == "" {
		t.Error("since query param was not sent")
	}
	if !strings.Contains(gotSince, "T") {
		t.Errorf("since param %q does not look like RFC3339", gotSince)
	}
}

func TestAuditQueryTools_Deduplication(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"event_type": "tool_execution", "tool": {"name": "check_connection"}},
			{"event_type": "tool_execution", "tool": {"name": "check_connection"}},
			{"event_type": "tool_execution", "tool": {"name": "get_database_info"}}
		]`))
	}))
	defer srv.Close()

	result, _ := auditQueryTools(context.Background(), srv.URL, "", time.Now())
	if len(result) != 2 {
		t.Errorf("expected 2 unique tools, got %d: %v", len(result), result)
	}
}

func TestAuditQueryTools_SkipsEventsWithoutTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"event_type": "tool_execution"},
			{"event_type": "tool_execution", "tool": {}},
			{"event_type": "tool_execution", "tool": {"name": ""}}
		]`))
	}))
	defer srv.Close()

	result, _ := auditQueryTools(context.Background(), srv.URL, "", time.Now())
	if len(result) != 0 {
		t.Errorf("expected empty result when no valid tool names, got %v", result)
	}
}

func TestAuditQueryTools_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	result, _ := auditQueryTools(context.Background(), srv.URL, "", time.Now())
	if result != nil {
		t.Errorf("expected nil on non-200 status, got %v", result)
	}
}

func TestAuditQueryTools_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not valid json`))
	}))
	defer srv.Close()

	result, _ := auditQueryTools(context.Background(), srv.URL, "", time.Now())
	if result != nil {
		t.Errorf("expected nil on invalid JSON, got %v", result)
	}
}

func TestAuditQueryTools_EmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	result, _ := auditQueryTools(context.Background(), srv.URL, "", time.Now())
	// Empty list is nil (no tools found — caller treats nil and empty the same way).
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

// TestAuditQueryTools_CapturesToolErrors is a regression test for the real
// gap found 2026-09-29: get_pgbackrest_status erroring with "pgbackrest:
// command not found" (the agent had been pointed at the wrong target)
// produced no signal in the terminal output at all — the error was only
// visible by reading the full JSON report's response_text after the fact.
// auditQueryTools now decodes each event's own error field, not just its
// name, precisely so a caller can surface this live.
func TestAuditQueryTools_CapturesToolErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"event_type": "tool_execution", "tool": {"name": "check_host"}},
			{"event_type": "tool_execution", "tool": {"name": "get_pgbackrest_status", "error": "pgbackrest: command not found"}}
		]`))
	}))
	defer srv.Close()

	names, toolErrors := auditQueryTools(context.Background(), srv.URL, "", time.Now())

	if len(names) != 2 {
		t.Fatalf("expected 2 tool names, got %d: %v", len(names), names)
	}
	if toolErrors["get_pgbackrest_status"] != "pgbackrest: command not found" {
		t.Errorf("toolErrors[get_pgbackrest_status] = %q, want the recorded error", toolErrors["get_pgbackrest_status"])
	}
	if _, ok := toolErrors["check_host"]; ok {
		t.Errorf("toolErrors should not have an entry for a tool with no error, got %v", toolErrors)
	}
}

func TestAuditQueryTools_NoErrorsWhenNoneRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"event_type": "tool_execution", "tool": {"name": "check_host"}}]`))
	}))
	defer srv.Close()

	_, toolErrors := auditQueryTools(context.Background(), srv.URL, "", time.Now())
	if toolErrors != nil {
		t.Errorf("expected nil toolErrors when no tool recorded an error, got %v", toolErrors)
	}
}

func TestAuditQueryTools_SendsAPIKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	auditQueryTools(context.Background(), srv.URL, "my-audit-key", time.Now())
	if gotAuth != "Bearer my-audit-key" {
		t.Errorf("Authorization = %q, want Bearer my-audit-key", gotAuth)
	}
}

func TestAuditQueryTools_NoAuthWhenKeyEmpty(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	auditQueryTools(context.Background(), srv.URL, "", time.Now())
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty when no key provided", gotAuth)
	}
}

func TestAuditQueryTools_URLConstruction(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	since := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	auditQueryTools(context.Background(), srv.URL, "", since)

	if !strings.HasPrefix(gotURL, "/v1/events?") {
		t.Errorf("URL path = %q, want /v1/events?...", gotURL)
	}
	if !strings.Contains(gotURL, "event_type=tool_execution") {
		t.Errorf("URL %q missing event_type=tool_execution", gotURL)
	}
	if !strings.Contains(gotURL, "2026-04-16") {
		t.Errorf("URL %q missing expected date in since param", gotURL)
	}
}
