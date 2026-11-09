# aiHelpDesk recovery objectives and environments

A recovery you cannot measure against anything is only a report. This is how `infrastructure.json` says what
each database is **supposed** to achieve, so the numbers aiHelpDesk produces (the
[data-loss estimate](PITR.md#4-operating-it) first) can be compared with it.

| Objective | Meaning | Used by |
|---|---|---|
| `rpo` | The most data, as time, that may be lost | The [PITR data-loss estimate](PITR.md#4-operating-it), which is set against it |
| `rto` | The longest a recovery may take | Not consumed yet |
| `availability_slo` | Share of time the database should be available, in percent | Recorded and resolved, not consumed yet |

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
6. [Design: how objectives will be used (not built yet)](#6-design-how-objectives-will-be-used-not-built-yet)
   - [6.1 One decision loop](#61-one-decision-loop)
   - [6.2 How it connects to the Vault Flywheel](#62-how-it-connects-to-the-vault-flywheel)
   - [6.3 What this depends on and what could mislead](#63-what-this-depends-on-and-what-could-mislead)
   - [6.4 Open decisions](#64-open-decisions)

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
    "prod":    { "description": "customer-facing", "objectives": { "rpo": "5m", "rto": "30m", "availability_slo": 99.95 } },
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
- **`recovery_preference`** is `availability_first` or `data_first`: what to favour when a recovery has to choose
  between getting the database back sooner and losing less data. It will decide how a recommendation is ranked
  and how an option that breaches an objective is treated. It never decides to act. Unset means no preference.
- **`enforcement`** is `warn`, `acknowledge` or `refuse`: what happens when a recovery would breach an objective.
  `warn` says so and blocks nothing; `acknowledge` makes the person approving acknowledge the breach separately;
  `refuse` refuses the recovery. Unset means `warn`, which is the behaviour from before it existed. An invalid
  value, inherited or not, is treated as `acknowledge`, not `warn`. So is a database that names an environment
  that is not declared and sets no enforcement of its own, because it cannot tell which enforcement its real
  environment would have set. A typo must not quietly weaken the check. What a database sets itself is always
  honoured.
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
incidents that make the database unavailable. A vacuum or a cache fix is an incident but not downtime, and a restore is
both. So every playbook series declares an **availability impact**, in `playbooks/availability_impact.go`:

| Impact | Meaning | Examples |
|---|---|---|
| `outage` | The database is not serving: down, restarting, being restored, unreachable, or without a running pod. | restore from backup, point-in-time restore, database restart, pod crash, StatefulSet scaled to zero, WAL disk full |
| `degraded` | Serving, but some clients or queries fail or are slow. | lock chains, rejected logins, exhausted connections, replication lag, a replica restart |
| `none` | No client-visible effect. | backup health, vacuum, cache and checkpoint tuning, stale slots, disk growth |

Only `outage` is charged to the budget. The others are listed beside it, uncharged, so nothing is hidden.

- The declaration is per series, not per playbook version, and lives in one table rather than in each playbook's YAML:
  the seeder skips a `(series, version)` it already holds, so a YAML field would need a version bump of every playbook
  to reach an existing store, which would also split each playbook's per-version statistics.
- An incident spans several runs, so its impact is the **worst** of its hops (`playbooks.WorstImpact`): a triage that
  only read the database (`none`) does not hide the restore (`outage`) that followed it. A PITR triage that never
  reached the restore stays `none`.
- A series with no declaration (a draft, an imported or a hand-made playbook) is **unclassified**: it is never charged
  and never guessed, and the ledger reports it as unclassified.
- The values are judgements, made so that a restore counts as the outage it is and a vacuum does not. Review them
  against how an incident of that kind looks to your clients. A test fails when a system playbook is added without one.

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
| Each stage of a recovery decision recorded as a typed, hash-covered `recovery_decision` audit event, with a snapshot of the objectives in force and the config digest, and shown in the incident narrative, `vault incidents` and `vault journey` ([AUDIT.md §4.6](AUDIT.md#46-recovery_decision-fields)) | Built |
| A lower bound on the loss of an LSN target, from when its archive segment arrived, so a proven breach of the `rpo` is enforced like any other ([PITR.md](PITR.md#lsn-targets-and-the-lower-bound)). An LSN in WAL that is not archived yet is still not enforced | Built |
| The outcome of the recovery recorded on that decision: how the remediation hop ended and its **recovery duration** (after the approval: restore, restart, verify), shown against the `rto` in `vault incidents`. Not the downtime, which is longer | Built |
| A decision that cannot be written to the audit trail is logged at error level and says so in the plan or result (`decision_unrecorded`); the restore is not held up for it | Built |
| An audit spool, so an unwritable record is kept and replayed; fail-closed when auditing is configured but unreachable, with a human break-glass override at the gate | Not built (v0.35) |
| Actual downtime (not just the recovery hop's duration) recorded against the `rto` | Not built |
| `recovery_preference` read by anything (ranking recommendations) | Not built |
| A budget window on `availability_slo` and the number of worst-case recoveries it absorbs (budget ÷ `rto`) | Not built |
| Recovery-time estimate, with its basis shown and "unknown" when there is no history | Not built |
| Backup staleness redefined: estimated restore time over the `rto` or archive lag over the `rpo` | Not built |
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
| The recovery-time estimate is built from the history of real recoveries of that database (p50/p90) and improves as incidents accumulate | Vault → objectives | Not built |
| A playbook's resolution rate says how likely an option is to work, so an option's expected downtime includes the chance it fails | Vault → objectives | Not built |
| The Vault's review queue (drafts waiting for a human) is ordered by budget burned or objective breached | Objectives → Vault | Not built |
| Certification (STABLE on pass rate and spread) also requires "recovers within the environment's `rto` at p90" | Objectives → Vault | Not built |
| The flywheel's improvement is stated in the customer's terms: "about 25% of a month's downtime budget saved per incident" | Both | Not built |
| A declared-versus-demonstrated report: "RTO 30 min, met 40% of the time, p90 52 min", for a person to act on (invest in HA or renegotiate; the system never changes the objective) | Both | Not built |

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

- **`pg_waldump` for the exact loss (Tier B).** Checked 2026-11-05 on the local fixtures. It is in the server
  package, so any image or host that runs PostgreSQL has it: present at the server's own version in the Docker
  and Kubernetes fixture image (Debian, `postgresql-16`, on `PATH`), in `postgres:16-alpine`
  (`/usr/local/bin`) and in AlloyDB Omni 17 (`/usr/lib/postgresql/17/bin`). Fetching an archived segment with
  `pgbackrest archive-get` into a temp directory and reading it with `pg_waldump` worked on the fixture: it
  prints every `COMMIT` with its LSN, transaction id and an exact timestamp, so an LSN maps to a time and commits
  can be counted. Not yet verified: a real systemd host (a Debian/Ubuntu PGDG install keeps it in
  `/usr/lib/postgresql/N/bin`, which is not on `PATH` for most users, so it must be found by path and its major
  version matched to the server's) and a managed or operator-run database, where the binary may be absent. Tier B
  must therefore degrade to the archive-based estimate and record why it could not run.
- **Real versus injected incidents.** Faulttest databases are tiny, so injected recovery times flatter any
  estimate. The incidents table already records an `origin` (`real` or `faulttest`), faulttest sets it on every
  run it triggers and `vault incidents --origin` filters on it, so the estimate can read real incidents only.
  Two limits remain. The per-version playbook metrics (resolution rate, recovery time) are still computed over
  both origins together. And `origin` is self-reported, with the same trust model as `skip_trust_gate`: a run
  that fails to set it counts as real. Until the gateway enforces it, an estimate must say which origin it used.
- **Duration depends on database size.** An average per playbook across databases means little. The estimate uses
  per-database history, ideally normalised to restore throughput and says "unknown" with none.
- **Data volume.** `vault calibration` reports INSUFFICIENT_DATA today. Nothing here may claim that the loop
  "compounds" until there are real numbers behind it.
- **Optimising for speed.** A playbook tuned for speed could drop its verification steps. Speed counts only when
  the verification passed and the gate stays a person.
- **Measurement.** aiHelpDesk sees only the incidents it handles. Downtime it never detected is not in any
  figure derived from its records, so every availability figure carries that caveat unless an external
  availability signal is ingested.

### 6.4 Open decisions

1. `recovery_preference`: two values or a rule such as "data-first unless downtime would exceed X"? Leaning: two.
2. Enforcement when an option breaches an objective: `warn`, `acknowledge` or `refuse`, per environment or global?
   Leaning: per environment.
3. Budget window: rolling 30 days or calendar month? Leaning: rolling.
4. "Downtime" from recorded incidents only (with the caveat) or from an external availability signal? Leaning:
   recorded incidents first.
