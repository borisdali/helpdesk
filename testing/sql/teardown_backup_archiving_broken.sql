--
-- aiHelpDesk fault teardown helper script.
--
-- Restore archive_command to its known-good baseline (see
-- testing/docker/docker-compose.yaml's startup command) and force one more
-- successful archiving attempt so pg_stat_archiver's last event is a
-- success again before the next test run — leaving archiving_stale=false
-- rather than requiring the next run to rely on stats-reset alone.

ALTER SYSTEM SET archive_command = '/bin/true';
SELECT pg_reload_conf();
SELECT pg_switch_wal();
SELECT pg_sleep(1);
