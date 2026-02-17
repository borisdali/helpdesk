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

### 3.4 Custom Namespace

Install to any namespace using `--namespace` and `--create-namespace`:

```bash
helm install helpdesk ./helm/helpdesk \
    --namespace my-custom-ns \
    --create-namespace \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001
```

### 3.5 Gateway Access

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

While the interactive orchestrator REPL is available via `kubectl exec`, the Gateway provides a REST API that is often more suitable for programmatic access and automation:

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
| **approvals** | CLI tool for managing approval requests (included in auditd pod) | - |

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

Create a ConfigMap with your policies:

```bash
kubectl -n helpdesk-system create configmap helpdesk-policies \
  --from-file=policies.yaml=./policies.yaml
```

Then reference it in values:

```yaml
governance:
  policy:
    enabled: true
    configMap: helpdesk-policies
```

Example `policies.yaml`:

```yaml
rules:
  - name: allow-read-operations
    match:
      action_class: read
    effect: allow

  - name: require-approval-for-writes
    match:
      action_class: write
    effect: require_approval

  - name: deny-destructive-on-production
    match:
      action_class: destructive
      tags: [production]
    effect: deny
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

## 11. Uninstall

```bash
helm uninstall helpdesk --namespace helpdesk-system
kubectl delete namespace helpdesk-system
```
