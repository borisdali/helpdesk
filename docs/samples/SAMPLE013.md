# aiHelpDesk Sample#13 (on Docker): Demo!

The raw sample commands and deliberations presented below complement this blog post: 

- **[Asking Your User to Click OK isn't Trust. Stop Apologizing for the Approval Gate](...)**
  How aiHelpDesk turned a liability disclosure into a safety net and what happened when we tried to fake the proof and failed.

If you are new to aiHelpDesk, start with aiHelpDesk innovative concept of the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifyiable on a live system. Your system.

Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel).

Finally, the question arises as to how the former (entitlements) map to the latter (the flywheel, which is essentially a loop)?

That's the question that [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md) the blog post attempt to address: the mapping.

---

If you are new to aiHelpDesk Demo, please review [this page](../../deploy/docker-compose/DEMO.md) first.

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This one happened to be running on Docker, but see [here](SAMPLE010.md), [here](SAMPLE011.md) and [here](SAMPLE012.md) for VM/Bare Metal, the source and K8s respectively (although not the exact commands shown on this page).

The tests below were conducted on Ubuntu 26.04 LTS (Resolute Raccoon): 

## Demo Test#1: Mode B (the default: interactive approval) + max connections exhausted (the default) fault

```
[boris@ ~/helpdesk/deploy/docker-compose]$ export COMPOSE_FILE=docker-compose.demo.yaml

[boris@ ~/helpdesk/deploy/docker-compose]$ docker compose down -v
[+] Running 7/7
 ✔ Container helpdesk-demo-runner         Removed                                                                                10.1s
 ✔ Container helpdesk-demo-gateway        Removed                                                                                 0.1s
 ✔ Container helpdesk-demo-db-agent       Removed                                                                                 0.1s
 ✔ Container helpdesk-demo-auditd         Removed                                                                                 0.1s
 ✔ Container helpdesk-demo-postgres       Removed                                                                                 0.2s
 ✔ Volume docker-compose_demo-audit-data  Removed                                                                                 0.0s
 ✔ Network docker-compose_default         Removed                                                                                 0.1s

[boris@ ~/helpdesk/deploy/docker-compose]$ docker compose up -d
[+] up 7/7
 ✔ Network docker-compose_default        Created                                                                                   0.0s
 ✔ Volume docker-compose_demo-audit-data Created                                                                                   0.0s
 ✔ Container helpdesk-demo-auditd        Healthy                                                                                   6.6s
 ✔ Container helpdesk-demo-postgres      Healthy                                                                                   6.0s
 ✔ Container helpdesk-demo-db-agent      Started                                                                                   6.0s
 ✔ Container helpdesk-demo-gateway       Healthy                                                                                  12.1s
 ✔ Container helpdesk-demo-runner        Started                                                                                  12.2s

[boris@ ~/helpdesk/deploy/docker-compose]$ docker compose run --rm demo-runner
[+]  4/4t 4/44
 ✔ Container helpdesk-demo-auditd   Healthy                                                                                        1.0s
 ✔ Container helpdesk-demo-db-agent Running                                                                                        0.0s
 ✔ Container helpdesk-demo-gateway  Running                                                                                        0.0s
 ✔ Container helpdesk-demo-postgres Healthy                                                                                        0.5s
Container helpdesk-demo-gateway Waiting
Container helpdesk-demo-gateway Healthy
Container docker-compose-demo-runner-run-13d84d5fbad3 Creating
Container docker-compose-demo-runner-run-13d84d5fbad3 Created
────────────────────────────────────────────────────────────────
  aiHelpDesk — Governed AI Incident Response Demo
────────────────────────────────────────────────────────────────

  Fault:     Max connections exhausted (db-max-connections)
  Mode:      interactive approval (mode B)
  Playbook:  pbs_connection_remediate
  Model:     anthropic / claude-haiku-4-5-20251001
  Gateway:   http://demo-gateway:8180

────────────────────────────────────────────────────────────────

▶ Step 1/5 — Waiting for services to be ready...
  Waiting for demo-postgres
✓ demo-postgres is ready
  Waiting for gateway
✓ gateway is ready

────────────────────────────────────────────────────────────────
▶ Step 2/5 — Injecting fault...
▶ Injecting fault: saturating connection pool with idle sessions...
▶   max_connections=30, superuser_reserved=3, opening 20 idle connections...
✓ Fault active: 20 idle connections holding the pool (max=30)

▶   Current database state:
 total_connections | idle | active
-------------------+------+--------
                26 |   20 |      1
(1 row)


────────────────────────────────────────────────────────────────
▶ Step 3/5 — Triggering playbook 'pbs_connection_remediate'...
▶   The step proposer is planning the first remediation action (15–45 seconds)...
✓ Playbook run started: plr_e922cf32

────────────────────────────────────────────────────────────────
▶ Step 4/5 — Step-approval gate...
  agent reading: get_active_connections

  ┌─────────────────────────────────────────────────────────┐
  │              STEP APPROVAL GATE                         │
  └─────────────────────────────────────────────────────────┘

  The AI agent has diagnosed the fault and proposes a remediation step.
  Before executing, human approval is required.

  Proposed action:  terminate_idle_connections
  Reasoning:        Step 1 confirms 20 idle connections (state='idle') with no idle-in-transaction sessions present. Step 2A requires a dry-run first to preview termination impact before requesting approval.
  Action class:     destructive
  Approval ID:      apr_6cbc9842

  This is aiHelpDesk's L2 autonomy gate — the agent proposed the action,
  but nothing executes until a human approves it.

  Press ENTER to approve this action, or Ctrl-C to abort.

  (In production: operators use the Decision Hub, Slack, or git-branch approval flow.)


✓ Gate approved.

  ┌─────────────────────────────────────────────────────────┐
  │              STEP APPROVAL GATE                         │
  └─────────────────────────────────────────────────────────┘

  The AI agent has diagnosed the fault and proposes a remediation step.
  Before executing, human approval is required.

  Proposed action:  terminate_idle_connections
  Reasoning:        The dry_run with idle_minutes=5 found zero idle connections to terminate because all 20 idle connections are under 5 minutes old (newly opened by pool/load test); now terminate all idle connections regardless of age per   guidance Step 2A.
  Action class:     destructive
  Approval ID:      apr_7b77f654

  This is aiHelpDesk's L2 autonomy gate — the agent proposed the action,
  but nothing executes until a human approves it.

  Press ENTER to approve this action, or Ctrl-C to abort.

  (In production: operators use the Decision Hub, Slack, or git-branch approval flow.)


✓ Gate approved.
  agent reading: get_active_connections
✓ Playbook completed — Connection overload remediation complete. Step 1 confirmed 20 idle connections (state='idle'), all from 172.20.0.7. Step 2A: All 20 idle connections were under 5 minutes old; terminated with idle_minutes=0, freeing 20  connection slots. Step 3: Verification confirms zero active connections remain, well below max_connections headroom. Step 4: Root cause is oversized or misconfigured application connection pool opening 20 connections from a single client     that were never returned on close. Recommend reviewing pool max_size and idle timeout settings.

────────────────────────────────────────────────────────────────

▶ Step 5/5 — Remediation complete.

  ┌─────────────────────────────────────────────────────────┐
  │              INCIDENT RESOLVED                          │
  └─────────────────────────────────────────────────────────┘

▶   Post-remediation database state:
 total_connections | idle | active
-------------------+------+--------
                 6 |    0 |      1
(1 row)


────────────────────────────────────────────────────────────────
  What just happened:

  1. A real fault was injected into a real PostgreSQL instance
  2. The aiHelpDesk database agent diagnosed it autonomously
  3. A step-approval gate surfaced — with blast radius and action class
  4. A human approved (or auto-approval ran after a countdown)
  5. The agent executed the remediation and verified recovery
  6. The full trace is now in the audit log (tamper-proof, hash-chained)

  Explore the audit trail:
    curl -s 'http://localhost:8180/api/v1/governance/events?limit=5' \
         -H 'Authorization: Bearer demo-api-key' | jq .

  View the journey (WHAT + WHY):
    curl -s 'http://localhost:8180/api/v1/governance/journeys?run_id=plr_e922cf32' \
         -H 'Authorization: Bearer demo-api-key' | jq .

  Try other faults:
    DEMO_FAULT=db-long-running-query   docker compose -f docker-compose.demo.yaml run --rm demo-runner
    DEMO_FAULT=db-tx-lock-chain-blocker docker compose -f docker-compose.demo.yaml run --rm demo-runner

────────────────────────────────────────────────────────────────
```

Let's list a few audit events:

```
[boris@ ~/helpdesk/deploy/docker-compose]$ curl -s 'http://localhost:8180/api/v1/governance/events?limit=5' \
         -H 'Authorization: Bearer demo-api-key' | jq .
[
  {
    "event_id": "tool_3add0dc2",
    "event_type": "tool_execution",
    "trace_id": "tr_130b88ee-a14",
    "origin": "direct_tool",
    "action_class": "read",
    "prev_hash": "f84fa210a3d601655bab27afcd8046d322661ca3acb9890a47c48000765d407a",
    "event_hash": "5216ecfda7e0ba732f350aa641c73553909e0c3fbdf6fb471a61a3eed65d2aeb",
    "session": {
      "id": "dbagent_4fc35449",
      "started_at": "0001-01-01T00:00:00Z",
      "delegation_count": 0
    },
    "input": {
      "user_query": "SELECT\n\t\tpid,\n\t\tusename as user,\n\t\tdatname as database,\n\t\tclient_addr,\n\t\tstate,\n\t\twait_event_type,\n\t\twait_event,\n\t\tEXTRACT(EPOCH FROM (now() - query_start))::int as query_seconds,\n\t\tLEFT(query, 100) as query_preview\n\tFROM pg_stat_activity\n\tWHERE pid != pg_backend_pid()\n\tAND state IS NOT NULL\n\tORDER BY query_start ASC NULLS LAST\n\tLIMIT 50;"
    },
    "tool": {
      "name": "get_active_connections",
      "agent": "postgres_database_agent",
      "parameters": {
        "connection_string": "host=demo-postgres port=5432 dbname=postgres user=postgres password=*** sslmode=disable"
      },
      "raw_command": "SELECT\n\t\tpid,\n\t\tusename as user,\n\t\tdatname as database,\n\t\tclient_addr,\n\t\tstate,\n\t\twait_event_type,\n\t\twait_event,\n\t\tEXTRACT(EPOCH FROM (now() - query_start))::int as query_seconds,                 \n\t\tLEFT(query, 100) as query_preview\n\tFROM pg_stat_activity\n\tWHERE pid != pg_backend_pid()\n\tAND state IS NOT NULL\n\tORDER BY query_start ASC NULLS LAST\n\tLIMIT 50;",
      "result": "(0 rows)\n\n",
      "duration_ms": 26338410
    },
    "outcome": {
      "status": "success",
      "duration_ms": 26338410
    },
    "timestamp": "2026-07-26T03:53:17.285946238Z"
  },
  {
    "event_id": "pol_fd2e00eb",
    "event_type": "policy_decision",
    "trace_id": "tr_130b88ee-a14",
    "action_class": "read",
    "prev_hash": "72a484386b33826453205e1611a54aef47384b9b56bd2fd704a3037007451b9c",
    "event_hash": "f84fa210a3d601655bab27afcd8046d322661ca3acb9890a47c48000765d407a",
    "session": {
      "id": "tr_130b88ee-a14",
      "started_at": "0001-01-01T00:00:00Z",
      "delegation_count": 0
    },
    "input": {
      "user_query": ""
    },
    "policy_decision": {
      "resource_type": "database",
      "resource_name": "demo-db",
      "action": "read",
...
```

... and the journey:

```
[boris@ ~/helpdesk/deploy/docker-compose]$ curl -s 'http://localhost:8180/api/v1/governance/journeys?run_id=plr_e922cf32' \
         -H 'Authorization: Bearer demo-api-key' | jq .
[
  {
    "trace_id": "tr_130b88ee-a14",
    "started_at": "2026-07-26T03:52:55.615612788Z",
    "ended_at": "2026-07-26T03:53:17.285946238Z",
    "duration_ms": 21670,
    "user_id": "demo@aihelpdesk.biz",
    "user_query": "Connection Overload — Terminate Idle Sessions",
    "purpose": "remediation",
    "agent": "postgres_database_agent",
    "tools_used": [
      "get_active_connections",
      "terminate_idle_connections",
      "terminate_idle_connections",
      "get_active_connections"
    ],
    "outcome": "success",
    "event_count": 15,
    "origin": "direct_tool",
    "incident_run_id": "plr_e922cf32"
  }
]
```

A few things worth pointing out in that demo run:

`user_query`: "Connection Overload — Terminate Idle Sessions". That's the playbook name (`pb.Name`) being used as the anchor's user query, which is correct since the demo doesn't pass a TriggerContext. In a production alert-triggered run, this would be the alert description.

`tools_used` is the accurate sequence: 
  `get_active_connections` → 
     `terminate_idle_connections` (idle_minutes=5, 0 terminated) → 
         `terminate_idle_connections` (idle_minutes=0, 20 terminated) → 
              `get_active_connections` (verification).

That's exactly what happened!

`incident_run_id`: `plr_e922cf32` — the round-trip link is complete.
        Journey → Incident is incident_run_id.
        Incident → Journey is ?run_id=plr_e922cf32.
        Both directions work as expected.


## Demo Test#2: Mode A (auto-approve) + long running query fault

```
[boris@ ~/helpdesk/deploy/docker-compose]$ DEMO_FAULT=db-long-running-query DEMO_MODE=auto docker compose run --rm demo-runner
[+]  4/4t 4/44
 ✔ Container helpdesk-demo-postgres Healthy                                                                                        0.5s
 ✔ Container helpdesk-demo-auditd   Healthy                                                                                        1.0s
 ✔ Container helpdesk-demo-db-agent Running                                                                                        0.0s
 ✔ Container helpdesk-demo-gateway  Running                                                                                        0.0s
Container helpdesk-demo-gateway Waiting
Container helpdesk-demo-gateway Healthy
Container docker-compose-demo-runner-run-e847c7d0d0e4 Creating
Container docker-compose-demo-runner-run-e847c7d0d0e4 Created
────────────────────────────────────────────────────────────────
  aiHelpDesk — Governed AI Incident Response Demo
────────────────────────────────────────────────────────────────

  Fault:     Long-running query blocking (db-long-running-query)
  Mode:      auto-approve (mode A)
  Playbook:  pbs_slow_query_remediate
  Model:     anthropic / claude-haiku-4-5-20251001
  Gateway:   http://demo-gateway:8180

────────────────────────────────────────────────────────────────

▶ Step 1/5 — Waiting for services to be ready...
  Waiting for demo-postgres
✓ demo-postgres is ready
  Waiting for gateway
✓ gateway is ready

────────────────────────────────────────────────────────────────
▶ Step 2/5 — Injecting fault...
▶ Injecting fault: starting a long-running query (pg_sleep 300s)...
✓ Fault active: long-running query pid=128 (pg_sleep 300s)
▶   Letting the fault age so the agent sees it as long-running (25s)...

▶   Current database state:
 total_connections | idle | active
-------------------+------+--------
                27 |   20 |      2
(1 row)


────────────────────────────────────────────────────────────────
▶ Step 3/5 — Triggering playbook 'pbs_slow_query_remediate'...
▶   The step proposer is planning the first remediation action (15–45 seconds)...
✓ Playbook run started: plr_efb2a2d5

────────────────────────────────────────────────────────────────
▶ Step 4/5 — Step-approval gate...

  agent reading: check_connection
  agent reading: get_active_connections
  agent reading: get_blocking_queries
  agent reading: get_session_info

  ┌─────────────────────────────────────────────────────────┐
  │              STEP APPROVAL GATE                         │
  └─────────────────────────────────────────────────────────┘

  The AI agent has diagnosed the fault and proposes a remediation step.
  Before executing, human approval is required.

  Proposed action:  cancel_query
  Reasoning:        PID 128 is a pg_sleep session (valid cancellation target per guidance) running for 34 seconds with no blocking queries detected; attempt graceful cancel first before considering terminate.
  Action class:     write
  Approval ID:      apr_e1fbe0b4

  This is aiHelpDesk's L2 autonomy gate — the agent proposed the action,
  but nothing executes until a human approves it.

  Auto-approve mode: approving in 8 seconds...
    Approving now...
✓ Gate approved automatically.
  agent reading: get_blocking_queries
✓ Playbook completed — Playbook execution successful. Step 1: Identified target session PID 128 (pg_sleep query, 29s duration, active state) — a valid cancellation candidate per guidance. Step 2: Inspected session (state=active,              has_writes=false, no lock wait). Step 3A: Issued cancel_query; confirmed cancelled=true. Step 4: Verified blockage cleared — get_blocking_queries returned no results both before and after cancellation. No downstream sessions blocked. No      further action required.

────────────────────────────────────────────────────────────────
▶ Step 5/5 — Remediation complete.

  ┌─────────────────────────────────────────────────────────┐
  │              INCIDENT RESOLVED                          │
  └─────────────────────────────────────────────────────────┘

▶   Post-remediation database state:
 total_connections | idle | active
-------------------+------+--------
                26 |   20 |      1
(1 row)


────────────────────────────────────────────────────────────────
  What just happened:

  1. A real fault was injected into a real PostgreSQL instance
  2. The aiHelpDesk database agent diagnosed it autonomously
  3. A step-approval gate surfaced — with blast radius and action class
  4. A human approved (or auto-approval ran after a countdown)
  5. The agent executed the remediation and verified recovery
  6. The full trace is now in the audit log (tamper-proof, hash-chained)

  Explore the audit trail:
    curl -s 'http://localhost:8180/api/v1/governance/events?limit=5' \
         -H 'Authorization: Bearer demo-api-key' | jq .

  View the journey (WHAT + WHY):
    curl -s 'http://localhost:8180/api/v1/governance/journeys?run_id=plr_efb2a2d5' \
         -H 'Authorization: Bearer demo-api-key' | jq .

  Try other faults:
    DEMO_FAULT=db-long-running-query   docker compose -f docker-compose.demo.yaml run --rm demo-runner
    DEMO_FAULT=db-tx-lock-chain-blocker docker compose -f docker-compose.demo.yaml run --rm demo-runner

────────────────────────────────────────────────────────────────
```

Let's get the journey for this demo run:

```
[boris@ ~/helpdesk/deploy/docker-compose]$ curl -s 'http://localhost:8180/api/v1/governance/journeys?run_id=plr_efb2a2d5' \
         -H 'Authorization: Bearer demo-api-key' | jq .
[
  {
    "trace_id": "tr_2fbee718-03c",
    "started_at": "2026-07-26T18:12:27.557842130Z",
    "ended_at": "2026-07-26T18:12:49.045099126Z",
    "duration_ms": 21487,
    "user_id": "demo@aihelpdesk.biz",
    "user_query": "Slow Query / Lock Contention — Cancel or Terminate Blocking Session",
    "purpose": "remediation",
    "agent": "postgres_database_agent",
    "tools_used": [
      "check_connection",
      "get_active_connections",
      "get_blocking_queries",
      "get_session_info",
      "get_session_info",
      "cancel_query",
      "get_blocking_queries"
    ],
    "outcome": "success",
    "event_count": 27,
    "retry_count": 1,
    "origin": "direct_tool",
    "incident_run_id": "plr_efb2a2d5"
  }
]
```

Let's review because all three elements came together in this run:

1/ Query aged to 34s (with the 25s sleep, which gave it time past the threshold before the agent checked).
2/ Agent used `get_active_connections` first and not `get_slow_queries`. Then it found the `pg_sleep` correctly and flagged it "valid cancellation target per guidance". Good!
3/ Gate fired on `cancel_query`, auto-approved as requested by Mode A and the cancel confirmed.
4/ Journey returned with `incident_run_id: "plr_efb2a2d5"`an both directions of the Journey↔Incident linkage work as expected.

The db-long-running-query fault is clean e2e: fault injected, gate fired twice, tools executed, recovery verified, audit trail queryable, journey linked to incident. The demo output is exactly right. W00t!



## Demo Test#3: Mode C (clamping) + tx locking fault: Journeys

```
[boris@ ~/helpdesk/deploy/docker-compose]$ DEMO_FAULT=db-tx-lock-chain-blocker DEMO_MODE=clamping docker compose run --rm demo-runner
[+]  4/4t 4/44
 ✔ Container helpdesk-demo-postgres Healthy                                                                                        0.5s
 ✔ Container helpdesk-demo-auditd   Healthy                                                                                        1.0s
 ✔ Container helpdesk-demo-db-agent Running                                                                                        0.0s
 ✔ Container helpdesk-demo-gateway  Running                                                                                        0.0s
Container helpdesk-demo-gateway Waiting
Container helpdesk-demo-gateway Healthy
Container docker-compose-demo-runner-run-7344f056150d Creating
Container docker-compose-demo-runner-run-7344f056150d Created

────────────────────────────────────────────────────────────────
  aiHelpDesk — Governed AI Incident Response Demo
────────────────────────────────────────────────────────────────

  Fault:     Transaction lock chain — active root blocker (db-tx-lock-chain-blocker)
  Mode:      governance bypass demo (mode C)
  Playbook:  pbs_lock_chain_remediate
  Model:     anthropic / claude-haiku-4-5-20251001
  Gateway:   http://demo-gateway:8180

────────────────────────────────────────────────────────────────

▶ Step 1/1 — Waiting for services to be ready...
  Waiting for demo-postgres
✓ demo-postgres is ready
  Waiting for gateway
✓ gateway is ready

────────────────────────────────────────────────────────────────

  Mode C — Governance Bypass: force + approval_override_roles

  Two runs. Same fault. Same playbook. Different roles.

  Run 1: operator@aihelpdesk.biz requests force
         → clamped (no dba_lead) → gate still fires
  Run 2: demo@aihelpdesk.biz requests force
         → accepted (has dba_lead) → gate fires but authority is clear

  Config: infrastructure.json sets approval_override_roles: [dba_lead]

────────────────────────────────────────────────────────────────

▶ Step 1 — Inject fault...
▶ Injecting fault: creating a transaction lock chain...
✓ Fault active: lock chain established (0 sessions waiting on lock)

────────────────────────────────────────────────────────────────
  ┌─────────────────────────────────────────────────────────┐
  │  Run 1/2 — operator@aihelpdesk.biz
  │  approval_mode: force    role: sre, operator (no dba_lead)
  └─────────────────────────────────────────────────────────┘

▶   Triggering pbs_lock_chain_remediate as operator@aihelpdesk.biz with approval_mode=force...

  Requested:          force
  Effective:          manual  ← clamped
⚠   Gateway: approval_mode clamped — operator@aihelpdesk.biz lacks dba_lead
  Run ID:             plr_75543a1c

✓   Gate fired as expected — force was clamped, approval still required.
  Proposed action:    get_blocking_queries
  Action class:       read

  Denying step — this run was for proof of clamping, not remediation.
{"run_id":"plr_75543a1c","status":"denied"}
✓   Step denied. Run 1 stopped. Governance held.

▶   Tearing down fault before Run 2...
────────────────────────────────────────────────────────────────
  ┌─────────────────────────────────────────────────────────┐
  │  Run 2/2 — demo@aihelpdesk.biz
  │  approval_mode: force    role: dba, sre, operator, dba_lead
  └─────────────────────────────────────────────────────────┘

▶   Re-injecting fault...
▶ Injecting fault: creating a transaction lock chain...
✓ Fault active: lock chain established (0 sessions waiting on lock)
▶   Letting the lock chain settle (5s)...

▶   Triggering pbs_lock_chain_remediate as demo@aihelpdesk.biz with approval_mode=force...

  Requested:          force
  Effective:          force  ← accepted, no clamping
✓   Gateway accepted force — dba_lead role verified.
  Run ID:             plr_8bc8be11


  ┌─────────────────────────────────────────────────────────┐
  │              STEP APPROVAL GATE                         │
  └─────────────────────────────────────────────────────────┘

  The AI agent has diagnosed the fault and proposes a remediation step.
  Before executing, human approval is required.

  Proposed action:  get_blocking_queries
  Reasoning:        Step 1: Identify the root blocker (blocking_pid = NULL) and all intermediate sessions in the lock chain before proceeding to inspection and termination.
  Action class:     read
  Approval ID:      apr_815bffd4

  This is aiHelpDesk's L2 autonomy gate — the agent proposed the action,
  but nothing executes until a human approves it.

  The gate fired — even with force, agent_approve always proposes steps one at a time.
  The difference: this user's force request was NOT downgraded. They have the authority.

  Auto-approving (demo@aihelpdesk.biz is authorized)...
✓   Completed — Playbook execution complete. Step 1 (get_blocking_queries) confirmed no blocking queries exist in the system. Since there are no blocking chains, no root blocker to identify, no intermediate sessions to inspect, and no        terminations required, all playbook objectives have been satisfied. The database is in a healthy state with respect to transaction lock chains.

────────────────────────────────────────────────────────────────
  What you just saw:

  Run 1 (operator@aihelpdesk.biz)
    requested=force  effective=manual    gate=fired  verdict=denied

  Run 2 (demo@aihelpdesk.biz)
    requested=force  effective=force     gate=fired  verdict=approved

  The gate fired in both runs — agent_approve always proposes steps one at a time.
  The only difference visible to the audit trail: one user's force request
  was silently downgraded. The other's was accepted. One line in users.yaml
  and one field in infrastructure.json control who holds the key.

  Journeys (WHAT + WHY):
    curl -s 'http://localhost:8180/api/v1/governance/journeys?run_id=plr_75543a1c' \n         -H 'Authorization: Bearer demo-api-key' | jq .
    curl -s 'http://localhost:8180/api/v1/governance/journeys?run_id=plr_8bc8be11' \n         -H 'Authorization: Bearer demo-api-key' | jq .

  Audit trail (both runs):
    curl -s 'http://localhost:8180/api/v1/governance/events?limit=30' \n         -H 'Authorization: Bearer demo-api-key' | jq '.[] | {user,tool,action_class}'

────────────────────────────────────────────────────────────────
```

Let's check the journeys for both runs:


```
[boris@ ~/helpdesk/deploy/docker-compose]$ curl -s 'http://localhost:8180/api/v1/governance/journeys?run_id=plr_75543a1c' \n         -H 'Authorization: Bearer demo-api-key' | jq .
[
  {
    "trace_id": "tr_90f6e536-05b",
    "started_at": "2026-07-26T19:25:33.465787766Z",
    "ended_at": "2026-07-26T19:25:33.465787766Z",
    "duration_ms": 0,
    "user_id": "operator@aihelpdesk.biz",
    "user_query": "Transaction Lock Chain — Terminate Root Blocker",
    "purpose": "remediation",
    "agent": "postgres_database_agent",
    "tools_used": [],
    "event_count": 1,
    "incident_run_id": "plr_75543a1c"
  }
]

[boris@ ~/helpdesk/deploy/docker-compose]$ curl -s 'http://localhost:8180/api/v1/governance/journeys?run_id=plr_8bc8be11' \n         -H 'Authorization: Bearer demo-api-key' | jq .
[
  {
    "trace_id": "tr_853c222f-e52",
    "started_at": "2026-07-26T19:25:45.356176870Z",
    "ended_at": "2026-07-26T19:25:47.283458271Z",
    "duration_ms": 1927,
    "user_id": "demo@aihelpdesk.biz",
    "user_query": "Transaction Lock Chain — Terminate Root Blocker",
    "purpose": "remediation",
    "agent": "postgres_database_agent",
    "tools_used": [
      "get_blocking_queries"
    ],
    "outcome": "success",
    "event_count": 4,
    "origin": "direct_tool",
    "incident_run_id": "plr_8bc8be11"
  }
]
```

The [journey](../JOURNEYS.md) linkage for both runs works perfectly: `plr_75543a1c` (denied, tools_used: []) and `plr_8bc8be11` (approved, `tools_used: ["get_blocking_queries"]`) both have `incident_run_id` set.

But notice the "no blocking queries" in Run 2, which means that Run 2 was missing the lock chain. Indeed, the check inside `inject_fault` already shows "0 sessions waiting on lock",  so session 2 hasn't blocked yet when the trigger fired.
The problem with this run turned out to be a bug of not properly polling until the waiter appears (rather than sleep a fixed amount).

## Demo Test#4: Mode C (clamping) + tx locking fault: Final run

```
[boris@ ~/helpdesk/deploy/docker-compose]$ DEMO_FAULT=db-tx-lock-chain-blocker DEMO_MODE=clamping docker compose run --rm demo-runner
[+]  4/4t 4/44
 ✔ Container helpdesk-demo-db-agent Running                                                                                        0.0s
 ✔ Container helpdesk-demo-gateway  Running                                                                                        0.0s
 ✔ Container helpdesk-demo-postgres Healthy                                                                                        0.5s
 ✔ Container helpdesk-demo-auditd   Healthy                                                                                        1.0s
Container helpdesk-demo-gateway Waiting
Container helpdesk-demo-gateway Healthy
Container docker-compose-demo-runner-run-127ee27d628d Creating
Container docker-compose-demo-runner-run-127ee27d628d Created
────────────────────────────────────────────────────────────────
  aiHelpDesk — Governed AI Incident Response Demo
────────────────────────────────────────────────────────────────

  Fault:     Transaction lock chain — active root blocker (db-tx-lock-chain-blocker)
  Mode:      governance bypass demo (mode C)
  Playbook:  pbs_lock_chain_remediate
  Model:     anthropic / claude-haiku-4-5-20251001
  Gateway:   http://demo-gateway:8180

────────────────────────────────────────────────────────────────

▶ Step 1/1 — Waiting for services to be ready...
  Waiting for demo-postgres
✓ demo-postgres is ready
  Waiting for gateway
✓ gateway is ready

────────────────────────────────────────────────────────────────

  Mode C — Governance Bypass: force + approval_override_roles

  Two runs. Same fault. Same playbook. Different roles.

  Run 1: operator@aihelpdesk.biz requests force
         → clamped (no dba_lead) → gate still fires
  Run 2: demo@aihelpdesk.biz requests force
         → accepted (has dba_lead) → gate fires but authority is clear

  Config: infrastructure.json sets approval_override_roles: [dba_lead]

────────────────────────────────────────────────────────────────

▶ Step 1 — Inject fault...
▶ Injecting fault: creating a transaction lock chain...
✓ Fault active: lock chain established (1 sessions waiting on lock)

────────────────────────────────────────────────────────────────
  ┌─────────────────────────────────────────────────────────┐
  │  Run 1/2 — operator@aihelpdesk.biz
  │  approval_mode: force    role: sre, operator (no dba_lead)
  └─────────────────────────────────────────────────────────┘

▶   Triggering pbs_lock_chain_remediate as operator@aihelpdesk.biz with approval_mode=force...

  Requested:          force
  Effective:          manual  ← clamped
⚠   Gateway: approval_mode clamped — operator@aihelpdesk.biz lacks dba_lead
  Run ID:             plr_f0feded3

✓   Gate fired as expected — force was clamped, approval still required.
  Proposed action:    get_blocking_queries
  Action class:       read

  Denying step — this run was for proof of clamping, not remediation.
{"run_id":"plr_f0feded3","status":"denied"}
✓   Step denied. Run 1 stopped. Governance held.

▶   Tearing down fault before Run 2...
────────────────────────────────────────────────────────────────
  ┌─────────────────────────────────────────────────────────┐
  │  Run 2/2 — demo@aihelpdesk.biz
  │  approval_mode: force    role: dba, sre, operator, dba_lead
  └─────────────────────────────────────────────────────────┘

▶   Re-injecting fault...
▶ Injecting fault: creating a transaction lock chain...
✓ Fault active: lock chain established (1 sessions waiting on lock)
▶   Waiting for lock chain to establish...
✓   Lock chain confirmed (1 session(s) waiting)

▶   Triggering pbs_lock_chain_remediate as demo@aihelpdesk.biz with approval_mode=force...

  Requested:          force
  Effective:          force  ← accepted, no clamping
✓   Gateway accepted force — dba_lead role verified.
  Run ID:             plr_8d9b0233

  agent reading: get_blocking_queries
  agent reading: get_session_info
  agent reading: get_session_info

  ┌─────────────────────────────────────────────────────────┐
  │              STEP APPROVAL GATE                         │
  └─────────────────────────────────────────────────────────┘

  The AI agent has diagnosed the fault and proposes a remediation step.
  Before executing, human approval is required.

  Proposed action:  terminate_connection
  Reasoning:        Step 3: Terminate the root blocker (PID 4984, blocking_pid=NULL) which is active in a read-only transaction with no writes and low cascade risk to the intermediate (PID 4985, also read-only); no operator approval needed   as both sessions are read-only.
  Action class:     destructive
  Approval ID:      apr_a916a070

  This is aiHelpDesk's L2 autonomy gate — the agent proposed the action,
  but nothing executes until a human approves it.

  The gate fired — even with force, agent_approve always proposes steps one at a time.
  The difference: this user's force request was NOT downgraded. They have the authority.

  Auto-approving (demo@aihelpdesk.biz is authorized)...

  agent reading: get_blocking_queries
✓   Completed — Transaction lock chain remediation completed successfully. Root blocker PID 4984 (blocking_pid=NULL, state=active, has_writes=false) was identified and terminated. Intermediate session PID 4985 was confirmed to have no writes (read-only, rollback instant). Post-termination verification via get_blocking_queries confirms no blocking queries remain—cascade cleared and lock contention resolved.

────────────────────────────────────────────────────────────────
  What you just saw:

  Run 1 (operator@aihelpdesk.biz)
    requested=force  effective=manual    gate=fired  verdict=denied

  Run 2 (demo@aihelpdesk.biz)
    requested=force  effective=force     gate=fired  verdict=approved

  The gate fired in both runs — agent_approve always proposes steps one at a time.
  The only difference visible to the audit trail: one user's force request
  was silently downgraded. The other's was accepted. One line in users.yaml
  and one field in infrastructure.json control who holds the key.

  Journeys (WHAT + WHY):
    curl -s 'http://localhost:8180/api/v1/governance/journeys?run_id=plr_f0feded3' \n         -H 'Authorization: Bearer demo-api-key' | jq .
    curl -s 'http://localhost:8180/api/v1/governance/journeys?run_id=plr_8d9b0233' \n         -H 'Authorization: Bearer demo-api-key' | jq .

  Audit trail (both runs):
    curl -s 'http://localhost:8180/api/v1/governance/events?limit=30' \n         -H 'Authorization: Bearer demo-api-key' | jq '.[] | {user,tool,action_class}'
────────────────────────────────────────────────────────────────
```

That's the complete Mode C story:

Lock chain established — both runs confirm "1 session(s) waiting".
The teardown only kills the two advisory lock sessions rather than all backends (which was disrupting the db-agent's connection pool). Advisory locks show up in `pg_blocking_pids()` so `get_blocking_queries` finds them and propose `terminate_connection` on the root blocker. The diff between the two runs:

  - Run 1: force clamped → gate fired → denied. Governance held.
  - Run 2: force accepted → reads silently approved → `terminate_connection` gate fires (destructive) → approved → root blocker PID 4984 terminated → verified cleared.

The agent's reasoning in the gate is clean: identifies the root blocker (blocking_pid=NULL), notes both sessions are read-only (`has_writes=false`, rollback instant), proposes `terminate_connection`, and post-verification confirms the cascade cleared.






## Triage Consistency Cert

```
[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest run \
>        --external \
>        --ids db-max-connections,db-long-running-query,db-tx-lock-chain-blocker \
>        --repeat 5 \
>        --conn "host=localhost port=5434 dbname=postgres user=postgres password=demopassword sslmode=disable" \
>        --agent-conn "host=demo-postgres port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable" \
>        --gateway http://localhost:8180 \
>        --api-key demo-api-key \
>        --agent-model claude-haiku-4-5-20251001
Tue Jul 28 19:32:02 EDT 2026
time=2026-07-28T19:32:02.919-04:00 level=INFO msg=--conn host=localhost
time=2026-07-28T19:32:02.920-04:00 level=INFO msg=--agent-conn host=demo-postgres

--- Testing: Max connections exhausted (db-max-connections) — 5 runs ---

  Run 1/5
time=2026-07-28T19:32:02.922-04:00 level=INFO msg="injecting failure" id=db-max-connections type=shell_exec mode=external conn=localhost
time=2026-07-28T19:32:06.101-04:00 level=INFO msg="shell_exec completed" output="Injected: 26 idle connections (1 existing → 27/30)"
time=2026-07-28T19:32:06.271-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-max-connections series_id=pbs_connection_triage playbook_id=pb_e3a2af9b gateway=http://localhost:8180 agent-conn="host=demo-postgres          port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:32:23.869-04:00 level=INFO msg="tearing down failure" id=db-max-connections type=shell_exec conn=localhost
time=2026-07-28T19:32:25.946-04:00 level=INFO msg="shell_exec completed" output="Teardown: idle connections terminated"
  [PASS] score=100%
         [PRIMARY 95%] Connection saturation due to accumulated idle connections with no automatic cleanup (idle_session_timeout disabled)
         [REJECTED 5%] Blocking transaction preventing connection slots from being freed — get_blocking_queries returned "No blocking queries found" and get_lock_info returned "No blocking locks found"

  Run 2/5
time=2026-07-28T19:32:25.946-04:00 level=INFO msg="injecting failure" id=db-max-connections type=shell_exec mode=external conn=localhost
time=2026-07-28T19:32:29.092-04:00 level=INFO msg="shell_exec completed" output="Injected: 26 idle connections (1 existing → 27/30)"
time=2026-07-28T19:32:29.187-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-max-connections series_id=pbs_connection_triage playbook_id=pb_e3a2af9b gateway=http://localhost:8180 agent-conn="host=demo-postgres          port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:32:51.778-04:00 level=INFO msg="tearing down failure" id=db-max-connections type=shell_exec conn=localhost
time=2026-07-28T19:32:53.856-04:00 level=INFO msg="shell_exec completed" output="Teardown: idle connections terminated"
  [PASS] score=75%
         [PRIMARY 95%] Idle session accumulation due to disabled idle_session_timeout and low max_connections limit
         [REJECTED 0%] Blocking query preventing new connections — get_blocking_queries returned "No blocking queries found"

  Run 3/5
time=2026-07-28T19:32:53.856-04:00 level=INFO msg="injecting failure" id=db-max-connections type=shell_exec mode=external conn=localhost
time=2026-07-28T19:32:56.997-04:00 level=INFO msg="shell_exec completed" output="Injected: 26 idle connections (1 existing → 27/30)"
time=2026-07-28T19:32:57.088-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-max-connections series_id=pbs_connection_triage playbook_id=pb_e3a2af9b gateway=http://localhost:8180 agent-conn="host=demo-postgres          port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:33:23.749-04:00 level=INFO msg="tearing down failure" id=db-max-connections type=shell_exec conn=localhost
time=2026-07-28T19:33:25.831-04:00 level=INFO msg="shell_exec completed" output="Teardown: idle connections terminated"
  [PASS] score=85%
         [PRIMARY 95%] Connection pool saturation caused by accumulated idle sessions exceeding max_connections limit; idle_session_timeout=0 prevents automatic termination
         [REJECTED 5%] Lock contention preventing connection release — get_blocking_queries returned "No blocking queries found" and sampled sessions show "No open transaction" with no writes

  Run 4/5
time=2026-07-28T19:33:25.831-04:00 level=INFO msg="injecting failure" id=db-max-connections type=shell_exec mode=external conn=localhost
time=2026-07-28T19:33:28.969-04:00 level=INFO msg="shell_exec completed" output="Injected: 26 idle connections (1 existing → 27/30)"
time=2026-07-28T19:33:29.068-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-max-connections series_id=pbs_connection_triage playbook_id=pb_e3a2af9b gateway=http://localhost:8180 agent-conn="host=demo-postgres          port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:33:49.562-04:00 level=INFO msg="tearing down failure" id=db-max-connections type=shell_exec conn=localhost
time=2026-07-28T19:33:51.618-04:00 level=INFO msg="shell_exec completed" output="Teardown: idle connections terminated"
  [PASS] score=85%
         [PRIMARY 99%] Idle connection saturation causing "too many clients" error
         [REJECTED 85%] Connection limit too low for application workload — Root cause is idle connection accumulation, not legitimate active load; max_connections=30 is intentionally set low but application did not clean up idle sessions    because idle_session_timeout is disabled.

  Run 5/5
time=2026-07-28T19:33:51.618-04:00 level=INFO msg="injecting failure" id=db-max-connections type=shell_exec mode=external conn=localhost
time=2026-07-28T19:33:54.759-04:00 level=INFO msg="shell_exec completed" output="Injected: 26 idle connections (1 existing → 27/30)"
time=2026-07-28T19:33:54.868-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-max-connections series_id=pbs_connection_triage playbook_id=pb_e3a2af9b gateway=http://localhost:8180 agent-conn="host=demo-postgres          port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:34:11.655-04:00 level=INFO msg="tearing down failure" id=db-max-connections type=shell_exec conn=localhost
time=2026-07-28T19:34:13.734-04:00 level=INFO msg="shell_exec completed" output="Teardown: idle connections terminated"
  [PASS] score=100%
         [PRIMARY 99%] Idle connection accumulation from a client that is not properly releasing connections between requests, combined with max_connections set to 30 (below the volume of idle clients), causing new legitimate connections to  be rejected with "too many clients" error
         [REJECTED 1%] Lock contention or blocking transactions preventing connection release — "No blocking queries found" and "No blocking locks found" — the active query is minimal and idle sessions hold no locks
time=2026-07-28T19:34:13.821-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (5 runs):
    Pass rate:    5/5 (100%)
    Confidence:   min=95% max=99% range=4pp mean=97%  (H1, passing runs only)
    Verdict:      STABLE
    Attribution:  idle-connection-accumulation (5/5)  consistent: yes  [taxonomy 1.0]
  ────────────────────────────────────────────────────────────────
time=2026-07-28T19:34:16.973-04:00 level=INFO msg="fault stability cert posted" fault_id=db-max-connections verdict=STABLE n_runs=5

--- Testing: Long-running query blocking (db-long-running-query) — 5 runs ---

  Run 1/5
time=2026-07-28T19:34:16.973-04:00 level=INFO msg="injecting failure" id=db-long-running-query type=shell_exec mode=external conn=localhost
time=2026-07-28T19:34:18.017-04:00 level=INFO msg="shell_exec completed" output="NOTICE:  relation \"_faulttest_longquery\" already exists, skipping\nCREATE TABLE\nINSERT 0 0\nInjected: ACCESS EXCLUSIVE lock on _faulttest_longquery (held     until killed)"
time=2026-07-28T19:34:18.113-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-long-running-query series_id=pbs_slow_query_triage playbook_id=pb_471590f3 gateway=http://localhost:8180 agent-conn="host=demo-postgres       port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:34:42.088-04:00 level=INFO msg="tearing down failure" id=db-long-running-query type=shell_exec conn=localhost
time=2026-07-28T19:34:42.147-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
  [PASS] score=90%

  Run 2/5
time=2026-07-28T19:34:42.147-04:00 level=INFO msg="injecting failure" id=db-long-running-query type=shell_exec mode=external conn=localhost
time=2026-07-28T19:34:43.183-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: ACCESS EXCLUSIVE lock on _faulttest_longquery (held until killed)"
time=2026-07-28T19:34:43.279-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-long-running-query series_id=pbs_slow_query_triage playbook_id=pb_471590f3 gateway=http://localhost:8180 agent-conn="host=demo-postgres       port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:35:00.070-04:00 level=INFO msg="tearing down failure" id=db-long-running-query type=shell_exec conn=localhost
time=2026-07-28T19:35:00.144-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
  [PASS] score=100%
         [PRIMARY 95%] Orphaned idle transaction holding ACCESS EXCLUSIVE lock on _faulttest_longquery, blocking all other access to that table
         [REJECTED 5%] Lock contention from concurrent writers on _faulttest_longquery — get_blocking_queries returned no active blocking chain and lock_info shows no contention—the issue is a single idle holder, not competition

  Run 3/5
time=2026-07-28T19:35:00.144-04:00 level=INFO msg="injecting failure" id=db-long-running-query type=shell_exec mode=external conn=localhost
time=2026-07-28T19:35:01.178-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: ACCESS EXCLUSIVE lock on _faulttest_longquery (held until killed)"
time=2026-07-28T19:35:01.275-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-long-running-query series_id=pbs_slow_query_triage playbook_id=pb_471590f3 gateway=http://localhost:8180 agent-conn="host=demo-postgres       port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:35:34.553-04:00 level=INFO msg="tearing down failure" id=db-long-running-query type=shell_exec conn=localhost
time=2026-07-28T19:35:34.612-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
  [PASS] score=100%
         [PRIMARY 99%] Session pid 18834 is holding an ACCESS EXCLUSIVE lock on _faulttest_longquery while idle in transaction, blocking all access to that table and causing queries to hang indefinitely.
         [REJECTED 1%] There is a deadlock or circular lock dependency causing hangs. — get_lock_info and get_blocking_queries both returned no blocking locks, eliminating lock cycles as the cause.

  Run 4/5
time=2026-07-28T19:35:34.612-04:00 level=INFO msg="injecting failure" id=db-long-running-query type=shell_exec mode=external conn=localhost
time=2026-07-28T19:35:35.643-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: ACCESS EXCLUSIVE lock on _faulttest_longquery (held until killed)"
time=2026-07-28T19:35:35.739-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-long-running-query series_id=pbs_slow_query_triage playbook_id=pb_471590f3 gateway=http://localhost:8180 agent-conn="host=demo-postgres       port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:35:58.479-04:00 level=INFO msg="tearing down failure" id=db-long-running-query type=shell_exec conn=localhost
time=2026-07-28T19:35:58.541-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
  [PASS] score=90%
         [PRIMARY 95%] Long-running transaction with exclusive table lock blocking access to _faulttest_longquery
         [REJECTED 5%] Deadlock or lock contention cycle — get_blocking_queries returned no blocking chain; no other queries are currently waiting on locks.

  Run 5/5
time=2026-07-28T19:35:58.541-04:00 level=INFO msg="injecting failure" id=db-long-running-query type=shell_exec mode=external conn=localhost
time=2026-07-28T19:35:59.574-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: ACCESS EXCLUSIVE lock on _faulttest_longquery (held until killed)"
time=2026-07-28T19:35:59.679-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-long-running-query series_id=pbs_slow_query_triage playbook_id=pb_471590f3 gateway=http://localhost:8180 agent-conn="host=demo-postgres       port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:36:16.645-04:00 level=INFO msg="tearing down failure" id=db-long-running-query type=shell_exec conn=localhost
time=2026-07-28T19:36:16.706-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
  [PASS] score=90%
         [PRIMARY 95%] Idle-in-transaction session (pid 18948) holding ACCESS EXCLUSIVE lock on _faulttest_longquery
         [REJECTED 0%] pg_stat_statements not installed preventing query analysis — This is a gap in diagnostics but not the root cause of inaccessibility; the lock is the active blocker.
time=2026-07-28T19:36:16.777-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (5 runs):
    Pass rate:    5/5 (100%)
    Confidence:   min=95% max=99% range=4pp mean=96%  (H1, passing runs only)
    Verdict:      STABLE
    Attribution:  lock-contention-blocking-queries (5/5)  consistent: yes  [taxonomy 1.0]
  ────────────────────────────────────────────────────────────────
time=2026-07-28T19:36:20.587-04:00 level=INFO msg="fault stability cert posted" fault_id=db-long-running-query verdict=STABLE n_runs=5

--- Testing: Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) — 5 runs ---

  Run 1/5
time=2026-07-28T19:36:20.587-04:00 level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external conn=localhost
time=2026-07-28T19:36:23.644-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on      chain), leaves C/D (blocked on chain2)"
time=2026-07-28T19:36:23.734-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_6a50c4ce gateway=http://localhost:8180 agent-conn="host=demo-postgres    port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:36:41.181-04:00 level=INFO msg="tearing down failure" id=db-tx-lock-chain-blocker type=shell_exec conn=localhost
time=2026-07-28T19:36:41.224-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n t\n t\n t\n(4 rows)\n\nDROP TABLE\nDROP TABLE"
  [PASS] score=100%
         [PRIMARY 99%] Root blocker PID 18999 is actively executing pg_sleep(3600) with an open transaction holding 4 locks across _faulttest_lock_chain, causing a 4-level lock chain; only terminate_connection can release these locks
         [REJECTED 0%] cancel_query on the root blocker would release the lock chain — cancel_query sends SIGINT which cannot interrupt pg_sleep or reliably release held transaction locks; the connection and locks persist, leaving the chain  blocked.

  Run 2/5
time=2026-07-28T19:36:41.224-04:00 level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external conn=localhost
time=2026-07-28T19:36:44.281-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on      chain), leaves C/D (blocked on chain2)"
time=2026-07-28T19:36:44.379-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_6a50c4ce gateway=http://localhost:8180 agent-conn="host=demo-postgres    port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:36:58.328-04:00 level=INFO msg="tearing down failure" id=db-tx-lock-chain-blocker type=shell_exec conn=localhost
time=2026-07-28T19:36:58.372-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n t\n t\n t\n(4 rows)\n\nDROP TABLE\nDROP TABLE"
  [PASS] score=100%
         [PRIMARY 95%] Root blocker PID 19041 is actively executing pg_sleep(3600) within an open transaction holding 4 exclusive locks on _faulttest_lock_chain; cancel_query is ineffective because even if SIGINT interrupts the sleep, the    transaction and its locks remain until the connection closes.
         [REJECTED 0%] cancel_query on PID 19041 will clear the lock chain. — SIGINT is unreliable for sleep statements; even if it succeeds, the transaction stays open and all locks persist until the connection closes.

  Run 3/5
time=2026-07-28T19:36:58.372-04:00 level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external conn=localhost
time=2026-07-28T19:37:01.429-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on      chain), leaves C/D (blocked on chain2)"
time=2026-07-28T19:37:01.526-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_6a50c4ce gateway=http://localhost:8180 agent-conn="host=demo-postgres    port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:37:15.702-04:00 level=INFO msg="tearing down failure" id=db-tx-lock-chain-blocker type=shell_exec conn=localhost
time=2026-07-28T19:37:15.743-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n t\n t\n t\n(4 rows)\n\nDROP TABLE\nDROP TABLE"
  [PASS] score=100%
         [PRIMARY 95%] Root blocker PID 19074 is actively executing pg_sleep(3600) while holding 4 exclusive transaction locks on _faulttest_lock_chain tables; has_open_tx=true, has_writes=yes, creating a 4-level downstream lock queue that   cannot clear until the connection terminates.
         [REJECTED 0%] cancel_query on PID 19074 could interrupt the sleep and release locks. — cancel_query sends SIGINT which does not close the connection; the transaction and its locks remain held even if the sleep is interrupted, so the lock queue does not clear.

  Run 4/5
time=2026-07-28T19:37:15.743-04:00 level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external conn=localhost
time=2026-07-28T19:37:18.799-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on      chain), leaves C/D (blocked on chain2)"
time=2026-07-28T19:37:18.894-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_6a50c4ce gateway=http://localhost:8180 agent-conn="host=demo-postgres    port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:37:38.940-04:00 level=INFO msg="tearing down failure" id=db-tx-lock-chain-blocker type=shell_exec conn=localhost
time=2026-07-28T19:37:38.980-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n t\n t\n t\n(4 rows)\n\nDROP TABLE\nDROP TABLE"
  [PASS] score=100%
         [PRIMARY 100%] PID 19114 is the root blocker holding 4 locks in an active transaction executing pg_sleep(3600); this long-running statement prevents any attempt to release locks until the connection closes.
         [REJECTED 0%] cancel_query would release the locks — cancel_query sends SIGINT which may interrupt pg_sleep but does not close the connection or release the open transaction; the 4 locks persist and the blocked sessions remain       queued.

  Run 5/5
time=2026-07-28T19:37:38.980-04:00 level=INFO msg="injecting failure" id=db-tx-lock-chain-blocker type=shell_exec mode=external conn=localhost
time=2026-07-28T19:37:42.030-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on      chain), leaves C/D (blocked on chain2)"
time=2026-07-28T19:37:42.127-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_6a50c4ce gateway=http://localhost:8180 agent-conn="host=demo-postgres    port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable"
time=2026-07-28T19:38:00.726-04:00 level=INFO msg="tearing down failure" id=db-tx-lock-chain-blocker type=shell_exec conn=localhost
time=2026-07-28T19:38:00.774-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n t\n t\n t\n(4 rows)\n\nDROP TABLE\nDROP TABLE"
  [PASS] score=100%
         [PRIMARY 95%] PID 19156 is the root blocker (blocking_pid=NULL) holding 4 locks on _faulttest_lock_chain via active pg_sleep(3600) transaction, creating a 4-level cascading lock chain that prevents downstream sessions from proceeding
         [REJECTED 5%] One of the intermediate victims (19165, 19167, 19166) is the true root blocker and can be resolved without terminating 19156 — get_blocking_queries output explicitly shows blocking_pid=NULL only for PID 19156, and      intermediate victims both hold locks (blocking_pid is not null) and are blocked (appear in blocked_pid column), confirming they are part of the chain, not the root
time=2026-07-28T19:38:00.844-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (5 runs):
    Pass rate:    5/5 (100%)
    Confidence:   min=95% max=100% range=5pp mean=97%  (H1, passing runs only)
    Verdict:      STABLE
    Attribution:  active-long-running-statement-blocker (4/5)  consistent: no (split)  [taxonomy 1.0]
                  cascaded-lock-chain: 1
  ────────────────────────────────────────────────────────────────
time=2026-07-28T19:38:05.633-04:00 level=INFO msg="fault stability cert posted" fault_id=db-tx-lock-chain-blocker verdict=STABLE n_runs=5

=== Fault Test Report: 9f8ea47b ===

[PASS] Max connections exhausted (db-max-connections) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Max connections exhausted (db-max-connections) - score: 75%
       Keywords: 100% | Tools: 50% | Category: 50% [no judge — add --judge for semantic scoring]
[PASS] Max connections exhausted (db-max-connections) - score: 85%
       Keywords: 100% | Tools: 100% | Category: 50% [no judge — add --judge for semantic scoring]
[PASS] Max connections exhausted (db-max-connections) - score: 85%
       Keywords: 100% | Tools: 100% | Category: 50% [no judge — add --judge for semantic scoring]
[PASS] Max connections exhausted (db-max-connections) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Long-running query blocking (db-long-running-query) - score: 90%
       Keywords: 100% | Tools: 50% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Long-running query blocking (db-long-running-query) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Long-running query blocking (db-long-running-query) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Long-running query blocking (db-long-running-query) - score: 90%
       Keywords: 100% | Tools: 50% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Long-running query blocking (db-long-running-query) - score: 90%
       Keywords: 100% | Tools: 50% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Transaction lock chain — active root blocker (pg_sleep trap) (db-tx-lock-chain-blocker) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 15 | Passed: 15 | Failed: 0 | Rate: 100%
  database: 15/15 (100%)

Report written to ./faulttest-9f8ea47b.json
```

Clean run! All three faults are now STABLE(5), with `db-max-connections` going from a noisy 3/5 split attribution to a clean 5/5 consistent after the `--agent-conn` fix. That's exactly the before/after that proves the fix mattered.  

Let's pull the live cert data:

```
[boris@ ~/helpdesk]$ for f in db-max-connections db-long-running-query db-tx-lock-chain-blocker; do
>      echo "=== $f ==="
>      curl -s "http://localhost:8180/api/v1/fleet/fault-stability/$f" -H 'Authorization: Bearer demo-api-key' -H 'X-User: demo@aihelpdesk.biz' | python3 -m json.tool
>      echo
>    done
=== db-max-connections ===
{
    "fault_id": "db-max-connections",
    "fault_name": "Max connections exhausted",
    "playbook_series_id": "pbs_connection_triage",
    "diagnosis_model": "claude-haiku-4-5-20251001",
    "n_runs": 5,
    "pass_rate": 1,
    "conf_range_pp": 4,
    "is_stable": true,
    "tested_at": "2026-07-28T23:34:16.967951377Z",
    "primary_attribution": "idle-connection-accumulation",
    "attribution_consistent": true,
    "attribution_distribution": {
        "idle-connection-accumulation": 5
    },
    "judge_spread": 0.2449489742783178,
    "taxonomy_version": "1.0"
}

=== db-long-running-query ===
{
    "fault_id": "db-long-running-query",
    "fault_name": "Long-running query blocking",
    "playbook_series_id": "pbs_slow_query_triage",
    "diagnosis_model": "claude-haiku-4-5-20251001",
    "n_runs": 5,
    "pass_rate": 1,
    "conf_range_pp": 4,
    "is_stable": true,
    "tested_at": "2026-07-28T23:36:20.582800254Z",
    "primary_attribution": "lock-contention-blocking-queries",
    "attribution_consistent": true,
    "attribution_distribution": {
        "lock-contention-blocking-queries": 5
    },
    "taxonomy_version": "1.0"
}

=== db-tx-lock-chain-blocker ===
{
    "fault_id": "db-tx-lock-chain-blocker",
    "fault_name": "Transaction lock chain \u2014 active root blocker (pg_sleep trap)",
    "playbook_series_id": "pbs_lock_chain_triage",
    "diagnosis_model": "claude-haiku-4-5-20251001",
    "n_runs": 5,
    "pass_rate": 1,
    "conf_range_pp": 5,
    "is_stable": true,
    "tested_at": "2026-07-28T23:38:05.629614345Z",
    "primary_attribution": "active-long-running-statement-blocker",
    "attribution_consistent": false,
    "attribution_distribution": {
        "active-long-running-statement-blocker": 4,
        "cascaded-lock-chain": 1
    },
    "taxonomy_version": "1.0"
}
```

All three certs are live and real:
```
  ┌──────────────────────────┬───────────┬───────────┬──────────────────┬─────────────────────────────────────────────────────────────────────────┐
  │          Fault           │  Verdict  │ Pass rate │ Confidence range │                               Attribution                               │
  ├──────────────────────────┼───────────┼───────────┼──────────────────┼─────────────────────────────────────────────────────────────────────────┤
  │ db-max-connections       │ STABLE(5) │ 100%      │ ±4pp             │ idle-connection-accumulation (5/5 consistent)                           │
  ├──────────────────────────┼───────────┼───────────┼──────────────────┼─────────────────────────────────────────────────────────────────────────┤
  │ db-long-running-query    │ STABLE(5) │ 100%      │ ±4pp             │ lock-contention-blocking-queries (5/5 consistent)                       │
  ├──────────────────────────┼───────────┼───────────┼──────────────────┼─────────────────────────────────────────────────────────────────────────┤
  │ db-tx-lock-chain-blocker │ STABLE(5) │ 100%      │ ±5pp             │ active-long-running-statement-blocker (4/5) + cascaded-lock-chain (1/5) │
  └──────────────────────────┴───────────┴───────────┴──────────────────┴─────────────────────────────────────────────────────────────────────────┘
```

## Supplementary Data

`vault accuracy` for a remediation playbook:

```
[boris@ ~/helpdesk/deploy/docker-compose]$ docker compose exec -T demo-gateway faulttest vault accuracy pbs_slow_query_remediate --gateway http://localhost:8180 --api-key demo-api-key
Gateway: http://localhost:8180  ·  version: v0.21.1-f48baab  ·  host: 39d766687d20

Diagnosis accuracy for series: pbs_slow_query_remediate

  No feedback submitted yet.
  Run a fault test and submit feedback after recovery to populate this report.

  Note: feedback is recorded on the triage run, not the remediation run.
  Try: faulttest vault accuracy pbs_slow_query_triage
```


`vault accuracy` for a triage playbook:

```
[boris@ ~/helpdesk/deploy/docker-compose]$ docker compose exec -T demo-gateway faulttest vault accuracy pbs_slow_query_triage --gateway http://localhost:8180 --api-key demo-api-key
Gateway: http://localhost:8180  ·  version: v0.21.1-f48baab  ·  host: 39d766687d20

Diagnosis accuracy for series: pbs_slow_query_triage

  No feedback submitted yet.
  Run a fault test and submit feedback after recovery to populate this report.

  Tip: run `faulttest vault accuracy` (no args) to list all series with feedback.
```


`vault calibration`:

```
[boris@ ~/helpdesk/deploy/docker-compose]$ docker compose exec -T demo-gateway faulttest vault calibration --gateway http://localhost:8180 --api-key demo-api-key
Gateway: http://localhost:8180  ·  version: v0.21.1-f48baab  ·  host: 39d766687d20

Diagnosis calibration — fleet-wide (0 runs with agent confidence + operator feedback)

CONFIDENCE    RUNS    CORRECT    ACCURACY    CALIBRATION
─────────────────────────────────────────────────────────────────
90-100%       0       0          –           INSUFFICIENT_DATA
70-89%        0       0          –           INSUFFICIENT_DATA
<70%          0       0          –           INSUFFICIENT_DATA

No runs with both eval scores and operator feedback yet.
Run faulttest with --gateway and submit feedback via `vault incidents` to populate.
```

`vault versions`:

```
[boris@ ~/helpdesk/deploy/docker-compose]$ docker compose exec -T demo-gateway faulttest vault versions --gateway http://localhost:8180 --api-key demo-api-key
Gateway: http://localhost:8180  ·  version: v0.21.1-f48baab  ·  host: 39d766687d20

Usage: faulttest vault versions <fault-id or series-id> [--gateway ...] [--api-key ...]

[boris@ ~/helpdesk/deploy/docker-compose]$ docker compose exec -T demo-gateway faulttest vault versions db-long-running-query --gateway http://localhost:8180 --api-key demo-api-key
Gateway: http://localhost:8180  ·  version: v0.21.1-f48baab  ·  host: 39d766687d20

Version stats for fault: db-long-running-query (Long-running query blocking)

TRIAGE  pbs_slow_query_triage — no run history yet

REMEDIATION  pbs_slow_query_remediate  (1 version(s))
VERSION     RUNS    SUCCESS%   AVG STEPS   AVG TIME    AVG DIAG   AVG REMED  APPROACH OK  JUDGE VERDICT
────────────────────────────────────────────────────────────────────────────────────────────────────
1.1 *       4       75%        1.5         34s         –          –          –          –
  id=pb_7df2f738

* = currently active   SUCCESS% = resolved + transitioned
id/from lines show playbook_id and the run that generated that version
```

`vault incidents`:

```
[boris@ ~/helpdesk/deploy/docker-compose]$ docker compose exec -T demo-gateway faulttest vault incidents --gateway http://localhost:8180 --api-key demo-api-key
Gateway: http://localhost:8180  ·  version: v0.21.1-f48baab  ·  host: 39d766687d20

Recent incidents (last 10)

RUN ID          SERIES                        STARTED           OUTCOME             OPERATOR
────────────────────────────────────────────────────────────────────────────────────────────────────────────
plr_8c707959    pbs_slow_query_remediate      2026-07-28 19:52  resolved            demo@aihelpdesk.biz
plr_5affaa4e    pbs_slow_query_remediate      2026-07-28 19:51  abandoned           operator@aihelpdesk.biz
plr_8d0448e2    pbs_connection_remediate      2026-07-28 19:44  resolved            demo@aihelpdesk.biz
plr_2463e2be    pbs_lock_chain_remediate      2026-07-28 18:47  resolved            demo@aihelpdesk.biz
plr_de46136c    pbs_connection_remediate      2026-07-28 17:49  resolved            demo@aihelpdesk.biz
plr_1f74fcc0    pbs_lock_chain_remediate      2026-07-28 17:47  resolved            demo-seed@aihelpdesk.biz
plr_864f820c    pbs_lock_chain_remediate      2026-07-28 16:47  resolved            demo-seed@aihelpdesk.biz
plr_a34276b7    pbs_slow_query_remediate      2026-07-28 13:07  resolved            demo-seed@aihelpdesk.biz
plr_19871e0b    pbs_slow_query_remediate      2026-07-28 12:07  resolved            demo-seed@aihelpdesk.biz
plr_98c6d642    pbs_connection_remediate      2026-07-28 01:48  resolved            demo@aihelpdesk.biz

  → vault incidents <plr_*>           full incident narrative
  → vault incidents <fault-id>        all runs for a fault
  → vault incidents --details         show JOURNEYS count and SOURCE
```

`vault incidents --details`:

```
[boris@ ~/helpdesk/deploy/docker-compose]$ docker compose exec -T demo-gateway faulttest vault incidents plr_8c707959 --details --gateway http://localhost:8180 --api-key demo-api-key
Gateway: http://localhost:8180  ·  version: v0.21.1-f48baab  ·  host: 39d766687d20


════════════════════════════════════════════════════════════
INCIDENT plr_8c707959
Started: 2026-07-28 19:52 UTC   Duration: 14s
Operator: demo@aihelpdesk.biz
Triggered by: PagerDuty P2: query on demo-postgres has been running 45s+ (threshold:
              30s) — flagged by slow-query monitor.
════════════════════════════════════════════════════════════

── TRIAGE
Playbook:  pbs_slow_query_remediate
Findings:  PID 99062 was successfully cancelled. The session was executing a
           pg_sleep(300) query (a stuck application connection) with no writes.
           cancel_query sent SIGINT and returned cancelled=true. Verification via
           get_blocking_queries confirms no blocking queries remain. The playbook
           goal — cancel or terminate the blocking/slow session — is
           complete.

── JOURNEYS
  WHY = Incident narrative (this view)   WHAT = Audit trail (vault journeys)

  triage:                tr_33f7b7f7-11b
                         reasoning chain, hypothesis building

  → vault journeys tr_33f7b7f7-11b
```
