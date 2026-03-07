#!/bin/bash
set -e

# Wait for the primary to accept connections.
until pg_isready -h postgres -U postgres; do
    sleep 1
done

# Take a base backup from the primary and configure standby.mode.
rm -rf /var/lib/postgresql/data/*
pg_basebackup -h postgres -U postgres -D /var/lib/postgresql/data -Fp -Xs -R

# pg_basebackup runs as root; postgres refuses to start on root-owned data.
chown -R postgres:postgres /var/lib/postgresql/data
chmod 0700 /var/lib/postgresql/data

# Drop privileges and start postgres.
exec gosu postgres postgres
