// Package main implements the helpdesk orchestrator - a multi-agent system for
// troubleshooting database and infrastructure issues. It routes user queries
// to specialized sub-agents based on the problem domain.

package main

import (
	"context"
	"fmt"
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

const rootAgentPrompt = `You are an expert in database and infrastructure troubleshooting.
You help users diagnose and resolve issues with their database systems and the infrastructure they run on.

## Available Specialist Agents

You have access to the following specialist agents that you can delegate to:

### postgres_database_agent
Use this agent for PostgreSQL database issues including:
- Connection problems
- Performance issues (slow queries, high CPU, memory)
- Configuration questions
- Replication and high availability
- Lock contention and deadlocks
- Table bloat and vacuum issues

### k8s_agent
Use this agent for Kubernetes infrastructure issues including:
- Pod status and health checks
- Service and LoadBalancer configuration
- Endpoint and networking issues
- Container logs and debugging
- Node status and resource issues
- Events and cluster diagnostics

## Troubleshooting Workflow

When a user reports an issue:

1. **Understand the problem**: Ask clarifying questions if needed to understand:
   - What is the symptom? (connection timeout, slow queries, pod crashes, etc.)
   - What is the environment? (PostgreSQL version, K8s cluster, cloud provider)
   - When did it start? What changed recently?

2. **Route to the right agent**:
   - Database-specific issues → postgres_database_agent
   - Infrastructure/K8s issues → k8s_agent
   - If the database runs on K8s and you suspect infrastructure issues, try k8s_agent first

3. **Synthesize findings**: After getting information from sub-agents, explain the findings
   to the user in clear terms and suggest next steps.

## Important Notes

- If a sub-agent is unavailable, inform the user and suggest manual troubleshooting steps
- Always explain your reasoning when delegating to a sub-agent
- Provide actionable recommendations based on the findings
`

// AgentConfig holds configuration for a remote agent.
type AgentConfig struct {
	Name        string
	URL         string
	Description string
}

// inputParams holds the orchestrator configuration.
type inputParams struct {
	modelName string
	apiKey    string
	agents    []AgentConfig
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

	modelName := os.Getenv("HELPDESK_MODEL_NAME")
	apiKey := os.Getenv("HELPDESK_API_KEY")
	if modelName == "" || apiKey == "" {
		log.Fatalf("Please set the HELPDESK_MODEL_NAME and HELPDESK_API_KEY env variables.")
	}

	// Configure available agents
	p := &inputParams{
		modelName: modelName,
		apiKey:    apiKey,
		agents: []AgentConfig{
			{
				Name:        "postgres_database_agent",
				URL:         "http://localhost:1100",
				Description: "PostgreSQL database troubleshooting agent for diagnosing connectivity, performance, configuration, and replication issues.",
			},
			{
				Name:        "k8s_agent",
				URL:         "http://localhost:1102",
				Description: "Kubernetes troubleshooting agent for diagnosing pod, service, endpoint, and infrastructure issues.",
			},
		},
	}

	// Create the LLM model
	llmModel, err := gemini.NewModel(ctx, p.modelName, &genai.ClientConfig{APIKey: p.apiKey})
	if err != nil {
		log.Fatalf("Failed to create model: %v", err)
	}

	// Create remote agent proxies (with health checking)
	remoteAgents, unavailableAgents := createRemoteAgents(p.agents)

	// Build the instruction with availability info
	instruction := rootAgentPrompt
	if len(unavailableAgents) > 0 {
		instruction += fmt.Sprintf("\n\n## Currently Unavailable Agents\nThe following agents are currently unavailable: %s\nIf you need these agents, inform the user and suggest they start the agent or try manual troubleshooting.\n",
			strings.Join(unavailableAgents, ", "))
	}

	// Create tools list - include Google Search for general queries
	tools := []tool.Tool{
		geminitool.GoogleSearch{},
	}

	// Create the root agent
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

	// Create the agent loader
	agentLoader := agent.NewSingleLoader(rootAgent)

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
