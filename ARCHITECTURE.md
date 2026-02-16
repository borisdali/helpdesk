# Architecture

aiHelpDesk is built on the idea of delegating specialized tasks to the expert sub-agents and coordinating them via the central Orchestrator. Sub-agents don't know or depend on each other, are stateless in nature and receive the requests and funnel their findings strictly through the Orchrestrator. This is powerful because by delegating a subtask to a separate agent, the details of that task in a confined to a separate context window. Once the subtask in question is done, the result is added to your main context window of the Orchtestrator and all the details of the subtask are discarded. This is better for context management because it avoids polution of the Orchtestrator's main context the irrelevant details of the subtasks.  

Sub-agents are standalone A2A servers. That means that if a provider with the deep domain expertise (e.g. K8s) can offer and swap aiHelpDesk's K8s agent with their own, as long it offers Agent Card at `/.well-known/agent-card.json` and abides by the other rules of the A2A protocol. Sub-agents are explicitly stateless — connection strings and Kubernetes contexts are passed per-request, not configured at startup. This means:

- **Multiple Orchestrators** can share the same sub-agent instances
- **Sub-agents can run anywhere** — same machine, different host, container, etc.
- **The Orchestrator manages the infrastructure inventory**, not the sub-agents

```
    ┌──────────────────┐          ┌──────────────────┐
    │  Orchestrator A  │          │  Orchestrator B  │
    │  (team-alpha)    │          │  (team-beta)     │
    │                  │          │                  │
    │ infrastructure:  │          │ infrastructure:  │
    │  - prod-db       │          │  - staging-db    │
    │  - prod-cluster  │          │  - dev-cluster   │
    └────────┬─────────┘          └────────┬─────────┘
             │                             │
             └──────────┬──────────────────┘
                        │  (A2A protocol)
        ┌───────────────┼───────────────────┐
        ▼               ▼                   ▼
  ┌───────────┐   ┌───────────┐   ┌───────────────┐
  │ database  │   │ k8s       │   │ incident      │
  │ agent     │   │ agent     │   │ agent         │
  │ :1100     │   │ :1102     │   │ :1104         │
  │           │   │           │   │               │
  │ 9 psql    │   │ 8 kubectl │   │ 2 bundle      │
  │ tools     │   │ tools     │   │ tools         │
  └───────────┘   └───────────┘   └───────────────┘
```

## Infrastructure Inventory

The Orchestrator loads an infrastructure inventory (`infrastructure.json`) that maps
managed database servers, Kubernetes clusters, and VMs. When the user asks about a
specific system, the Orchestrator passes the right connection string, `kubectl context`,
or VM info to the sub-agent:

```json
{
  "db_servers": {
    "global-corp-db": {
      "name": "Global Corp Production DB",
      "connection_string": "host=db1.example.com port=5432 dbname=prod user=admin",
      "k8s_cluster": "global-prod",
      "k8s_namespace": "database"
    },
    "local-co-db": {
      "name": "Local Company Dev DB",
      "connection_string": "host=db2.local.example.io port=5432 dbname=dev user=dba",
      "vm_name": "vm-db-dev-01"
    }
  },
  "k8s_clusters": {
    "global-prod": {
      "name": "Global Corp Production Cluster",
      "context": "global-prod-cluster"
    }
  },
  "vms": {
    "vm-db-dev-01": {
      "name": "Dev Database VM",
      "host": "db2.local.example.io"
    }
  }
}
```

Each database server runs on either a Kubernetes cluster (with `k8s_cluster` and optional
`k8s_namespace`) or a VM (with `vm_name`) — never both. The `k8s_namespace` defaults to
`"default"` when not specified.

## Agent Discovery

The Orchestrator finds sub-agents in two ways:

1. **Static config** (`agents.json`) — a list of agent names, URLs, and descriptions
2. **Dynamic discovery** (`HELPDESK_AGENT_URLS`) — comma-separated base URLs; the
   Orchestrator fetches each agent's `/.well-known/agent-card.json` to learn its name,
   description, and capabilities

At startup, the Orchestrator health-checks all agents and gracefully handles any that are unavailable.

## Prerequisites

- Go 1.24.4+
- PostgreSQL client (`psql`) for database agent
- `kubectl` configured for K8s agent
- API key for Google AI Studio (Gemini) or Anthropic (Claude)

## Environment Variables

### Required (all agents and Orchestrator)

```bash
# Google Gemini
export HELPDESK_MODEL_VENDOR="google"
export HELPDESK_MODEL_NAME="gemini-2.5-flash"
export HELPDESK_API_KEY="your-google-ai-studio-api-key"

# — or — Anthropic Claude
export HELPDESK_MODEL_VENDOR="anthropic"
export HELPDESK_MODEL_NAME="claude-sonnet-4-20250514"
export HELPDESK_API_KEY="your-anthropic-api-key"
```

### Orchestrator

```bash
# Infrastructure inventory (database servers, K8s clusters, VMs)
export HELPDESK_INFRA_CONFIG="infrastructure.json"

# Agent discovery: static config file (default)
export HELPDESK_AGENTS_CONFIG="agents.json"

# — or — dynamic discovery via agent card URLs
export HELPDESK_AGENT_URLS="http://host1:1100,http://host2:1102,http://host3:1104"
```

### REST Gateway

```bash
# Listen address (default: localhost:8080)
export HELPDESK_GATEWAY_ADDR="0.0.0.0:8080"

# Required: agent discovery (same as Orchestrator dynamic discovery)
export HELPDESK_AGENT_URLS="http://localhost:1100,http://localhost:1102,http://localhost:1104"
```

### Agent-specific

```bash
# Override default listen address for any agent
export HELPDESK_AGENT_ADDR="0.0.0.0:1100"

# Incident agent: output directory for bundles (defaults to current directory)
export HELPDESK_INCIDENT_DIR="/path/to/incidents"
```

The database agent also respects standard PostgreSQL environment variables (`PGHOST`,
`PGPORT`, `PGUSER`, `PGDATABASE`) as fallback defaults when no connection string is
provided.

## Running the System

Each agent is an independent process. Start them in any order — the Orchestrator
health-checks agents at startup and works with whatever is available. Agents can run
on the same machine or on different hosts.

### Start the sub-agents

```bash
# Terminal 1 — database agent (default :1100)
go run ./agents/database/

# Terminal 2 — k8s agent (default :1102)
go run ./agents/k8s/

# Terminal 3 — incident agent (default :1104)
go run ./agents/incident/
```

To run an agent on a different address:
```bash
HELPDESK_AGENT_ADDR="0.0.0.0:2100" go run ./agents/database/
```

### Start the Orchestrator

```bash
# Terminal 4
go run ./cmd/helpdesk/
```

The Orchestrator discovers agents from `agents.json` (or `HELPDESK_AGENT_URLS` for
dynamic discovery), checks each one, and reports availability:

```
Checking agent postgres_database_agent at http://localhost:1100...  OK
Checking agent k8s_agent at http://localhost:1102...  OK
Checking agent incident_agent at http://localhost:1104...  OK
Orchestrator initialized with 3 available agent(s)
```

Unavailable agents are noted but don't prevent the Orchestrator from starting — it
works with the agents that are reachable.

## Available Tools

### PostgreSQL Database Agent (default :1100)

| Tool | Description |
|------|-------------|
| `check_connection` | Test database connectivity, get version/user/server info |
| `get_database_info` | List databases with sizes, owners, recovery status |
| `get_active_connections` | Show running queries from pg_stat_activity |
| `get_connection_stats` | Connection summary: active/idle/waiting per database |
| `get_database_stats` | Commits, rollbacks, cache hit ratio, deadlocks |
| `get_config_parameter` | Query pg_settings for configuration parameters |
| `get_replication_status` | Primary/replica role, replication slots, lag info |
| `get_lock_info` | Find blocking locks and waiting queries |
| `get_table_stats` | Table sizes, dead tuples, vacuum times, scan types |

### Kubernetes Agent (default :1102)

| Tool | Description |
|------|-------------|
| `get_pods` | List pods with namespace/label filtering |
| `get_service` | List services, filter by type (LoadBalancer, etc.) |
| `describe_service` | Detailed service info (selectors, endpoints) |
| `get_endpoints` | Verify backend pod IPs for a service |
| `get_events` | Get events sorted by time, filter by type/resource |
| `get_pod_logs` | Retrieve pod logs with tail/previous support |
| `describe_pod` | Detailed pod info (conditions, events) |
| `get_nodes` | List cluster nodes with status |

### Incident Agent (default :1104)

| Tool | Description |
|------|-------------|
| `create_incident_bundle` | Collect diagnostic data from database, K8s, OS, and storage layers into a `.tar.gz` bundle |
| `list_incidents` | List previously created incident bundles with IDs, timestamps, and file paths |

The incident agent collects data across four layers:
- **Database**: version, connections, stats, replication, locks, table sizes (via `psql`)
- **Kubernetes**: pods, services, endpoints, events, nodes, resource usage (via `kubectl`)
- **OS**: uname, uptime, top, memory, dmesg, sysctl
- **Storage**: disk usage, inodes, mounts, block devices, I/O stats

Bundles are always created even if some commands fail (e.g., database unreachable). Failed commands are recorded as errors inside the bundle. Output directory is controlled by `HELPDESK_INCIDENT_DIR` (defaults to current directory). An `incidents.json` index file tracks all created bundles.

## Verifying Agent Cards

You can verify that agents are running by fetching their agent cards:

```bash
# Database agent
curl -s http://localhost:1100/.well-known/agent-card.json | jq .name
# Output: "postgres_database_agent"

# K8s agent
curl -s http://localhost:1102/.well-known/agent-card.json | jq .name
# Output: "k8s_agent"

# Incident agent
curl -s http://localhost:1104/.well-known/agent-card.json | jq .name
# Output: "incident_agent"
```

## Example Interactions

### Database Troubleshooting

```
User -> My database queries are running slow

Agent -> I'll investigate the database performance. Let me check active
connections and running queries...

[Calls postgres_database_agent with get_active_connections tool]

Based on the results, I can see there are 3 long-running queries...
```

### Kubernetes Troubleshooting

```
User -> I can't connect to my database running on Kubernetes

Agent -> Let me check the Kubernetes service configuration...

[Calls k8s_agent with get_service and get_endpoints tools]

The LoadBalancer service shows EXTERNAL-IP as <pending>, which means...
```

### Combined Troubleshooting

```
User -> My AlloyDB Omni connection times out from outside the cluster

Agent -> This could be a Kubernetes networking issue. Let me check the
service exposure first, then verify the database is accepting connections...

[Calls k8s_agent to check service/endpoints]
[Calls postgres_database_agent to verify database health]
```

### Incident Bundle Creation

```
User -> Create an incident for the global-prod database

Agent -> I'll create a diagnostic bundle for the global-prod infrastructure...

[Calls incident_agent with create_incident_bundle]

Incident bundle created:
- ID: a1b2c3d4
- Bundle: ./incident-a1b2c3d4-20260128-143022.tar.gz
- Layers: database, kubernetes, os, storage
- Some commands could not collect data: database/version.txt (connection refused)
```

## File Structure

```
helpdesk/
├── cmd/
│   ├── helpdesk/            # Orchestrator binary (human-facing)
│   │   ├── main.go          # Entry point, LLM + launcher setup
│   │   ├── orchestrator.go  # Config types, infra loading, prompt building
│   │   └── discovery.go     # Agent card fetching, health checks
│   ├── gateway/             # REST gateway binary (programmatic access)
│   │   ├── main.go          # Entry point, agent discovery, HTTP server
│   │   └── gateway.go       # REST handlers, A2A call proxy, response extraction
│   ├── auditd/              # Central audit service
│   │   └── main.go          # HTTP API, SQLite storage, socket notifications
│   ├── auditor/             # Real-time audit monitor
│   │   └── main.go          # Security alerts, chain verification, webhooks
│   └── srebot/              # SRE bot demo (o11y watcher simulation)
│       └── main.go          # Health check, AI diagnosis, incident + callback
├── agents/
│   ├── database/            # PostgreSQL agent binary
│   │   ├── main.go          # Entry point, uses agentutil SDK + audit
│   │   └── tools.go         # 9 psql tools, runPsql, audit logging
│   ├── k8s/                 # Kubernetes agent binary
│   │   ├── main.go          # Entry point, uses agentutil SDK + audit
│   │   └── tools.go         # 8 kubectl tools, runKubectl, audit logging
│   └── incident/            # Incident diagnostic bundle agent binary
│       ├── main.go          # Entry point, uses agentutil SDK
│       ├── tools.go         # 2 tools, layer collectors, command helpers
│       └── bundle.go        # Manifest, tarball assembly (archive/tar + gzip)
├── agentutil/               # SDK for agent authors
│   └── agentutil.go         # Config, CardOptions, NewLLM, Serve, InitAuditStore
├── internal/
│   ├── audit/               # Audit logging infrastructure
│   │   ├── audit.go         # Event types, action classification
│   │   ├── store.go         # SQLite store, socket notifications
│   │   ├── hash.go          # SHA-256 hash chain computation/verification
│   │   ├── tool_audit.go    # ToolAuditor wrapper for agent tool calls
│   │   └── trace.go         # Trace ID store for request correlation
│   ├── discovery/           # Shared agent card discovery
│   │   └── discovery.go     # Fetch and parse /.well-known/agent-card.json
│   ├── model/anthropic.go   # Anthropic LLM adapter
│   └── logging/logging.go   # Shared log setup
├── prompts/                 # Agent instruction files
│   ├── prompts.go           # Embeds all .txt files
│   ├── orchestrator.txt
│   ├── database.txt
│   ├── k8s.txt
│   └── incident.txt
├── testing/                 # Failure injection testing framework
│   ├── catalog/failures.yaml
│   ├── docker/              # Docker Compose test infrastructure
│   ├── k8s/                 # Kustomize base + failure overlays
│   ├── sql/                 # Injection/teardown scripts
│   ├── cmd/faulttest/       # Test harness CLI
│   └── testutil/            # Docker, K8s, psql, A2A helpers
├── agents.json              # Static agent endpoint config
├── infrastructure.json      # Managed infrastructure inventory
├── go.mod
├── go.sum
└── README.md
```

## Extending the System

### Adding a New Agent

1. Create a new directory under `agents/` (e.g., `agents/myagent/`)
2. Write `main.go` using the `agentutil` SDK:
   ```go
   package main

   import (
       "context"
       "helpdesk/agentutil"
       "google.golang.org/adk/agent/llmagent"
   )

   func main() {
       cfg := agentutil.MustLoadConfig("localhost:1200")
       ctx := context.Background()
       llm, _ := agentutil.NewLLM(ctx, cfg)
       // create tools with functiontool.New(...)
       agent, _ := llmagent.New(llmagent.Config{...})
       agentutil.Serve(ctx, agent, cfg)
   }
   ```
3. Define tools in `tools.go` using `functiontool.New()` with the signature:
   ```go
   func myTool(ctx tool.Context, args MyArgs) (MyResult, error)
   ```
4. Add the agent's URL to `agents.json` or `HELPDESK_AGENT_URLS`

### Adding Tools to Existing Agents

1. Define the args struct with JSON schema tags
2. Implement the tool function returning `(ResultStruct, error)`
3. Create the tool with `functiontool.New()`
4. Add to the agent's `Tools` slice in `createTools()`

## Agent-to-Agent Integration

The helpdesk sub-agents can be called directly by upstream programmatic agents
(e.g., an observability agent, a CI/CD pipeline, or a chatbot). The Orchestrator
is a UX layer for humans — external agents should bypass it and talk A2A to the
sub-agents directly. There are two integration paths: native A2A and the REST
gateway.

### Direct A2A Integration

Each sub-agent serves an agent card at `/.well-known/agent-card.json` that
describes its capabilities, tools, tags, and example prompts. An upstream agent
can discover sub-agents dynamically:

1. **Discover**: Fetch `http://<agent-host>:<port>/.well-known/agent-card.json`
2. **Inspect skills**: Each skill has `tags` (e.g., `"postgresql"`, `"kubernetes"`,
   `"incident"`) and `examples` to help the caller decide which agent to use
3. **Call**: Send a JSON-RPC `message/send` request to the agent's `url` field

#### Agent Card Schema (key fields)

| Field                | Description                                       |
|----------------------|---------------------------------------------------|
| `name`               | Agent identifier (e.g., `postgres_database_agent`) |
| `description`        | What the agent does                                |
| `url`                | JSON-RPC invoke endpoint                           |
| `version`            | Agent version                                      |
| `provider`           | Organization info                                  |
| `skills[].id`        | Unique skill identifier                            |
| `skills[].tags`      | Keywords (e.g., `"postgresql"`, `"locks"`)         |
| `skills[].examples`  | Example prompts                                    |

#### Example: A2A JSON-RPC call

```bash
curl -X POST http://localhost:1100/invoke \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "message/send",
    "params": {
      "message": {
        "messageId": "msg-001",
        "role": "user",
        "parts": [{"kind": "text", "text": "Check database connectivity for host=db1.example.com port=5432 dbname=prod user=admin"}]
      }
    }
  }'
```

#### Example: O11y Agent Workflow

An observability agent detects a database anomaly and investigates automatically:

1. **Detect**: O11y system fires an alert for high query latency on `prod-db`
2. **Discover**: Fetch agent cards from known helpdesk URLs, find
   `postgres_database_agent` by matching the `postgresql` tag
3. **Diagnose**: Send A2A message to database agent: "Check active connections
   and lock contention for host=db1.example.com..."
4. **Analyze**: Parse the agent's response (text in task history/artifacts)
5. **Escalate**: If the issue warrants it, call `incident_agent` with
   `create_incident_bundle` (including `callback_url` for async notification)
6. **Notify**: The incident agent POSTs the bundle result to the callback URL
   when complete

#### Callback URL (fire-and-forget)

The incident agent's `create_incident_bundle` tool supports an optional
`callback_url` parameter. When set, the agent POSTs the `IncidentBundleResult`
JSON to that URL after the bundle is created. This is best-effort: callback
failures are logged but do not affect the tool result.

### REST Gateway

For consumers that prefer plain REST over JSON-RPC, the optional REST gateway
(`cmd/gateway/`) provides HTTP endpoints that proxy to the A2A sub-agents:

| Method | Endpoint              | Description                              |
|--------|-----------------------|------------------------------------------|
| GET    | `/api/v1/agents`      | List discovered agents + cards           |
| POST   | `/api/v1/query`       | Send natural language message to an agent |
| POST   | `/api/v1/incidents`   | Create incident bundle                   |
| GET    | `/api/v1/incidents`   | List incident bundles                    |
| POST   | `/api/v1/db/{tool}`   | Call database agent tool                 |
| POST   | `/api/v1/k8s/{tool}`  | Call K8s agent tool                      |

Start the gateway:
```bash
HELPDESK_AGENT_URLS="http://localhost:1100,http://localhost:1102,http://localhost:1104" \
  go run ./cmd/gateway/
```

Example REST calls:
```bash
# List available agents
curl -s http://localhost:8080/api/v1/agents | jq '.[].name'

# Ask the AI agent a natural language question
curl -s http://localhost:8080/api/v1/query \
  -d '{"agent": "database", "message": "Why am I getting too many clients errors? The connection_string is `host=localhost port=5432 dbname=prod user=admin`."}'

# Check database connectivity
curl -X POST http://localhost:8080/api/v1/db/check_connection \
  -d '{"connection_string": "host=db1.example.com port=5432 dbname=prod user=admin"}'

# Create an incident bundle
curl -X POST http://localhost:8080/api/v1/incidents \
  -d '{"description": "High latency on prod-db", "connection_string": "host=db1.example.com port=5432 dbname=prod user=admin"}'

# List past incidents
curl -s http://localhost:8080/api/v1/incidents
```

**Note**: Each REST call triggers an LLM invocation in the sub-agent, which adds
latency and cost compared to direct A2A calls. The REST gateway is best suited
for integration testing, simple automation, and consumers that don't implement
A2A natively.

## Audit System

The helpdesk includes a tamper-evident audit system that records all tool executions
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

## Troubleshooting

### Agent Unavailable

If the Orchestrator reports an agent as unavailable:
1. Check if the agent process is running
2. Verify the port is not in use by another process
3. Check firewall rules if running on different machines

### psql/kubectl Not Found

Ensure the respective CLI tools are installed and in your PATH:
```bash
which psql    # Should return path to psql
which kubectl # Should return path to kubectl
```

### API Key Issues

Verify your API key is set correctly:
```bash
echo $HELPDESK_API_KEY
```

### Audit System Issues

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
