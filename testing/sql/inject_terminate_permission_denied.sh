#!/bin/bash
#
# aiHelpDesk fault injection helpder script.
#
# Injects a "terminate permission denied" scenario that triggers the Level-1
# mutation safeguard in terminate_connection.
#
# Setup:
#   1. Creates a restricted role with no pg_signal_backend privilege.
#   2. Starts a long-running privileged session (as postgres) that the
#      restricted role cannot terminate.
#
# The agent must connect using the RESTRICTED connection string printed below.
# When it calls terminate_connection against the sleeping PID, PostgreSQL will
# return pg_terminate_backend() = false, and the Level-1 safeguard should
# surface "TERMINATION FAILED" to the user instead of reporting success.

set -e

CONN_HOST="${CONN_HOST:-postgres}"
CONN_PORT="${CONN_PORT:-5432}"
CONN_DB="${CONN_DB:-testdb}"
RESTRICTED_USER="helpdesk_restricted_test"
RESTRICTED_PASS="Restricted1!"

psql -h "$CONN_HOST" -p "$CONN_PORT" -U postgres -d "$CONN_DB" <<SQL
-- Create a role with no pg_signal_backend privilege.
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$RESTRICTED_USER') THEN
    CREATE ROLE $RESTRICTED_USER LOGIN PASSWORD '$RESTRICTED_PASS';
  END IF;
END
\$\$;
GRANT CONNECT ON DATABASE $CONN_DB TO $RESTRICTED_USER;
-- Explicitly NOT granting pg_signal_backend.
SQL

# Start a superuser session that runs pg_sleep for 5 minutes in the background.
# Only a superuser can terminate another superuser's backend.
psql -h "$CONN_HOST" -p "$CONN_PORT" -U postgres -d "$CONN_DB" \
    -c "SELECT pg_sleep(300);" &
echo $! > /tmp/helpdesk_privileged_session.pid

echo ""
echo "Failure injected: terminate permission denied"
echo ""
echo "Restricted connection string:"
echo "  host=$CONN_HOST port=$CONN_PORT dbname=$CONN_DB user=$RESTRICTED_USER password=$RESTRICTED_PASS"
echo ""
echo "To reproduce the safeguard:"
echo "  1. Connect as the restricted user (see connection string above)."
echo "  2. Run get_active_connections to find the pg_sleep PID."
echo "  3. Run terminate_connection with that PID."
echo "  4. Expected: agent reports TERMINATION FAILED (pg_terminate_backend returned false)."
echo ""
echo "To tear down: run testing/sql/teardown_terminate_permission_denied.sh"
