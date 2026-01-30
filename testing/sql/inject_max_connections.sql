-- Saturate max_connections by opening idle connections via dblink.
-- Requires the dblink extension. Runs inside the pgloader sidecar via docker exec.
-- With max_connections=20 and ~3 reserved for superuser, opening 18 connections
-- will exhaust the pool for regular users.

CREATE EXTENSION IF NOT EXISTS dblink;

-- Open 18 persistent connections that stay idle.
DO $$
DECLARE
    i INT;
    conn_name TEXT;
BEGIN
    FOR i IN 1..18 LOOP
        conn_name := 'flood_conn_' || i;
        BEGIN
            PERFORM dblink_connect(conn_name,
                'host=postgres port=5432 dbname=testdb user=postgres password=testpass');
        EXCEPTION WHEN OTHERS THEN
            RAISE NOTICE 'Connection % failed: %', conn_name, SQLERRM;
        END;
    END LOOP;
END
$$;
