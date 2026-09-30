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
// Tool.Error mirrors internal/audit's real ToolExecution.Error field (wire
// name "error") — decoded here, not just tool.name, so a caller can tell
// "this tool was never called" apart from "this tool was called and failed"
// apart from "this tool succeeded but didn't trip a threshold." Collapsing
// those three into one generic message is exactly what made a real live
// failure (get_pgbackrest_status erroring with "pgbackrest: command not
// found" because the agent had been pointed at the wrong target) show up
// only as an unexplained EVIDENCE COVERAGE GAP, with the actual reason
// buried in the full response text instead of the terminal output —
// confirmed 2026-09-29 diagnosing exactly that run.
type auditEvent struct {
	EventType string `json:"event_type"`
	Tool      *struct {
		Name  string `json:"name"`
		Error string `json:"error,omitempty"`
	} `json:"tool,omitempty"`
}

// auditQueryTools fetches tool execution names and any recorded errors from
// the audit service for the given time window. names is nil when AuditURL is
// empty or the query fails; toolErrors maps a tool name to its most recent
// non-empty error message in the window (last-write-wins — a tool called
// multiple times only needs one representative failure surfaced, not a full
// history here).
//
// It calls GET {auditURL}/v1/events?since=RFC3339&event_type=tool_execution
// and extracts the tool name and error from each matching event.
func auditQueryTools(ctx context.Context, auditURL, apiKey string, since time.Time) (names []string, toolErrors map[string]string) {
	if auditURL == "" {
		return nil, nil
	}

	reqURL := fmt.Sprintf("%s/v1/events?since=%s&event_type=tool_execution",
		auditURL, since.UTC().Format(time.RFC3339))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		slog.Warn("audit query: failed to build request", "err", err)
		return nil, nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("audit query: HTTP request failed", "url", reqURL, "err", err)
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("audit query: unexpected status", "status", resp.StatusCode, "body", string(body))
		return nil, nil
	}

	var events []auditEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		slog.Warn("audit query: failed to decode response", "err", err)
		return nil, nil
	}

	seen := make(map[string]bool)
	for _, e := range events {
		if e.Tool == nil || e.Tool.Name == "" {
			continue
		}
		if !seen[e.Tool.Name] {
			names = append(names, e.Tool.Name)
			seen[e.Tool.Name] = true
		}
		if e.Tool.Error != "" {
			if toolErrors == nil {
				toolErrors = make(map[string]string)
			}
			toolErrors[e.Tool.Name] = e.Tool.Error
		}
	}

	slog.Debug("audit query: found tool executions", "count", len(names), "tools", names, "errors", toolErrors)
	return names, toolErrors
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
