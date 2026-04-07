# aiHelpDesk Rollback & Undo: sample run

Please see [here](ROLLBACK.md) for the full documentation reference of aiHelpDesk Rollback & Undo sub-module of [AI Governance](AIGOVERNANCE.md).

The sample log presented below is a simple-minded reverse of a `scale_deployment` mutation:

```
[boris@ ~/helpdesk]$ curl -s -X POST http://localhost:8080/api/v1/k8s/scale_deployment \
>     -H "Content-Type: application/json" \
>     -H "X-User: alice@example.com" \
>     -H "X-Purpose: remediation" \
>     -d '{"namespace": "production", "deployment": "api", "replicas": 1}'

{"agent":"k8s_agent","state":"completed","text":"deployment.apps/api scaled\n"}

-- K8 Agent:
time=2026-03-27T22:21:17.485-04:00 level=INFO msg="resolved namespace tags from sole cluster" namespace=production context=minikube tags=[development]
time=2026-03-27T22:21:18.096-04:00 level=INFO msg="using existing approval (cross-turn lookup)" approval_id=apr_b8887e71 resource=kubernetes:production
time=2026-03-27T22:21:18.261-04:00 level=INFO msg="tool ok" name=scale_deployment ms=36

-- auditd:
time=2026-03-27T22:21:18.133-04:00 level=INFO msg="policy decision: REQUIRE_APPROVAL" action=destructive resource_type=kubernetes resource_name=production effect=require_approval policy=authenticated-write user=alice@example.com              purpose=remediation
time=2026-03-27T22:21:18.134-04:00 level=INFO msg="policy check: REQUIRE_APPROVAL" event_id=pol_d8d00e94 resource=kubernetes:production action=destructive policy=authenticated-write
time=2026-03-27T22:21:18.413-04:00 level=INFO msg="policy decision: REQUIRE_APPROVAL" action=destructive resource_type=kubernetes resource_name=production effect=require_approval policy=authenticated-write user=alice@example.com              purpose=remediation
time=2026-03-27T22:21:18.414-04:00 level=INFO msg="policy check: REQUIRE_APPROVAL" event_id=pol_7c5643c9 resource=kubernetes:production action=destructive policy=authenticated-write


[boris@ ~/helpdesk]$ EVENT_ID=$(curl -s -H "X-User: alice@example.com" "http://localhost:1199/v1/events?event_type=tool_execution&tool_name=scale_deployment&limit=1" | jq -r '.[0].event_id')
[boris@ ~/helpdesk]$ echo "Event: $EVENT_ID"
Event: tool_94e7e364

[boris@ ~/helpdesk]$ curl -s -H "X-User: alice@example.com" "http://localhost:1199/v1/events/$EVENT_ID" | jq '.tool'
{
  "name": "scale_deployment",
  "agent": "k8s_agent",
  "parameters": {
    "args": [
      "scale",
      "deployment",
      "api",
      "--replicas",
      "1",
      "-n",
      "production"
    ],
    "context": ""
  },
  "raw_command": "kubectl scale deployment api --replicas 1 -n production",
  "result": "deployment.apps/api scaled\n",
  "duration_ms": 36161417,
  "pre_state": {
    "namespace": "production",
    "deployment": "api",
    "previous_replicas": 3
  }
}

[boris@ ~/helpdesk]$ curl -s -H "X-User: alice@example.com" "http://localhost:1199/v1/events/$EVENT_ID" | jq '.tool.pre_state'
{
  "namespace": "production",
  "deployment": "api",
  "previous_replicas": 3
}

-- Derive the rollback plan:

[boris@ ~/helpdesk]$ curl -s -H "X-User: alice@example.com" -X POST "http://localhost:1199/v1/events/$EVENT_ID/rollback-plan" | jq .
{
  "original_event_id": "tool_94e7e364",
  "original_tool": "scale_deployment",
  "original_trace_id": "dt_f708c272-e2b",
  "reversibility": "yes",
  "inverse_op": {
    "agent": "k8s",
    "tool": "scale_deployment",
    "args": {
      "deployment": "api",
      "namespace": "production",
      "replicas": 3
    },
    "description": "restore deployment/api in namespace production to 3 replica(s)"
  },
  "generated_at": "2026-03-28T02:45:59.292889Z"
}

-- Rollback dry-run:

[boris@ ~/helpdesk]$ curl -s -H "X-User: alice@example.com" -X POST http://localhost:1199/v1/rollbacks \
>     -H "Content-Type: application/json" \
>     -d "{\"original_event_id\": \"$EVENT_ID\", \"dry_run\": true}" | jq .

{
  "dry_run": true,
  "plan": {
    "original_event_id": "tool_94e7e364",
    "original_tool": "scale_deployment",
    "original_trace_id": "dt_f708c272-e2b",
    "reversibility": "yes",
    "inverse_op": {
      "agent": "k8s",
      "tool": "scale_deployment",
      "args": {
        "deployment": "api",
        "namespace": "production",
        "replicas": 3
      },
      "description": "restore deployment/api in namespace production to 3 replica(s)"
    },
    "generated_at": "2026-03-28T02:48:16.91437Z"
  }
}

-- Initiate and cancell a rollback:

[boris@ ~/helpdesk]$ ROLLBACK_ID=$(curl -s -H "X-User: alice@example.com" -X POST http://localhost:1199/v1/rollbacks \
>     -H "Content-Type: application/json" \
>     -d "{\"original_event_id\": \"$EVENT_ID\", \"justification\": \"test rollback\"}" \
>     | jq -r '.rollback.rollback_id')

[boris@ ~/helpdesk]$ echo "Rollback: $ROLLBACK_ID"
Rollback: rbk_c98ad7c5

[boris@ ~/helpdesk]$ curl -s -H "X-User: alice@example.com" "http://localhost:1199/v1/rollbacks/$ROLLBACK_ID" | jq .
{
  "plan": {
    "original_event_id": "tool_94e7e364",
    "original_tool": "scale_deployment",
    "original_trace_id": "dt_f708c272-e2b",
    "reversibility": "yes",
    "inverse_op": {
      "agent": "k8s",
      "tool": "scale_deployment",
      "args": {
        "deployment": "api",
        "namespace": "production",
        "replicas": 3
      },
      "description": "restore deployment/api in namespace production to 3 replica(s)"
    },
    "generated_at": "2026-03-28T02:50:40.676896Z"
  },
  "rollback": {
    "rollback_id": "rbk_c98ad7c5",
    "original_event_id": "tool_94e7e364",
    "original_trace_id": "dt_f708c272-e2b",
    "status": "pending_approval",
    "initiated_by": "alice@example.com",
    "initiated_at": "2026-03-28T02:50:40.677654Z",
    "rollback_trace_id": "tr_rbk_c98ad7c5",
    "plan_json": "{\"original_event_id\":\"tool_94e7e364\",\"original_tool\":\"scale_deployment\",\"original_trace_id\":\"dt_f708c272-e2b\",\"reversibility\":\"yes\",\"inverse_op\":{\"agent\":\"k8s\",\"tool\":\"scale_deployment\",\"args\":   {\"deployment\":\"api\",\"namespace\":\"production\",\"replicas\":3},\"description\":\"restore deployment/api in namespace production to 3 replica(s)\"},\"generated_at\":\"2026-03-28T02:50:40.676896Z\"}",
    "completed_at": "0001-01-01T00:00:00Z",
    "created_at": "2026-03-28T02:50:40.677654Z",
    "updated_at": "2026-03-28T02:50:40.677654Z"
  }
}

[boris@ ~/helpdesk]$ curl -s -H "X-User: alice@example.com" "http://localhost:1199/v1/rollbacks/$ROLLBACK_ID" | jq '{status: .rollback.status, plan: .plan.reversibility}'
{
  "status": "pending_approval",
  "plan": "yes"
}

[boris@ ~/helpdesk]$ go run ./cmd/approvals/main.go --url http://localhost:1199 list
ID            STATUS       ACTION       TOOL                  AGENT      REQUESTED  EXPIRES
apr_b8887e71  [?] pending  destructive  kubernetes:produc...  k8s_agent  22:17:40   54m

[boris@ ~/helpdesk]$ go run ./cmd/approvals/main.go --url http://localhost:1199 pending
ID            STATUS       ACTION       TOOL                  AGENT      REQUESTED  EXPIRES
apr_b8887e71  [?] pending  destructive  kubernetes:produc...  k8s_agent  22:17:40   54m

[boris@ ~/helpdesk]$ go run ./cmd/approvals/main.go --url http://localhost:1199 approve apr_b8887e71
Approved: apr_b8887e71
  Status:      approved
  Approved By: boris

-- ... or Cancel:

[boris@ ~/helpdesk]$ curl -s -H "X-User: alice@example.com" -X POST "http://localhost:1199/v1/rollbacks/$ROLLBACK_ID/cancel" | jq .
{
  "rollback": {
    "rollback_id": "rbk_c98ad7c5",
    "original_event_id": "tool_94e7e364",
    "original_trace_id": "dt_f708c272-e2b",
    "status": "cancelled",
    "initiated_by": "alice@example.com",
    "initiated_at": "2026-03-28T02:50:40.677654Z",
    "rollback_trace_id": "tr_rbk_c98ad7c5",
    "plan_json": "{\"original_event_id\":\"tool_94e7e364\",\"original_tool\":\"scale_deployment\",\"original_trace_id\":\"dt_f708c272-e2b\",\"reversibility\":\"yes\",\"inverse_op\":{\"agent\":\"k8s\",\"tool\":\"scale_deployment\",\"args\":   {\"deployment\":\"api\",\"namespace\":\"production\",\"replicas\":3},\"description\":\"restore deployment/api in namespace production to 3 replica(s)\"},\"generated_at\":\"2026-03-28T02:50:40.676896Z\"}",
    "completed_at": "0001-01-01T00:00:00Z",
    "created_at": "2026-03-28T02:50:40.677654Z",
    "updated_at": "2026-03-28T02:50:40.677654Z"
  }
}

-- ... and confirm the terminal state:

[boris@ ~/helpdesk]$ curl -s -H "X-User: alice@example.com" "http://localhost:1199/v1/rollbacks/$ROLLBACK_ID" | jq '.rollback.status'
"cancelled"


-- Check the audit trail:

[boris@ ~/helpdesk]$ RBK_TRACE=$(curl -s -H "X-User: alice@example.com" "http://localhost:1199/v1/rollbacks/$ROLLBACK_ID" | jq -r '.rollback.rollback_trace_id')
[boris@ ~/helpdesk]$ echo $RBK_TRACE
tr_rbk_c98ad7c5

[boris@ ~/helpdesk]$ curl -s -H "X-User: alice@example.com" "http://localhost:1199/v1/events?trace_id=$RBK_TRACE" | jq '.[].event_type'
"rollback_initiated"

[boris@ ~/helpdesk]$ curl -s -H "X-User: alice@example.com" 'http://localhost:1199/v1/journeys?trace_id=tr_rbk_c98ad7c5'|jq
[
  {
    "trace_id": "tr_rbk_c98ad7c5",
    "started_at": "2026-03-28T02:50:40.679145000Z",
    "ended_at": "2026-03-28T02:50:40.679145000Z",
    "duration_ms": 0,
    "tools_used": [],
    "event_count": 1
  }
]

[boris@ ~/helpdesk]$ sqlite3 -header -column /tmp/helpdesk/audit.db "SELECT id, timestamp, event_id, event_type, trace_id, session_agent sagent, session_id, action_class, tool_name, user_id, decision_agent,               decision_confidence confid, outcome_status outcome,     purpose pur, purpose_note purnote FROM audit_events ORDER BY timestamp DESC LIMIT 10"
id   timestamp                       event_id       event_type          trace_id         sagent  session_id         action_class  tool_name           user_id            decision_agent  confid  outcome           pur          purnote
---  ------------------------------  -------------  ------------------  ---------------  ------  -----------------  ------------  ------------------  -----------------  --------------  ------  ----------------  -----------  -------
313  2026-03-28T03:13:52.967635000Z  evt_eb1250a2   tool_execution                                                                restart_deployment                                             success
312  2026-03-28T02:55:27.963029000Z  rbk_564aeed0   rollback_initiated  tr_rbk_9a68887e          rbk_9a68887e       destructive                                                                  pending_approval
311  2026-03-28T02:50:40.679145000Z  rbk_e1c8e180   rollback_initiated  tr_rbk_c98ad7c5          rbk_c98ad7c5       destructive                                                                  pending_approval
309  2026-03-28T02:21:18.449613000Z  rty_b5553bc2   tool_retry          dt_f708c272-e2b          k8sagent_9b218fbe  read          scale_deployment                       k8s_agent               resolved
308  2026-03-28T02:21:18.414005000Z  pol_7c5643c9   policy_decision     dt_f708c272-e2b          dt_f708c272-e2b    destructive                                                                  require_approval  remediation
307  2026-03-28T02:21:18.208357000Z  tool_94e7e364  tool_execution      dt_f708c272-e2b          k8sagent_9b218fbe  destructive   scale_deployment                       k8s_agent               success
306  2026-03-28T02:21:18.133258000Z  pol_d8d00e94   policy_decision     dt_f708c272-e2b          dt_f708c272-e2b    destructive                                                                  require_approval  remediation
305  2026-03-28T02:21:17.946934000Z  pol_7ae52a5b   policy_decision     dt_f708c272-e2b          dt_f708c272-e2b    destructive                                                                  require_approval  remediation
304  2026-03-28T02:21:17.485592000Z  inv_8486dc8a   tool_invoked        dt_f708c272-e2b          k8sagent_9b218fbe  destructive
310  2026-03-28T02:21:17.481202000Z  gw_0a6d6fed    gateway_request     dt_f708c272-e2b          e2f83a2b           destructive   scale_deployment    alice@example.com  k8s_agent       1.0     success           remediation

```
