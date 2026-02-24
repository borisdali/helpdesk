# aiHelpDesk: Deployment on Kubernetes

## 1. Prerequisites

- Kubernetes cluster (1.24+)
- Helm 3.x
- `kubectl` configured to access your cluster

## 2. Quick Start (deployment from prebuilt binaries)

Similar to [VM-based Deployment](../docker-compose/README.md), the binary route is simpler and doesn't require cloning of the repo, but see deployment from source below if desired:

```bash
# Extract the deploy bundle
tar xzf helpdesk-v0.1.0-deploy.tar.gz
cd helpdesk-v0.1.0-deploy

# Create the API key secret
kubectl create namespace helpdesk-system
kubectl -n helpdesk-system create secret generic helpdesk-api-key \
    --from-literal=api-key=<YOUR_API_KEY>

# Install the Helm chart
helm install helpdesk ./helm/helpdesk \
    --namespace helpdesk-system \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001

# Start the interactive session:
kubectl -n helpdesk-system exec -it deploy/helpdesk-orchestrator -- helpdesk
```

Please see below for details and in particular on how to setup and
pass infrastructure.json to the Orchestrator (automatically created
by aiHelpDesk via a ConfigMap from the Helm's `my-values.yaml`).

## 3. Configuration

### 3.1 Model Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `model.vendor` | LLM provider: `anthropic` or `gemini` | `anthropic` |
| `model.name` | Model name (see below) | `claude-haiku-4-5-20251001` |
| `model.apiKeySecret` | Name of K8s Secret containing API key | `helpdesk-api-key` |
| `model.apiKeyKey` | Key within the Secret | `api-key` |

**Supported model names:**
- **Anthropic:** `claude-haiku-4-5-20251001`, `claude-sonnet-4-20250514`, `claude-opus-4-5-20251101`
- **Gemini:** `gemini-2.5-flash`, `gemini-2.5-flash-lite`, `gemini-2.5-pro`, `gemini-3-flash-preview`, `gemini-3-pro-preview`

**Note:** Gemini 1.x and 2.0 models are retired and will return errors.

### 3.2 K8s Agent Cluster Access

The K8s agent automatically uses **in-cluster configuration** when deployed in Kubernetes. This means:

- **Same cluster access**: Use empty context (`""`) in `infrastructure.json` for databases running in the same cluster as aiHelpDesk. The agent uses its service account (configured via RBAC in the Helm chart).

- **Multi-cluster access**: To query other clusters, mount a kubeconfig file:
  ```bash
  # Create secret from kubeconfig
  kubectl -n helpdesk-system create secret generic kubeconfig \
    --from-file=config=$HOME/.kube/config
  ```
  Then reference the context name in `infrastructure.json`.

Example `infrastructure.json` with both:
```json
{
  "db_servers": {
    "local-db": {
      "name": "Database in same cluster",
      "k8s_cluster": "local",
      "k8s_namespace": "db"
    },
    "remote-db": {
      "name": "Database in GKE",
      "k8s_cluster": "gke-prod",
      "k8s_namespace": "postgres"
    }
  },
  "k8s_clusters": {
    "local": {
      "name": "Local (same cluster)",
      "context": ""
    },
    "gke-prod": {
      "name": "Production GKE",
      "context": "gke_myproject_us-central1_prod"
    }
  }
}
```

### 3.3 Infrastructure Inventory

The Orchestrator needs to know which databases it manages. Configure this via `values.yaml` or `--set-json`.
While it's possible for the human operator's interactive session to ask adhoc questions about "unknown"
databases (subject to the rules defined by an administrator in the governance module), it's
more convenient, secure and structured to define the set of databases ahead of time:

**3.3.1 Option A: Using a values file**

Create `my-values.yaml`:
```yaml
image:
  repository: ghcr.io/borisdali/helpdesk
  tag: v0.1.0
  pullPolicy: IfNotPresent

# LLM model configuration. All agents and the orchestrator use these.
model:
  vendor: anthropic
  name: claude-haiku-4-5-20251001
...
...
# Infrastructure inventory — database servers, K8s clusters, and VMs.
# See helm/infrastructure.json.example for details.
infrastructure:
  db_servers:
    prod-orders-db:
      name: "Orders Production Database"
      connection_string: "host=orders-db.prod.example.com port=5432 dbname=orders user=app_user"
      k8s_cluster: "prod-cluster"
      k8s_namespace: "orders"
    prod-users-db:
      name: "Users Production Database"
      connection_string: "host=users-db.prod.example.com port=5432 dbname=users user=app_user"
      k8s_cluster: "prod-cluster"
      k8s_namespace: "users"
    legacy-analytics-db:
      name: "Legacy Analytics (VM)"
      connection_string: "host=analytics.internal.example.com port=5432 dbname=analytics user=etl_user"
      vm_name: "analytics-vm"
  k8s_clusters:
    prod-cluster:
      name: "Production GKE Cluster"
      context: "gke_myproject_us-central1_prod"
  vms:
    analytics-vm:
      name: "Analytics Server (on-prem)"
      host: "analytics.internal.example.com"
```

Then install:
```bash
cd helpdesk-v0.1.0-deploy
helm install helpdesk ./helm/helpdesk \
    --namespace helpdesk-system \
    -f ./helm/helpdesk/my-values.yaml
```

**3.3.2 Option B: Using --set-json (for simple configs)**

```bash
helm install helpdesk ./helm/helpdesk \
    --namespace helpdesk-system \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001 \
    --set-json 'infrastructure={"db_servers":{"mydb":{"name":"My Database","connection_string":"host=db.example.com port=5432 dbname=app user=admin"}}}'
```

See `infrastructure.json.example` in the bundle for a complete reference.

### 3.4 Database Authentication

The database agent runs `psql` to connect to PostgreSQL. To avoid password prompts, use a `.pgpass` file.

**Step 1: Create the `.pgpass` file**

```bash
# Format: hostname:port:database:username:password
# Use '*' as a wildcard for any field
cat > pgpass << 'EOF'
db.example.com:5432:mydb:myuser:mypassword
db2.example.com:5432:*:admin:adminpassword
EOF
# Note: do NOT chmod here — Kubernetes handles permissions automatically
```

> **Important:** The hostname must **exactly match** the hostname in your `infrastructure.json` connection strings (e.g., if the connection string says `host=db.example.com`, then `.pgpass` must also say `db.example.com`).

**Step 2: Create a Kubernetes Secret**

```bash
kubectl -n helpdesk-system create secret generic pgpass \
    --from-file=.pgpass=./.pgpass
```

The chart mounts this secret at `/home/helpdesk/.pgpass` with `0600` permissions automatically.

**Step 3: Reference it in `values.yaml`**

```yaml
agents:
  database:
    pgpassSecret: pgpass
```

Or pass it inline:

```bash
helm install helpdesk ./helm/helpdesk \
    --namespace helpdesk-system \
    --set agents.database.pgpassSecret=pgpass \
    ...
```

### 3.5 Custom Namespace

Install to any namespace using `--namespace` and `--create-namespace`:

```bash
helm install helpdesk ./helm/helpdesk \
    --namespace my-custom-ns \
    --create-namespace \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001
```

### 3.6 Gateway Access

By default, the gateway uses `ClusterIP`. For external access:

```bash
helm install helpdesk ./helm/helpdesk \
    --namespace helpdesk-system \
    --set gateway.service.type=LoadBalancer \
    ...
```

## 4. Interactive Session

For a human operator to start an interactive troubleshooting session run the following command:

```bash
kubectl -n helpdesk-system exec -it deploy/helpdesk-orchestrator -- helpdesk
```

## 5. Architecture Recap

```
                    ┌─────────────────┐
                    │    Gateway      │ ← External API (port 8080)
                    │  (ClusterIP)    │
                    └────────┬────────┘
                             │
        ┌────────────────────┼────────────────────┐
        │                    │                    │
        ▼                    ▼                    ▼
┌───────────────┐  ┌───────────────┐  ┌───────────────┐
│ Database Agent│  │   K8s Agent   │  │Incident Agent │
│  (port 1100)  │  │  (port 1102)  │  │  (port 1104)  │
└───────────────┘  └───────────────┘  └───────────────┘
        │                    │
        ▼                    ▼
   PostgreSQL DBs      K8s API Server


┌─────────────────────────────────────────────────────┐
│                   Orchestrator                       │
│  (Interactive pod for human troubleshooting)        │
│  - Loads infrastructure.json from ConfigMap         │
│  - Routes queries to appropriate agents             │
└─────────────────────────────────────────────────────┘
```

Only the **Orchestrator** needs the infrastructure inventory. Agents receive connection details as parameters when called.

Please see the complete aiHelpDesk architecture description [here](../../ARCHITECTURE.md).

## 6. Deployment from Source

To deploy on K8s by cloning the repo (instead of using pre-built bundles):

```bash
# 1. Clone the repo (assumes ADK is a sibling directory)
git clone https://github.com/borisdali/helpdesk.git
cd helpdesk

# 2. Build the Docker image locally
make image
# This creates: helpdesk:latest

# 3. Load image into your local K8s cluster
# For Minikube:
minikube image load helpdesk:latest

# For Kind:
kind load docker-image helpdesk:latest

# For Docker Desktop K8s: image is already available

# 4. Create namespace and API key secret
kubectl create namespace helpdesk-system
kubectl -n helpdesk-system create secret generic helpdesk-api-key \
    --from-literal=api-key=<YOUR_API_KEY>

# 5. Install with Helm using the local image
helm install helpdesk ./deploy/helm/helpdesk \
    --namespace helpdesk-system \
    --set image.repository=helpdesk \
    --set image.tag=latest \
    --set image.pullPolicy=Never \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001
```

**Key differences from pre-built bundle:**
- Use `./deploy/helm/helpdesk` (repo path) instead of `./helm/helpdesk` (bundle path)
- Set `image.pullPolicy=Never` to use the locally loaded image
- Set `image.repository=helpdesk` and `image.tag=latest` to match the local build

To include infrastructure config, create a `my-values.yaml` as shown above and add `-f my-values.yaml` to the helm install command.

## 7. Using the Gateway API

While the interactive orchestrator REPL is available via `kubectl exec`, the Gateway provides a REST API that is often more suitable for programmatic access and automation. See [API.md](../../API.md) for the full reference (all 17 endpoints with request/response shapes and query parameters).

```bash
# Port-forward the gateway
kubectl -n helpdesk-system port-forward svc/helpdesk-gateway 8080:8080

# In another terminal, query the system
curl -X POST http://localhost:8080/api/v1/query \
  -H "Content-Type: application/json" \
  -d '{"query": "What databases are you aware of?"}'

# List available agents
curl http://localhost:8080/api/v1/agents

# Call database agent tools directly
curl -X POST http://localhost:8080/api/v1/db/get_server_info \
  -H "Content-Type: application/json" \
  -d '{"connection_string": "host=mydb.example.com port=5432 dbname=mydb user=admin"}'

# Call K8s agent tools directly
curl -X POST http://localhost:8080/api/v1/k8s/get_pods \
  -H "Content-Type: application/json" \
  -d '{"namespace": "default"}'
```

The Gateway API is the recommended interface for:
- CI/CD pipelines and automation scripts
- Integration with monitoring/alerting systems (see [srebot example](../../cmd/srebot/README.md))
- Environments where interactive TTY access is limited

## 8. Helper Scripts

The deploy bundle includes helper scripts in the `scripts/` directory:

| Script | Description |
|--------|-------------|
| `gateway-repl.sh` | Interactive REPL using the Gateway API (recommended for containers) |
| `k8s-local-repl.sh` | Run orchestrator locally with K8s agents port-forwarded |

See [scripts/README.md](../../scripts/README.md) for detailed usage.

**Quick start with gateway-repl.sh:**
```bash
kubectl -n helpdesk-system port-forward svc/helpdesk-gateway 8080:8080 &
./scripts/gateway-repl.sh
```

## 9. AI Governance

aiHelpDesk includes an AI Governance framework with policy-based access control, human-in-the-loop approval workflows, and comprehensive audit logging.

### 9.1 Governance Components

| Component | Description | Default |
|-----------|-------------|---------|
| **auditd** | Central audit daemon with SQLite persistence and approval workflow API | Enabled |
| **auditor** | Real-time audit stream monitor with alerting | Disabled |
| **secbot** | Security responder that creates incidents for anomalies | Disabled |
| **govbot** | Compliance reporter CronJob — posts summary to Slack on a schedule | Disabled |
| **approvals** | Operator CLI for listing, approving, and denying approval requests (exec into auditd pod) | - |
| **govexplain** | Operator CLI for explaining past and hypothetical policy decisions (exec into any pod) | - |
| **srebot** | SRE automation bot — detects DB anomalies, triggers AI diagnosis + incident bundle | - |

### 9.2 Enable Governance

Add to your `values.yaml`:

```yaml
governance:
  auditd:
    enabled: true
    port: 1199
    persistence:
      enabled: true
      size: 5Gi

  # Optional: Real-time monitoring
  auditor:
    enabled: true
    alertWebhook: "https://hooks.slack.com/services/..."

  # Optional: Security responder
  secbot:
    enabled: true
    cooldown: "5m"
    maxEventsPerMinute: 100
```

### 9.3 Policy Configuration

**Step 1: Create a `policies.yaml` file**

A fully-commented example is included in the deploy bundle as `policies.example.yaml`. Copy and edit it:

```bash
cp policies.example.yaml my-policies.yaml
# Edit my-policies.yaml to match your environment
```

Minimal example (`my-policies.yaml`):

```yaml
version: "1"
policies:
  - name: allow-reads
    resources:
      - type: database
      - type: kubernetes
    rules:
      - action: read
        effect: allow

  - name: require-approval-for-writes
    resources:
      - type: database
        match:
          tags: [production]
    rules:
      - action: write
        effect: require_approval
      - action: destructive
        effect: deny
        message: "Destructive operations on production are prohibited."
```

See `policies.example.yaml` in the bundle for the full schema including time-based schedules, principal-based rules, row limits, and multi-approver quorum.

**Step 2: Create a Kubernetes ConfigMap**

```bash
kubectl -n helpdesk-system create configmap helpdesk-policies \
  --from-file=policies.yaml=./my-policies.yaml
```

**Step 3: Reference it in `values.yaml`**

```yaml
governance:
  policy:
    enabled: true
    configMap: helpdesk-policies
```

To update the policy without reinstalling:

```bash
kubectl -n helpdesk-system create configmap helpdesk-policies \
  --from-file=policies.yaml=./my-policies.yaml \
  --dry-run=client -o yaml | kubectl apply -f -
# Restart agents to pick up the new policy
kubectl -n helpdesk-system rollout restart deploy/helpdesk-database-agent deploy/helpdesk-k8s-agent
```

### 9.4 Approval Workflow

Configure approval notifications:

```yaml
governance:
  approvals:
    timeout: "5m"
    webhook: "https://hooks.slack.com/services/..."
    baseURL: "http://helpdesk-auditd:1199"

  email:
    enabled: true
    smtpHost: "smtp.example.com"
    smtpPort: "587"
    smtpSecret: "smtp-credentials"  # Secret with keys: username, password
    from: "helpdesk@example.com"
    to: "ops@example.com"
```

Manage approvals via the CLI (exec into the auditd pod):

```bash
# List pending approvals
kubectl -n helpdesk-system exec -it deploy/helpdesk-auditd -- approvals list --status pending

# Approve a request
kubectl -n helpdesk-system exec -it deploy/helpdesk-auditd -- approvals approve apr_xxx --reason "LGTM, verified safe"

# Watch for new approvals interactively
kubectl -n helpdesk-system exec -it deploy/helpdesk-auditd -- approvals watch
```

Or use the HTTP API directly:

```bash
# Port-forward auditd
kubectl -n helpdesk-system port-forward svc/helpdesk-auditd 1199:1199

# List pending approvals
curl http://localhost:1199/v1/approvals/pending

# Approve a request
curl -X POST http://localhost:1199/v1/approvals/apr_xxx/approve \
  -H "Content-Type: application/json" \
  -d '{"approved_by": "admin", "reason": "LGTM, verified safe"}'
```

### 9.5 Explaining Policy Decisions (govexplain)

`govexplain` is baked into every pod image. Exec into any running pod to use it — the `gateway` pod is the most convenient since its `HELPDESK_AUDIT_URL` is already set:

```bash
# List recent policy decisions (last hour)
kubectl -n helpdesk-system exec -it deploy/helpdesk-gateway -- \
  govexplain --list --since 1h

# Show only denials
kubectl -n helpdesk-system exec -it deploy/helpdesk-gateway -- \
  govexplain --list --since 24h --effect deny

# Explain a specific past decision by event ID
kubectl -n helpdesk-system exec -it deploy/helpdesk-gateway -- \
  govexplain --event tool_a1b2c3d4

# Hypothetical check: what would happen if an agent tried this?
kubectl -n helpdesk-system exec -it deploy/helpdesk-gateway -- \
  govexplain --resource database:prod-db --action write --tags production

# Talk directly to auditd (bypass gateway, exec into auditd pod)
kubectl -n helpdesk-system exec -it deploy/helpdesk-auditd -- \
  govexplain --auditd http://localhost:1199 --list --since 1h
```

### 9.6 Running the SRE Bot (srebot)

`srebot` is a one-shot tool — use `kubectl run` with `--rm` so the pod is cleaned up on exit:

```bash
kubectl -n helpdesk-system run srebot --rm -it --restart=Never \
  --image=$(kubectl -n helpdesk-system get deploy/helpdesk-gateway \
    -o jsonpath='{.spec.template.spec.containers[0].image}') \
  -- srebot \
    -gateway http://helpdesk-gateway:8080 \
    -conn 'host=db.example.com port=5432 dbname=myapp user=admin'
```

Or pin the image tag directly:

```bash
kubectl -n helpdesk-system run srebot --rm -it --restart=Never \
  --image=ghcr.io/borisdali/helpdesk:v1.0.0 \
  -- srebot \
    -gateway http://helpdesk-gateway:8080 \
    -conn 'host=db.example.com port=5432 dbname=myapp user=admin'
```

### 9.7 Running the Compliance Reporter (govbot)

`govbot` is deployed as a Kubernetes CronJob (runs automatically on the configured schedule). To trigger it immediately:

```bash
# Run now, without waiting for the next scheduled time
kubectl -n helpdesk-system create job govbot-now \
  --from=cronjob/helpdesk-govbot

# Follow the output
kubectl -n helpdesk-system logs -f job/govbot-now
```

### 9.8 Security Responder (secbot)

Unlike the CLI tools above, `secbot` is a **long-running daemon** — enable it once and it watches the audit stream continuously. It automatically creates incident bundles when it detects:

- `unauthorized_destructive` — a destructive tool call without a valid approval
- `hash_mismatch` — audit chain integrity failure (tampered event)
- `high_volume` — event rate exceeds threshold (potential abuse or runaway agent)
- `potential_sql_injection` / `potential_command_injection` — error patterns in tool output

Enable it in `values.yaml`:

```yaml
governance:
  secbot:
    enabled: true
    cooldown: "5m"           # minimum time between incident creations
    maxEventsPerMinute: 100  # high-volume alert threshold
```

Check that it is running and connected:

```bash
# Check pod status
kubectl -n helpdesk-system get pods -l app.kubernetes.io/component=secbot

# Follow logs (you'll see startup phases and any alerts)
kubectl -n helpdesk-system logs -f deploy/helpdesk-secbot

# Dry-run mode: log alerts but don't create incidents
# Add to your values.yaml under secbot: dryRun: true
# (requires adding the flag to the secbot Helm template args)
```

> **Socket scheduling note:** `secbot` reads from the same Unix socket as `auditd` via a shared PersistentVolumeClaim. If your storage class only supports `ReadWriteOnce`, both pods must land on the same node. Either use a `ReadWriteMany` storage class, or add a `nodeAffinity` / `podAffinity` rule to co-locate them. The default Helm chart uses `emptyDir` when persistence is disabled, which automatically co-locates via the volume.

## 10. Troubleshooting

### 10.1 Interactive REPL Shows Empty Responses

**Symptom:** When running the interactive orchestrator in a container, agent responses appear empty and require pressing Enter to display.

**Cause:** This is a known issue with the ADK (Agent Development Kit) REPL in containerized environments where TTY handling differs from local execution.

**Workarounds:**

1. **Gateway REPL** (recommended) - Interactive wrapper around the Gateway API:
   ```bash
   kubectl -n helpdesk-system port-forward svc/helpdesk-gateway 8080:8080 &
   ./scripts/gateway-repl.sh
   ```

2. **Local orchestrator** - Run orchestrator locally with port-forwarded agents:
   ```bash
   ./scripts/k8s-local-repl.sh [namespace]
   ```

3. **Direct API calls** - Use curl with the Gateway REST API (see Section 7).

### 10.2 Agents Not Discovered

**Symptom:** Orchestrator logs show "agent not available" or discovery failures.

**Solution:** Check that all agent pods are running and services are correctly configured:
```bash
kubectl -n helpdesk-system get pods
kubectl -n helpdesk-system get svc
kubectl -n helpdesk-system logs deploy/helpdesk-orchestrator
```

### 10.3 API Key Issues

**Symptom:** Agents fail with authentication errors to the LLM provider.

**Solution:** Verify the secret exists and contains the correct key:
```bash
kubectl -n helpdesk-system get secret helpdesk-api-key -o yaml
```

### 10.4 K8s Context Not Found

**Symptom:** K8s agent reports "context does not exist" when querying pods.

**Cause:** The K8s agent runs inside a pod and doesn't have access to your laptop's kubeconfig.

**Solution:**
- For databases in the **same cluster** as aiHelpDesk: use empty context (`""`) in `infrastructure.json`. The agent will use in-cluster authentication via its service account.
- For databases in **other clusters**: mount a kubeconfig as a secret (see Section 3.2).

### 10.5 Database Agent Prompts for Password

**Symptom:** The database agent asks for a password every time it connects to a database, or logs `fe_sendauth: no password supplied`.

**Solution:** Create a `.pgpass` secret and reference it via `agents.database.pgpassSecret` (see Section 3.4).

**Important:** The hostname in `.pgpass` must **exactly match** the hostname in your `infrastructure.json` connection strings. If the connection string says `host=db.example.com`, the `.pgpass` entry must also use `db.example.com` (not a K8s service name alias or IP).

Verify the secret was created correctly:
```bash
kubectl -n helpdesk-system get secret pgpass -o jsonpath='{.data.\.pgpass}' | base64 -d
```

Check the database agent logs for the actual error:
```bash
kubectl -n helpdesk-system logs deploy/helpdesk-database-agent | grep -i "psql\|pgpass\|password"
```

## 11. Uninstall

```bash
helm uninstall helpdesk --namespace helpdesk-system
kubectl delete namespace helpdesk-system
```
