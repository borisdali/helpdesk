package main

// The lifecycle of one Postgres instance, the same five steps on every
// platform the sysadmin tools reach (v0.32):
//
//	serving      is postgres accepting connections?
//	quiesce      make sure nothing runs on PGDATA (a restore is about to)
//	maintenance  run a script as the postgres OS user with the instance's data
//	             and pgBackRest repo, whether or not postgres is running
//	resume       start the instance again after quiesce
//	logs         the instance's recent output, including a crashed one's
//
// Why: on real Kubernetes, a postgres that can't start leaves its pod in
// CrashLoopBackOff, so `kubectl exec` into it fails, and with it every
// pgBackRest command, the WAL check, the restore and the pg_wal tail push,
// exactly when they're needed. The same holds for a real Docker container,
// which exits with postgres. The test fixtures hid it: their entrypoint
// (testing/docker/start-with-sshd.sh) keeps the container up after postgres
// dies. On Kubernetes, quiesce scales the StatefulSet to 0 and maintenance
// runs in a one-off pod with the StatefulSet's own image and volumes
// (k8s_lifecycle.go). Docker/Podman and a host/VM keep today's behavior:
// maintenance execs into the container or runs on the host, quiesce is
// nothing (the instance is already down when a restore runs), resume is the
// runtime's restart.

import (
	"context"
	"fmt"
	"strings"
)

// pgInstance is one Postgres instance as the lifecycle sees it. local is a
// colocated deployment with no infrastructure config: commands run on the
// agent's own host.
type pgInstance struct {
	host   resolvedHost
	local  bool
	target string
}

// instanceFor resolves target the way every sysadmin tool does: through the
// infrastructure config when one is loaded, else colocated.
func instanceFor(target string) (pgInstance, error) {
	if infraConfig == nil {
		return pgInstance{local: true}, nil
	}
	if target == "" {
		return pgInstance{}, fmt.Errorf("target is required (infrastructure config is loaded, so an empty target is a resolution failure, not a valid colocated-deployment request) — " +
			"if the given connection_string/server ID didn't match a known infrastructure entry, report that as its own finding and escalate rather than omitting target")
	}
	host, err := resolveHost(target)
	if err != nil {
		return pgInstance{}, err
	}
	return pgInstance{host: host, target: target}, nil
}

func (in pgInstance) k8s() bool { return in.host.K8sPodSelector != "" }

func (in pgInstance) container() bool {
	return in.host.Runtime == "docker" || in.host.Runtime == "podman"
}

// serving reports whether postgres accepts connections, with pg_isready's
// output. On Kubernetes an instance with no ready pod isn't serving (scaled
// to 0, or crash-looping), and pg_isready can't be asked.
func (in pgInstance) serving(ctx context.Context) (bool, string) {
	switch {
	case in.local:
		out, err := cmdRunner.Run(ctx, "pg_isready", nil, nil)
		return err == nil, strings.TrimSpace(out)
	case in.k8s():
		pod, ready, err := k8sInstancePod(ctx, in.host)
		if err != nil || pod == "" || !ready {
			return false, ""
		}
		out, err := execInProcess(ctx, in.host, []string{"su", "postgres", "-c", "pg_isready"})
		return err == nil, strings.TrimSpace(out)
	case in.container():
		out, err := execInProcess(ctx, in.host, []string{"su", "postgres", "-c", "pg_isready"})
		return err == nil, strings.TrimSpace(out)
	default:
		out, err := runOnHost(ctx, in.host, "pg_isready", nil, nil)
		return err == nil, strings.TrimSpace(out)
	}
}

// maintenance runs script as the postgres OS user with the instance's data
// and repo. On Kubernetes it runs in the instance's own pod while that pod is
// ready, else in a one-off pod (k8sMaintenance); the restore always uses the
// one-off pod, after quiesce (forceRunner).
func (in pgInstance) maintenance(ctx context.Context, script string, forceRunner bool) (string, error) {
	switch {
	case in.local:
		return cmdRunner.Run(ctx, "sh", []string{"-c", script}, nil)
	case in.k8s():
		if !forceRunner {
			if pod, ready, err := k8sInstancePod(ctx, in.host); err == nil && pod != "" && ready {
				return execInProcess(ctx, in.host, []string{"su", "postgres", "-c", script})
			}
		}
		return k8sMaintenance(ctx, in.host, script)
	case in.container():
		return execInProcess(ctx, in.host, []string{"su", "postgres", "-c", script})
	default:
		return runOnHost(ctx, in.host, "sh", []string{"-c", script}, nil)
	}
}

// quiesce makes sure nothing runs on PGDATA before a restore. Only Kubernetes
// has anything to do: a crash-looping pod restarts postgres on its own, so the
// StatefulSet is scaled to 0 (its replica count recorded for resume). A
// container or systemd instance a restore is allowed on is already down.
func (in pgInstance) quiesce(ctx context.Context) (string, error) {
	if in.k8s() {
		return k8sQuiesce(ctx, in.host)
	}
	return "", nil
}

// quiesced reports whether quiesce left the instance scaled down, so the
// result can tell the playbook it must be resumed.
func (in pgInstance) quiesced() bool { return in.k8s() }

// containerMaintenanceArgs is the Docker/Podman form of a maintenance run
// that doesn't need the instance's container to be running: a throwaway
// container from the same image with the same volumes (--volumes-from),
// running script as postgres. Built in v0.32 but not yet the default:
// maintenance still execs into the container, which the test fixture keeps
// alive after postgres dies; a real container exits with postgres, and
// switching to this (and the fixture to a stock entrypoint) is v0.33.
func containerMaintenanceArgs(container, image, script string) []string {
	return []string{"run", "--rm", "--volumes-from", container, "--entrypoint", "su", image, "postgres", "-c", script}
}

// containerMaintenance runs script per containerMaintenanceArgs, reading the
// image from the container itself.
func containerMaintenance(ctx context.Context, host resolvedHost, script string) (string, error) {
	image, err := runOnHost(ctx, host, host.Runtime, []string{"inspect", "-f", "{{.Config.Image}}", host.ContainerName}, nil)
	if err != nil {
		return image, fmt.Errorf("%s inspect %s: %w", host.Runtime, host.ContainerName, err)
	}
	return runOnHost(ctx, host, host.Runtime, containerMaintenanceArgs(host.ContainerName, strings.TrimSpace(image), script), nil)
}
