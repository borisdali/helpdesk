// Package agentutil provides the SDK surface for building helpdesk agents.
// It extracts the boilerplate duplicated across sub-agents: config loading,
// LLM creation, and A2A server startup.
package agentutil

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"
	"google.golang.org/genai"

	"google.golang.org/adk/agent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/server/adka2a"
	"google.golang.org/adk/session"

	"helpdesk/internal/audit"
	"helpdesk/internal/logging"
	"helpdesk/internal/model"
	"helpdesk/internal/policy"
)

// Config holds common agent configuration from HELPDESK_* env vars.
type Config struct {
	ModelVendor string
	ModelName   string
	APIKey      string
	ListenAddr  string
	ExternalURL string // Optional: externally reachable URL for the agent card

	// Audit configuration
	AuditEnabled bool
	AuditURL     string // URL of central audit service (preferred)
	AuditDir     string // Local directory for audit.db (fallback if AuditURL not set)

	// Policy configuration
	PolicyFile    string // Path to policy YAML file
	PolicyDryRun  bool   // Log policy decisions but don't enforce
	DefaultPolicy string // "allow" or "deny" when no policy matches (default: "deny")
}

// MustLoadConfig reads env vars. defaultAddr is used when HELPDESK_AGENT_ADDR is unset.
// Exits the process if required vars (MODEL_VENDOR, MODEL_NAME, API_KEY) are missing.
// It also initialises structured logging via logging.InitLogging.
func MustLoadConfig(defaultAddr string) Config {
	logging.InitLogging(os.Args[1:])

	auditEnabled := os.Getenv("HELPDESK_AUDIT_ENABLED")
	policyDryRun := os.Getenv("HELPDESK_POLICY_DRY_RUN")
	cfg := Config{
		ModelVendor:   os.Getenv("HELPDESK_MODEL_VENDOR"),
		ModelName:     os.Getenv("HELPDESK_MODEL_NAME"),
		APIKey:        os.Getenv("HELPDESK_API_KEY"),
		ListenAddr:    os.Getenv("HELPDESK_AGENT_ADDR"),
		ExternalURL:   os.Getenv("HELPDESK_AGENT_URL"),
		AuditEnabled:  auditEnabled == "true" || auditEnabled == "1",
		AuditURL:      os.Getenv("HELPDESK_AUDIT_URL"),
		AuditDir:      os.Getenv("HELPDESK_AUDIT_DIR"),
		PolicyFile:    os.Getenv("HELPDESK_POLICY_FILE"),
		PolicyDryRun:  policyDryRun == "true" || policyDryRun == "1",
		DefaultPolicy: os.Getenv("HELPDESK_DEFAULT_POLICY"),
	}

	if cfg.ModelVendor == "" || cfg.ModelName == "" || cfg.APIKey == "" {
		slog.Error("missing required environment variables: HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY")
		os.Exit(1)
	}

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = defaultAddr
	}

	if cfg.DefaultPolicy == "" {
		cfg.DefaultPolicy = "deny"
	}

	return cfg
}

// NewLLM creates an LLM model based on Config.ModelVendor (gemini or anthropic).
func NewLLM(ctx context.Context, cfg Config) (adkmodel.LLM, error) {
	switch strings.ToLower(cfg.ModelVendor) {
	case "google", "gemini":
		llm, err := gemini.NewModel(ctx, cfg.ModelName, &genai.ClientConfig{APIKey: cfg.APIKey})
		if err != nil {
			return nil, fmt.Errorf("failed to create Gemini model: %v", err)
		}
		slog.Info("using model", "vendor", "gemini", "model", cfg.ModelName)
		return llm, nil

	case "anthropic":
		llm, err := model.NewAnthropicModel(ctx, cfg.ModelName, cfg.APIKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create Anthropic model: %v", err)
		}
		slog.Info("using model", "vendor", "anthropic", "model", cfg.ModelName)
		return llm, nil

	default:
		return nil, fmt.Errorf("unknown model vendor: %s (supported: google, gemini, anthropic)", cfg.ModelVendor)
	}
}

// InitPolicyEngine initializes a policy engine for an agent if configured.
// Returns nil if no policy file is configured.
func InitPolicyEngine(cfg Config) (*policy.Engine, error) {
	if cfg.PolicyFile == "" {
		slog.Debug("policy enforcement disabled (no HELPDESK_POLICY_FILE)")
		return nil, nil
	}

	// Load policies from file
	policyCfg, err := policy.LoadFile(cfg.PolicyFile)
	if err != nil {
		return nil, fmt.Errorf("load policy file: %w", err)
	}

	// Determine default effect
	defaultEffect := policy.EffectDeny
	if strings.ToLower(cfg.DefaultPolicy) == "allow" {
		defaultEffect = policy.EffectAllow
	}

	engine := policy.NewEngine(policy.EngineConfig{
		PolicyConfig:  policyCfg,
		DefaultEffect: defaultEffect,
		DryRun:        cfg.PolicyDryRun,
	})

	slog.Info("policy enforcement enabled",
		"file", cfg.PolicyFile,
		"policies", len(policyCfg.Policies),
		"dry_run", cfg.PolicyDryRun,
		"default", cfg.DefaultPolicy)

	return engine, nil
}

// PolicyEnforcer wraps a policy engine with convenience methods for agents.
type PolicyEnforcer struct {
	engine     *policy.Engine
	traceStore *audit.CurrentTraceStore
}

// NewPolicyEnforcer creates a policy enforcer. If engine is nil, enforcement is disabled.
func NewPolicyEnforcer(engine *policy.Engine, traceStore *audit.CurrentTraceStore) *PolicyEnforcer {
	return &PolicyEnforcer{
		engine:     engine,
		traceStore: traceStore,
	}
}

// CheckTool evaluates whether a tool execution is allowed.
// Returns nil if allowed, error if denied or requires approval.
func (e *PolicyEnforcer) CheckTool(ctx context.Context, resourceType, resourceName string, action policy.ActionClass, tags []string) error {
	if e.engine == nil {
		return nil // No enforcement
	}

	req := policy.Request{
		Resource: policy.RequestResource{
			Type: resourceType,
			Name: resourceName,
			Tags: tags,
		},
		Action: action,
	}

	// Add trace context if available
	if e.traceStore != nil {
		req.Context.TraceID = e.traceStore.Get()
	}

	decision := e.engine.Evaluate(req)
	return decision.MustAllow()
}

// CheckDatabase is a convenience method for database operations.
func (e *PolicyEnforcer) CheckDatabase(ctx context.Context, dbName string, action policy.ActionClass, tags []string) error {
	return e.CheckTool(ctx, "database", dbName, action, tags)
}

// CheckKubernetes is a convenience method for Kubernetes operations.
func (e *PolicyEnforcer) CheckKubernetes(ctx context.Context, namespace string, action policy.ActionClass, tags []string) error {
	return e.CheckTool(ctx, "kubernetes", namespace, action, tags)
}

// InitAuditStore initializes an audit store for an agent if auditing is enabled.
// Returns nil if auditing is disabled. The caller should defer store.Close() if non-nil.
// If HELPDESK_AUDIT_URL is set, uses the central audit service (preferred).
// Otherwise falls back to local SQLite if HELPDESK_AUDIT_DIR is set.
func InitAuditStore(cfg Config) (audit.Auditor, error) {
	if !cfg.AuditEnabled {
		return nil, nil
	}

	// Prefer central audit service
	if cfg.AuditURL != "" {
		slog.Info("agent audit logging enabled (remote)", "url", cfg.AuditURL)
		return audit.NewRemoteStore(cfg.AuditURL), nil
	}

	// Fall back to local SQLite
	auditDir := cfg.AuditDir
	if auditDir == "" {
		auditDir = "."
	}

	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(auditDir, "audit.db"),
		// No socket for agents - only the orchestrator needs the socket
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create audit store: %w", err)
	}

	slog.Info("agent audit logging enabled (local)", "db", filepath.Join(auditDir, "audit.db"))
	return store, nil
}

// CardOptions allows agents to customize the AgentCard beyond the defaults
// that Serve derives automatically from the ADK agent.
type CardOptions struct {
	// Version is the agent's version string (e.g., "1.0.0").
	Version string

	// DocumentationURL points to the agent's documentation.
	DocumentationURL string

	// Provider describes the organization providing this agent.
	Provider *a2a.AgentProvider

	// SkillTags maps a skill ID to additional tags to merge onto the
	// auto-generated skills. Skill IDs follow the ADK pattern:
	// "agentName" for the model skill, "agentName-toolName" for tool skills.
	SkillTags map[string][]string

	// SkillExamples maps a skill ID to example prompts/scenarios.
	SkillExamples map[string][]string
}

// applyCardOptions merges optional metadata onto an AgentCard.
func applyCardOptions(card *a2a.AgentCard, opts CardOptions) {
	if opts.Version != "" {
		card.Version = opts.Version
	}
	if opts.DocumentationURL != "" {
		card.DocumentationURL = opts.DocumentationURL
	}
	if opts.Provider != nil {
		card.Provider = opts.Provider
	}
	for i := range card.Skills {
		skill := &card.Skills[i]
		if tags, ok := opts.SkillTags[skill.ID]; ok {
			skill.Tags = append(skill.Tags, tags...)
		}
		if examples, ok := opts.SkillExamples[skill.ID]; ok {
			skill.Examples = examples
		}
	}
}

// Serve starts an A2A server for the given agent on cfg.ListenAddr.
// It sets up the agent card, JSON-RPC handler, in-memory session service, and blocks.
// An optional CardOptions can be passed to enrich the agent card with additional metadata.
// If cfg.ExternalURL is set, it will be used in the agent card instead of the listener address.
func Serve(ctx context.Context, a agent.Agent, cfg Config, opts ...CardOptions) error {
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind to %s: %v", cfg.ListenAddr, err)
	}

	// Use ExternalURL if set, otherwise fall back to listener address.
	// ExternalURL is required in Kubernetes where pods communicate via service names.
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
		applyCardOptions(agentCard, opts[0])
	}

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(agentCard))

	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        a.Name(),
			Agent:          a,
			SessionService: session.InMemoryService(),
		},
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
// The traceStore is populated with trace_id from A2A message metadata for each request.
func ServeWithTracing(ctx context.Context, a agent.Agent, cfg Config, traceStore *audit.CurrentTraceStore, opts ...CardOptions) error {
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
		applyCardOptions(agentCard, opts[0])
	}

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(agentCard))

	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        a.Name(),
			Agent:          a,
			SessionService: session.InMemoryService(),
		},
	})
	requestHandler := a2asrv.NewHandler(executor)

	// Wrap with trace middleware to extract trace_id from incoming messages
	tracedHandler := audit.TraceMiddleware(traceStore, a2asrv.NewJSONRPCHandler(requestHandler))
	mux.Handle(agentPath, tracedHandler)

	slog.Info("starting A2A server with tracing",
		"agent", a.Name(),
		"url", baseURL.String(),
		"card", baseURL.String()+"/.well-known/agent-card.json",
	)

	return http.Serve(listener, mux)
}
