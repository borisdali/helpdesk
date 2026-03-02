# aiHelpDesk Compliance Reporting

This document covers the Compliance Reporting sub-module end to end: tool
invocation instrumentation, the ten-phase `govbot` report, policy coverage
analysis, dead rule detection, compliance history persistence, and the
historical trend block. For the broader AI Governance architecture see
[AIGOVERNANCE.md](AIGOVERNANCE.md). For the audit hash chain, event schema,
and `auditd` API reference see [AUDIT.md](AUDIT.md).

---

## 1. Overview

Compliance in aiHelpDesk operates at two distinct levels:

| Level | What | When |
|-------|------|------|
| **Real-time enforcement** | Policy engine allows/denies/approves each tool call as it happens | Every tool invocation |
| **Compliance reporting** | `govbot` takes a periodic snapshot of the entire governance posture | Scheduled (e.g. daily) or on-demand |

This document covers the second level. `govbot` is the only component in the
Compliance Reporting sub-module. It queries the gateway and auditd over HTTP,
runs ten sequential analysis phases, and emits a structured report to stdout.
Every run can be persisted to build a historical trend across days or weeks.

---

## 2. Architecture

```
                        ┌─────────────────────────────────────────────────────┐
                        │                  aiHelpDesk cluster                 │
                        │                                                     │
  database agent :1100  │──► tool_invoked ──────────────────────────────────┐ │
  k8s agent      :1102  │──► tool_invoked ──────────────────────────────────┤ │
                        │                                                   ▼ │
                        │                                         ┌─────────────────┐
                        │   policy check ────────────────────────►│  auditd :1199   │
                        │   (pol_* event)                         │                 │
                        │                                         │ • audit events  │
                        │                                         │ • govbot_runs   │
                        │                                         │ • approvals     │
                        │                                         └────────┬────────┘
                        │                                                  │
                        │                      ┌───────────────────────────┘
                        │                      ▼
                        │           ┌─────────────────────┐
                        │           │   govbot (one-shot)  │
                        │           │                      │
                        │           │ 1. Governance Status │◄─── GET /api/v1/governance
                        │           │ 2. Policy Overview   │
                        │           │ 3. Audit Activity    │◄─── GET /v1/events?since=...
                        │           │ 4. Policy Decisions  │
                        │           │ 5. Agent Coverage    │
                        │           │ 6. Pending Approvals │◄─── GET /v1/approvals/pending
                        │           │ 7. Chain Integrity   │◄─── GET /v1/verify
                        │           │ 8. Mutation Activity │
                        │           │ 9. Coverage Analysis │  (uses events from Phase 3)
                        │           │10. Summary           │
                        │           │                      │
                        │           │  POST /v1/govbot/runs│──► persists snapshot
                        │           └──────────┬───────────┘
                        └──────────────────────┼──────────────────────────────┘
                                               │
                                        stdout report
                                        + optional Slack webhook
```

---

## 3. Tool Invocation Instrumentation

### 3.1 The `tool_invoked` event

For compliance analysis to detect **unchecked tool calls** (i.e. tools that
executed without any policy evaluation), a `tool_invoked` event is emitted
unconditionally at the very start of every `CheckTool` call — before the policy
engine is consulted and before any early-return for agents running without
enforcement.

```
Agent calls CheckTool(ctx, resourceType, resourceName, action, tags)
      │
      ├── emit tool_invoked event  ← fires regardless of mode or policy config
      │         event_id: "inv_xxxxxxxx"
      │         resource_type, resource_name, action, tags captured
      │
      ├── (if no engine configured) → return nil  [fire-and-forget]
      │
      └── evaluate policy → emit pol_* event → allow / deny / require_approval
```

The `tool_invoked` event type is `"tool_invoked"`, distinct from
`"policy_decision"`. Phase 9 of the govbot report correlates the two event
types to identify coverage gaps.

### 3.2 Agent scope

| Agent | Instrumented | Notes |
|-------|-------------|-------|
| database-agent | **Yes** | All database tool calls via `agentutil.CheckTool` |
| k8s-agent | **Yes** | All Kubernetes tool calls via `agentutil.CheckTool` |
| incident-agent | No | Does not use policy-checked tools |
| research-agent | No | Does not use policy-checked tools |

Phase 9 includes an explicit note about this scope: results reflect
database and k8s agents only. A `tool_invoked` event that arrives with no
matching `policy_decision` in the same window signals a genuine coverage gap —
the tool ran but policy was never consulted.

### 3.3 Event fields

```json
{
  "event_id": "inv_a1b2c3d4",
  "event_type": "tool_invoked",
  "timestamp": "2026-03-01T09:00:00Z",
  "trace_id": "tr_...",
  "action_class": "write",
  "policy_decision": {
    "resource_type": "database",
    "resource_name": "prod-db",
    "action": "write",
    "tags": ["production"],
    "effect": ""          // intentionally empty — no policy evaluated yet
  }
}
```

The `effect` field is empty on `tool_invoked` events. A non-empty `effect`
(allow / deny / require_approval) only appears on `policy_decision` events.

---

## 4. Compliance Phases

govbot runs ten sequential phases and exits:

| Phase | Name | Data source |
|-------|------|-------------|
| 1 | Governance Status | `GET /api/v1/governance` |
| 2 | Policy Overview | Phase 1 data |
| 3 | Audit Activity | `GET /v1/events?since=...&limit=1000` |
| 4 | Policy Decision Analysis | Phase 3 data |
| 5 | Agent Enforcement Coverage | Phase 3 data |
| 6 | Pending Approvals | `GET /v1/approvals/pending` |
| 7 | Chain Integrity | `GET /v1/verify` |
| 8 | Mutation Activity | Phase 3 data |
| 9 | Policy Coverage Analysis | Phase 3 data + Phase 1 policy list |
| 10 | Compliance Summary | Aggregated alerts and warnings |

**Exit codes:**

| Code | Meaning |
|------|---------|
| `0` | Healthy — no alerts or warnings |
| `1` | Fatal — could not reach gateway |
| `2` | Alerts present — chain failure, policy bypass, or other critical finding |

Exit code `2` is useful in CI pipelines and cron-based alerting.

---

## 5. Policy Coverage Analysis (Phase 9)

Phase 9 is the core compliance analysis phase. It uses the audit events
already fetched in Phase 3 — no additional API calls.

### 5.1 Coverage gap detection

For each `(resource_type/resource_name, action)` pair observed in the window,
Phase 9 computes:

```
invoked  = count of tool_invoked events for this pair
checked  = count of policy_decision events for this pair (pre-execution only)
gap      = invoked − checked
coverage = checked / invoked × 100%
```

A `gap > 0` means some invocations executed without a policy check. These are
reported per resource-action pair:

```
[09:00:05] ── Phase 9: Policy Coverage Analysis ─────────────────────────────
[09:00:05] Note: reflects database + k8s agents only (incident + research not instrumented)

[09:00:05] Uncovered invocations (tool_invoked with no matching policy_decision):

[09:00:05]   [✗ ALERT]  database/prod-db                    action=write         invoked=10  checked=7   gap=3   (70% coverage)
[09:00:05]   [⚠ WARN ]  database/staging-db                 action=read          invoked=5   checked=0   gap=5   (0% coverage)

[09:00:05]   2 other resource-action pair(s): fully covered
```

**Severity rules:**

| Action class | Gap severity |
|-------------|--------------|
| `write` | **Alert** (exit 2) |
| `destructive` | **Alert** (exit 2) |
| `read` | Warning |

When all pairs are fully covered:

```
[09:00:05]   All 4 resource-action pair(s) fully covered ✓
```

When no `tool_invoked` events exist in the window (e.g. first run before
agents execute any tools):

```
[09:00:05]   No tool_invoked events in this window — no coverage data yet
[09:00:05]   (coverage data accumulates as agents execute tools with updated instrumentation)
```

### 5.2 Dead rule detection

After the coverage gap analysis, Phase 9 cross-references the policy
configuration against the set of resource types that actually appeared in
`tool_invoked` events. A policy covering a resource type with **zero**
observed invocations in the window is flagged as a dead rule:

```
[09:00:05] Dead policy rules (policy exists but no invocations observed):
[09:00:05]   ⚠ WARN  policy "k8s-production" covers resource type "kubernetes" — no tool_invoked events in window
```

Dead rules indicate one of:
- The policy is stale — the resource type it covers is no longer used
- The agent that would invoke tools for this resource type did not run during the window
- The window is too narrow (e.g. `‑since=1m`) to capture normal traffic

Dead rule warnings are included in the Phase 10 summary count and included in
the Slack message when a webhook is configured.

Dead rule detection only runs when at least one `tool_invoked` event exists
in the window, so a freshly deployed cluster does not generate false positives
before agents have executed any tools.

### 5.3 Invocations-by-resource snapshot

At the end of Phase 9, the per-pair `(invoked, checked)` counts are serialised
to JSON and stored in the compliance run snapshot under
`invocations_by_resource`. This field drives the Coverage line in the
[Historical Trend Block](#7-historical-trend-block).

```json
{
  "database/prod-db:write":  { "invoked": 10, "checked": 10 },
  "database/prod-db:read":   { "invoked": 35, "checked": 35 },
  "kubernetes/prod:write":   { "invoked": 4,  "checked": 3  }
}
```

---

## 6. Compliance History

Every govbot run can be persisted so that Phase 9 coverage maps accumulate
over time and the trend block has comparison data.

### 6.1 Storage backends

| Backend | Flag / env var | When to use |
|---------|---------------|-------------|
| **auditd** (recommended) | `-audit-url` / `HELPDESK_AUDIT_URL` | Docker Compose, Kubernetes — no extra volume or DB needed |
| **Local SQLite** | `-history-db /path/to/history.db` | Standalone or air-gapped installs |
| **PostgreSQL** | `-history-db postgres://user:pass@host/db` | Shared history across multiple govbot instances |

The auditd backend stores runs in the shared `govbot_runs` table inside the
same audit database. Multiple govbot instances (one per team or gateway) can
write to the same central auditd; the `gateway` column distinguishes their
origin.

### 6.2 What each snapshot stores

| Field | Description |
|-------|-------------|
| `run_at` | UTC timestamp of the run |
| `window` | Look-back window (e.g. `24h`) |
| `gateway` | Gateway URL this govbot pointed at |
| `status` | `healthy` / `warnings` / `alerts` |
| `alert_count`, `warning_count` | Counts of alerts and warnings |
| `alerts_json`, `warnings_json` | Full text of each alert and warning |
| `chain_valid` | Whether the audit hash chain was intact |
| `policy_denies` | Number of policy denials in the window |
| `policy_no_match` | Number of no-match decisions |
| `mutations_total`, `mutations_destructive` | Write and destructive tool counts |
| `pending_approvals`, `stale_approvals` | Approval queue state |
| `decisions_by_resource` | Per-resource allow/deny/req_apr breakdown (JSON) |
| `invocations_by_resource` | Per-pair `(invoked, checked)` coverage counts (JSON) |

### 6.3 Retention

```
govbot  ──POST /v1/govbot/runs?retain=365──►  auditd
```

When govbot posts a new run to auditd it passes the retain limit as a query
parameter. auditd prunes the oldest rows for that gateway, keeping at most
`retain` rows. The default retain limit is **365** (one year of daily runs).
Override with `-history-retain N`.

The local SQLite/PostgreSQL backend applies the same pruning logic inline on
every `save()` call.

---

## 7. Historical Trend Block

When at least **two prior runs** have been recorded for the same look-back
window, a compact trend block is appended after Phase 10:

```
[09:00:06] Historical Trend (last 30 daily runs):
[09:00:06]   Status:     healthy 28  warnings 2  alerts 0
[09:00:06]   Denials:    avg 0.4/run   today 2   ↑ above avg
[09:00:06]   Mutations:  avg 12/run    today 17  ↑ above avg
[09:00:06]   Chain:      valid 30/30
[09:00:06]   Stale apr:  avg 0.1/run   today 0
[09:00:06]   Coverage:   avg 94%/run   today 72%  ↓ below avg
```

**Lines explained:**

| Line | What it shows |
|------|--------------|
| Status | Count of healthy / warnings / alerts runs across the prior runs |
| Denials | Average policy denials per run vs. today's count; `↑ above avg` when today > 1.5× the average |
| Mutations | Average write/destructive tool calls per run vs. today; same threshold |
| Chain | How many prior runs had a valid audit hash chain |
| Stale apr | Average stale approval count per run vs. today |
| Coverage | Average `checked/invoked` % across prior runs vs. today; `↓ below avg` when today drops more than 5 percentage points below the average |

The Coverage line is omitted when no prior run has `invocations_by_resource`
data (i.e., before the updated agent instrumentation has been deployed and
run for at least one cycle).

---

## 8. Browsing Past Runs

### 8.1 History table

```bash
# From auditd (no gateway contact needed)
govbot -audit-url http://localhost:1199 -show-history 10

# From a local database
govbot -history-db /var/lib/govbot/history.db -show-history 10
```

Output:

```
Run at (UTC)          Window  Status    Denies   Muts  Chain   Alerts  Warnings
───────────────────────────────────────────────────────────────────────────────
2026-03-02 09:00      24h     HEALTHY        2     17      ✓        0         0
2026-03-01 09:00      24h     WARNINGS       0     12      ✓        0         1
2026-02-28 09:00      24h     HEALTHY        0      9      ✓        0         0
───────────────────────────────────────────────────────────────────────────────
3 record(s)
```

In `-show-history` mode govbot exits immediately after printing the table —
it does not contact the gateway or run any of the ten compliance phases.

### 8.2 REST API (auditd backend)

```bash
# All runs, newest first
curl "http://localhost:1199/v1/govbot/runs"

# Filter by look-back window
curl "http://localhost:1199/v1/govbot/runs?window=24h&limit=10"

# Filter by gateway (useful in central / multi-team deployments)
curl "http://localhost:1199/v1/govbot/runs?gateway=http%3A%2F%2Fgateway%3A8080&limit=30"
```

Each element of the JSON array is a `GovbotRun` object mirroring the fields
listed in §6.2. The `invocations_by_resource` field carries the per-pair
coverage JSON for further processing.

---

## 9. Flags Reference

```
-gateway string
      Gateway base URL (default "http://localhost:8080")

-since duration
      Look-back window for audit event analysis (default 24h)

-webhook string
      Slack webhook URL for posting the compliance summary (optional)

-dry-run
      Collect and print report but do not post to webhook

-audit-url string
      auditd URL for persisting compliance history
      (e.g. http://localhost:1199). Reads HELPDESK_AUDIT_URL env var.
      Takes precedence over -history-db.

-history-db string
      Local history database — SQLite file path or postgres:// DSN.
      Used when -audit-url is not set.

-history-retain int
      Maximum number of runs to keep per gateway (default 365).
      Passed to auditd as ?retain=N; applied locally for -history-db.

-show-history int
      Print last N compliance runs as a table and exit.
      Requires -audit-url or -history-db. Does not contact the gateway.
```

---

## 10. Deployment

### 10.1 Docker Compose

govbot runs under the `governance` profile and reads `HELPDESK_AUDIT_URL`
from the compose environment automatically. No extra volume is needed.

```bash
# On-demand compliance report
docker compose --profile governance run govbot

# Custom look-back window
GOVBOT_SINCE=6h docker compose --profile governance run govbot

# With Slack webhook
GOVBOT_WEBHOOK=https://hooks.slack.com/... \
  docker compose --profile governance run govbot

# Browse history (no gateway contact)
docker compose --profile governance run --no-deps govbot \
  /usr/local/bin/govbot -show-history 10
```

### 10.2 Kubernetes

govbot is deployed as a **CronJob**. Enable it in `values.yaml`:

```yaml
governance:
  govbot:
    enabled: true
    schedule: "0 8 * * *"    # daily at 08:00 UTC
    since: "24h"
    webhook: "https://hooks.slack.com/services/..."
    # historyRetain defaults to 365 — override if needed:
    # historyRetain: 90
```

Compliance history is stored in auditd automatically via `HELPDESK_AUDIT_URL`
injected by the Helm template. No PVC is required.

**Trigger a run immediately** (outside the CronJob schedule):

```bash
RELEASE=helpdesk
NS=helpdesk

kubectl create job -n $NS --from=cronjob/${RELEASE}-govbot govbot-manual-$(date +%s)
kubectl logs -n $NS -f job/govbot-manual-<id>
```

**Browse history from outside the cluster:**

```bash
# Port-forward auditd
kubectl port-forward -n $NS svc/${RELEASE}-auditd 1199:1199

# Query via REST
curl -s "http://localhost:1199/v1/govbot/runs?limit=10" | python3 -m json.tool

# Or use show-history via a one-shot pod
IMAGE=$(kubectl get cronjob -n $NS ${RELEASE}-govbot \
  -o jsonpath='{.spec.jobTemplate.spec.template.spec.containers[0].image}')

kubectl run -n $NS govbot-history \
  --image=$IMAGE --restart=Never \
  --env="HELPDESK_AUDIT_URL=http://${RELEASE}-auditd:1199" \
  -- /usr/local/bin/govbot -show-history 10

kubectl logs -n $NS -f pod/govbot-history
kubectl delete pod -n $NS govbot-history
```

---

## 11. Interpreting Results

### 11.1 Phase 9 — Coverage gaps

| Finding | Likely cause | Fix |
|---------|-------------|-----|
| `write` gap — alert | Agent executed write tools without a policy check; policy not loaded or `HELPDESK_POLICY_FILE` not set | Confirm `HELPDESK_POLICY_ENABLED=true` and `HELPDESK_POLICY_FILE` is set in the agent's environment |
| `read` gap — warning | Read tools called without policy check (allowed by default, but reduces visibility) | Enable policy for read actions or accept the gap |
| First run, no data | `tool_invoked` events only accumulate after the new instrumentation is deployed and agents have run | Allow one full window cycle to pass |

### 11.2 Dead rules

| Finding | Likely cause | Fix |
|---------|-------------|-----|
| Policy covers `database` but no invocations | Database agent did not run during the window, or window is too narrow | Widen `-since`, confirm the database agent is running |
| Policy covers `kubernetes` but no invocations | k8s agent did not run, or namespace is unused | Same as above |
| Dead rule persists for many windows | Policy rule is genuinely stale | Review and remove the policy entry |

### 11.3 Historical Trend — Coverage drop

A `↓ below avg` on the Coverage line means fewer tool calls had a policy check
this run compared to the historical average. Possible causes:

- An agent restarted mid-window and policy wasn't re-loaded in the new process
- A new tool was added without wiring it through `agentutil.CheckTool`
- The policy file was temporarily absent (→ check Phase 2 output)

### 11.4 No trend block appearing

The trend block requires at least two prior runs in the same window (i.e.,
both using the same `-since` value). Run govbot at least twice with history
configured.

---

## 12. Related Documents

| Document | Covers |
|----------|--------|
| [AIGOVERNANCE.md](AIGOVERNANCE.md) | Full governance architecture: policy engine, approvals, guardrails, operating mode, explainability |
| [AUDIT.md](AUDIT.md) | Audit hash chain, event schema, `auditd` and `auditor` API reference |
| [GOVEXPLAIN.md](GOVEXPLAIN.md) | `govexplain` CLI — explain why a specific past event was allowed or denied |
| [MUTATION_TOOLS.md](MUTATION_TOOLS.md) | Write and destructive tool inventory; blast-radius enforcement |
| [cmd/govbot/README.md](cmd/govbot/README.md) | govbot quick-start and sample runs |
| [GOVBOT_SAMPLE.md](GOVBOT_SAMPLE.md) | Full annotated sample govbot output |
