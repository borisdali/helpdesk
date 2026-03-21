package agentutil

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"helpdesk/internal/audit"
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

// registerDirectToolRoutes adds POST /tool/{name} routes to mux.
// The traceStore is updated for each request so that policy checks inside
// tool implementations can correlate events to the originating trace.
func registerDirectToolRoutes(mux *http.ServeMux, registry *DirectToolRegistry, traceStore *audit.CurrentTraceStore) {
	mux.HandleFunc("POST /tool/{name}", func(w http.ResponseWriter, r *http.Request) {
		toolName := r.PathValue("name")
		fn, ok := registry.Get(toolName)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"unknown tool: %s"}`, toolName) //nolint:errcheck
			return
		}

		var req DirectToolRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid JSON body"}`) //nolint:errcheck
			return
		}

		// Propagate trace context (principal, purpose, trace ID) into the
		// request context so policy enforcement and audit logging are attributed
		// to the originating caller.
		tc := &audit.TraceContext{
			TraceID:         req.TraceID,
			Origin:          "direct_tool",
			Principal:       req.Principal,
			Purpose:         req.Purpose,
			PurposeNote:     req.PurposeNote,
			PurposeExplicit: req.PurposeExplicit,
		}
		ctx := audit.WithTraceContext(r.Context(), tc)

		// Update the CurrentTraceStore so the PolicyEnforcer (which reads from
		// the store, not from context) can correlate its policy decisions with
		// the originating fleet job trace ID.
		if traceStore != nil && req.TraceID != "" {
			traceStore.Set(req.TraceID)
		}

		output, err := fn(ctx, req.Args)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			slog.Warn("direct tool call failed", "tool", toolName, "err", err)
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(DirectToolResponse{Error: err.Error()}) //nolint:errcheck
			return
		}
		json.NewEncoder(w).Encode(DirectToolResponse{Output: output}) //nolint:errcheck
	})
}
