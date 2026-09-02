# aiHelpDesk High Availability / Disaster Recovery

This document describes how aiHelpDesk diagnoses PostgreSQL HA/DR and, in particular, the streaming-replication failures. What's certified
today and what's on the roadmap. This page is the dedicated home for a domain that
doesn't fit [FAULTTEST.md](FAULTTEST.md#6-fault-catalog)'s catalog table, which is
organized by injection mechanism (Docker, SSH, Kubernetes, pure-SQL), not by failure
domain.

## Table of Contents

1. [Replication lag](#1-replication-lag)
2. [Replica disconnection (the absent-row edge case)](#2-replica-disconnection-the-absent-row-edge-case)
3. [Replica present but stalled (the reply-lag edge case)](#3-replica-present-but-stalled-the-reply-lag-edge-case)
4. [SysAdmin-domain escalation: telling a crashed, rejected, and frozen replica apart](#4-sysadmin-domain-escalation-telling-a-crashed-rejected-and-frozen-replica-apart)
5. [Roadmap](#5-roadmap)

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
Gateway force-gates the response for human review regardless of what the
model concluded or chose to mention (`objective_evidence:replica_disconnected`,
same mechanism used for `k8s-oomkilled`'s pod-restart/OOM-kill signals — see
`agents/k8s/objective_evidence.yaml` for that side, `agents/database/
objective_evidence.yaml` for this one). A model that silently misreads the
absence as healthy can still get overridden by this check; the guidance
above is what makes the *correct* diagnosis likely, this is what makes an
*incorrect* one non-silent.

**Why there's no automated *DB-level* remediation for this and why that's the honest
answer rather than a gap**: a disconnected walreceiver in a real environment can have
many different root causes starting with a network partition, replica host down, expired
credentials, a changed firewall rule, a deliberate decommission, etc. There is no single
SQL action that correctly fixes most of those, so aiHelpDesk doesn't pretend otherwise
by offering one. (A tool that specifically undoes *this fault's own* synthetic
injection mechanism was considered and rejected for exactly this reason — it would
only have been correct for a condition that exists nowhere except this test.) What
*does* exist now is a real SysAdmin-domain investigation that can tell a genuinely
crashed replica (safe to restart) apart from a healthy one being correctly rejected
(restarting wouldn't help) — see [§3](#3-sysadmin-domain-escalation-telling-a-crashed-replica-from-a-rejected-one).

**Verified, not just claimed**: the fault durably disconnects a real replica (blocks
reconnection via `max_wal_senders = 0` before terminating the existing connection —
terminating alone isn't enough, PostgreSQL's default retry interval reconnects within
~5 seconds) and fails loudly if no replica is attached rather than silently no-op'ing.
Reachable via `faulttest run --ids db-replica-disconnected --external --replica-conn
<...>` against any real environment with a streaming replica — including the bundled
`make faulttest`/`faulttest-fast` targets, which already provision one
(`testing/docker/docker-compose.repl.yaml`). `db-replica-container-stopped` (§3) is
Docker-only by design (it stops the replica's own container directly) and isn't
reachable via `--external`, but runs the same way under `make faulttest`/`faulttest-fast`.

## 3. Replica present but stalled (the reply-lag edge case)

Fault [`db-replica-stalled`](https://github.com/borisdali/helpdesk/blob/40c1d8b613f27b0bb30aa0d7488eae517f46cb32/testing/catalog/failures.yaml#L689). Prompted by external feedback on an earlier draft of this page: §2's absent-row check handles a replica that vanishes entirely, but what about one whose row is still *present* — so an absent-row check reads it as healthy — while the replica itself has silently stopped communicating?

**The failure mode**: a replica's connection to the primary can survive (TCP session intact, `pg_stat_replication` still shows a row with `state=streaming`) while the replica process itself is frozen — CPU-starved, hung, or a network partition that never tears down the socket. Byte-based lag alone doesn't catch this: `write_lag_bytes`/`flush_lag_bytes`/`replay_lag_bytes` can all read `0` (nothing new to replicate, or the gap simply hasn't grown yet), giving no signal that anything is wrong.

**Why aiHelpDesk doesn't make that mistake**: [`get_replication_status`](https://github.com/borisdali/helpdesk/blob/40c1d8b613f27b0bb30aa0d7488eae517f46cb32/agents/database/tools.go#L1202) also selects `reply_lag_seconds`, derived from `pg_stat_replication.reply_time` — how long since this replica last sent *any* feedback, independent of WAL position. A healthy connection replies roughly every `wal_receiver_status_interval` (Postgres default: 10s); a value well beyond that means the connection is present but the replica behind it has gone silent. The triage guidance (Ending C in [`replication-lag-triage.yaml`](../playbooks/replication-lag-triage.yaml)) treats this as its own condition, distinct from both Ending A (ordinary lag) and Ending B (absent entirely) — same handoff to SysAdmin as Ending B, since the primary's own side can't tell *why* the replica went quiet.

**A real, narrow constraint this fault had to be designed around**: Postgres has its own protocol-level timeout, `wal_sender_timeout` (default: 60s/1min) — if a connection goes silent for that long, the *primary itself* proactively terminates it, and the row disappears from `pg_stat_replication` entirely. Past that point this is Ending B, not Ending C, regardless of what caused the silence. That ceiling isn't optional headroom to design around loosely — it's a hard deadline. Confirmed live (2026-09-02): an earlier version of this fault paused the replica for 75s and thresholded the objective-evidence signal at 60s (matched exactly to `wal_sender_timeout`, no margin) — the primary always won that race, tearing the connection down before the signal could ever fire, so the fault only ever produced an ordinary disconnect and never tested what it claimed to. Fixed by choosing values with real margin on both sides: a 20s threshold (comfortably past a couple of missed heartbeats, nowhere near the 60s ceiling) and a 40s pause (past the threshold, 20s of margin below the ceiling).

**A second, independent backstop, same mechanism as §2's**: `get_replication_status` also feeds the `replica_stalled` objective-evidence signal (`agents/database/objective_evidence.yaml`) — deterministic, code-derived, reaches the same conclusion without any LLM involved. Confirmed (not just gated) once the model's own required `EVIDENCE` quote demonstrably cites the real `reply_lag_seconds` value.

**A real, structural parser bug found live while confirming that backstop, not specific to this signal**: the gateway's hypothesis-line parser split a model's response on every literal `" | "` it found — but a model quoting a raw `psql -x` line verbatim as its evidence (exactly what the protocol's own "verbatim quote from tool output" instruction asks for, and exactly what `reply_lag_seconds | 56` looks like) contains that same separator *inside* the quote. The naive split shredded the quote at its own internal `" | "`, truncating the parsed evidence down to just `"reply_lag_seconds"` — silently losing the actual value and making a textbook-correct diagnosis register as unconfirmed. `db-replica-disconnected`'s own confirmation happened to use a different check (`resource_named_in_quote`, reading raw response text rather than the parsed field) — which is what hid this bug until a signal using the default confirmation probe actually depended on correct parsing. Fixed by making the parser quote-aware (`splitOutsideQuotes`, `cmd/gateway/playbooks.go`) rather than working around it in just this one rule — the bug was in a code path every default-confirmation signal shares, present or future.

**Verified, not just claimed**: the fault freezes a real replica container (`docker pause` — Docker's cgroup freezer, chosen specifically because it stops the process without closing its TCP connection, unlike stop/kill) and confirms the connection genuinely survives (`pg_stat_replication` still shows the row) while `reply_lag_seconds` climbs. `requires_replica: true`, Docker-only by design (freezing a container isn't expressible over `--external`) — same category as `db-replica-container-stopped`, runs under `make faulttest`/`faulttest-fast`.

## 4. SysAdmin-domain escalation: telling a crashed, rejected, and frozen replica apart

From the primary's own vantage point, three very different real-world conditions can
look alike or ambiguous at the SQL level alone. Three faults exercise all of them:

- [`db-replica-disconnected`](https://github.com/borisdali/helpdesk/blob/ea74bcad0961ccb28e4cc662ef78078cfef4e3c7/testing/catalog/failures.yaml#L549)
  (§2): the primary rejects the connection (`pg_hba.conf`), while the replica container
  itself is healthy and running the whole time.
- [`db-replica-container-stopped`](https://github.com/borisdali/helpdesk/blob/ea74bcad0961ccb28e4cc662ef78078cfef4e3c7/testing/catalog/failures.yaml#L627):
  the replica's own container has been stopped cleanly, while the primary never rejects
  anything, there's simply nothing connected.
- [`db-replica-stalled`](https://github.com/borisdali/helpdesk/blob/40c1d8b613f27b0bb30aa0d7488eae517f46cb32/testing/catalog/failures.yaml#L689)
  (§3): the replica's own container is frozen (`docker pause`), while the connection to
  the primary is still technically intact the whole time.

All three get initially diagnosed by the DB Agent, which determines that it needs additional OS level info and so it escalates (the same way for all three) to the SysAdmin Agent: `ESCALATE_TO:
pbs_sysadmin_replica_connectivity_triage`, handing off the replica's own `host:port` via
a structured `TARGET:` signal line (not a full connection string — deliberately stripped
to `{{replica_host_port}}` so the DB Agent's own tools can't be pointed at the replica
directly. It has been determined empirically in the lab testing that giving it the full DSN and letting the DB Agent just query the
replica with its own tools instead of escalating, leads to inconsistent diagnosis and tripping the `target_drift` safeguard).

The SysAdmin hop (`pbs_sysadmin_replica_connectivity_triage`) inspects the replica's own
container state and logs — `Status`, `ExitCode`, `OOMKilled` and the actual log content
— and reaches three different, correct conclusions from the three faults above:

- Container running, logs show repeated `pg_hba.conf` rejection messages: nothing to
  restart, the replica is healthy, but refused → `ESCALATE_TO: none`.
- Container exited cleanly (`ExitCode=0`), logs show a clean shutdown with no rejection
  or corruption signal → `TRANSITION_TO: pbs_replica_restart_action`, a genuine
  remediation playbook that restarts the container and confirms it's healthy and
  streaming again before reporting success.
- Container `Status=paused` — frozen, not crashed and not exited. The connection dropped
  because nothing inside the container is executing at all, not because of anything a
  restart would fix; `pbs_replica_restart_action`'s own precondition (`exitcode=0`, clean
  shutdown logs) is never satisfied by a paused container, and no tool in the SysAdmin
  Agent's own toolset can unpause one today → `ESCALATE_TO: none`, reported for manual
  intervention rather than guessed at.

**Two real, general bugs found live while building this, both fixed in the shared
chain-escalation code, not this fault specifically:**

- A hop's `TARGET:` handoff only ever reached the *next* hop, not the whole rest of the
  chain — a third hop (the actual restart) silently fell back to the original caller's
  connection string whenever the middle hop forgot to restate it, sending the
  remediation step at the wrong server. Fixed by carrying the last-known target forward
  across the whole chain, not resetting it every hop.
- A force-mode auto-chain into a real remediation playbook still declared the top-level
  caller's original `diagnostic` purpose to its own tool calls — so a policy denying
  writes under a diagnostic purpose correctly, per its own rule, blocked the actual
  restart, even though the chain itself had already been authorized into remediation.
  Fixed by deriving each hop's declared purpose from its own playbook type.

Both fixes live in the Gateway's shared chain-escalation path, so they apply to every
triage→remediation transition in the system, not just this one.

**Live-verified**, not just unit-tested: the full 3-hop chain (DB triage → SysAdmin
triage → SysAdmin restart), replica genuinely stopped and genuinely restarted, confirmed
independently against `pg_stat_replication` (not just the agent's own self-report) after
each run.

## 5. Roadmap

Not yet built — tracked, not forgotten:

- **Unpause a frozen replica container.** §4's `Status=paused` branch correctly refuses
  to restart a frozen container (that's not what it needs) but has no tool capable of the
  actual fix (`docker unpause`) either — it can only report the finding for a human to
  act on. A narrowly-scoped `unpause_container` SysAdmin tool, mirroring
  `restart_container`'s own precondition-checked shape, is the natural next step.
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
- **K8s-hosted replica support.** `pbs_sysadmin_replica_connectivity_triage` only
  branches on `runtime=docker`/`podman` today — no `runtime=kubectl` path (mirroring
  `pbs_sysadmin_docker_inspect`'s own `ESCALATE_TO: pbs_k8s_pod_crash_triage`) exists
  yet, since no K8s-hosted replica scenario has been built.

---

See also: [FAULTTEST.md](FAULTTEST.md) for the fault injection CLI and full catalog
reference, [PLAYBOOKS.md](PLAYBOOKS.md) for the playbook schema and escalation
mechanics, [CONSISTENCY.md](CONSISTENCY.md) for how a playbook earns a stability
certification before entering live rotation.
