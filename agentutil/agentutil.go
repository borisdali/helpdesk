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

	"helpdesk/internal/logging"
	"helpdesk/internal/model"
)

// Config holds common agent configuration from HELPDESK_* env vars.
type Config struct {
	ModelVendor string
	ModelName   string
	APIKey      string
	ListenAddr  string
}

// MustLoadConfig reads env vars. defaultAddr is used when HELPDESK_AGENT_ADDR is unset.
// Exits the process if required vars (MODEL_VENDOR, MODEL_NAME, API_KEY) are missing.
// It also initialises structured logging via logging.InitLogging.
func MustLoadConfig(defaultAddr string) Config {
	logging.InitLogging(os.Args[1:])

	cfg := Config{
		ModelVendor: os.Getenv("HELPDESK_MODEL_VENDOR"),
		ModelName:   os.Getenv("HELPDESK_MODEL_NAME"),
		APIKey:      os.Getenv("HELPDESK_API_KEY"),
		ListenAddr:  os.Getenv("HELPDESK_AGENT_ADDR"),
	}

	if cfg.ModelVendor == "" || cfg.ModelName == "" || cfg.APIKey == "" {
		slog.Error("missing required environment variables: HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY")
		os.Exit(1)
	}

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = defaultAddr
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
func Serve(ctx context.Context, a agent.Agent, cfg Config, opts ...CardOptions) error {
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind to %s: %v", cfg.ListenAddr, err)
	}

	baseURL := &url.URL{Scheme: "http", Host: listener.Addr().String()}

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
