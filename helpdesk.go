// Package main implements the helpdesk orchestrator - a multi-agent system for
// troubleshooting database and infrastructure issues. It routes user queries
// to specialized sub-agents based on the problem domain.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/a2asrv"
	"github.com/google/uuid"
	"google.golang.org/genai"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/agent/remoteagent"
	"google.golang.org/adk/artifact"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/cmd/launcher/full"
	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/geminitool"
)

const baseAgentPrompt = `You are an expert in database and infrastructure troubleshooting.
You help users diagnose and resolve issues with their database systems and the infrastructure they run on.

## Troubleshooting Workflow

When a user reports an issue:

1. **Understand the problem**: Ask clarifying questions if needed to understand:
   - What is the symptom? (connection timeout, slow queries, pod crashes, etc.)
   - What is the environment? (PostgreSQL version, K8s cluster, cloud provider)
   - When did it start? What changed recently?

2. **Route to the right agent**: Delegate to the appropriate specialist agent based on the problem domain.

3. **Synthesize findings**: After getting information from sub-agents, explain the findings
   to the user in clear terms and suggest next steps.

## Important Notes

- If a sub-agent is unavailable, inform the user and suggest manual troubleshooting steps
- Always explain your reasoning when delegating to a sub-agent
- Provide actionable recommendations based on the findings
`

// AgentConfig holds configuration for a remote agent.
type AgentConfig struct {
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	Description string   `json:"description"`
	UseCases    []string `json:"use_cases,omitempty"`
}

// inputParams holds the orchestrator configuration.
type inputParams struct {
	modelName string
	apiKey    string
	agents    []AgentConfig
}

// loadAgentsConfig loads agent configurations from a JSON file.
func loadAgentsConfig(configPath string) ([]AgentConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read agents config file: %v", err)
	}

	var agents []AgentConfig
	if err := json.Unmarshal(data, &agents); err != nil {
		return nil, fmt.Errorf("failed to parse agents config: %v", err)
	}

	return agents, nil
}

// TenantConfig holds resource configuration for a tenant.
type TenantConfig struct {
	Name               string `json:"name"`
	PostgresConnection string `json:"postgres_connection"`
	K8sContext         string `json:"k8s_context"`
}

// TenantsConfig holds the full tenant configuration file structure.
type TenantsConfig struct {
	Tenants map[string]TenantConfig `json:"tenants"`
	Users   map[string]string       `json:"users"` // user -> tenant_id mapping
}

// loadTenantsConfig loads tenant configurations from a JSON file.
func loadTenantsConfig(configPath string) (*TenantsConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read tenants config file: %v", err)
	}

	var config TenantsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse tenants config: %v", err)
	}

	return &config, nil
}

// getTenantForUser returns the tenant config for a given username.
func (tc *TenantsConfig) getTenantForUser(username string) (*TenantConfig, string, error) {
	tenantID, ok := tc.Users[username]
	if !ok {
		return nil, "", fmt.Errorf("user %q not mapped to any tenant", username)
	}

	tenant, ok := tc.Tenants[tenantID]
	if !ok {
		return nil, "", fmt.Errorf("tenant %q not found in configuration", tenantID)
	}

	return &tenant, tenantID, nil
}

// buildTenantPromptSection generates tenant-specific instructions for a single tenant.
func buildTenantPromptSection(tenantID string, tenant *TenantConfig) string {
	var sb strings.Builder
	sb.WriteString("\n## Current Tenant Context\n\n")
	sb.WriteString(fmt.Sprintf("You are currently serving tenant: **%s** (%s)\n\n", tenant.Name, tenantID))
	sb.WriteString("When calling sub-agents, always use these tenant-specific parameters:\n\n")
	sb.WriteString(fmt.Sprintf("- For postgres_database_agent: use connection_string: `%s`\n", tenant.PostgresConnection))
	sb.WriteString(fmt.Sprintf("- For k8s_agent: use context: `%s`\n", tenant.K8sContext))
	sb.WriteString("\n**IMPORTANT**: Always include these parameters in your tool calls to ensure you're accessing the correct tenant's resources.\n")
	return sb.String()
}

// buildTenantsPromptSection generates multi-tenant instructions with user-to-tenant mapping.
func buildTenantsPromptSection(config *TenantsConfig) string {
	var sb strings.Builder
	sb.WriteString("\n## Multi-Tenant Mode\n\n")
	sb.WriteString("This system serves multiple tenants. Each user is mapped to a specific tenant.\n")
	sb.WriteString("You MUST use the correct tenant's resources based on the authenticated user.\n\n")

	sb.WriteString("### User to Tenant Mapping\n\n")
	for user, tenantID := range config.Users {
		sb.WriteString(fmt.Sprintf("- User `%s` → Tenant `%s`\n", user, tenantID))
	}

	sb.WriteString("\n### Tenant Resources\n\n")
	for tenantID, tenant := range config.Tenants {
		sb.WriteString(fmt.Sprintf("**%s** (%s):\n", tenant.Name, tenantID))
		sb.WriteString(fmt.Sprintf("- postgres connection_string: `%s`\n", tenant.PostgresConnection))
		sb.WriteString(fmt.Sprintf("- k8s context: `%s`\n", tenant.K8sContext))
		sb.WriteString("\n")
	}

	sb.WriteString("### Instructions\n\n")
	sb.WriteString("1. The current user's identity is set by the system (you'll see it in the session)\n")
	sb.WriteString("2. Look up the user's tenant from the mapping above\n")
	sb.WriteString("3. ALWAYS use that tenant's connection_string and context when calling sub-agents\n")
	sb.WriteString("4. NEVER access resources from a different tenant\n")
	sb.WriteString("5. If the user is not in the mapping, inform them they are not authorized\n")

	return sb.String()
}

// buildAgentPromptSection generates the "Available Specialist Agents" section
// dynamically from the loaded agent configurations.
func buildAgentPromptSection(agents []AgentConfig) string {
	if len(agents) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n## Available Specialist Agents\n\n")
	sb.WriteString("You have access to the following specialist agents that you can delegate to:\n\n")

	for _, agent := range agents {
		sb.WriteString(fmt.Sprintf("### %s\n", agent.Name))
		if agent.Description != "" {
			sb.WriteString(fmt.Sprintf("%s\n", agent.Description))
		}
		if len(agent.UseCases) > 0 {
			sb.WriteString("Use this agent for:\n")
			for _, useCase := range agent.UseCases {
				sb.WriteString(fmt.Sprintf("- %s\n", useCase))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// agentCardResponse represents the relevant fields from /.well-known/agent-card.json
type agentCardResponse struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Skills      []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"skills,omitempty"`
}

// discoverAgentFromURL fetches the agent card from a URL and converts it to AgentConfig.
func discoverAgentFromURL(baseURL string) (*AgentConfig, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	cardURL := strings.TrimSuffix(baseURL, "/") + "/.well-known/agent-card.json"
	resp, err := client.Get(cardURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch agent card: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent card returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent card: %v", err)
	}

	var card agentCardResponse
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, fmt.Errorf("failed to parse agent card: %v", err)
	}

	config := &AgentConfig{
		Name:        card.Name,
		Description: card.Description,
		URL:         baseURL, // Use the base URL we probed, not the invoke URL from the card
	}

	// Convert skills to use cases
	for _, skill := range card.Skills {
		if skill.Description != "" {
			config.UseCases = append(config.UseCases, skill.Description)
		} else if skill.Name != "" {
			config.UseCases = append(config.UseCases, skill.Name)
		}
	}

	if config.Name == "" {
		return nil, fmt.Errorf("agent card missing name")
	}

	return config, nil
}

// discoverAgents discovers agents from a list of base URLs by fetching their agent cards.
func discoverAgents(urls []string) ([]AgentConfig, []string) {
	var discovered []AgentConfig
	var failed []string

	for _, url := range urls {
		slog.Info("discovering agent", "url", url)
		config, err := discoverAgentFromURL(url)
		if err != nil {
			slog.Warn("agent discovery failed", "url", url, "err", err)
			failed = append(failed, url)
			continue
		}
		slog.Info("discovered agent", "name", config.Name, "url", url)
		discovered = append(discovered, *config)
	}

	return discovered, failed
}

// AuthInterceptor handles user authentication for multi-tenant support.
// User identity is determined from (in order of priority):
// 1. HELPDESK_USER environment variable (for testing/demo)
// 2. Falls back to "anonymous"
type AuthInterceptor struct {
	a2asrv.PassthroughCallInterceptor
	DefaultUser string // Set from HELPDESK_USER env var
}

// Before implements a before request callback.
func (a *AuthInterceptor) Before(ctx context.Context, callCtx *a2asrv.CallContext, req *a2asrv.Request) (context.Context, error) {
	username := a.DefaultUser
	if username == "" {
		username = "anonymous"
	}

	callCtx.User = &a2asrv.AuthenticatedUser{
		UserName: username,
	}

	slog.Info("authenticated user", "username", username)
	return ctx, nil
}

// saveReportFunc saves LLM responses as artifacts.
func saveReportFunc(ctx agent.CallbackContext, llmResponse *model.LLMResponse, llmResponseError error) (*model.LLMResponse, error) {
	if llmResponse == nil || llmResponse.Content == nil || llmResponseError != nil {
		return llmResponse, llmResponseError
	}
	for _, part := range llmResponse.Content.Parts {
		// Only save parts that have Text or InlineData - skip FunctionCall/FunctionResponse parts
		if part.Text == "" && part.InlineData == nil {
			continue
		}
		_, err := ctx.Artifacts().Save(ctx, uuid.NewString(), part)
		if err != nil {
			return nil, err
		}
	}
	return llmResponse, llmResponseError
}

// checkAgentHealth verifies that an agent is reachable by fetching its agent card.
func checkAgentHealth(agentURL string) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Try to fetch the agent card
	cardURL := strings.TrimSuffix(agentURL, "/") + "/.well-known/agent-card.json"
	resp, err := client.Get(cardURL)
	if err != nil {
		return fmt.Errorf("failed to reach agent at %s: %v", agentURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent at %s returned status %d", agentURL, resp.StatusCode)
	}

	return nil
}

// createRemoteAgents creates remote agent proxies for available agents.
// It checks agent health and only returns agents that are reachable.
func createRemoteAgents(configs []AgentConfig) ([]agent.Agent, []string) {
	var agents []agent.Agent
	var unavailable []string

	for _, cfg := range configs {
		slog.Info("confirming agent availability", "agent", cfg.Name, "url", cfg.URL)

		// Check if agent is healthy
		if err := checkAgentHealth(cfg.URL); err != nil {
			slog.Warn("agent unavailable", "agent", cfg.Name, "url", cfg.URL, "err", err)
			unavailable = append(unavailable, cfg.Name)
			continue
		}

		// Create remote agent proxy
		remoteAgent, err := remoteagent.NewA2A(remoteagent.A2AConfig{
			Name:             cfg.Name,
			Description:      cfg.Description,
			AgentCardSource:  cfg.URL,
		})
		if err != nil {
			slog.Warn("failed to create agent proxy", "agent", cfg.Name, "err", err)
			unavailable = append(unavailable, cfg.Name)
			continue
		}

		slog.Info("agent available", "agent", cfg.Name)
		agents = append(agents, remoteAgent)
	}

	return agents, unavailable
}

func main() {
	remainingArgs := initLogging(os.Args[1:])

	ctx := context.Background()

	modelVendor := os.Getenv("HELPDESK_MODEL_VENDOR")
	modelName := os.Getenv("HELPDESK_MODEL_NAME")
	apiKey := os.Getenv("HELPDESK_API_KEY")
	if modelVendor == "" || modelName == "" || apiKey == "" {
		slog.Error("missing required environment variables: HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY")
		os.Exit(1)
	}

	// Discover agents from URLs or load from config file.
	var agentConfigs []AgentConfig

	// First attempt to find agents based on HELPDESK_AGENT_URLS env var.
	agentURLs := os.Getenv("HELPDESK_AGENT_URLS")
	if agentURLs != "" {
		// URL-based discovery: probe each URL for agents.md
		urls := strings.Split(agentURLs, ",")
		for i := range urls {
			urls[i] = strings.TrimSpace(urls[i])
		}
		discovered, failed := discoverAgents(urls)
		if len(failed) > 0 {
			slog.Warn("failed to discover some agents", "urls", strings.Join(failed, ", "))
		}
		agentConfigs = discovered
	} else {
		// Fall back to config file.
		slog.Info("no dynamic agent discovery (HELPDESK_AGENT_URLS not set), falling back to static config file")
		agentsConfigPath := os.Getenv("HELPDESK_AGENTS_CONFIG")
		if agentsConfigPath == "" {
			agentsConfigPath = "agents.json"
		}
		var err error
		agentConfigs, err = loadAgentsConfig(agentsConfigPath)
		if err != nil {
			slog.Error("failed to load agents config", "path", agentsConfigPath, "err", err)
			os.Exit(1)
		}
		slog.Info("loaded agent configs from file", "path", agentsConfigPath)
	}

	if len(agentConfigs) == 0 {
		slog.Warn("no agents discovered or configured")
	}

	agentNames := make([]string, len(agentConfigs))
	for i, cfg := range agentConfigs {
		agentNames[i] = cfg.Name
	}
	slog.Info("expected expert agents", "agents", strings.Join(agentNames, ", "))

	p := &inputParams{
		modelName: modelName,
		apiKey:    apiKey,
		agents:    agentConfigs,
	}

	// Create the LLM model based on vendor
	var llmModel model.LLM
	var err error

	switch strings.ToLower(modelVendor) {
	case "google", "gemini":
		llmModel, err = gemini.NewModel(ctx, p.modelName, &genai.ClientConfig{APIKey: p.apiKey})
		if err != nil {
			slog.Error("failed to create model", "vendor", "gemini", "err", err)
			os.Exit(1)
		}
		slog.Info("using model", "vendor", "gemini", "model", modelName)

	case "anthropic":
		llmModel, err = NewAnthropicModel(ctx, modelName, apiKey)
		if err != nil {
			slog.Error("failed to create model", "vendor", "anthropic", "err", err)
			os.Exit(1)
		}
		slog.Info("using model", "vendor", "anthropic", "model", modelName)

	default:
		slog.Error("unknown model vendor", "vendor", modelVendor, "supported", "google, gemini, anthropic")
		os.Exit(1)
	}

	// Create remote agent proxies (with health checking)
	remoteAgents, unavailableAgents := createRemoteAgents(p.agents)

	// Load tenant configuration (optional - for multi-tenant mode)
	var tenantsConfig *TenantsConfig
	tenantsConfigPath := os.Getenv("HELPDESK_TENANTS_CONFIG")
	if tenantsConfigPath != "" {
		var err error
		tenantsConfig, err = loadTenantsConfig(tenantsConfigPath)
		if err != nil {
			slog.Error("failed to load tenants config", "path", tenantsConfigPath, "err", err)
			os.Exit(1)
		}
		slog.Info("multi-tenant mode enabled", "tenants", len(tenantsConfig.Tenants), "users", len(tenantsConfig.Users), "path", tenantsConfigPath)
	}

	// Build the instruction with dynamic agent section and availability info
	instruction := baseAgentPrompt + buildAgentPromptSection(p.agents)

	// Add tenant context if multi-tenant mode is enabled
	currentUser := os.Getenv("HELPDESK_USER")
	if tenantsConfig != nil {
		instruction += buildTenantsPromptSection(tenantsConfig)

		// Add current authenticated user to the prompt
		if currentUser != "" {
			tenant, tenantID, err := tenantsConfig.getTenantForUser(currentUser)
			if err != nil {
				slog.Warn("user not mapped to tenant", "user", currentUser, "err", err)
				instruction += fmt.Sprintf("\n## Current Session\n\nAuthenticated user: `%s` (WARNING: not mapped to any tenant)\n", currentUser)
			} else {
				instruction += fmt.Sprintf("\n## Current Session\n\n")
				instruction += fmt.Sprintf("Authenticated user: `%s`\n", currentUser)
				instruction += fmt.Sprintf("Tenant: **%s** (`%s`)\n\n", tenant.Name, tenantID)
				instruction += fmt.Sprintf("Use these parameters for ALL sub-agent calls:\n")
				instruction += fmt.Sprintf("- postgres_database_agent → connection_string: `%s`\n", tenant.PostgresConnection)
				instruction += fmt.Sprintf("- k8s_agent → context: `%s`\n", tenant.K8sContext)
			}
		}
	}

	if len(unavailableAgents) > 0 {
		instruction += fmt.Sprintf("\n## Currently Unavailable Agents\nThe following agents are currently unavailable: %s\nIf you need these agents, inform the user and suggest they start the agent or try manual troubleshooting.\n",
			strings.Join(unavailableAgents, ", "))
	}

	// Create tools list
	var tools []tool.Tool
	// Google Search is only available with Gemini models
	if strings.ToLower(modelVendor) == "google" || strings.ToLower(modelVendor) == "gemini" {
		tools = append(tools, geminitool.GoogleSearch{})
	}

	// Create the root agent with sub-agents
	// SubAgents must be set here for the ADK to make them available as transfer targets
	rootAgent, err := llmagent.New(llmagent.Config{
		Name:                "helpdesk_orchestrator",
		Model:               llmModel,
		Description:         "Multi-agent helpdesk system for database and infrastructure troubleshooting.",
		Instruction:         instruction,
		Tools:               tools,
		SubAgents:           remoteAgents,
		AfterModelCallbacks: []llmagent.AfterModelCallback{saveReportFunc},
	})
	if err != nil {
		slog.Error("failed to create root agent", "err", err)
		os.Exit(1)
	}

	slog.Info("orchestrator initialized", "available_agents", len(remoteAgents))
	if len(unavailableAgents) > 0 {
		slog.Warn("some agents unavailable", "agents", strings.Join(unavailableAgents, ", "))
	}

	// Create the agent loader with all agents (root + remote sub-agents)
	agentLoader, err := agent.NewMultiLoader(rootAgent, remoteAgents...)
	if err != nil {
		slog.Error("failed to create agent loader", "err", err)
		os.Exit(1)
	}

	// Configure services
	artifactService := artifact.InMemoryService()
	sessionService := session.InMemoryService()

	config := &launcher.Config{
		ArtifactService: artifactService,
		SessionService:  sessionService,
		AgentLoader:     agentLoader,
		A2AOptions: []a2asrv.RequestHandlerOption{
			a2asrv.WithCallInterceptor(&AuthInterceptor{DefaultUser: currentUser}),
		},
	}

	// Launch the orchestrator
	l := full.NewLauncher()
	if err = l.Execute(ctx, config, remainingArgs); err != nil {
		slog.Error("failed to launch", "err", err, "usage", l.CommandLineSyntax())
		os.Exit(1)
	}
}
