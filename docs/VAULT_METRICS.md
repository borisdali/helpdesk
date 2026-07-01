# aiHelpDesk Vault as a Learning Signal

The most common question after deploying an AI SRE system is this: **does it make things any better?**

A resolution rate tells you whether the system is working. It does not tell you whether it is improving. 

A 75% resolution rate this month and 75% last month could mean the playbook is perfectly stable. This is important, because without this stability, your AI system is not reproducable (in diagnosis and remediation) and can't be trusted. In fact, we strongly advocate for this stability and provide tools to esure this stability, see [here](CONSISTENCY.md) for details. 

But it's only half of the story. Because that consistency could mean that a playbook succeeded on different faults, masked regressions on others and got lucky with timing. There are a lot of possible underlying permutations and, well, the numbers look the same either way.

We should do better. And so aiHelpDesk [Vault](VAULT.md) answers the "is it getting better" question concretely, across three dimensions, tracked per playbook version so you can see the before-and-after of every improvement cycle.

---

## Table of Contents

1. [The Three Learning Metrics](#the-three-learning-metrics)
   - [1. Step count: is the agent becoming more direct?](#1-step-count-is-the-agent-becoming-more-direct)
   - [2. Recovery time: is the system responding faster?](#2-recovery-time-is-the-system-responding-faster)
   - [3. Approach appropriateness: did the agent fix it elegantly?](#3-approach-appropriateness-did-the-agent-fix-it-elegantly)
2. [Reading the Learning Signal in `vault list`](#reading-the-learning-signal-in-vault-list)
3. [Reading the Per-Version Table in `vault versions`](#reading-the-per-version-table-in-vault-versions)
4. [The Loop: How Metrics Drive the Next Version](#the-loop-how-metrics-drive-the-next-version)
5. [A Concrete Example: Cache Miss Remediation](#a-concrete-example-cache-miss-remediation)
6. [What "Getting Better" Actually Means](#what-getting-better-actually-means)
7. [Connecting to Other Vault Signals](#connecting-to-other-vault-signals)
8. [See Also](#see-also)

---

## The Three Learning Metrics

### 1. Step count: is the agent becoming more direct?

Every tool call during a remediation run is recorded as a step in `playbook_run_steps`. The average step count per version tells you whether the agent is converging more efficiently on the fix:

- **Dropping step count** after a `vault suggest-update` → the new playbook guidance is more targeted; the agent wastes fewer calls ruling out wrong hypotheses.
- **Rising step count** → the playbook may be underspecified or the fault is occurring in a new configuration the guidance didn't anticipate.

Step count is a proxy for the quality of the playbook's diagnostic framing, not just the raw outcome.

### 2. Recovery time: is the system responding faster?

The wall-clock time from `started_at` to `completed_at` on a completed remediation run, averaged per version. This captures not just tool call count but the latency of each call, the number of wait loops and any back-and-forth the agent needed to confirm the fix:

- **Shrinking recovery time** → faster tool execution paths, less back-and-forth, cleaner remediation sequence.
- **Growing recovery time** → look for new approval gate delays, heavier verification queries or model latency regression.

### 3. Approach appropriateness: did the agent fix it elegantly?

`verify_sql` tells you whether the problem was fixed. It cannot tell you whether the remediation approach was appropriate — whether the agent used the right tools, avoided unnecessary blast radius and didn't introduce collateral damage.

Operator feedback after each run captures this: **"Was the remediation approach appropriate? [y/n]"** at the post-incident prompt or at the gate. The fraction of "yes" verdicts, per version, is the approach appropriateness rate. This is the subjective quality signal that automated verification cannot replace.

---

## Reading the Learning Signal in `vault list`

`vault list` shows the full fault catalog. When a remediation playbook has two or more versions with run data, the per-version trend appears inline:

```
FAULT                            PLATFORM   DIAG PLAYBOOK              REMED PLAYBOOK             LAST TEST              STABLE         INCIDENTS
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
db-high-cache-miss               any        pbs_cache_miss_triage      pbs_cache_miss_remediate   2026-06-28  PASS       STABLE(5)      3 runs  100% resolved  4.0 steps  8s recovery  last: 2026-06-28
    v1.1 *  3r  100%  4.0 steps  8s recovery  100% approach OK
    v1.0    5r   60%  6.2 steps  42s recovery
```

The `*` marks the currently active version. The two rows tell the whole story: v1.0 ran 5 times, resolved 60% of the time, averaged 6.2 steps and 42 seconds; v1.1 runs 3 times, resolves every time, takes 4 steps and 8 seconds. The improvement loop worked.

When more than two versions exist, the two most recent are shown and a `→ vault versions <series>` pointer appears for the full history.

---

## Reading the Per-Version Table in `vault versions`

`vault versions <fault-id>` shows the complete version history for a playbook series:

```
Version stats for pbs_cache_miss_remediate — 2 version(s)

VERSION     RUNS    TRANSITIONED  AVG STEPS   AVG TIME    AVG DIAG   AVG REMED  APPROACH OK
─────────────────────────────────────────────────────────────────────────────────────────────
1.0          5      60%           6.2         42s         72%        –          60%
1.1  *       3      100%          4.0         8s          91%        85%        100%

* = currently active version
```

| Column | What it measures |
|--------|-----------------|
| `TRANSITIONED` | Fraction of runs that successfully transitioned to the next phase (resolution or escalation) — the primary success signal |
| `AVG STEPS` | Mean tool calls per run; lower is more efficient |
| `AVG TIME` | Mean wall-clock duration for completed runs |
| `AVG DIAG` | Average `diagnosis_score` from the LLM judge (automated evaluation) |
| `AVG REMED` | Average `remediation_score` from the LLM judge |
| `APPROACH OK` | Fraction of runs where operators rated the remediation approach as appropriate — the human quality signal |

`AVG DIAG` and `AVG REMED` are automated scores produced by an LLM judge that reads the agent's chain-of-thought alongside the known fault definition. `APPROACH OK` is operator-submitted feedback collected interactively after each run. These are independent signals: a run can score 100% on both automated dimensions while an operator notes collateral damage that the judge missed.

---

## The Loop: How Metrics Drive the Next Version

The learning signal is only useful if it connects to action. The improvement loop in aiHelpDesk closes automatically:

```
Fault occurs (real or injected)
      │
      ▼
Agent diagnoses + remediates using active playbook version
      │
      ├─► Metrics recorded: step count, recovery time, eval scores
      │
      └─► Operator submits feedback at gate or post-incident:
              "Was the diagnosis correct?"
              "Was the remediation approach appropriate?"
                    │
                    ▼
          vault accuracy → shows accuracy rate by version
          vault versions → shows per-version trend
                    │
                    ▼ (when metrics suggest improvement is possible)
          vault suggest-update <series>
              → reads the successful incident trace
              → proposes a new playbook version
              → operator reviews + activates
                    │
                    ▼
          Next runs use v1.1 — metrics tracked separately
          vault list now shows v1.0 → v1.1 comparison
```

A single improvement cycle — one fault, one feedback submission, one `suggest-update` review — produces a before-and-after comparison that is permanently recorded and visible to every operator on the team.

---

## A Concrete Example: Cache Miss Remediation

Here is a real improvement cycle for `db-high-cache-miss` through the `pbs_cache_miss_remediate` playbook:

**v1.0 — initial version**

The playbook guidance described the problem but did not specify which `pg_stat_bgwriter` metric to check first. The agent spent 2-3 tool calls confirming the symptom before acting.

```
vault versions db-high-cache-miss

VERSION     RUNS    TRANSITIONED  AVG STEPS   AVG TIME    APPROACH OK
v1.0         5      60%           6.2         42s         60%
```

Two of the five runs failed to transition (the agent stopped short of running `ALTER SYSTEM` after confirming the cache miss was transient). Three operators marked the approach as appropriate; two noted that the agent had queried `pg_stat_statements` unnecessarily.

**The suggest-update cycle**

```bash
faulttest vault suggest-update pbs_cache_miss_remediate \
  --gateway https://gateway.internal \
  --api-key $KEY
```

The gateway reads the successful traces and proposes a v1.1 draft that adds explicit guidance: check `blks_hit / (blks_hit + blks_read)` first; skip `pg_stat_statements` unless the ratio has been degraded for more than 5 minutes.

After review and activation:

**v1.1 — after suggest-update**

```
VERSION     RUNS    TRANSITIONED  AVG STEPS   AVG TIME    APPROACH OK
v1.1  *      3      100%          4.0         8s          100%
```

Fewer steps. Faster. Every operator marked the approach as appropriate. The improvement is visible, permanent and tied to the specific playbook version that introduced it.

---

## What "Getting Better" Actually Means

A system that is genuinely improving shows all three signals moving together:

| Signal | Trending correctly | Concerning |
|--------|--------------------|------------|
| Step count | Decreasing | Stable or increasing after a version update |
| Recovery time | Decreasing | Spikes after a model or infra change |
| Approach rate | Increasing | Below 70% sustained — the agent is technically resolving but operators aren't satisfied |

A resolution rate alone cannot distinguish these cases. A playbook that always runs 8 steps and always fixes the problem in 45 seconds has the same resolution rate as one that drops from 8 to 4 steps and 45 to 8 seconds after a version update. The Vault tracks the difference.

---

## Connecting to Other Vault Signals

The three learning metrics work alongside the existing Vault signals, not instead of them:

- **`vault accuracy`** — shows diagnosis accuracy (did the agent name the right root cause?) broken down by feedback time (at-gate vs. post-incident) and version. Accuracy is independent of resolution: a playbook can achieve 100% resolution while the agent's root-cause hypothesis is wrong — if the remediation step happened to fix the problem anyway.

- **`vault calibration`** — shows whether accuracy ratings are consistent across runs or noisy. Distinguishes a playbook that is genuinely accurate from one that got lucky on a small sample.

- **`vault drift`** — compares pass rates between the first and second halves of a history window. A playbook whose pass rate drops 30pp over 90 days has drifted — likely because the environment changed, not the playbook. Drift detection triggers investigation; the learning metrics tell you whether the investigation (and subsequent version update) restored performance.

Together, these signals give you a complete picture: the playbook is stable, the diagnosis is accurate, the approach is appropriate and the efficiency is improving. That is what "the AI SRE is getting better" means in measurable terms.

---

## See Also

- [VAULT.md](VAULT.md) — command reference, artifact lifecycle and the three paths into the Vault
- [VAULT_FEEDBACK_FLOW.md](VAULT_FEEDBACK_FLOW.md) — how operator feedback flows from gate prompt to accuracy rate
- [CONSISTENCY.md](CONSISTENCY.md) — the stability certification gate before a playbook enters live rotation
- [FAULTTEST.md](FAULTTEST.md) — fault injection testing, the primary source of per-version run data
