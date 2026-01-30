-- Terminate the long-running query and clean up.

-- Cancel the background query.
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE query LIKE '%pg_sleep(300)%'
  AND pid != pg_backend_pid();

-- Disconnect the dblink connection.
DO $$
BEGIN
    PERFORM dblink_disconnect('long_query_conn');
EXCEPTION WHEN OTHERS THEN
    NULL;
END
$$;

DROP TABLE IF EXISTS test_locked_table;
