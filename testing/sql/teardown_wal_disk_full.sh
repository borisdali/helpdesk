#!/bin/bash
#
# aiHelpDesk fault teardown script.
#
# The container was stopped by a simulated WAL PANIC (SIGABRT to the
# postmaster).  The pg_wal directory was never modified, so PostgreSQL
# can perform normal crash recovery on restart.
#
# Runs on the test host (shell_exec); requires Docker.

CONTAINER=helpdesk-test-pg

# Start (or restart) the postgres container.  PostgreSQL replays WAL to
# recover from the simulated unclean shutdown.
docker start "$CONTAINER"
echo "Started $CONTAINER"

# Wait for PostgreSQL to become ready (up to 60 s).
for i in $(seq 1 20); do
  if docker exec "$CONTAINER" pg_isready -U postgres -q 2>/dev/null; then
    echo "PostgreSQL ready after $((i * 3)) s"
    exit 0
  fi
  sleep 3
done

echo "WARNING: PostgreSQL not healthy after 60 s" >&2
exit 1
