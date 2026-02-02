# aiHelpDesk: Sample log of deploying aiHelpDesk in non-K8s env from source (by cloning the repo)

See [VM-based Deployment README](README.md) for background.

  ## 2.3 Interactive/Human session: Deployment from source (by cloning the repo)

```
[boris@ ~/helpdesk]$ docker compose -f deploy/docker-compose/docker-compose.yaml --profile interactive run orchestrator
[+] Creating 3/3
 ✔ Container docker-compose-database-agent-1  Running                                                                                                                                                                                        0.0s
 ✔ Container docker-compose-incident-agent-1  Running                                                                                                                                                                                        0.0s
 ✔ Container docker-compose-k8s-agent-1       Running                                                                                                                                                                                        0.0s
time=2026-02-01T02:00:04.707Z level=INFO msg="discovering agent" url=http://database-agent:1100
time=2026-02-01T02:00:04.711Z level=INFO msg="discovered agent" name=postgres_database_agent url=http://database-agent:1100
time=2026-02-01T02:00:04.711Z level=INFO msg="discovering agent" url=http://k8s-agent:1102
time=2026-02-01T02:00:04.714Z level=INFO msg="discovered agent" name=k8s_agent url=http://k8s-agent:1102
time=2026-02-01T02:00:04.714Z level=INFO msg="discovering agent" url=http://incident-agent:1104
time=2026-02-01T02:00:04.715Z level=INFO msg="discovered agent" name=incident_agent url=http://incident-agent:1104
time=2026-02-01T02:00:04.715Z level=INFO msg="expected expert agents" agents="postgres_database_agent, k8s_agent, incident_agent"
time=2026-02-01T02:00:04.716Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-02-01T02:00:04.716Z level=INFO msg="confirming agent availability" agent=postgres_database_agent url=http://database-agent:1100
time=2026-02-01T02:00:04.716Z level=INFO msg="agent available" agent=postgres_database_agent
time=2026-02-01T02:00:04.716Z level=INFO msg="confirming agent availability" agent=k8s_agent url=http://k8s-agent:1102
time=2026-02-01T02:00:04.716Z level=INFO msg="agent available" agent=k8s_agent
time=2026-02-01T02:00:04.716Z level=INFO msg="confirming agent availability" agent=incident_agent url=http://incident-agent:1104
time=2026-02-01T02:00:04.717Z level=INFO msg="agent available" agent=incident_agent
time=2026-02-01T02:00:04.718Z level=INFO msg="infrastructure config loaded" db_servers=3 k8s_clusters=1 vms=1
time=2026-02-01T02:00:04.718Z level=INFO msg="orchestrator initialized" available_agents=3
```

And here's the basic, bare bones session of a human operator talking to aiHelpDesk:

```
User -> who are you?

Agent -> I'm an expert helpdesk system for database and infrastructure operations. Here's what I do:

**My Role:**
- Help you diagnose issues with databases and Kubernetes infrastructure
- Inspect system health, stats, configurations, and cluster status
- Create diagnostic bundles for vendor support when incidents occur

**What I Can Help With:**
- **Troubleshooting**: Connection timeouts, slow queries, pod crashes, replication issues, etc.
- **Inspection**: Database stats, cluster status, configuration parameters, resource usage
- **Incident Management**: Creating diagnostic bundles and reviewing past incidents

**How I Work:**
I route your requests to specialized agents based on your needs:
- **PostgreSQL Database Agent** — for connection issues, queries, performance, replication
- **Kubernetes Agent** — for pod, service, endpoint, and cluster diagnostics
- **Incident Agent** — for creating diagnostic bundles for vendor support

**Infrastructure I Manage:**
I have visibility into:
- **Database Servers**: `local-co-db`, `global-corp-db-01`, `global-corp-db-02`
- **Kubernetes Cluster**: `global-prod` (production environment)
- **Virtual Machines**: `vm-db-dev-01`

**What Can You Ask Me?**
- "My database connection is timing out — can you investigate?"
- "Check the status of pods in our K8s cluster"
- "Create an incident bundle for vendor support"
- "What are the active connections on our production database?"

What infrastructure issue or diagnostic task can I help you with today?
```

As long as the deploy/docker-compose/infrastructure.json has been populated
with the location of the databases of interest, this question should be
answered properly:

```
User -> what databases are you aware of?

Agent -> I'm aware of the following managed databases:

### Database Servers

1. **local-co-db** (Local Company Dev DB)
   - **Type**: PostgreSQL on VM
   - **Host**: `db2.local.example.io`
   - **Connection**: `host=db2.local.example.io port=5432 dbname=dev user=dba`
   - **Infrastructure**: Runs on VM `vm-db-dev-01` (no Kubernetes)

2. **global-corp-db-01** (Global Corporation Main Production DB)
   - **Type**: PostgreSQL on Kubernetes
   - **Host**: `db01.global.example.com`
   - **Connection**: `host=db01.global.example.com port=5432 dbname=prod01 user=admin`
   - **Kubernetes**: Cluster `global-prod` (context: `global-prod-cluster`), namespace: `db1`

3. **global-corp-db-02** (Global Corporation Secondary Production DB)
   - **Type**: PostgreSQL on Kubernetes
   - **Host**: `db02.global.example.com`
   - **Connection**: `host=db02.global.example.com port=5432 dbname=prod02 user=admin`
   - **Kubernetes**: Cluster `global-prod` (context: `global-prod-cluster`), namespace: `db2`

All three are PostgreSQL databases (or PostgreSQL derivatives). I can investigate any of them using their connection strings and associated infrastructure contexts.

Would you like me to check the status or diagnose an issue with any of these databases?
```

It's easy to confirm that the list of your databases were indeed made available to aiHelpDesk (to the Orchestrator in particular) with the following command:

```
[boris@ ~/helpdesk/deploy/docker-compose]$ docker ps|grep docker-compose-orchestrator
c958e495047e   helpdesk:latest                      "/usr/local/bin/help…"   2 hours ago    Up 2 hours                                       docker-compose-orchestrator-run-b4b14664980c

[boris@ ~/helpdesk]$ docker exec -ti docker-compose-orchestrator-run-b4b14664980c cat /etc/helpdesk/infrastructure.json
{
  "db_servers": {
    "global-corp-db-01": {
      "name": "Global Corporation Main Production DB hosted on K8s",
      "connection_string": "host=db01.global.example.com port=5432 dbname=prod01 user=admin",
      "k8s_cluster": "global-prod",
      "k8s_namespace": "db1"
    },
...
```

