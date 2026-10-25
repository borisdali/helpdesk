# aiHelpDesk Sample#25 (on K8s): Proof Before Overwrite

The raw transcript of the sample commands and deliberations presented below complements this blog post:

- **[Postgres Never Asks for RESETLOGS](https://itnext.io/postgres-never-asks-for-resetlogs-d313f6801ac5)**  
  A restore with a hole in its history looks just like one without. aiHelpDesk proves the difference before it overwrites anything
  
  
If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel).

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This sample transcript was created entirely on K8s. Not because the capabilities shown here are new or special (all three were already built and live-verified against Docker), but because K8s is where every one of the real bugs in this transcript was actually found. See [here](SAMPLE010.md), [here](SAMPLE021.md) and [here](SAMPLE019.md) for VM/Bare Metal, the source and container (Docker/Podman) sample transcripts respectively.

This sample transcript is meant to showcase the new capabilities introduced in the [v0.32 release](https://github.com/borisdali/helpdesk/releases/tag/v0.32.0), which is a pgBackRest restore/recovery, but with the harness, safeguards and sanity checks that fire before and after the restore. Yes, it's in-place, so unless these precautions are taken, you may end up with the WORSE situation compared to when you started, i.e. your existing data and WAL segments may get wiped out and not restored/recovered properly.   
  
That's why we spent so much time to ensure that won't happen. If there's a problem with the backup, the repo, the "hole" in the WAL stream (i.e. it's not continuous), we don't even start a restore. That is, aiHelpDesk refuses to cause potentially more damage and advises you exactly why. That's the only way. Or at least that's the only way we see a reliable database operations to proceed (yes, especially when it's driven by probabilistic AI :-).
  
The samples are of real runs against the live K8s cluster. Not `helm template`, not a unit test, not a mock:

---

## K8s restore integration test

First off, the automated tests. Yes, we run it for every release as part of our CI, but aiHelpDesk is OSS and it's possible that we didn't test your specific platform, so we recommend you run it there yourself too. It's cheap, fast and is designed to give you the first level of confidence.  
  
If you are not familiar with the aiHelpDesk automated test pyramid, we recommend taking [a quick look at it first](../../testing/README.md#summary-aihelpdesk-testing-pyramid). For a restore on K8s in particular, there's a separate integration test [code link](https://github.com/borisdali/helpdesk/blob/af3bc7ecef9c1bd71199f9093d4bd159f81e55cf/Makefile#L170) with its own make target. It runs as part of the whole batter of integration tests too, but can be invoked on its own (faster, more targeted):

The name of the test is [`TestIntegration_K8sCluster_RestoreRefusesOnRealWALGap`](https://github.com/borisdali/helpdesk/blob/af3bc7ecef9c1bd71199f9093d4bd159f81e55cf/agents/sysadmin/restore_k8s_integration_test.go#L195), which does the following in order:  
1. reads the backup's start segment from `get_pgbackrest_status` and checks the chain is continuous  
2. renames that segment's file to `*.hidden-by-test` in the repo  
3. deletes `pg_control`, drops a canary file in PGDATA and deletes the Pod so it crash-loops  
4. confirms that `get_pgbackrest_status` says broken at that segment, with `consistency_reachable=false`  
5. calls `restore_from_backup` and requires a "WAL continuity is broken" refusal  
6. checks the StatefulSet is still at 1 replica and the canary file is still there  
7. in cleanup, unhides the file and repairs the instance with a restore and resume  

This test is strictly opt-in, since it crashes the Pod and it skips unless the context is set. That's the guard:

```
  HELPDESK_K8S_PGBACKREST_CONTEXT=<context> go test -tags integration -timeout 600s ./agents/sysadmin/ -run K8s_ -v
```

It runs from the laptop through the <context> and uses the same `kubectl exec` code path the in-cluster agent uses, with the kubeconfig context instead of the service account.




```
[boris@ ~/helpdesk]$ HELPDESK_K8S_SYSADMIN_API_KEY=$HELPDESK_CLIENT_API_KEY \
  HELPDESK_K8S_SYSADMIN_URL=http://localhost:11103 \
  HELPDESK_K8S_SYSADMIN_TARGET=pgbackrest-k8s-destruct \
  HELPDESK_K8S_PGBACKREST_CONTEXT=<context> \
  HELPDESK_K8S_PGBACKREST_NAMESPACE=db-destruct \
  go test --count=1 -tags integration -timeout 900s -v -run 'K8sCluster_RestoreRefusesOnRealWALGap' ./agents/sysadmin/...  

=== RUN   TestIntegration_K8sCluster_RestoreRefusesOnRealWALGap
--- PASS: TestIntegration_K8sCluster_RestoreRefusesOnRealWALGap (93.79s)
PASS
ok      helpdesk/agents/sysadmin        94.255s

Nice! The test passed and `db-destruct` Pod ended up as healthy. This is the WAL-continuity refusal on K8s in action. What it proved, in a real cluster against a real crash:
  - The precondition showed the chain continuous  
  - The backup's start segment was hidden, `pg_control` was deleted and the Pod went into CrashLoopBackOff  
  - `get_pgbackrest_status` ran in a one-off Pod, since the instance Pod can't be `kubectl exec`'d into. It reported `broken`, with the missing segment named and `consistency_reachable=false`.
  - `restore_from_backup` refused with a WAL-continuity error.
  - Nothing was touched: the StatefulSet stayed at 1 replica and the canary file in PGDATA survived.

All of this went through the Postgres-backed auditd and the identity-enforcing agent, with policy checks on, as a real install would have it. Ain't that cool :-)

In a nutshell, this is what the PASS of that test means, but if you are like me and would like to dig deeper, here's one way (aka the forensics): querying the audit events for the test's trace IDs is not a bad place to start:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-auditd-storage exec postgres-auditd-0 -- psql -U postgres -d auditd -c "select substr(timestamp,12,12) ts, event_type, tool_name, action_class, outcome_status, outcome_duration_ms ms, left(coalesce(outcome_error,''),120) err from audit_events where trace_id like 'tr_k8sit_%' and created_at > now() - interval '3 hours' order by id desc limit 40"
      ts      |     event_type     |      tool_name      | action_class | outcome_status | ms  | err
--------------+--------------------+---------------------+--------------+----------------+-----+-----
 02:43:40.136 | tool_execution     | restart_container   | destructive  | success        | 103 |
 02:43:40.031 | policy_decision    |                     | destructive  | allow          |   0 |
 02:43:39.644 | tool_invoked       | restart_container   | destructive  |                |   0 |
 02:43:28.435 | objective_evidence |                     |              |                |   0 |
 02:43:09.220 | policy_decision    |                     | destructive  | allow          |   0 |
 02:43:08.624 | tool_invoked       | restore_from_backup | destructive  |                |   0 |
 02:43:01.048 | objective_evidence |                     |              |                |   0 |
 02:42:43.765 | policy_decision    |                     | destructive  | allow          |   0 |
 02:42:43.162 | tool_invoked       | restore_from_backup | destructive  |                |   0 |
 02:42:42.234 | objective_evidence |                     |              |                |   0 |
 02:42:17.072 | objective_evidence |                     |              |                |   0 |
(11 rows)
```

Follow on with reviewing the SysAdmin Agent's log:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system logs deploy/helpdesk-sysadmin-agent --since=8h | grep -E 'restore_from_backup|direct tool'
time=2026-10-25T02:40:43.169Z level=DEBUG msg="direct tool: ok" tool=get_pgbackrest_status target=pgbackrest-k8s-destruct ms=17933
time=2026-10-25T02:41:24.643Z level=DEBUG msg="direct tool: ok" tool=restore_from_backup target=pgbackrest-k8s-destruct ms=24980
time=2026-10-25T02:41:27.028Z level=DEBUG msg="direct tool: ok" tool=restart_container target=pgbackrest-k8s-destruct ms=1874
time=2026-10-25T02:42:17.232Z level=DEBUG msg="direct tool: ok" tool=get_pgbackrest_status target=pgbackrest-k8s-destruct ms=764
time=2026-10-25T02:42:42.750Z level=DEBUG msg="direct tool: ok" tool=get_pgbackrest_status target=pgbackrest-k8s-destruct ms=17623

time=2026-10-25T02:43:01.448Z level=WARN msg="direct tool call failed" tool=restore_from_backup target=pgbackrest-k8s-destruct err="restore_from_backup: refusing to restore — WAL continuity is broken for backup 20261020-184816F: WAL segment  000000020000000000000008, needed for backup 20261020-184816F to reach a consistent state, is missing from the repo: PostgreSQL could not start from this backup. A human must decide whether a restore that can't be verified to reach the end of the archive is still worth it" ms=18286

time=2026-10-25T02:43:39.106Z level=DEBUG msg="direct tool: ok" tool=restore_from_backup target=pgbackrest-k8s-destruct ms=30482
time=2026-10-25T02:43:40.342Z level=DEBUG msg="direct tool: ok" tool=restart_container target=pgbackrest-k8s-destruct ms=697
```

Aha, note "refusing to restore — WAL continuity is broken... WAL segment ... needed for backup... to reach a consistent state, is missing from the repo". Bingo!

Forensics are indeed cool, but hold on, there's a better way :-). The test is equipped with the optional play-by-play logging, so... you just have to ask:



```
[boris@ ~/helpdesk]$ date; HELPDESK_K8S_SYSADMIN_API_KEY=$HELPDESK_CLIENT_API_KEY HELPDESK_K8S_SYSADMIN_URL=http://localhost:11103 \
>           HELPDESK_K8S_SYSADMIN_TARGET=pgbackrest-k8s-destruct HELPDESK_K8S_PGBACKREST_CONTEXT=<context> \
>           HELPDESK_K8S_PGBACKREST_NAMESPACE=db-destruct \
>                   time go test --count=1 -tags integration -timeout 900s -v -run 'K8sCluster_RestoreRefusesOnRealWALGap' \
>                     ./agents/sysadmin/... 2>&1 | tee /tmp/k8s-refusal.log
Sun Oct 25 00:40:40 EDT 2026
=== RUN   TestIntegration_K8sCluster_RestoreRefusesOnRealWALGap
    restore_k8s_integration_test.go:253: [04:40:43.034] instance pod pgbackrest-demo-0 is ready
    restore_k8s_integration_test.go:256: [04:40:43.035] 1. precondition: the WAL chain must be continuous before anything is hidden
    restore_k8s_integration_test.go:257: [04:40:43.035] POST http://localhost:11103/tool/get_pgbackrest_status args={"target":"pgbackrest-k8s-destruct"}
    restore_k8s_integration_test.go:257: [04:40:44.466]   -> HTTP 200 in 1.431s, error="", output={"stanza":"main","status_code":0,"status_message":"ok","repo_status_message":"ok","last_backup_label":"20261020-184816F","last_backup_time":"2026-10-20T18:48:18Z","last_backup_error":false,"backup_stale":true,"wal_continuity":{"status":"continuous","backup_label":"20261020-184816F","archive_id":"16-1","backup_start_wal":"000000020000000000000008","backup_stop_wal":"000000020000000000000008","consistency_reachable":true,"latest_timeline":6,"timeline_path":[2,3,4,5,6],"recoverable_to_wal":"00000005000000000000000F","archive_end_wal":"00000005000000000000000F","archive_end_time":"2026-10-25T02:43:35Z","segment_size_bytes":16777216,"segment_size_source":"wal_header"},"backups":[{"label":"2026102… (2910 more bytes)
    restore_k8s_integration_test.go:263: [04:40:44.467]    ok: continuous, backup 20261020-184816F starts at segment 000000020000000000000008
    restore_k8s_integration_test.go:264: [04:40:44.467] 2. hide the backup's start segment 000000020000000000000008 in the repo
    restore_k8s_integration_test.go:266: [04:40:44.467] exec in pgbackrest-demo-0: su postgres -c 'set -e; g=$(ls /var/lib/pgbackrest/archive/main/16-1/0000000200000000/000000020000000000000008-*); mv "$g" "$g.hidden-by-test"'
    restore_k8s_integration_test.go:266: [04:40:44.553]   -> err=<nil> out=
    restore_k8s_integration_test.go:269: [04:40:44.553] 3. crash the instance, leaving a canary file in PGDATA
    restore_k8s_integration_test.go:270: [04:40:44.554] crash: removing pg_control, then deleting the pod so postgres cannot start
    restore_k8s_integration_test.go:270: [04:40:44.554] exec in pgbackrest-demo-0: rm -f /var/lib/postgresql/data/global/pg_control && touch /var/lib/postgresql/data/walcheck-canary
    restore_k8s_integration_test.go:270: [04:40:44.626]   -> err=<nil> out=
    restore_k8s_integration_test.go:270: [04:40:44.626] kubectl delete pod pgbackrest-demo-0 --wait=true
    restore_k8s_integration_test.go:270: [04:40:46.527]   -> err=<nil> out=pod "pgbackrest-demo-0" deleted
    restore_k8s_integration_test.go:270: [04:40:51.611] crash: pod pgbackrest-demo-0 is not ready and cannot be exec'd into (CrashLoopBackOff)
    restore_k8s_integration_test.go:272: [04:40:51.611] 4. the WAL check on the crashed instance (the agent runs it in a one-off pod) must say broken at 000000020000000000000008
    restore_k8s_integration_test.go:273: [04:40:51.611] POST http://localhost:11103/tool/get_pgbackrest_status args={"target":"pgbackrest-k8s-destruct"}
    restore_k8s_integration_test.go:273: [04:41:09.486]   -> HTTP 200 in 17.875s, error="", output={"stanza":"main","status_code":0,"status_message":"ok","repo_status_message":"ok","last_backup_label":"20261020-184816F","last_backup_time":"2026-10-20T18:48:18Z","last_backup_error":false,"backup_stale":true,"wal_continuity":{"status":"broken","detail":"WAL segment 000000020000000000000008, needed for backup 20261020-184816F to reach a consistent state, is missing from the repo: PostgreSQL could not start from this backup","backup_label":"20261020-184816F","archive_id":"16-1","backup_start_wal":"000000020000000000000008","backup_stop_wal":"000000020000000000000008","consistency_reachable":false,"latest_timeline":6,"timeline_path":[2,3,4,5,6],"archive_end_wal":"00000005000000000000000F","arc… (3162 more bytes)
    restore_k8s_integration_test.go:281: [04:41:09.486]    ok: status=broken consistency_reachable=false gaps=[{Timeline:2 FirstMissing:000000020000000000000008 LastMissing:000000020000000000000008 Count:1}]
    restore_k8s_integration_test.go:283: [04:41:09.486] 5. restore_from_backup must refuse on the broken chain
    restore_k8s_integration_test.go:284: [04:41:09.486] POST http://localhost:11103/tool/restore_from_backup args={"stanza":"main","target":"pgbackrest-k8s-destruct"}
    restore_k8s_integration_test.go:284: [04:41:27.467]   -> HTTP 422 in 17.981s, error="restore_from_backup: refusing to restore — WAL continuity is broken for backup 20261020-184816F: WAL segment 000000020000000000000008, needed for backup 20261020-184816F to reach a consistent state, is missing from the repo: PostgreSQL could not start from this backup. A human must decide whether a restore that can't be verified to reach the end of the archive is still worth it", output=
    restore_k8s_integration_test.go:288: [04:41:27.467]    ok: refused: restore_from_backup: refusing to restore — WAL continuity is broken for backup 20261020-184816F: WAL segment 000000020000000000000008, needed for backup 20261020-184816F to reach a consistent state, is missing from the repo: PostgreSQL could not start from this backup. A human must decide whether a restore that can't be verified to reach the end of the archive is still worth it
    restore_k8s_integration_test.go:289: [04:41:27.467] 6. nothing may have been touched: the StatefulSet stays at 1 replica and the canary file survives
    restore_k8s_integration_test.go:245: [04:41:27.467] kubectl get statefulset pgbackrest-demo -o jsonpath={.spec.replicas}
    restore_k8s_integration_test.go:245: [04:41:27.519]   -> err=<nil> out=1
    restore_k8s_integration_test.go:293: [04:41:27.519]    ok: replicas=1
    restore_k8s_integration_test.go:295: [04:41:27.519] one-off pod on the instance's volumes: test -f /var/lib/postgresql/data/walcheck-canary && echo present
    restore_k8s_integration_test.go:295: [04:41:29.733]   -> err=<nil> out=present
    restore_k8s_integration_test.go:298: [04:41:29.733]    ok: canary file present
    restore_k8s_integration_test.go:254: [04:41:29.733] repair: unhide any hidden segments and bring the instance back
    restore_k8s_integration_test.go:254: [04:41:29.767] one-off pod on the instance's volumes: for g in $(find /var/lib/pgbackrest/archive -name '*.hidden-by-test'); do mv "$g" "${g%.hidden-by-test}"; done; rm -rf /var/lib/pgbackrest/pg_wal.saved-*
    restore_k8s_integration_test.go:254: [04:41:34.085]   -> err=<nil> out=
    restore_k8s_integration_test.go:254: [04:41:34.085] POST http://localhost:11103/tool/restore_from_backup args={"stanza":"main","target":"pgbackrest-k8s-destruct"}
    restore_k8s_integration_test.go:254: [04:42:06.240]   -> HTTP 200 in 32.156s, error="", output={"output":"P00   WARN: environment contains invalid option 'demo-service-port'\nP00   WARN: environment contains invalid option 'demo-port-5432-tcp-proto'\nP00   WARN: environment contains invalid option 'demo-port-22-tcp-addr'\nP00   WARN: environment contains invalid option 'demo-service-port-postgres'\nP00   WARN: environment contains invalid option 'demo-port-22-tcp-port'\nP00   WARN: environment contains invalid option 'demo-service-port-ssh'\nP00   WARN: environment contains invalid option 'demo-port-5432-tcp-addr'\nP00   WARN: environment contains invalid option 'demo-port-5432-tcp-port'\nP00   WARN: environment contains invalid option 'demo-port'\nP00   WARN: environment contains inv… (1341 more bytes)
    restore_k8s_integration_test.go:254: [04:42:06.241] POST http://localhost:11103/tool/restart_container args={"reason":"test repair","target":"pgbackrest-k8s-destruct"}
    restore_k8s_integration_test.go:254: [04:42:08.056]   -> HTTP 200 in 1.816s, error="", output={"server_id":"pgbackrest-k8s-destruct","runtime":"kubectl","target":"pgbackrest-demo","success":true,"output":"StatefulSet pgbackrest-demo scaled from 0 to 1 replicas"}
    restore_k8s_integration_test.go:254: [04:42:17.313] instance pod pgbackrest-demo-0 is ready
--- PASS: TestIntegration_K8sCluster_RestoreRefusesOnRealWALGap (94.34s)
PASS
ok  	helpdesk/agents/sysadmin	94.832s
       97.15 real         2.31 user         1.15 sys
```

So in the nutshell, here's what that show (under -v flag):  
  - Every tool call: the POST with its args, then the HTTP status, duration, error text and output clipped to 700 bytes. The refusal shows as the full error string. W00t!!  
  - Every kubectl call and every command run in the Pod or in a one-off Pod: the command, then the error and output clipped to 300 bytes  
  - The crash: a line when it starts and one when the Pod reaches CrashLoopBackOff and can't be exec'd into. The polling loop that waits for that stays quiet  
  - The refusal test, numbered: 1 precondition, 2 hide, 3 crash, 4 broken verdict, 5 refusal, 6 nothing touched. Each passing check logs an ok: line with the evidence. That includes the status, `consistency_reachable`, the gap list, the refusal text, the replica count and the canary. W00f  
  - The repair in cleanup, so you see how the instance was brought back  

Nice, huh? So there you have it: go the forensics route by digging into the audit or run the test with the verbose output to dig deeper into the details.


## VM/host





## K8s

```
[boris@ ~/helpdesk]$ kubectl -n db delete statefulset pgbackrest-demo
statefulset.apps "pgbackrest-demo" deleted

[boris@ ~/helpdesk]$ kubectl -n db delete pvc pgdata-pgbackrest-demo-0 pgbackrest-repo-pgbackrest-demo-0
persistentvolumeclaim "pgdata-pgbackrest-demo-0" deleted
persistentvolumeclaim "pgbackrest-repo-pgbackrest-demo-0" deleted

[boris@ ~/helpdesk]$ kubectl apply -n db -f testing/k8s/pgbackrest-demo.yaml
service/pgbackrest-demo unchanged
statefulset.apps/pgbackrest-demo created

[boris@ ~/helpdesk]$ kubectl -n db rollout status statefulset/pgbackrest-demo --timeout=180s
Waiting for 1 pods to be ready...
partitioned roll out complete: 1 new pods have been updated...
```

For the purpose of a manual test, let's take a fresh backup (simply by `exec`-ing into the pgBackRest demo Pod):

```
[boris@ ~/helpdesk]$ kubectl -n db exec pgbackrest-demo-0 -c postgres -- su postgres -c 'pgbackrest --stanza=main check && pgbackrest --stanza=main --type=full backup && pgbackrest --stanza=main info'"
stanza: main
    status: ok
    cipher: none

    db (current)
        wal archive min/max (16): 000000010000000000000001/000000010000000000000004

        full backup: 20261020-135030F
            timestamp start/stop: 2026-10-20 13:50:30+00 / 2026-10-20 13:50:32+00
            wal start/stop: 000000010000000000000003 / 000000010000000000000004
            database size: 29.3MB, database backup size: 29.3MB
            repo1: backup set size: 3.9MB, backup size: 3.9MB
```

Cool, let's run `helm upgrade and...

```
[boris@ ~/helpdesk]$ helm upgrade helpdesk deploy/helm/helpdesk -n helpdesk-system -f testing/k8s/helm-values-pgbackrest.yaml
Release "helpdesk" has been upgraded. Happy Helming!
NAME: helpdesk
LAST DEPLOYED: Tue Oct 20 09:55:39 2026
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

helpdesk-client is the authenticated CLI for operators.
Download the binary for your OS from the release tarball, or — on Linux — extract it
directly from the image:

  # Linux only (image contains a Linux/amd64 or Linux/arm64 binary)
  docker run --rm --entrypoint cat \
    helpdesk:latest \
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
```

Allright, let's bounce the Deployments and check the status of the Pods:

```
[boris@ ~/helpdesk]$ k -nhelpdesk-system rollout restart deploy
deployment.apps/helpdesk-auditd restarted
deployment.apps/helpdesk-database-agent restarted
deployment.apps/helpdesk-gateway restarted
deployment.apps/helpdesk-incident-agent restarted
deployment.apps/helpdesk-k8s-agent restarted
deployment.apps/helpdesk-orchestrator restarted
deployment.apps/helpdesk-research-agent restarted
deployment.apps/helpdesk-sysadmin-agent restarted

[boris@ ~/helpdesk]$ k -nhelpdesk-system get po
NAME                                       READY   STATUS    RESTARTS   AGE
helpdesk-auditd-66cbfdff77-2kq8n           1/1     Running   0          46s
helpdesk-database-agent-565496bc5f-jfwk5   1/1     Running   0          46s
helpdesk-gateway-66dfc67cb6-s7rqb          1/1     Running   0          46s
helpdesk-incident-agent-74d568665f-np8z8   1/1     Running   0          46s
helpdesk-k8s-agent-68f556d756-b7xxv        1/1     Running   0          45s
helpdesk-orchestrator-75c6fcbbfb-lj8bg     1/1     Running   0          46s
helpdesk-research-agent-8669697b79-qf5r2   1/1     Running   0          45s
helpdesk-sysadmin-agent-84785cf9c5-gx77v   1/1     Running   0          45s
```

Let's forward the Service's port, so that we can access the Gateway from your laptop and run the health triage directly through the Gateway API (with no faulttest involved for now), against the pgbackrest-k8s alias:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system port-forward svc/helpdesk-gateway 8080:8080 &
[1] 93756
[boris@ ~/helpdesk]$ Forwarding from [::1]:8080 -> 8080

[boris@ ~/helpdesk]$ PB=$(curl -s -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" "localhost:8080/api/v1/fleet/playbooks?series_id=pbs_pgbackrest_health_triage" | jq -r '.playbooks[0].playbook_id')
[boris@ ~/helpdesk]$ echo $PB
pb_f98376f2

[boris@ ~/helpdesk]$ curl -s -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" -X POST "localhost:8080/api/v1/fleet/playbooks/$PB/run" -H 'Content-Type: application/json'  -d '{"connection_string":"pgbackrest-k8s"}' | jq '{status, findings: .findings_summary, gate_reason, transition_target, objective_evidence_signals}'
{
  "status": "pending_gate",
  "findings": null,
  "gate_reason": "trust_not_earned",
  "transition_target": "pbs_pgbackrest_backup_remediate",
  "objective_evidence_signals": [
    "pgbackrest_backup_unhealthy"
  ]
}
```

What does the output show?
  - It shows that the alias resolves in the Gateway, that the SysAdmin agent ran the hop in-cluster and that the Gateway stopped the run at its gate.
  - `trust_not_earned` means the series has no stable, clean stability cert for the current model on record, so it fails closed. 
     A chain can't continue on its own without a certification (and yes, this is the same gate we saw on the VM chain as well earlier).
  - `findings: null` is because the run is paused before a finding is recorded.

it wants to hand off to pbs_pgbackrest_backup_remediate, because the backup is four days old, so
    the backup_stale signal would apply. The WAL-gap fault had already been torn down, so the chain should be healthy. That guess isn't confirmed.

But the more interesting part is next: the health triage ran against the in-cluster Pod and read the backup as unhealthy. 
The `pgbackrest_backup_unhealthy` signal fired, which is the `backup_stale` case: the last full backup is more than a few days old (20261020-135030F). Now, the triage wants to hand off to `pbs_pgbackrest_backup_remediat`e, which would take a fresh backup, but the Gateway holds the hop at the gate because `trust_not_earned` applies to that playbook too, so the run is paused waiting for approval.

What the run shows about the gates: both layers did their job here. The deterministic signal fired from the tool's structured result and the trust gate stopped an auto-chain. That's the same behaviour as the VM chain, now seen against a K8s target (the gate is now open, the run is pending and proceeding would approve the backup).

It's worth noting that this is not the `broken-chain` test (well, because the WAL-gap fault from the other tests had been torn down and the chain is, well, boringly continuos).

If you want to dig deeper, reviewing the Gateway log is not a bad idea to start:

```
[boris@ ~/helpdesk]$ k -nhelpdesk-system logs deploy/helpdesk-gateway --since=10m | grep -iE "error|warn|pgbackrest-k8s|playbook" | tail -20
time=2026-10-24T16:11:36.859Z level=INFO msg="authz: request denied" pattern="GET /api/v1/fleet/playbooks" principal="" anonymous=true err="authentication required: this endpoint requires authentication"
time=2026-10-24T16:11:49.673Z level=INFO msg="authz: request denied" pattern="GET /api/v1/fleet/playbooks" principal="" anonymous=true err="authentication required: this endpoint requires authentication"
time=2026-10-24T16:13:18.194Z level=INFO msg="authz: request denied" pattern="POST /api/v1/fleet/playbooks/{playbookID}/run" principal="" anonymous=true err="authentication required: this endpoint requires authentication"
time=2026-10-24T16:13:26.061Z level=INFO msg="authz: request denied" pattern="POST /api/v1/fleet/playbooks/{playbookID}/run" principal="" anonymous=true err="authentication required: this endpoint requires authentication"
time=2026-10-24T16:13:40.240Z level=DEBUG msg="triage prompt assembled" playbook=pbs_pgbackrest_health_triage prompt_len=8592 prompt="Your target is connection_string=\"pgbackrest-k8s\". Follow the Expert Guidance below — use get_pgbackrest_status as instructed. Do not call check_connection (that is a DB-agent tool, not available here).\nServer type: Kubernetes pod (cluster: k8s-cluster, namespace: db), pod selector: app=pgbackrest-demo.\nKubectl commands, pod log retrieval, and K8s event lookups are applicable to this server.\n\n## Response Protocol\nDo NOT write a CONCLUSION section or end with '---'. Close your response with this exact block (plain text, no markdown, no bold, no backticks):\n\nHYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: \"<verbatim quote from tool output>\"\nHYPOTHESIS_2: <alternative hypothesis> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason>\nROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: <one-sentence diagnosis and recommended action>\nTRANSITION_TO: <series_id>   — use this when the Expert Guidance 'Final step' specifies TRANSITION_TO (same-domain handoff to the expected remediation playbook)\nESCALATE_TO: <series_id or \"none\">   — use this for true out-of-scope escalations to a different domain; use \"none\" if no escalation is needed\nEmit exactly one of TRANSITION_TO or ESCALATE_TO (not both). Follow the 'Final step' in Expert Guidance to determine which signal and which series_id.\n\nRules: list hypotheses in descending confidence order; EVIDENCE must be a single short verbatim quote copied exactly from one tool's output — never join two facts with \"and\"/\",\" into one quote, never paraphrase or summarize what a tool returned, and never combine output from more than one tool call into a single quote (if two facts matter, put the second in a REJECTED reason or FINDINGS instead); every non-primary hypothesis must have REJECTED with a reason; CONFIDENCE is 0.0–1.0.\n\n## Playbook: pgBackRest Backup-Job Health Triage\n\n## Objective\nAssess pgBackRest backup-job health — the backup-taking precondition,\ndistinct from the database agent's WAL-archiving triage\n(pbs_db_backup_health_triage). Distinguishes four conditions that all look\nambiguous without a tool: never backed up (a policy question, not a\nfailure), the repo/stanza itself broken (a filesystem/permissions\nproblem), a stale backup (schedule/retention problem, repo itself is\nfine), a backup whose archived WAL chain is broken (v1.2: it exists but\ncan't be restored to the end of the archive), and healthy.\n\n\n## Expert Guidance\nUse get_pgbackrest_status. It reports the stanza's own status_code/\nstatus_message, a more specific repo_status_message when the stanza-level\nmessage is generic, the most recent backup's label/time, and a computed\nbackup_stale flag (true only when a most-recent backup exists and is\nolder than the requested/default staleness bound), and wal_continuity:\nwhether the archived WAL needed to restore that backup to the end of the\narchive is actually in the repo. wal_continuity.status is one of:\n- continuous: every segment from the backup to the archive end is present.\n- broken: a segment is missing, or the backup isn't on the newest\n  timeline's history. wal_continuity.detail says which, gaps lists the\n  missing segments, recoverable_to_wal is where recovery would actually\n  stop, and consistency_reachable=false means PostgreSQL could not even\n  start from this backup.\n- unverified: the check could not run (wal_continuity.detail says why).\n  Nothing is known either way.\n- no_backup: there is no backup to check.\n\nThree fields, three different conclusions — do not conflate them:\n- status_code != 0 means pgBackRest itself cannot determine backup state\n  at all (a filesystem/permissions/config problem with the repo). The\n  stanza name may show as \"[invalid]\" in this case — that is itself\n  diagnostic, not a tool bug.\n- backup_stale=true with status_code=0 means the repo is fine but the\n  most recent successful backup is older than expected — a\n  scheduling/retention problem, not a filesystem one.\n- last_backup_time being empty (no LastBackupTime) with status_code=0\n  means backups were never taken at all — distinct from \"stale,\" and not\n  necessarily a failure: it may just mean backups were never configured\n  for this instance.\n\nFive mutually exclusive endings, pick exactly one based on what you found:\n\nEnding A — never backed up (status_code=0, no backup has ever succeeded):\nThis is a policy question for the customer, not a diagnosis. Do not\nrecommend enabling backups as if it were an obvious fix; report the\nfinding and let a human decide.\nHYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: \"<verbatim quote from tool output>\"\nROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: pgBackRest stanza has no backups on record; no backup-taking capability exists for this instance until an operator configures and runs one\nESCALATE_TO: none\n\nEnding B — repo/stanza broken (status_code != 0):\nHYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: \"<verbatim quote from tool output>\"\nHYPOTHESIS_2: <alternative if considered> | CONFIDENCE: <0.0–1.0> | REJECTED: <one-sentence reason why ruled out>\nROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: status_code=<N>; status_message=<message>; repo_status_message=<message, if more specific than status_message>\nESCALATE_TO: none\n\nEnding C — backup stale (status_code=0, backup_stale=true):\nHYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: \"<verbatim quote from tool output>\"\nROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: status_code=0; backup_stale=true; last_backup_label=<label>; last_backup_time=<timestamp>; wal_continuity=<status>\nTRANSITION_TO: pbs_pgbackrest_backup_remediate\n\nEnding E — WAL chain broken (status_code=0, backup_stale=false,\nwal_continuity.status=broken):\nThe backup exists and is recent, but it cannot be restored to the end of\nthe archive: recovery would stop at the gap, or not start at all. This\nneeds a human: someone has to decide whether to take a fresh full backup\n(which starts a new, unbroken chain from now) and find out why WAL went\nmissing. Do not transition to a restore, even if prior findings flag data\nloss.\nHYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: \"<verbatim quote from tool output>\"\nROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: status_code=0; backup_stale=false; last_backup_label=<label>; wal_continuity=broken; wal_detail=<wal_continuity.detail>; recoverable_to_wal=<recoverable_to_wal>; consistency_reachable=<true|false>\nESCALATE_TO: none\n\nEnding D — healthy (status_code=0, backup_stale=false, a backup has\nsucceeded, wal_continuity.status continuous or unverified):\nHYPOTHESIS_1: <primary hypothesis> | CONFIDENCE: <0.0–1.0> | EVIDENCE: \"<verbatim quote from tool output>\"\nROOT_CAUSE: HYPOTHESIS_1\nFINDINGS: status_code=0; backup_stale=false; last_backup_label=<label>; last_backup_time=<timestamp>; wal_continuity=<status>; archive_end_time=<wal_continuity.archive_end_time>\n\nCheck the findings you were handed from the prior triage (if any) before\nchoosing your next line. If they explicitly state\ndata_loss_restore_candidate=true — meaning a prior triage already\nconfirmed actual data-directory loss/corruption on this instance, not a\nroutine backup-health check — AND wal_continuity.status is continuous,\nthe backup you just confirmed is exactly what's needed to recover it:\n  TRANSITION_TO: pbs_pgbackrest_restore_remediate\nIf data_loss_restore_candidate=true but wal_continuity.status is\nunverified, a restore can't be confirmed safe — restore_from_backup\nitself will refuse. Report that in FINDINGS for a human instead:\n  ESCALATE_TO: none\nOtherwise, this is a routine health check with nothing to do: end the\nFINDINGS line above with \"; recommended=no_changes_needed\" (it records the\nrun as resolved rather than waiting on a human; never add it on the two\ndata-loss lines above), then:\n  ESCALATE_TO: none\n\n\n## Same-domain transitions\nWhen triage completes successfully and a follow-on remediation under the same agent is appropriate, the allowed next playbooks are (by series ID): pbs_pgbackrest_backup_remediate, pbs_pgbackrest_restore_remediate\n\n## Constraints\n- Use only read-only diagnostic tools. Do not execute any write or destructive operations.\n- Collect evidence, form a hypothesis, and if the evidence contradicts it, back out and pursue a different hypothesis.\n- When you reach a clear diagnosis, present your findings and recommended remediation steps.\n- Do NOT execute remediation — describe it for operator review and approval.\n- Use ONLY `connection_string` = `pgbackrest-k8s`. Do not query any other server, even if your context lists others.\n\n"
time=2026-10-24T16:13:54.563Z level=INFO msg="playbook: gate pending — awaiting operator acknowledgment" run_id=plr_7ffc66c3 gate_type=transition next_series=pbs_pgbackrest_backup_remediate confidence_warning=""
```


K8s run shows the WAL check works in-cluster:
   `get_pgbackrest_status` runs over `kubectl exec`, reads the repo, and reports a correct verdict on a healthy chain.
   It also shows the gate rules load.
   That's the reporting half of item 1, on the healthy path.
   The VM run covers the guard's "allow" decision inside a real restore chain.








```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest run --ids db-pgbackrest-wal-gap-k8s \
   --via-gateway \
   --gateway $HELPDESK_GATEWAY_URL \
   --api-key $HELPDESK_CLIENT_API_KEY \
   --conn pgbackrest-k8s \
   --context k8s_context

time=2026-10-24T00:35:02.828-04:00 level=INFO msg=--conn host=pgbackrest-k8s

--- Testing: pgBackRest WAL chain broken (Kubernetes pod) (db-pgbackrest-wal-gap-k8s) ---
time=2026-10-24T00:35:02.832-04:00 level=INFO msg="executing injection spec" type=shell_exec phase=inject
time=2026-10-24T00:35:03.004-04:00 level=INFO msg="shell_exec completed" output="Defaulted container \"postgres\" out of: postgres, repo-owner (init)\nhid /var/lib/pgbackrest/archive/main/16-1/0000000100000000/000000010000000000000003-       ee004d38880ce66779b6b3a84d6ec1487e9962f9.gz"
time=2026-10-24T00:35:03.995-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-pgbackrest-wal-gap-k8s series_id=pbs_pgbackrest_health_triage playbook_id=pb_f98376f2 gateway=http://localhost:8080 agent-conn=pgbackrest-k8s
time=2026-10-24T00:35:20.549-04:00 level=INFO msg="executing injection spec" type=shell_exec phase=teardown
time=2026-10-24T00:35:20.839-04:00 level=INFO msg="shell_exec completed" output="Defaulted container \"postgres\" out of: postgres, repo-owner (init)"
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% judge=100%)

=== Fault Test Report: fc7ad841 ===

[PASS] pgBackRest WAL chain broken (Kubernetes pod) (db-pgbackrest-wal-gap-k8s) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

Report written to ./faulttest-fc7ad841.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "fc7ad841",
  "timestamp": "2026-10-24T04:35:20Z",
  "results": [
    {
      "failure_id": "db-pgbackrest-wal-gap-k8s",
      "failure_name": "pgBackRest WAL chain broken (Kubernetes pod)",
      "category": "database",
      "score": 1,
      "passed": true,
      "run_id": "plr_20317873",
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "ordering_pass": true,
      "tool_evidence_mode": "structured",
      "response_text": "I will check the pgBackRest backup-job health for the pgbackrest-k8s target. Let me start by calling get_pgbackrest_status to assess the stanza status, most recent backup, staleness, and WAL continuity.\nThe status    check reveals critical findings. Let me analyze:\n\n**Key observations:**\n- `status_code=0`: The pgBackRest repository itself is functioning correctly.\n- `backup_stale=true`: The most recent backup (20261020-135030F) is stale, dated 2026-  10-20T13:50:32Z.\n- `last_backup_label=\"20261020-135030F\"`: A full backup exists and is recorded.\n- `wal_continuity.status=\"broken\"`: The archived WAL chain is broken.\n- `consistency_reachable=false`: PostgreSQL cannot start from this  backup.\n- **Critical gap**: `gaps=[{\"count\":1,\"first_missing\":\"000000010000000000000003\",\"last_missing\":\"000000010000000000000003\",\"timeline\":1}]` — WAL segment 000000010000000000000003 is missing, and it is needed for the       backup to reach a consistent state.\n- The detail field explicitly states: *\"WAL segment 000000010000000000000003, needed for backup 20261020-135030F to reach a consistent state, is missing from the repo: PostgreSQL could not start from     this backup\"*\n\nThis is an Ending E scenario (WAL chain broken): the backup exists and is recent (though stale by schedule), but it cannot be restored because a critical WAL segment is missing. A restore would fail with consistency errors, and PostgreSQL could not start from this backup state.\n\nHYPOTHESIS_1: pgBackRest backup WAL chain is broken due to missing WAL segment 000000010000000000000003; the latest backup (20261020-135030F) cannot be restored to a consistent state  and PostgreSQL cannot start from it | CONFIDENCE: 0.95 | EVIDENCE: \"wal_continuity.status=\\\"broken\\\"; consistency_reachable=false; detail=\\\"WAL segment 000000010000000000000003, needed for backup 20261020-135030F to reach a consistent state, is missing from the repo: PostgreSQL could not start from this backup\\\"\"\nHYPOTHESIS_2: The backup is simply stale and will work on restore | CONFIDENCE: 0.05 | REJECTED: wal_continuity.status is explicitly \"broken\" and           consistency_reachable is false, not a staleness issue but an irreparable WAL chain break\nROOT_CAUSE: HYPOTHESIS_1",
      "duration": "17.543675084s",
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
      "primary_hypothesis": "pgBackRest backup WAL chain is broken due to missing WAL segment 000000010000000000000003; the latest backup (20261020-135030F) cannot be restored to a consistent state and PostgreSQL cannot start from it",
      "secondary_hypothesis": "The backup is simply stale and will work on restore",
      "secondary_confidence": 0.05,
      "hypotheses": [
        {
          "text": "pgBackRest backup WAL chain is broken due to missing WAL segment 000000010000000000000003; the latest backup (20261020-135030F) cannot be restored to a consistent state and PostgreSQL cannot start from it",
          "confidence": 0.95,
          "is_primary": true
        },
        {
          "text": "The backup is simply stale and will work on restore",
          "confidence": 0.05,
          "is_primary": false,
          "rejected_reason": "wal_continuity.status is explicitly \"broken\" and consistency_reachable is false, not a staleness issue but an irreparable WAL chain break"
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


[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault incidents plr_20317873 --summary --gateway $HELPDESK_GATEWAY_URL
Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.32.0-684e43f  ·  host: helpdesk-gateway-65c69bfd8c-fhhxz

INCIDENT plr_20317873 — needs_human after 16s

1. TRIAGE        pbs_pgbackrest_health_triage
   status_code=0; backup_stale=true; last_backup_label=20261020-135030F; wal_continuity=broken; wal_detail=WAL s…

Overall: 100% (diagnosis 100%)
```

So the fault passed and the teardown left the Pod as it was. Let's review the results item by item:

  - Score: 100% on keywords, tools and diagnosis (run plr_20317873, 17.5 seconds).
  - The injection: it hid exactly the backup's start segment, 000000010000000000000003.
  - The diagnosis: the agent reported `wal_continuity.status="broken"` and `consistency_reachable=false` and quoted the detail: "WAL segment 000000010000000000000003, needed for backup 20261020-135030F to reach a consistent state, is missing
    from the repo…"
  - The hypotheses: the agent ranked the broken chain at 0.95 and rejected "the backup is just stale" at 0.05. `backup_stale=true` was also true here, since the backup is four days old and it still named the broken chain as the cause.
  - The signal: `pgbackrest_wal_chain_broken` fired and was confirmed, so it was the deterministic check that caught it, not just the model's reading.
  - The teardown: I checked R/O in the DB Pod. `pgbackrest info` shows status ok and the archive back to …01 through …04 and no `*.hidden-by-fault` files are left.

   What does this demonstrate?
   Well, the run went from your laptop through the in-cluster Gateway and SysAdmin agent, then `kubectl exec` into the Pod and back.
   No SSH was involved. It also passed through the policy and audit path on the Postgres-backed auditd.
   Together with the earlier `make integration-k8s` results, that covers the `broken-chain` detection on Kubernetes end to end.

  Limits:
  - The harness ran without `--judge` (the report says so), so the semantic score wasn't used. Add `--judge` if you want that in the record.
  - This covers detection and the stop at a human. It doesn't cover an agent-driven restore on K8s yet.




