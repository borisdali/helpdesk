package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"os/exec"
	"strings"
	"time"
)

const autoDBImage = "postgres:16-alpine"

// startAutoDBContainer spins up a temporary Docker PostgreSQL container,
// waits for it to accept connections, and returns the libpq connection string,
// the container name, and a teardown function. The container is removed on teardown.
func startAutoDBContainer(ctx context.Context) (connStr, containerName string, teardown func(), err error) {
	name := fmt.Sprintf("faulttest-auto-db-%08x", rand.Uint32())
	password := "faulttest"
	dbname := "faulttest"

	// Pre-allocate a fixed host port. On macOS Docker Desktop, containers with
	// random port mappings (-p 127.0.0.1::5432) are assigned a NEW random port
	// every time they are started or restarted — so the connection string becomes
	// stale after the first stop/restart. Using a fixed port (-p HOST:5432) keeps
	// the mapping stable across restarts, which is required for pollRecovery to
	// reconnect after restart_container runs.
	port, err := freePort()
	if err != nil {
		return "", "", nil, fmt.Errorf("could not allocate free port: %w", err)
	}

	args := []string{
		"run", "-d",
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", port),
		"-e", "POSTGRES_PASSWORD=" + password,
		"-e", "POSTGRES_DB=" + dbname,
		"-e", "POSTGRES_USER=postgres",
		"--name", name,
		autoDBImage,
	}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return "", "", nil, fmt.Errorf("docker run failed: %w\n%s", err, out)
	}

	remove := func() {
		exec.Command("docker", "rm", "-f", name).Run() //nolint:errcheck
	}

	dsn := fmt.Sprintf("host=127.0.0.1 port=%d dbname=%s user=postgres password=%s sslmode=disable", port, dbname, password)

	if err := waitForAutoDBReady(ctx, dsn); err != nil {
		remove()
		return "", "", nil, fmt.Errorf("postgres not ready: %w", err)
	}

	return dsn, name, remove, nil
}

// freePort finds a free TCP port on 127.0.0.1 by binding and immediately
// releasing it. There is a small TOCTOU window between release and the docker
// run, but for test use this is acceptable.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

func waitForAutoDBReady(ctx context.Context, dsn string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.CommandContext(ctx, "psql", dsn, "-c", "SELECT 1").CombinedOutput()
		if err == nil && strings.Contains(string(out), "1") {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for postgres to be ready")
}
