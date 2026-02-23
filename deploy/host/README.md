# aiHelpDesk: Host Deployment (Binary Tarball)

This guide covers running aiHelpDesk directly on a host — a Linux or macOS machine — without Docker or Kubernetes. All binaries are statically compiled and have no external runtime dependencies beyond `psql` and `kubectl` (only needed by the database and K8s agents at query time).

## 1. What's in the Tarball

```
helpdesk-vX.Y.Z-linux-amd64/
├── startall.sh                 # Start/stop all services in one command
├── .env.example                # Configuration template — copy to .env and edit
├── infrastructure.json.example # Infrastructure inventory template
├── policies.example.yaml       # Policy rules template
│
├── helpdesk                    # Interactive orchestrator (multi-agent REPL)
├── gateway                     # REST API gateway
├── database-agent              # PostgreSQL diagnostics agent
├── k8s-agent                   # Kubernetes diagnostics agent
├── incident-agent              # Incident bundle collector
├── research-agent              # Web search agent (Gemini models)
│
├── auditd                      # AI Governance: audit daemon
├── auditor                     # AI Governance: real-time audit monitor + alerter
├── secbot                      # AI Governance: security responder
├── govbot                      # AI Governance: compliance reporter
├── approvals                   # AI Governance: approval management CLI
└── govexplain                  # AI Governance: policy explainability CLI
```

## 2. Quick Start

```bash
# 1. Extract the tarball
tar xzf helpdesk-vX.Y.Z-linux-amd64.tar.gz
cd helpdesk-vX.Y.Z-linux-amd64

# 2. Configure
cp .env.example .env
# Edit .env — at minimum set HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY

# 3. (Optional) Configure infrastructure inventory
cp infrastructure.json.example infrastructure.json
# Edit infrastructure.json with your database servers, K8s clusters, and VMs

# 4. Start everything
./startall.sh
```

`startall.sh` starts `auditd`, all agents, and the `gateway` in the background, then drops you into the interactive orchestrator REPL. Type your question and press Enter. Press `Ctrl-C` or type `exit` to stop everything.

## 3. Prerequisites

| Binary | Requires |
|--------|----------|
| `database-agent` | `psql` (PostgreSQL 16+ client) on `PATH` |
| `k8s-agent` | `kubectl` on `PATH`, kubeconfig accessible |
| `incident-agent` | `psql` and `kubectl` (collects from both layers) |
| All others | No external dependencies |

Install psql on common platforms:

```bash
# Debian/Ubuntu
sudo apt-get install -y postgresql-client-16

# RHEL/Rocky/Alma
sudo dnf install -y postgresql16

# macOS (Homebrew)
brew install libpq && brew link --force libpq
```

## 4. Configuration

Copy `.env.example` to `.env` and set at minimum:

```bash
HELPDESK_MODEL_VENDOR=anthropic          # or: gemini
HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001
HELPDESK_API_KEY=<your-api-key>
```

`startall.sh` sources `.env` automatically on startup. All variables exported from `.env` are inherited by every agent process.

**Database credentials:** Create a `.pgpass` file alongside `.env` for passwordless authentication:

```bash
# Format: hostname:port:database:username:password
cat > .pgpass << 'EOF'
prod-db.example.com:5432:myapp:app_user:secretpass
EOF
chmod 600 .pgpass
```

**Infrastructure inventory:** Set `HELPDESK_INFRA_CONFIG` in `.env` to point at your `infrastructure.json` so the orchestrator knows which servers exist:

```bash
HELPDESK_INFRA_CONFIG=./infrastructure.json
```

## 5. Starting and Stopping

```bash
# Start agents + gateway, then open the interactive REPL
./startall.sh

# Start agents + gateway only (headless — no REPL)
./startall.sh --no-repl

# Start with real-time governance monitoring (auditor + secbot)
./startall.sh --governance

# Stop all running helpdesk processes
./startall.sh --stop
```

Logs are written to `/tmp/helpdesk-<service>.log`. Tail them while running:

```bash
tail -f /tmp/helpdesk-database-agent.log
tail -f /tmp/helpdesk-auditd.log
tail -f /tmp/helpdesk-gateway.log
```

## 6. Headless / API Mode

To use the REST gateway API without the interactive REPL:

```bash
./startall.sh --no-repl &

# Query via the gateway
curl -s http://localhost:8080/api/v1/agents | jq .

curl -s -X POST http://localhost:8080/api/v1/query \
  -H "Content-Type: application/json" \
  -d '{"agent": "database", "message": "Check replication lag on prod-db"}'
```

## 7. AI Governance

See [here](../../AIGOVERNANCE.md) for details on aiHelpDesk AI Governance module and its sub-modules.

### 7.1 Enabling Governance

`auditd` starts automatically whenever the binary is present in the tarball directory. To also start the real-time monitor and security responder:

```bash
./startall.sh --governance
```

To enable policy enforcement, set these in `.env` before starting:

```bash
HELPDESK_POLICY_ENABLED=true
HELPDESK_POLICY_FILE=./policies.yaml   # copy and edit policies.example.yaml
```

To require all governance modules (audit, policy, approvals, guardrails) to be active before agents will start, set:

```bash
HELPDESK_OPERATING_MODE=fix
```

### 7.2 Policy Configuration

Copy and edit the bundled example:

```bash
cp policies.example.yaml policies.yaml
# Edit policies.yaml to match your environment
```

Minimal example (`policies.yaml`):

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

See `policies.example.yaml` for the full schema including time-based schedules, principal-based rules, row limits, and multi-approver quorum.

### 7.3 Managing Approvals

When a policy requires human approval, the agent pauses and waits. Use the `approvals` CLI to respond — it reads `HELPDESK_AUDIT_URL` from the environment (set automatically by `startall.sh` when `auditd` is running):

```bash
# List all pending approvals
./approvals pending

# Show details of a specific request
./approvals show apr_abc123

# Approve a request
./approvals approve apr_abc123 --reason "Verified by ops team, safe to proceed"

# Deny a request
./approvals deny apr_abc123 --reason "Not justified — use the read-only report instead"

# Watch for new approvals interactively (polls every 5 seconds)
./approvals watch
```

Or use the HTTP API directly:

```bash
curl http://localhost:1199/v1/approvals/pending

curl -X POST http://localhost:1199/v1/approvals/apr_abc123/approve \
  -H "Content-Type: application/json" \
  -d '{"approved_by": "admin", "reason": "Verified safe"}'
```

### 7.4 Explaining Policy Decisions (govexplain)

`govexplain` queries the audit trail to explain why a past action was allowed or denied, and can also answer hypothetical "what would happen if…" questions. It reads `HELPDESK_AUDIT_URL` from the environment:

```bash
# List recent policy decisions (last hour)
./govexplain --list --since 1h

# Show only denials from the last 24 hours
./govexplain --list --since 24h --effect deny

# Explain a specific past decision by audit event ID
./govexplain --event tool_a1b2c3d4

# Hypothetical: what would happen if an agent tried this action?
./govexplain --resource database:prod-db --action write --tags production

# Hypothetical via the gateway (if gateway is running)
./govexplain --gateway http://localhost:8080 \
  --resource database:prod-db --action destructive --tags production,critical
```

Exit codes: `0` = allowed, `1` = denied, `2` = requires approval, `3` = error.

### 7.5 Running the SRE Bot (srebot)

`srebot` is a one-shot automation tool. Run it while the stack is up — it contacts the gateway, runs AI diagnosis on a database, and creates an incident bundle:

```bash
./srebot \
  -gateway http://localhost:8080 \
  -conn 'host=prod-db.example.com port=5432 dbname=myapp user=admin'

# Skip the anomaly check and always run all phases
./srebot -force \
  -gateway http://localhost:8080 \
  -conn 'host=prod-db.example.com port=5432 dbname=myapp user=admin'

# Custom symptom description
./srebot \
  -gateway http://localhost:8080 \
  -conn 'host=prod-db.example.com port=5432 dbname=myapp user=admin' \
  -symptom "Replication lag exceeded 30 seconds on the primary."
```

### 7.6 Running the Compliance Reporter (govbot)

`govbot` generates a compliance summary from the audit trail and optionally posts it to Slack:

```bash
# Print a report for the last 24 hours
./govbot -gateway http://localhost:8080

# Custom look-back window
./govbot -gateway http://localhost:8080 -since 7d

# Post to Slack
./govbot -gateway http://localhost:8080 \
  -webhook https://hooks.slack.com/services/...
```

### 7.7 Security Responder (secbot)

`secbot` is a **long-running daemon**, not a one-shot CLI. `startall.sh --governance` starts it automatically in the background alongside `auditor`. It reads from the audit socket in real time and automatically creates incident bundles when it detects:

- `unauthorized_destructive` — a destructive tool call without a valid approval
- `hash_mismatch` — audit chain integrity failure (tampered event)
- `high_volume` — event rate exceeds threshold (potential abuse or runaway agent)
- `potential_sql_injection` / `potential_command_injection` — error patterns in tool output

```bash
# Start the full stack including secbot
./startall.sh --governance

# Check its log
tail -f /tmp/helpdesk-secbot.log

# Run it manually with tuned flags (while the rest of the stack is already running)
./secbot \
  -socket="${HELPDESK_AUDIT_SOCKET:-/tmp/helpdesk-audit.sock}" \
  -gateway=http://localhost:8080 \
  -cooldown=10m \
  -max-events-per-minute=50

# Dry-run: log alerts without creating incidents (useful for initial tuning)
./secbot \
  -socket="${HELPDESK_AUDIT_SOCKET:-/tmp/helpdesk-audit.sock}" \
  -gateway=http://localhost:8080 \
  -dry-run -verbose
```

Key flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-socket` | `/tmp/helpdesk-audit.sock` | Path to the audit Unix socket (must match `HELPDESK_AUDIT_SOCKET`) |
| `-gateway` | `http://localhost:8080` | Gateway URL for creating incidents |
| `-cooldown` | `5m` | Minimum time between incident creations |
| `-max-events-per-minute` | `100` | High-volume alert threshold |
| `-dry-run` | `false` | Log alerts without creating incidents |
| `-verbose` | `false` | Log every received audit event |

## 8. Troubleshooting

### Port already in use

`startall.sh` uses fixed ports (1100, 1102, 1104, 1106, 1199, 8080). If a previous run did not clean up:

```bash
./startall.sh --stop
# or kill by port:
lsof -ti :8080 | xargs kill
```

### Agent exits immediately

Check the log:

```bash
cat /tmp/helpdesk-database-agent.log
```

Common causes: missing `HELPDESK_MODEL_VENDOR`/`HELPDESK_API_KEY`, `HELPDESK_OPERATING_MODE=fix` with governance not fully configured, or `psql`/`kubectl` not on `PATH`.

### approvals / govexplain: "audit service URL required"

`auditd` must be running (it starts automatically via `startall.sh`). If you ran a binary directly without `startall.sh`, set the URL explicitly:

```bash
HELPDESK_AUDIT_URL=http://localhost:1199 ./approvals pending
HELPDESK_AUDIT_URL=http://localhost:1199 ./govexplain --list --since 1h
```

### Database connection refused

On Linux, `localhost` in a connection string resolves to the loopback interface. If your database is on a remote host, use its actual hostname or IP. For Unix socket connections, use `host=/var/run/postgresql`.
