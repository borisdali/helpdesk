# aiHelpDesk Client (`helpdesk-client`)

`helpdesk-client` is an authenticated interactive CLI for the aiHelpDesk Gateway. It's a better alternative to the legacy's `kubectl exec` and `docker compose run orchestrator` sessions with a connection that carries a verified identity and declared purpose on every request — so every operator query appears in the audit trail with full attribution.

Unlike the in-cluster Orchestrator REPL, `helpdesk-client` runs on your workstation and talks to the Gateway over HTTP. The Gateway enforces identity and purpose before forwarding requests to the agents.

For the Gateway REST API it calls, see [API.md](API.md). For identity provider setup, see [IDENTITY.md](IDENTITY.md).

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

`helpdesk-client` is not deployed as a Pod — it runs on your workstation and connects to the Gateway over the network.

**Option A: Extract from the release tarball**

Download the tarball for your workstation platform from the [release page](https://github.com/borisdali/helpdesk/releases/) and follow section 1.1.

**Option B: Extract from the container image**

```bash
docker run --rm --entrypoint cat ghcr.io/borisdali/helpdesk:latest \
  /usr/local/bin/helpdesk-client > helpdesk-client
chmod +x helpdesk-client
```

Then expose the Gateway so your workstation can reach it (see Section 5).

---

## 2. Quick Start

```bash
# Interactive REPL against a local Gateway
helpdesk-client --user alice@example.com --purpose diagnostic

# One-shot query
helpdesk-client \
  --user alice@example.com \
  --purpose diagnostic \
  --message "Show me the top 5 longest-running queries"

# Service account (API key) against a remote Gateway
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

Every request must carry a declared purpose. The Gateway can enforce this via the `requirePurposeForSensitive` policy setting (see [IDENTITY.md](IDENTITY.md)).

| Value | When to use |
|-------|-------------|
| `diagnostic` | Routine investigation: checking metrics, query plans, Pod status |
| `remediation` | Active intervention: cancelling queries, restarting deployments |
| `compliance` | Audit, reporting, or policy review work |
| `emergency` | Incident response where speed is critical and full audit trail is required |
| `fleet_rollout` | Automated multi-target change applied by the fleet runner (see [FLEET.md](FLEET.md)) |

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

The Gateway runs on `localhost:8080` by default. No extra configuration is needed:

```bash
helpdesk-client --user alice@example.com --purpose diagnostic
```

### 5.2 Docker Compose deployment

Inside the compose network the Gateway is reachable at `http://gateway:8080`. This is the default when using `docker compose run` (see Section 1.2).

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

Omit `--message` to enter the interactive REPL. `helpdesk-client` prints a prompt, sends each line to the Gateway, shows a spinner while waiting, and prints the response followed by a trace footer:

```
> show me all databases
Checking agent availability... done

The Gateway is aware of 3 databases:
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

For production, enable the `static` or `jwt` identity provider so the Gateway verifies the caller's identity. See [IDENTITY.md](IDENTITY.md) for setup instructions.

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
  --message "List all Pods in CrashLoopBackOff across all namespaces"
```

```bash
helpdesk-client \
  --user alice@example.com \
  --agent k8s \
  --purpose remediation \
  --purpose-note "INC-5102 payments Pod OOMKilled" \
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

## 10. Manual Testing Playbook

This section covers step-by-step verification of all three identity provider modes against a local docker-compose stack unless noted otherwise.

**Prerequisites:** stack up (`docker compose up -d`), `hashapikey` binary available.

**How to invoke `helpdesk-client`** — the commands below use the bare binary name for brevity. Substitute for your deployment:

| Deployment | Invocation |
|---|---|
| Host binary tarball | `./helpdesk-client` |
| Docker Compose | `docker compose --profile interactive run --rm helpdesk-client` |
| Kubernetes | `kubectl -n helpdesk-system run hc --rm -it --restart=Never --image=ghcr.io/borisdali/helpdesk:latest -- /usr/local/bin/helpdesk-client --gateway http://helpdesk-gateway:8080` |

On docker-compose the `--gateway` flag can be omitted — `HELPDESK_GATEWAY_URL=http://gateway:8080` is already set in the service environment. On host and Kubernetes it defaults to `http://localhost:8080`; override with `--gateway` or `HELPDESK_GATEWAY_URL` as needed.

### 10.1 `none` mode (default — no validation)

**Setup:** default `.env` — `HELPDESK_IDENTITY_PROVIDER` unset or `none`.

```bash
# Any X-User value is accepted verbatim — no users.yaml lookup
helpdesk-client \
  --user alice@example.com \
  --purpose diagnostic \
  --message "What databases are you aware of?"

# Unknown user passes too — header is trusted as-is
helpdesk-client \
  --user totally-fake-user \
  --purpose diagnostic \
  --message "What databases are you aware of?"
```

Expected: both succeed. Verify audit trail shows the `X-User` value as-is with `auth_method: "header"`:

```bash
curl -s "http://localhost:1199/v1/events?limit=5" | \
  jq '.[] | {principal, purpose, auth_method: .principal.auth_method}'
```

### 10.2 `static` mode — human user authentication

**Setup:** set in `.env`:

```
HELPDESK_IDENTITY_PROVIDER=static
HELPDESK_USERS_FILE_HOST=./users.yaml
```

Add a test user to `users.yaml`:

```yaml
users:
  - id: alice@example.com
    roles: [dba, sre]
```

Restart the Gateway: `docker compose restart gateway`

```bash
# Known user — succeeds
helpdesk-client \
  --user alice@example.com \
  --purpose diagnostic \
  --message "What databases are you aware of?"

# Unknown user — expect 401
helpdesk-client \
  --user nobody@example.com \
  --purpose diagnostic \
  --message "What databases are you aware of?"
```

Expected: first succeeds with `auth_method: "static"`, second fails with `401 Unauthorized`. Verify the 401 was recorded:

```bash
curl -s "http://localhost:1199/v1/events?outcome_status=error&limit=5" | \
  jq '.[] | {principal, error: .outcome.error_message}'
```

### 10.3 `static` mode — service account API key

**Generate a key and hash it:**

```bash
# Generate
export MY_KEY=$(openssl rand -hex 32)
echo "Key: $MY_KEY"

# Hash it (binary tarball)
./hashapikey "$MY_KEY"

# Hash it (docker compose)
docker compose run --rm --entrypoint /usr/local/bin/hashapikey auditd "$MY_KEY"
```

Add the service account to `users.yaml`:

```yaml
service_accounts:
  - id: test-bot
    roles: [sre-automation]
    api_key_hash: "$argon2id$..."   # paste hash output
```

Restart Gateway: `docker compose restart gateway`

```bash
# Valid key — succeeds with auth_method: "api_key"
helpdesk-client \
  --api-key "$MY_KEY" \
  --purpose diagnostic \
  --message "What databases are you aware of?"

# Wrong key — expect 401
helpdesk-client \
  --api-key "wrong-key" \
  --purpose diagnostic \
  --message "What databases are you aware of?"
```

Verify identity in the audit trail:

```bash
curl -s "http://localhost:1199/v1/events?limit=3" | \
  jq '.[] | {service: .principal.service, auth_method: .principal.auth_method, purpose}'
```

### 10.4 `jwt` mode — local test IdP

`jwttest` is a development tool available only when building from source — it is not included in release tarballs or the container image. Run it on your workstation; the Gateway fetches the JWKS endpoint over the network.

**Terminal 1 — start the mock JWKS server (requires source checkout):**

```bash
go run ./cmd/jwttest \
  -sub alice@example.com \
  -groups dba,sre \
  -port 9999 > /tmp/jwt.txt
# stderr prints the exact HELPDESK_JWT_* env vars to set
```

**Terminal 2 — configure the Gateway and test:**

Set in `.env` (use the values printed by `jwttest` on stderr):

```
HELPDESK_IDENTITY_PROVIDER=jwt
HELPDESK_JWT_ISSUER=http://localhost:9999
HELPDESK_JWT_AUDIENCE=helpdesk
```

For `HELPDESK_JWT_JWKS_URL`, the value depends on how the Gateway reaches your workstation:

| Deployment | JWKS URL |
|---|---|
| Host | `http://localhost:9999/.well-known/jwks.json` |
| Docker Compose (Mac/Windows) | `http://host.docker.internal:9999/.well-known/jwks.json` |
| Docker Compose (Linux) | `http://172.17.0.1:9999/.well-known/jwks.json` (use `ip route` to confirm) |
| Kubernetes | Expose `jwttest` via `kubectl port-forward` or a `NodePort` |

Restart Gateway: `docker compose restart gateway`

```bash
TOKEN=$(cat /tmp/jwt.txt | tr -d '\n')

# Valid JWT — succeeds
helpdesk-client \
  --api-key "$TOKEN" \
  --purpose diagnostic \
  --message "What databases are you aware of?"
```

Verify roles were extracted:

```bash
curl -s "http://localhost:1199/v1/events?limit=3" | \
  jq '.[] | {user_id: .principal.user_id, roles: .principal.roles, auth_method: .principal.auth_method}'
```

**Test expired token:**

```bash
go run ./cmd/jwttest -ttl 1s -sub alice@example.com > /tmp/jwt-expired.txt
sleep 2
TOKEN_EXP=$(cat /tmp/jwt-expired.txt | tr -d '\n')
helpdesk-client --api-key "$TOKEN_EXP" --purpose diagnostic --message "ping"
# Expected: 401 — token has expired
```

**Test wrong issuer:**

```bash
go run ./cmd/jwttest -iss https://evil.example.com > /tmp/jwt-bad-iss.txt
TOKEN_BAD=$(cat /tmp/jwt-bad-iss.txt | tr -d '\n')
helpdesk-client --api-key "$TOKEN_BAD" --purpose diagnostic --message "ping"
# Expected: 401 — issuer mismatch
```

### 10.5 Policy enforcement across modes

Once authentication is working, verify that the resolved identity flows correctly into policy decisions. Using `static` mode with `alice` (roles: `[dba, sre]`):

```bash
# Purpose not in allowed list for a pii-tagged database → expect 403
helpdesk-client \
  --user alice@example.com \
  --purpose fleet_rollout \
  --message "Check connection to pg-cluster-minkube"

# Correct purpose → should pass
helpdesk-client \
  --user alice@example.com \
  --purpose diagnostic \
  --message "Check connection to pg-cluster-minkube"
```

Inspect the policy decision audit events:

```bash
curl -s "http://localhost:1199/v1/events?event_type=policy_decision&limit=10" | \
  jq '.[] | {effect: .policy_decision.effect, purpose: .policy_decision.purpose, user: .policy_decision.user_id}'
```

---

## 12. Related Documentation

- [API.md](API.md) — Gateway REST API reference (all endpoints, request/response shapes)
- [IDENTITY.md](IDENTITY.md) — Identity provider setup (static, JWT)
- [AIGOVERNANCE.md](AIGOVERNANCE.md) — Governance framework overview
