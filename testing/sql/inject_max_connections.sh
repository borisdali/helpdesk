#!/bin/bash
#
# aiHelpDesk fault injection helper script.
#
# Fill available connection slots up to the non-superuser limit so regular
# users cannot connect while leaving superuser-reserved slots available for
# the agent to investigate and for the teardown script to clean up.
#
# Connection counts are read dynamically so this works regardless of the
# value of max_connections in the test Postgres instance.

MAX_CONN=$(psql -h host.docker.internal -p 15432 -U postgres -d testdb -t -A -c "SHOW max_connections;" 2>/dev/null | tr -d ' \n')
SU_RESERVED=$(psql -h host.docker.internal -p 15432 -U postgres -d testdb -t -A -c "SHOW superuser_reserved_connections;" 2>/dev/null | tr -d ' \n')
if ! printf '%s' "$MAX_CONN" | grep -qE '^[0-9]+$'; then
  echo "ERROR: could not read max_connections" >&2; exit 1
fi
if ! printf '%s' "$SU_RESERVED" | grep -qE '^[0-9]+$'; then
  SU_RESERVED=3
fi

EXISTING=$(psql -h host.docker.internal -p 15432 -U postgres -d testdb -t -A \
  -c "SELECT count(*) FROM pg_stat_activity WHERE datname = 'testdb';" 2>/dev/null | tr -d ' \n')
EXISTING=${EXISTING:-0}

# Target: fill up to max_connections - superuser_reserved (regular user limit).
TARGET=$((MAX_CONN - SU_RESERVED))
SLOTS=$((TARGET - EXISTING))
if [ "$SLOTS" -le 0 ]; then
  echo "Already at target ($EXISTING/$MAX_CONN connections); skipping flood"
  exit 0
fi

# Each connection holds open with pg_sleep(3600) so teardown can find and
# terminate them by matching query LIKE '%pg_sleep%'.
for i in $(seq 1 "$SLOTS"); do
  psql -h host.docker.internal -p 15432 -U postgres -d testdb -c "SELECT pg_sleep(3600)" >/dev/null 2>&1 &
done

# Save PIDs for teardown.
jobs -p > /tmp/flood_pids.txt
sleep 3
echo "Spawned $SLOTS background connections ($EXISTING existing → $TARGET/$MAX_CONN)"
