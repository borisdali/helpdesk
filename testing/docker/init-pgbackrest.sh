#!/bin/bash
# Runs once via Postgres's docker-entrypoint-initdb.d mechanism, on first
# container initialization only, as the postgres OS user, against the
# temporary init-phase server (still running, same PGDATA the real server
# will use afterward — same mechanism testing/docker/init-archive-command.sql
# already relies on for the plain `postgres` target, see that file's own
# comment for why a command-line -c flag would be the wrong place for
# archive_command specifically).
#
# Sets up a real pgBackRest repo + stanza + archive_command so the container
# is immediately ready for a backup to be taken. Deliberately does NOT take
# the initial backup here — confirmed live (2026-09-26) that `pgbackrest
# backup` racing against Docker's own init-phase temp-server lifecycle is
# genuinely flaky: the backup blocks on "backup begins after the next
# regular checkpoint completes", and on some runs the temp server tears down
# before that checkpoint completes, orphaning the backup with zero rows ever
# written to the repo. Reliable in every run once issued against the real,
# steady-state server instead — see the fault/test setup that takes the
# first backup as an explicit, separate step after the container reports
# healthy, not container-init.
set -euo pipefail

mkdir -p /var/lib/pgbackrest
cat > /etc/pgbackrest.conf <<'EOF'
[global]
repo1-path=/var/lib/pgbackrest
repo1-retention-full=2

[main]
pg1-path=/var/lib/postgresql/data
EOF

pgbackrest --stanza=main --log-level-console=info stanza-create

# archive_command is context=sighup: set via ALTER SYSTEM (mutable — see
# init-archive-command.sql's own comment on why this must never be a
# command-line -c flag), then pg_reload_conf() so it's live before this same
# init phase's own backup attempt runs — pgBackRest refuses to back up
# unless archive_command already invokes it ("archive_command must contain
# pgbackrest"), confirmed live during development.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    -c "ALTER SYSTEM SET archive_command = 'pgbackrest --stanza=main archive-push %p';"
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    -c "SELECT pg_reload_conf();"
