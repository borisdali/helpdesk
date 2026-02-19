//go:build integration

// Package governance contains integration tests for the AI Governance subsystem.
// They start a real auditd process (built on the fly) and exercise its full
// HTTP API end-to-end: audit events, hash-chain integrity, approval workflow,
// and governance info/policy endpoints.
//
// Run with:
//
//	go test -tags integration -timeout 120s ./testing/integration/governance/...
//
// No external Docker or database is required; auditd stores events in SQLite.
package governance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	auditdAddr  = "http://localhost:19901"
	auditdAddr2 = "http://localhost:19902" // for policy-enabled instance
)

// auditdBin is the path to the compiled auditd binary, set in TestMain.
var auditdBin string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "auditd-integration-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "SKIP: failed to create temp dir:", err)
		os.Exit(0)
	}
	defer os.RemoveAll(tmpDir)

	// Build auditd once; all tests share this binary.
	auditdBin = filepath.Join(tmpDir, "auditd")
	buildCmd := exec.Command("go", "build", "-o", auditdBin, "helpdesk/cmd/auditd")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "SKIP: failed to build auditd: %v\n%s\n", err, out)
		os.Exit(0)
	}

	// Start the primary auditd instance (no policy loaded).
	dbPath := filepath.Join(tmpDir, "audit.db")
	socketPath := filepath.Join(tmpDir, "audit.sock")
	proc := exec.Command(auditdBin,
		"-listen", ":19901",
		"-db", dbPath,
		"-socket", socketPath,
	)
	proc.Stderr = os.Stderr
	if err := proc.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "SKIP: failed to start auditd:", err)
		os.Exit(0)
	}

	if !waitForReady(auditdAddr+"/health", 10*time.Second) {
		proc.Process.Kill()
		fmt.Fprintln(os.Stderr, "SKIP: auditd did not become ready within 10s")
		os.Exit(0)
	}

	code := m.Run()
	proc.Process.Kill()
	proc.Wait()
	os.Exit(code)
}

// startAuditdWithPolicy starts a second auditd on auditdAddr2 with the given
// policy file. It registers a cleanup that kills the process when t ends.
func startAuditdWithPolicy(t *testing.T, policyPath string) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "audit.db")
	// Unix socket paths are limited to ~104 chars on macOS; use /tmp directly.
	socketPath := fmt.Sprintf("/tmp/atest-%d.sock", time.Now().UnixNano()%1e9)

	cmd := exec.Command(auditdBin,
		"-listen", ":19902",
		"-db", dbPath,
		"-socket", socketPath,
	)
	cmd.Env = append(os.Environ(),
		"HELPDESK_POLICY_ENABLED=true",
		"HELPDESK_POLICY_FILE="+policyPath,
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start policy auditd: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	if !waitForReady(auditdAddr2+"/health", 10*time.Second) {
		t.Fatal("policy auditd did not become ready within 10s")
	}
}

// waitForReady polls url until it returns 200 or timeout elapses.
func waitForReady(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// post sends a JSON POST to base+path and decodes the JSON response.
// It fails the test if the response status is >= 400.
func post(t *testing.T, base, path string, body any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	resp, err := http.Post(base+path, "application/json", bytes.NewReader(b))
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

// postStatus is like post but returns the status code and body without failing.
func postStatus(t *testing.T, base, path string, body any) (int, string) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// get fetches base+path and decodes the JSON response.
func get(t *testing.T, base, path string) map[string]any {
	t.Helper()
	resp, err := http.Get(base + path)
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

// getList fetches base+path and decodes a JSON array response.
func getList(t *testing.T, base, path string) []map[string]any {
	t.Helper()
	resp, err := http.Get(base + path)
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

// newEvent returns a minimal valid audit event body with a unique ID.
func newEvent(sessionID, eventType string) map[string]any {
	return map[string]any{
		"event_id":   fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"event_type": eventType,
		"session":    map[string]any{"id": sessionID},
		"input":      map[string]any{"user_query": "integration test"},
	}
}

// --- Health ---

func TestHealth(t *testing.T) {
	result := get(t, auditdAddr, "/health")
	if result["status"] != "ok" {
		t.Errorf("status = %q, want ok", result["status"])
	}
}

// =============================================================================
// Audit subsystem
// =============================================================================

func TestAudit_RecordEvent_ReturnsHashes(t *testing.T) {
	sessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	result := post(t, auditdAddr, "/v1/events", newEvent(sessionID, "delegation_decision"))

	if result["event_id"] == "" {
		t.Error("expected non-empty event_id in response")
	}
	if result["event_hash"] == "" {
		t.Error("expected non-empty event_hash in response")
	}
}

func TestAudit_RecordedEventIsQueryable(t *testing.T) {
	sessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	result := post(t, auditdAddr, "/v1/events", newEvent(sessionID, "delegation_decision"))
	eventID := result["event_id"].(string)

	events := getList(t, auditdAddr, "/v1/events?session_id="+sessionID)
	if len(events) == 0 {
		t.Fatal("expected at least one event for the session")
	}
	found := false
	for _, e := range events {
		if e["event_id"] == eventID {
			found = true
		}
	}
	if !found {
		t.Errorf("event %s not found in query results", eventID)
	}
}

func TestAudit_FilterByEventType(t *testing.T) {
	sessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	post(t, auditdAddr, "/v1/events", newEvent(sessionID, "delegation_decision"))
	post(t, auditdAddr, "/v1/events", newEvent(sessionID, "tool_execution"))

	events := getList(t, auditdAddr,
		"/v1/events?session_id="+sessionID+"&event_type=delegation_decision")

	if len(events) == 0 {
		t.Fatal("expected at least one delegation_decision event")
	}
	for _, e := range events {
		if e["event_type"] != "delegation_decision" {
			t.Errorf("event_type = %q, want delegation_decision only", e["event_type"])
		}
	}
}

func TestAudit_RecordOutcome(t *testing.T) {
	sessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	ev := newEvent(sessionID, "delegation_decision")
	result := post(t, auditdAddr, "/v1/events", ev)
	eventID := result["event_id"].(string)

	b, _ := json.Marshal(map[string]any{"status": "success", "duration_ms": 1500})
	resp, err := http.Post(
		auditdAddr+"/v1/events/"+eventID+"/outcome",
		"application/json",
		bytes.NewReader(b),
	)
	if err != nil {
		t.Fatalf("POST outcome: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("outcome status = %d, want 200", resp.StatusCode)
	}
}

func TestAudit_HashChainIsValid(t *testing.T) {
	traceID := fmt.Sprintf("trace-%d", time.Now().UnixNano())

	// Record several sequential events and verify each carries a hash.
	for i := 0; i < 4; i++ {
		ev := newEvent("chain-session-"+traceID, "tool_execution")
		ev["trace_id"] = traceID
		result := post(t, auditdAddr, "/v1/events", ev)
		if hash, _ := result["event_hash"].(string); hash == "" {
			t.Fatalf("event %d: missing event_hash in response", i)
		}
	}

	// The /v1/verify endpoint checks the full chain across all events.
	result := get(t, auditdAddr, "/v1/verify")
	if valid, _ := result["valid"].(bool); !valid {
		t.Errorf("chain integrity check failed: %v", result)
	}
}

func TestAudit_VerifyCountIncrements(t *testing.T) {
	before := get(t, auditdAddr, "/v1/verify")
	beforeTotal, _ := before["total_events"].(float64)

	sessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	post(t, auditdAddr, "/v1/events", newEvent(sessionID, "delegation_decision"))

	after := get(t, auditdAddr, "/v1/verify")
	afterTotal, _ := after["total_events"].(float64)

	if afterTotal <= beforeTotal {
		t.Errorf("total_events did not increase: before=%.0f after=%.0f", beforeTotal, afterTotal)
	}
	if valid, _ := after["valid"].(bool); !valid {
		t.Error("chain should remain valid after inserting events")
	}
}

// =============================================================================
// Approval subsystem
// =============================================================================

func newApproval(actionClass, tool, agent, requestedBy string) map[string]any {
	return map[string]any{
		"action_class": actionClass,
		"tool_name":    tool,
		"agent_name":   agent,
		"requested_by": requestedBy,
	}
}

func TestApprovals_CreateAndGet(t *testing.T) {
	result := post(t, auditdAddr, "/v1/approvals",
		newApproval("write", "execute_sql", "database-agent", "alice"))

	approvalID, _ := result["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("expected approval_id in response")
	}
	if result["status"] != "pending" {
		t.Errorf("status = %q, want pending", result["status"])
	}

	got := get(t, auditdAddr, "/v1/approvals/"+approvalID)
	if got["approval_id"] != approvalID {
		t.Errorf("approval_id = %q, want %q", got["approval_id"], approvalID)
	}
	if got["status"] != "pending" {
		t.Errorf("fetched status = %q, want pending", got["status"])
	}
}

func TestApprovals_ListPending(t *testing.T) {
	result := post(t, auditdAddr, "/v1/approvals",
		newApproval("destructive", "drop_table", "database-agent", "bob"))
	approvalID, _ := result["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("expected approval_id")
	}

	pending := getList(t, auditdAddr, "/v1/approvals/pending")
	found := false
	for _, a := range pending {
		if a["approval_id"] == approvalID {
			found = true
		}
	}
	if !found {
		t.Errorf("approval %s not found in /v1/approvals/pending", approvalID)
	}
}

func TestApprovals_Approve(t *testing.T) {
	result := post(t, auditdAddr, "/v1/approvals",
		newApproval("write", "update_config", "k8s-agent", "carol"))
	approvalID, _ := result["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("expected approval_id")
	}

	updated := post(t, auditdAddr, "/v1/approvals/"+approvalID+"/approve",
		map[string]any{"approved_by": "manager", "reason": "looks good"})

	if updated["status"] != "approved" {
		t.Errorf("status after approve = %q, want approved", updated["status"])
	}
	if updated["resolved_by"] != "manager" {
		t.Errorf("resolved_by = %q, want manager", updated["resolved_by"])
	}

	// Should no longer appear in the pending list.
	pending := getList(t, auditdAddr, "/v1/approvals/pending")
	for _, a := range pending {
		if a["approval_id"] == approvalID {
			t.Error("approved request should not be in pending list")
		}
	}
}

func TestApprovals_Deny(t *testing.T) {
	result := post(t, auditdAddr, "/v1/approvals",
		newApproval("destructive", "delete_namespace", "k8s-agent", "dave"))
	approvalID, _ := result["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("expected approval_id")
	}

	updated := post(t, auditdAddr, "/v1/approvals/"+approvalID+"/deny",
		map[string]any{"denied_by": "admin", "reason": "too risky"})

	if updated["status"] != "denied" {
		t.Errorf("status after deny = %q, want denied", updated["status"])
	}
	if updated["resolved_by"] != "admin" {
		t.Errorf("resolved_by = %q, want admin", updated["resolved_by"])
	}
}

func TestApprovals_Cancel(t *testing.T) {
	result := post(t, auditdAddr, "/v1/approvals",
		newApproval("write", "scale_deployment", "k8s-agent", "eve"))
	approvalID, _ := result["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("expected approval_id")
	}

	b, _ := json.Marshal(map[string]any{"cancelled_by": "eve", "reason": "changed mind"})
	resp, err := http.Post(
		auditdAddr+"/v1/approvals/"+approvalID+"/cancel",
		"application/json",
		bytes.NewReader(b),
	)
	if err != nil {
		t.Fatalf("POST cancel: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("cancel status = %d, want 200", resp.StatusCode)
	}

	got := get(t, auditdAddr, "/v1/approvals/"+approvalID)
	if got["status"] != "cancelled" {
		t.Errorf("status after cancel = %q, want cancelled", got["status"])
	}
}

func TestApprovals_FilterByStatus(t *testing.T) {
	// Create and approve a request so there is at least one "approved" record.
	result := post(t, auditdAddr, "/v1/approvals",
		newApproval("write", "patch_service", "k8s-agent", "frank"))
	approvalID, _ := result["approval_id"].(string)
	post(t, auditdAddr, "/v1/approvals/"+approvalID+"/approve",
		map[string]any{"approved_by": "lead"})

	approvals := getList(t, auditdAddr, "/v1/approvals?status=approved")
	found := false
	for _, a := range approvals {
		if s, _ := a["status"].(string); s != "approved" {
			t.Errorf("expected status=approved, got %q", s)
		}
		if a["approval_id"] == approvalID {
			found = true
		}
	}
	if !found {
		t.Errorf("approved request %s not found in filtered list", approvalID)
	}
}

func TestApprovals_MissingActionClass(t *testing.T) {
	code, body := postStatus(t, auditdAddr, "/v1/approvals",
		map[string]any{"requested_by": "alice"})
	if code != http.StatusBadRequest {
		t.Errorf("missing action_class: status = %d, want 400", code)
	}
	if !strings.Contains(body, "action_class") {
		t.Errorf("error body should mention action_class, got: %s", body)
	}
}

func TestApprovals_MissingRequestedBy(t *testing.T) {
	code, body := postStatus(t, auditdAddr, "/v1/approvals",
		map[string]any{"action_class": "write"})
	if code != http.StatusBadRequest {
		t.Errorf("missing requested_by: status = %d, want 400", code)
	}
	if !strings.Contains(body, "requested_by") {
		t.Errorf("error body should mention requested_by, got: %s", body)
	}
}

func TestApprovals_GetNonExistent(t *testing.T) {
	resp, err := http.Get(auditdAddr + "/v1/approvals/apr_nonexistent")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// =============================================================================
// Governance endpoints
// =============================================================================

func TestGovernance_HealthAndInfo(t *testing.T) {
	result := get(t, auditdAddr, "/v1/governance/info")

	// Audit subsystem: always enabled while auditd is running.
	audit, _ := result["audit"].(map[string]any)
	if audit == nil {
		t.Fatal("expected audit field in governance info")
	}
	if enabled, _ := audit["enabled"].(bool); !enabled {
		t.Error("audit.enabled should be true")
	}

	// Policy: not configured for the primary instance.
	policy, _ := result["policy"].(map[string]any)
	if policy == nil {
		t.Fatal("expected policy field in governance info")
	}
	if enabled, _ := policy["enabled"].(bool); enabled {
		t.Error("policy.enabled should be false when started without HELPDESK_POLICY_FILE")
	}

	// Timestamp is present.
	if ts, _ := result["timestamp"].(string); ts == "" {
		t.Error("expected non-empty timestamp")
	}
}

func TestGovernance_AuditCountReflectsRecordedEvents(t *testing.T) {
	before := get(t, auditdAddr, "/v1/governance/info")
	beforeTotal, _ := before["audit"].(map[string]any)["events_total"].(float64)

	sessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	post(t, auditdAddr, "/v1/events", newEvent(sessionID, "delegation_decision"))

	after := get(t, auditdAddr, "/v1/governance/info")
	afterTotal, _ := after["audit"].(map[string]any)["events_total"].(float64)

	if afterTotal <= beforeTotal {
		t.Errorf("events_total did not increase: before=%.0f after=%.0f", beforeTotal, afterTotal)
	}
}

func TestGovernance_PoliciesWithoutEngine(t *testing.T) {
	result := get(t, auditdAddr, "/v1/governance/policies")
	if enabled, _ := result["enabled"].(bool); enabled {
		t.Error("enabled should be false when no policy file configured")
	}
	if msg, _ := result["message"].(string); msg == "" {
		t.Error("expected a message explaining how to enable policy enforcement")
	}
}

const minimalPolicyYAML = `
version: "1"
policies:
  - name: db-policy
    description: Integration test policy
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
      - action: write
        effect: deny
        message: "writes require approval"
`

func TestGovernance_InfoWithPolicyEnabled(t *testing.T) {
	// Write a policy file and start a fresh auditd that loads it.
	policyPath := filepath.Join(t.TempDir(), "policies.yaml")
	if err := os.WriteFile(policyPath, []byte(minimalPolicyYAML), 0644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}

	startAuditdWithPolicy(t, policyPath)

	result := get(t, auditdAddr2, "/v1/governance/info")
	policy, _ := result["policy"].(map[string]any)
	if policy == nil {
		t.Fatal("expected policy field")
	}
	if enabled, _ := policy["enabled"].(bool); !enabled {
		t.Error("policy.enabled should be true when HELPDESK_POLICY_ENABLED=true and file is valid")
	}
	if count, _ := policy["policies_count"].(float64); count != 1 {
		t.Errorf("policies_count = %.0f, want 1", count)
	}
	if count, _ := policy["rules_count"].(float64); count != 2 {
		t.Errorf("rules_count = %.0f, want 2", count)
	}
}

func TestGovernance_PoliciesSummaryWithEngine(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "policies.yaml")
	if err := os.WriteFile(policyPath, []byte(minimalPolicyYAML), 0644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}

	startAuditdWithPolicy(t, policyPath)

	result := get(t, auditdAddr2, "/v1/governance/policies")
	if enabled, _ := result["enabled"].(bool); !enabled {
		t.Error("enabled should be true with policy engine loaded")
	}

	policies, _ := result["policies"].([]any)
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
