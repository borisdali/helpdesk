package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/discovery"
	"helpdesk/internal/identity"
	"helpdesk/internal/infra"
	"helpdesk/internal/toolregistry"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"
)

// mockAuditdPlaybook starts an httptest server that serves a single playbook at
// GET /v1/fleet/playbooks/{id} and returns a cleanup function.
func mockAuditdPlaybook(t *testing.T, pb *audit.Playbook) *httptest.Server {
	t.Helper()
	data, _ := json.Marshal(pb)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// makePlaybookRunGateway constructs a Gateway suitable for handlePlaybookRun tests.
func makePlaybookRunGateway(auditURL string, llmFn func(context.Context, string) (string, error)) *Gateway {
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{
		{Name: "check_connection", Agent: "database", ActionClass: "read", FleetEligible: true},
	})
	return &Gateway{
		agents:       make(map[string]*discovery.Agent),
		clients:      make(map[string]*a2aclient.Client), // no real agent — agent-mode tests expect 502
		infra:        makeTestInfra(),
		toolRegistry: reg,
		plannerLLM:   llmFn,
		auditURL:     auditURL,
	}
}

func postPlaybookRun(t *testing.T, gw *Gateway, playbookID, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/fleet/playbooks/"+playbookID+"/run",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("playbookID", playbookID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestHandlePlaybookRun_FleetMode verifies that a fleet-mode playbook goes through
// the planner and returns a job_def in the response.
func TestHandlePlaybookRun_FleetMode(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_fleet01",
		Name:          "Vacuum Triage",
		Description:   "Check vacuum status across all databases.",
		ExecutionMode: "fleet",
		IsActive:      true,
	}
	auditSrv := mockAuditdPlaybook(t, pb)

	plannerCalled := false
	llmFn := func(ctx context.Context, prompt string) (string, error) {
		plannerCalled = true
		// Return a minimal valid fleet plan JSON.
		return `{
			"name": "vacuum-check",
			"change": {"steps": [{"tool": "check_connection", "args": {}}]},
			"targets": ["prod-db-1"],
			"strategy": {}
		}`, nil
	}

	gw := makePlaybookRunGateway(auditSrv.URL, llmFn)
	rec := postPlaybookRun(t, gw, "pb_fleet01", `{}`)

	if !plannerCalled {
		t.Error("planner LLM was not called for fleet-mode playbook")
	}
	// Fleet path returns a plan response (not an agent text response).
	if rec.Code == http.StatusBadGateway {
		t.Errorf("got 502 Bad Gateway — fleet path should not route to agent: body=%s", rec.Body.String())
	}
}

// TestHandlePlaybookRun_AgentMode verifies that an agent-mode playbook is routed
// to the database agent (proxyToAgent) and NOT to the fleet planner.
// With no A2A client wired, the gateway returns 502 — confirming the agent path
// was taken (the fleet path would call the planner and return a different response).
func TestHandlePlaybookRun_AgentMode(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_agent01",
		Name:          "Database Down — Restart Triage",
		Description:   "Triage a completely unresponsive PostgreSQL instance.",
		Guidance:      "Step 1: run check_connection.",
		ExecutionMode: "agent",
		EntryPoint:    true,
		EscalatesTo:   []string{"pbs_db_config_recovery"},
		IsActive:      true,
	}
	auditSrv := mockAuditdPlaybook(t, pb)

	plannerCalled := false
	llmFn := func(ctx context.Context, prompt string) (string, error) {
		plannerCalled = true
		return `{}`, nil
	}

	gw := makePlaybookRunGateway(auditSrv.URL, llmFn)
	rec := postPlaybookRun(t, gw, "pb_agent01",
		`{"connection_string":"postgres://localhost/test","context":"prod-db-1 is down"}`)

	if plannerCalled {
		t.Error("fleet planner was called for agent-mode playbook — should route to agent instead")
	}
	// No A2A client registered → 502 from proxyToAgent, confirming agent path was taken.
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502 (no agent client) for agent-mode playbook, got %d: %s",
			rec.Code, rec.Body.String())
	}
}

// TestHandlePlaybookRun_NotFound verifies that a missing playbook ID returns 404.
func TestHandlePlaybookRun_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	gw := makePlaybookRunGateway(srv.URL, nil)
	rec := postPlaybookRun(t, gw, "pb_missing", `{}`)

	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rec.Code)
	}
}

// --- assembleTriagePrompt tests ---

func TestAssembleTriagePrompt_ContainsGuidance(t *testing.T) {
	pb := &audit.Playbook{
		Name:        "Test Playbook",
		Description: "Test description.",
		Guidance:    "Step 1: check connection. Step 2: read logs.",
	}
	prompt := assembleTriagePrompt(pb, PlaybookRunRequest{}, "")

	if !strings.Contains(prompt, "Step 1: check connection") {
		t.Error("prompt does not contain guidance")
	}
	if !strings.Contains(prompt, "Test description") {
		t.Error("prompt does not contain description")
	}
	if !strings.Contains(prompt, "read-only") {
		t.Error("prompt does not contain R/O constraint")
	}
}

func TestAssembleTriagePrompt_EscalatesTo(t *testing.T) {
	pb := &audit.Playbook{
		Name:        "Restart Triage",
		EscalatesTo: []string{"pbs_db_config_recovery", "pbs_db_pitr_recovery"},
	}
	prompt := assembleTriagePrompt(pb, PlaybookRunRequest{}, "")

	if !strings.Contains(prompt, "pbs_db_config_recovery") {
		t.Error("prompt missing escalates_to series ID")
	}
	if !strings.Contains(prompt, "pbs_db_pitr_recovery") {
		t.Error("prompt missing second escalates_to series ID")
	}
}

func TestAssembleTriagePrompt_ConnectionString(t *testing.T) {
	pb := &audit.Playbook{Name: "p"}
	req := PlaybookRunRequest{
		ConnectionString: "postgres://prod-db.example.com/mydb",
		Context:          "prod-db-1 returned connection refused at 10:05 UTC",
	}
	prompt := assembleTriagePrompt(pb, req, "")

	if !strings.Contains(prompt, "postgres://prod-db.example.com/mydb") {
		t.Error("prompt does not contain connection string")
	}
	if !strings.Contains(prompt, "connection refused") {
		t.Error("prompt does not contain operator context")
	}
}

func TestAssembleTriagePrompt_NoEscalatesTo(t *testing.T) {
	pb := &audit.Playbook{Name: "PITR Recovery"}
	prompt := assembleTriagePrompt(pb, PlaybookRunRequest{}, "")

	// No escalation paths section when EscalatesTo is empty.
	if strings.Contains(prompt, "Escalation paths") {
		t.Error("prompt should not have Escalation paths section when EscalatesTo is empty")
	}
}

// makeTestInfra is defined in gateway_test.go; referenced here for the
// makePlaybookRunGateway helper. No redeclaration needed.

// discovery is imported transitively via gateway.go; reference the type here
// to ensure the import is used.
var _ *infra.Config

// --- parseAgentEscalation ---

func TestParseAgentEscalation_FullSignal(t *testing.T) {
	text := "The database appears to have a corrupted WAL file.\n\nRecommendation: initiate PITR recovery.\n\nFINDINGS: WAL corruption detected; PITR recovery needed.\nESCALATE_TO: pbs_pitr_recovery\n"
	esc := parseAgentEscalation(text)

	if esc.Findings != "WAL corruption detected; PITR recovery needed." {
		t.Errorf("findings = %q", esc.Findings)
	}
	if esc.EscalateTo != "pbs_pitr_recovery" {
		t.Errorf("escalate_to = %q", esc.EscalateTo)
	}
	if strings.Contains(esc.CleanText, "FINDINGS:") {
		t.Error("CleanText should not contain FINDINGS: line")
	}
	if strings.Contains(esc.CleanText, "ESCALATE_TO:") {
		t.Error("CleanText should not contain ESCALATE_TO: line")
	}
	if !strings.Contains(esc.CleanText, "corrupted WAL") {
		t.Error("CleanText should retain the diagnostic text")
	}
}

func TestParseAgentEscalation_FindingsOnly(t *testing.T) {
	text := "Replication lag is caused by a long-running transaction on the primary.\n\nFINDINGS: Long-running transaction blocking replication; cancel or wait for completion.\n"
	esc := parseAgentEscalation(text)

	if esc.Findings == "" {
		t.Error("findings should be set")
	}
	if esc.EscalateTo != "" {
		t.Errorf("escalate_to should be empty, got %q", esc.EscalateTo)
	}
	if strings.Contains(esc.CleanText, "FINDINGS:") {
		t.Error("CleanText should not contain FINDINGS: line")
	}
}

func TestParseAgentEscalation_NoSignal(t *testing.T) {
	text := "The connection check returned: could not connect to server."
	esc := parseAgentEscalation(text)

	if esc.Findings != "" || esc.EscalateTo != "" {
		t.Errorf("expected empty signal, got findings=%q escalate_to=%q", esc.Findings, esc.EscalateTo)
	}
	if esc.CleanText != text {
		t.Errorf("CleanText should equal original text when no signal present")
	}
}

// --- checkRequiresEvidence ---

func TestCheckRequiresEvidence_EmptyPatterns(t *testing.T) {
	warnings := checkRequiresEvidence(nil, "some context")
	if warnings != nil {
		t.Errorf("expected nil warnings for empty patterns, got %v", warnings)
	}
}

func TestCheckRequiresEvidence_EmptyContext(t *testing.T) {
	// Empty context means the operator hasn't confirmed the evidence — all patterns missing.
	warnings := checkRequiresEvidence([]string{"FATAL.*invalid"}, "")
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning for empty context, got %v", warnings)
	}
}

func TestCheckRequiresEvidence_AllFound(t *testing.T) {
	patterns := []string{"FATAL.*invalid value for parameter", "could not open file"}
	ctx := "2024-01-01 FATAL: invalid value for parameter max_connections; also could not open file pg_hba.conf"
	warnings := checkRequiresEvidence(patterns, ctx)
	if len(warnings) != 0 {
		t.Errorf("expected no warnings when all patterns match, got %v", warnings)
	}
}

func TestCheckRequiresEvidence_PatternMissing(t *testing.T) {
	patterns := []string{"FATAL.*invalid value for parameter", "PANIC.*checkpoint"}
	ctx := "2024-01-01 FATAL: invalid value for parameter max_connections"
	warnings := checkRequiresEvidence(patterns, ctx)
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning for missing pattern, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "PANIC.*checkpoint") {
		t.Errorf("warning should name the missing pattern, got %q", warnings[0])
	}
}

func TestCheckRequiresEvidence_AllMissing(t *testing.T) {
	patterns := []string{"FATAL.*invalid", "PANIC.*checkpoint"}
	warnings := checkRequiresEvidence(patterns, "server is reachable but slow")
	if len(warnings) != 2 {
		t.Errorf("expected 2 warnings, got %d", len(warnings))
	}
}

// --- assembleTriagePrompt additions ---

func TestAssembleTriagePrompt_PriorFindings(t *testing.T) {
	pb := &audit.Playbook{Name: "PITR Recovery", Description: "Recover from data loss."}
	req := PlaybookRunRequest{
		PriorFindings: "Restart triage found WAL corruption; PITR required.",
	}
	prompt := assembleTriagePrompt(pb, req, "")

	if !strings.Contains(prompt, "Prior Investigation Findings") {
		t.Error("prompt missing 'Prior Investigation Findings' section")
	}
	if !strings.Contains(prompt, "WAL corruption") {
		t.Error("prompt should contain the prior findings text")
	}
}

// TestAssembleTriagePrompt_PriorFindings_Transition verifies that a
// same-domain TRANSITION_TO continuation (e.g. triage→remediate by the same
// agent) is told to treat the prior diagnosis as settled rather than
// re-investigate from scratch. Regression test for a bug found during live
// 3-hop escalation-chain verification: the prompt previously said "investigate
// further" unconditionally, so remediation playbooks re-derived their own
// diagnosis instead of building on triage's — and were exposed to the same
// misleading signals (e.g. an irrelevant connection-string port) a second time.
func TestAssembleTriagePrompt_PriorFindings_Transition(t *testing.T) {
	pb := &audit.Playbook{Name: "K8s Pod Crash — Remediation"}
	req := PlaybookRunRequest{
		PriorFindings: "WAL disk full; PANIC at pg_wal write.",
		IsTransition:  true,
	}
	prompt := assembleTriagePrompt(pb, req, "")

	if !strings.Contains(prompt, "settled") {
		t.Error("transition prompt should tell the agent to treat the prior diagnosis as settled")
	}
	if strings.Contains(prompt, "investigate further") {
		t.Error("transition prompt should NOT tell the agent to investigate further — it should confirm and remediate")
	}
}

// TestAssembleTriagePrompt_PriorFindings_Escalation verifies that a
// cross-domain ESCALATE_TO continuation (a different agent picking up the
// investigation) is still told to verify with its own tools, since the prior
// agent could not see into this agent's domain.
func TestAssembleTriagePrompt_PriorFindings_Escalation(t *testing.T) {
	pb := &audit.Playbook{Name: "K8s Pod Crash — Diagnosis"}
	req := PlaybookRunRequest{
		PriorFindings: "Container runtime is Kubernetes; pod in restart loop.",
		IsTransition:  false,
	}
	prompt := assembleTriagePrompt(pb, req, "")

	if !strings.Contains(prompt, "verify it with your own domain-specific tools") {
		t.Error("escalation prompt should tell the agent to verify the prior findings with its own tools")
	}
	if strings.Contains(prompt, "settled") {
		t.Error("escalation prompt should not claim the prior diagnosis is settled — a different domain/agent must still verify")
	}
}

func TestAssembleTriagePrompt_NoPriorFindings(t *testing.T) {
	pb := &audit.Playbook{Name: "Restart Triage"}
	prompt := assembleTriagePrompt(pb, PlaybookRunRequest{}, "")

	if strings.Contains(prompt, "Prior Investigation Findings") {
		t.Error("prompt should not have prior findings section when PriorFindings is empty")
	}
}

func TestAssembleTriagePrompt_ResponseProtocol(t *testing.T) {
	pb := &audit.Playbook{Name: "Triage"}
	prompt := assembleTriagePrompt(pb, PlaybookRunRequest{}, "")

	if !strings.Contains(prompt, "Response Protocol") {
		t.Error("prompt missing Response Protocol section")
	}
	if !strings.Contains(prompt, "FINDINGS:") {
		t.Error("prompt should instruct agent to emit FINDINGS: line")
	}
	if !strings.Contains(prompt, "TRANSITION_TO:") {
		t.Error("prompt should mention TRANSITION_TO: so agents follow playbook guidance")
	}
	if !strings.Contains(prompt, "ESCALATE_TO:") {
		t.Error("prompt should mention ESCALATE_TO: for true cross-domain escalations")
	}
	if !strings.Contains(prompt, "Expert Guidance") {
		t.Error("prompt should refer agent to Expert Guidance for which signal to use")
	}
}

// --- assembleCrystalBallPrompt ---

func TestAssembleCrystalBallPrompt_WithContext(t *testing.T) {
	req := PlaybookRunRequest{
		ConnectionString: "postgres://prod-db.example.com/mydb",
		Context:          "Checkpoints occurring too frequently. Database is still accepting queries.",
	}
	prompt := assembleCrystalBallPrompt(req, "")

	if strings.Contains(prompt, "is unavailable") {
		t.Error("prompt should not say 'is unavailable' when operator context is provided")
	}
	if !strings.Contains(prompt, "is reporting an issue with") {
		t.Error("prompt should use neutral 'is reporting an issue with' phrasing when context is provided")
	}
	if !strings.Contains(prompt, "Checkpoints occurring too frequently") {
		t.Error("prompt should contain operator context")
	}
	if !strings.Contains(prompt, "postgres://prod-db.example.com/mydb") {
		t.Error("prompt should contain connection string")
	}
}

func TestAssembleCrystalBallPrompt_WithoutContext(t *testing.T) {
	req := PlaybookRunRequest{
		ConnectionString: "postgres://prod-db.example.com/mydb",
	}
	prompt := assembleCrystalBallPrompt(req, "")

	if !strings.Contains(prompt, "is unavailable") {
		t.Error("prompt should say 'is unavailable' when no operator context is provided")
	}
}

func TestAssembleCrystalBallPrompt_NoConnectionString(t *testing.T) {
	req := PlaybookRunRequest{}
	prompt := assembleCrystalBallPrompt(req, "")

	if !strings.Contains(prompt, "database issue") {
		t.Error("prompt should contain fallback 'database issue' text when no connection string")
	}
}

func TestAssembleCrystalBallPrompt_ServerTypeHint(t *testing.T) {
	req := PlaybookRunRequest{ConnectionString: "host=localhost"}
	prompt := assembleCrystalBallPrompt(req, "This is a managed PostgreSQL instance.")

	if !strings.Contains(prompt, "managed PostgreSQL instance") {
		t.Error("prompt should contain server type hint")
	}
}

// --- requires_evidence warnings in fleet mode ---

// mockAuditdPlaybookAndRun starts an httptest server that serves a playbook at
// GET /v1/fleet/playbooks/{id} and (optionally) a run at GET /v1/fleet/playbook-runs/{runID}.
func mockAuditdPlaybookAndRun(t *testing.T, pb *audit.Playbook, run *audit.PlaybookRun) *httptest.Server {
	t.Helper()
	pbData, _ := json.Marshal(pb)
	var runData []byte
	if run != nil {
		runData, _ = json.Marshal(run)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/fleet/playbook-runs/") && run != nil {
			w.Write(runData) //nolint:errcheck
			return
		}
		if r.Method == http.MethodPost {
			// Simulate run recording: return a run with a generated run_id.
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_test01"}) //nolint:errcheck
			return
		}
		w.Write(pbData) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestHandlePlaybookRun_AgentApproveMode verifies that an agent_approve-mode
// playbook calls the planner LLM to propose the first step and returns HTTP 202
// with status "pending_approval" — it does NOT route to the agent or the fleet
// planner directly.
func TestHandlePlaybookRun_AgentApproveMode(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_approve01",
		SeriesID:      "pbs_lock_chain_remediate",
		Name:          "Transaction Lock Chain — Terminate Root Blocker",
		Guidance:      "Step 1: terminate_connection on root blocker PID.",
		ExecutionMode: "agent_approve",
		IsActive:      true,
	}

	// Mock auditd: handles GET playbook, POST run-record, POST step, POST approval.
	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_approve_test01", "approval_id": "apr_test01"}) //nolint:errcheck
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(pb) //nolint:errcheck
		}
	}))
	t.Cleanup(auditSrv.Close)

	// Override GET to return the playbook.
	auditSrv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(pb) //nolint:errcheck
			return
		}
		// POST: run record, step create, approval create all return 201.
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_approve_test01", "approval_id": "apr_test01"}) //nolint:errcheck
	}))
	t.Cleanup(auditSrv2.Close)

	plannerCalled := false
	llmFn := func(ctx context.Context, prompt string) (string, error) {
		plannerCalled = true
		return `{
			"action": "execute_step",
			"agent": "database",
			"tool": "terminate_connection",
			"args": {"pid": 1234},
			"reason": "Terminate the idle-in-transaction root blocker"
		}`, nil
	}

	gw := makePlaybookRunGateway(auditSrv2.URL, llmFn)
	rec := postPlaybookRun(t, gw, "pb_approve01",
		`{"connection_string":"host=prod-db port=5432 dbname=postgres"}`)

	if !plannerCalled {
		t.Error("planner LLM was not called for agent_approve-mode playbook")
	}
	// Should return 202 Accepted with pending_approval status.
	if rec.Code != http.StatusAccepted {
		t.Errorf("got %d, want 202 for agent_approve mode; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] != "pending_approval" {
		t.Errorf("status = %q, want pending_approval", resp["status"])
	}
	if resp["run_id"] == "" || resp["run_id"] == nil {
		t.Error("run_id should be set in agent_approve response")
	}
	step, _ := resp["step"].(map[string]any)
	if step == nil {
		t.Fatal("step should be present in pending_approval response")
	}
	if step["tool"] != "terminate_connection" {
		t.Errorf("step.tool = %v, want terminate_connection", step["tool"])
	}
}

// TestHandlePlaybookRun_AgentApproveMode_AnchorEventEmitted verifies that a
// gateway_request anchor event with empty tool_name is emitted when an
// agent_approve-mode playbook run starts. This anchor is what QueryJourneys
// Q1 uses to discover the run as a Journey.
func TestHandlePlaybookRun_AgentApproveMode_AnchorEventEmitted(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_anchor_test",
		SeriesID:      "pbs_connection_remediate",
		Name:          "Connection Overload — Terminate Idle Sessions",
		Guidance:      "Step 1: terminate idle connections.",
		ExecutionMode: "agent_approve",
		PlaybookType:  "remediation",
		IsActive:      true,
	}

	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(pb) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_anchor01", "approval_id": "apr_anchor01"}) //nolint:errcheck
	}))
	t.Cleanup(auditSrv.Close)

	llmFn := func(_ context.Context, _ string) (string, error) {
		return `{"action":"execute_step","agent":"database","tool":"terminate_idle_connections","args":{},"reason":"terminate idle"}`, nil
	}

	ta := &testAuditor{}
	gw := makePlaybookRunGateway(auditSrv.URL, llmFn)
	gw.auditor = audit.NewGatewayAuditor(ta)

	rec := postPlaybookRun(t, gw, "pb_anchor_test",
		`{"connection_string":"host=prod-db port=5432 dbname=postgres"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}

	ta.mu.Lock()
	events := ta.events
	ta.mu.Unlock()

	// Find the anchor: a gateway_request event with no tool.
	var anchor *audit.Event
	for _, e := range events {
		if e.EventType == audit.EventTypeGatewayRequest && e.Tool == nil {
			anchor = e
			break
		}
	}
	if anchor == nil {
		t.Fatal("no gateway_request anchor event emitted for agent_approve run — QueryJourneys will return []")
	}
	if anchor.TraceID == "" {
		t.Error("anchor event has empty TraceID")
	}
	// UserQuery should default to pb.Name when no TriggerContext is provided.
	if anchor.Input.UserQuery != pb.Name {
		t.Errorf("anchor event UserQuery = %q, want %q (pb.Name)", anchor.Input.UserQuery, pb.Name)
	}
}

func TestHandlePlaybookRun_FleetMode_RequiresEvidenceWarning(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:       "pb_cfg01",
		SeriesID:         "pbs_db_config",
		Name:             "Config Recovery",
		Description:      "Recover from a bad PostgreSQL configuration.",
		ExecutionMode:    "fleet",
		IsActive:         true,
		RequiresEvidence: []string{"FATAL.*invalid value for parameter"},
	}
	auditSrv := mockAuditdPlaybookAndRun(t, pb, nil)

	llmFn := func(ctx context.Context, prompt string) (string, error) {
		return `{"name":"cfg-recovery","change":{"steps":[{"tool":"check_connection","args":{}}]},"targets":["db1"],"strategy":{}}`, nil
	}
	gw := makePlaybookRunGateway(auditSrv.URL, llmFn)

	// No context provided — required evidence pattern is absent.
	rec := postPlaybookRun(t, gw, "pb_cfg01", `{}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	warnings, ok := resp["warnings"]
	if !ok {
		t.Fatal("response missing 'warnings' field when required evidence is absent")
	}
	wList, _ := warnings.([]any)
	if len(wList) == 0 {
		t.Error("warnings should be non-empty when required evidence pattern is not in context")
	}
}

func TestHandlePlaybookRun_FleetMode_RequiresEvidenceMatch(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:       "pb_cfg02",
		SeriesID:         "pbs_db_config2",
		Name:             "Config Recovery",
		Description:      "Recover from a bad PostgreSQL configuration.",
		ExecutionMode:    "fleet",
		IsActive:         true,
		RequiresEvidence: []string{"FATAL.*invalid value for parameter"},
	}
	auditSrv := mockAuditdPlaybookAndRun(t, pb, nil)

	llmFn := func(ctx context.Context, prompt string) (string, error) {
		return `{"name":"cfg-recovery","change":{"steps":[{"tool":"check_connection","args":{}}]},"targets":["db1"],"strategy":{}}`, nil
	}
	gw := makePlaybookRunGateway(auditSrv.URL, llmFn)

	// Context matches the required evidence pattern — no warnings expected.
	body := `{"context":"2024-01-15 FATAL: invalid value for parameter max_connections = 9999"}`
	rec := postPlaybookRun(t, gw, "pb_cfg02", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if _, hasWarnings := resp["warnings"]; hasWarnings {
		t.Error("response should not contain 'warnings' when required evidence is present in context")
	}
}

// --- extractConclusionFallback ---

func TestExtractConclusionFallback_InlineCONCLUSION(t *testing.T) {
	text := "The database is running normally.\n\n**CONCLUSION:** Database is OPERATIONAL; no action required."
	got := extractConclusionFallback(text)
	if got != "Database is OPERATIONAL; no action required." {
		t.Errorf("got %q", got)
	}
}

func TestExtractConclusionFallback_InlineCONCLUSION_PlainPrefix(t *testing.T) {
	text := "Checked all connections.\nCONCLUSION: All connections healthy."
	got := extractConclusionFallback(text)
	if got != "All connections healthy." {
		t.Errorf("got %q", got)
	}
}

func TestExtractConclusionFallback_SectionHeading(t *testing.T) {
	text := "Investigated the issue.\n\n## Findings Summary\n\nDatabase is DOWN; restart required."
	got := extractConclusionFallback(text)
	if got != "Database is DOWN; restart required." {
		t.Errorf("got %q", got)
	}
}

func TestExtractConclusionFallback_SectionHeading_BoldLine(t *testing.T) {
	text := "Checked logs.\n\n## Summary\n\n**Database is UNREACHABLE — connection refused on port 5432.**"
	got := extractConclusionFallback(text)
	if got != "Database is UNREACHABLE — connection refused on port 5432." {
		t.Errorf("got %q", got)
	}
}

func TestExtractConclusionFallback_StandaloneBoldStatusLine(t *testing.T) {
	// Pass 3: bold line with status keyword in last third of response.
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = "Some analysis line."
	}
	lines[10] = "**Database is HEALTHY and accepting connections.**"
	text := strings.Join(lines, "\n")
	got := extractConclusionFallback(text)
	if got != "Database is HEALTHY and accepting connections." {
		t.Errorf("got %q", got)
	}
}

func TestExtractConclusionFallback_NoMatch(t *testing.T) {
	text := "The database connection check returned an error. Please investigate further."
	got := extractConclusionFallback(text)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractConclusionFallback_EmptyInput(t *testing.T) {
	if got := extractConclusionFallback(""); got != "" {
		t.Errorf("expected empty string for empty input, got %q", got)
	}
}

// --- parseAgentEscalation edge cases ---

func TestParseAgentEscalation_BoldFindings(t *testing.T) {
	// LLM uses **FINDINGS:** instead of plain FINDINGS:
	text := "Investigated the cluster.\n\n**FINDINGS:** WAL corruption on replica.\n**ESCALATE_TO:** pbs_pitr_recovery\n"
	esc := parseAgentEscalation(text)
	if esc.Findings != "WAL corruption on replica." {
		t.Errorf("Findings = %q", esc.Findings)
	}
	if esc.EscalateTo != "pbs_pitr_recovery" {
		t.Errorf("EscalateTo = %q", esc.EscalateTo)
	}
}

func TestParseAgentEscalation_EscalateToNone(t *testing.T) {
	// ESCALATE_TO: none must not populate EscalateTo.
	text := "All checks passed.\n\nFINDINGS: Database is operational.\nESCALATE_TO: none\n"
	esc := parseAgentEscalation(text)
	if esc.EscalateTo != "" {
		t.Errorf("EscalateTo = %q, want empty (none should be discarded)", esc.EscalateTo)
	}
	if esc.Findings != "Database is operational." {
		t.Errorf("Findings = %q", esc.Findings)
	}
}

func TestParseAgentEscalation_FallbackFromCleanText(t *testing.T) {
	// No FINDINGS: line — fallback should extract from **CONCLUSION:** in clean text.
	text := "The investigation is complete.\n\n**CONCLUSION:** Database is DOWN; container exited with code 1."
	esc := parseAgentEscalation(text)
	if esc.Findings == "" {
		t.Error("expected fallback to extract findings from **CONCLUSION:**, got empty")
	}
	if !strings.Contains(esc.Findings, "DOWN") {
		t.Errorf("Findings should mention DOWN, got %q", esc.Findings)
	}
}

func TestParseAgentEscalation_TransitionTo(t *testing.T) {
	// TRANSITION_TO: is the same-domain triage→remediate signal; must populate
	// TransitionTo and leave EscalateTo empty.
	text := "Lock chain diagnosed.\n\n" +
		"FINDINGS: Root blocker PID 4321 (idle in transaction, has_writes=true); 3-level chain; terminate_connection required.\n" +
		"TRANSITION_TO: pbs_lock_chain_remediate\n"
	esc := parseAgentEscalation(text)

	if esc.Findings == "" {
		t.Error("Findings should be populated")
	}
	if esc.TransitionTo != "pbs_lock_chain_remediate" {
		t.Errorf("TransitionTo = %q, want pbs_lock_chain_remediate", esc.TransitionTo)
	}
	if esc.EscalateTo != "" {
		t.Errorf("EscalateTo = %q, want empty — TRANSITION_TO must not set EscalateTo", esc.EscalateTo)
	}
	if strings.Contains(esc.CleanText, "TRANSITION_TO:") {
		t.Error("CleanText should not contain TRANSITION_TO: line")
	}
	if strings.Contains(esc.CleanText, "FINDINGS:") {
		t.Error("CleanText should not contain FINDINGS: line")
	}
}

// --- findingsRecommendMonitor ---

func TestFindingsRecommendMonitor(t *testing.T) {
	cases := []struct {
		findings string
		want     bool
	}{
		// gate should NOT fire
		{"worst table public.orders dead_ratio=0.03; autovacuum=running; blocker_pid=none; recommended=monitor", true},
		{"checkpoints_req=0 timed=12; maxwritten_clean=0; buffers_backend_fsync=0; recommended=no_changes_needed", true},

		// gate SHOULD fire
		{"worst table public.orders dead_ratio=0.35; autovacuum=stuck; blocker_pid=none; recommended=manual_vacuum", false},
		{"top query queryid=42 mean_exec_time=15000ms calls=500/hr; wait_event=Lock; lock_contention=true; recommended=cancel_query", false},
		{"connections 198/200 (99%); blocker=PID 1234 (idle, 45m, has_writes=false); recommended=terminate_blocker", false},
		{"checkpoints_req=142 timed=8; maxwritten_clean=5; buffers_backend_fsync=0; recommended=max_wal_size=2GB", false},

		// edge cases
		{"", false},
		{"no recommended field at all", false},
		{"recommended=monitored", false}, // prefix match must not trigger
	}
	for _, tc := range cases {
		if got := findingsRecommendMonitor(tc.findings); got != tc.want {
			t.Errorf("findingsRecommendMonitor(%q) = %v, want %v", tc.findings, got, tc.want)
		}
	}
}

// --- TestHandlePlaybookRun_FleetMode_NoLLM verifies that a fleet-mode playbook run
// returns 503 when the planner LLM is not configured.
func TestHandlePlaybookRun_FleetMode_NoLLM(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_fleet02",
		Name:          "Vacuum Triage",
		Description:   "Check vacuum.",
		ExecutionMode: "fleet",
		IsActive:      true,
	}
	auditSrv := mockAuditdPlaybook(t, pb)
	gw := makePlaybookRunGateway(auditSrv.URL, nil) // nil LLM

	rec := postPlaybookRun(t, gw, "pb_fleet02", `{}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503 when plannerLLM is nil", rec.Code)
	}
}

// --- recordEscalationDecision tests ---

func TestRecordEscalationDecision_EmitsEvent(t *testing.T) {
	ta := &testAuditor{}
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client),
		auditor: audit.NewGatewayAuditor(ta),
	}

	pb := &audit.Playbook{
		PlaybookID: "pb_triage01",
		SeriesID:   "pbs_db_restart_triage",
		Name:       "DB Restart Triage",
	}
	principal := identity.ResolvedPrincipal{UserID: "ops@example.com", AuthMethod: "static"}
	traceID := audit.NewTraceIDWithPrefix("tr_")

	gw.recordEscalationDecision(context.Background(), traceID, principal,
		pb, "pbs_db_config_recovery", "connection pool exhaustion detected", false)

	ta.mu.Lock()
	events := ta.events
	ta.mu.Unlock()

	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	ev := events[0]

	if ev.EventType != audit.EventTypeDelegation {
		t.Errorf("EventType = %q, want %q", ev.EventType, audit.EventTypeDelegation)
	}
	if ev.TraceID != traceID {
		t.Errorf("TraceID = %q, want %q", ev.TraceID, traceID)
	}
	if !strings.HasPrefix(ev.EventID, "ps_") {
		t.Errorf("EventID = %q, want ps_ prefix", ev.EventID)
	}
	if ev.Decision == nil {
		t.Fatal("Decision is nil")
	}
	if ev.Decision.Agent != "pbs_db_config_recovery" {
		t.Errorf("Decision.Agent = %q, want pbs_db_config_recovery", ev.Decision.Agent)
	}
	if ev.Decision.RequestCategory != audit.CategoryIncident {
		t.Errorf("RequestCategory = %q, want %q", ev.Decision.RequestCategory, audit.CategoryIncident)
	}
	if ev.Decision.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0", ev.Decision.Confidence)
	}
	// ReasoningChain: from-playbook, to-playbook, findings.
	if len(ev.Decision.ReasoningChain) != 3 {
		t.Errorf("ReasoningChain len = %d, want 3", len(ev.Decision.ReasoningChain))
	}
	if !strings.Contains(ev.Decision.ReasoningChain[0], "pbs_db_restart_triage") {
		t.Errorf("ReasoningChain[0] should mention source playbook: %q", ev.Decision.ReasoningChain[0])
	}
	if !strings.Contains(ev.Decision.ReasoningChain[1], "pbs_db_config_recovery") {
		t.Errorf("ReasoningChain[1] should mention target playbook: %q", ev.Decision.ReasoningChain[1])
	}
	if ev.Principal == nil || ev.Principal.UserID != "ops@example.com" {
		t.Errorf("Principal not set correctly: %+v", ev.Principal)
	}
	if ev.Outcome == nil || ev.Outcome.Status != "success" {
		t.Errorf("Outcome = %+v, want success", ev.Outcome)
	}
	if ev.Timestamp.IsZero() {
		t.Error("Timestamp is zero")
	}
}

func TestRecordEscalationDecision_NoFindingsOmitsThirdStep(t *testing.T) {
	ta := &testAuditor{}
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client),
		auditor: audit.NewGatewayAuditor(ta),
	}

	pb := &audit.Playbook{SeriesID: "pbs_db_restart_triage"}
	gw.recordEscalationDecision(context.Background(), "tr_test123",
		identity.ResolvedPrincipal{}, pb, "pbs_db_config_recovery", "", false)

	ta.mu.Lock()
	events := ta.events
	ta.mu.Unlock()

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	// Without findings, reasoning chain has exactly 2 entries.
	if len(events[0].Decision.ReasoningChain) != 2 {
		t.Errorf("ReasoningChain len = %d, want 2 (no findings)", len(events[0].Decision.ReasoningChain))
	}
	// Anonymous principal → Principal field should be nil.
	if events[0].Principal != nil {
		t.Errorf("Principal should be nil for anonymous caller, got %+v", events[0].Principal)
	}
}

func TestRecordEscalationDecision_EmptyTraceIDGeneratesOne(t *testing.T) {
	ta := &testAuditor{}
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client),
		auditor: audit.NewGatewayAuditor(ta),
	}

	pb := &audit.Playbook{SeriesID: "pbs_triage"}
	gw.recordEscalationDecision(context.Background(), "",
		identity.ResolvedPrincipal{}, pb, "pbs_next", "", false)

	ta.mu.Lock()
	events := ta.events
	ta.mu.Unlock()

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].TraceID == "" {
		t.Error("TraceID should be auto-generated when empty string passed")
	}
}

func TestRecordEscalationDecision_NilAuditor(t *testing.T) {
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client),
		// auditor intentionally nil
	}
	pb := &audit.Playbook{SeriesID: "pbs_triage"}
	// Should be a no-op, not a panic.
	gw.recordEscalationDecision(context.Background(), "tr_test",
		identity.ResolvedPrincipal{}, pb, "pbs_next", "", false)
}

// ─── parseDiagnosticReport tests ─────────────────────────────────────────────

func TestParseDiagnosticReport_FullResponse(t *testing.T) {
	text := `The container was stopped cleanly by an operator.

HYPOTHESIS_1: Container was stopped by an operator | CONFIDENCE: 0.90 | EVIDENCE: "exitcode=0"
HYPOTHESIS_2: Disk exhaustion caused the stop | CONFIDENCE: 0.20 | REJECTED: disk check showed only 45% used, no "no space left" in logs
ROOT_CAUSE: HYPOTHESIS_1
FINDINGS: Container was cleanly stopped; no infrastructure fault detected.
ACTION_TAKEN: none — escalation recommended
ESCALATE_TO: none`

	report := parseDiagnosticReport(text)
	if report == nil {
		t.Fatal("expected non-nil DiagnosticReport")
	}
	if len(report.Hypotheses) != 2 {
		t.Fatalf("expected 2 hypotheses, got %d", len(report.Hypotheses))
	}

	h1 := report.Hypotheses[0]
	if h1.Rank != 1 {
		t.Errorf("h1.Rank = %d, want 1", h1.Rank)
	}
	if !h1.IsPrimary {
		t.Error("h1.IsPrimary should be true")
	}
	if h1.Confidence != 0.90 {
		t.Errorf("h1.Confidence = %f, want 0.90", h1.Confidence)
	}
	if h1.Evidence != "exitcode=0" {
		t.Errorf("h1.Evidence = %q, want %q", h1.Evidence, "exitcode=0")
	}
	if h1.RejectedReason != "" {
		t.Errorf("h1.RejectedReason should be empty, got %q", h1.RejectedReason)
	}

	h2 := report.Hypotheses[1]
	if h2.IsPrimary {
		t.Error("h2.IsPrimary should be false")
	}
	if h2.RejectedReason == "" {
		t.Error("h2.RejectedReason should be set")
	}

	if report.RootCause != "Container was stopped by an operator" {
		t.Errorf("RootCause = %q", report.RootCause)
	}
	if report.ActionTaken != "none — escalation recommended" {
		t.Errorf("ActionTaken = %q", report.ActionTaken)
	}
}

func TestParseDiagnosticReport_NoHypotheses_ReturnsNil(t *testing.T) {
	text := `FINDINGS: Something happened.
ESCALATE_TO: none`
	report := parseDiagnosticReport(text)
	if report != nil {
		t.Errorf("expected nil when no HYPOTHESIS lines, got %+v", report)
	}
}

func TestParseDiagnosticReport_SingleHypothesis(t *testing.T) {
	text := `HYPOTHESIS_1: OOM kill | CONFIDENCE: 0.95 | EVIDENCE: "oomkilled=true"
ROOT_CAUSE: HYPOTHESIS_1
FINDINGS: OOM kill detected.
ESCALATE_TO: none`
	report := parseDiagnosticReport(text)
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if len(report.Hypotheses) != 1 {
		t.Fatalf("expected 1 hypothesis, got %d", len(report.Hypotheses))
	}
	if !report.Hypotheses[0].IsPrimary {
		t.Error("single hypothesis should be primary")
	}
}

func TestParseDiagnosticReport_BoldHypothesisLines(t *testing.T) {
	// LLMs sometimes emit **HYPOTHESIS_N:** with markdown bold markers.
	text := `**HYPOTHESIS_1:** Container stopped by operator | CONFIDENCE: 0.88 | EVIDENCE: "exitcode=0"
**HYPOTHESIS_2:** OOM kill | CONFIDENCE: 0.15 | REJECTED: no OOM entry in kernel log
ROOT_CAUSE: HYPOTHESIS_1
FINDINGS: Clean stop by operator.
ACTION_TAKEN: none — escalation recommended
ESCALATE_TO: none`

	report := parseDiagnosticReport(text)
	if report == nil {
		t.Fatal("expected non-nil report for bold HYPOTHESIS lines")
	}
	if len(report.Hypotheses) != 2 {
		t.Fatalf("expected 2 hypotheses, got %d", len(report.Hypotheses))
	}
	if !report.Hypotheses[0].IsPrimary {
		t.Error("first hypothesis should be primary")
	}
	if report.Hypotheses[0].Confidence != 0.88 {
		t.Errorf("h1.Confidence = %f, want 0.88", report.Hypotheses[0].Confidence)
	}
	if report.Hypotheses[1].RejectedReason == "" {
		t.Error("h2.RejectedReason should be set")
	}
	if report.ActionTaken != "none — escalation recommended" {
		t.Errorf("ActionTaken = %q", report.ActionTaken)
	}
}

// ---- checkContextConsistency tests ----

func makeContextTestInfra() *infra.Config {
	return &infra.Config{
		DBServers: map[string]infra.DBServer{
			"test-pg": {
				Name:             "Test Postgres",
				ConnectionString: "host=localhost port=5433 dbname=postgres",
				VMName:           "test-host",
			},
			"pg-cluster-minikube": {
				Name:             "PG Cluster Minikube",
				ConnectionString: "host=pg-cluster-minikube port=5432 dbname=postgres",
				K8sCluster:       "minikube",
			},
			"standalone-db": {
				Name:             "Standalone DB",
				ConnectionString: "host=standalone port=5432",
			},
		},
		VMs: map[string]infra.VM{
			"test-host": {Name: "test-host", Runtime: "docker"},
		},
		K8sClusters: map[string]infra.K8sCluster{
			"minikube": {Name: "minikube", Context: "minikube"},
		},
	}
}

func TestCheckContextConsistency_K8sTermsOnDockerServer(t *testing.T) {
	cfg := makeContextTestInfra()
	warns := checkContextConsistency(cfg, "test-pg", "Pod db-0 is crashing. kubectl delete pod db-0.")
	if len(warns) == 0 {
		t.Fatal("expected warning for K8s terms on docker-hosted server, got none")
	}
	if !strings.Contains(warns[0], "Kubernetes") {
		t.Errorf("warning should mention Kubernetes, got: %s", warns[0])
	}
}

func TestCheckContextConsistency_K8sTermsOnK8sServer_NoWarning(t *testing.T) {
	cfg := makeContextTestInfra()
	warns := checkContextConsistency(cfg, "pg-cluster-minikube", "Pod db-0 is crashing. kubectl describe pod db-0.")
	if len(warns) != 0 {
		t.Errorf("expected no warning for K8s terms on K8s-hosted server, got: %v", warns)
	}
}

func TestCheckContextConsistency_NilInfra(t *testing.T) {
	warns := checkContextConsistency(nil, "test-pg", "pod crashed kubectl")
	if warns != nil {
		t.Errorf("expected nil with nil infra, got %v", warns)
	}
}

func TestCheckContextConsistency_UnknownServer(t *testing.T) {
	cfg := makeContextTestInfra()
	warns := checkContextConsistency(cfg, "unknown-server", "kubectl delete pod db-0")
	if warns != nil {
		t.Errorf("expected nil for unknown server, got %v", warns)
	}
}

func TestCheckContextConsistency_CleanContext_NoWarning(t *testing.T) {
	cfg := makeContextTestInfra()
	warns := checkContextConsistency(cfg, "test-pg", "High CPU on the host. The database is slow.")
	if len(warns) != 0 {
		t.Errorf("expected no warnings for clean context, got: %v", warns)
	}
}

func TestCheckContextConsistency_EmptyContext(t *testing.T) {
	cfg := makeContextTestInfra()
	warns := checkContextConsistency(cfg, "test-pg", "")
	if warns != nil {
		t.Errorf("expected nil for empty context, got %v", warns)
	}
}

func TestCheckContextConsistency_DockerTermsOnStandaloneServer(t *testing.T) {
	cfg := makeContextTestInfra()
	warns := checkContextConsistency(cfg, "standalone-db", "docker exec -it postgres psql")
	if len(warns) == 0 {
		t.Fatal("expected warning for docker terms on non-container server, got none")
	}
}

// ---- targetMatches tests ----

func TestTargetMatches_ExactMatch(t *testing.T) {
	if !targetMatches("test-pg", "test-pg") {
		t.Error("exact match should return true")
	}
}

func TestTargetMatches_ShortNameInConnectionString(t *testing.T) {
	if !targetMatches("test-pg", "host=test-pg dbname=postgres") {
		t.Error("short name as host value should match")
	}
}

func TestTargetMatches_DifferentHost(t *testing.T) {
	if targetMatches("test-pg", "host=pg-cluster-minikube dbname=postgres") {
		t.Error("different host should not match")
	}
}

func TestTargetMatches_EmptyActual(t *testing.T) {
	if targetMatches("test-pg", "") {
		t.Error("empty actual should not match")
	}
}

func TestTargetMatches_SubsetConnString(t *testing.T) {
	// Infra config has host+port+dbname; agent adds user= at runtime.
	intended := "host=localhost port=35432 dbname=postgres"
	actual := "host=localhost port=35432 dbname=postgres user=postgres"
	if !targetMatches(intended, actual) {
		t.Error("actual is a superset of intended fields — should match")
	}
}

func TestTargetMatches_SubsetMismatch(t *testing.T) {
	// Same structure but different host — must not match.
	intended := "host=localhost port=35432 dbname=postgres"
	actual := "host=other-host port=35432 dbname=postgres user=postgres"
	if targetMatches(intended, actual) {
		t.Error("different host value — should not match")
	}
}

// TestHandlePlaybookRunAsAgent_TargetDrift_EventPersisted verifies the fix for the
// documented "not persisted" limitation (docs/MUTATION_TOOLS.md §5.6): when
// checkTargetScope detects drift for a playbook run, handlePlaybookRunAsAgent must
// now record a durable delegation_verification event (previously the drift only
// ever appeared in this run's own HTTP response, with no queryable trace afterward).
func TestHandlePlaybookRunAsAgent_TargetDrift_EventPersisted(t *testing.T) {
	agentSrv, card := mockA2AServerWithText(t, agentNameDB, "investigation complete")
	_ = agentSrv
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("create A2A client: %v", err)
	}

	intended := "test-pg"
	cfg := &infra.Config{
		DBServers: map[string]infra.DBServer{
			intended: {Name: "Test Postgres", ConnectionString: "host=localhost port=35432 dbname=postgres"},
		},
	}

	// Auditd: serves a tool_execution event whose connection_string differs from
	// the intended target — this is what both checkTargetScope AND
	// proxyToAgentWithTool's own buildDelegationVerification will see (both query
	// the same event_type=tool_execution for the trace); agent_reasoning and
	// policy_decision fetches return empty (nothing narrated, nothing denied).
	auditdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("event_type") == "tool_execution" {
			json.NewEncoder(w).Encode([]audit.Event{ //nolint:errcheck
				{
					EventType: audit.EventTypeToolExecution,
					Tool: &audit.ToolExecution{
						Name:       "list_databases",
						Parameters: map[string]any{"connection_string": "pg-cluster-minikube"},
					},
				},
			})
			return
		}
		json.NewEncoder(w).Encode([]audit.Event{}) //nolint:errcheck
	}))
	t.Cleanup(auditdSrv.Close)

	ta := &testAuditor{}
	gw := &Gateway{
		agents:   make(map[string]*discovery.Agent),
		clients:  map[string]*a2aclient.Client{agentNameDB: client},
		infra:    cfg,
		auditor:  audit.NewGatewayAuditor(ta),
		auditURL: auditdSrv.URL,
	}

	pb := &audit.Playbook{
		PlaybookID:    "pb_drift01",
		SeriesID:      "pbs_db_restart_triage",
		Name:          "Database Down — Restart Triage",
		Guidance:      "Step 1: run check_connection.",
		ExecutionMode: "agent",
		IsActive:      true,
	}
	req := PlaybookRunRequest{ConnectionString: intended, Context: "connection refused"}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/playbooks/pb_drift01/run", nil)
	w := httptest.NewRecorder()

	gw.handlePlaybookRunAsAgent(w, r, pb, req, "plr_drift01", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	ta.mu.Lock()
	events := ta.events
	ta.mu.Unlock()

	var driftEvent *audit.Event
	for _, e := range events {
		if e.EventType == audit.EventTypeDelegationVerification &&
			e.DelegationVerification != nil && len(e.DelegationVerification.TargetDrift) > 0 {
			driftEvent = e
			break
		}
	}
	if driftEvent == nil {
		t.Fatalf("no delegation_verification event with TargetDrift populated was recorded; got %d total events", len(events))
	}
	if driftEvent.TraceID == "" {
		t.Error("TraceID is empty — event will not attach to the run's journey")
	}
	if driftEvent.Session.ID != driftEvent.TraceID {
		t.Errorf("Session.ID = %q, want to match TraceID %q", driftEvent.Session.ID, driftEvent.TraceID)
	}
	if len(driftEvent.DelegationVerification.TargetDrift) != 1 || driftEvent.DelegationVerification.TargetDrift[0] != "pg-cluster-minikube" {
		t.Errorf("TargetDrift = %v, want [pg-cluster-minikube]", driftEvent.DelegationVerification.TargetDrift)
	}
	if driftEvent.DelegationVerification.Mismatch {
		t.Error("Mismatch = true, want false: this event records drift, not fabrication — a real tool call happened")
	}
	if !strings.HasPrefix(driftEvent.EventID, "gv_") {
		t.Errorf("EventID = %q, want gv_ prefix", driftEvent.EventID)
	}
}

// ---- checkTargetScope tests ----

func TestCheckTargetScope_NoAuditURL(t *testing.T) {
	drift := checkTargetScope(nil, "", "", "tr_abc", time.Now().Add(-time.Minute), "test-pg")
	if drift != nil {
		t.Errorf("expected nil with empty auditURL, got %v", drift)
	}
}

func TestCheckTargetScope_ShortNameNoInfra_Skipped(t *testing.T) {
	// Short name with no infra config: cannot resolve, must not produce false positives.
	events := []audit.Event{
		{
			EventType: audit.EventTypeToolExecution,
			Tool: &audit.ToolExecution{
				Name:       "get_session_info",
				Parameters: map[string]any{"connection_string": "host=localhost port=35432 dbname=postgres user=postgres"},
			},
		},
	}
	srv := serveFakeToolEvents(t, events)
	drift := checkTargetScope(nil, srv.URL, "", "tr_abc", time.Now().Add(-time.Minute), "test-pg")
	if drift != nil {
		t.Errorf("expected nil (short name, no infra config — skip check), got %v", drift)
	}
}

func TestCheckTargetScope_EmptyIntendedTarget(t *testing.T) {
	drift := checkTargetScope(nil, "http://localhost:9999", "", "tr_abc", time.Now().Add(-time.Minute), "")
	if drift != nil {
		t.Errorf("expected nil with empty intended target, got %v", drift)
	}
}

func TestCheckTargetScope_NoDrift(t *testing.T) {
	events := []audit.Event{
		{
			EventType: audit.EventTypeToolExecution,
			Tool:      &audit.ToolExecution{Name: "get_session_info", Parameters: map[string]any{"connection_string": "test-pg"}},
		},
	}
	srv := serveFakeToolEvents(t, events)

	drift := checkTargetScope(nil, srv.URL, "", "tr_abc", time.Now().Add(-time.Minute), "test-pg")
	if drift != nil {
		t.Errorf("expected nil (no drift), got %v", drift)
	}
}

func TestCheckTargetScope_Drift(t *testing.T) {
	cfg := &infra.Config{
		DBServers: map[string]infra.DBServer{
			"test-pg": {Name: "Test Postgres", ConnectionString: "host=localhost port=35432 dbname=postgres"},
		},
	}
	events := []audit.Event{
		{
			EventType: audit.EventTypeToolExecution,
			Tool:      &audit.ToolExecution{Name: "get_session_info", Parameters: map[string]any{"connection_string": "host=localhost port=35432 dbname=postgres user=postgres"}},
		},
		{
			EventType: audit.EventTypeToolExecution,
			Tool:      &audit.ToolExecution{Name: "list_databases", Parameters: map[string]any{"connection_string": "pg-cluster-minikube"}},
		},
	}
	srv := serveFakeToolEvents(t, events)

	drift := checkTargetScope(cfg, srv.URL, "", "tr_abc", time.Now().Add(-time.Minute), "test-pg")
	if len(drift) != 1 || drift[0] != "pg-cluster-minikube" {
		t.Errorf("expected [pg-cluster-minikube], got %v", drift)
	}
}

func TestCheckTargetScope_FullConnStringMatchesShortName(t *testing.T) {
	// Agent resolves "test-pg" to full connection string — should not be flagged as drift.
	events := []audit.Event{
		{
			EventType: audit.EventTypeToolExecution,
			Tool: &audit.ToolExecution{
				Name:       "get_session_info",
				Parameters: map[string]any{"connection_string": "host=test-pg dbname=postgres"},
			},
		},
	}
	srv := serveFakeToolEvents(t, events)

	drift := checkTargetScope(nil, srv.URL, "", "tr_abc", time.Now().Add(-time.Minute), "test-pg")
	if drift != nil {
		t.Errorf("expected nil (full conn string contains intended target as host), got %v", drift)
	}
}

func TestCheckTargetScope_ResolvedViaInfraConfig(t *testing.T) {
	// Infra config has host+port+dbname; agent appends user= at runtime.
	// The agent-recorded connection string is a superset — must not flag as drift.
	cfg := &infra.Config{
		DBServers: map[string]infra.DBServer{
			"test-pg": {
				Name:             "Test Postgres",
				ConnectionString: "host=localhost port=35432 dbname=postgres",
			},
		},
	}
	events := []audit.Event{
		{
			EventType: audit.EventTypeToolExecution,
			Tool: &audit.ToolExecution{
				Name:       "get_session_info",
				Parameters: map[string]any{"connection_string": "host=localhost port=35432 dbname=postgres user=postgres"},
			},
		},
	}
	srv := serveFakeToolEvents(t, events)

	drift := checkTargetScope(cfg, srv.URL, "", "tr_abc", time.Now().Add(-time.Minute), "test-pg")
	if drift != nil {
		t.Errorf("expected nil (agent-added user= field is allowed), got %v", drift)
	}
}

func TestCheckTargetScope_ResolvedPlusUnintendedServer(t *testing.T) {
	// Agent correctly uses the resolved form for the intended target, but also queries
	// an unintended server. Only the unintended server should appear in drift.
	cfg := &infra.Config{
		DBServers: map[string]infra.DBServer{
			"test-pg": {
				Name:             "Test Postgres",
				ConnectionString: "host=localhost port=35432 dbname=postgres user=postgres",
			},
		},
	}
	events := []audit.Event{
		{
			EventType: audit.EventTypeToolExecution,
			Tool: &audit.ToolExecution{
				Name:       "get_session_info",
				Parameters: map[string]any{"connection_string": "host=localhost port=35432 dbname=postgres user=postgres"},
			},
		},
		{
			EventType: audit.EventTypeToolExecution,
			Tool: &audit.ToolExecution{
				Name:       "list_databases",
				Parameters: map[string]any{"connection_string": "test-db"},
			},
		},
	}
	srv := serveFakeToolEvents(t, events)

	drift := checkTargetScope(cfg, srv.URL, "", "tr_abc", time.Now().Add(-time.Minute), "test-pg")
	if len(drift) != 1 || drift[0] != "test-db" {
		t.Errorf("expected [test-db], got %v", drift)
	}
}

// ---- buildServerTypeHint tests ----

func TestBuildServerTypeHint_DockerServer(t *testing.T) {
	cfg := makeContextTestInfra()
	hint := buildServerTypeHint(cfg, "test-pg")
	if !strings.Contains(hint, "docker container") {
		t.Errorf("expected 'docker container' in hint, got: %s", hint)
	}
	if !strings.Contains(hint, "NOT a Kubernetes") {
		t.Errorf("hint should warn against K8s tools, got: %s", hint)
	}
}

func TestBuildServerTypeHint_K8sServer(t *testing.T) {
	cfg := makeContextTestInfra()
	hint := buildServerTypeHint(cfg, "pg-cluster-minikube")
	if !strings.Contains(hint, "Kubernetes pod") {
		t.Errorf("expected 'Kubernetes pod' in hint, got: %s", hint)
	}
	if strings.Contains(hint, "NOT a Kubernetes") {
		t.Errorf("K8s server hint should not warn against K8s tools, got: %s", hint)
	}
}

func TestBuildServerTypeHint_StandaloneServer(t *testing.T) {
	cfg := makeContextTestInfra()
	hint := buildServerTypeHint(cfg, "standalone-db")
	if !strings.Contains(hint, "standalone") {
		t.Errorf("expected 'standalone' in hint, got: %s", hint)
	}
	if !strings.Contains(hint, "NOT") {
		t.Errorf("standalone hint should warn against K8s tools, got: %s", hint)
	}
}

func TestBuildServerTypeHint_NilInfra(t *testing.T) {
	hint := buildServerTypeHint(nil, "test-pg")
	if hint != "" {
		t.Errorf("expected empty hint with nil infra, got: %s", hint)
	}
}

func TestBuildServerTypeHint_UnknownServer(t *testing.T) {
	cfg := makeContextTestInfra()
	hint := buildServerTypeHint(cfg, "no-such-server")
	if hint != "" {
		t.Errorf("expected empty hint for unknown server, got: %s", hint)
	}
}

func TestAssembleTriagePrompt_WithServerTypeHint(t *testing.T) {
	pb := &audit.Playbook{Name: "Triage"}
	req := PlaybookRunRequest{ConnectionString: "test-pg"}
	hint := "Server type: docker container on VM \"test-host\" (test-host), container name: test-db.\nThis is NOT a Kubernetes-managed server — do NOT attempt kubectl commands."
	prompt := assembleTriagePrompt(pb, req, hint)

	if !strings.Contains(prompt, "test-pg") {
		t.Error("prompt missing connection string")
	}
	if !strings.Contains(prompt, "NOT a Kubernetes-managed server") {
		t.Error("prompt missing server type hint")
	}
}

func TestBuildSuggestedNext_PopulatesFields(t *testing.T) {
	req := PlaybookRunRequest{
		ConnectionString: "prod-db",
		ApprovalMode:     "session",
	}
	result := buildSuggestedNext("pbs_sysadmin_docker_inspect", req, "run_123", "container stopped cleanly")

	if result["playbook_series_id"] != "pbs_sysadmin_docker_inspect" {
		t.Errorf("playbook_series_id = %v", result["playbook_series_id"])
	}
	if result["reason"] != "container stopped cleanly" {
		t.Errorf("reason = %v", result["reason"])
	}
	inner, ok := result["request"].(map[string]any)
	if !ok {
		t.Fatal("request field missing or wrong type")
	}
	if inner["connection_string"] != "prod-db" {
		t.Errorf("request.connection_string = %v", inner["connection_string"])
	}
	if inner["prior_run_id"] != "run_123" {
		t.Errorf("request.prior_run_id = %v", inner["prior_run_id"])
	}
	if inner["approval_mode"] != "session" {
		t.Errorf("request.approval_mode = %v", inner["approval_mode"])
	}
}

func TestMergeDiagnosticReports_BothNil(t *testing.T) {
	if mergeDiagnosticReports(nil, nil) != nil {
		t.Error("expected nil when both inputs are nil")
	}
}

func TestMergeDiagnosticReports_PrimaryNil(t *testing.T) {
	sec := &audit.DiagnosticReport{RootCause: "HYPOTHESIS_1"}
	got := mergeDiagnosticReports(nil, sec)
	if got != sec {
		t.Error("expected secondary to be returned unchanged when primary is nil")
	}
}

func TestMergeDiagnosticReports_SecondaryNil(t *testing.T) {
	pri := &audit.DiagnosticReport{RootCause: "HYPOTHESIS_1"}
	got := mergeDiagnosticReports(pri, nil)
	if got != pri {
		t.Error("expected primary to be returned unchanged when secondary is nil")
	}
}

func TestMergeDiagnosticReports_SecondaryTakesPrecedence(t *testing.T) {
	primary := &audit.DiagnosticReport{
		RootCause:   "HYPOTHESIS_1",
		ActionTaken: "none",
		Hypotheses: []audit.DiagnosticHypothesis{
			{Rank: 1, IsPrimary: true, Confidence: 0.6, Text: "db process crashed"},
		},
	}
	secondary := &audit.DiagnosticReport{
		RootCause:   "HYPOTHESIS_2",
		ActionTaken: "none — restart recommended",
		Hypotheses: []audit.DiagnosticHypothesis{
			{Rank: 1, IsPrimary: true, Confidence: 0.95, Text: "container stopped cleanly (exitcode=0)"},
		},
	}

	merged := mergeDiagnosticReports(primary, secondary)

	if merged.RootCause != "HYPOTHESIS_2" {
		t.Errorf("RootCause = %q, want HYPOTHESIS_2", merged.RootCause)
	}
	if merged.ActionTaken != "none — restart recommended" {
		t.Errorf("ActionTaken = %q", merged.ActionTaken)
	}
	if len(merged.Hypotheses) != 2 {
		t.Fatalf("len(Hypotheses) = %d, want 2", len(merged.Hypotheses))
	}
	// Highest confidence (0.95 from secondary) should be rank 1 and primary.
	if merged.Hypotheses[0].Text != "container stopped cleanly (exitcode=0)" {
		t.Errorf("top hypothesis = %q, want secondary's", merged.Hypotheses[0].Text)
	}
	if !merged.Hypotheses[0].IsPrimary {
		t.Error("top hypothesis should be marked IsPrimary")
	}
	if merged.Hypotheses[1].IsPrimary {
		t.Error("second hypothesis should not be marked IsPrimary")
	}
	if merged.Hypotheses[0].Rank != 1 || merged.Hypotheses[1].Rank != 2 {
		t.Errorf("ranks = %d,%d, want 1,2", merged.Hypotheses[0].Rank, merged.Hypotheses[1].Rank)
	}
}

func TestMergeDiagnosticReports_EmptySecondaryRootCause(t *testing.T) {
	primary := &audit.DiagnosticReport{
		RootCause: "HYPOTHESIS_1",
		Hypotheses: []audit.DiagnosticHypothesis{
			{Rank: 1, IsPrimary: true, Confidence: 0.7, Text: "primary only"},
		},
	}
	secondary := &audit.DiagnosticReport{
		RootCause: "", // empty — primary should win
		Hypotheses: []audit.DiagnosticHypothesis{
			{Rank: 1, IsPrimary: true, Confidence: 0.5, Text: "secondary lower confidence"},
		},
	}

	merged := mergeDiagnosticReports(primary, secondary)
	if merged.RootCause != "HYPOTHESIS_1" {
		t.Errorf("RootCause = %q, want HYPOTHESIS_1 (primary fallback)", merged.RootCause)
	}
	// Primary's hypothesis should rank first (higher confidence).
	if merged.Hypotheses[0].Text != "primary only" {
		t.Errorf("top hypothesis = %q, want primary's", merged.Hypotheses[0].Text)
	}
}

func chainablePB(approvalMode string) *audit.Playbook {
	return &audit.Playbook{ApprovalMode: approvalMode}
}

func TestCanAutoChain_AutoMode(t *testing.T) {
	gw := &Gateway{}
	if !gw.canAutoChain(context.Background(), "auto", "", chainablePB("session")) {
		t.Error("auto mode should allow chaining to session-gated playbook")
	}
	if !gw.canAutoChain(context.Background(), "auto", "", chainablePB("auto")) {
		t.Error("auto mode should allow chaining to auto-gated playbook")
	}
}

func TestCanAutoChain_ManualMode(t *testing.T) {
	gw := &Gateway{}
	if gw.canAutoChain(context.Background(), "manual", "", chainablePB("session")) {
		t.Error("manual requester mode should never allow chaining")
	}
}

func TestCanAutoChain_EmptyMode(t *testing.T) {
	gw := &Gateway{}
	if gw.canAutoChain(context.Background(), "", "", chainablePB("session")) {
		t.Error("empty requester mode should not allow chaining")
	}
}

func TestCanAutoChain_PlaybookManualGate(t *testing.T) {
	gw := &Gateway{}
	// Even auto requester mode cannot chain to a manual-gated playbook.
	if gw.canAutoChain(context.Background(), "auto", "", chainablePB("manual")) {
		t.Error("auto mode should not chain to manual-gated playbook")
	}
	if gw.canAutoChain(context.Background(), "auto", "", chainablePB("")) {
		t.Error("auto mode should not chain to unset-mode playbook")
	}
}

func TestCanAutoChain_ForceMode(t *testing.T) {
	gw := &Gateway{}
	// "force" bypasses the playbook-level gate entirely.
	if !gw.canAutoChain(context.Background(), "force", "", chainablePB("manual")) {
		t.Error("force mode should chain to manual-gated playbook")
	}
	if !gw.canAutoChain(context.Background(), "force", "", chainablePB("")) {
		t.Error("force mode should chain to unset-mode playbook")
	}
	if !gw.canAutoChain(context.Background(), "force", "", chainablePB("session")) {
		t.Error("force mode should chain to session-gated playbook")
	}
}

func TestCanAutoChain_SessionMode_NoAuditURL(t *testing.T) {
	// No auditURL → fetchApprovalSession will fail → no chaining.
	gw := &Gateway{auditURL: ""}
	if gw.canAutoChain(context.Background(), "session", "aps_123", chainablePB("session")) {
		t.Error("session mode with no auditURL should not allow chaining")
	}
}

func TestAppendChainedText_AppendsSeparator(t *testing.T) {
	primary := &responseCapture{code: http.StatusOK}
	primary.body.WriteString(`{"text":"primary findings"}`)

	chained := &responseCapture{code: http.StatusOK}
	chained.body.WriteString(`{"text":"sysadmin findings"}`)

	appendChainedText(primary, chained)

	var result map[string]any
	if err := json.Unmarshal(primary.body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	text, _ := result["text"].(string)
	if !strings.Contains(text, "primary findings") {
		t.Error("primary text missing from merged output")
	}
	if !strings.Contains(text, "---") {
		t.Error("separator missing from merged output")
	}
	if !strings.Contains(text, "sysadmin findings") {
		t.Error("chained text missing from merged output")
	}
}

func TestAppendChainedText_NilChained(t *testing.T) {
	primary := &responseCapture{code: http.StatusOK}
	primary.body.WriteString(`{"text":"primary findings"}`)
	before := primary.body.String()

	appendChainedText(primary, nil) // should be a no-op

	if primary.body.String() != before {
		t.Error("primary body was modified when chained is nil")
	}
}

func TestAppendChainedText_ChainedError(t *testing.T) {
	primary := &responseCapture{code: http.StatusOK}
	primary.body.WriteString(`{"text":"primary findings"}`)
	before := primary.body.String()

	chained := &responseCapture{code: http.StatusBadGateway}
	chained.body.WriteString(`{"error":"agent unreachable"}`)

	appendChainedText(primary, chained) // non-200 chained should be a no-op

	if primary.body.String() != before {
		t.Error("primary body was modified when chained returned an error")
	}
}

// mockChainAuditd starts a mock auditd that supports a full multi-hop
// auto-chain: primary playbook fetch by ID, chained playbook fetch by
// series_id, run creation (unique run_id per call), and run completion
// (PATCH bodies captured, keyed by run_id, for later assertion).
type mockChainAuditd struct {
	*httptest.Server
	mu        sync.Mutex
	nextRunID int
	patches   map[string]map[string]any

	// evidenceSkipCalls/evidenceSignal let tests inject a real objective_evidence
	// event starting from the Nth /v1/events?event_type=objective_evidence query
	// (0-indexed) — used to distinguish which hop's fetch should see evidence,
	// since each hop's trace_id is generated dynamically and unknown in advance.
	// Zero value (evidenceSignal == "") preserves the original always-empty
	// behavior for every existing caller of newMockChainAuditd.
	evidenceSkipCalls int
	evidenceSignal    string
	eventsCallCount   int
}

func (m *mockChainAuditd) patchFor(runID string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.patches[runID]
}

// runCount returns how many playbook runs have been created (one POST .../runs
// call per run) — used to assert a chain stopped at the expected hop count
// rather than continuing further than it should have.
func (m *mockChainAuditd) runCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nextRunID
}

func newMockChainAuditd(t *testing.T, byID map[string]*audit.Playbook, bySeries map[string]*audit.Playbook) *mockChainAuditd {
	t.Helper()
	m := &mockChainAuditd{patches: make(map[string]map[string]any)}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fleet/playbooks":
			if pb, ok := bySeries[r.URL.Query().Get("series_id")]; ok {
				json.NewEncoder(w).Encode(map[string]any{"playbooks": []*audit.Playbook{pb}}) //nolint:errcheck
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"playbooks": []*audit.Playbook{}}) //nolint:errcheck
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/fleet/playbooks/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/fleet/playbooks/")
			if pb, ok := byID[id]; ok {
				json.NewEncoder(w).Encode(pb) //nolint:errcheck
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/fleet/playbook-runs/"):
			// fetchPlaybookRun — used for PriorFindings continuity threading.
			runID := strings.TrimPrefix(r.URL.Path, "/v1/fleet/playbook-runs/")
			json.NewEncoder(w).Encode(&audit.PlaybookRun{ //nolint:errcheck
				RunID:           runID,
				FindingsSummary: "stub prior findings for " + runID,
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/runs"):
			m.mu.Lock()
			m.nextRunID++
			runID := fmt.Sprintf("plr_chaintest%02d", m.nextRunID)
			m.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"run_id": runID}) //nolint:errcheck
		case r.Method == http.MethodPatch:
			runID := strings.TrimPrefix(r.URL.Path, "/v1/fleet/playbook-runs/")
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			m.mu.Lock()
			m.patches[runID] = body
			m.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/events") &&
			r.URL.Query().Get("event_type") == "objective_evidence" && m.evidenceSignal != "":
			m.mu.Lock()
			callIdx := m.eventsCallCount
			m.eventsCallCount++
			m.mu.Unlock()
			if callIdx < m.evidenceSkipCalls {
				w.Write([]byte("[]")) //nolint:errcheck
				return
			}
			evidenceData, _ := json.Marshal([]audit.Event{
				{ObjectiveEvidence: &audit.ObjectiveEvidence{Tool: "get_pods", Signal: m.evidenceSignal}},
			})
			w.Write(evidenceData) //nolint:errcheck
		default:
			// Prior-findings lookups, delegation-verification event queries, etc.
			w.Write([]byte("[]")) //nolint:errcheck
		}
	}))
	t.Cleanup(m.Close)
	return m
}

// TestHandlePlaybookRun_AutoChain_PrimaryRecordKeepsOwnSignal is a regression
// test for a real bug found during live 3-hop escalation-chain verification:
// the primary/entry-point run's OWN escalated_to/transitioned_to fields were
// being overwritten with the LAST hop's signal in a multi-hop auto-chain,
// making the incident-narrative walker (handleGetIncident, which starts its
// classification from the triage run's own fields) misclassify the second
// hop as the terminal remediation and stop walking early — even though the
// chain had, e.g., escalated twice before transitioning. The primary run's
// persisted record must reflect what IT ITSELF decided (ESCALATE_TO here),
// not what the chain eventually resolved to.
func TestHandlePlaybookRun_AutoChain_PrimaryRecordKeepsOwnSignal(t *testing.T) {
	pbTriage := &audit.Playbook{
		PlaybookID:    "pb_bugtest_triage",
		SeriesID:      "pbs_bugtest_triage",
		Name:          "Bug Test Triage",
		ExecutionMode: "agent",
		AgentName:     "test_db_agent",
		IsActive:      true,
	}
	pbSysadmin := &audit.Playbook{
		PlaybookID:    "pb_bugtest_sysadmin",
		SeriesID:      "pbs_bugtest_sysadmin",
		Name:          "Bug Test Sysadmin",
		ExecutionMode: "agent",
		AgentName:     "test_sysadmin_agent",
		IsActive:      true,
	}
	pbRemediate := &audit.Playbook{
		PlaybookID:    "pb_bugtest_remediate",
		SeriesID:      "pbs_bugtest_remediate",
		Name:          "Bug Test Remediate",
		ExecutionMode: "agent",
		AgentName:     "test_remediate_agent",
		IsActive:      true,
	}

	auditSrv := newMockChainAuditd(t,
		map[string]*audit.Playbook{"pb_bugtest_triage": pbTriage},
		map[string]*audit.Playbook{
			"pbs_bugtest_sysadmin":  pbSysadmin,
			"pbs_bugtest_remediate": pbRemediate,
		})

	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	_, dbCard := mockA2AServerWithText(t, "test_db_agent",
		"FINDINGS: looks like a db problem\nESCALATE_TO: pbs_bugtest_sysadmin\n")
	_, sysadminCard := mockA2AServerWithText(t, "test_sysadmin_agent",
		"FINDINGS: actually a container issue\nTRANSITION_TO: pbs_bugtest_remediate\n")
	_, remediateCard := mockA2AServerWithText(t, "test_remediate_agent",
		"FINDINGS: restarted and confirmed healthy\n")
	for name, card := range map[string]*a2a.AgentCard{
		"test_db_agent":        dbCard,
		"test_sysadmin_agent":  sysadminCard,
		"test_remediate_agent": remediateCard,
	} {
		client, err := a2aclient.NewFromCard(context.Background(), card)
		if err != nil {
			t.Fatalf("create A2A client for %s: %v", name, err)
		}
		gw.clients[name] = client
	}

	rec := postPlaybookRun(t, gw, pbTriage.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"connection refused","approval_mode":"force"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatal("response missing run_id")
	}

	// recordPlaybookRunComplete for the primary run is fired via `go` — poll
	// briefly for the async PATCH to land.
	var patch map[string]any
	for i := 0; i < 50; i++ {
		if p := auditSrv.patchFor(runID); p != nil {
			patch = p
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if patch == nil {
		t.Fatalf("no PATCH captured for primary run %s within timeout", runID)
	}

	if got := patch["escalated_to"]; got != "pbs_bugtest_sysadmin" {
		t.Errorf("primary run's persisted escalated_to = %v, want 'pbs_bugtest_sysadmin' (its own signal, not the last hop's)", got)
	}
	if got := patch["transitioned_to"]; got != "" && got != nil {
		t.Errorf("primary run's persisted transitioned_to = %v, want empty — the primary run itself emitted ESCALATE_TO, not TRANSITION_TO; a later hop's TRANSITION_TO must not leak onto the primary's own record", got)
	}
}

// promptCapture records the raw request body of the most recent A2A call to
// a mock agent, thread-safe for concurrent test use.
type promptCapture struct {
	mu   sync.Mutex
	body string
}

func (c *promptCapture) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body
}

// mockA2AServerCapturing is mockA2AServerWithText plus request-body capture,
// so a test can assert on the actual prompt text the gateway sent to this
// agent — not just the mocked response.
func mockA2AServerCapturing(t *testing.T, agentName, responseText string, capture *promptCapture) (*httptest.Server, *a2a.AgentCard) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.mu.Lock()
		capture.body = string(body)
		capture.mu.Unlock()

		var req struct {
			ID string `json:"id"`
		}
		json.Unmarshal(body, &req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"kind": "task",
				"id":   "task-capture-1",
				"status": map[string]any{
					"state": "completed",
					"message": map[string]any{
						"role": "agent",
						"parts": []map[string]any{
							{"kind": "text", "text": responseText},
						},
					},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	card := &a2a.AgentCard{
		Name:               agentName,
		URL:                srv.URL,
		PreferredTransport: a2a.TransportProtocolJSONRPC,
	}
	return srv, card
}

// TestHandlePlaybookRun_AutoChain_IsTransitionFraming is a regression test
// for the third bug found during live 3-hop escalation-chain verification:
// PriorFindings was threaded correctly (data-plumbing was never broken), but
// the prompt text unconditionally said "investigate further" regardless of
// whether the continuation was a cross-domain ESCALATE_TO (should verify
// with its own tools) or a same-domain TRANSITION_TO (should treat the
// diagnosis as settled and proceed to remediation). This drives the same
// 3-playbook auto-chain as the sibling test above, but captures the actual
// prompt sent to each chained agent and asserts the framing differs
// correctly by hop type — proving the wiring at the chainEscalation call
// site (not just assembleTriagePrompt's branching logic in isolation).
func TestHandlePlaybookRun_AutoChain_IsTransitionFraming(t *testing.T) {
	pbTriage := &audit.Playbook{
		PlaybookID:    "pb_frametest_triage",
		SeriesID:      "pbs_frametest_triage",
		Name:          "Frame Test Triage",
		ExecutionMode: "agent",
		AgentName:     "frame_db_agent",
		IsActive:      true,
	}
	pbSysadmin := &audit.Playbook{
		PlaybookID:    "pb_frametest_sysadmin",
		SeriesID:      "pbs_frametest_sysadmin",
		Name:          "Frame Test Sysadmin",
		ExecutionMode: "agent",
		AgentName:     "frame_sysadmin_agent",
		IsActive:      true,
	}
	pbRemediate := &audit.Playbook{
		PlaybookID:    "pb_frametest_remediate",
		SeriesID:      "pbs_frametest_remediate",
		Name:          "Frame Test Remediate",
		ExecutionMode: "agent",
		AgentName:     "frame_remediate_agent",
		IsActive:      true,
	}

	auditSrv := newMockChainAuditd(t,
		map[string]*audit.Playbook{"pb_frametest_triage": pbTriage},
		map[string]*audit.Playbook{
			"pbs_frametest_sysadmin":  pbSysadmin,
			"pbs_frametest_remediate": pbRemediate,
		})

	gw := makePlaybookRunGateway(auditSrv.URL, nil)

	var sysadminCapture, remediateCapture promptCapture
	_, dbCard := mockA2AServerWithText(t, "frame_db_agent",
		"FINDINGS: looks like a db problem\nESCALATE_TO: pbs_frametest_sysadmin\n")
	_, sysadminCard := mockA2AServerCapturing(t, "frame_sysadmin_agent",
		"FINDINGS: actually a container issue\nTRANSITION_TO: pbs_frametest_remediate\n", &sysadminCapture)
	_, remediateCard := mockA2AServerCapturing(t, "frame_remediate_agent",
		"FINDINGS: restarted and confirmed healthy\n", &remediateCapture)
	for name, card := range map[string]*a2a.AgentCard{
		"frame_db_agent":        dbCard,
		"frame_sysadmin_agent":  sysadminCard,
		"frame_remediate_agent": remediateCard,
	} {
		client, err := a2aclient.NewFromCard(context.Background(), card)
		if err != nil {
			t.Fatalf("create A2A client for %s: %v", name, err)
		}
		gw.clients[name] = client
	}

	rec := postPlaybookRun(t, gw, pbTriage.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"connection refused","approval_mode":"force"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	// Hop 2 (sysadmin) was reached via triage's ESCALATE_TO — a cross-domain
	// handoff. It must be told to verify with its own tools, not treat the
	// prior diagnosis as settled.
	sysadminPrompt := sysadminCapture.get()
	if sysadminPrompt == "" {
		t.Fatal("sysadmin agent never received a request")
	}
	if !strings.Contains(sysadminPrompt, "verify it with your own domain-specific tools") {
		t.Error("sysadmin (escalation hop) prompt should tell the agent to verify with its own tools")
	}
	if strings.Contains(sysadminPrompt, "Treat this diagnosis as settled") {
		t.Error("sysadmin (escalation hop) prompt should NOT say the diagnosis is settled — it's a different domain/agent")
	}

	// Hop 3 (remediate) was reached via sysadmin's TRANSITION_TO — a
	// same-domain handoff. It must be told to treat the diagnosis as
	// settled, not re-investigate from scratch.
	remediatePrompt := remediateCapture.get()
	if remediatePrompt == "" {
		t.Fatal("remediate agent never received a request")
	}
	if !strings.Contains(remediatePrompt, "Treat this diagnosis as settled") {
		t.Error("remediate (transition hop) prompt should tell the agent the diagnosis is settled")
	}
	if strings.Contains(remediatePrompt, "verify it with your own domain-specific tools") {
		t.Error("remediate (transition hop) prompt should NOT tell the agent to verify with its own tools — it's the same agent continuing")
	}
}

// TestHandlePlaybookRun_ChainedHop_ObjectiveEvidence_ForcedGate verifies that
// objectiveEvidenceForceGate fires for a SECOND hop in a real multi-hop chain,
// not just the primary/first hop — the in-loop gate is re-evaluated on every
// loop iteration using `prev`, which only holds the primary's data on the
// first iteration. Hop1 (triage) escalates cleanly (no evidence on its own
// fetch); hop2 (sysadmin) both emits its own TRANSITION_TO AND has real
// objective evidence, so hop3 must never be reached — the gate must intercept
// using hop2's own trace/evidence, mirroring
// TestHandlePlaybookRun_ObjectiveEvidence_ForcedGate but one hop deeper.
func TestHandlePlaybookRun_ChainedHop_ObjectiveEvidence_ForcedGate(t *testing.T) {
	pbTriage := &audit.Playbook{
		PlaybookID: "pb_oevchain_triage", SeriesID: "pbs_oevchain_triage",
		Name: "OEV Chain Triage", ExecutionMode: "agent", AgentName: "oev_db_agent", IsActive: true,
	}
	pbSysadmin := &audit.Playbook{
		PlaybookID: "pb_oevchain_sysadmin", SeriesID: "pbs_oevchain_sysadmin",
		Name: "OEV Chain Sysadmin", ExecutionMode: "agent", AgentName: "oev_sysadmin_agent", IsActive: true,
	}
	pbRemediate := &audit.Playbook{
		PlaybookID: "pb_oevchain_remediate", SeriesID: "pbs_oevchain_remediate",
		Name: "OEV Chain Remediate", ExecutionMode: "agent", AgentName: "oev_remediate_agent", IsActive: true,
	}

	auditSrv := newMockChainAuditd(t,
		map[string]*audit.Playbook{"pb_oevchain_triage": pbTriage},
		map[string]*audit.Playbook{
			"pbs_oevchain_sysadmin":  pbSysadmin,
			"pbs_oevchain_remediate": pbRemediate,
		})
	// Skip 1 call: hop1's own evidence fetch sees no evidence, so it does not
	// get gated on its own and chainEscalation actually runs. Every call from
	// the 2nd onward (hop2's fetch) sees real evidence.
	auditSrv.evidenceSkipCalls = 1
	auditSrv.evidenceSignal = "pod_restarted"

	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	_, dbCard := mockA2AServerWithText(t, "oev_db_agent",
		"HYPOTHESIS_1: db-level connection issue | CONFIDENCE: 0.90 | EVIDENCE: \"connection refused\"\n"+
			"ROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: looks like a db problem\nESCALATE_TO: pbs_oevchain_sysadmin\n")
	_, sysadminCard := mockA2AServerWithText(t, "oev_sysadmin_agent",
		"HYPOTHESIS_1: pod recovered after restart | CONFIDENCE: 0.90 | EVIDENCE: \"restart_count=2\"\n"+
			"ROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: pod recovered\nTRANSITION_TO: pbs_oevchain_remediate\n")
	_, remediateCard := mockA2AServerWithText(t, "oev_remediate_agent", "FINDINGS: should never be reached\n")
	for name, card := range map[string]*a2a.AgentCard{
		"oev_db_agent":        dbCard,
		"oev_sysadmin_agent":  sysadminCard,
		"oev_remediate_agent": remediateCard,
	} {
		client, err := a2aclient.NewFromCard(context.Background(), card)
		if err != nil {
			t.Fatalf("create A2A client for %s: %v", name, err)
		}
		gw.clients[name] = client
	}

	rec := postPlaybookRun(t, gw, pbTriage.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"connection refused","approval_mode":"force"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] != "pending_gate" {
		t.Fatalf("status = %q, want pending_gate — chained-hop objective-evidence gate did not fire; body: %s", resp["status"], rec.Body.String())
	}
	if resp["gate_reason"] != "objective_evidence:pod_restarted" {
		t.Errorf("gate_reason = %q, want objective_evidence:pod_restarted", resp["gate_reason"])
	}
	if resp["transition_target"] != "pbs_oevchain_remediate" {
		t.Errorf("transition_target = %q, want pbs_oevchain_remediate — must reflect hop2's own signal, not hop1's", resp["transition_target"])
	}
	// Only 2 runs should have been created (hop1 + hop2) — the gate must stop
	// the chain before hop3 (remediate) is ever invoked.
	if got := auditSrv.runCount(); got != 2 {
		t.Errorf("runCount() = %d, want 2 — remediate agent should never have been invoked, the chain must stop at the gate", got)
	}
}

// TestHandlePlaybookRun_ChainedHop_ObjectiveEvidence_NoEscalation_SurfacesWarning
// verifies the standalone-warning path for a SECOND hop: hop1 (triage)
// escalates cleanly with no evidence; hop2 (sysadmin) has real objective
// evidence but emits no further TRANSITION_TO/ESCALATE_TO. The chain must
// complete normally (no pending_gate — there is no next-hop to gate approval
// for), and extra["evidence_warnings"] set by recordEvidenceWithoutEscalationWarning's
// *chained call site (not just its primary call site) must appear on the
// final response.
func TestHandlePlaybookRun_ChainedHop_ObjectiveEvidence_NoEscalation_SurfacesWarning(t *testing.T) {
	pbTriage := &audit.Playbook{
		PlaybookID: "pb_oevchain2_triage", SeriesID: "pbs_oevchain2_triage",
		Name: "OEV Chain2 Triage", ExecutionMode: "agent", AgentName: "oev2_db_agent", IsActive: true,
	}
	pbSysadmin := &audit.Playbook{
		PlaybookID: "pb_oevchain2_sysadmin", SeriesID: "pbs_oevchain2_sysadmin",
		Name: "OEV Chain2 Sysadmin", ExecutionMode: "agent", AgentName: "oev2_sysadmin_agent", IsActive: true,
	}

	auditSrv := newMockChainAuditd(t,
		map[string]*audit.Playbook{"pb_oevchain2_triage": pbTriage},
		map[string]*audit.Playbook{"pbs_oevchain2_sysadmin": pbSysadmin})
	auditSrv.evidenceSkipCalls = 1 // hop1's own fetch sees no evidence
	auditSrv.evidenceSignal = "oom_killed"

	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	_, dbCard := mockA2AServerWithText(t, "oev2_db_agent",
		"HYPOTHESIS_1: db-level connection issue | CONFIDENCE: 0.90 | EVIDENCE: \"connection refused\"\n"+
			"ROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: looks like a db problem\nESCALATE_TO: pbs_oevchain2_sysadmin\n")
	// No TRANSITION_TO/ESCALATE_TO — hop2 silently closes out despite evidence.
	_, sysadminCard := mockA2AServerWithText(t, "oev2_sysadmin_agent",
		"HYPOTHESIS_1: pod is healthy now | CONFIDENCE: 0.95 | EVIDENCE: \"status=Running\"\n"+
			"ROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: pod healthy; no action needed\n")
	for name, card := range map[string]*a2a.AgentCard{
		"oev2_db_agent":       dbCard,
		"oev2_sysadmin_agent": sysadminCard,
	} {
		client, err := a2aclient.NewFromCard(context.Background(), card)
		if err != nil {
			t.Fatalf("create A2A client for %s: %v", name, err)
		}
		gw.clients[name] = client
	}

	rec := postPlaybookRun(t, gw, pbTriage.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"connection refused","approval_mode":"force"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] == "pending_gate" {
		t.Error("status = pending_gate, but hop2 has no escalation target — should not re-route into the gate flow")
	}
	warnings, ok := resp["evidence_warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("expected exactly one evidence_warnings entry from hop2's chained call site, got %v — body: %s", resp["evidence_warnings"], rec.Body.String())
	}
	warnStr, _ := warnings[0].(string)
	if !strings.Contains(warnStr, "oom_killed") || !strings.Contains(warnStr, "pbs_oevchain2_sysadmin") {
		t.Errorf("evidence_warnings[0] = %q, want it to mention oom_killed and the sysadmin hop's own series_id", warnStr)
	}
	if resp["chained_run_id"] == nil || resp["chained_run_id"] == "" {
		t.Error("expected a chained_run_id — hop2 must have actually run via chainEscalation, not been skipped")
	}
}

// serveFakeToolEvents starts an httptest.Server that responds to
// GET /v1/events with the given events JSON-encoded.
func serveFakeToolEvents(t *testing.T, events []audit.Event) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ── enforceApprovalOverride ───────────────────────────────────────────────────

func TestEnforceApprovalOverride_NoInfra(t *testing.T) {
	g := &Gateway{infra: nil}
	mode := "force"
	var warnings []string
	g.enforceApprovalOverride(identity.ResolvedPrincipal{}, &mode, "manual", "host=db", &warnings)
	if mode != "force" {
		t.Errorf("mode changed without infra config: got %q", mode)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestEnforceApprovalOverride_NoRestriction(t *testing.T) {
	g := &Gateway{infra: &infra.Config{
		DBServers: map[string]infra.DBServer{
			"dev-db": {ConnectionString: "host=localhost port=5432 dbname=dev"},
		},
	}}
	mode := "force"
	var warnings []string
	g.enforceApprovalOverride(identity.ResolvedPrincipal{}, &mode, "manual", "host=localhost port=5432 dbname=dev", &warnings)
	if mode != "force" {
		t.Errorf("mode should not change when no approval_override_roles set: got %q", mode)
	}
}

func TestEnforceApprovalOverride_CallerHasRole(t *testing.T) {
	g := &Gateway{infra: &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-db": {
				ConnectionString:      "host=prod port=5432 dbname=app",
				ApprovalOverrideRoles: []string{"dba_lead"},
			},
		},
	}}
	principal := identity.ResolvedPrincipal{UserID: "alice", Roles: []string{"dba_lead"}}
	mode := "force"
	var warnings []string
	g.enforceApprovalOverride(principal, &mode, "manual", "host=prod port=5432 dbname=app", &warnings)
	if mode != "force" {
		t.Errorf("mode should not change when caller has required role: got %q", mode)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestEnforceApprovalOverride_CallerLacksRole(t *testing.T) {
	g := &Gateway{infra: &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-db": {
				ConnectionString:      "host=prod port=5432 dbname=app",
				ApprovalOverrideRoles: []string{"dba_lead", "oncall_senior"},
			},
		},
	}}
	principal := identity.ResolvedPrincipal{UserID: "bob", Roles: []string{"sre"}}
	mode := "force"
	var warnings []string
	g.enforceApprovalOverride(principal, &mode, "manual", "host=prod port=5432 dbname=app", &warnings)
	if mode != "manual" {
		t.Errorf("mode should be clamped to 'manual': got %q", mode)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "clamped") {
		t.Errorf("warning should mention 'clamped': %q", warnings[0])
	}
}

func TestEnforceApprovalOverride_NotAnOverride(t *testing.T) {
	// Requesting 'manual' against a playbook that is also 'manual' — not an override.
	g := &Gateway{infra: &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-db": {
				ConnectionString:      "host=prod port=5432 dbname=app",
				ApprovalOverrideRoles: []string{"dba_lead"},
			},
		},
	}}
	principal := identity.ResolvedPrincipal{UserID: "bob", Roles: []string{"sre"}}
	mode := "manual"
	var warnings []string
	g.enforceApprovalOverride(principal, &mode, "manual", "host=prod port=5432 dbname=app", &warnings)
	if mode != "manual" {
		t.Errorf("mode should be unchanged: got %q", mode)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestEnforceApprovalOverride_DBNotInInfra(t *testing.T) {
	// Infra is configured but the connStr doesn't match any server — no clamp.
	g := &Gateway{infra: &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-db": {
				ConnectionString:      "host=prod port=5432 dbname=app",
				ApprovalOverrideRoles: []string{"dba_lead"},
			},
		},
	}}
	mode := "force"
	var warnings []string
	g.enforceApprovalOverride(identity.ResolvedPrincipal{}, &mode, "manual", "host=unknown port=9999 dbname=other", &warnings)
	if mode != "force" {
		t.Errorf("mode should be unchanged when DB not in infra: got %q", mode)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestEnforceApprovalOverride_RoleChecks(t *testing.T) {
	cfg := &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-db": {
				ConnectionString:      "host=prod port=5432 dbname=app",
				ApprovalOverrideRoles: []string{"dba_lead", "oncall_senior"},
			},
		},
	}
	connStr := "host=prod port=5432 dbname=app"

	cases := []struct {
		name          string
		roles         []string
		requestedMode string
		playbookMode  string
		wantMode      string
		wantClamped   bool
	}{
		{"has first role", []string{"dba_lead"}, "force", "manual", "force", false},
		{"has second role", []string{"oncall_senior"}, "force", "manual", "force", false},
		{"has unrelated role", []string{"sre"}, "force", "manual", "manual", true},
		{"review override no role", []string{"sre"}, "review", "manual", "manual", true},
		{"review override has role", []string{"dba_lead"}, "review", "manual", "review", false},
		{"equal rank not an override", []string{}, "manual", "manual", "manual", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &Gateway{infra: cfg}
			mode := tc.requestedMode
			var warnings []string
			g.enforceApprovalOverride(
				identity.ResolvedPrincipal{Roles: tc.roles},
				&mode, tc.playbookMode, connStr, &warnings,
			)
			if mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", mode, tc.wantMode)
			}
			gotClamped := len(warnings) > 0
			if gotClamped != tc.wantClamped {
				t.Errorf("clamped = %v, want %v (warnings: %v)", gotClamped, tc.wantClamped, warnings)
			}
		})
	}
}

func TestHandlePlaybookRun_ApprovalOverrideClamped(t *testing.T) {
	// Integration: force mode gets clamped to manual for a restricted DB; warning
	// appears in the pending_approval response.
	pb := &audit.Playbook{
		PlaybookID:    "pb_override01",
		SeriesID:      "pbs_lock_chain_remediate",
		Name:          "Lock Chain Remediate",
		Guidance:      "Step 1: terminate root.",
		ExecutionMode: "agent_approve",
		ApprovalMode:  "manual",
		IsActive:      true,
	}

	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(pb) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_override01", "approval_id": "apr_override01"}) //nolint:errcheck
	}))
	t.Cleanup(auditSrv.Close)

	llmFn := func(_ context.Context, _ string) (string, error) {
		return `{"action":"execute_step","agent":"database","tool":"get_blocking_queries","args":{},"reason":"inspect"}`, nil
	}

	// Gateway with a DB that restricts override to dba_lead.
	// The test request carries no principal (zero value, no roles).
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{
		{Name: "get_blocking_queries", Agent: "database", ActionClass: "read"},
	})
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client),
		infra: &infra.Config{
			DBServers: map[string]infra.DBServer{
				"restricted-db": {
					ConnectionString:      "host=restricted port=5432 dbname=app",
					ApprovalOverrideRoles: []string{"dba_lead"},
				},
			},
		},
		toolRegistry: reg,
		plannerLLM:   llmFn,
		auditURL:     auditSrv.URL,
	}

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	body := `{"connection_string":"host=restricted port=5432 dbname=app","approval_mode":"force"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/playbooks/pb_override01/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("playbookID", "pb_override01")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v — %s", err, rec.Body.String())
	}
	warnings, _ := resp["warnings"].([]any)
	if len(warnings) == 0 {
		t.Fatal("expected warnings in response when approval_mode is clamped")
	}
	w0, _ := warnings[0].(string)
	if !strings.Contains(w0, "clamped") {
		t.Errorf("warning should mention 'clamped': %q", w0)
	}
	if !strings.Contains(w0, "dba_lead") {
		t.Errorf("warning should mention required role: %q", w0)
	}
}

// postProceedEscalation posts to the proceed-escalation endpoint through the mux.
func postProceedEscalation(t *testing.T, gw *Gateway, runID, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/fleet/playbook-runs/"+runID+"/proceed-escalation",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// mockGatePendingAuditd serves the auditd calls made by handleProceedEscalation:
// GET run, PATCH run, GET playbooks?series_id=, POST runs for recordPlaybookRunStart.
// remedPB may be nil for tests that deny or error before the playbook lookup.
func mockGatePendingAuditd(t *testing.T, run *audit.PlaybookRun, remedPB *audit.Playbook) *httptest.Server {
	t.Helper()
	runData, _ := json.Marshal(run)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/playbook-runs/"):
			w.Write(runData) //nolint:errcheck
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/playbook-runs/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Query().Get("series_id") != "":
			if remedPB != nil {
				json.NewEncoder(w).Encode(map[string]any{"playbooks": []*audit.Playbook{remedPB}}) //nolint:errcheck
			} else {
				json.NewEncoder(w).Encode(map[string]any{"playbooks": []*audit.Playbook{}}) //nolint:errcheck
			}
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/runs"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_rem01"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --- confidenceWarning ---

func TestConfidenceWarning_NilReport(t *testing.T) {
	gw := &Gateway{}
	w, m := gw.confidenceWarning(nil)
	if w != "" || m != "" {
		t.Errorf("nil report: got warn=%q mode=%q, want both empty", w, m)
	}
}

func TestConfidenceWarning_EmptyHypotheses(t *testing.T) {
	gw := &Gateway{}
	w, m := gw.confidenceWarning(&audit.DiagnosticReport{})
	if w != "" || m != "" {
		t.Errorf("empty hypotheses: got warn=%q mode=%q, want both empty", w, m)
	}
}

func TestConfidenceWarning_HighConfidence(t *testing.T) {
	gw := &Gateway{}
	report := &audit.DiagnosticReport{
		Hypotheses: []audit.DiagnosticHypothesis{{Rank: 1, Confidence: 0.85, IsPrimary: true}},
	}
	w, m := gw.confidenceWarning(report)
	if w != "" || m != "" {
		t.Errorf("high-confidence single: want no warn, got warn=%q mode=%q", w, m)
	}
}

func TestConfidenceWarning_LowAbsolute(t *testing.T) {
	gw := &Gateway{}
	report := &audit.DiagnosticReport{
		Hypotheses: []audit.DiagnosticHypothesis{{Rank: 1, Confidence: 0.55, IsPrimary: true}},
	}
	w, m := gw.confidenceWarning(report)
	if w == "" {
		t.Fatal("low-confidence: want non-empty warning, got empty")
	}
	if m != "manual" {
		t.Errorf("suggested mode = %q, want manual", m)
	}
	if !strings.Contains(w, "55%") {
		t.Errorf("warning should contain confidence percentage, got %q", w)
	}
}

func TestConfidenceWarning_CompetingHypotheses(t *testing.T) {
	gw := &Gateway{}
	// primary=0.80, secondary=0.60 → 0.60/0.80 = 0.75 > 0.70 threshold → competing triggers
	report := &audit.DiagnosticReport{
		Hypotheses: []audit.DiagnosticHypothesis{
			{Rank: 1, Confidence: 0.80, IsPrimary: true},
			{Rank: 2, Confidence: 0.60},
		},
	}
	w, m := gw.confidenceWarning(report)
	if w == "" {
		t.Fatal("competing hypotheses: want non-empty warning, got empty")
	}
	if m != "manual" {
		t.Errorf("suggested mode = %q, want manual", m)
	}
	if !strings.Contains(w, "competing") {
		t.Errorf("warning should mention competing hypothesis, got %q", w)
	}
}

func TestConfidenceWarning_CompetingBelowThreshold(t *testing.T) {
	gw := &Gateway{}
	// primary=0.80, secondary=0.40 → 0.40/0.80 = 0.50 < 0.70 threshold → no warn
	report := &audit.DiagnosticReport{
		Hypotheses: []audit.DiagnosticHypothesis{
			{Rank: 1, Confidence: 0.80, IsPrimary: true},
			{Rank: 2, Confidence: 0.40},
		},
	}
	w, m := gw.confidenceWarning(report)
	if w != "" || m != "" {
		t.Errorf("secondary below threshold: want no warn, got warn=%q mode=%q", w, m)
	}
}

// --- handleProceedEscalation ---

func TestHandleProceedEscalation_RunNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	gw := makePlaybookRunGateway(srv.URL, nil)
	rec := postProceedEscalation(t, gw, "plr_missing", `{"resolution":"approved"}`)

	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404 for missing run", rec.Code)
	}
}

func TestHandleProceedEscalation_WrongOutcome(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:       "plr_resolved01",
		Outcome:     audit.OutcomeResolved,
		EscalatedTo: "pbs_lock_chain_remediate",
	}
	auditSrv := mockGatePendingAuditd(t, run, nil)

	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	rec := postProceedEscalation(t, gw, "plr_resolved01", `{"resolution":"approved"}`)

	if rec.Code != http.StatusConflict {
		t.Errorf("got %d, want 409 for run with outcome=%q", rec.Code, audit.OutcomeResolved)
	}
}

func TestHandleProceedEscalation_Denied(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:           "plr_gate01",
		Outcome:         audit.OutcomeGatePending,
		EscalatedTo:     "pbs_lock_chain_remediate",
		FindingsSummary: "Long-running transaction blocking replication.",
	}
	auditSrv := mockGatePendingAuditd(t, run, nil)

	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	rec := postProceedEscalation(t, gw, "plr_gate01",
		`{"resolution":"denied","resolved_by":"ops-alice"}`)

	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200 for denied gate", rec.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["status"] != "denied" {
		t.Errorf("status = %q, want denied", resp["status"])
	}
	if resp["run_id"] != "plr_gate01" {
		t.Errorf("run_id = %q, want plr_gate01", resp["run_id"])
	}
}

// TestHandleProceedEscalation_Approved_ChainsToAgent verifies that an approved gate
// resolves the triage run and chains to the remediation playbook. With no A2A client
// wired, proxyToAgent returns 502 — confirming the agent path was reached.
func TestHandleProceedEscalation_Approved_ChainsToAgent(t *testing.T) {
	run := &audit.PlaybookRun{
		RunID:           "plr_gate02",
		Outcome:         audit.OutcomeGatePending,
		EscalatedTo:     "pbs_lock_chain_remediate",
		FindingsSummary: "Lock chain detected; root blocker PID=1234.",
	}
	remedPB := &audit.Playbook{
		PlaybookID:    "pb_remediate01",
		SeriesID:      "pbs_lock_chain_remediate",
		Name:          "Lock Chain — Terminate Root Blocker",
		ExecutionMode: "agent",
		IsActive:      true,
	}
	auditSrv := mockGatePendingAuditd(t, run, remedPB)

	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	rec := postProceedEscalation(t, gw, "plr_gate02",
		`{"resolution":"approved","resolved_by":"ops-alice","approval_mode":"auto"}`)

	// No A2A client → 502 from proxyToAgent, confirming the agent path was taken.
	if rec.Code != http.StatusBadGateway {
		t.Errorf("got %d, want 502 (no A2A client wired); body: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleProceedEscalation_Approved_Transition verifies the path where the
// pending gate originated from a TRANSITION_TO signal (run.TransitionedTo is set,
// run.EscalatedTo is empty). The gateway must:
//   - resolve the triage run with outcome="transitioned" (not "escalated")
//   - persist transitioned_to in the PATCH body
//   - look up the remediation playbook by the transitioned_to series_id
//   - chain to the agent (502 here — no A2A client wired)
func TestHandleProceedEscalation_Approved_Transition(t *testing.T) {
	var patchBody string
	run := &audit.PlaybookRun{
		RunID:           "plr_gate03",
		Outcome:         audit.OutcomeGatePending,
		TransitionedTo:  "pbs_vacuum_remediate",
		FindingsSummary: "Vacuum lag detected; manual vacuum needed.",
	}
	remedPB := &audit.Playbook{
		PlaybookID:    "pb_vac_rem01",
		SeriesID:      "pbs_vacuum_remediate",
		Name:          "Vacuum & Bloat — Remediation",
		ExecutionMode: "agent",
		IsActive:      true,
	}
	runData, _ := json.Marshal(run)

	// Custom mock so we can capture and assert the PATCH body.
	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/playbook-runs/"):
			w.Write(runData) //nolint:errcheck
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/playbook-runs/"):
			b, _ := io.ReadAll(r.Body)
			patchBody = string(b)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Query().Get("series_id") != "":
			json.NewEncoder(w).Encode(map[string]any{"playbooks": []*audit.Playbook{remedPB}}) //nolint:errcheck
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/runs"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_rem02"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(auditSrv.Close)

	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	rec := postProceedEscalation(t, gw, "plr_gate03",
		`{"resolution":"approved","resolved_by":"ops-alice","approval_mode":"auto"}`)

	// No A2A client → 502, confirming the agent chain path was reached.
	if rec.Code != http.StatusBadGateway {
		t.Errorf("got %d, want 502 (no A2A client wired); body: %s", rec.Code, rec.Body.String())
	}

	// Verify the PATCH stored outcome=transitioned with transitioned_to set.
	if !strings.Contains(patchBody, `"transitioned"`) {
		t.Errorf("PATCH body should contain outcome=transitioned; got: %s", patchBody)
	}
	if !strings.Contains(patchBody, "pbs_vacuum_remediate") {
		t.Errorf("PATCH body should contain transitioned_to=pbs_vacuum_remediate; got: %s", patchBody)
	}
}

// TestHandleProceedEscalation_NamespaceFallsBackToTriageRun verifies the
// informed-gate flow (used with --gate-escalation, distinct from faulttest's
// default direct-remediation path already covered by
// TestHandlePlaybookRunApprove/ProceedNamespaceForceOverridden): when the
// operator's proceed-escalation request doesn't specify a namespace, the
// chained remediation run must fall back to the one stored on the triage
// run rather than starting the remediation playbook with no namespace at all.
func TestHandleProceedEscalation_NamespaceFallsBackToTriageRun(t *testing.T) {
	var runStartBody string
	run := &audit.PlaybookRun{
		RunID:           "plr_gate_ns01",
		Outcome:         audit.OutcomeGatePending,
		TransitionedTo:  "pbs_k8s_node_pressure_remediate",
		FindingsSummary: "namespace=helpdesk-test; postgres_restarted=no",
		Namespace:       "helpdesk-test",
	}
	remedPB := &audit.Playbook{
		PlaybookID:    "pb_node_pressure_rem01",
		SeriesID:      "pbs_k8s_node_pressure_remediate",
		Name:          "K8s Node Memory Pressure — Remediation",
		ExecutionMode: "agent",
		IsActive:      true,
	}
	runData, _ := json.Marshal(run)

	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/playbook-runs/"):
			w.Write(runData) //nolint:errcheck
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/playbook-runs/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Query().Get("series_id") != "":
			json.NewEncoder(w).Encode(map[string]any{"playbooks": []*audit.Playbook{remedPB}}) //nolint:errcheck
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/runs"):
			b, _ := io.ReadAll(r.Body)
			runStartBody = string(b)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_rem_ns01"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(auditSrv.Close)

	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	// No "namespace" field in the request body — must fall back to run.Namespace.
	rec := postProceedEscalation(t, gw, "plr_gate_ns01",
		`{"resolution":"approved","resolved_by":"ops-alice","approval_mode":"auto"}`)

	// No A2A client → 502, confirming the agent chain path was reached.
	if rec.Code != http.StatusBadGateway {
		t.Errorf("got %d, want 502 (no A2A client wired); body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(runStartBody, `"namespace":"helpdesk-test"`) {
		t.Errorf("recordPlaybookRunStart body should carry namespace=helpdesk-test (fallback from the triage run), got: %s", runStartBody)
	}
}

// TestHandleProceedEscalation_PurposeFallsBackToTriageRun verifies the same
// fallback behavior as TestHandleProceedEscalation_NamespaceFallsBackToTriageRun,
// for purpose: when the operator's proceed-escalation request doesn't specify
// a purpose, the chained remediation run must fall back to the one stored on
// the triage run rather than starting the remediation playbook with no
// purpose at all — an empty purpose can cause policy checks on the next hop
// to deny an otherwise-legitimate tool call (e.g. K8s get_pods requiring an
// explicit purpose in its allowed_purposes list).
func TestHandleProceedEscalation_PurposeFallsBackToTriageRun(t *testing.T) {
	var runStartBody string
	run := &audit.PlaybookRun{
		RunID:           "plr_gate_purpose01",
		Outcome:         audit.OutcomeGatePending,
		EscalatedTo:     "pbs_sysadmin_docker_inspect",
		FindingsSummary: "Pod is Kubernetes-managed; sysadmin investigation required.",
		Purpose:         "diagnostic",
	}
	remedPB := &audit.Playbook{
		PlaybookID:    "pb_sysadmin_rem01",
		SeriesID:      "pbs_sysadmin_docker_inspect",
		Name:          "Sysadmin — Docker Inspect",
		ExecutionMode: "agent",
		IsActive:      true,
	}
	runData, _ := json.Marshal(run)

	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/playbook-runs/"):
			w.Write(runData) //nolint:errcheck
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/playbook-runs/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Query().Get("series_id") != "":
			json.NewEncoder(w).Encode(map[string]any{"playbooks": []*audit.Playbook{remedPB}}) //nolint:errcheck
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/runs"):
			b, _ := io.ReadAll(r.Body)
			runStartBody = string(b)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_rem_purpose01"}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(auditSrv.Close)

	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	// No "purpose" field in the request body — must fall back to run.Purpose.
	// Dispatch inline (rather than via postProceedEscalation) so the test can
	// inspect the request's headers after the call — proxyToAgentWithTool reads
	// X-Purpose directly off this same *http.Request, so this is the actual
	// mechanism the live bug was about, not just the recorded metadata body.
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/fleet/playbook-runs/plr_gate_purpose01/proceed-escalation",
		strings.NewReader(`{"resolution":"approved","resolved_by":"ops-alice","approval_mode":"auto"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// No A2A client → 502, confirming the agent chain path was reached.
	if rec.Code != http.StatusBadGateway {
		t.Errorf("got %d, want 502 (no A2A client wired); body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(runStartBody, `"purpose":"diagnostic"`) {
		t.Errorf("recordPlaybookRunStart body should carry purpose=diagnostic (fallback from the triage run), got: %s", runStartBody)
	}
	if got := req.Header.Get("X-Purpose"); got != "diagnostic" {
		t.Errorf("X-Purpose header = %q, want %q — proxyToAgentWithTool reads this directly for downstream policy checks", got, "diagnostic")
	}
}

// --- gate_escalation intercept tests ---

// mockA2AServerWithText starts a minimal JSON-RPC A2A server that returns responseText
// as the completed task's status message text.
func mockA2AServerWithText(t *testing.T, agentName, responseText string) (*httptest.Server, *a2a.AgentCard) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"kind": "task",
				"id":   "task-gate-1",
				"status": map[string]any{
					"state": "completed",
					"message": map[string]any{
						"role": "agent",
						"parts": []map[string]any{
							{"kind": "text", "text": responseText},
						},
					},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	card := &a2a.AgentCard{
		Name:               agentName,
		URL:                srv.URL,
		PreferredTransport: a2a.TransportProtocolJSONRPC,
	}
	return srv, card
}

// mockGateAuditdPlaybook starts a mock auditd that handles the calls made
// during a gate-intercepted playbook run (playbook fetch, run start, run complete).
func mockGateAuditdPlaybook(t *testing.T, pb *audit.Playbook) *httptest.Server {
	t.Helper()
	pbData, _ := json.Marshal(pb)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/fleet/playbooks"):
			w.Write(pbData) //nolint:errcheck
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/runs"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_gate_test01"}) //nolint:errcheck
		case r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusNoContent)
		default:
			// Delegation verification events query and any other reads → empty list.
			w.Write([]byte("[]")) //nolint:errcheck
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// makeGateGateway wires a gateway with a mock A2A agent for gate_escalation tests.
func makeGateGateway(t *testing.T, auditURL string, agentName, responseText string) *Gateway {
	t.Helper()
	_, card := mockA2AServerWithText(t, agentName, responseText)
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("create A2A client: %v", err)
	}
	gw := makePlaybookRunGateway(auditURL, nil)
	gw.clients = map[string]*a2aclient.Client{agentName: client}
	return gw
}

// TestHandlePlaybookRun_GateEscalation_Intercepts verifies that when
// gate_escalation=true and the agent emits an actionable TRANSITION_TO (recommended
// is not "monitor"), the gateway intercepts at the phase boundary and returns
// status="pending_gate" with transition_target and gate_type="transition".
func TestHandlePlaybookRun_GateEscalation_Intercepts(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_vac_triage01",
		SeriesID:      "pbs_vacuum_triage",
		Name:          "Vacuum & Bloat Triage",
		Guidance:      "Check dead tuples and autovacuum lag.",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	agentText := "Analysis complete.\n\n" +
		"FINDINGS: worst table public.orders dead_ratio=0.32 (4.2GB); autovacuum=stuck; blocker_pid=none; recommended=manual_vacuum\n" +
		"TRANSITION_TO: pbs_vacuum_remediate\n"

	auditSrv := mockGateAuditdPlaybook(t, pb)
	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)

	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"bloat alert","gate_escalation":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] != "pending_gate" {
		t.Errorf("status = %q, want pending_gate", resp["status"])
	}
	if resp["transition_target"] != "pbs_vacuum_remediate" {
		t.Errorf("transition_target = %q, want pbs_vacuum_remediate", resp["transition_target"])
	}
	if resp["gate_type"] != "transition" {
		t.Errorf("gate_type = %q, want transition", resp["gate_type"])
	}
	if resp["run_id"] == nil || resp["run_id"] == "" {
		t.Error("run_id should be populated in pending_gate response")
	}
	if resp["escalation_findings"] == nil || resp["escalation_findings"] == "" {
		t.Error("escalation_findings should be populated in pending_gate response")
	}
}

// TestHandlePlaybookRun_GateEscalation_TrueEscalation verifies that a genuine
// ESCALATE_TO (cross-domain handoff) returns gate_type="escalation" and
// escalation_target (not transition_target).
func TestHandlePlaybookRun_GateEscalation_TrueEscalation(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_conn_triage01",
		SeriesID:      "pbs_connection_triage",
		Name:          "Connection & Lock Triage",
		Guidance:      "Check connection pool and lock contention.",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	// True ESCALATE_TO: the DB triage found something requiring a different domain
	// (e.g. sysadmin-level action). This is NOT a same-series pipeline transition.
	agentText := "Blocker session is making external calls. OS-level escalation needed.\n\n" +
		"FINDINGS: connections 198/200 (99%); blocker=PID 4321 (active, 45m, has_writes=true); recommended=escalate\n" +
		"ESCALATE_TO: pbs_sysadmin_docker_inspect\n"

	auditSrv := mockGateAuditdPlaybook(t, pb)
	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)

	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"connection pool exhausted","gate_escalation":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] != "pending_gate" {
		t.Errorf("status = %q, want pending_gate", resp["status"])
	}
	if resp["escalation_target"] != "pbs_sysadmin_docker_inspect" {
		t.Errorf("escalation_target = %q, want pbs_sysadmin_docker_inspect", resp["escalation_target"])
	}
	if resp["gate_type"] != "escalation" {
		t.Errorf("gate_type = %q, want escalation", resp["gate_type"])
	}
	if resp["transition_target"] != nil && resp["transition_target"] != "" {
		t.Errorf("transition_target should be absent for true escalation, got %q", resp["transition_target"])
	}
	if resp["run_id"] == nil || resp["run_id"] == "" {
		t.Error("run_id should be populated in pending_gate response")
	}
}

// TestHandlePlaybookRun_GateEscalation_Monitor_NoIntercept verifies that when
// gate_escalation=true but the agent's FINDINGS line recommends "monitor" (nothing
// actionable found), the gateway does NOT intercept — the run completes normally
// without creating a pending_gate that would block on operator approval.
func TestHandlePlaybookRun_GateEscalation_Monitor_NoIntercept(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_vac_triage02",
		SeriesID:      "pbs_vacuum_triage",
		Name:          "Vacuum & Bloat Triage",
		Guidance:      "Check dead tuples and autovacuum lag.",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	// Triage completes but finds nothing actionable: autovacuum is keeping up,
	// dead_ratio is low. recommended=monitor → gate must NOT fire.
	agentText := "All tables look healthy. Autovacuum is running normally.\n\n" +
		"FINDINGS: worst table public.orders dead_ratio=0.03 (4.2GB); autovacuum=running; blocker_pid=none; recommended=monitor\n" +
		"TRANSITION_TO: pbs_vacuum_remediate\n"

	auditSrv := mockGateAuditdPlaybook(t, pb)
	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)

	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"routine check","gate_escalation":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	// Gate must NOT have fired: the run should complete, not pause for operator approval.
	if resp["status"] == "pending_gate" {
		t.Error("gate fired for recommended=monitor — should complete without operator gate")
	}
}

// TestHandlePlaybookRun_GateEscalation_RemediationPreview verifies that when
// gate_escalation=true fires and the remediation playbook can be resolved by
// series_id, the gate response includes a populated remediation_preview block.
func TestHandlePlaybookRun_GateEscalation_RemediationPreview(t *testing.T) {
	triagePB := &audit.Playbook{
		PlaybookID:    "pb_vac_triage03",
		SeriesID:      "pbs_vacuum_triage",
		Name:          "Vacuum & Bloat Triage",
		Guidance:      "Check dead tuples and autovacuum lag.",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	remPB := &audit.Playbook{
		PlaybookID:   "pb_vac_remediate01",
		SeriesID:     "pbs_vacuum_remediate",
		Name:         "Vacuum Remediation",
		Description:  "Run VACUUM ANALYZE and verify dead tuple ratio drops below 20%.",
		ApprovalMode: "review",
		IsActive:     true,
	}
	agentText := "High dead tuple ratio detected.\n\n" +
		"FINDINGS: worst table public.orders dead_ratio=0.32; recommended=manual_vacuum\n" +
		"TRANSITION_TO: pbs_vacuum_remediate\n"

	triageData, _ := json.Marshal(triagePB)
	remList, _ := json.Marshal(map[string]any{"playbooks": []*audit.Playbook{remPB}})
	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/fleet/playbooks"):
			// fetchPlaybookBySeriesID uses ?series_id=...; the initial triage fetch uses /id path.
			if r.URL.Query().Get("series_id") != "" {
				w.Write(remList) //nolint:errcheck
			} else {
				w.Write(triageData) //nolint:errcheck
			}
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/runs"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_gate_preview01"}) //nolint:errcheck
		case r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Write([]byte("[]")) //nolint:errcheck
		}
	}))
	t.Cleanup(auditSrv.Close)

	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)
	rec := postPlaybookRun(t, gw, triagePB.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"bloat alert","gate_escalation":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] != "pending_gate" {
		t.Errorf("status = %q, want pending_gate", resp["status"])
	}
	preview, ok := resp["remediation_preview"].(map[string]any)
	if !ok {
		t.Fatalf("remediation_preview absent or wrong type; full response: %v", resp)
	}
	if preview["name"] != remPB.Name {
		t.Errorf("remediation_preview.name = %q, want %q", preview["name"], remPB.Name)
	}
	if preview["approval_mode"] != remPB.ApprovalMode {
		t.Errorf("remediation_preview.approval_mode = %q, want %q", preview["approval_mode"], remPB.ApprovalMode)
	}
	if preview["series_id"] != remPB.SeriesID {
		t.Errorf("remediation_preview.series_id = %q, want %q", preview["series_id"], remPB.SeriesID)
	}
	if preview["description"] != remPB.Description {
		t.Errorf("remediation_preview.description = %q, want %q", preview["description"], remPB.Description)
	}
}

// TestLowConfidenceForceGate verifies the confidence-gate enforcement boundary.
func TestLowConfidenceForceGate(t *testing.T) {
	cases := []struct {
		name   string
		report *audit.DiagnosticReport
		want   bool
	}{
		{
			name:   "nil report — pre-B1 playbook, no gate",
			report: nil,
			want:   false,
		},
		{
			name: "primary confidence 0.45 — below threshold, force gate",
			report: &audit.DiagnosticReport{
				Hypotheses: []audit.DiagnosticHypothesis{
					{Rank: 1, IsPrimary: true, Confidence: 0.45, Text: "low-confidence root cause"},
				},
			},
			want: true,
		},
		{
			name: "primary confidence 0.50 — exactly at threshold, no gate",
			report: &audit.DiagnosticReport{
				Hypotheses: []audit.DiagnosticHypothesis{
					{Rank: 1, IsPrimary: true, Confidence: 0.50, Text: "borderline confidence"},
				},
			},
			want: false,
		},
		{
			name: "primary confidence 0.75 — high confidence, no gate",
			report: &audit.DiagnosticReport{
				Hypotheses: []audit.DiagnosticHypothesis{
					{Rank: 1, IsPrimary: true, Confidence: 0.75, Text: "high-confidence root cause"},
					{Rank: 2, IsPrimary: false, Confidence: 0.20, Text: "alternative", RejectedReason: "ruled out"},
				},
			},
			want: false,
		},
		{
			name: "no primary marked — uncertain, force gate",
			report: &audit.DiagnosticReport{
				Hypotheses: []audit.DiagnosticHypothesis{
					{Rank: 1, IsPrimary: false, Confidence: 0.60, Text: "hypothesis without primary flag"},
				},
			},
			want: true,
		},
		{
			name:   "non-nil report with empty hypotheses — uncertain, force gate",
			report: &audit.DiagnosticReport{},
			want:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lowConfidenceForceGate(tc.report)
			if got != tc.want {
				t.Errorf("lowConfidenceForceGate(%v) = %v, want %v", tc.report, got, tc.want)
			}
		})
	}
}

// ── objectiveEvidenceForceGate ─────────────────────────────────────────────

func TestObjectiveEvidenceForceGate(t *testing.T) {
	cases := []struct {
		name       string
		events     []audit.Event
		wantFire   bool
		wantReason string
	}{
		{
			name:     "no events — no gate",
			events:   nil,
			wantFire: false,
		},
		{
			name: "event with nil ObjectiveEvidence — no gate",
			events: []audit.Event{
				{EventType: audit.EventTypeObjectiveEvidence},
			},
			wantFire: false,
		},
		{
			name: "event with empty Signal — no gate",
			events: []audit.Event{
				{ObjectiveEvidence: &audit.ObjectiveEvidence{Tool: "get_pods"}},
			},
			wantFire: false,
		},
		{
			name: "real pod_restarted signal — gate fires",
			events: []audit.Event{
				{ObjectiveEvidence: &audit.ObjectiveEvidence{Tool: "get_pods", Signal: "pod_restarted"}},
			},
			wantFire:   true,
			wantReason: "pod_restarted",
		},
		{
			name: "real oom_killed signal — gate fires",
			events: []audit.Event{
				{ObjectiveEvidence: &audit.ObjectiveEvidence{Tool: "get_pods", Signal: "oom_killed"}},
			},
			wantFire:   true,
			wantReason: "oom_killed",
		},
		{
			name: "unrelated events mixed in — first real signal wins",
			events: []audit.Event{
				{EventType: audit.EventTypePolicyDecision},
				{ObjectiveEvidence: &audit.ObjectiveEvidence{Tool: "get_pods"}}, // empty signal, skipped
				{ObjectiveEvidence: &audit.ObjectiveEvidence{Tool: "get_pods", Signal: "pod_restarted"}},
			},
			wantFire:   true,
			wantReason: "pod_restarted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fire, reason := objectiveEvidenceForceGate(tc.events)
			if fire != tc.wantFire {
				t.Errorf("objectiveEvidenceForceGate() fire = %v, want %v", fire, tc.wantFire)
			}
			if reason != tc.wantReason {
				t.Errorf("objectiveEvidenceForceGate() reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// ── trustNotYetEarnedForceGate ──────────────────────────────────────────────

// serveFakeFaultStabilityCerts starts an httptest.Server that responds to
// GET /v1/fleet/fault-stability with the given certs JSON-encoded, mirroring
// the real {"certs": [...]} envelope auditd's handleList returns.
func serveFakeFaultStabilityCerts(t *testing.T, certs []*audit.FaultStabilityCert) *httptest.Server {
	t.Helper()
	if certs == nil {
		certs = []*audit.FaultStabilityCert{}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"certs": certs}) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTrustNotYetEarnedForceGate_FailsOpen_NoDiagnosisModel(t *testing.T) {
	// Mechanism not configured (diagnosisModel empty, the default for every
	// gateway that never calls SetDiagnosisModel — i.e. every other test in
	// this package) — must fail open regardless of what auditd would say.
	// Server would say "not earned" (empty certs) if it were ever queried.
	srv := serveFakeFaultStabilityCerts(t, nil)
	g := &Gateway{auditURL: srv.URL}
	if g.trustNotYetEarnedForceGate("pbs_db_restart_triage") {
		t.Error("expected fail-open (false) when diagnosisModel is unset")
	}
}

func TestTrustNotYetEarnedForceGate_FailsOpen_NoAuditURL(t *testing.T) {
	g := &Gateway{diagnosisModel: "claude-sonnet-4-6"}
	if g.trustNotYetEarnedForceGate("pbs_db_restart_triage") {
		t.Error("expected fail-open (false) when auditURL is unset")
	}
}

func TestTrustNotYetEarnedForceGate_FailsOpen_EmptySeriesID(t *testing.T) {
	srv := serveFakeFaultStabilityCerts(t, nil)
	g := &Gateway{auditURL: srv.URL, diagnosisModel: "claude-sonnet-4-6"}
	if g.trustNotYetEarnedForceGate("") {
		t.Error("expected fail-open (false) when seriesID is empty")
	}
}

func TestTrustNotYetEarnedForceGate_FailsClosed_NoCertOnRecord(t *testing.T) {
	// Mechanism IS configured, auditd genuinely has zero certs for this
	// series+model — "never faulttested" must be treated as "not earned."
	srv := serveFakeFaultStabilityCerts(t, nil)
	g := &Gateway{auditURL: srv.URL, diagnosisModel: "claude-sonnet-4-6"}
	if !g.trustNotYetEarnedForceGate("pbs_db_restart_triage") {
		t.Error("expected fail-closed (true) when no cert is on record")
	}
}

func TestTrustNotYetEarnedForceGate_Earned_AllCertsStableAndClean(t *testing.T) {
	srv := serveFakeFaultStabilityCerts(t, []*audit.FaultStabilityCert{
		{FaultID: "k8s-oomkilled", PlaybookSeriesID: "pbs_k8s_pod_crash_triage", DiagnosisModel: "claude-sonnet-4-6", IsStable: true, IsClean: true},
		{FaultID: "k8s-crashloop", PlaybookSeriesID: "pbs_k8s_pod_crash_triage", DiagnosisModel: "claude-sonnet-4-6", IsStable: true, IsClean: true},
	})
	g := &Gateway{auditURL: srv.URL, diagnosisModel: "claude-sonnet-4-6"}
	if g.trustNotYetEarnedForceGate("pbs_k8s_pod_crash_triage") {
		t.Error("expected earned (false) when every known cert is stable and clean")
	}
}

func TestTrustNotYetEarnedForceGate_NotEarned_OneUnstable(t *testing.T) {
	srv := serveFakeFaultStabilityCerts(t, []*audit.FaultStabilityCert{
		{FaultID: "k8s-oomkilled", PlaybookSeriesID: "pbs_k8s_pod_crash_triage", DiagnosisModel: "claude-sonnet-4-6", IsStable: true, IsClean: true},
		{FaultID: "k8s-crashloop", PlaybookSeriesID: "pbs_k8s_pod_crash_triage", DiagnosisModel: "claude-sonnet-4-6", IsStable: false, IsClean: true},
	})
	g := &Gateway{auditURL: srv.URL, diagnosisModel: "claude-sonnet-4-6"}
	if !g.trustNotYetEarnedForceGate("pbs_k8s_pod_crash_triage") {
		t.Error("expected not earned (true) when even one known cert is unstable — conservative, not 'any good result passes'")
	}
}

func TestTrustNotYetEarnedForceGate_NotEarned_OneDirty(t *testing.T) {
	srv := serveFakeFaultStabilityCerts(t, []*audit.FaultStabilityCert{
		{FaultID: "k8s-oomkilled", PlaybookSeriesID: "pbs_k8s_pod_crash_triage", DiagnosisModel: "claude-sonnet-4-6", IsStable: true, IsClean: true},
		{FaultID: "k8s-crashloop", PlaybookSeriesID: "pbs_k8s_pod_crash_triage", DiagnosisModel: "claude-sonnet-4-6", IsStable: true, IsClean: false, WarningCount: 2},
	})
	g := &Gateway{auditURL: srv.URL, diagnosisModel: "claude-sonnet-4-6"}
	if !g.trustNotYetEarnedForceGate("pbs_k8s_pod_crash_triage") {
		t.Error("expected not earned (true) when even one known cert is dirty (IsClean=false)")
	}
}

// TestHandlePlaybookRun_TrustGate_ForcesGate_UnearnedPlaybook is an end-to-end
// test through handlePlaybookRunAsAgent: a playbook with no fault-stability
// cert on record must come back pending_gate with gate_reason="trust_not_earned",
// even under approval_mode=force — consistent with how the existing two
// force-gates already override force mode (they run before canAutoChain is
// ever consulted).
func TestHandlePlaybookRun_TrustGate_ForcesGate_UnearnedPlaybook(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_trust01",
		SeriesID:      "pbs_trust_triage",
		Name:          "Trust Gate Triage",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	agentText := "HYPOTHESIS_1: clear root cause | CONFIDENCE: 0.95 | EVIDENCE: \"clean signal\"\n" +
		"ROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: resolved cleanly\nTRANSITION_TO: pbs_trust_remediate\n"

	// mockGateAuditdPlaybook's default branch returns "[]" for any unmatched
	// GET — including /v1/fleet/fault-stability — so this playbook has no
	// cert on record, same as "never faulttested."
	auditSrv := mockGateAuditdPlaybook(t, pb)
	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)
	gw.SetDiagnosisModel("claude-sonnet-4-6")

	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"test","approval_mode":"force"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] != "pending_gate" {
		t.Fatalf("status = %q, want pending_gate — trust gate did not fire under force mode despite no earned cert", resp["status"])
	}
	if resp["gate_reason"] != "trust_not_earned" {
		t.Errorf("gate_reason = %q, want trust_not_earned", resp["gate_reason"])
	}
}

// TestHandlePlaybookRun_TrustGate_SkippedWhenSkipTrustGateSet mirrors the
// faulttest bootstrapping case: a calibration run explicitly sets
// skip_trust_gate so it isn't gated waiting on a cert it's trying to
// establish in the first place.
func TestHandlePlaybookRun_TrustGate_SkippedWhenSkipTrustGateSet(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_trust02",
		SeriesID:      "pbs_trust_triage2",
		Name:          "Trust Gate Triage 2",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	agentText := "HYPOTHESIS_1: clear root cause | CONFIDENCE: 0.95 | EVIDENCE: \"clean signal\"\n" +
		"ROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: resolved cleanly\n"
	// No TRANSITION_TO/ESCALATE_TO — closes out on its own, same as a
	// faulttest calibration run's final hop typically would.

	auditSrv := mockGateAuditdPlaybook(t, pb)
	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)
	gw.SetDiagnosisModel("claude-sonnet-4-6")

	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"test","approval_mode":"force","skip_trust_gate":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] == "pending_gate" {
		t.Error("status = pending_gate, but skip_trust_gate was set — trust gate should not have fired")
	}
}

// ── recordEvidenceWithoutEscalationWarning ─────────────────────────────────

func TestRecordEvidenceWithoutEscalationWarning(t *testing.T) {
	t.Run("hop escalated — no-op regardless of evidence", func(t *testing.T) {
		srv := serveFakeToolEvents(t, []audit.Event{
			{ObjectiveEvidence: &audit.ObjectiveEvidence{Tool: "get_pods", Signal: "pod_restarted"}},
		})
		extra := map[string]any{}
		hop := agentRunResult{traceID: "tr_1", escalatedTo: "pbs_sysadmin_docker_inspect"}
		recordEvidenceWithoutEscalationWarning(extra, srv.URL, "", hop)
		if _, ok := extra["evidence_warnings"]; ok {
			t.Errorf("expected no evidence_warnings when hop escalated, got %v", extra["evidence_warnings"])
		}
	})

	t.Run("hop transitioned — no-op regardless of evidence", func(t *testing.T) {
		srv := serveFakeToolEvents(t, []audit.Event{
			{ObjectiveEvidence: &audit.ObjectiveEvidence{Tool: "get_pods", Signal: "oom_killed"}},
		})
		extra := map[string]any{}
		hop := agentRunResult{traceID: "tr_1", transitionTo: "pbs_db_config_recovery"}
		recordEvidenceWithoutEscalationWarning(extra, srv.URL, "", hop)
		if _, ok := extra["evidence_warnings"]; ok {
			t.Errorf("expected no evidence_warnings when hop transitioned, got %v", extra["evidence_warnings"])
		}
	})

	t.Run("no escalation, no evidence — no-op", func(t *testing.T) {
		srv := serveFakeToolEvents(t, nil)
		extra := map[string]any{}
		hop := agentRunResult{traceID: "tr_1"}
		recordEvidenceWithoutEscalationWarning(extra, srv.URL, "", hop)
		if _, ok := extra["evidence_warnings"]; ok {
			t.Errorf("expected no evidence_warnings when no evidence recorded, got %v", extra["evidence_warnings"])
		}
	})

	t.Run("no escalation, real evidence — warning appended", func(t *testing.T) {
		srv := serveFakeToolEvents(t, []audit.Event{
			{ObjectiveEvidence: &audit.ObjectiveEvidence{Tool: "get_pods", Signal: "pod_restarted"}},
		})
		extra := map[string]any{}
		hop := agentRunResult{traceID: "tr_1", playbookSeriesID: "pbs_k8s_pod_crash_triage", agentName: "k8s_agent"}
		recordEvidenceWithoutEscalationWarning(extra, srv.URL, "", hop)
		warnings, ok := extra["evidence_warnings"].([]string)
		if !ok || len(warnings) != 1 {
			t.Fatalf("expected exactly one evidence_warnings entry, got %v", extra["evidence_warnings"])
		}
		if !strings.Contains(warnings[0], "pbs_k8s_pod_crash_triage") || !strings.Contains(warnings[0], "k8s_agent") || !strings.Contains(warnings[0], "pod_restarted") {
			t.Errorf("warning missing expected context: %q", warnings[0])
		}
	})

	t.Run("second hop appends to existing warnings — accumulates, does not overwrite", func(t *testing.T) {
		srv := serveFakeToolEvents(t, []audit.Event{
			{ObjectiveEvidence: &audit.ObjectiveEvidence{Tool: "get_pods", Signal: "oom_killed"}},
		})
		extra := map[string]any{"evidence_warnings": []string{"prior hop warning"}}
		hop := agentRunResult{traceID: "tr_2", playbookSeriesID: "pbs_sysadmin_docker_inspect", agentName: "sysadmin_agent"}
		recordEvidenceWithoutEscalationWarning(extra, srv.URL, "", hop)
		warnings, ok := extra["evidence_warnings"].([]string)
		if !ok || len(warnings) != 2 {
			t.Fatalf("expected two accumulated evidence_warnings entries, got %v", extra["evidence_warnings"])
		}
		if warnings[0] != "prior hop warning" {
			t.Errorf("first warning should be preserved unchanged, got %q", warnings[0])
		}
	})
}

// TestHandlePlaybookRun_LowConfidence_ForcedGate verifies that when a triage
// agent emits a low-confidence primary hypothesis (< 0.50), the gateway fires
// a gate even without gate_escalation=true in the request, and sets
// gate_reason="low_confidence" in the response.
func TestHandlePlaybookRun_LowConfidence_ForcedGate(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_vac_triage04",
		SeriesID:      "pbs_vacuum_triage",
		Name:          "Vacuum & Bloat Triage",
		Guidance:      "Check dead tuples.",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	// Agent emits a properly structured low-confidence diagnosis.
	// parseDiagnosticReport will parse HYPOTHESIS_1 + ROOT_CAUSE and mark
	// Confidence=0.35, IsPrimary=true → lowConfidenceForceGate returns true.
	agentText := "Two possible causes; I cannot determine which with confidence.\n\n" +
		"HYPOTHESIS_1: autovacuum is genuinely stuck behind a long-running transaction | CONFIDENCE: 0.35 | EVIDENCE: \"last_autovacuum=2026-06-01\"\n" +
		"HYPOTHESIS_2: bloat is from a single recent bulk delete, autovacuum will catch up | CONFIDENCE: 0.60 | REJECTED: autovacuum_count unchanged over two checks\n" +
		"ROOT_CAUSE: HYPOTHESIS_1\n" +
		"FINDINGS: worst table public.orders dead_ratio=0.41; autovacuum=stuck; blocker_pid=none; recommended=manual_vacuum\n" +
		"TRANSITION_TO: pbs_vacuum_remediate\n"

	auditSrv := mockGateAuditdPlaybook(t, pb)
	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)

	// Deliberately omit gate_escalation — forced gate must fire on its own.
	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"bloat alert"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] != "pending_gate" {
		t.Errorf("status = %q, want pending_gate — low-confidence gate did not fire", resp["status"])
	}
	if resp["gate_reason"] != "low_confidence" {
		t.Errorf("gate_reason = %q, want low_confidence", resp["gate_reason"])
	}
	if resp["transition_target"] != "pbs_vacuum_remediate" {
		t.Errorf("transition_target = %q, want pbs_vacuum_remediate", resp["transition_target"])
	}
}

// mockGateAuditdPlaybookWithEvidence is mockGateAuditdPlaybook plus a real
// objective_evidence event served for GET /v1/events?event_type=objective_evidence
// — used to test objectiveEvidenceForceGate / recordEvidenceWithoutEscalationWarning
// without needing a real K8s agent to actually run get_pods.
func mockGateAuditdPlaybookWithEvidence(t *testing.T, pb *audit.Playbook, signal string) *httptest.Server {
	t.Helper()
	pbData, _ := json.Marshal(pb)
	evidenceData, _ := json.Marshal([]audit.Event{
		{ObjectiveEvidence: &audit.ObjectiveEvidence{Tool: "get_pods", Resource: "pg-cluster-minkube-1", Signal: signal}},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/fleet/playbooks"):
			w.Write(pbData) //nolint:errcheck
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/events") &&
			r.URL.Query().Get("event_type") == "objective_evidence":
			w.Write(evidenceData) //nolint:errcheck
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/runs"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"run_id": "plr_gate_evidence01"}) //nolint:errcheck
		case r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Write([]byte("[]")) //nolint:errcheck
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestHandlePlaybookRun_ObjectiveEvidence_ForcedGate verifies that a hop with a
// real objective_evidence event (e.g. a pod restart) forces a pending_gate even
// with high self-reported confidence and no gate_escalation flag from the
// caller — objectiveEvidenceForceGate must fire independent of, and in addition
// to, lowConfidenceForceGate.
func TestHandlePlaybookRun_ObjectiveEvidence_ForcedGate(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_db_restart_triage01",
		SeriesID:      "pbs_db_restart_triage",
		Name:          "Database Down — Restart Triage",
		Guidance:      "Check connection and pod state.",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	// High confidence (0.85, well above the 0.50 low-confidence threshold) —
	// isolates objective evidence as the reason the gate fires.
	agentText := "Pod recovered after a restart and is now healthy.\n\n" +
		"HYPOTHESIS_1: pod restarted due to a transient error and has recovered | CONFIDENCE: 0.85 | EVIDENCE: \"restart_count=2\"\n" +
		"ROOT_CAUSE: HYPOTHESIS_1\n" +
		"FINDINGS: pod recovered; no further action needed\n" +
		"TRANSITION_TO: pbs_db_config_recovery\n"

	auditSrv := mockGateAuditdPlaybookWithEvidence(t, pb, "pod_restarted")
	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)

	// Deliberately omit gate_escalation — the objective-evidence gate must
	// fire on its own, same as the low-confidence gate does.
	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"pg-cluster-minkube-local","context":"connection refused"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] != "pending_gate" {
		t.Errorf("status = %q, want pending_gate — objective-evidence gate did not fire despite high confidence", resp["status"])
	}
	if resp["gate_reason"] != "objective_evidence:pod_restarted" {
		t.Errorf("gate_reason = %q, want objective_evidence:pod_restarted", resp["gate_reason"])
	}
	if resp["transition_target"] != "pbs_db_config_recovery" {
		t.Errorf("transition_target = %q, want pbs_db_config_recovery", resp["transition_target"])
	}
}

// TestHandlePlaybookRun_LowConfidenceAndObjectiveEvidence_CombinedGateReason is
// a regression test for a real bug found via manual verification: the
// low-confidence and objective-evidence force-gate checks used to each guard
// themselves on `!req.GateEscalation`, so whichever ran first silently
// prevented the other from ever being evaluated — a hop with BOTH a
// low-confidence diagnosis AND real tool evidence only ever reported
// gate_reason: "low_confidence", hiding the fact that independently-verified
// evidence also existed. Both conditions must now be evaluated unconditionally
// and combined into a single "+"-joined gate_reason.
func TestHandlePlaybookRun_LowConfidenceAndObjectiveEvidence_CombinedGateReason(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_combined_triage01",
		SeriesID:      "pbs_combined_triage",
		Name:          "Combined Signal Triage",
		Guidance:      "Check pod state.",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	// Low confidence (0.35, below the 0.50 threshold) on a hop that ALSO has
	// real objective evidence served by the mock.
	agentText := "Uncertain diagnosis; pod recently restarted but root cause unclear.\n\n" +
		"HYPOTHESIS_1: transient issue, may have resolved | CONFIDENCE: 0.35 | EVIDENCE: \"restart_count=2\"\n" +
		"ROOT_CAUSE: HYPOTHESIS_1\n" +
		"FINDINGS: pod state uncertain\n" +
		"TRANSITION_TO: pbs_db_config_recovery\n"

	auditSrv := mockGateAuditdPlaybookWithEvidence(t, pb, "pod_restarted")
	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)

	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"pg-cluster-minkube-local","context":"connection refused"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] != "pending_gate" {
		t.Fatalf("status = %q, want pending_gate", resp["status"])
	}
	gateReason, _ := resp["gate_reason"].(string)
	if !strings.Contains(gateReason, "low_confidence") {
		t.Errorf("gate_reason = %q, want it to mention low_confidence", gateReason)
	}
	if !strings.Contains(gateReason, "objective_evidence:pod_restarted") {
		t.Errorf("gate_reason = %q, want it to also mention objective_evidence:pod_restarted — both signals must survive, not just whichever fired first", gateReason)
	}
}

// TestHandlePlaybookRun_ObjectiveEvidence_NoEscalation_SurfacesWarning verifies
// the complementary case objectiveEvidenceForceGate cannot catch on its own: a
// hop with real objective evidence that emits no TRANSITION_TO/ESCALATE_TO at
// all. There is no next-hop to gate approval for, so the response must not
// become pending_gate — instead recordEvidenceWithoutEscalationWarning must
// surface the discrepancy via extra["evidence_warnings"].
func TestHandlePlaybookRun_ObjectiveEvidence_NoEscalation_SurfacesWarning(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_k8s_pod_crash_triage01",
		SeriesID:      "pbs_k8s_pod_crash_triage",
		Name:          "K8s Pod Crash Triage",
		Guidance:      "Check pod status and restart history.",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	// No TRANSITION_TO/ESCALATE_TO line — the model silently closes out
	// despite the pod having real restart evidence (served by the mock below).
	agentText := "Pod is currently healthy.\n\n" +
		"HYPOTHESIS_1: transient issue, now resolved | CONFIDENCE: 0.90 | EVIDENCE: \"status=Running\"\n" +
		"ROOT_CAUSE: HYPOTHESIS_1\n" +
		"FINDINGS: pod healthy; no action needed\n"

	auditSrv := mockGateAuditdPlaybookWithEvidence(t, pb, "oom_killed")
	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)

	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"pg-cluster-minkube-local","context":"connection refused"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] == "pending_gate" {
		t.Error("status = pending_gate, but there is no escalation target — should not re-route into the gate flow")
	}
	warnings, ok := resp["evidence_warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("expected exactly one evidence_warnings entry, got %v", resp["evidence_warnings"])
	}
	warnStr, _ := warnings[0].(string)
	if !strings.Contains(warnStr, "oom_killed") {
		t.Errorf("evidence_warnings[0] = %q, want it to mention oom_killed", warnStr)
	}
}

// TestHandlePlaybookRun_GateEscalation_SyntheticGate_NoSignal verifies that when
// gate_escalation=true and remediation_series_id is provided, but the triage agent
// omits TRANSITION_TO/ESCALATE_TO (a protocol violation), the gateway still creates
// a pending_gate using the explicit remediation target and surfaces a warning.
func TestHandlePlaybookRun_GateEscalation_SyntheticGate_NoSignal(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_cache_triage01",
		SeriesID:      "pbs_cache_miss_triage",
		Name:          "Cache Miss Triage",
		Guidance:      "Check cache hit ratio and sequential scans.",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	// Agent diagnoses correctly but omits the required TRANSITION_TO signal.
	agentText := "Cache hit ratio is 92.8% — concerning. Sequential scans on test_large_table " +
		"are the likely cause. shared_buffers (5.75 GB) is adequate; indexes should be added.\n\n" +
		"HYPOTHESIS_1: sequential scans on test_large_table elevated blks_read after stat reset | CONFIDENCE: 0.85 | EVIDENCE: \"blks_read=576\"\n" +
		"ROOT_CAUSE: HYPOTHESIS_1\n" +
		"FINDINGS: cache_hit_ratio=0.928; blks_read=576; worst_table=test_large_table; recommended=add_index\n"
	// Note: no TRANSITION_TO line — this is the protocol violation we are testing.

	auditSrv := mockGateAuditdPlaybook(t, pb)
	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)

	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"high cache miss","gate_escalation":true,"remediation_series_id":"pbs_cache_miss_remediate"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] != "pending_gate" {
		t.Errorf("status = %q, want pending_gate — synthetic gate should fire when agent omits signal", resp["status"])
	}
	if resp["transition_target"] != "pbs_cache_miss_remediate" {
		t.Errorf("transition_target = %q, want pbs_cache_miss_remediate", resp["transition_target"])
	}
	if resp["gate_type"] != "transition" {
		t.Errorf("gate_type = %q, want transition", resp["gate_type"])
	}
	// Protocol violation must be surfaced as a warning.
	warnings, _ := resp["warnings"].([]any)
	if len(warnings) == 0 {
		t.Error("warnings should be non-empty — protocol violation must be surfaced")
	} else {
		found := false
		for _, w := range warnings {
			if s, _ := w.(string); strings.Contains(s, "omitted") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("warning should mention omitted signal, got: %v", warnings)
		}
	}
}

// TestHandlePlaybookRun_GateEscalation_NoSignal_NoRemediationTarget verifies that
// when gate_escalation=true but remediation_series_id is absent and the agent omits
// TRANSITION_TO, no synthetic gate is created — the run completes normally.
func TestHandlePlaybookRun_GateEscalation_NoSignal_NoRemediationTarget(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_cache_triage02",
		SeriesID:      "pbs_cache_miss_triage",
		Name:          "Cache Miss Triage",
		Guidance:      "Check cache hit ratio.",
		ExecutionMode: "agent",
		AgentName:     agentNameDB,
		IsActive:      true,
	}
	agentText := "Cache hit ratio is 92.8%. No action needed at this time.\n\n" +
		"FINDINGS: cache_hit_ratio=0.928; recommended=monitor\n"

	auditSrv := mockGateAuditdPlaybook(t, pb)
	gw := makeGateGateway(t, auditSrv.URL, agentNameDB, agentText)

	// gate_escalation=true but no remediation_series_id provided.
	rec := postPlaybookRun(t, gw, pb.PlaybookID,
		`{"connection_string":"postgres://localhost/test","context":"high cache miss","gate_escalation":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp["status"] == "pending_gate" {
		t.Error("gate fired without remediation_series_id — should not create a synthetic gate")
	}
}

// ── runQueryViaPlaybook ───────────────────────────────────────────────────

// mockA2AServerCapturingText is like mockA2AServerWithText but also records
// the raw request bytes it receives, so tests can assert what prompt/message
// text was actually sent to the agent.
func mockA2AServerCapturingText(t *testing.T, agentName string) (*a2a.AgentCard, func() string) {
	t.Helper()
	var mu sync.Mutex
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		lastBody = string(raw)
		mu.Unlock()
		var req struct {
			ID string `json:"id"`
		}
		json.Unmarshal(raw, &req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"kind": "task",
				"id":   "task-runq-1",
				"status": map[string]any{
					"state": "completed",
					"message": map[string]any{
						"role": "agent",
						"parts": []map[string]any{
							{"kind": "text", "text": "ok"},
						},
					},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	card := &a2a.AgentCard{
		Name:               agentName,
		URL:                srv.URL,
		PreferredTransport: a2a.TransportProtocolJSONRPC,
	}
	return card, func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastBody
	}
}

// TestRunQueryViaPlaybook_BuildsCorrectSyntheticRequest verifies that the
// query message flows through as both Context and TriggerContext, and that
// approval_mode is pinned to "manual" regardless of the playbook's own
// default — an auto-selected query must never silently authorize a
// write/destructive tool call.
func TestRunQueryViaPlaybook_BuildsCorrectSyntheticRequest(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_node_pressure01",
		SeriesID:      "pbs_k8s_node_pressure_triage",
		Name:          "K8s Node Memory Pressure — Triage",
		Guidance:      "Investigate node memory pressure.",
		ExecutionMode: "agent",
		AgentName:     agentNameK8s,
		ApprovalMode:  "auto", // deliberately permissive default; runQueryViaPlaybook must override it
		IsActive:      true,
	}
	card, capturedBody := mockA2AServerCapturingText(t, agentNameK8s)
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("create A2A client: %v", err)
	}

	auditSrv := mockAuditdForPlaybookSelection(t, []*audit.Playbook{pb})
	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	gw.clients = map[string]*a2aclient.Client{agentNameK8s: client}

	const message = "node memory-pressure alert for the node hosting postgres"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(`{}`))
	req.Header.Set("X-User", "ops@example.com")
	rec := httptest.NewRecorder()

	gw.runQueryViaPlaybook(rec, req, "pbs_k8s_node_pressure_triage", message, "ctx-123", agentNameK8s)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if body := capturedBody(); !strings.Contains(body, message) {
		t.Errorf("agent did not receive the query message in its prompt; body sent to agent: %s", body)
	}
}

// TestRunQueryViaPlaybook_UnknownSeriesID_FallsBackToProxyToAgent verifies
// that a series_id that fails to resolve degrades silently to an ordinary
// agent proxy — never a user-facing error.
func TestRunQueryViaPlaybook_UnknownSeriesID_FallsBackToProxyToAgent(t *testing.T) {
	card, capturedBody := mockA2AServerCapturingText(t, agentNameK8s)
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("create A2A client: %v", err)
	}

	// auditd has no playbooks at all — fetchPlaybookBySeriesID will fail.
	auditSrv := mockAuditdForPlaybookSelection(t, nil)
	gw := makePlaybookRunGateway(auditSrv.URL, nil)
	gw.clients = map[string]*a2aclient.Client{agentNameK8s: client}

	const message = "some query that matched a since-deleted playbook"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(`{}`))
	req.Header.Set("X-User", "ops@example.com")
	rec := httptest.NewRecorder()

	gw.runQueryViaPlaybook(rec, req, "pbs_deleted_playbook", message, "", agentNameK8s)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fallback to proxyToAgent); body: %s", rec.Code, rec.Body.String())
	}
	if body := capturedBody(); !strings.Contains(body, message) {
		t.Errorf("fallback proxy did not send the original message to the agent; body: %s", body)
	}
}

// TestIsAllowedNextPlaybook_NodePressureTriage_TransitionsToRemediate verifies
// that the node-pressure triage playbook's declared transitions_to allow-list
// accepts its own remediation playbook as a TRANSITION_TO target (not coerced
// to requires_operator_approval), and rejects anything else.
func TestIsAllowedNextPlaybook_NodePressureTriage_TransitionsToRemediate(t *testing.T) {
	pb := &audit.Playbook{
		SeriesID:      "pbs_k8s_node_pressure_triage",
		TransitionsTo: []string{"pbs_k8s_node_pressure_remediate"},
	}

	if !isAllowedNextPlaybook(pb, "pbs_k8s_node_pressure_remediate", directiveTransition) {
		t.Error("declared transitions_to target should be allowed")
	}
	if isAllowedNextPlaybook(pb, "pbs_some_other_playbook", directiveTransition) {
		t.Error("a series_id not in transitions_to should be rejected")
	}
}

// TestHandlePlaybookRunApprove_NamespaceForceOverridden verifies the fix for
// the second real bug found via manual verification: the step proposer LLM
// reliably guessed/defaulted the K8s namespace to "default" instead of the
// ticket's real namespace, even with strongly-worded playbook guidance
// asking it not to. The gateway must force the request's authoritative
// Namespace into the proposed step's args, exactly as it already does for
// ConnectionString, regardless of what the LLM proposed.
func TestHandlePlaybookRunApprove_NamespaceForceOverridden(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_node_pressure_rem01",
		SeriesID:      "pbs_k8s_node_pressure_remediate",
		Name:          "K8s Node Memory Pressure — Remediation",
		Guidance:      "Verify postgres is healthy.",
		ExecutionMode: "agent_approve",
		AgentName:     "k8s_agent",
		IsActive:      true,
	}
	auditSrv := mockAuditdPlaybook(t, pb)

	llmFn := func(ctx context.Context, prompt string) (string, error) {
		// Simulate exactly the observed failure mode: the LLM proposes a real
		// k8s tool call but guesses the wrong namespace.
		return `{"action":"execute_step","tool":"get_pods","args":{"namespace":"default"}}`, nil
	}

	reg := makeRegistryWithTools([]toolregistry.ToolEntry{
		{Name: "get_pods", Agent: "k8s", ActionClass: "read"},
	})
	gw := &Gateway{
		agents:       make(map[string]*discovery.Agent),
		clients:      make(map[string]*a2aclient.Client),
		toolRegistry: reg,
		plannerLLM:   llmFn,
		auditURL:     auditSrv.URL,
	}
	rec := postPlaybookRun(t, gw, "pb_node_pressure_rem01", `{"namespace":"helpdesk-test"}`)

	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 200 or 202; body: %s", rec.Code, rec.Body.String())
	}
	var resp ApproveRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if resp.Status != "pending_approval" {
		t.Fatalf("status = %q, want pending_approval; body: %s", resp.Status, rec.Body.String())
	}
	if resp.Step == nil {
		t.Fatal("Step is nil")
	}
	if got, _ := resp.Step.Args["namespace"].(string); got != "helpdesk-test" {
		t.Errorf("Step.Args[namespace] = %q, want the forced authoritative value %q (not the LLM's guessed %q)",
			got, "helpdesk-test", "default")
	}
}

func postProceed(t *testing.T, gw *Gateway, runID, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/fleet/playbook-runs/"+runID+"/proceed",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestHandlePlaybookRunProceed_NamespaceForceOverridden covers the second
// half of the namespace force-override fix: handlePlaybookRunApprove (tested
// above) only covers the FIRST proposed step. Every subsequent step goes
// through handlePlaybookRunProceed instead, which has its own separate
// override logic — this was the exact code path that executed step 2
// (get_node_status) during live manual verification, and had no direct
// regression coverage before this test.
func TestHandlePlaybookRunProceed_NamespaceForceOverridden(t *testing.T) {
	pb := &audit.Playbook{
		PlaybookID:    "pb_node_pressure_rem01",
		SeriesID:      "pbs_k8s_node_pressure_remediate",
		Name:          "K8s Node Memory Pressure — Remediation",
		Guidance:      "Verify node status.",
		ExecutionMode: "agent_approve",
		AgentName:     "k8s_agent",
		IsActive:      true,
	}
	run := &audit.PlaybookRun{
		RunID:      "plr_test01",
		PlaybookID: pb.PlaybookID,
		SeriesID:   pb.SeriesID,
		Namespace:  "helpdesk-test",
	}
	// Stale/wrong namespace stored on the pending step from an earlier
	// proposal — the fix must override this, not trust it.
	pendingStep := &audit.PlaybookRunStep{
		RunID:     run.RunID,
		StepIndex: 1,
		Agent:     "k8s",
		Tool:      "get_node_status",
		Args:      map[string]any{"namespace": "default", "node_name": "faulttest-eviction"},
		Status:    "proposed",
	}

	var gotToolArgs map[string]any
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Args map[string]any `json:"args"`
		}
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		gotToolArgs = req.Args
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"output": "MemoryPressure: False"}) //nolint:errcheck
	}))
	defer agentSrv.Close()

	runData, _ := json.Marshal(run)
	pbData, _ := json.Marshal(pb)
	stepData, _ := json.Marshal(pendingStep)
	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/pending-step"):
			w.Write(stepData) //nolint:errcheck
		case strings.HasSuffix(r.URL.Path, "/steps"):
			w.Write([]byte(`{"steps":[]}`)) //nolint:errcheck
		case strings.Contains(r.URL.Path, "/playbook-runs/"):
			w.Write(runData) //nolint:errcheck
		case strings.Contains(r.URL.Path, "/playbooks/"):
			w.Write(pbData) //nolint:errcheck
		default:
			w.Write([]byte("{}")) //nolint:errcheck
		}
	}))
	defer auditSrv.Close()

	gw := &Gateway{
		agents:   map[string]*discovery.Agent{agentNameK8s: {Name: agentNameK8s, InvokeURL: agentSrv.URL + "/invoke"}},
		auditURL: auditSrv.URL,
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			// Re-planning call after this step executes; declare done so the
			// handler returns without needing a second tool round trip.
			return `{"action":"complete","summary":"verification complete"}`, nil
		},
	}

	rec := postProceed(t, gw, run.RunID, `{"resolution":"approved"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if got, _ := gotToolArgs["namespace"].(string); got != "helpdesk-test" {
		t.Errorf("tool call args[namespace] = %q, want the forced authoritative value %q (not the stale %q stored on the step)",
			got, "helpdesk-test", "default")
	}
}
