# Policy Explainability: govexplain Reference

This document covers the `govexplain` CLI and the supporting explainability
infrastructure in auditd. It assumes the governance system is running (policy
engine enabled, auditd reachable). For the broader governance architecture see
[AIGOVERNANCE.md](AIGOVERNANCE.md).

---

## Overview

The explainability layer answers three questions:

| Question | Mode | Command |
|----------|------|---------|
| What would happen if I tried this? | Hypothetical | `--resource TYPE:NAME --action ACTION` |
| Why was event X allowed or denied? | Retrospective | `--event EVENT_ID` |
| What decisions happened recently? | List | `--list [--since 1h] [--effect deny]` |

All three modes share the same output format: a human-readable explanation
derived from the full policy evaluation trace, with machine-readable JSON
available via `--json`.

---

## Architecture

```
govexplain                     auditd :1199
    │                               │
    ├── --resource / --action ──────► GET /v1/governance/explain
    │   (hypothetical)              │   policy engine evaluates in-memory
    │                               │   no audit event written
    │                               │
    ├── --event EVENT_ID ───────────► GET /v1/events/{id}
    │   (retrospective)             │   returns stored audit event
    │                               │   explanation embedded in policy_decision
    │                               │
    └── --list ─────────────────────► GET /v1/events?event_type=policy_decision
        (batch)                     │   returns array of audit events
                                    │   client-side effect filter + limit
```

The gateway (`--gateway`, default) proxies all three paths under
`/api/v1/governance/...`. The `--auditd` flag talks directly to auditd,
which is useful when the gateway is not running.

---

## Running govexplain

### Direct to auditd (most common for local testing)

```bash
# Build once
go build -o govexplain ./cmd/govexplain/

# Or use go run
go run ./cmd/govexplain/main.go --auditd http://localhost:1199 ...
```

Set `HELPDESK_AUDIT_URL` to avoid repeating `--auditd` on every invocation:

```bash
export HELPDESK_AUDIT_URL=http://localhost:1199
./govexplain --resource database:alloydb-on-vm --action read
```

### Via gateway

```bash
export HELPDESK_GATEWAY_URL=http://localhost:8080
./govexplain --resource database:prod-db --action write --tags production
```

---

## Mode 1: Hypothetical

Evaluates a policy request in-memory. No audit event is written, no tool
executes. The result reflects what would happen if an agent attempted the
action right now.

```bash
./govexplain --auditd http://localhost:1199 \
  --resource database:alloydb-on-vm \
  --action read
```

```
Access to database alloydb-on-vm (tags: development) for read: ALLOWED

Policy "production-database-protection": skipped (resource_mismatch)
Policy "k8s-system-protection": skipped (resource_mismatch)
Policy "business-hours-freeze": skipped (resource_mismatch)
Policy "dba-privileges": skipped (principal_mismatch)
Policy "sre-staging-access": skipped (principal_mismatch)
Policy "automated-services": skipped (principal_mismatch)

Policy "development-permissive" matched:
  Rule 0   read|write → allow            matched
  → ALLOWED

The request is permitted to proceed.
```

### Tag resolution from infrastructure config

When `--tags` is not provided, govexplain automatically resolves the
resource's tags from `HELPDESK_INFRA_CONFIG` — the same config the live
agents use at runtime. This means the hypothetical evaluation reflects the
same policy the agent would actually apply.

```bash
# No --tags needed — tags resolved from infrastructure.json automatically
./govexplain --auditd http://localhost:1199 \
  --resource database:alloydb-on-vm \
  --action read
# → "Access to database alloydb-on-vm (tags: development) for read: ALLOWED"
```

```bash
# --tags overrides the infra config — useful for what-if scenarios
./govexplain --auditd http://localhost:1199 \
  --resource database:alloydb-on-vm \
  --action write \
  --tags production
# → "REQUIRES APPROVAL" — what would happen if this resource were in production
```

Tag resolution is performed by auditd at evaluation time. It requires auditd
to have been started with `HELPDESK_INFRA_CONFIG` set:

```bash
HELPDESK_POLICY_FILE=./policies.example.yaml \
HELPDESK_POLICY_ENABLED=true \
HELPDESK_INFRA_CONFIG=/path/to/infrastructure.json \
./auditd -listen :1199 -db /tmp/audit.db
```

### Hypothetical flags

| Flag | Description |
|------|-------------|
| `--resource TYPE:NAME` | Resource to evaluate, e.g. `database:prod-db`, `kubernetes:prod-ns` |
| `--action ACTION` | `read`, `write`, or `destructive` |
| `--tags TAG,...` | Comma-separated tags — overrides infra config lookup |
| `--user USER_ID` | Evaluate as a specific user |
| `--role ROLE` | Evaluate with a specific role |

---

## Mode 2: Retrospective

Retrieves a specific audit event by ID and shows the policy explanation that
was recorded at the time of the actual agent operation.

```bash
./govexplain --auditd http://localhost:1199 --event pol_a1b2c3d4
```

The event ID appears in the output of `--list` mode and in the `event_id`
field of every audit event. Policy decision events have IDs prefixed `pol_`.

---

## Mode 3: List

Fetches multiple `policy_decision` events from the audit store and prints
their explanations in sequence.

```bash
# All recent decisions (default limit: 20)
./govexplain --auditd http://localhost:1199 --list

# Last hour
./govexplain --auditd http://localhost:1199 --list --since 1h

# Only denials
./govexplain --auditd http://localhost:1199 --list --effect deny

# All decisions for a specific agent session
./govexplain --auditd http://localhost:1199 --list --session SESSION_ID

# All decisions within a single request trace
./govexplain --auditd http://localhost:1199 --list --trace TRACE_ID

# Wider window, more results
./govexplain --auditd http://localhost:1199 --list --since 24h --limit 100
```

### Output format

```
pol_a1b2c3d4  2026-02-22 10:15:30
Access to database prod-db for write: DENIED

No policy matched this resource — default effect is deny.

This usually means the resource is not listed in the infrastructure
config and therefore has no tags. Add it to HELPDESK_INFRA_CONFIG
with appropriate tags so a policy can be applied.
────────────────────────────────────────────────────────────
pol_e5f6g7h8  2026-02-22 10:16:00
Access to database alloydb-on-vm (tags: development) for read: ALLOWED

Policy "development-permissive" matched:
  Rule 0   read|write → allow            matched
  → ALLOWED

The request is permitted to proceed.
```

### List flags

| Flag | Default | Description |
|------|---------|-------------|
| `--since DURATION\|TIMESTAMP` | (all) | Show events newer than this. Accepts Go durations (`1h`, `30m`) or RFC3339 timestamps |
| `--effect EFFECT` | (all) | Filter by outcome: `allow`, `deny`, `require_approval` |
| `--session SESSION_ID` | (all) | Filter by agent session ID |
| `--trace TRACE_ID` | (all) | Filter by trace ID (all decisions within one user request) |
| `--limit N` | `20` | Maximum number of events to show |

Note: `--effect` filtering is applied client-side. The API returns up to 100
events; `--limit` then caps what is displayed.

### Retrieving a specific historical event

To get the full explanation for the 5th most recent decision:

```bash
# Option 1: use --list to find the event ID, then --event for detail
./govexplain --auditd http://localhost:1199 --list --limit 10
./govexplain --auditd http://localhost:1199 --event pol_xxxxxxxx

# Option 2: jq one-liner on the events API directly
curl -s "http://localhost:1199/v1/events?event_type=policy_decision" \
  | jq -r '.[4].policy_decision.explanation'

# Option 3: extract ID then explain
ID=$(curl -s "http://localhost:1199/v1/events?event_type=policy_decision" \
     | jq -r '.[4].event_id')
./govexplain --auditd http://localhost:1199 --event "$ID" --json | jq .
```

---

## JSON Output

All modes support `--json` for machine-readable output.

### Hypothetical (`--resource` / `--action`)

Returns a `DecisionTrace`:

```json
{
  "decision": {
    "effect": "deny",
    "policy_name": "default",
    "rule_index": 0,
    "message": "No matching policy found"
  },
  "policies_evaluated": [
    {
      "policy_name": "production-database-protection",
      "matched": false,
      "skip_reason": "resource_mismatch"
    }
  ],
  "default_applied": true,
  "explanation": "Access to database prod-db for write: DENIED\n\n..."
}
```

### Retrospective (`--event`) and List (`--list`)

Returns the stored audit event(s). The explanation is in
`policy_decision.explanation`:

```json
{
  "event_id": "pol_a1b2c3d4",
  "timestamp": "2026-02-22T10:15:30Z",
  "event_type": "policy_decision",
  "policy_decision": {
    "resource_type": "database",
    "resource_name": "prod-db",
    "action": "write",
    "effect": "deny",
    "policy_name": "default",
    "explanation": "Access to database prod-db for write: DENIED\n\n..."
  }
}
```

`--list --json` returns a JSON array of these event objects.

---

## Exit Codes

### Hypothetical and retrospective modes

| Code | Meaning |
|------|---------|
| `0` | Allowed |
| `1` | Denied |
| `2` | Requires approval |
| `3` | Error (network, missing args, policy engine not configured) |

### List mode

Exit code reflects the worst outcome across all listed events:

| Code | Meaning |
|------|---------|
| `0` | All events are allow |
| `1` | At least one deny |
| `2` | At least one require_approval, no denials |
| `3` | Error |

This makes `--list` scriptable: `--list --since 1h --effect deny` exits 1 if
there were any denials in the past hour, 0 otherwise.

---

## Seeing Allowed Decisions in auditd Logs

By default auditd logs denied and require_approval decisions at `WARN`/`INFO`
level. Allowed decisions are logged at `DEBUG` and suppressed. To see all
three:

```bash
HELPDESK_POLICY_FILE=./policies.example.yaml \
HELPDESK_POLICY_ENABLED=true \
HELPDESK_INFRA_CONFIG=/path/to/infrastructure.json \
./auditd -listen :1199 -db /tmp/audit.db --log-level=debug
```

The log lines look like:

```
DEBU policy decision: ALLOW   action=read  resource_type=database  resource_name=alloydb-on-vm  effect=allow  policy=development-permissive
WARN policy decision: DENY    action=write resource_type=database  resource_name=prod-db         effect=deny   policy=default  message="No matching policy found"
INFO policy decision: REQUIRE_APPROVAL  action=write  resource_type=database  resource_name=alloydb-on-vm  effect=require_approval  policy=production-database-protection
```

`--log-level` is also accepted as an environment variable:

```bash
HELPDESK_LOG_LEVEL=debug ./auditd -listen :1199 -db /tmp/audit.db
```

---

## Real-Time Event Stream (auditor)

To watch all audit events as they arrive from live agent sessions:

```bash
# Alerts only (default) — denied decisions, anomalies, chain breaks
./auditor -audit-service=http://localhost:1199

# All events — tool executions, policy decisions, reasoning events
./auditor -audit-service=http://localhost:1199 -log-all
```

`auditor` and `govexplain` serve different purposes:

| Tool | When to use |
|------|-------------|
| `govexplain --list` | Review recent policy decisions after the fact; filter by effect, session, trace |
| `auditor -log-all` | Live stream of everything as it happens during an active agent session |
| `govexplain --event ID` | Deep-dive on a single specific event |
| `govexplain --resource / --action` | Hypothetical — test a permission without running the agent |

---

## Environment Variables

| Variable | Description |
|----------|-------------|
| `HELPDESK_AUDIT_URL` | Default value for `--auditd` (e.g. `http://localhost:1199`) |
| `HELPDESK_GATEWAY_URL` | Default value for `--gateway` (e.g. `http://localhost:8080`) |
| `HELPDESK_POLICY_FILE` | Path to policy YAML — required for hypothetical mode |
| `HELPDESK_POLICY_ENABLED` | Set to `true` or `1` to activate the policy engine |
| `HELPDESK_INFRA_CONFIG` | Path to `infrastructure.json` — enables automatic tag resolution |
| `HELPDESK_LOG_LEVEL` | Log verbosity for auditd: `debug`, `info` (default), `warn`, `error` |

---

## Troubleshooting

### "No policy file configured"

auditd was started without `HELPDESK_POLICY_FILE` and/or `HELPDESK_POLICY_ENABLED`.
The hypothetical explain endpoint requires the policy engine to be loaded:

```bash
HELPDESK_POLICY_FILE=./policies.example.yaml \
HELPDESK_POLICY_ENABLED=true \
./auditd -listen :1199 -db /tmp/audit.db
```

### Tags not resolved — default-deny on a known resource

auditd was started without `HELPDESK_INFRA_CONFIG`, so it cannot look up the
resource's tags. Either set the variable:

```bash
HELPDESK_INFRA_CONFIG=/path/to/infrastructure.json ./auditd ...
```

Or pass tags explicitly on the govexplain command:

```bash
./govexplain --auditd http://localhost:1199 \
  --resource database:alloydb-on-vm \
  --action read \
  --tags development
```

The auditd startup log confirms whether infra config loaded:

```
INFO infra config loaded for tag resolution  databases=3  k8s_clusters=1
```

If you see instead:

```
WARN failed to load infra config; explain won't auto-resolve tags  path=...  err=...
```

The path is wrong or points to a directory rather than the JSON file.

### "No policy decision events found" in --list mode

`--list` shows real audit events recorded by running agents. Hypothetical
`govexplain --resource ... --action ...` queries do not write events to the
store. To populate the store, run an actual agent session with
`HELPDESK_AUDIT_URL` set.

To check what event types are currently in the store:

```bash
curl -s "http://localhost:1199/v1/events" \
  | jq 'group_by(.event_type) | map({type: .[0].event_type, count: length})'
```

### flag provided but not defined: -log-level

You are running an `auditd` binary that was built before the `--log-level`
flag was wired correctly. Rebuild:

```bash
go build -o auditd ./cmd/auditd/
```
