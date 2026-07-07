// Package serve wires ADK agents into HTTP/A2A servers.
// It is separate from the agentutil library so that non-server consumers
// (faulttest, gateway) can import agentutil without pulling in ADK server
// machinery (adka2a, runner, session, a2asrv).
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/server/adka2a"
	"google.golang.org/adk/session"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/authz"
	"helpdesk/internal/identity"
)

// InitApprovalClient initializes an approval client if the approval workflow is enabled.
// Returns nil when disabled or when HELPDESK_AUDIT_URL is unset.
func InitApprovalClient(cfg agentutil.Config) *audit.ApprovalClient {
	if !cfg.ApprovalEnabled {
		slog.Debug("approval workflow disabled (HELPDESK_APPROVAL_ENABLED not set)")
		return nil
	}

	if cfg.AuditURL == "" {
		slog.Warn("approval workflow requires HELPDESK_AUDIT_URL to be set")
		return nil
	}

	slog.Info("approval workflow enabled",
		"audit_url", cfg.AuditURL,
		"timeout", cfg.ApprovalTimeout)

	client := audit.NewApprovalClient(cfg.AuditURL)
	if cfg.AuditAPIKey != "" {
		client = client.WithAPIKey(cfg.AuditAPIKey)
	}
	return client
}

// InitAuditStore initializes an audit store for an agent if auditing is enabled.
// Returns nil if auditing is disabled. The caller should defer store.Close() if non-nil.
// If HELPDESK_AUDIT_URL is set, uses the central audit service (preferred).
// Otherwise falls back to local SQLite if HELPDESK_AUDIT_DIR is set.
func InitAuditStore(cfg agentutil.Config) (audit.Auditor, error) {
	if !cfg.AuditEnabled {
		return nil, nil
	}

	// Prefer central audit service
	if cfg.AuditURL != "" {
		slog.Info("agent audit logging enabled (remote)", "url", cfg.AuditURL)
		store := audit.NewRemoteStore(cfg.AuditURL)
		if cfg.AuditAPIKey != "" {
			store = store.WithAPIKey(cfg.AuditAPIKey)
		}
		return store, nil
	}

	// Fall back to local SQLite
	auditDir := cfg.AuditDir
	if auditDir == "" {
		auditDir = "."
	}

	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(auditDir, "audit.db"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create audit store: %w", err)
	}

	slog.Info("agent audit logging enabled (local)", "db", filepath.Join(auditDir, "audit.db"))
	return store, nil
}

// registerSchemasHandler registers GET /schemas on mux.
func registerSchemasHandler(mux *http.ServeMux, schemas map[string]map[string]any) {
	if schemas == nil {
		schemas = map[string]map[string]any{}
	}
	b, err := json.Marshal(schemas)
	if err != nil {
		slog.Warn("agentutil/serve: failed to marshal tool schemas", "err", err)
		return
	}
	mux.HandleFunc("GET /schemas", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(b) //nolint:errcheck
	})
}

// =============================================================================
// Tool-call tracking — injects structured tool call data into the A2A response
// so faulttest (and other evaluation clients) can use exact tool-name matching
// instead of brittle output-text heuristics.
// =============================================================================

// HelpdeskToolCallSummaryMetaKey is the DataPart metadata key used to mark the
// tool-call-summary artifact part emitted at the end of every A2A response.
// testutil.extractResponse looks for this key to populate AgentResponse.ToolCalls.
const HelpdeskToolCallSummaryMetaKey = "helpdesk_type"

// HelpdeskToolCallSummaryMetaValue is the value of HelpdeskToolCallSummaryMetaKey.
const HelpdeskToolCallSummaryMetaValue = "tool_call_summary"

type toolCallStoreKey struct{}

type toolCallStore struct {
	mu    sync.Mutex
	names []string
}

func (s *toolCallStore) add(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names = append(s.names, name)
}

func (s *toolCallStore) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.names) == 0 {
		return nil
	}
	out := make([]string, len(s.names))
	copy(out, s.names)
	return out
}

func toolCallStoreFromContext(ctx context.Context) *toolCallStore {
	s, _ := ctx.Value(toolCallStoreKey{}).(*toolCallStore)
	return s
}

func newToolCallCallbacks() (adka2a.BeforeExecuteCallback, adka2a.AfterEventCallback) {
	before := func(ctx context.Context, _ *a2asrv.RequestContext) (context.Context, error) {
		return context.WithValue(ctx, toolCallStoreKey{}, &toolCallStore{}), nil
	}

	after := func(ctx adka2a.ExecutorContext, adkEvent *session.Event, processed *a2a.TaskArtifactUpdateEvent) error {
		if adkEvent.Content != nil {
			if store := toolCallStoreFromContext(ctx); store != nil {
				for _, part := range adkEvent.Content.Parts {
					if part.FunctionCall != nil && part.FunctionCall.Name != "" {
						store.add(part.FunctionCall.Name)
					}
				}
			}
		}

		if adkEvent.IsFinalResponse() && processed != nil && processed.Artifact != nil {
			if store := toolCallStoreFromContext(ctx); store != nil {
				if names := store.snapshot(); len(names) > 0 {
					processed.Artifact.Parts = append(processed.Artifact.Parts, a2a.DataPart{
						Data: map[string]any{"tool_calls": names},
						Metadata: map[string]any{
							HelpdeskToolCallSummaryMetaKey: HelpdeskToolCallSummaryMetaValue,
						},
					})
				}
			}
		}
		return nil
	}

	return before, after
}

// registerDirectToolRoutes adds POST /tool/{name} routes to mux.
func registerDirectToolRoutes(mux *http.ServeMux, registry *agentutil.DirectToolRegistry, traceStore *audit.CurrentTraceStore, idProvider identity.Provider, authzr *authz.Authorizer) {
	const pattern = "POST /tool/{name}"
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		principal, err := idProvider.Resolve(r)
		if err != nil {
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

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid JSON body"}`) //nolint:errcheck
			return
		}

		var req agentutil.DirectToolRequest
		if _, ok := body["args"]; ok {
			data, _ := json.Marshal(body)
			_ = json.Unmarshal(data, &req)
		} else {
			req.Args = body
		}

		tc := &audit.TraceContext{
			TraceID:         req.TraceID,
			Origin:          "direct_tool",
			Principal:       req.Principal,
			Purpose:         req.Purpose,
			PurposeNote:     req.PurposeNote,
			PurposeExplicit: req.PurposeExplicit,
		}
		ctx := audit.WithTraceContext(r.Context(), tc)

		if traceStore != nil && req.TraceID != "" {
			traceStore.Set(req.TraceID)
		}

		target, _ := req.Args["target"].(string)

		start := time.Now()
		output, err := fn(ctx, req.Args)
		ms := time.Since(start).Milliseconds()
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			slog.Warn("direct tool call failed", "tool", toolName, "target", target, "err", err, "ms", ms)
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(agentutil.DirectToolResponse{Error: err.Error()}) //nolint:errcheck
			return
		}
		slog.Debug("direct tool: ok", "tool", toolName, "target", target, "ms", ms)
		json.NewEncoder(w).Encode(agentutil.DirectToolResponse{Output: output}) //nolint:errcheck
	})
}

// Serve starts an A2A server for the given agent on cfg.ListenAddr.
func Serve(ctx context.Context, a agent.Agent, cfg agentutil.Config, opts ...agentutil.CardOptions) error {
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind to %s: %v", cfg.ListenAddr, err)
	}

	var baseURL *url.URL
	if cfg.ExternalURL != "" {
		baseURL, err = url.Parse(cfg.ExternalURL)
		if err != nil {
			return fmt.Errorf("invalid HELPDESK_AGENT_URL: %v", err)
		}
	} else {
		baseURL = &url.URL{Scheme: "http", Host: listener.Addr().String()}
	}

	agentPath := "/invoke"
	agentCard := &a2a.AgentCard{
		Name:               a.Name(),
		Description:        a.Description(),
		Skills:             adka2a.BuildAgentSkills(a),
		PreferredTransport: a2a.TransportProtocolJSONRPC,
		URL:                baseURL.JoinPath(agentPath).String(),
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
	}

	if len(opts) > 0 {
		agentutil.ApplyCardOptions(agentCard, opts[0])
	}

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(agentCard))

	var toolSchemas map[string]map[string]any
	if len(opts) > 0 {
		toolSchemas = opts[0].ToolSchemas
	}
	registerSchemasHandler(mux, toolSchemas)

	toolCallBefore, toolCallAfter := newToolCallCallbacks()
	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        a.Name(),
			Agent:          a,
			SessionService: session.InMemoryService(),
		},
		BeforeExecuteCallback: toolCallBefore,
		AfterEventCallback:    toolCallAfter,
	})
	requestHandler := a2asrv.NewHandler(executor)
	mux.Handle(agentPath, a2asrv.NewJSONRPCHandler(requestHandler))

	slog.Info("starting A2A server",
		"agent", a.Name(),
		"url", baseURL.String(),
		"card", baseURL.String()+"/.well-known/agent-card.json",
	)

	return http.Serve(listener, mux)
}

// ServeWithTracing starts an A2A server with trace_id extraction from incoming messages.
func ServeWithTracing(ctx context.Context, a agent.Agent, cfg agentutil.Config, traceStore *audit.CurrentTraceStore, auditor audit.Auditor, opts ...agentutil.CardOptions) error {
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind to %s: %v", cfg.ListenAddr, err)
	}

	var baseURL *url.URL
	if cfg.ExternalURL != "" {
		baseURL, err = url.Parse(cfg.ExternalURL)
		if err != nil {
			return fmt.Errorf("invalid HELPDESK_AGENT_URL: %v", err)
		}
	} else {
		baseURL = &url.URL{Scheme: "http", Host: listener.Addr().String()}
	}

	agentPath := "/invoke"
	agentCard := &a2a.AgentCard{
		Name:               a.Name(),
		Description:        a.Description(),
		Skills:             adka2a.BuildAgentSkills(a),
		PreferredTransport: a2a.TransportProtocolJSONRPC,
		URL:                baseURL.JoinPath(agentPath).String(),
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
	}

	if len(opts) > 0 {
		agentutil.ApplyCardOptions(agentCard, opts[0])
	}

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(agentCard))

	var toolSchemas map[string]map[string]any
	if len(opts) > 0 {
		toolSchemas = opts[0].ToolSchemas
	}
	registerSchemasHandler(mux, toolSchemas)

	toolCallBefore, toolCallAfter := newToolCallCallbacks()
	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        a.Name(),
			Agent:          a,
			SessionService: session.InMemoryService(),
		},
		BeforeExecuteCallback: toolCallBefore,
		AfterEventCallback:    toolCallAfter,
	})
	requestHandler := a2asrv.NewHandler(executor)

	tracedHandler := audit.TraceMiddlewareWithAudit(traceStore, auditor, a.Name(), a2asrv.NewJSONRPCHandler(requestHandler))
	mux.Handle(agentPath, tracedHandler)

	slog.Info("starting A2A server with tracing",
		"agent", a.Name(),
		"url", baseURL.String(),
		"card", baseURL.String()+"/.well-known/agent-card.json",
	)

	return http.Serve(listener, mux)
}

// ServeWithTracingAndDirectTools is like ServeWithTracing but also registers
// a POST /tool/{name} endpoint for deterministic, LLM-bypassing tool dispatch.
func ServeWithTracingAndDirectTools(ctx context.Context, a agent.Agent, cfg agentutil.Config, traceStore *audit.CurrentTraceStore, auditor audit.Auditor, registry *agentutil.DirectToolRegistry, opts ...agentutil.CardOptions) error {
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind to %s: %v", cfg.ListenAddr, err)
	}

	var baseURL *url.URL
	if cfg.ExternalURL != "" {
		baseURL, err = url.Parse(cfg.ExternalURL)
		if err != nil {
			return fmt.Errorf("invalid HELPDESK_AGENT_URL: %v", err)
		}
	} else {
		baseURL = &url.URL{Scheme: "http", Host: listener.Addr().String()}
	}

	agentPath := "/invoke"
	agentCard := &a2a.AgentCard{
		Name:               a.Name(),
		Description:        a.Description(),
		Skills:             adka2a.BuildAgentSkills(a),
		PreferredTransport: a2a.TransportProtocolJSONRPC,
		URL:                baseURL.JoinPath(agentPath).String(),
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
	}

	if len(opts) > 0 {
		agentutil.ApplyCardOptions(agentCard, opts[0])
	}

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(agentCard))

	var toolSchemas map[string]map[string]any
	if len(opts) > 0 {
		toolSchemas = opts[0].ToolSchemas
	}
	registerSchemasHandler(mux, toolSchemas)

	toolCallBefore, toolCallAfter := newToolCallCallbacks()
	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        a.Name(),
			Agent:          a,
			SessionService: session.InMemoryService(),
		},
		BeforeExecuteCallback: toolCallBefore,
		AfterEventCallback:    toolCallAfter,
	})
	requestHandler := a2asrv.NewHandler(executor)

	tracedHandler := audit.TraceMiddlewareWithAudit(traceStore, auditor, a.Name(), a2asrv.NewJSONRPCHandler(requestHandler))
	mux.Handle(agentPath, tracedHandler)

	if registry != nil {
		var idProvider identity.Provider = &identity.NoAuthProvider{}
		idMode := os.Getenv("HELPDESK_IDENTITY_PROVIDER")
		enforcing := cfg.UsersFile != "" && idMode != "none"
		if enforcing {
			p, err := identity.NewStaticProvider(cfg.UsersFile)
			if err != nil {
				return fmt.Errorf("agent inbound auth: failed to load users file %q: %w", cfg.UsersFile, err)
			}
			idProvider = p
			slog.Info("agent inbound auth enabled", "users_file", cfg.UsersFile)
		} else {
			slog.Warn("POST /tool/{name} is unauthenticated — set HELPDESK_USERS_FILE to require service-account credentials")
		}
		authzr := authz.NewAuthorizer(authz.DefaultAgentPermissions, enforcing)
		registerDirectToolRoutes(mux, registry, traceStore, idProvider, authzr)
		slog.Info("direct tool dispatch enabled", "agent", a.Name(), "tools", registry.Len())
	}

	if len(opts) > 0 {
		for pattern, handler := range opts[0].ExtraHandlers {
			mux.Handle(pattern, handler)
		}
	}

	slog.Info("starting A2A server with tracing",
		"agent", a.Name(),
		"url", baseURL.String(),
		"card", baseURL.String()+"/.well-known/agent-card.json",
	)

	return http.Serve(listener, mux)
}
