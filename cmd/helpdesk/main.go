// Package main implements the helpdesk orchestrator — a multi-agent system for
// troubleshooting database and infrastructure issues. It routes user queries
// to specialized sub-agents based on the problem domain.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/artifact"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/cmd/launcher/full"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
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

	// Check if audit mode is enabled
	auditEnabled := os.Getenv("HELPDESK_AUDIT_ENABLED") == "true" || os.Getenv("HELPDESK_AUDIT_ENABLED") == "1"

	// Initialize audit store if enabled
	var auditor audit.Auditor
	if auditEnabled {
		auditURL := os.Getenv("HELPDESK_AUDIT_URL")
		if auditURL != "" {
			// Use central audit service (preferred)
			auditor = audit.NewRemoteStore(auditURL)
			slog.Info("audit logging enabled (remote)", "url", auditURL)
		} else {
			// Fall back to local store with socket (legacy mode)
			auditDir := os.Getenv("HELPDESK_AUDIT_DIR")
			if auditDir == "" {
				auditDir = "."
			}
			auditCfg := audit.StoreConfig{
				DBPath:     filepath.Join(auditDir, "audit.db"),
				SocketPath: filepath.Join(auditDir, "audit.sock"),
			}
			var err error
			auditor, err = audit.NewStore(auditCfg)
			if err != nil {
				slog.Error("failed to create audit store", "err", err)
				os.Exit(1)
			}
			slog.Info("audit logging enabled (local)", "db", auditCfg.DBPath, "socket", auditCfg.SocketPath)
		}
		defer auditor.Close()
	}

	// Create agent registry for delegate tool
	agentRegistry := audit.NewAgentRegistry()
	var unavailableAgents []string
	for _, cfg := range agentConfigs {
		if err := checkAgentHealth(cfg.URL); err != nil {
			slog.Warn("agent unavailable", "agent", cfg.Name, "url", cfg.URL, "err", err)
			unavailableAgents = append(unavailableAgents, cfg.Name)
			continue
		}
		agentRegistry.Register(cfg.Name, cfg.URL)
		slog.Info("agent available", "agent", cfg.Name)
	}

	// Create remote agent proxies for non-audit mode
	var remoteAgents []agent.Agent
	if !auditEnabled {
		remoteAgents, _ = createRemoteAgents(agentConfigs)
	}

	// Build the instruction: infrastructure first (so model sees the data before workflow),
	// then agents, then base prompt with workflow and examples.
	var instruction string

	// Load infrastructure configuration first (optional)
	infraConfigPath := os.Getenv("HELPDESK_INFRA_CONFIG")
	if infraConfigPath != "" {
		infraConfig, err := loadInfraConfig(infraConfigPath)
		if err != nil {
			slog.Error("failed to load infrastructure config", "path", infraConfigPath, "err", err)
			os.Exit(1)
		}
		instruction = buildInfraPromptSection(infraConfig)
		slog.Info("infrastructure config loaded", "db_servers", len(infraConfig.DBServers), "k8s_clusters", len(infraConfig.K8sClusters), "vms", len(infraConfig.VMs))
	}

	// Add base prompt and agent section
	if auditEnabled {
		// Use audit-aware prompt that requires delegate_to_agent tool
		instruction += prompts.OrchestratorAudit + buildAgentPromptSection(agentConfigs)
	} else {
		instruction += prompts.Orchestrator + buildAgentPromptSection(agentConfigs)
	}

	if len(unavailableAgents) > 0 {
		instruction += fmt.Sprintf("\n## Currently Unavailable Agents\nThe following agents are currently unavailable: %s\nIf you need these agents, inform the user and suggest they start the agent or try manual troubleshooting.\n",
			strings.Join(unavailableAgents, ", "))
	}

	// Create tools list
	var tools []tool.Tool
	if auditEnabled {
		// Create delegate tool with audit logging
		sessionID := "sess_" + uuid.New().String()[:8]
		userID := os.Getenv("USER")
		delegateTool, err := audit.DelegateTool(auditor, agentRegistry, sessionID, userID)
		if err != nil {
			slog.Error("failed to create delegate tool", "err", err)
			os.Exit(1)
		}
		tools = append(tools, delegateTool)
		slog.Info("delegate_to_agent tool created", "session_id", sessionID)
	}

	// Create the root agent
	agentConfig := llmagent.Config{
		Name:                "helpdesk_orchestrator",
		Model:               llmModel,
		Description:         "Multi-agent helpdesk system for database and infrastructure troubleshooting.",
		Instruction:         instruction,
		AfterModelCallbacks: []llmagent.AfterModelCallback{saveReportFunc},
	}

	if auditEnabled {
		// Use delegate tool instead of sub-agents for auditable routing
		agentConfig.Tools = tools
	} else {
		// Use direct sub-agent calls (original behavior)
		agentConfig.SubAgents = remoteAgents
	}

	rootAgent, err := llmagent.New(agentConfig)
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

	// Build launcher arguments
	launcherArgs := remainingArgs

	// Add streaming mode from env var if set (for container deployments)
	if streamingMode := os.Getenv("HELPDESK_STREAMING_MODE"); streamingMode != "" {
		// Prepend "console -streaming_mode=X" if no subcommand specified
		if len(launcherArgs) == 0 || (launcherArgs[0] != "console" && launcherArgs[0] != "web") {
			launcherArgs = append([]string{"console", "-streaming_mode=" + streamingMode}, launcherArgs...)
		}
		slog.Info("streaming mode configured", "mode", streamingMode)
	}

	l := full.NewLauncher()
	if err = l.Execute(ctx, config, launcherArgs); err != nil {
		slog.Error("failed to launch", "err", err, "usage", l.CommandLineSyntax())
		os.Exit(1)
	}
}
