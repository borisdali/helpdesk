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
  "agent":     "postgres_database_agent",
  "task_id":   "task-abc123",
  "state":     "completed",
  "text":      "Replication lag is 0 ms on all replicas.",
  "artifacts": []
}
```

Error responses: `{ "error": "<reason>" }`

The response header `X-Trace-ID` is set on every agent call. Pass it in the request to pin a specific trace ID for end-to-end correlation across gateway and agent audit logs.

### HTTP status codes

| Status | Meaning |
|---|---|
| `200 OK` | Agent task completed and the response text is the agent's output |
| `400 Bad Request` | Malformed request (missing required fields, invalid JSON, or unknown tool name) |
| `401 Unauthorized` | Authentication failed (bad or missing API key / JWT) |
| `403 Forbidden` | A governance policy denied the operation. The response body contains the full policy denial detail. The audit record status is `denied`. |
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

### `POST /api/v1/query`

Send a natural-language question to an agent.

| Field | Type | Required | Description |
|---|---|---|---|
| `agent` | string | yes | `database`, `db`, `k8s`, or `incident` |
| `message` | string | yes | The question or instruction (`query` is accepted as an alias) |

```bash
curl -s -X POST http://localhost:8080/api/v1/query \
  -H "Content-Type: application/json" \
  -d '{"agent": "database", "message": "Check replication lag on prod-db"}'
```

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

---

### `POST /api/v1/k8s/{tool}`

Invoke a specific Kubernetes agent tool directly by name.

```bash
curl -s -X POST http://localhost:8080/api/v1/k8s/get_pod_logs \
  -H "Content-Type: application/json" \
  -d '{"namespace": "default", "pod": "my-pod-abc123"}'
```

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
