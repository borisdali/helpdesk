# AI Governance Architecture

Please see [here](ARCHITECTURE.md) for the general overview of
aiHelpDesk Architecture. This page is dedicated to aiHelpDesk's very
important subsystem that we refer to as AI Governance.

## Overview

As aiHelpDesk evolves from read-only diagnostics to actively *fixing* infrastructure
issues, governance becomes critical for trust. The AI Governance system ensures that
when agents can modify databases, scale deployments, or restart services, they do so
safely, accountably, and with appropriate human oversight.

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

## Components

aiHelpDesk Governance consists of eight well-defined components:

| Component | Status | Description |
|-----------|--------|-------------|
| [Audit System](#audit-system) | **Implemented** | Tamper-evident logging with hash chains |
| [Policy Engine](#policy-engine) | **Implemented** | Rule-based access control |
| [Approval Workflows](#approval-workflows) | **Implemented** | Human-in-the-loop for risky ops |
| [Compliance Reporting](#compliance-reporting-govbot) | **Implemented** | Scheduled compliance snapshots and alerting |
| [Guardrails](#guardrails) | Partial | Blast-radius enforcement implemented; rate limits and circuit breaker planned |
| [Operating Mode](#operating-mode) | Planned | `readonly`/`fix` switch with governance enforcement |
| Identity & Access | Planned | User/role-based permissions |
| [Explainability](#explainability) | Designed | Decision trace, human-readable explanations, query interface — implementation planned |
| Rollback & Undo | Planned | Recovery from mistakes |

---

## Policy Engine

The Policy Engine defines what actions are allowed, by whom, on which resources,
and under what conditions. It is the foundation for all other governance controls.

### Policy Structure

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

### Policy Evaluation Flow

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

### Environment Variables

```bash
export HELPDESK_POLICY_FILE="/etc/helpdesk/policies.yaml"
export HELPDESK_DEFAULT_POLICY="deny"      # When no policy matches
export HELPDESK_POLICY_DRY_RUN="true"      # Log decisions but don't enforce
```

### Implementation

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

See `policies.example.yaml` for a complete policy configuration example.

### Agent Integration

Policy enforcement is integrated directly into the agents via `agentutil.PolicyEnforcer`.
Each agent initializes the policy engine at startup and checks policies before executing tools.

#### Initialization (main.go)

```go
// Initialize policy engine if configured
policyEngine, err := agentutil.InitPolicyEngine(cfg)
if err != nil {
    slog.Error("failed to initialize policy engine", "err", err)
    os.Exit(1)
}
policyEnforcer = agentutil.NewPolicyEnforcer(policyEngine, traceStore)
```

#### Tool Enforcement (tools.go)

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

#### Resource Tags

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

## Approval Workflows

When a policy rule has `effect: require_approval`, the agent blocks and waits
for a human to approve or deny the request before execution proceeds.

### Flow

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

### Implementation

Approval state is managed by `auditd` and persisted in SQLite.
The agent's `agentutil.PolicyEnforcer` polls the auditd approval API
until the request is decided or the timeout elapses.

| Component | Location | Role |
|-----------|----------|------|
| Approval API | `cmd/auditd/` | Stores requests, exposes approve/deny endpoints |
| PolicyEnforcer | `agentutil/agentutil.go` | Blocks tool execution, polls for decision |
| Approvals CLI | `cmd/approvals/` | Human tool to list and decide pending requests |
| Notification | `cmd/auditd/` | Sends Slack webhook and/or email on new request |

### Approvals CLI

Humans manage pending approvals with the `approvals` CLI:

```bash
# List all pending approval requests
approvals list --url http://localhost:1199

# Approve a specific request
approvals approve <approval-id> --url http://localhost:1199

# Deny a specific request
approvals deny <approval-id> --url http://localhost:1199
```

### Approval API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/v1/approvals` | Create a new approval request (called by agent) |
| GET | `/v1/approvals` | List all approval requests |
| GET | `/v1/approvals/pending` | List only pending requests |
| POST | `/v1/approvals/{id}/approve` | Approve a request |
| POST | `/v1/approvals/{id}/deny` | Deny a request |

### Configuration

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

### Approval States

| State | Description |
|-------|-------------|
| `pending` | Awaiting approval decision |
| `approved` | Manually approved by human |
| `denied` | Rejected by approver |
| `expired` | Approval request timed out — agent receives a denial |

---

## Guardrails

Guardrails are hard safety constraints that cannot be overridden, even with approval.

| Guardrail | Status | Description |
|-----------|--------|-------------|
| **Blast Radius** | **Implemented** | Caps rows/pods affected per operation |
| Rate Limits | Planned | Max write frequency per session |
| Circuit Breaker | Planned | Auto-stop on consecutive errors |

### Blast Radius (Implemented)

Blast radius limits cap how many rows or pods a single operation may affect.
They are evaluated **post-execution** — after the tool runs but before the LLM
receives the result. If the limit is exceeded, the result is withheld and an
error is returned to the LLM, and a `PostExecution: true` policy denial event
is recorded in the audit trail.

> **Design note:** post-execution evaluation has important limitations for large
> DML, DDL statements, and distributed topologies. See [GOVPOSTEVAL.md](GOVPOSTEVAL.md)
> for a full analysis of trade-offs, the rollback problem, and the planned
> pre-execution COUNT estimation approach.

Configure limits in your policy file under a rule's `conditions`:

```yaml
rules:
  - action: write
    effect: allow
    conditions:
      max_rows_affected: 1000   # database: rows modified (DELETE/UPDATE/INSERT)
      max_pods_affected: 10     # kubernetes: resources created/configured/deleted
```

See `policies.example.yaml` for full examples.

#### How it works

```
Pre-execution:   CheckDatabase / CheckKubernetes  → allow/deny/require_approval
                         │
                    tool executes
                         │
Post-execution:  CheckDatabaseResult              → blast-radius enforcement
                 CheckKubernetesResult
                         │
                 ┌───────┴────────┐
                 │ within limit   │ exceeded limit
                 ▼                ▼
              return result    return error +
                               audit PostExecution denial
```

Row counts are parsed from psql command tag output (`DELETE N`, `UPDATE N`,
`INSERT 0 N`). Pod counts are parsed from kubectl confirmation lines
(`pod "x" deleted`, `deployment "y" configured`, etc.).

#### Agent Integration

**Database agent** — `runPsqlWithToolName` calls `CheckDatabaseResult`
automatically. When adding a new write tool, pass `policy.ActionWrite` (or
`policy.ActionDestructive`) via the `action` variable — the post-execution
check is already wired.

**Kubernetes agent** — call `checkK8sPolicyResult` after any write or
destructive `runKubectlWithToolName` call:

```go
// Pre-execution check (existing pattern)
nsInfo := resolveNamespaceInfo(namespace)
if err := checkK8sPolicy(ctx, nsInfo.Namespace, policy.ActionWrite, nsInfo.Tags); err != nil {
    return "", err
}

// Execute
output, execErr := runKubectlWithToolName(ctx, kubeCtx, "tool_name", args...)

// Post-execution blast-radius check
if err := checkK8sPolicyResult(ctx, nsInfo.Namespace, policy.ActionWrite, nsInfo.Tags, output, execErr); err != nil {
    return "", err
}
```

### Planned Guardrails

**Rate limits** — cap write frequency per session (e.g. max 20 writes/minute).
Requires a per-session counter with TTL; not yet implemented.

**Circuit breaker** — auto-pause an agent after N consecutive errors in a
rolling window to prevent runaway failure loops. Not yet implemented.

---

## Operating Mode

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

### Why a Default of `readonly`

Most day-to-day use is diagnostic — querying databases, inspecting pods,
gathering logs. Read-only mode ensures that newly deployed agents can never
accidentally mutate state until an operator explicitly opts in. When write tools
are added in the future, they will be silently gated behind this flag until
the operator is ready.

### Startup Validation (fix mode)

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

### Runtime Enforcement

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

### Governance Misconfiguration Incidents

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

### Violation Types

| Condition | Severity | Action |
|-----------|----------|--------|
| `fix` mode + audit disabled or unreachable | **Critical** | Block tool, create incident, log stderr |
| `fix` mode + policy disabled or not loaded | **Critical** | Block tool, create incident, log stderr |
| `fix` mode + approval disabled for `destructive` action | Warning | Allow with log warning (governance gap, not a hard block) |

### Implementation Notes (Planned)

This feature is not yet implemented. When built, the key integration points will be:

- `agentutil.MustLoadConfig` — validate mode + governance config at startup
- `agentutil.PolicyEnforcer.CheckTool` — mode gate before policy evaluation
- `agentutil.PolicyEnforcer` — hold a reference to the gateway URL for the incident fallback
- `agents/*/main.go` — startup pre-flight check before `a2a.Serve`

---

## Audit System

aiHelpDesk includes a tamper-evident audit system that records all tool executions
across agents, providing accountability, compliance support, and security monitoring.
The audit system uses hash chains to detect tampering with the audit log.

### Architecture

```
                                    ┌─────────────────────┐
                                    │   auditor (CLI)     │
                                    │ Real-time monitor   │
                                    │ • Security alerts   │
                                    │ • Chain verification│
                                    └──────────┬──────────┘
                                               │ Unix socket
                                               │ notifications
    ┌───────────────┐                          ▼
    │  database     │──────┐         ┌─────────────────────┐
    │  agent        │      │         │    auditd service   │
    │  :1100        │      │ HTTP    │    :1199            │
    └───────────────┘      │         │                     │
                           ├────────►│ • SQLite storage    │
    ┌───────────────┐      │         │ • Hash chain        │
    │  k8s          │──────┤         │ • Socket notify     │
    │  agent        │      │         │ • Chain verification│
    │  :1102        │      │         └─────────────────────┘
    └───────────────┘      │                   │
                           │                   ▼
    ┌───────────────┐      │         ┌─────────────────────┐
    │  incident     │──────┘         │   audit.db (SQLite) │
    │  agent        │                │   • audit_events    │
    │  :1104        │                │   • Hash chain      │
    └───────────────┘                └─────────────────────┘
```

### Components

| Component | Location | Description |
|-----------|----------|-------------|
| `auditd` | `cmd/auditd/` | Central audit service with HTTP API and SQLite storage |
| `auditor` | `cmd/auditor/` | Real-time monitoring CLI with security alerting |
| `audit` package | `internal/audit/` | Core audit types, hash chain, and tool auditor |

### Hash Chain Integrity

Each audit event includes cryptographic hashes that form a chain:

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  Event 1     │     │  Event 2     │     │  Event 3     │
│              │     │              │     │              │
│ prev_hash:   │     │ prev_hash:   │     │ prev_hash:   │
│  genesis     │────►│  hash(E1)    │────►│  hash(E2)    │
│              │     │              │     │              │
│ event_hash:  │     │ event_hash:  │     │ event_hash:  │
│  SHA256(E1)  │     │  SHA256(E2)  │     │  SHA256(E3)  │
└──────────────┘     └──────────────┘     └──────────────┘
```

- **prev_hash**: Hash of the previous event (genesis hash for the first event)
- **event_hash**: SHA-256 hash of the event's canonical JSON representation

If an attacker modifies any event in the database:
1. The `event_hash` will no longer match the event content
2. The next event's `prev_hash` will no longer match
3. Chain verification will detect the break

### Event Schema

Audit events capture tool executions with full context:

| Field | Description |
|-------|-------------|
| `event_id` | Unique identifier (e.g., `tool_a1b2c3d4`) |
| `timestamp` | UTC timestamp in RFC3339Nano format |
| `event_type` | Type of event (`tool_execution`, `delegation`, etc.) |
| `trace_id` | End-to-end correlation ID from the orchestrator |
| `action_class` | Classification: `read`, `write`, or `destructive` |
| `session_id` | Agent session identifier |
| `tool.name` | Tool that was executed |
| `tool.parameters` | Input parameters to the tool |
| `tool.raw_command` | Actual command executed (SQL query, kubectl command) |
| `tool.result` | Truncated output (first 500 chars) |
| `tool.error` | Error message if the tool failed |
| `tool.duration` | Execution time |
| `outcome.status` | `success` or `error` |
| `prev_hash` | Hash of previous event in chain |
| `event_hash` | SHA-256 hash of this event |

### Action Classification

Tools are classified by their potential impact:

| Action Class | Description | Examples |
|--------------|-------------|----------|
| `read` | Read-only operations | `get_pods`, `get_database_info`, `get_active_connections` |
| `write` | State-modifying operations | `create_incident_bundle` |
| `destructive` | Potentially destructive operations | (Reserved for future tools) |

### Trace ID Propagation

The `trace_id` flows from the orchestrator through sub-agents for end-to-end
correlation:

1. User sends a query to the orchestrator
2. Orchestrator generates a `trace_id` and passes it in the A2A request metadata
3. Sub-agents extract `trace_id` via `TraceMiddleware` and include it in audit events
4. All events from a single user query share the same `trace_id`

This enables querying all tool executions triggered by a single user request:

```bash
# Query all events for a specific trace
curl "http://localhost:1199/api/events?trace_id=abc123"
```

### Auditd Service (cmd/auditd/)

The central audit service provides:

- **HTTP API** for recording events and querying the audit log
- **SQLite storage** with WAL mode for concurrent reads
- **Unix socket** for real-time event notifications
- **Hash chain** maintenance and verification

#### API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/events` | Record a new audit event |
| GET | `/api/events` | Query events with filters |
| GET | `/api/verify` | Verify hash chain integrity |
| GET | `/health` | Health check |

#### Query Filters

```bash
# Recent events
curl "http://localhost:1199/api/events?limit=10"

# Events by agent
curl "http://localhost:1199/api/events?agent=k8s_agent"

# Events by trace ID
curl "http://localhost:1199/api/events?trace_id=abc123"

# Events by action class
curl "http://localhost:1199/api/events?action_class=destructive"

# Events by tool name
curl "http://localhost:1199/api/events?tool_name=get_pods"

# Events since a timestamp
curl "http://localhost:1199/api/events?since=2026-01-01T00:00:00Z"
```

### Auditor CLI (cmd/auditor/)

The auditor provides real-time monitoring and security alerting:

```bash
# Start the auditor
go run ./cmd/auditor/ \
  --socket /tmp/helpdesk-audit.sock \
  --audit-url http://localhost:1199 \
  --verify-interval 30s
```

#### Security Detection

The auditor monitors for suspicious patterns:

| Detection | Description | Trigger |
|-----------|-------------|---------|
| High Volume | Burst of activity | >100 events/minute (configurable) |
| Off-Hours | Activity outside business hours | Events outside 6 AM - 10 PM local time |
| Unauthorized Destructive | Destructive ops without approval | `destructive` action without `approved` status |
| Timestamp Gap | Suspicious time jumps | Events with timestamps far in the past/future |
| Chain Tampering | Hash chain breaks | Periodic verification detects modified events |

#### Webhook Alerts

Security incidents can be sent to an external webhook:

```bash
go run ./cmd/auditor/ \
  --socket /tmp/helpdesk-audit.sock \
  --incident-webhook https://alerts.example.com/hook
```

Webhook payload:
```json
{
  "type": "high_volume",
  "timestamp": "2026-01-15T14:30:00Z",
  "agent": "k8s_agent",
  "count": 150,
  "threshold": 100,
  "message": "High event volume detected: 150 events in the last minute"
}
```

### Environment Variables

#### Auditd Service

```bash
# Listen address (default: localhost:1199)
export HELPDESK_AUDITD_ADDR="0.0.0.0:1199"

# SQLite database path (default: audit.db)
export HELPDESK_AUDIT_DB="/var/lib/helpdesk/audit.db"

# Unix socket for real-time notifications
export HELPDESK_AUDIT_SOCKET="/tmp/helpdesk-audit.sock"
```

#### Agent Audit Configuration

```bash
# Enable auditing for an agent (point to auditd service)
export HELPDESK_AUDIT_URL="http://localhost:1199"

# Note: Each agent automatically generates a unique session ID
```

#### Auditor CLI

```bash
# Unix socket to connect to for real-time events
# (matches HELPDESK_AUDIT_SOCKET in auditd)
--socket /tmp/helpdesk-audit.sock

# Auditd URL for chain verification
--audit-url http://localhost:1199

# Chain verification interval
--verify-interval 30s

# Webhook URL for security incidents
--incident-webhook https://alerts.example.com/hook

# Activity thresholds
--max-events-per-minute 100
--allowed-hours-start 6
--allowed-hours-end 22
```

### Running the Audit System

#### Start Auditd

```bash
# Terminal — audit service
HELPDESK_AUDIT_DB=/tmp/helpdesk/audit.db \
HELPDESK_AUDIT_SOCKET=/tmp/helpdesk-audit.sock \
  go run ./cmd/auditd/
```

#### Start Agents with Auditing

```bash
# Start agents with audit enabled
HELPDESK_AUDIT_URL="http://localhost:1199" go run ./agents/database/
HELPDESK_AUDIT_URL="http://localhost:1199" go run ./agents/k8s/
```

#### Start the Auditor

```bash
# Real-time monitoring with security alerts
go run ./cmd/auditor/ \
  --socket /tmp/helpdesk-audit.sock \
  --audit-url http://localhost:1199 \
  --verify-interval 30s
```

### Verifying Audit Integrity

#### Via API

```bash
curl -s http://localhost:1199/api/verify | jq
```

Response:
```json
{
  "verified": true,
  "total_events": 150,
  "broken_links": [],
  "first_event": "evt_a1b2c3d4",
  "last_event": "evt_z9y8x7w6"
}
```

#### Via SQL (Manual)

```sql
-- Check for broken hash chain links
SELECT
  e1.event_id as event,
  e1.prev_hash,
  e2.event_hash as expected_prev,
  CASE WHEN e1.prev_hash = e2.event_hash THEN 'OK' ELSE 'BROKEN' END as status
FROM audit_events e1
LEFT JOIN audit_events e2 ON e1.id = e2.id + 1
WHERE e1.id > 1
ORDER BY e1.id;
```

### Testing

Generate test events to verify the audit system:

```bash
# Send events directly to auditd
for i in {1..10}; do
  curl -X POST http://localhost:1199/api/events \
    -H "Content-Type: application/json" \
    -d '{
      "event_type": "tool_execution",
      "session": {"id": "test_session"},
      "tool": {
        "name": "test_tool",
        "raw_command": "SELECT 1",
        "agent": "test_agent"
      }
    }'
  sleep 0.1
done

# Verify the auditor receives them via the Unix socket
# (auditor should log each event as it arrives)
```

### Security Responder Bot (cmd/secbot/)

The `secbot` demonstrates automated security incident response. It monitors the
audit stream for critical security events and automatically creates incident
bundles for investigation.

```
┌─────────────────────┐
│   Audit Socket      │
│   (from auditd)     │
└──────────┬──────────┘
           │ subscribe
           ▼
┌─────────────────────┐         ┌─────────────────────┐
│   secbot            │  POST   │   REST Gateway      │
│   • Detect alerts   │────────►│   /api/v1/incidents │
│   • Enforce cooldown│         └──────────┬──────────┘
│   • Receive callback│                    │ A2A
└─────────────────────┘                    ▼
                               ┌─────────────────────┐
                               │   incident_agent    │
                               │   Create bundle     │
                               └─────────────────────┘
```

**Key features:**
- Monitors audit socket for security events
- Detects: hash mismatches, unauthorized destructive ops, injection attempts
- Creates incident bundles via REST gateway (maintains architecture)
- Cooldown period to prevent alert storms
- Receives async callback when bundle is ready

**Architectural note:** Unlike having the auditor call incident_agent directly,
secbot is an external automation client (like srebot). Sub-agents remain
independent and don't know about each other.

#### Running secbot

```bash
# Prerequisites: auditd, gateway, and incident_agent must be running

# Start secbot
go run ./cmd/secbot/ \
  --socket /tmp/helpdesk-audit.sock \
  --gateway http://localhost:8080 \
  --listen :9091 \
  --cooldown 5m

# Dry-run mode (log alerts but don't create incidents)
go run ./cmd/secbot/ --socket /tmp/helpdesk-audit.sock --dry-run
```

#### Detected Security Patterns

| Pattern | Trigger |
|---------|---------|
| `hash_mismatch` | Event hash doesn't match content |
| `unauthorized_destructive` | Destructive action without approval |
| `potential_sql_injection` | SQL syntax errors in tool output |
| `potential_command_injection` | Permission denied / command not found errors |

## Compliance Reporting (cmd/govbot/)

The `govbot` is a one-shot compliance reporter that queries the gateway's
governance API endpoints and produces a structured compliance snapshot. It
is designed to run on-demand or on a schedule (daily cron / Kubernetes CronJob)
and optionally post a summary to a Slack webhook.

```
Gateway /api/v1/governance/* → govbot → compliance report + optional Slack alert
```

govbot is stateless and read-only. No audit socket access or cluster privileges
are required — only network access to the gateway.

### Compliance Phases

```
Phase 1 — Governance Status:       GET /api/v1/governance
Phase 2 — Policy Overview:         Detailed policy rule breakdown
Phase 3 — Audit Activity:          GET /api/v1/governance/events?since=...
Phase 4 — Policy Decision Analysis: Per-resource allow/deny/no-match breakdown
Phase 5 — Pending Approvals:       GET /api/v1/governance/approvals/pending
Phase 6 — Chain Integrity:         GET /api/v1/governance/verify
Phase 7 — Compliance Summary:      Aggregated alerts and warnings + optional Slack post
```

### Exit Codes

| Code | Meaning |
|------|---------|
| `0`  | Healthy — no alerts or warnings |
| `1`  | Fatal — could not reach gateway |
| `2`  | Alerts present — chain integrity failure or other critical finding |

Exit code `2` is useful for CI pipelines and cron alerting.

### Detection Logic

**Phase 4 — no_match decisions:** When an agent connects to a database host
that is not listed in `infraConfig`, the policy engine has no tags to evaluate
and falls back to the default deny. These decisions are counted as `no_match`
and raise a warning. The fix is to ensure every host the agents contact appears
in the infrastructure config with appropriate tags.

**Phase 5 — Stale approvals:** Any approval request pending for more than
30 minutes is flagged as stale, indicating the notification channel may not
be working correctly.

**Phase 6 — Chain integrity:** A broken hash chain raises an **alert** (exit 2).

### Running govbot

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

### Scheduling in Kubernetes

govbot is deployed as a CronJob. Enable it in `values.yaml`:

```yaml
governance:
  govbot:
    enabled: true
    schedule: "0 8 * * *"   # daily at 08:00 UTC
    since: "24h"
    webhook: "https://hooks.slack.com/services/..."
```

See `cmd/govbot/README.md` for full documentation.

---

## Explainability

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

### Decision Trace

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

### Human-Readable Explanation

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

### Surfacing the Explanation

The explanation is surfaced in three ways:

#### 1. Inline at the point of denial

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

#### 2. Retrospective — explain a past audit event

```bash
# CLI
govexplain --gateway http://localhost:8080 --event tool_a1b2c3d4

# API
GET /api/v1/governance/events/tool_a1b2c3d4/explain
```

The gateway retrieves the stored `DecisionTrace` from the audit event and
returns it. No re-evaluation is needed — the trace was recorded at the time.

#### 3. Hypothetical — what would happen if?

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

### Audit Enrichment

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

### Gateway API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/governance/explain` | Hypothetical check — what would happen? |
| GET | `/api/v1/governance/events/{id}/explain` | Explain a specific past audit event |

#### Hypothetical check request parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `resource_type` | yes | `database`, `kubernetes` |
| `resource_name` | yes | Resource name (db name, namespace) |
| `action` | yes | `read`, `write`, `destructive` |
| `tags` | no | Comma-separated tags, e.g. `production,critical` |
| `user_id` | no | Evaluate as a specific user |
| `role` | no | Evaluate with a specific role |

#### Response format (both endpoints)

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

### govexplain CLI

A lightweight CLI binary for humans and scripts:

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

### Implementation Plan

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

## Troubleshooting

Please refer to [here](ARCHITECTURE.md#troubleshooting) for the general purpose
troubleshooting tips and known issues beyond AI Governance and Audit.
This troubleshooting section is specific to just these two topics.

#### Events Not Being Recorded

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

#### Auditor Not Receiving Events

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

#### Chain Verification Fails

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

#### Off-Hours Alerts Not Working

The auditor uses local time for off-hours detection. Verify your system
timezone is set correctly:

```bash
date  # Check current local time
```

---

## Roadmap

### Phase 1: Foundation (Complete)
- [x] Audit system with hash chains
- [x] Real-time monitoring (auditor)
- [x] Security alerting (secbot)
- [x] Policy engine (internal/policy/)
- [x] Policy enforcement in agents (database, k8s)

### Phase 2: Enforcement (Complete)
- [x] Approval workflows (cmd/approvals/, auditd API, Slack/email notifications)
- [x] Compliance reporting (cmd/govbot/, Kubernetes CronJob)
- [x] Blast-radius guardrails (`max_rows_affected`, `max_pods_affected`, post-execution hooks)
- [ ] Rate limits (write frequency per session)
- [ ] Circuit breaker (auto-pause on consecutive errors)

### Phase 3: Operations
- [ ] Explainability — decision trace, `govexplain` CLI, explain API endpoints
- [ ] Operating mode switch (`readonly` / `fix`) with governance enforcement
- [ ] Identity & access control (principal/role matching in policy engine)
- [ ] Time-based policy conditions (schedule: days/hours/timezone)
- [ ] Rollback capabilities

### Phase 4: Intelligence
- [ ] Anomaly detection (ML-based)
- [ ] Risk scoring
- [ ] Automated remediation suggestions
