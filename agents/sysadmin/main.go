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

	"github.com/google/uuid"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"helpdesk/agentutil"
	agentserve "helpdesk/agentutil/serve"
	"helpdesk/internal/audit"
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
		pgbackrestEvidenceRules, restoreVerifyEvidenceRules = loadSysadminEvidenceRules(rulesPath)
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

	cardOpts := cardOptions(tools)

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
		Description: "Check pgBackRest backup-job health: stanza status, the most recent backup's label/time/type, whether it's stale relative to the expected schedule, and wal_continuity — whether the archived WAL needed to restore that backup to the end of the archive is present and unbroken (status continuous, broken, unverified or no_backup; broken lists the gaps and where recovery would stop). This is the backup-taking precondition — distinct from the database agent's get_backup_status, which checks WAL archiving health via pg_stat_archiver.",
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
		Description: "Restore the database from the latest pgBackRest backup, overwriting the current data directory (destructive — requires operator approval). Restore-to-latest only: no point-in-time target. Refuses outright if the server is still accepting connections (pg_isready), or if the latest backup's WAL chain is not confirmed continuous to the end of the archive (wal_continuity.status other than continuous). Use only when get_pgbackrest_status confirms a healthy, non-stale backup with wal_continuity.status=continuous exists and the instance is confirmed down — never against a broken repo/stanza, a broken or unverified WAL chain, or a live instance. It keeps the WAL that was in pg_wal: moved aside before the restore, put back for recovery to replay past the end of the archive, and the segments past it pushed to the archive (pg_wal_tail: saved_to, replay_through_wal, pushed_to_archive); commits that were on disk but never archived are recovered rather than lost. On success it records where recovery should end (prediction: the archive end, any pg_wal tail, and the new timeline); after restarting the instance, call verify_restore_recovery to confirm where recovery actually ended.",
	}, restoreFromBackupTool)
	if err != nil {
		return nil, err
	}

	verifyRestoreRecoveryToolDef, err := functiontool.New(functiontool.Config{
		Name:        "verify_restore_recovery",
		Description: "After restore_from_backup and the restart, check where PostgreSQL's recovery actually ended against where restore_from_backup predicted it would (the last WAL segment restored from the archive, and the new timeline). Reads PostgreSQL's own recovery log lines. status: verified (as predicted), mismatch (stopped early, went past, or a different timeline — report it), not_found (no completed recovery in the log yet: the instance may not be restarted, or recovery failed before promotion), unverified (no prediction recorded for this target, or nothing restored from the archive). Also returns the exact LSN redo ended at, and replayed_through_wal when recovery replayed WAL that restore_from_backup put back into pg_wal past the archive. Read-only.",
	}, verifyRestoreRecoveryTool)
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
		verifyRestoreRecoveryToolDef,
	}, nil
}

// loadSysadminEvidenceRules loads agents/sysadmin/objective_evidence.yaml-shaped
// rules from path. Mirrors agents/database/main.go's loadDBEvidenceRules: one
// named return per sysadmin tool with its own objective-evidence probe.
func loadSysadminEvidenceRules(path string) (pgbackrestRules, restoreVerifyRules []evidence.Rule) {
	rulesByTool, err := evidence.LoadRules(path)
	if err != nil {
		slog.Error("failed to load objective_evidence rules — no forced-gate signals will fire from get_pgbackrest_status or verify_restore_recovery until this is fixed", "path", path, "err", err)
		return nil, nil
	}
	pgbackrestRules = rulesByTool["get_pgbackrest_status"]
	restoreVerifyRules = rulesByTool["verify_restore_recovery"]
	slog.Info("objective_evidence rules loaded", "path", path,
		"get_pgbackrest_status_rules", len(pgbackrestRules),
		"verify_restore_recovery_rules", len(restoreVerifyRules))
	return pgbackrestRules, restoreVerifyRules
}
