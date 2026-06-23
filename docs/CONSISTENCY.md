# aiHelpDesk Triage Consistency Certification

LLM-based diagnosis has two distinct quality dimensions that must both hold for production use:
**accuracy** (does the agent identify the correct root cause?) and **consistency** (does it give
the same answer on repeated runs of the same fault?). 

The [Operational SRE/DBA Flywheel](VAULT.md#the-operational-sredba-flywheel)
measures accuracy continuously through Vault signals: resolution rates, operator feedback,
calibration curves. 

Consistency is measured differently: it is a *pre-production gate*, a
certification you run before a playbook enters the live rotation and re-run whenever the
model or playbook changes significantly.

This page describes the Consistency Certification system: what it certifies, how it works
mechanically, how it relates to the other quality signals in the flywheel and how to run
it on each deployment platform.

---

## Table of Contents

1. [Why Consistency is a separate signal from Accuracy](#1-why-consistency-is-a-separate-signal-from-accuracy)
2. [Where certification fits in the flywheel](#2-where-certification-fits-in-the-flywheel)
3. [What a stability cert contains](#3-what-a-stability-cert-contains)
4. [STABLE vs. UNSTABLE: the criteria](#4-stable-vs-unstable-the-criteria)
5. [Running a certification](#5-running-a-certification)
   - [From source (make recertify)](#51-from-source-make-recertify)
   - [Host / VM (binary)](#52-host--vm-binary)
   - [Docker Compose (binary + auto-db)](#53-docker-compose-binary--auto-db)
   - [Kubernetes (Job / CronJob)](#54-kubernetes-job--cronjob)
6. [Reading the per-run output](#6-reading-the-per-run-output)
7. [Viewing certification results](#7-viewing-certification-results)
   - [vault list — STABLE column](#71-vault-list--stable-column)
   - [vault accuracy — full cert detail](#72-vault-accuracy--full-cert-detail)
8. [When to re-certify](#8-when-to-re-certify)
9. [Relationship to other quality signals](#9-relationship-to-other-quality-signals)

---

## 1. Why Consistency is a separate signal from Accuracy

Consider two playbooks, each tested 5 times against the same injected fault:

| Playbook | Run results | Pass rate | Confidence range |
|----------|-------------|-----------|-----------------|
| A | PASS PASS PASS FAIL PASS | 80% | 88–95% (7pp) |
| B | PASS PASS PASS PASS PASS | 100% | 62–97% (35pp) |

Playbook B has a perfect pass rate but a 35-percentage-point confidence spread — the agent's
certainty in its own answer swings wildly between runs. In production, that variance translates
to inconsistent gate decisions (sometimes `auto-approve`, sometimes `escalate`), inconsistent
operator-facing explanations and unreliable calibration data. Playbook A is more honest and
more operationally predictable, even though it sometimes fails.

The deeper issue: **you cannot build a meaningful calibration curve without consistent inputs**.
The `vault calibration` command, see [here](VAULT.md), shows whether confidence bands track actual
accuracy, but if the confidence for the same fault varies 35pp across runs, the band→accuracy
mapping is noise rather than signal. 

>> **Consistency certification is what licenses a playbook to contribute clean data to the calibration loop.**

Accuracy and consistency are also independent of each other in ways that matter:

- **Consistently correct** — stable, production-ready.
- **Consistently wrong** — reliable in a bad way; at least you know what you have and you can fix
  the playbook with a concrete target.
- **Inconsistently correct** — the most dangerous posture for production: passes QA by chance,
  fails under different phrasing or slightly different DB state.
- **Inconsistently wrong** — clearly not ready; cert will show UNSTABLE immediately.

---

## 2. Where certification fits in the flywheel

The [Operational SRE/DBA Flywheel](VAULT.md#the-operational-sredba-flywheel) describes the
accuracy feedback loop: fault → diagnosis → remediation → operator feedback → library improves.
Consistency certification is the *pre-promotion gate* that sits at the left edge of that loop,
before a playbook enters live rotation:

```
  ┌───────────────────────────────────────────────────────────────────────────────┐
  │                                                                               │
  │                  ┌─── CONSISTENCY GATE ───────────────────┐                   │
  │                  │                                        │                   │
  │   author or      │  faulttest run --repeat N              │  certified        │
  │   update    ───► │  inject → diagnose → score (×N)   ───► │  STABLE ──────►   │
  │   Playbook       │  post stability cert to auditd         │                   │
  │                  │                                        │  UNSTABLE ────►   │
  │                  └────────────────────────────────────────┘  fix, retry       │
  │                                                                               │
  │         ▼  STABLE cert issued                                                 │
  │                                                                               │
  │         Fault              Agent diagnoses             Playbook               │
  │   (injected or real) ──► + chain of thought ─────── ► remediates              │
  │          ▲                  captured                    │                     │
  │          │                                              │                     │
  │          │                           Operator confirms  ▼                     │
  │   Library improves  ◄── Human      ◄── diagnosis     Draft auto-saved         │
  │   (accuracy rises)      approves       correct?      to Vault                 │
  │                         (Vault review)  │                                     │
  │                                    accuracy_rate                              │
  │                                    feeds vault calibration                    │
  │                                                                               │
  └───────────────────────────────────────────────────────────────────────────────┘
```

The key point: **only STABLE playbooks should feed the accuracy and calibration signals**. An
unstable playbook produces variance in the accuracy signal that looks like measurement error
rather than real model or playbook quality. The consistency gate filters this out before the
data enters the flywheel.

In practice, a typical workflow is:

1. Author or update a triage playbook.
2. Run `make recertify FAULT_IDS=db-lock-contention` (or the platform-specific equivalent) with
   `--repeat 5`.
3. If STABLE: promote the playbook to active (`POST /api/v1/fleet/playbooks/{id}/activate`).
4. Let real incidents or `faulttest` gateway runs accumulate accuracy data.
5. When the model or playbook changes significantly: re-certify before promoting the new version.

---

## 3. What a stability cert contains

Each certification run posts a `FaultStabilityCert` record to auditd via
`POST /api/v1/fleet/fault-stability` (proxied through the Gateway). Upserts overwrite the
previous cert for the same `fault_id` — one cert per fault, always the latest run.

| Field | Type | Description |
|-------|------|-------------|
| `fault_id` | string | Fault catalog ID (e.g. `db-lock-contention`) |
| `fault_name` | string | Human-readable fault name |
| `playbook_series_id` | string | Triage playbook series used (e.g. `pbs_lock_chain_triage`) |
| `model` | string | LLM model used for diagnosis (e.g. `claude-haiku-4-5-20251001`) |
| `n_runs` | int | Number of inject→diagnose→teardown cycles run |
| `pass_rate` | float | Fraction of runs that passed (0.0–1.0) |
| `conf_range_pp` | int | Confidence spread in percentage points (max−min, passing runs only) |
| `is_stable` | bool | `true` if STABLE criteria are met |
| `tested_at` | timestamp | When the cert was issued |

The `model` field is intentional: a cert issued against `claude-haiku-4-5-20251001` does not
automatically transfer to `claude-haiku-4-5-20260101`. See [When to re-certify](#8-when-to-re-certify).

---

## 4. STABLE vs. UNSTABLE: the criteria

A cert is STABLE if **both** conditions hold:

1. **Pass rate ≥ 80%** — at least 4 out of 5 runs produce a correct diagnosis.
2. **Confidence range ≤ 30pp** — the agent's stated certainty on passing runs varies by no more
   than 30 percentage points (e.g. 72–98% = 26pp ✓, 60–97% = 37pp ✗).

Condition 2 is evaluated on **passing runs only** and only when confidence data is present.
If the playbook or model does not emit a `CONFIDENCE:` value, the confidence criterion is
skipped — only pass rate determines stability. This avoids penalising playbooks that simply
don't expose a confidence score.

**Why 80% and 30pp?**

- 80% pass rate allows for one failure in five runs, which is realistic for faults that depend
  on timing (lock chains, replication lag). A single flaky run shouldn't block certification;
  two flaky runs in five should.
- 30pp confidence range is wide enough to accommodate natural LLM variance on genuinely
  ambiguous faults, but narrow enough to catch playbooks where the agent is randomly picking
  between two hypotheses. A 90±15pp swing on the same fault suggests the diagnosis is not
  structurally grounded.

Protocol violations (agent omitted required `TRANSITION_TO`/`ESCALATE_TO` signal) are tracked
separately in `protocol_violations` on the stability report and cap that run's score at 75% —
which typically causes a FAIL, contributing to a lower pass rate.

---

## 5. Running a certification

Certification runs the full inject → diagnose → teardown cycle N times per fault (default: 5)
and posts a stability cert after each fault completes. Remediation is skipped in repeat mode —
consistency certification is purely about diagnosis stability.

### 5.1 From source (`make recertify`)

The simplest path during development or CI. Requires the source tree and `go`.

```bash
# Certify all 17 external-compatible faults (≈85 LLM calls, 30–40 min):
export FAULTTEST_GATEWAY_URL=http://localhost:8080
make recertify

# Database faults only (≈55 LLM calls):
make recertify CATEGORIES=database

# Single fault — fastest sanity check after a playbook edit:
make recertify FAULT_IDS=db-lock-contention RECERTIFY_REPEAT=3

# Custom number of runs:
make recertify RECERTIFY_REPEAT=10
```

`make recertify` starts the local test database automatically, runs all matching faults in
sequence and finishes with a `vault list` showing the STABLE column.

Additional variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `FAULTTEST_GATEWAY_URL` | *(required)* | Gateway URL |
| `FAULTTEST_CONN_STR` | local test DB | Injection DSN (connection to the test database) |
| `FAULTTEST_AGENT_CONN_STR` | `faulttest-db` | Alias sent to the agent in prompts |
| `FAULTTEST_API_KEY` | `$HELPDESK_CLIENT_API_KEY` | Gateway API key |
| `FAULTTEST_INFRA_CONFIG` | — | Path to `infrastructure.json` for safety-tag check |
| `RECERTIFY_REPEAT` | `5` | Runs per fault |
| `CATEGORIES` | all external | Comma-separated categories (`database`, `kubernetes`) |
| `FAULT_IDS` | all external | Comma-separated fault IDs; overrides `CATEGORIES` |

### 5.2 Host / VM (binary)

Customers on host deployments (systemd) use the `faulttest` binary directly. `--auto-db`
handles the test database — it spins up a temporary PostgreSQL container via Docker and tears
it down when the run completes. No separate database setup needed.

```bash
faulttest run \
  --auto-db \
  --external \
  --via-gateway \
  --gateway http://localhost:8080 \
  --api-key $HELPDESK_CLIENT_API_KEY \
  --repeat 5 \
  --approval-mode force
```

Filter to specific categories or faults:

```bash
# Database faults only:
faulttest run --auto-db --external --via-gateway \
  --gateway http://localhost:8080 --api-key $KEY \
  --repeat 5 --approval-mode force \
  --categories database

# Single fault:
faulttest run --auto-db --external --via-gateway \
  --gateway http://localhost:8080 --api-key $KEY \
  --repeat 5 --approval-mode force \
  --ids db-lock-contention
```

If Docker is not available on the host, supply your own test database connection:

```bash
faulttest run \
  --external \
  --via-gateway \
  --gateway http://localhost:8080 \
  --api-key $HELPDESK_CLIENT_API_KEY \
  --conn "host=test-db port=5432 dbname=testdb user=postgres password=..." \
  --agent-conn test-db \
  --repeat 5 \
  --approval-mode force
```

### 5.3 Docker Compose (binary + auto-db)

Docker Compose deployments run `faulttest` on the host (the binary is included in the release
bundle). `--auto-db` works the same way as on host/VM:

```bash
faulttest run \
  --auto-db \
  --external \
  --via-gateway \
  --gateway http://localhost:8080 \
  --api-key $HELPDESK_CLIENT_API_KEY \
  --repeat 5 \
  --approval-mode force
```

To point the agent at the gateway's internal alias for the test database (so the agent sees
the same name it would during a real incident):

```bash
faulttest run \
  --auto-db \
  --external \
  --via-gateway \
  --gateway http://localhost:8080 \
  --agent-conn faulttest-db \
  --repeat 5 \
  --approval-mode force
```

### 5.4 Kubernetes (Helm)

On Kubernetes, `--auto-db` is not available (no Docker socket in the pod). The Helm chart
ships a dedicated `recertify` CronJob
(`deploy/helm/helpdesk/templates/recertify-cronjob.yaml`) that connects to a test database
already running in the cluster. `--emit-and-wait` and `--approval-mode force` are baked in —
no TTY required.

**Enable the weekly CronJob** (runs every Sunday 03:00 UTC by default):

```yaml
# values.yaml or --set overrides
recertify:
  enabled: true
  conn: "host=test-postgres.helpdesk.svc port=5432 dbname=testdb user=postgres"
  dbPasswordSecret:
    name: pg-cluster-app    # K8s Secret holding the DB password
  gatewayAPIKeySecret: helpdesk-secrets
```

```bash
helm upgrade helpdesk deploy/helm/helpdesk \
  --reuse-values \
  -f recertify-values.yaml
```

**Run immediately** (one-shot, without waiting for the schedule):

```bash
kubectl create job --from=cronjob/helpdesk-recertify helpdesk-recertify-now \
  -n helpdesk
kubectl logs -n helpdesk -l job-name=helpdesk-recertify-now --follow
```

**Certify a specific fault only** (after a targeted playbook edit):

```yaml
recertify:
  enabled: true
  ids: "db-lock-contention"
  conn: "host=test-postgres.helpdesk.svc port=5432 dbname=testdb user=postgres"
  dbPasswordSecret:
    name: pg-cluster-app
  gatewayAPIKeySecret: helpdesk-secrets
```

**Key values** (`recertify.*`):

| Key | Default | Description |
|-----|---------|-------------|
| `enabled` | `false` | Deploy the CronJob |
| `schedule` | `"0 3 * * 0"` | Cron schedule (weekly Sunday 03:00 UTC) |
| `repeat` | `5` | Cycles per fault (drives STABLE/UNSTABLE verdict) |
| `ids` | `""` | Comma-separated fault IDs; empty = all external-compatible |
| `categories` | `""` | Comma-separated categories; empty = all |
| `conn` | `""` | Test database connection string (required) |
| `agentConn` | `"faulttest-db"` | Connection alias sent to the agent in prompts |
| `judge` | `false` | Enable LLM-as-judge scoring |
| `gatewayAPIKeySecret` | `""` | K8s Secret name for `HELPDESK_CLIENT_API_KEY` |
| `dbPasswordSecret.name` | `""` | K8s Secret name for DB password (`PGPASSWORD`) |
| `ttlSecondsAfterFinished` | `172800` | Job retention (48 h) |

The CronJob reuses the `faulttest` ServiceAccount (created when `faulttest.rbac.create=true`),
so no additional RBAC is needed.

> **Note on the test database.** The `faulttest-db` PostgreSQL instance used for certification
> should be a dedicated, non-production database. All faults in the external-compatible catalog
> use standard SQL injection (no OS-level or container-level commands) and restore the database
> to its pre-fault state on teardown, but production databases must never be used as injection
> targets. The `--infra-config` safety check enforces this: only databases with a `test` or
> `chaos` tag in `infrastructure.json` are permitted as injection targets.

---

## 6. Reading the per-run output

During a certification run, faulttest prints a compact one-liner per repetition:

```
--- Testing: Lock contention / deadlock (db-lock-contention) — 5 runs ---

  Run 1/5
  [PASS] score=95%
         [PRIMARY 92%] Transaction lock chain — pg_sleep blocking root holder
         [REJECTED 8%] Deadlock — mutual wait cycle

  Run 2/5
  [PASS] score=88%
         [PRIMARY 89%] Transaction lock chain — pg_sleep blocking root holder

  Run 3/5
  [FAIL] score=55% protocol-violation
         [PRIMARY 60%] Table-level lock — DDL statement blocked by open transaction

  Run 4/5
  [PASS] score=91%
         [PRIMARY 94%] Transaction lock chain — pg_sleep blocking root holder

  Run 5/5
  [PASS] score=90%
         [PRIMARY 91%] Transaction lock chain — pg_sleep blocking root holder

Stability Report: db-lock-contention
  Runs:               5  (4 passed, 1 failed)
  Pass rate:          80.0%  [threshold: ≥80%]   ✓
  H1 confidence:      89–94%  (range: 5pp)  [threshold: ≤30pp]  ✓
  H1 conf mean:       91.5%
  Protocol violations: 1
  Verdict:            STABLE
```

The per-hypothesis lines use the same format as single-run triage output:
- `[PRIMARY X%]` — the agent's top hypothesis with its stated confidence.
- `[REJECTED X%]` — a hypothesis the agent considered and ruled out, with the rejection reason.
- `[X%]` — a secondary hypothesis without explicit rejection.

`protocol-violation` appears when the agent omitted a required `TRANSITION_TO` or `ESCALATE_TO`
signal. The run is scored at most 75%, which typically causes a FAIL and counts toward the
`Protocol violations` tally in the stability report.

---

## 7. Viewing certification results

### 7.1 `vault list` — STABLE column

```bash
faulttest vault list --gateway http://gateway:8080
```

```
FAULT                          PLAYBOOK                   LAST TEST    STATUS  SCORE  STABLE       ACCURACY
-----------------------------------------------------------------------------------------------------------------
db-max-connections             pbs_db_conn_pooling        2026-06-20   PASS    95%    STABLE(5)    100% (4/4)
db-lock-contention             pbs_lock_chain_triage      2026-06-20   PASS    91%    STABLE(5)    –
db-idle-in-transaction         pbs_db_idle_txn            2026-06-15   PASS    88%    UNSTABLE(5)  –
db-high-cache-miss             pbs_cache_miss_triage      (never)      -       –      —            –
db-table-bloat                 pbs_vacuum_triage          2026-06-01   PASS    90%    STABLE(5) 21d –
```

| STABLE column | Meaning |
|---------------|---------|
| `STABLE(N)` | Certified STABLE in the last N runs |
| `STABLE(N) Xd` | STABLE but cert is X days old (shown after 14 days as an age reminder) |
| `UNSTABLE(N)` | Certified UNSTABLE in the last N runs — playbook needs attention |
| `—` | No certification run has been posted for this fault |

### 7.2 `vault accuracy` — full cert detail

When called with a fault ID (rather than a playbook series ID), `vault accuracy` shows the
full stability cert alongside the accuracy breakdown:

```bash
faulttest vault accuracy db-lock-contention --gateway http://gateway:8080
```

```
Accuracy: db-lock-contention → pbs_lock_chain_triage
  At-gate feedback:      4 runs   75% accurate (3/4)
  Post-incident:         2 runs  100% accurate (2/2)
  Combined:              6 runs   83% accurate

Stability Cert: db-lock-contention
  Fault:         Lock contention / deadlock
  Playbook:      pbs_lock_chain_triage
  Model:         claude-haiku-4-5-20251001
  Runs:          5
  Pass rate:     80.0%
  Conf range:    5pp  (H1 on passing runs)
  Verdict:       STABLE
  Tested:        2026-06-20T03:14:22Z
```

If the cert is older than 30 days, a warning is shown:

```
  ⚠  WARN: cert is 47 days old — re-certify if the model or playbook has changed
```

---

## 8. When to re-certify

A stability cert is tied to the model and playbook version at the time of the run. Any of the
following changes should trigger a re-certification run:

| Trigger | Why |
|---------|-----|
| **Model upgrade** | Different model weights produce different output distributions. A playbook STABLE under `claude-haiku-4-5-20251001` may be UNSTABLE under a newer release — or may become more stable. The cert includes the model field so `vault accuracy` can flag stale certs. |
| **Significant playbook edit** | Adding or removing hypothesis format fields, changing escalation conditions, or rewording diagnostic guidance can shift confidence and pass rates materially. Minor wording edits typically don't require re-certification. |
| **Significant infrastructure change** | If the agent's tool catalog changes (new tools added, existing tools removed) or the database configuration drifts significantly from the state at certification time, prior certs may no longer reflect real-world behaviour. |
| **Cert age > 30 days** | Shown as a warning in `vault accuracy`. Not a hard expiry — a STABLE cert doesn't automatically become invalid — but a prompt to re-verify on a realistic schedule. |

The recommended cadence for established deployments is a weekly CronJob (see [Kubernetes](#54-kubernetes-job--cronjob))
covering all external-compatible faults. This is approximately 85 LLM calls against 17 faults
at 5 runs each, typically completing in 30–40 minutes overnight.

For rapid iteration during playbook development, use a single-fault run with `--repeat 3` as a
fast feedback loop before committing and run the full `--repeat 5` certification before activating.

---

## 9. Relationship to other quality signals

aiHelpDesk provides several quality measurement tools that address different questions. They
are complementary, not redundant.

| Signal | Question answered | When it runs |
|--------|-------------------|--------------|
| **Consistency cert** (this doc) | Is this playbook stable enough to trust in production? | Pre-promotion gate; re-run on model/playbook change |
| **[Benchmarking](BENCHMARKING.md)** | How much does aiHelpDesk's scaffolding improve over a bare LLM? | One-time or after major scaffolding changes |
| **[LLM-as-judge](LLM_AS_JUDGE.md)** | Is the diagnosis semantically correct on this specific run? | During faulttest runs (opt-in `--judge`) |
| **[Vault calibration](VAULT.md#vault-calibration)** | When the agent says 90%, is it right 90% of the time? | After accumulating ≥10 runs with operator feedback |
| **[Vault accuracy](VAULT.md#vault-accuracy)** | What fraction of diagnoses are confirmed correct by operators? | Continuously from production incidents and faulttest gateway runs |

**The dependency chain:** consistency certification is a prerequisite for calibration to be
meaningful. A playbook that is UNSTABLE contributes noisy data to the calibration curve — the
confidence spread between runs cannot be distinguished from calibration error. Certifying
STABLE before promoting a playbook means the calibration signal is clean. Once the signal is
clean, `vault calibration` can tell you whether confidence is overconfident, underconfident,
or well-calibrated — and `vault accuracy` can tell you whether the consistent diagnosis is
actually correct.

In summary: consistency is not a replacement for accuracy measurement and accuracy measurement
is not a replacement for consistency certification. The full quality picture requires both,
in that order.
