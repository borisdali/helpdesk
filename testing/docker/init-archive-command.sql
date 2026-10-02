-- Runs once via Postgres's docker-entrypoint-initdb.d mechanism, on first
-- container initialization only. Sets archive_command's healthy baseline
-- via ALTER SYSTEM (mutable) rather than a command-line -c flag (immutable
-- for the life of the postmaster — see docker-compose.yaml's own comment
-- for why that distinction matters here). /bin/true is a synthetic,
-- always-succeeding "archive": db-backup-archiving-broken and friends only
-- care about what pg_stat_archiver reports, not a real archive destination.
ALTER SYSTEM SET archive_command = '/bin/true';
SELECT pg_reload_conf();
