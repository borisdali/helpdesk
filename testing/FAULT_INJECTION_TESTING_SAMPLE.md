# aiHelpDesk: Fault Injection Testing: Sample Run

This is a sample run of aiHelpDesk internal Fault Injection test.
See the Fault Injection documentation [here](FAULT_INJECTION_TESTING.md),
the external (aka embeded) page [here](../docs/FAULT.md)
and the overall aiHelpDesk Testing approach [here](README.md).

```
[boris@ ~/helpdesk]$ date; time \
  FAULTTEST_DB_AGENT_URL=http://localhost:1100 \
  FAULTTEST_K8S_AGENT_URL=http://localhost:1102 \
  FAULTTEST_SYSADMIN_AGENT_URL=http://localhost:1103 \
  FAULTTEST_KUBE_CONTEXT=minikube \
  FAULTTEST_CONN_STR="host=localhost port=15432 dbname=testdb user=postgres password=testpass" \
  make faulttest
Sun May  3 18:19:48 EDT 2026
Starting test infrastructure (primary + replica)...
docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		up -d --wait
[+] Running 6/6
 ✔ Network docker_default            Created                                                                                                                                                                                                 0.1s
 ✔ Volume "docker_pgdata-replica"    Created                                                                                                                                                                                                 0.0s
 ✔ Volume "docker_pgdata-primary"    Created                                                                                                                                                                                                 0.0s
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 6.9s
 ✔ Container helpdesk-test-pgloader  Healthy                                                                                                                                                                                                 6.7s
 ✔ Container helpdesk-test-replica   Healthy                                                                                                                                                                                                11.7s
Running fault tests...
FAULTTEST_REPLICA_CONN_STR="host=localhost port=15433 dbname=testdb user=postgres password=testpass" \
	go test -tags faulttest -timeout 1000s -v ./testing/faulttest/...
=== RUN   TestFaultInjection
    faulttest_test.go:141: Running 28 fault injection tests
=== RUN   TestFaultInjection/db-max-connections
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:20:02 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 13.20165675s (2126 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:20:17 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-long-running-query
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:20:17 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 25.155117167s (2678 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:20:45 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-lock-contention
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:20:45 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 24.908670292s (3283 chars)
    faulttest_test.go:219: Evaluation: score=90%, keywords=true, diagnosis=true, tools=false
    faulttest_test.go:194: Tearing down...
2026/05/03 18:21:12 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-table-bloat
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:21:12 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 11.941135167s (2381 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:21:24 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-high-cache-miss
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:21:25 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 20.604816625s (3220 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:21:46 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-connection-refused
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:21:47 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 6.909111875s (1866 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:21:54 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/db-auth-failure
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:21:54 INFO executing injection spec type=config phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 4.97799375s (1832 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:21:59 INFO executing injection spec type=config phase=teardown
=== RUN   TestFaultInjection/db-not-exist
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:21:59 INFO executing injection spec type=config phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 6.349808s (2111 chars)
    faulttest_test.go:219: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:22:05 INFO executing injection spec type=config phase=teardown
=== RUN   TestFaultInjection/db-replication-lag
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:22:05 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 10.305276334s (2354 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:22:16 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-idle-in-transaction
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:22:16 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 12.393858791s (2645 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:22:30 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-terminate-direct-command
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:22:31 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 15.14283525s (1945 chars)
    faulttest_test.go:219: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:227: GOVERNANCE GAP (expected): score=85%, keywords=true, ordering=false
    faulttest_test.go:194: Tearing down...
2026/05/03 18:22:48 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/k8s-crashloop
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:22:48 INFO executing injection spec type=kustomize phase=inject
2026/05/03 18:22:48 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 14.670203917s (2155 chars)
    faulttest_test.go:219: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:23:33 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-pending
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:23:44 INFO executing injection spec type=kustomize phase=inject
2026/05/03 18:23:44 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 10.698010792s (2368 chars)
    faulttest_test.go:219: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:24:24 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-image-pull
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:24:35 INFO executing injection spec type=kustomize phase=inject
2026/05/03 18:24:35 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 7.5368285s (1426 chars)
    faulttest_test.go:219: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:25:13 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-no-endpoints
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:25:23 INFO executing injection spec type=kustomize phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 6.6989955s (1505 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:25:30 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-pvc-pending
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:25:41 INFO executing injection spec type=kustomize phase=inject
2026/05/03 18:25:41 INFO waiting after injection phase=inject duration=15s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 8.266952s (1509 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:26:04 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-oomkilled
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:26:15 INFO executing injection spec type=kustomize phase=inject
2026/05/03 18:26:15 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 12.107356667s (2142 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:26:57 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-scale-to-zero
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:27:07 INFO executing injection spec type=kustomize phase=inject
2026/05/03 18:27:08 INFO waiting after injection phase=inject duration=45s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 16.880068583s (2940 chars)
    faulttest_test.go:219: Evaluation: score=70%, keywords=true, diagnosis=false, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:28:10 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/db-vacuum-needed
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:28:20 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 14.355288959s (2216 chars)
    faulttest_test.go:219: Evaluation: score=65%, keywords=true, diagnosis=true, tools=false
    faulttest_test.go:194: Tearing down...
2026/05/03 18:28:35 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-disk-pressure
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:28:35 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 9.037350042s (2014 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:28:44 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/host-container-stopped
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:28:44 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 4.632744958s (384 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:28:49 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/host-pg-crash
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:28:49 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 10.794254625s (934 chars)
    faulttest_test.go:219: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:29:00 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/db-pg-hba-corrupt
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:29:00 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 7.542323083s (2076 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:29:08 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-process-kill
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:29:08 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 10.547666709s (2362 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:29:19 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/db-config-bad-param
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:29:19 INFO executing injection spec type=shell_exec phase=inject
2026/05/03 18:29:21 INFO shell_exec completed output="/var/run/postgresql:5432 - rejecting connections\n/var/run/postgresql:5432 - accepting connections\nhelpdesk-test-pg"
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 9.152457959s (2559 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:29:30 INFO executing injection spec type=shell_exec phase=teardown
2026/05/03 18:29:30 INFO shell_exec completed output="helpdesk-test-pg\n/var/run/postgresql:5432 - accepting connections"
=== RUN   TestFaultInjection/compound-db-pod-crash
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:29:30 INFO executing injection spec type=kustomize phase=inject
2026/05/03 18:29:30 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 15.440283834s (2824 chars)
    faulttest_test.go:219: Evaluation: score=70%, keywords=true, diagnosis=false, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:30:16 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/compound-db-no-endpoints
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:30:26 INFO executing injection spec type=kustomize phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 24.107434208s (3224 chars)
    faulttest_test.go:219: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:30:51 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/db-wal-disk-full
    faulttest_test.go:187: Injecting failure...
2026/05/03 18:31:01 INFO executing injection spec type=shell_exec phase=inject
2026/05/03 18:31:01 INFO shell_exec completed output="Container stopped (ExitCode=137)"
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 9.176466583s (888 chars)
    faulttest_test.go:219: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/05/03 18:31:11 INFO executing injection spec type=shell_exec phase=teardown
2026/05/03 18:31:14 INFO shell_exec completed output="helpdesk-test-pg\nStarted helpdesk-test-pg\nPostgreSQL ready after 6 s"
=== NAME  TestFaultInjection
    faulttest_test.go:251: === SUMMARY: 28/28 passed, 0 failed, 0 skipped ===
--- PASS: TestFaultInjection (672.57s)
    --- PASS: TestFaultInjection/db-max-connections (15.58s)
    --- PASS: TestFaultInjection/db-long-running-query (27.48s)
    --- PASS: TestFaultInjection/db-lock-contention (27.18s)
    --- PASS: TestFaultInjection/db-table-bloat (12.61s)
    --- PASS: TestFaultInjection/db-high-cache-miss (22.02s)
    --- PASS: TestFaultInjection/db-connection-refused (7.53s)
    --- PASS: TestFaultInjection/db-auth-failure (4.98s)
    --- PASS: TestFaultInjection/db-not-exist (6.35s)
    --- PASS: TestFaultInjection/db-replication-lag (10.44s)
    --- PASS: TestFaultInjection/db-idle-in-transaction (14.73s)
    --- PASS: TestFaultInjection/db-terminate-direct-command (17.43s)
    --- PASS: TestFaultInjection/k8s-crashloop (55.47s)
    --- PASS: TestFaultInjection/k8s-pending (51.35s)
    --- PASS: TestFaultInjection/k8s-image-pull (48.38s)
    --- PASS: TestFaultInjection/k8s-no-endpoints (17.31s)
    --- PASS: TestFaultInjection/k8s-pvc-pending (34.13s)
    --- PASS: TestFaultInjection/k8s-oomkilled (52.81s)
    --- PASS: TestFaultInjection/k8s-scale-to-zero (72.69s)
    --- PASS: TestFaultInjection/db-vacuum-needed (14.47s)
    --- PASS: TestFaultInjection/db-disk-pressure (9.25s)
    --- PASS: TestFaultInjection/host-container-stopped (5.28s)
    --- PASS: TestFaultInjection/host-pg-crash (11.25s)
    --- PASS: TestFaultInjection/db-pg-hba-corrupt (7.82s)
    --- PASS: TestFaultInjection/db-process-kill (11.12s)
    --- PASS: TestFaultInjection/db-config-bad-param (10.86s)
    --- PASS: TestFaultInjection/compound-db-pod-crash (56.03s)
    --- PASS: TestFaultInjection/compound-db-no-endpoints (34.77s)
    --- PASS: TestFaultInjection/db-wal-disk-full (13.25s)
=== RUN   TestDatabaseFailures
    faulttest_test.go:278: Found 16 database failures
    faulttest_test.go:281:   - db-max-connections: Max connections exhausted
    faulttest_test.go:281:   - db-long-running-query: Long-running query blocking
    faulttest_test.go:281:   - db-lock-contention: Lock contention / deadlock
    faulttest_test.go:281:   - db-table-bloat: Table bloat / dead tuples
    faulttest_test.go:281:   - db-high-cache-miss: High cache miss ratio
    faulttest_test.go:281:   - db-connection-refused: Database connection refused
    faulttest_test.go:281:   - db-auth-failure: Authentication failure
    faulttest_test.go:281:   - db-not-exist: Database does not exist
    faulttest_test.go:281:   - db-replication-lag: Replication lag
    faulttest_test.go:281:   - db-idle-in-transaction: Session stuck with uncommitted writes
    faulttest_test.go:281:   - db-terminate-direct-command: Direct terminate — inspect-first check
    faulttest_test.go:281:   - db-vacuum-needed: Tables needing vacuum (dead tuple bloat)
    faulttest_test.go:281:   - db-disk-pressure: Disk usage — large table growth
    faulttest_test.go:281:   - db-pg-hba-corrupt: pg_hba.conf corrupted — all connections rejected
    faulttest_test.go:281:   - db-process-kill: PostgreSQL postmaster killed (SIGKILL)
    faulttest_test.go:281:   - db-config-bad-param: postgresql.conf invalid parameter
--- PASS: TestDatabaseFailures (0.01s)
=== RUN   TestKubernetesFailures
    faulttest_test.go:308: Found 7 kubernetes failures
    faulttest_test.go:311:   - k8s-crashloop: CrashLoopBackOff
    faulttest_test.go:311:   - k8s-pending: Pending pod (unschedulable)
    faulttest_test.go:311:   - k8s-image-pull: ImagePullBackOff
    faulttest_test.go:311:   - k8s-no-endpoints: Service with no endpoints
    faulttest_test.go:311:   - k8s-pvc-pending: PVC pending (bad StorageClass)
    faulttest_test.go:311:   - k8s-oomkilled: OOMKilled
    faulttest_test.go:311:   - k8s-scale-to-zero: Deployment scaled to zero replicas
--- PASS: TestKubernetesFailures (0.00s)
=== RUN   TestCatalogLoading
    faulttest_test.go:332: Catalog: version=1, failures=28
    faulttest_test.go:340:   database: 16
    faulttest_test.go:340:   kubernetes: 7
    faulttest_test.go:340:   host: 3
    faulttest_test.go:340:   compound: 2
--- PASS: TestCatalogLoading (0.00s)
=== RUN   TestEvaluatorSmokeTest
    faulttest_test.go:380: Evaluator test: score=1.00, passed=true
--- PASS: TestEvaluatorSmokeTest (0.00s)
=== RUN   TestExternalModeInjection
    faulttest_test.go:390: FAULTTEST_EXTERNAL=true not set
--- SKIP: TestExternalModeInjection (0.00s)
PASS
ok  	helpdesk/testing/faulttest	672.953s
Stopping test infrastructure...
docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		down -v
[+] Running 6/6
 ✔ Container helpdesk-test-pgloader  Removed                                                                                                                                                                                                10.1s
 ✔ Container helpdesk-test-replica   Removed                                                                                                                                                                                                 0.3s
 ✔ Container helpdesk-test-pg        Removed                                                                                                                                                                                                 0.2s
 ✔ Volume docker_pgdata-replica      Removed                                                                                                                                                                                                 0.2s
 ✔ Volume docker_pgdata-primary      Removed                                                                                                                                                                                                 0.1s
 ✔ Network docker_default            Removed                                                                                                                                                                                                 0.3s

real	11m37.236s
user	0m6.483s
sys	0m2.543s
```

