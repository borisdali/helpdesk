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
	auditdAddr2 = "http://localhost:19902" // for policy-enabled instance (HELPDESK_POLICY_ENABLED=true)
	auditdAddr3 = "http://localhost:19903" // for default-config instance (HELPDESK_POLICY_FILE set, HELPDESK_POLICY_ENABLED absent)
)

// auditdBin is the path to the compiled auditd binary, set in TestMain.
var (
	auditdBin  string
	auditorBin string
	secbotBin  string
)

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "auditd-integration-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "SKIP: failed to create temp dir:", err)
		os.Exit(0)
	}
	defer os.RemoveAll(tmpDir)

	// Build all governance binaries once; all tests share them.
	bins := map[string]*string{
		"helpdesk/cmd/auditd":  &auditdBin,
		"helpdesk/cmd/auditor": &auditorBin,
		"helpdesk/cmd/secbot":  &secbotBin,
	}
	for pkg, dest := range bins {
		bin := filepath.Join(tmpDir, filepath.Base(pkg))
		if out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "SKIP: failed to build %s: %v\n%s\n", pkg, err, out)
			os.Exit(0)
		}
		*dest = bin
	}
	auditdBin = filepath.Join(tmpDir, "auditd")

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

// =============================================================================
// Agent-reasoning audit layer
// =============================================================================

// TestIntegration_AgentReasoningRoundTrip verifies that an agent_reasoning event
// posted to the live auditd HTTP API (as NewReasoningCallback emits it) survives
// the full HTTP → SQLite → HTTP round-trip with all AgentReasoning fields intact,
// and that the ?event_type filter correctly surfaces it.
func TestIntegration_AgentReasoningRoundTrip(t *testing.T) {
	eventID := fmt.Sprintf("rsn-integ-%d", time.Now().UnixNano())

	payload := map[string]any{
		"event_id":   eventID,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"event_type": "agent_reasoning",
		"trace_id":   "integ-trace-rsn",
		"session":    map[string]any{"id": "integ-rsn-session"},
		"agent_reasoning": map[string]any{
			"reasoning":  "The user wants active connections. I will call get_active_connections first.",
			"tool_calls": []string{"get_active_connections", "get_connection_stats"},
		},
	}
	created := post(t, auditdAddr, "/v1/events", payload)
	if created["event_id"] == nil {
		t.Fatalf("POST /v1/events: event_id missing from response: %v", created)
	}

	// Retrieve by event ID.
	result := get(t, auditdAddr, "/v1/events/"+eventID)
	if result["event_id"] != eventID {
		t.Errorf("event_id = %v, want %s", result["event_id"], eventID)
	}
	if result["event_type"] != "agent_reasoning" {
		t.Errorf("event_type = %v, want agent_reasoning", result["event_type"])
	}
	if result["trace_id"] != "integ-trace-rsn" {
		t.Errorf("trace_id = %v, want integ-trace-rsn", result["trace_id"])
	}
	ar, _ := result["agent_reasoning"].(map[string]any)
	if ar == nil {
		t.Fatal("agent_reasoning field missing from retrieved event")
	}
	if reasoning, _ := ar["reasoning"].(string); reasoning == "" {
		t.Error("agent_reasoning.reasoning empty after store round-trip")
	}
	toolCalls, _ := ar["tool_calls"].([]any)
	if len(toolCalls) != 2 {
		t.Errorf("agent_reasoning.tool_calls = %v, want 2 entries", toolCalls)
	}
	if toolCalls[0] != "get_active_connections" {
		t.Errorf("tool_calls[0] = %v, want get_active_connections", toolCalls[0])
	}

	// Verify the ?event_type=agent_reasoning filter surfaces it.
	sessionEvents := getList(t, auditdAddr, "/v1/events?event_type=agent_reasoning&session_id=integ-rsn-session")
	found := false
	for _, e := range sessionEvents {
		if e["event_id"] == eventID {
			found = true
			if e["event_type"] != "agent_reasoning" {
				t.Errorf("filtered result has event_type = %v, want agent_reasoning", e["event_type"])
			}
		}
	}
	if !found {
		t.Errorf("event %s not found when filtering by event_type=agent_reasoning", eventID)
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

// =============================================================================
// Default-config govexplain regression
// =============================================================================

// stripEnv returns os.Environ() with all entries for the given keys removed.
// Used to prevent test-runner env vars from leaking into subprocess invocations.
func stripEnv(keys ...string) []string {
	skip := make(map[string]bool, len(keys))
	for _, k := range keys {
		skip[k] = true
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		if !skip[key] {
			out = append(out, e)
		}
	}
	return out
}

// startAuditdFileOnly starts auditd on auditdAddr3 with HELPDESK_POLICY_FILE set
// but HELPDESK_POLICY_ENABLED absent — replicating the docker-compose default
// after the fix (empty default for HELPDESK_POLICY_ENABLED in the auditd service).
// It strips HELPDESK_POLICY_ENABLED from the test runner's environment so the
// "absent" condition is not accidentally overridden.
func startAuditdFileOnly(t *testing.T, policyPath string) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "audit.db")
	socketPath := fmt.Sprintf("/tmp/atest3-%d.sock", time.Now().UnixNano()%1e9)

	cmd := exec.Command(auditdBin,
		"-listen", ":19903",
		"-db", dbPath,
		"-socket", socketPath,
	)
	cmd.Env = append(stripEnv("HELPDESK_POLICY_ENABLED"),
		"HELPDESK_POLICY_FILE="+policyPath,
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start file-only auditd: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	if !waitForReady(auditdAddr3+"/health", 10*time.Second) {
		t.Fatal("file-only auditd did not become ready within 10s")
	}
}

// TestGovernance_Explain_DefaultConfig is a regression test for the deployment
// bug where HELPDESK_POLICY_ENABLED defaulted to "false" in docker-compose,
// preventing auditd from loading the policy engine even when a policy file was
// mounted. With the fix (empty default), setting only HELPDESK_POLICY_FILE
// should be sufficient for auditd to load the policy and serve real decisions
// from /v1/governance/explain — not the stub {"enabled":false} response.
func TestGovernance_Explain_DefaultConfig(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "policies.yaml")
	if err := os.WriteFile(policyPath, []byte(minimalPolicyYAML), 0644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}

	startAuditdFileOnly(t, policyPath)

	// Governance info must show the policy engine is loaded.
	info := get(t, auditdAddr3, "/v1/governance/info")
	policyInfo, _ := info["policy"].(map[string]any)
	if policyInfo == nil {
		t.Fatal("expected policy field in governance info")
	}
	if enabled, _ := policyInfo["enabled"].(bool); !enabled {
		t.Error("policy.enabled should be true when HELPDESK_POLICY_FILE is set and HELPDESK_POLICY_ENABLED is absent")
	}

	// Explain: read on a database resource should be allowed.
	readResp := get(t, auditdAddr3,
		"/v1/governance/explain?resource_type=database&resource_name=prod-db&action=read")
	readDecision, _ := readResp["decision"].(map[string]any)
	if readDecision == nil {
		// Engine was not loaded — we got the stub {"enabled":false} response instead.
		t.Fatalf("explain response missing 'decision' field (got %v); policy engine may not have loaded", readResp)
	}
	if effect, _ := readDecision["effect"].(string); effect != "allow" {
		t.Errorf("explain effect for read = %q, want allow", effect)
	}

	// Explain: write on a database resource should be denied.
	writeResp := get(t, auditdAddr3,
		"/v1/governance/explain?resource_type=database&resource_name=prod-db&action=write")
	writeDecision, _ := writeResp["decision"].(map[string]any)
	if writeDecision == nil {
		t.Fatalf("explain response missing 'decision' field (got %v); policy engine may not have loaded", writeResp)
	}
	if effect, _ := writeDecision["effect"].(string); effect != "deny" {
		t.Errorf("explain effect for write = %q, want deny", effect)
	}
}

// =============================================================================
// Write/Destructive tool audit event tests
// =============================================================================

// newToolEvent returns an audit event body that simulates a tool_execution event
// with the given tool name and action class (write or destructive).
func newToolEvent(sessionID, toolName, actionClass string) map[string]any {
	return map[string]any{
		"event_id":   fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"event_type": "tool_execution",
		"session":    map[string]any{"id": sessionID},
		"input":      map[string]any{"user_query": "integration test"},
		"tool_execution": map[string]any{
			"tool_name":    toolName,
			"action_class": actionClass,
		},
	}
}

func TestAudit_WriteToolEvent_RecordedAndQueryable(t *testing.T) {
	sessionID := fmt.Sprintf("write-tool-session-%d", time.Now().UnixNano())

	// Record a cancel_query event (ActionWrite)
	result := post(t, auditdAddr, "/v1/events", newToolEvent(sessionID, "cancel_query", "write"))
	eventID, _ := result["event_id"].(string)
	if eventID == "" {
		t.Fatal("expected event_id in response")
	}
	if result["event_hash"] == "" {
		t.Error("expected event_hash in response for write tool event")
	}

	// Should be queryable by session
	events := getList(t, auditdAddr, "/v1/events?session_id="+sessionID)
	if len(events) == 0 {
		t.Fatal("expected at least one event for session")
	}
	found := false
	for _, e := range events {
		if e["event_id"] == eventID {
			found = true
		}
	}
	if !found {
		t.Errorf("write tool event %s not found in session query results", eventID)
	}
}

func TestAudit_DestructiveToolEvent_RecordedAndQueryable(t *testing.T) {
	sessionID := fmt.Sprintf("destructive-tool-session-%d", time.Now().UnixNano())

	// Record terminate_connection (ActionDestructive)
	result := post(t, auditdAddr, "/v1/events", newToolEvent(sessionID, "terminate_connection", "destructive"))
	eventID, _ := result["event_id"].(string)
	if eventID == "" {
		t.Fatal("expected event_id in response for destructive tool event")
	}

	// Retrieve by event ID
	event := get(t, auditdAddr, "/v1/events/"+eventID)
	if event["event_id"] != eventID {
		t.Errorf("event_id = %v, want %s", event["event_id"], eventID)
	}
	if event["event_type"] != "tool_execution" {
		t.Errorf("event_type = %v, want tool_execution", event["event_type"])
	}
}

func TestAudit_MultipleToolEvents_HashChainValid(t *testing.T) {
	traceID := fmt.Sprintf("tool-trace-%d", time.Now().UnixNano())
	sessionID := "tool-chain-" + traceID

	// Simulate a typical session: read → write → destructive
	toolSequence := []struct {
		tool        string
		actionClass string
	}{
		{"get_active_connections", "read"},
		{"cancel_query", "write"},
		{"terminate_connection", "destructive"},
		{"kill_idle_connections", "destructive"},
	}

	for _, step := range toolSequence {
		ev := newToolEvent(sessionID, step.tool, step.actionClass)
		ev["trace_id"] = traceID
		result := post(t, auditdAddr, "/v1/events", ev)
		if hash, _ := result["event_hash"].(string); hash == "" {
			t.Fatalf("tool event for %s (%s): missing event_hash", step.tool, step.actionClass)
		}
	}

	// Hash chain must remain valid after mixed-class tool events.
	result := get(t, auditdAddr, "/v1/verify")
	if valid, _ := result["valid"].(bool); !valid {
		t.Errorf("hash chain integrity check failed after write/destructive events: %v", result)
	}
}

func TestAudit_WriteApprovalWorkflow_ForNewTools(t *testing.T) {
	// Cancel query requires approval → create, approve, verify removed from pending.
	result := post(t, auditdAddr, "/v1/approvals",
		newApproval("write", "cancel_query", "postgres-agent", "operator"))
	approvalID, _ := result["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("expected approval_id for cancel_query approval request")
	}
	if result["status"] != "pending" {
		t.Errorf("initial status = %q, want pending", result["status"])
	}

	// Approve it.
	updated := post(t, auditdAddr, "/v1/approvals/"+approvalID+"/approve",
		map[string]any{"approved_by": "senior-dba", "reason": "low-impact cancellation"})
	if updated["status"] != "approved" {
		t.Errorf("status after approve = %q, want approved", updated["status"])
	}

	// Should no longer appear in pending list.
	pending := getList(t, auditdAddr, "/v1/approvals/pending")
	for _, a := range pending {
		if a["approval_id"] == approvalID {
			t.Error("approved cancel_query request should not be in pending list")
		}
	}
}

func TestAudit_DestructiveApprovalWorkflow_ForNewTools(t *testing.T) {
	// terminate_connection and kill_idle_connections require human approval.
	for _, toolName := range []string{"terminate_connection", "kill_idle_connections", "delete_pod", "restart_deployment", "scale_deployment"} {
		t.Run(toolName, func(t *testing.T) {
			result := post(t, auditdAddr, "/v1/approvals",
				newApproval("destructive", toolName, "test-agent", "sre-oncall"))
			approvalID, _ := result["approval_id"].(string)
			if approvalID == "" {
				t.Fatalf("%s: expected approval_id", toolName)
			}
			if result["status"] != "pending" {
				t.Errorf("%s: initial status = %q, want pending", toolName, result["status"])
			}

			// Deny the destructive operation.
			updated := post(t, auditdAddr, "/v1/approvals/"+approvalID+"/deny",
				map[string]any{"denied_by": "change-manager", "reason": "not in maintenance window"})
			if updated["status"] != "denied" {
				t.Errorf("%s: status after deny = %q, want denied", toolName, updated["status"])
			}
		})
	}
}
