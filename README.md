
<p align="center">
  <a href="https://medium.com/google-cloud/databases-on-k8s-really-part-1-d977510dba0a">
  <img alt="aiHelpDesk_logo" src="https://github.com/user-attachments/assets/9687ccd4-a2ad-4c85-9466-bcb6c006e8ac" width="40%"/>
  </a>
</p>


# aiHelpDesk: Agentic AI DB SRE support system that learns from every incident

aiHelpDesk is a Go-based AI multi-agent support system for diagnosing and remediating PostgreSQL (and PotsgreSQL derivative databases, like AlloyDB Omni) issues on Kubernetes and VMs. aiHelpDesk links frontier model reasoning to your specific environment — your databases, your tool catalog, your operational history — and couples it with a strictly governed execution arm that actually fixes problems, not just explains them.

Two things set aiHelpDesk apart from a general-purpose AI assistant:

**1. Agents that act, not just advise.**
The governed actuation arm — formal tool registry, fleet runner, playbooks, policy engine, blast-radius guards — executes remediation steps on your real infrastructure under a tamper-proof audit trail. Every tool call is logged, every destructive action requires human approval, and the governance framework enforces limits that can't be bypassed at runtime.

**2. Institutional memory that compounds.**
Every resolved incident automatically proposes a playbook draft. Every successful faulttest remediation auto-saves a draft. Human operators review and activate. The [Vault](docs/VAULT.md) — aiHelpDesk's library of fault→remedy pairings — grows richer with every incident. The next time the same failure occurs, the agent handles it faster and with higher confidence because someone already did the hard thinking.

## The Operational SRE/DBA Flywheel

<p align="center">
  <a href="https://medium.com/google-cloud/databases-on-k8s-really-part-1-d977510dba0a">
  <img alt="aiHelpDesk_flywheel" src="https://github.com/user-attachments/assets/68520c1b-188d-4d3f-8bbd-b4c4aed8b950" width="80%"/>
  </a>
</p>

The [Vault](docs/VAULT.md) is the mechanism that closes this loop. It holds every playbook, tracks their effectiveness across runs, flags regressions before they become incidents, and proposes updates when a successful incident trace suggests a better approach. See [here](https://medium.com/google-cloud/your-sre-on-call-runbook-is-already-obsolete-heres-why-that-s-not-your-fault-0a82b3b0183c) for the full story.

## Key Capabilities

- **Fault injection testing** — inject 27 known failure modes (SQL-only, SSH, K8s) against your own staging database, score the agent's diagnosis, verify remediation recovery. The `faulttest` tool is self-contained and needs no cluster access to run against external targets.
- **Fleet operations** — coordinate changes across multiple databases with canary phases, approval gates, schema drift detection, and full audit trails. Natural language → fleet plan via the Planner.
- **Playbooks** — saved runbooks that combine intent with expert guidance. System playbooks ship with aiHelpDesk; custom playbooks are authored, imported, or auto-generated from incident traces.
- **AI Governance** — eight-module framework including tamper-proof audit, blast-radius enforcement, off-hours guards, and real-time policy evaluation. Every actuation is governed.
- **Incident diagnostics** — the incident agent collects database, K8s, OS, and storage layers into a timestamped support bundle. On resolution, it automatically synthesises a playbook draft from the audit trace.
- **A2A protocol** — built on Google ADK and the Agent-to-Agent protocol. Expert agents (Database, Kubernetes, Sysadmin, Incident, Orchestrator) can be swapped or extended independently.

## AI-Assisted Database Management

While SaaS DBaaS systems are among the fastest-growing cloud sectors, many customers have legitimate reasons to avoid vendor lock-in and black-box management. See [here](https://medium.com/google-cloud/databases-on-k8s-really-part-1-d977510dba0a) for extensive treatment of this topic and, in particular, check out the 13 specific customer expectations of the cloud provider's DBaaS and how the actual cloud offerings mostly fall short them.

Enter the world of AI-Assisted Database Management products.

aiHelpDesk is the first product from the DDS Group on the path of AI-Assisted Database Management: a new breed of products where intelligence, governance, and operational memory live in your stack, not in a vendor's cloud.

See [design principles](docs/PRINCIPLES.md) and the [FAQ](docs/FAQ.md) before diving in.

---

## Deployment

aiHelpDesk runs on Kubernetes, VMs, bare metal or inside Docker/Podman containers. Binaries are provided for Linux x86-64 and ARM (Graviton, Ampere), and macOS (Intel and Apple Silicon).

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
tar xzf helpdesk-v0.1.0-deploy.tar.gz
kubectl create secret generic helpdesk-api-key --from-literal=api-key=<YOUR_API_KEY>
helm install helpdesk ./helpdesk-v0.1.0-deploy/helm/helpdesk \
  --set model.vendor=anthropic \
  --set model.name=claude-haiku-4-5-20251001
```

See [here](deploy/helm/README.md) for the full instructions.

---

## Architecture

See the primary [Architecture page](docs/ARCHITECTURE.md) for system design, configuration, and the extension guide.

## The Vault

See the primary [Vault page](docs/VAULT.md) for how aiHelpDesk accumulates and improves operational knowledge over time: the flywheel concept, how playbook drafts are auto-generated from incident traces, the three paths into the Vault, the review-and-activate workflow, and the three core customer workflows (onboarding, acceptance, regression monitoring).

## AI Governance

aiHelpDesk's Governance system rests on eight subsystems including full [auditing](docs/AUDIT.md), [compliance reporting](docs/COMPLIANCE.md), and the [journeys](docs/JOURNEYS.md). See the primary [AI Governance page](docs/AIGOVERNANCE.md) for the complete reference.

HTTP-level authorization is presented [here](docs/AUTHZ.md). Identity provider configuration (static users, JWT/OIDC, service accounts) is describe [here](docs/IDENTITY.md).

## Fleet Operations

See the primary [Fleet Management page](docs/FLEET.md) for how aiHelpDesk coordinates the multi-database operations: job definitions, canary phases, approval gates, schema drift detection, the natural-language Planner, and rollback.

## Playbooks

See the primary [Playbook page](docs/PLAYBOOKS.md) for how Playbooks take the central stage in aiHelpDesk. This page presents the Playbook schema, CRUD API, import formats (Markdown, YAML, Ansible, Rundeck), system playbooks, and versioning. See [here](docs/PLAYBOOK_OPS.md) for the Playbooks operational best practices.

## Testing

aiHelpDesk's testing strategy is documented in [here](testing/README.md). Two guides cover fault injection testing specifically:

- **[Fault Injection Testing](docs/FAULTTEST.md)** is the customer-facing guide: validate agent behavior against your own staging database using SQL-only injection, SSH-level fault injection, and automated remediation verification; no Docker or cluster access required
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

See the primary [API page](docs/API.md) for the full REST API reference: all endpoints with request/response shapes, query parameters, and `curl` examples.

## Helper Scripts

| Script | Description |
|--------|-------------|
| `scripts/gateway-repl.sh` | Interactive REPL using the Gateway API (recommended for containers) |
| `scripts/k8s-local-repl.sh` | Run orchestrator locally with K8s agents port-forwarded |

See [here](scripts/README.md) for detailed usage.

## Upstream Agents Calling aiHelpDesk

aiHelpDesk agents can be called by humans via the Orchestrator, or by upstream agents and programs via the A2A protocol and Gateway API:

- **[SREBot](cmd/srebot/README.md)** is the O11y watcher that calls aiHelpDesk to diagnose a database and ask for AI-powered triage
- **[SecBot](cmd/secbot/README.md)** is the Security responder that fires alerts for governance violations in real-time and creates incident bundles
- **[GovBot](cmd/govbot/README.md)** is the Compliance reporter that queries governance endpoints and posts structured snapshots to Slack on a schedule
- **[Real-Time Auditor](docs/AIGOVERNANCE.md#77-auditor-cli-cmdauditor)** is the Long-running daemon that connects to the audit socket and fires webhook/email/syslog alerts

See [here](docs/INTRO_DIALOG.md) for a sample interactive dialog with a human operator.

## LLM Support

aiHelpDesk is built on Google ADK for Go, extended to support both Anthropic and Gemini models:

| Vendor | Model | Notes |
|--------|-------|-------|
| Anthropic | `claude-haiku-4-5-20251001` | Fast, cost-effective |
| Anthropic | `claude-sonnet-4-20250514` | Balanced performance |
| Anthropic | `claude-opus-4-5-20251101` | Most capable |
| Gemini | `gemini-2.5-flash` | Fast, recommended for most use cases |
| Gemini | `gemini-2.5-flash-lite` | Fastest, lowest cost |
| Gemini | `gemini-2.5-pro` | Most capable 2.5 model |
| Gemini | `gemini-3-flash-preview` | Latest 3.0 series, fast |
| Gemini | `gemini-3-pro-preview` | Latest 3.0 series, most capable |

Individual expert agents can run with different LLMs if needed. Support for additional providers can be added by implementing ADK's LLM interface. Contact aiHelpDesk for specific model support requests.
