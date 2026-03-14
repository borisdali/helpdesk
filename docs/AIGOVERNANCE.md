# aiHelpDesk AI Governance Architecture

Please see [here](ARCHITECTURE.md) for the general overview of
aiHelpDesk Architecture. This page presents a part of this architecture
dedicated to aiHelpDesk's critical subsystem that we refer to as AI Governance.

## 1. Overview

As aiHelpDesk evolves from read-only diagnostics to actively *fixing* infrastructure
issues, governance becomes critical for trust. The AI Governance system ensures that
when aiHelpDesk agents are instructed to remedy a problem and so they have to modify
databases, scale deployments, or restart services, they do so safely, accountably,
and with appropriate human oversight.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           AI GOVERNANCE LAYERS                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │   POLICY     │  │   APPROVAL   │  │  GUARDRAILS  │  │    AUDIT     │     │
│  │   ENGINE     │  │   WORKFLOWS  │  │   & LIMITS   │  │   SYSTEM     │     │
│  │              │  │              │  │              │  │              │     │
│  │ What's       │  │ Human-in-    │  │ Hard safety  │  │ Tamper-proof │     │
│  │ allowed?     │  │ the-loop     │  │ constraints  │  │ record       │     │
│  └──────────────┘  └──────────────┘  └──────────────┘  └──────────────┘     │
│         │                 │                 │                 │             │
│         └─────────────────┴─────────────────┴─────────────────┘             │
│                                    │                                        │
│                           Enforcement Point                                 │
│                          (before tool execution)                            │
│                                                                             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │  IDENTITY    │  │  EXPLAIN-    │  │   ROLLBACK   │  │  COMPLIANCE  │     │
│  │  & ACCESS    │  │  ABILITY     │  │   & UNDO     │  │  REPORTING   │     │
│  │              │  │              │  │              │  │              │     │
│  │ Who can do   │  │ Why did AI   │  │ Recover from │  │ Prove it     │     │
│  │ what?        │  │ decide this? │  │ mistakes     │  │ works        │     │
│  └──────────────┘  └──────────────┘  └──────────────┘  └──────────────┘     │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 2. Components

aiHelpDesk Governance consists of eight well-defined components:

| Component | Status | Description |
|-----------|--------|-------------|
| [Audit System](#7-audit-system) | **Implemented** | Tamper-evident logging with hash chains |
| [Policy Engine](#3-policy-engine) | **Implemented** | Rule-based access control |
| [Approval Workflows](#4-approval-workflows) | **Implemented** | Human-in-the-loop for risky ops |
| [Compliance Reporting](#8-compliance-reporting-cmdgovbot) | **Implemented** | Scheduled compliance snapshots and alerting |
| [Guardrails](#5-guardrails) | **Implemented** | 4 guardrails: DB/K8s blast radius, transaction age, schedule; rate limits and circuit breaker planned |
| [Operating Mode](#6-operating-mode) | **Implemented** | `fix` mode enforces all governance modules at startup; violations generate compliance alerts and incidents |
| [Explainability](#9-explainability) | **Implemented** | Decision trace, human-readable explanations, `govexplain` query interface |
| [Identity & Access](#10-identity--access) | **Implemented** | Three-dimension access control: role, data sensitivity, and purpose |
| Rollback & Undo | Planned | Recovery from mistakes |

---

## 3. Policy Engine

The Policy Engine defines what actions are allowed, by whom, on which resources,
and under what conditions. It is the foundation for all other governance controls.

### 3.1 Policy Structure

```yaml
# /etc/helpdesk/policies.yaml
version: "1"

policies:
  # Policy for production databases
  - name: production-database-protection
    description: Restrict operations on production databases

    # Which resources this policy applies to
    resources:
      - type: database
        match:
          tags: [production]

    # Rules evaluated in order (first match wins)
    rules:
      - action: read
        effect: allow

      - action: write
        effect: allow
        conditions:
          require_approval: true
          max_rows_affected: 1000

      - action: destructive
        effect: deny
        message: "Destructive operations on production databases are prohibited"

  # Time-based restrictions
  - name: change-freeze
    description: No changes during peak hours
    resources:
      - type: database
        match: { tags: [production] }
    rules:
      - action: [write, destructive]
        effect: deny
        conditions:
          schedule:
            days: [mon, tue, wed, thu, fri]
            hours: [9, 10, 11, 12, 13, 14, 15, 16]
            timezone: America/New_York
        message: "Changes not allowed during business hours"

  # Role-based permissions
  - name: dba-privileges
    description: DBAs can perform write operations
    principals:
      - role: dba
    resources:
      - type: database
    rules:
      - action: [read, write]
        effect: allow
      - action: destructive
        effect: allow
        conditions:
          require_approval: true
          approval_quorum: 2
```

### 3.2 Policy Evaluation Flow

```
Request arrives
      │
      ▼
┌─────────────┐
│ Identify    │ ─── Who is making the request?
│ Principal   │     (user, service account, agent)
└─────────────┘
      │
      ▼
┌─────────────┐
│ Identify    │ ─── What resource is affected?
│ Resource    │     (database, k8s cluster, table)
└─────────────┘
      │
      ▼
┌─────────────┐
│ Classify    │ ─── read, write, or destructive?
│ Action      │
└─────────────┘
      │
      ▼
┌─────────────┐
│ Evaluate    │ ─── First matching rule wins
│ Rules       │     ALLOW / DENY / REQUIRE_APPROVAL
└─────────────┘
      │
      ├── ALLOW ──────────────► Proceed to execution
      ├── DENY ───────────────► Return error, audit denial
      └── REQUIRE_APPROVAL ───► Enter approval workflow
```

### 3.3 Environment Variables

```bash
export HELPDESK_POLICY_FILE="/etc/helpdesk/policies.yaml"
export HELPDESK_DEFAULT_POLICY="deny"      # When no policy matches
export HELPDESK_POLICY_DRY_RUN="true"      # Log decisions but don't enforce
```

### 3.4 Implementation

The policy engine is implemented in `internal/policy/`:

```go
import "helpdesk/internal/policy"

// Load policies from file
cfg, err := policy.LoadFile("/etc/helpdesk/policies.yaml")
if err != nil {
    log.Fatal(err)
}

// Create engine
engine := policy.NewEngine(policy.EngineConfig{
    PolicyConfig:  cfg,
    DefaultEffect: policy.EffectDeny,
    DryRun:        false,
})

// Evaluate a request
decision := engine.Evaluate(policy.Request{
    Principal: policy.RequestPrincipal{
        UserID: "alice@example.com",
        Roles:  []string{"dba"},
    },
    Resource: policy.RequestResource{
        Type: "database",
        Name: "prod-db",
        Tags: []string{"production"},
    },
    Action: policy.ActionWrite,
    Context: policy.RequestContext{
        RowsAffected: 50,
    },
})

// Check decision
if err := decision.MustAllow(); err != nil {
    switch e := err.(type) {
    case *policy.DeniedError:
        return fmt.Errorf("denied: %s", e.Decision.Message)
    case *policy.ApprovalRequiredError:
        return requestApproval(e.Decision)
    }
}
```

See [`policies.example.yaml`](policies.example.yaml) for a complete policy configuration example.

### 3.5 Agent Integration

Policy enforcement is integrated directly into the agents via `agentutil.PolicyEnforcer`.
Each agent initializes the policy engine at startup and checks policies before executing tools.

#### 3.5.1 Initialization (main.go)

```go
// Initialize policy engine if configured
policyEngine, err := agentutil.InitPolicyEngine(cfg)
if err != nil {
    slog.Error("failed to initialize policy engine", "err", err)
    os.Exit(1)
}
policyEnforcer = agentutil.NewPolicyEnforcer(policyEngine, traceStore)
```

#### 3.5.2 Tool Enforcement (tools.go)

```go
// Database agent example - before executing psql
if policyEnforcer != nil {
    if err := policyEnforcer.CheckDatabase(ctx, dbInfo.Name, policy.ActionRead, dbInfo.Tags); err != nil {
        slog.Warn("policy denied database access",
            "tool", toolName,
            "database", dbInfo.Name,
            "err", err)
        return "", fmt.Errorf("policy denied: %w", err)
    }
}

// Kubernetes agent example - before executing kubectl
if err := checkK8sPolicy(ctx, namespace, policy.ActionRead, nsInfo.Tags); err != nil {
    return result, fmt.Errorf("policy denied: %w", err)
}
```

#### 3.5.3 Resource Tags

Tags are resolved from the infrastructure configuration (`HELPDESK_INFRA_CONFIG`):

```json
{
  "db_servers": {
    "prod-db": {
      "name": "Production Database",
      "connection_string": "host=prod-db.example.com ...",
      "tags": ["production", "critical"],
      "k8s_namespace": "database-prod"
    },
    "staging-db": {
      "name": "Staging Database",
      "connection_string": "host=staging-db.example.com ...",
      "tags": ["staging"]
    }
  }
}
```

When an agent receives a request for `prod-db`, it:
1. Resolves the name to the connection string
2. Extracts tags (`["production", "critical"]`)
3. Checks policy with those tags
4. If allowed, executes the operation

---

## 4. Approval Workflows

When a policy rule has `effect: require_approval`, the agent blocks and waits
for a human to approve or deny the request before execution proceeds.

### 4.1 Flow

```
┌─────────────┐   require_approval   ┌─────────────────────┐
│   Agent     │─────────────────────►│   auditd            │
│  (blocked)  │                      │   /v1/approvals     │
└──────┬──────┘                      └──────────┬──────────┘
       │                                        │ Slack / email
       │                                        ▼
       │                             ┌─────────────────────┐
       │                             │   Approvers         │
       │                             │   (humans)          │
       │                             └──────────┬──────────┘
       │                                        │ POST /v1/approvals/{id}/approve
       │                                        │      or /deny
       │◄───────────────────────────────────────┘
       │     decision (allow / deny)
       ▼
┌─────────────┐
│   Execute   │
│  or Abort   │
└─────────────┘
```

### 4.2 Implementation

Approval state is managed by `auditd` and persisted in SQLite.
The agent's `agentutil.PolicyEnforcer` polls the auditd approval API
until the request is decided or the timeout elapses.

| Component | Location | Role |
|-----------|----------|------|
| Approval API | `cmd/auditd/` | Stores requests, exposes approve/deny endpoints |
| PolicyEnforcer | `agentutil/agentutil.go` | Blocks tool execution, polls for decision |
| Approvals CLI | `cmd/approvals/` | Human tool to list and decide pending requests |
| Notification | `cmd/auditd/` | Sends Slack webhook and/or email on new request |

### 4.3 Approvals CLI

Humans manage pending approvals with the `approvals` CLI:

```bash
# List all pending approval requests
approvals list --url http://localhost:1199

# Approve a specific request
approvals approve <approval-id> --url http://localhost:1199

# Deny a specific request
approvals deny <approval-id> --url http://localhost:1199
```
For details on how to run `approvals` in your specific deployment environment see [here](../deploy/docker-compose/README.md#34-managing-approvals) for running via Docker containers, [here](../deploy/host/README.md#73-managing-approvals) for running directly on a host and [here](../deploy/helm/README.md#94-approval-workflow) for running on K8s.

### 4.4 Approval API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/v1/approvals` | Create a new approval request (called by agent) |
| GET | `/v1/approvals` | List all approval requests |
| GET | `/v1/approvals/pending` | List only pending requests |
| POST | `/v1/approvals/{id}/approve` | Approve a request |
| POST | `/v1/approvals/{id}/deny` | Deny a request |

### 4.5 Configuration

```bash
# Timeout before an unanswered request is considered expired (default: 5m)
export HELPDESK_APPROVAL_TIMEOUT="15m"

# Slack webhook for approval notifications (optional)
export HELPDESK_APPROVAL_WEBHOOK="https://hooks.slack.com/..."

# Base URL embedded in approve/deny links sent via email or Slack
export HELPDESK_APPROVAL_BASE_URL="http://auditd.internal:1199"
```

Email notifications use the same SMTP settings as the auditor (see
[Environment Variables](#environment-variables) below).

### 4.6 Approval States

| State | Description |
|-------|-------------|
| `pending` | Awaiting approval decision |
| `approved` | Manually approved by human |
| `denied` | Rejected by approver |
| `expired` | Approval request timed out — agent receives a denial |

---

## 5. Guardrails

Guardrails are hard safety constraints enforced by the policy engine. Three are
quantitative limits (`max_*` conditions); one is a time-based gate (`schedule`).
All four are evaluated before the LLM receives any result.

| Guardrail | Policy condition | Applies to | Pre-exec | Post-exec |
|-----------|-----------------|------------|----------|-----------|
| **DB blast radius** | `max_rows_affected` | `run_query`, `cancel_query`, `terminate_connection`, `kill_idle_connections` | ✓ EXPLAIN estimate | ✓ command tag / function result |
| **K8s blast radius** | `max_pods_affected` | `delete_pod`, `restart_deployment`, `scale_deployment` | ✓ `scale_deployment` only (replica count known upfront) | ✓ `delete_pod`, `restart_deployment` (count from kubectl output) |
| **Transaction age** | `max_xact_age_secs` | `cancel_query`, `terminate_connection` | ✓ from `inspectConnection` before action | — |
| **Schedule** | `schedule` (days/hours/tz) | all write/destructive tools | ✓ timestamp check | — |

> **Blast-radius design note:** post-execution evaluation has important limitations for large
> DML, DDL statements, and distributed topologies. See [GOVPOSTEVAL.md](GOVPOSTEVAL.md)
> for a full analysis of trade-offs, the rollback problem, and the planned
> pre-execution COUNT estimation approach.

Configure guardrails in your policy file under a rule's `conditions`:

```yaml
rules:
  - action: [write, destructive]
    effect: allow
    conditions:
      max_rows_affected: 1000      # database: rows modified (DELETE/UPDATE/INSERT)
      max_pods_affected: 10        # kubernetes: resources created/configured/deleted
      max_xact_age_secs: 300       # block cancel/terminate when open txn > 5 min
      schedule:                    # only allow during business hours
        days: [mon, tue, wed, thu, fri]
        hours: [9, 10, 11, 12, 13, 14, 15, 16, 17]
        timezone: America/New_York
```

See [`policies.example.yaml`](policies.example.yaml) for a complete policy configuration example.

### 5.1 DB Blast Radius (`max_rows_affected`)

Caps rows modified by a single database operation. Enforced at two points:

```
Pre-execution:   EXPLAIN estimate  → deny if estimated rows > limit
                         │
                    tool executes
                         │
Post-execution:  parse command tag → deny if actual rows > limit
                 (DELETE N / UPDATE N / INSERT 0 N / terminated N)
                         │
                 ┌───────┴────────┐
                 │ within limit   │ exceeded limit
                 ▼                ▼
              return result    return error +
                               audit PostExecution: true denial
```

`cancel_query` and `terminate_connection` use `parsePgFunctionResult` (boolean
result of `pg_cancel_backend` / `pg_terminate_backend`). `kill_idle_connections`
uses `parseTerminatedCount` (integer from the `terminated | N` expanded row).

### 5.2 K8s Blast Radius (`max_pods_affected`)

Caps resources affected by a single Kubernetes operation. `scale_deployment`
enforces pre-execution only (replica count is known from `args.Replicas` before
kubectl runs). `delete_pod` and `restart_deployment` enforce post-execution
(count parsed from kubectl confirmation lines: `pod "x" deleted`,
`deployment "y" restarted`, etc.).

### 5.3 Transaction Age (`max_xact_age_secs`)

Blocks `cancel_query` and `terminate_connection` when the target session has
uncommitted writes in a transaction open longer than the configured limit.
Evaluated pre-execution via `inspectConnection` (which runs `get_session_info`
against the backend PID before any destructive action). Only fires when
`HasWrites = true` (i.e. `backend_xid IS NOT NULL`).

```yaml
conditions:
  max_xact_age_secs: 300   # block if open transaction with writes > 5 min
```

### 5.4 Schedule

Gates write and destructive operations to specific days and hours. Evaluated
pre-execution by checking `time.Now()` against the configured window. No
post-execution component — the operation is blocked before it starts.

```yaml
conditions:
  schedule:
    days: [mon, tue, wed, thu, fri]
    hours: [9, 10, 11, 12, 13, 14, 15, 16, 17]  # 9am–5pm
    timezone: America/New_York
```

### 5.5 Planned Guardrails

**Rate limits** — cap write frequency per session (e.g. max 20 writes/minute).
Requires a per-session counter with TTL; not yet implemented.

**Circuit breaker** — auto-pause an agent after N consecutive errors in a
rolling window to prevent runaway failure loops. Not yet implemented.

---

## 6. Operating Mode

The operating mode switch controls whether agents are allowed to execute
write and destructive tools at all, and enforces governance requirements
when they are.

| Mode | Description |
|------|-------------|
| `readonly` | **Default.** Write and destructive tools are disabled. Safe for diagnostics. |
| `fix` | Write and destructive tools are enabled. Governance (audit + policy) is required. |

```bash
export HELPDESK_OPERATING_MODE="readonly"  # default — safe
export HELPDESK_OPERATING_MODE="fix"       # enable mutations; enforces governance
```

### 6.1 Why a Default of `readonly`

Most day-to-day use is diagnostic — querying databases, inspecting pods,
gathering logs. Read-only mode ensures that newly deployed agents can never
accidentally mutate state until an operator explicitly opts in. When write tools
are added in the future, they will be silently gated behind this flag until
the operator is ready.

### 6.2 Startup Validation (fix mode)

When an agent starts in `fix` mode, it performs a pre-flight governance check
before registering any A2A skills:

```
Agent starts in fix mode
         │
         ▼
┌─────────────────────┐
│ Is policy enabled   │── No ──► stderr: "fix mode requires policy"
│ and loaded?         │         exit 1
└─────────┬───────────┘
          │ Yes
          ▼
┌─────────────────────┐
│ Is auditd reachable │── No ──► stderr: "fix mode requires audit"
│ (GET /health)?      │         exit 1
└─────────┬───────────┘
          │ Yes
          ▼
     Agent starts
```

This prevents a misconfigured deployment from silently operating without
governance. The failure is intentional: in `fix` mode, governance is
non-negotiable.

Five governance modules are validated at startup via `agentutil.EnforceFixMode`:

| Module | Severity | What is checked |
|--------|----------|-----------------|
| `audit` | **fatal** | `HELPDESK_AUDIT_ENABLED=true` and `HELPDESK_AUDIT_URL` set |
| `policy_engine` | **fatal** | `HELPDESK_POLICY_ENABLED=true` and `HELPDESK_POLICY_FILE` set |
| `guardrails` | **fatal** | `HELPDESK_POLICY_DRY_RUN` must not be `true` |
| `approval_workflows` | warning | `HELPDESK_APPROVAL_ENABLED=true` recommended |
| `explainability` | warning | `HELPDESK_INFRA_CONFIG` set (tag resolution for policy decisions) |

For every violation the agent:
1. Logs at `ERROR` (fatal) or `WARN` (warning) level
2. Best-effort POSTs a `governance_violation` audit event to auditd (if `HELPDESK_AUDIT_URL` is set)
3. Best-effort POSTs an incident to the gateway (if `HELPDESK_GATEWAY_URL` is set)

### 6.3 Runtime Enforcement

The mode check runs inside `PolicyEnforcer.CheckTool`, before the policy
engine is consulted, so it is invisible to tool authors. No tool code needs
to be mode-aware.

```
Tool called with ActionWrite or ActionDestructive
         │
         ▼
┌─────────────────────────┐
│ HELPDESK_OPERATING_MODE │
│       == readonly?      │── Yes ──► return error: "agent is in read-only mode"
└──────────┬──────────────┘
           │ No (fix mode)
           ▼
┌─────────────────────────┐
│ Audit reachable?        │── No ──► create governance incident (see below)
│ Policy loaded?          │         return error: "governance unavailable"
└──────────┬──────────────┘
           │ Yes
           ▼
     Normal policy evaluation
```

### 6.4 Governance Misconfiguration Incidents

In `fix` mode, if audit becomes unreachable at runtime (after successful
startup), the agent cannot use the audit trail to record a denial — because
the audit system is the problem. To break this chicken-and-egg deadlock, the
agent uses a two-stage fallback:

1. **POST `gateway /api/v1/incidents`** — create a security incident directly
   via the gateway, which routes it to the incident agent outside the
   broken audit path.
2. **Write to stderr** — ensure the violation is captured in container/system
   logs even if the gateway is also unreachable.

This guarantees that governance failures are always visible, even when the
audit system itself has failed.

### 6.5 Violation Types

| Condition | Severity | Action |
|-----------|----------|--------|
| `fix` mode + audit disabled or unreachable | **Critical** | Block tool, create incident, log stderr |
| `fix` mode + policy disabled or not loaded | **Critical** | Block tool, create incident, log stderr |
| `fix` mode + approval disabled for `destructive` action | Warning | Allow with log warning (governance gap, not a hard block) |

### 6.6 Implementation

Key integration points:

- `agentutil.CheckFixModeViolations(cfg)` — validates all five modules from `agentutil.Config`
- `agentutil.CheckFixModeAuditViolations(auditEnabled, auditURL)` — audit-only check for the orchestrator (which delegates policy enforcement to sub-agents)
- `agentutil.EnforceFixMode(ctx, violations, componentName, auditURL)` — logs, records `governance_violation` audit events, creates gateway incidents, and exits on fatal violations
- `agents/*/main.go` and `cmd/helpdesk/main.go` — call `EnforceFixMode` immediately after config loading, before any agent initialization

---

## 7. Audit System

The audit system records every tool execution, policy decision, delegation, and
gateway request into a tamper-evident, hash-chained log managed by `auditd`.

| Component | Location | Description |
|-----------|----------|-------------|
| `auditd` | `cmd/auditd/` | Central HTTP service; stores events, manages hash chain, serves approval and governance APIs |
| `auditor` | `cmd/auditor/` | Real-time monitoring CLI; reads the Unix socket, fires security alerts, verifies chain integrity |
| `secbot` | `cmd/secbot/` | Automated incident responder; listens to the audit socket and creates incident bundles via the gateway |
| `audit` package | `internal/audit/` | Core event types, hash chain implementation, store, trace middleware |

For the full API reference, event schema, auditor flags, environment variables,
and troubleshooting guide see **[AUDIT.md](AUDIT.md)**.

### 7.1 secbot — Security Responder

`secbot` subscribes to the auditd Unix socket and automatically creates incident
bundles when it detects critical security patterns:

| Pattern | Trigger |
|---------|---------|
| `hash_mismatch` | Event hash doesn't match content |
| `unauthorized_destructive` | Destructive action without approval |
| `potential_sql_injection` | SQL syntax errors in tool output |
| `potential_command_injection` | Permission denied / command not found errors |

```bash
go run ./cmd/secbot/ \
  --socket /tmp/helpdesk-audit.sock \
  --gateway http://localhost:8080 \
  --cooldown 5m

# Dry-run (log alerts, don't create incidents)
go run ./cmd/secbot/ --socket /tmp/helpdesk-audit.sock --dry-run
```

For deployment-specific instructions see:
[Docker Compose](../deploy/docker-compose/README.md#38-security-responder-secbot) ·
[Host](../deploy/host/README.md#77-security-responder-secbot) ·
[Helm](../deploy/helm/README.md#98-security-responder-secbot)

## 8. Compliance Reporting (cmd/govbot/)

The `govbot` is a one-shot compliance reporter that queries the gateway's
governance API endpoints and produces a structured compliance snapshot. It
is designed to run on-demand or on a schedule (daily cron / Kubernetes CronJob)
and optionally post a summary to a Slack webhook.

```
Gateway /api/v1/governance/* → govbot → compliance report + optional Slack alert
```

govbot is stateless and read-only. No audit socket access or cluster privileges
are required — only network access to the gateway.

For the full compliance architecture — tool invocation instrumentation, policy
coverage gap analysis, dead rule detection, compliance history, and the
historical trend block — see **[COMPLIANCE.md](COMPLIANCE.md)**.

### 8.1 Compliance Phases

```
Phase  1 — Governance Status
Phase  2 — Policy Overview
Phase  3 — Audit Activity
Phase  4 — Policy Decision Analysis
Phase  5 — Agent Enforcement Coverage
Phase  6 — Pending Approvals
Phase  7 — Chain Integrity
Phase  8 — Mutation Activity
Phase  9 — Policy Coverage Analysis   (tool_invoked vs policy_decision gaps)
Phase 10 — Compliance Summary
```

### 8.2 Exit Codes

| Code | Meaning |
|------|---------|
| `0`  | Healthy — no alerts or warnings |
| `1`  | Fatal — could not reach gateway |
| `2`  | Alerts present — chain integrity failure or other critical finding |

Exit code `2` is useful for CI pipelines and cron alerting.

### 8.3 Detection Logic

**Phase 4 — no_match decisions:** When an agent connects to a database host
that is not listed in `infraConfig`, the policy engine has no tags to evaluate
and falls back to the default deny. These decisions are counted as `no_match`
and raise a warning. The fix is to ensure every host the agents contact appears
in the infrastructure config with appropriate tags.

**Phase 5 — Stale approvals:** Any approval request pending for more than
30 minutes is flagged as stale, indicating the notification channel may not
be working correctly.

**Phase 6 — Chain integrity:** A broken hash chain raises an **alert** (exit 2).

See more on `secbot` [here](../cmd/secbot/README.md).

### 8.4 Running govbot

```bash
# On-demand
go run ./cmd/govbot/ -gateway http://localhost:8080

# Custom look-back window
go run ./cmd/govbot/ -gateway http://localhost:8080 -since 6h

# With Slack reporting
go run ./cmd/govbot/ -gateway http://localhost:8080 \
  -webhook https://hooks.slack.com/services/...

# Docker Compose (governance profile)
docker compose --profile governance run govbot

# Kubernetes — trigger a one-off run outside the CronJob schedule
kubectl create job govbot-manual --from=cronjob/helpdesk-govbot
```

For details on how to run `govbot` in your specific deployment environment see [here](../deploy/docker-compose/README.md#37-running-the-compliance-reporter-govbot) for running via Docker containers, [here](../deploy/host#76-running-the-compliance-reporter-govbot) for running directly on a host and [here](../deploy/helm/README.md#97-running-the-compliance-reporter-govbot) for running on K8s.

See this [sample](GOVBOT_SAMPLE.md) of running `govbot` on demand.

### 8.5 Scheduling in Kubernetes

govbot is deployed as a CronJob. Enable it in `values.yaml`:

```yaml
governance:
  govbot:
    enabled: true
    schedule: "0 8 * * *"   # daily at 08:00 UTC
    since: "24h"
    webhook: "https://hooks.slack.com/services/..."
```

See [cmd/govbot/README.md](../cmd/govbot/README.md) for the full documentation.

---

## 9. Explainability

Explainability gives users and operators a clear, structured answer to three
questions about any policy decision:

1. **Why was my access denied (or allowed)?** — inline explanation at the point of the decision
2. **Why was event X denied?** — retrospective lookup against the audit trail
3. **What would happen if I tried action Y?** — hypothetical dry-run without execution

Today the audit trail records the *outcome* of a policy decision (`effect`,
`policy_name`, `message`). What is missing is the *evaluation trace* — the
step-by-step record of which policies and rules were considered, why each was
skipped or matched, and which conditions passed or failed. Without the trace
the `message` field is the only signal, and it only describes the matched rule,
not the full reasoning path.

### 9.1 Decision Trace

The policy `engine.evaluate()` loops through policies and rules silently today.
The design adds an `Explain(req Request) DecisionTrace` method alongside
`Evaluate`, recording each step:

```go
// DecisionTrace is the full evaluation record for a single request.
type DecisionTrace struct {
    Decision          Decision        // final outcome
    PoliciesEvaluated []PolicyTrace   // one entry per policy in the config
    DefaultApplied    bool            // true when no policy matched at all
    Explanation       string          // human-readable summary (see below)
}

// PolicyTrace records what happened for a single policy.
type PolicyTrace struct {
    PolicyName string      // policy name
    Matched    bool        // true if resource + principal matched this policy
    SkipReason string      // if !Matched: "disabled", "principal_mismatch", "resource_mismatch"
    Rules      []RuleTrace // only populated when Matched == true
}

// RuleTrace records what happened for a single rule within a matched policy.
type RuleTrace struct {
    Index      int              // position in the rules list (0-based)
    Actions    []string         // actions this rule applies to
    Effect     string           // allow / deny / require_approval
    Matched    bool             // true if this rule produced the final decision
    SkipReason string           // if !Matched: "action_mismatch", "schedule_inactive"
    Conditions []ConditionTrace // populated for the matching rule
}

// ConditionTrace records whether a single condition passed or failed.
type ConditionTrace struct {
    Name   string // "max_rows_affected", "require_approval", "schedule", ...
    Passed bool
    Detail string // e.g. "rows_affected=1500 > limit=1000"
}
```

Only the **matching** rule records conditions. Skipped rules record only the
reason they were skipped, keeping the trace concise.

### 9.2 Human-Readable Explanation

`DecisionTrace.Explanation` is generated from the trace by a pure function,
`buildExplanation(req, trace)`. Example outputs:

**Denied by blast radius:**
```
Access to prod-db (tags: production, critical) for write: DENIED

Policy "production-database-protection" matched (type=database, tag=production):
  Rule 0  read → allow         skipped — action is write
  Rule 1  write → allow        matched
    ✗ max_rows_affected: 1500 rows affected, limit is 1000
  → DENY

To proceed, reduce the scope of the operation so fewer than 1000 rows are affected.
```

**Denied by explicit rule:**
```
Access to prod-db (tags: production) for destructive: DENIED

Policy "production-database-protection" matched (type=database, tag=production):
  Rule 0  read → allow         skipped — action is destructive
  Rule 1  write → allow        skipped — action is destructive
  Rule 2  destructive → deny   matched
    Message: "Destructive operations on production databases are prohibited"
  → DENY

This rule cannot be overridden by approval.
```

**Allowed with approval required:**
```
Access to prod-db (tags: production) for write: REQUIRES APPROVAL

Policy "production-database-protection" matched (type=database, tag=production):
  Rule 0  read → allow         skipped — action is write
  Rule 1  write → allow        matched
    ✓ max_rows_affected: 50 rows affected, limit is 1000
    ✓ require_approval (quorum: 1)
  → REQUIRE_APPROVAL

An approval request has been created. Use `approvals list` to see pending requests.
```

**No policy matched (default deny):**
```
Access to unknown-db (tags: none) for write: DENIED

No policy matched this resource — default effect is deny.

This usually means the database is not listed in the infrastructure config
and therefore has no tags. Add it to HELPDESK_INFRA_CONFIG with appropriate
tags so a policy can be applied.
```

### 9.3 Surfacing the Explanation

The explanation is surfaced in three ways:

#### 9.3.1 Inline at the point of denial

`PolicyEnforcer.CheckTool` (and `CheckDatabase`, `CheckKubernetes`) today returns
a terse error string. With explainability, the denial error wraps the full
`DecisionTrace`. The LLM receives the human-readable explanation directly and
can relay it to the user without any secondary lookup.

```go
// DeniedError gains the trace
type DeniedError struct {
    Decision Decision
    Trace    DecisionTrace // new
}

func (e *DeniedError) Error() string {
    return e.Trace.Explanation // replaces the terse one-liner
}
```

#### 9.3.2 Retrospective — explain a past audit event

```bash
# CLI
govexplain --gateway http://localhost:8080 --event tool_a1b2c3d4

# API
GET /api/v1/governance/events/tool_a1b2c3d4/explain
```

The gateway retrieves the stored `DecisionTrace` from the audit event and
returns it. No re-evaluation is needed — the trace was recorded at the time.

#### 9.3.3 Hypothetical — what would happen if?

```bash
# CLI
govexplain --gateway http://localhost:8080 \
  --resource database:prod-db \
  --action write \
  --tags production,critical

# API
GET /api/v1/governance/explain?resource_type=database&resource_name=prod-db&action=write&tags=production,critical
```

The gateway calls `engine.Explain()` in dry-run mode with the provided
parameters. No audit event is written, no tool is executed.

### 9.4 Audit Enrichment

The `PolicyDecision` audit struct gains two fields:

```go
type PolicyDecision struct {
    // existing fields unchanged ...
    ResourceType  string         `json:"resource_type"`
    ResourceName  string         `json:"resource_name"`
    Action        string         `json:"action"`
    Tags          []string       `json:"tags,omitempty"`
    Effect        string         `json:"effect"`
    PolicyName    string         `json:"policy_name"`
    RuleIndex     int            `json:"rule_index,omitempty"`
    Message       string         `json:"message,omitempty"`
    Note          string         `json:"note,omitempty"`
    DryRun        bool           `json:"dry_run,omitempty"`
    PostExecution bool           `json:"post_execution,omitempty"`

    // new fields:
    Trace       *policy.DecisionTrace `json:"trace,omitempty"`       // full evaluation trace
    Explanation string                `json:"explanation,omitempty"` // human-readable summary
}
```

The trace is always stored, regardless of the decision outcome — allowed
decisions are as important to explain as denied ones.

### 9.5 Gateway API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/governance/explain` | Hypothetical check — what would happen? |
| GET | `/api/v1/governance/events/{id}/explain` | Explain a specific past audit event |

#### 9.5.1 Hypothetical check request parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `resource_type` | yes | `database`, `kubernetes` |
| `resource_name` | yes | Resource name (db name, namespace) |
| `action` | yes | `read`, `write`, `destructive` |
| `tags` | no | Comma-separated tags, e.g. `production,critical` |
| `user_id` | no | Evaluate as a specific user |
| `role` | no | Evaluate with a specific role |

#### 9.5.2 Response format (both endpoints)

```json
{
  "decision": {
    "effect": "deny",
    "policy_name": "production-database-protection",
    "rule_index": 2,
    "message": "Destructive operations on production databases are prohibited"
  },
  "policies_evaluated": [
    {
      "policy_name": "production-database-protection",
      "matched": true,
      "rules": [
        { "index": 0, "actions": ["read"],        "effect": "allow", "matched": false, "skip_reason": "action_mismatch" },
        { "index": 1, "actions": ["write"],       "effect": "allow", "matched": false, "skip_reason": "action_mismatch" },
        { "index": 2, "actions": ["destructive"], "effect": "deny",  "matched": true,
          "conditions": [] }
      ]
    },
    {
      "policy_name": "change-freeze",
      "matched": false,
      "skip_reason": "resource_mismatch"
    }
  ],
  "default_applied": false,
  "explanation": "Access to prod-db (tags: production) for destructive: DENIED\n\nPolicy \"production-database-protection\" matched ..."
}
```

### 9.6 govexplain CLI

A lightweight CLI binary for humans and scripts (see [here](GOVEXPLAIN.md) for the full reference):

```bash
# Hypothetical — test a permission before attempting an action
govexplain \
  --gateway http://localhost:8080 \
  --resource database:prod-db \
  --action write \
  --tags production,critical

# Retrospective — understand why a past event was denied
govexplain \
  --gateway http://localhost:8080 \
  --event tool_a1b2c3d4

# JSON output for scripting
govexplain --gateway http://localhost:8080 \
  --resource database:prod-db --action write \
  --json
```

Exit codes: `0` = allowed, `1` = denied, `2` = requires approval, `3` = error.

For details on how to run `govexplain` in your specific deployment environment see [here](../deploy/docker-compose/README.md#35-explaining-policy-decisions-govexplain) for running via Docker containers, [here](../deploy/host#74-explaining-policy-decisions-govexplain) for running directly on a host and [here](../deploy/helm/README.md#95-explaining-policy-decisions-govexplain) for running on K8s.

### 9.7 Implementation Plan

Changes are additive — no existing behaviour changes:

| Component | Change | Location |
|-----------|--------|----------|
| Policy engine | Add `Explain(req) DecisionTrace`; instrument `evaluate()` to record trace | `internal/policy/engine.go` |
| Policy types | Add `DecisionTrace`, `PolicyTrace`, `RuleTrace`, `ConditionTrace` | `internal/policy/types.go` |
| Policy engine | Add `buildExplanation(req, trace) string` | `internal/policy/explain.go` (new file) |
| Audit types | Add `Trace` and `Explanation` to `PolicyDecision` | `internal/audit/event.go` |
| agentutil | Call `Explain` instead of `Evaluate`; populate audit fields; enrich `DeniedError` | `agentutil/agentutil.go` |
| Gateway | Add two explain endpoints; call auditd for event lookup | `cmd/gateway/` |
| govexplain CLI | New binary — thin HTTP client for the two gateway endpoints | `cmd/govexplain/` |

The largest single change is instrumenting `evaluate()` to record the trace
without altering its return value. The trace is built as a side-effect,
collected into a `DecisionTrace` that is returned alongside the `Decision`
from `Explain()`. `Evaluate()` calls `Explain()` and discards the trace,
preserving full backwards compatibility.

---

## 10. Identity & Access

The Identity & Access sub-module of aiHelpDesk AI Governance module answers two specific questions:

1. **Who is making this request?** — verified identity, not a header anyone can set
2. **Why are they making it?** — declared purpose, not just what they're allowed to do

These two questions, combined with the existing resource tag system, produce the
three-dimension access control model that the policy engine's `principals` block
is designed to support.

### 10.1 Why Role-Based Access Alone Is Insufficient

The policy engine supports principal matching, e.g. `role: dba`, `user: alice@example.com`,
`service: srebot`. This alone however is not sufficient or otherwise the policy engine
would just receive an empty `RequestPrincipal{}` because no code path connects, resolves
and propagates identity. This is the *plumbing* gap.

Beyond the plumbing, RBAC has a deeper limitation for agent systems.
An agent combines data sources and acts at scale in ways a human user does not —
and the same data can be legitimately accessed for one reason and not for another.
A DBA role authorizes access to production databases, but it does not authorize
bulk-exporting customer records for data analysis. The same access, the same role,
different purposes — and policy must be able to distinguish them.

The three access control dimensions this sub-module adds:

| Dimension | Without this sub-module | With this sub-module |
|-----------|--------------|-------------|
| **Role** | Defined in policy YAML, never populated in requests | Full resolution: identity provider → verified user → roles |
| **Data sensitivity** | Resource tags exist (`production`, `staging`) but no sensitivity classification | Explicit sensitivity class per resource: `pii`, `sensitive`, `internal`, `public`, `critical` |
| **Purpose** | Absent | Declared per request; enforced as a policy condition alongside role and sensitivity |

---

### 10.2 Identity Provider

Authentication happens at the Gateway — the single entry point for all requests.
The Gateway instantiates an identity provider, resolves a `ResolvedPrincipal` for
every incoming request, and attaches it to the `TraceContext` that flows through
the rest of the stack.

Three provider modes are supported, configured via `HELPDESK_IDENTITY_PROVIDER`:

| Mode | Use case | Mechanism |
|------|----------|-----------|
| `none` | Default — backwards compatible | `X-User` header accepted as-is; no validation; no role resolution |
| `static` | Self-hosted / simple deployments | Users, roles, and service account API keys declared in `users.yaml` |
| `jwt` | Orgs with SSO (Okta, Auth0, Azure AD, Google) | JWT validated against JWKS endpoint; roles extracted from a configured claim |

`none` preserves the behaviour before the Identity & Access sub-module was
introduced. Setting it to `none` allows existing deployments continue
to work without any configuration change.

#### 10.2.1 Go Interface

```go
// Package identity resolves verified principals from incoming requests.
package identity

import "net/http"

// Provider authenticates a request and returns the resolved principal.
// It is called by the Gateway on every incoming request.
type Provider interface {
    // Resolve extracts and verifies identity from the HTTP request.
    // Returns an error if authentication fails (wrong key, invalid token, etc.).
    // In "none" mode, Resolve never returns an error — it always succeeds.
    Resolve(r *http.Request) (ResolvedPrincipal, error)
}

// ResolvedPrincipal is the verified identity attached to a request.
// Created by the Gateway and propagated through every downstream call.
type ResolvedPrincipal struct {
    UserID     string   // Verified user ID (email, JWT sub, service account name)
    Roles      []string // Resolved roles (from users.yaml or JWT claim)
    Service    string   // Non-empty if this is a service account (e.g., "srebot")
    AuthMethod string   // "api_key", "jwt", "header" (legacy no-auth)
}

// IsAnonymous returns true when identity was not verified (AuthMethod == "header").
func (p ResolvedPrincipal) IsAnonymous() bool {
    return p.AuthMethod == "header"
}
```

#### 10.2.2 Static Identity Provider

Configured via `HELPDESK_USERS_FILE`:

```yaml
# /etc/helpdesk/users.yaml
version: "1"

users:
  - id: alice@example.com
    roles: [dba, sre]

  - id: bob@example.com
    roles: [sre]

  - id: carol@example.com
    roles: [developer]

service_accounts:
  - id: srebot
    roles: [sre-automation]
    api_key_hash: "$argon2id$v=19$m=65536,t=1,p=4$..."   # hash of the API key

  - id: secbot
    roles: [security-automation]
    api_key_hash: "$argon2id$v=19$m=65536,t=1,p=4$..."
```

Human users authenticate via `X-User: alice@example.com` header (unverified
in `none` mode; cross-referenced against `users.yaml` in `static` mode —
users not in the file are rejected).

Service accounts authenticate via `Authorization: Bearer <api-key>`. The key
is hashed with Argon2id and compared against `api_key_hash`.

#### 10.2.3 JWT Identity Provider

```bash
export HELPDESK_IDENTITY_PROVIDER="jwt"
export HELPDESK_JWT_JWKS_URL="https://idp.example.com/.well-known/jwks.json"
export HELPDESK_JWT_ISSUER="https://idp.example.com/"
export HELPDESK_JWT_ROLES_CLAIM="groups"   # JWT claim containing role list
export HELPDESK_JWT_AUDIENCE="helpdesk"    # optional — validates aud claim
export HELPDESK_JWT_CACHE_TTL="5m"         # JWKS key cache TTL
```

The Gateway validates the JWT signature against the JWKS endpoint, checks expiry,
issuer, and audience, then extracts `sub` as `UserID` and the configured claim
(default: `groups`) as `Roles`. JWKS keys are cached with TTL to avoid per-request
round-trips to the IdP.

---

### 10.3 Data Sensitivity Markings

Data markings declare what kind of data a resource contains, independently of its
environment tag. A database tagged `[production]` may or may not contain personal
data — those are orthogonal facts. Sensitivity markings make the distinction
machine-readable and policy-enforceable.

#### 10.3.1 Sensitivity Classes

| Class | Meaning | Typical resources |
|-------|---------|------------------|
| `public` | No sensitivity restrictions | Internal metrics, status dashboards |
| `internal` | Business data, not personally identifiable | Operational databases, deployment configs |
| `sensitive` | Commercially sensitive or under regulatory scope | Financial, legal, partner data |
| `pii` | Contains personal data (GDPR, CCPA scope) | Customer records, user tables |
| `critical` | High blast-radius or systems-of-record | Primary production databases, core K8s clusters |

Multiple classes are additive. A database can be both `pii` and `critical`.

#### 10.3.2 Declaring Sensitivity in Infra Config

`sensitivity` is a new field on `DBServer` and `K8sCluster` in `HELPDESK_INFRA_CONFIG`:

```json
{
  "db_servers": {
    "prod-db": {
      "name": "Production Database",
      "connection_string": "host=prod-db.example.com ...",
      "tags": ["production"],
      "sensitivity": ["pii", "critical"]
    },
    "analytics-db": {
      "name": "Analytics Read Replica",
      "connection_string": "...",
      "tags": ["production"],
      "sensitivity": ["internal"]
    },
    "dev-db": {
      "name": "Development Database",
      "connection_string": "...",
      "tags": ["development"],
      "sensitivity": ["internal"]
    }
  },
  "k8s_clusters": {
    "prod-cluster": {
      "context": "prod",
      "tags": ["production"],
      "sensitivity": ["critical"]
    }
  }
}
```

#### 10.3.3 Using Sensitivity in Policy

Sensitivity classes extend `ResourceMatch` — a policy can now target resources
by what data they contain, not just by their environment tag:

```yaml
resources:
  - type: database
    match:
      sensitivity: [pii]          # any database containing personal data

  - type: database
    match:
      tags: [production]
      sensitivity: [critical]     # production + critical (both must match)
```

A full policy example targeting PII databases with tighter controls than the
environment policy alone would provide:

```yaml
- name: pii-data-protection
  description: Extra controls on databases containing personal data
  priority: 110    # evaluated before environment-level policies

  resources:
    - type: database
      match:
        sensitivity: [pii]

  rules:
    - action: read
      effect: allow
      conditions:
        allowed_purposes: [diagnostic, remediation, compliance]

    - action: write
      effect: allow
      conditions:
        require_approval: true
        allowed_purposes: [remediation]
        approval_quorum: 2

    - action: destructive
      effect: deny
      message: "Destructive operations on PII databases require explicit DBA override policy"
```

---

### 10.4 Purpose-Based Access

Purpose answers "why is this access happening?" It is the dimension that makes
identical role + resource combinations distinguishable by intent.

#### 10.4.1 Purpose Vocabulary

| Purpose | Meaning | Typical operations |
|---------|---------|-------------------|
| `diagnostic` | Read-only investigation of an issue | Queries, pod inspection, log reads |
| `remediation` | Fixing an active problem | Cancel query, restart pod, scale deployment |
| `maintenance` | Planned change during a maintenance window | Any write or destructive operation |
| `compliance` | Compliance or audit-driven read of sensitive data | Sensitive reads that need extra traceability |
| `emergency` | Break-glass override for on-call response | Any operation — subject to post-hoc review |

#### 10.4.2 How Purpose Is Declared

**Implicit (default):** When no purpose is declared by the caller, it is derived
from the operating mode:

| Operating mode | Default purpose |
|---------------|----------------|
| `readonly` | `diagnostic` |
| `fix` | `remediation` |

**Explicit via request body** (preferred for audit clarity):

```json
{
  "query": "The analytics pipeline is blocked on prod-db. Please cancel the blocking query.",
  "purpose": "remediation",
  "purpose_note": "Blocking query preventing nightly analytics jobs — incident INC-2891"
}
```

**Explicit via header** (for programmatic callers):

```
X-Purpose: remediation
X-Purpose-Note: INC-2891 blocker removal
```

#### 10.4.3 Purpose Conditions in Policy Rules

Two new conditions extend the `Conditions` block in policy rules:

```yaml
conditions:
  allowed_purposes: [remediation, maintenance]   # deny if purpose not in this list
  blocked_purposes: [data_export, bulk_analysis]  # deny if purpose is in this list
```

If `allowed_purposes` is omitted, all purposes are permitted (backwards-compatible
default — existing rules behave exactly as today).

`blocked_purposes` can be used to harden a policy regardless of other conditions:

```yaml
- action: read
  effect: allow
  conditions:
    blocked_purposes: [data_export]
  message: "Bulk data export from this database is not permitted through the agent"
```

#### 10.4.4 Emergency Purpose

The `emergency` purpose is a structured break-glass mechanism. It can override
restrictive policies, but it never bypasses the audit trail and always requires
approval — the point is controlled override, not invisible override.

A break-glass policy that allows `oncall` role full access under emergency purpose:

```yaml
- name: emergency-break-glass
  description: Emergency access for on-call engineers during active incidents
  priority: 200    # highest possible — evaluated first

  principals:
    - role: oncall

  resources:
    - type: database
    - type: kubernetes

  rules:
    - action: [read, write, destructive]
      effect: allow
      conditions:
        allowed_purposes: [emergency]
        require_approval: true      # human sign-off still required
        approval_quorum: 1
      message: "Emergency access granted. All actions audited with elevated severity."
```

Every `emergency`-purpose audit event is flagged with elevated severity in the
audit trail, making it straightforward for govbot to report on break-glass usage.

#### 10.4.5 Requiring Explicit Purpose for Sensitive Resources

When `HELPDESK_REQUIRE_PURPOSE_FOR_SENSITIVE=true`, any access to a resource
with `sensitivity: [pii]` or `sensitivity: [critical]` without an explicit purpose
declaration is denied at the Gateway before the request reaches an agent:

```
POST /api/v1/query
 │
 ├─ resource resolved as pii + critical
 ├─ purpose not declared in request
 └─ HELPDESK_REQUIRE_PURPOSE_FOR_SENSITIVE=true
     → 403: "Access to sensitive resources requires an explicit purpose declaration.
             Add 'purpose' to your request body."
```

This is disabled by default so existing callers are not broken.

---

### 10.5 Principal Propagation

The most significant implementation gap: principal resolved at the Gateway is
currently discarded after the audit event is written. It never reaches the policy
engine that needs it.

The fix: principal and purpose flow as structured fields through every layer.

```
User / Service Account
      │ Authorization: Bearer <api-key>       (static or jwt mode)
      │ X-User: alice@example.com             (none mode — unverified)
      │ X-Purpose: remediation
      ▼
┌───────────────────────────────────────────────────────────┐
│  Gateway                                                  │
│  IdentityProvider.Resolve(r)                              │
│  → ResolvedPrincipal{                                     │
│      UserID:     "alice@example.com",                     │
│      Roles:      ["dba", "sre"],                          │
│      AuthMethod: "api_key",                               │
│    }                                                      │
│  TraceContext{                                            │
│    TraceID:     "tr_a1b2c3d4e5f6",                        │
│    Principal:   ResolvedPrincipal{...},   ← structured    │
│    Purpose:     "remediation",                            │
│    PurposeNote: "INC-2891",                               │
│  }                                                        │
└──────────────────────────┬────────────────────────────────┘
                           │ A2A message metadata
                           │ { "trace_id":    "tr_...",
                           │   "user_id":     "alice@example.com",
                           │   "roles":       ["dba", "sre"],
                           │   "auth_method": "api_key",
                           │   "purpose":     "remediation",
                           │   "purpose_note":"INC-2891" }
                           ▼
┌───────────────────────────────────────────────────────────┐
│  Orchestrator                                             │
│  Reads principal from incoming A2A metadata               │
│  Forwards it in outgoing A2A calls to sub-agents          │
└──────────────────────────┬────────────────────────────────┘
                           │ same A2A metadata forwarded downstream
                           ▼
┌───────────────────────────────────────────────────────────┐
│  DB Agent / K8s Agent                                     │
│  agentutil.PolicyEnforcer.CheckTool(ctx, ...)             │
│  → reads TraceContext from ctx                            │
│  → policy.Request{                                        │
│      Principal: {UserID, Roles, Service},                 │
│      Resource:  {Type, Name, Tags, Sensitivity},          │
│      Action:    ActionWrite,                              │
│      Context:   {Purpose, PurposeNote, ...},              │
│    }                                                      │
└──────────────────────────┬────────────────────────────────┘
                           │ POST /v1/governance/check
                           │ { principal, sensitivity,
                           │   purpose, purpose_note, ... }
                           ▼
┌───────────────────────────────────────────────────────────┐
│  auditd — policy engine evaluation                        │
│  PolicyDecision audit event includes:                     │
│    user_id, roles, auth_method                            │
│    purpose, purpose_note                                  │
│    sensitivity classes seen                               │
└───────────────────────────────────────────────────────────┘
```

#### 10.5.1 TraceContext Extension

`TraceContext.Principal` changes from a plain `string` to the structured
`identity.ResolvedPrincipal`. Purpose fields are added:

```go
// TraceContext extended with verified principal and purpose.
type TraceContext struct {
    TraceID     string                    `json:"trace_id"`
    ParentID    string                    `json:"parent_id,omitempty"`
    Origin      string                    `json:"origin"`
    Principal   identity.ResolvedPrincipal `json:"principal,omitempty"`  // was string
    Purpose     string                    `json:"purpose,omitempty"`
    PurposeNote string                    `json:"purpose_note,omitempty"`
}
```

`NewTraceContext` is updated to accept `ResolvedPrincipal` instead of a string.
All existing callers that pass a plain string are updated at the same time.

#### 10.5.2 A2A Metadata Convention

The orchestrator reads principal and purpose from its incoming A2A message metadata
and forwards them to every sub-agent it calls. The metadata keys are:

| Key | Type | Description |
|-----|------|-------------|
| `trace_id` | string | Existing — unchanged |
| `user_id` | string | Resolved user ID |
| `roles` | `[]string` | Resolved role list |
| `service` | string | Set only for service accounts |
| `auth_method` | string | `"api_key"`, `"jwt"`, `"header"` |
| `purpose` | string | Declared or derived purpose |
| `purpose_note` | string | Optional free-text note |

Sub-agents reconstruct a `TraceContext` from these fields when they receive a task.
Unknown keys are ignored — forwards compatibility.

#### 10.5.3 Policy Check Request Extension

`policyCheckReq` in `agentutil` and `PolicyCheckRequest` in `auditd` gain identity
and sensitivity fields:

```go
type policyCheckReq struct {
    ResourceType  string                    `json:"resource_type"`
    ResourceName  string                    `json:"resource_name"`
    Action        string                    `json:"action"`
    Tags          []string                  `json:"tags,omitempty"`
    Sensitivity   []string                  `json:"sensitivity,omitempty"`  // NEW
    TraceID       string                    `json:"trace_id,omitempty"`
    AgentName     string                    `json:"agent_name,omitempty"`
    Note          string                    `json:"note,omitempty"`
    // existing blast-radius fields unchanged ...

    // NEW identity and purpose fields:
    Principal     identity.ResolvedPrincipal `json:"principal,omitempty"`
    Purpose       string                    `json:"purpose,omitempty"`
    PurposeNote   string                    `json:"purpose_note,omitempty"`
}
```

#### 10.5.4 Audit Event Extension

`PolicyDecision` gains identity and purpose fields:

```go
type PolicyDecision struct {
    // existing fields unchanged ...

    // NEW — identity fields:
    UserID      string   `json:"user_id,omitempty"`
    Roles       []string `json:"roles,omitempty"`
    Service     string   `json:"service,omitempty"`
    AuthMethod  string   `json:"auth_method,omitempty"`

    // NEW — purpose fields:
    Purpose     string   `json:"purpose,omitempty"`
    PurposeNote string   `json:"purpose_note,omitempty"`

    // NEW — sensitivity classes of the resource accessed:
    Sensitivity []string `json:"sensitivity,omitempty"`
}
```

---

### 10.6 Policy Engine Extensions

The engine already implements `matchesPrincipal` — it evaluates `RequestPrincipal`
against `policy.Principals`. Three additive extensions are needed:

#### 10.6.1 Sensitivity Matching in Resource Rules

`ResourceMatch` gains a `Sensitivity` field. When set, a policy matches a resource
only if the resource's sensitivity list contains **all** of the listed classes
(same AND semantics as `tags`):

```go
// ResourceMatch extended:
type ResourceMatch struct {
    Name        string   `yaml:"name,omitempty"`
    NamePattern string   `yaml:"name_pattern,omitempty"`
    Tags        []string `yaml:"tags,omitempty"`
    Namespace   string   `yaml:"namespace,omitempty"`
    Sensitivity []string `yaml:"sensitivity,omitempty"` // NEW
}
```

`matchesResource` evaluates `Sensitivity` the same way it evaluates `Tags`:
the resource must carry all listed classes. Empty `Sensitivity` matches any resource.

#### 10.6.2 Purpose Conditions

`Conditions` gains purpose restriction fields:

```go
type Conditions struct {
    // existing conditions unchanged ...

    // NEW: Purpose-based conditions.
    // AllowedPurposes: if non-empty, the request purpose must be in this list.
    AllowedPurposes []string `yaml:"allowed_purposes,omitempty"`
    // BlockedPurposes: if non-empty, the request purpose must NOT be in this list.
    BlockedPurposes []string `yaml:"blocked_purposes,omitempty"`
}
```

`evaluateConditions` evaluates purpose after all other conditions. A purpose
mismatch produces a `ConditionTrace` entry (for explainability) and denies the
rule match, causing evaluation to continue to the next rule.

#### 10.6.3 RequestContext and RequestResource Extensions

```go
type RequestContext struct {
    // existing fields unchanged ...
    Purpose     string `json:"purpose,omitempty"`
    PurposeNote string `json:"purpose_note,omitempty"`
}

type RequestResource struct {
    Type        string   `json:"type"`
    Name        string   `json:"name"`
    Tags        []string `json:"tags,omitempty"`
    Sensitivity []string `json:"sensitivity,omitempty"` // NEW
}
```

---

### 10.7 Service Account Identity vs. Human Caller Identity

Requests carry two distinct identities simultaneously:

| Identity | Who | Resolved from |
|----------|-----|--------------|
| **Human caller** | The person who initiated the request | Authentication header at Gateway |
| **Executing agent** | The agent service that is running the tool | Hardcoded service account name in agent binary |

Policy rules can target either. Most rules should target the human caller —
the agent's service account is used only for service-level restrictions (e.g.,
the `automated-services` policy that caps automated writes to 100 rows).

The `agentutil.PolicyEnforcer` populates both: `Principal.UserID/Roles` from
the human caller (extracted from `TraceContext`), and `Principal.Service` from
the agent's own identity (set at agent startup from config).

---

### 10.8 Backwards Compatibility

All changes are strictly additive and layered. Existing deployments continue to
work without any configuration change:

| Scenario | Behaviour |
|----------|-----------|
| `HELPDESK_IDENTITY_PROVIDER` not set | Defaults to `none`: `X-User` header accepted as-is, `AuthMethod="header"`, no roles resolved |
| Policy rules with no `principals:` | Match any caller — unchanged |
| Policy rules with `principals:` | Now actually enforced (were silently matched against empty principal before — effectively `any`) |
| Infra config without `sensitivity` | Sensitivity list is empty; sensitivity-based policy match conditions evaluate as "no restriction" |
| Policy rules without `allowed_purposes` / `blocked_purposes` | All purposes permitted — unchanged |
| `X-Purpose` header absent, purpose not in body | Purpose derived from operating mode (`readonly` → `diagnostic`, `fix` → `remediation`) |

> **Note on silent change:** Policy rules with `principals: [{role: dba}]` that
> previously matched all callers (because principal was always empty) will now
> correctly match *only* DBA-role callers in `static` or `jwt` mode. In `none`
> mode they continue to match all callers. Operators upgrading to `static` or
> `jwt` should audit their policies to ensure role-restricted rules have the
> intended scope.

---

### 10.9 Security Considerations

**Principal spoofing in `none` mode:** The `X-User` header is accepted without
validation. Anyone with network access to the Gateway can claim any identity.
This is the pre-existing behaviour and is acceptable only in trusted networks.
Upgrading to `static` or `jwt` mode closes this gap.

**Purpose integrity:** Purpose is declared by the caller and cannot be
cryptographically verified. The enforcement mechanism is the audit trail — every
purpose declaration is recorded and all misuse is retrospectively detectable via
govbot. High-risk purposes (`emergency`) additionally require approval, adding
human oversight as a second control layer.

**API key storage:** Service account API keys are stored only as Argon2id hashes
in `users.yaml`. The plaintext key is generated once and given to the service;
the system never stores or logs it.

**JWT JWKS caching:** Cached JWKS keys reduce per-request latency but create a
window where a revoked key is still valid. The default TTL of 5 minutes is a
reasonable enterprise trade-off. Set `HELPDESK_JWT_CACHE_TTL=0` to disable
caching for environments with aggressive key rotation.

**Agent impersonation:** An agent cannot claim a human principal — it can only
forward the principal it received from the Gateway via `TraceContext`. Agents
have their own service identity (`Principal.Service`) that is set at startup from
config, not from incoming requests.

---

### 10.10 Configuration Reference

```bash
# Identity provider (default: "none")
export HELPDESK_IDENTITY_PROVIDER="static"    # or "jwt"

# Static provider
export HELPDESK_USERS_FILE="/etc/helpdesk/users.yaml"

# JWT provider
export HELPDESK_JWT_JWKS_URL="https://idp.example.com/.well-known/jwks.json"
export HELPDESK_JWT_ISSUER="https://idp.example.com/"
export HELPDESK_JWT_ROLES_CLAIM="groups"        # JWT claim containing role list
export HELPDESK_JWT_AUDIENCE="helpdesk"         # optional: validate aud claim
export HELPDESK_JWT_CACHE_TTL="5m"             # JWKS key cache TTL (0 = no cache)

# Purpose
export HELPDESK_DEFAULT_PURPOSE=""             # empty = infer from operating mode
export HELPDESK_REQUIRE_PURPOSE_FOR_SENSITIVE="false"  # deny access to pii/critical
                                                        # resources without purpose
```

---

### 10.11 Compliance Reporting Integration

govbot gains two new phases to report on identity and purpose coverage:

**Phase 11 — Identity Coverage**

```
Phase 11 — Identity Coverage
  Identity provider: static
  Requests with resolved principal:   847 / 851  (99.5%)
  Requests with anonymous principal:    4 / 851   (0.5%)  ← WARN if > 0 in static/jwt mode
  Policy decisions with role match:   712 / 847  (84.0%)
  Policy decisions with empty roles:  135 / 847  (15.9%)  ← identifies misconfigured users
```

**Phase 12 — Purpose Coverage**

```
Phase 12 — Purpose Coverage
  Requests with explicit purpose:     623 / 847  (73.6%)
  Requests with implicit purpose:     224 / 847  (26.4%)
  Emergency-purpose requests:           3 / 847   (0.4%)  ← ALERT if not reviewed
  Purpose breakdown:
    diagnostic:   401 (47.3%)
    remediation:  382 (45.1%)
    maintenance:   58  (6.8%)
    emergency:      3  (0.4%)
  PII resource accesses without explicit purpose: 12  ← WARN
```

---

### 10.12 govexplain Integration

`govexplain` gains `--user`, `--roles`, and `--purpose` flags for hypothetical
checks that include identity and purpose in the evaluation:

```bash
# Would alice (as dba) be allowed to write to prod-db for remediation?
govexplain \
  --gateway http://localhost:8080 \
  --resource database:prod-db \
  --action write \
  --user alice@example.com \
  --roles dba,sre \
  --purpose remediation

# Would the same action be denied for data_export purpose?
govexplain \
  --gateway http://localhost:8080 \
  --resource database:prod-db \
  --action read \
  --user alice@example.com \
  --roles dba \
  --purpose data_export
```

---

### 10.13 Implementation

All changes are additive. No existing behaviour changes in `none` mode (default).

| Component | Change | Location | Status |
|-----------|--------|----------|--------|
| **New package** | `identity.Provider` interface; `ResolvedPrincipal` type; `NoAuthProvider`, `StaticProvider`, `JWTProvider` implementations | `internal/identity/` | ✅ Done |
| **New config** | `users.yaml` format and loader | `internal/identity/config.go` | ✅ Done |
| Policy types | `Sensitivity []string` on `ResourceMatch`; `AllowedPurposes`, `BlockedPurposes` on `Conditions`; `Sensitivity`, `Purpose`/`PurposeNote` on `RequestResource`, `RequestContext` | `internal/policy/types.go` | ✅ Done |
| Policy engine | Sensitivity matching in `matchesResource`; purpose evaluation in `evaluateConditions`; ConditionTrace entries for purpose mismatches | `internal/policy/engine.go` | ✅ Done |
| Infra config | `Sensitivity []string` on `DBServer`, `K8sCluster` | `internal/infra/infra.go` | ✅ Done |
| Audit trace | `TraceContext.Principal` → `identity.ResolvedPrincipal`; add `Purpose`, `PurposeNote`; `PrincipalFromContext`, `PurposeFromContext` helpers | `internal/audit/trace.go` | ✅ Done |
| Trace middleware | Extract `user_id`, `roles`, `service`, `auth_method`, `purpose`, `purpose_note` from A2A metadata; build full `TraceContext` with principal+purpose | `internal/audit/trace_middleware.go` | ✅ Done |
| Delegate tool | Propagate principal + purpose fields in outgoing A2A message metadata | `internal/audit/delegate_tool.go` | ✅ Done |
| Audit events | `UserID`, `Roles`, `Service`, `AuthMethod`, `Purpose`, `PurposeNote`, `Sensitivity` on `PolicyDecision` | `internal/audit/event.go` | ✅ Done |
| agentutil | Extract principal + purpose from `context.Context`; populate `policy.Request.Principal`, `Resource.Sensitivity`, `Context.Purpose/PurposeNote`; propagate in `policyCheckReq` for remote mode and local `PolicyDecision` audit events | `agentutil/agentutil.go` | ✅ Done |
| Gateway | Instantiate identity provider from env; resolve principal on every request; propagate principal + purpose in A2A message metadata | `cmd/gateway/gateway.go`, `cmd/gateway/main.go` | ✅ Done |
| auditd governance | `Principal`, `Sensitivity`, `Purpose`, `PurposeNote` on `PolicyCheckRequest`; wire into `policy.Request` and `PolicyDecision` audit event; `sensitivityFromInfra` helper; `handleExplain` accepts `purpose` + `sensitivity` query params | `cmd/auditd/governance_handlers.go` | ✅ Done |
| govexplain | `--purpose` and `--sensitivity` flags; wired through local, direct, and gateway explain paths | `cmd/govexplain/main.go` | ✅ Done |
| govbot | Phase 11 (identity coverage) and Phase 12 (purpose coverage) reports | `cmd/govbot/main.go` | ✅ Done |
| policies.example.yaml | Sensitivity-based resource matching, purpose conditions, emergency break-glass policy | `policies.example.yaml` | ✅ Done |
| users.example.yaml | Example file for static identity provider (human users + service accounts with Argon2id hashes) | `users.example.yaml` | ✅ Done |

---

## 11. Troubleshooting

Please refer to [here](ARCHITECTURE.md#13-troubleshooting) for the general purpose
troubleshooting tips and known issues beyond AI Governance.

#### 11.1 Events Not Being Recorded

1. Verify auditd is running:
   ```bash
   curl http://localhost:1199/health
   ```

2. Check agent has `HELPDESK_AUDIT_URL` set:
   ```bash
   # When starting the agent
   HELPDESK_AUDIT_URL="http://localhost:1199" go run ./agents/database/
   ```

3. Check auditd logs for connection errors

#### 11.2 Auditor Not Receiving Events

1. Verify socket path matches between auditd and auditor:
   ```bash
   # Auditd uses HELPDESK_AUDIT_SOCKET
   # Auditor uses --socket flag
   # Both must point to the same path
   ```

2. Check socket file exists:
   ```bash
   ls -la /tmp/helpdesk-audit.sock
   ```

3. Ensure auditor connects before events are sent (events sent before
   connection are not replayed)

#### 11.3 Chain Verification Fails

If chain verification reports broken links:

1. Query the broken events:
   ```bash
   curl "http://localhost:1199/api/verify" | jq '.broken_links'
   ```

2. Investigate potential causes:
   - Database was modified directly (tampering)
   - Race condition during high-volume writes (bug - should be fixed)
   - Database corruption

3. For legitimate issues, the audit log should be considered compromised
   and investigated

#### 11.4 Off-Hours Alerts Not Working

The auditor uses local time for off-hours detection. Verify your system
timezone is set correctly:

```bash
date  # Check current local time
```

---

## 12. Roadmap

### 12.1 Phase 1: Foundation (Complete)
- [x] Audit system with hash chains
- [x] Real-time monitoring (auditor)
- [x] Security alerting (secbot)
- [x] Policy engine (internal/policy/)
- [x] Policy enforcement in agents (database, k8s)

### 12.2 Phase 2: Enforcement (Complete)
- [x] Approval workflows (cmd/approvals/, auditd API, Slack/email notifications)
- [x] Compliance reporting (cmd/govbot/, Kubernetes CronJob)
- [x] Guardrails: DB blast radius (`max_rows_affected`), K8s blast radius (`max_pods_affected`), transaction age (`max_xact_age_secs`), schedule — pre- and post-execution hooks
- [x] Explainability — decision trace, `govexplain` CLI, explain API endpoints
- [x] Operating mode switch (`readonly` / `fix`) with governance enforcement

### 12.3 Phase 3: Operations (In Design)
- [ ] **Identity & access** — three-dimension access control: role (verified via identity provider), data sensitivity markings, and purpose-based conditions. Design complete; see [§10](#10-identity--access).
- [ ] **Rollback & Undo** — recovery from agent-initiated mutations. Design pending.
- [ ] Rate limits (write frequency per session)
- [ ] Circuit breaker (auto-pause on consecutive errors)

### 12.4 Phase 4: Intelligence
- [ ] Anomaly detection (ML-based)
- [ ] Risk scoring
- [ ] Automated remediation suggestions
