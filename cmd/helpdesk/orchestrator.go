package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/agent/remoteagent"
	"google.golang.org/adk/model"
)

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

// DBServer represents a managed database server (AlloyDB Omni, standalone PostgreSQL, etc.).
// Each server runs on either a Kubernetes cluster or a VM — never both.
type DBServer struct {
	Name             string `json:"name"`
	ConnectionString string `json:"connection_string"`
	K8sCluster       string `json:"k8s_cluster,omitempty"`
	K8sNamespace     string `json:"k8s_namespace,omitempty"`
	VMName           string `json:"vm_name,omitempty"`
}

// K8sCluster represents a managed Kubernetes cluster.
type K8sCluster struct {
	Name    string `json:"name"`
	Context string `json:"context"`
}

// VM represents a virtual machine hosting infrastructure.
type VM struct {
	Name string `json:"name"`
	Host string `json:"host"`
}

// InfraConfig holds the infrastructure inventory.
type InfraConfig struct {
	DBServers   map[string]DBServer   `json:"db_servers"`
	K8sClusters map[string]K8sCluster `json:"k8s_clusters"`
	VMs         map[string]VM         `json:"vms"`
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
// Only database servers are listed — K8s clusters and VMs are referenced inline where applicable.
func buildInfraPromptSection(config *InfraConfig) string {
	if len(config.DBServers) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# STOP — READ THIS FIRST\n\n")
	sb.WriteString("You manage these databases: ")
	dbNames := make([]string, 0, len(config.DBServers))
	for id := range config.DBServers {
		dbNames = append(dbNames, id)
	}
	sb.WriteString(strings.Join(dbNames, ", "))
	sb.WriteString("\n\nIf the user asks about ANY of these databases, you ALREADY HAVE their connection details.\n")
	sb.WriteString("DO NOT ask for connection strings or passwords. Look up the database below and delegate.\n\n")
	sb.WriteString("## Managed Databases\n\n")

	for id, db := range config.DBServers {
		sb.WriteString(fmt.Sprintf("**%s** — %s\n", id, db.Name))
		sb.WriteString(fmt.Sprintf("- connection_string: `%s`\n", db.ConnectionString))
		// Add a ready-to-use delegation example for this specific database
		sb.WriteString(fmt.Sprintf("- To check this database, delegate: \"Check if the database is reachable using connection_string: %s\"\n", db.ConnectionString))

		if db.K8sCluster != "" {
			// Expand the K8s cluster reference inline.
			if k8s, ok := config.K8sClusters[db.K8sCluster]; ok {
				ns := db.K8sNamespace
				if ns == "" {
					ns = "default"
				}
				sb.WriteString(fmt.Sprintf("- Hosted on Kubernetes: cluster=%s, context=`%s`, namespace=`%s`\n",
					k8s.Name, k8s.Context, ns))
				sb.WriteString(fmt.Sprintf("- To check K8s pods, delegate: \"Check pods in namespace '%s' using context '%s'\"\n", ns, k8s.Context))
			} else {
				sb.WriteString(fmt.Sprintf("- Hosted on Kubernetes: cluster=%s (details not configured)\n", db.K8sCluster))
			}
		} else if db.VMName != "" {
			// Expand the VM reference inline.
			if vm, ok := config.VMs[db.VMName]; ok {
				sb.WriteString(fmt.Sprintf("- Hosted on VM: %s (host: `%s`)\n", vm.Name, vm.Host))
			} else {
				sb.WriteString(fmt.Sprintf("- Hosted on VM: %s (details not configured)\n", db.VMName))
			}
		} else {
			sb.WriteString("- Hosting: standalone (no K8s cluster or VM specified)\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("### CRITICAL: How to Delegate\n\n")
	sb.WriteString("You MUST include the connection_string when delegating to postgres_database_agent.\n")
	sb.WriteString("Copy the EXACT connection_string from the database entry above into your delegation.\n\n")
	sb.WriteString("**Template for database checks:**\n")
	sb.WriteString("\"Check if the database is reachable using connection_string: <paste the connection_string here>\"\n\n")
	sb.WriteString("**Template for K8s checks:**\n")
	sb.WriteString("\"Check pods in namespace '<namespace>' using context '<context>'\"\n\n")
	sb.WriteString("For VM-hosted databases, only database-level diagnostics are available (no K8s agent).\n")

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

// saveReportFunc saves LLM responses as artifacts.
func saveReportFunc(ctx agent.CallbackContext, llmResponse *model.LLMResponse, llmResponseError error) (*model.LLMResponse, error) {
	if llmResponse == nil || llmResponse.Content == nil || llmResponseError != nil {
		return llmResponse, llmResponseError
	}
	for _, part := range llmResponse.Content.Parts {
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

// createRemoteAgents creates remote agent proxies for available agents.
// It checks agent health and only returns agents that are reachable.
func createRemoteAgents(configs []AgentConfig) ([]agent.Agent, []string) {
	var agents []agent.Agent
	var unavailable []string

	for _, cfg := range configs {
		slog.Info("confirming agent availability", "agent", cfg.Name, "url", cfg.URL)

		// Fetch the agent card and override the URL to use our discovered URL.
		// This handles cases where agents advertise K8s service names but we're
		// connecting via localhost.
		card, err := fetchAgentCard(cfg.URL)
		if err != nil {
			slog.Warn("agent unavailable", "agent", cfg.Name, "url", cfg.URL, "err", err)
			unavailable = append(unavailable, cfg.Name)
			continue
		}

		remoteAgent, err := remoteagent.NewA2A(remoteagent.A2AConfig{
			Name:        cfg.Name,
			Description: cfg.Description,
			AgentCard:   card,
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

// Ensure saveReportFunc has the correct signature.
var _ llmagent.AfterModelCallback = saveReportFunc
