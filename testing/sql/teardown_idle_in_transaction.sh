#!/bin/bash
# Tear down the idle-in-transaction fault scenario.

# Kill the shell process (psql) if it is still running.
if [ -f /tmp/iit_fault_pid.txt ]; then
    kill "$(cat /tmp/iit_fault_pid.txt)" 2>/dev/null
    rm -f /tmp/iit_fault_pid.txt
fi

# Terminate any remaining PostgreSQL backends from the injected session
# and drop the test table.
psql -h postgres -U postgres -d testdb -c "
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE application_name = 'iit_fault_session'
  AND pid <> pg_backend_pid();
DROP TABLE IF EXISTS iit_writes_test;
"

echo "Torn down: idle-in-transaction fault"
