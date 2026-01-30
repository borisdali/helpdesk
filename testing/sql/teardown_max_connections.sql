-- Close all flood connections opened by inject_max_connections.sql.
DO $$
DECLARE
    i INT;
    conn_name TEXT;
BEGIN
    FOR i IN 1..18 LOOP
        conn_name := 'flood_conn_' || i;
        BEGIN
            PERFORM dblink_disconnect(conn_name);
        EXCEPTION WHEN OTHERS THEN
            -- Connection may already be closed.
            NULL;
        END;
    END LOOP;
END
$$;
