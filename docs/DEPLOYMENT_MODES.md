# aiHelpDesk Deployment Modes

aiHelpDesk is designed to be useful in three distinct operating contexts. The same
codebase serves all three, but the configuration surface, the operational requirements, and
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

## Mode 2 — Enterprise / IT-Hosted (Read-Only Governed)

**The scenario:** IT or Platform Engineering has deployed a central aiHelpDesk instance
with the full governance stack — audit, policy, compliance, pre-registered infrastructure —
but is not yet ready to permit write or destructive operations. This is a common
onboarding posture: get observability and audit coverage first, evaluate the system in
production, then graduate to `fix` mode once trust is established.

**Who runs it:** Same as Mode 3 — a central IT or Platform Engineering or DBA team.

**Connection model:** Pre-registered via `infra.json`. Ad-hoc connection strings work
for read-only diagnostics but mutation tools are blocked regardless.

**What is enabled:** Everything in Personal mode, plus:
- Policy engine — per-tool rules, blast-radius caps, schedule gates (for read tools)
- Role-based authorization — same roles as Mode 3
- Audit log — tamper-proof, hash-chained record of every action
- Fleet management — read-only visibility (job status, server steps, approval records)
- Govbot — periodic compliance snapshots and policy summaries
- Secbot — real-time alert monitoring with AI-assisted triage

**What is not enabled:** Mutation tools are unconditionally blocked in code — not just
by policy. An operator cannot accidentally unlock them with a permissive policy rule.
Approval workflows are not configured (there is nothing to approve).

**Operating mode:** `HELPDESK_OPERATING_MODE=readonly-governed`. Agents enforce the same
startup governance checks as `fix` mode and exit if audit or policy is not configured.

**Minimum required config:**

```bash
HELPDESK_OPERATING_MODE=readonly-governed
HELPDESK_AUDIT_ENABLED=true
HELPDESK_AUDIT_URL=http://auditd:8089
HELPDESK_POLICY_ENABLED=true
HELPDESK_POLICY_FILE=/etc/helpdesk/policies.yaml
HELPDESK_INFRA_CONFIG=/etc/helpdesk/infra.json
```

**Host deployment shortcut:**

```bash
./startall.sh --readonly-governed --services-only
```

**Typical users:** Same as Mode 3. This mode is typically temporary — the team is
evaluating aiHelpDesk in their production environment before enabling mutations.

---

## Mode 3 — Enterprise / IT-Hosted (Full Governed)

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

| Feature | Personal (Mode 1) | Enterprise R/O (Mode 2) | Enterprise Full (Mode 3) |
|---|---|---|---|
| `HELPDESK_OPERATING_MODE` | unset | `readonly-governed` | `fix` |
| Diagnostic (read-only) tools | ✅ | ✅ | ✅ |
| Ad-hoc connection strings | ✅ | read-only only | read-only only |
| Pre-registered infra targets | optional | required | required |
| Mutation tools | ❌ | ❌ (blocked in code) | ✅ (with governance) |
| Audit log | ❌ | ✅ required | ✅ required |
| Policy engine | ❌ | ✅ required | ✅ required |
| Approval workflows | ❌ | ❌ (no mutations) | ✅ |
| Role-based authz | ❌ | ✅ | ✅ |
| Fleet management | ❌ | read-only | ✅ |
| Govbot / compliance reports | ❌ | ✅ | ✅ |
| Secbot / alert monitoring | ❌ | ✅ | ✅ |
| Startup validation (fail-fast) | ❌ | ✅ | ✅ |

---

## Mixing Modes Is Not Supported

There is no "partial governance" configuration. Both `readonly-governed` and `fix`
trigger startup validation that refuses to proceed unless audit, policy, and audit URL
are all present and correctly configured.

This is intentional. A partially-governed deployment — one where mutations are enabled
but audit logging is not, or where policy is loaded but approvals are bypassed — creates
the illusion of safety without the substance. The operating mode flag is the sharp line
that separates "self-service diagnostic assistant" from "governed platform".

The path between modes is linear and deliberate: Personal → readonly-governed → fix.
Each step requires explicit configuration and passes through the same startup
validation gate.

---

## Choosing a Mode

If you are trying aiHelpDesk for the first time, or running it for personal use:
start with Personal mode. There is nothing to configure beyond an API key and agent
URLs.

If you are deploying aiHelpDesk for a team or organization: start with Mode 2
(`readonly-governed`) using the Host/VM, Docker Compose, or Helm deployment. This gives
you the full governance stack — audit, policy, compliance, fleet visibility — while
keeping mutations off. Evaluate it in production, build confidence, then graduate to
Mode 3 (`fix`) when ready. Read [AIGOVERNANCE.md](AIGOVERNANCE.md) for the full
governance architecture, and [IDENTITY.md](IDENTITY.md) for setting up users and roles.

Do not enable a governed mode on a personal deployment unless you intend to run the
full governance stack. The agents will refuse to start without it.
