# Informed Consent sample#6 (on Docker/Podman): Part 1

For the concept definition see [INFORMED_CONSENT.md](INFORMED_CONSENT.md). For the background on Fault Injection Testing in aiHelpDesk see [FAULTTEST.md](FAULTTEST.md).

For the background on interactive approvals see [here](PLAYBOOKS.md#interactive-approval-human-in-the-loop-demo).

Simple interactive/approval sample running on a host/VM is available [here](BENCHMARKING_SAMPLE4.md) and its counterpart for K8s is [here](BENCHMARKING_SAMPLE5.md).

In contrast to the previous samples, this one shows aiHelpDesk in action on Docker. The principles are exactly the same, with the tty-less test runs asking for async approvals via the [Decision Hub](DECISIONS.md).

This [blog post](https://medium.com/@borisdali/you-let-ai-operate-on-production-database-without-your-consent-bd4ffb954266) provides more color and sets the stage for the commands listed below and may be a better starting point if you are less familiar with aiHelpDesk.

## Let's get started

First off, to run aiHelpDesk in Docker or Podman containers on a host/VM, fire up the Docker Compose:

```
[boris@ /tmp/helpdesk/helpdesk-v0.16.0-deploy/docker-compose]$ docker compose --profile governance up -d
[+] Running 11/11
 ✔ Network helpdesk_default             Created            0.1s
 ✔ Container helpdesk-auditd-1          Healthy            14.8s
 ✔ Container helpdesk-k8s-agent-1       Started            13.1s
 ✔ Container helpdesk-research-agent-1  Started            13.2s
 ✔ Container helpdesk-incident-agent-1  Started            13.1s
 ✔ Container helpdesk-auditor-1         Started            13.0s
 ✔ Container helpdesk-database-agent-1  Started            13.1s
 ✔ Container helpdesk-sysadmin-agent-1  Started            13.1s
 ✔ Container helpdesk-gateway-1         Healthy            24.3s
 ✔ Container helpdesk-govbot-1          Started            23.9s
 ✔ Container helpdesk-secbot-1          Started            23.9s
```

To kick the tires, ask something about the databases that you know is true to confirm that the setup is correct (infra.json is loaded, password file, all the keys, etc.). The simplest way to do this is to run the `helpdesk-client` one-liner with the `--message` flag (alternatively, omit this flag and get dropped to the interactive `helpdesk-client` shell):

```
[boris@ /tmp/helpdesk/helpdesk-v0.16.0-deploy/docker-compose]$ docker compose --profile interactive run --rm helpdesk-client --purpose diagnostic --user alice@example.com --message "what dev databases are up?"
[+] Creating 7/7
 ✔ Container helpdesk-auditd-1          Running            0.0s
 ✔ Container helpdesk-incident-agent-1  Running            0.0s
 ✔ Container helpdesk-k8s-agent-1       Running            0.0s
 ✔ Container helpdesk-database-agent-1  Running            0.0s
 ✔ Container helpdesk-sysadmin-agent-1  Running            0.0s
 ✔ Container helpdesk-research-agent-1  Running            0.0s
 ✔ Container helpdesk-gateway-1         Running            0.0s
[+] Running 1/1
 ✔ Container helpdesk-auditd-1  Healthy                    1.0s
⠦  Thinking…I'll check the connectivity of all the dev/test databases in the infrastructure.
## Dev Database Status

**UP (4/6 databases):**

1. **pg-cluster-minkube** ✅ — PostgreSQL 18.1 (Kubernetes: Local Minikube Cluster)
2. **faulttest-db** ✅ — PostgreSQL 16.13 (Docker agent access)
3. **test-db** ✅ — PostgreSQL 16.13 (Alpine Linux)
4. **alloydb-on-vm** ✅ — PostgreSQL 17.7 on AlloyDB Omni

**DOWN (2/6 databases):**

1. **alloydb-on-vm-local** ❌ — Connection refused on localhost:5432 (server not running)
2. **faulttest-db-local** ❌ — Connection refused on localhost:15432 (server not running)

The two down databases appear to be the local host-side access variants that are not currently running.
[trace: tr_520aa3fc-4b2  2026-06-12 21:45:03]
[audit: check_connection (read), check_connection (read), check_connection (read), check_connection (read), check_connection (read), check_connection (read)]
```

Perhaps also add the uptime and current load to the mix to make things more interesting:

```
[boris@ /tmp/helpdesk/helpdesk-v0.16.0-deploy/docker-compose]$ docker compose --profile interactive run --rm helpdesk-client --purpose diagnostic --user alice@example.com --message "also add their uptime and current load"
```

| Database | Status | Version | Uptime | Connections | Load | Cache Hit Ratio |
|----------|--------|---------|--------|-------------|------|-----------------|
| **alloydb-on-vm** | ✅ Up | PG 17.7 | 8d 19h | 10/100 | Very Light | 99.98% |
| **alloydb-on-vm-local** | ❌ Down | — | — | — | — | — |
| **pg-cluster-minkube** | ✅ Up | PG 18.1 | 8d 19h | 12/100 | Very Light | 100.00% |
| **faulttest-db** | ✅ Up | PG 16.13 | 1d 18h | 6/100 | Very Light | 99.96% |
| **faulttest-db-local** | ❌ Down | — | — | — | — | — |
| **test-db** | ✅ Up | PG 16.13 | 8d 19h | 6/100 | Very Light | 99.97% |

### Key Observations:
- **Overall Health**: All reachable databases are healthy with excellent cache hit ratios (≥99.97%)
- **Connection Usage**: All running databases have light connection usage (6-12 out of 100 max)
- **Uptime Leaders**: `pg-cluster-minkube`, `test-db`, and `alloydb-on-vm` have been up for 11 days 4 hours
- **Newest**: `faulttest-db` is the most recently started at 4 days 3 hours uptime
- **Load**: All databases show minimal-to-light load with excellent cache performance

The two local-access databases (`alloydb-on-vm-local` and `faulttest-db-local`) are not reachable from this environment, which is expected if they're configured for local host-side access only.

```
[trace: tr_e1b641c9-380  2026-06-12 21:47:05]
[audit: check_connection (read), check_connection (read), check_connection (read), check_connection (read), check_connection (read), check_connection (read), get_status_summary (read), get_status_summary (read), get_status_summary (read), get_status_summary (read)]
```

Note the `[trace:...]` and `[audit:...]` lines at the bottom. The trace ID can be used to check the audit and the audit line shows immediately what tools from the [Tools Registry](TOOL_REGISTRY.md) were invoked by the agents (well, just the DB Agent in this case) to answer the questions.


## List of out of the box "system" fault injection tests

aiHelpDesk allows BYO faults and playbooks, but here's the list of fault injection tests that we ship and which are available out of the box:

```
[boris@ /tmp/helpdesk/helpdesk-v0.16.0-deploy/docker-compose]$ docker run --rm ghcr.io/borisdali/helpdesk:v0.16.0-23a7b2e faulttest list
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

Choose a failure scenario from the above list. In the example below we use `db-high-cache-miss`:

```
[boris@ /tmp/helpdesk/helpdesk-v0.16.0-deploy/docker-compose]$ date; time docker run --rm \
      --network helpdesk_default \
      -v "$(pwd)/infrastructure.json:/infrastructure.json:ro" \
      -v "$(pwd):/output" -w /output \
      -v "$HOME/.faulttest:/root/.faulttest" \
      -e DEV_DB_PASSWORD \
      -e ANTHROPIC_API_KEY \
      ghcr.io/borisdali/helpdesk:v0.16.0-6d05efd \
      faulttest run \
        --ids db-high-cache-miss \
        --conn "alloydb-on-vm" \
        --infra-config /infrastructure.json \
        --judge \
        --judge-vendor anthropic \
        --judge-model claude-haiku-4-5-20251001 \
        --judge-api-key $ANTHROPIC_API_KEY \
        --via-gateway --gateway http://gateway:8080 \
        --api-key $HELPDESK_CLIENT_API_KEY \
        --remediate --emit-and-wait --gate-escalation

Fri Jun 13 12:32:34 EDT 2026
time=2026-06-13T16:30:33.914Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: High cache miss ratio (db-high-cache-miss) ---
time=2026-06-13T16:30:33.915Z level=INFO msg="LLM judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-06-13T16:30:33.916Z level=INFO msg="injecting failure" id=db-high-cache-miss type=sql mode=external
time=2026-06-13T16:30:35.892Z level=INFO msg="sending prompt to agent via playbook" failure=db-high-cache-miss series_id=pbs_cache_miss_triage playbook_id=pb_d01ca069 gateway=http://gateway:8080
time=2026-06-13T16:31:11.445Z level=INFO msg="gate pending: operator review required" failure=db-high-cache-miss run_id=plr_ff704f1c escalation_target=""

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Next playbook     : pbs_cache_miss_remediate
  Findings          : cache_hit_ratio=92.86%; blks_read=571; blks_hit=7426; shared_buffers=5.74GB; largest_seqscan_table=public.test_large_table; recommended=prewarm
  Remediation plan  : High Cache Miss Ratio — Remediation (manual approval)
                      Confirm the cache miss condition is still active, then reset the buffer cache
statistics so the ratio reflects only current normal activity. If the
underlying cause was a one-time large sequential scan (the most common
injection pattern), resetting stats is sufficient — the ratio recovers
immediately as catalog queries populate the hit counter with no corresponding
disk reads. If shared_buffers is persistently undersized, the operator is
informed and escalation advice is provided.

  Hypotheses        :
    [PRIMARY 92%] Sequential scans on test_large_table (61 MB) during fault injection test caused cache misses, resulting in 92.86% hit ratio on the postgres database; shared_buffers (5.74 GB) is appropriately sized but cache was evicted by the workload pattern rather than undersizing
    [REJECTED 8%] Insufficient shared_buffers configuration causing the cache miss ratio — shared_buffers at 5.74 GB exceeds the 25% of system RAM recommendation and the test_large_table (61 MB) is substantially smaller than shared_buffers, so undersizing is not the root cause


Gate pending — run_id=plr_ff704f1c
  Resolve at        : POST http://gateway:8080/api/v1/decisions/gate:plr_ff704f1c/resolve
  Body fields:
    resolution    : "approved" | "denied"
    resolved_by   : your email or user ID
    approval_mode : "auto" | "review" | "manual" (default: playbook setting)
    reason        : optional — your assessment of the diagnosis

time=2026-06-13T16:31:27.332Z level=INFO msg="faultlib: gate poll" run_id=plr_ff704f1c outcome=gate_pending
time=2026-06-13T16:31:43.291Z level=INFO msg="faultlib: gate poll" run_id=plr_ff704f1c outcome=gate_pending
```

Good, the triage playbook is finished, the diagnosis is presented and so is the remediation plan on how AI intends to fix it.

Review it carefully. Both. Make sure that you agree or at least are comfortable with both. The diagnosis. And the remediation plan.

If you are, approve it via the [Decision Hub](DECISIONS.md). How? Well, the request ID was printed in the excerpt above (`plr_ff704f1c`) and we know the type of a request is `gate`, so the full ID is `gate:plr_ff704f1c`, but there's also a simple way to list the pending decisions in the Hub. Leave the test run pending and run this from a different terminal:

```
[boris@ ~]$ curl -H "Authorization: Bearer $API_KEY"  'http://localhost:8080/api/v1/decisions?status=pending&type=gate' | jq '.total'
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100 19880    0 19880    0     0   7463      0 --:--:--  0:00:02 --:--:--  7462
10

[boris@~]$ curl -H "Authorization: Bearer $API_KEY"  'http://localhost:8080/api/v1/decisions?status=pending&type=gate' | jq '.decisions[] | {id,status,summary}'
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100 19880    0 19880    0     0   4929      0 --:--:--  0:00:04 --:--:--  4930
{
  "id": "gate:plr_ff704f1c",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_cache_miss_remediate"
}
{
  "id": "gate:plr_964d7112",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_bgwriter_remediate"
}
...
```

The top request has the same ID as in our pending test, which is `plr_ff704f1c`. Let's get the full details of that request to confirm:

```
[boris@ ~]$ RUN_ID="plr_ff704f1c"
[boris@ ~]$ curl -H "Authorization: Bearer $API_KEY"  http://localhost:8080/api/v1/decisions | jq '.decisions[] | select(.id == "gate:'"$RUN_ID"'")'
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100 20257    0 20257    0     0   7970      0 --:--:--  0:00:02 --:--:--  7968
{
  "id": "gate:plr_ff704f1c",
  "type": "gate",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_cache_miss_remediate",
  "requested_by": "",
  "requested_at": "2026-06-13T16:30:36Z",
  "expires_at": "0001-01-01T00:00:00Z",
  "resolve_url": "/api/v1/decisions/gate:plr_ff704f1c/resolve",
  "extra": {
    "diagnostic_report": {
      "hypotheses": [
        {
          "rank": 1,
          "text": "Sequential scans on test_large_table (61 MB) during fault injection test caused cache misses, resulting in 92.86% hit ratio on the postgres database; shared_buffers (5.74 GB) is appropriately sized but cache was evicted by the workload pattern rather than undersizing",
          "confidence": 0.92,
          "evidence": "cache_hit_ratio",
          "is_primary": true
        },
        {
          "rank": 2,
          "text": "Insufficient shared_buffers configuration causing the cache miss ratio",
          "confidence": 0.08,
          "rejected_reason": "shared_buffers at 5.74 GB exceeds the 25% of system RAM recommendation and the test_large_table (61 MB) is substantially smaller than shared_buffers, so undersizing is not the root cause",
          "is_primary": false
        }
      ],
      "root_cause": "Sequential scans on test_large_table (61 MB) during fault injection test caused cache misses, resulting in 92.86% hit ratio on the postgres database; shared_buffers (5.74 GB) is appropriately sized but cache was evicted by the workload pattern rather than undersizing"
    },
    "findings": "cache_hit_ratio=92.86%; blks_read=571; blks_hit=7426; shared_buffers=5.74GB; largest_seqscan_table=public.test_large_table; recommended=prewarm",
    "gate_type": "transition",
    "remediation_preview": {
      "approval_mode": "manual",
      "description": "Confirm the cache miss condition is still active, then reset the buffer cache\nstatistics so the ratio reflects only current normal activity. If the\nunderlying cause was a one-time large sequential scan (the most common\ninjection pattern), resetting stats is sufficient — the ratio recovers\nimmediately as catalog queries populate the hit counter with no corresponding\ndisk reads. If shared_buffers is persistently undersized, the operator is\ninformed and escalation advice is provided.\n",
      "name": "High Cache Miss Ratio — Remediation",
      "series_id": "pbs_cache_miss_remediate"
    },
    "series_id": "pbs_cache_miss_triage",
    "transition_target": "pbs_cache_miss_remediate"
  }
}
```

Looks good? Approve it with a single command:

```
[boris@ /tmp/helpdesk/helpdesk-v0.16.0-deploy/docker-compose]$ curl -X POST \
     -H "Authorization: Bearer $APIKEY" \
     -H "Content-Type: application/json" \
     -d '{"resolution":"approved","resolved_by":"boris@borisdali.com"}' \
     http://localhost:8080/api/v1/decisions/gate:$RUN_ID/resolve|jq .

{
  "run_id": "plr_b0207596",
  "status": "pending_approval",
  "step": {
    "index": 1,
    "agent": "database",
    "tool": "get_database_stats",
    "args": {
      "connection_string": "host=host.docker.internal port=5432 dbname=postgres user=postgres password=ChangeMe123"
    },
    "reason": "Step 1: Confirm the current state by retrieving blks_hit and blks_read to compute the cache hit ratio and verify the 92.86% finding from triage.",
    "action_class": "read"
  },
  "approval_id": "apr_aa0346c2",
  "effective_approval_mode": "manual"
}
```

This unblocks the test, so going back to the original test terminal, we see this:

```
...
time=2026-06-13T16:35:38.440Z level=INFO msg="faultlib: gate poll" run_id=plr_ff704f1c outcome=gate_pending
time=2026-06-13T16:35:54.062Z level=INFO msg="faultlib: gate poll" run_id=plr_ff704f1c outcome=gate_pending
time=2026-06-13T16:36:09.618Z level=INFO msg="faultlib: gate poll" run_id=plr_ff704f1c outcome=transitioned
time=2026-06-13T16:36:10.153Z level=INFO msg="agent_approve: pending step" step_index=0 tool=get_database_stats action_class=read reason="" approval_id=apr_aa0346c2
time=2026-06-13T16:36:14.681Z level=INFO msg="agent_approve: pending step" step_index=2 tool=check_connection action_class=read reason="The database is not registered in the infrastructure config; first verify connectivity and confirm the database is reachable before proceeding with the remediation playbook." approval_id=apr_1de7d8bd
time=2026-06-13T16:36:19.780Z level=INFO msg="agent_approve: pending step" step_index=3 tool=get_database_info action_class=read reason="The target database is not registered in the infrastructure config; use get_database_info to list available databases and identify which one corresponds to the postgres instance at host.docker.internal:5432, or confirm if a different registered database should be used instead." approval_id=apr_f2842a52
time=2026-06-13T16:36:25.203Z level=INFO msg="agent_approve: pending step" step_index=4 tool=get_database_stats action_class=read reason="The target database is not registered; attempting to use 'test-db' from the known databases list to retrieve current cache hit ratio (Step 1 of playbook) and verify the high cache miss condition flagged in triage." approval_id=apr_60f7ef6c
time=2026-06-13T16:36:30.363Z level=INFO msg="agent_approve: pending step" step_index=5 tool=get_database_stats action_class=read reason="The target database is not registered; attempt remediation using 'test-db' from the known databases list, which is the closest match to the localhost scenario described in the triage findings." approval_id=apr_527f56da
time=2026-06-13T16:36:38.478Z level=INFO msg="agent_approve: pending step" step_index=6 tool=get_database_stats action_class=read reason="The target database host.docker.internal is not registered; attempting to use the available registered database 'faulttest-db' from the known list to proceed with Step 1 of the remediation playbook (confirm current cache hit ratio)." approval_id=apr_a0a64ee9
time=2026-06-13T16:36:42.276Z level=INFO msg="agent_approve: remediation complete" summary="The database target 'host=host.docker.internal port=5432 dbname=postgres user=postgres' is not registered in the infrastructure config. The known available databases are: alloydb-on-vm, alloydb-on-vm-local, faulttest-db, faulttest-db-local, pg-cluster-minkube, test-db. The playbook cannot proceed without a registered database connection. Operator must either register the target database with IT administrator or provide credentials for one of the known databases to continue remediation."

  Feedback pending — resolve at:
  POST /api/v1/decisions/feedback:plr_ff704f1c/resolve
  Body fields:
    resolution  : "approved" (diagnosis correct) | "denied" (diagnosis wrong)
    resolved_by : your email or user ID
    reason      : actual root cause (required when resolution="denied")

  Waiting for feedback (10m timeout, Ctrl+C to skip)...
```

At this point the fault injection test stops again and waits (for up to 10 min) for the optional gate-level feedback. We strongly encourage our customers to provide one. What do they think of the diagnosis. Was it clear and concise? Was the proposed remediation plan adequate? Was the human operator clear on what's about to transpire?

Logistics: providing a feedback in the non-interactive run is similar to clearing a gate. In another terminal query [Decision Hub](DECISIONS.md) API to see the pending feedback requests (or just grab the feedback:plr_<XYZ> from the test run):

```
[boris@~]$ curl -H "Authorization: Bearer $API_KEY" 'http://localhost:8080/api/v1/decisions?status=pending&type=feedback' | jq '.decisions[]  | {id,status,summary}'
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100  1901  100  1901    0     0   6341      0 --:--:-- --:--:-- --:--:--  6357
{
  "id": "feedback:plr_ff704f1c",
  "status": "pending",
  "summary": "Diagnosis feedback needed — pbs_cache_miss_triage"
}
{
  "id": "feedback:plr_6e0d1bd7",
  "status": "pending",
  "summary": "Diagnosis feedback needed — pbs_cache_miss_triage"
}
...
```

Once the top feedback request ID is confirmed to match the one prompted in the test run, review it and provide feedback. Note that this RUN_ID=`feedback:plr_ff704f1c` (for the purpose of providing feedback) is different from the previous RUN_ID=`gate:plr_ff704f1c` (for the purpose of clearing the gate). Some customers find it easier if the env vars are named differently too. Either way:

```
[boris@ ~]$ RUN_ID="plr_ff704f1c"
[boris@ ~]$ curl -H "Authorization: Bearer $API_KEY"  http://localhost:8080/api/v1/decisions | jq '.decisions[] | select(.id == "feedback:'"$RUN_ID"'")'
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100 25831    0 25831    0     0  11699      0 --:--:--  0:00:02 --:--:-- 11704
{
  "id": "feedback:plr_ff704f1c",
  "type": "feedback",
  "status": "pending",
  "summary": "Diagnosis feedback needed — pbs_cache_miss_triage",
  "requested_by": "faulttest",
  "requested_at": "2026-06-13T16:41:22.985957967Z",
  "expires_at": "0001-01-01T00:00:00Z",
  "resolve_url": "/api/v1/decisions/feedback:plr_613eb2be/resolve",
  "extra": {
    "run_id": "plr_ff704f1c",
    "series_id": "pbs_cache_miss_triage"
  }
}
```

Actual at-gate feedback:

```
[boris@ ~]$ RUN_ID="plr_ff704f1c"
[boris@ ~]$ curl -X POST -H "Authorization: Bearer $APIKEY" -H "Content-Type: application/json" \
>     -d '{"resolution":"approved","resolved_by":"boris@borisdali.com","reason":"Sequential scans on test_large_table evicted hot data from shared_buffers"}' \
>     http://localhost:8080/api/v1/decisions/feedback:$RUN_ID/resolve
{"verdict_correct":true,"status":"resolved"}
```

Once the at-gate feedback is submitted (or the threshold of 10min expires), the fault injection test proceeds:

```
  Feedback received — thank you.

Remediation: RECOVERED in 0.2s (score: 100%)
Incident plr_ff704f1c — resolved in 0.2s
  Diagnosis  : Sequential scans on test_large_table (61 MB) during fault injection test caused cache misses, resulting in 92.86% hit ratio on the postgres database; shared_buffers (5.74 GB) is appropriately sized but cache was evicted by the workload pattern rather than undersizing
  Remediation: pbs_cache_miss_remediate
  Narrative  : GET http://gateway:8080/api/v1/incidents/plr_ff704f1c
Vault: draft saved → pb_be39c4d3 (activate with 'faulttest vault list')
time=2026-06-13T16:42:11.340Z level=INFO msg="tearing down failure" id=db-high-cache-miss type=sql
Diagnostic Result:   [PASS] score=86% (keywords=100% tools=100% judge=67%)
Remediation Result:  [PASS] score=100% (0.2s, playbook)
Overall Result:      [PASS] score=92%

=== Fault Test Report: 8aa4804e ===

[PASS] High cache miss ratio (db-high-cache-miss) - score: 86% [judge: 67%]
       Keywords: 100% | Tools: 100% | Judge: 67%
       Remediation: 100% (0.2s, playbook) | Overall: 92%
       Reasoning: "Agent correctly identified low cache hit ratio caused by sequential scans on test_large_table and appropriately ruled out shared_buffers undersizing; however, failed to recommend the expected mitigations (index optimization on scanned columns or cache statistics reset to establish fresh baseline), instead proposing pg_prewarm which is less aligned with addressing the root access pattern issue."

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

LLM judge scored diagnosis for 1 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-8aa4804e.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "8aa4804e",
  "timestamp": "2026-06-13T16:42:11Z",
  "results": [
    {
      "failure_id": "db-high-cache-miss",
      "failure_name": "High cache miss ratio",
      "category": "database",
      "score": 0.8680000000000001,
      "passed": true,
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "tool_evidence_mode": "structured",
      "response_text": "I'll begin investigating why the target database is unavailable. Let me start with the connection check and then proceed with diagnostic steps.\nI see that the connection string is not registered in the infrastructure configuration. However, the format you provided matches the AlloyDB Omni container (host.docker.internal:5432). Let me attempt the connection using the registered alias \"alloydb-on-vm\" which points to that same host/port combination.\nGood — the database is reachable via the registered alias. The initial connection string format was not registered, but the underlying database is active. Now let me proceed with Step 1 of the Expert Guidance: retrieve database statistics to calculate the cache hit ratio.\nExcellent. I now have cache statistics. The **postgres** database shows a cache hit ratio of **92.86%** (concerning range 0.90–0.95), while the other databases (alloydbadmin and alloydbmetadata) show excellent hit ratios (99.98% and 99.99%). The postgres database has blks_read=571 and cache_hits=7426.\n\nNow let me proceed with Step 2: check for large sequential scans that might be evicting hot data.\nThe pg_stat_statements extension is not enabled, so I cannot directly retrieve slow queries. Let me check active connections to see if there are any currently running queries that might be performing large sequential scans.\nExcellent. I now have the memory configuration:\n- **shared_buffers = 735488 × 8kB = ~5.74 GB**\n- No active connections currently running\n\nLet me also get table statistics to identify if there are large tables that could be causing sequential scans.\nPerfect. There is a **test_large_table** with:\n- **Size: 61 MB**\n- **seq_scan count: 3** (indicating sequential scans have been performed)\n- **live_rows: 0** (the table appears to be empty or recently truncated)\n- **dead_rows: 0**\n\nLet me check the server baseline to understand the system's total RAM and verify shared_buffers sizing.\nExcellent diagnostic data. Now I have a clear picture. Let me analyze the findings:\n\n**Key Observations:**\n1. **Cache Hit Ratio (postgres database):** 92.86% — in the concerning range (0.90–0.95)\n2. **shared_buffers:** 735,488 × 8kB = **5.74 GB**\n3. **test_large_table:** 61 MB total, 57 MB table data + 4.2 MB index, with **seq_scan count = 3**\n4. **blks_read:** 571 blocks, **cache_hits:** 7,426\n5. **Current database size:** 74 MB total\n6. **No current active connections** (queries have completed)\n\nThe test_large_table at 61 MB is significantly smaller than the 5.74 GB shared_buffers, which is well-sized (PostgreSQL recommendation is 25% of RAM; 5.74 GB suggests a system with ~23 GB RAM total). The table should fit comfortably in shared_buffers.\n\nHowever, the cache hit ratio of 92.86% indicates elevated cache misses. Given that:\n- The large table has only 57 MB of actual data (fits in shared_buffers)\n- Three sequential scans were performed\n- The table currently contains 0 live rows (empty/truncated state)\n- The stats show blks_read=571 and cache_hits=7,426\n\n**Root Cause Hypothesis:**\nThe fault injection test filled test_large_table with data and then ran sequential scans (seq_scan count=3), which loaded 571 blocks into memory and resulted in cache misses. The table was subsequently truncated (0 live rows), but the statistics show the historical cache behavior from those scans. The elevated cache miss ratio reflects the impact of the sequential scans on the postgres database during the fault test window.\n\nThe shared_buffers configuration is **appropriately sized** (5.74 GB for what appears to be a ~23 GB system). The issue is the workload pattern: large sequential scans on test_large_table were performed as part of the fault injection test.\n\n**Remediation Recommendation:**\nSince shared_buffers is already well-sized (25%+ of system RAM) and the test table is now empty, the cache hit ratio should recover as the workload continues. If the fault test needs to be re-run or if similar sequential scans are required, the recommended mitigation would be to use **pg_prewarm** to pre-load the test_large_table into shared_buffers before the scans, which would eliminate cache misses for that specific workload.\n\nHYPOTHESIS_1: Sequential scans on test_large_table (61 MB) during fault injection test caused cache misses, resulting in 92.86% hit ratio on the postgres database; shared_buffers (5.74 GB) is appropriately sized but cache was evicted by the workload pattern rather than undersizing | CONFIDENCE: 0.92 | EVIDENCE: \"cache_hit_ratio | 92.86\" and \"seq_scan | 3\" on test_large_table; \"shared_buffers=735488\" (5.74 GB); \"total_size | 61 MB\" for test_large_table\nHYPOTHESIS_2: Insufficient shared_buffers configuration causing the cache miss ratio | CONFIDENCE: 0.08 | REJECTED: shared_buffers at 5.74 GB exceeds the 25% of system RAM recommendation and the test_large_table (61 MB) is substantially smaller than shared_buffers, so undersizing is not the root cause\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "34.249834183s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 0.67,
      "judge_reasoning": "Agent correctly identified low cache hit ratio caused by sequential scans on test_large_table and appropriately ruled out shared_buffers undersizing; however, failed to recommend the expected mitigations (index optimization on scanned columns or cache statistics reset to establish fresh baseline), instead proposing pg_prewarm which is less aligned with addressing the root access pattern issue.",
      "judge_model": "claude-haiku-4-5-20251001",
      "remediation_attempted": true,
      "remediation_passed": true,
      "recovery_time_seconds": 0.171108917,
      "remediation_score": 1,
      "remediation_method": "playbook",
      "overall_score": 0.9208000000000001
    }
  ],
  "summary": {
    "total": 1,
    "passed": 1,
    "failed": 0,
    "pass_rate": 1,
    "categories": {
      "database": {
        "total": 1,
        "passed": 1,
        "rate": 1
      }
    }
  }
}
--- END FAULTTEST REPORT JSON ---
```

