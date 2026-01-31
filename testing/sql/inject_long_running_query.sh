#!/bin/bash
# Spawn a psql session that holds an ACCESS EXCLUSIVE lock on a table for 5 minutes.
# This blocks all other queries against the table.

# Create the test table first.
psql -h postgres -U postgres -d testdb -c "
CREATE TABLE IF NOT EXISTS test_locked_table (id serial PRIMARY KEY, data text);
INSERT INTO test_locked_table (data) VALUES ('test') ON CONFLICT DO NOTHING;
"

# Start a long-running transaction that locks the table.
psql -h postgres -U postgres -d testdb -c "
BEGIN;
LOCK TABLE test_locked_table IN ACCESS EXCLUSIVE MODE;
SELECT pg_sleep(300);
COMMIT;
" &

echo $! > /tmp/long_query_pid.txt
echo "Started long-running query with lock"
