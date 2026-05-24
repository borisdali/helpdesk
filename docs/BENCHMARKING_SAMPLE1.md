# Benchmarking diagnosis sample#1: Crystal Ball vs. the full power of aiHelpDesk

For the background on Fault Injection Testing in aiHelpDesk see [here](FAULTTEST.md). The Crystal Ball mode is presented [here](BENCHMARKING.md#2-crystal-ball-mode).

This is an example showcasing a comparison of diagnosis of one specific database fault (Checkpoint stall / bgwriter overload that we refer to as `db-checkpoint-warning` in the catalog) by two different means. First, we run the diagnosis via the Crystal Ball mode. We then follow on with the full aiHelpDesk diagnosis according to the structured playbook guidance (and also see [another example](BENCHMARKING_SAMPLE2.md) that demonstrates the transaction lock chain fault).

If you are not familiar with the Crystal Ball mode, these two blog posts [here](https://medium.com/google-cloud/dont-ask-your-ai-to-diagnose-production-unless-you-ve-given-it-a-structured-guided-playbook-46195c2aae71) and [here](https://medium.com/google-cloud/we-wanted-a-dramatic-ai-agent-failure-we-got-something-better-5d6d57135a88) go beyond our standard documentation and present a gentle introduction and the thinking behind this comparison.

## Full, guided, structured aiHelpDesk diagnosis
In the examples below we inject the checkpoint stall / bgwriter overload fault test (dubbed `db-checkpoint-warning` in our [fault catalog](FAULTTEST.md#61-external-compatible-faults)) and attempt to diagnose this fault by running the Crystal Ball and the full aihelpDesk playbook-driven diagnosis. In this particular example we run both from the source code (but in the [latter section](BENCHMARKING_SAMPLE.md#running-on-k8s-via-a-helm-chart) in this document we show equivalent commands for running this test on K8s via Helm).

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault list|egrep '(FAULT|---|db-checkpoint-warning)'
FAULT                            PLATFORM   DIAG PLAYBOOK              REMED PLAYBOOK             FAULT TEST             INCIDENTS
---------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-checkpoint-warning            any        pbs_checkpoint_bgwriter_triage pbs_checkpoint_bgwriter_triage 2026-05-14  FAIL       -
```

First off, start aiHelpDesk Gateway without the Crystal Ball setting (either omit it altogether or set it to false as a flag or as an env var):

```
[boris@ ~/helpdesk]$ date; HELPDESK_CRYSTAL_BALL=true \ <-- do NOT set this var for the real, non-Crystal Ball run
  TEST_DB_PASSWORD=<your DB password> \
  HELPDESK_AGENT_API_KEY=<your API key if you run aiHelpDesk in static authn mode> \ <-- required if the HELPDESK_OPERATING_MODE=fix and HELPDESK_IDENTITY_PROVIDER=static, see https://github.com/borisdali/helpdesk/blob/main/docs/IDENTITY.md#2-identity-provider
  HELPDESK_AUDIT_API_KEY=<similar to the above and in the minimal, non-Prod setting it can be set to the same key> \
  HELPDESK_MODEL_VENDOR=anthropic \
  HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001 \
  HELPDESK_API_KEY=<your Anthropic key> \
  HELPDESK_INFRA_CONFIG=<your infra.json, e.g. ./deploy/docker-compose/infrastructure.json> \
  HELPDESK_OPERATING_MODE=fix \
  HELPDESK_IDENTITY_PROVIDER=static \
  HELPDESK_GATEWAY_ADDR=localhost:8080 \
  HELPDESK_AGENT_URLS="http://localhost:1100,http://localhost:1102,http://localhost:1103,http://localhost:1104,http://localhost:1106" \
  HELPDESK_AUDIT_URL=http://localhost:1199 \
  HELPDESK_AUDIT_ENABLED=true \
  HELPDESK_USERS_FILE=users.example.yaml \
go run ./cmd/gateway
```

The above Gateway incantation is for running tests from the source. If running on one of the three supported deployment platforms (directly on a host, VM or Bare Metal, inside a Docker/Podman container or on K8s), the Gateway runs automatically and is controlled by the platform specific mechanism (.env file, env vars or Helm chart's values.yaml).

Once the Gateway and the agents are up, run the test:

```
[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest run \
>       --ids db-checkpoint-warning \
>       --conn "host=localhost port=15432 dbname=testdb user=postgres password=testpass" \
>       --agent-conn faulttest-db \
>       --via-gateway \
>       --gateway http://localhost:8080 \
>       --api-key <your API key for static authn> \
>       --purpose diagnostic \
>       --judge --judge-api-key $API_KEY --judge-vendor anthropic --judge-model claude-haiku-4-5-20251001
Thu May 14 12:03:59 EDT 2026
time=2026-05-14T12:04:01.669-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-05-14T12:04:01.669-04:00 level=INFO msg="LLM judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: Checkpoint warnings — bgwriter overload (db-checkpoint-warning) ---
time=2026-05-14T12:04:01.669-04:00 level=INFO msg="injecting failure" id=db-checkpoint-warning type=sql mode=internal
time=2026-05-14T12:04:07.538-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-checkpoint-warning series_id=pbs_checkpoint_bgwriter_triage playbook_id=pb_53cd6324 gateway=http://localhost:8080
time=2026-05-14T12:04:33.087-04:00 level=INFO msg="tearing down failure" id=db-checkpoint-warning type=sql
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% judge=100%)

=== Fault Test Report: d5dcf519 ===

[PASS] Checkpoint warnings — bgwriter overload (db-checkpoint-warning) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Reasoning: "Agent correctly identified bgwriter_lru_maxpages = 2 as the primary root cause, properly analyzed buffers_backend elevation (47% of writes from backends), rejected max_wal_size as a factor, and distinguished                checkpoint_warning as a test parameter rather than a causal issue; diagnosis is complete and actionable."

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

LLM judge scored diagnosis for 1 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-d5dcf519.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "d5dcf519",
  "timestamp": "2026-05-14T16:04:33Z",
  "results": [
    {
      "failure_id": "db-checkpoint-warning",
      "failure_name": "Checkpoint warnings — bgwriter overload",
      "category": "database",
      "score": 1,
      "passed": true,
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "tool_evidence_mode": "structured",
      "response_text": "I'll begin diagnosing why faulttest-db is unavailable and following the Checkpoint \u0026 bgwriter Triage playbook. Let me start with a connectivity check.\nGood — the database is reachable. Now I'll follow the        playbook: STEP 1 is to confirm checkpoint warnings in the log, STEP 2 is bgwriter stats, STEP 3 is parameter review, and STEP 4 is recommendations. Let me gather the evidence.\nGood — database is up. Now let me get bgwriter settings and      additional checkpoint configuration:\nExcellent. Let me also check for more checkpoint parameters:\nNow I have comprehensive data. Let me analyze the findings:\n\n## Analysis\n\n**STEP 1 — Checkpoint warnings confirmed:**\nThe log shows      checkpoint_warning was set to 10 seconds, and there are deliberate forced checkpoints being executed every second (03:45:36, 03:45:37 in the log). The checkpoint_warning default is 30 seconds, but it was lowered to 10 seconds as a test       parameter from the configuration file.\n\n**STEP 2 — bgwriter stats analysis:**\nFrom `get_bgwriter_stats`:\n- **checkpoints_req: 34** vs **checkpoints_timed: 42** — This is nearly balanced, indicating that roughly equal numbers of           checkpoints are being triggered by WAL volume vs. scheduled time. However, over the lifetime this shows significant forced checkpoints.\n- **maxwritten_clean: 0** — bgwriter is NOT hitting its per-round page limit. This is good.\n- **        buffers_backend: 25,515** vs **buffers_checkpoint: 28,848** — The ratio is significant: 25515 / (28848 + 0 + 25515) = 47%, meaning nearly half of all dirty page writes are coming from backend processes rather than checkpoint/bgwriter. This   is a **red flag**.\n- **buffers_backend_fsync: 0** — Backends are not doing their own fsyncs, so the situation is not severe yet.\n- **buffers_clean: 0** — bgwriter clean writes are zero, which is unexpected. This correlates with a very      low **bgwriter_lru_maxpages: 2**, which is far below the default of 100.\n\n**STEP 3 — Parameter misconfiguration identified:**\nThe critical misconfiguration is:\n- **bgwriter_lru_maxpages = 2** (should be default 100 or higher for write    workloads)\n  - At only 2 pages per round with a bgwriter_delay of 200ms, bgwriter can only flush ~10 pages/second before throttling.\n  - This forces backends to write dirty pages themselves, inflating buffers_backend.\n  - This causes      unpredictable I/O latency during query execution.\n\n- **checkpoint_warning = 10 seconds** (default is 30 seconds)\n  - This is a **test parameter** set deliberately to trigger warnings more frequently.\n  - It does not affect checkpoint     frequency, only the warning threshold.\n\n- **max_wal_size = 1024 MB** (default, not reduced)\n  - This is at the default and has not been a problem historically.\n\n- **checkpoint_completion_target = 0.9** (good — smooths checkpoint I/      O)\n\n- **checkpoint_timeout = 300s** (good — scheduled checkpoints are normal)\n\n**Root cause:** The database is experiencing elevated write latency because bgwriter is severely throttled by `bgwriter_lru_maxpages = 2`. This forces         backends to perform their own writes during query execution, causing unpredictable I/O bursts. The checkpoint_warning parameter at 10 seconds is just a diagnostic trigger and not the cause of performance degradation.\n\nHYPOTHESIS_1:         bgwriter_lru_maxpages is set to 2, which is far below the default of 100, throttling the background writer and forcing backends to write 47% of dirty pages themselves | CONFIDENCE: 0.95 | EVIDENCE: \"buffers_backend: 25515\" out of total     54363 buffer writes, and \"bgwriter_lru_maxpages setting 2 unit default_value 100\"\nHYPOTHESIS_2: max_wal_size is too small and causing excessive forced checkpoints | CONFIDENCE: 0.05 | REJECTED: checkpoints_req (34) and checkpoints_timed   (42) are nearly balanced, and max_wal_size is at default 1GB, which is appropriate for the observed write volume\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "24.379706625s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 1,
      "judge_reasoning": "Agent correctly identified bgwriter_lru_maxpages = 2 as the primary root cause, properly analyzed buffers_backend elevation (47% of writes from backends), rejected max_wal_size as a factor, and distinguished         checkpoint_warning as a test parameter rather than a causal issue; diagnosis is complete and actionable.",
      "judge_model": "claude-haiku-4-5-20251001",
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

It's easier for humans to read the diagnosis by formatting the above JSON output. It's not difficult to automate this or just copy and paste the section between the BEGIN/END FAULTTEST REPORT JSON tags to a file (we call it /tmp/reason.json in the excerpt below) and run it through a simple Python snippet as follows:

```
[boris@ ~/helpdesk]$ vi /tmp/reason.json
[boris@ ~/helpdesk]$ cat /tmp/reason.json | python3 -c "
   import json, sys
   d = json.load(sys.stdin)
   r = d['results'][0]
   print(r['response_text'])
"
I'll begin diagnosing why faulttest-db is unavailable and following the Checkpoint & bgwriter Triage playbook. Let me start with a connectivity check.
Good — the database is reachable. Now I'll follow the playbook: STEP 1 is to confirm checkpoint warnings in the log, STEP 2 is bgwriter stats, STEP 3 is parameter review, and STEP 4 is recommendations. Let me gather the evidence.
Good — database is up. Now let me get bgwriter settings and additional checkpoint configuration:
Excellent. Let me also check for more checkpoint parameters:
Now I have comprehensive data. Let me analyze the findings:

## Analysis

**STEP 1 — Checkpoint warnings confirmed:**
The log shows checkpoint_warning was set to 10 seconds, and there are deliberate forced checkpoints being executed every second (03:45:36, 03:45:37 in the log). The checkpoint_warning default is 30 seconds, but it was lowered to 10 seconds   as a test parameter from the configuration file.

**STEP 2 — bgwriter stats analysis:**
From `get_bgwriter_stats`:
- **checkpoints_req: 34** vs **checkpoints_timed: 42** — This is nearly balanced, indicating that roughly equal numbers of checkpoints are being triggered by WAL volume vs. scheduled time. However, over the lifetime this shows significant    forced checkpoints.
- **maxwritten_clean: 0** — bgwriter is NOT hitting its per-round page limit. This is good.
- **buffers_backend: 25,515** vs **buffers_checkpoint: 28,848** — The ratio is significant: 25515 / (28848 + 0 + 25515) = 47%, meaning nearly half of all dirty page writes are coming from backend processes rather than checkpoint/bgwriter.    This is a **red flag**.
- **buffers_backend_fsync: 0** — Backends are not doing their own fsyncs, so the situation is not severe yet.
- **buffers_clean: 0** — bgwriter clean writes are zero, which is unexpected. This correlates with a very low **bgwriter_lru_maxpages: 2**, which is far below the default of 100.

**STEP 3 — Parameter misconfiguration identified:**
The critical misconfiguration is:
- **bgwriter_lru_maxpages = 2** (should be default 100 or higher for write workloads)
  - At only 2 pages per round with a bgwriter_delay of 200ms, bgwriter can only flush ~10 pages/second before throttling.
  - This forces backends to write dirty pages themselves, inflating buffers_backend.
  - This causes unpredictable I/O latency during query execution.

- **checkpoint_warning = 10 seconds** (default is 30 seconds)
  - This is a **test parameter** set deliberately to trigger warnings more frequently.
  - It does not affect checkpoint frequency, only the warning threshold.

- **max_wal_size = 1024 MB** (default, not reduced)
  - This is at the default and has not been a problem historically.

- **checkpoint_completion_target = 0.9** (good — smooths checkpoint I/O)

- **checkpoint_timeout = 300s** (good — scheduled checkpoints are normal)

**Root cause:** The database is experiencing elevated write latency because bgwriter is severely throttled by `bgwriter_lru_maxpages = 2`. This forces backends to perform their own writes during query execution, causing unpredictable I/O     bursts. The checkpoint_warning parameter at 10 seconds is just a diagnostic trigger and not the cause of performance degradation.

HYPOTHESIS_1: bgwriter_lru_maxpages is set to 2, which is far below the default of 100, throttling the background writer and forcing backends to write 47% of dirty pages themselves | CONFIDENCE: 0.95 | EVIDENCE: "buffers_backend: 25515" out  of total 54363 buffer writes, and "bgwriter_lru_maxpages setting 2 unit default_value 100"
HYPOTHESIS_2: max_wal_size is too small and causing excessive forced checkpoints | CONFIDENCE: 0.05 | REJECTED: checkpoints_req (34) and checkpoints_timed (42) are nearly balanced, and max_wal_size is at default 1GB, which is appropriate for the observed write volume
ROOT_CAUSE: HYPOTHESIS_1
```

Please note that this is just one field from the full JSON, which is `response_text`. The others fields may also be instructive, in particular `judge_reasoning` (if a test runs with the LLM-as-Judge option, see [here](https://github.com/borisdali/helpdesk/blob/main/docs/LLM_AS_JUDGE.md) for details).

Also note the structured way of presenting the diagnosis: the step-by-step analysis, the list of hypotheses supported by evidence and the confidence score, the reason for rejecting the alternative hypotheses and choosing one as the root cause (there happened to be no escalation section in this particular example, but you'll see it in many others where the RCA is inconclusive).


## Crystal Ball diagnosis

This is where we set `HELPDESK_CRYSTAL_BALL=true`. The rest is exactly the same. Once the Gateway is restarted with this env var set, run the same fault injection test in exactly the same way and review the results:

```
[boris@ ~/helpdesk]$ date; HELPDESK_CRYSTAL_BALL=true \ <-- do NOT set this var for the real, non-Crystal Ball run
TEST_DB_PASSWORD=<your DB password> \
...
Thu May 14 12:33:41 EDT 2026
time=2026-05-14T12:33:41.435-04:00 level=INFO msg="helpdesk gateway" version=dev
time=2026-05-14T12:33:41.435-04:00 level=WARN msg="⚠️   CRYSTAL-BALL MODE ENABLED — playbook guidance, hypothesis format, and escalation chaining are bypassed. NOT recommended for production use."
...


[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest run \
>       --ids db-checkpoint-warning \
>       --conn "host=localhost port=15432 dbname=testdb user=postgres password=testpass" \
>       --agent-conn faulttest-db \
>       --via-gateway \
>       --gateway http://localhost:8080 \
>       --api-key <your API key for static authn> \
>       --purpose diagnostic \
>       --judge --judge-api-key $API_KEY --judge-vendor anthropic --judge-model claude-haiku-4-5-20251001
Thu May 14 12:34:45 EDT 2026
time=2026-05-14T12:34:46.325-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-05-14T12:34:46.325-04:00 level=INFO msg="LLM judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: Checkpoint warnings — bgwriter overload (db-checkpoint-warning) ---
time=2026-05-14T12:34:46.325-04:00 level=INFO msg="injecting failure" id=db-checkpoint-warning type=sql mode=internal
time=2026-05-14T12:34:52.372-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-checkpoint-warning series_id=pbs_checkpoint_bgwriter_triage playbook_id=pb_53cd6324 gateway=http://localhost:8080
time=2026-05-14T12:35:20.006-04:00 level=WARN msg="⚠  crystal-ball mode active on gateway — playbook scaffolding is bypassed; this result measures unguided LLM capability only"
time=2026-05-14T12:35:21.861-04:00 level=INFO msg="tearing down failure" id=db-checkpoint-warning type=sql
Diagnostic Result:   [FAIL] score=60% (keywords=100% tools=100% judge=0%)

=== Fault Test Report: 83f92581 ===

[FAIL] Checkpoint warnings — bgwriter overload (db-checkpoint-warning) - score: 60% [judge: 0%]
       Keywords: 100% | Tools: 100% | Judge: 0%
       Reasoning: "Agent diagnosed a false root cause (checkpoint_warning misconfiguration) when the actual fault is bgwriter_lru_maxpages=2 causing bgwriter starvation and elevated backend writes; the agent reported zero maxwritten_clean
events when this should have been elevated, suggesting data misinterpretation or incorrect tool output parsing."

--- Summary ---
Total: 1 | Passed: 0 | Failed: 1 | Rate: 0%
  database: 0/1 (0%)

LLM judge scored diagnosis for 1 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-83f92581.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "83f92581",
  "timestamp": "2026-05-14T16:35:21Z",
  "results": [
    {
      "failure_id": "db-checkpoint-warning",
      "failure_name": "Checkpoint warnings — bgwriter overload",
      "category": "database",
      "score": 0.6000000000000001,
      "passed": false,
      "keyword_pass": true,
      "diagnosis_pass": false,
      "tool_evidence": true,
      "tool_evidence_mode": "structured",
      "response_text": "I'll help you investigate the elevated write latency and checkpoint warnings on faulttest-db. Let me start by checking the connection and then gathering diagnostics on checkpoint and I/O health.\nGood! The database
is reachable. Now let me check more details on WAL settings, database activity, and vacuum health to get a complete picture:\nLet me fix the log read call and also get more details on the actual disk I/O profile:\nPerfect! Now I have a
clear picture. Let me analyze the findings:\n\n## 📊 **DIAGNOSIS: Checkpoint Warning Configuration Issue Causing Perceived Elevated Write Latency**\n\n### **Findings:**\n\n#### **1. Checkpoint \u0026 Background Writer I/O Statistics**\n-
**48 timed checkpoints** vs. **36 requested (forced) checkpoints** — a 1.33:1 ratio indicates high checkpoint pressure\n- **Checkpoint write time: 268.8 seconds cumulative** across all checkpoints — averaging ~3.6 seconds per checkpoint
since startup\n- **28,039 buffers flushed by backends** vs. 31,383 by checkpoints — backends are doing significant I/O directly, suggesting checkpoint intervals are allowing buffers to accumulate between checkpoints\n- **Zero
`maxwritten_clean` events** — the bgwriter is not being throttled, good news\n- **Zero `buffers_backend_fsync`** — backends aren't being forced to do fsync, so they're not completely stalled\n\n#### **2. Configuration Root Cause**\nThe
PostgreSQL log reveals **artificial checkpoint_warning value oscillation** that explains the warnings:\n- Log shows `checkpoint_warning` being set to `'10s'`, `'3600s'`, and reset to default (`'30s'`)\n- **With a 10-second warning
threshold**, any checkpoint sequence completing in \u003e 10 seconds triggers warnings — even normal ones\n- **Default is 30 seconds** — appropriate for most workloads\n\nThe **critical clue**: Checkpoint write times are ~0.71–3.2 seconds
per checkpoint — well within normal bounds. The **warnings are false positives** caused by an overly aggressive `checkpoint_warning` setting.\n\n#### **3. Write Pattern Evidence**\n- **Large table: `_chkpt_gen`** with 300,000 rows taking
20 MB disk space — this is the workload being tested\n- **Very high cache hit ratio (99.99%)** — memory management is excellent\n- **Heavy write activity**: 3.3M rows inserted since startup, excellent commit rate (879 commits, only 1
rollback)\n- **Checkpoint sequence pattern** shows ~29 KB WAL distance per checkpoint — efficient WAL generation\n\n#### **4. Why Write Latency Appears Elevated**\n- **Frequent checkpoints** (1 every ~10–15 minutes historically) cause
periodic fsync bursts\n- **Multiple sync files** per checkpoint (27–30 files) mean I/O is spread across WAL and data files\n- Checkpoint **sync time is minimal** (0.28s total cumulative) but **write time dominates** (268.8s cumulative)\n-
This creates **bursty I/O patterns** even though individual checkpoint write times are acceptable (~20–40ms)\n\n---\n\n### **ROOT CAUSE**\n\nThe `checkpoint_warning` parameter is **intentionally misconfigured** (set to `'10s'` at various
points in the log). This hypersensitive setting triggers warnings for **any checkpoint taking \u003e 10 seconds**, even though:\n- Actual checkpoint write duration is 0.7–3.2 seconds (normal)\n- Sync duration is 0.004–0.017 seconds (very
fast)\n- **Total checkpoint time is 3–45ms** — excellent performance\n\nThe warnings are **not reflective of actual system problems** but rather a **threshold tuning issue**.\n\n---\n\n### **RECOMMENDATIONS**\n\n1. **Immediate: Reset
`checkpoint_warning` to default (30 seconds)**\n   ```sql\n   ALTER SYSTEM RESET checkpoint_warning;\n   SELECT pg_reload_conf();\n   ```\n   This will eliminate false-positive warnings while maintaining real alerting.\n\n2. **Performance
Optimization: Increase `checkpoint_timeout` if checkpoint frequency is actually a problem**\n   - Current tuning allows checkpoints every ~10–15 minutes (time-based)\n   - If write latency concerns persist, increase `checkpoint_timeout`
from default 5 minutes\n   - Monitor WAL volume between checkpoints; if stable under 1 GB, increase `max_wal_size`\n\n3. **Monitor actual metrics rather than warnings**\n   - **Checkpoint write time** (currently 0.7–3.2s) — acceptable\n
- **Checkpoint sync time** (currently 0.004–0.017s per file) — excellent\n   - **Backend fsync count** (currently 0) — no backend stalls, perfect\n   - Focus on these metrics rather than the warning threshold\n\n4. **Optional: If you
actually want tighter checkpoint monitoring**\n   - Set `checkpoint_warning = '30s'` (default) to alert only on genuinely slow checkpoints\n   - Investigate only if individual checkpoints exceed 30 seconds\n\n---\n\n**Conclusion**: Your
database is performing well. The frequent checkpoint warnings are a **configuration artifact**, not a system problem. Reset the threshold and monitor actual I/O metrics instead.",
      "duration": "28.04719075s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 0,
      "judge_reasoning": "Agent diagnosed a false root cause (checkpoint_warning misconfiguration) when the actual fault is bgwriter_lru_maxpages=2 causing bgwriter starvation and elevated backend writes; the agent reported zero
maxwritten_clean events when this should have been elevated, suggesting data misinterpretation or incorrect tool output parsing.",
      "judge_model": "claude-haiku-4-5-20251001",
      "overall_score": 0.6000000000000001
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

[FAIL] Checkpoint warnings — bgwriter overload (db-checkpoint-warning) - score: 60% ⚠ [crystal-ball] [judge: 0%]
```

## Diagnosis comparison: unconstrained Crystal Ball vs. guided, playbook driven aiHelpDesk

```
  ┌────────────────────────────────┬───────┬───────┬────────────────────────────────┐
  │              Mode              │ Score │ Judge │           Root Cause           │
  ├────────────────────────────────┼───────┼───────┼────────────────────────────────┤
  │ Guided (playbook)              │ 100%  │ 100%  │ bgwriter_lru_maxpages=2 ✓      │
  ├────────────────────────────────┼───────┼───────┼────────────────────────────────┤
  │ Crystal Ball (Gemini unguided) │ 60%   │ 0%    │ checkpoint_warning threshold ✗ │
  └────────────────────────────────┴───────┴───────┴────────────────────────────────┘
```

It may be worth pointing out that the mechanism of failure is also instructive: The Crystal Ball model saw `maxwritten_clean=0` and interpreted it as "bgwriter is not throttled, good news". Cool, except that... the exact opposite of correct. The checkpoint_warning=10s noise dominated its attention and it never synthesized `buffers_backend=28k + bgwriter_lru_maxpages=2 + buffers_clean=0` into the real root cause. Compare it to the structured aiHelpDesk diagnosis where the playbook scaffolding forced that synthesis.

## Running on K8s (via a Helm Chart)

The examples above, both via the structured aiHelpDesk guided playbook and via Crystal Ball, were done from the code source. Here's how the invocation of this fault test looks like on K8s. In the example below I ran the same `db-checkpoint-warning` failure scenario against the CNPG cluster called `pg-cluster-minikube` deployed in a `ns=db` on Minikube:

```
helm upgrade helpdesk . --namespace helpdesk-system \
  --set faulttest.enabled=true \
  --set faulttest.ids=db-checkpoint-warning \
  --set faulttest.judge=true \
  --set faulttest.viaGateway=true \
  --set faulttest.gatewayAPIKeySecret=<your API key> \
  --set faulttest.conn=<a fully qualified conn string or an alias from your infra.json file> \
  --set 'infrastructure.db_servers.pg-cluster-minikube.connection_string=host=pg-cluster-minikube-rw.db.svc.cluster.local port=5432 dbname=app user=app' \
  --set 'infrastructure.db_servers.pg-cluster-minikube.password_env=CNPG_APP_PASSWORD' \
  --set 'faulttest.extraEnv[0].name=CNPG_APP_PASSWORD' \
  --set 'faulttest.extraEnv[0]. valueFrom.secretKeyRef.name=pg-cluster-minkube-app' \
  --set 'faulttest.extraEnv[0].valueFrom.secretKeyRef.key=password' \
  --set image.pullPolicy=Never \
  --set image.tag=v0.13.0-a71a4ba \
  --timeout 15m \
  --set gateway.crystalBall=true \	<-- that's the setting you want to flip to false (or remove) for the normal, guided aiHelpDesk diagnosis
  --set faulttest.targetNamespace=db 
```

## Reproducability and differences in scoring
It's worth pointing out that the original listings and the comparison table were obtained by running the tests from the source against a PostgreSQL 16 database server running inside a Docker container and diagnosed by Haiku Anthropic models use by both the agents and the judge. In testing, we found that the version of PostgreSQL, the differences in models, perhaps the hosting platform, but most importantly the "mood" of the models affect the diagnosis significantly. But only the Crystal Ball diagnosis. 

The structured, guided aiHelpDesk diagnosis remains stable irrespective of all those deployment and environmenental factors. This, on its own, is a very important finding because consistency is the key for building a trust in AI-driven diagnosis.

> aiHelpDesk features a fully-fledged, 8-module [AI Governance](AIGOVERNANCE.md) suite as the basis for building the user trust, so that ALL decisions can be fully traced, explained and survive the most stringent audit. 
> But this is after the fact.  **The consistency in diagnosis** that comes from the structured guidance is another important piece in the trust puzzle.
