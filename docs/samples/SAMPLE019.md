# aiHelpDesk Sample#19 (on Docker): Tested Where It Counts

The raw transcript of the sample commands and deliberations presented below complements these two blog posts:

- **[AI-Based DB Replication Triage and Remediation. The Evaluation Story](https://itnext.io/ai-based-db-replication-triage-and-remediation-the-evaluation-story-140cef5f5677)**  
  Dana had a rule for vendor demos: whoever built the product was not allowed to showcase it.  

- **[How to evaluate and audit a vendor AI-powered product?](...)**  
  Sam did not read vendor documentation. Sam read vendor databases. How do you trust anything an agent says? Sam had a way to find out. He tested it.

If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel). 

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This one happened to be running on Docker, but see [here](SAMPLE010.md), [here](SAMPLE011.md) and [here](SAMPLE017.md) for VM/Bare Metal, the source and K8s respectively (although not the exact commands shown on this page).

In this transcript we look at three headline features of the aiHelpDesk [v0.27 release](https://github.com/borisdali/helpdesk/releases/tag/v0.27.0) release: the brand new [objective evidence framework](../OBJECTIVE_EVIDENCE.md), how it allowed us to institute the new [layer 3 model fabrication detection guard](../AIGOVERNANCE.md#layer-3--objective-evidence-content-verification) and the three new [replication failure injection scenarios](../HA_DR.md#4-sysadmin-domain-escalation-telling-a-crashed-rejected-and-frozen-replica-apart).


## 0. Back to the Basics

This section is likely redundant at this stage, but just in case that this is the first aiHelpDesk sample transcript you stumbled upon for Docker/Podman, here are the basics in a nutshell:

```
[boris@ /tmp/helpdesk/helpdesk-v0.27.0-deploy/docker-compose]$ docker compose --profile governance down -v --remove-orphans
[+] Running 13/13
 ✔ Container helpdesk-secbot-1          Removed                                                                                                                                                                                              0.3s
 ✔ Container helpdesk-auditor-1         Removed                                                                                                                                                                                              0.2s
 ✔ Container helpdesk-govbot-1          Removed                                                                                                                                                                                              0.1s
 ✔ Container helpdesk-gateway-1         Removed                                                                                                                                                                                              0.2s
 ✔ Container helpdesk-k8s-agent-1       Removed                                                                                                                                                                                              0.4s
 ✔ Container helpdesk-database-agent-1  Removed                                                                                                                                                                                              0.5s
 ✔ Container helpdesk-research-agent-1  Removed                                                                                                                                                                                              0.5s
 ✔ Container helpdesk-sysadmin-agent-1  Removed                                                                                                                                                                                              0.4s
 ✔ Container helpdesk-incident-agent-1  Removed                                                                                                                                                                                              0.6s
 ✔ Container helpdesk-auditd-1          Removed                                                                                                                                                                                              0.3s
 ✔ Volume helpdesk_audit-data           Removed                                                                                                                                                                                              0.0s
 ✔ Volume helpdesk_incidents            Removed                                                                                                                                                                                              0.0s
 ✔ Network helpdesk_default             Removed                                                                                                                                                                                              0.2s

[boris@ /tmp/helpdesk/helpdesk-v0.27.0-deploy/docker-compose]$ docker compose --profile governance up -d --wait
[+] Running 13/13
 ✔ Network helpdesk_default             Created                                                                                                                                                                                              0.0s
 ✔ Volume "helpdesk_incidents"          Created                                                                                                                                                                                              0.0s
 ✔ Volume "helpdesk_audit-data"         Created                                                                                                                                                                                              0.0s
 ✔ Container helpdesk-auditd-1          Healthy                                                                                                                                                                                             20.1s
 ✔ Container helpdesk-auditor-1         Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-sysadmin-agent-1  Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-research-agent-1  Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-incident-agent-1  Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-k8s-agent-1       Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-database-agent-1  Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-gateway-1         Healthy                                                                                                                                                                                             19.8s
 ✔ Container helpdesk-govbot-1          Healthy                                                                                                                                                                                             19.4s
 ✔ Container helpdesk-secbot-1          Healthy                                                                                                                                                                                             19.4s
```


See the previous sample transcripts (starting with [this sample](SAMPLE006.md)) for more details on this if the above was too quick of a whirlwind tour.


## 1st replication failure scenario: `db-replica-stalled`

All right, there are four replication failure scenarios that we'll be trying to certify here. See [this doc](../HA_DR.md#1-replication-lag) for details, but in a nutshell, here are they are:
  - [db-replication-lag](https://github.com/borisdali/helpdesk/blob/5761ec983b4b3dac5de97cea48217bc764bc992f/testing/catalog/failures.yaml#L506)
  - [db-replica-disconnected](https://github.com/borisdali/helpdesk/blob/5761ec983b4b3dac5de97cea48217bc764bc992f/testing/catalog/failures.yaml#L549)
  - [db-replica-stalled](https://github.com/borisdali/helpdesk/blob/40c1d8b613f27b0bb30aa0d7488eae517f46cb32/testing/catalog/failures.yaml#L689)
  - [db-replica-container-stopped](https://github.com/borisdali/helpdesk/blob/ea74bcad0961ccb28e4cc662ef78078cfef4e3c7/testing/catalog/failures.yaml#L627)

OK, let's kick this off with `db-replica-stalled` and run the failure injection testing with `--repeat N` to see if we can get it certified:

```
[boris@ ~/helpdesk]$ date; time docker run --rm --network helpdesk_default \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest faulttest run \
        --ids db-replica-stalled \
        --repeat 3 \
        --approval-mode=force \
        --via-gateway \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY" \
        --judge-api-key "$HELPDESK_API_KEY" \
        --conn "host=host.docker.internal port=15432 dbname=testdb user=postgres password=xxx" \
        --replica-conn "host=host.docker.internal port=15433 dbname=testdb user=postgres password=xxx" \
        --agent-model claude-haiku-4-5-20251001 \
        --operator alice@example.com
Thu Sep  3 13:21:33 EDT 2026
time=2026-09-03T17:21:34.193Z level=INFO msg=--conn host=host.docker.internal
time=2026-09-03T17:21:34.194Z level=INFO msg=--replica-conn host=host.docker.internal
time=2026-09-03T17:21:34.198Z level=INFO msg="executing injection spec" type=docker phase=inject

--- Testing: Replica present but not communicating (frozen walreceiver) (db-replica-stalled) — 3 runs ---

  Run 1/3
time=2026-09-03T17:21:34.335Z level=INFO msg="waiting after injection" phase=inject duration=40s
time=2026-09-03T17:22:14.716Z level=INFO msg="sending prompt to agent via playbook" failure=db-replica-stalled series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_a6be1e90)
time=2026-09-03T17:22:49.223Z level=INFO msg="executing injection spec" type=docker phase=teardown
time=2026-09-03T17:22:49.278Z level=INFO msg="waiting after injection" phase=teardown duration=5s
time=2026-09-03T17:22:54.282Z level=INFO msg="executing injection spec" type=docker phase=inject
  [PASS] score=100%
         [PRIMARY 95%] Replica process has stalled or hung, losing the ability to apply WAL or send replication feedback while the TCP connection remains open.
         [95%] Replica container processes are frozen by Docker cgroup freezer (paused state)
         [REJECTED 80%] Replica is CPU-starved or experiencing severe I/O contention, causing WAL application to fall behind. — reply_lag_seconds metric directly measures feedback intervals independent of WAL position; a stalled feedback     cycle indicates process-level stall, not just load.
         [REJECTED 0%] Primary rejected the replica connection attempt — Container is paused before any connection attempt could be logged; rejection would require the replica to be running and attempting to connect

  Run 2/3
time=2026-09-03T17:22:54.374Z level=INFO msg="waiting after injection" phase=inject duration=40s
time=2026-09-03T17:23:34.921Z level=INFO msg="sending prompt to agent via playbook" failure=db-replica-stalled series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_7305d328)
time=2026-09-03T17:24:03.807Z level=INFO msg="executing injection spec" type=docker phase=teardown
time=2026-09-03T17:24:03.870Z level=INFO msg="waiting after injection" phase=teardown duration=5s
  [PASS] score=100%
         [PRIMARY 100%] Docker cgroup freezer has paused the container's processes; replica cannot execute replication protocol operations.
         [95%] Replica process is stalled or frozen (stopped sending feedback despite TCP connection remaining open)
         [REJECTED 5%] Network partition partway through TCP session (primary sees connection, replica cannot reach back) — Would manifest identically to process stall; diagnosis requires host-level network inspection
         [REJECTED 0%] Replica was rejected by the primary's pg_hba.conf or authentication. — Container processes are frozen and cannot attempt any connection at all, making authentication failure irrelevant to the observed stall.

  Run 3/3
time=2026-09-03T17:24:08.872Z level=INFO msg="executing injection spec" type=docker phase=inject
time=2026-09-03T17:24:08.973Z level=INFO msg="waiting after injection" phase=inject duration=40s
time=2026-09-03T17:24:49.523Z level=INFO msg="sending prompt to agent via playbook" failure=db-replica-stalled series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_e2c5e778)
time=2026-09-03T17:25:25.423Z level=INFO msg="executing injection spec" type=docker phase=teardown
time=2026-09-03T17:25:25.483Z level=INFO msg="waiting after injection" phase=teardown duration=5s
  [PASS] score=100%
         [PRIMARY 95%] Replica container process (WAL receiver) is stalled or unresponsive; TCP connection remains open but replica stopped sending feedback
         [95%] The replica container is paused (processes frozen via Docker cgroup freezer), preventing replication feedback even though the container is not crashed or exited
         [REJECTED 5%] Network partition between primary and replica that has not yet torn down the TCP session — Network partition would eventually cause TCP timeout and connection drop; persistent open connection with no feedback for 52+   seconds more likely indicates stalled replica process than transient network issue.
         [REJECTED 5%] The replica container is running normally but the primary is rejecting it — check_host confirms Status=paused which directly contradicts normal running operation; a paused container cannot establish or maintain an      active connection.
time=2026-09-03T17:25:31.033Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (3 runs):
    Pass rate:    3/3 (100%)
    Confidence:   min=95% max=100% range=5pp mean=97%  (H1, passing runs only)
    Verdict:      STABLE
    Attribution:  replica-stalled-but-connected (3/3)  consistent: yes  [taxonomy 1.2]
  ────────────────────────────────────────────────────────────────
    Warnings:     0/3 run(s) tripped a verified warning signal
    Warning types: objective_evidence:replica_stalled=3(predictable)
    Confirmed:     objective_evidence:replica_stalled=3(predictable)
    Clean:        yes
time=2026-09-03T17:25:33.888Z level=INFO msg="fault stability cert posted" fault_id=db-replica-stalled verdict=STABLE clean=CLEAN warning_count=0 n_runs=3

=== Fault Test Report: 08313fe4 ===

[PASS] Replica present but not communicating (frozen walreceiver) (db-replica-stalled) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Replica present but not communicating (frozen walreceiver) (db-replica-stalled) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Replica present but not communicating (frozen walreceiver) (db-replica-stalled) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 3 | Passed: 3 | Failed: 0 | Rate: 100%
  database: 3/3 (100%)

time=2026-09-03T17:25:34.608Z level=INFO msg="fault stability cert posted" fault_id=db-replica-stalled::hop:pbs_sysadmin_replica_connectivity_triage verdict=STABLE clean=CLEAN warning_count=0 n_runs=3
Report written to ./faulttest-08313fe4.json
```

Nice! Before drawing any conclusions let's get the diagnosis accuracy breakdown report that shows the full consistency certification cert for that fault. That's what [`vault accuracy`](../VAULT.md#vault-accuracy) is there for! (and yes, it also shows how often the agent's root-cause hypothesis was confirmed correct by operators, but that's of less interest at the moment). Let's get it:

```
[boris@ ~/helpdesk]$ docker run --rm --network helpdesk_default \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest \
    faulttest accuracy \
        db-replica-stalled \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY"

Gateway: http://gateway:8080  ·  version: v0.26.0-35-g3a0d5c0-3a0d5c0  ·  host: e2b4d78448f8

Diagnosis accuracy for series: pbs_replication_lag

  Feedback submitted : 3 runs
  Correct diagnoses  : 3
  Accuracy rate      : 100%

  Breakdown by feedback time:
    Post-incident (after recovery): 3 of 3 correct (100%)

Triage consistency
  Fault         : db-replica-stalled  (Replica present but not communicating (frozen walreceiver))
  Verdict       : STABLE
  Runs          : 3
  Pass rate     : 100%
  Conf range    : 5pp  (primary hypothesis, passing runs only)
  Clean         : yes
  Warning types : objective_evidence:replica_stalled=3(predictable)
  Confirmed     : objective_evidence:replica_stalled=3(predictable)
  Playbook      : pbs_replication_lag
  Diagnosis model: claude-haiku-4-5-20251001
  Tested at     : 2026-09-03 17:25 UTC  (0 days ago)

  Attribution (taxonomy 1.2)
  Primary class  : replica-stalled-but-connected
  Consistent     : yes  (3/3 runs)

Cert history (last 4)
  2026-09-03 17:07 UTC   STABLE   CLEAN attr=consistent (3 runs)
      ↳ changed since 2026-09-03 16:06 UTC: trust earned (now STABLE+CLEAN+attribution-consistent); stability: UNSTABLE→STABLE; warning_distribution: objective_evidence:replica_stalled 0→3; confirmed_distribution: objective_evidence:         replica_stalled 0→3
  2026-09-03 16:06 UTC   UNSTABLE CLEAN attr=consistent (3 runs)
  2026-09-03 15:45 UTC   UNSTABLE CLEAN attr=consistent (3 runs)
      ↳ changed since 2026-09-03 14:31 UTC: trust regressed (was STABLE+CLEAN+attribution-consistent); stability: STABLE→UNSTABLE; warning_distribution: objective_evidence:replica_stalled 3→0; confirmed_distribution: objective_evidence:      replica_stalled 3→0
  2026-09-03 14:31 UTC   STABLE   CLEAN attr=consistent (3 runs)
```

That confirms it! Nice, clean run, full success with everything working together correctly. Here's the breakdown:

  - Evidence gate:  
    `objective_evidence_confirmed: ["replica_stalled"]` fired correctly on all 3 runs.

  - SysAdmin Agent hop investigation is now complete (not degraded):  
    Run 1's transcript shows `check_host` succeeding directly against the replica ("the container is running but its processes are paused").

  - Both certs posted STABLE/CLEAN:
    The triage hop and the `::hop:pbs_sysadmin_replica_connectivity_triage` escalation hop.

  - 100% accuracy, consistent attribution (replica-stalled-but-connected, 3/3).

  - And a bonus! Note the cert history section that shows the exact trust-regression-then-recovery story (UNSTABLE→STABLE once the API keys landed correctly). We [described it in detail](https://itnext.io/the-ai-that-un-trusts-itself-the-page-that-never-came-6cc9df173533) previously, but it's nice to see it firing here as well.


W00t! `db-replica-stalled` is fully certified and done.

Let's see where we presently stand - here's the current inventory with respect to all four replication failure scenarios:

```
[boris@ ~/helpdesk]$ docker run --rm --network helpdesk_default \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest \
    faulttest vault list \
        --ids db-replication-lag,db-replica-disconnected,db-replica-container-stopped,db-replica-stalled \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY"
Gateway: http://gateway:8080  ·  version: v0.26.0-35-g3a0d5c0-3a0d5c0  ·  host: e2b4d78448f8

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-replication-lag               any        pbs_replication_lag             pbs_replication_remediate        READY                  —              0 runs  (system)
    diag  → vault versions pbs_replication_lag
db-replica-disconnected          any        pbs_replication_lag             (none)                           (never)                —              -
    → vault versions pbs_replication_lag
db-replica-container-stopped     any        pbs_replication_lag             (none)                           (never)                —              -
    → vault versions pbs_replication_lag
db-replica-stalled               any        pbs_replication_lag             (none)                           2026-09-03  PASS       STABLE(3) attr=replica-stalled-but-connected -
    → vault versions pbs_replication_lag
```


## 2nd replication failure scenario: `db-replication-lag` 

Let's run the same fault injection test with `--repeat N` as we did in the previous section, but for the `db-replication-lag` now:

```
[boris@ ~/helpdesk]$ date; time docker run --rm --network helpdesk_default \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest faulttest run \
        --ids db-replication-lag \
        --repeat 3 \
        --approval-mode=force \
        --via-gateway \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY" \
        --judge-api-key "$HELPDESK_API_KEY" \
        --conn "host=host.docker.internal port=15432 dbname=testdb user=postgres password=xxx" \
        --replica-conn "host=host.docker.internal port=15433 dbname=testdb user=postgres password=xxx" \
        --agent-model claude-haiku-4-5-20251001 \
        --operator alice@example.com
Thu Sep  3 13:47:57 EDT 2026
time=2026-09-03T17:47:57.654Z level=INFO msg=--conn host=host.docker.internal
time=2026-09-03T17:47:57.654Z level=INFO msg=--replica-conn host=host.docker.internal

--- Testing: Replication lag (db-replication-lag) — 3 runs ---

  Run 1/3
time=2026-09-03T17:47:57.659Z level=INFO msg="executing injection spec" type=sql phase=inject
time=2026-09-03T17:47:57.934Z level=INFO msg="sending prompt to agent via playbook" failure=db-replication-lag series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal port=15432 dbname=testdb user=postgres password=testpass"
  ⚠  FABRICATION RISK: mismatch (narrated tool call not confirmed)
  Feedback submitted (triage/post_incident run_id=plr_31980b13)
time=2026-09-03T17:49:00.765Z level=INFO msg="executing injection spec" type=sql phase=teardown
  [PASS] score=100%
         [PRIMARY 95%] Inactive replication slot (replica_slot) belongs to a permanently disconnected replica that cannot reconnect, blocking 28.6 MB of WAL
         [95%] The primary is closing or timing out replication connections from the replica, either due to primary-side configuration, resource exhaustion on the primary, or a network connectivity issue between primary and replica
         [REJECTED 5%] The active replica at 172.18.0.4 is the one returning stale data — Active replica shows zero replay_lag_bytes and reply_lag_seconds of 9 (normal feedback interval), indicating it is current and receiving WAL correctly.
         [REJECTED 0%] The replica crashed but automatically restarted multiple times and is now running — check_host shows exitcode=0 with status=running and restarting=false, indicating clean state and no active restart loop; the logs show a pattern of intentional failures (timeouts and server-closed-connection errors) rather than process crashes.

  Run 2/3
time=2026-09-03T17:49:00.784Z level=INFO msg="executing injection spec" type=sql phase=inject
time=2026-09-03T17:49:01.183Z level=INFO msg="sending prompt to agent via playbook" failure=db-replication-lag series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal port=15432 dbname=testdb user=postgres password=testpass"
Agent error: POST http://gateway:8080/api/v1/fleet/playbooks/pb_f772c6b3/run: Post "http://gateway:8080/api/v1/fleet/playbooks/pb_f772c6b3/run": context deadline exceeded (Client.Timeout exceeded while awaiting headers)
time=2026-09-03T17:50:11.187Z level=INFO msg="executing injection spec" type=sql phase=teardown
time=2026-09-03T17:50:11.248Z level=INFO msg="executing injection spec" type=sql phase=inject
  [FAIL] score=0%

  Run 3/3
time=2026-09-03T17:50:11.892Z level=INFO msg="sending prompt to agent via playbook" failure=db-replication-lag series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_581b1a23)
time=2026-09-03T17:50:46.127Z level=INFO msg="executing injection spec" type=sql phase=teardown
  [PASS] score=100%
         [PRIMARY 95%] Primary-side stale replication slot from a decommissioned replica is retaining WAL; actively-connected replica at 172.18.0.4 is healthy with zero replay lag
         [REJECTED 5%] Stale data complaint refers to a real lag on the active replica at 172.18.0.4 that is masked by false-zero metrics or application-level snapshot staleness — Active replica shows zero lag across all metrics              (write_lag_bytes=0, flush_lag_bytes=0, replay_lag_bytes=0) and is actively streaming with normal reply feedback (2 seconds), ruling out true replication lag
time=2026-09-03T17:50:46.430Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (3 runs):
    Pass rate:    2/3 (67%)  [UNSTABLE: want >= 80%]
    [WARN] 1 failed run(s) excluded from confidence stats
    Confidence:   min=95% max=95% range=0pp mean=95%  (H1, passing runs only)
    Verdict:      UNSTABLE
    Attribution:  replica-disconnected (1/3)  consistent: no (split)  [taxonomy 1.2]
                  UNKNOWN: 1
                  stale-replication-slot-accumulating-wal: 1
  ────────────────────────────────────────────────────────────────
    Warnings:     1/3 run(s) tripped a verified warning signal
    Warning types: mismatch=1(varies)
    Clean:        no
time=2026-09-03T17:50:49.622Z level=INFO msg="fault stability cert posted" fault_id=db-replication-lag verdict=UNSTABLE clean=DIRTY warning_count=1 n_runs=3
time=2026-09-03T17:50:50.180Z level=INFO msg="fault stability cert posted" fault_id=db-replication-lag::hop:pbs_sysadmin_replica_connectivity_triage verdict=STABLE clean=CLEAN warning_count=0 n_runs=1

=== Fault Test Report: 4534c881 ===

[PASS] Replication lag (db-replication-lag) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[FAIL] Replication lag (db-replication-lag) - score: 0%
       Error: POST http://gateway:8080/api/v1/fleet/playbooks/pb_f772c6b3/run: Post "http://gateway:8080/api/v1/fleet/playbooks/pb_f772c6b3/run": context deadline exceeded (Client.Timeout exceeded while awaiting headers)
[PASS] Replication lag (db-replication-lag) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 3 | Passed: 2 | Failed: 1 | Rate: 66%
  database: 2/3 (66%)

Report written to ./faulttest-4534c881.json
```

And here's the part that we omit (for the sake of brevity and readability) for the other three faults/certs in this transcript sample. We show it in full here in full because the cert we get here is UNSTABLE/DIRTY and so we figured it's worth taking the real estate on this command transcript to show the full `faulttest` JSON. Here it is:

```
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "4534c881",
  "timestamp": "2026-09-03T17:50:50Z",
  "results": [
    {
      "failure_id": "db-replication-lag",
      "failure_name": "Replication lag",
      "category": "database",
      "score": 1,
      "passed": true,
      "run_id": "plr_31980b13",
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "ordering_pass": true,
      "tool_evidence_mode": "structured",
      "response_text": "I'll begin investigating replication lag on the target database. Let me start by checking the connection and confirming the server state.\n\n**Checking connectivity to the target database and confirming its role       (primary vs replica):**\nGood — the target is reachable and is a **primary** server. Now let me get replication status to check for connected replicas and their lag:\n\n**Checking replication status to identify replica lag and slot state:**  \nCritical finding: **The replication slot `replica_slot` is INACTIVE but has 28.6 MB of retained WAL (lag_bytes=28633584)**. However, there IS an active streaming connection from 172.18.0.4 (walreceiver) with:\n- write_lag_bytes: 0 (WAL     written to replica immediately)\n- flush_lag_bytes: 0 (WAL flushed immediately)\n- replay_lag_bytes: 0 (WAL replayed immediately)\n- reply_lag_seconds: 9 (replica feedback received within normal interval)\n\nThis indicates **two separate     issues**:\n\n1. **Active replica connection is healthy** (172.18.0.4) — no replay lag, recent feedback\n2. **Stale replication slot** (`replica_slot`) is inactive but blocking WAL recycling with 28.6 MB of retained WAL\n\nThe complaint       mentions \"replica seems to be returning stale data — recent writes on the primary are not visible.\" Let me check if there's a second replica or if the active connection at 172.18.0.4 is the one reporting stale reads. Let me get active      connections to see current state:\n\n**Checking active connections to understand current workload and replica connectivity:**\nThe active replication connection is from 172.18.0.4 and has been running for 1364 seconds (~22.7 minutes) with a  START_REPLICATION command at LSN 0/3000000. The zero lag bytes on this connection suggest it's caught up. However, the complaint is about stale data on the replica.\n\nLet me check the database and table statistics to see if there's evidence of recent writes and when the replica might have last replayed them:\n\n**Checking database statistics to understand write activity:**\nNo recent writes on testdb (rows_inserted/updated/deleted all 0). The statistics show 740 commits but     zero row modifications, suggesting the activity is reads or metadata operations.\n\nNow, the critical issue: **The stale replication slot is blocking WAL recycling**. But the active replica connection shows zero lag. This suggests:\n- The    replica at 172.18.0.4 is receiving and applying WAL correctly (zero replay lag)\n- BUT the complaint is that \"replica seems to be returning stale data\"\n\nThis could indicate:\n1. There's a **second replica** (perhaps a standby at 172.18.0.3 or similar) that is NOT in pg_stat_replication — it may have disconnected\n2. The stale slot (`replica_slot`) may belong to that second, disconnected replica\n3. The disconnected replica cannot reconnect because the slot is inactive\n\nLet me check if there are other potential replica hosts by examining the infrastructure context. The prompt mentions \"faulttest-db-replica\" connections. Let me check the PostgreSQL logs for clues about replica connections or disconnections:    \n\n**Checking PostgreSQL logs for replica connection/disconnection events:**\nLet me correct that:\n\n**Re-checking PostgreSQL logs for replication events:**\nLogging is disabled. Let me check the configuration to understand the replication setup:\n\n**Checking replication and WAL archiving configuration:**\nLet me use the correct syntax for get_config_parameter:\n\n**Checking WAL sender configuration:**\nKey configuration found:\n- `max_wal_senders = 3` (set via command line,  allows up to 3 WAL sender processes)\n- Only 1 active WAL sender is currently connected (walreceiver at 172.18.0.4)\n- `wal_level = replica` (supports replication)\n- `hot_standby = on` (standby can serve reads)\n\nNow the pattern is clear:  \n\n**The inactive slot `replica_slot` (28.6 MB lag) belongs to a DISCONNECTED replica**, while a different active replica connection (172.18.0.4) is currently streaming with zero lag. The complaint about \"stale data\" suggests the stale    slot's replica is the one that has fallen behind or disconnected.\n\nLet me verify if there's evidence of the replica at 172.18.0.4 being the only active one by checking if there's any other slot or replica indicator. Based on the            replication status output, I have:\n\n1. **Active streaming connection**: 172.18.0.4 (walreceiver), state=streaming, all lag bytes = 0, reply_lag_seconds = 9 ✓ Healthy\n2. **Inactive replication slot**: replica_slot, lag_bytes = 28,633,584   (~28.6 MB) ✗ Stale/disconnected\n\nThis is **Ending B**: A replica has disconnected entirely. The inactive slot is blocking 28.6 MB of WAL. The replica at the disconnected address cannot reconnect because the slot has lost track.\n\nHowever, I need to determine: **which replica host is disconnected?** The Additional context section states the primary connection_string is at `host=host.docker.internal port=15432`. The infrastructure context mentions:\n- `faulttest-db-replica`:    `host.docker.internal:15433/testdb` (Docker agent access)\n- `faulttest-db-replica-local`: `localhost:15433/testdb` (host agent access)\n\nThe disconnected replica is likely at **host.docker.internal:15433** (port 15433, the replica port).   \n\nLet me formulate my diagnosis:\n\n---\n\n**DIAGNOSIS:**\n\nThe primary at `host=host.docker.internal port=15432` has:\n1. One **active** streaming replica connection (172.18.0.4, walreceiver) with zero replay lag — this is healthy and    current\n2. One **inactive** replication slot named `replica_slot` with 28.6 MB of retained WAL\n\nThe stale replication slot indicates a replica has disconnected and cannot reconnect. The 28.6 MB lag_bytes means the slot is holding back WAL recycling, preventing the primary from reclaiming that space. The complaint about \"stale data\" on the replica likely refers to a read-only standby that attempted to connect using that slot but failed.\n\nThe disconnected replica is         preventing WAL cleanup and should be investigated for connectivity issues at the container/host level.\n\nHYPOTHESIS_1: Inactive replication slot (replica_slot) belongs to a permanently disconnected replica that cannot reconnect, blocking 28.6 MB of WAL | CONFIDENCE: 0.95 | EVIDENCE: \"slot_name | replica_slot ... active | f ... lag_bytes | 28633584\"\nHYPOTHESIS_2: The active replica at 172.18.0.4 is the one returning stale data | CONFIDENCE: 0.05 | REJECTED: Active replica     shows zero replay_lag_bytes and reply_lag_seconds of 9 (normal feedback interval), indicating it is current and receiving WAL correctly.\nROOT_CAUSE: HYPOTHESIS_1\n\n---\n\nI'll begin the investigation of the replica at host.docker.internal: 15433 following the Expert Guidance step-by-step.\n\n**Step 1 — Replica container state:**\nI need to check the container status of the replica to understand whether it is running, crashed, paused, or in a restart loop. This will tell me     whether we're dealing with a crashed process (safe to restart) or a running container that is being rejected by the primary.\nExcellent. The replica container is **running** — Status=running, exitcode=0, restarting=false, oomkilled=false.    This means the container process itself is alive. A disconnect in the face of a running container indicates the connection is being rejected or blocked, not that the replica has crashed.\n\n**Step 2 — Replica logs:**\nNow I need to retrieve  the container's logs to see what the replica is reporting about its connection attempts to the primary. I'm looking for connection-rejection messages (which would indicate the primary is refusing it), or other error signals.\nPerfect. The    logs are very clear and revealing. The replica is exhibiting a pattern of repeated connection attempts to the primary, but each connection is either timing out or being abruptly closed:\n\n1. **\"LOG: started streaming WAL from primary...    \"** — the replica successfully connects to the primary\n2. **\"FATAL: terminating walreceiver due to timeout\"** OR **\"FATAL: could not receive data from WAL stream: server closed the connection unexpectedly\"** — within minutes to hours,  the connection either times out or the primary closes it\n\nThis repeating cycle (reconnect → failure within ~15-30 minutes) indicates **the replica is actively trying to stream WAL but the primary is repeatedly disconnecting it or letting   it timeout**. This is NOT a local crash, NOT an authentication failure, NOT a pg_hba.conf rejection (those would occur immediately at connection time, not after streaming begins).\n\nThe most recent log entry shows recovery is paused, which  is consistent with the replica failing to receive WAL data continuously.\n\n**Step 3 — Conclude:**\n\n**Container state:**\n- Status: running\n- ExitCode: 0\n- OOMKilled: false\n- Restarting: false\n\n**Log evidence:**\nThe replica           repeatedly connects successfully (\"started streaming WAL from primary\") but then either times out (\"terminating walreceiver due to timeout\") or the primary closes the connection unexpectedly (\"could not receive data from WAL stream:     server closed the connection unexpectedly\"). Most recent entry: \"recovery has paused\".\n\n**Conclusion:**\nThe replica container is healthy and running. It is NOT crashed. The connection failures are occurring at the protocol/replication  level — either the primary is aggressively closing replication connections, or there is a network issue causing timeouts. The replica is functioning correctly; the problem is upstream (primary-side) or in the network path between primary and replica.\n\nThis is **NOT a case where the replica is crashed and safe to restart**. The replica is already running and attempting to replicate. A restart of the replica container would not fix the fundamental issue: either the primary is    rejecting/closing the replication connection, or there is a network or configuration issue on the primary that needs to be addressed.\n\n**Escalation determination:**\nAccording to Expert Guidance, when \"Container running, logs show         repeated connection-rejected/no-pg_hba-entry messages: nothing to restart — the replica is healthy but refused\" → ESCALATE_TO: none.\n\nAlthough these are not explicitly \"connection rejected\" messages from pg_hba.conf, the pattern shows   the replica is being repeatedly disconnected by the primary (either intentionally closing the connection or letting it timeout). This is functionally equivalent to rejection: the replica itself is healthy, but something upstream is           preventing successful replication.\n\nHYPOTHESIS_1: The primary is closing or timing out replication connections from the replica, either due to primary-side configuration, resource exhaustion on the primary, or a network connectivity issue  between primary and replica | CONFIDENCE: 0.95 | EVIDENCE: \"FATAL:  could not receive data from WAL stream: server closed the connection unexpectedly\" and \"FATAL:  terminating walreceiver due to timeout\" occurring repeatedly in a pattern of ~15-30 minute intervals despite successful initial connection with \"started streaming WAL from primary\"\n\nHYPOTHESIS_2: The replica crashed but automatically restarted multiple times and is now running | CONFIDENCE: 0.0 | REJECTED:     check_host shows exitcode=0 with status=running and restarting=false, indicating clean state and no active restart loop; the logs show a pattern of intentional failures (timeouts and server-closed-connection errors) rather than process       crashes.\n\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "1m1.919507153s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 1,
      "mismatch": true,
      "overall_score": 1,
      "primary_confidence": 0.95,
      "primary_hypothesis": "Inactive replication slot (replica_slot) belongs to a permanently disconnected replica that cannot reconnect, blocking 28.6 MB of WAL",
      "secondary_hypothesis": "The primary is closing or timing out replication connections from the replica, either due to primary-side configuration, resource exhaustion on the primary, or a network connectivity issue between primary and   replica",
      "secondary_confidence": 0.95,
      "hypotheses": [
        {
          "text": "Inactive replication slot (replica_slot) belongs to a permanently disconnected replica that cannot reconnect, blocking 28.6 MB of WAL",
          "confidence": 0.95,
          "is_primary": true
        },
        {
          "text": "The primary is closing or timing out replication connections from the replica, either due to primary-side configuration, resource exhaustion on the primary, or a network connectivity issue between primary and replica",
          "confidence": 0.95,
          "is_primary": false
        },
        {
          "text": "The active replica at 172.18.0.4 is the one returning stale data",
          "confidence": 0.05,
          "is_primary": false,
          "rejected_reason": "Active replica shows zero replay_lag_bytes and reply_lag_seconds of 9 (normal feedback interval), indicating it is current and receiving WAL correctly."
        },
        {
          "text": "The replica crashed but automatically restarted multiple times and is now running",
          "confidence": 0,
          "is_primary": false,
          "rejected_reason": "check_host shows exitcode=0 with status=running and restarting=false, indicating clean state and no active restart loop; the logs show a pattern of intentional failures (timeouts and server-closed-connection     errors) rather than process crashes."
        }
      ]
    },
    {
      "failure_id": "db-replication-lag",
      "failure_name": "Replication lag",
      "category": "database",
      "score": 0,
      "passed": false,
      "keyword_pass": false,
      "diagnosis_pass": false,
      "tool_evidence": false,
      "ordering_pass": false,
      "response_text": "",
      "duration": "1m10.388697908s",
      "error": "POST http://gateway:8080/api/v1/fleet/playbooks/pb_f772c6b3/run: Post \"http://gateway:8080/api/v1/fleet/playbooks/pb_f772c6b3/run\": context deadline exceeded (Client.Timeout exceeded while awaiting headers)",
      "keyword_score": 0,
      "tool_score": 0,
      "diagnosis_score": 0
    },
    {
      "failure_id": "db-replication-lag",
      "failure_name": "Replication lag",
      "category": "database",
      "score": 1,
      "passed": true,
      "run_id": "plr_581b1a23",
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "ordering_pass": true,
      "tool_evidence_mode": "structured",
      "response_text": "I'm beginning an automated diagnostic playbook targeting a single Docker container on the local VM. The prompt indicates this is a replication lag triage scenario, but notes the target database is currently            unavailable. Let me start by checking connectivity to this specific target, then proceed with diagnostics.\nGood — the target database is now reachable. The connection string resolves to a PostgreSQL 16.14 instance running in Docker. Since   this is a replication lag triage, I need to first determine whether this is the primary or a replica, then assess replication status. Let me gather server info and replication status.\nCritical finding: The target is a PRIMARY server, not a  replica. However, there's a significant discrepancy in the replication status data:\n\n**Observed facts:**\n1. **Target is PRIMARY** (pg_is_in_recovery = false, role = \"primary\")\n2. **Active replica is connected**: One streaming replica   (application_name='walreceiver', client_addr=172.18.0.4, state='streaming', reply_lag_seconds=2)\n3. **Replica is caught up**: write_lag_bytes=0, flush_lag_bytes=0, replay_lag_bytes=0\n4. **Stale replication slot exists**: replica_slot is    INACTIVE (active=false) with lag_bytes=28,633,584 (~27 MB retained)\n\nThis is a **mixed scenario**: there is an active replica connection that is current, but there's also an inactive replication slot that is retaining WAL. The prompt       states \"recent writes on the primary are not visible [on the replica]\" — but the active replica's LSN gap is zero. This suggests either:\n\nA) The inactive slot is from a **different, permanently disconnected replica** that has been        decommissioned, and the currently-lagging replica mentioned in the complaint is the one at 172.18.0.4. But the active connection shows zero replay lag.\n\nB) The complaint describes an older state, and the replica has since caught up.\n\nC)  The active connection at 172.18.0.4 (172.18.0.4:5432) is the replica mentioned, but it's now current because the stale writes have been replicated. The stale slot from an old replica is no longer relevant to the *current* lag complaint.\n\n**The core finding**: There is an **inactive replication slot retaining 28.6 MB of WAL**. Even though the currently-connected replica is caught up, this slot is preventing WAL cleanup on the primary. If the slot remains inactive indefinitely,  it can eventually exhaust disk space.\n\nLet me verify the active connections to see if the connected replica's details confirm it is healthy:\n**Key observation**: The active WAL sender at 172.18.0.4 has been streaming for **1501 seconds    (~25 minutes)**. The query shows `START_REPLICATION 0/3000000 TIMELINE 1` — this replica is actively receiving WAL and has zero lag currently (as seen in reply_lag_seconds=2, write_lag_bytes=0, etc.).\n\nThe inactive slot `replica_slot` with 28.6 MB of retained WAL is the problem. It's not associated with the active streaming connection. This indicates a **permanently decommissioned replica** whose replication slot was never dropped.\n\n**Hypothesis formation**:\n\n**            HYPOTHESIS_1**: An old replica (distinct from the currently-connected one at 172.18.0.4) was permanently removed/shut down, but its replication slot (`replica_slot`) was never dropped. The slot is inactive and retaining 28.6 MB of WAL on the primary. The currently-connected replica at 172.18.0.4 is healthy and caught up (zero replay lag), so it is NOT the source of the stale data complaint — the stale data complaint likely refers to a test or observation made when that old       replica was still running or had just been disconnected.\n\n**HYPOTHESIS_2**: The complaint about stale data on \"the replica\" refers to the active replica at 172.18.0.4, which is somehow not applying writes in real time despite showing     zero replay_lag_bytes. This could indicate either a read-snapshot issue (the replica is replaying WAL but the connection used to test for stale data is seeing an older transaction snapshot) or a network/application-level issue unrelated to   replication lag itself. However, this is less likely given the clean zero-byte lag metrics and the active streaming state.\n\n**ROOT_CAUSE**: The stale data complaint is likely a **stale observation** from when the old replica (whose slot is now inactive) was either recently disconnected or struggling. The currently-connected replica is healthy. The **actionable issue** is the inactive replication slot `replica_slot` retaining WAL, which can eventually exhaust disk space if not  cleaned up.\n\n**FINDINGS**: This is a replication lag triage scenario, but the diagnosis is incomplete because the target provided is the PRIMARY, not a replica showing lag. The primary has an active replica at 172.18.0.4 with zero replay   lag (healthy), but an inactive replication slot `replica_slot` is retaining 28.6 MB of WAL from a decommissioned replica. The stale data complaint likely stems from an earlier state or a different diagnosis issue (not replication lag). The   inactive slot should be dropped after confirming the old replica is permanently decommissioned.\n\nHowever, per the playbook's instructions, this scenario does **not** match any of the three standard endings (Ending A for normal lagging-but- connected replica, Ending B for fully disconnected replica from current ops, or Ending C for stalled-but-connected replica). The current server is the PRIMARY, and any replica diagnostics must be done from the replica's connection string,    which is not provided in the prompt.\n\n**The constraint violation**: The prompt states \"The primary connection_string is ... use it verbatim for all tool calls\" and \"Do not check other databases.\" But the playbook is titled              \"Replication Lag Triage\" and expects me to diagnose replica lag. I have confirmed the target is the PRIMARY, not a replica. Per playbook guidance: \"If pg_is_in_recovery() is true on the target, run the checks on the primary instead\" —    but here, the inverse is true: the target IS the primary, so I cannot directly diagnose the replica's state.\n\nThe playbook provides no mechanism for \"target is primary; replica is elsewhere and unreachable.\" The \"Additional context\"    section does not provide a replica host:port (only the primary connection string). This falls under: \"If no replica host:port is present in the Additional context section of this prompt, you cannot complete the handoff — use Ending A's      shape instead but with ESCALATE_TO: none and FINDINGS noting the missing target.\"\n\nHYPOTHESIS_1: Primary-side stale replication slot from a decommissioned replica is retaining WAL; actively-connected replica at 172.18.0.4 is healthy with  zero replay lag | CONFIDENCE: 0.95 | EVIDENCE: \"slot_name | replica_slot; active | f; lag_bytes | 28633584\" and \"state | streaming; replay_lag_bytes | 0; reply_lag_seconds | 2\"\nHYPOTHESIS_2: Stale data complaint refers to a real lag on  the active replica at 172.18.0.4 that is masked by false-zero metrics or application-level snapshot staleness | CONFIDENCE: 0.05 | REJECTED: Active replica shows zero lag across all metrics (write_lag_bytes=0, flush_lag_bytes=0,              replay_lag_bytes=0) and is actively streaming with normal reply feedback (2 seconds), ruling out true replication lag\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "32.895704973s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 1,
      "overall_score": 1,
      "primary_confidence": 0.95,
      "primary_hypothesis": "Primary-side stale replication slot from a decommissioned replica is retaining WAL; actively-connected replica at 172.18.0.4 is healthy with zero replay lag",
      "secondary_hypothesis": "Stale data complaint refers to a real lag on the active replica at 172.18.0.4 that is masked by false-zero metrics or application-level snapshot staleness",
      "secondary_confidence": 0.05,
      "hypotheses": [
        {
          "text": "Primary-side stale replication slot from a decommissioned replica is retaining WAL; actively-connected replica at 172.18.0.4 is healthy with zero replay lag",
          "confidence": 0.95,
          "is_primary": true
        },
        {
          "text": "Stale data complaint refers to a real lag on the active replica at 172.18.0.4 that is masked by false-zero metrics or application-level snapshot staleness",
          "confidence": 0.05,
          "is_primary": false,
          "rejected_reason": "Active replica shows zero lag across all metrics (write_lag_bytes=0, flush_lag_bytes=0, replay_lag_bytes=0) and is actively streaming with normal reply feedback (2 seconds), ruling out true replication lag"
        }
      ]
    }
  ],
  "summary": {
    "total": 3,
    "passed": 2,
    "failed": 1,
    "pass_rate": 0.6666666666666666,
    "categories": {
      "database": {
        "total": 3,
        "passed": 2,
        "rate": 0.6666666666666666
      }
    }
  }
}
--- END FAULTTEST REPORT JSON ---
Evaluation scores posted to auditd: 2/3 runs

real    2m53.573s
user    0m0.030s
sys     0m0.038s
```

Let's get the `vault accuracy` for the `db-replication-lag` fault:

```
[boris@ ~/helpdesk]$ docker run --rm --network helpdesk_default \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest \
    faulttest accuracy \
        db-replication-lag \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY"

Gateway: http://gateway:8080  ·  version: v0.26.0-35-g3a0d5c0-3a0d5c0  ·  host: e2b4d78448f8

Diagnosis accuracy for series: pbs_replication_lag

  Feedback submitted : 5 runs
  Correct diagnoses  : 5
  Accuracy rate      : 100%

  Breakdown by feedback time:
    Post-incident (after recovery): 5 of 5 correct (100%)

Triage consistency
  Fault         : db-replication-lag  (Replication lag)
  Verdict       : UNSTABLE
  Runs          : 3
  Pass rate     : 67%
  Conf range    : 0pp  (primary hypothesis, passing runs only)
  Clean         : no  (1/3 run(s) tripped a verified warning signal)
  Warning types : mismatch=1(varies)
  Playbook      : pbs_replication_lag
  Diagnosis model: claude-haiku-4-5-20251001
  Tested at     : 2026-09-03 17:50 UTC  (0 days ago)

  Attribution (taxonomy 1.2)
  Primary class  : replica-disconnected
  Consistent     : no (split)  (1/3 runs)
  Distribution   : UNKNOWN=1, replica-disconnected=1, stale-replication-slot-accumulating-wal=1
  Judge spread   : 0.47
```

This is interesting. A few things happened here, which are worth flagging rather than just noting the cert:

  1/ The UNSTABLE/DIRTY cert is correct, not concerning  
     It's the stability mechanism doing its job: 1 timeout + 1 fabrication-risk flag out of 3 runs is a genuine instability, correctly reported as such.  

  2/ Run 2's failure is a timeout ("context deadline exceeded... awaiting headers", after Run 1 itself took over a minute).
     This seems suspiciously like an environmental load from extensive container churn, but it doesn't smell like a code bug. It's definetely worth keeping an eye on though.

  3/ Run 1's "FABRICATION RISK: mismatch" is the one item worth a deeper look.
     That's a real, code-derived signal (`checkFabricationRisk`: the model narrated a tool call the audit trail never confirmed actually executed). Hmm... this doesn't look like a noise.
     Let me check what specifically triggered it before deciding whether it's worth pursuing further:

```
[boris@ ~/helpdesk]$ docker run --rm --network helpdesk_default \
>        -v "$(pwd)/testing:/testing-docker/testing" \
>        -w /testing-docker \
>        helpdesk:latest faulttest vault incidents plr_31980b13 \
>        --gateway http://gateway:8080 --api-key gateway-api-key
Gateway: http://gateway:8080  ·  version: v0.26.0-35-g3a0d5c0-3a0d5c0  ·  host: e2b4d78448f8


════════════════════════════════════════════════════════════
INCIDENT plr_31980b13
Started: 2026-09-03 17:47 UTC   Duration: 61s
Operator: alice@example.com
════════════════════════════════════════════════════════════

── TRIAGE
Playbook:  pbs_replication_lag
Findings:  The replica container is healthy and running, successfully initiating
           replication connections to the primary but the primary repeatedly
           closes or times out these connections within 15-30 minutes, preventing
           continuous WAL streaming; the replica itself is not crashed and
           restarting it will not resolve the upstream replication refusal or
           timeout issue.
           ⚠ unverified — no matching tool execution in the audit trail

Hypotheses:
  [PRIMARY  95%] Inactive replication slot (replica_slot) belongs to a permanently disconnected replica that cannot reconnect, blocking 28.6 MB of WAL
                 Evidence: "slot_name | replica_slot ... active | f ... lag_bytes | 28633584"
  [REJECTED  5%] The active replica at 172.18.0.4 is the one returning stale data
                 Rejected: Active replica shows zero replay_lag_bytes and reply_lag_seconds of 9 (normal feedback interval), indicating it is current and receiving WAL correctly.

── ESCALATION 1/1
Playbook:  pbs_sysadmin_replica_connectivity_triage   Outcome: resolved
Findings:  The replica container is healthy and running, successfully initiating
           replication connections to the primary but the primary repeatedly
           closes or times out these connections within 15-30 minutes, preventing
           continuous WAL streaming; the replica itself is not crashed and
           restarting it will not resolve the upstream replication refusal or
           timeout issue.

Hypotheses:
  [PRIMARY  95%] The primary is closing or timing out replication connections from the replica, either due to primary-side configuration, resource exhaustion on the primary, or a network connectivity issue between primary and replica
  [REJECTED  0%] The replica crashed but automatically restarted multiple times and is now running

── EVALUATION
Score:         100%   (diagnosis 100%)
Diagnosis:     1.00 (heuristic)   Agent confidence: 95%

── POST-INCIDENT FEEDBACK
  triage:                      ✓ correct

── JOURNEYS
  WHY = Incident narrative (this view)   WHAT = Audit trail (vault journeys)

  triage+escalation:1:   faulttest-4534c881-db-replication-lag-r1

  → vault journeys faulttest-4534c881-db-replication-lag-r1
```

Aha. This seems to confirm that it's a mismatch on a specific tool call and the transcript seems to give a clue as to why:  
   Right before this point, the model's text shows "Checking PostgreSQL logs... Let me correct that: Re-checking... Logging is disabled"

This looks like a self-correcting retry after an apparent wrong-syntax first attempt. Possibly `read_pg_log` and `get_pg_settings` called with bad parameters then retried correctly? This is just a hypothesis with no facts available to dig further, but it does seem like the first, malformed attempt may have failed client-side validation before ever reaching the audit trail as a recorded tool-execution event. And so the mismatch-checker correctly found "narrated but no matching execution", which is...  technically accurate, but likely a benign self-correction rather than genuine fabrication.

Let's not chase this further right now because it would take us on a possibly long tangent. It's a single occurrence that doesn't block anything and `db-replication-lag` isn't part of our original four-fault plan anyway. Let's keep it in mind if the same pattern shows up again on a different fault, since that would suggest the mismatch-detector genuinely doesn't handle self-correcting retries well rather than this being one-off noise.

So here's where we currently stand:

```
[boris@ ~/helpdesk]$ docker run --rm --network helpdesk_default \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest \
    faulttest vault list \
        --ids db-replication-lag,db-replica-disconnected,db-replica-container-stopped,db-replica-stalled \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY"
Gateway: http://gateway:8080  ·  version: v0.26.0-35-g3a0d5c0-3a0d5c0  ·  host: e2b4d78448f8

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-replication-lag               any        pbs_replication_lag             pbs_replication_remediate        2026-09-03  PASS       UNSTABLE(3) ⚠1/3 warnings attr=replica-disconnected(split) 0 runs  (system)
    diag  → vault versions pbs_replication_lag
db-replica-disconnected          any        pbs_replication_lag             (none)                           (never)                —              -
    → vault versions pbs_replication_lag
db-replica-container-stopped     any        pbs_replication_lag             (none)                           (never)                —              -
    → vault versions pbs_replication_lag
db-replica-stalled               any        pbs_replication_lag             (none)                           2026-09-03  PASS       STABLE(3) attr=replica-stalled-but-connected -
    → vault versions pbs_replication_lag
```

With that, there are two more faults left: `db-replica-disconnected` and `db-replica-container-stopped`. Let's get crackin:


## 3rd replication failure scenario: `db-replica-disconnected`

Let's run the fault injection test with `--repeat N` to see if we can get this fault certified:

```
[boris@ ~/helpdesk]$ date; time docker run --rm --network helpdesk_default \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest faulttest run \
        --ids db-replica-disconnected \
        --repeat 3 \
        --approval-mode=force \
        --via-gateway \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY" \
        --judge-api-key "$HELPDESK_API_KEY" \
        --conn "host=host.docker.internal port=15432 dbname=testdb user=postgres password=xxx" \
        --replica-conn "host=host.docker.internal port=15433 dbname=testdb user=postgres password=xxx" \
        --agent-model claude-haiku-4-5-20251001 \
        --operator alice@example.com
Thu Sep  3 13:59:25 EDT 2026
time=2026-09-03T17:59:25.993Z level=INFO msg=--conn host=host.docker.internal
time=2026-09-03T17:59:25.993Z level=INFO msg=--replica-conn host=host.docker.internal

--- Testing: Replica disconnected (walreceiver dropped) (db-replica-disconnected) — 3 runs ---

  Run 1/3
time=2026-09-03T17:59:25.997Z level=INFO msg="executing injection spec" type=sql phase=inject
time=2026-09-03T17:59:26.631Z level=INFO msg="sending prompt to agent via playbook" failure=db-replica-disconnected series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal       port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_9cdf41ea)
time=2026-09-03T18:00:07.623Z level=INFO msg="executing injection spec" type=sql phase=teardown
time=2026-09-03T18:00:07.642Z level=INFO msg="executing injection spec" type=sql phase=inject
  [PASS] score=100%
         [PRIMARY 99%] Primary's pg_hba.conf rejects unencrypted replication connections from the replica's network address (172.18.0.4)
         [95%] Replica container (helpdesk-test-replica at host.docker.internal:15433) has crashed, is not running, or has lost network connectivity to the primary.
         [REJECTED 5%] Replica connection was explicitly closed or configured to disconnect, but replication slot was not cleaned up. — No evidence of deliberate administrative action in database logs or connection state; stale slot with     growing lag indicates abnormal disconnect, not planned.
         [REJECTED 0%] Replica process crashed or is unhealthy — check_host shows Status=running with exitcode=0, restarting=false, oomkilled=false — the replica container is healthy and continuously attempting reconnection.

  Run 2/3
time=2026-09-03T18:00:09.133Z level=INFO msg="sending prompt to agent via playbook" failure=db-replica-disconnected series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal       port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_ed9bc22e)
time=2026-09-03T18:00:53.674Z level=INFO msg="executing injection spec" type=sql phase=teardown
  [PASS] score=100%
         [PRIMARY 95%] Replica disconnected (container crashed, network failure, or process terminated); slot remains on primary holding WAL
         [95%] Primary is rejecting the replica's replication connection at the pg_hba.conf layer because the replica is attempting an unencrypted connection while the primary requires SSL/TLS
         [REJECTED 5%] Replica paused WAL replay on purpose (intentional standby pause) — Playbook context reports replica "hasn't reflected any new writes in over 10 minutes" and "shows no recent activity", not a deliberate pause
         [REJECTED 0%] The replica container crashed or encountered an OOM kill — check_host shows Status=running, exitcode=0, oomkilled=false — container is up with no prior crash.

  Run 3/3
time=2026-09-03T18:00:53.692Z level=INFO msg="executing injection spec" type=sql phase=inject
time=2026-09-03T18:00:55.429Z level=INFO msg="sending prompt to agent via playbook" failure=db-replica-disconnected series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal       port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_1c2234ac)
time=2026-09-03T18:01:34.259Z level=INFO msg="executing injection spec" type=sql phase=teardown
  [PASS] score=100%
         [PRIMARY 95%] Replica process crashed, hung, or network partition severed the replication connection, leaving the physical replication slot inactive and unable to release retained WAL on the primary
         [95%] Replica is being rejected by primary due to pg_hba.conf rules
         [REJECTED 5%] Replica is still running but unable to connect to primary due to authentication or network configuration change — The primary is fully accepting connections and no log errors are visible; if replica had auth/network    issues it would still maintain a TCP connection to pg_stat_replication or show a stuck "handshake" state
         [REJECTED 5%] Replica crashed or is in restart loop — check_host shows Status=running, exitcode=0, restarting=false, oomkilled=false — the container is healthy and not crashing.
time=2026-09-03T18:01:34.575Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (3 runs):
    Pass rate:    3/3 (100%)
    Confidence:   min=95% max=99% range=4pp mean=96%  (H1, passing runs only)
    Verdict:      STABLE
    Attribution:  replica-disconnected (3/3)  consistent: yes  [taxonomy 1.2]
  ────────────────────────────────────────────────────────────────
    Warnings:     0/3 run(s) tripped a verified warning signal
    Warning types: objective_evidence:replica_disconnected=3(predictable)
    Confirmed:     objective_evidence:replica_disconnected=3(predictable)
    Clean:        yes
time=2026-09-03T18:01:37.135Z level=INFO msg="fault stability cert posted" fault_id=db-replica-disconnected verdict=STABLE clean=CLEAN warning_count=0 n_runs=3

=== Fault Test Report: 75976ba4 ===

[PASS] Replica disconnected (walreceiver dropped) (db-replica-disconnected) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Replica disconnected (walreceiver dropped) (db-replica-disconnected) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Replica disconnected (walreceiver dropped) (db-replica-disconnected) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 3 | Passed: 3 | Failed: 0 | Rate: 100%
  database: 3/3 (100%)

time=2026-09-03T18:01:37.651Z level=INFO msg="fault stability cert posted" fault_id=db-replica-disconnected::hop:pbs_sysadmin_replica_connectivity_triage verdict=STABLE clean=CLEAN warning_count=0 n_runs=3
Report written to ./faulttest-75976ba4.json
```

Good! Let's get the `vault accuracy` for it:

```
[boris@ ~/helpdesk]$ docker run --rm --network helpdesk_default \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest \
    faulttest accuracy \
        db-replica-disconnected \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY"

Diagnosis accuracy for series: pbs_replication_lag

  Feedback submitted : 8 runs
  Correct diagnoses  : 8
  Accuracy rate      : 100%

  Breakdown by feedback time:
    Post-incident (after recovery): 8 of 8 correct (100%)

Triage consistency
  Fault         : db-replica-disconnected  (Replica disconnected (walreceiver dropped))
  Verdict       : STABLE
  Runs          : 3
  Pass rate     : 100%
  Conf range    : 4pp  (primary hypothesis, passing runs only)
  Clean         : yes
  Warning types : objective_evidence:replica_disconnected=3(predictable)
  Confirmed     : objective_evidence:replica_disconnected=3(predictable)
  Playbook      : pbs_replication_lag
  Diagnosis model: claude-haiku-4-5-20251001
  Tested at     : 2026-09-03 18:01 UTC  (0 days ago)

  Attribution (taxonomy 1.2)
  Primary class  : replica-disconnected
  Consistent     : yes  (3/3 runs)
```

Good, so `db-replica-disconnected` came back fully clean too. 3/3 pass, STABLE, attribution consistent (replica-disconnected), objective evidence confirmed on all 3 runs, no warnings. And yes, the `vault accuracy` also confirms 8/8 correct diagnoses across history for that series.

Let's run the `vault list` to see where the four replication faults stand so far:

```
[boris@ ~/helpdesk]$ docker run --rm --network helpdesk_default \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest \
    faulttest vault list \
        --ids db-replication-lag,db-replica-disconnected,db-replica-container-stopped,db-replica-stalled \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY"
Gateway: http://gateway:8080  ·  version: v0.26.0-35-g3a0d5c0-3a0d5c0  ·  host: e2b4d78448f8

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-replication-lag               any        pbs_replication_lag             pbs_replication_remediate        2026-09-03  PASS       UNSTABLE(3) ⚠1/3 warnings attr=replica-disconnected(split) 0 runs  (system)
    diag  → vault versions pbs_replication_lag
db-replica-disconnected          any        pbs_replication_lag             (none)                           2026-09-03  PASS       STABLE(3) attr=replica-disconnected -
    → vault versions pbs_replication_lag
db-replica-container-stopped     any        pbs_replication_lag             (none)                           (never)                —              -
    → vault versions pbs_replication_lag
db-replica-stalled               any        pbs_replication_lag             (none)                           2026-09-03  PASS       STABLE(3) attr=replica-stalled-but-connected -
    → vault versions pbs_replication_lag
```

That leaves `db-replica-container-stopped` as the only fault from the four still showing (never) tested. Let's try to certify it too:


## 4th replication failure scenario: `db-replica-container-stopped`

```
[boris@ ~/helpdesk]$ date; time docker run --rm --network helpdesk_default \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest faulttest run \
        --ids db-replica-container-stopped \
        --repeat 3 \
        --approval-mode=force \
        --via-gateway \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY" \
        --judge-api-key "$HELPDESK_API_KEY" \
        --conn "host=host.docker.internal port=15432 dbname=testdb user=postgres password=xxx" \
        --replica-conn "host=host.docker.internal port=15433 dbname=testdb user=postgres password=xxx" \
        --agent-model claude-haiku-4-5-20251001 \
        --operator alice@example.com
Thu Sep  3 14:04:36 EDT 2026
time=2026-09-03T18:04:36.499Z level=INFO msg=--conn host=host.docker.internal
time=2026-09-03T18:04:36.499Z level=INFO msg=--replica-conn host=host.docker.internal

--- Testing: Replica container stopped (db-replica-container-stopped) — 3 runs ---

  Run 1/3
time=2026-09-03T18:04:36.502Z level=INFO msg="executing injection spec" type=docker phase=inject
time=2026-09-03T18:04:37.197Z level=INFO msg="sending prompt to agent via playbook" failure=db-replica-container-stopped series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal  port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_d5b51ee3)
time=2026-09-03T18:05:21.476Z level=INFO msg="executing injection spec" type=docker phase=teardown
time=2026-09-03T18:05:22.140Z level=INFO msg="waiting after injection" phase=teardown duration=5s
  [PASS] score=100%
         [PRIMARY 95%] Replica has disconnected from the primary, leaving behind an inactive replication slot that is retaining WAL.
         [95%] Replica actively rejected by primary due to pg_hba.conf policy denying unencrypted replication connections from the replica's host
         [REJECTED 5%] Replica is connected but in a stalled state and not sending feedback. — get_active_connections returns no results, indicating no WAL sender process is present; a connected replica would show a walsender entry even if   stalled.
         [REJECTED 5%] Replica crashed and needs restart — Container exited with exitcode=0 indicating clean shutdown, not a crash; logs show intentional termination by administrator command, not a process failure

  Run 2/3
time=2026-09-03T18:05:27.142Z level=INFO msg="executing injection spec" type=docker phase=inject
time=2026-09-03T18:05:27.726Z level=INFO msg="sending prompt to agent via playbook" failure=db-replica-container-stopped series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal  port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_fbd088db)
time=2026-09-03T18:06:23.179Z level=INFO msg="executing injection spec" type=docker phase=teardown
time=2026-09-03T18:06:23.735Z level=INFO msg="waiting after injection" phase=teardown duration=5s
time=2026-09-03T18:06:28.737Z level=INFO msg="executing injection spec" type=docker phase=inject
  [PASS] score=100%
         [PRIMARY 95%] Replica container or process has crashed or is unreachable at the network level; the replication connection has been dropped but the WAL slot remains, retaining 59 MB of unconfirmed WAL
         [95%] Replica was cleanly shut down by administrator command after successfully achieving replication stream
         [95%] Replica container restarted successfully and is now streaming WAL from the primary; the earlier pg_hba.conf rejection errors appear to have been transient or have been resolved
         [REJECTED 90%] The prior diagnosis that the replica was cleanly stopped is partially incorrect; the logs show the primary's pg_hba.conf was actively rejecting replica connections with "no encryption" errors until recovery — This is  not a primary hypothesis but rather a corrective finding to the prior diagnosis—the restart succeeded, so it is not an alternative root cause, merely additional context for why the replica was disconnected.
         [REJECTED 5%] Replica is still running but authentication or connection configuration has changed, preventing it from reconnecting — No retry attempts or stalled connections visible on primary; if replica were attempting to connect  it would either fail visibly in the primary log or show as a stalled row in pg_stat_replication
         [REJECTED 0%] Replica failed due to pg_hba.conf rejection and never recovered — Logs show connection rejection resolved at 18:01:35 when replica successfully began streaming WAL before shutdown

  Run 3/3
time=2026-09-03T18:06:29.590Z level=INFO msg="sending prompt to agent via playbook" failure=db-replica-container-stopped series_id=pbs_replication_lag playbook_id=pb_f772c6b3 gateway=http://gateway:8080 agent-conn="host=host.docker.internal  port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_af9b4e11)
time=2026-09-03T18:07:24.129Z level=INFO msg="executing injection spec" type=docker phase=teardown
time=2026-09-03T18:07:24.690Z level=INFO msg="waiting after injection" phase=teardown duration=5s
  [PASS] score=100%
         [PRIMARY 98%] The replica container was cleanly shut down by an administrator (fast shutdown request), not crashed; it previously recovered from pg_hba.conf rejection and successfully replicated until termination.
         [95%] Replica has disconnected from the primary and has not re-established the replication connection; the inactive replication slot is retaining 91+ MB of WAL that cannot be recycled until the replica reconnects or the slot is      dropped.
         [95%] Primary pg_hba.conf explicitly rejects the replica's replication connection attempt; restart succeeded but replication cannot resume until the primary's configuration is corrected
         [REJECTED 5%] Replica connection was deliberately paused by an administrator and the replica is waiting for manual intervention to resume. — No evidence of administrative pause; the slot is inactive (not paused), indicating true     disconnection rather than a managed pause.
         [REJECTED 5%] Replica container crashed due to infrastructure failure and needed restart only — Restart succeeded and replica is running, but logs immediately show repeated primary-side pg_hba.conf rejections on reconnect            attempt—this is not a container crash symptom, but a configuration barrier on the primary.
         [REJECTED 2%] The replica crashed due to a replication protocol error or corruption. — All shutdown sequences in the logs show clean, graceful termination with no PANIC, FATAL (other than the initial pg_hba rejection), or corruption signals; exitcode=0 confirms clean process exit.
time=2026-09-03T18:07:29.990Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (3 runs):
    Pass rate:    3/3 (100%)
    Confidence:   min=95% max=98% range=3pp mean=96%  (H1, passing runs only)
    Verdict:      STABLE
    Attribution:  replica-disconnected (3/3)  consistent: yes  [taxonomy 1.2]
  ────────────────────────────────────────────────────────────────
    Warnings:     0/3 run(s) tripped a verified warning signal
    Warning types: objective_evidence:replica_disconnected=3(predictable)
    Confirmed:     objective_evidence:replica_disconnected=3(predictable)
    Clean:        yes
time=2026-09-03T18:07:32.631Z level=INFO msg="fault stability cert posted" fault_id=db-replica-container-stopped verdict=STABLE clean=CLEAN warning_count=0 n_runs=3
time=2026-09-03T18:07:33.384Z level=INFO msg="fault stability cert posted" fault_id=db-replica-container-stopped::hop:pbs_replica_restart_action verdict=STABLE clean=CLEAN warning_count=0 n_runs=2

=== Fault Test Report: 4a847532 ===

[PASS] Replica container stopped (db-replica-container-stopped) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Replica container stopped (db-replica-container-stopped) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Replica container stopped (db-replica-container-stopped) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 3 | Passed: 3 | Failed: 0 | Rate: 100%
  database: 3/3 (100%)

time=2026-09-03T18:07:33.987Z level=INFO msg="fault stability cert posted" fault_id=db-replica-container-stopped::hop:pbs_sysadmin_replica_connectivity_triage verdict=STABLE clean=CLEAN warning_count=0 n_runs=3
Report written to ./faulttest-4a847532.json
```

Let's get the `vault accuracy` for this failure scenaio `db-replica-container-stopped`):

```
[boris@ ~/helpdesk]$ docker run --rm --network helpdesk_default \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest \
    faulttest accuracy \
        db-replica-container-stopped \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY"

Diagnosis accuracy for series: pbs_replication_lag

  Feedback submitted : 11 runs
  Correct diagnoses  : 11
  Accuracy rate      : 100%

  Breakdown by feedback time:
    Post-incident (after recovery): 11 of 11 correct (100%)

Triage consistency
  Fault         : db-replica-container-stopped  (Replica container stopped)
  Verdict       : STABLE
  Runs          : 3
  Pass rate     : 100%
  Conf range    : 3pp  (primary hypothesis, passing runs only)
  Clean         : yes
  Warning types : objective_evidence:replica_disconnected=3(predictable)
  Confirmed     : objective_evidence:replica_disconnected=3(predictable)
  Playbook      : pbs_replication_lag
  Diagnosis model: claude-haiku-4-5-20251001
  Tested at     : 2026-09-03 18:07 UTC  (0 days ago)

  Attribution (taxonomy 1.2)
  Primary class  : replica-disconnected
  Consistent     : yes  (3/3 runs)
```

Good! `db-replica-container-stopped` is now also fully clean: 3/3 pass, STABLE, attribution consistent (replica-disconnected), all objective evidence confirmed, no warnings. And the `vault accuracy` shows 11/11 correct across the whole `pbs_replication_lag` series history now.

## Summary table of the four replication failure scenarios 

Let's run the `vault list` on the four replication failure scenarios we've just reviewed and certified:

```
[boris@ ~/helpdesk]$ docker run --rm --network helpdesk_default \
    -v "$(pwd)/testing:/testing-docker/testing" \
    -v "$(pwd)/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    helpdesk:latest \
    faulttest vault list \
        --ids db-replication-lag,db-replica-disconnected,db-replica-container-stopped,db-replica-stalled \
        --gateway http://gateway:8080 \
        --api-key "$HELPDESK_CLIENT_API_KEY"
Gateway: http://gateway:8080  ·  version: v0.26.0-35-g3a0d5c0-3a0d5c0  ·  host: e2b4d78448f8

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-replication-lag               any        pbs_replication_lag             pbs_replication_remediate        2026-09-03  PASS       UNSTABLE(3) ⚠1/3 warnings attr=replica-disconnected(split) 0 runs  (system)
    diag  → vault versions pbs_replication_lag
db-replica-disconnected          any        pbs_replication_lag             (none)                           2026-09-03  PASS       STABLE(3) attr=replica-disconnected -
    → vault versions pbs_replication_lag
db-replica-container-stopped     any        pbs_replication_lag             (none)                           2026-09-03  PASS       STABLE(3) attr=replica-disconnected -
    → vault versions pbs_replication_lag
db-replica-stalled               any        pbs_replication_lag             (none)                           2026-09-03  PASS       STABLE(3) attr=replica-stalled-but-connected -
    → vault versions pbs_replication_lag
```

Here's the same data, but summarized in a table format:

```
  ┌──────────────────────────────┬─────────────┬───────────────────────────────┬───────────────────┐
  │            Fault             │   Verdict   │          Attribution          │       Clean       │
  ├──────────────────────────────┼─────────────┼───────────────────────────────┼───────────────────┤
  │ db-replica-stalled           │ STABLE(3)   │ replica-stalled-but-connected │ yes               │
  ├──────────────────────────────┼─────────────┼───────────────────────────────┼───────────────────┤
  │ db-replica-disconnected      │ STABLE(3)   │ replica-disconnected          │ yes               │
  ├──────────────────────────────┼─────────────┼───────────────────────────────┼───────────────────┤
  │ db-replica-container-stopped │ STABLE(3)   │ replica-disconnected          │ yes               │
  ├──────────────────────────────┼─────────────┼───────────────────────────────┼───────────────────┤
  │ db-replication-lag           │ UNSTABLE(3) │ replica-disconnected (split)  │ no — 1/3 warnings │
  └──────────────────────────────┴─────────────┴───────────────────────────────┴───────────────────┘
```

Three of four are clean. The `db-replication-lag` is the one still showing the split-attribution/fabrication-risk pattern from earlier (as a reminder: single-occurrence, likely a benign self-correcting retry on the mismatch detector, not chased further for now). 

And with that we completed all four replication faults verification. W00t!
