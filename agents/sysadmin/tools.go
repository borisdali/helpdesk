package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"google.golang.org/adk/tool"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/policy"
)

// toolAuditor is set during initialization if auditing is enabled.
var toolAuditor *audit.ToolAuditor

// policyEnforcer is set during initialization for policy enforcement.
var policyEnforcer *agentutil.PolicyEnforcer

// CommandRunner abstracts command execution for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, env []string) (string, error)
}

// execRunner is the production CommandRunner.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// cmdRunner is the active command runner. Override in tests.
var cmdRunner CommandRunner = execRunner{}

// argsToStruct converts a map[string]any to a typed struct via JSON round-trip.
func argsToStruct[T any](args map[string]any) (T, error) {
	var result T
	data, err := json.Marshal(args)
	if err != nil {
		return result, err
	}
	return result, json.Unmarshal(data, &result)
}

// resolvedHost holds the operational properties of a DB server's host,
// assembled by traversing DBServer → VM or DBServer → K8sCluster in the
// infrastructure config.
type resolvedHost struct {
	// VM / container fields
	Runtime       string // container runtime binary: "docker", "podman", or "" (systemd/k8s)
	ContainerName string // from DBServer.ContainerName (docker/podman only)
	SystemdUnit   string // from DBServer.SystemdUnit (systemd only)
	// Kubernetes fields
	K8sContext     string // kubectl --context value
	K8sNamespace   string // pod namespace
	K8sPodSelector string // label selector used to locate the pod (e.g. "app=postgres")
	// Policy fields
	Tags        []string
	Sensitivity []string
}

// resolveHost looks up a DB server by ID and assembles the resolvedHost the
// sysadmin tools need. It handles both VM-hosted (docker/podman/systemd) and
// Kubernetes-hosted databases.
func resolveHost(serverID string) (resolvedHost, error) {
	if infraConfig == nil {
		return resolvedHost{}, fmt.Errorf("no infrastructure config loaded; set HELPDESK_INFRA_CONFIG")
	}
	serverID = strings.TrimSpace(serverID)
	db, ok := infraConfig.DBServers[serverID]
	if !ok {
		known := make([]string, 0, len(infraConfig.DBServers))
		for id := range infraConfig.DBServers {
			known = append(known, id)
		}
		sort.Strings(known)
		return resolvedHost{}, fmt.Errorf("server %q not found in infrastructure config. Known servers: %s",
			serverID, strings.Join(known, ", "))
	}

	// ── Kubernetes path ──────────────────────────────────────────────────────
	if db.K8sCluster != "" {
		k8s, ok := infraConfig.K8sClusters[db.K8sCluster]
		if !ok {
			return resolvedHost{}, fmt.Errorf(
				"server %q references K8s cluster %q which is not defined in infrastructure config", serverID, db.K8sCluster)
		}
		if db.K8sPodSelector == "" {
			return resolvedHost{}, fmt.Errorf(
				"server %q has k8s_cluster but no k8s_pod_selector; add a label selector (e.g. \"app=postgres\") to locate the pod", serverID)
		}
		ns := db.K8sNamespace
		if ns == "" {
			ns = "default"
		}
		return resolvedHost{
			K8sContext:     k8s.Context,
			K8sNamespace:   ns,
			K8sPodSelector: db.K8sPodSelector,
			Tags:           db.Tags,
			Sensitivity:    db.Sensitivity,
		}, nil
	}

	// ── VM path ──────────────────────────────────────────────────────────────
	if db.VMName == "" {
		return resolvedHost{}, fmt.Errorf(
			"server %q has neither vm_name nor k8s_cluster; sysadmin operations require one of these in infrastructure config", serverID)
	}
	vm, ok := infraConfig.VMs[db.VMName]
	if !ok {
		return resolvedHost{}, fmt.Errorf(
			"server %q references VM %q which is not defined in infrastructure config", serverID, db.VMName)
	}

	h := resolvedHost{
		Runtime:       vm.Runtime,
		ContainerName: db.ContainerName,
		SystemdUnit:   db.SystemdUnit,
		Tags:          db.Tags,
		Sensitivity:   db.Sensitivity,
	}

	// Validate: must be configured for either a container runtime or systemd.
	switch h.Runtime {
	case "docker", "podman":
		if h.ContainerName == "" {
			return resolvedHost{}, fmt.Errorf(
				"server %q: VM runtime is %q but no container_name is set on the db_server entry", serverID, h.Runtime)
		}
	case "":
		if h.SystemdUnit == "" {
			return resolvedHost{}, fmt.Errorf(
				"server %q: VM has no runtime (systemd expected) but no systemd_unit is set on the db_server entry", serverID)
		}
	default:
		return resolvedHost{}, fmt.Errorf(
			"server %q: VM has unknown runtime %q (supported: docker, podman, or empty for systemd)", serverID, h.Runtime)
	}

	return h, nil
}

// execInProcess runs cmd inside the process hosting the database — either via
// "docker/podman exec <container>" or "kubectl exec <pod>". Returns an error
// for systemd targets (no exec primitive available).
func execInProcess(ctx context.Context, host resolvedHost, cmd []string) (string, error) {
	switch {
	case host.Runtime == "docker" || host.Runtime == "podman":
		args := append([]string{"exec", host.ContainerName}, cmd...)
		return cmdRunner.Run(ctx, host.Runtime, args, nil)

	case host.K8sPodSelector != "":
		// Resolve the pod name from the selector first.
		getPodArgs := []string{
			"get", "pod",
			"-l", host.K8sPodSelector,
			"-n", host.K8sNamespace,
			"-o", "jsonpath={.items[0].metadata.name}",
		}
		if host.K8sContext != "" {
			getPodArgs = append([]string{"--context", host.K8sContext}, getPodArgs...)
		}
		podName, err := cmdRunner.Run(ctx, "kubectl", getPodArgs, nil)
		if err != nil {
			return "", fmt.Errorf("kubectl get pod: %w: %s", err, podName)
		}
		podName = strings.TrimSpace(podName)
		if podName == "" {
			return "", fmt.Errorf("no pod found for selector %q in namespace %q", host.K8sPodSelector, host.K8sNamespace)
		}
		execArgs := []string{"exec", podName, "-n", host.K8sNamespace}
		if host.K8sContext != "" {
			execArgs = append([]string{"--context", host.K8sContext}, execArgs...)
		}
		execArgs = append(execArgs, "--")
		execArgs = append(execArgs, cmd...)
		return cmdRunner.Run(ctx, "kubectl", execArgs, nil)

	default:
		return "", fmt.Errorf("cannot exec into process: target uses systemd (no exec primitive)")
	}
}

// containerRuntimeBin returns the container runtime binary ("docker" or "podman"),
// or "" when the host uses systemd.
func containerRuntimeBin(host resolvedHost) (string, error) {
	switch host.Runtime {
	case "docker", "podman":
		return host.Runtime, nil
	case "":
		return "", nil // systemd path
	default:
		return "", fmt.Errorf("unknown runtime %q (supported: docker, podman, or empty for systemd)", host.Runtime)
	}
}

// ── check_host ───────────────────────────────────────────────────────────────

// CheckHostArgs defines arguments for check_host.
type CheckHostArgs struct {
	Target string `json:"target" jsonschema:"required,Server ID from infrastructure config (e.g. 'prod_db'). Must match a key in db_servers."`
}

func checkHostImpl(ctx context.Context, args CheckHostArgs) (CheckHostResult, error) {
	host, err := resolveHost(args.Target)
	if err != nil {
		return CheckHostResult{}, err
	}

	runtime, err := containerRuntimeBin(host)
	if err != nil {
		return CheckHostResult{}, err
	}

	var runtimeLabel, output string
	var runErr error

	if runtime != "" {
		// Docker or Podman
		runtimeLabel = runtime
		output, runErr = cmdRunner.Run(ctx, runtime, []string{
			"inspect",
			"--format",
			"{{.State.Status}} (running={{.State.Running}}, restarting={{.State.Restarting}}, oomkilled={{.State.OOMKilled}}, dead={{.State.Dead}}, exitcode={{.State.ExitCode}})",
			host.ContainerName,
		}, nil)
		output = strings.TrimSpace(output)
		if runErr != nil {
			return CheckHostResult{
				ServerID: args.Target,
				Runtime:  runtimeLabel,
				Status:   "error",
				Details:  fmt.Sprintf("%v: %s", runErr, output),
			}, nil
		}
	} else {
		// Systemd
		runtimeLabel = "systemd"
		output, runErr = cmdRunner.Run(ctx, "systemctl", []string{
			"show", "--property=ActiveState,SubState,Result,MainPID,ExecMainStartTimestamp",
			host.SystemdUnit,
		}, nil)
		output = strings.TrimSpace(output)
		if runErr != nil {
			return CheckHostResult{
				ServerID: args.Target,
				Runtime:  runtimeLabel,
				Status:   "error",
				Details:  fmt.Sprintf("%v: %s", runErr, output),
			}, nil
		}
	}

	// Derive a status string from the raw output.
	status := "unknown"
	switch {
	case strings.Contains(output, "running=true"), strings.Contains(output, "ActiveState=active"):
		status = "running"
	case strings.Contains(output, "restarting=true"):
		status = "restarting"
	case strings.Contains(output, "dead=true"),
		strings.Contains(output, "ActiveState=inactive"),
		strings.Contains(output, "ActiveState=failed"):
		status = "stopped"
	}

	return CheckHostResult{
		ServerID: args.Target,
		Runtime:  runtimeLabel,
		Status:   status,
		Details:  output,
	}, nil
}

func checkHostTool(ctx tool.Context, args CheckHostArgs) (CheckHostResult, error) {
	return checkHostImpl(ctx, args)
}

// ── get_host_logs ────────────────────────────────────────────────────────────

// GetHostLogsArgs defines arguments for get_host_logs.
type GetHostLogsArgs struct {
	Target string `json:"target" jsonschema:"required,Server ID from infrastructure config."`
	Lines  int    `json:"lines,omitempty" jsonschema:"Number of recent log lines to return (default 100)."`
	Filter string `json:"filter,omitempty" jsonschema:"Optional substring to filter log lines (case-sensitive)."`
}

func getHostLogsImpl(ctx context.Context, args GetHostLogsArgs) (HostLogsResult, error) {
	host, err := resolveHost(args.Target)
	if err != nil {
		return HostLogsResult{}, err
	}

	lines := args.Lines
	if lines <= 0 {
		lines = 100
	}

	runtime, err := containerRuntimeBin(host)
	if err != nil {
		return HostLogsResult{}, err
	}

	var runtimeLabel, out string
	var runErr error

	if runtime != "" {
		runtimeLabel = runtime
		out, runErr = cmdRunner.Run(ctx, runtime, []string{
			"logs", "--tail", fmt.Sprintf("%d", lines), host.ContainerName,
		}, nil)
	} else {
		runtimeLabel = "systemd"
		out, runErr = cmdRunner.Run(ctx, "journalctl", []string{
			"-u", host.SystemdUnit,
			"-n", fmt.Sprintf("%d", lines),
			"--no-pager",
		}, nil)
	}

	if runErr != nil && strings.TrimSpace(out) == "" {
		return HostLogsResult{}, fmt.Errorf("get_host_logs %s: %w", args.Target, runErr)
	}

	logOutput := strings.TrimSpace(out)
	if args.Filter != "" {
		var filtered []string
		for _, line := range strings.Split(logOutput, "\n") {
			if strings.Contains(line, args.Filter) {
				filtered = append(filtered, line)
			}
		}
		logOutput = strings.Join(filtered, "\n")
	}

	linesReturned := 0
	if logOutput != "" {
		linesReturned = len(strings.Split(logOutput, "\n"))
	}

	return HostLogsResult{
		ServerID: args.Target,
		Runtime:  runtimeLabel,
		Lines:    linesReturned,
		Logs:     logOutput,
	}, nil
}

func getHostLogsTool(ctx tool.Context, args GetHostLogsArgs) (HostLogsResult, error) {
	return getHostLogsImpl(ctx, args)
}

// ── check_disk ───────────────────────────────────────────────────────────────

// CheckDiskArgs defines arguments for check_disk.
type CheckDiskArgs struct {
	Target    string `json:"target,omitempty" jsonschema:"Server ID from infrastructure config. When omitted, runs df on the agent host."`
	RunOnHost bool   `json:"run_on_host,omitempty" jsonschema:"If true, run df on the VM host OS instead of inside the container. Ignored for systemd targets."`
}

func checkDiskImpl(ctx context.Context, args CheckDiskArgs) (DiskResult, error) {
	// If a target is given and run_on_host is not set, exec inside the process.
	if args.Target != "" && !args.RunOnHost && infraConfig != nil {
		host, err := resolveHost(args.Target)
		if err != nil {
			return DiskResult{}, err
		}
		out, runErr := execInProcess(ctx, host, []string{"df", "-h"})
		if runErr == nil {
			return DiskResult{ServerID: args.Target, Output: strings.TrimSpace(out)}, nil
		}
		// Systemd targets return "cannot exec" — fall through to local df.
		if !strings.Contains(runErr.Error(), "cannot exec into process") {
			return DiskResult{}, fmt.Errorf("check_disk: %w: %s", runErr, out)
		}
	}
	out, err := cmdRunner.Run(ctx, "df", []string{"-h"}, nil)
	if err != nil {
		return DiskResult{}, fmt.Errorf("check_disk: %w: %s", err, out)
	}
	return DiskResult{
		ServerID: args.Target,
		Output:   strings.TrimSpace(out),
	}, nil
}

func checkDiskTool(ctx tool.Context, args CheckDiskArgs) (DiskResult, error) {
	return checkDiskImpl(ctx, args)
}

// ── check_memory ─────────────────────────────────────────────────────────────

// CheckMemoryArgs defines arguments for check_memory.
type CheckMemoryArgs struct {
	Target    string `json:"target,omitempty" jsonschema:"Server ID from infrastructure config. When omitted, runs free on the agent host."`
	RunOnHost bool   `json:"run_on_host,omitempty" jsonschema:"If true, run free on the VM host OS instead of inside the container. Ignored for systemd targets."`
}

func checkMemoryImpl(ctx context.Context, args CheckMemoryArgs) (MemoryResult, error) {
	// If a target is given and run_on_host is not set, exec inside the process.
	if args.Target != "" && !args.RunOnHost && infraConfig != nil {
		host, err := resolveHost(args.Target)
		if err != nil {
			return MemoryResult{}, err
		}
		out, runErr := execInProcess(ctx, host, []string{"free", "-h"})
		if runErr == nil {
			return MemoryResult{ServerID: args.Target, Output: strings.TrimSpace(out)}, nil
		}
		// Systemd targets return "cannot exec" — fall through to local free.
		if !strings.Contains(runErr.Error(), "cannot exec into process") {
			return MemoryResult{}, fmt.Errorf("check_memory: %w: %s", runErr, out)
		}
	}
	out, err := cmdRunner.Run(ctx, "free", []string{"-h"}, nil)
	if err != nil {
		return MemoryResult{}, fmt.Errorf("check_memory: %w: %s", err, out)
	}
	return MemoryResult{
		ServerID: args.Target,
		Output:   strings.TrimSpace(out),
	}, nil
}

func checkMemoryTool(ctx tool.Context, args CheckMemoryArgs) (MemoryResult, error) {
	return checkMemoryImpl(ctx, args)
}

// ── read_pg_log_file ──────────────────────────────────────────────────────────

const pgLogDefaultDir = "/var/lib/postgresql/data/log"

// ReadPgLogFileArgs defines arguments for read_pg_log_file.
type ReadPgLogFileArgs struct {
	Target  string `json:"target" jsonschema:"required,Server ID from infrastructure config."`
	Lines   int    `json:"lines,omitempty" jsonschema:"Number of recent log lines to return (default 100)."`
	Filter  string `json:"filter,omitempty" jsonschema:"Only return lines containing this string (case-insensitive). Useful for: ERROR, FATAL, PANIC, OOM, 'no space'."`
	LogPath string `json:"log_path,omitempty" jsonschema:"Override the log directory path inside the container. Default: /var/lib/postgresql/data/log."`
}

func readPgLogFileImpl(ctx context.Context, args ReadPgLogFileArgs) (PgLogFileResult, error) {
	host, err := resolveHost(args.Target)
	if err != nil {
		return PgLogFileResult{}, err
	}

	logDir := args.LogPath
	if logDir == "" {
		logDir = pgLogDefaultDir
	}

	lines := args.Lines
	if lines <= 0 {
		lines = 100
	}

	// Find the most recently modified log file.
	lsOut, err := execInProcess(ctx, host, []string{"ls", "-t", logDir})
	if err != nil {
		return PgLogFileResult{}, fmt.Errorf("read_pg_log_file: cannot list %s: %w", logDir, err)
	}
	files := strings.Fields(strings.TrimSpace(lsOut))
	if len(files) == 0 {
		return PgLogFileResult{
			ServerID: args.Target,
			Runtime:  hostRuntimeLabel(host),
			Logs:     fmt.Sprintf("no log files found in %s — logging_collector may not be enabled", logDir),
		}, nil
	}
	latestFile := logDir + "/" + files[0]

	// Read the last N lines.
	tailOut, err := execInProcess(ctx, host, []string{"tail", "-n", fmt.Sprintf("%d", lines), latestFile})
	if err != nil {
		return PgLogFileResult{}, fmt.Errorf("read_pg_log_file: cannot read %s: %w", latestFile, err)
	}
	logOutput := strings.TrimSpace(tailOut)

	// Apply filter (case-insensitive).
	if args.Filter != "" {
		lf := strings.ToLower(args.Filter)
		var filtered []string
		for _, line := range strings.Split(logOutput, "\n") {
			if strings.Contains(strings.ToLower(line), lf) {
				filtered = append(filtered, line)
			}
		}
		logOutput = strings.Join(filtered, "\n")
	}

	linesReturned := 0
	if logOutput != "" {
		linesReturned = len(strings.Split(logOutput, "\n"))
	}

	return PgLogFileResult{
		ServerID:      args.Target,
		Runtime:       hostRuntimeLabel(host),
		LinesReturned: linesReturned,
		Logs:          logOutput,
	}, nil
}

func readPgLogFileTool(ctx tool.Context, args ReadPgLogFileArgs) (PgLogFileResult, error) {
	return readPgLogFileImpl(ctx, args)
}

// hostRuntimeLabel returns a human-readable label for the host's exec mechanism.
func hostRuntimeLabel(host resolvedHost) string {
	if host.Runtime != "" {
		return host.Runtime
	}
	if host.K8sPodSelector != "" {
		return "kubectl"
	}
	return "systemd"
}

// ── restart_container ────────────────────────────────────────────────────────

// RestartContainerArgs defines arguments for restart_container.
type RestartContainerArgs struct {
	Target string `json:"target" jsonschema:"required,Server ID from infrastructure config."`
	Reason string `json:"reason" jsonschema:"required,Human-readable reason for the restart. Logged for audit trail."`
}

func restartContainerImpl(ctx context.Context, args RestartContainerArgs) (RestartResult, error) {
	host, err := resolveHost(args.Target)
	if err != nil {
		return RestartResult{}, err
	}
	if host.Runtime == "" {
		return RestartResult{}, fmt.Errorf(
			"server %q is managed via systemd, not a container runtime; use restart_service instead", args.Target)
	}

	runtime, err := containerRuntimeBin(host)
	if err != nil {
		return RestartResult{}, err
	}

	if policyEnforcer != nil {
		policyCtx := agentutil.WithToolName(ctx, "restart_container")
		if err := policyEnforcer.CheckTool(policyCtx, "host", args.Target,
			policy.ActionDestructive, host.Tags, "restart container: "+args.Reason, host.Sensitivity); err != nil {
			slog.Warn("policy denied container restart", "target", args.Target, "err", err)
			return RestartResult{}, err
		}
	}

	start := time.Now()
	out, runErr := cmdRunner.Run(ctx, runtime, []string{"restart", host.ContainerName}, nil)
	duration := time.Since(start)
	output := strings.TrimSpace(out)

	var errMsg string
	if runErr != nil {
		errMsg = runErr.Error()
		slog.Warn("restart_container failed",
			"target", args.Target, "container", host.ContainerName, "err", runErr)
	} else {
		slog.Info("restart_container succeeded",
			"target", args.Target, "container", host.ContainerName, "reason", args.Reason)
	}

	if toolAuditor != nil {
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       "restart_container",
			Parameters: map[string]any{"target": args.Target, "reason": args.Reason},
			RawCommand: fmt.Sprintf("%s restart %s", runtime, host.ContainerName),
		}, audit.ToolResult{
			Output: output,
			Error:  errMsg,
		}, duration)
	}

	return RestartResult{
		ServerID: args.Target,
		Runtime:  runtime,
		Target:   host.ContainerName,
		Success:  runErr == nil,
		Output:   output,
	}, runErr
}

func restartContainerTool(ctx tool.Context, args RestartContainerArgs) (RestartResult, error) {
	return restartContainerImpl(ctx, args)
}

// ── restart_service ──────────────────────────────────────────────────────────

// RestartServiceArgs defines arguments for restart_service.
type RestartServiceArgs struct {
	Target string `json:"target" jsonschema:"required,Server ID from infrastructure config."`
	Reason string `json:"reason" jsonschema:"required,Human-readable reason for the restart. Logged for audit trail."`
}

func restartServiceImpl(ctx context.Context, args RestartServiceArgs) (RestartResult, error) {
	host, err := resolveHost(args.Target)
	if err != nil {
		return RestartResult{}, err
	}
	if host.SystemdUnit == "" {
		return RestartResult{}, fmt.Errorf(
			"server %q uses a container runtime, not systemd; use restart_container instead", args.Target)
	}

	if policyEnforcer != nil {
		policyCtx := agentutil.WithToolName(ctx, "restart_service")
		if err := policyEnforcer.CheckTool(policyCtx, "host", args.Target,
			policy.ActionDestructive, host.Tags, "restart service: "+args.Reason, host.Sensitivity); err != nil {
			slog.Warn("policy denied service restart", "target", args.Target, "err", err)
			return RestartResult{}, err
		}
	}

	start := time.Now()
	out, runErr := cmdRunner.Run(ctx, "systemctl", []string{"restart", host.SystemdUnit}, nil)
	duration := time.Since(start)
	output := strings.TrimSpace(out)

	var errMsg string
	if runErr != nil {
		errMsg = runErr.Error()
		slog.Warn("restart_service failed",
			"target", args.Target, "unit", host.SystemdUnit, "err", runErr)
	} else {
		slog.Info("restart_service succeeded",
			"target", args.Target, "unit", host.SystemdUnit, "reason", args.Reason)
	}

	if toolAuditor != nil {
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       "restart_service",
			Parameters: map[string]any{"target": args.Target, "reason": args.Reason},
			RawCommand: fmt.Sprintf("systemctl restart %s", host.SystemdUnit),
		}, audit.ToolResult{
			Output: output,
			Error:  errMsg,
		}, duration)
	}

	return RestartResult{
		ServerID: args.Target,
		Runtime:  "systemd",
		Target:   host.SystemdUnit,
		Success:  runErr == nil,
		Output:   output,
	}, runErr
}

func restartServiceTool(ctx tool.Context, args RestartServiceArgs) (RestartResult, error) {
	return restartServiceImpl(ctx, args)
}

// ── DirectToolRegistry ───────────────────────────────────────────────────────

// marshalResult marshals a value to JSON string for the direct tool registry.
func marshalResult(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// NewSysadminDirectRegistry builds a DirectToolRegistry for all sysadmin tools.
// These handlers are invoked via POST /tool/{name} for deterministic fleet execution.
func NewSysadminDirectRegistry() *agentutil.DirectToolRegistry {
	r := agentutil.NewDirectToolRegistry()

	r.Register("check_host", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[CheckHostArgs](args)
		if err != nil {
			return "", err
		}
		result, err := checkHostImpl(ctx, a)
		if err != nil {
			return "", err
		}
		return marshalResult(result)
	})

	r.Register("get_host_logs", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetHostLogsArgs](args)
		if err != nil {
			return "", err
		}
		result, err := getHostLogsImpl(ctx, a)
		if err != nil {
			return "", err
		}
		return marshalResult(result)
	})

	r.Register("check_disk", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[CheckDiskArgs](args)
		if err != nil {
			return "", err
		}
		result, err := checkDiskImpl(ctx, a)
		if err != nil {
			return "", err
		}
		return marshalResult(result)
	})

	r.Register("check_memory", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[CheckMemoryArgs](args)
		if err != nil {
			return "", err
		}
		result, err := checkMemoryImpl(ctx, a)
		if err != nil {
			return "", err
		}
		return marshalResult(result)
	})

	r.Register("read_pg_log_file", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[ReadPgLogFileArgs](args)
		if err != nil {
			return "", err
		}
		result, err := readPgLogFileImpl(ctx, a)
		if err != nil {
			return "", err
		}
		return marshalResult(result)
	})

	r.Register("restart_container", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[RestartContainerArgs](args)
		if err != nil {
			return "", err
		}
		result, err := restartContainerImpl(ctx, a)
		if err != nil {
			return "", err
		}
		return marshalResult(result)
	})

	r.Register("restart_service", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[RestartServiceArgs](args)
		if err != nil {
			return "", err
		}
		result, err := restartServiceImpl(ctx, a)
		if err != nil {
			return "", err
		}
		return marshalResult(result)
	})

	return r
}
