# aiHelpDesk Playbook

A **Playbook** is not a traditional runbook. A runbook is static. A fixed sequence of steps written once and executed literally, assuming a known environment. A Playbook in aiHelpDesk encodes strategic **intent** and expert **knowledge**: what class of problem this is, what symptoms indicate it, what the planner should prioritise, and when to escalate. The fleet planner reads that intent and expertise, examines the current tool catalog and live infrastructure state, and generates the actual execution steps fresh each time. The same Playbook handles a connection exhaustion fault differently on a bare-metal host than on a Kubernetes cluster, because the available tools differ. This is what "never a stale script" means in practice.

Playbooks are the system's universal remediation artifact. When the orchestrator diagnoses a fault — real or injected — it selects a Playbook and hands it to the fleet planner for execution. When `faulttest` validates an agent's remediation capability, it does so against a Playbook. When the Vault synthesises institutional knowledge from a resolved incident, the output is a Playbook. They are the connective tissue between diagnosis and action across every execution path in aiHelpDesk.

System Playbooks ship with aiHelpDesk and cover the most common database triage scenarios out of the box. You can author custom Playbooks from scratch, import and convert your existing static runbooks (Markdown, plain text, YAML, Rundeck, Ansible), or let aiHelpDesk synthesise them automatically from resolved incident traces via the [Vault](VAULT.md).

See Playbook [operational best practices](PLAYBOOK_OPS.md) on how aiHelpDesk recommends making use of the Playbook feature.

---

## Concepts

### Intent vs. knowledge

Every Playbook carries two classes of fields:

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
| `symptoms` | []string | Observable indicators that should trigger this Playbook |
| `guidance` | string | Expert reasoning injected into the planner prompt at run time |
| `escalation` | []string | Conditions under which the agent must stop and escalate to a human |
| `related_playbooks` | []string | `pb_*` IDs of related Playbooks |
| `author` | string | Author identity or team name |
| `version` | string | Free-form version string (e.g. `"1.2"`) |
| `agent_name` | string | Overrides the default agent for this Playbook (e.g. `"sysadmin"` for host-level operations). Affects which agent is dispatched when this Playbook is run or chained to. |
| `approval_mode` | string | Default `approval_mode` for standalone runs (`""` / `"manual"` / `"session"` / `"auto"`). Also acts as a **chaining gate**: playbooks with `approval_mode: ""` or `"manual"` can never be auto-chained — callers always receive `suggested_next` for them regardless of requester mode. Only `"session"` and `"auto"` are eligible for auto-chaining. |

The `guidance` field is the most important knowledge field. It is injected into the planner prompt as a `## Playbook Guidance` section whenever the Playbook is run. Use it for expert heuristics, prioritisation notes, tool sequencing hints, and common misdiagnosis warnings. It does not appear in ad-hoc `/fleet/plan` calls.

### Versioning

Each Playbook belongs to a **series** identified by `series_id` (a stable `pbs_` prefixed identifier). A series can have multiple versions but exactly one is **active** at any time. The active version is the one selected when the Playbook is run.

| Field | Meaning |
|---|---|
| `series_id` | Stable identifier shared across all versions of the same Playbook (auto-generated as `pbs_<uuid[:8]>` if omitted on create) |
| `is_active` | `true` for the version that runs when the Playbook is invoked |
| `is_system` | `true` for Playbooks shipped with aiHelpDesk (read-only via API) |
| `source` | `system` (shipped), `imported` (import endpoint), or `manual` (API-created) |

When you create a Playbook without specifying a `series_id`, a new series is started and the Playbook is immediately active. When you supply an existing `series_id`, the new version is **inactive by default** — you promote it explicitly via the activate endpoint. This lets you author and review a new version before it takes effect.

### System Playbooks

aiHelpDesk ships 14 expert-authored system Playbooks that are seeded into auditd on startup:

| Series ID | Name | Problem class | Agent | Key tools |
|---|---|---|---|---|
| `pbs_vacuum_triage` | Vacuum & Bloat Triage | capacity | database | `get_vacuum_status`, `get_disk_usage`, `get_pg_settings` |
| `pbs_slow_query_triage` | Slow Query Triage | performance | database | `get_slow_queries`, `get_wait_events`, `get_blocking_queries`, `explain_query` |
| `pbs_connection_triage` | Connection & Lock Triage | availability | database | `get_server_info`, `get_blocking_queries`, `get_session_info`, `get_lock_info` |
| `pbs_lock_chain_triage` | Transaction Lock Chain Triage | availability | database | `get_blocking_queries`, `get_session_info`, `terminate_connection` |
| `pbs_lock_chain_remediate` | Transaction Lock Chain — Terminate Root Blocker | availability | database | `get_blocking_queries`, `get_session_info`, `terminate_connection` |
| `pbs_replication_lag` | Replication Lag Triage | availability | database | `get_replication_status`, `get_server_info` |
| `pbs_checkpoint_bgwriter_triage` | Checkpoint & bgwriter Triage | performance | database | `get_bgwriter_stats`, `read_pg_log`, `get_pg_settings` |
| `pbs_db_restart_triage` | Database Down — Restart Triage | availability | database | `check_connection`, `get_pod_status`, `get_pod_logs`, `get_events`, `read_pg_log`, `read_uploaded_file`, `restart_deployment` |
| `pbs_db_config_recovery` | Database Down — Configuration Recovery | availability | database | `get_pod_logs`, `get_events`, `get_pg_settings`, `read_pg_log`, `read_uploaded_file`, `restart_deployment` |
| `pbs_db_pitr_recovery` | Database Down — Backup Restore & PITR | availability | database | `check_connection`, `get_pod_logs`, `get_events`, `read_pg_log`, `read_uploaded_file` |
| `pbs_sysadmin_docker_inspect` | Sysadmin — Docker Container Inspection | availability | sysadmin | `check_host`, `get_host_logs`, `check_memory`, `read_pg_log_file` |
| `pbs_db_restart_action` | Sysadmin — Docker Container Restart | availability | sysadmin | `restart_container`, `check_host`, `check_connection` |
| `pbs_wal_disk_full` | WAL Disk Full — Recovery | capacity | sysadmin | `check_host`, `get_host_logs`, `check_disk`, `get_pg_settings` |
| `pbs_wal_stale_slot` | WAL Accumulation — Stale Replication Slot | capacity | database | `get_pg_settings`, `get_replication_status`, `get_active_connections` |

The **Checkpoint & bgwriter Triage** Playbook (`pbs_checkpoint_bgwriter_triage`) covers performance degradation caused by misconfigured checkpoint or background-writer parameters. It is not a database-down scenario — the database stays available — but write latency degrades due to periodic I/O bursts. The canonical symptom is a PostgreSQL `LOG: checkpoints are occurring too frequently` warning accompanied by `maxwritten_clean > 0` in `pg_stat_bgwriter` (bgwriter hitting its per-round page limit) and elevated `buffers_backend` (regular backends forced to flush dirty pages themselves). The five-step guidance leads the agent from log confirmation through counter interpretation, parameter identification (`max_wal_size`, `bgwriter_lru_maxpages`, `checkpoint_completion_target`), safe `ALTER SYSTEM SET + pg_reload_conf()` recommendations presented to the operator for approval, and a post-change verification using `get_bgwriter_stats`. Escalation triggers include `buffers_backend_fsync > 0` (backends doing their own fsyncs — a severe condition requiring immediate I/O capacity review) and `checkpoint_sync_time > 30s`. This Playbook also demonstrates the Crystal Ball gap clearly: the PostgreSQL `HINT: Consider increasing max_wal_size` in the log is a shortcut that `bgwriter_lru_maxpages=2` makes misleading — an unguided agent typically follows the hint and misses the root cause, while the Playbook's systematic counter analysis surfaces it.

The **Transaction Lock Chain Triage** Playbook (`pbs_lock_chain_triage`) handles lock queues whose root holder is keeping an open transaction — either `idle in transaction` (paused after DML) or `active` while executing a long-running or sleeping statement such as `pg_sleep`. The canonical symptom is multiple sessions in `Lock` wait state against the same table, with the root blocker appearing dormant or slow in `pg_stat_activity`. The critical diagnostic rule the Playbook enforces: `cancel_query` (`pg_cancel_backend`) is **unreliable** for root blockers. If the root is idle-in-transaction, SIGINT is ignored entirely. If the root is active with `pg_sleep`, SIGINT interrupts the sleep and the function returns `true` — a false positive that looks like success but leaves the transaction and its locks intact (the session moves to `idle in transaction aborted`). The Playbook explicitly directs the agent to `terminate_connection` (`pg_terminate_backend`) on the root blocker, which sends SIGTERM and unconditionally closes the connection regardless of state. This Playbook is the reference case for the Crystal Ball gap in remediation: an unguided agent presents `cancel_query` as "Option 1 (Immediate)" and declares the problem resolved — while the Playbook-guided agent proceeds directly to terminate and the lock queue clears in under a second. When an operator is ready to act on the triage findings, the companion **`pbs_lock_chain_remediate`** Playbook executes the termination under step-by-step approval.

The **Transaction Lock Chain — Terminate Root Blocker** Playbook (`pbs_lock_chain_remediate`) is the remediation counterpart to `pbs_lock_chain_triage`. Where the triage Playbook runs autonomously (agent mode) to diagnose, this Playbook uses `execution_mode: agent_approve` to execute termination under explicit per-step operator approval. The LLM proposes one action at a time across a four-step sequence — map the full chain (`get_blocking_queries`), inspect the root AND each intermediate for `has_writes` (`get_session_info`), terminate the root only (`terminate_connection`), and verify the cascade cleared (`get_blocking_queries` again) — and the operator approves each step before it executes. A critical safety property: in a multi-level chain, terminating the root releases its lock and causes each intermediate session to complete and disconnect without `COMMIT`, rolling back its own open transaction as a side effect. If an intermediate has `has_writes=true`, that uncommitted work is silently discarded. The Playbook surfaces this cascade risk explicitly in the `reason` field of the termination step proposal, giving the operator full visibility before they approve. An unguided agent terminates without disclosing this. The Playbook has `approval_mode: manual` and is never auto-chained: the operator always invokes it explicitly after reviewing the triage findings. See [agent_approve execution mode](#agent_approve-execution-mode) below for the full lifecycle.

The "Database Down" Playbooks form an escalating triage graph for Docker-hosted databases. Always begin with **Restart Triage** to classify the failure. For Kubernetes-hosted databases, if pod logs reveal a configuration error, proceed to **Configuration Recovery**; if they reveal data corruption, proceed to **Backup Restore & PITR**. For Docker-hosted databases where the DB agent cannot read container logs, the triage playbook escalates to **Docker Container Inspection** — the SysAdmin agent reads `docker inspect` output and container logs to determine whether the container stopped cleanly, crashed, was OOM-killed, or hit a WAL disk full condition, then revises the root-cause hypothesis accordingly.

- **Clean shutdown or OOM kill**: Inspection escalates to **Docker Container Restart** (`pbs_db_restart_action`), which performs the actual `restart_container` call and verifies connectivity.
- **WAL disk full** (`PANIC: could not write to file "pg_wal/...": No space left on device` in logs): Inspection escalates to **WAL Disk Full — Recovery** (`pbs_wal_disk_full`), which diagnoses the root cause of WAL accumulation (archiving backlog, stale replication slot, or genuine growth) and guides safe cleanup before any restart attempt. Restarting without first freeing disk space will re-PANIC immediately.

The **WAL Accumulation — Stale Replication Slot** Playbook (`pbs_wal_stale_slot`) handles the complementary scenario where the database is still up but `pg_wal` is growing without bound. It differs from `pbs_wal_disk_full` in that the database is reachable — the agent works through a four-hypothesis elimination tree (archive failure → inactive slot → long transaction → write volume) rather than reading crash logs. Dropping the slot requires `approval_mode: manual` since it permanently removes the replica's reconnection point.

`pbs_db_restart_action`, `pbs_wal_disk_full`, `pbs_wal_stale_slot`, and `pbs_lock_chain_remediate` all have `approval_mode: manual` and can never be auto-chained — the operator always receives `suggested_next` and must invoke them explicitly.

Because psql-based tools cannot reach a down database, all Playbooks targeting a completely unreachable database rely on K8s tools (`get_pod_logs`, `get_events`) or host tools (`check_host`, `get_host_logs`) for live diagnostics, and on `get_saved_snapshots` to retrieve values captured in prior fleet-runner baselines — such as `data_directory`, `config_file`, `hba_file`, and `log_directory` — without a live connection. The agent calls `get_saved_snapshots(tool_name="get_baseline", server_name=<target>)` to find these paths from the most recent recorded snapshot.

For databases running on bare-metal hosts (no Kubernetes and no Docker), `get_pod_logs` is unavailable. In that case the agent will attempt `read_pg_log`, which reads the PostgreSQL log directly via `pg_read_file()` — but this too requires a live DB connection. When the database is completely down and unreachable, an operator must retrieve the log file manually (e.g. via SSH or a jump host) and upload it with `POST /api/v1/fleet/uploads`. The agent then reads it using `read_uploaded_file` with the returned `upload_id`. See [Operator file uploads](API.md#operator-file-uploads) in the API reference.

System Playbooks are **read-only**: `PUT` and `DELETE` return `400 Bad Request`. To customise one, run it as-is, or import and save your own version in the same series (the activate endpoint then lets you promote your version).

Seeding is idempotent — restarting auditd never duplicates system Playbooks. If a newer version of a system Playbook ships with an aiHelpDesk upgrade, it is inserted as an **inactive** version so customers can review and promote it when ready.

---

## Agent endpoint security

Agent `POST /tool/{name}` endpoints are authenticated by default. The gateway
sends a bearer token to every agent it calls; each agent validates it against
a local users file before executing the tool.

### How it works

| Component | Env var | Default |
|-----------|---------|---------|
| Agent identity provider | `HELPDESK_IDENTITY_PROVIDER` | `static` |
| Agent users file (inside container) | `HELPDESK_USERS_FILE` | `/etc/helpdesk/users.yaml` |
| Users file on the host | `HELPDESK_USERS_FILE_HOST` | `./users.example.yaml` |
| Gateway → agent bearer token | `HELPDESK_AGENT_API_KEY` | `gateway-api-key` |

The default key `gateway-api-key` is pre-hashed in `users.example.yaml` (the
`gateway` service account). This is intentionally a known example value so the
stack works out of the box — **REMBER TO REPLACE IT IN PRODUCTION**.

### Production setup

1. **Generate a strong key and hash it:**
   ```bash
   openssl rand -hex 32 | go run ./cmd/hashapikey
   ```
   Copy the printed Argon2id hash.

2. **Create your users file** by copying `deploy/docker-compose/users.example.yaml`
   and replacing the `gateway` service account hash with the one you just generated.
   Remove or update any other placeholder hashes.

3. **Set env vars** (e.g. in `.env` or your secrets manager):
   ```bash
   HELPDESK_AGENT_API_KEY=<your-secret-key>
   HELPDESK_USERS_FILE_HOST=/path/to/your/users.yaml
   ```

The gateway authenticates to all agents using the same `HELPDESK_AGENT_API_KEY`.
The auditd service and the gateway's own HTTP listener do not use
`HELPDESK_IDENTITY_PROVIDER` — they are excluded by design.

---

## Informed gate

The informed gate is a phase-boundary checkpoint between a triage playbook and its remediation counterpart. Where the existing horizontal escalation mechanism chains one agent to another mid-diagnosis, the informed gate pauses at the **vertical handoff** — triage is fully complete, and the operator reviews the findings before the remediation playbook is invoked. Authorization happens after evidence is seen, not before.

### When to use it

Pass `"gate_escalation": true` in the `/run` request body for any agent-mode triage playbook. The gate fires after the triage agent completes and emits its `TRANSITION_TO:` or `ESCALATE_TO:` signal. If neither signal is present (the agent resolved the issue directly), `gate_escalation` is a no-op and the normal response is returned. If the `FINDINGS:` line contains `recommended=monitor` or `recommended=no_changes_needed`, the gate is also skipped — nothing needs operator action. Without this flag, the existing behaviour applies (auto-chain if `approval_mode` permits, or return `suggested_next`).

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pbs_vacuum_triage/run \
  -H "Content-Type: application/json" \
  -d '{
    "connection_string": "prod-primary",
    "gate_escalation":   true
  }'
```

### `pending_gate` response

When the gate fires, the run returns HTTP 200 with `"status": "pending_gate"`. The response shape varies by gate type.

**Transition gate** (`TRANSITION_TO:` — triage handing off to its remediation counterpart):

```json
{
  "run_id":               "plr_a3f7c1b2",
  "status":               "pending_gate",
  "gate_type":            "transition",
  "findings":             "Table public.orders has 94% dead tuple ratio...",
  "transition_target":    "pbs_vacuum_remediate",
  "escalation_findings":  "Table public.orders has 94% dead tuple ratio...",
  "remediation_preview":  { "series_id": "pbs_vacuum_remediate", "name": "Vacuum Remediation", "description": "Run VACUUM ANALYZE and verify dead tuple ratio drops below 20%.", "approval_mode": "review" },
  "diagnostic_report":    { "hypotheses": [...], "root_cause": "..." },
  "confidence_warning":   "",
  "suggested_approval_mode": ""
}
```

**Escalation gate** (`ESCALATE_TO:` — true cross-domain handoff):

```json
{
  "run_id":               "plr_b8e2d4f1",
  "status":               "pending_gate",
  "gate_type":            "escalation",
  "findings":             "Connection refused — Docker-level investigation needed.",
  "escalation_target":    "pbs_sysadmin_docker_inspect",
  "escalation_findings":  "Connection refused — Docker-level investigation needed.",
  "remediation_preview":  { "series_id": "pbs_sysadmin_docker_inspect", "name": "Docker Container Inspect", "description": "Inspect the database container for OOM kills, crash loops, or misconfig.", "approval_mode": "manual" },
  "diagnostic_report":    { "hypotheses": [...], "root_cause": "..." },
  "confidence_warning":   "Primary hypothesis confidence 55% — competing hypothesis at 42%.",
  "suggested_approval_mode": "manual",
  "gate_reason":          "low_confidence"
}
```

`gate_type` tells operators what kind of handoff this is: `"transition"` is a routine expected pipeline step; `"escalation"` is an out-of-scope cross-domain handoff that may warrant closer scrutiny. `remediation_preview` describes the next playbook that would run after approval — its name, intent description, and default `approval_mode` — so operators know exactly what they are authorising before clicking approve. `diagnostic_report` contains the structured hypothesis breakdown from triage (populated when the agent emits `HYPOTHESIS_N:` lines; `null` otherwise — see [Structured diagnostic report](#structured-diagnostic-report)). The `confidence_warning` field is populated when the primary hypothesis confidence is below 70%, or when a competing hypothesis scores more than 70% of the primary. It is **advisory and non-blocking** — the operator can still proceed — but when present, `suggested_approval_mode` is always `"manual"`. When confidence drops below 50%, the gateway **forces** a gate regardless of whether `gate_escalation=true` was set in the request, and sets `gate_reason: "low_confidence"` in the response. A coin-flip diagnosis must not auto-chain into destructive remediation.

The triage run is recorded with `outcome: gate_pending`. The run ID is stable — you can use `GET /api/v1/fleet/playbook-runs/{run_id}` to retrieve findings later.

### Proceeding through the gate

Call `POST /api/v1/fleet/playbook-runs/{run_id}/proceed-escalation` with your decision:

```bash
# Approve — choose how closely to watch the remediation
curl -s -X POST http://localhost:8080/api/v1/fleet/playbook-runs/plr_a3f7c1b2/proceed-escalation \
  -H "Content-Type: application/json" \
  -d '{
    "resolution":       "approved",
    "resolved_by":      "alice",
    "approval_mode":    "review",
    "connection_string": "prod-primary"
  }'

# Deny — no remediation runs; triage run is marked abandoned
curl -s -X POST http://localhost:8080/api/v1/fleet/playbook-runs/plr_a3f7c1b2/proceed-escalation \
  -H "Content-Type: application/json" \
  -d '{"resolution": "denied", "resolved_by": "alice"}'
# → {"status": "denied", "run_id": "plr_a3f7c1b2"}
```

`resolved_by` is optional; it defaults to the `X-User` request header if omitted. It is recorded in the `gate_acknowledged` audit event.

The `approval_mode` you choose at the gate applies to the remediation playbook only:

| Choice | Effect on remediation |
|---|---|
| `manual` | Prompt for every step (read-only and write/destructive) |
| `review` | Auto-approve read steps; prompt for write/destructive |
| `auto` | Auto-approve all steps |
| `session` | Use a pre-created approval session token |

When `resolution: "approved"`, the response is whatever the remediation playbook returns — `200` with findings for `execution_mode: agent`, or `202 pending_approval` for `execution_mode: agent_approve`. Standard approval loops apply from there.

The gateway emits a `gate_acknowledged` audit event recording the operator's identity, resolution, chosen approval mode, and any confidence warning. The triage run outcome is updated to `"transitioned"` for transition gates or `"escalated"` for escalation gates.

### Relationship to the two-playbook split

The gate is complementary to — not a replacement for — the triage + remediation playbook pair. Triage and remediation playbooks remain separate, composable artifacts. The gate is a request-level option that adds a human checkpoint at the handoff. `prior_run_id` threading is automatic: the remediation playbook always starts with the full triage findings in context.

### faulttest flags

Two faulttest CLI flags exercise the gate path:

| Flag | Effect |
|---|---|
| `--gate-escalation` | Adds `"gate_escalation": true` to every PlaybookRun request so the gateway intercepts `TRANSITION_TO` and `ESCALATE_TO` signals at the phase boundary. |
| `--emit-and-wait` | Replaces TTY prompts with HTTP polling: gate polls until externally resolved; step approvals use the audit service long-poll. Safe in Kubernetes Jobs and Docker containers. |

```bash
go run ./testing/cmd/faulttest run \
  --ids db-tx-lock-chain-blocker --external --conn faulttest-db \
  --via-gateway --gateway http://localhost:8080 \
  --remediate --gate-escalation --emit-and-wait \
  --approval-mode manual --audit-url http://localhost:7070
# → logs "Gate pending — resolve_url=..." and polls until resolved externally
```

### Gate notifications

When a gate fires, the gateway can push a notification to a webhook or email. Configure via environment variables:

| Variable | Description |
|---|---|
| `HELPDESK_DECISION_WEBHOOK` | Webhook URL; Slack incoming webhooks are auto-detected and formatted |
| `HELPDESK_DECISION_WEBHOOK_SECRET` | HMAC-SHA256 key for `X-Helpdesk-Signature` request signing |
| `HELPDESK_BASE_URL` | Gateway public URL; used to build absolute resolve links in notifications |

See [docs/DECISIONS.md](DECISIONS.md) for the full webhook payload shape, Slack detection, HMAC signing, and email configuration.

### Git opt-in

Operators can resolve a gate by merging a specially-named branch:

```
approved/gate/{runID}   → approved
```

Register `POST /api/v1/webhooks/git` as a webhook in your git provider and set `HELPDESK_GIT_WEBHOOK_SECRET`. Works with GitHub, GitLab, Gitea, and any provider that sends merge events. See [docs/DECISIONS.md — Git webhook adapter](DECISIONS.md#git-webhook-adapter-opt-in) for full setup.

---

## API

All Playbook endpoints are accessible via the Gateway on port 8080. The Gateway proxies CRUD and activation calls to auditd; the import endpoint is handled entirely within the Gateway (no auditd round-trip for the LLM extraction path).

### List Playbooks

```
GET /api/v1/fleet/playbooks
```

Returns the active version of every Playbook (system and user), ordered by creation time.

```bash
curl http://localhost:8080/api/v1/fleet/playbooks | jq .playbooks
```

**Query parameters:**

| Parameter | Default | Description |
|---|---|---|
| `active_only` | `true` | Set to `false` to include all versions in a series, not just the active one |
| `include_system` | `true` | Set to `false` to hide system Playbooks |
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

The `stats` field is included inline on every Playbook that has at least one recorded run. It is `null` / omitted for Playbooks that have never been run. Stats are series-wide (all versions of a Playbook combined). To get stats for a specific Playbook separately, use `GET /api/v1/fleet/playbooks/{playbookID}/stats`.

### Get a Playbook

```
GET /api/v1/fleet/playbooks/{playbookID}
```

Returns a single Playbook by its `playbook_id`. Returns `404` if not found.

### Create a Playbook

```
POST /api/v1/fleet/playbooks
```

Creates a new Playbook. `name` and `description` are required.

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

Response: `201 Created` with the full Playbook object. A `series_id` and `playbook_id` are generated automatically. `is_active` is set to `true`.

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

### Update a Playbook

```
PUT /api/v1/fleet/playbooks/{playbookID}
```

Replaces all fields of an existing Playbook. Omitting an optional field clears it. Returns `404` if not found. Returns `400` for system Playbooks (`is_system=true`).

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

Atomically promotes a Playbook version to active within its series, deactivating all other versions. Idempotent: activating an already-active Playbook is a no-op. Returns `404` if not found, `400` for system Playbooks.

```bash
# Promote v2 — v1 automatically becomes inactive
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_v2id/activate | jq .is_active
```

Response: `200 OK` with the now-active Playbook object.

### Delete a Playbook

```
DELETE /api/v1/fleet/playbooks/{playbookID}
```

Deletes a Playbook version. Returns `204 No Content` on success, `404` if not found, `400` for system Playbooks.

### Run a Playbook

```
POST /api/v1/fleet/playbooks/{playbookID}/run
```

Executes the Playbook. Behaviour depends on `execution_mode`:

**`execution_mode: fleet` (default)** — generates a fresh fleet plan from the Playbook's `description` and `guidance` and returns a `FleetPlanResponse` (same shape as `POST /api/v1/fleet/plan`). Requires LLM configuration.

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_a1b2c3d4/run \
  | jq -r '.job_def_raw' > /tmp/plan.json

# Review the plan, then execute:
./fleet-runner --job-file /tmp/plan.json --dry-run
./fleet-runner --job-file /tmp/plan.json
```

**`execution_mode: agent`** — routes to the database agent as an agentic triage session. The agent gathers evidence, forms ranked hypotheses with confidence scores, backs out when evidence contradicts a hypothesis, and returns a structured diagnosis with recommended (not executed) remediation steps. Returns the same response shape as `POST /api/v1/query`.

**`execution_mode: agent_approve`** — the gateway drives a step-by-step execution loop with per-step operator approval. The LLM proposes a single action at a time, the gateway surfaces it to the operator, and after the operator approves, the gateway executes the tool directly and re-plans based on the result. No action is executed without explicit approval. Returns `202 Accepted` with a `pending_approval` status rather than a final result. See [agent_approve execution mode](#agent_approve-execution-mode) below.

Optional request body:

| Field | Description |
|---|---|
| `connection_string` | PostgreSQL DSN for the target database |
| `context` | Free-form operator context (server name, symptoms, recent changes, relevant log lines) |
| `context_id` | A2A session ID for multi-turn continuity within an existing session |
| `prior_run_id` | `plr_*` run ID of a previous investigation to continue from (see [Continuity threading](#continuity-threading)) |
| `approval_mode` | `auto`, `session`, `manual`, or `force` — controls tool-call gating and chaining eligibility (see [Approval modes](#approval-modes)) |
| `approval_session` | Required when `approval_mode=session`. The `aps_*` session ID from `POST /v1/approval/sessions` on auditd |

```bash
# Triage a down database (agent mode)
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_restart_triage/run \
  -H "Content-Type: application/json" \
  -d '{"connection_string":"postgres://prod-db.example.com/app","context":"pod in CrashLoopBackOff since 10:00 UTC"}' \
  | jq .text
```

**Run recording** — every `/run` call records a `PlaybookRun` entry in auditd before the LLM or agent is invoked. The run starts with `outcome=unknown`. For agent-mode runs the Gateway parses the agent's structured response and updates the outcome automatically — see [Structured escalation signal](#structured-escalation-signal). Operators can always override the outcome via `PATCH /playbook-runs/{runID}`. See [Run tracking](#run-tracking) below.

**Requires-evidence warnings** — if the Playbook has `requires_evidence` patterns and the provided `context` does not contain matching log lines or error text, the response includes a `warnings` array:

```json
{
  "text": "...",
  "warnings": [
    "expected evidence pattern not found in provided context: \"FATAL.*invalid value for parameter\""
  ]
}
```

Warnings are soft — execution is not blocked. They signal that you may be running the wrong Playbook for the observed failure mode. Providing relevant log lines in `context` removes the warning if the pattern matches.

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
       agent investigates, emits FINDINGS / TRANSITION_TO or ESCALATE_TO signal
                    ↓
       gateway parses signal → outcome auto-updated (resolved|transitioned|escalated|unknown)
                    ↓
       operator reviews diagnosis — may patch outcome to correct it
```

**Agent-approve mode:**
```
POST /run → 202 Accepted
  {status: "pending_approval", run_id, step: {tool, args, reason}, approval_id}
                    ↓
       operator reviews proposed step (tool + args + reason)
                    ↓
POST /proceed {resolution: "approved", step_index: N, resolved_by: "alice"}
  → gateway executes tool via direct dispatch
  → LLM re-plans based on result
  → returns next step OR {status: "complete", summary}
                    ↓
       repeat until status="complete" or operator denies a step
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

### List runs for a Playbook

```
GET /api/v1/fleet/playbooks/{playbookID}/runs
```

Returns the most recent runs for a specific Playbook ID (not series-wide), most recent first. Default limit is 20, maximum 100.

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

Returns aggregated outcome statistics for the **series** the Playbook belongs to (i.e. all versions of the Playbook combined). Returns `404` if the Playbook ID is not found.

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

Use `resolution_rate` to identify Playbooks that frequently escalate — a low rate often signals that the Playbook's guidance or escalation conditions need tuning.

### Record an outcome

```
PATCH /api/v1/fleet/playbook-runs/{runID}
```

Updates an existing run with its final outcome. Called by operators after reviewing the agent's diagnosis or confirming a fleet plan resolved the issue.

| Field | Required | Description |
|---|---|---|
| `outcome` | yes | `resolved` \| `escalated` \| `abandoned` \| `unknown` |
| `escalated_to` | no | Series ID (`pbs_*`) of the next Playbook if outcome is `escalated` |
| `findings_summary` | no | Free-form summary of what was found and recommended |

```bash
# Mark a run as resolved
curl -s -X PATCH http://localhost:8080/api/v1/fleet/playbook-runs/plr_3f7a2b1c \
  -H "Content-Type: application/json" \
  -d '{"outcome":"resolved","findings_summary":"Autovacuum was disabled on accounts table. Re-enabled and ran VACUUM ANALYZE."}'

# Mark a run as escalated to a follow-on Playbook
curl -s -X PATCH http://localhost:8080/api/v1/fleet/playbook-runs/plr_8c9d2e3f \
  -H "Content-Type: application/json" \
  -d '{"outcome":"escalated","escalated_to":"pbs_db_config_recovery","findings_summary":"Logs show FATAL: invalid value for parameter max_connections."}'
```

Returns `204 No Content` on success.

### `PlaybookRun` object

| Field | Type | Description |
|---|---|---|
| `run_id` | string | Unique run identifier (`plr_` prefix) |
| `playbook_id` | string | The specific Playbook version that was run |
| `series_id` | string | Series the Playbook belongs to |
| `execution_mode` | string | `fleet`, `agent`, or `agent_approve` |
| `outcome` | string | `resolved` \| `escalated` \| `abandoned` \| `unknown` |
| `escalated_to` | string | Series ID of the follow-on Playbook (when `outcome=escalated`) |
| `findings_summary` | string | Operator-provided summary of diagnosis and action taken |
| `context_id` | string | A2A session ID (agent-mode runs only) |
| `operator` | string | Identity from `X-User` request header |
| `started_at` | RFC3339 | When the run was initiated |
| `completed_at` | RFC3339 | When the run was patched with a final outcome |
| `diagnostic_report` | object | Structured hypothesis report parsed from the agent's response. `null` when the agent did not emit `HYPOTHESIS_N:` lines. See [Structured diagnostic report](#structured-diagnostic-report). |

---

## Adaptive triage

The three Database Down Playbooks form an adaptive triage system. Rather than following a fixed script, the agent gathers evidence, tests hypotheses, and navigates the escalation graph based on what it finds.

### Entry points

The `entry_point: true` field marks the **starting Playbook** for a problem class. When a database goes completely unreachable, the operator runs the entry-point Playbook for `problem_class: availability` — currently **Database Down — Restart Triage** — regardless of the suspected cause. The agent classifies the failure from pod logs and either resolves it (pod restart suffices) or escalates along the appropriate path.

Only one Playbook per `problem_class` should have `entry_point: true`.

### Escalation graph

`escalates_to` lists the series IDs the agent should consider next if its current hypothesis is disproven by the evidence. This forms a directed graph of triage paths:

```
pbs_db_restart_triage  (entry_point: true)
        │
        ├─ K8s: logs show bad config      → pbs_db_config_recovery
        │                                          │
        │                                          └─ logs show corrupt data → pbs_db_pitr_recovery
        │
        ├─ K8s: logs show corrupt/missing files → pbs_db_pitr_recovery
        │
        └─ Docker-hosted DB (agent cannot read docker logs)
                                          → pbs_sysadmin_docker_inspect
                                              (sysadmin agent reads container
                                               state + logs, revises hypothesis)
                                                    │
                                                    ├─ exitcode=0, clean shutdown
                                                    │       → pbs_db_restart_action [manual]
                                                    │
                                                    └─ "No space left on device" in pg_wal
                                                            → pbs_wal_disk_full [manual]
                                                              (free disk space first,
                                                               then restart)
```

The agent is prompted with the escalation paths at run time:

> "If your investigation reveals a different root cause than this Playbook addresses, the next Playbooks to consider are (by series ID): `pbs_db_config_recovery`, `pbs_db_pitr_recovery`, `pbs_sysadmin_docker_inspect`"

For Docker-hosted databases, the DB agent is instructed to emit `ESCALATE_TO: pbs_sysadmin_docker_inspect` immediately after confirming "connection refused" — it cannot read Docker container logs, so it cannot distinguish a clean stop from a crash or a disk-full condition. The SysAdmin agent, which runs as the second stage, calls `check_host` and `get_host_logs` and explicitly states whether the DB agent's prior hypothesis was confirmed, revised, or corrected. If the logs contain `No space left on device` with a `pg_wal` path, it escalates to `pbs_wal_disk_full` rather than directly to the restart playbook, since restarting with a full WAL disk will immediately re-PANIC.

### Requires-evidence warnings

`requires_evidence` contains log patterns or error substrings that should be present in the operator's context before this Playbook is appropriate. They are regex-compatible (e.g. `"FATAL.*invalid value for parameter"`).

When you run a Playbook with `requires_evidence` set and the patterns are absent from the `context` you provided (or you provided no context at all), the response includes a `warnings` array:

```json
{
  "text": "...",
  "warnings": [
    "expected evidence pattern not found in provided context: \"FATAL.*invalid value for parameter\"",
    "expected evidence pattern not found in provided context: \"FATAL.*configuration file\""
  ]
}
```

This signals that you may have selected the wrong Playbook for the failure mode. To suppress the warning, include the relevant log lines in the `context` field.

Warnings never block execution — they are advisory.

### Structured diagnostic report

For agent-mode runs the agent emits a ranked hypothesis block before the standard escalation signal:

```
HYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: 0.90 | EVIDENCE: "<verbatim quote from tool output>"
HYPOTHESIS_2: <alternative> | CONFIDENCE: 0.20 | REJECTED: <one-sentence reason why this is not the root cause>
ROOT_CAUSE: HYPOTHESIS_1
FINDINGS: <one-sentence summary of the root cause and recommended action>
ACTION_TAKEN: <what was done, or "none — escalation recommended">
TRANSITION_TO: <series_id>   # same-domain triage→remediation; or
ESCALATE_TO: <series_id>     # cross-domain escalation to a different agent
```

Rules the agent follows:

- Hypotheses are listed in descending confidence order.
- `EVIDENCE` is a verbatim short quote from actual tool output, not a paraphrase.
- Every non-primary hypothesis has a `REJECTED:` reason.
- `CONFIDENCE` is in the range 0.0–1.0.
- `FINDINGS` is the human-readable summary consumed by run tracking.

The Gateway parses this block and stores it as a `DiagnosticReport` on the `PlaybookRun` record. Retrieve it via `GET /api/v1/fleet/playbook-runs/{runID}` — the `diagnostic_report` field is included in the run JSON:

```bash
curl -s http://localhost:8080/api/v1/fleet/playbook-runs/plr_3f7a2b1c \
  | jq '.diagnostic_report'
```

```json
{
  "hypotheses": [
    {
      "rank": 1,
      "text": "Container was stopped by an operator",
      "confidence": 0.90,
      "evidence": "exitcode=0",
      "is_primary": true
    },
    {
      "rank": 2,
      "text": "Disk exhaustion caused the stop",
      "confidence": 0.20,
      "rejected_reason": "disk check showed only 45% used, no 'no space left' in logs",
      "is_primary": false
    }
  ],
  "root_cause": "Container was stopped by an operator",
  "action_taken": "none — escalation recommended"
}
```

The diagnostic report is available immediately after the agent session completes. If the agent's response does not contain any `HYPOTHESIS_N:` lines (older agent versions, or non-structured runs), `diagnostic_report` is `null`.

### Structured escalation signal

For agent-mode runs the Gateway parses a structured signal from the agent's response before returning it to the caller. The agent appends one of two signal lines after the `FINDINGS:` line, depending on the nature of the handoff:

```
FINDINGS: <one-sentence diagnosis and recommended action>
TRANSITION_TO: <series_id>   # same-domain: triage → remediation within the same series family
```

or

```
FINDINGS: <one-sentence diagnosis and recommended action>
ESCALATE_TO: <series_id>     # cross-domain: hand off to a different agent/domain
```

**`TRANSITION_TO:`** is used when the triage playbook hands off to its remediation counterpart within the same problem domain — for example, `pbs_vacuum_triage` → `pbs_vacuum_remediate`, or `pbs_lock_chain_triage` → `pbs_lock_chain_remediate`. The two playbooks form a deliberate pair; the triage agent has done its job and the next step is the expected remediation.

**`ESCALATE_TO:`** is used for true out-of-scope escalations — the diagnosis requires a different agent or domain entirely, such as a DB agent discovering a Docker-level problem and handing off to the SysAdmin agent (`ESCALATE_TO: pbs_sysadmin_docker_inspect`). These are genuinely unexpected handoffs.

The Gateway strips these lines from the visible `text` returned to the operator, then uses them to:

- Set `outcome=resolved` when only `FINDINGS:` is present, or when `FINDINGS:` contains `recommended=monitor` or `recommended=no_changes_needed` (nothing actionable found)
- Set `outcome=transitioned` and `transitioned_to=<series_id>` when `TRANSITION_TO:` is present
- Set `outcome=escalated` and `escalated_to=<series_id>` when `ESCALATE_TO:` is present
- Populate `findings_summary` with the FINDINGS text

**No-gate shortcut** — when `gate_escalation=true` and the agent's `FINDINGS:` line contains `recommended=monitor` or `recommended=no_changes_needed`, the gate does **not** fire regardless of signal line. The run is recorded as `outcome=resolved` immediately. This prevents operators from being asked to approve a pipeline that triage has already determined requires no action.

What happens next depends on **two conditions** that the Gateway checks before auto-chaining:

1. The requester's `approval_mode` (or session) authorises escalation.
2. The **target playbook's own `approval_mode`** is `session` or `auto` — playbooks with `approval_mode: manual` (or unset) always require explicit operator invocation, regardless of the requester's mode.

| Requester `approval_mode` | Target playbook `approval_mode` | Gateway behaviour |
|---|---|---|
| `auto` | `session` or `auto` | **Auto-chains immediately** — the Gateway looks up the escalated Playbook, runs it as a second agent session, merges the two diagnostic reports, and returns the combined findings in a single response. No second API call needed. |
| `auto` | `manual` or `""` | Returns `suggested_next` — the target playbook requires explicit operator invocation. |
| `session` (with `escalation` in `allowed_classes`) | `session` or `auto` | Auto-chains — the session token covers cross-agent escalation and the target allows it. |
| `session` (without `escalation`) | any | Returns `suggested_next`. |
| `manual` or `""` | any | Returns `suggested_next` — operator fires the follow-on call manually. |

#### Auto-chaining (`approval_mode=auto`)

```bash
# One call — DB agent diagnoses, escalates to SysAdmin agent, both findings returned
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_restart_triage/run \
  -H "Content-Type: application/json" \
  -d '{
    "connection_string": "staging-db",
    "approval_mode":     "auto"
  }' | jq '{text, chained_run_id, chained_findings, diagnostic_report}'
```

When chaining occurs, the response includes:

| Field | Description |
|---|---|
| `chain` | Array of per-step objects describing each chained leg. Each entry contains: `step` (1-based index), `playbook_series_id`, `agent_name`, `run_id`, `findings` (the step's `FINDINGS:` text), `text` (full agent response), `diagnostic_report` (structured hypotheses for that step). |
| `chained_run_id` | Run ID of the escalated (second) agent session (present for single-step chains; use `chain` for multi-step). |
| `chained_findings` | The second agent's `FINDINGS:` line (present for single-step chains; use `chain` for multi-step). |
| `diagnostic_report` | Merged report: hypotheses from all chained agents, re-ranked by confidence; the last agent's root cause takes precedence. |

#### Manual mode — `suggested_next`

When the run's approval mode does not permit auto-chaining, the response includes a `suggested_next` field containing a ready-to-fire request body for the escalated Playbook:

```json
{
  "text": "...",
  "escalation_hint": "pbs_sysadmin_docker_inspect",
  "suggested_next": {
    "playbook_series_id": "pbs_sysadmin_docker_inspect",
    "reason": "connection refused — docker container state unknown",
    "request": {
      "connection_string": "staging-db",
      "prior_run_id":      "plr_8c9d2e3f",
      "context":           "connection refused — docker container state unknown",
      "approval_mode":     "manual"
    }
  }
}
```

The `escalation_hint` field is still present for backwards compatibility. To trigger the follow-on Playbook manually:

```bash
HINT=$(echo "$RESP" | jq -r '.escalation_hint // empty')

if [ -n "$HINT" ]; then
  NEXT_ID=$(curl -s "http://localhost:8080/api/v1/fleet/playbooks?series_id=$HINT" \
    | jq -r '.playbooks[0].playbook_id')
  FIRST_RUN_ID=$(echo "$RESP" | jq -r .run_id)
  echo "Escalating to $HINT (Playbook $NEXT_ID)"
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

### Approval modes

Agent-mode runs may call write or destructive tools (e.g. `restart_deployment`, `cancel_query`) and may trigger cross-agent escalation chains. Four modes control whether those operations are gated:

| Mode | Tool calls (write/destructive) | Cross-agent escalation |
|---|---|---|
| `auto` | Proxied immediately — no gate | Auto-chains if the **target playbook's `approval_mode`** is `session` or `auto`; returns `suggested_next` otherwise |
| `session` | Gated by session token | Auto-chains if session includes `"escalation"` in `allowed_classes` **and** target playbook's `approval_mode` is `session` or `auto` |
| `manual` | Rejected (403) | Always returns `suggested_next` — operator fires the second call manually |
| `force` | Proxied immediately — no gate | Auto-chains through **all** playbooks, including those with `approval_mode: manual`. Use when deliberately authorising the full diagnosis-to-remediation path end-to-end. |

The same `approval_mode` governs both tool-level gates and cross-agent escalation. Additionally, each playbook's own `approval_mode` acts as a **chaining gate**: a playbook with `approval_mode: manual` (or unset) can never be auto-chained by `auto` or `session` — only `force` bypasses this. This means `approval_mode` on a playbook has two roles: it sets the default for standalone invocations and it controls whether that playbook can be a chaining target.

#### Full-chain automation (`approval_mode=force`)

Use `force` when you want the gateway to run the complete diagnosis-to-remediation chain in a single API call — including remediation playbooks that would otherwise require explicit operator invocation:

```bash
# One call: DB triage → docker inspect → restart container (if diagnosis is unambiguous)
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_restart_triage/run \
  -H "Content-Type: application/json" \
  -d '{
    "connection_string": "staging-db",
    "approval_mode":     "force"
  }' | jq '{chain: [.chain[] | {step, agent_name, findings}], text}'
```

`force` is intentionally named to make the operator's conscious choice visible in the audit trail. Every tool execution and policy decision event records `approval_mode: force`, so the audit log clearly shows that a human made a deliberate decision to automate the full chain, rather than the system doing it silently.

#### Per-database override restrictions (`approval_override_roles`)

A database entry in `infrastructure.json` can declare which roles are permitted to request an approval mode more permissive than the playbook's own `approval_mode` setting:

```json
{
  "db_servers": {
    "prod-primary": {
      "name": "Production Primary",
      "connection_string": "host=prod.internal port=5432 dbname=app user=app",
      "tags": ["production"],
      "sensitivity": ["pii"],
      "approval_override_roles": ["dba_lead", "oncall_senior"]
    }
  }
}
```

With this configuration: a caller requesting `approval_mode: force` against `prod-primary` must hold the `dba_lead` or `oncall_senior` role. A caller without a matching role is silently **clamped** to the playbook's declared mode (typically `manual`) — the run proceeds, but without the requested override. The response `warnings` array records what was requested, what it was clamped to, and the caller's identity:

```json
{
  "warnings": [
    "approval_mode clamped to \"manual\": override to \"force\" requires one of roles [dba_lead oncall_senior] (caller: bob@example.com)"
  ]
}
```

Databases with no `approval_override_roles` (e.g., dev or test databases) are unrestricted — any caller may pass any approval mode, as before. The restriction only applies when a caller requests a mode **more permissive** than the playbook's declared mode; requesting `manual` on a `manual` playbook is never an override and is never gated.

#### Creating an approval session (`session` mode)

Create a session token on auditd before starting the run. The token specifies which action classes it covers and for how long:

```bash
# Grant 30 minutes of write + destructive authority, plus cross-agent escalation chaining
SESSION=$(curl -s -X POST http://localhost:1199/v1/approval/sessions \
  -H "Content-Type: application/json" \
  -d '{
    "granted_by":      "alice@example.com",
    "expires_in_secs": 1800,
    "allowed_classes": ["write", "destructive", "escalation"],
    "scope":           "pbs_db_restart_triage"
  }' | jq -r .session_id)

echo "Session: $SESSION"   # aps_3f7a2b1c

# Run the playbook using the session token
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_restart_triage/run \
  -H "Content-Type: application/json" \
  -d "{
    \"connection_string\": \"postgres://prod-db.example.com/app\",
    \"context\": \"pod in CrashLoopBackOff since 10:00 UTC\",
    \"approval_mode\":    \"session\",
    \"approval_session\": \"$SESSION\"
  }" | jq .text

# Revoke the session early when the maintenance window closes
curl -s -X DELETE http://localhost:1199/v1/approval/sessions/$SESSION
```

**Session validation:** the gateway calls auditd to validate the session before proxying each write or destructive tool call. If the session is expired, revoked, or does not cover the tool's action class, the gateway returns `403` with:

```json
{
  "error":  "approval_session_required",
  "detail": "session missing, expired, or does not cover this action class"
}
```

The `scope` field is informational — it is stored on the session record for audit purposes but not enforced by the gateway. Use it to document which playbook or maintenance window the session was created for.

See `GET /v1/approval/sessions/{id}` in [AUDIT.md §6.8](AUDIT.md#68-approval-sessions) for the full session API.

#### Manual mode

```bash
curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_restart_triage/run \
  -H "Content-Type: application/json" \
  -d '{
    "connection_string": "postgres://prod-db.example.com/app",
    "context":           "pod CrashLoopBackOff — want diagnosis only, no restarts",
    "approval_mode":     "manual"
  }' | jq '{text, diagnostic_report}'
```

The agent runs through its full investigation and produces a `diagnostic_report` and `FINDINGS:` signal, but cannot execute any write or destructive tool. Use this when you want to understand what the agent _would_ do before granting authority.

### Full Database Down triage example

```bash
GW=http://localhost:8080
CONN="postgres://prod-db.example.com/app"
LOGS="$(cat /tmp/postgres.log)"  # retrieved via SSH or uploaded via /uploads

# 1. Find the entry-point Playbook for availability
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

# 3. If the agent recommends escalation, continue in the next Playbook
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

## agent_approve execution mode

`agent_approve` is the third execution mode for Playbooks. Where `agent` mode lets the LLM run autonomously (gathering evidence and deciding actions without human gates), `agent_approve` puts the operator in the loop at every step: the gateway proposes one action, the operator approves or denies it, the gateway executes and re-plans, and the loop continues until done.

Use `agent_approve` for Playbooks that perform **mutating** operations — process terminations, setting changes, restarts — where you want a human to confirm each individual tool call before it fires. Critically, the operator sees the proposed action and its stated reason *before* it executes — this is the only point in the system where cascade risks (e.g. "terminating this session will also roll back session B's uncommitted writes") are surfaced to a human before damage is done.

`agent_approve` requires the gateway to be configured with a planner LLM (the same one used by fleet planning). If no planner LLM is wired up, runs fail at the first step with `planner LLM not configured`.

### Reactive re-planning

The defining property of `agent_approve` — and what distinguishes it from `fleet` mode — is that the plan is not generated up front. After each step executes, its result is fed back to the LLM, which proposes the next step against the **actual observed state** rather than a frozen prediction made before the run started.

Concretely, on every `/proceed` call the gateway:

1. Persists the executed step's result to the run's step history.
2. Re-invokes the planner LLM with the Playbook guidance and the **full history of executed steps and their results so far** as context.
3. The LLM either proposes the next single tool call (with a fresh `reason`) or signals `complete` with a summary.

This means the plan adapts to evidence as it arrives. A few concrete consequences:

- **Step count is not fixed.** A lock-chain remediation against a 2-session chain produces fewer `get_session_info` steps than one against a 5-session chain — the LLM inspects every intermediate it discovers, and the chain depth is only known after `get_blocking_queries` returns.
- **State changes between steps are absorbed.** If a blocked session disconnects on its own between step 1 and step 3, the LLM sees the updated `get_blocking_queries` output and skips the now-unnecessary termination.
- **The `reason` field is grounded in real data.** Cascade warnings ("terminating root will also roll back session 92621 with `has_writes=true`") are written after the LLM has actually inspected session 92621 — they are not boilerplate from the Playbook guidance.
- **Failures truncate the plan.** If the LLM cannot produce a valid next step (parse error, empty tool, LLM error), the run is abandoned at the current step rather than continuing on a stale plan.

Contrast with `fleet`, where the planner emits the full multi-step job up front and `fleet-runner` executes it. In that mode, the only way to react to evidence is `--replan` (see [docs/FLEET.md](FLEET.md)), which regenerates the plan offline against drifted schema — not against tool results. `agent_approve` is fundamentally reactive; `fleet` is fundamentally batch.

### When to use each execution mode

| Mode | What the LLM does | What the operator does | When to use |
|---|---|---|---|
| `fleet` | Generates a full multi-step job plan | Reviews the plan, runs it via fleet-runner | Batch maintenance, scheduled tasks |
| `agent` | Runs autonomously to diagnosis | Reviews the findings | Read-only triage and diagnosis |
| `agent_approve` | Proposes one step at a time, re-plans after each | Approves or denies each step | Mutating remediation requiring human gates |

### Starting an agent_approve run

```bash
# Find the playbook ID for the remediation series
PB=$(curl -s "http://localhost:8080/api/v1/fleet/playbooks?series_id=pbs_lock_chain_remediate" \
  | jq -r '.playbooks[0].playbook_id')

# Start the run
RESP=$(curl -s -X POST "http://localhost:8080/api/v1/fleet/playbooks/$PB/run" \
  -H "Content-Type: application/json" \
  -d '{"connection_string": "postgres://prod-db.example.com/app"}')

echo "$RESP" | jq '{status, run_id, step: .step}'
```

Response (`202 Accepted`):

```json
{
  "run_id":      "plr_3f7a2b1c",
  "status":      "pending_approval",
  "approval_id": "apr_9a8b7c6d",
  "step": {
    "index":  1,
    "agent":  "database",
    "tool":   "get_blocking_queries",
    "args":   {"connection_string": "postgres://prod-db.example.com/app"},
    "reason": "Confirm the root blocker is still present before terminating."
  }
}
```

The `reason` field is written by the LLM and shown to the operator to explain why this step is being proposed. The operator should read it before approving. For destructive steps, the Playbook guidance instructs the LLM to include cascade side effects in `reason` — for example, if terminating a root blocker will also roll back an intermediate session's uncommitted writes, that warning appears here before the operator approves.

### Approving a step

```
POST /api/v1/fleet/playbook-runs/{runID}/proceed
```

| Field | Required | Description |
|---|---|---|
| `resolution` | yes | `approved` or `denied` |
| `resolved_by` | yes | Identity of the operator approving the step |
| `step_index` | yes | Must match the `step.index` in the pending response (prevents double-approval races) |
| `reason` | no | Free-form note (recorded in audit log; useful for denials) |

```bash
RUN_ID=$(echo "$RESP" | jq -r .run_id)
STEP=$(echo "$RESP" | jq -r '.step.index')

curl -s -X POST "http://localhost:8080/api/v1/fleet/playbook-runs/$RUN_ID/proceed" \
  -H "Content-Type: application/json" \
  -d "{\"resolution\":\"approved\",\"resolved_by\":\"alice@example.com\",\"step_index\":$STEP}"
```

If the step was the last one, the response is `200 OK` with `status: "complete"`:

```json
{
  "run_id":  "plr_3f7a2b1c",
  "status":  "complete",
  "summary": "Root blocker (pid=9847) terminated. Lock queue cleared — 3 previously blocked sessions resumed."
}
```

If more steps are needed, the response is another `pending_approval` with the next proposed step. Repeat the approve-and-proceed loop until `status: "complete"`.

If the operator denies a step, the run is abandoned:

```bash
curl -s -X POST "http://localhost:8080/api/v1/fleet/playbook-runs/$RUN_ID/proceed" \
  -H "Content-Type: application/json" \
  -d "{\"resolution\":\"denied\",\"resolved_by\":\"alice@example.com\",\"step_index\":$STEP,\"reason\":\"Session holds an uncommitted write — deferring to on-call DBA.\"}"
```

Response: `200 OK` with `status: "denied"`. The run is marked `outcome=abandoned` in auditd.

Two other terminal failure modes exist, both of which return `422 Unprocessable Entity` and mark the run `outcome=abandoned`:

- **Tool execution failed** — the operator approved the step, but the agent's tool call returned an error (network failure, agent-side validation error, policy denial). Response body: `tool execution failed: <err>`. The step's status is set to `failed` in the run's step history; no further re-planning is attempted.
- **Re-planning failed** — the step executed successfully, but the subsequent call to the planner LLM failed (LLM unavailable, malformed response, empty tool name). Response body: `re-planning failed after step N: <err>`. The successful step is preserved in the history; only the run as a whole is abandoned.

In both cases the operator must inspect the run's step history (via `GET /api/v1/fleet/playbook-runs/{runID}/steps`) to see what executed before the failure.

### Full shell example

```bash
GW=http://localhost:8080
CONN="postgres://prod-db.example.com/app"
OPERATOR="alice@example.com"

# Find the active remediation playbook
PB=$(curl -s "$GW/api/v1/fleet/playbooks?series_id=pbs_lock_chain_remediate" \
  | jq -r '.playbooks[0].playbook_id')

# Start the run
RESP=$(curl -s -X POST "$GW/api/v1/fleet/playbooks/$PB/run" \
  -H "Content-Type: application/json" \
  -d "{\"connection_string\":\"$CONN\"}")

RUN_ID=$(echo "$RESP" | jq -r .run_id)

# Step-by-step approval loop
while true; do
  STATUS=$(echo "$RESP" | jq -r .status)

  if [ "$STATUS" = "complete" ]; then
    echo "Done: $(echo "$RESP" | jq -r .summary)"
    break
  fi

  if [ "$STATUS" != "pending_approval" ]; then
    echo "Unexpected status: $STATUS" >&2
    exit 1
  fi

  TOOL=$(echo "$RESP"   | jq -r '.step.tool')
  ARGS=$(echo "$RESP"   | jq -r '.step.args | tostring')
  REASON=$(echo "$RESP" | jq -r '.step.reason')
  STEP=$(echo "$RESP"   | jq -r '.step.index')

  echo ""
  echo "Step $STEP: $TOOL"
  echo "  Args:   $ARGS"
  echo "  Reason: $REASON"
  read -r -p "Approve? [y/n] " choice

  if [ "$choice" != "y" ]; then
    curl -s -X POST "$GW/api/v1/fleet/playbook-runs/$RUN_ID/proceed" \
      -H "Content-Type: application/json" \
      -d "{\"resolution\":\"denied\",\"resolved_by\":\"$OPERATOR\",\"step_index\":$STEP}" > /dev/null
    echo "Step denied — run abandoned."
    break
  fi

  RESP=$(curl -s -X POST "$GW/api/v1/fleet/playbook-runs/$RUN_ID/proceed" \
    -H "Content-Type: application/json" \
    -d "{\"resolution\":\"approved\",\"resolved_by\":\"$OPERATOR\",\"step_index\":$STEP}")
done
```

### Automated approval (CI and fault tests)

In CI or automated test harnesses, `--approval-mode force` auto-approves every step and logs each proposal before executing:

```bash
go run ./testing/cmd/faulttest run \
  --ids db-tx-lock-chain-blocker --external --conn faulttest-db \
  --db-agent http://localhost:8080 --via-gateway --gateway http://localhost:8080 \
  --infra-config ~/cassiopeia/claude/infrastructure.json \
  --remediate --approval-mode force
```

Each step appears as a structured log line before execution:

```
level=INFO msg="agent_approve: pending step" step_index=5 tool=terminate_connection \
  pid=92612 reason="Root blocker (blocking_pid=NULL, has_writes=true, 36s idle). \
  Terminating root will cascade to roll back session 92621 (has_writes=true, 39s idle) \
  — operator should confirm this work can be discarded before approving."
```

The loop completes without pausing. `force` chains through all playbooks including those with `approval_mode: manual`, making it suitable for unattended regression runs where the safety story is tested rather than exercised.

### Interactive approval (human-in-the-loop demo)

`--approval-mode manual` turns the approval loop into a live terminal prompt. For each proposed step, `faulttest` prints the tool, its logical arguments (connection plumbing stripped), and the full reason field — then waits for `y/n` before sending `/proceed`:

```bash
go run ./testing/cmd/faulttest run \
  --ids db-tx-lock-chain-blocker --external --conn faulttest-db \
  --db-agent http://localhost:8080 --via-gateway --gateway http://localhost:8080 \
  --infra-config ~/cassiopeia/claude/infrastructure.json \
  --remediate --approval-mode manual
```

The read-only steps (`get_blocking_queries`, `get_session_info`) appear first. The number of `get_session_info` steps varies — the step proposer inspects every session it classifies as an intermediate before terminating, so a longer chain produces more inspection steps. The `terminate_connection` step always comes last before the verification. At that step the output looks like:

```
────────────────────────────────────────────────────────────────
  Step N — terminate_connection

  pid:       97617
  reason:    Root blocker (blocking_pid=NULL, idle in transaction).
             Root has_writes=true, idle_secs=40. WARNING:
             terminating root will cascade to roll back session
             97618 (has_writes=true, 43s idle) — operator should
             confirm this work can be discarded before approving.
             Session 97619 (has_writes=false, read-only) and 97620
             (has_writes=false, read-only) will rollback instantly
             with no data loss.
────────────────────────────────────────────────────────────────
  Approve? [y/n]:
```

The `reason` field is what the Playbook guidance instructs the LLM to produce: before the operator sees a destructive action, they see exactly why it is being proposed and what side effects to expect. If the operator types `n`, `faulttest` sends `resolution: "denied"` to `/proceed` and the gateway marks the run abandoned — nothing is executed.

This is the contrast with Crystal Ball mode: an unguided agent would either terminate silently (if running autonomously) or, as observed in testing, present `cancel_query` as "Option 1 (Immediate)" with an incorrect description of its effect. The guided `agent_approve` path surfaces the correct action, its reason, and its cascade risk — all before the operator commits.

### Step tracking

Every proposed and executed step is recorded in auditd and accessible via the following endpoints.

**List steps for a run:**

```
GET /api/v1/fleet/playbook-runs/{runID}/steps
```

```bash
curl -s http://localhost:8080/api/v1/fleet/playbook-runs/plr_3f7a2b1c/steps | jq .
```

Response:

```json
{
  "steps": [
    {
      "run_id":      "plr_3f7a2b1c",
      "step_index":  1,
      "agent":       "database",
      "tool":        "get_blocking_queries",
      "args":        {"connection_string": "..."},
      "reason":      "Identify the full lock chain and confirm the root blocker is still present.",
      "status":      "succeeded",
      "result":      "2-level chain: root pid=9847 (idle in transaction, no blocking_pid); intermediate pid=10234 (blocked by 9847, itself blocking 2 sessions)",
      "approval_id": "apr_9a8b7c6d",
      "created_at":  "2026-05-22T10:00:00Z",
      "updated_at":  "2026-05-22T10:00:05Z"
    },
    {
      "run_id":      "plr_3f7a2b1c",
      "step_index":  2,
      "agent":       "database",
      "tool":        "get_session_info",
      "args":        {"pid": 9847},
      "reason":      "Inspect root blocker (pid=9847) before acting: confirm idle-in-transaction state and check has_writes. Also inspecting intermediate session (pid=10234) for cascade rollback risk.",
      "status":      "succeeded",
      "result":      "pid=9847: state=idle in transaction, has_open_tx=true, has_writes=true, idle_secs=47. pid=10234: has_open_tx=true, has_writes=true, idle_secs=46 (blocked victim)",
      "approval_id": "apr_2c3d4e5f",
      "created_at":  "2026-05-22T10:00:07Z",
      "updated_at":  "2026-05-22T10:00:09Z"
    },
    {
      "run_id":      "plr_3f7a2b1c",
      "step_index":  3,
      "agent":       "database",
      "tool":        "terminate_connection",
      "args":        {"pid": 9847},
      "reason":      "Terminate root blocker pid=9847 (idle in transaction, has_writes=true, idle_secs=47 — under 30-min escalation threshold). WARNING: terminating root will cascade to roll back intermediate session pid=10234 (has_writes=true, 46s idle) — operator should confirm this work can be discarded before approving.",
      "status":      "succeeded",
      "result":      "terminated=true",
      "approval_id": "apr_1b2c3d4e",
      "created_at":  "2026-05-22T10:00:14Z",
      "updated_at":  "2026-05-22T10:00:16Z"
    },
    {
      "run_id":      "plr_3f7a2b1c",
      "step_index":  4,
      "agent":       "database",
      "tool":        "get_blocking_queries",
      "args":        {"connection_string": "..."},
      "reason":      "Verify the full chain cleared — intermediate and leaf sessions should have resumed.",
      "status":      "succeeded",
      "result":      "no blocked sessions",
      "approval_id": "apr_5e6f7a8b",
      "created_at":  "2026-05-22T10:00:19Z",
      "updated_at":  "2026-05-22T10:00:21Z"
    }
  ]
}
```

**Get the current pending step:**

```
GET /api/v1/fleet/playbook-runs/{runID}/pending-step
```

Returns the single step currently awaiting approval (`status=proposed`). Returns `404` if no step is pending. Use this to poll for the next step after calling `/proceed`, or to resume an in-progress run after a gateway restart.

### `PlaybookRunStep` object

| Field | Type | Description |
|---|---|---|
| `run_id` | string | The run this step belongs to |
| `step_index` | int | 1-based sequential index within the run |
| `agent` | string | Agent that will execute the tool (e.g. `database`) |
| `tool` | string | Tool name proposed by the LLM |
| `args` | object | Arguments for the tool call |
| `reason` | string | LLM-written explanation shown to the operator at approval time |
| `status` | string | `proposed` → `approved` or `denied` → `executing` → `succeeded` or `failed` |
| `approval_id` | string | `apr_*` approval record ID (set after the step is approved) |
| `result` | string | Tool output (set after execution) |
| `error` | string | Error message if the tool call failed |
| `created_at` | RFC3339 | When the step was proposed |
| `updated_at` | RFC3339 | When the step was last updated |

### Diagnosis / remediation playbook split

The `agent_approve` mode naturally pairs with an `agent`-mode diagnosis Playbook. The recommended pattern for a fault that requires both investigation and a mutating fix:

1. Run the **diagnosis Playbook** (`execution_mode: agent`, `approval_mode: manual`) — the agent gathers evidence autonomously and returns a structured `DiagnosticReport` with findings. No mutations.
2. Review the findings. If the agent recommends action, invoke the **remediation Playbook** (`execution_mode: agent_approve`, `approval_mode: manual`) — the gateway proposes one step at a time and you approve each one.

The `pbs_lock_chain_triage` / `pbs_lock_chain_remediate` pair demonstrates this pattern:

```bash
# 1. Diagnose (autonomous, read-only)
TRIAGE_PB=$(curl -s "$GW/api/v1/fleet/playbooks?series_id=pbs_lock_chain_triage" \
  | jq -r '.playbooks[0].playbook_id')
DIAG=$(curl -s -X POST "$GW/api/v1/fleet/playbooks/$TRIAGE_PB/run" \
  -H "Content-Type: application/json" \
  -d "{\"connection_string\":\"$CONN\"}")

echo "$DIAG" | jq -r .text     # agent findings
TRIAGE_RUN=$(echo "$DIAG" | jq -r .run_id)

# 2. Review — if diagnosis confirms idle-in-tx blocker, proceed to remediation
REMED_PB=$(curl -s "$GW/api/v1/fleet/playbooks?series_id=pbs_lock_chain_remediate" \
  | jq -r '.playbooks[0].playbook_id')
curl -s -X POST "$GW/api/v1/fleet/playbooks/$REMED_PB/run" \
  -H "Content-Type: application/json" \
  -d "{\"connection_string\":\"$CONN\",\"prior_run_id\":\"$TRIAGE_RUN\"}"
# → {status: "pending_approval", step: {tool: "get_blocking_queries", ...}}
# Continue the approval loop as shown above.
```

Passing `prior_run_id` threads the triage findings into the remediation Playbook's context — the LLM knows what was already confirmed and does not repeat diagnostic steps.

---

## Importing Playbooks

The import endpoint converts existing runbooks into Playbook drafts without persisting them. The caller reviews the draft and, if satisfied, calls `POST /api/v1/fleet/playbooks` to save it.

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

For `format=yaml`, the import endpoint expects the canonical aiHelpDesk Playbook YAML schema. This is the same format used by the system Playbooks in the `playbooks/` directory:

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
execution_mode: fleet            # fleet (default) | agent | agent_approve
entry_point: false               # true = preferred starting Playbook for this problem_class
escalates_to: []                 # series IDs of follow-on Playbooks if hypothesis is wrong
requires_evidence: []            # log patterns expected before selecting this Playbook
```

`name` and `description` are required. Missing fields produce `warning_messages` and reduce `confidence` to `0.8`.

When importing via LLM (`format=markdown`, `text`, `rundeck`, `ansible`), the importer infers `execution_mode` and `entry_point` from context and extracts `requires_evidence` from "when to use" language in the source. `escalates_to` is always left empty on import — series IDs of other Playbooks cannot be inferred from text and must be filled in by the operator after reviewing the draft.

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

A typical workflow for adding a new version of an existing Playbook:

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
| `symptoms` | []string | Observable indicators that should trigger this Playbook |
| `guidance` | string | Expert reasoning injected into the planner prompt |
| `escalation` | []string | Conditions requiring human escalation |
| `target_hints` | []string | Tag names or server name patterns for target resolution |
| `related_playbooks` | []string | `pb_*` IDs of related Playbooks |
| `author` | string | Author identity or team name |
| `version` | string | Free-form version string |
| `is_active` | bool | `true` if this is the active version in its series |
| `is_system` | bool | `true` for Playbooks shipped with aiHelpDesk (read-only) |
| `source` | string | `system` \| `imported` \| `manual` |
| `entry_point` | bool | `true` marks this as the preferred starting Playbook for its `problem_class`. Used by the planner to resolve "where do I start?" when multiple Playbooks could apply. Only one Playbook per problem class should have `entry_point=true`. |
| `escalates_to` | []string | Series IDs (`pbs_*`) of Playbooks to consider next if this Playbook's hypothesis is disproven by the collected evidence. Injected into the agent prompt as escalation context. |
| `requires_evidence` | []string | Log patterns or error signals expected to be present before this Playbook is selected. Expressed as case-insensitive substrings or regex fragments (e.g. `"FATAL.*invalid value for parameter"`). At run time the Gateway checks these patterns against the `context` field of the run request and emits `warnings` for any that are missing. Execution is never blocked — warnings are advisory only. |
| `execution_mode` | string | `fleet` (default) — routes through the fleet planner and returns a `FleetPlanResponse`. `agent` — routes directly to an agent as an autonomous triage session; the agent collects evidence, forms hypotheses, and returns a structured diagnosis with recommended (not executed) remediation steps. `agent_approve` — the gateway drives a step-by-step loop: the LLM proposes one action at a time, the operator approves each step before it executes, the gateway executes via direct tool dispatch, and the LLM re-plans based on the result; returns `202 Accepted` with `pending_approval` status until the run completes. See [agent_approve execution mode](#agent_approve-execution-mode). |
| `agent_name` | string | For `execution_mode: agent` and `agent_approve` Playbooks, the agent to route to. Defaults to the database agent (`database_agent`) if omitted. Use `sysadmin_agent` for Playbooks that require host-level diagnostics. |
| `approval_mode` | string | Default approval mode for runs of this Playbook: `auto`, `session`, or `manual`. Can be overridden per run via the request body. Defaults to `""` which is treated as `manual` (safe default). For Playbooks that cross agent boundaries via auto-chaining, this also gates cross-agent escalation — see [Approval modes](#approval-modes). |
| `stats` | object | Inline run statistics for the Playbook's series. Populated by `GET /fleet/playbooks` (list); omitted when no runs have been recorded. See `PlaybookRunStats` below. Not persisted — computed on read. |
| `created_at` | RFC3339 | Creation timestamp |
| `updated_at` | RFC3339 | Last update timestamp |

### `PlaybookRunStats` object

Returned inline in `GET /fleet/playbooks` and by `GET /fleet/playbooks/{playbookID}/stats`. Stats are **series-wide** — they aggregate all versions of a Playbook.

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
| `resolved` | Agent-mode: Gateway parses a `FINDINGS:` line with no follow-on signal, **or** the `FINDINGS:` line contains `recommended=monitor` or `recommended=no_changes_needed`. Fleet-mode: operator PATCHes the run after confirming the plan resolved the issue. |
| `transitioned` | Agent-mode: Gateway parses a `TRANSITION_TO: <series_id>` signal; `transitioned_to` is set to that series ID. Indicates the triage-to-remediation handoff completed within the same problem domain. |
| `escalated` | Agent-mode: Gateway parses an `ESCALATE_TO: <series_id>` signal; `escalated_to` is set to that series ID. Indicates a true cross-domain handoff to a different agent. |
| `abandoned` | Operator explicitly PATCHes the run with `outcome=abandoned` — used when an investigation was started but not completed (e.g. alert cleared before diagnosis, wrong Playbook selected). Also set when a gate is denied. |
| `unknown` | Default at run start. Remains `unknown` if the agent's response contained no parseable signal and the operator has not yet patched the run. Runs that stay `unknown` are **not counted** in `resolution_rate` or `escalation_rate` — only the denominator `total_runs` includes them. |

`resolution_rate` and `escalation_rate` use `total_runs` (not `resolved + escalated`) as the denominator, so `unknown` and `abandoned` runs dilute the rates. A low `resolution_rate` on an agent-mode Playbook often means the agent is not producing parseable `FINDINGS:` signals — check the `findings_summary` field on recent runs via `GET /playbook-runs/{runID}`.
