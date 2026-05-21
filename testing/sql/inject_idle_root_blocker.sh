#!/bin/bash
#
# aiHelpDesk fault injection helper script.
#
# Inject an idle-in-transaction root blocker with 3 victim sessions queued.
#
# The root blocker holds a row-level lock via UPDATE then goes idle
# (the printf pipe keeps the psql session open waiting for more SQL).
#
# Key diagnostic trap: cancel_query (pg_cancel_backend) returns true for
# idle-in-transaction sessions but does NOT release held locks — there is no
# active query to interrupt. Only terminate_connection (pg_terminate_backend)
# closes the connection and frees all locks.

psql -h postgres -U postgres -d testdb -c "
  CREATE TABLE IF NOT EXISTS _faulttest_lock_chain (id int PRIMARY KEY);
  INSERT INTO _faulttest_lock_chain VALUES (1) ON CONFLICT DO NOTHING;
"

{ printf "BEGIN;\nUPDATE _faulttest_lock_chain SET id = 1 WHERE id = 1;\n"; sleep 3600; } \
  | psql -h postgres -U postgres -d testdb >/dev/null 2>&1 &
echo $! > /tmp/faulttest_lock_chain_root.pid
sleep 1

for i in 1 2 3; do
  psql -h postgres -U postgres -d testdb \
    -c "SELECT * FROM _faulttest_lock_chain WHERE id = 1 FOR UPDATE;" >/dev/null 2>&1 &
done
sleep 1
echo "Injected: idle-in-transaction root blocker + 3 victims on _faulttest_lock_chain"
