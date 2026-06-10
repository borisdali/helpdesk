#!/bin/bash
#
# aiHelpDesk fault injection helper script.
#
# Kill the long-running lock holder and clean up.

if [ -f /tmp/long_query_pid.txt ]; then
    kill "$(cat /tmp/long_query_pid.txt)" 2>/dev/null || true
    rm -f /tmp/long_query_pid.txt
fi

# Terminate from the server side using the lock relation as the identifier.
psql -h host.docker.internal -p 15432 -U postgres -d testdb -c "
SELECT pg_terminate_backend(l.pid)
FROM pg_locks l
JOIN pg_class c ON c.oid = l.relation
WHERE c.relname = 'test_locked_table'
  AND l.pid <> pg_backend_pid();
DROP TABLE IF EXISTS test_locked_table;
"
echo "Torn down long-running lock on test_locked_table"
