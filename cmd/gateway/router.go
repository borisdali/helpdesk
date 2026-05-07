package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"helpdesk/internal/audit"
	"helpdesk/internal/identity"
)

// routingAgentDescriptions describes each routable agent for the LLM prompt.
// Keyed by the internal agent name; value is a plain-English description of
// what problems the agent handles.
var routingAgentDescriptions = map[string]string{
	agentNameDB:       "Live PostgreSQL system problems that require querying a running database: connections, locks, replication lag, active queries, pg_stat_* views, configuration drift, performance on a specific server. Use only when the user needs current state from a live system.",
	agentNameK8s:      "Live Kubernetes cluster problems that require kubectl: pods, deployments, services, endpoints, events, node resources, CrashLoopBackOff, OOMKilled. Use only when the user needs current state from a live cluster.",
	agentNameIncident: "Incident creation and investigation: creating incident bundles, listing past incidents, cross-system triage that spans database and infrastructure.",
	agentNameResearch: "Conceptual questions, how-does-it-work explanations, documentation lookup, and best-practice advice that do not require querying a live system. Examples: explaining VACUUM vs VACUUM FULL, what WAL is, how connection pooling works, what a CrashLoopBackOff means. Prefer this agent whenever the question can be answered from knowledge rather than live data.",
	agentNameSysadmin: "Live host/OS-level problems that require shell access: CPU, memory, disk, running processes, system journal, filesystem, non-Kubernetes Linux infrastructure.",
}

// RoutingDecision is the LLM's structured response when routing a query.
type RoutingDecision struct {
	Agent                  string             `json:"agent"`
	RequestCategory        string             `json:"request_category"`
	Confidence             float64            `json:"confidence"`
	UserIntent             string             `json:"user_intent"`
	ReasoningChain         []string           `json:"reasoning_chain"`
	AlternativesConsidered []RoutingAlternative `json:"alternatives_considered"`
}

// RoutingAlternative is an agent that was considered but not selected.
type RoutingAlternative struct {
	Agent           string `json:"agent"`
	RejectedBecause string `json:"rejected_because"`
}

// routeWithLLM uses plannerLLM to select the best agent for the given message.
// Returns an error if the LLM is not configured or the response cannot be parsed.
func (g *Gateway) routeWithLLM(ctx context.Context, message string) (*RoutingDecision, error) {
	if g.plannerLLM == nil {
		return nil, fmt.Errorf("LLM routing not configured (HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY required)")
	}

	prompt := g.buildRoutingPrompt(message)

	var decision RoutingDecision
	for attempt := 1; attempt <= 2; attempt++ {
		raw, err := g.plannerLLM(ctx, prompt)
		if err != nil {
			return nil, fmt.Errorf("routing LLM call failed: %w", err)
		}
		raw = stripMarkdownFences(raw)
		if err := json.Unmarshal([]byte(raw), &decision); err != nil {
			slog.Warn("gateway router: failed to parse LLM response",
				"attempt", attempt, "raw", raw, "err", err)
			if attempt == 2 {
				return nil, fmt.Errorf("routing LLM returned unparseable JSON after %d attempts: %w", attempt, err)
			}
			continue
		}
		break
	}

	// Validate the chosen agent is real.
	if _, ok := routingAgentDescriptions[decision.Agent]; !ok {
		return nil, fmt.Errorf("routing LLM returned unknown agent %q", decision.Agent)
	}

	return &decision, nil
}

// buildRoutingPrompt assembles the LLM prompt for agent routing.
func (g *Gateway) buildRoutingPrompt(message string) string {
	var agentList string
	for name, desc := range routingAgentDescriptions {
		// Only include agents that are actually available.
		if _, ok := g.clients[name]; ok {
			agentList += fmt.Sprintf("  %s — %s\n", name, desc)
		}
	}

	return fmt.Sprintf(`You are a request router for an AI operations platform.
Given a user message, select the single best agent to handle it.

## Available Agents

%s
## Instructions

- Choose exactly one agent from the list above.
- Set confidence between 0.0 and 1.0 (how certain you are this is the right agent).
- Provide 1–3 reasoning_chain steps explaining your choice.
- For each agent you considered but did not choose, add an entry in alternatives_considered with rejected_because.
- request_category must be one of: database, kubernetes, incident, research, sysadmin, fleet, unknown.
- Key routing rule: if the question can be answered from knowledge or documentation without querying a live system, choose research_agent even if the topic is PostgreSQL or Kubernetes.
- Output ONLY valid JSON. Do not insert any words, punctuation, or characters outside of JSON string values.

## User Message

%q

## Response Format

Respond with ONLY valid JSON — no markdown fences, no prose, nothing outside the JSON object itself:
{
  "agent": "<internal agent name>",
  "request_category": "<category>",
  "confidence": <0.0-1.0>,
  "user_intent": "<one sentence describing what the user wants>",
  "reasoning_chain": ["<step 1>", "<step 2>"],
  "alternatives_considered": [
    {"agent": "<name>", "rejected_because": "<reason>"}
  ]
}`, agentList, message)
}

// recordRoutingDecision emits a delegation_decision audit event for the
// LLM routing choice. This mirrors the orchestrator's delegate_to_agent
// audit pattern so query journeys through the gateway are fully traceable.
func (g *Gateway) recordRoutingDecision(ctx context.Context, traceID string, principal identity.ResolvedPrincipal, decision *RoutingDecision) {
	if g.auditor == nil {
		return
	}

	alts := make([]audit.Alternative, 0, len(decision.AlternativesConsidered))
	for _, a := range decision.AlternativesConsidered {
		alts = append(alts, audit.Alternative{
			Agent:           a.Agent,
			RejectedBecause: a.RejectedBecause,
		})
	}

	var p *identity.ResolvedPrincipal
	if principal.EffectiveID() != "" {
		p = &principal
	}

	event := &audit.Event{
		EventID:   "rt_" + uuid.New().String()[:8],
		Timestamp: time.Now().UTC(),
		EventType: audit.EventTypeDelegation,
		TraceID:   traceID,
		Principal: p,
		Session: audit.Session{
			ID: traceID,
		},
		Input: audit.Input{
			UserQuery: decision.UserIntent,
		},
		Decision: &audit.Decision{
			Agent:                  decision.Agent,
			RequestCategory:        audit.RequestCategory(decision.RequestCategory),
			Confidence:             decision.Confidence,
			UserIntent:             decision.UserIntent,
			ReasoningChain:         decision.ReasoningChain,
			AlternativesConsidered: alts,
		},
		Outcome: &audit.Outcome{
			Status: "success",
		},
	}

	if err := g.auditor.RecordEvent(ctx, event); err != nil {
		slog.Warn("gateway router: failed to record routing decision", "trace_id", traceID, "err", err)
	}
}
