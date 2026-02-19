package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

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
