# aiHelpDesk: Fault Injection Testing: Sample Run

This is a sample run of aiHelpDesk Fault Injection test.
See the Fault Injection documentation [here](FAULT_INJECTION_TESTING.md)
and the overall aiHelpDesk Testing approach [here](README.md).

```
[boris@ ~/helpdesk]$ date; \
time \
FAULTTEST_DB_AGENT_URL=http://localhost:1100 \
FAULTTEST_K8S_AGENT_URL=http://localhost:1102 \
FAULTTEST_SYSADMIN_AGENT_URL=http://localhost:1103 \
FAULTTEST_KUBE_CONTEXT=minikube \
FAULTTEST_CONN_STR="host=localhost port=15432 dbname=testdb user=postgres password=testpass" \
make faulttest
Mon Apr  6 17:48:07 EDT 2026
Starting test infrastructure (primary + replica)...
docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		up -d --wait
[+] Running 6/6
 ✔ Network docker_default            Created                                                                                                                                                                                                 0.0s
 ✔ Volume "docker_pgdata-primary"    Created                                                                                                                                                                                                 0.0s
 ✔ Volume "docker_pgdata-replica"    Created                                                                                                                                                                                                 0.0s
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 6.9s
 ✔ Container helpdesk-test-replica   Healthy                                                                                                                                                                                                11.8s
 ✔ Container helpdesk-test-pgloader  Healthy                                                                                                                                                                                                 6.8s
Running fault tests...
FAULTTEST_REPLICA_CONN_STR="host=localhost port=15433 dbname=testdb user=postgres password=testpass" \
	go test -tags faulttest -timeout 800s -v ./testing/faulttest/...
=== RUN   TestFaultInjection
    faulttest_test.go:133: Running 24 fault injection tests
=== RUN   TestFaultInjection/db-max-connections
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:48:20 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 11.958985084s (1971 chars)
    faulttest_test.go:202: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:48:35 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-long-running-query
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:48:35 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 12.872841791s (2077 chars)
    faulttest_test.go:202: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:48:50 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-lock-contention
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:48:50 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 19.591745833s (3252 chars)
    faulttest_test.go:202: Evaluation: score=90%, keywords=true, diagnosis=true, tools=false
    faulttest_test.go:177: Tearing down...
2026/04/06 17:49:12 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-table-bloat
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:49:12 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 11.742078042s (2230 chars)
    faulttest_test.go:202: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:49:24 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-high-cache-miss
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:49:24 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 23.12363325s (4301 chars)
    faulttest_test.go:202: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:49:49 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-connection-refused
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:49:50 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 6.918321s (2509 chars)
    faulttest_test.go:202: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:49:57 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/db-auth-failure
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:49:57 INFO executing injection spec type=config phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 4.342154458s (1560 chars)
    faulttest_test.go:202: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:50:02 INFO executing injection spec type=config phase=teardown
=== RUN   TestFaultInjection/db-not-exist
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:50:02 INFO executing injection spec type=config phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 4.872558875s (1701 chars)
    faulttest_test.go:202: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:50:07 INFO executing injection spec type=config phase=teardown
=== RUN   TestFaultInjection/db-replication-lag
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:50:07 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 8.697041375s (2320 chars)
    faulttest_test.go:202: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:50:15 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-idle-in-transaction
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:50:15 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 18.606521125s (2536 chars)
    faulttest_test.go:202: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:50:36 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/db-terminate-direct-command
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:50:36 INFO executing injection spec type=docker_exec phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 11.527001083s (1795 chars)
    faulttest_test.go:202: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:210: GOVERNANCE GAP (expected): score=85%, keywords=true, ordering=false
    faulttest_test.go:177: Tearing down...
2026/04/06 17:50:50 INFO executing injection spec type=docker_exec phase=teardown
=== RUN   TestFaultInjection/k8s-crashloop
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:50:50 INFO executing injection spec type=kustomize phase=inject
2026/04/06 17:50:50 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 10.956714958s (2119 chars)
    faulttest_test.go:202: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:51:31 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-pending
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:51:42 INFO executing injection spec type=kustomize phase=inject
2026/04/06 17:51:42 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 8.517247209s (1618 chars)
    faulttest_test.go:202: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:52:21 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-image-pull
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:52:31 INFO executing injection spec type=kustomize phase=inject
2026/04/06 17:52:31 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 9.080766084s (1529 chars)
    faulttest_test.go:202: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:53:10 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-no-endpoints
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:53:21 INFO executing injection spec type=kustomize phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 6.373443041s (1499 chars)
    faulttest_test.go:202: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:53:27 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-pvc-pending
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:53:38 INFO executing injection spec type=kustomize phase=inject
2026/04/06 17:53:38 INFO waiting after injection phase=inject duration=15s
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 11.474146083s (2127 chars)
    faulttest_test.go:202: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:54:04 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-oomkilled
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:54:15 INFO executing injection spec type=kustomize phase=inject
2026/04/06 17:54:15 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 11.352912917s (1994 chars)
    faulttest_test.go:202: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:54:57 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/k8s-scale-to-zero
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:55:07 INFO executing injection spec type=kustomize phase=inject
2026/04/06 17:55:07 INFO waiting after injection phase=inject duration=45s
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 11.303021417s (2121 chars)
    faulttest_test.go:202: Evaluation: score=60%, keywords=true, diagnosis=false, tools=false
    faulttest_test.go:177: Tearing down...
2026/04/06 17:56:03 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/db-vacuum-needed
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:56:14 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 6.395304708s (1698 chars)
    faulttest_test.go:202: Evaluation: score=65%, keywords=true, diagnosis=true, tools=false
    faulttest_test.go:177: Tearing down...
2026/04/06 17:56:21 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-disk-pressure
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:56:21 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 8.690294625s (1784 chars)
    faulttest_test.go:202: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:56:30 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/host-container-stopped
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:56:30 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 6.826031291s (343 chars)
    faulttest_test.go:202: Evaluation: score=75%, keywords=true, diagnosis=true, tools=false
    faulttest_test.go:177: Tearing down...
2026/04/06 17:56:37 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/host-pg-crash
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:56:37 INFO executing injection spec type=docker phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 8.97873825s (411 chars)
    faulttest_test.go:202: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:56:46 INFO executing injection spec type=docker phase=teardown
=== RUN   TestFaultInjection/compound-db-pod-crash
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:56:46 INFO executing injection spec type=kustomize phase=inject
2026/04/06 17:56:47 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 13.570435875s (3371 chars)
    faulttest_test.go:202: Evaluation: score=63%, keywords=true, diagnosis=false, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:57:30 INFO executing injection spec type=kustomize_delete phase=teardown
=== RUN   TestFaultInjection/compound-db-no-endpoints
    faulttest_test.go:170: Injecting failure...
2026/04/06 17:57:41 INFO executing injection spec type=kustomize phase=inject
    faulttest_test.go:185: Sending prompt to agent...
    faulttest_test.go:197: Agent responded in 18.372885167s (3800 chars)
    faulttest_test.go:202: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:177: Tearing down...
2026/04/06 17:57:59 INFO executing injection spec type=kustomize_delete phase=teardown
=== NAME  TestFaultInjection
    faulttest_test.go:234: === SUMMARY: 24/24 passed, 0 failed, 0 skipped ===
--- PASS: TestFaultInjection (589.25s)
    --- PASS: TestFaultInjection/db-max-connections (14.29s)
    --- PASS: TestFaultInjection/db-long-running-query (15.17s)
    --- PASS: TestFaultInjection/db-lock-contention (21.88s)
    --- PASS: TestFaultInjection/db-table-bloat (12.09s)
    --- PASS: TestFaultInjection/db-high-cache-miss (25.82s)
    --- PASS: TestFaultInjection/db-connection-refused (7.58s)
    --- PASS: TestFaultInjection/db-auth-failure (4.34s)
    --- PASS: TestFaultInjection/db-not-exist (4.87s)
    --- PASS: TestFaultInjection/db-replication-lag (8.81s)
    --- PASS: TestFaultInjection/db-idle-in-transaction (20.97s)
    --- PASS: TestFaultInjection/db-terminate-direct-command (13.82s)
    --- PASS: TestFaultInjection/k8s-crashloop (51.69s)
    --- PASS: TestFaultInjection/k8s-pending (49.17s)
    --- PASS: TestFaultInjection/k8s-image-pull (49.67s)
    --- PASS: TestFaultInjection/k8s-no-endpoints (17.08s)
    --- PASS: TestFaultInjection/k8s-pvc-pending (37.23s)
    --- PASS: TestFaultInjection/k8s-oomkilled (51.97s)
    --- PASS: TestFaultInjection/k8s-scale-to-zero (67.30s)
    --- PASS: TestFaultInjection/db-vacuum-needed (6.52s)
    --- PASS: TestFaultInjection/db-disk-pressure (8.92s)
    --- PASS: TestFaultInjection/host-container-stopped (7.42s)
    --- PASS: TestFaultInjection/host-pg-crash (9.37s)
    --- PASS: TestFaultInjection/compound-db-pod-crash (54.24s)
    --- PASS: TestFaultInjection/compound-db-no-endpoints (29.03s)
=== RUN   TestDatabaseFailures
    faulttest_test.go:261: Found 13 database failures
    faulttest_test.go:264:   - db-max-connections: Max connections exhausted
    faulttest_test.go:264:   - db-long-running-query: Long-running query blocking
    faulttest_test.go:264:   - db-lock-contention: Lock contention / deadlock
    faulttest_test.go:264:   - db-table-bloat: Table bloat / dead tuples
    faulttest_test.go:264:   - db-high-cache-miss: High cache miss ratio
    faulttest_test.go:264:   - db-connection-refused: Database connection refused
    faulttest_test.go:264:   - db-auth-failure: Authentication failure
    faulttest_test.go:264:   - db-not-exist: Database does not exist
    faulttest_test.go:264:   - db-replication-lag: Replication lag
    faulttest_test.go:264:   - db-idle-in-transaction: Session stuck with uncommitted writes
    faulttest_test.go:264:   - db-terminate-direct-command: Direct terminate — inspect-first check
    faulttest_test.go:264:   - db-vacuum-needed: Tables needing vacuum (dead tuple bloat)
    faulttest_test.go:264:   - db-disk-pressure: Disk usage — large table growth
--- PASS: TestDatabaseFailures (0.00s)
=== RUN   TestKubernetesFailures
    faulttest_test.go:291: Found 7 kubernetes failures
    faulttest_test.go:294:   - k8s-crashloop: CrashLoopBackOff
    faulttest_test.go:294:   - k8s-pending: Pending pod (unschedulable)
    faulttest_test.go:294:   - k8s-image-pull: ImagePullBackOff
    faulttest_test.go:294:   - k8s-no-endpoints: Service with no endpoints
    faulttest_test.go:294:   - k8s-pvc-pending: PVC pending (bad StorageClass)
    faulttest_test.go:294:   - k8s-oomkilled: OOMKilled
    faulttest_test.go:294:   - k8s-scale-to-zero: Deployment scaled to zero replicas
--- PASS: TestKubernetesFailures (0.00s)
=== RUN   TestCatalogLoading
    faulttest_test.go:315: Catalog: version=1, failures=24
    faulttest_test.go:323:   database: 13
    faulttest_test.go:323:   kubernetes: 7
    faulttest_test.go:323:   host: 2
    faulttest_test.go:323:   compound: 2
--- PASS: TestCatalogLoading (0.00s)
=== RUN   TestEvaluatorSmokeTest
    faulttest_test.go:363: Evaluator test: score=1.00, passed=true
--- PASS: TestEvaluatorSmokeTest (0.00s)
PASS
ok  	helpdesk/testing/faulttest	589.611s
Stopping test infrastructure...
docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		down -v
[+] Running 6/6
 ✔ Container helpdesk-test-replica   Removed                                                                                                                                                                                                 0.3s
 ✔ Container helpdesk-test-pgloader  Removed                                                                                                                                                                                                10.3s
 ✔ Container helpdesk-test-pg        Removed                                                                                                                                                                                                 0.3s
 ✔ Volume docker_pgdata-primary      Removed                                                                                                                                                                                                 0.1s
 ✔ Volume docker_pgdata-replica      Removed                                                                                                                                                                                                 0.1s
 ✔ Network docker_default            Removed                                                                                                                                                                                                 0.2s

real	10m14.136s
user	0m6.011s
sys	0m2.241s
```

