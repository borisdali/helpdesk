#!/bin/bash
#
# aiHelpDesk fault injection helpder script.
#
# Spawn 17 psql connections inside the pgloader container.
# Each runs pg_sleep(3600) to hold the connection open for 1 hour.
# With max_connections=20 and superuser_reserved_connections=3, 17
# connections exhaust the non-superuser quota so regular users cannot
# connect, while leaving the 3 superuser-reserved slots available for
# the agent to investigate and for the teardown script to clean up.
for i in $(seq 1 17); do
    psql -h postgres -U postgres -d testdb -c "SELECT pg_sleep(3600)" &
done
# Save PIDs for teardown.
jobs -p > /tmp/flood_pids.txt
echo "Spawned 17 background connections"
# Don't wait — let the parent (docker exec -d) detach.
