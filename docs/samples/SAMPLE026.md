# aiHelpDesk Sample#26 (on K8s): Troubleshooting and Restoring databases hosted on VM and inside K8s from the central, in-cluster aiHelpDesk gear hosted on K8s.

The raw transcript of the sample commands and deliberations presented below complements this blog post:

- **[Break It on a Tuesday](https://itnext.io/break-it-on-a-tuesday-e3113b17bf8d)**  
  AI DBA demo from a vendor always works. Should you trust it? Martin’s 40 databases live half in Pods and half on VMs. Before he lets an AI repair either one, he made it break and fix both.
  
  
If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel).

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This sample transcript was created entirely on K8s. Not because the capabilities shown here are new or special (all three were already built and live-verified against Docker), but because K8s is where every one of the real bugs in this transcript was actually found. See [here](SAMPLE010.md), [here](SAMPLE021.md) and [here](SAMPLE019.md) for VM/Bare Metal, the source and container (Docker/Podman) sample transcripts respectively.

This sample transcript is designed to showcase a single new capability introduced in the [v0.33 release](https://github.com/borisdali/helpdesk/releases/tag/v0.33.0), which is not a PITR (see [SAMPLE027.md](SAMPLE027.md) for that), but a troubleshooting and remediation (well, restore and recovery in particular in this case) of databases hosted in-cluster and on remote VMs from the same central aiHelpDesk gear deployed on K8s.

---

## Run 1: Pod's WAL-gap test


Before we start it may be worth taking a minute to confirm that the image used for the tests indeed includes the VM and K8s WAL-gap faults and the corrupted data ones (they are embeded inside the image). The image used for the tests below is built from aiHelpDesk v0.33 branch and the Gateway confirms it as `version: k8schain-2247`:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system get deploy \
  -o jsonpath='{range .items[*]}{.spec.template.spec.containers[0].image}{"\n"}{end}' | \
  sort | uniq -c

10 ghcr.io/borisdali/helpdesk:k8schain-2247

[boris@ ~/helpdesk]$ for id in db-pgdata-corrupted-k8s db-pgbackrest-wal-gap-k8s db-pgdata-corrupted-vm db-pgbackrest-wal-gap-vm; do \
  printf "%-28s " $id; \
  kubectl -n helpdesk-system exec deploy/helpdesk-gateway -- sh -c \
  "grep -ac 'id: $id\$' /usr/local/bin/faulttest"; done

db-pgdata-corrupted-k8s      1
db-pgbackrest-wal-gap-k8s    1
db-pgdata-corrupted-vm       1
db-pgbackrest-wal-gap-vm     1
```

Good. Now, the faulttest binary is in the image too, so we can just `kubectl exec` on the Gateway Pod to run the failure injection tests and BTW also run the `vault incidents` to confirm that each run is treated as an incident inside the vault, let it's summary (aka TL;DR) and the full narrative (there's no need even for port-forwarding :-)).

And just one last sanity check before taking this show on the road, let's take a peek at the `helm` history runs:

```
[boris@ ~/helpdesk]$ helm -n helpdesk-system history helpdesk
REVISION        UPDATED                         STATUS          CHART           APP VERSION     DESCRIPTION
1               Fri Oct 23 17:26:11 2026        superseded      helpdesk-0.1.0  0.1.0           Install complete
2               Sat Oct 24 00:31:12 2026        superseded      helpdesk-0.1.0  0.1.0           Upgrade complete
3               Sat Oct 24 22:11:32 2026        superseded      helpdesk-0.1.0  0.1.0           Upgrade complete
4               Sun Oct 25 17:27:53 2026        superseded      helpdesk-0.1.0  0.1.0           Upgrade complete
5               Mon Oct 26 17:42:51 2026        superseded      helpdesk-0.1.0  0.1.0           Upgrade complete
6               Mon Oct 26 22:48:56 2026        superseded      helpdesk-0.1.0  0.1.0           Upgrade complete
7               Mon Oct 26 22:55:47 2026        failed          helpdesk-0.1.0  0.1.0           Upgrade "helpdesk" failed: context canceled
8               Mon Oct 26 23:08:36 2026        deployed        helpdesk-0.1.0  0.1.0           Rollback to 6
```

That's mine and yours obviously is going to be different, but my preference is to take stock of where we stand before I start. Anyhow, with that, let's run the firt WAL-gap test where the injected fault is hiding one of the necessary WAL segments and confirms that the lack of the WAL-continuity blocks the restore (which won't be able to complete anyhow and would leave the data directory in the unpredictable state):

```
[boris@ ~/helpdesk]$ helm upgrade helpdesk ./deploy/helm/helpdesk -n helpdesk-system \
  --reuse-values --timeout 15m \
  --set faulttest.enabled=true --set faulttest.ids=db-pgbackrest-wal-gap-k8s \
  --set faulttest.primaryInjection=true --set faulttest.emitAndWait=false \
  --set faulttest.targetNamespace=db --set faulttest.conn=pgbackrest-k8s \
  --set faulttest.viaGateway=true --set faulttest.gatewayAPIKeySecret=gateway-api-key \
  --set faulttest.remediate=false --set faulttest.gateEscalation=false --set faulttest.approvalMode= \
  --set faulttest.rbac.destructive=false --set faulttest.operator=alice@example.com

Release "helpdesk" has been upgraded. Happy Helming!
NAME: helpdesk
LAST DEPLOYED: Mon Oct 26 23:11:16 2026
NAMESPACE: helpdesk-system
STATUS: deployed
REVISION: 9
DESCRIPTION: Upgrade complete
TEST SUITE: None
NOTES:
aiHelpDesk has been deployed.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Connecting with helpdesk-client
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

helpdesk-client is the authenticated CLI for operators.
Download the binary for your OS from the release tarball, or — on Linux — extract it
directly from the image:

  # Linux only (image contains a Linux/amd64 or Linux/arm64 binary)
  docker run --rm --entrypoint cat \
    ghcr.io/borisdali/helpdesk:k8schain-2247 \
    /usr/local/bin/helpdesk-client > helpdesk-client && chmod +x helpdesk-client

  # macOS: download the tarball for your platform (darwin/amd64 or darwin/arm64) from
  # https://github.com/borisdali/helpdesk/releases/ and extract helpdesk-client.

The gateway Service is type ClusterIP. Forward it locally:

  kubectl port-forward svc/helpdesk-gateway \
    8080:8080

Then in another terminal:

  helpdesk-client --gateway http://localhost:8080 \
    --purpose diagnostic

To expose the gateway externally instead, upgrade with:

  helm upgrade helpdesk <chart> --set gateway.service.type=LoadBalancer

Authentication is enabled (provider: static).
Pass your API key:

  helpdesk-client --gateway <URL> --api-key <your-key> --purpose diagnostic

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Fault Injection Test running
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

A faulttest Job was launched as a post-install hook.
Helm waits for it to finish — stream its output in another terminal:

  kubectl -n helpdesk-system logs \
    -l app.kubernetes.io/component=faulttest --follow

When it completes, extract the JSON report from the logs:

  kubectl -n helpdesk-system logs \
    -l app.kubernetes.io/component=faulttest \
    | sed -n '/^--- BEGIN FAULTTEST REPORT JSON ---$/,/^--- END FAULTTEST REPORT JSON ---$/{ /^---/d; p }' \
    > faulttest-report.json

Disable for subsequent upgrades (so it does not re-run on unrelated changes):

  helm upgrade helpdesk <chart> --reuse-values \
    --set faulttest.enabled=false
```

Now `helm upgrade` has somewhat annoying habit to hang until it completes, so let's poll the `kubectl log` results for this Pod's run from another terminal while it runs:

```
[boris@ ~/helpdesk]$ until kubectl -n helpdesk-system logs -l app.kubernetes.io/component=faulttest -f 2>/dev/null; do sleep 3; done
time=2026-10-27T03:11:18.675Z level=INFO msg=--conn alias=pgbackrest-k8s host=pgbackrest-demo.db.svc.cluster.local

--- Testing: pgBackRest WAL chain broken (Kubernetes pod) (db-pgbackrest-wal-gap-k8s) ---
time=2026-10-27T03:11:18.679Z level=INFO msg="executing injection spec" type=shell_exec phase=inject
time=2026-10-27T03:11:18.966Z level=INFO msg="shell_exec completed" output="Defaulted container \"postgres\" out of: postgres, repo-owner (init)\nhid /var/lib/pgbackrest/archive/main/16-1/0000000200000000/000000020000000000000006-            d9deef710e5ef6001b230d3da0281fd7be1b9ff3.gz"
time=2026-10-27T03:11:19.785Z level=INFO msg="sending prompt to agent via playbook" failure=db-pgbackrest-wal-gap-k8s series_id=pbs_pgbackrest_health_triage playbook_id=pb_f98376f2 gateway=http://helpdesk-gateway:8080 agent-                  conn="host=pgbackrest-demo.db.svc.cluster.local port=5432 dbname=testdb user=postgres password=testpass"
time=2026-10-27T03:11:34.753Z level=INFO msg="audit evidence" failure=db-pgbackrest-wal-gap-k8s tools=[get_pgbackrest_status]
time=2026-10-27T03:11:42.975Z level=INFO msg="executing injection spec" type=shell_exec phase=teardown
time=2026-10-27T03:11:43.281Z level=INFO msg="shell_exec completed" output="Defaulted container \"postgres\" out of: postgres, repo-owner (init)"
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% category=100%)

=== Fault Test Report: 8d205769 ===

[PASS] pgBackRest WAL chain broken (Kubernetes pod) (db-pgbackrest-wal-gap-k8s) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

Report written to /reports/faulttest-8d205769.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "8d205769",
  "timestamp": "2026-10-27T03:11:43Z",
  "results": [
    {
      "failure_id": "db-pgbackrest-wal-gap-k8s",
      "failure_name": "pgBackRest WAL chain broken (Kubernetes pod)",
      "category": "database",
      "score": 1,
      "passed": true,
      "run_id": "plr_68e31a92",
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "ordering_pass": true,
      "tool_evidence_mode": "audit",
      "response_text": "I will check the pgBackRest backup-job health for the target database using get_pgbackrest_status. This will tell me whether the backup exists, is fresh, and whether the archived WAL chain needed for restore is        continuous.\nBased on the get_pgbackrest_status output, I have clear diagnostic information. Let me analyze the findings:\n\n**Status Code**: status_code=0 (repo is functional)\n**Backup Exists**: Yes, last_backup_label=\"20261027-024942F\", last_backup_time=\"2026-10-27T02:49:44Z\"\n**Backup Stale**: backup_stale=false (backup is recent)\n**WAL Continuity**: wal_continuity.status=\"broken\"\n\nThe critical finding is in wal_continuity.detail: \"WAL segment                       000000020000000000000006, needed for backup 20261027-024942F to reach a consistent state, is missing from the repo: PostgreSQL could not start from this backup\"\n\nThe consistency_reachable field is false, meaning PostgreSQL cannot even     start from this backup. The missing segment is identified: timeline 2, segment 000000020000000000000006. Recovery would fail at startup.\n\nThis is Ending E from the Expert Guidance: the backup exists and is recent, but the WAL chain is      broken and cannot be restored. This is a data integrity issue that requires human intervention — someone must investigate why timeline-2 WAL went missing and decide on recovery strategy (typically: take a fresh full backup to establish a new unbroken chain).\n\nHYPOTHESIS_1: WAL archiving chain is broken; the most recent backup cannot reach a consistent state because a required WAL segment is missing from the repository | CONFIDENCE: 0.95 | EVIDENCE: \"wal_continuity.            status=\\\"broken\\\"; detail=\\\"WAL segment 000000020000000000000006, needed for backup 20261027-024942F to reach a consistent state, is missing from the repo: PostgreSQL could not start from this backup\\\"\"\nHYPOTHESIS_2: Repository     permissions or filesystem corruption deleted the segment after backup | CONFIDENCE: 0.05 | REJECTED: The repo status_code=0 indicates the repository itself is healthy and accessible; status_message=\"ok\" confirms normal                      operation\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "15.60151959s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 1,
      "objective_evidence_signals": [
        "pgbackrest_wal_chain_broken"
      ],
      "objective_evidence_confirmed": [
        "pgbackrest_wal_chain_broken"
      ],
      "overall_score": 1,
      "primary_confidence": 0.95,
      "primary_hypothesis": "WAL archiving chain is broken; the most recent backup cannot reach a consistent state because a required WAL segment is missing from the repository",
      "secondary_hypothesis": "Repository permissions or filesystem corruption deleted the segment after backup",
      "secondary_confidence": 0.05,
      "hypotheses": [
        {
          "text": "WAL archiving chain is broken; the most recent backup cannot reach a consistent state because a required WAL segment is missing from the repository",
          "confidence": 0.95,
          "is_primary": true
        },
        {
          "text": "Repository permissions or filesystem corruption deleted the segment after backup",
          "confidence": 0.05,
          "is_primary": false,
          "rejected_reason": "The repo status_code=0 indicates the repository itself is healthy and accessible; status_message=\"ok\" confirms normal operation"
        }
      ]
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
Evaluation scores posted to auditd: 1/1 runs
```

Good. Let's get the TL;DR of the incident (just a single hop in this case):

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system exec deploy/helpdesk-gateway -- faulttest \
  vault incidents plr_68e31a92 \
  --summary \
  --gateway $HELPDESK_GATEWAY_URL \
  --api_key $HELPDESK_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: k8schain-2247  ·  host: helpdesk-gateway-6cb5f74dcc-27hdd

INCIDENT plr_68e31a92 — needs_human after 16s

1. TRIAGE        pbs_pgbackrest_health_triage
   status_code=0; backup_stale=false; last_backup_label=20261027-024942F; wal_continuity=broken; wal_detail=WAL …

Root cause: pgbackrest-wal-chain-broken

Overall: 100% (diagnosis 100%)
```

Let's also confirm that the release is fully recovered and the teardown left the repo clean (nothing left hidden) and get the Job/Pod status:

```
[boris@ ~/helpdesk]$ helm -n helpdesk-system history helpdesk | tail -3
7               Mon Oct 26 22:55:47 2026        failed          helpdesk-0.1.0  0.1.0           Upgrade "helpdesk" failed: context canceled
8               Mon Oct 26 23:08:36 2026        superseded      helpdesk-0.1.0  0.1.0           Rollback to 6
9               Mon Oct 26 23:11:16 2026        deployed        helpdesk-0.1.0  0.1.0           Upgrade complete
0
         status: ok

[boris@ ~/helpdesk]$ kubectl -n db exec pgbackrest-demo-0 -- sh -c \
  'find /var/lib/pgbackrest -name "*.hidden-by-fault" | wc -l; \
  su postgres -c "pgbackrest --stanza=main info" | \
  grep -E "status|wal│archive"' | grep -v Defaulted

             wal archive min/max (16): 000000010000000000000003/000000020000000000000006

[boris@ ~/helpdesk]$ kubectl -n helpdesk-system get job,pod -l app.kubernetes.io/component=faulttest | tail -3

     NAME                           READY   STATUS      RESTARTS   AGE
     pod/helpdesk-faulttest-kkq7d   0/1     Completed   0          16m
```

Cool, everything is in order and now let's get the full incident's narrative from the vault:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system exec deploy/helpdesk-gateway -- faulttest \
  vault incidents plr_68e31a92 \
  --gateway $HELPDESK_GATEWAY_URL \
  --api_key $HELPDESK_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: k8schain-2247  ·  host: helpdesk-gateway-6cb5f74dcc-27hdd

════════════════════════════════════════════════════════════
INCIDENT plr_68e31a92
Started: 2026-10-27 03:11 UTC   Duration: 16s
Operator: alice@example.com
Origin: faulttest   Status: needs_human
Attribution: pgbackrest-wal-chain-broken
Bundle: /data/incidents/incident-cd5544de-20261027-031144.tar.gz
════════════════════════════════════════════════════════════

── TRIAGE
Playbook:  pbs_pgbackrest_health_triage
Findings:  status_code=0; backup_stale=false; last_backup_label=20261027-024942F;
           wal_continuity=broken; wal_detail=WAL segment 000000020000000000000006
           missing from repo; recoverable_to_wal=none (PostgreSQL cannot start);
           consistency_reachable=false
           ✓ [Layer 4] confirmed evidence — pgbackrest_wal_chain_broken

Hypotheses:
  [PRIMARY  95%] WAL archiving chain is broken; the most recent backup cannot reach a consistent state because a required WAL segment is missing from the repository
                 Evidence: "wal_continuity.status=\\\"broken\\\"; detail=\\\"WAL segment 000000020000000000000006, needed for backup 20261027-024942F to reach a consistent state, is missing from the repo: PostgreSQL could not start from this backup\\"
  [REJECTED  5%] Repository permissions or filesystem corruption deleted the segment after backup
                 Rejected: The repo status_code=0 indicates the repository itself is healthy and accessible; status_message="ok" confirms normal operation

── EVALUATION
Score:         100%   (diagnosis 100%)
Diagnosis:     1.00 (heuristic)   Agent confidence: 95%

── JOURNEYS
  WHY = Incident narrative (this view)   WHAT = Audit trail (vault journeys)

  triage:                faulttest-8d205769-db-pgbackrest-wal-gap-k8s
                         reasoning chain, hypothesis building

  → vault journeys faulttest-8d205769-db-pgbackrest-wal-gap-k8s
```

So let's summarize where we stand after this run 1. It's obviously a clean pass, entirely in-cluster, in ~25s and the Job log shows the following:

  - Everything ran inside the cluster.
    The gateway was `http://helpdesk-gateway:8080` (the Service, not a port-forward) and the injection used the Job's own ServiceAccount, with no kubeconfig. It hid 000000020000000000000006, the backup's start segment on timeline 2 and the teardown put it back. Neat.

  - Diagnosis: the health triage read `wal_continuity.status="broken"` and `consistency_reachable=false`, named the missing segment, called it Ending E and handed the decision to a human.
    It did not take the healthy stanza status (`status_code=0`) as proof the backup was usable. The signal `pgbackrest_wal_chain_broken` fired and was confirmed.

  - Score: 100%, with keywords, tools and category all at 100%.

  - Tools were scored from the audit trail (`tool_evidence_mode: audit`).
    The chart wires `--audit-url` to the in-cluster auditd and it accepted the Job's key.

  - After the `helm upgrade`, the Job pod is Completed, with no `*.hidden-by-fault` files remain in db and the stanza is ok with the archive intact (…03 to …2/…06). The JSON report is in the Job's log stored in the /reports dir, but it's of type emptyDir, so it's gone with the Pod.

This confirmed that the default Role, `exec` plus `read`, is enough for a fault that only runs commands in a Pod. The kubectl verbs in that script are covered by the Role test and the live run agrees.



## Run 2: Pod's 4-hop restore

This is failure injection test relies on the destructive RBAC (delete pod, scale, one-off repair pod, logs). Now, `helm upgrade`'s values from run 1 carry over, but we need to override the ones that matter (`remediate`, `gateEscalation`, `approvalMode`, `rbac.destructive` and `targetNamespace`). Additionally, `emitAndWait` was explicitly turned off in run 1, so also need turn it on here:

Let's kick off this destructive test!

```
[boris@ ~/helpdesk]$ helm upgrade helpdesk ./deploy/helm/helpdesk -n helpdesk-system \
  --reuse-values --timeout 20m \
  -f deploy/helm/examples/faulttest-k8s-job.yaml \
  --set faulttest.gatewayAPIKeySecret=gateway-api-key --set faulttest.operator=alice@example.com \
  --set faulttest.emitAndWait=true

Release "helpdesk" has been upgraded. Happy Helming!
NAME: helpdesk
LAST DEPLOYED: Mon Oct 26 23:43:24 2026
NAMESPACE: helpdesk-system
STATUS: deployed
REVISION: 10
DESCRIPTION: Upgrade complete
TEST SUITE: None
NOTES:
aiHelpDesk has been deployed.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Connecting with helpdesk-client
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
...
```

And as usual, let's poll the `kubectl log` from a different terminal:

```
[boris@ ~/helpdesk]$ until kubectl -n helpdesk-system logs -l app.kubernetes.io/component=faulttest -f 2>/dev/null; do sleep 3; done
time=2026-10-27T03:43:25.891Z level=INFO msg=--conn alias=pgbackrest-k8s-destruct host=pgbackrest-demo.db-destruct.svc.cluster.local

--- Testing: Data directory corrupted — restore from latest backup (Kubernetes pod) (db-pgdata-corrupted-k8s) ---
time=2026-10-27T03:43:25.895Z level=INFO msg="executing injection spec" type=shell_exec phase=inject
time=2026-10-27T03:43:34.093Z level=INFO msg="shell_exec completed" output="Defaulted container \"postgres\" out of: postgres, repo-owner (init)\npod \"pgbackrest-demo-0\" deleted from db-destruct namespace\ncrashed: pgbackrest-demo-0 is not ready and cannot be exec'd into"
time=2026-10-27T03:43:34.775Z level=INFO msg="sending prompt to agent via playbook" failure=db-pgdata-corrupted-k8s series_id=pbs_db_data_loss_triage playbook_id=pb_b6ce3f37 gateway=http://helpdesk-gateway:8080 agent-conn="host=pgbackrest-   demo.db-destruct.svc.cluster.local port=5432 dbname=testdb user=postgres password=testpass"
time=2026-10-27T03:43:49.268Z level=WARN msg="gateway warning" failure=db-pgdata-corrupted-k8s warning="expected evidence pattern not found in provided context: \"PANIC.*could not locate a valid checkpoint\""
time=2026-10-27T03:43:49.268Z level=WARN msg="gateway warning" failure=db-pgdata-corrupted-k8s warning="expected evidence pattern not found in provided context: \"database files are incompatible with server\""
time=2026-10-27T03:43:49.268Z level=WARN msg="gateway warning" failure=db-pgdata-corrupted-k8s warning="expected evidence pattern not found in provided context: \"invalid page.*could not read block\""
time=2026-10-27T03:43:49.268Z level=WARN msg="gateway warning" failure=db-pgdata-corrupted-k8s warning="expected evidence pattern not found in provided context: \"could not find the database system\""
time=2026-10-27T03:43:49.484Z level=INFO msg="audit evidence" failure=db-pgdata-corrupted-k8s tools=[check_connection]
  ⚠  TOOL ERROR: check_connection failed: "exit status 2"

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

time=2026-10-27T03:43:49.485Z level=INFO msg="gate pending: operator review required" failure=db-pgdata-corrupted-k8s run_id=plr_cb23439f escalation_target=pbs_sysadmin_host_triage
  Next playbook     : pbs_sysadmin_host_triage
  Findings          : single_instance=true; backup_tool=pgbackrest; pitr_decided=false
  Remediation plan  : Sysadmin — Host/Container Inspection (manual approval)
                      Inspect a PostgreSQL instance reported as unreachable, whether it runs
                      in a Docker/Podman container, under systemd directly on a host/VM, or
                      (handed off further) in a Kubernetes pod. Determine whether it exited
                      cleanly (manual stop / graceful shutdown) or crashed (OOM kill, panic,
                      unexpected exit), revise the prior DB agent's root cause hypothesis if
                      necessary, and provide evidence-based remediation guidance.


Gate pending — run_id=plr_cb23439f
  Resolve at        : POST http://helpdesk-gateway:8080/api/v1/decisions/gate:plr_cb23439f/resolve
  Body fields:
    resolution        : "approved" | "denied"
    resolved_by       : your email or user ID
    approval_mode     : "force" | "auto" | "review" | "manual" — use "force" to allow the full remediation chain to auto-run
    reason            : optional — free-text operator comment
    verdict_correct : true | false  (triage/at_gate feedback, before remediation runs)
    verdict_notes   : string        (required when verdict_correct=false)

  Remediation feedback (separate call, same gate window):
    POST http://helpdesk-gateway:8080/api/v1/fleet/playbook-runs/plr_cb23439f/feedback
    {"feedback_type":"remediation","feedback_time":"at_gate","verdict_correct":true,"verdict_notes":"..."}

time=2026-10-27T03:43:49.485Z level=INFO msg="emit-and-wait: auto-approving gate with force mode" run_id=plr_cb23439f operator=alice@example.com
time=2026-10-27T03:45:17.136Z level=INFO msg="gate auto-approved" run_id=plr_cb23439f status=pending_gate
time=2026-10-27T03:45:17.661Z level=INFO msg="waitForChildRunComplete: watching child run" child=plr_97e25f05
time=2026-10-27T03:45:17.775Z level=INFO msg="waitForChildRunComplete: child run hit a second gate mid-chain, force-approving" child=plr_97e25f05
time=2026-10-27T03:45:54.063Z level=INFO msg="waitForChildRunComplete: watching child run" child=plr_706ca5ca
time=2026-10-27T03:45:54.232Z level=INFO msg="waitForChildRunComplete: child run hit a second gate mid-chain, force-approving" child=plr_706ca5ca
time=2026-10-27T03:47:21.555Z level=INFO msg="waitForChildRunComplete: watching child run" child=plr_08a60c0d
time=2026-10-27T03:47:22.557Z level=INFO msg="waitForChildRunComplete: child run complete" child=plr_08a60c0d outcome=resolved

  Feedback pending — resolve at:
  POST http://helpdesk-gateway:8080/api/v1/decisions/feedback:plr_cb23439f/resolve
  Body fields:
    resolution  : "approved" (diagnosis correct) | "denied" (diagnosis wrong)
    resolved_by : your email or user ID
    reason      : actual root cause (required when resolution="denied")

  Waiting for feedback (10m timeout, Ctrl+C to skip)...
  Feedback received — thank you.

Remediation: RECOVERED 237.1s after injection (score: 75%)
Incident plr_cb23439f — recovered 237.1s after the fault was injected
  Remediation: pbs_pgbackrest_restore_remediate (resolved)
  Narrative  : GET http://helpdesk-gateway:8080/api/v1/incidents/plr_cb23439f
Vault: draft saved → pb_dc87629e (activate with 'faulttest vault list')
Chain evidence:      the first hop alone missed some criteria; the later hops of the chain met them (2 later diagnostic hop(s); keywords=100% tools=100% score now 85%)
  Feedback submitted (triage/post_incident run_id=plr_cb23439f)
time=2026-10-27T03:54:18.360Z level=INFO msg="executing injection spec" type=shell_exec phase=teardown
Diagnostic Result:   [PASS] score=85% (keywords=100% tools=100% category=50%)
Remediation Result:  [PASS] score=75% (237.1s, playbook)
Overall Result:      [PASS] score=81%
time=2026-10-27T03:54:18.594Z level=INFO msg="shell_exec completed" output="Defaulted container \"postgres\" out of: postgres, repo-owner (init)\npgbackrest-demo is ready: nothing to repair"

=== Fault Test Report: cbebfc0f ===

[PASS] Data directory corrupted — restore from latest backup (Kubernetes pod) (db-pgdata-corrupted-k8s) - score: 85%
       Keywords: 100% | Tools: 100% | Category: 50% [no judge — add --judge for semantic scoring]
       Remediation: 75% (237.1s, playbook) | Overall: 81%

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

Report written to /reports/faulttest-cbebfc0f.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "cbebfc0f",
  "timestamp": "2026-10-27T03:54:18Z",
  "results": [
    {
      "failure_id": "db-pgdata-corrupted-k8s",
      "failure_name": "Data directory corrupted — restore from latest backup (Kubernetes pod)",
      "category": "database",
      "score": 0.8500000000000001,
      "passed": true,
      "run_id": "plr_cb23439f",
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "ordering_pass": true,
      "tool_evidence_mode": "audit+chain",
      "response_text": "I will begin diagnosing the unavailability of the target database following the playbook protocol. Let me start by checking connectivity to confirm the failure.\n\nAttempting to connect to the target database to       verify it is unreachable and establish the failure type.\nThe connection is refused, confirming the database is unreachable. Per the playbook protocol for a Kubernetes-hosted instance (identified as \"Kubernetes: Local Minikube Cluster\" in  Known Infrastructure for pgbackrest-k8s-destruct), I cannot read pod logs directly — that capability belongs to the sysadmin agent. \n\nI will now extract the required findings from the Known Infrastructure context without attempting further diagnostic calls:\n\nFrom Known Infrastructure:\n- **Server**: pgbackrest-k8s-destruct at `host=pgbackrest-demo.db-destruct.svc.cluster.local port=5432 dbname=testdb`\n- **Hosting**: Kubernetes pod (minikube-cluster, namespace: db-destruct,  pod selector: app=pgbackrest-demo)\n- **Single instance**: There is no indication of a primary/replica topology for this specific instance in the Known Infrastructure list. It is standalone.\n- **Backup tool**: The name \"pgbackrest-k8s-     destruct\" and the pod name \"pgbackrest-demo\" indicate this is a pgBackRest-managed instance.\n- **PITR decided**: No — this is the first pass; we are assessing the failure type, not yet committing to an arbitrary point-in-time restore.    \n\nPer Step 1 of the Expert Guidance, I must hand off to the sysadmin agent because:\n1. This is a Kubernetes-hosted pod, and I have no tool to read pod logs (kubectl logs is a sysadmin capability).\n2. The connection failure alone does not distinguish between a crash, data corruption, WAL loss, or a storage volume issue.\n3. The sysadmin agent (pbs_sysadmin_host_triage) has get_host_logs and can read the pod's container logs to determine the actual failure signature.",
      "duration": "15.175116299s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 0.5,
      "chain_evidence_hops": 2,
      "gateway_warnings": [
        "expected evidence pattern not found in provided context: \"PANIC.*could not locate a valid checkpoint\"",
        "expected evidence pattern not found in provided context: \"database files are incompatible with server\"",
        "expected evidence pattern not found in provided context: \"invalid page.*could not read block\"",
        "expected evidence pattern not found in provided context: \"could not find the database system\""
      ],
      "remediation_attempted": true,
      "remediation_passed": true,
      "recovery_time_seconds": 237.144424648,
      "remediation_score": 0.75,
      "remediation_method": "playbook",
      "overall_score": 0.81
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
Evaluation scores posted to auditd: 1/1 runs
```

Here's the TL;DR of the run (aka incident `summary`):

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system exec deploy/helpdesk-gateway -- faulttest \
  vault incidents plr_cb23439f \
  --summary \
  --gateway $HELPDESK_GATEWAY_URL \
  --api_key $HELPDESK_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: k8schain-2247  ·  host: helpdesk-gateway-6cb5f74dcc-27hdd

INCIDENT plr_cb23439f — resolved in 224s

1. TRIAGE        pbs_db_data_loss_triage → pbs_sysadmin_host_triage
   single_instance=true; backup_tool=pgbackrest; pitr_decided=false
2. ESCALATION 1  pbs_sysadmin_host_triage → pbs_pgbackrest_health_triage  [plr_97e25f05]
   data_loss_restore_candidate=true; Pod phase=Running, restart_count=3, last_termination_reason=Error, last_ter…
3. ESCALATION 2  pbs_pgbackrest_health_triage → pbs_pgbackrest_restore_remediate  [plr_706ca5ca]
   status_code=0; backup_stale=false; last_backup_label=20261027-024944F; last_backup_time=2026-10-27T02:49:47Z;…
4. REMEDIATION   pbs_pgbackrest_restore_remediate (resolved)  [plr_08a60c0d]
   Restore from backup 20261027-024944F (2026-10-27T02:49:47Z) and recovery to redo LSN 0/19000148 (timeline 10)…

Root cause: missing-data-directory

Overall: 81% (diagnosis 50% · remediation 75%)
```

And here's the full incident narrative:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system exec deploy/helpdesk-gateway -- faulttest \
  vault incidents plr_cb23439f \
  --gateway $HELPDESK_GATEWAY_URL \
  --api_key $HELPDESK_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: k8schain-2247  ·  host: helpdesk-gateway-6cb5f74dcc-27hdd

════════════════════════════════════════════════════════════
INCIDENT plr_cb23439f
Started: 2026-10-27 03:43 UTC   Duration: 224s
Operator: alice@example.com
Origin: faulttest   Status: resolved
Attribution: missing-data-directory
Bundle: /data/incidents/incident-c3e0e8fb-20261027-034355.tar.gz
Draft playbook: pb_dc87629e
════════════════════════════════════════════════════════════

── TRIAGE
Playbook:  pbs_db_data_loss_triage
Findings:  single_instance=true; backup_tool=pgbackrest; pitr_decided=false

── GATE
Decision:  approved by alice@example.com  at 03:43 UTC

── ESCALATION 1/2
Playbook:  pbs_sysadmin_host_triage   Outcome: transitioned
Transitioned to: pbs_pgbackrest_health_triage (still a diagnosis hop, not yet remediation)
Findings:  data_loss_restore_candidate=true; Pod phase=Running, restart_count=3,
           last_termination_reason=Error, last_termination_exitcode=2; Logs show:
           "postgres: could not find the database system\nExpected to find it in
           the directory \"/var/lib/postgresql/data\",\nbut could not open file
           \"/var/lib/postgresql/data/global/pg_control\": No such file or
           directory"

Hypotheses:
  [PRIMARY  95%] PostgreSQL data directory has been lost or deleted (pg_control missing), causing container crash-loop on startup; this is a data-loss recovery case eligible for pgBackRest restore
  [REJECTED  5%] Kubernetes persistent volume mount failure preventing pg_data visibility to the pod

── ESCALATION 2/2
Playbook:  pbs_pgbackrest_health_triage   Outcome: transitioned
Transitioned to: pbs_pgbackrest_restore_remediate (still a diagnosis hop, not yet remediation)
Findings:  status_code=0; backup_stale=false; last_backup_label=20261027-024944F;
           last_backup_time=2026-10-27T02:49:47Z; wal_continuity=continuous;
           archive_end_time=2026-10-27T02:49:47Z;
           data_loss_restore_candidate=true from prior findings; backup is
           suitable for restore to recover corrupted data directory.

Hypotheses:
  [PRIMARY  99%] pgBackRest stanza is healthy with a recent, non-stale full backup and continuous WAL chain from backup to archive end; restore is the appropriate remediation for the confirmed data-directory corruption (pg_control missing).

── REMEDIATION
Playbook:  pbs_pgbackrest_restore_remediate   Outcome: resolved
Plan:      Restore from backup 20261027-024944F (2026-10-27T02:49:47Z) and
           recovery to redo LSN 0/19000148 (timeline 10) completed successfully;
           PostgreSQL is online and ready to accept connections; commits after
           the archive end (000000090000000000000018) were recovered from pg_wal
           through segment 000000090000000000000019, which restore_from_backup
           pushed to archive — only changes never written to pg_wal are lost;
           original pg_wal copies are saved at
           /var/lib/pgbackrest/pg_wal.saved-20261027T034639Z and can be removed
           by human verification.

── EVALUATION
Score:         81%   (diagnosis 50% · remediation 75%)
Diagnosis:     0.50 (heuristic)

── POST-INCIDENT FEEDBACK
  triage:                      ✓ correct

── JOURNEYS
  WHY = Incident narrative (this view)   WHAT = Audit trail (vault journeys)

  triage:                faulttest-cbebfc0f-db-pgdata-corrupted-k8s
                         reasoning chain, hypothesis building
  escalation:1:          faulttest-cbebfc0f-db-pgdata-corrupted-k8s-remed
                         intermediate hop — transitioned to pbs_pgbackrest_health_triage
  escalation:2:          tr_98d98b9f-531
                         intermediate hop — transitioned to pbs_pgbackrest_restore_remediate
  remediation:           tr_1dc0e6b7-b4d
                         tool calls, approvals, blast-radius decisions

  → vault journeys faulttest-cbebfc0f-db-pgdata-corrupted-k8s
```

And with that we confirmed that Run 2 passed from the in-cluster Job. Everything ran in the cluster, including `faulttest`. Let's see what the log shows:

  - Injection with the Job's own ServiceAccount.
    It deleted `pgbackrest-demo-0` and verified the crash (not ready and cannot be exec'd into). The `TOOL ERROR: check_connection failed` is expected, because the database is down.

  - The gate was approved as a named human.
    `emit-and-wait`: auto-approving gate with force mode ... `operator=alice@example.com`. With usersFile on, the operator was checked against the cluster's users file first.

  - Four hops, with the two mid-chain gates force-approved and recovery verified from inside the pod 237.1s after injection.

  - The Decision Hub loop worked from a Job.
    The log printed the feedback card, waited, then said "Feedback received — thank you". The Job ran for about 11 minutes in all, and roughly 7 of those were waiting for me to answer the card, as designed :-)

  - Scores: diagnostic 85% (keywords 100%, tools 100%, category 50%), remediation 75%, overall 81%.

    The tool mode is a new one, audit+chain: the audit trail had only hop 1's `check_connection` and the chain evidence added the later hops' tools.

  - `vault incidents` shows `Root cause: missing-data-directory` and resolved in 224s.


Now, let's flip the order and kick off run#4 (WAL-gap test) ahead of run#3 (the destructive test):

## Run 4: VM over SSH WAL-gap test:

First off, let's confirm that the VM that hosts your postgres accepts connections from inside a Pod (where aiHelpDesk gear runs). To that end let's spin up a throwaway Pod, using the image already in the cluster:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system run psqltest --rm -it \
  --restart=Never --image=ghcr.io/borisdali/helpdesk:k8schain-2247 \
  --command -- psql "host=<your IP address> port=5432 dbname=postgres user=postgres password=xxx" -c "select 1"
If you don't see a command prompt, try pressing enter.
 ?column?
----------
        1
(1 row)

pod "psqltest" deleted
```

Cool, a Pod can reach postgres on the remote VM by IP. Can it reach it by name?

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system run psqltest --rm -it \
  --restart=Never --image=ghcr.io/borisdali/helpdesk:k8schain-2247 \
  --command -- psql "host=pgbackrest-vm.test port=5432 dbname=postgres user=postgres password=xxx" -c "select 1"
If you don't see a command prompt, try pressing enter.

pod "psqltest" deleted
```

Ah, it can't for now. You likely *can* reach it from your laptop, but not from inside the Pod that uses its own name resolution method. Let's fix that!

### DNS detour

If your VM hosting your target postgres DBs are already fully DNS-resolveable inside a cluster by name, skip this part (in any enterprise settings they should). In my lab they were not, so this is a few minor step detour to avoid using the IPs directly, which is annoying. But more importantly, this verifies that the routing, `pg_hba.conf` entry and `listen_addresses` all allow remote connections from your in-cluster Pod. It's worth the check!

Now, my VM name is pgbackrest-vm.test, which is tricky because it resolves through my /etc/hosts just fine, but `.test` is a reserved TLD (RFC 6761) that no public DNS answers, so the Pod's DNS (e.g. CoreDNS or similar) is supposed to forward the name to a node's resolver, but nothing there knows what pgbackrest-vm.test is :-(

No matter, let's teach it to get it resolved too. First, the name needs to be added to the CoreDNS in my case. If the Corefile already has a hosts block, like in my case, just add one line there. Now, this changes cluster DNS (kube-system) for every ns and run `getent` from a Pod to confirm the resolution (if you need to revert back, just run `kubectl replace -f /tmp/coredns.backup.yaml` after stripping its resourceVersion or just delete that one line in `kubectl -n kube-system edit cm coredns`):

```
[boris@ ~/helpdesk]$ kubectl -n kube-system get cm coredns -o yaml > /tmp/coredns.backup.yaml

[boris@ ~/helpdesk]$ kubectl -n kube-system get cm coredns -o json | python3 -c "
  import json,sys
  cm=json.load(sys.stdin); c=cm['data']['Corefile']
  assert 'pgbackrest-vm.test' not in c
  cm['data']['Corefile']=c.replace('       <my IP address> host.minikube.internal\n','       <my IP address> host.minikube.internal\n       <my IP address> pgbackrest-vm.test\n')
  print(json.dumps(cm))" | kubectl replace -f -
configmap/coredns replaced

[boris@ ~/helpdesk]$ kubectl -n kube-system rollout restart deploy/coredns && kubectl -n kube-system rollout status deploy/coredns
deployment.apps/coredns restarted
Waiting for deployment spec update to be observed...
Waiting for deployment "coredns" rollout to finish: 0 out of 1 new replicas have been updated...
Waiting for deployment "coredns" rollout to finish: 0 out of 1 new replicas have been updated...
Waiting for deployment "coredns" rollout to finish: 0 of 1 updated replicas are available...
deployment "coredns" successfully rolled out

[boris@ ~/helpdesk]$ kubectl -n helpdesk-system run dnstest --rm -it \
  --restart=Never --image=ghcr.io/borisdali/helpdesk:k8schain-2247 \
  --command -- getent hosts pgbackrest-vm.test
<my IP address>    pgbackrest-vm.test
pod "dnstest" deleted
```

Let's confirm that we can resolve the name and run a trivial query through psql from an existing pod:

```
kubectl -n helpdesk-system exec deploy/helpdesk-gateway -- sh -c \
  'getent hosts pgbackrest-vm.test; \
   psql "host=pgbackrest-vm.test port=5432 dbname=postgres user=postgres password=xxx connect_timeout=5" \
   -Atc "select current_setting(\$\$server_version\$\$), inet_server_addr()"; \
   echo "psql exit: $?"
  '

<my IP address> pgbackrest-vm.test
16.15 (Ubuntu 16.15-1.pgdg24.04+2)|<my IP address>      psql exit: 0
```

Bingo! With that we confirmed that the name works e2e from inside the cluster. DNS now resolves and psql reaches remote postgres on the VM by name trouble free. OK, ready to proceed:

Now, it's also worth confirming that the Secret's `known_hosts` contains the VM's name:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system get secret sysadmin-ssh -o json | python3 -c "
> import json,sys,base64
> kh=base64.b64decode(json.load(sys.stdin)['data']['known_hosts']).decode()
> names=sorted({l.split()[0] for l in kh.splitlines() if l and not l.startswith('#')})
> print('hosts in the Secret:', names)
> print('has pgbackrest-vm.test:', 'pgbackrest-vm.test' in names)"; echo "--- your current ~/.ssh/known_hosts has it:"; grep -c '^pgbackrest-vm.test ' ~/.ssh/known_hosts

hosts in the Secret: ['xyz`', 'xyz2', ... 'pgbackrest-vm.test']
has pgbackrest-vm.test: True
--- your current ~/.ssh/known_hosts has it:
1
```

Good, my `pgbackrest-vm.test` is among the hosts! 


### Enable SSH access detour

With that name resolution detour now settled, let's take a fresh full backup:

```
[boris@ ~/helpdesk]$ ssh -i $KEY postgres@pgbackrest-vm.test 'pgbackrest --stanza=main --type=full backup'
[boris@ ~/helpdesk]$ echo $?
0
[boris@ ~/helpdesk]$ ssh -i $KEY postgres@pgbackrest-vm.test 'pgbackrest --stanza=main info' | grep -E "status|full backup:"
    status: ok
        full backup: 20261025-125505F
        full backup: 20261027-095942F
```

Next, let's enable SSH (helm settings have memory :-)) and create a secret for `faulttest` to run with the SSH key:

```
[boris@ ~/helpdesk]$ KEY=testing/docker/ssh/pgbackrest_test_client_key
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system create secret generic faulttest-ssh-key --from-file=id_ed25519=$KEY
secret/faulttest-ssh-key created

[boris@ ~/helpdesk]$ helm upgrade helpdesk ./deploy/helm/helpdesk -n helpdesk-system \
  --reuse-values \
  --set faulttest.enabled=false \
  --set agents.sysadmin.ssh.secretName=sysadmin-ssh \
  --set 'infrastructure.vms.pgbackrest-real-ubuntu-vm.ssh_key_path=/etc/helpdesk/ssh/id_ed25519'

Release "helpdesk" has been upgraded. Happy Helming!
NAME: helpdesk
LAST DEPLOYED: Tue Oct 27 09:57:53 2026
NAMESPACE: helpdesk-system
STATUS: deployed
REVISION: 11
DESCRIPTION: Upgrade complete
TEST SUITE: None
NOTES:
aiHelpDesk has been deployed.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Connecting with helpdesk-client
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
...
```

... and let's make sure that both the DB Agent and the SysAdmin Agent (both very relevant to our tests today) have been fully rolled out:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system rollout status deploy/helpdesk-sysadmin-agent deploy/helpdesk-database-agent
deployment "helpdesk-sysadmin-agent" successfully rolled out
deployment "helpdesk-database-agent" successfully rolled out
```

Nice, we are ready to roll!

```
[boris@ ~/helpdesk]$ helm upgrade helpdesk ./deploy/helm/helpdesk -n helpdesk-system \
  --reuse-values --timeout 15m \
  --set faulttest.enabled=true --set faulttest.ids=db-pgbackrest-wal-gap-vm \
  --set faulttest.primaryInjection=true --set faulttest.viaGateway=true \
  --set faulttest.gatewayAPIKeySecret=gateway-api-key --set faulttest.operator=alice@example.com \
  --set faulttest.remediate=false --set faulttest.gateEscalation=false --set faulttest.approvalMode= \
  --set faulttest.emitAndWait=false --set faulttest.rbac.destructive=false \
  --set faulttest.targetNamespace=helpdesk-test \
  --set 'faulttest.conn=host=pgbackrest-vm.test port=5432 dbname=postgres user=postgres password=testpass' \
  --set faulttest.agentConn=pgbackrest-ubuntu-vm \
  --set faulttest.ssh.secretName=faulttest-ssh-key --set faulttest.ssh.host=postgres@pgbackrest-vm.test --set faulttest.ssh.port=22

Release "helpdesk" has been upgraded. Happy Helming!
NAME: helpdesk
LAST DEPLOYED: Tue Oct 27 10:00:51 2026
NAMESPACE: helpdesk-system
STATUS: deployed
REVISION: 12
DESCRIPTION: Upgrade complete
TEST SUITE: None
NOTES:
aiHelpDesk has been deployed.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Connecting with helpdesk-client
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
...
```

... and, as usual, let's poll the log of that Job:

```
[boris@ ~/helpdesk]$ until kubectl -n helpdesk-system logs -l app.kubernetes.io/component=faulttest -f 2>/dev/null; do sleep 3; done
time=2026-10-27T14:00:53.831Z level=INFO msg=--conn host=pgbackrest-vm.test
time=2026-10-27T14:00:53.832Z level=INFO msg=--agent-conn alias=pgbackrest-ubuntu-vm host=pgbackrest-vm.test

--- Testing: pgBackRest WAL chain broken (real host/VM) (db-pgbackrest-wal-gap-vm) ---
time=2026-10-27T14:00:53.851Z level=INFO msg="executing injection spec" type=ssh_exec phase=inject
time=2026-10-27T14:00:54.201Z level=INFO msg="ssh_exec completed" target=postgres@pgbackrest-vm.test output="Warning: Permanently added 'pgbackrest-vm.test' (ED25519) to the list of known hosts.\r\nhid /var/lib/pgbackrest/archive/main/16-1/  0000002900000000/00000029000000000000001D-1554acd7a80bd2e3b48910ff0f7594243331315d.gz"
time=2026-10-27T14:00:56.507Z level=INFO msg="sending prompt to agent via playbook" failure=db-pgbackrest-wal-gap-vm series_id=pbs_pgbackrest_health_triage playbook_id=pb_f98376f2 gateway=http://helpdesk-gateway:8080 agent-                   conn="host=pgbackrest-vm.test port=5432 dbname=postgres user=postgres password=testpass"
time=2026-10-27T14:01:14.465Z level=INFO msg="audit evidence" failure=db-pgbackrest-wal-gap-vm tools=[get_pgbackrest_status]
time=2026-10-27T14:01:20.095Z level=INFO msg="executing injection spec" type=ssh_exec phase=teardown
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% category=100%)
time=2026-10-27T14:01:20.763Z level=INFO msg="ssh_exec completed" target=postgres@pgbackrest-vm.test output="restored /var/lib/pgbackrest/archive/main/16-1/0000002900000000/00000029000000000000001D-1554acd7a80bd2e3b48910ff0f7594243331315d.gz"

=== Fault Test Report: a78ffff2 ===

[PASS] pgBackRest WAL chain broken (real host/VM) (db-pgbackrest-wal-gap-vm) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

Report written to /reports/faulttest-a78ffff2.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "a78ffff2",
  "timestamp": "2026-10-27T14:01:20Z",
  "results": [
    {
      "failure_id": "db-pgbackrest-wal-gap-vm",
      "failure_name": "pgBackRest WAL chain broken (real host/VM)",
      "category": "database",
      "score": 1,
      "passed": true,
      "run_id": "plr_85721afd",
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "ordering_pass": true,
      "tool_evidence_mode": "audit",
      "response_text": "I will diagnose the pgBackRest backup-job health for the pgbackrest-ubuntu-vm server. Let me start by checking the backup status using get_pgbackrest_status.\n\n**Expected finding**: I will retrieve the stanza status, backup metadata, WAL continuity status, and staleness information. This will show whether the repo/stanza is intact, whether a backup exists and is recent, and whether the archived WAL chain is unbroken from that backup to the end of the     archive.\n**Analysis of get_pgbackrest_status output:**\n\nThe stanza status is healthy at the repository level (status_code=0, status_message=\"ok\"), and a backup exists and is not stale (backup_stale=false, last_backup_label=\"20261027-   095942F\", last_backup_time=\"2026-10-27T13:59:45Z\"). However, the WAL continuity status is **broken**: the critical WAL segment `00000029000000000000001D` that is needed for the backup to reach a consistent state is missing from the        archive. The consistency_reachable field is explicitly false, meaning PostgreSQL cannot even start from this backup. The gap is:\n- Timeline 41: segment 00000029000000000000001D (first and last missing, count 1).\n\nThis is Ending E from the Expert Guidance — a broken WAL chain.\n\nHYPOTHESIS_1: pgBackRest repository is intact and the most recent backup is current, but the archived WAL required to restore that backup to a consistent state is missing from the archive, making      recovery impossible. | CONFIDENCE: 0.95 | EVIDENCE: \"wal_continuity.status=broken; consistency_reachable=false; detail=WAL segment 00000029000000000000001D, needed for backup 20261027-095942F to reach a consistent state, is missing from the repo: PostgreSQL could not start from this backup\"\nHYPOTHESIS_2: The backup never actually completed successfully and the metadata is stale. | CONFIDENCE: 0.05 | REJECTED: last_backup_error=false and last_backup_time is recent (2026-10-    27T13:59:45Z) with a valid stop_time; the backup metadata is consistent.\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "18.917885883s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 1,
      "objective_evidence_signals": [
        "pgbackrest_wal_chain_broken"
      ],
      "objective_evidence_confirmed": [
        "pgbackrest_wal_chain_broken"
      ],
      "overall_score": 1,
      "primary_confidence": 0.95,
      "primary_hypothesis": "pgBackRest repository is intact and the most recent backup is current, but the archived WAL required to restore that backup to a consistent state is missing from the archive, making recovery impossible.",
      "secondary_hypothesis": "The backup never actually completed successfully and the metadata is stale.",
      "secondary_confidence": 0.05,
      "hypotheses": [
        {
          "text": "pgBackRest repository is intact and the most recent backup is current, but the archived WAL required to restore that backup to a consistent state is missing from the archive, making recovery impossible.",
          "confidence": 0.95,
          "is_primary": true
        },
        {
          "text": "The backup never actually completed successfully and the metadata is stale.",
          "confidence": 0.05,
          "is_primary": false,
          "rejected_reason": "last_backup_error=false and last_backup_time is recent (2026-10-27T13:59:45Z) with a valid stop_time; the backup metadata is consistent."
        }
      ]
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
Evaluation scores posted to auditd: 1/1 runs
```

Here's the TL;DR of the run (aka incident `summary`):

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system exec deploy/helpdesk-gateway -- faulttest \
  vault incidents plr_85721afd \
  --summary \
  --gateway $HELPDESK_GATEWAY_URL \
  --api_key $HELPDESK_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: k8schain-2247  ·  host: helpdesk-gateway-6cb5f74dcc-27hdd

INCIDENT plr_85721afd — needs_human after 16s

1. TRIAGE        pbs_pgbackrest_health_triage
   status_code=0; backup_stale=false; last_backup_label=20261027-095942F; wal_continuity=broken; wal_detail=WAL …

Root cause: pgbackrest-wal-chain-broken

Overall: 100% (diagnosis 100%)
```

And here's the full incident narrative:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system exec deploy/helpdesk-gateway -- faulttest \
  vault incidents plr_85721afd \
  --gateway $HELPDESK_GATEWAY_URL \
  --api_key $HELPDESK_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: k8schain-2247  ·  host: helpdesk-gateway-6cb5f74dcc-27hdd

════════════════════════════════════════════════════════════
INCIDENT plr_85721afd
Started: 2026-10-27 14:00 UTC   Duration: 16s
Operator: alice@example.com
Origin: faulttest   Status: needs_human
Attribution: pgbackrest-wal-chain-broken
Bundle: /data/incidents/incident-7838ecc3-20261027-140123.tar.gz
════════════════════════════════════════════════════════════

── TRIAGE
Playbook:  pbs_pgbackrest_health_triage
Findings:  status_code=0; backup_stale=false; last_backup_label=20261027-095942F;
           wal_continuity=broken; wal_detail=WAL segment
           00000029000000000000001D, needed for backup 20261027-095942F to reach
           a consistent state, is missing from the repo: PostgreSQL could not
           start from this backup; recoverable_to_wal=none (recovery cannot
           proceed); consistency_reachable=false
           ✓ [Layer 4] confirmed evidence — pgbackrest_wal_chain_broken

Hypotheses:
  [PRIMARY  95%] pgBackRest repository is intact and the most recent backup is current, but the archived WAL required to restore that backup to a consistent state is missing from the archive, making recovery impossible.
                 Evidence: "wal_continuity.status=broken; consistency_reachable=false; detail=WAL segment 00000029000000000000001D, needed for backup 20261027-095942F to reach a consistent state, is missing from the repo: PostgreSQL could    not start from this backup"
  [REJECTED  5%] The backup never actually completed successfully and the metadata is stale.
                 Rejected: last_backup_error=false and last_backup_time is recent (2026-10-27T13:59:45Z) with a valid stop_time; the backup metadata is consistent.

── EVALUATION
Score:         100%   (diagnosis 100%)
Diagnosis:     1.00 (heuristic)   Agent confidence: 95%

── JOURNEYS
  WHY = Incident narrative (this view)   WHAT = Audit trail (vault journeys)

  triage:                faulttest-a78ffff2-db-pgbackrest-wal-gap-vm
                         reasoning chain, hypothesis building

  → vault journeys faulttest-a78ffff2-db-pgbackrest-wal-gap-vm
```

What this run shows:
  - The Job reached the VM over SSH by name.
    The inject hid 00000029000000000000001D (timeline 41) in the VM's repo and the teardown logged restored …00000029000000000000001D-…gz.

  - The SysAdmin Agent reached the VM over SSH from its own Pod.
    `get_pgbackrest_status` read `wal_continuity.status=broken` and `consistency_reachable=false` and named the missing segment.

    The tool evidence was taken from the audit trail (`tool_evidence_mode: audit`, `tools=[get_pgbackrest_status]`), so the call was recorded, not just claimed.

  - Ending E, as designed.
    The model said the backup is current (`backup_stale=false`, taken at 13:59:45Z) and the stanza healthy (`status_code=0`) and still concluded recovery is impossible because of the missing segment.
    The signal `pgbackrest_wal_chain_broken` fired and was confirmed.

  - Score 100% (keywords, tools and category all 100%), ~27s e2e.

  - One hop and `Root cause: pgbackrest-wal-chain-broken`.

A minor footnote:
   The line `Warning: Permanently added 'pgbackrest-vm.test' (ED25519) to the list of known hosts` comes from the harness's injector, which runs `ssh -o StrictHostKeyChecking=no`.
   So the harness does not verify the VM's host key. The SysAdmin Agent does, against the `known_hosts` Secret. That's an old, deliberate trade-off for a test tool, not something introduced in this release, for security-minded folks it's important to be clear as to which side checks what.


## Run 3: VM over SSH for the 4-hop destructive test:

This is the destructive one: it really stops the VM's postgres by removing `pg_control` and the chain is there to restore it. There has to be a backup obviously and the previous run's teardown restored the repo, so no cleanup is needed.  Let's fire up the `help upgrade` to run this test:
```
[boris@ ~/helpdesk]$ date; time helm upgrade helpdesk ./deploy/helm/helpdesk -n helpdesk-system 
  --reuse-values --timeout 25m \
  -f deploy/helm/examples/faulttest-vm-job.yaml \
  --set faulttest.gatewayAPIKeySecret=gateway-api-key --set faulttest.operator=alice@example.com \
  --set faulttest.targetNamespace=helpdesk-test --set faulttest.rbac.destructive=false \
  --set 'faulttest.conn=host=pgbackrest-vm.test port=5432 dbname=postgres user=postgres password=xxx ' \
  --set faulttest.agentConn=pgbackrest-ubuntu-vm --set faulttest.ssh.host=postgres@pgbackrest-vm.test \
  --set faulttest.emitAndWait=true

Tue Oct 27 10:09:16 EDT 2026
Release "helpdesk" has been upgraded. Happy Helming!
NAME: helpdesk
LAST DEPLOYED: Tue Oct 27 10:09:17 2026
NAMESPACE: helpdesk-system
STATUS: deployed
REVISION: 13
DESCRIPTION: Upgrade complete
TEST SUITE: None
NOTES:
aiHelpDesk has been deployed.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Connecting with helpdesk-client
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
...
```

... and while it runs (which really appears like hanging until it completes), in another terminal run `kubectl logs -f` to see the run live:

```
[boris@ ~/helpdesk]$ until kubectl -n helpdesk-system logs -l app.kubernetes.io/component=faulttest -f 2>/dev/null; do sleep 3; done
time=2026-10-27T14:09:18.936Z level=INFO msg=--conn host=pgbackrest-vm.test
time=2026-10-27T14:09:18.936Z level=INFO msg=--agent-conn alias=pgbackrest-ubuntu-vm host=pgbackrest-vm.test

--- Testing: Data directory corrupted — restore from latest backup (real host/VM) (db-pgdata-corrupted-vm) ---
time=2026-10-27T14:09:18.939Z level=INFO msg="executing injection spec" type=ssh_exec phase=inject
time=2026-10-27T14:09:20.848Z level=INFO msg="ssh_exec completed" target=postgres@pgbackrest-vm.test output="Warning: Permanently added 'pgbackrest-vm.test' (ED25519) to the list of known hosts.\r\nJob for postgresql@16-main.service failed   because the service did not take the steps required by its unit configuration.\nSee \"systemctl status postgresql@16-main.service\" and \"journalctl -xeu postgresql@16-main.service\" for details."
time=2026-10-27T14:09:21.921Z level=INFO msg="sending prompt to agent via playbook" failure=db-pgdata-corrupted-vm series_id=pbs_db_data_loss_triage playbook_id=pb_b6ce3f37 gateway=http://helpdesk-gateway:8080 agent-conn="host=pgbackrest-vm. test port=5432 dbname=postgres user=postgres password=xxx"
time=2026-10-27T14:09:37.914Z level=WARN msg="gateway warning" failure=db-pgdata-corrupted-vm warning="expected evidence pattern not found in provided context: \"PANIC.*could not locate a valid checkpoint\""
time=2026-10-27T14:09:37.914Z level=WARN msg="gateway warning" failure=db-pgdata-corrupted-vm warning="expected evidence pattern not found in provided context: \"database files are incompatible with server\""
time=2026-10-27T14:09:37.914Z level=WARN msg="gateway warning" failure=db-pgdata-corrupted-vm warning="expected evidence pattern not found in provided context: \"invalid page.*could not read block\""
time=2026-10-27T14:09:37.914Z level=WARN msg="gateway warning" failure=db-pgdata-corrupted-vm warning="expected evidence pattern not found in provided context: \"could not find the database system\""
time=2026-10-27T14:09:37.996Z level=INFO msg="audit evidence" failure=db-pgdata-corrupted-vm tools=[check_connection]
  ⚠  TOOL ERROR: check_connection failed: "exit status 2"
time=2026-10-27T14:09:37.997Z level=INFO msg="gate pending: operator review required" failure=db-pgdata-corrupted-vm run_id=plr_f8cee154 escalation_target=pbs_sysadmin_host_triage

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Next playbook     : pbs_sysadmin_host_triage
  Findings          : single_instance=true; backup_tool=pgbackrest; pitr_decided=false
  Remediation plan  : Sysadmin — Host/Container Inspection (manual approval)
                      Inspect a PostgreSQL instance reported as unreachable, whether it runs
                      in a Docker/Podman container, under systemd directly on a host/VM, or
                      (handed off further) in a Kubernetes pod. Determine whether it exited
                      cleanly (manual stop / graceful shutdown) or crashed (OOM kill, panic,
                      unexpected exit), revise the prior DB agent's root cause hypothesis if
                      necessary, and provide evidence-based remediation guidance.


Gate pending — run_id=plr_f8cee154
  Resolve at        : POST http://helpdesk-gateway:8080/api/v1/decisions/gate:plr_f8cee154/resolve
  Body fields:
    resolution        : "approved" | "denied"
    resolved_by       : your email or user ID
    approval_mode     : "force" | "auto" | "review" | "manual" — use "force" to allow the full remediation chain to auto-run
    reason            : optional — free-text operator comment
    verdict_correct : true | false  (triage/at_gate feedback, before remediation runs)
    verdict_notes   : string        (required when verdict_correct=false)

  Remediation feedback (separate call, same gate window):
    POST http://helpdesk-gateway:8080/api/v1/fleet/playbook-runs/plr_f8cee154/feedback
    {"feedback_type":"remediation","feedback_time":"at_gate","verdict_correct":true,"verdict_notes":"..."}

time=2026-10-27T14:09:37.999Z level=INFO msg="emit-and-wait: auto-approving gate with force mode" run_id=plr_f8cee154 operator=alice@example.com
time=2026-10-27T14:11:07.130Z level=INFO msg="gate auto-approved" run_id=plr_f8cee154 status=pending_gate
time=2026-10-27T14:11:07.518Z level=INFO msg="waitForChildRunComplete: watching child run" child=plr_aab10403
time=2026-10-27T14:11:08.096Z level=INFO msg="waitForChildRunComplete: child run hit a second gate mid-chain, force-approving" child=plr_aab10403
time=2026-10-27T14:11:29.012Z level=INFO msg="waitForChildRunComplete: watching child run" child=plr_baca0da5
time=2026-10-27T14:11:29.607Z level=INFO msg="waitForChildRunComplete: child run hit a second gate mid-chain, force-approving" child=plr_baca0da5
time=2026-10-27T14:12:09.622Z level=INFO msg="waitForChildRunComplete: watching child run" child=plr_3792cf64
time=2026-10-27T14:12:15.236Z level=INFO msg="waitForChildRunComplete: child run complete" child=plr_3792cf64 outcome=resolved

  Feedback pending — resolve at:
  POST http://helpdesk-gateway:8080/api/v1/decisions/feedback:plr_f8cee154/resolve
  Body fields:
    resolution  : "approved" (diagnosis correct) | "denied" (diagnosis wrong)
    resolved_by : your email or user ID
    reason      : actual root cause (required when resolution="denied")

  Waiting for feedback (10m timeout, Ctrl+C to skip)...
  Feedback received — thank you.

Remediation: RECOVERED 176.4s after injection (score: 75%)
Incident plr_f8cee154 — recovered 176.4s after the fault was injected
  Remediation: pbs_pgbackrest_restore_remediate (resolved)
  Narrative  : GET http://helpdesk-gateway:8080/api/v1/incidents/plr_f8cee154
Vault: draft saved → pb_4078c716 (activate with 'faulttest vault list')
Chain evidence:      the first hop alone missed some criteria; the later hops of the chain met them (2 later diagnostic hop(s); keywords=100% tools=100% score now 85%)
  Feedback submitted (triage/post_incident run_id=plr_f8cee154)
time=2026-10-27T14:16:32.001Z level=INFO msg="executing injection spec" type=ssh_exec phase=teardown
Diagnostic Result:   [PASS] score=85% (keywords=100% tools=100% category=50%)
Remediation Result:  [PASS] score=75% (176.4s, playbook)
Overall Result:      [PASS] score=81%

=== Fault Test Report: fa2a805b ===

[PASS] Data directory corrupted — restore from latest backup (real host/VM) (db-pgdata-corrupted-vm) - score: 85%
       Keywords: 100% | Tools: 100% | Category: 50% [no judge — add --judge for semantic scoring]
       Remediation: 75% (176.4s, playbook) | Overall: 81%

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

time=2026-10-27T14:16:32.313Z level=INFO msg="ssh_exec completed" target=postgres@pgbackrest-vm.test output="postgresql@16-main is active: nothing to repair"
Report written to /reports/faulttest-fa2a805b.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "fa2a805b",
  "timestamp": "2026-10-27T14:16:32Z",
  "results": [
    {
      "failure_id": "db-pgdata-corrupted-vm",
      "failure_name": "Data directory corrupted — restore from latest backup (real host/VM)",
      "category": "database",
      "score": 0.8500000000000001,
      "passed": true,
      "run_id": "plr_f8cee154",
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "ordering_pass": true,
      "tool_evidence_mode": "audit+chain",
      "response_text": "I'll begin diagnosing why this PostgreSQL instance is unavailable. Let me start by attempting to connect to the target database and then gather diagnostic information.\nThe connection attempt failed with \"connection  refused\" — the PostgreSQL service is not responding on the expected address and port. According to the Expert Guidance for this playbook (Database Down — Data Loss Triage), I must now follow Step 1.\n\n**Step 1 Analysis — Confirm failure    type and scope:**\n\nFrom the Known Infrastructure context, I can determine the following *without* requiring log evidence:\n\n1. **single_instance**: Looking at the infrastructure list, `pgbackrest-ubuntu-vm` (pgbackrest-vm.test) is listed  as a standalone VM with `[systemd: postgresql@16-main]`. There is no replica entry paired with it. **TRUE** — this is a single, standalone instance.\n\n2. **backup_tool**: The server is named \"pgBackRest Real Ubuntu VM\" and the             infrastructure context explicitly indicates this server. Given the name pattern and the context description, the backup tool is **pgBackRest**.\n\n3. **pitr_decided**: On a first pass, no point-in-time recovery decision has been made. The    database is simply unavailable. **FALSE** — PITR is not yet decided.\n\nHowever, the server is hosted on a bare VM with **[systemd: postgresql@16-main]** — this means I cannot read the systemd journal or service logs myself. According to     Expert Guidance Step 1:\n\n\u003e You have no tool of your own to read Docker/Podman container logs, a systemd-managed host's own service/journal logs, or a Kubernetes pod's logs ... if the entry shows \"[docker container: ...]\", \"[podman  container: ...]\", \"[systemd: ...]\" or a hosting of \"Kubernetes: ...\", you cannot confirm the failure signature yourself.\n\nThe playbook explicitly states:\n\n\u003e Before escalating, answer what you CAN already determine without any   log evidence, from the Known Infrastructure context alone ... Emit exactly: FINDINGS: ... ESCALATE_TO: pbs_sysadmin_host_triage and stop.\n\nThis is the correct path. I cannot determine whether the failure is due to corruption, WAL loss, a   simple crash, or another root cause without reading the systemd journal and PostgreSQL logs — those belong to the sysadmin triage agent.",
      "duration": "17.064602924s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 0.5,
      "chain_evidence_hops": 2,
      "gateway_warnings": [
        "expected evidence pattern not found in provided context: \"PANIC.*could not locate a valid checkpoint\"",
        "expected evidence pattern not found in provided context: \"database files are incompatible with server\"",
        "expected evidence pattern not found in provided context: \"invalid page.*could not read block\"",
        "expected evidence pattern not found in provided context: \"could not find the database system\""
      ],
      "remediation_attempted": true,
      "remediation_passed": true,
      "recovery_time_seconds": 176.409379537,
      "remediation_score": 0.75,
      "remediation_method": "playbook",
      "overall_score": 0.81
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
Evaluation scores posted to auditd: 1/1 runs
```

Here's the TL;DR of the run (aka incident `summary`):

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system exec deploy/helpdesk-gateway -- faulttest \
  vault incidents plr_f8cee154 \
  --summary \
  --gateway $HELPDESK_GATEWAY_URL \
  --api_key $HELPDESK_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: k8schain-2247  ·  host: helpdesk-gateway-6cb5f74dcc-27hdd

INCIDENT plr_f8cee154 — resolved in 167s

1. TRIAGE        pbs_db_data_loss_triage → pbs_sysadmin_host_triage
   single_instance=true; backup_tool=pgbackrest; pitr_decided=false
2. ESCALATION 1  pbs_sysadmin_host_triage → pbs_pgbackrest_health_triage  [plr_aab10403]
   data_loss_restore_candidate=true; Logs show: "postgres: could not find the database system ... could not open…
3. ESCALATION 2  pbs_pgbackrest_health_triage → pbs_pgbackrest_restore_remediate  [plr_baca0da5]
   status_code=0; backup_stale=false; last_backup_label=20261027-095942F; last_backup_time=2026-10-27T13:59:45Z;…
4. REMEDIATION   pbs_pgbackrest_restore_remediate (resolved)  [plr_3792cf64]
   Restore from backup 20261027-095942F (2026-10-27T13:59:45Z) completed successfully; recovery verified at WAL …

Root cause: missing-data-directory

Overall: 81% (diagnosis 50% · remediation 75%)
```

And the full incident narrative:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system exec deploy/helpdesk-gateway -- faulttest \
  vault incidents plr_f8cee154 \
  --gateway $HELPDESK_GATEWAY_URL \
  --api_key $HELPDESK_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: k8schain-2247  ·  host: helpdesk-gateway-6cb5f74dcc-27hdd

════════════════════════════════════════════════════════════
INCIDENT plr_f8cee154
Started: 2026-10-27 14:09 UTC   Duration: 167s
Operator: alice@example.com
Origin: faulttest   Status: resolved
Attribution: missing-data-directory
Bundle: /data/incidents/incident-7bb9c945-20261027-140940.tar.gz
Draft playbook: pb_4078c716
════════════════════════════════════════════════════════════

── TRIAGE
Playbook:  pbs_db_data_loss_triage
Findings:  single_instance=true; backup_tool=pgbackrest; pitr_decided=false

── GATE
Decision:  approved by alice@example.com  at 14:09 UTC

── ESCALATION 1/2
Playbook:  pbs_sysadmin_host_triage   Outcome: transitioned
Transitioned to: pbs_pgbackrest_health_triage (still a diagnosis hop, not yet remediation)
Findings:  data_loss_restore_candidate=true; Logs show: "postgres: could not find
           the database system ... could not open file
           \"/var/lib/postgresql/16/main/global/pg_control\": No such file or
           directory"; the pg_control file is missing from the data directory,
           preventing PostgreSQL from starting; a pgBackRest restore is required
           to recover the database.

Hypotheses:
  [PRIMARY  95%] Data directory pg_control file is missing or corrupted; the database is unable to start and requires restoration from pgBackRest backup to recover.
  [REJECTED  5%] Disk space exhaustion in the data directory that truncated pg_control during a prior write operation.

── ESCALATION 2/2
Playbook:  pbs_pgbackrest_health_triage   Outcome: transitioned
Transitioned to: pbs_pgbackrest_restore_remediate (still a diagnosis hop, not yet remediation)
Findings:  status_code=0; backup_stale=false; last_backup_label=20261027-095942F;
           last_backup_time=2026-10-27T13:59:45Z; wal_continuity=continuous;
           archive_end_time=2026-10-27T13:59:45Z;
           data_loss_restore_candidate=true confirmed; restore is safe

Hypotheses:
  [PRIMARY  100%] pgBackRest backup repository is healthy with a continuous WAL chain; the most recent backup can be restored to the end of the archive, meeting the safety precondition for data-directory recovery

── REMEDIATION
Playbook:  pbs_pgbackrest_restore_remediate   Outcome: resolved
Plan:      Restore from backup 20261027-095942F (2026-10-27T13:59:45Z) completed
           successfully; recovery verified at WAL segment
           00000029000000000000001E, LSN 0/1E000148, timeline 42; commits after
           archive end were recovered from pg_wal through
           00000029000000000000001E; only changes never written to pg_wal are
           lost; original WAL saved to
           /var/lib/postgresql/16/pg_wal.saved-20261027T141141Z for manual
           cleanup after verification; instance is running and ready to accept
           connections.

── EVALUATION
Score:         81%   (diagnosis 50% · remediation 75%)
Diagnosis:     0.50 (heuristic)

── POST-INCIDENT FEEDBACK
  triage:                      ✓ correct

── JOURNEYS
  WHY = Incident narrative (this view)   WHAT = Audit trail (vault journeys)

  triage:                faulttest-fa2a805b-db-pgdata-corrupted-vm
                         reasoning chain, hypothesis building
  escalation:1:          faulttest-fa2a805b-db-pgdata-corrupted-vm-remed
                         intermediate hop — transitioned to pbs_pgbackrest_health_triage
  escalation:2:          tr_f4f3b0bc-68f
                         intermediate hop — transitioned to pbs_pgbackrest_restore_remediate
  remediation:           tr_7e859ecb-9cd
                         tool calls, approvals, blast-radius decisions

  → vault journeys faulttest-fa2a805b-db-pgdata-corrupted-vm
```

And BTW, when the test is done, it's designed to offer a user an opportunity to provide incident diagnosis/remediation feedback (waiting for up to 10 min before finishing the run). We call it a "feedback card". It pops up when `emitAndWait` option is requested and here's an example of how that feedback can be provided (that's a post-incident feedback, not to be confused with the at-the-gate feedback):

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system exec deploy/helpdesk-gateway -- sh -c \
  'curl -s -X POST http://localhost:8080/api/v1/decisions/feedback:plr_f8cee154/resolve \
  -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"resolution\":\"approved\",\"resolved_by\":\"alice@example.com\",\"reason\":\"correct\"}"'

{"status":"resolved","verdict_correct":true}
```

Once this post-incident feedback is provided, notice that the test gets unblocked and proceeds with the "Feedback received - thank you" message:

```
  Waiting for feedback (10m timeout, Ctrl+C to skip)...
  Feedback received — thank you.
```

  - The chain: data-loss triage, host triage, health triage, restore.
    The first gate is approved as `alice@example.com` and the later ones are force-approved.

  - The recovery:
    The harness checks from the pod with psql over the network, which is why step A and the DNS fix mattered.
    Expect RECOVERED … after injection around 3 to 4 minutes.

  - The incident:
    It ends with `Root cause: missing-data-directory`.


## Summary of the four runs:

All four runs have now passed, with aiHelpDesk gear hosted completely in-cluster: Gateway, agents, auditd and faulttest client itself running as a Helm Job:

```
  ┌─────┬─────────────┬────────────────────┬──────────────┬────────────────────────────┐
  │  #  │   Target    │       Fault        │     Run      │           Result           │
  ├─────┼─────────────┼────────────────────┼──────────────┼────────────────────────────┤
  │ 1   │ Pod         │ hidden WAL segment │ plr_68e31a92 │ 100%, 25s, ends at a human │
  ├─────┼─────────────┼────────────────────┼──────────────┼────────────────────────────┤
  │ 2   │ Pod         │ four-hop restore   │ plr_cb23439f │ 81%, recovered 237s        │
  ├─────┼─────────────┼────────────────────┼──────────────┼────────────────────────────┤
  │ 3   │ VM over SSH │ four-hop restore   │ plr_f8cee154 │ 81%, recovered 176s        │
  ├─────┼─────────────┼────────────────────┼──────────────┼────────────────────────────┤
  │ 4   │ VM over SSH │ hidden WAL segment │ plr_85721afd │ 100%, 27s, ends at a human │
  └─────┴─────────────┴────────────────────┴──────────────┴────────────────────────────┘
```

See [this blog post](https://itnext.io/break-it-on-a-tuesday-e3113b17bf8d) for more color on these tests.

