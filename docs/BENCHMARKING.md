# aiHelpDesk Agent Benchmarking

This page describes how to measure the value of aiHelpDesk's diagnostic accuracy,
how much of a difference its expert scaffolding makes (playbook guidance, hypothesis format,
escalation chains) versus just a baseline LLM capability. The comparison uses
**Crystal Ball mode** on the Gateway and the **`--via-gateway`** flag on the
built-in, customer-facing Fault Injection Testing (aka `faulttest`).

---

## Table of Contents

1. [Background: what scaffolding does](#1-background-what-scaffolding-does)
2. [Crystal Ball mode](#2-crystal-ball-mode)
   - [What it does](#21-what-it-does)
   - [Enabling it](#22-enabling-it)
3. [Running an A/B comparison with faulttest](#3-running-an-ab-comparison-with-faulttest)
   - [How `--via-gateway` works](#31-how---via-gateway-works)
   - [The `diagnosis_playbook_series_id` catalog field](#32-the-diagnosis_playbook_series_id-catalog-field)
   - [Step-by-step workflow](#33-step-by-step-workflow)
4. [Interpreting the results](#4-interpreting-the-results)
5. [Adding A/B support to more faults](#5-adding-ab-support-to-more-faults)

---

## 1. Background: what scaffolding does

By default, when the Gateway runs a Playbook (`POST /api/v1/fleet/playbooks/{id}/run`),
it wraps the agent call in several layers of expert scaffolding before the prompt
reaches the LLM:

- **Playbook guidance** — a `## Playbook Guidance` section injected into the
  system prompt with tool-call hints, step-by-step investigation order, common
  misdiagnosis warnings, and escalation conditions written by domain experts.
- **Hypothesis format** — a structured `HYPOTHESIS_N: / CONFIDENCE: / EVIDENCE:`
  output format that makes the agent's reasoning auditable and machine-parseable.
- **Escalation chaining** — when the agent emits `ESCALATE_TO: <series_id>`,
  the gateway automatically fires the next playbook in the chain (subject to
  approval mode) and merges the diagnostic reports.

This scaffolding is aiHelpDesk's primary value-add over a bare LLM: it turns a
general-purpose model into something that behaves like a trained SRE or DBA.

When you call an agent directly (via A2A or `/api/v1/query`) none of this
scaffolding is applied and all you get is the agent's built-in system prompt only. This
is the baseline the benchmarking workflow measures against.

---

## 2. Crystal Ball mode

### 2.1 What it does

Crystal Ball mode strips all scaffolding from playbook runs. When enabled on
the Gateway, `POST /api/v1/fleet/playbooks/{id}/run` sends the operator question
directly to the agent with:

- No playbook guidance
- No hypothesis output format
- No escalation chaining

The LLM decides which tools to call, in whatever order and what conclusions to draw entirely on its
own, using only its built-in system prompt.

Same starting point, same prompt, same data data, but no structured guided scaffolding (which often leads to a very different result).

Every response in crystal-ball mode carries a warning field so callers know
scaffolding was bypassed:

```json
{
  "text": "...",
  "crystal_ball": true,
  "crystal_ball_warning": "Crystal-ball mode is active. Playbook guidance, hypothesis formatting, and escalation chaining are bypassed. NOT recommended for production use."
}
```

When `faulttest` detects this field it logs a warning per fault:

```
level=WARN msg="⚠  crystal-ball mode active on gateway — playbook scaffolding is bypassed; this result measures unguided LLM capability only"
```

> **Not for production.** Crystal-ball mode skips escalation chaining, so
> multi-agent diagnosis chains do not run. Use it only in test environments
> for benchmarking or demos.

### 2.2 Enabling it

| Deployment | Setting |
|------------|---------|
| **CLI / systemd** | `HELPDESK_CRYSTAL_BALL=true` in `.env` or shell |
| **Docker Compose** | `HELPDESK_CRYSTAL_BALL=true` in the shell or `.env` file |
| **Helm** | `--set gateway.crystalBall=true` |

The setting takes effect immediately on the running Gateway. Nno restart needed
for Docker Compose (env var is read at startup) or Helm (`helm upgrade` rolls
out a new pod).

---

## 3. Running an A/B comparison with faulttest

### 3.1 How `--via-gateway` works

Normally `faulttest` calls agents **directly** via A2A (`--k8s-agent`,
`--db-agent`, etc.), bypassing the gateway's playbook endpoint entirely. The
gateway is only used for remediation.

`--via-gateway` changes the diagnosis call to go through
`POST /api/v1/fleet/playbooks/{id}/run` on the gateway instead. This is the
path where scaffolding is applied — and therefore the path where crystal-ball
mode has something to strip.

Without `--via-gateway`, enabling crystal-ball mode on the Gateway has no effect
on `faulttest` results because the gateway is never in the diagnosis path.

### 3.2 The `diagnosis_playbook_series_id` catalog field

`--via-gateway` needs to know which playbook to run for each fault. The catalog
field `diagnosis_playbook_series_id` provides this:

```yaml
- id: db-wal-disk-full-k8s
  name: "WAL disk full — writes failing (Kubernetes)"
  category: kubernetes
  diagnosis_playbook_series_id: pbs_k8s_pod_crash_triage
  ...
```

When `--via-gateway` is active and a fault has this field set, faulttest:

1. Resolves the series ID to the active versioned playbook ID via
   `GET /api/v1/fleet/playbooks?series_id=<id>`
2. Calls `POST /api/v1/fleet/playbooks/{id}/run` with `connection_string` and
   the fault prompt as `context`
3. Scores the response text as usual (keywords, tools, judge)
4. Sets `CrystalBall=true` on the response if the gateway signals it

Faults **without** `diagnosis_playbook_series_id` fall back to the direct-agent
path even when `--via-gateway` is set, so the flag is safe to pass for a mixed
catalog run.

### 3.3 Step-by-step workflow

**Prerequisites:**
- A running aiHelpDesk Gateway with the target Playbook registered
  (`pbs_k8s_pod_crash_triage` or equivalent)
- `faulttest` built from source or the standalone binary
- Access to the K8s cluster for injection (for `db-wal-disk-full-k8s`)

**Step 1 — Scaffolded baseline (normal Gateway)**

```bash
faulttest run \
  --conn test-db \
  --k8s-agent http://k8s-agent:1102 \
  --gateway http://gateway:8080 \
  --api-key $HELPDESK_CLIENT_API_KEY \
  --via-gateway \
  --judge \
  --ids db-wal-disk-full-k8s \
  --report-dir ./reports/scaffolded
```

Expected output:
```
--- Testing: WAL disk full — writes failing (Kubernetes) (db-wal-disk-full-k8s) ---
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% judge=100%)
```

**Step 2 — Enable Crystal Ball mode on the Gateway**

```bash
# Helm
helm upgrade helpdesk . --reuse-values --set gateway.crystalBall=true

# Docker Compose
HELPDESK_CRYSTAL_BALL=true docker compose up -d gateway

# Systemd
echo "HELPDESK_CRYSTAL_BALL=true" >> /opt/helpdesk/.env
systemctl restart helpdesk-gateway
```

**Step 3 — Crystal Ball run (identical command)**

```bash
faulttest run \
  --conn test-db \
  --k8s-agent http://k8s-agent:1102 \
  --gateway http://gateway:8080 \
  --api-key $HELPDESK_CLIENT_API_KEY \
  --via-gateway \
  --judge \
  --ids db-wal-disk-full-k8s \
  --report-dir ./reports/crystal-ball
```

Expected output (note the warning):
```
--- Testing: WAL disk full — writes failing (Kubernetes) (db-wal-disk-full-k8s) ---
level=WARN msg="⚠  crystal-ball mode active on gateway — playbook scaffolding is bypassed; this result measures unguided LLM capability only"
Diagnostic Result:   [PASS] score=??% (keywords=??% tools=??% judge=??%)
```

**Step 4 — Compare**

```bash
jq '{id: .id, score: .summary.pass_rate}' \
  reports/scaffolded/faulttest-*.json \
  reports/crystal-ball/faulttest-*.json
```

Or for per-fault judge reasoning:

```bash
jq '.results[] | {id: .failure_id, score: .overall_score, judge: .judge_reasoning}' \
  reports/scaffolded/faulttest-*.json

jq '.results[] | {id: .failure_id, score: .overall_score, judge: .judge_reasoning}' \
  reports/crystal-ball/faulttest-*.json
```

**Step 5 — Restore normal mode**

```bash
helm upgrade helpdesk . --reuse-values --set gateway.crystalBall=false
```

---

## 4. Interpreting the results

The score delta between the scaffolded and crystal-ball runs is the concrete
contribution of the Playbook system for that fault:

| Scaffolded | Crystal-ball | Interpretation |
|:----------:|:------------:|----------------|
| High | High | The LLM finds this fault on its own — scaffolding adds polish but isn't load-bearing |
| High | Low | Scaffolding is doing significant work — the guidance, tool order hints, or hypothesis format are critical for this fault |
| Low | Low | Neither scaffolding nor raw LLM capability is sufficient — the fault or playbook guidance needs work |
| Low | High | Unusual. May indicate the playbook guidance is actively misleading the agent |

The LLM-as-judge score (when `--judge` is set) is the most meaningful component
for this comparison because it evaluates the quality of the diagnosis narrative,
not just keyword presence. See [LLM_AS_JUDGE.md](LLM_AS_JUDGE.md) for details
on judge scoring and how to enable it.

---

## 5. Adding A/B support to more faults

Currently only `db-wal-disk-full-k8s` has `diagnosis_playbook_series_id` set.
To enable A/B comparison for additional faults:

1. **Identify or create a gateway playbook** that covers the fault's symptom
   class. The playbook must have an `agent_name` matching the agent that handles
   the fault's category (`k8s_agent`, `postgres_database_agent`, `sysadmin_agent`).

2. **Add `diagnosis_playbook_series_id`** to the fault entry in
   `testing/catalog/failures.yaml`:

   ```yaml
   - id: k8s-oomkilled
     ...
     diagnosis_playbook_series_id: pbs_k8s_pod_crash_triage
   ```

   Multiple faults can share the same playbook series ID when the playbook's
   guidance is broad enough to cover them (e.g. `pbs_k8s_pod_crash_triage`
   handles any pod crash regardless of exit code).

3. **Register the playbook** in your gateway deployment. See
   [PLAYBOOKS.md](PLAYBOOKS.md) for how to import and activate playbooks.

4. **Run the A/B workflow** from [§3.3](#33-step-by-step-workflow) above.
