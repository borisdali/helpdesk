-- Clean up lock contention sessions.

-- Terminate any sessions still holding locks on the test table.
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE query LIKE '%test_lock_table%'
  AND pid != pg_backend_pid();

-- Disconnect dblink sessions.
DO $$
BEGIN
    PERFORM dblink_disconnect('lock_sess_a');
EXCEPTION WHEN OTHERS THEN NULL;
END
$$;

DO $$
BEGIN
    PERFORM dblink_disconnect('lock_sess_b');
EXCEPTION WHEN OTHERS THEN NULL;
END
$$;

DROP TABLE IF EXISTS test_lock_table;
