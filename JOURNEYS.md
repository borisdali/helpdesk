# aiHelpDesk User Journeys: Audit Trail Across a Request

This document covers the `GET /v1/journeys` endpoint and the concept of a
journey in the aiHelpDesk audit model. For the broader governance architecture
see [AIGOVERNANCE.md](AIGOVERNANCE.md). For policy decision history see
[GOVEXPLAIN.md](GOVEXPLAIN.md).

---

## What Is a Journey

A **journey** is everything that happened as a result of one user request —
all the audit events that share the same `trace_id`.

A single natural-language query like _"show me slow queries on alloydb-on-vm"_
typically produces:

- one `delegation_decision` event (orchestrator routes the request to an agent)
- one or more `tool_call` events (the agent executes tools)
- zero or more `policy_decision` events (policy checks on each tool)
- zero or more `reasoning` events (LLM chain-of-thought, if enabled)

`GET /v1/journeys` groups these into one summary per trace, ordered
newest-first, so you can see all recent activity at a glance and then drill
into any trace for the full event-by-event breakdown.

---

## The Three Audit IDs

| ID | Scope | Prefix | Example |
|----|-------|--------|---------|
| `event_id` | One record | `evt_`, `tool_`, `pol_`, `rsn_` | `tool_a1b2c3d4` |
| `session_id` | One process lifetime | `sess_`, `dbagent_`, `k8sagent_` | `dbagent_9f3e` |
| `trace_id` | One user request end-to-end | `tr_`, `dt_` | `tr_7c2a1b9e` |

- `session_id → trace_id` is 1:M — one agent session handles many user requests.
- `trace_id → event_id` is 1:M — one user request produces many audit records.
- `trace_id` is what uniquely identifies a journey.

### Trace ID prefixes

| Prefix | Origin |
|--------|--------|
| `tr_` | Natural-language query via `POST /api/v1/query` (orchestrator-routed) |
| `dt_` | Direct tool call via `POST /api/v1/db/{tool}` or `/api/v1/k8s/{tool}` (gateway direct) |

Events without a `trace_id` are agent invocations that predate trace propagation
or were made by scripts that bypassed the gateway entirely.

---

## Architecture

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
         │                                │               └── pol_* events
         │                                │
         └── GET /api/v1/governance/journeys ──────────► GET /v1/journeys
             (read journeys)                             returns []JourneySummary
```

The gateway proxies all governance reads under `/api/v1/governance/...`.
The raw endpoint at `/v1/journeys` is served directly by auditd.

---

## Endpoint

### `GET /v1/journeys` (auditd)
### `GET /api/v1/governance/journeys` (gateway proxy)

Returns an array of journey summaries, newest first.

### Query parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `user` | string | Filter to journeys initiated by this user ID |
| `from` | RFC3339 | Only journeys whose delegation event is at or after this time |
| `until` | RFC3339 | Only journeys whose delegation event is before this time |
| `limit` | int | Maximum number of journeys to return (default: 50) |

All parameters are optional. With no parameters, the 50 most recent journeys
are returned.

### Response

```json
[
  {
    "trace_id":    "tr_7c2a1b9e",
    "started_at":  "2026-03-01T09:14:22Z",
    "ended_at":    "2026-03-01T09:14:28Z",
    "duration_ms": 6142,
    "user_id":     "alice",
    "user_query":  "show me slow queries on alloydb-on-vm",
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

### Field reference

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

## Examples

### My recent journeys

```bash
curl "http://localhost:1199/v1/journeys?user=alice&limit=10"
```

### Journeys in a time window

```bash
# Yesterday 4pm–midnight UTC
curl "http://localhost:1199/v1/journeys?from=2026-02-28T16:00:00Z&until=2026-03-01T00:00:00Z"
```

### Via gateway

```bash
curl "http://localhost:8080/api/v1/governance/journeys?user=alice&limit=10"
```

### Journeys that ended in error

The summary endpoint does not support an `outcome` filter directly. Use `jq`
to filter client-side:

```bash
curl -s "http://localhost:1199/v1/journeys" \
  | jq '[.[] | select(.outcome == "error")]'
```

### Journey count by user

```bash
curl -s "http://localhost:1199/v1/journeys?limit=200" \
  | jq 'group_by(.user_id) | map({user: .[0].user_id, journeys: length}) | sort_by(-.journeys)'
```

---

## Drilling Into a Journey

Once you have a `trace_id`, fetch every event in that journey from the events
endpoint:

```bash
# All events for a specific journey
curl "http://localhost:1199/v1/events?trace_id=tr_7c2a1b9e"

# Just policy decisions for that journey
curl "http://localhost:1199/v1/events?trace_id=tr_7c2a1b9e&event_type=policy_decision"
```

For a human-readable policy explanation of any `pol_` event in the trace, use
`govexplain`:

```bash
./govexplain --auditd http://localhost:1199 --event pol_a1b2c3d4
```

See [GOVEXPLAIN.md](GOVEXPLAIN.md) for full govexplain reference.

---

## Journey Coverage

A journey only appears in `GET /v1/journeys` if it has at least one
`delegation_decision` event with a non-empty `trace_id`. This means:

- NL queries via `POST /api/v1/query` always produce a journey (orchestrator
  creates the `delegation_decision` event and sets `trace_id`).
- Direct tool calls via `POST /api/v1/db/{tool}` produce a gateway-side
  `gateway_request` event with a `dt_` trace, but no `delegation_decision`
  event. These appear in `GET /v1/events` but **not** in `GET /v1/journeys`.
- Raw A2A calls to an agent endpoint with no `trace_id` in message metadata
  appear in `GET /v1/events` with an empty `trace_id` and are not surfaced by
  journeys at all.

To see all events regardless of journey status, use `GET /v1/events` directly.

---

## Environment Variables

| Variable | Description |
|----------|-------------|
| `HELPDESK_AUDIT_URL` | auditd base URL (e.g. `http://localhost:1199`) |
| `HELPDESK_GATEWAY_URL` | Gateway base URL (e.g. `http://localhost:8080`) |

---

## Troubleshooting

### Empty result despite active agents

Journeys are anchored to `delegation_decision` events. Confirm they exist:

```bash
curl -s "http://localhost:1199/v1/events?event_type=delegation_decision" | jq length
```

If this returns `0`, either the orchestrator is not running with
`HELPDESK_AUDIT_URL` set, or you are querying a fresh database. Run a
natural-language query via `POST /api/v1/query` to produce the first journey.

### Events visible in `/v1/events` but not in `/v1/journeys`

These are direct tool calls or raw A2A invocations — they produce `tool_call`
events but no `delegation_decision` anchor. See [Journey Coverage](#journey-coverage)
above.

### `started_at` / `ended_at` in unexpected order

`started_at` is the timestamp of the `delegation_decision` event;
`ended_at` is the timestamp of the last event under that trace. If clocks are
skewed between the gateway host and the agent host, these may appear reversed.
Ensure NTP is synchronised across all components.
