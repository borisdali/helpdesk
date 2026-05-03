#!/bin/bash
#
# aiHelpDesk fault injection script.
#
# Fill the pg_wal directory to within ~10 MB of the volume limit, then
# force a WAL segment switch to trigger a PANIC immediately.
#
# Runs on the test host (shell_exec); requires Docker.

set -e

CONTAINER=helpdesk-test-pg

# Verify the container is running.
if ! docker inspect --format "{{.State.Running}}" "$CONTAINER" 2>/dev/null | grep -q true; then
  echo "ERROR: $CONTAINER is not running" >&2
  exit 1
fi

# Calculate how much to fill (leave 10 MB free so teardown has headroom).
AVAIL_KB=$(docker exec "$CONTAINER" df -k /var/lib/postgresql/data | awk 'NR==2{print $4}')
FILL_MB=$(( AVAIL_KB / 1024 - 10 ))
if [ "$FILL_MB" -le 0 ]; then
  echo "ERROR: less than 10 MB free on data volume (${AVAIL_KB} KB available)" >&2
  exit 1
fi

echo "Filling pg_wal with ${FILL_MB} MB (${AVAIL_KB} KB was free)"
docker exec "$CONTAINER" dd if=/dev/zero \
  of=/var/lib/postgresql/data/pg_wal/FAULTTEST_WAL_FILLER \
  bs=1M count="$FILL_MB" 2>&1

# Force a WAL segment switch.  PostgreSQL will attempt to open a new segment,
# hit ENOSPC, and emit PANIC — the process exits and the container stops.
# The psql call is expected to fail; ignore its exit code.
docker exec "$CONTAINER" psql -U postgres \
  -c "SELECT pg_switch_wal();" 2>&1 || true

sleep 2
echo "Injection complete — postgres PANIC expected, container may have stopped"
