# aiHelpDesk Sample#20 (from the source): veto split (`evidence_coverage_gap` / `evidence_required_but_unconfirmed`)

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

In this sample transcript we look at a specific feature originally presented in aiHelpDesk [v0.27 release](https://github.com/borisdali/helpdesk/releases/tag/v0.27.0): in particular the [objective evidence framework](../OBJECTIVE_EVIDENCE.md) and how it now became an AI harness layer (the [forth layer to be exact](../AIGOVERNANCE.md#layer-4--objective-evidence-content-verification)) of the model fabrication detection mechanism.

As [a reviewer pointed out](https://www.linkedin.com/feed/update/urn:li:activity:7498800236610387968?commentUrn=urn%3Ali%3Acomment%3A%28activity%3A7498800236610387968%2C7498801672496926720%29&replyUrn=urn%3Ali%3Acomment%3A%28activity%3A7498800236610387968%2C7502762441844428800%29&dashCommentUrn=urn%3Ali%3Afsd_comment%3A%287498801672496926720%2Curn%3Ali%3Aactivity%3A7498800236610387968%29&dashReplyUrn=urn%3Ali%3Afsd_comment%3A%287502762441844428800%2Curn%3Ali%3Aactivity%3A7498800236610387968%29), there are two different cases to consider: an agent that declares no objective evidence at all and the one that declares, but cannot confirm. And the question is whether the former scores higher than than latter? What do we count as a "pass"?

This question prompted us to institute a new mechanism that we refer to as the "evidence-content gate". This is how it's defined in the docs:

>  Evidence-content gate (`evidence_required_but_unconfirmed`) — when a fault declares `expected_diagnosis.objective_evidence_signal` that signal must also appear in the run's `objective_evidence_confirmed` list. Not just have fired, but have been demonstrably cited in the model's own hypothesis text (see [OBJECTIVE_EVIDENCE.md §4](OBJECTIVE_EVIDENCE.md#4-confirming-a-signal-the-confirmation-registry)).

>  If declared and unconfirmed, aiHelpDesk force-sets the passed to false... regardless of score.

>  That is, a fault can score 100% on keywords/category/tools and still fail here. This is deliberately an unweighted veto, not a fifth weighted dimension: a confirmed vs. unconfirmed diagnosis is a different kind of trust claim, not a "somewhat worse" one, so a partial score contribution that a strong keyword/tool score could outweigh would defeat the point.



So which case scores higher and do either one of them pass? Let's look at [the code](https://github.com/borisdali/helpdesk/blob/4fc3296460fc592833553cf9bbd2c48ece439d23/testing/cmd/faulttest/main.go#L573-L574)

```
 if sig := ...ObjectiveEvidenceSignal; sig != "" && !evidenceSignalConfirmed(sig, evalResult.ObjectiveEvidenceConfirmed) {
    evalResult.Passed = false
 }
```

So our `evidenceSignalConfirmed` just checks the (list) membership. An agent that declares nothing has an empty `ObjectiveEvidenceConfirmed` list vs. an agent that declares-and-fails-confirmation... also has that signal missing from the list.
So yes, both fail the same membership check, both get `passed := false`. Staying silent doesn't dodge the veto. That is, it fires on "not confirmed," not on "fired, but unconfirmed" specifically, so silence doesn't get treated as a smaller sin than a wrong-but-confident claim. Should it?

As a reviewer pointed out: "Both should fail the gate and count them in separate buckets. Silence is a coverage gap, showing that the agent never reached the evidence path at all, and that fix lives in the tooling or the prompt. A declared-but-unconfirmed claim is the model asserting past what it can show, which is a different fix. Collapsing both into one Passed=false number is what makes the failure rate stop telling you which one to go work on".

And that precisely what we did, but as of the [v0.28 release](https://github.com/borisdali/helpdesk/releases/tag/v0.28.0) we make the difference between them (the two buckets) visible: lack of prompt/tooling coverage vs. problems w/ confirmation logic). This is a step forward because finding which bucket the failed gate falls into no longer requires digging through the raw JSON response.

Now, we still keep the combined pass rate (as a headline) because that's one number we use for CI gating and cross-fault/cross-release comparison. We are not changing it just because we're getting more precise about why a run failed. What we do instead is surfacing the `objective_evidence_signals present-or-absent` vs `objective_evidence_confirmed` by making them the first-class fields. So a separate, more granular axis is displayed alongside the headline:

```
 Pass rate:  4/5 (80%)             ← headline, unchanged
 ...
 Evidence gaps: 1/5 coverage-gap (never fired), 1/5 unconfirmed (fired, not confirmed)
```

So: pass rate leads, the split rides alongside it as its own visible line, not buried and not competing for the headline slot.

See [here](https://github.com/borisdali/helpdesk/blob/release/v0.28.0/docs/FAULTTEST.md#11-scoring-formula) for more details.


'Nuff said, let's see some examples. There are three tests in total:

  -- Test 1: Happy path  
  -- Test 2: Deliberately forcing `evidence_coverage_gap`   
  -- Test 3: Deliberately forcing `evidence_required_but_unconfirmed`  

Let's kick off with the happy path:

## Test 1: Happy path


`objective_evidence_signals` present-or-absent vs. `objective_evidence_confirmed`, which is exactly the "not a first-class field yet" problem he's naming.


So as a TL;DR: that's where we stand: the pass rate we introduced in v0.27 leads, but the newly introduced split in v0.28 now rides alongside it as its own visible line, not buried and not competing for the headline slot.


It's not needed for this specific test to run `--repeat N`, but we'll reuse the prevous run of this `db-replica-disconnected` fault that was used for the purpose of obtaining the cert:

```
[boris@cassiopeia ~/cassiopeia/helpdesk]$ date; time go run ./testing/cmd/faulttest run       --ids db-replica-disconnected,db-replica-container-stopped,db-replica-stalled --repeat 3       --approval-mode=force --via-gateway       --gateway  http://localhost:8080       --api-key "$HELPDESK_CLIENT_API_KEY"       --judge-api-key "$HELPDESK_API_KEY"       --conn "host=localhost port=15432 dbname=testdb user=postgres password=testpass"       --replica-conn "host=localhost port=15433 dbname=testdb user=postgres password=testpass"       --agent-model claude-haiku-4-5-20251001       --operator alice@example.com
Tue Sep  8 09:34:00 EDT 2026
time=2026-09-08T09:34:01.146-04:00 level=INFO msg=--conn host=localhost
time=2026-09-08T09:34:01.146-04:00 level=INFO msg=--replica-conn host=localhost

--- Testing: Replica disconnected (walreceiver dropped) (db-replica-disconnected) — 3 runs ---

  Run 1/3
time=2026-09-08T09:34:01.149-04:00 level=INFO msg="executing injection spec" type=sql phase=inject
time=2026-09-08T09:34:01.763-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-replica-disconnected series_id=pbs_replication_lag playbook_id=pb_ab6059a6 gateway=http://localhost:8080 agent-conn="host=localhost           port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_9677e124)
time=2026-09-08T09:34:41.943-04:00 level=INFO msg="executing injection spec" type=sql phase=teardown
  [PASS] score=100%
         [PRIMARY 95%] Replica is disconnected from primary and has been for some time; the inactive replication slot "replica_slot" is retaining 28,633,584 bytes of WAL that cannot be recycled until the slot is dropped or the replica        reconnects
         [95%] Primary has changed pg_hba.conf to require encrypted replication, but replica is attempting unencrypted connection and is being systematically rejected
         [REJECTED 5%] Replica temporarily lost network connectivity but may auto-reconnect soon; the lag is residual from the disconnection period — Playbook guidance and downstream consumer report ("hasn't reflected any new writes in over  10 minutes") indicate prolonged absence, not a transient blip
         [REJECTED 5%] Replica container crashed and needs restart — Container is running with exitcode=0, oomkilled=false, restarting=false; logs show active reconnection attempts, not a crash.

  Run 2/3
time=2026-09-08T09:34:41.992-04:00 level=INFO msg="executing injection spec" type=sql phase=inject
time=2026-09-08T09:34:43.345-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-replica-disconnected series_id=pbs_replication_lag playbook_id=pb_ab6059a6 gateway=http://localhost:8080 agent-conn="host=localhost           port=15432 dbname=testdb user=postgres password=testpass"
  ⚠  UNVERIFIED EVIDENCE (primary, non-blocking): 1 quote(s) did not match any real tool output
         Replica has disconnected from primary; inactive replication slot is retaining WAL on primary — (slot
  Feedback submitted (triage/post_incident run_id=plr_aea03a63)
time=2026-09-08T09:35:27.952-04:00 level=INFO msg="executing injection spec" type=sql phase=teardown
  [PASS] score=100%
         [PRIMARY 95%] Replica has disconnected from primary; inactive replication slot is retaining WAL on primary
         [95%] Primary server's pg_hba.conf rejects replication connections from the replica's host without SSL encryption
         [REJECTED 5%] Replica connection dropped due to network transient that has not recovered — No active connection in pg_stat_replication means the connection is not present at all, not merely lagging; this is a complete disconnection, not a network transient.
         [REJECTED 0%] Replica container crashed or was forcibly terminated — Container is running with Status=running, exitcode=0, oomkilled=false; the process did not crash.

  Run 3/3
time=2026-09-08T09:35:27.990-04:00 level=INFO msg="executing injection spec" type=sql phase=inject
time=2026-09-08T09:35:29.287-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-replica-disconnected series_id=pbs_replication_lag playbook_id=pb_ab6059a6 gateway=http://localhost:8080 agent-conn="host=localhost           port=15432 dbname=testdb user=postgres password=testpass"
  ⚠  UNVERIFIED EVIDENCE (primary, non-blocking): 1 quote(s) did not match any real tool output
         Replica connection to primary has been severed (network failure, container restart, or replica process termination); slot is inactive and retaining 28.6 MB of WAL, and no replication connections are active on the primary — No active connections found.
  Feedback submitted (triage/post_incident run_id=plr_0ea024ac)
time=2026-09-08T09:36:10.934-04:00 level=INFO msg="executing injection spec" type=sql phase=teardown
  [PASS] score=100%
         [PRIMARY 99%] The replica is healthy and running, but the primary's pg_hba.conf is configured to reject unencrypted replication connections from the replica's network address.
         [95%] Replica connection to primary has been severed (network failure, container restart, or replica process termination); slot is inactive and retaining 28.6 MB of WAL, and no replication connections are active on the primary
         [REJECTED 5%] Replica is deliberately paused via pg_wal_replay_pause() and has reconnected but replay is frozen — Replica is completely absent from the primary's connection tracking; if it were connected and paused,                  pg_stat_replication would show a row with state=streaming or catchup, which does not appear
         [REJECTED 1%] The replica process crashed and is not attempting connections. — check_host shows Status=running with exitcode=0 and oomkilled=false; the logs show active, repeated connection attempts every ~1 second, proving the      replica process is running and executing.
time=2026-09-08T09:36:11.355-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (3 runs):
    Pass rate:    3/3 (100%)
    Confidence:   min=95% max=99% range=4pp mean=96%  (H1, passing runs only)
    Verdict:      STABLE
    Attribution:  replica-disconnected (3/3)  consistent: yes  [taxonomy 1.2]
  ────────────────────────────────────────────────────────────────
    Warnings:     0/3 run(s) tripped a verified warning signal
    Signal types: objective_evidence:replica_disconnected=3(predictable), unverified_evidence=2(varies)
    Confirmed:     objective_evidence:replica_disconnected=3(predictable)
    Clean:        yes
time=2026-09-08T09:36:14.339-04:00 level=INFO msg="fault stability cert posted" fault_id=db-replica-disconnected verdict=STABLE clean=CLEAN warning_count=0 n_runs=3
time=2026-09-08T09:36:15.049-04:00 level=INFO msg="fault stability cert posted" fault_id=db-replica-disconnected::hop:pbs_sysadmin_replica_connectivity_triage verdict=STABLE clean=CLEAN warning_count=0 n_runs=3

=== Fault Test Report: f6aa71f3 ===

[PASS] Replica disconnected (walreceiver dropped) (db-replica-disconnected) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Replica disconnected (walreceiver dropped) (db-replica-disconnected) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Replica disconnected (walreceiver dropped) (db-replica-disconnected) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 3 | Passed: 3 | Failed: 0 | Rate: 100%
  database: 3/3 (100%)

Report written to ./faulttest-f6aa71f3.json
```

For completeness, let's run the `vault list` on that fault...

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault list \
>    --ids db-replica-disconnected \
>    --api-key "$HELPDESK_CLIENT_API_KEY" \
>    --gateway $HELPDESK_GATEWAY_URL
Gateway: http://localhost:8080  ·  version: dev  ·  host: cassiopeia

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-replica-disconnected          any        pbs_replication_lag             (none)                           2026-09-08  PASS       STABLE(3) attr=replica-disconnected -
    → vault versions pbs_replication_lag
```

... followed by the details via `vault accuracy` for that fault:

```
[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest vault accuracy \
>     db-replica-disconnected \
>    --api-key "$HELPDESK_CLIENT_API_KEY" \
>    --gateway $HELPDESK_GATEWAY_URL
Tue Sep  8 09:43:40 EDT 2026
Gateway: http://localhost:8080  ·  version: dev  ·  host: cassiopeia

Diagnosis accuracy for series: pbs_replication_lag

  Feedback submitted : 65 runs
  Correct diagnoses  : 65
  Accuracy rate      : 100%

  Breakdown by feedback time:
    Post-incident (after recovery): 65 of 65 correct (100%)

Triage consistency
  Fault         : db-replica-disconnected  (Replica disconnected (walreceiver dropped))
  Verdict       : STABLE
  Runs          : 3
  Pass rate     : 100%
  Conf range    : 4pp  (primary hypothesis, passing runs only)
  Clean         : yes
  Signal types  : objective_evidence:replica_disconnected=3(predictable), unverified_evidence=2(varies)
  Confirmed     : objective_evidence:replica_disconnected=3(predictable)
  Playbook      : pbs_replication_lag
  Diagnosis model: claude-haiku-4-5-20251001
  Tested at     : 2026-09-08 13:36 UTC  (0 days ago)

  Attribution (taxonomy 1.2)
  Primary class  : replica-disconnected
  Consistent     : yes  (3/3 runs)

Cert history (last 10)
  2026-09-08 13:36 UTC   STABLE   CLEAN attr=consistent (3 runs)
      ↳ changed since 2026-09-08 04:53 UTC: trust earned (now STABLE+CLEAN+attribution-consistent); clean: DIRTY→CLEAN; warning_distribution: unverified_evidence 1→2
  2026-09-08 04:53 UTC   STABLE   DIRTY attr=consistent (3 runs)
  2026-09-08 04:19 UTC   STABLE   DIRTY attr=consistent (3 runs)
  2026-09-08 03:03 UTC   STABLE   DIRTY attr=consistent (3 runs)
      ↳ changed since 2026-09-08 01:22 UTC: warning_distribution: unverified_evidence 3→1
  2026-09-08 01:22 UTC   STABLE   DIRTY attr=consistent (3 runs)
      ↳ changed since 2026-09-07 23:07 UTC: stability: UNSTABLE→STABLE; clean: CLEAN→DIRTY; attribution: split→consistent; warning_distribution: objective_evidence:replica_disconnected 0→3, unverified_evidence 0→3; confirmed_distribution:    objective_evidence:replica_disconnected 0→3
  2026-09-07 23:07 UTC   UNSTABLE CLEAN attr=split      (1 runs)
      ↳ changed since 2026-09-07 22:06 UTC: stability: STABLE→UNSTABLE; clean: DIRTY→CLEAN; attribution: consistent→split; warning_distribution: objective_evidence:replica_disconnected 3→0, unverified_evidence 3→0; confirmed_distribution:    objective_evidence:replica_disconnected 3→0
  2026-09-07 22:06 UTC   STABLE   DIRTY attr=consistent (3 runs)
      ↳ changed since 2026-09-07 20:42 UTC: stability: UNSTABLE→STABLE; clean: CLEAN→DIRTY; attribution: split→consistent; warning_distribution: objective_evidence:replica_disconnected 0→3, unverified_evidence 0→3; confirmed_distribution:    objective_evidence:replica_disconnected 0→3
  2026-09-07 20:42 UTC   UNSTABLE CLEAN attr=split      (1 runs)
  2026-09-07 20:41 UTC   UNSTABLE CLEAN attr=split      (1 runs)
  2026-09-07 20:14 UTC   UNSTABLE CLEAN attr=split      (1 runs)
```


No false coverage-gap/unconfirmed is 

...

The last full `--repeat 3` run (report f6aa71f3) covered exactly this:

All 9/9 PASS, zero `⚠ EVIDENCE COVERAGE GAP` / `⚠ EVIDENCE REQUIRED` lines anywhere in the output, and every `Confirmed:` line showed only `objective_evidence:<signal>=3(predictable)`. Nothing under a coverage-gap or unconfirmed bucket.  That's true for all three replica faults we ran (see the full 3-replica run [here](SAMPLE021.md)).


## Test 2: Deliberately forcing `evidence_coverage_gap`

So everything we did so far exercised the real catalog against a real fault. Neither deliberate-failure branch (catalog typo forcing `evidence_coverage_gap` or whatever forces `evidence_required_but_unconfirmed`) got triggered on purpose.  The split's code is well-covered by unit tests (`TestClassifyEvidenceGate`, 7 cases) and an integration round-trip test, but that's not the same as a live run against a real agent showing the actual printed `⚠ line` and `[FAIL]`, which is the whole point here about visibility.

Let's take one of the replication stories: `db-replica-disconnected`: back up the original/correct catalog, apply a typo to that replication fault and... run:

```
   cp testing/catalog/failures.yaml /tmp/failures.yaml.bak
   echo "backed up"
   sed -n '619p' testing/catalog/failures.yaml
```

In particular, let's temporarily flip this line for this test:

```
 619 -        objective_evidence_signal: replica_disconnected
 619 +        objective_evidence_signal: replica_disconnected_TYPO
```

Once this catalog edit is done, we are all set to run the failure injection test:

```
[boris@ ~/helpdesk]$ date; time  go run ./testing/cmd/faulttest run \
>     --ids db-replica-disconnected --repeat 1 \
>     --approval-mode=force --via-gateway --gateway http://localhost:8080 \
>     --api-key "$HELPDESK_CLIENT_API_KEY" --judge-api-key "$HELPDESK_API_KEY" \
>     --conn "host=localhost port=15432 dbname=testdb user=postgres password=xxx" \
>     --replica-conn "host=localhost port=15433 dbname=testdb user=postgres password=xxx" \
>     --agent-model claude-haiku-4-5-20251001 --operator alice@example.com
Tue Sep  8 10:37:43 EDT 2026
time=2026-09-08T10:37:45.599-04:00 level=INFO msg=--conn host=localhost
time=2026-09-08T10:37:45.600-04:00 level=INFO msg=--replica-conn host=localhost

--- Testing: Replica disconnected (walreceiver dropped) (db-replica-disconnected) ---
time=2026-09-08T10:37:45.603-04:00 level=INFO msg="executing injection spec" type=sql phase=inject
time=2026-09-08T10:37:46.130-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-replica-disconnected series_id=pbs_replication_lag playbook_id=pb_ab6059a6 gateway=http://localhost:8080 agent-conn="host=localhost           port=15432 dbname=testdb user=postgres password=xxx"

  ⚠  EVIDENCE COVERAGE GAP: expected signal "replica_disconnected_TYPO" never fired — agent's tool calls never reached this evidence path — failing regardless of keyword/category score

  Feedback submitted (triage/post_incident run_id=plr_f340cfa6)
time=2026-09-08T10:38:30.454-04:00 level=INFO msg="executing injection spec" type=sql phase=teardown
Diagnostic Result:   [FAIL] score=100% (keywords=100% tools=100% judge=100%)

=== Fault Test Report: 1b6d7e98 ===

[FAIL] Replica disconnected (walreceiver dropped) (db-replica-disconnected) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 1 | Passed: 0 | Failed: 1 | Rate: 0%
  database: 0/1 (0%)

Report written to ./faulttest-1b6d7e98.json
```

Bingo! Note the `EVIDENCE COVERAGE GAP` because, well, expected signal "replica_disconnected_TYPO" indeed never fired. Agent's tool calls never reached this evidence path, which, importantly, fails regardless of the [score](../FAULTTEST.md#11-scoring-formula). That is, the test scored 100% keywords/tools/category, right root cause of `pg_hba.conf` rejection and yet the overall mark is `[FAIL]` (the real `replica_disconnected` still fired under its real name, but just not the typo'd one the catalog now asks for :-).

This is all the info we need, but more troubleshooting info is available in the raw JSON if you venture to check. In particular, you'll find there the precise labeling: `evidence_coverage_gap`: true and `objective_evidence_signals` / `objective_evidence_confirmed` both still show the real signal `replica_disconnected` fired and was confirmed under its real name. The catalog just declared a name that never appears, which is exactly the distinction we were shooing for: never-fired vs. fired-but-unconfirmed.

```
Report written to ./faulttest-1b6d7e98.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "1b6d7e98",
  "timestamp": "2026-09-08T14:38:30Z",
  "results": [
    {
      "failure_id": "db-replica-disconnected",
      "failure_name": "Replica disconnected (walreceiver dropped)",
      "category": "database",
      "score": 1,
      "passed": false,
      "run_id": "plr_f340cfa6",
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "ordering_pass": true,
      "tool_evidence_mode": "structured",
      "response_text": "I will begin diagnosing why the target database is unavailable. This is an automated triage session, so I will continue investigating even if the initial connection fails.\n\n**Expectation**: The target at             `host=localhost port=15432 dbname=testdb user=postgres password=testpass` may be down or unresponsive; I will attempt connection and then gather evidence from logs or system state if needed.\nGood — the connection succeeded. The database is  reachable. Now I will check the server's replication status to assess lag and replica connectivity.\n\n**Expectation**: This is the primary (I can confirm via role), and I need to see pg_stat_replication to identify any connected replicas,   their WAL position gaps (write_lag, flush_lag, replay_lag), and any inactive replication slots that might indicate a disconnected replica.\n**Critical finding**: The replication status shows:\n- Role: Primary (confirmed)\n- A physical        replication slot named `replica_slot` exists\n- The slot is **inactive** (active = false)\n- The slot has a **large lag of 129,029,872 bytes (~123 MB)** retained WAL\n\nThere are **no rows in pg_stat_replication**, meaning no active replicas are currently connected. However, an inactive slot with substantial retained WAL indicates the replica was recently connected and has now disconnected, with significant WAL accumulated behind its restart_lsn.\n\n**Expectation**: I will check active connections and the PostgreSQL log to see if there are any recent connection attempts from the replica or error messages explaining the disconnection.\n**Log limitation**: The PostgreSQL logging collector is disabled, so I cannot read the log file. However, the active connections check shows no current database connections at all (despite the server reporting 1 active connection earlier — that was likely the diagnostic connection itself which has now closed).\n\nLet me    check if there have been recent write operations or if WAL is still being generated, which would help confirm whether the primary is still processing writes while the replica sits disconnected.\n\n**Expectation**: Database statistics will    show commit counts and other activity, helping confirm the primary is still operational and writing.\n**Observation**: The primary database shows 431 commits in testdb and 2137 commits in postgres. Row modification activity (inserts,         updates, deletes) is all zero, indicating the workload is primarily reads, not writes. This is consistent with the test environment context.\n\n**Summary of evidence**:\n\n1. **Primary is operational**: Connection successful, role=primary,   uptime 9+ hours.\n2. **Replica is disconnected**: No rows in pg_stat_replication; no active streaming connections.\n3. **Stale inactive slot**: A physical replication slot (`replica_slot`) exists with `active=false` and `lag_bytes=129029872` (~123 MB).\n4. **Replica has not reflected writes in 10+ minutes**: Confirmed by the problem statement — the consumer reports the replica hasn't shown new writes in over 10 minutes, and the replica's own status page shows no recent activity. \n5. **Limited primary write activity**: The database statistics show zero INSERT/UPDATE/DELETE operations, so the lag is not due to massive write volume overwhelming the replica. The lag is static (the replica disconnected, accumulated      retained WAL, and has not reconnected).\n\n**Hypothesis Formation**:\n\nThe evidence points to a **disconnected replica** scenario (Ending B from the playbook):\n- The replica was connected (evidence: inactive slot with physical WAL          retained)\n- The replica has disconnected (evidence: absent from pg_stat_replication, inactive slot status)\n- The replica has not reconnected for over 10 minutes (evidence: slot remains inactive, consumer report)\n- This is NOT a            replication lag issue the primary can resolve — this is a **replica-side or network connectivity issue** that requires host/container-level investigation.\n\nThe playbook directs Ending B escalation to                                         `pbs_sysadmin_replica_connectivity_triage` with TARGET set to the replica's host:port.\n\nHYPOTHESIS_1: Replica disconnected from primary; inactive replication slot retains 129MB of WAL pending replica reconnection. | CONFIDENCE: 0.95 |      EVIDENCE: \"slot_name | replica_slot\\nslot_type | physical\\nactive    | f\\nlag_bytes | 129029872\"\nHYPOTHESIS_2: Replica process is running but network partition prevents WAL streaming. | CONFIDENCE: 0.05 | REJECTED: Cannot diagnose      replica's own process state from primary side; requires host/container-level inspection by sysadmin.\nROOT_CAUSE: HYPOTHESIS_1\n\n---\n\nI'll investigate the replica connectivity issue by following the Expert Guidance step by step.\n\n**Step 1 — Replica container state:**\nI expect check_host to reveal whether the replica container (localhost:15433) is running, crashed, paused, or in a restart loop. This will determine whether we're dealing with a container-level failure or a    rejected connection.\nExcellent — the replica container is **running** (Status=running, exitcode=0, oomkilled=false, restarting=false). This means the container itself is healthy and not crashed. A disconnect with a running container         indicates the replica is being rejected or blocked, not suffering from a local crash.\n\n**Step 2 — Replica logs:**\nI expect the logs to show connection-level signals: either repeated reconnection attempts followed by rejection messages     (primary refusing the replica), or clean shutdown/startup messages, or corruption/FATAL errors. I'll retrieve the last 50 lines of container output first.\nPerfect — the logs are crystal clear. The replica is **running and healthy**, but     the **primary is actively rejecting the replication connection** due to pg_hba.conf configuration.\n\n**Step 3 — Conclude:**\n\n**Container state (from check_host):**\n- Status: running\n- ExitCode: 0\n- OOMKilled: false\n- Restarting:       false\n\n**Log evidence (from get_host_logs):**\nThe replica has been repeatedly attempting to connect to the primary (172.18.0.2:5432) since 14:37:45 UTC. Every single attempt fails with the same error:\n\n\"FATAL:  pg_hba.conf rejects      replication connection for host \\\"172.18.0.4\\\", user \\\"postgres\\\", no encryption\"\n\nThis error repeats continuously every ~1 second. The replica's process is healthy and running (PID 19 is actively monitoring WAL availability), but it cannot establish a replication connection because the primary's pg_hba.conf does not have a rule allowing unencrypted replication connections from the replica's IP address (172.18.0.4).\n\n**Conclusion:**\nThe replica is NOT crashed and   NOT safe to restart. The problem is a **primary-side configuration issue**: the primary is rejecting replication connections from this replica's IP due to pg_hba.conf policy. Restarting the replica will not resolve this — it will simply fail to connect again immediately after startup. The root cause requires modifying the primary's pg_hba.conf to allow replication connections from 172.18.0.4, either unencrypted or with SSL configured.\n\nHYPOTHESIS_1: Primary's pg_hba.conf       rejects replication connection from replica's IP address due to encryption requirement mismatch | CONFIDENCE: 0.98 | EVIDENCE: \"FATAL:  pg_hba.conf rejects replication connection for host \\\"172.18.0.4\\\", user \\\"postgres\\\", no        encryption\"\nHYPOTHESIS_2: Replica process crashed and needs restart | CONFIDENCE: 0.01 | REJECTED: check_host shows Status=running with exitcode=0 and oomkilled=false; the container is active and not crashed\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "44.299647834s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 1,
      "objective_evidence_signals": [
        "replica_disconnected"
      ],
      "objective_evidence_confirmed": [
        "replica_disconnected"
      ],
      "evidence_coverage_gap": true,
      "overall_score": 1,
      "primary_confidence": 0.98,
      "primary_hypothesis": "Primary's pg_hba.conf rejects replication connection from replica's IP address due to encryption requirement mismatch",
      "secondary_hypothesis": "Replica disconnected from primary; inactive replication slot retains 129MB of WAL pending replica reconnection.",
      "secondary_confidence": 0.95,
      "hypotheses": [
        {
          "text": "Primary's pg_hba.conf rejects replication connection from replica's IP address due to encryption requirement mismatch",
          "confidence": 0.98,
          "is_primary": true
        },
        {
          "text": "Replica disconnected from primary; inactive replication slot retains 129MB of WAL pending replica reconnection.",
          "confidence": 0.95,
          "is_primary": false
        },
        {
          "text": "Replica process is running but network partition prevents WAL streaming.",
          "confidence": 0.05,
          "is_primary": false,
          "rejected_reason": "Cannot diagnose replica's own process state from primary side; requires host/container-level inspection by sysadmin."
        },
        {
          "text": "Replica process crashed and needs restart",
          "confidence": 0.01,
          "is_primary": false,
          "rejected_reason": "check_host shows Status=running with exitcode=0 and oomkilled=false; the container is active and not crashed"
        }
      ]
    }
  ],
  "summary": {
    "total": 1,
    "passed": 0,
    "failed": 1,
    "pass_rate": 0,
    "categories": {
      "database": {
        "total": 1,
        "passed": 0,
        "rate": 0
      }
    }
  }
}
--- END FAULTTEST REPORT JSON ---
Evaluation scores posted to auditd: 1/1 runs

real    0m47.631s
user    0m0.721s
sys     0m0.531s
```

Before we forget, let's revert the catalog back to its production state:

```
   cp /tmp/failures.yaml.bak testing/catalog/failures.yaml
   diff /tmp/failures.yaml.bak testing/catalog/failures.yaml 
   sed -n '619p' /Users/boris/cassiopeia/helpdesk/testing/catalog/failures.yaml
```


## Test 3: Deliberately forcing `evidence_required_but_unconfirmed`

Now this test's recipe needs the same "break the expectation, not the model" approach as the previous test and we settled on a clean, deterministic lever of `resource_named_in_quote`. It exists specifically because the default probe doesn't work for bool signals like this one:

```
   cp agents/database/objective_evidence.yaml /tmp/objective_evidence.yaml.bak
   echo "backed up"
```

... and adjust agents/database/objective_evidence.yaml as follows:

```
 47 -  confirmation_probe: resource_named_in_quote
 47 +  # TEMPORARY (A3 manual test, revert after): confirmation_probe removed so
 48 +  # this falls back to the default evidence_quote_contains_value, which
 49 +  # compares against the literal bool value "false" — a real model always
 50 +  # quotes psql's own "f" abbreviation, never the word "false", so this
 51 +  # signal will fire but never confirm. That's the whole point of the test.
 52 +  # confirmation_probe: resource_named_in_quote
```

And we are ready to roll (just keep in mind, depending on how/what platform you run aiHelpDesk on, from the source, on K8s, via a Docker/Podman container or directly on a host/VM, you may need to bounce the DB Agent because the objective evidence YAML is only read at the startup):

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest run \
>     --ids db-replica-disconnected --repeat 1 \
>     --approval-mode=force --via-gateway --gateway http://localhost:8080 \
>     --api-key "$HELPDESK_CLIENT_API_KEY" --judge-api-key "$HELPDESK_API_KEY" \
>     --conn "host=localhost port=15432 dbname=testdb user=postgres password=xxx" \
>     --replica-conn "host=localhost port=15433 dbname=testdb user=postgres password=xxx" \
>     --agent-model claude-haiku-4-5-20251001 --operator alice@example.com
time=2026-09-08T10:47:17.501-04:00 level=INFO msg=--conn host=localhost
time=2026-09-08T10:47:17.501-04:00 level=INFO msg=--replica-conn host=localhost

--- Testing: Replica disconnected (walreceiver dropped) (db-replica-disconnected) ---
time=2026-09-08T10:47:17.504-04:00 level=INFO msg="executing injection spec" type=sql phase=inject
time=2026-09-08T10:47:17.760-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-replica-disconnected series_id=pbs_replication_lag playbook_id=pb_ab6059a6 gateway=http://localhost:8080 agent-conn="host=localhost           port=15432 dbname=testdb user=postgres password=xxx"

  ⚠  EVIDENCE REQUIRED: expected signal "replica_disconnected" fired but was not confirmed — failing regardless of keyword/category score

  Feedback submitted (triage/post_incident run_id=plr_b6c65523)
time=2026-09-08T10:47:37.882-04:00 level=INFO msg="executing injection spec" type=sql phase=teardown
Diagnostic Result:   [FAIL] score=100% (keywords=100% tools=100% judge=100%)

=== Fault Test Report: 553a7dc7 ===

[FAIL] Replica disconnected (walreceiver dropped) (db-replica-disconnected) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 1 | Passed: 0 | Failed: 1 | Rate: 0%
  database: 0/1 (0%)

Report written to ./faulttest-553a7dc7.json
```

Nice! The model's own quote proves the mechanism because it wrote "active    | f", never the word false, so `evidence_quote_contains_value` correctly never matched even though the real `replica_disconnected` signal fired. Here's the summary of the three artifacts:

```
        - ⚠  EVIDENCE REQUIRED: expected signal `replica_disconnected` fired but was not confirmed
        - [FAIL] despite a correct, 100%-scoring diagnosis
        - JSON: "objective_evidence_signals": ["replica_disconnected"] (fired) with no objective_evidence_confirmed at all,
                "objective_evidence_unconfirmed": ["replica_disconnected"], "evidence_required_but_unconfirmed": true —
```

This is the exact opposite bucket from the previous test's `evidence_coverage_gap`, both correctly distinguished from a single fault, on demand.

```
      "objective_evidence_gate": true,
      "objective_evidence_signals": [
        "replica_disconnected"
      ],
      "objective_evidence_unconfirmed": [
        "replica_disconnected"
      ],
      "evidence_required_but_unconfirmed": true,
```

And yes, don't forget to revert (and bounce the DB Agent again):

```
cp /tmp/objective_evidence.yaml.bak agents/database/objective_evidence.yaml
```

## TL;DR: of all three tests

  - Happy path: all 3 replica faults, `--repeat 3`, zero coverage-gap/unconfirmed lines, `Confirmed:` shows only objective_evidence:<signal>=N(predictable).

  - Never fired: ⚠ EVIDENCE COVERAGE GAP, `evidence_coverage_gap`: true, real signal still confirmed under its real name.

  - Fired, not confirmed: ⚠ EVIDENCE REQUIRED, `evidence_required_but_unconfirmed`: true, `objective_evidence_unconfirmed` populated, no `objective_evidence_confirmed` at all.

And that's a wrap! All three branches now demonstrated live, distinctly labeled, exactly as designed.

