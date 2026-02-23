package agentutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"helpdesk/internal/audit"
)

// FixModeViolation describes a governance module that is required but not active
// when HELPDESK_OPERATING_MODE=fix.
type FixModeViolation struct {
	Module      string // e.g. "audit", "policy_engine", "guardrails"
	Severity    string // "fatal" (agent will not start) or "warning" (alerting only)
	Description string
	Remediation string
}

// CheckFixModeViolations validates that all required governance modules are active
// for fix mode operation. Returns nil if not in fix mode or if fully compliant.
//
// Checked modules:
//   - audit:              HELPDESK_AUDIT_ENABLED=true and HELPDESK_AUDIT_URL set
//   - policy_engine:      HELPDESK_POLICY_ENABLED=true and HELPDESK_POLICY_FILE set
//   - guardrails:         HELPDESK_POLICY_DRY_RUN must not be true
//   - approval_workflows: HELPDESK_APPROVAL_ENABLED=true (warning only)
//   - explainability:     HELPDESK_INFRA_CONFIG set (warning only)
func CheckFixModeViolations(cfg Config) []FixModeViolation {
	if strings.ToLower(os.Getenv("HELPDESK_OPERATING_MODE")) != "fix" {
		return nil
	}

	var v []FixModeViolation

	// 1. Audit system — tamper-evident log of every agent action.
	if !cfg.AuditEnabled {
		v = append(v, FixModeViolation{
			Module:      "audit",
			Severity:    "fatal",
			Description: "Audit logging is disabled. HELPDESK_AUDIT_ENABLED must be 'true' in fix mode.",
			Remediation: "Set HELPDESK_AUDIT_ENABLED=true and HELPDESK_AUDIT_URL to the auditd service URL.",
		})
	} else if cfg.AuditURL == "" {
		v = append(v, FixModeViolation{
			Module:      "audit",
			Severity:    "fatal",
			Description: "Central audit service URL is not set. HELPDESK_AUDIT_URL is required in fix mode.",
			Remediation: "Set HELPDESK_AUDIT_URL to the running auditd service URL (e.g. http://auditd:1199).",
		})
	}

	// 2. Policy engine — defines what actions are allowed on which resources.
	if !cfg.PolicyEnabled {
		v = append(v, FixModeViolation{
			Module:      "policy_engine",
			Severity:    "fatal",
			Description: "Policy enforcement is disabled. HELPDESK_POLICY_ENABLED must be 'true' in fix mode.",
			Remediation: "Set HELPDESK_POLICY_ENABLED=true and HELPDESK_POLICY_FILE to a valid policy YAML.",
		})
	} else if cfg.PolicyFile == "" {
		v = append(v, FixModeViolation{
			Module:      "policy_engine",
			Severity:    "fatal",
			Description: "Policy engine is enabled but HELPDESK_POLICY_FILE is not set.",
			Remediation: "Set HELPDESK_POLICY_FILE to a valid policy YAML file.",
		})
	}

	// 3. Guardrails — policy must actually enforce, not merely log decisions.
	if cfg.PolicyDryRun {
		v = append(v, FixModeViolation{
			Module:      "guardrails",
			Severity:    "fatal",
			Description: "Policy dry-run mode is active. In fix mode policies must be enforced, not just logged.",
			Remediation: "Unset HELPDESK_POLICY_DRY_RUN or set it to 'false'.",
		})
	}

	// 4. Approval workflows — human-in-the-loop for write/destructive operations.
	if !cfg.ApprovalEnabled {
		v = append(v, FixModeViolation{
			Module:      "approval_workflows",
			Severity:    "warning",
			Description: "Approval workflows are disabled. Risky fix-mode operations will not require human sign-off.",
			Remediation: "Set HELPDESK_APPROVAL_ENABLED=true.",
		})
	}

	// 5. Explainability — infrastructure config required for tag-based policy evaluation.
	if os.Getenv("HELPDESK_INFRA_CONFIG") == "" {
		v = append(v, FixModeViolation{
			Module:      "explainability",
			Severity:    "warning",
			Description: "HELPDESK_INFRA_CONFIG is not set. Policy decisions cannot auto-resolve resource tags.",
			Remediation: "Set HELPDESK_INFRA_CONFIG to the infrastructure JSON file path.",
		})
	}

	return v
}

// CheckFixModeAuditViolations validates the audit module for components that delegate
// policy enforcement to sub-agents (e.g. the helpdesk orchestrator). Only the audit
// system is checked — policy, approvals, and guardrails are enforced by each sub-agent.
func CheckFixModeAuditViolations(auditEnabled bool, auditURL string) []FixModeViolation {
	if strings.ToLower(os.Getenv("HELPDESK_OPERATING_MODE")) != "fix" {
		return nil
	}

	var v []FixModeViolation

	if !auditEnabled {
		v = append(v, FixModeViolation{
			Module:      "audit",
			Severity:    "fatal",
			Description: "Audit logging is disabled. HELPDESK_AUDIT_ENABLED must be 'true' in fix mode.",
			Remediation: "Set HELPDESK_AUDIT_ENABLED=true and HELPDESK_AUDIT_URL to the auditd service URL.",
		})
	} else if auditURL == "" {
		v = append(v, FixModeViolation{
			Module:      "audit",
			Severity:    "fatal",
			Description: "Central audit service URL is not set. HELPDESK_AUDIT_URL is required in fix mode.",
			Remediation: "Set HELPDESK_AUDIT_URL to the running auditd service URL (e.g. http://auditd:1199).",
		})
	}

	return v
}

// EnforceFixMode handles governance violations detected in fix mode.
//
// For each violation it:
//  1. Logs the violation (ERROR for fatal, WARN for warning)
//  2. Best-effort records a governance_violation audit event to auditd (if auditURL is set)
//  3. Best-effort creates an incident via the gateway (if HELPDESK_GATEWAY_URL is set)
//
// Fatal violations cause os.Exit(1) after attempting the above. Warning violations
// are reported but do not block startup.
//
// componentName identifies the process in reports (e.g. "postgres_database_agent").
func EnforceFixMode(ctx context.Context, violations []FixModeViolation, componentName, auditURL string) {
	if len(violations) == 0 {
		return
	}

	slog.Warn("fix mode governance violations detected",
		"component", componentName,
		"count", len(violations),
	)

	var hasFatal bool
	for _, v := range violations {
		if v.Severity == "fatal" {
			hasFatal = true
			slog.Error("governance violation",
				"component", componentName,
				"module", v.Module,
				"severity", "fatal",
				"description", v.Description,
				"remediation", v.Remediation,
			)
		} else {
			slog.Warn("governance violation",
				"component", componentName,
				"module", v.Module,
				"severity", "warning",
				"description", v.Description,
				"remediation", v.Remediation,
			)
		}
	}

	// Best-effort: record governance_violation events to the audit trail.
	if auditURL != "" {
		for _, v := range violations {
			recordGovernanceViolationEvent(ctx, auditURL, componentName, v)
		}
	}

	// Best-effort: create an incident via the gateway to alert operators.
	gatewayURL := os.Getenv("HELPDESK_GATEWAY_URL")
	if gatewayURL != "" {
		go createGovernanceIncident(gatewayURL, componentName, violations)
	}

	if hasFatal {
		slog.Error("fix mode governance check failed — process will not start",
			"component", componentName,
		)
		os.Exit(1)
	}
}

// recordGovernanceViolationEvent sends a single governance_violation event to auditd.
func recordGovernanceViolationEvent(ctx context.Context, auditURL, componentName string, v FixModeViolation) {
	event := &audit.Event{
		EventID:   "gov_" + uuid.New().String()[:8],
		Timestamp: time.Now().UTC(),
		EventType: audit.EventTypeGovernanceViolation,
		Session:   audit.Session{ID: componentName},
		GovernanceViolation: &audit.GovernanceViolation{
			OperatingMode: "fix",
			Module:        v.Module,
			Severity:      v.Severity,
			Description:   v.Description,
			Remediation:   v.Remediation,
		},
	}

	data, err := json.Marshal(event)
	if err != nil {
		slog.Warn("failed to marshal governance violation event", "module", v.Module, "err", err)
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(strings.TrimRight(auditURL, "/")+"/v1/events", "application/json", bytes.NewReader(data))
	if err != nil {
		slog.Warn("failed to send governance violation to auditd", "module", v.Module, "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("auditd rejected governance violation event",
			"module", v.Module,
			"status", resp.StatusCode,
			"body", strings.TrimSpace(string(body)),
		)
	}
}

// createGovernanceIncident POSTs to the gateway to open an incident for the violation.
func createGovernanceIncident(gatewayURL, componentName string, violations []FixModeViolation) {
	modules := make([]string, len(violations))
	for i, v := range violations {
		modules[i] = v.Module
	}

	desc := fmt.Sprintf(
		"Fix mode governance compliance violation in %s: required modules not configured: %s",
		componentName, strings.Join(modules, ", "),
	)

	payload := map[string]any{
		"infra_key":   "governance-violation",
		"description": desc,
		"layers":      []string{"governance"},
	}

	data, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(
		strings.TrimRight(gatewayURL, "/")+"/api/v1/incidents",
		"application/json",
		bytes.NewReader(data),
	)
	if err != nil {
		slog.Warn("failed to create governance incident", "component", componentName, "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 300 {
		slog.Info("governance incident created",
			"component", componentName,
			"modules", strings.Join(modules, ", "),
		)
	} else {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("gateway rejected governance incident request",
			"component", componentName,
			"status", resp.StatusCode,
			"body", strings.TrimSpace(string(body)),
		)
	}
}
