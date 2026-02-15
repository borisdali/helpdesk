// Package audit provides structured audit logging for agent delegation decisions.
package audit

import (
	"encoding/json"
	"time"
)

// EventType identifies the type of audit event.
type EventType string

const (
	EventTypeDelegation     EventType = "delegation_decision"
	EventTypeOutcome        EventType = "delegation_outcome"
	EventTypeGatewayRequest EventType = "gateway_request"
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

// Outcome captures the result of a delegation (filled in after completion).
type Outcome struct {
	Status       string        `json:"status"` // success, error, timeout
	ErrorMessage string        `json:"error_message,omitempty"`
	Duration     time.Duration `json:"duration_ms"`
}

// Event is a single audit event for delegation decisions.
type Event struct {
	EventID   string    `json:"event_id"`
	Timestamp time.Time `json:"timestamp"`
	EventType EventType `json:"event_type"`

	Session  Session   `json:"session"`
	Input    Input     `json:"input"`
	Output   *Output   `json:"output,omitempty"`
	Decision *Decision `json:"decision,omitempty"`
	Outcome  *Outcome  `json:"outcome,omitempty"`
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
