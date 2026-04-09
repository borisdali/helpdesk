# aiHelpDesk Rollback & Undo

This is a sub-module of aiHelpDesk [AI Governance](AIGOVERNANCE.md).

When an agent-initiated mutation turns out to be wrong — a deployment scaled to
the wrong replica count, rows updated with bad values, a pod deleted before its
work was finished — aiHelpDesk can generate and execute the compensating
operation that reverses it. Every rollback is a first-class governed action:
it goes through the same policy check, approval gate, and audit trail as the
original mutation.

This page covers the full rollback lifecycle for single-event rollbacks and
fleet-job rollbacks. Feel free to jump to a [sample rollback run](ROLLBACK_SAMPLE.md) to see all
of this in action.

---

## Table of Contents

1. [How it works](#1-how-it-works)
2. [Reversibility classification](#2-reversibility-classification)
3. [Pre-mutation state capture](#3-pre-mutation-state-capture)
   - [Kubernetes](#31-kubernetes)
   - [Database (Tier 1 — row-capture)](#32-database-tier-1--row-capture)
   - [Database (Tier 2 — WAL decode)](#33-database-tier-2--wal-decode)
   - [Capability detection](#34-capability-detection)
4. [HTTP API](#4-http-api)
5. [CLI](#5-cli)
6. [Fleet rollback](#6-fleet-rollback)
7. [Governance](#7-governance)
8. [Limitations](#8-limitations)
9. [Testing](#9-testing)

---

## 1. How it works

```
Original mutation                    Rollback lifecycle
─────────────────                    ─────────────────────────────────────────────
Agent calls scale_deployment    →  1. PreState captured in audit event (pre_state field)
Event recorded in auditd        →  2. POST /v1/events/{id}/rollback-plan  → derive plan
                                   3. POST /v1/rollbacks                 → initiate
                                      ├─ duplicate check (409 if already exists)
                                      ├─ reversibility check (422 if not reversible)
                                      ├─ policy check (403 if denied)
                                      ├─ approval gate → status = pending_approval
                                      └─ RollbackExecutor.Execute (auto or after approval)
                                         ├─ dispatch inverse op to gateway
                                         ├─ emit rollback_executed event
                                         └─ emit rollback_verified event
```

The `pre_state` field is written **inside** the `tool_execution` audit event at mutation
time. No secondary table join is needed to derive a plan later.

---

## 2. Reversibility classification

The table below lists just the initial set of aiHelpDesk mutations, their reversibility and the potential rollback mechanism:

| Tool | Reversibility | Mechanism |
|------|--------------|-----------|
| `scale_deployment` | **Yes** | Restore replica count from `ScalePreState` captured before scaling |
| `exec_update` | **Yes** | Tier 1 SELECT snapshot or Tier 2 WAL decode → per-row UPDATE |
| `exec_delete` | **Yes** | Tier 1 SELECT snapshot or Tier 2 WAL decode → re-INSERT |
| `exec_insert` | **Yes** | `INSERT … RETURNING` captures inserted PKs → DELETE |
| `delete_pod` | **Partial** | Controller recreates the pod automatically; informational note only |
| `restart_deployment` | **No** | Restart already happened; image rollback is a separate out-of-band operation |
| `terminate_connection` | **No** | Connection is already gone; `get_session_info` pre-flight surfaced the rollback cost before approval |
| `terminate_idle_connections` | **No** | Same as above |
| `cancel_query` | **No** | Query already cancelled; pre-flight warning is sufficient |
| All other tools | **No** | Inverse operation is tool specific |

Operations marked **Partial** return a `rollback-plan` with `reversibility: "partial"` and a
`not_reversible_reason` explaining the self-recovery path. They cannot be initiated via
`POST /v1/rollbacks` (returns `422`).

---

## 3. Pre-mutation state capture

### 3.1 Kubernetes

`scale_deployment` reads the current replica count from kubectl **before** issuing
the scale command. The snapshot is stored inline in the `tool_execution` audit event:

```json
{
  "event_type": "tool_execution",
  "tool": {
    "name": "scale_deployment",
    "pre_state": {
      "namespace": "production",
      "deployment": "api",
      "previous_replicas": 3
    }
  }
}
```

Pre-state capture is **best-effort** — a kubectl read failure logs a warning but does
not abort the scale. If `pre_state` is absent the derived plan has `reversibility: "no"`
with an explanation.

### 3.2 Database (Tier 1 — row-capture)

Works on any PostgreSQL instance with normal `SELECT` access. Before the DML executes:

| Operation | Pre-capture | Inverse SQL |
|-----------|-------------|-------------|
| `INSERT` | `INSERT … RETURNING <pk_cols>` captures the new PKs | `DELETE FROM t WHERE pk IN (…)` |
| `UPDATE` | `SELECT * FROM t WHERE <same condition>` captures old values | `UPDATE t SET col=old WHERE pk=…` (one per row) |
| `DELETE` | `SELECT * FROM t WHERE <same condition>` captures the rows | `INSERT INTO t (cols) VALUES (old_vals…)` |

Row count is bounded by the existing `max_rows_affected` blast-radius limit (default: 100, see [here](AIGOVERNANCE.md#51-db-blast-radius-max_rows_affected)).
This cap also limits the Tier 1 rollback plan to 100 rows. There is a TOCTOU gap between
the pre-SELECT and the DML; it is acceptable for approval-gated, short-window mutations.

```json
{
  "event_type": "tool_execution",
  "tool": {
    "name": "exec_update",
    "pre_state": {
      "schema": "public",
      "table": "orders",
      "operation": "update",
      "pk_columns": ["order_id"],
      "rows": [
        {"order_id": 42, "status": "pending", "updated_at": "2026-03-27T10:00:00Z"},
        {"order_id": 43, "status": "pending", "updated_at": "2026-03-27T10:01:00Z"}
      ],
      "row_count": 2,
      "captured_at": "2026-03-27T10:05:00Z",
      "tier": 1
    }
  }
}
```

### 3.3 Database (Tier 2 — LSN bracketing / WAL decoding)

Requires PostgreSQL server configured with `wal_level = logical` and the connecting
user granted the `REPLICATION` privilege. This tier:

- Captures cascades and trigger-fired side effects that Tier 1 misses
- Has no TOCTOU gap — changes are read directly from the WAL stream
- Is automatically selected when the prerequisites are met

**Flow** (wraps each DML call):

```
1. pg_create_logical_replication_slot('helpdesk_rbk_<trace8>', 'wal2json')
2. lsn_before = pg_current_wal_lsn()
3. Execute DML
4. lsn_after = pg_current_wal_lsn()
5. pg_logical_slot_peek_changes(slot, lsn_after, NULL, 'format-version', '2')
6. Parse wal2json → WALCapture
7. pg_drop_replication_slot(slot)   ← deferred; always runs even on error
```

The slot name embeds the trace ID (`helpdesk_rbk_<8 chars>`) for traceability. A
background goroutine in the database agent drops any stale `helpdesk_rbk_*` slots
older than 10 minutes (leaked by crash or cancelled context).

When `REPLICA IDENTITY FULL` is set on the target table, `OldValues` contains all
column values for `UPDATE` and `DELETE` operations. Without it, only `PKValues` is
populated and the plan falls back to Tier 1-style inverse SQL using PK equality.

```json
{
  "pre_state": {
    "schema": "public",
    "table": "orders",
    "operation": "update",
    "pk_columns": ["order_id"],
    "rows": [...],
    "row_count": 2,
    "tier": 2,
    "wal": {
      "slot_name": "helpdesk_rbk_tr_abc123",
      "lsn_before": "0/1A00000",
      "lsn_after":  "0/1A00200",
      "captured_at": "2026-03-27T10:05:01Z",
      "changes": [
        {
          "kind": "update",
          "schema": "public",
          "table": "orders",
          "pk_values": {"order_id": 42},
          "old_values": {"order_id": 42, "status": "pending"},
          "new_values": {"order_id": 42, "status": "shipped"}
        }
      ]
    }
  }
}
```

### 3.4 Capability detection

`DetectRollbackCapability` probes the target database at tool-call time and selects
the appropriate tier automatically:

```
SHOW wal_level;
SELECT rolreplication FROM pg_roles WHERE rolname = current_user;
SELECT relname, relreplident FROM pg_class
  JOIN pg_namespace ON pg_namespace.oid = relnamespace
  WHERE nspname = $1 AND relkind = 'r';
```

| Condition | Selected mode |
|-----------|---------------|
| `wal_level = logical` AND user has `REPLICATION` | `wal_decode` (Tier 2) |
| Otherwise | `row_capture` (Tier 1) |
| Blast-radius exceeded or capability detection failed | `none` |

You can override auto-detection per-target in `infrastructure.json`:

```json
{
  "name": "prod-db-1",
  "rollback_mode": "wal_decode"
}
```

Valid values: `"wal_decode"`, `"row_capture"`, `"none"`.

---

## 4. HTTP API

All rollback endpoints are served by `auditd` on port `1199`.

### `POST /v1/events/{eventID}/rollback-plan`

Derives the rollback plan for a `tool_execution` event without persisting anything.
Use this to preview what a rollback would do before initiating.

```bash
curl -X POST http://localhost:1199/v1/events/tool_abc12345/rollback-plan
```

Response (`200 OK`):

```json
{
  "original_event_id": "tool_abc12345",
  "original_tool": "scale_deployment",
  "original_trace_id": "tr_a1b2c3d4",
  "reversibility": "yes",
  "inverse_op": {
    "agent": "k8s",
    "tool": "scale_deployment",
    "args": {
      "namespace": "production",
      "deployment": "api",
      "replicas": 3
    },
    "description": "restore deployment/api in namespace production to 3 replica(s)"
  },
  "generated_at": "2026-03-27T10:10:00Z"
}
```

For irreversible operations, `reversibility` is `"no"` or `"partial"` and `not_reversible_reason` is set. `inverse_op` is absent.

**Status codes:**

| Status | Meaning |
|--------|---------|
| `200 OK` | Plan derived successfully |
| `404 Not Found` | Event ID does not exist or is not a `tool_execution` event |

---

### `POST /v1/rollbacks`

Initiate a rollback. Persists a `RollbackRecord`, emits a `rollback_initiated` audit event,
and either executes immediately (auto-approved) or waits for human approval.

```bash
curl -X POST http://localhost:1199/v1/rollbacks \
  -H "Content-Type: application/json" \
  -d '{
    "original_event_id": "tool_abc12345",
    "justification": "scaling error — wrong replica count deployed to production",
    "dry_run": false
  }'
```

**Request fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `original_event_id` | **yes** | The `event_id` of the `tool_execution` event to reverse |
| `justification` | no | Human-readable reason, recorded in the rollback record and audit trail |
| `dry_run` | no | `true` = return the plan without persisting or executing anything (default: `false`) |

**Dry-run response (`200 OK`):**

```json
{
  "dry_run": true,
  "plan": { ... }
}
```

**Initiation response (`201 Created`):**

```json
{
  "rollback": {
    "rollback_id": "rbk_a1b2c3d4",
    "original_event_id": "tool_abc12345",
    "original_trace_id": "tr_a1b2c3d4",
    "status": "pending_approval",
    "initiated_by": "alice@example.com",
    "initiated_at": "2026-03-27T10:10:00Z",
    "rollback_trace_id": "tr_rbk_a1b2c3d4",
    "plan_json": "..."
  }
}
```

**Status codes:**

| Status | Meaning |
|--------|---------|
| `201 Created` | Rollback record created; status is `pending_approval` |
| `200 OK` | Dry-run only; no record persisted |
| `400 Bad Request` | Missing `original_event_id` |
| `404 Not Found` | Event not found |
| `409 Conflict` | An active (non-terminal) rollback already exists for this event |
| `422 Unprocessable Entity` | Operation is not reversible; response includes `not_reversible_reason` |

---

### `GET /v1/rollbacks`

List rollback records. Supports optional query parameters:

| Parameter | Description |
|-----------|-------------|
| `status` | Filter by status: `pending_approval`, `executing`, `success`, `failed`, `cancelled` |
| `original_event_id` | Filter by the original event |
| `limit` | Maximum number of records (default: 50) |

```bash
curl "http://localhost:1199/v1/rollbacks?status=pending_approval"
```

Response: JSON array of `RollbackRecord` objects.

---

### `GET /v1/rollbacks/{rollbackID}`

Retrieve a single rollback record with its derived plan.

```bash
curl http://localhost:1199/v1/rollbacks/rbk_a1b2c3d4
```

Response:

```json
{
  "rollback": {
    "rollback_id": "rbk_a1b2c3d4",
    "status": "pending_approval",
    ...
  },
  "plan": {
    "reversibility": "yes",
    "inverse_op": { ... }
  }
}
```

**Status codes:** `200 OK` or `404 Not Found`.

---

### `POST /v1/rollbacks/{rollbackID}/cancel`

Cancel a rollback that has not yet reached a terminal state.

```bash
curl -X POST http://localhost:1199/v1/rollbacks/rbk_a1b2c3d4/cancel
```

Response (`200 OK`):

```json
{
  "rollback": {
    "rollback_id": "rbk_a1b2c3d4",
    "status": "cancelled",
    ...
  }
}
```

**Status codes:**

| Status | Meaning |
|--------|---------|
| `200 OK` | Cancelled successfully |
| `404 Not Found` | Rollback record not found |
| `409 Conflict` | Rollback is already in a terminal state (`success`, `failed`, or `cancelled`) |

---

### `POST /v1/fleet/jobs/{jobID}/rollback`

Initiate a rollback for a fleet job. Constructs a reverse job definition where steps
run in reverse order and servers are processed canary-last (the mirror of canary-first
in the original).

```bash
curl -X POST http://localhost:1199/v1/fleet/jobs/flj_abc123/rollback \
  -H "Content-Type: application/json" \
  -d '{
    "scope": "all",
    "justification": "bad migration — rolling back all servers"
  }'
```

**Request fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `scope` | no | `"all"` (default), `"canary_only"`, `"failed_only"`, or a JSON array of server names |
| `justification` | no | Reason recorded in the fleet rollback record and audit trail |
| `dry_run` | no | Print the reverse job definition without persisting or executing |

**Scopes:**

| Value | Meaning |
|-------|---------|
| `all` | Roll back every server the original job touched |
| `canary_only` | Roll back only the canary server(s) (useful for stopping a bad deployment before it reaches waves) |
| `failed_only` | Roll back only servers that reported a failure status |
| JSON array | Roll back an explicit named subset: `["prod-db-1", "prod-db-3"]` |

Response (`201 Created`):

```json
{
  "fleet_rollback": {
    "fleet_rollback_id": "frb_e5f6g7h8",
    "original_job_id": "flj_abc123",
    "status": "pending_approval",
    "scope": "all",
    "initiated_by": "alice@example.com"
  }
}
```

---

### `GET /v1/fleet/jobs/{jobID}/rollback`

Retrieve the fleet rollback status for a job.

```bash
curl http://localhost:1199/v1/fleet/jobs/flj_abc123/rollback
```

Response: `FleetRollbackRecord` JSON, or `404` if no rollback has been initiated for this job.

---

### Rollback record status lifecycle

```
pending_approval ──► executing ──► success
        │                │
        │                └──────► failed
        │
        └──────────────────────► cancelled
```

| Status | Meaning |
|--------|---------|
| `pending_approval` | Created; waiting for human approval before execution |
| `executing` | Compensating operation dispatched to the gateway |
| `success` | Inverse operation completed and verified |
| `failed` | Inverse operation failed; `result_output` contains the error |
| `cancelled` | Manually cancelled before execution |

---

## 5. CLI

`helpdesk-client` provides four rollback flags that talk to `auditd` directly
(they do not go through the gateway).

```bash
# Preview the rollback plan for an event (read-only, nothing persisted)
helpdesk-client \
  --audit-url http://localhost:1199 \
  --rollback-plan tool_abc12345

# Dry-run: derive and print the plan without persisting or executing
helpdesk-client \
  --audit-url http://localhost:1199 \
  --rollback-event tool_abc12345 \
  --rollback-dry-run

# Initiate a rollback
helpdesk-client \
  --audit-url http://localhost:1199 \
  --rollback-event tool_abc12345 \
  --rollback-justification "wrong replica count — restoring to 3"

# List all rollback records
helpdesk-client \
  --audit-url http://localhost:1199 \
  --list-rollbacks
```

**Flags:**

| Flag | Environment Variable | Description |
|------|----------------------|-------------|
| `--rollback-plan <eventID>` | — | Derive and print the plan for a `tool_execution` event. No persistence. |
| `--rollback-event <eventID>` | — | Initiate (or dry-run) a rollback for the event. Requires `--audit-url`. |
| `--rollback-justification <text>` | — | Reason recorded in the rollback record (used with `--rollback-event`) |
| `--rollback-dry-run` | — | Combined with `--rollback-event`: derive the plan without persisting or executing |
| `--list-rollbacks` | — | List all rollback records from the audit service |
| `--audit-url` | `HELPDESK_AUDIT_URL` | auditd base URL (required for all rollback flags) |

**Exit codes:**

| Exit code | Meaning |
|-----------|---------|
| `0` | Success |
| `1` | Error (event not found, not reversible, conflict, network failure) |

On exit code `1`, a descriptive message is printed to stderr. For `409 Conflict` the
message includes the existing rollback ID so you can inspect it with `GET /v1/rollbacks/{id}`.

---

## 6. Fleet rollback

Fleet rollbacks work by constructing a reverse `JobDef` and submitting it to `fleet-runner`
as a normal fleet job with full governance integration:

**Step order reversal:** the compensating steps run in the opposite order of the original.
If the original job ran `[exec_update, scale_deployment]`, the rollback runs
`[scale_deployment_rollback, exec_sql (inverse UPDATE)]`.

**Canary-last ordering:** the original job processed canary servers first, so they have
been in the new state the longest. The rollback processes them last, giving the most
time to verify the rollback completed correctly on wave servers before touching the canary.

**Inverse ops from per-server plans:** each server's rollback steps use the `InverseOp`
derived from that server's `tool_execution` audit events, so each server rolls back to
its own pre-mutation state (not a global average).

**Non-reversible steps:** if a step has no inverse operation (e.g. `restart_deployment`),
the original step is preserved with a `_rollback_note` arg explaining why.

**Scope:** the `scope` parameter controls which servers are included:

```bash
# Roll back only the canary server before it reaches wave phase
curl -X POST http://localhost:1199/v1/fleet/jobs/flj_abc123/rollback \
  -d '{"scope": "canary_only"}'

# Roll back only servers that reported failure
curl -X POST http://localhost:1199/v1/fleet/jobs/flj_abc123/rollback \
  -d '{"scope": "failed_only"}'
```

The resulting fleet job goes through the same approval gate, canary/wave strategy, and
full audit trail as any other fleet job. Its name is prefixed `"rollback: "` and its
`original_job_id` field links back to the job being reversed.

---

## 7. Governance

### Action classes for rollback tools

Rollback operations carry the same action class as the original mutation. This ensures
the approval threshold is equivalent — a rollback of a destructive operation requires
the same level of approval as the original.

| Rollback tool | Action class |
|---------------|-------------|
| `rollback_scale_deployment` | `destructive` |
| `rollback_exec_update` | `destructive` |
| `rollback_exec_delete` | `destructive` |
| `rollback_exec_insert` | `write` |

### Role requirements

| Endpoint | Required role |
|----------|--------------|
| `GET /v1/rollbacks` | any (admin bypass) |
| `GET /v1/rollbacks/{id}` | any (admin bypass) |
| `POST /v1/events/{id}/rollback-plan` | any (admin bypass) |
| `GET /v1/fleet/jobs/{id}/rollback` | any (admin bypass) |
| `POST /v1/rollbacks` | `operator` or `admin` |
| `POST /v1/rollbacks/{id}/cancel` | `operator` or `admin` |
| `POST /v1/fleet/jobs/{id}/rollback` | `operator`, `fleet-approver`, or `admin` |

### Audit trail

Every rollback produces three audit events:

| Event type | When emitted | Contents |
|------------|-------------|----------|
| `rollback_initiated` | On `POST /v1/rollbacks` | Rollback ID, original event ID, original trace ID, plan, initiator |
| `rollback_executed` | After the compensating op is dispatched | Status, result output |
| `rollback_verified` | After post-rollback verification | Verification result |

All three events carry the `rollback_trace_id` (`tr_rbk_*`) as their `trace_id`. The
trace ID is `"tr_" + rollback_id` — the same convention fleet uses (`tr_flj_*`) — so
the trace and record are derivable from each other without a lookup. The rollback
journey is surfaced by `GET /v1/journeys` as a first-class entry:

```bash
curl "http://localhost:1199/v1/journeys?trace_id=tr_rbk_a1b2c3d4"
```

The compensating tool call itself (dispatched by `RollbackExecutor` via the gateway) also
appears in the audit trail as a normal `tool_execution` event under the same rollback trace ID.

### Duplicate prevention

Only one active (non-terminal) rollback can exist per original event ID. A second
`POST /v1/rollbacks` for the same event returns `409 Conflict`. To re-initiate after a
failure, cancel the existing record first, then create a new one.

---

## 8. Limitations

**Pre-rollback support events:** `scale_deployment` events recorded before rollback
support was deployed have no `pre_state` field and cannot be reversed automatically.
The derived plan returns `reversibility: "no"` with an explanation.

**Restart and connection operations:** `restart_deployment`, `terminate_connection`,
`terminate_idle_connections`, and `cancel_query` are irreversible by design. The
`get_session_info` pre-flight for termination operations already surfaces the rollback
cost estimate before approval, making post-facto reversal unnecessary.

**Tier 1 TOCTOU gap:** there is a brief window between the row-capture SELECT and the
actual DML. In the rare case where another session modifies the same rows in that window,
the rollback SQL may restore incorrect values. This risk is mitigated by the short window
(typically milliseconds for approval-gated operations) and by `REPLICA IDENTITY FULL` +
Tier 2 WAL decode when stronger guarantees are needed.

**Tier 2 WAL decode prerequisites:** requires `wal_level = logical` on the target PostgreSQL
instance and the `REPLICATION` privilege for the connecting user. Neither is configured on
the standard `docker-compose.yaml` test instance (which uses `wal_level = replica`). Enable
it per-target in `infrastructure.json` using `"rollback_mode": "wal_decode"`.

**Blast-radius cap:** both tiers are bounded by the existing `max_rows_affected` policy
(default: 100 rows). Operations that exceed the limit are marked `rollback_mode: none`
at capture time and `reversibility: no` at plan time.

**Fleet rollback execution:** fleet rollbacks are built as new `fleet-runner` jobs and
follow the normal fleet execution path. They do not roll back in-progress jobs — only
completed fleet jobs can be reversed.

**No image rollback:** `restart_deployment` image rollbacks (`kubectl rollout undo`) are
not currently supported. This is scheduled for a future release.

Also see [here](GOVPOSTEVAL.md#the-rollback-problem).

---

## 9. Testing

### Unit tests

| Package | File | Coverage |
|---------|------|---------|
| `internal/audit` | `rollback_test.go` | `DeriveRollbackPlan` for all tools; `RollbackStore` CRUD; inverse SQL generators |
| `agents/database` | `rollback_cap_test.go` | `ReplicaIdentityFull`; `NewWALBracket` slot name; `DetectRollbackCapability` mode override + auto-detect fallback |
| `agents/k8s` | `tools_test.go` | `scale_deployment` captures `PreState`; pre-read failure does not abort the tool call |
| `cmd/auditd` | `rollback_handlers_test.go` | All 5 single-event HTTP handler paths (dry-run, 201, 422, 409, 400, 200 get, 404, cancel OK, cancel 409) |
| `cmd/fleet-runner` | `rollback_test.go` | `BuildRollbackJobDef` steps reversed, canary-last, non-reversible step note, scope filter, name prefix, nil/empty guards; `reverseCanaryOrder` |

### Integration tests (`-tags integration`)

```bash
go test -tags integration -timeout 60s ./agents/database/...
```

| Test | What it verifies |
|------|-----------------|
| `TestDetectRollbackCapability_Integration` | `SHOW wal_level`, `pg_roles`, `pg_class` queries work against real PG; auto-detect yields `row_capture` for stock PG 16 |
| `TestDetectRollbackCapability_Integration_Override` | Mode override takes precedence while still populating `WALLevel` |
| `TestWALBracket_Open_FailsWithoutLogicalWAL` | `Open()` returns a meaningful error on a non-logical instance |

Requires the test PostgreSQL instance:
```bash
docker compose -f testing/docker/docker-compose.yaml up -d --wait
```

### E2E tests (`-tags e2e`)

```bash
go test -tags e2e -timeout 300s ./testing/e2e/...
```

| Test | Requires | What it verifies |
|------|----------|-----------------|
| `TestRollback_PreState_SurvivesStoreRoundTrip` | auditd | `pre_state` field in a synthetic `tool_execution` event survives the store round-trip |
| `TestRollback_DerivePlan_OK` | auditd | `/v1/events/{id}/rollback-plan` returns correct inverse op and replica count |
| `TestRollback_DerivePlan_NotFound` | auditd | 404 for non-existent event |
| `TestRollback_InitiateLifecycle` | auditd | Full lifecycle: 201 → list → get → cancel → persisted status |
| `TestRollback_DryRun` | auditd | Dry-run returns plan; no record created |
| `TestRollback_Duplicate_Returns409` | auditd | Second initiation for same event returns 409 |

### Fault tests

The fault catalog includes `k8s-scale-to-zero`: the postgres StatefulSet is scaled to 0
replicas and the K8s agent is prompted to restore service. The agent is expected to call
`get_pods` (diagnose zero replicas), then `scale_deployment` (restore). This is the K8s-side
analogue of `db-idle-in-transaction` — a mutation-remediation scenario.

```bash
go run ./testing/cmd/faulttest --id k8s-scale-to-zero
```
