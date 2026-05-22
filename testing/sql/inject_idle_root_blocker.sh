#!/bin/bash
#
# aiHelpDesk fault injection helper script.
#
# Inject a two-level idle-in-transaction lock chain:
#
#   A (root)         — idle in transaction, holds row-lock on _faulttest_lock_chain row 1
#   B (intermediate) — holds row-lock on _faulttest_lock_chain2 row 1,
#                       blocked waiting for A's lock on _faulttest_lock_chain row 1
#   C, D (leaves)    — blocked waiting for B's lock on _faulttest_lock_chain2 row 1
#
# Terminating A releases the full chain: B's SELECT FOR UPDATE returns, B's
# psql exits (implicit ROLLBACK releases chain2 lock), then C and D acquire
# chain2's lock and complete. All four sessions exit cleanly.
#
# Crystal Ball gaps this fault exposes:
#   1. cancel_query(A)       — pg_cancel_backend returns true but the idle-in-transaction
#                              session has no active query; SIGINT is ignored; all locks
#                              persist unchanged.
#   2. terminate_connection(B) — B exits and C/D are temporarily freed from chain2, but
#                              A still holds the chain lock; B or any replacement
#                              immediately requeues behind A.

psql -h host.docker.internal -p 15432 -U postgres -d testdb -c "
  CREATE TABLE IF NOT EXISTS _faulttest_lock_chain  (id int PRIMARY KEY);
  CREATE TABLE IF NOT EXISTS _faulttest_lock_chain2 (id int PRIMARY KEY);
  INSERT INTO _faulttest_lock_chain  VALUES (1) ON CONFLICT DO NOTHING;
  INSERT INTO _faulttest_lock_chain2 VALUES (1) ON CONFLICT DO NOTHING;
"

# Session A: root blocker — BEGIN + UPDATE chain, then hold stdin open (idle in transaction)
{ { printf "BEGIN;\nUPDATE _faulttest_lock_chain SET id=1 WHERE id=1;\n"; sleep 3600; } \
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

echo "Injected: two-level lock chain — root A (idle-in-tx on chain), intermediate B (chain2 + blocked on chain), leaves C/D (blocked on chain2)"
