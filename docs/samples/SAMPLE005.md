# aiHelpDesk Sample#5 (on K8s): Informed Gate & Two-layer security

For the background on Fault Injection Testing in aiHelpDesk see [here](../FAULTTEST.md).

For the background on interactive approvals see [here](../PLAYBOOKS.md#interactive-approval-human-in-the-loop-demo) and the simple interactive/approval sample running on a host/VM is available [here](SAMPLE004.md).

In this document we present four (!) sample runs on K8s that demonstrate not only the interactive async approvals via the [Decision Hub](../DECISIONS.md), but also the practical application of aiHelpDesk defence-in-depth as it applies to running fault injection tests.

Similar to the previous example of running on host/VM (or from the code source), you need two terminals (or better three) for running this fault injection test on K8s. The first terminal runs `helm upgrade`. The second terminal runs `kubectl get logs`. And the third one is where you issue the `curl` commands to review and approve the authorization requests (to clear the gate and individual remediation steps - depending on the requested `approval_mode`, see [here](../PLAYBOOKS.md#proceeding-through-the-gate)). You can, of course, combine the terminals two and three into one (or fold all three of the terminals into a single one by running `helm upgrade` in the background), but it may be more convenient to run them on three separate terminals (at least until you get comfortable with the process).

So let's get started:

## Run #1 - Terminal #1 (main test run):

```
[boris@ /tmp/helpdesk/helpdesk-v0.15.0-deploy/helm/helpdesk]$ helm upgrade helpdesk . --namespace helpdesk-system \
    --set faulttest.enabled=true \
    --set faulttest.ids=db-tx-lock-chain-blocker \
    --set faulttest.judge=true \
    --set faulttest.viaGateway=true \
    --set faulttest.remediate=true \
    --set faulttest.gateEscalation=true \				<-- this is a new flag introduced in release 0.15
    --set faulttest.emitAndWait=true \					<-- this is a new flag introduced in release 0.15
    --set faulttest.gatewayAPIKeySecret=<your Gateway API key> 
    --set faulttest.conn=pg-cluster-minikube \
    --set faulttest.dbPasswordSecret.name=pg-cluster-minkube-app \
    --set image.pullPolicy=Never \
    --set image.tag=v0.15.0-b7c7068 \
    --timeout 15m \
    --set faulttest.targetNamespace=db \
    --set faulttest.approvalMode=review					<-- this is a new flag introduced in release 0.15
```

The above command probably looks faimilar because we used it in previous examples, e.g. see [here](SAMPLE001.md#running-on-k8s-via-a-helm-chart). But in contrast to the previous examples, there are now three additional flags here that enable interactive async approvals via a central [Decision Hub API](../DECISIONS.md). All three flags are optional, but together they provide the experience of a human operator that is in full control over a test.

In particular, we expect that many folks would prefer to stop and review the triage diagnosis before attempting any remediation steps because the latter can be destructive and so before launching them you may want to form an opinion as to whether or not the diagnosis is indeed correct and your AI is solving the right problem and following the right steps that you'd expect (and follow by yourself if you had to).

Once this `helm upgrade` command is launched, it would stop and wait (until the test completes or until a timeout threshold of 15m is reached, as requested via a command line flag) and the results of the run can be viewed in real time by observing a K8s log, as follows:

## Run #1 - Terminal #2 (kubectl logs):

```
[boris@ ~]$ k logs -nhelpdesk-system job/helpdesk-faulttest -f
Defaulted container "faulttest" out of: faulttest, wait-for-gateway (init)
time=2026-06-05T16:22:42.310Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-06-05T16:22:42.310Z level=INFO msg="LLM judge enabled" vendor="" model=claude-haiku-4-5-20251001
time=2026-06-05T16:22:42.310Z level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external

--- Testing: Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) ---
time=2026-06-05T16:22:45.351Z level=INFO msg="shell_exec completed" output="NOTICE:  relation \"_faulttest_lock_chain\" already exists, skipping\nNOTICE:  relation \"_faulttest_lock_chain2\" already exists, skipping\nCREATE TABLE\nCREATE     TABLE\nINSERT 0 0\nINSERT 0 0\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on chain), leaves C/D (blocked on chain2)"
time=2026-06-05T16:22:46.613Z level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_6ec09e05 gateway=http://helpdesk-gateway:8080
time=2026-06-05T16:23:07.992Z level=INFO msg="audit evidence" failure=db-tx-lock-chain-blocker tools="[get_session_info get_blocking_queries check_connection]"
time=2026-06-05T16:23:12.562Z level=INFO msg="gate pending: operator review required" failure=db-tx-lock-chain-blocker run_id=plr_314dd2b0 escalation_target=""

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Escalation target :
  Findings          : Root blocker PID 103989 (active, has_writes=true); 4-level chain; terminate_connection required.


Gate pending — run_id=plr_314dd2b0
  Escalation target :
  Findings          : Root blocker PID 103989 (active, has_writes=true); 4-level chain; terminate_connection required.
  Resolve at        : POST http://helpdesk-gateway:8080/api/v1/decisions/gate:plr_314dd2b0/resolve

time=2026-06-05T16:23:28.107Z level=INFO msg="faultlib: gate poll" run_id=plr_314dd2b0 outcome=gate_pending
time=2026-06-05T16:23:43.799Z level=INFO msg="faultlib: gate poll" run_id=plr_314dd2b0 outcome=gate_pending

```

This is the [informed gate](../PLAYBOOKS.md#informed-gate) in action. In this mode, a human operator is expected to review the results of the triage and, if satisfied, let aiHelpDesk proceed with actually fixing the problem (by effectively launching the remediation playbook). Please note that the gate is opt-in per-request (note `--set faulttest.gateEscalation=true`) in the above command and so if this behavior is not desired (e.g. for the fully automated CI runs), it can be skipped. See more on this point in the [last section](SAMPLE005.md#control-surfaces).

## Run #1 - Terminal #3 (approval terminal):

```
[boris@ ~]$ APIKEY=$(k get secret -n helpdesk-system gateway-api-key -o jsonpath='{.data.api-key}' | base64 -d)
[boris@ ~]$ curl -s -H "Authorization: Bearer $APIKEY" "http://localhost:8080/api/v1/decisions?type=gate" | jq .
Handling connection for 8080
{
  "decisions": [
    {
      "id": "gate:plr_314dd2b0",
      "type": "gate",
      "status": "pending",
      "summary": "Triage complete — TRANSITION_TO pbs_lock_chain_remediate",
      "requested_by": "",
      "requested_at": "2026-06-05T16:22:47Z",
      "expires_at": "0001-01-01T00:00:00Z",
      "resolve_url": "/api/v1/decisions/gate:plr_314dd2b0/resolve",
      "extra": {
        "findings": "Root blocker PID 103989 (active, has_writes=true); 4-level chain; terminate_connection required.",
        "gate_type": "transition",
        "series_id": "pbs_lock_chain_triage",
        "transition_target": "pbs_lock_chain_remediate"
      }
    }
  ],
  "total": 1
}
```

The gate ID that is waiting for approval is visible in the `kubectl log` and also in the above curl against the Decision Hub, which provides all the necessary information for a human operator to make an informed decision (please let us know if disagree and prefer to see additional info). Let's assume that the diagnosis makes sense and you prefer to proceed with actually fixing the problem (the remediation phase):

```
[boris@ ~]$ GATE_ID=plr_314dd2b0
[boris@ ~]$ curl -s -X POST \
>     -H "Authorization: Bearer $APIKEY" \
>     -H "Content-Type: application/json" \
>     "http://localhost:8080/api/v1/decisions/gate:$GATE_ID/resolve" \
>     -d '{"resolution":"approved","resolved_by":"boris@borisdali.com"}' | jq .
Handling connection for 8080
{
  "run_id": "plr_3c33d7ea",
  "status": "pending_approval",
  "step": {
    "index": 1,
    "agent": "database",
    "tool": "get_blocking_queries",
    "args": {
      "connection_string": "host=pg-cluster-minkube-rw.db.svc.cluster.local port=5432 dbname=app user=app"
    },
    "reason": "Step 1: Confirm the blocking chain is still active, identify all participants (root, intermediates, leaves), and verify PID 103989 is the root blocker.",
    "action_class": "read"
  },
  "approval_id": "apr_b4d688ea",
  "effective_approval_mode": "manual"
}
```

## Run #1 - Back to Terminal #1 (main test run):

Once the triage gate is cleared, the remediation phase is free to kick in (notice how `outcome=gate_pending` switched to `outcome=transitioned` below):

```
time=2026-06-05T16:30:24.628Z level=INFO msg="faultlib: gate poll" run_id=plr_314dd2b0 outcome=gate_pending
time=2026-06-05T16:30:40.248Z level=INFO msg="faultlib: gate poll" run_id=plr_314dd2b0 outcome=gate_pending

time=2026-06-05T16:30:56.137Z level=INFO msg="faultlib: gate poll" run_id=plr_314dd2b0 outcome=transitioned

time=2026-06-05T16:30:56.836Z level=INFO msg="agent_approve: pending step" step_index=0 tool=get_blocking_queries action_class=read reason="" approval_id=apr_b4d688ea
time=2026-06-05T16:31:05.413Z level=INFO msg="agent_approve: pending step" step_index=2 tool=get_session_info action_class=read reason="Step 2: Inspect the root blocker (PID 103989, blocking_pid=NULL) to confirm has_open_tx=true, state,      has_writes status, and idle_secs before proceeding to termination." approval_id=apr_7dea57ed
time=2026-06-05T16:31:11.853Z level=INFO msg="agent_approve: pending step" step_index=3 tool=get_session_info action_class=read reason="Step 2 requires inspection of all intermediate sessions before termination; PID 103990 is an intermediate (blocking_pid=103989, blocks 103991) and must be checked for has_writes status." approval_id=apr_92f518b6
time=2026-06-05T16:31:20.115Z level=INFO msg="agent_approve: pending step" step_index=4 tool=get_session_info action_class=read reason="Step 2 requires inspection of all intermediate sessions before termination; PID 103990 is an intermediate (blocks 103991 while blocked by 103989), and PID 103991 is also intermediate (blocks 103992 while blocked by 103990), so we must inspect 103991 to assess cascade risk." approval_id=apr_77713d40
time=2026-06-05T16:31:27.416Z level=INFO msg="agent_approve: pending step" step_index=5 tool=get_session_info action_class=read reason="Step 2 requires inspection of all intermediate sessions before termination; PID 103992 is a leaf (blocked but not blocking others) and must be checked for has_writes status to complete cascade risk assessment." approval_id=apr_6973b91f
time=2026-06-05T16:31:38.359Z level=INFO msg="agent_approve: pending step" step_index=6 tool=terminate_connection action_class=destructive reason="Root blocker (PID 103989, blocking_pid=NULL) is active with has_writes=true in a 8m 26s        transaction; WARNING: terminating root will cascade to roll back session 103990 (has_writes=true, 8m 33s idle) — operator should confirm this work can be discarded before approving. Sessions 103991 and 103992 are read-only leaves with no     cascade write risk." approval_id=apr_16fe1b5c
time=2026-06-05T16:31:51.471Z level=INFO msg="agent_approve: remediation complete" summary="Step 1–2 completed: identified root blocker PID 103989 (blocking_pid=NULL, has_writes=true, active state, 8m 26s in transaction). Identified          intermediates: PID 103990 (has_writes=true, cascade risk), PID 103991 (read-only, low risk), PID 103992 (read-only, low risk). Step 3 termination attempted but denied by policy 'pii-data-protection' rule 2 (destructive operations on PII      databases prohibited). Remediation cannot proceed without policy override or DBA+Legal approval. Escalation required—do not retry terminate_connection without explicit authorization and policy exception."
time=2026-06-05T16:31:51.544Z level=INFO msg="recovery check failed, retrying" err="verify_sql returned false" remaining=1m0s
time=2026-06-05T16:31:56.579Z level=INFO msg="recovery check failed, retrying" err="verify_sql returned false" remaining=55s
time=2026-06-05T16:32:01.669Z level=INFO msg="recovery check failed, retrying" err="verify_sql returned false" remaining=50s
```

But unfortunately in this case the remeidation fails... because of a security policy in place. Two of them in fact. But let's take it slow, one step at a time. There's an interesting bit in the above log excerpt that is worth paying attention to:

```
Step 3 termination attempted but denied by policy 'pii-data-protection' rule 2
(destructive operations on PII databases prohibited). 
Remediation cannot proceed without policy override or DBA+Legal approval. 
Escalation required—do not retry terminate_connection without explicit authorization and policy exception."
```

Aha. The e2e fault injection test flow worked as designed here, however the remediation "failed" because the governance policy `pii-data-protection` blocked `terminate_connection` on a database tagged with sensitivity: ["pii", "internal"].

That's the system working correctly, but the target database cluster we chose as a target for this fault inject test has the wrong settinings/tags.


Can we keep the PII tag and just get an override for this run by adding `--set faulttest.approvalMode=force` to the `helm` command? Would that `force` option bypass the policy enforcement entirely? Let's find out!


## Run #2 - Attempt to bypass the policy with `approval_mode=force`:

So this second run is similar to the one above, with three terminals: one running `helm upgrade --set faulttest.enabled=true`, one tail-ing `kubectl logs` and the third terminal used to review the authorization requests and granting (or denying) approvals. I'll speed up for this second run and show the relevant excerpts together, but please keep in mind that they come from three separate terminals, just like in the detailed example of run #1 above:

```
[boris@ /tmp/helpdesk/helpdesk-v0.15.0-deploy/helm/helpdesk]$ helm upgrade helpdesk . --namespace helpdesk-system \
    --set faulttest.enabled=true \
    --set faulttest.ids=db-tx-lock-chain-blocker \
    --set faulttest.judge=true \
    --set faulttest.viaGateway=true \
    --set faulttest.remediate=true \
    --set faulttest.gateEscalation=true \
    --set faulttest.emitAndWait=true \
    --set faulttest.gatewayAPIKeySecret=<your Gateway API key> 
    --set faulttest.conn=pg-cluster-minikube \
    --set faulttest.dbPasswordSecret.name=pg-cluster-minkube-app \
    --set image.pullPolicy=Never \
    --set image.tag=v0.15.0-b7c7068 \
    --timeout 15m \
    --set faulttest.targetNamespace=db \
    --set faulttest.approvalMode=force

[boris@ ~]$ k logs -nhelpdesk-system job/helpdesk-faulttest -f
Defaulted container "faulttest" out of: faulttest, wait-for-gateway (init)

--- Testing: Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) ---
time=2026-06-05T16:35:48.501Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-06-05T16:35:48.501Z level=INFO msg="LLM judge enabled" vendor="" model=claude-haiku-4-5-20251001
time=2026-06-05T16:35:48.501Z level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external
time=2026-06-05T16:35:51.557Z level=INFO msg="shell_exec completed" output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on chain),   leaves C/D (blocked on chain2)"
time=2026-06-05T16:35:52.041Z level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_6ec09e05 gateway=http://helpdesk-gateway:8080

time=2026-06-05T16:36:12.633Z level=WARN msg="gateway warning" failure=db-tx-lock-chain-blocker warning="approval_mode clamped to \"manual\": override to \"force\" requires one of roles [dba_lead oncall_senior] (caller: gateway)"

time=2026-06-05T16:36:14.528Z level=INFO msg="audit evidence" failure=db-tx-lock-chain-blocker tools="[get_session_info get_blocking_queries check_connection]"
time=2026-06-05T16:36:16.635Z level=INFO msg="gate pending: operator review required" failure=db-tx-lock-chain-blocker run_id=plr_1a89f306 escalation_target=""

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Escalation target :
  Findings          : Root blocker PID 104290 (active, executing pg_sleep(3600), has_writes=yes); 3-level chain; terminate_connection required to close connection and release transaction locks across the cascade.


Gate pending — run_id=plr_1a89f306
  Escalation target :
  Findings          : Root blocker PID 104290 (active, executing pg_sleep(3600), has_writes=yes); 3-level chain; terminate_connection required to close connection and release transaction locks across the cascade.
  Resolve at        : POST http://helpdesk-gateway:8080/api/v1/decisions/gate:plr_1a89f306/resolve

time=2026-06-05T16:36:33.351Z level=INFO msg="faultlib: gate poll" run_id=plr_1a89f306 outcome=gate_pending
time=2026-06-05T16:36:49.307Z level=INFO msg="faultlib: gate poll" run_id=plr_1a89f306 outcome=gate_pending
...
```

Similar to the above, the run stops at the gate and expects a human to review the results of the triage and agree or disagree with the diagnosis. If a human operator agrees, they can clear the gate for aiHelpDesk start the remediation part:

```
[boris@ ~]$ APIKEY=$(kubectl get secret -n helpdesk-system gateway-api-key -o jsonpath='{.data.api-key}' | base64 -d)
[boris@ ~]$ curl -s -H "Authorization: Bearer $APIKEY" "http://localhost:8080/api/v1/decisions?type=gate" | jq .
Handling connection for 8080
{
  "decisions": [
    {
      "id": "gate:plr_1a89f306",
      "type": "gate",
      "status": "pending",
      "summary": "Triage complete — TRANSITION_TO pbs_lock_chain_remediate",
      "requested_by": "",
      "requested_at": "2026-06-05T16:35:53Z",
      "expires_at": "0001-01-01T00:00:00Z",
      "resolve_url": "/api/v1/decisions/gate:plr_1a89f306/resolve",
      "extra": {
        "findings": "Root blocker PID 104290 (active, executing pg_sleep(3600), has_writes=yes); 3-level chain; terminate_connection required to close connection and release transaction locks across the cascade.",
        "gate_type": "transition",
        "series_id": "pbs_lock_chain_triage",
        "transition_target": "pbs_lock_chain_remediate"
      }
    }
  ],
  "total": 1
}
[boris@ ~]$ GATE_ID=plr_1a89f306
[boris@ ~]$ curl -s -X POST -H "Authorization: Bearer $APIKEY" -H "Content-Type: application/json" "http://localhost:8080/api/v1/decisions/gate:$GATE_ID/resolve" -d '{"resolution":"approved","resolved_by":"boris@    borisdali.com"}' | jq .
Handling connection for 8080
{
  "run_id": "plr_5ff1ca97",
  "status": "pending_approval",
  "step": {
    "index": 1,
    "agent": "database",
    "tool": "get_blocking_queries",
    "args": {
      "connection_string": "host=pg-cluster-minkube-rw.db.svc.cluster.local port=5432 dbname=app user=app"
    },
    "reason": "Step 1: Confirm the blocking chain is still present and identify all participants (root, intermediates, leaves) before proceeding with termination.",
    "action_class": "read"
  },
  "approval_id": "apr_8e9345ab",
  "effective_approval_mode": "manual"
}
```

Cool. Did it run to the end and succeeded now? Not quite:

```
time=2026-06-05T16:36:49.307Z level=INFO msg="faultlib: gate poll" run_id=plr_1a89f306 outcome=gate_pending
time=2026-06-05T16:37:05.163Z level=INFO msg="faultlib: gate poll" run_id=plr_1a89f306 outcome=transitioned
time=2026-06-05T16:37:05.752Z level=INFO msg="agent_approve: pending step" step_index=0 tool=get_blocking_queries action_class=read reason="" approval_id=apr_8e9345ab
time=2026-06-05T16:37:12.649Z level=INFO msg="agent_approve: pending step" step_index=2 tool=get_session_info action_class=read reason="Step 2: Inspect the root blocker (PID 104290, blocking_pid=NULL) to confirm has_open_tx=true, check state and has_writes status before proceeding to termination." approval_id=apr_d0ce057c
time=2026-06-05T16:37:20.644Z level=INFO msg="agent_approve: pending step" step_index=3 tool=get_session_info action_class=read reason="Step 2 requires inspection of all intermediate sessions before termination; PID 104291 is an intermediate (blocked by 104290, blocks 104292) and must be checked for has_writes status." approval_id=apr_94aab2a4
time=2026-06-05T16:37:30.715Z level=INFO msg="agent_approve: pending step" step_index=4 tool=get_session_info action_class=read reason="Step 2 requires inspection of all intermediate sessions before acting; PID 104291 is intermediate (blocks 104292 but is blocked by 104290), and PID 104292 is also intermediate (blocks 104293 but is blocked by 104291) — must inspect 104292 to assess cascade risk." approval_id=apr_73511ab0
time=2026-06-05T16:37:39.925Z level=INFO msg="agent_approve: pending step" step_index=5 tool=get_session_info action_class=read reason="Step 2 requires inspection of all intermediate sessions before termination; PID 104293 is a leaf blocker  (has blocking_pid=104292) and must be checked to determine if it has uncommitted writes that would cascade on termination." approval_id=apr_d0ba3640
time=2026-06-05T16:37:48.058Z level=INFO msg="agent_approve: pending step" step_index=6 tool=terminate_connection action_class=destructive reason="Root blocker (PID 104290, blocking_pid=NULL) is active in pg_sleep with has_writes=true and    idle_secs=88; intermediates 104291 (has_writes=true) and 104292,104293 (read-only) are at cascade risk — operator approval required before execution, but this is the next mandatory step per guidance Step 3." approval_id=apr_44c0f0ae
time=2026-06-05T16:38:00.563Z level=INFO msg="agent_approve: remediation complete" summary="Step 1 confirmed root blocker PID 104290 (blocking_pid=NULL) with intermediates 104291, 104292, 104293. Step 2 inspected all sessions: root           has_writes=true with 1m 28s active state; intermediate 104291 has_writes=true (rollback 48s–3m 12s); intermediates 104292 and 104293 are read-only (instant rollback). Step 3 attempted terminate_connection but was DENIED by policy 'pii-data-  protection' rule 2 (destructive operations on PII databases prohibited). Remediation cannot proceed without policy override/approval from DBA + Legal per the access control decision. Escalation required."
time=2026-06-05T16:38:00.642Z level=INFO msg="recovery check failed, retrying" err="verify_sql returned false" remaining=1m0s
time=2026-06-05T16:38:05.679Z level=INFO msg="recovery check failed, retrying" err="verify_sql returned false" remaining=55s
```

So what happened here? The `approval_mode=force` didn't work?

Well, aiHelpDesk features two separate enforcement layers blocking this and `force` doesn't bypass policy. Alas:

**Layer 1**: approval mode clamped:
The warning shows that the requested `force` mode was downgraded (aka clamped) to `manual` because the caller didn't have the `dba_lead` or `oncall_senior` role.
`force` is a privileged mode gated by `approval_override_roles` tag in the infra.json. And the caller is missing that role for the target database cluster.

**Layer 2**: policy enforcement:
`pii-data-protection` rule 2 blocks `terminate_connection` on PII-tagged databases.
This layer 2 security enforcement runs at tool execution time, independently of approval mode.
Even if force had gotten through, policy still denies the tool call.

That is, `approvalMode=force` is for "bypassing" the step-approval wait loop (with appropriate role, of course), not for bypassing security policies.

>
> **Policy enforcement is unconditional**.
>

## Run #3 - Attempt to bypass the policy with `approval_mode=force` and by running as a principal with Lead DBA/Senior Oncall role:

Fine, let Alice run it (because she's part of the DBA team and has the power to request the `approval_mode=force`):

```
[boris@ ~]$ kubectl get secret -n helpdesk-system helpdesk-users -o jsonpath='{.data.users\.yaml}' | base64 -d|grep -A2 alice
  - id: alice@example.com
    roles: [dba, dba_lead, sre, operator]

[boris@ /tmp/helpdesk/helpdesk-v0.15.0-deploy/helm/helpdesk]$ k logs -nhelpdesk-system job/helpdesk-faulttest -f
Defaulted container "faulttest" out of: faulttest, wait-for-gateway (init)

--- Testing: Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) ---
time=2026-06-05T16:44:41.443Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-06-05T16:44:41.443Z level=INFO msg="LLM judge enabled" vendor="" model=claude-haiku-4-5-20251001
time=2026-06-05T16:44:41.443Z level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external
time=2026-06-05T16:44:44.493Z level=INFO msg="shell_exec completed" output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on chain),   leaves C/D (blocked on chain2)"
time=2026-06-05T16:44:45.363Z level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_6ec09e05 gateway=http://helpdesk-gateway:8080
time=2026-06-05T16:45:04.717Z level=INFO msg="audit evidence" failure=db-tx-lock-chain-blocker tools="[get_session_info get_blocking_queries check_connection]"
time=2026-06-05T16:45:07.156Z level=INFO msg="gate pending: operator review required" failure=db-tx-lock-chain-blocker run_id=plr_739af648 escalation_target=""

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Escalation target :
  Findings          : Root blocker PID 104506 (active, has_writes=true); 4-level lock chain; terminate_connection required.


Gate pending — run_id=plr_739af648
  Escalation target :
  Findings          : Root blocker PID 104506 (active, has_writes=true); 4-level lock chain; terminate_connection required.
  Resolve at        : POST http://helpdesk-gateway:8080/api/v1/decisions/gate:plr_739af648/resolve

time=2026-06-05T16:45:22.923Z level=INFO msg="faultlib: gate poll" run_id=plr_739af648 outcome=gate_pending
time=2026-06-05T16:45:39.214Z level=INFO msg="faultlib: gate poll" run_id=plr_739af648 outcome=gate_pending
time=2026-06-05T16:45:54.944Z level=INFO msg="faultlib: gate poll" run_id=plr_739af648 outcome=gate_pending


[boris@cassiopeia ~]$ curl -s -H "Authorization: Bearer $APIKEY" "http://localhost:8080/api/v1/decisions?type=gate" | jq .
Handling connection for 8080
{
  "decisions": [
    {
      "id": "gate:plr_739af648",
      "type": "gate",
      "status": "pending",
      "summary": "Triage complete — TRANSITION_TO pbs_lock_chain_remediate",
      "requested_by": "alice@example.com",
      "requested_at": "2026-06-05T16:44:46Z",
      "expires_at": "0001-01-01T00:00:00Z",
      "resolve_url": "/api/v1/decisions/gate:plr_739af648/resolve",
      "extra": {
        "findings": "Root blocker PID 104506 (active, has_writes=true); 4-level lock chain; terminate_connection required.",
        "gate_type": "transition",
        "series_id": "pbs_lock_chain_triage",
        "transition_target": "pbs_lock_chain_remediate"
      }
    }
  ],
  "total": 1
}

[boris@ ~]$ GATE_ID=plr_739af648
[boris@ ~]$ curl -s -X POST -H "Authorization: Bearer $APIKEY" -H "Content-Type: application/json" "http://localhost:8080/api/v1/decisions/gate:$GATE_ID/resolve" -d '{"resolution":"approved","resolved_by":"boris@borisdali.com"}' | jq .
Handling connection for 8080
{
  "run_id": "plr_2cc12788",
  "status": "pending_approval",
  "step": {
    "index": 1,
    "agent": "database",
    "tool": "get_blocking_queries",
    "args": {
      "connection_string": "host=pg-cluster-minkube-rw.db.svc.cluster.local port=5432 dbname=app user=app"
    },
    "reason": "Step 1: Confirm the lock chain is still active, identify the root blocker (blocking_pid = NULL), classify all intermediates, and record their PIDs before proceeding to inspection.",
    "action_class": "read"
  },
  "approval_id": "apr_d4002247",
  "effective_approval_mode": "manual"
}
```

So now with the gate cleared the test should succeed, right? Again, not quite:

```
time=2026-06-05T16:46:58.946Z level=INFO msg="faultlib: gate poll" run_id=plr_739af648 outcome=gate_pending
time=2026-06-05T16:47:15.544Z level=INFO msg="faultlib: gate poll" run_id=plr_739af648 outcome=transitioned
time=2026-06-05T16:47:16.632Z level=INFO msg="agent_approve: pending step" step_index=0 tool=get_blocking_queries action_class=read reason="" approval_id=apr_d4002247
time=2026-06-05T16:47:23.413Z level=INFO msg="agent_approve: pending step" step_index=2 tool=get_session_info action_class=read reason="Step 2: Inspect the root blocker (PID 104506, blocking_pid=NULL) to confirm has_open_tx=true and check    state, has_writes, and idle_secs before proceeding to termination." approval_id=apr_b146c26c
time=2026-06-05T16:47:31.176Z level=INFO msg="agent_approve: pending step" step_index=3 tool=get_session_info action_class=read reason="Step 2 requires inspection of all intermediate sessions before termination; PID 104507 is an intermediate (blocking_pid=104506, blocks PID 104509) and must be checked for has_writes status to assess cascade risk." approval_id=apr_56eba6f4
time=2026-06-05T16:47:38.823Z level=INFO msg="agent_approve: pending step" step_index=4 tool=get_session_info action_class=read reason="Step 2 requires inspection of all intermediate sessions before termination; PID 104509 is an intermediate (blocked by 104507, blocks 104508) and has not yet been inspected." approval_id=apr_2d33187f
time=2026-06-05T16:47:46.058Z level=INFO msg="agent_approve: pending step" step_index=5 tool=get_session_info action_class=read reason="Step 2 requires inspection of ALL intermediate sessions before termination; PID 104508 is an intermediate (blocked by 104509, which itself is blocked downstream) and has not yet been inspected." approval_id=apr_b547897a
time=2026-06-05T16:47:53.322Z level=INFO msg="agent_approve: pending step" step_index=6 tool=terminate_connection action_class=destructive reason="Root blocker 104506 (blocking_pid=NULL, state=active, has_writes=true, idle_secs=166) must be  terminated; intermediates 104507 (has_writes=true) and 104509 (has_writes=false) will cascade-rollback, with 104507's uncommitted work discarded as a side effect of lock release—operator has been informed of cascade risk and must approve     before this call executes." approval_id=apr_02c135c9
time=2026-06-05T16:48:04.443Z level=INFO msg="agent_approve: remediation complete" summary="Playbook execution halted at Step 3 (Terminate root). Root blocker PID 104506 was identified (blocking_pid=NULL, state=active, has_writes=true,       idle_secs=166). Intermediates 104507 (has_writes=true) and 104509 (read-only) were inspected. Termination was attempted but DENIED by policy: destructive operations on PII databases require DBA + Legal contact. Operator must escalate through proper channels before remediation can proceed."
time=2026-06-05T16:48:04.620Z level=INFO msg="recovery check failed, retrying" err="verify_sql returned false" remaining=1m0s
time=2026-06-05T16:48:09.669Z level=INFO msg="recovery check failed, retrying" err="verify_sql returned false" remaining=55s
```

So yes, there's clearly progress here, note that there's no longer the warning we saw earlier:

```
  level=WARN msg="gateway warning" ... "approval_mode clamped to \"manual\": override to \"force\" requires one of roles [dba_lead oncall_senior]"
```

In other words, the Gateway accepted `approval_mode=force` without clamping because alice has `dba_lead`. That's aiHelpDesk Layer 1 signal.

The step approvals also proceed automatically without any polling wait. Every `agent_approve: pending` step fires within ~8s of the previous one (just the 15s gate poll interval), whereas with `manual` mode each destructive step would require an explicit external POST to resolve. The automatic progression through all 6 steps is the behavioral confirmation that `force` is in effect.

Then at step 6 (`terminate_connection`, destructive op), the policy enforcement still fired unconditionally. That is, aiHelpDesk Layer 2 holds regardless of alice's role or approval mode.

To summarize:
- Layer 1 is indeed bypass-able with the right role: no clamping warning, steps auto-proceed without any human review or approval
- Layer 2 holds true regardless: policy blocks the destructive tools, period

We'd like to think of these two layers as a "destructive operations on PII databases require DBA + Legal contact." Exactly as the log says :-)


## Run #4 - A successful fault injection test:

To reiterate the point of this excercise, there's no way to bypass Layer 2 security outlined above unless the `sensitivity: ["pii"]` tag is actually removed from the target database server in question (which realistically shouldn't be coupled with the `tag: ["chaos"]` anyway as this tag makes the a database server eligible for the fault injection testing in the first place). Now, the point here is that for the [enteprise deployment](../DEPLOYMENT_MODES.md#mode-3--enterprise--it-hosted-full-governed) the infra.json is under the central, right IT control and aiHelpDesk in this operating mode doesn't allow running a diagnosis, let alone remedation, against arbitary database clusters that are not listed in infra.json.

Once the `sensitivty` tag is cleared, we can proceed with the full fault injection test. And this time the full flow succeeds e2e: the gate, emit-and-wait, step-by-step approvals, agent correctly identifying the full blocking chain and cascade risk. The remediation also succeeds because the governance policy `pii-data-protection` block has been removed to authorize `terminate_connection` on a target database cluster:

```
[boris@ ~]$ curl -s -H "Authorization: Bearer $APIKEY" "http://localhost:8080/api/v1/decisions?type=step_approval&status=pending" | jq .
{
  "decisions": [
    {
      "id": "step:apr_9115c829",
      "type": "step_approval",
      "status": "pending",
      "summary": "destructive database/terminate_connection",
      "requested_by": "faulttest",
      "requested_at": "2026-06-05T18:07:48.721428127Z",
      "expires_at": "2026-06-05T19:07:48.721406002Z",
      "resolve_url": "/api/v1/decisions/step:apr_9115c829/resolve",
      "extra": {
        "action_class": "destructive",
        "agent": "database",
        "resource_name": "",
        "resource_type": "",
        "run_id": "plr_cd11a5b2",
        "tool": "terminate_connection"
      }
    },

[boris@ ~]$ curl -s -X POST -H "Authorization: Bearer $APIKEY" -H "Content-Type: application/json" "http://localhost:8080/api/v1/decisions/step:apr_9115c829/resolve" -d '{"resolution":"approved","resolved_by":"boris@borisdali.com"}'
Handling connection for 8080
forbidden: one of roles [dba fleet-approver] required
```

This is another point worth mentioning. You probably noticed the intentional asymmetry with respect to approving a gate vs. a destructive execution step. This is because the contracts are different:

```
┌────────────────────────────┬──────────────────────────────────────────────────────────────────────────────────────────────┬───────────────────────────────────┐
│       Decision type        │                            What it actually authorizes                                       │           Role required           │
├────────────────────────────┼──────────────────────────────────────────────────────────────────────────────────────────────┼───────────────────────────────────┤
│ Gate (gate:plr_*)          │ "Should the triage/remediation chain continue at all?"                                       │                                   │
│                            │ Doesn't execute a destructive action itself. Just unblocks the playbook to begin.            │ Authenticated only (admin bypass) │
├────────────────────────────┼──────────────────────────────────────────────────────────────────────────────────────────────┼───────────────────────────────────┤
│ Step approval (step:apr_*) │ "Should this specific destructive tool call (terminate_connection, etc.) execute right now?" │                                   │
│                            │  Authorizes the actual write.                                                                │ dba or fleet-approver             │
└────────────────────────────┴──────────────────────────────────────────────────────────────────────────────────────────────┼───────────────────────────────────┘
```

## The defense-in-depth that makes this safe:

A gate approval can request an `approval_mode`, but the gateway clamps it against the caller's role via `enforceApprovalOverride` (see [here](../cmd/gateway/playbooks.go#L135)).
So an API-key caller approving a gate with `approval_mode:force` (or `auto`) would get silently downgraded. 
This makes the chain to proceed in the `manual` or `review` mode and every destructive step still hits the role-checked step-approval gate. 
The API-key holder can say "go ahead and try", but can't bypass the DBA at the actual destructive call.

>
> This matches aiHelpDesk overal production threat model:
> - Operators / on-call who hold the gateway API key can triage broadly and unblock investigations.
>   That's low blast radius because nothing destructive has happened yet.
> 
> - DBAs / approvers are the ones who actually authorize writes. 
>   One tool call at a time. 
>   Regardless of how the run was kicked off.
> 

aiHelpDesk is OSS, so it's obviously possible to configure it to equivalent of DBA-gating of steps too by simply changing `POST /api/v1/decisions/{id}/resolve` [here](../internal/authz/gateway_routes.go#L52) from `{AdminBypass: true}` to `{RequireRoles: ["dba", "fleet-approver"], AdminBypass: true}`, but we don't recommend going down this route because it flattens the distinction. The current model is the standard "service can request, human-with-role authorizes the write" split.

It may be also worth pointing out that a Bearer-API-key request would also have been rejected if the API key had no role mapped to it. 
The fact that alice with X-User: alice@example.com succeeded (no Bearer at all) means she's resolved as an identity with the `dba` role via the users file (or equivalent JWT authn if it's used in favor of `static`).
And that's the principal type the step gate is built to accept.

Long story short, alice@ as a DBA can approve this destructive step (while the "normal" authenticated user that lacks the DBA role, can't as shown above):

```
[boris@ ~]$ curl -s -X POST -H "X-User: alice@example.com" -H "Content-Type: application/json" "http://localhost:8080/api/v1/decisions/step:apr_9115c829/resolve" -d '{"resolution":"approved","resolved_by":"boris@borisdali.com"}'
Handling connection for 8080
{"status":"approved"}
```

And with that, with the triage/remediation gate cleared and the destructive operation approved, we finally get the full fault injection test finishig successfully, both the triage, the transition and the remedation phases:

## Full log of the successful fault injection test

```
[boris@ ~]$ k logs -nhelpdesk-system job/helpdesk-faulttest -f
Defaulted container "faulttest" out of: faulttest, wait-for-gateway (init)

--- Testing: Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) ---
time=2026-06-05T17:43:49.177Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-06-05T17:43:49.177Z level=INFO msg="LLM judge enabled" vendor="" model=claude-haiku-4-5-20251001
time=2026-06-05T17:43:49.177Z level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external
time=2026-06-05T17:43:52.236Z level=INFO msg="shell_exec completed" output="NOTICE:  relation \"_faulttest_lock_chain\" already exists, skipping\nNOTICE:  relation \"_faulttest_lock_chain2\" already exists, skipping\nCREATE TABLE\nCREATE     TABLE\nINSERT 0 0\nINSERT 0 0\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on chain), leaves C/D (blocked on chain2)"
time=2026-06-05T17:43:53.171Z level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_6ec09e05 gateway=http://helpdesk-gateway:8080
time=2026-06-05T17:44:20.345Z level=INFO msg="audit evidence" failure=db-tx-lock-chain-blocker tools="[get_session_info get_blocking_queries check_connection]"

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Escalation target :
  Findings          : Root blocker PID 105718 (active, has_writes=yes); 3-level chain; terminate_connection required.


Gate pending — run_id=plr_be6bd41e
  Escalation target :
  Findings          : Root blocker PID 105718 (active, has_writes=yes); 3-level chain; terminate_connection required.
  Resolve at        : POST http://helpdesk-gateway:8080/api/v1/decisions/gate:plr_be6bd41e/resolve

time=2026-06-05T17:44:22.269Z level=INFO msg="gate pending: operator review required" failure=db-tx-lock-chain-blocker run_id=plr_be6bd41e escalation_target=""
time=2026-06-05T17:44:38.071Z level=INFO msg="faultlib: gate poll" run_id=plr_be6bd41e outcome=gate_pending
time=2026-06-05T17:44:53.454Z level=INFO msg="faultlib: gate poll" run_id=plr_be6bd41e outcome=gate_pending

time=2026-06-05T18:07:06.525Z level=INFO msg="faultlib: gate poll" run_id=plr_be6bd41e outcome=transitioned
time=2026-06-05T18:07:07.430Z level=INFO msg="agent_approve: pending step" step_index=0 tool=get_blocking_queries action_class=read reason="" approval_id=apr_806b439c
time=2026-06-05T18:07:16.727Z level=INFO msg="agent_approve: pending step" step_index=2 tool=get_session_info action_class=read reason="Step 2: Inspect the root blocker (PID 105718, blocking_pid = NULL) to confirm has_open_tx, state,         has_writes, and idle_secs before proceeding to termination." approval_id=apr_20c4f076
time=2026-06-05T18:07:25.362Z level=INFO msg="agent_approve: pending step" step_index=3 tool=get_session_info action_class=read reason="Step 2 requires inspection of all intermediate sessions before termination; PID 105719 is an intermediate (blocking_pid=105718, blocks PID 105721) and must be checked for has_writes status." approval_id=apr_33281477
time=2026-06-05T18:07:32.709Z level=INFO msg="agent_approve: pending step" step_index=4 tool=get_session_info action_class=read reason="Step 2 requires inspection of all intermediate sessions before termination; PID 105721 is an intermediate (blocks 105720 and is blocked by 105718) and has not yet been inspected." approval_id=apr_2c1c9d0c
time=2026-06-05T18:07:40.264Z level=INFO msg="agent_approve: pending step" step_index=5 tool=get_session_info action_class=read reason="Step 2 requires inspection of ALL intermediate sessions before acting; PID 105720 is a leaf blocker (has  blocking_pid=105721) and must be checked for has_writes status to assess cascade risk." approval_id=apr_a8bba45d
time=2026-06-05T18:07:48.726Z level=INFO msg="agent_approve: step approval pending — waiting for external resolution" step_index=6 tool=terminate_connection approval_id=apr_9115c829
time=2026-06-05T18:16:35.474Z level=INFO msg="agent_approve: step approval resolved" approval_id=apr_9115c829 resolution=approved

time=2026-06-05T18:16:49.828Z level=INFO msg="agent_approve: pending step" step_index=7 tool=get_blocking_queries action_class=read reason="Step 4 verification: confirm that terminating root PID 105718 cleared the lock chain and no sessions  remain blocked." approval_id=apr_6233a8a8

  Remediation complete: Root blocker PID 105718 (blocking_pid=NULL, has_writes=true, active state) was successfully terminated. Cascade disclosure was provided: intermediate PID 105719 (has_writes=true) will roll back upon root termination.  Verification via get_blocking_queries confirms no blocking queries remain — the lock chain has been fully cleared.

Remediation: RECOVERED in 0.1s (score: 100%)
Vault: draft saved → pb_04c4573d (activate with 'faulttest vault list')
time=2026-06-05T18:17:15.234Z level=INFO msg="tearing down failure" id=db-tx-lock-chain-blocker type=shell_exec
time=2026-06-05T18:17:15.291Z level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n(0 rows)\n\nDROP TABLE\nDROP TABLE"
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% judge=100%)
Remediation Result:  [PASS] score=100% (0.1s, playbook)
Overall Result:      [PASS] score=100%

=== Fault Test Report: a59221f7 ===

[PASS] Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Remediation: 100% (0.1s, playbook) | Overall: 100%
       Reasoning: "Agent correctly identified PID 105718 as the root blocker with no blocking_pid, explicitly rejected cancel_query with detailed explanation of why it fails (transaction and locks persist), recommended terminate_connection   as the only solution, noted the 3-level chain structure, and explained the cascade effect and rollback consequences."

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

LLM judge scored diagnosis for 1 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to /reports/faulttest-a59221f7.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "a59221f7",
  "timestamp": "2026-06-05T18:17:15Z",
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
      "tool_evidence_mode": "audit",
      "response_text": "I'll begin diagnosing the unavailability of this Kubernetes pod database. Let me start with a connection check.\nGood — the database is reachable. Now let me proceed with the lock chain diagnosis as outlined in the    playbook.\n\n**Step 1: Get the full blocking query chain**\nExcellent — I have identified a 3-level lock chain. Let me trace the hierarchy:\n- **PID 105718**: `SELECT pg_sleep(3600)` (blocks PID 105719)\n- **PID 105719**: `SELECT * FROM      _faulttest_lock_chain WHERE id=1 FOR UPDATE;` (blocks PID 105721)\n- **PID 105721**: `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR UPDATE;` (blocks PID 105720)\n- **PID 105720**: `SELECT * FROM _faulttest_lock_chain2 WHERE id=1 FOR    UPDATE;` (waiting)\n\nThe **root blocker** is **PID 105718** (no blocking_pid of its own, and its `blocking_query` is `SELECT pg_sleep(3600)`).\n\n**Step 2: Inspect the root blocker session (PID 105718)**\nPerfect. The root blocker session   details:\n- **PID**: 105718\n- **State**: active (executing `SELECT pg_sleep(3600)`)\n- **has_open_tx**: true (16 seconds old)\n- **has_writes**: yes (uncommitted DML)\n- **total_locks**: 4\n- **locked_tables**: _faulttest_lock_chain,        _faulttest_lock_chain_pkey\n- **Rollback estimate**: ~8s to ~32s\n\nThis is a classic root blocker scenario: an active statement (`pg_sleep(3600)`) that is holding transaction-level locks and preventing a cascade of downstream sessions from  acquiring their own locks.\n\n---\n\n## Diagnosis\n\n**HYPOTHESIS_1**: Root blocker (PID 105718) is executing a long-running statement (`SELECT pg_sleep(3600)`) within an open transaction that holds exclusive locks on                         `_faulttest_lock_chain`. This blocks PID 105719, which in turn blocks PID 105721, which in turn blocks PID 105720. The chain involves 4 sessions across 3 levels of waiting, all unable to proceed until the root's 3600-second sleep completes   (or the session terminates).\n\n**Evidence from get_blocking_queries**:\n- PID 105718 blocks PID 105719 via transactionid lock\n- PID 105719 blocks PID 105721 via transactionid lock\n- PID 105721 blocks PID 105720 via tuple lock on           `_faulttest_lock_chain2`\n\n**Evidence from get_session_info on PID 105718**:\n- State: active (16s in current state)\n- Last query: `SELECT pg_sleep(3600);`\n- has_open_tx: true\n- has_writes: yes\n- total_locks: 4\n- locked_tables:         _faulttest_lock_chain, _faulttest_lock_chain_pkey\n\n**HYPOTHESIS_2**: The lock chain could be broken by cancelling PID 105718 using `cancel_query`. | CONFIDENCE: 0.0 | REJECTED: cancel_query (pg_cancel_backend) sends SIGINT and is           unreliable for root blockers. The root session is actively executing pg_sleep(3600), which may or may not be interruptible, but even if the query is cancelled, the transaction and its locks persist until the connection closes. The lock queue remains blocked. Only terminate_connection (pg_terminate_backend / SIGTERM) unconditionally closes the connection and releases all held locks.\n\n---\n\n## Recommendation\n\nThe root blocker must be terminated using `terminate_connection`,   not `cancel_query`. Here is why:\n\n1. **cancel_query is ineffective**: Even if it successfully cancels the `pg_sleep(3600)` statement, the transaction remains open and all held locks (4 total, including those on `_faulttest_lock_chain` and  `_faulttest_lock_chain_pkey`) are still held. Downstream sessions continue waiting indefinitely.\n\n2. **terminate_connection is required**: `pg_terminate_backend(105718)` sends SIGTERM, which closes the connection unconditionally. This      immediately releases all 4 locks held by the root blocker and triggers a cascade:\n   - PID 105719 acquires its pending lock and completes\n   - PID 105721 (previously blocked by 105719) acquires its pending lock and completes\n   - PID      105720 (previously blocked by 105721) acquires its pending lock and completes\n\n3. **Multi-level chain note**: This is a 3-level chain, but the root blocker is always and only PID 105718 (blocking_pid = NULL). Terminating any intermediate   session (105719 or 105721) would only temporarily clear their downstream blockers; the root lock would persist and a replacement session would immediately requeue.\n\n4. **Uncommitted work**: has_writes=true indicates the root blocker has    uncommitted DML. Terminating the session will roll back this work. Rollback is estimated to take 8–32 seconds based on the transaction age and lock count. This is the expected outcome and the price of breaking the lock chain.                 \n\nHYPOTHESIS_1: Root blocker PID 105718 executes long-running pg_sleep(3600) within open transaction holding 4 locks across 3-level session chain | CONFIDENCE: 0.95 | EVIDENCE: \"blocking_query | SELECT pg_sleep(3600); blocking_pid |       105718\" and \"State: active (16s in current state)\" and \"has_open_tx: true\" and \"total_locks: 4\"\nHYPOTHESIS_2: cancel_query can break the chain | CONFIDENCE: 0.0 | REJECTED: cancel_query is unreliable for root blockers; it does not    release held locks regardless of whether the query is executing or idle, so the 4 locks persist and downstream sessions remain blocked indefinitely.\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "27.711713971s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 1,
      "judge_reasoning": "Agent correctly identified PID 105718 as the root blocker with no blocking_pid, explicitly rejected cancel_query with detailed explanation of why it fails (transaction and locks persist), recommended                 terminate_connection as the only solution, noted the 3-level chain structure, and explained the cascade effect and rollback consequences.",
      "judge_model": "claude-haiku-4-5-20251001",
      "remediation_attempted": true,
      "remediation_passed": true,
      "recovery_time_seconds": 0.052816417,
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


## Control Surfaces:

Here's a table of the actual control surfaces, top to bottom:

```
  ┌─────────────────────────────────┬────────────────────────────────────────────────────────────┬─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
  │              Layer              │                       What it gates                        │                                                        How to bypass                                                        │
  ├─────────────────────────────────┼────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
  │ Informed Gate (gate_escalation) │ The triage → remediation phase boundary                    │ Don't set gate_escalation=true on the run request. Gateway auto-chains.                                                     │
  ├─────────────────────────────────┼────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
  │ Step Approval (per-step)        │ Each destructive tool call inside the remediation playbook │ Request approval_mode=auto (or use a playbook authored as agent mode, which skips step gating entirely for read-only flows) │
  ├─────────────────────────────────┼────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
  │ Policy (internal/policy)        │ Action class + resource match                              │ Not bypassable per-request — only the policy file defines what's allowed                                                    │
  ├─────────────────────────────────┼────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
  │ Audit                           │ Every tool call, every decision                            │ Never bypassable; emits regardless                                                                                          │
  └─────────────────────────────────┴────────────────────────────────────────────────────────────┴─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

Bypassing the gate alone is straightforward, just omit the flag:

```
# Triage chains straight into remediation, no human pause at the boundary
curl -X POST .../playbooks/pb_xxx/run \
  -d '{"connection_string":"prod-db", "approval_mode":"review"}'
```

  In this mode the playbook still pauses at each destructive step (because `approval_mode=review` emits step approvals to the [Decision Hub](../DECISIONS.md)).

  Bypassing both the gate and step approvals, which amounts for the true "trust the chain" hands-free run is also easy:

```
  curl -X POST .../playbooks/pb_xxx/run \
    -d '{"connection_string":"prod-db", "approval_mode":"auto"}'
```

  But `approval_mode=auto` (and `force`) is role-clamped, which we saw in action in the above examples (until alice stepped in):

```
  level=WARN msg="gateway warning" ... "approval_mode clamped to \"manual\": override to \"force\" requires one of roles [dba_lead oncall_senior]"
```

  So an operator self-bypassing requires either `dba_lead` or `oncall_senior` (configurable in policy).
  Anyone below that gets silently downgraded back to the playbook's default approval mode, which is usually `manual` or `review`.
  This is the "trust but verify" tier: senior roles can pre-authorize a whole run, while everyone else can pre-authorize the path (skip the gate), but not the (destructive) steps.

   Finally, all of this leads to the following practical mode combinations for operators on different trust tiers:

```
  ┌──────────────────────┬─────────────────┬───────────────┬─────────────────────────────────────────────────────────────────────────┐
  │ Operator trust level │ gate_escalation │ approval_mode │                                 Result                                  │
  ├──────────────────────┼─────────────────┼───────────────┼─────────────────────────────────────────────────────────────────────────┤
  │ New / cautious       │ true            │ manual        │ Pause at boundary AND every step — most scrutiny                        │
  ├──────────────────────┼─────────────────┼───────────────┼─────────────────────────────────────────────────────────────────────────┤
  │ Routine on-call      │ false           │ review        │ Auto-chain to remediation, approve each destructive step                │
  ├──────────────────────┼─────────────────┼───────────────┼─────────────────────────────────────────────────────────────────────────┤
  │ Senior DBA           │ false           │ auto          │ Fully autonomous; no human in the loop after kickoff                    │
  ├──────────────────────┼─────────────────┼───────────────┼─────────────────────────────────────────────────────────────────────────┤
  │ Compliance run       │ true            │ manual        │ Forces explicit "yes" before any remediation begins, regardless of role │
  └──────────────────────┴─────────────────┴───────────────┴─────────────────────────────────────────────────────────────────────────┘
```

>
>  **Important caveat:**
>  Even in auto mode, the policy enforcer still runs on every tool call.
>  So "trust" only means "don't ask me again". It very explicitly does *NOT* mean "let through actions that policy would otherwise deny."
>
>  If your policy file forbids dropping replication slots on production, `approval_mode=auto` won't override that.
>  The tool call still gets denied, just without a human approval prompt sitting in front of it.
>

  So yes, the gate is bypassable and via `approval_mode=auto` the entire approval chain is bypassable. But only for principals that the company's policy says are allowed to bypass and only within what the policy permits each tool to do.
  The gate isn't a security control. It's a human-review control. The security controls are policy + audit and those are non-bypassable.

