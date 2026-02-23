// Package audit provides structured audit logging for agent delegation decisions.
package audit

import (
	"encoding/json"
	"time"
)

// EventType identifies the type of audit event.
type EventType string

const (
	EventTypeDelegation          EventType = "delegation_decision"
	EventTypeOutcome             EventType = "delegation_outcome"
	EventTypeGatewayRequest      EventType = "gateway_request"
	EventTypeToolExecution       EventType = "tool_execution"
	EventTypePolicyDecision      EventType = "policy_decision"
	EventTypeAgentReasoning      EventType = "agent_reasoning"
	EventTypeGovernanceViolation EventType = "governance_violation"
)

// RequestCategory classifies the type of user request.
type RequestCategory string

const (
	CategoryDatabase   RequestCategory = "database"
	CategoryKubernetes RequestCategory = "kubernetes"
	CategoryIncident   RequestCategory = "incident"
	CategoryResearch   RequestCategory = "research"
	CategoryUnknown    RequestCategory = "unknown"
)

// Alternative represents an agent that was considered but not chosen.
type Alternative struct {
	Agent          string `json:"agent"`
	RejectedBecause string `json:"rejected_because"`
}

// Decision captures the routing decision made by the orchestrator.
type Decision struct {
	Agent                  string          `json:"agent"`
	RequestCategory        RequestCategory `json:"request_category"`
	Confidence             float64         `json:"confidence"`
	UserIntent             string          `json:"user_intent"`
	ReasoningChain         []string        `json:"reasoning_chain"`
	AlternativesConsidered []Alternative   `json:"alternatives_considered"`
}

// Session identifies the user session context.
type Session struct {
	ID              string    `json:"id"`
	UserID          string    `json:"user_id,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	DelegationCount int       `json:"delegation_count"`
}

// Input captures the user's request and context.
type Input struct {
	UserQuery             string   `json:"user_query"`
	InfrastructureContext []string `json:"infrastructure_context,omitempty"`
}

// Output captures the agent's response.
type Output struct {
	Response string `json:"response,omitempty"`
}

// ToolExecution captures details of a tool invocation.
type ToolExecution struct {
	// Name is the tool that was called (e.g., "check_connection", "get_pods").
	Name string `json:"name"`

	// Agent is the agent that executed this tool (for tool_execution events).
	Agent string `json:"agent,omitempty"`

	// Parameters are the arguments passed to the tool.
	Parameters map[string]any `json:"parameters,omitempty"`

	// RawCommand is the actual command executed (e.g., SQL query, kubectl command).
	// This is filled in by the agent when available.
	RawCommand string `json:"raw_command,omitempty"`

	// Result is a summary of the tool's output.
	Result string `json:"result,omitempty"`

	// Error contains any error message if the tool failed.
	Error string `json:"error,omitempty"`

	// Duration is how long the tool execution took.
	Duration time.Duration `json:"duration_ms,omitempty"`
}

// Outcome captures the result of a delegation (filled in after completion).
type Outcome struct {
	Status       string        `json:"status"` // success, error, timeout
	ErrorMessage string        `json:"error_message,omitempty"`
	Duration     time.Duration `json:"duration_ms"`
}

// PolicyDecision captures the outcome of a policy evaluation.
// Emitted by PolicyEnforcer before every tool execution, regardless of outcome.
type PolicyDecision struct {
	ResourceType string   `json:"resource_type"`          // "database", "kubernetes"
	ResourceName string   `json:"resource_name"`          // db name, namespace, etc.
	Action       string   `json:"action"`                 // "read", "write", "destructive"
	Tags         []string `json:"tags,omitempty"`         // resource tags used for matching
	Effect       string   `json:"effect"`                 // "allow", "deny", "require_approval"
	PolicyName   string   `json:"policy_name"`            // which policy matched
	RuleIndex    int      `json:"rule_index,omitempty"`   // index of the matched rule within the policy
	Message      string   `json:"message,omitempty"`      // denial or approval message from the policy rule
	Note         string   `json:"note,omitempty"`         // diagnostic context (e.g. why tags are missing)
	DryRun       bool     `json:"dry_run,omitempty"`      // true when policy is in dry-run mode
	PostExecution bool    `json:"post_execution,omitempty"` // true for post-execution blast-radius checks

	// Explainability fields — populated by agentutil.PolicyEnforcer when using engine.Explain().
	// Trace is the JSON-serialised policy.DecisionTrace (stored as raw JSON to avoid import cycles).
	Trace       json.RawMessage `json:"trace,omitempty"`       // full evaluation trace
	Explanation string          `json:"explanation,omitempty"` // human-readable explanation
}

// AgentReasoning captures the LLM's text deliberation immediately before
// it issues one or more tool calls. This fills the gap between policy
// decision events (which rule matched) and tool execution events (what ran):
// it records *why* the agent decided to call a specific tool.
type AgentReasoning struct {
	// Reasoning is the model's text output that preceded the tool call(s).
	Reasoning string `json:"reasoning"`
	// ToolCalls lists the names of the tools the model decided to invoke.
	ToolCalls []string `json:"tool_calls"`
}

// GovernanceViolation records a compliance violation when a required governance
// module is disabled or misconfigured in fix mode (HELPDESK_OPERATING_MODE=fix).
type GovernanceViolation struct {
	OperatingMode string `json:"operating_mode"` // "fix"
	Module        string `json:"module"`          // "audit", "policy_engine", "guardrails", etc.
	Severity      string `json:"severity"`        // "fatal" or "warning"
	Description   string `json:"description"`
	Remediation   string `json:"remediation,omitempty"`
}

// Event is a single audit event for delegation decisions.
type Event struct {
	EventID   string    `json:"event_id"`
	Timestamp time.Time `json:"timestamp"`
	EventType EventType `json:"event_type"`

	// Trace fields for end-to-end correlation
	TraceID  string `json:"trace_id,omitempty"`  // correlates all events in a request chain
	ParentID string `json:"parent_id,omitempty"` // immediate parent event (causality)

	// Action classification for approval workflow
	ActionClass ActionClass `json:"action_class,omitempty"` // read, write, destructive

	// Hash chain for tamper evidence
	PrevHash  string `json:"prev_hash,omitempty"`  // hash of previous event
	EventHash string `json:"event_hash,omitempty"` // hash of this event

	Session  Session   `json:"session"`
	Input    Input     `json:"input"`
	Output   *Output   `json:"output,omitempty"`
	Tool                *ToolExecution       `json:"tool,omitempty"`
	Approval            *Approval            `json:"approval,omitempty"`
	Decision            *Decision            `json:"decision,omitempty"`
	PolicyDecision      *PolicyDecision      `json:"policy_decision,omitempty"`
	AgentReasoning      *AgentReasoning      `json:"agent_reasoning,omitempty"`
	GovernanceViolation *GovernanceViolation `json:"governance_violation,omitempty"`
	Outcome             *Outcome             `json:"outcome,omitempty"`
}

// MarshalJSON returns the JSON encoding of the event.
func (e *Event) MarshalJSON() ([]byte, error) {
	type Alias Event
	return json.Marshal(&struct {
		*Alias
		Timestamp string `json:"timestamp"`
	}{
		Alias:     (*Alias)(e),
		Timestamp: e.Timestamp.Format(time.RFC3339Nano),
	})
}

// String returns a JSON string representation of the event.
func (e *Event) String() string {
	b, _ := json.Marshal(e)
	return string(b)
}
