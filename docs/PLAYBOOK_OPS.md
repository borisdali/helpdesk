# aiHelpDesk Playbook Operations Guide

Operational best practices for taking advantage of aiHelpDesk [playbooks](PLAYBOOKS.md) effectively. This guide covers what to set up before an incident occurs and how to work the investigation workflow during one.

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

The three database availability playbooks form a directed chain. Always start at the entry point:

```
pbs_db_restart_triage  (entry_point: true)
        │
        │  evidence: FATAL / PANIC in logs, CrashLoopBackOff due to config error
        ▼
pbs_db_config_recovery
        │
        │  evidence: PANIC, checksum failure, invalid page
        ▼
pbs_db_pitr_recovery
```

- **Start at `pbs_db_restart_triage` every time.** It classifies the failure and either resolves it (OOM kill, clean shutdown) or tells you which playbook to run next via `escalation_hint`.
- **Do not jump to `pbs_db_config_recovery` or `pbs_db_pitr_recovery` without running restart triage first**, unless you already have clear `FATAL`/`PANIC` evidence.

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
jq '{run_id, escalation_hint}' /tmp/run1.json
```

Save the `run_id` — you will need it for continuity threading in step 3.

### 2.3 Step 3 — Follow the escalation hint

If the agent signals `escalation_hint`, run the next playbook with `prior_run_id` set to the first run's ID. This injects the first run's findings into the second run's prompt, so the agent picks up where the previous one left off rather than starting fresh.

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

### 2.4 Step 4 — Record the outcome

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

---

## 3. Outcome hygiene

`resolution_rate` in playbook stats is only meaningful if runs don't accumulate as `unknown`. The two common causes:

| Situation | Action |
|---|---|
| Agent ran but `outcome` stayed `unknown` | Check `findings_summary` on the run — if the agent produced a conclusion, the auto-parse may have failed. PATCH the outcome manually. |
| Investigation started but not completed | PATCH to `abandoned`. This is expected for incidents that self-resolve before diagnosis is complete. |
| Wrong playbook was selected | PATCH to `abandoned` on both runs, note the correct playbook in `findings_summary`. |

A healthy playbook should show `resolution_rate > 0.7` after 10+ runs. Below that, review recent `findings_summary` values — a low rate usually means either the agent isn't producing parseable signals or the playbook is being triggered for the wrong problem class.


See [here](PLAYBOOKS.md#record-an-outcome) for the patching instructions.


---

## 4. Quick-reference checklist

**Before an incident:**
- [ ] Scheduled `get_baseline` fleet job running every 6 hours on all DB servers
- [ ] Verified `get_saved_snapshots` returns data for at least one recent run per target
- [ ] Playbook series IDs noted for your on-call runbook

**During a DB-down incident:**
- [ ] Collect 50–100 lines from the PostgreSQL log
- [ ] Identify the key error line (FATAL/PANIC/connection refused)
- [ ] Start at `pbs_db_restart_triage` (always the entry point)
- [ ] Pass the log line in `context`
- [ ] Save `run_id` from the response
- [ ] If `escalation_hint` is set, run the next playbook with `prior_run_id`

**After resolution:**
- [ ] Confirm `outcome` is `resolved` (auto-set for agent runs, manual PATCH for fleet runs)
- [ ] If not auto-set, PATCH with `findings_summary` describing what was found and fixed
- [ ] If abandoned, PATCH to `abandoned` so stats stay accurate
