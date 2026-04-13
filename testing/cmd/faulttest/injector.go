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

	"helpdesk/internal/infra"
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

// Inject activates a failure mode. In external mode or when SSHHost is configured,
// ExternalInject is used when set.
func (inj *Injector) Inject(ctx context.Context, f Failure) error {
	spec := f.Inject
	mode := "internal"
	if (inj.cfg.External || inj.cfg.SSHHost != "") && f.ExternalInject.Type != "" {
		spec = f.ExternalInject
		mode = "external"
	}
	slog.Info("injecting failure", "id", f.ID, "type", spec.Type, "mode", mode)
	return inj.exec(ctx, spec, f)
}

// Teardown deactivates a failure mode and restores normal state. In external mode
// or when SSHHost is configured, ExternalTeardown is used when set.
func (inj *Injector) Teardown(ctx context.Context, f Failure) error {
	spec := f.Teardown
	if (inj.cfg.External || inj.cfg.SSHHost != "") && f.ExternalTeardown.Type != "" {
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
	case "shell_exec":
		return inj.execShell(ctx, spec)
	case "":
		return nil
	default:
		return fmt.Errorf("unknown injection type: %s", spec.Type)
	}
}

func (inj *Injector) execSQL(ctx context.Context, spec InjectSpec) error {
	connStr := inj.resolvedConnStr()
	if spec.Target == "replica" {
		connStr = inj.cfg.ReplicaConnStr
	}

	if spec.ScriptInline != "" {
		return testutil.RunSQLString(ctx, connStr, spec.ScriptInline)
	}

	scriptPath := filepath.Join(inj.cfg.TestingDir, spec.Script)

	if spec.ExecVia == "pgloader" {
		return testutil.RunSQLViaPgloader(ctx, scriptPath)
	}

	return testutil.RunSQL(ctx, connStr, scriptPath)
}

func (inj *Injector) resolvedConnStr() string {
	connStr, _ := inj.resolvedConnEnv()
	return connStr
}

func (inj *Injector) execDocker(ctx context.Context, spec InjectSpec) error {
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

func (inj *Injector) execDockerExec(ctx context.Context, spec InjectSpec) error {
	container := spec.ExecVia
	if container == "" {
		container = "helpdesk-test-pgloader"
	}
	if spec.ScriptInline != "" {
		return testutil.DockerExecInlineScript(ctx, container, []byte(spec.ScriptInline), spec.User, spec.Detach)
	}
	scriptPath := filepath.Join(inj.cfg.TestingDir, spec.Script)
	return testutil.DockerCopyAndExec(ctx, container, scriptPath, spec.Detach)
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

// execShell runs an inline bash script or script file on the local host.
// Useful for multi-step inject/teardown that mix docker exec, docker cp, etc.
func (inj *Injector) execShell(ctx context.Context, spec InjectSpec) error {
	var scriptContent []byte
	if spec.ScriptInline != "" {
		scriptContent = []byte(spec.ScriptInline)
	} else if spec.Script != "" {
		path := filepath.Join(inj.cfg.TestingDir, spec.Script)
		var err error
		scriptContent, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("shell_exec: reading %s: %v", path, err)
		}
	} else {
		return fmt.Errorf("shell_exec: script or script_inline is required")
	}
	connStr, pgpassword := inj.resolvedConnEnv()
	cmd := exec.CommandContext(ctx, "bash", "-s")
	cmd.Stdin = bytes.NewReader(scriptContent)
	// Expose the resolved connection string so scripts can use $FAULTTEST_CONN.
	// Also set PGPASSWORD when the infra config supplies a password via
	// password_env, preventing psql from opening /dev/tty and hanging.
	env := append(os.Environ(), "FAULTTEST_CONN="+connStr)
	if pgpassword != "" {
		env = append(env, "PGPASSWORD="+pgpassword)
	}
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("shell_exec: %v\n%s", err, output)
	}
	slog.Info("shell_exec completed", "output", strings.TrimSpace(string(output)))
	return nil
}

// resolvedConnEnv returns the libpq connection string and, separately, the
// password to set as PGPASSWORD. When cfg.ConnStr is a named infra key, the
// entry's ResolvedConnectionString() is used and its password_env value is
// read from the environment. Falls back to cfg.ConnStr / "" when the key is
// not found or no infra config is configured.
func (inj *Injector) resolvedConnEnv() (connStr, pgpassword string) {
	if inj.cfg.InfraConfigPath != "" {
		if cfg, err := infra.Load(inj.cfg.InfraConfigPath); err == nil {
			if db, ok := cfg.DBServers[inj.cfg.ConnStr]; ok {
				pw := ""
				if db.PasswordEnv != "" {
					pw = os.Getenv(db.PasswordEnv)
				}
				return db.ResolvedConnectionString(), pw
			}
		}
	}
	return inj.cfg.ConnStr, ""
}

// execSSH runs a script on a remote host via SSH.
// spec.ExecVia is the target in "user@host" or "host" form; cfg.SSHHost is used
// as fallback when ExecVia is empty. The SSH user from HarnessConfig is prepended
// when no "@" is present. Script content is streamed via stdin.
func (inj *Injector) execSSH(ctx context.Context, spec InjectSpec) error {
	target := spec.ExecVia
	if target == "" {
		target = inj.cfg.SSHHost
	}
	if target == "" {
		return fmt.Errorf("ssh_exec: exec_via (remote host) is required; pass --ssh-host or set exec_via in the catalog")
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
