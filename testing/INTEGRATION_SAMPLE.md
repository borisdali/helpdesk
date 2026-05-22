# aiHelpDesk: Integration Testing: Sample Run

This is a sample run of aiHelpDesk Integration test.
See overall aiHelpDesk Testing approach [here](README.md).

```
[boris@cassiopeia ~/cassiopeia/helpdesk]$ date; time make integration-nocache
Thu May 21 19:58:15 EDT 2026
Starting test infrastructure...
docker compose -f testing/docker/docker-compose.yaml up -d --wait
[+] Running 4/4
 ✔ Network docker_default            Created                                                                                                                                                                                                 0.0s
 ✔ Volume "docker_pgdata"            Created                                                                                                                                                                                                 0.0s
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 6.2s
 ✔ Container helpdesk-test-pgloader  Healthy                                                                                                                                                                                                 6.2s
Running integration tests...
go test --count=1 -tags integration -timeout 120s -v ./testing/integration/... ./agents/database/... ./agents/k8s/... ./agents/sysadmin/... 2>&1 | tee /tmp/helpdesk-integration.log
=== RUN   TestDockerExec_SimpleCommand
--- PASS: TestDockerExec_SimpleCommand (0.12s)
=== RUN   TestDockerExec_PostgresVersion
--- PASS: TestDockerExec_PostgresVersion (0.06s)
=== RUN   TestDockerExec_NonexistentContainer
--- PASS: TestDockerExec_NonexistentContainer (0.02s)
=== RUN   TestDockerCompose_Ps
--- PASS: TestDockerCompose_Ps (0.08s)
=== RUN   TestRunSQLStringViaPgloader_Success
--- PASS: TestRunSQLStringViaPgloader_Success (0.09s)
=== RUN   TestRunSQLStringViaPgloader_Query
    docker_test.go:102: pgloader→postgres: 172.19.0.2/32:5432 db=testdb
    docker_test.go:103: host→postgres:     172.19.0.2/32:5432 db=testdb
--- PASS: TestRunSQLStringViaPgloader_Query (0.42s)
=== RUN   TestDockerCompose_StopStartService
    docker_test.go:152: DockerComposeStop and DockerComposeStart helpers are available
--- PASS: TestDockerCompose_StopStartService (0.02s)
=== RUN   TestExternalInjectSpecs
=== RUN   TestExternalInjectSpecs/db-table-bloat
    external_inject_test.go:88: Testing external inject: Table bloat / dead tuples
2026/05/21 19:58:25 INFO executing injection spec type=sql phase=inject
2026/05/21 19:58:25 INFO executing injection spec type=sql phase=teardown
=== RUN   TestExternalInjectSpecs/db-idle-in-tx-blocker
    external_inject_test.go:88: Testing external inject: Idle-in-transaction root blocker with lock queue
2026/05/21 19:58:25 INFO executing injection spec type=shell_exec phase=inject
2026/05/21 19:58:27 INFO shell_exec completed output="CREATE TABLE\nINSERT 0 1\nInjected: idle-in-transaction root blocker + 3 victims on _faulttest_lock_chain"
2026/05/21 19:58:27 INFO executing injection spec type=shell_exec phase=teardown
2026/05/21 19:58:28 INFO shell_exec completed output="pg_terminate_backend \n----------------------\n t\n t\n t\n t\n(4 rows)\n\nDROP TABLE"
=== RUN   TestExternalInjectSpecs/db-vacuum-needed
    external_inject_test.go:88: Testing external inject: Tables needing vacuum (dead tuple bloat)
2026/05/21 19:58:28 INFO executing injection spec type=sql phase=inject
2026/05/21 19:58:28 INFO executing injection spec type=sql phase=teardown
=== RUN   TestExternalInjectSpecs/db-disk-pressure
    external_inject_test.go:88: Testing external inject: Disk usage — large table growth
2026/05/21 19:58:28 INFO executing injection spec type=sql phase=inject
2026/05/21 19:58:28 INFO executing injection spec type=sql phase=teardown
=== RUN   TestExternalInjectSpecs/db-wal-stale-slot
    external_inject_test.go:88: Testing external inject: WAL accumulation — stale replication slot
2026/05/21 19:58:28 INFO executing injection spec type=sql phase=inject
2026/05/21 19:58:28 INFO executing injection spec type=sql phase=teardown
--- PASS: TestExternalInjectSpecs (2.87s)
    --- PASS: TestExternalInjectSpecs/db-table-bloat (0.50s)
    --- PASS: TestExternalInjectSpecs/db-idle-in-tx-blocker (2.11s)
    --- PASS: TestExternalInjectSpecs/db-vacuum-needed (0.07s)
    --- PASS: TestExternalInjectSpecs/db-disk-pressure (0.12s)
    --- PASS: TestExternalInjectSpecs/db-wal-stale-slot (0.07s)
=== RUN   TestCustomCatalogMergeAndInject
2026/05/21 19:58:28 INFO executing injection spec type=sql phase=inject
2026/05/21 19:58:28 INFO executing injection spec type=sql phase=teardown
--- PASS: TestCustomCatalogMergeAndInject (0.07s)
=== RUN   TestExternalTeardownCleans
2026/05/21 19:58:28 INFO executing injection spec type=sql phase=inject
2026/05/21 19:58:28 INFO executing injection spec type=sql phase=teardown
--- PASS: TestExternalTeardownCleans (0.12s)
=== RUN   TestConnection_Success
--- PASS: TestConnection_Success (0.02s)
=== RUN   TestConnection_WrongPassword
--- PASS: TestConnection_WrongPassword (0.02s)
=== RUN   TestConnection_WrongDatabase
--- PASS: TestConnection_WrongDatabase (0.02s)
=== RUN   TestRunSQLString_Success
--- PASS: TestRunSQLString_Success (0.05s)
=== RUN   TestRunSQLString_CreateAndDropTable
--- PASS: TestRunSQLString_CreateAndDropTable (0.08s)
=== RUN   TestRunSQLString_SyntaxError
--- PASS: TestRunSQLString_SyntaxError (0.02s)
=== RUN   TestQuery_PgStatActivity
--- PASS: TestQuery_PgStatActivity (0.02s)
=== RUN   TestQuery_ConnectionStats
    integration_test.go:184: output:  database | total_connections | max_connections
        ----------+-------------------+-----------------
                  |                 5 |              50
         testdb   |                 3 |              50
        (2 rows)

--- PASS: TestQuery_ConnectionStats (0.02s)
=== RUN   TestQuery_DatabaseStats
--- PASS: TestQuery_DatabaseStats (0.02s)
=== RUN   TestQuery_ConfigParameters
--- PASS: TestQuery_ConfigParameters (0.02s)
=== RUN   TestQuery_ReplicationStatus
--- PASS: TestQuery_ReplicationStatus (0.02s)
=== RUN   TestQuery_LockInfo
--- PASS: TestQuery_LockInfo (0.02s)
=== RUN   TestTableStats_CreateAndQuery
    integration_test.go:304: expected 100 live tuples, output:   relname   | n_live_tup | n_dead_tup
        ------------+------------+------------
         stats_test |        200 |          0
        (1 row)

--- PASS: TestTableStats_CreateAndQuery (0.06s)
=== RUN   TestQuery_ContextCancellation
--- PASS: TestQuery_ContextCancellation (0.10s)
=== RUN   TestQuery_ExtendedFormat
--- PASS: TestQuery_ExtendedFormat (0.03s)
=== RUN   TestQuery_BgwriterStats
--- PASS: TestQuery_BgwriterStats (0.02s)
=== RUN   TestResearchAgent_AgentCard
    research_agent_test.go:23: Research agent not running at http://localhost:1106
--- SKIP: TestResearchAgent_AgentCard (0.00s)
=== RUN   TestResearchAgent_BasicQuery
    research_agent_test.go:79: Research agent not running at http://localhost:1106
--- SKIP: TestResearchAgent_BasicQuery (0.00s)
=== RUN   TestResearchAgent_WebSearchQuery
    research_agent_test.go:115: Research agent not running at http://localhost:1106
--- SKIP: TestResearchAgent_WebSearchQuery (0.00s)
PASS
ok  	helpdesk/testing/integration	5.002s


time=2026-05-21T19:58:28.052-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/auditd-integration-3703494015/audit.db
time=2026-05-21T19:58:28.071-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:58:28.071-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:58:28.071-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:58:28.072-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:58:28.072-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:58:28.072-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:58:28.073-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:58:28.073-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:58:28.074-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:58:28.074-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:58:28.074-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:58:28.075-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:58:28.075-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:58:28.076-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:58:28.076-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:58:28.082-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:58:28.082-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-21T19:58:28.082-04:00 level=INFO msg="audit service starting" version=dev listen=:19901 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/auditd-integration-3703494015/audit.db backend=sqlite socket=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/auditd-integration-3703494015/audit.sock authz_enforcing=false
=== RUN   TestApprovalSession_Create_AssignsIDWithPrefix
time=2026-05-21T19:58:28.161-04:00 level=INFO msg="approval session created" session_id=aps_4aa16e74 granted_by=boris expires_at=2026-05-22T00:28:28.160Z allowed_classes="[write destructive]"
--- PASS: TestApprovalSession_Create_AssignsIDWithPrefix (0.00s)
=== RUN   TestApprovalSession_Create_ReturnsExpiresAt
time=2026-05-21T19:58:28.161-04:00 level=INFO msg="approval session created" session_id=aps_ab67e2e8 granted_by=alice expires_at=2026-05-22T00:58:28.161Z allowed_classes=[write]
--- PASS: TestApprovalSession_Create_ReturnsExpiresAt (0.00s)
=== RUN   TestApprovalSession_Create_MissingGrantedBy_Returns400
--- PASS: TestApprovalSession_Create_MissingGrantedBy_Returns400 (0.00s)
=== RUN   TestApprovalSession_Create_ZeroExpiry_Returns400
--- PASS: TestApprovalSession_Create_ZeroExpiry_Returns400 (0.00s)
=== RUN   TestApprovalSession_Create_EmptyClasses_Returns400
--- PASS: TestApprovalSession_Create_EmptyClasses_Returns400 (0.00s)
=== RUN   TestApprovalSession_Get_RoundTrip
time=2026-05-21T19:58:28.163-04:00 level=INFO msg="approval session created" session_id=aps_05cce872 granted_by=charlie expires_at=2026-05-22T00:13:28.162Z allowed_classes=[destructive]
--- PASS: TestApprovalSession_Get_RoundTrip (0.00s)
=== RUN   TestApprovalSession_Get_WithScope
time=2026-05-21T19:58:28.163-04:00 level=INFO msg="approval session created" session_id=aps_c47def6f granted_by=ops-team expires_at=2026-05-22T00:28:28.163Z allowed_classes=[write]
--- PASS: TestApprovalSession_Get_WithScope (0.00s)
=== RUN   TestApprovalSession_Get_NotFound_Returns404
--- PASS: TestApprovalSession_Get_NotFound_Returns404 (0.00s)
=== RUN   TestApprovalSession_Revoke_SetsRevokedFlag
time=2026-05-21T19:58:28.164-04:00 level=INFO msg="approval session created" session_id=aps_1874b3a8 granted_by=bob expires_at=2026-05-22T00:28:28.164Z allowed_classes=[write]
time=2026-05-21T19:58:28.164-04:00 level=INFO msg="approval session revoked" session_id=aps_1874b3a8
--- PASS: TestApprovalSession_Revoke_SetsRevokedFlag (0.00s)
=== RUN   TestApprovalSession_Revoke_NotFound_Returns404
--- PASS: TestApprovalSession_Revoke_NotFound_Returns404 (0.00s)
=== RUN   TestApprovalSession_Revoke_Idempotent
time=2026-05-21T19:58:28.165-04:00 level=INFO msg="approval session created" session_id=aps_ac1f58f0 granted_by=eve expires_at=2026-05-22T00:28:28.165Z allowed_classes=[write]
time=2026-05-21T19:58:28.165-04:00 level=INFO msg="approval session revoked" session_id=aps_ac1f58f0
time=2026-05-21T19:58:28.165-04:00 level=INFO msg="approval session revoked" session_id=aps_ac1f58f0
--- PASS: TestApprovalSession_Revoke_Idempotent (0.00s)
=== RUN   TestApprovalSession_Expiry_FieldsRetained
time=2026-05-21T19:58:28.166-04:00 level=INFO msg="approval session created" session_id=aps_696d014e granted_by=tester expires_at=2026-05-21T23:58:35.165Z allowed_classes=[write]
--- PASS: TestApprovalSession_Expiry_FieldsRetained (0.00s)
=== RUN   TestAuditorHTTPPollingMode
time=2026-05-21T19:58:28.175-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestAuditorHTTPPollingMode1987722337/001/audit.db
time=2026-05-21T19:58:28.194-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:58:28.194-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:58:28.195-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:58:28.195-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:58:28.196-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:58:28.196-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:58:28.197-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:58:28.197-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:58:28.197-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:58:28.198-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:58:28.198-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:58:28.198-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:58:28.199-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:58:28.199-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:58:28.199-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:58:28.206-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:58:28.206-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-21T19:58:28.206-04:00 level=INFO msg="audit service starting" version=dev listen=:19910 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestAuditorHTTPPollingMode1987722337/001/audit.db backend=sqlite socket=/tmp/audit_mon_19910.sock authz_enforcing=false
time=2026-05-21T19:58:28.654-04:00 level=INFO msg="starting auditor" socket=audit.sock log_all=false audit_service=http://localhost:19910
time=2026-05-21T19:58:28.654-04:00 level=INFO msg="webhook notifier enabled" url=http://127.0.0.1:52986
time=2026-05-21T19:58:28.654-04:00 level=INFO msg="notifiers configured" count=1
time=2026-05-21T19:58:28.654-04:00 level=INFO msg="audit socket not available; switching to HTTP polling mode" socket=audit.sock url=http://localhost:19910
time=2026-05-21T19:58:28.656-04:00 level=INFO msg="polling for new events" interval=5s url=http://localhost:19910
    audit_monitor_test.go:183: webhook received: level=INFO message="Delegation to  (0% confidence)"

🚨 [AUDIT CRITICAL] DESTRUCTIVE operation detected
time=2026-05-21T19:58:38.663-04:00 level=ERROR msg="[AUDIT CRITICAL] DESTRUCTIVE operation detected" event_id=evt_test_1779407914270936000 session_id=sess_monitor_test user_id=testuser action_class=destructive trace_id=""
    audit_monitor_test.go:183: webhook received: level=CRITICAL message="DESTRUCTIVE operation detected"
--- PASS: TestAuditorHTTPPollingMode (10.50s)
=== RUN   TestAuditorFabricationMismatchAlert
time=2026-05-21T19:58:38.687-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestAuditorFabricationMismatchAlert2157802012/001/audit.db
time=2026-05-21T19:58:38.712-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:58:38.712-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:58:38.713-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:58:38.713-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:58:38.714-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:58:38.714-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:58:38.715-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:58:38.715-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:58:38.715-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:58:38.716-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:58:38.716-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:58:38.717-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:58:38.717-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:58:38.718-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:58:38.718-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:58:38.724-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:58:38.724-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-21T19:58:38.725-04:00 level=INFO msg="audit service starting" version=dev listen=:19912 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestAuditorFabricationMismatchAlert2157802012/001/audit.db backend=sqlite socket=/tmp/audit_mon_19912.sock authz_enforcing=false
time=2026-05-21T19:58:38.789-04:00 level=INFO msg="starting auditor" socket=audit.sock log_all=false audit_service=http://localhost:19912 incident_webhook=http://127.0.0.1:53032
time=2026-05-21T19:58:38.789-04:00 level=INFO msg="audit socket not available; switching to HTTP polling mode" socket=audit.sock url=http://localhost:19912
time=2026-05-21T19:58:38.791-04:00 level=INFO msg="polling for new events" interval=5s url=http://localhost:19912

🚨 [AUDIT CRITICAL] FABRICATION RISK — agent returned success but audit trail has no matching tool executions
time=2026-05-21T19:58:48.798-04:00 level=ERROR msg="[AUDIT CRITICAL] FABRICATION RISK — agent returned success but audit trail has no matching tool executions" event_id=gv_1779407924787006000 session_id=tr_fab_1779407924778803000 user_id="" agent=postgres_database_agent action_class=destructive trace_id=tr_fab_1779407924778803000
    audit_monitor_test.go:270: incident webhook: alert_type=fabrication_mismatch severity=CRITICAL
--- PASS: TestAuditorFabricationMismatchAlert (10.14s)
=== RUN   TestSecbotHTTPPollingMode
time=2026-05-21T19:58:48.831-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingMode540357354/001/audit.db
time=2026-05-21T19:58:48.858-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:58:48.858-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:58:48.859-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:58:48.859-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:58:48.860-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:58:48.860-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:58:48.861-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:58:48.861-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:58:48.861-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:58:48.862-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:58:48.862-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:58:48.863-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:58:48.863-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:58:48.864-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:58:48.864-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:58:48.870-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:58:48.870-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-21T19:58:48.870-04:00 level=INFO msg="audit service starting" version=dev listen=:19911 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingMode540357354/001/audit.db backend=sqlite socket=/tmp/audit_mon_19911.sock authz_enforcing=false
    audit_monitor_test.go:308: secbot output:

        [19:58:49] ── Phase 1: Startup ──────────────────────────────────
        [19:58:49] Audit service: http://localhost:19911 (HTTP polling)
        [19:58:49] Gateway:       http://127.0.0.1:19999
        [19:58:49] Callback:      127.0.0.1:53057
        [19:58:49] Cooldown:      5m0s
        [19:58:49] Max events/min: 100
        [19:58:49] Dry run:       true


        [19:58:49] ── Phase 2: Connect to Audit Stream ──────────────────


        [19:58:49] ── Phase 3: Monitoring for Security Events ───────────
        [19:58:49] Watching for: high_volume, hash_mismatch, unauthorized_destructive, potential_sql_injection, potential_command_injection

        [19:58:49] Baseline: 0 existing events (not re-analyzed)
        [19:58:49] Polling audit service for new events every 5s
        [19:58:59] EVENT #1: evt_test_1779407934916503000 (type=tool_call)
        [19:58:59] SECURITY ALERT: unauthorized_destructive
        [19:58:59]   Event ID:  evt_test_1779407934916503000
        [19:58:59]   Trace ID:
        [19:58:59]   Time:      2026-05-21T23:58:54Z
        [19:58:59]   Tool:      delete_database
        [19:58:59]   Agent:     database-agent
        [19:58:59]   [DRY RUN] Would create incident bundle

--- PASS: TestSecbotHTTPPollingMode (18.13s)
=== RUN   TestSecbotHTTPPollingReconnect
time=2026-05-21T19:59:06.955-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect557634503/001/audit.db
time=2026-05-21T19:59:06.982-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:06.983-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:06.983-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:06.984-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:06.984-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:06.985-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:06.985-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:06.986-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:06.986-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:06.987-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:06.987-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:06.988-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:06.988-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:06.989-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:06.989-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:06.997-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:06.997-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-21T19:59:06.997-04:00 level=INFO msg="audit service starting" version=dev listen=:19912 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect557634503/001/audit.db backend=sqlite socket=/tmp/audit_mon_19912.sock authz_enforcing=false
    audit_monitor_test.go:362: posting first event (before restart)...
    audit_monitor_test.go:367: restarting auditd...
time=2026-05-21T19:59:22.091-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect557634503/001/audit.db
time=2026-05-21T19:59:22.097-04:00 level=INFO msg="playbooks: seed complete" seeded=0 skipped=14
time=2026-05-21T19:59:22.097-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:22.097-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-21T19:59:22.097-04:00 level=INFO msg="audit service starting" version=dev listen=:19912 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect557634503/001/audit.db backend=sqlite socket=/tmp/audit_mon_19912.sock authz_enforcing=false
    audit_monitor_test.go:376: posting second event (after restart)...
    audit_monitor_test.go:381: secbot output:

        [19:59:07] ── Phase 1: Startup ──────────────────────────────────
        [19:59:07] Audit service: http://localhost:19912 (HTTP polling)
        [19:59:07] Gateway:       http://127.0.0.1:19999
        [19:59:07] Callback:      127.0.0.1:53104
        [19:59:07] Cooldown:      5m0s
        [19:59:07] Max events/min: 100
        [19:59:07] Dry run:       true


        [19:59:07] ── Phase 2: Connect to Audit Stream ──────────────────


        [19:59:07] ── Phase 3: Monitoring for Security Events ───────────
        [19:59:07] Watching for: high_volume, hash_mismatch, unauthorized_destructive, potential_sql_injection, potential_command_injection

        [19:59:07] Baseline: 0 existing events (not re-analyzed)
        [19:59:07] Polling audit service for new events every 5s
        [19:59:17] EVENT #1: evt_test_1779407953040929000 (type=tool_call)
        [19:59:17] SECURITY ALERT: unauthorized_destructive
        [19:59:17]   Event ID:  evt_test_1779407953040929000
        [19:59:17]   Trace ID:
        [19:59:17]   Time:      2026-05-21T23:59:13Z
        [19:59:17]   Tool:      delete_database
        [19:59:17]   Agent:     database-agent
        [19:59:17]   [DRY RUN] Would create incident bundle

        [19:59:22] WARN: HTTP poll failed: Get "http://localhost:19912/v1/events?limit=200&since=2026-05-21T23:59:13Z": dial tcp [::1]:19912: connect: connection refused
        [19:59:27] EVENT #2: evt_test_1779407962166176000 (type=tool_call)
        [19:59:27] SECURITY ALERT: unauthorized_destructive
        [19:59:27]   Event ID:  evt_test_1779407962166176000
        [19:59:27]   Trace ID:
        [19:59:27]   Time:      2026-05-21T23:59:22Z
        [19:59:27]   Tool:      delete_database
        [19:59:27]   Agent:     database-agent
        [19:59:27]   [DRY RUN] Would create incident bundle

--- PASS: TestSecbotHTTPPollingReconnect (27.25s)
=== RUN   TestHealth
--- PASS: TestHealth (0.00s)
=== RUN   TestAudit_RecordEvent_ReturnsHashes
--- PASS: TestAudit_RecordEvent_ReturnsHashes (0.00s)
=== RUN   TestAudit_RecordedEventIsQueryable
--- PASS: TestAudit_RecordedEventIsQueryable (0.00s)
=== RUN   TestAudit_FilterByEventType
--- PASS: TestAudit_FilterByEventType (0.00s)
=== RUN   TestAudit_RecordOutcome
--- PASS: TestAudit_RecordOutcome (0.00s)
=== RUN   TestAudit_HashChainIsValid
--- PASS: TestAudit_HashChainIsValid (0.01s)
=== RUN   TestAudit_VerifyCountIncrements
--- PASS: TestAudit_VerifyCountIncrements (0.00s)
=== RUN   TestApprovals_CreateAndGet
time=2026-05-21T19:59:34.208-04:00 level=INFO msg="approval request created" approval_id=apr_39bc30e5 action_class=write tool=execute_sql agent=database-agent requested_by=alice
--- PASS: TestApprovals_CreateAndGet (0.00s)
=== RUN   TestApprovals_ListPending
time=2026-05-21T19:59:34.211-04:00 level=INFO msg="approval request created" approval_id=apr_2a319d62 action_class=destructive tool=drop_table agent=database-agent requested_by=bob
--- PASS: TestApprovals_ListPending (0.00s)
=== RUN   TestApprovals_Approve
time=2026-05-21T19:59:34.214-04:00 level=INFO msg="approval request created" approval_id=apr_8ad750ea action_class=write tool=update_config agent=k8s-agent requested_by=carol
time=2026-05-21T19:59:34.216-04:00 level=INFO msg="approval granted" approval_id=apr_8ad750ea approved_by=manager valid_for=0s
--- PASS: TestApprovals_Approve (0.00s)
=== RUN   TestApprovals_Deny
time=2026-05-21T19:59:34.219-04:00 level=INFO msg="approval request created" approval_id=apr_18cfa0a1 action_class=destructive tool=delete_namespace agent=k8s-agent requested_by=dave
time=2026-05-21T19:59:34.220-04:00 level=INFO msg="approval denied" approval_id=apr_18cfa0a1 denied_by=admin
--- PASS: TestApprovals_Deny (0.00s)
=== RUN   TestApprovals_Cancel
time=2026-05-21T19:59:34.222-04:00 level=INFO msg="approval request created" approval_id=apr_0887dbe1 action_class=write tool=scale_deployment agent=k8s-agent requested_by=eve
time=2026-05-21T19:59:34.222-04:00 level=INFO msg="approval cancelled" approval_id=apr_0887dbe1
--- PASS: TestApprovals_Cancel (0.00s)
=== RUN   TestApprovals_FilterByStatus
time=2026-05-21T19:59:34.224-04:00 level=INFO msg="approval request created" approval_id=apr_30947a25 action_class=write tool=patch_service agent=k8s-agent requested_by=frank
time=2026-05-21T19:59:34.225-04:00 level=INFO msg="approval granted" approval_id=apr_30947a25 approved_by=lead valid_for=0s
--- PASS: TestApprovals_FilterByStatus (0.00s)
=== RUN   TestApprovals_MissingActionClass
--- PASS: TestApprovals_MissingActionClass (0.00s)
=== RUN   TestApprovals_MissingRequestedBy
--- PASS: TestApprovals_MissingRequestedBy (0.00s)
=== RUN   TestApprovals_GetNonExistent
--- PASS: TestApprovals_GetNonExistent (0.00s)
=== RUN   TestGovernance_HealthAndInfo
--- PASS: TestGovernance_HealthAndInfo (0.00s)
=== RUN   TestGovernance_AuditCountReflectsRecordedEvents
--- PASS: TestGovernance_AuditCountReflectsRecordedEvents (0.00s)
=== RUN   TestGovernance_PoliciesWithoutEngine
--- PASS: TestGovernance_PoliciesWithoutEngine (0.00s)
=== RUN   TestGovernance_InfoWithPolicyEnabled
time=2026-05-21T19:59:34.248-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_InfoWithPolicyEnabled3409379662/002/audit.db
time=2026-05-21T19:59:34.270-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:34.270-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:34.271-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:34.271-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:34.272-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:34.272-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:34.272-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:34.273-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:34.273-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:34.274-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:34.274-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:34.274-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:34.275-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:34.275-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:34.275-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:34.281-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:34.281-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_InfoWithPolicyEnabled3409379662/001/policies.yaml policies=1
time=2026-05-21T19:59:34.281-04:00 level=INFO msg="audit service starting" version=dev listen=:19902 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_InfoWithPolicyEnabled3409379662/002/audit.db backend=sqlite socket=/tmp/atest-229715000.sock authz_enforcing=false
--- PASS: TestGovernance_InfoWithPolicyEnabled (0.11s)
=== RUN   TestIntegration_AgentReasoningRoundTrip
--- PASS: TestIntegration_AgentReasoningRoundTrip (0.00s)
=== RUN   TestGovernance_PoliciesSummaryWithEngine
time=2026-05-21T19:59:34.348-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_PoliciesSummaryWithEngine2983525571/002/audit.db
time=2026-05-21T19:59:34.365-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:34.365-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:34.366-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:34.366-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:34.367-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:34.367-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:34.367-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:34.368-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:34.368-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:34.368-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:34.369-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:34.369-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:34.369-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:34.370-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:34.370-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:34.375-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:34.375-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_PoliciesSummaryWithEngine2983525571/001/policies.yaml policies=1
time=2026-05-21T19:59:34.376-04:00 level=INFO msg="audit service starting" version=dev listen=:19902 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_PoliciesSummaryWithEngine2983525571/002/audit.db backend=sqlite socket=/tmp/atest-337911000.sock authz_enforcing=false
--- PASS: TestGovernance_PoliciesSummaryWithEngine (0.11s)
=== RUN   TestGovernance_Explain_DefaultConfig
time=2026-05-21T19:59:34.453-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_Explain_DefaultConfig54657849/002/audit.db
time=2026-05-21T19:59:34.471-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:34.471-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:34.472-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:34.472-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:34.472-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:34.473-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:34.474-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:34.474-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:34.474-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:34.475-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:34.475-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:34.475-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:34.476-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:34.476-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:34.476-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:34.482-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:34.482-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_Explain_DefaultConfig54657849/001/policies.yaml policies=1
time=2026-05-21T19:59:34.482-04:00 level=INFO msg="audit service starting" version=dev listen=:19903 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_Explain_DefaultConfig54657849/002/audit.db backend=sqlite socket=/tmp/atest3-443548000.sock authz_enforcing=false
time=2026-05-21T19:59:34.547-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=db-policy message="writes require approval"
--- PASS: TestGovernance_Explain_DefaultConfig (0.11s)
=== RUN   TestAudit_WriteToolEvent_RecordedAndQueryable
--- PASS: TestAudit_WriteToolEvent_RecordedAndQueryable (0.00s)
=== RUN   TestAudit_DestructiveToolEvent_RecordedAndQueryable
--- PASS: TestAudit_DestructiveToolEvent_RecordedAndQueryable (0.00s)
=== RUN   TestAudit_MultipleToolEvents_HashChainValid
--- PASS: TestAudit_MultipleToolEvents_HashChainValid (0.00s)
=== RUN   TestAudit_WriteApprovalWorkflow_ForNewTools
time=2026-05-21T19:59:34.554-04:00 level=INFO msg="approval request created" approval_id=apr_6b57b9d0 action_class=write tool=cancel_query agent=postgres-agent requested_by=operator
time=2026-05-21T19:59:34.555-04:00 level=INFO msg="approval granted" approval_id=apr_6b57b9d0 approved_by=senior-dba valid_for=0s
--- PASS: TestAudit_WriteApprovalWorkflow_ForNewTools (0.00s)
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/terminate_connection
time=2026-05-21T19:59:34.556-04:00 level=INFO msg="approval request created" approval_id=apr_9f5193e5 action_class=destructive tool=terminate_connection agent=test-agent requested_by=sre-oncall
time=2026-05-21T19:59:34.556-04:00 level=INFO msg="approval denied" approval_id=apr_9f5193e5 denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/terminate_idle_connections
time=2026-05-21T19:59:34.557-04:00 level=INFO msg="approval request created" approval_id=apr_edf37708 action_class=destructive tool=terminate_idle_connections agent=test-agent requested_by=sre-oncall
time=2026-05-21T19:59:34.557-04:00 level=INFO msg="approval denied" approval_id=apr_edf37708 denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/delete_pod
time=2026-05-21T19:59:34.558-04:00 level=INFO msg="approval request created" approval_id=apr_ef7a8ee1 action_class=destructive tool=delete_pod agent=test-agent requested_by=sre-oncall
time=2026-05-21T19:59:34.558-04:00 level=INFO msg="approval denied" approval_id=apr_ef7a8ee1 denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/restart_deployment
time=2026-05-21T19:59:34.558-04:00 level=INFO msg="approval request created" approval_id=apr_2967da68 action_class=destructive tool=restart_deployment agent=test-agent requested_by=sre-oncall
time=2026-05-21T19:59:34.559-04:00 level=INFO msg="approval denied" approval_id=apr_2967da68 denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/scale_deployment
time=2026-05-21T19:59:34.559-04:00 level=INFO msg="approval request created" approval_id=apr_1da14aaf action_class=destructive tool=scale_deployment agent=test-agent requested_by=sre-oncall
time=2026-05-21T19:59:34.560-04:00 level=INFO msg="approval denied" approval_id=apr_1da14aaf denied_by=change-manager
--- PASS: TestAudit_DestructiveApprovalWorkflow_ForNewTools (0.00s)
    --- PASS: TestAudit_DestructiveApprovalWorkflow_ForNewTools/terminate_connection (0.00s)
    --- PASS: TestAudit_DestructiveApprovalWorkflow_ForNewTools/terminate_idle_connections (0.00s)
    --- PASS: TestAudit_DestructiveApprovalWorkflow_ForNewTools/delete_pod (0.00s)
    --- PASS: TestAudit_DestructiveApprovalWorkflow_ForNewTools/restart_deployment (0.00s)
    --- PASS: TestAudit_DestructiveApprovalWorkflow_ForNewTools/scale_deployment (0.00s)
=== RUN   TestIntegration_DelegationVerification_MismatchSurfacesInJourneys
--- PASS: TestIntegration_DelegationVerification_MismatchSurfacesInJourneys (0.00s)
=== RUN   TestIntegration_VerifyTrace_QueryContract
--- PASS: TestIntegration_VerifyTrace_QueryContract (0.00s)
=== RUN   TestIntegration_VerifyTrace_SinceFilter
--- PASS: TestIntegration_VerifyTrace_SinceFilter (0.00s)
=== RUN   TestIntegration_DelegationVerification_CleanVerification
--- PASS: TestIntegration_DelegationVerification_CleanVerification (0.00s)
=== RUN   TestIdentity_UserPrincipal_FlowsThroughToAuditEvent
time=2026-05-21T19:59:34.577-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_UserPrincipal_FlowsThroughToAuditEvent1578839870/002/audit.db
time=2026-05-21T19:59:34.597-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:34.598-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:34.598-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:34.598-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:34.599-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:34.599-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:34.599-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:34.600-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:34.600-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:34.601-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:34.602-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:34.602-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:34.602-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:34.603-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:34.603-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:34.608-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:34.608-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_UserPrincipal_FlowsThroughToAuditEvent1578839870/001/policies.yaml policies=4
time=2026-05-21T19:59:34.608-04:00 level=INFO msg="audit service starting" version=dev listen=:53139 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_UserPrincipal_FlowsThroughToAuditEvent1578839870/002/audit.db backend=sqlite socket=/tmp/aidtest-567314000.sock authz_enforcing=false
--- PASS: TestIdentity_UserPrincipal_FlowsThroughToAuditEvent (0.11s)
=== RUN   TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent
time=2026-05-21T19:59:34.684-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent259588102/002/audit.db
time=2026-05-21T19:59:34.702-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:34.703-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:34.704-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:34.704-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:34.705-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:34.705-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:34.705-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:34.706-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:34.706-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:34.706-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:34.707-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:34.707-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:34.708-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:34.708-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:34.708-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:34.714-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:34.714-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent259588102/001/policies.yaml policies=4
time=2026-05-21T19:59:34.714-04:00 level=INFO msg="audit service starting" version=dev listen=:53146 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent259588102/002/audit.db backend=sqlite socket=/tmp/aidtest-674465000.sock authz_enforcing=false
--- PASS: TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent (0.11s)
=== RUN   TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity
time=2026-05-21T19:59:34.790-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity1152481843/002/audit.db
time=2026-05-21T19:59:34.808-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:34.809-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:34.809-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:34.809-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:34.810-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:34.810-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:34.810-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:34.811-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:34.811-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:34.812-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:34.812-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:34.812-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:34.812-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:34.813-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:34.813-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:34.818-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:34.818-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity1152481843/001/policies.yaml policies=4
time=2026-05-21T19:59:34.819-04:00 level=INFO msg="audit service starting" version=dev listen=:53153 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity1152481843/002/audit.db backend=sqlite socket=/tmp/aidtest-781206000.sock authz_enforcing=false
--- PASS: TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity (0.11s)
=== RUN   TestIdentity_DBACanWrite
time=2026-05-21T19:59:34.898-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DBACanWrite3861092307/002/audit.db
time=2026-05-21T19:59:34.915-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:34.915-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:34.916-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:34.916-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:34.916-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:34.917-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:34.917-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:34.918-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:34.918-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:34.918-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:34.919-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:34.919-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:34.919-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:34.920-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:34.920-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:34.925-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:34.925-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DBACanWrite3861092307/001/policies.yaml policies=4
time=2026-05-21T19:59:34.926-04:00 level=INFO msg="audit service starting" version=dev listen=:53160 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DBACanWrite3861092307/002/audit.db backend=sqlite socket=/tmp/aidtest-888994000.sock authz_enforcing=false
--- PASS: TestIdentity_DBACanWrite (0.11s)
=== RUN   TestIdentity_NonDBADeniedWrite
time=2026-05-21T19:59:35.005-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonDBADeniedWrite3655654293/002/audit.db
time=2026-05-21T19:59:35.024-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:35.024-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:35.024-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:35.025-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:35.025-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:35.025-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:35.026-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:35.026-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:35.027-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:35.027-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:35.027-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:35.028-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:35.028-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:35.028-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:35.028-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:35.033-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:35.033-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonDBADeniedWrite3655654293/001/policies.yaml policies=4
time=2026-05-21T19:59:35.034-04:00 level=INFO msg="audit service starting" version=dev listen=:53167 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonDBADeniedWrite3655654293/002/audit.db backend=sqlite socket=/tmp/aidtest-995630000.sock authz_enforcing=false
time=2026-05-21T19:59:35.099-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=default-policy user=bob@example.com message="writes not allowed"
time=2026-05-21T19:59:35.100-04:00 level=WARN msg="policy check: DENY" event_id=pol_f1f60ea7 resource=database:prod-db action=write policy=default-policy agent=""
--- PASS: TestIdentity_NonDBADeniedWrite (0.11s)
=== RUN   TestIdentity_OncallEmergencyBreakGlass
time=2026-05-21T19:59:35.112-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_OncallEmergencyBreakGlass2595491922/002/audit.db
time=2026-05-21T19:59:35.129-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:35.129-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:35.130-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:35.130-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:35.130-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:35.131-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:35.131-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:35.131-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:35.132-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:35.132-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:35.132-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:35.133-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:35.133-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:35.133-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:35.133-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:35.140-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:35.140-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_OncallEmergencyBreakGlass2595491922/001/policies.yaml policies=4
time=2026-05-21T19:59:35.140-04:00 level=INFO msg="audit service starting" version=dev listen=:53174 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_OncallEmergencyBreakGlass2595491922/002/audit.db backend=sqlite socket=/tmp/aidtest-102448000.sock authz_enforcing=false
--- PASS: TestIdentity_OncallEmergencyBreakGlass (0.11s)
=== RUN   TestIdentity_NonOncallEmergencyDenied
time=2026-05-21T19:59:35.218-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonOncallEmergencyDenied1980664463/002/audit.db
time=2026-05-21T19:59:35.235-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:35.236-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:35.236-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:35.237-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:35.237-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:35.237-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:35.238-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:35.238-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:35.238-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:35.239-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:35.239-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:35.239-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:35.240-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:35.241-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:35.241-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:35.246-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:35.246-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonOncallEmergencyDenied1980664463/001/policies.yaml policies=4
time=2026-05-21T19:59:35.246-04:00 level=INFO msg="audit service starting" version=dev listen=:53181 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonOncallEmergencyDenied1980664463/002/audit.db backend=sqlite socket=/tmp/aidtest-209274000.sock authz_enforcing=false
time=2026-05-21T19:59:35.313-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=default-policy user=dave@example.com purpose=emergency message="writes not allowed"
time=2026-05-21T19:59:35.314-04:00 level=WARN msg="policy check: DENY" event_id=pol_a4a4e6fd resource=database:prod-db action=write policy=default-policy agent=""
--- PASS: TestIdentity_NonOncallEmergencyDenied (0.11s)
=== RUN   TestIdentity_DiagnosticPurposeBlocksWrite
time=2026-05-21T19:59:35.325-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DiagnosticPurposeBlocksWrite1222952917/002/audit.db
time=2026-05-21T19:59:35.343-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:35.343-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:35.343-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:35.344-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:35.344-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:35.345-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:35.345-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:35.345-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:35.346-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:35.346-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:35.346-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:35.347-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:35.347-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:35.348-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:35.348-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:35.353-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:35.353-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DiagnosticPurposeBlocksWrite1222952917/001/policies.yaml policies=4
time=2026-05-21T19:59:35.353-04:00 level=INFO msg="audit service starting" version=dev listen=:53188 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DiagnosticPurposeBlocksWrite1222952917/002/audit.db backend=sqlite socket=/tmp/aidtest-316030000.sock authz_enforcing=false
time=2026-05-21T19:59:35.419-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=dba-policy user=alice@example.com purpose=diagnostic message="Purpose \"diagnostic\" is in the blocked list [diagnostic]"
time=2026-05-21T19:59:35.420-04:00 level=WARN msg="policy check: DENY" event_id=pol_1e466ed9 resource=database:prod-db action=write policy=dba-policy agent=""
--- PASS: TestIdentity_DiagnosticPurposeBlocksWrite (0.11s)
=== RUN   TestIdentity_RemediationPurposeAllowsDBAWrite
time=2026-05-21T19:59:35.432-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_RemediationPurposeAllowsDBAWrite3986718285/002/audit.db
time=2026-05-21T19:59:35.449-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:35.449-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:35.450-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:35.450-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:35.450-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:35.451-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:35.451-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:35.452-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:35.452-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:35.452-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:35.453-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:35.453-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:35.453-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:35.454-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:35.454-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:35.460-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:35.460-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_RemediationPurposeAllowsDBAWrite3986718285/001/policies.yaml policies=4
time=2026-05-21T19:59:35.460-04:00 level=INFO msg="audit service starting" version=dev listen=:53195 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_RemediationPurposeAllowsDBAWrite3986718285/002/audit.db backend=sqlite socket=/tmp/aidtest-422476000.sock authz_enforcing=false
--- PASS: TestIdentity_RemediationPurposeAllowsDBAWrite (0.11s)
=== RUN   TestIdentity_PIIReadWithPurpose_Allowed
time=2026-05-21T19:59:35.550-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithPurpose_Allowed3585978794/002/audit.db
time=2026-05-21T19:59:35.581-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:35.582-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:35.582-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:35.583-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:35.583-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:35.584-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:35.584-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:35.584-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:35.585-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:35.586-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:35.586-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:35.586-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:35.587-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:35.587-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:35.587-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:35.594-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:35.595-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithPurpose_Allowed3585978794/001/policies.yaml policies=4
time=2026-05-21T19:59:35.595-04:00 level=INFO msg="audit service starting" version=dev listen=:53202 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithPurpose_Allowed3585978794/002/audit.db backend=sqlite socket=/tmp/aidtest-534969000.sock authz_enforcing=false
--- PASS: TestIdentity_PIIReadWithPurpose_Allowed (0.11s)
=== RUN   TestIdentity_PIIReadWithoutPurpose_Denied
time=2026-05-21T19:59:35.652-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithoutPurpose_Denied479313118/002/audit.db
time=2026-05-21T19:59:35.672-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:35.672-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:35.673-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:35.673-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:35.674-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:35.674-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:35.674-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:35.675-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:35.675-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:35.676-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:35.676-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:35.676-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:35.677-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:35.677-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:35.677-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:35.683-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:35.683-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithoutPurpose_Denied479313118/001/policies.yaml policies=4
time=2026-05-21T19:59:35.683-04:00 level=INFO msg="audit service starting" version=dev listen=:53209 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithoutPurpose_Denied479313118/002/audit.db backend=sqlite socket=/tmp/aidtest-643625000.sock authz_enforcing=false
time=2026-05-21T19:59:35.747-04:00 level=WARN msg="policy decision: DENY" action=read resource_type=database resource_name=customers effect=deny policy=pii-protection user=bob@example.com resource_sensitivity=[pii] message="Purpose \"\" is not in the allowed list [diagnostic compliance remediation]"
time=2026-05-21T19:59:35.748-04:00 level=WARN msg="policy check: DENY" event_id=pol_9beec44e resource=database:customers action=read policy=pii-protection agent=""
--- PASS: TestIdentity_PIIReadWithoutPurpose_Denied (0.11s)
=== RUN   TestIdentity_PIIWrite_AlwaysDenied
time=2026-05-21T19:59:35.760-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIWrite_AlwaysDenied2392874404/002/audit.db
time=2026-05-21T19:59:35.780-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:35.781-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:35.781-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:35.782-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:35.782-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:35.782-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:35.783-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:35.783-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:35.784-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:35.784-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:35.784-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:35.785-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:35.785-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:35.786-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:35.786-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:35.792-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:35.792-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIWrite_AlwaysDenied2392874404/001/policies.yaml policies=4
time=2026-05-21T19:59:35.792-04:00 level=INFO msg="audit service starting" version=dev listen=:53216 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIWrite_AlwaysDenied2392874404/002/audit.db backend=sqlite socket=/tmp/aidtest-750636000.sock authz_enforcing=false
time=2026-05-21T19:59:35.854-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=customers effect=deny policy=pii-protection user=alice@example.com resource_sensitivity=[pii] purpose=remediation message="Writes to PII databases are prohibited."
time=2026-05-21T19:59:35.855-04:00 level=WARN msg="policy check: DENY" event_id=pol_03d0c8f7 resource=database:customers action=write policy=pii-protection agent=""
--- PASS: TestIdentity_PIIWrite_AlwaysDenied (0.11s)
=== RUN   TestIdentity_Explain_WithPurposeAndSensitivity_Allow
time=2026-05-21T19:59:35.866-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Allow1552459911/002/audit.db
time=2026-05-21T19:59:35.884-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:35.885-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:35.885-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:35.885-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:35.886-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:35.886-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:35.887-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:35.887-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:35.887-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:35.888-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:35.888-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:35.888-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:35.889-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:35.889-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:35.889-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:35.895-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:35.895-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Allow1552459911/001/policies.yaml policies=4
time=2026-05-21T19:59:35.895-04:00 level=INFO msg="audit service starting" version=dev listen=:53223 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Allow1552459911/002/audit.db backend=sqlite socket=/tmp/aidtest-857350000.sock authz_enforcing=false
--- PASS: TestIdentity_Explain_WithPurposeAndSensitivity_Allow (0.11s)
=== RUN   TestIdentity_Explain_WithPurposeAndSensitivity_Deny
time=2026-05-21T19:59:35.974-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Deny128568774/002/audit.db
time=2026-05-21T19:59:35.996-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:35.996-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:35.996-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:35.997-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:35.998-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:35.998-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:35.998-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:35.999-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:35.999-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:36.000-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:36.000-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:36.000-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:36.001-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:36.001-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:36.001-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:36.008-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:36.008-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Deny128568774/001/policies.yaml policies=4
time=2026-05-21T19:59:36.008-04:00 level=INFO msg="audit service starting" version=dev listen=:53230 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Deny128568774/002/audit.db backend=sqlite socket=/tmp/aidtest-963539000.sock authz_enforcing=false
time=2026-05-21T19:59:36.067-04:00 level=WARN msg="policy decision: DENY" action=read resource_type=database resource_name=customers effect=deny policy=pii-protection resource_sensitivity=[pii] message="Purpose \"\" is not in the allowed list [diagnostic compliance remediation]"
--- PASS: TestIdentity_Explain_WithPurposeAndSensitivity_Deny (0.11s)
=== RUN   TestIdentity_Explain_DiagnosticPurpose_DeniesWrite
time=2026-05-21T19:59:36.079-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_DiagnosticPurpose_DeniesWrite2049929866/002/audit.db
time=2026-05-21T19:59:36.162-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:36.162-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:36.162-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:36.163-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:36.163-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:36.164-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:36.164-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:36.165-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:36.165-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:36.165-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:36.166-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:36.166-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:36.166-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:36.167-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:36.167-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:36.173-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:36.173-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_DiagnosticPurpose_DeniesWrite2049929866/001/policies.yaml policies=4
time=2026-05-21T19:59:36.173-04:00 level=INFO msg="audit service starting" version=dev listen=:53237 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_DiagnosticPurpose_DeniesWrite2049929866/002/audit.db backend=sqlite socket=/tmp/aidtest-69994000.sock authz_enforcing=false
time=2026-05-21T19:59:36.274-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=default-policy purpose=diagnostic message="writes not allowed"
--- PASS: TestIdentity_Explain_DiagnosticPurpose_DeniesWrite (0.21s)
=== RUN   TestIdentity_FullPolicyDecisionRoundTrip
time=2026-05-21T19:59:36.294-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_FullPolicyDecisionRoundTrip1813132768/002/audit.db
time=2026-05-21T19:59:36.317-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-21T19:59:36.317-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-21T19:59:36.318-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-21T19:59:36.318-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-21T19:59:36.319-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-21T19:59:36.319-04:00 level=INFO msg="playbooks: seeded system playbook" name="Idle-in-Transaction Blocker Triage" series=pbs_idle_blocker_triage version=1.0 active=true
time=2026-05-21T19:59:36.320-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-21T19:59:36.320-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-21T19:59:36.320-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-21T19:59:36.321-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-21T19:59:36.321-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-21T19:59:36.321-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-21T19:59:36.322-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-21T19:59:36.322-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-21T19:59:36.322-04:00 level=INFO msg="playbooks: seed complete" seeded=14 skipped=0
time=2026-05-21T19:59:36.329-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-21T19:59:36.329-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_FullPolicyDecisionRoundTrip1813132768/001/policies.yaml policies=4
time=2026-05-21T19:59:36.329-04:00 level=INFO msg="audit service starting" version=dev listen=:53246 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_FullPolicyDecisionRoundTrip1813132768/002/audit.db backend=sqlite socket=/tmp/aidtest-278443000.sock authz_enforcing=false
--- PASS: TestIdentity_FullPolicyDecisionRoundTrip (0.11s)
PASS
ok  	helpdesk/testing/integration/governance	72.409s
=== RUN   TestDatabaseDirectRegistry_AllToolsRegistered
--- PASS: TestDatabaseDirectRegistry_AllToolsRegistered (0.00s)
=== RUN   TestArgsToStruct_RoundTrip
--- PASS: TestArgsToStruct_RoundTrip (0.00s)
=== RUN   TestArgsToStruct_EmptyArgs
--- PASS: TestArgsToStruct_EmptyArgs (0.00s)
=== RUN   TestDatabaseDirectRegistry_ToolCallable
2026/05/21 19:58:24 INFO tool ok name=check_connection ms=0
--- PASS: TestDatabaseDirectRegistry_ToolCallable (0.00s)
=== RUN   TestReplicaIdentityFull_Nil
--- PASS: TestReplicaIdentityFull_Nil (0.00s)
=== RUN   TestReplicaIdentityFull_NilMap
--- PASS: TestReplicaIdentityFull_NilMap (0.00s)
=== RUN   TestReplicaIdentityFull_PublicSchema
--- PASS: TestReplicaIdentityFull_PublicSchema (0.00s)
=== RUN   TestReplicaIdentityFull_NonPublicSchema
--- PASS: TestReplicaIdentityFull_NonPublicSchema (0.00s)
=== RUN   TestReplicaIdentityFull_DefaultIdentity
--- PASS: TestReplicaIdentityFull_DefaultIdentity (0.00s)
=== RUN   TestNewWALBracket_SlotName_ShortTraceID
--- PASS: TestNewWALBracket_SlotName_ShortTraceID (0.00s)
=== RUN   TestNewWALBracket_SlotName_TruncatesTo8
--- PASS: TestNewWALBracket_SlotName_TruncatesTo8 (0.00s)
=== RUN   TestNewWALBracket_SlotName_ExactlyEight
--- PASS: TestNewWALBracket_SlotName_ExactlyEight (0.00s)
=== RUN   TestDetectRollbackCapability_Override_WALDecode
--- PASS: TestDetectRollbackCapability_Override_WALDecode (0.00s)
=== RUN   TestDetectRollbackCapability_Override_RowCapture
--- PASS: TestDetectRollbackCapability_Override_RowCapture (0.00s)
=== RUN   TestDetectRollbackCapability_Override_None
--- PASS: TestDetectRollbackCapability_Override_None (0.00s)
=== RUN   TestDetectRollbackCapability_AutoDetect_FallsBackToRowCapture
--- PASS: TestDetectRollbackCapability_AutoDetect_FallsBackToRowCapture (0.00s)
=== RUN   TestDetectRollbackCapability_ReturnsNonNilCapability
--- PASS: TestDetectRollbackCapability_ReturnsNonNilCapability (0.00s)
=== RUN   TestInspectQuery_AllColumnsPresent
--- PASS: TestInspectQuery_AllColumnsPresent (0.05s)
=== RUN   TestInspectConnection_NonExistentPID
2026/05/21 19:58:24 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/21 19:58:24 INFO tool ok name=get_session_info ms=23
--- PASS: TestInspectConnection_NonExistentPID (0.05s)
=== RUN   TestInspectConnection_WriteTransaction
2026/05/21 19:58:25 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/21 19:58:25 INFO tool ok name=get_session_info ms=22
--- PASS: TestInspectConnection_WriteTransaction (20.12s)
=== RUN   TestInspectConnection_ReadOnlyTransaction
2026/05/21 19:58:45 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/21 19:58:45 INFO tool ok name=get_session_info ms=26
--- PASS: TestInspectConnection_ReadOnlyTransaction (0.12s)
=== RUN   TestGetSessionInfoTool_Integration
2026/05/21 19:58:45 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/21 19:58:45 INFO tool ok name=get_session_info ms=21
--- PASS: TestGetSessionInfoTool_Integration (20.10s)
=== RUN   TestGetStatusSummaryTool_Integration
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_status_summary ms=41
--- PASS: TestGetStatusSummaryTool_Integration (0.13s)
=== RUN   TestDetectRollbackCapability_Integration
    tools_integration_test.go:364: WALLevel = "replica"  HasReplication = true  Mode = "row_capture"
--- PASS: TestDetectRollbackCapability_Integration (0.03s)
=== RUN   TestDetectRollbackCapability_Integration_Override
--- PASS: TestDetectRollbackCapability_Integration_Override (0.03s)
=== RUN   TestGetBgwriterStatsTool_Integration
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_bgwriter_stats ms=35
--- PASS: TestGetBgwriterStatsTool_Integration (0.10s)
=== RUN   TestWALBracket_Open_FailsWithoutLogicalWAL
    tools_integration_test.go:462: WALBracket.Open correctly failed on wal_level="replica": create replication slot helpdesk_rbk_integrat: ERROR: logical decoding requires wal_level >= logical (SQLSTATE 55000)
--- PASS: TestWALBracket_Open_FailsWithoutLogicalWAL (0.07s)
=== RUN   TestParseRowsAffected
=== RUN   TestParseRowsAffected/DELETE
=== RUN   TestParseRowsAffected/UPDATE
=== RUN   TestParseRowsAffected/INSERT
=== RUN   TestParseRowsAffected/INSERT_single_row
=== RUN   TestParseRowsAffected/DELETE_embedded_in_expanded_output
=== RUN   TestParseRowsAffected/zero_rows_deleted
=== RUN   TestParseRowsAffected/SELECT_returns_nothing
=== RUN   TestParseRowsAffected/empty_output
=== RUN   TestParseRowsAffected/unrelated_output
=== RUN   TestParseRowsAffected/verb_prefix_but_no_number
--- PASS: TestParseRowsAffected (0.00s)
    --- PASS: TestParseRowsAffected/DELETE (0.00s)
    --- PASS: TestParseRowsAffected/UPDATE (0.00s)
    --- PASS: TestParseRowsAffected/INSERT (0.00s)
    --- PASS: TestParseRowsAffected/INSERT_single_row (0.00s)
    --- PASS: TestParseRowsAffected/DELETE_embedded_in_expanded_output (0.00s)
    --- PASS: TestParseRowsAffected/zero_rows_deleted (0.00s)
    --- PASS: TestParseRowsAffected/SELECT_returns_nothing (0.00s)
    --- PASS: TestParseRowsAffected/empty_output (0.00s)
    --- PASS: TestParseRowsAffected/unrelated_output (0.00s)
    --- PASS: TestParseRowsAffected/verb_prefix_but_no_number (0.00s)
=== RUN   TestDiagnosePsqlError
=== RUN   TestDiagnosePsqlError/database_does_not_exist
=== RUN   TestDiagnosePsqlError/connection_refused
=== RUN   TestDiagnosePsqlError/unknown_host
=== RUN   TestDiagnosePsqlError/password_auth_failed
=== RUN   TestDiagnosePsqlError/no_pg_hba.conf_entry
=== RUN   TestDiagnosePsqlError/timeout_expired
=== RUN   TestDiagnosePsqlError/could_not_connect
=== RUN   TestDiagnosePsqlError/role_does_not_exist_(caught_by_does-not-exist_case)
=== RUN   TestDiagnosePsqlError/ssl_unsupported
=== RUN   TestDiagnosePsqlError/ssl_required
=== RUN   TestDiagnosePsqlError/unknown_error_returns_empty
=== RUN   TestDiagnosePsqlError/empty_output_returns_empty
--- PASS: TestDiagnosePsqlError (0.00s)
    --- PASS: TestDiagnosePsqlError/database_does_not_exist (0.00s)
    --- PASS: TestDiagnosePsqlError/connection_refused (0.00s)
    --- PASS: TestDiagnosePsqlError/unknown_host (0.00s)
    --- PASS: TestDiagnosePsqlError/password_auth_failed (0.00s)
    --- PASS: TestDiagnosePsqlError/no_pg_hba.conf_entry (0.00s)
    --- PASS: TestDiagnosePsqlError/timeout_expired (0.00s)
    --- PASS: TestDiagnosePsqlError/could_not_connect (0.00s)
    --- PASS: TestDiagnosePsqlError/role_does_not_exist_(caught_by_does-not-exist_case) (0.00s)
    --- PASS: TestDiagnosePsqlError/ssl_unsupported (0.00s)
    --- PASS: TestDiagnosePsqlError/ssl_required (0.00s)
    --- PASS: TestDiagnosePsqlError/unknown_error_returns_empty (0.00s)
    --- PASS: TestDiagnosePsqlError/empty_output_returns_empty (0.00s)
=== RUN   TestRunPsql_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestRunPsql_Success (0.00s)
=== RUN   TestRunPsql_Error
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=badhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool="" ms=0 err="exit status 1" output="connection refused"
--- PASS: TestRunPsql_Error (0.00s)
=== RUN   TestRunPsql_EmptyOutput
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool="" ms=0 err="exit status 1" output="(no output from psql)"
--- PASS: TestRunPsql_EmptyOutput (0.00s)
=== RUN   TestRunPsql_UndiagnosedError
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool="" ms=0 err="exit status 1" output="some weird error"
--- PASS: TestRunPsql_UndiagnosedError (0.00s)
=== RUN   TestRunPsql_EmptyConnStr
--- PASS: TestRunPsql_EmptyConnStr (0.00s)
=== RUN   TestCheckConnectionTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=check_connection ms=0
--- PASS: TestCheckConnectionTool_Success (0.00s)
=== RUN   TestCheckConnectionTool_Failure
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=check_connection ms=0 err="exit status 1" output="password authentication failed"
--- PASS: TestCheckConnectionTool_Failure (0.00s)
=== RUN   TestGetServerInfoTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_server_info ms=0
--- PASS: TestGetServerInfoTool_Success (0.00s)
=== RUN   TestGetServerInfoTool_Failure
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=get_server_info ms=0 err="exit status 1" output="connection refused"
--- PASS: TestGetServerInfoTool_Failure (0.00s)
=== RUN   TestGetActiveConnectionsTool_WithConnections
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_active_connections ms=0
--- PASS: TestGetActiveConnectionsTool_WithConnections (0.00s)
=== RUN   TestGetActiveConnectionsTool_NoConnections
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_active_connections ms=0
--- PASS: TestGetActiveConnectionsTool_NoConnections (0.00s)
=== RUN   TestGetActiveConnectionsTool_EmptyOutput
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_active_connections ms=0
--- PASS: TestGetActiveConnectionsTool_EmptyOutput (0.00s)
=== RUN   TestGetActiveConnectionsTool_IdleIncludedByDefault
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_active_connections ms=0
--- PASS: TestGetActiveConnectionsTool_IdleIncludedByDefault (0.00s)
=== RUN   TestGetActiveConnectionsTool_ActiveOnly
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_active_connections ms=0
--- PASS: TestGetActiveConnectionsTool_ActiveOnly (0.00s)
=== RUN   TestGetLockInfoTool_WithLocks
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_lock_info ms=0
--- PASS: TestGetLockInfoTool_WithLocks (0.00s)
=== RUN   TestGetLockInfoTool_NoLocks
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_lock_info ms=0
--- PASS: TestGetLockInfoTool_NoLocks (0.00s)
=== RUN   TestGetLockInfoTool_EmptyOutput
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_lock_info ms=0
--- PASS: TestGetLockInfoTool_EmptyOutput (0.00s)
=== RUN   TestGetTableStatsTool_WithTableName
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_table_stats ms=0
--- PASS: TestGetTableStatsTool_WithTableName (0.00s)
=== RUN   TestGetTableStatsTool_CustomSchema
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_table_stats ms=0
--- PASS: TestGetTableStatsTool_CustomSchema (0.00s)
=== RUN   TestGetTableStatsTool_DefaultSchema
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_table_stats ms=0
--- PASS: TestGetTableStatsTool_DefaultSchema (0.00s)
=== RUN   TestGetDatabaseInfoTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_database_info ms=0
--- PASS: TestGetDatabaseInfoTool_Success (0.00s)
=== RUN   TestGetConnectionStatsTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_connection_stats ms=0
--- PASS: TestGetConnectionStatsTool_Success (0.00s)
=== RUN   TestGetDatabaseStatsTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_database_stats ms=0
--- PASS: TestGetDatabaseStatsTool_Success (0.00s)
=== RUN   TestGetConfigParameterTool_SpecificParameter
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_config_parameter ms=0
--- PASS: TestGetConfigParameterTool_SpecificParameter (0.00s)
=== RUN   TestGetConfigParameterTool_DefaultParameters
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_config_parameter ms=0
--- PASS: TestGetConfigParameterTool_DefaultParameters (0.00s)
=== RUN   TestGetReplicationStatusTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_replication_status ms=0
--- PASS: TestGetReplicationStatusTool_Success (0.00s)
=== RUN   TestToolsErrorHandling
=== RUN   TestToolsErrorHandling/getServerInfoTool
2026/05/21 19:59:05 WARN psql command failed tool=get_server_info ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getDatabaseInfoTool
2026/05/21 19:59:05 WARN psql command failed tool=get_database_info ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getActiveConnectionsTool
2026/05/21 19:59:05 WARN psql command failed tool=get_active_connections ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getConnectionStatsTool
2026/05/21 19:59:05 WARN psql command failed tool=get_connection_stats ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getDatabaseStatsTool
2026/05/21 19:59:05 WARN psql command failed tool=get_database_stats ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getConfigParameterTool
2026/05/21 19:59:05 WARN psql command failed tool=get_config_parameter ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getReplicationStatusTool
2026/05/21 19:59:05 WARN psql command failed tool=get_replication_status ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getLockInfoTool
2026/05/21 19:59:05 WARN psql command failed tool=get_lock_info ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getTableStatsTool
2026/05/21 19:59:05 WARN psql command failed tool=get_table_stats ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getBgwriterStatsTool
2026/05/21 19:59:05 WARN psql command failed tool="" ms=0 err="exit status 1" output="connection refused"
--- PASS: TestToolsErrorHandling (0.00s)
    --- PASS: TestToolsErrorHandling/getServerInfoTool (0.00s)
    --- PASS: TestToolsErrorHandling/getDatabaseInfoTool (0.00s)
    --- PASS: TestToolsErrorHandling/getActiveConnectionsTool (0.00s)
    --- PASS: TestToolsErrorHandling/getConnectionStatsTool (0.00s)
    --- PASS: TestToolsErrorHandling/getDatabaseStatsTool (0.00s)
    --- PASS: TestToolsErrorHandling/getConfigParameterTool (0.00s)
    --- PASS: TestToolsErrorHandling/getReplicationStatusTool (0.00s)
    --- PASS: TestToolsErrorHandling/getLockInfoTool (0.00s)
    --- PASS: TestToolsErrorHandling/getTableStatsTool (0.00s)
    --- PASS: TestToolsErrorHandling/getBgwriterStatsTool (0.00s)
=== RUN   TestGetStatusSummaryTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_status_summary ms=0
--- PASS: TestGetStatusSummaryTool_Success (0.00s)
=== RUN   TestGetStatusSummaryTool_PsqlError
2026/05/21 19:59:05 WARN psql command failed tool=get_status_summary ms=0 err="exit status 1" output="(no output from psql)"
--- PASS: TestGetStatusSummaryTool_PsqlError (0.00s)
=== RUN   TestGetStatusSummaryTool_MalformedOutput
2026/05/21 19:59:05 INFO tool ok name=get_status_summary ms=0
--- PASS: TestGetStatusSummaryTool_MalformedOutput (0.00s)
=== RUN   TestParseTerminatedCount
=== RUN   TestParseTerminatedCount/single_row_terminated
=== RUN   TestParseTerminatedCount/zero_terminated
=== RUN   TestParseTerminatedCount/large_count
=== RUN   TestParseTerminatedCount/no_terminated_field
=== RUN   TestParseTerminatedCount/empty_output
=== RUN   TestParseTerminatedCount/unrelated_pipe_line
--- PASS: TestParseTerminatedCount (0.00s)
    --- PASS: TestParseTerminatedCount/single_row_terminated (0.00s)
    --- PASS: TestParseTerminatedCount/zero_terminated (0.00s)
    --- PASS: TestParseTerminatedCount/large_count (0.00s)
    --- PASS: TestParseTerminatedCount/no_terminated_field (0.00s)
    --- PASS: TestParseTerminatedCount/empty_output (0.00s)
    --- PASS: TestParseTerminatedCount/unrelated_pipe_line (0.00s)
=== RUN   TestParsePgFunctionResult
=== RUN   TestParsePgFunctionResult/cancelled_true
=== RUN   TestParsePgFunctionResult/cancelled_false
=== RUN   TestParsePgFunctionResult/terminated_true
=== RUN   TestParsePgFunctionResult/terminated_false
=== RUN   TestParsePgFunctionResult/no_relevant_column
=== RUN   TestParsePgFunctionResult/empty_output
--- PASS: TestParsePgFunctionResult (0.00s)
    --- PASS: TestParsePgFunctionResult/cancelled_true (0.00s)
    --- PASS: TestParsePgFunctionResult/cancelled_false (0.00s)
    --- PASS: TestParsePgFunctionResult/terminated_true (0.00s)
    --- PASS: TestParsePgFunctionResult/terminated_false (0.00s)
    --- PASS: TestParsePgFunctionResult/no_relevant_column (0.00s)
    --- PASS: TestParsePgFunctionResult/empty_output (0.00s)
=== RUN   TestParseExpandedRow
=== RUN   TestParseExpandedRow/simple_record
=== RUN   TestParseExpandedRow/boolean_fields
=== RUN   TestParseExpandedRow/empty_value
=== RUN   TestParseExpandedRow/empty_input
--- PASS: TestParseExpandedRow (0.00s)
    --- PASS: TestParseExpandedRow/simple_record (0.00s)
    --- PASS: TestParseExpandedRow/boolean_fields (0.00s)
    --- PASS: TestParseExpandedRow/empty_value (0.00s)
    --- PASS: TestParseExpandedRow/empty_input (0.00s)
=== RUN   TestParseConnectionPlan
=== RUN   TestParseConnectionPlan/idle_session_no_tx
=== RUN   TestParseConnectionPlan/write_tx_with_locks
=== RUN   TestParseConnectionPlan/short_write_tx_rollback_minimum
--- PASS: TestParseConnectionPlan (0.00s)
    --- PASS: TestParseConnectionPlan/idle_session_no_tx (0.00s)
    --- PASS: TestParseConnectionPlan/write_tx_with_locks (0.00s)
    --- PASS: TestParseConnectionPlan/short_write_tx_rollback_minimum (0.00s)
=== RUN   TestFormatDuration
--- PASS: TestFormatDuration (0.00s)
=== RUN   TestFormatConnectionPlan
=== RUN   TestFormatConnectionPlan/no_open_transaction
=== RUN   TestFormatConnectionPlan/read-only_transaction
=== RUN   TestFormatConnectionPlan/write_transaction_with_estimate
--- PASS: TestFormatConnectionPlan (0.00s)
    --- PASS: TestFormatConnectionPlan/no_open_transaction (0.00s)
    --- PASS: TestFormatConnectionPlan/read-only_transaction (0.00s)
    --- PASS: TestFormatConnectionPlan/write_transaction_with_estimate (0.00s)
=== RUN   TestGetSessionInfoTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
--- PASS: TestGetSessionInfoTool_Success (0.00s)
=== RUN   TestGetSessionInfoTool_NoPidFound
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
--- PASS: TestGetSessionInfoTool_NoPidFound (0.00s)
=== RUN   TestCancelQueryTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=cancel_query ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_Success (0.00s)
=== RUN   TestCancelQueryTool_NoPidFound
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
--- PASS: TestCancelQueryTool_NoPidFound (0.00s)
=== RUN   TestCancelQueryTool_Failure
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=get_session_info ms=0 err="exit status 1" output="connection refused"
--- PASS: TestCancelQueryTool_Failure (0.00s)
=== RUN   TestCancelQueryTool_Level1_ReturnedFalse
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=cancel_query ms=0
--- PASS: TestCancelQueryTool_Level1_ReturnedFalse (0.00s)
=== RUN   TestCancelQueryTool_Level2_StillActive
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=cancel_query ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_Level2_StillActive (0.00s)
=== RUN   TestCancelQueryTool_Level2_ResolvesOnRetry
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=cancel_query ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_Level2_ResolvesOnRetry (0.00s)
=== RUN   TestCancelQueryTool_Level2_ExhaustedWarning
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=cancel_query ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_Level2_ExhaustedWarning (0.00s)
=== RUN   TestCancelQueryTool_IdleInTransaction_FalsePositive
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=cancel_query ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_IdleInTransaction_FalsePositive (0.00s)
=== RUN   TestTerminateConnectionTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=terminate_connection ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestTerminateConnectionTool_Success (0.00s)
=== RUN   TestTerminateConnectionTool_NoPidFound
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
--- PASS: TestTerminateConnectionTool_NoPidFound (0.00s)
=== RUN   TestTerminateConnectionTool_Failure
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=get_session_info ms=0 err="exit status 1" output="connection refused"
--- PASS: TestTerminateConnectionTool_Failure (0.00s)
=== RUN   TestTerminateConnectionTool_Level1_ReturnedFalse
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=terminate_connection ms=0
--- PASS: TestTerminateConnectionTool_Level1_ReturnedFalse (0.00s)
=== RUN   TestTerminateConnectionTool_Level2_PidStillAlive
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=terminate_connection ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestTerminateConnectionTool_Level2_PidStillAlive (0.00s)
=== RUN   TestTerminateConnectionTool_Level2_ResolvesOnRetry
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=terminate_connection ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestTerminateConnectionTool_Level2_ResolvesOnRetry (0.00s)
=== RUN   TestTerminateConnectionTool_Level2_EscalationRequired
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=terminate_connection ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestTerminateConnectionTool_Level2_EscalationRequired (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_TooShortIdle
--- PASS: TestTerminateIdleConnectionsTool_TooShortIdle (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_DryRun_Found
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=terminate_idle_connections ms=0
--- PASS: TestTerminateIdleConnectionsTool_DryRun_Found (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_DryRun_NoneFound
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=terminate_idle_connections ms=0
--- PASS: TestTerminateIdleConnectionsTool_DryRun_NoneFound (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=terminate_idle_connections ms=0
--- PASS: TestTerminateIdleConnectionsTool_Success (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_WithDatabaseFilter
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=terminate_idle_connections ms=0
--- PASS: TestTerminateIdleConnectionsTool_WithDatabaseFilter (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_Failure
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=terminate_idle_connections ms=0 err="exit status 1" output="connection refused"
--- PASS: TestTerminateIdleConnectionsTool_Failure (0.00s)
=== RUN   TestCancelQueryTool_PolicyDenied
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestCancelQueryTool_PolicyDenied2979602635/001/db-policies-3451127816.yaml policies=1 dry_run=false default=allow
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN policy decision: DENY action=write resource_type=database resource_name="" effect=deny policy=deny-write message="write operations are not permitted in this test"
2026/05/21 19:59:05 WARN policy denied database access tool=cancel_query database="" action=write tags=[] from_infra_config=false err="Access to database  for write: DENIED\n\nPolicy \"deny-write\" matched:\n  Rule 0   write → deny                  matched\n  → DENIED\n\nReason: write operations are not permitted in this test"
--- PASS: TestCancelQueryTool_PolicyDenied (0.00s)
=== RUN   TestTerminateConnectionTool_PolicyDenied
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateConnectionTool_PolicyDenied2687971134/001/db-policies-1566561419.yaml policies=1 dry_run=false default=allow
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=deny-destructive message="destructive operations are not permitted in this test"
2026/05/21 19:59:05 WARN policy denied database access tool=terminate_connection database="" action=destructive tags=[] from_infra_config=false err="Access to database  for destructive: DENIED\n\nPolicy \"deny-destructive\" matched:\n  Rule 0   destructive → deny            matched\n  → DENIED\n\nReason: destructive operations are not permitted in this test"
--- PASS: TestTerminateConnectionTool_PolicyDenied (0.00s)
=== RUN   TestCancelQueryTool_PostExecPolicyChecked
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestCancelQueryTool_PostExecPolicyChecked2835273044/001/db-policies-3822003135.yaml policies=1 dry_run=false default=allow
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=cancel_query ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_PostExecPolicyChecked (0.00s)
=== RUN   TestTerminateConnectionTool_PostExecPolicyChecked
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateConnectionTool_PostExecPolicyChecked2455189858/001/db-policies-1039165258.yaml policies=1 dry_run=false default=allow
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=terminate_connection ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestTerminateConnectionTool_PostExecPolicyChecked (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_BlastRadiusDenied
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateIdleConnectionsTool_BlastRadiusDenied1134472881/001/db-policies-3510426695.yaml policies=1 dry_run=false default=deny
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=terminate_idle_connections ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=db-blast-radius message="Operation affects 20 rows, limit is 5"
2026/05/21 19:59:05 WARN post-execution policy check: blast radius exceeded resource_type=database resource_name="" action=destructive rows_affected=20 pods_affected=0 policy=db-blast-radius message="Operation affects 20 rows, limit is 5"
--- PASS: TestTerminateIdleConnectionsTool_BlastRadiusDenied (0.00s)
=== RUN   TestResolveDatabaseInfo_InfraEnforced_UnknownConnString
--- PASS: TestResolveDatabaseInfo_InfraEnforced_UnknownConnString (0.00s)
=== RUN   TestResolveDatabaseInfo_InfraEnforced_UnknownName
--- PASS: TestResolveDatabaseInfo_InfraEnforced_UnknownName (0.00s)
=== RUN   TestResolveDatabaseInfo_InfraEnforced_RegisteredConnString
--- PASS: TestResolveDatabaseInfo_InfraEnforced_RegisteredConnString (0.00s)
=== RUN   TestResolveDatabaseInfo_InfraPermissive_UnknownConnString
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost dbname=devdb user=dev" known_databases=0
--- PASS: TestResolveDatabaseInfo_InfraPermissive_UnknownConnString (0.00s)
=== RUN   TestResolveDatabaseInfo_PasswordEnv_ByName
2026/05/21 19:59:05 INFO resolved database name to connection string name=secured-db
--- PASS: TestResolveDatabaseInfo_PasswordEnv_ByName (0.00s)
=== RUN   TestResolveDatabaseInfo_PasswordEnv_Missing
2026/05/21 19:59:05 INFO resolved database name to connection string name=secured-db
--- PASS: TestResolveDatabaseInfo_PasswordEnv_Missing (0.00s)
=== RUN   TestCheckConnectionTool_InfraEnforced_Rejected
--- PASS: TestCheckConnectionTool_InfraEnforced_Rejected (0.00s)
=== RUN   TestCancelQueryTool_SessionPlanSentToPolicy
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestCancelQueryTool_SessionPlanSentToPolicy1066876177/001/policy-2295968812.yaml policies=1 dry_run=false default=deny
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO policy decision: REQUIRE_APPROVAL action=write resource_type=database resource_name="" effect=require_approval policy=require-approval-policy
2026/05/21 19:59:05 INFO approval request created approval_id=tool-approval-1 resource=database:
2026/05/21 19:59:05 WARN policy denied database access tool=cancel_query database="" action=write tags=[] from_infra_config=false err="approval required (ID: tool-approval-1) — this operation needs human authorization before it can execute. Ask an approver to run: ./approvals approve tool-approval-1 — then reply here to retry."
--- PASS: TestCancelQueryTool_SessionPlanSentToPolicy (0.00s)
=== RUN   TestTerminateConnectionTool_SessionPlanSentToPolicy
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateConnectionTool_SessionPlanSentToPolicy3898175722/001/policy-313159417.yaml policies=1 dry_run=false default=deny
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO policy decision: REQUIRE_APPROVAL action=destructive resource_type=database resource_name="" effect=require_approval policy=require-approval-policy
2026/05/21 19:59:05 INFO approval request created approval_id=tool-approval-1 resource=database:
2026/05/21 19:59:05 WARN policy denied database access tool=terminate_connection database="" action=destructive tags=[] from_infra_config=false err="approval required (ID: tool-approval-1) — this operation needs human authorization before it can execute. Ask an approver to run: ./approvals approve tool-approval-1 — then reply here to retry."
--- PASS: TestTerminateConnectionTool_SessionPlanSentToPolicy (0.00s)
=== RUN   TestEstimateRowsAffected
=== RUN   TestEstimateRowsAffected/SELECT_is_skipped_without_calling_runner
=== RUN   TestEstimateRowsAffected/DELETE_returns_planner_estimate
=== RUN   TestEstimateRowsAffected/UPDATE_returns_planner_estimate
=== RUN   TestEstimateRowsAffected/psql_error_silently_skipped
=== RUN   TestEstimateRowsAffected/malformed_JSON_silently_skipped
--- PASS: TestEstimateRowsAffected (0.00s)
    --- PASS: TestEstimateRowsAffected/SELECT_is_skipped_without_calling_runner (0.00s)
    --- PASS: TestEstimateRowsAffected/DELETE_returns_planner_estimate (0.00s)
    --- PASS: TestEstimateRowsAffected/UPDATE_returns_planner_estimate (0.00s)
    --- PASS: TestEstimateRowsAffected/psql_error_silently_skipped (0.00s)
    --- PASS: TestEstimateRowsAffected/malformed_JSON_silently_skipped (0.00s)
=== RUN   TestRunPsqlAs_PreExecBlastRadiusDenied
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestRunPsqlAs_PreExecBlastRadiusDenied3694183070/001/db-policies-1924167136.yaml policies=1 dry_run=false default=deny
2026/05/21 19:59:05 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=db-blast-radius message="Operation affects 50000 rows, limit is 1000"
2026/05/21 19:59:05 WARN post-execution policy check: blast radius exceeded resource_type=database resource_name="" action=destructive rows_affected=50000 pods_affected=0 policy=db-blast-radius message="Operation affects 50000 rows, limit is 1000"
--- PASS: TestRunPsqlAs_PreExecBlastRadiusDenied (0.00s)
=== RUN   TestTerminateConnectionTool_XactAgeGuardrail
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateConnectionTool_XactAgeGuardrail4052029655/001/db-policies-2962530304.yaml policies=1 dry_run=false default=allow
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=xact-age-limit message="Transaction has been open for 2h 0m; rollback may take as long. Limit is 30m 0s"
2026/05/21 19:59:05 WARN pre-execution policy check: transaction age exceeded resource_name="" action=destructive xact_age_secs=7200 policy=xact-age-limit message="Transaction has been open for 2h 0m; rollback may take as long. Limit is 30m 0s"
--- PASS: TestTerminateConnectionTool_XactAgeGuardrail (0.00s)
=== RUN   TestCancelQueryTool_XactAgeGuardrail
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestCancelQueryTool_XactAgeGuardrail3254186511/001/db-policies-657107280.yaml policies=1 dry_run=false default=allow
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN policy decision: DENY action=write resource_type=database resource_name="" effect=deny policy=xact-age-limit message="Transaction has been open for 2h 0m; rollback may take as long. Limit is 30m 0s"
2026/05/21 19:59:05 WARN pre-execution policy check: transaction age exceeded resource_name="" action=write xact_age_secs=7200 policy=xact-age-limit message="Transaction has been open for 2h 0m; rollback may take as long. Limit is 30m 0s"
--- PASS: TestCancelQueryTool_XactAgeGuardrail (0.00s)
=== RUN   TestTerminateConnectionTool_ToolNamePolicyDenied
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateConnectionTool_ToolNamePolicyDenied1677395757/001/db-policies-1790304131.yaml policies=1 dry_run=false default=allow
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=deny-tool-terminate_connection message="tool terminate_connection is disabled by policy"
2026/05/21 19:59:05 WARN policy denied database access tool=terminate_connection database="" action=destructive tags=[] from_infra_config=false err="Access to database  for destructive: DENIED\n\nPolicy \"deny-tool-terminate_connection\" matched:\n  Rule 0   read|write|destructive → deny  matched\n  → DENIED\n\nReason: tool terminate_connection is disabled by policy"
--- PASS: TestTerminateConnectionTool_ToolNamePolicyDenied (0.00s)
=== RUN   TestCancelQueryTool_ToolNamePolicyAllows_WhenOtherToolDenied
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestCancelQueryTool_ToolNamePolicyAllows_WhenOtherToolDenied1412764305/001/db-policies-4018020039.yaml policies=1 dry_run=false default=allow
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_session_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=cancel_query ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_ToolNamePolicyAllows_WhenOtherToolDenied (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_ToolPatternPolicyDenied
2026/05/21 19:59:05 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateIdleConnectionsTool_ToolPatternPolicyDenied1197287611/001/db-policies-1956559468.yaml policies=1 dry_run=false default=allow
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=deny-tool-pattern message="tool matching terminate_* is disabled by policy"
2026/05/21 19:59:05 WARN policy denied database access tool=terminate_idle_connections database="" action=destructive tags=[] from_infra_config=false err="Access to database  for destructive: DENIED\n\nPolicy \"deny-tool-pattern\" matched:\n  Rule 0   read|write|destructive → deny  matched\n  → DENIED\n\nReason: tool matching terminate_* is disabled by policy"
--- PASS: TestTerminateIdleConnectionsTool_ToolPatternPolicyDenied (0.00s)
=== RUN   TestGetPgSettingsTool_NonDefaultSettings
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_pg_settings ms=0
--- PASS: TestGetPgSettingsTool_NonDefaultSettings (0.00s)
=== RUN   TestGetPgSettingsTool_AllDefaultsReturnsMessage
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_pg_settings ms=0
--- PASS: TestGetPgSettingsTool_AllDefaultsReturnsMessage (0.00s)
=== RUN   TestGetPgSettingsTool_CategoryFilter
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_pg_settings ms=0
--- PASS: TestGetPgSettingsTool_CategoryFilter (0.00s)
=== RUN   TestGetExtensionsTool_WithExtensions
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_extensions ms=0
--- PASS: TestGetExtensionsTool_WithExtensions (0.00s)
=== RUN   TestGetExtensionsTool_NoExtensions
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_extensions ms=0
--- PASS: TestGetExtensionsTool_NoExtensions (0.00s)
=== RUN   TestGetBaselineTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_server_info ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_pg_settings ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_extensions ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_disk_usage ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_disk_usage ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestGetBaselineTool_Success (0.00s)
=== RUN   TestGetBaselineTool_PartialFailure
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=get_server_info ms=0 err="connection refused" output="(no output from psql)"
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=get_pg_settings ms=0 err="connection refused" output="(no output from psql)"
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=get_extensions ms=0 err="connection refused" output="(no output from psql)"
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=get_disk_usage ms=0 err="connection refused" output="(no output from psql)"
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestGetBaselineTool_PartialFailure (0.00s)
=== RUN   TestGetSlowQueriesTool_WithResults
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_slow_queries ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_slow_queries ms=0
--- PASS: TestGetSlowQueriesTool_WithResults (0.00s)
=== RUN   TestGetSlowQueriesTool_ExtensionMissing
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_slow_queries ms=0
--- PASS: TestGetSlowQueriesTool_ExtensionMissing (0.00s)
=== RUN   TestGetSlowQueriesTool_NoResults
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_slow_queries ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_slow_queries ms=0
--- PASS: TestGetSlowQueriesTool_NoResults (0.00s)
=== RUN   TestGetVacuumStatusTool_WithResults
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_vacuum_status ms=0
--- PASS: TestGetVacuumStatusTool_WithResults (0.00s)
=== RUN   TestGetVacuumStatusTool_NoResults
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_vacuum_status ms=0
--- PASS: TestGetVacuumStatusTool_NoResults (0.00s)
=== RUN   TestGetDiskUsageTool_WithResults
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_disk_usage ms=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_disk_usage ms=0
--- PASS: TestGetDiskUsageTool_WithResults (0.00s)
=== RUN   TestGetWaitEventsTool_WithEvents
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_wait_events ms=0
--- PASS: TestGetWaitEventsTool_WithEvents (0.00s)
=== RUN   TestGetWaitEventsTool_NoWaits
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_wait_events ms=0
--- PASS: TestGetWaitEventsTool_NoWaits (0.00s)
=== RUN   TestGetBgwriterStatsTool_Success
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_bgwriter_stats ms=0
--- PASS: TestGetBgwriterStatsTool_Success (0.00s)
=== RUN   TestGetBlockingQueriesTool_WithBlocks
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_blocking_queries ms=0
--- PASS: TestGetBlockingQueriesTool_WithBlocks (0.00s)
=== RUN   TestGetBlockingQueriesTool_NoBlocking
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=get_blocking_queries ms=0
--- PASS: TestGetBlockingQueriesTool_NoBlocking (0.00s)
=== RUN   TestExplainQueryTool_SelectQuery
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=explain_query ms=0
--- PASS: TestExplainQueryTool_SelectQuery (0.00s)
=== RUN   TestExplainQueryTool_DMLRejectedByDefault
--- PASS: TestExplainQueryTool_DMLRejectedByDefault (0.00s)
=== RUN   TestExplainQueryTool_DMLAllowedWithFlag
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=explain_query ms=0
--- PASS: TestExplainQueryTool_DMLAllowedWithFlag (0.00s)
=== RUN   TestExplainQueryTool_EmptyQuery
--- PASS: TestExplainQueryTool_EmptyQuery (0.00s)
=== RUN   TestExplainQueryTool_DMLWrappedInTransaction
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=explain_query ms=0
--- PASS: TestExplainQueryTool_DMLWrappedInTransaction (0.00s)
=== RUN   TestReadUploadedFileTool_Success
--- PASS: TestReadUploadedFileTool_Success (0.00s)
=== RUN   TestReadUploadedFileTool_WithFilter
--- PASS: TestReadUploadedFileTool_WithFilter (0.00s)
=== RUN   TestReadUploadedFileTool_NotFound
--- PASS: TestReadUploadedFileTool_NotFound (0.00s)
=== RUN   TestReadUploadedFileTool_NoAuditURL
--- PASS: TestReadUploadedFileTool_NoAuditURL (0.00s)
=== RUN   TestReadUploadedFileTool_EmptyUploadID
--- PASS: TestReadUploadedFileTool_EmptyUploadID (0.00s)
=== RUN   TestReadUploadedFileTool_FilterNoMatch
--- PASS: TestReadUploadedFileTool_FilterNoMatch (0.00s)
=== RUN   TestGetPgLogTool_ReturnsLastNLines
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=read_pg_log ms=0
--- PASS: TestGetPgLogTool_ReturnsLastNLines (0.00s)
=== RUN   TestGetPgLogTool_WithFilter
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 INFO tool ok name=read_pg_log ms=0
--- PASS: TestGetPgLogTool_WithFilter (0.00s)
=== RUN   TestGetPgLogTool_FilterCaseInsensitive
2026/05/21 19:59:05 INFO tool ok name=read_pg_log ms=0
--- PASS: TestGetPgLogTool_FilterCaseInsensitive (0.00s)
=== RUN   TestGetPgLogTool_EmptyLog
2026/05/21 19:59:05 INFO tool ok name=read_pg_log ms=0
--- PASS: TestGetPgLogTool_EmptyLog (0.00s)
=== RUN   TestGetPgLogTool_FilterNoMatch
2026/05/21 19:59:05 INFO tool ok name=read_pg_log ms=0
--- PASS: TestGetPgLogTool_FilterNoMatch (0.00s)
=== RUN   TestGetPgLogTool_ConnectionError
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=read_pg_log ms=0 err="exit status 1" output="connection refused"
--- PASS: TestGetPgLogTool_ConnectionError (0.00s)
=== RUN   TestGetPgLogTool_PermissionDeniedPgLsLogdir
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=pg-cluster-minkube" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=read_pg_log ms=0 err="ERROR:  permission denied for function pg_ls_logdir\nexit status 1" output="(no output from psql)"
--- PASS: TestGetPgLogTool_PermissionDeniedPgLsLogdir (0.00s)
=== RUN   TestGetPgLogTool_LoggingCollectorDisabled
2026/05/21 19:59:05 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/21 19:59:05 WARN psql command failed tool=read_pg_log ms=0 err="ERROR:  could not open directory \"log\": No such file or directory\nexit status 1" output="(no output from psql)"
--- PASS: TestGetPgLogTool_LoggingCollectorDisabled (0.00s)
=== RUN   TestRunPsqlTuples_UsesTupleFlags
2026/05/21 19:59:05 INFO tool ok name=test_tool ms=0
--- PASS: TestRunPsqlTuples_UsesTupleFlags (0.00s)
=== RUN   TestGetSavedSnapshots_Success
--- PASS: TestGetSavedSnapshots_Success (0.00s)
=== RUN   TestGetSavedSnapshots_MultipleSnapshots
--- PASS: TestGetSavedSnapshots_MultipleSnapshots (0.00s)
=== RUN   TestGetSavedSnapshots_Empty
--- PASS: TestGetSavedSnapshots_Empty (0.00s)
=== RUN   TestGetSavedSnapshots_NoAuditURL
--- PASS: TestGetSavedSnapshots_NoAuditURL (0.00s)
=== RUN   TestGetSavedSnapshots_NoToolName
--- PASS: TestGetSavedSnapshots_NoToolName (0.00s)
=== RUN   TestGetSavedSnapshots_Truncation
--- PASS: TestGetSavedSnapshots_Truncation (0.00s)
PASS
ok  	helpdesk/agents/database	41.321s
=== RUN   TestDiagnoseClientError_Nil
--- PASS: TestDiagnoseClientError_Nil (0.00s)
=== RUN   TestDiagnoseClientError_ContextNotExist
--- PASS: TestDiagnoseClientError_ContextNotExist (0.00s)
=== RUN   TestDiagnoseClientError_ConnectionRefused
--- PASS: TestDiagnoseClientError_ConnectionRefused (0.00s)
=== RUN   TestDiagnoseClientError_NetOpError
--- PASS: TestDiagnoseClientError_NetOpError (0.00s)
=== RUN   TestDiagnoseClientError_UnableToConnect
--- PASS: TestDiagnoseClientError_UnableToConnect (0.00s)
=== RUN   TestDiagnoseClientError_Timeout
--- PASS: TestDiagnoseClientError_Timeout (0.00s)
=== RUN   TestDiagnoseClientError_Certificate
--- PASS: TestDiagnoseClientError_Certificate (0.00s)
=== RUN   TestDiagnoseClientError_NoConfig
--- PASS: TestDiagnoseClientError_NoConfig (0.00s)
=== RUN   TestDiagnoseClientError_UnknownPassthrough
--- PASS: TestDiagnoseClientError_UnknownPassthrough (0.00s)
=== RUN   TestFormatAge
--- PASS: TestFormatAge (0.00s)
=== RUN   TestPodReadyString
=== RUN   TestPodReadyString/all_ready
=== RUN   TestPodReadyString/some_ready
=== RUN   TestPodReadyString/none_ready
=== RUN   TestPodReadyString/empty
--- PASS: TestPodReadyString (0.00s)
    --- PASS: TestPodReadyString/all_ready (0.00s)
    --- PASS: TestPodReadyString/some_ready (0.00s)
    --- PASS: TestPodReadyString/none_ready (0.00s)
    --- PASS: TestPodReadyString/empty (0.00s)
=== RUN   TestTotalRestarts
=== RUN   TestTotalRestarts/zero
=== RUN   TestTotalRestarts/mixed
=== RUN   TestTotalRestarts/empty
--- PASS: TestTotalRestarts (0.00s)
    --- PASS: TestTotalRestarts/zero (0.00s)
    --- PASS: TestTotalRestarts/mixed (0.00s)
    --- PASS: TestTotalRestarts/empty (0.00s)
=== RUN   TestLastTerminatedState
=== RUN   TestLastTerminatedState/no_last_state
=== RUN   TestLastTerminatedState/OOMKilled
=== RUN   TestLastTerminatedState/Completed
=== RUN   TestLastTerminatedState/Error
--- PASS: TestLastTerminatedState (0.00s)
    --- PASS: TestLastTerminatedState/no_last_state (0.00s)
    --- PASS: TestLastTerminatedState/OOMKilled (0.00s)
    --- PASS: TestLastTerminatedState/Completed (0.00s)
    --- PASS: TestLastTerminatedState/Error (0.00s)
=== RUN   TestNodeRoles
=== RUN   TestNodeRoles/control-plane
=== RUN   TestNodeRoles/worker
=== RUN   TestNodeRoles/multiple_roles_sorted
=== RUN   TestNodeRoles/no_roles
=== RUN   TestNodeRoles/nil_labels
--- PASS: TestNodeRoles (0.00s)
    --- PASS: TestNodeRoles/control-plane (0.00s)
    --- PASS: TestNodeRoles/worker (0.00s)
    --- PASS: TestNodeRoles/multiple_roles_sorted (0.00s)
    --- PASS: TestNodeRoles/no_roles (0.00s)
    --- PASS: TestNodeRoles/nil_labels (0.00s)
=== RUN   TestNodeStatus
=== RUN   TestNodeStatus/ready
=== RUN   TestNodeStatus/not_ready
=== RUN   TestNodeStatus/ready_+_unschedulable
=== RUN   TestNodeStatus/unknown_(no_conditions)
--- PASS: TestNodeStatus (0.00s)
    --- PASS: TestNodeStatus/ready (0.00s)
    --- PASS: TestNodeStatus/not_ready (0.00s)
    --- PASS: TestNodeStatus/ready_+_unschedulable (0.00s)
    --- PASS: TestNodeStatus/unknown_(no_conditions) (0.00s)
=== RUN   TestExternalAddresses
=== RUN   TestExternalAddresses/loadbalancer_with_IP
=== RUN   TestExternalAddresses/loadbalancer_with_hostname
=== RUN   TestExternalAddresses/external_IPs_in_spec
=== RUN   TestExternalAddresses/no_external
--- PASS: TestExternalAddresses (0.00s)
    --- PASS: TestExternalAddresses/loadbalancer_with_IP (0.00s)
    --- PASS: TestExternalAddresses/loadbalancer_with_hostname (0.00s)
    --- PASS: TestExternalAddresses/external_IPs_in_spec (0.00s)
    --- PASS: TestExternalAddresses/no_external (0.00s)
=== RUN   TestEventTimestamp
=== RUN   TestEventTimestamp/prefers_LastTimestamp
=== RUN   TestEventTimestamp/falls_back_to_EventTime
=== RUN   TestEventTimestamp/falls_back_to_CreationTimestamp
--- PASS: TestEventTimestamp (0.00s)
    --- PASS: TestEventTimestamp/prefers_LastTimestamp (0.00s)
    --- PASS: TestEventTimestamp/falls_back_to_EventTime (0.00s)
    --- PASS: TestEventTimestamp/falls_back_to_CreationTimestamp (0.00s)
=== RUN   TestDiagnoseClientError_NetOpErrorConnectionRefused
--- PASS: TestDiagnoseClientError_NetOpErrorConnectionRefused (0.00s)
=== RUN   TestK8sDirectRegistry_AllToolsRegistered
--- PASS: TestK8sDirectRegistry_AllToolsRegistered (0.00s)
=== RUN   TestK8sArgsToStruct_RoundTrip
--- PASS: TestK8sArgsToStruct_RoundTrip (0.00s)
=== RUN   TestK8sArgsToStruct_EmptyArgs
--- PASS: TestK8sArgsToStruct_EmptyArgs (0.00s)
=== RUN   TestK8sDirectRegistry_ToolCallable
2026/05/21 19:58:27 INFO tool ok name=describe_service ms=0
--- PASS: TestK8sDirectRegistry_ToolCallable (0.00s)
=== RUN   TestDiagnoseKubectlError
=== RUN   TestDiagnoseKubectlError/context_does_not_exist
=== RUN   TestDiagnoseKubectlError/connection_refused
=== RUN   TestDiagnoseKubectlError/unable_to_connect
=== RUN   TestDiagnoseKubectlError/unauthorized
=== RUN   TestDiagnoseKubectlError/forbidden
=== RUN   TestDiagnoseKubectlError/namespace_not_found
=== RUN   TestDiagnoseKubectlError/resource_not_found
=== RUN   TestDiagnoseKubectlError/kubectl_not_installed
=== RUN   TestDiagnoseKubectlError/command_not_found
=== RUN   TestDiagnoseKubectlError/invalid_configuration
=== RUN   TestDiagnoseKubectlError/i/o_timeout
=== RUN   TestDiagnoseKubectlError/deadline_exceeded
=== RUN   TestDiagnoseKubectlError/certificate_expired
=== RUN   TestDiagnoseKubectlError/certificate_unknown_authority
=== RUN   TestDiagnoseKubectlError/unknown_error_returns_empty
=== RUN   TestDiagnoseKubectlError/empty_output_returns_empty
--- PASS: TestDiagnoseKubectlError (0.00s)
    --- PASS: TestDiagnoseKubectlError/context_does_not_exist (0.00s)
    --- PASS: TestDiagnoseKubectlError/connection_refused (0.00s)
    --- PASS: TestDiagnoseKubectlError/unable_to_connect (0.00s)
    --- PASS: TestDiagnoseKubectlError/unauthorized (0.00s)
    --- PASS: TestDiagnoseKubectlError/forbidden (0.00s)
    --- PASS: TestDiagnoseKubectlError/namespace_not_found (0.00s)
    --- PASS: TestDiagnoseKubectlError/resource_not_found (0.00s)
    --- PASS: TestDiagnoseKubectlError/kubectl_not_installed (0.00s)
    --- PASS: TestDiagnoseKubectlError/command_not_found (0.00s)
    --- PASS: TestDiagnoseKubectlError/invalid_configuration (0.00s)
    --- PASS: TestDiagnoseKubectlError/i/o_timeout (0.00s)
    --- PASS: TestDiagnoseKubectlError/deadline_exceeded (0.00s)
    --- PASS: TestDiagnoseKubectlError/certificate_expired (0.00s)
    --- PASS: TestDiagnoseKubectlError/certificate_unknown_authority (0.00s)
    --- PASS: TestDiagnoseKubectlError/unknown_error_returns_empty (0.00s)
    --- PASS: TestDiagnoseKubectlError/empty_output_returns_empty (0.00s)
=== RUN   TestParsePodsAffected
=== RUN   TestParsePodsAffected/single_pod_deleted
=== RUN   TestParsePodsAffected/multiple_pods_deleted
=== RUN   TestParsePodsAffected/deployment_configured
=== RUN   TestParsePodsAffected/resource_created
=== RUN   TestParsePodsAffected/mixed_actions
=== RUN   TestParsePodsAffected/read-only_output_(no_mutations)
=== RUN   TestParsePodsAffected/empty_output
=== RUN   TestParsePodsAffected/error_output
=== RUN   TestParsePodsAffected/deployment_restarted
=== RUN   TestParsePodsAffected/deployment_scaled
--- PASS: TestParsePodsAffected (0.00s)
    --- PASS: TestParsePodsAffected/single_pod_deleted (0.00s)
    --- PASS: TestParsePodsAffected/multiple_pods_deleted (0.00s)
    --- PASS: TestParsePodsAffected/deployment_configured (0.00s)
    --- PASS: TestParsePodsAffected/resource_created (0.00s)
    --- PASS: TestParsePodsAffected/mixed_actions (0.00s)
    --- PASS: TestParsePodsAffected/read-only_output_(no_mutations) (0.00s)
    --- PASS: TestParsePodsAffected/empty_output (0.00s)
    --- PASS: TestParsePodsAffected/error_output (0.00s)
    --- PASS: TestParsePodsAffected/deployment_restarted (0.00s)
    --- PASS: TestParsePodsAffected/deployment_scaled (0.00s)
=== RUN   TestDeletePodTool_Success
2026/05/21 19:58:27 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_Success (0.00s)
=== RUN   TestDeletePodTool_WithGracePeriod
2026/05/21 19:58:27 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_WithGracePeriod (0.00s)
=== RUN   TestDeletePodTool_Failure
2026/05/21 19:58:27 ERROR kubectl command failed tool=delete_pod args="[delete pod bad-pod -n default]" ms=0 err="Error from server (NotFound): pods \"bad-pod\" not found"
--- PASS: TestDeletePodTool_Failure (0.00s)
=== RUN   TestDeletePodTool_VerificationWarning_PodStillTerminating
2026/05/21 19:58:27 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_VerificationWarning_PodStillTerminating (0.00s)
=== RUN   TestDeletePodTool_PolicyDenied
2026/05/21 19:58:27 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestDeletePodTool_PolicyDenied2220001487/001/k8s-policies-3577362081.yaml policies=1 dry_run=false default=allow
2026/05/21 19:58:27 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=production effect=deny policy=deny-k8s-destructive message="destructive kubernetes operations are not permitted in this test"
--- PASS: TestDeletePodTool_PolicyDenied (0.00s)
=== RUN   TestRestartDeploymentTool_Success
2026/05/21 19:58:27 INFO tool ok name=restart_deployment ms=0
--- PASS: TestRestartDeploymentTool_Success (0.00s)
=== RUN   TestRestartDeploymentTool_Failure
2026/05/21 19:58:27 ERROR kubectl command failed tool=restart_deployment args="[rollout restart deployment missing -n default]" ms=0 err="Error from server (NotFound): deployments \"missing\" not found"
--- PASS: TestRestartDeploymentTool_Failure (0.00s)
=== RUN   TestRestartDeploymentTool_VerificationWarning_AnnotationMissing
2026/05/21 19:58:27 INFO tool ok name=restart_deployment ms=0
--- PASS: TestRestartDeploymentTool_VerificationWarning_AnnotationMissing (0.00s)
=== RUN   TestRestartDeploymentTool_PolicyDenied
2026/05/21 19:58:27 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestRestartDeploymentTool_PolicyDenied1321018151/001/k8s-policies-729949993.yaml policies=1 dry_run=false default=allow
2026/05/21 19:58:27 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=production effect=deny policy=deny-k8s-destructive message="destructive kubernetes operations are not permitted in this test"
--- PASS: TestRestartDeploymentTool_PolicyDenied (0.00s)
=== RUN   TestScaleDeploymentTool_Success
2026/05/21 19:58:27 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_Success (0.00s)
=== RUN   TestScaleDeploymentTool_ScaleToZero
2026/05/21 19:58:27 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_ScaleToZero (0.00s)
=== RUN   TestScaleDeploymentTool_CapturesPreState
2026/05/21 19:58:27 INFO sqlite journal mode mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestScaleDeploymentTool_CapturesPreState2414235139/001/k8s_prestate_test.db
2026/05/21 19:58:27 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_CapturesPreState (0.01s)
=== RUN   TestScaleDeploymentTool_PreStateReadFailure_ToolStillRuns
2026/05/21 19:58:27 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_PreStateReadFailure_ToolStillRuns (0.00s)
=== RUN   TestScaleDeploymentTool_Failure
2026/05/21 19:58:27 ERROR kubectl command failed tool=scale_deployment args="[scale deployment ghost --replicas 3 -n default]" ms=0 err="Error from server (NotFound): deployments \"ghost\" not found"
--- PASS: TestScaleDeploymentTool_Failure (0.00s)
=== RUN   TestScaleDeploymentTool_VerificationFailed_WrongReplicas
2026/05/21 19:58:27 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_VerificationFailed_WrongReplicas (0.00s)
=== RUN   TestScaleDeploymentTool_PolicyDenied
2026/05/21 19:58:27 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestScaleDeploymentTool_PolicyDenied3452506407/001/k8s-policies-2496746044.yaml policies=1 dry_run=false default=allow
2026/05/21 19:58:27 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=production effect=deny policy=deny-k8s-destructive message="destructive kubernetes operations are not permitted in this test"
--- PASS: TestScaleDeploymentTool_PolicyDenied (0.00s)
=== RUN   TestDeletePodTool_VerificationWarning_ResolvesOnRetry
2026/05/21 19:58:27 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_VerificationWarning_ResolvesOnRetry (0.00s)
=== RUN   TestDeletePodTool_VerificationWarning_ExhaustedEscalation
2026/05/21 19:58:27 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_VerificationWarning_ExhaustedEscalation (0.00s)
=== RUN   TestRestartDeploymentTool_VerificationWarning_ResolvesOnRetry
2026/05/21 19:58:27 INFO tool ok name=restart_deployment ms=0
--- PASS: TestRestartDeploymentTool_VerificationWarning_ResolvesOnRetry (0.00s)
=== RUN   TestScaleDeploymentTool_Level2_RetryApplySucceeds
2026/05/21 19:58:27 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_Level2_RetryApplySucceeds (0.00s)
=== RUN   TestScaleDeploymentTool_Level2_RetryApplyFails
2026/05/21 19:58:27 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_Level2_RetryApplyFails (0.00s)
=== RUN   TestDeletePodTool_BlastRadiusAllowed
2026/05/21 19:58:27 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestDeletePodTool_BlastRadiusAllowed1133296566/001/k8s-policies-2444749640.yaml policies=1 dry_run=false default=deny
2026/05/21 19:58:27 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_BlastRadiusAllowed (0.00s)
=== RUN   TestDeletePodTool_BlastRadiusDenied
2026/05/21 19:58:27 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestDeletePodTool_BlastRadiusDenied2069371067/001/k8s-policies-2141574104.yaml policies=1 dry_run=false default=deny
2026/05/21 19:58:27 INFO tool ok name=delete_pod ms=0
2026/05/21 19:58:27 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=default effect=deny policy=k8s-blast-radius message="Operation affects 3 pods, limit is 1"
2026/05/21 19:58:27 WARN post-execution policy check: blast radius exceeded resource_type=kubernetes resource_name=default action=destructive rows_affected=0 pods_affected=3 policy=k8s-blast-radius message="Operation affects 3 pods, limit is 1"
--- PASS: TestDeletePodTool_BlastRadiusDenied (0.00s)
=== RUN   TestScaleDeploymentTool_BlastRadiusDenied_PreExec
2026/05/21 19:58:27 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestScaleDeploymentTool_BlastRadiusDenied_PreExec987793418/001/k8s-policies-2031983886.yaml policies=1 dry_run=false default=deny
2026/05/21 19:58:27 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=default effect=deny policy=k8s-blast-radius message="Operation affects 20 pods, limit is 5"
2026/05/21 19:58:27 WARN post-execution policy check: blast radius exceeded resource_type=kubernetes resource_name=default action=destructive rows_affected=0 pods_affected=20 policy=k8s-blast-radius message="Operation affects 20 pods, limit is 5"
--- PASS: TestScaleDeploymentTool_BlastRadiusDenied_PreExec (0.00s)
=== RUN   TestRestartDeploymentTool_BlastRadiusEnforced_PostExec
2026/05/21 19:58:27 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestRestartDeploymentTool_BlastRadiusEnforced_PostExec3367893680/001/k8s-policies-959638929.yaml policies=1 dry_run=false default=deny
2026/05/21 19:58:27 INFO tool ok name=restart_deployment ms=0
2026/05/21 19:58:27 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=default effect=deny policy=k8s-blast-radius message="Operation affects 2 pods, limit is 1"
2026/05/21 19:58:27 WARN post-execution policy check: blast radius exceeded resource_type=kubernetes resource_name=default action=destructive rows_affected=0 pods_affected=2 policy=k8s-blast-radius message="Operation affects 2 pods, limit is 1"
--- PASS: TestRestartDeploymentTool_BlastRadiusEnforced_PostExec (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraEnforced_UnknownNamespaceUnknownContext
--- PASS: TestResolveNamespaceInfo_InfraEnforced_UnknownNamespaceUnknownContext (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraEnforced_RegisteredByDBName
2026/05/21 19:58:27 INFO resolved database name to namespace name=prod-db namespace=prod-namespace
--- PASS: TestResolveNamespaceInfo_InfraEnforced_RegisteredByDBName (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraEnforced_RegisteredByNamespace
--- PASS: TestResolveNamespaceInfo_InfraEnforced_RegisteredByNamespace (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraEnforced_NonDBNamespaceInKnownCluster
2026/05/21 19:58:27 INFO resolved namespace tags from cluster namespace=default context=gke_prod tags=[production]
--- PASS: TestResolveNamespaceInfo_InfraEnforced_NonDBNamespaceInKnownCluster (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraEnforced_NonDBNamespaceNoContextSoleCluster
2026/05/21 19:58:27 INFO resolved namespace tags from sole cluster namespace=default context=gke_prod tags=[production]
--- PASS: TestResolveNamespaceInfo_InfraEnforced_NonDBNamespaceNoContextSoleCluster (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraPermissive_UnknownNamespace
--- PASS: TestResolveNamespaceInfo_InfraPermissive_UnknownNamespace (0.00s)
=== RUN   TestGetPodsTool_InfraEnforced_Rejected
--- PASS: TestGetPodsTool_InfraEnforced_Rejected (0.00s)
=== RUN   TestGetPodResources_RequestsLimitsOnly
2026/05/21 19:58:27 INFO tool ok name=get_pod_resources count=1 ms=0
--- PASS: TestGetPodResources_RequestsLimitsOnly (0.00s)
=== RUN   TestGetPodResources_WithLiveUsage
2026/05/21 19:58:27 INFO tool ok name=get_pod_resources count=1 ms=0
--- PASS: TestGetPodResources_WithLiveUsage (0.00s)
=== RUN   TestGetPodResources_PolicyDenied
2026/05/21 19:58:27 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGetPodResources_PolicyDenied2815114877/001/k8s-policies-1603362026.yaml policies=1 dry_run=false default=allow
2026/05/21 19:58:27 INFO tool ok name=get_pod_resources count=0 ms=0
--- PASS: TestGetPodResources_PolicyDenied (0.00s)
=== RUN   TestGetNodeStatus_AllNodes
2026/05/21 19:58:27 INFO tool ok name=get_node_status count=1 ms=0
--- PASS: TestGetNodeStatus_AllNodes (0.00s)
=== RUN   TestGetNodeStatus_SingleNode
2026/05/21 19:58:27 INFO tool ok name=get_node_status count=1 ms=0
--- PASS: TestGetNodeStatus_SingleNode (0.00s)
=== RUN   TestGetNodeStatus_MemoryPressure_IncludesMessage
2026/05/21 19:58:27 INFO tool ok name=get_node_status count=1 ms=0
--- PASS: TestGetNodeStatus_MemoryPressure_IncludesMessage (0.00s)
PASS
ok  	helpdesk/agents/k8s	0.963s
=== RUN   TestSysadminDirectRegistry_AllToolsRegistered
--- PASS: TestSysadminDirectRegistry_AllToolsRegistered (0.00s)
=== RUN   TestArgsToStruct_RoundTrip
--- PASS: TestArgsToStruct_RoundTrip (0.00s)
=== RUN   TestArgsToStruct_EmptyArgs
--- PASS: TestArgsToStruct_EmptyArgs (0.00s)
=== RUN   TestSysadminDirectRegistry_CheckHost
--- PASS: TestSysadminDirectRegistry_CheckHost (0.00s)
=== RUN   TestCheckHost_DockerRunning
--- PASS: TestCheckHost_DockerRunning (0.00s)
=== RUN   TestCheckHost_DockerStopped
--- PASS: TestCheckHost_DockerStopped (0.00s)
=== RUN   TestCheckHost_SystemdActive
--- PASS: TestCheckHost_SystemdActive (0.00s)
=== RUN   TestCheckHost_UnknownServer
--- PASS: TestCheckHost_UnknownServer (0.00s)
=== RUN   TestCheckHost_NoInfraConfig
--- PASS: TestCheckHost_NoInfraConfig (0.00s)
=== RUN   TestGetHostLogs_Docker
--- PASS: TestGetHostLogs_Docker (0.00s)
=== RUN   TestGetHostLogs_Filter
--- PASS: TestGetHostLogs_Filter (0.00s)
=== RUN   TestGetHostLogs_DefaultLines
--- PASS: TestGetHostLogs_DefaultLines (0.00s)
=== RUN   TestCheckDisk_Container
--- PASS: TestCheckDisk_Container (0.00s)
=== RUN   TestCheckDisk_RunOnHost
--- PASS: TestCheckDisk_RunOnHost (0.00s)
=== RUN   TestCheckDisk_Systemd
--- PASS: TestCheckDisk_Systemd (0.00s)
=== RUN   TestCheckMemory_NoTarget
--- PASS: TestCheckMemory_NoTarget (0.00s)
=== RUN   TestCheckMemory_Container
--- PASS: TestCheckMemory_Container (0.00s)
=== RUN   TestCheckMemory_RunOnHost
--- PASS: TestCheckMemory_RunOnHost (0.00s)
=== RUN   TestCheckMemory_Systemd
--- PASS: TestCheckMemory_Systemd (0.00s)
=== RUN   TestRestartContainer_Success
2026/05/21 19:58:27 INFO restart_container succeeded target=prod_db container=alloydb-omni reason="crash loop detected"
--- PASS: TestRestartContainer_Success (0.00s)
=== RUN   TestRestartContainer_WrongType
--- PASS: TestRestartContainer_WrongType (0.00s)
=== RUN   TestRestartService_Success
2026/05/21 19:58:27 INFO restart_service succeeded target=prod_db unit=postgresql-16 reason="configuration change applied"
--- PASS: TestRestartService_Success (0.00s)
=== RUN   TestRestartService_WrongType
--- PASS: TestRestartService_WrongType (0.00s)
=== RUN   TestResolveHost_NoVMName
--- PASS: TestResolveHost_NoVMName (0.00s)
=== RUN   TestResolveHost_PodmanRuntime
--- PASS: TestResolveHost_PodmanRuntime (0.00s)
=== RUN   TestReadPgLogFile_Docker
--- PASS: TestReadPgLogFile_Docker (0.00s)
=== RUN   TestReadPgLogFile_K8s
--- PASS: TestReadPgLogFile_K8s (0.00s)
=== RUN   TestReadPgLogFile_Filter
--- PASS: TestReadPgLogFile_Filter (0.00s)
=== RUN   TestResolveHost_K8s
--- PASS: TestResolveHost_K8s (0.00s)
=== RUN   TestResolveHost_K8s_MissingSelector
--- PASS: TestResolveHost_K8s_MissingSelector (0.00s)
=== RUN   TestResolveHost_K8s_UnknownCluster
--- PASS: TestResolveHost_K8s_UnknownCluster (0.00s)
=== RUN   TestCheckDisk_K8s
--- PASS: TestCheckDisk_K8s (0.00s)
=== RUN   TestCheckMemory_K8s
--- PASS: TestCheckMemory_K8s (0.00s)
=== RUN   TestReadPgLogFile_NoLogFiles
--- PASS: TestReadPgLogFile_NoLogFiles (0.00s)
=== RUN   TestReadPgLogFile_CustomLogPath
--- PASS: TestReadPgLogFile_CustomLogPath (0.00s)
=== RUN   TestExecInProcess_K8s_NoPodFound
--- PASS: TestExecInProcess_K8s_NoPodFound (0.00s)
PASS
ok  	helpdesk/agents/sysadmin	0.337s

=== Test Summary ===
  Total:  463
  Passed: 463
  Failed: 0
Stopping test infrastructure...
docker compose -f testing/docker/docker-compose.yaml down -v
[+] Running 4/4
 ✔ Container helpdesk-test-pgloader  Removed                                                                                                                                                                                                10.2s
 ✔ Container helpdesk-test-pg        Removed                                                                                                                                                                                                 0.1s
 ✔ Volume docker_pgdata              Removed                                                                                                                                                                                                 0.0s
 ✔ Network docker_default            Removed                                                                                                                                                                                                 0.1s

real	1m31.229s
user	0m7.416s
sys	0m5.391s
```

