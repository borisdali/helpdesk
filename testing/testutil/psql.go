package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RunSQL executes a SQL script file against the database using psql.
func RunSQL(ctx context.Context, connStr, scriptPath string) error {
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("reading SQL script %s: %v", scriptPath, err)
	}
	return RunSQLString(ctx, connStr, string(script))
}

// RunSQLString executes a SQL string against the database using psql.
func RunSQLString(ctx context.Context, connStr, sql string) error {
	cmd := exec.CommandContext(ctx, "psql", connStr, "-c", sql)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql: %v\n%s", err, output)
	}
	return nil
}

// RunSQLViaPgloader executes a SQL script file inside the pgloader container
// using docker exec. This is used for persistent-connection scripts (e.g.,
// dblink-based connection flooding) that need to run inside a container with
// a persistent connection to the database.
func RunSQLViaPgloader(ctx context.Context, scriptPath string) error {
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("reading SQL script %s: %v", scriptPath, err)
	}
	return RunSQLStringViaPgloader(ctx, string(script))
}

// RunSQLStringViaPgloader executes a SQL string inside the pgloader container.
func RunSQLStringViaPgloader(ctx context.Context, sql string) error {
	args := []string{
		"exec", "-i", "helpdesk-test-pgloader",
		"psql", "-h", "postgres", "-U", "postgres", "-d", "testdb",
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = strings.NewReader(sql)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pgloader psql: %v\n%s", err, output)
	}
	return nil
}
