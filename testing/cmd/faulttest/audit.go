package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"helpdesk/internal/audit"
)

// auditEvent is a minimal representation of an audit event for tool evidence.
type auditEvent struct {
	EventType string `json:"event_type"`
	Tool      *struct {
		Name string `json:"name"`
	} `json:"tool,omitempty"`
}

// auditQueryTools fetches tool execution names from the audit service for the
// given time window. Returns nil when AuditURL is empty or the query fails.
//
// It calls GET {auditURL}/v1/events?since=RFC3339&event_type=tool_execution
// and extracts the tool name from each matching event.
func auditQueryTools(ctx context.Context, auditURL, apiKey string, since time.Time) []string {
	if auditURL == "" {
		return nil
	}

	reqURL := fmt.Sprintf("%s/v1/events?since=%s&event_type=tool_execution",
		auditURL, since.UTC().Format(time.RFC3339))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		slog.Warn("audit query: failed to build request", "err", err)
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("audit query: HTTP request failed", "url", reqURL, "err", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("audit query: unexpected status", "status", resp.StatusCode, "body", string(body))
		return nil
	}

	var events []auditEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		slog.Warn("audit query: failed to decode response", "err", err)
		return nil
	}

	var tools []string
	seen := make(map[string]bool)
	for _, e := range events {
		if e.Tool == nil || e.Tool.Name == "" {
			continue
		}
		if !seen[e.Tool.Name] {
			tools = append(tools, e.Tool.Name)
			seen[e.Tool.Name] = true
		}
	}

	slog.Debug("audit query: found tool executions", "count", len(tools), "tools", tools)
	return tools
}

// pushJudgeReasoning records the LLM judge's evaluation of an agent's diagnosis
// as an agent_reasoning event in the central audit store. This makes faulttest
// judge verdicts visible alongside live agent reasoning in the governance trail.
// Best-effort: failures are logged at Warn and never abort the run.
func pushJudgeReasoning(ctx context.Context, auditURL, apiKey, traceID, agentName, reasoning string, toolCalls []string) {
	if auditURL == "" || reasoning == "" {
		return
	}
	store := audit.NewRemoteStore(auditURL)
	if apiKey != "" {
		store = store.WithAPIKey(apiKey)
	}
	event := &audit.Event{
		EventID:   "jg_" + uuid.New().String()[:8],
		Timestamp: time.Now().UTC(),
		EventType: audit.EventTypeAgentReasoning,
		TraceID:   traceID,
		Session: audit.Session{
			ID:        traceID,
			AgentName: agentName,
		},
		AgentReasoning: &audit.AgentReasoning{
			Reasoning: reasoning,
			ToolCalls: toolCalls,
		},
	}
	if err := store.Record(ctx, event); err != nil {
		slog.Warn("faulttest: failed to push judge reasoning to audit", "trace_id", traceID, "err", err)
	}
}

// agentNameFromCategory maps a fault category to the canonical agent name used
// in audit events, matching the names registered in the audit governance trail.
func agentNameFromCategory(category string) string {
	switch category {
	case "database":
		return "postgres_database_agent"
	case "kubernetes":
		return "k8s_agent"
	case "host":
		return "sysadmin_agent"
	default:
		return category + "_agent"
	}
}
