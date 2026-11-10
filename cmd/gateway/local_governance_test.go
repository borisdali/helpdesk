package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
)

// localGovernance starts the embedded governance server over a real store holding
// a few events, and returns its URL: the gateway's local audit mode, as run.
func localGovernance(t *testing.T) (string, *audit.Store) {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "audit.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	rec := func(id, trace string, typ audit.EventType, class audit.ActionClass, agent string, at time.Duration) {
		t.Helper()
		ev := &audit.Event{EventID: id, Timestamp: base.Add(at), EventType: typ, TraceID: trace, ActionClass: class,
			Session: audit.Session{ID: "sess_" + trace, AgentName: agent, UserID: "ops@example.com"}, Outcome: &audit.Outcome{Status: "success"}}
		if typ == audit.EventTypeToolExecution {
			ev.Tool = &audit.ToolExecution{Name: "get_status_summary", Agent: agent}
		}
		if err := store.Record(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	rec("ev_a1", "tr_alpha_1", audit.EventTypeGatewayRequest, audit.ActionRead, "gateway", 0)
	rec("ev_a2", "tr_alpha_1", audit.EventTypeToolExecution, audit.ActionRead, "database", time.Second)
	rec("ev_b1", "tr_beta_1", audit.EventTypeGatewayRequest, audit.ActionWrite, "gateway", 10*time.Minute)
	rec("ev_b2", "tr_beta_1", audit.EventTypeToolExecution, audit.ActionDestructive, "k8s", 10*time.Minute+time.Second)

	u, err := startLocalGovernanceServer(store)
	if err != nil {
		t.Fatal(err)
	}
	return u, store
}

func getJSON(t *testing.T, rawURL string, into any) int {
	t.Helper()
	resp, err := http.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, into); err != nil {
			t.Fatalf("%s: %v\n%s", rawURL, err, body)
		}
	}
	return resp.StatusCode
}

func TestLocalGovernance_Info(t *testing.T) {
	u, _ := localGovernance(t)
	var info struct {
		Audit struct {
			Enabled     bool    `json:"enabled"`
			Backend     string  `json:"backend"`
			EventsTotal float64 `json:"events_total"`
			ChainValid  bool    `json:"chain_valid"`
			LastEventAt string  `json:"last_event_at"`
		} `json:"audit"`
		Timestamp string `json:"timestamp"`
	}
	if code := getJSON(t, u+"/v1/governance/info", &info); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !info.Audit.Enabled || info.Audit.Backend != "sqlite" || info.Audit.EventsTotal != 4 || !info.Audit.ChainValid || info.Audit.LastEventAt == "" || info.Timestamp == "" {
		t.Errorf("info = %+v", info)
	}
}

func TestLocalGovernance_VerifyReportsTheChain(t *testing.T) {
	u, _ := localGovernance(t)
	var status struct {
		Valid       bool `json:"valid"`
		TotalEvents int  `json:"total_events"`
	}
	if code := getJSON(t, u+"/v1/verify", &status); code != http.StatusOK || !status.Valid || status.TotalEvents != 4 {
		t.Errorf("%d %+v", code, status)
	}
}

func TestLocalGovernance_EventsAndFilters(t *testing.T) {
	u, _ := localGovernance(t)
	count := func(query string) int {
		t.Helper()
		var evs []audit.Event
		if code := getJSON(t, u+"/v1/events"+query, &evs); code != http.StatusOK {
			t.Fatalf("%s: %d", query, code)
		}
		return len(evs)
	}
	// The events are 60 and 50 minutes old; 55 minutes ago keeps only the later pair.
	since := url.QueryEscape(time.Now().UTC().Add(-55 * time.Minute).Format(time.RFC3339))
	for query, want := range map[string]int{
		"": 4, "?trace_id=tr_alpha_1": 2, "?trace_id_prefix=tr_be": 2, "?session_id=sess_tr_beta_1": 2,
		"?event_type=tool_execution": 2, "?event_type=gateway_request": 2, "?agent=k8s": 1,
		"?action_class=destructive": 1, "?limit=3": 3, "?limit=zero": 4, "?since=" + since: 2,
		"?since=not-a-time": 4, // an unreadable time is ignored
		"?trace_id=none":    0,
	} {
		if got := count(query); got != want {
			t.Errorf("events%s = %d, want %d", query, got, want)
		}
	}
	// Nothing matching is a list, not null.
	resp, _ := http.Get(u + "/v1/events?trace_id=none")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.TrimSpace(string(b)) != "[]" {
		t.Errorf("no match = %q", b)
	}
}

func TestLocalGovernance_OneEvent(t *testing.T) {
	u, _ := localGovernance(t)
	var ev audit.Event
	if code := getJSON(t, u+"/v1/events/ev_b2", &ev); code != http.StatusOK || ev.EventID != "ev_b2" || ev.TraceID != "tr_beta_1" {
		t.Errorf("%d %+v", code, ev)
	}
	if code := getJSON(t, u+"/v1/events/ev_nope", nil); code != http.StatusNotFound {
		t.Errorf("unknown event: %d", code)
	}
}

func TestLocalGovernance_JourneysFilters(t *testing.T) {
	u, _ := localGovernance(t)
	count := func(query string) int {
		t.Helper()
		var js []audit.JourneySummary
		if code := getJSON(t, u+"/v1/journeys"+query, &js); code != http.StatusOK {
			t.Fatalf("%s: %d", query, code)
		}
		return len(js)
	}
	// The two journeys began 60 and 50 minutes ago: 55 minutes ago falls between them.
	between := url.QueryEscape(time.Now().UTC().Add(-55 * time.Minute).Format(time.RFC3339))
	from, until := between, between
	for query, want := range map[string]int{
		"": 2, "?trace_id=tr_alpha_1": 1, "?user=ops@example.com": 2, "?user=nobody": 0, "?limit=1": 1, "?limit=banana": 2,
		"?from=" + from: 1, "?until=" + until: 1, "?from=garbage": 2,
		"?run_id=plr_unknown": 0, // a run that does not exist is no journeys, never every journey
	} {
		if got := count(query); got != want {
			t.Errorf("journeys%s = %d, want %d", query, got, want)
		}
	}
	// ?incident_only keeps the journeys that belong to a playbook run: with no runs
	// in local mode, none, and not an error (the table it looks in must exist).
	var js []audit.JourneySummary
	if code := getJSON(t, u+"/v1/journeys?incident_only=true", &js); code != http.StatusOK || len(js) != 0 {
		t.Errorf("incident_only: %d, %d journeys", code, len(js))
	}
}

// What local mode does not have says so, in the shape the callers expect.
func TestLocalGovernance_WhatIsNotAvailable(t *testing.T) {
	u, _ := localGovernance(t)
	for _, path := range []string{"/v1/approvals", "/v1/approvals/pending"} {
		resp, err := http.Get(u + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(b)) != "[]" {
			t.Errorf("%s: %d %q (approvals are not stored locally: an empty list)", path, resp.StatusCode, b)
		}
	}
	for path, want := range map[string]string{
		"/v1/governance/policies": "policy engine not available in gateway local mode",
		"/v1/governance/explain":  "explain not available in gateway local mode",
	} {
		resp, err := http.Get(u + path)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented || body["error"] != want {
			t.Errorf("%s: %d %v", path, resp.StatusCode, body)
		}
	}
}

// A store that cannot be read is an error, not an empty answer.
func TestLocalGovernance_ADeadStoreIsAnError(t *testing.T) {
	u, store := localGovernance(t)
	store.Close()
	for _, path := range []string{"/v1/events", "/v1/events/ev_a1", "/v1/verify", "/v1/journeys"} {
		if code := getJSON(t, u+path, nil); code != http.StatusInternalServerError {
			t.Errorf("%s with a dead store = %d, want 500", path, code)
		}
	}
}
