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
	"time"

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
	var err error
	switch spec.Type {
	case "sql":
		err = inj.execSQL(ctx, spec)
	case "docker":
		err = inj.execDocker(ctx, spec)
	case "docker_exec":
		err = inj.execDockerExec(ctx, spec)
	case "kustomize":
		err = inj.execKustomize(ctx, spec)
	case "kustomize_delete":
		err = inj.execKustomizeDelete(ctx, spec)
	case "config":
		err = inj.execConfig(ctx, spec)
	case "ssh_exec":
		err = inj.execSSH(ctx, spec)
	case "shell_exec":
		err = inj.execShell(ctx, spec)
	case "":
		return nil
	default:
		return fmt.Errorf("unknown injection type: %s", spec.Type)
	}
	if err != nil {
		return err
	}
	if spec.Wait != "" {
		d, parseErr := time.ParseDuration(spec.Wait)
		if parseErr != nil {
			slog.Warn("invalid wait duration, skipping", "wait", spec.Wait, "err", parseErr)
		} else {
			slog.Info("waiting after injection", "duration", d)
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

func (inj *Injector) execSQL(ctx context.Context, spec InjectSpec) error {
	connStr := inj.resolvedConnStr()
	if spec.Target == "replica" {
		if inj.cfg.ReplicaConnStr == "" {
			return fmt.Errorf("this fault targets the replica but --replica-conn is not set; provide a replica connection string to run it")
		}
		connStr = inj.resolvedReplicaConnStr()
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

// resolvedReplicaConnStr resolves cfg.ReplicaConnStr through the infra config
// so callers can pass an infra key instead of a raw DSN.
func (inj *Injector) resolvedReplicaConnStr() string {
	if inj.cfg.InfraConfigPath != "" && inj.cfg.ReplicaConnStr != "" {
		if cfg, err := infra.Load(inj.cfg.InfraConfigPath); err == nil {
			if db, ok := cfg.DBServers[inj.cfg.ReplicaConnStr]; ok {
				return db.ResolvedConnectionString()
			}
		}
	}
	return inj.cfg.ReplicaConnStr
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
	// FAULTTEST_CONTAINER is the docker/podman container name from the infra
	// config, used by docker-based inject/teardown scripts.
	// FAULTTEST_K8S_CONTEXT is the kubectl context from --context, used by
	// K8s shell_exec inject/teardown scripts.
	env := append(os.Environ(), "FAULTTEST_CONN="+connStr)
	if pgpassword != "" {
		env = append(env, "PGPASSWORD="+pgpassword)
	}
	if containerName := inj.resolvedContainerName(); containerName != "" {
		env = append(env, "FAULTTEST_CONTAINER="+containerName)
	}
	if inj.cfg.KubeContext != "" {
		env = append(env, "FAULTTEST_K8S_CONTEXT="+inj.cfg.KubeContext)
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

// resolvedContainerName returns the container_name from the infra config for
// the current --conn target, or "" if not configured. Exposed to inject scripts
// as $FAULTTEST_CONTAINER so docker-based external faults can target the right
// container without hardcoding a name.
func (inj *Injector) resolvedContainerName() string {
	if inj.cfg.InfraConfigPath != "" {
		if cfg, err := infra.Load(inj.cfg.InfraConfigPath); err == nil {
			if db, ok := cfg.DBServers[inj.cfg.ConnStr]; ok {
				return db.ContainerName
			}
		}
	}
	return ""
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

	// Prepend a profile source + variable exports so the remote script can use
	// $FAULTTEST_CONN and $FAULTTEST_CONTAINER without requiring the SSH server
	// to accept env vars.  Non-interactive bash -s sessions don't load ~/.bashrc
	// or ~/.profile, so docker/kubectl may not be in PATH without this.
	connStr, _ := inj.resolvedConnEnv()
	var preamble strings.Builder
	// Extend PATH to cover common Docker/kubectl install locations.
	// Non-interactive SSH sessions on macOS/Linux start with a minimal PATH
	// (/usr/bin:/bin) — Docker Desktop, Homebrew, and nvm all install outside
	// that.  We extend rather than replace so any existing entries are kept.
	preamble.WriteString(`export PATH="/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin:$PATH"` + "\n")
	fmt.Fprintf(&preamble, "export FAULTTEST_CONN=%q\n", connStr)
	if containerName := inj.resolvedContainerName(); containerName != "" {
		fmt.Fprintf(&preamble, "export FAULTTEST_CONTAINER=%q\n", containerName)
	}
	scriptContent = append([]byte(preamble.String()), scriptContent...)

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
