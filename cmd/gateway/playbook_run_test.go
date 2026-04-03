package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"helpdesk/internal/audit"
	"helpdesk/internal/discovery"
	"helpdesk/internal/infra"
	"helpdesk/internal/toolregistry"

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
	prompt := assembleTriagePrompt(pb, PlaybookRunRequest{})

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
	prompt := assembleTriagePrompt(pb, PlaybookRunRequest{})

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
	prompt := assembleTriagePrompt(pb, req)

	if !strings.Contains(prompt, "postgres://prod-db.example.com/mydb") {
		t.Error("prompt does not contain connection string")
	}
	if !strings.Contains(prompt, "connection refused") {
		t.Error("prompt does not contain operator context")
	}
}

func TestAssembleTriagePrompt_NoEscalatesTo(t *testing.T) {
	pb := &audit.Playbook{Name: "PITR Recovery"}
	prompt := assembleTriagePrompt(pb, PlaybookRunRequest{})

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
	prompt := assembleTriagePrompt(pb, req)

	if !strings.Contains(prompt, "Prior Investigation Findings") {
		t.Error("prompt missing 'Prior Investigation Findings' section")
	}
	if !strings.Contains(prompt, "WAL corruption") {
		t.Error("prompt should contain the prior findings text")
	}
}

func TestAssembleTriagePrompt_NoPriorFindings(t *testing.T) {
	pb := &audit.Playbook{Name: "Restart Triage"}
	prompt := assembleTriagePrompt(pb, PlaybookRunRequest{})

	if strings.Contains(prompt, "Prior Investigation Findings") {
		t.Error("prompt should not have prior findings section when PriorFindings is empty")
	}
}

func TestAssembleTriagePrompt_ResponseProtocol(t *testing.T) {
	pb := &audit.Playbook{Name: "Triage"}
	prompt := assembleTriagePrompt(pb, PlaybookRunRequest{})

	if !strings.Contains(prompt, "Response Protocol") {
		t.Error("prompt missing Response Protocol section")
	}
	if !strings.Contains(prompt, "FINDINGS:") {
		t.Error("prompt should instruct agent to emit FINDINGS: line")
	}
	if !strings.Contains(prompt, "ESCALATE_TO:") {
		t.Error("prompt should mention ESCALATE_TO: line")
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

// TestHandlePlaybookRun_FleetMode_NoLLM verifies that a fleet-mode playbook run
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
