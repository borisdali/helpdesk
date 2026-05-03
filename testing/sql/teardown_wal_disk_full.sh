#!/bin/bash
#
# aiHelpDesk fault teardown script.
#
# Remove the WAL filler file and restart the postgres container.
# The container may be stopped (postgres PANIC'd); the volume is accessed
# through a temporary Alpine container so we don't need postgres running.
#
# Runs on the test host (shell_exec); requires Docker.

set -e

CONTAINER=helpdesk-test-pg

# Find the pgdata Docker volume by name (compose project prefix varies).
VOL=$(docker volume ls --format "{{.Name}}" | grep pgdata | tail -1)
if [ -z "$VOL" ]; then
  echo "ERROR: could not find a Docker volume matching 'pgdata'" >&2
  exit 1
fi

# Remove the filler file using a short-lived Alpine container.
docker run --rm -v "${VOL}:/pgdata" alpine \
  rm -f /pgdata/pg_wal/FAULTTEST_WAL_FILLER
echo "Removed FAULTTEST_WAL_FILLER from volume $VOL"

# Start (or restart) the postgres container.
docker start "$CONTAINER"
echo "Started $CONTAINER"

# Wait for PostgreSQL to become ready (up to 60 s).
for i in $(seq 1 20); do
  if docker exec "$CONTAINER" pg_isready -U postgres -q 2>/dev/null; then
    echo "Postgres is ready after $((i * 3)) s"
    exit 0
  fi
  sleep 3
done

echo "WARNING: postgres not healthy after 60 s" >&2
exit 1
