# aiHelpDesk Incidents

An Incident in aiHelpDesk is more than a failure event. It is a **structured diagnostic trace** — a complete, timestamped record of what was observed, what tools were called, what remediation was attempted, and what the outcome was. This structure is what makes an Incident an asset, not just a problem to be closed.

Every Incident, whether it comes from a real production failure or from deliberate fault injection, produces the same artifact. That uniformity is what connects the four core concepts of aiHelpDesk into a self-improving system:

```
  Incident (real or injected)
      │
      ├── contains one or more Faults          → docs/FAULTTEST.md
      │
      ├── leaves an audit trace                → docs/AUDIT.md
      │
      ├── triggers Playbook remediation        → docs/PLAYBOOKS.md
      │
      └── feeds a draft into the Vault         → docs/VAULT.md
```

The [Operational SRE/DBA Flywheel](VAULT.md#the-operational-sredba-flywheel) runs on this structure. Without it, incidents are isolated events. With it, each one makes the next one faster to resolve.

---

## What an Incident Contains

An Incident is created by the `create_incident_bundle` tool, called either by the Incident agent during a real investigation or by `faulttest` during a controlled injection run.

A bundle is a timestamped `.tar.gz` archive with four optional layers:

| Layer | Contents |
|-------|----------|
| `database/` | PostgreSQL version, connections, locks, replication lag, table stats, settings (via `psql`) |
| `kubernetes/` | Pods, services, endpoints, events, node resource usage (via `kubectl`) |
| `os/` | CPU, memory, disk, running processes, system journal |
| `storage/` | Disk usage, mount points, inode counts |

Not every layer is populated in every Incident. A pure database incident may skip the K8s layer; a DB-down scenario may have an empty `database/` with connection errors recorded. Partial collection is expected and does not prevent the bundle from being created.

Alongside the collected data, the audit trail holds the full reasoning trace: every tool call the agent made, its inputs and outputs, the agent's reasoning, and the policy decisions applied. This is the diagnostic trace that makes the Incident useful beyond immediate triage.

---

## Two Paths Into the System

### Real Incidents

When a production failure occurs, the orchestrator or a triggering system ([srebot](../cmd/srebot/README.md), an alerting webhook) asks the incident agent to investigate. The agent calls `create_incident_bundle` when it has enough information to record the investigation:

```json
{
  "tool": "create_incident_bundle",
  "args": {
    "infra_key": "prod-db-1",
    "description": "Connection pool exhausted — PgBouncer restart resolved",
    "connection_string": "host=prod-db-1 port=5432 dbname=app user=helpdesk",
    "outcome": "resolved"
  }
}
```

The `outcome` field is the trigger for the flywheel:

| Value | Effect |
|-------|--------|
| `"resolved"` | Incident closed — Playbook draft synthesised from the winning approach |
| `"escalated"` | Handed off to a human — draft captures diagnostic steps taken before escalation |
| `""` (empty) | Still investigating — no draft generated yet |

When `outcome` is `"resolved"` or `"escalated"` and `HELPDESK_GATEWAY_URL` is configured, the gateway's `from-trace` endpoint is called automatically. A Playbook draft is synthesised from the audit trail of every tool call made during the investigation and saved to the Vault as an inactive draft.

The bundle result:

```json
{
  "incident_id": "a3f9b2c1",
  "bundle_path": "/incidents/a3f9b2c1.tar.gz",
  "timestamp": "20260427-143022",
  "layers": ["database", "os", "storage"],
  "playbook_draft": "name: Connection Pool Saturation\n...",
  "playbook_id": "pb_a3f9b2c1"
}
```

`playbook_id` is the Vault identifier of the persisted draft. When the gateway's auditd integration is not configured, the draft is returned inline in `playbook_draft` only.

### Injected Incidents (faulttest)

A controlled fault injection run produces the same structured artifact through a different path. `faulttest` injects a known Fault, which is a specific, reproducible failure mode. Injecting a fault sends a diagnostic prompt to the agent, scores the response, and optionally triggers the linked Playbook to verify recovery.

When remediation succeeds, faulttest calls the same `from-trace` endpoint and saves a draft to the Vault:

```
[PASS] Max connections (db-max-connections) — score: 91%
       Remediation: RECOVERED in 4.2s
       Vault: draft saved → pb_faulttest_a1b2c3
```

The critical point: **from the Vault's perspective, a real Incident and an injected Incident look identical.** Both produce a draft. Both go through the same human review gate before activation. The Vault does not distinguish between production knowledge and validated synthetic knowledge — it accumulates both.

See also the [Life of an Incident](PLAYBOOKS.md#life-of-an-incident) for a concrete example of this path.

---

## Faults and Incidents

A Fault is a specific, named failure mode. An Incident may contain one or more Faults.

In fault injection testing, each Fault is a discrete catalog entry with an injection script, a teardown script, expected diagnostic keywords, expected tool calls, and optionally a linked Playbook for remediation. The catalog ships with built-in Faults covering the most common PostgreSQL failure modes; operators can extend it with custom entries for their environment.

In real Incidents, Faults are not pre-declared — the agent discovers them during investigation. A single Incident might surface connection exhaustion caused by a misconfigured application connection pool combined with a runaway query holding locks. The audit trace captures both.

The relationship:

```
Fault Catalog (testing/catalog/failures.yaml)
    │
    ├── db-max-connections      ──► injected → Incident → diagnosis scored → Playbook triggered
    ├── db-lock-contention      ──► injected → Incident → diagnosis scored → Playbook triggered
    ├── db-replication-lag      ──► injected → Incident → diagnosis scored → Playbook triggered
    └── ...
                                            ↕ same Vault entry point
Real production events
    └── connection pool saturated, novel cause  ──► Incident → Playbook draft → Vault
```

See [FAULTTEST.md](FAULTTEST.md) for the full catalog, injection mechanics, scoring, and remediation mode.

---

## The Audit Trail

Every tool call made during an Incident investigation — whether by a human-triggered session, the orchestrator, a fleet runner job, or faulttest — is recorded in the audit trail with:

- Tool name, inputs, and result summary
- Agent identity and session trace ID
- Timestamp and duration
- Policy decision (allowed / denied / approval required)
- Reasoning confidence score

This trail is the raw material for Playbook synthesis. It is also the compliance record for governed deployments — proof of what the agent did, under what policy, authorised by whom.

```bash
# View the trace for a specific incident
curl -s "http://localhost:1199/v1/events?trace_id=tr_a3f9b2c1&limit=50" \
  | jq '.events[] | {tool: .tool_name, result: .result_summary, policy: .policy_decision}'
```

See [AUDIT.md](AUDIT.md) for the full event schema, query API, and retention configuration.

---

## From Incident to Vault: the Full Path

```
  ┌─────────────────────────────────────────────────────────────────────┐
  │                                                                     │
  │   Incident occurs (real or injected)                                │
  │        │                                                            │
  │        ▼                                                            │
  │   Agent investigates — tool calls recorded in audit trail           │
  │        │                                                            │
  │        ▼                                                            │
  │   create_incident_bundle(outcome="resolved")                        │
  │        │                                                            │
  │        ├── Bundle saved  (.tar.gz, database/k8s/os/storage layers)  │
  │        │                                                            │
  │        └── from-trace called automatically                          │
  │                  │                                                  │
  │                  ▼                                                  │
  │            Playbook draft synthesised from audit trace              │
  │            saved to Vault as source=generated, is_active=false      │
  │                  │                                                  │
  │                  ▼                                                  │
  │            Human reviews draft                                      │
  │            (vault list, vault status, API)                          │
  │                  │                                                  │
  │                  ▼                                                  │
  │            Operator activates → Library improves                    │
  │                  │                                                  │
  │                  └──────────────────────────────────► next Incident │
  │                                                       handled faster│
  └─────────────────────────────────────────────────────────────────────┘
```

---

## Listing and Retrieving Incidents

```bash
# List all incidents via the gateway
curl -s http://localhost:8080/api/v1/incidents \
  -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" | jq .

# List via the incident agent directly
curl -s http://localhost:1104/invoke \
  -H "Content-Type: application/json" \
  -d '{"tool": "list_incidents", "args": {}}'

# Create an incident bundle directly via the gateway
curl -s -X POST http://localhost:8080/api/v1/incidents \
  -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "description": "High connection count on prod-db-1",
    "connection_string": "host=prod-db-1 port=5432 dbname=app user=helpdesk",
    "infra_key": "prod-db-1",
    "outcome": "resolved"
  }'
```

Bundles are stored in `HELPDESK_INCIDENT_DIR` (default: current directory for host deployments, `/data/incidents` in Docker/K8s). An `incidents.json` index tracks all created bundles with IDs, timestamps, and paths.

---

## Connection to Other Docs

| Document | What it covers |
|----------|----------------|
| [VAULT.md](VAULT.md) | The Operational SRE/DBA Flywheel; how drafts enter and are activated; vault CLI commands |
| [FAULTTEST.md](FAULTTEST.md) | Fault catalog, injection mechanics, scoring, remediation mode, vault integration |
| [PLAYBOOKS.md](PLAYBOOKS.md) | Playbook schema, CRUD API, import formats, system Playbooks |
| [PLAYBOOK_OPS.md](PLAYBOOK_OPS.md) | Step-by-step investigation workflow; outcome hygiene; Vault draft review |
| [AUDIT.md](AUDIT.md) | Audit event schema, query API, compliance record, retention |
| [AIGOVERNANCE.md](AIGOVERNANCE.md) | Policy engine, approval workflows, blast radius guardrails |
| [API.md](API.md) | Full REST API reference including `/incidents` and `/fleet/playbooks/from-trace` |
