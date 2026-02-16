package audit

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// traceContextKey is the context key for trace information.
type traceContextKey struct{}

// TraceContext carries trace information through the request chain.
type TraceContext struct {
	// TraceID is the top-level request identifier that correlates all events.
	TraceID string `json:"trace_id"`

	// ParentID is the event ID of the immediate parent (for causality).
	ParentID string `json:"parent_id,omitempty"`

	// Origin identifies where the request originated.
	Origin string `json:"origin"` // "gateway", "orchestrator", "api"

	// Principal is the authenticated user or API key identity.
	Principal string `json:"principal,omitempty"`
}

// NewTraceID generates a new trace ID.
func NewTraceID() string {
	return "tr_" + uuid.New().String()[:12]
}

// NewTraceContext creates a new trace context for a top-level request.
func NewTraceContext(origin, principal string) *TraceContext {
	return &TraceContext{
		TraceID:   NewTraceID(),
		Origin:    origin,
		Principal: principal,
	}
}

// Child creates a child trace context with this event as the parent.
func (tc *TraceContext) Child(parentEventID string) *TraceContext {
	return &TraceContext{
		TraceID:   tc.TraceID,
		ParentID:  parentEventID,
		Origin:    tc.Origin,
		Principal: tc.Principal,
	}
}

// WithTraceContext adds trace context to a context.Context.
func WithTraceContext(ctx context.Context, tc *TraceContext) context.Context {
	return context.WithValue(ctx, traceContextKey{}, tc)
}

// TraceContextFromContext extracts trace context from a context.Context.
// Returns nil if no trace context is present.
func TraceContextFromContext(ctx context.Context) *TraceContext {
	tc, _ := ctx.Value(traceContextKey{}).(*TraceContext)
	return tc
}

// TraceIDFromContext extracts just the trace ID from context.
// Returns empty string if no trace context is present.
func TraceIDFromContext(ctx context.Context) string {
	if tc := TraceContextFromContext(ctx); tc != nil {
		return tc.TraceID
	}
	return ""
}

// CurrentTraceStore provides thread-safe storage for the current trace ID.
// Used when context propagation isn't available (e.g., ADK tools).
type CurrentTraceStore struct {
	mu      sync.RWMutex
	traceID string
}

// Set stores the current trace ID.
func (s *CurrentTraceStore) Set(traceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traceID = traceID
}

// Get retrieves the current trace ID.
func (s *CurrentTraceStore) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.traceID
}

// Clear clears the current trace ID.
func (s *CurrentTraceStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traceID = ""
}
