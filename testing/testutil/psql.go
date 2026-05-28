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

// RunSQLBool executes a SQL query that returns a single boolean row and returns
// nil only when the result is "t" (true). Used by pollRecovery to check
// verify_sql conditions — queries like "SELECT count(*) = 0 FROM ..." exit
// with code 0 regardless of the result, so the exit code alone is not enough.
func RunSQLBool(ctx context.Context, connStr, sql string) error {
	cmd := exec.CommandContext(ctx, "psql", connStr,
		"-v", "ON_ERROR_STOP=1",
		"-t", "-A", // tuples-only, unaligned: output is just "t" or "f"
		"-c", sql,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "t" {
		return fmt.Errorf("verify_sql returned false")
	}
	return nil
}

// RunSQLString executes a SQL string against the database using psql.
// Statements are piped via stdin (-f -) rather than passed with -c so that
// each statement is sent in its own protocol message. This is required for
// ALTER SYSTEM, which rejects execution inside a transaction block — psql -c
// with multiple statements sends them all in one message, which PostgreSQL
// treats as an implicit transaction.
func RunSQLString(ctx context.Context, connStr, sql string) error {
	cmd := exec.CommandContext(ctx, "psql", connStr, "-v", "ON_ERROR_STOP=1", "-f", "-")
	cmd.Stdin = strings.NewReader(sql)
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
// Connects via host.docker.internal:15432 (the host port binding) rather than
// the container hostname, because the pgloader container may be on multiple
// Docker networks and hostname resolution can return the wrong postgres instance.
func RunSQLStringViaPgloader(ctx context.Context, sql string) error {
	args := []string{
		"exec", "helpdesk-test-pgloader",
		"psql", "-h", "host.docker.internal", "-p", "15432", "-U", "postgres", "-d", "testdb",
		"-v", "ON_ERROR_STOP=1",
		"-c", sql,
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pgloader psql: %v\n%s", err, output)
	}
	return nil
}
