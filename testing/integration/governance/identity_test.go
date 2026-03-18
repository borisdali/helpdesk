//go:build integration

package governance

// Integration tests for Identity & Access propagation through the real auditd binary.
//
// Each test starts its own auditd instance on a dynamically chosen free port
// (to avoid conflicts with the primary instance on 19901 and to allow tests to
// run without port-collision worries), exercises the HTTP API, and then tears
// down the process via t.Cleanup.
//
// These tests exercise the full pipeline:
//   - POST /v1/governance/check with principal / purpose / sensitivity fields
//   - The live policy engine evaluating those fields against real policy rules
//   - The resulting pol_* audit event persisted in SQLite with all identity fields
//   - Retrieval of that event via GET /v1/events/{id}
//   - GET /v1/governance/explain with ?purpose=... and ?sensitivity=... query params

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// identityPolicyYAML exercises role-based, sensitivity-based, and purpose-based rules.
//
// Policy priority rationale:
//   200 emergency-break-glass — oncall+emergency overrides everything
//   110 pii-protection        — PII resources: reads require purpose, writes denied
//    80 dba-policy            — DBA can read freely; DBA can write unless purpose=diagnostic
//    10 default-policy        — everyone else: allow reads, deny writes
//
// Diagnostic-purpose write blocking is embedded in dba-policy (blocked_purposes: [diagnostic])
// rather than a separate policy, to avoid the policy evaluation ordering problem: a standalone
// "diagnostic-readonly" policy using effect:allow+blocked_purposes would inadvertently allow
// non-diagnostic writes for non-DBA principals before the default-deny rule is reached.
const identityPolicyYAML = `
version: "1"
policies:
  # Emergency break-glass: oncall + emergency purpose → always allow.
  - name: emergency-break-glass
    priority: 200
    principals:
      - role: oncall
    resources:
      - type: database
      - type: kubernetes
    rules:
      - action: [read, write, destructive]
        effect: allow
        conditions:
          allowed_purposes: [emergency]
        message: "Emergency break-glass: fully audited."

  # PII protection: reads require a declared purpose; writes are denied.
  - name: pii-protection
    priority: 110
    resources:
      - type: database
        match:
          sensitivity: [pii]
    rules:
      - action: read
        effect: allow
        conditions:
          allowed_purposes: [diagnostic, compliance, remediation]
        message: "PII read requires a declared purpose."
      - action: write
        effect: deny
        message: "Writes to PII databases are prohibited."

  # DBA role: reads always allowed; writes allowed unless purpose is diagnostic.
  - name: dba-policy
    priority: 80
    principals:
      - role: dba
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
      - action: write
        effect: allow
        conditions:
          blocked_purposes: [diagnostic]

  # Default: allow reads, deny writes for everyone else.
  - name: default-policy
    priority: 10
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
      - action: write
        effect: deny
        message: "writes not allowed"
`

// startAuditdOnFreePort builds a fresh auditd instance on a dynamically
// allocated TCP port with the given policy YAML loaded and returns the base URL.
// The process is killed by t.Cleanup when the test finishes.
func startAuditdOnFreePort(t *testing.T, policyYAML string) string {
	t.Helper()

	// Find a free port; release it before exec (inherent TOCTOU, acceptable in tests).
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	policyPath := filepath.Join(t.TempDir(), "policies.yaml")
	if err := os.WriteFile(policyPath, []byte(policyYAML), 0644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "audit.db")
	socketPath := fmt.Sprintf("/tmp/aidtest-%d.sock", time.Now().UnixNano()%1e9)

	cmd := exec.Command(auditdBin,
		"-listen", fmt.Sprintf(":%d", port),
		"-db", dbPath,
		"-socket", socketPath,
	)
	cmd.Env = append(os.Environ(),
		"HELPDESK_POLICY_ENABLED=true",
		"HELPDESK_POLICY_FILE="+policyPath,
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start auditd: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	baseURL := fmt.Sprintf("http://localhost:%d", port)
	if !waitForReady(baseURL+"/health", 10*time.Second) {
		t.Fatal("auditd did not become ready within 10s")
	}
	return baseURL
}

// getPolicyDecision retrieves the pol_* event by ID and returns the policy_decision sub-object.
func getPolicyDecision(t *testing.T, baseURL, eventID string) map[string]any {
	t.Helper()
	event := get(t, baseURL, "/v1/events/"+eventID)
	pd, _ := event["policy_decision"].(map[string]any)
	if pd == nil {
		t.Fatalf("event %s missing policy_decision field; full event: %v", eventID, event)
	}
	return pd
}

// =============================================================================
// Identity field propagation
// =============================================================================

func TestIdentity_UserPrincipal_FlowsThroughToAuditEvent(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	result := post(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "dev-db",
		"action":        "read",
		"trace_id":      "tr_ident1",
		"principal": map[string]any{
			"user_id":     "alice@example.com",
			"roles":       []string{"dba", "sre"},
			"auth_method": "jwt",
		},
		"purpose":      "diagnostic",
		"purpose_note": "investigating slow queries",
	})

	eventID, _ := result["event_id"].(string)
	if eventID == "" {
		t.Fatalf("no event_id in check response: %v", result)
	}

	pd := getPolicyDecision(t, base, eventID)

	if pd["user_id"] != "alice@example.com" {
		t.Errorf("user_id = %v, want alice@example.com", pd["user_id"])
	}
	roles, _ := pd["roles"].([]any)
	if len(roles) != 2 {
		t.Errorf("roles = %v, want [dba sre]", pd["roles"])
	}
	if pd["auth_method"] != "jwt" {
		t.Errorf("auth_method = %v, want jwt", pd["auth_method"])
	}
	if pd["purpose"] != "diagnostic" {
		t.Errorf("purpose = %v, want diagnostic", pd["purpose"])
	}
	if pd["purpose_note"] != "investigating slow queries" {
		t.Errorf("purpose_note = %v, want 'investigating slow queries'", pd["purpose_note"])
	}
}

func TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	result := post(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "dev-db",
		"action":        "read",
		"trace_id":      "tr_svc1",
		"principal": map[string]any{
			"service":     "srebot",
			"auth_method": "api_key",
		},
	})

	eventID, _ := result["event_id"].(string)
	if eventID == "" {
		t.Fatalf("no event_id in check response: %v", result)
	}

	pd := getPolicyDecision(t, base, eventID)

	if pd["service"] != "srebot" {
		t.Errorf("service = %v, want srebot", pd["service"])
	}
	if pd["auth_method"] != "api_key" {
		t.Errorf("auth_method = %v, want api_key", pd["auth_method"])
	}
	// Service principal must not record a user_id.
	if uid, _ := pd["user_id"].(string); uid != "" {
		t.Errorf("user_id = %q, want empty for service principal", uid)
	}
}

func TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	// No principal field at all — simulates a request that bypassed the identity provider.
	result := post(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "dev-db",
		"action":        "read",
		"trace_id":      "tr_anon1",
	})

	eventID, _ := result["event_id"].(string)
	if eventID == "" {
		t.Fatalf("no event_id: %v", result)
	}

	pd := getPolicyDecision(t, base, eventID)

	if uid, _ := pd["user_id"].(string); uid != "" {
		t.Errorf("user_id = %q, want empty for anonymous request", uid)
	}
	if svc, _ := pd["service"].(string); svc != "" {
		t.Errorf("service = %q, want empty for anonymous request", svc)
	}
}

// =============================================================================
// Role-based access control (via principal.roles)
// =============================================================================

func TestIdentity_DBACanWrite(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	code, body := postStatus(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "prod-db",
		"action":        "write",
		"trace_id":      "tr_dbawrite",
		"principal": map[string]any{
			"user_id":     "alice@example.com",
			"roles":       []string{"dba"},
			"auth_method": "jwt",
		},
	})
	if code != http.StatusOK {
		t.Fatalf("dba write: status = %d, want 200; body: %s", code, body)
	}
	// Verify allow in persisted event.
	var result map[string]any
	decodeJSON(t, body, &result)
	eventID, _ := result["event_id"].(string)
	pd := getPolicyDecision(t, base, eventID)
	if pd["effect"] != "allow" {
		t.Errorf("dba write: effect = %v, want allow", pd["effect"])
	}
}

func TestIdentity_NonDBADeniedWrite(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	code, body := postStatus(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "prod-db",
		"action":        "write",
		"trace_id":      "tr_devwrite",
		"principal": map[string]any{
			"user_id":     "bob@example.com",
			"roles":       []string{"developer"},
			"auth_method": "jwt",
		},
	})
	if code != http.StatusForbidden {
		t.Fatalf("developer write: status = %d, want 403; body: %s", code, body)
	}
	var result map[string]any
	decodeJSON(t, body, &result)
	if result["effect"] != "deny" {
		t.Errorf("developer write: effect = %v, want deny", result["effect"])
	}
}

func TestIdentity_OncallEmergencyBreakGlass(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	// oncall + emergency purpose → break-glass allow even for destructive.
	code, body := postStatus(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "prod-db",
		"action":        "destructive",
		"trace_id":      "tr_brkglass",
		"principal": map[string]any{
			"user_id":     "carol@example.com",
			"roles":       []string{"oncall", "sre"},
			"auth_method": "jwt",
		},
		"purpose":      "emergency",
		"purpose_note": "prod DB locked, tables inaccessible",
	})
	if code != http.StatusOK {
		t.Fatalf("oncall+emergency destructive: status = %d, want 200; body: %s", code, body)
	}
	var result map[string]any
	decodeJSON(t, body, &result)
	if result["effect"] != "allow" {
		t.Errorf("oncall+emergency: effect = %v, want allow", result["effect"])
	}
	if result["policy_name"] != "emergency-break-glass" {
		t.Errorf("policy_name = %v, want emergency-break-glass", result["policy_name"])
	}
	// Verify audit event records all identity + purpose fields.
	eventID, _ := result["event_id"].(string)
	pd := getPolicyDecision(t, base, eventID)
	if pd["user_id"] != "carol@example.com" {
		t.Errorf("user_id = %v, want carol@example.com", pd["user_id"])
	}
	if pd["purpose"] != "emergency" {
		t.Errorf("purpose = %v, want emergency", pd["purpose"])
	}
}

func TestIdentity_NonOncallEmergencyDenied(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	// developer + emergency purpose — not in oncall role → break-glass doesn't apply.
	code, _ := postStatus(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "prod-db",
		"action":        "write",
		"trace_id":      "tr_notoncall",
		"principal": map[string]any{
			"user_id":     "dave@example.com",
			"roles":       []string{"developer"},
			"auth_method": "jwt",
		},
		"purpose": "emergency",
	})
	if code != http.StatusForbidden {
		t.Fatalf("non-oncall with emergency purpose: status = %d, want 403", code)
	}
}

// =============================================================================
// Purpose-based access control
// =============================================================================

func TestIdentity_DiagnosticPurposeBlocksWrite(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	// diagnostic purpose → dba-policy blocked_purposes condition denies write even for DBA.
	code, body := postStatus(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "prod-db",
		"action":        "write",
		"trace_id":      "tr_diagwrite",
		"principal": map[string]any{
			"user_id":     "alice@example.com",
			"roles":       []string{"dba"},
			"auth_method": "jwt",
		},
		"purpose": "diagnostic",
	})
	// Even a DBA is blocked from writing with diagnostic purpose.
	if code != http.StatusForbidden {
		t.Fatalf("dba+diagnostic write: status = %d, want 403; body: %s", code, body)
	}
	var result map[string]any
	decodeJSON(t, body, &result)
	if result["effect"] != "deny" {
		t.Errorf("dba+diagnostic write: effect = %v, want deny", result["effect"])
	}
	if result["policy_name"] != "dba-policy" {
		t.Errorf("policy_name = %v, want dba-policy", result["policy_name"])
	}
}

func TestIdentity_RemediationPurposeAllowsDBAWrite(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	code, body := postStatus(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "prod-db",
		"action":        "write",
		"trace_id":      "tr_remedwrite",
		"principal": map[string]any{
			"user_id":     "alice@example.com",
			"roles":       []string{"dba"},
			"auth_method": "jwt",
		},
		"purpose":      "remediation",
		"purpose_note": "INC-1234 corrupt row cleanup",
	})
	if code != http.StatusOK {
		t.Fatalf("dba+remediation write: status = %d, want 200; body: %s", code, body)
	}
	var result map[string]any
	decodeJSON(t, body, &result)
	if result["effect"] != "allow" {
		t.Errorf("dba+remediation write: effect = %v, want allow", result["effect"])
	}
	// Purpose and note must survive into the audit event.
	eventID, _ := result["event_id"].(string)
	pd := getPolicyDecision(t, base, eventID)
	if pd["purpose"] != "remediation" {
		t.Errorf("purpose = %v, want remediation", pd["purpose"])
	}
	if pd["purpose_note"] != "INC-1234 corrupt row cleanup" {
		t.Errorf("purpose_note = %v", pd["purpose_note"])
	}
}

// =============================================================================
// Sensitivity-based access control
// =============================================================================

func TestIdentity_PIIReadWithPurpose_Allowed(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	code, body := postStatus(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "customers",
		"action":        "read",
		"trace_id":      "tr_piipurp",
		"sensitivity":   []string{"pii"},
		"purpose":       "diagnostic",
		"principal": map[string]any{
			"user_id":     "bob@example.com",
			"roles":       []string{"sre"},
			"auth_method": "jwt",
		},
	})
	if code != http.StatusOK {
		t.Fatalf("pii+diagnostic read: status = %d, want 200; body: %s", code, body)
	}
	var result map[string]any
	decodeJSON(t, body, &result)
	if result["effect"] != "allow" {
		t.Errorf("pii+diagnostic read: effect = %v, want allow", result["effect"])
	}
	// Sensitivity must appear in the persisted audit event.
	eventID, _ := result["event_id"].(string)
	pd := getPolicyDecision(t, base, eventID)
	sens, _ := pd["sensitivity"].([]any)
	if len(sens) != 1 || sens[0] != "pii" {
		t.Errorf("sensitivity = %v, want [pii]", pd["sensitivity"])
	}
}

func TestIdentity_PIIReadWithoutPurpose_Denied(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	// PII resource, no purpose declared → pii-protection policy denies.
	code, body := postStatus(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "customers",
		"action":        "read",
		"trace_id":      "tr_piinopurp",
		"sensitivity":   []string{"pii"},
		"principal": map[string]any{
			"user_id":     "bob@example.com",
			"roles":       []string{"sre"},
			"auth_method": "jwt",
		},
		// No purpose field.
	})
	if code != http.StatusForbidden {
		t.Fatalf("pii without purpose: status = %d, want 403; body: %s", code, body)
	}
	var result map[string]any
	decodeJSON(t, body, &result)
	if result["effect"] != "deny" {
		t.Errorf("pii without purpose: effect = %v, want deny", result["effect"])
	}
}

func TestIdentity_PIIWrite_AlwaysDenied(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	// PII + write → pii-protection unconditionally denies writes (even for DBA + purpose).
	code, body := postStatus(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "customers",
		"action":        "write",
		"trace_id":      "tr_piiwrite",
		"sensitivity":   []string{"pii"},
		"purpose":       "remediation",
		"principal": map[string]any{
			"user_id":     "alice@example.com",
			"roles":       []string{"dba"},
			"auth_method": "jwt",
		},
	})
	if code != http.StatusForbidden {
		t.Fatalf("pii write: status = %d, want 403; body: %s", code, body)
	}
	var result map[string]any
	decodeJSON(t, body, &result)
	if result["effect"] != "deny" {
		t.Errorf("pii write: effect = %v, want deny", result["effect"])
	}
	if result["policy_name"] != "pii-protection" {
		t.Errorf("policy_name = %v, want pii-protection", result["policy_name"])
	}
}

// =============================================================================
// /v1/governance/explain with purpose and sensitivity query params
// =============================================================================

func TestIdentity_Explain_WithPurposeAndSensitivity_Allow(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	// PII read + diagnostic purpose → explain should show allow.
	result := get(t, base,
		"/v1/governance/explain?resource_type=database&resource_name=customers&action=read&sensitivity=pii&purpose=diagnostic")

	decision, _ := result["decision"].(map[string]any)
	if decision == nil {
		t.Fatalf("explain: missing decision field; got %v", result)
	}
	if decision["effect"] != "allow" {
		t.Errorf("explain pii+diagnostic read: effect = %v, want allow", decision["effect"])
	}
	if decision["policy_name"] != "pii-protection" {
		t.Errorf("explain: policy_name = %v, want pii-protection", decision["policy_name"])
	}
}

func TestIdentity_Explain_WithPurposeAndSensitivity_Deny(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	// PII read without purpose → explain should show deny.
	result := get(t, base,
		"/v1/governance/explain?resource_type=database&resource_name=customers&action=read&sensitivity=pii")

	decision, _ := result["decision"].(map[string]any)
	if decision == nil {
		t.Fatalf("explain: missing decision field; got %v", result)
	}
	if decision["effect"] != "deny" {
		t.Errorf("explain pii without purpose: effect = %v, want deny", decision["effect"])
	}
}

func TestIdentity_Explain_DiagnosticPurpose_DeniesWrite(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	// No principal (no DBA role) + diagnostic purpose + write → default-policy denies.
	// The dba-policy doesn't match (no DBA principal), so default-policy is the catch-all deny.
	result := get(t, base,
		"/v1/governance/explain?resource_type=database&resource_name=prod-db&action=write&purpose=diagnostic")

	decision, _ := result["decision"].(map[string]any)
	if decision == nil {
		t.Fatalf("explain: missing decision field; got %v", result)
	}
	if decision["effect"] != "deny" {
		t.Errorf("explain diagnostic+write: effect = %v, want deny", decision["effect"])
	}
	if decision["policy_name"] != "default-policy" {
		t.Errorf("explain: policy_name = %v, want default-policy", decision["policy_name"])
	}
}

// =============================================================================
// Audit event round-trip: all identity fields survive SQLite store
// =============================================================================

func TestIdentity_FullPolicyDecisionRoundTrip(t *testing.T) {
	base := startAuditdOnFreePort(t, identityPolicyYAML)

	// Post a check with every identity field populated.
	result := post(t, base, "/v1/governance/check", map[string]any{
		"resource_type": "database",
		"resource_name": "prod-db",
		"action":        "read",
		"trace_id":      "tr_fullroundtrip",
		"sensitivity":   []string{"pii", "critical"},
		"purpose":       "compliance",
		"purpose_note":  "SOC2 annual audit Q1",
		"principal": map[string]any{
			"user_id":     "auditor@example.com",
			"roles":       []string{"compliance"},
			"service":     "",
			"auth_method": "jwt",
		},
	})

	eventID, _ := result["event_id"].(string)
	if eventID == "" {
		t.Fatalf("no event_id: %v", result)
	}

	event := get(t, base, "/v1/events/"+eventID)
	pd, _ := event["policy_decision"].(map[string]any)
	if pd == nil {
		t.Fatalf("policy_decision field missing from %v", event)
	}

	checks := map[string]any{
		"resource_type": "database",
		"resource_name": "prod-db",
		"action":        "read",
		"user_id":       "auditor@example.com",
		"auth_method":   "jwt",
		"purpose":       "compliance",
		"purpose_note":  "SOC2 annual audit Q1",
	}
	for field, want := range checks {
		if pd[field] != want {
			t.Errorf("%s = %v, want %v", field, pd[field], want)
		}
	}

	sens, _ := pd["sensitivity"].([]any)
	if len(sens) != 2 {
		t.Errorf("sensitivity = %v, want [pii critical]", pd["sensitivity"])
	}

	roles, _ := pd["roles"].([]any)
	if len(roles) != 1 || roles[0] != "compliance" {
		t.Errorf("roles = %v, want [compliance]", pd["roles"])
	}
}

// =============================================================================
// Helpers
// =============================================================================

// decodeJSON is a test helper to decode a JSON string into a map.
func decodeJSON(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("decode JSON: %v\nbody: %s", err, s)
	}
}
