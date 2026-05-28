#!/bin/bash
#
# aiHelpDesk fault injection helper script.
#
# Inject a two-level lock chain where the root blocker appears ACTIVE
# (running pg_sleep inside an open transaction) rather than idle in transaction.
#
#   A (root)         — active, running pg_sleep(3600) inside a BEGIN+UPDATE
#                       transaction; holds row-lock on _faulttest_lock_chain row 1.
#                       State: active / wait_event=Timeout — looks like a slow query.
#   B (intermediate) — holds row-lock on _faulttest_lock_chain2 row 1,
#                       blocked waiting for A's lock on _faulttest_lock_chain row 1
#   C, D (leaves)    — blocked waiting for B's lock on _faulttest_lock_chain2 row 1
#
# Crystal Ball trap:
#   The unguided agent sees A as an active query blocking everything and calls
#   cancel_query(A). pg_cancel_backend sends SIGINT, pg_sleep is interrupted,
#   and the function returns true — success. But A's transaction is still open
#   (now in idle in transaction (aborted) state) and all row locks remain held.
#   B, C, D are still blocked. The agent declared victory over a false positive.
#
#   terminate_connection is the only effective action: it sends SIGTERM, closes
#   the connection, and rolls back the transaction unconditionally.

psql -h host.docker.internal -p 15432 -U postgres -d testdb -c "
  CREATE TABLE IF NOT EXISTS _faulttest_lock_chain  (id int PRIMARY KEY);
  CREATE TABLE IF NOT EXISTS _faulttest_lock_chain2 (id int PRIMARY KEY);
  INSERT INTO _faulttest_lock_chain  VALUES (1) ON CONFLICT DO NOTHING;
  INSERT INTO _faulttest_lock_chain2 VALUES (1) ON CONFLICT DO NOTHING;
"

# Session A: root blocker — UPDATE chain (acquires lock), then pg_sleep inside the
# same transaction. State shows as 'active' with wait_event=Timeout.
# application_name is set so teardown can find this session even when its
# visible query is 'SELECT pg_sleep(3600)' (no _faulttest_lock_chain in the text).
{ printf "SET application_name='_faulttest_lock_chain_root';\nBEGIN;\nUPDATE _faulttest_lock_chain SET id=1 WHERE id=1;\nSELECT pg_sleep(3600);\n" \
  | psql -h host.docker.internal -p 15432 -U postgres -d testdb; } >/dev/null 2>&1 &
echo $! > /tmp/faulttest_lock_chain_root.pid
sleep 1

# Session B: intermediate — UPDATE chain2 (acquires lock), then SELECT chain FOR UPDATE
# (blocks on A's lock while still holding the chain2 lock)
{ printf "BEGIN;\nUPDATE _faulttest_lock_chain2 SET id=1 WHERE id=1;\nSELECT * FROM _faulttest_lock_chain WHERE id=1 FOR UPDATE;\n" \
  | psql -h host.docker.internal -p 15432 -U postgres -d testdb; } >/dev/null 2>&1 &
sleep 1

# Sessions C and D: leaves — SELECT chain2 FOR UPDATE (blocked by B's UPDATE lock on chain2)
for i in 1 2; do
  psql -h host.docker.internal -p 15432 -U postgres -d testdb \
    -c "SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE;" >/dev/null 2>&1 &
done
sleep 1

echo "Injected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on chain), leaves C/D (blocked on chain2)"
