-- Inject a long-running query that holds an ACCESS EXCLUSIVE lock on a table.
-- This blocks all other queries against that table.

CREATE TABLE IF NOT EXISTS test_locked_table (id serial PRIMARY KEY, data text);
INSERT INTO test_locked_table (data) VALUES ('test') ON CONFLICT DO NOTHING;

-- Run pg_sleep in background via dblink so it holds a lock for 300 seconds.
CREATE EXTENSION IF NOT EXISTS dblink;

SELECT dblink_connect('long_query_conn',
    'host=postgres port=5432 dbname=testdb user=postgres password=testpass');

-- Start a long-running transaction that locks the table.
SELECT dblink_send_query('long_query_conn',
    'BEGIN; LOCK TABLE test_locked_table IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(300); COMMIT;');
