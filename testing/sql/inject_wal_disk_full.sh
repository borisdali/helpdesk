#!/bin/bash
#
# aiHelpDesk fault injection script.
#
# Simulate a WAL disk full PANIC: write the PANIC message to PID 1's stderr
# (visible in docker logs), then send SIGABRT to the postmaster so the
# container exits with a non-zero code — exactly how a real ENOSPC crash
# appears to an observer using check_host + get_host_logs.
#
# Filling the actual filesystem is not used here because Docker Desktop
# reports theoretical disk space (hundreds of GB) that doesn't reflect the
# real VM disk image size, making dd-based fills unreliable and slow.
#
# Runs on the test host (shell_exec); requires Docker.

set -e

CONTAINER=helpdesk-test-pg

# Verify the container is running.
if ! docker inspect --format "{{.State.Running}}" "$CONTAINER" 2>/dev/null | grep -q true; then
  echo "ERROR: $CONTAINER is not running" >&2
  exit 1
fi

# Write the PANIC message to PID 1's stderr so it appears in docker logs.
# PostgreSQL's real ENOSPC PANIC goes through elog(), which also writes to
# stderr — the container's captured output stream.
docker exec "$CONTAINER" bash -c \
  'printf "PANIC:  could not write to file \"pg_wal/000000010000000000000001\": No space left on device\n" > /proc/1/fd/2'

# Read the postmaster PID and send SIGABRT — the same signal PostgreSQL's
# PANIC handler triggers via abort().  This exits the container with a
# non-zero code (OOMKilled=false, ExitCode≠0), distinct from a clean stop.
PG_PID=$(docker exec "$CONTAINER" head -1 /var/lib/postgresql/data/postmaster.pid 2>/dev/null)
if [ -z "$PG_PID" ]; then
  echo "ERROR: could not read postmaster.pid" >&2
  exit 1
fi

docker exec "$CONTAINER" kill -SIGABRT "$PG_PID" || true

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

# Fallback: SIGABRT may have been delivered to a worker process that already
# exited; try again with SIGKILL on the postmaster.
docker exec "$CONTAINER" kill -SIGKILL "$PG_PID" 2>/dev/null || true
sleep 2
echo "Container stopped (SIGKILL fallback)"
exit 0
