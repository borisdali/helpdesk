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
	"strconv"
	"strings"
	"time"
)

// pgInstance is one Postgres instance as the lifecycle sees it. local is a
// colocated deployment with no infrastructure config: commands run on the
// agent's own host.
type pgInstance struct {
	host   resolvedHost
	local  bool
	target string
	// stopped is set by afterStop: a container this tool stopped. Its
	// maintenance then runs in a throwaway container on its volumes, since
	// there is no container to exec into.
	stopped bool
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
	case in.container() && in.stopped:
		return in.runThrowawayContainer(ctx, script)
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
// running script as postgres, the image read from the container itself
// (`<runtime> inspect -f {{.Config.Image}}`). Wired in v0.33 for a container
// restore_to_point_in_time stopped itself (pgInstance.stopped); the v0.32
// restore tools still exec into the container, which the test fixture keeps
// alive after postgres dies. The image must carry /etc/pgbackrest.conf (or
// the config must be on a volume): the throwaway container has only the
// image and the volumes.
func containerMaintenanceArgs(container, image, script string) []string {
	return []string{"run", "--rm", "--volumes-from", container, "--entrypoint", "su", image, "postgres", "-c", script}
}

// ── stopping a serving instance (point-in-time restore of a live database) ───
//
// restore_from_backup only ever restores an instance that is already down.
// restore_to_point_in_time can also be authorized, at the gate, to stop a
// serving one. These are the two lifecycle steps that needs, per platform:
//
//	stopServing   stop it and keep it stopped (nothing may restart it)
//	startAfterStop undo stopServing when nothing was changed (a refusal)
//
// A restored instance is started by restart_container / restart_service, as
// after any restore; startAfterStop is only for the abort path.

// containerStopTimeout is how long `docker stop` waits for the container to
// exit before it kills it. A variable so tests don't wait it out.
var containerStopTimeout = 60 * time.Second

// pgCtlFastStopScript stops PostgreSQL cleanly (a shutdown checkpoint, WAL
// flushed) with pgBackRest's own idea of PGDATA.
const pgCtlFastStopScript = `PGDATA=$(grep '^pg1-path=' /etc/pgbackrest.conf | cut -d= -f2)
pg_ctl -D "$PGDATA" stop -m fast -w -t 60`

// stopServing stops a serving instance and keeps it stopped, returning a
// sentence saying how. A colocated deployment (no infrastructure config) is
// refused: there is nothing that could keep it down and bring it back.
func (in pgInstance) stopServing(ctx context.Context) (string, error) {
	switch {
	case in.local:
		return "", fmt.Errorf("a colocated instance (no infrastructure config) cannot be stopped and restored by this tool: only a Docker/Podman container, a systemd host or a Kubernetes StatefulSet can")
	case in.k8s():
		// Clean stop first, so the shutdown checkpoint is written; the
		// scale-down then keeps the pod from coming back. A failure here is
		// not fatal: scaling to 0 stops it anyway.
		_, _ = in.maintenance(ctx, pgCtlFastStopScript, false)
		return k8sQuiesce(ctx, in.host)
	case in.container():
		rt := in.host.Runtime
		// The clean stop is best-effort too: a container whose PID 1 is
		// PostgreSQL exits with it, and one that keeps PID 1 alive (the
		// test fixture) stays up until the stop below.
		_, _ = execInProcess(ctx, in.host, []string{"su", "postgres", "-c", pgCtlFastStopScript})
		secs := int(containerStopTimeout / time.Second)
		if secs < 1 {
			secs = 1
		}
		if out, err := runOnHost(ctx, in.host, rt, []string{"stop", "-t", strconv.Itoa(secs), in.host.ContainerName}, nil); err != nil {
			return "", fmt.Errorf("%s stop %s: %w: %s", rt, in.host.ContainerName, err, strings.TrimSpace(out))
		}
		return fmt.Sprintf("container %s stopped", in.host.ContainerName), nil
	default:
		if out, err := runOnHost(ctx, in.host, "sudo", []string{"systemctl", "stop", in.host.SystemdUnit}, nil); err != nil {
			return "", fmt.Errorf("systemctl stop %s: %w: %s", in.host.SystemdUnit, err, strings.TrimSpace(out))
		}
		return fmt.Sprintf("systemd unit %s stopped", in.host.SystemdUnit), nil
	}
}

// afterStop returns the instance as it is once stopServing has run.
func (in pgInstance) afterStop() pgInstance {
	in.stopped = true
	return in
}

// startAfterStop starts an instance stopServing stopped, when nothing about
// it was changed. The restore tools' restart steps do the same for a
// restored one.
func (in pgInstance) startAfterStop(ctx context.Context) (string, error) {
	switch {
	case in.k8s():
		return k8sResume(ctx, in.host)
	case in.container():
		rt := in.host.Runtime
		out, err := runOnHost(ctx, in.host, rt, []string{"start", in.host.ContainerName}, nil)
		return strings.TrimSpace(out), err
	case in.local:
		return "", fmt.Errorf("a colocated instance cannot be started by this tool")
	default:
		// restart, not start: it is what the sudoers grant for this unit allows.
		out, err := runOnHost(ctx, in.host, "sudo", []string{"systemctl", "restart", in.host.SystemdUnit}, nil)
		return strings.TrimSpace(out), err
	}
}

// runThrowawayContainer runs script as postgres in a one-off container from
// the instance's image on its volumes (containerMaintenanceArgs), for a
// container that is stopped.
func (in pgInstance) runThrowawayContainer(ctx context.Context, script string) (string, error) {
	rt := in.host.Runtime
	img, err := runOnHost(ctx, in.host, rt, []string{"inspect", "-f", "{{.Config.Image}}", in.host.ContainerName}, nil)
	image := strings.TrimSpace(img)
	if err != nil || image == "" {
		return img, fmt.Errorf("reading the image of container %s: %v: %s", in.host.ContainerName, err, image)
	}
	return runOnHost(ctx, in.host, rt, containerMaintenanceArgs(in.host.ContainerName, image, script), nil)
}
