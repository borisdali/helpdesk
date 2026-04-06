# aiHelpDesk: Integration Testing: Sample Run

This is a sample run of aiHelpDesk Integration test.
See the overall aiHelpDesk Testing approach [here](README.md).

```
[boris@ ~/helpdesk]$ make integration
Starting test infrastructure...
docker compose -f testing/docker/docker-compose.yaml up -d --wait
[+] Running 4/4
 ✔ Network docker_default            Created                                                                                                                                                                                                 0.0s
 ✔ Volume "docker_pgdata"            Created                                                                                                                                                                                                 0.0s
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 6.5s
 ✔ Container helpdesk-test-pgloader  Healthy                                                                                                                                                                                                 6.4s
Running integration tests...
go test -tags integration -timeout 120s -v ./testing/integration/...
=== RUN   TestDockerExec_SimpleCommand
--- PASS: TestDockerExec_SimpleCommand (0.06s)
=== RUN   TestDockerExec_PostgresVersion
--- PASS: TestDockerExec_PostgresVersion (0.06s)
=== RUN   TestDockerExec_NonexistentContainer
--- PASS: TestDockerExec_NonexistentContainer (0.02s)
=== RUN   TestDockerCompose_Ps
--- PASS: TestDockerCompose_Ps (0.11s)
=== RUN   TestRunSQLStringViaPgloader_Success
--- PASS: TestRunSQLStringViaPgloader_Success (0.07s)
=== RUN   TestRunSQLStringViaPgloader_Query
--- PASS: TestRunSQLStringViaPgloader_Query (0.15s)
=== RUN   TestDockerCompose_StopStartService
    docker_test.go:123: DockerComposeStop and DockerComposeStart helpers are available
--- PASS: TestDockerCompose_StopStartService (0.02s)
=== RUN   TestConnection_Success
--- PASS: TestConnection_Success (0.02s)
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
--- PASS: TestTableStats_CreateAndQuery (0.06s)
=== RUN   TestQuery_ContextCancellation
--- PASS: TestQuery_ContextCancellation (0.10s)
=== RUN   TestQuery_ExtendedFormat
--- PASS: TestQuery_ExtendedFormat (0.03s)
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
ok  	helpdesk/testing/integration	1.389s
time=2026-03-24T15:27:48.917-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/auditd-integration-1629561101/audit.db
time=2026-03-24T15:27:48.928-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:27:48.928-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-03-24T15:27:48.929-04:00 level=INFO msg="audit service starting" version=dev listen=:19901 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/auditd-integration-1629561101/audit.db backend=sqlite socket=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/auditd-integration-1629561101/audit.sock
=== RUN   TestAuditorHTTPPollingMode
time=2026-03-24T15:27:48.941-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestAuditorHTTPPollingMode3626244391/001/audit.db
time=2026-03-24T15:27:48.952-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:27:48.952-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-03-24T15:27:48.953-04:00 level=INFO msg="audit service starting" version=dev listen=:19910 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestAuditorHTTPPollingMode3626244391/001/audit.db backend=sqlite socket=/tmp/audit_mon_19910.sock
time=2026-03-24T15:27:49.405-04:00 level=INFO msg="starting auditor" socket=audit.sock log_all=false
time=2026-03-24T15:27:49.405-04:00 level=INFO msg="webhook notifier enabled" url=http://127.0.0.1:63588
time=2026-03-24T15:27:49.405-04:00 level=INFO msg="notifiers configured" count=1
time=2026-03-24T15:27:49.405-04:00 level=INFO msg="audit socket not available; switching to HTTP polling mode" socket=audit.sock url=http://localhost:19910
time=2026-03-24T15:27:49.409-04:00 level=INFO msg="polling for new events" interval=5s url=http://localhost:19910
    audit_monitor_test.go:183: webhook received: level=INFO message="Delegation to  (0% confidence)"

🚨 [AUDIT CRITICAL] DESTRUCTIVE operation detected
time=2026-03-24T15:27:59.414-04:00 level=ERROR msg="[AUDIT CRITICAL] DESTRUCTIVE operation detected" event_id=evt_test_1774380475036696000 session_id=sess_monitor_test user_id=testuser action_class=destructive trace_id=""
    audit_monitor_test.go:183: webhook received: level=CRITICAL message="DESTRUCTIVE operation detected"
--- PASS: TestAuditorHTTPPollingMode (10.49s)
=== RUN   TestSecbotHTTPPollingMode
time=2026-03-24T15:27:59.445-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingMode6415134/001/audit.db
time=2026-03-24T15:27:59.460-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:27:59.460-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-03-24T15:27:59.461-04:00 level=INFO msg="audit service starting" version=dev listen=:19911 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingMode6415134/001/audit.db backend=sqlite socket=/tmp/audit_mon_19911.sock
    audit_monitor_test.go:221: secbot output:

        [15:27:59] ── Phase 1: Startup ──────────────────────────────────
        [15:27:59] Audit service: http://localhost:19911 (HTTP polling)
        [15:27:59] Gateway:       http://127.0.0.1:19999
        [15:27:59] Callback:      127.0.0.1:63599
        [15:27:59] Cooldown:      5m0s
        [15:27:59] Max events/min: 100
        [15:27:59] Dry run:       true


        [15:27:59] ── Phase 2: Connect to Audit Stream ──────────────────


        [15:27:59] ── Phase 3: Monitoring for Security Events ───────────
        [15:27:59] Watching for: high_volume, hash_mismatch, unauthorized_destructive, potential_sql_injection, potential_command_injection

        [15:27:59] Baseline: 0 existing events (not re-analyzed)
        [15:27:59] Polling audit service for new events every 5s
        [15:28:09] EVENT #1: evt_test_1774380485533419000 (type=tool_call)
        [15:28:09] SECURITY ALERT: unauthorized_destructive
        [15:28:09]   Event ID:  evt_test_1774380485533419000
        [15:28:09]   Trace ID:
        [15:28:09]   Time:      2026-03-24T19:28:05Z
        [15:28:09]   Tool:      delete_database
        [15:28:09]   Agent:     database-agent
        [15:28:09]   [DRY RUN] Would create incident bundle

--- PASS: TestSecbotHTTPPollingMode (18.13s)
=== RUN   TestSecbotHTTPPollingReconnect
time=2026-03-24T15:28:17.575-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect2590792723/001/audit.db
time=2026-03-24T15:28:17.590-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:17.590-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-03-24T15:28:17.591-04:00 level=INFO msg="audit service starting" version=dev listen=:19912 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect2590792723/001/audit.db backend=sqlite socket=/tmp/audit_mon_19912.sock
    audit_monitor_test.go:275: posting first event (before restart)...
    audit_monitor_test.go:280: restarting auditd...
time=2026-03-24T15:28:32.709-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect2590792723/001/audit.db
time=2026-03-24T15:28:32.710-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:32.710-04:00 level=INFO msg="policy engine disabled (governance/check returns 503)" HELPDESK_POLICY_ENABLED="" HELPDESK_POLICY_FILE=""
time=2026-03-24T15:28:32.711-04:00 level=INFO msg="audit service starting" version=dev listen=:19912 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestSecbotHTTPPollingReconnect2590792723/001/audit.db backend=sqlite socket=/tmp/audit_mon_19912.sock
    audit_monitor_test.go:289: posting second event (after restart)...
    audit_monitor_test.go:294: secbot output:

        [15:28:17] ── Phase 1: Startup ──────────────────────────────────
        [15:28:17] Audit service: http://localhost:19912 (HTTP polling)
        [15:28:17] Gateway:       http://127.0.0.1:19999
        [15:28:17] Callback:      127.0.0.1:63620
        [15:28:17] Cooldown:      5m0s
        [15:28:17] Max events/min: 100
        [15:28:17] Dry run:       true


        [15:28:17] ── Phase 2: Connect to Audit Stream ──────────────────


        [15:28:17] ── Phase 3: Monitoring for Security Events ───────────
        [15:28:17] Watching for: high_volume, hash_mismatch, unauthorized_destructive, potential_sql_injection, potential_command_injection

        [15:28:17] Baseline: 0 existing events (not re-analyzed)
        [15:28:17] Polling audit service for new events every 5s
        [15:28:27] EVENT #1: evt_test_1774380503664399000 (type=tool_call)
        [15:28:27] SECURITY ALERT: unauthorized_destructive
        [15:28:27]   Event ID:  evt_test_1774380503664399000
        [15:28:27]   Trace ID:
        [15:28:27]   Time:      2026-03-24T19:28:23Z
        [15:28:27]   Tool:      delete_database
        [15:28:27]   Agent:     database-agent
        [15:28:27]   [DRY RUN] Would create incident bundle

        [15:28:32] WARN: HTTP poll failed: Get "http://localhost:19912/v1/events?limit=200&since=2026-03-24T19:28:23Z": dial tcp [::1]:19912: connect: connection refused
        [15:28:37] EVENT #2: evt_test_1774380512787350000 (type=tool_call)
        [15:28:37] SECURITY ALERT: unauthorized_destructive
        [15:28:37]   Event ID:  evt_test_1774380512787350000
        [15:28:37]   Trace ID:
        [15:28:37]   Time:      2026-03-24T19:28:32Z
        [15:28:37]   Tool:      delete_database
        [15:28:37]   Agent:     database-agent
        [15:28:37]   [DRY RUN] Would create incident bundle

--- PASS: TestSecbotHTTPPollingReconnect (27.25s)
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
time=2026-03-24T15:28:44.828-04:00 level=INFO msg="approval request created" approval_id=apr_ddbb4f3b action_class=write tool=execute_sql agent=database-agent requested_by=alice
--- PASS: TestApprovals_CreateAndGet (0.00s)
=== RUN   TestApprovals_ListPending
time=2026-03-24T15:28:44.829-04:00 level=INFO msg="approval request created" approval_id=apr_9f2b243f action_class=destructive tool=drop_table agent=database-agent requested_by=bob
--- PASS: TestApprovals_ListPending (0.00s)
=== RUN   TestApprovals_Approve
time=2026-03-24T15:28:44.830-04:00 level=INFO msg="approval request created" approval_id=apr_ecc348b8 action_class=write tool=update_config agent=k8s-agent requested_by=carol
time=2026-03-24T15:28:44.831-04:00 level=INFO msg="approval granted" approval_id=apr_ecc348b8 approved_by=manager valid_for=0s
--- PASS: TestApprovals_Approve (0.00s)
=== RUN   TestApprovals_Deny
time=2026-03-24T15:28:44.833-04:00 level=INFO msg="approval request created" approval_id=apr_ae1d58b3 action_class=destructive tool=delete_namespace agent=k8s-agent requested_by=dave
time=2026-03-24T15:28:44.833-04:00 level=INFO msg="approval denied" approval_id=apr_ae1d58b3 denied_by=admin
--- PASS: TestApprovals_Deny (0.00s)
=== RUN   TestApprovals_Cancel
time=2026-03-24T15:28:44.834-04:00 level=INFO msg="approval request created" approval_id=apr_d59c191b action_class=write tool=scale_deployment agent=k8s-agent requested_by=eve
time=2026-03-24T15:28:44.835-04:00 level=INFO msg="approval cancelled" approval_id=apr_d59c191b
--- PASS: TestApprovals_Cancel (0.00s)
=== RUN   TestApprovals_FilterByStatus
time=2026-03-24T15:28:44.837-04:00 level=INFO msg="approval request created" approval_id=apr_0b9a3aab action_class=write tool=patch_service agent=k8s-agent requested_by=frank
time=2026-03-24T15:28:44.838-04:00 level=INFO msg="approval granted" approval_id=apr_0b9a3aab approved_by=lead valid_for=0s
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
time=2026-03-24T15:28:44.863-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_InfoWithPolicyEnabled2393793278/002/audit.db
time=2026-03-24T15:28:44.875-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:44.875-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_InfoWithPolicyEnabled2393793278/001/policies.yaml policies=1
time=2026-03-24T15:28:44.875-04:00 level=INFO msg="audit service starting" version=dev listen=:19902 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_InfoWithPolicyEnabled2393793278/002/audit.db backend=sqlite socket=/tmp/atest-842579000.sock
--- PASS: TestGovernance_InfoWithPolicyEnabled (0.11s)
=== RUN   TestIntegration_AgentReasoningRoundTrip
--- PASS: TestIntegration_AgentReasoningRoundTrip (0.00s)
=== RUN   TestGovernance_PoliciesSummaryWithEngine
time=2026-03-24T15:28:44.963-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_PoliciesSummaryWithEngine1770311707/002/audit.db
time=2026-03-24T15:28:44.974-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:44.975-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_PoliciesSummaryWithEngine1770311707/001/policies.yaml policies=1
time=2026-03-24T15:28:44.975-04:00 level=INFO msg="audit service starting" version=dev listen=:19902 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_PoliciesSummaryWithEngine1770311707/002/audit.db backend=sqlite socket=/tmp/atest-951770000.sock
--- PASS: TestGovernance_PoliciesSummaryWithEngine (0.11s)
=== RUN   TestGovernance_Explain_DefaultConfig
time=2026-03-24T15:28:45.080-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_Explain_DefaultConfig4253184970/002/audit.db
time=2026-03-24T15:28:45.094-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:45.094-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_Explain_DefaultConfig4253184970/001/policies.yaml policies=1
time=2026-03-24T15:28:45.094-04:00 level=INFO msg="audit service starting" version=dev listen=:19903 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestGovernance_Explain_DefaultConfig4253184970/002/audit.db backend=sqlite socket=/tmp/atest3-61425000.sock
time=2026-03-24T15:28:45.170-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=db-policy message="writes require approval"
--- PASS: TestGovernance_Explain_DefaultConfig (0.11s)
=== RUN   TestAudit_WriteToolEvent_RecordedAndQueryable
--- PASS: TestAudit_WriteToolEvent_RecordedAndQueryable (0.00s)
=== RUN   TestAudit_DestructiveToolEvent_RecordedAndQueryable
--- PASS: TestAudit_DestructiveToolEvent_RecordedAndQueryable (0.00s)
=== RUN   TestAudit_MultipleToolEvents_HashChainValid
--- PASS: TestAudit_MultipleToolEvents_HashChainValid (0.01s)
=== RUN   TestAudit_WriteApprovalWorkflow_ForNewTools
time=2026-03-24T15:28:45.187-04:00 level=INFO msg="approval request created" approval_id=apr_23968eb1 action_class=write tool=cancel_query agent=postgres-agent requested_by=operator
time=2026-03-24T15:28:45.189-04:00 level=INFO msg="approval granted" approval_id=apr_23968eb1 approved_by=senior-dba valid_for=0s
--- PASS: TestAudit_WriteApprovalWorkflow_ForNewTools (0.00s)
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/terminate_connection
time=2026-03-24T15:28:45.191-04:00 level=INFO msg="approval request created" approval_id=apr_684d090d action_class=destructive tool=terminate_connection agent=test-agent requested_by=sre-oncall
time=2026-03-24T15:28:45.193-04:00 level=INFO msg="approval denied" approval_id=apr_684d090d denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/terminate_idle_connections
time=2026-03-24T15:28:45.195-04:00 level=INFO msg="approval request created" approval_id=apr_cf760edd action_class=destructive tool=terminate_idle_connections agent=test-agent requested_by=sre-oncall
time=2026-03-24T15:28:45.197-04:00 level=INFO msg="approval denied" approval_id=apr_cf760edd denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/delete_pod
time=2026-03-24T15:28:45.200-04:00 level=INFO msg="approval request created" approval_id=apr_a664b8da action_class=destructive tool=delete_pod agent=test-agent requested_by=sre-oncall
time=2026-03-24T15:28:45.201-04:00 level=INFO msg="approval denied" approval_id=apr_a664b8da denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/restart_deployment
time=2026-03-24T15:28:45.203-04:00 level=INFO msg="approval request created" approval_id=apr_5e594f30 action_class=destructive tool=restart_deployment agent=test-agent requested_by=sre-oncall
time=2026-03-24T15:28:45.204-04:00 level=INFO msg="approval denied" approval_id=apr_5e594f30 denied_by=change-manager
=== RUN   TestAudit_DestructiveApprovalWorkflow_ForNewTools/scale_deployment
time=2026-03-24T15:28:45.205-04:00 level=INFO msg="approval request created" approval_id=apr_497dd196 action_class=destructive tool=scale_deployment agent=test-agent requested_by=sre-oncall
time=2026-03-24T15:28:45.206-04:00 level=INFO msg="approval denied" approval_id=apr_497dd196 denied_by=change-manager
--- PASS: TestAudit_DestructiveApprovalWorkflow_ForNewTools (0.02s)
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
time=2026-03-24T15:28:45.233-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_UserPrincipal_FlowsThroughToAuditEvent2100616255/002/audit.db
time=2026-03-24T15:28:45.247-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:45.248-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_UserPrincipal_FlowsThroughToAuditEvent2100616255/001/policies.yaml policies=4
time=2026-03-24T15:28:45.248-04:00 level=INFO msg="audit service starting" version=dev listen=:63665 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_UserPrincipal_FlowsThroughToAuditEvent2100616255/002/audit.db backend=sqlite socket=/tmp/aidtest-219254000.sock
--- PASS: TestIdentity_UserPrincipal_FlowsThroughToAuditEvent (0.11s)
=== RUN   TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent
time=2026-03-24T15:28:45.338-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent4163002140/002/audit.db
time=2026-03-24T15:28:45.348-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:45.348-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent4163002140/001/policies.yaml policies=4
time=2026-03-24T15:28:45.349-04:00 level=INFO msg="audit service starting" version=dev listen=:63672 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent4163002140/002/audit.db backend=sqlite socket=/tmp/aidtest-328361000.sock
--- PASS: TestIdentity_ServicePrincipal_FlowsThroughToAuditEvent (0.11s)
=== RUN   TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity
time=2026-03-24T15:28:45.450-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity899560557/002/audit.db
time=2026-03-24T15:28:45.462-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:45.462-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity899560557/001/policies.yaml policies=4
time=2026-03-24T15:28:45.462-04:00 level=INFO msg="audit service starting" version=dev listen=:63679 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity899560557/002/audit.db backend=sqlite socket=/tmp/aidtest-436898000.sock
--- PASS: TestIdentity_AnonymousPrincipal_AuditEventHasNoIdentity (0.11s)
=== RUN   TestIdentity_DBACanWrite
time=2026-03-24T15:28:45.562-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DBACanWrite712252383/002/audit.db
time=2026-03-24T15:28:45.574-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:45.574-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DBACanWrite712252383/001/policies.yaml policies=4
time=2026-03-24T15:28:45.574-04:00 level=INFO msg="audit service starting" version=dev listen=:63686 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DBACanWrite712252383/002/audit.db backend=sqlite socket=/tmp/aidtest-546715000.sock
--- PASS: TestIdentity_DBACanWrite (0.11s)
=== RUN   TestIdentity_NonDBADeniedWrite
time=2026-03-24T15:28:45.672-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonDBADeniedWrite1773263587/002/audit.db
time=2026-03-24T15:28:45.684-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:45.685-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonDBADeniedWrite1773263587/001/policies.yaml policies=4
time=2026-03-24T15:28:45.685-04:00 level=INFO msg="audit service starting" version=dev listen=:63693 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonDBADeniedWrite1773263587/002/audit.db backend=sqlite socket=/tmp/aidtest-656777000.sock
time=2026-03-24T15:28:45.761-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=default-policy user=bob@example.com message="writes not allowed"
time=2026-03-24T15:28:45.762-04:00 level=WARN msg="policy check: DENY" event_id=pol_02e3e1e2 resource=database:prod-db action=write policy=default-policy agent=""
--- PASS: TestIdentity_NonDBADeniedWrite (0.11s)
=== RUN   TestIdentity_OncallEmergencyBreakGlass
time=2026-03-24T15:28:45.781-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_OncallEmergencyBreakGlass3074605422/002/audit.db
time=2026-03-24T15:28:45.793-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:45.793-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_OncallEmergencyBreakGlass3074605422/001/policies.yaml policies=4
time=2026-03-24T15:28:45.794-04:00 level=INFO msg="audit service starting" version=dev listen=:63700 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_OncallEmergencyBreakGlass3074605422/002/audit.db backend=sqlite socket=/tmp/aidtest-765815000.sock
--- PASS: TestIdentity_OncallEmergencyBreakGlass (0.11s)
=== RUN   TestIdentity_NonOncallEmergencyDenied
time=2026-03-24T15:28:45.894-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonOncallEmergencyDenied3434222787/002/audit.db
time=2026-03-24T15:28:45.908-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:45.909-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonOncallEmergencyDenied3434222787/001/policies.yaml policies=4
time=2026-03-24T15:28:45.909-04:00 level=INFO msg="audit service starting" version=dev listen=:63707 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_NonOncallEmergencyDenied3434222787/002/audit.db backend=sqlite socket=/tmp/aidtest-876376000.sock
time=2026-03-24T15:28:45.981-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=default-policy user=dave@example.com purpose=emergency message="writes not allowed"
time=2026-03-24T15:28:45.982-04:00 level=WARN msg="policy check: DENY" event_id=pol_52e62f92 resource=database:prod-db action=write policy=default-policy agent=""
--- PASS: TestIdentity_NonOncallEmergencyDenied (0.11s)
=== RUN   TestIdentity_DiagnosticPurposeBlocksWrite
time=2026-03-24T15:28:45.996-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DiagnosticPurposeBlocksWrite3617405004/002/audit.db
time=2026-03-24T15:28:46.006-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:46.006-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DiagnosticPurposeBlocksWrite3617405004/001/policies.yaml policies=4
time=2026-03-24T15:28:46.006-04:00 level=INFO msg="audit service starting" version=dev listen=:63714 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_DiagnosticPurposeBlocksWrite3617405004/002/audit.db backend=sqlite socket=/tmp/aidtest-984934000.sock
time=2026-03-24T15:28:46.089-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=dba-policy user=alice@example.com purpose=diagnostic message="Purpose \"diagnostic\" is in the blocked list [diagnostic]"
time=2026-03-24T15:28:46.091-04:00 level=WARN msg="policy check: DENY" event_id=pol_6db538e6 resource=database:prod-db action=write policy=dba-policy agent=""
--- PASS: TestIdentity_DiagnosticPurposeBlocksWrite (0.11s)
=== RUN   TestIdentity_RemediationPurposeAllowsDBAWrite
time=2026-03-24T15:28:46.106-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_RemediationPurposeAllowsDBAWrite130768019/002/audit.db
time=2026-03-24T15:28:46.115-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:46.115-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_RemediationPurposeAllowsDBAWrite130768019/001/policies.yaml policies=4
time=2026-03-24T15:28:46.115-04:00 level=INFO msg="audit service starting" version=dev listen=:63721 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_RemediationPurposeAllowsDBAWrite130768019/002/audit.db backend=sqlite socket=/tmp/aidtest-95153000.sock
--- PASS: TestIdentity_RemediationPurposeAllowsDBAWrite (0.11s)
=== RUN   TestIdentity_PIIReadWithPurpose_Allowed
time=2026-03-24T15:28:46.229-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithPurpose_Allowed2551559115/002/audit.db
time=2026-03-24T15:28:46.241-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:46.241-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithPurpose_Allowed2551559115/001/policies.yaml policies=4
time=2026-03-24T15:28:46.242-04:00 level=INFO msg="audit service starting" version=dev listen=:63728 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithPurpose_Allowed2551559115/002/audit.db backend=sqlite socket=/tmp/aidtest-209946000.sock
--- PASS: TestIdentity_PIIReadWithPurpose_Allowed (0.11s)
=== RUN   TestIdentity_PIIReadWithoutPurpose_Denied
time=2026-03-24T15:28:46.340-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithoutPurpose_Denied1174023463/002/audit.db
time=2026-03-24T15:28:46.355-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:46.355-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithoutPurpose_Denied1174023463/001/policies.yaml policies=4
time=2026-03-24T15:28:46.355-04:00 level=INFO msg="audit service starting" version=dev listen=:63735 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIReadWithoutPurpose_Denied1174023463/002/audit.db backend=sqlite socket=/tmp/aidtest-323118000.sock
time=2026-03-24T15:28:46.429-04:00 level=WARN msg="policy decision: DENY" action=read resource_type=database resource_name=customers effect=deny policy=pii-protection user=bob@example.com resource_sensitivity=[pii] message="Purpose \"\" is not in the allowed list [diagnostic compliance remediation]"
time=2026-03-24T15:28:46.431-04:00 level=WARN msg="policy check: DENY" event_id=pol_fcbe2896 resource=database:customers action=read policy=pii-protection agent=""
--- PASS: TestIdentity_PIIReadWithoutPurpose_Denied (0.11s)
=== RUN   TestIdentity_PIIWrite_AlwaysDenied
time=2026-03-24T15:28:46.458-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIWrite_AlwaysDenied1319169158/002/audit.db
time=2026-03-24T15:28:46.472-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:46.473-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIWrite_AlwaysDenied1319169158/001/policies.yaml policies=4
time=2026-03-24T15:28:46.473-04:00 level=INFO msg="audit service starting" version=dev listen=:63742 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_PIIWrite_AlwaysDenied1319169158/002/audit.db backend=sqlite socket=/tmp/aidtest-437738000.sock
time=2026-03-24T15:28:46.544-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=customers effect=deny policy=pii-protection user=alice@example.com resource_sensitivity=[pii] purpose=remediation message="Writes to PII databases are prohibited."
time=2026-03-24T15:28:46.547-04:00 level=WARN msg="policy check: DENY" event_id=pol_4194568e resource=database:customers action=write policy=pii-protection agent=""
--- PASS: TestIdentity_PIIWrite_AlwaysDenied (0.11s)
=== RUN   TestIdentity_Explain_WithPurposeAndSensitivity_Allow
time=2026-03-24T15:28:46.573-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Allow1545118762/002/audit.db
time=2026-03-24T15:28:46.587-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:46.588-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Allow1545118762/001/policies.yaml policies=4
time=2026-03-24T15:28:46.588-04:00 level=INFO msg="audit service starting" version=dev listen=:63749 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Allow1545118762/002/audit.db backend=sqlite socket=/tmp/aidtest-552786000.sock
--- PASS: TestIdentity_Explain_WithPurposeAndSensitivity_Allow (0.11s)
=== RUN   TestIdentity_Explain_WithPurposeAndSensitivity_Deny
time=2026-03-24T15:28:46.677-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Deny4008155920/002/audit.db
time=2026-03-24T15:28:46.691-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:46.691-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Deny4008155920/001/policies.yaml policies=4
time=2026-03-24T15:28:46.691-04:00 level=INFO msg="audit service starting" version=dev listen=:63756 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_WithPurposeAndSensitivity_Deny4008155920/002/audit.db backend=sqlite socket=/tmp/aidtest-662337000.sock
time=2026-03-24T15:28:46.767-04:00 level=WARN msg="policy decision: DENY" action=read resource_type=database resource_name=customers effect=deny policy=pii-protection resource_sensitivity=[pii] message="Purpose \"\" is not in the allowed list [diagnostic compliance remediation]"
--- PASS: TestIdentity_Explain_WithPurposeAndSensitivity_Deny (0.11s)
=== RUN   TestIdentity_Explain_DiagnosticPurpose_DeniesWrite
time=2026-03-24T15:28:46.788-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_DiagnosticPurpose_DeniesWrite4002873067/002/audit.db
time=2026-03-24T15:28:46.801-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:46.801-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_DiagnosticPurpose_DeniesWrite4002873067/001/policies.yaml policies=4
time=2026-03-24T15:28:46.801-04:00 level=INFO msg="audit service starting" version=dev listen=:63763 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_Explain_DiagnosticPurpose_DeniesWrite4002873067/002/audit.db backend=sqlite socket=/tmp/aidtest-771474000.sock
time=2026-03-24T15:28:46.877-04:00 level=WARN msg="policy decision: DENY" action=write resource_type=database resource_name=prod-db effect=deny policy=default-policy purpose=diagnostic message="writes not allowed"
--- PASS: TestIdentity_Explain_DiagnosticPurpose_DeniesWrite (0.11s)
=== RUN   TestIdentity_FullPolicyDecisionRoundTrip
time=2026-03-24T15:28:46.900-04:00 level=INFO msg="sqlite journal mode" mode=delete path=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_FullPolicyDecisionRoundTrip3282635721/002/audit.db
time=2026-03-24T15:28:46.916-04:00 level=INFO msg="authorization configured" enforcing=false
time=2026-03-24T15:28:46.916-04:00 level=INFO msg="policy engine loaded" file=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_FullPolicyDecisionRoundTrip3282635721/001/policies.yaml policies=4
time=2026-03-24T15:28:46.916-04:00 level=INFO msg="audit service starting" version=dev listen=:63770 db=/var/folders/qy/tqplcz1s5qngcp1c75pcp4lm0000gp/T/TestIdentity_FullPolicyDecisionRoundTrip3282635721/002/audit.db backend=sqlite socket=/tmp/aidtest-882003000.sock
--- PASS: TestIdentity_FullPolicyDecisionRoundTrip (0.11s)
PASS
ok  	helpdesk/testing/integration/governance	(cached)
Stopping test infrastructure...
docker compose -f testing/docker/docker-compose.yaml down -v
[+] Running 4/4
 ✔ Container helpdesk-test-pgloader  Removed                                                                                                                                                                                                10.2s
 ✔ Container helpdesk-test-pg        Removed                                                                                                                                                                                                 0.2s
 ✔ Volume docker_pgdata              Removed                                                                                                                                                                                                 0.0s
 ✔ Network docker_default            Removed                                                                                                                                                                                                 0.2s
```

