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

## Reporting errors from sub-agents

When a sub-agent reports an error, relay it verbatim. Do NOT paraphrase, summarize,
or wrap it in narrative like "I encountered an issue". Pass through the sub-agent's
error block exactly as received, then add your own recommendations on a new line after it.

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

// PostgresServer represents a managed PostgreSQL server (AlloyDB Omni or standalone).
type PostgresServer struct {
	Name             string `json:"name"`
	ConnectionString string `json:"connection_string"`
	K8sCluster       string `json:"k8s_cluster,omitempty"`
}

// K8sCluster represents a managed Kubernetes cluster.
type K8sCluster struct {
	Name    string `json:"name"`
	Context string `json:"context"`
}

// InfraConfig holds the infrastructure inventory.
type InfraConfig struct {
	PostgresServers map[string]PostgresServer `json:"postgres_servers"`
	K8sClusters     map[string]K8sCluster     `json:"k8s_clusters"`
}

// loadInfraConfig loads infrastructure configuration from a JSON file.
func loadInfraConfig(path string) (*InfraConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read infrastructure config file: %v", err)
	}

	var config InfraConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse infrastructure config: %v", err)
	}

	return &config, nil
}

// buildInfraPromptSection generates the managed infrastructure section for the agent prompt.
func buildInfraPromptSection(config *InfraConfig) string {
	var sb strings.Builder
	sb.WriteString("\n## Managed Infrastructure\n\n")

	if len(config.PostgresServers) > 0 {
		sb.WriteString("### PostgreSQL Servers\n\n")
		for id, pg := range config.PostgresServers {
			sb.WriteString(fmt.Sprintf("**%s** (%s)\n", id, pg.Name))
			sb.WriteString(fmt.Sprintf("- connection_string: `%s`\n", pg.ConnectionString))
			if pg.K8sCluster != "" {
				if k8s, ok := config.K8sClusters[pg.K8sCluster]; ok {
					sb.WriteString(fmt.Sprintf("- Runs on K8s cluster: **%s** (context: `%s`)\n", pg.K8sCluster, k8s.Context))
				} else {
					sb.WriteString(fmt.Sprintf("- Runs on K8s cluster: **%s** (not found in k8s_clusters)\n", pg.K8sCluster))
				}
			} else {
				sb.WriteString("- Runs on VM (no K8s cluster)\n")
			}
			sb.WriteString("\n")
		}
	}

	if len(config.K8sClusters) > 0 {
		sb.WriteString("### Kubernetes Clusters\n\n")
		for id, k8s := range config.K8sClusters {
			sb.WriteString(fmt.Sprintf("**%s** (%s) — context: `%s`\n", id, k8s.Name, k8s.Context))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("### Instructions\n\n")
	sb.WriteString("- When investigating a postgres server, use its connection_string with the database agent.\n")
	sb.WriteString("- If the server has an associated K8s cluster, use that cluster's context with the K8s agent.\n")
	sb.WriteString("- K8s clusters not tied to any postgres server can still be inspected independently.\n")

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

	// Build the instruction with dynamic agent section and availability info
	instruction := baseAgentPrompt + buildAgentPromptSection(p.agents)

	// Load infrastructure configuration (optional)
	infraConfigPath := os.Getenv("HELPDESK_INFRA_CONFIG")
	if infraConfigPath != "" {
		infraConfig, err := loadInfraConfig(infraConfigPath)
		if err != nil {
			slog.Error("failed to load infrastructure config", "path", infraConfigPath, "err", err)
			os.Exit(1)
		}
		instruction += buildInfraPromptSection(infraConfig)
		slog.Info("infrastructure config loaded", "pg_servers", len(infraConfig.PostgresServers), "k8s_clusters", len(infraConfig.K8sClusters))
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
	}

	// Launch the orchestrator
	l := full.NewLauncher()
	if err = l.Execute(ctx, config, remainingArgs); err != nil {
		slog.Error("failed to launch", "err", err, "usage", l.CommandLineSyntax())
		os.Exit(1)
	}
}
