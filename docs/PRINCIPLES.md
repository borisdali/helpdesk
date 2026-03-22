# aiHelpDesk Design Principles

These are the principles that drive every architectural decision in aiHelpDesk.
When a feature or a proposed change conflicts with one of these, the principle wins.

---

## 1. Tools, not prompts

aiHelpDesk does not let the LLM compose and run free-form SQL statements,
arbitrary shell commands, or ad-hoc `kubectl` invocations.

Every action the system can take is a named, code-reviewed, pre-approved tool:
`get_active_connections`, `describe_pod`, `cancel_query`, etc. The full list
is always visible in the formal repository that we refer to as thei Tool
Registry (`GET /api/v1/tools`). Nothing outside that list can execute.

This is not a limitation — it is a deliberate design choice. A system that can
run any SQL or any shell command cannot be audited meaningfully, cannot enforce
blast-radius limits, cannot be tested for failure modes, and cannot earn the
trust of the operators who have to explain its actions to their organization.
Narrow tools with known inputs and known effects are the only foundation on
which safe automation can be built.

## 2. Mutations are a different category of problem

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

## 3. Audit is non-negotiable

Every action — read or write, successful or failed, approved or denied — is
recorded in a tamper-proof, hash-chained audit log. The hash chain means
that any deletion or modification of a past record is detectable.

The audit trail is the foundation of everything else: policy enforcement,
compliance reports, approval workflows, journey reconstruction, and human
oversight. An AI system that cannot account for its own actions cannot be
trusted. aiHelpDesk starts from the assumption that the audit log must exist
before anything else, not as an afterthought.

## 4. The LLM is a reasoning layer, not an execution layer

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

## 5. Context isolation through sub-agents

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

## 6. Stateless sub-agents, stateful Orchestrator

Sub-agents receive everything they need per request: connection strings,
Kubernetes contexts, target server names. They store nothing between requests.
This means multiple Orchestrators (for different teams or environments) can
share the same sub-agent instances, sub-agents can be upgraded or replaced
independently, and no per-agent configuration sprawl accumulates over time.

The Orchestrator owns the infrastructure inventory. Sub-agents own their tool
implementations. The audit daemon owns the persistent record. Responsibility
is clearly partitioned.

## 7. Human in the loop is not optional

aiHelpDesk is a shift-left support tool, not a replacement for human
judgment or accountability. The system is designed to dramatically reduce the time it takes to
diagnose a problem and to surface the right information for a decision — but
the decision itself, especially for write and destructive operations, rests
with a human.

The approval workflow, the review-and-confirm step, the fleet-runner plan
review, and the dry-run mode are all expressions of this principle. The goal
is not to remove humans from the loop; it is to give humans much better
information when they are in the loop.

## 8. Extensibility without forking

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
