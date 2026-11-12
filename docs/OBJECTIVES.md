# aiHelpDesk recovery objectives and environments

A recovery you cannot measure against anything is only a report. This is how `infrastructure.json` says what
each database is **supposed** to achieve, so the numbers aiHelpDesk produces (the
[data-loss estimate](PITR.md#4-operating-it) first) can be compared with it.

| Objective | Meaning | Used by |
|---|---|---|
| `rpo` | The most data, as time, that may be lost | The [PITR data-loss estimate](PITR.md#4-operating-it), which is set against it |
| `rto` | The longest a recovery may take | Not consumed yet |
| `availability_slo` | Share of time the database should be available, in percent | The [availability budget](#52-the-availability-budget): what it allows to be down |
| `availability_window` | The rolling window the SLO is measured over (`"30d"`; 30 days when unset) | The same budget |

This page describes the configuration and how it resolves. The checks that use it are added one at a time and
documented where they appear.

---

## Table of Contents

1. [Where an objective is set](#1-where-an-objective-is-set)
2. [Environments are not tags](#2-environments-are-not-tags)
3. [Values](#3-values)
4. [A mistake never costs the inventory](#4-a-mistake-never-costs-the-inventory)
5. [Reading it in code](#5-reading-it-in-code)
   - [5.1 Which incidents spend the availability budget](#51-which-incidents-spend-the-availability-budget)
   - [5.2 The availability budget](#52-the-availability-budget)
   - [5.3 The availability ledger](#53-the-availability-ledger)
   - [5.4 The budget at the gate](#54-the-budget-at-the-gate)
   - [5.5 The recovery-time estimate](#55-the-recovery-time-estimate)
6. [Design: how objectives will be used (not built yet)](#6-design-how-objectives-will-be-used-not-built-yet)
   - [6.1 One decision loop](#61-one-decision-loop)
   - [6.2 How it connects to the Vault Flywheel](#62-how-it-connects-to-the-vault-flywheel)
   - [6.3 What this depends on and what could mislead](#63-what-this-depends-on-and-what-could-mislead)

---

## 1. Where an objective is set

Most specific first:

| Level | Where | Applies to |
|---|---|---|
| Database | `db_servers.<id>.objectives` | That database |
| Environment | `environments.<name>.objectives` | Every database whose `environment` is `<name>` |
| Fleet | `defaults.objectives` | Everything else |

Each objective resolves **on its own**. A database that sets only its `rpo` takes its `rto` from its
environment and the environment's from the fleet default. Every resolved value carries its source
(`database`, `environment:prod`, `default`), so a number is never shown without saying where it came from.

```json
{
  "defaults": { "objectives": { "rpo": "1h", "rto": "4h", "availability_slo": 99.0 } },
  "environments": {
    "prod":    { "description": "customer-facing", "objectives": { "rpo": "5m", "rto": "30m", "availability_slo": 99.95, "availability_window": "90d" } },
    "staging": { "objectives": { "rpo": "1d" } },
    "dev":     {}
  },
  "db_servers": {
    "prod-orders": { "name": "orders", "connection_string": "host=...", "environment": "prod" },
    "prod-ledger": { "name": "ledger", "connection_string": "host=...", "environment": "prod",
                     "objectives": { "rpo": "0s", "rto": "10m" } },
    "scratch":     { "name": "scratch", "connection_string": "host=..." }
  }
}
```

Here `prod-orders` gets everything from `prod`; `prod-ledger` has its own `rpo` and `rto` and the `prod`
availability; `scratch` has no environment and gets the fleet default.

## 2. Environments are not tags

`tags` are free-form strings the policy engine and the fleet runner match on. They mean nothing to the config
and nothing checks them. An environment is checked:

- A database names its environment in `environment`.
- The name must be declared under `environments`. A typo (`prdo`) is an error that is logged at startup and the
  database gets only the objectives it sets itself: not its environment's, which do not exist and not the fleet
  default as a stand-in.
- A name is lowercase letters, digits, `-` and `_`, at most 32 characters.

An environment does not imply a tag and a tag does not imply an environment. A database can have both.

## 3. Values

- **Durations** are strings: `"15m"`, `"1h30m"`, `"90s"` and a days unit, `"1d"` or `"1d12h"`. A bare number is
  invalid: `900` would be seconds to one reader and minutes to another. So is a sign after the days (`"1d-5m"`).
- **`rpo`** may be `"0s"`: no data loss tolerated. It is a value, not an absence and does not fall through to the
  next level.
- **`rto`** must be greater than zero.
- **`availability_slo`** is a percentage greater than 0 and at most 100.
- **`availability_window`** is a duration greater than zero: the rolling window the SLO is measured over. Unset
  means 30 days and everything that shows a budget says when the window is that default. An unreadable window
  is never replaced by the default: no budget is computed and the problem is logged. Together with the SLO it
  gives the downtime budget ([§5.2](#52-the-availability-budget)).
- **`recovery_preference`** is `availability_first` or `data_first`: what to favour when a recovery has to choose
  between getting the database back sooner and losing less data. It will decide how a recommendation is ranked
  and how an option that breaches an objective is treated. It never decides to act. Unset means no preference.
- **`enforcement`** is `warn`, `acknowledge` or `refuse`: what happens when a recovery would breach an objective.
  `warn` says so and blocks nothing; `acknowledge` makes the person approving acknowledge the breach separately;
  `refuse` refuses the recovery. Unset means `warn`, which is the behaviour from before it existed. An invalid
  value, inherited or not, is treated as `acknowledge`, not `warn`. So is a database that names an environment
  that is not declared and sets no enforcement of its own, because it cannot tell which enforcement its real
  environment would have set. A typo must not quietly weaken the check. What a database sets itself is always
  honoured. **`enforcement` governs the `rpo` only**: it acts on the estimated data loss of a point-in-time
  restore before the instance is stopped. The downtime a restore costs has its own setting,
  `availability_enforcement`, below and the `rto` is shown against what a recovery took, after the fact.
- **`availability_enforcement`** is `warn`, `acknowledge` or `refuse` and says what happens when a restore is
  expected to breach the availability budget ([§5.4](#54-the-budget-at-the-gate)). It is its own setting and not
  `enforcement`'s, because an existing `enforcement: refuse` means "refuse a restore that loses too much data" and
  must not start refusing restores for a budget it never named. Unset means `warn`. It follows the same rules as
  `enforcement`: an invalid value, inherited or not and a database that names an environment that is not declared
  and sets none of its own, are treated as `acknowledge`, never as `warn`.
- Both are exact lowercase strings. `Refuse` is an error, not a guess.

Both settings resolve like the objectives (database, then environment, then fleet default, each on its own), so a
production environment can be `data_first` with `acknowledge` while one database in it overrides to `refuse`.

A config that sets none of these loads exactly as before.

## 4. A mistake never costs the inventory

Most services treat an `infrastructure.json` that fails to load as "no infrastructure config" and carry on: no
target resolution, no policy tags, no approval rules. So a mistake in an objective must not fail the load and it
does not. The file loads, the inventory is intact and:

- the gateway and the sysadmin agent log every problem at startup, at error level, one line each, naming the
  setting (`db_servers.orders.objectives: rpo must not be negative`);
- an **invalid value blocks its own objective**: it resolves as unset, with the reason in `problems` and does
  not fall through to the next level, which would pass a typo off as the fleet default;
- the other objectives of the same database are unaffected.

A caller that finds an objective unset must look at `problems` before treating it as "no objective".

## 5. Reading it in code

`infra.Config.ResolveObjectives(<db_servers key>)` returns the resolved objectives for one database, each with
`Set` and `Source` and `Problems`. A database that is not configured returns `ok=false`; a configured one with
nothing set returns `Set=false` for each objective and no problems. `Config.Validate()` lists every problem in the
file and `Config.LogProblems(slog.Error)` logs them.

### 5.1 Which incidents spend the availability budget

An availability budget (what `availability_slo` allows a database to be down in a window) must be charged only for
incidents that make the database unavailable. A vacuum or a cache fix is an incident, but not downtime and a restore is
both. So every playbook series declares an **availability impact**, in `playbooks/availability_impact.go`:

| Impact | Meaning | Examples |
|---|---|---|
| `outage` | The database is not serving: down, restarting, being restored, unreachable or without a running pod. | restore from backup, point-in-time restore, database restart, pod crash, StatefulSet scaled to zero, WAL disk full |
| `degraded` | Serving, but some clients or queries fail or are slow. | lock chains, rejected logins, exhausted connections, replication lag, a replica restart |
| `none` | No client-visible effect. | backup health, vacuum, cache and checkpoint tuning, stale slots, disk growth |

Only `outage` is charged to the budget. The others are listed beside it, uncharged, so nothing is hidden.

- The declaration is per series, not per playbook version and lives in one table rather than in each playbook's YAML:
  the seeder skips a `(series, version)` it already holds, so a YAML field would need a version bump of every playbook
  to reach an existing store, which would also split each playbook's per-version statistics.
- An incident spans several runs, so its impact is the **worst** of its hops (`playbooks.WorstImpact`): a triage that
  only read the database (`none`) does not hide the restore (`outage`) that followed it. A PITR triage that never
  reached the restore stays `none`.
- A series with no declaration (a draft, an imported or a hand-made playbook) is **unclassified**: it is never charged
  and never guessed and the ledger reports it as unclassified.
- The values are judgements, made so that a restore counts as the outage it is and a vacuum does not. Review them
  against how an incident of that kind looks to your clients. A test fails when a system playbook is added without one.

### 5.2 The availability budget

An `rto` says how long one recovery may take. It says nothing about how often a failure may happen and the two
together decide whether the SLO can be met at all. The SLO and its window give the total downtime a database may
have and the `rto` says how much of it one recovery can use:

```
budget               = window x (100 - availability_slo) / 100
recoveries absorbed  = floor(budget / rto)
```

| SLO over 30 days | Budget | A 30m `rto` |
|---|---|---|
| 99% | 7h 12m | uses 7% of it, tolerates 14 recoveries |
| 99.9% | 43m 12s | uses 69%, tolerates **1** recovery |
| 99.95% | 21m 36s | longer than the whole budget: **contradictory** |
| 99.99% | 4m 19s | longer than the whole budget: **contradictory** |

An `rto` longer than the whole budget means one recovery that takes the full `rto` breaks the SLO: the two
declared numbers cannot both be met. aiHelpDesk says so, but blocks nothing and does not change either number: it is
your objective and the fix is yours (a wider window, a lower SLO or a faster recovery).

- **Where it is stated**   
  `LogProblems` logs it at startup for the gateway and the sysadmin agent, once per database
  (`infrastructure config: availability objectives contradict each other`); `infra.Config.Consistency()` lists the
  same findings; and the RECOVERY DECISION section of `vault incidents` shows a `budget:` line (with a `⚠` when
  contradictory) beneath the objectives in force. The recovery decision records the window, where it came from and
  the budget, so the record shows the numbers the approver saw.

- **The default window is a default**   
  With no `availability_window` the window is 30 days, rolling and every
  statement says `(default window)` and the record says `built-in default`. A calendar month would need the
  month's start, which no setting carries.

- **A typo never becomes a default**   
  An unreadable `availability_slo` or `availability_window` or a database in an
  environment that is not declared (whose own slo or window might have been the environment's), computes no
  budget and says why. It never falls back to the default window or to "no objective".

- **Old events are not rewritten**   
  A decision recorded before v0.35 has no window and gets no budget line; the
  default is not assumed after the fact.

- **What this is not**   
  It is arithmetic on declared numbers. It does not measure downtime, count incidents already
  in the window or forecast: a recovery that finishes faster than its `rto` uses less than the share shown and one
  that fails uses more. The budget already spent comes from the ledger, which is not built yet.

### 5.3 The availability ledger

The budget is what the SLO allows. The ledger is how much of it the database's recorded incidents have already used
in the window. `GET /api/v1/availability-ledger?database=<key>` returns it and `faulttest vault budget <database>`
prints it.

```
Availability budget: prod-orders
  Window   30d [environment:prod], as of 2026-11-10 12:00 UTC
  Budget   availability 99.9% over 30d allows 43m12s of downtime per window; rto 30m uses 69% of it: tolerates 1 recovery per window at the rto
  Spent    20m 00s in 1 outage incident(s), 46% of the budget
  Left     23m 12s
```

What counts and what is said about what does not:

- **Real incidents only**   
  A faulttest injection never counts and the ledger says how many it left out.

- **Only outages are charged**   
  An incident is classified by the worst playbook among all the runs it spanned
  ([§5.1](#51-which-incidents-spend-the-availability-budget)). Degraded and harmless incidents and ones whose
  playbooks declare nothing (`unclassified`), are listed beside the charge and cost nothing.

- **Only a confirmed recovery is charged**   
  An outage that ended `escalated`, `needs_human` or `abandoned` or that has
  stayed open for more than a day (almost certainly a run that never finished), has an unknown real downtime. It is
  listed as **unconfirmed** and not charged and `remaining_worst_case` says what is left if all of it counts. An outage
  still open and younger than a day is charged up to now.

- **Duration is from detection to recovery**   
  It runs from the start of the incident's entry playbook run to the end of
  its last run, so it includes the wait for approvals and begins when aiHelpDesk was engaged, not when the failure
  began. It is not measured downtime and the output says so every time.

- **Only the part inside the window is spent**   
  An incident that began before the window is clipped to it and one that
  ended before it is not counted.

- **A database is found the way the agents find it**   
  `database` may be a key, a name, a container name or a connection
  string; a bare port two servers share is refused (`409`). An incident is attributed to a database once, when it is
  created ([INCIDENTS.md](INCIDENTS.md)); incidents that could not be attributed are in no database's ledger and the
  ledger says how many there were. Incidents recorded before v0.35 have no database and are among them.

- **It never guesses**   
  If the incidents cannot be read from auditd the answer is an error, not an empty ledger. If the
  page was full the ledger says it may be missing incidents. With no budget (no `availability_slo` or one that cannot
  be trusted) the spend is still listed and nothing is shown as remaining.

### 5.4 The budget at the gate

A point-in-time restore costs downtime as well as data. Before it stops anything, the sysadmin agent sets what is
left of the downtime budget (§5.3) against the restore, next to the data-loss estimate set against the `rpo` and the
plan carries the result as `availability_budget` (`helpdesk-client --pitr-plan` prints it as `BUDGET CHECK`).

It is evaluated in the agent, not at the gateway, for two reasons: only the agent knows whether the instance is
**serving** and it already measures again just before the stop, so the ledger is read at that moment too. The
gateway's own ledger endpoint and the agent read it with the same code.

How long the restore will take is not known: until there is an estimate from history, the declared `rto` stands in
for it, as an upper bound. So a restore that does not fit in what is left only **may** exceed the budget (a restore
can beat its `rto`) and that never refuses. Only a budget that is already spent is a certain breach.

| Verdict | When |
|---|---|
| `within` | what is left of the budget is at least the `rto` |
| `may_exceed` | some is left, but less than the `rto` |
| `exceeds` | the budget is already spent (nothing left or overspent) |
| `not_comparable` | there is budget left but no `rto` is declared, so how long the restore may take is not known |
| `no_objective` | no `availability_slo` is set or the target is not a configured database |
| `objective_invalid` | the SLO or its window was set and could not be read |
| `ledger_unavailable` | the budget is known but how much of it has been spent could not be read (no audit service or it failed) |

The verdict is made on the **confirmed** spend. Outages whose real length is unknown (§5.3) are not counted, so a
guess cannot refuse a restore, but the detail says how many there are and what would be left if they counted in full.

What approving demands is `availability_enforcement`:

| `availability_enforcement` | `exceeds` | `may_exceed`, `objective_invalid`, `ledger_unavailable` | anything else |
|---|---|---|---|
| `warn` (the default) | shown | shown | shown |
| `acknowledge` | needs `acknowledge_objective_breach` | needs `acknowledge_objective_breach` | shown |
| `refuse` | the restore is refused, whatever is acknowledged, **if the instance is serving**; otherwise it needs `acknowledge_objective_breach` | needs `acknowledge_objective_breach` (asks, never refuses) | shown |

- **Refusing is only coherent for a serving instance**   
  The restore is then what causes the downtime and refusing
  keeps it serving. An instance that is already down accrues the downtime whether or not it is restored, so refusing
  would leave it down and spend the budget faster. There a `refuse` asks for the acknowledgement instead and the plan
  says why.

- **What cannot be checked asks and does not refuse**   
  An unreadable objective and an unreadable spend are not "nothing
  to check": a typo or a dead audit service must not quietly switch a strict enforcement off. Nothing is known to be
  breached, so they ask.

- **One acknowledgement covers every breach shown at the gate**   
  The gate has one field, `acknowledge_objective_breach`.
  A refusal from either objective stands whatever is acknowledged. When both ask, the error names both and the
  recovery decision records which objectives stood in the way (`approval.breaches`: `rpo`, `availability_budget`), so
  that acknowledging a data-loss breach is never taken to have acknowledged a budget breach too.

- **It is recorded**   
  Each option on the plan and execution decisions carries `budget_verdict`, `budget_gate`,
  `budget_remaining_seconds` and the sentence that made it; the objectives in force carry the effective
  `availability_enforcement`. `vault incidents` shows it on the option line, apart from the loss verdict.

What this does not do: it does not predict the restore's duration (the `rto` is an upper bound), it does not count
downtime that is not in a recorded incident and it does not choose between restoring fast and restoring precisely
(`recovery_preference` has no consumer yet).

### 5.5 The recovery-time estimate

The `rto` is a promise someone wrote down. The estimate is what has actually happened: how long this **server's**
restores have taken, from its own history, shown beside the promise. The plan carries it as
`availability_budget.recovery_estimate` and `helpdesk-client --pitr-plan` prints it under `Past restores`.

**"Server", not "database"**   
Everywhere in the objectives, "the database" means one `db_servers` entry: one PostgreSQL
server (the instance or cluster), because that is what a restore restores (`pgbackrest --delta restore` with no
`--db-include`: every database in the cluster comes back or none does). Objectives, incident attribution, the downtime
ledger and this estimate are all kept per entry. Nothing here is per database inside a server and nothing claims to be.
Two entries that reach the same host and port are one machine with two separate budgets and histories; the config says
so at startup (a warning, not an error).

What it measures is the same duration the outcome stage of a recovery decision records and compares with the `rto`:
the remediation run that did the restore (restore, restart, verify), which begins **after** the approval. It is not the
downtime, which began earlier and includes the wait for an approval.

```
Past restores      : 5 resolved restores of this server in the last 90d (real incidents): median 20m, longest 45m (too few for a p90, which needs 10)
RTO CHECK (!)      : exceeds: 2 of the 5 past restores took longer than the rto of 30m (the longest took 45m)
```

The rules that keep it honest:

- **Real incidents only**   
  A faulttest injection never counts. So a database that has only ever been restored by
  faulttest has no history and says so.

- **Only restores that ran**   
  Runs of the restore playbooks (`pbs_pgbackrest_pitr_remediate` and
  `pbs_pgbackrest_restore_remediate`, which are dominated by the same thing: the size of the data) that ended
  `resolved`. For a remediation run that means its own trace shows a write or destructive tool call that succeeded:
  the restore itself ran. A restore that failed or did nothing (its precondition failed), took a time too, but was not
  a recovery; the count of runs left out is stated. `resolved` does not mean the verification afterwards passed: a
  restore that ran and then reported a recovery mismatch is still counted, as the time it took to do the restore.

- **Only this server**   
  Another server's restores say little about this one's, so there is no fallback to the
  fleet. With fewer than three samples the status is `insufficient_history` and with none `no_history`; the declared
  `rto` then stands alone, exactly as before.

- **What it summarises depends on how much there is**   
  From three samples: the median, the longest and the restore is
  planned for as long as the **longest**. From ten: a real p90 (nearest-rank) and the restore is planned for the p90.
  Below ten the p90 would be the longest under another name (by nearest rank the p90 of nine samples *is* the longest),
  so it is not offered and not called one. The sample size and the 90-day window are always beside the numbers. A
  history that cannot be read is reported as `unavailable`, never as no history.

- **The `rto` comparison is a count**   
  `vs_rto` is `within` when none of the counted restores took longer than the
  declared `rto` and `exceeds` when some did, with how many: the declared number against the demonstrated one.

**What it changes.**   
In the budget check (§5.4) a restore is taken to last as long as the estimate plans for (its
**p90** from ten samples, its **longest** from three to nine) when there is an estimate and as long as the declared
`rto`, as an upper bound, when there is not. The verdict says which (`duration_basis`: `history_p90`,
`history_longest` or `declared_rto`) and the decision record keeps it, with the longest (`estimate_max_seconds`)
and, from ten samples, the p90. Records from before this say `history`, which meant the p90. The estimate only moves a `within`
to a `may_exceed` and back: it never refuses a restore, because a `may_exceed` asks and does not refuse and a spent
budget is a breach whatever history says.

**What it does not do.**   
It holds nothing by itself: a history that breaks the `rto` is shown at the gate (`RTO CHECK
(!)`) and recorded and is not enforced. It covers restores only, not a restart or a scale-up. And it is only as good as
the incidents behind it: real history is thin and a server restored three times in a year has three samples. Two
more limits are not corrected for. A restore's time is roughly the size of the data over the throughput, plus the WAL
to replay, so a history from when the server was half its size is optimistic and a restore to ten minutes ago is not
a restore to three days ago; both kinds are counted together and the restored size is not recorded yet (v0.36).

### 5.6 Is the backup posture enough for the objectives?

`backup_stale` in `get_pgbackrest_status` answers a question about the backup **job**: is the newest backup older than
an age somebody passed in? A server can pass that and still be unable to meet what was promised. So the same result
carries a second, separate answer, `objective_exposure`, which sets the backup posture against the declared objectives:

```json
"objective_exposure": {
  "status": "exposed",
  "rpo": {"status": "may_exceed", "objective_seconds": 900, "measured_seconds": 7200,
          "detail": "the newest archived WAL reached the repository 2h ago, more than the rpo of 15m: anything committed since is not archived, ... That is an upper bound: an idle server writes no WAL ..."},
  "rto": {"status": "within", "objective_seconds": 1800, "measured_seconds": 1500,
          "detail": "none of the 3 past restores took longer than the rto of 30m (the longest took 25m)"}
}
```

- **The `rpo` check** is the age of the newest archived WAL against the `rpo`: what is not yet in the archive is what
  losing the server now would lose. It is an **upper bound**, never a proven breach, because an idle server writes
  no WAL and an old archive can then mean nothing is unarchived. So over the `rpo` the status is `may_exceed`, in the
  same words as at the restore gate and the exact figure for a live server stays with the point-in-time plan.
- **The `rto` check** is the longest past restore of this server (§5.5, real incidents only) against the `rto`:
  `within` when none took longer, `exceeds` when some did. With too little history or none or a history that could
  not be read, it says `not_comparable` and why. It is never "within" by default.
- **`status`** is `exposed` when either check says so, `within` when what could be checked held (a check that cannot
  be made does not count against it and does not count for it) and `not_assessed` when nothing could be checked.
- **Additive, deliberately.** `backup_stale` keeps its meaning and its name: playbook endings and cited evidence are
  written against it and changing what it says would change what they do. The new block is present only for a target
  that is a configured server declaring at least one of the two objectives, so a server that declared nothing is told
  nothing about objectives nobody wrote.

**What it does not do.**   
It is information: it refuses nothing and no rule forces a gate on it. There is no standing
alert in the sense of a process that watches and notifies: nothing in the product schedules work, scheduled runs are
a CronJob (or any scheduler) calling a playbook or tool, as for any other scheduled check (see
[FLEET.md](FLEET.md)).

**The consumer: `pbs_pgbackrest_objective_exposure_triage`.** A read-only playbook that calls `get_pgbackrest_status`
and files the `objective_exposure` block under one of five endings: within, not assessed, the history breaks the `rto`,
the archive is older than the `rpo` or the status could not be read at all (the check did not run). It never restores and never takes a backup (it has no transitions and its
prose names no mutating tool, both pinned by tests); an exposed server goes to a person. It is classified `none` for
the availability budget. Start it by hand or from a CronJob calling the gateway. **One run records one incident**
(every entry-point playbook run does), so schedule it daily, not every few minutes: only the within and
not-assessed endings carry `recommended=no_changes_needed` and are recorded `resolved`; the two exposed endings and
the could-not-read ending are recorded `needs_human`, which is what they are. Each run also costs one root-cause classification call when the
gateway has a planner model. No gate rule is attached to the block yet: the `rpo` side is an upper bound and would
gate healthy idle servers.

What "none" and "costs nothing" mean here, since they are easy to over-read:   
  * The ledger's `none` list is the incidents whose playbooks are declared to have no availability impact ([§5.1](#51-which-incidents-spend-the-availability-budget)):
they are listed beside the charge and **never spend the downtime budget**, whatever their status (`resolved` for the
quiet endings, `needs_human` for the exposed ones).   
  * That is the only thing that costs nothing 😉. A run is not free otherwise: it records an incident, a model reads the status and writes the finding (a few seconds and tokens), a
root-cause classification call is made when the gateway has a planner model and an incident bundle is written when
automatic bundles are on.

**Live runs and what they show**   
Against the Ubuntu VM the playbook ran many times while it was being written, each run changing the wording: an unreachable VM (the model improvised, which added the
"could-not-read" ending and the "first and only tool call" rule), an archive older than the `rpo` (the model asserted
the server was idle, then built an evidence quote by joining fields, then added a second hypothesis only to reject it:
the prescribed measurement wording, the verbatim-quote rule and the one-hypothesis rule).   

These runs against the remote VM are in the ledger's `none` list, each `needs_human`, 13 to 20 seconds. Against a Docker container (AlloyDB Omni,
PostgreSQL 17.7, through the Compose stack, `rpo` 10m) the quiet ending ran: `exposure=within`, the archive 3m 38s old,
the `rto` `not_comparable`, one hypothesis, one tool call, recorded `resolved` origin `real`, 10 seconds and in the
ledger's `none` list.   

Its attribution was `objectives-not-assessed`, which was wrong: the class description named "not
enough restore history" as a reason, so a run with the `rpo` checked and within was filed as not assessed. The
descriptions were rewritten since (n playbook 1.5).

## 6. Design: how objectives will be used (not built yet)

Sections 1-5 are built. This section is the design the next steps follow. Everything in it is marked built or not,
so it can be checked against the code.

### 6.1 One decision loop

Objectives are declared. aiHelpDesk then **observes** (archive lag, backup age, past recovery durations, downtime
recorded in the window) and **estimates** (data loss, recovery time, budget burn) for each candidate recovery
action and sets those against the declared values. Every recovery decision becomes a comparison between options,
each described by three numbers: expected data loss, expected downtime and the share of the availability budget
it spends.

| Piece | Status |
|---|---|
| `rpo`, `rto`, `availability_slo` declared, three-level resolution with sources | Built |
| Data-loss estimate for PITR, set against the `rpo` | Built ([PITR.md §4](PITR.md#4-operating-it)) |
| Enforcement of the `rpo` at the PITR gate: `warn`, `acknowledge` (`acknowledge_objective_breach`) or `refuse`, with the loss re-measured just before the instance is stopped ([PITR.md §4](PITR.md#4-operating-it)) | Built |
| `recovery_preference` and `enforcement` in the config: validated, resolved per database, environment and fleet with sources | Built |
| `enforcement` read by the PITR restore (above) | Built |
| Each stage of a recovery decision recorded as a typed, hash-covered `recovery_decision` audit event, with a snapshot of the objectives in force and the config digest and shown in the incident narrative, `vault incidents` and `vault journey` ([AUDIT.md §4.6](AUDIT.md#46-recovery_decision-fields)) | Built |
| A lower bound on the loss of an LSN target, from when its archive segment arrived, so a proven breach of the `rpo` is enforced like any other ([PITR.md](PITR.md#lsn-targets-and-the-lower-bound)). An LSN in WAL that is not archived yet is still not enforced | Built |
| The outcome of the recovery recorded on that decision: how the remediation hop ended and its **recovery duration** (after the approval: restore, restart, verify), shown against the `rto` in `vault incidents`. Not the downtime, which is longer | Built |
| A decision that cannot be written to the audit trail is logged at error level and says so in the plan or result (`decision_unrecorded`); the restore is not held up for it | Built |
| An audit spool, so an unwritable record is kept and replayed; fail-closed when auditing is configured but unreachable, with a human break-glass override at the gate | Not built (v0.35) |
| Actual downtime (not just the recovery hop's duration) recorded against the `rto` | Not built |
| `recovery_preference` read by anything (ranking recommendations) | Not built |
| A window on `availability_slo`, the downtime budget it gives, the number of worst-case recoveries it absorbs (budget ÷ `rto`) and a check that flags an `rto` longer than the whole budget; read from declared numbers only ([§5.2](#52-the-availability-budget)), shown in the recovery decision's objectives | Built |
| A ledger of the downtime budget already spent in the window, from recorded real incidents, charged by availability impact, with unconfirmed outages listed apart ([§5.3](#53-the-availability-ledger)) | Built |
| The remaining budget shown to the person at the gate (`helpdesk-client --pitr-plan` and recorded on the plan and execution decisions) | Built |
| A restore expected to breach the remaining availability budget is held to `acknowledge` or `refuse` by its own setting, `availability_enforcement` (default `warn`); refuse only for a serving instance; one acknowledgement covers every breach and the record says which there were ([§5.4](#54-the-budget-at-the-gate)) | Built |
| The budget check applied to any tool declared `impact: outage` in the tool registry (planned restarts, scale-downs, failover), not only the restore: a spent budget argues against voluntary downtime, never against recovery | Not built (v0.36) |
| The `rto` itself enforced at the gate (it stands in for the restore's duration, as an upper bound, in the budget check; nothing else compares it before the restore) | Not built |
| Recovery-time estimate for a restore, from this server's past real restores (median and longest from three samples, a p90 only from ten, with the sample size and the window), "unknown" below three samples, set against the declared `rto` as a count of the past restores that broke it and used as the restore's duration in the budget check ([§5.5](#55-the-recovery-time-estimate)) | Built (restores only) |
| A recovery-time estimate for recoveries other than a restore (a restart, a scale-up) | Not built |
| The number of transactions a point-in-time restore discards, counted from the WAL with `pg_waldump` at plan time, from where recovery would stop (PostgreSQL's own rule), exact for a serving instance read to its own position and "at least" otherwise, with the reason when it cannot be made, kept on the audit record; can be turned off ([PITR.md §4](PITR.md#4-operating-it)) | Built (verified end to end on real WAL in the Docker fixture and on the Ubuntu VM over SSH, on a K8s Pod and in a Docker container on PostgreSQL 17.7; not yet verified: a prepared transaction, the cost over many busy segments, a managed database) |
| The same count measured at the restore itself, after the stop, so the record says how many transactions were actually discarded, not how many were at stake at plan time | Not built |
| Reading a range across a point-in-time restore on the instance's older timelines and a read longer than 16 segments (only the first 16 are read and the count says "at least") | Partly built (timelines are followed from the replay path; the cap is a starting figure to be set from a measured read) |
| The estimate enforced: a restore whose history breaks the `rto` held to `acknowledge` or `refuse` | Not built |
| Backup posture against the objectives in `get_pgbackrest_status` (`objective_exposure`: the age of the newest archived WAL against the `rpo`, an upper bound and the longest past restore against the `rto`), beside an unchanged `backup_stale` ([§5.6](#56-is-the-backup-posture-enough-for-the-objectives)) | Built (information only) |
| A playbook that acts on `objective_exposure` (today `backup_stale` is what the backup-health playbooks end on) | Not built |
| A standing alert: a scheduled run of the above that notifies when the posture is `exposed`, with delivery and de-duplication (needs a scheduler; nothing in the product schedules work today) | Not built |
| An exact `rpo` exposure for a live server (unarchived WAL, from the current LSN and `pg_stat_archiver`) as a recurring measurement, not only inside the point-in-time plan | Not built |
| Per-incident outcome (actual loss, duration, budget burn) recorded against the objective | Not built |

What it must **not** do: choose the objectives or adjust them toward what it measures (a system that does that is
grading itself) or run a destructive action without the existing approval. Objectives constrain what is
recommended and how strict the gate is. Feasibility advice ("your `archive_timeout` bounds your rpo at 5 minutes")
is in scope, as advice a person approves.

### 6.2 How it connects to the Vault Flywheel

The [Flywheel](VAULT.md#the-operational-sredba-flywheel) is an **inner loop** about competence: does the system
diagnose and fix better after each incident? It measures that in its own units (accuracy rate, resolution rate,
step count, recovery time per playbook version). It has no notion of whether that improvement matters to the
business. The objectives are the **outer loop** about accountability: were the promises kept? They have the
yardstick and no engine for getting better. Each supplies what the other lacks:

| Connection | Direction | Status |
|---|---|---|
| The recovery-time estimate is built from the history of real recoveries of that database (p50/p90) and improves as incidents accumulate | Vault → objectives | Built for restores ([§5.5](#55-the-recovery-time-estimate)): from the incidents' own runs, per database, not per playbook version; a restore that has never happened has no history |
| A playbook's resolution rate says how likely an option is to work, so an option's expected downtime includes the chance it fails | Vault → objectives | Not built |
| The Vault's review queue (drafts waiting for a human) is ordered by budget burned or objective breached | Objectives → Vault | Not built |
| Certification (STABLE on pass rate and spread) also requires "recovers within the environment's `rto` at p90" | Objectives → Vault | Not built |
| The flywheel's improvement is stated in the customer's terms: "about 25% of a month's downtime budget saved per incident" | Both | Not built |
| A declared-versus-demonstrated report: "RTO 30 min, met 40% of the time, p90 52 min", for a person to act on (invest in HA or renegotiate; the system never changes the objective) | Both | Not built |
| Practice restores (drills) on a clone of the server feed the estimate, **labelled as rehearsals and kept apart from real history** (never merged into it), so a server that is rarely restored still has a number. Proposed, not decided: needs a declared link from the clone to the server, a data-size comparison and a recency limit | Both | Not built (proposed) |

**What closes the loop is a person.**   
The system measures and shows the gap between what was declared and what has been
demonstrated; a human acts on it (more resources, a faster backup path, a better playbook or a more honest objective);
the next recoveries then measure the result. The system never changes an objective. Without the "act, then measure
again" half there is only a log of what happened.

**The unit is a server.**   
"Database" in this section and in §5 means one `db_servers` entry, a whole PostgreSQL server
(see §5.5): that is what an objective is declared for, what an incident is attributed to, what a restore restores and
what the budget and the estimate are kept for. Per-database figures inside a server are not claimed.

Here's the visual of how we think about relationship between the inner/flywheel and the outer/objectives loops:

```
    INNER (Vault flywheel)                      OUTER (objectives)
    playbooks get better  ── durations, ──►  RTO estimate, budget burn,
    per incident             resolution         "demonstrated vs declared"
                             rates
            ▲                                          │
            └──── what to review/certify first ◄───────┘
                  (the incidents that cost the most budget)
```

### 6.3 What this depends on and what could mislead

- **`pg_waldump` for the exact loss (Tier B)**   
  Checked on the local fixtures. It is in the server package, so any image or host that runs PostgreSQL has it: present at the server's own version in the Docker
  and K8s fixture image (Debian, `postgresql-16`, on `PATH`), in `postgres:16-alpine`
  (`/usr/local/bin`) and in AlloyDB Omni 17 (`/usr/lib/postgresql/17/bin`).   

  Fetching an archived segment with `pgbackrest archive-get` into a temp directory and reading it with `pg_waldump` worked on the fixture: it
  prints every `COMMIT` with its LSN, transaction id and an exact timestamp, so an LSN maps to a time and commits
  can be counted.   

  **Checked on a real systemd host**   
  (Ubuntu 24.04, PostgreSQL 16.15 from PGDG): the binary is `/usr/lib/postgresql/16/bin/pg_waldump`, not on `PATH` for the `postgres` user, so it is found by path and its
  version matched to the server's (it was); `pgbackrest archive-get` of the newest archived segment works as
  `postgres` and hands over a decompressed 16 MiB segment.   

   Three things the fixtures did not show:   
    * the commit time ends in a zone **abbreviation** of the host's own `TZ` (`EST`), which is ambiguous, so the reader runs
  `pg_waldump` with `TZ=UTC` and refuses any other abbreviation (the commit is still counted, with no time)  

   * a DDL commit line was 1245 bytes of invalidation messages, so the reader has no line limit  

   * a file in `pg_wal` is not necessarily WAL (PostgreSQL recycles old files into the names of segments that do not exist yet and
  `pg_waldump` answered "could not find a valid record" on one), so what lies past the instance's own
  `pg_current_wal_lsn()` is never read.   

   **The three commits**   
   (three commits made on purpose, xids 785-787) verified the rest of the read path:
   * `TZ=UTC` changes the printed zone from `EST` to `UTC` and the times then agree with the host's clock to the second  

   * a read bounded with `-e` at the instance's own position exits 0 with nothing on stderr, while an unbounded read of
  the live segment ends with exit 1 and `invalid record length at <lsn>: expected at least 24, got 0`: the end of the
  written WAL. The reader treats it as a normal end only for that zero length and any other damage or unknown message as
  a failure  

   * the archive's WAL is **per timeline**: after a point-in-time restore the segments before the new timeline's first
  one are on the earlier timelines, so `archive-get` of the preceding segment numbers on the newest timeline fails
  ("could not locate WAL file") and a range that crosses a restore must be read timeline by timeline, not as one run
  of segment numbers  

   **The whole path e2e verification against real WAL**   
   In a Docker fixture, an integration test, run repeatedly: commits made before and after a target time, archived, then read by the real script through the real container with the real
  `pg_waldump` and `pgbackrest archive-get`. The commits before the target were read and not counted, the ones after it
  were counted and the commits that existed only in the instance's own `pg_wal` (never archived) were counted by the
  serving read and not by the archive-only one. It also found something worth knowing: a transaction that took an id
  and wrote nothing else (`SELECT txid_current()`) commits asynchronously, so its record can still be unwritten when the
  instance is asked where its WAL ends and a commit is only counted once written (see [PITR.md §4](PITR.md#4-operating-it)).  

  The first costs: reading one archived segment took 0.1-0.3 s and one archived plus one live segment 0.2-0.6 s, on a
  nearly empty fixture, which says little about a busy database.

   **How stable is what is read?**   
   Two different things and only one of them is a risk here.
   * The WAL **file** format is not stable across major versions (each one changes its page magic and record layouts
  change), so `pg_waldump` must be the same major version as the server that wrote the WAL. The script picks the
  binary by the server's major version (`PG_VERSION`) and a mismatch makes `pg_waldump` fail, which is a failed
  read with its reason, never a wrong count.
   * The **text** `pg_waldump` prints is what is parsed: the `rmgr: Transaction … tx: N, lsn: X/Y … desc: COMMIT|ABORT
  <timestamp> <zone>` line and the flags `-r -s -e -n -p`. It is not a documented stable interface, but it has not
  changed in anything this reads. Compared on **16.14** (Debian), **16.15** (Ubuntu) and **18.6** (Debian, the audit
  store's container, read-only): the same line layout, the same `COMMIT` and `ABORT` forms (the aborts are real captures
  from 18), the same zone behaviour (`UTC` under `TZ=UTC`, an abbreviation such as `EST` otherwise), the same bounded
  read (exit 0) and the same unbounded end (exit 1, `invalid record length … expected at least 24, got 0`), the same
  `-p DIR START END` form. Run, but not compared line by line: **17.7** (an AlloyDB Omni container; the count came out
  exact, see below). Not compared: 13, 14, 15.
   * Format drift is guarded, not assumed away. A Transaction record that is not a commit or abort in the form read
  and is not one of the kinds that end no transaction (`ASSIGNMENT`, `INVALIDATIONS`, `PREPARE`) is counted as not
  understood, so the count becomes "at least"; a read in which records were seen and none could be taken for a commit
  or an abort is reported as not understood and not as an exact zero (this was a gap: only lines containing `COMMIT` or
  `ABORT` used to count as not understood, so a renamed description would have read as no transactions). The decoder
  that produced a count is kept with it (`transactions.decoder`).

   **And on the remote Linux host/VM accessed over SSH**   
   PostgreSQL 16.15, the in-cluster SysAdmin Agent): five commits made after a target time, then `plan_point_in_time_recovery` for that time. It reported
  `feasible` with `transactions.status: measured`, **5 commits**, exact (no `at_least`: a serving instance read to its
  own position), the first at the second the inserts ran and nothing counted from before the target (the table's
  `CREATE TABLE` precedes it). The five commit LSNs (`0/45019588` to `0/45019728`) sit directly before the position the
  `pg_switch_wal()` run after them returned (`0/450197A0`), which is independent evidence that the right records were
  counted. Three segments (two archived, one live from `pg_wal`) were read in **181 ms**, on a quiet VM. The decoder
  recorded was `pg_waldump (PostgreSQL) 16.15`.

   **And on a K8s Pod**   
  (`pgbackrest-demo-0`, PostgreSQL 16.14 Debian, the in-cluster SysAdmin Agent, `kubectl exec`):
  the same procedure. `feasible`, `measured`, **5 commits**, exact, all at `19:08:37Z`, after the target `19:08:21Z`.
  The commit LSNs (`0/90174D0` to `0/90176A8`) sit directly before the position the `pg_switch_wal()` run after them
  returned (`0/9017720`). Two segments were read in **1167 ms**, from segment `…09`, the first to reach the repository
  at or after the target time. The decoder recorded was `pg_waldump (PostgreSQL) 16.14 (Debian 16.14-1.pgdg13+1)`.

   **And on a Docker container, through the Compose stack**   
  (an AlloyDB Omni container, PostgreSQL 17.7, local Docker Desktop, the Compose stack's SysAdmin Agent reaching it with
  `docker exec`; archiving and a pgBackRest stanza were switched on for the test): the same procedure. `feasible`,
  `measured`, **5 commits**, exact, all at `14:08:09Z`, after the target `14:07:23Z`. The commit LSNs (`0/36024480` to
  `0/36024640`) sit directly before the position the `pg_switch_wal()` run after them returned (`0/360246B8`). Two
  segments were read in **214 ms**, from segment `…36`, the first to reach the repository at or after the target time.
  The decoder recorded was `pg_waldump (PostgreSQL) 17.7`. This is the first run on PostgreSQL 17.

   Not yet verified: a prepared transaction's commit line (the reader handles the form from
  PostgreSQL's source, not from a capture), how long a read takes over many busy segments and a managed or
  operator-run database, where the binary may be absent. Tier B therefore degrades to the archive-based estimate and records why it could not run and it
  is built that way: a count that could not be made says why and is never "nothing lost".

- **Real versus injected incidents**   
  Faulttest databases are tiny, so injected recovery times flatter any
  estimate. The incidents table already records an `origin` (`real` or `faulttest`), faulttest sets it on every
  run it triggers and `vault incidents --origin` filters on it, so the estimate can read real incidents only.
  Two limits remain. The per-version playbook metrics (resolution rate, recovery time) are still computed over
  both origins together. And `origin` is self-reported, with the same trust model as `skip_trust_gate`: a run
  that fails to set it counts as real. Until the gateway enforces it, an estimate must say which origin it used.

- **Duration depends on database size**   
  An average per playbook across databases means little. The estimate uses
  per-database history, ideally normalised to restore throughput and says "unknown" with none.

- **Data volume**   
  `vault calibration` reports INSUFFICIENT_DATA today. Nothing here may claim that the loop
  "compounds" until there are real numbers behind it.

- **Optimising for speed**   
  A playbook tuned for speed could drop its verification steps. Speed counts only when
  the verification passed and the gate stays a person.

- **Measurement**   
  aiHelpDesk sees only the incidents it handles. Downtime it never detected is not in any
  figure derived from its records, so every availability figure carries that caveat unless an external
  availability signal is ingested.

