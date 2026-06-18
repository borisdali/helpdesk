# aiHelpDesk Informed Consent

> "You let AI operate on a production database without your consent?
> Stop calling it autonomous. Start calling it unaccountable. An informed consent for the 2am incident that may save or cripple your mission critical database isn’t a feature. It’s a necessity."
>
> This [blog post](https://medium.com/google-cloud/you-let-ai-operate-on-production-database-without-your-consent-bd4ffb954266) first introduced this concept.

Informed Consent is aiHelpDesk's answer to the question every operator eventually asks: *how do I know I can trust what the AI just told me. And did I actually agree to let it act?*

The concept borrows from medical ethics. Before a surgeon operates, three things must happen: the patient must be **informed** of the diagnosis and proposed treatment, must **consent** to the procedure and must retain the **right to refuse**. Skipping any one of these is not a shortcut. It is a failure of accountability.

aiHelpDesk applies the same framework to AI-driven database and infrastructure operations.

---

## The three parts

### Part 1: Informed

Before any remediation step executes, the agent presents everything it knows:

- Root-cause hypothesis with supporting evidence (active locks, waiting sessions, slow query plans, pod restart history)
- Confidence level — hypotheses below 50% automatically trigger human review and cannot proceed autonomously
- Proposed remediation steps with expected effects and blast-radius estimates
- Rollback estimate when an uncommitted transaction would be affected

This is the **triage gate**: the mandatory pause between diagnosis and action. Nothing executes until the operator has seen the full picture.

**What makes "Informed" verifiable, not just claimed**

Any system can claim to show a diagnosis. The claim only means something if the diagnosis is actually correct. This is where the [Operational Flywheel](VAULT.md#the-operational-sredba-flywheel) closes the loop: every operator response at the gate — "was this diagnosis correct?" — feeds `vault accuracy`, which tracks per-playbook diagnosis correctness across all runs. `vault calibration` then checks whether the model's own confidence scores predict real accuracy. If a playbook consistently diagnoses incorrectly, the flywheel surfaces it before you have to learn that the hard way.

*Without the flywheel, Informed Consent is a process claim. With it, it is a measured property.*

### Part 2: Consent

Consent is not a binary. aiHelpDesk offers four approval modes so operators can choose the level of oversight that matches their risk tolerance:

| Mode | What the operator controls |
|------|---------------------------|
| `manual` | Every write and destructive action requires explicit approval |
| `review` | Operator reviews the full remediation plan, then approves the batch |
| `session` | A named session token pre-authorises a scoped set of actions |
| `auto` | Fully autonomous within the playbook's `permitted_tools` whitelist |

In all modes, the Decision Hub (`GET /api/v1/decisions`) gives a non-interactive path to the same gate — curl, webhook, or CI pipeline can resolve it. The operator is never forced to be at a terminal.

The consent is also **time-bounded**: a gate that is not resolved within the configured timeout transitions to `abandoned`, not silently approved.

### Part 3: Right to Refuse

Denial is a first-class outcome. When an operator denies the gate:

- The remediation does not execute — period
- The denial reason (`verdict_notes`) is recorded in the audit log alongside the run ID, operator identity and timestamp
- The triage run is marked with the operator's verdict: the agent's diagnosis can be tagged as incorrect even without remediation proceeding
- The hash-chained audit record cannot be modified after the fact

The right to refuse also applies to individual steps in `agent_approve` mode: an operator who approves step 1 (read) can still deny step 2 (write) when the concrete tool call and arguments are visible.

---

## How the three parts map to aiHelpDesk features

| Informed Consent part | Where it happens | How to operate it |
|-----------------------|-----------------|-------------------|
| Informed: triage result | Triage gate (`pending_gate` decision) | [PLAYBOOK_OPS.md §2.3](PLAYBOOK_OPS.md#23-step-3-understand-the-triage-result) |
| Informed: proposed steps | Same gate — remediation plan preview | [PLAYBOOK_OPS.md §2.4](PLAYBOOK_OPS.md#24-step-4-provide-an-informed-consent-aka-reviewapprove-the-gate) |
| Informed: accuracy verified | `vault accuracy`, `vault calibration` | [VAULT_FEEDBACK_FLOW.md](VAULT_FEEDBACK_FLOW.md#vault-command-reference) |
| Consent: approval mode | `approval_mode` field on playbook | [PLAYBOOKS.md](PLAYBOOKS.md) |
| Consent: gate resolution | Decision Hub or interactive gate | [DECISIONS.md](DECISIONS.md), [PLAYBOOK_OPS.md §2.4](PLAYBOOK_OPS.md#24-step-4-provide-an-informed-consent-aka-reviewapprove-the-gate) |
| Right to Refuse: deny | `POST /api/v1/decisions/gate:{runID}/resolve` with `resolution=denied` | [PLAYBOOK_OPS.md §2.4](PLAYBOOK_OPS.md#24-step-4-provide-an-informed-consent-aka-reviewapprove-the-gate) |
| Right to Refuse: audit trail | Hash-chained audit log | [AUDIT.md](AUDIT.md) |
| Right to Refuse: feedback tagged | `verdict_correct=false` at gate | [VAULT_FEEDBACK_FLOW.md §Feedback reference](VAULT_FEEDBACK_FLOW.md#feedback-reference) |

---

## Why autonomy is not the goal

A common framing of AI operations is "how much can we automate?" aiHelpDesk's framing is different: how much can we *verify* and therefore how much can we *trust*?

Full autonomy (`auto` mode) is available, but it is the end of a journey, not the starting point. The journey is:

1. Run the playbook in `manual` mode with operator feedback enabled
2. Let `vault accuracy` accumulate diagnosis correctness data
3. Run `vault calibration` to confirm the model's confidence scores are trustworthy
4. Once accuracy and calibration meet your bar, relax the approval mode

Skipping to autonomy without steps 1–3 is trading accountability for speed. The blog post that introduced this concept names that trade-off directly: "faster failure, not reliability."

---

See also the [Operational Guide](VAULT_FEEDBACK_FLOW.md) on how we turn Informed Consent from a methodology to a shipping product.

## Related docs

- [Blog post: You let AI operate on a production database without your consent](https://medium.com/google-cloud/you-let-ai-operate-on-production-database-without-your-consent-bd4ffb954266) — the external reference that motivated this framework
- [PRINCIPLES.md §8](PRINCIPLES.md#8-informed-consent-human-in-the-loop-is-not-optional) — design principle
- [PLAYBOOK_OPS.md §2.4](PLAYBOOK_OPS.md#24-step-4-provide-an-informed-consent-aka-reviewapprove-the-gate) — step-by-step gate operations with curl commands
- [VAULT_FEEDBACK_FLOW.md](VAULT_FEEDBACK_FLOW.md) — how feedback enters the flywheel and what each `vault` command measures
- [DECISIONS.md](DECISIONS.md) — Decision Hub reference
- [MUTATION_TOOLS.md §2](MUTATION_TOOLS.md#2-two-step-review-and-confirm-process) — two-step review-and-confirm for mutation tools
- [BENCHMARKING_SAMPLE6.md](BENCHMARKING_SAMPLE6.md) — end-to-end walkthrough on Docker/Podman
