#!/bin/bash
#
# Fault injection: crash the PostgreSQL postmaster with SIGABRT.
#
# Runs inside helpdesk-test-pgloader. Connects to the postgres container via
# psql and uses COPY TO PROGRAM to run kill -SIGABRT against the postmaster
# PID from within the postgres container's process space.
#
# The psql connection will drop immediately when the postmaster is killed —
# the error is expected and suppressed. After injection the container will be
# in Exited state with exit code 134 (128 + SIGABRT=6).
#
# Teardown: docker compose start postgres

psql "host=postgres port=5432 dbname=testdb user=postgres password=testpass" \
    -c "COPY (SELECT 1) TO PROGRAM 'kill -SIGABRT \$(head -1 /var/lib/postgresql/data/postmaster.pid)'" \
    2>/dev/null || true

# Brief wait to let the container reach Exited state before the agent prompt.
sleep 1

echo "Postgres crash injected — container should now be in Exited state with non-zero exit code."
