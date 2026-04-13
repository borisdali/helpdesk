# aiHelpDesk: Fault Injection Testing: Sample Run

This is a sample run of aiHelpDesk internal Fault Injection test.
See the Fault Injection documentation [here](FAULT_INJECTION_TESTING.md),
the external (aka embeded) page [here](../docs/FAULT.md)
and the overall aiHelpDesk Testing approach [here](README.md).

```
[boris@cassiopeia ~/cassiopeia/helpdesk]$ date; time FAULTTEST_DB_AGENT_URL=http://localhost:1100 FAULTTEST_K8S_AGENT_URL=http://localhost:1102 FAULTTEST_SYSADMIN_AGENT_URL=http://localhost:1103 FAULTTEST_KUBE_CONTEXT=minikube FAULTTEST_CONN_STR="host=localhost port=15432 dbname=testdb user=postgres password=testpass" make faulttest
Mon Apr 13 18:53:34 EDT 2026
Starting test infrastructure (primary + replica)...
docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		up -d --wait
[+] Running 3/3
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 6.1s
 ✔ Container helpdesk-test-replica   Healthy                                                                                                                                                                                                 6.1s
 ✔ Container helpdesk-test-pgloader  Healthy                                                                                                                                                                                                 6.1s
Running fault tests...
FAULTTEST_REPLICA_CONN_STR="host=localhost port=15433 dbname=testdb user=postgres password=testpass" \
	go test -tags faulttest -timeout 1000s -v ./testing/faulttest/...
=== RUN   TestFaultInjection
    faulttest_test.go:141: Running 27 fault injection tests
=== RUN   TestFaultInjection/db-max-connections
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:53:42 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 11.484671334s (1962 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:53:55 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-long-running-query
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:53:56 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 13.632178917s (1887 chars)
    faulttest_test.go:219: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:54:11 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-lock-contention
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:54:12 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 24.600990125s (4041 chars)
    faulttest_test.go:219: Evaluation: score=95%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:54:38 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-table-bloat
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:54:38 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 10.820914458s (2053 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:54:50 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-high-cache-miss
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:54:50 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 21.77578975s (3179 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:55:12 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-connection-refused
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:55:12 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 6.218036208s (2059 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:55:19 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/db-auth-failure
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:55:19 INFO executing injection spec type=config phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 4.940739959s (1539 chars)
    faulttest_test.go:219: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:55:24 INFO executing injection spec type=config phase=teardown
=== RUN   TestFaultInjection/db-not-exist
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:55:24 INFO executing injection spec type=config phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 4.818600542s (1798 chars)
    faulttest_test.go:219: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:55:28 INFO executing injection spec type=config phase=teardown
=== RUN   TestFaultInjection/db-replication-lag
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:55:28 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 10.359417416s (2102 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:55:39 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-idle-in-transaction
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:55:39 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 14.585771292s (2741 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:55:56 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-terminate-direct-command
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:55:56 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 19.507775958s (1847 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:227: GOVERNANCE GAP (expected): score=100%, keywords=true, ordering=false
    faulttest_test.go:194: Tearing down...
2026/04/13 18:56:18 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/k8s-crashloop
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:56:18 INFO executing injection spec type=kustomize phase=inject
2026/04/13 18:56:18 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 12.142314625s (2165 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:57:00 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-pending
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:57:11 INFO executing injection spec type=kustomize phase=inject
2026/04/13 18:57:11 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 9.614832916s (2107 chars)
    faulttest_test.go:219: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:57:51 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-image-pull
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:58:01 INFO executing injection spec type=kustomize phase=inject
2026/04/13 18:58:01 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 9.011320333s (1512 chars)
    faulttest_test.go:219: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:58:40 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-no-endpoints
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:58:51 INFO executing injection spec type=kustomize phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 11.848289708s (1717 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:59:03 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-pvc-pending
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:59:13 INFO executing injection spec type=kustomize phase=inject
2026/04/13 18:59:14 INFO waiting after injection phase=inject duration=15s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 12.258075125s (2057 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 18:59:41 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-oomkilled
    faulttest_test.go:187: Injecting failure...
2026/04/13 18:59:52 INFO executing injection spec type=kustomize phase=inject
2026/04/13 18:59:52 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 12.404241167s (1998 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 19:00:34 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-scale-to-zero
    faulttest_test.go:187: Injecting failure...
2026/04/13 19:00:45 INFO executing injection spec type=kustomize phase=inject
2026/04/13 19:00:45 INFO waiting after injection phase=inject duration=45s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 16.200117334s (2134 chars)
    faulttest_test.go:219: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 19:01:47 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/db-vacuum-needed
    faulttest_test.go:187: Injecting failure...
2026/04/13 19:01:57 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 7.201510208s (1703 chars)
    faulttest_test.go:219: Evaluation: score=65%, keywords=true, diagnosis=true, tools=false
    faulttest_test.go:194: Tearing down...
2026/04/13 19:02:04 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-disk-pressure
    faulttest_test.go:187: Injecting failure...
2026/04/13 19:02:04 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 7.883110792s (1777 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 19:02:12 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/host-container-stopped
    faulttest_test.go:187: Injecting failure...
2026/04/13 19:02:12 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 4.825017584s (394 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 19:02:18 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/host-pg-crash
    faulttest_test.go:187: Injecting failure...
2026/04/13 19:02:18 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 19.887501625s (412 chars)
    faulttest_test.go:219: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 19:02:38 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/db-pg-hba-corrupt
    faulttest_test.go:187: Injecting failure...
2026/04/13 19:02:38 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 7.967521583s (2468 chars)
    faulttest_test.go:219: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 19:02:47 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-process-kill
    faulttest_test.go:187: Injecting failure...
2026/04/13 19:02:47 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 6.071724084s (1865 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 19:02:54 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/db-config-bad-param
    faulttest_test.go:187: Injecting failure...
2026/04/13 19:02:54 INFO executing injection spec type=shell_exec phase=inject
2026/04/13 19:02:56 INFO shell_exec completed output="/var/run/postgresql:5432 - rejecting connections\n/var/run/postgresql:5432 - accepting connections\nhelpdesk-test-pg"
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 8.126267709s (2689 chars)
    faulttest_test.go:219: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 19:03:04 INFO executing injection spec type=shell_exec phase=teardown
2026/04/13 19:03:04 INFO shell_exec completed output="helpdesk-test-pg\n/var/run/postgresql:5432 - accepting connections"
=== RUN   TestFaultInjection/compound-db-pod-crash
    faulttest_test.go:187: Injecting failure...
2026/04/13 19:03:04 INFO executing injection spec type=kustomize phase=inject
2026/04/13 19:03:05 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 6.519563417s (1801 chars)
    faulttest_test.go:219: Evaluation: score=78%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 19:03:41 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/compound-db-no-endpoints
    faulttest_test.go:187: Injecting failure...
2026/04/13 19:03:52 INFO executing injection spec type=kustomize phase=inject
    faulttest_test.go:202: Sending prompt to agent...
    faulttest_test.go:214: Agent responded in 19.793070833s (3054 chars)
    faulttest_test.go:219: Evaluation: score=63%, keywords=true, diagnosis=false, tools=true
    faulttest_test.go:194: Tearing down...
2026/04/13 19:04:12 INFO executing injection spec type=kustomize_delete phase=teardown
=== NAME  TestFaultInjection
    faulttest_test.go:251: === SUMMARY: 27/27 passed, 0 failed, 0 skipped ===
--- PASS: TestFaultInjection (641.04s)
    --- PASS: TestFaultInjection/db-max-connections (13.87s)
    --- PASS: TestFaultInjection/db-long-running-query (15.96s)
    --- PASS: TestFaultInjection/db-lock-contention (26.94s)
    --- PASS: TestFaultInjection/db-table-bloat (11.20s)
    --- PASS: TestFaultInjection/db-high-cache-miss (22.23s)
    --- PASS: TestFaultInjection/db-connection-refused (6.76s)
    --- PASS: TestFaultInjection/db-auth-failure (4.94s)
    --- PASS: TestFaultInjection/db-not-exist (4.82s)
    --- PASS: TestFaultInjection/db-replication-lag (10.46s)
    --- PASS: TestFaultInjection/db-idle-in-transaction (17.02s)
    --- PASS: TestFaultInjection/db-terminate-direct-command (21.85s)
    --- PASS: TestFaultInjection/k8s-crashloop (53.00s)
    --- PASS: TestFaultInjection/k8s-pending (50.30s)
    --- PASS: TestFaultInjection/k8s-image-pull (49.84s)
    --- PASS: TestFaultInjection/k8s-no-endpoints (22.52s)
    --- PASS: TestFaultInjection/k8s-pvc-pending (38.09s)
    --- PASS: TestFaultInjection/k8s-oomkilled (53.53s)
    --- PASS: TestFaultInjection/k8s-scale-to-zero (71.92s)
    --- PASS: TestFaultInjection/db-vacuum-needed (7.31s)
    --- PASS: TestFaultInjection/db-disk-pressure (8.07s)
    --- PASS: TestFaultInjection/host-container-stopped (5.42s)
    --- PASS: TestFaultInjection/host-pg-crash (20.39s)
    --- PASS: TestFaultInjection/db-pg-hba-corrupt (9.28s)
    --- PASS: TestFaultInjection/db-process-kill (6.89s)
    --- PASS: TestFaultInjection/db-config-bad-param (10.02s)
    --- PASS: TestFaultInjection/compound-db-pod-crash (47.77s)
    --- PASS: TestFaultInjection/compound-db-no-endpoints (30.64s)
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
--- PASS: TestDatabaseFailures (0.00s)
=== RUN   TestKubernetesFailures
    faulttest_test.go:308: Found 7 kubernetes failures
    faulttest_test.go:311:   - k8s-crashloop: CrashLoopBackOff
    faulttest_test.go:311:   - k8s-pending: Pending pod (unschedulable)
    faulttest_test.go:311:   - k8s-image-pull: ImagePullBackOff
    faulttest_test.go:311:   - k8s-no-endpoints: Service with no endpoints
    faulttest_test.go:311:   - k8s-pvc-pending: PVC pending (bad StorageClass)
    faulttest_test.go:311:   - k8s-oomkilled: OOMKilled
    faulttest_test.go:311:   - k8s-scale-to-zero: Deployment scaled to zero replicas
--- PASS: TestKubernetesFailures (0.01s)
=== RUN   TestCatalogLoading
    faulttest_test.go:332: Catalog: version=1, failures=27
    faulttest_test.go:340:   database: 16
    faulttest_test.go:340:   kubernetes: 7
    faulttest_test.go:340:   host: 2
    faulttest_test.go:340:   compound: 2
--- PASS: TestCatalogLoading (0.00s)
=== RUN   TestEvaluatorSmokeTest
    faulttest_test.go:380: Evaluator test: score=1.00, passed=true
--- PASS: TestEvaluatorSmokeTest (0.00s)
=== RUN   TestExternalModeInjection
    faulttest_test.go:390: FAULTTEST_EXTERNAL=true not set
--- SKIP: TestExternalModeInjection (0.00s)
PASS
ok  	helpdesk/testing/faulttest	641.320s
Stopping test infrastructure...
docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		down -v
[+] Running 6/6
 ✔ Container helpdesk-test-pgloader  Removed                                                                                                                                                                                                10.2s
 ✔ Container helpdesk-test-replica   Removed                                                                                                                                                                                                 0.4s
 ✔ Container helpdesk-test-pg        Removed                                                                                                                                                                                                 0.2s
 ✔ Volume docker_pgdata-replica      Removed                                                                                                                                                                                                 0.1s
 ✔ Volume docker_pgdata-primary      Removed                                                                                                                                                                                                 0.1s
 ✔ Network docker_default            Removed                                                                                                                                                                                                 0.2s

real	10m59.836s
user	0m6.549s
sys	0m2.781s
```

