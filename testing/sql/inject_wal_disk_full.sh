#!/bin/bash
#
# aiHelpDesk fault injection script.
#
# Simulate a WAL disk full PANIC: append the PANIC message to PostgreSQL's
# log file (with logging_collector=on, that's where the agent will look),
# then SIGKILL the container from outside so it exits with a non-zero code
# (ExitCode=137, OOMKilled=false) — indistinguishable from an internal crash
# to check_host, and the log file carries the diagnostic evidence.
#
# Writing to /proc/1/fd/2 is blocked by Docker Desktop's seccomp profile
# even as root, so we write directly to the log file instead.
#
# Runs on the test host (shell_exec); requires Docker.

set -e

CONTAINER=helpdesk-test-pg

# Verify the container is running.
if ! docker inspect --format "{{.State.Running}}" "$CONTAINER" 2>/dev/null | grep -q true; then
  echo "ERROR: $CONTAINER is not running" >&2
  exit 1
fi

# Append the PANIC message to the PostgreSQL log file.
# With logging_collector=on the log is at $PGDATA/log/postgresql.log.
# docker exec runs as root, which can append to files owned by the
# postgres user.
TS=$(docker exec "$CONTAINER" date -u '+%Y-%m-%d %H:%M:%S.000 UTC')
docker exec "$CONTAINER" bash -c "
  mkdir -p /var/lib/postgresql/data/log
  echo '${TS} [1] FATAL:  could not write to file \"pg_wal/000000010000000000000001\": No space left on device' \
    >> /var/lib/postgresql/data/log/postgresql.log
  echo '${TS} [1] PANIC:  could not write to file \"pg_wal/000000010000000000000001\": No space left on device' \
    >> /var/lib/postgresql/data/log/postgresql.log
  echo '${TS} [1] LOG:  startup process (PID 1) was terminated by signal 6: Aborted' \
    >> /var/lib/postgresql/data/log/postgresql.log
"

# Kill the container from outside — no /proc permission issues.
# SIGKILL gives ExitCode=137, OOMKilled=false, which the sysadmin guidance
# routes to "process exited with error, check logs".
docker kill "$CONTAINER" > /dev/null

# Wait up to 10 s for the container to stop.
for i in $(seq 1 10); do
  STATUS=$(docker inspect --format "{{.State.Running}}" "$CONTAINER" 2>/dev/null || echo "false")
  if [ "$STATUS" != "true" ]; then
    EXIT_CODE=$(docker inspect --format "{{.State.ExitCode}}" "$CONTAINER" 2>/dev/null || echo "?")
    echo "Container stopped (ExitCode=${EXIT_CODE})"
    exit 0
  fi
  sleep 1
done

echo "Warning: container still running after 10 s"
exit 0
