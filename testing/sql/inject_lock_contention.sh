#!/bin/bash
# Inject lock contention: two sessions each holding a row lock the other wants.
# PostgreSQL detects the deadlock after deadlock_timeout and kills one session,
# but the surviving session keeps its lock visible in pg_locks.

# Create the test table.
psql -h postgres -U postgres -d testdb -c "
CREATE TABLE IF NOT EXISTS test_lock_table (id INT PRIMARY KEY, data TEXT);
INSERT INTO test_lock_table VALUES (1, 'row1'), (2, 'row2') ON CONFLICT (id) DO NOTHING;
"

# Session A: lock row 1, sleep, then try row 2.
psql -h postgres -U postgres -d testdb -c "
BEGIN;
UPDATE test_lock_table SET data = 'locked_by_a' WHERE id = 1;
SELECT pg_sleep(1);
UPDATE test_lock_table SET data = 'locked_by_a' WHERE id = 2;
COMMIT;
" &
echo $! > /tmp/lock_sess_a_pid.txt

# Session B: lock row 2, sleep, then try row 1.
psql -h postgres -U postgres -d testdb -c "
BEGIN;
UPDATE test_lock_table SET data = 'locked_by_b' WHERE id = 2;
SELECT pg_sleep(1);
UPDATE test_lock_table SET data = 'locked_by_b' WHERE id = 1;
COMMIT;
" &
echo $! > /tmp/lock_sess_b_pid.txt

echo "Started lock contention sessions"
