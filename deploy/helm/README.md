# aiHelpDesk: Deployment on Kubernetes

## Prerequisites

- Kubernetes cluster (1.24+)
- Helm 3.x
- `kubectl` configured to access your cluster

## Quick Start

```bash
# Extract the deploy bundle
tar xzf helpdesk-v1.0.0-deploy.tar.gz
cd helpdesk-v1.0.0-deploy

# Create the API key secret
kubectl create namespace helpdesk-system
kubectl -n helpdesk-system create secret generic helpdesk-api-key \
    --from-literal=api-key=<YOUR_API_KEY>

# Install the Helm chart
helm install helpdesk ./helm/helpdesk \
    --namespace helpdesk-system \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001
```

## Configuration

### Model Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `model.vendor` | LLM provider: `anthropic` or `google` | `google` |
| `model.name` | Model name (e.g., `claude-haiku-4-5-20251001`) | `gemini-2.5-flash` |
| `model.apiKeySecret` | Name of K8s Secret containing API key | `helpdesk-api-key` |
| `model.apiKeyKey` | Key within the Secret | `api-key` |

### Infrastructure Inventory

The orchestrator needs to know which databases it manages. Configure this via `values.yaml` or `--set-json`:

**Option A: Using a values file**

Create `my-values.yaml`:
```yaml
model:
  vendor: anthropic
  name: claude-haiku-4-5-20251001

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
helm install helpdesk ./helm/helpdesk \
    --namespace helpdesk-system \
    -f my-values.yaml
```

**Option B: Using --set-json (for simple configs)**

```bash
helm install helpdesk ./helm/helpdesk \
    --namespace helpdesk-system \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001 \
    --set-json 'infrastructure={"db_servers":{"mydb":{"name":"My Database","connection_string":"host=db.example.com port=5432 dbname=app user=admin"}}}'
```

See `infrastructure.json.example` in the bundle for a complete reference.

### Custom Namespace

Install to any namespace using `--namespace` and `--create-namespace`:

```bash
helm install helpdesk ./helm/helpdesk \
    --namespace my-custom-ns \
    --create-namespace \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001
```

### Gateway Access

By default, the gateway uses `ClusterIP`. For external access:

```bash
helm install helpdesk ./helm/helpdesk \
    --namespace helpdesk-system \
    --set gateway.service.type=LoadBalancer \
    ...
```

## Interactive Session

To start an interactive troubleshooting session:

```bash
kubectl -n helpdesk-system exec -it deploy/helpdesk-orchestrator -- helpdesk
```

## Architecture

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

Only the **orchestrator** needs the infrastructure inventory. Agents receive connection details as parameters when called.

## Uninstall

```bash
helm uninstall helpdesk --namespace helpdesk-system
kubectl delete namespace helpdesk-system
```
