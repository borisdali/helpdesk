package faultlib

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"

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

// Inject applies a failure mode.
func (i *Injector) Inject(ctx context.Context, f Failure) error {
	return i.exec(ctx, f.Inject, "inject")
}

// Teardown removes a failure mode.
func (i *Injector) Teardown(ctx context.Context, f Failure) error {
	return i.exec(ctx, f.Teardown, "teardown")
}

func (i *Injector) exec(ctx context.Context, spec InjectSpec, phase string) error {
	slog.Info("executing injection spec", "type", spec.Type, "phase", phase)

	switch spec.Type {
	case "":
		return nil
	case "sql":
		return i.execSQL(ctx, spec)
	case "docker":
		return i.execDocker(ctx, spec)
	case "docker_exec":
		return i.execDockerExec(ctx, spec)
	case "kustomize":
		return i.execKustomize(ctx, spec)
	case "kustomize_delete":
		return i.execKustomizeDelete(ctx, spec)
	case "config":
		return i.execConfig(ctx, spec)
	default:
		return fmt.Errorf("unknown inject type: %s", spec.Type)
	}
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
