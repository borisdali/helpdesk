package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"

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
