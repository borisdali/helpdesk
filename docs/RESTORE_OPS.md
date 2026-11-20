# aiHelpDesk Restore Operations Guide

Operational best practices for restores:   
* what to set up before you need one
* how to prove a backup restores and how long it takes by **drilling** it
* how to read the plan when you do need one
* how to learn from it afterwards

For how a point-in-time restore works and what protects it, see [PITR.md](PITR.md). For the objectives it is measured against, see [OBJECTIVES.md](OBJECTIVES.md).

> **A backup nobody has restored is a hope and a restore nobody has practised is a first attempt on the worst day.** A drill is a restore of your backup onto a copy you bring, checked by aiHelpDesk and put on the record. It proves the backup restores, gives the plan a restore time before the bad day and gives your staff a rehearsal.

**Drill every database that has an objective, at least once a quarter and after any change to the backup path, the hardware or the data size.** This is a recommendation we would make as strongly as taking the backup: a backup strategy without drills is untested and an `rto` without a drill is a number nobody has measured. aiHelpDesk reminds you when a quarter has passed (§2.7), in `get_pgbackrest_status` and in the restore plan.

---

## Table of Contents

1. [Before you need a restore](#1-before-you-need-a-restore)
   - [1.1 Set the objectives](#11-set-the-objectives)
   - [1.2 Keep the backups healthy](#12-keep-the-backups-healthy)
   - [1.3 Why drill](#13-why-drill)
   - [1.4 How drills fit the flywheel](#14-how-drills-fit-the-flywheel)
2. [Running a restore drill](#2-running-a-restore-drill)
   - [2.1 Bring a clone](#21-bring-a-clone)
   - [2.2 Register the clone](#22-register-the-clone)
   - [2.3 What to time](#23-what-to-time)
   - [2.4 Record the drill](#24-record-the-drill)
   - [2.5 Read the verdict](#25-read-the-verdict)
   - [2.6 What a drill counts for](#26-what-a-drill-counts-for)
   - [2.7 The reminder](#27-the-reminder)
   - [2.8 Looking back at drills](#28-looking-back-at-drills)
3. [When you do need a restore](#3-when-you-do-need-a-restore)
4. [After a restore](#4-after-a-restore)
5. [Training with drills](#5-training-with-drills)
6. [When a drill is refused](#6-when-a-drill-is-refused)
7. [Limits](#7-limits)
8. [Quick-reference checklist](#8-quick-reference-checklist)
9. [See also](#9-see-also)

---

## 1. Before you need a restore

### 1.1 Set the objectives

A restore is measured against what the database is supposed to achieve. Set at least one of `rpo`, `rto` or `availability_slo` for every database that matters, in `infrastructure.json` ([OBJECTIVES.md](OBJECTIVES.md#1-where-an-objective-is-set)):

```json
"environments": { "prod": { "objectives": { "rpo": "5m", "rto": "30m", "availability_slo": 99.95 } } }
```

Two things follow from setting them. The plan can say whether a restore fits what is left of the downtime budget and a database with an objective is **reminded to drill** (§2.7): nothing says a restore matters to a database that has none and a development database should not nag.

### 1.2 Keep the backups healthy

`get_pgbackrest_status` reports the stanza, the newest backup and whether the archived WAL needed to restore it is continuous. Two signals sit side by side: `backup_stale` (is the newest backup older than the age you passed in) and `objective_exposure` (is the posture enough for the objectives you declared). Neither says the backup *restores*. That is what a drill is for.

### 1.3 Why drill

- **It proves recoverability.** No number from real incidents can show a backup restores before the incident happens.
- **It gives the plan a number.** The restore-time estimate needs three real restores of the same server. Most servers never have three, so most plans fall back to the declared `rto`, an upper bound. A drill produces restores on demand, on a copy.
- **It rehearses the people.** The procedure is run by someone other than whoever wrote it, on a day when nothing is on fire.

A drill is **not an incident**. Recording one creates no incident and never charges the availability budget.

**What skipping drills costs.** The first restore of a server is then the first time anyone learns that the backup is incomplete, that the restore takes three hours against a 30-minute `rto`, or that the on-call engineer has never run the procedure. All three surface during an outage, with the availability budget already burning and the plan unable to give a figure (`no_history`).

**When to drill again, not only on the calendar:** after a change of backup tool, repository, storage or retention; after a major PostgreSQL upgrade; when the database has grown by more than a fifth (older drills stop counting at ±20% of the size, §2.6); after a restore that took longer than the last drill said (§4); and when the person who last ran one has left.

### 1.4 How drills fit the flywheel

Objectives are the outer loop of the Vault flywheel ([OBJECTIVES.md §6.2](OBJECTIVES.md#62-how-it-connects-to-the-vault-flywheel)): the flywheel makes diagnosis and repair better after each incident and the objectives ask whether the promises were kept. Both learn only from things going wrong. A server that rarely fails, or rarely needs a restore, gives them nothing to learn from. Drills supply the missing step on a quiet day:

1. **Declare** an `rto`.
2. **Drill**: restore on a copy and record how long it took.
3. **Compare**: the plan shows the declared `rto` against what the drill demonstrated (`RTO CHECK`).
4. **Act**: a person improves the backup path or the resources, or makes the `rto` honest. The system never changes an objective.
5. **Drill again** and see the change.

Real restores then take over as the evidence (from three) and the drills go on guarding the cases a real restore has not yet covered. Drills stay out of the incident history on purpose: they are not in the playbook statistics, resolution rates or certification, because a rehearsal on a copy says nothing about how a playbook handles a real outage.

---

## 2. Running a restore drill

### 2.1 Bring a clone

aiHelpDesk does **not** create the clone: it can take very long for a big database and it is your tooling. It verifies the clone and links it to its source in a record.

| How you make the clone | `--drill-method` | Plans a restore? |
|---|---|---|
| `pgbackrest restore` of the source's backup onto a scratch host | `pgbackrest` | yes, when the size is comparable and it is recent |
| A storage snapshot clone | `snapshot` | no: it says nothing about a restore through pgBackRest |
| A cloud provider's clone or restore | `cloud` | no |
| Anything else | `other` | no |

The method is **your claim**: aiHelpDesk does not tell how a clone was made, so label it truthfully.

Whatever the method, three rules keep the drill safe:

1. **Restore with archiving off.** With pgBackRest: `pgbackrest --stanza=main --archive-mode=off restore`. A clone that keeps the source's `archive_mode` and `archive_command` pushes its own timeline's WAL and history file into the source's repository. The repository accepts them (same system identifier) and a later point-in-time restore of the source could follow or collide with them. This was reproduced for real: a clone restored with the archiving kept pushed a timeline-2 history into its repository within seconds. aiHelpDesk **refuses to link** such a clone (§6).
2. **Promote it.** The clone must be out of recovery. A clone paused at a target, not yet promoted or still a standby, is refused: a paused restore and a standby cannot be told apart, so it fails closed.
3. **Never experiment against the repository production writes to.** Restoring *from* it is a read and is fine. Running a clone that can archive *into* it is the hazard.

For a point-in-time drill add the target and let it promote:

```bash
pgbackrest --stanza=main --archive-mode=off --type=time --target="2026-11-18 10:00:00+00" --target-action=promote restore
```

### 2.2 Register the clone

The clone has to be an entry in the infrastructure config before a drill can be recorded against it. That is your step, like bringing the clone:

```json
"db_servers": {
  "orders-clone": {
    "name": "orders-clone",
    "connection_string": "host=scratch.example.com port=5432 dbname=orders user=postgres",
    "password_env": "ORDERS_CLONE_PW"
  }
}
```

Give the clone **no `environment` and no objectives**: it would inherit the environment's and a scratch copy should not get a downtime budget or a drill reminder of its own. An ordinary login role is enough for the checks and credentials come from the environment variable, never the file.

Two more things have to be true before the verification can read the clone:

- **Your policy has to allow it.** The checks run through the database agent, which asks the policy engine for `read` on **both** servers. Deny-by-default applies to a clone like to any database: one with no tag, or a tag no policy names, is refused (`policy denied: ... No policy matched this resource`). Give the clone a tag that a policy of yours allows `read` on (for a scratch copy, a rule for `tags: [test]` or `[development]` that allows only `read` is enough). The drill needs no write and no destructive permission on either server.
- **The agent has to reach it.** The database agent, not your workstation, connects to the clone: its host needs a route to the clone's port (a firewall rule for the PostgreSQL port of the clone is the usual gap).

### 2.3 What to time

The drill's duration is **your word** (recorded as `reported`) and it is what the plan will use, so time the same thing the plan compares it with. A real restore is timed as the remediation run: **the restore, the restart and the verification**, from when the restore command starts until the database accepts connections and has been checked. It does not include waiting for a person.

- **Include:** the restore itself, WAL replay to the target, the start and your own check that it works.
- **Leave out:** creating the VM or volume, copying a snapshot into place and any time spent waiting for someone.

If you time less than that, the drill reads optimistic and nothing flags it. If your clone had to be provisioned first, record when the *restore* started, not when you began.

The clone's **size** should match the source's cluster (the plan compares them). A clone holding a subset of the data is recorded as proof of recovery, but it does not plan a restore.

### 2.4 Record the drill

```bash
helpdesk-client --drill-record \
  --drill-source prod-orders --drill-clone orders-clone --drill-method pgbackrest \
  --drill-started 2026-11-19T02:00:00Z --drill-finished 2026-11-19T02:01:35Z \
  --drill-backup-label 20261101-020000F \
  --drill-verify-sql "select count(*) > 0 from orders" \
  --ticket DRILL-7 --drill-note "Q4 drill with the new runbook"
```

Give the two times or `--drill-duration 1m35s`; the times win when both are given. `--drill-verify-sql` is optional and must be a single `SELECT` that returns true on the clone (a row count you expect, for instance). The ticket is a reference you give the drill, recorded with it.

aiHelpDesk then reads **both** servers, read-only and records the drill whatever the verdict. Recording is a human act at the gateway: the model never links a drill. The command exits non-zero when the drill was refused or could not be verified, so a script notices.

### 2.5 Read the verdict

| Check | What it asks | If it fails |
|---|---|---|
| `reachable` | Can both servers be reached? | `unverifiable`: fix the connection, record again |
| `same_system` | Do the two share a `system_identifier`? This is the proof the clone is a copy of the source | `refused`: a logical copy or a new cluster is not a restore of this backup |
| `not_the_source` | Is the clone a different server and not a standby of the source? | `refused` |
| `archive_guard` | Could the clone push into the source's archive? | `refused`: restore with `--archive-mode=off` |
| `consistent` | Is it out of recovery? | `refused`: promote it |
| `size_class` | Is its size within 20% of the source's? | a **warning**: recorded, not used to plan |
| `verify_sql` | Does your own check return true? | `refused` |

The overall status is `verified`, `verified_with_warnings` (a size warning or archiving on with no command), `refused` or `unverifiable`. A refused or unverifiable drill **is on the record** so the trail shows the attempt, but it does not count. Fix what the checks name and record again, replacing the earlier attempt:

```bash
helpdesk-client --drill-record ... --drill-supersedes drl_ab12cd34
```

### 2.6 What a drill counts for

Two different questions, with different rules.

**The reminder** asks whether the procedure has been practised. **Any verified drill** resets it, whatever its method or size.

**The restore-time estimate** asks whether a drill says something about *this* restore. It counts a drill only when it is:

- verified and of the **same system** (matched by `system_identifier` when it can be read, by name otherwise: the estimate says which);
- a **pgBackRest** drill, with a recorded duration;
- within **20% of the size** of the restore being planned;
- no older than **90 days**.

Drills are **never mixed with real restores**. They are two sets with two counts: real restores plan the restore as soon as there are three; drills fill in below that (the longest of three to nine, a p90 from ten); and with too few of either the declared `rto` stands. A plan made from drills says so: *demonstrated on a copy, not on this server*. A drill that took longer than the `rto` raises `RTO CHECK (!)` even before any real restore has shown it.

### 2.7 The reminder

`drill_interval` (in the objectives, `"90d"` by default) is how long a database may go without a verified drill. `"0d"` turns it off. An interval that cannot be read does not switch it off: the default applies and the config problem is reported.

When it is due, the reminder reads *last verified drill 143 days ago (limit 90 days)* (or *no verified restore drill of this server is recorded*) and a refused attempt since the last good drill is named with what failed. It shows in the point-in-time plan (`DRILL OVERDUE (!)`), in `get_pgbackrest_status` (`drill_currency`, `drill_overdue`) and in `faulttest vault budget`. **It only reminds**: nothing is blocked, held or refused because a drill is overdue.

### 2.8 Looking back at drills

```bash
helpdesk-client --drill-list --drill-source prod-orders      # a table, newest first
faulttest vault drills --source prod-orders                  # the same, with --status, --days, --all
faulttest vault drills drl_ab12cd34                          # one drill in full: every check, what replaced it
```

A drill that a later record replaces (a refusal put right) is left out unless you ask for `--all`. The record is also a hash-covered `restore_drill` event in the audit trail and a failed attempt to record one leaves a gateway request event, so the trail shows who tried and why it did not go through. See [API.md](API.md#restore-drills) for the endpoints.

---

## 3. When you do need a restore

Read the plan before you approve anything: `helpdesk-client --pitr-plan` ([PITR.md](PITR.md#the-short-way-helpdesk-client)). The lines that come from drills and objectives:

| Line | What it tells you |
|---|---|
| `Past restores` | How long this server's real restores have taken and how many there are |
| `Restore drills` | How many drills count, how long they took and, when the plan's duration comes from them, that it was *demonstrated on a copy, not on this server* |
| `RTO CHECK (!)` | Past restores (or drills) took longer than the declared `rto`: the objective has not been met in practice |
| `BUDGET CHECK` | Whether the downtime fits what is left of the budget and on what duration the answer rests |
| `DRILL OVERDUE (!)` | No verified drill within the interval. A reminder: it holds nothing back |

A duration that rests on a drill is a better guess than the declared `rto` and a worse one than three real restores. Treat it as that.

---

## 4. After a restore

- **Compare the plan with what happened.** `faulttest vault incidents <run>` shows *PLAN VS ACTUAL* with the recovery decision: the planned recovery time and its basis (a drill basis reads *the longest restore drill, demonstrated on a copy*) against how long the restore took.
- **If the restore took longer than a drill said,** the drill read optimistic: different hardware, a smaller clone or a duration that left something out (§2.3). Drill again, on the hardware you would restore to and time it the way §2.3 says.
- **A real restore is the best drill.** It joins the real-restore history that outranks drills. A drill after a real restore costs little and refreshes the reminder.
- **Spend:** `faulttest vault budget <database>` shows the budget the incident used and the drill reminder beside it.

---

## 5. Training with drills

- **Rotate who runs it.** The person who built the backup runbook is the one who needs it least. Have someone else follow it and note where they got stuck.
- **A drill rehearses the restore and the check, not the gate.** The gate (the recovery decision, the acknowledgements) is exercised by a point-in-time restore on a non-production database; practise that separately, with the people who would approve for real.
- **Make a drill day a habit.** The reminder does it per database; a quarterly drill day across the fleet is the same rhythm with a person attached.
- **Write down what surprised you** in the drill's `--drill-note`, so the next person finds it. Keep it plain text: control characters are refused.
- **Who may record a drill:** anyone who can authenticate to the gateway, like approving a recovery gate. Decision rights for restores are a separate piece of work; until then, treat recording a drill as the act of a named operator and use the ticket.

---

## 6. When a drill is refused

| Verdict says | Why | Fix |
|---|---|---|
| `the clone is not a copy of the source` | Different `system_identifier`: a logical dump, a new cluster or the wrong server | Restore the source's backup with pgBackRest or clone its storage, not a dump |
| `archive_mode is on and the clone has an archive command` | The clone could push into the source's archive | `pgbackrest restore --archive-mode=off` or `archive_mode=off` on the clone, then record again |
| `the clone is still in recovery` | Paused at a target, not promoted or a standby | Promote it (`--target-action=promote` or `pg_promote()`), then record again |
| `the clone is a standby streaming from a primary` | It is a replica, not a restored copy | Restore a copy instead |
| `the clone is the source` / `reached at the source's address` | The same server | Use a different clone |
| `the clone could not be reached` (`unverifiable`) | Connection, password or firewall | Fix access and record again |
| `The clone cannot be used: ... not registered` | The clone is not in the infrastructure config | Add it (§2.2) |
| `policy denied: ... No policy matched this resource` (`unverifiable`, naming the source or the clone) | No policy allows `read` on that database: its tags match no rule and deny-by-default applies | Tag the clone (and check the source's tag) so a `read` rule matches, restart the database agent, then record again (§2.2) |
| `size_class` warning | The clone is not within 20% of the source | Recorded as proof of recovery; for a drill that plans restores, restore the whole cluster |
| `verify_sql` failed | Your own check returned false or could not run | The restore is incomplete or the SQL is wrong |

---

## 7. Limits

- **The method is your claim.** A snapshot clone labelled `pgbackrest` would be counted. The duration is also your word and the record says `reported`.
- **Matching by name.** The sysadmin agent reads the source's `system_identifier` (from the running instance or from the data directory when it is down) and matches drills by it. When it cannot, drills are matched by name and the estimate says so; a server rebuilt but not yet re-drilled would then still count its old drills. The posture check (`get_pgbackrest_status`) and `vault budget` match by name only.
- **Ninety days.** An older drill is kept on record and no longer plans a restore. It still resets the reminder until the interval passes.
- **One clone, one size.** The comparison is with the backup being restored and the posture check with the newest drill, since it has no restore in view.
- **`verify_sql`** is one `SELECT`, no semicolon, at most 500 characters, run on the clone as a scalar subquery. It is not a test framework.
- **A clone that is deliberately paused** at a recovery target is not supported: promote it.

---

## 8. Quick-reference checklist

- [ ] Every database that matters has an `rpo`, `rto` or `availability_slo`.
- [ ] Each has been drilled within its `drill_interval` (`faulttest vault budget <db>` says).
- [ ] The clone was restored with `--archive-mode=off`, promoted and is registered with no environment or objectives.
- [ ] The duration recorded is restore + start + check, not provisioning or waiting.
- [ ] A refused drill was put right and re-recorded with `--drill-supersedes`.
- [ ] After a real restore, the plan-vs-actual was read and any optimistic drill redone.

---

## 9. See also

| Document | What it covers |
|---|---|
| [PITR.md](PITR.md) | Point-in-time restore: the safety model, the plan, the gate, what is verified |
| [OBJECTIVES.md](OBJECTIVES.md) | `rpo`, `rto`, `availability_slo`, `drill_interval`, the budget and the ledger |
| [BACKUP.md](BACKUP.md) | pgBackRest backup health and WAL continuity |
| [API.md](API.md#restore-drills) | The drill endpoints |
| [CLIENT.md](CLIENT.md) | `helpdesk-client` flags, including `--drill-record` and `--drill-list` |
| [PLAYBOOK_OPS.md](PLAYBOOK_OPS.md) | The investigation workflow around an incident |
