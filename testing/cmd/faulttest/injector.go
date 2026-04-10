package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"helpdesk/testing/testutil"
)

// Injector handles failure injection and teardown.
type Injector struct {
	cfg *HarnessConfig
}

// NewInjector creates a new Injector with the given config.
func NewInjector(cfg *HarnessConfig) *Injector {
	return &Injector{cfg: cfg}
}

// Inject activates a failure mode. In external mode, ExternalInject is used when set.
func (inj *Injector) Inject(ctx context.Context, f Failure) error {
	spec := f.Inject
	if inj.cfg.External && f.ExternalInject.Type != "" {
		spec = f.ExternalInject
	}
	slog.Info("injecting failure", "id", f.ID, "type", spec.Type)
	return inj.exec(ctx, spec, f)
}

// Teardown deactivates a failure mode and restores normal state. In external mode,
// ExternalTeardown is used when set.
func (inj *Injector) Teardown(ctx context.Context, f Failure) error {
	spec := f.Teardown
	if inj.cfg.External && f.ExternalTeardown.Type != "" {
		spec = f.ExternalTeardown
	}
	slog.Info("tearing down failure", "id", f.ID, "type", spec.Type)
	return inj.exec(ctx, spec, f)
}

func (inj *Injector) exec(ctx context.Context, spec InjectSpec, f Failure) error {
	switch spec.Type {
	case "sql":
		return inj.execSQL(ctx, spec)
	case "docker":
		return inj.execDocker(ctx, spec)
	case "docker_exec":
		return inj.execDockerExec(ctx, spec)
	case "kustomize":
		return inj.execKustomize(ctx, spec)
	case "kustomize_delete":
		return inj.execKustomizeDelete(ctx, spec)
	case "config":
		return inj.execConfig(ctx, spec)
	case "ssh_exec":
		return inj.execSSH(ctx, spec)
	case "":
		return nil
	default:
		return fmt.Errorf("unknown injection type: %s", spec.Type)
	}
}

func (inj *Injector) execSQL(ctx context.Context, spec InjectSpec) error {
	if spec.ScriptInline != "" {
		connStr := inj.cfg.ConnStr
		if spec.Target == "replica" {
			connStr = inj.cfg.ReplicaConnStr
		}
		return testutil.RunSQLString(ctx, connStr, spec.ScriptInline)
	}

	scriptPath := filepath.Join(inj.cfg.TestingDir, spec.Script)

	if spec.ExecVia == "pgloader" {
		return testutil.RunSQLViaPgloader(ctx, scriptPath)
	}

	connStr := inj.cfg.ConnStr
	if spec.Target == "replica" {
		connStr = inj.cfg.ReplicaConnStr
	}
	return testutil.RunSQL(ctx, connStr, scriptPath)
}

func (inj *Injector) execDocker(ctx context.Context, spec InjectSpec) error {
	switch spec.Action {
	case "stop":
		return testutil.DockerComposeStop(ctx, spec.Service)
	case "start":
		return testutil.DockerComposeStart(ctx, spec.Service)
	default:
		return fmt.Errorf("unknown docker action: %s", spec.Action)
	}
}

func (inj *Injector) execDockerExec(ctx context.Context, spec InjectSpec) error {
	scriptPath := filepath.Join(inj.cfg.TestingDir, spec.Script)
	container := spec.ExecVia
	if container == "" {
		container = "helpdesk-test-pgloader"
	}

	// Copy script into container and execute it.
	if err := testutil.DockerCopyAndExec(ctx, container, scriptPath, spec.Detach); err != nil {
		return fmt.Errorf("docker exec in %s: %v", container, err)
	}
	return nil
}

func (inj *Injector) execKustomize(ctx context.Context, spec InjectSpec) error {
	overlayDir := filepath.Join(inj.cfg.TestingDir, spec.Overlay)
	return testutil.KustomizeApply(ctx, overlayDir, inj.cfg.KubeContext)
}

func (inj *Injector) execKustomizeDelete(ctx context.Context, spec InjectSpec) error {
	overlayDir := filepath.Join(inj.cfg.TestingDir, spec.Overlay)
	if err := testutil.KustomizeDelete(ctx, overlayDir, inj.cfg.KubeContext); err != nil {
		return err
	}

	// Restore base manifests if specified.
	if restore, ok := spec.Restore.(string); ok && restore != "" {
		restoreDir := filepath.Join(inj.cfg.TestingDir, restore)
		return testutil.KustomizeApply(ctx, restoreDir, inj.cfg.KubeContext)
	}
	return nil
}

func (inj *Injector) execConfig(_ context.Context, spec InjectSpec) error {
	if spec.Override != nil {
		if v, ok := spec.Override["connection_string"]; ok {
			inj.cfg.ConnStr = v
		}
		return nil
	}

	// Restore is handled by the caller resetting HarnessConfig.
	return nil
}

// execSSH runs a script on a remote host via SSH.
// spec.ExecVia is the target in "user@host" or "host" form; the SSH user from
// HarnessConfig is used as a fallback when ExecVia has no user prefix.
// The script content is streamed via stdin: ssh ... 'bash -s' < scriptContent.
func (inj *Injector) execSSH(ctx context.Context, spec InjectSpec) error {
	target := spec.ExecVia
	if target == "" {
		return fmt.Errorf("ssh_exec: exec_via (remote host) is required")
	}

	if !strings.Contains(target, "@") && inj.cfg.SSHUser != "" {
		target = inj.cfg.SSHUser + "@" + target
	}

	var scriptContent []byte
	switch {
	case spec.ScriptInline != "":
		scriptContent = []byte(spec.ScriptInline)
	case spec.Script != "":
		scriptPath := filepath.Join(inj.cfg.TestingDir, spec.Script)
		var err error
		scriptContent, err = os.ReadFile(scriptPath)
		if err != nil {
			return fmt.Errorf("ssh_exec: reading script %s: %v", scriptPath, err)
		}
	default:
		return fmt.Errorf("ssh_exec: script or script_inline is required")
	}

	args := []string{"-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes"}
	if inj.cfg.SSHKeyPath != "" {
		args = append(args, "-i", inj.cfg.SSHKeyPath)
	}
	args = append(args, target, "bash -s")

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(scriptContent)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh_exec on %s: %v\n%s", target, err, output)
	}
	slog.Info("ssh_exec completed", "target", target, "output", strings.TrimSpace(string(output)))
	return nil
}
