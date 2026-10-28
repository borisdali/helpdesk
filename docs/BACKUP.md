# aiHelpDesk Backup & Restore

This document describes how aiHelpDesk diagnoses and, where safely possible, remediates
Postgres backup-taking failures. What's certified today and what's on the roadmap.

Backup health is table-stakes, not an advanced feature. It's kept in its own page,
separate from [HA_DR.md](HA_DR.md) (streaming replication, failover, etc.), because it's a
different concern (data durability vs. availability) with its own and growing, set of
failure modes and tools. This page is expected to expand substantially as backup
scenarios beyond WAL archiving and pgBackRest are added.

## Table of Contents

1. [WAL archiving failure (the backup-taking precondition)](#1-wal-archiving-failure-the-backup-taking-precondition)
2. [pgBackRest job-level backup health](#2-pgbackrest-job-level-backup-health)
3. [Restore from backup after data loss](#3-restore-from-backup-after-data-loss)
4. [Roadmap](#4-roadmap)

---

## 1. WAL archiving failure (the backup-taking precondition)

Fault: [`db-backup-archiving-broken`](https://github.com/borisdali/helpdesk/blob/cae8f37cc0606e5e545915b5f8fdf2ed1d9336a9/testing/catalog/failures.yaml#L388)  
Triage playbook: [`pbs_db_backup_health_triage`](../playbooks/database-backup-health-triage.yaml)  
Remediation playbook: [`pbs_db_backup_archiving_remediate`](../playbooks/database-backup-archiving-remediate.yaml)  

Other playbooks in this category deal with the failure scenarios that cover *restoring and recovering* from a backup after a data loss. A good example is the PITR scenario and its associated [`pbs_db_data_loss_triage`](../playbooks/database-data-loss-triage.yaml) playbook, part of the "Database Down" [playbook chain/graph](PLAYBOOK_OPS.md#12-understand-the-db-down-escalation-chain). This scanrio however is different. It covers backup-*taking* failures, which is really the precondition for both PITR and most other base-backup strategies that actually depend on it.

**The failure mode**:   
There are multiple reasons for a backup to fail. This particular one deals with the `archive_command` silently failing due to a broken script, a bad path, a permissions change, a full disk at the archive destination or similar reasons. Nothing in a routine health check may surface it. The database itself is unaffected and keeps serving traffic normally and the only symptom is that the WAL archive, which a recovery would need later, has silently stopped growing.

**Why aiHelpDesk doesn't make that mistake**:   
[`get_backup_status`](https://github.com/borisdali/helpdesk/blob/2a597237643e386a0b0920ac9e58437107bf1087/agents/database/tools.go#L1414) reads Postgres's own `pg_stat_archiver` view directly. In particular, the `archived_count`, `failed_count`, `last_archived_wal`/`time`, `last_failed_wal`/`time`, plus `SHOW archive_mode` and compute a single `archiving_stale` signal in SQL. It's `true` only when the most recent archiving event was a failure that hasn't since been followed by a success.  

This is a deliberate, narrower read than `failed_count > 0`: `pg_stat_archiver` is a cumulative counter since the last stats reset, so an
old failure that was retried and later succeeded is not evidence anything is currently wrong,
only an unresolved *most recent* failure is. The triage playbook is explicit about this
distinction and about a second one: `archive_mode=off` (archiving never configured) is a policy
question for the customer, not a diagnosis — it must never be reported the same way as a
genuine failure.

**A second, independent backstop that doesn't depend on the model getting it right**:   
Same [objective evidence](OBJECTIVE_EVIDENCE.md) mechanism used throughout: In this particular case it's the `backup_archiving_stale` [condition](../agents/database/objective_evidence.yaml), gated on `archive_mode=="on"`, so a deployment that never configured archiving can never trip it, force-gates the response for human review if the model's own conclusion doesn't account for a genuinely stale archiver.

**What's honest about the remediation, not oversold**:   
`set_archive_command` applies an explicit, caller-supplied value, i.e. it never guesses a replacement command. The remediation
playbook's own first move is to look for a pre-failure reading of `archive_command` via
`get_saved_snapshots` (the tool built for exactly this, see
[PLAYBOOK_OPS.md §1.1](PLAYBOOK_OPS.md#11-schedule-regular-baselines-required-for-db-down-diagnosis)
for the scheduled-baseline job this depends on), filtered to a snapshot recorded *before* the
failure began — reading the most recent snapshot blindly risks picking up a value already
captured after the break, if `get_pg_settings` happened to be called again during this same
incident's own triage.  

If no pre-failure reading exists (no scheduled baseline configured), the
playbook stops and asks a human for the value rather than inventing one. This playbook also
does not take a fresh base backup or touch anything beyond `archive_command` itself — a failure
caused by something else (disk full at the destination, a genuinely different problem) is
explicitly named as out of scope in its own escalation criteria.

**Verified, not just claimed**:   
`archive_mode=on` is set once at container startup
(`testing/docker/docker-compose.yaml`); `archive_command`'s own healthy baseline is set via
Postgres's `docker-entrypoint-initdb.d` init-script mechanism
(`testing/docker/init-archive-command.sql`), not a command-line flag — a real bug found live
during development: a parameter supplied via a command-line `-c` flag at postmaster start is
immutable for that process's lifetime and `ALTER SYSTEM` can never override it, which silently
no-op'd the fault's own injection SQL until this was caught and fixed. Confirmed end-to-end
against a live container: baseline healthy → injection → real `archive_command` change,
`failed_count` climbing, `archiving_stale=true` → teardown → restored, a fresh success
recorded, `archiving_stale=false` again.

**An architectural incompatibility found testing against a real CloudNativePG cluster**:   
This fault and `set_archive_command` are `external_compat: true`, i.e.  structurally portable pure SQL, working against any Postgres reachable over libpq. Tested
against a real CNPG-managed cluster to confirm that in practice, not just in theory and it...
genuinely does not work there. The reason?   

The CNPG `app` user isn't a superuser, so the first attempt failed with
`permission denied for function pg_stat_reset_shared` / `permission denied to set parameter
"archive_command"`, which is a plausible, privilege-shaped explanation.  

Enabling CNPG's `enableSuperuserAccess` and retrying with real `postgres` superuser credentials (confirmed via
`usesuper=t`) failed identically, but with a different error:  

  `could not open file "postgresql.auto.conf": Permission denied`.   

Checking directly inside the Pod reveals...   
... the file `ALTER SYSTEM` writes to postgresql.auto.conf and its mode is set to `-r--------` (400, read-only even for its own owning OS user).   

Not only that but the container runs with `readOnlyRootFilesystem: true`.   

This is clearly CNPG's deliberate design where Postgres configuration is strictly reconciled from the `Cluster` CR itself... and the file is locked down specifically so
nothing, not even a real superuser, can write to it out-of-band and drift from what the operator expects.   

So this is not a privilege problem at all. No privilege level change can fix it.

**Practical implication**: `db-backup-archiving-broken`'s fault injection and
`set_archive_command`'s remediation are both architecturally incompatible with CNPG as written —
this isn't a gap in test coverage, it's a real limitation of the *capability* against any
CNPG-managed customer deployment. The correct way to change `archive_command` on CNPG is a
`Cluster` CR write (a K8s API operation reconciled by the operator), not SQL — the same shape of
conclusion as §2's CNPG/pgBackRest finding, for an entirely different, independent reason. A
CNPG-native remediation path (patching the `Cluster` CR instead of running `ALTER SYSTEM`) is a
distinct, unbuilt future capability, not an extension of `set_archive_command` itself.

## 2. pgBackRest job-level backup health

Fault: [`db-pgbackrest-repo-unreadable`](https://github.com/borisdali/helpdesk/blob/cae8f37cc0606e5e545915b5f8fdf2ed1d9336a9/testing/catalog/failures.yaml#L449)  
Triage playbook: [`pbs_pgbackrest_health_triage`](../playbooks/pgbackrest-health-triage.yaml)  
Remediation playbook: [`pbs_pgbackrest_backup_remediate`](../playbooks/pgbackrest-backup-remediate.yaml)  

The WAL-*archiving* failure scenario above covers half of the backup-*taking* precondition, but says nothing about whether an actual backup-*taking* **job** (pgBackRest) is itself healthy. Things can and in reality, do often go south, be it a stale schedule, a broken repo or backups that were never configured in the first place.

**The failure mode**:   
pgBackRest's own repo/stanza becomes unreadable: permissions, ownership or a mount problem at the repo destination. This in turn leads to the next scheduled backup silently fail to run or the repo state itself becomes unqueryable.  

Distinct from the failure mode above: WAL archiving can be perfectly healthy (Postgres happily shipping segments) while the separate backup-taking job that actually produces a restorable base backup has stopped working entirely or never ran.

**Why aiHelpDesk doesn't make that mistake**:   
[`get_pgbackrest_status`](../agents/sysadmin/tools.go) runs `pgbackrest info --output=json` and distinguishes three genuinely different conditions that
all look ambiguous without a tool:   
  - `status_code != 0` (the repo/stanza itself is broken, e.g. a filesystem/permissions problem pgBackRest can't see past, sometimes reported under a stanza name of literally `"[invalid]"`, with the real diagnostic detail in the more specific
`repo_status_message` rather than the generic top-level one)   
  - `backup_stale=true` with `status_code=0` (the repo is fine but the most recent successful backup is older than the expected schedule — a retention/scheduling problem, not a filesystem one) 
  - no backup ever recorded at all (a policy question for the customer, not a technical failure, which actually matches §1's own `archive_mode=off` precedent of never reporting a policy choice as a diagnosis)  

This is deliberately SysAdmin-domain, not Database-domain: `pgbackrest info` is a host-level CLI
invocation, not a SQL query, so it lives on the agent that already owns host-level command
dispatch. And pgBackRest just so happened to be the first consumer of that agent's SSH-or-local remote-host-reach capability
(`agents/sysadmin/sshexec.go`), needed because a production pgBackRest repo host is much less
likely to be colocated with the agent process than a Docker test target.

**A second, independent backstop that doesn't depend on the model getting it right**:   
Same [objective evidence](OBJECTIVE_EVIDENCE.md) mechanism used throughout: In this particular case it's the `pgbackrest_backup_unhealthy` [condition](../agents/sysadmin/objective_evidence.yaml), which flips to `true` on a non-OK stanza status, a stale backup or no backup ever recorded, force-gates the response for human review if the model's own conclusion doesn't account for it.

**What's honest about the remediation, not oversold**:   
`run_pgbackrest_backup` is deliberately narrow as it exists only to remediate a *stale* backup on an otherwise-healthy repo by taking a
fresh one, the same action a human would take, no value-guessing involved. Unlike §1's
`set_archive_command` (which has a confirmed-correct prior value to restore, via
`get_saved_snapshots`), a broken repo/stanza has no equivalent "known-good setting" to put back —
permissions, ownership and mount problems need human investigation, not an automated retry. The
remediation playbook's own first step re-checks `status_code` and refuses outright to run a
backup against a repo that isn't genuinely healthy and its final step re-verifies via
`get_pgbackrest_status` again rather than trusting the backup command's own exit code alone.

**Verified, not just claimed**:   
Real pgBackRest 2.59.1 installed into a disposable test container
(`testing/docker/docker-compose.pgbackrest.yaml`, which is deployed deliberately as a separate compose service and
Postgres target from §1's. This is partially because pgBackRest refuses to run unless `archive_command` itself invokes it,
which conflicts directly with §1's own `archive_command` fault injection on the same target, which then results in
`ERROR: [068]: archive_command '/bin/true' must contain pgbackrest`).   

The fault's full inject → observe → teardown cycle has been tested against the real container, not just unit-tested
against recorded JSON, including the exact `su postgres -c '...'` dispatch form the tools
themselves use. Two specific bugs have been closed this way before they could ship silently broken:   
  - a broken repo reports its stanza under the literal name `"[invalid]"`, which an early exact-name lookup would have missed entirely
  - `pgbackrest backup` (unlike `pgbackrest info`) has no auto-detect-the-only-stanza behavior and fails outright without an explicit `--stanza=` flag, requiring `run_pgbackrest_backup` to resolve the stanza name itself via the same lookup
`get_pgbackrest_status` already does, rather than assuming pgBackRest would infer it the same way `info` does.

**Integration path, also verified, not just the unit level**:   
This fault is the first one whose diagnosis playbook is a first-hop `sysadmin_agent` playbook. Indeed, every prior SysAdmin-domain
playbook was only ever reached via `ESCALATE_TO` chaining from a database-agent hop, which
carries target context forward automatically.   

Tracing the actual live-call path (rather than just re-checking unit coverage) surfaced three wiring issues that only exist at that layer,
all fixed:   
  - a missing test-infra entry for the pgBackRest target (the SysAdmin Agent's target-resolution fallback had nothing to match against) 
  - a shared prompt-assembly helper that hardcoded the wrong tool hint for every SysAdmin playbook except one   
  - a fabrication-detection keyword classifier that didn't recognize "backup" as a write action  

Each of these issues would have silently degraded a live run without ever failing a unit test.

**A fourth, more serious issue found writing this tool's own documentation**:   
`run_pgbackrest_backup` called no `policyEnforcer.CheckTool` at all, unlike `restart_container`/`restart_service`, a real
write action bypassed operating-mode enforcement and tag-based policy rules entirely, with only
the remediation playbook's own `approval_mode: manual` standing between a proposal and execution.  

This was found by checking the actual code against a doc paragraph about to claim otherwise.
Fixed by adding the same `CheckTool` call the two restart tools already make, with `ActionWrite` in place of `ActionDestructive`. 
A policy-denial regression test (`TestRunPgBackRestBackupTool_PolicyDenied`) confirms the check actually fires.

**The SSH dispatch path itself, also live-verified against a real remote host, not just a synthetic one**:   
Every prior test of `agents/sysadmin/sshexec.go` (the agent's SSH-or-local remote-host-reach
capability referenced above) ran against an in-process synthetic SSH server — real enough to
prove the Go client logic, but not real `sshd`. A second, genuinely remote-reachable container
(real `openssh-server`, a pinned host key, `testing/docker/docker-compose.pgbackrest.yaml`'s own
`15436:22` mapping) surfaced three bugs a synthetic server never could:   
  - the upstream Postgres image ships its home directory world-writable, which `sshd`'s
`StrictModes` silently rejects a key against — no error, just "Permission denied"   
  - the OS package auto-generates RSA/ECDSA host keys alongside the one actually pinned, so a
real SSH client's default algorithm negotiation could pick a key the pinned `known_hosts` entry
never matches, "knownhosts: key mismatch," even though the right key was present and correct   
  - reusing the same connection-string port across two `db_servers` entries (one for
docker-exec, one for SSH, same underlying container) would have made target resolution
genuinely ambiguous for a real agent session, not just a hypothetical   

With those fixed, the SSH path was verified at every layer: raw `ssh` against the pinned
`known_hosts`, then a full live run through the real gateway/playbook/LLM pipeline
(`faulttest run --agent-conn pgbackrest-db-ssh --remediate --gate-escalation`) reaching the
identical correct diagnosis the docker-exec path reaches, with `objective_evidence_confirmed`
firing. A permanent regression test now guards this path going forward —
`agents/sysadmin/pgbackrest_ssh_integration_test.go` (`make integration`, skips cleanly if the
container isn't up) — rather than relying on this live run being repeated by hand.

**A Kubernetes-dispatch safety bug, found auditing cross-platform coverage, not reported by a
user**:   
Every *other* SysAdmin-domain tool (`check_host`, `restart_container`, …) explicitly
checks `host.K8sPodSelector` and dispatches via `kubectl exec` when a target resolves to a
K8s Pod. `get_pgbackrest_status`/`run_pgbackrest_backup` didn't — a K8s-configured target
silently fell into the SSH-or-local `default` branch and ran `pgbackrest` as a bare local command
on the *agent's own host*, not an error and not the real target. Confirmed live with a capturing
test runner before the fix (`cmdRunner.Run` called with `name="pgbackrest"` against a K8s-only
`infraConfig` entry). First fixed by refusing loudly rather than guessing; then genuinely closed
by routing the K8s case through `execInProcess`'s existing `kubectl exec` branch (already shared
by `check_host`/`restart_container`) — the same `su postgres -c '...'` wrapping the docker/podman
path already uses, not new plumbing.  

This only helps a **self-managed** Postgres Pod (a plain
`Deployment`, pgBackRest installed in the image) — an operator-managed one like CloudNativePG,
which doesn't run `pgbackrest` inside its Pod at all, now fails with a real, honest "command not
found" from inside that Pod instead of a hardcoded refusal naming one specific operator. See
[§3](#3-roadmap) for the separate, still-open question of operator-managed (CNPG-style) backup
visibility, which this change does not address.

**A parsing bug found live against an actual K8s Pod**:  

And an initial wrong hypothesis, corrected before shipping a fix that wouldn't have worked. 

Deploying a self-managed Postgres+pgBackRest Pod to a real K8s cluster to prove the `kubectl exec` path above (not
just unit-test it) surfaced `get_pgbackrest_status: parsing pgbackrest info JSON: invalid
character 'P' looking for beginning of value`.  

First hypothesis: `execRunner.Run`'s `cmd.CombinedOutput()` merges stdout and stderr for every dispatch mode and K8s
auto-injects a `<SERVICE_NAME>_*` env var per Service in the namespace — pgBackRest warns about
any that resemble its own `PGBACKREST_*`-prefixed options, so a Service literally named
`pgbackrest-demo` produced several. That fix (separating stdout/stderr capture in
`execRunner.Run` and `sshRun`, kept regardless — it's a real improvement on its own merits) did
**not** resolve the failure.   

Checking both streams independently, live, rather than trusting the
first plausible explanation: the WARN lines are on **stdout**, not stderr — pgBackRest writes
them there itself, ahead of the real JSON array, on an otherwise healthy run. The actual fix is
`jsonArrayPrefix` (`agents/sysadmin/tools.go`): locate the first `[` in the raw output before
unmarshaling, rather than assuming the whole string is JSON. Verified fixed against the real Pod
with the Service-link env-var injection deliberately left in place (not worked around) — a
regression test captures the exact live preamble.

## 3. Restore from backup after data loss

Fault: [`db-pgdata-corrupted`](../testing/catalog/failures.yaml) (search for the fault ID, the
file is not short)  
Triage playbooks: [`pbs_db_data_loss_triage`](../playbooks/database-data-loss-triage.yaml) →
[`pbs_sysadmin_host_triage`](../playbooks/sysadmin-host-triage.yaml) →
[`pbs_pgbackrest_health_triage`](../playbooks/pgbackrest-health-triage.yaml)  
Remediation playbook: [`pbs_pgbackrest_restore_remediate`](../playbooks/pgbackrest-restore-remediate.yaml)  

§§1–2 above both answer "is backup *healthy*?" This section answers a different, more severe
question: the data directory itself is gone or corrupted (missing `pg_control`, "could not find
the database system") and the instance won't start at all. §1/§2's R/O checks don't apply because 
there's nothing left to query. Recovery means overwriting the current data directory with a
backup, which is the most destructive operation in this project. v0.31 scopes this to the
narrowest real slice: restore to the latest available pgBackRest backup, a single standalone
instance, no point-in-time target. Replica rebuild after promotion and non-pgBackRest restore
are explicitly out of scope — see [§4](#4-roadmap).

Restoring to a *chosen moment* instead (a table dropped at 14:05) is a separate tool and playbook pair
since v0.33, `restore_to_point_in_time`, with its own safety model: see [Point-in-time recovery](PITR.md).

### **Three layers stand between a diagnosis and `restore_from_backup`**   
These additional three layers of safegards come on top of the general
[AI Governance](AIGOVERNANCE.md) that every playbook runs already (which includes the Policy Engine, [Informed Consent](INFORMED_CONSENT.md) gates,
[Evidence-Provenance](OBJECTIVE_EVIDENCE.md) checks, the [`trust_not_earned`](CONSISTENCY.md) certification gate):

1. [**Each hop decides only what it can see**](#layer-1-each-hop-decides-only-what-it-can-see):  
   The chain is split so no agent ever has to infer a condition it has no tool to observe.
2. [**The backup-health gate is deterministic code, not model prose**](#layer-2-the-backup-health-gate-is-deterministic-code-not-model-prose):  
   The `pgbackrest_backup_unhealthy` objective-evidence signal force-gates the chain, even under `approval_mode=force`.
3. [**Fails closed by design, not by accident**](#layer-3-fails-closed-by-design-not-by-accident):  
   A restore needs an explicit, narrow conjunction of findings and a silence or ambiguity flat out stops the chain.

Only Layer 2 is fully independent of the model. Layers 1 and 3 are carried out through
LLM-interpreted playbook guidance (see [the honest limit](#layer-3-fails-closed-by-design-not-by-accident)
under Layer 3). Underneath all three, the tool itself refuses to run against a live instance
or onto a WAL chain it can't confirm reaches the end of the archive (v0.32, see
[WAL continuity](#wal-continuity-the-restore-refuses-a-chain-it-cant-finish)) and pgBackRest
refuses to restore without a selectable backup. See
[Underneath the layers](#underneath-the-layers-whats-honest-about-the-remediation-not-oversold) below for details.

**The failure mode**:  
A missing/corrupted `pg_control` file (disk corruption, an accidental delete, a bad volume
restore) leaves Postgres unable to start at all and the error message is `postgres: could not find the database
system`. This is distinct from every scenario in §1/§2: the database isn't slow or
misconfigured, it's *gone* and the only way back is restoring from a backup rather than fixing a
setting.

#### 👉 Layer 1: each hop decides only what it can see

The chain above exists because no single agent holds every tool the decision needs.   

The Database Agent (`pbs_db_data_loss_triage`) has no tool to read Docker/Podman container logs. The SysAdmin Agent
has no tool to verify Postgres backup health. Rather than having each hop guess at what it can't
see, each one **front-loads every condition it can determine without the evidence the next hop is
about to fetch** (single instance? pgBackRest in use? PITR already decided?) into its own
`FINDINGS`, so the next hop only has to judge the one condition it's actually equipped to answer
and can act on the combined picture directly.   

**No bouncing back for a decision already made**. An earlier revision of this chain *did* bounce `pbs_sysadmin_host_triage` back to
`pbs_db_data_loss_triage` before transitioning onward, adding two full LLM round-trips for zero
safety benefit once it was clear none of the static conditions needed fresh evidence to answer (which was caught in review before it shipped, not after a slow live run).

#### 👉 Layer 2: the backup-health gate is deterministic code, not model prose

The only layer that doesn't depend on the model getting it right and the one that actually matters most for a destructive op: `pbs_pgbackrest_health_triage`'s `get_pgbackrest_status` call carries the exact same
`pgbackrest_backup_unhealthy` [objective-evidence signal](OBJECTIVE_EVIDENCE.md) as §2 and here
it does real work: `pbs_pgbackrest_restore_remediate` is only ever reached once that hop's own
`TRANSITION_TO` fires, which should only happen when the backup is genuinely confirmed healthy.  

If the model's response claims "healthy" but the tool's own structured result says otherwise, the
signal goes unconfirmed and the gateway forces a `pending_gate`. The important part is this: **it cannot be bypassed by
`approval_mode=force`**.   

That is, `cmd/gateway/playbooks.go`'s own force-gate mechanism exists specifically
to override the caller's force-mode auto-chain intent when evidence contradicts the model's
conclusion. The single most important precondition for this op — is there actually a healthy
backup to restore from — is backed by a deterministic Go code path, not left to the model's prose.

#### 👉 Layer 3: fails closed by design, not by accident

We found this during live debugging of something else entirely, confirmed while weeding out three separate live bugs during this chain's own testing:  
- a stdio-buffering race that delayed when a crash message reached the log stream  
- a log-request window too small once prior test cycles had piled up legitimate noise   
- `get_host_logs` silently discarding stderr (where Postgres's own fatal startup message actually lives)  

Each of these bugs independently caused the diagnosis to miss the real corruption signature. In every one of those three cases, the diagnosis was wrong or
incomplete and in every one, nothing destructive happened anyway. That's not luck.  

`pbs_sysadmin_host_triage`'s own guidance only sets `FINDINGS: data_loss_restore_candidate=true` on a narrow, explicit conjunction — the exact
`"could not find the database system"` signature *and* `single_instance=true` *and*
`backup_tool=pgbackrest` *and* `pitr_decided=false` *and* "nothing in these logs suggests damage
beyond what a plain restore would fix." Every branch where that conjunction doesn't hold says, verbatim, **"Do NOT escalate... needs a human DBA."**   

`pbs_pgbackrest_health_triage` only transitions onward to `pbs_pgbackrest_restore_remediate` if that exact marker was set upstream.
Otherwise its own guidance is `ESCALATE_TO: none`. Composed with the gateway's own chain-escalation mechanism,
which requires an *explicit, positive* `TRANSITION_TO`/`ESCALATE_TO` signal to continue at all and
never proceeds on silence or ambiguity, the result is that an uncertain or under-evidenced model
doesn't need to actively decide *not* to recommend a restore. It just never produces the signal
that would start one and the chain stops there by default.

**The honest limit of this one, stated with the same care as everything else here**: this is enforced
through LLM-interpreted playbook guidance, not a deterministic Go-level check the way the backup-health
gate above is. The three live bugs that exercised this path were all cases of the model being
appropriately *conservative* under incomplete evidence — it correctly never escalated. None of them
tested the opposite failure mode: a model *confidently and wrongly* concluding corruption when there
isn't any, then escalating on a false positive. That's a different, harder gap this property does not
close and nothing here should be read as claiming it does.

#### What this does *not* cover, said plainly rather than left implicit

- `pbs_db_data_loss_triage` and `pbs_sysadmin_host_triage` have no dedicated objective-evidence
  signal of their own — `get_host_logs` returns raw text, not a structured result with a named
  probe the way `get_pgbackrest_status` does. Their findings (single instance? does the log text
  suggest damage beyond what a plain restore fixes?) rest on the model's own quoted-evidence
  discipline, not a deterministic check — the fail-closed property above narrows the blast radius of
  that gap considerably, but, per the honest limit just stated, does not close it.
- `restore_from_backup` itself doesn't re-verify `backup_stale` at the Go level — it relies on
  the remediation playbook's LLM-mediated re-check plus the force-gate above. (Since v0.32 it does
  refuse on a non-zero `status_code` and on any WAL-continuity status other than `continuous`,
  see [WAL continuity](#wal-continuity-the-restore-refuses-a-chain-it-cant-finish).) As of v0.31.x, Step 1's own
  `backup_stale=true` precondition is an explicit, named stop (cite the staleness, do not
  transition or escalate automatically, defer to a human) rather than unguided model judgment —
  found live (2026-10-11) to matter: the earlier, vaguer wording let the model either invent a
  nonexistent escalation target or decide on its own to restore from a stale backup anyway, both
  without ever surfacing the decision to a human. This is still prose discipline, not a Go-level
  check.
- WAL that never reached the archive is only recovered if it is still in the dead instance's own
  `pg_wal` (since v0.32, see [The unarchived pg_wal tail](#the-unarchived-pg_wal-tail-commits-on-disk-but-not-in-the-archive)).
  When `pg_wal` is gone with the volume, a restore-to-latest still ends at
  `wal_continuity.archive_end_time` and nothing decides for a human whether that loss is
  acceptable.

#### WAL continuity: the restore refuses a chain it can't finish

*Added in v0.32.* A restore is only as good as the WAL behind it. `pgbackrest --delta restore`
lays down the latest backup and Postgres then replays archived WAL from that backup's start to
the end of the archive, following timeline history to the newest timeline. If a segment on that
path is missing, recovery either can't reach a consistent state at all (Postgres won't start) or
silently stops at the gap and comes up with older data and in both cases PGDATA has already been
overwritten. Backup age (`backup_stale`) says nothing about this: a fresh backup can sit on a
broken chain.

`get_pgbackrest_status` now returns a `wal_continuity` block, computed from the repo alone, with no
live instance needed:

1. The backup's own WAL start/stop segments and LSNs, from `pgbackrest info`.
2. The newest timeline, from the archive's `.history` files and that file's contents: every
   ancestor timeline and the LSN where each one forked (`pgbackrest repo-get`).
3. The segment files on the timelines along that path (one `pgbackrest repo-ls`, filtered to
   those timelines).
4. The WAL segment size, from the page header of the smallest of those segments
   (`pgbackrest repo-get`).

It then walks the segments in the order Postgres would read them: up to each fork from the
parent timeline, from the fork's segment onward from the child. `.partial` segments don't count,
since `restore_command` never fetches them.

The walk depends on the WAL segment size: it's 16 MiB by default, but `initdb --wal-segsize` can
set any power of two from 1 MiB to 1 GiB and the size decides which segment name follows which
(at 64 MiB, `…0000003F` is followed by the next logical id's `…00000000`). pgBackRest doesn't
record it in `pgbackrest info`, `archive.info`, `backup.info` or the backup manifest and the usual
sources (`SHOW wal_segment_size`, `pg_controldata`) need the data directory a restore is about to
replace. So it comes from two places that must agree:

- **The segment's own page header.** Every WAL segment starts with a header recording the
  cluster's segment size (`xlp_seg_size`), in a layout unchanged since Postgres 8.3. The check
  fetches the smallest gz, bz2 or uncompressed segment on the path (usually a few KB compressed)
  and decodes it; `repo-get` decrypts but doesn't decompress. lz4 and zst segments aren't read.
- **The backups' LSNs.** Each backup's start/stop LSN and segment name fit only one size, unless
  every backup starts in the first 1 MiB of a 4 GiB logical WAL id.

If the header contradicts the LSNs, the status is `unverified`. If no header could be read, the
LSNs decide and if they're ambiguous too, Postgres's 16 MiB default is assumed. That fallback
fails safe: a wrong size only makes real segments look missing, never a broken chain look
complete. `segment_size_source` (`wal_header`, `backup_lsns` or `default`) says which applied.

The result:

| `status` | Meaning |
|---|---|
| `continuous` | Every segment from the backup to the archive end is present. |
| `broken` | A segment is missing (`gaps` lists them; `consistency_reachable=false` when it's one the backup itself needs, `recoverable_to_wal` is where recovery would stop) or the backup isn't on the newest timeline's history. |
| `unverified` | The check couldn't run, e.g. pgBackRest older than 2.26 (no `repo-ls`). |
| `no_backup` | There is no backup to check. |

Three things make use of it:

- **`restore_from_backup` refuses unless the status is `continuous`**, `unverified` included.
  This is a Go-level check next to the `pg_isready` refusal, independent of any model or gate. It
  runs after the policy check and `pg_isready`, immediately before the restore.  

- **A new objective-evidence signal, `pgbackrest_wal_chain_broken`,** force-gates a response
  when the chain is definitely `broken` (not on `unverified`, so an older pgBackRest doesn't gate
  every routine triage). It's listed ahead of `pgbackrest_backup_unhealthy`, since only the first
  matching rule per result is recorded and a broken chain is the more severe finding.  

- **The playbooks.** `pbs_pgbackrest_health_triage` (v1.2) has a new Ending E for a broken chain,
  a human escalation and only transitions to the restore when the chain is `continuous`.
  `pbs_pgbackrest_restore_remediate` (v1.7) has a matching precondition and reports the recovery
  point Postgres actually reached (below).  

- **Checking the restore afterwards.**   
  The check above is a prediction.   
  `restore_from_backup` records it on success (`prediction`: the last archived segment and `latest_timeline + 1`) and
  after the restart `verify_restore_recovery` reads Postgres's own recovery lines (the last segment restored before `selected new timeline ID`, `redo done at <LSN>`) and compares:  
    - `verified`,   
    - `mismatch` (signal `restore_recovery_mismatch`),   
    - `not_found` (no completed recovery in the log yet) or   
    - `unverified` (no prediction recorded, e.g. after an agent restart).   

  👉 The prediction never comes from the model.   
  It reads container logs first, then the Postgres log file. The location of the log file is OS specific, e.g. on a Debian/Ubuntu VM the recovery lines are only in `/var/log/postgresql/`, not the
  journal.   

  Here's an example of the live baseline (real Ubuntu VM, 2026-10-18, compared by hand before the automation tool existed): predicted `0000001B000000000000000C` and timeline 30; Postgres restored `…0C`, `redo done at 0/C000138`, selected timeline 30.   

  Now, the first run through the agent (same VM, 2026-10-19, full `db-pgdata-corrupted-vm` chain with three approved gates): `restore_from_backup` predicted `0000001F000000000000000E` and timeline 34 and `verify_restore_recovery` returned
  `verified`: "recovery replayed through `0000001F000000000000000E` (redo done at 0/E000138) and selected timeline 34, as predicted".  

  The key here is that the final FINDINGS reported *that* LSN, not the prediction. It always does.

**Verified, not just claimed**: the unit tests run against real output captured from the
Docker fixture's repo, an archive that had been through 29 timeline switches from earlier restore
tests, with `.partial` segments at several of them. Removing segments from that real listing
gives each `broken` case. An integration test
(`TestIntegration_RestoreFromBackup_RefusesOnRealWALGap`) moves the backup's own WAL segment out
of the real repo, crashes the instance and checks that `restore_from_backup` refuses and a canary
file in PGDATA survives.

The check is also tested against Postgres itself. `TestIntegration_WALCheck_PredictionMatchesRealRecovery`
hides one segment at a time (one from before the backup, the segment right after it, the newest
one), asks the check where recovery would stop, then runs a real `pgbackrest restore` and reads
Postgres's log for the last segment it actually restored. All three agree, including the case
the check gets right and a backup-age view never could: the newest segment of a timeline that
later forked is lost WAL, not the archive simply ending one segment earlier. (Those restores set a
recovery target past any WAL, so Postgres stops instead of promoting and the shared archive
gains no new timeline.) The real crash-and-restore test now also checks Postgres's log against
the check: the last segment restored is `archive_end_wal` and the new timeline is
`latest_timeline + 1` and that `verify_restore_recovery` reports `verified` from the container's
real log.

Fault: [`db-pgbackrest-wal-gap`](../testing/catalog/failures.yaml) hides the latest backup's own
start segment on a healthy instance and expects the health triage to report the broken chain
(signal `pgbackrest_wal_chain_broken`) and hand the decision to a human.

#### The unarchived pg_wal tail: commits on disk but not in the archive

*Added in v0.32.* Postgres's analog of Oracle's online redo logs is the tail of `pg_wal`: the
segment being written and any completed ones still marked `.ready` because archiving lags or is
broken. Those commits are on disk but not in the archive. `pgbackrest --delta restore` removes
everything the backup's manifest doesn't list, `pg_wal`'s contents included.

👉 Found by a canary test on the Docker fixture (2026-10-19), before the fix:
  - archiving broken (`archive_command=/bin/false`), row A committed into a segment then left
    `.ready`, row B into the open segment, then the `db-pgdata-corrupted` crash (`pg_control`
    deleted) and `restore_from_backup`'s own script;
  - `pg_wal` went from 5 segments to empty, recovery ended at the archive end
    (`redo done at 0/8000138`) and the table holding both rows didn't exist.

`restore_from_backup` now does what the Postgres documentation describes for archive recovery:

- **Before the restore**, it moves the segment files in `pg_wal` to a directory beside PGDATA
  (outside it, since `--delta` cleans PGDATA). If that fails, the restore doesn't run.
- **After the restore**, it copies them back before startup. Recovery restores from the archive
  first and falls back to `pg_wal`, so it replays the tail and stops at its first invalid record,
  CRC-checked like any other WAL. Segments Postgres pre-created by renaming old ones don't count:
  only a segment whose first page header matches its own name is part of the tail.
- **It pushes the tail to the archive** with `pgbackrest archive-push`, before startup, every
  segment past the archive end, the newest included. Otherwise the new timeline would depend on
  WAL the repo doesn't have and the next restore would stop at the old archive end, found during
  the canary test. Not a `.ready` marker: found by the integration test, Postgres deletes the
  marker during archive recovery and then the segment itself, as if archived.
- **The newest too**, not left to Postgres. Postgres archives the old timeline's last segment as
  `.partial` when it promotes, but only when the end of WAL is mid-segment. After a clean
  shutdown (with archiving on, the shutdown switches WAL) the newest segment is complete, the new
  timeline starts at the next one and there is no `.partial`. The first version relied on the
  `.partial` and the VM run below lost row B's segment from both the archive and `pg_wal` that
  way. Pushing an incomplete newest segment under its own name is safe: the old timeline is dead
  after the restore, recovery reading it from the archive stops at the same invalid record it
  would in `pg_wal` and Postgres, having restored it from the archive, makes no `.partial`.
- **It reports all of it** in `pg_wal_tail`: `saved_to` (the original copies, kept until a human
  removes them), `replay_through_wal`, `pushed_to_archive`. A failed push doesn't fail the restore;
  the detail says so, which segments still need pushing and where the copies are.

The prediction now includes the tail (`replay_through_wal`). Postgres logs nothing for segments
it reads from `pg_wal`, so `verify_restore_recovery` also checks which segment `redo done at`
falls in: `verified` then reports `replayed_through_wal` and a recovery that stopped short of the
tail is a `mismatch`.

👉 After the fix, same fixture, same crash: both rows came back. Recovery replayed the archive
to `…0A`, then `…0B`/`…0C` from `pg_wal` and ended at `invalid record length at 0/C000118`, the
normal end of WAL, with `redo done at 0/C0000E0`.

`TestIntegration_RestoreFromBackup_KeepsUnarchivedWAL` makes the canary test permanent: both rows
survive, `verify_restore_recovery` reports `verified` through the tail, every tail segment is in
the repo and once the new timeline archives a segment of its own the WAL check is `continuous`
across it. With the old restore script it fails. `…_CleanShutdown` is the same canary with
Postgres shut down cleanly after `pg_control` is gone, as the VM fault's `systemctl restart` does;
Docker's `docker restart` doesn't shut down cleanly, which is why the first version passed every
Docker test. With the newest segment left to the `.partial` it fails on the WAL check itself:
"WAL segment …27 on timeline 11 is missing". An earlier version of the test only waited for the
WAL check to be `continuous` onto the new timeline, which the history file alone satisfies.
Two more cover the
failure paths: with the repo read-only the push fails but the restore succeeds, both rows come
back and pushing from `saved_to` by hand, as the detail says, makes the archive continuous; with
PGDATA's parent read-only nothing can be saved and the restore refuses before touching PGDATA.
`db-pgdata-corrupted-unarchived-tail` runs the same canary through the full agent chain in
`make faulttest-gateway-pgbackrest`, its `verify_sql` checking the rows themselves.

👉 The same canary on the real Ubuntu VM (systemd, SSH dispatch), through the full agent chain
with `pbs_pgbackrest_restore_remediate` v1.7, 2026-10-19 (incident `plr_c18ee2b4`, restore hop
`plr_cc73029d`):
  - fresh backup `20261019-185433F`, archive end `…11`; row A committed into `…12` (left `.ready`),
    row B into the open `…13`;
  - `restore_from_backup` saved `pg_wal` to `/var/lib/postgresql/16/pg_wal.saved-20261019T225649Z`
    and pushed `…12` to the archive;
  - `verify_restore_recovery`: `verified`, the archive restored through `…12`, `pg_wal` replayed
    through `…13`, `redo done at 0/130000D8`, new timeline 36;
  - both rows back, `archive_command` back on, the repo's `wal archive max` now `…12`. The run's
    findings: "Commits after the archive end (000000230000000000000011) were recovered from pg_wal
    through 000000230000000000000013; only changes never written to pg_wal are lost."
  - but `…13` (row B) was then in neither the archive nor `pg_wal` and no `.partial` either: the
    inject's `systemctl restart` had shut Postgres down cleanly, the shutdown's WAL switch made
    `…13` complete (`redo done at 0/130000D8` is that switch record) and timeline 36 started at
    `…14`. The next restore along timeline 36 would have stopped after `…12`. Reproduced on Docker
    the same day, then fixed by pushing the newest segment too (above). The VM's own archive was
    repaired by pushing `…13` from `saved_to`, which is what that directory is kept for.

👉 The same VM canary after the fix, 2026-10-19 (incident `plr_38972d91`, restore hop
`plr_cd2d34d4`): row A in `00000024…16` (`.ready`), row B in `…17` and again a clean shutdown,
so `…17` was complete (`redo done at 0/170000D8`). `verify_restore_recovery`: `verified`, the
archive restored through `…17`, new timeline 37; both rows back and the repo's `wal archive max`
is now `…17`: row B's segment is in the archive this time.

The run before it, a plain VM restore with archiving healthy, also showed the tail on a real
host: the archive ended on timeline 31 while the server was on 34 and of the two segments in
`pg_wal` the header check kept the open one and left out the one Postgres had pre-created
(Postgres's own log: `unexpected pageaddr 0/E000000 in WAL segment …10`).

The tail is only as good as `pg_wal` itself: when the volume is gone or the server had promoted
to a timeline whose history file never reached the archive, recovery still ends at the archive
end.

#### Underneath the layers: what's honest about the remediation, not oversold

`restore_from_backup` is restore-to-latest only — pgBackRest's own default recovery target, no
`--type`/`--target` flag, matching the stated scope exactly. A recovery target lives in a different tool
(`restore_to_point_in_time`, [PITR.md](PITR.md)) so this one stays unable to stop early. It refuses outright if `pg_isready`
reports the instance still accepting connections — confirmed live, this is the one thing standing
between restoring a dead instance and destroying a live one. It is classified
`policy.ActionDestructive`, the same tier as `restart_container`/`restart_service`, flowing
through the existing policy engine's approval/purpose rules with no special-casing.

#### Verified, not just claimed

The full crash → diagnose → restore → recover cycle was run live against a real pgBackRest
fixture (`testing/docker/docker-compose.pgbackrest.yaml`) before any Go code was written: delete
`pg_control`, crash the container, confirm `pgbackrest restore` brings it back, confirm a real
`psql` connection succeeds afterward. That same live pass corrected two assumptions that would
otherwise have shipped wrong: `pgbackrest restore --force` does **not** bypass its own
`postmaster.pid`-present refusal (`--force` means something else to pgBackRest) — the real fix is
removing the stale pid file once `pg_isready` has already confirmed the server is down; and
deleting `pg_control` crashes the *whole container* (postgres is PID 1 in the test image), taking
`sshd` down with it and making the target unreachable at the exact moment diagnosis needed it —
fixed in `testing/docker/start-with-sshd.sh` by making the entrypoint survive postgres's exit
instead of `exec`-replacing itself.

A separate, targeted live test (2026-10-06) confirmed the backup-existence question specifically:
a throwaway stanza that was never `stanza-create`'d, restored against with a canary file placed in
the target directory first. pgBackRest refused outright — `[075]: no backup set found to
restore`, exit code 75 — and the canary file survived untouched. pgBackRest validates that a
selectable backup exists (it has to read `backup.info` to build a restore file-list) before it
does anything destructive to the target — restoring against a nonexistent backup is already safe
today, confirmed empirically rather than assumed, with zero code of our own responsible for it.

#### Real host/VM support, not just Docker

Everything above was built and live-verified against the dedicated Docker fixture
(`testing/docker/docker-compose.pgbackrest.yaml`).  

This same chain also works against a real, SSH-reachable Linux host/VM running Postgres as a genuine systemd service. Not a disguised
container, but via [`db-pgdata-corrupted-vm`](../testing/catalog/failures.yaml) (new fault, same diagnosis/remediation playbooks).  

This required making the following SysAdmin Agent's tool more robust and portable: `check_host`, `get_host_logs`, `check_disk`, `check_memory`, `read_pg_log_file`,
`restart_container` and `restart_service`. All of them called the local-exec path directly, ignoring a target's configured `ssh_user`/`ssh_key_path` — only `get_pgbackrest_status`/
`run_pgbackrest_backup` (§2 above) ever actually dispatched over SSH.   

See [SYSADMIN_AGENT.md §5](SYSADMIN_AGENT.md#5-container-runtime-dispatch) for the full fix and the real in-process-SSH-server tests proving it.

`pbs_pgbackrest_restore_remediate` has a new `restart_service` path written from well-established
Debian/RHEL packaging conventions (journalctl showing only service-lifecycle lines, not
Postgres's own output.   

`read_pg_log_file`'s log directory differing by distro, see the new `pg_log_dir` infra-config field), not something directly observed against a real VM the way every
other claim in this document is.  

#### Kubernetes: restoring an instance that can't start

*Added in v0.32.* On Docker and the VM the restore runs where Postgres runs: `docker exec` into
the container or a shell over SSH on the host. On K8s that breaks exactly when it's needed.
A Postgres that can't start (`pg_control` gone) exits, the pod goes into `CrashLoopBackOff` and
`kubectl exec` into it fails and with it every pgBackRest command: `get_pgbackrest_status`, the
WAL check, the restore and the push of the `pg_wal` tail. A real Docker container behaves the same
way; the Docker fixture only hides it, because its entrypoint (`start-with-sshd.sh`) keeps the
container up after Postgres dies.

So the restore is now a lifecycle with the same steps on every platform
(`agents/sysadmin/lifecycle.go`) and K8s gets its own implementation of each
(`k8s_lifecycle.go`), for a self-managed Postgres StatefulSet whose image carries pgBackRest:

| Step | Kubernetes | Docker/Podman, host/VM |
|---|---|---|
| Serving? | a ready pod answers `pg_isready` | `pg_isready` in the container / on the host |
| Quiesce | scale the StatefulSet to 0, the replica count recorded in an annotation | nothing: the instance is already down |
| Maintenance run | a one-off pod with the StatefulSet's image and volumes, deleted afterwards | `docker exec` / the host's shell, as before |
| Resume (`restart_container`) | scale back to the recorded replicas | `docker restart` / `restart_service`, as before |
| Logs (`get_host_logs`) | `kubectl logs`, the previous container run first | `docker logs` / journal, as before |

- **Reads** (`get_pgbackrest_status`, the WAL check) exec into the pod while it's ready and use a
  one-off pod while it isn't.
- **The restore** refuses while Postgres serves, checks the WAL, scales the StatefulSet to 0 (so
  the crash-looping pod can't start Postgres on PGDATA mid-restore), then runs the same restore
  script as on Docker in a one-off pod. The saved `pg_wal` copy goes on the repo volume: the
  one-off pod's own disk goes with it and the data volume is PGDATA itself, which `--delta`
  cleans. It leaves the StatefulSet at 0 and its `quiesced` field says to resume it with
  `restart_container`; recovery runs when the pod starts.
- **RBAC:** the sysadmin agent's ClusterRole gains pods create/delete, StatefulSets
  get/list/patch and their scale subresource. Still no deployments, jobs or nodes.

👉 Verified in-cluster on K8s (2026-10-20), with the whole stack deployed by Helm
(`testing/k8s/helm-values-pgbackrest.yaml`) and the demo pod (`testing/k8s/pgbackrest-demo.yaml`)
running the stock Postgres entrypoint, so a crash really crash-loops. `make integration-k8s` calls
the in-cluster sysadmin agent's tools through its `/tool/{name}` endpoint, under its own service
account, in a namespace of its own that must end in `-destruct`:
  - **refusal:** the latest backup's start segment hidden, then a real crash. `get_pgbackrest_status`
    ran in a one-off pod and reported the chain `broken` at that segment; `restore_from_backup`
    refused with the StatefulSet still at 1 replica and a canary file in PGDATA untouched;
  - **the `pg_wal` tail, resume and verify:** archiving broken, row A committed in a `.ready`
    segment and row B in the open one, then a real crash. The restore scaled to 0, restored, saved
    the copy on the repo volume and pushed the tail; `restart_container` scaled back; both rows
    came back, `verify_restore_recovery` read the recovery from `kubectl logs` and reported it
    `verified` through the tail and every tail segment was in the archive.

The broken-chain detection also ran through the in-cluster agents as a fault
(`db-pgbackrest-wal-gap-k8s`, via faulttest and the gateway): the health triage reported the chain
broken and the `pgbackrest_wal_chain_broken` signal fired.

👉 K8s found two bugs in the WAL check that Docker and the VM never showed, both fixed with
regression tests:
  - **an empty gap list** when the backup's only segment was missing and nothing had been archived
    after it (a brand-new pod): the verdict was right, `broken`, but `gaps` named nothing;
  - **an unreadable `.history` file:** every Service in the pod's namespace injects environment
    variables pgBackRest warns about on stdout (`environment contains invalid option`) and a
    history file read with those lines in front failed to parse, so the check came back
    `unverified` instead of `broken` and the signal never fired. The text reads now drop
    pgBackRest's own log lines, as the JSON reads already skipped them.

And two fixture gaps: `init-pgbackrest.sh` (stanza and `archive_command`) was mounted by the
compose file rather than built into the image, so the first pod came up without either; and a
K8s volume is root-owned where a Docker named volume inherits the image's ownership, so the
repo volume needs an init container to hand it to `postgres`.

#### The agent-driven chain on Kubernetes

*Added in v0.33.* v0.32 verified each tool in-cluster, but the playbooks did not route a K8s 
data-loss case to them: the host triage sent every `runtime=kubectl` target straight to the pod-crash
triage and the entry hop told the DB agent to use `get_pod_logs`, a tool of the K8s Agent. Now:

- `pbs_db_data_loss_triage` (v1.10) hands a K8s-hosted instance to `pbs_sysadmin_host_triage`
  the same way it does a Docker or systemd one: it front-loads `single_instance`, `backup_tool` and
  `pitr_decided` and stops.
- `pbs_sysadmin_host_triage` (v2.4), on `runtime=kubectl` with those findings in hand, calls
  `get_host_logs` (`kubectl logs`, the crashed run first) and applies the same "could not find the
  database system" rule as for any runtime. It deliberately does not call `read_pg_log_file`, which needs
  exec into a pod that is crash-looping. Without the signature it falls back to
  `pbs_k8s_pod_crash_triage` as before.
- `pbs_pgbackrest_restore_remediate` (v1.9) resumes the StatefulSet with `restart_container`. It expects its first
  `check_host` after the restore to fail on Kubernetes (the restore scaled the StatefulSet to 0, so there is no pod
  to inspect) and goes on to the restart; the audit trail of the verified run shows exactly that error.

👉 Verified on K8s (2026-10-25) with the whole stack in-cluster, by `db-pgdata-corrupted-k8s`
(`pg_control` deleted in the `db-destruct` pod, which then crash-looped), through the gateway with
`--remediate --approval-mode force --gate-escalation` (incident `plr_c0d8fb87`, 215s, four hops, both
mid-chain gates force-approved):

| hop | run | tools it executed (audit trail) |
|---|---|---|
| `pbs_db_data_loss_triage` | `plr_c0d8fb87` | `check_connection` (error: the database is down), `create_incident_bundle` |
| `pbs_sysadmin_host_triage` | `plr_756f360c` | `check_host`, `get_host_logs`; transitioned with `data_loss_restore_candidate=true` |
| `pbs_pgbackrest_health_triage` | `plr_accc38c0` | `get_pgbackrest_status`; transitioned to the restore |
| `pbs_pgbackrest_restore_remediate` | `plr_23190a73` | `get_pgbackrest_status`, `restore_from_backup` (30.6s), `check_host` (error: the pod is gone, the restore scaled the StatefulSet to 0), `restart_container`, `check_host`, `get_host_logs`, `verify_restore_recovery` |

The harness confirmed recovery 229.7s after injection, from inside the pod (`verify_exec`: the instance
is serving and has left recovery).

Not yet: a ReadWriteOnce volume on a multi-node cluster (the one-off pod isn't pinned to the
instance's node); a remote repo (S3/GCS/Azure), where the one-off pod needs the repo's credentials
rather than a volume. Operator-managed Postgres (CloudNativePG) has no pgBackRest at all and stays out
of scope.

## 4. Roadmap

Not yet built — tracked, not forgotten:

- **Point-in-time recovery gaps ([PITR.md §7](PITR.md#7-limits)).**  
  Built in v0.33 for a single instance, to a time or LSN, down or serving. Not yet: a systemd VM run, routing the
  data-loss chain's `pitr_decided` into it, pushing the saved WAL tail to the archive, named restore points
  and transaction IDs, and approving the gate from `helpdesk-client`.

- **Multi-node volumes and remote repos for the Kubernetes restore (§3).**  
  The agent-driven chain works on K8s since v0.33 ([Kubernetes](#the-agent-driven-chain-on-kubernetes));
  multi-node ReadWriteOnce volumes and remote repos aren't handled.

- **Backup-check history (planned for v0.33).**  
  Each `get_pgbackrest_status` result is kept today only as a tool-call record in the audit
  trail, as JSON. A dedicated auditd table, one row per check (stanza, last backup, `backup_stale`,
  WAL status, `recoverable_to_wal`, `archive_end_time`, gaps, segment size and its source, trace
  ID), would answer questions pgBackRest's own history can't: when the WAL chain first broke, how
  far behind the archive was at each check and exactly what a refused restore saw.

- **Dedicated objective-evidence signals for `pbs_db_data_loss_triage` /
  `pbs_sysadmin_host_triage` (§3).**  
  Both currently reason over raw log text with no structured, named probe backing their
  conclusions the way `get_pgbackrest_status` backs the health-check hop. Building this would mean
  giving `get_host_logs`/`read_pg_log_file` a typed result with real signals (a specific failure
  string present, a specific one absent) rather than leaving those two hops as the one part of the
  chain with no deterministic backstop at all.

- **`pg_basebackup` job-level health.**   
  §2 above closes this gap for pgBackRest specifically —
  the more common production backup tool for self-managed Postgres. A bare `pg_basebackup`
  (no pgBackRest/wal-g/similar wrapper) has no equivalent status command to poll at all; covering
  it would need a different integration point (reading a cron job's own exit status/log or a
  wrapper script's own status file), not an extension of `get_pgbackrest_status`.

- **wal-g job-level health.**   
  Same shape of gap as `pg_basebackup` above, for the other common
  self-managed backup tool — not yet built.

- **pgBackRest remediation beyond stale-backup.**   
  §2's `run_pgbackrest_backup` deliberately
  refuses to act on a broken repo/stanza (permissions, ownership, mount problems have no
  confirmed-correct value to restore). A narrower, safely-automatable subset of those — e.g. a
  misconfigured `pgbackrest.conf` setting with a known-good prior reading, mirroring §1's
  `set_archive_command`/`get_saved_snapshots` pattern — is a candidate for later expansion, once
  a real customer-reported case justifies it.

- **CloudNativePG-native WAL-archiving remediation.**   
  §1's `set_archive_command` cannot act
  against a real CNPG-managed cluster, at any privilege level — see §1's own "Verified, not just
  claimed" callout for the direct evidence (`postgresql.auto.conf` is mode 400, read-only even
  for the Postgres superuser and the container runs `readOnlyRootFilesystem: true`). This is
  CNPG's own deliberate design, not fixable by granting more access. A CNPG-native remediation
  path — patching the `Cluster` custom resource's own `archive_command` field (a K8s API write,
  reconciled by the operator) instead of running SQL — is a distinct, unbuilt capability, not an
  extension of `set_archive_command` itself. Diagnosis (`get_backup_status`, pure `SHOW`/`SELECT`)
  is unaffected and already works against CNPG's own unprivileged `app` user.

- **CloudNativePG (and similar K8s operator-managed) backup visibility.**   
  §2's `get_pgbackrest_status`/`run_pgbackrest_backup` now refuse outright (rather than silently
  running against the wrong host) when a target resolves to a K8s Pod — see §2's own
  "Verified, not just claimed" callout.  

  That refusal is permanent by design, not a stopgap, but the reason is more precise than "CNPG doesn't run pgBackRest": CNPG's own *native* backup path
  is Barman Cloud (itself being moved out of the core operator into an official plugin as of
  1.26+) and the CNPG maintainers have explicitly declined to add pgBackRest support directly
  to the operator — not an oversight, a stated preference for K8s-native primitives
  (volume snapshots) over wrapping external tools, plus supportability concerns about bugs in an
  external tool being magnified in a concurrent K8s environment ([cloudnative-pg/cloudnative-pg
  discussion #3145](https://github.com/cloudnative-pg/cloudnative-pg/discussions/3145)).  

  Community-maintained pgBackRest support *does* exist via CNPG's newer generic plugin interface
  (CNPG-I — experimental third-party plugins from Dalibo and Opera Software), so "CNPG never uses
  pgBackRest" isn't strictly true. But CNPG-I plugins integrate over **gRPC** (a sidecar
  container or a standalone in-namespace Deployment), never SSH — so even a CNPG cluster running
  one of those plugins isn't reachable through this project's planned remote-host pgBackRest
  reach (v0.31, SSH to a repo/PG host that isn't colocated with the agent).   

  That's the real, durable reason this gap doesn't close in v0.31: the access pattern is fundamentally different
  (a gRPC plugin protocol, not a `pgbackrest` CLI reachable over SSH), not merely "no sshd in the
  Pod today." The real fix is a CNPG-native integration — reading the `Backup`/`ScheduledBackup`
  custom resource's own status via the K8s API for the Barman Cloud path or the relevant
  plugin's own status surface for a third-party one — architecturally unrelated to anything in
  §2 and not built yet.

- **Cloud-managed backup visibility**   
  (RDS/Cloud SQL/AlloyDB automated backups). Zero coverage
  today — those platforms expose backup status via their own control-plane APIs, not anything
  queryable from inside Postgres or a host-level CLI; a genuinely different integration point
  from either of §1/§2. (Same shape of gap as CNPG above — a managed-platform control plane, not
  a host-level CLI or SQL view.)

---

See also: [HA_DR.md](HA_DR.md) for streaming-replication and failover diagnosis,
[PLAYBOOKS.md](PLAYBOOKS.md) for the "Database Down" playbook graph that §3's chain is part of,
[PLAYBOOK_OPS.md §1.2](PLAYBOOK_OPS.md#12-understand-the-db-down-escalation-chain) for that
graph's full escalation-chain diagram, [OBJECTIVE_EVIDENCE.md](OBJECTIVE_EVIDENCE.md) for how the
force-gate mechanism §2/§3 both rely on actually works, [FAULTTEST.md](FAULTTEST.md) for the
fault injection CLI and full catalog reference, [CONSISTENCY.md](CONSISTENCY.md) for how a
playbook earns a stability certification before entering live rotation.
