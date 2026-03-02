# aiHelpDesk: Governance Compliance Reporter (govbot)

The govbot is a one-shot compliance reporter that queries the aiHelpDesk
Gateway's governance API endpoints and produces a structured compliance
snapshot. It is designed to run on-demand or on a schedule (e.g. daily
cron / Kubernetes CronJob) and optionally post a summary to a Slack webhook.

## 1. Architecture

```
Gateway /api/v1/governance/* → govbot → compliance report + optional Slack alert
```

The `govbot` is stateless and read-only. It contacts the Gateway over HTTP,
fetches governance state from the underlying audit daemon (auditd), and
prints a structured report to stdout. No audit socket access or cluster
privileges are required — only network access to the gateway.

## 2. Compliance Phases

govbot runs ten sequential phases and exits:

```
Phase  1 — Governance Status:         GET /api/v1/governance
Phase  2 — Policy Overview:           Detailed policy rule breakdown from Phase 1 data
Phase  3 — Audit Activity:            GET /api/v1/governance/events?since=...&limit=1000
Phase  4 — Policy Decision Analysis:  Per-resource allow/deny/no-match breakdown
Phase  5 — Agent Enforcement Coverage:Coverage by agent type
Phase  6 — Pending Approvals:         GET /api/v1/governance/approvals/pending
Phase  7 — Chain Integrity:           GET /api/v1/governance/verify
Phase  8 — Mutation Activity:         Write and destructive tool breakdown
Phase  9 — Policy Coverage Analysis:  tool_invoked vs policy_decision gap analysis
Phase 10 — Compliance Summary:        Aggregated alerts and warnings + optional Slack post
```

See [COMPLIANCE.md](../../COMPLIANCE.md) for a full description of each phase,
coverage gap detection, dead rule detection, and the historical trend block.

## 3. Exit Codes

| Code | Meaning |
|------|---------|
| `0`  | Healthy — no alerts or warnings |
| `1`  | Fatal error — could not reach gateway or Phase 1 failed |
| `2`  | Alerts present — chain integrity failure, policy bypass, or other critical finding |

Exit code `2` is useful for CI pipelines and cron alerting.

## 4. Command Line Flags

For details on how to run `govbot` in your specific deployment environment see [here](../../deploy/docker-compose/README.md#37-running-the-compliance-reporter-govbot) for running via Docker containers, [here](../../deploy/host#76-running-the-compliance-reporter-govbot) for running directly on a host and [here](../../deploy/helm/README.md#97-running-the-compliance-reporter-govbot) for running on K8s.

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
      Auditd service URL for persisting compliance history
      (e.g. http://auditd:1199). Reads HELPDESK_AUDIT_URL env var by default.
      Takes precedence over -history-db. Recommended for Docker/K8s deployments.
-history-db string
      Path to a local history database (SQLite file path or postgres:// DSN).
      Used when -audit-url is not set. Useful for standalone / air-gapped installs.
-history-retain int
      Maximum number of runs to keep when using -history-db (default 365).
      Ignored when -audit-url is set (auditd manages its own data lifecycle).
-show-history int
      Print the last N compliance runs as a table and exit.
      Requires -audit-url or -history-db. Does not contact the gateway.
```

## 5. Compliance History

govbot can persist a snapshot of each run to enable trend analysis across
multiple runs. Two storage backends are supported:

| Backend | Flag | Best for |
|---------|------|----------|
| **auditd** (recommended) | `-audit-url` / `HELPDESK_AUDIT_URL` env var | Docker Compose, Kubernetes, aiHelpDesk-as-a-Service |
| **Local SQLite / PostgreSQL** | `-history-db` | Standalone installs, air-gapped environments |

The auditd backend stores runs in the shared audit database — no extra volume
or database required. Multiple govbot instances (one per team) can write to the
same central auditd, and the gateway exposes runs at
`GET /api/v1/governance/govbot/runs`.

### 5.1 Enabling persistence

```bash
# Via auditd (default in Docker / K8s — set by HELPDESK_AUDIT_URL env var)
go run ./cmd/govbot -gateway http://localhost:8080 \
    -audit-url http://localhost:1199

# Local SQLite (development / standalone)
go run ./cmd/govbot -gateway http://localhost:8080 \
    -history-db /var/lib/govbot/history.db
```

When history is configured, a **Historical Trend** block is
appended to the Phase 9 Compliance Summary after at least two prior runs
have been recorded for the same look-back window:

```
[09:00:03] Historical Trend (last 30 daily runs):
[09:00:03]   Status:     healthy 28  warnings 2  alerts 0
[09:00:03]   Denials:    avg 0.4/run   today 2   ↑ above avg
[09:00:03]   Mutations:  avg 12/run    today 17  ↑ above avg
[09:00:03]   Chain:      valid 30/30
[09:00:03]   Stale apr:  avg 0.1/run   today 0
```

### 5.2 Browsing history

```bash
# Last 10 runs from auditd (no gateway contact)
go run ./cmd/govbot -audit-url http://localhost:1199 -show-history 10

# Last 10 runs from a local database
go run ./cmd/govbot -history-db /var/lib/govbot/history.db -show-history 10
```

Output:

```
Run at (UTC)          Window  Status    Denies   Muts  Chain   Alerts  Warnings
───────────────────────────────────────────────────────────────────────────────
2026-03-01 09:00      24h     HEALTHY        2     17      ✓        0         0
2026-02-28 09:00      24h     WARNINGS       0     12      ✓        0         1
2026-02-27 09:00      24h     HEALTHY        0      9      ✓        0         0
```

### 5.3 Docker Compose

History is stored automatically in auditd via the `HELPDESK_AUDIT_URL`
environment variable injected by the compose file. No extra volume is needed.

```bash
# First run — no trend block yet
docker compose --profile governance run govbot

# Subsequent runs — trend block appears after Phase 9
docker compose --profile governance run govbot
```

### 5.4 Kubernetes (Helm)

History is stored in auditd by default — no PVC is required. Enable the
CronJob in `values.yaml`:

```yaml
governance:
  govbot:
    enabled: true
    schedule: "0 8 * * *"
```

For a local SQLite or PostgreSQL database (air-gapped / standalone installs):

```yaml
governance:
  govbot:
    enabled: true
    historyDB: "/var/lib/govbot/history.db"   # SQLite
    # historyDB: "postgres://govbot:pass@pg/govbot_history"  # PostgreSQL
    # When historyDB is set, HELPDESK_AUDIT_URL is not injected automatically.
```

## 6. Detection Logic

### 6.1 Phase 4 — Policy Decision Analysis

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

### 6.2 Phase 5 — Stale Pending Approvals

Any approval request that has been pending for more than **30 minutes**
is flagged as stale. This indicates the approver notification (Slack/email)
may not have reached its destination. A stale approval count raises a
**warning**.

### 6.3 Phase 6 — Chain Integrity

govbot calls the audit hash chain verification endpoint. A chain failure
raises an **alert** (not just a warning) and is included in the Slack
message regardless of webhook configuration being enabled.

## 7. Sample Run

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

## 8. Sample Run: With Warnings

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
(see [Policy Configuration](../../deploy/helm/README.md#94-policy-configuration)).

Also see [this](GOVBOT_SAMPLE.md) as a complete sample run of the `govbot` Compliance Reporter.

## 9. Running in Docker Compose

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

## 10. Running in Kubernetes

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

## 11. Integration with AI Governance

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
