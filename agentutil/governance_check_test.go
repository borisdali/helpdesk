package agentutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

// setFixModeEnv sets HELPDESK_OPERATING_MODE=fix for the duration of the test.
func setFixModeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HELPDESK_OPERATING_MODE", "fix")
}

// fullFixModeConfig returns a Config with all five governance modules satisfied.
func fullFixModeConfig(t *testing.T) Config {
	t.Helper()
	path := writeTempPolicyFile(t, minimalPolicyYAML)
	t.Setenv("HELPDESK_INFRA_CONFIG", "/tmp/infra.json") // just needs to be non-empty
	return Config{
		AuditEnabled:    true,
		AuditURL:        "http://auditd:1199",
		PolicyEnabled:   true,
		PolicyFile:      path,
		PolicyDryRun:    false,
		ApprovalEnabled: true,
	}
}

// --- CheckFixModeViolations ---

func TestCheckFixModeViolations_NotInFixMode(t *testing.T) {
	// No HELPDESK_OPERATING_MODE set → no violations regardless of config.
	violations := CheckFixModeViolations(Config{})
	if len(violations) != 0 {
		t.Errorf("expected no violations outside fix mode, got %d: %v", len(violations), violations)
	}
}

func TestCheckFixModeViolations_ReadonlyMode(t *testing.T) {
	t.Setenv("HELPDESK_OPERATING_MODE", "readonly")
	violations := CheckFixModeViolations(Config{})
	if len(violations) != 0 {
		t.Errorf("expected no violations in readonly mode, got %d", len(violations))
	}
}

func TestCheckFixModeViolations_FullyCompliant(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	violations := CheckFixModeViolations(cfg)
	if len(violations) != 0 {
		t.Errorf("expected no violations with all modules configured, got %d: %v", len(violations), violations)
	}
}

// --- Audit module ---

func TestCheckFixModeViolations_AuditDisabled(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	cfg.AuditEnabled = false

	violations := CheckFixModeViolations(cfg)
	requireViolation(t, violations, "audit", "fatal")
}

func TestCheckFixModeViolations_AuditEnabledNoURL(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	cfg.AuditURL = ""

	violations := CheckFixModeViolations(cfg)
	requireViolation(t, violations, "audit", "fatal")
}

func TestCheckFixModeViolations_AuditEnabledWithURL_NoViolation(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)

	violations := CheckFixModeViolations(cfg)
	requireNoViolation(t, violations, "audit")
}

// --- Policy engine ---

func TestCheckFixModeViolations_PolicyDisabled(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	cfg.PolicyEnabled = false

	violations := CheckFixModeViolations(cfg)
	requireViolation(t, violations, "policy_engine", "fatal")
}

func TestCheckFixModeViolations_PolicyEnabledNoFile(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	cfg.PolicyFile = ""

	violations := CheckFixModeViolations(cfg)
	requireViolation(t, violations, "policy_engine", "fatal")
}

func TestCheckFixModeViolations_PolicyEnabledRemoteMode_NoFileNeeded(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	cfg.PolicyFile = ""               // no local file
	cfg.PolicyCheckURL = "http://auditd:1199" // remote check mode satisfies requirement

	violations := CheckFixModeViolations(cfg)
	requireNoViolation(t, violations, "policy_engine")
}

func TestCheckFixModeViolations_PolicyEnabledWithFile_NoViolation(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)

	violations := CheckFixModeViolations(cfg)
	requireNoViolation(t, violations, "policy_engine")
}

// --- Guardrails (dry-run) ---

func TestCheckFixModeViolations_DryRunEnabled(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	cfg.PolicyDryRun = true

	violations := CheckFixModeViolations(cfg)
	requireViolation(t, violations, "guardrails", "fatal")
}

func TestCheckFixModeViolations_DryRunDisabled_NoViolation(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	cfg.PolicyDryRun = false

	violations := CheckFixModeViolations(cfg)
	requireNoViolation(t, violations, "guardrails")
}

// --- Approval workflows (warning) ---

func TestCheckFixModeViolations_ApprovalsDisabled(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	cfg.ApprovalEnabled = false

	violations := CheckFixModeViolations(cfg)
	requireViolation(t, violations, "approval_workflows", "warning")
}

func TestCheckFixModeViolations_ApprovalsEnabled_NoViolation(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)

	violations := CheckFixModeViolations(cfg)
	requireNoViolation(t, violations, "approval_workflows")
}

// --- Explainability / infra config (warning) ---

func TestCheckFixModeViolations_InfraConfigMissing(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	t.Setenv("HELPDESK_INFRA_CONFIG", "") // clear what fullFixModeConfig set

	violations := CheckFixModeViolations(cfg)
	requireViolation(t, violations, "explainability", "warning")
}

func TestCheckFixModeViolations_InfraConfigSet_NoViolation(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t) // sets HELPDESK_INFRA_CONFIG

	violations := CheckFixModeViolations(cfg)
	requireNoViolation(t, violations, "explainability")
}

// --- Multiple violations ---

func TestCheckFixModeViolations_MultipleViolations(t *testing.T) {
	setFixModeEnv(t)
	// Bare config: nothing enabled.
	violations := CheckFixModeViolations(Config{})

	requireViolation(t, violations, "audit", "fatal")
	requireViolation(t, violations, "policy_engine", "fatal")
	// approval_workflows and explainability warnings also expected.
	requireViolation(t, violations, "approval_workflows", "warning")
	requireViolation(t, violations, "explainability", "warning")
}

func TestCheckFixModeViolations_OnlyWarnings_NoFatal(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	cfg.ApprovalEnabled = false
	t.Setenv("HELPDESK_INFRA_CONFIG", "")

	violations := CheckFixModeViolations(cfg)

	// Should have exactly two warning violations.
	if len(violations) != 2 {
		t.Fatalf("expected 2 violations, got %d: %v", len(violations), violations)
	}
	for _, v := range violations {
		if v.Severity != "warning" {
			t.Errorf("expected all violations to be warnings, got %q for module %q", v.Severity, v.Module)
		}
	}
}

// --- Violation fields ---

func TestCheckFixModeViolations_ViolationHasDescription(t *testing.T) {
	setFixModeEnv(t)
	cfg := fullFixModeConfig(t)
	cfg.AuditEnabled = false

	violations := CheckFixModeViolations(cfg)
	v := findViolation(violations, "audit")
	if v == nil {
		t.Fatal("expected audit violation")
	}
	if v.Description == "" {
		t.Error("violation Description should be non-empty")
	}
	if v.Remediation == "" {
		t.Error("violation Remediation should be non-empty")
	}
}

// --- CheckFixModeAuditViolations ---

func TestCheckFixModeAuditViolations_NotInFixMode(t *testing.T) {
	violations := CheckFixModeAuditViolations(false, "")
	if len(violations) != 0 {
		t.Errorf("expected no violations outside fix mode, got %d", len(violations))
	}
}

func TestCheckFixModeAuditViolations_AuditDisabled(t *testing.T) {
	setFixModeEnv(t)
	violations := CheckFixModeAuditViolations(false, "http://auditd:1199")
	requireViolation(t, violations, "audit", "fatal")
}

func TestCheckFixModeAuditViolations_AuditEnabledNoURL(t *testing.T) {
	setFixModeEnv(t)
	violations := CheckFixModeAuditViolations(true, "")
	requireViolation(t, violations, "audit", "fatal")
}

func TestCheckFixModeAuditViolations_AuditFullyConfigured(t *testing.T) {
	setFixModeEnv(t)
	violations := CheckFixModeAuditViolations(true, "http://auditd:1199")
	if len(violations) != 0 {
		t.Errorf("expected no violations when audit is fully configured, got %d", len(violations))
	}
}

func TestCheckFixModeAuditViolations_DoesNotCheckPolicy(t *testing.T) {
	// CheckFixModeAuditViolations should only report on the audit module,
	// not on policy/approvals/guardrails (those are for sub-agents).
	setFixModeEnv(t)
	// Audit is fully configured; policy is intentionally absent.
	violations := CheckFixModeAuditViolations(true, "http://auditd:1199")
	requireNoViolation(t, violations, "policy_engine")
	requireNoViolation(t, violations, "guardrails")
	requireNoViolation(t, violations, "approval_workflows")
	requireNoViolation(t, violations, "explainability")
}

// --- EnforceFixMode: no-op path ---

func TestEnforceFixMode_NoViolations_IsNoOp(t *testing.T) {
	// Should return without calling os.Exit.
	// If this test completes, the no-op path works.
	EnforceFixMode(context.Background(), nil, "test-component", "")
}

func TestEnforceFixMode_EmptyViolations_IsNoOp(t *testing.T) {
	EnforceFixMode(context.Background(), []FixModeViolation{}, "test-component", "")
}

func TestEnforceFixMode_WarningOnly_DoesNotExit(t *testing.T) {
	// Warning violations should log and return — not call os.Exit.
	// If this test completes, the non-fatal path works correctly.
	// auditURL and HELPDESK_GATEWAY_URL are empty so the best-effort HTTP
	// calls are skipped entirely.
	EnforceFixMode(context.Background(), []FixModeViolation{
		{Module: "approval_workflows", Severity: "warning", Description: "test"},
		{Module: "explainability", Severity: "warning", Description: "test"},
	}, "test-component", "")
}

// --- EnforceFixMode: fatal exit path (subprocess) ---
//
// Go tests cannot catch os.Exit in-process, so the fatal path is tested by
// re-running the test binary as a subprocess with a sentinel env var.
// The helper below is the code that runs inside the subprocess.

// TestFatalViolationHelper is a subprocess helper — not a standalone test.
// It calls EnforceFixMode with a fatal violation and expects the process to exit 1.
func TestFatalViolationHelper(t *testing.T) {
	if os.Getenv("FATAL_VIOLATION_HELPER") != "1" {
		t.Skip("subprocess helper — set FATAL_VIOLATION_HELPER=1 to run")
	}
	EnforceFixMode(context.Background(), []FixModeViolation{
		{Module: "audit", Severity: "fatal", Description: "audit disabled in fix mode"},
	}, "test-component", "")
	// Reaching here means os.Exit was not called — the parent test will detect
	// this via a non-1 exit code.
}

func TestEnforceFixMode_FatalViolation_ExitsWithCode1(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFatalViolationHelper", "-test.v")
	cmd.Env = append(os.Environ(), "FATAL_VIOLATION_HELPER=1")
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected subprocess to exit with an error, got: %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1, got %d\nstderr: %s", exitErr.ExitCode(), exitErr.Stderr)
	}
}

func TestEnforceFixMode_MixedViolations_ExitsWithCode1(t *testing.T) {
	// At least one fatal violation → must exit, even alongside warnings.
	cmd := exec.Command(os.Args[0], "-test.run=TestMixedViolationHelper", "-test.v")
	cmd.Env = append(os.Environ(), "MIXED_VIOLATION_HELPER=1")
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected subprocess to exit with an error, got: %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1, got %d", exitErr.ExitCode())
	}
}

// TestMixedViolationHelper is the subprocess helper for TestEnforceFixMode_MixedViolations_ExitsWithCode1.
func TestMixedViolationHelper(t *testing.T) {
	if os.Getenv("MIXED_VIOLATION_HELPER") != "1" {
		t.Skip("subprocess helper — set MIXED_VIOLATION_HELPER=1 to run")
	}
	EnforceFixMode(context.Background(), []FixModeViolation{
		{Module: "approval_workflows", Severity: "warning", Description: "no approvals"},
		{Module: "audit", Severity: "fatal", Description: "audit disabled"},
		{Module: "explainability", Severity: "warning", Description: "no infra config"},
	}, "test-component", "")
}

// --- helpers ---

// requireViolation asserts that violations contains an entry with the given module and severity.
func requireViolation(t *testing.T, violations []FixModeViolation, module, severity string) {
	t.Helper()
	v := findViolation(violations, module)
	if v == nil {
		t.Errorf("expected violation for module %q, not found in: %v", module, violations)
		return
	}
	if v.Severity != severity {
		t.Errorf("violation[%q].Severity = %q, want %q", module, v.Severity, severity)
	}
}

// requireNoViolation asserts that violations does NOT contain an entry for module.
func requireNoViolation(t *testing.T, violations []FixModeViolation, module string) {
	t.Helper()
	if v := findViolation(violations, module); v != nil {
		t.Errorf("unexpected violation for module %q (severity=%q): %s", module, v.Severity, v.Description)
	}
}

// findViolation returns the first violation for module, or nil.
func findViolation(violations []FixModeViolation, module string) *FixModeViolation {
	for i := range violations {
		if violations[i].Module == module {
			return &violations[i]
		}
	}
	return nil
}
