# Benchmarking diagnosis sample#2: Crystal Ball vs. the full power of aiHelpDesk

For the background on Fault Injection Testing in aiHelpDesk see [here](FAULTTEST.md). The Crystal Ball mode is presented [here](BENCHMARKING.md#2-crystal-ball-mode).

This is an example showcasing a comparison of diagnosis of one specific database fault by two different means. First, we run the diagnosis via the Crystal Ball mode. We then follow on with the full aiHelpDesk diagnosis according to the structured playbook guidance. The specific fault used in this example is a lock chain blocking downstream transactions (but also see [another example](BENCHMARKING_SAMPLE1.md) that demonstrates the checkpoint stall / bgwriter overload fault).

If you are not familiar with the Crystal Ball mode, these two blog posts [here](https://medium.com/google-cloud/dont-ask-your-ai-to-diagnose-production-unless-you-ve-given-it-a-structured-guided-playbook-46195c2aae71) and [here](https://medium.com/google-cloud/we-wanted-a-dramatic-ai-agent-failure-we-got-something-better-5d6d57135a88) go beyond our standard documentation and present a gentle introduction and the thinking behind this comparison.

## Crystal Ball diagnosis
In the examples below we inject the transaction lock chain fault test (dubbed `db-tx-lock-chain-blocker` in our [fault catalog](FAULTTEST.md#61-external-compatible-faults)) and attempt to diagnose this fault by running the Crystal Ball and the full aihelpDesk playbook-driven diagnosis. In this particular example we run both from the source code (but in the [latter section](BENCHMARKING_SAMPLE.md#running-on-k8s-via-a-helm-chart) in this document we show equivalent commands for running this test on K8s via Helm).

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault list|egrep '(FAULT|---|lock-chain)'
FAULT                            PLATFORM   DIAG PLAYBOOK              REMED PLAYBOOK             FAULT TEST             INCIDENTS
---------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-tx-lock-chain-blocker         any        pbs_lock_chain_triage      pbs_lock_chain_remediate   2026-05-23  PASS       -
```

First off, start aiHelpDesk Gateway in Crystal Ball mode (to build a comparison baseline): 

```
[boris@ ~/helpdesk]$ date; HELPDESK_CRYSTAL_BALL=true \ <-- do NOT set this var for real Production troubleshooting/remediation
  TEST_DB_PASSWORD=<your DB password> \
  HELPDESK_AGENT_API_KEY=<your API key if you run aiHelpDesk in static authn mode> \ <-- required if the HELPDESK_OPERATING_MODE=fix and HELPDESK_IDENTITY_PROVIDER=static, see https://github.com/borisdali/helpdesk/blob/main/docs/IDENTITY.md#2-identity-provider
  HELPDESK_AUDIT_API_KEY=<similar to the above and in the minimal, non-Prod setting it can be set to the same key> \
  HELPDESK_MODEL_VENDOR=anthropic \
  HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001 \
  HELPDESK_API_KEY=<your Anthropic key> \
  HELPDESK_INFRA_CONFIG=<your infra.json, e.g. ./deploy/docker-compose/infrastructure.json> \
  HELPDESK_OPERATING_MODE=fix \
  HELPDESK_IDENTITY_PROVIDER=static \
  HELPDESK_GATEWAY_ADDR=localhost:8080 \
  HELPDESK_AGENT_URLS="http://localhost:1100,http://localhost:1102,http://localhost:1103,http://localhost:1104,http://localhost:1106" \
  HELPDESK_AUDIT_URL=http://localhost:1199 \
  HELPDESK_AUDIT_ENABLED=true \
  HELPDESK_USERS_FILE=users.example.yaml \
go run ./cmd/gateway
```

The above Gateway incantation is for running tests from the source. If running on one of the three supported deployment platforms (directly on a host, VM or Bare Metal, inside a Docker/Podman container or on K8s), the Gateway runs automatically and is controlled by the platform specific mechanism (.env file, env vars or Helm chart's values.yaml).

When the Gateway starts, it may be a good idea to check the log to ensure that the Crystal Ball mode is on (easy to spot by the yellow warning sign):

```
time=2026-05-23T12:47:47.993-04:00 level=WARN msg="⚠️  CRYSTAL-BALL MODE ENABLED — playbook guidance, hypothesis format, and escalation chaining are bypassed. NOT recommended for production use."
```

Once the Gateway and the agents are up, run the test:

```
[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest run 
  --ids db-tx-lock-chain-blocker \
  --db-agent http://localhost:8080 \
  --via-gateway --gateway http://localhost:8080 \
  --judge --judge-model claude-haiku-4-5-20251001 --judge-vendor anthropic --judge-api-key=$ANTHROPIC_API_KEY \
  --external \
  --conn faulttest-db --infra-config ~/cassiopeia/claude/infrastructure.json \
  --remediate --approval-mode manual

--- Testing: Transaction lock chain blocker — two-level lock chain (db-tx-lock-chain-blocker) ---
time=2026-05-23T12:58:47.935-04:00 level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external
time=2026-05-23T12:58:51.039-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on      chain), leaves C/D (blocked on chain2)"
time=2026-05-23T12:58:51.394-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_blocker_triage playbook_id=pb_5bebf0cc gateway=http://localhost:8080
time=2026-05-23T12:59:20.755-04:00 level=WARN msg="⚠  crystal-ball mode active on gateway — playbook scaffolding is bypassed; this result measures unguided LLM capability only"
time=2026-05-23T12:59:22.741-04:00 level=INFO msg="tearing down failure" id=db-tx-lock-chain-blocker type=shell_exec
time=2026-05-23T12:59:22.829-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n t\n t\n t\n(4 rows)\n\nDROP TABLE\nDROP TABLE"
Diagnostic Result:   [PASS] score=73% (keywords=100% tools=66% judge=67%)

=== Fault Test Report: b25c8e6f ===

[PASS] Transaction Lock Chain Triage / Terminate Root Blocker — two-level lock chain (db-tx-lock-chain-blocker) - score: 73% ⚠ [crystal-ball] [judge: 67%]
       Keywords: 100% | Tools: 66% | Judge: 67%
       Reasoning: "Agent correctly identified PID 88455 as the root blocker and the two-level lock chain structure, but failed to recognize the critical trap that cancel_query is ineffective (it interrupts pg_sleep but leaves the transaction and row-lock persisted in idle-in-transaction-aborted state), and incorrectly presented cancel_query as a viable immediate solution rather than emphasizing that only terminate_connection on PID 88455 fully resolves the cascade."

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

Note: 1 fault(s) ran in crystal-ball mode — playbook scaffolding was bypassed.
Results measure unguided LLM capability only and are not comparable to guided runs.

LLM judge scored diagnosis for 1 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-b25c8e6f.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "b25c8e6f",
  "timestamp": "2026-05-23T16:59:22Z",
  "results": [
    {
      "failure_id": "db-tx-lock-chain-blocker",
      "failure_name": "Transaction Lock Chain Triage / Terminate Root Blocker — two-level lock chain",
      "category": "database",
      "score": 0.7346666666666668,
      "passed": true,
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "tool_evidence_mode": "structured",
      "response_text": "I'll investigate the blocking issue systematically. Let me start by checking the connection and then examining lock contention.\nExcellent! I've identified the root cause. Let me get more detailed blocking information:\n## Summary of Findings\n\nI've identified **a classic lock chain deadlock scenario** causing all the blocking:\n\n### The Lock Chain (PID → blocks → PID):\n\n1. **PID 88455** (root blocker) — Running `SELECT pg_sleep(3600)` \n   - Status:  ACTIVE for ~24 seconds\n   - Holds: Locks on `_faulttest_lock_chain` and `_faulttest_lock_chain_pkey`\n   - Has writes in its transaction\n\n2. **PID 88457** (blocked by 88455, blocks 88458)\n   - Query: `SELECT * FROM _faulttest_lock_chain  WHERE id=1 FOR UPDATE`\n   - Waiting: **22.8 seconds** for transactionid ExclusiveLock held by PID 88455\n   - Holds: Locks on `_faulttest_lock_chain2` and `_faulttest_lock_chain2_pkey`\n\n3. **PID 88458** (blocked by 88457, blocks           88459)\n   - Query: `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE`\n   - Waiting: **21.8 seconds** for transactionid ExclusiveLock held by PID 88457\n   - No writes (read-only)\n\n4. **PID 88459** (blocked by 88458)\n   -       Query: `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE`\n   - Waiting: **21.8 seconds** for tuple lock on `_faulttest_lock_chain2`\n\n### Root Cause\n\n**PID 88455 is the culprit** — it started a transaction, acquired locks on    `_faulttest_lock_chain`, and then called `pg_sleep(3600)` (a 1-hour sleep), leaving the transaction open. This blocks PID 88457, which in turn blocks PID 88458, which blocks PID 88459.\n\nThis appears to be an intentional **lock chain test** (based on the table names `_faulttest_lock_chain*`), but the problem is that PID 88455 will sleep for the full hour unless interrupted.\n\n### Recommendation\n\nTo unblock the other sessions, you have two options:\n\n**Option 1               (Immediate):** Cancel the `pg_sleep()` in PID 88455\n- This will let PID 88455's transaction complete and release its locks\n- The cascade of blocked sessions will then proceed\n\n**Option 2 (If you want to reset):** Terminate PID 88455      entirely\n- This will roll back its transaction (estimated 12–48 seconds)\n- All downstream sessions will be unblocked\n\nI can execute either action with your confirmation. Which would you prefer?",
      "duration": "29.714884s",
      "keyword_score": 1,
      "tool_score": 0.6666666666666666,
      "diagnosis_score": 0.67,
      "judge_reasoning": "Agent correctly identified PID 88455 as the root blocker and the two-level lock chain structure, but failed to recognize the critical trap that cancel_query is ineffective (it interrupts pg_sleep but leaves the      transaction and row-lock persisted in idle-in-transaction-aborted state), and incorrectly presented cancel_query as a viable immediate solution rather than emphasizing that only terminate_connection on PID 88455 fully resolves the cascade.",
      "judge_model": "claude-haiku-4-5-20251001",
      "crystal_ball": true,
      "overall_score": 0.7346666666666668
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

It's easier for humans to read the diagnosis by formatting the above JSON output. It's not difficult to automate this or just copy and paste the section between the BEGIN/END FAULTTEST REPORT JSON tags to a file (we call it /tmp/reason.json in the excerpt below) and run it through a simple Python snippet as follows:

```
[boris@ ~/helpdesk]$ vi /tmp/reason.json
[boris@ ~/helpdesk]$ cat /tmp/reason.json | python3 -c "
   import json, sys
   d = json.load(sys.stdin)
   r = d['results'][0]
   print(r['response_text'])
"
I'll investigate the blocking issue systematically. Let me start by checking the connection and then examining lock contention.
Excellent! I've identified the root cause. Let me get more detailed blocking information:
## Summary of Findings

I've identified **a classic lock chain deadlock scenario** causing all the blocking:

### The Lock Chain (PID → blocks → PID):

1. **PID 88455** (root blocker) — Running `SELECT pg_sleep(3600)`
   - Status: ACTIVE for ~24 seconds
   - Holds: Locks on `_faulttest_lock_chain` and `_faulttest_lock_chain_pkey`
   - Has writes in its transaction

2. **PID 88457** (blocked by 88455, blocks 88458)
   - Query: `SELECT * FROM _faulttest_lock_chain WHERE id=1 FOR UPDATE`
   - Waiting: **22.8 seconds** for transactionid ExclusiveLock held by PID 88455
   - Holds: Locks on `_faulttest_lock_chain2` and `_faulttest_lock_chain2_pkey`

3. **PID 88458** (blocked by 88457, blocks 88459)
   - Query: `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE`
   - Waiting: **21.8 seconds** for transactionid ExclusiveLock held by PID 88457
   - No writes (read-only)

4. **PID 88459** (blocked by 88458)
   - Query: `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE`
   - Waiting: **21.8 seconds** for tuple lock on `_faulttest_lock_chain2`

### Root Cause

**PID 88455 is the culprit** — it started a transaction, acquired locks on `_faulttest_lock_chain`, and then called `pg_sleep(3600)` (a 1-hour sleep), leaving the transaction open. This blocks PID 88457, which in turn blocks PID 88458, which blocks PID 88459.

This appears to be an intentional **lock chain test** (based on the table names `_faulttest_lock_chain*`), but the problem is that PID 88455 will sleep for the full hour unless interrupted.

### Recommendation

To unblock the other sessions, you have two options:

**Option 1 (Immediate):** Cancel the `pg_sleep()` in PID 88455
- This will let PID 88455's transaction complete and release its locks
- The cascade of blocked sessions will then proceed

**Option 2 (If you want to reset):** Terminate PID 88455 entirely
- This will roll back its transaction (estimated 12–48 seconds)
- All downstream sessions will be unblocked

I can execute either action with your confirmation. Which would you prefer?
```

Please note that this is just one field from the full JSON, which is `response_text`. The others fields may also be instructive, in particular `judge_reasoning` (if a test runs with the LLM-as-Judge option, see [here](https://github.com/borisdali/helpdesk/blob/main/docs/LLM_AS_JUDGE.md) for details).


## Full, guided, structured aiHelpDesk diagnosis
To run the normal, guided, structured aiHelpDesk diagnosis and remediation Crystal Ball has to be off, either not set or set to false: `HELPDESK_CRYSTAL_BALL=false`. The rest is exactly the same. Once the Gateway is restarted with this env var set, run the same fault injection test in exactly the same way and review the results:

```
[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest run \
  --ids db-tx-lock-chain-blocker \
  --db-agent http://localhost:8080 \
  --via-gateway --gateway http://localhost:8080 \
  --judge --judge-model claude-haiku-4-5- 20251001 --judge-vendor anthropic --judge-api-key=$ANTHROPIC_API_KEY \
  --external \
  --conn faulttest-db --infra-config ~/cassiopeia/claude/infrastructure.json \
  --remediate --approval-mode force
Sat May 23 13:41:15 EDT 2026
time=2026-05-23T13:41:18.212-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-05-23T13:41:18.212-04:00 level=INFO msg="LLM judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: Transaction Lock Chain Triage / Terminate Root Blocker — two-level lock chain (db-tx-lock-chain-blocker) ---
time=2026-05-23T13:41:18.212-04:00 level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external
time=2026-05-23T13:41:21.320-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on      chain), leaves C/D (blocked on chain2)"
time=2026-05-23T13:41:21.748-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_5bebf0cc gateway=http://localhost:8080
time=2026-05-23T13:41:43.594-04:00 level=INFO msg="starting remediation" failure=db-tx-lock-chain-blocker playbook=pbs_lock_chain_remediate agent_prompt=false
time=2026-05-23T13:41:47.017-04:00 level=INFO msg="agent_approve: pending step" step_index=1 tool=get_blocking_queries args="map[connection_string:host=localhost port=15432 dbname=testdb user=postgres password=testpass dbname:testdb host:    localhost password:testpass port:15432 user:postgres]" reason="Step 1: Identify the blocking chain, classify root/intermediate/leaf sessions, and record all PIDs before proceeding to inspection." approval_id=apr_0d7d6401
time=2026-05-23T13:41:52.294-04:00 level=INFO msg="agent_approve: pending step" step_index=2 tool=get_session_info args="map[connection_string:host=localhost port=15432 dbname=testdb user=postgres password=testpass pid:92612]" reason="Step 1 identified PID 92612 as the root blocker (blocking_pid = NULL); now Step 2 requires inspecting its state, has_open_tx, has_writes, and idle_secs before any termination decision." approval_id=apr_bf9de6a0
time=2026-05-23T13:41:56.754-04:00 level=INFO msg="agent_approve: pending step" step_index=3 tool=get_session_info args="map[connection_string:host=localhost port=15432 dbname=testdb user=postgres password=testpass pid:92621]" reason="Step 1 identified 92621 as an intermediate (blocking_pid=92612, but also blocks 92622); Step 2 requires inspection of all intermediate sessions before acting." approval_id=apr_e2ae4071
time=2026-05-23T13:42:00.872-04:00 level=INFO msg="agent_approve: pending step" step_index=4 tool=get_session_info args="map[connection_string:host=localhost port=15432 dbname=testdb user=postgres password=testpass pid:92622]" reason="Step 1 requires inspection of all intermediate sessions before termination; PID 92622 is an intermediate (blocked by 92621, but also blocks 92623) and must be checked for has_writes status to assess cascade risk." approval_id=apr_1a35a2b6
time=2026-05-23T13:42:06.838-04:00 level=INFO msg="agent_approve: pending step" step_index=5 tool=terminate_connection args="map[connection_string:host=localhost port=15432 dbname=testdb user=postgres password=testpass pid:92612 reason:Root  blocker (blocking_pid=NULL, idle in transaction, has_writes=true, 36s idle). Terminating root will cascade to roll back session 92621 (has_writes=true, 39s idle) — operator should confirm this work can be discarded before approving. Session  92622 has no writes (read-only, instant rollback). WARNING: terminating root will cascade to roll back session 92621 (has_writes=true, 39s idle) — operator should confirm this work can be discarded before approving.]" reason="Step 3:         Terminate the root blocker (PID 92612) with explicit cascade disclosure of intermediate sessions with uncommitted writes." approval_id=apr_030c80c6
time=2026-05-23T13:42:12.266-04:00 level=INFO msg="agent_approve: pending step" step_index=6 tool=get_blocking_queries args="map[connection_string:host=localhost port=15432 dbname=testdb user=postgres password=testpass dbname:testdb host:    localhost password:testpass port:15432 user:postgres]" reason="Step 4: Verify the cascade cleared by confirming no Lock wait states remain after root blocker termination." approval_id=apr_bf86221e
time=2026-05-23T13:42:15.748-04:00 level=INFO msg="agent_approve: remediation complete" summary="Root blocker PID 92612 (idle in transaction, has_writes=true) was successfully terminated. Cascade rollback of intermediate session 92621        (has_writes=true) occurred as expected. Session 92622 (read-only) rolled back instantly. Verification via get_blocking_queries confirms no blocking queries remain — all lock waits have been cleared."
Remediation: RECOVERED in 0.1s (score: 100%)
time=2026-05-23T13:42:16.183-04:00 level=WARN msg="vault: could not generate playbook draft" fault=db-tx-lock-chain-blocker err="gateway returned 422: {\"error\":\"trace \\\"faulttest-74b503c0-db-tx-lock-chain-blocker\\\" contains no tool          execution events: nothing to synthesize from\"}\n"
time=2026-05-23T13:42:16.183-04:00 level=INFO msg="tearing down failure" id=db-tx-lock-chain-blocker type=shell_exec
time=2026-05-23T13:42:16.228-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n(0 rows)\n\nDROP TABLE\nDROP TABLE"
Diagnostic Result:   [PASS] score=86% (keywords=100% tools=100% judge=67%)
Remediation Result:  [PASS] score=100% (0.1s, playbook)
Overall Result:      [PASS] score=92%

=== Fault Test Report: 74b503c0 ===
[PASS] Transaction Lock Chain Triage / Terminate Root Blocker — two-level lock chain (db-tx-lock-chain-blocker) - score: 86% [judge: 67%]
       Keywords: 100% | Tools: 100% | Judge: 67%
       Remediation: 100% (0.1s, playbook) | Overall: 92%
       Reasoning: "Agent correctly identified PID 92612 as the root blocker and the two-level lock chain structure, but failed to explicitly address why cancel_query is ineffective (the critical trap mentioned in expected diagnosis) and did  not emphasize that terminate_connection is the only solution—the statement 'cancel_query could interrupt the pg_sleep itself, the locks would persist' is present but buried and not clearly positioned as a reason to reject cancel_query as a   primary recommendation."

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

LLM judge scored diagnosis for 1 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-74b503c0.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "74b503c0",
  "timestamp": "2026-05-23T17:42:16Z",
  "results": [
    {
      "failure_id": "db-tx-lock-chain-blocker",
      "failure_name": "Transaction Lock Chain Triage / Terminate Root Blocker — two-level lock chain",
      "category": "database",
      "score": 0.8680000000000001,
      "passed": true,
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "tool_evidence_mode": "structured",
      "response_text": "I'll begin diagnosing the unavailability of the target database. Let me start by testing connectivity and then gather evidence on the lock contention.\nGood — the database is reachable. Now I have evidence of a multi- level lock chain. Let me analyze it:\n\n- PID 92621 (blocked by 92612) is waiting on transactionid, running `SELECT * FROM _faulttest_lock_chain WHERE id=1 FOR UPDATE;`\n- PID 92622 (blocked by 92621) is waiting on transactionid, running     `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE;`\n- PID 92623 (blocked by 92622) is waiting on tuple lock for _faulttest_lock_chain2, running `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE;`\n- PID 92612 (blocking   92621) is running `SELECT pg_sleep(3600);` — this is the root blocker\n\nThe root blocker is **PID 92612** (has no blocking_pid in the result set). Let me inspect it now.\nInteresting. The session shows:\n- State: **active** (not idle in     transaction)\n- Has writes: **yes**\n- Locked tables: _faulttest_lock_chain, _faulttest_lock_chain_pkey\n- Total locks: 4\n- Running: `SELECT pg_sleep(3600);` (will sleep for 1 hour)\n\nThis is a deliberate fault injection test scenario      where an active session is holding locks while sleeping. The multi-level lock chain indicates:\n\n1. PID 92612 (root blocker): Active, holding locks on _faulttest_lock_chain, running `SELECT pg_sleep(3600)` (sleeps until lock is              released)\n2. PID 92621: Blocked waiting for 92612's transaction to complete\n3. PID 92622: Blocked waiting for 92621\n4. PID 92623: Blocked waiting for 92622\n\nThis creates a cascading deadlock scenario where upstream sessions are waiting  on locks held by the root blocker.\n\nLet me get more details on the active connections to confirm this picture:\nPerfect. The active connections confirm the lock chain. All sessions are in **active** state (not idle in transaction). The     root blocker is PID 92612, which:\n- Is running `SELECT pg_sleep(3600);` (will continue for ~1 hour)\n- Holds ExclusiveLock on transactions (indicated by the transactionid wait events)\n- Is directly blocking PID 92621\n- Indirectly blocks   PIDs 92622 and 92623 through the chain\n\nThe database is unavailable because:\n1. PID 92612 is holding locks while sleeping for 3600 seconds\n2. All other sessions trying to access the locked tables are queued up behind it\n3. This is a     cascading lock chain where each blocked session itself becomes a blocker\n\n**Analysis:**\n\nThe root cause is **PID 92612** holding locks in an active transaction while executing `SELECT pg_sleep(3600)`. This is a fault injection test that  deliberately creates a lock bottleneck. The session is **active** (not idle in transaction), so it's currently executing the pg_sleep function. However, it holds locks that prevent downstream sessions from proceeding.\n\nTo resolve this, the root blocker must be terminated. While `cancel_query` could interrupt the pg_sleep itself, the locks would persist if the session remains open. The correct action is `terminate_connection` to close the session and release all held locks,     which will cascade and unblock all downstream sessions.\n\nHYPOTHESIS_1: Root blocker (PID 92612) is holding locks while executing pg_sleep(3600), creating a cascading lock chain that prevents all downstream sessions from acquiring needed    locks and making the database unavailable | CONFIDENCE: 0.95 | EVIDENCE: \"blocking_pid: 92612, blocking_query: SELECT pg_sleep(3600), locked_tables: _faulttest_lock_chain, Total locks: 4, blocked_duration increasing for all downstream       sessions\"\nHYPOTHESIS_2: Intermediate victim misidentification (treating 92621 or 92622 as root) | CONFIDENCE: 0.00 | REJECTED: get_blocking_queries clearly shows no blocking_pid for 92612; all others have blocking_pid pointing upstream,    confirming 92612 as sole root\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "20.119631958s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 0.67,
      "judge_reasoning": "Agent correctly identified PID 92612 as the root blocker and the two-level lock chain structure, but failed to explicitly address why cancel_query is ineffective (the critical trap mentioned in expected diagnosis)   and did not emphasize that terminate_connection is the only solution—the statement 'cancel_query could interrupt the pg_sleep itself, the locks would persist' is present but buried and not clearly positioned as a reason to reject             cancel_query as a primary recommendation.",
      "judge_model": "claude-haiku-4-5-20251001",
      "remediation_attempted": true,
      "remediation_passed": true,
      "recovery_time_seconds": 0.064533416,
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

And here's the formatted version for human's comsumption:

```
[boris@cassiopeia ~/cassiopeia/helpdesk]$ vi /tmp/reason.json
[boris@cassiopeia ~/cassiopeia/helpdesk]$ cat /tmp/reason.json | python3 -c "
   import json, sys
   d = json.load(sys.stdin)
   r = d['results'][0]
   print(r['response_text'])
"
I'll begin diagnosing the unavailability of the target database. Let me start by testing connectivity and then gather evidence on the lock contention.
Good — the database is reachable. Now I have evidence of a multi-level lock chain. Let me analyze it:

- PID 92621 (blocked by 92612) is waiting on transactionid, running `SELECT * FROM _faulttest_lock_chain WHERE id=1 FOR UPDATE;`
- PID 92622 (blocked by 92621) is waiting on transactionid, running `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE;`
- PID 92623 (blocked by 92622) is waiting on tuple lock for _faulttest_lock_chain2, running `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE;`
- PID 92612 (blocking 92621) is running `SELECT pg_sleep(3600);` — this is the root blocker

The root blocker is **PID 92612** (has no blocking_pid in the result set). Let me inspect it now.
Interesting. The session shows:
- State: **active** (not idle in transaction)
- Has writes: **yes**
- Locked tables: _faulttest_lock_chain, _faulttest_lock_chain_pkey
- Total locks: 4
- Running: `SELECT pg_sleep(3600);` (will sleep for 1 hour)

This is a deliberate fault injection test scenario where an active session is holding locks while sleeping. The multi-level lock chain indicates:

1. PID 92612 (root blocker): Active, holding locks on _faulttest_lock_chain, running `SELECT pg_sleep(3600)` (sleeps until lock is released)
2. PID 92621: Blocked waiting for 92612's transaction to complete
3. PID 92622: Blocked waiting for 92621
4. PID 92623: Blocked waiting for 92622

This creates a cascading deadlock scenario where upstream sessions are waiting on locks held by the root blocker.

Let me get more details on the active connections to confirm this picture:
Perfect. The active connections confirm the lock chain. All sessions are in **active** state (not idle in transaction). The root blocker is PID 92612, which:
- Is running `SELECT pg_sleep(3600);` (will continue for ~1 hour)
- Holds ExclusiveLock on transactions (indicated by the transactionid wait events)
- Is directly blocking PID 92621
- Indirectly blocks PIDs 92622 and 92623 through the chain

The database is unavailable because:
1. PID 92612 is holding locks while sleeping for 3600 seconds
2. All other sessions trying to access the locked tables are queued up behind it
3. This is a cascading lock chain where each blocked session itself becomes a blocker

**Analysis:**

The root cause is **PID 92612** holding locks in an active transaction while executing `SELECT pg_sleep(3600)`. This is a fault injection test that deliberately creates a lock bottleneck. The session is **active** (not idle in transaction),  so it's currently executing the pg_sleep function. However, it holds locks that prevent downstream sessions from proceeding.

To resolve this, the root blocker must be terminated. While `cancel_query` could interrupt the pg_sleep itself, the locks would persist if the session remains open. The correct action is `terminate_connection` to close the session and        release all held locks, which will cascade and unblock all downstream sessions.

HYPOTHESIS_1: Root blocker (PID 92612) is holding locks while executing pg_sleep(3600), creating a cascading lock chain that prevents all downstream sessions from acquiring needed locks and making the database unavailable | CONFIDENCE: 0.95  | EVIDENCE: "blocking_pid: 92612, blocking_query: SELECT pg_sleep(3600), locked_tables: _faulttest_lock_chain, Total locks: 4, blocked_duration increasing for all downstream sessions"
HYPOTHESIS_2: Intermediate victim misidentification (treating 92621 or 92622 as root) | CONFIDENCE: 0.00 | REJECTED: get_blocking_queries clearly shows no blocking_pid for 92612; all others have blocking_pid pointing upstream, confirming     92612 as sole root
ROOT_CAUSE: HYPOTHESIS_1
```

A few things to note from this run:
Teardown found (0 rows), which means that the remediation genuinely cleared the fault before teardown ran, confirming the 100% score is real this time.

The step 5 reason field is the story:
```
        reason: "Root blocker (blocking_pid=NULL, idle in transaction, has_writes=true, 36s idle).
        Terminating root will cascade to roll back session 92621 (has_writes=true, 39s idle) —
        operator should confirm this work can be discarded before approving."
```

That's what agent_approve surfaces before the operator clicks approve. Crystal Ball terminated silently. This is the cascade safety gap made visible.

The step proposer is also classifying the chain correctly. It found 92622 as another intermediate (it holds the tuple lock that 92623 is queued behind), so it inspected three sessions before terminating. That's correct PostgreSQL lock queue behavior.

The judge 67% diagnosis is slightly unfair because the agent DID say "cancel_query could interrupt the pg_sleep itself, the locks would persist if the session remains open" but the judge called it "buried."
The judge gave Crystal Ball the same 67% despite Crystal Ball recommending cancel as "Option 1 (Immediate)" with a wrong description. Same score for opposite quality.

Also note the structured way of presenting the diagnosis: the step-by-step analysis, the list of hypotheses supported by evidence and the confidence score, the reason for rejecting the alternative hypotheses and choosing one as the root cause (there happened to be no escalation section in this particular example, but you'll see it in many others where the RCA is inconclusive).

## Diagnosis comparison: unconstrained Crystal Ball vs. guided, structured, playbook driven aiHelpDesk

The numbers that matter for the comparison:

```
  ┌─────────────────────────────┬──────────────┬─────────────┐
  │                             │ Crystal Ball │   Guided    │
  ├─────────────────────────────┼──────────────┼─────────────┤
  │ Diagnosis                   │ 73%          │ 86%         │
  ├─────────────────────────────┼──────────────┼─────────────┤
  │ Remediation                 │ —            │ 100%        │
  ├─────────────────────────────┼──────────────┼─────────────┤
  │ Presented cancel as viable  │ ✓ (Option 1) │ ✗           │
  ├─────────────────────────────┼──────────────┼─────────────┤
  │ Cascade WARNING in approval │ ✗            │ ✓           │
  ├─────────────────────────────┼──────────────┼─────────────┤
  │ Teardown found sessions     │ 4 remaining  │ 0 remaining │
  └─────────────────────────────┴──────────────┴─────────────┘
```

The judge score being equal (67%) for both diagnoses is worth a notice. The judge is evaluating surface-level diagnosis quality, but the real differentiation is in the remediation path, which the judge doesn't see at all.

The remediation step sequence in the full aiHelpDesk diagnosis is right on the money:

tool=get_blocking_queries         Step 1:  map the chain
tool=get_session_info pid=92612   Step 2a: inspect root
tool=get_session_info pid=92621   Step 2b: inspect intermediate (has_writes=true)
tool=get_session_info pid=92622   Step 2c: inspect second intermediate
tool=terminate_connection         Step 3:  terminate with cascade WARNING
tool=get_blocking_queries         Step 4:  verify cleared

And the terminate_connection reason field is exactly what the playbook guidance specifies:

```
  	Root blocker (blocking_pid=NULL, has_writes=true, 36s idle).
  	Terminating root will cascade to roll back session 92621 (has_writes=true, 39s idle) —
  	operator should confirm this work can be discarded before approving.
  	Session 92622 has no writes (read-only, instant rollback).
```

That's the operator-facing approval request. Everything the human needs to make an informed decision, in the right format, before anything is executed.


## Running on K8s (via a Helm Chart)

The examples above, both via the structured aiHelpDesk guided playbook and via Crystal Ball, were done from the code source. Here's how the invocation of this fault test looks like on K8s. In the example below I ran the same `db-tx-lock-chain-blocker` failure scenario against the CNPG cluster called `pg-cluster-minikube` deployed in a `ns=db` on Minikube:

```
helm upgrade helpdesk . --namespace helpdesk-system \
  --set faulttest.enabled=true \
  --set faulttest.ids=db-tx-lock-chain-blocker \
  --set faulttest.judge=true \
  --set faulttest.viaGateway=true \
  --set faulttest.gatewayAPIKeySecret=<your API key> \
  --set faulttest.conn=<a fully qualified conn string or an alias from your infra.json file> \
  --set 'infrastructure.db_servers.pg-cluster-minikube.connection_string=host=pg-cluster-minikube-rw.db.svc.cluster.local port=5432 dbname=app user=app' \
  --set 'infrastructure.db_servers.pg-cluster-minikube.password_env=CNPG_APP_PASSWORD' \
  --set 'faulttest.extraEnv[0].name=CNPG_APP_PASSWORD' \
  --set 'faulttest.extraEnv[0]. valueFrom.secretKeyRef.name=pg-cluster-minkube-app' \
  --set 'faulttest.extraEnv[0].valueFrom.secretKeyRef.key=password' \
  --set image.pullPolicy=Never \
  --set image.tag=v0.13.0-a71a4ba \
  --timeout 15m \
  --set gateway.crystalBall=true \	<-- that's the setting you want to flip to false (or remove) for the normal, guided aiHelpDesk diagnosis
  --set faulttest.targetNamespace=db 
```

## Reproducability and differences in scoring
It's worth pointing out that the original listings and the comparison table were obtained by running the tests from the source against a PostgreSQL 16 database server running inside a Docker container and diagnosed by Haiku Anthropic models use by both the agents and the judge. In testing, we found that the version of PostgreSQL, the differences in models, perhaps the hosting platform, but most importantly the "mood" of the models affect the diagnosis significantly. But only the Crystal Ball diagnosis. 

The structured, guided aiHelpDesk diagnosis remains stable irrespective of all those deployment and environmenental factors. This, on its own, is a very important finding because consistency is the key for building a trust in AI-driven diagnosis.

> aiHelpDesk features a fully-fledged, 8-module [AI Governance](AIGOVERNANCE.md) suite as the basis for building the user trust, so that ALL decisions can be fully traced, explained and survive the most stringent audit. 
> But this is after the fact.  **The consistency in diagnosis** that comes from the structured guidance is another important piece in the trust puzzle.
