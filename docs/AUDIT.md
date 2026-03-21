# aiHelpDesk Audit System

This document covers the audit system in depth: event types, the hash chain,
all API endpoints, the `auditor` monitoring CLI, and environment variables. For
the broader governance architecture see [AIGOVERNANCE.md](AIGOVERNANCE.md). For
policy decision history and the `govexplain` CLI see [GOVEXPLAIN.md](GOVEXPLAIN.md).
For end-to-end request journeys see [JOURNEYS.md](JOURNEYS.md).

---

## 1. Overview

The audit system is a tamper-evident, hash-chained log of every significant
action taken by aiHelpDesk agents. Every tool execution, policy decision,
delegation, and gateway request produces an audit event. Events are stored in
`auditd`, an independent service, so that a compromised agent cannot erase its
own footprint.

```
 database agent :1100  ────┐
 k8s agent      :1102  ────┤  HTTP POST /v1/events      ┌─────────────────┐
 incident agent :1104  ────┤──────────────────────────► │auditd :1199     │
 orchestrator          ────┤                            │• Hash chain     │
 gateway        :8080  ────┘                            │ • SQLite / PG   │
                                                        │ • Approval API  │
                            ┌──────────────────────────►│ • Governance API│
                            │  Unix socket (real-time)  └────────┬────────┘
                            │  notifications                     │
                     ┌──────┴──────┐                             │
                     │  auditor    │                  audit.db (SQLite)
                     │  (CLI)      │                  or postgres://...
                     └─────────────┘
```

---

## 2. The Three Audit IDs

Every event carries three independent identifiers. Understanding them is
essential for querying and correlating events.

| ID | Scope | Prefix examples | Assigned by |
|----|-------|-----------------|-------------|
| `event_id` | One record | `evt_`, `tool_`, `pol_`, `rsn_` | auditd (at record time) |
| `session_id` | One process lifetime | `sess_`, `dbagent_`, `k8sagent_` | Each component on startup |
| `trace_id` | One user request end-to-end | `tr_`, `dt_` | Orchestrator or gateway |

- **`session_id → trace_id`** is 1:M — one agent process handles many requests.
- **`trace_id → event_id`** is 1:M — one request produces many audit records.
- Events without a `trace_id` came from direct A2A calls or gateway direct
  tool calls that predate trace propagation (see [JOURNEYS.md](JOURNEYS.md)).

### 2.1 event_id prefix → event type

| Prefix | Event type | Recorded by |
|--------|-----------|-------------|
| `evt_` | `delegation_decision` | Orchestrator — routes a request to an agent |
| `evt_` | `gateway_request` | Gateway — records every inbound request; anchor for NL-query journeys |
| `tool_` | `tool_execution` | Agent — records tool name, params, result, duration |
| `pol_` | `policy_decision` | Agent / auditd — records policy evaluation outcome |
| `rsn_` | `agent_reasoning` | Agent — LLM deliberation text captured automatically when audit is enabled and the model emits text alongside a tool call |
| `dv_` | `delegation_verification` | Orchestrator — records what a sub-agent actually executed vs. what it claimed; used to detect LLM fabrication |

### 2.2 trace_id prefix → request origin

| Prefix | Origin |
|--------|--------|
| `tr_` | Natural-language query via `POST /api/v1/query` (orchestrator-routed) |
| `tr_flj_` | Fleet job — `tr_` + job ID (e.g. `tr_flj_4dd009b7`); one trace per job |
| `dt_` | Direct tool call via `POST /api/v1/db/{tool}` or `/api/v1/k8s/{tool}` (not a journey) |

---

## 3. Hash Chain Integrity

Each event is linked to its predecessor by a SHA-256 hash chain:

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  Event 1     │     │  Event 2     │     │  Event 3     │
│ prev_hash:   │     │ prev_hash:   │     │ prev_hash:   │
│  "genesis"   │────►│  SHA256(E1)  │────►│  SHA256(E2)  │
│ event_hash:  │     │ event_hash:  │     │ event_hash:  │
│  SHA256(E1)  │     │  SHA256(E2)  │     │  SHA256(E3)  │
└──────────────┘     └──────────────┘     └──────────────┘
```

- `event_hash` = SHA-256 of the event's canonical JSON representation
- `prev_hash` = `event_hash` of the immediately preceding event (`"genesis"` for event 1)

Any modification to a stored event breaks the chain at that point and at every
subsequent event. `GET /v1/verify` reports the first broken link.

---

## 4. Event Schema

Core fields present on every event:

| Field | Description |
|-------|-------------|
| `event_id` | Unique identifier (e.g. `tool_a1b2c3d4`) |
| `timestamp` | UTC timestamp (RFC3339Nano) |
| `event_type` | `delegation_decision`, `gateway_request`, `tool_execution`, `policy_decision`, `agent_reasoning`, `delegation_verification`, `governance_violation` |
| `session_id` | Session identifier of the recording component |
| `trace_id` | End-to-end correlation ID; empty when no orchestrator context |
| `origin` | Dispatch path that produced the event: `"direct_tool"` (fleet-runner structured dispatch via `POST /tool/{name}`), `"agent"` (LLM/A2A path), or `"gateway"` (gateway-originated request). Set on `tool_execution` and `tool_invoked` events; absent on delegation and reasoning events. See [§4.5](#45-origin-values). |
| `agent` | Name of the agent that recorded the event |
| `prev_hash` | SHA-256 of the previous event in the chain |
| `event_hash` | SHA-256 of this event's canonical JSON |

### 4.1 tool_execution fields

| Field | Description |
|-------|-------------|
| `tool_name` | Tool that executed (e.g. `run_sql`, `delete_pod`) |
| `action_class` | `read`, `write`, or `destructive` |
| `outcome_status` | `success` or `error` |
| `outcome_error` | Error message if the tool failed |
| `duration_ms` | Execution time in milliseconds |

### 4.2 policy_decision fields

| Field | Description |
|-------|-------------|
| `resource_type` | `database` or `kubernetes` |
| `resource_name` | Resource identifier |
| `action` | `read`, `write`, or `destructive` |
| `tags` | Tags resolved from infra config |
| `effect` | `allow`, `deny`, or `require_approval` |
| `policy_name` | Name of the matched policy (or `default`) |
| `message` | Human-readable reason from the matched rule |
| `explanation` | Full decision trace in human-readable form |
| `dry_run` | `true` if enforcement was in dry-run mode |
| `post_execution` | `true` if this was a blast-radius post-execution check |

### 4.3 delegation_decision fields (orchestrator)

| Field | Description |
|-------|-------------|
| `user_id` | User who sent the original request |
| `user_query` | Original natural-language query text |
| `decision_agent` | Agent the orchestrator selected |

### 4.4 delegation_verification fields (orchestrator)

Emitted by the orchestrator after every `delegate_to_agent` call completes.
The orchestrator queries the audit trail independently of the agent's text
response — this is the authoritative record of what the sub-agent actually did.

| Field | Description |
|-------|-------------|
| `delegation_event_id` | `event_id` of the corresponding `delegation_decision` event |
| `agent` | Name of the sub-agent that was delegated to |
| `tools_confirmed` | Tool names that appear in the audit trail for this trace since the delegation started |
| `destructive_confirmed` | Subset of `tools_confirmed` that are classified as `destructive` |
| `mismatch` | `true` when the delegation was classified as `destructive` but no destructive tool execution is in the trail — strong signal of LLM fabrication |

When `mismatch=true`, the journey outcome is elevated to `unverified_claim` (see
[§5](#5-action-classification) and [JOURNEYS.md — Outcomes](JOURNEYS.md#journey-outcomes)).
The orchestrator prompt instructs the LLM to report mismatches to the user and
**not** claim success.

`delegation_verification` events do **not** contribute to `tools_used` or
`event_count` in journey summaries — they are internal governance plumbing.

### 4.5 origin values

The `origin` field records *how* a tool was invoked. It is set on
`tool_execution` and `tool_invoked` events and is absent on delegation,
reasoning, and policy events.

| Value | When set | Trace prefix |
|-------|----------|--------------|
| `"direct_tool"` | Fleet-runner dispatched the tool via `POST /tool/{name}` on the agent — no LLM involvement | `dt_` |
| `"agent"` | Gateway routed an NL query to the agent via A2A; the agent's LLM selected and invoked the tool | `tr_` |
| `"gateway"` | Gateway itself generated the event (e.g. `gateway_request` anchor events) | `tr_`, `dt_` |

**Why it matters:** filtering by `origin` lets you isolate structured,
deterministic fleet operations (`direct_tool`) from LLM-mediated interactions
(`agent`) — useful for compliance reporting, anomaly detection, and auditing
the LLM's decision-making independently of automated jobs.

```bash
# All tool executions that went through the LLM
curl "http://localhost:1199/v1/events?event_type=tool_execution&origin=agent"

# All tool executions from fleet-runner structured dispatch
curl "http://localhost:1199/v1/events?event_type=tool_execution&origin=direct_tool"

# Direct-tool events for a specific trace
curl "http://localhost:1199/v1/events?trace_id=dt_abc12345&origin=direct_tool"
```

---

## 5. Action Classification

| Class | Policy pre-check | Post-execution blast-radius check | Typical tools |
|-------|-----------------|----------------------------------|---------------|
| `read` | Optional | No | `get_pods`, `run_sql` (SELECT), `get_active_connections` |
| `write` | Yes | Yes (`max_rows_affected`) | `cancel_query`, `create_incident_bundle` |
| `destructive` | Yes (may require approval) | Yes (`max_rows_affected`, `max_pods_affected`) | `terminate_connection`, `delete_pod`, `restart_deployment`, `scale_deployment` |

---

## 6. auditd API Reference

Base URL: `http://localhost:1199` (default). All paths are under `/v1/`.

### 6.1 Audit events

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/v1/events` | Record a new audit event (called by agents) |
| `POST` | `/v1/events/{eventID}/outcome` | Attach an outcome to an existing event |
| `GET` | `/v1/events` | Query events with filters (see below) |
| `GET` | `/v1/events/{eventID}` | Retrieve a single event by ID |
| `GET` | `/v1/verify` | Verify hash chain integrity |

### 6.2 Journey summaries

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/v1/journeys` | List journey summaries (one per trace_id); see [JOURNEYS.md](JOURNEYS.md) |

### 6.3 Approvals

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/v1/approvals` | Create an approval request (called by agent) |
| `GET` | `/v1/approvals` | List all approval requests |
| `GET` | `/v1/approvals/pending` | List only pending requests |
| `GET` | `/v1/approvals/{id}` | Retrieve a specific approval |
| `GET` | `/v1/approvals/{id}/wait` | Long-poll until decision (used by agent) |
| `POST` | `/v1/approvals/{id}/approve` | Approve a request |
| `POST` | `/v1/approvals/{id}/deny` | Deny a request |
| `POST` | `/v1/approvals/{id}/cancel` | Cancel a pending request |

### 6.4 Governance

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/v1/governance/info` | Audit stats, backend, chain validity |
| `GET` | `/v1/governance/policies` | Policy summary (requires policy engine) |
| `GET` | `/v1/governance/explain` | Hypothetical policy check (requires policy engine) |
| `POST` | `/v1/governance/check` | Evaluate + record a policy decision atomically |

### 6.5 Fleet jobs

Fleet job records live in three additive tables alongside the main audit event
chain. They are written by `fleet-runner` via the gateway; the audit event
chain records the individual tool calls as normal `gateway_request` events.

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/v1/fleet/jobs` | Register a new fleet job |
| `GET` | `/v1/fleet/jobs` | List fleet jobs (filterable by status, submitted_by) |
| `GET` | `/v1/fleet/jobs/{jobID}` | Get a fleet job record |
| `PATCH` | `/v1/fleet/jobs/{jobID}` | Update fleet job status |
| `POST` | `/v1/fleet/jobs/{jobID}/servers` | Add a server record to a job |
| `PATCH` | `/v1/fleet/jobs/{jobID}/servers/{serverName}` | Update per-server status and output |
| `GET` | `/v1/fleet/jobs/{jobID}/servers` | List all per-server records for a job |
| `POST` | `/v1/fleet/jobs/{jobID}/servers/{serverName}/steps` | Add a step record |
| `PATCH` | `/v1/fleet/jobs/{jobID}/servers/{serverName}/steps/{stepIndex}` | Update step status |
| `GET` | `/v1/fleet/jobs/{jobID}/servers/{serverName}/steps` | List per-step records |
| `POST` | `/v1/fleet/jobs/{jobID}/approval` | Create an approval request for a fleet job |
| `GET` | `/v1/fleet/jobs/{jobID}/approval/{approvalID}` | Poll approval status |

**Fleet job status values:** `pending` → `running` → `success` / `failed` / `cancelled`

**Per-server status values:** `pending` → `running` → `success` / `failed` / `partial`
(`partial` means some steps failed with `on_failure: continue` but no stop-failure occurred)

**Per-step status values:** `pending` → `success` / `failed`

Fleet job records are **not** part of the audit event hash chain — they are
operational records managed separately. The tool calls themselves (the actual
work) appear as `gateway_request` audit events with `purpose=fleet_rollout`,
`purpose_note=job_id=<id> server=<name> stage=<stage>`, and the fleet-runner's
service account as the principal. This lets you correlate fleet job activity in
the standard audit trail:

```bash
# All tool calls from a specific fleet job
curl -s 'http://localhost:1199/v1/events?event_type=gateway_request' | \
  jq '[.[] | select(.purpose_note | contains("job_id=flj_4dd009b7"))]'

# All fleet_rollout activity (any job)
curl -s 'http://localhost:1199/v1/journeys?purpose=fleet_rollout' | jq .
```

### 6.6 Health

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health` | Returns `{"status":"ok"}` |

---

## 7. Event Query Filters

`GET /v1/events` accepts the following query parameters:

| Parameter | Type | Description |
|-----------|------|-------------|
| `session_id` | string | Filter by agent session ID |
| `trace_id` | string | Filter by exact trace ID |
| `trace_id_prefix` | string | Filter by trace ID prefix (e.g. `tr_`, `dt_`) |
| `event_type` | string | `delegation_decision`, `gateway_request`, `tool_execution`, `policy_decision`, `agent_reasoning`, `delegation_verification`, `governance_violation` |
| `agent` | string | Filter by agent name |
| `action_class` | string | `read`, `write`, or `destructive` |
| `tool_name` | string | Filter by tool name (e.g. `terminate_connection`) |
| `outcome_status` | string | Filter by outcome (e.g. `success`, `error`, `denied`) |
| `origin` | string | Filter by dispatch path: `direct_tool`, `agent`, or `gateway` (see [§4.5](#45-origin-values)) |
| `since` | RFC3339 | Only events at or after this timestamp |
| `limit` | int | Maximum events to return (default: 100) |

```bash
# All events for a specific user request
curl "http://localhost:1199/v1/events?trace_id=tr_7c2a1b9e"

# Recent policy denials
curl "http://localhost:1199/v1/events?event_type=policy_decision&since=2026-03-01T00:00:00Z"

# All destructive tool calls from the database agent
curl "http://localhost:1199/v1/events?agent=postgres_database_agent&action_class=destructive"

# All delegation verifications — reveals mismatches (LLM fabrication incidents)
curl "http://localhost:1199/v1/events?event_type=delegation_verification"

# Verify chain integrity
curl "http://localhost:1199/v1/verify" | jq
```

---

## 8. Starting auditd

```bash
# Minimal — SQLite, default listen :1199
go run ./cmd/auditd/ -db /var/lib/helpdesk/audit.db

# With approval notifications via Slack
HELPDESK_APPROVAL_WEBHOOK=https://hooks.slack.com/services/... \
  go run ./cmd/auditd/ -db /var/lib/helpdesk/audit.db

# With email approval notifications
go run ./cmd/auditd/ \
  -db /var/lib/helpdesk/audit.db \
  -smtp-host mail.example.com \
  -email-from helpdesk@example.com \
  -email-to ops@example.com \
  -approval-base-url http://auditd.internal:1199
```

### 8.1 auditd environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HELPDESK_AUDIT_ADDR` | `:1199` | HTTP listen address |
| `HELPDESK_AUDIT_DB` | `audit.db` | SQLite database file path (or postgres:// DSN) |
| `HELPDESK_AUDIT_SOCKET` | `/tmp/helpdesk-audit.sock` | Unix socket for real-time notifications |
| `HELPDESK_APPROVAL_WEBHOOK` | — | Slack/webhook URL for approval notifications |
| `HELPDESK_APPROVAL_BASE_URL` | — | Base URL embedded in approve/deny email links |
| `HELPDESK_EMAIL_FROM` | — | Sender address for approval emails |
| `HELPDESK_EMAIL_TO` | — | Comma-separated approval email recipients |
| `SMTP_HOST` | — | SMTP server for approval emails |
| `SMTP_PORT` | `587` | SMTP port |
| `SMTP_USER` | — | SMTP username |
| `SMTP_PASSWORD` | — | SMTP password |

### 8.2 Agent environment variables

| Variable | Description |
|----------|-------------|
| `HELPDESK_AUDIT_URL` | URL of the auditd service (e.g. `http://localhost:1199`) |
| `HELPDESK_AUDIT_ENABLED` | Set to `true` to enable audit recording (required in `fix` mode) |

---

## 9. auditor CLI

The `auditor` subscribes to the auditd Unix socket and provides real-time
monitoring, security alerting, and chain verification.

```bash
# Alerts only (default) — denials, anomalies, chain breaks
go run ./cmd/auditor/ --socket /tmp/helpdesk-audit.sock

# All events
go run ./cmd/auditor/ --socket /tmp/helpdesk-audit.sock --log-all

# Periodic chain verification against auditd
go run ./cmd/auditor/ \
  --socket /tmp/helpdesk-audit.sock \
  --audit-service http://localhost:1199 \
  --verify-interval 5m

# Verify chain integrity and exit (useful for CI / cron)
go run ./cmd/auditor/ --verify --db /var/lib/helpdesk/audit.db

# Prometheus metrics
go run ./cmd/auditor/ --socket /tmp/helpdesk-audit.sock --prometheus :9090
```

### 9.1 auditor flags

| Flag | Default | Description |
|------|---------|-------------|
| `--socket PATH` | `audit.sock` | Unix socket from auditd |
| `--log-all` | false | Log all events, not just alerts |
| `--json` | false | Output events as JSON lines |
| `--verify` | false | Verify chain integrity and exit (uses `--db`) |
| `--db PATH` | `audit.db` | Database path for `--verify` mode |
| `--audit-service URL` | — | auditd URL for periodic chain verification |
| `--verify-interval DURATION` | `0` (disabled) | How often to verify chain (e.g. `5m`, `1h`) |
| `--webhook URL` | — | Webhook for alerts (Slack, PagerDuty, etc.) |
| `--webhook-all` | false | Send all events to webhook, not just alerts |
| `--webhook-test` | false | Send a test alert on startup |
| `--incident-webhook URL` | — | URL to POST security incidents for automated response |
| `--max-events-per-minute N` | `0` (disabled) | Alert on high event volume |
| `--allowed-hours-start N` | `-1` (disabled) | Start of allowed hours (0–23) |
| `--allowed-hours-end N` | `-1` (disabled) | End of allowed hours (0–23) |
| `--prometheus ADDR` | — | Expose Prometheus metrics (e.g. `:9090`) |
| `--syslog` | false | Send alerts to syslog (Linux only) |
| `--smtp-host HOST` | — | SMTP server for email alerts |
| `--email-from ADDR` | — | Email sender |
| `--email-to ADDRS` | — | Comma-separated email recipients |
| `--email-test` | false | Send a test email on startup |

### 9.2 Security detection patterns

| Pattern | Trigger |
|---------|---------|
| High volume | More than `--max-events-per-minute` events in a rolling window |
| Off-hours | Events outside `--allowed-hours-start` to `--allowed-hours-end` |
| Hash mismatch | Event hash does not match content |
| Unauthorized destructive | `destructive` action without approved status |
| Potential SQL injection | SQL syntax errors in tool output |
| Potential command injection | Permission denied / command not found in tool output |

---

## 10. Chain Verification

### 10.1 Via API

```bash
curl -s http://localhost:1199/v1/verify | jq
```

```json
{
  "valid": true,
  "total_events": 247,
  "checked_at": "2026-03-01T09:30:00Z"
}
```

When the chain is broken:

```json
{
  "valid": false,
  "total_events": 247,
  "first_invalid_id": 183,
  "checked_at": "2026-03-01T09:30:00Z"
}
```

### 10.2 Via auditor (one-shot)

```bash
go run ./cmd/auditor/ --verify --db /var/lib/helpdesk/audit.db
```

Exit code `0` = chain valid; non-zero = chain broken or error.

### 10.3 Via SQL

```sql
-- Find broken links directly
SELECT
  e1.event_id,
  e1.prev_hash,
  e2.event_hash  AS expected_prev,
  CASE WHEN e1.prev_hash = e2.event_hash THEN 'OK' ELSE 'BROKEN' END AS status
FROM audit_events e1
LEFT JOIN audit_events e2 ON e1.id = e2.id + 1
WHERE e1.id > 1
  AND e1.prev_hash != COALESCE(e2.event_hash, 'genesis')
ORDER BY e1.id;
```

---

## 11. Troubleshooting

### 11.1 Events not appearing in auditd

1. Confirm auditd is reachable: `curl http://localhost:1199/health`
2. Confirm `HELPDESK_AUDIT_URL` is set on the agent and `HELPDESK_AUDIT_ENABLED=true`
3. Check auditd logs for HTTP errors from agent connections

### 11.2 auditor not receiving events

1. Confirm `--socket` path matches `HELPDESK_AUDIT_SOCKET` in auditd
2. Check the socket file exists: `ls -la /tmp/helpdesk-audit.sock`
3. The auditor must connect before events are emitted — events are not replayed
   to late subscribers

### 11.3 Chain integrity failure

1. Find the first broken event with `GET /v1/verify`
2. Possible causes: direct database modification (tampering), database
   corruption, or a write race condition during a crash
3. If tampering is suspected, treat the log as potentially compromised and
   preserve a copy before any remediation

### 11.4 Events with empty trace_id

Direct tool calls via `POST /api/v1/db/{tool}` and direct A2A calls to agents
without `trace_id` in message metadata produce events with no `trace_id`. These
appear in `GET /v1/events` but not in `GET /v1/journeys`. See
[JOURNEYS.md — Journey Coverage](JOURNEYS.md#journey-coverage) for details.
