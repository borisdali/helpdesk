# aiHelpDesk Sample#8 (on a K8s): Triage Consistency Certification badge + AI Ledger

The sample commands presented below augment the following two blog posts:

- **[The LLM Is the Dumbest Part of Your AI Operations Platform](https://medium.com/google-cloud/the-llm-is-the-dumbest-part-of-your-ai-operations-platform-1ac95039cacd)**
  Why model-neutrality isn’t a nice-to-have. It’s the only architecture that survives a production contract. We’ve been saying this for a while now: models are quickly becoming a (disposable) commodity. Here’s how:

- **[AI Fixed Your Database. Here Are the Receipts](https://medium.com/@borisdali/ai-fixed-your-database-here-are-the-receipts-543b145b135e)**
  The black box is now a ledger. Don't settle for anything less than an Informed Consent and 100% auditable track record of all the AI decisions that led to a problem diagnosis and remediation


For the background on Triage Consistency Certification see [here](CONSISTENCY.md). This Consistency Certification/Badge is part of the greater aiHelpDesk Operational SRE/DBA Flywheel - see [here](VAULT.md#the-operational-sredba-flywheel) for details.

The sample commands below are shown on K8s, but similar samples of running aiHelpDesk in Docker/Podman and on a host/VM available [here](BENCHMARKING_SAMPLE6.md) and [here](BENCHMARKING_SAMPLE7.md) respectively (although not the exact commands shown here).

## Fault Injection Test

aiHelpDesk Fault Injection Testing is [well documented](FAULTTEST.md), with multiple [examples availble](FAULTTEST_SAMPLE.md) on [K8s](BENCHMARKING_SAMPLE5.md), [Docker/Podman](BENCHMARKING_SAMPLE6.md) and on a [host/VM](BENCHMARKING_SAMPLE7.md). On K8s however we mostly showed how to run `faulttest` via Helm, while in this case we directly create a K8s Job via `kubectl`:

```
[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ kubectl run faulttest-demo \
    --image=ghcr.io/borisdali/helpdesk:v0.18.0 \
    --image-pull-policy=Never \
    --restart=Never \
    --namespace=helpdesk-system \
    --env="HELPDESK_MODEL_VENDOR=anthropic" \
    --env="HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001" \
    --env="HELPDESK_API_KEY=$HELPDESK_API_KEY" \
   -- faulttest run \
         --conn "host=pg-cluster-minkube-rw.db.svc.cluster.local port=5432 dbname=app user=app password=<your password> sslmode=disable" \
         --ids db-max-connections \
         --remediate \
         --gate-escalation \
         --emit-and-wait \
         --approval-mode force \
         --judge \
         --judge-vendor anthropic \
         --judge-model claude-haiku-4-5-20251001 \
         --remediation-judge \
         --report-per-fault \
         --gateway http://helpdesk-gateway:8080 \
         --api-key $HELPDESK_CLIENT_API_KEY
pod/faulttest-demo created

[boris@cassiopeia ~]$ kubectl logs -f -n helpdesk-system faulttest-demo
time=2026-06-26T00:20:07.646Z level=INFO msg=--conn host=pg-cluster-minkube-rw.db.svc.cluster.local

--- Testing: Max connections exhausted (db-max-connections) ---
time=2026-06-26T00:20:07.647Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-06-26T00:20:07.647Z level=INFO msg="LLM diagnosis judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-06-26T00:20:07.647Z level=INFO msg="LLM remediation judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-06-26T00:20:07.647Z level=INFO msg="injecting failure" id=db-max-connections type=shell_exec mode=external conn=pg-cluster-minkube-rw.db.svc.cluster.local
time=2026-06-26T00:20:11.105Z level=INFO msg="shell_exec completed" output="Injected: 96 idle connections (1 existing → 97/100)"
time=2026-06-26T00:20:11.825Z level=INFO msg="sending prompt to agent via playbook" failure=db-max-connections series_id=pbs_connection_triage playbook_id=pb_34be5ebc gateway=http://helpdesk-gateway:8080 agent-conn="host=pg-cluster-minkube-  rw.db.svc.cluster.local port=5432 dbname=app user=app password=VGv7ZSF58adBmURXRpeA5ZVBrd2zrlub4BPMrf8oBcHkEqDdogLvCQI9CP0bz5AH sslmode=disable"
time=2026-06-26T00:20:43.033Z level=WARN msg="gateway warning" failure=db-max-connections warning="approval_mode clamped to \"manual\": override to \"force\" requires one of roles [dba_lead oncall_senior] (caller: gateway)"
time=2026-06-26T00:20:44.626Z level=INFO msg="gate pending: operator review required" failure=db-max-connections run_id=plr_264f28fc escalation_target=""

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Next playbook     : pbs_connection_remediate
  Findings          : Connection pool saturation at 108/100 (108%) with 96 idle connections waiting on client; recommend reducing application pool size to <85 connections or implementing idle connection timeout; increasing max_connections    requires memory reallocation and should be second-order remediation
  Remediation plan  : Connection Overload — Terminate Idle Sessions (manual approval)
                      Remediate connection pool exhaustion or a session stuck in idle-in-transaction
by terminating the sessions responsible. Idle connections waste slots without
doing work; idle-in-transaction sessions hold locks and prevent autovacuum.
Both require operator confirmation before termination because an uncommitted
idle-in-transaction session may contain significant writes.

  Hypotheses        :
    [PRIMARY 95%] Application connection pool is misconfigured or oversized relative to max_connections (100); 96 idle connections in the app database indicate the pool is holding connections indefinitely without timeout-based cleanup
    [REJECTED 5%] Long-running transaction preventing connection cleanup — get_blocking_queries returned "No blocking queries found" and all 96 idle connections show no open transactions (wait_event='ClientRead' indicates they are awaiting   client input, not holding locks)


Gate pending — run_id=plr_264f28fc
  Resolve at        : POST http://helpdesk-gateway:8080/api/v1/decisions/gate:plr_264f28fc/resolve
  Body fields:
    resolution        : "approved" | "denied"
    resolved_by       : your email or user ID
    approval_mode     : "auto" | "review" | "manual" (default: playbook setting)
    reason            : optional — free-text operator comment
    verdict_correct : true | false  (triage/at_gate feedback, before remediation runs)
    verdict_notes   : string        (required when verdict_correct=false)

  Remediation feedback (separate call, same gate window):
    POST http://helpdesk-gateway:8080/api/v1/fleet/playbook-runs/plr_264f28fc/feedback
    {"feedback_type":"remediation","feedback_time":"at_gate","verdict_correct":true,"verdict_notes":"..."}

time=2026-06-26T00:20:59.811Z level=INFO msg="faultlib: gate poll" run_id=plr_264f28fc outcome=gate_pending
time=2026-06-26T00:21:15.534Z level=INFO msg="faultlib: gate poll" run_id=plr_264f28fc outcome=gate_pending
```

This is aiHelpDesk [Informed Gate](INFORMED_CONSENT.md). It's optional and is triggered via the `--gate-escalation` parameter. [Decision Hub](DECISIONS.md) is a central place that accumulates and tracks requests for approval. Let's assume that the diagnosis looks reasonable and os is the proposed remediation plan. Let's provide a quick feedback on the at-gate diagnosis (helps improve the triage playbooks) and approve the remediation plan:

```
[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ curl -s -X POST http://localhost:8080/api/v1/fleet/playbook-runs/plr_264f28fc/feedback \
    -H "Authorization: Bearer $APIKEY" \
    -H "Content-Type: application/json" \
    -d '{"feedback_type":"remediation","feedback_time":"at_gate","verdict_correct":true, "verdict_notes":"LGTM, plan looks appropriate"}' | jq .
{
  "run_id": "plr_264f28fc",
  "feedback_type": "remediation",
  "feedback_time": "at_gate",
  "series_id": "pbs_connection_triage",
  "verdict_correct": true,
  "verdict_notes": "LGTM, plan looks appropriate",
  "operator": "",
  "submitted_at": "2026-06-26T00:22:06.519317219Z"
}

[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ curl -s -X POST http://localhost:8080/api/v1/decisions/gate:plr_264f28fc/resolve \
    -H "Authorization: Bearer $APIKEY" \
    -H "Content-Type: application/json" \
    -d '{"resolution":"approved", "resolved_by":"boris@borisdali.com", "verdict_correct":true, "reason":"diagnosis looks right, proceed with remediation"}' | jq .
{
  "run_id": "plr_1ea628e2",
  "status": "pending_approval",
  "step": {
    "index": 1,
    "agent": "database",
    "tool": "get_active_connections",
    "args": {
      "connection_string": "host=pg-cluster-minkube-rw.db.svc.cluster.local port=5432 dbname=app user=app password=VGv7ZSF58adBmURXRpeA5ZVBrd2zrlub4BPMrf8oBcHkEqDdogLvCQI9CP0bz5AH sslmode=disable"
    },
    "reason": "Step 1: Confirm the nature of the overload by counting connections by state (idle, idle in transaction, idle in transaction aborted) to verify the prior triage finding of 96 idle connections and determine which remediation     path to follow.",
    "action_class": "read"
  },
  "approval_id": "apr_a28f28a5",
  "effective_approval_mode": "manual"
}
```

Good, now flipping back to the terminal running the fault injection test, we can see that the things started to move along:

```
time=2026-06-26T00:22:02.307Z level=INFO msg="faultlib: gate poll" run_id=plr_264f28fc outcome=gate_pending
time=2026-06-26T00:22:18.323Z level=INFO msg="faultlib: gate poll" run_id=plr_264f28fc outcome=gate_pending
time=2026-06-26T00:22:33.805Z level=INFO msg="faultlib: gate poll" run_id=plr_264f28fc outcome=transitioned
time=2026-06-26T00:22:34.504Z level=INFO msg="agent_approve: pending step" step_index=0 tool=get_active_connections action_class=read reason="" approval_id=apr_a28f28a5
time=2026-06-26T00:22:44.028Z level=INFO msg="agent_approve: pending step" step_index=2 tool=terminate_idle_connections action_class=destructive reason="Step 1 confirmed 50+ idle connections (state='idle') are causing the 108/100 connection  pool overload; dry_run=true preview before requesting approval to terminate idle connections older than 5 minutes." approval_id=apr_3ae03a82
time=2026-06-26T00:22:54.332Z level=INFO msg="agent_approve: pending step" step_index=3 tool=terminate_idle_connections action_class=destructive reason="The idle_minutes=5 dry run found zero connections (all are under 5 minutes old, likely   from a load test), so per guidance Step 2A we must now execute with idle_minutes=0 to terminate all idle connections regardless of age, which will free up connection slots from the pool saturation event." approval_id=apr_3337652b
time=2026-06-26T00:23:02.308Z level=INFO msg="agent_approve: pending step" step_index=4 tool=get_active_connections action_class=read reason="Step 3: Verify that the 96 idle connections were successfully terminated and that total connection  count is now below max_connections - 5 headroom." approval_id=apr_f75363de
time=2026-06-26T00:23:12.228Z level=INFO msg="agent_approve: pending step" step_index=5 tool=get_connection_stats action_class=read reason="Step 3: Verify that total active connections are now below max_connections - 5 after terminating 96   idle connections to confirm sufficient headroom has been freed." approval_id=apr_4fde30f7
time=2026-06-26T00:23:19.513Z level=INFO msg="agent_approve: remediation complete" summary="Playbook executed successfully. Step 1: Confirmed 96 idle connections (state='idle') causing 108/100 connection pool saturation. Step 2A: Terminated  all 96 idle connections (all were under 5 minutes old, newly created by pool). Step 3: Verified remediation—connection count dropped from 108 to 12 total (1 active in app database, 11 system connections), now well below max_connections=100   with adequate headroom. Step 4: Root cause confirmed as oversized application connection pool not returning connections on close. Recommend reducing application pool max_size to <85 and implementing idle connection timeout in the pool        configuration."

  Feedback pending — resolve at:
  POST http://helpdesk-gateway:8080/api/v1/decisions/feedback:plr_264f28fc/resolve
  Body fields:
    resolution  : "approved" (diagnosis correct) | "denied" (diagnosis wrong)
    resolved_by : your email or user ID
    reason      : actual root cause (required when resolution="denied")

  Waiting for feedback (10m timeout, Ctrl+C to skip)...
  Feedback received — thank you.

Remediation: RECOVERED in 0.1s (score: 100%)
Incident plr_264f28fc — resolved in 0.1s
  Diagnosis  : Application connection pool is misconfigured or oversized relative to max_connections (100); 96 idle connections in the app database indicate the pool is holding connections indefinitely without timeout-based cleanup
  Remediation: pbs_connection_remediate
  Narrative  : GET http://helpdesk-gateway:8080/api/v1/incidents/plr_264f28fc
Vault: draft saved → pb_356a3eb8 (activate with 'faulttest vault list')
  Feedback submitted (triage/post_incident run_id=plr_264f28fc)
time=2026-06-26T00:23:51.324Z level=INFO msg="tearing down failure" id=db-max-connections type=shell_exec conn=pg-cluster-minkube-rw.db.svc.cluster.local
time=2026-06-26T00:23:53.373Z level=INFO msg="shell_exec completed" output="Teardown: idle connections terminated"
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% judge=100%)
Remediation Result:  [PASS] score=100% (0.1s, playbook)
Overall Result:      [PASS] score=100%
Fault report: ./faulttest-3c439baf-db-max-connections.json

=== Fault Test Report: 3c439baf ===

[PASS] Max connections exhausted (db-max-connections) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Remediation: 100% (0.1s, playbook) | Overall: 100%
       Reasoning: "Agent correctly identified max_connections limit (100) exceeded by idle sessions (108 total, 96 idle in app database), diagnosed connection pool misconfiguration as root cause, and implicitly recommended connection cleanup and pool reconfiguration, fully addressing all expected diagnosis points."

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

LLM judge scored diagnosis for 1 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.
```

There's a lot ot unpack here, including the automatic post-incident approval (we call it [`auto-judge`](VAULT.md#vault-accuracy)) when `--aproval-mode=force` and `--judge` flags are requested togetherlike in the example above, but it's zoom-in on the Incident concept first:

## List of Incidents (for a specific failure scenario)

Here's the full list of incidents for a specific fault, `db-max-connections` in this example:

```
[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ kubectl run vault-incidents \
  --image=ghcr.io/borisdali/helpdesk:v0.18.0 \
  --image-pull-policy=Never \
  --restart=Never \
  --namespace=helpdesk-system \
  -- faulttest vault incidents \
     --gateway http://helpdesk-gateway:8080 \
     --api-key $HELPDESK_CLIENT_API_KEY \
     db-max-connections
pod/vault-incidents created

[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk] kubectl logs -n helpdesk-system vault-incidents
Incidents for db-max-connections (pbs_connection_triage) — 8 runs

RUN ID          STARTED           DIAG        REMEDIATION       FEEDBACK      SCORE  FINDINGS
─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
plr_264f28fc    2026-06-26 00:20  transitioned  resolved 1m       ✓ correct     100%   Connection pool saturation at 108/100...
plr_ea3f8fda    2026-06-25 23:05  transitioned  resolved 46.0s    ✓ correct     92%    connections 97/100 (97%); blocker=non...
plr_3afc1023    2026-06-25 21:43  transitioned  resolved 27.0s    ✓ correct     100%   connections 97/100 (97%); blocker=non...
plr_52fab62d    2026-06-25 20:38  transitioned  resolved 48.0s    ✓ correct     100%   connections 97/100 (97%); blocker=non...
plr_edb3b635    2026-06-24 04:43  transitioned  resolved 4m       ✓ correct     –      The application connection pool is ho...
plr_4406375b    2026-06-24 04:27  transitioned  resolved 4m       ✓ correct     52%    connections 97/100 (97%); blocker=non...
plr_9658cbfb    2026-06-24 03:57  transitioned  resolved 4m       ✓ correct     –      connections 97/100 (97%); blocker=non...
plr_cfcce531    2026-06-24 03:39  transitioned  resolved 4m       ✓ correct     60%    connections 97/100 (97%); blocker=non...

To submit feedback:
  curl -sX POST http://helpdesk-gateway:8080/api/v1/decisions/feedback:<run_id>/resolve \
    -H 'Authorization: Bearer $API_KEY' -H 'Content-Type: application/json' \
    -d '{"resolution": "approved", "resolved_by": "you@example.com", "reason": "<root cause>"}'
  (resolution=approved → correct, resolution=denied → wrong diagnosis)
```

## Vault Incidents: Details, the full narrative of an incident

```
[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ kubectl run vault-incidents2 \
  --image=ghcr.io/borisdali/helpdesk:v0.18.0 \
  --image-pull-policy=Never \
  --restart=Never \
  --namespace=helpdesk-system \
  -- faulttest vault incidents \
     --gateway http://helpdesk-gateway:8080 \
     --api-key $HELPDESK_CLIENT_API_KEY \
     plr_264f28fc
pod/vault-incidents2 created

[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ kubectl logs -n helpdesk-system vault-incidents2

════════════════════════════════════════════════════════════
INCIDENT plr_264f28fc
Started: 2026-06-26 00:20 UTC   Duration: 187s
════════════════════════════════════════════════════════════

── TRIAGE
Playbook:  pbs_connection_triage
Findings:  Connection pool saturation at 108/100 (108%) with 96 idle connections
           waiting on client; recommend reducing application pool size to <85
           connections or implementing idle connection timeout; increasing
           max_connections requires memory reallocation and should be
           second-order remediation

Hypotheses:
  [PRIMARY  95%] Application connection pool is misconfigured or oversized relative to max_connections (100); 96 idle connections in the app database indicate the pool is holding connections indefinitely without timeout-based cleanup
                 Evidence: "total_connections=108\" exceeds \"max_connections=100\"; \"idle=96\" with all waiting on \"ClientRead\"; no blocking queries or locks detected"
  [REJECTED  5%] Long-running transaction preventing connection cleanup
                 Rejected: get_blocking_queries returned "No blocking queries found" and all 96 idle connections show no open transactions (wait_event='ClientRead' indicates they are awaiting client input, not holding locks)

── GATE
Decision:  approved by boris@borisdali.com  at 00:22 UTC
Feedback:
  remediation at gate:         ✓ correct (plan looks appropriate)
  triage at gate:              ✓ correct

── REMEDIATION
Playbook:  pbs_connection_remediate   Outcome: resolved
Plan:      Playbook executed successfully. Step 1: Confirmed 96 idle connections
           (state='idle') causing 108/100 connection pool saturation. Step 2A:
           Terminated all 96 idle connections (all were under 5 minutes old,
           newly created by pool). Step 3: Verified remediation—connection
           count dropped from 108 to 12 total (1 active in app database, 11
           system connections), now well below max_connections=100 with adequate
           headroom. Step 4: Root cause confirmed as oversized application
           connection pool not returning connections on close. Recommend reducing
           application pool max_size to <85 and implementing idle connection
           timeout in the pool configuration.
Steps:     ✓   ✓   ✓   ✓   ✓

── EVALUATION
Score:         100%   (diagnosis 100% · remediation 100%)
Diagnosis:     1.00 (LLM judge)   Agent confidence: 95%

── POST-INCIDENT FEEDBACK
  triage:                      ✓ correct (Agent correctly identified max_connections limit (100) exceeded by idle sessions (108 total, 96 idle in app database), diagnosed connection pool misconfiguration as root cause, and implicitly         recommended connection cleanup and pool reconfiguration, fully addressing all expected diagnosis points.)
```

## Triage Consistency Certification/Badge testing

The command to run the consistency cert testing is almost identical to the normal fault injection test with just the last clause of `--repeat N` added at the end. Since the consistency tests run only on triage and not remediation, the remediation parameters can actually be skipped altogether (and the first line of the `Run 1/N` log clearly states that too), but they don't hurst and for consistency with the previous examples, it's easier to use the same command:

```
[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ kubectl run faulttest-demo \
    --image=ghcr.io/borisdali/helpdesk:v0.18.0 \
    --image-pull-policy=Never \
    --restart=Never \
    --namespace=helpdesk-system \
    --env="HELPDESK_MODEL_VENDOR=anthropic" \
    --env="HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001" \
    --env="HELPDESK_API_KEY=$HELPDESK_API_KEY" \
   -- faulttest run \
         --conn "host=pg-cluster-minkube-rw.db.svc.cluster.local port=5432 dbname=app user=app password=<your password> sslmode=disable" \
         --ids db-max-connections \
         --remediate \
         --gate-escalation \
         --emit-and-wait \
         --approval-mode force \
         --judge \
         --judge-vendor anthropic \
         --judge-model claude-haiku-4-5-20251001 \
         --remediation-judge \
         --report-per-fault \
         --gateway http://helpdesk-gateway:8080 \
         --api-key $HELPDESK_CLIENT_API_KEY \
         --repeat 3
pod/faulttest-demo created


[boris@cassiopeia ~]$ kubectl logs -f -n helpdesk-system faulttest-demo
time=2026-06-26T02:00:47.805Z level=INFO msg=--conn host=pg-cluster-minkube-rw.db.svc.cluster.local
time=2026-06-26T02:00:47.806Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-06-26T02:00:47.806Z level=INFO msg="LLM diagnosis judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-06-26T02:00:47.806Z level=INFO msg="LLM remediation judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: Max connections exhausted (db-max-connections) — 3 runs ---

  Run 1/3
time=2026-06-26T02:00:47.806Z level=WARN msg="--remediate is disabled in --repeat mode" repeat=3
time=2026-06-26T02:00:47.806Z level=INFO msg="injecting failure" id=db-max-connections type=shell_exec mode=external conn=pg-cluster-minkube-rw.db.svc.cluster.local
time=2026-06-26T02:00:51.914Z level=INFO msg="shell_exec completed" output="Injected: 96 idle connections (1 existing → 97/100)"
time=2026-06-26T02:00:52.257Z level=INFO msg="sending prompt to agent via playbook" failure=db-max-connections series_id=pbs_connection_triage playbook_id=pb_34be5ebc gateway=http://helpdesk-gateway:8080 agent-conn="host=pg-cluster-minkube-  rw.db.svc.cluster.local port=5432 dbname=app user=app password=VGv7ZSF58adBmURXRpeA5ZVBrd2zrlub4BPMrf8oBcHkEqDdogLvCQI9CP0bz5AH sslmode=disable"
time=2026-06-26T02:01:16.920Z level=WARN msg="gateway warning" failure=db-max-connections warning="approval_mode clamped to \"manual\": override to \"force\" requires one of roles [dba_lead oncall_senior] (caller: gateway)"
  Feedback submitted (triage/post_incident run_id=plr_19a775c3)
time=2026-06-26T02:01:18.720Z level=INFO msg="tearing down failure" id=db-max-connections type=shell_exec conn=pg-cluster-minkube-rw.db.svc.cluster.local
time=2026-06-26T02:01:20.776Z level=INFO msg="shell_exec completed" output="Teardown: idle connections terminated"
time=2026-06-26T02:01:20.776Z level=INFO msg="injecting failure" id=db-max-connections type=shell_exec mode=external conn=pg-cluster-minkube-rw.db.svc.cluster.local
  [PASS] score=86%
         [PRIMARY 95%] Connection pool saturation — idle pooled connections consume 97 of 100 available slots, leaving no room for new connection requests
         [REJECTED 5%] Blocking transaction preventing connection release — No blocking queries found; all idle connections are in ClientRead state waiting on client, not on locks

  Run 2/3
time=2026-06-26T02:01:23.865Z level=INFO msg="shell_exec completed" output="Injected: 96 idle connections (1 existing → 97/100)"
time=2026-06-26T02:01:24.045Z level=INFO msg="sending prompt to agent via playbook" failure=db-max-connections series_id=pbs_connection_triage playbook_id=pb_34be5ebc gateway=http://helpdesk-gateway:8080 agent-conn="host=pg-cluster-minkube-  rw.db.svc.cluster.local port=5432 dbname=app user=app password=VGv7ZSF58adBmURXRpeA5ZVBrd2zrlub4BPMrf8oBcHkEqDdogLvCQI9CP0bz5AH sslmode=disable"
time=2026-06-26T02:01:43.643Z level=WARN msg="gateway warning" failure=db-max-connections warning="approval_mode clamped to \"manual\": override to \"force\" requires one of roles [dba_lead oncall_senior] (caller: gateway)"
time=2026-06-26T02:01:45.352Z level=INFO msg="tearing down failure" id=db-max-connections type=shell_exec conn=pg-cluster-minkube-rw.db.svc.cluster.local
  Feedback submitted (triage/post_incident run_id=plr_f85e2c74)
  [PASS] score=86%
         [PRIMARY 99%] Connection pool leak — application connections not being properly closed, accumulating in idle state exceeding max_connections
         [REJECTED 5%] Long-running transaction holding connection open — get_blocking_queries returned no blocking queries and only 1 active connection; idle transaction holding is ruled out

  Run 3/3
time=2026-06-26T02:01:47.412Z level=INFO msg="shell_exec completed" output="Teardown: idle connections terminated"
time=2026-06-26T02:01:47.412Z level=INFO msg="injecting failure" id=db-max-connections type=shell_exec mode=external conn=pg-cluster-minkube-rw.db.svc.cluster.local
time=2026-06-26T02:01:50.818Z level=INFO msg="shell_exec completed" output="Injected: 96 idle connections (1 existing → 97/100)"
time=2026-06-26T02:01:51.936Z level=INFO msg="sending prompt to agent via playbook" failure=db-max-connections series_id=pbs_connection_triage playbook_id=pb_34be5ebc gateway=http://helpdesk-gateway:8080 agent-conn="host=pg-cluster-minkube-  rw.db.svc.cluster.local port=5432 dbname=app user=app password=VGv7ZSF58adBmURXRpeA5ZVBrd2zrlub4BPMrf8oBcHkEqDdogLvCQI9CP0bz5AH sslmode=disable"
time=2026-06-26T02:02:20.349Z level=WARN msg="gateway warning" failure=db-max-connections warning="approval_mode clamped to \"manual\": override to \"force\" requires one of roles [dba_lead oncall_senior] (caller: gateway)"
time=2026-06-26T02:02:22.736Z level=INFO msg="tearing down failure" id=db-max-connections type=shell_exec conn=pg-cluster-minkube-rw.db.svc.cluster.local
  Feedback submitted (triage/post_incident run_id=plr_e70f10f0)
  [PASS] score=100%
         [PRIMARY 95%] Connection pool is creating too many simultaneous connections and not releasing them efficiently, exceeding max_connections=100.
         [REJECTED 5%] A long-running transaction is blocking connection cleanup, causing accumulation. — All idle session samples show "No open transaction" and no blocking queries detected.

  Stability report (3 runs):
    Pass rate:    3/3 (100%)
    Confidence:   min=95% max=99% range=4pp mean=96%  (H1, passing runs only)
    Verdict:      STABLE
  ────────────────────────────────────────────────────────────────
time=2026-06-26T02:02:24.783Z level=INFO msg="shell_exec completed" output="Teardown: idle connections terminated"
time=2026-06-26T02:02:25.516Z level=INFO msg="fault stability cert posted" fault_id=db-max-connections verdict=STABLE n_runs=3
Fault report: ./faulttest-7823e755-db-max-connections.json

=== Fault Test Report: 7823e755 ===

[PASS] Max connections exhausted (db-max-connections) - score: 86% [judge: 67%]
       Keywords: 100% | Tools: 100% | Judge: 67%
       Reasoning: "Agent correctly identified that idle connections are consuming the connection slots and diagnosed connection pool saturation as root cause, but failed to recommend the two specific remediation actions (terminating idle     connections OR increasing max_connections) explicitly stated in the EXPECTED DIAGNOSIS."
[PASS] Max connections exhausted (db-max-connections) - score: 86% [judge: 67%]
       Keywords: 100% | Tools: 100% | Judge: 67%
       Reasoning: "Agent correctly identified that max_connections (100) was exceeded by total_connections (108) with 96 idle connections as the root cause, but failed to recommend the two explicit solutions from EXPECTED DIAGNOSIS: either   terminating idle connections or increasing max_connections—instead only diagnosed a connection pool leak without actionable remediation steps."
[PASS] Max connections exhausted (db-max-connections) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Reasoning: "Agent correctly identified that max_connections limit (100) was exceeded by total connections (108) due to idle sessions (96) consuming all slots, diagnosed the connection pool exhaustion root cause with high confidence (0.95), and implicitly recommended the solution of closing idle connections while noting that increasing max_connections is also an option, fully addressing all key points in the expected diagnosis."

--- Summary ---
Total: 3 | Passed: 3 | Failed: 0 | Rate: 100%
  database: 3/3 (100%)

LLM judge scored diagnosis for 3 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.
```

Where to see the the Consistency Cert (aka stability) numbers? A new column called STABLE was added to the `vault list` and full cert is published for every fault/triage playbook in `vault accuracy` badge part:

```
[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ kubectl run vault-list \
  --image=ghcr.io/borisdali/helpdesk:v0.18.0 \
  --image-pull-policy=Never \
  --restart=Never \
  --namespace=helpdesk-system \
  -- faulttest vault list \
     --gateway http://helpdesk-gateway:8080 \
     --api-key $HELPDESK_CLIENT_API_KEY
pod/vault-list created

[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ kubectl logs -n helpdesk-system vault-list

FAULT                            PLATFORM   DIAG PLAYBOOK              REMED PLAYBOOK             LAST TEST              STABLE         INCIDENTS
------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-max-connections               any        pbs_connection_triage      pbs_connection_remediate   2026-06-22  PASS       STABLE(5)      2 runs  100% resolved  –  last: 2026-06-22  (system)
db-long-running-query            any        pbs_slow_query_triage      pbs_slow_query_remediate   2026-06-22  PASS       STABLE(5)      2 runs  100% resolved  –  last: 2026-06-22  (system)
db-lock-contention               any        pbs_lock_contention_triage pbs_slow_query_remediate   2026-06-23  PASS       STABLE(3)      2 runs  100% resolved  –  last: 2026-06-22  (system)
db-table-bloat                   any        pbs_vacuum_triage          pbs_vacuum_remediate       2026-06-22  PASS       STABLE(5)      2 runs  100% resolved  –  last: 2026-06-22  (system)
db-high-cache-miss               any        pbs_cache_miss_triage      pbs_cache_miss_remediate   2026-06-22  PASS       STABLE(5)      1 runs  100% resolved  –  last: 2026-06-22  (system)
db-connection-refused            any        pbs_db_restart_triage      pbs_db_restart_triage      READY                  —              0 runs  (system)
db-auth-failure                  any        pbs_auth_failure_triage    (none)                     (never)                —              -
db-not-exist                     any        -                          (none)                     NO PLAYBOOK            —              -
db-replication-lag               any        pbs_replication_lag        pbs_replication_remediate  READY                  —              0 runs  (system)
db-idle-in-transaction           any        pbs_connection_triage      pbs_connection_remediate   2026-06-22  PASS       STABLE(5)      2 runs  100% resolved  –  last: 2026-06-22  (system)
db-tx-lock-chain-blocker         any        pbs_lock_chain_triage      pbs_lock_chain_remediate   2026-06-22  PASS       STABLE(5)      1 runs  100% resolved  –  last: 2026-06-22  (system)
db-terminate-direct-command      any        -                          (none)                     NO PLAYBOOK            STABLE(5)      -
k8s-crashloop                    k8s        pbs_k8s_pod_crash_triage   pbs_k8s_pod_crash_remediate READY                 —              0 runs  (system)
k8s-pending                      k8s        pbs_k8s_pending_triage     (none)                     (never)                —              -
k8s-image-pull                   k8s        pbs_k8s_image_pull_triage  (none)                     (never)                —              -
k8s-no-endpoints                 k8s        pbs_k8s_no_endpoints_triage (none)                     (never)               —              -
k8s-pvc-pending                  k8s        pbs_k8s_pvc_triage         (none)                     (never)                —              -
k8s-oomkilled                    k8s        pbs_k8s_pod_crash_triage   pbs_k8s_pod_crash_remediate READY                 —              0 runs  (system)
k8s-scale-to-zero                k8s        pbs_k8s_scale_to_zero_triage pbs_k8s_scale_to_zero_remediate READY           —              0 runs  (system)
db-vacuum-needed                 any        pbs_vacuum_triage          pbs_vacuum_remediate       2026-06-22  PASS       STABLE(5)      2 runs  100% resolved  –  last: 2026-06-22  (system)
db-disk-pressure                 any        pbs_disk_pressure_triage   (none)                     2026-06-22  PASS       STABLE(5)      -
host-container-stopped           docker/vm  pbs_db_restart_triage      (none)                     (never)                —              -
host-pg-crash                    docker/vm  pbs_db_restart_triage      (none)                     (never)                —              -
db-pg-hba-corrupt                any        pbs_pg_hba_triage          pbs_db_config_recovery     2026-06-22  FAIL       UNSTABLE(1)    0 runs  (system)
db-process-kill                  any        pbs_db_restart_triage      pbs_db_restart_triage      2026-06-22  FAIL       UNSTABLE(1)    0 runs  (system)
db-config-bad-param              any        -                          (none)                     NO PLAYBOOK            UNSTABLE(1)    -
compound-db-pod-crash            multi      -                          (none)                     NO PLAYBOOK            —              -
compound-db-no-endpoints         multi      -                          (none)                     NO PLAYBOOK            —              -
db-wal-disk-full                 docker/vm  -                          (none)                     NO PLAYBOOK            UNSTABLE(1)    -
db-wal-disk-full-k8s             k8s        pbs_k8s_pod_crash_triage   (none)                     2026-06-22  PASS       UNSTABLE(5)    -
db-wal-stale-slot                any        pbs_wal_stale_slot         pbs_stale_slot_remediate   2026-06-22  PASS       STABLE(5)      1 runs  100% resolved  –  last: 2026-06-22  (system)
db-checkpoint-warning            any        pbs_checkpoint_bgwriter_triage pbs_bgwriter_remediate 2026-06-22  PASS       STABLE(5)      1 runs  100% resolved  –  last: 2026-06-22  (system)
...
```

## Vault Accuracy and Consistency Certification/Badge

`vault accuracy` without a specific fault shows a table with the abbreviated summary attributes for each (namely at-gate/post-incident feedback and diagnosis/remediation accuracy):

```
[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ kubectl run vault-accuracy \
  --image=ghcr.io/borisdali/helpdesk:v0.18.0 \
  --image-pull-policy=Never \
  --restart=Never \
  --namespace=helpdesk-system \
  -- faulttest vault accuracy \
     --gateway http://helpdesk-gateway:8080 \
     --api-key $HELPDESK_CLIENT_API_KEY
pod/vault-accuracy created


[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ kubectl logs -n helpdesk-system vault-accuracy

  FAULT                                SERIES                                 AT-GATE  POST-INC  DIAG ACC REMED ACC
  ──────────────────────────────────── ──────────────────────────────────── ─────────  ────────  ──────── ─────────
  db-max-connections                   pbs_connection_triage                      8/8       9/9      100%      100%
  ...
```

Adding a specific fault as the last argument, shows a lot more data about that fault and the related playbooks:

```
[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ kubectl run vault-accuracy2 \
  --image=ghcr.io/borisdali/helpdesk:v0.18.0 \
  --image-pull-policy=Never \
  --restart=Never \
  --namespace=helpdesk-system \
  -- faulttest vault accuracy \
     --gateway http://helpdesk-gateway:8080 \
     --api-key $HELPDESK_CLIENT_API_KEY \
     db-max-connections
pod/vault-accuracy2 created

[boris@ /tmp/helpdesk/helpdesk-v0.18.0-deploy/helm/helpdesk]$ kubectl logs -n helpdesk-system vault-accuracy2

Diagnosis accuracy for series: pbs_connection_triage

  Feedback submitted : 17 runs
  Correct diagnoses  : 17
  Accuracy rate      : 100%

  Breakdown by feedback time:
    At-gate (before remediation) : 8 of 8 correct (100%)
    Post-incident (after recovery): 9 of 9 correct (100%)

Remediation accuracy
  Feedback submitted : 8 runs
  Appropriate        : 8
  Accuracy rate      : 100%

  Breakdown by feedback time:
    At-gate (before remediation) : 8 of 8 appropriate (100%)

Triage consistency
  Fault         : db-max-connections  (Max connections exhausted)
  Verdict       : STABLE
  Runs          : 3
  Pass rate     : 100%
  Conf range    : 4pp  (primary hypothesis, passing runs only)
  Playbook      : pbs_connection_triage
  Diagnosis model: claude-haiku-4-5-20251001
  Judge model   : claude-haiku-4-5-20251001
  Tested at     : 2026-06-26 02:02 UTC  (0 days ago)
```

Note in particular the `Triage consistency` section that shows up if any of the fault injection tests ran with `--repeat N` parameter.

