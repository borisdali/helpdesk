# aiHelpDesk Fault Injection Testing (external)

`faulttest` is aiHelpDesk's customer-facing CLI for validating how well your agents diagnose and recover from real database and infrastructure failures. It is one of the two cornerstones of the [Operational SRE/DBA Flywheel](VAULT.md) — the feedback loop that makes aiHelpDesk's operational knowledge compound over time.

You bring your own database server, let aiHelpDesk inject a known fault, send a diagnostic prompt to the agent, score the response against expected keywords and tool usage, and optionally trigger a remediation playbook and confirm recovery — all without touching your production systems. When remediation succeeds, a playbook draft is **automatically saved to the [Vault](VAULT.md)** for your review.

This page covers external fault injection against customer-owned databases. For the internal Docker-compose harness and governance integration tests, see [here](../testing/FAULT_INJECTION_TESTING.md). For the wider testing strategy see [here](../testing/README.md).

The tool was designed for two complementary use cases:

- **Internal QA** — engineers run the full catalog against Docker-compose or Kubernetes stacks to prevent regressions in agent behavior before shipping
- **External Customer Validation** — operators run a safe subset of SQL-based faults against a staging or canary database they already own, confirming the agents behave correctly in their specific environment before going to production

---

## Table of Contents

1. [How it works](#1-how-it-works)
2. [Prerequisites](#2-prerequisites)
3. [Modes of operation](#3-modes-of-operation)
   - [External mode (SQL only)](#31-external-mode-sql-only)
   - [SSH injection mode](#32-ssh-injection-mode)
   - [Remediation mode](#33-remediation-mode)
4. [Policy safety: the infra-config guard](#4-policy-safety-the-infra-config-guard)
5. [CLI reference](#5-cli-reference)
   - [list](#51-list)
   - [run](#52-run)
   - [inject / teardown](#53-inject--teardown)
   - [validate](#54-validate)
   - [example](#55-example)
   - [vault](#56-vault) — see also [VAULT.md](VAULT.md) for the full flywheel concept
6. [Fault catalog](#6-fault-catalog)
   - [External-compatible faults](#61-external-compatible-faults)
   - [SSH-injectable faults](#62-ssh-injectable-faults)
   - [Remediation specs](#63-remediation-specs)
7. [Example workflows](#7-example-workflows)
   - [Smoke test a staging database](#71-smoke-test-a-staging-database)
   - [Full run with remediation](#72-full-run-with-remediation)
   - [Interactive single-fault injection](#73-interactive-single-fault-injection)
   - [Running from Docker](#74-running-from-docker)
   - [Running from Kubernetes (Helm)](#75-running-from-kubernetes-helm)
   - [Vault: tracking history, drift, and auto-suggest](#76-vault-tracking-history-and-drift)
8. [Interpreting results](#8-interpreting-results) — including [LLM-as-judge fields](LLM_AS_JUDGE.md)
9. [Customer fault catalogs](#9-customer-fault-catalogs)
   - [Overview](#91-overview)
   - [Writing a catalog file](#92-writing-a-catalog-file)
   - [Validating before running](#93-validating-before-running)
   - [Running with a custom catalog](#94-running-with-a-custom-catalog)
   - [Filtering by source](#95-filtering-by-source)
10. [Extending the built-in catalog](#10-extending-the-built-in-catalog)

---

## 1. How it works

```
faulttest run --external --conn "host=staging-db port=5432 ..."

  ┌─────────────────────────────────┐
  │  For each fault in catalog:     │
  │                                 │
  │  1. Inject         ─────────────┼──► ExternalInject SQL runs against your DB
  │                                 │
  │  2. Prompt agent   ─────────────┼──► POST /api/v1/query  (gateway)
  │                    ◄────────────┼─── agent response text
  │                                 │
  │  3. Evaluate                    │    score keywords, diagnosis category, tool calls
  │                                 │
  │  4. Remediate (opt)─────────────┼──► POST /api/v1/fleet/playbooks/{id}/run
  │                    ◄────────────┼─── poll SELECT 1 until recovery confirmed
  │                                 │
  │  4b. Vault auto-suggest ────────┼──► POST /api/v1/fleet/playbooks/from-trace
  │      (on remediation pass)      │    draft saved to Vault for review
  │                                 │
  │  5. Teardown       ─────────────┼──► cleanup SQL removes injected state
  └─────────────────────────────────┘

  JSON report written to faulttest-<run-id>.json
  Run history appended to ~/.faulttest/history.json
```

Each fault is scored on three weighted dimensions. The weights depend on whether
the [LLM-as-judge](LLM_AS_JUDGE.md) is enabled (via `--judge`):

| Dimension | Default weights | With `--judge` |
|-----------|:--------------:|:--------------:|
| Keyword match | 50% | 20% |
| Diagnosis (category match or judge score) | 30% | 40% |
| Tool evidence | 20% | 40% |

A fault passes when the weighted score reaches 60% or higher. Ordering assertions (e.g., `get_session_info` must precede `terminate_connection`) are also evaluated and gate the pass verdict independently of the score.

When `--judge` is enabled, the diagnosis dimension is scored by a secondary LLM that reads the agent's response against a natural-language `narrative` describing what a correct answer should contain. This replaces the brittle category string-match with semantic evaluation. See [LLM-as-Judge](LLM_AS_JUDGE.md) for full details.

---

## 2. Prerequisites

**Deployment platform**: `faulttest` is a client-side test runner — it connects to your aiHelpDesk deployment over HTTP and to your database over a PostgreSQL connection string. It does not need to run inside your cluster or on the same host as the agents. How you obtain the binary depends on your deployment platform:

| Platform | How to get `faulttest` | Deployment guide |
|----------|------------------------|------------------|
| **Host (binary tarball)** | Included in the platform tarball (`helpdesk-vX.Y.Z-linux-amd64.tar.gz`). Run it directly alongside the other binaries. | [deploy/host/README.md](../deploy/host/README.md) — agents run directly on a Linux or macOS host, no Docker or K8s required |
| **Docker Compose** | Use the same helpdesk Docker image your stack already pulls. Run `docker run ... faulttest` or add a one-off `docker compose run` service. | [deploy/docker-compose/README.md](../deploy/docker-compose/README.md) — agents run in Docker containers on a VM, orchestrated via Compose |
| **Kubernetes / Helm** | Use `kubectl run` with the same image referenced in your Helm values. No separate image pull needed. | [deploy/helm/README.md](../deploy/helm/README.md) — agents deployed as Kubernetes workloads via the included Helm chart |

```bash
# Host tarball — faulttest is already in the extracted directory
./faulttest run --conn "host=staging-db ..." --db-agent http://gateway:8080

# Docker Compose — use the running image
docker run --rm --network helpdesk_default \
  ghcr.io/org/helpdesk:v0.8.0 faulttest --help

# Kubernetes — spin up a short-lived pod
kubectl run faulttest --image=ghcr.io/org/helpdesk:v0.8.0 --rm -it \
  -- faulttest --help
```

The `faulttest` binary is self-contained: the built-in catalog is compiled into it. No source tree or extra files are required at runtime.

**Database agent running**: `faulttest` sends prompts over the A2A protocol to whichever agent you point it at. The gateway is the most convenient entry point for authenticated queries:

```bash
helpdesk-client --gateway http://your-gateway:8080 --api-key sk-...
# Agents reachable via the gateway at POST /api/v1/query
```

Alternatively, point directly at the database agent's A2A port (default 1100).

**Database access**: the tool needs a libpq connection string to inject and tear down SQL faults. The same connection string is embedded in the prompt so the agent uses it for all tool calls.

> **Out-of-the-box behavior**: the standalone binary (downloaded from the release bundle) automatically defaults to external / SQL-only mode. You do not need to pass `--external` — it is implied unless you explicitly set `--external=false`. This means the binary works safely against any PostgreSQL instance with no Docker or cluster access required.

---

## 3. Modes of operation

### 3.1 External mode

`--external` restricts the run to faults marked `external_compat: true` in the catalog. These faults use either `sql` or `shell_exec` injection — no Docker daemon, no Kubernetes control plane, and no internal Docker Compose stack required. Anything reachable from the machine running faulttest qualifies.

**This is the default when running the standalone binary.** The binary detects at startup that the internal Docker test infrastructure is not present and enables external mode automatically, so customers never see a flood of "injection failed" errors from faults that require our internal Docker stack. You can disable this with `--external=false` if you are deliberately running the full suite against the Docker compose test environment.

```bash
# Standalone binary — --external is implied, these are equivalent:
faulttest run \
  --conn "host=staging-db port=5432 dbname=mydb user=myuser password=..." \
  --db-agent http://gateway:8080 \
  --infra-config infrastructure.json

faulttest run --external \
  --conn "host=staging-db port=5432 dbname=mydb user=myuser password=..." \
  --db-agent http://gateway:8080 \
  --infra-config infrastructure.json
```

The `--infra-config` flag is recommended (see [section 4](#4-policy-safety-the-infra-config-guard)).

**What external mode injects:**

| Fault | Injection mechanism |
|-------|---------------------|
| `db-table-bloat` | SQL: creates table, inserts rows, disables autovacuum, deletes half |
| `db-high-cache-miss` | SQL: creates a table larger than `shared_buffers`, forces sequential scan |
| `db-vacuum-needed` | SQL: creates bloat table, disables autovacuum, generates dead tuples |
| `db-disk-pressure` | SQL: inserts 10,000 rows of 2 KB each |
| `db-replication-lag` | SQL: `pg_wal_replay_pause()` on replica |
| `db-max-connections` | SQL: opens near-`max_connections` idle sessions |
| `db-long-running-query` | SQL: `pg_sleep(300)` in a detached session |
| `db-lock-contention` | SQL: acquires `ACCESS EXCLUSIVE` lock and holds it |
| `db-idle-in-transaction` | SQL: opens a transaction, performs a write, holds it open |
| `db-terminate-direct-command` | Same as idle-in-transaction; tests inspect-before-act ordering |

All teardowns remove injected state completely: tables are dropped, held sessions are terminated, paused replay is resumed.

### 3.2 SSH injection mode

For OS-level faults that cannot be expressed in SQL — pg_hba.conf corruption, process kill, configuration file poisoning — `faulttest` can run scripts on a remote host via SSH. The script content is streamed over stdin; no files need to be pre-staged on the target.

```bash
faulttest inject --id db-pg-hba-corrupt \
  --conn "host=staging-db port=5432 dbname=mydb user=myuser" \
  --ssh-host staging-vm \
  --ssh-user ubuntu \
  --ssh-key ~/.ssh/staging.pem
```

The target host in the fault spec (`exec_via`) can be overridden by `--ssh-host`. SSH options used: `-o StrictHostKeyChecking=no -o BatchMode=yes`.

SSH-injectable faults are **not** marked `external_compat` — they require OS access and are excluded from `--external` runs.

### 3.3 Remediation mode

`--remediate` adds a recovery phase after injection and diagnosis. After the agent evaluates the fault, `faulttest` triggers either a fleet playbook or a direct agent prompt, then polls a fault-specific verification SQL query until the database responds correctly or the timeout elapses.

```bash
faulttest run --external --remediate \
  --conn "host=staging-db port=5432 dbname=mydb user=myuser password=..." \
  --db-agent http://gateway:8080 \
  --gateway http://gateway:8080 \
  --api-key sk-... \
  --infra-config infrastructure.json
```

Faults that have a `remediation` block in the catalog will trigger the playbook specified there. Faults without a `remediation` block are evaluated for diagnosis only.

**Remediation scoring:**

Each remediation attempt produces a `remediation_score` (0.0–1.0) based on recovery speed relative to the fault's `verify_timeout` (default 120s):

| Recovery time | Score | Meaning |
|---------------|:-----:|---------|
| ≤ half the timeout | **1.0** | Fast recovery — playbook acted promptly |
| ≤ full timeout | **0.75** | Recovered within the window, but slowly |
| Timed out | **0.0** | Recovery not confirmed |

The `overall_score` combines diagnosis and remediation:

```
overall_score = diagnosis_score × 0.6 + remediation_score × 0.4
```

When no remediation is attempted, `overall_score` equals `score` (the diagnosis-only score). This means a fault that was correctly diagnosed but not remediated is not penalised — remediation is strictly additive.

**Fault-specific verification SQL:**

Each fault in the catalog can define a `verify_sql` query that confirms the specific condition has been resolved, rather than relying on a generic connectivity check:

```yaml
remediation:
  playbook_id: pbs_db_conn_pooling
  verify_sql: >
    SELECT count(*) < current_setting('max_connections')::int - 5
    FROM pg_stat_activity WHERE state = 'idle'
  verify_timeout: "120s"
```

The query must return successfully (exit 0 from psql) for recovery to be confirmed. A query that returns rows is enough — the row content is not checked. If `verify_sql` is absent, `faulttest` falls back to `SELECT 1` (bare connectivity check).

**Remediation method:**

| `remediation_method` | How it was triggered |
|----------------------|----------------------|
| `playbook` | Fleet playbook run via `POST /api/v1/fleet/playbooks/{id}/run` |
| `agent_prompt` | Direct agent call via `POST /api/v1/query` with a configured prompt |
| `none` | No remediation block configured for this fault |

---

## 4. Policy safety: the infra-config guard

Before injecting any fault, `faulttest` optionally checks that the target PostgreSQL host is present in your `infrastructure.json` and is tagged `test` or `chaos`. This prevents accidental injection against production databases. The check applies to all three injection subcommands: `run`, `inject`, and `teardown`.

```json
{
  "db_servers": {
    "staging-db": {
      "connection_string": "host=staging-db port=5432 dbname=mydb user=myuser",
      "tags": ["staging", "test"]
    }
  }
}
```

```bash
faulttest run --external \
  --conn "host=staging-db port=5432 ..." \
  --infra-config infrastructure.json
# ✓ staging-db has "test" tag — injection allowed

faulttest run --external \
  --conn "host=prod-db port=5432 ..." \
  --infra-config infrastructure.json
# ✗ prod-db has no "test" or "chaos" tag — injection refused
```

### Storing the password outside the config file

Plain-text passwords in `infrastructure.json` are not acceptable in most environments. Use `password_env` to store the password in an environment variable instead:

```json
{
  "db_servers": {
    "staging-db": {
      "connection_string": "host=staging-db port=5432 dbname=mydb user=myuser",
      "password_env": "STAGING_DB_PASSWORD",
      "tags": ["staging", "test"]
    }
  }
}
```

At runtime the gateway (and any other component reading `infrastructure.json`) appends `password=<value>` to the connection string from the named environment variable. The file itself never contains the password.

```bash
# Pass the password via environment variable — the gateway resolves it at call time
export STAGING_DB_PASSWORD="$(vault read -field=password secret/staging-db)"

# Use the alias in --agent-conn so the gateway finds the registered entry;
# use --conn with the full DSN for injection (faulttest resolves its own connection)
faulttest run --external \
  --conn "host=staging-db port=5432 dbname=mydb user=myuser password=$STAGING_DB_PASSWORD" \
  --agent-conn staging-db \
  --infra-config infrastructure.json \
  --db-agent http://helpdesk-gateway:8080
```

If `--infra-config` is omitted the check is skipped. This is intentional for air-gapped or single-tenant setups where the operator knows their target. The flag is strongly recommended in any shared environment.

---

## 5. CLI reference

### 5.1 list

```
faulttest list [options]
```

Lists all faults in the catalog. Add `--external` to show only externally injectable faults; add `--categories database` to filter by category.

```bash
# All faults
faulttest list

# External-compatible only
faulttest list --external

# One category
faulttest list --categories database
```

Output columns: `ID`, `CATEGORY`, `SEVERITY`, `EXTERNAL` (yes/blank), `SOURCE` (builtin/custom), `NAME`.

Add `--source builtin` or `--source custom` to restrict output to faults from one catalog only.

### 5.2 run

```
faulttest run [options]
```

Injects each fault in sequence, prompts the agent, evaluates the response, optionally remediates, tears down, and writes a JSON report.

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--conn` | — | — | PostgreSQL connection string used for fault injection and teardown |
| `--agent-conn` | — | `--conn` | Connection identifier sent to the agent in prompts. Defaults to `--conn`. Use this when the agent should see a registered alias from `infrastructure.json` (e.g. `staging-db`) while `--conn` holds the full DSN with password for injection. |
| `--replica-conn` | — | — | Replica connection string (replication-lag fault) |
| `--db-agent` | — | — | Database agent A2A URL or gateway URL |
| `--k8s-agent` | — | — | Kubernetes agent A2A URL |
| `--sysadmin-agent` | — | — | SysAdmin agent A2A URL |
| `--orchestrator` | — | — | Orchestrator A2A URL (compound faults) |
| `--context` | — | — | Kubernetes context |
| `--categories` | — | all | Comma-separated categories: `database,kubernetes,host,compound` |
| `--ids` | — | all | Comma-separated fault IDs to run |
| `--external` | — | true¹ | Only external_compat faults; SQL injection only |
| `--ssh-user` | `USER` | current user | SSH username for ssh_exec faults |
| `--ssh-key` | — | — | SSH private key path |
| `--remediate` | — | false | Run remediation phase after diagnosis |
| `--gateway` | — | — | Gateway URL for playbook/agent remediation and vault playbook checks. No default — must be set explicitly when `--remediate` or `vault list` needs live validation. |
| `--api-key` | `HELPDESK_CLIENT_API_KEY` | — | Bearer token for gateway auth |
| `--purpose` | — | `diagnostic` | Purpose declared in gateway requests (e.g. `diagnostic`, `remediation`, `maintenance`). Required when your gateway policy enforces declared purposes. |
| `--judge` | — | `false` | Enable LLM-as-judge for semantic diagnosis scoring. See [LLM-as-Judge](LLM_AS_JUDGE.md). |
| `--judge-model` | `HELPDESK_MODEL_NAME` | — | Model name for the judge LLM |
| `--judge-vendor` | `HELPDESK_MODEL_VENDOR` | — | Model vendor for the judge LLM |
| `--judge-api-key` | `HELPDESK_API_KEY` | — | API key for the judge (defaults to the agent key) |
| `--audit-url` | — | — | auditd URL for audit-trail-based tool evidence (`ToolEvidenceMode: audit`) |
| `--infra-config` | — | — | Path to `infrastructure.json` for safety check |
| `--testing-dir` | — | auto-detected | Path to the `testing/` directory |
| `--catalog` | — | — | Additional customer catalog file (repeatable) |
| `--source` | — | all | Filter by source: `builtin` or `custom` |
| `--report-dir` | — | `.` | Directory to write the JSON report (useful when running in a container with a mounted volume) |

¹ Default is `true` when running the standalone binary (no source tree detected). Default is `false` when running from the source tree (e.g. `go run ./testing/cmd/faulttest`). Override explicitly with `--external=false`.

### 5.3 inject / teardown

Interactive mode: inject or remove a single named fault without running an agent.

```bash
# Inject a fault (leaves it active)
faulttest inject --id db-table-bloat \
  --conn "host=staging-db port=5432 ..."

# After manual investigation, tear it down
faulttest teardown --id db-table-bloat \
  --conn "host=staging-db port=5432 ..."
```

Both commands print the suggested prompt for the agent after injection, so you can manually paste it into `helpdesk-client` for an interactive session.

### 5.4 validate

```
faulttest validate --catalog <file> [--catalog <file> ...] [--testing-dir .]
```

Validates one or more customer catalog files before running them. Errors (exit 1) block any run; warnings are informational only.

| Severity | Condition |
|----------|-----------|
| **Error** | Missing `id`, `name`, `category`, or `inject.type` |
| **Error** | `inject.type` is not a known type |
| **Error** | Duplicate ID — conflicts with built-in catalog or another custom file |
| **Error** | `script:` file referenced and `--testing-dir` set but file not found |
| **Warning** | `category` is not one of `database`, `kubernetes`, `host`, `compound` |
| **Warning** | `script:` file referenced but no `--testing-dir` — cannot verify existence |
| **Warning** | No `expected_keywords` — scoring will be unreliable |

```bash
faulttest validate --catalog my-faults.yaml

# Validating my-faults.yaml (3 entries):
#   [OK]   my-slow-query
#   [WARN] my-custom-fault: no expected_keywords; scoring will be unreliable
#   [ERR]  my-bad-fault: inject.type "unknown" is not a known type
#
# 1 error(s), 1 warning(s).
```

### 5.5 example

```
faulttest example [--category database|kubernetes|host|compound]
```

Prints an annotated YAML template to stdout covering every field with inline comments. Pipe it to a file and edit. Default category: `database`.

```bash
faulttest example > my-faults.yaml
faulttest example --category kubernetes > k8s-faults.yaml
```

### 5.6 vault

```
faulttest vault <list|status|drift|suggest|suggest-update>
```

The vault is aiHelpDesk's library of fault→remedy pairings and the engine of the [Operational SRE/DBA Flywheel](VAULT.md). Run history is stored in `~/.faulttest/history.json` and is updated automatically at the end of every `faulttest run`. For the full vault concept and three customer workflows, see [VAULT.md](VAULT.md).

#### vault list

```bash
faulttest vault list [--gateway http://gateway:8080] [--api-key sk-...]
```

Shows the full fault catalog alongside the linked playbook (if any), the date of the last run, and the pass/fail status. When `--gateway` is provided, `faulttest` also verifies that referenced playbook IDs exist on the gateway and shows `PLAYBOOK NOT FOUND` for any that are missing or not yet registered.

```
FAULT                            PLAYBOOK                     LAST RUN     STATUS
--------------------------------------------------------------------------------------------
db-max-connections               (none)                       2026-04-16   NO PLAYBOOK
db-connection-refused            pbs_db_restart_triage        2026-04-15   PASS
db-pg-hba-corrupt                pbs_db_config_recovery       (never)      -
db-lock-contention               (none)                       2026-04-14   FAIL
```

Status values:

| Status | Meaning |
|--------|---------|
| `PASS` / `FAIL` | Last run result |
| `-` | Fault has a playbook linked but has never been run |
| `NO PLAYBOOK` | No `remediation.playbook_id` configured in the catalog |
| `PLAYBOOK NOT FOUND` | Playbook ID configured but not found on the gateway (`--gateway` required) |

#### vault status

```bash
faulttest vault status [--since-days 30]
```

Shows overall pass rates across all runs in the history window, plus a per-fault breakdown:

```
=== Vault Status (last 30 days, 4 runs) ===

DATE         RUN ID               PASS RATE
--------------------------------------------------
2026-04-10   run-a1b2c3           80% (8/10)
2026-04-12   run-d4e5f6           90% (9/10)
2026-04-14   run-g7h8i9           80% (8/10)
2026-04-16   run-j0k1l2           90% (9/10)

=== Per-Fault Pass Rates ===

FAULT                            PASS RATE  RUNS
-------------------------------------------------------
db-lock-contention               75%        4
db-max-connections               100%       4
db-table-bloat                   100%       4
```

#### vault drift

```bash
faulttest vault drift [--since-days 90]
```

Compares pass rates between the first and second halves of the history window and flags faults whose pass rate dropped by more than 20 percentage points. Useful for catching quiet regressions:

```
=== Vault Drift Analysis (last 90 days) ===

FAULT                            FIRST HALF   SECOND HALF  DRIFT
------------------------------------------------------------------------
db-lock-contention               100%         50%          -50%
db-replication-lag               75%          33%          -42%
```

#### vault suggest

```bash
faulttest vault suggest \
  --trace-id tr_abc123 \
  --outcome resolved \
  --gateway http://gateway:8080 \
  --api-key sk-...
```

Manually synthesises a playbook draft from any audit trace ID and prints it to stdout. When the gateway's auditd is configured, the draft is also **automatically saved** to the Vault as an inactive draft (`source=generated`, `is_active=false`) and the `playbook_id` of the saved draft is printed. Activate it with `POST /api/v1/fleet/playbooks/{id}/activate` when ready.

Note: when `faulttest run --remediate` passes, vault auto-suggest runs automatically — you only need to call this manually for traces from real incidents outside of faulttest runs.

#### vault suggest-update

```bash
faulttest vault suggest-update \
  --series-id pbs_db_restart_triage \
  --trace-id tr_xyz789 \
  --outcome resolved \
  --gateway http://gateway:8080 \
  --api-key sk-...
```

Fetches the current active playbook for `--series-id`, synthesises a proposed update from the given trace, and displays the two side by side so you can compare and decide whether to activate the proposal. Useful when `vault drift` shows a declining pass rate and you want to incorporate a more recent successful approach into the existing playbook.

---

## 6. Fault catalog

The catalog lives at `testing/catalog/failures.yaml`. It is version-controlled alongside the codebase and versioned with the `version: "1"` field.

### 6.1 External-compatible faults

These faults work against any PostgreSQL instance accessible over libpq. No Docker, no Kubernetes, no OS shell access required.

| ID | Name | Severity |
|----|------|----------|
| `db-table-bloat` | Table bloat / dead tuples | medium |
| `db-high-cache-miss` | High cache miss ratio | medium |
| `db-vacuum-needed` | Tables needing vacuum | medium |
| `db-disk-pressure` | Disk usage — large table growth | medium |
| `db-replication-lag` | Replication lag | high |
| `db-max-connections` | Max connections exhausted | high |
| `db-long-running-query` | Long-running query blocking | high |
| `db-lock-contention` | Lock contention / deadlock | high |
| `db-idle-in-transaction` | Session stuck with uncommitted writes | high |
| `db-terminate-direct-command` | Direct terminate — inspect-first check | high |

### 6.2 SSH-injectable faults

These faults require OS-level access to the database host and are injected via SSH. Not included in `--external` runs.

| ID | Name | Severity | What it does |
|----|------|----------|--------------|
| `db-pg-hba-corrupt` | pg_hba.conf corrupted | critical | Replaces pg_hba.conf to reject all non-local connections; reloads config |
| `db-process-kill` | PostgreSQL postmaster killed | critical | Sends SIGKILL to the postmaster PID |
| `db-config-bad-param` | postgresql.conf invalid parameter | high | Appends `shared_buffers = 999GB` to postgresql.conf |

### 6.3 Remediation specs

Some faults carry a `remediation` block that identifies the recovery action. When `--remediate` is set, `faulttest` triggers this action after the diagnosis phase.

| Fault | Playbook |
|-------|----------|
| `db-connection-refused` | `pbs_db_restart_triage` |
| `db-pg-hba-corrupt` | `pbs_db_config_recovery` |
| `db-process-kill` | `pbs_db_restart_triage` |

The playbook IDs must exist in your aiHelpDesk deployment. See [Playbooks](PLAYBOOKS.md) for how to create and activate them. If a playbook ID is not found the remediation phase records an error in the report but does not fail the overall run.

Each fault's `remediation` block specifies a `verify_sql` query that confirms the specific condition has resolved. Generic `SELECT 1` (the default) only checks connectivity; fault-specific queries confirm the actual state was corrected:

| Fault | verify_sql |
|-------|-----------|
| `db-max-connections` | `SELECT count(*) < current_setting('max_connections')::int - 5 FROM pg_stat_activity WHERE state = 'idle'` |
| `db-idle-in-transaction` | `SELECT count(*) = 0 FROM pg_stat_activity WHERE state = 'idle in transaction'` |
| `db-lock-contention` | `SELECT count(*) = 0 FROM pg_locks WHERE NOT granted` |
| `db-connection-refused` | `SELECT 1` (connectivity check is sufficient — the fault kills the postmaster) |

When writing customer catalog entries, prefer specific queries that directly verify the fault condition rather than bare connectivity checks.

---

## 7. Example workflows

### 7.1 Smoke test a staging database

Run the full external-compatible suite against a staging database to confirm the database agent gives correct diagnoses. Takes roughly 10–20 minutes (one fault at a time, LLM calls included).

```bash
# --external is the default for the standalone binary; omit or include, same result
faulttest run \
  --conn "host=staging-db.internal port=5432 dbname=myapp user=dbuser password=$(cat .pgpass)" \
  --db-agent http://helpdesk-gateway:8080 \
  --api-key $HELPDESK_API_KEY \
  --infra-config infrastructure.json
```

The report is written to `faulttest-<run-id>.json`.

### 7.2 Full run with remediation

Same as above, but also trigger playbook-based recovery for faults that have a `remediation` spec and verify the database comes back:

```bash
faulttest run --remediate \
  --conn "host=staging-db.internal port=5432 dbname=myapp user=dbuser" \
  --db-agent http://helpdesk-gateway:8080 \
  --gateway http://helpdesk-gateway:8080 \
  --api-key $HELPDESK_API_KEY \
  --infra-config infrastructure.json
```

Sample output:

```
--- Testing: Max connections exhausted (db-max-connections) ---
Remediation: RECOVERED in 12.3s (score: 100%)
Result: [PASS] score=87% | Diagnosis: 92% | Remediation: 100% | Overall: 95%

--- Testing: Long-running query blocking (db-long-running-query) ---
Result: [PASS] score=74%

=== SUMMARY ===
Passed: 9/10  Failed: 1  Skipped: 0
Report: faulttest-a3f2b1c4.json
```

The `overall_score` in the report combines `diagnosis_score × 0.6 + remediation_score × 0.4`. Faults without a remediation spec show only the diagnosis score.

### 7.3 Interactive single-fault injection

Inject one fault by hand, investigate with `helpdesk-client`, then tear down:

```bash
# Step 1: inject
faulttest inject --id db-idle-in-transaction \
  --conn "host=staging-db port=5432 dbname=myapp user=dbuser"

# Output:
#   Failure injected: Session stuck with uncommitted writes
#   Suggested prompt:
#     A backend session appears to be stuck in a long-running transaction ...
#   To tear down: faulttest teardown --id db-idle-in-transaction [same flags]

# Step 2: run the agent interactively
helpdesk-client \
  --gateway http://helpdesk-gateway:8080 \
  --api-key $HELPDESK_API_KEY \
  --purpose diagnostic \
  --message "A backend session appears to be stuck..."

# Step 3: tear down
faulttest teardown --id db-idle-in-transaction \
  --conn "host=staging-db port=5432 dbname=myapp user=dbuser"
```

### 7.4 Running from Docker

`faulttest` is included in the standard helpdesk Docker image. Docker Compose users can run it without downloading a separate binary by using `docker run` or `docker compose run` against the same image their deployment uses.

```bash
# One-off run against a staging database (gateway on the same Docker network).
# -v $(pwd):/output -w /output writes the JSON report to the host's current directory.
# Add -e for each password_env variable referenced in infrastructure.json.
docker run --rm \
  --network helpdesk_default \
  -v "$(pwd)/infrastructure.json:/infrastructure.json:ro" \
  -v "$(pwd):/output" -w /output \
  -e DEV_DB_PASSWORD \
  ghcr.io/org/helpdesk:v0.8.0 \
  faulttest run \
    --conn "host=localhost port=5432 dbname=myapp user=dbuser" \
    --db-agent http://gateway:8080 \
    --api-key $HELPDESK_API_KEY \
    --infra-config /infrastructure.json
```

If the gateway is reachable on the host network:

```bash
docker run --rm --network host \
  -v "$(pwd)/infrastructure.json:/infrastructure.json:ro" \
  -v "$(pwd):/output" -w /output \
  -e DEV_DB_PASSWORD \
  ghcr.io/org/helpdesk:v0.8.0 \
  faulttest run \
    --conn "host=localhost port=5432 dbname=myapp user=dbuser" \
    --db-agent http://localhost:8080 \
    --api-key $HELPDESK_API_KEY \
    --infra-config /infrastructure.json
```

### 7.5 Running from Kubernetes (Helm)

For Helm deployments the recommended approach is a one-off Job rather than `kubectl run`, because a Job can mount the ConfigMap and Secrets that the chart already created — giving `faulttest` access to `infrastructure.json` and any `password_env` variables without duplicating credentials:

```bash
kubectl -n helpdesk-system apply -f - <<'EOF'
apiVersion: batch/v1
kind: Job
metadata:
  name: faulttest
  namespace: helpdesk-system
spec:
  ttlSecondsAfterFinished: 300
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: faulttest
        image: ghcr.io/org/helpdesk:v0.8.0
        args:
          - faulttest
          - run
          - --conn=host=localhost port=5432 dbname=myapp user=dbuser
          - --db-agent=http://helpdesk-gateway:8080
          - --api-key=$(HELPDESK_CLIENT_API_KEY)
          - --infra-config=/etc/helpdesk/infrastructure.json
        env:
        - name: HELPDESK_CLIENT_API_KEY
          valueFrom:
            secretKeyRef:
              name: helpdesk-api-key       # same Secret the chart already creates
              key: api-key
        - name: DEV_DB_PASSWORD            # add one entry per password_env var
          valueFrom:
            secretKeyRef:
              name: helpdesk-db-passwords
              key: dev-db-password
        volumeMounts:
        - name: infra-config
          mountPath: /etc/helpdesk/infrastructure.json
          subPath: infrastructure.json
          readOnly: true
      volumes:
      - name: infra-config
        configMap:
          name: helpdesk-infra-config      # same ConfigMap the chart already creates
EOF

kubectl -n helpdesk-system logs -f job/faulttest
```

For a quick one-liner when you only need to list faults or have no `infrastructure.json`:

```bash
kubectl -n helpdesk-system run faulttest --rm -it --restart=Never \
  --image=ghcr.io/org/helpdesk:v0.8.0 \
  -- faulttest list
```

### 7.6 Vault: tracking history and drift

After running `faulttest run` a few times, use the vault to review trends and pairing status. For the full vault concept, lifecycle, and three customer workflows, see [VAULT.md](VAULT.md).

**Check what's covered and what's missing:**

```bash
# No --gateway: shows last-run status without live playbook validation
faulttest vault list

# With --gateway: also verifies playbook IDs exist on the deployment
faulttest vault list \
  --gateway http://helpdesk-gateway:8080 \
  --api-key $HELPDESK_API_KEY
```

**Review pass rates across recent runs:**

```bash
# Last 30 days (default)
faulttest vault status

# Last 90 days, filtered to a specific database target
faulttest vault status --since-days 90 --target staging-db
```

**Find regressions before they become incidents:**

```bash
# Flag faults whose pass rate dropped >20 percentage points
faulttest vault drift --since-days 90
```

If drift is detected, use `faulttest inject` + `faulttest teardown` to reproduce the fault interactively and investigate why the agent's diagnosis changed. Then use `suggest-update` to incorporate the latest successful approach:

```bash
faulttest vault suggest-update \
  --series-id pbs_db_conn_pooling \
  --trace-id tr_latest \
  --gateway http://helpdesk-gateway:8080 \
  --api-key $HELPDESK_API_KEY
```

**Vault auto-suggest after remediation:**

When `faulttest run --remediate` succeeds, a playbook draft is automatically saved to the Vault. You will see this in the terminal output:

```
Remediation: RECOVERED in 4.2s (score: 100%)
Vault: draft saved → pb_faulttest_a1b2 (activate with 'faulttest vault list')
```

Review and activate the draft when ready:

```bash
# Activate
curl -X POST http://helpdesk-gateway:8080/api/v1/fleet/playbooks/pb_faulttest_a1b2/activate \
  -H "Authorization: Bearer $HELPDESK_API_KEY"
```

**Manual suggest from any real incident trace:**

```bash
faulttest vault suggest \
  --trace-id tr_abc123 \
  --outcome resolved \
  --gateway http://helpdesk-gateway:8080 \
  --api-key $HELPDESK_API_KEY
# → draft printed and auto-saved when auditd is configured
```

---

## 8. Interpreting results

The JSON report contains one entry per fault:

```json
{
  "failure_id": "db-max-connections",
  "failure_name": "Max connections exhausted",
  "category": "database",
  "score": 0.87,
  "passed": true,
  "keyword_pass": true,
  "diagnosis_pass": true,
  "diagnosis_score": 1.0,
  "tool_evidence": true,
  "tool_evidence_mode": "audit",
  "ordering_pass": true,
  "response_text": "...",
  "duration": "18.4s",
  "judge_reasoning": "Agent correctly identified max_connections exhaustion and recommended PgBouncer.",
  "judge_model": "claude-haiku-4-5-20251001",
  "judge_skipped": false,
  "remediation_attempted": true,
  "remediation_passed": true,
  "remediation_score": 1.0,
  "remediation_method": "playbook",
  "recovery_time_seconds": 12.3,
  "overall_score": 0.92
}
```

**Score breakdown:**

| Field | Meaning |
|-------|---------|
| `keyword_pass` | At least one expected keyword found in agent response |
| `diagnosis_pass` | `diagnosis_score >= 0.5` |
| `diagnosis_score` | 0.0–1.0. With `--judge`: maps from the judge's 0–3 score. Without `--judge`: fraction of diagnosis-category words matched in the response. |
| `tool_evidence` | At least 50% of expected tools were confirmed called |
| `tool_evidence_mode` | How tool evidence was determined — three-tier fallback (see below). Omitted when no tools were expected. |
| `ordering_pass` | Tool ordering constraints satisfied (e.g., inspect before terminate) |
| `score` | Weighted combination — see the weights table in §1 |
| `passed` | `score >= 0.6` **and** `ordering_pass = true` |
| `judge_reasoning` | One-sentence explanation from the judge LLM (omitted when skipped) |
| `judge_model` | Model that produced the judge score (omitted when skipped) |
| `judge_skipped` | `true` when judge was disabled, narrative was absent, or the judge call failed |
| `remediation_score` | 0.0–1.0: `1.0` if recovered within half the verify timeout, `0.75` within the full timeout, `0.0` if timed out. Only present when `--remediate` was set. |
| `remediation_method` | `playbook` or `agent_prompt` (only when `--remediate` was set) |
| `overall_score` | `diagnosis_score × 0.6 + remediation_score × 0.4` when remediation was attempted; equals `score` otherwise |

**Tool evidence: three-tier fallback**

`faulttest` determines whether the agent called the expected tools using the best available source, in priority order:

| Priority | Mode | How | When available |
|----------|------|-----|----------------|
| 1 | `audit` | Exact tool names from auditd's `tool_execution` events | `--audit-url` is set and auditd is reachable |
| 2 | `structured` | Exact tool names from the `tool_call_summary` DataPart emitted by ADK agents | Agents built with `agentutil.ServeA2A` (direct A2A, not via gateway) |
| 3 | `text_fallback` | Keyword pattern matching against the agent's response text | All other cases |

`audit` mode is the most accurate: it queries auditd directly for `tool_execution` events in the time window of the agent call, giving exact tool names regardless of which agent or transport was used. `text_fallback` is least reliable — a tool name appearing in the response text does not prove the tool was actually called. The mode used is recorded in `tool_evidence_mode` so you can assess reliability.

To enable audit mode, point `--audit-url` at your auditd instance:

```bash
faulttest run --external \
  --conn "host=staging-db ..." \
  --db-agent http://gateway:8080 \
  --audit-url http://auditd:1199 \
  --api-key $HELPDESK_API_KEY
```

**Common failure patterns:**

| Symptom | Likely cause |
|---------|-------------|
| `keyword_pass=false` | Agent did not reach the right conclusion; check the `response_text` |
| `diagnosis_pass=false` | Agent diagnosed a different root cause category |
| `tool_evidence=false` | Agent responded without calling the expected tools (fabricated answer) |
| `ordering_pass=false` | Agent terminated a session without first inspecting it |
| `error` field set | Injection, teardown, or agent call failed — fault did not run |

**Governance gap tests:** a small number of faults are marked `governance_gap: true`. These document known agent behaviour gaps (e.g., an imperative "terminate it immediately" prompt that causes the agent to skip the inspect step). A failed evaluation on a governance-gap test is expected and does not count as a failure in the summary — it is logged separately so you can track whether the gap narrows over time.

---

## 9. Customer fault catalogs

### 9.1 Overview

Every `faulttest` binary ships with the built-in catalog embedded at compile time — you can run `faulttest list` in a directory with no source tree present and see all 27 built-in faults. Customer catalog files layer on top of this without modifying the binary.

A customer catalog is a plain YAML file you author, validate with `faulttest validate`, and pass to any subcommand via `--catalog`. The flag is repeatable; multiple files are merged in order. IDs must be globally unique — any collision with a built-in fault or another custom file is an error.

The workflow is:

```
faulttest example > my-faults.yaml   # start from an annotated template
# edit my-faults.yaml
faulttest validate --catalog my-faults.yaml  # check for errors/warnings
faulttest list    --catalog my-faults.yaml   # preview merged catalog
faulttest run     --catalog my-faults.yaml --external --conn "host=..."
```

### 9.2 Writing a catalog file

Generate a fully annotated template for any category:

```bash
faulttest example                       # database template (default)
faulttest example --category kubernetes
faulttest example --category host
faulttest example --category compound
```

Every field supported in the built-in catalog is supported in customer catalogs. The `version` field is optional in customer files (it is required in the built-in catalog). The `source` field is set automatically by `faulttest` — never write it in YAML.

**Minimal example (SQL fault):**

```yaml
failures:
  - id: my-slow-query-storm          # must not clash with built-in IDs
    name: "Custom: Slow query storm"
    category: database
    severity: high
    description: >
      Simulates a storm of long-running queries. The agent should detect
      blocked sessions and recommend termination.
    inject:
      type: sql
      script_inline: |
        SELECT pg_sleep(300);        -- run in a background session
    teardown:
      type: sql
      script_inline: |
        SELECT pg_terminate_backend(pid)
        FROM pg_stat_activity
        WHERE state = 'active' AND query LIKE '%pg_sleep%'
          AND pid <> pg_backend_pid();
    prompt: |
      There seems to be a performance problem on {{connection_string}}.
      Can you investigate?
    timeout: "120s"
    evaluation:
      expected_tools:
        - list_long_running_queries
        - terminate_connection
      expected_keywords:
        any_of:
          - "long-running"
          - "pg_sleep"
          - "terminate"
      expected_diagnosis:
        category: performance
```

**Known inject types** (same as built-in catalog):

| Type | Description |
|------|-------------|
| `sql` | SQL via psql; `script_inline` or `script` (file path relative to `--testing-dir`) |
| `docker` | `docker compose stop/start/kill` on a named service |
| `docker_exec` | Run a script inside a container via `docker exec` |
| `shell_exec` | Run a bash script on the local host |
| `ssh_exec` | Run a bash script on a remote host via SSH |
| `kustomize` | Apply a kustomize overlay (`kubectl apply -k`) |
| `kustomize_delete` | Delete a kustomize overlay and optionally re-apply a base |
| `config` | Override a connection string in the harness config |

### 9.3 Validating before running

```bash
faulttest validate --catalog my-faults.yaml [--catalog second.yaml]
```

The validate subcommand checks every entry and prints a per-fault verdict. Exit code is 1 if any errors are found; warnings do not affect the exit code.

```
Validating my-faults.yaml (2 entries):
  [OK]   my-slow-query-storm
  [WARN] my-other-fault: no expected_keywords; scoring will be unreliable

0 error(s), 1 warning(s).
```

To also verify that `script:` file references exist on disk, pass `--testing-dir`:

```bash
faulttest validate --catalog my-faults.yaml --testing-dir /path/to/testing
```

### 9.4 Running with a custom catalog

Pass `--catalog` to any subcommand. The built-in faults are always included unless you filter them out with `--source`:

```bash
# Run all built-in + custom faults
faulttest run --catalog my-faults.yaml --external \
  --conn "host=staging-db port=5432 ..."

# Run only your custom faults
faulttest run --catalog my-faults.yaml --source custom --external \
  --conn "host=staging-db port=5432 ..."

# Inject a single custom fault interactively
faulttest inject --catalog my-faults.yaml --id my-slow-query-storm \
  --conn "host=staging-db port=5432 ..."

# List everything with source column
faulttest list --catalog my-faults.yaml
```

Multiple `--catalog` flags are merged in order. IDs must be unique across all files:

```bash
faulttest run \
  --catalog db-custom.yaml \
  --catalog k8s-custom.yaml \
  --conn "host=staging-db port=5432 ..."
```

### 9.5 Filtering by source

`--source` restricts which faults are acted on:

| Value | Meaning |
|-------|---------|
| _(omitted)_ | All faults — built-in and custom |
| `builtin` | Only the faults shipped with `faulttest` |
| `custom` | Only the faults from your `--catalog` files |

`--source` combines with all other filters (`--categories`, `--ids`, `--external`):

```bash
# Only my custom database faults
faulttest list --catalog my-faults.yaml --source custom --categories database

# Validate the built-in catalog passes all checks (should always be true)
faulttest list --source builtin
```

---

## 10. Extending the built-in catalog

> This section is for contributors adding faults to the catalog shipped with `faulttest`. To add faults for your own environment without modifying source, see [section 9](#9-customer-fault-catalogs) above.

The built-in catalog lives at `testing/catalog/failures.yaml` and is compiled into the `faulttest` binary via `//go:embed`. Each fault follows this schema:

```yaml
- id: db-my-new-fault           # unique, lowercase, hyphenated
  name: "Descriptive name"
  category: database             # database | kubernetes | host | compound
  severity: high                 # low | medium | high | critical
  description: >
    One-paragraph description of what the fault simulates.

  # Standard injection (Docker exec, SQL, kustomize, etc.)
  inject:
    type: sql
    script_inline: |
      CREATE TABLE IF NOT EXISTS my_fault_table (id int);
  teardown:
    type: sql
    script_inline: "DROP TABLE IF EXISTS my_fault_table;"

  # Mark as externally injectable (no Docker/OS infrastructure needed).
  external_compat: true

  # Optional: override inject/teardown for --external mode.
  # Use type: sql for stateless DDL/DML (CREATE TABLE, INSERT, etc.).
  # Use type: shell_exec for anything that must hold state across calls
  # (open transactions, held locks) — run psql in the background with &.
  # The env var $FAULTTEST_CONN holds the resolved connection string from --conn.
  external_inject:
    type: sql
    script_inline: "CREATE TABLE IF NOT EXISTS my_fault_table (id int);"
  external_teardown:
    type: sql
    script_inline: "DROP TABLE IF EXISTS my_fault_table;"

  # Example: holding an open transaction requires shell_exec + background psql:
  # external_inject:
  #   type: shell_exec
  #   script_inline: |
  #     psql "$FAULTTEST_CONN" -c "CREATE TABLE IF NOT EXISTS t (id int);"
  #     { { printf "BEGIN;\nLOCK TABLE t IN ACCESS EXCLUSIVE MODE;\n"; sleep 600; } | psql "$FAULTTEST_CONN"; } >/dev/null 2>&1 &
  #     echo $! > /tmp/faulttest_myfault_pid.txt
  #     sleep 1
  # external_teardown:
  #   type: shell_exec
  #   script_inline: |
  #     kill "$(cat /tmp/faulttest_myfault_pid.txt)" 2>/dev/null || true
  #     rm -f /tmp/faulttest_myfault_pid.txt
  #     psql "$FAULTTEST_CONN" -c "DROP TABLE IF EXISTS t;"

  # Optional: trigger playbook remediation when --remediate is set.
  remediation:
    playbook_id: pbs_my_playbook
    verify_sql: "SELECT 1"
    verify_timeout: "120s"

  prompt: >
    Agent-facing prompt describing the symptom. Use
    `{{connection_string}}` as the placeholder — faulttest
    substitutes the actual value at runtime.

  evaluation:
    expected_tools:
      - check_connection
      - get_active_connections
    expected_keywords:
      any_of:
        - "my keyword"
        - "synonym"
    expected_diagnosis:
      category: "my_diagnosis_category"    # used when --judge is not set
      narrative: >                          # used by the LLM judge when --judge is set
        The agent should identify <root cause> and recommend <remediation>.
        It should explain <key detail> and mention <expected outcome>.
    # Optional: assert tool A is mentioned before tool B.
    expected_tool_order:
      - [get_session_info, terminate_connection]

  timeout: 60s
  governance_gap: false          # true = known gap; failure is logged, not asserted
```

After adding a fault, the test count floor checks in both test files will still pass (they assert `>= 27`, not exactly 27), so no test edits are required for additions. Run `go test ./testing/faultlib/... ./testing/cmd/faulttest/...` to confirm.
