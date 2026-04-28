package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/a2aclient"

	"helpdesk/internal/audit"
	"helpdesk/internal/discovery"
	"helpdesk/internal/identity"
)

// makeRouterGateway builds a Gateway with a stubbed LLM and a named set of
// registered clients so buildRoutingPrompt only lists available agents.
func makeRouterGateway(llmFn func(context.Context, string) (string, error), registeredAgents []string) *Gateway {
	clients := make(map[string]*a2aclient.Client, len(registeredAgents))
	for _, name := range registeredAgents {
		clients[name] = nil // presence is all that matters for routing prompt
	}
	return &Gateway{
		agents:     make(map[string]*discovery.Agent),
		clients:    clients,
		plannerLLM: llmFn,
	}
}

// validRoutingJSON returns a well-formed routing decision JSON for the given agent.
func validRoutingJSON(agent string) string {
	d := RoutingDecision{
		Agent:           agent,
		RequestCategory: "database",
		Confidence:      0.9,
		UserIntent:      "check connection count",
		ReasoningChain:  []string{"mentions connections", "database agent handles pg"},
		AlternativesConsidered: []RoutingAlternative{
			{Agent: agentNameK8s, RejectedBecause: "no k8s context"},
		},
	}
	b, _ := json.Marshal(d)
	return string(b)
}

// ── buildRoutingPrompt ────────────────────────────────────────────────────

func TestBuildRoutingPrompt_ContainsMessage(t *testing.T) {
	gw := makeRouterGateway(nil, []string{agentNameDB})
	prompt := gw.buildRoutingPrompt("how many connections are open?")
	if !strings.Contains(prompt, "how many connections are open?") {
		t.Error("prompt should contain the user message")
	}
}

func TestBuildRoutingPrompt_OnlyListsRegisteredAgents(t *testing.T) {
	// Only DB registered — k8s should not appear.
	gw := makeRouterGateway(nil, []string{agentNameDB})
	prompt := gw.buildRoutingPrompt("anything")
	if strings.Contains(prompt, agentNameK8s) {
		t.Errorf("prompt should not list unregistered agent %q", agentNameK8s)
	}
	if !strings.Contains(prompt, agentNameDB) {
		t.Errorf("prompt should list registered agent %q", agentNameDB)
	}
}

func TestBuildRoutingPrompt_AllRegisteredAgentsListed(t *testing.T) {
	all := []string{agentNameDB, agentNameK8s, agentNameIncident, agentNameResearch, agentNameSysadmin}
	gw := makeRouterGateway(nil, all)
	prompt := gw.buildRoutingPrompt("anything")
	for _, name := range all {
		if !strings.Contains(prompt, name) {
			t.Errorf("prompt missing registered agent %q", name)
		}
	}
}

// ── routeWithLLM ─────────────────────────────────────────────────────────

func TestRouteWithLLM_NoLLM(t *testing.T) {
	gw := makeRouterGateway(nil, []string{agentNameDB})
	_, err := gw.routeWithLLM(context.Background(), "check connections")
	if err == nil {
		t.Fatal("expected error when plannerLLM is nil")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("error = %q, want mention of 'not configured'", err.Error())
	}
}

func TestRouteWithLLM_LLMError(t *testing.T) {
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		return "", fmt.Errorf("upstream timeout")
	}, []string{agentNameDB})

	_, err := gw.routeWithLLM(context.Background(), "check connections")
	if err == nil {
		t.Fatal("expected error when LLM fails")
	}
	if !strings.Contains(err.Error(), "upstream timeout") {
		t.Errorf("error = %q, want upstream timeout", err.Error())
	}
}

func TestRouteWithLLM_MalformedJSON(t *testing.T) {
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		return "not json at all", nil
	}, []string{agentNameDB})

	_, err := gw.routeWithLLM(context.Background(), "check connections")
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "unparseable JSON") {
		t.Errorf("error = %q, want mention of 'unparseable JSON'", err.Error())
	}
}

func TestRouteWithLLM_UnknownAgent(t *testing.T) {
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		return `{"agent":"nonexistent_agent","confidence":0.9}`, nil
	}, []string{agentNameDB})

	_, err := gw.routeWithLLM(context.Background(), "check connections")
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "unknown agent") {
		t.Errorf("error = %q, want mention of 'unknown agent'", err.Error())
	}
}

func TestRouteWithLLM_Success(t *testing.T) {
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		return validRoutingJSON(agentNameDB), nil
	}, []string{agentNameDB, agentNameK8s})

	decision, err := gw.routeWithLLM(context.Background(), "how many connections?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Agent != agentNameDB {
		t.Errorf("Agent = %q, want %q", decision.Agent, agentNameDB)
	}
	if decision.Confidence != 0.9 {
		t.Errorf("Confidence = %v, want 0.9", decision.Confidence)
	}
	if len(decision.ReasoningChain) == 0 {
		t.Error("ReasoningChain should not be empty")
	}
}

func TestRouteWithLLM_StripsMarkdownFences(t *testing.T) {
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		return "```json\n" + validRoutingJSON(agentNameDB) + "\n```", nil
	}, []string{agentNameDB})

	decision, err := gw.routeWithLLM(context.Background(), "check pg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Agent != agentNameDB {
		t.Errorf("Agent = %q, want %q", decision.Agent, agentNameDB)
	}
}

// ── handleQuery routing integration ──────────────────────────────────────

// postQuery sends a POST /api/v1/query request and returns the recorder.
func postQuery(t *testing.T, gw *Gateway, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "test@example.com")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandleQuery_NoAgent_NoLLM_Returns503(t *testing.T) {
	gw := makeRouterGateway(nil, []string{agentNameDB})

	rec := postQuery(t, gw, `{"message":"how many connections?"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "agent") {
		t.Errorf("body = %q, want mention of 'agent'", rec.Body.String())
	}
}

func TestHandleQuery_NoAgent_LLMError_Returns503(t *testing.T) {
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		return "", fmt.Errorf("LLM unavailable")
	}, []string{agentNameDB})

	rec := postQuery(t, gw, `{"message":"how many connections?"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandleQuery_ExplicitAgent_UnknownAlias_Returns400(t *testing.T) {
	gw := makeRouterGateway(nil, []string{agentNameDB})

	rec := postQuery(t, gw, `{"agent":"bogus","message":"check pg"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown agent") {
		t.Errorf("body = %q, want mention of 'unknown agent'", rec.Body.String())
	}
}

func TestHandleQuery_MissingMessage_Returns400(t *testing.T) {
	gw := makeRouterGateway(nil, []string{agentNameDB})

	rec := postQuery(t, gw, `{"agent":"db"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// ── recordRoutingDecision ─────────────────────────────────────────────────

func TestRecordRoutingDecision_EmitsEvent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "router-audit-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := audit.NewStore(audit.StoreConfig{DSN: filepath.Join(tmpDir, "audit.db")})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	gw := makeRouterGateway(nil, nil)
	gw.auditor = audit.NewGatewayAuditor(store)

	traceID := audit.NewTraceIDWithPrefix("rt_")
	decision := &RoutingDecision{
		Agent:           agentNameDB,
		RequestCategory: "database",
		Confidence:      0.92,
		UserIntent:      "check active connections",
		ReasoningChain:  []string{"message mentions pg connections", "db agent handles postgresql"},
		AlternativesConsidered: []RoutingAlternative{
			{Agent: agentNameK8s, RejectedBecause: "no kubernetes context in message"},
		},
	}

	gw.recordRoutingDecision(context.Background(), traceID, identity.ResolvedPrincipal{UserID: "ops@example.com", AuthMethod: "static"}, decision)

	events, err := store.Query(context.Background(), audit.QueryOptions{TraceID: traceID})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	got := events[0]
	if got.EventType != audit.EventTypeDelegation {
		t.Errorf("EventType = %q, want %q", got.EventType, audit.EventTypeDelegation)
	}
	if got.TraceID != traceID {
		t.Errorf("TraceID = %q, want %q", got.TraceID, traceID)
	}
	if got.Decision == nil {
		t.Fatal("Decision is nil")
	}
	if got.Decision.Agent != agentNameDB {
		t.Errorf("Decision.Agent = %q, want %q", got.Decision.Agent, agentNameDB)
	}
	if got.Decision.Confidence != 0.92 {
		t.Errorf("Decision.Confidence = %v, want 0.92", got.Decision.Confidence)
	}
	if len(got.Decision.ReasoningChain) != 2 {
		t.Errorf("ReasoningChain len = %d, want 2", len(got.Decision.ReasoningChain))
	}
	if len(got.Decision.AlternativesConsidered) != 1 {
		t.Errorf("AlternativesConsidered len = %d, want 1", len(got.Decision.AlternativesConsidered))
	}
	if got.Decision.AlternativesConsidered[0].Agent != agentNameK8s {
		t.Errorf("Alternative.Agent = %q, want %q", got.Decision.AlternativesConsidered[0].Agent, agentNameK8s)
	}
}

func TestRecordRoutingDecision_NilAuditor(t *testing.T) {
	gw := makeRouterGateway(nil, nil)
	// Should be a no-op, not a panic.
	gw.recordRoutingDecision(context.Background(), "rt_test", identity.ResolvedPrincipal{}, &RoutingDecision{
		Agent: agentNameDB,
	})
}

// ── handleQuery explicit-agent path ───────────────────────────────────────

func TestHandleQuery_ExplicitAgent_ResolvesAlias(t *testing.T) {
	// "db" alias should resolve to postgres_database_agent and reach proxyToAgent.
	// With no client registered, we get 502 — but that confirms the alias resolved
	// and the handler didn't reject it as unknown.
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client), // empty — triggers 502, not 400
	}

	rec := postQuery(t, gw, `{"agent":"db","message":"check connections"}`)

	// 400 would mean alias lookup failed; 502 means it resolved and hit proxyToAgent.
	if rec.Code == http.StatusBadRequest {
		t.Errorf("got 400 — alias 'db' was not resolved; body: %s", rec.Body.String())
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (agent not available)", rec.Code)
	}
}

func TestHandleQuery_ExplicitAgent_AllAliasesAccepted(t *testing.T) {
	aliases := []string{"database", "db", "k8s", "sysadmin", "host", "incident", "research"}
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			gw := &Gateway{
				agents:  make(map[string]*discovery.Agent),
				clients: make(map[string]*a2aclient.Client),
			}
			body := `{"agent":"` + alias + `","message":"test"}`
			rec := postQuery(t, gw, body)
			if rec.Code == http.StatusBadRequest {
				t.Errorf("alias %q rejected with 400: %s", alias, rec.Body.String())
			}
		})
	}
}

// ── integration: trace ID linkage ─────────────────────────────────────────

// TestHandleQuery_LLMRouting_TraceIDLinksEvents is an integration test that
// verifies the core invariant of the LLM routing feature: both the
// delegation_decision event (routing choice) and the gateway_request event
// (agent call, even when the agent is unavailable) must share the same
// trace ID, so QueryJourneys can link them into a single journey.
func TestHandleQuery_LLMRouting_TraceIDLinksEvents(t *testing.T) {
	ta := &testAuditor{}
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client), // empty → 502, but still records gateway_request
		auditor: audit.NewGatewayAuditor(ta),
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			return validRoutingJSON(agentNameDB), nil
		},
	}

	rec := postQuery(t, gw, `{"message":"how many connections are open?"}`)

	// 502 is expected — no real agent is registered. What matters is the audit trail.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}

	ta.mu.Lock()
	events := ta.events
	ta.mu.Unlock()

	// Expect exactly two events: delegation_decision + gateway_request.
	if len(events) != 2 {
		t.Fatalf("recorded %d audit events, want 2 (delegation_decision + gateway_request)", len(events))
	}

	// Both events must carry the same trace ID.
	traceID := events[0].TraceID
	if traceID == "" {
		t.Fatal("first event has empty TraceID")
	}
	if events[1].TraceID != traceID {
		t.Errorf("event[0].TraceID = %q, event[1].TraceID = %q — must match", traceID, events[1].TraceID)
	}

	// Verify event types are exactly what we expect (order: routing first, then request).
	types := map[audit.EventType]bool{}
	for _, e := range events {
		types[e.EventType] = true
	}
	if !types[audit.EventTypeDelegation] {
		t.Error("missing delegation_decision event")
	}
	if !types[audit.EventTypeGatewayRequest] {
		t.Error("missing gateway_request event")
	}

	// The trace ID on the response must match what was stored.
	if got := rec.Header().Get("X-Trace-ID"); got != traceID {
		t.Errorf("response X-Trace-ID = %q, want %q (matching stored events)", got, traceID)
	}

	// The delegation event must name the agent chosen by the LLM.
	var delegationEvent *audit.Event
	for i := range events {
		if events[i].EventType == audit.EventTypeDelegation {
			delegationEvent = events[i]
		}
	}
	if delegationEvent.Decision == nil || delegationEvent.Decision.Agent != agentNameDB {
		t.Errorf("delegation_decision.Agent = %q, want %q",
			delegationEvent.Decision.Agent, agentNameDB)
	}
}

func TestHandleQuery_TraceIDSetBeforeRouting(t *testing.T) {
	// Even when the agent is not available (502), the trace ID header must be
	// present in the response so callers can correlate errors.
	// Use an empty clients map so the "agent not available" 502 fires cleanly
	// (present-but-nil client would panic; absent client returns 502 safely).
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client), // intentionally empty
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			return validRoutingJSON(agentNameDB), nil
		},
	}

	rec := postQuery(t, gw, `{"message":"check connections"}`)

	// Agent not in clients → 502, but trace ID must already be set on the response.
	traceID := rec.Header().Get("X-Trace-ID")
	if traceID == "" {
		t.Error("X-Trace-ID header should be set even on error responses")
	}
	if !strings.HasPrefix(traceID, "tr_") {
		t.Errorf("X-Trace-ID = %q, want tr_ prefix", traceID)
	}
}
