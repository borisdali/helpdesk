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
| `dt_` | Direct tool call via `POST /api/v1/db/{tool}` or `/api/v1/k8s/{tool}` (Gateway direct) |

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
| `from` | RFC3339 | Only journeys whose delegation event is at or after this time |
| `until` | RFC3339 | Only journeys whose delegation event is before this time |
| `limit` | int | Maximum number of journeys to return (default: 50) |

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
    "event_count": 5
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
    "event_count": 3
  }
]
```

### 5.5 Field reference

| Field | Description |
|-------|-------------|
| `trace_id` | Unique journey identifier; use this to fetch full event detail |
| `started_at` | Timestamp of the first `delegation_decision` event |
| `ended_at` | Timestamp of the last event in the trace |
| `duration_ms` | Wall-clock duration from first to last event |
| `user_id` | User who initiated the request (from the delegation event) |
| `user_query` | Original natural-language query text |
| `agent` | Agent that handled the request |
| `tools_used` | Unique tool names called during this journey, sorted alphabetically |
| `outcome` | Worst outcome across all events: `error` > `success` |
| `event_count` | Total audit events recorded under this trace_id |

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

The summary endpoint does not support an `outcome` filter directly. Use `jq`
to filter client-side:

```bash
curl -s "http://localhost:1199/v1/journeys" \
  | jq '[.[] | select(.outcome == "error")]'
```

### 6.4 Journey count by user

```bash
curl -s "http://localhost:1199/v1/journeys?limit=200" \
  | jq 'group_by(.user_id) | map({user: .[0].user_id, journeys: length}) | sort_by(-.journeys)'
```

---

### 6.5 Drilling Into a Journey

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

| Origin | Anchor event | Trace prefix |
|--------|-------------|--------------|
| Orchestrator REPL (`cmd/helpdesk`) | `delegation_decision` | `tr_` |
| Gateway NL query (`POST /api/v1/query`) | `gateway_request` (no tool) | `tr_` |

- **NL queries via `POST /api/v1/query`** always produce a journey — the
  Gateway records a `gateway_request` anchor event that ties all subsequent
  agent tool calls to the trace.
- **Direct tool calls via `POST /api/v1/db/{tool}`** produce a Gateway-side
  `gateway_request` event with a `dt_` trace and a `tool_name` set. These
  appear in `GET /v1/events` but **not** in `GET /v1/journeys`.
- **Raw A2A calls** to an agent endpoint with no `trace_id` in message metadata
  appear in `GET /v1/events` with an empty `trace_id` and are not surfaced by
  journeys at all.

To see all events regardless of journey status, use `GET /v1/events` directly.

---

## 8. Environment Variables

| Variable | Description |
|----------|-------------|
| `HELPDESK_AUDIT_URL` | auditd base URL (e.g. `http://localhost:1199`) |
| `HELPDESK_GATEWAY_URL` | Gateway base URL (e.g. `http://localhost:8080`) |

---

## 9. Troubleshooting

### 9.1 `curl: (7) Failed to connect to localhost port 1199` on Kubernetes

auditd is an in-cluster service and is not exposed outside the cluster. Use
the Gateway port-forward instead (see [section 4.2](#42-kubernetes)):

```bash
kubectl port-forward -n helpdesk-system svc/helpdesk-gateway 8080:8080
# then use http://localhost:8080/api/v1/governance/journeys
```

### 9.2 Empty result despite active agents

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

### 9.3 Events visible in `/v1/events` but not in `/v1/journeys`

These are direct tool calls or raw A2A invocations — they produce `tool_call`
events but no `delegation_decision` anchor. See [Journey Coverage](#7-journey-coverage)
above.

### 9.4 `started_at` / `ended_at` in unexpected order

`started_at` is the timestamp of the `delegation_decision` event;
`ended_at` is the timestamp of the last event under that trace. If clocks are
skewed between the Gateway host and the agent host, these may appear reversed.
Ensure NTP is synchronised across all components.
