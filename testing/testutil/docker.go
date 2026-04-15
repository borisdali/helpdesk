// Package testutil provides helper functions for the fault testing harness.
package testutil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

// DockerComposeKill sends a signal to a specific service in the compose stack.
// Uses docker compose kill which sends the signal from the host — this bypasses
// the Linux PID-1 signal immunity that applies to signals sent from within the
// container's own PID namespace.
func DockerComposeKill(ctx context.Context, signal, service string) error {
	_, err := DockerCompose(ctx, "kill", "--signal="+signal, service)
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

// DockerExecInlineScript runs an inline bash script inside a running container via stdin.
// If user is non-empty, the script is run as that OS user (docker exec -u).
// If detach is true, the script is first written to a temp file inside the container,
// then launched in the background (docker exec -d).
func DockerExecInlineScript(ctx context.Context, container string, script []byte, user string, detach bool) error {
	if detach {
		// Write the script into the container via stdin, then run it in the background.
		writeArgs := []string{"exec", "-i", container, "bash", "-c",
			"cat > /tmp/faulttest_inline.sh && chmod +x /tmp/faulttest_inline.sh"}
		writeCmd := exec.CommandContext(ctx, "docker", writeArgs...)
		writeCmd.Stdin = bytes.NewReader(script)
		if out, err := writeCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("writing inline script to %s: %v\n%s", container, err, out)
		}
		detachArgs := []string{"exec", "-d"}
		if user != "" {
			detachArgs = append(detachArgs, "-u", user)
		}
		detachArgs = append(detachArgs, container, "bash", "/tmp/faulttest_inline.sh")
		if out, err := exec.CommandContext(ctx, "docker", detachArgs...).CombinedOutput(); err != nil {
			return fmt.Errorf("docker exec -d inline script in %s: %v\n%s", container, err, out)
		}
		time.Sleep(2 * time.Second)
		return nil
	}

	args := []string{"exec", "-i"}
	if user != "" {
		args = append(args, "-u", user)
	}
	args = append(args, container, "bash", "-s")
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker exec inline script in %s: %v\n%s", container, err, output)
	}
	return nil
}

// DockerCopyAndExec copies a script into a container and executes it.
// If detach is true, the script runs in the background (docker exec -d)
// and the function returns immediately without waiting for completion.
func DockerCopyAndExec(ctx context.Context, container, scriptPath string, detach bool) error {
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("reading script %s: %v", scriptPath, err)
	}

	// Copy script into the container.
	destPath := "/tmp/" + filepath.Base(scriptPath)
	cpCmd := exec.CommandContext(ctx, "docker", "cp", scriptPath, container+":"+destPath)
	if out, err := cpCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker cp %s: %v\n%s", scriptPath, err, out)
	}
	_ = script // validated above via ReadFile

	// Make it executable.
	chmodCmd := exec.CommandContext(ctx, "docker", "exec", container, "chmod", "+x", destPath)
	if out, err := chmodCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chmod in container: %v\n%s", err, out)
	}

	if detach {
		// Run in background — docker exec -d doesn't wait.
		execCmd := exec.CommandContext(ctx, "docker", "exec", "-d", container, "bash", destPath)
		if out, err := execCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("docker exec -d: %v\n%s", err, out)
		}
		// Give background processes a moment to spawn.
		time.Sleep(2 * time.Second)
		return nil
	}

	// Run in foreground and wait.
	execCmd := exec.CommandContext(ctx, "docker", "exec", container, "bash", destPath)
	if out, err := execCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker exec: %v\n%s", err, out)
	}
	return nil
}
