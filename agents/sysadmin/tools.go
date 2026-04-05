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
	"helpdesk/internal/infra"
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

// resolveHost returns the HostConfig, tags, and sensitivity for a DB server ID.
// Returns an error if infraConfig is not loaded or the server is unknown.
func resolveHost(serverID string) (*infra.HostConfig, []string, []string, error) {
	if infraConfig == nil {
		return nil, nil, nil, fmt.Errorf("no infrastructure config loaded; set HELPDESK_INFRA_CONFIG")
	}
	serverID = strings.TrimSpace(serverID)
	db, ok := infraConfig.DBServers[serverID]
	if !ok {
		known := make([]string, 0, len(infraConfig.DBServers))
		for id := range infraConfig.DBServers {
			known = append(known, id)
		}
		sort.Strings(known)
		return nil, nil, nil, fmt.Errorf("server %q not found in infrastructure config. Known servers: %s",
			serverID, strings.Join(known, ", "))
	}
	if db.Host == nil {
		return nil, db.Tags, db.Sensitivity, fmt.Errorf(
			"server %q has no host config; add a 'host' block to infrastructure.json", serverID)
	}
	return db.Host, db.Tags, db.Sensitivity, nil
}

// containerRuntimeBin returns the container runtime binary ("docker" or "podman"),
// or "" when the host uses systemd. Returns an error for unknown/unconfigured hosts.
func containerRuntimeBin(host *infra.HostConfig) (string, error) {
	switch host.ContainerRuntime {
	case "docker", "podman":
		return host.ContainerRuntime, nil
	case "":
		if host.SystemdUnit != "" {
			return "", nil // systemd path
		}
		return "", fmt.Errorf("host config has neither container_runtime nor systemd_unit configured")
	default:
		return "", fmt.Errorf("unknown container_runtime %q (supported: docker, podman)", host.ContainerRuntime)
	}
}

// ── check_host ───────────────────────────────────────────────────────────────

// CheckHostArgs defines arguments for check_host.
type CheckHostArgs struct {
	Target string `json:"target" jsonschema:"required,Server ID from infrastructure config (e.g. 'prod_db'). Must match a key in db_servers."`
}

func checkHostImpl(ctx context.Context, args CheckHostArgs) (CheckHostResult, error) {
	host, _, _, err := resolveHost(args.Target)
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
	host, _, _, err := resolveHost(args.Target)
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
	Target string `json:"target,omitempty" jsonschema:"Server ID for context and policy tagging (optional if running without infra config)."`
}

func checkDiskImpl(ctx context.Context, args CheckDiskArgs) (DiskResult, error) {
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
	Target string `json:"target,omitempty" jsonschema:"Server ID for context and policy tagging (optional if running without infra config)."`
}

func checkMemoryImpl(ctx context.Context, args CheckMemoryArgs) (MemoryResult, error) {
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

// ── restart_container ────────────────────────────────────────────────────────

// RestartContainerArgs defines arguments for restart_container.
type RestartContainerArgs struct {
	Target string `json:"target" jsonschema:"required,Server ID from infrastructure config."`
	Reason string `json:"reason" jsonschema:"required,Human-readable reason for the restart. Logged for audit trail."`
}

func restartContainerImpl(ctx context.Context, args RestartContainerArgs) (RestartResult, error) {
	host, tags, sensitivity, err := resolveHost(args.Target)
	if err != nil {
		return RestartResult{}, err
	}
	if host.ContainerRuntime == "" {
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
			policy.ActionDestructive, tags, "restart container: "+args.Reason, sensitivity); err != nil {
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
	host, tags, sensitivity, err := resolveHost(args.Target)
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
			policy.ActionDestructive, tags, "restart service: "+args.Reason, sensitivity); err != nil {
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
