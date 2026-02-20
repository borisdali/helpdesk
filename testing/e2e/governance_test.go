//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// isAuditdReachable returns true if auditd is responding at the given URL.
func isAuditdReachable(auditdURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, auditdURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// auditdGet fetches a JSON object from auditd directly.
func auditdGet(t *testing.T, auditdURL, path string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, auditdURL+path, nil)
	if err != nil {
		t.Fatalf("build request GET %s: %v", path, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("GET %s → %d: %s", path, resp.StatusCode, raw)
	}
	var result map[string]any
	json.Unmarshal(raw, &result)
	return result
}

// auditdGetList fetches a JSON array from auditd directly.
func auditdGetList(t *testing.T, auditdURL, path string) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, auditdURL+path, nil)
	if err != nil {
		t.Fatalf("build request GET %s: %v", path, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("GET %s → %d: %s", path, resp.StatusCode, raw)
	}
	var result []map[string]any
	json.Unmarshal(raw, &result)
	return result
}

// auditdGetSoft fetches a JSON object from auditd but returns nil (instead of
// calling t.Fatalf) when the endpoint responds with a non-2xx status. Use this
// for optional/informational checks where a 404 just means the feature is not
// yet present in the running image.
func auditdGetSoft(t *testing.T, auditdURL, path string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, auditdURL+path, nil)
	if err != nil {
		t.Logf("auditdGetSoft: build request GET %s: %v", path, err)
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("auditdGetSoft: GET %s: %v", path, err)
		return nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Logf("auditdGetSoft: GET %s → %d (skipping check): %s", path, resp.StatusCode, raw)
		return nil
	}
	var result map[string]any
	json.Unmarshal(raw, &result)
	return result
}

// auditdPost sends a JSON POST to auditd and decodes the response.
func auditdPost(t *testing.T, auditdURL, path string, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, auditdURL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("build request POST %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("POST %s → %d: %s", path, resp.StatusCode, raw)
	}
	var result map[string]any
	json.Unmarshal(raw, &result)
	return result
}

// =============================================================================
// Gateway governance endpoints (no API key required)
// =============================================================================

// TestGovernance_GatewayInfoEndpoint verifies the gateway proxies /api/v1/governance
// correctly and returns a valid governance info structure.
func TestGovernance_GatewayInfoEndpoint(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	info, err := client.GovernanceInfo(ctx)
	if err != nil {
		t.Fatalf("GovernanceInfo: %v", err)
	}

	// Audit subsystem must always be reported (auditd is running in the full stack).
	audit, _ := info["audit"].(map[string]any)
	if audit == nil {
		t.Fatal("governance info missing audit field")
	}
	if enabled, _ := audit["enabled"].(bool); !enabled {
		t.Error("audit.enabled should be true in the full stack")
	}
	if _, ok := audit["events_total"]; !ok {
		t.Error("audit.events_total field missing")
	}
	if _, ok := audit["chain_valid"]; !ok {
		t.Error("audit.chain_valid field missing")
	}

	// Policy section is always present (enabled or not).
	if _, ok := info["policy"]; !ok {
		t.Error("governance info missing policy field")
	}

	// Timestamp is present.
	if ts, _ := info["timestamp"].(string); ts == "" {
		t.Error("governance info missing timestamp")
	}

	t.Logf("governance/info: events_total=%.0f chain_valid=%v policy.enabled=%v",
		audit["events_total"],
		audit["chain_valid"],
		func() any {
			if p, ok := info["policy"].(map[string]any); ok {
				return p["enabled"]
			}
			return false
		}(),
	)
}

// TestGovernance_GatewayPoliciesEndpoint verifies the /api/v1/governance/policies
// endpoint responds with a valid JSON body.
func TestGovernance_GatewayPoliciesEndpoint(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := client.GovernancePolicies(ctx)
	if err != nil {
		t.Fatalf("GovernancePolicies: %v", err)
	}

	// enabled field must be present (true or false).
	if _, ok := result["enabled"]; !ok {
		t.Error("policies response missing enabled field")
	}
	t.Logf("governance/policies: enabled=%v", result["enabled"])
}

// TestGovernance_ChainIntegrityViaGateway verifies the hash chain is valid
// as reported through the gateway → auditd path.
func TestGovernance_ChainIntegrityViaGateway(t *testing.T) {
	cfg := LoadConfig()
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	info, err := client.GovernanceInfo(ctx)
	if err != nil {
		t.Fatalf("GovernanceInfo: %v", err)
	}

	audit, _ := info["audit"].(map[string]any)
	if audit == nil {
		t.Fatal("audit field missing")
	}
	if valid, _ := audit["chain_valid"].(bool); !valid {
		t.Error("audit.chain_valid=false — hash chain is broken in the running stack")
	}
}

// =============================================================================
// Direct auditd checks (no API key required)
// =============================================================================

// TestGovernance_AuditdHealth verifies auditd's /health endpoint is up.
func TestGovernance_AuditdHealth(t *testing.T) {
	cfg := LoadConfig()
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}

	result := auditdGet(t, cfg.AuditdURL, "/health")
	if result["status"] != "ok" {
		t.Errorf("health status = %v, want ok", result["status"])
	}
}

// TestGovernance_AuditdVerifyChain hits the verify endpoint directly on auditd
// and confirms the chain is intact.
func TestGovernance_AuditdVerifyChain(t *testing.T) {
	cfg := LoadConfig()
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}

	result := auditdGet(t, cfg.AuditdURL, "/v1/verify")
	if valid, _ := result["valid"].(bool); !valid {
		t.Errorf("chain integrity check failed: %v", result)
	}
	t.Logf("verify: total_events=%.0f valid=%v", result["total_events"], result["valid"])
}

// =============================================================================
// Approval workflow (no API key required)
// =============================================================================

// TestGovernance_ApprovalLifecycle exercises the full approval workflow end-to-end
// via auditd: create → verify pending → approve → verify resolved.
func TestGovernance_ApprovalLifecycle(t *testing.T) {
	cfg := LoadConfig()
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}

	// Create a pending approval request.
	created := auditdPost(t, cfg.AuditdURL, "/v1/approvals", map[string]any{
		"action_class":  "destructive",
		"tool_name":     "drop_table",
		"agent_name":    "database-agent",
		"requested_by":  "e2e-test-user",
		"resource_type": "database",
		"resource_name": "prod-db",
	})
	approvalID, _ := created["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("approval_id missing from create response")
	}
	if created["status"] != "pending" {
		t.Errorf("initial status = %v, want pending", created["status"])
	}
	t.Logf("created approval: %s", approvalID)

	// Verify it appears in the pending list.
	pending := auditdGetList(t, cfg.AuditdURL, "/v1/approvals/pending")
	found := false
	for _, a := range pending {
		if a["approval_id"] == approvalID {
			found = true
		}
	}
	if !found {
		t.Errorf("approval %s not in pending list", approvalID)
	}

	// Verify the governance info reflects the pending count.
	// Use the soft variant: a 404 means the image predates the governance/info endpoint.
	govInfo := auditdGetSoft(t, cfg.AuditdURL, "/v1/governance/info")
	if govInfo != nil {
		approvals, _ := govInfo["approvals"].(map[string]any)
		if approvals != nil {
			pendingCount, _ := approvals["pending_count"].(float64)
			if pendingCount < 1 {
				t.Errorf("governance info pending_count = %.0f, expected >= 1", pendingCount)
			}
		}
	}

	// Approve it.
	approved := auditdPost(t, cfg.AuditdURL, "/v1/approvals/"+approvalID+"/approve",
		map[string]any{"approved_by": "e2e-approver", "reason": "e2e test approval"})
	if approved["status"] != "approved" {
		t.Errorf("status after approve = %v, want approved", approved["status"])
	}
	if approved["resolved_by"] != "e2e-approver" {
		t.Errorf("resolved_by = %v, want e2e-approver", approved["resolved_by"])
	}

	// Should no longer be in pending.
	pending = auditdGetList(t, cfg.AuditdURL, "/v1/approvals/pending")
	for _, a := range pending {
		if a["approval_id"] == approvalID {
			t.Error("approved request still appears in pending list")
		}
	}
	t.Logf("approval %s approved successfully", approvalID)
}

// =============================================================================
// Audit trail from real agent calls (requires API key + full stack)
// =============================================================================

// TestGovernance_AgentCallGeneratesAuditEvents is the key regression test for
// the bug where HELPDESK_AUDIT_ENABLED was missing from agents so no events
// were recorded. It makes a real agent call and verifies the audit count grows.
func TestGovernance_AgentCallGeneratesAuditEvents(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)

	// Baseline event count via governance info.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	before, err := client.GovernanceInfo(ctx)
	if err != nil {
		t.Fatalf("baseline GovernanceInfo: %v", err)
	}
	beforeAudit, _ := before["audit"].(map[string]any)
	beforeTotal, _ := beforeAudit["events_total"].(float64)
	t.Logf("baseline events_total: %.0f", beforeTotal)

	// Make a real agent call (a lightweight DB connectivity check).
	agentCtx, agentCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer agentCancel()

	resp, err := client.DBTool(agentCtx, "check_connection", map[string]any{
		"connection_string": cfg.ConnStr,
	})
	if err != nil {
		t.Fatalf("agent call failed: %v", err)
	}
	t.Logf("agent response (%d chars): %s", len(resp.Text), truncate(resp.Text, 150))

	// Give auditd a moment to persist the event (it's asynchronous in the agent).
	time.Sleep(2 * time.Second)

	// Recheck event count.
	afterCtx, afterCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer afterCancel()

	after, err := client.GovernanceInfo(afterCtx)
	if err != nil {
		t.Fatalf("post-call GovernanceInfo: %v", err)
	}
	afterAudit, _ := after["audit"].(map[string]any)
	afterTotal, _ := afterAudit["events_total"].(float64)
	t.Logf("post-call events_total: %.0f", afterTotal)

	if afterTotal <= beforeTotal {
		t.Errorf("events_total did not increase after agent call: before=%.0f after=%.0f\n"+
			"This likely means HELPDESK_AUDIT_ENABLED is not set on the agents.", beforeTotal, afterTotal)
	}

	// Chain must still be valid after new events.
	if valid, _ := afterAudit["chain_valid"].(bool); !valid {
		t.Error("hash chain invalid after recording new events")
	}
}

// TestGovernance_TraceIDCorrelation verifies that the X-Trace-ID header returned
// by the gateway can be used to look up the audit events in auditd.
func TestGovernance_TraceIDCorrelation(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}

	// Make an agent call and capture the X-Trace-ID response header.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	b, _ := json.Marshal(map[string]any{
		"connection_string": cfg.ConnStr,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.GatewayURL+"/api/v1/db/check_connection",
		bytes.NewReader(b))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 90 * time.Second}
	httpResp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("agent call: %v", err)
	}
	io.ReadAll(httpResp.Body)
	httpResp.Body.Close()

	traceID := httpResp.Header.Get("X-Trace-ID")
	if traceID == "" {
		t.Skip("X-Trace-ID not returned by gateway — audit may not be enabled on this stack")
	}
	t.Logf("X-Trace-ID: %s", traceID)

	// Give auditd a moment to persist.
	time.Sleep(2 * time.Second)

	// Query auditd for events with this trace ID.
	events := auditdGetList(t, cfg.AuditdURL, "/v1/events?trace_id="+traceID)
	if len(events) == 0 {
		t.Errorf("no audit events found for trace_id=%s — "+
			"audit events from agent calls are not reaching auditd", traceID)
	} else {
		t.Logf("found %d event(s) for trace_id=%s", len(events), traceID)
		for _, e := range events {
			t.Logf("  event_type=%v agent=%v", e["event_type"],
				func() any {
					if sess, ok := e["session"].(map[string]any); ok {
						return sess["user_id"]
					}
					return "-"
				}(),
			)
		}
	}
}

// TestGovernance_GatewayWithoutAuditdConfigured verifies graceful degradation:
// when the gateway has no auditd URL configured, governance endpoints still
// return a valid JSON response (not a 500).
//
// This test is skipped when the full stack is up (since auditd is always
// configured there). Set E2E_SKIP_DEGRADED=true to explicitly skip it.
func TestGovernance_GatewayWithoutAuditdConfigured(t *testing.T) {
	cfg := LoadConfig()

	// If auditd is reachable, the gateway is fully configured — skip this test.
	if isAuditdReachable(cfg.AuditdURL) {
		t.Skip("auditd is reachable; degraded-mode test not applicable in full stack")
	}
	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}

	client := NewGatewayClient(cfg.GatewayURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Should still return 200 with an informative message, not a 500.
	info, err := client.GovernanceInfo(ctx)
	if err != nil {
		t.Fatalf("GovernanceInfo failed (expected graceful degradation): %v", err)
	}
	t.Logf("degraded governance response: %v", info)
	// At minimum, it should return something — not crash.
	if len(info) == 0 {
		t.Error("expected non-empty governance response even without auditd")
	}
}

// TestGovernance_FullStackSummary is a read-only diagnostic that logs the complete
// governance posture of the running stack. Always passes; useful for debugging.
func TestGovernance_FullStackSummary(t *testing.T) {
	cfg := LoadConfig()

	gatewayOK := IsGatewayReachable(cfg.GatewayURL)
	auditdOK := isAuditdReachable(cfg.AuditdURL)

	t.Logf("=== Governance Stack Summary ===")
	t.Logf("Gateway  (%s): reachable=%v", cfg.GatewayURL, gatewayOK)
	t.Logf("Auditd   (%s): reachable=%v", cfg.AuditdURL, auditdOK)

	if !gatewayOK && !auditdOK {
		t.Skip("neither gateway nor auditd is reachable — stack not running")
	}

	if auditdOK {
		verify := auditdGet(t, cfg.AuditdURL, "/v1/verify")
		t.Logf("Audit chain: total_events=%.0f valid=%v", verify["total_events"], verify["valid"])

		govInfo := auditdGetSoft(t, cfg.AuditdURL, "/v1/governance/info")
		if policy, ok := govInfo["policy"].(map[string]any); ok {
			t.Logf("Policy: enabled=%v policies_count=%v rules_count=%v",
				policy["enabled"], policy["policies_count"], policy["rules_count"])
		}
		if approvals, ok := govInfo["approvals"].(map[string]any); ok {
			t.Logf("Approvals: pending=%v webhook=%v email=%v",
				approvals["pending_count"],
				approvals["webhook_configured"],
				approvals["email_configured"])
		}
	}

	if gatewayOK {
		client := NewGatewayClient(cfg.GatewayURL)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if policies, err := client.GovernancePolicies(ctx); err == nil {
			t.Logf("Gateway policies: enabled=%v", policies["enabled"])
		}
	}

	t.Logf("=== End Summary ===")
	// Always pass — this is diagnostic only.
	_ = fmt.Sprintf("governance summary complete for stack at %s", cfg.GatewayURL)
}
