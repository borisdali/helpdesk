package audit

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"helpdesk/internal/identity"
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

	// Principal is the verified identity of the caller.
	Principal identity.ResolvedPrincipal `json:"principal,omitempty"`

	// Purpose is the declared or operating-mode-derived reason for this request.
	// One of: diagnostic, remediation, maintenance, compliance, emergency.
	Purpose string `json:"purpose,omitempty"`

	// PurposeNote is an optional free-text explanation (e.g. incident number).
	PurposeNote string `json:"purpose_note,omitempty"`
}

// NewTraceID generates a new trace ID with the default "tr_" prefix.
// Use NewTraceIDWithPrefix to produce a prefix that identifies the call origin.
func NewTraceID() string {
	return NewTraceIDWithPrefix("tr_")
}

// NewTraceIDWithPrefix generates a trace ID with a custom prefix.
// Conventional prefixes used by the gateway:
//
//	"tr_"  — natural-language query  (POST /api/v1/query)
//	"dt_"  — direct tool invocation  (POST /api/v1/db/{tool}, /api/v1/k8s/{tool})
//	"chk_" — direct governance check (POST /v1/governance/check, no agent)
func NewTraceIDWithPrefix(prefix string) string {
	return prefix + uuid.New().String()[:12]
}

// NewTraceContext creates a new trace context for a top-level request.
func NewTraceContext(origin string, principal identity.ResolvedPrincipal) *TraceContext {
	return &TraceContext{
		TraceID:   NewTraceID(),
		Origin:    origin,
		Principal: principal,
	}
}

// Child creates a child trace context with this event as the parent.
func (tc *TraceContext) Child(parentEventID string) *TraceContext {
	return &TraceContext{
		TraceID:     tc.TraceID,
		ParentID:    parentEventID,
		Origin:      tc.Origin,
		Principal:   tc.Principal,
		Purpose:     tc.Purpose,
		PurposeNote: tc.PurposeNote,
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

// PrincipalFromContext extracts the resolved principal from trace context.
// Returns a zero ResolvedPrincipal if no trace context is present.
func PrincipalFromContext(ctx context.Context) identity.ResolvedPrincipal {
	if tc := TraceContextFromContext(ctx); tc != nil {
		return tc.Principal
	}
	return identity.ResolvedPrincipal{}
}

// PurposeFromContext extracts the purpose and purpose note from trace context.
func PurposeFromContext(ctx context.Context) (purpose, purposeNote string) {
	if tc := TraceContextFromContext(ctx); tc != nil {
		return tc.Purpose, tc.PurposeNote
	}
	return "", ""
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
