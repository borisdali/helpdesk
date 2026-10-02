--
-- aiHelpDesk fault injection helper script.
--
-- Break WAL archiving: point archive_command at a command that always
-- fails, then force real archiving attempts so pg_stat_archiver actually
-- records the failure rather than waiting on ordinary WAL traffic.

-- Reset archiver stats first so this fault's failure is unambiguous
-- against whatever archiving activity happened earlier in this
-- long-lived container (pg_stat_archiver is cumulative since last reset
-- or server start).
SELECT pg_stat_reset_shared('archiver');

-- archive_command is context=sighup: pg_reload_conf() alone applies it,
-- no restart needed. archive_mode itself is set once at container
-- startup (see testing/docker/docker-compose.yaml) since that parameter
-- does need a restart.
ALTER SYSTEM SET archive_command = '/bin/false';
SELECT pg_reload_conf();

-- Force two real archiving attempts rather than waiting for ordinary WAL
-- traffic to fill a segment naturally.
SELECT pg_switch_wal();
SELECT pg_sleep(1);
SELECT pg_switch_wal();
SELECT pg_sleep(1);
