# aiHelpDesk Attribution-Aware Stability Certs

A stability cert records whether a [playbook's](PLAYBOOKS.md) *outcome* is consistent across N runs. Starting with aiHelpDesk release [v0.21.0](https://github.com/borisdali/helpdesk/releases/tag/v0.21.0), it also records whether the playbook's *conclusion* is [consistent](CONSISTENCY.md). That is, did the agent tell the same root-cause story each time or just pass the scorer by different paths? In our experience, these are distinct signals that can diverge in ways that matter for production use.

---

## Table of Contents

1. [The three named stabilities](#1-the-three-named-stabilities)
2. [How attribution classification works](#2-how-attribution-classification-works)
3. [New cert fields](#3-new-cert-fields)
4. [Taxonomy versioning](#4-taxonomy-versioning)
5. [Reading attribution in cert output](#5-reading-attribution-in-cert-output)
6. [Authoring root_cause_classes](#6-authoring-root_cause_classes)
7. [Three suspects for any cert regression](#7-three-suspects-for-any-cert-regression)
8. [Relationship to other quality signals](#8-relationship-to-other-quality-signals)

---

## 1. The three named stabilities

`STABLE(N)` previously collapsed three distinct guarantees into one number:

| Stability | Question | Instrument |
|-----------|----------|------------|
| **Outcome** | Did the agent pass the scorer ≥80% of the time? | Pass rate + confidence spread (existing cert criteria) |
| **Conclusion** | Did the agent reach the same root-cause attribution each time? | Attribution classifier against `root_cause_classes` |
| **Evaluation** | Were the judge scores consistent across same-attribution runs? | Standard deviation of `diagnosis_score` across runs |

These can diverge. Consider a cert where 3 runs say "connection-pool-saturation" and 2 runs say "connection-pool-leak". All 5 pass the scorer because both attributions produce valid diagnostic steps. The pass rate is 100%, the cert is STABLE by outcome. But the agent is not telling the same story. 

If you deploy a new model and that ratio flips to 1-and-4, you have no way to distinguish a meaningful shift from random variance. Attribution consistency is what makes that shift visible.

The key decomposition:
- **Same attribution + consistent judge scores** → clean data; the cert is trustworthy
- **Same attribution + varying judge scores** → judge noise (possible; measure separately)
- **Different attributions + identical structured scores** → rubric gap; the scorer is not distinguishing the two stories
- **UNKNOWN attribution** → classifier could not map the response; check whether a new class is needed (see §4)

---

## 2. How attribution classification works

### The `root_cause_classes` field

Each triage playbook carries a versioned, closed list of plausible root-cause attributions:

```yaml
root_cause_classes:
  version: "1.0"
  classes:
    - connection-pool-saturation
    - connection-pool-leak
    - connection-limit-misconfiguration
    - idle-connection-accumulation
```

This list is authored alongside `symptoms` and `escalation` — by the same person, at the same time, for the same fault class. It is the definitive taxonomy for that playbook's problem domain.

Remediation playbooks do not carry `root_cause_classes`. The classifier is only meaningful for triage — remediation outcomes are binary (recovered / not recovered), not a taxonomy of root-cause stories.

### The classifier

At cert time, after all N inject→diagnose→teardown cycles complete, `faulttest` classifies each run's diagnostic response text against the playbook's class list. The classifier:

1. Sends the response text and the closed class list to a constrained LLM call (Claude Haiku, temperature=0)
2. Instructs the model to return exactly one string from the list or `UNKNOWN`
3. Validates the response against the allowed list — any non-matching output is treated as `UNKNOWN`

The call is cheap (Haiku, short prompt), post-hoc (never during live diagnosis) and non-blocking: if no API key is available, `newAttributionCompleter` returns nil and all attributions are recorded as `UNKNOWN`. The cert is always posted regardless of classifier outcome.

### UNKNOWN is a signal, not a failure

When the classifier returns `UNKNOWN` it means the response text did not clearly match any class in the current list. Do not stretch to the nearest bucket. The correct response is to decide:

- **Does this represent a real root cause not in the list?** → add a new class (minor version bump; see §4)
- **Is the response text unusual but the fault type is unchanged?** → check the classifier prompt, not the taxonomy

A pattern of `UNKNOWN` on a specific fault suggests the taxonomy is incomplete. A pattern of `UNKNOWN` across all faults during a cert run suggests the API key is missing or the completer failed.

---

## 3. New cert fields

Five new columns are added to `fault_stability_cert` in v0.21.0. They are stored alongside the existing outcome-stability fields and populated after each `faulttest run --repeat N` with `--gateway` and `HELPDESK_API_KEY` set.

| Field | Type | Description |
|-------|------|-------------|
| `primary_attribution` | string | Plurality class across all N runs. `UNKNOWN` when no class matched in a majority of runs or when the classifier was not available. |
| `attribution_consistent` | bool | `true` when all N runs were attributed to the same non-`UNKNOWN` class. `false` when runs split across two or more classes. |
| `attribution_distribution` | JSON object | Per-class run count: `{"connection-pool-saturation":3,"connection-pool-leak":2}`. Includes `UNKNOWN` counts if any. |
| `judge_spread` | float | Standard deviation of `diagnosis_score` across all N runs. Only meaningful when `attribution_consistent=true` — spread across different attributions measures something other than judge noise. |
| `taxonomy_version` | string | Semver string from `root_cause_classes.version` at cert time. Stored so `cert-compare` can detect when two certs were produced against different taxonomy versions. |

These fields are omitted from the cert display when `primary_attribution` is empty or `UNKNOWN` (e.g. when `root_cause_classes` is not set on the playbook or no API key was available at cert time). The cert behaves identically to pre-v0.21.0 in that case.

---

## 4. Taxonomy versioning

The `root_cause_classes.version` field follows semver. The rules are:

| Change type | Version bump | cert-compare behaviour |
|-------------|-------------|------------------------|
| New class added | minor (1.0 → 1.1) | no warning; old certs remain comparable — existing class labels are unchanged |
| Class renamed, split or merged | major (1.x → 2.0) | `⚠ TAXONOMY MAJOR` in cert-compare; attribution columns flagged non-comparable |
| Class removed | major (1.x → 2.0) | same as above |

Minor bumps are additive. A cert produced under taxonomy 1.0 is still valid under 1.1 because all labels from 1.0 exist unchanged in 1.1. The new label simply was not yet available when the earlier cert ran.

Major bumps are breaking. If "connection-pool-saturation" from v1 was split into "pool-size-misconfiguration" and "pool-exhaustion" in v2, a run that was `connection-pool-saturation` under v1 maps ambiguously to v2. The old cert's attribution column is no longer meaningful. `cert-compare` surfaces this rather than silently comparing incompatible labels.

**Validator enforcement.** The playbook seeder validates that changes to `root_cause_classes` in any YAML file are consistent with the version bump present. If an existing class is removed, renamed or split without a major version bump, the seeder logs a warning at startup. This prevents accidental breaking changes from appearing as minor bumps.

### Discovery events

When the classifier consistently returns `UNKNOWN` for a particular fault, treat it as a discovery event:

1. Inspect several `UNKNOWN` runs manually. Is there a clear root-cause pattern the taxonomy does not cover?
2. If yes: add a new class, increment the minor version (`1.0 → 1.1`), rebuild and re-certify.
3. If no: the response text is too ambiguous to classify — improve the playbook guidance so the agent emits clearer FINDINGS text.

Do not add a catch-all class like "other" — it erases the signal that `UNKNOWN` provides.

---

## 5. Reading attribution in cert output

### `vault list` — STABLE column

Before v0.21.0:
```
db-max-connections   pbs_max_conn_triage   STABLE(5)
```

After v0.21.0, when attribution is available and consistent:
```
db-max-connections   pbs_max_conn_triage   STABLE(5)   attr=connection-pool-saturation
```

When attribution is split across runs:
```
db-max-connections   pbs_max_conn_triage   STABLE(5)   attr=connection-pool-saturation(split)
⚠ CONCLUSION UNSTABLE  (3× connection-pool-saturation, 2× connection-pool-leak)
```

A split cert is outcome-stable but conclusion-unstable. It should trigger playbook review before using the cert as a model-gating signal — the agent is not reliably telling the same story.

The `attr=` label only appears when `root_cause_classes` is populated on the triage playbook and `HELPDESK_API_KEY` (or `--judge-api-key`) was available during the certification run. When absent, the STABLE column output is identical to pre-v0.21.0.

### `vault accuracy` — cert detail block

When called with a fault ID, `vault accuracy` includes the attribution block in the cert detail:

```
Triage consistency
  Fault          : db-max-connections  (Max connections exhausted)
  Verdict        : STABLE
  Runs           : 5
  Pass rate      : 100%
  Conf range     : 8pp
  Attribution    : connection-pool-saturation  (consistent, 5/5)
  Judge spread   : 0.04  (σ of diagnosis_score, consistent runs)
  Taxonomy       : 1.0
  Playbook       : pbs_connection_triage
  Diagnosis model: claude-sonnet-4-6
  Tested at      : 2026-06-28 03:14 UTC
```

When attribution is split:
```
  Attribution    : connection-pool-saturation  (split: 3× saturation, 2× leak)
  Judge spread   : —  (not computed; split attribution)
```

`Judge spread` is only shown when `attribution_consistent=true` — std dev across different root-cause stories measures something other than judge noise and is not reported.

### `vault cert-compare` — taxonomy warning

When two certs for the same fault have different taxonomy major versions:

```
db-max-connections   STABLE @ t1.x   STABLE @ t2.0   ⚠ TAXONOMY MAJOR
  attribution comparison invalid — taxonomy v1→v2 was a breaking change (split/merge/rename)
  re-run cert suite under taxonomy 2.0 before using for model gating
```

The `⚠ TAXONOMY MAJOR` flag does not affect the STABLE/UNSTABLE comparison — that reflects outcome stability and is always valid. It flags only that the attribution labels are not directly comparable. When the taxonomy major has changed, re-certify both models under the new taxonomy before drawing conclusions from the attribution columns.

When both certs share the same major version (same major, any minor), `cert-compare` shows attribution normally and stays quiet:

```
db-max-connections   STABLE        STABLE        —
  attr: connection-pool-saturation → connection-pool-saturation  (consistent)
```

---

## 6. Authoring `root_cause_classes`

### When to add

Every triage playbook should have `root_cause_classes`. If you are authoring a new triage playbook or updating an existing one, add (or update) the field at the same time as `symptoms` and `escalation`.

Remediation playbooks **must not** have `root_cause_classes`. The classifier is not applicable — remediation outcomes are pass/fail, not a taxonomy.

### Size and naming

| Guideline | Rationale |
|-----------|-----------|
| 3–6 classes; maximum 7 | Fewer than 3 is too coarse (everything lumps into one bucket); more than 7 makes the classifier prompt unwieldy and increases `UNKNOWN` rates |
| kebab-case strings | `connection-pool-saturation`, not `ConnectionPoolSaturation` or `connection pool saturation` |
| Mutually exclusive | Each run should clearly map to one class. Overlapping classes produce split certs that don't reflect real ambiguity |
| Concrete, not generic | `autovacuum-blocked-by-long-transaction`, not `autovacuum-issue`. The classifier needs enough signal to distinguish |
| Cover the real failure modes | Start from your escalation conditions and observed incidents. If a fault type never appears in production, don't add it |

### Field placement in YAML

`root_cause_classes` is a top-level field in the playbook YAML, at the same level as `symptoms` and `escalation`:

```yaml
name: Connection Pool Triage
version: "1.3"
playbook_type: triage
symptoms:
  - "pg_stat_activity shows connections near max_connections"
escalation:
  - "connections > max_connections with no idle sessions to terminate"
root_cause_classes:
  version: "1.0"
  classes:
    - connection-pool-saturation
    - connection-pool-leak
    - connection-limit-misconfiguration
    - idle-connection-accumulation
guidance: |
  ...
```

When bumping `root_cause_classes.version`, also bump the playbook `version` field and follow the [system playbook update procedure](PLAYBOOKS.md#updating-a-system-playbook) so the seeder applies the change on next startup.

---

## 7. Three suspects for any cert regression

When `vault cert-compare` shows a regression — a fault that was STABLE under the old model and UNSTABLE under the new one — exactly one of three things is responsible. All three are versioned fields in the cert row:

1. **Model change** — `diagnosis_model` field. The new model weights produce different output distributions. This is the most common cause and the one `cert-compare` was built to detect.

2. **Playbook change** — `playbook_id` / `version`. New guidance text, modified escalation conditions or a different tool ordering in the playbook can shift pass rates independently of the model. Check whether a new playbook version was activated between the two cert runs.

3. **Taxonomy major bump** — `taxonomy_version` field. A breaking taxonomy change (class split/merge/rename) means the old cert's attribution column is not comparable. The outcome stability verdict is still valid, but a cert under taxonomy v2 may classify runs differently from v1 — which can expose runs that previously passed as UNKNOWN under the new classes. This shows up as `⚠ TAXONOMY MAJOR` in cert-compare.

No other variable should be in play: `faulttest run --repeat N` fixes the fault injection (same SQL, same teardown), the evaluation rubric (same keyword and tool-ordering checks) and the judge prompt (same model, same template). If you see a regression and none of the three suspects has changed, check whether the infrastructure state drifted (different DB load, different connection pool size) between cert runs.

---

## 8. Relationship to other quality signals

| Signal | Question answered | When it runs |
|--------|-------------------|--------------|
| **[Consistency cert](CONSISTENCY.md)** | Is the agent's outcome stable enough to trust in production? | Pre-promotion gate; re-run on model/playbook change |
| **Attribution certs** (this doc) | Is the agent reaching the same conclusion each time or just passing? | After each certification run (requires `HELPDESK_API_KEY` and `root_cause_classes`) |
| **[LLM-as-judge](LLM_AS_JUDGE.md)** | Is the diagnosis semantically correct on this specific run? | During faulttest runs (opt-in `--judge`) |
| **[Vault calibration](VAULT.md#vault-calibration)** | When the agent says 90%, is it right 90% of the time? | After accumulating ≥10 runs with operator feedback |
| **[Vault accuracy](VAULT.md#vault-accuracy)** | What fraction of diagnoses are confirmed correct by operators? | Continuously from production incidents and faulttest gateway runs |

**The dependency chain:** attribution consistency is a prerequisite for calibration to be meaningful at the class level. A fault where the agent alternates between two attributions produces two different confidence distributions. `vault calibration` will see a single mixed band and cannot distinguish miscalibration from attribution variance. Attribution consistency guarantees that the confidence band for a given class reflects a single, repeatable story — not a blend of two.

Outcome stability (consistency cert) is still a prerequisite for attribution consistency to be reliable: a cert on an UNSTABLE playbook has high run-to-run variance in the response text, which makes the classifier results noisy. Certify STABLE first; then read the attribution columns.
