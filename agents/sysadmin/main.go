// Package main implements the sysadmin agent for host-level operations.
// It provides tools for inspecting and restarting database processes running
// in Docker, Podman, or systemd on the local host.
package main

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/google/uuid"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"helpdesk/agentutil"
	agentserve "helpdesk/agentutil/serve"
	"helpdesk/internal/audit"
	"helpdesk/internal/buildinfo"
	"helpdesk/internal/evidence"
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
			dbKeys := make([]string, 0, len(infraConfig.DBServers))
			for k := range infraConfig.DBServers {
				dbKeys = append(dbKeys, k)
			}
			sort.Strings(dbKeys)
			slog.Info("infrastructure config loaded", "databases", len(infraConfig.DBServers), "db_keys", strings.Join(dbKeys, ", "))
		}
	}

	// Load objective_evidence rules if available. Same convention as
	// agents/database/main.go's loadDBEvidenceRules: a loud warning when the
	// env var is simply unset, not a silent disable — see that file's own
	// comment for the live incident (2026-09-07) this convention exists to
	// prevent from recurring.
	if rulesPath := os.Getenv("HELPDESK_SYSADMIN_EVIDENCE_RULES"); rulesPath != "" {
		pgbackrestEvidenceRules = loadSysadminEvidenceRules(rulesPath)
	} else {
		slog.Warn("HELPDESK_SYSADMIN_EVIDENCE_RULES not set — objective-evidence force-gate disabled: no forced-gate signals will fire from get_pgbackrest_status, and faulttest will report EVIDENCE COVERAGE GAP on every run that would otherwise trip one")
	}

	// Initialize audit store if enabled.
	auditStore, err := agentserve.InitAuditStore(cfg)
	if err != nil {
		slog.Error("failed to initialize audit store", "err", err)
		os.Exit(1)
	}

	// Create trace store for propagating trace_id from incoming requests.
	traceStore := &audit.CurrentTraceStore{}

	if auditStore != nil {
		defer func() { _ = auditStore.Close() }()
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
	approvalClient := agentserve.InitApprovalClient(cfg)

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
			agentName:                            {"host", "infrastructure", "diagnostics"},
			agentName + "-check_host":            {"host", "container", "status"},
			agentName + "-get_host_logs":         {"host", "container", "logs"},
			agentName + "-check_disk":            {"host", "storage", "diagnostics"},
			agentName + "-check_memory":          {"host", "memory", "diagnostics"},
			agentName + "-read_pg_log_file":      {"host", "postgres", "logs", "diagnostics"},
			agentName + "-restart_container":     {"host", "container", "remediation"},
			agentName + "-restart_service":       {"host", "systemd", "remediation"},
			agentName + "-get_pgbackrest_status": {"host", "backup", "diagnostics"},
			agentName + "-run_pgbackrest_backup": {"host", "backup", "remediation"},
			agentName + "-restore_from_backup":   {"host", "backup", "remediation"},
		},
		SkillExamples: map[string][]string{
			agentName + "-check_host":            {"Is the alloydb-omni container running?"},
			agentName + "-get_host_logs":         {"Show the last 200 log lines from the prod_db container"},
			agentName + "-restart_container":     {"Restart the prod_db container — crash loop detected"},
			agentName + "-get_pgbackrest_status": {"Check whether pgBackRest's last backup for prod_db is stale"},
		},
		SkillAutoRemediationEligible: map[string]bool{
			agentName + "-restart_container": true,
			agentName + "-restart_service":   true,
		},
		SkillSchemaHash: agentutil.ComputeSchemaFingerprints(agentName, tools),
		ToolSchemas:     agentutil.ComputeInputSchemas(tools),
	}

	if err := agentserve.ServeWithTracingAndDirectTools(ctx, sysadminAgent, cfg, traceStore, auditStore, NewSysadminDirectRegistry(), cardOpts); err != nil {
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

	getPgBackRestStatusToolDef, err := functiontool.New(functiontool.Config{
		Name:        "get_pgbackrest_status",
		Description: "Check pgBackRest backup-job health: stanza status, the most recent backup's label/time/type, and whether it's stale relative to the expected schedule. This is the backup-taking precondition — distinct from the database agent's get_backup_status, which checks WAL archiving health via pg_stat_archiver.",
	}, getPgBackRestStatusTool)
	if err != nil {
		return nil, err
	}

	runPgBackRestBackupToolDef, err := functiontool.New(functiontool.Config{
		Name:        "run_pgbackrest_backup",
		Description: "Take a fresh pgBackRest backup (default type=full). Requires operator approval (Write action). Use only to remediate a STALE backup on an otherwise-healthy repo (get_pgbackrest_status status_code=0, backup_stale=true) — never against a broken repo/stanza (status_code != 0), which needs human investigation, not a new backup attempt.",
	}, runPgBackRestBackupTool)
	if err != nil {
		return nil, err
	}

	restoreFromBackupToolDef, err := functiontool.New(functiontool.Config{
		Name:        "restore_from_backup",
		Description: "Restore the database from the latest pgBackRest backup, overwriting the current data directory (destructive — requires operator approval). Restore-to-latest only: no point-in-time target. Refuses outright if the server is still accepting connections (pg_isready). Use only when get_pgbackrest_status confirms a healthy, non-stale backup exists and the instance is confirmed down — never against a broken repo/stanza or a live instance.",
	}, restoreFromBackupTool)
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
		getPgBackRestStatusToolDef,
		runPgBackRestBackupToolDef,
		restoreFromBackupToolDef,
	}, nil
}

// loadSysadminEvidenceRules loads agents/sysadmin/objective_evidence.yaml-shaped
// rules from path. Mirrors agents/database/main.go's loadDBEvidenceRules, sized
// down to this agent's one rule set so far (get_pgbackrest_status) — extend the
// same way (one more named return + one more rulesByTool[...] lookup) if a
// second sysadmin tool ever needs its own objective-evidence probe.
func loadSysadminEvidenceRules(path string) (pgbackrestRules []evidence.Rule) {
	rulesByTool, err := evidence.LoadRules(path)
	if err != nil {
		slog.Error("failed to load objective_evidence rules — no forced-gate signals will fire from get_pgbackrest_status until this is fixed", "path", path, "err", err)
		return nil
	}
	pgbackrestRules = rulesByTool["get_pgbackrest_status"]
	slog.Info("objective_evidence rules loaded", "path", path, "get_pgbackrest_status_rules", len(pgbackrestRules))
	return pgbackrestRules
}
