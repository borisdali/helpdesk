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

## 1. Infrastructure Inventory

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

## 2. Agent Discovery

The Orchestrator finds sub-agents in two ways:

1. **Static config** (`agents.json`) — a list of agent names, URLs, and descriptions
2. **Dynamic discovery** (`HELPDESK_AGENT_URLS`) — comma-separated base URLs; the
   Orchestrator fetches each agent's `/.well-known/agent-card.json` to learn its name,
   description, and capabilities

At startup, the Orchestrator health-checks all agents and gracefully handles any that are unavailable.

## 3. Prerequisites

- Go 1.24.4+
- PostgreSQL client (`psql`) for database agent
- `kubectl` configured for K8s agent
- API key for Google AI Studio (Gemini) or Anthropic (Claude)

## 4. Environment Variables

### 4.1 Required (all agents and Orchestrator)

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

### 4.2 Orchestrator

```bash
# Infrastructure inventory (database servers, K8s clusters, VMs)
export HELPDESK_INFRA_CONFIG="infrastructure.json"

# Agent discovery: static config file (default)
export HELPDESK_AGENTS_CONFIG="agents.json"

# — or — dynamic discovery via agent card URLs
export HELPDESK_AGENT_URLS="http://host1:1100,http://host2:1102,http://host3:1104"
```

### 4.3 REST Gateway

```bash
# Listen address (default: localhost:8080)
export HELPDESK_GATEWAY_ADDR="0.0.0.0:8080"

# Required: agent discovery (same as Orchestrator dynamic discovery)
export HELPDESK_AGENT_URLS="http://localhost:1100,http://localhost:1102,http://localhost:1104"
```

### 4.4 Agent-specific

```bash
# Override default listen address for any agent
export HELPDESK_AGENT_ADDR="0.0.0.0:1100"

# Incident agent: output directory for bundles (defaults to current directory)
export HELPDESK_INCIDENT_DIR="/path/to/incidents"
```

The database agent also respects standard PostgreSQL environment variables (`PGHOST`,
`PGPORT`, `PGUSER`, `PGDATABASE`) as fallback defaults when no connection string is
provided.

## 5. Running the System

Each agent is an independent process. Start them in any order — the Orchestrator
health-checks agents at startup and works with whatever is available. Agents can run
on the same machine or on different hosts.

### 5.1 Start the sub-agents

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

### 5.2 Start the Orchestrator

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

## 6. Available Tools

### 6.1 PostgreSQL Database Agent (default :1100)

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

### 6.2 Kubernetes Agent (default :1102)

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

### 6.3 Incident Agent (default :1104)

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

## 7. Verifying Agent Cards

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

## 8. Example Interactions

### 8.1 Database Troubleshooting

```
User -> My database queries are running slow

Agent -> I'll investigate the database performance. Let me check active
connections and running queries...

[Calls postgres_database_agent with get_active_connections tool]

Based on the results, I can see there are 3 long-running queries...
```

### 8.2 Kubernetes Troubleshooting

```
User -> I can't connect to my database running on Kubernetes

Agent -> Let me check the Kubernetes service configuration...

[Calls k8s_agent with get_service and get_endpoints tools]

The LoadBalancer service shows EXTERNAL-IP as <pending>, which means...
```

### 8.3 Combined Troubleshooting

```
User -> My AlloyDB Omni connection times out from outside the cluster

Agent -> This could be a Kubernetes networking issue. Let me check the
service exposure first, then verify the database is accepting connections...

[Calls k8s_agent to check service/endpoints]
[Calls postgres_database_agent to verify database health]
```

### 8.4 Incident Bundle Creation

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

## 9. File Structure

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
│   ├── secbot/              # Security responder bot
│   │   └── main.go          # Monitors audit stream, creates incident bundles
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
│   ├── policy/              # AI Governance policy engine
│   │   ├── types.go         # Policy, Rule, Condition structs
│   │   ├── loader.go        # YAML policy file loading
│   │   └── engine.go        # Policy evaluation logic
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

## 10. Extending the System

### 10.1 Adding a New Agent

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

### 10.2 Adding Tools to Existing Agents

1. Define the args struct with JSON schema tags
2. Implement the tool function returning `(ResultStruct, error)`
3. Create the tool with `functiontool.New()`
4. Add to the agent's `Tools` slice in `createTools()`

## 11. Agent-to-Agent Integration

The helpdesk sub-agents can be called directly by upstream programmatic agents
(e.g., an observability agent, a CI/CD pipeline, or a chatbot). The Orchestrator
is a UX layer for humans — external agents should bypass it and talk A2A to the
sub-agents directly. There are two integration paths: native A2A and the REST
Gateway.

### 11.1 Direct A2A Integration

Each sub-agent serves an agent card at `/.well-known/agent-card.json` that
describes its capabilities, tools, tags, and example prompts. An upstream agent
can discover sub-agents dynamically:

1. **Discover**: Fetch `http://<agent-host>:<port>/.well-known/agent-card.json`
2. **Inspect skills**: Each skill has `tags` (e.g., `"postgresql"`, `"kubernetes"`,
   `"incident"`) and `examples` to help the caller decide which agent to use
3. **Call**: Send a JSON-RPC `message/send` request to the agent's `url` field

#### 11.1.1 Agent Card Schema (key fields)

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

#### 11.1.2 Example: A2A JSON-RPC call

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

#### 11.1.3 Example: O11y Agent Workflow

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

#### 11.1.4 Callback URL (fire-and-forget)

The incident agent's `create_incident_bundle` tool supports an optional
`callback_url` parameter. When set, the agent POSTs the `IncidentBundleResult`
JSON to that URL after the bundle is created. This is best-effort: callback
failures are logged but do not affect the tool result.

### 11.2 REST Gateway

For consumers that prefer plain REST API over JSON-RPC, the optional REST Gateway
(`cmd/gateway/`) provides HTTP endpoints that proxy to the A2A sub-agents:

| Method | Endpoint              | Description                              |
|--------|-----------------------|------------------------------------------|
| GET    | `/api/v1/agents`      | List discovered agents + cards           |
| POST   | `/api/v1/query`       | Send natural language message to an agent |
| POST   | `/api/v1/incidents`   | Create incident bundle                   |
| GET    | `/api/v1/incidents`   | List incident bundles                    |
| POST   | `/api/v1/db/{tool}`   | Call database agent tool                 |
| POST   | `/api/v1/k8s/{tool}`  | Call K8s agent tool                      |

Start the Gateway:
```bash
HELPDESK_AGENT_URLS="http://localhost:1100,http://localhost:1102,http://localhost:1104" \
  go run ./cmd/gateway/
```

Example REST API calls:
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
latency and cost compared to direct A2A calls. The REST Gateway is best suited
for integration testing, simple automation, and consumers that don't implement
A2A natively.

## 12. AI Governance (including full audit) System
aiHelpDesk is proud to feature a sophisticated AI Governanance system,
which relies on comprehensive real-time and after the fact persistant
auditing. Please see [here](AIGOVERNANCE.md) for details.

## 13 Troubleshooting

### 13.1 Agent Unavailable

If the Orchestrator reports an agent as unavailable:
1. Check if the agent process is running
2. Verify the port is not in use by another process
3. Check firewall rules if running on different machines

### 13.2 psql/kubectl Not Found

Ensure the respective CLI tools are installed and in your PATH:
```bash
which psql    # Should return path to psql
which kubectl # Should return path to kubectl
```

### 13.3 API Key Issues

Verify your API key is set correctly:
```bash
echo $HELPDESK_API_KEY
```

### 13.4 AI Governance (and audit in particular) System Issues
See the [here](AIGOVERNANCE.md#audit-system-issues) for known/reported issues
pertaining to AI Governance in general and the built-in
audit system in particular.

