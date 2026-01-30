// Package testutil provides helper functions for the fault testing harness.
package testutil

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// DockerComposeDir is the path to the docker-compose files relative to the
// project root. Set by the caller before use.
var DockerComposeDir string

// DockerCompose runs a docker compose command with the given arguments.
func DockerCompose(ctx context.Context, args ...string) (string, error) {
	cmdArgs := []string{"compose"}
	if DockerComposeDir != "" {
		cmdArgs = append(cmdArgs, "-f", DockerComposeDir+"/docker-compose.yaml")
	}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker compose %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output), nil
}

// DockerComposeStop stops a specific service in the compose stack.
func DockerComposeStop(ctx context.Context, service string) error {
	_, err := DockerCompose(ctx, "stop", service)
	return err
}

// DockerComposeStart starts a specific service in the compose stack.
func DockerComposeStart(ctx context.Context, service string) error {
	_, err := DockerCompose(ctx, "start", service)
	return err
}

// DockerExec runs a command inside a running container.
func DockerExec(ctx context.Context, container string, cmd ...string) (string, error) {
	args := append([]string{"exec", "-i", container}, cmd...)
	c := exec.CommandContext(ctx, "docker", args...)
	output, err := c.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker exec %s %s: %v\n%s",
			container, strings.Join(cmd, " "), err, output)
	}
	return string(output), nil
}
