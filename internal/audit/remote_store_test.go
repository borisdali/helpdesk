package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestRemoteStore_Query_ForwardsAllSupportedFields proves every QueryOptions
// field that cmd/auditd's handleQueryEvents actually reads from the URL is
// forwarded by RemoteStore.Query — closing a gap where TraceIDPrefix,
// EventTypes, ToolName, ApprovalStatus (N/A — not server-supported, see
// below), OutcomeStatus, Origin, and Limit were silently dropped even though
// the server already honored them, so a caller setting e.g. ToolName got
// unfiltered results back with no error.
func TestRemoteStore_Query_ForwardsAllSupportedFields(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]")) //nolint:errcheck
	}))
	defer srv.Close()

	store := NewRemoteStore(srv.URL)
	since := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	_, err := store.Query(context.Background(), QueryOptions{
		SessionID:     "sess-1",
		TraceID:       "tr-1",
		TraceIDPrefix: "chk_",
		EventType:     EventTypeToolExecution,
		EventTypes:    []EventType{EventTypeToolInvoked, EventTypePolicyDecision},
		Agent:         "k8s_agent",
		ActionClass:   ActionWrite,
		Since:         since,
		ToolName:      "delete_pod",
		OutcomeStatus: "denied",
		Origin:        "direct_tool",
		Limit:         42,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	cases := map[string]string{
		"session_id":      "sess-1",
		"trace_id":        "tr-1",
		"trace_id_prefix": "chk_",
		"agent":           "k8s_agent",
		"action_class":    "write",
		"tool_name":       "delete_pod",
		"outcome_status":  "denied",
		"origin":          "direct_tool",
		"limit":           "42",
	}
	for key, want := range cases {
		got := gotQuery.Get(key)
		if got != want {
			t.Errorf("query param %q = %q, want %q", key, got, want)
		}
	}
	// EventType is only sent when EventTypes (plural) isn't already narrowing
	// the query — the handler treats "types" as taking precedence, so both
	// being sent is harmless, but confirm the singular form is still present.
	if got := gotQuery.Get("event_type"); got != string(EventTypeToolExecution) {
		t.Errorf("event_type = %q, want %q", got, EventTypeToolExecution)
	}
	if got := gotQuery.Get("types"); got != "tool_invoked,policy_decision" {
		t.Errorf("types = %q, want %q", got, "tool_invoked,policy_decision")
	}
	if got := gotQuery.Get("since"); got != since.Format(time.RFC3339) {
		t.Errorf("since = %q, want %q", got, since.Format(time.RFC3339))
	}
}

// TestRemoteStore_Query_OmitsUnsetFields proves an empty QueryOptions sends
// no query string at all — no field is forwarded as its zero value by
// accident (e.g. limit=0 would silently override auditd's own default of
// 100 results).
func TestRemoteStore_Query_OmitsUnsetFields(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]")) //nolint:errcheck
	}))
	defer srv.Close()

	store := NewRemoteStore(srv.URL)
	if _, err := store.Query(context.Background(), QueryOptions{}); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if gotRawQuery != "" {
		t.Errorf("raw query = %q, want empty for a zero-value QueryOptions", gotRawQuery)
	}
}
