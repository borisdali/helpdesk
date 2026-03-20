# aiHelpDesk Fleet Runner

`fleet-runner` applies a single operation across a subset of `infrastructure.json` targets with staged rollout. It is designed for operations that need to be repeated safely across many database servers — diagnostic sweeps, configuration checks, table health reports, or targeted write operations (e.g. terminating idle connections) — without manual coordination.

---

## How it works

1. **Target resolution** — filters `infrastructure.json` by tags, explicit names, or both
2. **Preflight checks** — verifies each server is reachable before any stage executes
3. **Canary phase** — applies the change to the first N servers sequentially; any failure aborts the job
4. **Wave phase** — applies the change to remaining servers in parallel waves; a circuit breaker aborts the job if the failure rate exceeds the configured threshold
5. **Audit trail** — every tool call carries `X-Purpose: fleet_rollout` and `X-Purpose-Note: job_id=<id> server=<name> stage=<stage>` so the full fleet job is traceable in the Governance audit trail

---

## Job definition file

Fleet runner reads a JSON file describing the job:

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

This job collects table statistics (dead rows, bloat ratio, `last_vacuum`, `last_autovacuum`, `last_analyze`) across all production databases, letting you identify tables that need manual `VACUUM ANALYZE` attention.

### `change` object

| Field | Description |
|-------|-------------|
| `steps` | Array of steps to execute on each target server. At least one step is required. |

Each step has the following fields:

| Field | Description |
|-------|-------------|
| `agent` | `"database"` or `"k8s"` |
| `tool` | Tool name. Database tools: `check_connection`, `get_server_info`, `get_database_info`, `get_active_connections`, `get_connection_stats`, `get_database_stats`, `get_config_parameter`, `get_replication_status`, `get_lock_info`, `get_table_stats`, `get_session_info`, `cancel_query`, `terminate_connection`, `terminate_idle_connections`. |
| `args` | Tool arguments. The server identifier (`connection_string` or `context`) is injected automatically per target. |
| `on_failure` | `"stop"` (default) to abort the server on failure, or `"continue"` to log the error and proceed to the next step. |

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
| `canary_count` | `1` | Number of canary servers (sequential, any failure aborts). |
| `wave_size` | `0` | Servers per parallel wave. `0` = all remaining in one wave. |
| `wave_pause_seconds` | `0` | Pause between waves (seconds). |
| `failure_threshold` | `0.5` | Fraction of failures that trips the circuit breaker (0.0–1.0). |
| `dry_run` | `false` | Print the plan without contacting Gateway or auditd. |

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
      - action: write
        effect: require_approval
      - action: read
        effect: allow
```

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
```

---

## Dry-run example

```bash
./fleet-runner --job-file jobs/vacuum-prod.json --dry-run
```

Output:
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
