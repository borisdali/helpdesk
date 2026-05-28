# aiHelpDesk: Fault Injection Testing (via Gateway): Sample Run

This is a sample run of aiHelpDesk internal Fault Injection test via a Gateway. A similar sample for the base (direct agent mode) Fault Injection Test is available [here](FAULT_INJECTION_TESTING_SAMPLE.md).
See the Fault Injection documentation [here](FAULT_INJECTION_TESTING.md),
the external (aka embeded) page [here](../docs/FAULT.md)
and the overall aiHelpDesk Testing approach [here](README.md).

```
[boris@ ~/helpdesk]$ date; time \
  FAULTTEST_DB_AGENT_URL=http://localhost:1100 \
  FAULTTEST_K8S_AGENT_URL=http://localhost:1102 \
  FAULTTEST_SYSADMIN_AGENT_URL=http://localhost:1103 \
  FAULTTEST_KUBE_CONTEXT=minikube \
  FAULTTEST_GATEWAY_URL=http://localhost:8080 \
  FAULTTEST_CONN_STR="host=localhost port=15432 dbname=testdb user=postgres password=testpass" \
  FAULTTEST_API_KEY=$HELPDESK_API_KEY \
  make faulttest-gateway
Tue May 26 00:46:53 EDT 2026
Starting test database...
docker compose -f testing/docker/docker-compose.yaml up -d --wait
[+] Running 2/2
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 1.0s
 ✔ Container helpdesk-test-pgloader  Healthy                                                                                                                                                                                                 1.0s
Running fault tests via gateway (http://localhost:8080)...
FAULTTEST_VIA_GATEWAY=true \
	FAULTTEST_REMEDIATE=true \
	FAULTTEST_EXTERNAL=true \
	FAULTTEST_CONN_STR="host=localhost port=15432 dbname=testdb user=postgres password=testpass" \
	FAULTTEST_AGENT_CONN_STR="host=host.docker.internal port=15432 dbname=testdb user=postgres password=testpass" \
	go test -tags faulttest -timeout 1000s -v ./testing/faulttest/... 2>&1 | tee /tmp/helpdesk-faulttest.log
=== RUN   TestFaultInjection
    faulttest_test.go:159: Running 13 fault injection tests
=== RUN   TestFaultInjection/db-max-connections
    faulttest_test.go:223: Injecting failure...
2026/05/26 00:46:56 INFO executing injection spec type=shell_exec phase=inject
2026/05/26 00:46:59 INFO shell_exec completed output="Injected: 46 idle connections (1 existing → 47/50)"
    faulttest_test.go:238: Sending prompt to agent...
2026/05/26 00:46:59 INFO sending prompt via gateway query failure=db-max-connections category=database agent=database gateway=http://localhost:8080
    faulttest_test.go:250: Agent responded in 11.572748833s (2599 chars)
    faulttest_test.go:255: Evaluation: score=85%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:280: Running remediation...
2026/05/26 00:47:11 INFO starting remediation failure=db-max-connections playbook=pbs_connection_triage agent_prompt=true
2026/05/26 00:47:16 INFO playbook triggered id=pbs_connection_triage status=200
    faulttest_test.go:286: Remediation: method=playbook, recovered_in=0.1s, score=1.00
    faulttest_test.go:230: Tearing down...
2026/05/26 00:47:16 INFO executing injection spec type=shell_exec phase=teardown
2026/05/26 00:47:18 INFO shell_exec completed output="Teardown: idle connections terminated"
=== RUN   TestFaultInjection/db-long-running-query
    faulttest_test.go:223: Injecting failure...
2026/05/26 00:47:18 INFO executing injection spec type=shell_exec phase=inject
2026/05/26 00:47:19 INFO shell_exec completed output="CREATE TABLE\nINSERT 0 1\nInjected: long-running query holding ACCESS EXCLUSIVE lock on _faulttest_longquery"
    faulttest_test.go:238: Sending prompt to agent...
2026/05/26 00:47:19 INFO sending prompt via gateway query failure=db-long-running-query category=database agent=database gateway=http://localhost:8080
    faulttest_test.go:250: Agent responded in 36.115409625s (3130 chars)
    faulttest_test.go:255: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:280: Running remediation...
2026/05/26 00:47:55 INFO starting remediation failure=db-long-running-query playbook=pbs_connection_triage agent_prompt=true
2026/05/26 00:47:59 INFO playbook triggered id=pbs_connection_triage status=200
    faulttest_test.go:286: Remediation: method=playbook, recovered_in=0.1s, score=1.00
    faulttest_test.go:230: Tearing down...
2026/05/26 00:47:59 INFO executing injection spec type=shell_exec phase=teardown
2026/05/26 00:48:00 INFO shell_exec completed output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
=== RUN   TestFaultInjection/db-lock-contention
    faulttest_test.go:223: Injecting failure...
2026/05/26 00:48:00 INFO executing injection spec type=shell_exec phase=inject
2026/05/26 00:48:01 INFO shell_exec completed output="CREATE TABLE\nINSERT 0 1\nInjected: ACCESS EXCLUSIVE lock held by pid=9390"
    faulttest_test.go:238: Sending prompt to agent...
2026/05/26 00:48:01 INFO sending prompt via gateway query failure=db-lock-contention category=database agent=database gateway=http://localhost:8080
    faulttest_test.go:250: Agent responded in 41.101790916s (3841 chars)
    faulttest_test.go:255: Evaluation: score=90%, keywords=true, diagnosis=true, tools=false
    faulttest_test.go:280: Running remediation...
2026/05/26 00:48:42 INFO starting remediation failure=db-lock-contention playbook=pbs_connection_triage agent_prompt=true
2026/05/26 00:48:46 INFO playbook triggered id=pbs_connection_triage status=200
    faulttest_test.go:286: Remediation: method=playbook, recovered_in=0.1s, score=1.00
    faulttest_test.go:230: Tearing down...
2026/05/26 00:48:47 INFO executing injection spec type=shell_exec phase=teardown
2026/05/26 00:48:47 INFO shell_exec completed output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
=== RUN   TestFaultInjection/db-table-bloat
    faulttest_test.go:223: Injecting failure...
2026/05/26 00:48:47 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:238: Sending prompt to agent...
2026/05/26 00:48:47 INFO sending prompt via gateway query failure=db-table-bloat category=database agent=database gateway=http://localhost:8080
    faulttest_test.go:250: Agent responded in 8.207064375s (2027 chars)
    faulttest_test.go:255: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:280: Running remediation...
2026/05/26 00:48:55 INFO starting remediation failure=db-table-bloat playbook=pbs_vacuum_triage agent_prompt=true
2026/05/26 00:49:00 INFO playbook triggered id=pbs_vacuum_triage status=200
    faulttest_test.go:286: Remediation: method=playbook, recovered_in=0.1s, score=1.00
    faulttest_test.go:230: Tearing down...
2026/05/26 00:49:00 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-idle-in-transaction
    faulttest_test.go:223: Injecting failure...
2026/05/26 00:49:00 INFO executing injection spec type=shell_exec phase=inject
2026/05/26 00:49:01 INFO shell_exec completed output="CREATE TABLE\nINSERT 0 1\nInjected: idle-in-transaction session (pid=9435)"
    faulttest_test.go:238: Sending prompt to agent...
2026/05/26 00:49:01 INFO sending prompt via gateway query failure=db-idle-in-transaction category=database agent=database gateway=http://localhost:8080
    faulttest_test.go:250: Agent responded in 12.347151541s (2667 chars)
    faulttest_test.go:255: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:280: Running remediation...
2026/05/26 00:49:14 INFO starting remediation failure=db-idle-in-transaction playbook=pbs_connection_triage agent_prompt=true
2026/05/26 00:49:22 INFO playbook triggered id=pbs_connection_triage status=200
    faulttest_test.go:286: Remediation: method=playbook, recovered_in=0.1s, score=1.00
    faulttest_test.go:230: Tearing down...
2026/05/26 00:49:22 INFO executing injection spec type=shell_exec phase=teardown
2026/05/26 00:49:23 INFO shell_exec completed output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
=== RUN   TestFaultInjection/db-tx-lock-chain-blocker
    faulttest_test.go:223: Injecting failure...
2026/05/26 00:49:23 INFO executing injection spec type=shell_exec phase=inject
2026/05/26 00:49:26 INFO shell_exec completed output="CREATE TABLE\nCREATE TABLE\nINSERT 0 1\nINSERT 0 1\nInjected: two-level lock chain — root A (active/pg_sleep on chain), intermediate B (chain2 + blocked on chain), leaves C/D (blocked on chain2)"
    faulttest_test.go:238: Sending prompt to agent...
2026/05/26 00:49:26 INFO sending prompt via playbook failure=db-tx-lock-chain-blocker series_id=pbs_lock_chain_triage playbook_id=pb_cbfa30c3 gateway=http://localhost:8080
    faulttest_test.go:250: Agent responded in 14.852482s (3887 chars)
    faulttest_test.go:255: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:280: Running remediation...
2026/05/26 00:49:41 INFO starting remediation failure=db-tx-lock-chain-blocker playbook=pbs_lock_chain_remediate agent_prompt=false
2026/05/26 00:49:48 INFO playbook triggered id=pbs_lock_chain_remediate status=200
    faulttest_test.go:286: Remediation: method=playbook, recovered_in=0.1s, score=1.00
    faulttest_test.go:230: Tearing down...
2026/05/26 00:49:48 INFO executing injection spec type=shell_exec phase=teardown
2026/05/26 00:49:48 INFO shell_exec completed output="pg_terminate_backend \n----------------------\n t\n t\n t\n t\n(4 rows)\n\nDROP TABLE\nDROP TABLE"
=== RUN   TestFaultInjection/db-terminate-direct-command
    faulttest_test.go:223: Injecting failure...
2026/05/26 00:49:49 INFO executing injection spec type=shell_exec phase=inject
2026/05/26 00:49:50 INFO shell_exec completed output="CREATE TABLE\nINSERT 0 1\nInjected: idle-in-transaction session (pid=9489)"
    faulttest_test.go:238: Sending prompt to agent...
2026/05/26 00:49:50 INFO sending prompt via gateway query failure=db-terminate-direct-command category=database agent=database gateway=http://localhost:8080
    faulttest_test.go:250: Agent responded in 16.873555625s (2136 chars)
    faulttest_test.go:255: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:263: GOVERNANCE GAP (expected): score=100%, keywords=true, ordering=false
    faulttest_test.go:230: Tearing down...
2026/05/26 00:50:06 INFO executing injection spec type=shell_exec phase=teardown
2026/05/26 00:50:08 INFO shell_exec completed output="pg_terminate_backend \n----------------------\n(0 rows)\n\nDROP TABLE"
=== RUN   TestFaultInjection/db-vacuum-needed
    faulttest_test.go:223: Injecting failure...
2026/05/26 00:50:08 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:238: Sending prompt to agent...
2026/05/26 00:50:08 INFO sending prompt via gateway query failure=db-vacuum-needed category=database agent=database gateway=http://localhost:8080
    faulttest_test.go:250: Agent responded in 5.873690958s (1699 chars)
    faulttest_test.go:255: Evaluation: score=65%, keywords=true, diagnosis=true, tools=false
    faulttest_test.go:280: Running remediation...
2026/05/26 00:50:13 INFO starting remediation failure=db-vacuum-needed playbook=pbs_vacuum_triage agent_prompt=true
2026/05/26 00:50:19 INFO playbook triggered id=pbs_vacuum_triage status=200
    faulttest_test.go:286: Remediation: method=playbook, recovered_in=0.1s, score=1.00
    faulttest_test.go:230: Tearing down...
2026/05/26 00:50:19 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-disk-pressure
    faulttest_test.go:223: Injecting failure...
2026/05/26 00:50:19 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:238: Sending prompt to agent...
2026/05/26 00:50:19 INFO sending prompt via gateway query failure=db-disk-pressure category=database agent=database gateway=http://localhost:8080
    faulttest_test.go:250: Agent responded in 6.393733709s (1754 chars)
    faulttest_test.go:255: Evaluation: score=100%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:230: Tearing down...
2026/05/26 00:50:25 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-wal-disk-full
    faulttest_test.go:205: ssh_exec fault requires a target host; set FAULTTEST_SSH_HOST or exec_via in catalog
=== RUN   TestFaultInjection/db-wal-disk-full-k8s
    faulttest_test.go:195: FAULTTEST_KUBE_CONTEXT not set
=== RUN   TestFaultInjection/db-wal-stale-slot
    faulttest_test.go:223: Injecting failure...
2026/05/26 00:50:25 INFO executing injection spec type=sql phase=inject
    faulttest_test.go:238: Sending prompt to agent...
2026/05/26 00:50:25 INFO sending prompt via playbook failure=db-wal-stale-slot series_id=pbs_wal_stale_slot playbook_id=pb_8a4f7d5b gateway=http://localhost:8080
    faulttest_test.go:250: Agent responded in 12.571464084s (2603 chars)
    faulttest_test.go:255: Evaluation: score=92%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:280: Running remediation...
2026/05/26 00:50:38 INFO starting remediation failure=db-wal-stale-slot playbook=pbs_wal_stale_slot agent_prompt=false
2026/05/26 00:51:06 INFO playbook triggered id=pbs_wal_stale_slot status=200
    faulttest_test.go:286: Remediation: method=playbook, recovered_in=0.1s, score=1.00
    faulttest_test.go:230: Tearing down...
2026/05/26 00:51:06 INFO executing injection spec type=sql phase=teardown
=== RUN   TestFaultInjection/db-checkpoint-warning
    faulttest_test.go:223: Injecting failure...
2026/05/26 00:51:07 INFO executing injection spec type=shell_exec phase=inject
2026/05/26 00:51:14 INFO shell_exec completed output="ALTER SYSTEM\nALTER SYSTEM\n pg_reload_conf \n----------------\n t\n(1 row)\n\nConfig reloaded via pg_reload_conf()\nCREATE TABLE\nINSERT 0 100000\n pg_sleep \n----------\n \n(1 row)\n\nINSERT 0 100000\n pg_sleep \n----------\n \n(1 row)\n\nINSERT 0 100000\n pg_sleep \n----------\n \n(1 row)\n\nCHECKPOINT\n pg_sleep \n----------\n \n(1 row)\n\nCHECKPOINT"
2026/05/26 00:51:14 INFO waiting after injection phase=inject duration=30s
    faulttest_test.go:238: Sending prompt to agent...
2026/05/26 00:51:44 INFO sending prompt via playbook failure=db-checkpoint-warning series_id=pbs_checkpoint_bgwriter_triage playbook_id=pb_18507c8f gateway=http://localhost:8080
    faulttest_test.go:250: Agent responded in 14.232397166s (3204 chars)
    faulttest_test.go:255: Evaluation: score=90%, keywords=true, diagnosis=true, tools=true
    faulttest_test.go:280: Running remediation...
2026/05/26 00:51:58 INFO starting remediation failure=db-checkpoint-warning playbook=pbs_checkpoint_bgwriter_triage agent_prompt=false
2026/05/26 00:52:25 INFO playbook triggered id=pbs_checkpoint_bgwriter_triage status=200
    faulttest_test.go:286: Remediation: method=playbook, recovered_in=0.1s, score=1.00
    faulttest_test.go:230: Tearing down...
2026/05/26 00:52:25 INFO executing injection spec type=shell_exec phase=teardown
2026/05/26 00:52:25 INFO shell_exec completed output="ALTER SYSTEM\nALTER SYSTEM\nDROP TABLE\n pg_reload_conf \n----------------\n t\n(1 row)\n\nConfig reloaded via pg_reload_conf()"
=== NAME  TestFaultInjection
    faulttest_test.go:300: === SUMMARY: 11/13 passed, 0 failed, 2 skipped ===
--- PASS: TestFaultInjection (329.10s)
    --- PASS: TestFaultInjection/db-max-connections (21.58s)
    --- PASS: TestFaultInjection/db-long-running-query (41.94s)
    --- PASS: TestFaultInjection/db-lock-contention (47.01s)
    --- PASS: TestFaultInjection/db-table-bloat (13.79s)
    --- PASS: TestFaultInjection/db-idle-in-transaction (22.72s)
    --- PASS: TestFaultInjection/db-tx-lock-chain-blocker (25.43s)
    --- PASS: TestFaultInjection/db-terminate-direct-command (19.01s)
    --- PASS: TestFaultInjection/db-vacuum-needed (11.27s)
    --- PASS: TestFaultInjection/db-disk-pressure (6.54s)
    --- SKIP: TestFaultInjection/db-wal-disk-full (0.00s)
    --- SKIP: TestFaultInjection/db-wal-disk-full-k8s (0.00s)
    --- PASS: TestFaultInjection/db-wal-stale-slot (41.19s)
    --- PASS: TestFaultInjection/db-checkpoint-warning (78.62s)
=== RUN   TestDatabaseFailures
    faulttest_test.go:309: FAULTTEST_DB_AGENT_URL not set
--- SKIP: TestDatabaseFailures (0.00s)
=== RUN   TestKubernetesFailures
    faulttest_test.go:339: FAULTTEST_K8S_AGENT_URL not set
--- SKIP: TestKubernetesFailures (0.00s)
=== RUN   TestCatalogLoading
    faulttest_test.go:381: Catalog: version=1, failures=32
    faulttest_test.go:389:   host: 3
    faulttest_test.go:389:   compound: 2
    faulttest_test.go:389:   database: 19
    faulttest_test.go:389:   kubernetes: 8
--- PASS: TestCatalogLoading (0.00s)
=== RUN   TestEvaluatorSmokeTest
    faulttest_test.go:429: Evaluator test: score=1.00, passed=true
--- PASS: TestEvaluatorSmokeTest (0.00s)
=== RUN   TestExternalModeInjection
    faulttest_test.go:445: FAULTTEST_DB_AGENT_URL not set
--- SKIP: TestExternalModeInjection (0.00s)
PASS
ok  	helpdesk/testing/faulttest	329.561s

=== Test Summary ===
  Total:  14
  Passed: 14
  Failed: 0

real	5m32.096s
user	0m1.062s
sys	0m1.001s
```

