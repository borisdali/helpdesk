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

> In the demo runner and in production terminal UI, the gate is labelled the
> **INFORMED CONSENT GATE** — the name signals that this is an entitlement
> (Right I), not a feature that can be configured away.

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

In all modes, the Decision Hub (`GET /api/v1/decisions`) gives a non-interactive path to the same gate — curl, webhook or CI pipeline can resolve it. The operator is never forced to be at a terminal.

The consent is also **time-bounded**: a gate that is not resolved within the configured timeout transitions to `abandoned`, not silently approved.

### Part 3: Right to Refuse

Denial is a first-class outcome. When an operator denies the gate:

- The remediation does not execute — period
- The denial reason (`verdict_notes`) is recorded in the audit log alongside the run ID, operator identity and timestamp
- The triage run is marked with the operator's verdict: the agent's diagnosis can be tagged as incorrect even without remediation proceeding
- The hash-chained audit record cannot be modified after the fact

The right to refuse also applies to individual steps in `agent_approve` mode: an operator who approves step 1 (read) can still deny step 2 (write) when the concrete tool call and arguments are visible.

---

## Informed in numbers: the objectives, the budget and the ledger

The three parts above are about a diagnosis and a plan. For a **restore** there is a second thing a person should be
informed of before consenting: what it will cost. A PITR throws away data and takes a service down and a team has
already said how much of *each* it can afford, in three numbers: the **RPO** (how much it can lose), the **RTO** (how long
it can wait) and the availability **SLO** (how much downtime it can afford in total). Before v0.35 those lived in three
places and the gate knew about none of them. Now the gate puts them next to each other, in front of the approver, in the
plan (`plan_point_in_time_recovery`, printed by `helpdesk-client --pitr-plan`):

- **Data**:   
  The history and the number of transactions, the restore would discard, against the `rpo`.

- **Time**:   
  How long this server's restores really take (from its own past restores, not a guess), against the `rto`.

- **Downtime**:   
  The cost of the restore against the **availability budget**, which is the SLO converted into minutes
  (99.9% over 30 days is 43m 12s). What is left of it is read from the **ledger**: the record of what each real outage
  already spent. The budget is the allowance and the ledger is the receipt.

What this adds to each part:

| Part | What the objectives, budget and ledger add |
|---|---|
| **Informed** | The consequence is a number set against a declared limit, not an adjective. It also says what it could not measure (no transaction count, no history) instead of leaving a gap that reads as "nothing". |
| **Consent** | When the restore would exceed an objective, approving it is not enough: a second, separate acknowledgement (`--acknowledge-objective-breach`) is required and it names the breach it covers. It is bound to the plan digest, so it is consent to this plan. Only the human's approval at the gateway can give it, never the model and the restore tool itself refuses without it, before it stops anything. It is recorded in the hash-chained `recovery_decision` event with who gave it. |
| **Right to refuse** | An objective can be set to `refuse` in advance, by whoever owns it, so refusing is a decision made calmly and not under pressure at 2am. A refused restore leaves the instance untouched. A down instance is asked and not refused, because refusing would keep it down and spend the budget faster. |

Two things are different from the diagnosis case and are worth stating:

- **The consent is cumulative**   
  The budget is shared by every incident in the window, so a "yes" spends something that
  every later decision also draws on. The ledger is what lets an approver see what is already gone.

- **The numbers need the same proof as the diagnosis**   
  Part 1 says the claim is only worth something if the diagnosis
  is shown to be correct and the flywheel checks that. The budget's figures are only an informed basis if they are
  shown to match what restores really take. That check is not built yet: the plan's estimate and the actual duration
  are recorded, but not yet compared and the estimate needs real restores to learn from (planned for v0.36, with
  rehearsals on a clone). Until then the estimate says how many samples stand behind it and the ledger's clock
  is not the downtime ([the two clocks](OBJECTIVES.md#two-clocks)).

Two limits stay as they are: the acknowledgement records who gave it, not why (a justification and a ticket number
are slated for v0.36) and the audit record is tamper-evident, not signed yet.

---

## How the three parts map to aiHelpDesk features

| Informed Consent part | Where it happens | How to operate it |
|-----------------------|-----------------|-------------------|
| Informed: triage result | Triage gate (`pending_gate` decision) | [PLAYBOOK_OPS.md §2.3](PLAYBOOK_OPS.md#23-step-3-follow-the-escalation-if-needed) |
| Informed: proposed steps | Same gate — remediation plan preview | [PLAYBOOK_OPS.md §2.4](PLAYBOOK_OPS.md#24-step-4-provide-an-informed-consent-aka-reviewapprove-the-gate) |
| Informed: accuracy verified | `vault accuracy`, `vault calibration` | [VAULT_FEEDBACK_FLOW.md](VAULT_FEEDBACK_FLOW.md#vault-command-reference) |
| Consent: approval mode | `approval_mode` field on playbook | [PLAYBOOKS.md](PLAYBOOKS.md) |
| Consent: gate resolution | Decision Hub or interactive gate | [DECISIONS.md](DECISIONS.md), [PLAYBOOK_OPS.md §2.4](PLAYBOOK_OPS.md#24-step-4-provide-an-informed-consent-aka-reviewapprove-the-gate) |
| Informed: the cost of a restore | RPO, RTO and BUDGET CHECK in the plan; the ledger | [OBJECTIVES.md](OBJECTIVES.md), `helpdesk-client --pitr-plan`, `faulttest vault budget <db>` |
| Consent: a named second yes | `--acknowledge-objective-breach`, bound to the plan digest | [PITR.md](PITR.md), [OBJECTIVES.md](OBJECTIVES.md) |
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
- [OBJECTIVES.md](OBJECTIVES.md) — the RPO, RTO and availability SLO, the budget, the ledger and the recovery-time estimate that make the gate informed about a restore's cost
- [VS_YOUR_SCRIPTS.md](VS_YOUR_SCRIPTS.md) — why a script that restores cleanly does not give an approver this
- [PRINCIPLES.md §8](PRINCIPLES.md#8-informed-consent-human-in-the-loop-is-not-optional) — design principle
- [PLAYBOOK_OPS.md §2.4](PLAYBOOK_OPS.md#24-step-4-provide-an-informed-consent-aka-reviewapprove-the-gate) — step-by-step gate operations with curl commands
- [VAULT_FEEDBACK_FLOW.md](VAULT_FEEDBACK_FLOW.md) — how feedback enters the flywheel and what each `vault` command measures
- [DECISIONS.md](DECISIONS.md) — Decision Hub reference
- [MUTATION_TOOLS.md §2](MUTATION_TOOLS.md#2-two-step-review-and-confirm-process) — two-step review-and-confirm for mutation tools
- [SAMPLE006.md](samples/SAMPLE006.md) — end-to-end walkthrough on Docker/Podman
- [DEMO.md](../deploy/docker-compose/DEMO.md) — the 10-minute demo surfaces the INFORMED CONSENT GATE interactively, followed by a Right IV calibration record and Bill of Rights summary
