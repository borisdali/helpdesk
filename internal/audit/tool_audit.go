package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// ToolAuditor wraps tool executions with audit logging.
type ToolAuditor struct {
	auditor    Auditor
	agentName  string
	sessionID  string
	traceID    string             // Static trace ID (fallback)
	traceStore *CurrentTraceStore // Dynamic trace ID from incoming requests
}

// NewToolAuditor creates a new tool auditor for an agent.
// If auditor is nil, auditing is disabled (no-op).
func NewToolAuditor(auditor Auditor, agentName, sessionID, traceID string) *ToolAuditor {
	return &ToolAuditor{
		auditor:   auditor,
		agentName: agentName,
		sessionID: sessionID,
		traceID:   traceID,
	}
}

// NewToolAuditorWithTraceStore creates a tool auditor that gets trace_id dynamically.
// The traceStore is populated by TraceMiddleware from incoming A2A requests.
func NewToolAuditorWithTraceStore(auditor Auditor, agentName, sessionID string, traceStore *CurrentTraceStore) *ToolAuditor {
	return &ToolAuditor{
		auditor:    auditor,
		agentName:  agentName,
		sessionID:  sessionID,
		traceStore: traceStore,
	}
}

// getTraceID returns the current trace ID, preferring the dynamic store.
func (ta *ToolAuditor) getTraceID() string {
	if ta.traceStore != nil {
		if traceID := ta.traceStore.Get(); traceID != "" {
			return traceID
		}
	}
	return ta.traceID
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
	if ta.auditor == nil {
		return
	}

	// Classify the action based on tool name
	actionClass := ClassifyTool(call.Name)
	traceID := ta.getTraceID()

	event := &Event{
		EventID:     "tool_" + uuid.New().String()[:8],
		Timestamp:   time.Now().UTC(),
		EventType:   EventTypeToolExecution,
		TraceID:     traceID,
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
			Agent:      ta.agentName, // Track which agent executed this tool
		},
		// No Decision for tool executions - they're not LLM decisions
		Outcome: &Outcome{
			Status:   outcomeStatus(result.Error),
			Duration: duration,
		},
	}

	if result.Error != "" {
		event.Outcome.ErrorMessage = result.Error
	}

	if err := ta.auditor.Record(ctx, event); err != nil {
		slog.Warn("failed to record tool audit event", "tool", call.Name, "err", err)
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
