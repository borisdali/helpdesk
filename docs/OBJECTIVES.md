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
| Extra acknowledgement at the gate when the loss exceeds the `rpo`, re-measured when the instance is stopped | Not built |
| `recovery_preference` (`availability_first` or `data_first`) per environment and database | Not built |
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
