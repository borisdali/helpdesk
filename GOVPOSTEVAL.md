# Post-Execution Policy Evaluation: Design Considerations

This document captures the design analysis behind the blast-radius guardrail and
the trade-offs involved in choosing between post-execution detection and
pre-execution prevention. It is intended as a reference for future implementors
rather than as a user-facing guide.

---

## What the Current Implementation Does

The blast-radius guardrail is evaluated **post-execution**: after a tool runs,
the agent parses the affected count from the output and re-evaluates the policy
engine with that count populated in `RequestContext`. If the count exceeds
`max_rows_affected` or `max_pods_affected`, the policy engine returns a denial,
the result is withheld from the LLM, and a `PostExecution: true` denial event is
written to the audit trail.

```
psql executes DELETE → commits → returns "DELETE 10000"
                                        │
                              parseRowsAffected → 10000
                                        │
                              CheckDatabaseResult → denied (> limit)
                                        │
                              return error to LLM
                              write PostExecution audit event
```

**What this is:** post-hoc detection with notification.

**What this is not:** prevention. By the time the check runs, the transaction
has already committed. The 10,000 rows are gone.

---

## The Rollback Problem

The obvious remediation — wrap the write in an explicit transaction and
`ROLLBACK` if the limit is exceeded — is impractical for large DML:

- A `DELETE` of 10,000 rows that takes 2 hours will take approximately the
  same time to roll back. PostgreSQL must undo every row change individually,
  traversing the undo log in reverse.
- Locks are held for the entire forward + reverse window — 4 hours of total
  lock contention for an operation that "didn't happen".
- `autovacuum` cannot reclaim dead tuples from the forward pass until the
  rollback completes, further degrading performance.
- The rollback generates the same volume of WAL writes as the original
  operation — no I/O shortcut exists.

**Conclusion:** for large, long-running DML, transactional rollback causes more
operational damage than leaving the operation to complete. Post-execution
detection with rollback is only appropriate when the operation is fast enough
that rollback is cheap (sub-second to low-second range).

---

## DDL Statements

DDL (ALTER TABLE, CREATE INDEX, DROP TABLE, etc.) breaks the blast-radius model
in two independent ways.

### Row count does not apply

`ALTER TABLE users ADD COLUMN deleted_at TIMESTAMPTZ` produces no meaningful
row count in the psql command tag. The blast expressed by DDL is structural and
temporal — schema changes disrupt application compatibility, and `ALTER TABLE`
acquires an `ACCESS EXCLUSIVE` lock that blocks all reads and writes for its
duration. `max_rows_affected` offers zero protection here.

DDL risk is better categorised by **scope of change**:

| DDL Statement | Risk Level | Notes |
|---------------|-----------|-------|
| `CREATE TABLE`, `CREATE INDEX` | Medium | Acquires lock; reversible |
| `ALTER TABLE ADD COLUMN` (nullable, no default) | Medium | Metadata-only in PG ≥ 11; fast |
| `ALTER TABLE ADD COLUMN ... DEFAULT ...` (PG < 11) | High | Full table rewrite |
| `ALTER TABLE DROP COLUMN`, `RENAME TABLE` | High | Structural; disrupts app |
| `DROP TABLE`, `TRUNCATE` | Critical | Data loss; `TRUNCATE` non-transactional in some contexts |
| `CREATE INDEX CONCURRENTLY` | Medium | Cannot run in transaction (see below) |

### Some DDL cannot run inside a transaction block

PostgreSQL DDL is transactional for most statements — unlike MySQL, you can
`ROLLBACK` an `ALTER TABLE`. However, the following **cannot run inside an
explicit transaction** at all and will error if attempted:

- `CREATE INDEX CONCURRENTLY` / `DROP INDEX CONCURRENTLY`
- `VACUUM`, `CLUSTER`
- `CREATE DATABASE`, `DROP DATABASE`
- `ALTER TYPE ... ADD VALUE` (enum extension; partially lifted in PG ≥ 12)

Any transactional wrapping strategy must detect these statements first and
refuse or route them differently — a blanket `BEGIN` / `ROLLBACK` wrapper will
cause these operations to fail entirely.

### Lock contention is the real blast radius for DDL

Even for DDL that is transactional, the `ACCESS EXCLUSIVE` lock is held for the
full duration. A rolled-back `ALTER TABLE` on a 500 GB table still blocked every
query for however long the operation ran. The rollback eliminates the schema
change but does not eliminate the operational impact. For DDL, the right
guardrail is **approval before execution**, not blast-radius detection after.

---

## Distributed Transactions and Replicas

### Standard read-replica setup (streaming / logical replication)

PostgreSQL replication only ships **committed** transactions. A `ROLLBACK` on the
primary produces no WAL entries that replicas receive — the operation simply
never exists from the replica's perspective. Transactional rollback on the
primary is sufficient; no distributed coordination is required.

Agents pointed at a read replica for writes will fail outright — replicas are
read-only in this topology, making the concern moot.

### Citus (distributed PostgreSQL)

Citus transparently uses two-phase commit (2PC) for cross-shard writes.
`BEGIN` → distributed DML → `ROLLBACK` propagates atomically to all shards via
2PC. Edge cases (coordinator crash between `PREPARE` and `COMMIT PREPARED`)
are handled by Citus's recovery path, not the agent layer.

### Multi-master / BDR (Bi-Directional Replication)

BDR replicates committed transactions between nodes. A transaction rolled back
on node A never reaches node B — that boundary is clean. However:

- **Conflict resolution** is complex in multi-master topologies; agents writing
  to different nodes in sequence can create divergence that is difficult to
  reconcile.
- **Sequences** do not participate in MVCC. Row count from a large INSERT may
  be rolled back, but sequence values consumed are not recovered.
- **DDL in BDR** requires a global DDL lock. Even a rolled-back DDL statement
  holds the global lock for its full duration — the operational impact is not
  undone by the rollback.

For databases tagged as `multi-master` or `bdr`, policy should mandate approval
for any write or destructive action rather than relying on blast-radius limits.

### Cross-database operations (independent servers)

If an agent (or an LLM orchestrating multiple agents) performs writes on two
independent database servers in sequence, there is no distributed transaction
coordinator. Rolling back server A after server B has committed requires an
explicit compensating transaction on server B — application-level saga
coordination that the current architecture has no mechanism for.

This is an application design boundary, not a database-level problem, and is
out of scope for the blast-radius guardrail.

---

## The Right Tool for Each Scenario

Post-execution detection and pre-execution prevention serve different purposes.
Neither is universally correct.

| Scenario | Right mechanism | Rationale |
|----------|----------------|-----------|
| Fast, unpredictable DML (OLTP, small tables) | Post-execution check | Count unknown ahead of time; rollback cheap if needed |
| Large or long-running DML (bulk deletes, mass updates) | Pre-execution COUNT estimate | Rollback cost equals execution cost; prevention beats detection |
| DDL of any kind | Approval workflow | Row count meaningless; lock contention is the blast regardless of rollback |
| Non-transactional DDL (CONCURRENTLY, VACUUM) | Block or require approval | Cannot be wrapped in transaction at all |
| Kubernetes mutations | Post-execution check | No transaction concept; pod/resource count unknown until kubectl returns |
| Multi-master / BDR writes | Approval workflow | Distributed conflict risk; sequences non-recoverable |
| Cross-database multi-server writes | Out of scope (saga pattern) | No coordinator; compensating transactions are application logic |

---

## Pre-Execution COUNT Estimation

For large DML, the correct blast-radius guardrail runs **before** the write
by executing the same `WHERE` clause as a `SELECT COUNT(*)`:

```sql
-- Agent wants to run:
DELETE FROM orders WHERE status = 'cancelled' AND created_at < '2020-01-01';

-- Pre-flight check (read-only, no locks held):
SELECT COUNT(*) FROM orders
WHERE status = 'cancelled' AND created_at < '2020-01-01';
-- → 10,000 rows → exceeds limit → deny; DELETE never executes
```

**Advantages:**
- No transaction, no rollback, no write locks
- The COUNT query is read-only; cancellation is safe and immediate
- Protects the database before any mutation occurs

**Limitations:**

**TOCTOU window:** Between the COUNT and the DELETE, additional rows may become
eligible (concurrent inserts, status changes). For most operational use cases
this window is acceptable. In high-concurrency environments where the window
matters, the COUNT can be run inside the same transaction as the write with an
`SELECT ... FOR UPDATE` on a sentinel row, though this is rarely necessary.

**Accuracy vs. speed:** `SELECT COUNT(*)` with the same WHERE clause visits the
same rows as the DELETE. On large tables without a covering index, this can
itself be slow. For a rough estimate, `EXPLAIN` (without `ANALYZE`) returns the
planner's row estimate instantly, though accuracy depends on statistics currency.
A two-tier approach — `EXPLAIN` for a quick check, `COUNT(*)` only if the
estimate is near the threshold — balances speed and accuracy.

**SQL parsing requirement:** To construct the COUNT query, the agent must either:
- Parse the write statement to extract the `WHERE` clause, or
- Require the LLM to submit structured write requests (table + WHERE clause as
  separate fields rather than raw SQL), or
- Require the LLM to provide a paired check query alongside each write query

Raw SQL parsing for arbitrary queries is brittle. Structured write requests or
paired queries are more robust implementation strategies.

---

## Corrected Characterisation of Blast-Radius Guardrails

The current implementation should be described as:

**Post-execution blast radius** = detection and audit trail.
Useful for Kubernetes (no transaction concept) and fast/small DML where rollback
is cheap. Does **not** prevent committed database mutations.

**Pre-execution COUNT check** (not yet implemented) = prevention.
The correct guardrail for large DML. Denies the operation before it starts.
Subject to a narrow TOCTOU window.

**Approval workflow** = the right guardrail for DDL and multi-master writes.
Not a blast-radius mechanism — a human-in-the-loop gate that operates before
any execution begins.

The statement in `AIGOVERNANCE.md` that guardrails are "hard safety constraints
that cannot be overridden" applies in full only once pre-execution COUNT
estimation is implemented. Until then, the blast-radius check for DML is
detection, not prevention.

---

## Implementation Roadmap

Items in priority order, based on risk reduction:

1. **Pre-execution COUNT estimation for DML** — highest impact; prevents large
   mutations before they start. Requires structured write API or SQL parsing.

2. **DDL classification and mandatory approval routing** — classify any DDL
   statement as `destructive` regardless of row count; route to approval
   workflow unconditionally.

3. **Non-transactional DDL detection** — detect `CONCURRENTLY`, `VACUUM`,
   `DROP DATABASE`, etc. and block them from the transactional execution path;
   require explicit approval.

4. **Tag-based escalation for distributed topologies** — policy rules matching
   tags like `multi-master` or `bdr` automatically require approval for write
   and destructive actions.

5. **Saga / compensating transaction framework** — cross-database coordination
   for multi-server write sequences. Lowest priority; significant complexity;
   only relevant once the agent layer can orchestrate multi-database writes.
