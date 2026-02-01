# aiHelpDesk: Deployment for VM-based (non-K8s) databases

## 1. Deployment from source (by cloning the repo):

```
[boris@ ~/helpdesk]$ docker compose -f deploy/docker-compose/docker-compose.yaml up -d
[+] Running 4/4
 ! incident-agent Warning pull access denied for helpdesk, repository does not exist or may require 'docker login'                                                                                                                           0.2s
 ! database-agent Warning pull access denied for helpdesk, repository does not exist or may require 'docker login'                                                                                                                           0.2s
 ! gateway Warning        pull access denied for helpdesk, repository does not exist or may require 'docker login'                                                                                                                           0.2s
 ! k8s-agent Warning      pull access denied for helpdesk, repository does not exist or may require 'docker login'                                                                                                                           0.2s
Compose can now delegate builds to bake for better performance.
 To do so, set COMPOSE_BAKE=true.
[+] Building 2.1s (82/100)                                                                                                                                                                                                   docker:desktop-linux
 => [database-agent internal] load build definition from Dockerfile                                                                                                                                                                          0.0s
 => => transferring dockerfile: 2.71kB                                                                                                                                                                                                       0.0s
 => [gateway internal] load metadata for docker.io/library/debian:bookworm-slim                                                                                                                                                              0.6s
 => [gateway internal] load metadata for docker.io/library/golang:1.25                                                                                                                                                                       0.6s
 => [database-agent internal] load .dockerignore                                                                                                                                                                                             0.0s
 => => transferring context: 354B                                                                                                                                                                                                            0.0s
 => [gateway builder  1/13] FROM docker.io/library/golang:1.25@sha256:ce63a16e0f7063787ebb4eb28e72d477b00b4726f79874b3205a965ffd797ab2                                                                                                       0.0s
 => => resolve docker.io/library/golang:1.25@sha256:ce63a16e0f7063787ebb4eb28e72d477b00b4726f79874b3205a965ffd797ab2                                                                                                                         0.0s
 => [database-agent internal] load build context                                                                                                                                                                                             0.0s
 => => transferring context: 45.57kB                                                                                                                                                                                                         0.0s
 => [gateway stage-1  1/11] FROM docker.io/library/debian:bookworm-slim@sha256:56ff6d36d4eb3db13a741b342ec466f121480b5edded42e4b7ee850ce7a418ee                                                                                              0.1s
 => => resolve docker.io/library/debian:bookworm-slim@sha256:56ff6d36d4eb3db13a741b342ec466f121480b5edded42e4b7ee850ce7a418ee                                                                                                                0.0s
 => CACHED [gateway stage-1  2/11] RUN apt-get update && apt-get install -y --no-install-recommends     ca-certificates     curl     gnupg     lsb-release     && rm -rf /var/lib/apt/lists/*                                                0.0s
 => CACHED [gateway stage-1  3/11] RUN echo "deb http://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main"     > /etc/apt/sources.list.d/pgdg.list     && curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc     | g  0.0s
 => CACHED [gateway stage-1  4/11] RUN ARCH=$(dpkg --print-architecture)     && curl -fsSL "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/${ARCH}/kubectl"     -o /usr/local/bin/kubectl     && ch  0.0s
 => CACHED [gateway builder  2/13] WORKDIR /src                                                                                                                                                                                              0.0s
 => CACHED [database-agent builder  3/13] COPY ADK/github/adk-go /src/adk-go                                                                                                                                                                 0.0s
 => CACHED [database-agent builder  4/13] COPY helpdesk /src/helpdesk                                                                                                                                                                        0.0s
 => CACHED [database-agent builder  5/13] WORKDIR /src/helpdesk                                                                                                                                                                              0.0s
 => CACHED [database-agent builder  6/13] RUN go mod edit -replace google.golang.org/adk=/src/adk-go                                                                                                                                         0.0s
 => CACHED [database-agent builder  7/13] RUN go mod download                                                                                                                                                                                0.0s
 => CACHED [database-agent builder  8/13] RUN CGO_ENABLED=0 go build -o /out/database-agent  ./agents/database/                                                                                                                              0.0s
 => CACHED [database-agent builder  9/13] RUN CGO_ENABLED=0 go build -o /out/k8s-agent       ./agents/k8s/                                                                                                                                   0.0s
 => CACHED [database-agent builder 10/13] RUN CGO_ENABLED=0 go build -o /out/incident-agent   ./agents/incident/                                                                                                                             0.0s
 => CACHED [database-agent builder 11/13] RUN CGO_ENABLED=0 go build -o /out/gateway          ./cmd/gateway/                                                                                                                                 0.0s
 => CACHED [database-agent builder 12/13] RUN CGO_ENABLED=0 go build -o /out/helpdesk         ./cmd/helpdesk/                                                                                                                                0.0s
 => CACHED [database-agent builder 13/13] RUN CGO_ENABLED=0 go build -o /out/srebot           ./cmd/srebot/                                                                                                                                  0.0s
 => CACHED [database-agent stage-1  5/11] COPY --from=builder /out/database-agent  /usr/local/bin/database-agent                                                                                                                             0.0s
 => CACHED [database-agent stage-1  6/11] COPY --from=builder /out/k8s-agent       /usr/local/bin/k8s-agent                                                                                                                                  0.0s
 => CACHED [database-agent stage-1  7/11] COPY --from=builder /out/incident-agent  /usr/local/bin/incident-agent                                                                                                                             0.0s
 => CACHED [database-agent stage-1  8/11] COPY --from=builder /out/gateway         /usr/local/bin/gateway                                                                                                                                    0.0s
 => CACHED [database-agent stage-1  9/11] COPY --from=builder /out/helpdesk        /usr/local/bin/helpdesk                                                                                                                                   0.0s
 => CACHED [database-agent stage-1 10/11] COPY --from=builder /out/srebot          /usr/local/bin/srebot                                                                                                                                     0.0s
 => CACHED [database-agent stage-1 11/11] RUN mkdir -p /data/incidents                                                                                                                                                                       0.0s
 => [database-agent] exporting to image                                                                                                                                                                                                      1.0s
 => => exporting layers                                                                                                                                                                                                                      0.0s
 => => exporting manifest sha256:ed71e1d4964e3696fee88aab983b1901a25fde4b34a22ea0faf8116f4cf0137c                                                                                                                                            0.0s
 => => exporting config sha256:f7d02f2728aa2c5afdb47039e4d58a71a1054a4e3cc832e36e55971e2a9c7005                                                                                                                                              0.0s
 => => exporting attestation manifest sha256:07e334950e245594e234d6db166e7146169d620c8c264edded6cd0c3ce3bdd58                                                                                                                                0.0s
 => => exporting manifest list sha256:e9dd10b7dcac5453ca5beb6320eae3c8ea1d253492c0a3bc9bfa350ea16f3e22                                                                                                                                       0.0s
 => => naming to docker.io/library/helpdesk:latest                                                                                                                                                                                           0.0s
 => => unpacking to docker.io/library/helpdesk:latest                                                                                                                                                                                        1.0s
 => [database-agent] resolving provenance for metadata file                                                                                                                                                                                  0.0s
 => [incident-agent internal] load build definition from Dockerfile                                                                                                                                                                          0.0s
 => => transferring dockerfile: 2.71kB                                                                                                                                                                                                       0.0s
 => [k8s-agent internal] load build definition from Dockerfile                                                                                                                                                                               0.0s
 => => transferring dockerfile: 2.71kB                                                                                                                                                                                                       0.0s
 => [k8s-agent internal] load .dockerignore                                                                                                                                                                                                  0.0s
 => => transferring context: 354B                                                                                                                                                                                                            0.0s
 => [incident-agent internal] load .dockerignore                                                                                                                                                                                             0.0s
 => => transferring context: 354B                                                                                                                                                                                                            0.0s
 => [k8s-agent internal] load build context                                                                                                                                                                                                  0.0s
 => => transferring context: 45.57kB                                                                                                                                                                                                         0.0s
 => [incident-agent internal] load build context                                                                                                                                                                                             0.0s
 => => transferring context: 45.57kB                                                                                                                                                                                                         0.0s
 => CACHED [incident-agent builder  3/13] COPY ADK/github/adk-go /src/adk-go                                                                                                                                                                 0.0s
 => CACHED [incident-agent builder  4/13] COPY helpdesk /src/helpdesk                                                                                                                                                                        0.0s
 => CACHED [incident-agent builder  5/13] WORKDIR /src/helpdesk                                                                                                                                                                              0.0s
 => CACHED [incident-agent builder  6/13] RUN go mod edit -replace google.golang.org/adk=/src/adk-go                                                                                                                                         0.0s
 => CACHED [incident-agent builder  7/13] RUN go mod download                                                                                                                                                                                0.0s
 => CACHED [incident-agent builder  8/13] RUN CGO_ENABLED=0 go build -o /out/database-agent  ./agents/database/                                                                                                                              0.0s
 => CACHED [incident-agent builder  9/13] RUN CGO_ENABLED=0 go build -o /out/k8s-agent       ./agents/k8s/                                                                                                                                   0.0s
 => CACHED [incident-agent builder 10/13] RUN CGO_ENABLED=0 go build -o /out/incident-agent   ./agents/incident/                                                                                                                             0.0s
 => CACHED [incident-agent builder 11/13] RUN CGO_ENABLED=0 go build -o /out/gateway          ./cmd/gateway/                                                                                                                                 0.0s
 => CACHED [incident-agent builder 12/13] RUN CGO_ENABLED=0 go build -o /out/helpdesk         ./cmd/helpdesk/                                                                                                                                0.0s
 => CACHED [incident-agent builder 13/13] RUN CGO_ENABLED=0 go build -o /out/srebot           ./cmd/srebot/                                                                                                                                  0.0s
 => CACHED [incident-agent stage-1  5/11] COPY --from=builder /out/database-agent  /usr/local/bin/database-agent                                                                                                                             0.0s
 => CACHED [incident-agent stage-1  6/11] COPY --from=builder /out/k8s-agent       /usr/local/bin/k8s-agent                                                                                                                                  0.0s
 => CACHED [incident-agent stage-1  7/11] COPY --from=builder /out/incident-agent  /usr/local/bin/incident-agent                                                                                                                             0.0s
 => CACHED [incident-agent stage-1  8/11] COPY --from=builder /out/gateway         /usr/local/bin/gateway                                                                                                                                    0.0s
 => CACHED [incident-agent stage-1  9/11] COPY --from=builder /out/helpdesk        /usr/local/bin/helpdesk                                                                                                                                   0.0s
 => CACHED [incident-agent stage-1 10/11] COPY --from=builder /out/srebot          /usr/local/bin/srebot                                                                                                                                     0.0s
 => CACHED [k8s-agent stage-1 11/11] RUN mkdir -p /data/incidents                                                                                                                                                                            0.0s
 => [k8s-agent] exporting to image                                                                                                                                                                                                           0.0s
 => => exporting layers                                                                                                                                                                                                                      0.0s
 => => exporting manifest sha256:c9a443facf0b6e87b0b7562bfcdd7b46bf193e57e1a9d7f71b539fc39c2223a5                                                                                                                                            0.0s
 => => exporting config sha256:f4b52b7ce1f2df0cfb2136f6b01565516b233f9a02f3187dcff43d870798dcc6                                                                                                                                              0.0s
 => => exporting attestation manifest sha256:36586f5caf2523b3552e8fbae154f8c647209696673bbd1c321993046f294738                                                                                                                                0.0s
 => => exporting manifest list sha256:2548e6ea3f5c19b16baf53a06aff715fcd6490a414f76ebe0647185350a8a22a                                                                                                                                       0.0s
 => => naming to docker.io/library/helpdesk:latest                                                                                                                                                                                           0.0s
 => => unpacking to docker.io/library/helpdesk:latest                                                                                                                                                                                        0.0s
 => [incident-agent] exporting to image                                                                                                                                                                                                      0.0s
 => => exporting layers                                                                                                                                                                                                                      0.0s
 => => exporting manifest sha256:d25e9fddce054a0cb6235001d85c58e252253679635e50c3e6e8243efbc6012d                                                                                                                                            0.0s
 => => exporting config sha256:7f461bfc0ea780abf930929424ea4ac39e66af0a6468916a13066000b4d0a2c2                                                                                                                                              0.0s
 => => exporting attestation manifest sha256:74c83b55786608bb2ebee9e9544d0a2426ab78ba8a5de86425489b484d0a480d                                                                                                                                0.0s
 => => exporting manifest list sha256:7b45999a836e4e5769b3d48e342e77b145eb518f5f9f2e86983482991189267b                                                                                                                                       0.0s
 => => naming to docker.io/library/helpdesk:latest                                                                                                                                                                                           0.0s
 => => unpacking to docker.io/library/helpdesk:latest                                                                                                                                                                                        0.0s
 => [incident-agent] resolving provenance for metadata file                                                                                                                                                                                  0.0s
 => [k8s-agent] resolving provenance for metadata file                                                                                                                                                                                       0.0s
 => [gateway internal] load build definition from Dockerfile                                                                                                                                                                                 0.0s
 => => transferring dockerfile: 2.71kB                                                                                                                                                                                                       0.0s
 => [gateway internal] load .dockerignore                                                                                                                                                                                                    0.0s
 => => transferring context: 354B                                                                                                                                                                                                            0.0s
 => [gateway internal] load build context                                                                                                                                                                                                    0.0s
 => => transferring context: 45.57kB                                                                                                                                                                                                         0.0s
 => CACHED [gateway builder  3/13] COPY ADK/github/adk-go /src/adk-go                                                                                                                                                                        0.0s
 => CACHED [gateway builder  4/13] COPY helpdesk /src/helpdesk                                                                                                                                                                               0.0s
 => CACHED [gateway builder  5/13] WORKDIR /src/helpdesk                                                                                                                                                                                     0.0s
 => CACHED [gateway builder  6/13] RUN go mod edit -replace google.golang.org/adk=/src/adk-go                                                                                                                                                0.0s
 => CACHED [gateway builder  7/13] RUN go mod download                                                                                                                                                                                       0.0s
 => CACHED [gateway builder  8/13] RUN CGO_ENABLED=0 go build -o /out/database-agent  ./agents/database/                                                                                                                                     0.0s
 => CACHED [gateway builder  9/13] RUN CGO_ENABLED=0 go build -o /out/k8s-agent       ./agents/k8s/                                                                                                                                          0.0s
 => CACHED [gateway builder 10/13] RUN CGO_ENABLED=0 go build -o /out/incident-agent   ./agents/incident/                                                                                                                                    0.0s
 => CACHED [gateway builder 11/13] RUN CGO_ENABLED=0 go build -o /out/gateway          ./cmd/gateway/                                                                                                                                        0.0s
 => CACHED [gateway builder 12/13] RUN CGO_ENABLED=0 go build -o /out/helpdesk         ./cmd/helpdesk/                                                                                                                                       0.0s
 => CACHED [gateway builder 13/13] RUN CGO_ENABLED=0 go build -o /out/srebot           ./cmd/srebot/                                                                                                                                         0.0s
 => CACHED [gateway stage-1  5/11] COPY --from=builder /out/database-agent  /usr/local/bin/database-agent                                                                                                                                    0.0s
 => CACHED [gateway stage-1  6/11] COPY --from=builder /out/k8s-agent       /usr/local/bin/k8s-agent                                                                                                                                         0.0s
 => CACHED [gateway stage-1  7/11] COPY --from=builder /out/incident-agent  /usr/local/bin/incident-agent                                                                                                                                    0.0s
 => CACHED [gateway stage-1  8/11] COPY --from=builder /out/gateway         /usr/local/bin/gateway                                                                                                                                           0.0s
 => CACHED [gateway stage-1  9/11] COPY --from=builder /out/helpdesk        /usr/local/bin/helpdesk                                                                                                                                          0.0s
 => CACHED [gateway stage-1 10/11] COPY --from=builder /out/srebot          /usr/local/bin/srebot                                                                                                                                            0.0s
 => CACHED [gateway stage-1 11/11] RUN mkdir -p /data/incidents                                                                                                                                                                              0.0s
 => [gateway] exporting to image                                                                                                                                                                                                             0.0s
 => => exporting layers                                                                                                                                                                                                                      0.0s
 => => exporting manifest sha256:6c4ae4b2ad61c05d66371741fd8c11c24451d5c7ddd09261b50c5bc301212063                                                                                                                                            0.0s
 => => exporting config sha256:24b19f982481bd9516fdecbc24ee83d8821e7bdf17ea3070f95611e411d4a845                                                                                                                                              0.0s
 => => exporting attestation manifest sha256:05ff074883bee709e0c8fe1b5aa8b40ad86082f4a4522f1577b2124c8f3e51cf                                                                                                                                0.0s
 => => exporting manifest list sha256:7c835ad8018e7e070dc1684f62c27e668a8c670e29961ef531216afae8b99e9f                                                                                                                                       0.0s
 => => naming to docker.io/library/helpdesk:latest                                                                                                                                                                                           0.0s
 => => unpacking to docker.io/library/helpdesk:latest                                                                                                                                                                                        0.0s
 => [gateway] resolving provenance for metadata file                                                                                                                                                                                         0.0s
[+] Running 10/10
 ✔ database-agent                             Built                                                                                                                                                                                          0.0s
 ✔ gateway                                    Built                                                                                                                                                                                          0.0s
 ✔ incident-agent                             Built                                                                                                                                                                                          0.0s
 ✔ k8s-agent                                  Built                                                                                                                                                                                          0.0s
 ✔ Network docker-compose_default             Created                                                                                                                                                                                        0.0s
 ✔ Volume "docker-compose_incidents"          Created                                                                                                                                                                                        0.0s
 ✔ Container docker-compose-database-agent-1  Started                                                                                                                                                                                        0.5s
 ✔ Container docker-compose-incident-agent-1  Started                                                                                                                                                                                        0.4s
 ✔ Container docker-compose-k8s-agent-1       Started                                                                                                                                                                                        0.4s
 ✔ Container docker-compose-gateway-1         Started                                                                                                                                                                                        0.4s
```

This deployment should result in downloading the helpdesk image and the four agents running in the Docker containers:

```
[boris@ ~/helpdesk]$ docker images
REPOSITORY                      TAG       IMAGE ID       CREATED         SIZE
helpdesk                        latest    7c835ad8018e   3 hours ago     582MB

[boris@ ~/helpdesk]$ docker ps
CONTAINER ID   IMAGE                                COMMAND                  CREATED         STATUS                  PORTS                     NAMES
225ec339e8ca   helpdesk:latest                      "/usr/local/bin/gate…"   7 seconds ago   Up 7 seconds            0.0.0.0:8080->8080/tcp    docker-compose-gateway-1
3eed36277655   helpdesk:latest                      "/usr/local/bin/k8s-…"   7 seconds ago   Up 7 seconds            0.0.0.0:1102->1102/tcp    docker-compose-k8s-agent-1
fd3b782c0d49   helpdesk:latest                      "/usr/local/bin/inci…"   7 seconds ago   Up 7 seconds            0.0.0.0:1104->1104/tcp    docker-compose-incident-agent-1
9dda96b44913   helpdesk:latest                      "/usr/local/bin/data…"   8 seconds ago   Up 7 seconds            0.0.0.0:1100->1100/tcp    docker-compose-database-agent-1
```

## 2. Interactive/Human session: Deployment from source (by cloning the repo)

First off, copy and adjust the .env file and the infrastructure.json file.
The former contains the LLM info (e.g. Anthropic, Gemini, etc.), while
the latter file contains the databases that aiHelpDesk needs to be are of.

```
[boris@ ~/helpdesk]$ cp deploy/docker-compose/.env.example deploy/docker-compose/.env
[boris@ ~/helpdesk]$ cp deploy/docker-compose/infrastructure.json.example deploy/docker-compose/infrastructure.json
```

Next, as a human operator, run the interactive session by invoking
the Orchestator:

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

Here's the basic, bare bones session of a human operator talking to aiHelpDesk:

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

## 3. SRE bot demo: Deployment from source (by cloning the repo)

```
[boris@ ~/helpdesk]$ docker run --rm --network docker-compose_default helpdesk:latest /usr/local/bin/srebot -gateway http://gateway:8080 -conn 'host=db.example.com port=5432 dbname=myapp user=admin'
[02:51:08] ── Phase 1: Agent Discovery ──────────────────────────
[02:51:08] GET /api/v1/agents
[02:51:08] Found 3 agents: k8s_agent, incident_agent, postgres_database_agent

[02:51:08] ── Phase 2: Health Check ─────────────────────────────
[02:51:08] POST /api/v1/db/check_connection
[02:51:11] Anomaly detected: "...s you provided.
---
ERROR — check_connection failed for host=db..."

[02:51:11] ── Phase 3: AI Diagnosis ─────────────────────────────
[02:51:11] POST /api/v1/query  agent=database
[02:51:11] Prompt: "Users are reporting database connectivity issues. The connection_string is `host=db.example.com port=5432 dbname=myapp u..."
[02:51:16] Agent response (1838 chars):
           ┌──────────────────────────────────────────────────────────────────────┐
           │ I'll investigate the connectivity issue. Let me start by testing     │
           │ the connection to your database.                                     │
           │ ---                                                                  │
           │ ERROR — check_connection failed for `host=db.example.com           │
           │ port=5432 dbname=myapp user=admin`                                   │
           │                                                                      │
           │ ```                                                                  │
           │ connection failed: The hostname in the connection string could not   │
           │ be resolved. Check for typos in the 'host' parameter and ensure DNS  │
           │ is working.                                                          │
           │                                                                      │
           │ Raw error: psql: error: could not translate host name                │
           │ "db.example.com" to address: Name or service not known               │
           │ ```                                                                  │
           │                                                                      │
           │ This means: The hostname `db.example.com` cannot be resolved by DNS. │
           │                                                                      │
           │ ---                                                                  │
           │                                                                      │
           │ ## Findings                                                          │
           │                                                                      │
           │ The database connectivity issue is due to a **DNS resolution         │
           │ failure**. The system cannot translate the hostname                  │
           │ `db.example.com` to an IP address.                                   │
           │                                                                      │
           │ ### Likely causes:                                                   │
           │ 1. **DNS misconfiguration** — The DNS server cannot resolve the    │
           │ hostname                                                             │
           │ 2. **Typo in hostname** — "db.example.com" may not be the correct  │
           │ hostname                                                             │
           │ 3. **Network/DNS outage** — The DNS service is temporarily         │
           │ unavailable                                                          │
           │ 4. **Network connectivity issue** — The client cannot reach the    │
           │ DNS server                                                           │
           │                                                                      │
           │ ### Recommended next steps:                                          │
           │ 1. **Verify the hostname** — Confirm with your database            │
           │ administrator that `db.example.com` is the correct hostname          │
           │ 2. **Test DNS resolution** — From the client machine, run:         │
           │    ```bash                                                           │
           │    nslookup db.example.com                                           │
           │    # or                                                              │
           │    dig db.example.com                                                │
           │    # or                                                              │
           │    ping db.example.com                                               │
           │    ```                                                               │
           │ 3. **Check DNS configuration** — Ensure your system's DNS servers  │
           │ are properly configured                                              │
           │ 4. **Check network connectivity** — Verify you can reach the       │
           │ network where the database resides                                   │
           │ 5. **Test with an IP address** — If you know the database IP, try  │
           │ connecting directly using the IP instead of the hostname to isolate  │
           │ the DNS issue                                                        │
           │                                                                      │
           │ Once DNS resolution is working, please provide me with the           │
           │ corrected connection string or let me know when the issue is         │
           │ resolved, and I can run a full diagnostic.                           │
           └──────────────────────────────────────────────────────────────────────┘

[02:51:16] ── Phase 4: Create Incident Bundle ───────────────────
[02:51:16] POST /api/v1/incidents
[02:51:16]   infra_key:    srebot-demo
[02:51:16]   callback_url: http://172.19.0.6:9090/callback
[02:51:21] Incident agent responded (825 chars)

[02:51:21] ── Phase 5: Awaiting Callback ────────────────────────
[02:51:21] Listening on :9090 for POST /callback ...
[02:51:21] Callback received!
[02:51:21]   incident_id: 0af54dc1
[02:51:21]   bundle_path: /data/incidents/incident-0af54dc1-20260201-025117.tar.gz
[02:51:21]   layers:      [database, os, storage]
[02:51:21]   errors:      17
[02:51:21]     - database/version.txt: psql failed: exit status 2
Output: psql: error: could not translate host name "db.example.com" to address: Name or service not known
...
[02:51:21] Done.

```
