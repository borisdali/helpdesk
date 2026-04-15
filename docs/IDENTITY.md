# aiHelpDesk Identity & Access

This document covers the Identity & Access sub-module of
aiHelpDesk [AI Governance](AIGOVERNANCE.md). See that document for context
on how Identity & Access fits into the broader governance architecture.

## 1. Why Role-Based Access (RBAC) Alone Is Insufficient

At aiHelpDesk we stipulate that agentic systems require even tighter control
than those accessed strictly by humans. We strongly discourage allowing agents
to use system-wide predefined roles, especially those shared with humans (e.g.
DBA). Carving out a dedicated agent role is a good first step, but once you
allow an agent to access your data, you should have the flexibility to *exclude*
actions the agent should not attempt. An example: exporting sensitive customer
data. If that is a legitimate operation, do not allow it without a clear,
agent-provided justification.

Hence the three access control dimensions this sub-module adds on top of the
existing structural resource dimension of policy tags:

| Dimension | Description |
|-----------|-------------|
| **Role** | Verified identity → resolved roles (from IdP or `users.yaml`) |
| **Data sensitivity** | Explicit sensitivity class per resource: `pii`, `sensitive`, `internal`, `public`, `critical` |
| **Purpose** | Declared per request; enforced as a policy condition alongside role and sensitivity |

The three dimensions answer *who* is asking and *why*; the tag-based dimensions
answer *what* and against *which resource*. A full policy decision is the
intersection of both. For example: "alice (role=dba-agent, purpose=remediation)
writing to a sensitive production PII database" — matching all five of the axes
results in either `deny` or `allow` (possibly with the human review/approval).

---

## 2. Identity Provider

Authentication happens at the Gateway — the single entry point for all external
requests. The Gateway instantiates an identity provider, resolves a
`ResolvedPrincipal` for every incoming request, and attaches it to the
`TraceContext` that flows through the rest of the stack.

Three provider modes are supported via `HELPDESK_IDENTITY_PROVIDER`:

| Mode | Use case | Mechanism |
|------|----------|-----------|
| `none` | Default — backwards compatible | `X-User` header accepted as-is; no validation; no role resolution |
| `static` | Self-hosted / simple deployments | Users, roles, and service account API keys declared in `users.yaml` |
| `jwt` | Orgs with SSO (Okta, Auth0, Azure AD, Google) | JWT validated against JWKS endpoint; roles extracted from a configured claim |

`none` preserves behaviour from before this sub-module was introduced — existing
deployments continue to work without any configuration change.

### 2.1 Go Interface

```go
// Provider authenticates a request and returns the resolved principal.
type Provider interface {
    Resolve(r *http.Request) (ResolvedPrincipal, error)
}

// ResolvedPrincipal is the verified identity attached to a request.
type ResolvedPrincipal struct {
    UserID     string   // Verified user ID (email, JWT sub, service account name)
    Roles      []string // Resolved roles (from users.yaml or JWT claim)
    Service    string   // Non-empty for service accounts (e.g. "srebot")
    AuthMethod string   // "api_key", "jwt", "header" (legacy no-auth), "static"
}

// IsAnonymous returns true when identity was not verified (AuthMethod == "header").
func (p ResolvedPrincipal) IsAnonymous() bool { return p.AuthMethod == "header" }

// EffectiveID returns Service if set, otherwise UserID.
func (p ResolvedPrincipal) EffectiveID() string { ... }
```

### 2.2 Static Identity Provider

Configured via `HELPDESK_USERS_FILE` (default: `/etc/helpdesk/users.yaml`).

```yaml
# /etc/helpdesk/users.yaml

# Optional: map your IdP group names to aiHelpDesk canonical role names.
# See docs/AUTHZ.md §6 for details.
role_aliases:
  database-admin: dba
  platform-sre: sre

users:
  - id: alice@example.com
    roles: [dba, sre]           # canonical names, or alias names (expanded at resolve time)

  - id: bob@example.com
    roles: [developer]

service_accounts:
  - id: srebot
    roles: [sre-automation]
    api_key_hash: "$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash-A>"

  - id: secbot
    roles: [security-scanner]
    api_key_hash: "$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash-B>"

  - id: fleet-runner
    roles: [sre-automation]
    api_key_hash: "$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash-C>"
```

Human users authenticate via `X-User: alice@example.com`. In `static` mode the
header value is looked up in `users.yaml` — users not in the file receive a
401. In `none` mode the header is accepted as-is with no validation.

Service accounts authenticate via `Authorization: Bearer <api-key>`. The key
is hashed with Argon2id and compared against `api_key_hash`.

> **Critical: each service account must have a unique API key.**
> The identity provider iterates service accounts in map order (non-deterministic
> in Go) and returns the first account whose hash matches. If two accounts share
> the same hash — a mistake easy to make when copy-pasting from an example —
> the resolved identity is whichever account happens to come first. The other
> account's audit trail, policy rules, and `principal_mismatch` diagnosis will
> all be wrong. Generate an independent key for every service account:
>
> ```bash
> openssl rand -hex 32          # plaintext key → goes to FLEET_RUNNER_API_KEY
> go run ./cmd/hashapikey       # hash → goes to fleet-runner.api_key_hash
> ```

**Generating an API key hash:**

```bash
# Interactive (hidden input):
go run ./cmd/hashapikey

# From argument:
go run ./cmd/hashapikey my-secret-key

# From pipe:
echo -n "my-secret-key" | go run ./cmd/hashapikey
```

The `hashapikey` binary is included in all release tarballs.

### 2.3 JWT Identity Provider

```bash
export HELPDESK_IDENTITY_PROVIDER="jwt"
export HELPDESK_JWT_JWKS_URL="https://idp.example.com/.well-known/jwks.json"
export HELPDESK_JWT_ISSUER="https://idp.example.com/"
export HELPDESK_JWT_ROLES_CLAIM="groups"   # JWT claim containing role list
export HELPDESK_JWT_AUDIENCE="helpdesk"    # optional — validates aud claim
export HELPDESK_JWT_CACHE_TTL="5m"         # JWKS key cache TTL (0 = no cache)
```

The Gateway validates the JWT signature against the JWKS endpoint, checks
expiry, issuer, and audience, then extracts `sub` as `UserID` and the
configured claim (default: `groups`) as `Roles`. Supported algorithms:
RS256, RS384, RS512, ES256, ES384, ES512. HMAC algorithms (HS256 etc.) are
not supported — they require the gateway to hold the secret key, which defeats
the purpose of JWKS.

**Role names in JWT mode:** Role values from the claim are used verbatim — the
`role_aliases` field in `users.yaml` has no effect in JWT mode. Ensure your IdP
emits the canonical role names (`dba`, `sre`, `fleet-operator`, etc.) in the
configured claim, or use `HELPDESK_JWT_ROLES_CLAIM` to point at a dedicated
claim that already uses canonical names. See [AUTHZ.md §6.2](AUTHZ.md#62-jwt-provider-helpdesk_identity_providerjwt) for details.

JWKS keys are cached with TTL to avoid per-request round-trips to the IdP.
When a JWT carries no `kid` and the JWKS has exactly one key, that key is used
automatically (common for dev/internal IdPs that omit `kid`).

**Common IdP JWKS URLs:**

| IdP | JWKS URL |
|-----|----------|
| Okta | `https://{domain}/oauth2/v1/keys` |
| Auth0 | `https://{tenant}.auth0.com/.well-known/jwks.json` |
| Google | `https://www.googleapis.com/oauth2/v3/certs` |
| Keycloak | `https://{host}/realms/{realm}/protocol/openid-connect/certs` |
| Azure AD | `https://login.microsoftonline.com/{tenant}/discovery/v2.0/keys` |

**Development / local testing** — `jwttest` generates an in-memory RSA key
pair, serves a JWKS endpoint, and prints a signed RS256 JWT:

```bash
# Terminal 1: start mock JWKS server, write token to file
go run ./cmd/jwttest > /tmp/jwt.txt
# stderr shows the exact gateway env vars to use

# Terminal 2: read token, make requests
TOKEN=$(cat /tmp/jwt.txt | tr -d '\n')
curl -H "Authorization: Bearer $TOKEN" ...

# Flags:
#   -sub alice@example.com   JWT sub claim
#   -groups dba,sre          comma-separated groups
#   -iss https://...         issuer
#   -aud helpdesk            audience
#   -ttl 1h                  token validity
#   -port 9999               JWKS server port
#   -kid ""                  omit kid from JWT header
```

See sample log of testing authn via `jwttest` on K8s [here](IDENTITY_JWT_SAMPLE.md).

### 2.4 Fleet-Runner Authentication

`fleet-runner` is a service account, not a human. It authenticates via
`Authorization: Bearer <api-key>` in all provider modes.

**Static provider (recommended for self-hosted):**

```bash
# Generate and register the key
openssl rand -hex 32 | tee /tmp/fleet-runner-key.txt
go run ./cmd/hashapikey < /tmp/fleet-runner-key.txt  # paste hash into users.yaml

# Configure fleet-runner
FLEET_RUNNER_API_KEY=$(cat /tmp/fleet-runner-key.txt)
```

`users.yaml` entry:
```yaml
service_accounts:
  - id: fleet-runner
    roles: [sre-automation]
    api_key_hash: "<output of hashapikey>"
```

**JWT provider (for organisations with SSO):**

If `HELPDESK_IDENTITY_PROVIDER=jwt`, the gateway validates Bearer tokens as
JWTs. Fleet-runner can use a machine identity issued by your IdP rather than a
static API key. Typical setup with Kubernetes workload identity or a service
account token:

```bash
# Fleet-runner reads a projected service account token
FLEET_RUNNER_API_KEY=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)
```

The JWT must contain:
- `sub`: the service account name (e.g. `fleet-runner@your-domain.com` or the
  K8s service account format `system:serviceaccount:ops:fleet-runner`)
- Your configured `HELPDESK_JWT_ROLES_CLAIM` must include `sre-automation` (or
  whichever role your fleet-runner policy matches on)

Policy rules that target fleet-runner by service account name work with static
provider (`service: fleet-runner` matches `Principal.Service`). With JWT
provider, the identity resolves as a user (`Principal.UserID`), not a service
account, so policies should match by role instead:

```yaml
# Static provider: match by service account name
principals:
  - service: fleet-runner

# JWT provider: match by role
principals:
  - role: sre-automation
```

The gateway's `GET /api/v1/governance/explain` endpoint automatically injects
the caller's resolved identity (including `service` for service accounts) as
query parameters before forwarding to auditd, so explain results reflect the
actual principal. You can also pass identity explicitly:

```bash
# Explain as fleet-runner service account (static provider)
curl "http://localhost:8080/api/v1/governance/explain?resource_type=database&resource_name=prod-db&action=read&purpose=fleet_rollout" \
  -H "Authorization: Bearer $FLEET_RUNNER_API_KEY"

# Explain hypothetically without a token
curl "http://localhost:8080/api/v1/governance/explain?resource_type=database&resource_name=prod-db&action=read&purpose=fleet_rollout&service=fleet-runner"
```

### 2.5 Authentication Failures

On auth failure the Gateway returns HTTP 401 and records a `gateway_request`
audit event with `outcome_status = "error"` before rejecting the request.
This means every failed authentication attempt is visible in the audit trail:

```bash
# Query all auth failures
curl -s 'http://localhost:1199/v1/events?outcome_status=error' | \
  jq '.[] | {event_id, timestamp, error: .outcome.error_message}'
```

### 2.6 HTTP Authorization (Role Checks)

Authentication (section 2 above) answers "who is this caller?" Authorization answers "is this caller allowed to use this endpoint?"

Authorization is covered in [AUTHZ.md](AUTHZ.md), including:
- The full role reference table (what each role grants)
- How to discover the required role for a given operation (`GET /api/v1/roles`)
- Role aliases (`role_aliases` in `users.yaml`)
- Operating mode blocking (`readonly-governed`)

---

## 3. Data Sensitivity Markings

Sensitivity markings declare what kind of data a resource contains,
independently of its environment tag. A database tagged `[production]` may or
may not contain personal data — those are orthogonal facts.

### 3.1 Sensitivity Classes

| Class | Meaning | Typical resources |
|-------|---------|------------------|
| `public` | No sensitivity restrictions | Internal metrics, status dashboards |
| `internal` | Business data, not personally identifiable | Operational databases, deployment configs |
| `sensitive` | Commercially sensitive or under regulatory scope | Financial, legal, partner data |
| `pii` | Contains personal data (GDPR, CCPA scope) | Customer records, user tables |
| `critical` | High blast-radius or systems-of-record | Primary production databases, core K8s clusters |

Multiple classes are additive. A database can be both `pii` and `critical`.

### 3.2 Declaring Sensitivity in Infra Config

```json
{
  "db_servers": {
    "prod-db": {
      "name": "Production Database",
      "connection_string": "host=prod-db.example.com ...",
      "tags": ["production"],
      "sensitivity": ["pii", "critical"]
    },
    "analytics-db": {
      "name": "Analytics Read Replica",
      "connection_string": "...",
      "tags": ["production"],
      "sensitivity": ["internal"]
    }
  },
  "k8s_clusters": {
    "prod-cluster": {
      "context": "prod",
      "tags": ["production"],
      "sensitivity": ["critical"]
    }
  }
}
```

### 3.3 Using Sensitivity in Policy

```yaml
resources:
  - type: database
    match:
      sensitivity: [pii]          # any database containing personal data

  - type: database
    match:
      tags: [production]
      sensitivity: [critical]     # production AND critical (both must match)
```

Full example — tighter controls on PII databases:

```yaml
- name: pii-data-protection
  priority: 110

  resources:
    - type: database
      match:
        sensitivity: [pii]

  rules:
    - action: read
      effect: allow
      conditions:
        allowed_purposes: [diagnostic, remediation, compliance]

    - action: write
      effect: allow
      conditions:
        require_approval: true
        allowed_purposes: [remediation]
        approval_quorum: 2

    - action: destructive
      effect: deny
      message: "Destructive operations on PII databases require explicit DBA override policy"
```

---

## 4. Purpose-Based Access

Purpose answers "why is this access happening?" It makes identical
role + resource combinations distinguishable by intent.

### 4.1 Purpose Vocabulary

| Purpose | Meaning | Typical operations |
|---------|---------|-------------------|
| `diagnostic` | Read-only investigation | Queries, pod inspection, log reads |
| `remediation` | Fixing an active problem | Cancel query, restart pod, scale deployment |
| `maintenance` | Planned change during a maintenance window | Any write or destructive operation |
| `compliance` | Compliance or audit-driven read of sensitive data | Sensitive reads needing extra traceability |
| `emergency` | Break-glass override for on-call response | Any operation — subject to post-hoc review |
| `fleet_rollout` | Automated change applied by fleet-runner across a fleet | Any tool call executed as part of a fleet job |

`fleet_rollout` is set automatically by fleet-runner via the
`HELPDESK_SESSION_PURPOSE=fleet_rollout` environment variable. Policy rules
governing fleet activity should match on this purpose. Note that
`fleet_rollout` is a non-interactive purpose — there is no human operator
present to respond to a policy denial in real time; fleet-runner will record
the failure and move to the next server. PII/critical databases that require
`[diagnostic, remediation, compliance]` will block fleet jobs unless
`fleet_rollout` is added to their `allowed_purposes`, which is a deliberate
policy decision.

### 4.2 How Purpose Is Declared

**Implicit (default):** derived from the operating mode when not declared:

| Operating mode | Default purpose |
|---------------|----------------|
| `readonly` | `diagnostic` |
| `fix` | `remediation` |

**Explicit via request body:**

```json
{
  "agent": "database",
  "message": "Cancel the blocking query on prod-db",
  "purpose": "remediation",
  "purpose_note": "Blocking analytics jobs — incident INC-2891"
}
```

**Explicit via header:**

```
X-Purpose: remediation
X-Purpose-Note: INC-2891 blocker removal
```

### 4.3 Purpose Conditions in Policy Rules

```yaml
conditions:
  allowed_purposes: [remediation, maintenance]   # deny if purpose not in list
  blocked_purposes: [data_export]                # deny if purpose is in list
```

If `allowed_purposes` is omitted, all purposes are permitted (backwards-compatible
default). `blocked_purposes` can harden a rule regardless of other conditions.

### 4.4 Emergency Purpose (Break-Glass)

`emergency` can override restrictive policies but never bypasses the audit trail
and always requires approval — controlled override, not invisible override:

```yaml
- name: emergency-break-glass
  priority: 200    # evaluated first

  principals:
    - role: oncall

  resources:
    - type: database
    - type: kubernetes

  rules:
    - action: [read, write, destructive]
      effect: allow
      conditions:
        allowed_purposes: [emergency]
        require_approval: true
        approval_quorum: 1
      message: "Emergency access granted. All actions audited with elevated severity."
```

### 4.5 Requiring Explicit Purpose for Sensitive Resources

When `HELPDESK_REQUIRE_PURPOSE_FOR_SENSITIVE=true` is set on an agent, any tool
call against a resource with `pii` or `critical` sensitivity is denied unless
the caller explicitly declared a purpose (via `X-Purpose` header or `purpose`
in the request body). Purposes derived from the operating mode (the default
`diagnostic` in `readonly` mode, `remediation` in `fix` mode) do not satisfy
this requirement.

```
POST /api/v1/query
  purpose: not declared → derived as "diagnostic" from readonly mode
  agent calls: list_tables on prod-db (sensitivity: [pii, critical])
  HELPDESK_REQUIRE_PURPOSE_FOR_SENSITIVE=true on the database agent
    → denied: "access to database/prod-db requires an explicit purpose declaration
               (sensitivity: pii,critical, current purpose "diagnostic" was derived
               from operating mode, not declared); add 'purpose' to your request
               body or X-Purpose header"
```

This is enforced at the agent level (in the policy enforcer, before policy
evaluation) rather than at the gateway, because the gateway does not know the
resource sensitivity for NL queries. Set the env var on each agent that should
enforce it. It is `false` by default so existing callers are not broken.

---

## 5. Principal Propagation

The resolved principal flows from the Gateway through every downstream layer
without requiring re-authentication at each hop.

```
User / Service Account
      │ Authorization: Bearer <api-key>       (static or jwt mode)
      │ X-User: alice@example.com             (none mode — unverified)
      │ X-Purpose: remediation
      ▼
┌───────────────────────────────────────────────────────────┐
│  Gateway                                                  │
│  IdentityProvider.Resolve(r)                              │
│  → ResolvedPrincipal{                                     │
│      UserID:     "alice@example.com",                     │
│      Roles:      ["dba", "sre"],                          │
│      AuthMethod: "jwt",                                   │
│    }                                                      │
└──────────────────────────┬────────────────────────────────┘
                           │ A2A message metadata
                           │ { "trace_id":    "tr_...",
                           │   "user_id":     "alice@example.com",
                           │   "roles":       ["dba", "sre"],
                           │   "auth_method": "jwt",
                           │   "purpose":     "remediation",
                           │   "purpose_note":"INC-2891" }
                           ▼
┌───────────────────────────────────────────────────────────┐
│  Orchestrator (if present)                                │
│  Reads principal from incoming A2A metadata               │
│  Forwards it in outgoing A2A calls to sub-agents          │
└──────────────────────────┬────────────────────────────────┘
                           │ same A2A metadata forwarded downstream
                           ▼
┌───────────────────────────────────────────────────────────┐
│  DB Agent / K8s Agent                                     │
│  agentutil.PolicyEnforcer.CheckTool(ctx, ...)             │
│  → reads TraceContext from ctx                            │
│  → policy.Request{                                        │
│      Principal: {UserID, Roles, Service},                 │
│      Resource:  {Type, Name, Tags, Sensitivity},          │
│      Action:    ActionWrite,                              │
│      Context:   {Purpose, PurposeNote, ...},              │
│    }                                                      │
└──────────────────────────┬────────────────────────────────┘
                           │
                           ▼
┌───────────────────────────────────────────────────────────┐
│  policy_decision audit event includes:                    │
│    user_id, roles, auth_method                            │
│    purpose, purpose_note                                  │
│    sensitivity classes of the resource                    │
└───────────────────────────────────────────────────────────┘
```

### 5.1 A2A Metadata Keys

| Key | Type | Description |
|-----|------|-------------|
| `trace_id` | string | Existing — unchanged |
| `user_id` | string | Resolved user ID |
| `roles` | `[]string` | Resolved role list |
| `service` | string | Set only for service accounts |
| `auth_method` | string | `"api_key"`, `"jwt"`, `"header"`, `"static"` |
| `purpose` | string | Declared or derived purpose |
| `purpose_note` | string | Optional free-text note |

Unknown keys are ignored — forwards compatible.

### 5.2 Service Account vs. Human Caller Identity

Requests carry two distinct identities simultaneously:

| Identity | Who | Resolved from |
|----------|-----|--------------|
| **Human caller** | The person who initiated the request | Authentication header at Gateway |
| **Executing agent** | The agent service running the tool | Service account name set at agent startup |

Policy rules can target either. Most rules target the human caller. The agent's
service account (`Principal.Service`) is used for service-level restrictions
(e.g. caps on automated writes).

**Fleet-runner is the caller.** When `fleet-runner` submits a job step, there
is no human in the loop — the `Principal` is fleet-runner's service account
(`Principal.Service = "fleet-runner"`), not a human user. The audit trail for
every tool call within a fleet job carries:

```json
{
  "principal": { "service": "fleet-runner", "roles": ["sre-automation"], "auth_method": "api_key" },
  "purpose": "fleet_rollout",
  "purpose_note": "job_id=flj_4dd009b7 server=prod-db-1 stage=wave-1"
}
```

Policy rules intended to govern fleet automation should explicitly include
`fleet_rollout` in `allowed_purposes` — it is not in the standard purpose
vocabulary that most existing rules allow. See §4.1 for the full purpose table.

---

## 6. Policy Engine Extensions

### 6.1 Sensitivity Matching in Resource Rules

`ResourceMatch.Sensitivity` targets resources by what data they contain:

```go
type ResourceMatch struct {
    Name        string   `yaml:"name,omitempty"`
    Tags        []string `yaml:"tags,omitempty"`
    Sensitivity []string `yaml:"sensitivity,omitempty"` // AND semantics
}
```

Empty `Sensitivity` matches any resource (backwards-compatible default).

### 6.2 Purpose Conditions

```go
type Conditions struct {
    // ... existing conditions ...
    AllowedPurposes []string `yaml:"allowed_purposes,omitempty"`
    BlockedPurposes []string `yaml:"blocked_purposes,omitempty"`
}
```

Purpose mismatches produce a `ConditionTrace` entry for explainability.

### 6.3 Request Extensions

```go
type RequestContext struct {
    Purpose     string `json:"purpose,omitempty"`
    PurposeNote string `json:"purpose_note,omitempty"`
    // ... existing fields ...
}

type RequestResource struct {
    Type        string   `json:"type"`
    Name        string   `json:"name"`
    Tags        []string `json:"tags,omitempty"`
    Sensitivity []string `json:"sensitivity,omitempty"`
}
```

---

## 7. Audit Trail Integration

Identity and purpose fields appear on every relevant event type:

**`gateway_request` events** (top-level `Event` fields):

```json
{
  "event_type": "gateway_request",
  "principal": {
    "user_id": "alice@example.com",
    "roles": ["dba", "sre"],
    "auth_method": "jwt"
  },
  "purpose": "remediation",
  "purpose_note": "INC-2891",
  "session": { "id": "sess_abc123", "user_id": "alice@example.com" }
}
```

**`policy_decision` events** (`policy_decision` sub-object):

```json
{
  "event_type": "policy_decision",
  "policy_decision": {
    "user_id": "alice@example.com",
    "roles": ["dba", "sre"],
    "auth_method": "jwt",
    "purpose": "remediation",
    "sensitivity": ["pii", "critical"],
    "effect": "allow"
  }
}
```

### 7.1 Querying by Identity and Outcome

```bash
# All auth failures (wrong key, invalid token, unknown user)
curl -s 'http://localhost:1199/v1/events?outcome_status=error' | jq .

# All policy denials
curl -s 'http://localhost:1199/v1/events?event_type=policy_decision&outcome_status=denied' | jq .

# Journeys by user
curl -s 'http://localhost:1199/v1/journeys?user=alice@example.com' | jq .

# Journeys by purpose
curl -s 'http://localhost:1199/v1/journeys?purpose=emergency' | jq .

# Policy decisions filtered by effect (client-side)
go run ./cmd/govexplain --auditd http://localhost:1199 --list --effect deny --since 24h
```

---

## 8. Compliance Reporting

[`govbot`](AIGOVERNANCE.md#8-compliance-reporting-cmdgovbot) includes two identity-focused phases:

**Phase 10 — Identity Coverage**

```
Phase 10 — Identity Coverage
  Identity provider: static
  Requests with resolved principal:   847 / 851  (99.5%)
  Requests with anonymous principal:    4 / 851   (0.5%)  ← WARN if > 0 in static/jwt mode
  Policy decisions with role match:   712 / 847  (84.0%)
  Policy decisions with empty roles:  135 / 847  (15.9%)  ← identifies misconfigured users
```

**Phase 11 — Purpose Coverage**

```
Phase 11 — Purpose Coverage
  Requests with explicit purpose:     623 / 847  (73.6%)
  Requests with implicit purpose:     224 / 847  (26.4%)
  Emergency-purpose requests:           3 / 847   (0.4%)  ← ALERT if not reviewed
  Purpose breakdown:
    diagnostic:   401 (47.3%)
    remediation:  382 (45.1%)
    maintenance:   58  (6.8%)
    emergency:      3  (0.4%)
```

---

## 9. govexplain Integration

[`govexplain`](AIGOVERNANCE.md#9-explainability) is also Identity & Access aware:

```bash
# Would alice (as dba) be allowed to write to prod-db for remediation?
govexplain \
  --gateway http://localhost:8080 \
  --resource database:prod-db \
  --action write \
  --user alice@example.com \
  --roles dba,sre \
  --purpose remediation

# List recent denials
govexplain --auditd http://localhost:1199 --list --effect deny --since 24h

# List all policy decisions with tabular output
govexplain --auditd http://localhost:1199 --list --table
```

---

## 10. Configuration Reference

```bash
# Identity provider (default: "none")
HELPDESK_IDENTITY_PROVIDER=none     # or "static" or "jwt"

# Static provider
HELPDESK_USERS_FILE=/etc/helpdesk/users.yaml

# JWT provider
HELPDESK_JWT_JWKS_URL=https://idp.example.com/.well-known/jwks.json
HELPDESK_JWT_ISSUER=https://idp.example.com/
HELPDESK_JWT_ROLES_CLAIM=groups      # JWT claim containing role list
HELPDESK_JWT_AUDIENCE=helpdesk       # optional: validate aud claim
HELPDESK_JWT_CACHE_TTL=5m            # JWKS key cache TTL (0 = no cache)

# Purpose enforcement (set on each agent, not the gateway)
HELPDESK_REQUIRE_PURPOSE_FOR_SENSITIVE=false  # deny pii/critical access without explicit purpose
```

---

## 11. Security Considerations

**Principal spoofing in `none` mode:** The `X-User` header is accepted without
validation. Anyone with network access to the Gateway can claim any identity.
Upgrading to `static` or `jwt` closes this gap.

**Gateway as AuthN boundary:** The Gateway is the only component that performs
authentication. Agent ports should be firewalled from external access — only
the Gateway port (`:8080` by default) should be externally reachable.

**Purpose integrity:** Purpose is declared by the caller and cannot be
cryptographically verified. The enforcement mechanism is the audit trail — every
declaration is recorded and misuse is retrospectively detectable via govbot.
High-risk purposes (`emergency`) additionally require approval.

**API key storage:** Service account API keys are stored only as Argon2id
hashes in `users.yaml`. The plaintext key is generated once and given to the
service; the system never stores or logs it. Parameters: m=65536, t=3, p=4,
32-byte output.

**JWT JWKS caching:** Cached JWKS keys create a window where a revoked key
remains valid. The default TTL of 5 minutes is a reasonable enterprise
trade-off. Set `HELPDESK_JWT_CACHE_TTL=0` to disable caching for environments
with aggressive key rotation.

**Agent impersonation:** An agent cannot claim a human principal — it can only
forward the principal it received from the Gateway via `TraceContext`. Agents
have their own service identity (`Principal.Service`) set at startup from
config, not from incoming requests.

**Backwards compatibility:** All changes are additive. Setting
`HELPDESK_IDENTITY_PROVIDER=none` (the default) preserves pre-existing
behaviour exactly. Policy rules with `principals:` that previously matched all
callers (because principal was always empty) will now correctly match only the
specified roles in `static` or `jwt` mode. Operators upgrading should audit
their policies to ensure role-restricted rules have the intended scope.

---

## 12. Implementation Map

| Component | Location |
|-----------|----------|
| `identity.Provider` interface, `ResolvedPrincipal`, `NoAuthProvider`, `StaticProvider`, `JWTProvider` | `internal/identity/` |
| `users.yaml` config types and loader | `internal/identity/config.go` |
| `HashAPIKey` / `VerifyArgon2id` | `internal/identity/static.go` |
| `hashapikey` CLI | `cmd/hashapikey/` |
| `jwttest` dev helper | `cmd/jwttest/` |
| Sensitivity + purpose on `ResourceMatch`, `Conditions`, `RequestResource`, `RequestContext` | `internal/policy/types.go` |
| Sensitivity matching, purpose evaluation in policy engine | `internal/policy/engine.go` |
| `Sensitivity []string` on `DBServer`, `K8sCluster` | `internal/infra/infra.go` |
| `TraceContext.Principal`, `Purpose`, `PurposeNote`; `PrincipalFromContext`, `PurposeFromContext` | `internal/audit/trace.go` |
| A2A metadata parsing (user_id, roles, auth_method, purpose) | `internal/audit/trace_middleware.go` |
| Principal + purpose propagation in outgoing A2A calls | `internal/audit/delegate_tool.go` |
| `PolicyDecision.UserID/Roles/AuthMethod/Purpose/Sensitivity` | `internal/audit/event.go` |
| `Event.Principal`, `Event.Purpose`, `Event.PurposeNote` (top-level) | `internal/audit/event.go` |
| `QueryOptions.OutcomeStatus` filter | `internal/audit/store.go` |
| `PurposeExplicit bool` in `TraceContext`; `PurposeExplicitFromContext` helper; `purpose_explicit` in A2A metadata | `internal/audit/trace.go`, `internal/audit/trace_middleware.go`, `cmd/gateway/gateway.go` |
| `sensitivity []string` on `CheckTool`/`CheckDatabase`/`CheckKubernetes`; `RequirePurposeForSensitive` pre-check | `agentutil/agentutil.go` |
| `databaseInfo.Sensitivity` populated from infra config | `agents/database/tools.go` |
| `HELPDESK_REQUIRE_PURPOSE_FOR_SENSITIVE` env var | `agents/database/main.go`, `agents/k8s/main.go` |
| Gateway: identity provider init, principal resolution, 401 on failure, audit on failure | `cmd/gateway/gateway.go`, `cmd/gateway/main.go` |
| Gateway: `handleGovernanceExplain` injects resolved principal into explain query | `cmd/gateway/gateway.go` |
| auditd: `?outcome_status=` query param, purpose param in journeys | `cmd/auditd/main.go` |
| auditd: `?service=` query param on `handleExplain`; `RequestPrincipal.Service` | `cmd/auditd/governance_handlers.go` |
| auditd: governance check with principal + sensitivity | `cmd/auditd/governance_handlers.go` |
| fleet-runner: `HELPDESK_SESSION_PURPOSE=fleet_rollout`; Bearer token auth via `FLEET_RUNNER_API_KEY` | `cmd/fleet-runner/runner.go`, `deploy/docker-compose/docker-compose.yaml` |
| fleet-runner: `fleet-runner` service account in `users.yaml` | `users.example.yaml` |
| govexplain: `--user`, `--role`, `--purpose`, `--sensitivity`, `--effect` flags | `cmd/govexplain/main.go` |
| govbot: Phase 10 (identity coverage), Phase 11 (purpose coverage) | `cmd/govbot/main.go` |
