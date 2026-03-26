package agentutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
	"google.golang.org/genai"
	adktool "google.golang.org/adk/tool"

	"helpdesk/internal/audit"
	"helpdesk/internal/identity"
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
	err := e.CheckTool(context.Background(), "database", "mydb", policy.ActionRead, nil, "unit test", nil)
	if err != nil {
		t.Errorf("read action should be allowed, got: %v", err)
	}
}

func TestCheckTool_DeniedError_HasExplanation(t *testing.T) {
	e := newMinimalEnforcer(t)
	err := e.CheckTool(context.Background(), "database", "mydb", policy.ActionDestructive, nil, "unit test", nil)
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
	err := e.CheckTool(context.Background(), "database", "dev-db", policy.ActionRead, nil, "unit test", nil)
	if err != nil {
		t.Errorf("allow: expected nil error, got: %v", err)
	}
}

func TestCheckTool_RemoteCheck_Deny(t *testing.T) {
	srv := mockPolicyCheckServer(t, "deny", http.StatusForbidden)
	defer srv.Close()

	e := newRemoteEnforcer(srv.URL)
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionWrite, nil, "unit test", nil)
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
	err := e.CheckTool(context.Background(), "database", "dev-db", policy.ActionRead, nil, "unit test", nil)
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
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionWrite, nil, "unit test", nil)
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
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionDestructive, nil, "test", nil)
	if err != nil {
		t.Errorf("nil enforcer: expected nil error, got: %v", err)
	}
}

// --- readonly-governed mode mutation blocking ---

func TestCheckTool_ReadonlyGoverned_BlocksWrite(t *testing.T) {
	t.Setenv("HELPDESK_OPERATING_MODE", "readonly-governed")
	e := &PolicyEnforcer{} // no engine or remote URL needed — mode check fires first
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionWrite, nil, "test", nil)
	if err == nil {
		t.Fatal("write action in readonly-governed mode: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "readonly-governed") {
		t.Errorf("error message should mention readonly-governed, got: %v", err)
	}
}

func TestCheckTool_ReadonlyGoverned_BlocksDestructive(t *testing.T) {
	t.Setenv("HELPDESK_OPERATING_MODE", "readonly-governed")
	e := &PolicyEnforcer{}
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionDestructive, nil, "test", nil)
	if err == nil {
		t.Fatal("destructive action in readonly-governed mode: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "readonly-governed") {
		t.Errorf("error message should mention readonly-governed, got: %v", err)
	}
}

func TestCheckTool_ReadonlyGoverned_AllowsRead(t *testing.T) {
	t.Setenv("HELPDESK_OPERATING_MODE", "readonly-governed")
	// No engine, no remote URL — read passes the mode check and then hits the
	// "no enforcement" fast path, returning nil.
	e := &PolicyEnforcer{}
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionRead, nil, "test", nil)
	if err != nil {
		t.Errorf("read action in readonly-governed mode: expected nil, got: %v", err)
	}
}

func TestCheckTool_Fix_WriteNotBlockedByModeGuard(t *testing.T) {
	t.Setenv("HELPDESK_OPERATING_MODE", "fix")
	// In fix mode the mode guard must NOT block writes — enforcement is handled
	// by the policy engine or remote check. With no engine configured the call
	// passes through (no-op enforcement).
	e := &PolicyEnforcer{}
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionWrite, nil, "test", nil)
	if err != nil {
		t.Errorf("write action in fix mode with no enforcement configured: expected nil, got: %v", err)
	}
}

// TestCheckTool_ReadonlyGoverned_AuditsDenial verifies that blocking a write in
// readonly-governed mode records a policy_decision event with effect=deny and
// PolicyName=readonly_governed_mode, so the audit trail reflects the block.
func TestCheckTool_ReadonlyGoverned_AuditsDenial(t *testing.T) {
	t.Setenv("HELPDESK_OPERATING_MODE", "readonly-governed")

	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	ta := audit.NewToolAuditor(store, "test-agent", "sess-rg", "trace-rg")
	e := NewPolicyEnforcerWithConfig(PolicyEnforcerConfig{ToolAuditor: ta})

	err = e.CheckTool(context.Background(), "database", "prod-db", policy.ActionWrite, []string{"env:prod"}, "test note", nil)
	if err == nil {
		t.Fatal("expected error for write in readonly-governed mode, got nil")
	}

	// tool_invoked must have been recorded before the mode check returned.
	invokedEvents, err := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypeToolInvoked})
	if err != nil {
		t.Fatalf("Query tool_invoked: %v", err)
	}
	if len(invokedEvents) != 1 {
		t.Fatalf("expected 1 tool_invoked event, got %d", len(invokedEvents))
	}

	// policy_decision must have been recorded with deny + correct policy name.
	polEvents, err := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypePolicyDecision})
	if err != nil {
		t.Fatalf("Query policy_decision: %v", err)
	}
	if len(polEvents) != 1 {
		t.Fatalf("expected 1 policy_decision event, got %d", len(polEvents))
	}
	pd := polEvents[0].PolicyDecision
	if pd == nil {
		t.Fatal("PolicyDecision field is nil")
	}
	if pd.Effect != "deny" {
		t.Errorf("Effect = %q, want \"deny\"", pd.Effect)
	}
	if pd.PolicyName != "readonly_governed_mode" {
		t.Errorf("PolicyName = %q, want \"readonly_governed_mode\"", pd.PolicyName)
	}
	if pd.ResourceName != "prod-db" {
		t.Errorf("ResourceName = %q, want \"prod-db\"", pd.ResourceName)
	}
	if pd.Action != string(policy.ActionWrite) {
		t.Errorf("Action = %q, want %q", pd.Action, policy.ActionWrite)
	}
}

// TestCheckTool_EmitsToolInvokedWhenPolicyDisabled verifies that a tool_invoked
// event is recorded unconditionally even when policy enforcement is disabled
// (engine=nil, policyCheckURL=""). This is the core guarantee of the coverage
// gap instrumentation: we know the tool was called regardless of enforcement.
func TestCheckTool_EmitsToolInvokedWhenPolicyDisabled(t *testing.T) {
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	ta := audit.NewToolAuditor(store, "test-agent", "sess-1", "trace-cov")
	e := NewPolicyEnforcerWithConfig(PolicyEnforcerConfig{
		ToolAuditor: ta,
		// No Engine, no PolicyCheckURL — enforcement disabled.
	})

	if err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionWrite, []string{"env:prod"}, "", nil); err != nil {
		t.Errorf("expected nil (no enforcement), got: %v", err)
	}

	events, err := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypeToolInvoked})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 tool_invoked event, got %d", len(events))
	}
	evt := events[0]
	if evt.PolicyDecision == nil {
		t.Fatal("PolicyDecision field is nil on tool_invoked event")
	}
	if evt.PolicyDecision.ResourceName != "prod-db" {
		t.Errorf("ResourceName = %q, want prod-db", evt.PolicyDecision.ResourceName)
	}
	if evt.PolicyDecision.Action != string(policy.ActionWrite) {
		t.Errorf("Action = %q, want %q", evt.PolicyDecision.Action, policy.ActionWrite)
	}
	if evt.PolicyDecision.Effect != "" {
		t.Errorf("Effect = %q, want empty (not yet evaluated)", evt.PolicyDecision.Effect)
	}

	// Verify no policy_decision event was emitted (enforcement was disabled).
	polEvents, _ := store.Query(context.Background(), audit.QueryOptions{EventType: audit.EventTypePolicyDecision})
	if len(polEvents) != 0 {
		t.Errorf("expected 0 policy_decision events when enforcement disabled, got %d", len(polEvents))
	}
}

// --- Approval context (session_info threading) ---

// requireApprovalPolicyYAML is a policy that requires approval for write and
// destructive actions while allowing reads.
const requireApprovalPolicyYAML = `
version: "1"
policies:
  - name: require-approval-policy
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
      - action: write
        effect: require_approval
      - action: destructive
        effect: require_approval
`

// mockApprovalServer starts an httptest server implementing the auditd approval
// API. The POST /v1/approvals handler captures each request body and sends it to
// the returned channel; GET /v1/approvals list queries return an empty list (no
// existing approvals found) so requestApproval always creates a fresh request and
// returns ApprovalPendingError.
func mockApprovalServer(t *testing.T) (*httptest.Server, <-chan audit.ApprovalCreateRequest) {
	t.Helper()
	ch := make(chan audit.ApprovalCreateRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/approvals":
			var req audit.ApprovalCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request body", http.StatusBadRequest)
				return
			}
			ch <- req
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(audit.ApprovalCreateResponse{
				ApprovalID: "test-approval-1",
				Status:     "pending",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/approvals":
			// List approvals — return empty list (no existing approvals).
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]audit.StoredApproval{})
		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

// mockApprovalServerWithExistingApproval returns a server that always reports an
// already-approved approval for any list query (simulates the cross-turn retry
// scenario where a previous turn created an approval that has since been granted).
func mockApprovalServerWithExistingApproval(t *testing.T, approvalID, toolName string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/approvals" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]audit.StoredApproval{{
				ApprovalID: approvalID,
				Status:     "approved",
				ToolName:   toolName,
				ResolvedBy: "test-approver",
			}})
			return
		}
		http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newRequireApprovalEnforcer creates a PolicyEnforcer backed by
// requireApprovalPolicyYAML, wired to an approval client at approvalURL.
func newRequireApprovalEnforcer(t *testing.T, approvalURL string) *PolicyEnforcer {
	t.Helper()
	path := writeTempPolicyFile(t, requireApprovalPolicyYAML)
	engine, err := InitPolicyEngine(Config{PolicyEnabled: true, PolicyFile: path, DefaultPolicy: "deny"})
	if err != nil {
		t.Fatalf("InitPolicyEngine: %v", err)
	}
	return NewPolicyEnforcerWithConfig(PolicyEnforcerConfig{
		Engine:         engine,
		ApprovalClient: audit.NewApprovalClient(approvalURL),
	})
}

func TestRequestApproval_SessionInfoInContext(t *testing.T) {
	appSrv, captured := mockApprovalServer(t)
	e := newRequireApprovalEnforcer(t, appSrv.URL)

	note := "Session PID 1234\n  User:     app\n  Database: prod\n  State:    active"
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionWrite, nil, note, nil)
	// requestApproval now returns immediately with ApprovalPendingError instead of blocking.
	var pending *ApprovalPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("CheckTool (require_approval): expected *ApprovalPendingError, got %T: %v", err, err)
	}
	if pending.ApprovalID == "" {
		t.Error("ApprovalPendingError.ApprovalID must not be empty")
	}

	select {
	case req := <-captured:
		if req.Context == nil {
			t.Fatal("approval request_context is nil; expected session_info to be populated")
		}
		got, ok := req.Context["session_info"]
		if !ok {
			t.Fatalf("approval request_context missing 'session_info' key; got: %v", req.Context)
		}
		if !containsStr(fmt.Sprintf("%v", got), "PID 1234") {
			t.Errorf("session_info = %v, want to contain 'PID 1234'", got)
		}
	default:
		t.Fatal("no approval request was captured by mock server")
	}
}

func TestRequestApproval_NoSessionInfoWhenNoteEmpty(t *testing.T) {
	appSrv, captured := mockApprovalServer(t)
	e := newRequireApprovalEnforcer(t, appSrv.URL)

	// Empty note → session_info must NOT appear in the approval context.
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionWrite, nil, "", nil)
	var pending *ApprovalPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("CheckTool: expected *ApprovalPendingError, got %T: %v", err, err)
	}

	select {
	case req := <-captured:
		if _, ok := req.Context["session_info"]; ok {
			t.Errorf("empty note must not add session_info to context; got: %v", req.Context)
		}
	default:
		t.Fatal("no approval request was captured")
	}
}

func TestCheckTool_RequireApproval_RemoteCheck_NoteForwarded(t *testing.T) {
	// Remote governance check (handleRemoteResponse) returns require_approval;
	// the local approval client must receive the note in request_context.session_info.
	govSrv := mockPolicyCheckServer(t, "require_approval", http.StatusOK)
	defer govSrv.Close()

	appSrv, captured := mockApprovalServer(t)
	e := NewPolicyEnforcerWithConfig(PolicyEnforcerConfig{
		PolicyCheckURL: govSrv.URL,
		ApprovalClient: audit.NewApprovalClient(appSrv.URL),
	})

	note := "Session PID 9999\n  User:     slow_client\n  State:    active (5m 10s)"
	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionDestructive, nil, note, nil)
	var pending *ApprovalPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("CheckTool (remote require_approval): expected *ApprovalPendingError, got %T: %v", err, err)
	}

	select {
	case req := <-captured:
		if req.Context == nil {
			t.Fatal("approval request_context is nil")
		}
		got, ok := req.Context["session_info"]
		if !ok {
			t.Fatalf("session_info missing from approval request_context via remote check path; got: %v", req.Context)
		}
		if !containsStr(fmt.Sprintf("%v", got), "PID 9999") {
			t.Errorf("session_info = %v, want to contain 'PID 9999'", got)
		}
	default:
		t.Fatal("no approval request captured via remote check path")
	}
}

func TestRequestApproval_CrossTurnRetry_UsesExistingApproval(t *testing.T) {
	// Simulate the second turn: a prior turn already created an approval that has
	// since been granted. The list endpoint returns it as approved, so requestApproval
	// should allow the operation without creating a new request.
	appSrv := mockApprovalServerWithExistingApproval(t, "apr_existing", "database:prod-db")
	e := newRequireApprovalEnforcer(t, appSrv.URL)

	err := e.CheckTool(context.Background(), "database", "prod-db", policy.ActionWrite, nil, "", nil)
	if err != nil {
		t.Fatalf("CheckTool (cross-turn retry with approved approval): expected nil, got: %v", err)
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

func TestApplyCardOptions_SkillFleetEligible(t *testing.T) {
	card := &a2a.AgentCard{
		Name: "test",
		Skills: []a2a.AgentSkill{
			{ID: "agent-get_status_summary"},
			{ID: "agent-get_server_info"},
		},
	}
	applyCardOptions(card, CardOptions{
		SkillFleetEligible: map[string]bool{
			"agent-get_status_summary": true,
			"agent-get_server_info":    false, // explicit false should not append fleet:true
		},
	})

	hasFleetsTag := func(skill a2a.AgentSkill) bool {
		for _, tag := range skill.Tags {
			if tag == "fleet:true" {
				return true
			}
		}
		return false
	}

	if !hasFleetsTag(card.Skills[0]) {
		t.Errorf("get_status_summary: expected fleet:true tag, got tags %v", card.Skills[0].Tags)
	}
	if hasFleetsTag(card.Skills[1]) {
		t.Errorf("get_server_info: expected no fleet:true tag (false), got tags %v", card.Skills[1].Tags)
	}
}

func TestApplyCardOptions_SkillCapabilities(t *testing.T) {
	card := &a2a.AgentCard{
		Name: "test",
		Skills: []a2a.AgentSkill{
			{ID: "agent-get_status_summary"},
			{ID: "agent-check_connection"},
		},
	}
	applyCardOptions(card, CardOptions{
		SkillCapabilities: map[string][]string{
			"agent-get_status_summary": {"uptime", "connection_count"},
			"agent-check_connection":   {"connectivity"},
		},
	})

	wantTags := func(skill a2a.AgentSkill, expected []string) {
		t.Helper()
		tagSet := make(map[string]bool, len(skill.Tags))
		for _, tag := range skill.Tags {
			tagSet[tag] = true
		}
		for _, want := range expected {
			if !tagSet["cap:"+want] {
				t.Errorf("skill %q: expected tag %q, got tags %v", skill.ID, "cap:"+want, skill.Tags)
			}
		}
	}

	wantTags(card.Skills[0], []string{"uptime", "connection_count"})
	wantTags(card.Skills[1], []string{"connectivity"})

	// Unrelated skill should have no cap: tags.
	if len(card.Skills[0].Tags) != 2 {
		t.Errorf("get_status_summary: expected exactly 2 tags, got %v", card.Skills[0].Tags)
	}
}

func TestApplyCardOptions_SkillSupersedes(t *testing.T) {
	card := &a2a.AgentCard{
		Name: "test",
		Skills: []a2a.AgentSkill{
			{ID: "agent-get_status_summary"},
			{ID: "agent-get_server_info"},
		},
	}
	applyCardOptions(card, CardOptions{
		SkillSupersedes: map[string][]string{
			"agent-get_status_summary": {"get_server_info", "get_connection_stats"},
		},
	})

	hasSupersedesTag := func(skill a2a.AgentSkill, name string) bool {
		for _, tag := range skill.Tags {
			if tag == "supersedes:"+name {
				return true
			}
		}
		return false
	}

	if !hasSupersedesTag(card.Skills[0], "get_server_info") {
		t.Errorf("get_status_summary: expected supersedes:get_server_info, got %v", card.Skills[0].Tags)
	}
	if !hasSupersedesTag(card.Skills[0], "get_connection_stats") {
		t.Errorf("get_status_summary: expected supersedes:get_connection_stats, got %v", card.Skills[0].Tags)
	}
	if len(card.Skills[1].Tags) != 0 {
		t.Errorf("get_server_info: expected no tags, got %v", card.Skills[1].Tags)
	}
}

func TestApplyCardOptions_TaxonomyRoundTrip(t *testing.T) {
	// Verify the full wire format: applyCardOptions serializes typed fields to
	// key:value tag strings, and parseSkillTags (in toolregistry) deserializes them.
	// This ensures the two halves of the pipeline agree on the format.
	card := &a2a.AgentCard{
		Name: "myagent",
		Skills: []a2a.AgentSkill{
			{ID: "myagent-get_status_summary"},
		},
	}
	applyCardOptions(card, CardOptions{
		SkillFleetEligible: map[string]bool{
			"myagent-get_status_summary": true,
		},
		SkillCapabilities: map[string][]string{
			"myagent-get_status_summary": {"uptime", "connection_count"},
		},
		SkillSupersedes: map[string][]string{
			"myagent-get_status_summary": {"get_server_info"},
		},
	})

	tags := card.Skills[0].Tags
	// Manually parse using the same logic as toolregistry.parseSkillTags.
	var fleetEligible bool
	var caps, supersedes []string
	for _, tag := range tags {
		switch {
		case tag == "fleet:true":
			fleetEligible = true
		case len(tag) > 4 && tag[:4] == "cap:":
			caps = append(caps, tag[4:])
		case len(tag) > 11 && tag[:11] == "supersedes:":
			supersedes = append(supersedes, tag[11:])
		}
	}

	if !fleetEligible {
		t.Error("round-trip: fleet:true not found in tags")
	}
	if len(caps) != 2 {
		t.Errorf("round-trip: caps = %v, want [uptime connection_count]", caps)
	}
	if len(supersedes) != 1 || supersedes[0] != "get_server_info" {
		t.Errorf("round-trip: supersedes = %v, want [get_server_info]", supersedes)
	}
}

// ── Identity & Purpose propagation ───────────────────────────────────────────

// mockCapturingPolicyServer captures the decoded policyCheckReq and returns the
// given effect. The captured request is sent on the returned channel.
func mockCapturingPolicyServer(t *testing.T, effect string, httpStatus int) (*httptest.Server, <-chan policyCheckReq) {
	t.Helper()
	ch := make(chan policyCheckReq, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/governance/check" || r.Method != http.MethodPost {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var req policyCheckReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		ch <- req
		resp := policyCheckResp{
			Effect:      effect,
			PolicyName:  "mock-policy",
			Explanation: "mock explanation: " + strings.ToUpper(effect),
			EventID:     "pol_mock0001",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		json.NewEncoder(w).Encode(resp)
	}))
	return srv, ch
}

// ctxWithPrincipalAndPurpose returns a context carrying the given identity and purpose
// via a TraceContext, matching what TraceMiddleware injects in production.
func ctxWithPrincipalAndPurpose(principal identity.ResolvedPrincipal, purpose, purposeNote string) context.Context {
	tc := audit.NewTraceContext("test", principal)
	tc.Purpose = purpose
	tc.PurposeNote = purposeNote
	return audit.WithTraceContext(context.Background(), tc)
}

func TestCheckTool_RemoteCheck_PropagatesPrincipal(t *testing.T) {
	srv, captured := mockCapturingPolicyServer(t, "allow", http.StatusOK)
	defer srv.Close()

	principal := identity.ResolvedPrincipal{
		UserID:     "alice@example.com",
		Roles:      []string{"dba", "sre"},
		AuthMethod: "jwt",
	}
	ctx := ctxWithPrincipalAndPurpose(principal, "diagnostic", "investigating INC-123")
	e := newRemoteEnforcer(srv.URL)
	if err := e.CheckTool(ctx, "database", "prod-db", policy.ActionRead, nil, "test", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := <-captured
	if req.Principal.UserID != "alice@example.com" {
		t.Errorf("Principal.UserID = %q, want alice@example.com", req.Principal.UserID)
	}
	if len(req.Principal.Roles) != 2 || req.Principal.Roles[0] != "dba" {
		t.Errorf("Principal.Roles = %v, want [dba sre]", req.Principal.Roles)
	}
	if req.Principal.AuthMethod != "jwt" {
		t.Errorf("Principal.AuthMethod = %q, want jwt", req.Principal.AuthMethod)
	}
	if req.Purpose != "diagnostic" {
		t.Errorf("Purpose = %q, want diagnostic", req.Purpose)
	}
	if req.PurposeNote != "investigating INC-123" {
		t.Errorf("PurposeNote = %q, want 'investigating INC-123'", req.PurposeNote)
	}
}

func TestCheckTool_RemoteCheck_PropagatesServicePrincipal(t *testing.T) {
	srv, captured := mockCapturingPolicyServer(t, "allow", http.StatusOK)
	defer srv.Close()

	principal := identity.ResolvedPrincipal{
		Service:    "srebot",
		AuthMethod: "api_key",
	}
	ctx := ctxWithPrincipalAndPurpose(principal, "", "")
	e := newRemoteEnforcer(srv.URL)
	if err := e.CheckTool(ctx, "database", "dev-db", policy.ActionRead, nil, "automated", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := <-captured
	if req.Principal.Service != "srebot" {
		t.Errorf("Principal.Service = %q, want srebot", req.Principal.Service)
	}
	if req.Principal.AuthMethod != "api_key" {
		t.Errorf("Principal.AuthMethod = %q, want api_key", req.Principal.AuthMethod)
	}
	if req.Principal.UserID != "" {
		t.Errorf("Principal.UserID = %q, want empty for service principal", req.Principal.UserID)
	}
}

func TestCheckTool_LocalEngine_PrincipalInPolicyRequest(t *testing.T) {
	// Use a policy that allows dba role and denies others.
	yaml := `
version: "1"
policies:
  - name: dba-only
    priority: 100
    principals:
      - role: dba
    resources:
      - type: database
    rules:
      - action: write
        effect: allow
  - name: default-deny
    resources:
      - type: database
    rules:
      - action: write
        effect: deny
        message: "unauthorized"
`
	cfg, err := policy.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	engine := policy.NewEngine(policy.EngineConfig{PolicyConfig: cfg})
	e := NewPolicyEnforcerWithConfig(PolicyEnforcerConfig{Engine: engine})

	// DBA principal — should be allowed.
	dbaCtx := ctxWithPrincipalAndPurpose(
		identity.ResolvedPrincipal{UserID: "alice@example.com", Roles: []string{"dba"}, AuthMethod: "jwt"},
		"", "",
	)
	if err := e.CheckTool(dbaCtx, "database", "prod-db", policy.ActionWrite, nil, "test", nil); err != nil {
		t.Errorf("dba should be allowed, got: %v", err)
	}

	// Developer principal — should be denied.
	devCtx := ctxWithPrincipalAndPurpose(
		identity.ResolvedPrincipal{UserID: "bob@example.com", Roles: []string{"developer"}, AuthMethod: "jwt"},
		"", "",
	)
	if err := e.CheckTool(devCtx, "database", "prod-db", policy.ActionWrite, nil, "test", nil); err == nil {
		t.Error("developer should be denied, got nil error")
	}
}

func TestCheckTool_LocalEngine_PurposeInPolicyRequest(t *testing.T) {
	// Diagnostic purpose is read-only; remediation can write.
	yaml := `
version: "1"
policies:
  - name: diagnostic-readonly
    priority: 90
    resources:
      - type: database
    rules:
      - action: [write, destructive]
        effect: allow
        conditions:
          blocked_purposes: [diagnostic]
  - name: default-allow-read
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
`
	cfg, err := policy.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	engine := policy.NewEngine(policy.EngineConfig{PolicyConfig: cfg})
	e := NewPolicyEnforcerWithConfig(PolicyEnforcerConfig{Engine: engine})

	// diagnostic purpose — write should be denied.
	diagCtx := ctxWithPrincipalAndPurpose(identity.ResolvedPrincipal{UserID: "alice@example.com"}, "diagnostic", "")
	if err := e.CheckTool(diagCtx, "database", "prod-db", policy.ActionWrite, nil, "test", nil); err == nil {
		t.Error("diagnostic purpose: write should be denied, got nil")
	}

	// remediation purpose — write should be allowed.
	remCtx := ctxWithPrincipalAndPurpose(identity.ResolvedPrincipal{UserID: "alice@example.com"}, "remediation", "INC-5678")
	if err := e.CheckTool(remCtx, "database", "prod-db", policy.ActionWrite, nil, "test", nil); err != nil {
		t.Errorf("remediation purpose: write should be allowed, got: %v", err)
	}
}

// ── Schema fingerprint helpers ────────────────────────────────────────────────

// mockSchemaTool implements the minimal interface needed by ComputeSchemaFingerprints
// and ComputeInputSchemas: tool.Tool + declarationProvider.
type mockSchemaTool struct {
	name string
	decl *genai.FunctionDeclaration
}

func (m *mockSchemaTool) Name() string                                        { return m.name }
func (m *mockSchemaTool) Description() string                                 { return "mock " + m.name }
func (m *mockSchemaTool) IsLongRunning() bool                                 { return false }
func (m *mockSchemaTool) Declaration() *genai.FunctionDeclaration             { return m.decl }

func makeMockSchemaTool(name string, params *genai.Schema) *mockSchemaTool {
	return &mockSchemaTool{
		name: name,
		decl: &genai.FunctionDeclaration{
			Name:        name,
			Description: "mock " + name,
			Parameters:  params,
		},
	}
}

func TestComputeSchemaFingerprints_Basic(t *testing.T) {
	params := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"connection_string": {Type: genai.TypeString},
		},
		Required: []string{"connection_string"},
	}
	tool1 := makeMockSchemaTool("check_connection", params)
	tool2 := makeMockSchemaTool("no_params_tool", nil) // no parameters → omitted

	fps := ComputeSchemaFingerprints("myagent", []adktool.Tool{tool1, tool2})

	if _, ok := fps["myagent-check_connection"]; !ok {
		t.Error("expected fingerprint for check_connection")
	}
	if _, ok := fps["myagent-no_params_tool"]; ok {
		t.Error("no_params_tool should be omitted (nil params)")
	}
	if len(fps["myagent-check_connection"]) != 12 {
		t.Errorf("fingerprint length = %d, want 12", len(fps["myagent-check_connection"]))
	}
}

func TestComputeSchemaFingerprints_Deterministic(t *testing.T) {
	params := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"pid": {Type: genai.TypeInteger},
		},
		Required: []string{"pid"},
	}
	tool := makeMockSchemaTool("terminate_connection", params)
	fps1 := ComputeSchemaFingerprints("db", []adktool.Tool{tool})
	fps2 := ComputeSchemaFingerprints("db", []adktool.Tool{tool})
	if fps1["db-terminate_connection"] != fps2["db-terminate_connection"] {
		t.Error("ComputeSchemaFingerprints is not deterministic")
	}
}

func TestComputeInputSchemas_Basic(t *testing.T) {
	params := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"connection_string": {Type: genai.TypeString, Description: "PostgreSQL DSN"},
		},
		Required: []string{"connection_string"},
	}
	tool := makeMockSchemaTool("check_connection", params)
	schemas := ComputeInputSchemas([]adktool.Tool{tool})

	if _, ok := schemas["check_connection"]; !ok {
		t.Fatal("expected schema for check_connection")
	}
	schema := schemas["check_connection"]
	if _, ok := schema["properties"]; !ok {
		t.Error("schema missing 'properties' key")
	}
}

func TestComputeInputSchemas_NoDeclaration(t *testing.T) {
	tool := &mockSchemaTool{name: "bare_tool"} // nil decl → omitted
	schemas := ComputeInputSchemas([]adktool.Tool{tool})
	if _, ok := schemas["bare_tool"]; ok {
		t.Error("bare_tool (nil Declaration) should be omitted from schemas")
	}
}

func TestApplyCardOptions_SkillSchemaHash(t *testing.T) {
	card := &a2a.AgentCard{
		Name: "myagent",
		Skills: []a2a.AgentSkill{
			{ID: "myagent-check_connection"},
			{ID: "myagent-get_pods"},
		},
	}
	applyCardOptions(card, CardOptions{
		SkillSchemaHash: map[string]string{
			"myagent-check_connection": "abc123def456",
		},
	})

	var foundHash string
	for _, tag := range card.Skills[0].Tags {
		if strings.HasPrefix(tag, "schema_hash:") {
			foundHash = strings.TrimPrefix(tag, "schema_hash:")
		}
	}
	if foundHash != "abc123def456" {
		t.Errorf("schema_hash tag = %q, want %q", foundHash, "abc123def456")
	}
	// Second skill has no hash — verify no spurious tag.
	for _, tag := range card.Skills[1].Tags {
		if strings.HasPrefix(tag, "schema_hash:") {
			t.Errorf("unexpected schema_hash tag on skill without hash: %q", tag)
		}
	}
}

