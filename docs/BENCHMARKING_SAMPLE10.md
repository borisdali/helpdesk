# aiHelpDesk Sample#10 (Host/VM): Calibration Data Quality Banner + Consistency Certs Comparison + Journey's Agent Reasoning and the Chain-of-Thought

The sample commands presented below complements these two blog post: 

- **["The AI Said So". Blind Trust Is Not a Feature. It's Liability](...)**. 
  94% Confident. Zero Accountable. That's the opposite of trust. 
  The AI SRE and AI Ops industry is selling you confidence scores with no denominator, no audit trail of any reasoning that went into a decision making and no mechanism for a second opinion before an irreversible action. 
  Here's what informed consent looks like when it's actually enforced by design

- **[The AI Benchmark Said It Was Better. Your Production Database Disagreed](...)**
  Generic LLM benchmarks measure language model capability. 
  They don't measure whether your AI SRE tool can still diagnose connection pool saturation after the upgrade. 
  Those are different tests. 
  And only one of them matters when your pager goes off at 2am in the morning

It all starts with the [Vault](VAULT.md). If you need a background, start there. Next, head over to [this page](VAULT_METRICS.md) to see how aiHelpDesk turns your [Incident](INCIDENTS.md) data into a learning signal.

For more context, aiHelpDesk Fault Injection Testing is well documented [here](FAULTTEST.md), with multiple [examples availble](FAULTTEST_SAMPLE.md) on [K8s](BENCHMARKING_SAMPLE5.md), [Docker/Podman](BENCHMARKING_SAMPLE6.md) and on a [host/VM](BENCHMARKING_SAMPLE7.md). 

---

The sample commands posted below are broken into two parts and are shown for running aiHelpDesk on a host/VM, but similar samples of running aiHelpDesk on K8s and inside Docker/Podman containers are available [here](BENCHMARKING_SAMPLE8.md) and [here](BENCHMARKING_SAMPLE9.md) respectively (although not the exact commands shown on this page).


First off, as usual with running aiHelPDesk directly on a host/VM, we fire up the stack (see more on it in the [previous samples](BENCHMARKING_SAMPLE7.md) showing host/VM):

```
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ ./startall.sh --services-only --governance
Starting helpdesk services...
  auditd (pid 41038) -> /tmp/helpdesk-auditd.log
  database-agent (pid 41040) -> /tmp/helpdesk-database-agent.log
  k8s-agent (pid 41041) -> /tmp/helpdesk-k8s-agent.log
  sysadmin-agent (pid 41042) -> /tmp/helpdesk-sysadmin-agent.log
  incident-agent (pid 41043) -> /tmp/helpdesk-incident-agent.log
  gateway (pid 41045) -> /tmp/helpdesk-gateway.log
Gateway listening on http://localhost:8080
Auditing: enabled  (http://localhost:1199)
Policy:   enabled  (/tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64/./policies.yaml)
Mode:     fix

Starting governance components...
  auditor (pid 41047) -> /tmp/helpdesk-auditor.log
  secbot (pid 41048) -> /tmp/helpdesk-secbot.log

Running headless (--services-only). Press Ctrl-C to stop all services.
```


## Calibration Data Quality Banner

With no human-graded runs (`human_runs == 0`), aiHelpDesk shows a warning banner above the calibration table. See [here](VAULT.md#vault-calibration) for details:

```
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ curl -sX POST $HELPDESK-GATEWAY-URL/api/v1/fleet/playbook-runs/$RUN_ID/feedback \
>     -H "Authorization: Bearer $HELPDESK-API-KEY" -H "Content-Type: application/json" \
>     -d '{"feedback_type":"triage","feedback_time":"post_incident","verdict_correct":true,"feedback_source":"auto_judge"}'
{"run_id":"plr_408c1709","feedback_type":"triage","feedback_time":"post_incident","series_id":"pbs_slow_query_triage","verdict_correct":true,"operator":"","submitted_at":"2026-07-07T00:47:28.817205Z","feedback_source":"auto_judge"}

[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ ./faulttest vault calibration \
   db-long-running-query 
   --gateway $HELPDESK-GATEWAY-URL \
   --api-key $HELPDESK-API-KEY

Gateway: http://localhost:8080  ·  version: v0.20.0-0469ff0  ·  host: <your-host>

┌─────────────────────────────────────────────────────────────────────┐
│ ⚠  Data quality: 0 of 1 run(s) have human operator feedback.        │
│    This table measures self-consistency (LLM judge vs. itself),     │
│    not calibration against human judgment. To build a meaningful    │
│    calibration dataset, run faulttest interactively (without        │
│    --approval-mode force) or submit feedback via:                   │
│      faulttest vault feedback <run-id> --gateway $GW --api-key $KEY │
└─────────────────────────────────────────────────────────────────────┘

Diagnosis calibration — pbs_slow_query_triage (1 runs with agent confidence + operator feedback)

CONFIDENCE    RUNS    CORRECT    ACCURACY    CALIBRATION
─────────────────────────────────────────────────────────────────
90-100%       1       1          100%        INSUFFICIENT_DATA  ⚠ 1/1 keyword (no judge)
70-89%        0       0          –           INSUFFICIENT_DATA
<70%          0       0          –           INSUFFICIENT_DATA

Note: all 1 run(s) above use auto_judge feedback (LLM judge score ≥ 0.8, --approval-mode force).
      Calibration measures self-consistency, not human judgment. Run interactively to collect real operator verdicts.
```


## Compare Model Consistency Certs

Want to upgrade the model to a new version or switch to a different model or even to a different vendor entirely? [Get the numbers first](VAULT.md#vault-cert-compare). Compares consistency certification results across two diagnosis models, fault by fault. Here's how:

Now, in reality you would run the normal fault injection tests wit the `--repeat N` parameter to earn the [Consistency Certificate] badge. We've shown this previously [here](BENCHMARKING_SAMPLE8.md#triage-consistency-certificationbadge-testing). It does take time, especially if you run the `--recertify` across a large number of tests (e.g. N>5) because each run genuinely stands up a test, runs it and tears it down, which takes time. For all faults, on an "average" machine, for 5 tests/fault, this may take half an hour. If you want to just test the banner quickly in a few minutes, seed the same data with the simple curl commands shown below:

```
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ curl -s -X POST $HELPDESK-GATEWAY-URL/api/v1/fleet/fault-stability \
>     -H "Authorization: Bearer $HELPDESK-API-KEY" \
>     -H "Content-Type: application/json" \
>     -d '{"fault_id":"db-lock-contention","fault_name":"Lock Contention / Deadlock","diagnosis_model":"gemini-3.5-flash","n_runs":5,"pass_rate":1.0,"is_stable":true}'
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ echo $?
0

[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ curl -s -X POST $HELPDESK-GATEWAY-URL/api/v1/fleet/fault-stability \
>     -H "Authorization: Bearer $HELPDESK-API-KEY" \
>     -H "Content-Type: application/json" \
>     -d '{"fault_id":"db-lock-contention","fault_name":"Lock Contention / Deadlock","diagnosis_model":"claude-sonnet-4-6","n_runs":5,"pass_rate":0.40,"is_stable":false}'
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ echo $?
0

[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ curl -s -X POST $HELPDESK-GATEWAY-URL/api/v1/fleet/fault-stability \
>     -H "Authorization: Bearer $HELPDESK-API-KEY" \
>     -H "Content-Type: application/json" \
>     -d '{"fault_id":"db-max-connections","fault_name":"Max Connections Exhaustion","diagnosis_model":"gemini-3.5-flash","n_runs":5,"pass_rate":0.40,"is_stable":false}'
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ echo $?
0

[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ curl -s -X POST $HELPDESK-GATEWAY-URL/api/v1/fleet/fault-stability \
>     -H "Authorization: Bearer $HELPDESK-API-KEY" \
>     -H "Content-Type: application/json" \
>     -d '{"fault_id":"db-max-connections","fault_name":"Max Connections Exhaustion","diagnosis_model":"claude-sonnet-4-6","n_runs":5,"pass_rate":1.0,"is_stable":true}'
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ echo $?
0

[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ curl -s -X POST $HELPDESK-GATEWAY-URL/api/v1/fleet/fault-stability \
>     -H "Authorization: Bearer $HELPDESK-API-KEY" \
>     -H "Content-Type: application/json" \
>     -d '{"fault_id":"db-vacuum-needed","fault_name":"Vacuum Bloat","diagnosis_model":"gemini-3.5-flash","n_runs":5,"pass_rate":1.0,"is_stable":true}'
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ echo $?
0
```

Once the calibration data is in, we can run the model comparison, aka [`cert-compare`](VAULT.md#vault-cert-compare):

```
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ ./faulttest vault cert-compare \
>     gemini-3.5-flash claude-sonnet-4-6 \
>     --gateway $HELPDESK-GATEWAY-URL \
>     --api-key $HELPDESK-API-KEY

Gateway: http://localhost:8080  ·  version: v0.20.0-0469ff0  ·  host: <your-host>

Stability comparison: gemini-3.5-flash → claude-sonnet-4-6
STABLE/UNSTABLE = triage diagnosis cert only (keyword + tool + category scoring across --repeat runs; remediation not included)

FAULT                               gemini-3.5-flash  sonnet-4-6    CHANGE
────────────────────────────────────────────────────────────────────────────────
Lock Contention / Deadlock          STABLE        UNSTABLE      ⚠ REGRESSION
  db-lock-contention diag_rate: 100% → 40%  (Δ-60%)
Max Connections Exhaustion          UNSTABLE      STABLE        ✓ IMPROVEMENT
Vacuum Bloat                        STABLE        (no data)     ? NOT RUN YET

3 fault(s) total  ·  1 regression(s) ⚠  ·  1 improvement(s) ✓  ·  1 not run under both models ?

⚠  1 regression(s) detected — promote claude-sonnet-4-6 only after investigating the faults above.
```

It's important to repeat, that this is not the live `faulttest` runs, just the `curl` simulations, but we believe it's adequate for testing this feature for these reasons:

1/ Transparency:
   `curl` commands show what a cert is: the payload, the fields, the numbers.
   A `faulttest` run is a black box from the blog reader's perspective.

2/ Reproducibility:
   Anybody with a running Gateway can copy-paste the `curl` commands and see [`cert-compare`](VAULT.md#vault-cert-compare) work in just a few minutes.
   A real `faulttest` requires time, setup, a test DB and agent credentials.

3/ The thing of interest here is the comparison, not the data generation that we've already proven to work in the previous samples.
   The `cert-compare` logic and output format is the story. 
   How the certs got there is a footnote.

That being said, in production, the certs come from `faulttest run --repeat N` (see [this link](CONSISTENCY.md#52-host--vm-binary) for how it looks like on a host/VM in particular), not from the curl simulation shown above.

---

For reference, this is the full list of presently available fault injection tests in the vault (the [pubicly avaialble](FAULTTEST.md) ones, the [internal](../testing/FAULT_INJECTION_TESTING.md) list is twice longer):

```
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ ./faulttest vault list \
>   --gateway $HELPDESK-GATEWAY-URL \
>   --api-key $HELPDESK-API-KEY

Gateway: http://localhost:8080  ·  version: v0.20.0-0469ff0  ·  host: <your-host>

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-max-connections               any        pbs_connection_triage           pbs_connection_remediate         2026-07-07  PASS       STABLE(5)      13 runs  62% resolved  100% accurate  avg: 2.1 steps, 22s recovery  (generated)
    diag  1.4   *  1r  100%  avg: 58s recovery  100% approach OK
    diag  1.3      19r  74%  avg: 37s recovery  100% approach OK
    remed 1.4   *  4r  75%  avg: 13s recovery
    remed 1.3      9r  100%  avg: 3.0 steps, 26s recovery
db-long-running-query            any        pbs_slow_query_triage           pbs_slow_query_remediate         2026-06-22  PASS       STABLE(5) 16d  2 runs  100% resolved  100% accurate  avg: 4.0 steps, 10s recovery  (system)
    diag  → vault versions pbs_slow_query_triage
    remed → vault versions pbs_slow_query_remediate
db-lock-contention               any        pbs_lock_contention_triage      pbs_slow_query_remediate         2026-06-23  PASS       UNSTABLE(5)    2 runs  100% resolved  –  avg: 4.0 steps, 10s recovery  (system)
    diag  → vault versions pbs_lock_contention_triage
    remed → vault versions pbs_slow_query_remediate
db-table-bloat                   any        pbs_vacuum_triage               pbs_vacuum_remediate             2026-06-22  PASS       STABLE(5) 16d  11 runs  91% resolved  100% accurate  avg: 7.2 steps, 9m48s recovery  (system)
    diag  1.5   *  1r  100%  avg: 6m32s recovery  100% approach OK
    diag  1.4      9r  89%  avg: 1m26s recovery  100% approach OK
    diag  → vault versions pbs_vacuum_triage
    remed → vault versions pbs_vacuum_remediate
db-high-cache-miss               any        pbs_cache_miss_triage           pbs_cache_miss_remediate         2026-07-04  PASS       STABLE(5) 16d  1 runs  100% resolved  –  avg: 4.0 steps, 11s recovery  (system)
    diag  → vault versions pbs_cache_miss_triage
    remed → vault versions pbs_cache_miss_remediate
db-idle-in-transaction           any        pbs_connection_triage           pbs_connection_remediate         2026-06-22  PASS       STABLE(5) 16d  13 runs  62% resolved  100% accurate  avg: 2.1 steps, 22s recovery  (generated)
    diag  1.4   *  1r  100%  avg: 58s recovery  100% approach OK
    diag  1.3      19r  74%  avg: 37s recovery  100% approach OK
    remed 1.4   *  4r  75%  avg: 13s recovery
    remed 1.3      9r  100%  avg: 3.0 steps, 26s recovery
db-tx-lock-chain-blocker         any        pbs_lock_chain_triage           pbs_lock_chain_remediate         2026-07-01  FAIL       STABLE(5) 16d  3 runs  33% resolved  100% accurate  avg: 19.3 steps, 1m13s recovery  (system)
    diag  → vault versions pbs_lock_chain_triage
    remed → vault versions pbs_lock_chain_remediate
db-terminate-direct-command      any        -                               (none)                           NO PLAYBOOK            STABLE(5) 16d  -
db-vacuum-needed                 any        pbs_vacuum_triage               pbs_vacuum_remediate             2026-07-07  PASS       STABLE(5)      11 runs  91% resolved  100% accurate  avg: 7.2 steps, 9m48s recovery  (system)
    diag  1.5   *  1r  100%  avg: 6m32s recovery  100% approach OK
    diag  1.4      9r  89%  avg: 1m26s recovery  100% approach OK
    diag  → vault versions pbs_vacuum_triage
    remed → vault versions pbs_vacuum_remediate
db-disk-pressure                 any        pbs_disk_pressure_triage        (none)                           2026-06-22  PASS       STABLE(5) 16d  -
    → vault versions pbs_disk_pressure_triage
db-pg-hba-corrupt                any        pbs_pg_hba_triage               pbs_db_config_recovery           2026-06-22  FAIL       UNSTABLE(1) 16d 0 runs  (system)
db-process-kill                  any        pbs_db_restart_triage           pbs_db_restart_triage            2026-06-22  FAIL       UNSTABLE(1) 16d 1 runs  0% resolved  –  avg: 6s recovery  (system)
    → vault versions pbs_db_restart_triage
db-config-bad-param              any        -                               (none)                           NO PLAYBOOK            UNSTABLE(1) 16d -
db-wal-disk-full                 docker/vm  -                               (none)                           NO PLAYBOOK            UNSTABLE(1) 16d -
db-wal-disk-full-k8s             k8s        pbs_k8s_pod_crash_triage        (none)                           2026-06-22  PASS       UNSTABLE(5) 16d -
    → vault versions pbs_k8s_pod_crash_triage
db-wal-stale-slot                any        pbs_wal_stale_slot              pbs_stale_slot_remediate         2026-06-29  PASS       STABLE(5) 16d  14 runs  79% resolved  100% accurate  avg: 3.4 steps, 24s recovery  (system)
    diag  1.3   *  8r  100%  avg: 2m12s recovery  100% approach OK
    diag  1.2      9r  0%  avg: 3m55s recovery
    remed → vault versions pbs_stale_slot_remediate
db-checkpoint-warning            any        pbs_checkpoint_bgwriter_triage  pbs_bgwriter_remediate           2026-06-22  PASS       STABLE(5) 16d  1 runs  100% resolved  –  avg: 6.0 steps, 16s recovery  (system)
    diag  → vault versions pbs_checkpoint_bgwriter_triage
    remed → vault versions pbs_bgwriter_remediate
```


## Journey's Agent Reasoning and Chain-of-Thought

The API endpoint to get the full agent reasoning and the chain-of-thought is `GET /api/v1/fleet/playbook-runs/{runID}/events` see [here](PLAYBOOKS.md#run-events-chain-of-thought) for details. Quoting that section directly here:

"Returns the chain-of-thought audit events for a run in chronological order — agent reasoning, tool executions, and policy decisions

curl -s http://localhost:8080/api/v1/fleet/playbook-runs/plr_3f7a2b1c/events \
  | jq '[.[] | {event_type, timestamp, output: .output.response}]'
"

Here's how this looks in practice via the CLI aka [`vault journey --detail`](VAULT.md#--detail-reasoning-interleaved-with-tool-calls):

First let's get the list of last 5 journeys (and add an extra filter to limit it to the last 20 days. The important qualifier here is `--incident`, which effectively limits the [Journeys](JOURNEYS.md) to only those related to an [Incident](INCIDENTS.md):

```
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ ./faulttest vault journey \
   --incident \
   --limit 5 \
   --gateway $HELPDESK-GATEWAY-URL \
   --api-key $HELPDESK-API-KEY \
   --since 20d

Gateway: http://localhost:8080  ·  version: v0.20.0-0469ff0  ·  host: <your-host>

Recent journeys — 5 entries (last 20d)

TRACE ID                                      STARTED           DUR      AGENT                      ORIGIN        OUTCOME            INCIDENT        TOOLS
─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
faulttest-ffa40a4a-db-vacuum-needed-remed     2026-07-07 05:26  22.7s    pbs_vacuum_remediate       direct_tool   success            plr_d7e1df41    get_vacuum_status, get_active_connect...
faulttest-ffa40a4a-db-vacuum-needed           2026-07-07 05:19  36.3s    postgres_database_agent    agent         success            plr_7cbf0427    check_connection, get_vacuum_status, ...
faulttest-aecfce98-db-max-connections-remed   2026-07-07 04:58  15.6s    postgres_database_agent    –             denied             plr_3a0948b6    –
faulttest-aecfce98-db-max-connections         2026-07-07 04:57  24.3s    postgres_database_agent    agent         success            plr_820e2974    check_connection, get_server_info, ge...
tr_0aa032cc-827                               2026-07-07 01:28  18.4s    postgres_database_agent    agent         success            plr_828472f2    check_connection, get_slow_queries, g...

  ! = fabrication mismatch (agent reported success but no tool call recorded)

To drill into a trace:
  faulttest vault journey <trace_id> --gateway http://localhost:8080

```

Find a Journey (again linked to an incident), with the Origin pointing to an `agent` and get the full reasoning and the chain-of-thought by adding the `--detail` flag:

```
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ ./faulttest vault journey \
   faulttest-aecfce98-db-max-connections \
   --detail \
   --gateway $HELPDESK-GATEWAY-URL \
   --api-key $HELPDESK-API-KEY

Gateway: http://localhost:8080  ·  version: v0.20.0-0469ff0  ·  host: <your-host>

JOURNEY  faulttest-aecfce98-db-max-connections
──────────────────────────────────────────────────────────────────────
  Started:           2026-07-07 04:57:04 UTC
  Ended:             2026-07-07 04:57:28 UTC
  Duration:          24.3s
  Agent:             postgres_database_agent
  Category:          incident
  Origin:            agent
  Outcome:           success
  Events:            29

QUERY
──────────────────────────────────────────────────────────────────────
  Call check_connection with connection_string="host=localhost
  port=15432 dbname=testdb user=postgres password=testpass" and
  begin diagnosing why it is unavailable. Do not ask which database
  — the target is "host=localhost port=15432 dbname=testdb
  user=postgres password=testpass". ...

DELEGATIONS
──────────────────────────────────────────────────────────────────────
  1. playbook transition from pbs_connection_triage

EXECUTION TRACE
──────────────────────────────────────────────────────────────────────
  "I'll begin investigating the connection unavailability for the
   target database. Let me start with check_connection."
  ► check_connection                       [ok]

  "Good news — the database is reachable. Now let me gather
   diagnostic data about connection status, server configuration,
   and any blocking activity. Since users are reporting "too many
   clients" errors despite the connection being successful, I need
   to assess connection saturation and understand the state of
   active/idle connections."
  ► get_server_info                        [ok]

  ► get_connection_stats                   [ok]

  ► get_active_connections                 [ok]

  ► get_blocking_queries                   [ok]

  "The picture is now very clear. Let me check the idle session
   timeout configuration to understand why these idle connections
   are persisting:"
  ► get_pg_settings                        [ok]

  "Let me check the actual idle_session_timeout parameter value
   directly:"

  "Let me try the correct approach to get all settings that mention
   idle:"
  ► get_pg_settings                        [ok]

INCIDENT LINK
──────────────────────────────────────────────────────────────────────
  Run ID:            plr_820e2974

  → vault incidents plr_820e2974

REMEDIATION JOURNEY
──────────────────────────────────────────────────────────────────────
  Trace:             faulttest-aecfce98-db-max-connections-remed
  Agent:             postgres_database_agent
  Outcome:           denied

  → vault journeys faulttest-aecfce98-db-max-connections-remed
```

