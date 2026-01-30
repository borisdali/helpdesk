package main

import (
	"context"
	"fmt"
	"log/slog"
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

// Inject activates a failure mode.
func (inj *Injector) Inject(ctx context.Context, f Failure) error {
	slog.Info("injecting failure", "id", f.ID, "type", f.Inject.Type)
	return inj.exec(ctx, f.Inject, f)
}

// Teardown deactivates a failure mode and restores normal state.
func (inj *Injector) Teardown(ctx context.Context, f Failure) error {
	slog.Info("tearing down failure", "id", f.ID, "type", f.Teardown.Type)
	return inj.exec(ctx, f.Teardown, f)
}

func (inj *Injector) exec(ctx context.Context, spec InjectSpec, f Failure) error {
	switch spec.Type {
	case "sql":
		return inj.execSQL(ctx, spec)
	case "docker":
		return inj.execDocker(ctx, spec)
	case "kustomize":
		return inj.execKustomize(ctx, spec)
	case "kustomize_delete":
		return inj.execKustomizeDelete(ctx, spec)
	case "config":
		return inj.execConfig(ctx, spec)
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
