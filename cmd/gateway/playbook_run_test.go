package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/discovery"
	"helpdesk/internal/identity"
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
	if !strings.Contains(prompt, "ESCALATE_TO:") {
		t.Error("prompt should mention ESCALATE_TO: line")
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
		pb, "pbs_db_config_recovery", "connection pool exhaustion detected")

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
		identity.ResolvedPrincipal{}, pb, "pbs_db_config_recovery", "")

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
		identity.ResolvedPrincipal{}, pb, "pbs_next", "")

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
		identity.ResolvedPrincipal{}, pb, "pbs_next", "")
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
				Name:          "Test Postgres",
				ConnectionString: "host=localhost port=5433 dbname=postgres",
				VMName:        "test-host",
			},
			"pg-cluster-minikube": {
				Name:       "PG Cluster Minikube",
				ConnectionString: "host=pg-cluster-minikube port=5432 dbname=postgres",
				K8sCluster: "minikube",
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
