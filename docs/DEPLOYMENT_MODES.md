# aiHelpDesk Deployment Modes

aiHelpDesk is designed to be useful in two fundamentally different contexts. The same
codebase serves both, but the configuration surface, the operational requirements, and
the trust model are different enough that it is worth describing them explicitly.

---

## Mode 1 — Personal / Self-Service

**The scenario:** An engineer has a problem right now. They want to run aiHelpDesk
locally, point it at a database or cluster they already have access to, and get answers.
No tickets, no change-control, no compliance officer.

**Who runs it:** The engineer themselves, on their laptop or a personal workstation.

**Connection model:** Ad-hoc. The database connection string is passed inline per query
or per API call. There is no central registry of pre-approved targets. The operator is
also the user.

**What is enabled:** All diagnostic (read-only) tools — connection checks, query
inspection, replication lag, pod status, log tailing, event streams. The full set of
LLM-powered reasoning across database and Kubernetes agents is available.

**What is not enabled:** Mutation tools (`cancel_query`, `terminate_connection`,
`kill_idle_connections`, `delete_pod`, `restart_deployment`, `scale_deployment`, etc.) are
off by default. Audit logging, policy enforcement, approval workflows, identity/role
checks and fleet management are all absent. The last module in this list
is particularly important as we get many questions about it: aiHelpDesk Personal Deployment Mode
doesn't offer a way to run commands across multiple databases.

**Operating mode:** `HELPDESK_OPERATING_MODE` is unset (defaults to `readonly`).

**Minimum required config:**

```bash
HELPDESK_MODEL_VENDOR=anthropic
HELPDESK_API_KEY=sk-ant-...

# Agents + gateway — nothing else is required
./cmd/gateway/gateway
./agents/database/database
./agents/k8s/k8s
```

**Infrastructure config:** Optional. An `infra.json` file can be used to pre-define
frequently-used targets, but ad-hoc connection strings passed in queries work equally
well without it.

**Typical users:** Individual engineers, DBAs doing one-off diagnostics, SREs triaging
an incident from their own machine.

---

## Mode 2 — Enterprise / IT-Hosted

**The scenario:** IT or Platform Engineering or DBA team runs a central aiHelpDesk instance. It
manages a known set of production databases and clusters. Engineers query it to
diagnose problems, but only through the centrally registered targets. Mutations go
through a governed workflow with audit trails, policy checks, blast-radius limits, and
human approval. Fleet jobs (bulk operations across many servers) require explicit
submission and role-based sign-off.

**Who runs it:** A central IT or Platform Engineering or DBA team. Individual engineers are
consumers, not operators.

**Connection model:** Pre-registered. All databases and clusters are defined in a
central `infra.json`. Ad-hoc connection strings are not permitted for mutation tools.
The set of targets is known, tagged, and auditable.

**What is enabled:** Everything in Personal mode, plus:
- Write and destructive mutation tools (behind policy + approval gate)
- Policy engine — per-tool rules, blast-radius caps, schedule gates
- Approval workflows — human review before any write or destructive action executes
- Role-based authorization — `dba`, `fleet-operator`, `fleet-approver`, `admin`, `operator` roles
- Audit log — tamper-proof, hash-chained record of every action
- Fleet management — staged rollout of operations across many targets (canary → waves → circuit breaker)
- Govbot — periodic compliance snapshots and policy summaries
- Secbot — real-time alert monitoring with AI-assisted triage

**Operating mode:** `HELPDESK_OPERATING_MODE=fix`. Agents enforce this at startup —
if the governance stack is not fully configured, the agent exits rather than starting
in a degraded state.

**Minimum required config:**

```bash
HELPDESK_OPERATING_MODE=fix
HELPDESK_AUDIT_ENABLED=true
HELPDESK_AUDIT_URL=http://auditd:8089
HELPDESK_POLICY_ENABLED=true
HELPDESK_POLICY_FILE=/etc/helpdesk/policies.yaml
HELPDESK_INFRA_CONFIG=/etc/helpdesk/infra.json
```

**Identity and access:**

```bash
# On auditd — enables role checks on approval and fleet endpoints
-users-file /etc/helpdesk/users.yaml

# On gateway — enables role checks on fleet submission and planner
HELPDESK_IDENTITY_PROVIDER=static
HELPDESK_USERS_FILE=/etc/helpdesk/users.yaml
```

See [IDENTITY.md](IDENTITY.md) for JWT provider configuration and the `hashapikey`
utility for generating service account credentials.

**Deployment targets:** Host/VM (`deploy/host/`), Docker Compose (`deploy/docker-compose/`),
or Kubernetes Helm chart (`deploy/helm/`). The Helm chart defaults to governance-enabled
configuration.

**Typical users:** Any engineer in the organization, through a UI or the `helpdesk`
CLI. The platform team controls who can do what via `users.yaml` and `policies.yaml`.

---

## Feature Comparison

| Feature | Personal | Enterprise |
|---|---|---|
| Diagnostic (read-only) tools | ✅ | ✅ |
| Ad-hoc connection strings | ✅ | read-only only |
| Pre-registered infra targets | optional | required |
| Mutation tools | ❌ | ✅ (with governance) |
| Audit log | ❌ | ✅ required |
| Policy engine | ❌ | ✅ required |
| Approval workflows | ❌ | ✅ |
| Role-based authz | ❌ | ✅ |
| Fleet management | ❌ | ✅ |
| Govbot / compliance reports | ❌ | ✅ |
| Secbot / alert monitoring | ❌ | ✅ |
| Startup validation (fail-fast) | ❌ | ✅ |

---

## Mixing Modes Is Not Supported

There is no "partial governance" configuration. The governance stack is
all-or-nothing by design: `HELPDESK_OPERATING_MODE=fix` triggers startup validation
that refuses to proceed unless audit, policy, and audit URL are all present and
correctly configured.

This is intentional. A partially-governed deployment — one where mutations are enabled
but audit logging is not, or where policy is loaded but approvals are bypassed — creates
the illusion of safety without the substance. The operating mode flag is the sharp line
that separates "self-service diagnostic assistant" from "governed mutation platform".

---

## Choosing a Mode

If you are trying aiHelpDesk for the first time, or running it for personal use:
start with Personal mode. There is nothing to configure beyond an API key and agent
URLs.

If you are deploying aiHelpDesk for a team or organization: start with the Enterprise
Helm chart or Docker Compose or a Host/VM configuration, which includes governance by default. Read
[AIGOVERNANCE.md](AIGOVERNANCE.md) for the full governance architecture, and
[IDENTITY.md](IDENTITY.md) for setting up users and roles.

Do not enable `HELPDESK_OPERATING_MODE=fix` on a personal deployment unless you
intend to run the full governance stack. The agents will refuse to start without it.
