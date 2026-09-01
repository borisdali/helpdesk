# High Availability / Disaster Recovery & Replication

How aiHelpDesk diagnoses PostgreSQL streaming-replication failures. What's certified
today and what's on the roadmap. This page is the dedicated home for a domain that
doesn't fit [FAULTTEST.md](FAULTTEST.md#6-fault-catalog)'s catalog table, which is
organized by injection mechanism (Docker, SSH, Kubernetes, pure-SQL), not by failure
domain.

## Table of Contents

1. [Replication lag](#1-replication-lag)
2. [Replica disconnection (the absent-row edge case)](#2-replica-disconnection-the-absent-row-edge-case)
3. [Roadmap](#3-roadmap)

---

## 1. Replication lag

Fault [`db-replication-lag`](https://github.com/borisdali/helpdesk/blob/5761ec983b4b3dac5de97cea48217bc764bc992f/testing/catalog/failures.yaml#L506), triage playbook [`pbs_replication_lag`](../playbooks/replication-lag-triage.yaml).

This is the case where a replica that's still connected, but falling behind. [`get_replication_status`](https://github.com/borisdali/helpdesk/blob/5761ec983b4b3dac5de97cea48217bc764bc992f/agents/database/tools.go#L1096) reads
`sent_lsn`/`write_lsn`/`flush_lsn`/`replay_lsn` gaps per replica from
`pg_stat_replication`, distinguishing a replica in `streaming` state with growing lag
(can't keep up with write volume) from one in `catchup` state (recovering from a larger
gap, expected to self-resolve). The playbook explicitly guards against the most common
false positive: a lag spike during a large batch write is normal and should not be
escalated unless it fails to recover within 15 minutes of the write completing.

Remediation (`pbs_replication_remediate`) is narrow and safe by design: it only resumes
an explicitly-paused `pg_wal_replay_resume()` and explicitly refuses to act when lag is
driven by write volume instead ("do NOT call it — it is a no-op or may error").
`external_compat: true` + `requires_replica: true` — pure libpq, runs against any
Postgres instance over `--external`, but needs a real streaming replica already
attached (see [FAULTTEST.md §10](FAULTTEST.md#10-extending-the-built-in-catalog) for
what the `requires_replica` field means and why it's separate from `external_compat`).

## 2. Replica disconnection (the absent-row edge case)

Fault [`db-replica-disconnected`](https://github.com/borisdali/helpdesk/blob/5761ec983b4b3dac5de97cea48217bc764bc992f/testing/catalog/failures.yaml#L549). This addition was prompted by a public technical
exchange about a specific, well-posed failure mode: what happens when a streaming
replica's walreceiver drops entirely, not just falls behind?

**The failure mode**: `pg_stat_replication` only lists *currently connected* replicas.
When a walreceiver disconnects, its row doesn't show degraded stats. Instead, it disappears
completely. A naive check ("query `pg_stat_replication`, see no problem rows") could
misread that absence as "no active replication issues," when the correct read is "this
replica isn't here at all."

**Why aiHelpDesk doesn't make that mistake**: `get_replication_status`
([`agents/database/tools.go`](../agents/database/tools.go)) queries both
`pg_stat_replication` *and* `pg_replication_slots` in the same call. The slots table
persists across a disconnect — an inactive physical slot with a growing gap between
`pg_current_wal_lsn()` and `restart_lsn` is exactly the signal a
`pg_stat_replication`-only check would miss. The triage guidance is explicit about this:
a replica absent from `pg_stat_replication` is its own named condition (not a lag
figure) and the correct response is `ESCALATE_TO: none` with findings recommending
manual investigation — not silence and not a fabricated "zero lag" reading.

**A second, independent backstop that doesn't depend on the model getting it
right**: the guidance above only helps if the diagnosing LLM actually follows it.
`get_replication_status` also feeds a deterministic, code-derived check
(`internal/evidence`, `agents/database/tools.go`) that reaches the same
conclusion without any LLM involved — if the primary reports zero connected
replicas *and* a replication slot is inactive with real retained WAL, the
gateway force-gates the response for human review regardless of what the
model concluded or chose to mention (`objective_evidence:replica_disconnected`,
same mechanism used for `k8s-oomkilled`'s pod-restart/OOM-kill signals — see
`agents/k8s/objective_evidence.yaml` for that side, `agents/database/
objective_evidence.yaml` for this one). A model that silently misreads the
absence as healthy can still get overridden by this check; the guidance
above is what makes the *correct* diagnosis likely, this is what makes an
*incorrect* one non-silent.

**Why there's no automated remediation for this today and why that's the honest
answer rather than a gap**: a disconnected walreceiver in a real environment can have
many different root causes starting with a network partition, replica host down, expired
credentials, a changed firewall rule, a deliberate decommission, etc. There is no single
SQL action that correctly fixes most of those, so aiHelpDesk doesn't pretend otherwise
by offering one. (A tool that specifically undoes *this fault's own* synthetic
injection mechanism was considered and rejected for exactly this reason — it would
only have been correct for a condition that exists nowhere except this test. See the
roadmap below for the version of "remediation" that's actually being considered:
escalating to a sysadmin-domain investigation of host/network/credential state, the
same cross-domain pattern already used for
[`host-container-stopped`](FAULTTEST.md#6-fault-catalog).)

**Verified, not just claimed**: the fault durably disconnects a real replica (blocks
reconnection via `max_wal_senders = 0` before terminating the existing connection —
terminating alone isn't enough, PostgreSQL's default retry interval reconnects within
~5 seconds) and fails loudly if no replica is attached rather than silently no-op'ing.
Reachable via `faulttest run --ids db-replica-disconnected --external --replica-conn
<...>` against any real environment with a streaming replica — including the bundled
`make faulttest`/`faulttest-fast` targets, which already provision one
(`testing/docker/docker-compose.repl.yaml`).

## 3. Roadmap

Not yet built — tracked, not forgotten:

- **Replica promotion / failover.** `pg_promote()` exists in the codebase today only
  as a manual step buried in the PITR-recovery playbook's guidance — there's no
  first-class `promote_replica` tool or dedicated failover playbook yet.
- **Patroni-managed HA.** Zero coverage today. Everything current talks directly to
  PostgreSQL over SQL; nothing talks to Patroni's own REST API or its DCS backend
  (etcd/Consul/ZooKeeper) for leader-election state. This is a new tool category, not
  an incremental addition to existing ones.
- **`pg_basebackup` / pgBackRest / pgbackup-job health.** Existing coverage
  (`pbs_db_pitr_recovery`) is about *restoring* from a backup after data loss, but there's presently 
  no coverage yet of backup-*taking* failures (a scheduled `pg_basebackup` job failing
  mid-run, disk exhaustion during backup, verification failures).
- **Sysadmin-domain escalation for disconnected replicas.** The genuinely useful
  "next step" for §2 above — a sysadmin playbook (+ likely new sysadmin tools) that a
  DB triage playbook could `ESCALATE_TO` for host/network/credential investigation.
  Deliberately not built as a side effect of one test fault; a real feature decision
  to be scoped properly first.

---

See also: [FAULTTEST.md](FAULTTEST.md) for the fault injection CLI and full catalog
reference, [PLAYBOOKS.md](PLAYBOOKS.md) for the playbook schema and escalation
mechanics, [CONSISTENCY.md](CONSISTENCY.md) for how a playbook earns a stability
certification before entering live rotation.
