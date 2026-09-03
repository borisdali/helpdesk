# aiHelpDesk Second Opinion

Every consequential claim aiHelpDesk makes — about a root cause, a remediation plan, a
playbook improvement, a confidence score, a stability certificate — has at least one
independent check. This is not a safety feature bolted on after the fact. It is a design
principle: **probabilistic systems must be checked by something other than themselves.**

The medical analogy that we already used in defining [Informed Consent](INFORMED_CONSENT.md), makes its way here as well and it's deliberate. A radiologist reads a scan. A second radiologist reads
the same scan before surgery. The second reading does not imply the first was wrong. It
acknowledges that consequential decisions under uncertainty require independent verification,
not because any one reading is untrustworthy in general, but because the cost of a wrong
answer in a specific case is too high to leave unverified.

aiHelpDesk applies this at five layers.

---

## Table of Contents

1. [Layer 1: Diagnosis](#1-layer-1-diagnosis)
2. [Layer 2: Remediation](#2-layer-2-remediation)
3. [Layer 3: Playbook Improvement](#3-layer-3-playbook-improvement)
4. [Layer 4: Confidence Calibration](#4-layer-4-confidence-calibration)
5. [Layer 5: Stability](#5-layer-5-stability)
6. [The Accountability Gap: Auditing the Auditors](#6-the-accountability-gap-auditing-the-auditors)
7. [Summary Table](#7-summary-table)
8. [See also](#8-see-also)

---

## 1. Layer 1: Diagnosis

**First opinion:** The monitoring system fires an alert: connection pool exhausted,
replication lag, pod crash loop.

**Second opinion:** The aiHelpDesk triage agent runs the playbook, collects tool evidence,
forms a hypothesis and reports a root cause with a confidence score. This is an independent
assessment: the agent has no knowledge of what the alert said. It derives its conclusion from
the current state of the system, not from the alert label.

**What makes it verifiable:**

- The triage gate presents both the agent's hypothesis and the evidence it cited, before
  any action is taken. The operator can compare the alert to the diagnosis directly.
- `vault accuracy` tracks whether operator verdicts (`verdict_correct`) match the agent's
  root-cause claims across all runs.
- `vault calibration` checks whether the agent's stated confidence (`CONFIDENCE: 0.92`)
  predicts actual accuracy over time.

**Resolved (v0.20.0):** the triggering alert text is now persisted as `trigger_context` on
the `PlaybookRun` record, so the "alert said X, aiHelpDesk said Y" comparison is available
after the fact, not just at gate time.

**A different kind of check, not a third opinion:** before this diagnosis ever reaches a
human, a deterministic, code-derived backstop can also force the gate — not another party
forming a judgment, but a plain comparison between a typed value read off a tool's real
result and the value the agent's own response quoted. See
[AIGOVERNANCE.md §1.1 Layer 3](AIGOVERNANCE.md#11-llm-fabrication-detection) and
[OBJECTIVE_EVIDENCE.md](OBJECTIVE_EVIDENCE.md) for the full mechanism — it's deliberately
outside this document's opinion-counting scheme, since it isn't an opinion at all.

---

## 2. Layer 2: Remediation

**First opinion:** The triage agent proposes a remediation plan: terminate connection PID
1234, restart deployment api-server, run VACUUM on orders table.

**Second opinion:** The operator reviews the diagnosis and proposed steps at the triage
gate before any write or destructive action executes. The operator sees the full evidence,
the confidence level, the proposed steps and the blast-radius estimate. They can approve,
deny or modify.

**What makes it verifiable:**

- Every gate resolution — approve or deny — is recorded in the tamper-proof audit trail
  with operator identity, timestamp and the verdict reason.
- Denial is a first-class outcome. A denied gate is not a failure; it is the system
  working as intended.
- `verdict_notes` at the gate captures the operator's reasoning, which feeds `vault
  calibration` as an independent ground-truth signal.
- Individual steps in `agent_approve` mode can be approved or denied independently —
  approving step 1 (read) does not commit you to step 2 (write).

**Current gap:** None structural. Quality of this layer depends on operators actually
submitting `verdict_correct` — see Layer 4 for the calibration data dependency.

---

## 3. Layer 3: Playbook Improvement

This layer has two independent checks in sequence.

**First opinion:** `vault suggest-update` synthesises a proposed guidance change from
recent failure traces. An LLM reads the failure pattern and proposes a revised playbook.

**Second opinion (automated):** `vault diff --judge` asks a separate LLM — the Judge,
typically a smaller, faster model — to evaluate whether the proposed change is an
improvement, a regression or uncertain. The Judge produces a structured verdict:
`APPROVE`, `NEEDS_REVIEW` or `REJECT`, with one-sentence reasoning.

**Third opinion (human):** The operator reads the diff and the Judge verdict and decides
whether to activate the draft. A `NEEDS_REVIEW` verdict routes the decision to human
judgment. No draft auto-activates under any circumstances.

**What makes it verifiable:**

- The diff is field-by-field, human-readable and permanent. `vault diff <v1> <v2>`
  works even after both versions are active.
- The Judge verdict is stored on the draft record (`judge_verdict`, `judge_model`,
  `judge_at`).
- Subsequent `vault versions` data shows whether the activated version actually improved
  — closing the loop on whether the improvement was real.

**Resolved (v0.20.0):** the Judge's track record is now measured — `vault judge-accuracy`
compares judge verdicts recorded at diff time against actual subsequent run outcomes,
per series. See [§6](#6-the-accountability-gap-auditing-the-auditors).

---

## 4. Layer 4: Confidence Calibration

**First opinion:** The agent states `CONFIDENCE: 0.92` on its primary hypothesis.

**Second opinion:** `vault calibration` bands all runs by stated confidence and checks
whether actual accuracy — measured against operator `verdict_correct` submissions —
matches. If the agent says 92% confident and is right 75% of the time, the calibration
band returns `OVERCONFIDENT`.

**What makes it verifiable:**

- The command is queryable at any time: `faulttest vault calibration --gateway $GW`.
- Bands are model-specific — the model cert PK (v0.20.0) keeps different models' figures apart.
- `HumanRuns` vs `AutoJudgeRuns` breakdown is surfaced in the output.

**Resolved (v0.20.0):**

1. **Model cert PK** — `fault_stability_cert`'s primary key is `(fault_id,
   diagnosis_model)`, not `fault_id` alone. Two models' accuracy figures are no longer
   blended into one band.

2. **Auto-judge dominance** — a prominent data-quality banner now prints before the
   calibration table whenever human coverage is below 50% (or zero), so "calibration"
   measuring self-consistency rather than accuracy against human judgment is surfaced,
   not silent. Check `Sources:`/the banner in the command output.

The `vault calibration` table's meaningfulness still depends on the actual composition of
the feedback it draws from — the banner makes that composition visible, it doesn't make
thin human coverage go away.

---

## 5. Layer 5: Stability

**First opinion:** A single faulttest run produces a pass or fail and a score.

**Second opinion:** `faulttest run --repeat N` runs the same fault N times under the same
conditions and certifies whether the pass rate (≥ 80%) and confidence spread (≤ 30pp)
are within bounds. Variance across runs, not just the mean, is what the cert measures.

**What makes it verifiable:**

- The cert is stored in `fault_stability_cert` and queryable via `vault accuracy`.
- Cert age is shown: `STABLE(5) 14d` — the number of days since last certification.
- A cert issued before a model upgrade is explicitly marked as potentially stale.

**Resolved (v0.20.0):** same fix as Layer 4 — the cert's primary key includes
`diagnosis_model`, so a cert issued under Sonnet 4.6 is no longer overwritten by a run
under Opus 4.8. The badge names the model it certifies.

---

## 6. The Accountability Gap: Auditing the Auditors

Every second-opinion mechanism above is itself a claim. The claim is only as strong as
the mechanism's own accountability.

**The Judge's track record is now measured (v0.20.0).** Judge verdicts are stored on
draft records; once runs accumulate under an activated version, `vault judge-accuracy`
correlates the activation judge verdict against the actual improvement delta from `vault
versions`, per series — the judge is accountable to the same measurement infrastructure
it is part of, and `vault diff --judge APPROVE`'s real track record is queryable rather
than assumed.

This closed a real, non-hypothetical concern. In the pbs_connection_triage case study
(see [JUDGMENT_LAYER.md](JUDGMENT_LAYER.md)), `vault suggest-update` identified the failure
pattern correctly and the judge would likely have approved a draft that would have made the
failure rate worse. The human layer caught it that time. Before v0.20.0, if the judge had
approved it convincingly instead, an operator might have activated it without the scrutiny
the NEEDS_REVIEW path prompted — and nothing downstream would have flagged that this
judge's APPROVE verdicts don't hold up. `vault judge-accuracy` now would.

**What's still worth watching:** the correlation needs runs to accumulate under an
activated version before it's meaningful — a freshly-activated draft has no track record
yet by construction, not because the mechanism is broken.

The broader principle: **any system that provides a second opinion must itself be subject
to a second opinion.** The chain terminates at human judgment — but the human judgment
layer is most effective when it has data about whether the automated checks below it have
been reliable.

---

## 7. Summary Table

| Layer | First opinion | Second opinion | Third opinion | Verifiable today | Notes |
|-------|--------------|----------------|---------------|-----------------|-----|
| Diagnosis | Monitoring alert | Agent hypothesis | Human at gate | Yes — alert stored as `trigger_context` | Also backed by a deterministic, non-opinion check — see [§1](#1-layer-1-diagnosis) |
| Remediation | Agent plan | Human gate approval | — | Yes | None structural |
| Playbook improvement | LLM suggest-update | LLM Judge | Human | Yes — judge accuracy now tracked | `vault judge-accuracy` |
| Confidence | Agent CONFIDENCE: | Calibration bands | — | Yes — data-quality banner surfaces thin human coverage | Meaningfulness still depends on feedback composition |
| Stability | Single run | N-run cert | — | Yes — model is part of the cert's primary key | — |

---

## 8. See also

| Document | What it covers |
|----------|----------------|
| [INFORMED_CONSENT.md](INFORMED_CONSENT.md) | The gate in detail — informed, consent, right to refuse |
| [JUDGMENT_LAYER.md](JUDGMENT_LAYER.md) | When the second opinion layer fails: the manual improvement path |
| [LLM_AS_JUDGE.md](LLM_AS_JUDGE.md) | How the Judge works; APPROVE/NEEDS_REVIEW/REJECT schema |
| [AIGOVERNANCE.md §1.1](AIGOVERNANCE.md#11-llm-fabrication-detection) | The three LLM fabrication-detection layers — a related but distinct concept from the opinions on this page: checks for whether a claim is *true*, not checks for whether a conclusion is *good* |
| [OBJECTIVE_EVIDENCE.md](OBJECTIVE_EVIDENCE.md) | The deterministic, code-derived backstop referenced in Layer 1 |
| [VAULT.md](VAULT.md) | vault calibration, vault accuracy, vault versions |
| [CONSISTENCY.md](CONSISTENCY.md) | Stability certification — how STABLE(N) is earned |
| [CUSTOMER_RIGHTS.md](CUSTOMER_RIGHTS.md) | The rights that depend on this layer working correctly |
