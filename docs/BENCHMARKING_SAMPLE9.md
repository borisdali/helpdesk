# aiHelpDesk Sample#9 (Docker): Report Card + New Version of a Playbook (generated with human oversight)

The sample commands presented below complements these two blog post: 

- **[AI SRE Just Got Its First Report Card](https://medium.com/@borisdali/your-ai-sre-just-got-its-first-report-card-d46c47b06fd5)**. 
  Turn Your Incident Audit Trail into a Learning Curve

- **[Your AI SRE Tried to Improve Its Own Playbook. Here's What It Got Wrong.](...)**
  A case study in the limits of automated playbook tuning. And what human judgment still has to add.

It all starts with the [Vault](VAULT.md). If you need a background, start there. Next, head over to [this page](VAULT_METRICS.md) to see how aiHelpDesk turns your [Incident](INCIDENTS.md) data into a learning signal.

For more context, aiHelpDesk Fault Injection Testing is well documented [here](FAULTTEST.md), with multiple [examples availble](FAULTTEST_SAMPLE.md) on [K8s](BENCHMARKING_SAMPLE5.md), [Docker/Podman](BENCHMARKING_SAMPLE6.md) and on a [host/VM](BENCHMARKING_SAMPLE7.md). 

---

The sample commands posted below are broken into two parts and are shown for running aiHelpDesk on Docker, but similar samples of running aiHelpDesk on K8s and on a host/VM are available [here](BENCHMARKING_SAMPLE8.md) and [here](BENCHMARKING_SAMPLE7.md) respectively (although not the exact commands shown on this page).

Part 1 is just the normal, previously documented workflow of reviewing the inventory (failure scenarios, content of the vault) and running a fault injection test.

Part 2 is different. The diagnosis for a particular test we chose and the way we decided to run `faulttest` (with `--approval-mode force`), is not confident, which is caught by the judge. So what now? That's Part 2!

## Part 1-a: Inventory: Failure Scenarios + Vault

Similar to the [previous Docker sample](BENCHMARKING_SAMPLE6.md#lets-get-started), first off fire up aiHelpDesk on Docker and optionally run `helpdesk-client` to verify the deployment. Ask some basic questions, e.g. "which of my dev databases are up, what's their uptime and average load?"

Next, get the inventory of the existing failure scenarios (there are presently 17 external faults and 34 total internal ones):

```
[boris@ /tmp/helpdesk/helpdesk-v0.19.0-deploy/docker-compose]$ docker run --rm \
    --network helpdesk_default \
    -v "$HOME/.faulttest:/root/.faulttest" \
    ghcr.io/borisdali/helpdesk:v0.19.0 \
    faulttest list \
      --gateway http://gateway:8080 \
      --api-key $HELPDESK_CLIENT_API_KEY

ID                             CATEGORY     SEVERITY   EXTERNAL DB      SOURCE   NAME
-----------------------------------------------------------------------------------------------------------
db-max-connections             database     high       yes      auto    builtin  Max connections exhausted
db-long-running-query          database     high       yes      auto    builtin  Long-running query blocking
db-lock-contention             database     high       yes      auto    builtin  Lock contention / deadlock
db-table-bloat                 database     medium     yes      auto    builtin  Table bloat / dead tuples
db-high-cache-miss             database     medium     yes      auto    builtin  High cache miss ratio
db-idle-in-transaction         database     high       yes      auto    builtin  Session stuck with uncommitted writes
db-tx-lock-chain-blocker       database     high       yes      auto    builtin  Transaction lock chain — active root blocker (pg_sleep trap)
db-terminate-direct-command    database     high       yes      auto    builtin  Direct terminate — inspect-first check
db-vacuum-needed               database     medium     yes      auto    builtin  Tables needing vacuum (dead tuple bloat)
db-disk-pressure               database     medium     yes      auto    builtin  Disk usage — large table growth
db-pg-hba-corrupt              database     critical   yes      byo     builtin  pg_hba.conf corrupted — all connections rejected
db-process-kill                database     critical   yes      byo     builtin  PostgreSQL postmaster killed (SIGKILL)
db-config-bad-param            database     high       yes      byo     builtin  postgresql.conf invalid parameter
db-wal-disk-full               host         critical   yes      byo     builtin  WAL disk full — writes failing
db-wal-disk-full-k8s           kubernetes   critical   yes      byo     builtin  WAL disk full — writes failing (Kubernetes)
db-wal-stale-slot              database     high       yes      auto    builtin  WAL accumulation — stale replication slot
db-checkpoint-warning          database     medium     yes      auto    builtin  Checkpoint warnings — bgwriter overload

Total: 17 failure modes
```

Next, review the content of the vault for these failure scenarios:

```
[boris@ /tmp/helpdesk/helpdesk-v0.19.0-deploy/docker-compose]$ docker run --rm \
    --network helpdesk_default \
    -v "$HOME/.faulttest:/root/.faulttest" \
    ghcr.io/borisdali/helpdesk:v0.19.0 \
    faulttest vault list \
      --gateway http://gateway:8080 \
      --api-key $HELPDESK_CLIENT_API_KEY

Gateway: http://gateway:8080  ·  version: v0.19.0-6278bb2  ·  host: 41c1c6120572

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-max-connections               any        pbs_connection_triage           pbs_connection_remediate         2026-07-01  PASS       STABLE(3)      12 runs  67% resolved  100% accurate  avg: 2.2 steps, 22s recovery  (generated)
    → vault versions pbs_connection_triage
db-long-running-query            any        pbs_slow_query_triage           pbs_slow_query_remediate         2026-06-22  PASS       STABLE(5)      2 runs  100% resolved  –  avg: 4.0 steps, 10s recovery  (system)
    → vault versions pbs_slow_query_triage
db-lock-contention               any        pbs_lock_contention_triage      pbs_slow_query_remediate         2026-06-23  PASS       STABLE(5)      2 runs  100% resolved  –  avg: 4.0 steps, 10s recovery  (system)
    → vault versions pbs_lock_contention_triage
db-table-bloat                   any        pbs_vacuum_triage               pbs_vacuum_remediate             2026-06-22  PASS       STABLE(5)      10 runs  90% resolved  100% accurate  avg: 7.4 steps, 10m44s recovery  (system)
    1.4   *  9r  0%  avg: 1m26s recovery  100% approach OK
    1.3      12r  0%  avg: 17s recovery
db-high-cache-miss               any        pbs_cache_miss_triage           pbs_cache_miss_remediate         2026-07-04  PASS       STABLE(5)      1 runs  100% resolved  –  avg: 4.0 steps, 11s recovery  (system)
    → vault versions pbs_cache_miss_triage
db-idle-in-transaction           any        pbs_connection_triage           pbs_connection_remediate         2026-06-22  PASS       STABLE(5)      12 runs  67% resolved  100% accurate  avg: 2.2 steps, 22s recovery  (generated)
    → vault versions pbs_connection_triage
db-tx-lock-chain-blocker         any        pbs_lock_chain_triage           pbs_lock_chain_remediate         2026-07-01  FAIL       STABLE(5)      3 runs  33% resolved  100% accurate  avg: 19.3 steps, 1m13s recovery  (system)
    → vault versions pbs_lock_chain_triage
db-terminate-direct-command      any        -                               (none)                           NO PLAYBOOK            STABLE(5)      -
db-vacuum-needed                 any        pbs_vacuum_triage               pbs_vacuum_remediate             2026-07-02  PASS       STABLE(5)      10 runs  90% resolved  100% accurate  avg: 7.4 steps, 10m44s recovery  (system)
    1.4   *  9r  0%  avg: 1m26s recovery  100% approach OK
    1.3      12r  0%  avg: 17s recovery
db-disk-pressure                 any        pbs_disk_pressure_triage        (none)                           2026-06-22  PASS       STABLE(5)      -
    → vault versions pbs_disk_pressure_triage
db-pg-hba-corrupt                any        pbs_pg_hba_triage               pbs_db_config_recovery           2026-06-22  FAIL       UNSTABLE(1)    0 runs  (system)
db-process-kill                  any        pbs_db_restart_triage           pbs_db_restart_triage            2026-06-22  FAIL       UNSTABLE(1)    1 runs  0% resolved  –  avg: 6s recovery  (system)
    → vault versions pbs_db_restart_triage
db-config-bad-param              any        -                               (none)                           NO PLAYBOOK            UNSTABLE(1)    -
db-wal-disk-full                 docker/vm  -                               (none)                           NO PLAYBOOK            UNSTABLE(1)    -
db-wal-disk-full-k8s             k8s        pbs_k8s_pod_crash_triage        (none)                           2026-06-22  PASS       UNSTABLE(5)    -
    → vault versions pbs_k8s_pod_crash_triage
db-wal-stale-slot                any        pbs_wal_stale_slot              pbs_stale_slot_remediate         2026-06-29  PASS       STABLE(5)      14 runs  79% resolved  100% accurate  avg: 3.4 steps, 24s recovery  (system)
    1.3   *  8r  0%  avg: 2m12s recovery  100% approach OK
    1.2      9r  0%  avg: 3m55s recovery
db-checkpoint-warning            any        pbs_checkpoint_bgwriter_triage  pbs_bgwriter_remediate           2026-06-22  PASS       STABLE(5)      1 runs  100% resolved  –  avg: 6.0 steps, 16s recovery  (system)
    → vault versions pbs_checkpoint_bgwriter_triage
```

Note how some of the failure scenarios are calibrated, have multiple versions of the playbooks (just the triage playbooks in release v0.19.0, but remediation playbooks are added in the next version too).

If you prefer to get the more compact view of the vault, without the breakdown by playbook version, add the `--short` flag to the above command:

```
[boris@ /tmp/helpdesk/helpdesk-v0.19.0-deploy/docker-compose]$ docker run --rm \
    --network helpdesk_default \
    -v "$HOME/.faulttest:/root/.faulttest" \
    ghcr.io/borisdali/helpdesk:v0.19.0 \
    faulttest vault list \
      --gateway http://gateway:8080 \
      --api-key $HELPDESK_CLIENT_API_KEY \
      --short

Gateway: http://gateway:8080  ·  version: v0.19.0-6278bb2  ·  host: 41c1c6120572

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-max-connections               any        pbs_connection_triage           pbs_connection_remediate         2026-07-01  PASS       STABLE(3)      12 runs  67% resolved  100% accurate  avg: 2.2 steps, 22s recovery  (generated)
db-long-running-query            any        pbs_slow_query_triage           pbs_slow_query_remediate         2026-06-22  PASS       STABLE(5)      2 runs  100% resolved  –  avg: 4.0 steps, 10s recovery  (system)
db-lock-contention               any        pbs_lock_contention_triage      pbs_slow_query_remediate         2026-06-23  PASS       STABLE(5)      2 runs  100% resolved  –  avg: 4.0 steps, 10s recovery  (system)
db-table-bloat                   any        pbs_vacuum_triage               pbs_vacuum_remediate             2026-06-22  PASS       STABLE(5)      10 runs  90% resolved  100% accurate  avg: 7.4 steps, 10m44s recovery  (system)
db-high-cache-miss               any        pbs_cache_miss_triage           pbs_cache_miss_remediate         2026-07-04  PASS       STABLE(5)      1 runs  100% resolved  –  avg: 4.0 steps, 11s recovery  (system)
db-idle-in-transaction           any        pbs_connection_triage           pbs_connection_remediate         2026-06-22  PASS       STABLE(5)      12 runs  67% resolved  100% accurate  avg: 2.2 steps, 22s recovery  (generated)
db-tx-lock-chain-blocker         any        pbs_lock_chain_triage           pbs_lock_chain_remediate         2026-07-01  FAIL       STABLE(5)      3 runs  33% resolved  100% accurate  avg: 19.3 steps, 1m13s recovery  (system)
db-terminate-direct-command      any        -                               (none)                           NO PLAYBOOK            STABLE(5)      -
db-vacuum-needed                 any        pbs_vacuum_triage               pbs_vacuum_remediate             2026-07-02  PASS       STABLE(5)      10 runs  90% resolved  100% accurate  avg: 7.4 steps, 10m44s recovery  (system)
db-disk-pressure                 any        pbs_disk_pressure_triage        (none)                           2026-06-22  PASS       STABLE(5)      -
db-pg-hba-corrupt                any        pbs_pg_hba_triage               pbs_db_config_recovery           2026-06-22  FAIL       UNSTABLE(1)    0 runs  (system)
db-process-kill                  any        pbs_db_restart_triage           pbs_db_restart_triage            2026-06-22  FAIL       UNSTABLE(1)    1 runs  0% resolved  –  avg: 6s recovery  (system)
db-config-bad-param              any        -                               (none)                           NO PLAYBOOK            UNSTABLE(1)    -
db-wal-disk-full                 docker/vm  -                               (none)                           NO PLAYBOOK            UNSTABLE(1)    -
db-wal-disk-full-k8s             k8s        pbs_k8s_pod_crash_triage        (none)                           2026-06-22  PASS       UNSTABLE(5)    -
db-wal-stale-slot                any        pbs_wal_stale_slot              pbs_stale_slot_remediate         2026-06-29  PASS       STABLE(5)      14 runs  79% resolved  100% accurate  avg: 3.4 steps, 24s recovery  (system)
db-checkpoint-warning            any        pbs_checkpoint_bgwriter_triage  pbs_bgwriter_remediate           2026-06-22  PASS       STABLE(5)      1 runs  100% resolved  –  avg: 6.0 steps, 16s recovery  (system)
```

## Part 1-b: Fault Injection Test

Again, very similar to the [previous example](), but please note the `--approval-mode force` clause, which still accepts the optional at-gate feeback from an operator, but doesn't prompt for the post-incident feedback. This is because the assumption with the `forced` run is well calibrated and went through the multiple testing iterations already. The rest is very similar to the previous examples:

```
[boris@ /tmp/helpdesk/helpdesk-v0.19.0-deploy/docker-compose]$ date; time docker run --rm \
>      --network helpdesk_default \
>      -v "$(pwd)/infrastructure.json:/infrastructure.json:ro" \
>      -v "$(pwd):/output" -w /output \
>      -v "$HOME/.faulttest:/root/.faulttest" \
>      -e DEV_DB_PASSWORD \
>      -e PGPASSFILE=/output/.pgpass \
>      -e ANTHROPIC_API_KEY \
>      ghcr.io/borisdali/helpdesk:v0.19.0 \
>      faulttest run \
>        --ids db-max-connections \
>        --conn "alloydb-on-vm" \
>        --infra-config /infrastructure.json \
>        --judge \
>        --judge-vendor anthropic \
>        --judge-model claude-haiku-4-5-20251001 \
>        --judge-api-key $ANTHROPIC_API_KEY \
>        --via-gateway --gateway http://gateway:8080 \
>        --api-key $HELPDESK_CLIENT_API_KEY \
>        --approval-mode force \
>        --report-per-fault \
>        --remediate --remediation-judge --emit-and-wait --gate-escalation

Sat Jul  4 09:54:12 EDT 2026
time=2026-07-04T13:54:12.581Z level=INFO msg=--conn alias=alloydb-on-vm host=host.docker.internal

--- Testing: Max connections exhausted (db-max-connections) ---
time=2026-07-04T13:54:12.587Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-07-04T13:54:12.587Z level=INFO msg="LLM diagnosis judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-07-04T13:54:12.587Z level=INFO msg="LLM remediation judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-07-04T13:54:12.587Z level=INFO msg="injecting failure" id=db-max-connections type=shell_exec mode=external conn=host.docker.internal
time=2026-07-04T13:54:15.789Z level=INFO msg="shell_exec completed" output="Injected: 69 idle connections (1 existing → 70/100)"
time=2026-07-04T13:54:16.314Z level=INFO msg="sending prompt to agent via playbook" failure=db-max-connections series_id=pbs_connection_triage playbook_id=pb_cf2a367b gateway=http://gateway:8080 agent-conn="host=host.docker.internal port=5432 dbname=postgres user=postgres"
time=2026-07-04T13:54:44.013Z level=WARN msg="gateway warning" failure=db-max-connections warning="approval_mode clamped to \"manual\": override to \"force\" requires one of roles [dba_lead oncall_senior] (caller: gateway)"
time=2026-07-04T13:54:46.045Z level=INFO msg="gate pending: operator review required" failure=db-max-connections run_id=plr_e3e9875d escalation_target=""

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Next playbook     : pbs_connection_remediate
  Findings          : Database at 79/100 connections (79% utilization) with 69 idle connections; idle_session_timeout is disabled (set to 0), allowing idle connections to accumulate indefinitely and blocking new connection attempts when capacity is exceeded; enable idle_session_timeout to auto-terminate idle connections.
  Remediation plan  : Idle Connection Pool Exhaustion Recovery
                      Diagnoses and remediates database connection pool exhaustion caused by accumulated idle connections.
Identifies idle sessions waiting on client reads, terminates them safely, and restores available connection capacity.
This playbook is triggered when connection count approaches max_connections limit with most slots held by idle processes.

  Hypotheses        :
    [PRIMARY 95%] Idle connection accumulation due to disabled idle_session_timeout allowing client applications to maintain idle connections indefinitely
    [REJECTED 80%] Connection pool misconfiguration in client applications — Root cause confirmed to be server-side timeout configuration; client behavior is symptom not cause

────────────────────────────────────────────────────────────────
  ⚠  CONFIDENCE WARNING: Primary hypothesis confidence 95% — competing hypothesis at 80%. Uncertain diagnosis: consider step-by-step approval.
────────────────────────────────────────────────────────────────


Gate pending — run_id=plr_e3e9875d
  Resolve at        : POST http://gateway:8080/api/v1/decisions/gate:plr_e3e9875d/resolve
  Body fields:
    resolution        : "approved" | "denied"
    resolved_by       : your email or user ID
    approval_mode     : "auto" | "review" | "manual" (default: playbook setting)
    reason            : optional — free-text operator comment
    verdict_correct : true | false  (triage/at_gate feedback, before remediation runs)
    verdict_notes   : string        (required when verdict_correct=false)

  Remediation feedback (separate call, same gate window):
    POST http://gateway:8080/api/v1/fleet/playbook-runs/plr_e3e9875d/feedback
    {"feedback_type":"remediation","feedback_time":"at_gate","verdict_correct":true,"verdict_notes":"..."}

time=2026-07-04T13:55:01.599Z level=INFO msg="faultlib: gate poll" run_id=plr_e3e9875d outcome=gate_pending
time=2026-07-04T13:55:17.020Z level=INFO msg="faultlib: gate poll" run_id=plr_e3e9875d outcome=gate_pending
```

At this point the failure injection test blocks and waits for a human to review and resolve the gate.

Please note the `CONFIDENCE WARNING` advising that the primary and the competing hypothesis are in the same ballpark of confidence. This safeguard is an important indication that `--approval-mode force` may be premature for this failure scenario and step-by-step review by a human operator is advisable (which should probably result in tightening the playbook instructions further).

For the purpose of this example, let's provide the at-gate feedback and let the remedition proceed. Leave the `faulttest` running, switch to another terminal and provide feedback/approve:

```
[boris@ ~]$ RUN_ID=plr_e3e9875d
[boris@ ~]$ curl -s -X POST http://localhost:8080/api/v1/fleet/playbook-runs/$RUN_ID/feedback \
>     -H "Content-Type: application/json" \
>     -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" \
>     -d '{"feedback_type":"remediation","feedback_time":"at_gate","verdict_correct":true,"verdict_notes":"LGTM, terminate safely"}'
{"run_id":"plr_e3e9875d","feedback_type":"remediation","feedback_time":"at_gate","series_id":"pbs_connection_triage","verdict_correct":true,"verdict_notes":"LGTM, terminate safely","operator":"","submitted_at":"2026-07-04T14:07:11.530200381Z"}

[boris@ ~]$ curl -X POST \
>      -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" \
>      -H "Content-Type: application/json" \
>      -d '{"resolution":"approved","resolved_by":"boris@borisdali.com"}' \
>      http://localhost:8080/api/v1/decisions/gate:$RUN_ID/resolve|jq .
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100    61    0     0  100    61      0      1  0:01:01  0:00:43  0:00:18     0
100  8777    0  8716  100    61    175      1  0:01:01  0:00:49  0:00:12  1992
{
  "agent": "postgres_database_agent",
  "artifacts": [
    {
      "id": "019f2d75-6b3c-7640-aff1-8de6a44259b4",
      "name": "",
      "parts": "I'll begin diagnosis of the connection pool exhaustion issue. Let me start by testing the connection and gathering evidence about the current state.\nI encountered a policy denial. The access is blocked because the purpose isn't specified in the allowed list. However, I note this is a **read-level policy denial**, not a connectivity failure. This indicates the database container itself may be running, but policy is preventing access.\n\nLet me attempt to access the database using the alternate host-side connection string that may have different policy attributes:\nBoth connection strings are blocked by the same policy issue: the request lacks a recognized purpose attribute. The policy explicitly allows purposes like \"diagnostic\", \"remediation\", \"compliance\", \"maintenance\", \"fleet_rollout\", and \"emergency\", but the current request has an empty purpose string.\n\nSince I am operating under an automated triage playbook (as indicated by the message header \"Call check_connection with connection_string=\"), and the policy is blocking all read access to this database, I cannot proceed with any diagnostic queries on this target.\n\nThis is a **policy enforcement barrier**, not a database connectivity or application-level issue. The database container may be running, but access is denied at the authorization layer before any database queries can be executed.\n\nHYPOTHESIS_1: Policy authorization barrier preventing diagnostic access to the database at the tool level | CONFIDENCE: 1.0 | EVIDENCE: \"policy denied: Access to database alloydb-on-vm (tags: development, chaos) for read: DENIED ... purpose=\\\"\\\" is not in the allowed list [diagnostic remediation compliance maintenance fleet_rollout emergency]\"\nHYPOTHESIS_2: Database container is down or unreachable | CONFIDENCE: 0.0 | REJECTED: Policy denial occurs before network/connectivity attempts, indicating the database endpoint is known and reachable by the access control layer\nROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: Access to the target database is blocked by policy enforcement requiring an explicit purpose attribute; diagnostic investigation cannot proceed without policy exemption or re-invocation with proper authorization context.\nESCALATE_TO: none"
    }
  ],
  "chain": [
    {
      "step": 1,
      "playbook_series_id": "pbs_connection_remediate",
      "agent_name": "postgres_database_agent",
      "run_id": "plr_e92fefcf",
      "findings": "Access to the target database is blocked by policy enforcement requiring an explicit purpose attribute; diagnostic investigation cannot proceed without policy exemption or re-invocation with proper authorization context.",
      "text": "I'll begin diagnosis of the connection pool exhaustion issue. Let me start by testing the connection and gathering evidence about the current state.\nI encountered a policy denial. The access is blocked because the purpose isn't specified in the allowed list. However, I note this is a **read-level policy denial**, not a connectivity failure. This indicates the database container itself may be running, but policy is preventing access.\n\nLet me attempt to access the database using the alternate host-side connection string that may have different policy attributes:\nBoth connection strings are blocked by the same policy issue: the request lacks a recognized purpose attribute. The policy explicitly allows purposes like \"diagnostic\", \"remediation\", \"compliance\", \"maintenance\", \"fleet_rollout\", and \"emergency\", but the current request has an empty purpose string.\n\nSince I am operating under an automated triage playbook (as indicated by the message header \"Call check_connection with connection_string=\"), and the policy is blocking all read access to this database, I cannot proceed with any diagnostic queries on this target.\n\nThis is a **policy enforcement barrier**, not a database connectivity or application-level issue. The database container may be running, but access is denied at the authorization layer before any database queries can be executed.\n\nHYPOTHESIS_1: Policy authorization barrier preventing diagnostic access to the database at the tool level | CONFIDENCE: 1.0 | EVIDENCE: \"policy denied: Access to database alloydb-on-vm (tags: development, chaos) for read: DENIED ... purpose=\\\"\\\" is not in the allowed list [diagnostic remediation compliance maintenance fleet_rollout emergency]\"\nHYPOTHESIS_2: Database container is down or unreachable | CONFIDENCE: 0.0 | REJECTED: Policy denial occurs before network/connectivity attempts, indicating the database endpoint is known and reachable by the access control layer\nROOT_CAUSE: HYPOTHESIS_1",
      "diagnostic_report": {
        "hypotheses": [
          {
            "rank": 1,
            "text": "Policy authorization barrier preventing diagnostic access to the database at the tool level",
            "confidence": 1,
            "evidence": "policy denied: Access to database alloydb-on-vm (tags: development, chaos) for read: DENIED ... purpose=\\\"\\\" is not in the allowed list [diagnostic remediation compliance maintenance fleet_rollout emergency]",
            "is_primary": true
          },
          {
            "rank": 2,
            "text": "Database container is down or unreachable",
            "confidence": 0,
            "rejected_reason": "Policy denial occurs before network/connectivity attempts, indicating the database endpoint is known and reachable by the access control layer",
            "is_primary": false
          }
        ],
        "root_cause": "Policy authorization barrier preventing diagnostic access to the database at the tool level"
      }
    }
  ],
  "context_id": "019f2d74-cf69-7f06-8407-36c158012cc4",
  "diagnostic_report": {
    "hypotheses": [
      {
        "rank": 1,
        "text": "Policy authorization barrier preventing diagnostic access to the database at the tool level",
        "confidence": 1,
        "evidence": "policy denied: Access to database alloydb-on-vm (tags: development, chaos) for read: DENIED ... purpose=\\\"\\\" is not in the allowed list [diagnostic remediation compliance maintenance fleet_rollout emergency]",
        "is_primary": true
      },
      {
        "rank": 2,
        "text": "Database container is down or unreachable",
        "confidence": 0,
        "rejected_reason": "Policy denial occurs before network/connectivity attempts, indicating the database endpoint is known and reachable by the access control layer",
        "is_primary": false
      }
    ],
    "root_cause": "Policy authorization barrier preventing diagnostic access to the database at the tool level"
  },
  "findings": "Access to the target database is blocked by policy enforcement requiring an explicit purpose attribute; diagnostic investigation cannot proceed without policy exemption or re-invocation with proper authorization context.",
  "run_id": "plr_e92fefcf",
  "state": "completed",
  "task_id": "019f2d74-cf69-7d97-b851-f40ab2d723c5",
  "text": "I'll begin diagnosis of the connection pool exhaustion issue. Let me start by testing the connection and gathering evidence about the current state.\nI encountered a policy denial. The access is blocked because the purpose isn't specified in the allowed list. However, I note this is a **read-level policy denial**, not a connectivity failure. This indicates the database container itself may be running, but policy is preventing access.\n\nLet me attempt to access the database using the alternate host-side connection string that may have different policy attributes:\nBoth connection strings are blocked by the same policy issue: the request lacks a recognized purpose attribute. The policy explicitly allows purposes like \"diagnostic\", \"remediation\", \"compliance\", \"maintenance\", \"fleet_rollout\", and \"emergency\", but the current request has an empty purpose string.\n\nSince I am operating under an automated triage playbook (as indicated by the message header \"Call check_connection with connection_string=\"), and the policy is blocking all read access to this database, I cannot proceed with any diagnostic queries on this target.\n\nThis is a **policy enforcement barrier**, not a database connectivity or application-level issue. The database container may be running, but access is denied at the authorization layer before any database queries can be executed.\n\nHYPOTHESIS_1: Policy authorization barrier preventing diagnostic access to the database at the tool level | CONFIDENCE: 1.0 | EVIDENCE: \"policy denied: Access to database alloydb-on-vm (tags: development, chaos) for read: DENIED ... purpose=\\\"\\\" is not in the allowed list [diagnostic remediation compliance maintenance fleet_rollout emergency]\"\nHYPOTHESIS_2: Database container is down or unreachable | CONFIDENCE: 0.0 | REJECTED: Policy denial occurs before network/connectivity attempts, indicating the database endpoint is known and reachable by the access control layer\nROOT_CAUSE: HYPOTHESIS_1",
  "tool_calls": [
    "check_connection",
    "check_connection"
  ]
}
```

Back to the original terminal running the fault injection test, we see that it's now unblocked and proceeds with the remediation:

```
time=2026-07-04T14:07:19.524Z level=INFO msg="faultlib: gate poll" run_id=plr_e3e9875d outcome=gate_pending
time=2026-07-04T14:07:34.621Z level=INFO msg="faultlib: gate poll" run_id=plr_e3e9875d outcome=gate_pending
time=2026-07-04T14:07:50.229Z level=INFO msg="faultlib: gate poll" run_id=plr_e3e9875d outcome=transitioned
time=2026-07-04T14:08:04.474Z level=INFO msg="gate resolved externally" status=transitioned

  Feedback pending — resolve at:
  POST http://gateway:8080/api/v1/decisions/feedback:plr_e3e9875d/resolve
  Body fields:
    resolution  : "approved" (diagnosis correct) | "denied" (diagnosis wrong)
    resolved_by : your email or user ID
    reason      : actual root cause (required when resolution="denied")

  Waiting for feedback (10m timeout, Ctrl+C to skip)...
  Feedback received — thank you.

Remediation: RECOVERED in 0.0s (score: 100%)
Incident plr_e3e9875d — resolved in 0.0s
  Diagnosis  : Idle connection accumulation due to disabled idle_session_timeout allowing client applications to maintain idle connections indefinitely
  Remediation: pbs_connection_remediate
  Narrative  : GET http://gateway:8080/api/v1/incidents/plr_e3e9875d
Vault: draft saved → pb_517e6030 (activate with 'faulttest vault list')
  Feedback submitted (triage/post_incident run_id=plr_e3e9875d)
time=2026-07-04T14:08:39.193Z level=INFO msg="tearing down failure" id=db-max-connections type=shell_exec conn=host.docker.internal
Diagnostic Result:   [PASS] score=86% (keywords=100% tools=100% judge=67%)
Remediation Result:  [PASS] score=100% (0.0s, playbook)
Overall Result:      [PASS] score=92%
time=2026-07-04T14:08:41.224Z level=INFO msg="shell_exec completed" output="Teardown: idle connections terminated"
Fault report: ./faulttest-30bd6d13-db-max-connections.json

=== Fault Test Report: 30bd6d13 ===

[PASS] Max connections exhausted (db-max-connections) - score: 86% [judge: 67%]
       Keywords: 100% | Tools: 100% | Judge: 67%
       Remediation: 100% (0.0s, playbook) | Overall: 92%
       Reasoning: "Agent correctly identified idle connection accumulation as root cause but missed the key expected recommendation to either terminate idle connections or increase max_connections, focusing instead on timeout configuration as the primary issue."

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

LLM judge scored diagnosis for 1 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-30bd6d13.json
```

As mentioned earlier, at the end of the `faulttest`, there was no prompt for the post-incident feedback due to the requested `--approval-mode force` mode.

Overall, there wasn't anything new in the above compared to what we already presented in the previous samples. But here's where the fun begins:

## Part 1-c: Review `vault versions` for `pbs_connection_triage` playbook

```
[boris@ /tmp/helpdesk/helpdesk-v0.19.0-deploy/docker-compose]$ docker run --rm \
>     --network helpdesk_default \
>     -v "$HOME/.faulttest:/root/.faulttest" \
>     ghcr.io/borisdali/helpdesk:v0.19.0 \
>     faulttest vault versions \
>       --gateway http://gateway:8080 \
>       --api-key $HELPDESK_CLIENT_API_KEY \
>       pbs_connection_triage

Gateway: http://gateway:8080  ·  version: v0.19.0-6278bb2  ·  host: 41c1c6120572

Version stats for pbs_connection_triage

VERSION     RUNS    SUCCESS%   AVG STEPS   AVG TIME    AVG DIAG   AVG REMED  APPROACH OK
──────────────────────────────────────────────────────────────────────────────────────
1.3 *       20      75%        –           1m15s       89%        100%       100%
  id=pb_cf2a367b

* = currently active   SUCCESS% = resolved + transitioned
id/from lines show playbook_id and the run that generated that version
```


## Part 2-a: The Problem and the Two Options

So here's the problem: the triage of this `max connections` fault results in the diagnosis with multiple competing hypotheses, as we saw from the failure injection test earlier. aiHelpDesk flagged this clearly at the [Informed Gate](PLAYBOOKS.md#informed-gate), which is the implementation of aiHelpDesk [Informed Consent](INFORMED_CONSENT.md) policy.

```
  ────────────────────────────────────────────────────────────────
    ⚠  CONFIDENCE WARNING: Primary hypothesis confidence 95% — competing hypothesis at 80%. Uncertain diagnosis: consider step-by-step approval.
  ────────────────────────────────────────────────────────────────
```

So we clearly need to fix this or it would result to escalations to a human operator every time. 
OK, our workflow for these cases is this:

```
        find the playbook source →
                edit it to fix the diagnostic gap →
                        bump version to 1.4 →
                                seed it into the running auditd →
                                        run faulttest again →
                                                vault diff
```

The guidance in the current playbook v1.3 [already says](https://github.com/borisdali/helpdesk/blob/main/playbooks/connection-triage.yaml#L57) `kill_idle` is the required FINDINGS recommendation, but the judge caught that the agent was framing timeout configuration as the primary fix. That's the problem. We have two paths to fix this: 

Path A: manual edit + import (precise control):
Edit the YAML to strengthen the guidance, then push it via the gateway API.
The gap to fix is clear: the agent recommended `idle_session_timeout` configuration as the primary fix instead of `kill_idle`.
The guidance already says this is wrong, but evidently it's not explicit enough for the agent.

Path B is faster, but less controlled: run `vault suggest-update` and rely on the LLM to generate v1.4 from run history:

```
  docker run --rm \
    --network helpdesk_default \
    -v "$HOME/.faulttest:/root/.faulttest" \
    ghcr.io/borisdali/helpdesk:v0.19.0 \
    faulttest vault suggest-update pbs_connection_triage \
      --gateway http://gateway:8080 \
      --api-key $HELPDESK_CLIENT_API_KEY
```

This would generate a draft with an LLM-proposed improvement. You review it, then activate with `vault activate <draft-id>`.

The real question here however isn't "which command do I run?", but rather "what does each approach teach me about my agent's failure mode?".

The core tradeoff:

`suggest-update` (Path B) is the discovery path: you're asking the LLM to read the run history and infer what the playbook is doing wrong. It can catch gaps you didn't notice.
But it's a black box: the LLM might fix one thing while quietly softening something else and the diff will be large and hard to audit.

On the other hand, the manual edit (Path A) is the diagnosis path. You've already read the judge's reasoning, you understand exactly why the agent failed (it correctly identified idle connection accumulation, but narrated the configuration fix as the headline instead of `kill_idle`) and you make one surgical change. The diff is a single sentence and every reader knows why it's there.

Here's another way to think about these two options and the decision rule:

```
  ┌──────────────────────────────────────────────────┬────────────────────────────────────────────────────────────────┐
  │                    Situation                     │                              Use                               │
  ├──────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────┤
  │ Judge feedback clearly names the gap             │ Manual edit — don't add noise                                  │
  ├──────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────┤
  │ Judge score is low but you're not sure why       │ `suggest-update` first                                           │
  ├──────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────┤
  │ Multiple runs failing in different ways          │ `suggest-update` — breadth wins                                  │
  ├──────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────┤
  │ You're building a new playbook from scratch      │ `suggest-update` bootstraps, manual refines                      │
  ├──────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────┤
  │ You have a v1.3 → v1.4 regression to investigate │ Manual — you need a precise diff for vault diff to be readable │
  └──────────────────────────────────────────────────┴────────────────────────────────────────────────────────────────┘
```

The ordering here matters. Let's run `suggest-update` first so that you don't bias yourself, then write the manual edit independently, then compare what each produced.
If they converge on the same change, that's a strong signal the fix is right. If they diverge, that's the most interesting part — what the human saw that the LLM missed or vice versa.

The meta-story here is this: you're using an AI judge to grade your AI SRE, then using a second AI to fix the first AI's playbook, while a human watches to see if any of them got it right.

## Part 2-b: Option B of `suggest-update`

In the 0.19 release we made `suggest-update` smart enough to not only make use of `--series-id` (not a positional arg), but also auto-pick the most recent resolved run. So here it goes:

```
[boris@ /tmp/helpdesk/helpdesk-v0.19.0-deploy/docker-compose]$ date; time docker run --rm \
>     --network helpdesk_default \
>     -v "$HOME/.faulttest:/root/.faulttest" \
>     -e ANTHROPIC_API_KEY \
>     ghcr.io/borisdali/helpdesk:v0.19.0 \
>     faulttest vault suggest-update \
>       --series-id pbs_connection_triage \
>       --trace-id plr_e3e9875d \
>       --gateway http://gateway:8080 \
>       --api-key $HELPDESK_CLIENT_API_KEY

Sat Jul  4 17:08:14 EDT 2026
Gateway: http://gateway:8080  ·  version: v0.19.0-6278bb2  ·  host: 41c1c6120572

Resolved plr_e3e9875d → faulttest-30bd6d13-db-max-connections (triage journey)

=== Playbook Update Proposal: pbs_connection_triage ===

Current:  pb_cf2a367b — Connection & Lock Triage
Trace:    faulttest-30bd6d13-db-max-connections (outcome: resolved)

--- CURRENT DESCRIPTION ---
Investigate high connection counts, connection pool saturation, or lock
contention that may be causing availability degradation. Identify idle
connections, blocking transactions, and sessions holding long-running locks.


--- CURRENT GUIDANCE ---
Start with get_server_info to check active_connections vs max_connections.
If active_connections > 80% of max_connections, the system is at risk of
connection exhaustion.

Use get_blocking_queries to surface any blocking chains. A single idle
transaction holding a lock can block dozens of application requests. Pay
attention to wait_duration — any lock held > 5 minutes is a candidate for
investigation.

Use get_session_info (if available) on sessions with long durations to
determine if they have uncommitted writes (has_writes=true). These are the
most dangerous to terminate and should be escalated before action.

Use get_lock_info to see the full lock table. Filter for AccessExclusiveLock —
these block even reads and are the most disruptive.

Common misdiagnosis: blaming connection count when the real issue is a single
long-running transaction preventing autovacuum from reclaiming connection slots
(wraparound risk). Check transaction age alongside connection count.

Required output — write these exact lines at the end of your response,
no markdown, no extra text on any line. The handoff signal (TRANSITION_TO
or ESCALATE_TO) is mandatory: omitting it stalls the operator review gate:
HYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: "<verbatim quote from tool output>"
HYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>
ROOT_CAUSE: HYPOTHESIS_1
FINDINGS: connections <active>/<max> (<pct>%); blocker=<PID <pid> (<state>, <duration>, has_writes=<true|false>) | none>; recommended=<terminate_blocker|kill_idle|escalate|monitor>
TRANSITION_TO: pbs_connection_remediate


--- PROPOSED DRAFT (from trace) ---
name: Connection & Lock Triage
description: |
  Investigate high connection counts, connection pool saturation, or lock
  contention that may be causing availability degradation. Identify idle
  connections, blocking transactions, and sessions holding long-running locks.

  This playbook also addresses connection leaks caused by idle sessions that
  do not participate in lock conflicts but still consume connection slots.
problem_class: availability
symptoms:
  - connection pool exhausted
  - FATAL: remaining connection slots are reserved
  - application cannot connect to database
  - active_connections / max_connections > 0.8
  - long-running transactions visible in monitoring
  - high proportion of idle connections relative to active (e.g., >80% idle)
guidance: |
  Start with get_server_info to check total_connections vs max_connections,
  and active_connections to understand the ratio. If total_connections > 80%
  of max_connections, the system is at risk of connection exhaustion.

  Use get_connection_stats to see idle, idle_in_transaction, and waiting_on_lock
  breakdown per database. Pay attention to idle_in_transaction sessions — these
  hold transaction snapshots and prevent autovacuum from advancing and may block
  other operations. Also note any sessions waiting on locks.

  Use get_active_connections to list all sessions ordered by query_start (oldest
  first). Look for sessions in "idle" state that have been running for hours or
  days with no activity — these are prime candidates for termination. Cross-check
  client_addr to identify connection pools or stalled clients.

  Use get_blocking_queries to surface any blocking chains. A single idle
  transaction holding a lock can block dozens of application requests. Pay
  attention to blocked_duration — any lock held > 5 minutes is a candidate for
  investigation. If no rows are returned, there is no active lock contention;
  the problem is purely connection count, not deadlock.

  Use get_lock_info to see the full lock table. Filter for AccessExclusiveLock —
  these block even reads and are the most disruptive. Again, if no rows are
  returned, lock contention is not the issue.

  Check idle_session_timeout and idle_in_transaction_session_timeout settings
  (via get_config_parameter). If both are set to 0 (disabled), idle connections
  will accumulate indefinitely. A setting like idle_session_timeout=86400000ms
  (24 hours) may be too permissive if connection pool size is limited; consider
  lowering it to 5–10 minutes (300000–600000 ms) for aggressive cleanup.

  Common misdiagnosis 1: blaming connection count when the real issue is a single
  long-running transaction preventing autovacuum from reclaiming connection slots
  (wraparound risk). Check transaction age alongside connection count.

  Common misdiagnosis 2: assuming all idle connections are harmless. In fact,
  idle sessions that are not in a transaction (state='idle', idle_in_transaction
  count = 0) still consume connection slots. If max_connections is small (e.g.,
  100) and a connection pool leaves connections open for reuse, even brief bursts
  can exhaust slots. This is a connection pool configuration issue, not a database
  lock issue, and requires either raising max_connections or implementing connection
  pooling middleware (e.g., pgbouncer).

  REMEDIATION DECISION TREE:
  - If no blocking queries found AND no locks held:
    → Problem is connection pool saturation or client leak. Proceed to kill_idle.
  - If blocking queries found AND blocked_duration > 5 minutes:
    → Problem is lock contention. Proceed to terminate_blocker.
  - If idle_in_transaction > 30% of total_connections:
    → Problem is stalled transaction snapshots. Proceed to kill_idle_transactions.
  - If active_connections >= max_connections (no slots remaining):
    → Escalate immediately; no safe action possible without risk of cascading failures.

  Required output — write these exact lines at the end of your response,
  no markdown, no extra text on any line. The handoff signal (TRANSITION_TO
  or ESCALATE_TO) is mandatory; omitting it stalls the operator review gate:
  HYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: "<verbatim quote from tool output>"
  HYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>
  ROOT_CAUSE: HYPOTHESIS_1
  FINDINGS: connections <active>/<max> (<pct>%); idle=<idle_count> idle_in_transaction=<count>; blocker=<PID <pid> (<state>, <duration>, has_writes=<true|false>) | none>; recommended=<terminate_blocker|kill_idle|kill_idle_transactions|escalate|monitor>
  TRANSITION_TO: pbs_connection_remediate
escalation:
  - active_connections >= max_connections (no slots remaining)
  - total_connections >= max_connections (no slots remaining)
  - blocking chain involves a transaction open > 30 minutes
  - session with uncommitted writes (has_writes=true) open > 10 minutes
  - idle_in_transaction > 50% of total_connections AND connection usage > 80%

Proposed draft saved as: pb_d6688d31 (inactive, source=generated)

# To activate the proposed draft:
#   curl -X POST http://gateway:8080/api/v1/fleet/playbooks/pb_d6688d31/activate \
#        -H 'Authorization: Bearer <key>'

real	0m20.664s
user	0m0.022s
sys	0m0.019s
```

## Part 2-c: Option B Analysis

`vault suggest-update` produced rich output. Let's review it in detail:

What the LLM added (genuine improvements):

- REMEDIATION DECISION TREE: excellent, this is exactly the kind of structured reasoning the agent needs.
- idle=<count> idle_in_transaction=<count> added to the FINDINGS format: sure, it makes the structured output richer.
- New symptom: "high proportion of idle connections relative to active"
- More nuanced escalation conditions

All good stuff. What the LLM got wrong:

It removed the most important paragraph from v1.3:

>  When idle connections are the primary cause... your immediate remediation recommendation MUST be terminate_idle_connections — encoded as kill_idle in the FINDINGS line. Application-level changes (pool size reduction, idle timeout
>  configuration) are the correct long-term fix but must NOT be listed as the primary recommended action...

And replaced it with this:

>  Check idle_session_timeout... If both are set to 0 (disabled), idle connections will accumulate indefinitely... consider lowering it to 5–10 minutes for aggressive cleanup.

That's the exact failure mode the judge flagged. The LLM reinforced the wrong behavior instead of correcting it. If you activated pb_d6688d31 "as-is" and ran `faulttest` again, the judge score would likely drop, not improve.

## Part 2-d: The solution: combine paths A and B

The manual edit is now clearly the right path for this specific fix. The LLM draft is useful as structural scaffolding (keep the decision tree, keep the extended FINDINGS format, keep the new symptom) but the guidance paragraph needs the
  human's targeted fix. And so the ideal v1.4 is a merge:

- 1/ Take the LLM's structural additions (decision tree, FINDINGS format, symptom).
- 2/ Restore the v1.3 `kill_idle` mandate paragraph.
- 3/ Add one sentence making explicit what the LLM missed: finding `idle_session_timeout=0` is the root cause explanation, not the immediate action.
- 4/ Drop the LLM's paragraph that validates recommending timeout configuration as a fix.

Let's merge it now (in this example I'm just showing the `git diff` directly from the code source):

```
[boris@ ~/helpdesk]$ git diff playbooks/connection-triage.yaml
diff --git a/playbooks/connection-triage.yaml b/playbooks/connection-triage.yaml
index ade9766..e0110d1 100644
--- a/playbooks/connection-triage.yaml
+++ b/playbooks/connection-triage.yaml
@@ -1,6 +1,6 @@
 series_id: pbs_connection_triage
 name: Connection & Lock Triage
-version: "1.3"
+version: "1.4"
 playbook_type: triage
 entry_point: true
 execution_mode: agent
@@ -18,6 +18,7 @@ symptoms:
   - "application cannot connect to database"
   - "active_connections / max_connections > 0.8"
   - "long-running transactions visible in monitoring"
+  - "high proportion of idle connections relative to active (>80% idle)"
 guidance: |
   Start with get_server_info to check active_connections vs max_connections.
   If active_connections > 80% of max_connections, the system is at risk of
@@ -44,17 +45,34 @@ guidance: |
   "escalate" only for sessions with has_writes=true that are too old to terminate
   safely without risking data loss.

-  Common misdiagnosis: blaming connection count when the real issue is a single
+  CRITICAL — idle_session_timeout=0 trap: finding that idle_session_timeout is
+  disabled (set to 0) explains WHY idle connections accumulated. It is the root
+  cause explanation, not the immediate action. Do NOT recommend configuring
+  idle_session_timeout as your primary fix. The immediate recommendation is still
+  kill_idle (terminate the idle connections now). Mention the timeout configuration
+  only as a secondary long-term preventive measure, after kill_idle.
+
+  Remediation decision guide:
+  - No blocking queries AND no locks held → kill_idle (connection saturation)
+  - Blocking queries found AND blocked_duration > 5 min → terminate_blocker
+  - active_connections >= max_connections (no free slots) → escalate immediately
+  - Sessions with has_writes=true open > 10 min → escalate before any termination
+
+  Common misdiagnosis 1: blaming connection count when the real issue is a single
   long-running transaction preventing autovacuum from reclaiming connection slots
   (wraparound risk). Check transaction age alongside connection count.

+  Common misdiagnosis 2: recommending idle_session_timeout configuration as the
+  primary fix when idle connections are present. The timeout change prevents future
+  accumulation but does nothing for the connections already open right now.
+
   Required output — write these exact lines at the end of your response,
   no markdown, no extra text on any line. The handoff signal (TRANSITION_TO
   or ESCALATE_TO) is mandatory: omitting it stalls the operator review gate:
   HYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: "<verbatim quote from tool output>"
   HYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>
   ROOT_CAUSE: HYPOTHESIS_1
-  FINDINGS: connections <active>/<max> (<pct>%); blocker=<PID <pid> (<state>, <duration>, has_writes=<true|false>) | none>; recommended=<terminate_blocker|kill_idle|escalate|monitor>
+  FINDINGS: connections <active>/<max> (<pct>%); idle=<idle_count>; blocker=<PID <pid> (<state>, <duration>, has_writes=<true|false>) | none>; recommended=<terminate_blocker|kill_idle|escalate|monitor>
   TRANSITION_TO: pbs_connection_remediate
 escalation:
   - "active_connections >= max_connections (no slots remaining)"
```

## Part 2-e: Implement the fix

The quickest way here is to push v1.4 into the running auditd directly. The gateway proxies the playbook create endpoint and so all we need to do is to POST the YAML content as a JSON payloadand let the /import endpoint parses YAML directly. It accepts {"text": "<yaml content>", "format": "yaml"} and returns a draft, then you call activate. Let me construct the full push command:

```
[boris@ ~]$ cp <code source>/helpdesk/playbooks/connection-triage.yaml \
>      /tmp/helpdesk/helpdesk-v0.19.0-deploy/docker-compose/connection-triage.yaml

[boris@ ~]$   python3 -c '
>   import json
>   with open("/tmp/helpdesk/helpdesk-v0.19.0-deploy/docker-compose/connection-triage.yaml") as f:
>       content = f.read()
>   payload = {"text": content, "format": "yaml", "hints": {"series_id": "pbs_connection_triage"}}
>   print(json.dumps(payload))
>   ' > /tmp/pb_import.json

[boris@ ~]$   DRAFT=$(curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks/import \
>     -H "Content-Type: application/json" \
>     -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" \
>     --data-binary @/tmp/pb_import.json)

[boris@ ~]$ echo "$DRAFT" | python3 -m json.tool | grep -E "playbook_id|version|warning"
        "playbook_id": "",
        "version": "1.4",
```

The import parsed correctly (version: 1.4) but playbook_id: "" didn't, which means that the /import endpoint just parses without persisting. Need one more step: POST the draft object to save it, then activate:

```
-- Extract the draft object from the import response
[boris@ ~]$ DRAFT_OBJ=$(echo "$DRAFT" | python3 -c "import sys,json; print(json.dumps(json.load(sys.stdin)['draft']))")

-- Save it to get a playbook_id
[boris@ ~]$ SAVED=$(curl -s -X POST http://localhost:8080/api/v1/fleet/playbooks \
>     -H "Content-Type: application/json" \
>     -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" \
>     -d "$DRAFT_OBJ")

[boris@ ~]$ echo "$SAVED" | python3 -m json.tool | grep -E "playbook_id|version|series"
    "playbook_id": "pb_faa4ee0f",
    "version": "1.4",
    "series_id": "pbs_connection_triage",

-- Activate
[boris@ ~]$   DRAFT_ID=$(echo "$SAVED" | python3 -c "import sys,json; print(json.load(sys.stdin)['playbook_id'])")
[boris@ ~]$   echo "Activating $DRAFT_ID ..."
Activating pb_faa4ee0f ...
[boris@ ~]$   curl -s -X POST "http://localhost:8080/api/v1/fleet/playbooks/$DRAFT_ID/activate" -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" | python3 -m json.tool
{
    "playbook_id": "pb_faa4ee0f",
    "name": "Connection & Lock Triage",
    "description": "Investigate high connection counts, connection pool saturation, or lock\ncontention that may be causing availability degradation. Identify idle\nconnections, blocking transactions, and sessions holding long-running locks. \n",
    "created_by": "",
    "created_at": "2026-07-04T22:17:50.733600553Z",
    "updated_at": "2026-07-04T22:17:50.733600553Z",
    "problem_class": "availability",
    "symptoms": [
        "connection pool exhausted",
        "FATAL: remaining connection slots are reserved",
        "application cannot connect to database",
        "active_connections / max_connections > 0.8",
        "long-running transactions visible in monitoring",
        "high proportion of idle connections relative to active (>80% idle)"
    ],
    "guidance": "Start with get_server_info to check active_connections vs max_connections.\nIf active_connections > 80% of max_connections, the system is at risk of\nconnection exhaustion.\n\nUse get_blocking_queries to surface any blocking chains. A single idle\ntransaction holding a lock can block dozens of application requests. Pay\nattention to wait_duration \u2014 any lock held > 5 minutes is a candidate for\ninvestigation.\n\nUse get_session_info (if available) on         sessions with long durations to\ndetermine if they have uncommitted writes (has_writes=true). These are the\nmost dangerous to terminate and should be escalated before action.\n\nUse get_lock_info to see the full lock table. Filter for       AccessExclusiveLock \u2014\nthese block even reads and are the most disruptive.\n\nWhen idle connections are the primary cause (no blocking transactions, no locked\nqueries, no uncommitted writes), your immediate remediation recommendation   MUST\nbe terminate_idle_connections \u2014 encoded as kill_idle in the FINDINGS line.\nApplication-level changes (pool size reduction, idle timeout configuration) are\nthe correct long-term fix but must NOT be listed as the primary           recommended\naction when idle connections can be terminated directly right now. Reserve\n\"escalate\" only for sessions with has_writes=true that are too old to terminate\nsafely without risking data loss.\n\nCRITICAL \u2014                  idle_session_timeout=0 trap: finding that idle_session_timeout is\ndisabled (set to 0) explains WHY idle connections accumulated. It is the root\ncause explanation, not the immediate action. Do NOT recommend configuring\nidle_session_timeout as your primary fix. The immediate recommendation is still\nkill_idle (terminate the idle connections now). Mention the timeout configuration\nonly as a secondary long-term preventive measure, after kill_idle.\n\nRemediation decision guide:  \n- No blocking queries AND no locks held \u2192 kill_idle (connection saturation)\n- Blocking queries found AND blocked_duration > 5 min \u2192 terminate_blocker\n- active_connections >= max_connections (no free slots) \u2192 escalate       immediately\n- Sessions with has_writes=true open > 10 min \u2192 escalate before any termination\n\nCommon misdiagnosis 1: blaming connection count when the real issue is a single\nlong-running transaction preventing autovacuum from         reclaiming connection slots\n(wraparound risk). Check transaction age alongside connection count.\n\nCommon misdiagnosis 2: recommending idle_session_timeout configuration as the\nprimary fix when idle connections are present. The timeout    change prevents future\naccumulation but does nothing for the connections already open right now.\n\nRequired output \u2014 write these exact lines at the end of your response,\nno markdown, no extra text on any line. The handoff signal      (TRANSITION_TO\nor ESCALATE_TO) is mandatory: omitting it stalls the operator review gate:\nHYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0\u20131.0> | EVIDENCE: \"<verbatim quote from tool output>\"\nHYPOTHESIS_2: <alternative if      considered> | CONFIDENCE: <0.0\u20131.0> | REJECTED: <one-sentence reason why ruled out>\nROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: connections <active>/<max> (<pct>%); idle=<idle_count>; blocker=<PID <pid> (<state>, <duration>,                    has_writes=<true|false>) | none>; recommended=<terminate_blocker|kill_idle|escalate|monitor>\nTRANSITION_TO: pbs_connection_remediate\n",
    "escalation": [
        "active_connections >= max_connections (no slots remaining)",
        "blocking chain involves a transaction open > 30 minutes",
        "session with uncommitted writes (has_writes=true) open > 10 minutes"
    ],
    "author": "aiHelpDesk",
    "version": "1.4",
    "series_id": "pbs_connection_triage",
    "is_active": true,
    "is_system": false,
    "source": "imported",
    "entry_point": true,
    "execution_mode": "agent"
}
```

Now that the v1.4 is in, run another fault injection test with it, the same way above:

```
[boris@ /tmp/helpdesk/helpdesk-v0.19.0-deploy/docker-compose]$ date; time docker run --rm \
>     --network helpdesk_default \
>     -v "$(pwd)/infrastructure.json:/infrastructure.json:ro" \
>     -v "$(pwd):/output" -w /output \
>     -v "$HOME/.faulttest:/root/.faulttest" \
>     -e DEV_DB_PASSWORD \
>     -e PGPASSFILE=/output/.pgpass \
>     -e ANTHROPIC_API_KEY \
>     ghcr.io/borisdali/helpdesk:v0.19.0 \
>     faulttest run \
>       --ids db-max-connections \
>       --conn "alloydb-on-vm" \
>       --infra-config /infrastructure.json \
>       --judge \
>       --judge-vendor anthropic \
>       --judge-model claude-haiku-4-5-20251001 \
>       --judge-api-key $ANTHROPIC_API_KEY \
>       --via-gateway --gateway http://gateway:8080 \
>       --api-key $HELPDESK_CLIENT_API_KEY \
>       --approval-mode force \
>       --report-per-fault \
>       --remediate --remediation-judge --emit-and-wait --gate-escalation
Sat Jul  4 18:25:32 EDT 2026
time=2026-07-04T22:25:32.629Z level=INFO msg=--conn alias=alloydb-on-vm host=host.docker.internal
time=2026-07-04T22:25:32.635Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-07-04T22:25:32.635Z level=INFO msg="LLM diagnosis judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-07-04T22:25:32.635Z level=INFO msg="LLM remediation judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001
...
```

Finally, let's run `vault versions` to get the by-version stats (for v1.3 vs. v1.4 in this case):

```
[boris@ /tmp/helpdesk/helpdesk-v0.19.0-deploy/docker-compose]$ docker run --rm \
    --network helpdesk_default \
    -v "$HOME/.faulttest:/root/.faulttest" \
    ghcr.io/borisdali/helpdesk:v0.19.0 \
    faulttest vault versions \
      -- gateway http://gateway:8080 --api-key $HELPDESK_CLIENT_API_KEY \
      pbs_connection_triage
Gateway: http://gateway:8080  ·  version: v0.19.0-6278bb2  ·  host: 41c1c6120572

Version stats for pbs_connection_triage

VERSION     RUNS    SUCCESS%   AVG STEPS   AVG TIME    AVG DIAG   AVG REMED  APPROACH OK
──────────────────────────────────────────────────────────────────────────────────────
1.3         20      75%        –           1m15s       89%        100%       100%
  id=pb_cf2a367b
1.4 *       1       100%       –           1m38s       100%       100%       100%
  id=pb_faa4ee0f

* = currently active   SUCCESS% = resolved + transitioned
id/from lines show playbook_id and the run that generated that version
```

And the numbers tell the story:

```
  ┌────────────────┬───────┬───────┐
  │                │ v1.3  │ v1.4  │
  ├────────────────┼───────┼───────┤
  │ Runs           │ 20    │ 1     │
  ├────────────────┼───────┼───────┤
  │ Success%       │ 75%   │ 100%  │
  ├────────────────┼───────┼───────┤
  │ Avg diag score │ 89%   │ 100%  │
  ├────────────────┼───────┼───────┤
  │ Avg remed      │ 100%  │ 100%  │
  ├────────────────┼───────┼───────┤
  │ Avg time       │ 1m15s │ 1m38s │
  └────────────────┴───────┴───────┘
```

The diagnosti score went up from 89% to 100%, which emans that the `idle_session_timeout=0` trap fix worked on the first run. The time jump (1m15s → 1m38s) is noise from the gate approval wait, not agent behavior.

Now let's run the diff to see exactly what changed between the two versions. That's what `vault diff` is there for to get the before/after guidance diff alongside the score delta:

```
[boris@ /tmp/helpdesk/helpdesk-v0.19.0-deploy/docker-compose]$ docker run --rm \
>     --network helpdesk_default \
>     -v "$HOME/.faulttest:/root/.faulttest" \
>     ghcr.io/borisdali/helpdesk:v0.19.0 \
>     faulttest vault diff \
>       --gateway http://gateway:8080 \
>       --api-key $HELPDESK_CLIENT_API_KEY \
>       pb_cf2a367b pb_faa4ee0f
Gateway: http://gateway:8080  ·  version: v0.19.0-6278bb2  ·  host: 41c1c6120572

Diff: series pbs_connection_triage
  before  pb_cf2a367b  v1.3  Connection & Lock Triage
  after   pb_faa4ee0f  v1.4  Connection & Lock Triage

── guidance ────────────────────
  before  Start with get_server_info to check active_connections vs max_connections.
          If active_connections > 80% of max_connections, the system is at risk of
          connection exhaustion.

          Use get_blocking_queries to surface any blocking chains. A single idle
          transaction holding a lock can block dozens of application requests. Pay
          attention to wait_duration — any lock held > 5 minutes is a candidate for
          investigation.

          Use get_session_info (if available) on sessions with long durations to
          determine if they have uncommitted writes (has_writes=true). These are the
          most dangerous to terminate and should be escalated before action.

          Use get_lock_info to see the full lock table. Filter for AccessExclusiveLock —
          these block even reads and are the most disruptive.

          Common misdiagnosis: blaming connection count when the real issue is a single
          long-running transaction preventing autovacuum from reclaiming connection slots
          (wraparound risk). Check transaction age alongside connection count.

          Required output — write these exact lines at the end of your response,
          no markdown, no extra text on any line. The handoff signal (TRANSITION_TO
          or ESCALATE_TO) is mandatory: omitting it stalls the operator review gate:
          HYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: "<verbatim quote from tool output>"
          HYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>
          ROOT_CAUSE: HYPOTHESIS_1
          FINDINGS: connections <active>/<max> (<pct>%); blocker=<PID <pid> (<state>, <duration>, has_writes=<true|false>) | none>; recommended=<terminate_blocker|kill_idle|escalate|monitor>
          TRANSITION_TO: pbs_connection_remediate
  after   Start with get_server_info to check active_connections vs max_connections.
          If active_connections > 80% of max_connections, the system is at risk of
          connection exhaustion.

          Use get_blocking_queries to surface any blocking chains. A single idle
          transaction holding a lock can block dozens of application requests. Pay
          attention to wait_duration — any lock held > 5 minutes is a candidate for
          investigation.

          Use get_session_info (if available) on sessions with long durations to
          determine if they have uncommitted writes (has_writes=true). These are the
          most dangerous to terminate and should be escalated before action.

          Use get_lock_info to see the full lock table. Filter for AccessExclusiveLock —
          these block even reads and are the most disruptive.

          When idle connections are the primary cause (no blocking transactions, no locked
          queries, no uncommitted writes), your immediate remediation recommendation MUST
          be terminate_idle_connections — encoded as kill_idle in the FINDINGS line.
          Application-level changes (pool size reduction, idle timeout configuration) are
          the correct long-term fix but must NOT be listed as the primary recommended
          action when idle connections can be terminated directly right now. Reserve
          "escalate" only for sessions with has_writes=true that are too old to terminate
          safely without risking data loss.

          CRITICAL — idle_session_timeout=0 trap: finding that idle_session_timeout is
          disabled (set to 0) explains WHY idle connections accumulated. It is the root
          cause explanation, not the immediate action. Do NOT recommend configuring
          idle_session_timeout as your primary fix. The immediate recommendation is still
          kill_idle (terminate the idle connections now). Mention the timeout configuration
          only as a secondary long-term preventive measure, after kill_idle.

          Remediation decision guide:
          - No blocking queries AND no locks held → kill_idle (connection saturation)
          - Blocking queries found AND blocked_duration > 5 min → terminate_blocker
          - active_connections >= max_connections (no free slots) → escalate immediately
          - Sessions with has_writes=true open > 10 min → escalate before any termination

          Common misdiagnosis 1: blaming connection count when the real issue is a single
          long-running transaction preventing autovacuum from reclaiming connection slots
          (wraparound risk). Check transaction age alongside connection count.

          Common misdiagnosis 2: recommending idle_session_timeout configuration as the
          primary fix when idle connections are present. The timeout change prevents future
          accumulation but does nothing for the connections already open right now.

          Required output — write these exact lines at the end of your response,
          no markdown, no extra text on any line. The handoff signal (TRANSITION_TO
          or ESCALATE_TO) is mandatory: omitting it stalls the operator review gate:
          HYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: "<verbatim quote from tool output>"
          HYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>
          ROOT_CAUSE: HYPOTHESIS_1
          FINDINGS: connections <active>/<max> (<pct>%); idle=<idle_count>; blocker=<PID <pid> (<state>, <duration>, has_writes=<true|false>) | none>; recommended=<terminate_blocker|kill_idle|escalate|monitor>
          TRANSITION_TO: pbs_connection_remediate

── symptoms ────────────────────
  before  connection pool exhausted
          FATAL: remaining connection slots are reserved
          application cannot connect to database
          active_connections / max_connections > 0.8
          long-running transactions visible in monitoring
  after   connection pool exhausted
          FATAL: remaining connection slots are reserved
          application cannot connect to database
          active_connections / max_connections > 0.8
          long-running transactions visible in monitoring
          high proportion of idle connections relative to active (>80% idle)

── approval_mode ──────────────────�
  before  manual
  after

3 field(s) changed.
```

Good, the diff came through cleanly:

What v1.3 had: one "Common misdiagnosis" paragraph (confusing connection count with wraparound risk), no explicit `kill_idle` mandate, and a FINDINGS format without the idle= field.

What v1.4 added (three surgical additions):
- 1/ The explicit `kill_idle` mandate — "your immediate remediation recommendation MUST be terminate_idle_connections"
- 2/ The CRITICAL — `idle_session_timeout=0` trap paragraph — distinguishing root cause explanation from immediate action
- 3/ The Remediation decision guide — a four-case explicit decision tree

What the LLM's `suggest-update` draft did instead:
Removed the `kill_idle` mandate entirely and reinforced the wrong behaviour.
It read the failure as "model was confused about what to do" and responded by softening the guidance, when the actual gap was the opposite —
The guidance needed to be more explicit and more forceful.

So that's the lesson here: the LLM-generated update diagnosed the symptom (model hedged), but prescribed the wrong medicine (make it softer), while the correct fix was to add an explicit prohibition on the wrong recommendation path.


