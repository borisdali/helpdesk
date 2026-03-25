# aiHelpDesk HTTP Authorization

This document covers HTTP-level authorization — who can call which endpoint and with which role. This is distinct from [policy-based governance](AIGOVERNANCE.md3-policy-engine), which determines what an agent is allowed to do once a request is admitted.

**Quick reference:** To find out what role you need to call a specific endpoint, use `GET /api/v1/roles` — the live authorization table the system is currently enforcing. See [§5. Finding Your Role](#5-finding-your-role).

---

## 1. Terminology

These terms appear throughout this document and the codebase. They are sometimes used interchangeably in the industry, so the distinctions below reflect specifically how they are used in aiHelpDesk's context.

| Term | Abbreviation | Meaning in aiHelpDesk |
|---|---|---|
| **Authentication** | AuthN | Verifying *who* the caller is. Handled by `internal/identity/` — resolves a `ResolvedPrincipal` from an API key, JWT, or header. |
| **Authorization** | AuthZ | Verifying *whether* an authenticated caller may use a specific HTTP endpoint. Handled by `internal/authz/` — role-based (RBAC), evaluated at the HTTP boundary before the request reaches any handler. |
| **Policy / governance** | — | Verifying *whether* an admitted request may perform a specific action on a specific resource under specific conditions. Handled by the policy engine inside each agent — attribute-based (ABAC), evaluated with full context (resource tags, sensitivity class, purpose, blast radius, time window). |
| **RBAC** | Role-Based Access Control | The model used by the HTTP authorization layer. Access is determined by the caller's roles (`dba`, `sre`, etc.) matched against a static per-route permission table. |
| **ABAC** | Attribute-Based Access Control | The model used by the policy governance layer. Access is determined by a combination of attributes: principal identity, resource tags, declared purpose, action class, time, and blast radius. |

**Key distinction:** both AuthZ and policy governance can return `403 Forbidden`, but they operate at different levels and are distinguishable:
- AuthZ `403`: role check failed at the HTTP boundary — the request never reached an agent. Audit record has no `tool_name`.
- Policy `403`: the agent evaluated the operation and a governance rule denied it. Audit record has a `tool_name` and full policy denial detail.

See [§3 below](#3-how-enforcement-works) for the per-request flow, and [API.md](API.md#http-status-codes) for status code semantics.

---

## 2. Authorization vs Governance

Building on the terms defined above, the table below summarizes aiHelpDesk's two complementary enforcement layers:

| Layer | What it controls | Where it lives | When it acts |
|---|---|---|---|
| **HTTP authorization** (this doc) | Who can call which endpoint | `internal/authz/` | At the HTTP boundary, before the request reaches the handler |
| **Policy governance** | What the agent may do (tools, resources, blast radius, approvals) | `internal/audit/`, policy engine | Inside the agent, after the request is admitted |

A request rejected at the HTTP layer never reaches the agent. A request admitted at the HTTP layer may still be denied by a governance policy. For example, a DBA calling `POST /api/v1/db/cancel_query` is admitted (has the role), but the policy engine may still block the operation if the target database is out of maintenance window or the blast radius cap is exceeded. In a sense the HTTP endpoint security can be thought of as the coarse-grained AuthZ, while the next layer's policy governance can be perceived as the fine-grained one, evaluated inside the agent with full context (resource tags, sensitivity, purpose, blast radius, time windows).

Both layers are required in Mode 2 and Mode 3 deployments, see [here](DEPLOYMENT_MODES.md) for details.

---

## 3. How Enforcement Works

### 3.1 Activation

HTTP authorization enforcement is activated by setting `HELPDESK_IDENTITY_PROVIDER` to a value other than `none` (or leaving it unset). When the identity provider is `static` or `jwt`, the authorizer runs in enforcing mode on every request. While the `none` mode is technically permitted, we encourage customers to avoid using it beyond simple testing for the Enterprise [Deployment Modes](DEPLOYMENT_MODES.md).

```bash
# Enforcing: every request must carry a valid identity
export HELPDESK_IDENTITY_PROVIDER=static
export HELPDESK_USERS_FILE=/etc/helpdesk/users.yaml

# Non-enforcing (default): no enforcement; backwards-compatible; suitable for testing
export HELPDESK_IDENTITY_PROVIDER=none
```

When enforcement is off, the authorizer accepts all requests regardless of role. This preserves backward compatibility with existing deployments that have no `users.yaml`. See [here](IDENTITY.md) for identity provider configuration.

### 3.2 Per-Request Flow

```
HTTP request
    │
    ▼
Identity resolution (identity.Provider.Resolve)
    ├── X-User header → ResolvedPrincipal (static) or NoAuth (none mode)
    └── Authorization: Bearer <token> → service account or JWT principal
    │
    ▼
Authorization check (authz.Authorize(pattern, principal))
    ├── AllowAnonymous → pass
    ├── principal.IsAnonymous() → 401
    ├── AdminBypass + admin role → pass
    ├── ServiceOnly + human caller → 403
    ├── RequireRoles: any match → pass
    └── RequireRoles: no match → 403
    │
    ▼
Handler — principal available via authz.PrincipalFromContext(ctx)
```

### 3.3 Response Codes

| Code | Meaning in authorization context |
|---|---|
| `401 Unauthorized` | Identity not provided, invalid, or not registered in `users.yaml` |
| `403 Forbidden` | Identity is valid but the caller's roles do not satisfy the route's requirement |

A `403` from the authorization layer is distinct from a `403` from policy governance. The HTTP response body will indicate which layer rejected the request:
- Authorization: `"authentication required"` or `"forbidden"` (from `internal/authz/`)
- Governance: full policy denial detail with rule name and reason (from the policy engine)

### 3.4 Fail-Closed Behaviour

For routes not in the permission table (unknown or future routes):
- Anonymous caller → `401`
- Authenticated caller → pass (the mux handles `404`)

The default therefore fails toward authentication: unknown routes are accessible to authenticated users but not to anonymous callers.

---

## 4. Roles Reference

The following table lists all canonical role names, what each role grants access to, and which deployment modes use the role.

### 4.1 Role Summary

| Role | Who it is for | What it grants |
|---|---|---|
| `dba` | Database administrators | Direct DB tool invocation (`POST /api/v1/db/{tool}`), DB approval actions |
| `sre` | Site reliability engineers | Direct DB and K8s tool invocation |
| `oncall` | On-call engineers | Direct DB and K8s tool invocation |
| `k8s-admin` | Kubernetes administrators | Direct K8s tool invocation (`POST /api/v1/k8s/{tool}`) |
| `sre-automation` | Automation service accounts (srebot, secbot) | DB and K8s tool invocation programmatically |
| `fleet-operator` | Fleet job authors | Submit fleet jobs (`POST /api/v1/fleet/jobs`) |
| `fleet-approver` | Fleet job approvers | Approve/deny fleet approval requests |
| `admin` | Superusers | Bypass all role checks on any route (configurable — see [§6](#6-admin-role)) |

Roles not in this table are valid for policy governance purposes (e.g. `developer`, `security-scanner`) but do not grant any elevated HTTP authorization beyond what any authenticated user has.

### 4.2 Gateway Routes by Access Level

**Public — no authentication required:**

| Route | Description |
|---|---|
| `GET /health` | Liveness probe |
| `GET /api/v1/agents` | List registered agents |
| `GET /api/v1/tools` | List all tools and their schemas |
| `GET /api/v1/tools/{toolName}` | Get a single tool |
| `GET /api/v1/roles` | Live authorization table (this endpoint) |

**Authenticated — any verified non-anonymous user:**

| Route | Description |
|---|---|
| `POST /api/v1/query` | Natural-language query to an agent |
| `POST /api/v1/incidents` | Create incident diagnostic bundle |
| `GET /api/v1/incidents` | List incident bundles |
| `POST /api/v1/research` | Research query |
| `GET /api/v1/infrastructure` | List registered infrastructure |
| `GET /api/v1/databases` | List registered databases |
| `GET /api/v1/governance/*` | All governance read endpoints |
| `POST /api/v1/fleet/plan` | Fleet dry-run planner |
| `GET /api/v1/fleet/jobs/*` | Fleet job read endpoints |

**Role-required:**

| Route | Required role(s) — any one suffices |
|---|---|
| `POST /api/v1/db/{tool}` | `dba`, `sre`, `oncall`, or `sre-automation` |
| `POST /api/v1/k8s/{tool}` | `sre`, `k8s-admin`, `oncall`, or `sre-automation` |
| `POST /api/v1/fleet/jobs` | `fleet-operator` |

### 4.3 auditd Routes by Access Level

auditd enforces the same model. Human-readable (GET) endpoints are open to any authenticated user; write endpoints are service-only (machine-to-machine); approval actions require specific roles.

**Service-only writes** (reject human callers even if authenticated):

| Route | Caller |
|---|---|
| `POST /v1/events` and `POST /v1/events/{eventID}/outcome` | Gateway auditor, agents |
| `POST /v1/approvals` | agents (on policy-required approval) |
| `POST /v1/governance/check` | agents (pre-flight governance) |
| `POST /v1/govbot/runs` | govbot service account |
| `POST /v1/fleet/jobs` and all fleet lifecycle writes | fleet-runner service account |

**Role-required:**

| Route | Required role(s) — any one suffices |
|---|---|
| `POST /v1/approvals/{approvalID}/approve` | `dba` (for DB approvals) or `fleet-approver` (for fleet approvals) |
| `POST /v1/approvals/{approvalID}/deny` | `dba` or `fleet-approver` |
| `POST /v1/approvals/{approvalID}/cancel` | any authenticated user (ownership enforced in handler) |

The middleware gate for approve/deny allows either `dba` or `fleet-approver` through. The handler then narrows the check: a `dba` cannot approve a fleet job and a `fleet-approver` cannot approve a DB action.

---

## 5. Finding Your Role

**The canonical way to discover what role you need for a given operation:**

```bash
curl http://localhost:8080/api/v1/roles | jq .
```

This endpoint returns the live authorization table the running Gateway is enforcing, including any custom role aliases your deployment has configured. It is always up-to-date — the table is compiled directly from `DefaultGatewayPermissions` at startup.

**Sample response:**

```json
{
  "roles": [
    {
      "name": "dba",
      "grants": [
        "POST /api/v1/db/{tool}",
        "POST /v1/approvals/{approvalID}/approve",
        "POST /v1/approvals/{approvalID}/deny"
      ]
    },
    {
      "name": "fleet-operator",
      "grants": ["POST /api/v1/fleet/jobs"]
    },
    {
      "name": "sre",
      "grants": [
        "POST /api/v1/db/{tool}",
        "POST /api/v1/k8s/{tool}"
      ]
    }
  ],
  "admin_role": "admin",
  "aliases": {
    "database-admin": "dba",
    "platform-sre": "sre"
  },
  "enforcing": true
}
```

**Reading the output:**
- `roles[].grants` — the list of routes that role unlocks. If a route requires multiple roles, each role will list that route independently.
- `admin_role` — the role name that bypasses all checks. `admin` by default; can be overridden per deployment (see [§5](#5-admin-role)).
- `aliases` — maps your organization's IdP group names to canonical role names (see [§6](#6-role-aliases)).
- `enforcing` — `true` means authorization is active; `false` means the system is in non-enforcing (development) mode.

**Routes NOT listed under any role** in the `GET /api/v1/roles` response are open to any authenticated user — you only need a valid identity, not a specific role.

**Routes NOT listed anywhere** (neither in `roles` nor implied by authentication) are public — no credentials needed.

---

## 6. Admin Role

The `admin` role bypasses authorization checks on every route in both the Gateway and auditd. It is the escape hatch for operators who need unrestricted access for maintenance or onboarding.

The admin role name defaults to `"admin"` but can be changed at startup via `SetAdminRole` (see `internal/authz/authz.go`). The current configured name is always visible in `GET /api/v1/roles` under the `admin_role` key.

```yaml
# users.yaml — granting admin access
users:
  - id: ops@example.com
    roles: [admin]
```

**Use sparingly.** The admin role makes it impossible for auditd to enforce service-only write restrictions on that principal. aiHelpDesk recommends following the standard security best practices, in particular adhering to the principle of least privilege and granting the minimum role (e.g. `dba`, `fleet-operator` if elevated privileges are needed) for day-to-day operations and reserve `admin` for break-glass scenarios.

---

## 7. Role Aliases

### 7.1 Static provider (`HELPDESK_IDENTITY_PROVIDER=static`)

Your organization may use group names in `users.yaml` that differ from aiHelpDesk's canonical role names. Role aliases let you map those names to canonical roles without renaming every user entry.

Declare aliases in `users.yaml`:

```yaml
# /etc/helpdesk/users.yaml
role_aliases:
  database-admin: dba          # "database-admin" → canonical role "dba"
  platform-sre: sre            # "platform-sre" → canonical role "sre"
  sre-ops: sre                 # multiple aliases can map to the same canonical role
  k8s-platform: k8s-admin
  deploy-engineer: fleet-operator
  deploy-approver: fleet-approver

users:
  - id: alice@example.com
    roles: [database-admin]    # expanded to "dba" at resolve time
  - id: bob@example.com
    roles: [platform-sre]      # expanded to "sre"
```

Aliases are resolved by `StaticProvider` at identity resolution time — by the time a principal reaches the authorizer, only canonical role names are present. The raw alias names are never stored in the principal or in the audit log.

The alias map from the currently loaded `users.yaml` is surfaced via `GET /api/v1/roles` under the `aliases` key, so operators can inspect the effective mapping without reading the config file.

**Alias rules:**
- Aliases may only point to canonical role names — you cannot chain aliases.
- An alias key that matches a canonical role name is a no-op (resolved to itself).
- A role name that is not a canonical name and has no alias entry is passed through unchanged (it can still be used by policy rules; it just won't match any HTTP authorization table entry).
- Aliases apply to both `users` and `service_accounts` entries.

### 7.2 JWT provider (`HELPDESK_IDENTITY_PROVIDER=jwt`)

Role aliases in `users.yaml` have no effect when using the JWT provider. The JWT provider extracts roles directly from the token claim (configured via `HELPDESK_JWT_ROLES_CLAIM`, default: `groups`) and passes them to the authorizer verbatim — there is no alias expansion step.

The recommended approach for JWT deployments is to emit canonical role names from the IdP:

- **Okta / Auth0 / Azure AD:** use group-to-claim transformation rules to map your internal group names to canonical names (`dba`, `sre`, etc.) in the `groups` claim, or add a dedicated `helpdesk_roles` claim and point `HELPDESK_JWT_ROLES_CLAIM` at it.
- **Keycloak:** use client-scope mappers to rename or filter roles before they reach the token.
- **Kubernetes workload identity:** configure the projected claim to include the correct role strings.

If changing the IdP is not feasible in the short term, the cleanest workaround is to set `HELPDESK_JWT_ROLES_CLAIM` to a claim that already uses canonical names, and populate that claim via an IdP transformation rule.

> **Note:** The `aliases` field in `GET /api/v1/roles` will be empty when the JWT provider is active, because the alias map lives in `users.yaml` which is not loaded in JWT mode. The authorizer enforces canonical names only.

---

## 8. Operating Mode and Authorization

`HELPDESK_OPERATING_MODE` is a second, independent enforcement layer that sits inside the Gateway handlers (not in `internal/authz/`). It blocks write and destructive tool invocations regardless of the caller's role.

| Mode | DB/K8s write tools | DB/K8s destructive tools |
|---|---|---|
| unset (default) | allowed | allowed |
| `fix` | allowed | allowed |
| `readonly-governed` | **blocked (403)** | **blocked (403)** |

In `readonly-governed` mode, even a caller with the `dba` role will receive `403` when attempting a write or destructive tool. The authorization check passes (correct role), but the operating mode gate rejects the request before the agent is contacted. This is intentional — `readonly-governed` is the "evaluate in production safely" posture.

Operating mode is checked after authorization. A caller without the required role will still receive `403` from the authorization layer; the operating mode gate is not reached.

---

## 9. Service Accounts and the `ServiceOnly` Constraint

Certain auditd write endpoints are marked `ServiceOnly`. A human caller — even one with the `admin` role, unless `AdminBypass` applies — is rejected with `403` on these endpoints.

`ServiceOnly` is determined by the `Service` field of `ResolvedPrincipal`:
- Service accounts (authenticated via `Authorization: Bearer <api-key>`) have `Service != ""`.
- Human users (authenticated via `X-User` header) have `Service == ""`.

This prevents human operators from accidentally writing audit events directly to auditd, which would undermine the tamper-proof audit chain. The only legitimate callers for these endpoints are the Gateway's internal auditor and agent service accounts.

Service account configuration is covered in [IDENTITY.md §2.2](IDENTITY.md#22-static-identity-provider).

---

## 10. Implementation Reference

For engineers working on the authorization code:

| Component | File | Purpose |
|---|---|---|
| `Permission`, `Authorizer`, `Authorize`, `Require` | `internal/authz/authz.go` | Core authorization logic and context helpers |
| `Middleware` | `internal/authz/middleware.go` | http.Handler wrapper (for tests; production uses per-route closures) |
| Gateway permission table | `internal/authz/gateway_routes.go` | `DefaultGatewayPermissions` — 30 entries |
| auditd permission table | `internal/authz/auditd_routes.go` | `DefaultAuditdPermissions` — 37 entries |
| Gateway route wiring | `cmd/gateway/gateway.go` `RegisterRoutes` | `auth(pattern, h)` closure applied to every route |
| auditd route wiring | `cmd/auditd/main.go` | same `auth(pattern, h)` pattern |
| Approval fine-grained check | `cmd/auditd/approval_handlers.go` | `authzr.Require(principal, required)` after middleware gate |
| Role alias expansion | `internal/identity/static.go` `expandRoles` | applied at resolve time in `StaticProvider` |

**Completeness tests** in `internal/authz/authz_test.go` verify that every route registered in `RegisterRoutes` (Gateway) and `main.go` (auditd) has a corresponding entry in the permission table, and vice versa. These tests run as part of `go test ./...` and will fail if a new route is added without a permission table entry.

**Important architectural note:** `r.Pattern` in Go 1.22 `http.ServeMux` is only set after the mux has dispatched to the matched handler — it is empty in any outer middleware wrapper. For this reason, authorization uses per-route closures (capturing the pattern string at registration time) rather than a single outer middleware wrapping the mux. See the `IMPORTANT` note in `internal/authz/middleware.go` for details.
