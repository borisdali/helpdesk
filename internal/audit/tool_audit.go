package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// ToolAuditor wraps tool executions with audit logging.
type ToolAuditor struct {
	store     *Store
	agentName string
	sessionID string
	traceID   string
}

// NewToolAuditor creates a new tool auditor for an agent.
// If store is nil, auditing is disabled (no-op).
func NewToolAuditor(store *Store, agentName, sessionID, traceID string) *ToolAuditor {
	return &ToolAuditor{
		store:     store,
		agentName: agentName,
		sessionID: sessionID,
		traceID:   traceID,
	}
}

// ToolCall represents a tool invocation to be audited.
type ToolCall struct {
	Name       string
	Parameters map[string]any
	RawCommand string // e.g., the actual SQL query or kubectl command
}

// ToolResult represents the result of a tool invocation.
type ToolResult struct {
	Output string
	Error  string
}

// RecordToolCall records a tool execution event.
// Call this after the tool has executed with its result.
func (ta *ToolAuditor) RecordToolCall(ctx context.Context, call ToolCall, result ToolResult, duration time.Duration) {
	if ta.store == nil {
		return
	}

	// Classify the action based on tool name
	actionClass := ClassifyTool(call.Name)

	event := &Event{
		EventID:     "tool_" + uuid.New().String()[:8],
		Timestamp:   time.Now().UTC(),
		EventType:   EventTypeDelegation, // Tool execution is part of delegation
		TraceID:     ta.traceID,
		ActionClass: actionClass,
		Session: Session{
			ID: ta.sessionID,
		},
		Input: Input{
			UserQuery: call.RawCommand, // Store the actual command as the "query"
		},
		Tool: &ToolExecution{
			Name:       call.Name,
			Parameters: call.Parameters,
			RawCommand: call.RawCommand,
			Result:     truncateString(result.Output, 500),
			Error:      result.Error,
			Duration:   duration,
		},
		Decision: &Decision{
			Agent:           ta.agentName,
			RequestCategory: categorizeToolAgent(ta.agentName),
		},
		Outcome: &Outcome{
			Status:   outcomeStatus(result.Error),
			Duration: duration,
		},
	}

	if result.Error != "" {
		event.Outcome.ErrorMessage = result.Error
	}

	if err := ta.store.Record(ctx, event); err != nil {
		slog.Warn("failed to record tool audit event", "tool", call.Name, "err", err)
	} else {
		slog.Debug("tool execution audited",
			"tool", call.Name,
			"action_class", actionClass,
			"duration", duration,
			"trace_id", ta.traceID)
	}
}

func outcomeStatus(errMsg string) string {
	if errMsg != "" {
		return "error"
	}
	return "success"
}

func categorizeToolAgent(agentName string) RequestCategory {
	switch agentName {
	case "postgres_database_agent":
		return CategoryDatabase
	case "k8s_agent":
		return CategoryKubernetes
	case "incident_agent":
		return CategoryIncident
	case "research_agent":
		return CategoryResearch
	default:
		return CategoryUnknown
	}
}
