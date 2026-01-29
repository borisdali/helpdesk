// Package main implements the helpdesk orchestrator — a multi-agent system for
// troubleshooting database and infrastructure issues. It routes user queries
// to specialized sub-agents based on the problem domain.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/artifact"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/cmd/launcher/full"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/geminitool"

	"helpdesk/agentutil"
	"helpdesk/internal/logging"
	"helpdesk/prompts"
)

func main() {
	remainingArgs := logging.InitLogging(os.Args[1:])

	ctx := context.Background()

	cfg := agentutil.Config{
		ModelVendor: os.Getenv("HELPDESK_MODEL_VENDOR"),
		ModelName:   os.Getenv("HELPDESK_MODEL_NAME"),
		APIKey:      os.Getenv("HELPDESK_API_KEY"),
	}
	if cfg.ModelVendor == "" || cfg.ModelName == "" || cfg.APIKey == "" {
		slog.Error("missing required environment variables: HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY")
		os.Exit(1)
	}

	// Discover agents from URLs or load from config file.
	var agentConfigs []AgentConfig

	agentURLs := os.Getenv("HELPDESK_AGENT_URLS")
	if agentURLs != "" {
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
	for i, c := range agentConfigs {
		agentNames[i] = c.Name
	}
	slog.Info("expected expert agents", "agents", strings.Join(agentNames, ", "))

	// Create the LLM model
	llmModel, err := agentutil.NewLLM(ctx, cfg)
	if err != nil {
		slog.Error("failed to create model", "err", err)
		os.Exit(1)
	}

	// Create remote agent proxies (with health checking)
	remoteAgents, unavailableAgents := createRemoteAgents(agentConfigs)

	// Build the instruction with dynamic agent section and availability info
	instruction := prompts.Orchestrator + buildAgentPromptSection(agentConfigs)

	// Load infrastructure configuration (optional)
	infraConfigPath := os.Getenv("HELPDESK_INFRA_CONFIG")
	if infraConfigPath != "" {
		infraConfig, err := loadInfraConfig(infraConfigPath)
		if err != nil {
			slog.Error("failed to load infrastructure config", "path", infraConfigPath, "err", err)
			os.Exit(1)
		}
		instruction += buildInfraPromptSection(infraConfig)
		slog.Info("infrastructure config loaded", "db_servers", len(infraConfig.DBServers), "k8s_clusters", len(infraConfig.K8sClusters), "vms", len(infraConfig.VMs))
	}

	if len(unavailableAgents) > 0 {
		instruction += fmt.Sprintf("\n## Currently Unavailable Agents\nThe following agents are currently unavailable: %s\nIf you need these agents, inform the user and suggest they start the agent or try manual troubleshooting.\n",
			strings.Join(unavailableAgents, ", "))
	}

	// Create tools list
	var tools []tool.Tool
	if strings.ToLower(cfg.ModelVendor) == "google" || strings.ToLower(cfg.ModelVendor) == "gemini" {
		tools = append(tools, geminitool.GoogleSearch{})
	}

	// Create the root agent with sub-agents
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

	agentLoader, err := agent.NewMultiLoader(rootAgent, remoteAgents...)
	if err != nil {
		slog.Error("failed to create agent loader", "err", err)
		os.Exit(1)
	}

	artifactService := artifact.InMemoryService()
	sessionService := session.InMemoryService()

	config := &launcher.Config{
		ArtifactService: artifactService,
		SessionService:  sessionService,
		AgentLoader:     agentLoader,
	}

	l := full.NewLauncher()
	if err = l.Execute(ctx, config, remainingArgs); err != nil {
		slog.Error("failed to launch", "err", err, "usage", l.CommandLineSyntax())
		os.Exit(1)
	}
}
