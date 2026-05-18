# aiHelpDesk: Integration Testing: Sample Run

This is a sample run of aiHelpDesk Integration test.
See overall aiHelpDesk Testing approach [here](README.md).

```
[boris@ ~/helpdesk]$ make integration-nocache
Starting test infrastructure...
docker compose -f testing/docker/docker-compose.yaml up -d --wait
[+] Running 2/2
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 1.0s
 ✔ Container helpdesk-test-pgloader  Healthy                                                                                                                                                                                                 1.0s
Running integration tests...
go test --count=1 -tags integration -timeout 120s -v ./testing/integration/... ./agents/database/... ./agents/k8s/... ./agents/sysadmin/... 2>&1 | tee /tmp/helpdesk-integration.log
=== RUN   TestDockerExec_SimpleCommand
--- PASS: TestDockerExec_SimpleCommand (0.06s)
=== RUN   TestDockerExec_PostgresVersion
--- PASS: TestDockerExec_PostgresVersion (0.05s)
=== RUN   TestDockerExec_NonexistentContainer
--- PASS: TestDockerExec_NonexistentContainer (0.02s)
=== RUN   TestDockerCompose_Ps
--- PASS: TestDockerCompose_Ps (0.07s)
=== RUN   TestRunSQLStringViaPgloader_Success
--- PASS: TestRunSQLStringViaPgloader_Success (0.07s)
=== RUN   TestRunSQLStringViaPgloader_Query
--- PASS: TestRunSQLStringViaPgloader_Query (0.17s)
=== RUN   TestDockerCompose_StopStartService
    docker_test.go:123: DockerComposeStop and DockerComposeStart helpers are available
--- PASS: TestDockerCompose_StopStartService (0.02s)
=== RUN   TestExternalInjectSpecs
=== RUN   TestExternalInjectSpecs/db-table-bloat
    external_inject_test.go:82: Testing external inject: Table bloat / dead tuples
2026/05/13 16:30:32 INFO executing injection spec type=sql phase=inject
2026/05/13 16:30:33 INFO executing injection spec type=sql phase=teardown
=== RUN   TestExternalInjectSpecs/db-vacuum-needed
    external_inject_test.go:82: Testing external inject: Tables needing vacuum (dead tuple bloat)
2026/05/13 16:30:33 INFO executing injection spec type=sql phase=inject
2026/05/13 16:30:33 INFO executing injection spec type=sql phase=teardown
=== RUN   TestExternalInjectSpecs/db-disk-pressure
    external_inject_test.go:82: Testing external inject: Disk usage — large table growth
2026/05/13 16:30:33 INFO executing injection spec type=sql phase=inject
2026/05/13 16:30:33 INFO executing injection spec type=sql phase=teardown
=== RUN   TestExternalInjectSpecs/db-wal-stale-slot
    external_inject_test.go:82: Testing external inject: WAL accumulation — stale replication slot
2026/05/13 16:30:33 INFO executing injection spec type=sql phase=inject
2026/05/13 16:30:33 INFO executing injection spec type=sql phase=teardown
--- PASS: TestExternalInjectSpecs (0.61s)
    --- PASS: TestExternalInjectSpecs/db-table-bloat (0.30s)
    --- PASS: TestExternalInjectSpecs/db-vacuum-needed (0.11s)
    --- PASS: TestExternalInjectSpecs/db-disk-pressure (0.12s)
    --- PASS: TestExternalInjectSpecs/db-wal-stale-slot (0.07s)
=== RUN   TestCustomCatalogMergeAndInject
2026/05/13 16:30:33 INFO executing injection spec type=sql phase=inject
2026/05/13 16:30:33 INFO executing injection spec type=sql phase=teardown
--- PASS: TestCustomCatalogMergeAndInject (0.09s)
=== RUN   TestExternalTeardownCleans
2026/05/13 16:30:33 INFO executing injection spec type=sql phase=inject
2026/05/13 16:30:33 INFO executing injection spec type=sql phase=teardown
--- PASS: TestExternalTeardownCleans (0.11s)
=== RUN   TestConnection_Success
--- PASS: TestConnection_Success (0.03s)
=== RUN   TestConnection_WrongPassword
--- PASS: TestConnection_WrongPassword (0.02s)
=== RUN   TestConnection_WrongDatabase
--- PASS: TestConnection_WrongDatabase (0.02s)
=== RUN   TestRunSQLString_Success
--- PASS: TestRunSQLString_Success (0.02s)
=== RUN   TestRunSQLString_CreateAndDropTable
--- PASS: TestRunSQLString_CreateAndDropTable (0.06s)
=== RUN   TestRunSQLString_SyntaxError
--- PASS: TestRunSQLString_SyntaxError (0.02s)
=== RUN   TestQuery_PgStatActivity
--- PASS: TestQuery_PgStatActivity (0.02s)
=== RUN   TestQuery_ConnectionStats
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
--- PASS: TestTableStats_CreateAndQuery (0.10s)
=== RUN   TestQuery_ContextCancellation
--- PASS: TestQuery_ContextCancellation (0.10s)
=== RUN   TestQuery_ExtendedFormat
--- PASS: TestQuery_ExtendedFormat (0.04s)
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
ok  	helpdesk/testing/integration	2.408s
time=2026-05-13T16:30:35.395-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/auditd-integration-1023692669/audit.db
time=2026-05-13T16:30:35.413-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:30:35.414-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:30:35.414-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:30:35.415-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:30:35.415-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:30:35.415-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:30:35.416-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:30:35.416-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:30:35.417-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:30:35.417-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:30:35.417-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:30:35.418-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:30:35.418-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:30:35.418-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:30:35.424-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:30:35.424-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-13T16:30:35.425-04:00 level=INFO msg="audit service starting" version=dev listen=:19901 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/auditd-integration-1023692669/audit.db backend=sqlite socket=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/auditd-integration-1023692669/audit.sock authz_enforcing=false
=== RUN   TestApprovalSession_Create_AssignsIDWithPrefix
time=2026-05-13T16:30:35.428-04:00 level=INFO msg="approval session created" session_id=aps_8286cbfa granted_by=boris expires_at=2026-05-13T21:00:35.428Z allowed_classes="[write destructive]"
--- PASS: TestApprovalSession_Create_AssignsIDWithPrefix (0.00s)
=== RUN   TestApprovalSession_Create_ReturnsExpiresAt
time=2026-05-13T16:30:35.429-04:00 level=INFO msg="approval session created" session_id=aps_940e727a granted_by=alice expires_at=2026-05-13T21:30:35.428Z allowed_classes=[write]
--- PASS: TestApprovalSession_Create_ReturnsExpiresAt (0.00s)
=== RUN   TestApprovalSession_Create_MissingGrantedBy_Returns400
--- PASS: TestApprovalSession_Create_MissingGrantedBy_Returns400 (0.00s)
=== RUN   TestApprovalSession_Create_ZeroExpiry_Returns400
--- PASS: TestApprovalSession_Create_ZeroExpiry_Returns400 (0.00s)
=== RUN   TestApprovalSession_Create_EmptyClasses_Returns400
--- PASS: TestApprovalSession_Create_EmptyClasses_Returns400 (0.00s)
=== RUN   TestApprovalSession_Get_RoundTrip
time=2026-05-13T16:30:35.430-04:00 level=INFO msg="approval session created" session_id=aps_79e8661d granted_by=charlie expires_at=2026-05-13T20:45:35.429Z allowed_classes=[destructive]
--- PASS: TestApprovalSession_Get_RoundTrip (0.00s)
=== RUN   TestApprovalSession_Get_WithScope
time=2026-05-13T16:30:35.430-04:00 level=INFO msg="approval session created" session_id=aps_e23d22a1 granted_by=ops-team expires_at=2026-05-13T21:00:35.430Z allowed_classes=[write]
--- PASS: TestApprovalSession_Get_WithScope (0.00s)
=== RUN   TestApprovalSession_Get_NotFound_Returns404
--- PASS: TestApprovalSession_Get_NotFound_Returns404 (0.00s)
=== RUN   TestApprovalSession_Revoke_SetsRevokedFlag
time=2026-05-13T16:30:35.431-04:00 level=INFO msg="approval session created" session_id=aps_a7768525 granted_by=bob expires_at=2026-05-13T21:00:35.431Z allowed_classes=[write]
time=2026-05-13T16:30:35.431-04:00 level=INFO msg="approval session revoked" session_id=aps_a7768525
--- PASS: TestApprovalSession_Revoke_SetsRevokedFlag (0.00s)
=== RUN   TestApprovalSession_Revoke_NotFound_Returns404
--- PASS: TestApprovalSession_Revoke_NotFound_Returns404 (0.00s)
=== RUN   TestApprovalSession_Revoke_Idempotent
time=2026-05-13T16:30:35.432-04:00 level=INFO msg="approval session created" session_id=aps_7a4433f8 granted_by=eve expires_at=2026-05-13T21:00:35.432Z allowed_classes=[write]
time=2026-05-13T16:30:35.432-04:00 level=INFO msg="approval session revoked" session_id=aps_7a4433f8
time=2026-05-13T16:30:35.432-04:00 level=INFO msg="approval session revoked" session_id=aps_7a4433f8
--- PASS: TestApprovalSession_Revoke_Idempotent (0.00s)
=== RUN   TestApprovalSession_Expiry_FieldsRetained
time=2026-05-13T16:30:35.433-04:00 level=INFO msg="approval session created" session_id=aps_0d08cf0b granted_by=tester expires_at=2026-05-13T20:30:42.432Z allowed_classes=[write]
--- PASS: TestApprovalSession_Expiry_FieldsRetained (0.00s)
=== RUN   TestAuditorHTTPPollingMode
time=2026-05-13T16:30:35.442-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestAuditorHTTPPollingMode345386903/001/audit.db
time=2026-05-13T16:30:35.457-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:30:35.457-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:30:35.458-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:30:35.458-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:30:35.459-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:30:35.459-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:30:35.459-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:30:35.460-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:30:35.460-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:30:35.460-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:30:35.461-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:30:35.461-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:30:35.461-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:30:35.461-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:30:35.467-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:30:35.467-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-13T16:30:35.467-04:00 level=INFO msg="audit service starting" version=dev listen=:19910 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestAuditorHTTPPollingMode345386903/001/audit.db backend=sqlite socket=/tmp/audit_mon_19910.sock authz_enforcing=false
time=2026-05-13T16:30:35.930-04:00 level=INFO msg="starting auditor" socket=audit.sock log_all=false audit_service=http://localhost:19910
time=2026-05-13T16:30:35.930-04:00 level=INFO msg="webhook notifier enabled" url=http://127.0.0.1:64173
time=2026-05-13T16:30:35.930-04:00 level=INFO msg="notifiers configured" count=1
time=2026-05-13T16:30:35.930-04:00 level=INFO msg="audit socket not available; switching to HTTP polling mode" socket=audit.sock url=http://localhost:19910
time=2026-05-13T16:30:35.934-04:00 level=INFO msg="polling for new events" interval=5s url=http://localhost:19910

🚨 [AUDIT CRITICAL] DESTRUCTIVE operation detected
    audit_monitor_test.go:183: webhook received: level=INFO message="Delegation to  (0% confidence)"
time=2026-05-13T16:30:45.937-04:00 level=ERROR msg="[AUDIT CRITICAL] DESTRUCTIVE operation detected" event_id=evt_test_1778704241539548000 session_id=sess_monitor_test user_id=testuser action_class=destructive trace_id=""
    audit_monitor_test.go:183: webhook received: level=CRITICAL message="DESTRUCTIVE operation detected"
--- PASS: TestAuditorHTTPPollingMode (10.51s)
=== RUN   TestAuditorFabricationMismatchAlert
time=2026-05-13T16:30:45.956-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestAuditorFabricationMismatchAlert3925403247/001/audit.db
time=2026-05-13T16:30:45.974-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:30:45.975-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:30:45.975-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:30:45.976-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:30:45.976-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:30:45.977-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:30:45.977-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:30:45.977-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:30:45.978-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:30:45.978-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:30:45.978-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:30:45.979-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:30:45.979-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:30:45.979-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:30:45.984-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:30:45.984-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-13T16:30:45.984-04:00 level=INFO msg="audit service starting" version=dev listen=:19912 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestAuditorFabricationMismatchAlert3925403247/001/audit.db backend=sqlite socket=/tmp/audit_mon_19912.sock authz_enforcing=false
time=2026-05-13T16:30:46.056-04:00 level=INFO msg="starting auditor" socket=audit.sock log_all=false audit_service=http://localhost:19912 incident_webhook=http://127.0.0.1:64184
time=2026-05-13T16:30:46.056-04:00 level=INFO msg="audit socket not available; switching to HTTP polling mode" socket=audit.sock url=http://localhost:19912
time=2026-05-13T16:30:46.058-04:00 level=INFO msg="polling for new events" interval=5s url=http://localhost:19912

🚨 [AUDIT CRITICAL] FABRICATION RISK — agent returned success but audit trail has no matching tool executions
time=2026-05-13T16:30:56.063-04:00 level=ERROR msg="[AUDIT CRITICAL] FABRICATION RISK — agent returned success but audit trail has no matching tool executions" event_id=gv_1778704252052451000 session_id=tr_fab_1778704252047808000 user_id="" agent=postgres_database_agent action_class=destructive trace_id=tr_fab_1778704252047808000
    audit_monitor_test.go:270: incident webhook: alert_type=fabrication_mismatch severity=CRITICAL
--- PASS: TestAuditorFabricationMismatchAlert (10.13s)
=== RUN   TestSecbotHTTPPollingMode
time=2026-05-13T16:30:56.095-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingMode2775625352/001/audit.db
time=2026-05-13T16:30:56.115-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:30:56.115-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:30:56.115-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:30:56.116-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:30:56.116-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:30:56.117-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:30:56.117-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:30:56.117-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:30:56.118-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:30:56.118-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:30:56.119-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:30:56.119-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:30:56.119-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:30:56.119-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:30:56.125-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:30:56.125-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-13T16:30:56.125-04:00 level=INFO msg="audit service starting" version=dev listen=:19911 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingMode2775625352/001/audit.db backend=sqlite socket=/tmp/audit_mon_19911.sock authz_enforcing=false
    audit_monitor_test.go:308: secbot output:

        [16:30:56] ── Phase 1: Startup ──────────────────────────────────
        [16:30:56] Audit service: http://localhost:19911 (HTTP polling)
        [16:30:56] Gateway:       http://127.0.0.1:19999
        [16:30:56] Callback:      127.0.0.1:64206
        [16:30:56] Cooldown:      5m0s
        [16:30:56] Max events/min: 100
        [16:30:56] Dry run:       true


        [16:30:56] ── Phase 2: Connect to Audit Stream ──────────────────


        [16:30:56] ── Phase 3: Monitoring for Security Events ───────────
        [16:30:56] Watching for: high_volume, hash_mismatch, unauthorized_destructive, potential_sql_injection, potential_command_injection

        [16:30:56] Baseline: 0 existing events (not re-analyzed)
        [16:30:56] Polling audit service for new events every 5s
        [16:31:06] EVENT #1: evt_test_1778704262180484000 (type=tool_call)
        [16:31:06] SECURITY ALERT: unauthorized_destructive
        [16:31:06]   Event ID:  evt_test_1778704262180484000
        [16:31:06]   Trace ID:
        [16:31:06]   Time:      2026-05-13T20:31:02Z
        [16:31:06]   Tool:      delete_database
        [16:31:06]   Agent:     database-agent
        [16:31:06]   [DRY RUN] Would create incident bundle

--- PASS: TestSecbotHTTPPollingMode (18.12s)
=== RUN   TestSecbotHTTPPollingReconnect
time=2026-05-13T16:31:14.218-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect2971597401/001/audit.db
time=2026-05-13T16:31:14.241-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:14.242-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:14.242-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:14.243-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:14.243-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:14.243-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:14.244-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:14.244-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:14.244-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:14.245-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:14.245-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:14.246-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:14.246-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:14.246-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:14.252-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:14.252-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-13T16:31:14.252-04:00 level=INFO msg="audit service starting" version=dev listen=:19912 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect2971597401/001/audit.db backend=sqlite socket=/tmp/audit_mon_19912.sock authz_enforcing=false
    audit_monitor_test.go:362: posting first event (before restart)...
    audit_monitor_test.go:367: restarting auditd...
time=2026-05-13T16:31:29.347-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect2971597401/001/audit.db
time=2026-05-13T16:31:29.353-04:00 level=INFO msg="playbooks: seed complete" seeded=0 skipped=13
time=2026-05-13T16:31:29.353-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:29.353-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-05-13T16:31:29.353-04:00 level=INFO msg="audit service starting" version=dev listen=:19912 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect2971597401/001/audit.db backend=sqlite socket=/tmp/audit_mon_19912.sock authz_enforcing=false
    audit_monitor_test.go:376: posting second event (after restart)...
    audit_monitor_test.go:381: secbot output:

        [16:31:14] ── Phase 1: Startup ──────────────────────────────────
        [16:31:14] Audit service: http://localhost:19912 (HTTP polling)
        [16:31:14] Gateway:       http://127.0.0.1:19999
        [16:31:14] Callback:      127.0.0.1:64252
        [16:31:14] Cooldown:      5m0s
        [16:31:14] Max events/min: 100
        [16:31:14] Dry run:       true


        [16:31:14] ── Phase 2: Connect to Audit Stream ──────────────────


        [16:31:14] ── Phase 3: Monitoring for Security Events ───────────
        [16:31:14] Watching for: high_volume, hash_mismatch, unauthorized_destructive, potential_sql_injection, potential_command_injection

        [16:31:14] Baseline: 0 existing events (not re-analyzed)
        [16:31:14] Polling audit service for new events every 5s
        [16:31:24] EVENT #1: evt_test_1778704280304209000 (type=tool_call)
        [16:31:24] SECURITY ALERT: unauthorized_destructive
        [16:31:24]   Event ID:  evt_test_1778704280304209000
        [16:31:24]   Trace ID:
        [16:31:24]   Time:      2026-05-13T20:31:20Z
        [16:31:24]   Tool:      delete_database
        [16:31:24]   Agent:     database-agent
        [16:31:24]   [DRY RUN] Would create incident bundle

        [16:31:29] WARN: HTTP poll failed: Get "http://localhost:19912/v1/events?limit=200&since=2026-05-13T20:31:20Z": dial tcp [::1]:19912: connect: connection refused
        [16:31:34] EVENT #2: evt_test_1778704289424307000 (type=tool_call)
        [16:31:34] SECURITY ALERT: unauthorized_destructive
        [16:31:34]   Event ID:  evt_test_1778704289424307000
        [16:31:34]   Trace ID:
        [16:31:34]   Time:      2026-05-13T20:31:29Z
        [16:31:34]   Tool:      delete_database
        [16:31:34]   Agent:     database-agent
        [16:31:34]   [DRY RUN] Would create incident bundle

--- PASS: TestSecbotHTTPPollingReconnect (27.24s)
=== RUN   TestHealth
--- PASS: TestHealth (0.00s)
=== RUN   TestAudit_RecordEvent_ReturnsHashes
--- PASS: TestAudit_RecordEvent_ReturnsHashes (0.01s)
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
time=2026-05-13T16:31:41.461-04:00 level=INFO msg="approval request created" approval_id=apr_88970688 action_class=write tool=execute_sql agent=database-agent requested_by=alice
--- PASS: TestApprovals_CreateAndGet (0.00s)
=== RUN   TestApprovals_ListPending
time=2026-05-13T16:31:41.463-04:00 level=INFO msg="approval request created" approval_id=apr_063168b6 action_class=destructive tool=drop_table agent=database-agent requested_by=bob
--- PASS: TestApprovals_ListPending (0.00s)
=== RUN   TestApprovals_Approve
time=2026-05-13T16:31:41.464-04:00 level=INFO msg="approval request created" approval_id=apr_9240e4af action_class=write tool=update_config agent=k8s-agent requested_by=carol
time=2026-05-13T16:31:41.465-04:00 level=INFO msg="approval granted" approval_id=apr_9240e4af approved_by=manager valid_for=0s
--- PASS: TestApprovals_Approve (0.00s)
=== RUN   TestApprovals_Deny
time=2026-05-13T16:31:41.466-04:00 level=INFO msg="approval request created" approval_id=apr_b44734f8 action_class=destructive tool=delete_namespace agent=k8s-agent requested_by=dave
time=2026-05-13T16:31:41.467-04:00 level=INFO msg="approval denied" approval_id=apr_b44734f8 denied_by=admin
--- PASS: TestApprovals_Deny (0.00s)
=== RUN   TestApprovals_Cancel
time=2026-05-13T16:31:41.468-04:00 level=INFO msg="approval request created" approval_id=apr_718914cd action_class=write tool=scale_deployment agent=k8s-agent requested_by=eve
time=2026-05-13T16:31:41.468-04:00 level=INFO msg="approval cancelled" approval_id=apr_718914cd
--- PASS: TestApprovals_Cancel (0.00s)
=== RUN   TestApprovals_FilterByStatus
time=2026-05-13T16:31:41.470-04:00 level=INFO msg="approval request created" approval_id=apr_0a1adab2 action_class=write tool=patch_service agent=k8s-agent requested_by=frank
time=2026-05-13T16:31:41.471-04:00 level=INFO msg="approval granted" approval_id=apr_0a1adab2 approved_by=lead valid_for=0s
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
time=2026-05-13T16:31:41.491-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_InfoWithPolicyEnabled1929284456/002/audit.db
time=2026-05-13T16:31:41.509-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:41.509-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:41.509-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:41.510-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:41.510-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:41.511-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:41.511-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:41.511-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:41.512-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:41.512-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:41.512-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:41.513-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:41.513-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:41.513-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:41.518-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:41.518-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_InfoWithPolicyEnabled1929284456/001/policies.yaml policies=1
time=2026-05-13T16:31:41.518-04:00 level=INFO msg="audit service starting" version=dev listen=:19902 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_InfoWithPolicyEnabled1929284456/002/audit.db backend=sqlite socket=/tmp/atest-475650000.sock authz_enforcing=false
--- PASS: TestGovernance_InfoWithPolicyEnabled (0.11s)
=== RUN   TestIntegration_AgentReasoningRoundTrip
--- PASS: TestIntegration_AgentReasoningRoundTrip (0.00s)
=== RUN   TestGovernance_PoliciesSummaryWithEngine
time=2026-05-13T16:31:41.595-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_PoliciesSummaryWithEngine254173189/002/audit.db
time=2026-05-13T16:31:41.611-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:41.611-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:41.612-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:41.612-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:41.613-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:41.613-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:41.613-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:41.614-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:41.614-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:41.614-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:41.615-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:41.615-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:41.615-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:41.615-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:41.620-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:41.620-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_PoliciesSummaryWithEngine254173189/001/policies.yaml policies=1
time=2026-05-13T16:31:41.620-04:00 level=INFO msg="audit service starting" version=dev listen=:19902 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_PoliciesSummaryWithEngine254173189/002/audit.db backend=sqlite socket=/tmp/atest-585008000.sock authz_enforcing=false
--- PASS: TestGovernance_PoliciesSummaryWithEngine (0.11s)
=== RUN   TestGovernance_Explain_DefaultConfig
time=2026-05-13T16:31:41.701-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_Explain_DefaultConfig3702057880/002/audit.db
time=2026-05-13T16:31:41.716-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:41.717-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:41.717-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:41.718-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:41.718-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:41.718-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:41.719-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:41.719-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:41.719-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:41.720-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:41.720-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:41.720-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:41.721-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:41.721-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:41.725-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:41.725-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_Explain_DefaultConfig3702057880/001/policies.yaml policies=1
time=2026-05-13T16:31:41.726-04:00 level=INFO msg="audit service starting" version=dev listen=:19903 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_Explain_DefaultConfig3702057880/002/audit.db backend=sqlite socket=/tmp/atest3-690950000.sock authz_enforcing=false
time=2026-05-13T16:31:41.795-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=db-policy message="writes require approval"
--- PASS: TestGovernance_Explain_DefaultConfig (0.11s)
=== RUN   TestAudit_WriteToolEvent_RecordedAndQueryable
--- PASS: TestAudit_WriteToolEvent_RecordedAndQueryable (0.00s)
=== RUN   TestAudit_DestructiveToolEvent_RecordedAndQueryable
--- PASS: TestAudit_DestructiveToolEvent_RecordedAndQueryable (0.00s)
=== RUN   TestAudit_MultipleToolEvents_HashChainValid
--- PASS: TestAudit_MultipleToolEvents_HashChainValid (0.00s)
=== RUN   TestAudit_WriteApprovalWorkflow_ForNewTools
time=2026-05-13T16:31:41.800-04:00 level=INFO msg="approval request created" approval_id=apr_34713b9e action_class=write tool=cancel_query agent=postgres-agent requested_by=operator
time=2026-05-13T16:31:41.801-04:00 level=INFO msg="approval granted" approval_id=apr_34713b9e approved_by=senior-dba valid_for=0s
--- PASS: TestAudit_WriteApprovalWorkflow_ForNewTools (0.00s)
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/terminate_connection
time=2026-05-13T16:31:41.801-04:00 level=INFO msg="approval request created" approval_id=apr_049b0c7f action_class=destructive tool=terminate_connection agent=test-agent requested_by=sre-oncall
time=2026-05-13T16:31:41.801-04:00 level=INFO msg="approval denied" approval_id=apr_049b0c7f denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/terminate_idle_connections
time=2026-05-13T16:31:41.802-04:00 level=INFO msg="approval request created" approval_id=apr_128d7108 action_class=destructive tool=terminate_idle_connections agent=test-agent requested_by=sre-oncall
time=2026-05-13T16:31:41.802-04:00 level=INFO msg="approval denied" approval_id=apr_128d7108 denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/delete_pod
time=2026-05-13T16:31:41.803-04:00 level=INFO msg="approval request created" approval_id=apr_dcf17c4a action_class=destructive tool=delete_pod agent=test-agent requested_by=sre-oncall
time=2026-05-13T16:31:41.803-04:00 level=INFO msg="approval denied" approval_id=apr_dcf17c4a denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/restart_deployment
time=2026-05-13T16:31:41.803-04:00 level=INFO msg="approval request created" approval_id=apr_374ad339 action_class=destructive tool=restart_deployment agent=test-agent requested_by=sre-oncall
time=2026-05-13T16:31:41.804-04:00 level=INFO msg="approval denied" approval_id=apr_374ad339 denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/scale_deployment
time=2026-05-13T16:31:41.804-04:00 level=INFO msg="approval request created" approval_id=apr_481cecfb action_class=destructive tool=scale_deployment agent=test-agent requested_by=sre-oncall
time=2026-05-13T16:31:41.804-04:00 level=INFO msg="approval denied" approval_id=apr_481cecfb denied_by=change-manager
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
time=2026-05-13T16:31:41.817-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_UserPrincipal_FlowsThroughToAuditEvent4089877760/002/audit.db
time=2026-05-13T16:31:41.831-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:41.831-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:41.832-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:41.832-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:41.833-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:41.833-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:41.833-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:41.834-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:41.834-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:41.834-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:41.834-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:41.835-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:41.835-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:41.835-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:41.839-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:41.840-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_UserPrincipal_FlowsThroughToAuditEvent4089877760/001/policies.yaml policies=4
time=2026-05-13T16:31:41.840-04:00 level=INFO msg="audit service starting" version=dev listen=:64296 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_UserPrincipal_FlowsThroughToAuditEvent4089877760/002/audit.db backend=sqlite socket=/tmp/aidtest-809493000.sock authz_enforcing=false
--- PASS: TestIdentity_UserPrincipal_FlowsThroughToAuditEvent (0.11s)
=== RUN   TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent
time=2026-05-13T16:31:41.925-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent450690721/002/audit.db
time=2026-05-13T16:31:41.940-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:41.941-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:41.941-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:41.942-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:41.942-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:41.942-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:41.943-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:41.943-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:41.943-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:41.944-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:41.944-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:41.944-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:41.945-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:41.945-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:41.949-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:41.949-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent450690721/001/policies.yaml policies=4
time=2026-05-13T16:31:41.950-04:00 level=INFO msg="audit service starting" version=dev listen=:64303 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent450690721/002/audit.db backend=sqlite socket=/tmp/aidtest-916118000.sock authz_enforcing=false
--- PASS: TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent (0.11s)
=== RUN   TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity
time=2026-05-13T16:31:42.032-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity2835072191/002/audit.db
time=2026-05-13T16:31:42.047-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:42.047-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:42.048-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:42.048-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:42.049-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:42.049-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:42.049-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:42.050-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:42.050-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:42.050-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:42.051-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:42.051-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:42.051-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:42.051-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:42.056-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:42.056-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity2835072191/001/policies.yaml policies=4
time=2026-05-13T16:31:42.056-04:00 level=INFO msg="audit service starting" version=dev listen=:64310 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity2835072191/002/audit.db backend=sqlite socket=/tmp/aidtest-22580000.sock authz_enforcing=false
--- PASS: TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity (0.11s)
=== RUN   TestIdentity_DBACanWrite
time=2026-05-13T16:31:42.138-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DBACanWrite628840004/002/audit.db
time=2026-05-13T16:31:42.153-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:42.153-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:42.154-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:42.154-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:42.155-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:42.155-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:42.155-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:42.156-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:42.156-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:42.156-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:42.157-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:42.157-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:42.158-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:42.158-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:42.162-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:42.162-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DBACanWrite628840004/001/policies.yaml policies=4
time=2026-05-13T16:31:42.163-04:00 level=INFO msg="audit service starting" version=dev listen=:64317 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DBACanWrite628840004/002/audit.db backend=sqlite socket=/tmp/aidtest-129104000.sock authz_enforcing=false
--- PASS: TestIdentity_DBACanWrite (0.11s)
=== RUN   TestIdentity_NonDBADeniedWrite
time=2026-05-13T16:31:42.245-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonDBADeniedWrite506679662/002/audit.db
time=2026-05-13T16:31:42.260-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:42.260-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:42.261-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:42.261-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:42.261-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:42.262-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:42.262-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:42.262-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:42.263-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:42.263-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:42.264-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:42.264-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:42.264-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:42.264-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:42.269-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:42.269-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonDBADeniedWrite506679662/001/policies.yaml policies=4
time=2026-05-13T16:31:42.269-04:00 level=INFO msg="audit service starting" version=dev listen=:64324 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonDBADeniedWrite506679662/002/audit.db backend=sqlite socket=/tmp/aidtest-235644000.sock authz_enforcing=false
time=2026-05-13T16:31:42.339-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=default-policy user=bob@example.com message="writes not allowed"
time=2026-05-13T16:31:42.340-04:00 level=WARN msg="policy check: DENY" event_id=pol_d7e9e231 resource=database:prod-db action=write policy=default-policy agent=""
--- PASS: TestIdentity_NonDBADeniedWrite (0.11s)
=== RUN   TestIdentity_OncallEmergencyBreakGlass
time=2026-05-13T16:31:42.351-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_OncallEmergencyBreakGlass438118519/002/audit.db
time=2026-05-13T16:31:42.367-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:42.367-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:42.368-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:42.368-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:42.368-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:42.369-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:42.369-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:42.369-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:42.370-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:42.370-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:42.371-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:42.371-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:42.372-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:42.372-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:42.377-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:42.377-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_OncallEmergencyBreakGlass438118519/001/policies.yaml policies=4
time=2026-05-13T16:31:42.377-04:00 level=INFO msg="audit service starting" version=dev listen=:64331 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_OncallEmergencyBreakGlass438118519/002/audit.db backend=sqlite socket=/tmp/aidtest-342133000.sock authz_enforcing=false
--- PASS: TestIdentity_OncallEmergencyBreakGlass (0.12s)
=== RUN   TestIdentity_NonOncallEmergencyDenied
time=2026-05-13T16:31:42.474-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonOncallEmergencyDenied78114071/002/audit.db
time=2026-05-13T16:31:42.491-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:42.492-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:42.495-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:42.496-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:42.496-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:42.496-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:42.497-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:42.497-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:42.498-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:42.498-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:42.498-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:42.499-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:42.499-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:42.499-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:42.509-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:42.510-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonOncallEmergencyDenied78114071/001/policies.yaml policies=4
time=2026-05-13T16:31:42.510-04:00 level=INFO msg="audit service starting" version=dev listen=:64338 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonOncallEmergencyDenied78114071/002/audit.db backend=sqlite socket=/tmp/aidtest-458971000.sock authz_enforcing=false
time=2026-05-13T16:31:42.564-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=default-policy user=dave@example.com purpose=emergency message="writes not allowed"
time=2026-05-13T16:31:42.565-04:00 level=WARN msg="policy check: DENY" event_id=pol_4c588d33 resource=database:prod-db action=write policy=default-policy agent=""
--- PASS: TestIdentity_NonOncallEmergencyDenied (0.11s)
=== RUN   TestIdentity_DiagnosticPurposeBlocksWrite
time=2026-05-13T16:31:42.586-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DiagnosticPurposeBlocksWrite4238224050/002/audit.db
time=2026-05-13T16:31:42.605-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:42.609-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:42.610-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:42.611-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:42.611-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:42.612-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:42.612-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:42.612-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:42.613-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:42.613-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:42.613-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:42.614-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:42.614-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:42.614-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:42.619-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:42.619-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DiagnosticPurposeBlocksWrite4238224050/001/policies.yaml policies=4
time=2026-05-13T16:31:42.619-04:00 level=INFO msg="audit service starting" version=dev listen=:64345 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DiagnosticPurposeBlocksWrite4238224050/002/audit.db backend=sqlite socket=/tmp/aidtest-568827000.sock authz_enforcing=false
time=2026-05-13T16:31:42.673-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=dba-policy user=alice@example.com purpose=diagnostic message="Purpose \"diagnostic\" is in the blocked list [diagnostic]"
time=2026-05-13T16:31:42.674-04:00 level=WARN msg="policy check: DENY" event_id=pol_e4dea1a9 resource=database:prod-db action=write policy=dba-policy agent=""
--- PASS: TestIdentity_DiagnosticPurposeBlocksWrite (0.11s)
=== RUN   TestIdentity_RemediationPurposeAllowsDBAWrite
time=2026-05-13T16:31:42.684-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_RemediationPurposeAllowsDBAWrite4188763182/002/audit.db
time=2026-05-13T16:31:42.700-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:42.700-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:42.701-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:42.701-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:42.701-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:42.702-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:42.702-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:42.702-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:42.703-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:42.703-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:42.703-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:42.704-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:42.704-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:42.704-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:42.709-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:42.709-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_RemediationPurposeAllowsDBAWrite4188763182/001/policies.yaml policies=4
time=2026-05-13T16:31:42.709-04:00 level=INFO msg="audit service starting" version=dev listen=:64352 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_RemediationPurposeAllowsDBAWrite4188763182/002/audit.db backend=sqlite socket=/tmp/aidtest-676232000.sock authz_enforcing=false
--- PASS: TestIdentity_RemediationPurposeAllowsDBAWrite (0.11s)
=== RUN   TestIdentity_PIIReadWithPurpose_Allowed
time=2026-05-13T16:31:42.792-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithPurpose_Allowed3638310250/002/audit.db
time=2026-05-13T16:31:42.807-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:42.808-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:42.808-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:42.808-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:42.809-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:42.809-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:42.809-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:42.810-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:42.810-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:42.811-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:42.811-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:42.812-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:42.812-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:42.812-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:42.817-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:42.817-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithPurpose_Allowed3638310250/001/policies.yaml policies=4
time=2026-05-13T16:31:42.817-04:00 level=INFO msg="audit service starting" version=dev listen=:64359 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithPurpose_Allowed3638310250/002/audit.db backend=sqlite socket=/tmp/aidtest-782716000.sock authz_enforcing=false
--- PASS: TestIdentity_PIIReadWithPurpose_Allowed (0.11s)
=== RUN   TestIdentity_PIIReadWithoutPurpose_Denied
time=2026-05-13T16:31:42.900-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithoutPurpose_Denied3276682116/002/audit.db
time=2026-05-13T16:31:42.914-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:42.915-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:42.915-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:42.916-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:42.916-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:42.916-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:42.917-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:42.917-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:42.917-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:42.918-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:42.918-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:42.918-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:42.919-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:42.919-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:42.924-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:42.924-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithoutPurpose_Denied3276682116/001/policies.yaml policies=4
time=2026-05-13T16:31:42.924-04:00 level=INFO msg="audit service starting" version=dev listen=:64366 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithoutPurpose_Denied3276682116/002/audit.db backend=sqlite socket=/tmp/aidtest-890032000.sock authz_enforcing=false
time=2026-05-13T16:31:42.993-04:00 level=WARN msg="policy decision: DENY" action=read resource_type=database resource_name=customers effect=deny policy=pii-protection user=bob@example.com resource_sensitivity=[pii] message="Purpose \"\" is not in the allowed list [diagnostic compliance remediation]"
time=2026-05-13T16:31:42.994-04:00 level=WARN msg="policy check: DENY" event_id=pol_9107f21d resource=database:customers action=read policy=pii-protection agent=""
--- PASS: TestIdentity_PIIReadWithoutPurpose_Denied (0.11s)
=== RUN   TestIdentity_PIIWrite_AlwaysDenied
time=2026-05-13T16:31:43.005-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIWrite_AlwaysDenied1288246626/002/audit.db
time=2026-05-13T16:31:43.020-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:43.021-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:43.021-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:43.022-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:43.022-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:43.022-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:43.023-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:43.023-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:43.023-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:43.024-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:43.024-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:43.024-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:43.025-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:43.025-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:43.029-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:43.029-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIWrite_AlwaysDenied1288246626/001/policies.yaml policies=4
time=2026-05-13T16:31:43.029-04:00 level=INFO msg="audit service starting" version=dev listen=:64373 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIWrite_AlwaysDenied1288246626/002/audit.db backend=sqlite socket=/tmp/aidtest-996348000.sock authz_enforcing=false
time=2026-05-13T16:31:43.100-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=customers effect=deny policy=pii-protection user=alice@example.com resource_sensitivity=[pii] purpose=remediation message="Writes to PII databases are prohibited."
time=2026-05-13T16:31:43.100-04:00 level=WARN msg="policy check: DENY" event_id=pol_1e4e36ac resource=database:customers action=write policy=pii-protection agent=""
--- PASS: TestIdentity_PIIWrite_AlwaysDenied (0.11s)
=== RUN   TestIdentity_Explain_WithPurposeAndSensitivity_Allow
time=2026-05-13T16:31:43.112-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Allow4069666566/002/audit.db
time=2026-05-13T16:31:43.128-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:43.129-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:43.129-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:43.129-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:43.130-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:43.130-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:43.130-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:43.131-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:43.131-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:43.132-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:43.132-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:43.132-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:43.133-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:43.133-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:43.138-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:43.138-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Allow4069666566/001/policies.yaml policies=4
time=2026-05-13T16:31:43.138-04:00 level=INFO msg="audit service starting" version=dev listen=:64380 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Allow4069666566/002/audit.db backend=sqlite socket=/tmp/aidtest-102643000.sock authz_enforcing=false
--- PASS: TestIdentity_Explain_WithPurposeAndSensitivity_Allow (0.11s)
=== RUN   TestIdentity_Explain_WithPurposeAndSensitivity_Deny
time=2026-05-13T16:31:43.243-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Deny3949693027/002/audit.db
time=2026-05-13T16:31:43.265-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:43.265-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:43.266-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:43.266-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:43.268-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:43.269-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:43.269-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:43.270-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:43.271-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:43.271-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:43.271-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:43.272-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:43.272-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:43.272-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:43.279-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:43.280-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Deny3949693027/001/policies.yaml policies=4
time=2026-05-13T16:31:43.280-04:00 level=INFO msg="audit service starting" version=dev listen=:64387 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Deny3949693027/002/audit.db backend=sqlite socket=/tmp/aidtest-212314000.sock authz_enforcing=false
time=2026-05-13T16:31:43.317-04:00 level=WARN msg="policy decision: DENY" action=read resource_type=database resource_name=customers effect=deny policy=pii-protection resource_sensitivity=[pii] message="Purpose \"\" is not in the allowed list [diagnostic compliance remediation]"
--- PASS: TestIdentity_Explain_WithPurposeAndSensitivity_Deny (0.11s)
=== RUN   TestIdentity_Explain_DiagnosticPurpose_DeniesWrite
time=2026-05-13T16:31:43.341-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_DiagnosticPurpose_DeniesWrite4258041450/002/audit.db
time=2026-05-13T16:31:43.360-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:43.361-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:43.361-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:43.362-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:43.363-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:43.363-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:43.364-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:43.364-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:43.364-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:43.365-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:43.365-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:43.366-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:43.366-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:43.366-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:43.372-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:43.372-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_DiagnosticPurpose_DeniesWrite4258041450/001/policies.yaml policies=4
time=2026-05-13T16:31:43.372-04:00 level=INFO msg="audit service starting" version=dev listen=:64394 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_DiagnosticPurpose_DeniesWrite4258041450/002/audit.db backend=sqlite socket=/tmp/aidtest-321354000.sock authz_enforcing=false
time=2026-05-13T16:31:43.430-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=default-policy purpose=diagnostic message="writes not allowed"
--- PASS: TestIdentity_Explain_DiagnosticPurpose_DeniesWrite (0.11s)
=== RUN   TestIdentity_FullPolicyDecisionRoundTrip
time=2026-05-13T16:31:43.441-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_FullPolicyDecisionRoundTrip3016378245/002/audit.db
time=2026-05-13T16:31:43.456-04:00 level=INFO msg="playbooks: seeded system playbook" name="Checkpoint & bgwriter Triage" series=pbs_checkpoint_bgwriter_triage version=1.0 active=true
time=2026-05-13T16:31:43.456-04:00 level=INFO msg="playbooks: seeded system playbook" name="Connection & Lock Triage" series=pbs_connection_triage version=1.0 active=true
time=2026-05-13T16:31:43.456-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Configuration Recovery" series=pbs_db_config_recovery version=1.1 active=true
time=2026-05-13T16:31:43.457-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Backup Restore & PITR" series=pbs_db_pitr_recovery version=1.1 active=true
time=2026-05-13T16:31:43.458-04:00 level=INFO msg="playbooks: seeded system playbook" name="Database Down — Restart Triage" series=pbs_db_restart_triage version=1.3 active=true
time=2026-05-13T16:31:43.458-04:00 level=INFO msg="playbooks: seeded system playbook" name="K8s Pod Crash — Diagnosis" series=pbs_k8s_pod_crash_triage version=1.0 active=true
time=2026-05-13T16:31:43.458-04:00 level=INFO msg="playbooks: seeded system playbook" name="Replication Lag Triage" series=pbs_replication_lag version=1.0 active=true
time=2026-05-13T16:31:43.459-04:00 level=INFO msg="playbooks: seeded system playbook" name="Slow Query Triage" series=pbs_slow_query_triage version=1.0 active=true
time=2026-05-13T16:31:43.459-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Inspection" series=pbs_sysadmin_docker_inspect version=1.4 active=true
time=2026-05-13T16:31:43.460-04:00 level=INFO msg="playbooks: seeded system playbook" name="Sysadmin — Docker Container Restart" series=pbs_db_restart_action version=1.0 active=true
time=2026-05-13T16:31:43.460-04:00 level=INFO msg="playbooks: seeded system playbook" name="Vacuum & Bloat Triage" series=pbs_vacuum_triage version=1.0 active=true
time=2026-05-13T16:31:43.460-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Disk Full — Recovery" series=pbs_wal_disk_full version=1.0 active=true
time=2026-05-13T16:31:43.461-04:00 level=INFO msg="playbooks: seeded system playbook" name="WAL Accumulation — Stale Replication Slot" series=pbs_wal_stale_slot version=1.1 active=true
time=2026-05-13T16:31:43.461-04:00 level=INFO msg="playbooks: seed complete" seeded=13 skipped=0
time=2026-05-13T16:31:43.465-04:00 level=WARN msg="authorization NOT enforcing: all endpoints are open — set HELPDESK_USERS_FILE to enable role-based access control"
time=2026-05-13T16:31:43.465-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_FullPolicyDecisionRoundTrip3016378245/001/policies.yaml policies=4
time=2026-05-13T16:31:43.466-04:00 level=INFO msg="audit service starting" version=dev listen=:64401 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_FullPolicyDecisionRoundTrip3016378245/002/audit.db backend=sqlite socket=/tmp/aidtest-432303000.sock authz_enforcing=false
--- PASS: TestIdentity_FullPolicyDecisionRoundTrip (0.11s)
PASS
ok  	helpdesk/testing/integration/governance	71.890s
=== RUN   TestDatabaseDirectRegistry_AllToolsRegistered
--- PASS: TestDatabaseDirectRegistry_AllToolsRegistered (0.00s)
=== RUN   TestArgsToStruct_RoundTrip
--- PASS: TestArgsToStruct_RoundTrip (0.00s)
=== RUN   TestArgsToStruct_EmptyArgs
--- PASS: TestArgsToStruct_EmptyArgs (0.00s)
=== RUN   TestDatabaseDirectRegistry_ToolCallable
2026/05/13 16:30:32 INFO tool ok name=check_connection ms=0
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
--- PASS: TestInspectQuery_AllColumnsPresent (0.06s)
=== RUN   TestInspectConnection_NonExistentPID
2026/05/13 16:30:32 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/13 16:30:32 INFO tool ok name=get_session_info ms=21
--- PASS: TestInspectConnection_NonExistentPID (0.04s)
=== RUN   TestInspectConnection_WriteTransaction
2026/05/13 16:30:32 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/13 16:30:32 INFO tool ok name=get_session_info ms=24
--- PASS: TestInspectConnection_WriteTransaction (20.09s)
=== RUN   TestInspectConnection_ReadOnlyTransaction
2026/05/13 16:30:52 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/13 16:30:52 INFO tool ok name=get_session_info ms=28
--- PASS: TestInspectConnection_ReadOnlyTransaction (0.11s)
=== RUN   TestGetSessionInfoTool_Integration
2026/05/13 16:30:52 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/13 16:30:52 INFO tool ok name=get_session_info ms=21
--- PASS: TestGetSessionInfoTool_Integration (20.09s)
=== RUN   TestGetStatusSummaryTool_Integration
2026/05/13 16:31:12 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/13 16:31:12 INFO tool ok name=get_status_summary ms=31
--- PASS: TestGetStatusSummaryTool_Integration (0.10s)
=== RUN   TestDetectRollbackCapability_Integration
    tools_integration_test.go:364: WALLevel = "replica"  HasReplication = true  Mode = "row_capture"
--- PASS: TestDetectRollbackCapability_Integration (0.04s)
=== RUN   TestDetectRollbackCapability_Integration_Override
--- PASS: TestDetectRollbackCapability_Integration_Override (0.03s)
=== RUN   TestGetBgwriterStatsTool_Integration
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost port=15432 dbname=testdb user=postgres password=testpass" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_bgwriter_stats ms=21
--- PASS: TestGetBgwriterStatsTool_Integration (0.04s)
=== RUN   TestWALBracket_Open_FailsWithoutLogicalWAL
    tools_integration_test.go:462: WALBracket.Open correctly failed on wal_level="replica": create replication slot helpdesk_rbk_integrat: ERROR: logical decoding requires wal_level >= logical (SQLSTATE 55000)
--- PASS: TestWALBracket_Open_FailsWithoutLogicalWAL (0.04s)
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
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestRunPsql_Success (0.00s)
=== RUN   TestRunPsql_Error
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=badhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool="" ms=0 err="exit status 1" output="connection refused"
--- PASS: TestRunPsql_Error (0.00s)
=== RUN   TestRunPsql_EmptyOutput
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool="" ms=0 err="exit status 1" output="(no output from psql)"
--- PASS: TestRunPsql_EmptyOutput (0.00s)
=== RUN   TestRunPsql_UndiagnosedError
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool="" ms=0 err="exit status 1" output="some weird error"
--- PASS: TestRunPsql_UndiagnosedError (0.00s)
=== RUN   TestRunPsql_EmptyConnStr
--- PASS: TestRunPsql_EmptyConnStr (0.00s)
=== RUN   TestCheckConnectionTool_Success
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=check_connection ms=0
--- PASS: TestCheckConnectionTool_Success (0.00s)
=== RUN   TestCheckConnectionTool_Failure
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=check_connection ms=0 err="exit status 1" output="password authentication failed"
--- PASS: TestCheckConnectionTool_Failure (0.00s)
=== RUN   TestGetServerInfoTool_Success
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_server_info ms=0
--- PASS: TestGetServerInfoTool_Success (0.00s)
=== RUN   TestGetServerInfoTool_Failure
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=get_server_info ms=0 err="exit status 1" output="connection refused"
--- PASS: TestGetServerInfoTool_Failure (0.00s)
=== RUN   TestGetActiveConnectionsTool_WithConnections
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_active_connections ms=0
--- PASS: TestGetActiveConnectionsTool_WithConnections (0.00s)
=== RUN   TestGetActiveConnectionsTool_NoConnections
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_active_connections ms=0
--- PASS: TestGetActiveConnectionsTool_NoConnections (0.00s)
=== RUN   TestGetActiveConnectionsTool_EmptyOutput
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_active_connections ms=0
--- PASS: TestGetActiveConnectionsTool_EmptyOutput (0.00s)
=== RUN   TestGetActiveConnectionsTool_IdleIncludedByDefault
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_active_connections ms=0
--- PASS: TestGetActiveConnectionsTool_IdleIncludedByDefault (0.00s)
=== RUN   TestGetActiveConnectionsTool_ActiveOnly
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_active_connections ms=0
--- PASS: TestGetActiveConnectionsTool_ActiveOnly (0.00s)
=== RUN   TestGetLockInfoTool_WithLocks
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_lock_info ms=0
--- PASS: TestGetLockInfoTool_WithLocks (0.00s)
=== RUN   TestGetLockInfoTool_NoLocks
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_lock_info ms=0
--- PASS: TestGetLockInfoTool_NoLocks (0.00s)
=== RUN   TestGetLockInfoTool_EmptyOutput
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_lock_info ms=0
--- PASS: TestGetLockInfoTool_EmptyOutput (0.00s)
=== RUN   TestGetTableStatsTool_WithTableName
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_table_stats ms=0
--- PASS: TestGetTableStatsTool_WithTableName (0.00s)
=== RUN   TestGetTableStatsTool_CustomSchema
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_table_stats ms=0
--- PASS: TestGetTableStatsTool_CustomSchema (0.00s)
=== RUN   TestGetTableStatsTool_DefaultSchema
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_table_stats ms=0
--- PASS: TestGetTableStatsTool_DefaultSchema (0.00s)
=== RUN   TestGetDatabaseInfoTool_Success
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_database_info ms=0
--- PASS: TestGetDatabaseInfoTool_Success (0.00s)
=== RUN   TestGetConnectionStatsTool_Success
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_connection_stats ms=0
--- PASS: TestGetConnectionStatsTool_Success (0.00s)
=== RUN   TestGetDatabaseStatsTool_Success
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_database_stats ms=0
--- PASS: TestGetDatabaseStatsTool_Success (0.00s)
=== RUN   TestGetConfigParameterTool_SpecificParameter
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_config_parameter ms=0
--- PASS: TestGetConfigParameterTool_SpecificParameter (0.00s)
=== RUN   TestGetConfigParameterTool_DefaultParameters
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_config_parameter ms=0
--- PASS: TestGetConfigParameterTool_DefaultParameters (0.00s)
=== RUN   TestGetReplicationStatusTool_Success
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_replication_status ms=0
--- PASS: TestGetReplicationStatusTool_Success (0.00s)
=== RUN   TestToolsErrorHandling
=== RUN   TestToolsErrorHandling/getServerInfoTool
2026/05/13 16:31:13 WARN psql command failed tool=get_server_info ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getDatabaseInfoTool
2026/05/13 16:31:13 WARN psql command failed tool=get_database_info ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getActiveConnectionsTool
2026/05/13 16:31:13 WARN psql command failed tool=get_active_connections ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getConnectionStatsTool
2026/05/13 16:31:13 WARN psql command failed tool=get_connection_stats ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getDatabaseStatsTool
2026/05/13 16:31:13 WARN psql command failed tool=get_database_stats ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getConfigParameterTool
2026/05/13 16:31:13 WARN psql command failed tool=get_config_parameter ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getReplicationStatusTool
2026/05/13 16:31:13 WARN psql command failed tool=get_replication_status ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getLockInfoTool
2026/05/13 16:31:13 WARN psql command failed tool=get_lock_info ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getTableStatsTool
2026/05/13 16:31:13 WARN psql command failed tool=get_table_stats ms=0 err="exit status 1" output="connection refused"
=== RUN   TestToolsErrorHandling/getBgwriterStatsTool
2026/05/13 16:31:13 WARN psql command failed tool=get_bgwriter_stats ms=0 err="exit status 1" output="connection refused"
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
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_status_summary ms=0
--- PASS: TestGetStatusSummaryTool_Success (0.00s)
=== RUN   TestGetStatusSummaryTool_PsqlError
2026/05/13 16:31:13 WARN psql command failed tool=get_status_summary ms=0 err="exit status 1" output="(no output from psql)"
--- PASS: TestGetStatusSummaryTool_PsqlError (0.00s)
=== RUN   TestGetStatusSummaryTool_MalformedOutput
2026/05/13 16:31:13 INFO tool ok name=get_status_summary ms=0
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
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
--- PASS: TestGetSessionInfoTool_Success (0.00s)
=== RUN   TestGetSessionInfoTool_NoPidFound
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
--- PASS: TestGetSessionInfoTool_NoPidFound (0.00s)
=== RUN   TestCancelQueryTool_Success
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=cancel_query ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_Success (0.00s)
=== RUN   TestCancelQueryTool_NoPidFound
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
--- PASS: TestCancelQueryTool_NoPidFound (0.00s)
=== RUN   TestCancelQueryTool_Failure
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=get_session_info ms=0 err="exit status 1" output="connection refused"
--- PASS: TestCancelQueryTool_Failure (0.00s)
=== RUN   TestCancelQueryTool_Level1_ReturnedFalse
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=cancel_query ms=0
--- PASS: TestCancelQueryTool_Level1_ReturnedFalse (0.00s)
=== RUN   TestCancelQueryTool_Level2_StillActive
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=cancel_query ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_Level2_StillActive (0.00s)
=== RUN   TestCancelQueryTool_Level2_ResolvesOnRetry
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=cancel_query ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_Level2_ResolvesOnRetry (0.00s)
=== RUN   TestCancelQueryTool_Level2_ExhaustedWarning
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=cancel_query ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_Level2_ExhaustedWarning (0.00s)
=== RUN   TestTerminateConnectionTool_Success
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=terminate_connection ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestTerminateConnectionTool_Success (0.00s)
=== RUN   TestTerminateConnectionTool_NoPidFound
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
--- PASS: TestTerminateConnectionTool_NoPidFound (0.00s)
=== RUN   TestTerminateConnectionTool_Failure
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=get_session_info ms=0 err="exit status 1" output="connection refused"
--- PASS: TestTerminateConnectionTool_Failure (0.00s)
=== RUN   TestTerminateConnectionTool_Level1_ReturnedFalse
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=terminate_connection ms=0
--- PASS: TestTerminateConnectionTool_Level1_ReturnedFalse (0.00s)
=== RUN   TestTerminateConnectionTool_Level2_PidStillAlive
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=terminate_connection ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestTerminateConnectionTool_Level2_PidStillAlive (0.00s)
=== RUN   TestTerminateConnectionTool_Level2_ResolvesOnRetry
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=terminate_connection ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestTerminateConnectionTool_Level2_ResolvesOnRetry (0.00s)
=== RUN   TestTerminateConnectionTool_Level2_EscalationRequired
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=terminate_connection ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestTerminateConnectionTool_Level2_EscalationRequired (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_TooShortIdle
--- PASS: TestTerminateIdleConnectionsTool_TooShortIdle (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_DryRun_Found
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=terminate_idle_connections ms=0
--- PASS: TestTerminateIdleConnectionsTool_DryRun_Found (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_DryRun_NoneFound
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=terminate_idle_connections ms=0
--- PASS: TestTerminateIdleConnectionsTool_DryRun_NoneFound (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_Success
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=terminate_idle_connections ms=0
--- PASS: TestTerminateIdleConnectionsTool_Success (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_WithDatabaseFilter
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=terminate_idle_connections ms=0
--- PASS: TestTerminateIdleConnectionsTool_WithDatabaseFilter (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_Failure
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=terminate_idle_connections ms=0 err="exit status 1" output="connection refused"
--- PASS: TestTerminateIdleConnectionsTool_Failure (0.00s)
=== RUN   TestCancelQueryTool_PolicyDenied
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestCancelQueryTool_PolicyDenied2479236523/001/db-policies-3733794293.yaml policies=1 dry_run=false default=allow
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN policy decision: DENY action=write resource_type=database resource_name="" effect=deny policy=deny-write message="write operations are not permitted in this test"
2026/05/13 16:31:13 WARN policy denied database access tool=cancel_query database="" action=write tags=[] from_infra_config=false err="Access to database  for write: DENIED\n\nPolicy \"deny-write\" matched:\n  Rule 0   write → deny                  matched\n  → DENIED\n\nReason: write operations are not permitted in this test"
--- PASS: TestCancelQueryTool_PolicyDenied (0.00s)
=== RUN   TestTerminateConnectionTool_PolicyDenied
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateConnectionTool_PolicyDenied443195635/001/db-policies-4233194442.yaml policies=1 dry_run=false default=allow
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=deny-destructive message="destructive operations are not permitted in this test"
2026/05/13 16:31:13 WARN policy denied database access tool=terminate_connection database="" action=destructive tags=[] from_infra_config=false err="Access to database  for destructive: DENIED\n\nPolicy \"deny-destructive\" matched:\n  Rule 0   destructive → deny            matched\n  → DENIED\n\nReason: destructive operations are not permitted in this test"
--- PASS: TestTerminateConnectionTool_PolicyDenied (0.00s)
=== RUN   TestCancelQueryTool_PostExecPolicyChecked
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestCancelQueryTool_PostExecPolicyChecked2569370411/001/db-policies-1096969268.yaml policies=1 dry_run=false default=allow
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=cancel_query ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_PostExecPolicyChecked (0.00s)
=== RUN   TestTerminateConnectionTool_PostExecPolicyChecked
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateConnectionTool_PostExecPolicyChecked2037385729/001/db-policies-4036184721.yaml policies=1 dry_run=false default=allow
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=terminate_connection ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestTerminateConnectionTool_PostExecPolicyChecked (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_BlastRadiusDenied
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateIdleConnectionsTool_BlastRadiusDenied4141357785/001/db-policies-2556880097.yaml policies=1 dry_run=false default=deny
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=terminate_idle_connections ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=db-blast-radius message="Operation affects 20 rows, limit is 5"
2026/05/13 16:31:13 WARN post-execution policy check: blast radius exceeded resource_type=database resource_name="" action=destructive rows_affected=20 pods_affected=0 policy=db-blast-radius message="Operation affects 20 rows, limit is 5"
--- PASS: TestTerminateIdleConnectionsTool_BlastRadiusDenied (0.00s)
=== RUN   TestResolveDatabaseInfo_InfraEnforced_UnknownConnString
--- PASS: TestResolveDatabaseInfo_InfraEnforced_UnknownConnString (0.00s)
=== RUN   TestResolveDatabaseInfo_InfraEnforced_UnknownName
--- PASS: TestResolveDatabaseInfo_InfraEnforced_UnknownName (0.00s)
=== RUN   TestResolveDatabaseInfo_InfraEnforced_RegisteredConnString
--- PASS: TestResolveDatabaseInfo_InfraEnforced_RegisteredConnString (0.00s)
=== RUN   TestResolveDatabaseInfo_InfraPermissive_UnknownConnString
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost dbname=devdb user=dev" known_databases=0
--- PASS: TestResolveDatabaseInfo_InfraPermissive_UnknownConnString (0.00s)
=== RUN   TestResolveDatabaseInfo_PasswordEnv_ByName
2026/05/13 16:31:13 INFO resolved database name to connection string name=secured-db
--- PASS: TestResolveDatabaseInfo_PasswordEnv_ByName (0.00s)
=== RUN   TestResolveDatabaseInfo_PasswordEnv_Missing
2026/05/13 16:31:13 INFO resolved database name to connection string name=secured-db
--- PASS: TestResolveDatabaseInfo_PasswordEnv_Missing (0.00s)
=== RUN   TestCheckConnectionTool_InfraEnforced_Rejected
--- PASS: TestCheckConnectionTool_InfraEnforced_Rejected (0.00s)
=== RUN   TestCancelQueryTool_SessionPlanSentToPolicy
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestCancelQueryTool_SessionPlanSentToPolicy1414588890/001/policy-1313779165.yaml policies=1 dry_run=false default=deny
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO policy decision: REQUIRE_APPROVAL action=write resource_type=database resource_name="" effect=require_approval policy=require-approval-policy
2026/05/13 16:31:13 INFO approval request created approval_id=tool-approval-1 resource=database:
2026/05/13 16:31:13 WARN policy denied database access tool=cancel_query database="" action=write tags=[] from_infra_config=false err="approval required (ID: tool-approval-1) — this operation needs human authorization before it can execute. Ask an approver to run: ./approvals approve tool-approval-1 — then reply here to retry."
--- PASS: TestCancelQueryTool_SessionPlanSentToPolicy (0.00s)
=== RUN   TestTerminateConnectionTool_SessionPlanSentToPolicy
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateConnectionTool_SessionPlanSentToPolicy2743506835/001/policy-3471837667.yaml policies=1 dry_run=false default=deny
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO policy decision: REQUIRE_APPROVAL action=destructive resource_type=database resource_name="" effect=require_approval policy=require-approval-policy
2026/05/13 16:31:13 INFO approval request created approval_id=tool-approval-1 resource=database:
2026/05/13 16:31:13 WARN policy denied database access tool=terminate_connection database="" action=destructive tags=[] from_infra_config=false err="approval required (ID: tool-approval-1) — this operation needs human authorization before it can execute. Ask an approver to run: ./approvals approve tool-approval-1 — then reply here to retry."
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
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestRunPsqlAs_PreExecBlastRadiusDenied4168795811/001/db-policies-1971792322.yaml policies=1 dry_run=false default=deny
2026/05/13 16:31:13 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=db-blast-radius message="Operation affects 50000 rows, limit is 1000"
2026/05/13 16:31:13 WARN post-execution policy check: blast radius exceeded resource_type=database resource_name="" action=destructive rows_affected=50000 pods_affected=0 policy=db-blast-radius message="Operation affects 50000 rows, limit is 1000"
--- PASS: TestRunPsqlAs_PreExecBlastRadiusDenied (0.00s)
=== RUN   TestTerminateConnectionTool_XactAgeGuardrail
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateConnectionTool_XactAgeGuardrail847277338/001/db-policies-2515119751.yaml policies=1 dry_run=false default=allow
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=xact-age-limit message="Transaction has been open for 2h 0m; rollback may take as long. Limit is 30m 0s"
2026/05/13 16:31:13 WARN pre-execution policy check: transaction age exceeded resource_name="" action=destructive xact_age_secs=7200 policy=xact-age-limit message="Transaction has been open for 2h 0m; rollback may take as long. Limit is 30m 0s"
--- PASS: TestTerminateConnectionTool_XactAgeGuardrail (0.00s)
=== RUN   TestCancelQueryTool_XactAgeGuardrail
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestCancelQueryTool_XactAgeGuardrail2672624122/001/db-policies-3702993927.yaml policies=1 dry_run=false default=allow
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN policy decision: DENY action=write resource_type=database resource_name="" effect=deny policy=xact-age-limit message="Transaction has been open for 2h 0m; rollback may take as long. Limit is 30m 0s"
2026/05/13 16:31:13 WARN pre-execution policy check: transaction age exceeded resource_name="" action=write xact_age_secs=7200 policy=xact-age-limit message="Transaction has been open for 2h 0m; rollback may take as long. Limit is 30m 0s"
--- PASS: TestCancelQueryTool_XactAgeGuardrail (0.00s)
=== RUN   TestTerminateConnectionTool_ToolNamePolicyDenied
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateConnectionTool_ToolNamePolicyDenied1541020814/001/db-policies-578557697.yaml policies=1 dry_run=false default=allow
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=deny-tool-terminate_connection message="tool terminate_connection is disabled by policy"
2026/05/13 16:31:13 WARN policy denied database access tool=terminate_connection database="" action=destructive tags=[] from_infra_config=false err="Access to database  for destructive: DENIED\n\nPolicy \"deny-tool-terminate_connection\" matched:\n  Rule 0   read|write|destructive → deny  matched\n  → DENIED\n\nReason: tool terminate_connection is disabled by policy"
--- PASS: TestTerminateConnectionTool_ToolNamePolicyDenied (0.00s)
=== RUN   TestCancelQueryTool_ToolNamePolicyAllows_WhenOtherToolDenied
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestCancelQueryTool_ToolNamePolicyAllows_WhenOtherToolDenied1619204983/001/db-policies-3864439915.yaml policies=1 dry_run=false default=allow
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_session_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=cancel_query ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestCancelQueryTool_ToolNamePolicyAllows_WhenOtherToolDenied (0.00s)
=== RUN   TestTerminateIdleConnectionsTool_ToolPatternPolicyDenied
2026/05/13 16:31:13 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestTerminateIdleConnectionsTool_ToolPatternPolicyDenied1387542999/001/db-policies-783207032.yaml policies=1 dry_run=false default=allow
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN policy decision: DENY action=destructive resource_type=database resource_name="" effect=deny policy=deny-tool-pattern message="tool matching terminate_* is disabled by policy"
2026/05/13 16:31:13 WARN policy denied database access tool=terminate_idle_connections database="" action=destructive tags=[] from_infra_config=false err="Access to database  for destructive: DENIED\n\nPolicy \"deny-tool-pattern\" matched:\n  Rule 0   read|write|destructive → deny  matched\n  → DENIED\n\nReason: tool matching terminate_* is disabled by policy"
--- PASS: TestTerminateIdleConnectionsTool_ToolPatternPolicyDenied (0.00s)
=== RUN   TestGetPgSettingsTool_NonDefaultSettings
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_pg_settings ms=0
--- PASS: TestGetPgSettingsTool_NonDefaultSettings (0.00s)
=== RUN   TestGetPgSettingsTool_AllDefaultsReturnsMessage
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_pg_settings ms=0
--- PASS: TestGetPgSettingsTool_AllDefaultsReturnsMessage (0.00s)
=== RUN   TestGetPgSettingsTool_CategoryFilter
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_pg_settings ms=0
--- PASS: TestGetPgSettingsTool_CategoryFilter (0.00s)
=== RUN   TestGetExtensionsTool_WithExtensions
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_extensions ms=0
--- PASS: TestGetExtensionsTool_WithExtensions (0.00s)
=== RUN   TestGetExtensionsTool_NoExtensions
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_extensions ms=0
--- PASS: TestGetExtensionsTool_NoExtensions (0.00s)
=== RUN   TestGetBaselineTool_Success
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_server_info ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_pg_settings ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_extensions ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_disk_usage ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_disk_usage ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestGetBaselineTool_Success (0.00s)
=== RUN   TestGetBaselineTool_PartialFailure
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=get_server_info ms=0 err="connection refused" output="(no output from psql)"
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=get_pg_settings ms=0 err="connection refused" output="(no output from psql)"
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=get_extensions ms=0 err="connection refused" output="(no output from psql)"
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=get_disk_usage ms=0 err="connection refused" output="(no output from psql)"
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
--- PASS: TestGetBaselineTool_PartialFailure (0.00s)
=== RUN   TestGetSlowQueriesTool_WithResults
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_slow_queries ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_slow_queries ms=0
--- PASS: TestGetSlowQueriesTool_WithResults (0.00s)
=== RUN   TestGetSlowQueriesTool_ExtensionMissing
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_slow_queries ms=0
--- PASS: TestGetSlowQueriesTool_ExtensionMissing (0.00s)
=== RUN   TestGetSlowQueriesTool_NoResults
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_slow_queries ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_slow_queries ms=0
--- PASS: TestGetSlowQueriesTool_NoResults (0.00s)
=== RUN   TestGetVacuumStatusTool_WithResults
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_vacuum_status ms=0
--- PASS: TestGetVacuumStatusTool_WithResults (0.00s)
=== RUN   TestGetVacuumStatusTool_NoResults
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_vacuum_status ms=0
--- PASS: TestGetVacuumStatusTool_NoResults (0.00s)
=== RUN   TestGetDiskUsageTool_WithResults
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_disk_usage ms=0
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_disk_usage ms=0
--- PASS: TestGetDiskUsageTool_WithResults (0.00s)
=== RUN   TestGetWaitEventsTool_WithEvents
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_wait_events ms=0
--- PASS: TestGetWaitEventsTool_WithEvents (0.00s)
=== RUN   TestGetWaitEventsTool_NoWaits
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_wait_events ms=0
--- PASS: TestGetWaitEventsTool_NoWaits (0.00s)
=== RUN   TestGetBgwriterStatsTool_Success
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_bgwriter_stats ms=0
--- PASS: TestGetBgwriterStatsTool_Success (0.00s)
=== RUN   TestGetBlockingQueriesTool_WithBlocks
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_blocking_queries ms=0
--- PASS: TestGetBlockingQueriesTool_WithBlocks (0.00s)
=== RUN   TestGetBlockingQueriesTool_NoBlocking
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=get_blocking_queries ms=0
--- PASS: TestGetBlockingQueriesTool_NoBlocking (0.00s)
=== RUN   TestExplainQueryTool_SelectQuery
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=explain_query ms=0
--- PASS: TestExplainQueryTool_SelectQuery (0.00s)
=== RUN   TestExplainQueryTool_DMLRejectedByDefault
--- PASS: TestExplainQueryTool_DMLRejectedByDefault (0.00s)
=== RUN   TestExplainQueryTool_DMLAllowedWithFlag
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=explain_query ms=0
--- PASS: TestExplainQueryTool_DMLAllowedWithFlag (0.00s)
=== RUN   TestExplainQueryTool_EmptyQuery
--- PASS: TestExplainQueryTool_EmptyQuery (0.00s)
=== RUN   TestExplainQueryTool_DMLWrappedInTransaction
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=explain_query ms=0
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
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=read_pg_log ms=0
--- PASS: TestGetPgLogTool_ReturnsLastNLines (0.00s)
=== RUN   TestGetPgLogTool_WithFilter
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 INFO tool ok name=read_pg_log ms=0
--- PASS: TestGetPgLogTool_WithFilter (0.00s)
=== RUN   TestGetPgLogTool_FilterCaseInsensitive
2026/05/13 16:31:13 INFO tool ok name=read_pg_log ms=0
--- PASS: TestGetPgLogTool_FilterCaseInsensitive (0.00s)
=== RUN   TestGetPgLogTool_EmptyLog
2026/05/13 16:31:13 INFO tool ok name=read_pg_log ms=0
--- PASS: TestGetPgLogTool_EmptyLog (0.00s)
=== RUN   TestGetPgLogTool_FilterNoMatch
2026/05/13 16:31:13 INFO tool ok name=read_pg_log ms=0
--- PASS: TestGetPgLogTool_FilterNoMatch (0.00s)
=== RUN   TestGetPgLogTool_ConnectionError
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=read_pg_log ms=0 err="exit status 1" output="connection refused"
--- PASS: TestGetPgLogTool_ConnectionError (0.00s)
=== RUN   TestGetPgLogTool_PermissionDeniedPgLsLogdir
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=pg-cluster-minkube" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=read_pg_log ms=0 err="ERROR:  permission denied for function pg_ls_logdir\nexit status 1" output="(no output from psql)"
--- PASS: TestGetPgLogTool_PermissionDeniedPgLsLogdir (0.00s)
=== RUN   TestGetPgLogTool_LoggingCollectorDisabled
2026/05/13 16:31:13 WARN connection string not found in infraConfig; policy will evaluate with no tags connection_string="host=localhost" known_databases=0
2026/05/13 16:31:13 WARN psql command failed tool=read_pg_log ms=0 err="ERROR:  could not open directory \"log\": No such file or directory\nexit status 1" output="(no output from psql)"
--- PASS: TestGetPgLogTool_LoggingCollectorDisabled (0.00s)
=== RUN   TestRunPsqlTuples_UsesTupleFlags
2026/05/13 16:31:13 INFO tool ok name=test_tool ms=0
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
ok  	helpdesk/agents/database	41.168s
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
2026/05/13 16:30:35 INFO tool ok name=describe_service ms=0
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
2026/05/13 16:30:35 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_Success (0.00s)
=== RUN   TestDeletePodTool_WithGracePeriod
2026/05/13 16:30:35 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_WithGracePeriod (0.00s)
=== RUN   TestDeletePodTool_Failure
2026/05/13 16:30:35 ERROR kubectl command failed tool=delete_pod args="[delete pod bad-pod -n default]" ms=0 err="Error from server (NotFound): pods \"bad-pod\" not found"
--- PASS: TestDeletePodTool_Failure (0.00s)
=== RUN   TestDeletePodTool_VerificationWarning_PodStillTerminating
2026/05/13 16:30:35 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_VerificationWarning_PodStillTerminating (0.00s)
=== RUN   TestDeletePodTool_PolicyDenied
2026/05/13 16:30:35 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestDeletePodTool_PolicyDenied2771808898/001/k8s-policies-3804792152.yaml policies=1 dry_run=false default=allow
2026/05/13 16:30:35 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=production effect=deny policy=deny-k8s-destructive message="destructive kubernetes operations are not permitted in this test"
--- PASS: TestDeletePodTool_PolicyDenied (0.00s)
=== RUN   TestRestartDeploymentTool_Success
2026/05/13 16:30:35 INFO tool ok name=restart_deployment ms=0
--- PASS: TestRestartDeploymentTool_Success (0.00s)
=== RUN   TestRestartDeploymentTool_Failure
2026/05/13 16:30:35 ERROR kubectl command failed tool=restart_deployment args="[rollout restart deployment missing -n default]" ms=0 err="Error from server (NotFound): deployments \"missing\" not found"
--- PASS: TestRestartDeploymentTool_Failure (0.00s)
=== RUN   TestRestartDeploymentTool_VerificationWarning_AnnotationMissing
2026/05/13 16:30:35 INFO tool ok name=restart_deployment ms=0
--- PASS: TestRestartDeploymentTool_VerificationWarning_AnnotationMissing (0.00s)
=== RUN   TestRestartDeploymentTool_PolicyDenied
2026/05/13 16:30:35 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestRestartDeploymentTool_PolicyDenied3870660253/001/k8s-policies-2462269678.yaml policies=1 dry_run=false default=allow
2026/05/13 16:30:35 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=production effect=deny policy=deny-k8s-destructive message="destructive kubernetes operations are not permitted in this test"
--- PASS: TestRestartDeploymentTool_PolicyDenied (0.00s)
=== RUN   TestScaleDeploymentTool_Success
2026/05/13 16:30:35 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_Success (0.00s)
=== RUN   TestScaleDeploymentTool_ScaleToZero
2026/05/13 16:30:35 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_ScaleToZero (0.00s)
=== RUN   TestScaleDeploymentTool_CapturesPreState
2026/05/13 16:30:35 INFO sqlite journal mode mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestScaleDeploymentTool_CapturesPreState620041753/001/k8s_prestate_test.db
2026/05/13 16:30:35 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_CapturesPreState (0.01s)
=== RUN   TestScaleDeploymentTool_PreStateReadFailure_ToolStillRuns
2026/05/13 16:30:35 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_PreStateReadFailure_ToolStillRuns (0.00s)
=== RUN   TestScaleDeploymentTool_Failure
2026/05/13 16:30:35 ERROR kubectl command failed tool=scale_deployment args="[scale deployment ghost --replicas 3 -n default]" ms=0 err="Error from server (NotFound): deployments \"ghost\" not found"
--- PASS: TestScaleDeploymentTool_Failure (0.00s)
=== RUN   TestScaleDeploymentTool_VerificationFailed_WrongReplicas
2026/05/13 16:30:35 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_VerificationFailed_WrongReplicas (0.00s)
=== RUN   TestScaleDeploymentTool_PolicyDenied
2026/05/13 16:30:35 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestScaleDeploymentTool_PolicyDenied3067014813/001/k8s-policies-3215537754.yaml policies=1 dry_run=false default=allow
2026/05/13 16:30:35 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=production effect=deny policy=deny-k8s-destructive message="destructive kubernetes operations are not permitted in this test"
--- PASS: TestScaleDeploymentTool_PolicyDenied (0.00s)
=== RUN   TestDeletePodTool_VerificationWarning_ResolvesOnRetry
2026/05/13 16:30:35 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_VerificationWarning_ResolvesOnRetry (0.00s)
=== RUN   TestDeletePodTool_VerificationWarning_ExhaustedEscalation
2026/05/13 16:30:35 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_VerificationWarning_ExhaustedEscalation (0.00s)
=== RUN   TestRestartDeploymentTool_VerificationWarning_ResolvesOnRetry
2026/05/13 16:30:35 INFO tool ok name=restart_deployment ms=0
--- PASS: TestRestartDeploymentTool_VerificationWarning_ResolvesOnRetry (0.00s)
=== RUN   TestScaleDeploymentTool_Level2_RetryApplySucceeds
2026/05/13 16:30:35 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_Level2_RetryApplySucceeds (0.00s)
=== RUN   TestScaleDeploymentTool_Level2_RetryApplyFails
2026/05/13 16:30:35 INFO tool ok name=scale_deployment ms=0
--- PASS: TestScaleDeploymentTool_Level2_RetryApplyFails (0.00s)
=== RUN   TestDeletePodTool_BlastRadiusAllowed
2026/05/13 16:30:35 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestDeletePodTool_BlastRadiusAllowed3096227939/001/k8s-policies-2090574709.yaml policies=1 dry_run=false default=deny
2026/05/13 16:30:35 INFO tool ok name=delete_pod ms=0
--- PASS: TestDeletePodTool_BlastRadiusAllowed (0.00s)
=== RUN   TestDeletePodTool_BlastRadiusDenied
2026/05/13 16:30:35 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestDeletePodTool_BlastRadiusDenied1627064267/001/k8s-policies-2150426356.yaml policies=1 dry_run=false default=deny
2026/05/13 16:30:35 INFO tool ok name=delete_pod ms=0
2026/05/13 16:30:35 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=default effect=deny policy=k8s-blast-radius message="Operation affects 3 pods, limit is 1"
2026/05/13 16:30:35 WARN post-execution policy check: blast radius exceeded resource_type=kubernetes resource_name=default action=destructive rows_affected=0 pods_affected=3 policy=k8s-blast-radius message="Operation affects 3 pods, limit is 1"
--- PASS: TestDeletePodTool_BlastRadiusDenied (0.00s)
=== RUN   TestScaleDeploymentTool_BlastRadiusDenied_PreExec
2026/05/13 16:30:35 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestScaleDeploymentTool_BlastRadiusDenied_PreExec2174964290/001/k8s-policies-3241258270.yaml policies=1 dry_run=false default=deny
2026/05/13 16:30:35 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=default effect=deny policy=k8s-blast-radius message="Operation affects 20 pods, limit is 5"
2026/05/13 16:30:35 WARN post-execution policy check: blast radius exceeded resource_type=kubernetes resource_name=default action=destructive rows_affected=0 pods_affected=20 policy=k8s-blast-radius message="Operation affects 20 pods, limit is 5"
--- PASS: TestScaleDeploymentTool_BlastRadiusDenied_PreExec (0.00s)
=== RUN   TestRestartDeploymentTool_BlastRadiusEnforced_PostExec
2026/05/13 16:30:35 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestRestartDeploymentTool_BlastRadiusEnforced_PostExec872138193/001/k8s-policies-2602019301.yaml policies=1 dry_run=false default=deny
2026/05/13 16:30:35 INFO tool ok name=restart_deployment ms=0
2026/05/13 16:30:35 WARN policy decision: DENY action=destructive resource_type=kubernetes resource_name=default effect=deny policy=k8s-blast-radius message="Operation affects 2 pods, limit is 1"
2026/05/13 16:30:35 WARN post-execution policy check: blast radius exceeded resource_type=kubernetes resource_name=default action=destructive rows_affected=0 pods_affected=2 policy=k8s-blast-radius message="Operation affects 2 pods, limit is 1"
--- PASS: TestRestartDeploymentTool_BlastRadiusEnforced_PostExec (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraEnforced_UnknownNamespaceUnknownContext
--- PASS: TestResolveNamespaceInfo_InfraEnforced_UnknownNamespaceUnknownContext (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraEnforced_RegisteredByDBName
2026/05/13 16:30:35 INFO resolved database name to namespace name=prod-db namespace=prod-namespace
--- PASS: TestResolveNamespaceInfo_InfraEnforced_RegisteredByDBName (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraEnforced_RegisteredByNamespace
--- PASS: TestResolveNamespaceInfo_InfraEnforced_RegisteredByNamespace (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraEnforced_NonDBNamespaceInKnownCluster
2026/05/13 16:30:35 INFO resolved namespace tags from cluster namespace=default context=gke_prod tags=[production]
--- PASS: TestResolveNamespaceInfo_InfraEnforced_NonDBNamespaceInKnownCluster (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraEnforced_NonDBNamespaceNoContextSoleCluster
2026/05/13 16:30:35 INFO resolved namespace tags from sole cluster namespace=default context=gke_prod tags=[production]
--- PASS: TestResolveNamespaceInfo_InfraEnforced_NonDBNamespaceNoContextSoleCluster (0.00s)
=== RUN   TestResolveNamespaceInfo_InfraPermissive_UnknownNamespace
--- PASS: TestResolveNamespaceInfo_InfraPermissive_UnknownNamespace (0.00s)
=== RUN   TestGetPodsTool_InfraEnforced_Rejected
--- PASS: TestGetPodsTool_InfraEnforced_Rejected (0.00s)
=== RUN   TestGetPodResources_RequestsLimitsOnly
2026/05/13 16:30:35 INFO tool ok name=get_pod_resources count=1 ms=0
--- PASS: TestGetPodResources_RequestsLimitsOnly (0.00s)
=== RUN   TestGetPodResources_WithLiveUsage
2026/05/13 16:30:35 INFO tool ok name=get_pod_resources count=1 ms=0
--- PASS: TestGetPodResources_WithLiveUsage (0.00s)
=== RUN   TestGetPodResources_PolicyDenied
2026/05/13 16:30:35 INFO policy enforcement enabled file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGetPodResources_PolicyDenied726076513/001/k8s-policies-1545699161.yaml policies=1 dry_run=false default=allow
2026/05/13 16:30:35 INFO tool ok name=get_pod_resources count=0 ms=0
--- PASS: TestGetPodResources_PolicyDenied (0.00s)
=== RUN   TestGetNodeStatus_AllNodes
2026/05/13 16:30:35 INFO tool ok name=get_node_status count=1 ms=0
--- PASS: TestGetNodeStatus_AllNodes (0.00s)
=== RUN   TestGetNodeStatus_SingleNode
2026/05/13 16:30:35 INFO tool ok name=get_node_status count=1 ms=0
--- PASS: TestGetNodeStatus_SingleNode (0.00s)
=== RUN   TestGetNodeStatus_MemoryPressure_IncludesMessage
2026/05/13 16:30:35 INFO tool ok name=get_node_status count=1 ms=0
--- PASS: TestGetNodeStatus_MemoryPressure_IncludesMessage (0.00s)
PASS
ok  	helpdesk/agents/k8s	0.896s
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
2026/05/13 16:30:34 INFO restart_container succeeded target=prod_db container=alloydb-omni reason="crash loop detected"
--- PASS: TestRestartContainer_Success (0.00s)
=== RUN   TestRestartContainer_WrongType
--- PASS: TestRestartContainer_WrongType (0.00s)
=== RUN   TestRestartService_Success
2026/05/13 16:30:34 INFO restart_service succeeded target=prod_db unit=postgresql-16 reason="configuration change applied"
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
ok  	helpdesk/agents/sysadmin	0.324s

=== Integration Test Summary ===
  Total:  340
  Passed: 340
  Failed: 0
Stopping test infrastructure...
docker compose -f testing/docker/docker-compose.yaml down -v
[+] Running 4/4
 ✔ Container helpdesk-test-pgloader  Removed                                                                                                                                                                                                10.1s
 ✔ Container helpdesk-test-pg        Removed                                                                                                                                                                                                 0.2s
 ✔ Volume docker_pgdata              Removed                                                                                                                                                                                                 0.0s
 ✔ Network docker_default            Removed                                                                                                                                                                                                 0.1s
```

