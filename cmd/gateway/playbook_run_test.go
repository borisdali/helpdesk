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
