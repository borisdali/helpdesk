package agentutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"helpdesk/internal/audit"
	"helpdesk/internal/authz"
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
// idProvider and authzr enforce inbound auth when enforcing mode is active.
func registerDirectToolRoutes(mux *http.ServeMux, registry *DirectToolRegistry, traceStore *audit.CurrentTraceStore, idProvider identity.Provider, authzr *authz.Authorizer) {
	const pattern = "POST /tool/{name}"
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		// Resolve caller identity and enforce authorization.
		principal, err := idProvider.Resolve(r)
		if err != nil {
			// Unrecognized credential — treat as anonymous and let Authorize decide.
			principal = identity.ResolvedPrincipal{AuthMethod: "header"}
		}
		if authErr := authzr.Authorize(pattern, principal); authErr != nil {
			status := http.StatusForbidden
			if errors.Is(authErr, authz.ErrUnauthorized) {
				status = http.StatusUnauthorized
			}
			slog.Info("direct tool: request denied",
				"principal", principal.EffectiveID(),
				"err", authErr)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"error":%q}`, authErr.Error()) //nolint:errcheck
			return
		}
		toolName := r.PathValue("name")
		fn, ok := registry.Get(toolName)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"unknown tool: %s"}`, toolName) //nolint:errcheck
			return
		}

		// Decode body as a raw map first. If the map contains an "args" key
		// (fleet-runner envelope format), unwrap it and extract trace context.
		// Otherwise treat the entire body as the flat args — matching the shape
		// advertised in input_schema and the tool registry.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid JSON body"}`) //nolint:errcheck
			return
		}

		var req DirectToolRequest
		if _, ok := body["args"]; ok {
			// Envelope format: {"args": {...}, "trace_id": "...", ...}
			// Re-marshal and unmarshal to populate the typed fields.
			data, _ := json.Marshal(body)
			_ = json.Unmarshal(data, &req)
		} else {
			// Flat format: {"target": "...", ...} — body IS the args.
			req.Args = body
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
