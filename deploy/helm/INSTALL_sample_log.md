# aiHelpDesk: Sample log of deploying aiHelpDesk in a K8s env 

See [K8s-based Deployment README](README.md) for background (and [VM-based Deployment README](../docker-compose/README.md) for the installation instructions of aiHelpDesk on VMs).

## 2. Deployment on Minikube

```
[boris@ /tmp/helpdesk]$ tar xvfz helpdesk-v0.1.2-deploy.tar.gz
x helpdesk-v0.1.2-deploy/
x helpdesk-v0.1.2-deploy/docker-compose/
x helpdesk-v0.1.2-deploy/helm/
x helpdesk-v0.1.2-deploy/helm/infrastructure.json.example
x helpdesk-v0.1.2-deploy/helm/helpdesk/
x helpdesk-v0.1.2-deploy/helm/helpdesk/Chart.yaml
x helpdesk-v0.1.2-deploy/helm/helpdesk/templates/
x helpdesk-v0.1.2-deploy/helm/helpdesk/values.yaml
...

[boris@ /tmp/helpdesk]$ cd helpdesk-v0.1.0-deploy

[boris@ /tmp/helpdesk/helpdesk-v0.1.0-deploy]$ helm install helpdesk ./helm/helpdesk --namespace helpdesk-system -f ./helm/helpdesk/values.yaml
NAME: helpdesk
LAST DEPLOYED: Thu Feb  5 13:38:51 2026
NAMESPACE: helpdesk-system
STATUS: deployed
REVISION: 1
DESCRIPTION: Install complete
TEST SUITE: None

[boris@cassiopeia /tmp/helpdesk/helpdesk-v0.1.2-deploy]$ kubectl get all -n helpdesk-system
NAME                                           READY   STATUS    RESTARTS        AGE
pod/helpdesk-database-agent-6849cdc54c-wqntr   1/1     Running   0               4m59s
pod/helpdesk-gateway-6b977fd447-rl54x          1/1     Running   2 (4m56s ago)   4m59s
pod/helpdesk-incident-agent-7bf9f79dcf-8nw9t   1/1     Running   0               4m59s
pod/helpdesk-k8s-agent-5bb7f99bcb-cvg9k        1/1     Running   0               4m59s
pod/helpdesk-orchestrator-5d46c5ddbf-bpm7s     1/1     Running   0               4m59s

NAME                              TYPE        CLUSTER-IP      EXTERNAL-IP   PORT(S)    AGE
service/helpdesk-database-agent   ClusterIP   10.96.14.235    <none>        1100/TCP   4m59s
service/helpdesk-gateway          ClusterIP   10.100.198.85   <none>        8080/TCP   4m59s
service/helpdesk-incident-agent   ClusterIP   10.96.106.160   <none>        1104/TCP   4m59s
service/helpdesk-k8s-agent        ClusterIP   10.96.44.71     <none>        1102/TCP   4m59s

NAME                                      READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/helpdesk-database-agent   1/1     1            1           4m59s
deployment.apps/helpdesk-gateway          1/1     1            1           4m59s
deployment.apps/helpdesk-incident-agent   1/1     1            1           4m59s
deployment.apps/helpdesk-k8s-agent        1/1     1            1           4m59s
deployment.apps/helpdesk-orchestrator     1/1     1            1           4m59s

NAME                                                 DESIRED   CURRENT   READY   AGE
replicaset.apps/helpdesk-database-agent-6849cdc54c   1         1         1       4m59s
replicaset.apps/helpdesk-gateway-6b977fd447          1         1         1       4m59s
replicaset.apps/helpdesk-incident-agent-7bf9f79dcf   1         1         1       4m59s
replicaset.apps/helpdesk-k8s-agent-5bb7f99bcb        1         1         1       4m59s
replicaset.apps/helpdesk-orchestrator-5d46c5ddbf     1         1         1       4m59s
```

All aiHelpDesk Pods are up, let's fire up the Orchestrator for the interfactive session:

```
[boris@ /tmp/helpdesk/helpdesk-v0.1.2-deploy]$ kubectl -n helpdesk-system exec -it deploy/helpdesk-orchestrator -- helpdesk
time=2026-02-05T18:44:43.400Z level=INFO msg="discovering agent" url=http://helpdesk-database-agent:1100
time=2026-02-05T18:44:43.417Z level=INFO msg="discovered agent" name=postgres_database_agent url=http://helpdesk-database-agent:1100
time=2026-02-05T18:44:43.417Z level=INFO msg="discovering agent" url=http://helpdesk-k8s-agent:1102
time=2026-02-05T18:44:43.419Z level=INFO msg="discovered agent" name=k8s_agent url=http://helpdesk-k8s-agent:1102
time=2026-02-05T18:44:43.419Z level=INFO msg="discovering agent" url=http://helpdesk-incident-agent:1104
time=2026-02-05T18:44:43.420Z level=INFO msg="discovered agent" name=incident_agent url=http://helpdesk-incident-agent:1104
time=2026-02-05T18:44:43.421Z level=INFO msg="expected expert agents" agents="postgres_database_agent, k8s_agent, incident_agent"
time=2026-02-05T18:44:43.422Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-02-05T18:44:43.422Z level=INFO msg="confirming agent availability" agent=postgres_database_agent url=http://helpdesk-database-agent:1100
time=2026-02-05T18:44:43.423Z level=INFO msg="agent available" agent=postgres_database_agent
time=2026-02-05T18:44:43.423Z level=INFO msg="confirming agent availability" agent=k8s_agent url=http://helpdesk-k8s-agent:1102
time=2026-02-05T18:44:43.423Z level=INFO msg="agent available" agent=k8s_agent
time=2026-02-05T18:44:43.423Z level=INFO msg="confirming agent availability" agent=incident_agent url=http://helpdesk-incident-agent:1104
time=2026-02-05T18:44:43.423Z level=INFO msg="agent available" agent=incident_agent
time=2026-02-05T18:44:43.424Z level=INFO msg="infrastructure config loaded" db_servers=5 k8s_clusters=2 vms=1
time=2026-02-05T18:44:43.424Z level=INFO msg="orchestrator initialized" available_agents=3

User -> what dbs are you aware of?

Agent -> I manage the following databases:

1. **dev-local-db** — Local Development Database
   - Connection: `host=localhost port=5432 dbname=devdb user=postgres`
   - Hosting: standalone (no K8s cluster or VM specified)

2. **legacy-analytics-db** — Legacy Analytics Database on VM
   - Connection: `host=analytics.internal.example.com port=5432 dbname=analytics user=etl_user`
   - Hosted on VM: Analytics Server (on-prem)

3. **prod-orders-db** — Orders Production Database
   - Connection: `host=orders-db.prod.example.com port=5432 dbname=orders user=app_user`
   - Hosted on Kubernetes: Production GKE Cluster (context: `gke_myproject_us-central1_prod`, namespace: `orders`)

4. **prod-users-db** — Users Production Database
   - Connection: `host=users-db.prod.example.com port=5432 dbname=users user=app_user`
   - Hosted on Kubernetes: Production GKE Cluster (context: `gke_myproject_us-central1_prod`, namespace: `users`)

5. **staging-db** — Staging Database (shared)
   - Connection: `host=staging-db port=5432 dbname=app user=app`
   - Hosted on Kubernetes: Staging Minikube (context: `minikube`, namespace: `db`)

Is there a specific database you'd like me to help you with?
```

TODO(bdali): continue...

