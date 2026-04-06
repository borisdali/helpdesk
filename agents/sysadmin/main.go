// Package main implements the sysadmin agent for host-level operations.
// It provides tools for inspecting and restarting database processes running
// in Docker, Podman, or systemd on the local host.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/google/uuid"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/buildinfo"
	"helpdesk/internal/infra"
	"helpdesk/prompts"
)

// infraConfig holds the loaded infrastructure configuration (if available).
// Used by tools to resolve server IDs to HostConfig.
var infraConfig *infra.Config

func main() {
	cfg := agentutil.MustLoadConfig("localhost:1103")
	ctx := context.Background()

	// Enforce governance compliance in fix mode before any other initialization.
	agentutil.EnforceFixMode(ctx, agentutil.CheckFixModeViolations(cfg), "sysadmin_agent", cfg.AuditURL)

	// Load infrastructure config (required for resolving server IDs to HostConfig).
	if infraPath := os.Getenv("HELPDESK_INFRA_CONFIG"); infraPath != "" {
		var err error
		infraConfig, err = infra.Load(infraPath)
		if err != nil {
			slog.Warn("failed to load infrastructure config", "path", infraPath, "err", err)
		} else {
			slog.Info("infrastructure config loaded", "databases", len(infraConfig.DBServers))
		}
	}

	// Initialize audit store if enabled.
	auditStore, err := agentutil.InitAuditStore(cfg)
	if err != nil {
		slog.Error("failed to initialize audit store", "err", err)
		os.Exit(1)
	}

	// Create trace store for propagating trace_id from incoming requests.
	traceStore := &audit.CurrentTraceStore{}

	if auditStore != nil {
		defer auditStore.Close()
		sessionID := "sysadmin_" + uuid.New().String()[:8]
		toolAuditor = audit.NewToolAuditorWithTraceStore(auditStore, "sysadmin_agent", sessionID, traceStore)
		slog.Info("tool auditing enabled", "session_id", sessionID)
	}

	// Initialize policy engine if configured.
	policyEngine, err := agentutil.InitPolicyEngine(cfg)
	if err != nil {
		slog.Error("failed to initialize policy engine", "err", err)
		os.Exit(1)
	}

	// Initialize approval client for human-in-the-loop workflows.
	approvalClient := agentutil.InitApprovalClient(cfg)

	policyEnforcer = agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{
		Engine:                     policyEngine,
		PolicyCheckURL:             cfg.PolicyCheckURL,
		PolicyCheckAPIKey:          cfg.AuditAPIKey,
		TraceStore:                 traceStore,
		ApprovalClient:             approvalClient,
		ApprovalTimeout:            cfg.ApprovalTimeout,
		AgentName:                  "sysadmin_agent",
		ToolAuditor:                toolAuditor,
		RequirePurposeForSensitive: os.Getenv("HELPDESK_REQUIRE_PURPOSE_FOR_SENSITIVE") == "true",
	})

	slog.Info("governance",
		"audit", auditStore != nil,
		"policy", cfg.PolicyEnabled,
		"approval", approvalClient != nil,
	)

	llmModel, err := agentutil.NewLLM(ctx, cfg)
	if err != nil {
		slog.Error("failed to create LLM model", "err", err)
		os.Exit(1)
	}

	tools, err := createTools()
	if err != nil {
		slog.Error("failed to create tools", "err", err)
		os.Exit(1)
	}

	instruction := prompts.Sysadmin
	if infraConfig != nil {
		instruction += "\n\n## Known Infrastructure\n\n" + infraConfig.Summary()
	}

	sysadminAgent, err := llmagent.New(llmagent.Config{
		Name:        "sysadmin_agent",
		Description: "Host-level operations agent that can inspect container and systemd service status, retrieve logs, check disk and memory, and restart database processes when authorized.",
		Instruction: instruction,
		Model:       llmModel,
		Tools:       tools,
		AfterModelCallbacks: []llmagent.AfterModelCallback{
			agentutil.NewReasoningCallback(toolAuditor),
		},
	})
	if err != nil {
		slog.Error("failed to create sysadmin agent", "err", err)
		os.Exit(1)
	}

	const agentName = "sysadmin_agent"

	cardOpts := agentutil.CardOptions{
		Version:  buildinfo.Version,
		Provider: &a2a.AgentProvider{Org: "Helpdesk"},
		SkillTags: map[string][]string{
			agentName:                         {"host", "infrastructure", "diagnostics"},
			agentName + "-check_host":         {"host", "container", "status"},
			agentName + "-get_host_logs":      {"host", "container", "logs"},
			agentName + "-check_disk":          {"host", "storage", "diagnostics"},
			agentName + "-check_memory":        {"host", "memory", "diagnostics"},
			agentName + "-read_pg_log_file":    {"host", "postgres", "logs", "diagnostics"},
			agentName + "-restart_container":   {"host", "container", "remediation"},
			agentName + "-restart_service":    {"host", "systemd", "remediation"},
		},
		SkillExamples: map[string][]string{
			agentName + "-check_host":        {"Is the alloydb-omni container running?"},
			agentName + "-get_host_logs":     {"Show the last 200 log lines from the prod_db container"},
			agentName + "-restart_container": {"Restart the prod_db container — crash loop detected"},
		},
		SkillAutoRemediationEligible: map[string]bool{
			agentName + "-restart_container": true,
			agentName + "-restart_service":   true,
		},
		SkillSchemaHash: agentutil.ComputeSchemaFingerprints(agentName, tools),
		ToolSchemas:     agentutil.ComputeInputSchemas(tools),
	}

	if err := agentutil.ServeWithTracingAndDirectTools(ctx, sysadminAgent, cfg, traceStore, auditStore, NewSysadminDirectRegistry(), cardOpts); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func createTools() ([]tool.Tool, error) {
	checkHostToolDef, err := functiontool.New(functiontool.Config{
		Name:        "check_host",
		Description: "Check the status of the database process on the host. For Docker/Podman targets, inspects the container state. For systemd targets, shows the service ActiveState and SubState.",
	}, checkHostTool)
	if err != nil {
		return nil, err
	}

	getHostLogsToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_host_logs",
		Description: "Retrieve recent log lines from the database process. For Docker/Podman uses 'docker/podman logs'. For systemd uses journalctl. Use when the DB is down and read_pg_log is unavailable.",
	}, getHostLogsTool)
	if err != nil {
		return nil, err
	}

	checkDiskToolDef, err := functiontool.New(functiontool.Config{
		Name:        "check_disk",
		Description: "Show disk utilization on the host (df -h). Use to determine whether the database stopped due to a full data or log volume.",
	}, checkDiskTool)
	if err != nil {
		return nil, err
	}

	checkMemoryToolDef, err := functiontool.New(functiontool.Config{
		Name:        "check_memory",
		Description: "Show memory utilization on the host (free -h). Use to detect OOM conditions that may have caused the database process to be killed.",
	}, checkMemoryTool)
	if err != nil {
		return nil, err
	}

	readPgLogFileToolDef, err := functiontool.New(functiontool.Config{
		Name:        "read_pg_log_file",
		Description: "Read the PostgreSQL log file directly from inside the container or pod (via exec). Works when Postgres is down — does not require a live database connection. Use get_host_logs for process stdout/stderr; use this tool for the PostgreSQL log file written by logging_collector.",
	}, readPgLogFileTool)
	if err != nil {
		return nil, err
	}

	restartContainerToolDef, err := functiontool.New(functiontool.Config{
		Name:        "restart_container",
		Description: "Restart a Docker or Podman container hosting the database. Requires the server to have a container_runtime and container_name configured in infrastructure.json. Use restart_service for systemd-managed databases.",
	}, restartContainerTool)
	if err != nil {
		return nil, err
	}

	restartServiceToolDef, err := functiontool.New(functiontool.Config{
		Name:        "restart_service",
		Description: "Restart a systemd service hosting the database (systemctl restart). Requires the server to have a systemd_unit configured in infrastructure.json. Use restart_container for Docker/Podman-managed databases.",
	}, restartServiceTool)
	if err != nil {
		return nil, err
	}

	return []tool.Tool{
		checkHostToolDef,
		getHostLogsToolDef,
		checkDiskToolDef,
		checkMemoryToolDef,
		readPgLogFileToolDef,
		restartContainerToolDef,
		restartServiceToolDef,
	}, nil
}
