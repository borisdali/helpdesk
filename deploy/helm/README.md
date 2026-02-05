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
```

Please see below for details and in particular on how to setup and
pass infrastructure.json to the Orchestrator (automatically created
by aiHelpDesk via a ConfigMap from the Helm's `my-values.yaml`).

## 3. Configuration

### 3.1 Model Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `model.vendor` | LLM provider: `anthropic` or `google` | `anthropic` |
| `model.name` | Model name (e.g. `gemini-2.5-flash`) | `claude-haiku-4-5-20251001` |
| `model.apiKeySecret` | Name of K8s Secret containing API key | `helpdesk-api-key` |
| `model.apiKeyKey` | Key within the Secret | `api-key` |

### 3.2 Infrastructure Inventory

The Orchestrator needs to know which databases it manages. Configure this via `values.yaml` or `--set-json`.
While it's possible for the human operator's interactive session to ask adhoc questions about "unknown"
databases (subject to the rules defined by an administrator in the governance module, it's 
more convenient, secure and structured to define the set of databases ahead of time):

**3.2.1 Option A: Using a values file**

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
    -f ./helm/helpdesk/values.yaml
```

**3.2.2 Option B: Using --set-json (for simple configs)**

```bash
helm install helpdesk ./helm/helpdesk \
    --namespace helpdesk-system \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001 \
    --set-json 'infrastructure={"db_servers":{"mydb":{"name":"My Database","connection_string":"host=db.example.com port=5432 dbname=app user=admin"}}}'
```

See `infrastructure.json.example` in the bundle for a complete reference.

### 3.3 Custom Namespace

Install to any namespace using `--namespace` and `--create-namespace`:

```bash
helm install helpdesk ./helm/helpdesk \
    --namespace my-custom-ns \
    --create-namespace \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001
```

### 3.4 Gateway Access

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

## 7. Uninstall

```bash
helm uninstall helpdesk --namespace helpdesk-system
kubectl delete namespace helpdesk-system
```
