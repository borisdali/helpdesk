package agentutil

import (
	"os"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
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
