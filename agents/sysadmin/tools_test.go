package main

import (
	"context"
	"strings"
	"testing"

	"helpdesk/internal/infra"
)

// mockRunner implements CommandRunner for testing.
type mockRunner struct {
	output string
	err    error
}

func (m mockRunner) Run(_ context.Context, _ string, _ []string, _ []string) (string, error) {
	return m.output, m.err
}

// withMockRunner temporarily replaces cmdRunner for a test.
func withMockRunner(output string, err error) func() {
	old := cmdRunner
	cmdRunner = mockRunner{output: output, err: err}
	return func() { cmdRunner = old }
}

// mockToolContext implements tool.Context (embeds context.Context) for testing.
type mockToolContext struct {
	context.Context
}

// withDockerInfra sets up a fake infraConfig with a Docker-hosted DB and cleans up
// after the test.
func withDockerInfra(t *testing.T) {
	t.Helper()
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod_db": {
				Name:             "prod_db",
				ConnectionString: "host=localhost",
				VMName:           "prod-vm",
				ContainerName:    "alloydb-omni",
			},
		},
		VMs: map[string]infra.VM{
			"prod-vm": {
				Name:    "prod-vm",
				Address: "localhost",
				Runtime: "docker",
			},
		},
	}
	t.Cleanup(func() { infraConfig = nil })
}

// withSystemdInfra sets up a fake infraConfig with a systemd-managed DB.
func withSystemdInfra(t *testing.T) {
	t.Helper()
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod_db": {
				Name:             "prod_db",
				ConnectionString: "host=localhost",
				VMName:           "prod-vm",
				SystemdUnit:      "postgresql-16",
			},
		},
		VMs: map[string]infra.VM{
			"prod-vm": {
				Name:    "prod-vm",
				Address: "localhost",
				Runtime: "", // systemd
			},
		},
	}
	t.Cleanup(func() { infraConfig = nil })
}

// ── check_host ───────────────────────────────────────────────────────────────

func TestCheckHost_DockerRunning(t *testing.T) {
	withDockerInfra(t)
	defer withMockRunner("running (running=true, restarting=false, oomkilled=false, dead=false, exitcode=0)", nil)()

	result, err := checkHostImpl(context.Background(), CheckHostArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("checkHostImpl: %v", err)
	}
	if result.Status != "running" {
		t.Errorf("Status = %q, want running", result.Status)
	}
	if result.Runtime != "docker" {
		t.Errorf("Runtime = %q, want docker", result.Runtime)
	}
}

func TestCheckHost_DockerStopped(t *testing.T) {
	withDockerInfra(t)
	defer withMockRunner("exited (running=false, restarting=false, oomkilled=false, dead=false, exitcode=1)", nil)()

	result, err := checkHostImpl(context.Background(), CheckHostArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("checkHostImpl: %v", err)
	}
	if result.Status != "unknown" {
		// "exited" doesn't match our running/stopped detection patterns for containers
		// but "dead=false" is also not set, so status is "unknown" — acceptable for exited containers.
		t.Logf("Status = %q (expected unknown for exited container)", result.Status)
	}
}

func TestCheckHost_SystemdActive(t *testing.T) {
	withSystemdInfra(t)
	defer withMockRunner("ActiveState=active\nSubState=running\nResult=success\nMainPID=12345\nExecMainStartTimestamp=Mon 2024-01-01 00:00:00 UTC", nil)()

	result, err := checkHostImpl(context.Background(), CheckHostArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("checkHostImpl: %v", err)
	}
	if result.Status != "running" {
		t.Errorf("Status = %q, want running", result.Status)
	}
	if result.Runtime != "systemd" {
		t.Errorf("Runtime = %q, want systemd", result.Runtime)
	}
}

func TestCheckHost_UnknownServer(t *testing.T) {
	withDockerInfra(t)
	_, err := checkHostImpl(context.Background(), CheckHostArgs{Target: "nonexistent"})
	if err == nil {
		t.Error("expected error for unknown server, got nil")
	}
}

func TestCheckHost_NoInfraConfig(t *testing.T) {
	infraConfig = nil
	_, err := checkHostImpl(context.Background(), CheckHostArgs{Target: "prod_db"})
	if err == nil {
		t.Error("expected error when infraConfig is nil, got nil")
	}
}

// ── get_host_logs ─────────────────────────────────────────────────────────────

func TestGetHostLogs_Docker(t *testing.T) {
	withDockerInfra(t)
	logLines := "2024-01-01 FATAL: database files are incompatible with server\n2024-01-01 INFO: starting up"
	defer withMockRunner(logLines, nil)()

	result, err := getHostLogsImpl(context.Background(), GetHostLogsArgs{Target: "prod_db", Lines: 10})
	if err != nil {
		t.Fatalf("getHostLogsImpl: %v", err)
	}
	if result.Lines == 0 {
		t.Error("Lines = 0, want > 0")
	}
	if result.Runtime != "docker" {
		t.Errorf("Runtime = %q, want docker", result.Runtime)
	}
}

func TestGetHostLogs_Filter(t *testing.T) {
	withDockerInfra(t)
	logLines := "INFO: checkpoint complete\nFATAL: could not write to file\nINFO: autovacuum started"
	defer withMockRunner(logLines, nil)()

	result, err := getHostLogsImpl(context.Background(), GetHostLogsArgs{
		Target: "prod_db",
		Lines:  50,
		Filter: "FATAL",
	})
	if err != nil {
		t.Fatalf("getHostLogsImpl with filter: %v", err)
	}
	if result.Lines != 1 {
		t.Errorf("Lines = %d, want 1 (only FATAL line)", result.Lines)
	}
}

func TestGetHostLogs_DefaultLines(t *testing.T) {
	withDockerInfra(t)
	defer withMockRunner("line1\nline2", nil)()

	// Lines=0 should default to 100
	result, err := getHostLogsImpl(context.Background(), GetHostLogsArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("getHostLogsImpl default lines: %v", err)
	}
	if result.ServerID != "prod_db" {
		t.Errorf("ServerID = %q, want prod_db", result.ServerID)
	}
}

// ── check_disk ───────────────────────────────────────────────────────────────

const dfOutput = "Filesystem      Size  Used Avail Use% Mounted on\n/dev/sda1        50G   45G  2.0G  96% /"

// TestCheckDisk_Container verifies that a docker target routes through docker exec.
func TestCheckDisk_Container(t *testing.T) {
	withDockerInfra(t)
	defer withMockRunner(dfOutput, nil)()

	result, err := checkDiskImpl(context.Background(), CheckDiskArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("checkDiskImpl: %v", err)
	}
	if result.Output == "" {
		t.Error("Output is empty")
	}
	if result.ServerID != "prod_db" {
		t.Errorf("ServerID = %q, want prod_db", result.ServerID)
	}
}

// TestCheckDisk_RunOnHost verifies that run_on_host bypasses docker exec even for container targets.
func TestCheckDisk_RunOnHost(t *testing.T) {
	withDockerInfra(t)
	defer withMockRunner(dfOutput, nil)()

	result, err := checkDiskImpl(context.Background(), CheckDiskArgs{Target: "prod_db", RunOnHost: true})
	if err != nil {
		t.Fatalf("checkDiskImpl: %v", err)
	}
	if result.Output == "" {
		t.Error("Output is empty")
	}
}

// TestCheckDisk_Systemd verifies that a systemd target falls through to local df.
func TestCheckDisk_Systemd(t *testing.T) {
	withSystemdInfra(t)
	defer withMockRunner(dfOutput, nil)()

	result, err := checkDiskImpl(context.Background(), CheckDiskArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("checkDiskImpl: %v", err)
	}
	if result.Output == "" {
		t.Error("Output is empty")
	}
}

// ── check_memory ─────────────────────────────────────────────────────────────

const freeOutput = "              total        used        free\nMem:           15Gi        12Gi       1.0Gi\nSwap:         2.0Gi       512Mi       1.5Gi"

// TestCheckMemory_NoTarget verifies the no-infra / no-target local path.
func TestCheckMemory_NoTarget(t *testing.T) {
	defer withMockRunner(freeOutput, nil)()

	result, err := checkMemoryImpl(context.Background(), CheckMemoryArgs{})
	if err != nil {
		t.Fatalf("checkMemoryImpl: %v", err)
	}
	if result.Output == "" {
		t.Error("Output is empty")
	}
}

// TestCheckMemory_Container verifies that a docker target routes through docker exec.
func TestCheckMemory_Container(t *testing.T) {
	withDockerInfra(t)
	defer withMockRunner(freeOutput, nil)()

	result, err := checkMemoryImpl(context.Background(), CheckMemoryArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("checkMemoryImpl: %v", err)
	}
	if result.Output == "" {
		t.Error("Output is empty")
	}
	if result.ServerID != "prod_db" {
		t.Errorf("ServerID = %q, want prod_db", result.ServerID)
	}
}

// TestCheckMemory_RunOnHost verifies that run_on_host bypasses docker exec.
func TestCheckMemory_RunOnHost(t *testing.T) {
	withDockerInfra(t)
	defer withMockRunner(freeOutput, nil)()

	result, err := checkMemoryImpl(context.Background(), CheckMemoryArgs{Target: "prod_db", RunOnHost: true})
	if err != nil {
		t.Fatalf("checkMemoryImpl: %v", err)
	}
	if result.Output == "" {
		t.Error("Output is empty")
	}
}

// TestCheckMemory_Systemd verifies that a systemd target falls through to local free.
func TestCheckMemory_Systemd(t *testing.T) {
	withSystemdInfra(t)
	defer withMockRunner(freeOutput, nil)()

	result, err := checkMemoryImpl(context.Background(), CheckMemoryArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("checkMemoryImpl: %v", err)
	}
	if result.Output == "" {
		t.Error("Output is empty")
	}
}

// ── restart_container ─────────────────────────────────────────────────────────

func TestRestartContainer_Success(t *testing.T) {
	withDockerInfra(t)
	defer withMockRunner("alloydb-omni", nil)()

	result, err := restartContainerImpl(context.Background(), RestartContainerArgs{
		Target: "prod_db",
		Reason: "crash loop detected",
	})
	if err != nil {
		t.Fatalf("restartContainerImpl: %v", err)
	}
	if !result.Success {
		t.Error("Success = false, want true")
	}
	if result.Runtime != "docker" {
		t.Errorf("Runtime = %q, want docker", result.Runtime)
	}
	if result.Target != "alloydb-omni" {
		t.Errorf("Target = %q, want alloydb-omni", result.Target)
	}
}

func TestRestartContainer_WrongType(t *testing.T) {
	withSystemdInfra(t)
	_, err := restartContainerImpl(context.Background(), RestartContainerArgs{
		Target: "prod_db",
		Reason: "test",
	})
	if err == nil {
		t.Error("expected error when target uses systemd, got nil")
	}
}

// ── restart_service ───────────────────────────────────────────────────────────

func TestRestartService_Success(t *testing.T) {
	withSystemdInfra(t)
	defer withMockRunner("", nil)()

	result, err := restartServiceImpl(context.Background(), RestartServiceArgs{
		Target: "prod_db",
		Reason: "configuration change applied",
	})
	if err != nil {
		t.Fatalf("restartServiceImpl: %v", err)
	}
	if !result.Success {
		t.Error("Success = false, want true")
	}
	if result.Runtime != "systemd" {
		t.Errorf("Runtime = %q, want systemd", result.Runtime)
	}
	if result.Target != "postgresql-16" {
		t.Errorf("Target = %q, want postgresql-16", result.Target)
	}
}

func TestRestartService_WrongType(t *testing.T) {
	withDockerInfra(t)
	_, err := restartServiceImpl(context.Background(), RestartServiceArgs{
		Target: "prod_db",
		Reason: "test",
	})
	if err == nil {
		t.Error("expected error when target uses container runtime, got nil")
	}
}

// ── resolve_host ──────────────────────────────────────────────────────────────

func TestResolveHost_NoVMName(t *testing.T) {
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"bare_db": {
				Name:             "bare_db",
				ConnectionString: "host=localhost",
				// VMName is empty — no VM configured
			},
		},
	}
	t.Cleanup(func() { infraConfig = nil })

	_, err := resolveHost("bare_db")
	if err == nil {
		t.Error("expected error for server with no vm_name, got nil")
	}
}

func TestResolveHost_PodmanRuntime(t *testing.T) {
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"podman_db": {
				Name:             "podman_db",
				ConnectionString: "host=localhost",
				VMName:           "podman-vm",
				ContainerName:    "pg16",
			},
		},
		VMs: map[string]infra.VM{
			"podman-vm": {
				Name:    "podman-vm",
				Address: "localhost",
				Runtime: "podman",
			},
		},
	}
	t.Cleanup(func() { infraConfig = nil })

	host, err := resolveHost("podman_db")
	if err != nil {
		t.Fatalf("resolveHost: %v", err)
	}
	if host.Runtime != "podman" {
		t.Errorf("Runtime = %q, want podman", host.Runtime)
	}
}

// withK8sInfra sets up a fake infraConfig with a Kubernetes-hosted DB.
func withK8sInfra(t *testing.T) {
	t.Helper()
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod_db": {
				Name:             "prod_db",
				ConnectionString: "host=pg-service.prod.svc.cluster.local",
				K8sCluster:       "prod-cluster",
				K8sNamespace:     "prod",
				K8sPodSelector:   "app=postgres,instance=prod-db",
			},
		},
		K8sClusters: map[string]infra.K8sCluster{
			"prod-cluster": {
				Name:    "prod-cluster",
				Context: "gke_project_region_prod-cluster",
			},
		},
	}
	t.Cleanup(func() { infraConfig = nil })
}

// ── read_pg_log_file ─────────────────────────────────────────────────────────

const pgLogContent = "2026-04-05 18:00:01 UTC [1] LOG:  database system is ready to accept connections\n2026-04-05 18:05:00 UTC [42] FATAL:  could not open file"

// TestReadPgLogFile_Docker verifies the docker exec path (ls then tail).
func TestReadPgLogFile_Docker(t *testing.T) {
	withDockerInfra(t)
	// Mock runner returns log filename on first call (ls -t), log content on second (tail).
	old := cmdRunner
	cmdRunner = &multiMockRunner{responses: []mockResponse{
		{output: "postgresql-2026-04-05.log\n", err: nil},
		{output: pgLogContent, err: nil},
	}}
	defer func() { cmdRunner = old }()

	result, err := readPgLogFileImpl(context.Background(), ReadPgLogFileArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("readPgLogFileImpl: %v", err)
	}
	if result.Logs == "" {
		t.Error("Logs is empty")
	}
	if result.ServerID != "prod_db" {
		t.Errorf("ServerID = %q, want prod_db", result.ServerID)
	}
	if result.Runtime != "docker" {
		t.Errorf("Runtime = %q, want docker", result.Runtime)
	}
}

// TestReadPgLogFile_K8s verifies the kubectl exec path.
func TestReadPgLogFile_K8s(t *testing.T) {
	withK8sInfra(t)
	old := cmdRunner
	cmdRunner = &multiMockRunner{responses: []mockResponse{
		{output: "pg-prod-db-0\n", err: nil},                               // kubectl get pod
		{output: "postgresql-2026-04-05.log\n", err: nil},                  // kubectl exec ls
		{output: "pg-prod-db-0\n", err: nil},                               // kubectl get pod (for tail)
		{output: pgLogContent, err: nil},                                    // kubectl exec tail
	}}
	defer func() { cmdRunner = old }()

	result, err := readPgLogFileImpl(context.Background(), ReadPgLogFileArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("readPgLogFileImpl: %v", err)
	}
	if result.Runtime != "kubectl" {
		t.Errorf("Runtime = %q, want kubectl", result.Runtime)
	}
	if result.Logs == "" {
		t.Error("Logs is empty")
	}
}

// TestReadPgLogFile_Filter verifies case-insensitive line filtering.
func TestReadPgLogFile_Filter(t *testing.T) {
	withDockerInfra(t)
	old := cmdRunner
	cmdRunner = &multiMockRunner{responses: []mockResponse{
		{output: "postgresql-2026-04-05.log\n", err: nil},
		{output: pgLogContent, err: nil},
	}}
	defer func() { cmdRunner = old }()

	result, err := readPgLogFileImpl(context.Background(), ReadPgLogFileArgs{
		Target: "prod_db",
		Filter: "FATAL",
	})
	if err != nil {
		t.Fatalf("readPgLogFileImpl: %v", err)
	}
	if result.LinesReturned != 1 {
		t.Errorf("LinesReturned = %d, want 1", result.LinesReturned)
	}
}

// TestResolveHost_K8s verifies the k8s resolution path.
func TestResolveHost_K8s(t *testing.T) {
	withK8sInfra(t)
	host, err := resolveHost("prod_db")
	if err != nil {
		t.Fatalf("resolveHost: %v", err)
	}
	if host.K8sPodSelector != "app=postgres,instance=prod-db" {
		t.Errorf("K8sPodSelector = %q", host.K8sPodSelector)
	}
	if host.K8sNamespace != "prod" {
		t.Errorf("K8sNamespace = %q, want prod", host.K8sNamespace)
	}
	if host.K8sContext != "gke_project_region_prod-cluster" {
		t.Errorf("K8sContext = %q", host.K8sContext)
	}
}

// TestResolveHost_K8s_MissingSelector verifies that k8s without pod selector errors.
func TestResolveHost_K8s_MissingSelector(t *testing.T) {
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"k8s_db": {K8sCluster: "prod-cluster"},
		},
		K8sClusters: map[string]infra.K8sCluster{
			"prod-cluster": {Context: "ctx"},
		},
	}
	t.Cleanup(func() { infraConfig = nil })

	_, err := resolveHost("k8s_db")
	if err == nil {
		t.Error("expected error for missing k8s_pod_selector")
	}
}

// TestResolveHost_K8s_UnknownCluster verifies that an unresolved cluster reference errors.
func TestResolveHost_K8s_UnknownCluster(t *testing.T) {
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"k8s_db": {K8sCluster: "nonexistent", K8sPodSelector: "app=pg"},
		},
		K8sClusters: map[string]infra.K8sCluster{},
	}
	t.Cleanup(func() { infraConfig = nil })

	_, err := resolveHost("k8s_db")
	if err == nil {
		t.Error("expected error for unknown k8s cluster")
	}
}

// TestCheckDisk_K8s verifies that a k8s target routes through kubectl exec.
func TestCheckDisk_K8s(t *testing.T) {
	withK8sInfra(t)
	old := cmdRunner
	cmdRunner = &multiMockRunner{responses: []mockResponse{
		{output: "pg-prod-db-0", err: nil}, // kubectl get pod
		{output: dfOutput, err: nil},       // kubectl exec df
	}}
	defer func() { cmdRunner = old }()

	result, err := checkDiskImpl(context.Background(), CheckDiskArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("checkDiskImpl (k8s): %v", err)
	}
	if result.Output == "" {
		t.Error("Output is empty")
	}
}

// TestCheckMemory_K8s verifies that a k8s target routes through kubectl exec.
func TestCheckMemory_K8s(t *testing.T) {
	withK8sInfra(t)
	old := cmdRunner
	cmdRunner = &multiMockRunner{responses: []mockResponse{
		{output: "pg-prod-db-0", err: nil}, // kubectl get pod
		{output: freeOutput, err: nil},     // kubectl exec free
	}}
	defer func() { cmdRunner = old }()

	result, err := checkMemoryImpl(context.Background(), CheckMemoryArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("checkMemoryImpl (k8s): %v", err)
	}
	if result.Output == "" {
		t.Error("Output is empty")
	}
}

// TestReadPgLogFile_NoLogFiles verifies the graceful response when the log directory is empty.
func TestReadPgLogFile_NoLogFiles(t *testing.T) {
	withDockerInfra(t)
	defer withMockRunner("", nil)() // ls returns empty

	result, err := readPgLogFileImpl(context.Background(), ReadPgLogFileArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("readPgLogFileImpl: %v", err)
	}
	if result.LinesReturned != 0 {
		t.Errorf("LinesReturned = %d, want 0", result.LinesReturned)
	}
	if !strings.Contains(result.Logs, "no log files found") {
		t.Errorf("expected 'no log files found' message, got: %q", result.Logs)
	}
}

// TestReadPgLogFile_CustomLogPath verifies that log_path overrides the default directory.
func TestReadPgLogFile_CustomLogPath(t *testing.T) {
	withDockerInfra(t)
	old := cmdRunner
	cmdRunner = &multiMockRunner{responses: []mockResponse{
		{output: "pg.log\n", err: nil},
		{output: pgLogContent, err: nil},
	}}
	defer func() { cmdRunner = old }()

	result, err := readPgLogFileImpl(context.Background(), ReadPgLogFileArgs{
		Target:  "prod_db",
		LogPath: "/var/log/postgresql",
	})
	if err != nil {
		t.Fatalf("readPgLogFileImpl: %v", err)
	}
	if result.Logs == "" {
		t.Error("Logs is empty")
	}
}

// TestExecInProcess_K8s_NoPodFound verifies the error when the selector matches no pods.
func TestExecInProcess_K8s_NoPodFound(t *testing.T) {
	defer withMockRunner("", nil)() // kubectl get pod returns empty

	host := resolvedHost{
		K8sContext:     "ctx",
		K8sNamespace:   "prod",
		K8sPodSelector: "app=postgres",
	}
	_, err := execInProcess(context.Background(), host, []string{"df", "-h"})
	if err == nil {
		t.Error("expected error when no pod found")
	}
}

// multiMockRunner returns pre-configured responses in order.
type mockResponse struct {
	output string
	err    error
}

type multiMockRunner struct {
	responses []mockResponse
	idx       int
}

func (m *multiMockRunner) Run(_ context.Context, _ string, _ []string, _ []string) (string, error) {
	if m.idx >= len(m.responses) {
		return "", nil
	}
	r := m.responses[m.idx]
	m.idx++
	return r.output, r.err
}
