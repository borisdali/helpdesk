# aiHelpDesk FAQ

Before reviewing the frequently asked questions below
we recommend getting familiar with aiHelpDesk eight
[design principles](PRINCIPLES.md).

---

## "Isn't this just a chatbot?"

No. A chatbot generates text. aiHelpDesk executes actions against your
infrastructure.

When you ask a chatbot "why are my Postgres queries slow?", it gives you a
generic answer based on training data. When you ask aiHelpDesk the same
question, it connects to your actual database, runs `get_active_connections`
and `get_lock_info`, reads your real `pg_stat_activity`, and tells you which
specific query, from which specific session, has been blocked on which specific
lock for how many seconds — on your instance, right now.

The conversation interface is a UX layer. The substance is a multi-agent system
with structured tool calls, a policy engine, an audit trail, and an approval
workflow.

---

## "Why do I need this when I can just ask ChatGPT?"

ChatGPT and similar general-purpose assistants cannot connect to your
infrastructure. They do not know your actual table sizes, your running
transactions, your replication lag, your pod restart history, or your lock
contention at this moment. They can suggest what to look at; they cannot look.

Beyond that, ChatGPT is unlikely to "remember" your database history,
your workload, your operational metrics and your data growth patterns.
If allowed, aiHelpDesk can do all of the above, which gives it a full
context of a problem, not only immediate, but with the historical perspective
in mind as well.

For remediation, a general-purpose AI assistant has no notion of authorization,
blast-radius limits, or audit. If you paste a connection string into ChatGPT
and ask it what SQL to run, there is no record of that conversation, no policy
check on the proposed query, no confirmation step before execution, and no
tamper-proof log of what was done. For production infrastructure operated by
teams with compliance requirements, that is not an acceptable workflow.

aiHelpDesk is purpose-built for infrastructure operations: it knows your
environment because you gave it your `infrastructure.json`, it enforces your
policies because you wrote your policy rules, and every action it takes or
proposes is recorded in an auditable trail. Guaranteed.

---

## "AI systems are probabilistic. Production operations require determinism. How can you trust this in production?"

This is the sharpest objection to any AI operations system, and it deserves a precise answer.

**First: traditional operations are already probabilistic — just unmeasured.**

A human SRE following a runbook makes implicit judgment calls at every branch: "if connections are exhausted, go to step 4b" — but which connections? Is this different from last time? The runbook was written six months ago; the schema has changed. The on-call engineer is tired at 3am. Different engineers execute the same runbook differently, silently, with no record of which branch was taken or why.

The "determinism" of a static runbook is largely illusory. It holds only in the most controlled lab conditions. In production, human + static runbook is deeply probabilistic — just probabilistic in a way nobody measures. aiHelpDesk doesn't introduce probabilism. It makes existing probabilism **visible, auditable, and improvable**.

**Second: aiHelpDesk's architecture places determinism exactly where it matters.**

The system has three distinct layers, each with a different contract:

```
  Reasoning layer   → probabilistic, contextual, adaptive   (LLM)
  Governance layer  → deterministic, enforced, auditable     (policy engine, blast-radius)
  Execution layer   → deterministic, exact, logged           (tool calls)
```

The LLM never directly executes anything. It proposes a tool call — `terminate_connection(pid=1234)` — and the governance layer evaluates it against policy rules, blast-radius limits, and approval gates before anything touches your database. `terminate_connection(pid=1234)` then executes exactly that command, logs it exactly, and the audit chain records it immutably. The execution is byte-for-byte deterministic.

Determinism at the *reasoning* layer would actually be harmful: a static mapping of "symptom → action" is precisely what fails when the root cause of a familiar symptom has changed, or when a new tool is available that handles the situation better, or when the environment differs from when the runbook was written.

**Third: the probabilism in aiHelpDesk is bounded, measured, and continuously narrowed.**

- **Bounded**: Playbook `guidance` constrains the planner's reasoning space. The LLM isn't reasoning from scratch — it works within expert-encoded constraints that tighten variance toward known-good approaches.
- **Measured**: `faulttest` gives you a concrete reliability figure. Run `db-max-connections` fifty times — if the agent diagnoses correctly 92% of the time with 100% remediation recovery, that's a number you can put in a runbook review. No human + static runbook system can tell you its reliability percentage.
- **Continuously narrowed**: every resolved incident auto-proposes a Playbook draft. Every accepted draft encodes more expert knowledge into the guidance field, which further constrains the reasoning space on the next run. The [Vault](VAULT.md) is the mechanism by which variance shrinks over time as operational experience accumulates.

**The honest caveat.**

For workflows where exact step-by-step repeatability is non-negotiable — regulatory procedures with prescribed command sequences, PITR restores to a specific LSN, compliance-mandated audit trails of precise actions — the answer isn't "trust the LLM." It's to use the fleet runner's explicit job definition format, which is fully deterministic: the exact tool, exact arguments, exact rollback steps are specified by a human and executed verbatim. The LLM selects *which* Playbook fits the situation; the Playbook constrains what the planner may generate; the policy layer enforces hard limits. You can tune how much latitude the planner has, down to zero.

The question is never "deterministic or probabilistic" — it is "which layer should reason adaptively, and where must we enforce hard guarantees?" aiHelpDesk is designed around that distinction.

---

## "Can it run arbitrary SQL statements or shell commands?"

No, and this is intentional.

aiHelpDesk operates exclusively through a strict Tool Registry. Every action
the system can take is a named, code-reviewed tool with defined inputs, defined
outputs, and a known action class (read, write, or destructive). The full list
is always visible at `GET /api/v1/tools`.

The database agent has a strictly defined number tools: checking connectivity, reading stats,
listing active connections, checking replication, finding locks, etc. As a customer
you also get to control which tools you allow in your environment. There is
no `run_sql` escape hatch. The Kubernetes agent is the same: listing pods,
reading logs, describing services, etc. There is no `kubectl exec` escape hatch.

The same, only stricter constraint applies to the fleet management operations:
a fleet job specifies which exact tool to run on which servers — it cannot
inject an arbitrary command.

This is not a missing feature. A system that can run arbitrary SQL or arbitrary
shell commands cannot enforce blast-radius limits, cannot be audited
meaningfully, and cannot give operators the guarantee that "this system will
never drop a table because I asked the wrong question." The Tool Registry is
the boundary of what is possible, and that boundary is what makes the system
trustworthy.

If you need a tool that does not exist today, the right path is to add it to
the appropriate agent with the appropriate tests, classification, and safeguards
— not to add a generic execution escape hatch.

---

## "What stops the AI from making a destructive mistake?"

Several independent layers:

**Tool classification and policy engine.** Every tool is classified as `read`,
`write`, or `destructive`. The policy engine can block any class of tool for
any combination of user, purpose, time window, or resource. A policy that says
"only read tools are allowed on prod unless `purpose=incident-p1`" is
enforced before any tool call happens, regardless of what the LLM said.

**Blast-radius limits.** Write and destructive tools have mandatory pre-flight
checks. For example, `kill_idle_connections` counts the connections it would
terminate and blocks if that number exceeds the configured limit. The LLM
cannot override this — it is enforced in code.

**Two-step review-and-confirm.** For mutation operations, the LLM proposes the
action and the human confirms before execution. The system does not execute
first and ask forgiveness later.

**Approval workflow.** For critical production systems marked with a
`sensitivity` level in the infrastructure config, write and destructive jobs
enter an explicit human approval queue. Fleet management pauses before the canary
wave and waits for approval.

**Audit trail.** Every action, approved or denied, successful or failed, is
recorded in a hash-chained audit log. Any modification or deletion of a past
record is detectable. The audit trail is the mechanism for accountability after
the fact.

---

## "Does it have access to my production database credentials?"

The Orchestrator holds the infrastructure inventory (`infrastructure.json`),
which includes connection strings. It passes the relevant connection string to
the sub-agent on a per-request basis — the sub-agent does not store it.

In practice, aiHelpDesk connects to your databases with the credentials
you configure. For production databases we strontly advise keeping the
credentials in the external vaults, retrieving them and passing along
to aiHelpDesk at runtime. For databases with sensitive data, we encourage
customers to not only tag them as "production" and "critical", but set
the `sensitivity` field to `pii` or similar in the infrastructure config.
The system then enforces the additional safeguards described above, and the planner
excludes those servers from fleet jobs by default.

You retain full control of what credentials you provide and therefore what
access the system has. aiHelpDesk does not require superuser access —
read-only diagnostic tools need only a monitoring role.

---

## "Can I control who can do what?"

Yes, at multiple levels.

**API key / principal**: the Gateway resolves the caller's identity from the
`Authorization` header or `X-Purpose` / `X-Principal` headers. Every audit
event records the resolved principal.

**Policy rules**: the YAML-based policy engine lets you write rules like
"deny write tools for role=readonly", "require approval for destructive tools
on servers tagged production", or "allow read tools only when purpose=diagnostic
or purpose=incident-p1". Rules are evaluated in priority order and the first
match wins.

**Sensitivity tagging**: servers in `infrastructure.json` can have a
`sensitivity` list (e.g. `["pii", "financial"]`). The fleet planner excludes
sensitive servers from jobs by default, and the policy engine can reference
sensitivity tags in its rules.

**Fleet approval gating**: jobs whose steps include write or destructive tools
pause before execution and wait for explicit human approval via
`POST /v1/fleet/jobs/{jobID}/approval`.

---

## "What LLMs does it support?"

At the moment the tested models are Anthropic Claude (Haiku, Sonnet, Opus — any generation) and Google Gemini
(2.5 Flash, 2.5 Pro, 3.0 series). The model is selected via `HELPDESK_MODEL_VENDOR`
and `HELPDESK_MODEL_NAME`. The orchestrator and the fleet planner can use
different models if you want a faster, cheaper model for routine diagnosis
and a more capable one for planning.

No application code is coupled to a specific provider. Switching models is an
environment variable change.

---

## "Can it run in a disconnected or air-gapped environment?"

Architecturally yes — with a locally-hosted model. In practice, not yet without
a small code addition.

Every component of aiHelpDesk except the LLM call is fully local: the
Orchestrator, all sub-agents, the Gateway, auditd, and fleet-runner are Go
binaries that communicate over localhost HTTP. They connect to your databases
via `psql`, to your cluster via `kubectl` or `client-go`, and to each other via the A2A
protocol. There is no telemetry, no license server, no data leaving your
network, and no dependency on any cloud service other than the LLM API.

The LLM is the single external dependency. Currently, the system has adapters
for two cloud APIs: Anthropic Claude and Google Gemini. Running fully air-gapped
requires pointing the LLM at a locally-hosted inference server instead — for
example Ollama, which exposes an OpenAI-compatible API, or any other local
inference endpoint.

Adding that adapter is a single, well-scoped change: a new
`HELPDESK_MODEL_VENDOR` case in the LLM factory that points at
`http://localhost:11434` (or wherever the local server listens). Nothing else
in the system would need to change. This is on the roadmap; it is a gap in
current implementation, not a fundamental architectural constraint.

---

## "Is the audit trail tamper-proof?"

Each audit event includes a SHA-256 hash of the previous event's hash plus
its own content, forming a hash chain. Any modification, insertion, or deletion
of a past record breaks the chain at that point. The `GET /api/v1/governance/verify`
endpoint walks the full chain and reports the first inconsistency found, if any.

The audit store is a SQLite database (or Postgres for production deployments).
It does not prevent someone with direct filesystem or database access from
modifying records — tamper detection is not the same as tamper prevention.
What it provides is the ability to prove to an auditor, after the fact, whether
the record was intact or was altered. aiHelpDesk is also equipped with the
out of the box jobs to verify that the audit hasn't been tampered with
and alert otherwise.

---

## "What is the fleet management for?"

The interactive Orchestrator is for a human working through a problem
one step at a time. `fleet-runner` is for applying a known, reviewed operation
across many servers at once — think "run `get_database_stats` on all 40
staging databases" or "cancel idle transactions on all production replicas".

Fleet jobs use a canary → wave rollout strategy: the job runs on a small canary
first, and only fans out across waves if the canary succeeds. A circuit breaker
halts the job when the failure rate within a wave exceeds a configured
threshold.

The NL planner (`POST /api/v1/fleet/plan`) lets you describe a fleet job in
natural language and receive a structured `JobDef` JSON for review. You inspect
it, optionally edit it, and then submit it to fleet-runner. The plan is never
auto-submitted — it always requires a human to pull the trigger.

---

## "Can I use my own agent implementations?"

Yes. Sub-agents are standalone A2A servers. Any agent that serves a card at
`/.well-known/agent-card.json` and follows the A2A protocol can be registered
with the orchestrator or Gateway. A provider with deep expertise in, say,
MySQL or CockroachDB can implement their own database agent and swap it in
without touching the orchestrator or any other part of the system.

The A2A protocol details what the agent card must contain, how tasks are
submitted, and how results are returned. The `agentutil` SDK in this repository
provides a thin wrapper for building compliant agents in Go, but nothing
requires agents to be written in Go.

---

## "The mutation tools are marked not production-ready. When will they be?"

When we are satisfied that the governance module is complete and battle-tested
enough to be trusted with them.

The diagnostic (read-only) tools are production-ready today. The mutation tools
like `cancel_query`, `terminate_connection`,`kill_idle_connections`,`delete_pod`,
`restart_deployment`, `scale_deployment`, etc. exist in the codebase to drive
development and testing of the governance module. They will not carry the
production-ready designation until the policy engine, blast-radius controls,
two-step confirm, fault-injection test coverage, and approval workflow have all
been validated to our satisfaction.

We would rather be conservative and wrong about the timeline than release early
and be wrong about safety.
