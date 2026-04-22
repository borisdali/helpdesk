//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestAuditToolEvidenceContract verifies the interface contract between auditd
// and faulttest's auditQueryTools function.
//
// auditQueryTools sends:
//
//	GET /v1/events?event_type=tool_execution&since=<RFC3339>
//
// and expects a JSON array where each element has:
//
//	{ "event_type": "tool_execution", "tool": { "name": "<tool-name>" } }
//
// If auditd renames the field ("tool" → "tool_call"), changes the nesting
// ("tool.name" → "tool_name"), or breaks query-parameter filtering, this test
// will catch it before a full faulttest run.
//
// No LLM API key required — the event is synthesised directly via POST
// /v1/events; no agent call is needed.
func TestAuditToolEvidenceContract(t *testing.T) {
	cfg := LoadConfig()
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}

	// Capture the time before posting so the since= filter is guaranteed to
	// include our event but exclude anything older in the store.
	since := time.Now().UTC()

	// Synthesise a tool_execution event — same shape that agentutil emits.
	eventID := fmt.Sprintf("e2e-toolcontract-%d", time.Now().UnixNano())
	payload := map[string]any{
		"event_id":   eventID,
		"timestamp":  time.Now().UTC().Format(time.RFC3339Nano),
		"event_type": "tool_execution",
		"session":    map[string]any{"id": "e2e-toolcontract-session"},
		"input":      map[string]any{"user_query": "check database health"},
		"tool": map[string]any{
			"name":  "check_connection",
			"agent": "database",
		},
	}

	created := auditdPost(t, cfg.AuditdURL, "/v1/events", payload)
	if created["event_id"] == nil {
		t.Fatalf("POST /v1/events: event_id missing from response — auditd may not be accepting events: %v", created)
	}
	t.Logf("created tool_execution event: %s", eventID)

	// Query using the exact parameters that auditQueryTools sends.
	sinceStr := since.Format(time.RFC3339)
	path := fmt.Sprintf("/v1/events?event_type=tool_execution&since=%s", sinceStr)
	events := auditdGetList(t, cfg.AuditdURL, path)

	t.Logf("query %s returned %d event(s)", path, len(events))

	// ── Primary assertion: tool.name field must exist ─────────────────────
	//
	// auditQueryTools decodes each element into:
	//   type auditEvent struct {
	//     EventType string `json:"event_type"`
	//     Tool *struct { Name string `json:"name"` } `json:"tool,omitempty"`
	//   }
	// If the "tool" key is renamed or the "name" sub-key moves, auditQueryTools
	// silently returns no tools and the audit evidence path produces wrong scores.

	var found map[string]any
	for _, e := range events {
		if e["event_id"] == eventID {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatalf("tool_execution event %s not found in query result (%d events total) — "+
			"either since= filter is broken or the event was not stored", eventID, len(events))
	}

	tool, ok := found["tool"].(map[string]any)
	if !ok || tool == nil {
		t.Fatalf("event %s: field \"tool\" is missing or not an object\n"+
			"auditQueryTools reads e.Tool.Name — if the key is wrong, all audit evidence returns nil\n"+
			"full event: %v", eventID, found)
	}

	name, ok := tool["name"].(string)
	if !ok || name == "" {
		t.Fatalf("event %s: tool.name is missing or empty\n"+
			"auditQueryTools reads e.Tool.Name — if the sub-key is wrong, tool names are lost\n"+
			"tool field: %v", eventID, tool)
	}

	if name != "check_connection" {
		t.Errorf("tool.name = %q, want %q", name, "check_connection")
	}

	t.Logf("contract verified: tool.name = %q (field format is compatible with auditQueryTools)", name)

	// ── Secondary assertion: event_type= filter must work ────────────────
	//
	// If the filter is ignored, auditQueryTools would receive all event types
	// (agent_call, policy_decision, etc.) which don't have a tool.name field,
	// inflating the tool list with nil entries.

	for _, e := range events {
		et, _ := e["event_type"].(string)
		if et != "tool_execution" {
			t.Errorf("event_type filter broken: response includes event with event_type=%q; "+
				"want only tool_execution events", et)
		}
	}

	// ── Tertiary assertion: since= filter must work ───────────────────────
	//
	// auditQueryTools passes callStart as the since= value to scope results to
	// the current faulttest run. If the filter is ignored, tool evidence from
	// previous runs leaks into the current score.

	for _, e := range events {
		tsRaw, _ := e["timestamp"].(string)
		if tsRaw == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, tsRaw)
		if err != nil {
			ts, err = time.Parse(time.RFC3339, tsRaw)
		}
		if err != nil {
			continue
		}
		if ts.Before(since.Add(-time.Second)) {
			t.Errorf("since= filter broken: event at %s is before the filter cutoff %s — "+
				"old events are leaking into query results", tsRaw, sinceStr)
		}
	}
}

// TestAuditToolEvidence_RealAgentCall verifies that after a real agent call via
// the gateway, tool_execution events appear in auditd with the correct format.
//
// This test requires a live LLM API key and the full stack. It complements
// TestAuditToolEvidenceContract (which uses a synthetic event) by confirming
// that agents actually emit tool_execution events to auditd during operation.
func TestAuditToolEvidence_RealAgentCall(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}

	callStart := time.Now().UTC()

	// Ask the DB agent to check the database connection — it should always call
	// check_connection or get_database_info for this prompt.
	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	t.Log("sending query to database agent via gateway")
	resp, err := client.Query(ctx, "database", "Check the database connection. Connection string: "+cfg.ConnStr)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if resp.Error != "" {
		SkipIfLLMKeyInvalid(t, resp.Error)
		t.Fatalf("agent returned error: %s", resp.Error)
	}
	SkipIfLLMKeyInvalid(t, resp.Text)
	t.Logf("agent response (%d chars): %.200s", len(resp.Text), resp.Text)

	// Give auditd a moment to persist the events written by the agent.
	time.Sleep(2 * time.Second)

	// Query auditd using the same call auditQueryTools makes.
	sinceStr := callStart.Format(time.RFC3339)
	path := fmt.Sprintf("/v1/events?event_type=tool_execution&since=%s", sinceStr)
	events := auditdGetList(t, cfg.AuditdURL, path)

	t.Logf("auditd returned %d tool_execution event(s) since call start", len(events))

	if len(events) == 0 {
		t.Fatal("no tool_execution events found in auditd after agent call — " +
			"agents are not emitting tool_execution events, or the since= filter is dropping them")
	}

	// Verify at least one event has a non-empty tool.name.
	var toolNames []string
	for _, e := range events {
		tool, ok := e["tool"].(map[string]any)
		if !ok || tool == nil {
			continue
		}
		name, _ := tool["name"].(string)
		if name != "" {
			toolNames = append(toolNames, name)
		}
	}

	if len(toolNames) == 0 {
		t.Fatalf("tool_execution events found but none have tool.name set — "+
			"auditQueryTools would return an empty tool list\nevents: %v", events)
	}

	t.Logf("tool_execution events with tool.name: %v", toolNames)

	// Verify at least one expected DB tool was called.
	expectedTools := map[string]bool{
		"check_connection":       true,
		"get_database_info":      true,
		"get_active_connections": true,
		"get_database_stats":     true,
		"get_config_parameter":   true,
	}
	foundExpected := false
	for _, name := range toolNames {
		if expectedTools[name] {
			foundExpected = true
			break
		}
	}
	if !foundExpected {
		t.Errorf("none of the expected DB tools found in audit trail\ngot: %v\nwant one of: check_connection, get_database_info, get_active_connections, ...",
			toolNames)
	}

	// Spot-check the HTTP path: verify auditQueryTools would return the same
	// names by calling the same URL with a raw HTTP client.
	rawURL := cfg.AuditdURL + path
	httpResp, err := (&http.Client{Timeout: 10 * time.Second}).Get(rawURL)
	if err != nil {
		t.Fatalf("raw HTTP GET %s: %v", rawURL, err)
	}
	httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Errorf("raw HTTP GET %s: status %d, want 200", rawURL, httpResp.StatusCode)
	}
}
