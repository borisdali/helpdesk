
<p align="center">
  <a href="https://medium.com/@borisdali/your-sre-on-call-runbook-is-already-obsolete-heres-why-that-s-not-your-fault-0a82b3b0183c">
  <img alt="aiHelpDesk_logo" src="https://github.com/user-attachments/assets/9687ccd4-a2ad-4c85-9466-bcb6c006e8ac" width="40%"/>
  </a>
</p>


# aiHelpDesk: AI-driven incident response and remediation for production database and Kubernetes operations — governed, certified, auditable

[![CI](https://github.com/borisdali/helpdesk/actions/workflows/ci.yml/badge.svg)](https://github.com/borisdali/helpdesk/actions/workflows/ci.yml) [![golangci-lint](https://github.com/borisdali/helpdesk/actions/workflows/golangci-lint.yml/badge.svg)](https://github.com/borisdali/helpdesk/actions/workflows/golangci-lint.yml) [![Release](https://github.com/borisdali/helpdesk/actions/workflows/release.yml/badge.svg)](https://github.com/borisdali/helpdesk/actions/workflows/release.yml) [![Go Version](https://img.shields.io/github/go-mod/go-version/borisdali/helpdesk)](https://github.com/borisdali/helpdesk/blob/main/go.mod) [![codecov](https://codecov.io/gh/borisdali/helpdesk/badge.svg)](https://codecov.io/gh/borisdali/helpdesk) [![Docs](https://img.shields.io/badge/docs-helpdesk-blue)](https://github.com/borisdali/helpdesk/tree/main/docs)

aiHelpDesk is the accountability layer for teams using AI agents on production PostgreSQL and Kubernetes systems. It diagnoses incidents, proposes remediation and fixes your database problems. All under a strict governance framework that ensures every consequential action is approved, audited and certified as consistent.

A _productivity_ agent makes your team faster. An _accountability_ agent certifies that what was done was correct, safe and defensible. aiHelpDesk is the second kind. Exclusively. It does not write migrations, optimize schemas or answer general SQL questions. It is for the moment something goes wrong in production and an AI is about to recommend a write or destructive action. See [who this is for](docs/FOR_WHOM.md).

Three things set aiHelpDesk apart from a general-purpose AI assistant:

**1. Agents that act, not just advise.**
The governed actuation arm — formal [tool registry](docs/TOOL_REGISTRY.md), [fleet runner](docs/FLEET.md), [playbooks](docs/PLAYBOOKS.md), [policy engine](docs/AIGOVERNANCE.md#3-policy-engine), [blast-radius guards](docs/AIGOVERNANCE.md#5-guardrails) — executes remediation steps on your real infrastructure under a tamper-proof [audit trail](docs/AUDIT.md). Every tool call is logged, every [destructive action](docs/MUTATION_TOOLS.md) requires [human approval](docs/INFORMED_CONSENT.md) and the governance framework enforces limits that can't be bypassed at runtime. This is Google's ["Safety Trifecta"](https://pub.towardsai.net/google-just-published-the-blueprint-heres-what-s-already-built-7055588c0ae4) (transparency, real-time risk evaluation, progressive authorization) as a running system, not a design doc.

**2. Institutional memory that compounds.**
Every resolved incident automatically proposes a playbook draft. Every successful `faulttest` remediation auto-saves a draft. Human operators review and activate. The [Vault](docs/VAULT.md), which is aiHelpDesk's library of fault→remedy pairings, grows richer with every [incident](docs/INCIDENTS.md). The next time the same failure occurs, the agent handles it faster and with higher confidence because someone already did the hard thinking.

**3. Stability certs with attribution — not just pass/fail.**
Before a [playbook](docs/PLAYBOOKS.md) enters live rotation it is certified across [three dimensions](docs/ATTRIBUTION_CERTS.md): _outcome_ (did it pass?), _conclusion_ (did the agent reach the same diagnosis every run?) and _evaluation_ (did the judge agree with itself?). `STABLE(7) attr=oom-kill (7/7)` is a different claim from "it passed." It proves the agent doesn't just get the _right answer_. It gets it for the _right reason_, **consistently**.

---
Cloud DBaaS vs. Self-Service: While the cloud vendor's SaaS / DBaaS systems are among the fastest-growing cloud sectors, many customers have legitimate reasons to avoid vendor lock-in and black-box management. 
See [here](https://medium.com/google-cloud/databases-on-k8s-really-part-1-d977510dba0a) for extensive treatment of this topic and the 13 specific customer expectations that the cloud DBaaS providers mostly fail to satisfy.
With [avalanche-like](https://medium.com/google-cloud/your-sre-on-call-runbook-is-already-obsolete-heres-why-that-s-not-your-fault-0a82b3b0183c#5634) AI adoption, we expect the shift towards self-managing databases to only accelerate, pushing the products like aiHelpDesk into the mainstream.

## The Operational SRE/DBA Flywheel

<p align="center">
  <a href="https://medium.com/@borisdali/your-sre-on-call-runbook-is-already-obsolete-heres-why-that-s-not-your-fault-0a82b3b0183c#7fe7">
  <img alt="aiHelpDesk_flywheel" src="https://github.com/user-attachments/assets/68520c1b-188d-4d3f-8bbd-b4c4aed8b950" width="80%"/>
  </a>
</p>

The [Vault](docs/VAULT.md) is the mechanism that closes this loop. It holds every playbook, tracks their effectiveness across runs, flags regressions before they become incidents and proposes updates when a successful incident trace suggests a better approach. See [here](https://medium.com/google-cloud/your-sre-on-call-runbook-is-already-obsolete-heres-why-that-s-not-your-fault-0a82b3b0183c) for the full story.

## Key Capabilities

- **Fault injection testing** — inject 32 known failure modes (SQL-only, SSH, K8s) against your own staging database, score the agent's diagnosis, verify remediation recovery. The `faulttest` tool is self-contained and needs no cluster access to run against external targets.
- **Fleet operations** — coordinate changes across multiple databases with canary phases, approval gates, schema drift detection and full audit trails. Natural language → fleet plan via the Planner.
- **Playbooks** — saved runbooks that combine intent with expert guidance. System playbooks ship with aiHelpDesk; custom playbooks are authored, imported or auto-generated from incident traces.
- **AI Governance** — eight-module framework including tamper-proof audit, blast-radius enforcement, off-hours guards and real-time policy evaluation. Every actuation is governed.
- **Incident diagnostics** — the incident agent collects database, K8s, OS and storage layers into a timestamped support bundle. On resolution, it automatically synthesises a playbook draft from the audit trace.
- **A2A protocol** — built on Google ADK and the Agent-to-Agent protocol. Expert agents (Database, Kubernetes, Sysadmin, Incident, Orchestrator) can be swapped or extended independently.

See [design principles](docs/PRINCIPLES.md), the [FAQ](docs/FAQ.md) and [who is it for](docs/FOR_WHOM.md) before diving in.

---

## Deployment

aiHelpDesk runs on VMs / bare metal (either directly or inside Docker/Podman containers) or on K8s. Binaries are provided for Linux x86-64 and ARM (Graviton, Ampere) and macOS (Intel and Apple Silicon).

### Docker/Podman 

```bash
docker compose -f deploy/docker-compose/docker-compose.yaml up -d
```

See [here](deploy/docker-compose/README.md) for the full instructions.

### Directly on a host/VM

```bash
./startall.sh
```

See [here](deploy/host/README.md) for the full instructions.

### Kubernetes / Helm

```bash
kubectl create secret generic helpdesk-api-key --from-literal=api-key=<YOUR_API_KEY>
helm install helpdesk ./helpdesk-vX.Y.Z-deploy/helm/helpdesk \
  --set model.vendor=anthropic \
  --set model.name=claude-haiku-4-5-20251001
```

See [here](deploy/helm/README.md) for the full instructions.

---

## Architecture

See the primary [Architecture page](docs/ARCHITECTURE.md) for system design, configuration and the extension guide.

## The Vault

See the primary [Vault page](docs/VAULT.md) for how aiHelpDesk accumulates and improves operational knowledge over time: the flywheel concept, how playbook drafts are auto-generated from incident traces, the three paths into the Vault, the review-and-activate workflow and the three core customer workflows (onboarding, acceptance, regression monitoring).

**The Vault as a learning signal** — aiHelpDesk tracks three per-version metrics that answer "is the system actually improving?": step count (is the agent becoming more direct?), recovery time (is it responding faster?) and approach appropriateness (is it fixing problems elegantly or just technically?). A resolution rate tells you whether the system is working. These metrics tell you whether it is getting better. See [here](docs/VAULT_METRICS.md) for the full treatment.

## Stability Certification

Before a playbook enters live rotation it is certified STABLE across three dimensions: outcome (pass rate across N runs), conclusion (attribution consistency — did the agent reach the same diagnosis every time?) and evaluation (judge variance — did the scoring model agree with itself?). A cert like `STABLE(7) attr=oom-kill (7/7)` is auditable proof that the agent doesn't just pass — it reaches the correct conclusion, consistently, for the right reason.

`vault accuracy` shows per-playbook correctness from human operator feedback. `vault calibration` shows whether the system's stated confidence predicts actual accuracy. `vault cert-compare` gates model upgrades by comparing cert depth across two models before promoting. See [CONSISTENCY.md](docs/CONSISTENCY.md) and [ATTRIBUTION_CERTS.md](docs/ATTRIBUTION_CERTS.md) for the full concept and per-platform runbooks.

## AI Governance

aiHelpDesk's Governance system rests on eight subsystems including full [auditing](docs/AUDIT.md), [compliance reporting](docs/COMPLIANCE.md) and the [journeys](docs/JOURNEYS.md). See the primary [AI Governance page](docs/AIGOVERNANCE.md) for the complete reference.

HTTP-level authorization is presented [here](docs/AUTHZ.md). Identity provider configuration (static users, JWT/OIDC, service accounts) is described [here](docs/IDENTITY.md).

## Fleet Operations

See the primary [Fleet Management page](docs/FLEET.md) for how aiHelpDesk coordinates the multi-database operations: job definitions, canary phases, approval gates, schema drift detection, the natural-language Planner and rollback.

## Playbooks

See the primary [Playbook page](docs/PLAYBOOKS.md) for how Playbooks take the central stage in aiHelpDesk. This page presents the Playbook schema, CRUD API, import formats (Markdown, YAML, Ansible, Rundeck), system playbooks and versioning. See [here](docs/PLAYBOOK_OPS.md) for the Playbooks operational best practices.

## Testing

aiHelpDesk's testing strategy is documented in [here](testing/README.md). Two guides cover fault injection testing specifically:

- **[Fault Injection Testing](docs/FAULTTEST.md)** is the customer-facing guide: validate agent behavior against your own staging database using SQL-only injection, SSH-level fault injection and automated remediation verification; no Docker or cluster access required
- **[Internal Fault Injection Harness](testing/FAULT_INJECTION_TESTING.md)** is the engineer-facing guide: Docker-compose test stack, full catalog of 27 failure modes, CI/CD integration

## Gateway REST API

```bash
# Query the system
curl -X POST http://localhost:8080/api/v1/query \
  -H "Content-Type: application/json" \
  -d '{"agent": "database", "message": "What is the server uptime?"}'

# List agents
curl http://localhost:8080/api/v1/agents

# Synthesise a playbook from an incident trace
curl -X POST http://localhost:8080/api/v1/fleet/playbooks/from-trace \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"trace_id": "tr_abc123", "outcome": "resolved"}'
```

See the primary [API page](docs/API.md) for the full REST API reference: all endpoints with request/response shapes, query parameters and `curl` examples.

## Helper Scripts

| Script | Description |
|--------|-------------|
| `scripts/gateway-repl.sh` | Interactive REPL using the Gateway API (recommended for containers) |
| `scripts/k8s-local-repl.sh` | Run orchestrator locally with K8s agents port-forwarded |

See [here](scripts/README.md) for detailed usage.

## Upstream Agents Calling aiHelpDesk

aiHelpDesk agents can be called by humans via the Orchestrator or by upstream agents and programs via the A2A protocol and Gateway API:

- **[SREBot](cmd/srebot/README.md)** is the O11y watcher that calls aiHelpDesk to diagnose a database and ask for AI-powered triage
- **[SecBot](cmd/secbot/README.md)** is the Security responder that fires alerts for governance violations in real-time and creates incident bundles
- **[GovBot](cmd/govbot/README.md)** is the Compliance reporter that queries governance endpoints and posts structured snapshots to Slack on a schedule
- **[Real-Time Auditor](docs/AIGOVERNANCE.md#77-auditor-cli-cmdauditor)** is the Long-running daemon that connects to the audit socket and fires webhook/email/syslog alerts

See [here](docs/INTRO_DIALOG.md) for a sample interactive dialog with a human operator.

## LLM Support

aiHelpDesk is model agnostic. It is built on Google ADK for Go and tested with the Anthropic and Gemini models, but it should work with the frontier LLMs.

From our standpoint, the LLMs are a disposable commodity. Flip from Gemini to Anthropic and aiHelpDesk should continue to give you exactly the same diagnosis and remediation. Anything shorter than that is a P0 bug.

[This blog post](https://medium.com/google-cloud/llms-are-functions-not-brains-aihelpdesk-perspective-e12e5432a9ed) adds some additional context for how AI models are treated at aiHelpDesk.

| Vendor | Model | Notes |
|--------|-------|-------|
| Anthropic | `claude-haiku-4-5-20251001` | Fast, cost-effective |
| Anthropic | `claude-sonnet-4-6` | Balanced performance |
| Anthropic | `claude-opus-4-8` | Most capable |
| Gemini | `gemini-2.5-flash` | Fast, recommended for most use cases |
| Gemini | `gemini-2.5-flash-lite` | Fastest, lowest cost |
| Gemini | `gemini-2.5-pro` | Most capable 2.5 model |
| Gemini | `gemini-3-flash-preview` | Latest 3.0 series, fast |
| Gemini | `gemini-3-pro-preview` | Latest 3.0 series, most capable |

Individual expert agents can run with different LLMs if needed. Support for additional providers can be added by implementing ADK's LLM interface. Contact us at info@aiHelpDesk.biz for specific model support requests.
