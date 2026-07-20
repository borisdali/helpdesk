# aiHelpDesk Sample#11 (on K8s): 3D Consistency Cert

The sample commands and deliberations presented below complement this two blog post: 

- **[Trust Has Three Dimensions. Demand from your AI SRE vendor to show a cert with all three.](https://itnext.io/trust-has-three-dimensions-demand-from-your-ai-sre-vendor-to-show-a-cert-with-all-three-0f361a443807#bf32)**
  Your Agent got it right. Can you prove that it knew why? The cert may be telling the truth, but does it have enough depth to back up that claim?

For background on aiHelpDesk Consistency Certification badge, see [here](../CONSISTENCY.md). aiHelpDesk [release 0.21](https://github.com/borisdali/helpdesk/releases/tag/v0.21.0) makes the certs 3D. See [here](../ATTRIBUTION_CERTS.md) for background on attribution-aware certs.

If you need to step back even further, start with the [Vault](../VAULT.md). Next, head over to [this page](../VAULT_METRICS.md) to see how aiHelpDesk turns your [Incident](../INCIDENTS.md) data into a learning signal.

For more context, aiHelpDesk Fault Injection Testing is well documented [here](../FAULTTEST.md), with multiple [examples availble](../FAULTTEST_SAMPLE.md) on [K8s](SAMPLE005.md), [Docker/Podman](SAMPLE006.md) and on a [host/VM](SAMPLE007.md). 

---

The sample commands posted below are broken into two parts and are shown for running aiHelpDesk on K8s, but similar samples of running aiHelpDesk on host/VM directly and inside Docker/Podman containers are available [here](SAMPLE010.md) and [here](SAMPLE009.md) respectively (although not the exact commands shown on this page).

## The Demo: two faults, same playbook, different diagnosis!

Here's the skinny on the 3D certs demo (attribution-aware certs, see above for context):

From the catalog, it's clear that there are two faults that share the same triage playbook (`pbs_k8s_pod_crash_triage`):

- k8s-crashloop — application error, repeated crash
- k8s-oomkilled — memory limit exceeded, kernel killed

Same playbook, two different correct attributions. That's the exact setup attribution-aware certs are designed to surface.
The wrong attribution has real operational consequences, not just a scoring penalty:

- OOMKilled:
  Wrong action if attributed as CrashLoop: you go read app logs, find nothing, restart the pod, it dies in 5 minutes.
  Memory limit is never raised.

- CrashLoop:
  Wrong action if attributed as OOMKill: you increase memory limits, restart, crash continues.
  You've done nothing useful.

Both faults could be surfaced as "pod is not running."
The agent runs the same playbook on both.
Attribution-aware certs tell you whether it consistently says the right thing about WHY. Not that it just PASSED.
That's the story.

## The Demo Script

1/ Run `k8s-oomkilled` 3× → cert shows `attribution=oom-kill (3/3) STABLE`
2/ Run `k8s-crashloop` 3× → cert shows `attribution=process-error (3/3) STABLE`
3/ `cert-compare` across the two:
   Same playbook version, same outcome (PASS), different attribution labels, which proves that the cert is capturing what story the agent told, not just whether it passed


### `k8s-oomkilled` fault injection test (on K8s)

```
[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest run \
       --ids k8s-oomkilled \
       --repeat 3 \
       --agent-model claude-sonnet-4-6 \
       --judge --judge-vendor anthropic \
       --judge-model claude-haiku-4-5-20251001 \
       --judge-api-key $ANTHROPIC_API_KEY \
       --via-gateway \
       --gateway $HELPDESK_GATEWAY_URL \
       --api-key $HELPDESK_CLIENT_API_KEY \
       --approval-mode force \
       --operator alice@example.com 
       --users-file users.example.yaml

Thu Jul 16 18:50:40 EDT 2026
time=2026-07-16T18:50:43.211-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-07-16T18:50:43.211-04:00 level=INFO msg="LLM diagnosis judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: OOMKilled (k8s-oomkilled) — 3 runs ---

  Run 1/3
time=2026-07-16T18:50:43.211-04:00 level=INFO msg="injecting failure" id=k8s-oomkilled type=kustomize mode=internal conn=""
time=2026-07-16T18:50:43.462-04:00 level=INFO msg="waiting after injection" duration=30s
time=2026-07-16T18:51:14.615-04:00 level=INFO msg="sending prompt to agent via playbook" failure=k8s-oomkilled series_id=pbs_k8s_pod_crash_triage playbook_id=pb_9e5aca70 gateway=http://localhost:8080 agent-conn=""
time=2026-07-16T18:51:50.801-04:00 level=WARN msg="gateway warning" failure=k8s-oomkilled warning="no connection_string specified — agent will need to ask which database to investigate"
  Feedback submitted (triage/post_incident run_id=plr_25a5eb1b)
time=2026-07-16T18:51:53.737-04:00 level=INFO msg="tearing down failure" id=k8s-oomkilled type=kustomize_delete conn=""
  [PASS] score=86%
         [PRIMARY 99%] PostgreSQL pod is OOMKilled due to severely undersized memory limit (10Mi)
         [99%] PostgreSQL pod is OOMKilled due to severely undersized memory limit (10Mi)
         [95%] Container memory limit of 10Mi is too low for PostgreSQL to start; process exceeds limit immediately on startup and is killed by OOM killer
         [REJECTED 5%] PostgreSQL has a genuine memory leak or runaway allocation — A memory leak would manifest after some runtime; here the pod is killed within seconds of startup before PostgreSQL finishes initialization, proving the      limit is simply too low for basic startup overhead.
         [REJECTED 1%] Kubernetes node pressure or insufficient cluster memory — get_events shows no node-level MemoryPressure or NodeNotReady events; only container-level OOMKilled signals
         [REJECTED 1%] Kubernetes node pressure or insufficient cluster memory — get_events shows no node-level MemoryPressure or NodeNotReady events; only container-level OOMKilled signals

  Run 2/3
time=2026-07-16T18:52:04.188-04:00 level=INFO msg="injecting failure" id=k8s-oomkilled type=kustomize mode=internal conn=""
time=2026-07-16T18:52:04.415-04:00 level=INFO msg="waiting after injection" duration=30s
time=2026-07-16T18:52:35.153-04:00 level=INFO msg="sending prompt to agent via playbook" failure=k8s-oomkilled series_id=pbs_k8s_pod_crash_triage playbook_id=pb_9e5aca70 gateway=http://localhost:8080 agent-conn=""
time=2026-07-16T18:53:28.616-04:00 level=WARN msg="gateway warning" failure=k8s-oomkilled warning="no connection_string specified — agent will need to ask which database to investigate"
  Feedback submitted (triage/post_incident run_id=plr_c0be6121)
time=2026-07-16T18:53:31.821-04:00 level=INFO msg="tearing down failure" id=k8s-oomkilled type=kustomize_delete conn=""
  [PASS] score=100%
         [PRIMARY 99%] PostgreSQL container killed by OOM killer due to memory limit of 10Mi being insufficient for process startup
         [99%] Memory limit is critically undersized (10Mi), causing the kernel to OOMKill the PostgreSQL process on every startup attempt
         [REJECTED 1%] Data corruption or filesystem issue preventing startup — Pod logs show clean startup sequence (log redirection) before crash; OOMKilled termination at container level proves kernel kill, not process error
         [REJECTED 1%] A transient startup error unrelated to memory would explain the crashes — The exit_code=137 is definitively kernel OOMKill, not a process error; 10Mi is demonstrably insufficient for any PostgreSQL workload

  Run 3/3
time=2026-07-16T18:53:42.229-04:00 level=INFO msg="injecting failure" id=k8s-oomkilled type=kustomize mode=internal conn=""
time=2026-07-16T18:53:42.448-04:00 level=INFO msg="waiting after injection" duration=30s
time=2026-07-16T18:54:13.311-04:00 level=INFO msg="sending prompt to agent via playbook" failure=k8s-oomkilled series_id=pbs_k8s_pod_crash_triage playbook_id=pb_9e5aca70 gateway=http://localhost:8080 agent-conn=""
time=2026-07-16T18:54:47.841-04:00 level=WARN msg="gateway warning" failure=k8s-oomkilled warning="no connection_string specified — agent will need to ask which database to investigate"
  Feedback submitted (triage/post_incident run_id=plr_a03b8661)
time=2026-07-16T18:54:50.104-04:00 level=INFO msg="tearing down failure" id=k8s-oomkilled type=kustomize_delete conn=""
  [PASS] score=73%
         [PRIMARY 99%] ** PostgreSQL pod is being killed by the OOM killer because the memory limit is set to an impossibly low value (10Mi), preventing PostgreSQL from initializing
         [95%] Pod memory limit is too low (10Mi); PostgreSQL cannot allocate sufficient memory during startup and is killed by the OOM killer on every restart
         [REJECTED 5%] PostgreSQL has a memory leak causing rapid heap exhaustion after startup — OOMKilled with 10Mi limit leaves no room for even healthy startup; limit is the immediate cause, not a leak
         [REJECTED 1%] ** PostgreSQL process has a genuine memory leak — The pod has only restarted 3 times over ~40 seconds; a memory leak would not manifest this quickly at initialization, and the memory limit of 10Mi is far too low for    any PostgreSQL version to even start
time=2026-07-16T18:55:01.326-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (3 runs):
    Pass rate:    3/3 (100%)
    Confidence:   min=99% max=99% range=0pp mean=99%  (H1, passing runs only)
    Verdict:      STABLE
    Attribution:  oom-kill (3/3)  consistent: yes  [taxonomy 1.0]
  ────────────────────────────────────────────────────────────────
time=2026-07-16T18:55:03.594-04:00 level=INFO msg="fault stability cert posted" fault_id=k8s-oomkilled verdict=STABLE n_runs=3

=== Fault Test Report: b3cf5e7c ===

[PASS] OOMKilled (k8s-oomkilled) - score: 86% [judge: 100%]
       Keywords: 100% | Tools: 66% | Judge: 100%
       Reasoning: "Agent correctly identified exit_code=137 and OOMKilled as the root cause, distinguished it from memory leaks (noting immediate startup failure), ruled out node-level pressure, and pinpointed the 10Mi limit as the resource  misconfiguration causing PostgreSQL to fail on initialization—matching all key points in the expected diagnosis."
[PASS] OOMKilled (k8s-oomkilled) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Reasoning: "Agent correctly identified exit_code=137 and OOMKilled as the termination reason, inspected container memory limit (10Mi), diagnosed it as critically undersized for PostgreSQL startup, explicitly ruled out memory leak and  node pressure, and provided evidence from all relevant tool outputs (get_pods, get_pod_resources, get_pod_logs, get_events)."
[PASS] OOMKilled (k8s-oomkilled) - score: 73% [judge: 100%]
       Keywords: 100% | Tools: 33% | Judge: 100%
       Reasoning: "Agent correctly identified exit_code=137 and OOMKilled as the root cause, pinpointed the 10Mi memory limit as critically undersized for PostgreSQL startup, explicitly rejected the memory leak hypothesis with sound          reasoning (pod dies on startup before leak could develop), and correctly attributed the issue to resource misconfiguration rather than node pressure or code defects."

--- Summary ---
Total: 3 | Passed: 3 | Failed: 0 | Rate: 100%
  kubernetes: 3/3 (100%)

LLM judge scored diagnosis for 3 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-b3cf5e7c.json
--- BEGIN FAULTTEST REPORT JSON ---
...
--- END FAULTTEST REPORT JSON ---
Evaluation scores posted to auditd: 3/3 runs

real    4m26.215s
user    0m2.779s
sys     0m0.860s
```

Judge is firing now and all three runs scored 100% semantically. The cert is clean:

```
  - STABLE · attribution=oom-kill (3/3) · taxonomy 1.0 ✓
  - Judge: 100% / 100% / 100% — diagnosis consistently correct
  - Overall: 86% / 100% / 73% — all variance is tool coverage, not diagnosis quality
```

The score spread comes entirely from run 3:
The agent hit ERROR — `get_pod_resources` failed: unexpected additional properties ["pod"].
It hallucinated a pod parameter that isn't in the tool schema, so the tool call didn't register as evidence (`tool_score=0.33`, only `get_pods` counted).
Despite all of that, it still arrived at the correct diagnosis from the data it did retrieve.

And that's a very important signal!
The judge/attribution machinery correctly classifies this run as `oom-kill` with 100% semantic correctness even though the structured tool score is low.
Without the judge, that run would look like a near-failure (73% with keyword+tool weighting).
With the judge, it's correctly identified as a diagnosis that got the right answer (althrough through a suboptimal path).


### `k8s-crashloop` fault injection test (on K8s)

```
[boris@ ~/helpdesk]$ date; time go run ./testing/cmd/faulttest run \
       --ids k8s-crashloop \
       --repeat 3 \
       --agent-model claude-sonnet-4-6 \
       --judge --judge-vendor anthropic \
       --judge-model claude-haiku-4-5-20251001 \
       --judge-api-key $ANTHROPIC_API_KEY \
       --via-gateway \
       --gateway $HELPDESK_GATEWAY_URL \
       --api-key $HELPDESK_CLIENT_API_KEY \
       --approval-mode force \
       --operator alice@example.com 
       --users-file users.example.yaml

Thu Jul 16 19:19:52 EDT 2026
time=2026-07-16T19:19:52.426-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-07-16T19:19:52.426-04:00 level=INFO msg="LLM diagnosis judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: CrashLoopBackOff (k8s-crashloop) — 3 runs ---

  Run 1/3
time=2026-07-16T19:19:52.426-04:00 level=INFO msg="injecting failure" id=k8s-crashloop type=kustomize mode=internal conn=""
time=2026-07-16T19:19:52.668-04:00 level=INFO msg="waiting after injection" duration=30s
time=2026-07-16T19:20:23.868-04:00 level=INFO msg="sending prompt to agent via playbook" failure=k8s-crashloop series_id=pbs_k8s_pod_crash_triage playbook_id=pb_9e5aca70 gateway=http://localhost:8080 agent-conn=""
time=2026-07-16T19:21:21.751-04:00 level=WARN msg="gateway warning" failure=k8s-crashloop warning="no connection_string specified — agent will need to ask which database to investigate"
  Feedback submitted (triage/post_incident run_id=plr_8b4e223c)
time=2026-07-16T19:21:25.770-04:00 level=INFO msg="tearing down failure" id=k8s-crashloop type=kustomize_delete conn=""
  [PASS] score=100%
         [PRIMARY 98%] PostgreSQL configuration is invalid, preventing startup (FATAL: invalid configuration error during startup indicates a malformed parameter or syntax error in postgresql.conf or command-line args)
         [95%] PostgreSQL startup fails due to invalid configuration parameters in postgresql.conf or environment variables that are incompatible with PostgreSQL 16
         [REJECTED 5%] Corrupted or missing configuration file preventing PostgreSQL initialization — The error message is specific ("invalid configuration") not "file not found" or "permission denied"; corrupted files typically produce      parse errors, not generic invalidity.
         [REJECTED 2%] Kubernetes probe misconfiguration is causing premature pod termination — Pod exits with exit code 1 before any probe could run; the crash log directly shows PostgreSQL FATAL error, not a probe timeout

  Run 2/3
time=2026-07-16T19:21:36.372-04:00 level=INFO msg="injecting failure" id=k8s-crashloop type=kustomize mode=internal conn=""
time=2026-07-16T19:21:36.600-04:00 level=INFO msg="waiting after injection" duration=30s
time=2026-07-16T19:22:07.135-04:00 level=INFO msg="sending prompt to agent via playbook" failure=k8s-crashloop series_id=pbs_k8s_pod_crash_triage playbook_id=pb_9e5aca70 gateway=http://localhost:8080 agent-conn=""
time=2026-07-16T19:22:52.417-04:00 level=WARN msg="gateway warning" failure=k8s-crashloop warning="no connection_string specified — agent will need to ask which database to investigate"
  Feedback submitted (triage/post_incident run_id=plr_6a72a9a5)
time=2026-07-16T19:22:55.553-04:00 level=INFO msg="tearing down failure" id=k8s-crashloop type=kustomize_delete conn=""
  [PASS] score=100%
         [PRIMARY 95%] PostgreSQL configuration is invalid (invalid parameter in postgresql.conf, postgresql.auto.conf, or environment variables) causing immediate startup failure
         [95%] PostgreSQL startup fails due to invalid configuration in postgresql.conf or environment setup
         [REJECTED 5%] Configuration file is corrupted or unreadable by PostgreSQL process — Would typically show "could not open file" or "permission denied" rather than "invalid configuration"
         [REJECTED 5%] Configuration issue is transient and a restart will recover the pod — The same error appears consistently across 3 restart attempts, indicating a persistent configuration problem rather than a transient fault

  Run 3/3
time=2026-07-16T19:23:05.960-04:00 level=INFO msg="injecting failure" id=k8s-crashloop type=kustomize mode=internal conn=""
time=2026-07-16T19:23:06.175-04:00 level=INFO msg="waiting after injection" duration=30s
time=2026-07-16T19:23:38.432-04:00 level=INFO msg="sending prompt to agent via playbook" failure=k8s-crashloop series_id=pbs_k8s_pod_crash_triage playbook_id=pb_9e5aca70 gateway=http://localhost:8080 agent-conn=""
time=2026-07-16T19:24:42.132-04:00 level=WARN msg="gateway warning" failure=k8s-crashloop warning="no connection_string specified — agent will need to ask which database to investigate"
  Feedback submitted (triage/post_incident run_id=plr_9a8721f0)
time=2026-07-16T19:24:44.090-04:00 level=INFO msg="tearing down failure" id=k8s-crashloop type=kustomize_delete conn=""
  [PASS] score=100%
         [PRIMARY 95%] PostgreSQL startup failed due to invalid configuration syntax in postgresql.conf or invalid command-line parameters
         [95%] PostgreSQL startup fails due to an invalid configuration parameter in the pod's environment or postgresql.conf file
         [REJECTED 5%] Corrupted configuration file from incomplete volume mount or persistence layer — Events show volume provisioning succeeded and container started normally; configuration error is more likely parsing/syntax issue than    corruption
         [REJECTED 5%] Transient startup race condition — The error persists identically across 4 container restart attempts over 40 seconds, ruling out transient timing issues.
time=2026-07-16T19:24:55.218-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (3 runs):
    Pass rate:    3/3 (100%)
    Confidence:   min=95% max=98% range=3pp mean=96%  (H1, passing runs only)
    Verdict:      STABLE
    Attribution:  process-error (3/3)  consistent: yes  [taxonomy 1.0]
  ────────────────────────────────────────────────────────────────
time=2026-07-16T19:24:57.529-04:00 level=INFO msg="fault stability cert posted" fault_id=k8s-crashloop verdict=STABLE n_runs=3

=== Fault Test Report: 36cb819e ===

[PASS] CrashLoopBackOff (k8s-crashloop) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Reasoning: "Agent correctly identified CrashLoopBackOff with exit_code=1 as a process-level crash (not OOM or scheduling issue), retrieved container logs to find the 'FATAL: invalid configuration' error, and diagnosed this as a        PostgreSQL startup configuration failure rather than a transient or resource issue, fully addressing all key points in the expected diagnosis."
[PASS] CrashLoopBackOff (k8s-crashloop) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Reasoning: "Agent correctly identified CrashLoopBackOff with exit_code=1 as a process-level crash (not OOM or scheduling issue), retrieved container logs showing 'FATAL: invalid configuration' error, and diagnosed the root cause as a  PostgreSQL configuration problem with high confidence, addressing all key points in the expected diagnosis."
[PASS] CrashLoopBackOff (k8s-crashloop) - score: 100% [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Reasoning: "Agent correctly identified CrashLoopBackOff with exit_code=1 as a process-level crash (not OOM or scheduling issue), retrieved container logs showing 'FATAL: invalid configuration', and diagnosed the root cause as invalid  PostgreSQL configuration with high confidence (0.95), addressing all key points in the expected diagnosis."

--- Summary ---
Total: 3 | Passed: 3 | Failed: 0 | Rate: 100%
  kubernetes: 3/3 (100%)

LLM judge scored diagnosis for 3 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-36cb819e.json
--- BEGIN FAULTTEST REPORT JSON ---
...
--- END FAULTTEST REPORT JSON ---
Evaluation scores posted to auditd: 3/3 runs

real    5m6.335s
user    0m1.797s
sys     0m0.664s
```

Clean sweep!
`k8s-crashloop` is `STABLE(3) · attribution=process-error (3/3)`.
Tool scores 100% on all three runs (no parameter errors this time, unlike `k8s-oomkilled`'s run 1 and 3). Judge 100% across the board.

The two certs are now posted:

```
  ┌───────────────┬─────────────────────┬──────────────────────────┬──────┬───────┐
  │     Fault     │     Attribution     │         Playbook         │ Runs │ Judge │
  ├───────────────┼─────────────────────┼──────────────────────────┼──────┼───────┤
  │ k8s-oomkilled │ oom-kill (3/3)      │ pbs_k8s_pod_crash_triage │ 3    │ 100%  │
  ├───────────────┼─────────────────────┼──────────────────────────┼──────┼───────┤
  │ k8s-crashloop │ process-error (3/3) │ pbs_k8s_pod_crash_triage │ 3    │ 100%  │
  └───────────────┴─────────────────────┴──────────────────────────┴──────┴───────┘
```

So the same playbook, taxonomy 1.0, different correct attributions... and that's our demo.
Running `vault cert-compare` to see them side by side is the next release:

```
  go run ./testing/cmd/faulttest vault cert-compare k8s-oomkilled k8s-crashloop --gateway $GW --api-key $HELPDESK_CLIENT_API_KEY
```


### The results are in!

```
[boris@ /tmp/helpdesk/helpdesk-v0.21.0-deploy/helm/helpdesk]$ k delete pod vault-cert-compare -nhelpdesk-system
pod "vault-cert-compare" deleted

[boris@ /tmp/helpdesk/helpdesk-v0.21.0-deploy/helm/helpdesk]$ kubectl run vault-list-k8s \
     --image=ghcr.io/borisdali/helpdesk:v0.21.0 \
     --image-pull-policy=Never \
     --restart=Never \
     --namespace=helpdesk-system \
     -- faulttest vault list \
        --ids k8s-oomkilled,k8s-crashloop \
        --gateway $HELPDESK_GATEWAY_URL
        --api-key $HELPDESK_AI_KEY
        --external=false
pod/vault-list-k8s created

[boris@ /tmp/helpdesk/helpdesk-v0.21.0-deploy/helm/helpdesk]$ kubectl logs -n helpdesk-system vault-list-k8s
Gateway: $HELPDESK_GATEWAY_URL ·  version: v0.21.0-bf26835  ·  host: helpdesk-gateway-78467d45c4-rjg74

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
k8s-crashloop                    k8s        pbs_k8s_pod_crash_triage        pbs_k8s_pod_crash_remediate      (never)                STABLE(3) attr=process-error 6 runs  83% resolved  83% accurate  avg: 22s recovery  (system)
    diag  1.3   *  6r  0%  avg: 47s recovery
    diag  1.2      5r  0%  avg: 6s recovery
    remed → vault versions pbs_k8s_pod_crash_remediate
k8s-oomkilled                    k8s        pbs_k8s_pod_crash_triage        pbs_k8s_pod_crash_remediate      (never)                STABLE(3) attr=oom-kill 6 runs  83% resolved  83% accurate  avg: 22s recovery  (system)
    diag  1.3   *  6r  0%  avg: 47s recovery
    diag  1.2      5r  0%  avg: 6s recovery
    remed → vault versions pbs_k8s_pod_crash_remediate
```

Looking good. Both rows:

```
  k8s-crashloop  k8s  pbs_k8s_pod_crash_triage  …  STABLE(3) attr=process-error
  k8s-oomkilled  k8s  pbs_k8s_pod_crash_triage  …  STABLE(3) attr=oom-kill
```

Same DIAG PLAYBOOK column, different `attr=`, so the cert proves the agent is actually differentiating `exit_code=1` / `FATAL: invalid configuration from exit_code=137` / `OOMKilled`.
And not just passing because the keyword "CrashLoopBackOff" appears in both.

One thing worth noting: the INCIDENTS column shows shared stats (6 runs, 83% resolved) because both faults point to the same `pbs_k8s_pod_crash_triage` series.
All 6 diagnosis runs (3+3) pool into one playbook version row.
That's expected and it actually reinforces the point: same playbook, same run history, but the cert distinguishes what it concluded per fault.... (but let's see what feedback we get on this because it seems a potential point of contention).


### Where and how the 3D cert is visible?

Outcome:
STABLE(3) pass count. Always visible in vault list. ✓

Conclusion:
attr=oom-kill (3/3) / attr=process-error (3/3). Visible in vault list STABLE column. ✓ — and this is exactly what the demo just showed.

Evaluation (judge spread):
Computed, stored in the cert, and surfaced in vault accuracy when spread > 0 (Judge spread: X.XX).
With our 3/3 100% judge scores, spread is 0 so it doesn't print —
That's correct: "nothing to say when it's tight."
The signal appears when you have 100%/80%/60% variance across runs. ✓ implemented, just not visible when everything is perfect.

The taxonomy major-bump warning is also implemented in aiHelpDesk [release 0.21](https://github.com/borisdali/helpdesk/releases/tag/v0.21.0): `⚠ TAXONOMY MAJOR 1.0→2.0: attribution comparison invalid.` 

Here's the summary of the main 3D cert features and where they are visible:

```
   Full status:
  ┌─────────────────────────────────────┬────────┬─────────────────────────────────────────────────────────────┐
  │               Feature               │ Status │                        Visible where                        │
  ├─────────────────────────────────────┼────────┼─────────────────────────────────────────────────────────────┤
  │ Outcome stability                   │ ✓      │ STABLE(3) in vault list                                     │
  ├─────────────────────────────────────┼────────┼─────────────────────────────────────────────────────────────┤
  │ Conclusion stability                │ ✓      │ attr=oom-kill (3/3) in vault list                           │
  ├─────────────────────────────────────┼────────┼─────────────────────────────────────────────────────────────┤
  │ Evaluation stability (judge spread) │ ✓      │ vault accuracy when spread > 0; silent when tight (correct) │
  ├─────────────────────────────────────┼────────┼─────────────────────────────────────────────────────────────┤
  │ cert-compare attribution diff       │ ✓      │ attribution: oom-kill → process-error                       │
  ├─────────────────────────────────────┼────────┼─────────────────────────────────────────────────────────────┤
  │ Taxonomy major-bump warning         │ ✓      │ cert-compare when major version changes                     │
  └─────────────────────────────────────┴────────┴─────────────────────────────────────────────────────────────┘
```


### `vault list` (on K8s)

To wrap up this sample, here' an incomplete list of (external) incidents (real and injected) known to the vault:

```
[boris@ /tmp/helpdesk/helpdesk-v0.21.0-deploy/helm/helpdesk]$ kubectl run vault-list \
   --image=ghcr.io/borisdali/helpdesk:v0.21.0 \
   --image-pull-policy=Never \
   --restart=Never \
   --namespace=helpdesk-system \
   --faulttest vault list \
      --gateway $HELPDESK_GATEWAY_URL \
      --api-key $HELPDESK_CLIENT_API_KEY
pod/vault-list created

[boris@ /tmp/helpdesk/helpdesk-v0.21.0-deploy/helm/helpdesk]$ kubectl logs -n helpdesk-system vault-list

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.21.0-bf26835  ·  host: helpdesk-gateway-78467d45c4-rjg74

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-max-connections               any        pbs_connection_triage           pbs_connection_remediate         (never)                STABLE(3) attr=idle-connection-accumulation 13 runs  62% resolved  100% accurate  avg: 2.1 steps, 22s         recovery  (generated)
    diag  1.5   *  3r  0%  avg: 23s recovery
    diag  1.4      1r  100%  avg: 58s recovery  100% approach OK
    diag  → vault versions pbs_connection_triage
    remed 1.4   *  4r  75%  avg: 13s recovery
    remed 1.3      9r  100%  avg: 3.0 steps, 26s recovery
db-long-running-query            any        pbs_slow_query_triage           pbs_slow_query_remediate         (never)                STABLE(5) 24d  2 runs  100% resolved  100% accurate  avg: 4.0 steps, 10s recovery  (system)
    diag  → vault versions pbs_slow_query_triage
    remed → vault versions pbs_slow_query_remediate
db-lock-contention               any        pbs_lock_contention_triage      pbs_slow_query_remediate         (never)                UNSTABLE(5)    2 runs  100% resolved  –  avg: 4.0 steps, 10s recovery  (system)
    diag  → vault versions pbs_lock_contention_triage
    remed → vault versions pbs_slow_query_remediate
db-table-bloat                   any        pbs_vacuum_triage               pbs_vacuum_remediate             (never)                STABLE(5) 24d  11 runs  91% resolved  100% accurate  avg: 7.2 steps, 9m48s recovery  (system)
    diag  1.5   *  1r  100%  avg: 6m32s recovery  100% approach OK
    diag  1.4      9r  89%  avg: 1m26s recovery  100% approach OK
    diag  → vault versions pbs_vacuum_triage
    remed → vault versions pbs_vacuum_remediate
db-high-cache-miss               any        pbs_cache_miss_triage           pbs_cache_miss_remediate         (never)                STABLE(5) 24d  1 runs  100% resolved  –  avg: 4.0 steps, 11s recovery  (system)
    diag  → vault versions pbs_cache_miss_triage
    remed → vault versions pbs_cache_miss_remediate
db-connection-refused            any        pbs_db_restart_triage           pbs_db_restart_action            (never)                STABLE(3)      7 runs  71% resolved  89% accurate  avg: 18s recovery  (system)
    diag  1.7   *  20r  0%  avg: 1m39s recovery  100% approach OK
    diag  1.6      3r  0%  avg: 25s recovery
    diag  → vault versions pbs_db_restart_triage
    remed 1.1   *  2r  100%  avg: 13s recovery
    remed 1.0      5r  80%  avg: 20s recovery
db-idle-in-transaction           any        pbs_connection_triage           pbs_connection_remediate         (never)                STABLE(5) 24d  13 runs  62% resolved  100% accurate  avg: 2.1 steps, 22s recovery  (generated)
    diag  1.5   *  3r  0%  avg: 23s recovery
    diag  1.4      1r  100%  avg: 58s recovery  100% approach OK
    diag  → vault versions pbs_connection_triage
    remed 1.4   *  4r  75%  avg: 13s recovery
    remed 1.3      9r  100%  avg: 3.0 steps, 26s recovery
db-tx-lock-chain-blocker         any        pbs_lock_chain_triage           pbs_lock_chain_remediate         (never)                STABLE(5) 24d  3 runs  33% resolved  100% accurate  avg: 19.3 steps, 1m13s recovery  (system)
    diag  → vault versions pbs_lock_chain_triage
    remed → vault versions pbs_lock_chain_remediate
db-terminate-direct-command      any        -                               (none)                           NO PLAYBOOK            STABLE(5) 24d  -
db-vacuum-needed                 any        pbs_vacuum_triage               pbs_vacuum_remediate             (never)                STABLE(5)      11 runs  91% resolved  100% accurate  avg: 7.2 steps, 9m48s recovery  (system)
    diag  1.5   *  1r  100%  avg: 6m32s recovery  100% approach OK
    diag  1.4      9r  89%  avg: 1m26s recovery  100% approach OK
    diag  → vault versions pbs_vacuum_triage
    remed → vault versions pbs_vacuum_remediate
db-disk-pressure                 any        pbs_disk_pressure_triage        (none)                           (never)                STABLE(5) 24d  -
    → vault versions pbs_disk_pressure_triage
db-pg-hba-corrupt                any        pbs_pg_hba_triage               pbs_db_config_recovery           READY                  UNSTABLE(1) 24d 0 runs  (system)
db-process-kill                  any        pbs_db_restart_triage           pbs_db_restart_triage            (never)                UNSTABLE(1) 24d 32 runs  3% resolved  89% accurate  avg: 1m9s recovery  (imported)
    1.7   *  20r  0%  avg: 1m39s recovery  100% approach OK
    1.6      3r  0%  avg: 25s recovery
    → vault versions pbs_db_restart_triage
db-config-bad-param              any        -                               (none)                           NO PLAYBOOK            UNSTABLE(1) 24d -
db-wal-disk-full                 docker/vm  -                               (none)                           NO PLAYBOOK            UNSTABLE(1) 24d -
db-wal-disk-full-k8s             k8s        pbs_k8s_pod_crash_triage        (none)                           (never)                UNSTABLE(5) 24d -
    1.3   *  3r  0%  avg: 41s recovery
    1.2      5r  0%  avg: 6s recovery
db-wal-stale-slot                any        pbs_wal_stale_slot              pbs_stale_slot_remediate         (never)                STABLE(5) 24d  14 runs  79% resolved  100% accurate  avg: 3.4 steps, 24s recovery  (system)
    diag  1.3      8r  100%  avg: 2m12s recovery  100% approach OK
    diag  1.2      9r  0%  avg: 3m55s recovery
    remed → vault versions pbs_stale_slot_remediate
db-checkpoint-warning            any        pbs_checkpoint_bgwriter_triage  pbs_bgwriter_remediate           (never)                STABLE(5) 24d  1 runs  100% resolved  –  avg: 6.0 steps, 16s recovery  (system)
    diag  → vault versions pbs_checkpoint_bgwriter_triage
    remed → vault versions pbs_bgwriter_remediate
```

