#!/bin/bash
#
# aiHelpDesk fault injection helper script.
#
# Terminate all pg_sleep(3600) backend sessions via pg_terminate_backend so
# that subsequent tests get a clean connection pool.  Using server-side
# termination is reliable regardless of whether the PID file from the inject
# script survived the detached docker-exec session.
psql -h postgres -U postgres -d testdb \
  -c "SELECT pg_terminate_backend(pid)
      FROM pg_stat_activity
      WHERE query LIKE '%pg_sleep%'
        AND pid <> pg_backend_pid();" \
  2>/dev/null
echo "Terminated pg_sleep flood connections"

# Clean up the PID file if it still exists.
rm -f /tmp/flood_pids.txt
