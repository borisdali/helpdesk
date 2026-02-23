# aiHelpDesk: Governance Compliance Reporter (govbot)

The govbot is a one-shot compliance reporter that queries the Helpdesk
gateway's governance API endpoints and produces a structured compliance
snapshot. It is designed to run on-demand or on a schedule (e.g. daily
cron / Kubernetes CronJob) and optionally post a summary to a Slack webhook.

## Architecture

```
Gateway /api/v1/governance/* → govbot → compliance report + optional Slack alert
```

The govbot is stateless and read-only. It contacts the gateway over HTTP,
fetches governance state from the underlying audit daemon (auditd), and
prints a structured report to stdout. No audit socket access or cluster
privileges are required — only network access to the gateway.

## Compliance Phases

govbot runs seven sequential phases and exits:

```
Phase 1 — Governance Status:     GET /api/v1/governance
Phase 2 — Policy Overview:       Detailed policy rule breakdown from Phase 1 data
Phase 3 — Audit Activity:        GET /api/v1/governance/events?since=...&limit=1000
Phase 4 — Policy Decision Analysis: Per-resource allow/deny/no-match breakdown
Phase 5 — Pending Approvals:     GET /api/v1/governance/approvals/pending
Phase 6 — Chain Integrity:       GET /api/v1/governance/verify
Phase 7 — Compliance Summary:    Aggregated alerts and warnings + optional Slack post
```

## Exit Codes

| Code | Meaning |
|------|---------|
| `0`  | Healthy — no alerts or warnings |
| `1`  | Fatal error — could not reach gateway or Phase 1 failed |
| `2`  | Alerts present — chain integrity failure, policy bypass, or other critical finding |

Exit code `2` is useful for CI pipelines and cron alerting.

## Command Line Flags

For details on how to run `govbot` in your specific deployment environment see [here](deploy/docker-compose/README.md#37-running-the-compliance-reporter-govbot) for running via Docker containers and [here](deploy/helm/README.md#97-running-   the-compliance-reporter-govbot) for running on K8s.

```
-gateway string
      Gateway base URL (default "http://localhost:8080")
-since duration
      Look-back window for audit event analysis (default 24h)
-webhook string
      Slack webhook URL for posting the compliance summary (optional)
-dry-run
      Collect and print report but do not post to webhook
```

## Detection Logic

### Phase 4 — Policy Decision Analysis

govbot counts policy decisions by resource over the look-back window:

| Column   | Meaning |
|----------|---------|
| `allow`  | Decisions permitted by a matching rule |
| `deny`   | Decisions denied by a matching rule |
| `req_apr`| Decisions routed to the approval workflow |
| `no_match` ⚠ | Decisions where no policy rule matched — likely missing infrastructure tags |

A non-zero `no_match` count raises a **warning**. This usually means the
infrastructure config (`infraConfig`) does not contain the database host
the agent is connecting to, so the policy engine has no tags to match on.

### Phase 5 — Stale Pending Approvals

Any approval request that has been pending for more than **30 minutes**
is flagged as stale. This indicates the approver notification (Slack/email)
may not have reached its destination. A stale approval count raises a
**warning**.

### Phase 6 — Chain Integrity

govbot calls the audit hash chain verification endpoint. A chain failure
raises an **alert** (not just a warning) and is included in the Slack
message regardless of webhook configuration being enabled.

## Sample Run

```
[boris@ ~/helpdesk]$ go run ./cmd/govbot/ -gateway http://localhost:8080 -since 1h

[09:00:01] ── Phase 1: Governance Status ────────────────────────────
[09:00:01] Audit enabled:     true  (1247 total events)
[09:00:01] Chain valid:       true
[09:00:01] Last event:        2026-02-20T08:59:43Z
[09:00:01] Policy enabled:    true  (2 policies, 5 rules)
[09:00:01] Pending approvals: 0
[09:00:01] Approval notify:   webhook=true  email=false

[09:00:01] ── Phase 2: Policy Overview ──────────────────────────────
[09:00:01]   [enabled] production-databases
[09:00:01]              Restricts access to production database servers
[09:00:01]              Resources: database/prod-db, database/analytics-db
[09:00:01]              read, write                    → allow  [tag:env=production]
[09:00:01]              destructive                    → require_approval  [tag:env=production]
[09:00:01]   [enabled] kubernetes-namespaces
[09:00:01]              Controls K8s namespace access by environment
[09:00:01]              Resources: kubernetes/production
[09:00:01]              read                           → allow
[09:00:01]              write, destructive             → require_approval

[09:00:01] ── Phase 3: Audit Activity (last 1h) ────────────────────
[09:00:01] Events fetched:    42
[09:00:01]   policy_decision                   38
[09:00:01]   tool_call                          2
[09:00:01]   tool_result                        2

[09:00:02] ── Phase 4: Policy Decision Analysis ────────────────────
[09:00:02] Resource                                   allow    deny  req_apr no_match
[09:00:02] ────────────────────────────────────────────────────────────────────────
[09:00:02] database/prod-db                              35       0        3       0
[09:00:02] ────────────────────────────────────────────────────────────────────────
[09:00:02] TOTAL                                         35       0        3       0

[09:00:02] ── Phase 5: Pending Approvals ───────────────────────────
[09:00:02] No pending approvals

[09:00:02] ── Phase 6: Chain Integrity ──────────────────────────────
[09:00:02] Chain status:  ✓ VALID
[09:00:02] Total events:  1247

[09:00:02] ── Phase 7: Compliance Summary ───────────────────────────
[09:00:02] Overall status: ✓ HEALTHY
[09:00:02] No alerts or warnings. Governance posture is healthy.
```

## Sample Run: With Warnings

When infrastructure tags are missing or approvals go stale:

```
[09:00:02] ── Phase 7: Compliance Summary ───────────────────────────
[09:00:02] Overall status: ⚠ WARNINGS

[09:00:02] Warnings (2):
[09:00:02]   - 8 policy decisions matched no rule (policy_name=default) — likely missing tags in infrastructure config
[09:00:02]   - 1 approval request(s) have been pending for over 30m0s — approvers may not have been notified
```

To fix the `no_match` warning, ensure the database host in the agent's
connection string matches a `db_servers` entry in the infrastructure config
(see [Policy Configuration](../../deploy/helm/README.md#93-policy-configuration)).

## Running in Docker Compose

govbot runs as a one-shot command under the `governance` profile:

```bash
# On-demand compliance report
docker compose --profile governance run govbot

# With Slack webhook
GOVBOT_WEBHOOK=https://hooks.slack.com/... \
  docker compose --profile governance run govbot

# Custom look-back window (last 6 hours)
GOVBOT_SINCE=6h docker compose --profile governance run govbot
```

## Running in Kubernetes

govbot is deployed as a Kubernetes **CronJob** that runs on a configurable
schedule. Enable it in `values.yaml`:

```yaml
governance:
  govbot:
    enabled: true
    schedule: "0 8 * * *"   # daily at 08:00 UTC
    since: "24h"
    webhook: "https://hooks.slack.com/services/..."
```

Then upgrade the Helm release:

```bash
helm upgrade helpdesk ./deploy/helm/helpdesk -f values.yaml
```

To trigger an immediate run outside the schedule:

```bash
kubectl create job govbot-manual \
  --from=cronjob/helpdesk-govbot
```

View the report output:

```bash
kubectl logs -l app.kubernetes.io/component=govbot --tail=200
```

## Integration with AI Governance

govbot is the compliance layer of the AI Governance framework:

| Component  | Role |
|------------|------|
| **auditd** | Records every agent action in a tamper-evident hash chain |
| **auditor** | Real-time stream monitor — alerts on anomalies as they happen |
| **secbot** | Security responder — detects policy violations and creates incident bundles |
| **govbot** | Compliance reporter — periodic snapshot of overall governance posture |
| **approvals CLI** | Human-in-the-loop — approve or deny individual requests |

Run govbot daily to ensure policy coverage is complete and the audit chain
remains intact. Use the Slack webhook to route reports to your compliance
or security operations channel.
