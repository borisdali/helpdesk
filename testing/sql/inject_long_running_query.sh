#!/bin/bash
#
# aiHelpDesk fault injection helper script.
#
# Spawn a psql session that holds an ACCESS EXCLUSIVE lock on a table
# indefinitely (no pg_sleep self-clearing). The bash `sleep 600` keeps stdin
# open so psql never sees EOF and never auto-commits; the lock only releases
# when the session is killed by the agent or the teardown script runs.

psql -h host.docker.internal -p 15432 -U postgres -d testdb -c "
CREATE TABLE IF NOT EXISTS test_locked_table (id serial PRIMARY KEY, data text);
INSERT INTO test_locked_table (data) VALUES ('test') ON CONFLICT DO NOTHING;
"

{ { printf "BEGIN;\nLOCK TABLE test_locked_table IN ACCESS EXCLUSIVE MODE;\n"; sleep 600; } \
  | psql -h host.docker.internal -p 15432 -U postgres -d testdb; } >/dev/null 2>&1 &

echo $! > /tmp/long_query_pid.txt
echo "Injected: ACCESS EXCLUSIVE lock on test_locked_table (held until killed)"
