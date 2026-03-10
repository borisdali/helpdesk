# aiHelpDesk: Fault Injection Testing: Sample Run

This is a sample run of aiHelpDesk Fault Injection test.
See the Fault Injection documentation [here](FAULT_INJECTION_TESTING.md)
and the overall aiHelpDesk Testing approach [here](https://github.com/borisdali/helpdesk/blob/v0.5.0/testing/README.md).

```
[boris@ ~/helpdesk]$ date; \
time FAULTTEST_DB_AGENT_URL=http://localhost:1100 \
FAULTTEST_K8S_AGENT_URL=http://localhost:1102 \
FAULTTEST_KUBE_CONTEXT=minikube \
FAULTTEST_CONN_STR="host=localhost port=15432 dbname=testdb user=postgres password=testpass"  \
make faulttest

Sat Mar  7 16:59:28 EST 2026
Starting test infrastructure (primary + replica)...
docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		up -d --wait
[+] Running 6/6
 ✔ Network docker_default            Created                                                                                                                                                                                                 0.0s
 ✔ Volume "docker_pgdata-primary"    Created                                                                                                                                                                                                 0.0s
 ✔ Volume "docker_pgdata-replica"    Created                                                                                                                                                                                                 0.0s
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 6.6s
 ✔ Container helpdesk-test-replica   Healthy                                                                                                                                                                                                11.5s
 ✔ Container helpdesk-test-pgloader  Healthy                                                                                                                                                                                                 6.5s
Running fault tests...
FAULTTEST_REPLICA_CONN_STR="host=localhost port=15433 dbname=testdb user=postgres password=testpass" \
	go test -tags faulttest -timeout 600s -v ./testing/faulttest/...
=== RUN   TestFaultInjection
    faulttest_test.go:129: Running 19 fault injection tests
=== RUN   TestFaultInjection/db-max-connections
    faulttest_test.go:166: Injecting failure...
2026/03/07 16:59:41 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 8.943585958s (2114 chars)
    faulttest_test.go:198: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 16:59:52 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-long-running-query
    faulttest_test.go:166: Injecting failure...
2026/03/07 16:59:52 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 8.464912541s (1760 chars)
    faulttest_test.go:198: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:00:03 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-lock-contention
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:00:03 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 11.743298083s (3073 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:00:17 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-table-bloat
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:00:17 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 9.180301709s (2160 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:00:27 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-high-cache-miss
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:00:27 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 12.215243209s (2710 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:00:39 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-connection-refused
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:00:39 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 5.872149542s (2309 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:00:46 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/db-auth-failure
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:00:46 INFO executing injection spec type=config phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 4.741769666s (1954 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:00:51 INFO executing injection spec type=config phase=teardown
=== RUN   TestFaultInjection/db-not-exist
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:00:51 INFO executing injection spec type=config phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 3.484921458s (1341 chars)
    faulttest_test.go:198: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:00:54 INFO executing injection spec type=config phase=teardown
=== RUN   TestFaultInjection/db-replication-lag
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:00:54 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 7.212118917s (1877 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:01:01 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-idle-in-transaction
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:01:02 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 10.156044125s (2693 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:01:14 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-terminate-direct-command
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:01:14 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 8.065746625s (1854 chars)
    faulttest_test.go:198: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:206: GOVERNANCE GAP (expected): score=85%, keywords=true, ordering=false
    faulttest_test.go:173: Tearing down...
2026/03/07 17:01:24 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/k8s-crashloop
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:01:24 INFO executing injection spec type=kustomize phase=inject
2026/03/07 17:01:25 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 9.329930625s (2077 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:02:04 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-pending
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:02:15 INFO executing injection spec type=kustomize phase=inject
2026/03/07 17:02:15 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 8.764410375s (2091 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:02:54 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-image-pull
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:03:04 INFO executing injection spec type=kustomize phase=inject
2026/03/07 17:03:04 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 6.275755541s (1555 chars)
    faulttest_test.go:198: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:03:41 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-no-endpoints
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:03:51 INFO executing injection spec type=kustomize phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 10.145779208s (2782 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:04:02 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-pvc-pending
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:04:12 INFO executing injection spec type=kustomize phase=inject
2026/03/07 17:04:12 INFO waiting after injection phase=inject duration=15s
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 9.791992875s (2134 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:04:37 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-oomkilled
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:04:48 INFO executing injection spec type=kustomize phase=inject
2026/03/07 17:04:48 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 7.005366542s (1709 chars)
    faulttest_test.go:198: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:05:25 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/compound-db-pod-crash
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:05:35 INFO executing injection spec type=kustomize phase=inject
2026/03/07 17:05:36 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 9.76130125s (2490 chars)
    faulttest_test.go:198: Evaluation: score=63%, keywords=true, diagnosis=false, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:06:15 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/compound-db-no-endpoints
    faulttest_test.go:166: Injecting failure...
2026/03/07 17:06:26 INFO executing injection spec type=kustomize phase=inject
    faulttest_test.go:181: Sending prompt to agent...
    faulttest_test.go:193: Agent responded in 10.255327709s (2509 chars)
    faulttest_test.go:198: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:173: Tearing down...
2026/03/07 17:06:37 INFO executing injection spec type=kustomize_delete phase=teardown
=== NAME  TestFaultInjection
    faulttest_test.go:230: === SUMMARY: 19/19 passed, 0 failed, 0 skipped ===
--- PASS: TestFaultInjection (425.98s)
    --- PASS: TestFaultInjection/db-max-connections (11.32s)
    --- PASS: TestFaultInjection/db-long-running-query (10.82s)
    --- PASS: TestFaultInjection/db-lock-contention (14.09s)
    --- PASS: TestFaultInjection/db-table-bloat (9.52s)
    --- PASS: TestFaultInjection/db-high-cache-miss (12.64s)
    --- PASS: TestFaultInjection/db-connection-refused (6.48s)
    --- PASS: TestFaultInjection/db-auth-failure (4.74s)
    --- PASS: TestFaultInjection/db-not-exist (3.49s)
    --- PASS: TestFaultInjection/db-replication-lag (7.31s)
    --- PASS: TestFaultInjection/db-idle-in-transaction (12.44s)
    --- PASS: TestFaultInjection/db-terminate-direct-command (10.38s)
    --- PASS: TestFaultInjection/k8s-crashloop (50.21s)
    --- PASS: TestFaultInjection/k8s-pending (49.53s)
    --- PASS: TestFaultInjection/k8s-image-pull (47.21s)
    --- PASS: TestFaultInjection/k8s-no-endpoints (20.87s)
    --- PASS: TestFaultInjection/k8s-pvc-pending (35.54s)
    --- PASS: TestFaultInjection/k8s-oomkilled (47.66s)
    --- PASS: TestFaultInjection/compound-db-pod-crash (50.62s)
    --- PASS: TestFaultInjection/compound-db-no-endpoints (21.10s)
=== RUN   TestDatabaseFailures
    faulttest_test.go:257: Found 11 database failures
    faulttest_test.go:260:   - db-max-connections: Max connections exhausted
    faulttest_test.go:260:   - db-long-running-query: Long-running query blocking
    faulttest_test.go:260:   - db-lock-contention: Lock contention / deadlock
    faulttest_test.go:260:   - db-table-bloat: Table bloat / dead tuples
    faulttest_test.go:260:   - db-high-cache-miss: High cache miss ratio
    faulttest_test.go:260:   - db-connection-refused: Database connection refused
    faulttest_test.go:260:   - db-auth-failure: Authentication failure
    faulttest_test.go:260:   - db-not-exist: Database does not exist
    faulttest_test.go:260:   - db-replication-lag: Replication lag
    faulttest_test.go:260:   - db-idle-in-transaction: Session stuck with uncommitted writes
    faulttest_test.go:260:   - db-terminate-direct-command: Direct terminate — inspect-first check
--- PASS: TestDatabaseFailures (0.00s)
=== RUN   TestKubernetesFailures
    faulttest_test.go:287: Found 6 kubernetes failures
    faulttest_test.go:290:   - k8s-crashloop: CrashLoopBackOff
    faulttest_test.go:290:   - k8s-pending: Pending pod (unschedulable)
    faulttest_test.go:290:   - k8s-image-pull: ImagePullBackOff
    faulttest_test.go:290:   - k8s-no-endpoints: Service with no endpoints
    faulttest_test.go:290:   - k8s-pvc-pending: PVC pending (bad StorageClass)
    faulttest_test.go:290:   - k8s-oomkilled: OOMKilled
--- PASS: TestKubernetesFailures (0.00s)
=== RUN   TestCatalogLoading
    faulttest_test.go:311: Catalog: version=1, failures=19
    faulttest_test.go:319:   database: 11
    faulttest_test.go:319:   kubernetes: 6
    faulttest_test.go:319:   compound: 2
--- PASS: TestCatalogLoading (0.00s)
=== RUN   TestEvaluatorSmokeTest
    faulttest_test.go:359: Evaluator test: score=1.00, passed=true
--- PASS: TestEvaluatorSmokeTest (0.00s)
PASS
ok  	helpdesk/testing/faulttest	426.407s
Stopping test infrastructure...
docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		down -v
[+] Running 6/6
 ✔ Container helpdesk-test-replica   Removed                                                                                                                                                                                                 0.2s
 ✔ Container helpdesk-test-pgloader  Removed                                                                                                                                                                                                10.2s
 ✔ Container helpdesk-test-pg        Removed                                                                                                                                                                                                 0.2s
 ✔ Volume docker_pgdata-primary      Removed                                                                                                                                                                                                 0.1s
 ✔ Volume docker_pgdata-replica      Removed                                                                                                                                                                                                 0.0s
 ✔ Network docker_default            Removed                                                                                                                                                                                                 0.2s

real	7m30.140s
user	0m5.388s
sys	0m2.066s
```

