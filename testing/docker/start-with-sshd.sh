#!/bin/bash
# Starts sshd in the background, then runs the base postgres image's own
# entrypoint with whatever args docker-compose's `command:` supplies —
# preserves normal postgres startup behavior unchanged, sshd is purely
# additive. Exists so the sysadmin agent's SSH-dispatch path
# (agents/sysadmin/sshexec.go) has a real, separate connection path to test
# pgBackRest's own SSH dispatch against, distinct from the docker-exec path
# already covered by db-pgbackrest-repo-unreadable.
#
# Deliberately does NOT `exec` the postgres entrypoint (which would replace
# this script, making postgres PID 1). db-pgdata-corrupted (v0.31) crashes
# postgres on purpose (missing pg_control -> "PANIC: could not locate a
# valid checkpoint record") to test real restore — if postgres were PID 1,
# that crash would kill the whole container, taking sshd down with it and
# making the target unreachable by either of agents/sysadmin's dispatch
# paths at the exact moment diagnosis/restore needs them. Backgrounding it
# instead keeps this script as PID 1: postgres's stdout/stderr still go to
# the container's own streams (same fds, just not through exec), so
# `docker logs` is unaffected, but a postgres crash no longer takes sshd or
# the container itself down with it.
set -uo pipefail

/usr/sbin/sshd

# stdbuf -oL -eL: force line-buffered stdout/stderr through the whole
# docker-entrypoint.sh -> postgres process tree (propagates via LD_PRELOAD,
# inherited across fork/exec). Without this, a non-TTY pipe makes glibc's
# stdio fully block-buffer postgres's own output — found live, 2026-10-07:
# a real agent run's get_host_logs call raced postgres's own crash message
# ("could not find the database system"), which was still sitting in
# postgres's unflushed buffer, while this script's own `echo` below (plain
# bash, flushes immediately) had already landed in the log stream —
# `docker logs` showed the wrapper's "exited with code 2" line with
# postgres's own diagnostic text missing entirely, even though it appeared
# moments later once the OS eventually flushed it. A bigger get_host_logs
# line window would not have fixed this: the content wasn't written yet,
# regardless of how many lines were requested.
stdbuf -oL -eL /usr/local/bin/docker-entrypoint.sh "$@" &
PG_PID=$!
wait "$PG_PID"
EXIT_CODE=$?
echo "start-with-sshd.sh: postgres exited with code ${EXIT_CODE} — container staying up (sshd/exec still reachable) for diagnosis and restore"

# Keep PID 1 alive indefinitely. Nothing restarts postgres automatically
# from here — that's the remediation playbook's job (restore_from_backup,
# then restart_container to re-run this script from scratch against the
# repaired data directory).
tail -f /dev/null
