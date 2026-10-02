package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/agent"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/evidence"
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
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		// Preserve the historical combined-output shape on failure: the
		// real diagnostic detail for most CLI tools lives on stderr, and
		// every "%w: %s" error-formatting call site across this package
		// already expects it folded in here.
		return stdout.String() + stderr.String(), err
	}
	if stderr.Len() > 0 {
		slog.Debug("command succeeded with stderr output", "name", name, "stderr", stderr.String())
	}
	// On success, stdout alone is the real output — not combined with
	// stderr. Found live, not hypothetical: kubectl-exec dispatch against a
	// real K8s pod broke get_pgbackrest_status's JSON parse
	// ("invalid character 'P' looking for beginning of value") because
	// pgBackRest wrote non-fatal env-var warnings to stderr (Kubernetes
	// auto-injects a `<SERVICE_NAME>_*` env var per Service in the
	// namespace, and pgBackRest warns about any that happen to look like
	// one of its own `PGBACKREST_*`-prefixed options) on an otherwise
	// perfectly healthy run. Docker-based testing never exercised this
	// path — Docker doesn't auto-inject Service-derived env vars the way
	// Kubernetes does, so stderr was always empty there regardless of
	// CombinedOutput's merging.
	return stdout.String(), nil
}

// cmdRunner is the active command runner. Override in tests.
var cmdRunner CommandRunner = execRunner{}

// infraConfigMu guards concurrent reads and writes to infraConfig, which may be
// updated at runtime via the register_infra_db direct tool.
var infraConfigMu sync.RWMutex

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
	// SSH fields — populated from the VM entry, empty unless the VM has SSHUser
	// set. See runOnHost (sshexec.go) for the local-vs-SSH dispatch this enables.
	VMAddress  string
	SSHUser    string
	SSHPort    int
	SSHKeyPath string
	// Kubernetes fields
	K8sContext     string // kubectl --context value
	K8sNamespace   string // pod namespace
	K8sPodSelector string // label selector used to locate the pod (e.g. "app=postgres")
	// Policy fields
	Tags        []string
	Sensitivity []string
}

// connStrPort extracts the port number from a libpq connection string or
// "host:port" address. Returns "" when no port is found.
func connStrPort(s string) string {
	// libpq keyword format: "... port=5432 ..."
	for _, field := range strings.Fields(s) {
		if strings.HasPrefix(field, "port=") {
			p := strings.TrimPrefix(field, "port=")
			if p != "" {
				return p
			}
		}
	}
	// "host:port" format (after stripping any scheme prefix)
	s = strings.TrimPrefix(s, "postgresql://")
	s = strings.TrimPrefix(s, "postgres://")
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		p := s[idx+1:]
		// strip any trailing path/query
		if sl := strings.IndexAny(p, "/?"); sl >= 0 {
			p = p[:sl]
		}
		if p != "" && p != s {
			return p
		}
	}
	// bare port (digits only)
	if strings.TrimFunc(s, func(r rune) bool { return r >= '0' && r <= '9' }) == "" && s != "" {
		return s
	}
	return ""
}

// resolveHost looks up a DB server by ID and assembles the resolvedHost the
// sysadmin tools need. It handles both VM-hosted (docker/podman/systemd) and
// Kubernetes-hosted databases.
// As a fallback, target may also be a connection string or port number — in
// that case the server is identified by matching the port against registered
// connection strings (useful for ephemeral auto-DB servers whose ID is not
// known to the LLM at startup time).
func resolveHost(serverID string) (resolvedHost, error) {
	infraConfigMu.RLock()
	ic := infraConfig
	if ic == nil {
		infraConfigMu.RUnlock()
		return resolvedHost{}, fmt.Errorf("no infrastructure config loaded; set HELPDESK_INFRA_CONFIG")
	}
	serverID = strings.TrimSpace(serverID)
	db, ok := ic.DBServers[serverID]
	if !ok {
		// Fallback 1: treat target as a connection string and match by port.
		// This allows the LLM to pass the connection string from triage findings
		// when it doesn't know the server ID (e.g. ephemeral auto-DB containers).
		if port := connStrPort(serverID); port != "" {
			for id, candidate := range ic.DBServers {
				if connStrPort(candidate.ConnectionString) == port {
					db = candidate
					serverID = id
					ok = true
					break
				}
			}
		}
	}
	if !ok {
		// Fallback 2: match by container name. The LLM often extracts the
		// container name from prior-run findings instead of using the server ID
		// or connection string. Scanning for a matching ContainerName lets it
		// succeed without knowing the server ID.
		for id, candidate := range ic.DBServers {
			if candidate.ContainerName == serverID {
				db = candidate
				serverID = id
				ok = true
				break
			}
		}
	}
	infraConfigMu.RUnlock()
	if !ok {
		known := make([]string, 0, len(ic.DBServers))
		for id := range ic.DBServers {
			known = append(known, id)
		}
		sort.Strings(known)
		return resolvedHost{}, fmt.Errorf("server %q not found in infrastructure config. Known servers: %s",
			serverID, strings.Join(known, ", "))
	}

	// ── Kubernetes path ──────────────────────────────────────────────────────
	if db.K8sCluster != "" {
		k8s, ok := ic.K8sClusters[db.K8sCluster]
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
	vm, ok := ic.VMs[db.VMName]
	if !ok {
		return resolvedHost{}, fmt.Errorf(
			"server %q references VM %q which is not defined in infrastructure config", serverID, db.VMName)
	}

	h := resolvedHost{
		Runtime:       vm.Runtime,
		ContainerName: db.ContainerName,
		SystemdUnit:   db.SystemdUnit,
		VMAddress:     vm.Address,
		SSHUser:       vm.SSHUser,
		SSHPort:       vm.SSHPort,
		SSHKeyPath:    vm.SSHKeyPath,
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
		podName, err := resolveK8sPodName(ctx, host)
		if err != nil {
			return "", err
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

// resolveK8sPodName resolves host.K8sPodSelector to a concrete pod name via
// "kubectl get pod -l <selector> -n <namespace>". Shared by execInProcess and
// checkHostK8s so both use identical pod-resolution logic.
func resolveK8sPodName(ctx context.Context, host resolvedHost) (string, error) {
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
	return podName, nil
}

// k8sContainerStatusJSON is one entry of `kubectl get pod -o json`'s
// status.containerStatuses array — named (not anonymous) so it can be
// referenced by type in checkHostK8s without duplicating the field list.
type k8sContainerStatusJSON struct {
	Ready        bool `json:"ready"`
	RestartCount int  `json:"restartCount"`
	State        struct {
		Waiting *struct {
			Reason string `json:"reason"`
		} `json:"waiting"`
	} `json:"state"`
	LastState struct {
		Terminated *struct {
			Reason   string `json:"reason"`
			ExitCode int    `json:"exitCode"`
		} `json:"terminated"`
	} `json:"lastState"`
}

// k8sPodStatusJSON is the minimal subset of `kubectl get pod -o json` output
// checkHostK8s needs.
type k8sPodStatusJSON struct {
	Status struct {
		Phase             string                   `json:"phase"`
		ContainerStatuses []k8sContainerStatusJSON `json:"containerStatuses"`
	} `json:"status"`
}

// checkHostK8s handles check_host for a Kubernetes-resolved target. There is
// no local docker/podman/systemd process to inspect here — the workload runs
// in a pod on a cluster. Rather than misbehave via the systemd branch (empty
// SystemdUnit — see checkHostImpl's dispatch), this surfaces real pod
// status/restart/last-termination fields, and makes the "this is Kubernetes,
// not Docker" signal unambiguous via Runtime="kubectl" — the field
// playbooks/sysadmin-docker-inspect.yaml's guidance checks first to decide
// whether to escalate to the K8s agent instead of continuing its
// docker-oriented steps.
func checkHostK8s(ctx context.Context, target string, host resolvedHost) (CheckHostResult, error) {
	podName, err := resolveK8sPodName(ctx, host)
	if err != nil {
		return CheckHostResult{}, fmt.Errorf("check_host: %w", err)
	}

	getArgs := []string{"get", "pod", podName, "-n", host.K8sNamespace, "-o", "json"}
	if host.K8sContext != "" {
		getArgs = append([]string{"--context", host.K8sContext}, getArgs...)
	}
	out, runErr := cmdRunner.Run(ctx, "kubectl", getArgs, nil)
	if runErr != nil {
		return CheckHostResult{
			ServerID: target,
			Runtime:  "kubectl",
			Status:   "error",
			Details:  fmt.Sprintf("%v: %s", runErr, strings.TrimSpace(out)),
		}, nil
	}

	var pod k8sPodStatusJSON
	if err := json.Unmarshal([]byte(out), &pod); err != nil {
		return CheckHostResult{}, fmt.Errorf("check_host: parsing kubectl output for pod %q: %w", podName, err)
	}

	var cs *k8sContainerStatusJSON
	if len(pod.Status.ContainerStatuses) > 0 {
		cs = &pod.Status.ContainerStatuses[0]
	}

	phase := pod.Status.Phase
	status := "unknown"
	switch {
	case cs != nil && cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff":
		status = "restarting"
	case phase == "Running" && cs != nil && cs.Ready:
		status = "running"
	case phase == "Failed" || phase == "Succeeded":
		status = "stopped"
	}

	lastReason, lastExit := "none", -1
	restartCount := 0
	if cs != nil {
		restartCount = cs.RestartCount
		if cs.LastState.Terminated != nil {
			lastReason = cs.LastState.Terminated.Reason
			lastExit = cs.LastState.Terminated.ExitCode
		}
	}

	details := fmt.Sprintf(
		"pod=%s phase=%s restart_count=%d last_termination_reason=%s last_termination_exitcode=%d",
		podName, phase, restartCount, lastReason, lastExit)

	return CheckHostResult{
		ServerID: target,
		Runtime:  "kubectl", // the definitive "this is K8s, not docker/podman/systemd" signal
		Status:   status,
		Details:  details,
	}, nil
}

// ── register_infra_db ─────────────────────────────────────────────────────────

// RegisterInfraDBArgs holds the payload for the register_infra_db direct tool.
// Not exposed to the LLM — only callable via POST /tool/register_infra_db.
type RegisterInfraDBArgs struct {
	ServerID      string `json:"server_id"`
	ContainerName string `json:"container_name"`
	Runtime       string `json:"runtime,omitempty"` // "docker" or "podman"; defaults to "docker"
	ConnStr       string `json:"conn_str,omitempty"`
}

const localVMKey = "__faulttest_local__"

// registerInfraDB adds a server entry to the in-memory infraConfig so sysadmin
// tools can resolve ephemeral containers (e.g. faulttest --auto-db) that are not
// present in the on-disk HELPDESK_INFRA_CONFIG.
func registerInfraDB(args RegisterInfraDBArgs) error {
	if args.ServerID == "" {
		return fmt.Errorf("register_infra_db: server_id is required")
	}
	if args.ContainerName == "" {
		return fmt.Errorf("register_infra_db: container_name is required")
	}
	runtime := args.Runtime
	if runtime == "" {
		runtime = "docker"
	}

	infraConfigMu.Lock()
	defer infraConfigMu.Unlock()

	if infraConfig == nil {
		infraConfig = &infra.Config{
			DBServers: make(map[string]infra.DBServer),
			VMs:       make(map[string]infra.VM),
		}
	}
	if infraConfig.DBServers == nil {
		infraConfig.DBServers = make(map[string]infra.DBServer)
	}
	if infraConfig.VMs == nil {
		infraConfig.VMs = make(map[string]infra.VM)
	}

	infraConfig.VMs[localVMKey] = infra.VM{
		Name:    localVMKey,
		Runtime: runtime,
	}
	infraConfig.DBServers[args.ServerID] = infra.DBServer{
		Name:             args.ServerID,
		ConnectionString: args.ConnStr,
		VMName:           localVMKey,
		ContainerName:    args.ContainerName,
		Tags:             []string{"chaos", "test"},
	}
	slog.Info("register_infra_db: server registered", "server_id", args.ServerID, "container", args.ContainerName)
	return nil
}

// ── check_host ───────────────────────────────────────────────────────────────

// CheckHostArgs defines arguments for check_host.
type CheckHostArgs struct {
	Target string `json:"target" jsonschema:"required,Server ID from infrastructure config (e.g. 'prod_db'), or a connection string / port (e.g. 'host=127.0.0.1 port=5432' or '5432') when the server ID is unknown — resolved by port matching."`
}

func checkHostImpl(ctx context.Context, args CheckHostArgs) (CheckHostResult, error) {
	host, err := resolveHost(args.Target)
	if err != nil {
		return CheckHostResult{}, err
	}

	// Kubernetes-resolved hosts have no docker/podman/systemd runtime to
	// inspect locally — the workload runs in a pod on a cluster. Check this
	// FIRST: containerRuntimeBin only distinguishes docker/podman from ""
	// (systemd), and resolveHost never populates Runtime on the K8s path —
	// without this branch a K8s-resolved host would silently fall into the
	// systemd path below with an empty SystemdUnit.
	if host.K8sPodSelector != "" {
		return checkHostK8s(ctx, args.Target, host)
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

func checkHostTool(ctx agent.ToolContext, args CheckHostArgs) (CheckHostResult, error) {
	start := time.Now()
	result, err := checkHostImpl(ctx, args)
	duration := time.Since(start)
	if err == nil {
		slog.Info("tool ok", "name", "check_host", "ms", duration.Milliseconds())
	}
	if toolAuditor != nil {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       "check_host",
			Parameters: map[string]any{"target": args.Target},
		}, audit.ToolResult{
			Output: result.Details,
			Error:  errMsg,
		}, duration)
	}
	return result, err
}

// ── get_host_logs ────────────────────────────────────────────────────────────

// GetHostLogsArgs defines arguments for get_host_logs.
type GetHostLogsArgs struct {
	Target string `json:"target" jsonschema:"required,Server ID from infrastructure config, or a connection string / port — resolved by port matching."`
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

	// When docker/podman logs return nothing, PostgreSQL is almost certainly
	// configured with logging_collector=on, which silences container stdout.
	// Tell the agent explicitly — an empty result here is not a dead end,
	// it is the signal to call read_pg_log_file next.
	if logOutput == "" && runtime != "" {
		logOutput = "(no output in container stdout/stderr — logging_collector=on is likely active, " +
			"which redirects all PostgreSQL output to an on-disk log file. " +
			"Call read_pg_log_file to read the PostgreSQL log.)"
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

func getHostLogsTool(ctx agent.ToolContext, args GetHostLogsArgs) (HostLogsResult, error) {
	start := time.Now()
	result, err := getHostLogsImpl(ctx, args)
	duration := time.Since(start)
	if err == nil {
		slog.Info("tool ok", "name", "get_host_logs", "ms", duration.Milliseconds())
	}
	if toolAuditor != nil {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       "get_host_logs",
			Parameters: map[string]any{"target": args.Target, "lines": args.Lines, "filter": args.Filter},
		}, audit.ToolResult{
			Output: result.Logs,
			Error:  errMsg,
		}, duration)
	}
	return result, err
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

func checkDiskTool(ctx agent.ToolContext, args CheckDiskArgs) (DiskResult, error) {
	start := time.Now()
	result, err := checkDiskImpl(ctx, args)
	duration := time.Since(start)
	if err == nil {
		slog.Info("tool ok", "name", "check_disk", "ms", duration.Milliseconds())
	}
	if toolAuditor != nil {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       "check_disk",
			Parameters: map[string]any{"target": args.Target, "run_on_host": args.RunOnHost},
		}, audit.ToolResult{
			Output: result.Output,
			Error:  errMsg,
		}, duration)
	}
	return result, err
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

func checkMemoryTool(ctx agent.ToolContext, args CheckMemoryArgs) (MemoryResult, error) {
	start := time.Now()
	result, err := checkMemoryImpl(ctx, args)
	duration := time.Since(start)
	if err == nil {
		slog.Info("tool ok", "name", "check_memory", "ms", duration.Milliseconds())
	}
	if toolAuditor != nil {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       "check_memory",
			Parameters: map[string]any{"target": args.Target, "run_on_host": args.RunOnHost},
		}, audit.ToolResult{
			Output: result.Output,
			Error:  errMsg,
		}, duration)
	}
	return result, err
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
	lsOut, lsErr := execInProcess(ctx, host, []string{"ls", "-t", logDir})
	if lsErr != nil {
		// Container may be stopped (e.g. after a crash).  Fall back to
		// docker/podman cp so the log file is still readable post-mortem.
		if host.Runtime == "docker" || host.Runtime == "podman" {
			return readPgLogFileViaCopy(ctx, host, logDir, lines, args.Filter, args.Target)
		}
		return PgLogFileResult{}, fmt.Errorf("read_pg_log_file: cannot list %s: %w", logDir, lsErr)
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

func readPgLogFileTool(ctx agent.ToolContext, args ReadPgLogFileArgs) (PgLogFileResult, error) {
	start := time.Now()
	result, err := readPgLogFileImpl(ctx, args)
	duration := time.Since(start)
	if err == nil {
		slog.Info("tool ok", "name", "read_pg_log_file", "ms", duration.Milliseconds())
	}
	if toolAuditor != nil {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       "read_pg_log_file",
			Parameters: map[string]any{"target": args.Target, "lines": args.Lines, "filter": args.Filter, "log_path": args.LogPath},
		}, audit.ToolResult{
			Output: result.Logs,
			Error:  errMsg,
		}, duration)
	}
	return result, err
}

// readPgLogFileViaCopy reads the PostgreSQL log from a stopped container using
// "docker/podman cp" instead of exec.  Called as a fallback when execInProcess
// fails — typically because the container crashed and is no longer running.
func readPgLogFileViaCopy(ctx context.Context, host resolvedHost, logDir string, lines int, filter, serverID string) (PgLogFileResult, error) {
	tmpDir, err := os.MkdirTemp("", "pg_log_*")
	if err != nil {
		return PgLogFileResult{}, fmt.Errorf("read_pg_log_file: cannot create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// docker cp containerName:logDir/. tmpDir copies the directory contents.
	src := host.ContainerName + ":" + logDir + "/."
	if _, cpErr := cmdRunner.Run(ctx, host.Runtime, []string{"cp", src, tmpDir}, nil); cpErr != nil {
		return PgLogFileResult{}, fmt.Errorf("read_pg_log_file: container is stopped and docker cp failed — log dir may not exist: %w", cpErr)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil || len(entries) == 0 {
		return PgLogFileResult{
			ServerID: serverID,
			Runtime:  hostRuntimeLabel(host),
			Logs:     fmt.Sprintf("no log files found in %s (read from stopped container via cp)", logDir),
		}, nil
	}

	// Sort by modification time descending to pick the most recent file.
	type fileEntry struct {
		name    string
		modTime time.Time
	}
	var fes []fileEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fes = append(fes, fileEntry{e.Name(), info.ModTime()})
	}
	if len(fes) == 0 {
		return PgLogFileResult{
			ServerID: serverID,
			Runtime:  hostRuntimeLabel(host),
			Logs:     fmt.Sprintf("no log files found in %s (read from stopped container via cp)", logDir),
		}, nil
	}
	sort.Slice(fes, func(i, j int) bool { return fes[i].modTime.After(fes[j].modTime) })

	content, err := os.ReadFile(tmpDir + "/" + fes[0].name)
	if err != nil {
		return PgLogFileResult{}, fmt.Errorf("read_pg_log_file: cannot read %s: %w", fes[0].name, err)
	}

	allLines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(allLines) > lines {
		allLines = allLines[len(allLines)-lines:]
	}
	logOutput := strings.Join(allLines, "\n")

	if filter != "" {
		lf := strings.ToLower(filter)
		var keep []string
		for _, line := range strings.Split(logOutput, "\n") {
			if strings.Contains(strings.ToLower(line), lf) {
				keep = append(keep, line)
			}
		}
		logOutput = strings.Join(keep, "\n")
	}

	linesReturned := 0
	if logOutput != "" {
		linesReturned = len(strings.Split(logOutput, "\n"))
	}
	return PgLogFileResult{
		ServerID:      serverID,
		Runtime:       hostRuntimeLabel(host),
		LinesReturned: linesReturned,
		Logs:          logOutput,
	}, nil
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
	Target string `json:"target" jsonschema:"required,Server ID from infrastructure config, or a connection string / port — resolved by port matching."`
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

func restartContainerTool(ctx agent.ToolContext, args RestartContainerArgs) (RestartResult, error) {
	return restartContainerImpl(ctx, args)
}

// ── restart_service ──────────────────────────────────────────────────────────

// RestartServiceArgs defines arguments for restart_service.
type RestartServiceArgs struct {
	Target string `json:"target" jsonschema:"required,Server ID from infrastructure config, or a connection string / port — resolved by port matching."`
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

func restartServiceTool(ctx agent.ToolContext, args RestartServiceArgs) (RestartResult, error) {
	return restartServiceImpl(ctx, args)
}

// ── get_pgbackrest_status ───────────────────────────────────────────────────

// defaultPgBackRestMaxAgeHours is the default staleness bound applied when
// MaxAgeHours is unset: one day plus a real margin, not an arbitrary round
// number — matches this project's convention of justifying thresholds (see
// e.g. replica_stalled's 20s-with-margin-below-a-60s-ceiling) rather than
// picking one for its own sake. A daily backup is the most common cadence;
// callers with a different real schedule (hourly, weekly) should pass their
// own MaxAgeHours rather than rely on this default.
const defaultPgBackRestMaxAgeHours = 25

// GetPgBackRestStatusArgs defines arguments for the get_pgbackrest_status tool.
type GetPgBackRestStatusArgs struct {
	Target      string `json:"target,omitempty" jsonschema:"Server ID from infrastructure config. Required whenever infrastructure config is loaded — omitting it then is treated as a resolution failure and errors loudly, not a request to run locally. Only leave this empty when no infrastructure config exists at all (a genuinely colocated deployment). If your connection_string/server ID doesn't match a known infrastructure entry, do not guess or omit target — report that as its own finding and escalate."`
	Stanza      string `json:"stanza,omitempty" jsonschema:"pgBackRest stanza name. When omitted, the only stanza present is used — an error if more than one exists."`
	MaxAgeHours int    `json:"max_age_hours,omitempty" jsonschema:"How many hours old the most recent successful backup may be before it's considered stale. Default 25 (one day plus margin) — override to match the target's actual backup schedule. Zero or negative values fall back to the default; there is no way to force an immediate backup to read as stale via this parameter."`
}

// PgBackRestBackup is one entry from pgBackRest's own "backup" array — one
// real backup attempt that completed (a hard failure that never completes
// adds no entry here at all; see GetPgBackRestStatusResult's own doc comment
// for why staleness, not this Error field, is the primary health signal).
type PgBackRestBackup struct {
	Label     string    `json:"label"`
	Type      string    `json:"type"` // full, diff, incr
	Error     bool      `json:"error"`
	StartTime time.Time `json:"start_time"`
	StopTime  time.Time `json:"stop_time"`
}

// GetPgBackRestStatusResult is the structured result for get_pgbackrest_status.
// Schema verified directly against a real pgBackRest 2.59.1 install (not
// assumed from documentation alone) — see project memory for the full
// captured `info --output=json` example this parser is built against.
//
// BackupStale, not the most recent backup's own Error field, is the primary
// health signal: a hard failure (repo unreachable, permission denied) exits
// nonzero and adds NO entry to pgBackRest's own backup array at all —
// confirmed live by attempting a backup with a broken archive_command and
// observing no new entry appeared. Error=true on an entry that DID complete
// most likely marks a softer, file-level issue (e.g. a checksum problem),
// not "the last attempt failed to run" — that case shows up as staleness
// instead, since no successful completion ever updates LastBackupTime.
type GetPgBackRestStatusResult struct {
	Output        string `json:"output"`
	Stanza        string `json:"stanza,omitempty"`
	StatusCode    int    `json:"status_code"`
	StatusMessage string `json:"status_message,omitempty"`
	// RepoStatusMessage, when non-empty, is a more specific error than
	// StatusMessage — verified live: a broken repo path leaves StatusMessage
	// as a generic "other" but RepoStatusMessage holds the real detail (e.g.
	// a PathOpenError naming the exact path and errno). Always prefer this
	// over StatusMessage when both are present.
	RepoStatusCode    int                `json:"repo_status_code,omitempty"`
	RepoStatusMessage string             `json:"repo_status_message,omitempty"`
	Backups           []PgBackRestBackup `json:"backups,omitempty"`
	LastBackupLabel   string             `json:"last_backup_label,omitempty"`
	LastBackupTime    string             `json:"last_backup_time,omitempty"` // RFC3339; empty if no backup has ever succeeded
	// LastBackupError deliberately has no `omitempty` — unlike LastBackupTime
	// (where "absent" and "empty" are the same real state: no backup ever
	// succeeded), false here is a meaningful, distinct answer from "not
	// computed," and omitempty would silently drop it from the JSON the
	// model reads. See BackupStale's own comment below for the live
	// confusion this exact pattern caused for that field.
	LastBackupError bool `json:"last_backup_error"`
	// BackupStale is true when a most-recent backup exists and is older than
	// the requested (or default) MaxAgeHours. Deliberately false, not true,
	// when no backup has EVER succeeded (LastBackupTime == "") — "never
	// backed up" and "backed up too long ago" are different findings, and
	// callers must check LastBackupTime == "" for the former rather than
	// relying on this field alone.
	//
	// No `omitempty`: found live 2026-09-30 that a model reading this result
	// treated the field's *absence* (the omitempty-dropped false case) as
	// ambiguous — "maybe stale" — rather than confidently reading it as a
	// definite "no," and proceeded to call run_pgbackrest_backup on an
	// already-current backup rather than following its own playbook's
	// Ending A ("already healthy, nothing to do"). A write action should
	// never hinge on whether the model correctly reconstructs a boolean from
	// its absence.
	BackupStale bool `json:"backup_stale"`
}

// PgBackRestSummary is the single synthesized item get_pgbackrest_status'
// objective_evidence probe thresholds — mirrors BackupArchiverSummary's own
// doc comment in agents/database/tools.go (v0.30 Part B): one item, since
// stanza health is a property of the whole result, not a per-row signal.
type PgBackRestSummary struct {
	Stanza          string
	StatusCode      int
	BackupStale     bool
	NeverBackedUp   bool
	LastBackupLabel string
}

// pgbackrestEvidenceSchema declares get_pgbackrest_status' probe:
// backup_unhealthy, true when the stanza itself reports a non-ok status, the
// most recent backup is stale, or no backup has ever succeeded. See
// agents/sysadmin/objective_evidence.yaml for the active configuration.
//
// The resource extractor deliberately returns Stanza, not LastBackupLabel —
// found live 2026-09-30: backup_unhealthy is an OR of three conditions
// (StatusCode != 0, BackupStale, NeverBackedUp), but LastBackupLabel is only
// guaranteed non-empty for one of them (a stale-but-taken backup). For the
// other two — a broken repo (confirmed live: reports stanza "[invalid]",
// LastBackupLabel empty) and never-backed-up (LastBackupLabel empty by
// definition) — resource_named_in_quote's own `if ev.Resource == ""
// { return false }` guard made confirmation structurally impossible
// regardless of how thoroughly the model engaged with the real
// status_code/repo_status_message data. Stanza is populated in every one of
// parsePgBackRestInfo's resolution outcomes (including "[invalid]" for a
// broken repo), so it's always a real, quotable string to check for.
var pgbackrestEvidenceSchema = evidence.NewToolSchema[PgBackRestSummary]("get_pgbackrest_status", func(s PgBackRestSummary) string {
	return s.Stanza
}).
	Bool("backup_unhealthy", func(s PgBackRestSummary) bool {
		return s.StatusCode != 0 || s.BackupStale || s.NeverBackedUp
	}).
	Register()

// pgbackrestEvidenceRules holds the loaded rules for pgbackrestEvidenceSchema,
// set by main() at startup. See loadDBEvidenceRules's sibling in
// agents/database/main.go for the nil/empty convention this mirrors.
var pgbackrestEvidenceRules []evidence.Rule

// pgBackRestRawStanza/pgBackRestRawBackup/pgBackRestRawStatus mirror pgBackRest's
// own `info --output=json` schema exactly as verified live (2026-09-26) — see
// GetPgBackRestStatusResult's doc comment. Unexported: these are a parsing
// detail, never returned directly.
type pgBackRestRawStanza struct {
	Name   string `json:"name"`
	Status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
	// Repo carries a second, often more specific status than the top-level
	// one above — verified live (2026-09-26): a permission-denied repo path
	// produced Name="[invalid]", top-level status {99, "other"}, but
	// Repo[0].Status.Message held the real detail:
	// "[PathOpenError] unable to list file info for path
	// '/var/lib/pgbackrest/backup': [13] Permission denied". Always prefer
	// this message when present; it's what a human actually needs to see.
	Repo []struct {
		Status struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"status"`
	} `json:"repo"`
	Backup []pgBackRestRawBackup `json:"backup"`
}

type pgBackRestRawBackup struct {
	Label     string `json:"label"`
	Type      string `json:"type"`
	Error     bool   `json:"error"`
	Timestamp struct {
		Start int64 `json:"start"`
		Stop  int64 `json:"stop"`
	} `json:"timestamp"`
}

// jsonArrayPrefix strips any non-JSON preamble before the first `[`,
// returning output unchanged if none is found (letting json.Unmarshal
// produce its own clear error rather than masking a genuinely different
// problem). pgbackrest writes its own non-fatal `WARN:`/`INFO:` lines to
// *stdout*, ahead of the real JSON array, whenever it has something to
// complain about on an otherwise healthy run — confirmed live against a
// real Kubernetes pod: Kubernetes auto-injects a `<SERVICE_NAME>_*` env var
// per Service in the namespace, and pgbackrest warns about any that happen
// to resemble one of its own `PGBACKREST_*`-prefixed options
// ("WARN: environment contains invalid option 'demo-service-port'"),
// breaking a naive json.Unmarshal of the raw combined output. This is
// pgbackrest's own stdout behavior, not a stdout/stderr-mixing artifact of
// this codebase's own dispatch layer (that was the first, wrong hypothesis
// — both streams were checked directly, live, before writing this). Docker-
// based testing never exercised this because Docker doesn't auto-inject
// Service-derived env vars the way Kubernetes does.
func jsonArrayPrefix(output string) string {
	if idx := strings.IndexByte(output, '['); idx >= 0 {
		return output[idx:]
	}
	return output
}

// parsePgBackRestInfo parses `pgbackrest info --output=json` output (an
// array of stanzas — confirmed correct even for a single-stanza instance,
// live, 2026-09-26) and computes staleness for the requested stanza.
// wantStanza matches by exact name; when that fails (including when
// wantStanza is empty), a single remaining entry is used regardless of its
// name — see the name-resolution block below for why a broken stanza's own
// reported name can't be relied on. More than one candidate with no exact
// match is an error rather than a silent guess.
func parsePgBackRestInfo(output, wantStanza string, maxAge time.Duration) (GetPgBackRestStatusResult, error) {
	var raw []pgBackRestRawStanza
	if err := json.Unmarshal([]byte(jsonArrayPrefix(output)), &raw); err != nil {
		return GetPgBackRestStatusResult{}, fmt.Errorf("parsing pgbackrest info JSON: %w", err)
	}

	var stanza *pgBackRestRawStanza
	if wantStanza != "" {
		for i := range raw {
			if raw[i].Name == wantStanza {
				stanza = &raw[i]
				break
			}
		}
	}
	if stanza == nil {
		// No exact name match (or none was requested). A single entry is
		// used regardless of its name/whether one was requested — verified
		// live that a critically broken stanza can report Name="[invalid]"
		// rather than its configured name, and that's exactly the case
		// callers most need surfaced, not silently turned into a generic
		// "not found" error that discards the real diagnostic detail sitting
		// right there in the same response.
		switch len(raw) {
		case 0:
			return GetPgBackRestStatusResult{}, fmt.Errorf("pgbackrest info returned no stanzas at all — repo1-path likely doesn't exist or isn't reachable")
		case 1:
			stanza = &raw[0]
		default:
			names := make([]string, len(raw))
			for i := range raw {
				names[i] = raw[i].Name
			}
			if wantStanza != "" {
				return GetPgBackRestStatusResult{}, fmt.Errorf("stanza %q not found among %d present (%s)", wantStanza, len(raw), strings.Join(names, ", "))
			}
			return GetPgBackRestStatusResult{}, fmt.Errorf("no stanza specified and %d stanzas present (%s) — pass one explicitly", len(raw), strings.Join(names, ", "))
		}
	}

	result := GetPgBackRestStatusResult{
		Stanza:        stanza.Name,
		StatusCode:    stanza.Status.Code,
		StatusMessage: stanza.Status.Message,
	}
	if len(stanza.Repo) > 0 {
		result.RepoStatusCode = stanza.Repo[0].Status.Code
		result.RepoStatusMessage = stanza.Repo[0].Status.Message
	}
	for _, b := range stanza.Backup {
		result.Backups = append(result.Backups, PgBackRestBackup{
			Label:     b.Label,
			Type:      b.Type,
			Error:     b.Error,
			StartTime: time.Unix(b.Timestamp.Start, 0).UTC(),
			StopTime:  time.Unix(b.Timestamp.Stop, 0).UTC(),
		})
	}

	// Most recent by StopTime, not array position — pgBackRest's own
	// documentation examples (backup[-1]) confirm array order is
	// chronological, but comparing explicitly costs nothing and removes the
	// assumption entirely.
	var last *PgBackRestBackup
	for i := range result.Backups {
		if last == nil || result.Backups[i].StopTime.After(last.StopTime) {
			last = &result.Backups[i]
		}
	}
	if last != nil {
		result.LastBackupLabel = last.Label
		result.LastBackupTime = last.StopTime.Format(time.RFC3339)
		result.LastBackupError = last.Error
		if maxAge > 0 {
			result.BackupStale = time.Since(last.StopTime) > maxAge
		}
	}
	return result, nil
}

func getPgBackRestStatusImpl(ctx context.Context, args GetPgBackRestStatusArgs) (GetPgBackRestStatusResult, error) {
	cmdArgs := []string{"info", "--output=json"}
	if args.Stanza != "" {
		cmdArgs = append(cmdArgs, "--stanza="+args.Stanza)
	}

	var out string
	var err error
	switch {
	case args.Target != "" && infraConfig != nil:
		var host resolvedHost
		host, err = resolveHost(args.Target)
		if err != nil {
			return GetPgBackRestStatusResult{}, err
		}
		switch {
		case host.Runtime == "docker" || host.Runtime == "podman" || host.K8sPodSelector != "":
			// pgBackRest checks PGDATA ownership; run as the postgres OS
			// user inside the container/pod rather than docker-exec's or
			// kubectl-exec's own default (root), matching how pgBackRest
			// expects to be invoked. shellCommand (sshexec.go) already
			// exists for safe single-string quoting — reused here rather
			// than writing a second quoter. execInProcess already knows how
			// to route a Kubernetes-resolved host through `kubectl exec`
			// (resolveHost leaves Runtime empty and K8sPodSelector set on
			// that path — see checkHostImpl's identical branch) — this is
			// the one targeted at a Postgres pod that is self-managed
			// (plain Deployment, pgBackRest installed in the image), not an
			// operator-managed one: CloudNativePG and similar operators
			// don't run pgBackRest inside their pods at all, so resolving a
			// CNPG-style target here would just fail with a real, honest
			// "pgbackrest: command not found" from inside that pod, not a
			// misleading silent-local-exec the way it used to.
			pgbrCmd := shellCommand("pgbackrest", cmdArgs, nil)
			out, err = execInProcess(ctx, host, []string{"su", "postgres", "-c", pgbrCmd})
		default:
			// SSH or bare local — runOnHost's own dispatch (sshexec.go)
			// decides between them. The configured SSHUser (for SSH) or the
			// agent process's own OS user (for local) is expected to already
			// have PGDATA-appropriate permissions; unlike the docker/podman
			// case above, there's no docker-exec default-user problem to
			// work around here.
			out, err = runOnHost(ctx, host, "pgbackrest", cmdArgs, nil)
		}
	case infraConfig != nil:
		// infraConfig is loaded (a real deployment with db_servers defined),
		// but no target was given — this is a resolution failure, not a
		// deliberate choice. Falling through to bare-local dispatch here
		// would silently run pgbackrest against the agent's own host
		// instead of any real target, which is never correct once infra
		// config exists at all. Confirmed live 2026-09-30: a model given a
		// connection_string that genuinely didn't match any known
		// infrastructure entry reasoned its way into omitting target rather
		// than escalating, producing a Go-level "executable not found"
		// error that read like "pgBackRest isn't installed" when the real
		// problem was "this agent process was never told about this
		// target." Refuse loudly instead of letting that ambiguity through.
		return GetPgBackRestStatusResult{}, fmt.Errorf(
			"get_pgbackrest_status: target is required (infrastructure config is loaded, so an empty target is a resolution failure, not a valid colocated-deployment request) — " +
				"if the given connection_string/server ID didn't match a known infrastructure entry, report that as its own finding and escalate rather than omitting target")
	default:
		// No infra config loaded at all — the only sensible reading is a
		// genuinely colocated deployment, where this agent process runs on
		// the same host as the pgBackRest repo it's checking.
		out, err = cmdRunner.Run(ctx, "pgbackrest", cmdArgs, nil)
	}
	if err != nil {
		return GetPgBackRestStatusResult{Output: out}, fmt.Errorf("get_pgbackrest_status: %w: %s", err, out)
	}

	maxAgeHours := args.MaxAgeHours
	if maxAgeHours <= 0 {
		maxAgeHours = defaultPgBackRestMaxAgeHours
	}
	result, parseErr := parsePgBackRestInfo(out, args.Stanza, time.Duration(maxAgeHours)*time.Hour)
	if parseErr != nil {
		return GetPgBackRestStatusResult{Output: out}, fmt.Errorf("get_pgbackrest_status: %w", parseErr)
	}
	result.Output = out

	if toolAuditor != nil {
		summary := []PgBackRestSummary{{
			Stanza:          result.Stanza,
			StatusCode:      result.StatusCode,
			BackupStale:     result.BackupStale,
			NeverBackedUp:   result.LastBackupTime == "",
			LastBackupLabel: result.LastBackupLabel,
		}}
		evidence.Evaluate(ctx, toolAuditor, pgbackrestEvidenceSchema, summary, pgbackrestEvidenceRules)
	}
	return result, nil
}

func getPgBackRestStatusTool(ctx agent.ToolContext, args GetPgBackRestStatusArgs) (GetPgBackRestStatusResult, error) {
	start := time.Now()
	result, err := getPgBackRestStatusImpl(ctx, args)
	duration := time.Since(start)
	if err == nil {
		slog.Info("tool ok", "name", "get_pgbackrest_status", "ms", duration.Milliseconds())
	}
	if toolAuditor != nil {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       "get_pgbackrest_status",
			Parameters: map[string]any{"target": args.Target, "stanza": args.Stanza},
		}, audit.ToolResult{
			Output: result.Output,
			Error:  errMsg,
		}, duration)
	}
	return result, err
}

// ── run_pgbackrest_backup ────────────────────────────────────────────────────

// RunPgBackRestBackupArgs defines arguments for the run_pgbackrest_backup tool.
type RunPgBackRestBackupArgs struct {
	Target string `json:"target,omitempty" jsonschema:"Server ID from infrastructure config. Required whenever infrastructure config is loaded — omitting it then is treated as a resolution failure and errors loudly, not a request to run locally. Only leave this empty when no infrastructure config exists at all (a genuinely colocated deployment). If your connection_string/server ID doesn't match a known infrastructure entry, do not guess or omit target — report that as its own finding and escalate."`
	Stanza string `json:"stanza,omitempty" jsonschema:"pgBackRest stanza name. When omitted, the only stanza present is used — an error if more than one exists."`
	Type   string `json:"type,omitempty" jsonschema:"Backup type: full, diff, or incr. Default full — always valid regardless of backup history, unlike diff/incr which require a prior full backup to reference."`
}

// RunPgBackRestBackupResult is the result of run_pgbackrest_backup.
type RunPgBackRestBackupResult struct {
	Output string `json:"output"`
}

// runPgBackRestBackupImpl is deliberately narrow: it exists ONLY to remediate
// pbs_pgbackrest_health_triage's Ending C (a stale backup on an otherwise
// healthy repo) by taking a fresh one — the same action a human would take,
// no value-guessing involved. It does not attempt to fix Ending B (a broken
// repo/stanza): permissions, ownership, and mount problems have no
// confirmed-correct value to restore (unlike set_archive_command's
// get_saved_snapshots pattern in agents/database/tools.go), so that case
// stays a human escalation by design — see
// pbs_pgbackrest_backup_remediate's own guidance for the explicit check
// that refuses to run this against a broken repo.
func runPgBackRestBackupImpl(ctx context.Context, args RunPgBackRestBackupArgs) (RunPgBackRestBackupResult, error) {
	backupType := args.Type
	if backupType == "" {
		backupType = "full"
	}

	// Unlike `pgbackrest info`, the `backup` subcommand has no
	// auto-detect-the-only-stanza behavior — it errors outright with
	// "backup command requires option: stanza" if omitted. Confirmed live
	// (2026-09-27) against a real single-stanza install. Resolve the same
	// way get_pgbackrest_status's own single-stanza fallback does, rather
	// than pushing that requirement onto every caller.
	stanza := args.Stanza
	if stanza == "" {
		status, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: args.Target})
		if err != nil {
			return RunPgBackRestBackupResult{}, fmt.Errorf("run_pgbackrest_backup: resolving stanza name: %w", err)
		}
		stanza = status.Stanza
	}
	cmdArgs := []string{"--stanza=" + stanza, "--type=" + backupType, "backup"}

	var out string
	var err error
	switch {
	case args.Target != "" && infraConfig != nil:
		var host resolvedHost
		host, err = resolveHost(args.Target)
		if err != nil {
			return RunPgBackRestBackupResult{}, err
		}
		if policyEnforcer != nil {
			policyCtx := agentutil.WithToolName(ctx, "run_pgbackrest_backup")
			if err := policyEnforcer.CheckTool(policyCtx, "host", args.Target,
				policy.ActionWrite, host.Tags, "run pgbackrest backup (stanza="+stanza+")", host.Sensitivity); err != nil {
				slog.Warn("policy denied pgbackrest backup", "target", args.Target, "err", err)
				return RunPgBackRestBackupResult{}, err
			}
		}
		switch {
		case host.Runtime == "docker" || host.Runtime == "podman" || host.K8sPodSelector != "":
			pgbrCmd := shellCommand("pgbackrest", cmdArgs, nil)
			out, err = execInProcess(ctx, host, []string{"su", "postgres", "-c", pgbrCmd})
		default:
			out, err = runOnHost(ctx, host, "pgbackrest", cmdArgs, nil)
		}
	case infraConfig != nil:
		// Same reasoning as get_pgbackrest_status's own identical branch —
		// infra config is loaded, so an empty target is a resolution
		// failure, not a valid colocated-deployment request. Refuse rather
		// than silently running a real backup command against the agent's
		// own host.
		return RunPgBackRestBackupResult{}, fmt.Errorf(
			"run_pgbackrest_backup: target is required (infrastructure config is loaded, so an empty target is a resolution failure, not a valid colocated-deployment request) — " +
				"if the given connection_string/server ID didn't match a known infrastructure entry, report that as its own finding and escalate rather than omitting target")
	default:
		out, err = cmdRunner.Run(ctx, "pgbackrest", cmdArgs, nil)
	}
	if err != nil {
		return RunPgBackRestBackupResult{Output: out}, fmt.Errorf("run_pgbackrest_backup: %w: %s", err, out)
	}
	return RunPgBackRestBackupResult{Output: out}, nil
}

func runPgBackRestBackupTool(ctx agent.ToolContext, args RunPgBackRestBackupArgs) (RunPgBackRestBackupResult, error) {
	start := time.Now()
	result, err := runPgBackRestBackupImpl(ctx, args)
	duration := time.Since(start)
	if err == nil {
		slog.Info("tool ok", "name", "run_pgbackrest_backup", "ms", duration.Milliseconds())
	}
	if toolAuditor != nil {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		toolAuditor.RecordToolCall(ctx, audit.ToolCall{
			Name:       "run_pgbackrest_backup",
			Parameters: map[string]any{"target": args.Target, "stanza": args.Stanza, "type": args.Type},
		}, audit.ToolResult{
			Output: result.Output,
			Error:  errMsg,
		}, duration)
	}
	return result, err
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

	r.Register("get_pgbackrest_status", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[GetPgBackRestStatusArgs](args)
		if err != nil {
			return "", err
		}
		result, err := getPgBackRestStatusImpl(ctx, a)
		if err != nil {
			return "", err
		}
		return marshalResult(result)
	})

	r.Register("run_pgbackrest_backup", func(ctx context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[RunPgBackRestBackupArgs](args)
		if err != nil {
			return "", err
		}
		result, err := runPgBackRestBackupImpl(ctx, a)
		if err != nil {
			return "", err
		}
		return marshalResult(result)
	})

	// register_infra_db is an admin-only tool: not exposed to the LLM but callable
	// via POST /tool/register_infra_db so faulttest can register ephemeral containers.
	r.Register("register_infra_db", func(_ context.Context, args map[string]any) (string, error) {
		a, err := argsToStruct[RegisterInfraDBArgs](args)
		if err != nil {
			return "", err
		}
		if err := registerInfraDB(a); err != nil {
			return "", err
		}
		return `{"ok":true}`, nil
	})

	return r
}
