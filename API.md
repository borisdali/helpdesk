# aiHelpDesk Gateway API Reference

The gateway exposes a REST API on port `8080` (default). All responses are `application/json`.

All agent-backed endpoints proxy through the [A2A protocol](https://google.github.io/A2A/) to the relevant sub-agent and return a unified response envelope. Governance endpoints are thin proxies to `auditd` on port `1199`.

## Base URL

```
http://localhost:8080
```

---

## Response envelope

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

Error responses:

```json
{ "error": "agent \"foo\" not available" }
```

The response header `X-Trace-ID` is always set on agent calls. Pass it in requests to force a specific trace ID for end-to-end correlation across gateway and agent audit logs.

---

## Core endpoints

### `GET /api/v1/agents`

List all registered agents and their A2A metadata.

```bash
curl http://localhost:8080/api/v1/agents
```

Response: array of agent objects with `name`, `invoke_url`, `description`, `version`, `skills`.

---

### `POST /api/v1/query`

Send a natural-language question to an agent.

**Body:**

| Field | Type | Required | Description |
|---|---|---|---|
| `agent` | string | yes | `database`, `db`, `k8s`, or `incident` |
| `message` | string | yes | The question or instruction |

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

Invoke a specific database agent tool directly by name. The body is a JSON object of tool parameters.

```bash
# Check replication lag directly via tool call
curl -s -X POST http://localhost:8080/api/v1/db/check_replication_lag \
  -H "Content-Type: application/json" \
  -d '{"host": "prod-db.example.com", "port": 5432}'

# Run a query
curl -s -X POST http://localhost:8080/api/v1/db/run_query \
  -H "Content-Type: application/json" \
  -d '{"host": "prod-db.example.com", "query": "SELECT count(*) FROM pg_stat_activity"}'
```

Use `GET /api/v1/agents` to discover which tools each agent exposes via its skills list.

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

Run a web research query (uses the research agent).

**Body:**

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

Response when configured:
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

## Governance endpoints

All governance endpoints require `auditd` to be running and `HELPDESK_AUDIT_URL` to be set. When not configured, they return:

```json
{ "enabled": false, "message": "Governance service not configured. Set HELPDESK_AUDIT_URL to enable." }
```

Query parameters are forwarded verbatim to `auditd`.

---

### `GET /api/v1/governance`

Governance system status: audit chain health, policy config summary, pending approval count.

```bash
curl http://localhost:8080/api/v1/governance
```

Response fields: `policy` (enabled, file, policies_count, rules_count), `approvals` (pending_count, webhook_configured, email_configured), `audit` (events_total, chain_valid, last_event_at), `timestamp`.

---

### `GET /api/v1/governance/policies`

Active policy rules in human-readable form.

```bash
curl http://localhost:8080/api/v1/governance/policies
```

---

### `GET /api/v1/governance/explain`

Hypothetical policy check: what would happen if an agent tried this action right now? Does not record an audit event or execute any tool.

**Query parameters:**

| Parameter | Required | Description |
|---|---|---|
| `resource_type` | yes | `database` or `kubernetes` |
| `resource_name` | yes | Resource name (e.g. `prod-db`, `default`) |
| `action` | yes | `read`, `write`, or `destructive` |
| `tags` | no | Comma-separated tags, e.g. `production,critical`. Auto-resolved from infra config when omitted. |
| `user_id` | no | Evaluate as a specific user |
| `role` | no | Evaluate with a specific role |

```bash
# Would a write to prod-db be allowed?
curl "http://localhost:8080/api/v1/governance/explain?resource_type=database&resource_name=prod-db&action=write"

# With explicit tags
curl "http://localhost:8080/api/v1/governance/explain?resource_type=database&resource_name=prod-db&action=destructive&tags=production,critical"
```

---

### `GET /api/v1/governance/events`

Query the audit event trail. Returns up to 100 events by default.

**Query parameters:**

| Parameter | Description |
|---|---|
| `session_id` | Filter by session ID |
| `trace_id` | Filter by trace ID (end-to-end correlation) |
| `event_type` | Filter by event type |
| `agent` | Filter by agent name |
| `action_class` | Filter by action class (`read`, `write`, `destructive`) |
| `since` | RFC3339 timestamp lower bound, e.g. `2024-01-15T00:00:00Z` |

```bash
# Recent events from the database agent
curl "http://localhost:8080/api/v1/governance/events?agent=postgres_database_agent"

# Destructive actions since a timestamp
curl "http://localhost:8080/api/v1/governance/events?action_class=destructive&since=2024-01-15T00:00:00Z"

# All events for a trace
curl "http://localhost:8080/api/v1/governance/events?trace_id=trace_abc123"
```

---

### `GET /api/v1/governance/events/{eventID}`

Retrieve a single audit event by ID. Includes `policy_decision.trace` and `policy_decision.explanation` when present.

```bash
curl http://localhost:8080/api/v1/governance/events/tool_a1b2c3d4
```

---

### `GET /api/v1/governance/approvals/pending`

List all approvals currently waiting for a human decision.

```bash
curl http://localhost:8080/api/v1/governance/approvals/pending
```

---

### `GET /api/v1/governance/approvals`

List all approvals (any status). Supports filtering.

**Query parameters:**

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

---

### `GET /api/v1/governance/verify`

Verify audit chain integrity (hash chain over all events).

```bash
curl http://localhost:8080/api/v1/governance/verify
```

Response:
```json
{
  "valid":        true,
  "total_events": 142,
  "checked_at":   "2024-01-15T12:00:00Z"
}
```

---

## Approval actions (direct to auditd)

The gateway does not expose approval write endpoints — approve/deny calls go directly to `auditd` on port `1199`. Use the `approvals` CLI or the auditd HTTP API:

```bash
# Approve
curl -X POST http://localhost:1199/v1/approvals/apr_abc123/approve \
  -H "Content-Type: application/json" \
  -d '{"approved_by": "ops-team", "reason": "Verified safe to proceed"}'

# Deny
curl -X POST http://localhost:1199/v1/approvals/apr_abc123/deny \
  -H "Content-Type: application/json" \
  -d '{"denied_by": "ops-team", "reason": "Not justified"}'
```

Or use the `approvals` CLI:

```bash
./approvals pending
./approvals approve apr_abc123 --reason "Verified by ops team"
./approvals deny apr_abc123 --reason "Use read-only report instead"
```

---

## Ports reference

| Port | Service | Protocol |
|---|---|---|
| `8080` | Gateway REST API | HTTP |
| `1199` | auditd (audit + approvals) | HTTP |
| `1100` | database-agent (A2A) | HTTP |
| `1102` | k8s-agent (A2A) | HTTP |
| `1104` | incident-agent (A2A) | HTTP |
| `1106` | research-agent (A2A) | HTTP |
| `9091` | secbot metrics | HTTP |
