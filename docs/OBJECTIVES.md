# aiHelpDesk recovery objectives and environments

A recovery you cannot measure against anything is only a report. This is how `infrastructure.json` says what
each database is **supposed** to achieve, so the numbers aiHelpDesk produces (the
[data-loss estimate](PITR.md#4-operating-it) first) can be compared with it.

| Objective | Meaning | Used by |
|---|---|---|
| `rpo` | The most data, as time, that may be lost | Not consumed yet: next is the PITR data-loss comparison |
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
environment, and the environment's from the fleet default. Every resolved value carries its source
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
- The name must be declared under `environments`. A typo (`prdo`) is an error that is logged at startup, and the
  database gets only the objectives it sets itself: not its environment's, which do not exist, and not the fleet
  default as a stand-in.
- A name is lowercase letters, digits, `-` and `_`, at most 32 characters.

An environment does not imply a tag, and a tag does not imply an environment. A database can have both.

## 3. Values

- **Durations** are strings: `"15m"`, `"1h30m"`, `"90s"`, and a days unit, `"1d"` or `"1d12h"`. A bare number is
  invalid: `900` would be seconds to one reader and minutes to another. So is a sign after the days (`"1d-5m"`).
- **`rpo`** may be `"0s"`: no data loss tolerated. It is a value, not an absence, and does not fall through to the
  next level.
- **`rto`** must be greater than zero.
- **`availability_slo`** is a percentage greater than 0 and at most 100.

A config that sets none of these loads exactly as before.

## 4. A mistake never costs the inventory

Most services treat an `infrastructure.json` that fails to load as "no infrastructure config" and carry on: no
target resolution, no policy tags, no approval rules. So a mistake in an objective must not fail the load, and it
does not. The file loads, the inventory is intact, and:

- the gateway and the sysadmin agent log every problem at startup, at error level, one line each, naming the
  setting (`db_servers.orders.objectives: rpo must not be negative`);
- an **invalid value blocks its own objective**: it resolves as unset, with the reason in `problems`, and does
  not fall through to the next level, which would pass a typo off as the fleet default;
- the other objectives of the same database are unaffected.

A caller that finds an objective unset must look at `problems` before treating it as "no objective".

## 5. Reading it in code

`infra.Config.ResolveObjectives(<db_servers key>)` returns the resolved objectives for one database, each with
`Set` and `Source`, and `Problems`. A database that is not configured returns `ok=false`; a configured one with
nothing set returns `Set=false` for each objective and no problems. `Config.Validate()` lists every problem in the
file, and `Config.LogProblems(slog.Error)` logs them.
