# aiHelpDesk Vault Feedback & Commands: Operational Guide

This document is the operational detail layer beneath the [Operational SRE/DBA Flywheel](VAULT.md#the-operational-sredba-flywheel). The flywheel shows the high-level loop. This document explains exactly where operator feedback enters that loop, what form it takes and which `vault` commands consume it to close the cycle.

The feedback model is the same whether the event is a real production incident or a simulated one, manufactred via aiHelpDesk [fault injection test](FAULTTEST.md) system: the gate schema, the endpoint, and the `run_feedback` table are shared. This document describes the **faulttest** path. For the equivalent steps in a live incident, see [PLAYBOOK_OPS.md §2.4](PLAYBOOK_OPS.md#24-step-4-provide-an-informed-consent-aka-reviewapprove-the-gate) (at-gate feedback) and [§2.6](PLAYBOOK_OPS.md#26-step-6-finish-the-remediation--provide-incident-feedback) (post-incident feedback).

The feedback loop described here is also the measurement layer of [Informed Consent](INFORMED_CONSENT.md): `vault accuracy` and `vault calibration` are what make the "Informed" part of that framework verifiable rather than just claimed.

---

## The lifecycle in one picture

```
faulttest run
    │
    ├─ 1. Inject fault
    │
    ├─ 2. Agent diagnoses + proposes remediation plan
    │        │
    │        └── [pending_gate] ──► GATE: operator reviews diagnosis + proposed steps
    │                                        │
    │                              ┌─────────┴────────────────────────────────────────┐
    │                              │  Q1: "Was the diagnosis correct?" [y/n]          │
    │                              │      → feedback_type=triage                      │
    │                              │        feedback_time=at_gate      ← best signal  │
    │                              │                                                  │
    │                              │  Q2: "Is the remediation approach appropriate?"  │
    │                              │      → feedback_type=remediation                 │
    │                              │        feedback_time=at_gate                     │
    │                              └─────────┬────────────────────────────────────────┘
    │                                        │
    ├─ 3. Remediation executes
    │
    ├─ 4. Recovery verified
    │        │
    │        └──► POST-RECOVERY PROMPTS (interactive only, requires --gateway)
    │                │
    │                ├─ "Was the diagnosis correct?" [y/n]
    │                │    → feedback_type=triage
    │                │      feedback_time=post_incident
    │                │
    │                └─ "Was the remediation approach appropriate?" [y/n]
    │                     → feedback_type=remediation
    │                       feedback_time=post_incident
    │
    └─ 5. Scores & history written
             │
             ├─ ~/.faulttest/history.json  (local, always)
             └─ auditd run_evaluation      (when --gateway is set)
```

---

## Feedback reference

All four combinations are implemented and captured by faulttest.

| # | `feedback_type` | `feedback_time` | Question asked | When captured |
|---|-----------------|-----------------|----------------|---------------|
| 1 | `triage` | `at_gate` | Was the diagnosis correct? | At the triage→remediation gate, **before** remediation runs |
| 2 | `triage` | `post_incident` | Was the diagnosis correct? | After recovery completes |
| 3 | `remediation` | `post_incident` | Was the remediation approach appropriate? | After recovery completes |
| 4 | `remediation` | `at_gate` | Is the remediation approach appropriate? | At the same gate as #1 — asked immediately after Q1, before remediation runs |

**Why `triage/at_gate` is the highest-quality signal for diagnosis:**  
It is captured before the operator knows whether remediation succeeded. Post-incident feedback is contaminated by outcome bias — an operator who just saw a 12-second recovery is more likely to confirm the diagnosis regardless of whether it was actually correct. At-gate feedback has no such bias, so `vault calibration` prefers it over post-incident when both exist for the same triage run.

**Why `remediation/post_incident` is the preferred signal for remediation calibration:**  
At-gate remediation feedback is a review of a plan. Post-incident remediation feedback reflects whether the plan actually worked. For calibrating `remediation_judge_score` against ground truth, the actual outcome is more informative than a pre-execution opinion, so `vault calibration` prefers post-incident when both exist for the same run.

**Why both types of post-incident feedback share `post_incident`:**  
They are captured in the same interactive session after recovery, but stored as separate records keyed by `(run_id, feedback_type, feedback_time)`. Both are anchored to the triage `run_id` — not the remediation `run_id` — so they can be joined with `run_evaluation` (which is also keyed on the triage run_id) without a cross-table join.

**Submitting feedback manually** (non-interactive / `--emit-and-wait` mode):

```bash
# triage/at_gate — include verdict_correct in the gate resolution body
curl -sX POST "$GATEWAY/api/v1/decisions/gate:$RUN_ID/resolve" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{"resolution":"approved","resolved_by":"ops@example.com",
       "verdict_correct":true,"verdict_notes":"PID 867 was the idle-in-tx blocker"}'

# triage/post_incident or remediation/post_incident — direct feedback endpoint
curl -sX POST "$GATEWAY/api/v1/fleet/playbook-runs/$RUN_ID/feedback" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  -d '{"feedback_type":"triage","feedback_time":"post_incident",
       "verdict_correct":true,"verdict_notes":"connection exhaustion confirmed"}'
```

---

## Vault command reference

| Command | Question answered | Data source | Uses feedback? | Requires gateway? |
|---------|------------------|-------------|:--------------:|:-----------------:|
| `vault drift` | Which faults are regressing over time? | Local `~/.faulttest/history.json` (default) or auditd via `--gateway` | Optional¹ | No (local) / Yes (gateway) |
| `vault versions` | Did a playbook version change help or hurt? | auditd `playbook_runs` + `run_evaluation` | No | Yes |
| `vault accuracy` | How often does the agent diagnose/remediate correctly? | `run_feedback` (triage + remediation) | Yes — triage + remediation | Yes |
| `vault calibration` | Can I trust the automated scores? | `run_evaluation` + `run_feedback` | Yes — triage + remediation | Yes |

¹ `vault drift` with `--gateway` reads fleet-wide run history from auditd (`fault-run-history` endpoint) instead of the local file, giving a multi-machine view. Without `--gateway` it reads local history and can optionally enrich drifted faults with triage accuracy from the gateway.

### Which feedback each command consumes

| Command | `triage/at_gate` | `triage/post_incident` | `remediation/at_gate` | `remediation/post_incident` |
|---------|:----------------:|:----------------------:|:---------------------:|:---------------------------:|
| `vault drift` | — | — | — | — |
| `vault versions` | — | — | — | — |
| `vault accuracy` | ✓ | ✓ | ✓ | ✓ |
| `vault calibration` | ✓ preferred (triage) | ✓ fallback (triage) | ✓ fallback (remediation) | ✓ preferred (remediation) |

---

## When to use which command

### Situation 1: Routine CI / scheduled regression check

**Run:** `vault drift`  
**Needs:** Nothing beyond local history — no gateway required.  
**How:** Run after each faulttest batch or on a weekly schedule.  
**Tells you:** Which faults have had a >20% pass-rate drop between the first and second halves of the window. These are the ones to investigate further.

```bash
faulttest vault drift --since-days 60
```

---

### Situation 2: A playbook was updated — did it improve things?

**Run:** `vault versions <fault-id>`  
**Needs:** Gateway + at least a few runs against the new version.  
**Tells you:** Per-version resolution rate, average step count, average recovery time, average automated diagnosis and remediation scores. No operator feedback required — all signal comes from automated faulttest scores.

```bash
faulttest vault versions db-lock-contention --gateway $GATEWAY --api-key $KEY
```

If the new version shows lower `AVG DIAG` or higher step count, roll it back and use `vault suggest-update` to generate a better proposal.

---

### Situation 3: How accurate is the agent's diagnosis, really?

**Run:** `vault accuracy <fault-id or series-id>`  
**Needs:** Gateway + triage feedback submitted (at-gate or post-incident).  
**Tells you:** How often operators confirm the diagnosis was correct — both overall and broken down by when they submitted the feedback (before vs after seeing remediation succeed).

```bash
faulttest vault accuracy db-lock-contention --gateway $GATEWAY --api-key $KEY
```

No args lists all faults with feedback, useful for a fleet-wide accuracy sweep:

```bash
faulttest vault accuracy --gateway $GATEWAY --api-key $KEY
```

The at-gate count is the more trustworthy signal. If post-incident accuracy is significantly higher than at-gate accuracy, operators may be confirming diagnoses they didn't fully read because remediation succeeded anyway.

---

### Situation 4: Are the automated scores trustworthy?

**Run:** `vault calibration [fault-id or series-id]`  
**Needs:** Gateway + both eval scores (faulttest run with `--gateway`) and operator feedback.  
**Tells you:** Whether the agent's self-reported confidence (`primary_confidence`, from the `CONFIDENCE:` field on its primary hypothesis) and the LLM judge's remediation score predict actual operator-confirmed correctness. Two sections — diagnosis calibration and, when remediation feedback exists, remediation calibration.

```bash
faulttest vault calibration --gateway $GATEWAY --api-key $KEY          # fleet-wide
faulttest vault calibration db-lock-contention --gateway $GATEWAY ...  # one fault
```

Runs where the agent did not emit a structured `HYPOTHESIS_1: ... | CONFIDENCE:` line are excluded from the diagnosis confidence bands and shown in a separate footnote as heuristic-only runs. These runs still contribute to `vault accuracy` (they have feedback), but cannot be placed in a confidence band.

Interpret the output:

| Label | Meaning | Action |
|-------|---------|--------|
| `WELL_CALIBRATED` | Confidence band predicts accuracy within ±10 percentage points | No change needed |
| `OVERCONFIDENT` | Agent reports high confidence but operators disagree more than expected | Lower pass threshold or strengthen hypothesis guidance |
| `UNDERCONFIDENT` | Agent reports low confidence but operators agree more than expected | Raise pass threshold; agent is being too conservative |
| `INSUFFICIENT_DATA` | Fewer than 3 runs in this band | Collect more feedback before acting |

---

## Recommended weekly workflow

```
Monday  — faulttest run (CI job, all external faults)
                │
                ├─ Interactive: answer at-gate + post-recovery prompts
                │  or --emit-and-wait: resolve gates via Decision Hub
                │
                └─ Automated: scores written to auditd automatically

Wednesday — review
  1. vault drift           → any new regressions this week?
  2. vault accuracy        → is at-gate accuracy holding?
  3. vault calibration     → are automated scores still trustworthy?
  4. vault versions        → if a playbook was updated, did it help?

When drift or accuracy degrades:
  5. vault incidents       → find the specific run IDs
  6. vault suggest-update  → propose a guidance update from a recent good trace
```

---

## Quick-reference cheat-sheet

```
FEEDBACK — what to submit and when
───────────────────────────────────────────────────────────────────────────
  At the escalation gate (before remediation)
    → "Was the diagnosis correct?"           triage / at_gate      [best signal]
    → "Is the remediation approach right?"   remediation / at_gate [best signal]

  After recovery (interactive faulttest, or via API)
    → "Was the diagnosis correct?"           triage / post_incident
    → "Was the remediation appropriate?"     remediation / post_incident

COMMANDS — what question each answers
───────────────────────────────────────────────────────────────────────────
  vault drift          Which faults are regressing?       (local default; --gateway for fleet-wide)
  vault versions       Did the playbook version improve?  (auditd, no feedback needed)
  vault accuracy       Are diagnosis/remediation correct? (triage + remediation feedback)
  vault calibration    Can I trust the scores?            (eval + triage + remediation feedback)

DEPENDENCY CHAIN
───────────────────────────────────────────────────────────────────────────
  drift  →  identifies which fault to look at
  versions  →  correlates regression to a playbook version
  accuracy  →  checks whether diagnosis and remediation quality actually dropped
  calibration  →  validates whether the scores you're reading are reliable
```
