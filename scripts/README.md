# aiHelpDesk Helper Scripts

These scripts provide workarounds for the ADK REPL bug in containerized environments and make it easier to interact with aiHelpDesk.

## gateway-repl.sh

An interactive REPL-like interface for the Gateway REST API. This is the **recommended** way to interact with aiHelpDesk when running in Docker or Kubernetes.

### Usage

```bash
# Start port-forward to the gateway (if running in K8s)
kubectl -n helpdesk-system port-forward svc/helpdesk-gateway 8080:8080 &

# Run the interactive REPL
./scripts/gateway-repl.sh [gateway-url]

# Examples:
./scripts/gateway-repl.sh                        # Uses http://localhost:8080
./scripts/gateway-repl.sh http://gateway:8080    # Custom URL
```

### Features

**Infrastructure-Aware Queries:**
- `/databases` - List all managed databases from infrastructure.json
- `/infra` - Show infrastructure summary
- Natural language queries automatically detect mentioned databases and inject connection details

**Agent Tools:**
- `/agents` - List available agents and their skills
- `/tools` - List available tools for database and K8s agents
- `/db <tool> <json>` - Call database agent tool directly
- `/k8s <tool> <json>` - Call K8s agent tool directly

**Smart Routing:**
- Queries mentioning pods, k8s, nodes, deploy → K8s agent
- Queries mentioning incidents, bundles → Incident agent
- Queries about database status (up/running/healthy) for K8s-hosted DBs → K8s agent
- Everything else → Database agent

### Example Session

```
$ ./scripts/gateway-repl.sh

aiHelpDesk Gateway REPL
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Type natural language queries (auto-routes to db/k8s/incident agent based on keywords)
Commands: /databases, /agents, /tools, /db <tool>, /k8s <tool>, /help, /quit

User -> /databases

Managed Databases (1):

  staging-db - Staging Database
    Connection: host=pg-cluster-minkube-1.db port=5432 dbname=app user=app
    Hosting: Kubernetes: Local cluster, namespace: db

User -> is the staging-db up?

Agent (k8s):

The staging-db pods are healthy. Found 1 pod running in the db namespace:
- pg-cluster-minkube-1: Running (1/1 ready)

User -> /db get_server_info {"connection_string": "host=localhost port=5432 dbname=mydb"}

[completed]

PostgreSQL version: 16.1
Uptime: 3 days 4 hours
...

User -> /quit
Goodbye!
```

### Prerequisites

- `curl` and `jq` installed
- Gateway accessible (port-forward if running in K8s)
- For infrastructure features: `HELPDESK_INFRA_CONFIG` set in gateway

---

## k8s-local-repl.sh

Runs the helpdesk orchestrator locally while agents remain in K8s. This provides the full orchestrator experience with multi-agent coordination, bypassing the ADK REPL container bug.

### Usage

```bash
./scripts/k8s-local-repl.sh [namespace]

# Examples:
./scripts/k8s-local-repl.sh                    # Uses helpdesk-system namespace
./scripts/k8s-local-repl.sh my-namespace       # Custom namespace
```

### What It Does

1. Loads environment from `.env` file (if present)
2. Port-forwards all three agent services from K8s to localhost
3. Fetches `infrastructure.json` from the K8s ConfigMap
4. Builds the orchestrator binary if not found
5. Runs the orchestrator locally with full agent connectivity
6. Cleans up port-forwards on exit

### Prerequisites

- `kubectl` configured with cluster access
- `HELPDESK_API_KEY` set (or in `.env` file)
- Go toolchain (if orchestrator binary needs to be built)
- Agents running in K8s

### Example

```bash
$ ./scripts/k8s-local-repl.sh helpdesk-system

[local-repl] Loading .env
[local-repl] Using binary: ./helpdesk
[local-repl] Namespace: helpdesk-system
[local-repl] Model: anthropic/claude-haiku-4-5-20251001
[local-repl] Checking agent pods...
[local-repl] Starting port-forwards...
[local-repl] Port-forwards active:
[local-repl]   - Database agent: localhost:1100
[local-repl]   - K8s agent:      localhost:1102
[local-repl]   - Incident agent: localhost:1104
[local-repl] Fetching infrastructure config from ConfigMap...
[local-repl] Infrastructure config: /tmp/helpdesk-infrastructure.json

[local-repl] Starting interactive orchestrator...
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

time=... level=INFO msg="discovered agent" name=postgres_database_agent
time=... level=INFO msg="discovered agent" name=k8s_agent
time=... level=INFO msg="discovered agent" name=incident_agent
time=... level=INFO msg="infrastructure config loaded" db_servers=1

User -> What databases do you manage?
...
```

---

## When to Use Which Script

| Scenario | Recommended Script |
|----------|-------------------|
| Quick queries, tool calls | `gateway-repl.sh` |
| CI/CD, automation | `gateway-repl.sh` (or direct curl) |
| Multi-agent orchestration | `k8s-local-repl.sh` |
| Complex troubleshooting sessions | `k8s-local-repl.sh` |
| Container environment (Docker/K8s) | `gateway-repl.sh` |
| Full orchestrator features | `k8s-local-repl.sh` |

---

## Troubleshooting

### gateway-repl.sh: "Cannot connect to Gateway"

```bash
# Check if gateway is running
kubectl -n helpdesk-system get pods -l app.kubernetes.io/component=gateway

# Start port-forward
kubectl -n helpdesk-system port-forward svc/helpdesk-gateway 8080:8080 &
```

### gateway-repl.sh: "No databases configured"

The gateway needs `HELPDESK_INFRA_CONFIG` set. Ensure your Helm values include infrastructure:

```bash
helm upgrade helpdesk ./deploy/helm/helpdesk \
    --namespace helpdesk-system \
    --reuse-values \
    --set-json 'infrastructure={...}'
```

### k8s-local-repl.sh: "Port-forward failed"

```bash
# Check if agents are running
kubectl -n helpdesk-system get pods

# Check for existing port-forwards
lsof -i :1100,:1102,:1104

# Kill stale port-forwards
pkill -f "port-forward.*helpdesk"
```

### k8s-local-repl.sh: "HELPDESK_API_KEY is not set"

```bash
# Set directly
export HELPDESK_API_KEY=your-key

# Or create .env file
echo "HELPDESK_API_KEY=your-key" >> .env
```
