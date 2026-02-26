package main

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestCollectDatabaseLayer_Success(t *testing.T) {
	orig := runPsql
	defer func() { runPsql = orig }()

	runPsql = func(ctx context.Context, connStr, query string) (string, error) {
		return "mock output for: " + query[:20], nil
	}

	files, errs := collectDatabaseLayer(context.Background(), "host=test")

	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	// Expect 9 files: version.txt, databases.txt, active_connections.txt,
	// connection_stats.txt, database_stats.txt, config_params.txt,
	// replication_status.txt, locks.txt, table_stats.txt
	if len(files) != 9 {
		t.Errorf("expected 9 files, got %d: %v", len(files), mapKeys(files))
	}

	expectedFiles := []string{
		"version.txt", "databases.txt", "active_connections.txt",
		"connection_stats.txt", "database_stats.txt", "config_params.txt",
		"replication_status.txt", "locks.txt", "table_stats.txt",
	}
	for _, f := range expectedFiles {
		if _, ok := files[f]; !ok {
			t.Errorf("missing expected file: %s", f)
		}
	}
}

func TestCollectDatabaseLayer_PartialFailure(t *testing.T) {
	orig := runPsql
	defer func() { runPsql = orig }()

	callCount := 0
	runPsql = func(ctx context.Context, connStr, query string) (string, error) {
		callCount++
		if callCount == 3 {
			return "", fmt.Errorf("connection timeout")
		}
		return "mock output", nil
	}

	files, errs := collectDatabaseLayer(context.Background(), "host=test")

	if len(errs) != 1 {
		t.Errorf("expected 1 error, got %d: %v", len(errs), errs)
	}
	// Should still have 9 files (8 successful + 1 with ERROR prefix).
	if len(files) != 9 {
		t.Errorf("expected 9 files (including error), got %d", len(files))
	}

	// Check that the error file has ERROR prefix.
	foundError := false
	for _, content := range files {
		if strings.HasPrefix(content, "ERROR:") {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Error("expected one file to have ERROR: prefix")
	}
}

func TestCollectKubernetesLayer_Success(t *testing.T) {
	orig := runKubectl
	defer func() { runKubectl = orig }()

	runKubectl = func(ctx context.Context, kubeContext string, args ...string) (string, error) {
		return "mock kubectl output", nil
	}

	files, errs := collectKubernetesLayer(context.Background(), "my-context", "default")

	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	// Expect 8 files: pods.txt, pods_all.txt, services.txt, endpoints.txt,
	// events.txt, nodes.txt, top_nodes.txt, top_pods.txt
	if len(files) != 8 {
		t.Errorf("expected 8 files, got %d: %v", len(files), mapKeys(files))
	}

	expectedFiles := []string{
		"pods.txt", "pods_all.txt", "services.txt", "endpoints.txt",
		"events.txt", "nodes.txt", "top_nodes.txt", "top_pods.txt",
	}
	for _, f := range expectedFiles {
		if _, ok := files[f]; !ok {
			t.Errorf("missing expected file: %s", f)
		}
	}
}

func TestCollectKubernetesLayer_AllFail(t *testing.T) {
	orig := runKubectl
	defer func() { runKubectl = orig }()

	runKubectl = func(ctx context.Context, kubeContext string, args ...string) (string, error) {
		return "", fmt.Errorf("cluster unreachable")
	}

	files, errs := collectKubernetesLayer(context.Background(), "bad-context", "default")

	if len(errs) != 8 {
		t.Errorf("expected 8 errors, got %d: %v", len(errs), errs)
	}
	// All files should have ERROR prefix.
	for name, content := range files {
		if !strings.HasPrefix(content, "ERROR:") {
			t.Errorf("file %s should have ERROR: prefix, got: %s", name, content[:min(50, len(content))])
		}
	}
}

func TestCollectOSLayer_Success(t *testing.T) {
	orig := runCommand
	defer func() { runCommand = orig }()

	runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		return "mock " + name + " output", nil
	}

	files, errs := collectOSLayer(context.Background())

	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	// macOS omits free, vmstat, dmesg (not available / require sudo).
	// Linux includes all 9: uname, uptime, hostname, top, ps, free, vmstat, dmesg, sysctl.
	wantFiles := 9
	if runtime.GOOS == "darwin" {
		wantFiles = 7 // uname, uptime, hostname, top, ps, vm_stat, sysctl
	}
	if len(files) != wantFiles {
		t.Errorf("expected %d files, got %d: %v", wantFiles, len(files), mapKeys(files))
	}
}

func TestCollectStorageLayer_Success(t *testing.T) {
	orig := runCommand
	defer func() { runCommand = orig }()

	runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		return "mock storage output", nil
	}

	files, errs := collectStorageLayer(context.Background())

	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	// Both platforms produce 5 files; the disk-inventory file differs:
	//   Linux: lsblk.txt   macOS: diskutil.txt
	if len(files) != 5 {
		t.Errorf("expected 5 files, got %d: %v", len(files), mapKeys(files))
	}

	diskFile := "lsblk.txt"
	if runtime.GOOS == "darwin" {
		diskFile = "diskutil.txt"
	}
	expectedFiles := []string{"df.txt", "df_inodes.txt", "mount.txt", diskFile, "iostat.txt"}
	for _, f := range expectedFiles {
		if _, ok := files[f]; !ok {
			t.Errorf("missing expected file: %s", f)
		}
	}
}

func TestCollectOSLayer_CommandNotFound(t *testing.T) {
	orig := runCommand
	defer func() { runCommand = orig }()

	// sysctl is present in both the Linux and macOS command lists; use it as
	// the command that fails so the test is platform-independent.
	// (dmesg is Linux-only and absent from the macOS branch.)
	runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "sysctl" {
			return "", fmt.Errorf("permission denied")
		}
		return "output", nil
	}

	files, errs := collectOSLayer(context.Background())

	if len(errs) != 1 {
		t.Errorf("expected 1 error, got %d: %v", len(errs), errs)
	}
	// sysctl.txt should exist with an ERROR prefix.
	if content, ok := files["sysctl.txt"]; !ok {
		t.Error("sysctl.txt should exist")
	} else if !strings.HasPrefix(content, "ERROR:") {
		t.Errorf("sysctl.txt should have ERROR: prefix, got: %s", content)
	}
}

func TestCollectDatabaseLayer_ConnStrPassedThrough(t *testing.T) {
	orig := runPsql
	defer func() { runPsql = orig }()

	var receivedConnStr string
	runPsql = func(ctx context.Context, connStr, query string) (string, error) {
		receivedConnStr = connStr
		return "output", nil
	}

	connStr := "host=db.example.com port=5432 dbname=prod user=test"
	collectDatabaseLayer(context.Background(), connStr)

	if receivedConnStr != connStr {
		t.Errorf("connStr not passed through: got %q, want %q", receivedConnStr, connStr)
	}
}

func TestCollectKubernetesLayer_ContextAndNamespacePassedThrough(t *testing.T) {
	orig := runKubectl
	defer func() { runKubectl = orig }()

	var receivedContext string
	var receivedArgs []string
	runKubectl = func(ctx context.Context, kubeContext string, args ...string) (string, error) {
		receivedContext = kubeContext
		receivedArgs = args
		return "output", nil
	}

	collectKubernetesLayer(context.Background(), "gke_prod", "my-namespace")

	if receivedContext != "gke_prod" {
		t.Errorf("kubeContext not passed through: got %q, want %q", receivedContext, "gke_prod")
	}
	// Check that namespace is included in args.
	foundNamespace := false
	for i, arg := range receivedArgs {
		if arg == "-n" && i+1 < len(receivedArgs) && receivedArgs[i+1] == "my-namespace" {
			foundNamespace = true
			break
		}
	}
	if !foundNamespace {
		t.Errorf("namespace not found in args: %v", receivedArgs)
	}
}

// mapKeys returns the keys of a map for error messages.
func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
