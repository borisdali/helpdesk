# Benchmarking remediation sample#4: Informed Gate

For the background on Fault Injection Testing in aiHelpDesk see [here](FAULTTEST.md).

For the background on interactive approvals see [here](PLAYBOOKS.md#interactive-approval-human-in-the-loop-demo).

The examples below show how running fault injection tests (with interactive approvals and informed gates) may look like while running from source code. Other pages show similar examples for running on a host/VM, in Docker/Podman containers or on K8s.

To begin, start the test PostgreSQL database called faulttest-db (or BYO):

```
[boris@ ~/helpdesk]$ docker compose -f testing/docker/docker-compose.yaml up -d --wait
[+] Running 3/3
 ✔ Network docker_default            Created                                                                                                                                                                                                 0.0s
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 6.5s
 ✔ Container helpdesk-test-pgloader  Healthy                                                                                                                                                                                                 6.4s
```

## Terminal #1 (main test run):

```
[boris@ /tmp/helpdesk/helpdesk-v0.14.0-darwin-arm64]$ grep -A1 alice users.yaml
  - id: alice@example.com
    roles: [dba, dba_lead, sre, operator]

[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest run \
    --ids db-tx-lock-chain-blocker \
    --external \
    --conn faulttest-db-local \
    --via-gateway --gateway http://localhost:8080 \
    --api-key <your-api-key> \
    --operator alice@example.com \
    --infra-config <your infrastructure.json> \
    --remediate --gate-escalation --emit-and-wait --approval-mode review

Wed Jun  3 19:35:03 EDT 2026

--- Testing: Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) ---
time=2026-06-03T19:35:03.569-04:00 level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external
time=2026-06-03T19:35:06.683-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on chain), leaves C/D (blocked on chain2)"
time=2026-06-03T19:35:06.840-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_dc50a3ac gateway=http://localhost:8080
time=2026-06-03T19:35:25.436-04:00 level=INFO msg="gate pending: operator review required" failure=db-tx-lock-chain-blocker run_id=plr_38766666 escalation_target=pbs_lock_chain_remediate

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Escalation target : pbs_lock_chain_remediate
  Findings          : Root blocker PID 42 (active, has_writes=true, executing pg_sleep(3600) with open transaction holding 4 locks); 4-level chain; terminate_connection required to cascade unlock; cancel_query will not release locks.


Gate pending — run_id=plr_38766666
  Escalation target : pbs_lock_chain_remediate
  Findings          : Root blocker PID 42 (active, has_writes=true, executing pg_sleep(3600) with open transaction holding 4 locks); 4-level chain; terminate_connection required to cascade unlock; cancel_query will not release locks.
  Resolve at        : POST http://localhost:8080/api/v1/decisions/gate:plr_38766666/resolve

time=2026-06-03T19:35:40.828-04:00 level=INFO msg="faultlib: gate poll" run_id=plr_38766666 outcome=gate_pending
time=2026-06-03T19:35:56.240-04:00 level=INFO msg="faultlib: gate poll" run_id=plr_38766666 outcome=gate_pending
time=2026-06-03T19:36:11.868-04:00 level=INFO msg="faultlib: gate poll" run_id=plr_38766666 outcome=gate_pending
time=2026-06-03T19:36:27.464-04:00 level=INFO msg="faultlib: gate poll" run_id=plr_38766666 outcome=gate_pending
```

At this point this Fault Injection Test finished the diagnosis stage and reached the [Transition] Gate (see [here](DECISIONS.md) if you are not familiar with the term) and waits for a human to review. This is because the test was requested with `--approval-mode review` flag, so a human operator gets a chance to review the findings and the remediation recommendations and either approve or deny them.

## Terminal #2 (approval terminal) - Gate approval:

In this other, approval terminal, query the list of pending Gates and approve:

```
[boris@ ~/helpdesk]$ curl -s -H "Authorization: Bearer gateway-api-key" "http://localhost:8080/api/v1/decisions?type=gate&status=pending" |jq .
{
  "decisions": [
    {
      "id": "gate:plr_38766666",
      "type": "gate",
      "status": "pending",
      "summary": "Triage complete — ESCALATE_TO pbs_lock_chain_remediate",
      "requested_by": "alice@example.com",
      "requested_at": "2026-06-03T23:35:07Z",
      "expires_at": "0001-01-01T00:00:00Z",
      "resolve_url": "http://localhost:8080/api/v1/decisions/gate:plr_38766666/resolve",
      "extra": {
        "escalation_target": "pbs_lock_chain_remediate",
        "findings": "Root blocker PID 42 (active, has_writes=true, executing pg_sleep(3600) with open transaction holding 4 locks); 4-level chain; terminate_connection required to cascade unlock; cancel_query will not release locks.",
        "gate_type": "escalation",
        "series_id": "pbs_lock_chain_triage"
      }
    }
  ],
  "total": 1
}
```

If LGTM, approve:

```
[boris@cassiopeia ~/cassiopeia/helpdesk]$ curl -s -X POST "http://localhost:8080/api/v1/decisions/gate:plr_38766666/resolve" -H "Authorization: Bearer gateway-api-key" -H "Content-Type: application/json" -d '{"resolution": "approved",        "resolved_by":"alice@example.com"}'
{"run_id":"plr_6fa8db1f","status":"pending_approval","step":{"index":1,"agent":"database","tool":"get_blocking_queries","args":{},"reason":"Verify the blocking chain is still present and confirm PID 42 is the root blocker (blocking_pid =     NULL) before proceeding to inspect session details.","action_class":"read"},"approval_id":"apr_43438cab","effective_approval_mode":"manual"}
```

## Terminal #1 (main test run) - Gate approved, continue...:

Back to the main, Terminal #1, we can almost immediately (with up to 10s polling interval delay) see that the approval has been received and the test is now progressing:

```
time=2026-06-03T19:37:13.414-04:00 level=INFO msg="faultlib: gate poll" run_id=plr_38766666 outcome=gate_pending
time=2026-06-03T19:37:28.877-04:00 level=INFO msg="faultlib: gate poll" run_id=plr_38766666 outcome=escalated
time=2026-06-03T19:37:32.016-04:00 level=INFO msg="agent_approve: pending step" step_index=0 tool=get_blocking_queries action_class=read reason="" approval_id=apr_43438cab
time=2026-06-03T19:37:35.283-04:00 level=INFO msg="agent_approve: pending step" step_index=2 tool=get_blocking_queries action_class=read reason="Initial attempt did not include connection_string parameter; retrying with explicit database connection credentials to identify the root blocker and transaction lock chain." approval_id=apr_5e63eecd
time=2026-06-03T19:37:39.072-04:00 level=INFO msg="agent_approve: pending step" step_index=3 tool=get_session_info action_class=read reason="Step 2: Inspect the root blocker (PID 42, blocking_pid=NULL) to confirm has_open_tx=true, state, has_writes status, and idle_secs before proceeding to termination." approval_id=apr_dcf30d01
time=2026-06-03T19:37:43.092-04:00 level=INFO msg="agent_approve: pending step" step_index=4 tool=get_session_info action_class=read reason="Step 2 requires inspection of all intermediate sessions; PID 51 is an intermediate (blocking_pid=42, blocks PID 52) and must be checked for has_writes before proceeding to termination." approval_id=apr_957ae47c
time=2026-06-03T19:37:46.902-04:00 level=INFO msg="agent_approve: pending step" step_index=5 tool=get_session_info action_class=read reason="Step 1 classification identified PID 52 as an intermediate (blocked by 51, blocks 53); Step 2 requires inspecting all intermediate sessions before acting." approval_id=apr_b69208ea
time=2026-06-03T19:37:50.699-04:00 level=INFO msg="agent_approve: pending step" step_index=6 tool=get_session_info action_class=read reason="Step 2 requires inspection of ALL intermediate sessions before acting; PID 53 is a leaf (blocked by 52) and must be checked for has_writes status to complete cascade risk assessment." approval_id=apr_62743bdc
time=2026-06-03T19:37:55.770-04:00 level=INFO msg="agent_approve: step approval pending — waiting for external resolution" step_index=7 tool=terminate_connection approval_id=apr_fe6bab23
time=2026-06-03T19:38:06.189-04:00 level=INFO msg="faultlib: step approval poll" approval_id=apr_fe6bab23 status=pending
time=2026-06-03T19:38:16.904-04:00 level=INFO msg="faultlib: step approval poll" approval_id=apr_fe6bab23 status=pending
```

## Terminal #2 (approval terminal) - Step approval:

The test at this point is at the pending approval stage again (for the second time), but this time it's not because of a Transition Gate (which only shows up at the playbook boundary to give a chance for an Operator to review the diagnostic findings), but because during the Remediation stage, one of the tools called by the playbook, happened to be a destructive one, in this case it's `terminate_connection` one. Hence the request for the second, so called `step` approval:

```
[boris@ ~/helpdesk]$ curl -s -H "Authorization: Bearer gateway-api-key" "http://localhost:8080/api/v1/decisions?status=pending&type=step_approval" |jq .
{
  "decisions": [
    {
      "id": "step:apr_fe6bab23",
      "type": "step_approval",
      "status": "pending",
      "summary": "destructive database/terminate_connection",
      "requested_by": "faulttest",
      "requested_at": "2026-06-03T23:37:55.769003Z",
      "expires_at": "2026-06-04T00:37:55.768981Z",
      "resolve_url": "http://localhost:8080/api/v1/decisions/step:apr_fe6bab23/resolve",
      "extra": {
        "action_class": "destructive",
        "agent": "database",
        "resource_name": "",
        "resource_type": "",
        "run_id": "plr_6fa8db1f",
        "tool": "terminate_connection"
      }
    },
    {
      "id": "step:apr_62743bdc",
      "type": "step_approval",
      "status": "pending",
      "summary": "destructive database/get_session_info",
      "requested_by": "faulttest",
      "requested_at": "2026-06-03T23:37:50.698551Z",
   ...

[boris@ ~/helpdesk]$ curl -s -X POST "http://localhost:8080/api/v1/decisions/step:apr_fe6bab23/resolve" -H "Authorization: Bearer gateway-api-key" -H "Content-Type: application/json" -d '{"resolution": "approved", "resolved_by":"alice@example.com"}'
{"status":"approved"}
```

## Terminal #1 (main test run) - Step approved, finish the run:

Back to the main terminal with the Fault Injection Test that is now unblocked and contiues all the way to the end:

```
time=2026-06-03T19:41:04.023-04:00 level=INFO msg="faultlib: step approval poll" approval_id=apr_fe6bab23 status=pending
time=2026-06-03T19:41:14.474-04:00 level=INFO msg="faultlib: step approval poll" approval_id=apr_fe6bab23 status=pending
time=2026-06-03T19:41:24.912-04:00 level=INFO msg="faultlib: step approval poll" approval_id=apr_fe6bab23 status=approved
time=2026-06-03T19:41:24.912-04:00 level=INFO msg="agent_approve: step approval resolved" approval_id=apr_fe6bab23 resolution=approved
time=2026-06-03T19:41:31.528-04:00 level=INFO msg="agent_approve: pending step" step_index=8 tool=get_blocking_queries action_class=read reason="Step 4: Verify that terminating root PID 42 has cleared the lock chain and no sessions remain in Lock wait state." approval_id=apr_9da6c257

  Remediation complete: Transaction lock chain successfully resolved. Root blocker PID 42 (blocking_pid=NULL, state=active, has_writes=true) was terminated. Cascade affected intermediates PID 51 (has_writes=true) and PID 52 (has_writes=false, read-only). Verification Step 4 confirms no blocking queries remain—all sessions cleared, lock chain dissolved.

Remediation: RECOVERED in 0.1s (score: 100%)
Vault: draft saved → pb_d874b826 (activate with 'faulttest vault list')
time=2026-06-03T19:41:50.463-04:00 level=INFO msg="tearing down failure" id=db-tx-lock-chain-blocker type=shell_exec
time=2026-06-03T19:41:50.539-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n(0 rows)\n\nDROP TABLE\nDROP TABLE"
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% judge=100%)
Remediation Result:  [PASS] score=100% (0.1s, playbook)
Overall Result:      [PASS] score=100%

=== Fault Test Report: 15d9bf5f ===

[PASS] Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
       Remediation: 100% (0.1s, playbook) | Overall: 100%

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

Report written to ./faulttest-15d9bf5f.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "15d9bf5f",
  "timestamp": "2026-06-03T23:41:50Z",
  "results": [
    {
      "failure_id": "db-tx-lock-chain-blocker",
      "failure_name": "Transaction lock chain — active root blocker (pg_sleep trap)",
      "category": "database",
      "score": 1,
      "passed": true,
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "tool_evidence_mode": "structured",
      "response_text": "I'll begin investigating the connection failure and any blocking transactions on the target database.\nPerfect. The database is reachable and I can see a clear multi-level lock chain. Now I'll examine the root blocker (PID 42) in detail to confirm its state and held locks.\nExcellent. I now have complete evidence of the lock chain and root blocker. Let me present the diagnosis:\n\n---\n\n## Lock Chain Analysis\n\n**Chain Structure:**\n- **PID 42** (root blocker): executing `SELECT pg_sleep(3600)` — state: active, no blocking_pid (blocking_pid is NULL)\n  - Holds 4 locks including _faulttest_lock_chain and _faulttest_lock_chain_pkey\n  - Has open transaction (11s old, has_writes=true)\n  \n- **PID 51** (level 1 victim): blocked by PID 42\n  - Attempting: `SELECT * FROM _faulttest_lock_chain WHERE id=1 FOR UPDATE`\n  - Waiting on transactionid lock (5.9s blocked)\n  \n- **PID 52** (level 2 victim): blocked by PID 51\n  - Attempting: `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE`\n  - Waiting on transactionid lock (4.9s blocked)\n  \n- **PID 53** (level 3 victim): blocked by PID 52\n  - Attempting: `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE`\n  - Waiting on tuple lock (4.9s blocked)\n\nThis is a 4-level blocking chain. The root blocker (PID 42) is executing `pg_sleep(3600)` with an open transaction that holds row locks. The sleep statement is active and blocking an entire cascade of dependent queries.\n\n---\n\n## Why cancel_query Will Not Work\n\nThe root blocker (PID 42) is in state='active' executing `pg_sleep(3600)`. While `cancel_query` appears attractive because a query is executing, PostgreSQL's behavior is:\n\n1. `cancel_query` sends SIGINT to the backend.\n2. `pg_sleep()` is interruptible and will be cancelled.\n3. **However**, the open transaction remains active and all 4 held locks persist.\n4. The downstream sessions (51, 52, 53) will remain blocked indefinitely, waiting on locks that are still held by the now-idle PID 42.\n\nThis is a critical misdiagnosis trap: cancelling the sleep query does NOT release the transaction or its locks. The lock chain does NOT clear.\n\n---\n\n## Why terminate_connection Is Required\n\nOnly `terminate_connection` closes the connection unconditionally via SIGTERM, which:\n1. Immediately closes the TCP connection to PID 42\n2. Triggers rollback of the open transaction\n3. Releases all 4 held locks\n4. Unblocks PID 51 → which then completes and releases its locks\n5. Triggers a cascade release of PID 52's locks\n6. Unblocks PID 53\n\n---\n\nHYPOTHESIS_1: Root blocker (PID 42) holds a multi-level lock chain via an active pg_sleep statement with an open transaction; cancel_query will not release locks because the transaction remains open; terminate_connection is required | CONFIDENCE: 0.99 | EVIDENCE: \"State: active (11s in current state), Open TX age: 11s, Has writes: yes, Locked tables: _faulttest_lock_chain, _faulttest_lock_chain_pkey, Total locks: 4, Last query: SELECT pg_sleep(3600);\"\n\nHYPOTHESIS_2: Terminating intermediate victims (PID 51, 52, or 53) will resolve the lock chain | CONFIDENCE: 0.0 | REJECTED: Intermediate victims have blocking_pid values (51→42, 52→51, 53→52); terminating them frees only downstream sessions temporarily; the root blocker PID 42 retains all locks and any replacement session requeues immediately.\n\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "18.751330417s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 1,
      "remediation_attempted": true,
      "remediation_passed": true,
      "recovery_time_seconds": 0.0782795,
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

real	6m47.392s
user	0m0.344s
sys	0m0.546s
```
