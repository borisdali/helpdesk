# Helpdesk Multi-Agent System

A Go-based multi-agent self-service help system for troubleshooting PostgreSQL databases and Kubernetes infrastructure. Built using Google ADK and the A2A (Agent-to-Agent) protocol.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                 helpdesk_orchestrator                    │
│                    (port 8080)                          │
│  Routes user queries to specialized sub-agents          │
│  based on problem domain (database vs infrastructure)   │
└────────────────────────┬────────────────────────────────┘
                         │
            ┌────────────┴────────────┐
            ▼                         ▼
┌───────────────────────┐   ┌───────────────────────┐
│ postgres_database_    │   │      k8s_agent        │
│ agent (port 1100)     │   │     (port 1102)       │
│                       │   │                       │
│ 9 psql-based tools    │   │ 8 kubectl-based tools │
│ for database          │   │ for Kubernetes        │
│ troubleshooting       │   │ troubleshooting       │
└───────────────────────┘   └───────────────────────┘
```

## Prerequisites

- Go 1.24.4+
- PostgreSQL client (`psql`) for database agent
- `kubectl` configured for K8s agent
- Google AI Studio API key

## Environment Variables

```bash
export HELPDESK_API_KEY="your-google-ai-studio-api-key"
export HELPDESK_MODEL_NAME="gemini-2.5-flash"
```

For the database agent, you can also set standard PostgreSQL environment variables:
```bash
export PGHOST="localhost"
export PGPORT="5432"
export PGUSER="postgres"
export PGDATABASE="postgres"
```

## Running the System

### 1. Start the Database Agent (Terminal 1)

```bash
cd ~/cassiopeia/helpdesk
go run helpdesk-database-agent.go
```

Output:
```
Starting Database A2A server on http://localhost:1100
Agent card available at: http://localhost:1100/.well-known/agent-card.json
```

### 2. Start the K8s Agent (Terminal 2)

```bash
cd ~/cassiopeia/helpdesk
go run helpdesk-k8s-agent.go
```

Output:
```
Starting K8s A2A server on http://localhost:1102
Agent card available at: http://localhost:1102/.well-known/agent-card.json
```

### 3. Start the Orchestrator (Terminal 3)

```bash
cd ~/cassiopeia/helpdesk
go run helpdesk.go
```

Output:
```
Checking agent postgres_database_agent at http://localhost:1100...
  OK: Agent postgres_database_agent is available
Checking agent k8s_agent at http://localhost:1102...
  OK: Agent k8s_agent is available
Orchestrator initialized with 2 available agent(s)
```

The orchestrator will check agent health at startup and gracefully handle unavailable agents.

## Available Tools

### PostgreSQL Database Agent (port 1100)

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

### Kubernetes Agent (port 1102)

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

## Verifying Agent Cards

You can verify that agents are running by fetching their agent cards:

```bash
# Database agent
curl -s http://localhost:1100/.well-known/agent-card.json | jq .name
# Output: "postgres_database_agent"

# K8s agent
curl -s http://localhost:1102/.well-known/agent-card.json | jq .name
# Output: "k8s_agent"
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

## File Structure

```
helpdesk/
├── helpdesk.go                      # Main orchestrator
├── helpdesk-database-agent.go       # PostgreSQL troubleshooting agent
├── helpdesk-k8s-agent.go            # Kubernetes troubleshooting agent
├── helpdesk-postgres-database-agent.go  # (legacy) Alternative DB agent
├── go.mod
├── go.sum
└── README.md
```

## Extending the System

### Adding a New Agent

1. Create a new file (e.g., `helpdesk-newagent.go`)
2. Define tools using `functiontool.New()` with the signature:
   ```go
   func myTool(ctx tool.Context, args MyArgs) (MyResult, error)
   ```
3. Create the agent with `llmagent.New()` and include your tools
4. Expose via A2A using the same pattern as existing agents
5. Add the agent config to the orchestrator's `agents` slice

### Adding Tools to Existing Agents

1. Define the args struct with JSON schema tags
2. Implement the tool function returning `(ResultStruct, error)`
3. Create the tool with `functiontool.New()`
4. Add to the agent's `Tools` slice

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
