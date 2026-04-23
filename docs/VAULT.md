# aiHelpDesk Vault

The Vault is aiHelpDesk's institutional memory for operational knowledge. It is where every Playbook lives, where every incident trace lands, and where the library of known fault→remedy pairings grows. Automatically and with human approval at the gate.

A traditional runbook is a static procedure — a fixed sequence of steps written once and followed literally. An aiHelpDesk [Playbook](PLAYBOOKS.md) is fundamentally different: it encodes strategic **intent** and expert **knowledge** that the fleet planner uses to generate an execution plan dynamically, against the current state of your infrastructure and tool catalog. The same Playbook produces different steps when your database configuration differs, when new tools are available, or when the environment has changed. This is what "never a stale script" means in practice.

The Vault is the library where these Playbooks live. Tracked, versioned, and continuously improved as your infrastructure, applications that make use of it and agents evolve.

---

## The Operational SRE/DBA Flywheel

The Vault is the engine of a feedback loop that tightens with every incident:

```
  ┌──────────────────────────────────────────────────────────────────┐
  │                                                                  │
  │         Fault          Agent diagnoses             Playbook      │
  │   (injected or real) ──► correctly ──────────────► remediates    │
  │          ▲                                            │          │
  │          │                                            │          │
  │          │                                            ▼          │
  │   Library improves  ◄── Human approves ◄── Draft auto-saved      │
  │     (activated)         (Vault review)        to Vault           │
  │                                                                  │
  └──────────────────────────────────────────────────────────────────┘
```

**Two selling points drive this loop:**

1. **Agents that act, not just advise.** The governed actuation arm is the baseline foundation of aiHelpDesk. In addition to the eight-module [AI Governance](AIGOVERNANCE.md) system, featuring the multi-layer AI anti-hallucinations safeguards, aiHelpDesk is equipped with a formal [Tool Registry](TOOL_REGISTRY.md), [Playbooks](PLAYBOOKS.md), [Fleet Management](FLEET.md), [Policy Engine](AIGOVERNANCE.md#3-policy-engine), [Blast Radius Guardrails](AIGOVERNANCE.md#5-guardrails), not to mentioned the hardened system prompts (through defensive engineering technique) - all to ensure  that the remedal actions are carried out safely on your real infrastructure. 

    aiHelpDesk doesn't give a hypethetical advise that it carries no responsbility for. It acts on its proposed course of action through a Playbook (with the human operator's approval, step-by-step or autonomously) and reports the results back via the feedback loop that keeps the Playbooks current and improving after every incident, real or injected.

2. **Institutional memory that compounds.** Every resolved incident automatically proposes a Playbook draft. Every faulttest pass with remediation auto-saves a draft. Human operators review and activate. The library grows. The next similar incident is handled faster, with higher confidence, because someone already did the hard thinking.

The Vault is the mechanism that makes this second point real. Without it, every operator repeats the same diagnostic steps from scratch. And in a different way, with the different mistakes. With it, the hard-won knowledge of how to fix `db-max-connections` or `db-lock-contention` formally accumulates in one place, versioned, with a known track record.

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

`faulttest vault` provides the operational window into the Vault from the command line. Run history is stored in `~/.faulttest/history.json` and is updated automatically at the end of every `faulttest run`.

### vault list

```bash
faulttest vault list [--gateway http://gateway:8080] [--api-key sk-...]
                     [--target staging-db]
```

Shows the full fault catalog alongside the linked Playbook, date of last run, and pass/fail status. When `--gateway` is provided, also verifies that referenced Playbook series IDs exist on the Gateway.

```
FAULT                            PLAYBOOK                     LAST RUN     STATUS
--------------------------------------------------------------------------------------------
db-max-connections               pbs_db_conn_pooling          2026-04-16   PASS
db-connection-refused            pbs_db_restart_triage        2026-04-15   PASS
db-pg-hba-corrupt                pbs_db_config_recovery       (never)      -
db-lock-contention               (none)                       2026-04-14   FAIL
db-idle-in-transaction           pbs_db_idle_txn              2026-04-10   NO PLAYBOOK
```

| Status | Meaning |
|--------|---------|
| `PASS` / `FAIL` | Last run result |
| `-` | Fault has a Playbook linked but has never been run against this target |
| `NO PLAYBOOK` | No `remediation.playbook_id` configured in the catalog |
| `PLAYBOOK NOT FOUND` | Playbook series ID configured but not found on the Gateway |

Use `--target` to filter history to a specific database server (the `--agent-conn` alias set during runs).

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
```

When drift is detected, run `faulttest inject` + `faulttest teardown` to reproduce the fault interactively and investigate what changed in the agent or environment.

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
| [PLAYBOOK_OPS.md](PLAYBOOK_OPS.md) | Operational best practices for authoring and running Playbooks |
| [FAULTTEST.md](FAULTTEST.md) | Full faulttest CLI reference, fault catalog, scoring, remediation |
| [FLEET.md](FLEET.md) | Fleet runner, job definitions, schema drift, planner |
| [API.md](API.md) | Full REST API reference including `/fleet/playbooks/from-trace` |
