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
func buildInfraPromptSection(config *InfraConfig) string {
	var sb strings.Builder
	sb.WriteString("\n## Managed Infrastructure\n\n")

	if len(config.DBServers) > 0 {
		sb.WriteString("### Database Servers\n\n")
		for id, db := range config.DBServers {
			sb.WriteString(fmt.Sprintf("**%s** (%s)\n", id, db.Name))
			sb.WriteString(fmt.Sprintf("- connection_string: `%s`\n", db.ConnectionString))
			if db.K8sCluster != "" {
				if k8s, ok := config.K8sClusters[db.K8sCluster]; ok {
					sb.WriteString(fmt.Sprintf("- Runs on K8s cluster: **%s** (context: `%s`)", db.K8sCluster, k8s.Context))
				} else {
					sb.WriteString(fmt.Sprintf("- Runs on K8s cluster: **%s** (not found in k8s_clusters)", db.K8sCluster))
				}
				ns := db.K8sNamespace
				if ns == "" {
					ns = "default"
				}
				sb.WriteString(fmt.Sprintf(", namespace: `%s`\n", ns))
			} else if db.VMName != "" {
				if vm, ok := config.VMs[db.VMName]; ok {
					sb.WriteString(fmt.Sprintf("- Runs on VM: **%s** (%s, host: `%s`)\n", db.VMName, vm.Name, vm.Host))
				} else {
					sb.WriteString(fmt.Sprintf("- Runs on VM: **%s** (not found in vms)\n", db.VMName))
				}
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

	if len(config.VMs) > 0 {
		sb.WriteString("### Virtual Machines\n\n")
		for id, vm := range config.VMs {
			sb.WriteString(fmt.Sprintf("**%s** (%s) — host: `%s`\n", id, vm.Name, vm.Host))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("### Instructions\n\n")
	sb.WriteString("- When investigating a database server, use its connection_string with the database agent.\n")
	sb.WriteString("- If the server has an associated K8s cluster, use that cluster's context and namespace with the K8s agent.\n")
	sb.WriteString("- If the server runs on a VM, no K8s context is available — use the database agent and OS-level diagnostics.\n")
	sb.WriteString("- K8s clusters not tied to any database server can still be inspected independently.\n")

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

		if err := checkAgentHealth(cfg.URL); err != nil {
			slog.Warn("agent unavailable", "agent", cfg.Name, "url", cfg.URL, "err", err)
			unavailable = append(unavailable, cfg.Name)
			continue
		}

		remoteAgent, err := remoteagent.NewA2A(remoteagent.A2AConfig{
			Name:            cfg.Name,
			Description:     cfg.Description,
			AgentCardSource: cfg.URL,
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
