# aiHelpDesk Client (`helpdesk-client`)

`helpdesk-client` is an authenticated interactive CLI for the aiHelpDesk gateway. It replaces `kubectl exec` and `docker compose run orchestrator` sessions with a connection that carries a verified identity and declared purpose on every request — so every operator query appears in the audit trail with full attribution.

Unlike the in-cluster Orchestrator REPL, `helpdesk-client` runs on your workstation and talks to the gateway over HTTP. The gateway enforces identity and purpose before forwarding requests to the agents.

For the gateway REST API it calls, see [API.md](API.md). For identity provider setup, see [IDENTITY.md](IDENTITY.md).

---

## 1. Installation

### 1.1 Host (binary tarball)

`helpdesk-client` is included in every platform-specific tarball. After extracting the tarball it is ready to use:

```bash
tar xzf helpdesk-vX.Y.Z-darwin-arm64.tar.gz   # pick your platform
cd helpdesk-vX.Y.Z-darwin-arm64
./helpdesk-client --version
```

On macOS, you may need to remove the quarantine attribute:

```bash
xattr -d com.apple.quarantine helpdesk-client
```

### 1.2 Docker Compose

`helpdesk-client` is included as an `interactive` profile service in the provided `docker-compose.yaml`. No installation step is needed — run it directly with `docker compose`:

```bash
# Interactive REPL
docker compose -f deploy/docker-compose/docker-compose.yaml \
  --profile interactive run --rm helpdesk-client

# One-shot query
docker compose -f deploy/docker-compose/docker-compose.yaml \
  --profile interactive run --rm helpdesk-client \
  --message "What databases are you aware of?"
```

Inside the compose network, `helpdesk-client` connects to `http://gateway:8080` automatically.

### 1.3 Helm / Kubernetes

`helpdesk-client` is not deployed as a pod — it runs on your workstation and connects to the gateway over the network.

**Option A: Extract from the release tarball**

Download the tarball for your workstation platform from the [release page](https://github.com/borisdali/helpdesk/releases/) and follow section 1.1.

**Option B: Extract from the container image**

```bash
docker run --rm --entrypoint cat ghcr.io/borisdali/helpdesk:latest \
  /usr/local/bin/helpdesk-client > helpdesk-client
chmod +x helpdesk-client
```

Then expose the gateway so your workstation can reach it (see Section 5).

---

## 2. Quick Start

```bash
# Interactive REPL against a local gateway
helpdesk-client --user alice@example.com --purpose diagnostic

# One-shot query
helpdesk-client \
  --user alice@example.com \
  --purpose diagnostic \
  --message "Show me the top 5 longest-running queries"

# Service account (API key) against a remote gateway
helpdesk-client \
  --gateway https://helpdesk.internal.example.com \
  --api-key "$HELPDESK_CLIENT_API_KEY" \
  --purpose remediation \
  --purpose-note "INC-4821 orders-db high latency" \
  --agent database \
  --message "Are there any blocked queries on the orders database?"
```

---

## 3. Flags and Environment Variables

All flags have a corresponding environment variable. The flag takes precedence when both are set.

| Flag | Environment Variable | Default | Description |
|------|----------------------|---------|-------------|
| `--gateway` | `HELPDESK_GATEWAY_URL` | `http://localhost:8080` | Gateway base URL |
| `--user` | `HELPDESK_CLIENT_USER` | _(none)_ | Operator identity sent as `X-User` header |
| `--api-key` | `HELPDESK_CLIENT_API_KEY` | _(none)_ | Bearer token for service account authentication |
| `--purpose` | `HELPDESK_SESSION_PURPOSE` | _(none)_ | Session purpose (see Section 4) |
| `--purpose-note` | `HELPDESK_SESSION_PURPOSE_NOTE` | _(none)_ | Free-text context, e.g. incident ticket number |
| `--agent` | `HELPDESK_CLIENT_AGENT` | `database` | Target agent: `database`, `k8s`, `incident`, `research` |
| `--message` | _(flag only)_ | _(none)_ | One-shot message; omit for interactive REPL |
| `--timeout` | _(flag only)_ | `5m` | Per-request timeout |
| `--version` | _(flag only)_ | _(n/a)_ | Print version and exit |

### Using environment variables

Set variables in your shell profile or in a `.env` file for convenience:

```bash
export HELPDESK_GATEWAY_URL=https://helpdesk.internal.example.com
export HELPDESK_CLIENT_USER=alice@example.com
export HELPDESK_SESSION_PURPOSE=diagnostic
```

Then simply run:

```bash
helpdesk-client
```

---

## 4. Session Purpose

Every request must carry a declared purpose. The gateway can enforce this via the `requirePurposeForSensitive` policy setting (see [IDENTITY.md](IDENTITY.md)).

| Value | When to use |
|-------|-------------|
| `diagnostic` | Routine investigation: checking metrics, query plans, pod status |
| `remediation` | Active intervention: cancelling queries, restarting deployments |
| `compliance` | Audit, reporting, or policy review work |
| `emergency` | Incident response where speed is critical and full audit trail is required |

Pair `--purpose` with `--purpose-note` to give auditors context, for example:

```bash
helpdesk-client \
  --purpose remediation \
  --purpose-note "INC-4821: orders-db connection pool exhaustion"
```

The purpose and note appear verbatim in every audit event generated during the session.

---

## 5. Connecting to the Gateway

### 5.1 Host deployment

The gateway runs on `localhost:8080` by default. No extra configuration is needed:

```bash
helpdesk-client --user alice@example.com --purpose diagnostic
```

### 5.2 Docker Compose deployment

Inside the compose network the gateway is reachable at `http://gateway:8080`. This is the default when using `docker compose run` (see Section 1.2).

From a workstation outside the compose network, use the published port (default `8080`):

```bash
helpdesk-client \
  --gateway http://localhost:8080 \
  --user alice@example.com \
  --purpose diagnostic
```

### 5.3 Kubernetes (Helm) deployment

The gateway uses `ClusterIP` by default. Choose one of the following approaches.

**Port-forward (recommended for occasional access):**

```bash
kubectl -n helpdesk-system port-forward svc/helpdesk-gateway 8080:8080
```

Then, in a separate terminal:

```bash
helpdesk-client \
  --gateway http://localhost:8080 \
  --user alice@example.com \
  --purpose diagnostic
```

**LoadBalancer (recommended for team-wide access):**

```bash
helm upgrade helpdesk ./helm/helpdesk \
  --namespace helpdesk-system \
  --set gateway.service.type=LoadBalancer \
  --reuse-values

# Wait for the external IP
kubectl -n helpdesk-system get svc helpdesk-gateway
```

Then point the client at the external IP or hostname:

```bash
helpdesk-client \
  --gateway http://<EXTERNAL-IP>:8080 \
  --user alice@example.com \
  --purpose diagnostic
```

---

## 6. Modes of Operation

### 6.1 Interactive REPL

Omit `--message` to enter the interactive REPL. `helpdesk-client` prints a prompt, sends each line to the gateway, shows a spinner while waiting, and prints the response followed by a trace footer:

```
> show me all databases
Checking agent availability... done

The gateway is aware of 3 databases:
  - prod-orders-db (Orders Production Database)
  - prod-users-db (Users Production Database)
  - legacy-analytics-db (Legacy Analytics)

[trace: task-a1b2c3d4  2026-03-18T14:22:05Z]

>
```

Type `exit` or press `Ctrl-D` to quit.

### 6.2 One-Shot Mode

Pass `--message` to send a single query and exit. Useful for scripts and CI pipelines:

```bash
helpdesk-client \
  --user srebot \
  --api-key "$HELPDESK_CLIENT_API_KEY" \
  --purpose diagnostic \
  --message "Are there any long-running queries on the orders database?" \
  --timeout 2m
```

The exit code is `0` on success and non-zero on error.

---

## 7. Authentication

### 7.1 Human operators (`--user`)

`--user` sets the `X-User` header. With the `none` identity provider (development default), the header is accepted as-is and recorded in the audit trail without verification.

For production, enable the `static` or `jwt` identity provider so the gateway verifies the caller's identity. See [IDENTITY.md](IDENTITY.md) for setup instructions.

### 7.2 Service accounts (`--api-key`)

`--api-key` sends an `Authorization: Bearer <token>` header. The `static` identity provider validates this against an Argon2id hash in `users.yaml`. Service accounts are the recommended approach for automation, bots, and CI pipelines.

To generate a key hash:

```bash
# Docker Compose
docker compose run --rm auditd hashapikey

# Kubernetes
kubectl -n helpdesk-system run hashapikey --rm -it --restart=Never \
  --image=ghcr.io/borisdali/helpdesk:v0.6.0 -- hashapikey
```

Copy the printed hash into the `api_key_hash` field in `users.yaml`. See [IDENTITY.md](IDENTITY.md) for the full service account setup.

---

## 8. Per-Agent Examples

### 8.1 Database agent

```bash
# Interactive session targeting the database agent
helpdesk-client \
  --user alice@example.com \
  --agent database \
  --purpose diagnostic

# Example queries in the REPL:
# > What is the replication lag on prod-orders-db?
# > Show me the top 10 queries by total execution time
# > Are there any connections waiting on locks?
# > What is the current connection count by database?
```

One-shot:

```bash
helpdesk-client \
  --user alice@example.com \
  --agent database \
  --purpose remediation \
  --purpose-note "INC-4821 orders-db latency spike" \
  --message "Cancel all queries running longer than 10 minutes on prod-orders-db"
```

### 8.2 Kubernetes agent

```bash
helpdesk-client \
  --user alice@example.com \
  --agent k8s \
  --purpose diagnostic \
  --message "List all pods in CrashLoopBackOff across all namespaces"
```

```bash
helpdesk-client \
  --user alice@example.com \
  --agent k8s \
  --purpose remediation \
  --purpose-note "INC-5102 payments pod OOMKilled" \
  --message "Restart the payments deployment in the production namespace"
```

### 8.3 Incident agent

```bash
helpdesk-client \
  --user alice@example.com \
  --agent incident \
  --purpose emergency \
  --purpose-note "INC-5200 multi-service outage" \
  --message "Create an incident bundle for the current database and K8s failures"
```

### 8.4 Research agent

```bash
helpdesk-client \
  --user alice@example.com \
  --agent research \
  --purpose compliance \
  --message "Summarise all destructive operations performed in the last 7 days"
```

---

## 9. Trace ID Footer

On a TTY, every response ends with a trace footer:

```
[trace: task-a1b2c3d4  2026-03-18T14:22:05Z]
```

The task ID can be used to look up the full audit event in `auditd`:

```bash
# Docker Compose
curl http://localhost:1199/v1/events/task-a1b2c3d4

# Kubernetes (port-forward auditd first)
kubectl -n helpdesk-system port-forward svc/helpdesk-auditd 1199:1199
curl http://localhost:1199/v1/events/task-a1b2c3d4
```

The audit event includes the full tool call log, the declared purpose, operator identity, and the governance decision (allow / require_approval / deny) for each action taken during the request.

---

## 10. Related Documentation

- [API.md](API.md) — Gateway REST API reference (all endpoints, request/response shapes)
- [IDENTITY.md](IDENTITY.md) — Identity provider setup (static, JWT)
- [AIGOVERNANCE.md](AIGOVERNANCE.md) — Governance framework overview
