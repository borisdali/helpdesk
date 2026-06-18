# Informed Consent sample#7 (on a host/VM): Part 2

For the concept definition see the [Informed Consent](INFORMED_CONSENT.md) page. For the background on Fault Injection Testing in aiHelpDesk see [FAULTTEST.md](FAULTTEST.md).

For the background on interactive approvals see [here](PLAYBOOKS.md#interactive-approval-human-in-the-loop-demo).

Simple interactive/approval sample running inside Docker containers is available [here](BENCHMARKING_SAMPLE6.md) and its counterpart for K8s is [here](BENCHMARKING_SAMPLE5.md).

In contrast to the previous samples, this one shows aiHelpDesk running directly on a host/VM. The principles are exactly the same where depending on whether a `--emit-and-wait` parameter is request, the run is considered interactive or not. For the former, the dialog occurs in-line, while the for the latter, the approvals and prompts are presented asynchronously via the [Decision Hub](DECISIONS.md).

The initial [blog post](https://medium.com/@borisdali/you-let-ai-operate-on-production-database-without-your-consent-bd4ffb954266) provides more color on aiHelpDesk [Informed Consent](INFORMED_CONSENT.md) and poses this question: did the AI operate with your consent? This blog post sets the stage for the commands listed below and may be a better starting point if you are less familiar with aiHelpDesk.

This follow up [blog post](...) provides goes a step further and adds an additional question: when you consented, were you right to?

Commands and their output below is used as the supporting documentation for the conclusion presented in the second blog post.

## Let's get started

Fire up aiHelpDesk on a host/VM:

```
[boris@ /tmp/helpdesk/helpdesk-v0.17.0-darwin-arm64]$ ./startall.sh --services-only --governance
Starting helpdesk services...
  auditd (pid 45447) -> /tmp/helpdesk-auditd.log
  database-agent (pid 45449) -> /tmp/helpdesk-database-agent.log
  k8s-agent (pid 45450) -> /tmp/helpdesk-k8s-agent.log
  sysadmin-agent (pid 45451) -> /tmp/helpdesk-sysadmin-agent.log
  incident-agent (pid 45452) -> /tmp/helpdesk-incident-agent.log
  gateway (pid 45454) -> /tmp/helpdesk-gateway.log
Gateway listening on http://localhost:8080
Auditing: enabled  (http://localhost:1199)
Policy:   enabled  (/tmp/helpdesk/helpdesk-v0.17.0-darwin-arm64/./policies.yaml)
Mode:     fix

Starting governance components...
  auditor (pid 45456) -> /tmp/helpdesk-auditor.log
  secbot (pid 45457) -> /tmp/helpdesk-secbot.log

Running headless (--services-only). Press Ctrl-C to stop all services.
```

Leave that running and in a different terminal run a test question through aiHelpDesk client to confirm that aiHelpDesk is configured correctly:

```
[boris@ ~/helpdesk]$ /tmp/helpdesk-client --audit-url http://localhost:1199 --user alice@example.com --purpose diagnostic --message "what dev databases are up, their load and uptime?"
⠴  Thinking…I'll check the status of the dev databases listed in the infrastructure config.
Here's the status of the dev databases:
```

| Database | Status | Version | Uptime | Connections | Cache Hit Ratio |
|----------|--------|---------|--------|-------------|-----------------|
| **test-db** | ✅ UP | PG 16.13 | 16d 7h | 6/100 | 99.97% |
| **alloydb-on-vm** | ✅ UP | PG 17.7 | 16d 7h | 10/100 | 99.99% |
| **pg-cluster-minkube** | ❌ DOWN | — | — | — | — |
| **faulttest-db** | ✅ UP | PG 16.13 | 6h (recently restarted) | 6/100 | 99.73% |

**Summary:**
- **3 out of 4 databases are up** with healthy load (all under 10% connection capacity)
- **test-db** and **alloydb-on-vm** have been running stable for 16+ days with excellent cache hit ratios (99%+)
- **faulttest-db** was recently restarted 6 hours ago and is operating normally
- **pg-cluster-minkube** (Kubernetes CNPG cluster) is unreachable—this may be expected if the Minikube cluster is not currently running

```
Would you like me to investigate why the Kubernetes cluster is down, or do you need more details on any of the running databases?
[trace: tr_3d8c5030-2dd  2026-06-17 20:51:52]
[audit: get_status_summary (read), get_status_summary (read), get_status_summary (read), get_status_summary (read)]
```

## Get the list of available fault injection tests

aiHelpDesk allows BYO faults and playbooks, but here's the list of fault injection tests that we ship and which are available out of the box:

```
[boris@cassiopeia /tmp/helpdesk/helpdesk-v0.17.0-darwin-arm64]$ ./faulttest list
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

## Run the fault injection test

Choose a failure scenario from the above list. In the example below we use `db-tx-lock-chain-blocker`. Here's the full comman to run the full injection test for this scenario:

```
[boris@ /tmp/helpdesk/helpdesk-v0.17.0-darwin-arm64]$ ./faulttest run \
  --ids db-tx-lock-chain-blocker \
  --via-gateway --gateway http://localhost:8080 \
  --api-key $HELPDESK_CLIENT_API_KEY \
  --gate-escalation \
  --audit-url http://localhost:1199 \
  --auto-db 
  --remediate \
  --operator alice@example.com \
  --approval-mode auto
```

Note the use of the `--auto-db` and the absense of `--conn` and `infra-config` flags. For the same of simplicity, we are also not using [LLM-as-Judge](LLM_AS_JUDGE.md), but it can be easily added as we did in the previous examples.

```
Starting temporary PostgreSQL container (postgres:16-alpine)...		<-- that's the result of using --auto-db flag
Auto-DB ready: host=127.0.0.1 port=51630 dbname=faulttest user=postgres password=faulttest sslmode=disable

time=2026-06-17T21:00:15.685-04:00 level=INFO msg="auto-DB registered with gateway" server_id=faulttest-auto-51630

--- Testing: Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) ---
time=2026-06-17T21:00:15.685-04:00 level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external
time=2026-06-17T21:00:18.750-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on      chain), leaves C/D (blocked on chain2)"
time=2026-06-17T21:00:19.264-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_d62510d9 gateway=http://localhost:8080
time=2026-06-17T21:00:39.824-04:00 level=INFO msg="audit evidence" failure=db-tx-lock-chain-blocker tools="[get_active_connections get_session_info get_blocking_queries check_connection]"
time=2026-06-17T21:00:39.825-04:00 level=INFO msg="gate pending: operator review required" failure=db-tx-lock-chain-blocker run_id=plr_16f55166 escalation_target=""
```

And since we are not using `--emit-and-wait`, the informed gate prompts are shown in-line:

```
════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Next playbook     : pbs_lock_chain_remediate
  Findings          : Root blocker PID 63 (active, has_writes=yes); 4-level chain (63→64→66→65); terminate_connection required to close the session and cascade-release downstream locks.
  Remediation plan  : Transaction Lock Chain — Terminate Root Blocker (manual approval)
                      Terminate the root blocker in a transaction lock chain. Handles both simple
(one root, N direct victims) and multi-level chains (root → intermediate →
leaves). The root may be idle-in-transaction or actively executing a statement
(e.g. pg_sleep) that holds an open transaction. Uses terminate_connection
(pg_terminate_backend) — not cancel_query (pg_cancel_backend) — which closes
the connection unconditionally and releases all held locks. In a multi-level
chain, terminating the root triggers a cascade: intermediate sessions acquire
their pending locks and exit, rolling back their own open transactions. If any
intermediate session has uncommitted writes, that work is silently discarded —
the operator must see this before approving.

  Hypotheses        :
    [PRIMARY 100%] PID 63 is an active root blocker executing a long-running sleep operation (pg_sleep(3600)) within an open transaction holding exclusive locks on _faulttest_lock_chain, causing a 4-level downstream lock chain that prevents  all queued queries from executing
    [REJECTED 0%] The lock chain can be resolved by cancel_query on the root blocker, since it will interrupt the pg_sleep — cancel_query sends SIGINT which terminates the sleep but leaves the transaction and all locks open;                  terminate_connection is required to close the session unconditionally and release all held locks.

  Approve remediation? [y/N]: y
  Approval mode [manual/review/auto] (default: review):
  Approval note (optional): LGTM, 100% primary hypotheses is not... too shabby
```

Next section of the run is the optional, but highly recommended feedback:

```
  Feedback (optional, but recommended — recorded before remediation runs):
    Was the triage diagnosis correct? [y/n/skip]: y
    Root cause (Enter to confirm: "PID 63 is an active root blocker executing a long-running sleep operation (pg_sleep(3600)) within an open transaction holding exclusive locks on _faulttest_lock_chain, causing a 4-level downstream lock      chain that prevents all queued queries from executing"):
    Was the proposed remediation appropriate? [y/n/skip]: y
  Feedback submitted (remediation/at_gate run_id=plr_16f55166)
```

We refer to it as at-gate triage and remediation plan feedback. Here's the [page](VAULT_FEEDBACK_FLOW.md#feedback-reference) explaining different types of feedback (triage vs. remediation), the time they are requested (at gate vs. post-incident) and the their meaning.

And the run continues...

```
time=2026-06-17T21:01:34.713-04:00 level=INFO msg="agent_approve: pending step" step_index=1 tool=get_blocking_queries action_class=read reason="Step 1: Verify the prior triage diagnosis by confirming PID 63 is still the root blocker and     obtain the complete chain (63→64→66→65) with blocking_pid values to classify all participants." approval_id=apr_9955d0e8
time=2026-06-17T21:01:40.132-04:00 level=INFO msg="agent_approve: pending step" step_index=2 tool=get_session_info action_class=read reason="Step 2: Inspect root blocker PID 63 to confirm has_open_tx=true, state, has_writes status, and       idle_secs before proceeding to termination." approval_id=apr_2bd0b911
time=2026-06-17T21:01:45.139-04:00 level=INFO msg="agent_approve: pending step" step_index=3 tool=get_session_info action_class=read reason="Inspect intermediate session PID 64 (first blocker in the chain) to check for uncommitted writes     before terminating root PID 63." approval_id=apr_987044bb
time=2026-06-17T21:01:50.011-04:00 level=INFO msg="agent_approve: pending step" step_index=4 tool=get_session_info action_class=read reason="Inspect intermediate session 66 (blocked by 64, blocking 65) to check for uncommitted writes before  cascade termination." approval_id=apr_e0f5a9a8
time=2026-06-17T21:01:53.470-04:00 level=INFO msg="agent_approve: pending step" step_index=5 tool=get_session_info action_class=read reason="Inspect the final leaf session (PID 65) to complete the pre-termination cascade risk assessment      before requesting approval to terminate the root." approval_id=apr_612d1da8
time=2026-06-17T21:01:58.374-04:00 level=INFO msg="agent_approve: pending step" step_index=6 tool=terminate_connection action_class=destructive reason="Step 3: Terminate root blocker PID 63 (blocking_pid=NULL, state=active, has_writes=yes,   idle_secs=86) after confirming cascade impact: intermediate PID 64 has has_writes=true and will roll back ~45s–3m0s of work when root lock releases; intermediates PID 66 and 65 are read-only (instant rollback). Operator must approve cascade  disclosure before proceeding." approval_id=apr_9655ee04
time=2026-06-17T21:02:05.226-04:00 level=INFO msg="agent_approve: pending step" step_index=7 tool=get_blocking_queries action_class=read reason="Step 4: Verify the cascade cleared by confirming no sessions remain in a Lock wait state after   root termination." approval_id=apr_21e4099f
time=2026-06-17T21:02:10.726-04:00 level=INFO msg="agent_approve: remediation complete" summary="Playbook execution complete. Step 1: Confirmed root blocker PID 63 (blocking_pid=NULL) with 4-level chain (63→64→66→65); identified              intermediates 64, 66, 65. Step 2: Inspected root (PID 63: active, has_writes=true, 1m44s idle) and all intermediates (PID 64: has_writes=true; PIDs 66, 65: read-only). Cascade risk disclosed: terminating root will roll back PID 64's          uncommitted writes. Step 3: Terminated root PID 63 via terminate_connection — session terminated successfully. Step 4: Verified cascade cleared — get_blocking_queries returned no blocking queries; lock chain fully released. Remediation       successful."
```

And at this point in the run we reach the post-incident feedback:

```
  Post-incident feedback (optional, but recommended):
    Was the triage diagnosis correct? [y/n/skip]: y
    Root cause (Enter to confirm: "PID 63 is an active root blocker executing a long-running sleep operation (pg_sleep(3600)) within an open transaction holding exclusive locks on _faulttest_lock_chain, causing a 4-level downstream lock      chain that prevents all queued queries from executing"):
  Feedback submitted (triage/post_incident run_id=plr_16f55166)
    Was the remediation approach appropriate? [y/n/skip]: y
    Remediation approach notes (optional): seems like it resolved the problem
  Feedback submitted (remediation/post_incident run_id=plr_16f55166)
```

What remains is just to wrap up the test:

```
Remediation: RECOVERED in 0.1s (score: 100%)
Incident plr_16f55166 — resolved in 0.1s
  Diagnosis  : PID 63 is an active root blocker executing a long-running sleep operation (pg_sleep(3600)) within an open transaction holding exclusive locks on _faulttest_lock_chain, causing a 4-level downstream lock chain that prevents all  queued queries from executing
  Remediation: pbs_lock_chain_remediate
  Narrative  : GET http://localhost:8080/api/v1/incidents/plr_16f55166
Vault: draft saved → pb_6845cd96 (activate with 'faulttest vault list')
time=2026-06-17T21:02:55.617-04:00 level=INFO msg="tearing down failure" id=db-tx-lock-chain-blocker type=shell_exec
time=2026-06-17T21:02:55.738-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n(0 rows)\n\nDROP TABLE\nDROP TABLE"
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% judge=100%)
Remediation Result:  [PASS] score=100% (0.1s, playbook)
Overall Result:      [PASS] score=100%

=== Fault Test Report: 0905bf2f ===

[PASS] Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
       Remediation: 100% (0.1s, playbook) | Overall: 100%

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)
```
