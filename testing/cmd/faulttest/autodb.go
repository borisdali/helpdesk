package main

import (
	"context"
	"fmt"
	"math/rand/v2"
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

	args := []string{
		"run", "-d",
		"-p", "127.0.0.1::5432",
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

	port, err := resolveAutoDBPort(ctx, name)
	if err != nil {
		remove()
		return "", "", nil, err
	}

	dsn := fmt.Sprintf("host=127.0.0.1 port=%s dbname=%s user=postgres password=%s sslmode=disable", port, dbname, password)

	if err := waitForAutoDBReady(ctx, dsn); err != nil {
		remove()
		return "", "", nil, fmt.Errorf("postgres not ready: %w", err)
	}

	return dsn, name, remove, nil
}

func resolveAutoDBPort(ctx context.Context, name string) (string, error) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.CommandContext(ctx, "docker", "port", name, "5432").Output()
		if err == nil {
			// Output: "0.0.0.0:54321\n" or "127.0.0.1:54321\n"
			line := strings.TrimSpace(string(out))
			if idx := strings.LastIndex(line, ":"); idx >= 0 {
				return line[idx+1:], nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", fmt.Errorf("could not resolve mapped port for container %s", name)
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
