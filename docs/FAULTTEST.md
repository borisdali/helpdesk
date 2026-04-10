# aiHelpDesk Fault Injection Testing

`faulttest` is a CLI tool that validates how well your aiHelpDesk agents diagnose and recover from real database and infrastructure failures. You inject a known fault, send a diagnostic prompt to the agent, score the response against expected keywords and tool usage, and optionally trigger a remediation playbook and confirm recovery — all without touching your production systems.

The tool was designed for two complementary use cases:

- **Internal QA** — engineers run the full catalog against Docker-compose or Kubernetes stacks to prevent regressions in agent behavior before shipping
- **Customer validation** — operators run a safe subset of SQL-based faults against a staging or canary database they already own, confirming the agents behave correctly in their specific environment before going to production

This page covers the customer-facing, external-mode use case. For the internal Docker-compose harness and governance integration tests, see [here](../testing/FAULT_INJECTION_TESTING.md) and for the wider aiHelpDesk testing strategy see [here](../testing/README.md).

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
6. [Fault catalog](#6-fault-catalog)
   - [External-compatible faults](#61-external-compatible-faults)
   - [SSH-injectable faults](#62-ssh-injectable-faults)
   - [Remediation specs](#63-remediation-specs)
7. [Example workflows](#7-example-workflows)
   - [Smoke test a staging database](#71-smoke-test-a-staging-database)
   - [Full run with remediation](#72-full-run-with-remediation)
   - [Interactive single-fault injection](#73-interactive-single-fault-injection)
8. [Interpreting results](#8-interpreting-results)
9. [Extending the catalog](#9-extending-the-catalog)

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
  │  5. Teardown       ─────────────┼──► cleanup SQL removes injected state
  └─────────────────────────────────┘

  JSON report written to faulttest-<run-id>.json
```

Each fault is scored on three weighted dimensions:

| Dimension | Weight | What it checks |
|-----------|--------|----------------|
| Keywords | 50% | Expected terms appear in the agent's response |
| Diagnosis category | 30% | The agent identifies the correct root-cause class |
| Tool evidence | 20% | The agent's response mentions the right diagnostic tools |

A fault passes when the weighted score reaches 60% or higher. Ordering assertions (e.g., `get_session_info` must precede `terminate_connection`) are also evaluated and gate the pass verdict independently of the score.

---

## 2. Prerequisites

**Binary**: build `faulttest` from source or download from the release bundle:

```bash
go build -o faulttest ./testing/cmd/faulttest/
```

**Database agent running**: `faulttest` sends prompts over the A2A protocol to whichever agent you point it at. The gateway is the most convenient entry point for authenticated queries:

```bash
helpdesk-client --gateway http://your-gateway:8080 --api-key sk-...
# Agents reachable via the gateway at POST /api/v1/query
```

Alternatively, point directly at the database agent's A2A port (default 1100).

**Database access**: the tool needs a libpq connection string to inject and tear down SQL faults. The same connection string is embedded in the prompt so the agent uses it for all tool calls.

---

## 3. Modes of operation

### 3.1 External mode (SQL only)

`--external` restricts the run to faults marked `external_compat: true` in the catalog. These faults are injected and torn down purely through SQL — no Docker access, no OS shell, no cluster control plane required. Anything injectable over a standard PostgreSQL connection qualifies.

```bash
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

`--remediate` adds a recovery phase after injection and diagnosis. After the agent evaluates the fault, `faulttest` triggers either a fleet playbook or a direct agent call, then polls a verification SQL query (default `SELECT 1`) until the database responds successfully or the timeout elapses.

```bash
faulttest run --external --remediate \
  --conn "host=staging-db port=5432 dbname=mydb user=myuser password=..." \
  --db-agent http://gateway:8080 \
  --gateway http://gateway:8080 \
  --api-key sk-... \
  --infra-config infrastructure.json
```

Faults that have a `remediation` block in the catalog will trigger the playbook specified there. Faults without a `remediation` block are evaluated normally and skipped in the remediation phase.

The JSON report includes `remediation_attempted`, `remediation_passed`, and `recovery_time_seconds` for each fault where remediation ran.

---

## 4. Policy safety: the infra-config guard

Before injecting any fault, `faulttest` optionally checks that the target PostgreSQL host is present in your `infrastructure.json` and is tagged `test` or `chaos`. This prevents accidental injection against production databases.

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

Output columns: `ID`, `CATEGORY`, `SEVERITY`, `EXTERNAL` (yes/blank), `NAME`.

### 5.2 run

```
faulttest run [options]
```

Injects each fault in sequence, prompts the agent, evaluates the response, optionally remediates, tears down, and writes a JSON report.

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--conn` | — | — | PostgreSQL connection string |
| `--replica-conn` | — | — | Replica connection string (replication-lag fault) |
| `--db-agent` | — | — | Database agent A2A URL or gateway URL |
| `--k8s-agent` | — | — | Kubernetes agent A2A URL |
| `--sysadmin-agent` | — | — | SysAdmin agent A2A URL |
| `--orchestrator` | — | — | Orchestrator A2A URL (compound faults) |
| `--context` | — | — | Kubernetes context |
| `--categories` | — | all | Comma-separated categories: `database,kubernetes,host,compound` |
| `--ids` | — | all | Comma-separated fault IDs to run |
| `--external` | — | false | Only external_compat faults; SQL injection only |
| `--ssh-user` | `USER` | current user | SSH username for ssh_exec faults |
| `--ssh-key` | — | — | SSH private key path |
| `--remediate` | — | false | Run remediation phase after diagnosis |
| `--gateway` | — | `http://localhost:8080` | Gateway URL for playbook/agent remediation |
| `--api-key` | `HELPDESK_CLIENT_API_KEY` | — | Bearer token for gateway auth |
| `--infra-config` | — | — | Path to `infrastructure.json` for safety check |
| `--testing-dir` | — | auto-detected | Path to the `testing/` directory |

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

---

## 7. Example workflows

### 7.1 Smoke test a staging database

Run the full external-compatible suite against a staging database to confirm the database agent gives correct diagnoses. Takes roughly 10–20 minutes (one fault at a time, LLM calls included).

```bash
faulttest run --external \
  --conn "host=staging-db.internal port=5432 dbname=myapp user=dbuser password=$(cat .pgpass)" \
  --db-agent http://helpdesk-gateway:8080 \
  --api-key $HELPDESK_API_KEY \
  --infra-config infrastructure.json
```

The report is written to `faulttest-<run-id>.json`.

### 7.2 Full run with remediation

Same as above, but also trigger playbook-based recovery for faults that have a `remediation` spec and verify the database comes back:

```bash
faulttest run --external --remediate \
  --conn "host=staging-db.internal port=5432 dbname=myapp user=dbuser" \
  --db-agent http://helpdesk-gateway:8080 \
  --gateway http://helpdesk-gateway:8080 \
  --api-key $HELPDESK_API_KEY \
  --infra-config infrastructure.json
```

Sample output:

```
--- Testing: Max connections exhausted (db-max-connections) ---
Remediation: RECOVERED in 12.3s
Result: [PASS] score=87%

--- Testing: Long-running query blocking (db-long-running-query) ---
Result: [PASS] score=74%

=== SUMMARY ===
Passed: 9/10  Failed: 1  Skipped: 0
Report: faulttest-a3f2b1c4.json
```

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
  "tool_evidence": true,
  "ordering_pass": true,
  "response_text": "...",
  "duration": "18.4s",
  "remediation_attempted": true,
  "remediation_passed": true,
  "recovery_time_seconds": 12.3
}
```

**Score breakdown:**

| Field | Meaning |
|-------|---------|
| `keyword_pass` | At least one expected keyword found in agent response |
| `diagnosis_pass` | Agent response contains terms from the expected diagnosis category |
| `tool_evidence` | Agent response mentions at least one expected tool |
| `ordering_pass` | Tool ordering constraints satisfied (e.g., inspect before terminate) |
| `score` | Weighted combination: 50% keywords + 30% diagnosis + 20% tools |
| `passed` | `score >= 0.6` **and** `ordering_pass = true` |

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

## 9. Extending the catalog

The catalog (`testing/catalog/failures.yaml`) is a YAML file versioned with the project. Each fault follows this schema:

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

  # Mark as externally injectable (SQL only, no Docker/OS needed).
  external_compat: true

  # Optional: override inject/teardown for --external mode.
  external_inject:
    type: sql
    script_inline: "..."
  external_teardown:
    type: sql
    script_inline: "..."

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
      category: "my_diagnosis_category"
    # Optional: assert tool A is mentioned before tool B.
    expected_tool_order:
      - [get_session_info, terminate_connection]

  timeout: 60s
  governance_gap: false          # true = known gap; failure is logged, not asserted
```

After adding a fault, update the count assertions in both test files:

- `testing/faultlib/faultlib_test.go` — `want 27` → new total
- `testing/cmd/faulttest/config_test.go` — same; also update per-category counts

Run `go test ./testing/faultlib/... ./testing/cmd/faulttest/...` to confirm.
