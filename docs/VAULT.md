# aiHelpDesk Vault

The Vault is aiHelpDesk's institutional memory for operational knowledge. It is where every [Playbook](PLAYBOOKS.md) lives, where every [Incident](INCIDENTS.md) trace lands, and where the library of known fault→remedy pairings grows. Automatically and with human approval at the gate.

A traditional runbook is a static procedure — a fixed sequence of steps written once and followed literally. An aiHelpDesk [Playbook](PLAYBOOKS.md) is fundamentally different: it encodes strategic **intent** and expert **knowledge** that the fleet planner uses to generate an execution plan dynamically, against the current state of your infrastructure and tool catalog. The same Playbook produces different steps when your database configuration differs, when new tools are available, or when the environment has changed. This is what "never a stale script" means in practice.

The Vault is the library where these Playbooks live. Tracked, versioned, and continuously improved as your infrastructure, applications that make use of it and agents evolve.

---

## Table of Contents

1. [The Operational SRE/DBA Flywheel](#the-operational-sredba-flywheel)
2. [How Artifacts Enter the Vault](#how-artifacts-enter-the-vault)
   - [1. System Playbooks (shipped)](#1-system-playbooks-shipped)
   - [2. The incident agent (auto-suggest on resolution)](#2-the-incident-agent-auto-suggest-on-resolution)
   - [3. faulttest auto-suggest (on remediation pass)](#3-faulttest-auto-suggest-on-remediation-pass)
3. [The Artifact Lifecycle](#the-artifact-lifecycle)
4. [Vault Commands](#vault-commands)
   - [vault list](#vault-list)
   - [vault accuracy](#vault-accuracy)
   - [vault incidents](#vault-incidents)
   - [vault status](#vault-status)
   - [vault drift](#vault-drift)
   - [vault versions](#vault-versions)
   - [vault calibration](#vault-calibration)
   - [vault suggest](#vault-suggest)
   - [vault suggest-update](#vault-suggest-update)
5. [The `from-trace` Endpoint](#the-from-trace-endpoint)
6. [Reviewing and Activating Drafts](#reviewing-and-activating-drafts)
7. [Three Customer Workflows](#three-customer-workflows)
   - [1. Onboarding — linking your first Playbooks](#1-onboarding--linking-your-first-playbooks)
   - [2. Playbook acceptance — review and approve auto-generated drafts](#2-playbook-acceptance--review-and-approve-auto-generated-drafts)
   - [3. Regression monitoring — catching drift before it becomes an incident](#3-regression-monitoring--catching-drift-before-it-becomes-an-incident)
8. [Connection to Other Docs](#connection-to-other-docs)

---

## The Operational SRE/DBA Flywheel

The Vault is the engine of a feedback loop that tightens with every incident:

```
  ┌────────────────────────────────────────────────────────────────────────────────┐
  │                                                                                │
  │              ┌─── CONSISTENCY GATE (pre-promotion) ──────────────┐             │
  │              │  faulttest run --repeat N                         │ STABLE      │
  │  author  ──► │  inject → diagnose → score (×N) → stability cert  │ ──────────► │
  │  Playbook    │                                 see CONSISTENCY.md│ UNSTABLE ─► │
  │              └───────────────────────────────────────────────────┘    fix      │
  │                                                                                │
  │         Fault               Agent diagnoses           Playbook                 │
  │   (injected or real) ───► + chain of thought ──────► remediates                │
  │          ▲                  captured                    │                      │
  │          │                                              │                      │
  │          │                           Operator confirms  ▼                      │
  │   Library improves  ◄── Human      ◄── diagnosis     Draft auto-saved          │
  │   (accuracy rises)      approves       correct?      to Vault                  │
  │                         (Vault review)  ↓                                      │
  │                                    accuracy_rate                               │
  │                                    feeds vault calibration                     │
  └────────────────────────────────────────────────────────────────────────────────┘
```

The loop closes at three levels: 

First, there is a **Consistency gate**: before a Playbook
enters live rotation, it is certified STABLE by running the same fault N times and verifying
that both pass rate (≥80%) and confidence spread (≤30pp) are within bounds, see [here](CONSISTENCY.md) for the full treatment. 

Second, there is a carefully tracked **Resolution rate** (does the Playbook fix the problem?). 

Third, there is an **Accuracy rate** (does the agent identify the *right* root cause?). Accuracy is measured separately from
resolution because a Playbook can achieve 100% resolution rate while the agent's diagnosis is
wrong, if the remediation step happens to fix the problem anyway. 

Distinguishing these three signals is what makes the Vault's knowledge meaningful rather than just empirically successful.

See [Life of an Incident](PLAYBOOKS.md#life-of-an-incident) for a full walkthrough of how a single incident contributes to both signals.

**Two selling points drive this loop:**

1. **Agents that act, not just advise.** The governed actuation arm is the baseline foundation of aiHelpDesk. In addition to the eight-module [AI Governance](AIGOVERNANCE.md) system, featuring the multi-layer AI anti-hallucinations safeguards, aiHelpDesk is equipped with a formal [Tool Registry](TOOL_REGISTRY.md), [Playbooks](PLAYBOOKS.md), [Fleet Management](FLEET.md), [Policy Engine](AIGOVERNANCE.md#3-policy-engine), [Blast Radius Guardrails](AIGOVERNANCE.md#5-guardrails), not to mentioned the hardened system prompts (through defensive engineering technique) - all to ensure  that the remedal actions are carried out safely on your real infrastructure. 

    aiHelpDesk doesn't give a hypethetical advise that it carries no responsbility for. It acts on its proposed course of action through a Playbook (with the human operator's approval, step-by-step or autonomously) and reports the results back via the feedback loop that keeps the Playbooks current and improving after every incident, real or injected.

2. **Institutional memory that compounds.** Every resolved incident automatically proposes a Playbook draft. Every faulttest pass with remediation auto-saves a draft. Human operators review and activate. The library grows. The next similar incident is handled faster, with higher confidence, because someone already did the hard thinking.

The Vault is the mechanism that makes this second point real. Without it, every operator repeats the same diagnostic steps from scratch. And in a different way, with the different mistakes. With it, the hard-won knowledge of how to fix `db-max-connections` or `db-lock-contention` formally accumulates in one place, versioned, with a known track record — and with a measurable diagnosis accuracy rate that tells you not just whether the system is *fixing* problems but whether it is *understanding* them correctly.

---

## How Artifacts Enter the Vault

There are three paths by which operational knowledge enters the Vault:

### 1. System Playbooks (shipped)

At aiHelpDesk Beta we ship 7 expert-authored system Playbooks that are seeded into auditd on startup. They cover the most common PostgreSQL triage scenarios out of the box — vacuum, slow queries, connection exhaustion, replication lag, database-down recovery, and PITR restore.

These are read-only in the API (`PUT`/`DELETE` return 400) but can be cloned into a new custom version in the same series. See [PLAYBOOKS.md](PLAYBOOKS.md) for the full list and schema.

### 2. The incident agent (auto-suggest on resolution)

When your aiHelpDesk incident agent calls `create_incident_bundle` with `outcome="resolved"` or `outcome="escalated"`, and `HELPDESK_GATEWAY_URL` is configured, a Playbook draft is **automatically synthesised** from the audit trace of that incident and saved to the Vault as an inactive draft.

The agent signals resolution naturally:

```json
{
  "tool": "create_incident_bundle",
  "args": {
    "infra_key": "global-corp-db",
    "description": "Max connections exhausted — resolved by restarting PgBouncer",
    "connection_string": "host=prod-db ...",
    "outcome": "resolved"
  }
}
```

The Gateway's `from-trace` endpoint synthesises the draft from the audit trail of tool calls made during the investigation. The result is returned in the tool response:

```json
{
  "incident_id": "a3f9b2c1",
  "bundle_path": "/incidents/a3f9b2c1.tar.gz",
  "layers": ["database", "os", "storage"],
  "playbook_draft": "name: Connection Pool Saturation\n...",
  "playbook_id": "pb_a3f9b2c1"
}
```

`playbook_id` is the Vault identifier of the persisted draft. When auditd is not configured on the Gateway, the draft is returned in `playbook_draft` only (no persistence) and `playbook_id` is empty.

**What `outcome` means:**

| Value | Effect |
|-------|--------|
| `"resolved"` | Incident closed successfully — draft synthesised from the winning approach |
| `"escalated"` | Escalated to a human — draft captures the diagnostic steps taken before escalation |
| `""` (empty) | Still investigating — no draft generated |

The `generate_playbook_draft: true` field is preserved for backward compatibility but `outcome` is the preferred mechanism going forward.

### 3. faulttest auto-suggest (on remediation pass)

When `faulttest run --remediate` succeeds for a fault — meaning the injected failure was reproduced, the Playbook was triggered, and the database recovered — faulttest automatically calls the Gateway's `from-trace` endpoint and prints the result:

```
Remediation: RECOVERED in 4.2s (score: 100%)
Vault: draft saved → pb_faulttest_a1b2c3 (activate with 'faulttest Vault list')
```

The trace ID used is a pseudo-ID (`faulttest-{run-id}-{fault-id}`) since faulttest operates from outside the audit event stream. When the Gateway's auditd integration is configured, the synthesis has full access to the tool call evidence and produces a higher-quality draft. When auditd is not configured, the LLM synthesises from the fault metadata alone.

In either case, the draft lands as `source="generated"`, `is_active=false`. No action is taken on your live infrastructure until a human activates it.

---

## The Artifact Lifecycle

Every draft that enters the Vault — regardless of path — follows the same lifecycle:

```
  generated / imported / manual
         │
         ▼
    [ source="generated"    ]   ← auto-saved by from-trace or incident agent
    [ source="imported"     ]   ← imported via API from Markdown/YAML/Ansible
    [ source="manual"       ]   ← created directly via API
    [ is_active = false     ]
         │
         │   operator reviews draft
         │   (faulttest vault list, vault status)
         │
         ▼
    POST /api/v1/fleet/playbooks/{id}/activate
         │
         ▼
    [ is_active = true      ]   ← this version runs when the Playbook is invoked
    [ series promoted       ]   ← previous active version becomes inactive
```

Within a series (identified by `series_id`, prefixed `pbs_`), exactly one version is active at a time. Activation creates a new version without destroying history. You can see all versions and their sources via:

```bash
GET /api/v1/fleet/playbooks?series_id=pbs_db_restart_triage
```

---

## Vault Commands

`faulttest vault` provides the operational window into the Vault from the command line. Run history is stored in `~/.faulttest/history.json` and is updated automatically at the end of every `faulttest run`. When `--gateway` is configured, per-fault evaluation scores (`keyword_score`, `tool_score`, `diagnosis_score`, `overall_score`) are also written to the auditd `run_evaluation` table via `POST /api/v1/fleet/playbook-runs/{runID}/evaluation`, keyed by the gateway's `plr_*` playbook run ID. The local JSON file acts as a cache; auditd is the durable store.

### vault list

```bash
faulttest vault list [--gateway http://gateway:8080] [--api-key sk-...]
                     [--target staging-db]
```

Shows the full fault catalog alongside the linked Playbook, date of last test run, pass/fail status, consistency certification verdict, and diagnosis accuracy. When `--gateway` is provided, also verifies that referenced Playbook series IDs exist on the Gateway and fetches live stability certs and accuracy data.

```
FAULT                          PLAYBOOK                   LAST TEST    STATUS  SCORE  STABLE       ACCURACY
-----------------------------------------------------------------------------------------------------------------
db-max-connections             pbs_db_conn_pooling        2026-06-20   PASS    95%    STABLE(5)    100% (4/4)
db-lock-contention             pbs_lock_chain_triage      2026-06-20   PASS    91%    STABLE(5)    –
db-idle-in-transaction         pbs_db_idle_txn            2026-06-15   PASS    88%    UNSTABLE(5)  –
db-high-cache-miss             pbs_cache_miss_triage      (never)      -       –      —            –
db-table-bloat                 pbs_vacuum_triage          2026-06-01   PASS    90%    STABLE(5) 21d –
db-connection-refused          pbs_db_restart_triage      2026-04-15   PASS    82%    —            –
db-pg-hba-corrupt              pbs_db_config_recovery     (never)      -       –      —            –
```

**STATUS column:**

| Value | Meaning |
|-------|---------|
| `PASS` / `FAIL` | Last run result |
| `-` | Fault has a Playbook linked but has never been run against this target |
| `NO PLAYBOOK` | No `remediation.playbook_id` configured in the catalog |
| `PLAYBOOK NOT FOUND` | Playbook series ID configured but not found on the Gateway |

**STABLE column** — consistency certification verdict from the most recent `faulttest run --repeat N` (requires `--gateway`):

| Value | Meaning |
|-------|---------|
| `STABLE(N)` | Certified STABLE in the last N runs: pass rate ≥ 80% and confidence spread ≤ 30pp |
| `STABLE(N) Xd` | STABLE but cert is X days old — shown after 14 days as an age reminder |
| `UNSTABLE(N)` | Certified UNSTABLE — pass rate or confidence spread outside bounds; playbook needs attention before promotion |
| `—` | No certification run has been posted for this fault |

The `ACCURACY` column shows the diagnosis accuracy rate from operator feedback (see [operator feedback](PLAYBOOKS.md#operator-feedback)). `–` means no feedback has been submitted yet.

Use `--target` to filter history to a specific database server (the `--agent-conn` alias set during runs). See [here](CONSISTENCY.md) for how to run certifications and what STABLE/UNSTABLE means for the flywheel.

### vault accuracy

```bash
faulttest vault accuracy <fault-id or series-id> [--gateway http://gateway:8080] [--api-key sk-...]
```

Accepts either a fault catalog ID (e.g. `db-lock-contention`) or a playbook series ID (e.g. `pbs_lock_chain_triage`). Shows the per-series diagnosis accuracy breakdown — how often the agent's root-cause hypothesis was confirmed correct by operators — and, when called with a fault ID, also shows the full consistency certification cert for that fault.

**Called with a playbook series ID** — accuracy only:

```bash
faulttest vault accuracy pbs_lock_chain_triage \
  --gateway http://gateway:8080 \
  --api-key $HELPDESK_API_KEY
```

```
Diagnosis accuracy for series: pbs_lock_chain_triage

  Feedback submitted : 12 runs
  Correct diagnoses  : 11
  Accuracy rate      : 92%

  Breakdown by feedback time:
    At-gate (before remediation) : 8 of 9 correct (89%)
    Post-incident (after recovery): 3 of 3 correct (100%)
```

**Called with a fault ID** — accuracy plus full stability cert:

```bash
faulttest vault accuracy db-lock-contention \
  --gateway http://gateway:8080 \
  --api-key $HELPDESK_API_KEY
```

```
Accuracy: db-lock-contention → pbs_lock_chain_triage
  At-gate feedback:      8 runs   89% accurate (8/9)
  Post-incident:         3 runs  100% accurate (3/3)
  Combined:             12 runs   92% accurate

Triage consistency
  Fault         : db-lock-contention  (Lock contention / deadlock)
  Verdict       : STABLE
  Runs          : 5
  Pass rate     : 100%
  Conf range    : 5pp  (primary hypothesis, passing runs only)
  Playbook      : pbs_lock_chain_triage
  Diagnosis model: claude-sonnet-4-6
  Judge model   : claude-haiku-4-5-20251001
  Tested at     : 2026-06-20 03:14 UTC  (1 days ago)
```

If the cert is older than 30 days, a warning is shown beneath it:

```
  [WARN] cert is older than 30 days — consider re-running --repeat to refresh
```

The overall accuracy rate is `correct / total` across both feedback times; nil verdicts are excluded. The breakdown section appears whenever at least one feedback type has data, letting you compare the signal quality: at-gate feedback is uncontaminated by knowledge of whether the fix worked, while post-incident feedback can be influenced by hindsight.

With no argument, lists all catalog faults that have a diagnosis playbook series and shows a table with per-type counts:

```
  FAULT                                SERIES                               AT-GATE   POST-INC  ACCURACY
  ──────────────────────────────────────────────────────────────────────────────────────────────────────
  db-lock-contention                   pbs_lock_chain_triage                  8/9       3/3       92%
  db-slow-query                        pbs_slow_query_triage                  4/5       –         80%
```

`AT-GATE` and `POST-INC` show `correct/total`; `–` means no feedback of that type has been submitted for the series yet.

Use `vault accuracy` alongside `resolution_rate` (from `vault stats`) to distinguish between "the agent diagnosed correctly but remediation didn't work" and "the agent misdiagnosed and remediation fixed the wrong thing." Use the stability cert block to understand whether the accuracy signal is built on a stable or noisy foundation, see [here](CONSISTENCY.md) for details.

Feedback is submitted interactively by `faulttest` after a successful recovery when running with `--remediate` and `--gateway` (see below), or manually via `POST /api/v1/fleet/playbook-runs/{runID}/feedback`.

### vault incidents

```bash
faulttest vault incidents <fault-id or series-id> \
  [--limit N] \
  --gateway http://gateway:8080 \
  --api-key $HELPDESK_API_KEY
```

Lists the most recent triage runs for a fault or playbook series. Accepts either a fault catalog ID (e.g. `db-lock-contention`) or a series ID (e.g. `pbs_lock_chain_triage`). Requires `--gateway`.

```
Incidents for db-lock-contention (pbs_lock_chain_triage) — 3 runs

RUN ID          STARTED            DIAG        REMEDIATION       FEEDBACK      SCORE  FINDINGS
──────────────────────────────────────────────────────────────────────────────────────────────────────────────
plr_a3f7c1b2   2026-06-01 14:32   resolved    resolved          ✓ correct     91%    lock_type=relation, rel...
plr_e8c2d5a1   2026-05-28 09:11   resolved    –                 submitted     85%    blocking_pid=3421, wait...
plr_f1b9e3c4   2026-05-14 22:45   unresolved  –                 ✗ wrong       40%    –
```

| Column | Source |
|--------|--------|
| `DIAG` | Triage run outcome from gateway |
| `REMEDIATION` | Outcome of the linked remediation run (if any) |
| `FEEDBACK` | Operator post-incident verdict via Decision Hub |
| `SCORE` | `overall_score` from `run_evaluation` in auditd (requires `--gateway`) |
| `FINDINGS` | Truncated `findings_summary` from the playbook run |

The `SCORE` column is populated only when faulttest evaluation data has been posted to auditd (i.e., the run was triggered by `faulttest run --gateway`). Real-incident runs triggered from the product UI show `–` unless scores are manually submitted via `POST /api/v1/fleet/playbook-runs/{runID}/evaluation`.

**Deep-dive mode:** pass a `plr_*` run ID instead of a fault or series ID to print the full incident journey for that specific run:

```bash
faulttest vault incidents plr_a3f7c1b2 \
  --gateway http://gateway:8080 --api-key $HELPDESK_API_KEY
```

```
════════════════════════════════════════════════════════════
INCIDENT plr_a3f7c1b2
Started: 2026-06-01 14:32 UTC   Duration: 47s
Operator: alice

── TRIAGE ──────────────────────────────────────────────────
Playbook:  pbs_lock_chain_triage
Findings:  Transaction lock chain detected on pg_locks...

Hypotheses:
  [PRIMARY  92%] Lock contention from long-running txn (pid 1234)
                 Evidence: "waiting on ShareLock"
  [REJECTED 41%] High connection count near pg_max_connections
                 Rejected: pg_stat_activity shows only 23/100 used

── GATE ────────────────────────────────────────────────────
Approved by: alice  at 14:33 UTC  (approved)
Feedback:
  Triage at gate:      ✓ correct

── REMEDIATION ─────────────────────────────────────────────
Playbook:  pbs_lock_chain_remediate   Outcome: resolved (8.1s)
Steps:     ✓ get_blocking_queries  ✓ terminate_connection

── EVALUATION ──────────────────────────────────────────────
Diagnosis:         0.91 (LLM judge)   Agent confidence: 0.92
Remediation:       0.88 (LLM judge)

── POST-INCIDENT FEEDBACK ──────────────────────────────────
  Triage:      ✓ correct
  Remediation: ✓ worked as expected
```

The deep-dive assembles data from `GET /api/v1/incidents/{runID}` on the gateway, which joins triage, gate, remediation, eval scores, and all four feedback slots into a single timeline view.

### vault status

```bash
faulttest vault status [--since-days 30] [--target staging-db] [--fault db-lock-contention]
```

Shows overall pass rates across all runs in the history window, plus a per-fault score breakdown with keyword, tool, category, judge, and remediation columns:

```
=== Vault Status — staging-db (last 30 days, 4 runs) ===

DATE         RUN ID               PASS RATE
--------------------------------------------------
2026-04-10   a1b2c3d4             80% (8/10)
2026-04-14   e5f6g7h8             90% (9/10)
2026-04-16   i9j0k1l2             90% (9/10)

=== Per-Fault Detail ===

db-lock-contention (Lock contention / deadlock)
  DATE         RUN       KWD    TOOLS  SCORE  CATEG  JUDGE  REMED  RESULT
  -------------------------------------------------------------------------
  2026-04-10   a1b2c3    90%    100%   88%    -      67%    -      PASS
  2026-04-14   e5f6g7    90%    100%   88%    -      67%    100%   PASS
  2026-04-16   i9j0k1    90%    100%   91%    -      100%   100%   PASS
```

### vault drift

```bash
faulttest vault drift [--since-days 90] [--target staging-db]
```

Compares pass rates between the first and second halves of the history window and flags faults whose pass rate dropped by more than 20 percentage points. Use this to catch quiet regressions before they become production incidents:

```
=== Vault Drift Analysis — all targets (last 90 days) ===

FAULT                            FIRST HALF   SECOND HALF  DRIFT
------------------------------------------------------------------------
db-lock-contention               100%         50%          -50%
db-replication-lag               75%          33%          -42%

(2 fault(s) suppressed — fewer than 3 runs per window half)
```

Faults with fewer than 3 runs in either the first or second half are suppressed from the table and counted in the footer. The floor matches `vault calibration`'s `INSUFFICIENT_DATA` threshold — below 3 samples, drift numbers are noise, not signal.

When drift is detected, run `faulttest inject` + `faulttest teardown` to reproduce the fault interactively and investigate what changed in the agent or environment.

### vault versions

```bash
faulttest vault versions <fault-id or series-id> \
  --gateway http://gateway:8080 \
  --api-key $HELPDESK_API_KEY
```

Shows per-version run stats for a playbook series: resolution rate, average step count, average recovery time, and separate diagnosis / remediation scores. Accepts either a fault catalog ID (e.g. `db-lock-contention`) or a series ID (e.g. `pbs_lock_chain_triage`). Requires `--gateway`.

```
Version stats for pbs_cache_miss_remediate — 2 version(s)

VERSION     RUNS    RESOLVED    AVG STEPS   AVG TIME    AVG DIAG   AVG REMED
─────────────────────────────────────────────────────────────────────────────
1.0          5      60%         6.2         42s         72%        –
1.1  *       3      100%        4.0         8s          91%        85%

* = currently active version
```

Data sources:

| Column | Source |
|--------|--------|
| `RUNS` / `RESOLVED` | `playbook_runs` table in auditd, grouped by `playbook_id` |
| `AVG STEPS` | Average number of steps recorded in `playbook_run_steps` per run |
| `AVG TIME` | Average wall-clock time between `started_at` and `completed_at` for completed runs |
| `AVG DIAG` | Average `diagnosis_score` from `run_evaluation`; `–` when no runs have eval data |
| `AVG REMED` | Average `remediation_score` for runs where remediation was executed (score > 0); `–` when no remediation runs |

The gateway endpoint backing this command: `GET /api/v1/fleet/series/{seriesID}/version-stats`.

### vault calibration

```bash
# Fleet-wide calibration
faulttest vault calibration \
  --gateway https://gateway.internal \
  --api-key $HELPDESK_API_KEY

# Scoped to one series (or fault ID)
faulttest vault calibration db-lock-contention \
  --gateway https://gateway.internal \
  --api-key $HELPDESK_API_KEY
```

Shows how well the agent's self-reported confidence and LLM judge scores predict whether operators confirm the outcome was correct. Requires runs that have both evaluation data (`--gateway` flag during `faulttest run`) and operator feedback (at-gate or post-incident).

**Triage calibration** bands on `primary_confidence` — the agent's `CONFIDENCE:` value from its primary hypothesis line (`HYPOTHESIS_1: ... | CONFIDENCE: 0.92`). Runs where the agent did not emit a structured hypothesis are excluded from the confidence bands and counted separately as heuristic-only runs. At-gate feedback is preferred over post-incident for triage — it is captured before the operator knows whether remediation succeeded, eliminating outcome bias.

**Remediation calibration** bands on `remediation_judge_score` — the LLM-as-judge quality grade for the remediation plan and execution. Post-incident feedback is preferred over at-gate for remediation, because post-incident reflects the actual outcome rather than a pre-execution plan review.

```
Diagnosis calibration — fleet-wide (17 runs with agent confidence + operator feedback)
(2 run(s) excluded — agent did not emit a CONFIDENCE: value on primary hypothesis)

CONFIDENCE    RUNS    CORRECT    ACCURACY    CALIBRATION
─────────────────────────────────────────────────────────────────
90-100%          12         10        83%    OVERCONFIDENT  ⚠ 7/12 keyword (no judge)
70-89%            4          3        75%    WELL_CALIBRATED
<70%              1          1       100%    INSUFFICIENT_DATA

Remediation calibration — fleet-wide (8 runs with remediation judge score + operator feedback)

CONFIDENCE    RUNS    CORRECT    ACCURACY    CALIBRATION
─────────────────────────────────────────────────────────────────
90-100%           5          4        80%    WELL_CALIBRATED
70-89%            3          3       100%    UNDERCONFIDENT
<70%              0          0          –    INSUFFICIENT_DATA
```

The `⚠ N/M keyword (no judge)` annotation appears on a band when some of its runs were scored
using the keyword heuristic (`--judge` was not used or not available). These runs are still
included in the band — they have a confidence value — but their `verdict_correct` determination
came from keyword matching rather than a semantic LLM judge. The annotation is a data-quality
signal: if a band is `OVERCONFIDENT` but most of its verdicts are heuristic-derived, tighten
the signal by running `--judge` before drawing conclusions.

This is distinct from the *excluded* runs reported in the header line (`agent did not emit a
CONFIDENCE: value`) — those runs have no confidence score at all and do not appear in any band.

The remediation section only appears when there are runs with both a non-zero `remediation_judge_score` and operator remediation feedback (`feedback_type: "remediation"`).

Calibration is determined by comparing `ACCURACY` against the midpoint of each band:

| Band | Expected accuracy | WELL_CALIBRATED range |
|------|------------------|-----------------------|
| `90-100%` | 95% | 85–100% |
| `70-89%` | 80% | 70–90% |
| `<70%` | 50% | 40–60% |

**OVERCONFIDENT** — model scores high but operators disagree more than expected.  
**UNDERCONFIDENT** — model scores low but operators agree more than expected.  
**INSUFFICIENT_DATA** — fewer than 3 runs in this band; no reliable conclusion.

The gateway endpoint backing this command: `GET /api/v1/fleet/calibration?series_id=<optional>`.

### vault suggest

```bash
faulttest vault suggest \
  --trace-id tr_abc123 \
  --outcome resolved \
  --gateway http://gateway:8080 \
  --api-key $HELPDESK_API_KEY
```

Manually synthesises a Playbook draft from any audit trace ID. Useful when you want to create a Playbook from a specific real incident that wasn't auto-suggested, or when you want to produce an on-demand draft for a trace you know about. Prints the draft YAML to stdout with activation instructions.

When the gateway's auditd is configured, the draft is also auto-saved and the `playbook_id` of the persisted draft is printed.

### vault suggest-update

```bash
faulttest vault suggest-update \
  --series-id pbs_db_restart_triage \
  --trace-id tr_xyz789 \
  --outcome resolved \
  --gateway http://gateway:8080 \
  --api-key $HELPDESK_API_KEY
```

Fetches the current active Playbook for a series, synthesises a proposed update from a recent successful incident trace, and displays both side by side so you can compare and decide whether to activate the proposal:

```
=== Playbook Update Proposal: pbs_db_restart_triage ===

Current:  pb_f49b5eac — Database Down — Restart Triage
Trace:    tr_xyz789 (outcome: resolved)

--- CURRENT GUIDANCE ---
Check max_connections first, then inspect pg_stat_activity...

--- PROPOSED DRAFT (from trace) ---
name: Database Down — Restart Triage
guidance: |
  In this incident, the agent first confirmed the database was unreachable
  via check_connection, then read pod logs to find OOM kill signals...

Proposed draft saved as: pb_proposed_001 (inactive, source=generated)

# To activate the proposed draft:
#   curl -X POST .../api/v1/fleet/playbooks/pb_proposed_001/activate \
#        -H 'Authorization: Bearer <key>'
```

---

## The `from-trace` Endpoint

The gateway endpoint that powers all auto-suggest paths:

```
POST /api/v1/fleet/playbooks/from-trace
Content-Type: application/json

{
  "trace_id": "tr_abc123",
  "outcome": "resolved"
}
```

The gateway:
1. Fetches audit events for the given `trace_id` from auditd (when auditd is configured)
2. Passes the tool call sequence and outcome to the planner LLM
3. Synthesises a Playbook YAML draft with `name`, `description`, `problem_class`, `symptoms`, `guidance`, and `escalation` fields
4. Auto-persists the draft as `source="generated"`, `is_active=false` (when auditd is configured)
5. Returns `{"draft": "...", "source": "...", "playbook_id": "pb_..."}` — `playbook_id` is empty when persistence is unavailable

The synthesis prompt grounds the LLM in the actual sequence of tool calls made during the incident, not just the fault metadata. This produces Playbooks that are specific to your environment's tool catalog and agent behavior.

```bash
# Manual call — useful for testing or when trace IDs are known
curl -X POST http://gateway:8080/api/v1/fleet/playbooks/from-trace \
  -H "Authorization: Bearer $HELPDESK_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"trace_id": "tr_abc123", "outcome": "resolved"}'
```

---

## Reviewing and Activating Drafts

All auto-generated drafts land as inactive (`is_active=false`). This is intentional — the human stays in the loop before anything is promoted to production use.

**List pending drafts** (generated, not yet activated):

```bash
# Via the API
curl -s http://gateway:8080/api/v1/fleet/playbooks?source=generated \
  -H "Authorization: Bearer $HELPDESK_API_KEY" | jq '.playbooks[] | select(.is_active == false)'

# Via vault list (shows latest run status alongside Playbook link)
faulttest vault list --gateway http://gateway:8080 --api-key $HELPDESK_API_KEY
```

**Activate a draft** (promotes it in its series, deactivates the previous version):

```bash
curl -X POST http://gateway:8080/api/v1/fleet/playbooks/pb_a3f9b2c1/activate \
  -H "Authorization: Bearer $HELPDESK_API_KEY"
```

**Discard a draft** (if the proposal isn't useful):

```bash
curl -X DELETE http://gateway:8080/api/v1/fleet/playbooks/pb_a3f9b2c1 \
  -H "Authorization: Bearer $HELPDESK_API_KEY"
```

If you want to refine the draft before activating, update it:

```bash
curl -X PUT http://gateway:8080/api/v1/fleet/playbooks/pb_a3f9b2c1 \
  -H "Authorization: Bearer $HELPDESK_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"guidance": "Updated guidance based on operator review..."}'
```

---

## Three Customer Workflows

### 1. Onboarding — linking your first Playbooks

When you first deploy aiHelpDesk, link each fault in the catalog to a Playbook series. This tells `faulttest vault list` which Playbooks should exist and enables the `PLAYBOOK NOT FOUND` detection:

```yaml
# In your custom fault catalog (or the built-in)
- id: db-max-connections
  remediation:
    Playbook_id: pbs_db_conn_pooling   # series_id of your Playbook
    verify_sql: "SELECT count(*) < 50 FROM pg_stat_activity"
    verify_timeout: "120s"
```

Then validate that all linked Playbooks exist on your deployment:

```bash
faulttest vault list \
  --gateway http://helpdesk-gateway:8080 \
  --api-key $HELPDESK_API_KEY
# → PLAYBOOK NOT FOUND entries show what still needs to be registered
```

### 2. Playbook acceptance — review and approve auto-generated drafts

After each `faulttest run --remediate` pass or resolved incident, check for pending drafts:

```bash
# See which faults now have associated drafts
faulttest vault list --gateway http://helpdesk-gateway:8080 --api-key $HELPDESK_API_KEY

# Review a specific draft
curl http://helpdesk-gateway:8080/api/v1/fleet/playbooks/pb_a3f9b2c1 \
  -H "Authorization: Bearer $HELPDESK_API_KEY" | jq .

# Activate if it looks good
curl -X POST http://helpdesk-gateway:8080/api/v1/fleet/playbooks/pb_a3f9b2c1/activate \
  -H "Authorization: Bearer $HELPDESK_API_KEY"
```

Run the relevant fault again with `--remediate` after activation to confirm the newly promoted Playbook continues to achieve recovery:

```bash
faulttest run \
  --external --conn "host=staging-db ..." \
  --db-agent http://helpdesk-gateway:8080 \
  --ids db-max-connections \
  --remediate --gateway http://helpdesk-gateway:8080 --api-key $HELPDESK_API_KEY
```

### 3. Regression monitoring — catching drift before it becomes an incident

Run `faulttest` on a schedule (weekly CI job or cron) and use the Vault commands to monitor health over time:

```bash
# Weekly CI — run all external faults with remediation + notify Slack on completion
faulttest run \
  --external --conn "host=staging-db ..." \
  --db-agent http://helpdesk-gateway:8080 \
  --judge --judge-vendor anthropic --judge-model claude-haiku-4-5-20251001 \
  --remediation-judge \
  --remediate --gateway http://helpdesk-gateway:8080 --api-key $HELPDESK_API_KEY \
  --notify-url https://hooks.slack.com/services/xxx/yyy/zzz

# After a few weeks, check for drift
faulttest vault drift --since-days 90

# For a specific fault showing drift, compare against a recent trace
faulttest vault suggest-update \
  --series-id pbs_db_conn_pooling \
  --trace-id tr_latest_run \
  --gateway http://helpdesk-gateway:8080 --api-key $HELPDESK_API_KEY
```

The drift command identifies which Playbooks may need a guidance update. `suggest-update` then proposes the update based on what the agent actually did in the most recent successful run.

---

## Connection to Other Docs

| Document | What it covers |
|----------|---------------|
| [PLAYBOOKS.md](PLAYBOOKS.md) | Playbook schema, CRUD API, import formats, system Playbooks |
| [INCIDENTS.md](INCIDENTS.md) | What an Incident is; how real and injected Incidents feed the Vault; bundle anatomy |
| [PLAYBOOK_OPS.md](PLAYBOOK_OPS.md) | Operational best practices for authoring and running Playbooks |
| [FAULTTEST.md](FAULTTEST.md) | Full faulttest CLI reference, fault catalog, scoring, remediation |
| [FLEET.md](FLEET.md) | Fleet runner, job definitions, schema drift, planner |
| [API.md](API.md) | Full REST API reference including `/fleet/playbooks/from-trace` |
