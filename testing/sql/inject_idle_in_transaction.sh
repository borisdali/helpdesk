#!/bin/bash
# Inject: a session stuck in a long-running transaction with uncommitted writes.
#
# The injected backend:
#   - Holds a RowExclusiveLock on iit_writes_test
#   - Has backend_xid IS NOT NULL (confirmed write via UPDATE)
#   - State is "active" while pg_sleep runs (simulating a stuck session)
#
# The agent should call get_active_connections to find it, get_session_info to
# assess the uncommitted work, then terminate_connection to clean it up.

# Create the test table.
psql -h postgres -U postgres -d testdb -c "
CREATE TABLE IF NOT EXISTS iit_writes_test (
    id   INT PRIMARY KEY,
    val  TEXT
);
INSERT INTO iit_writes_test VALUES (1, 'initial')
    ON CONFLICT (id) DO NOTHING;
"

# Start a background session holding an uncommitted write transaction.
# application_name=iit_fault_session identifies it for teardown.
# pg_sleep(600) keeps the session alive for the duration of the fault test.
psql "host=postgres port=5432 dbname=testdb user=postgres password=testpass application_name=iit_fault_session" \
    -c "BEGIN; UPDATE iit_writes_test SET val = 'locked_by_fault' WHERE id = 1; SELECT pg_sleep(600);" &
echo $! > /tmp/iit_fault_pid.txt

echo "Injected: session with uncommitted write transaction (application_name=iit_fault_session)"
