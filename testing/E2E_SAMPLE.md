# aiHelpDesk: e2e Testing: Sample Run

This is a sample run of aiHelpDesk e2e test.
See the overall aiHelpDesk Testing approach [here](README.md).

```
[boris@ ~/helpdesk]$ date; HELPDESK_MODEL_VENDOR=google HELPDESK_MODEL_NAME=gemini-2.5-flash HELPDESK_API_KEY==$(cat ../llm/...api_key) make e2e
Mon Apr  6 18:04:56 EDT 2026
docker build --load --build-arg VERSION=v0.8.0-29-gcb80581-dirty-cb80581 -t ghcr.io/borisdali/helpdesk:v0.8.0-29-gcb80581-dirty -t helpdesk:latest -f Dockerfile ..
[+] Building 0.6s (54/54) FINISHED                                                                                                                                                                                           docker:desktop-linux
 => [internal] load build definition from Dockerfile                                                                                                                                                                                         0.0s
 => => transferring dockerfile: 6.42kB                                                                                                                                                                                                       0.0s
 => [internal] load metadata for docker.io/library/debian:bookworm-slim                                                                                                                                                                      0.3s
 => [internal] load metadata for docker.io/library/golang:1.25-bookworm                                                                                                                                                                      0.3s
 => [internal] load .dockerignore                                                                                                                                                                                                            0.0s
 => => transferring context: 354B                                                                                                                                                                                                            0.0s
 => [internal] load build context                                                                                                                                                                                                            0.1s
 => => transferring context: 67.27kB                                                                                                                                                                                                         0.1s
 => [builder  1/26] FROM docker.io/library/golang:1.25-bookworm@sha256:7fb09d8804035fbde8a84ed59ca9f46dd68c6f160f9d193e98d795d8d9e002ec                                                                                                      0.0s
 => => resolve docker.io/library/golang:1.25-bookworm@sha256:7fb09d8804035fbde8a84ed59ca9f46dd68c6f160f9d193e98d795d8d9e002ec                                                                                                                0.0s
 => [stage-1  1/22] FROM docker.io/library/debian:bookworm-slim@sha256:f06537653ac770703bc45b4b113475bd402f451e85223f0f2837acbf89ab020a                                                                                                      0.0s
 => => resolve docker.io/library/debian:bookworm-slim@sha256:f06537653ac770703bc45b4b113475bd402f451e85223f0f2837acbf89ab020a                                                                                                                0.0s
 => CACHED [stage-1  2/22] RUN apt-get update && apt-get install -y --no-install-recommends     ca-certificates     curl     gnupg     lsb-release     && rm -rf /var/lib/apt/lists/*                                                        0.0s
 => CACHED [stage-1  3/22] RUN echo "deb http://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main"     > /etc/apt/sources.list.d/pgdg.list     && curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc     | gpg --dea  0.0s
 => CACHED [stage-1  4/22] RUN ARCH=$(dpkg --print-architecture)     && curl -fsSL "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/${ARCH}/kubectl"     -o /usr/local/bin/kubectl     && chmod +x /  0.0s
 => CACHED [builder  2/26] WORKDIR /src                                                                                                                                                                                                      0.0s
 => CACHED [builder  3/26] COPY ADK/github/adk-go /src/adk-go                                                                                                                                                                                0.0s
 => CACHED [builder  4/26] COPY helpdesk /src/helpdesk                                                                                                                                                                                       0.0s
 => CACHED [builder  5/26] WORKDIR /src/helpdesk                                                                                                                                                                                             0.0s
 => CACHED [builder  6/26] RUN go mod edit -replace google.golang.org/adk=/src/adk-go                                                                                                                                                        0.0s
 => CACHED [builder  7/26] RUN go mod download                                                                                                                                                                                               0.0s
 => CACHED [builder  8/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/database-agent  ./agents/database/                            0.0s
 => CACHED [builder  9/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/k8s-agent       ./agents/k8s/                                 0.0s
 => CACHED [builder 10/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/incident-agent  ./agents/incident/                            0.0s
 => CACHED [builder 11/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/research-agent  ./agents/research/                            0.0s
 => CACHED [builder 12/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/gateway         ./cmd/gateway/                                0.0s
 => CACHED [builder 13/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/helpdesk        ./cmd/helpdesk/                               0.0s
 => CACHED [builder 14/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/helpdesk-client ./cmd/helpdesk-client/                        0.0s
 => CACHED [builder 15/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/srebot          ./cmd/srebot/                                 0.0s
 => CACHED [builder 16/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/auditd          ./cmd/auditd/                                 0.0s
 => CACHED [builder 17/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/auditor         ./cmd/auditor/                                0.0s
 => CACHED [builder 18/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/approvals       ./cmd/approvals/                              0.0s
 => CACHED [builder 19/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/secbot          ./cmd/secbot/                                 0.0s
 => CACHED [builder 20/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/govbot          ./cmd/govbot/                                 0.0s
 => CACHED [builder 21/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/govexplain     ./cmd/govexplain/                              0.0s
 => CACHED [builder 22/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/hashapikey    ./cmd/hashapikey/                               0.0s
 => CACHED [builder 23/26] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.8.0-29-gcb80581-dirty-cb80581" -o /out/fleet-runner  ./cmd/fleet-runner/                             0.0s
 => CACHED [builder 24/26] RUN mkdir -p /out/data/incidents /out/etc/helpdesk                                                                                                                                                                0.0s
 => CACHED [builder 25/26] COPY helpdesk/policies.example.yaml /out/etc/helpdesk/policies.example.yaml                                                                                                                                       0.0s
 => CACHED [builder 26/26] COPY helpdesk/users.example.yaml /out/etc/helpdesk/users.example.yaml                                                                                                                                             0.0s
 => CACHED [stage-1  5/22] COPY --from=builder /out/database-agent  /usr/local/bin/database-agent                                                                                                                                            0.0s
 => CACHED [stage-1  6/22] COPY --from=builder /out/k8s-agent       /usr/local/bin/k8s-agent                                                                                                                                                 0.0s
 => CACHED [stage-1  7/22] COPY --from=builder /out/incident-agent  /usr/local/bin/incident-agent                                                                                                                                            0.0s
 => CACHED [stage-1  8/22] COPY --from=builder /out/research-agent  /usr/local/bin/research-agent                                                                                                                                            0.0s
 => CACHED [stage-1  9/22] COPY --from=builder /out/gateway         /usr/local/bin/gateway                                                                                                                                                   0.0s
 => CACHED [stage-1 10/22] COPY --from=builder /out/helpdesk        /usr/local/bin/helpdesk                                                                                                                                                  0.0s
 => CACHED [stage-1 11/22] COPY --from=builder /out/helpdesk-client /usr/local/bin/helpdesk-client                                                                                                                                           0.0s
 => CACHED [stage-1 12/22] COPY --from=builder /out/srebot          /usr/local/bin/srebot                                                                                                                                                    0.0s
 => CACHED [stage-1 13/22] COPY --from=builder /out/auditd          /usr/local/bin/auditd                                                                                                                                                    0.0s
 => CACHED [stage-1 14/22] COPY --from=builder /out/auditor         /usr/local/bin/auditor                                                                                                                                                   0.0s
 => CACHED [stage-1 15/22] COPY --from=builder /out/approvals       /usr/local/bin/approvals                                                                                                                                                 0.0s
 => CACHED [stage-1 16/22] COPY --from=builder /out/secbot          /usr/local/bin/secbot                                                                                                                                                    0.0s
 => CACHED [stage-1 17/22] COPY --from=builder /out/govbot          /usr/local/bin/govbot                                                                                                                                                    0.0s
 => CACHED [stage-1 18/22] COPY --from=builder /out/govexplain      /usr/local/bin/govexplain                                                                                                                                                0.0s
 => CACHED [stage-1 19/22] COPY --from=builder /out/hashapikey     /usr/local/bin/hashapikey                                                                                                                                                 0.0s
 => CACHED [stage-1 20/22] COPY --from=builder /out/fleet-runner   /usr/local/bin/fleet-runner                                                                                                                                               0.0s
 => CACHED [stage-1 21/22] COPY --from=builder /out/data            /data                                                                                                                                                                    0.0s
 => CACHED [stage-1 22/22] COPY --from=builder /out/etc/helpdesk    /etc/helpdesk                                                                                                                                                            0.0s
 => exporting to image                                                                                                                                                                                                                       0.0s
 => => exporting layers                                                                                                                                                                                                                      0.0s
 => => exporting manifest sha256:ced7d1522854bf47956859ddec60d0f987077ec84a0b4689c9fd1a4cbabbd687                                                                                                                                            0.0s
 => => exporting config sha256:5d92dc7b74ded13e32fb714b97548f09dc41227e2b8d9a01ecf5f57cb7308146                                                                                                                                              0.0s
 => => exporting attestation manifest sha256:64a273d5af713288c9cd985139768f0117b1bf1a7d354d70df4012cfd1dfa0a9                                                                                                                                0.0s
 => => exporting manifest list sha256:5f7aadc9eb65fdd137d3fc3448afceb38b4af90e966dcc32d94b155a08b08e9c                                                                                                                                       0.0s
 => => naming to ghcr.io/borisdali/helpdesk:v0.8.0-29-gcb80581-dirty                                                                                                                                                                         0.0s
 => => unpacking to ghcr.io/borisdali/helpdesk:v0.8.0-29-gcb80581-dirty                                                                                                                                                                      0.0s
 => => naming to docker.io/library/helpdesk:latest                                                                                                                                                                                           0.0s
 => => unpacking to docker.io/library/helpdesk:latest                                                                                                                                                                                        0.0s
Starting full stack...
HELPDESK_IDENTITY_PROVIDER=none docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
[+] Running 6/6
 ✔ Container helpdesk-gateway-1         Healthy                                                                                                                                                                                             13.4s
 ✔ Container helpdesk-research-agent-1  Healthy                                                                                                                                                                                              3.4s
 ✔ Container helpdesk-database-agent-1  Healthy                                                                                                                                                                                              3.4s
 ✔ Container helpdesk-k8s-agent-1       Healthy                                                                                                                                                                                              3.4s
 ✔ Container helpdesk-incident-agent-1  Healthy                                                                                                                                                                                              3.4s
 ✔ Container helpdesk-auditd-1          Healthy                                                                                                                                                                                             12.9s
Running E2E tests...
go test -tags e2e -timeout 300s -v ./testing/e2e/...
=== RUN   TestGatewayDiscovery
    gateway_test.go:42: Discovered 4 agents:
    gateway_test.go:44:   - incident_agent
    gateway_test.go:44:   - research_agent
    gateway_test.go:44:   - postgres_database_agent
    gateway_test.go:44:   - k8s_agent
--- PASS: TestGatewayDiscovery (0.06s)
=== RUN   TestGatewayHealthCheck
    gateway_test.go:84: Response (646 chars): ---
        ERROR — check_connection failed for host=localhost port=15432 dbname=testdb user=postgres password=testpass

        Connection refused. The PostgreSQL server may not be running, or the host/port in the...
--- PASS: TestGatewayHealthCheck (0.35s)
=== RUN   TestGatewayAIDiagnosis
    gateway_test.go:111: Sending diagnosis prompt to database agent...
    gateway_test.go:115: LLM API key is set but rejected by the provider — update HELPDESK_API_KEY
--- SKIP: TestGatewayAIDiagnosis (0.37s)
=== RUN   TestGatewayQueryUnknownAgent
--- PASS: TestGatewayQueryUnknownAgent (0.00s)
=== RUN   TestGatewayIncidentBundle
    gateway_test.go:197: Creating incident bundle with callback: http://127.0.0.1:63472/callback
    gateway_test.go:206: LLM API key is set but rejected by the provider — update HELPDESK_API_KEY
--- SKIP: TestGatewayIncidentBundle (0.15s)
=== RUN   TestGatewayResearch
    gateway_test.go:243: Sending research query: What is the latest stable version of PostgreSQL?
    gateway_test.go:247: LLM API key is set but rejected by the provider — update HELPDESK_API_KEY
--- SKIP: TestGatewayResearch (0.14s)
=== RUN   TestGatewayResearchMissingQuery
--- PASS: TestGatewayResearchMissingQuery (0.00s)
=== RUN   TestSREBotWorkflow
=== RUN   TestSREBotWorkflow/Phase1_Discovery
    gateway_test.go:308: Found 4 agents
=== RUN   TestSREBotWorkflow/Phase2_HealthCheck
    gateway_test.go:332: Anomaly detected in health check response
=== RUN   TestSREBotWorkflow/Phase3_AIDiagnosis
    gateway_test.go:353: LLM API key is set but rejected by the provider — update HELPDESK_API_KEY
--- PASS: TestSREBotWorkflow (0.09s)
    --- PASS: TestSREBotWorkflow/Phase1_Discovery (0.00s)
    --- PASS: TestSREBotWorkflow/Phase2_HealthCheck (0.03s)
    --- SKIP: TestSREBotWorkflow/Phase3_AIDiagnosis (0.05s)
=== RUN   TestGovernance_GatewayInfoEndpoint
    governance_test.go:213: governance/info: events_total=14 chain_valid=true policy.enabled=false
--- PASS: TestGovernance_GatewayInfoEndpoint (0.00s)
=== RUN   TestGovernance_GatewayPoliciesEndpoint
    governance_test.go:246: governance/policies: enabled=false
--- PASS: TestGovernance_GatewayPoliciesEndpoint (0.00s)
=== RUN   TestGovernance_ChainIntegrityViaGateway
--- PASS: TestGovernance_ChainIntegrityViaGateway (0.00s)
=== RUN   TestGovernance_AuditdHealth
--- PASS: TestGovernance_AuditdHealth (0.00s)
=== RUN   TestGovernance_AuditdVerifyChain
    governance_test.go:304: verify: total_events=14 valid=true
--- PASS: TestGovernance_AuditdVerifyChain (0.00s)
=== RUN   TestGovernance_ApprovalLifecycle
    governance_test.go:335: created approval: apr_29e58702
    governance_test.go:379: approval apr_29e58702 approved successfully
--- PASS: TestGovernance_ApprovalLifecycle (0.01s)
=== RUN   TestGovernance_AgentCallGeneratesAuditEvents
    governance_test.go:412: baseline events_total: 14
    governance_test.go:424: agent response (646 chars): ---
        ERROR — check_connection failed for host=localhost port=15432 dbname=testdb user=postgres password=testpass

        Connection refused. The PostgreSQL ...
    governance_test.go:439: post-call events_total: 17
--- PASS: TestGovernance_AgentCallGeneratesAuditEvents (2.04s)
=== RUN   TestGovernance_TraceIDCorrelation
    governance_test.go:492: X-Trace-ID: dt_07aea2c2-cf4
    governance_test.go:503: found 3 event(s) for trace_id=dt_07aea2c2-cf4
    governance_test.go:505:   event_type=gateway_request agent=<nil>
    governance_test.go:505:   event_type=tool_invoked agent=<nil>
    governance_test.go:505:   event_type=tool_execution agent=<nil>
--- PASS: TestGovernance_TraceIDCorrelation (2.07s)
=== RUN   TestGovernance_GatewayWithoutAuditdConfigured
    governance_test.go:528: auditd is reachable; degraded-mode test not applicable in full stack
--- SKIP: TestGovernance_GatewayWithoutAuditdConfigured (0.00s)
=== RUN   TestGovernance_FullStackSummary
    governance_test.go:558: === Governance Stack Summary ===
    governance_test.go:559: Gateway  (http://localhost:8080): reachable=true
    governance_test.go:560: Auditd   (http://localhost:1199): reachable=true
    governance_test.go:568: Audit chain: total_events=20 valid=true
    governance_test.go:572: Policy: enabled=false policies_count=0 rules_count=0
    governance_test.go:576: Approvals: pending=0 webhook=false email=false
    governance_test.go:588: Gateway policies: enabled=false
    governance_test.go:592: === End Summary ===
--- PASS: TestGovernance_FullStackSummary (0.02s)
=== RUN   TestGovernance_ExplainEndpoint
    governance_test.go:618: auditd explain response: map[enabled:false message:No policy file configured. Set HELPDESK_POLICY_FILE to enable policy enforcement.]
    governance_test.go:622: policy not enabled on this stack; skipping decision field checks
    governance_test.go:640: gateway explain response: map[enabled:false message:No policy file configured. Set HELPDESK_POLICY_FILE to enable policy enforcement.]
    governance_test.go:643: gateway: policy not enabled on this stack
--- PASS: TestGovernance_ExplainEndpoint (0.01s)
=== RUN   TestGovernance_GetEvent_HasTrace
    governance_test.go:698: created event: e2e-trace-test-1775513118774564000
    governance_test.go:702: retrieved event: map[event_hash:55e7ccba3a32b5388ea825c8905826f0afd7f16e4d92ca81feb3135494453268 event_id:e2e-trace-test-1775513118774564000 event_type:policy_decision input:map[user_query:] policy_decision:map[action:write effect:deny explanation:Access DENIED: writes to production are prohibited by e2e-test-policy message:writes to production are prohibited policy_name:e2e-test-policy resource_name:e2e-prod-db resource_type:database trace:map[decision:map[Effect:deny PolicyName:e2e-test-policy] default_applied:false]] prev_hash:f3b8d900c72022751c09596f4565a2b58636de1f46cca1870fd1868518353ebe session:map[delegation_count:0 id:e2e-test-session started_at:0001-01-01T00:00:00Z] timestamp:2026-04-06T22:05:18.774566Z]
    governance_test.go:722: explanation: Access DENIED: writes to production are prohibited by e2e-test-policy
    governance_test.go:735: gateway proxy event_id: e2e-trace-test-1775513118774564000
--- PASS: TestGovernance_GetEvent_HasTrace (0.01s)
=== RUN   TestGovernance_GetEvent_AgentReasoning
    governance_test.go:771: created agent_reasoning event: e2e-reasoning-1775513118786493000
    governance_test.go:792: reasoning: The user wants connection stats. I will call get_active_connections first, then get_connection_stats.
    governance_test.go:793: tool_calls: [get_active_connections get_connection_stats]
    governance_test.go:807: gateway proxy retrieved agent_reasoning event_id: e2e-reasoning-1775513118786493000
--- PASS: TestGovernance_GetEvent_AgentReasoning (0.01s)
=== RUN   TestGovernance_ReasoningEventsInTrace
    governance_test.go:858: X-Trace-ID: dt_c091f477-da4
    governance_test.go:865: total events for trace dt_c091f477-da4: 3
    governance_test.go:870:   event_type=gateway_request
    governance_test.go:870:   event_type=tool_invoked
    governance_test.go:870:   event_type=tool_execution
    governance_test.go:883: WARNING: no agent_reasoning events found for trace dt_c091f477-da4 — the model may have responded with bare function calls (no text deliberation). Verify NewReasoningCallback is wired in agents/database/main.go.
--- PASS: TestGovernance_ReasoningEventsInTrace (3.04s)
=== RUN   TestGovernance_GetEvent_DelegationVerification
    governance_test.go:946: created delegation_verification event: e2e-dv-1775513121851850000
    governance_test.go:970: delegation_verification round-trip OK: mismatch=true agent=postgres_database_agent tools=[get_session_info]
    governance_test.go:992: journey found: trace_id=e2e-delver-1775513121848388000 outcome=unverified_claim tools_used=[] event_count=1
    governance_test.go:1012: gateway proxy delegation_verification round-trip OK
--- PASS: TestGovernance_GetEvent_DelegationVerification (0.03s)
=== RUN   TestIdentityE2E_PolicyDecisionEvent_IdentityRoundTrip
    identity_test.go:124: created policy_decision event: e2e-ident-1775513121865160000
    identity_test.go:160: identity round-trip OK: user_id=alice@example.com purpose=diagnostic sensitivity=[pii]
--- PASS: TestIdentityE2E_PolicyDecisionEvent_IdentityRoundTrip (0.01s)
=== RUN   TestIdentityE2E_ServicePrincipal_RoundTrip
    identity_test.go:210: service principal round-trip OK: service=srebot auth_method=api_key
--- PASS: TestIdentityE2E_ServicePrincipal_RoundTrip (0.01s)
=== RUN   TestIdentityE2E_PolicyCheck_IdentityFieldsInStoredEvent
    identity_test.go:231: policy engine not enabled on this stack — skipping identity policy check test
--- SKIP: TestIdentityE2E_PolicyCheck_IdentityFieldsInStoredEvent (0.00s)
=== RUN   TestIdentityE2E_Explain_WithPurposeAndSensitivity
    identity_test.go:324: policy engine not enabled on this stack
--- SKIP: TestIdentityE2E_Explain_WithPurposeAndSensitivity (0.00s)
=== RUN   TestIdentityE2E_XUserHeader_PropagatestoAuditTrail
    identity_test.go:412: X-Trace-ID: dt_046ba8ae-9b2 — looking for user_id=e2e-alice@example.com in audit trail
    identity_test.go:421: found 3 audit event(s) for trace_id=dt_046ba8ae-9b2
    identity_test.go:441: ✓ found user_id="e2e-alice@example.com" in session of gateway_request event gw_78d2af58
--- PASS: TestIdentityE2E_XUserHeader_PropagatestoAuditTrail (3.03s)
=== RUN   TestIdentityE2E_AnonymousRequest_NoIdentityInTrace
    identity_test.go:506: anonymous call X-Trace-ID: dt_66183872-353
    identity_test.go:524: anonymous request: 3 event(s) found, none with unexpected user_id — OK
--- PASS: TestIdentityE2E_AnonymousRequest_NoIdentityInTrace (3.05s)
=== RUN   TestMultiAgentIncidentResponse
    multi_agent_test.go:32: E2E_ORCHESTRATOR_URL not set
--- SKIP: TestMultiAgentIncidentResponse (0.00s)
=== RUN   TestFaultInjectionE2E
    multi_agent_test.go:129: Test infrastructure not running (need testing/docker/docker-compose.yaml with pgloader)
--- SKIP: TestFaultInjectionE2E (0.09s)
=== RUN   TestOrchestratorDelegation
    orchestrator_test.go:22: E2E_ORCHESTRATOR_URL not set
--- SKIP: TestOrchestratorDelegation (0.00s)
=== RUN   TestOrchestratorCompoundPrompt
    orchestrator_test.go:87: E2E_ORCHESTRATOR_URL not set
--- SKIP: TestOrchestratorCompoundPrompt (0.00s)
=== RUN   TestDirectAgentCall
    orchestrator_test.go:142: Note: Direct A2A calls may return empty if agent requires specific session context
=== RUN   TestDirectAgentCall/database_agent_direct
    orchestrator_test.go:192: Response (426 chars, 59.294291ms)
=== RUN   TestDirectAgentCall/k8s_agent_direct
    orchestrator_test.go:170: Agent URL or required config not set
--- PASS: TestDirectAgentCall (0.06s)
    --- PASS: TestDirectAgentCall/database_agent_direct (0.06s)
    --- SKIP: TestDirectAgentCall/k8s_agent_direct (0.00s)
=== RUN   TestPlaybooks_SystemPlaybooksSeededAtStartup
    playbooks_test.go:78: playbook list: total=7 system=7 series_found=map[pbs_connection_triage:true pbs_db_config_recovery:true pbs_db_pitr_recovery:true pbs_db_restart_triage:true pbs_replication_lag:true pbs_slow_query_triage:true pbs_vacuum_triage:true]
--- PASS: TestPlaybooks_SystemPlaybooksSeededAtStartup (0.00s)
=== RUN   TestPlaybooks_SystemPlaybooksAreReadOnly
    playbooks_test.go:110: testing read-only protection on system playbook pb_31a213d7
    playbooks_test.go:132: system playbook read-only OK: PUT→400 DELETE→400
--- PASS: TestPlaybooks_SystemPlaybooksAreReadOnly (0.00s)
=== RUN   TestPlaybooks_CRUDLifecycle
    playbooks_test.go:173: created playbook: id=pb_1ed02fc7 series=pbs_f705dca4
    playbooks_test.go:212: CRUD lifecycle OK: create→get→list→delete for playbook pb_1ed02fc7
--- PASS: TestPlaybooks_CRUDLifecycle (0.01s)
=== RUN   TestPlaybooks_ActivateVersion
    playbooks_test.go:253: created v1=pb_f3dfcc77 v2=pb_e1b25873 series=pbs_8a5294af
    playbooks_test.go:287: activate version OK: v2 is now active, v1 is inactive
--- PASS: TestPlaybooks_ActivateVersion (0.01s)
=== RUN   TestPlaybooks_ImportYAML
    playbooks_test.go:357: YAML import OK: confidence=1.0 source=imported name=E2E Import Test
--- PASS: TestPlaybooks_ImportYAML (0.01s)
=== RUN   TestPlaybooks_ListQueryParams
    playbooks_test.go:421: list params OK: default=7 no_system=0 series_with_inactive=2
--- PASS: TestPlaybooks_ListQueryParams (0.01s)
=== RUN   TestPlaybooks_RunFleetMode
    playbooks_test.go:461: fleet planner requires infrastructure config (HELPDESK_INFRA_CONFIG) — not configured in this e2e stack: POST /api/v1/fleet/playbooks/pb_31a213d7/run: HTTP 503: {"error":"fleet planner requires infrastructure config (HELPDESK_INFRA_CONFIG)"}
--- SKIP: TestPlaybooks_RunFleetMode (0.00s)
=== RUN   TestPlaybooks_RunRecording
    playbooks_test.go:519: using playbook id=pb_31a213d7 series=pbs_vacuum_triage
    playbooks_test.go:531: PlaybookRun returned error (expected for unconfigured stack): POST /api/v1/fleet/playbooks/pb_31a213d7/run: HTTP 503: {"error":"fleet planner requires infrastructure config (HELPDESK_INFRA_CONFIG)"}
    playbooks_test.go:543: run recording: count=2
    playbooks_test.go:554: stats: total_runs=2 series_id=pbs_vacuum_triage
--- PASS: TestPlaybooks_RunRecording (0.01s)
=== RUN   TestPlaybooks_InlineStatsInList
    playbooks_test.go:617: inline stats OK: playbook_id=pb_31a213d7 total_runs=3
--- PASS: TestPlaybooks_InlineStatsInList (0.01s)
=== RUN   TestPlaybooks_GetRunByID
    playbooks_test.go:662: fetching run_id=plr_c24c8d17
    playbooks_test.go:678: GET run OK: run_id=plr_c24c8d17 playbook_id=pb_31a213d7 outcome=unknown
--- PASS: TestPlaybooks_GetRunByID (0.00s)
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields/restart_triage_is_entry_point_agent
    playbooks_test.go:735: restart_triage: execution_mode=agent entry_point=true escalates_to=[pbs_db_config_recovery pbs_db_pitr_recovery]
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields/config_recovery_is_agent_with_evidence
    playbooks_test.go:754: config_recovery: escalates_to=[pbs_db_pitr_recovery] requires_evidence=[FATAL.*invalid value for parameter FATAL.*configuration file FATAL.*could not open file]
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields/pitr_recovery_is_agent_with_evidence
    playbooks_test.go:766: pitr_recovery: requires_evidence=[PANIC.*could not locate a valid checkpoint database files are incompatible with server invalid page.*could not read block]
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields/operational_playbooks_are_fleet
--- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields (0.00s)
    --- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields/restart_triage_is_entry_point_agent (0.00s)
    --- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields/config_recovery_is_agent_with_evidence (0.00s)
    --- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields/pitr_recovery_is_agent_with_evidence (0.00s)
    --- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields/operational_playbooks_are_fleet (0.00s)
=== RUN   TestPlaybooks_RunAgentMode
    playbooks_test.go:827: LLM API key is set but rejected by the provider — update HELPDESK_API_KEY
--- SKIP: TestPlaybooks_RunAgentMode (0.05s)
=== RUN   TestResearchAgentDiscovery
    research_agent_test.go:45: Found research_agent: Research agent that searches the web for current information, documentation, best practices, and recent developments.
--- PASS: TestResearchAgentDiscovery (0.00s)
=== RUN   TestResearchAgentDirectQuery
    research_agent_test.go:78: Sending query to research agent...
    research_agent_test.go:84: LLM API key is set but rejected by the provider — update HELPDESK_API_KEY
--- SKIP: TestResearchAgentDirectQuery (0.04s)
=== RUN   TestResearchAgentWebSearch
    research_agent_test.go:122: Sending web search query to research agent...
    research_agent_test.go:127: LLM API key is set but rejected by the provider — update HELPDESK_API_KEY
--- SKIP: TestResearchAgentWebSearch (0.11s)
=== RUN   TestOrchestratorDelegationToResearch
    research_agent_test.go:150: E2E_ORCHESTRATOR_URL not set
--- SKIP: TestOrchestratorDelegationToResearch (0.00s)
=== RUN   TestGatewayResearchQuery
    research_agent_test.go:220: Sending query to research agent via gateway...
    research_agent_test.go:225: Research agent not available through gateway (expected for non-Gemini models): POST /api/v1/query: HTTP 502: {"error":"agent task failed: agent run failed: failed to call model: Error 400, Message: API key not valid. Please pass a valid API key., Status: INVALID_ARGUMENT, Details: [map[@type:type.googleapis.com/google.rpc.ErrorInfo domain:googleapis.com metadata:map[service:generativelanguage.googleapis.com] reason:API_KEY_INVALID] map[@type:type.googleapis.com/google.rpc.LocalizedMessage locale:en-US message:API key not valid. Please pass a valid API key.]]"}
--- SKIP: TestGatewayResearchQuery (0.05s)
=== RUN   TestRollback_PreState_SurvivesStoreRoundTrip
    rollback_test.go:92: created event: e2e-scale-1775513128441137000
    rollback_test.go:115: pre_state round-trip OK: map[deployment_name:api namespace:production previous_replicas:3]
--- PASS: TestRollback_PreState_SurvivesStoreRoundTrip (0.01s)
=== RUN   TestRollback_DerivePlan_OK
    rollback_test.go:127: event: e2e-scale-1775513128447583000
    rollback_test.go:133: plan: map[generated_at:2026-04-06T22:05:28.452240004Z inverse_op:map[agent:k8s args:map[deployment: namespace:production replicas:5] description:restore deployment/ in namespace production to 5 replica(s) tool:scale_deployment] original_event_id:e2e-scale-1775513128447583000 original_tool:scale_deployment original_trace_id:e2e-rbk-47583000 reversibility:yes]
--- PASS: TestRollback_DerivePlan_OK (0.01s)
=== RUN   TestRollback_DerivePlan_NotFound
--- PASS: TestRollback_DerivePlan_NotFound (0.00s)
=== RUN   TestRollback_InitiateLifecycle
    rollback_test.go:177: original event: e2e-scale-1775513128455801000
    rollback_test.go:198: rollback_id: rbk_b3ce66d1
    rollback_test.go:238: rollback lifecycle OK: rbk_b3ce66d1 → pending_approval → cancelled
--- PASS: TestRollback_InitiateLifecycle (0.02s)
=== RUN   TestRollback_DryRun
--- PASS: TestRollback_DryRun (0.00s)
=== RUN   TestRollback_Duplicate_Returns409
--- PASS: TestRollback_Duplicate_Returns409 (0.01s)
=== RUN   TestUploads_CRUDLifecycle
    uploads_test.go:53: uploaded: id=ul_66fb7476 filename=postgresql-e2e-1775513128482245000.log size=179
    uploads_test.go:81: upload CRUD OK: id=ul_66fb7476 content_len=179
--- PASS: TestUploads_CRUDLifecycle (0.00s)
=== RUN   TestUploads_NotFound
--- PASS: TestUploads_NotFound (0.00s)
PASS
ok  	helpdesk/testing/e2e	15.660s
Stopping full stack...
HELPDESK_IDENTITY_PROVIDER=none docker compose -f deploy/docker-compose/docker-compose.yaml down -v
[+] Running 9/9
 ✔ Container helpdesk-gateway-1         Removed                                                                                                                                                                                              0.4s
 ✔ Container helpdesk-k8s-agent-1       Removed                                                                                                                                                                                              0.5s
 ✔ Container helpdesk-database-agent-1  Removed                                                                                                                                                                                              0.4s
 ✔ Container helpdesk-research-agent-1  Removed                                                                                                                                                                                              0.5s
 ✔ Container helpdesk-incident-agent-1  Removed                                                                                                                                                                                              0.3s
 ✔ Container helpdesk-auditd-1          Removed                                                                                                                                                                                              0.2s
 ✔ Volume helpdesk_audit-data           Removed                                                                                                                                                                                              0.0s
 ✔ Volume helpdesk_incidents            Removed                                                                                                                                                                                              0.0s
 ✔ Network helpdesk_default             Removed                                                                                                                                                                                              0.2s
```

