# aiHelpDesk Playbooks

A **playbook** is a saved runbook artifact that combines a natural-language fleet intent with expert triage knowledge. Running a playbook calls the fleet planner fresh every time, producing a plan against the current tool catalog and live infrastructure state — never a stale script.

Playbooks are the primary authoring unit for fleet operations. System playbooks ship with aiHelpDesk and cover the most common database triage scenarios out of the box. Operators can author custom playbooks from scratch or import existing runbooks from Markdown, plain text, YAML, Rundeck, or Ansible formats.

---

## Concepts

### Intent vs. knowledge

Every playbook carries two classes of fields:

**Intent fields** — drive the planner directly:

| Field | Purpose |
|---|---|
| `name` | Human-readable name |
| `description` | Passed verbatim to the fleet planner as the plan intent |
| `target_hints` | Tag names or server name patterns to narrow target resolution |

**Knowledge fields** — enrich authoring, selection, and execution:

| Field | Type | Purpose |
|---|---|---|
| `problem_class` | string | `performance` \| `availability` \| `capacity` \| `data_integrity` \| `security` |
| `symptoms` | []string | Observable indicators that should trigger this playbook |
| `guidance` | string | Expert reasoning injected into the planner prompt at run time |
| `escalation` | []string | Conditions under which the agent must stop and escalate to a human |
| `related_playbooks` | []string | `pb_*` IDs of related playbooks |
| `author` | string | Author identity or team name |
| `version` | string | Free-form version string (e.g. `"1.2"`) |

The `guidance` field is the most important knowledge field. It is injected into the planner prompt as a `## Playbook Guidance` section whenever the playbook is run. Use it for expert heuristics, prioritisation notes, tool sequencing hints, and common misdiagnosis warnings. It does not appear in ad-hoc `/fleet/plan` calls.

### Versioning

Each playbook belongs to a **series** identified by `series_id` (a stable `pbs_` prefixed identifier). A series can have multiple versions but exactly one is **active** at any time. The active version is the one selected when the playbook is run.

| Field | Meaning |
|---|---|
| `series_id` | Stable identifier shared across all versions of the same playbook (auto-generated as `pbs_<uuid[:8]>` if omitted on create) |
| `is_active` | `true` for the version that runs when the playbook is invoked |
| `is_system` | `true` for playbooks shipped with aiHelpDesk (read-only via API) |
| `source` | `system` (shipped), `imported` (import endpoint), or `manual` (API-created) |

When you create a playbook without specifying a `series_id`, a new series is started and the playbook is immediately active. When you supply an existing `series_id`, the new version is **inactive by default** — you promote it explicitly via the activate endpoint. This lets you author and review a new version before it takes effect.

### System playbooks

aiHelpDesk ships 7 expert-authored system playbooks that are seeded into auditd on startup:

| Series ID | Name | Problem class | Key tools |
|---|---|---|---|
| `pbs_vacuum_triage` | Vacuum & Bloat Triage | capacity | `get_vacuum_status`, `get_disk_usage`, `get_pg_settings` |
| `pbs_slow_query_triage` | Slow Query Triage | performance | `get_slow_queries`, `get_wait_events`, `get_blocking_queries`, `explain_query` |
| `pbs_connection_triage` | Connection & Lock Triage | availability | `get_server_info`, `get_blocking_queries`, `get_session_info`, `get_lock_info` |
| `pbs_replication_lag` | Replication Lag Triage | availability | `get_replication_status`, `get_server_info` |
| `pbs_db_restart_triage` | Database Down — Restart Triage | availability | `check_connection`, `get_pod_status`, `get_pod_logs`, `get_events`, `read_pg_log`, `read_uploaded_file`, `restart_deployment` |
| `pbs_db_config_recovery` | Database Down — Configuration Recovery | availability | `get_pod_logs`, `get_events`, `get_pg_settings`, `read_pg_log`, `read_uploaded_file`, `restart_deployment` |
| `pbs_db_pitr_recovery` | Database Down — Backup Restore & PITR | availability | `check_connection`, `get_pod_logs`, `get_events`, `read_pg_log`, `read_uploaded_file` |

The three "Database Down" playbooks form an escalating sequence. When a database is completely unreachable, begin with **Restart Triage** to classify the failure from pod logs. If the logs reveal a configuration error, proceed to **Configuration Recovery**. If they reveal data corruption or missing files, escalate immediately to **Backup Restore & PITR**, which always requires human DBA involvement.

Because psql-based tools cannot reach a down database, all three playbooks rely on K8s tools (`get_pod_logs`, `get_events`) for live diagnostics, and on `get_saved_snapshots` to retrieve values captured in prior fleet-runner baselines — such as `data_directory`, `config_file`, `hba_file`, and `log_directory` — without a live connection. The agent calls `get_saved_snapshots(tool_name="get_baseline", server_name=<target>)` to find these paths from the most recent recorded snapshot.

For databases running on bare-metal hosts (no Kubernetes), `get_pod_logs` is unavailable. In that case the agent will attempt `read_pg_log`, which reads the PostgreSQL log directly via `pg_read_file()` — but this too requires a live DB connection. When the database is completely down and unreachable, an operator must retrieve the log file manually (e.g. via SSH or a jump host) and upload it with `POST /api/v1/fleet/uploads`. The agent then reads it using `read_uploaded_file` with the returned `upload_id`. See [Operator file uploads](API.md#operator-file-uploads) in the API reference.

System playbooks are **read-only**: `PUT` and `DELETE` return `400 Bad Request`. To customise one, run it as-is, or import and save your own version in the same series (the activate endpoint then lets you promote your version).

Seeding is idempotent — restarting auditd never duplicates system playbooks. If a newer version of a system playbook ships with an aiHelpDesk upgrade, it is inserted as an **inactive** version so customers can review and promote it when ready.

---

## API

All playbook endpoints are accessible via the Gateway on port 8080. The Gateway proxies CRUD and activation calls to auditd; the import endpoint is handled entirely within the Gateway (no auditd round-trip for the LLM extraction path).

### List playbooks

```
GET /api/v1/fleet/playbooks
```

Returns the active version of every playbook (system and user), ordered by creation time.

```bash
curl http://localhost:8080/api/v1/fleet/playbooks | jq .playbooks
```

**Query parameters:**

| Parameter | Default | Description |
|---|---|---|
| `active_only` | `true` | Set to `false` to include all versions in a series, not just the active one |
| `include_system` | `true` | Set to `false` to hide system playbooks |
| `series_id` | — | Filter to a single series (all versions when combined with `active_only=false`) |

```bash
# All versions of a specific series
curl "http://localhost:8080/api/v1/fleet/playbooks?series_id=pbs_vacuum_triage&active_only=false"

# User-authored only
curl "http://localhost:8080/api/v1/fleet/playbooks?include_system=false"
```

Response:
```json
{
  "playbooks": [
    {
      "playbook_id":    "pb_a1b2c3d4",
      "series_id":      "pbs_vacuum_triage",
      "name":           "Vacuum & Bloat Triage",
      "version":        "1.0",
      "is_active":      true,
      "is_system":      true,
      "source":         "system",
      "problem_class":  "capacity",
      "execution_mode": "fleet",
      "entry_point":    false,
      "description":    "...",
      "guidance":       "...",
      "symptoms":       ["..."],
      "escalation":     ["..."],
      "created_at":     "2026-04-02T00:00:00Z",
      "updated_at":     "2026-04-02T00:00:00Z",
      "stats": {
        "series_id":       "pbs_vacuum_triage",
        "total_runs":      12,
        "resolved":        10,
        "escalated":        1,
        "abandoned":        1,
        "resolution_rate":  0.833,
        "escalation_rate":  0.083,
        "last_run_at":     "2026-04-03T10:05:00Z"
      }
    }
  ]
}
```

The `stats` field is included inline on every playbook that has at least one recorded run. It is `null` / omitted for playbooks that have never been run. Stats are series-wide (all versions of a playbook combined). To get stats for a specific playbook separately, use `GET /api/v1/fleet/playbooks/{playbookID}/stats`.

### Get a playbook

```
GET /api/v1/fleet/playbooks/{playbookID}
```

Returns a single playbook by its `playbook_id`. Returns `404` if not found.

### Create a playbook

```
POST /api/v1/fleet/playbooks
```

Creates a new playbook. `name` and `description` are required.

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks \
  -H "Content-Type: application/json" \
  -d '{
    "name":          "weekly-staging-health",
    "description":   "Check connection health and table statistics on all staging databases",
    "target_hints":  ["staging"],
    "problem_class": "availability",
    "symptoms":      ["connection timeouts", "high error rate on staging"],
    "guidance":      "Start with check_connection. High dead-tuple counts on staging often mean autovacuum is disabled for testing — confirm before escalating.",
    "escalation":    ["active_connections >= max_connections"],
    "author":        "alice@example.com",
    "version":       "1.0"
  }'
```

Response: `201 Created` with the full playbook object. A `series_id` and `playbook_id` are generated automatically. `is_active` is set to `true`.

To create a second version in an existing series, include `series_id` in the body. The new version starts inactive:

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks \
  -H "Content-Type: application/json" \
  -d '{
    "series_id":   "pbs_a1b2c3d4",
    "name":        "weekly-staging-health",
    "description": "Check connection health, table stats, and replication on all staging databases",
    "version":     "1.1",
    "guidance":    "Also run get_replication_status if the server has replicas.",
    "author":      "alice@example.com"
  }'
```

### Update a playbook

```
PUT /api/v1/fleet/playbooks/{playbookID}
```

Replaces all fields of an existing playbook. Omitting an optional field clears it. Returns `404` if not found. Returns `400` for system playbooks (`is_system=true`).

```bash
curl -s -X PUT http://localhost:8080/api/v1/fleet/playbooks/pb_a1b2c3d4 \
  -H "Content-Type: application/json" \
  -d '{
    "name":        "weekly-staging-health",
    "description": "Check connection health and table statistics on all staging databases",
    "guidance":    "Run get_vacuum_status if table stats show bloat ratio > 20%.",
    "version":     "1.1"
  }'
```

### Activate a version

```
POST /api/v1/fleet/playbooks/{playbookID}/activate
```

Atomically promotes a playbook version to active within its series, deactivating all other versions. Idempotent: activating an already-active playbook is a no-op. Returns `404` if not found, `400` for system playbooks.

```bash
# Promote v2 — v1 automatically becomes inactive
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_v2id/activate | jq .is_active
```

Response: `200 OK` with the now-active playbook object.

### Delete a playbook

```
DELETE /api/v1/fleet/playbooks/{playbookID}
```

Deletes a playbook version. Returns `204 No Content` on success, `404` if not found, `400` for system playbooks.

### Run a playbook

```
POST /api/v1/fleet/playbooks/{playbookID}/run
```

Executes the playbook. Behaviour depends on `execution_mode`:

**`execution_mode: fleet` (default)** — generates a fresh fleet plan from the playbook's `description` and `guidance` and returns a `FleetPlanResponse` (same shape as `POST /api/v1/fleet/plan`). Requires LLM configuration.

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_a1b2c3d4/run \
  | jq -r '.job_def_raw' > /tmp/plan.json

# Review the plan, then execute:
./fleet-runner --job-file /tmp/plan.json --dry-run
./fleet-runner --job-file /tmp/plan.json
```

**`execution_mode: agent`** — routes to the database agent as an agentic triage session. The agent gathers evidence, forms hypotheses, backs out when evidence contradicts them, and returns a diagnosis with recommended (not executed) remediation steps. Returns the same response shape as `POST /api/v1/query`.

Optional request body:

| Field | Description |
|---|---|
| `connection_string` | PostgreSQL DSN for the target database |
| `context` | Free-form operator context (server name, symptoms, recent changes, relevant log lines) |
| `context_id` | A2A session ID for multi-turn continuity within an existing session |
| `prior_run_id` | `plr_*` run ID of a previous investigation to continue from (see [Continuity threading](#continuity-threading)) |

```bash
# Triage a down database (agent mode)
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_restart_triage/run \
  -H "Content-Type: application/json" \
  -d '{"connection_string":"postgres://prod-db.example.com/app","context":"pod in CrashLoopBackOff since 10:00 UTC"}' \
  | jq .text
```

**Run recording** — every `/run` call records a `PlaybookRun` entry in auditd before the LLM or agent is invoked. The run starts with `outcome=unknown`. For agent-mode runs the Gateway parses the agent's structured response and updates the outcome automatically — see [Structured escalation signal](#structured-escalation-signal). Operators can always override the outcome via `PATCH /playbook-runs/{runID}`. See [Run tracking](#run-tracking) below.

**Requires-evidence warnings** — if the playbook has `requires_evidence` patterns and the provided `context` does not contain matching log lines or error text, the response includes a `warnings` array:

```json
{
  "text": "...",
  "warnings": [
    "expected evidence pattern not found in provided context: \"FATAL.*invalid value for parameter\""
  ]
}
```

Warnings are soft — execution is not blocked. They signal that you may be running the wrong playbook for the observed failure mode. Providing relevant log lines in `context` removes the warning if the pattern matches.

See [FLEET.md](FLEET.md#natural-language-job-planner) for full fleet planner semantics.

---

## Run tracking

Every call to `POST /run` writes a `PlaybookRun` record to auditd before routing to the fleet planner or database agent. This gives operators a complete audit trail of what was investigated, when, and with what outcome.

### Run lifecycle

**Fleet mode:**
```
POST /run → outcome=unknown (run recorded)
                    ↓
       operator reviews plan, executes via fleet-runner
                    ↓
PATCH /playbook-runs/{runID} → outcome=resolved|escalated|abandoned
```

**Agent mode:**
```
POST /run → outcome=unknown (run recorded)
                    ↓
       agent investigates, emits FINDINGS / ESCALATE_TO signal
                    ↓
       gateway parses signal → outcome auto-updated (resolved|escalated|unknown)
                    ↓
       operator reviews diagnosis — may patch outcome to correct it
```

For agent-mode runs the Gateway parses the agent's structured response (see [Structured escalation signal](#structured-escalation-signal)) and calls `PATCH /playbook-runs/{runID}` automatically once the agent session completes. `findings_summary` and `escalated_to` are populated from the agent's output. Operators can always override via a manual PATCH.

The Gateway records run start **synchronously** and run completion **asynchronously** (best-effort, 5 s timeout). If completion recording fails, the run remains at `outcome=unknown` — this is visible in `/stats` as the `abandoned` bucket if the operator patches it, or remains `unknown` until corrected.

### Get a specific run

```
GET /api/v1/fleet/playbook-runs/{runID}
```

Returns a single `PlaybookRun` by its `run_id`. Use this to retrieve the full record after an agent session completes — in particular `findings_summary` and `escalated_to`, which are populated automatically from the agent's structured response.

```bash
curl -s http://localhost:8080/api/v1/fleet/playbook-runs/plr_3f7a2b1c | jq '{outcome, findings_summary, escalated_to}'
```

Returns `404` if the run ID is not found.

### List runs for a playbook

```
GET /api/v1/fleet/playbooks/{playbookID}/runs
```

Returns the most recent runs for a specific playbook ID (not series-wide), most recent first. Default limit is 20, maximum 100.

```bash
curl -s http://localhost:8080/api/v1/fleet/playbooks/pb_a1b2c3d4/runs | jq .
```

Response:

```json
{
  "runs": [
    {
      "run_id":          "plr_3f7a2b1c",
      "playbook_id":     "pb_a1b2c3d4",
      "series_id":       "pbs_vacuum_triage",
      "execution_mode":  "fleet",
      "outcome":         "resolved",
      "operator":        "alice@example.com",
      "started_at":      "2026-04-03T10:00:00Z",
      "completed_at":    "2026-04-03T10:05:00Z"
    }
  ],
  "count": 1
}
```

### Get run statistics

```
GET /api/v1/fleet/playbooks/{playbookID}/stats
```

Returns aggregated outcome statistics for the **series** the playbook belongs to (i.e. all versions of the playbook combined). Returns `404` if the playbook ID is not found.

```bash
curl -s http://localhost:8080/api/v1/fleet/playbooks/pb_a1b2c3d4/stats | jq .
```

Response:

```json
{
  "series_id":       "pbs_vacuum_triage",
  "total_runs":      47,
  "resolved":        38,
  "escalated":        6,
  "abandoned":        3,
  "resolution_rate":  0.809,
  "escalation_rate":  0.128,
  "last_run_at":     "2026-04-03T10:05:00Z"
}
```

Use `resolution_rate` to identify playbooks that frequently escalate — a low rate often signals that the playbook's guidance or escalation conditions need tuning.

### Record an outcome

```
PATCH /api/v1/fleet/playbook-runs/{runID}
```

Updates an existing run with its final outcome. Called by operators after reviewing the agent's diagnosis or confirming a fleet plan resolved the issue.

| Field | Required | Description |
|---|---|---|
| `outcome` | yes | `resolved` \| `escalated` \| `abandoned` \| `unknown` |
| `escalated_to` | no | Series ID (`pbs_*`) of the next playbook if outcome is `escalated` |
| `findings_summary` | no | Free-form summary of what was found and recommended |

```bash
# Mark a run as resolved
curl -s -X PATCH http://localhost:8080/api/v1/fleet/playbook-runs/plr_3f7a2b1c \
  -H "Content-Type: application/json" \
  -d '{"outcome":"resolved","findings_summary":"Autovacuum was disabled on accounts table. Re-enabled and ran VACUUM ANALYZE."}'

# Mark a run as escalated to a follow-on playbook
curl -s -X PATCH http://localhost:8080/api/v1/fleet/playbook-runs/plr_8c9d2e3f \
  -H "Content-Type: application/json" \
  -d '{"outcome":"escalated","escalated_to":"pbs_db_config_recovery","findings_summary":"Logs show FATAL: invalid value for parameter max_connections."}'
```

Returns `204 No Content` on success.

### `PlaybookRun` object

| Field | Type | Description |
|---|---|---|
| `run_id` | string | Unique run identifier (`plr_` prefix) |
| `playbook_id` | string | The specific playbook version that was run |
| `series_id` | string | Series the playbook belongs to |
| `execution_mode` | string | `fleet` or `agent` |
| `outcome` | string | `resolved` \| `escalated` \| `abandoned` \| `unknown` |
| `escalated_to` | string | Series ID of the follow-on playbook (when `outcome=escalated`) |
| `findings_summary` | string | Operator-provided summary of diagnosis and action taken |
| `context_id` | string | A2A session ID (agent-mode runs only) |
| `operator` | string | Identity from `X-User` request header |
| `started_at` | RFC3339 | When the run was initiated |
| `completed_at` | RFC3339 | When the run was patched with a final outcome |

---

## Adaptive triage

The three Database Down playbooks form an adaptive triage system. Rather than following a fixed script, the agent gathers evidence, tests hypotheses, and navigates the escalation graph based on what it finds.

### Entry points

The `entry_point: true` field marks the **starting playbook** for a problem class. When a database goes completely unreachable, the operator runs the entry-point playbook for `problem_class: availability` — currently **Database Down — Restart Triage** — regardless of the suspected cause. The agent classifies the failure from pod logs and either resolves it (pod restart suffices) or escalates along the appropriate path.

Only one playbook per `problem_class` should have `entry_point: true`.

### Escalation graph

`escalates_to` lists the series IDs the agent should consider next if its current hypothesis is disproven by the evidence. This forms a directed graph of triage paths:

```
pbs_db_restart_triage  (entry_point: true)
        │
        ├─ logs show bad config → pbs_db_config_recovery
        │                               │
        │                               └─ logs show corrupt data → pbs_db_pitr_recovery
        │
        └─ logs show corrupt/missing files → pbs_db_pitr_recovery
```

The agent is prompted with the escalation paths at run time:

> "If your investigation reveals a different root cause than this playbook addresses, the next playbooks to consider are (by series ID): `pbs_db_config_recovery`, `pbs_db_pitr_recovery`"

### Requires-evidence warnings

`requires_evidence` contains log patterns or error substrings that should be present in the operator's context before this playbook is appropriate. They are regex-compatible (e.g. `"FATAL.*invalid value for parameter"`).

When you run a playbook with `requires_evidence` set and the patterns are absent from the `context` you provided (or you provided no context at all), the response includes a `warnings` array:

```json
{
  "text": "...",
  "warnings": [
    "expected evidence pattern not found in provided context: \"FATAL.*invalid value for parameter\"",
    "expected evidence pattern not found in provided context: \"FATAL.*configuration file\""
  ]
}
```

This signals that you may have selected the wrong playbook for the failure mode. To suppress the warning, include the relevant log lines in the `context` field.

Warnings never block execution — they are advisory.

### Structured escalation signal

For agent-mode runs the Gateway parses a structured signal from the agent's response before returning it to the caller. The agent is instructed to append two lines at the end of its response:

```
FINDINGS: <one-sentence diagnosis and recommended action>
ESCALATE_TO: <series_id>     # optional — only when a follow-on playbook is needed
```

The Gateway strips these lines from the visible `text` returned to the operator, then uses them to:

- Set `outcome=resolved` when only `FINDINGS:` is present (root cause identified)
- Set `outcome=escalated` and `escalated_to=<series_id>` when `ESCALATE_TO:` is present
- Populate `findings_summary` with the FINDINGS text
- Add `escalation_hint` to the JSON response when `ESCALATE_TO:` is present

The `escalation_hint` field lets the caller automatically chain to the next playbook:

```bash
# Run restart triage, capture any escalation hint
RESP=$(curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_restart_triage/run \
  -H "Content-Type: application/json" \
  -d '{"connection_string":"...","context":"<pod logs>"}')

echo "$RESP" | jq .text    # diagnosis for operator review
HINT=$(echo "$RESP" | jq -r '.escalation_hint // empty')

if [ -n "$HINT" ]; then
  # Resolve the series ID to a playbook ID and continue the investigation
  NEXT_ID=$(curl -s "http://localhost:8080/api/v1/fleet/playbooks?series_id=$HINT" \
    | jq -r '.playbooks[0].playbook_id')
  FIRST_RUN_ID=$(echo "$RESP" | jq -r .run_id)   # from run tracking
  echo "Escalating to $HINT (playbook $NEXT_ID)"
fi
```

### Continuity threading

When following an escalation path, pass the `prior_run_id` of the previous investigation in the request body. The Gateway fetches the prior run's `findings_summary` from auditd and injects it into the agent prompt:

```
## Prior Investigation Findings
A previous investigation reached the following conclusion:
<prior findings_summary>

Continue from this context and investigate further.
```

This gives the next agent session full context about what was already ruled out, avoiding redundant tool calls.

```bash
# Escalate from restart triage to config recovery, passing prior findings
FIRST_RUN_ID="plr_8c9d2e3f"
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_config_recovery/run \
  -H "Content-Type: application/json" \
  -d "{
    \"connection_string\": \"postgres://prod-db.example.com/app\",
    \"context\": \"<additional log lines>\",
    \"prior_run_id\": \"$FIRST_RUN_ID\"
  }" | jq .text
```

### Full Database Down triage example

```bash
GW=http://localhost:8080
CONN="postgres://prod-db.example.com/app"
LOGS="$(cat /tmp/postgres.log)"  # retrieved via SSH or uploaded via /uploads

# 1. Find the entry-point playbook for availability
PB=$(curl -s "$GW/api/v1/fleet/playbooks" \
  | jq -r '.playbooks[] | select(.series_id == "pbs_db_restart_triage") | .playbook_id')

# 2. Run it — the agent classifies the failure
RESP=$(curl -s -X POST "$GW/api/v1/fleet/playbooks/$PB/run" \
  -H "Content-Type: application/json" \
  -d "{\"connection_string\":\"$CONN\",\"context\":\"$LOGS\"}")

echo "=== Diagnosis ==="
echo "$RESP" | jq -r .text

HINT=$(echo "$RESP" | jq -r '.escalation_hint // empty')
RUN_ID=$(curl -s "$GW/api/v1/fleet/playbooks/$PB/runs" | jq -r '.runs[0].run_id')

# 3. If the agent recommends escalation, continue in the next playbook
if [ -n "$HINT" ]; then
  NEXT_PB=$(curl -s "$GW/api/v1/fleet/playbooks?series_id=$HINT" \
    | jq -r '.playbooks[0].playbook_id')
  echo "=== Escalating to $HINT ==="
  curl -s -X POST "$GW/api/v1/fleet/playbooks/$NEXT_PB/run" \
    -H "Content-Type: application/json" \
    -d "{\"connection_string\":\"$CONN\",\"context\":\"$LOGS\",\"prior_run_id\":\"$RUN_ID\"}" \
    | jq -r .text
fi
```

---

## Importing playbooks

The import endpoint converts existing runbooks into playbook drafts without persisting them. The caller reviews the draft and, if satisfied, calls `POST /api/v1/fleet/playbooks` to save it.

```
POST /api/v1/fleet/playbooks/import
```

### Request

```json
{
  "text":   "<runbook content>",
  "format": "yaml",
  "hints":  {
    "name":          "optional pre-filled name",
    "problem_class": "performance",
    "series_id":     "pbs_existing_series"
  }
}
```

| Field | Required | Description |
|---|---|---|
| `text` | yes | The raw runbook content |
| `format` | no | `yaml` (default: `text`) |
| `hints` | no | Pre-filled values; used when the extracted value is empty |

**Supported formats:**

| Format | LLM required | Description |
|---|---|---|
| `yaml` | No | Direct parse of the canonical aiHelpDesk YAML schema (see below). Fast, deterministic, `confidence=1.0`. |
| `text` | Yes | Plain-text runbook |
| `markdown` | Yes | Markdown runbook |
| `rundeck` | Yes | Rundeck job definition XML/YAML — shell commands are translated into tool references |
| `ansible` | Yes | Ansible playbook — tasks are translated into natural-language tool descriptions |

LLM-backed formats require `HELPDESK_MODEL_VENDOR`, `HELPDESK_MODEL_NAME`, and `HELPDESK_API_KEY` to be configured on the Gateway. The `yaml` format never calls the LLM and works without any API key.

### Response

```json
{
  "draft": {
    "name":          "Vacuum & Bloat Triage",
    "description":   "Investigate table bloat and autovacuum health across all databases.",
    "problem_class": "capacity",
    "symptoms":      ["table bloat > 20%", "autovacuum not running"],
    "guidance":      "Start with get_vacuum_status...",
    "escalation":    ["disk usage > 90%"],
    "target_hints":  [],
    "author":        "alice",
    "version":       "1.0",
    "series_id":     "",
    "source":        "imported"
  },
  "warning_messages": ["author could not be extracted from the source text"],
  "confidence": 0.87
}
```

The `draft` is **not persisted**. To save it:

```bash
# Import
DRAFT=$(curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/import \
  -H "Content-Type: application/json" \
  -d '{"text": "<runbook>", "format": "markdown"}')

# Review
echo "$DRAFT" | jq .draft

# Save (copy the draft fields into a create request)
echo "$DRAFT" | jq '.draft' \
  | curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks \
      -H "Content-Type: application/json" \
      -d @-
```

### YAML import format

For `format=yaml`, the import endpoint expects the canonical aiHelpDesk playbook YAML schema. This is the same format used by the system playbooks in the `playbooks/` directory:

```yaml
series_id: pbs_my_playbook       # leave empty to auto-generate
name: My Playbook
version: "1.0"
problem_class: performance       # performance|availability|capacity|data_integrity|security
author: alice
description: |
  Investigate slow queries on the primary. Check pg_stat_activity and explain plans
  for queries running longer than 5 seconds.
symptoms:
  - "p99 query latency > 5s"
  - "slow query alert firing"
guidance: |
  Begin with get_slow_queries to identify the top offenders by total_time.
  Cross-check with get_wait_events to distinguish CPU-bound vs. I/O-bound queries.
  Use explain_query on candidates with high total_time and low calls (one-shot expensive
  queries are often missing an index). Common misdiagnosis: blaming the query when the
  real issue is table bloat causing sequential scans — check get_vacuum_status.
escalation:
  - "any query running > 30 minutes with writes (has_writes=true)"
  - "blocking chain involves a write transaction open > 10 minutes"
target_hints: []
execution_mode: fleet            # fleet (default) | agent
entry_point: false               # true = preferred starting playbook for this problem_class
escalates_to: []                 # series IDs of follow-on playbooks if hypothesis is wrong
requires_evidence: []            # log patterns expected before selecting this playbook
```

`name` and `description` are required. Missing fields produce `warning_messages` and reduce `confidence` to `0.8`.

When importing via LLM (`format=markdown`, `text`, `rundeck`, `ansible`), the importer infers `execution_mode` and `entry_point` from context and extracts `requires_evidence` from "when to use" language in the source. `escalates_to` is always left empty on import — series IDs of other playbooks cannot be inferred from text and must be filled in by the operator after reviewing the draft.

---

## Authoring guidance

### Writing effective `description` fields

The `description` is passed verbatim to the fleet planner as the job intent. Write it as a directive:

```
# Good: clear intent the planner can act on
"description": "Investigate replication lag on all production replicas. Check WAL sender state, sent/replay LSN gaps, and replica disk usage."

# Weak: too vague to generate a useful plan
"description": "Check replication"
```

Reference tool names when the triage path is well-known:

```
"description": "Check connection health on all databases. Use get_server_info for active vs. max connection counts, get_blocking_queries for any blocking chains."
```

### Writing effective `guidance` fields

Guidance is injected into the planner prompt for every run. Keep it focused on what the planner might miss or get wrong:

- Tool sequencing ("begin with X before Y, because...")
- Thresholds and cut-offs ("lag > 5 minutes is a hard escalation trigger")
- Common misdiagnoses ("a lag spike during a bulk load is normal; only escalate if lag persists 15 minutes after the batch completes")
- Cross-tool correlation ("cross-check get_wait_events against get_slow_queries — a query that appears expensive in slow_queries but shows no wait events is CPU-bound, not I/O-bound")

Guidance does not need to be exhaustive — the planner has full access to the tool catalog and can reason independently. Focus on non-obvious heuristics that come from operational experience.

### Escalation conditions

List conditions where the operator must stop automated investigation and involve a human. The planner treats escalation conditions as guardrails:

```yaml
escalation:
  - "replay_lag > 5 minutes on any replica"
  - "replication slot with no active connection and LSN delta > 10 GB"
  - "WAL sender state is not 'streaming' and not 'catchup'"
```

Use specific, measurable thresholds rather than vague descriptions.

---

## Lifecycle example

A typical workflow for adding a new version of an existing playbook:

```bash
# 1. Check the current active version
curl -s "http://localhost:8080/api/v1/fleet/playbooks?series_id=pbs_vacuum_triage" \
  | jq '.playbooks[0] | {playbook_id, version, is_active}'

# 2. Create a new version in the same series (inactive by default)
NEW=$(curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks \
  -H "Content-Type: application/json" \
  -d '{
    "series_id":     "pbs_vacuum_triage",
    "name":          "Vacuum & Bloat Triage",
    "version":       "1.1",
    "description":   "...",
    "guidance":      "Updated guidance: also check get_disk_usage when bloat ratio > 30%.",
    "problem_class": "capacity"
  }')
NEW_ID=$(echo "$NEW" | jq -r .playbook_id)

# 3. Test it — run the new (inactive) version directly by ID
curl -s -X POST "http://localhost:8080/api/v1/fleet/playbooks/$NEW_ID/run" \
  | jq .plan_description

# 4. Promote when satisfied
curl -s -X POST "http://localhost:8080/api/v1/fleet/playbooks/$NEW_ID/activate" \
  | jq '{playbook_id, version, is_active}'

# 5. Verify the old version is now inactive
curl -s "http://localhost:8080/api/v1/fleet/playbooks?series_id=pbs_vacuum_triage&active_only=false" \
  | jq '.playbooks[] | {version, is_active}'
```

---

## Playbook schema reference

Full field reference for the `Playbook` object returned by all endpoints:

| Field | Type | Description |
|---|---|---|
| `playbook_id` | string | Unique identifier (`pb_` prefix) |
| `series_id` | string | Stable series identifier (`pbs_` prefix); shared across versions |
| `name` | string | Human-readable name |
| `description` | string | Planner intent — passed verbatim at run time |
| `problem_class` | string | `performance` \| `availability` \| `capacity` \| `data_integrity` \| `security` |
| `symptoms` | []string | Observable indicators that should trigger this playbook |
| `guidance` | string | Expert reasoning injected into the planner prompt |
| `escalation` | []string | Conditions requiring human escalation |
| `target_hints` | []string | Tag names or server name patterns for target resolution |
| `related_playbooks` | []string | `pb_*` IDs of related playbooks |
| `author` | string | Author identity or team name |
| `version` | string | Free-form version string |
| `is_active` | bool | `true` if this is the active version in its series |
| `is_system` | bool | `true` for playbooks shipped with aiHelpDesk (read-only) |
| `source` | string | `system` \| `imported` \| `manual` |
| `entry_point` | bool | `true` marks this as the preferred starting playbook for its `problem_class`. Used by the planner to resolve "where do I start?" when multiple playbooks could apply. Only one playbook per problem class should have `entry_point=true`. |
| `escalates_to` | []string | Series IDs (`pbs_*`) of playbooks to consider next if this playbook's hypothesis is disproven by the collected evidence. Injected into the agent prompt as escalation context. |
| `requires_evidence` | []string | Log patterns or error signals expected to be present before this playbook is selected. Expressed as case-insensitive substrings or regex fragments (e.g. `"FATAL.*invalid value for parameter"`). At run time the Gateway checks these patterns against the `context` field of the run request and emits `warnings` for any that are missing. Execution is never blocked — warnings are advisory only. |
| `execution_mode` | string | `fleet` (default) — routes through the fleet planner and returns a `FleetPlanResponse`. `agent` — routes directly to the database agent as an interactive agentic session; the agent collects evidence, forms hypotheses, and returns a diagnosis with recommended (not executed) remediation steps. The Gateway automatically parses the agent's structured response to set `outcome` and `findings_summary` on the run record. |
| `stats` | object | Inline run statistics for the playbook's series. Populated by `GET /fleet/playbooks` (list); omitted when no runs have been recorded. See `PlaybookRunStats` below. Not persisted — computed on read. |
| `created_at` | RFC3339 | Creation timestamp |
| `updated_at` | RFC3339 | Last update timestamp |

### `PlaybookRunStats` object

Returned inline in `GET /fleet/playbooks` and by `GET /fleet/playbooks/{playbookID}/stats`. Stats are **series-wide** — they aggregate all versions of a playbook.

| Field | Type | Description |
|---|---|---|
| `series_id` | string | Series the stats belong to |
| `total_runs` | int | Total number of recorded runs |
| `resolved` | int | Runs with `outcome=resolved` |
| `escalated` | int | Runs with `outcome=escalated` |
| `abandoned` | int | Runs with `outcome=abandoned` |
| `resolution_rate` | float | `resolved / total_runs` (0–1) |
| `escalation_rate` | float | `escalated / total_runs` (0–1) |
| `last_run_at` | string | Timestamp of the most recent run |

**How outcomes are set:**

| Outcome | How it gets recorded |
|---|---|
| `resolved` | Agent-mode: Gateway parses a `FINDINGS:` line (or conclusion fallback) from the agent's response and no `ESCALATE_TO:` signal is present. Fleet-mode: operator PATCHes the run after confirming the plan resolved the issue. |
| `escalated` | Agent-mode: Gateway parses an `ESCALATE_TO: <series_id>` signal from the agent's response; `escalated_to` is set to that series ID. Fleet or agent: operator PATCHes with `outcome=escalated`. |
| `abandoned` | Operator explicitly PATCHes the run with `outcome=abandoned` — used when an investigation was started but not completed (e.g. alert cleared before diagnosis, wrong playbook selected). |
| `unknown` | Default at run start. Remains `unknown` if the agent's response contained no parseable signal and the operator has not yet patched the run. Runs that stay `unknown` are **not counted** in `resolution_rate` or `escalation_rate` — only the denominator `total_runs` includes them. |

`resolution_rate` and `escalation_rate` use `total_runs` (not `resolved + escalated`) as the denominator, so `unknown` and `abandoned` runs dilute the rates. A low `resolution_rate` on an agent-mode playbook often means the agent is not producing parseable `FINDINGS:` signals — check the `findings_summary` field on recent runs via `GET /playbook-runs/{runID}`.
