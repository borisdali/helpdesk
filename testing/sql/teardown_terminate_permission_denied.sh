#!/bin/bash
#
# aiHelpDesk fault injection helpder script.
#
# Tears down the "terminate permission denied" fault injection scenario.

set -e

CONN_HOST="${CONN_HOST:-postgres}"
CONN_PORT="${CONN_PORT:-5432}"
CONN_DB="${CONN_DB:-testdb}"
RESTRICTED_USER="helpdesk_restricted_test"

# Kill the background pg_sleep process if still running.
if [ -f /tmp/helpdesk_privileged_session.pid ]; then
    kill "$(cat /tmp/helpdesk_privileged_session.pid)" 2>/dev/null || true
    rm -f /tmp/helpdesk_privileged_session.pid
fi

# Drop the restricted role.
psql -h "$CONN_HOST" -p "$CONN_PORT" -U postgres -d "$CONN_DB" \
    -c "DROP ROLE IF EXISTS $RESTRICTED_USER;"

echo "Teardown complete: restricted role dropped, privileged session killed."
