#!/bin/bash
# Kill the long-running query and clean up.

if [ -f /tmp/long_query_pid.txt ]; then
    kill "$(cat /tmp/long_query_pid.txt)" 2>/dev/null
    rm -f /tmp/long_query_pid.txt
fi

# Also terminate from the server side.
psql -h postgres -U postgres -d testdb -c "
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE query LIKE '%pg_sleep(300)%'
  AND pid != pg_backend_pid();
DROP TABLE IF EXISTS test_locked_table;
"
echo "Torn down long-running query"
