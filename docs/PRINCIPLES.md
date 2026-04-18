# aiHelpDesk Design Principles

These are the principles that drive every architectural decision in aiHelpDesk.
When a feature or a proposed change conflicts with one of these, the principle wins.

---

## 0. Quality beats Velocity

This is our Principle#0 - we move fast, but when we can't confidently quailfy a release
we stop and don't ship until we do.

## 1. aiHelpDesk is OSS

We believe in open source.

As they say, open source is a baseline for innovation, a collaborative foundation
that drives technological advancement. We believe that.

We also don't like black boxes where we need to take somebody else's word for what's
(hidden) inside. We don't think our customers should do that either. They deserve
better than just running vulnerability scans on our images and testing our
software in the hope that they are thorough enough to cover all the edge cases.
We prefer aiHelpDesk to be open to customers and to anybody else to review,
comment, send suggestions and PRs.

aiHelpDesk is OSS.

## 2. Tools, not prompts

aiHelpDesk does not let the LLM compose and run free-form SQL statements,
arbitrary shell commands, or ad-hoc `kubectl` invocations.

Every action the system can take is a named, code-reviewed, pre-approved tool:
`get_active_connections`, `describe_pod`, `cancel_query`, etc. The full list
is always visible in the formal repository that we refer to as the Tool
Registry (`GET /api/v1/tools`). Nothing outside that list can execute.

This is not a limitation — it is a deliberate design choice. A system that can
run any SQL or any shell command cannot be audited meaningfully, cannot enforce
blast-radius limits, cannot be tested for failure modes, and cannot earn the
trust of the operators who have to explain its actions to their organization.
Narrow tools with known inputs and known effects are the only foundation on
which safe automation can be built.

## 3. Mutations are in a category of their own

Read-only operations (checking connectivity, reading stats, listing pods)
and write/destructive operations (cancelling a query, terminating a session,
restarting a deployment) are fundamentally different. aiHelpDesk treats them
differently at every layer:

- All mutation tools are classified explicitly: `read`, `write`, or `destructive`.
- Mutations require a two-step **review-and-confirm** workflow — the LLM proposes,
  a human confirms before execution.
- Blast-radius limits are enforced in code, not by instruction: a policy can cap
  the number of rows that can be affected, or the number of connections that can
  be terminated, before execution is blocked — not after.
- Every mutation tool has a mandatory fault-injection test scenario in the test
  suite. A tool is not considered production-ready without one.
- The initial set of mutation tools (`cancel_query`, `terminate_connection`,
  `kill_idle`, `delete_pod`, `restart_deployment`, `scale_deployment`) are
  explicitly marked as not yet production-ready. We will not relax that
  designation until we are satisfied with the Governance module that wraps them.

## 4. Audit is non-negotiable

Every action — read or write, successful or failed, approved or denied — is
recorded in a tamper-proof, hash-chained audit log. The hash chain means
that any deletion or modification of a past record is detectable.

The audit trail is the foundation of everything else: policy enforcement,
compliance reports, approval workflows, journey reconstruction, and human
oversight. An AI system that cannot account for its own actions cannot be
trusted. aiHelpDesk starts from the assumption that the audit log must exist
before anything else, not as an afterthought.

## 5. The LLM is a reasoning layer, not an execution layer

The LLM in aiHelpDesk makes a proposal on *what* to do: which tool to call, in what order
and for which target. It has no authority to make decisions, only proposals that
aiHelpDesk is to accept and continue with or reject. In particular, LLM
does not decide *whether* it is allowed to do what it proposes — that is
the job of the policy engine. It does not decide *what command to run* — that
is the job of the tool implementation. And it cannot claim to have taken an
action it did not actually take — that is prevented by the delegation
verification subsystem, which cross-references audit records against the LLM's
stated reasoning.

This separation is intentional. LLMs are capable of sophisticated multi-step
reasoning, but they are also capable of hallucination, implicit assumption, and
inconsistent behaviour across runs. The system is engineered to tolerate those
failure modes rather than depend on the absence of them.

## 6. Context isolation through sub-agents

Each sub-agent (e.g. database, Kubernetes, incident) operates in its own context
window. When the Orchestrator delegates a task, the details of that task are
confined to the sub-agent's context. When the task completes, only the result
flows back — the diagnostic details, intermediate steps, and tool outputs are
discarded from the Orchestrator's context.

This is not only good engineering for context-window management; it is also a
security boundary. A sub-agent that is only ever asked about Kubernetes does
not need access to database credentials, and the Orchestrator's context does
not accumulate verbose low-level output that could distract the model or lead
to unexpected cross-domain reasoning.

## 7. Stateless sub-agents, stateful Orchestrator

Sub-agents receive everything they need per request: connection strings,
Kubernetes contexts, target server names. They store nothing between requests.
This means multiple Orchestrators (for different teams or environments) can
share the same sub-agent instances, sub-agents can be upgraded or replaced
independently, and no per-agent configuration sprawl accumulates over time.

The Orchestrator owns the infrastructure inventory. Sub-agents own their tool
implementations. The audit daemon owns the persistent record. Responsibility
is clearly partitioned.

## 8. Human in the loop is not optional

aiHelpDesk is a shift-left support tool, not a replacement for human
judgment or accountability. The system is designed to dramatically reduce the time it takes to
diagnose a problem and to surface the right information for a decision — but
the decision itself, especially for write and destructive operations for
production databases, rests with a human.

The approval workflow, the review-and-confirm step, the fleet-runner plan
review, and the dry-run mode are all expressions of this principle. The goal
is not to remove humans from the loop; it is to give humans much better
information when they are in the loop.

## 10. Probabilism is bounded, measured, and governed

The question is never "deterministic or probabilistic" — it is "which layer should reason adaptively, and where must hard guarantees be enforced?"

aiHelpDesk has three layers with different contracts:

```
  Reasoning layer   → probabilistic, contextual, adaptive   (LLM)
  Governance layer  → deterministic, enforced, auditable     (policy engine, blast-radius)
  Execution layer   → deterministic, exact, logged           (tool calls)
```

The LLM never directly executes anything. It proposes a tool call — `terminate_connection(pid=1234)` — and the governance layer evaluates it against policy rules, blast-radius limits, and approval gates before anything touches your infrastructure. The execution is byte-for-byte deterministic. Determinism at the *reasoning* layer would be harmful: a static mapping of symptom → action is precisely what fails when the root cause of a familiar symptom has changed, or when the environment differs from when the rule was written.

The probabilism in the reasoning layer is not left unchecked:

- **Bounded**: Playbook `guidance` constrains the planner's reasoning space. The LLM works within expert-encoded constraints, not from scratch.
- **Measured**: `faulttest` gives a concrete reliability figure across repeated runs — a number that static runbooks cannot produce.
- **Continuously narrowed**: every resolved incident auto-proposes a Playbook draft via the Vault. Every accepted draft encodes more expert knowledge into the guidance field, which further constrains the reasoning space on the next run. Variance shrinks as operational experience accumulates.

When exact step-by-step repeatability is non-negotiable, the fleet runner's explicit job definition format is available: exact tool, exact arguments, exact rollback steps — specified by a human and executed verbatim. The LLM selects *which* Playbook fits; the Playbook constrains what the planner may generate; the policy layer enforces hard limits. How much latitude the planner has is tunable, down to zero.

This principle is the answer to "AI systems are probabilistic — can you trust this in production?" Traditional operations are already probabilistic; the difference is that aiHelpDesk's probabilism is visible, bounded, measured, and continuously improved.

## 9. Extensibility without forking

A third-party provider with deep domain expertise can replace aiHelpDesk's
K8s agent with their own implementation, as long as it serves an agent card
at `/.well-known/agent-card.json` and follows the A2A protocol. The
Orchestrator does not care how a tool is implemented — only what it is named
and what it returns.

This applies equally to LLMs: the same Orchestrator and sub-agents can run on
Anthropic Claude or Google Gemini, switched via environment variable. No
business logic is coupled to a specific model provider. The same extensibility
point means that a locally-hosted model (Ollama, vLLM, or any
OpenAI-compatible inference server) can be substituted for a cloud API,
enabling fully air-gapped deployments. Adding a new model vendor is a single
factory change; nothing else in the system needs to know.
