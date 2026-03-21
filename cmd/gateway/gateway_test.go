package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"

	"helpdesk/internal/audit"
	"helpdesk/internal/discovery"
	"helpdesk/internal/fleet"
	"helpdesk/internal/infra"
	"helpdesk/internal/toolregistry"
)

// --- extractText tests ---

func TestExtractText_Empty(t *testing.T) {
	got := extractText(nil)
	if got != "" {
		t.Errorf("extractText(nil) = %q, want empty", got)
	}
}

func TestExtractText_SingleTextPart(t *testing.T) {
	parts := a2a.ContentParts{a2a.TextPart{Text: "hello"}}
	got := extractText(parts)
	if got != "hello" {
		t.Errorf("extractText = %q, want %q", got, "hello")
	}
}

func TestExtractText_MultipleTextParts(t *testing.T) {
	parts := a2a.ContentParts{
		a2a.TextPart{Text: "line1"},
		a2a.TextPart{Text: "line2"},
	}
	got := extractText(parts)
	if got != "line1\nline2" {
		t.Errorf("extractText = %q, want %q", got, "line1\nline2")
	}
}

func TestExtractText_NonTextPartsIgnored(t *testing.T) {
	parts := a2a.ContentParts{
		a2a.TextPart{Text: "hello"},
		a2a.DataPart{Data: map[string]any{"key": "val"}},
		a2a.TextPart{Text: "world"},
	}
	got := extractText(parts)
	if got != "hello\nworld" {
		t.Errorf("extractText = %q, want %q", got, "hello\nworld")
	}
}

// --- buildToolPrompt tests ---

func TestBuildToolPrompt_NoArgs(t *testing.T) {
	got := buildToolPrompt("check_connection", nil)
	if got != "Call the check_connection tool." {
		t.Errorf("buildToolPrompt = %q", got)
	}
}

func TestBuildToolPrompt_WithArgs(t *testing.T) {
	got := buildToolPrompt("get_pods", map[string]any{
		"namespace": "default",
	})
	if !strings.Contains(got, "Call the get_pods tool") {
		t.Errorf("missing tool name in prompt: %q", got)
	}
	if !strings.Contains(got, "namespace=default") {
		t.Errorf("missing arg in prompt: %q", got)
	}
}

// --- extractResponse tests ---

func TestExtractResponse_TaskWithStatusMessage(t *testing.T) {
	task := &a2a.Task{
		ID: "task-1",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateCompleted,
			Message: &a2a.Message{
				Role:  a2a.MessageRoleAgent,
				Parts: a2a.ContentParts{a2a.TextPart{Text: "done"}},
			},
		},
	}

	resp := extractResponse(task)
	if resp.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", resp.TaskID, "task-1")
	}
	if resp.State != "completed" {
		t.Errorf("State = %q, want %q", resp.State, "completed")
	}
	if resp.Text != "done" {
		t.Errorf("Text = %q, want %q", resp.Text, "done")
	}
}

func TestExtractResponse_TaskWithHistory(t *testing.T) {
	task := &a2a.Task{
		ID: "task-2",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateCompleted,
		},
		History: []*a2a.Message{
			{Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.TextPart{Text: "request"}}},
			{Role: a2a.MessageRoleAgent, Parts: a2a.ContentParts{a2a.TextPart{Text: "response"}}},
		},
	}

	resp := extractResponse(task)
	if resp.Text != "response" {
		t.Errorf("Text = %q, want %q (from history)", resp.Text, "response")
	}
}

func TestExtractResponse_TaskWithArtifactFallback(t *testing.T) {
	task := &a2a.Task{
		ID: "task-3",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateCompleted,
		},
		Artifacts: []*a2a.Artifact{
			{
				ID:    "art-1",
				Name:  "report",
				Parts: a2a.ContentParts{a2a.TextPart{Text: "artifact text"}},
			},
		},
	}

	resp := extractResponse(task)
	if resp.Text != "artifact text" {
		t.Errorf("Text = %q, want %q (from artifact fallback)", resp.Text, "artifact text")
	}
	if len(resp.Artifacts) != 1 {
		t.Errorf("Artifacts len = %d, want 1", len(resp.Artifacts))
	}
}

func TestExtractResponse_Message(t *testing.T) {
	msg := &a2a.Message{
		Role:  a2a.MessageRoleAgent,
		Parts: a2a.ContentParts{a2a.TextPart{Text: "direct message"}},
	}

	resp := extractResponse(msg)
	if resp.Text != "direct message" {
		t.Errorf("Text = %q, want %q", resp.Text, "direct message")
	}
	if resp.TaskID != "" {
		t.Errorf("TaskID = %q, want empty for Message type", resp.TaskID)
	}
}

// --- extractResponse: failed task state ---

func TestExtractResponse_FailedTaskState(t *testing.T) {
	task := &a2a.Task{
		Status: a2a.TaskStatus{
			State:   a2a.TaskStateFailed,
			Message: &a2a.Message{Parts: a2a.ContentParts{a2a.TextPart{Text: "runner crashed"}}},
		},
	}
	resp := extractResponse(task)
	if resp.State != string(a2a.TaskStateFailed) {
		t.Errorf("State = %q, want %q", resp.State, a2a.TaskStateFailed)
	}
	if resp.Text != "runner crashed" {
		t.Errorf("Text = %q, want %q", resp.Text, "runner crashed")
	}
}

// --- isPolicyDenial ---

func TestIsPolicyDenial_Positive(t *testing.T) {
	cases := []string{
		"policy denied: purpose not allowed",
		"Policy Denied: read access blocked",
		"I cannot proceed: policy denied by pii-data-protection",
		"Access to database foo: DENIED\npolicy denied: ...",
	}
	for _, c := range cases {
		if !isPolicyDenial(c) {
			t.Errorf("isPolicyDenial(%q) = false, want true", c)
		}
	}
}

func TestIsPolicyDenial_Negative(t *testing.T) {
	cases := []string{
		"VACUUM completed successfully",
		"connected to postgres 16.1",
		"I don't have a run_sql tool available",
		"",
	}
	for _, c := range cases {
		if isPolicyDenial(c) {
			t.Errorf("isPolicyDenial(%q) = true, want false", c)
		}
	}
}

// --- isToolError ---

func TestIsToolError_Positive(t *testing.T) {
	cases := []string{
		"---\nERROR — get_server_info failed for pg-cluster\n\npsql failed\n---",
		"---\nERROR — check_connection failed for fault-test-db\n\nConnection refused\n---",
		"---\nERROR — get_session_info failed for alloydb-on-vm\n\nsome error\n---\n\nThis means: ...",
	}
	for _, c := range cases {
		if !isToolError(c) {
			t.Errorf("isToolError(%q) = false, want true", c)
		}
	}
}

func TestIsToolError_Negative(t *testing.T) {
	cases := []string{
		"PostgreSQL 16.3 on aarch64",
		"policy denied: purpose not allowed",
		"ERROR: permission denied for table foo", // postgres error, not errorResult marker
		"",
	}
	for _, c := range cases {
		if isToolError(c) {
			t.Errorf("isToolError(%q) = true, want false", c)
		}
	}
}

// --- Handler validation tests ---

func TestHandleResearch_MissingQuery(t *testing.T) {
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client),
	}

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	// Test empty body
	req := httptest.NewRequest(http.MethodPost, "/api/v1/research", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "query is required") {
		t.Errorf("body = %q, want error about missing query", rec.Body.String())
	}
}

func TestHandleResearch_InvalidJSON(t *testing.T) {
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client),
	}

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/research", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "invalid JSON") {
		t.Errorf("body = %q, want error about invalid JSON", rec.Body.String())
	}
}

func TestHandleResearch_AgentNotAvailable(t *testing.T) {
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client), // empty - no research agent
	}

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/research", strings.NewReader(`{"query":"test query"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if !strings.Contains(rec.Body.String(), "not available") {
		t.Errorf("body = %q, want error about agent not available", rec.Body.String())
	}
}

// --- Tool registry handler tests ---

func makeRegistryWithTools(entries []toolregistry.ToolEntry) *toolregistry.Registry {
	return toolregistry.New(entries)
}

func TestHandleDBTool_UnknownTool(t *testing.T) {
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{
		{Name: "check_connection", Agent: "database", ActionClass: "read"},
		{Name: "get_server_info", Agent: "database", ActionClass: "read"},
	})
	gw := &Gateway{
		agents:       make(map[string]*discovery.Agent),
		clients:      make(map[string]*a2aclient.Client),
		toolRegistry: reg,
	}

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/db/no_such_tool", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (bad request for unknown tool)", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "unknown tool") {
		t.Errorf("body = %q, want error mentioning unknown tool", rec.Body.String())
	}
}

func TestHandleDBTool_KnownTool(t *testing.T) {
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{
		{Name: "check_connection", Agent: "database", ActionClass: "read"},
	})
	gw := &Gateway{
		agents:       make(map[string]*discovery.Agent),
		clients:      make(map[string]*a2aclient.Client), // no actual agent — will get 502
		toolRegistry: reg,
	}

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/db/check_connection", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	// Registry validation passes — should reach the agent lookup (502 because no agent configured).
	if rec.Code == http.StatusBadRequest {
		t.Errorf("status = %d (BadRequest), want registry validation to pass for known tool", rec.Code)
	}
	// Expect 502 because the DB agent client is not set up.
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d (agent not available after registry passes)", rec.Code, http.StatusBadGateway)
	}
}

// --- Planner helper tests ---

func TestBuildPlannerInfraContext_Restricted(t *testing.T) {
	cfg := &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-users-db": {
				Name:        "Users Production Database",
				Tags:        []string{"production"},
				Sensitivity: []string{"pii"},
			},
			"staging-db": {
				Name: "Staging Database",
				Tags: []string{"staging"},
			},
		},
	}

	summary, restricted := buildPlannerInfraContext(cfg)

	if !strings.Contains(summary, "[RESTRICTED]") {
		t.Error("summary should contain [RESTRICTED] for pii server")
	}
	if !strings.Contains(summary, "prod-users-db") {
		t.Error("summary should contain prod-users-db")
	}
	if len(restricted) != 1 {
		t.Errorf("restricted len = %d, want 1", len(restricted))
	}
	if restricted[0] != "prod-users-db" {
		t.Errorf("restricted[0] = %q, want %q", restricted[0], "prod-users-db")
	}
}

func TestBuildPlannerToolCatalog(t *testing.T) {
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{
		{Name: "check_connection", Agent: "database", ActionClass: "read", Description: "Test DB connectivity"},
		{Name: "cancel_query", Agent: "database", ActionClass: "write", Description: "Cancel a running query"},
	})

	catalog := buildPlannerToolCatalog(reg)

	if !strings.Contains(catalog, "check_connection") {
		t.Error("catalog should contain check_connection")
	}
	if !strings.Contains(catalog, "cancel_query") {
		t.Error("catalog should contain cancel_query")
	}
	if !strings.Contains(catalog, "agent=database") {
		t.Error("catalog should contain agent=database")
	}
	if !strings.Contains(catalog, "class=read") {
		t.Error("catalog should contain class=read")
	}
	if !strings.Contains(catalog, "class=write") {
		t.Error("catalog should contain class=write")
	}
}

// --- resolveTargetsFromInfra tests ---

func makeTestInfra() *infra.Config {
	return &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-db-1": {Tags: []string{"production"}},
			"prod-db-2": {Tags: []string{"production"}},
			"staging-db": {Tags: []string{"staging"}},
			"dev-db":     {Tags: []string{"development"}},
		},
	}
}

func TestResolveTargetsFromInfra_TagMatch(t *testing.T) {
	cfg := makeTestInfra()
	targets := fleet.Targets{Tags: []string{"production"}}
	got := resolveTargetsFromInfra(cfg, targets)
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 production servers", len(got))
	}
	for _, s := range got {
		if s != "prod-db-1" && s != "prod-db-2" {
			t.Errorf("unexpected server %q in result", s)
		}
	}
}

func TestResolveTargetsFromInfra_NameMatch(t *testing.T) {
	cfg := makeTestInfra()
	targets := fleet.Targets{Names: []string{"staging-db", "dev-db"}}
	got := resolveTargetsFromInfra(cfg, targets)
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestResolveTargetsFromInfra_Exclude(t *testing.T) {
	cfg := makeTestInfra()
	targets := fleet.Targets{Tags: []string{"production"}, Exclude: []string{"prod-db-1"}}
	got := resolveTargetsFromInfra(cfg, targets)
	if len(got) != 1 {
		t.Errorf("len = %d, want 1 (prod-db-1 excluded)", len(got))
	}
	if got[0] != "prod-db-2" {
		t.Errorf("got[0] = %q, want prod-db-2", got[0])
	}
}

func TestResolveTargetsFromInfra_AllServers(t *testing.T) {
	cfg := makeTestInfra()
	targets := fleet.Targets{} // no filters → all servers
	got := resolveTargetsFromInfra(cfg, targets)
	if len(got) != 4 {
		t.Errorf("len = %d, want 4 (all servers)", len(got))
	}
}

func TestResolveTargetsFromInfra_NilConfig(t *testing.T) {
	got := resolveTargetsFromInfra(nil, fleet.Targets{Tags: []string{"production"}})
	if got != nil {
		t.Errorf("got %v, want nil for nil config", got)
	}
}

// --- stripMarkdownFences tests ---

func TestStripMarkdownFences_NoFences(t *testing.T) {
	input := `{"key": "value"}`
	got := stripMarkdownFences(input)
	if got != input {
		t.Errorf("stripMarkdownFences(%q) = %q, want unchanged", input, got)
	}
}

func TestStripMarkdownFences_JSONFences(t *testing.T) {
	input := "```json\n{\"key\": \"value\"}\n```"
	got := stripMarkdownFences(input)
	want := `{"key": "value"}`
	if got != want {
		t.Errorf("stripMarkdownFences = %q, want %q", got, want)
	}
}

func TestStripMarkdownFences_PlainFences(t *testing.T) {
	input := "```\n{\"key\": \"value\"}\n```"
	got := stripMarkdownFences(input)
	want := `{"key": "value"}`
	if got != want {
		t.Errorf("stripMarkdownFences = %q, want %q", got, want)
	}
}

func TestStripMarkdownFences_EmptyString(t *testing.T) {
	got := stripMarkdownFences("")
	if got != "" {
		t.Errorf("stripMarkdownFences(%q) = %q, want empty", "", got)
	}
}

// --- handleFleetPlan handler tests ---

// makePlannerGateway builds a Gateway wired for fleet plan tests.
// reg may be nil (simulates missing tool registry).
// llmFn may be nil (simulates missing LLM — the handler won't reach it when infra/registry are absent).
func makePlannerGateway(cfg *infra.Config, reg *toolregistry.Registry, llmFn func(context.Context, string) (string, error)) *Gateway {
	gw := &Gateway{
		agents:       make(map[string]*discovery.Agent),
		clients:      make(map[string]*a2aclient.Client),
		infra:        cfg,
		toolRegistry: reg,
		plannerLLM:   llmFn,
	}
	return gw
}

func postFleetPlan(t *testing.T, gw *Gateway, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandleFleetPlan_MissingDescription(t *testing.T) {
	cfg := makeTestInfra()
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{{Name: "check_connection", Agent: "database", ActionClass: "read"}})
	gw := makePlannerGateway(cfg, reg, nil)

	rec := postFleetPlan(t, gw, `{}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "description") {
		t.Errorf("body = %q, want mention of description", rec.Body.String())
	}
}

func TestHandleFleetPlan_MissingInfra(t *testing.T) {
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{{Name: "check_connection", Agent: "database", ActionClass: "read"}})
	gw := makePlannerGateway(nil, reg, nil) // nil infra

	rec := postFleetPlan(t, gw, `{"description":"vacuum all prod databases"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "infrastructure") {
		t.Errorf("body = %q, want mention of infrastructure", rec.Body.String())
	}
}

func TestHandleFleetPlan_MissingRegistry(t *testing.T) {
	cfg := makeTestInfra()
	gw := makePlannerGateway(cfg, nil, nil) // nil registry

	rec := postFleetPlan(t, gw, `{"description":"vacuum all prod databases"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "tool registry") {
		t.Errorf("body = %q, want mention of tool registry", rec.Body.String())
	}
}

func TestHandleFleetPlan_LLMError(t *testing.T) {
	cfg := makeTestInfra()
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{{Name: "check_connection", Agent: "database", ActionClass: "read"}})
	gw := makePlannerGateway(cfg, reg, func(_ context.Context, _ string) (string, error) {
		return "", fmt.Errorf("upstream timeout")
	})

	rec := postFleetPlan(t, gw, `{"description":"check all prod databases"}`)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestHandleFleetPlan_MalformedLLMResponse(t *testing.T) {
	cfg := makeTestInfra()
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{{Name: "check_connection", Agent: "database", ActionClass: "read"}})
	gw := makePlannerGateway(cfg, reg, func(_ context.Context, _ string) (string, error) {
		return "this is not json at all", nil
	})

	rec := postFleetPlan(t, gw, `{"description":"check all prod databases"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
}

func TestHandleFleetPlan_UnknownTool(t *testing.T) {
	cfg := makeTestInfra()
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{
		{Name: "check_connection", Agent: "database", ActionClass: "read"},
	})
	// LLM returns a job with a tool that is not in the registry.
	llmResp := map[string]any{
		"job_def": map[string]any{
			"name": "test-job",
			"change": map[string]any{
				"steps": []any{
					map[string]any{"agent": "database", "tool": "run_sql", "on_failure": "stop"},
				},
			},
			"targets":  map[string]any{"tags": []string{"production"}},
			"strategy": map[string]any{"canary_count": 1},
		},
		"planner_notes": "test",
	}
	raw, _ := json.Marshal(llmResp)
	gw := makePlannerGateway(cfg, reg, func(_ context.Context, _ string) (string, error) {
		return string(raw), nil
	})

	rec := postFleetPlan(t, gw, `{"description":"run sql on all dbs"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
	if !strings.Contains(rec.Body.String(), "unknown tool") {
		t.Errorf("body = %q, want mention of unknown tool", rec.Body.String())
	}
}

func TestHandleFleetPlan_RestrictedServer(t *testing.T) {
	// Infra with one restricted server.
	cfg := &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-users-db": {Tags: []string{"production"}, Sensitivity: []string{"pii"}},
			"staging-db":    {Tags: []string{"staging"}},
		},
	}
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{
		{Name: "check_connection", Agent: "database", ActionClass: "read"},
	})
	// LLM targets production (which includes the restricted server) without excluding it.
	llmResp := map[string]any{
		"job_def": map[string]any{
			"name": "test-job",
			"change": map[string]any{
				"steps": []any{
					map[string]any{"agent": "database", "tool": "check_connection", "on_failure": "stop"},
				},
			},
			"targets":  map[string]any{"tags": []string{"production"}},
			"strategy": map[string]any{"canary_count": 1},
		},
		"planner_notes": "test",
	}
	raw, _ := json.Marshal(llmResp)
	gw := makePlannerGateway(cfg, reg, func(_ context.Context, _ string) (string, error) {
		return string(raw), nil
	})

	rec := postFleetPlan(t, gw, `{"description":"check all production databases"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
	if !strings.Contains(rec.Body.String(), "restricted") {
		t.Errorf("body = %q, want mention of restricted", rec.Body.String())
	}
}

func TestHandleFleetPlan_RequiresApproval(t *testing.T) {
	cfg := makeTestInfra()
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{
		{Name: "check_connection", Agent: "database", ActionClass: "read"},
		{Name: "terminate_connection", Agent: "database", ActionClass: "destructive"},
	})
	llmResp := map[string]any{
		"job_def": map[string]any{
			"name": "terminate-job",
			"change": map[string]any{
				"steps": []any{
					map[string]any{"agent": "database", "tool": "terminate_connection", "on_failure": "stop"},
				},
			},
			"targets":  map[string]any{"tags": []string{"staging"}},
			"strategy": map[string]any{"canary_count": 1},
		},
		"planner_notes": "test",
	}
	raw, _ := json.Marshal(llmResp)
	gw := makePlannerGateway(cfg, reg, func(_ context.Context, _ string) (string, error) {
		return string(raw), nil
	})

	rec := postFleetPlan(t, gw, `{"description":"terminate idle connections on staging"}`)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp FleetPlanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.RequiresApproval {
		t.Error("RequiresApproval = false, want true for destructive step")
	}
	if len(resp.WrittenSteps) == 0 {
		t.Error("WrittenSteps is empty, want terminate_connection")
	}
}

func TestHandleFleetPlan_ReadOnlyNoApproval(t *testing.T) {
	cfg := makeTestInfra()
	reg := makeRegistryWithTools([]toolregistry.ToolEntry{
		{Name: "check_connection", Agent: "database", ActionClass: "read"},
	})
	llmResp := map[string]any{
		"job_def": map[string]any{
			"name": "health-check-job",
			"change": map[string]any{
				"steps": []any{
					map[string]any{"agent": "database", "tool": "check_connection", "on_failure": "stop"},
				},
			},
			"targets":  map[string]any{"tags": []string{"staging"}},
			"strategy": map[string]any{"canary_count": 1},
		},
		"planner_notes": "connectivity check",
	}
	raw, _ := json.Marshal(llmResp)
	gw := makePlannerGateway(cfg, reg, func(_ context.Context, _ string) (string, error) {
		return string(raw), nil
	})

	rec := postFleetPlan(t, gw, `{"description":"check connectivity on staging"}`)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp FleetPlanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.RequiresApproval {
		t.Error("RequiresApproval = true, want false for read-only steps")
	}
}

// --- Fleet job journey anchor tests ---

// testAuditor is a minimal in-memory Auditor used to capture recorded events.
type testAuditor struct {
	mu     sync.Mutex
	events []*audit.Event
}

func (a *testAuditor) Record(_ context.Context, e *audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

func (a *testAuditor) RecordOutcome(_ context.Context, _ string, _ *audit.Outcome) error {
	return nil
}

func (a *testAuditor) Query(_ context.Context, _ audit.QueryOptions) ([]audit.Event, error) {
	return nil, nil
}

func (a *testAuditor) Close() error { return nil }

// TestHandleFleetCreateJob_AnchorEvent verifies that creating a fleet job sets
// X-Trace-ID = "tr_" + jobID on the response and records a gateway_request
// audit event with that trace ID and no tool (so it qualifies as a journey anchor).
// Running with two different job IDs also confirms trace ID uniqueness.
// --- dispatchDirectTool tests ---

// mockDirectToolAgent starts an httptest server that handles POST /tool/{name}.
// The handler runs handlerFn to produce the response body and HTTP status.
func mockDirectToolAgent(t *testing.T, toolName string, handlerFn func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *discovery.Agent) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tool/"+toolName && r.Method == http.MethodPost {
			handlerFn(w, r)
			return
		}
		http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	agent := &discovery.Agent{
		Name:      agentNameDB,
		InvokeURL: srv.URL + "/invoke", // dispatchDirectTool strips /invoke
	}
	return srv, agent
}

// makeDirectDispatchGateway wires the gateway with one registered agent.
// Builds the struct directly to avoid NewGateway's a2aclient.NewFromCard call
// which requires a non-nil agent card.
func makeDirectDispatchGateway(agent *discovery.Agent) *Gateway {
	return &Gateway{
		agents:  map[string]*discovery.Agent{agent.Name: agent},
		clients: make(map[string]*a2aclient.Client),
		toolRegistry: makeRegistryWithTools([]toolregistry.ToolEntry{
			{Name: "check_connection", Agent: "database", ActionClass: "read"},
			{Name: "terminate_idle_connections", Agent: "database", ActionClass: "destructive"},
		}),
	}
}

func TestDispatchDirectTool_Success(t *testing.T) {
	_, agent := mockDirectToolAgent(t, "check_connection", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"output": "PostgreSQL 16.1 — connected"}) //nolint:errcheck
	})
	gw := makeDirectDispatchGateway(agent)

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/db/check_connection",
		strings.NewReader(`{"connection_string":"postgres://localhost/test"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp a2aResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.State != "completed" {
		t.Errorf("State = %q, want completed", resp.State)
	}
	if !strings.Contains(resp.Text, "PostgreSQL 16.1") {
		t.Errorf("Text = %q, want mention of PostgreSQL 16.1", resp.Text)
	}
	if resp.AgentName != agentNameDB {
		t.Errorf("AgentName = %q, want %q", resp.AgentName, agentNameDB)
	}
}

func TestDispatchDirectTool_AgentReturnsError(t *testing.T) {
	_, agent := mockDirectToolAgent(t, "terminate_idle_connections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"error": "policy denied: destructive action blocked"}) //nolint:errcheck
	})
	gw := makeDirectDispatchGateway(agent)

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/db/terminate_idle_connections",
		strings.NewReader(`{"connection_string":"postgres://prod/db","idle_minutes":10}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// 422 from agent → policy denial text → 403 from gateway
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (policy denial)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "policy denied") {
		t.Errorf("body = %q, want policy denied message", rec.Body.String())
	}
}

func TestDispatchDirectTool_AgentReturnsToolError(t *testing.T) {
	_, agent := mockDirectToolAgent(t, "check_connection", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"error": "connection refused"}) //nolint:errcheck
	})
	gw := makeDirectDispatchGateway(agent)

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/db/check_connection",
		strings.NewReader(`{"connection_string":"postgres://bad-host/db"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestDispatchDirectTool_AgentNotRegistered(t *testing.T) {
	// Gateway with no agents registered.
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client),
		toolRegistry: makeRegistryWithTools([]toolregistry.ToolEntry{
			{Name: "check_connection", Agent: "database", ActionClass: "read"},
		}),
	}

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/db/check_connection",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (agent not available)", rec.Code)
	}
}

func TestDispatchDirectTool_TraceIDPropagated(t *testing.T) {
	var capturedTraceID string

	_, agent := mockDirectToolAgent(t, "check_connection", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		if id, ok := body["trace_id"].(string); ok {
			capturedTraceID = id
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"output": "ok"}) //nolint:errcheck
	})
	gw := makeDirectDispatchGateway(agent)

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/db/check_connection",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Trace-ID", "tr_flj_test999")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if capturedTraceID != "tr_flj_test999" {
		t.Errorf("agent received trace_id = %q, want tr_flj_test999", capturedTraceID)
	}
	// Gateway must also echo the trace ID back on the response.
	if got := rec.Header().Get("X-Trace-ID"); got != "tr_flj_test999" {
		t.Errorf("response X-Trace-ID = %q, want tr_flj_test999", got)
	}
}

func TestHandleFleetCreateJob_AnchorEvent(t *testing.T) {
	cases := []struct {
		jobID   string
		jobName string
	}{
		{"flj_test123", "my-job"},
		{"flj_other456", "another-job"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.jobID, func(t *testing.T) {
			// Mock auditd backend that returns the created job record.
			auditdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
					"job_id": tc.jobID,
					"name":   tc.jobName,
				})
			}))
			defer auditdSrv.Close()

			ta := &testAuditor{}
			gw := &Gateway{
				agents:   make(map[string]*discovery.Agent),
				clients:  make(map[string]*a2aclient.Client),
				auditURL: auditdSrv.URL,
				auditor:  audit.NewGatewayAuditor(ta),
			}
			mux := http.NewServeMux()
			gw.RegisterRoutes(mux)

			body := `{"name":"` + tc.jobName + `"}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/jobs", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
			}

			wantTraceID := "tr_" + tc.jobID
			if got := rec.Header().Get("X-Trace-ID"); got != wantTraceID {
				t.Errorf("X-Trace-ID = %q, want %q", got, wantTraceID)
			}

			// Anchor event must have the correct trace ID and no tool execution
			// so that QueryJourneys recognises it as a journey anchor.
			ta.mu.Lock()
			events := ta.events
			ta.mu.Unlock()

			if len(events) != 1 {
				t.Fatalf("recorded %d audit events, want 1", len(events))
			}
			e := events[0]
			if e.TraceID != wantTraceID {
				t.Errorf("event.TraceID = %q, want %q", e.TraceID, wantTraceID)
			}
			if e.Tool != nil {
				t.Errorf("event.Tool = %+v, want nil (anchor event must not have a tool)", e.Tool)
			}
		})
	}
}
