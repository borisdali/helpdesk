# aiHelpDesk User Journeys: Audit Trail Across a Request

This document covers the `GET /v1/journeys` endpoint and the concept of a
journey in the aiHelpDesk [audit model](AUDIT.md). For the broader
governance architecture see [AIGOVERNANCE.md](AIGOVERNANCE.md).
For policy decision history see [GOVEXPLAIN.md](GOVEXPLAIN.md).

---

## 1. What is an aiHelpDesk Journey?

A **journey** is everything that happened as a result of one user request —
all the audit events that share the same `trace_id`.

A single natural-language query like _"show me slow queries on my-db postgres database"_
typically produces:

- one `delegation_decision` event (orchestrator routes the request to an agent)
- one or more `tool_call` events (the agent executes tools)
- zero or more `policy_decision` events (policy checks on each tool)
- zero or more `agent_reasoning` events (LLM deliberation text captured automatically
  when audit is enabled — see below)

`GET /v1/journeys` groups these into one summary per trace, ordered
newest-first, so you can see all recent activity at a glance and then drill
into any trace for the full event-by-event breakdown.

### 1.1 Agent reasoning events

`agent_reasoning` events (`rsn_` prefix) capture the LLM's deliberation text —
the model's "thinking out loud" before it calls a tool. They are recorded
automatically by every agent whenever **both** of these are true:

1. Auditing is enabled (`HELPDESK_AUDIT_ENABLED=true` + `HELPDESK_AUDIT_URL` set)
2. The model emits text **and** a function call in the same response turn

There is no separate flag. If auditing is on, reasoning is captured. If the
model returns a pure function call with no preceding text, nothing is recorded
for that turn (there is no deliberation to capture).

```bash
# Retrieve all reasoning events for a specific journey
curl "http://localhost:1199/v1/events?trace_id=tr_7c2a1b9e&event_type=agent_reasoning"
```

---

## 2. The Three Audit IDs

| ID | Scope | Prefix | Example |
|----|-------|--------|---------|
| `event_id` | One record | `evt_`, `tool_`, `pol_`, `rsn_` | `tool_a1b2c3d4` |
| `session_id` | One process lifetime | `sess_`, `dbagent_`, `k8sagent_` | `dbagent_9f3e` |
| `trace_id` | One user request end-to-end | `tr_`, `dt_` | `tr_7c2a1b9e` |

- `session_id → trace_id` is 1:M — one agent session handles many user requests.
- `trace_id → event_id` is 1:M — one user request produces many audit records.
- `trace_id` is what uniquely identifies a journey.

### 2.1 Trace ID prefixes

| Prefix | Origin |
|--------|--------|
| `tr_` | Natural-language query via `POST /api/v1/query` (orchestrator-routed) |
| `tr_flj_` | Fleet job — `tr_` + job ID (e.g. `tr_flj_4dd009b7`); one trace per job |
| `dt_` | Direct tool call via `POST /api/v1/db/{tool}` or `/api/v1/k8s/{tool}` (not a journey) |

Events without a `trace_id` are agent invocations that predate trace propagation
or were made by scripts that bypassed the Gateway entirely.

---

## 3. Architecture

```
User → Gateway :8080                  auditd :1199
         │                                │
         ├── POST /api/v1/query ──────────────────── orchestrator
         │   (NL query)              tr_ trace_id     │
         │                                │           └── agent (trace propagated)
         │                                │               ├── tool_call events
         │                                │               └── pol_* events
         │
         ├── POST /api/v1/db/{tool} ─────────────────── agent (trace propagated)
         │   (direct tool call)      dt_ trace_id        ├── tool_call events
         │                                │              └── pol_* events
         │                                │
         └── GET /api/v1/governance/journeys ──────────► GET /v1/journeys
             (read journeys)                             returns []JourneySummary
```

The Gateway proxies all governance reads under `/api/v1/governance/...`.
The raw endpoint at `/v1/journeys` is served directly by auditd.

---

## 4. Accessing the API by Deployment Type

The journey and events endpoints are served by **auditd** (port 1199) and
proxied by the **Gateway** (port 8080). Which address you use depends on your
deployment.

### 4.1 Docker Compose / local binary

Both ports are available on localhost. Use either directly:

```bash
# Via auditd (direct)
curl "http://localhost:1199/v1/journeys"

# Via Gateway (proxy)
curl "http://localhost:8080/api/v1/governance/journeys"
```

### 4.2 Kubernetes

Neither port is reachable from outside the cluster. Use **`kubectl port-forward`**
to open a local tunnel, then run the same `curl` commands against localhost.

**Recommended: port-forward the Gateway (single tunnel, all endpoints)**

```bash
kubectl port-forward -n helpdesk-system \
  svc/helpdesk-gateway 8080:8080

# Now in another terminal:
curl "http://localhost:8080/api/v1/governance/journeys?user=alice&limit=10"
curl "http://localhost:8080/api/v1/governance/journeys?limit=200" \
  | jq 'group_by(.user_id) | map({user: .[0].user_id, journeys: length}) | sort_by(-.journeys)'
```

**Alternative: port-forward auditd directly (raw endpoints)**

```bash
kubectl port-forward -n helpdesk-system \
  svc/helpdesk-auditd 1199:1199

# Now in another terminal:
curl "http://localhost:1199/v1/journeys?user=alice&limit=10"
curl "http://localhost:1199/v1/events?trace_id=tr_7c2a1b9e"
```

The Gateway path (`/api/v1/governance/journeys`) and the auditd path
(`/v1/journeys`) return identical data — the Gateway simply proxies the
request.

---

## 5. Endpoint

### 5.1 `GET /v1/journeys` (auditd)
### 5.2 `GET /api/v1/governance/journeys` (Gateway proxy)

Returns an array of journey summaries, newest first.

### 5.3 Query parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `user` | string | Filter to journeys initiated by this user ID |
| `purpose` | string | Filter by declared purpose (e.g. `fleet_rollout`, `remediation`, `emergency`) |
| `from` | RFC3339 | Only journeys whose anchor event is at or after this time |
| `until` | RFC3339 | Only journeys whose anchor event is before this time |
| `since` | duration | Shorthand for `from=now-duration` (e.g. `since=24h`, `since=7d`) |
| `limit` | int | Maximum number of journeys to return (default: 50) |
| `category` | string | Filter by request category (e.g. `database`, `kubernetes`) |
| `outcome` | string | Filter by computed journey outcome (e.g. `outcome=unverified_claim`, `outcome=error`) |
| `has_retries` | bool | When `true`, return only journeys where at least one mutation tool had to retry |
| `trace_id` | string | Filter to a single journey by exact trace ID |
| `origin` | string | Filter by dispatch path: `agent` (LLM-mediated) or `gateway`. See [§7.1](#71-origin-values-in-journeys). |

All parameters are optional. With no parameters, the 50 most recent journeys
are returned.

### 5.4 Response

```json
[
  {
    "trace_id":    "tr_7c2a1b9e",
    "started_at":  "2026-03-01T09:14:22Z",
    "ended_at":    "2026-03-01T09:14:28Z",
    "duration_ms": 6142,
    "user_id":     "alice",
    "user_query":  "show me slow queries on my-db",
    "agent":       "postgres_database_agent",
    "tools_used":  ["get_session_info", "run_sql"],
    "outcome":     "success",
    "event_count": 5,
    "origin":      "agent"
  },
  {
    "trace_id":    "tr_2e9f4d1a",
    "started_at":  "2026-03-01T08:55:10Z",
    "ended_at":    "2026-03-01T08:55:11Z",
    "duration_ms": 1203,
    "user_id":     "bob",
    "user_query":  "terminate connection 97 on prod-db",
    "agent":       "postgres_database_agent",
    "tools_used":  ["terminate_connection"],
    "outcome":     "error",
    "event_count": 3,
    "origin":      "agent"
  }
]
```

### 5.5 Field reference

| Field | Description |
|-------|-------------|
| `trace_id` | Unique journey identifier; use this to fetch full event detail |
| `started_at` | Timestamp of the first `delegation_decision` or `gateway_request` event |
| `ended_at` | Timestamp of the last event in the trace |
| `duration_ms` | Wall-clock duration from first to last event |
| `user_id` | User who initiated the request (from the delegation event) |
| `user_query` | Original natural-language query text |
| `agent` | Agent that handled the request |
| `category` | Request category from the delegation event (`database`, `kubernetes`, etc.) |
| `tools_used` | Unique tool names called during this journey, sorted alphabetically |
| `outcome` | Highest-priority outcome across all events (see [§5.6](#56-journey-outcomes)) |
| `event_count` | Audit events recorded under this trace (excludes `delegation_verification` and `verification_outcome` plumbing events) |
| `retry_count` | Number of mutation-tool re-poll attempts (non-zero means a tool had to wait for state to propagate but ultimately succeeded) |
| `origin` | Dispatch path for this journey: `"agent"` for LLM-mediated interactions, `"gateway"` for gateway-originated NL queries. Taken from the first `tool_execution` event in the trace. See [§7.1](#71-origin-values-in-journeys). |

### 5.6 Journey outcomes

Outcomes are computed by taking the highest-priority outcome across all events
in the trace. The priority order (highest to lowest) is:

| Outcome | Priority | Meaning |
|---------|----------|---------|
| `unverified_claim` | 9 | A destructive delegation completed but no destructive tool execution appears in the audit trail — strong indicator of LLM fabrication. See [§8](#8-unverified-claims-and-llm-fabrication-detection). |
| `error` | 8 | At least one tool or delegation returned an error |
| `denied` | 7 | A policy engine decision denied the action |
| `escalation_required` | 6 | A mutation tool exhausted all retries and could not confirm the action; manual escalation needed |
| `verified_failed` | 5 | Post-execution Level 2 verification confirmed the mutation did not take effect |
| `verified_warning` | 4 | Post-execution Level 2 verification succeeded after retries; action completed but with delays |
| `approved` | 3 | Human approval was granted for a `require_approval` policy decision |
| `verified_ok` | 2 | Post-execution Level 2 verification confirmed the mutation succeeded on first check |
| `success` | 1 | At least one tool or delegation completed successfully |
| `verified` | 0.5 | Delegation was verified as clean (destructive tool confirmed); does not override any real outcome |

The `outcome` field is absent when no outcome has been recorded for the trace yet.

---

## 6. Examples

> **On Kubernetes:** replace `http://localhost:1199` with
> `http://localhost:8080/api/v1/governance` (Gateway) after running
> `kubectl port-forward -n helpdesk-system svc/helpdesk-gateway 8080:8080`.
> See [section 4.2](#42-kubernetes) for details.

### 6.1 My recent journeys

```bash
# Local / Docker Compose
curl "http://localhost:1199/v1/journeys?user=alice&limit=10"

# Kubernetes (Gateway port-forward)
curl "http://localhost:8080/api/v1/governance/journeys?user=alice&limit=10"
```

### 6.2 Journeys in a time window

```bash
# Yesterday 4pm–midnight UTC
curl "http://localhost:1199/v1/journeys?from=2026-02-28T16:00:00Z&until=2026-03-01T00:00:00Z"

# Kubernetes
curl "http://localhost:8080/api/v1/governance/journeys?from=2026-02-28T16:00:00Z&until=2026-03-01T00:00:00Z"
```

### 6.3 Journeys that ended in error

```bash
curl -s "http://localhost:1199/v1/journeys?outcome=error"
```

### 6.4 Unverified claims — possible LLM fabrication

```bash
# Find all journeys where the orchestrator claimed success but audit disagrees
curl -s "http://localhost:1199/v1/journeys?outcome=unverified_claim"

# Then drill into a specific trace to see the delegation_verification event
curl -s "http://localhost:1199/v1/events?trace_id=tr_7c2a1b9e&event_type=delegation_verification"
```

### 6.5 Journeys needing human escalation

```bash
curl -s "http://localhost:1199/v1/journeys?outcome=escalation_required"
```

### 6.6 Recent database journeys with retries

```bash
curl -s "http://localhost:1199/v1/journeys?category=database&has_retries=true&since=24h"
```

### 6.7 Journey count by user

```bash
curl -s "http://localhost:1199/v1/journeys?limit=200" \
  | jq 'group_by(.user_id) | map({user: .[0].user_id, journeys: length}) | sort_by(-.journeys)'
```

### 6.8 Filter by dispatch path

```bash
# Only LLM-mediated journeys (agent selected the tools)
curl -s "http://localhost:1199/v1/journeys?origin=agent"

# Direct-tool events for a specific fleet job trace (raw event view)
curl -s "http://localhost:1199/v1/events?origin=direct_tool&trace_id=dt_abc12345"

# Confirm that all recent tool calls came through the LLM path — none bypassed it
DIRECT=$(curl -s "http://localhost:1199/v1/events?origin=direct_tool&since=2026-03-01T00:00:00Z" | jq length)
echo "direct-dispatch events since midnight: $DIRECT"
```

---

### 6.9 Drilling Into a Journey

Once you have a `trace_id`, fetch every event in that journey from the events
endpoint:

```bash
# Local / Docker Compose
curl "http://localhost:1199/v1/events?trace_id=tr_7c2a1b9e"
curl "http://localhost:1199/v1/events?trace_id=tr_7c2a1b9e&event_type=policy_decision"

# Kubernetes (Gateway port-forward)
curl "http://localhost:8080/api/v1/governance/events?trace_id=tr_7c2a1b9e"
curl "http://localhost:8080/api/v1/governance/events?trace_id=tr_7c2a1b9e&event_type=policy_decision"
```

For a human-readable policy explanation of any `pol_` event in the trace, use
`govexplain`:

```bash
./govexplain --auditd http://localhost:1199 --event pol_a1b2c3d4
```

See [GOVEXPLAIN.md](GOVEXPLAIN.md) for full govexplain reference.

---

## 7. Journey Coverage

A journey appears in `GET /v1/journeys` when its trace has an **anchor event**
with a non-empty `trace_id`. Two event types serve as anchors:

| Source | Anchor event | Trace prefix |
|--------|-------------|--------------|
| Orchestrator REPL (`cmd/helpdesk`) | `delegation_decision` | `tr_` |
| Gateway NL query (`POST /api/v1/query`) | `gateway_request` (no tool) | `tr_` |

- **NL queries via `POST /api/v1/query`** always produce a journey — the
  Gateway records a `gateway_request` anchor event that ties all subsequent
  agent tool calls to the trace.
- **Direct tool calls via `POST /api/v1/db/{tool}`** produce a Gateway-side
  `gateway_request` event with a `dt_` trace and a `tool_name` set. These
  appear in `GET /v1/events` but **not** in `GET /v1/journeys`.
- **Fleet jobs** appear as journeys. When a fleet job is created via
  `POST /api/v1/fleet/jobs`, the gateway records a `gateway_request` anchor
  event with trace ID `tr_<jobID>` and no tool name. All subsequent tool
  calls for that job (across all servers and steps) carry the same
  `X-Trace-ID: tr_<jobID>` header, so they are grouped under one journey.
  The journey's `user_id` is the fleet-runner service account, `user_query` is
  `"fleet job: <name>"`, and `purpose` is `fleet_rollout`.
- **Raw A2A calls** to an agent endpoint with no `trace_id` in message metadata
  appear in `GET /v1/events` with an empty `trace_id` and are not surfaced by
  journeys at all.

To see all events regardless of journey status, use `GET /v1/events` directly.

```bash
# All fleet job journeys
curl "http://localhost:1199/v1/journeys?purpose=fleet_rollout"

# A specific fleet job's journey
curl "http://localhost:1199/v1/journeys?trace_id=tr_flj_4dd009b7"

# All events for a fleet job (full detail)
curl "http://localhost:1199/v1/events?trace_id=tr_flj_4dd009b7"
```

### 7.1 Origin values in journeys

The `origin` field on a `JourneySummary` records the dispatch path used for
the tool calls in that journey. It is taken from the **first `tool_execution`
event** found in the trace.

| Value | Meaning | Typical source |
|-------|---------|----------------|
| `"agent"` | Tools were selected and invoked by the LLM via the A2A protocol | Interactive `POST /api/v1/query` (orchestrator or gateway NL path) |
| `"gateway"` | Tools were invoked by the gateway itself | Governance or policy evaluation endpoints |

> **Why `direct_tool` never appears here:** Fleet-runner jobs use a `dt_` trace
> prefix. The journey view excludes `dt_` traces (they have `tool_name` set on
> the `gateway_request` anchor, which disqualifies them as journey anchors).
> Fleet-runner tool calls are fully auditable via `GET /v1/events?trace_id=dt_...`
> and their individual `origin` field is `"direct_tool"` — but the journey
> aggregation view is intentionally restricted to human-initiated or
> orchestrator-mediated sessions.

```bash
# Only LLM-mediated journeys (interactive operator sessions)
curl "http://localhost:1199/v1/journeys?origin=agent"

# Confirm no fleet-runner direct-tool journeys exist
curl "http://localhost:1199/v1/journeys?origin=direct_tool"   # always []

# All direct-tool events for a fleet job (raw event view)
curl "http://localhost:1199/v1/events?origin=direct_tool&trace_id=dt_abc12345"
```

---

---

## 8. Unverified Claims and LLM Fabrication Detection

The `unverified_claim` outcome is the highest-severity journey outcome and
indicates a potential **LLM fabrication** incident: the orchestrator delegated
a destructive action (e.g. terminate a connection) to a sub-agent, but the
audit trail shows no destructive tool was actually executed.

### How it works

After every `delegate_to_agent` call, the orchestrator:

1. Queries `GET /v1/events?event_type=tool_execution&trace_id=X&since=T` to
   fetch all tool executions recorded by the sub-agent since the delegation started
2. Classifies each tool against the known action map (`terminate_connection` →
   `destructive`, `cancel_query` → `write`, etc.)
3. Emits a `delegation_verification` event recording the result
4. If the delegation was `destructive` and **no destructive tool execution**
   appears in the trail, sets `mismatch=true`

When `mismatch=true`:
- The journey outcome is elevated to `unverified_claim`
- The orchestrator's response includes an `[AUDIT VERIFICATION]` block
  informing the LLM that the action could not be confirmed
- The LLM's system prompt instructs it to report this to the user and **not**
  claim success

### Investigating an unverified claim

```bash
# 1. Find all unverified claim journeys
curl -s "http://localhost:1199/v1/journeys?outcome=unverified_claim"

# 2. Get the trace_id from the result, then fetch all events for that trace
curl -s "http://localhost:1199/v1/events?trace_id=tr_abc123"

# 3. Look specifically at the delegation_verification event
curl -s "http://localhost:1199/v1/events?trace_id=tr_abc123&event_type=delegation_verification"
```

The `delegation_verification` event shows:
- `tools_confirmed`: what the agent actually executed
- `destructive_confirmed`: which of those were destructive
- `mismatch`: whether there is a discrepancy

### Root causes

| Scenario | How to confirm | Action |
|----------|---------------|--------|
| Agent was never called (LLM fabricated the entire response) | No `gateway_request` event for the agent in the trace | Retry; report to the AI governance team |
| Agent was called but couldn't connect | `gateway_request` event present, no `tool_execution` events | Check agent logs; retry |
| Async propagation race (rare) | `delegation_verification` was emitted before the tool event reached auditd | Retry the same delegation |
| Genuine policy block | `policy_decision` with `effect=deny` in the trace | Check policy configuration |

The `unverified_claim` detection is generic — it works for any tool classified
as `destructive` in the action map, both current and future ones.

---

## 9. Environment Variables

| Variable | Description |
|----------|-------------|
| `HELPDESK_AUDIT_URL` | auditd base URL (e.g. `http://localhost:1199`) |
| `HELPDESK_GATEWAY_URL` | Gateway base URL (e.g. `http://localhost:8080`) |

---

## 10. Troubleshooting

### 10.1 `curl: (7) Failed to connect to localhost port 1199` on Kubernetes

auditd is an in-cluster service and is not exposed outside the cluster. Use
the Gateway port-forward instead (see [section 4.2](#42-kubernetes)):

```bash
kubectl port-forward -n helpdesk-system svc/helpdesk-gateway 8080:8080
# then use http://localhost:8080/api/v1/governance/journeys
```

### 10.2 Empty result despite active agents

Journeys are anchored to `delegation_decision` events. Confirm they exist:

```bash
# Local / Docker Compose
curl -s "http://localhost:1199/v1/events?event_type=delegation_decision" | jq length

# Kubernetes
curl -s "http://localhost:8080/api/v1/governance/events?event_type=delegation_decision" | jq length
```

If this returns `0`, either the orchestrator is not running with
`HELPDESK_AUDIT_URL` set, or you are querying a fresh database. Run a
natural-language query via `POST /api/v1/query` to produce the first journey.

### 10.3 Events visible in `/v1/events` but not in `/v1/journeys`

These are direct tool calls or raw A2A invocations — they produce `tool_execution`
events but no `delegation_decision` anchor. See [Journey Coverage](#7-journey-coverage)
above.

### 10.4 `started_at` / `ended_at` in unexpected order

`started_at` is the timestamp of the first anchor event (delegation or gateway);
`ended_at` is the timestamp of the last event under that trace. If clocks are
skewed between the Gateway host and the agent host, these may appear reversed.
Ensure NTP is synchronised across all components.

### 10.5 Journey shows `unverified_claim` but the action did succeed

This can happen in a rare async race: the `delegation_verification` check ran
before the sub-agent's `tool_execution` event was persisted to auditd. In this
case:

1. Verify by checking `GET /v1/events?trace_id=X&event_type=tool_execution` —
   if the destructive tool appears, it was a timing issue
2. The orchestrator will have told the user the action could not be verified;
   the user should retry — the second attempt will produce a clean verification
