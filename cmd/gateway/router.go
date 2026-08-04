package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
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
	Agent                  string               `json:"agent"`
	RequestCategory        string               `json:"request_category"`
	Confidence             float64              `json:"confidence"`
	UserIntent             string               `json:"user_intent"`
	ReasoningChain         []string             `json:"reasoning_chain"`
	AlternativesConsidered []RoutingAlternative `json:"alternatives_considered"`
	// PlaybookSeriesID and PlaybookConfidence are set when the LLM (or the
	// keyword pre-filter, via a synthetic decision) selected an existing
	// entry-point triage playbook instead of a bare agent proxy.
	PlaybookSeriesID   string  `json:"playbook_series_id,omitempty"`
	PlaybookConfidence float64 `json:"playbook_confidence,omitempty"`
}

// RoutingAlternative is an agent that was considered but not selected.
type RoutingAlternative struct {
	Agent           string `json:"agent"`
	RejectedBecause string `json:"rejected_because"`
}

// entryPointPlaybookCache caches the active, entry_point:true triage
// playbooks used for query-time auto-selection, avoiding an extra HTTP
// round trip to auditd on every query.
type entryPointPlaybookCache struct {
	mu        sync.Mutex
	playbooks []*audit.Playbook
	fetchedAt time.Time
}

// entryPointPlaybookCacheTTL controls how long entryPointPlaybooks trusts
// its cached result before refetching from auditd.
const entryPointPlaybookCacheTTL = 30 * time.Second

// entryPointPlaybooks returns the active, entry_point:true triage playbooks
// available for query-time auto-selection, using a short-lived cache.
//
// On any error (auditd unreachable, unconfigured, bad response, etc.) this
// returns (nil, err). Callers MUST treat that as "no playbooks available"
// and fall through to ordinary agent routing — this must never turn into a
// hard failure of the query path.
func (g *Gateway) entryPointPlaybooks(ctx context.Context) ([]*audit.Playbook, error) {
	if g.auditURL == "" {
		return nil, fmt.Errorf("auditd not configured")
	}

	g.entryPointCache.mu.Lock()
	if time.Since(g.entryPointCache.fetchedAt) < entryPointPlaybookCacheTTL {
		cached := g.entryPointCache.playbooks
		g.entryPointCache.mu.Unlock()
		return cached, nil
	}
	g.entryPointCache.mu.Unlock()

	url := strings.TrimSuffix(g.auditURL, "/") + "/v1/fleet/playbooks?active_only=true&include_system=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if g.auditAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+g.auditAPIKey)
	}
	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req = req.WithContext(ctx2)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auditd returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var result struct {
		Playbooks []*audit.Playbook `json:"playbooks"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	entryPoints := make([]*audit.Playbook, 0, len(result.Playbooks))
	for _, pb := range result.Playbooks {
		if pb.EntryPoint && pb.PlaybookType == "triage" {
			entryPoints = append(entryPoints, pb)
		}
	}

	g.entryPointCache.mu.Lock()
	g.entryPointCache.playbooks = entryPoints
	g.entryPointCache.fetchedAt = time.Now()
	g.entryPointCache.mu.Unlock()

	return entryPoints, nil
}

// symptomTokenRe splits a symptom string into candidate word tokens.
var symptomTokenRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// symptomTokens tokenizes a symptom string into lowercase words of length
// >= 4, deduplicated. Short words ("is", "the", "pod") are dropped — they're
// too common to be meaningful signal for a substring match.
func symptomTokens(symptom string) []string {
	raw := symptomTokenRe.Split(strings.ToLower(symptom), -1)
	seen := make(map[string]bool, len(raw))
	tokens := make([]string, 0, len(raw))
	for _, tok := range raw {
		if len(tok) < 4 || seen[tok] {
			continue
		}
		seen[tok] = true
		tokens = append(tokens, tok)
	}
	return tokens
}

// matchPlaybookByKeywords is a cheap, deterministic pre-filter that matches
// a free-text query against playbook symptom strings via substring
// tokenization, so the LLM routing call can be skipped entirely on a
// strong, unambiguous match. Returns the matched playbook, the specific
// symptom string it matched, its match score (fraction of that symptom's
// tokens found in the message), and whether the match was strong enough to
// act on.
//
// A match is "strong" only when: the best-scoring playbook has score >= 0.6,
// at least 2 tokens matched, and it beats the next-best DIFFERENT playbook's
// score by >= 0.15 — this margin avoids acting on an ambiguous tie between
// two playbooks with overlapping vocabulary.
func matchPlaybookByKeywords(message string, playbooks []*audit.Playbook) (*audit.Playbook, string, float64, bool) {
	lowerMsg := strings.ToLower(message)

	type candidate struct {
		pb      *audit.Playbook
		symptom string
		score   float64
	}

	// Best match per playbook, across all of that playbook's symptoms —
	// this way a playbook with several matching symptoms doesn't compete
	// against itself for the "second best" slot below.
	bestPerPlaybook := make(map[string]candidate)
	for _, pb := range playbooks {
		for _, symptom := range pb.Symptoms {
			tokens := symptomTokens(symptom)
			if len(tokens) == 0 {
				continue
			}
			matched := 0
			for _, tok := range tokens {
				if strings.Contains(lowerMsg, tok) {
					matched++
				}
			}
			if matched < 2 {
				continue
			}
			score := float64(matched) / float64(len(tokens))
			if cur, ok := bestPerPlaybook[pb.SeriesID]; !ok || score > cur.score {
				bestPerPlaybook[pb.SeriesID] = candidate{pb: pb, symptom: symptom, score: score}
			}
		}
	}

	var best, secondBest candidate
	for _, c := range bestPerPlaybook {
		if c.score > best.score {
			secondBest = best
			best = c
		} else if c.score > secondBest.score {
			secondBest = c
		}
	}

	if best.pb == nil || best.score < 0.6 {
		return nil, "", 0, false
	}
	if best.score-secondBest.score < 0.15 {
		return nil, "", 0, false
	}
	return best.pb, best.symptom, best.score, true
}

// categoryForAgent maps an internal agent name to the request_category
// string used in routing decisions — used by the keyword fast-path to build
// a synthetic RoutingDecision (the LLM path gets this from the model itself).
func categoryForAgent(agentName string) string {
	switch agentName {
	case agentNameDB:
		return "database"
	case agentNameK8s:
		return "kubernetes"
	case agentNameIncident:
		return "incident"
	case agentNameResearch:
		return "research"
	case agentNameSysadmin:
		return "sysadmin"
	default:
		return "unknown"
	}
}

// routeWithLLM uses plannerLLM to select the best agent for the given message,
// optionally also selecting an entry-point triage playbook from candidates.
// Returns an error if the LLM is not configured or the response cannot be parsed.
func (g *Gateway) routeWithLLM(ctx context.Context, message string, playbooks []*audit.Playbook) (*RoutingDecision, error) {
	if g.plannerLLM == nil {
		return nil, fmt.Errorf("LLM routing not configured (HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY required)")
	}

	prompt := g.buildRoutingPrompt(message, playbooks)

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

	// Defend against a hallucinated or low-confidence playbook pick: clear
	// it and fail open into ordinary agent routing rather than erroring the
	// whole request over a bad playbook guess.
	if decision.PlaybookSeriesID != "" {
		valid := false
		for _, pb := range playbooks {
			if pb.SeriesID == decision.PlaybookSeriesID {
				valid = true
				break
			}
		}
		if !valid {
			slog.Warn("gateway router: LLM selected a playbook series_id not in the candidate list; clearing",
				"playbook_series_id", decision.PlaybookSeriesID)
			decision.PlaybookSeriesID = ""
			decision.PlaybookConfidence = 0
		} else if decision.PlaybookConfidence < 0.75 {
			slog.Warn("gateway router: LLM playbook_confidence below threshold; clearing",
				"playbook_series_id", decision.PlaybookSeriesID, "confidence", decision.PlaybookConfidence)
			decision.PlaybookSeriesID = ""
			decision.PlaybookConfidence = 0
		}
	}

	return &decision, nil
}

// buildRoutingPrompt assembles the LLM prompt for agent routing, optionally
// also asking the LLM to select an entry-point triage playbook from candidates.
func (g *Gateway) buildRoutingPrompt(message string, playbooks []*audit.Playbook) string {
	var agentList string
	for name, desc := range routingAgentDescriptions {
		// Only include agents that are actually available.
		if _, ok := g.clients[name]; ok {
			agentList += fmt.Sprintf("  %s — %s\n", name, desc)
		}
	}

	var playbookSection, playbookFieldDoc string
	if len(playbooks) > 0 {
		var pbList string
		for _, pb := range playbooks {
			pbList += fmt.Sprintf("  %s (agent: %s, problem_class: %s) — symptoms: %s\n",
				pb.SeriesID, pb.AgentName, pb.ProblemClass, strings.Join(pb.Symptoms, "; "))
		}
		playbookSection = fmt.Sprintf(`
## Available Triage Playbooks

If the message clearly matches one of these, additionally set "playbook_series_id"
in your response to that playbook's exact series_id and "playbook_confidence"
(0.0-1.0). Leave playbook_series_id empty ("") if none clearly applies — do not
guess. Never invent a series_id not listed here.

%s`, pbList)
		playbookFieldDoc = `,
  "playbook_series_id": "<series_id or empty>",
  "playbook_confidence": <0.0-1.0>`
	}

	return fmt.Sprintf(`You are a request router for an AI operations platform.
Given a user message, select the single best agent to handle it.

## Available Agents

%s%s
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
  ]%s
}`, agentList, playbookSection, message, playbookFieldDoc)
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
			PlaybookSeriesID:       decision.PlaybookSeriesID,
		},
		Outcome: &audit.Outcome{
			Status: "success",
		},
	}

	if err := g.auditor.RecordEvent(ctx, event); err != nil {
		slog.Warn("gateway router: failed to record routing decision", "trace_id", traceID, "err", err)
	}
}
