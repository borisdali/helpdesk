# aiHelpDesk Playbook Operations Guide

Operational best practices for taking advantage of aiHelpDesk [Playbooks](PLAYBOOKS.md) effectively. This guide covers what to set up before an incident occurs and how to work the investigation workflow during one.

> **Every resolved incident improves the Vault.** When you close a playbook run with `outcome="resolved"`, the Gateway automatically synthesises a playbook draft from the audit trace and saves it to the Vault for your review. Over time, this turns each incident into an institutional memory contribution — see the [Operational SRE/DBA Flywheel](VAULT.md) flow.

---

## 1. Pre-incident preparation

### 1.1 Schedule regular baselines (required for DB-down diagnosis)

The database agent's `get_saved_snapshots` tool retrieves previously recorded tool outputs from the audit history. During a DB-down investigation the agent uses this to find `data_directory`, `config_file`, `log_directory`, and prior `pg_settings`. This information may not be easily accessible when a database is down.

**If no baseline has ever been captured, `get_saved_snapshots` returns nothing and the agent is limited to infrastructure config alone.**

Set up a scheduled fleet job that runs `get_baseline` against all database servers at least twice a day (morning/evening), but the prefered interval (and the default) is four times a day. `get_baseline` collects server info, non-default pg_settings, extensions, and disk usage in a single call and stores the result in the audit trail.

**Job file — `jobs/db-baseline.json`:**

```json
{
  "job_id": "db-baseline-scheduled",
  "description": "Scheduled baseline capture for all database servers",
  "targets": {},
  "steps": [
    {
      "tool":  "get_baseline",
      "agent": "postgres_database_agent",
      "args":  {}
    }
  ]
}
```

`"targets": {}` with no `tags` or `names` selects all servers in `infrastructure.json`. If you want only a specific environment:

```json
"targets": { "tags": ["production"] }
```

**Refresh tool snapshots once, then schedule:**

```bash
# One-time: bake in tool_snapshots so drift detection works
./fleet-runner --job-file jobs/db-baseline.json --refresh-snapshots

# Test run
./fleet-runner --job-file jobs/db-baseline.json --dry-run

# Then add to cron (host deployment):
# 0 */6 * * *  /opt/helpdesk/fleet-runner --job-file /opt/helpdesk/jobs/db-baseline.json \
#               --gateway http://localhost:8080 --api-key $(cat /etc/helpdesk/fleet-runner.key)
```

**Kubernetes CronJob (Helm values):**

```yaml
fleetRunner:
  enabled: true
  schedule: "0 */6 * * *"   # every 6 hours
  jobFile: "/etc/helpdesk/db-baseline.json"
  apiKeySecret: fleet-runner-key
  apiKeyKey: api-key
  extraVolumes:
    - name: baseline-job
      configMap:
        name: db-baseline-job
  extraVolumeMounts:
    - name: baseline-job
      mountPath: /etc/helpdesk/db-baseline.json
      subPath: db-baseline.json
      readOnly: true
```

**Verify snapshots exist before you need them:**

```bash
# Ask the agent directly — if it returns data, you're covered
curl -s -H "X-User: ops@example.com" -H "X-Purpose: diagnostic" \
  http://localhost:8080/api/v1/query \
  -H "Content-Type: application/json" \
  -d '{"query": "Show me the most recent get_baseline snapshot for prod-db-1"}' \
  | jq -r '.text'
```

Or check via the audit trail:

```bash
curl -s -H "X-User: ops@example.com" \
  "http://localhost:8080/api/v1/audit/events?tool_name=get_baseline&limit=5" | jq .
```

### 1.2 Understand the DB-down escalation chain

The database availability playbooks form a directed chain. Always start at the entry point:

```
pbs_db_restart_triage  (entry_point: true)
        │
        ├─ Kubernetes DB ─────────────────────────────────────────────
        │       │  evidence: FATAL / PANIC in pod logs, CrashLoopBackOff
        │       ▼
        │   pbs_db_config_recovery
        │       │  evidence: PANIC, checksum failure, invalid page
        │       ▼
        │   pbs_db_pitr_recovery
        │
        └─ Docker-hosted DB ──────────────────────────────────────────
                │  DB agent cannot read docker logs
                ▼
            pbs_sysadmin_docker_inspect  ← approval_mode: session
                (SysAdmin agent: check_host + get_host_logs,
                 confirms or revises prior hypothesis)
                │  exitcode=0 + clean shutdown confirmed
                ▼
            pbs_db_restart_action  ← approval_mode: manual (explicit only)
                (SysAdmin agent: restart_container + verify)
```

- **Start at `pbs_db_restart_triage` every time.** It classifies the failure and either resolves it or emits `ESCALATE_TO:` pointing to the next playbook.
- For **Docker-hosted** databases, the DB agent escalates to `pbs_sysadmin_docker_inspect` automatically — it cannot inspect the container without docker tools. The SysAdmin agent determines whether the container stopped cleanly, crashed, or was OOM-killed and adjusts the diagnosis accordingly.
- If the inspection confirms a condition that warrants a restart, `pbs_sysadmin_docker_inspect` escalates to `pbs_db_restart_action` (the remediation step). Because `pbs_db_restart_action` has `approval_mode: manual`, the gateway returns `suggested_next` for this step with `auto` or `session` — it requires the operator to explicitly invoke it, or use `approval_mode=force` to authorize the full chain up front.
- **Do not jump to `pbs_db_config_recovery` or `pbs_db_pitr_recovery` without running restart triage first**, unless you already have clear `FATAL`/`PANIC` evidence.

### 1.2a Auto-chaining vs. manual escalation

By default, playbooks run with `approval_mode=manual` (no mutations, no auto-chaining). The operator receives `suggested_next` in the response and fires the next call themselves. This is the safe default for production.

Auto-chaining is controlled by **two independent gates**:

1. The **requester's `approval_mode`** (or session) must authorise escalation.
2. The **target playbook's own `approval_mode`** must be `session` or `auto`. Playbooks with `approval_mode: manual` (or unset) can never be auto-chained — the gateway always returns `suggested_next` for them, regardless of the requester's mode.

This means `approval_mode=auto` auto-chains **diagnostic** steps (like `pbs_sysadmin_docker_inspect`, which has `approval_mode: session`) but **not** remediation steps (like `pbs_db_restart_action`, which has `approval_mode: manual`). The operator always receives `suggested_next` for the restart step and must invoke it explicitly after reviewing the diagnosis.

To automate the entire path — diagnosis **and** remediation — use `approval_mode=force`. This bypasses the playbook-level `manual` gate and chains through every step. Use it in non-production environments or when you have already reviewed the escalation path and accept full responsibility for the outcome.

**With `approval_mode=auto`, the Docker-hosted DB flow produces:**
- A `chain` array with 2 entries (DB triage + Docker inspection) merged into a single diagnostic response.
- A `suggested_next` field pre-filled with the `pbs_db_restart_action` request — ready for the operator to review and fire.

**With `approval_mode=force`, the Docker-hosted DB flow produces:**
- A `chain` array with 3 entries (DB triage + Docker inspection + container restart).
- No `suggested_next` — the full chain ran to completion.

```bash
# approval_mode=auto: diagnosis chains (2 steps), restart requires explicit approval
curl -s -H "X-User: ops@example.com" -H "X-Purpose: diagnostic" \
  -X POST http://localhost:8080/api/v1/fleet/playbooks/$RESTART_ID/run \
  -H "Content-Type: application/json" \
  -d '{
    "connection_string": "prod-db-1",
    "approval_mode":     "auto"
  }' | jq '{chain: [.chain[] | {step, agent_name, findings}], suggested_next}'
# → chain has 2 entries (db triage + docker inspect)
# → suggested_next points to pbs_db_restart_action for the operator to review and fire
```

To auto-chain the inspection step using a session token (e.g. for a scoped maintenance window):

```bash
SESSION=$(curl -s -X POST http://localhost:1199/v1/approval/sessions \
  -H "Content-Type: application/json" \
  -d '{
    "granted_by":      "alice@example.com",
    "expires_in_secs": 1800,
    "allowed_classes": ["escalation"],
    "scope":           "pbs_db_restart_triage"
  }' | jq -r .session_id)

curl -s -H "X-User: ops@example.com" -H "X-Purpose: diagnostic" \
  -X POST http://localhost:8080/api/v1/fleet/playbooks/$RESTART_ID/run \
  -H "Content-Type: application/json" \
  -d "{
    \"connection_string\": \"prod-db-1\",
    \"approval_mode\":     \"session\",
    \"approval_session\":  \"$SESSION\"
  }" | jq '{chain: [.chain[] | {step, agent_name, findings}], suggested_next}'
```

The inspection step chains because `pbs_sysadmin_docker_inspect` has `approval_mode: session` and the session includes `"escalation"` in `allowed_classes`. The restart step still stops at `suggested_next` because `pbs_db_restart_action` has `approval_mode: manual`.

To run the full chain end-to-end including the restart (e.g. in staging or after a pre-reviewed escalation path):

```bash
# approval_mode=force: all 3 steps run in one call, including restart_container
curl -s -H "X-User: ops@example.com" -H "X-Purpose: diagnostic" \
  -X POST http://localhost:8080/api/v1/fleet/playbooks/$RESTART_ID/run \
  -H "Content-Type: application/json" \
  -d '{
    "connection_string": "staging-db",
    "approval_mode":     "force"
  }' | jq '{chain: [.chain[] | {step, agent_name, findings}]}'
# → chain has 3 entries; no suggested_next
```

The merged `diagnostic_report` contains hypotheses from all chained agents ranked by confidence, with the last agent's root cause taking precedence.

### 1.3 Know your `requires_evidence` patterns

The config recovery and PITR recovery playbooks require log evidence before they're appropriate to run. When you call `/run` without a `context` field, you will receive warnings for all unmatched patterns.

| Playbook | Evidence patterns required |
|---|---|
| `pbs_db_config_recovery` | `FATAL.*invalid value for parameter`, `FATAL.*configuration file`, `FATAL.*could not open file` |
| `pbs_db_pitr_recovery` | `PANIC.*could not locate a valid checkpoint`, `database files are incompatible with server`, `invalid page.*could not read block` |

Always pass relevant log lines in the `context` field of your run request. This is not a gate — the run proceeds regardless — but it removes warnings and gives the agent a confirmed hypothesis to start from.

---

## 2. Running a DB-down investigation

### 2.1 Step 1 — Collect the immediate evidence

Before triggering any playbook, grab the last 50–100 lines of the PostgreSQL log. On Kubernetes:

```bash
kubectl logs <pod-name> --tail=100 > /tmp/db-failure.log
cat /tmp/db-failure.log
```

On a VM/host:

```bash
tail -100 /var/lib/postgresql/data/log/postgresql-*.log > /tmp/db-failure.log
```

Identify the most relevant line. Common patterns and what they mean:

| Log line | Meaning | Start with |
|---|---|---|
| `connection refused` (no log) | Process not running | `pbs_db_restart_triage` |
| `database system was shut down` | Clean shutdown | `pbs_db_restart_triage` |
| `OOM kill` / `out of memory` | OOM kill | `pbs_db_restart_triage` |
| `FATAL: invalid value for parameter` | Bad config value | start with `pbs_db_restart_triage`, but likely proceed with `pbs_db_config_recovery` |
| `FATAL: could not open file "postgresql.conf"` | Config file missing/corrupt | start with `pbs_db_restart_triage`, but likely proceed with `pbs_db_config_recovery` |
| `PANIC: could not locate a valid checkpoint` | WAL corruption | start with `pbs_db_restart_triage`, but likely proceed with `pbs_db_pitr_recovery` |
| `invalid page in block` / `checksum failure` | Data corruption | start with `pbs_db_restart_triage`, but likely proceed with `pbs_db_pitr_recovery` |

### 2.2 Step 2 — Trigger the entry-point playbook

```bash
# Get the restart triage playbook ID
RESTART_ID=$(curl -s -H "X-User: ops@example.com" \
  http://localhost:8080/api/v1/fleet/playbooks \
  | jq -r '.playbooks[] | select(.series_id=="pbs_db_restart_triage") | .id')

# Run with context (paste the key log line)
curl -s -H "X-User: ops@example.com" -H "X-Purpose: diagnostic" \
  -X POST http://localhost:8080/api/v1/fleet/playbooks/$RESTART_ID/run \
  -H "Content-Type: application/json" \
  -d '{
    "connection_string": "prod-db-1",
    "context": "FATAL: invalid value for parameter \"max_connections\" in file \"/etc/postgresql/postgresql.conf\" line 42"
  }' > /tmp/run1.json

# Read the diagnosis
jq -r '.text' /tmp/run1.json
echo "---"
jq '{run_id, escalation_hint, suggested_next}' /tmp/run1.json
```

Save the `run_id` — you will need it for continuity threading in step 3.

If `suggested_next` is present, the gateway has prepared the full request body for the follow-on playbook. You can fire it directly (see step 3) or let the gateway auto-chain it by re-running with `approval_mode=auto`.

### 2.3 Step 3 — Follow the escalation

**Option A — manual (default, production-safe):** use the `suggested_next` field from step 2.

```bash
RUN1_ID=$(jq -r .run_id /tmp/run1.json)
ESCALATION=$(jq -r '.escalation_hint // empty' /tmp/run1.json)

# Resolve the escalation target to a playbook ID
NEXT_ID=$(curl -s -H "X-User: ops@example.com" \
  http://localhost:8080/api/v1/fleet/playbooks \
  | jq -r --arg s "$ESCALATION" '.playbooks[] | select(.series_id==$s) | .id')

# Run with prior_run_id for continuity
curl -s -H "X-User: ops@example.com" -H "X-Purpose: diagnostic" \
  -X POST http://localhost:8080/api/v1/fleet/playbooks/$NEXT_ID/run \
  -H "Content-Type: application/json" \
  -d "{
    \"connection_string\": \"prod-db-1\",
    \"prior_run_id\": \"$RUN1_ID\",
    \"context\": \"FATAL: invalid value for parameter max_connections\"
  }" > /tmp/run2.json

jq -r '.text' /tmp/run2.json
jq '{run_id, escalation_hint}' /tmp/run2.json
```

Repeat for further escalations, always chaining `prior_run_id` to the immediately preceding run.

**Option B — auto-chain (single call, requires `approval_mode=auto`):** re-run step 2 with `"approval_mode": "auto"`. The gateway runs both agents and returns the merged result. This is appropriate in pre-production or when you have already reviewed the escalation path and trust both agents.

```bash
curl -s -H "X-User: ops@example.com" -H "X-Purpose: diagnostic" \
  -X POST http://localhost:8080/api/v1/fleet/playbooks/$RESTART_ID/run \
  -H "Content-Type: application/json" \
  -d '{
    "connection_string": "prod-db-1",
    "approval_mode":     "auto"
  }' | jq '{chain: [.chain[] | {step, agent_name, findings}], suggested_next, diagnostic_report}'
```

### 2.4 Step 4 — Record the outcome and trigger draft synthesis

Agent-mode runs auto-record `outcome` and `findings_summary` from the agent's structured response. Verify:

```bash
RUN_ID=$(jq -r .run_id /tmp/run2.json)
curl -s -H "X-User: ops@example.com" \
  http://localhost:8080/api/v1/fleet/playbook-runs/$RUN_ID \
  | jq '{outcome, findings_summary, escalated_to}'
```

If the incident was resolved by operator action after the playbook completed, or if you abandoned the investigation, patch the run:

```bash
# Resolved
curl -s -H "X-User: ops@example.com" \
  -X PATCH http://localhost:8080/api/v1/fleet/playbook-runs/$RUN_ID \
  -H "Content-Type: application/json" \
  -d '{"outcome": "resolved", "findings_summary": "Removed invalid max_connections override from postgresql.conf and restarted."}'

# Abandoned (alert self-cleared, wrong playbook, etc.)
curl -s -H "X-User: ops@example.com" \
  -X PATCH http://localhost:8080/api/v1/fleet/playbook-runs/$RUN_ID \
  -H "Content-Type: application/json" \
  -d '{"outcome": "abandoned"}'
```

When a run is closed with `outcome="resolved"` or `outcome="escalated"` and the Gateway has auditd configured, a playbook draft is **automatically synthesised** from the audit trace and saved to the Vault as an inactive draft. You do not need to do anything to trigger this — it happens as a side-effect of recording the outcome.

### 2.5 Step 5 — Review the Vault draft

After closing the incident, check for the pending draft and decide whether to activate it:

```bash
# See if a draft was generated for this series
curl -s "http://localhost:8080/api/v1/fleet/playbooks?source=generated" \
  -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" \
  | jq '.playbooks[] | select(.is_active == false) | {id, name, created_at}'

# Inspect the draft
curl -s http://localhost:8080/api/v1/fleet/playbooks/<playbook_id> \
  -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" | jq .

# Activate if it captures the right approach
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/<playbook_id>/activate \
  -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY"

# Or discard if it's not useful
curl -s -X DELETE http://localhost:8080/api/v1/fleet/playbooks/<playbook_id> \
  -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY"
```

Before activating, consider running the draft through `faulttest run --remediate` against staging to validate it actually resolves the fault class it claims to fix. An activated playbook becomes the version used for all future runs in its series.

---

## 3. Outcome hygiene

`resolution_rate` in playbook stats is only meaningful if runs don't accumulate as `unknown`. The two common causes:

| Situation | Action |
|---|---|
| Agent ran but `outcome` stayed `unknown` | Check `findings_summary` on the run — if the agent produced a conclusion, the auto-parse may have failed. PATCH the outcome manually. |
| Investigation started but not completed | PATCH to `abandoned`. This is expected for incidents that self-resolve before diagnosis is complete. |
| Wrong playbook was selected | PATCH to `abandoned` on both runs, note the correct playbook in `findings_summary`. |

A healthy playbook should show `resolution_rate > 0.7` after 10+ runs. Below that, review recent `findings_summary` values — a low rate usually means either the agent isn't producing parseable signals or the playbook is being triggered for the wrong problem class.

Outcome quality also directly affects draft quality. The `from-trace` synthesis uses `findings_summary` as context — a detailed summary ("removed invalid max_connections override from postgresql.conf, restarted") produces a more actionable draft than an empty or generic one.

See [here](PLAYBOOKS.md#record-an-outcome) for the patching instructions.


---

## 4. Quick-reference checklist

**Before an incident:**
- [ ] Scheduled `get_baseline` fleet job running every 6 hours on all DB servers
- [ ] Verified `get_saved_snapshots` returns data for at least one recent run per target
- [ ] Playbook series IDs noted for your on-call runbook

**During a DB-down incident:**
- [ ] Collect 50–100 lines from the PostgreSQL log (for K8s/VM; for Docker, the SysAdmin agent will do this)
- [ ] Identify the key error line (FATAL/PANIC/connection refused)
- [ ] Start at `pbs_db_restart_triage` (always the entry point)
- [ ] Pass the log line in `context`
- [ ] Save `run_id` from the response
- [ ] If `suggested_next` is present: either fire the next playbook manually (manual mode) or re-run with `approval_mode=auto` for a single-call chained investigation
- [ ] For Docker-hosted DBs with `approval_mode=auto`: the gateway auto-chains through `pbs_sysadmin_docker_inspect` (2-step diagnosis) and returns `suggested_next` for `pbs_db_restart_action` — ensure the SysAdmin agent is running
- [ ] `pbs_db_restart_action` always requires explicit invocation (`approval_mode: manual`) — review the `chain` diagnosis before firing the restart

**After resolution:**
- [ ] Confirm `outcome` is `resolved` (auto-set for agent runs, manual PATCH for fleet runs)
- [ ] If not auto-set, PATCH with `findings_summary` describing what was found and fixed
- [ ] If abandoned, PATCH to `abandoned` so stats stay accurate
- [ ] Check for a pending Vault draft (`GET /api/v1/fleet/playbooks?source=generated`)
- [ ] Review the draft — activate if it captures the right approach, discard otherwise
- [ ] Optionally validate the draft against staging via `faulttest run --remediate` before activating

---

## 5. See also

| Document | What it covers |
|----------|----------------|
| [INCIDENTS.md](INCIDENTS.md) | What an Incident is; how real and injected Incidents feed the Vault; bundle anatomy |
| [VAULT.md](VAULT.md) | The Operational SRE/DBA Flywheel, how drafts enter the Vault, vault commands (`vault list`, `vault status`, `vault drift`, `vault suggest`) |
| [PLAYBOOKS.md](PLAYBOOKS.md) | Playbook schema, CRUD API, import formats, system playbooks |
| [FAULTTEST.md](FAULTTEST.md) | Fault injection CLI, fault catalog, remediation mode, LLM-as-judge scoring |
| [FLEET.md](FLEET.md) | Fleet runner, job definitions, scheduled baseline capture |
| [API.md](API.md) | Full REST API reference including `/fleet/playbooks/from-trace` |
