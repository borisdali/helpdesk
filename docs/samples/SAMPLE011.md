# aiHelpDesk Sample#11 (on K8s): The power of `vault judge-accuracy`

The raw sample commands and deliberations presented below complement this blog post: 

- **[Your AI Just Rewrote Its Own Playbook. How Do You Know It Got Better?](https://levelup.gitconnected.com/your-ai-just-rewrote-its-own-playbook-how-do-you-know-it-got-better-85322fdec17c)**
  Every AI system will eventually propose a change that makes things worse. The question is whether you’ll know before or after it runs on your production database at 2am.

It all starts with the [Vault](../VAULT.md). If you need a background, start there. Next, head over to [this page](../VAULT_METRICS.md) to see how aiHelpDesk turns your [Incident](../INCIDENTS.md) data into a learning signal.

For more context, aiHelpDesk Fault Injection Testing is well documented [here](../FAULTTEST.md), with multiple [examples availble](FAULTTEST_SAMPLE.md) on [K8s](SAMPLE005.md), [Docker/Podman](SAMPLE006.md) and on a [host/VM](SAMPLE007.md). 

---

The sample commands posted below are broken into two parts and are shown for running aiHelpDesk on K8s, but similar samples of running aiHelpDesk on host/VM directly and inside Docker/Podman containers are available [here](SAMPLE010.md) and [here](SAMPLE009.md) respectively (although not the exact commands shown on this page).


## Diff between `vault judge-accuracy` and `vault accuracy`

This is a popular question, so it's worth clearing it first:

[`vault judge-accuracy`](../VAULT.md#vault-judge-accuracy) is a very powerful command, but not to be confused with [`vault accuracy`](../VAULT.md#vault-accuracy). Both of these commands have the word `accuracy` in the name, but they measure different things at different lifecycle moments:

The latter (`vault accuracy`) measures the agent's diagnosis quality, scored by humans.

The data comes from operator feedback submitted after incidents (`vault feedback`). The human who dealt with the incident says whether the agent's diagnosis was correct, so this command is designed to answer this question: "How often does the agent get it right on this fault class?" The score is human-verified.

In contrast, the former command (`vault judge-accuracy`) measures the LLM judge's prediction quality, verified by actual run outcomes.

The data comes from judge verdicts recorded by `vault diff --judge` at playbook activation time. 
The judge looked at a proposed playbook change and said APPROVE, NEEDS_REVIEW, or REJECT. 
`vault judge-accuracy` then checks whether that prediction was right, as in "Did the version the judge approved actually resolve more incidents? Did the version it flagged actually underperform?".
Or look at it differently, this command is designed to provide an answer to this question: "When the judge says a playbook is good, is it right?"

So to summarize, here's the definition in one sentence for each:

- `vault accuracy`: Was the agent's diagnosis correct? (human verdict, per incident)
- `vault judge-accuracy`: Was the judge's playbook assessment correct? (run outcomes vs. judge prediction, per playbook version)

Or, perhaps a better framing is this:

  `vault accuracy` audits the agent. `vault judge-accuracy` audits the judge.

They're two rungs of the same accountability stack. 
The agent needs to be accurate and the judge that certifies playbook improvements needs to be accurate too. 
If `vault judge-accuracy` shows the judge keeps approving versions that don't improve RESOLVED%, the judge model or prompt needs revisiting. 
If `vault accuracy` shows diagnosis dropping on a fault class, a playbook update is needed.


## Demo of `vault judge-accuracy`:

With that intro out of the way, let's see the power of `vault judge-accuracy`:

The walkthrough below produces a real pre-activation judge verdict, activates the draft, accumulates run data and then cross-references the prediction against outcomes. The commands are shown for running aiHelpDesk from the source, but, as usual, can be easily adapted to K8s, Docker/Podman and for running aiHelpDesk directly on a host/VM:

If you prefer to reproduce this walkthrough locally on a laptop, please set these env vars first:

```
export HELPDESK_GATEWAY_URL=http://localhost:8080
export HELPDESK_CLIENT_API_KEY=<your aiHelpDesk API key>
```

Step 1: Find a draft or produce one

Check what's pending:

```
  go run ./testing/cmd/faulttest vault drafts --gateway $HELPDESK_GATEWAY_URL --api-key $HELPDESK_CLIENT_API_KEY
```

If nothing is pending, synthesise a draft from the most recent successful run:

```
  go run ./testing/cmd/faulttest vault suggest-update \
    --series-id pbs_connection_triage \
    --gateway $HELPDESK_GATEWAY_URL --api-key $HELPDESK_CLIENT_API_KEY
```

Note the draft ID printed — e.g. pb_40729257.

Step 2: Diff with judge (this is what creates the accountability record)

```
  go run ./testing/cmd/faulttest vault diff pb_40729257 \
    --judge \
    --judge-vendor anthropic \
    --judge-model claude-haiku-4-5-20251001 \
    --judge-api-key $ANTHROPIC_API_KEY \
    --gateway $HELPDESK_GATEWAY_URL \
    --api-key $HELPDESK_CLIENT_API_KEY
```

  Expected output:

```
  Diff: series pbs_connection_triage
    before  pb_a8ef87d7  v1.5  ...
    after   pb_40729257  v1.6  ...

  ── guidance ─────────────────────────────────────────────────────────────
    [field-by-field diff]

  ── LLM Judge Review ────────────────────────────────────────────────────
  Verdict:            ✓  APPROVE
  Guidance quality:   Improved: ...
  Escalation safety:  Unchanged.
  Reasoning:          ...
  Judge model:        claude-haiku-4-5-20251001

  Judge verdict persisted to pb_40729257.
```

The verdict is now stored. `vault judge-accuracy` will track it against future run data.

Step 3 — Activate

```
  go run ./testing/cmd/faulttest vault activate pb_40729257 \
    --gateway $HELPDESK_GATEWAY_URL --api-key $HELPDESK_CLIENT_API_KEY
```

And that's the skinny on the walkthrough.

Now for brevity I'm omitting the first fault injection testing runs here that were done for the purpose of obtaining the [Consistency Certificate](../CONSISTENCY.md) (note the `--repeat N` flag in the command below), but the raw commands below is what counts because they tell the whole story:

```
[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest run \
      --ids db-connection-refused \
      --auto-db \
      --repeat 3 \
      --agent-model claude-sonnet-4-6 \
      --judge --judge-vendor anthropic \
      --judge-model claude-haiku-4-5-20251001 \
      --judge-api-key $ANTHROPIC_API_KEY \
      --gateway $HELPDESK_GATEWAY_URL \
      --api-key $HELPDESK_CLIENT_API_KEY \
      --approval-mode force \
      --emit-and-wait --gate-escalation

Sat Jul 11 23:48:06 EDT 2026
Starting temporary PostgreSQL container (postgres:16-alpine)...
Auto-DB ready: host=127.0.0.1 port=57978 dbname=faulttest user=postgres password=faulttest sslmode=disable

time=2026-07-11T23:48:10.949-04:00 level=INFO msg="auto-DB registered with gateway" server_id=faulttest-auto-57978
time=2026-07-11T23:48:10.949-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-07-11T23:48:10.949-04:00 level=INFO msg="LLM diagnosis judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: Database connection refused (db-connection-refused) — 3 runs ---

  Run 1/3
time=2026-07-11T23:48:10.955-04:00 level=INFO msg="injecting failure" id=db-connection-refused type=shell_exec mode=external conn=127.0.0.1
time=2026-07-11T23:48:11.160-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-2ad33cdf\nPostgreSQL container faulttest-auto-db-2ad33cdf stopped"
time=2026-07-11T23:48:11.537-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-connection-refused series_id=pbs_db_restart_triage playbook_id=pb_5954d4e0 gateway=http://localhost:8080 agent-conn="host=127.0.0.1           port=57978 dbname=faulttest user=postgres password=faulttest sslmode=disable"
  Feedback submitted (triage/post_incident run_id=plr_7fad501c)
time=2026-07-11T23:48:32.667-04:00 level=INFO msg="tearing down failure" id=db-connection-refused type=shell_exec conn=127.0.0.1
time=2026-07-11T23:48:35.790-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-2ad33cdf\nPostgreSQL container faulttest-auto-db-2ad33cdf started"
  [PASS] score=73%
         [PRIMARY 60%] PostgreSQL process crashed or was terminated, and database cannot restart due to undiagnosed failure (data corruption, config error, or infrastructure issue)
         [REJECTED 40%] Container was stopped or crashed, but no access to container logs available to confirm root cause — No metadata in Known Infrastructure confirming port 57978 is container-hosted; cannot escalate to container inspector without first confirming hosting type.

  Run 2/3
time=2026-07-11T23:48:35.791-04:00 level=INFO msg="injecting failure" id=db-connection-refused type=shell_exec mode=external conn=127.0.0.1
time=2026-07-11T23:48:35.949-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-2ad33cdf\nPostgreSQL container faulttest-auto-db-2ad33cdf stopped"
time=2026-07-11T23:48:36.267-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-connection-refused series_id=pbs_db_restart_triage playbook_id=pb_5954d4e0 gateway=http://localhost:8080 agent-conn="host=127.0.0.1           port=57978 dbname=faulttest user=postgres password=faulttest sslmode=disable"
  Feedback submitted (triage/post_incident run_id=plr_d188a80a)
time=2026-07-11T23:48:56.002-04:00 level=INFO msg="tearing down failure" id=db-connection-refused type=shell_exec conn=127.0.0.1
time=2026-07-11T23:48:59.117-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-2ad33cdf\nPostgreSQL container faulttest-auto-db-2ad33cdf started"
  [PASS] score=86%
         [PRIMARY 100%] PostgreSQL process is not running on port 57978, likely due to a crash, intentional shutdown, or container exit.
         [REJECTED 0%] Network/firewall issue preventing connection to a running server. — Connection refused error indicates the port is not listening at all, not a network timeout.

  Run 3/3
time=2026-07-11T23:48:59.117-04:00 level=INFO msg="injecting failure" id=db-connection-refused type=shell_exec mode=external conn=127.0.0.1
time=2026-07-11T23:48:59.304-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-2ad33cdf\nPostgreSQL container faulttest-auto-db-2ad33cdf stopped"
time=2026-07-11T23:48:59.720-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-connection-refused series_id=pbs_db_restart_triage playbook_id=pb_5954d4e0 gateway=http://localhost:8080 agent-conn="host=127.0.0.1           port=57978 dbname=faulttest user=postgres password=faulttest sslmode=disable"
  Feedback submitted (triage/post_incident run_id=plr_c069a641)
time=2026-07-11T23:49:11.138-04:00 level=INFO msg="tearing down failure" id=db-connection-refused type=shell_exec conn=127.0.0.1
time=2026-07-11T23:49:14.278-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-2ad33cdf\nPostgreSQL container faulttest-auto-db-2ad33cdf started"
  [PASS] score=100%
         [PRIMARY 100%] PostgreSQL process is not running on the target port (connection refused) due to container crash, intentional shutdown, or service failure
         [REJECTED 0%] Network/firewall issue preventing connection — "Connection refused" specifically means the port is not accepting connections, not a network timeout or reachability issue
time=2026-07-11T23:49:14.799-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (3 runs):
    Pass rate:    3/3 (100%)
    Confidence:   min=60% max=100% range=40pp mean=87%  (H1, passing runs only)  [UNSTABLE: want <= 30pp]
    Verdict:      UNSTABLE
  ────────────────────────────────────────────────────────────────
time=2026-07-11T23:49:16.832-04:00 level=INFO msg="fault stability cert posted" fault_id=db-connection-refused verdict=UNSTABLE n_runs=3

=== Fault Test Report: fb5f9e04 ===

[PASS] Database connection refused (db-connection-refused) - score: 73% [judge: 33%]
       Keywords: 100% | Tools: 100% | Judge: 33%
       Reasoning: "Agent correctly identified the symptom (connection refused, PostgreSQL not listening) but failed to follow the expected diagnostic pathway by refusing to escalate to pbs_sysadmin_docker_inspect due to unconfirmed hosting   type, when the playbook's expert guidance indicates escalation is the correct action for unknown infrastructure regardless of confirmation status."
[PASS] Database connection refused (db-connection-refused) - score: 86% [judge: 67%]
       Keywords: 100% | Tools: 100% | Judge: 67%
       Reasoning: "Agent correctly identified the root cause (PostgreSQL process not running/connection refused) and recognized the need for escalation due to inability to access container/system state, but failed to emit the required        ESCALATE_TO: pbs_sysadmin_docker_inspect action despite acknowledging the playbook instruction to do so when container state cannot be determined."
[PASS] Database connection refused (db-connection-refused) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Reasoning: "Agent correctly identified connection refused as PostgreSQL not running/accepting connections, properly determined Docker container hosting context, and appropriately escalated to pbs_sysadmin_docker_inspect with explicit  justification that container state inspection is beyond agent capabilities—all key points from EXPECTED DIAGNOSIS are fully addressed."

--- Summary ---
Total: 3 | Passed: 3 | Failed: 0 | Rate: 100%
  database: 3/3 (100%)

LLM judge scored diagnosis for 3 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-fb5f9e04.json
```

This run is revealing exactly what `vault judge-accuracy` is designed to capture:

What actually happened:

  - 3/3 PASS, but UNSTABLE
    Confidence range 40pp (60%→100%), above the 30pp ceiling

  - The instability is real, not noise.
    Judge scores: 33% → 67% → 100%

Why the inconsistency?
The `auto-db` isn't in the infra config, so the agent has no Known Infrastructure entry telling it the server is Docker-hosted.
The playbook says "look at Known Infrastructure to determine hosting type"... but when there's no entry and so the agent's behavior is undefined and varies run-to-run:

```
  - Run 1 (60% confidence, judge 33%): 
    "No metadata in Known Infrastructure confirming port 57978 is container-hosted and so... cannot escalate without confirmation." 
    Agent withheld escalation.

  - Run 2 (100% confidence, judge 67%): 
    Agent acknowledged it should escalate, but didn't emit the ESCALATE_TO line.

  - Run 3 (100% confidence, judge 100%): 
    Agent inferred Docker hosting from context and escalated correctly.
```

The fix: The playbook guidance needs a default action for when hosting type is unknown because currently it's silent on this case.
Whats the correct default? When in doubt, escalate to SysAdmin.

Let's update the playbook with these instructions v1.6 → v1.7:

And just to summarize, here's where we presently stand:

The baseline is already posted (UNSTABLE, 3/3 pass, 40pp range) and so the remaining workflow has these steps:

```
  # Step 1 — baseline is already in the vault (just ran: UNSTABLE cert posted)
  vault list db-connection-refused          # shows UNSTABLE, last tested now

  # Step 2 — upload the new playbook version as a draft
  vault suggest-update pbs_db_restart_triage

  # Step 3 — judge assesses the update: does it expect improvement?
  vault diff --judge pbs_db_restart_triage
  # → judge sees the new "unknown hosting → default to escalation" rule
  # → should emit APPROVE: the ambiguity that caused run-to-run inconsistency is now resolved

  # Step 4 — activate the new version
  vault activate pbs_db_restart_triage 1.7

  # Step 5 — re-run with the same command
  go run ./testing/cmd/faulttest run \
      --ids db-connection-refused --auto-db --repeat 3 \
      --agent-model claude-sonnet-4-6 \
      --judge --judge-vendor anthropic \
      --judge-model claude-haiku-4-5-20251001 \
      --judge-api-key $ANTHROPIC_API_KEY \
      --gateway $GW --api-key $HELPDESK_CLIENT_API_KEY \
      --approval-mode force --emit-and-wait --gate-escalation
  # → expect: tighter confidence range, STABLE verdict

  # Step 6 — was the judge right?
  vault judge-accuracy
  # → shows: the APPROVE verdict for pbs_db_restart_triage v1.7 was correct
  #          pass rate held at 100%, confidence range narrowed
```

The UNSTABLE verdict on the current baseline is legitimate signal, not noise.
This is because the guidance left the agents without a path when hosting type was unknown, causing confident-but-wrong behavior in 2 of 3 runs. 
The fix gives agents a deterministic fallback. That's exactly the kind of testable hypothesis `vault diff --judge` was built for.

Here's the skinny:

```
[boris@ /tmp/helpdesk/helpdesk-v0.20.0-darwin-arm64]$ ./faulttest vault list \
   db-connection-refused
   --gateway $HELPDESK-GATEWAY-URL \
   --api-key $HELPDESK-API-KEY

Gateway: http://localhost:8080  ·  version: v0.20.0-0469ff0  ·  host: <your-host>

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-connection-refused            any        pbs_db_restart_triage           pbs_db_restart_action            2026-07-12  PASS       UNSTABLE(3)    0 runs  (system)
    diag  1.5   *  8r  12%  avg: 17s recovery
    diag  1.4      1r  0%  avg: 6s recovery
```

Good, the baseline is now in place: UNSTABLE(3) cert posted, diag version 1.5 active.

It may be worth paying attention to the 8r 12% before proceeding.
That's the average `diagnosis_score` from `run_evaluation` in auditd across all 8 recorded runs with this playbook version.
It's pulled down hard by one of the previous injection-failed runs (score 0) and the early false-positive runs where the container was never actually stopped (those had odd scoring too).
It doesn't represent the faulttest pass rate.
The cert itself (UNSTABLE(3)) is the authoritative stability signal.
The 12% is informational noise from the mixed-provenance run history.

The judge should now see the new "unknown hosting type → default to escalation" rule and emit APPROVE
That's the gap that caused runs 1 and 2 to score 33%/67%. Once we get the verdict, we ca proceed with activating and re-runing to close the loop with a new playbook version.

Time to run `vault suggest-update`:

```
[boris@cassiopeia ~/cassiopeia/helpdesk]$ go run ./testing/cmd/faulttest vault suggest-update --series-id pbs_db_restart_triage --gateway $GW --api-key $HELPDESK_CLIENT_API_KEY
Gateway: http://localhost:8080  ·  version: dev  ·  host: cassiopeia

Auto-selected trace: faulttest-c09790ce-db-connection-refused-r3

=== Playbook Update Proposal: pbs_db_restart_triage ===

Current:  pb_5954d4e0 — Database Down — Restart Triage
Trace:    faulttest-c09790ce-db-connection-refused-r3 (outcome: resolved)

--- CURRENT DESCRIPTION ---
Triage a completely unresponsive PostgreSQL instance. Confirm the database is
unreachable, classify the failure type from pod logs and K8s events, and attempt
a controlled restart if the logs indicate a clean shutdown or transient OOM kill.
Use stored baseline data (data_directory, log paths) from prior get_server_info
snapshots to orient recovery when the database cannot be directly queried.


--- CURRENT GUIDANCE ---
IMPORTANT: When the database is down, psql-based tools (get_server_info,
get_slow_queries, etc.) will fail immediately. Use check_connection first
to confirm the failure mode (connection refused vs. timeout vs. auth error),
then shift to K8s tools and baseline history.

Step 1 — Confirm and classify the outage:
Run check_connection to capture the exact error. "Connection refused" means
the PostgreSQL process is not running. "Connection timed out" may indicate a
network or firewall issue rather than a DB failure — check K8s service and
endpoint health. "Password authentication failed" means the DB is up but
pg_hba.conf or credentials changed — this is not a restart scenario.

Step 2 — Identify the hosting type and check container/pod state:
Look at the Known Infrastructure section of your context for this server's entry.
- If the entry shows "[docker container: ...]" or "[podman container: ...]": the
  server is container-hosted on a VM. You cannot read docker logs or inspect the
  container directly. You MUST emit ESCALATE_TO: pbs_sysadmin_docker_inspect in
  your structured block — do not attempt to diagnose the container state yourself.
  The sysadmin agent has the tools (check_host, get_host_logs, read_pg_log_file)
  to determine definitively whether the container crashed, was stopped cleanly,
  or hit a disk/OOM issue. Do NOT guess or skip this escalation step.
- If the entry shows "Kubernetes: ...": use get_pod_status to determine the
container state. CrashLoopBackOff means PostgreSQL is repeatedly failing to
start — config or data issue, not a simple restart. OOMKilled means the pod
was killed by the OOM killer — a restart will likely succeed but the root
cause (memory pressure) must be addressed. Completed/Succeeded indicates the
process exited cleanly — check for intentional shutdown.

Step 3 — Read the logs (Kubernetes only):
Use get_pod_logs to retrieve the last 100–200 lines of PostgreSQL output.
Key signals to look for:
- "database system was shut down" followed by absence of "database system is
  ready to accept connections" — clean shutdown, safe to restart.
- "out of memory" or "OOM" in system/kernel messages — OOM kill, safe to
  restart but monitor memory.
- "FATAL" or "PANIC" lines — indicate a hard failure; see the config recovery
  or data recovery playbooks before attempting restart.
- "invalid page" or "checksum failure" — do NOT restart; escalate to data
  recovery immediately.

Step 4 — Check K8s events (Kubernetes only):
Use get_events for the pod's namespace to see recent evictions, OOM kills,
node pressure events, or storage mount failures. Storage mount failures
(PVC not bound, volume unavailable) cannot be resolved by a restart.

Step 5 — Attempt restart (only if logs show clean shutdown or OOM kill):
Use restart_deployment to trigger a rolling restart. Watch get_pod_status
for the new pod to reach Running state. Then retry check_connection — if
the database accepts connections within 60 seconds, the incident is resolved.

Step 6 — Use baseline data if the DB does not come back:
If the database was previously monitored, ToolResultStore history contains
a get_server_info snapshot with data_directory, config_file, hba_file,
log_directory, and log_filename. Retrieve this to orient deeper investigation
without requiring a live connection.

Common misdiagnosis: restarting a pod that is in CrashLoopBackOff due to a
config error — the restart will fail again immediately. Always read the logs
before restarting. If you see any FATAL or PANIC lines, use the config
recovery playbook instead.

Required output — write these exact lines at the end of your response,
no markdown, no extra text on any line. The handoff signal (TRANSITION_TO
or ESCALATE_TO) is mandatory: omitting it stalls the operator review gate:
HYPOTHESIS_1: <primary hypothesis about failure type> | CONFIDENCE: <0.0–1.0> | EVIDENCE: "<verbatim quote from pod logs or events>"
HYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>
ROOT_CAUSE: HYPOTHESIS_1
FINDINGS: hosting=<k8s|docker|bare_metal>; failure_type=<clean_shutdown|oom_kill|config_error|data_corruption|unknown>; restart_safe=<true|false>; escalate_to=<pbs_series_id | none>


--- PROPOSED DRAFT (from trace) ---
name: Database Down — Restart Triage
description: |
  Triage a completely unresponsive PostgreSQL instance. Confirm the database is
  unreachable, classify the failure type from pod logs and K8s events, and attempt
  a controlled restart if the logs indicate a clean shutdown or transient OOM kill.
  Use stored baseline data (data_directory, log paths) from prior get_server_info
  snapshots to orient recovery when the database cannot be directly queried.
problem_class: availability
symptoms:
  - database is not accepting connections
  - check_connection returns connection refused or timeout
  - monitoring alert: database_up=0
  - application errors: could not connect to server
  - K8s pod in CrashLoopBackOff or Error state
  - pg_isready returns 'no response'
  - repeated connection timeouts (sustained >30 seconds) even after initial check
guidance: |
  IMPORTANT: When the database is down, psql-based tools (get_server_info,
  get_slow_queries, etc.) will fail immediately. Use check_connection first
  to confirm the failure mode (connection refused vs. timeout vs. auth error),
  then shift to K8s tools and baseline history.

  Step 1 — Confirm and classify the outage:
  Run check_connection to capture the exact error and duration. "Connection refused"
  means the PostgreSQL process is not running. "Connection timed out" may indicate a
  network or firewall issue rather than a DB failure — check K8s service and endpoint
  health. "Password authentication failed" means the DB is up but pg_hba.conf or
  credentials changed — this is not a restart scenario. IMPORTANT: Check the
  duration_ms field in the tool result — if check_connection itself took >30 seconds
  to return, this indicates a slow or hung network/connectivity layer, not necessarily
  a down database process. Escalate to infrastructure if durations are consistently
  high even after restart.

  Step 2 — Identify the hosting type and check container/pod state:
  Look at the Known Infrastructure section of your context for this server's entry.
  - If the entry shows "[docker container: ...]" or "[podman container: ...]": the
    server is container-hosted on a VM. You cannot read docker logs or inspect the
    container directly. You MUST emit ESCALATE_TO: pbs_sysadmin_docker_inspect in
    your structured block — do not attempt to diagnose the container state yourself.
    The sysadmin agent has the tools (check_host, get_host_logs, read_pg_log_file)
    to determine definitively whether the container crashed, was stopped cleanly,
    or hit a disk/OOM issue. Do NOT guess or skip this escalation step.
  - If the entry shows "Kubernetes: ...": use get_pod_status to determine the
  container state. CrashLoopBackOff means PostgreSQL is repeatedly failing to
  start — config or data issue, not a simple restart. OOMKilled means the pod
  was killed by the OOM killer — a restart will likely succeed but the root
  cause (memory pressure) must be addressed. Completed/Succeeded indicates the
  process exited cleanly — check for intentional shutdown.

  Step 3 — Read the logs (Kubernetes only):
  Use get_pod_logs to retrieve the last 100–200 lines of PostgreSQL output.
  Key signals to look for:
  - "database system was shut down" followed by absence of "database system is
    ready to accept connections" — clean shutdown, safe to restart.
  - "out of memory" or "OOM" in system/kernel messages — OOM kill, safe to
    restart but monitor memory.
  - "FATAL" or "PANIC" lines — indicate a hard failure; see the config recovery
    or data recovery playbooks before attempting restart.
  - "invalid page" or "checksum failure" — do NOT restart; escalate to data
    recovery immediately.
  - Startup lines missing entirely (no "database system is ready to accept connections")
    but no FATAL/PANIC errors — indicates the startup sequence may have hung or
    been interrupted; check pod restart count and consider infrastructure issues
    (storage latency, network timeouts during initialization).

  Step 4 — Check K8s events (Kubernetes only):
  Use get_events for the pod's namespace to see recent evictions, OOM kills,
  node pressure events, or storage mount failures. Storage mount failures
  (PVC not bound, volume unavailable) cannot be resolved by a restart.
  Watch for repeated BackOff events within a short time window (e.g., >3 restarts
  in <5 minutes) — this pattern indicates a crash loop that a simple restart will
  not resolve.

  Step 5 — Validate connectivity tool behavior:
  If check_connection succeeded but took an unusually long time (>20 seconds, or
  >2x the baseline from prior runs), note this in your hypothesis. Long connection
  times during what should be a "down" incident may indicate:
  - Network partition or latency spike (not a database process issue)
  - Database process is running but severely CPU-starved or I/O-bound
  - Connection pooling or kernel network stack backlog
  In these cases, escalate to infrastructure monitoring before assuming a restart
  will help.

  Step 6 — Attempt restart (only if logs show clean shutdown or OOM kill):
  Use restart_deployment to trigger a rolling restart. Watch get_pod_status
  for the new pod to reach Running state. Then retry check_connection — if
  the database accepts connections within 60 seconds, the incident is resolved.

  Step 7 — Use baseline data if the DB does not come back:
  If the database was previously monitored, ToolResultStore history contains
  a get_server_info snapshot with data_directory, config_file, hba_file,
  log_directory, and log_filename. Retrieve this to orient deeper investigation
  without requiring a live connection.

  Common misdiagnosis: restarting a pod that is in CrashLoopBackOff due to a
  config error — the restart will fail again immediately. Always read the logs
  before restarting. If you see any FATAL or PANIC lines, use the config
  recovery playbook instead. Also, do not confuse a slow connection check
  (high duration_ms) with a database that is truly down — instrument your
  decision with baseline connection times from the ToolResultStore.

  Required output — write these exact lines at the end of your response,
  no markdown, no extra text on any line. The handoff signal (TRANSITION_TO
  or ESCALATE_TO) is mandatory — omitting it stalls the operator review gate:
  HYPOTHESIS_1: <primary hypothesis about failure type> | CONFIDENCE: <0.0–1.0> | EVIDENCE: "<verbatim quote from pod logs or events>"
  HYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>
  ROOT_CAUSE: HYPOTHESIS_1
  FINDINGS: hosting=<k8s|docker|bare_metal>; failure_type=<clean_shutdown|oom_kill|config_error|data_corruption|unknown>; restart_safe=<true|false>; escalate_to=<pbs_series_id | none>
escalation:
  - pod logs contain 'invalid page in block' or 'checksum failure'
  - pod logs contain PANIC (not FATAL) — indicates unrecoverable state
  - CrashLoopBackOff and logs show FATAL config errors — use config recovery playbook
  - storage mount failure or PVC not bound — requires infrastructure intervention
  - database does not accept connections within 5 minutes of pod restart
  - get_events shows node NotReady or repeated evictions — node-level issue
  - check_connection duration_ms consistently >20 seconds across multiple attempts — escalate to infrastructure for network/kernel diagnosis
  - pod restart count >3 within a 5-minute window — indicates crash loop, not transient failure
  - get_pod_logs missing startup success message and no FATAL/PANIC errors — possible hung initialization; check storage and network latency before restart

Proposed draft saved as: pb_7d04912b (inactive, source=generated)

# To activate the proposed draft:
#   curl -X POST http://localhost:8080/api/v1/fleet/playbooks/pb_7d04912b/activate \
#        -H 'Authorization: Bearer <key>'
```

Let's check `vault versions`:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault versions \
      pbs_db_restart_triage \
      --gateway $HELPDESK_GATEWAY_URL \
      --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.20.0-7-g3475cfd  ·  host: <your-host>

Version stats for pbs_db_restart_triage

VERSION     RUNS    SUCCESS%   AVG STEPS   AVG TIME    AVG DIAG   AVG REMED  APPROACH OK  JUDGE VERDICT
────────────────────────────────────────────────────────────────────────────────────────────────────
1.4         1       0%         –           6s          –          –          –          –
  id=pb_e5334286
1.5 *       8       12%        –           17s         81%        –          –          –
  id=pb_5954d4e0

* = currently active   SUCCESS% = resolved + transitioned
id/from lines show playbook_id and the run that generated that version
```

Now let's the diff the two playbook versions 1.5 and the new 1.6 we've just generated via `vault suggest-update`:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault diff \
      --judge pb_5954d4e0 pb_7d04912b \
      --gateway $HELPDESK_GATEWAY_URL \
      --api-key $HELPDESK_CLIENT_API_KEY
      --judge-vendor anthropic \
      --judge-model claude-haiku-4-5-20251001 \
      --judge-api-key $ANTHROPIC_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.20.0-7-g3475cfd  ·  host: <your-host>

Diff: series pbs_db_restart_triage
  before  pb_5954d4e0  v1.5  Database Down — Restart Triage
  after   pb_7d04912b  v1.6  Database Down — Restart Triage

── guidance ────────────────────
  before  IMPORTANT: When the database is down, psql-based tools (get_server_info,
          get_slow_queries, etc.) will fail immediately. Use check_connection first
          to confirm the failure mode (connection refused vs. timeout vs. auth error),
          then shift to K8s tools and baseline history.

          Step 1 — Confirm and classify the outage:
          Run check_connection to capture the exact error. "Connection refused" means
          the PostgreSQL process is not running. "Connection timed out" may indicate a
          network or firewall issue rather than a DB failure — check K8s service and
          endpoint health. "Password authentication failed" means the DB is up but
          pg_hba.conf or credentials changed — this is not a restart scenario.

          Step 2 — Identify the hosting type and check container/pod state:
          Look at the Known Infrastructure section of your context for this server's entry.
          - If the entry shows "[docker container: ...]" or "[podman container: ...]": the
            server is container-hosted on a VM. You cannot read docker logs or inspect the
            container directly. You MUST emit ESCALATE_TO: pbs_sysadmin_docker_inspect in
            your structured block — do not attempt to diagnose the container state yourself.
            The sysadmin agent has the tools (check_host, get_host_logs, read_pg_log_file)
            to determine definitively whether the container crashed, was stopped cleanly,
            or hit a disk/OOM issue. Do NOT guess or skip this escalation step.
          - If the entry shows "Kubernetes: ...": use get_pod_status to determine the
          container state. CrashLoopBackOff means PostgreSQL is repeatedly failing to
          start — config or data issue, not a simple restart. OOMKilled means the pod
          was killed by the OOM killer — a restart will likely succeed but the root
          cause (memory pressure) must be addressed. Completed/Succeeded indicates the
          process exited cleanly — check for intentional shutdown.

          Step 3 — Read the logs (Kubernetes only):
          Use get_pod_logs to retrieve the last 100–200 lines of PostgreSQL output.
          Key signals to look for:
          - "database system was shut down" followed by absence of "database system is
            ready to accept connections" — clean shutdown, safe to restart.
          - "out of memory" or "OOM" in system/kernel messages — OOM kill, safe to
            restart but monitor memory.
          - "FATAL" or "PANIC" lines — indicate a hard failure; see the config recovery
            or data recovery playbooks before attempting restart.
          - "invalid page" or "checksum failure" — do NOT restart; escalate to data
            recovery immediately.

          Step 4 — Check K8s events (Kubernetes only):
          Use get_events for the pod's namespace to see recent evictions, OOM kills,
          node pressure events, or storage mount failures. Storage mount failures
          (PVC not bound, volume unavailable) cannot be resolved by a restart.

          Step 5 — Attempt restart (only if logs show clean shutdown or OOM kill):
          Use restart_deployment to trigger a rolling restart. Watch get_pod_status
          for the new pod to reach Running state. Then retry check_connection — if
          the database accepts connections within 60 seconds, the incident is resolved.

          Step 6 — Use baseline data if the DB does not come back:
          If the database was previously monitored, ToolResultStore history contains
          a get_server_info snapshot with data_directory, config_file, hba_file,
          log_directory, and log_filename. Retrieve this to orient deeper investigation
          without requiring a live connection.

          Common misdiagnosis: restarting a pod that is in CrashLoopBackOff due to a
          config error — the restart will fail again immediately. Always read the logs
          before restarting. If you see any FATAL or PANIC lines, use the config
          recovery playbook instead.

          Required output — write these exact lines at the end of your response,
          no markdown, no extra text on any line. The handoff signal (TRANSITION_TO
          or ESCALATE_TO) is mandatory: omitting it stalls the operator review gate:
          HYPOTHESIS_1: <primary hypothesis about failure type> | CONFIDENCE: <0.0–1.0> | EVIDENCE: "<verbatim quote from pod logs or events>"
          HYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>
          ROOT_CAUSE: HYPOTHESIS_1
          FINDINGS: hosting=<k8s|docker|bare_metal>; failure_type=<clean_shutdown|oom_kill|config_error|data_corruption|unknown>; restart_safe=<true|false>; escalate_to=<pbs_series_id | none>
  after   IMPORTANT: When the database is down, psql-based tools (get_server_info,
          get_slow_queries, etc.) will fail immediately. Use check_connection first
          to confirm the failure mode (connection refused vs. timeout vs. auth error),
          then shift to K8s tools and baseline history.

          Step 1 — Confirm and classify the outage:
          Run check_connection to capture the exact error and duration. "Connection refused"
          means the PostgreSQL process is not running. "Connection timed out" may indicate a
          network or firewall issue rather than a DB failure — check K8s service and endpoint
          health. "Password authentication failed" means the DB is up but pg_hba.conf or
          credentials changed — this is not a restart scenario. IMPORTANT: Check the
          duration_ms field in the tool result — if check_connection itself took >30 seconds
          to return, this indicates a slow or hung network/connectivity layer, not necessarily
          a down database process. Escalate to infrastructure if durations are consistently
          high even after restart.

          Step 2 — Identify the hosting type and check container/pod state:
          Look at the Known Infrastructure section of your context for this server's entry.
          - If the entry shows "[docker container: ...]" or "[podman container: ...]": the
            server is container-hosted on a VM. You cannot read docker logs or inspect the
            container directly. You MUST emit ESCALATE_TO: pbs_sysadmin_docker_inspect in
            your structured block — do not attempt to diagnose the container state yourself.
            The sysadmin agent has the tools (check_host, get_host_logs, read_pg_log_file)
            to determine definitively whether the container crashed, was stopped cleanly,
            or hit a disk/OOM issue. Do NOT guess or skip this escalation step.
          - If the entry shows "Kubernetes: ...": use get_pod_status to determine the
          container state. CrashLoopBackOff means PostgreSQL is repeatedly failing to
          start — config or data issue, not a simple restart. OOMKilled means the pod
          was killed by the OOM killer — a restart will likely succeed but the root
          cause (memory pressure) must be addressed. Completed/Succeeded indicates the
          process exited cleanly — check for intentional shutdown.

          Step 3 — Read the logs (Kubernetes only):
          Use get_pod_logs to retrieve the last 100–200 lines of PostgreSQL output.
          Key signals to look for:
          - "database system was shut down" followed by absence of "database system is
            ready to accept connections" — clean shutdown, safe to restart.
          - "out of memory" or "OOM" in system/kernel messages — OOM kill, safe to
            restart but monitor memory.
          - "FATAL" or "PANIC" lines — indicate a hard failure; see the config recovery
            or data recovery playbooks before attempting restart.
          - "invalid page" or "checksum failure" — do NOT restart; escalate to data
            recovery immediately.
          - Startup lines missing entirely (no "database system is ready to accept connections")
            but no FATAL/PANIC errors — indicates the startup sequence may have hung or
            been interrupted; check pod restart count and consider infrastructure issues
            (storage latency, network timeouts during initialization).

          Step 4 — Check K8s events (Kubernetes only):
          Use get_events for the pod's namespace to see recent evictions, OOM kills,
          node pressure events, or storage mount failures. Storage mount failures
          (PVC not bound, volume unavailable) cannot be resolved by a restart.
          Watch for repeated BackOff events within a short time window (e.g., >3 restarts
          in <5 minutes) — this pattern indicates a crash loop that a simple restart will
          not resolve.

          Step 5 — Validate connectivity tool behavior:
          If check_connection succeeded but took an unusually long time (>20 seconds, or
          >2x the baseline from prior runs), note this in your hypothesis. Long connection
          times during what should be a "down" incident may indicate:
          - Network partition or latency spike (not a database process issue)
          - Database process is running but severely CPU-starved or I/O-bound
          - Connection pooling or kernel network stack backlog
          In these cases, escalate to infrastructure monitoring before assuming a restart
          will help.

          Step 6 — Attempt restart (only if logs show clean shutdown or OOM kill):
          Use restart_deployment to trigger a rolling restart. Watch get_pod_status
          for the new pod to reach Running state. Then retry check_connection — if
          the database accepts connections within 60 seconds, the incident is resolved.

          Step 7 — Use baseline data if the DB does not come back:
          If the database was previously monitored, ToolResultStore history contains
          a get_server_info snapshot with data_directory, config_file, hba_file,
          log_directory, and log_filename. Retrieve this to orient deeper investigation
          without requiring a live connection.

          Common misdiagnosis: restarting a pod that is in CrashLoopBackOff due to a
          config error — the restart will fail again immediately. Always read the logs
          before restarting. If you see any FATAL or PANIC lines, use the config
          recovery playbook instead. Also, do not confuse a slow connection check
          (high duration_ms) with a database that is truly down — instrument your
          decision with baseline connection times from the ToolResultStore.

          Required output — write these exact lines at the end of your response,
          no markdown, no extra text on any line. The handoff signal (TRANSITION_TO
          or ESCALATE_TO) is mandatory — omitting it stalls the operator review gate:
          HYPOTHESIS_1: <primary hypothesis about failure type> | CONFIDENCE: <0.0–1.0> | EVIDENCE: "<verbatim quote from pod logs or events>"
          HYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>
          ROOT_CAUSE: HYPOTHESIS_1
          FINDINGS: hosting=<k8s|docker|bare_metal>; failure_type=<clean_shutdown|oom_kill|config_error|data_corruption|unknown>; restart_safe=<true|false>; escalate_to=<pbs_series_id | none>

── symptoms ────────────────────
  before  database is not accepting connections
          check_connection returns connection refused or timeout
          monitoring alert: database_up=0
          application errors: could not connect to server
          K8s pod in CrashLoopBackOff or Error state
          pg_isready returns 'no response'
  after   database is not accepting connections
          check_connection returns connection refused or timeout
          monitoring alert: database_up=0
          application errors: could not connect to server
          K8s pod in CrashLoopBackOff or Error state
          pg_isready returns 'no response'
          repeated connection timeouts (sustained >30 seconds) even after initial check

── escalation ───────────────────�
  before  pod logs contain 'invalid page in block' or 'checksum failure'
          pod logs contain PANIC (not FATAL) — indicates unrecoverable state
          CrashLoopBackOff and logs show FATAL config errors — use config recovery playbook
          storage mount failure or PVC not bound — requires infrastructure intervention
          database does not accept connections within 5 minutes of pod restart
          get_events shows node NotReady or repeated evictions — node-level issue
  after   pod logs contain 'invalid page in block' or 'checksum failure'
          pod logs contain PANIC (not FATAL) — indicates unrecoverable state
          CrashLoopBackOff and logs show FATAL config errors — use config recovery playbook
          storage mount failure or PVC not bound — requires infrastructure intervention
          database does not accept connections within 5 minutes of pod restart
          get_events shows node NotReady or repeated evictions — node-level issue
          check_connection duration_ms consistently >20 seconds across multiple attempts — escalate to infrastructure for network/kernel diagnosis
          pod restart count >3 within a 5-minute window — indicates crash loop, not transient failure
          get_pod_logs missing startup success message and no FATAL/PANIC errors — possible hung initialization; check storage and network latency before restart

3 field(s) changed.

To activate:  faulttest vault activate pb_7d04912b --gateway http://localhost:8080 --api-key <key>
To discard:   curl -X DELETE http://localhost:8080/api/v1/fleet/playbooks/pb_7d04912b -H 'Authorization: Bearer <key>'

── LLM Judge Review ────────────────────────────────────────────────────
time=2026-07-12T12:10:31.684-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
Verdict:            ✓  APPROVE
Guidance quality:   The update adds concrete diagnostic signals (connection duration_ms thresholds, startup sequence absence patterns, restart count heuristics) and introduces a new validation step (Step 5) that distinguishes slow            connections from true outages, reducing misdiagnosis of infrastructure vs. process failures.
Escalation safety:  Escalation criteria are tighter and safer: the update adds three new criteria (connection duration >20s, restart count >3 in 5min, missing startup logs without FATAL/PANIC) that catch subtle infrastructure and crash-loop  patterns earlier, preventing wasteful restarts and enabling faster root-cause identification.
Reasoning:          Version 1.6 improves diagnostic precision and escalation safety by quantifying soft signals (duration thresholds, restart frequency) and explicitly separating network/infrastructure issues from database process failures,  with no loss of existing safeguards.
Judge model:        claude-haiku-4-5-20251001
Verdict saved to pb_7d04912b.
```

APPROVE verdict recorded on `pb_7d04912b`. But before activating, it's worth noticing that the generated v1.6 and our local v1.7 are different changes.

The AI-generated draft (v1.6, what was just judged) added:
- `duration_ms` threshold checks in Step 1 and 5
- A new "connectivity validation" Step 5
- Three new escalation criteria around crash-loop patterns

Our local v1.7 added:
- The "unknown hosting → default to escalation" rule in Step 2

The judge APPROVEd v1.6's additions on their own merits because they ARE genuine improvements.
But they don't address the specific gap that caused UNSTABLE: the agent withholding escalation when no Known Infrastructure entry exists.
Version v1.6 Step 2 is identical to v1.5 Step 2 on the unknown-hosting case.

But notice the catch: activating v1.6 and re-running is now likely to produce a wrong APPROVE!
The judge predicted improvement, but stability won't change because the root cause of UNSTABLE is still present.
That's exactly what `vault judge-accuracy` is built to catch.

The full story is this: judge over-predicted on v1.6, then operator uploads the targeted v1.7 fix, judge APPROVEs that one too and re-run confirms STABLE.

Let's proceed with activating v1.6 "as-is". Then re-run and check `vault judge-accuracy`. Let's see if the data matches our prediction:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault \
      activate pb_7d04912b \
      --judge-api-key $ANTHROPIC_API_KEY \
      --gateway $HELPDESK_GATEWAY_URL \
      --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.20.0-7-g3475cfd  ·  host: <your-host>

Activated: pb_7d04912b  v1.6  Database Down — Restart Triage
Series:    pbs_db_restart_triage

faulttest vault active --gateway http://localhost:8080 --api-key <key>
```

Let's confirm:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault \
      active pb_7d04912b \
      --judge-api-key $ANTHROPIC_API_KEY \
      --gateway $HELPDESK_GATEWAY_URL \
      --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.20.0-7-g3475cfd  ·  host: <your-host>

SERIES                              VERSION  SOURCE     UPDATED     NAME
────────────────────────────────────────────────────────────────────────────────────────────────────────────────
pbs_db_restart_triage               1.6      generated  2026-07-12  Database Down — Restart Triage
```

And the playbook version v1.6 is now live. Now let's run the same repeat command to get the post-activation data point:

```
[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest run \
      --ids db-connection-refused \
      --auto-db \
      --repeat 3 \
      --agent-model claude-sonnet-4-6 \
      --judge --judge-vendor anthropic \
      --judge-model claude-haiku-4-5-20251001 \
      --judge-api-key $ANTHROPIC_API_KEY \
      --gateway $HELPDESK_GATEWAY_URL \
      --api-key $HELPDESK_CLIENT_API_KEY \
      --approval-mode force \
      --emit-and-wait --gate-escalation

Sun Jul 12 15:52:34 EDT 2026
Starting temporary PostgreSQL container (postgres:16-alpine)...
Auto-DB ready: host=127.0.0.1 port=60556 dbname=faulttest user=postgres password=faulttest sslmode=disable

time=2026-07-12T15:52:38.027-04:00 level=INFO msg="auto-DB registered with gateway" server_id=faulttest-auto-60556
time=2026-07-12T15:52:38.029-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-07-12T15:52:38.029-04:00 level=INFO msg="LLM diagnosis judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: Database connection refused (db-connection-refused) — 3 runs ---

  Run 1/3
time=2026-07-12T15:52:38.029-04:00 level=INFO msg="injecting failure" id=db-connection-refused type=shell_exec mode=external conn=127.0.0.1
time=2026-07-12T15:52:38.346-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-9fb0c12e\nPostgreSQL container faulttest-auto-db-9fb0c12e stopped"
time=2026-07-12T15:52:38.847-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-connection-refused series_id=pbs_db_restart_triage playbook_id=pb_7d04912b gateway=http://localhost:8080 agent-conn="host=127.0.0.1           port=60556 dbname=faulttest user=postgres password=faulttest sslmode=disable"
  Feedback submitted (triage/post_incident run_id=plr_fa0bedba)
time=2026-07-12T15:53:09.362-04:00 level=INFO msg="tearing down failure" id=db-connection-refused type=shell_exec conn=127.0.0.1
time=2026-07-12T15:53:12.516-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-9fb0c12e\nPostgreSQL container faulttest-auto-db-9fb0c12e started"
  [PASS] score=73%
         [PRIMARY 95%] PostgreSQL process is not running on 127.0.0.1:60556 — process crashed, exited unexpectedly, or was stopped
         [REJECTED 5%] Network misconfiguration or firewall rule blocking access to port 60556 — "Connection refused" strongly indicates the port is not listening (process down), not a network routing issue

  Run 2/3
time=2026-07-12T15:53:12.516-04:00 level=INFO msg="injecting failure" id=db-connection-refused type=shell_exec mode=external conn=127.0.0.1
time=2026-07-12T15:53:12.695-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-9fb0c12e\nPostgreSQL container faulttest-auto-db-9fb0c12e stopped"
time=2026-07-12T15:53:13.252-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-connection-refused series_id=pbs_db_restart_triage playbook_id=pb_7d04912b gateway=http://localhost:8080 agent-conn="host=127.0.0.1           port=60556 dbname=faulttest user=postgres password=faulttest sslmode=disable"
  Feedback submitted (triage/post_incident run_id=plr_dd1e58e6)
time=2026-07-12T15:53:39.390-04:00 level=INFO msg="tearing down failure" id=db-connection-refused type=shell_exec conn=127.0.0.1
time=2026-07-12T15:53:42.531-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-9fb0c12e\nPostgreSQL container faulttest-auto-db-9fb0c12e started"
  [PASS] score=100%
         [PRIMARY 95%] PostgreSQL process is not running or not listening on 127.0.0.1:60556 due to container crash, stop, or misconfiguration
         [REJECTED 5%] Port binding issue or firewall block preventing connection to a running process — "Connection refused is a definitive indicator the process is not accepting connections on that port; network-level issues would produce  timeout or unreachable host errors instead."

  Run 3/3
time=2026-07-12T15:53:42.531-04:00 level=INFO msg="injecting failure" id=db-connection-refused type=shell_exec mode=external conn=127.0.0.1
time=2026-07-12T15:53:42.839-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-9fb0c12e\nPostgreSQL container faulttest-auto-db-9fb0c12e stopped"
time=2026-07-12T15:53:43.344-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-connection-refused series_id=pbs_db_restart_triage playbook_id=pb_7d04912b gateway=http://localhost:8080 agent-conn="host=127.0.0.1           port=60556 dbname=faulttest user=postgres password=faulttest sslmode=disable"
time=2026-07-12T15:54:06.769-04:00 level=WARN msg="gateway warning" failure=db-connection-refused warning="triage agent omitted required TRANSITION_TO/ESCALATE_TO signal; gate created from remediation_series_id"
  Feedback submitted (triage/post_incident run_id=plr_0e85570b)
time=2026-07-12T15:54:09.319-04:00 level=INFO msg="tearing down failure" id=db-connection-refused type=shell_exec conn=127.0.0.1
time=2026-07-12T15:54:12.842-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-9fb0c12e\nPostgreSQL container faulttest-auto-db-9fb0c12e started"
  [PASS] score=75% protocol-violation
         [PRIMARY 95%] PostgreSQL process at 127.0.0.1:60556 is not running or not accepting connections
         [REJECTED 5%] Firewall or network-level connectivity issue preventing TCP connection — Connection refused specifically indicates the target port is not listening (not a network layer issue); if it were a firewall, the connection     would time out.

  Stability report (3 runs):
    Pass rate:    3/3 (100%)
    Confidence:   min=95% max=95% range=0pp mean=95%  (H1, passing runs only)
    Violations:   1 protocol violation(s)
    Verdict:      STABLE
  ────────────────────────────────────────────────────────────────
time=2026-07-12T15:54:13.687-04:00 level=INFO msg="fault stability cert posted" fault_id=db-connection-refused verdict=STABLE n_runs=3

=== Fault Test Report: 47abebb7 ===

[PASS] Database connection refused (db-connection-refused) - score: 73% [judge: 33%]
       Keywords: 100% | Tools: 100% | Judge: 33%
       Reasoning: "Agent correctly identified the connection refused symptom and PostgreSQL process not running, but failed to recognize the Docker-hosted nature of the fault-injected server and missed the critical escalation to              pbs_sysadmin_docker_inspect; instead, incorrectly classified it as a standalone VM-based instance requiring sysadmin escalation outside the agent's scope."
[PASS] Database connection refused (db-connection-refused) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Reasoning: "Agent correctly identified connection refused as the root cause (PostgreSQL not running/listening), properly classified the hosting type as Docker-based, correctly reasoned through tool limitations, and appropriately       escalated to pbs_sysadmin_docker_inspect per playbook guidance for Docker-hosted databases that are down and unreachable."
[PASS] Database connection refused (db-connection-refused) - score: 75% [judge: 67%]
       Keywords: 100% | Tools: 100% | Judge: 67%
       [WARN] protocol violation: triage agent omitted TRANSITION_TO/ESCALATE_TO — diagnosis score capped at 75%
       Reasoning: "Agent correctly identified the root cause (PostgreSQL process not running/not accepting connections on port 60556) with strong evidence, but failed to escalate to pbs_sysadmin_docker_inspect as expected; instead, the agent over-interpreted the user's 'standalone' statement and concluded Docker inspection was forbidden, when the expected diagnosis explicitly states escalation to Docker inspection is the correct outcome for container-hosted scenarios."

--- Summary ---
Total: 3 | Passed: 3 | Failed: 0 | Rate: 100%
  database: 3/3 (100%)

Note: 1 fault(s) had a protocol violation — triage agent omitted the required
TRANSITION_TO/ESCALATE_TO handoff signal. Diagnosis score was capped at 75%.
Tighten playbook guidance to enforce the signal; the fallback gate caught this run.

LLM judge scored diagnosis for 3 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-47abebb7.json
--- BEGIN FAULTTEST REPORT JSON ---
...
--- END FAULTTEST REPORT JSON ---
Evaluation scores posted to auditd: 3/3 runs

real    1m40.309s
user    0m0.398s
sys     0m0.478s
```

The cert says STABLE but the run tells a more nuanced story. Three things to unpack before running vault judge-accuracy.

1/ What flipped from UNSTABLE → STABLE:
  The stability cert measures PRIMARY CONFIDENCE range, not judge score.
  Previous run: 60%/100%/100% → 40pp → UNSTABLE.
  This run: 95%/95%/95% → 0pp → STABLE.
  The agent became more consistently confident in its symptom identification ("connection refused = process down"), so the cert improved... even though the decision quality didn't.

2/ The agent behavior is still broken in 2 of 3 runs:
  - Run 1 (judge 33%): read Known Infrastructure, found no entry for port 60556, concluded "standalone VM", withheld Docker escalation
  - Run 2 (judge 100%): reasoned through the baseline data patterns to infer Docker hosting, escalated correctly
  - Run 3 (judge 67% + protocol violation): read "standalone PostgreSQL" from context, concluded Docker inspection was "forbidden" by the user, omitted `ESCALATE_TO` entirely

  The agent in runs 1 and 3 is reading something in the gateway context that says "standalone PostgreSQL — NOT Kubernetes-managed. Do NOT use kubectl or K8s tools." 
  That text is overriding its reasoning about Docker hosting.

3/ The problem is that the gateway registration only sends `server_id`, `name`, `connection_string` and `tags`. No `hosting_type` or container metadata.
   The "standalone PostgreSQL — NOT Kubernetes-managed" text in the agent's context is the gateway's default injection for servers with no container info.
   So the agent correctly reads that default and in 2 of 3 runs concludes Docker escalation is "forbidden."

This means that two separate fixes are needed not one:

1/ Registration fix:
   Add `container_name` to registerAutoDBWithGateway so the auto-db gets the right hosting context injected

2/ Playbook fix (our v1.7):
   For real incidents where a server exists in prod, but has no `hosting_type` entry, set the "unknown → escalate anyway" rule

The interesting part here is this: the cert went UNSTABLE → STABLE, so `vault judge-accuracy` now record APPROVE as correct... even though the agent quality (33%/100%/67%) didn't actually improve.
That's a real finding worth noting: the stability cert measures confidence consistency,
And a judge optimized against the cert can get a "correct" APPROVE by changing the agent's calibration without changing its actual escalation behavior.

Now why we didn't run into this before? Because for the first time in these examples we are using `--auto-db` feature (ephemeral database) combined with the playbook escalation feature where the control is passed by the DB Agent to the SysAdmin Agent to continue the triage process.




Uploading a file gives back the draft ID. When `-file` path is provided, POST call is issued with the draft to `/api/v1/fleet/playbooks` to persist it and get a real `playbook_id` (with the `/import` endpoint being validate-only).

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault suggest-update \
      --series-id pbs_db_restart_triage \
      --file playbooks/database-restart-triage.yaml \
      --gateway $HELPDESK_GATEWAY_URL \
      --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.20.0-7-g3475cfd  ·  host: <your-host>

Uploaded: pb_2c498091  v1.7  Database Down — Restart Triage
Series:   pbs_db_restart_triage

# To diff against active version:
#   faulttest vault diff --judge <active-id> pb_2c498091 --gateway http://localhost:8080 --api-key <key>
# To activate:
#   faulttest vault activate pb_2c498091 --gateway http://localhost:8080 --api-key <key>
```

So `pb_2c498091` is the version v1.7 draft. The active v1.6 was `pb_7d04912b`. Now let's run the judge diff:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault diff \
      --judge pb_7d04912b pb_2c498091 \
      --gateway $HELPDESK_GATEWAY_URL \
      --api-key $HELPDESK_CLIENT_API_KEY
      --judge-vendor anthropic \
      --judge-model claude-haiku-4-5-20251001 \
      --judge-api-key $ANTHROPIC_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.20.0-7-g3475cfd  ·  host: <your-host>

Diff: series pbs_db_restart_triage
  before  pb_7d04912b  v1.6  Database Down — Restart Triage
  after   pb_2c498091  v1.7  Database Down — Restart Triage

── guidance ────────────────────
  before  IMPORTANT: When the database is down, psql-based tools (get_server_info,
          get_slow_queries, etc.) will fail immediately. Use check_connection first
          to confirm the failure mode (connection refused vs. timeout vs. auth error),
          then shift to K8s tools and baseline history.

          Step 1 — Confirm and classify the outage:
          Run check_connection to capture the exact error and duration. "Connection refused"
          means the PostgreSQL process is not running. "Connection timed out" may indicate a
          network or firewall issue rather than a DB failure — check K8s service and endpoint
          health. "Password authentication failed" means the DB is up but pg_hba.conf or
          credentials changed — this is not a restart scenario. IMPORTANT: Check the
          duration_ms field in the tool result — if check_connection itself took >30 seconds
          to return, this indicates a slow or hung network/connectivity layer, not necessarily
          a down database process. Escalate to infrastructure if durations are consistently
          high even after restart.

          Step 2 — Identify the hosting type and check container/pod state:
          Look at the Known Infrastructure section of your context for this server's entry.
          - If the entry shows "[docker container: ...]" or "[podman container: ...]": the
            server is container-hosted on a VM. You cannot read docker logs or inspect the
            container directly. You MUST emit ESCALATE_TO: pbs_sysadmin_docker_inspect in
            your structured block — do not attempt to diagnose the container state yourself.
            The sysadmin agent has the tools (check_host, get_host_logs, read_pg_log_file)
            to determine definitively whether the container crashed, was stopped cleanly,
            or hit a disk/OOM issue. Do NOT guess or skip this escalation step.
          - If the entry shows "Kubernetes: ...": use get_pod_status to determine the
          container state. CrashLoopBackOff means PostgreSQL is repeatedly failing to
          start — config or data issue, not a simple restart. OOMKilled means the pod
          was killed by the OOM killer — a restart will likely succeed but the root
          cause (memory pressure) must be addressed. Completed/Succeeded indicates the
          process exited cleanly — check for intentional shutdown.

          Step 3 — Read the logs (Kubernetes only):
          Use get_pod_logs to retrieve the last 100–200 lines of PostgreSQL output.
          Key signals to look for:
          - "database system was shut down" followed by absence of "database system is
            ready to accept connections" — clean shutdown, safe to restart.
          - "out of memory" or "OOM" in system/kernel messages — OOM kill, safe to
            restart but monitor memory.
          - "FATAL" or "PANIC" lines — indicate a hard failure; see the config recovery
            or data recovery playbooks before attempting restart.
          - "invalid page" or "checksum failure" — do NOT restart; escalate to data
            recovery immediately.
          - Startup lines missing entirely (no "database system is ready to accept connections")
            but no FATAL/PANIC errors — indicates the startup sequence may have hung or
            been interrupted; check pod restart count and consider infrastructure issues
            (storage latency, network timeouts during initialization).

          Step 4 — Check K8s events (Kubernetes only):
          Use get_events for the pod's namespace to see recent evictions, OOM kills,
          node pressure events, or storage mount failures. Storage mount failures
          (PVC not bound, volume unavailable) cannot be resolved by a restart.
          Watch for repeated BackOff events within a short time window (e.g., >3 restarts
          in <5 minutes) — this pattern indicates a crash loop that a simple restart will
          not resolve.

          Step 5 — Validate connectivity tool behavior:
          If check_connection succeeded but took an unusually long time (>20 seconds, or
          >2x the baseline from prior runs), note this in your hypothesis. Long connection
          times during what should be a "down" incident may indicate:
          - Network partition or latency spike (not a database process issue)
          - Database process is running but severely CPU-starved or I/O-bound
          - Connection pooling or kernel network stack backlog
          In these cases, escalate to infrastructure monitoring before assuming a restart
          will help.

          Step 6 — Attempt restart (only if logs show clean shutdown or OOM kill):
          Use restart_deployment to trigger a rolling restart. Watch get_pod_status
          for the new pod to reach Running state. Then retry check_connection — if
          the database accepts connections within 60 seconds, the incident is resolved.

          Step 7 — Use baseline data if the DB does not come back:
          If the database was previously monitored, ToolResultStore history contains
          a get_server_info snapshot with data_directory, config_file, hba_file,
          log_directory, and log_filename. Retrieve this to orient deeper investigation
          without requiring a live connection.

          Common misdiagnosis: restarting a pod that is in CrashLoopBackOff due to a
          config error — the restart will fail again immediately. Always read the logs
          before restarting. If you see any FATAL or PANIC lines, use the config
          recovery playbook instead. Also, do not confuse a slow connection check
          (high duration_ms) with a database that is truly down — instrument your
          decision with baseline connection times from the ToolResultStore.

          Required output — write these exact lines at the end of your response,
          no markdown, no extra text on any line. The handoff signal (TRANSITION_TO
          or ESCALATE_TO) is mandatory — omitting it stalls the operator review gate:
          HYPOTHESIS_1: <primary hypothesis about failure type> | CONFIDENCE: <0.0–1.0> | EVIDENCE: "<verbatim quote from pod logs or events>"
          HYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>
          ROOT_CAUSE: HYPOTHESIS_1
          FINDINGS: hosting=<k8s|docker|bare_metal>; failure_type=<clean_shutdown|oom_kill|config_error|data_corruption|unknown>; restart_safe=<true|false>; escalate_to=<pbs_series_id | none>
  after   IMPORTANT: When the database is down, psql-based tools (get_server_info,
          get_slow_queries, etc.) will fail immediately. Use check_connection first
          to confirm the failure mode, then shift to K8s tools and baseline history.

          Step 1 — Confirm and classify the outage:
          Run check_connection to capture the exact error. Different errors indicate
          very different root causes — read the message carefully before proceeding:
          - "connection refused" → the PostgreSQL process is not listening on that port.
            Either the server is stopped, or a firewall is blocking the TCP connection.
            Proceed to Step 2 to determine which. Do NOT restart until Step 3/4 confirm
            the server is actually down.
          - "sorry, too many clients already" or "remaining connection slots are reserved"
            → max_connections is exhausted. The database IS running. Do NOT restart —
            that would disrupt every active connection for no benefit. Check
            pg_stat_activity for idle/idle-in-transaction sessions; use
            kill_idle_connections or terminate_connection to free slots. Set
            restart_safe=false and failure_type=max_connections_exceeded in FINDINGS.
          - "no pg_hba.conf entry for host" or "password authentication failed" → the
            database IS running but the connection is blocked by authentication config.
            Do NOT restart. Set failure_type=pg_hba_auth_blocked in FINDINGS and
            recommend inspecting pg_hba.conf or the credentials used.
          - "connection timed out" → may indicate a network or firewall issue. Check K8s
            service and endpoint health before assuming the DB process is down. Set
            failure_type=unknown in FINDINGS.

          Step 2 — Identify the hosting type and check container/pod state:
          Only proceed here when Step 1 confirmed "connection refused" (server likely stopped).
          Look at the Known Infrastructure section of your context for this server's entry.
          - If the entry shows "[docker container: ...]" or "[podman container: ...]": the
            server is container-hosted on a VM. You cannot read docker logs or inspect the
            container directly. You MUST emit ESCALATE_TO: pbs_sysadmin_docker_inspect in
            your structured block — do not attempt to diagnose the container state yourself.
            The sysadmin agent has the tools (check_host, get_host_logs, read_pg_log_file)
            to determine definitively whether the container crashed, was stopped cleanly,
            or hit a disk/OOM issue. Do NOT guess or skip this escalation step.
          - If the entry shows "Kubernetes: ...": use get_pod_status to determine the
            container state. CrashLoopBackOff means PostgreSQL is repeatedly failing to
            start — config or data issue, not a simple restart. OOMKilled means the pod
            was killed by the OOM killer — a restart will likely succeed but the root
            cause (memory pressure) must be addressed. Completed/Succeeded indicates the
            process exited cleanly — check for intentional shutdown.
          - If there is NO Known Infrastructure entry for this server, or the hosting type
            cannot be determined: default to escalation. Emit ESCALATE_TO:
            pbs_sysadmin_docker_inspect. The sysadmin agent can inspect the host and
            determine whether the server is bare-metal, containerised, or K8s-managed.
            Do NOT withhold escalation because you cannot confirm the hosting type —
            unknown hosting is itself a signal that the sysadmin path is appropriate.

          Step 3 — Read the logs (Kubernetes only):
          Use get_pod_logs to retrieve the last 100–200 lines of PostgreSQL output.
          Key signals to look for:
          - "database system was shut down" followed by absence of "database system is
            ready to accept connections" — clean shutdown, safe to restart.
          - "out of memory" or "OOM" in system/kernel messages — OOM kill, safe to
            restart but monitor memory.
          - "FATAL" or "PANIC" lines — indicate a hard failure; see the config recovery
            or data recovery playbooks before attempting restart.
          - "invalid page" or "checksum failure" — do NOT restart; escalate to data
            recovery immediately.

          Step 4 — Check K8s events (Kubernetes only):
          Use get_events for the pod's namespace to see recent evictions, OOM kills,
          node pressure events, or storage mount failures. Storage mount failures
          (PVC not bound, volume unavailable) cannot be resolved by a restart.

          Step 5 — Attempt restart (only if logs show clean shutdown or OOM kill):
          Before triggering a rolling restart, assess blast radius:
          - A rolling restart of a live pod will briefly disconnect all active clients.
            If the pod is already down (Terminated/Error), there are no active connections
            to protect — restart is safe from a client perspective.
          - If the pod is still Running but unhealthy, check pg_stat_activity for sessions
            with backend_xid set (uncommitted writes) — those transactions will be rolled
            back. Confirm this is acceptable before proceeding.
          Use restart_deployment to trigger a rolling restart. Watch get_pod_status
          for the new pod to reach Running state. Then retry check_connection — if
          the database accepts connections within 60 seconds, the incident is resolved.

          Step 6 — Use baseline data if the DB does not come back:
          If the database was previously monitored, ToolResultStore history contains
          a get_server_info snapshot with data_directory, config_file, hba_file,
          log_directory, and log_filename. Retrieve this to orient deeper investigation
          without requiring a live connection.

          Common misdiagnosis: restarting a pod that is in CrashLoopBackOff due to a
          config error — the restart will fail again immediately. Always read the logs
          before restarting. If you see any FATAL or PANIC lines, use the config
          recovery playbook instead.

          Required output — write these exact lines at the end of your response,
          no markdown, no extra text on any line. The handoff signal (TRANSITION_TO
          or ESCALATE_TO) is mandatory: omitting it stalls the operator review gate:
          HYPOTHESIS_1: <primary hypothesis about failure type> | CONFIDENCE: <0.0–1.0> | EVIDENCE: "<verbatim quote from pod logs or events>"
          HYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>
          ROOT_CAUSE: HYPOTHESIS_1
          FINDINGS: hosting=<k8s|docker|bare_metal>; connection_refused_cause=<server_stopped|max_connections|pg_hba|network|unknown>;                                                                                                            failure_type=<clean_shutdown|oom_kill|config_error|data_corruption|max_connections_exceeded|pg_hba_auth_blocked|unknown>; restart_safe=<true|false>; escalate_to=<pbs_series_id | none>

── symptoms ────────────────────
  before  database is not accepting connections
          check_connection returns connection refused or timeout
          monitoring alert: database_up=0
          application errors: could not connect to server
          K8s pod in CrashLoopBackOff or Error state
          pg_isready returns 'no response'
          repeated connection timeouts (sustained >30 seconds) even after initial check
  after   database is not accepting connections
          check_connection returns connection refused or timeout
          monitoring alert: database_up=0
          application errors: could not connect to server
          application errors: sorry, too many clients already
          K8s pod in CrashLoopBackOff or Error state
          pg_isready returns 'no response'

── escalation ───────────────────�
  before  pod logs contain 'invalid page in block' or 'checksum failure'
          pod logs contain PANIC (not FATAL) — indicates unrecoverable state
          CrashLoopBackOff and logs show FATAL config errors — use config recovery playbook
          storage mount failure or PVC not bound — requires infrastructure intervention
          database does not accept connections within 5 minutes of pod restart
          get_events shows node NotReady or repeated evictions — node-level issue
          check_connection duration_ms consistently >20 seconds across multiple attempts — escalate to infrastructure for network/kernel diagnosis
          pod restart count >3 within a 5-minute window — indicates crash loop, not transient failure
          get_pod_logs missing startup success message and no FATAL/PANIC errors — possible hung initialization; check storage and network latency before restart
  after   pod logs contain 'invalid page in block' or 'checksum failure'
          pod logs contain PANIC (not FATAL) — indicates unrecoverable state
          CrashLoopBackOff and logs show FATAL config errors — use config recovery playbook
          storage mount failure or PVC not bound — requires infrastructure intervention
          database does not accept connections within 5 minutes of pod restart
          get_events shows node NotReady or repeated evictions — node-level issue
          check_connection returns max_connections or pg_hba error — server is running, restart is wrong remedy

3 field(s) changed.

To activate:  faulttest vault activate pb_2c498091 --gateway http://localhost:8080 --api-key <key>
To discard:   curl -X DELETE http://localhost:8080/api/v1/fleet/playbooks/pb_2c498091 -H 'Authorization: Bearer <key>'

── LLM Judge Review ────────────────────────────────────────────────────
time=2026-07-14T00:03:14.850-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
Verdict:            ✓  APPROVE
Guidance quality:   The new version significantly improves clarity by explicitly distinguishing between connection-refused (process stopped) and non-restart scenarios (max_connections, pg_hba auth, timeouts) in Step 1, preventing harmful     restarts; it also adds practical blast-radius assessment before restart and better handles unknown hosting types.
Escalation safety:  Escalation safety is enhanced by adding a new criterion that explicitly prevents restart when max_connections or pg_hba errors occur (which indicate the database is running), and by mandating escalation when hosting type  cannot be determined rather than proceeding with uncertain assumptions.
Reasoning:          The update eliminates dangerous misdiagnoses (restarting a healthy database with exhausted connections or auth issues) while tightening escalation criteria and improving decision flow, with only minor deletions of         connectivity-timing guidance that are offset by stronger operational safeguards.
Judge model:        claude-haiku-4-5-20251001
Verdict saved to pb_2c498091.
```

APPROVE with strong reasoning. The judge now correctly identified both the `max_connections`/`pg_hba` non-restart fix and the unknown-hosting escalation mandate as safety improvements. Now let's activate and re-run:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault activate \
      pb_2c498091 
      --gateway $HELPDESK_GATEWAY_URL 
      --api-key $HELPDESK_CLIENT_API_KEY \

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.20.0-7-g3475cfd  ·  host: <your-host>

Activated: pb_2c498091  v1.7  Database Down — Restart Triage
Series:    pbs_db_restart_triage

faulttest vault active --gateway http://localhost:8080 --api-key <key>

[boris@cassiopeia ~/cassiopeia/helpdesk]$ go run ./testing/cmd/faulttest vault active --gateway $GW --api-key $HELPDESK_CLIENT_API_KEY
Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.20.0-7-g3475cfd  ·  host: <your-host>

SERIES                              VERSION  SOURCE     UPDATED     NAME
────────────────────────────────────────────────────────────────────────────────────────────────────────────────
pbs_db_restart_triage               1.7      imported   2026-07-14  Database Down — Restart Triage
...
```

And so playbook version v1.7 is now live. Let's run the `faulttest` to generate data under the new playbook:

```
[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest run \
      --ids db-connection-refused \
      --auto-db \
      --repeat 3 \
      --agent-model claude-sonnet-4-6 \
      --judge --judge-vendor anthropic \
      --judge-model claude-haiku-4-5-20251001 \
      --judge-api-key $ANTHROPIC_API_KEY \
      --gateway $HELPDESK_GATEWAY_URL 
      --api-key $HELPDESK_CLIENT_API_KEY \
      --approval-mode force \
      --emit-and-wait \
      --gate-escalation

Tue Jul 14 00:09:53 EDT 2026
Starting temporary PostgreSQL container (postgres:16-alpine)...
Auto-DB ready: host=127.0.0.1 port=51466 dbname=faulttest user=postgres password=faulttest sslmode=disable

time=2026-07-14T00:09:57.139-04:00 level=INFO msg="auto-DB registered with gateway" server_id=faulttest-auto-51466
time=2026-07-14T00:09:57.140-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-07-14T00:09:57.140-04:00 level=INFO msg="LLM diagnosis judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: Database connection refused (db-connection-refused) — 3 runs ---

  Run 1/3
time=2026-07-14T00:09:57.140-04:00 level=INFO msg="injecting failure" id=db-connection-refused type=shell_exec mode=external conn=127.0.0.1
time=2026-07-14T00:09:57.529-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-cbd84df4\nPostgreSQL container faulttest-auto-db-cbd84df4 stopped"
time=2026-07-14T00:09:57.981-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-connection-refused series_id=pbs_db_restart_triage playbook_id=pb_2c498091 gateway=http://localhost:8080 agent-conn="host=127.0.0.1           port=51466 dbname=faulttest user=postgres password=faulttest sslmode=disable"
  Feedback submitted (triage/post_incident run_id=plr_807f0ae5)
time=2026-07-14T00:10:19.721-04:00 level=INFO msg="tearing down failure" id=db-connection-refused type=shell_exec conn=127.0.0.1
time=2026-07-14T00:10:22.843-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-cbd84df4\nPostgreSQL container faulttest-auto-db-cbd84df4 started"
  [PASS] score=100%
         [PRIMARY 95%] PostgreSQL process is not running on 127.0.0.1:51466 due to a host-level issue (container stopped, service stopped, or misconfiguration) that requires sysadmin inspection.
         [REJECTED 5%] Port 51466 is blocked by a firewall or the connection string specifies the wrong host/port. — The target connection string was provided explicitly for this exact host:port, making misconfiguration less likely than an   actual service outage.

  Run 2/3
time=2026-07-14T00:10:22.843-04:00 level=INFO msg="injecting failure" id=db-connection-refused type=shell_exec mode=external conn=127.0.0.1
time=2026-07-14T00:10:23.045-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-cbd84df4\nPostgreSQL container faulttest-auto-db-cbd84df4 stopped"
time=2026-07-14T00:10:23.371-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-connection-refused series_id=pbs_db_restart_triage playbook_id=pb_2c498091 gateway=http://localhost:8080 agent-conn="host=127.0.0.1           port=51466 dbname=faulttest user=postgres password=faulttest sslmode=disable"
  Feedback submitted (triage/post_incident run_id=plr_61b1aaae)
time=2026-07-14T00:10:37.194-04:00 level=INFO msg="tearing down failure" id=db-connection-refused type=shell_exec conn=127.0.0.1
time=2026-07-14T00:10:40.365-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-cbd84df4\nPostgreSQL container faulttest-auto-db-cbd84df4 started"
  [PASS] score=100%
         [PRIMARY 100%] PostgreSQL process is not running on 127.0.0.1:51466 due to host/networking/container infrastructure failure
         [REJECTED 0%] The connection string is incorrect and the server is running elsewhere — The connection string was provided verbatim by the user as the authoritative target; no basis to assume it is wrong.

  Run 3/3
time=2026-07-14T00:10:40.366-04:00 level=INFO msg="injecting failure" id=db-connection-refused type=shell_exec mode=external conn=127.0.0.1
time=2026-07-14T00:10:40.567-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-cbd84df4\nPostgreSQL container faulttest-auto-db-cbd84df4 stopped"
time=2026-07-14T00:10:40.988-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-connection-refused series_id=pbs_db_restart_triage playbook_id=pb_2c498091 gateway=http://localhost:8080 agent-conn="host=127.0.0.1           port=51466 dbname=faulttest user=postgres password=faulttest sslmode=disable"
  Feedback submitted (triage/post_incident run_id=plr_936512c6)
time=2026-07-14T00:10:56.307-04:00 level=INFO msg="tearing down failure" id=db-connection-refused type=shell_exec conn=127.0.0.1
time=2026-07-14T00:10:59.431-04:00 level=INFO msg="shell_exec completed" output="faulttest-auto-db-cbd84df4\nPostgreSQL container faulttest-auto-db-cbd84df4 started"
  [PASS] score=86%
         [PRIMARY 95%] PostgreSQL process on host=127.0.0.1 port=51466 is not running; the root cause requires host-level inspection to determine whether it crashed, was stopped intentionally, or is blocked by a firewall
         [REJECTED 5%] Firewall or network misconfiguration is blocking TCP connections to port 51466 on 127.0.0.1 — localhost loopback (127.0.0.1) typically does not have firewall rules; connection refused specifically indicates no          listening process rather than a blocked port

  Stability report (3 runs):
    Pass rate:    3/3 (100%)
    Confidence:   min=95% max=100% range=5pp mean=97%  (H1, passing runs only)
    Verdict:      STABLE
  ────────────────────────────────────────────────────────────────
time=2026-07-14T00:11:00.088-04:00 level=INFO msg="fault stability cert posted" fault_id=db-connection-refused verdict=STABLE n_runs=3
=== Fault Test Report: c67f7fc0 ===

[PASS] Database connection refused (db-connection-refused) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Reasoning: "Agent correctly identified connection refused as the root cause, properly recognized that the target server is not in known infrastructure, appropriately escalated to pbs_sysadmin_docker_inspect per expert guidance, and    provided sound reasoning for why escalation (rather than direct troubleshooting) is the correct action."
[PASS] Database connection refused (db-connection-refused) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Reasoning: "Agent correctly identified connection refused as the root cause, properly determined the server is not in Known Infrastructure, and appropriately escalated to pbs_sysadmin_docker_inspect per Expert Guidance, addressing all key points in the expected diagnosis."
[PASS] Database connection refused (db-connection-refused) - score: 86% [judge: 67%]
       Keywords: 100% | Tools: 100% | Judge: 67%
       Reasoning: "Agent correctly identified the connection refused symptom and root cause (PostgreSQL not running/unreachable), but escalated to the general sysadmin agent instead of the specific ESCALATE_TO: pbs_sysadmin_docker_inspect    mechanism mentioned in EXPECTED_DIAGNOSIS, and did not definitively recognize this as a Docker-hosted container requiring container-specific inspection."

--- Summary ---
Total: 3 | Passed: 3 | Failed: 0 | Rate: 100%
  database: 3/3 (100%)

LLM judge scored diagnosis for 3 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-c67f7fc0.json
--- BEGIN FAULTTEST REPORT JSON ---
...
--- END FAULTTEST REPORT JSON ---
Evaluation scores posted to auditd: 3/3 runs

real    1m7.579s
user    0m0.381s
sys     0m0.476s
```

That's a solid 3/3 PASS, STABLE (5pp range), judge 100%/100%/67%. The 67% on run 3 is the agent escalating correctly in prose, but not emitting the exact `ESCALATE_TO: pbs_sysadmin_docker_inspect` token in its structured block, which is a real signal, not noise. Now close the loop with vault judge-accuracy:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault \
   judge-accuracy pbs_db_restart_triage 
   --gateway $HELPDESK_GATEWAY_URL 
   --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.20.0-7-g3475cfd  ·  host: <your-host>

SERIES                            VERSION     JUDGE VERDICT         RUNS    SUCCESS%   JUDGE MODEL
───────────────────────────────────────────────────────────────────────────────────────────────────────────
pbs_db_restart_triage             1.7         APPROVE               3       0%         claude-haiku-4-5-20251001
pbs_db_restart_triage             1.6         APPROVE               3       0%         claude-haiku-4-5-20251001

JUDGE VERDICT is the prediction recorded by `vault diff`.
SUCCESS% is the actual outcome after runs on this version.
```

Both versions show 0% succes. That's misleading at best, but is the artifact of `--repeat` mode where escalation chains never complete (sysadmin never runs, gateway keeps them "pending at escalation gate"). The actual quality signal lives in the cert and judge scores:

```
  ┌─────────────────────┬───────────────────────┬───────────────────┐
  │       Version       │         Cert          │   Judge scores    │
  ├─────────────────────┼───────────────────────┼───────────────────┤
  │ v1.6 (AI-generated) │ UNSTABLE (40pp range) │ 33% / 67% / 100%  │
  ├─────────────────────┼───────────────────────┼───────────────────┤
  │ v1.7 (manual)       │ STABLE (5pp range)    │ 100% / 100% / 67% │
  └─────────────────────┴───────────────────────┴───────────────────┘
```

The 0% SUCCESS% for escalation-heavy faults is a metric gap (it'll only flip to a real number when `--repeat` is dropped and the full sysadmin chain actually runs... which is unacceptable). For single-step playbooks like `pbs_vacuum_triage`, APPROVE → 100% works correctly.

So how to we solve this metric gap here for escalation-heavy faults where the 0% SUCCESS% looks very misleading because APPROVE with 0% SUCCESS% looks like the prediction was wrong? In reality 3/3 runs correctly escalated and got faulttest PASS.  The gap is that the current metric conflates "incident fully resolved end-to-end" with "triage step did its job correctly." The real question is what SUCCESS% should mean for a triage playbook that's supposed to escalate. 

Fix: show RESOLVED% / ESCALATED% side by side, e.g. 0% / 100%, which is more honest about what actually happened (but comes at the expense of the wider output).

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault \
   judge-accuracy pbs_db_restart_triage 
   --gateway $HELPDESK_GATEWAY_URL 
   --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.20.0-7-g3475cfd  ·  host: <your-host>

SERIES                            VERSION     JUDGE VERDICT         RUNS    RESOLVED%   ESCALATED%   JUDGE MODEL
─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
pbs_db_restart_triage             1.7         APPROVE               3       0%          100%         claude-haiku-4-5-20251001
pbs_db_restart_triage             1.6         APPROVE               3       0%          66%          claude-haiku-4-5-20251001

JUDGE VERDICT is the prediction recorded by `vault diff`.
RESOLVED% = fraction of runs resolved (or transitioned to remediation) by this version.
ESCALATED% = fraction of runs that correctly escalated to a specialist agent.
```

That's the real signal right here:

- v1.6 APPROVE → 66% because one run had `gate_pending` with empty `escalated_to` and so the agent didn't escalate at all on that run (exactly what the UNSTABLE cert was measuring).
- v1.7 APPROVE → 100%, consistent with STABLE cert. The two metrics corroborate each other perfectly.


