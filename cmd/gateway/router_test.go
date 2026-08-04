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
	"time"

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
	prompt := gw.buildRoutingPrompt("how many connections are open?", nil)
	if !strings.Contains(prompt, "how many connections are open?") {
		t.Error("prompt should contain the user message")
	}
}

func TestBuildRoutingPrompt_OnlyListsRegisteredAgents(t *testing.T) {
	// Only DB registered — k8s should not appear.
	gw := makeRouterGateway(nil, []string{agentNameDB})
	prompt := gw.buildRoutingPrompt("anything", nil)
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
	prompt := gw.buildRoutingPrompt("anything", nil)
	for _, name := range all {
		if !strings.Contains(prompt, name) {
			t.Errorf("prompt missing registered agent %q", name)
		}
	}
}

// ── routeWithLLM ─────────────────────────────────────────────────────────

func TestRouteWithLLM_NoLLM(t *testing.T) {
	gw := makeRouterGateway(nil, []string{agentNameDB})
	_, err := gw.routeWithLLM(context.Background(), "check connections", nil)
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

	_, err := gw.routeWithLLM(context.Background(), "check connections", nil)
	if err == nil {
		t.Fatal("expected error when LLM fails")
	}
	if !strings.Contains(err.Error(), "upstream timeout") {
		t.Errorf("error = %q, want upstream timeout", err.Error())
	}
}

func TestRouteWithLLM_MalformedJSON_RetriesOnce(t *testing.T) {
	calls := 0
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		calls++
		return "not json at all", nil
	}, []string{agentNameDB})

	_, err := gw.routeWithLLM(context.Background(), "check connections", nil)
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if !strings.Contains(err.Error(), "unparseable JSON") {
		t.Errorf("error = %q, want mention of 'unparseable JSON'", err.Error())
	}
	if calls != 2 {
		t.Errorf("LLM called %d times, want 2 (initial + one retry)", calls)
	}
}

func TestRouteWithLLM_SucceedsOnRetry(t *testing.T) {
	calls := 0
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		calls++
		if calls == 1 {
			// First call: inject a token-leak artifact like the real model produced
			return `{"agent":"postgres_database_agent","confidence":0.9 immunotherapy"}`, nil
		}
		return validRoutingJSON(agentNameDB), nil
	}, []string{agentNameDB})

	decision, err := gw.routeWithLLM(context.Background(), "check connections", nil)
	if err != nil {
		t.Fatalf("expected success on retry, got: %v", err)
	}
	if decision.Agent != agentNameDB {
		t.Errorf("Agent = %q, want %q", decision.Agent, agentNameDB)
	}
	if calls != 2 {
		t.Errorf("LLM called %d times, want 2", calls)
	}
}

func TestRouteWithLLM_UnknownAgent(t *testing.T) {
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		return `{"agent":"nonexistent_agent","confidence":0.9}`, nil
	}, []string{agentNameDB})

	_, err := gw.routeWithLLM(context.Background(), "check connections", nil)
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

	decision, err := gw.routeWithLLM(context.Background(), "how many connections?", nil)
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

	decision, err := gw.routeWithLLM(context.Background(), "check pg", nil)
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

// TestLocalHandleJourneys_RunIDFilter verifies that ?run_id=plr_* translates to
// the run's stored trace_id and returns the matching journey. This is the user-
// facing path for navigating from an incident (plr_*) to its audit Journey.
func TestLocalHandleJourneys_RunIDFilter(t *testing.T) {
	store, err := audit.NewStore(audit.StoreConfig{DBPath: filepath.Join(t.TempDir(), "audit.db")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	// Insert a gateway_request anchor event under tr_remediation.
	// This is the anchor emitted by handlePlaybookRun for agent_approve runs.
	if err := store.Record(ctx, &audit.Event{
		EventID:   "anchor_rem1",
		Timestamp: base,
		EventType: audit.EventTypeGatewayRequest,
		TraceID:   "tr_remediation",
		Session:   audit.Session{ID: "sess_rem", UserID: "ops@example.com"},
		Input:     audit.Input{UserQuery: "Connection Overload — Terminate Idle Sessions"},
		Tool:      nil, // empty tool_name — required for Q1 to pick this up as a journey anchor
	}); err != nil {
		t.Fatalf("record anchor: %v", err)
	}

	// Insert a tool execution event under the same trace.
	if err := store.Record(ctx, &audit.Event{
		EventID:   "tool_rem1",
		Timestamp: base.Add(time.Second),
		EventType: audit.EventTypeToolExecution,
		TraceID:   "tr_remediation",
		Session:   audit.Session{ID: "sess_rem"},
		Tool:      &audit.ToolExecution{Name: "terminate_idle_connections"},
		Outcome:   &audit.Outcome{Status: "success"},
	}); err != nil {
		t.Fatalf("record tool event: %v", err)
	}

	// Register the playbook run so ?run_id= can be translated to the trace_id.
	prs, err := audit.NewPlaybookRunStore(store.DB(), false)
	if err != nil {
		t.Fatalf("NewPlaybookRunStore: %v", err)
	}
	if err := prs.Record(ctx, &audit.PlaybookRun{
		RunID:      "plr_rem01",
		PlaybookID: "pb_conn_v15",
		SeriesID:   "pbs_connection_remediate",
		TraceID:    "tr_remediation",
		Operator:   "ops@example.com",
		StartedAt:  base,
	}); err != nil {
		t.Fatalf("insert playbook run: %v", err)
	}

	handler := localHandleJourneys(store)

	// ?run_id= should return the journey for tr_remediation.
	req := httptest.NewRequest(http.MethodGet, "/v1/journeys?run_id=plr_rem01", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var journeys []audit.JourneySummary
	if err := json.NewDecoder(rec.Body).Decode(&journeys); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(journeys) != 1 {
		t.Fatalf("got %d journeys, want 1; body: %s", len(journeys), rec.Body.String())
	}
	if journeys[0].TraceID != "tr_remediation" {
		t.Errorf("TraceID = %q, want tr_remediation", journeys[0].TraceID)
	}
	if journeys[0].IncidentRunID != "plr_rem01" {
		t.Errorf("IncidentRunID = %q, want plr_rem01", journeys[0].IncidentRunID)
	}

	// Unknown run_id should return an empty list, not an error.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/journeys?run_id=plr_notexist", nil)
	rec2 := httptest.NewRecorder()
	handler(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("unknown run_id: status = %d, want 200", rec2.Code)
	}
	var empty []audit.JourneySummary
	if err := json.NewDecoder(rec2.Body).Decode(&empty); err != nil {
		t.Fatalf("decode empty response: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("unknown run_id returned %d journeys, want 0", len(empty))
	}
}

// ── matchPlaybookByKeywords ─────────────────────────────────────────────

func nodePressurePlaybookFixture() *audit.Playbook {
	return &audit.Playbook{
		PlaybookID:    "pb_node_pressure01",
		SeriesID:      "pbs_k8s_node_pressure_triage",
		AgentName:     agentNameK8s,
		ProblemClass:  "availability",
		EntryPoint:    true,
		PlaybookType:  "triage",
		ExecutionMode: "agent",
		IsActive:      true,
		Symptoms: []string{
			"node experiencing MemoryPressure condition",
			"kubelet node-pressure eviction alert for the node hosting postgres",
			"Evicted event in namespace events",
		},
	}
}

func podCrashPlaybookFixture() *audit.Playbook {
	return &audit.Playbook{
		PlaybookID:    "pb_pod_crash01",
		SeriesID:      "pbs_k8s_pod_crash_triage",
		AgentName:     agentNameK8s,
		ProblemClass:  "availability",
		EntryPoint:    true,
		PlaybookType:  "triage",
		ExecutionMode: "agent",
		IsActive:      true,
		Symptoms: []string{
			"PostgreSQL pod is restarting or in CrashLoopBackOff",
			"pod status shows Completed (exit 0) or OOMKilled (exit 137)",
		},
	}
}

func TestMatchPlaybookByKeywords_StrongMatch(t *testing.T) {
	pbs := []*audit.Playbook{nodePressurePlaybookFixture(), podCrashPlaybookFixture()}
	pb, symptom, score, ok := matchPlaybookByKeywords(
		"We received a node memory-pressure alert for the node hosting the postgres pod", pbs)
	if !ok {
		t.Fatal("expected a strong match")
	}
	if pb.SeriesID != "pbs_k8s_node_pressure_triage" {
		t.Errorf("matched playbook = %q, want pbs_k8s_node_pressure_triage", pb.SeriesID)
	}
	if symptom == "" {
		t.Error("expected a non-empty matched symptom")
	}
	if score < 0.6 {
		t.Errorf("score = %v, want >= 0.6", score)
	}
}

func TestMatchPlaybookByKeywords_NoMatch(t *testing.T) {
	pbs := []*audit.Playbook{nodePressurePlaybookFixture(), podCrashPlaybookFixture()}
	_, _, _, ok := matchPlaybookByKeywords("how does VACUUM work in postgres?", pbs)
	if ok {
		t.Error("expected no match for an unrelated conceptual question")
	}
}

func TestMatchPlaybookByKeywords_EmptyPlaybookList(t *testing.T) {
	_, _, _, ok := matchPlaybookByKeywords("node memory pressure evicted", nil)
	if ok {
		t.Error("expected no match against an empty playbook list")
	}
}

func TestMatchPlaybookByKeywords_AmbiguousTie_FallsThrough(t *testing.T) {
	// Two playbooks with identical symptom text score identically — should not
	// pick either one, since the margin check (score - secondBest >= 0.15) fails.
	pbA := nodePressurePlaybookFixture()
	pbA.SeriesID = "pbs_a"
	pbA.Symptoms = []string{"shared distinctive symptom phrase here"}
	pbB := nodePressurePlaybookFixture()
	pbB.SeriesID = "pbs_b"
	pbB.Symptoms = []string{"shared distinctive symptom phrase here"}

	_, _, _, ok := matchPlaybookByKeywords("shared distinctive symptom phrase here", []*audit.Playbook{pbA, pbB})
	if ok {
		t.Error("expected an ambiguous tie between two identically-scored playbooks to fall through")
	}
}

// ── entryPointPlaybooks ──────────────────────────────────────────────────

func mockAuditdPlaybookList(t *testing.T, playbooks []*audit.Playbook) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"playbooks": playbooks}) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// mockAuditdForPlaybookSelection serves both the list endpoint (used by
// entryPointPlaybooks and fetchPlaybookBySeriesID) and the single-playbook-
// by-ID endpoint (used internally by handlePlaybookRun's own fetchPlaybook
// call) from the same fixture set, so a full handleQuery -> runQueryViaPlaybook
// -> handlePlaybookRun dispatch can be exercised end-to-end in tests.
func mockAuditdForPlaybookSelection(t *testing.T, playbooks []*audit.Playbook) *httptest.Server {
	t.Helper()
	byID := make(map[string]*audit.Playbook, len(playbooks))
	for _, pb := range playbooks {
		byID[pb.PlaybookID] = pb
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/fleet/playbooks" {
			seriesID := r.URL.Query().Get("series_id")
			matched := make([]*audit.Playbook, 0, len(playbooks))
			for _, pb := range playbooks {
				if seriesID == "" || pb.SeriesID == seriesID {
					matched = append(matched, pb)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"playbooks": matched}) //nolint:errcheck
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/fleet/playbooks/")
		if pb, ok := byID[id]; ok {
			json.NewEncoder(w).Encode(pb) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEntryPointPlaybooks_NoAuditURL_ReturnsError(t *testing.T) {
	gw := &Gateway{}
	_, err := gw.entryPointPlaybooks(context.Background())
	if err == nil {
		t.Error("expected an error when auditURL is unset")
	}
}

func TestEntryPointPlaybooks_FiltersToEntryPointTriageOnly(t *testing.T) {
	remediation := nodePressurePlaybookFixture()
	remediation.SeriesID = "pbs_k8s_node_pressure_remediate"
	remediation.PlaybookType = "remediation"
	remediation.EntryPoint = false

	nonEntry := podCrashPlaybookFixture()
	nonEntry.SeriesID = "pbs_some_non_entry_triage"
	nonEntry.EntryPoint = false

	entry := nodePressurePlaybookFixture()

	srv := mockAuditdPlaybookList(t, []*audit.Playbook{remediation, nonEntry, entry})
	gw := &Gateway{auditURL: srv.URL}

	pbs, err := gw.entryPointPlaybooks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pbs) != 1 || pbs[0].SeriesID != "pbs_k8s_node_pressure_triage" {
		t.Errorf("got %d playbooks, want exactly [pbs_k8s_node_pressure_triage]", len(pbs))
	}
}

func TestEntryPointPlaybooks_CachesWithinTTL(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"playbooks": []*audit.Playbook{nodePressurePlaybookFixture()},
		})
	}))
	t.Cleanup(srv.Close)
	gw := &Gateway{auditURL: srv.URL}

	if _, err := gw.entryPointPlaybooks(context.Background()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := gw.entryPointPlaybooks(context.Background()); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls != 1 {
		t.Errorf("auditd called %d times within TTL, want 1", calls)
	}
}

func TestEntryPointPlaybooks_RefetchesAfterTTL(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"playbooks": []*audit.Playbook{nodePressurePlaybookFixture()},
		})
	}))
	t.Cleanup(srv.Close)
	gw := &Gateway{auditURL: srv.URL}

	if _, err := gw.entryPointPlaybooks(context.Background()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Force the cache to look stale.
	gw.entryPointCache.mu.Lock()
	gw.entryPointCache.fetchedAt = time.Now().Add(-entryPointPlaybookCacheTTL - time.Second)
	gw.entryPointCache.mu.Unlock()

	if _, err := gw.entryPointPlaybooks(context.Background()); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls != 2 {
		t.Errorf("auditd called %d times after TTL expiry, want 2", calls)
	}
}

func TestEntryPointPlaybooks_FailsOpenOnAuditdError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	gw := &Gateway{auditURL: srv.URL}

	pbs, err := gw.entryPointPlaybooks(context.Background())
	if err == nil {
		t.Error("expected an error when auditd returns 500")
	}
	if pbs != nil {
		t.Errorf("expected nil playbooks on error, got %d", len(pbs))
	}
}

// ── buildRoutingPrompt playbook section ─────────────────────────────────

func TestBuildRoutingPrompt_IncludesPlaybooks(t *testing.T) {
	gw := makeRouterGateway(nil, []string{agentNameK8s})
	prompt := gw.buildRoutingPrompt("anything", []*audit.Playbook{nodePressurePlaybookFixture()})
	if !strings.Contains(prompt, "pbs_k8s_node_pressure_triage") {
		t.Error("prompt should list the candidate playbook's series_id")
	}
	if !strings.Contains(prompt, "playbook_series_id") {
		t.Error("prompt should document the playbook_series_id response field")
	}
}

func TestBuildRoutingPrompt_OmitsPlaybookSectionWhenEmpty(t *testing.T) {
	gw := makeRouterGateway(nil, []string{agentNameK8s})
	prompt := gw.buildRoutingPrompt("anything", nil)
	if strings.Contains(prompt, "Available Triage Playbooks") {
		t.Error("prompt should not mention playbooks when none are available")
	}
	if strings.Contains(prompt, "playbook_series_id") {
		t.Error("prompt should not document playbook_series_id when no playbooks are available")
	}
}

// ── routeWithLLM playbook selection ──────────────────────────────────────

func validRoutingJSONWithPlaybook(agent, seriesID string, confidence float64) string {
	d := RoutingDecision{
		Agent:              agent,
		RequestCategory:    "kubernetes",
		Confidence:         0.9,
		UserIntent:         "diagnose node memory pressure",
		ReasoningChain:     []string{"message mentions node memory pressure"},
		PlaybookSeriesID:   seriesID,
		PlaybookConfidence: confidence,
	}
	b, _ := json.Marshal(d)
	return string(b)
}

func TestRouteWithLLM_PlaybookSeriesID_ValidAndConfident_Kept(t *testing.T) {
	pbs := []*audit.Playbook{nodePressurePlaybookFixture()}
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		return validRoutingJSONWithPlaybook(agentNameK8s, "pbs_k8s_node_pressure_triage", 0.9), nil
	}, []string{agentNameK8s})

	decision, err := gw.routeWithLLM(context.Background(), "node memory pressure alert", pbs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.PlaybookSeriesID != "pbs_k8s_node_pressure_triage" {
		t.Errorf("PlaybookSeriesID = %q, want pbs_k8s_node_pressure_triage", decision.PlaybookSeriesID)
	}
}

func TestRouteWithLLM_PlaybookSeriesID_NotInCandidateList_Cleared(t *testing.T) {
	pbs := []*audit.Playbook{nodePressurePlaybookFixture()}
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		// Hallucinated series_id not present in the candidate list.
		return validRoutingJSONWithPlaybook(agentNameK8s, "pbs_totally_made_up", 0.95), nil
	}, []string{agentNameK8s})

	decision, err := gw.routeWithLLM(context.Background(), "node memory pressure alert", pbs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.PlaybookSeriesID != "" {
		t.Errorf("PlaybookSeriesID = %q, want empty (hallucinated series_id must be cleared)", decision.PlaybookSeriesID)
	}
	// The underlying agent routing decision itself must still succeed —
	// a bad playbook pick must fail open, not fail the whole request.
	if decision.Agent != agentNameK8s {
		t.Errorf("Agent = %q, want %q even though playbook pick was cleared", decision.Agent, agentNameK8s)
	}
}

func TestRouteWithLLM_PlaybookConfidence_BelowThreshold_Cleared(t *testing.T) {
	pbs := []*audit.Playbook{nodePressurePlaybookFixture()}
	gw := makeRouterGateway(func(_ context.Context, _ string) (string, error) {
		return validRoutingJSONWithPlaybook(agentNameK8s, "pbs_k8s_node_pressure_triage", 0.4), nil
	}, []string{agentNameK8s})

	decision, err := gw.routeWithLLM(context.Background(), "node memory pressure alert", pbs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.PlaybookSeriesID != "" {
		t.Errorf("PlaybookSeriesID = %q, want empty (confidence below 0.75 threshold)", decision.PlaybookSeriesID)
	}
}

// ── handleQuery playbook dispatch ────────────────────────────────────────

func TestHandleQuery_KeywordFastPath_SkipsLLM_SelectsPlaybook(t *testing.T) {
	llmCalled := false
	auditdSrv := mockAuditdForPlaybookSelection(t, []*audit.Playbook{nodePressurePlaybookFixture()})
	ta := &testAuditor{}
	gw := &Gateway{
		agents:   make(map[string]*discovery.Agent),
		clients:  make(map[string]*a2aclient.Client), // absent (not nil-valued) — clean 502 from proxyToAgent
		auditor:  audit.NewGatewayAuditor(ta),
		auditURL: auditdSrv.URL,
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			llmCalled = true
			return validRoutingJSON(agentNameK8s), nil
		},
	}

	rec := postQuery(t, gw, `{"message":"We received a node memory-pressure alert for the node hosting the postgres pod"}`)

	if llmCalled {
		t.Error("routing LLM should not be called when the keyword fast-path finds a strong match")
	}
	// 502 confirms the full dispatch (handleQuery -> runQueryViaPlaybook ->
	// handlePlaybookRun -> agent-mode proxy) was actually taken, not just that
	// a decision was recorded — no A2A client is registered, so the agent
	// proxy step itself is what returns 502.
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (playbook dispatch should reach the agent-proxy step); body: %s",
			rec.Code, rec.Body.String())
	}

	ta.mu.Lock()
	events := ta.events
	ta.mu.Unlock()

	var delegationEvent *audit.Event
	for _, e := range events {
		if e.EventType == audit.EventTypeDelegation {
			delegationEvent = e
		}
	}
	if delegationEvent == nil {
		t.Fatal("no delegation_decision event recorded")
	}
	if delegationEvent.Decision.PlaybookSeriesID != "pbs_k8s_node_pressure_triage" {
		t.Errorf("Decision.PlaybookSeriesID = %q, want pbs_k8s_node_pressure_triage",
			delegationEvent.Decision.PlaybookSeriesID)
	}
}

func TestHandleQuery_NoPlaybookMatch_ProxiesToAgent_Unchanged(t *testing.T) {
	// No entry-point playbooks configured (auditURL unset) — must behave
	// exactly as it did before playbook selection existed.
	ta := &testAuditor{}
	gw := &Gateway{
		agents:  make(map[string]*discovery.Agent),
		clients: make(map[string]*a2aclient.Client),
		auditor: audit.NewGatewayAuditor(ta),
		plannerLLM: func(_ context.Context, _ string) (string, error) {
			return validRoutingJSON(agentNameDB), nil
		},
	}

	rec := postQuery(t, gw, `{"message":"how does VACUUM work in postgres?"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (no agent client registered)", rec.Code)
	}

	ta.mu.Lock()
	events := ta.events
	ta.mu.Unlock()

	var delegationEvent *audit.Event
	for _, e := range events {
		if e.EventType == audit.EventTypeDelegation {
			delegationEvent = e
		}
	}
	if delegationEvent == nil {
		t.Fatal("no delegation_decision event recorded")
	}
	if delegationEvent.Decision.PlaybookSeriesID != "" {
		t.Errorf("Decision.PlaybookSeriesID = %q, want empty — no playbook should be selected",
			delegationEvent.Decision.PlaybookSeriesID)
	}
	if delegationEvent.Decision.Agent != agentNameDB {
		t.Errorf("Decision.Agent = %q, want %q", delegationEvent.Decision.Agent, agentNameDB)
	}
}

func TestHandleQuery_ExplicitAgent_NeverConsultsPlaybooks(t *testing.T) {
	// An auditd server that fails the test if hit at all — an explicit agent
	// request must never call entryPointPlaybooks.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("auditd was contacted (%s %s) — explicit agent requests must skip playbook selection entirely",
			r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	gw := &Gateway{
		agents:   make(map[string]*discovery.Agent),
		clients:  make(map[string]*a2aclient.Client),
		auditURL: srv.URL,
	}

	rec := postQuery(t, gw, `{"agent":"k8s","message":"node memory pressure alert evicted pod"}`)
	if rec.Code == http.StatusInternalServerError {
		t.Fatal("request failed via the auditd stub — playbook selection was consulted")
	}
}
