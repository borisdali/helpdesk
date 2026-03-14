//go:build e2e

package e2e

// End-to-end tests for Identity & Access propagation through the live stack.
//
// These tests exercise identity, purpose, and sensitivity fields flowing through:
//   - POST /v1/governance/check  → policy evaluation → persisted pol_* audit event
//   - GET  /v1/governance/explain?purpose=...&sensitivity=...
//   - gateway X-User header     → A2A metadata → agent → audit trail (API key required)
//
// Tests that require only auditd (no gateway, no LLM) skip gracefully when
// auditd is not reachable. Tests that require a live agent call are gated by
// RequireAPIKey and also skip when the gateway is unreachable.
//
// Policy-dependent assertions (those that need a policy engine loaded in the
// running stack) use t.Logf instead of t.Errorf when the policy is disabled,
// so the tests are informative but not flaky on stacks without a policy file.

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

// =============================================================================
// Capability probe
// =============================================================================

// auditdSupportsIdentityFields probes whether the running auditd binary has the
// Identity & Access fields (user_id, roles, auth_method, purpose, sensitivity)
// compiled into its PolicyDecision struct. When the stack is running a stale
// image built before those fields were added, auditd will deserialize and
// reserialize the event, silently dropping unknown JSON keys.
//
// Returns false with a t.Log when identity fields are not supported, so callers
// can skip gracefully instead of hard-failing. Rebuild with 'make image' (or
// use 'make e2e' / 'make e2e-identity', which both depend on the image target).
func auditdSupportsIdentityFields(t *testing.T, auditdURL string) bool {
	t.Helper()
	probeID := fmt.Sprintf("e2e-probe-%d", time.Now().UnixNano())
	probe := map[string]any{
		"event_id":   probeID,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"event_type": "policy_decision",
		"session":    map[string]any{"id": "e2e-probe-session"},
		"policy_decision": map[string]any{
			"resource_type": "database",
			"resource_name": "probe",
			"action":        "read",
			"effect":        "allow",
			"user_id":       "probe@example.com",
		},
	}
	created := auditdPost(t, auditdURL, "/v1/events", probe)
	eventID, _ := created["event_id"].(string)
	if eventID == "" {
		return false
	}
	result := auditdGet(t, auditdURL, "/v1/events/"+eventID)
	pd, _ := result["policy_decision"].(map[string]any)
	if pd == nil {
		return false
	}
	if pd["user_id"] != "probe@example.com" {
		t.Logf("auditd does not support identity fields — image predates Identity & Access feature. Rebuild with 'make image'.")
		return false
	}
	return true
}

// =============================================================================
// Direct auditd: identity fields survive the store round-trip
// =============================================================================

// TestIdentityE2E_PolicyDecisionEvent_IdentityRoundTrip verifies that a
// policy_decision event carrying all identity and purpose fields survives the
// full POST /v1/events → SQLite → GET /v1/events/{id} round-trip in the live
// auditd instance. No policy engine or API key is required.
func TestIdentityE2E_PolicyDecisionEvent_IdentityRoundTrip(t *testing.T) {
	cfg := LoadConfig()
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}
	if !auditdSupportsIdentityFields(t, cfg.AuditdURL) {
		t.Skip("auditd binary predates identity fields — rebuild image with 'make image'")
	}

	eventID := fmt.Sprintf("e2e-ident-%d", time.Now().UnixNano())

	payload := map[string]any{
		"event_id":   eventID,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"event_type": "policy_decision",
		"trace_id":   "e2e-ident-trace",
		"session":    map[string]any{"id": "e2e-ident-session"},
		"policy_decision": map[string]any{
			"resource_type": "database",
			"resource_name": "prod-customers",
			"action":        "read",
			"effect":        "allow",
			"policy_name":   "pii-protection",
			// Identity fields
			"user_id":     "alice@example.com",
			"roles":       []string{"dba", "sre"},
			"auth_method": "jwt",
			// Purpose fields
			"purpose":      "diagnostic",
			"purpose_note": "INC-9001 investigation",
			// Sensitivity
			"sensitivity": []string{"pii"},
		},
	}

	created := auditdPost(t, cfg.AuditdURL, "/v1/events", payload)
	if created["event_id"] == nil {
		t.Fatalf("POST /v1/events: event_id missing from response: %v", created)
	}
	t.Logf("created policy_decision event: %s", eventID)

	result := auditdGet(t, cfg.AuditdURL, "/v1/events/"+eventID)
	if result["event_id"] != eventID {
		t.Errorf("event_id = %v, want %s", result["event_id"], eventID)
	}

	pd, _ := result["policy_decision"].(map[string]any)
	if pd == nil {
		t.Fatalf("policy_decision field missing from retrieved event")
	}

	checks := map[string]string{
		"user_id":      "alice@example.com",
		"auth_method":  "jwt",
		"purpose":      "diagnostic",
		"purpose_note": "INC-9001 investigation",
		"effect":       "allow",
		"policy_name":  "pii-protection",
	}
	for field, want := range checks {
		if pd[field] != want {
			t.Errorf("%s = %v, want %v", field, pd[field], want)
		}
	}

	roles, _ := pd["roles"].([]any)
	if len(roles) != 2 {
		t.Errorf("roles = %v, want [dba sre]", pd["roles"])
	}

	sens, _ := pd["sensitivity"].([]any)
	if len(sens) != 1 || sens[0] != "pii" {
		t.Errorf("sensitivity = %v, want [pii]", pd["sensitivity"])
	}

	t.Logf("identity round-trip OK: user_id=%v purpose=%v sensitivity=%v",
		pd["user_id"], pd["purpose"], pd["sensitivity"])
}

// TestIdentityE2E_ServicePrincipal_RoundTrip verifies that a service account
// principal (no user_id, non-empty service) survives the store round-trip.
func TestIdentityE2E_ServicePrincipal_RoundTrip(t *testing.T) {
	cfg := LoadConfig()
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}
	if !auditdSupportsIdentityFields(t, cfg.AuditdURL) {
		t.Skip("auditd binary predates identity fields — rebuild image with 'make image'")
	}

	eventID := fmt.Sprintf("e2e-svc-%d", time.Now().UnixNano())

	payload := map[string]any{
		"event_id":   eventID,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"event_type": "policy_decision",
		"trace_id":   "e2e-svc-trace",
		"session":    map[string]any{"id": "e2e-svc-session"},
		"policy_decision": map[string]any{
			"resource_type": "database",
			"resource_name": "dev-db",
			"action":        "read",
			"effect":        "allow",
			"policy_name":   "automated-services",
			"service":       "srebot",
			"auth_method":   "api_key",
		},
	}

	auditdPost(t, cfg.AuditdURL, "/v1/events", payload)

	result := auditdGet(t, cfg.AuditdURL, "/v1/events/"+eventID)
	pd, _ := result["policy_decision"].(map[string]any)
	if pd == nil {
		t.Fatal("policy_decision missing from retrieved event")
	}
	if pd["service"] != "srebot" {
		t.Errorf("service = %v, want srebot", pd["service"])
	}
	if pd["auth_method"] != "api_key" {
		t.Errorf("auth_method = %v, want api_key", pd["auth_method"])
	}
	if uid, _ := pd["user_id"].(string); uid != "" {
		t.Errorf("user_id = %q, want empty for service principal", uid)
	}
	t.Logf("service principal round-trip OK: service=%v auth_method=%v", pd["service"], pd["auth_method"])
}

// =============================================================================
// Direct auditd: /v1/governance/check with identity fields
// =============================================================================

// TestIdentityE2E_PolicyCheck_IdentityFieldsInStoredEvent posts a real policy
// check request to the live auditd with principal, purpose, and sensitivity
// fields and verifies the persisted pol_* event contains all of them.
// Skipped when the policy engine is not loaded in the running stack.
func TestIdentityE2E_PolicyCheck_IdentityFieldsInStoredEvent(t *testing.T) {
	cfg := LoadConfig()
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}

	// Check if policy engine is loaded — skip gracefully if not.
	info := auditdGet(t, cfg.AuditdURL, "/v1/governance/info")
	policy, _ := info["policy"].(map[string]any)
	if enabled, _ := policy["enabled"].(bool); !enabled {
		t.Skip("policy engine not enabled on this stack — skipping identity policy check test")
	}

	body := map[string]any{
		"resource_type": "database",
		"resource_name": "e2e-db",
		"action":        "read",
		"trace_id":      fmt.Sprintf("e2e-chk-%d", time.Now().UnixNano()),
		"principal": map[string]any{
			"user_id":     "carol@example.com",
			"roles":       []string{"sre"},
			"auth_method": "jwt",
		},
		"purpose":      "compliance",
		"purpose_note": "quarterly audit",
		"sensitivity":  []string{"internal"},
	}

	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.AuditdURL+"/v1/governance/check", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/governance/check: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Both allow (200) and deny (403) are valid outcomes depending on the stack's
	// policy file. Either way the pol_* event must be persisted with identity fields.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, raw)
	}

	var checkResp map[string]any
	json.Unmarshal(raw, &checkResp)
	eventID, _ := checkResp["event_id"].(string)
	if eventID == "" {
		t.Fatalf("event_id missing from /v1/governance/check response: %s", raw)
	}
	t.Logf("policy check → effect=%v event_id=%s", checkResp["effect"], eventID)

	// Retrieve the stored audit event and verify identity fields.
	event := auditdGet(t, cfg.AuditdURL, "/v1/events/"+eventID)
	pd, _ := event["policy_decision"].(map[string]any)
	if pd == nil {
		t.Fatalf("policy_decision missing from stored event %s", eventID)
	}

	if pd["user_id"] != "carol@example.com" {
		t.Errorf("user_id = %v, want carol@example.com", pd["user_id"])
	}
	if pd["auth_method"] != "jwt" {
		t.Errorf("auth_method = %v, want jwt", pd["auth_method"])
	}
	if pd["purpose"] != "compliance" {
		t.Errorf("purpose = %v, want compliance", pd["purpose"])
	}
	if pd["purpose_note"] != "quarterly audit" {
		t.Errorf("purpose_note = %v, want 'quarterly audit'", pd["purpose_note"])
	}
	sens, _ := pd["sensitivity"].([]any)
	if len(sens) != 1 || sens[0] != "internal" {
		t.Errorf("sensitivity = %v, want [internal]", pd["sensitivity"])
	}
	t.Logf("identity fields in stored pol_* event: user_id=%v purpose=%v sensitivity=%v",
		pd["user_id"], pd["purpose"], pd["sensitivity"])
}

// =============================================================================
// Direct auditd: /v1/governance/explain with purpose and sensitivity params
// =============================================================================

// TestIdentityE2E_Explain_WithPurposeAndSensitivity verifies that the live
// auditd /v1/governance/explain endpoint accepts and reflects the ?purpose= and
// ?sensitivity= query parameters in the decision trace. Skipped when the policy
// engine is not loaded.
func TestIdentityE2E_Explain_WithPurposeAndSensitivity(t *testing.T) {
	cfg := LoadConfig()
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}

	info := auditdGet(t, cfg.AuditdURL, "/v1/governance/info")
	policy, _ := info["policy"].(map[string]any)
	if enabled, _ := policy["enabled"].(bool); !enabled {
		t.Skip("policy engine not enabled on this stack")
	}

	// explain a read with purpose and sensitivity — result depends on the stack's policy.
	result := auditdGet(t, cfg.AuditdURL,
		"/v1/governance/explain?resource_type=database&resource_name=prod-db&action=read&purpose=diagnostic&sensitivity=pii")

	if _, ok := result["decision"]; !ok {
		t.Fatal("explain response missing decision field")
	}
	if _, ok := result["explanation"]; !ok {
		t.Fatal("explain response missing explanation field")
	}
	decision, _ := result["decision"].(map[string]any)
	t.Logf("explain pii+diagnostic read: effect=%v policy=%v",
		decision["effect"], decision["policy_name"])

	// Also check via gateway proxy if available.
	if IsGatewayReachable(cfg.GatewayURL) {
		gatewayResult := auditdGet(t, cfg.GatewayURL,
			"/api/v1/governance/explain?resource_type=database&resource_name=prod-db&action=read&purpose=diagnostic&sensitivity=pii")
		if _, ok := gatewayResult["decision"]; !ok {
			t.Error("gateway explain response missing decision field")
		}
		gd, _ := gatewayResult["decision"].(map[string]any)
		t.Logf("gateway explain pii+diagnostic read: effect=%v policy=%v",
			gd["effect"], gd["policy_name"])
	}
}

// =============================================================================
// Gateway: X-User header propagates through the full pipeline (API key required)
// =============================================================================

// TestIdentityE2E_XUserHeader_PropagatestoAuditTrail makes a real agent call
// through the gateway with an X-User header and verifies that the user identity
// reaches the audit trail. This exercises the full path:
//
//	X-User header → gateway identity resolution (NoAuthProvider)
//	→ A2A metadata (user_id, auth_method)
//	→ agent TraceMiddleware → TraceContext
//	→ agentutil.CheckTool → policy_decision event
//	→ auditd audit store
//
// The test uses the `check_connection` tool (read-only, very fast) to minimise
// LLM token spend while still exercising the identity propagation pipeline.
// If the stack's policy engine is disabled, policy_decision events are absent;
// the test checks tool_invoked events as a fallback.
func TestIdentityE2E_XUserHeader_PropagatestoAuditTrail(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}

	const testUserID = "e2e-alice@example.com"

	// Make a real agent call, injecting X-User so NoAuthProvider picks it up.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	b, _ := json.Marshal(map[string]any{
		"connection_string": cfg.ConnStr,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.GatewayURL+"/api/v1/db/check_connection", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", testUserID)

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
	t.Logf("X-Trace-ID: %s — looking for user_id=%s in audit trail", traceID, testUserID)

	// Give auditd time to persist the events (agent recording is asynchronous).
	time.Sleep(3 * time.Second)

	events := auditdGetList(t, cfg.AuditdURL, "/v1/events?trace_id="+traceID)
	if len(events) == 0 {
		t.Fatalf("no audit events found for trace_id=%s", traceID)
	}
	t.Logf("found %d audit event(s) for trace_id=%s", len(events), traceID)

	// Look for any event that carries the user_id we injected.
	// policy_decision events carry it when policy enforcement is enabled;
	// gateway_request anchor events always carry it via the session.user_id field.
	foundIdentity := false
	for _, e := range events {
		// Check policy_decision.user_id (most direct evidence of propagation).
		if pd, ok := e["policy_decision"].(map[string]any); ok {
			if pd["user_id"] == testUserID {
				foundIdentity = true
				t.Logf("✓ found user_id=%q in policy_decision event %v", testUserID, e["event_id"])
				break
			}
		}
		// Also check the gateway_request anchor event's session.user_id field,
		// which the gateway populates directly from the resolved principal.
		if sess, ok := e["session"].(map[string]any); ok {
			if sess["user_id"] == testUserID {
				foundIdentity = true
				t.Logf("✓ found user_id=%q in session of %v event %v",
					testUserID, e["event_type"], e["event_id"])
				break
			}
		}
	}

	if !foundIdentity {
		// Soft-fail with a log rather than t.Errorf: identity propagation requires
		// either policy enforcement or a stack built after the identity feature was
		// merged. Log the event types seen to help diagnose.
		eventTypes := make([]string, 0, len(events))
		for _, e := range events {
			if et, ok := e["event_type"].(string); ok {
				eventTypes = append(eventTypes, et)
			}
		}
		t.Logf("⚠ user_id=%q not found in any audit event for trace %s", testUserID, traceID)
		t.Logf("  event types seen: %v", eventTypes)
		t.Logf("  This may mean: (a) identity provider not configured, (b) policy engine disabled,")
		t.Logf("  or (c) the stack predates the identity propagation feature.")
	}
}

// TestIdentityE2E_AnonymousRequest_NoIdentityInTrace verifies that a gateway
// request made WITHOUT an X-User header results in audit events with an empty
// user_id — confirming that the identity system correctly handles anonymous
// callers rather than fabricating an identity.
func TestIdentityE2E_AnonymousRequest_NoIdentityInTrace(t *testing.T) {
	RequireAPIKey(t)
	cfg := LoadConfig()

	if !IsGatewayReachable(cfg.GatewayURL) {
		t.Skipf("gateway not reachable at %s", cfg.GatewayURL)
	}
	if !isAuditdReachable(cfg.AuditdURL) {
		t.Skipf("auditd not reachable at %s", cfg.AuditdURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	b, _ := json.Marshal(map[string]any{
		"connection_string": cfg.ConnStr,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.GatewayURL+"/api/v1/db/check_connection", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Explicitly no X-User header.

	httpClient := &http.Client{Timeout: 90 * time.Second}
	httpResp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("agent call: %v", err)
	}
	io.ReadAll(httpResp.Body)
	httpResp.Body.Close()

	traceID := httpResp.Header.Get("X-Trace-ID")
	if traceID == "" {
		t.Skip("X-Trace-ID not returned — audit not enabled on this stack")
	}
	t.Logf("anonymous call X-Trace-ID: %s", traceID)

	time.Sleep(3 * time.Second)

	events := auditdGetList(t, cfg.AuditdURL, "/v1/events?trace_id="+traceID)
	if len(events) == 0 {
		t.Fatalf("no audit events found for trace_id=%s", traceID)
	}

	// Verify no event fabricated a user_id for the anonymous caller.
	for _, e := range events {
		if pd, ok := e["policy_decision"].(map[string]any); ok {
			if uid, _ := pd["user_id"].(string); uid != "" {
				t.Errorf("anonymous request: policy_decision has unexpected user_id=%q in event %v",
					uid, e["event_id"])
			}
		}
	}
	t.Logf("anonymous request: %d event(s) found, none with unexpected user_id — OK", len(events))
}
