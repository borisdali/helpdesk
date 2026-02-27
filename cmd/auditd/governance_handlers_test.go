package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
	"helpdesk/internal/policy"
)

// minimalPolicyYAML is a valid two-rule policy used across governance tests.
const minimalPolicyYAML = `
version: "1"
policies:
  - name: db-policy
    description: Test database policy
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
      - action: write
        effect: deny
        message: "writes not allowed"
`

func writeTempPolicy(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "policies-*.yaml")
	if err != nil {
		t.Fatalf("create temp policy file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp policy file: %v", err)
	}
	f.Close()
	return f.Name()
}

func makeEngine(t *testing.T, yaml string) *policy.Engine {
	t.Helper()
	cfg, err := policy.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	return policy.NewEngine(policy.EngineConfig{PolicyConfig: cfg})
}

// --- handleGetInfo ---

func TestGovernanceInfo_NoPolicyEngine(t *testing.T) {
	gs := &governanceServer{} // nil engine, nil stores

	req := httptest.NewRequest(http.MethodGet, "/v1/governance/info", nil)
	w := httptest.NewRecorder()
	gs.handleGetInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var info GovernanceInfo
	if err := json.NewDecoder(w.Body).Decode(&info); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if info.Policy == nil {
		t.Fatal("expected Policy field to be set")
	}
	if info.Policy.Enabled {
		t.Error("expected policy.enabled=false when no engine loaded")
	}
	if info.Policy.PoliciesCount != 0 {
		t.Errorf("policies_count = %d, want 0", info.Policy.PoliciesCount)
	}
}

func TestGovernanceInfo_WithPolicyEngine(t *testing.T) {
	gs := &governanceServer{
		policyEngine: makeEngine(t, minimalPolicyYAML),
		policyFile:   "/etc/helpdesk/policies.yaml",
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/governance/info", nil)
	w := httptest.NewRecorder()
	gs.handleGetInfo(w, req)

	var info GovernanceInfo
	if err := json.NewDecoder(w.Body).Decode(&info); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !info.Policy.Enabled {
		t.Error("expected policy.enabled=true")
	}
	if info.Policy.PoliciesCount != 1 {
		t.Errorf("policies_count = %d, want 1", info.Policy.PoliciesCount)
	}
	if info.Policy.RulesCount != 2 {
		t.Errorf("rules_count = %d, want 2", info.Policy.RulesCount)
	}
	if info.Policy.File != "/etc/helpdesk/policies.yaml" {
		t.Errorf("file = %q, want /etc/helpdesk/policies.yaml", info.Policy.File)
	}
}

func TestGovernanceInfo_AuditFieldsWithNilStore(t *testing.T) {
	gs := &governanceServer{}

	req := httptest.NewRequest(http.MethodGet, "/v1/governance/info", nil)
	w := httptest.NewRecorder()
	gs.handleGetInfo(w, req)

	var info GovernanceInfo
	json.NewDecoder(w.Body).Decode(&info)

	// Audit section is always present; nil store means zero counts.
	if !info.Audit.Enabled {
		t.Error("expected audit.enabled=true (the service is running)")
	}
	if info.Audit.EventsTotal != 0 {
		t.Errorf("events_total = %d, want 0 with nil store", info.Audit.EventsTotal)
	}
}

// --- handleGetPolicySummary ---

func TestGovernancePolicySummary_NoPolicyEngine(t *testing.T) {
	gs := &governanceServer{}

	req := httptest.NewRequest(http.MethodGet, "/v1/governance/policies", nil)
	w := httptest.NewRecorder()
	gs.handleGetPolicySummary(w, req)

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if enabled, _ := resp["enabled"].(bool); enabled {
		t.Error("expected enabled=false when no engine")
	}
}

func TestGovernancePolicySummary_WithPolicyEngine(t *testing.T) {
	gs := &governanceServer{
		policyEngine: makeEngine(t, minimalPolicyYAML),
		policyFile:   "/tmp/policies.yaml",
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/governance/policies", nil)
	w := httptest.NewRecorder()
	gs.handleGetPolicySummary(w, req)

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if enabled, _ := resp["enabled"].(bool); !enabled {
		t.Error("expected enabled=true")
	}
	policies, _ := resp["policies"].([]any)
	if len(policies) != 1 {
		t.Fatalf("policies count = %d, want 1", len(policies))
	}
	pol := policies[0].(map[string]any)
	if pol["name"] != "db-policy" {
		t.Errorf("policy name = %q, want db-policy", pol["name"])
	}
	rules, _ := pol["rules"].([]any)
	if len(rules) != 2 {
		t.Errorf("rules count = %d, want 2", len(rules))
	}
}

// --- newGovernanceServer: env var logic ---

func TestNewGovernanceServer_PolicyEnabled(t *testing.T) {
	path := writeTempPolicy(t, minimalPolicyYAML)
	t.Setenv("HELPDESK_POLICY_ENABLED", "true")
	t.Setenv("HELPDESK_POLICY_FILE", path)

	gs := newGovernanceServer(nil, nil, nil)
	if gs.policyEngine == nil {
		t.Error("expected policy engine to be loaded when HELPDESK_POLICY_ENABLED=true")
	}
}

func TestNewGovernanceServer_PolicyDisabled(t *testing.T) {
	path := writeTempPolicy(t, minimalPolicyYAML)
	t.Setenv("HELPDESK_POLICY_ENABLED", "false")
	t.Setenv("HELPDESK_POLICY_FILE", path)

	gs := newGovernanceServer(nil, nil, nil)
	if gs.policyEngine != nil {
		t.Error("expected no policy engine when HELPDESK_POLICY_ENABLED=false")
	}
}

func TestNewGovernanceServer_BackwardCompat_FileSetNoFlag(t *testing.T) {
	path := writeTempPolicy(t, minimalPolicyYAML)
	t.Setenv("HELPDESK_POLICY_FILE", path)
	// HELPDESK_POLICY_ENABLED deliberately not set.

	gs := newGovernanceServer(nil, nil, nil)
	if gs.policyEngine == nil {
		t.Error("expected policy engine to be loaded by backward compat when only HELPDESK_POLICY_FILE is set")
	}
}

func TestNewGovernanceServer_PolicyEnabledBadFile(t *testing.T) {
	t.Setenv("HELPDESK_POLICY_ENABLED", "true")
	t.Setenv("HELPDESK_POLICY_FILE", "/nonexistent/policies.yaml")

	// Should not panic or crash — warn and leave engine nil.
	gs := newGovernanceServer(nil, nil, nil)
	if gs.policyEngine != nil {
		t.Error("expected nil engine when policy file does not exist")
	}
}

func TestNewGovernanceServer_NoPolicyConfig(t *testing.T) {
	// Neither env var set — governance server starts fine with no policy engine.
	gs := newGovernanceServer(nil, nil, nil)
	if gs.policyEngine != nil {
		t.Error("expected nil engine when no policy env vars are set")
	}
}

// --- handleExplain ---

func TestHandleExplain_NoPolicyEngine(t *testing.T) {
	gs := &governanceServer{} // nil engine

	req := httptest.NewRequest(http.MethodGet, "/v1/governance/explain?resource_type=database&resource_name=prod-db&action=write", nil)
	w := httptest.NewRecorder()
	gs.handleExplain(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if enabled, _ := resp["enabled"].(bool); enabled {
		t.Error("expected enabled=false when no engine is loaded")
	}
}

func TestHandleExplain_MissingParams(t *testing.T) {
	gs := &governanceServer{policyEngine: makeEngine(t, minimalPolicyYAML)}

	// Missing action parameter.
	req := httptest.NewRequest(http.MethodGet, "/v1/governance/explain?resource_type=database&resource_name=prod-db", nil)
	w := httptest.NewRecorder()
	gs.handleExplain(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing action: status = %d, want 400", w.Code)
	}
}

func TestHandleExplain_AllowedDecision(t *testing.T) {
	gs := &governanceServer{policyEngine: makeEngine(t, minimalPolicyYAML)}

	req := httptest.NewRequest(http.MethodGet, "/v1/governance/explain?resource_type=database&resource_name=test-db&action=read", nil)
	w := httptest.NewRecorder()
	gs.handleExplain(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var trace map[string]any
	if err := json.NewDecoder(w.Body).Decode(&trace); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	decision, _ := trace["decision"].(map[string]any)
	if decision == nil {
		t.Fatal("response missing decision field")
	}
	if decision["effect"] != "allow" {
		t.Errorf("Effect = %v, want allow", decision["effect"])
	}
	if expl, _ := trace["explanation"].(string); expl == "" {
		t.Error("explanation should be non-empty for allowed decisions")
	}
}

func TestHandleExplain_DeniedDecision(t *testing.T) {
	gs := &governanceServer{policyEngine: makeEngine(t, minimalPolicyYAML)}

	req := httptest.NewRequest(http.MethodGet, "/v1/governance/explain?resource_type=database&resource_name=prod-db&action=write", nil)
	w := httptest.NewRecorder()
	gs.handleExplain(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var trace map[string]any
	json.NewDecoder(w.Body).Decode(&trace)

	decision, _ := trace["decision"].(map[string]any)
	if decision["effect"] != "deny" {
		t.Errorf("Effect = %v, want deny", decision["effect"])
	}
	expl, _ := trace["explanation"].(string)
	if !strings.Contains(expl, "DENIED") {
		t.Errorf("explanation should contain DENIED, got: %s", expl)
	}
	if !strings.Contains(expl, "writes not allowed") {
		t.Errorf("explanation should contain the denial message, got: %s", expl)
	}
}

func TestHandleExplain_RequireApproval(t *testing.T) {
	const approvalPolicyYAML = `
version: "1"
policies:
  - name: approval-policy
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
        conditions:
          require_approval: true
`
	gs := &governanceServer{policyEngine: makeEngine(t, approvalPolicyYAML)}

	req := httptest.NewRequest(http.MethodGet, "/v1/governance/explain?resource_type=database&resource_name=prod-db&action=write", nil)
	w := httptest.NewRecorder()
	gs.handleExplain(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var trace map[string]any
	json.NewDecoder(w.Body).Decode(&trace)

	decision, _ := trace["decision"].(map[string]any)
	if decision["effect"] != "require_approval" {
		t.Errorf("Effect = %v, want require_approval", decision["effect"])
	}
	if expl, _ := trace["explanation"].(string); expl == "" {
		t.Error("explanation should be non-empty for require_approval decisions")
	}
}

func TestHandleExplain_TagsPassedThrough(t *testing.T) {
	const tagPolicyYAML = `
version: "1"
policies:
  - name: prod-policy
    resources:
      - type: database
        match:
          tags: [production]
    rules:
      - action: write
        effect: deny
        message: "production writes denied"
  - name: default-allow
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
`
	gs := &governanceServer{policyEngine: makeEngine(t, tagPolicyYAML)}

	// Staging tag — prod-policy doesn't match, default-allow does.
	req := httptest.NewRequest(http.MethodGet, "/v1/governance/explain?resource_type=database&resource_name=dev-db&action=write&tags=staging", nil)
	w := httptest.NewRecorder()
	gs.handleExplain(w, req)
	var trace map[string]any
	json.NewDecoder(w.Body).Decode(&trace)
	if d, _ := trace["decision"].(map[string]any); d["effect"] != "allow" {
		t.Errorf("staging tag: Effect = %v, want allow", d["effect"])
	}

	// Production tag — prod-policy matches and denies.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/governance/explain?resource_type=database&resource_name=prod-db&action=write&tags=production", nil)
	w2 := httptest.NewRecorder()
	gs.handleExplain(w2, req2)
	var trace2 map[string]any
	json.NewDecoder(w2.Body).Decode(&trace2)
	if d, _ := trace2["decision"].(map[string]any); d["effect"] != "deny" {
		t.Errorf("production tag: Effect = %v, want deny", d["effect"])
	}
}

// TestHandleExplain_TagsResolvedFromInfraConfig verifies that when no ?tags= parameter
// is supplied, handleExplain auto-resolves the resource tags from the infra config.
// This mirrors how agents enrich policy.Request at runtime.
func TestHandleExplain_TagsResolvedFromInfraConfig(t *testing.T) {
	const devPolicyYAML = `
version: "1"
policies:
  - name: dev-allow-all
    resources:
      - type: database
        match:
          tags: [development]
    rules:
      - action: [read, write]
        effect: allow
`
	ic := &infra.Config{
		DBServers: map[string]infra.DBServer{
			"alloydb-on-vm": {
				Name: "AlloyDB on VM",
				Tags: []string{"development"},
			},
		},
	}
	gs := &governanceServer{
		policyEngine: makeEngine(t, devPolicyYAML),
		infraConfig:  ic,
	}

	// No ?tags= — tags must be resolved from infraConfig automatically.
	req := httptest.NewRequest(http.MethodGet,
		"/v1/governance/explain?resource_type=database&resource_name=alloydb-on-vm&action=read", nil)
	w := httptest.NewRecorder()
	gs.handleExplain(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var trace map[string]any
	if err := json.NewDecoder(w.Body).Decode(&trace); err != nil {
		t.Fatalf("decode trace: %v", err)
	}
	d, _ := trace["decision"].(map[string]any)
	if got := d["effect"]; got != "allow" {
		t.Errorf("Effect = %v, want allow (tags should have been resolved from infra config)", got)
	}
	// The explanation should mention the resolved tag.
	expl, _ := trace["explanation"].(string)
	if !strings.Contains(expl, "development") {
		t.Errorf("explanation %q should mention the resolved tag 'development'", expl)
	}
}

// TestHandleExplain_ExplicitTagsOverrideInfra verifies that an explicit ?tags= parameter
// takes precedence over infra-config tags.
func TestHandleExplain_ExplicitTagsOverrideInfra(t *testing.T) {
	const prodDenyYAML = `
version: "1"
policies:
  - name: prod-deny-write
    resources:
      - type: database
        match:
          tags: [production]
    rules:
      - action: write
        effect: deny
        message: "production writes denied"
  - name: dev-allow-all
    resources:
      - type: database
        match:
          tags: [development]
    rules:
      - action: write
        effect: allow
`
	// Infra says the resource has "development" tag, but the caller passes "production".
	ic := &infra.Config{
		DBServers: map[string]infra.DBServer{
			"shared-db": {
				Name: "Shared DB",
				Tags: []string{"development"},
			},
		},
	}
	gs := &governanceServer{
		policyEngine: makeEngine(t, prodDenyYAML),
		infraConfig:  ic,
	}

	// Explicit ?tags=production should override the infra-config "development" tag.
	req := httptest.NewRequest(http.MethodGet,
		"/v1/governance/explain?resource_type=database&resource_name=shared-db&action=write&tags=production", nil)
	w := httptest.NewRecorder()
	gs.handleExplain(w, req)

	var trace map[string]any
	json.NewDecoder(w.Body).Decode(&trace)
	d, _ := trace["decision"].(map[string]any)
	if got := d["effect"]; got != "deny" {
		t.Errorf("Effect = %v, want deny (explicit production tag should override infra-config)", got)
	}
}

// --- handleGetEvent ---

// newTestAuditStore creates a temporary SQLite-backed audit store for testing.
func newTestAuditStore(t *testing.T) *audit.Store {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestHandleGetEvent_NotFound(t *testing.T) {
	gs := &governanceServer{auditStore: newTestAuditStore(t)}

	req := httptest.NewRequest(http.MethodGet, "/v1/events/nonexistent-id", nil)
	req.SetPathValue("eventID", "nonexistent-id")
	w := httptest.NewRecorder()
	gs.handleGetEvent(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleGetEvent_Found(t *testing.T) {
	store := newTestAuditStore(t)
	gs := &governanceServer{auditStore: store}

	event := &audit.Event{
		EventID:   "evt-handler-found",
		Timestamp: time.Now(),
		EventType: audit.EventTypePolicyDecision,
		Session:   audit.Session{ID: "sess-test"},
		PolicyDecision: &audit.PolicyDecision{
			ResourceType: "database",
			ResourceName: "prod-db",
			Action:       "write",
			Effect:       "deny",
			PolicyName:   "test-policy",
			Message:      "writes not allowed",
		},
	}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatalf("Record: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/events/evt-handler-found", nil)
	req.SetPathValue("eventID", "evt-handler-found")
	w := httptest.NewRecorder()
	gs.handleGetEvent(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var result map[string]any
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["event_id"] != "evt-handler-found" {
		t.Errorf("event_id = %v, want evt-handler-found", result["event_id"])
	}
	pd, _ := result["policy_decision"].(map[string]any)
	if pd == nil {
		t.Fatal("policy_decision field missing from response")
	}
	if pd["effect"] != "deny" {
		t.Errorf("policy_decision.effect = %v, want deny", pd["effect"])
	}
}

func TestHandleGetEvent_WithAgentReasoning(t *testing.T) {
	store := newTestAuditStore(t)
	gs := &governanceServer{auditStore: store}

	event := &audit.Event{
		EventID:   "evt-handler-reasoning",
		Timestamp: time.Now(),
		EventType: audit.EventTypeAgentReasoning,
		TraceID:   "trace-unit-42",
		Session:   audit.Session{ID: "sess-rsn"},
		AgentReasoning: &audit.AgentReasoning{
			Reasoning: "The user wants connection stats. I will call get_active_connections.",
			ToolCalls: []string{"get_active_connections", "get_connection_stats"},
		},
	}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatalf("Record: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/events/evt-handler-reasoning", nil)
	req.SetPathValue("eventID", "evt-handler-reasoning")
	w := httptest.NewRecorder()
	gs.handleGetEvent(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var result map[string]any
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["event_id"] != "evt-handler-reasoning" {
		t.Errorf("event_id = %v, want evt-handler-reasoning", result["event_id"])
	}
	if result["event_type"] != "agent_reasoning" {
		t.Errorf("event_type = %v, want agent_reasoning", result["event_type"])
	}
	ar, _ := result["agent_reasoning"].(map[string]any)
	if ar == nil {
		t.Fatal("agent_reasoning field missing from response")
	}
	if reasoning, _ := ar["reasoning"].(string); reasoning == "" {
		t.Error("agent_reasoning.reasoning is empty after store round-trip")
	}
	toolCalls, _ := ar["tool_calls"].([]any)
	if len(toolCalls) != 2 {
		t.Errorf("agent_reasoning.tool_calls = %v, want 2 entries", toolCalls)
	}
	if toolCalls[0] != "get_active_connections" {
		t.Errorf("tool_calls[0] = %v, want get_active_connections", toolCalls[0])
	}
}

func TestHandleGetEvent_WithTraceAndExplanation(t *testing.T) {
	store := newTestAuditStore(t)
	gs := &governanceServer{auditStore: store}

	// Simulate what agentutil.CheckTool stores — trace as raw JSON plus a human explanation.
	traceJSON := json.RawMessage(`{"decision":{"effect":"deny","policy_name":"prod-policy"},"default_applied":false}`)
	event := &audit.Event{
		EventID:   "evt-handler-trace",
		Timestamp: time.Now(),
		EventType: audit.EventTypePolicyDecision,
		Session:   audit.Session{ID: "sess-test"},
		PolicyDecision: &audit.PolicyDecision{
			ResourceType: "database",
			ResourceName: "prod-db",
			Action:       "write",
			Effect:       "deny",
			PolicyName:   "prod-policy",
			Trace:        traceJSON,
			Explanation:  "Access DENIED: writes to production are prohibited by prod-policy",
		},
	}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatalf("Record: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/events/evt-handler-trace", nil)
	req.SetPathValue("eventID", "evt-handler-trace")
	w := httptest.NewRecorder()
	gs.handleGetEvent(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)

	pd, _ := result["policy_decision"].(map[string]any)
	if pd == nil {
		t.Fatal("policy_decision missing from response")
	}
	// Trace must survive the store round-trip (stored as part of JSON blob).
	if _, ok := pd["trace"]; !ok {
		t.Error("policy_decision.trace field missing — trace not persisted through store round-trip")
	}
	// Explanation must survive too.
	if expl, _ := pd["explanation"].(string); !strings.Contains(expl, "DENIED") {
		t.Errorf("policy_decision.explanation missing or truncated, got: %q", expl)
	}
}

// --- handlePolicyCheck ---

const approvalPolicyYAML = `
version: "1"
policies:
  - name: approval-policy
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
        conditions:
          require_approval: true
`

func TestHandlePolicyCheck_NoPolicyEngine(t *testing.T) {
	gs := &governanceServer{} // nil engine, nil store — returns before touching either

	body := strings.NewReader(`{"resource_type":"database","resource_name":"prod-db","action":"write"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/governance/check", body)
	w := httptest.NewRecorder()
	gs.handlePolicyCheck(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandlePolicyCheck_MissingParams(t *testing.T) {
	gs := &governanceServer{
		policyEngine: makeEngine(t, minimalPolicyYAML),
		auditStore:   newTestAuditStore(t),
	}

	cases := []struct {
		name string
		body string
	}{
		{"missing resource_type", `{"resource_name":"prod-db","action":"write"}`},
		{"missing resource_name", `{"resource_type":"database","action":"write"}`},
		{"missing action", `{"resource_type":"database","resource_name":"prod-db"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(tc.body)
			req := httptest.NewRequest(http.MethodPost, "/v1/governance/check", body)
			w := httptest.NewRecorder()
			gs.handlePolicyCheck(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", tc.name, w.Code)
			}
		})
	}
}

func TestHandlePolicyCheck_Allow(t *testing.T) {
	store := newTestAuditStore(t)
	gs := &governanceServer{
		policyEngine: makeEngine(t, minimalPolicyYAML),
		auditStore:   store,
	}

	body := strings.NewReader(`{"resource_type":"database","resource_name":"dev-db","action":"read","session_id":"sess-allow","trace_id":"trace-allow"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/governance/check", body)
	w := httptest.NewRecorder()
	gs.handlePolicyCheck(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp PolicyCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Effect != "allow" {
		t.Errorf("Effect = %q, want allow", resp.Effect)
	}
	if resp.EventID == "" {
		t.Error("event_id should be set in response")
	}
	if !strings.HasPrefix(resp.EventID, "pol_") {
		t.Errorf("event_id = %q, want pol_* prefix", resp.EventID)
	}
	if resp.Explanation == "" {
		t.Error("explanation should be non-empty")
	}

	// Verify the pol_* event was persisted in the store.
	events, err := store.Query(context.Background(), audit.QueryOptions{EventID: resp.EventID, Limit: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("pol_* audit event not persisted in store")
	}
	pd := events[0].PolicyDecision
	if pd == nil {
		t.Fatal("persisted event missing policy_decision")
	}
	if pd.Effect != "allow" {
		t.Errorf("persisted effect = %q, want allow", pd.Effect)
	}
	if pd.ResourceType != "database" {
		t.Errorf("persisted resource_type = %q, want database", pd.ResourceType)
	}
	if pd.ResourceName != "dev-db" {
		t.Errorf("persisted resource_name = %q, want dev-db", pd.ResourceName)
	}
}

func TestHandlePolicyCheck_Deny(t *testing.T) {
	store := newTestAuditStore(t)
	gs := &governanceServer{
		policyEngine: makeEngine(t, minimalPolicyYAML),
		auditStore:   store,
	}

	body := strings.NewReader(`{"resource_type":"database","resource_name":"prod-db","action":"write"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/governance/check", body)
	w := httptest.NewRecorder()
	gs.handlePolicyCheck(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", w.Code, w.Body.String())
	}

	var resp PolicyCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Effect != "deny" {
		t.Errorf("Effect = %q, want deny", resp.Effect)
	}
	if !strings.Contains(resp.Message, "writes not allowed") {
		t.Errorf("Message = %q, want to contain 'writes not allowed'", resp.Message)
	}
	if !strings.Contains(resp.Explanation, "DENIED") {
		t.Errorf("Explanation = %q, want to contain DENIED", resp.Explanation)
	}

	// Verify the deny event was persisted.
	events, err := store.Query(context.Background(), audit.QueryOptions{EventID: resp.EventID, Limit: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("deny pol_* audit event not persisted in store")
	}
	if events[0].PolicyDecision.Effect != "deny" {
		t.Errorf("persisted effect = %q, want deny", events[0].PolicyDecision.Effect)
	}
}

func TestHandlePolicyCheck_RequireApproval(t *testing.T) {
	store := newTestAuditStore(t)
	gs := &governanceServer{
		policyEngine: makeEngine(t, approvalPolicyYAML),
		auditStore:   store,
	}

	body := strings.NewReader(`{"resource_type":"database","resource_name":"prod-db","action":"write"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/governance/check", body)
	w := httptest.NewRecorder()
	gs.handlePolicyCheck(w, req)

	// require_approval is not a deny — should return 200.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp PolicyCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Effect != "require_approval" {
		t.Errorf("Effect = %q, want require_approval", resp.Effect)
	}
	if !resp.RequiresApproval {
		t.Error("requires_approval should be true")
	}
	if resp.EventID == "" {
		t.Error("event_id should be set in response")
	}
}

func TestHandlePolicyCheck_TagsAutoResolved(t *testing.T) {
	const prodDenyYAML = `
version: "1"
policies:
  - name: prod-deny-write
    resources:
      - type: database
        match:
          tags: [production]
    rules:
      - action: write
        effect: deny
        message: "production writes denied"
  - name: default-allow
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
`
	ic := &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-db": {
				Name: "Production DB",
				Tags: []string{"production"},
			},
		},
	}
	store := newTestAuditStore(t)
	gs := &governanceServer{
		policyEngine: makeEngine(t, prodDenyYAML),
		auditStore:   store,
		infraConfig:  ic,
	}

	// Request carries no tags — the handler must resolve "production" from infraConfig.
	body := strings.NewReader(`{"resource_type":"database","resource_name":"prod-db","action":"write"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/governance/check", body)
	w := httptest.NewRecorder()
	gs.handlePolicyCheck(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (tags should have been auto-resolved to production); body: %s", w.Code, w.Body.String())
	}

	var resp PolicyCheckResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Effect != "deny" {
		t.Errorf("Effect = %q, want deny", resp.Effect)
	}

	// The persisted event should carry the resolved tag.
	events, err := store.Query(context.Background(), audit.QueryOptions{EventID: resp.EventID, Limit: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("pol_* event not persisted")
	}
	found := false
	for _, tag := range events[0].PolicyDecision.Tags {
		if tag == "production" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("persisted tags = %v, want to include 'production'", events[0].PolicyDecision.Tags)
	}
}

func TestHandlePolicyCheck_SessionIDFallback(t *testing.T) {
	store := newTestAuditStore(t)
	gs := &governanceServer{
		policyEngine: makeEngine(t, minimalPolicyYAML),
		auditStore:   store,
	}

	// No session_id — trace_id should be used as session fallback.
	body := strings.NewReader(`{"resource_type":"database","resource_name":"dev-db","action":"read","trace_id":"trace-xyz"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/governance/check", body)
	w := httptest.NewRecorder()
	gs.handlePolicyCheck(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp PolicyCheckResponse
	json.NewDecoder(w.Body).Decode(&resp)

	events, err := store.Query(context.Background(), audit.QueryOptions{EventID: resp.EventID, Limit: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("event not persisted")
	}
	if events[0].Session.ID != "trace-xyz" {
		t.Errorf("session_id = %q, want trace-xyz (fallback to trace_id)", events[0].Session.ID)
	}
	if events[0].TraceID != "trace-xyz" {
		t.Errorf("trace_id = %q, want trace-xyz", events[0].TraceID)
	}
}

func TestHandlePolicyCheck_AgentWithoutTraceID_Returns400(t *testing.T) {
	gs := &governanceServer{
		policyEngine: makeEngine(t, minimalPolicyYAML),
		auditStore:   newTestAuditStore(t),
	}

	// agent_name present, trace_id absent — must be rejected.
	body := strings.NewReader(`{"resource_type":"database","resource_name":"dev-db","action":"read","agent_name":"db_agent"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/governance/check", body)
	w := httptest.NewRecorder()
	gs.handlePolicyCheck(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var errResp map[string]string
	json.NewDecoder(w.Body).Decode(&errResp)
	if !strings.Contains(errResp["error"], "trace_id") {
		t.Errorf("error message should mention trace_id, got: %q", errResp["error"])
	}
}

func TestHandlePolicyCheck_DirectCallAutoGeneratesChkTraceID(t *testing.T) {
	store := newTestAuditStore(t)
	gs := &governanceServer{
		policyEngine: makeEngine(t, minimalPolicyYAML),
		auditStore:   store,
	}

	// No agent_name, no trace_id — should succeed with a synthetic chk_* trace_id.
	body := strings.NewReader(`{"resource_type":"database","resource_name":"dev-db","action":"read"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/governance/check", body)
	w := httptest.NewRecorder()
	gs.handlePolicyCheck(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp PolicyCheckResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if !strings.HasPrefix(resp.TraceID, "chk_") {
		t.Errorf("trace_id = %q, want chk_* prefix for direct call", resp.TraceID)
	}

	// Event should be recorded with the synthetic trace_id.
	events, err := store.Query(context.Background(), audit.QueryOptions{EventID: resp.EventID, Limit: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("event not persisted")
	}
	if events[0].TraceID != resp.TraceID {
		t.Errorf("stored trace_id = %q, want %q", events[0].TraceID, resp.TraceID)
	}
}
