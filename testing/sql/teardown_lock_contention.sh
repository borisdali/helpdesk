#!/bin/bash
# Clean up lock contention sessions.

for f in /tmp/lock_sess_a_pid.txt /tmp/lock_sess_b_pid.txt; do
    if [ -f "$f" ]; then
        kill "$(cat "$f")" 2>/dev/null
        rm -f "$f"
    fi
done

psql -h postgres -U postgres -d testdb -c "
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE query LIKE '%test_lock_table%'
  AND pid != pg_backend_pid();
DROP TABLE IF EXISTS test_lock_table;
"
echo "Torn down lock contention"
