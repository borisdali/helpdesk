# aiHelpDesk: Deployment for VM-based (non-K8s) databases

## 1. Deployment from binary

There are two ways to deploy and run aiHelpDesk on non-K8s environments, i.e. VMs or bare metal. In both cases the first step is downloading the right tarball for your platform. There are five tarballs available for every aiHelpDesk release. The first one that ends with "-deploy.tar.gz" is intended for section 1.1 below (run aiHelpDesk agents in Docker containers). The other four tarballs are binaries specific to a platform and are intended for section 1.2

```
  ┌─────────────────────────────────────┬──────────────────────────────┐──────────────────────────────────────┐────────────┐
  │           Tarball Name              │               For            │               Platform               │ See section│
  ├─────────────────────────────────────┼──────────────────────────────┤──────────────────────────────────────┤────────────┤
  │ helpdesk-vX.Y.Z-deploy.tar.gz       │ running in Docker containers │ Agnostic                             │    1.1     │
  ├─────────────────────────────────────┼──────────────────────────────┤──────────────────────────────────────┤────────────┤
  │ helpdesk-vX.Y.Z-linux-amd64.tar.gz  │ running on a host            │ Linux on x86_64 (most servers, VMs)  │    1.2     │
  ├─────────────────────────────────────┼                              ┤──────────────────────────────────────┤            │
  │ helpdesk-vX.Y.Z-linux-arm64.tar.gz  │                              │ Linux on ARM (Graviton, Ampere)      │            │
  ├─────────────────────────────────────┼                              ┤──────────────────────────────────────┤            │
  │ helpdesk-vX.Y.Z-darwin-amd64.tar.gz │                              │ macOS on Intel                       │            │
  ├─────────────────────────────────────┼                              ┤──────────────────────────────────────┤            │
  │ helpdesk-vX.Y.Z-darwin-arm64.tar.gz │                              │ macOS on Apple Silicon               │            │
  └─────────────────────────────────────┴──────────────────────────────┘──────────────────────────────────────┘────────────┘
```

The last four tarballs are platform specific and are intended for users who want to run the agents directly on a host without Docker.

Here's the aiHelpDesk release [download page](https://github.com/borisdali/helpdesk/releases/).


  ### 1.1 The Docker route (relies on Docker Compose with the pull of the pre-built image from GHCR)

To run aiHelpDesk in Docker containers, download the "-deploy.tar.gz" platform agnostic tarball and run the following commands:

```
  tar xzf helpdesk-v.0.1.0-deploy.tar.gz
  cd helpdesk-v0.1.0-deploy/docker-compose
  cp .env.example .env
  cp infrastructure.json.example infrastructure.json
  # edit both files
  docker compose up -d

  source ./.env
  docker compose --profile interactive run orchestrator
```

The last two commands are optional and intended for the human operators as it starts an interactive aiHelpDesk session.

**Docker Networking Note:** When running in Docker containers, `localhost` in a connection string refers to the container itself, not the host machine. To connect to a database on the host:
- **Docker Desktop (Mac/Windows):** Use `host.docker.internal` as the hostname
- **Linux:** Use `172.17.0.1` (Docker bridge gateway) or the host's actual IP

**Password Authentication:** To avoid password prompts, create a `.pgpass` file and mount it into the database-agent container. The hostname must **exactly match** the hostname in your connection string:

```bash
# Create .pgpass in the docker-compose directory
cat > .pgpass << 'EOF'
# Format: hostname:port:database:username:password
host.docker.internal:5432:postgres:postgres:YourPassword
orders-db.prod.example.com:5432:orders:app_user:secretpass
EOF
chmod 600 .pgpass
```

Add to `docker-compose.yaml` under the database-agent service:
```yaml
database-agent:
  environment:
    HOME: /home/helpdesk
  volumes:
    - ./.pgpass:/home/helpdesk/.pgpass:ro
```

  ### 1.2 The non-Docker route to run the pre-built binaries on a host

> **Note:** This section 1.2 is left here mostly for historical reasons. Please see now a dedicated page for running aiHelpDesk directly on a host [here](../host/README.md).

To run aiHelpDesk directly on a host (without Docker), download the right platform-specific tarballs and run the following commands:

```
  tar xzf helpdesk-v0.1.0-darwin-arm64.tar.gz   # pick your platform
  cd helpdesk-v0.1.0-darwin-arm64

  (Mac specific): depending on your Mac OS version, you may need to remove the quarantine flag that Mac automatically puts on the software downloaded from the Internet:
	`xattr -d com.apple.quarantine *`

  # Option A: set env vars directly
export HELPDESK_MODEL_VENDOR=anthropic
export HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001
export HELPDESK_API_KEY=<your-API-key>
./startall.sh

  # Option B: use a .env file
cat > .env <<EOF
HELPDESK_MODEL_VENDOR=anthropic
HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001
HELPDESK_API_KEY=your-key
HELPDESK_INFRA_CONFIG=./infrastructure.json
EOF
./startall.sh
```

  The script:
  - Sources `.env` if present
  - Validates required env vars
  - Starts all 3 agents + Gateway in the background (logs go to `/tmp/helpdesk-*.log`)
  - Launches the interactive orchestrator in the foreground
  - Cleans up all background processes on exit/`Ctrl-C`
  - `--no-repl` runs headless (gateway only, no orchestrator)
  - `--stop` kills any running helpdesk processes

N.B: Please note that the binary tarballs expect `psql` and `kubectl` already installed on the host — those are baked into the Docker image (see option 1.1), but not into the Go binaries (this option 1.2).

N.B: Please see the note above in section 1.1 regarding the password prompts.


## 2. Deployment from source (by cloning the repo):

 ### 2.1 Clone the aiHelpDesk repo :-)

 ### 2.2 Run Docker Compose to start all aiHelpDesk agents

```
  docker compose -f deploy/docker-compose/docker-compose.yaml up -d
```

See the [sample log](INSTALL_from_source_sample_log.md)  of running the above commands.


  ## 2.3 Interactive/Human session: Deployment from source (by cloning the repo)

Once all the aiHelpDesk agents are up, copy and adjust the `.env` file and the `infrastructure.json` file.
The former contains the LLM info (e.g. Anthropic, Gemini, etc.), while the latter file contains the databases that aiHelpDesk needs to be are of.

```
  cp deploy/docker-compose/.env.example deploy/docker-compose/.env
  cp deploy/docker-compose/infrastructure.json.example deploy/docker-compose/infrastructure.json
```

If you prefer the list of databases (that go into `infrastructure.json` file) to reside elsewhere, set the `HELPDESK_INFRA_CONFIG` env variable as explained in `.env` file (ignore the Kube stuff, which isn't relevant for the VM deployment):

```
[boris@ ~/helpdesk/deploy/docker-compose]$ cat .env.example
# Model configuration (required)
HELPDESK_MODEL_VENDOR=anthropic
HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001
HELPDESK_API_KEY=<your-api-key-here>

# Kubeconfig path for K8s and incident agents (optional)
KUBECONFIG=~/.kube/config

# Infrastructure inventory for the orchestrator (optional).
# Path to a JSON file describing your database servers, K8s clusters, and VMs.
# Copy the example and edit it with your real servers:
#   cp infrastructure.json.example infrastructure.json
HELPDESK_INFRA_CONFIG=./infrastructure.json
```

Next, as a human operator, run the interactive session by invoking the Orchestator:

```
  docker compose -f deploy/docker-compose/docker-compose.yaml --profile interactive run orchestrator
```

See the [sample log](INSTALL_from_source_sample_interactive_log.md)  of running the above commands.


  ### 2.4 SRE bot demo: Deployment from source (by cloning the repo)

Here's an example of an SRE bot detecting that the `db.example.com` is going offline, which results in a failure to establish a connection. As a result, aiHelpDesk automatically records an incident and creates a troubelshooting bundle to investigate further either interally or by sending to a vendor:

```
  docker run --rm --network helpdesk_default helpdesk:latest srebot -gateway http://gateway:8080 -conn 'host=db.example.com port=5432 dbname=myapp user=admin'
```

See the [sample log](INSTALL_from_source_sample_SRE_bot_log.md)  of running the above commands.


## 3. AI Governance Components

aiHelpDesk includes an [AI Governance framework](../../AIGOVERNANCE.md) with policy-based access control, human-in-the-loop approval workflows, and comprehensive audit logging. The governance components are:

| Component | Description | Port |
|-----------|-------------|------|
| **auditd** | Central audit daemon with SQLite persistence and approval workflow API | 1199 |
| **auditor** | Real-time audit stream monitor with alerting (Slack, email) | - |
| **secbot** | Security responder that creates incidents for security anomalies | 9091 |
| **govbot** | Compliance reporter — runs on demand or on a schedule, posts summary to Slack | - |
| **approvals** | Operator CLI for listing, approving, and denying approval requests | - |
| **govexplain** | Operator CLI for explaining past and hypothetical policy decisions | - |
| **srebot** | SRE automation bot — detects DB anomalies, triggers AI diagnosis + incident bundle | - |

### 3.1 Enabling Governance (Docker Compose)

The audit daemon (`auditd`) runs by default. To enable the optional monitoring components:

```bash
# Start all services including governance monitors
docker compose --profile governance up -d

# Or start just the core services (auditd runs automatically)
docker compose up -d
```

### 3.2 Enabling Governance (Binary Deployment)

```bash
# Start with governance monitoring
./startall.sh --governance

# Or start without governance monitoring
./startall.sh
```

### 3.3 Policy Configuration

Create a policy file to control agent operations:

```yaml
# policies.yaml
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

Mount the policy file and set the environment variable:

```yaml
# docker-compose.yaml addition
database-agent:
  environment:
    HELPDESK_POLICY_FILE: /etc/helpdesk/policies.yaml
  volumes:
    - ./policies.yaml:/etc/helpdesk/policies.yaml:ro
```

### 3.4 Managing Approvals

When an operation requires approval, use the CLI:

```bash
# List pending approvals
docker compose exec auditd /usr/local/bin/approvals list --status pending

# Approve a request
docker compose exec auditd /usr/local/bin/approvals approve apr_xxx --reason "LGTM, verified and it is safe"

# Watch for new approvals interactively
docker compose exec auditd /usr/local/bin/approvals watch
```

Or use the HTTP API directly:

```bash
# List pending approvals
curl http://localhost:1199/v1/approvals/pending

# Approve a request
curl -X POST http://localhost:1199/v1/approvals/apr_xxx/approve \
  -H "Content-Type: application/json" \
  -d '{"approved_by": "admin", "reason": "LGTM, verified and it is safe"}'
```

### 3.5 Explaining Policy Decisions (govexplain)

`govexplain` is baked into the Docker image. Exec into the `gateway` container (which has the auditd URL wired in via `HELPDESK_AUDIT_URL`) to run it:

```bash
# List recent policy decisions (last hour)
docker compose exec gateway govexplain --list --since 1h

# Show only denials
docker compose exec gateway govexplain --list --since 24h --effect deny

# Explain a specific past decision by event ID
docker compose exec gateway govexplain --event tool_a1b2c3d4

# Hypothetical check: what would happen if an agent tried this?
docker compose exec gateway govexplain \
  --resource database:prod-db --action write --tags production

# Talk directly to auditd (no gateway needed)
docker compose exec auditd govexplain \
  --auditd http://localhost:1199 --list --since 1h
```

### 3.6 Running the SRE Bot (srebot)

`srebot` is a one-shot automation tool — run it with `docker run --rm` so it exits cleanly when done:

```bash
docker run --rm --network helpdesk_default helpdesk:latest srebot \
  -gateway http://gateway:8080 \
  -conn 'host=db.example.com port=5432 dbname=myapp user=admin'
```

> **Note:** The network is always `helpdesk_default` because `docker-compose.yaml` pins the project name to `helpdesk` via the top-level `name:` field. If you started the stack without this file (e.g. a very old release), check the actual name with `docker network ls`.

### 3.7 Running the Compliance Reporter (govbot)

`govbot` is included as a `--profile governance` service so you can run it on demand:

```bash
# One-shot compliance report (last 24h, printed to stdout)
docker compose --profile governance run --rm govbot

# Post to Slack
GOVBOT_WEBHOOK=https://hooks.slack.com/services/... \
  docker compose --profile governance run --rm govbot

# Custom look-back window
GOVBOT_SINCE=7d docker compose --profile governance run --rm govbot
```

### 3.8 Security Responder (secbot)

Unlike the CLI tools above, `secbot` is a **long-running daemon** — start it once and leave it running alongside the stack. It reads from the audit socket in real time and automatically creates incident bundles when it detects:

- `unauthorized_destructive` — a destructive tool call without a valid approval
- `hash_mismatch` — audit chain integrity failure (tampered event)
- `high_volume` — event rate exceeds threshold (potential abuse or runaway agent)
- `potential_sql_injection` / `potential_command_injection` — error patterns in tool output

It is included in the `--profile governance` set, so it starts automatically with:

```bash
docker compose --profile governance up -d
```

To tune its behaviour without editing `docker-compose.yaml`, set variables in `.env`:

```bash
# docker-compose.yaml passes these through to secbot already:
# (edit docker-compose.yaml secbot.command if you need other flags)

# Check secbot logs
docker compose logs -f secbot

# Dry-run mode: log alerts but don't create incidents (useful for tuning)
# Edit docker-compose.yaml secbot command and add: --dry-run

# Verify it is connected and watching
docker compose exec secbot cat /proc/1/cmdline | tr '\0' ' '
```

Key flags (set via `command:` in `docker-compose.yaml`):

| Flag | Default | Description |
|------|---------|-------------|
| `-cooldown` | `5m` | Minimum time between incident creations |
| `-max-events-per-minute` | `100` | Alert threshold for high-volume detection |
| `-dry-run` | `false` | Log alerts without creating incidents |
| `-verbose` | `false` | Log every received audit event |

> **Note:** `secbot` must be able to reach the audit socket at `/data/audit/audit.sock` (the shared `audit-data` volume) and the gateway at `http://gateway:8080`. Both are wired correctly in the provided `docker-compose.yaml`.

### 3.9 Notification Configuration

Configure approval and alert notifications in `.env`:

```bash
# Slack webhook for approvals
HELPDESK_APPROVAL_WEBHOOK=https://hooks.slack.com/services/...

# Email notifications
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_USER=user@example.com
SMTP_PASSWORD=your-password
HELPDESK_EMAIL_FROM=helpdesk@example.com
HELPDESK_EMAIL_TO=ops@example.com
```

## 4. Using the Gateway API

In addition to the interactive orchestrator REPL and the governance APIs, the Gateway provides a REST API for programmatic access. See [API.md](../../API.md) for the full reference (all 17 endpoints with request/response shapes and query parameters).

```bash
# Query the system
curl -X POST http://localhost:8080/api/v1/query \
  -H "Content-Type: application/json" \
  -d '{"agent": "database", "message": "What databases are you aware of?"}'

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

The Gateway API is useful for:
- CI/CD pipelines and automation scripts
- Integration with monitoring/alerting systems (see [srebot example](../../cmd/srebot/README.md))
- Environments where interactive TTY access is limited

## 5. Troubleshooting

### Interactive REPL Shows Empty Responses

**Symptom:** When running the interactive orchestrator in a Docker container, agent responses appear empty and require pressing Enter to display.

**Cause:** This is a known issue with the ADK (Agent Development Kit) REPL in containerized environments where TTY handling differs from local execution.

**Workarounds:**

1. **Gateway REPL** (recommended) - Interactive wrapper around the Gateway API:
   ```bash
   ./scripts/gateway-repl.sh http://localhost:8080
   ```

2. **Direct API calls** - Use curl with the Gateway REST API (see Section 3).

3. **Run binaries directly** - Use section 1.2 (non-Docker route) which runs natively on the host.

### Database Agent Prompts for Password

**Symptom:** The database agent asks for a password every time it connects to a database.

**Solution:** Create a `.pgpass` file with entries matching your connection strings.

**Important for Docker deployments:** The hostname in `.pgpass` must **exactly match** the hostname in your `infrastructure.json` connection strings:

```
# If infrastructure.json uses host.docker.internal (to reach host from container):
host.docker.internal:5432:postgres:postgres:YourPassword

# If infrastructure.json uses localhost (for native binary deployment):
localhost:5432:postgres:postgres:YourPassword

# Wildcard for any host (less secure, useful for testing):
*:5432:*:postgres:YourPassword
```

For Docker Compose, mount the `.pgpass` file into the database-agent container by adding to `docker-compose.yaml`:

```yaml
database-agent:
  environment:
    HOME: /home/helpdesk
  volumes:
    - ./.pgpass:/home/helpdesk/.pgpass:ro
```

Then secure the local file:
```bash
chmod 600 .pgpass
```

### Agents Not Discovered

**Symptom:** Orchestrator logs show "agent not available" or discovery failures.

**Solution:** Check that all agent containers are running:
```bash
docker compose ps
docker compose logs orchestrator
```

