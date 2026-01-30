-- Inject lock contention: two sessions each holding a lock the other wants.
-- Session A locks row 1, then tries to lock row 2.
-- Session B locks row 2, then tries to lock row 1.
-- PostgreSQL will detect the deadlock after deadlock_timeout (default 1s)
-- and kill one, but the surviving session keeps its lock visible in pg_locks.

CREATE TABLE IF NOT EXISTS test_lock_table (
    id INT PRIMARY KEY,
    data TEXT
);

INSERT INTO test_lock_table VALUES (1, 'row1'), (2, 'row2')
ON CONFLICT (id) DO NOTHING;

CREATE EXTENSION IF NOT EXISTS dblink;

-- Session A: lock row 1, then try row 2.
SELECT dblink_connect('lock_sess_a',
    'host=postgres port=5432 dbname=testdb user=postgres password=testpass');
SELECT dblink_send_query('lock_sess_a',
    'BEGIN; UPDATE test_lock_table SET data = ''locked_by_a'' WHERE id = 1; SELECT pg_sleep(1); UPDATE test_lock_table SET data = ''locked_by_a'' WHERE id = 2; COMMIT;');

-- Session B: lock row 2, then try row 1.
SELECT dblink_connect('lock_sess_b',
    'host=postgres port=5432 dbname=testdb user=postgres password=testpass');
SELECT dblink_send_query('lock_sess_b',
    'BEGIN; UPDATE test_lock_table SET data = ''locked_by_b'' WHERE id = 2; SELECT pg_sleep(1); UPDATE test_lock_table SET data = ''locked_by_b'' WHERE id = 1; COMMIT;');
