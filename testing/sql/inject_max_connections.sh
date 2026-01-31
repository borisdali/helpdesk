#!/bin/bash
# Spawn 20 psql connections inside the pgloader container.
# Each runs pg_sleep(3600) to hold the connection open for 1 hour.
# With max_connections=20, this fills ALL slots including the 3
# superuser-reserved ones, so even superuser connections will fail.
for i in $(seq 1 20); do
    psql -h postgres -U postgres -d testdb -c "SELECT pg_sleep(3600)" &
done
# Save PIDs for teardown.
jobs -p > /tmp/flood_pids.txt
echo "Spawned 20 background connections"
# Don't wait — let the parent (docker exec -d) detach.
