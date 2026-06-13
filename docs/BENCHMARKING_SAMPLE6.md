# Benchmarking sample#6 (Docker/Podman): Informed Consent

For the background on Fault Injection Testing in aiHelpDesk see [here](FAULTTEST.md).

For the background on interactive approvals see [here](PLAYBOOKS.md#interactive-approval-human-in-the-loop-demo).

Simple interactive/approval sample running on a host/VM is available [here](BENCHMARKING_SAMPLE4.md) and its counterpart for K8s is [here](BENCHMARKING_SAMPLE5.md).

In contrast to the previous samples, this one show aiHelpDesk in action on Docker. The principles are exactly the same, with the tty-less test runs asking for async approvals via the [Decision Hub](DECISIONS.md).

This [blog post](https://medium.com/@borisdali/you-let-ai-operate-on-production-database-without-your-consent-bd4ffb954266) provides more color and sets the stage for the commands listed below and may be a better starting point to get the context if you less familiar with aiHelpDesk.

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
      ghcr.io/borisdali/helpdesk:v0.16.0-21d906a \
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

Fri Jun 12 18:24:34 EDT 2026
time=2026-06-12T22:24:35.128Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-06-12T22:24:35.130Z level=INFO msg="LLM judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: High cache miss ratio (db-high-cache-miss) ---
time=2026-06-12T22:24:35.130Z level=INFO msg="injecting failure" id=db-high-cache-miss type=sql mode=external
time=2026-06-12T22:24:38.555Z level=INFO msg="sending prompt to agent via playbook" failure=db-high-cache-miss series_id=pbs_cache_miss_triage playbook_id=pb_d01ca069 gateway=http://gateway:8080

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Next playbook     : pbs_cache_miss_remediate
  Findings          : cache_hit_ratio=92.80%; blks_read=576; blks_hit=7419; shared_buffers=5.75GB; largest_seqscan_table=public.test_large_table; recommended=investigate_queries
  Remediation plan  : High Cache Miss Ratio — Remediation (manual approval)
                      Confirm the cache miss condition is still active, then reset the buffer cache
statistics so the ratio reflects only current normal activity. If the
underlying cause was a one-time large sequential scan (the most common
injection pattern), resetting stats is sufficient — the ratio recovers
immediately as catalog queries populate the hit counter with no corresponding
disk reads. If shared_buffers is persistently undersized, the operator is
informed and escalation advice is provided.

  Hypotheses        :
    [PRIMARY 85%] Excessive sequential scans on test_large_table (57 MB) are flooding the buffer pool and evicting hot data, causing elevated cache miss ratio on the postgres database despite adequate shared_buffers configuration
    [REJECTED 5%] Undersized shared_buffers configuration causing persistent cache misses — shared_buffers is already set to 5.75 GB, which is 25%+ of available memory and far exceeds the largest table size of 57 MB, making undersizing an implausible cause


Gate pending — run_id=plr_68a3a679
  Resolve at        : POST http://gateway:8080/api/v1/decisions/gate:plr_68a3a679/resolve

time=2026-06-12T22:25:08.533Z level=INFO msg="gate pending: operator review required" failure=db-high-cache-miss run_id=plr_68a3a679 escalation_target=""
time=2026-06-12T22:25:24.056Z level=INFO msg="faultlib: gate poll" run_id=plr_68a3a679 outcome=gate_pending
time=2026-06-12T22:25:39.543Z level=INFO msg="faultlib: gate poll" run_id=plr_68a3a679 outcome=gate_pending
```

Good, the triage playbook is finished, the diagnosis is presented and so is the remediation plan on how AI intends to fix it.

Review carefully. Both. Make sure that you agree or at least comfortable with both. The diagnosis. And the remediation plan.

If you are, approve it via the [Decision Hub](DECISIONS.md). How? Well, the request ID was printed in the excerpt above (`plr_68a3a679`) and we know the type of a request is `gate`, so the full ID is `gate:plr_68a3a679`, but there's also a simple way to list the pending decisions in the Hub. Leave the test run pending and run this from a different terminal:

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
  "id": "gate:plr_68a3a679",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_cache_miss_remediate"
}
{
  "id": "gate:plr_964d7112",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_bgwriter_remediate"
}
{
  "id": "gate:plr_c620fa71",
  "status": "pending",
  "summary": "Triage complete — ESCALATE_TO pbs_stale_slot_remediate"
}
{
  "id": "gate:plr_9ebf93eb",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_vacuum_remediate"
}
{
  "id": "gate:plr_4e3b5c49",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_lock_chain_remediate"
}
{
  "id": "gate:plr_49300d34",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_connection_remediate"
}
{
  "id": "gate:plr_55eecec2",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_vacuum_remediate"
}
{
  "id": "gate:plr_9b25353c",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_slow_query_remediate"
}
{
  "id": "gate:plr_1124cd4a",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_slow_query_remediate"
}
{
  "id": "gate:plr_474e4ffa",
  "status": "pending",
  "summary": "Triage complete — TRANSITION_TO pbs_connection_remediate"
}
```

The top request has the same ID as in our pending test, which is `plr_68a3a679`. Let's get the full details of that request to confirm:

```
[boris@ ~]$ RUN_ID="plr_68a3a679"
[boris@ ~]$ curl -H "Authorization: Bearer $API_KEY"  http://localhost:8080/api/v1/decisions | jq '.decisions[] | select(.id == "gate:'"$RUN_ID"'")'
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100 20257    0 20257    0     0   7970      0 --:--:--  0:00:02 --:--:--  7968
{
  "decisions": [
    {
      "id": "gate:plr_68a3a679",
      "type": "gate",
      "status": "pending",
      "summary": "Triage complete — TRANSITION_TO pbs_cache_miss_remediate",
      "requested_by": "",
      "requested_at": "2026-06-12T22:24:39Z",
      "expires_at": "0001-01-01T00:00:00Z",
      "resolve_url": "/api/v1/decisions/gate:plr_68a3a679/resolve",
      "extra": {
        "diagnostic_report": {
          "hypotheses": [
            {
              "rank": 1,
              "text": "Excessive sequential scans on test_large_table (57 MB) are flooding the buffer pool and evicting hot data, causing elevated cache miss ratio on the postgres database despite adequate shared_buffers configuration",
              "confidence": 0.85,
              "evidence": "cache_hit_ratio",
              "is_primary": true
            },
            {
              "rank": 2,
              "text": "Undersized shared_buffers configuration causing persistent cache misses",
              "confidence": 0.05,
              "rejected_reason": "shared_buffers is already set to 5.75 GB, which is 25%+ of available memory and far exceeds the largest table size of 57 MB, making undersizing an implausible cause",
              "is_primary": false
            }
          ],
          "root_cause": "Excessive sequential scans on test_large_table (57 MB) are flooding the buffer pool and evicting hot data, causing elevated cache miss ratio on the postgres database despite adequate shared_buffers configuration"
        },
        "findings": "cache_hit_ratio=92.80%; blks_read=576; blks_hit=7419; shared_buffers=5.75GB; largest_seqscan_table=public.test_large_table; recommended=investigate_queries",
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
}
```

Now we can approve it with a single command:

```
[boris@ /tmp/helpdesk/helpdesk-v0.16.0-deploy/docker-compose]$ curl -X POST \
     -H "Authorization: Bearer $APIKEY" \
     -H "Content-Type: application/json" \
     -d '{"resolution":"approved","resolved_by":"boris@borisdali.com"}' \
     http://localhost:8080/api/v1/decisions/gate:$RUN_ID/resolve|jq .

{
  "run_id": "plr_68a3a679",
  "status": "pending_approval",
  "step": {
    "index": 1,
    "agent": "database",
    "tool": "get_database_stats",
    "args": {
      "connection_string": "host=host.docker.internal port=5432 dbname=postgres user=postgres password=ChangeMe123"
    },
    "reason": "Step 1: Retrieve current blks_hit and blks_read to confirm the cache hit ratio state before remediation.",
    "action_class": "read"
  },
  "approval_id": "apr_b8f20321",
  "effective_approval_mode": "manual"
}
```

This unblocks the test, so going back to the original test terminal, we see this:

```
...
time=2026-06-13T15:49:17.286Z level=INFO msg="faultlib: gate poll" run_id=plr_613eb2be outcome=gate_pending
time=2026-06-13T15:49:32.776Z level=INFO msg="faultlib: gate poll" run_id=plr_613eb2be outcome=transitioned
time=2026-06-13T15:49:33.377Z level=INFO msg="agent_approve: pending step" step_index=0 tool=get_database_stats action_class=read reason="" approval_id=apr_7eb5bd53
  Feedback pending — resolve at:
  POST /api/v1/decisions/feedback:plr_613eb2be/resolve
  Body: {"resolution":"approved"|"denied","resolved_by":"...","reason":"..."}

  Waiting for feedback (10m timeout, Ctrl+C to skip)...
```

At this point the fault injection test stops again and wait (for up to 10 min) for the optional gate-level feedback. We strongly encourage our customers to provide one. What do they think of the diagnosis. Was it clear and concise? Was the proposed remediation plan adequate? Was the human operator clear on what's about to transpire?

Logistics: providing a feedback in the non-interactive run is similar to clearing a gate. In another terminal query [Decision Hub](DECISIONS.md) API to see the pending feedback requests (or just grab the feedback:plr_<XYZ> from the test run):

```
[boris@~]$ curl -H "Authorization: Bearer $API_KEY" 'http://localhost:8080/api/v1/decisions?status=pending&type=feedback' | jq '.decisions[]  | {id,status,summary}'
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100  1901  100  1901    0     0   6341      0 --:--:-- --:--:-- --:--:--  6357
{
  "id": "feedback:plr_613eb2be",
  "status": "pending",
  "summary": "Diagnosis feedback needed — pbs_cache_miss_triage"
}
{
  "id": "feedback:plr_6e0d1bd7",
  "status": "pending",
  "summary": "Diagnosis feedback needed — pbs_cache_miss_triage"
}
{
  "id": "feedback:plr_68a3a679",
  "status": "pending",
  "summary": "Diagnosis feedback needed — pbs_cache_miss_triage"
}
{
  "id": "feedback:plr_dee972c2",
  "status": "pending",
  "summary": "Diagnosis feedback needed — pbs_slow_query_triage"
}
{
  "id": "feedback:plr_7bbe456d",
  "status": "pending",
  "summary": "Diagnosis feedback needed — pbs_cache_miss_triage"
}
```

Once the top feedback request ID is confirmed to match the one prompted in the test run, review it and provide feedback:

```
[boris@ ~]$ RUN_ID="plr_613eb2be"
[boris@ ~]$ curl -H "Authorization: Bearer $API_KEY"  http://localhost:8080/api/v1/decisions | jq '.decisions[] | select(.id == "feedback:'"$RUN_ID"'")'
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100 25831    0 25831    0     0  11699      0 --:--:--  0:00:02 --:--:-- 11704
{
  "id": "feedback:plr_613eb2be",
  "type": "feedback",
  "status": "pending",
  "summary": "Diagnosis feedback needed — pbs_cache_miss_triage",
  "requested_by": "faulttest",
  "requested_at": "2026-06-13T15:50:22.985957967Z",
  "expires_at": "0001-01-01T00:00:00Z",
  "resolve_url": "/api/v1/decisions/feedback:plr_613eb2be/resolve",
  "extra": {
    "run_id": "plr_613eb2be",
    "series_id": "pbs_cache_miss_triage"
  }
}
```

Actual at-gate feedback:

```
[boris@ ~]$ curl -X POST -H "Authorization: Bearer $APIKEY" -H "Content-Type: application/json" -d '{"resolution":"approved","resolved_by":"boris@borisdali.com"}' http://localhost:8080/api/v1/decisions/feedback:$RUN_ID/resolve|jq .
  % Total    % Received % Xferd  Average Speed   Time    Time     Time  Current
                                 Dload  Upload   Total   Spent    Left  Speed
100   108  100    47  100    61    151    197 --:--:-- --:--:-- --:--:--   349
{
  "diagnosis_correct": true,
  "status": "resolved"
}
```

Once the at-gate feedback is submitted (or the threshold of 10min expires), the fault injection test proceeds:

```
  Feedback received — thank you.

Remediation: RECOVERED in 0.2s (score: 100%)
Incident plr_613eb2be — resolved in 0.2s
  Diagnosis  : Sequential scans on test_large_table (61 MB) evicted hot data from the 5.76 GB shared_buffers pool, causing elevated cache misses in the postgres database
  Remediation: pbs_cache_miss_remediate
  Narrative  : GET http://gateway:8080/api/v1/incidents/plr_613eb2be
time=2026-06-13T15:53:38.517Z level=INFO msg="tearing down failure" id=db-high-cache-miss type=sql
Vault: draft saved → pb_4d1ad1f1 (activate with 'faulttest vault list')
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% judge=100%)
Remediation Result:  [PASS] score=100% (0.2s, playbook)
Overall Result:      [PASS] score=100%

=== Fault Test Report: 30270444 ===

[PASS] High cache miss ratio (db-high-cache-miss) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Remediation: 100% (0.2s, playbook) | Overall: 100%
       Reasoning: "Agent correctly identified low cache hit ratio (92.86%) caused by sequential scans on test_large_table, properly ruled out undersized shared_buffers using evidence from sibling databases and table size comparison, and appropriately recommended optimizing query access patterns (implicitly through the root cause analysis), while correctly establishing that the issue is workload pattern not buffer pool sizing."

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

LLM judge scored diagnosis for 1 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-30270444.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "30270444",
  "timestamp": "2026-06-13T15:53:38Z",
  "results": [
    {
      "failure_id": "db-high-cache-miss",
      "failure_name": "High cache miss ratio",
      "category": "database",
      "score": 1,
      "passed": true,
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "tool_evidence_mode": "structured",
      "response_text": "I'll help you diagnose why the database at host=host.docker.internal port=5432 is unavailable. Let me start by checking the connection and gathering diagnostic information.\nThe connection string is not registered in the infrastructure config. However, this appears to be the AlloyDB instance (host.docker.internal:5432 matches alloydb-on-vm in the infrastructure). Let me try using the registered database name and also attempt a direct connection approach.\nGood! The database is actually reachable — check_connection succeeded using alloydb-on-vm. The database is running PostgreSQL 17.7. Now let me gather the diagnostic data according to the Expert Guidance for cache miss diagnosis. I'll use the alloydb-on-vm connection (which maps to the specified host/port).\nExcellent! I now have the key diagnostic data. Let me analyze the findings:\n\n**Database Statistics Analysis:**\n- **postgres database**: cache_hit_ratio = 92.86% (concerning, below 95%)\n  - blks_read = 571\n  - cache_hits = 7426\n  - Total blocks accessed = 571 + 7426 = 7997\n\n- **alloydbadmin database**: cache_hit_ratio = 99.98% (healthy)\n- **alloydbmetadata database**: cache_hit_ratio = 99.99% (healthy)\n\n**Memory Configuration:**\n- shared_buffers = 735,488 * 8 kB = 5,865,904 kB ≈ 5.76 GB\n\nThe postgres database shows a cache hit ratio of 92.86%, which falls into the \"concerning\" range (90–95%). This indicates elevated cache misses. Let me get more detailed information about the actual queries and any recent stats resets.\nPerfect! This is the key evidence. The table statistics reveal:\n- A table named **test_large_table** with a size of **61 MB**\n- **3 sequential scans** (seqscan = 3)\n- **0 index scans**\n- 0 live rows (table appears to be emptied after the fault injection)\n\nThis is consistent with the fault injection scenario described in the playbook: a large table was filled and sequential scans were run, which would read every block and evict hot data from the 5.76 GB shared_buffers pool.\n\nLet me verify the server information to understand the total available RAM and get additional context.\nPerfect! I now have complete diagnostic evidence. Let me summarize the key findings:\n\n## Diagnosis\n\n**Cache Hit Ratio Analysis:**\n- postgres database: 92.86% cache hit ratio (concerning range: 90–95%)\n  - blks_read = 571 blocks from disk\n  - cache_hits = 7,426 hits from cache\n  - This indicates 7.14% of block accesses required disk I/O\n\n**Root Cause Evidence:**\n1. **test_large_table** (61 MB) shows 3 sequential scans with 0 index scans\n2. **shared_buffers** is set to 5.76 GB (735,488 * 8 KB)\n3. The table size (61 MB) is small relative to shared_buffers (5.76 GB), but the sequential scans still caused elevated cache misses\n4. The postgres database was reset/fault-injected, which cleared table data but left scan statistics\n\n**Why Cache Misses Occurred:**\n- Sequential scans on test_large_table loaded all 61 MB of blocks into the buffer pool\n- This access pattern (full table scans) evicted other frequently-accessed hot pages\n- The 92.86% hit ratio reflects the period after the sequential scans completed and stats were gathered\n- The other databases (alloydbadmin at 99.98%, alloydbmetadata at 99.99%) have normal hit ratios, confirming the issue is specific to the postgres database's workload pattern\n\n**Configuration Assessment:**\n- shared_buffers of 5.76 GB is appropriately sized for a system with sufficient RAM\n- The issue is not undersized shared_buffers, but rather the workload pattern: large sequential scans evict hot data\n\nThe fault injection scenario has run as designed — a large sequential scan filled the buffer pool temporarily, causing elevated cache misses during that period. The system has since stabilized, but the postgres database still shows the residual effects (92.86% hit ratio).\n\nHYPOTHESIS_1: Sequential scans on test_large_table (61 MB) evicted hot data from the 5.76 GB shared_buffers pool, causing elevated cache misses in the postgres database | CONFIDENCE: 0.95 | EVIDENCE: \"table_name | test_large_table\\ntotal_size | 61 MB\\nseq_scan   | 3\\nidx_scan   | 0\" and \"database | postgres\\ncache_hit_ratio | 92.86\\nblocks_read | 571\\ncache_hits | 7426\"\nHYPOTHESIS_2: Undersized shared_buffers is the root cause | CONFIDENCE: 0.05 | REJECTED: shared_buffers is 5.76 GB, which is substantially larger than the 61 MB table causing the miss rate, and sibling databases show healthy 99.98% hit ratios\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "35.940709642s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 1,
      "judge_reasoning": "Agent correctly identified low cache hit ratio (92.86%) caused by sequential scans on test_large_table, properly ruled out undersized shared_buffers using evidence from sibling databases and table size comparison, and appropriately recommended optimizing query access patterns (implicitly through the root cause analysis), while correctly establishing that the issue is workload pattern not buffer pool sizing.",
      "judge_model": "claude-haiku-4-5-20251001",
      "remediation_attempted": true,
      "remediation_passed": true,
      "recovery_time_seconds": 0.192654667,
      "remediation_score": 1,
      "remediation_method": "playbook",
      "overall_score": 1
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

