#!/bin/bash
set -e

# Enable replication slots and pg_hba for the replica.
cat >> "$PGDATA/pg_hba.conf" <<EOF
host replication postgres all trust
EOF

# Create a replication user (using the superuser for simplicity).
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    SELECT pg_create_physical_replication_slot('replica_slot', true);
EOSQL
