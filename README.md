# aiHelpDesk Multi-Agent System

An AI Go-based multi-agent self-service help system for troubleshooting PostgreSQL databases and Kubernetes infrastructure, with incident diagnostic bundle creation for vendor support. Built using Google ADK and the A2A (Agent-to-Agent) protocol.

## Architecture

Sub-agents are standalone A2A servers. They are stateless — connection strings and
Kubernetes contexts are passed per-request, not configured at startup. This means:

- **Multiple orchestrators** can share the same sub-agent instances
- **Sub-agents can run anywhere** — same machine, different host, container, etc.
- **The orchestrator manages the infrastructure inventory**, not the sub-agents

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

### Infrastructure Inventory

The orchestrator loads an infrastructure inventory (`infrastructure.json`) that maps
managed PostgreSQL servers and Kubernetes clusters. When the user asks about a specific
system, the orchestrator passes the right connection string or kubectl context to the
sub-agent:

```json
{
  "postgres_servers": {
    "global-corp-db": {
      "name": "Global Corp Production DB",
      "connection_string": "host=db1.example.com port=5432 dbname=prod user=admin",
      "k8s_cluster": "global-prod"
    }
  },
  "k8s_clusters": {
    "global-prod": {
      "name": "Global Corp Production Cluster",
      "context": "global-prod-cluster"
    }
  }
}
```

### Agent Discovery

The orchestrator finds sub-agents in two ways:

1. **Static config** (`agents.json`) — a list of agent names, URLs, and descriptions
2. **Dynamic discovery** (`HELPDESK_AGENT_URLS`) — comma-separated base URLs; the
   orchestrator fetches each agent's `/.well-known/agent-card.json` to learn its name,
   description, and capabilities

At startup, the orchestrator health-checks all agents and gracefully handles any that
are unavailable.

## Prerequisites

- Go 1.24.4+
- PostgreSQL client (`psql`) for database agent
- `kubectl` configured for K8s agent
- API key for Google AI Studio (Gemini) or Anthropic (Claude)

## Environment Variables

### Required (all agents and orchestrator)

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
# Infrastructure inventory (PostgreSQL servers + K8s clusters)
export HELPDESK_INFRA_CONFIG="infrastructure.json"

# Agent discovery: static config file (default)
export HELPDESK_AGENTS_CONFIG="agents.json"

# — or — dynamic discovery via agent card URLs
export HELPDESK_AGENT_URLS="http://host1:1100,http://host2:1102,http://host3:1104"
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

Each agent is an independent process. Start them in any order — the orchestrator
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

### Start the orchestrator

```bash
# Terminal 4
go run ./cmd/helpdesk/
```

The orchestrator discovers agents from `agents.json` (or `HELPDESK_AGENT_URLS` for
dynamic discovery), checks each one, and reports availability:

```
Checking agent postgres_database_agent at http://localhost:1100...  OK
Checking agent k8s_agent at http://localhost:1102...  OK
Checking agent incident_agent at http://localhost:1104...  OK
Orchestrator initialized with 3 available agent(s)
```

Unavailable agents are noted but don't prevent the orchestrator from starting — it
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
├── cmd/helpdesk/            # Orchestrator binary
│   ├── main.go              # Entry point, LLM + launcher setup
│   ├── orchestrator.go      # Config types, infra loading, prompt building
│   └── discovery.go         # Agent card fetching, health checks
├── agents/
│   ├── database/            # PostgreSQL agent binary
│   │   ├── main.go          # Entry point, uses agentutil SDK
│   │   └── tools.go         # 9 psql tools, runPsql, diagnosePsqlError
│   ├── k8s/                 # Kubernetes agent binary
│   │   ├── main.go          # Entry point, uses agentutil SDK
│   │   └── tools.go         # 8 kubectl tools, runKubectl, diagnoseKubectlError
│   └── incident/            # Incident diagnostic bundle agent binary
│       ├── main.go          # Entry point, uses agentutil SDK
│       ├── tools.go         # 2 tools, layer collectors, command helpers
│       └── bundle.go        # Manifest, tarball assembly (archive/tar + gzip)
├── agentutil/               # SDK for agent authors
│   └── agentutil.go         # Config, NewLLM, Serve
├── internal/
│   ├── model/anthropic.go   # Anthropic LLM adapter
│   └── logging/logging.go   # Shared log setup
├── prompts/                 # Agent instruction files
│   ├── prompts.go           # Embeds all .txt files
│   ├── orchestrator.txt
│   ├── database.txt
│   ├── k8s.txt
│   └── incident.txt
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

## Troubleshooting

### Agent Unavailable

If the orchestrator reports an agent as unavailable:
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
