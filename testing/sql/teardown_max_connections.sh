#!/bin/bash
#
# aiHelpDesk fault injection helper script.
#
# Terminate flood connections injected by inject_max_connections.sh.
# Primary path: kill the sleep|psql subshell PIDs (closes stdin → psql exits).
# Fallback: terminate any surviving idle testdb backends via pg_terminate_backend.

PSQL="psql -h host.docker.internal -p 15432 -U postgres -d testdb"

if [ -f /tmp/flood_pids.txt ]; then
  while read -r pid; do
    kill -- -"$pid" 2>/dev/null || kill "$pid" 2>/dev/null || true
  done < /tmp/flood_pids.txt
  rm -f /tmp/flood_pids.txt
  sleep 2
fi

# Safety net: terminate any surviving idle backends on this database.
$PSQL -c "
  SELECT pg_terminate_backend(pid)
  FROM pg_stat_activity
  WHERE state IN ('idle', 'idle in transaction')
    AND datname = 'testdb'
    AND pid <> pg_backend_pid();" \
  2>/dev/null

echo "Terminated flood connections"
