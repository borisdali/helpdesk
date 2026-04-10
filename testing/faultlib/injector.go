package faultlib

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

// Inject applies a failure mode. In external mode, ExternalInject is used when set.
func (i *Injector) Inject(ctx context.Context, f Failure) error {
	spec := f.Inject
	if i.cfg.External && f.ExternalInject.Type != "" {
		spec = f.ExternalInject
	}
	return i.exec(ctx, spec, "inject")
}

// Teardown removes a failure mode. In external mode, ExternalTeardown is used when set.
func (i *Injector) Teardown(ctx context.Context, f Failure) error {
	spec := f.Teardown
	if i.cfg.External && f.ExternalTeardown.Type != "" {
		spec = f.ExternalTeardown
	}
	return i.exec(ctx, spec, "teardown")
}

func (i *Injector) exec(ctx context.Context, spec InjectSpec, phase string) error {
	slog.Info("executing injection spec", "type", spec.Type, "phase", phase)

	var err error
	switch spec.Type {
	case "":
		return nil // no-op; skip wait
	case "sql":
		err = i.execSQL(ctx, spec)
	case "docker":
		err = i.execDocker(ctx, spec)
	case "docker_exec":
		err = i.execDockerExec(ctx, spec)
	case "kustomize":
		err = i.execKustomize(ctx, spec)
	case "kustomize_delete":
		err = i.execKustomizeDelete(ctx, spec)
	case "config":
		err = i.execConfig(ctx, spec)
	case "ssh_exec":
		err = i.execSSH(ctx, spec)
	default:
		return fmt.Errorf("unknown inject type: %s", spec.Type)
	}
	if err != nil {
		return err
	}

	if spec.Wait != "" {
		d, parseErr := time.ParseDuration(spec.Wait)
		if parseErr != nil {
			slog.Warn("invalid wait duration, skipping", "phase", phase, "wait", spec.Wait, "err", parseErr)
		} else {
			slog.Info("waiting after injection", "phase", phase, "duration", d)
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

func (i *Injector) execSQL(ctx context.Context, spec InjectSpec) error {
	connStr := i.cfg.ConnStr
	if spec.Target == "replica" {
		connStr = i.cfg.ReplicaConnStr
	}

	if spec.ScriptInline != "" {
		return testutil.RunSQLString(ctx, connStr, spec.ScriptInline)
	}
	if spec.Script != "" {
		scriptPath := filepath.Join(i.cfg.TestingDir, spec.Script)
		return testutil.RunSQL(ctx, connStr, scriptPath)
	}
	return nil
}

func (i *Injector) execDocker(ctx context.Context, spec InjectSpec) error {
	switch spec.Action {
	case "stop":
		return testutil.DockerComposeStop(ctx, spec.Service)
	case "start":
		return testutil.DockerComposeStart(ctx, spec.Service)
	case "kill":
		sig := spec.Signal
		if sig == "" {
			sig = "SIGKILL"
		}
		return testutil.DockerComposeKill(ctx, sig, spec.Service)
	default:
		return fmt.Errorf("unknown docker action: %s", spec.Action)
	}
}

func (i *Injector) execDockerExec(ctx context.Context, spec InjectSpec) error {
	scriptPath := filepath.Join(i.cfg.TestingDir, spec.Script)
	container := spec.ExecVia
	if container == "" {
		container = "helpdesk-test-pgloader"
	}
	return testutil.DockerCopyAndExec(ctx, container, scriptPath, spec.Detach)
}

func (i *Injector) execKustomize(ctx context.Context, spec InjectSpec) error {
	overlayPath := filepath.Join(i.cfg.TestingDir, spec.Overlay)
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-k", overlayPath, "--context", i.cfg.KubeContext)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply: %v\n%s", err, output)
	}
	return nil
}

func (i *Injector) execKustomizeDelete(ctx context.Context, spec InjectSpec) error {
	overlayPath := filepath.Join(i.cfg.TestingDir, spec.Overlay)
	cmd := exec.CommandContext(ctx, "kubectl", "delete", "-k", overlayPath, "--context", i.cfg.KubeContext, "--ignore-not-found")
	if output, err := cmd.CombinedOutput(); err != nil {
		slog.Warn("kubectl delete failed", "err", err, "output", string(output))
	}

	// If restore is specified, re-apply the base.
	if restore, ok := spec.Restore.(string); ok && restore != "" {
		restorePath := filepath.Join(i.cfg.TestingDir, restore)
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-k", restorePath, "--context", i.cfg.KubeContext)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("kubectl apply restore: %v\n%s", err, output)
		}
	}
	return nil
}

func (i *Injector) execConfig(ctx context.Context, spec InjectSpec) error {
	// Config override: temporarily change the connection string.
	if newConn, ok := spec.Override["connection_string"]; ok {
		i.cfg.ConnStr = newConn
	}
	return nil
}

// execSSH runs a script on a remote host via SSH.
// spec.ExecVia is the target in "user@host" or "host" form; the SSH user from
// HarnessConfig is used as a fallback when ExecVia has no user prefix.
// The script content is streamed via stdin: ssh ... 'bash -s' < scriptContent.
func (i *Injector) execSSH(ctx context.Context, spec InjectSpec) error {
	target := spec.ExecVia
	if target == "" {
		return fmt.Errorf("ssh_exec: exec_via (remote host) is required")
	}

	// If ExecVia has no "@", prepend the configured SSH user.
	if !strings.Contains(target, "@") && i.cfg.SSHUser != "" {
		target = i.cfg.SSHUser + "@" + target
	}

	// Resolve script content.
	var scriptContent []byte
	switch {
	case spec.ScriptInline != "":
		scriptContent = []byte(spec.ScriptInline)
	case spec.Script != "":
		scriptPath := filepath.Join(i.cfg.TestingDir, spec.Script)
		var err error
		scriptContent, err = os.ReadFile(scriptPath)
		if err != nil {
			return fmt.Errorf("ssh_exec: reading script %s: %v", scriptPath, err)
		}
	default:
		return fmt.Errorf("ssh_exec: script or script_inline is required")
	}

	args := []string{"-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes"}
	if i.cfg.SSHKeyPath != "" {
		args = append(args, "-i", i.cfg.SSHKeyPath)
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
