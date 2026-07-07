package agentutil

import (
	"context"

	"helpdesk/internal/identity"
)

// DirectToolFunc is a tool handler callable with a plain context.Context,
// bypassing the ADK/LLM layer. Used for deterministic fleet job execution.
type DirectToolFunc func(ctx context.Context, args map[string]any) (string, error)

// DirectToolRegistry maps tool names to directly-callable implementations.
type DirectToolRegistry struct {
	tools map[string]DirectToolFunc
}

// NewDirectToolRegistry returns an empty registry.
func NewDirectToolRegistry() *DirectToolRegistry {
	return &DirectToolRegistry{tools: make(map[string]DirectToolFunc)}
}

// Register adds a tool to the registry.
func (r *DirectToolRegistry) Register(name string, fn DirectToolFunc) {
	r.tools[name] = fn
}

// Get returns the handler for the given tool name.
func (r *DirectToolRegistry) Get(name string) (DirectToolFunc, bool) {
	fn, ok := r.tools[name]
	return fn, ok
}

// Len returns the number of registered tools.
func (r *DirectToolRegistry) Len() int {
	return len(r.tools)
}

// DirectToolRequest is the JSON body accepted by POST /tool/{name}.
// It carries both the tool arguments and the trace context so that
// policy enforcement and audit logging are attributed to the originating request.
type DirectToolRequest struct {
	TraceID         string                     `json:"trace_id,omitempty"`
	Principal       identity.ResolvedPrincipal `json:"principal,omitempty"`
	Purpose         string                     `json:"purpose,omitempty"`
	PurposeNote     string                     `json:"purpose_note,omitempty"`
	PurposeExplicit bool                       `json:"purpose_explicit,omitempty"`
	Args            map[string]any             `json:"args"`
}

// DirectToolResponse is the JSON body returned by POST /tool/{name}.
type DirectToolResponse struct {
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

