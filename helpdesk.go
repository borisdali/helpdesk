// Package main implements the helpdesk orchestrator - a multi-agent system for
// troubleshooting database and infrastructure issues. It routes user queries
// to specialized sub-agents based on the problem domain.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
		log.Printf("Discovering agent at %s...", url)
		config, err := discoverAgentFromURL(url)
		if err != nil {
			log.Printf("  SKIP: %v", err)
			failed = append(failed, url)
			continue
		}
		log.Printf("  OK: Found agent '%s'", config.Name)
		discovered = append(discovered, *config)
	}

	return discovered, failed
}

// AuthInterceptor sets 'user' name needed for both a2a and webui launchers which share the same sessions service.
type AuthInterceptor struct {
	a2asrv.PassthroughCallInterceptor
}

// Before implements a before request callback.
func (a *AuthInterceptor) Before(ctx context.Context, callCtx *a2asrv.CallContext, req *a2asrv.Request) (context.Context, error) {
	callCtx.User = &a2asrv.AuthenticatedUser{
		UserName: "user",
	}
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
		log.Printf("Checking agent %s at %s...", cfg.Name, cfg.URL)

		// Check if agent is healthy
		if err := checkAgentHealth(cfg.URL); err != nil {
			log.Printf("  WARNING: Agent %s is unavailable: %v", cfg.Name, err)
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
			log.Printf("  WARNING: Failed to create proxy for %s: %v", cfg.Name, err)
			unavailable = append(unavailable, cfg.Name)
			continue
		}

		log.Printf("  OK: Agent %s is available", cfg.Name)
		agents = append(agents, remoteAgent)
	}

	return agents, unavailable
}

func main() {
	ctx := context.Background()

	modelVendor := os.Getenv("HELPDESK_MODEL_VENDOR")
	modelName := os.Getenv("HELPDESK_MODEL_NAME")
	apiKey := os.Getenv("HELPDESK_API_KEY")
	if modelVendor == "" || modelName == "" || apiKey == "" {
		log.Fatalf("Please set the HELPDESK_MODEL_VENDOR (e.g. Google/Gemini, Anthropic, etc.), HELPDESK_MODEL_NAME and HELPDESK_API_KEY env variables.")
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
			log.Printf("Failed to discover agents at: %s", strings.Join(failed, ", "))
		}
		agentConfigs = discovered
	} else {
		// Fall back to config file.
		log.Printf("No dynamic agent discover (HELPDESK_AGENT_URLS env var is not set), so falling back to a static config file")
		agentsConfigPath := os.Getenv("HELPDESK_AGENTS_CONFIG")
		if agentsConfigPath == "" {
			agentsConfigPath = "agents.json"
		}
		var err error
		agentConfigs, err = loadAgentsConfig(agentsConfigPath)
		if err != nil {
			log.Fatalf("Failed to load agents config from %s: %v", agentsConfigPath, err)
		}
		log.Printf("Loaded configs of expected to participate expert agents from config file: %s", agentsConfigPath)
	}

	if len(agentConfigs) == 0 {
		log.Printf("WARNING: No agents discovered or configured")
	}

	agentNames := make([]string, len(agentConfigs))
	for i, cfg := range agentConfigs {
		agentNames[i] = cfg.Name
	}
	log.Printf("Expert agent(s) expected to participate are: %s", strings.Join(agentNames, ", "))

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
			log.Fatalf("Failed to create Gemini model: %v", err)
		}
		log.Printf("Using Google/Gemini model: %s", modelName)

	case "anthropic":
		llmModel, err = NewAnthropicModel(ctx, modelName, apiKey)
		if err != nil {
			log.Fatalf("Failed to create Anthropic model: %v", err)
		}
		log.Printf("Using Anthropic model: %s", modelName)

	default:
		log.Fatalf("Unknown LLM model vendor: %s (supported: google, gemini, anthropic)", modelVendor)
	}

	// Create remote agent proxies (with health checking)
	remoteAgents, unavailableAgents := createRemoteAgents(p.agents)

	// Build the instruction with dynamic agent section and availability info
	instruction := baseAgentPrompt + buildAgentPromptSection(p.agents)
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
		log.Fatalf("Failed to create root agent: %v", err)
	}

	log.Printf("Orchestrator initialized with %d available agent(s)", len(remoteAgents))
	if len(unavailableAgents) > 0 {
		log.Printf("Unavailable agents: %s", strings.Join(unavailableAgents, ", "))
	}

	// Create the agent loader with all agents (root + remote sub-agents)
	agentLoader, err := agent.NewMultiLoader(rootAgent, remoteAgents...)
	if err != nil {
		log.Fatalf("Failed to create agent loader: %v", err)
	}

	// Configure services
	artifactService := artifact.InMemoryService()
	sessionService := session.InMemoryService()

	config := &launcher.Config{
		ArtifactService: artifactService,
		SessionService:  sessionService,
		AgentLoader:     agentLoader,
		A2AOptions: []a2asrv.RequestHandlerOption{
			a2asrv.WithCallInterceptor(&AuthInterceptor{}),
		},
	}

	// Launch the orchestrator
	l := full.NewLauncher()
	if err = l.Execute(ctx, config, os.Args[1:]); err != nil {
		log.Fatalf("Failed to launch: %v\n\n%s", err, l.CommandLineSyntax())
	}
}
