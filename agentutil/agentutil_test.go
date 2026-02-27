package agentutil

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"

	"helpdesk/internal/policy"
)

// minimalPolicyYAML is a valid policy file used across policy tests.
const minimalPolicyYAML = `
version: "1"
policies:
  - name: test-policy
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
      - action: write
        effect: deny
      - action: destructive
        effect: deny
        message: "not allowed"
`

func writeTempPolicyFile(t *testing.T, content string) string {
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

// blastRadiusPolicyYAML allows writes with max 100 rows affected.
const blastRadiusPolicyYAML = `
version: "1"
policies:
  - name: blast-radius-policy
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
        conditions:
          max_rows_affected: 100
`

// newBlastRadiusEnforcer creates a PolicyEnforcer backed by the blast-radius policy.
func newBlastRadiusEnforcer(t *testing.T) *PolicyEnforcer {
	t.Helper()
	path := writeTempPolicyFile(t, blastRadiusPolicyYAML)
	engine, err := InitPolicyEngine(Config{PolicyEnabled: true, PolicyFile: path, DefaultPolicy: "deny"})
	if err != nil {
		t.Fatalf("InitPolicyEngine: %v", err)
	}
	return NewPolicyEnforcerWithConfig(PolicyEnforcerConfig{Engine: engine})
}

// --- CheckResult ---

func TestCheckResult_NilEngine(t *testing.T) {
	e := &PolicyEnforcer{} // no engine
	err := e.CheckResult(context.Background(), "database", "mydb", policy.ActionWrite, nil, ToolOutcome{RowsAffected: 9999})
	if err != nil {
		t.Errorf("nil engine should be no-op, got: %v", err)
	}
}

func TestCheckResult_SkipsWhenToolFailed(t *testing.T) {
	e := newBlastRadiusEnforcer(t)
	// Tool errored out — nothing was actually executed, blast-radius is irrelevant.
	err := e.CheckResult(context.Background(), "database", "mydb", policy.ActionWrite, nil, ToolOutcome{
		RowsAffected: 9999,
		Err:          errors.New("psql failed"),
	})
	if err != nil {
		t.Errorf("should be no-op when outcome.Err is set, got: %v", err)
	}
}

func TestCheckResult_SkipsReadWithZeroRows(t *testing.T) {
	e := newBlastRadiusEnforcer(t)
	// Pure read with nothing measured — fast path exit.
	err := e.CheckResult(context.Background(), "database", "mydb", policy.ActionRead, nil, ToolOutcome{
		RowsAffected: 0,
		PodsAffected: 0,
	})
	if err != nil {
		t.Errorf("read with 0 rows/pods should be no-op, got: %v", err)
	}
}

func TestCheckResult_AllowsWriteWithinLimit(t *testing.T) {
	e := newBlastRadiusEnforcer(t)
	err := e.CheckResult(context.Background(), "database", "mydb", policy.ActionWrite, nil, ToolOutcome{
		RowsAffected: 50, // under the 100-row limit
	})
	if err != nil {
		t.Errorf("50 rows should be within blast-radius limit, got: %v", err)
	}
}

func TestCheckResult_DeniesWriteExceedingLimit(t *testing.T) {
	e := newBlastRadiusEnforcer(t)
	err := e.CheckResult(context.Background(), "database", "mydb", policy.ActionWrite, nil, ToolOutcome{
		RowsAffected: 150, // over the 100-row limit
	})
	if err == nil {
		t.Fatal("150 rows exceeds blast-radius limit, expected denial error")
	}
	if !policy.IsDenied(err) {
		t.Errorf("expected DeniedError, got: %T %v", err, err)
	}
}

func TestCheckResult_DenialMessageContainsRowCount(t *testing.T) {
	e := newBlastRadiusEnforcer(t)
	err := e.CheckResult(context.Background(), "database", "mydb", policy.ActionWrite, nil, ToolOutcome{
		RowsAffected: 1500,
	})
	if err == nil {
		t.Fatal("expected denial error")
	}
	msg := err.Error()
	if msg == "" {
		t.Error("denial error message is empty")
	}
	// The message should mention the actual row count and the limit.
	for _, want := range []string{"1500", "100"} {
		if !containsStr(msg, want) {
			t.Errorf("denial message %q missing %q", msg, want)
		}
	}
}

func TestCheckDatabaseResult_ConvenienceWrapper(t *testing.T) {
	e := newBlastRadiusEnforcer(t)
	// CheckDatabaseResult is a thin wrapper — just verify it routes correctly.
	withinErr := e.CheckDatabaseResult(context.Background(), "mydb", policy.ActionWrite, nil, ToolOutcome{RowsAffected: 10})
	if withinErr != nil {
		t.Errorf("10 rows within limit: unexpected error: %v", withinErr)
	}
	exceededErr := e.CheckDatabaseResult(context.Background(), "mydb", policy.ActionWrite, nil, ToolOutcome{RowsAffected: 9999})
	if exceededErr == nil {
		t.Error("9999 rows: expected denial error")
	}
}

// --- CheckTool ---

// newMinimalEnforcer creates a PolicyEnforcer backed by minimalPolicyYAML.
func newMinimalEnforcer(t *testing.T) *PolicyEnforcer {
	t.Helper()
	path := writeTempPolicyFile(t, minimalPolicyYAML)
	engine, err := InitPolicyEngine(Config{PolicyEnabled: true, PolicyFile: path, DefaultPolicy: "deny"})
	if err != nil {
		t.Fatalf("InitPolicyEngine: %v", err)
	}
	return NewPolicyEnforcerWithConfig(PolicyEnforcerConfig{Engine: engine})
}

func TestCheckTool_AllowReturnsNil(t *testing.T) {
	e := newMinimalEnforcer(t)
	err := e.CheckTool(context.Background(), "database", "mydb", policy.ActionRead, nil, "unit test")
	if err != nil {
		t.Errorf("read action should be allowed, got: %v", err)
	}
}

func TestCheckTool_DeniedError_HasExplanation(t *testing.T) {
	e := newMinimalEnforcer(t)
	err := e.CheckTool(context.Background(), "database", "mydb", policy.ActionDestructive, nil, "unit test")
	if err == nil {
		t.Fatal("destructive action should be denied")
	}

	var de *policy.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("expected *policy.DeniedError, got %T: %v", err, err)
	}
	if de.Explanation == "" {
		t.Error("DeniedError.Explanation should be non-empty")
	}
	if !containsStr(de.Explanation, "DENIED") {
		t.Errorf("Explanation should contain DENIED, got: %s", de.Explanation)
	}
}

func TestCheckResult_DeniedError_HasExplanation(t *testing.T) {
	e := newBlastRadiusEnforcer(t)
	err := e.CheckResult(context.Background(), "database", "mydb", policy.ActionWrite, nil, ToolOutcome{
		RowsAffected: 150, // over the 100-row limit
	})
	if err == nil {
		t.Fatal("150 rows exceeds blast-radius limit, expected denial error")
	}

	var de *policy.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("expected *policy.DeniedError, got %T: %v", err, err)
	}
	if de.Explanation == "" {
		t.Error("DeniedError.Explanation should be non-empty for blast-radius denial")
	}
	// The explanation should contain both the actual count and the limit.
	for _, want := range []string{"150", "100"} {
		if !containsStr(de.Explanation, want) {
			t.Errorf("Explanation missing %q, got: %s", want, de.Explanation)
		}
	}
}

// --- Remote policy check (CheckTool + CheckResult via mock auditd) ---

// newRemoteEnforcer creates a PolicyEnforcer that calls the provided URL for policy checks.
func newRemoteEnforcer(url string) *PolicyEnforcer {
	return NewPolicyEnforcerWithConfig(PolicyEnforcerConfig{
		PolicyCheckURL: url,
	})
}

// mockPolicyCheckServer starts an httptest server that returns the given effect.
// It returns the server; callers should defer server.Close().
func mockPolicyCheckServer(t *testing.T, effect string, httpStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/governance/check" || r.Method != http.MethodPost {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		resp := policyCheckResp{
			Effect:      effect,
			PolicyName:  "mock-policy",
			Message:     "mock message",
			Explanation: "mock explanation: " + strings.ToUpper(effect),
			EventID:     "pol_mock0001",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestCheckTool_RemoteCheck_Allow(t *testing.T) {
	srv := mockPolicyCheckServer(t, "allow", http.StatusOK)
	defer srv.Close()

	e := newRemoteEnforcer(srv.URL)
	err := e.CheckTool(context.Background(), "database", "dev-db", policy.ActionRead, nil, "unit test")
	if err != nil {
		t.Errorf("allow: expected nil error, got: %v", err)
	}
}

func TestCheckTool_RemoteCheck_Deny(t *testing.T) {
	srv := mockPolicyCheckServer(t, "deny", http.StatusForbidden)
	defer srv.Close()

	e := newRemoteEnforcer(srv.URL)
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionWrite, nil, "unit test")
	if err == nil {
		t.Fatal("deny: expected error, got nil")
	}
	var de *policy.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("deny: expected *policy.DeniedError, got %T: %v", err, err)
	}
	if de.Decision.PolicyName != "mock-policy" {
		t.Errorf("policy_name = %q, want mock-policy", de.Decision.PolicyName)
	}
	if !containsStr(de.Explanation, "DENY") {
		t.Errorf("Explanation = %q, want to contain DENY", de.Explanation)
	}
}

func TestCheckTool_RemoteCheck_Unreachable(t *testing.T) {
	// Point to a port with no listener — should fail closed.
	e := newRemoteEnforcer("http://127.0.0.1:19999")
	err := e.CheckTool(context.Background(), "database", "dev-db", policy.ActionRead, nil, "unit test")
	if err == nil {
		t.Fatal("unreachable service: expected error, got nil")
	}
	if !containsStr(err.Error(), "policy service unreachable") {
		t.Errorf("error = %q, want to contain 'policy service unreachable'", err.Error())
	}
}

func TestCheckTool_RemoteCheck_RequireApproval_NoClient(t *testing.T) {
	srv := mockPolicyCheckServer(t, "require_approval", http.StatusOK)
	defer srv.Close()

	e := newRemoteEnforcer(srv.URL) // no approvalClient
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionWrite, nil, "unit test")
	if err == nil {
		t.Fatal("require_approval: expected error, got nil")
	}
	var are *policy.ApprovalRequiredError
	if !errors.As(err, &are) {
		t.Fatalf("require_approval: expected *policy.ApprovalRequiredError, got %T: %v", err, err)
	}
	if are.Decision.PolicyName != "mock-policy" {
		t.Errorf("policy_name = %q, want mock-policy", are.Decision.PolicyName)
	}
}

func TestCheckResult_RemoteCheck_Allow(t *testing.T) {
	srv := mockPolicyCheckServer(t, "allow", http.StatusOK)
	defer srv.Close()

	e := newRemoteEnforcer(srv.URL)
	err := e.CheckResult(context.Background(), "database", "prod-db", policy.ActionWrite,
		nil, ToolOutcome{RowsAffected: 50})
	if err != nil {
		t.Errorf("allow: expected nil error, got: %v", err)
	}
}

func TestCheckResult_RemoteCheck_Deny(t *testing.T) {
	srv := mockPolicyCheckServer(t, "deny", http.StatusForbidden)
	defer srv.Close()

	e := newRemoteEnforcer(srv.URL)
	err := e.CheckResult(context.Background(), "database", "prod-db", policy.ActionWrite,
		nil, ToolOutcome{RowsAffected: 9999})
	if err == nil {
		t.Fatal("deny: expected error, got nil")
	}
	var de *policy.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("deny: expected *policy.DeniedError, got %T: %v", err, err)
	}
}

func TestCheckResult_RemoteCheck_SkipsWhenToolFailed(t *testing.T) {
	// Even with remote enforcer, a tool error should bypass the check.
	e := newRemoteEnforcer("http://127.0.0.1:19999") // unreachable — but should never be called
	err := e.CheckResult(context.Background(), "database", "prod-db", policy.ActionWrite,
		nil, ToolOutcome{Err: errors.New("psql failed"), RowsAffected: 9999})
	if err != nil {
		t.Errorf("tool failure should skip check, got: %v", err)
	}
}

func TestCheckResult_RemoteCheck_SkipsPureReadWithZeroRows(t *testing.T) {
	e := newRemoteEnforcer("http://127.0.0.1:19999") // unreachable — should never be called
	err := e.CheckResult(context.Background(), "database", "dev-db", policy.ActionRead,
		nil, ToolOutcome{RowsAffected: 0, PodsAffected: 0})
	if err != nil {
		t.Errorf("pure read with zero rows should skip check, got: %v", err)
	}
}

func TestCheckTool_NilEnforcer_NoRemoteURL(t *testing.T) {
	// Neither engine nor remote URL — no-op.
	e := &PolicyEnforcer{}
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionDestructive, nil, "test")
	if err != nil {
		t.Errorf("nil enforcer: expected nil error, got: %v", err)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && stringContainsHelper(s, sub))
}

func stringContainsHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- InitPolicyEngine ---

func TestInitPolicyEngine_Disabled(t *testing.T) {
	engine, err := InitPolicyEngine(Config{PolicyEnabled: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if engine != nil {
		t.Error("expected nil engine when policy is disabled")
	}
}

func TestInitPolicyEngine_EnabledNoFile(t *testing.T) {
	engine, err := InitPolicyEngine(Config{PolicyEnabled: true, PolicyFile: ""})
	if err == nil {
		t.Fatal("expected error when PolicyEnabled=true but no PolicyFile")
	}
	if engine != nil {
		t.Error("expected nil engine on error")
	}
}

func TestInitPolicyEngine_EnabledWithValidFile(t *testing.T) {
	path := writeTempPolicyFile(t, minimalPolicyYAML)
	engine, err := InitPolicyEngine(Config{PolicyEnabled: true, PolicyFile: path, DefaultPolicy: "deny"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if engine == nil {
		t.Fatal("expected non-nil engine for valid policy file")
	}
}

func TestInitPolicyEngine_EnabledWithNonexistentFile(t *testing.T) {
	engine, err := InitPolicyEngine(Config{PolicyEnabled: true, PolicyFile: "/nonexistent/policies.yaml"})
	if err == nil {
		t.Fatal("expected error for nonexistent policy file")
	}
	if engine != nil {
		t.Error("expected nil engine on error")
	}
}

func TestInitPolicyEngine_RemoteMode_SkipsLocalEngine(t *testing.T) {
	// PolicyCheckURL set → remote mode; nil engine expected, no error.
	engine, err := InitPolicyEngine(Config{
		PolicyEnabled:  true,
		PolicyCheckURL: "http://auditd:1199",
		// Deliberately no PolicyFile — not needed in remote mode.
	})
	if err != nil {
		t.Fatalf("unexpected error in remote mode: %v", err)
	}
	if engine != nil {
		t.Error("expected nil engine in remote check mode")
	}
}

func TestInitPolicyEngine_DryRunPassedThrough(t *testing.T) {
	path := writeTempPolicyFile(t, minimalPolicyYAML)
	// Dry-run should not return an error — the engine is still created.
	engine, err := InitPolicyEngine(Config{PolicyEnabled: true, PolicyFile: path, PolicyDryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if engine == nil {
		t.Fatal("expected non-nil engine even in dry-run mode")
	}
}

// --- MustLoadConfig: policy-related env vars ---

func setRequiredModelEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HELPDESK_MODEL_VENDOR", "anthropic")
	t.Setenv("HELPDESK_MODEL_NAME", "claude-test")
	t.Setenv("HELPDESK_API_KEY", "test-key")
}

func TestMustLoadConfig_PolicyEnabled(t *testing.T) {
	setRequiredModelEnv(t)
	t.Setenv("HELPDESK_POLICY_ENABLED", "true")
	t.Setenv("HELPDESK_POLICY_FILE", "/tmp/policies.yaml")

	cfg := MustLoadConfig(":9999")
	if !cfg.PolicyEnabled {
		t.Error("expected PolicyEnabled=true")
	}
	if cfg.PolicyFile != "/tmp/policies.yaml" {
		t.Errorf("PolicyFile = %q, want /tmp/policies.yaml", cfg.PolicyFile)
	}
}

func TestMustLoadConfig_PolicyDisabled(t *testing.T) {
	setRequiredModelEnv(t)
	t.Setenv("HELPDESK_POLICY_ENABLED", "false")

	cfg := MustLoadConfig(":9999")
	if cfg.PolicyEnabled {
		t.Error("expected PolicyEnabled=false")
	}
}

func TestMustLoadConfig_PolicyDisabledExplicitlyOverridesFile(t *testing.T) {
	setRequiredModelEnv(t)
	t.Setenv("HELPDESK_POLICY_ENABLED", "false")
	t.Setenv("HELPDESK_POLICY_FILE", "/tmp/policies.yaml")

	cfg := MustLoadConfig(":9999")
	if cfg.PolicyEnabled {
		t.Error("explicit HELPDESK_POLICY_ENABLED=false must override file presence")
	}
}

func TestMustLoadConfig_PolicyBackwardCompat_FileSetNoFlag(t *testing.T) {
	setRequiredModelEnv(t)
	t.Setenv("HELPDESK_POLICY_FILE", "/tmp/policies.yaml")
	// HELPDESK_POLICY_ENABLED deliberately not set.

	cfg := MustLoadConfig(":9999")
	if !cfg.PolicyEnabled {
		t.Error("PolicyEnabled should be inferred as true when HELPDESK_POLICY_FILE is set without the flag")
	}
}

func TestMustLoadConfig_PolicyNotSetAtAll(t *testing.T) {
	setRequiredModelEnv(t)
	// Neither HELPDESK_POLICY_ENABLED nor HELPDESK_POLICY_FILE set.

	cfg := MustLoadConfig(":9999")
	if cfg.PolicyEnabled {
		t.Error("expected PolicyEnabled=false when nothing is set")
	}
	if cfg.PolicyFile != "" {
		t.Errorf("expected empty PolicyFile, got %q", cfg.PolicyFile)
	}
}

func TestMustLoadConfig_PolicyCheckURL_SetFromAuditURL(t *testing.T) {
	setRequiredModelEnv(t)
	t.Setenv("HELPDESK_POLICY_ENABLED", "true")
	t.Setenv("HELPDESK_POLICY_FILE", "/tmp/policies.yaml")
	t.Setenv("HELPDESK_AUDIT_URL", "http://auditd:1199")

	cfg := MustLoadConfig(":9999")
	if cfg.PolicyCheckURL != "http://auditd:1199" {
		t.Errorf("PolicyCheckURL = %q, want http://auditd:1199 (set from AuditURL)", cfg.PolicyCheckURL)
	}
}

func TestMustLoadConfig_PolicyCheckURL_NotSetWhenPolicyDisabled(t *testing.T) {
	setRequiredModelEnv(t)
	t.Setenv("HELPDESK_POLICY_ENABLED", "false")
	t.Setenv("HELPDESK_AUDIT_URL", "http://auditd:1199")

	cfg := MustLoadConfig(":9999")
	if cfg.PolicyCheckURL != "" {
		t.Errorf("PolicyCheckURL = %q, want empty (policy disabled)", cfg.PolicyCheckURL)
	}
}

func TestMustLoadConfig_AuditEnabled(t *testing.T) {
	setRequiredModelEnv(t)
	t.Setenv("HELPDESK_AUDIT_ENABLED", "true")
	t.Setenv("HELPDESK_AUDIT_URL", "http://localhost:1199")

	cfg := MustLoadConfig(":9999")
	if !cfg.AuditEnabled {
		t.Error("expected AuditEnabled=true")
	}
	if cfg.AuditURL != "http://localhost:1199" {
		t.Errorf("AuditURL = %q, want http://localhost:1199", cfg.AuditURL)
	}
}

func TestMustLoadConfig_AuditDisabledByDefault(t *testing.T) {
	setRequiredModelEnv(t)

	cfg := MustLoadConfig(":9999")
	if cfg.AuditEnabled {
		t.Error("expected AuditEnabled=false when HELPDESK_AUDIT_ENABLED is not set")
	}
}

func TestApplyCardOptions_Empty(t *testing.T) {
	card := &a2a.AgentCard{
		Name:    "test",
		Version: "0.1.0",
	}
	applyCardOptions(card, CardOptions{})

	if card.Version != "0.1.0" {
		t.Errorf("Version changed to %q, expected no change", card.Version)
	}
}

func TestApplyCardOptions_Version(t *testing.T) {
	card := &a2a.AgentCard{Name: "test", Version: "0.1.0"}
	applyCardOptions(card, CardOptions{Version: "2.0.0"})
	if card.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", card.Version, "2.0.0")
	}
}

func TestApplyCardOptions_DocumentationURL(t *testing.T) {
	card := &a2a.AgentCard{Name: "test"}
	applyCardOptions(card, CardOptions{DocumentationURL: "https://docs.example.com"})
	if card.DocumentationURL != "https://docs.example.com" {
		t.Errorf("DocumentationURL = %q, want %q", card.DocumentationURL, "https://docs.example.com")
	}
}

func TestApplyCardOptions_Provider(t *testing.T) {
	card := &a2a.AgentCard{Name: "test"}
	provider := &a2a.AgentProvider{Org: "TestOrg", URL: "https://test.org"}
	applyCardOptions(card, CardOptions{Provider: provider})
	if card.Provider == nil {
		t.Fatal("Provider should be set")
	}
	if card.Provider.Org != "TestOrg" {
		t.Errorf("Provider.Org = %q, want %q", card.Provider.Org, "TestOrg")
	}
}

func TestApplyCardOptions_SkillTagsMerged(t *testing.T) {
	card := &a2a.AgentCard{
		Name: "test",
		Skills: []a2a.AgentSkill{
			{ID: "skill-a", Tags: []string{"existing"}},
			{ID: "skill-b", Tags: []string{"b-tag"}},
		},
	}
	applyCardOptions(card, CardOptions{
		SkillTags: map[string][]string{
			"skill-a": {"new-tag-1", "new-tag-2"},
		},
	})
	if len(card.Skills[0].Tags) != 3 {
		t.Fatalf("skill-a tags = %v, want 3 tags", card.Skills[0].Tags)
	}
	if card.Skills[0].Tags[0] != "existing" || card.Skills[0].Tags[1] != "new-tag-1" {
		t.Errorf("skill-a tags = %v, unexpected order", card.Skills[0].Tags)
	}
	// skill-b should be unchanged.
	if len(card.Skills[1].Tags) != 1 {
		t.Errorf("skill-b tags = %v, expected unchanged", card.Skills[1].Tags)
	}
}

func TestApplyCardOptions_SkillExamples(t *testing.T) {
	card := &a2a.AgentCard{
		Name: "test",
		Skills: []a2a.AgentSkill{
			{ID: "skill-a", Examples: []string{"old example"}},
		},
	}
	applyCardOptions(card, CardOptions{
		SkillExamples: map[string][]string{
			"skill-a": {"example 1", "example 2"},
		},
	})
	if len(card.Skills[0].Examples) != 2 {
		t.Fatalf("skill-a examples = %v, want 2", card.Skills[0].Examples)
	}
	if card.Skills[0].Examples[0] != "example 1" {
		t.Errorf("examples[0] = %q, want %q", card.Skills[0].Examples[0], "example 1")
	}
}
