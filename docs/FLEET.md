# aiHelpDesk Fleet Management

While resolving issues with a specific database without engaging an often lengthy vendor support protocol is useful, aiHelpDesk also offers the Fleet Management capabilities where a set of diagnostic or remediation operations can be safely repeated across multiple databases. Examples here include the diagnostic sweeps, configuration checks, table health reports, or specific targeted write operations (e.g. changing a flag, terminating idle connections, etc.) — all without manual coordination, but with the optional human operator approval for the mission critical databases.

To be sure, safety is the key here, especially for the mutations that target more than a single database and so multiple precaution, verification and approval mechanisms are part of this aiHelpDesk Fleet Management module as described on this page.

The core architectural element of this aiHelpDesk module is the `fleet-runner` that is designed to apply a sequence of operations across a subset of `infrastructure.json` targets via a staged progressive rollout with the optional canary and wave phases, preflight checks, circuit breaker, full mandatory [audit trail](AUDIT.md#65-fleet-jobs) while also adhering to the normal aiHelpDesk [identity & access](IDENTITY.md#24-fleet-runner-authentication) mechanisms.

---

## How it works

1. **Target resolution** — filters `infrastructure.json` by tags, explicit names, or both
2. **Preflight checks** — verifies each server is reachable before any stage executes
3. **Approval gate** — if any step is a write or destructive operation, pauses and waits for human approval before contacting any server
4. **Canary phase** — applies all steps to the first N servers sequentially; any stop-failure aborts the job
5. **Wave phase** — applies all steps to remaining servers in parallel waves; a circuit breaker aborts the job if the failure rate exceeds the configured threshold
6. **Audit trail** — every tool call carries `X-Purpose: fleet_rollout` and `X-Purpose-Note: job_id=<id> server=<name> stage=<stage>` so the full fleet job is traceable in the Governance audit trail

---

## Job definition file

Here's an example of a JSON file describing a single-step job (in this case its `get_table_stats`) that is submitted to the `fleet-runner` for execution:

```json
{
  "name": "vacuum-health-prod-dbs",
  "change": {
    "steps": [
      {
        "agent": "database",
        "tool": "get_table_stats",
        "args": {"schema_name": "public"},
        "on_failure": "stop"
      }
    ]
  },
  "targets": {
    "tags": ["production"],
    "exclude": ["prod-db-3"]
  },
  "strategy": {
    "canary_count": 1,
    "wave_size": 3,
    "wave_pause_seconds": 10,
    "failure_threshold": 0.5
  }
}
```

This particular job collects table statistics (dead rows, bloat ratio, `last_vacuum`, `last_autovacuum`, `last_analyze`) across all production databases, letting you identify tables that may need `VACUUM ANALYZE` attention.

### `change` object

| Field | Description |
|-------|-------------|
| `steps` | Array of steps to execute on each target server. At least one step is required. |

Each step has the following fields:

| Field | Description |
|-------|-------------|
| `agent` | `"database"` or `"k8s"` |
| `tool` | Tool name. Database tools: `check_connection`, `get_server_info`, `get_database_info`, `get_active_connections`, `get_connection_stats`, `get_database_stats`, `get_config_parameter`, `get_replication_status`, `get_lock_info`, `get_table_stats`, `get_session_info`, `cancel_query`, `terminate_connection`, `terminate_idle_connections`. Use `GET /api/v1/tools` to list all available tools with their action classes. |
| `args` | Tool arguments. The server identifier (`connection_string` or `context`) is injected automatically per target — do not include it here. |
| `on_failure` | `"stop"` (default) to abort the server on failure, or `"continue"` to log the error and proceed to the next step. |

**Multi-step example** — check connections then collect table stats, continuing past step 1 failures:

```json
{
  "name": "prod-health-sweep",
  "change": {
    "steps": [
      {
        "agent": "database",
        "tool": "check_connection",
        "on_failure": "continue"
      },
      {
        "agent": "database",
        "tool": "get_table_stats",
        "args": {"schema_name": "public"},
        "on_failure": "stop"
      }
    ]
  },
  "targets": {"tags": ["production"]},
  "strategy": {"canary_count": 1, "wave_size": 5, "failure_threshold": 0.3}
}
```

When `on_failure: "continue"` steps fail, the server's overall status is recorded as `partial` rather than `failed`. Whether `partial` counts as a circuit-breaker failure is controlled by `count_partial_as_success`.

### `targets` object

| Field | Description |
|-------|-------------|
| `tags` | Include servers whose tags contain any of these values. |
| `names` | Include servers by their exact `infrastructure.json` name. |
| `exclude` | Remove these server names from the resolved set. |

If neither `tags` nor `names` is specified, all servers in `infrastructure.json` are selected (minus `exclude`).

### `strategy` object

| Field | Default | Description |
|-------|---------|-------------|
| `canary_count` | `1` | Number of canary servers (sequential, any stop-failure aborts). |
| `wave_size` | `0` | Servers per parallel wave. `0` = all remaining in one wave. |
| `wave_pause_seconds` | `0` | Pause between waves (seconds). |
| `failure_threshold` | `0.5` | Fraction of failures that trips the circuit breaker (0.0–1.0). |
| `dry_run` | `false` | Print the plan without contacting Gateway or auditd. |
| `count_partial_as_success` | `false` | When `true`, servers with `partial` status (continue-on-failure steps) do not count toward the circuit-breaker failure rate. |
| `approval_timeout_seconds` | `0` | How long to wait for human approval before aborting. `0` = wait indefinitely. |
| `schema_drift` | `"abort"` | Policy for schema drift detection: `"abort"` (default), `"warn"`, or `"ignore"`. Overrides the `--schema-drift` flag and `HELPDESK_SCHEMA_DRIFT` env var for this specific job. |

### `tool_snapshots` object (generated by the Planner)

When a job is generated by the Planner (`POST /api/v1/fleet/plan`), a `tool_snapshots` object is added to the job definition automatically. It records the schema fingerprint and agent version of every tool in the plan at the moment the plan was created:

```json
{
  "name": "prod-connection-health",
  "change": { "steps": [...] },
  "targets": { "tags": ["production"] },
  "strategy": { "canary_count": 1 },
  "tool_snapshots": {
    "check_connection": {
      "agent_version": "1.2.0",
      "schema_fingerprint": "a3f9c2b17e84",
      "captured_at": "2026-03-20T14:22:00Z"
    }
  }
}
```

Fleet-runner compares these snapshots against the live tool registry at execution time. If a tool's schema changed between plan and execution, fleet-runner applies the `schema_drift` policy.

Jobs authored by hand without `tool_snapshots` are treated as a drift condition (same as a fingerprint mismatch) unless `schema_drift` is set to `"ignore"`.

---

## Schema drift detection

Fleet jobs are created at plan time but may run hours or days later. If an agent is redeployed with a changed tool schema (renamed argument, new required field, removed parameter) between plan and execution, the job would silently dispatch stale args to every target server.

Schema drift detection prevents this by comparing the schema fingerprint recorded at plan time against the live registry at the start of execution.

### Detection policy

Policy is resolved in priority order (highest wins):

1. `strategy.schema_drift` in the job file
2. `--schema-drift` CLI flag
3. `HELPDESK_SCHEMA_DRIFT` env var
4. `"abort"` (hardcoded default)

| Condition | `abort` (default) | `warn` | `ignore` |
|-----------|------------------|--------|---------|
| No change | silent | silent | silent |
| Version changed, same fingerprint | warn + proceed | warn + proceed | silent |
| Fingerprint changed | **error — abort** | warn + proceed | silent |
| Tool removed from registry | **error — abort** | warn + proceed | silent |
| No `tool_snapshots` in job file | **error — abort** | warn + proceed | silent |

### Abort error format

```
schema drift detected for tool "get_status_summary":
  planned against: fingerprint=a3f9c2  version=0.5.0  captured=2026-03-15T10:30:00Z
  current:         fingerprint=def456  version=0.6.0
  → aborting (set strategy.schema_drift=warn to override)
```

The job exits before any server is contacted. No approval request is created.

### Dry-run behavior

In dry-run mode (`--dry-run` or `strategy.dry_run: true`), drift results are printed as a table even when the policy is `"ignore"`, giving operators full visibility before committing to execution.

### Overriding drift policy for a single job

Add `"schema_drift": "warn"` to the `strategy` object in the job file before running:

```json
{
  "strategy": {
    "canary_count": 1,
    "schema_drift": "warn"
  }
}
```

This overrides both the CLI flag and environment variable for that specific job.

---

## Approval gating

When any step in the job has an action class of `write` or `destructive`, fleet runner pauses **before contacting any server** and submits an approval request to auditd. Execution only proceeds after a human explicitly approves.

To approve or deny a pending fleet job approval:

```bash
# List pending approvals
curl http://localhost:1199/v1/approvals/pending | jq .

# Approve
curl -X POST http://localhost:1199/v1/approvals/apr_abc123/approve \
  -H "Content-Type: application/json" \
  -d '{"approved_by": "ops-lead", "reason": "Reviewed target list and step plan"}'

# Deny
curl -X POST http://localhost:1199/v1/approvals/apr_abc123/deny \
  -H "Content-Type: application/json" \
  -d '{"denied_by": "ops-lead", "reason": "Too broad — scope to staging first"}'
```

Or use the `approvals` CLI:

```bash
./approvals list
./approvals approve apr_abc123
```

**Dry-run** prints `APPROVAL WOULD BE REQUIRED` without creating an approval request or contacting any server.

**Timeout:** set `approval_timeout_seconds` in the strategy. If the approval is not resolved within the window, the job is aborted and the approval is cancelled. `0` (default) waits indefinitely.

---

## CLI flags

```
fleet-runner [flags]

Required:
  --job-file string     Path to JSON job definition file

Optional:
  --gateway string      Gateway URL (default: HELPDESK_GATEWAY_URL or http://localhost:8080)
  --audit-url string    Auditd URL for job tracking (default: HELPDESK_AUDIT_URL or http://localhost:1199)
  --api-key string      Service account API key (default: HELPDESK_CLIENT_API_KEY)
  --infra string        Path to infrastructure.json (default: HELPDESK_INFRA_CONFIG or infrastructure.json)
  --dry-run             Override strategy.dry_run: print plan, exit 0
  --canary int          Override strategy.canary_count
  --wave-size int       Override strategy.wave_size
  --pause int           Override strategy.wave_pause_seconds
  --schema-drift string Schema drift policy: abort, warn, ignore (default: abort; overridden by strategy.schema_drift in the job file)
  --log-level string    Log level: debug, info, warn, error (default: info)
```

Environment variables (take precedence over defaults, overridden by flags):

| Variable | Description |
|----------|-------------|
| `HELPDESK_GATEWAY_URL` | Gateway URL |
| `HELPDESK_AUDIT_URL` | Auditd URL |
| `HELPDESK_CLIENT_API_KEY` | Service account API key |
| `HELPDESK_INFRA_CONFIG` | Path to infrastructure.json |
| `HELPDESK_FLEET_JOB_FILE` | Path to job file |
| `HELPDESK_CLIENT_USER` | Identity recorded as `submitted_by` (default: `fleet-runner`) |
| `HELPDESK_SCHEMA_DRIFT` | Default schema drift policy (`abort`, `warn`, `ignore`). Overridden by `--schema-drift` flag; both overridden by `strategy.schema_drift` in the job file. |

---

## Natural language job Planner

The Gateway can generate a fleet job definition from a plain English description. It builds context from the live infrastructure inventory and tool catalog, calls the LLM, then validates every generated tool name against the tool registry and checks that no restricted server (tagged with `sensitivity`) is targeted without explicit exclusion.

**The Planner never submits a job.** It returns a `job_def` for human review; you run it manually with `fleet-runner --job-file`.

### Generate a plan

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/plan \
  -H "Content-Type: application/json" \
  -d '{
    "description": "Check connection health on all production databases",
    "target_hints": ["production", "non-pii"]
  }' | jq .
```

Response:

```json
{
  "job_def": {
    "name": "prod-connection-health",
    "change": {
      "steps": [
        {"agent": "database", "tool": "check_connection", "on_failure": "stop"}
      ]
    },
    "targets": {"tags": ["production"], "exclude": ["prod-users-db"]},
    "strategy": {"canary_count": 1, "wave_size": 0, "failure_threshold": 0.5}
  },
  "job_def_raw": "{\n  \"name\": \"prod-connection-health\", ...\n}",
  "planner_notes": "Runs check_connection on all production servers. prod-users-db excluded because it is tagged pii.",
  "requires_approval": false,
  "written_steps": [],
  "excluded_servers": ["prod-users-db"],
  "warning_messages": []
}
```

| Field | Description |
|-------|-------------|
| `job_def` | Parsed JobDef object ready to save as JSON. |
| `job_def_raw` | Pretty-printed JSON string (copy-paste ready). |
| `planner_notes` | Plain English summary of what the job does and why. |
| `requires_approval` | `true` if any step has action class `write` or `destructive`. |
| `written_steps` | Tool names that triggered `requires_approval`. |
| `excluded_servers` | Servers the Planner excluded (usually restricted/PII servers). |
| `warning_messages` | Non-fatal warnings from the Planner (e.g. broad target scope). |

### Save and run the plan

```bash
# Save the job definition
curl -s -X POST http://localhost:8080/api/v1/fleet/plan \
  -H "Content-Type: application/json" \
  -d '{"description": "Check connection health on all production databases"}' \
  | jq -r '.job_def_raw' > jobs/prod-health.json

# Review it
cat jobs/prod-health.json

# Run it (dry-run first)
./fleet-runner --job-file jobs/prod-health.json --dry-run
./fleet-runner --job-file jobs/prod-health.json
```

### Using the helpdesk client

```bash
./helpdesk-client --plan-fleet-job "terminate idle connections older than 30 minutes on staging" \
  --target-hints "staging"
```

The client prints the plan with approval warnings and a ready-to-run command.

### Planner safety guarantees

All four checks are deterministic and run after the LLM response — they cannot be bypassed by prompt content.

- **Unknown tools are rejected** with `422`. Every generated tool name is validated against the live tool registry.
- **Invalid step args are rejected** with `422`. When a tool has a declared parameter schema, the Planner validates each step's args: unknown parameters (not in the schema's `properties`) and missing required parameters both produce a `422` error with a descriptive message. Type mismatches are not checked — JSON number/string coercion is left to the agent.
- **Unknown tags are rejected** with `422`. Every tag in `targets.tags` must exist verbatim in `infrastructure.json`. The Planner will not infer or substitute tag names (e.g. "staging" → "development"). The error response lists all available tags so you can correct the description.
- **Restricted servers are rejected** with `422`. Any server with a non-empty `sensitivity` field that appears in the resolved target set causes an error. Refine the description or add the server to `targets.exclude`.
- **The Planner never auto-submits.** A human must review and run the job.

`warning_messages` in the response are genuinely non-fatal notices (e.g. broad target scope, empty exclusion list). They do not indicate a validation failure.

### Parameter schemas in the tool catalog

When tools have declared parameter schemas (populated via `ComputeInputSchemas` in the agent), the Planner includes a `Parameters:` block in its tool catalog. This gives the LLM exact parameter names, types, and required markers — reducing hallucinated argument names significantly:

```
  get_status_summary  agent=database  class=read  caps=[uptime, version, ...]
    Description: Returns a concise status snapshot...
    Parameters:
      connection_string (string, required): PostgreSQL connection string
      verbose (boolean): Include extended diagnostics
```

Tools without a declared schema fall back to the single-line format. Plan quality degrades gracefully rather than breaking.

### Configuration

| Variable | Description |
|----------|-------------|
| `HELPDESK_API_KEY` | API key for the Planner LLM (same key used by agents). `ANTHROPIC_API_KEY` is accepted as a fallback. If neither is set, `POST /api/v1/fleet/plan` returns `503`. |
| `HELPDESK_MODEL_NAME` | LLM model to use (default: `claude-haiku-4-5-20251001`). Same variable used by all other components. |

---

## Identity and authentication

Fleet runner authenticates as the `fleet-runner` service account (defined in `users.yaml`). Every request to the Gateway carries:

```
Authorization: Bearer <HELPDESK_CLIENT_API_KEY>
X-Purpose: fleet_rollout
X-Purpose-Note: job_id=flj_abc123 server=prod-db-1 stage=canary
```

Configure the service account in `users.yaml`:

```yaml
service_accounts:
  - id: fleet-runner
    roles: [sre-automation]
    api_key_hash: "$argon2id$v=19$m=65536,t=3,p=4$..."
```

Generate the hash:
```bash
./hashapikey
```

---

## Policy configuration

Add a policy rule to control what fleet-runner can do:

```yaml
# policies.yaml
policies:
  - name: fleet-runner-policy
    principals:
      - service: fleet-runner
    resources:
      - type: database
        match:
          tags: [production]
    rules:
      - action: read
        effect: allow
      - action: write
        effect: allow
        conditions:
          require_approval: true
          allowed_purposes: [fleet_rollout]
      - action: destructive
        effect: deny
```

See `policies.example.yaml` for the full reference policy including the `fleet-runner-policy` entry with row limits and purpose restrictions.

---

## Viewing job status

Fleet job records are stored in the auditd database. Query them via the Gateway:

```bash
# List recent jobs
curl http://localhost:8080/api/v1/fleet/jobs | jq .

# Get a specific job
curl http://localhost:8080/api/v1/fleet/jobs/flj_abc123 | jq .

# Get per-server status
curl http://localhost:8080/api/v1/fleet/jobs/flj_abc123/servers | jq .

# Get per-step status for one server
curl http://localhost:8080/api/v1/fleet/jobs/flj_abc123/servers/prod-db-1/steps | jq .
```

Per-step response:

```json
[
  {
    "step_index": 0,
    "tool": "check_connection",
    "status": "success",
    "output": "Connection OK — PostgreSQL 16.2",
    "started_at": "2024-01-15T02:00:01Z",
    "finished_at": "2024-01-15T02:00:02Z"
  },
  {
    "step_index": 1,
    "tool": "get_table_stats",
    "status": "success",
    "output": "...",
    "started_at": "2024-01-15T02:00:02Z",
    "finished_at": "2024-01-15T02:00:04Z"
  }
]
```

Server `status` values: `pending`, `running`, `success`, `partial` (some `continue`-on-failure steps failed), `failed` (a `stop`-on-failure step failed).

Step `status` values: `pending`, `success`, `failed`.

---

## Dry-run example

```bash
./fleet-runner --job-file jobs/vacuum-prod.json --dry-run
```

Output (read-only job):
```
DRY RUN — fleet job: vacuum-health-prod-dbs
Steps (1):
  [1] database/get_table_stats  (on_failure=stop)
Resolved servers (5):
  prod-db-1                                   [canary]
  prod-db-2                                   [wave-1]
  prod-db-3                                   [wave-1]
  prod-db-4                                   [wave-2]
  prod-db-5                                   [wave-2]

Strategy:
  canary_count:        1
  wave_size:           2
  wave_pause_seconds:  10
  failure_threshold:   50%

No gateway or auditd contact (dry run).
```

For jobs with write or destructive steps:
```
APPROVAL WOULD BE REQUIRED: job contains write operations
```

---

## Deployment

### Host (binary tarball)

```bash
# Run directly
./fleet-runner --job-file jobs/vacuum-prod.json \
  --gateway http://localhost:8080 \
  --audit-url http://localhost:1199 \
  --api-key $(cat .fleet-runner-key)

# Dry-run first
./fleet-runner --job-file jobs/vacuum-prod.json --dry-run
```

`fleet-runner` is included in the release tarball alongside all other aiHelpDesk binaries.

### Docker Compose

```bash
# Dry-run
docker compose --profile fleet run --rm fleet-runner \
  --job-file /jobs/vacuum-prod.json --dry-run

# Execute
FLEET_RUNNER_API_KEY=<key> FLEET_JOBS_DIR=./jobs \
  docker compose --profile fleet run --rm fleet-runner \
  --job-file /jobs/vacuum-prod.json
```

### Kubernetes (Helm CronJob)

```yaml
# values-fleet.yaml
fleetRunner:
  enabled: true
  schedule: "0 2 * * *"        # 2 AM daily
  jobFile: "/etc/helpdesk/fleet-job.json"
  apiKeySecret: fleet-runner-key
  apiKeyKey: api-key
  extraVolumes:
    - name: fleet-job
      configMap:
        name: fleet-job-config
  extraVolumeMounts:
    - name: fleet-job
      mountPath: /etc/helpdesk/fleet-job.json
      subPath: fleet-job.json
      readOnly: true
```

```bash
# Create the job definition ConfigMap
kubectl create configmap fleet-job-config \
  --from-file=fleet-job.json=jobs/vacuum-prod.json

# Create the API key Secret
kubectl create secret generic fleet-runner-key \
  --from-literal=api-key=$(cat .fleet-runner-key)

# Deploy / update
helm upgrade helpdesk ./deploy/helm/helpdesk -f values-fleet.yaml
```

---

## Audit trail

Every fleet-runner tool call generates an audit event linking back to the job:

```json
{
  "event_type": "tool_call",
  "agent": "postgres_database_agent",
  "tool_name": "get_table_stats",
  "purpose": "fleet_rollout",
  "purpose_note": "job_id=flj_abc123 server=prod-db-2 stage=wave-1",
  "principal": "fleet-runner"
}
```

Query all events for a fleet job:
```bash
curl "http://localhost:1199/v1/events?limit=200" | \
  jq '.[] | select(.purpose_note | startswith("job_id=flj_abc123"))'
```
