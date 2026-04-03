# aiHelpDesk API Reference

aiHelpDesk exposes three distinct API surfaces:

| API | Port | Who uses it |
|---|---|---|
| [Gateway REST API](#gateway-rest-api-port-8080) | `8080` | Operators, upstream agents, automation |
| [auditd API](#auditd-api-port-1199) | `1199` | Operators (approval writes), `approvals` CLI, `govexplain` CLI |
| [Agent A2A API](#agent-a2a-api-ports-11001106) | `1100`–`1106` | Custom integrations that speak the A2A protocol natively |

All three services speak HTTP and return `application/json`.

---

## Gateway REST API (port 8080)

The gateway is the primary entry point. It translates REST calls into [A2A](https://google.github.io/A2A/) messages to sub-agents and proxies read-only governance queries to `auditd`. Use this for all routine operations.

**Base URL:** `http://localhost:8080`

### Response envelope

Agent endpoints (`/api/v1/query`, `/api/v1/db/{tool}`, etc.) return:

```json
{
  "agent":      "postgres_database_agent",
  "task_id":    "task-abc123",
  "state":      "completed",
  "text":       "Replication lag is 0 ms on all replicas.",
  "context_id": "ctx_7f3a9b2e",
  "artifacts":  []
}
```

`context_id` is present on all agent responses. Pass it back in `POST /api/v1/query` to continue the conversation in the same agent session (see [`POST /api/v1/query`](#post-apiv1query)).

Error responses: `{ "error": "<reason>" }`

The response header `X-Trace-ID` is set on every agent call. Pass it in the request to pin a specific trace ID for end-to-end correlation across gateway and agent audit logs.

### HTTP status codes

| Status | Meaning |
|---|---|
| `200 OK` | Agent task completed and the response text is the agent's output |
| `400 Bad Request` | Malformed request (missing required fields, invalid JSON, or unknown tool name) |
| `401 Unauthorized` | Authentication failed (bad or missing API key / JWT) or caller is anonymous on an endpoint that requires identity |
| `403 Forbidden` | Role-based authorization denied the request (wrong or missing role), a governance policy denied the operation, or the operating mode blocks the action. The response body identifies which layer rejected the request. |
| `422 Unprocessable Entity` | The request was well-formed but failed semantic validation (e.g. fleet planner returned an unknown tool or targeted a restricted server) |
| `502 Bad Gateway` | The A2A task itself failed (agent runner error), or the agent service is unreachable |
| `503 Service Unavailable` | A required service (e.g. fleet planner, auditd) is not configured |

**Note on `403` vs `200` for policy denials:** For direct tool calls (`/api/v1/db/{tool}`, `/api/v1/k8s/{tool}`), policy denials are detected from the agent response text and returned as `403`. For natural-language queries (`/api/v1/query`), the agent decides how to present a denial in its prose response — the gateway cannot reliably distinguish a policy-blocked tool call from a successful but empty result in that path, so callers should inspect `text` for policy denial details.

---

### `GET /health`

Liveness probe. Returns `{"status":"ok"}` when the gateway is up and all agents have been discovered. Useful for load balancer health checks and `docker compose up --wait`.

```bash
curl http://localhost:8080/health
```

---

### `GET /api/v1/agents`

List all registered agents and their A2A metadata.

```bash
curl http://localhost:8080/api/v1/agents
```

Response: array of agent objects with `name`, `invoke_url`, `description`, `version`, `skills`.

---

### `GET /api/v1/tools`

List all tools registered in the tool registry, built from the live agent cards. Includes the tool's action class (`read`, `write`, `destructive`) and parameter schema.

```bash
curl http://localhost:8080/api/v1/tools | jq .
```

Response: array of tool entries:

```json
[
  {
    "name":         "check_connection",
    "agent":        "database",
    "description":  "Test connectivity to a database server",
    "action_class": "read",
    "input_schema": { "type": "object", "properties": { ... } }
  },
  {
    "name":         "terminate_connection",
    "agent":        "database",
    "description":  "Terminate a specific backend connection by PID",
    "action_class": "destructive",
    "input_schema": { ... }
  }
]
```

Use this to discover valid tool names before writing a fleet job definition. Unknown tool names passed to `/api/v1/db/{tool}` or `/api/v1/k8s/{tool}` are rejected with `400` — the list here is the authoritative source.

---

### `GET /api/v1/tools/{toolName}`

Get a single tool by name.

```bash
curl http://localhost:8080/api/v1/tools/get_table_stats | jq .
```

Returns `404` if the tool is not registered.

---

### `GET /api/v1/roles`

Returns the live HTTP authorization table the gateway is currently enforcing. Use this to discover what role a caller needs for a given endpoint.

```bash
curl http://localhost:8080/api/v1/roles | jq .
```

Response:

```json
{
  "roles": [
    {
      "name": "dba",
      "grants": [
        "POST /api/v1/db/{tool}",
        "POST /v1/approvals/{approvalID}/approve",
        "POST /v1/approvals/{approvalID}/deny"
      ]
    },
    {
      "name": "fleet-operator",
      "grants": ["POST /api/v1/fleet/jobs"]
    },
    {
      "name": "sre",
      "grants": [
        "POST /api/v1/db/{tool}",
        "POST /api/v1/k8s/{tool}"
      ]
    }
  ],
  "admin_role": "admin",
  "aliases": {
    "database-admin": "dba"
  },
  "enforcing": true
}
```

| Field | Description |
|---|---|
| `roles[].name` | Canonical role name |
| `roles[].grants` | Routes unlocked by this role. Routes not listed here are open to any authenticated user (or public). |
| `admin_role` | Role name that bypasses all checks. Default: `"admin"`. |
| `aliases` | Map of IdP group names → canonical role names (from `role_aliases` in `users.yaml`). |
| `enforcing` | `true` when authorization is active; `false` in non-enforcing (development) mode. |

This endpoint is always public — no authentication required. See [AUTHZ.md](AUTHZ.md) for the full authorization reference.

---

### `POST /api/v1/query`

Send a natural-language question to an agent.

| Field | Type | Required | Description |
|---|---|---|---|
| `agent` | string | yes | `database`, `db`, `k8s`, or `incident` |
| `message` | string | yes | The question or instruction (`query` is accepted as an alias) |
| `context_id` | string | no | Resume an existing agent session. Pass the `context_id` returned by a previous response to continue a multi-turn conversation. Omit (or pass `""`) to start a new session. |

The response includes `context_id` alongside the agent's reply:

```json
{
  "agent":      "postgres_database_agent",
  "task_id":    "task-abc123",
  "state":      "completed",
  "text":       "I found 3 idle connections. Shall I terminate them?",
  "context_id": "ctx_7f3a9b2e"
}
```

Pass `context_id` back on the next request to continue the conversation:

```bash
# Turn 1 — start a new session
curl -s -X POST http://localhost:8080/api/v1/query \
  -H "Content-Type: application/json" \
  -d '{"agent": "database", "message": "Are there idle connections on prod-db?"}'
# → response includes "context_id": "ctx_7f3a9b2e"

# Turn 2 — continue the same session
curl -s -X POST http://localhost:8080/api/v1/query \
  -H "Content-Type: application/json" \
  -d '{"agent": "database", "message": "yes, terminate them", "context_id": "ctx_7f3a9b2e"}'
```

**Session lifetime:** sessions live in agent process memory. An agent restart clears all sessions — the next request with a stale `context_id` starts a fresh session silently.

---

### `POST /api/v1/incidents`

Create an incident diagnostic bundle. The body is passed as-is to the incident agent.

```bash
curl -s -X POST http://localhost:8080/api/v1/incidents \
  -H "Content-Type: application/json" \
  -d '{"host": "prod-db.example.com", "description": "OOM killer triggered"}'
```

---

### `GET /api/v1/incidents`

List all previously created incident bundles.

```bash
curl http://localhost:8080/api/v1/incidents
```

---

### `POST /api/v1/db/{tool}`

Invoke a specific database agent tool directly by name. The body is a JSON object of tool parameters. Use `GET /api/v1/tools` to discover valid tool names and their parameter schemas.

**Important:** `{tool}` must be a registered tool name (e.g. `check_connection`, `get_server_info`). Unknown tool names are validated against the tool registry and rejected with `400 Bad Request` before the agent is contacted.

```bash
curl -s -X POST http://localhost:8080/api/v1/db/check_replication_lag \
  -H "Content-Type: application/json" \
  -d '{"host": "prod-db.example.com", "port": 5432}'
```

#### Database tool quick reference

All tools accept `connection_string` (PostgreSQL DSN; falls back to `HELPDESK_DB_URL` env). Action class is `read` unless noted.

| Tool | Key parameters | What it returns |
|------|----------------|----------------|
| `check_connection` | — | Connectivity and server version |
| `get_server_info` | — | Version, uptime, PG settings summary |
| `get_status_summary` | — | Compact health snapshot (supersedes server_info + connection_stats) |
| `get_database_stats` | — | Cache hit ratio, buffer and tuple counts |
| `get_connection_stats` | — | Active/idle/waiting connection counts by state |
| `get_active_connections` | — | `pg_stat_activity` snapshot |
| `get_session_info` | `pid` (required) | Detailed session state, lock holds, uncommitted write estimate |
| `get_lock_info` | — | Lock grants and waits from `pg_locks` |
| `get_replication_status` | — | Streaming replica lag and WAL position |
| `get_table_stats` | `schema` | Dead tuple counts, autovacuum timestamps per table |
| `get_config_parameter` | `parameter` (required) | Single GUC value and source |
| `get_database_info` | — | Database list with sizes and owner |
| `get_pg_settings` | `category`, `show_all` | Non-default GUC values, grouped by category |
| `get_extensions` | — | Installed extensions with versions |
| `get_baseline` | — | Combined report: server info + settings + extensions + disk usage |
| `get_slow_queries` | `limit` | Top-N queries by total execution time from `pg_stat_statements` |
| `get_vacuum_status` | `min_dead_ratio` | Tables with high dead-tuple ratio, last autovacuum timestamps |
| `get_disk_usage` | `top_n` | Database sizes (`pg_database_size`) + largest tables (`pg_total_relation_size`) |
| `get_wait_events` | — | Aggregated wait event types from `pg_stat_activity` |
| `get_blocking_queries` | — | Blocking/blocked session pairs with lock type and relation |
| `explain_query` | `query` (required), `allow_dml` | `EXPLAIN (ANALYZE, BUFFERS)` output; DML wrapped in BEGIN/ROLLBACK when `allow_dml=true` |
| `cancel_query` | `pid` (required) | `pg_cancel_backend` — **write** |
| `terminate_connection` | `pid` (required) | `pg_terminate_backend` — **destructive** |
| `kill_idle_connections` | `idle_threshold_seconds` | Terminate all idle connections older than threshold — **destructive** |
| `read_pg_log` | `lines`, `filter` | Read the tail of the most-recently-modified PostgreSQL log file via `pg_read_file()`. Requires a live DB connection and `pg_read_server_files` privilege or superuser. Returns up to 128 KB (last ~1000 lines). Use `filter` (case-insensitive substring) to focus on errors. |
| `read_uploaded_file` | `upload_id` (required), `filter` | Read the content of a file previously uploaded by an operator via `POST /api/v1/fleet/uploads`. Use this when `read_pg_log` is not available (e.g. DB is completely down). Requires `HELPDESK_AUDIT_URL` to be configured. |
| `get_saved_snapshots` | `tool_name` (required), `server_name`, `limit`, `since` | Retrieve previously recorded outputs of a tool from the audit history. Use when the DB is unreachable and you need a value captured in a prior run — e.g. `config_file` path or `data_directory` from a past `get_baseline`. Also useful for diffing two snapshots ("what changed?") or finding when a setting last changed. Returns up to 3 snapshots by default (max 10), capped at 32 KB total. Requires `HELPDESK_AUDIT_URL`. |

---

### `POST /api/v1/k8s/{tool}`

Invoke a specific Kubernetes agent tool directly by name.

```bash
curl -s -X POST http://localhost:8080/api/v1/k8s/get_pod_logs \
  -H "Content-Type: application/json" \
  -d '{"namespace": "default", "pod": "my-pod-abc123"}'
```

#### Kubernetes tool quick reference

All tools accept `context` (kubeconfig context name; defaults to current context).

| Tool | Key parameters | What it returns |
|------|----------------|----------------|
| `get_pods` | `namespace` (required), `pod_name` | Pod list with status, restarts, age |
| `get_pod_logs` | `namespace` (required), `pod_name` (required), `lines` | Container log tail |
| `get_events` | `namespace` (required), `resource_name` | K8s events (warnings + normals) |
| `describe_pod` | `namespace` (required), `pod_name` (required) | Full pod describe output |
| `get_service` | `namespace` (required), `service_name` | Service spec with ClusterIP, ports, selector |
| `get_endpoints` | `namespace` (required), `service_name` | Endpoint addresses for a service |
| `get_pod_resources` | `namespace` (required), `pod_name` | CPU/memory requests + limits; live usage via `kubectl top` when metrics-server is available |
| `get_node_status` | `node_name` | Node conditions (Ready, MemoryPressure, DiskPressure, PIDPressure), allocatable vs capacity resources |
| `scale_deployment` | `namespace` (required), `deployment_name` (required), `replicas` (required) | Scale a deployment — **destructive** |
| `restart_deployment` | `namespace` (required), `deployment_name` (required) | Rolling restart — **destructive** |
| `delete_pod` | `namespace` (required), `pod_name` (required) | Delete a pod — **destructive** |

---

### `POST /api/v1/research`

Run a web research query via the research agent.

| Field | Type | Required | Description |
|---|---|---|---|
| `query` | string | yes | The research question |

```bash
curl -s -X POST http://localhost:8080/api/v1/research \
  -H "Content-Type: application/json" \
  -d '{"query": "PostgreSQL 16 logical replication known issues"}'
```

---

### `GET /api/v1/infrastructure`

Return the loaded infrastructure inventory summary (from `HELPDESK_INFRA_CONFIG`).

```bash
curl http://localhost:8080/api/v1/infrastructure
```

```json
{
  "configured":   true,
  "db_servers":   3,
  "k8s_clusters": 1,
  "vms":          2,
  "databases":    ["prod-db", "staging-db"],
  "summary":      "3 database servers, 1 K8s cluster, 2 VMs"
}
```

---

### `GET /api/v1/databases`

List configured database servers.

```bash
curl http://localhost:8080/api/v1/databases
```

---

### Governance endpoints (gateway → auditd proxies)

All `/api/v1/governance/*` endpoints require `auditd` to be running and `HELPDESK_AUDIT_URL` set. When not configured they return `{"enabled": false, ...}`. Query parameters are forwarded verbatim to auditd.

#### `GET /api/v1/governance`

Governance system status: audit chain health, policy config, pending approval count.

```bash
curl http://localhost:8080/api/v1/governance
```

Response fields: `policy` (enabled, file, policies_count, rules_count), `approvals` (pending_count, webhook_configured, email_configured), `audit` (events_total, chain_valid, last_event_at), `timestamp`.

#### `GET /api/v1/governance/policies`

Active policy rules in human-readable form.

```bash
curl http://localhost:8080/api/v1/governance/policies
```

#### `GET /api/v1/governance/explain`

Hypothetical policy check — evaluates the policy engine without recording an event or executing any tool.

| Parameter | Required | Description |
|---|---|---|
| `resource_type` | yes | `database` or `kubernetes` |
| `resource_name` | yes | Resource name, e.g. `prod-db` |
| `action` | yes | `read`, `write`, or `destructive` |
| `tags` | no | Comma-separated tags. Auto-resolved from infra config when omitted. |
| `user_id` | no | Evaluate as a specific user |
| `role` | no | Evaluate with a specific role |

```bash
curl "http://localhost:8080/api/v1/governance/explain?resource_type=database&resource_name=prod-db&action=write"
curl "http://localhost:8080/api/v1/governance/explain?resource_type=database&resource_name=prod-db&action=destructive&tags=production,critical"
```

#### `GET /api/v1/governance/events`

Query the audit event trail (up to 100 by default).

| Parameter | Description |
|---|---|
| `session_id` | Filter by session ID |
| `trace_id` | Filter by trace ID |
| `event_type` | Filter by event type |
| `agent` | Filter by agent name |
| `action_class` | `read`, `write`, or `destructive` |
| `since` | RFC3339 lower bound, e.g. `2024-01-15T00:00:00Z` |

```bash
curl "http://localhost:8080/api/v1/governance/events?agent=postgres_database_agent"
curl "http://localhost:8080/api/v1/governance/events?action_class=destructive&since=2024-01-15T00:00:00Z"
```

#### `GET /api/v1/governance/events/{eventID}`

Single audit event by ID. Includes `policy_decision.trace` and `policy_decision.explanation` when present.

```bash
curl http://localhost:8080/api/v1/governance/events/tool_a1b2c3d4
```

#### `GET /api/v1/governance/approvals/pending`

Pending approvals queue.

```bash
curl http://localhost:8080/api/v1/governance/approvals/pending
```

#### `GET /api/v1/governance/approvals`

All approvals, filterable.

| Parameter | Description |
|---|---|
| `status` | `pending`, `approved`, or `denied` |
| `agent` | Filter by agent name |
| `trace_id` | Filter by trace ID |
| `requested_by` | Filter by requester |
| `limit` | Max results (default 100) |

```bash
curl "http://localhost:8080/api/v1/governance/approvals?status=approved&limit=50"
```

#### `GET /api/v1/governance/verify`

Verify audit chain integrity.

```bash
curl http://localhost:8080/api/v1/governance/verify
```

```json
{ "valid": true, "total_events": 142, "checked_at": "2024-01-15T12:00:00Z" }
```

---

## auditd API (port 1199)

`auditd` is the AI Governance daemon. Its HTTP API is split into three groups:

- **Audit events** — agents write events here; read access is mostly through the gateway proxy
- **Approvals** — the only write path not exposed by the gateway; operators use this directly
- **Governance info** — the same endpoints the gateway proxies

Use the `approvals` CLI (`./approvals`, `kubectl exec`, `docker compose exec`) instead of calling this API directly unless you are building a custom integration.

**Base URL:** `http://localhost:1199`

### Audit event endpoints

These are internal endpoints used by agents to record their activity. They are documented here for custom agent integrations.

#### `POST /v1/events`

Record an audit event.

#### `POST /v1/events/{eventID}/outcome`

Record the outcome of a tool call (success/failure, result summary) after the fact.

#### `GET /v1/events`

Query events directly (same parameters as the gateway proxy — see above).

#### `GET /v1/events/{eventID}`

Single event by ID.

#### `GET /v1/verify`

Audit chain integrity check (same as gateway `/api/v1/governance/verify`).

---

### Fleet endpoints (gateway → auditd proxies)

All `/api/v1/fleet/*` endpoints require `auditd` to be running and `HELPDESK_AUDIT_URL` set. They return `503` otherwise.

#### `POST /api/v1/fleet/plan`

Generate a fleet job definition from a natural language description. Requires `ANTHROPIC_API_KEY` to be set in the gateway's environment. Returns `503` if not configured.

The planner validates every generated tool name against the tool registry and rejects jobs that target restricted servers (those with a non-empty `sensitivity` in `infrastructure.json`). **The planner never submits a job** — it returns a plan for human review.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `description` | string | yes | Plain English description of what the job should do |
| `target_hints` | []string | no | Hints to guide target selection (e.g. `["production", "non-pii"]`) |

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/plan \
  -H "Content-Type: application/json" \
  -d '{"description": "check connection health on all staging databases"}'
```

Response fields: `job_def`, `job_def_raw`, `planner_notes`, `requires_approval`, `written_steps`, `excluded_servers`, `warning_messages`. See [FLEET.md](FLEET.md#natural-language-job-planner) for full details.

Status codes: `400` (missing description), `422` (unknown tool or restricted server in generated plan), `503` (infra config or tool registry not loaded, or `ANTHROPIC_API_KEY` not set).

---

#### `POST /api/v1/fleet/jobs`

Register a new fleet job in the audit record (called automatically by `fleet-runner`).

#### `GET /api/v1/fleet/jobs`

List recent fleet jobs.

```bash
curl http://localhost:8080/api/v1/fleet/jobs | jq .
```

#### `GET /api/v1/fleet/jobs/{jobID}`

Get a specific fleet job, including its full `job_def`, status, and summary.

```bash
curl http://localhost:8080/api/v1/fleet/jobs/flj_abc123 | jq .
curl http://localhost:8080/api/v1/fleet/jobs/flj_abc123 | jq '.job_def | fromjson'
```

#### `GET /api/v1/fleet/jobs/{jobID}/servers`

Get per-server execution status for a fleet job.

```bash
curl http://localhost:8080/api/v1/fleet/jobs/flj_abc123/servers | jq .
```

Server `status` values: `pending`, `running`, `success`, `partial`, `failed`.

#### `GET /api/v1/fleet/jobs/{jobID}/servers/{serverName}/steps`

Get per-step execution status for one server within a fleet job.

```bash
curl http://localhost:8080/api/v1/fleet/jobs/flj_abc123/servers/prod-db-1/steps | jq .
```

Returns an array ordered by `step_index`. Each entry includes `tool`, `status`, `output`, `started_at`, `finished_at`. Step `status` values: `pending`, `success`, `failed`.

#### `GET /api/v1/fleet/jobs/{jobID}/approval/{approvalID}`

Get the approval status for a fleet job that is waiting for human sign-off. `approvalID` is returned by `fleet-runner` in its logs when it submits the approval request.

```bash
curl http://localhost:8080/api/v1/fleet/jobs/flj_abc123/approval/apr_xyz789 | jq .
```

To approve or deny, use the auditd approval endpoints directly (see below) — the gateway does not proxy write operations on approvals.

---

#### Playbook endpoints

Playbooks are saved runbook artifacts combining a natural-language fleet intent with expert triage knowledge. aiHelpDesk ships 7 system playbooks out of the box (4 operational + 3 Database Down triage), with adaptive escalation across the triage graph. Operators create, version, and import their own. See **[PLAYBOOKS.md](PLAYBOOKS.md)** for the full reference.

#### `GET /api/v1/fleet/playbooks`

List playbooks. Returns the active version of every playbook by default. Each entry includes an inline `stats` object (series-wide run history) when at least one run exists.

Query params: `active_only` (default `true`), `include_system` (default `true`), `series_id` (filter to one series).

```bash
curl http://localhost:8080/api/v1/fleet/playbooks | jq .playbooks

# All versions of a series
curl "http://localhost:8080/api/v1/fleet/playbooks?series_id=pbs_vacuum_triage&active_only=false"
```

#### `GET /api/v1/fleet/playbooks/{playbookID}`

Get a single playbook by ID. Returns `404` if not found.

#### `POST /api/v1/fleet/playbooks`

Create a new playbook. `name` and `description` are required. Supply `series_id` to add a version to an existing series (new version starts inactive); omit it to start a new series (starts active).

Response: `201 Created` with the full playbook object. See [PLAYBOOKS.md — field reference](PLAYBOOKS.md#playbook-schema-reference) for all fields.

#### `PUT /api/v1/fleet/playbooks/{playbookID}`

Replace a playbook's fields. All fields overwritten; omitting a field clears it. Returns `404` if not found, `400` for system playbooks.

#### `POST /api/v1/fleet/playbooks/{playbookID}/activate`

Atomically promotes a version to active within its series, deactivating all others. Idempotent. Returns `404` if not found, `400` for system playbooks.

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_v2id/activate | jq .is_active
```

#### `DELETE /api/v1/fleet/playbooks/{playbookID}`

Delete a playbook. Returns `204 No Content`, `404` if not found, `400` for system playbooks.

#### `POST /api/v1/fleet/playbooks/{playbookID}/run`

Runs a playbook. Behaviour depends on the playbook's `execution_mode`.

**`execution_mode: fleet` (default)** — calls the fleet planner and returns a `FleetPlanResponse` (same shape as `POST /api/v1/fleet/plan`). The playbook's `description`, `guidance`, and `target_hints` are injected into the planner prompt. Requires LLM configuration.

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_a1b2c3d4/run \
  | jq -r '.job_def_raw' > /tmp/plan.json
./fleet-runner --job-file /tmp/plan.json --dry-run
```

**`execution_mode: agent`** — routes to the database agent as an agentic triage session. The agent gathers evidence, forms and tests hypotheses, and returns a diagnosis with recommended (not executed) remediation steps. Returns the same response shape as `POST /api/v1/query`. Used by the Database Down playbooks. The gateway parses a structured signal from the agent's response to automatically set `outcome`, `findings_summary`, and `escalated_to` on the run record.

Optional request body:

| Field | Description |
|---|---|
| `connection_string` | PostgreSQL DSN for the target database |
| `context` | Free-form operator context: server name, symptoms, log lines, recent changes. Used to evaluate `requires_evidence` patterns. |
| `context_id` | A2A session ID to resume a multi-turn session |
| `prior_run_id` | `plr_*` run ID of a prior investigation; its `findings_summary` is injected into the prompt for continuity |

Response fields (in addition to the standard `text`, `agent`, `task_id`, `context_id`):

| Field | When present | Description |
|---|---|---|
| `warnings` | `requires_evidence` patterns absent from `context` | Advisory list of missing evidence patterns. Execution is not blocked. |
| `escalation_hint` | Agent signalled escalation | Series ID (`pbs_*`) of the recommended follow-on playbook |

```bash
# First investigation — entry-point playbook
RESP=$(curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_restart_triage/run \
  -H "Content-Type: application/json" \
  -d '{"connection_string":"postgres://prod-db.example.com/app","context":"pod CrashLoopBackOff; logs show FATAL: invalid value for parameter max_connections"}')
echo "$RESP" | jq .text

# Follow-on investigation using escalation hint and prior run ID
RUN_ID=$(curl -s http://localhost:8080/api/v1/fleet/playbooks/pb_restart_triage/runs | jq -r '.runs[0].run_id')
NEXT_SERIES=$(echo "$RESP" | jq -r '.escalation_hint // empty')
if [ -n "$NEXT_SERIES" ]; then
  NEXT_PB=$(curl -s "http://localhost:8080/api/v1/fleet/playbooks?series_id=$NEXT_SERIES" \
    | jq -r '.playbooks[0].playbook_id')
  curl -s -X POST "http://localhost:8080/api/v1/fleet/playbooks/$NEXT_PB/run" \
    -H "Content-Type: application/json" \
    -d "{\"connection_string\":\"postgres://prod-db.example.com/app\",\"prior_run_id\":\"$RUN_ID\"}" \
    | jq .text
fi
```

See [PLAYBOOKS.md — Adaptive triage](PLAYBOOKS.md#adaptive-triage) for the full escalation graph, requires-evidence system, and continuity threading details.

#### `GET /api/v1/fleet/playbooks/{playbookID}/runs`

List recorded runs for a playbook, most recent first. Default limit 20, maximum 100.

```bash
curl http://localhost:8080/api/v1/fleet/playbooks/pb_a1b2c3d4/runs | jq '{count: .count, last_outcome: .runs[0].outcome}'
```

Response: `{ "runs": [...], "count": N }`. Each run object includes `run_id`, `playbook_id`, `series_id`, `execution_mode`, `outcome`, `escalated_to`, `findings_summary`, `operator`, `started_at`, `completed_at`.

#### `GET /api/v1/fleet/playbook-runs/{runID}`

Fetch a single run by its `run_id`. Useful for retrieving the `findings_summary` and `escalated_to` fields that are populated automatically after an agent-mode run completes.

```bash
curl -s http://localhost:8080/api/v1/fleet/playbook-runs/plr_3f7a2b1c \
  | jq '{outcome, findings_summary, escalated_to}'
```

Returns `404` if the run ID is not found.

#### `GET /api/v1/fleet/playbooks/{playbookID}/stats`

Aggregated outcome statistics for the **series** the playbook belongs to (all versions combined). Returns `404` if the playbook is not found.

```bash
curl http://localhost:8080/api/v1/fleet/playbooks/pb_a1b2c3d4/stats | jq '{total_runs, resolution_rate, escalation_rate}'
```

Response: `{ "series_id", "total_runs", "resolved", "escalated", "abandoned", "resolution_rate", "escalation_rate", "last_run_at" }`.

#### `PATCH /api/v1/fleet/playbook-runs/{runID}`

Record or correct the final outcome of a run. For agent-mode runs the outcome is set automatically from the agent's structured response; this endpoint lets operators override it after reviewing.

| Field | Required | Description |
|---|---|---|
| `outcome` | yes | `resolved` \| `escalated` \| `abandoned` \| `unknown` |
| `escalated_to` | no | Series ID (`pbs_*`) of the next playbook when `outcome=escalated` |
| `findings_summary` | no | Summary of what was found and what action was taken |

Returns `204 No Content`. See [PLAYBOOKS.md — Run tracking](PLAYBOOKS.md#run-tracking) for the full lifecycle and field reference.

```bash
curl -s -X PATCH http://localhost:8080/api/v1/fleet/playbook-runs/plr_3f7a2b1c \
  -H "Content-Type: application/json" \
  -d '{"outcome":"resolved","findings_summary":"Autovacuum disabled on accounts table; re-enabled and ran VACUUM ANALYZE."}'
```

#### `POST /api/v1/fleet/playbooks/import`

Convert an existing runbook into a playbook draft without persisting it. The caller reviews the draft and saves it via `POST /api/v1/fleet/playbooks`.

| Field | Required | Description |
|---|---|---|
| `text` | yes | Raw runbook content |
| `format` | no | `yaml` \| `text` \| `markdown` \| `rundeck` \| `ansible` (default: `text`) |
| `hints.name` | no | Pre-filled name (used when LLM cannot extract one) |
| `hints.problem_class` | no | Pre-filled problem class |
| `hints.series_id` | no | Target series for the imported draft |

`format=yaml` uses a direct parse (no LLM, `confidence=1.0`). Other formats require LLM configuration. See [PLAYBOOKS.md — importing playbooks](PLAYBOOKS.md#importing-playbooks) for format details and the YAML schema.

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/import \
  -H "Content-Type: application/json" \
  -d '{"text": "<runbook yaml>", "format": "yaml"}' | jq '{draft: .draft.name, confidence: .confidence}'
```

---

### Operator file uploads

Operators upload files (typically PostgreSQL log files retrieved from a remote host) so that agents can analyse them when the database is unreachable. The `read_uploaded_file` database tool reads uploaded content by `upload_id`.

Uploads are stored in auditd's SQLite database, expire after **24 hours**, and are capped at **50 MB** per file.

#### `POST /api/v1/fleet/uploads`

Upload a file as `multipart/form-data` with a single `file` field. Returns `201 Created` with upload metadata.

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/uploads \
  -F "file=@/var/log/postgresql/postgresql-2024.log" | jq '{upload_id, filename, size, expires_at}'
```

Response:

```json
{
  "upload_id":   "ul_3f7a2b1c",
  "filename":    "postgresql-2024.log",
  "size":        94208,
  "uploaded_at": "2024-01-15T10:00:00Z",
  "expires_at":  "2024-01-16T10:00:00Z"
}
```

#### `GET /api/v1/fleet/uploads/{uploadID}`

Get upload metadata (no content). Returns `404` if not found or expired.

```bash
curl http://localhost:8080/api/v1/fleet/uploads/ul_3f7a2b1c | jq .
```

#### `GET /api/v1/fleet/uploads/{uploadID}/content`

Return the raw file content (`text/plain`). Returns `404` if not found or expired.

```bash
curl http://localhost:8080/api/v1/fleet/uploads/ul_3f7a2b1c
```

**Typical workflow for DB-down log analysis:**

```bash
# 1. Retrieve the log from the remote host however you can (scp, jump host, etc.)
scp ops@db-host:/var/log/postgresql/postgresql.log /tmp/pg.log

# 2. Upload it to aiHelpDesk
UPLOAD_ID=$(curl -s -X POST http://localhost:8080/api/v1/fleet/uploads \
  -F "file=@/tmp/pg.log" | jq -r .upload_id)

# 3. Ask the database agent to analyse it
curl -s -X POST http://localhost:8080/api/v1/query \
  -H "Content-Type: application/json" \
  -d "{\"agent\":\"database\",\"message\":\"Analyse the uploaded log (upload_id: $UPLOAD_ID). Look for FATAL errors and the likely root cause.\"}"
```

---

#### `GET /api/v1/tool-results`

Query the persistent tool result log — a record of every tool execution written by fleet-runner jobs and direct tool calls (when `HELPDESK_AUDIT_URL` is configured). Useful for post-incident triage and auditing what was collected during a job.

The database agent's `get_saved_snapshots` tool uses this endpoint internally to retrieve prior outputs during triage (e.g. recovering a `config_file` path when the DB is down, or diffing two baselines).

| Parameter | Description |
|---|---|
| `server` | Filter by server name |
| `tool` | Filter by tool name |
| `job_id` | Filter by fleet job ID (`flj_*`) |
| `since` | Duration string: `7d`, `24h`, `30m` (results newer than this window) |
| `limit` | Max results (default 100, max 1000) |

```bash
# Last 7 days of vacuum status results on prod-db-1
curl "http://localhost:8080/api/v1/tool-results?server=prod-db-1&tool=get_vacuum_status&since=7d"

# All results from a specific fleet job
curl "http://localhost:8080/api/v1/tool-results?job_id=flj_abc123"
```

Response:

```json
{
  "count": 3,
  "results": [
    {
      "result_id":    "res_a1b2c3d4",
      "server_name":  "prod-db-1",
      "tool_name":    "get_vacuum_status",
      "tool_args":    "{\"connection_string\":\"...\"}",
      "output":       "...",
      "job_id":       "flj_abc123",
      "trace_id":     "tr_flj_abc123",
      "recorded_by":  "fleet-runner",
      "recorded_at":  "2026-03-30T02:01:00Z",
      "success":      true
    }
  ]
}
```

Results are ordered most-recent first.

---

### Rollback endpoints (auditd direct)

Rollback endpoints are available directly on auditd (`http://localhost:1199`). The gateway does not proxy them. See [ROLLBACK.md](ROLLBACK.md) for full semantics.

#### `POST /v1/rollbacks`

Initiate a rollback for a prior mutation event. Returns `422` if the event is not reversible (includes `not_reversible_reason`). Returns `409` if an active rollback already exists for that event. Pass `"dry_run": true` to receive the plan without executing.

```bash
# Dry-run: inspect the inverse operation before committing
curl -s -X POST http://localhost:1199/v1/rollbacks \
  -H "Content-Type: application/json" \
  -d '{"original_event_id": "tool_abc12345", "dry_run": true}' | jq .

# Initiate (requires operator or admin role)
curl -s -X POST http://localhost:1199/v1/rollbacks \
  -H "Content-Type: application/json" \
  -d '{"original_event_id": "tool_abc12345", "justification": "scaled too far"}' | jq .
```

#### `GET /v1/rollbacks`

List all rollback records.

#### `GET /v1/rollbacks/{rollbackID}`

Get a rollback record including its derived `RollbackPlan` and current status.

#### `POST /v1/rollbacks/{rollbackID}/cancel`

Cancel a rollback that is in `pending_approval` status.

#### `POST /v1/events/{eventID}/rollback-plan`

Derive and return the rollback plan for an event without creating a rollback record. Returns the `RollbackPlan` with `reversibility`, `inverse_op`, and (if not reversible) `not_reversible_reason`. Read-only; no approval required.

```bash
curl -s -X POST http://localhost:1199/v1/events/tool_abc12345/rollback-plan | jq .
```

#### `POST /v1/fleet/jobs/{jobID}/rollback`

Initiate a fleet-level rollback — constructs a reverse job definition and submits it through the normal fleet approval pipeline. Accepts `scope` (`"all"`, `"canary_only"`, `"failed_only"`, or a JSON array of server names) and `dry_run`.

#### `GET /v1/fleet/jobs/{jobID}/rollback`

Get the status of an in-progress or completed fleet rollback.

---

### Approval endpoints

The approval write operations are only available here — the gateway does not proxy them.

#### `POST /v1/approvals`

Create an approval request. Agents call this automatically when a policy requires human sign-off; you would only call it directly to simulate or test the approval workflow.

**Body:**

| Field | Type | Required | Description |
|---|---|---|---|
| `action_class` | string | yes | `read`, `write`, or `destructive` |
| `requested_by` | string | yes | Identity of the requester |
| `event_id` | string | no | Associated audit event ID |
| `trace_id` | string | no | Trace ID for correlation |
| `tool_name` | string | no | Tool that triggered the request |
| `agent_name` | string | no | Agent that triggered the request |
| `resource_type` | string | no | `database` or `kubernetes` |
| `resource_name` | string | no | Resource name |
| `policy_name` | string | no | Policy that triggered the request |
| `approver_role` | string | no | Role required to approve |
| `expires_in_minutes` | int | no | Expiry window (default 60) |
| `callback_url` | string | no | URL auditd will POST to when resolved |
| `request_context` | object | no | Arbitrary key/value context |

Response (`201 Created`):
```json
{
  "approval_id": "apr_abc123",
  "status":      "pending",
  "expires_at":  "2024-01-15T13:00:00Z"
}
```

---

#### `GET /v1/approvals/pending`

List pending approvals (shorthand for `GET /v1/approvals?status=pending`).

```bash
curl http://localhost:1199/v1/approvals/pending
```

---

#### `GET /v1/approvals`

List approvals with optional filters (same parameters as the gateway proxy — `status`, `agent`, `trace_id`, `requested_by`, `limit`).

```bash
curl "http://localhost:1199/v1/approvals?status=pending"
```

---

#### `GET /v1/approvals/{approvalID}`

Retrieve a single approval request.

```bash
curl http://localhost:1199/v1/approvals/apr_abc123
```

---

#### `GET /v1/approvals/{approvalID}/wait`

Long-poll until the approval is resolved. Returns immediately if already resolved; otherwise blocks until resolved or the timeout elapses, then returns the current state.

| Parameter | Description |
|---|---|
| `timeout` | Go duration string, e.g. `30s`, `2m` (max `120s`, default `30s`) |

```bash
curl "http://localhost:1199/v1/approvals/apr_abc123/wait?timeout=60s"
```

Agents use this to block execution until a human has approved or denied their request.

---

#### `POST /v1/approvals/{approvalID}/approve`

Approve a pending request.

**Body:**

| Field | Type | Required | Description |
|---|---|---|---|
| `approved_by` | string | yes | Identity of the approver |
| `reason` | string | no | Free-text justification |
| `valid_for_minutes` | int | no | How long the approval is valid (for re-use within a session) |

```bash
curl -X POST http://localhost:1199/v1/approvals/apr_abc123/approve \
  -H "Content-Type: application/json" \
  -d '{"approved_by": "ops-team", "reason": "Verified safe to proceed"}'
```

Response: the updated approval object.

---

#### `POST /v1/approvals/{approvalID}/deny`

Deny a pending request.

**Body:**

| Field | Type | Required | Description |
|---|---|---|---|
| `denied_by` | string | yes | Identity of the denier |
| `reason` | string | no | Free-text justification |

```bash
curl -X POST http://localhost:1199/v1/approvals/apr_abc123/deny \
  -H "Content-Type: application/json" \
  -d '{"denied_by": "ops-team", "reason": "Use the read-only report instead"}'
```

Response: the updated approval object.

---

#### `POST /v1/approvals/{approvalID}/cancel`

Cancel a pending request (typically called by the requesting agent on timeout or abort).

**Body:**

| Field | Type | Required | Description |
|---|---|---|---|
| `cancelled_by` | string | no | Identity of the canceller (defaults to `"system"`) |
| `reason` | string | no | Free-text reason |

```bash
curl -X POST http://localhost:1199/v1/approvals/apr_abc123/cancel \
  -H "Content-Type: application/json" \
  -d '{"cancelled_by": "agent", "reason": "Request timed out"}'
```

---

### Governance info endpoints (direct)

These are the same endpoints that the gateway proxies under `/api/v1/governance/*`. Call them directly when you need to bypass the gateway or when the gateway is not running.

| Endpoint | Description |
|---|---|
| `GET /v1/governance/info` | Governance status (→ gateway `/api/v1/governance`) |
| `GET /v1/governance/policies` | Policy summary (→ gateway `/api/v1/governance/policies`) |
| `GET /v1/governance/explain` | Hypothetical policy check (→ gateway `/api/v1/governance/explain`) |

### Health

```bash
curl http://localhost:1199/health
# → {"status":"ok"}
```

---

## Agent A2A API (ports 1100–1106)

Each agent implements the [A2A (Agent-to-Agent) protocol](https://google.github.io/A2A/). You can call agents directly without going through the gateway — useful when building custom orchestrators, upstream agents, or integrations that want fine-grained control over task lifecycle.

| Agent | Port | Internal name |
|---|---|---|
| database-agent | `1100` | `postgres_database_agent` |
| k8s-agent | `1102` | `k8s_agent` |
| incident-agent | `1104` | `incident_agent` |
| research-agent | `1106` | `research_agent` |

### Discovery: Agent Card

Every A2A agent exposes its capabilities at a well-known URL. The card lists the agent's name, description, version, and skills (available tools).

```bash
curl http://localhost:1100/.well-known/agent.json | jq .
```

Example (abbreviated):
```json
{
  "name":        "postgres_database_agent",
  "description": "PostgreSQL diagnostics agent",
  "version":     "1.0.0",
  "url":         "http://localhost:1100",
  "skills": [
    { "id": "check_replication_lag", "name": "Check Replication Lag", ... },
    { "id": "run_query",             "name": "Run Query",             ... }
  ]
}
```

Fetch cards for all agents:
```bash
for port in 1100 1102 1104 1106; do
  echo "=== :$port ==="; curl -s http://localhost:$port/.well-known/agent.json | jq '.name,.skills[].id'
done
```

### Sending a task

A2A uses JSON-RPC 2.0 over HTTP POST to `/`. The primary method is `message/send`.

```bash
curl -s -X POST http://localhost:1100/ \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": "req-1",
    "method": "message/send",
    "params": {
      "message": {
        "role": "user",
        "parts": [{"type": "text", "text": "Check replication lag on prod-db"}]
      }
    }
  }' | jq .
```

Response: an A2A Task object with `id`, `status.state` (`completed`, `failed`, …), `status.message`, and `artifacts`.

The gateway's `/api/v1/query` and `/api/v1/db/{tool}` endpoints are thin wrappers around exactly this call — they construct the prompt text and call `message/send` on your behalf.

### A2A protocol reference

The full A2A specification — including streaming (`message/stream`), task state machine, artifact handling, and push notifications — is published at **[google.github.io/A2A](https://google.github.io/A2A/)**. The Go SDK used by aiHelpDesk is [github.com/a2aproject/a2a-go](https://github.com/a2aproject/a2a-go).

---

## Ports reference

| Port | Service | Protocol |
|---|---|---|
| `8080` | Gateway REST API | HTTP |
| `1199` | auditd (audit + approvals + governance) | HTTP |
| `1100` | database-agent (A2A) | HTTP |
| `1102` | k8s-agent (A2A) | HTTP |
| `1104` | incident-agent (A2A) | HTTP |
| `1106` | research-agent (A2A) | HTTP |
| `9091` | secbot (health/metrics) | HTTP |
