#!/bin/bash
#
# aiHelpDesk fault injection helper script.
#
# Tear down the two-level idle-in-transaction lock chain fault.

if [ -f /tmp/faulttest_lock_chain_root.pid ]; then
  kill "$(cat /tmp/faulttest_lock_chain_root.pid)" 2>/dev/null || true
  rm -f /tmp/faulttest_lock_chain_root.pid
fi

psql -h host.docker.internal -p 15432 -U postgres -d testdb -c "
  SELECT pg_terminate_backend(pid)
  FROM pg_stat_activity
  WHERE query LIKE '%_faulttest_lock_chain%'
    AND pid <> pg_backend_pid();
  DROP TABLE IF EXISTS _faulttest_lock_chain;
  DROP TABLE IF EXISTS _faulttest_lock_chain2;
"
echo "Torn down: two-level idle-in-transaction lock chain fault"
