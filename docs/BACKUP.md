# aiHelpDesk Backup & Restore

This document describes how aiHelpDesk diagnoses and, where safely possible, remediates
PostgreSQL backup-taking failures. What's certified today and what's on the roadmap.

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
Kubernetes Pod. `get_pgbackrest_status`/`run_pgbackrest_backup` didn't — a K8s-configured target
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
instance, no point-in-time target. Full PITR-to-arbitrary-timestamp, replica rebuild after
promotion and non-pgBackRest restore are explicitly out of scope — see [§4](#4-roadmap).

**The failure mode**:  
A missing/corrupted `pg_control` file (disk corruption, an accidental delete, a bad volume
restore) leaves PostgreSQL unable to start at all and the error message is `postgres: could not find the database
system`. This is distinct from every scenario in §1/§2: the database isn't slow or
misconfigured, it's *gone* and the only way back is restoring from a backup rather than fixing a
setting.

**Why aiHelpDesk doesn't make that mistake**:  
The chain above exists because no single agent holds every tool the decision needs. The DB agent
(`pbs_db_data_loss_triage`) has no tool to read Docker/Podman container logs; the SysAdmin agent
has no tool to verify Postgres backup health. Rather than having each hop guess at what it can't
see, each one **front-loads every condition it can determine without the evidence the next hop is
about to fetch** (single instance? pgBackRest in use? PITR already decided?) into its own
`FINDINGS`, so the next hop only has to judge the one condition it's actually equipped to answer
and can act on the combined picture directly — no bouncing back for a decision already made. An
earlier revision of this chain *did* bounce `pbs_sysadmin_host_triage` back to
`pbs_db_data_loss_triage` before transitioning onward, adding two full LLM round-trips for zero
safety benefit once it was clear none of the static conditions needed fresh evidence to answer —
caught in review before it shipped, not after a slow live run.

**A second, independent backstop that doesn't depend on the model getting it right:**   
And this is the one that actually matters for a destructive op: `pbs_pgbackrest_health_triage`'s `get_pgbackrest_status` call carries the exact same
`pgbackrest_backup_unhealthy` [objective-evidence signal](OBJECTIVE_EVIDENCE.md) as §2 and here
it does real work: `pbs_pgbackrest_restore_remediate` is only ever reached once that hop's own
`TRANSITION_TO` fires, which should only happen when the backup is genuinely confirmed healthy.  

If the model's response claims "healthy" but the tool's own structured result says otherwise, the
signal goes unconfirmed and the gateway forces a `pending_gate`. The import part is this: **it cannot be bypassed by
`approval_mode=force`**.   

That is, `cmd/gateway/playbooks.go`'s own force-gate mechanism exists specifically
to override the caller's force-mode auto-chain intent when evidence contradicts the model's
conclusion. The single most important precondition for this op — is there actually a healthy
backup to restore from — is backed by a deterministic Go code path, not left to the model's prose.

**A third protection, fails closed by design, not by accident**:  
We found this during live debugging of something else entirely, confirmed while weeding out three separate live bugs during this chain's own testing:  
- a stdio-buffering race that delayed when a crash message reached the log stream  
- a log-request window too small once prior test cycles had piled up legitimate noise   
- `get_host_logs` silently discarding stderr (where PostgreSQL's own fatal startup message actually lives)  

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

**What this does *not* cover, said plainly rather than left implicit**:  
- `pbs_db_data_loss_triage` and `pbs_sysadmin_host_triage` have no dedicated objective-evidence
  signal of their own — `get_host_logs` returns raw text, not a structured result with a named
  probe the way `get_pgbackrest_status` does. Their findings (single instance? does the log text
  suggest damage beyond what a plain restore fixes?) rest on the model's own quoted-evidence
  discipline, not a deterministic check — the fail-closed property above narrows the blast radius of
  that gap considerably, but, per the honest limit just stated, does not close it.
- `restore_from_backup` itself doesn't independently re-verify `status_code`/`backup_stale` at the
  Go level when called with an explicit stanza (the normal path from the remediation playbook's
  own Step 1) — it relies on that playbook's LLM-mediated re-check plus the force-gate above as
  the real backstops, not a third, redundant tool-level check.
- There is no verification, before or after the restore, that the WAL chain from the backup to the
  crash point is actually contiguous — see [§4](#4-roadmap)'s "WAL-chain completeness" item. A gap
  in the archive wouldn't be caught by anything above; Postgres replays however much WAL it can
  find and completes recovery there, logging it the same way as legitimately reaching the true
  end of the stream.

**What's honest about the remediation, not oversold**:  
`restore_from_backup` is restore-to-latest only — pgBackRest's own default recovery target, no
`--type`/`--target` flag, matching the stated scope exactly. It refuses outright if `pg_isready`
reports the instance still accepting connections — confirmed live, this is the one thing standing
between restoring a dead instance and destroying a live one. It is classified
`policy.ActionDestructive`, the same tier as `restart_container`/`restart_service`, flowing
through the existing policy engine's approval/purpose rules with no special-casing.

**Verified, not just claimed**:  
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

**Real host/VM support, not just Docker**:  
Everything above was built and live-verified against the dedicated Docker fixture
(`testing/docker/docker-compose.pgbackrest.yaml`).  

This same chain also works against a real, SSH-reachable Linux host/VM running PostgreSQL as a genuine systemd service. Not a disguised
container, but via [`db-pgdata-corrupted-vm`](../testing/catalog/failures.yaml) (new fault, same diagnosis/remediation playbooks).  

This required making the following SysAdmin Agent's tool more robust and portable: `check_host`, `get_host_logs`, `check_disk`, `check_memory`, `read_pg_log_file`,
`restart_container` and `restart_service`. All of them called the local-exec path directly, ignoring a target's configured `ssh_user`/`ssh_key_path` — only `get_pgbackrest_status`/
`run_pgbackrest_backup` (§2 above) ever actually dispatched over SSH.   

See [SYSADMIN_AGENT.md §5](SYSADMIN_AGENT.md#5-container-runtime-dispatch) for the full fix and the real in-process-SSH-server tests proving it.

`pbs_pgbackrest_restore_remediate` has a new `restart_service` path written from well-established
Debian/RHEL packaging conventions (journalctl showing only service-lifecycle lines, not
PostgreSQL's own output.   

`read_pg_log_file`'s log directory differing by distro, see the new `pg_log_dir` infra-config field), not something directly observed against a real VM the way every
other claim in this document is.  

## 4. Roadmap

Not yet built — tracked, not forgotten:

- **WAL-chain completeness verification (§3).**  
  Nothing today confirms, before or after a restore, that the WAL chain from the backup to the
  crash point is actually contiguous. This is implementable without a live source instance:
  `pgbackrest info --output=json` already reports the stanza's `archive[].max` (the latest WAL
  segment filename physically present in the repo — not yet parsed into
  `GetPgBackRestStatusResult`) and Postgres's own recovery log reports exactly which segments it
  replayed (`"restored log file \"...\" from archive"`) and the final LSN (`"redo done at ..."`).
  Comparing the two after a restore — did recovery replay through the repo's own latest segment,
  or stop short — would turn a silent partial-recovery into a reported, CRITICAL finding instead
  of a result that looks identical to a clean recovery from the outside.

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
  running against the wrong host) when a target resolves to a Kubernetes Pod — see §2's own
  "Verified, not just claimed" callout.  

  That refusal is permanent by design, not a stopgap, but the reason is more precise than "CNPG doesn't run pgBackRest": CNPG's own *native* backup path
  is Barman Cloud (itself being moved out of the core operator into an official plugin as of
  1.26+) and the CNPG maintainers have explicitly declined to add pgBackRest support directly
  to the operator — not an oversight, a stated preference for Kubernetes-native primitives
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
