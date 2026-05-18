# aiHelpDesk: e2e Testing: Sample Run

This is a sample run of aiHelpDesk e2e test.
See the overall aiHelpDesk Testing approach [here](README.md).

```
[boris@ ~/helpdesk]$ date; HELPDESK_MODEL_VENDOR=google HELPDESK_MODEL_NAME=gemini-2.5-flash HELPDESK_API_KEY==$(cat ../llm/...api_key) make e2e

[boris@ ~/helpdesk]$ date; HELPDESK_IDENTITY_PROVIDER=none \
  HELPDESK_MODEL_VENDOR=anthropic \
  HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001 \
  HELPDESK_API_KEY=$(head -1 ../llm/anthropic_api_key) \
  make e2e
Wed May 13 16:58:34 EDT 2026
docker build --load --build-arg VERSION=v0.12.2-7-ga6a2103-dirty-a6a2103 \
		-t ghcr.io/borisdali/helpdesk:v0.12.2-7-ga6a2103-dirty-a6a2103 \
		-t ghcr.io/borisdali/helpdesk:v0.12.2-7-ga6a2103-dirty \
		-t helpdesk:latest \
		-f Dockerfile ..
[+] Building 41.0s (60/60) FINISHED                                                                                                                                                                                          docker:desktop-linux
 => [internal] load build definition from Dockerfile                                                                                                                                                                                         0.0s
 => => transferring dockerfile: 7.01kB                                                                                                                                                                                                       0.0s
 => [internal] load metadata for docker.io/library/golang:1.25-bookworm                                                                                                                                                                      0.4s
 => [internal] load metadata for docker.io/library/debian:bookworm-slim                                                                                                                                                                      0.4s
 => [auth] library/debian:pull token for registry-1.docker.io                                                                                                                                                                                0.0s
 => [auth] library/golang:pull token for registry-1.docker.io                                                                                                                                                                                0.0s
 => [internal] load .dockerignore                                                                                                                                                                                                            0.0s
 => => transferring context: 575B                                                                                                                                                                                                            0.0s
 => [builder  1/28] FROM docker.io/library/golang:1.25-bookworm@sha256:e3a54b77385b4f8a31c1db4d12429ffb3718ea76865731a787c497755d409547                                                                                                      0.0s
 => => resolve docker.io/library/golang:1.25-bookworm@sha256:e3a54b77385b4f8a31c1db4d12429ffb3718ea76865731a787c497755d409547                                                                                                                0.0s
 => [stage-1  1/24] FROM docker.io/library/debian:bookworm-slim@sha256:67b30a61dc87758f0caf819646104f29ecbda97d920aaf5edc834128ac8493d3                                                                                                      0.0s
 => => resolve docker.io/library/debian:bookworm-slim@sha256:67b30a61dc87758f0caf819646104f29ecbda97d920aaf5edc834128ac8493d3                                                                                                                0.0s
 => [internal] load build context                                                                                                                                                                                                            0.0s
 => => transferring context: 78.89kB                                                                                                                                                                                                         0.0s
 => CACHED [builder  2/28] WORKDIR /src                                                                                                                                                                                                      0.0s
 => CACHED [builder  3/28] COPY ADK/github/adk-go /src/adk-go                                                                                                                                                                                0.0s
 => [builder  4/28] COPY helpdesk /src/helpdesk                                                                                                                                                                                              0.1s
 => [builder  5/28] WORKDIR /src/helpdesk                                                                                                                                                                                                    0.0s
 => [builder  6/28] RUN go mod edit -replace google.golang.org/adk=/src/adk-go                                                                                                                                                               0.1s
 => [builder  7/28] RUN go mod download                                                                                                                                                                                                      8.5s
 => [builder  8/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/database-agent  ./agents/database/                                  10.9s
 => [builder  9/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/k8s-agent       ./agents/k8s/                                        8.8s
 => [builder 10/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/sysadmin-agent ./agents/sysadmin/                                    0.6s
 => [builder 11/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/incident-agent  ./agents/incident/                                   0.6s
 => [builder 12/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/research-agent  ./agents/research/                                   0.6s
 => [builder 13/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/gateway         ./cmd/gateway/                                       0.7s
 => [builder 14/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/helpdesk        ./cmd/helpdesk/                                      0.9s
 => [builder 15/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/helpdesk-client ./cmd/helpdesk-client/                               0.3s
 => [builder 16/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/srebot          ./cmd/srebot/                                        0.2s
 => [builder 17/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/auditd          ./cmd/auditd/                                        0.6s
 => [builder 18/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/auditor         ./cmd/auditor/                                       0.5s
 => [builder 19/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/approvals       ./cmd/approvals/                                     0.4s
 => [builder 20/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/secbot          ./cmd/secbot/                                        0.4s
 => [builder 21/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/govbot          ./cmd/govbot/                                        0.5s
 => [builder 22/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/govexplain     ./cmd/govexplain/                                     0.3s
 => [builder 23/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/hashapikey    ./cmd/hashapikey/                                      0.2s
 => [builder 24/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/fleet-runner  ./cmd/fleet-runner/                                    0.5s
 => [builder 25/28] RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w -X helpdesk/internal/buildinfo.Version=v0.12.2-7-ga6a2103-dirty-a6a2103" -o /out/faulttest    ./testing/cmd/faulttest/                                0.7s
 => [builder 26/28] RUN mkdir -p /out/data/incidents /out/etc/helpdesk                                                                                                                                                                       0.1s
 => [builder 27/28] COPY helpdesk/policies.example.yaml /out/etc/helpdesk/policies.example.yaml                                                                                                                                              0.0s
 => [builder 28/28] COPY helpdesk/users.example.yaml /out/etc/helpdesk/users.example.yaml                                                                                                                                                    0.0s
 => CACHED [stage-1  2/24] RUN apt-get update && apt-get install -y --no-install-recommends     ca-certificates     curl     gnupg     lsb-release     openssh-client     && rm -rf /var/lib/apt/lists/*                                     0.0s
 => CACHED [stage-1  3/24] RUN echo "deb http://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main"     > /etc/apt/sources.list.d/pgdg.list     && curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc     | gpg --dea  0.0s
 => CACHED [stage-1  4/24] RUN ARCH=$(dpkg --print-architecture)     && curl -fsSL "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/${ARCH}/kubectl"     -o /usr/local/bin/kubectl     && chmod +x /  0.0s
 => [stage-1  5/24] COPY --from=builder /out/database-agent  /usr/local/bin/database-agent                                                                                                                                                   0.0s
 => [stage-1  6/24] COPY --from=builder /out/k8s-agent       /usr/local/bin/k8s-agent                                                                                                                                                        0.0s
 => [stage-1  7/24] COPY --from=builder /out/sysadmin-agent /usr/local/bin/sysadmin-agent                                                                                                                                                    0.0s
 => [stage-1  8/24] COPY --from=builder /out/incident-agent  /usr/local/bin/incident-agent                                                                                                                                                   0.0s
 => [stage-1  9/24] COPY --from=builder /out/research-agent  /usr/local/bin/research-agent                                                                                                                                                   0.0s
 => [stage-1 10/24] COPY --from=builder /out/gateway         /usr/local/bin/gateway                                                                                                                                                          0.0s
 => [stage-1 11/24] COPY --from=builder /out/helpdesk        /usr/local/bin/helpdesk                                                                                                                                                         0.0s
 => [stage-1 12/24] COPY --from=builder /out/helpdesk-client /usr/local/bin/helpdesk-client                                                                                                                                                  0.0s
 => [stage-1 13/24] COPY --from=builder /out/srebot          /usr/local/bin/srebot                                                                                                                                                           0.0s
 => [stage-1 14/24] COPY --from=builder /out/auditd          /usr/local/bin/auditd                                                                                                                                                           0.0s
 => [stage-1 15/24] COPY --from=builder /out/auditor         /usr/local/bin/auditor                                                                                                                                                          0.0s
 => [stage-1 16/24] COPY --from=builder /out/approvals       /usr/local/bin/approvals                                                                                                                                                        0.0s
 => [stage-1 17/24] COPY --from=builder /out/secbot          /usr/local/bin/secbot                                                                                                                                                           0.0s
 => [stage-1 18/24] COPY --from=builder /out/govbot          /usr/local/bin/govbot                                                                                                                                                           0.0s
 => [stage-1 19/24] COPY --from=builder /out/govexplain      /usr/local/bin/govexplain                                                                                                                                                       0.0s
 => [stage-1 20/24] COPY --from=builder /out/hashapikey     /usr/local/bin/hashapikey                                                                                                                                                        0.0s
 => [stage-1 21/24] COPY --from=builder /out/fleet-runner   /usr/local/bin/fleet-runner                                                                                                                                                      0.0s
 => [stage-1 22/24] COPY --from=builder /out/faulttest     /usr/local/bin/faulttest                                                                                                                                                          0.0s
 => [stage-1 23/24] COPY --from=builder /out/data            /data                                                                                                                                                                           0.0s
 => [stage-1 24/24] COPY --from=builder /out/etc/helpdesk    /etc/helpdesk                                                                                                                                                                   0.0s
 => exporting to image                                                                                                                                                                                                                       3.3s
 => => exporting layers                                                                                                                                                                                                                      1.9s
 => => exporting manifest sha256:799350892bc8b2c4b3f4a3aa2a9f72b1a990e6bd8be392a24b0627d72bbcbdbd                                                                                                                                            0.0s
 => => exporting config sha256:1fb7ea11bd05eccf4a7e4f3d7b55f0a3fe1bea58e06e1ec91678c923af8b8e34                                                                                                                                              0.0s
 => => exporting attestation manifest sha256:86bc57993964eb4b07ca4d8d0cb561712e42a7ac0a852aeeb03eca4445c791fc                                                                                                                                0.0s
 => => exporting manifest list sha256:b97716eeb25ef36ce09a14b7c27ce5514f1a38a5503111de3f4651829d064d32                                                                                                                                       0.0s
 => => naming to ghcr.io/borisdali/helpdesk:v0.12.2-7-ga6a2103-dirty-a6a2103                                                                                                                                                                 0.0s
 => => unpacking to ghcr.io/borisdali/helpdesk:v0.12.2-7-ga6a2103-dirty-a6a2103                                                                                                                                                              1.4s
 => => naming to ghcr.io/borisdali/helpdesk:v0.12.2-7-ga6a2103-dirty                                                                                                                                                                         0.0s
 => => unpacking to ghcr.io/borisdali/helpdesk:v0.12.2-7-ga6a2103-dirty                                                                                                                                                                      0.0s
 => => naming to docker.io/library/helpdesk:latest                                                                                                                                                                                           0.0s
 => => unpacking to docker.io/library/helpdesk:latest                                                                                                                                                                                        0.0s

View build details: docker-desktop://dashboard/build/desktop-linux/desktop-linux/nd7vdk784qn8qn0ft64qcl8m3
Starting full stack...
HELPDESK_IDENTITY_PROVIDER=none docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
[+] Running 10/10
 ✔ Network helpdesk_default             Created                                                                                                                                                                                              0.0s
 ✔ Volume "helpdesk_audit-data"         Created                                                                                                                                                                                              0.0s
 ✔ Volume "helpdesk_incidents"          Created                                                                                                                                                                                              0.0s
 ✔ Container helpdesk-auditd-1          Healthy                                                                                                                                                                                             12.4s
 ✔ Container helpdesk-database-agent-1  Healthy                                                                                                                                                                                             12.3s
 ✔ Container helpdesk-incident-agent-1  Healthy                                                                                                                                                                                             12.3s
 ✔ Container helpdesk-k8s-agent-1       Healthy                                                                                                                                                                                             12.3s
 ✔ Container helpdesk-research-agent-1  Healthy                                                                                                                                                                                             12.3s
 ✔ Container helpdesk-sysadmin-agent-1  Healthy                                                                                                                                                                                             12.3s
 ✔ Container helpdesk-gateway-1         Healthy                                                                                                                                                                                             22.2s
Running E2E tests...
go test -tags e2e -timeout 300s -v ./testing/e2e/... 2>&1 | tee /tmp/helpdesk-e2e.log
=== RUN   TestAuditToolEvidenceContract
    audit_tool_evidence_test.go:58: created tool_execution event: e2e-toolcontract-1778705980454570000
    audit_tool_evidence_test.go:65: query /v1/events?event_type=tool_execution&since=2026-05-13T20:59:40Z returned 1 event(s)
    audit_tool_evidence_test.go:107: contract verified: tool.name = "check_connection" (field format is compatible with auditQueryTools)
--- PASS: TestAuditToolEvidenceContract (0.02s)
=== RUN   TestAuditToolEvidence_RealAgentCall
    audit_tool_evidence_test.go:173: sending query to database agent via gateway
    audit_tool_evidence_test.go:183: agent response (1484 chars): ---
        ERROR — check_connection failed for host=localhost port=15432 dbname=testdb user=postgres password=testpass

        Connection refused. The PostgreSQL server may not be running, or the host/port in the c
    audit_tool_evidence_test.go:193: auditd returned 2 tool_execution event(s) since call start
    audit_tool_evidence_test.go:218: tool_execution events with tool.name: [check_connection check_connection]
--- PASS: TestAuditToolEvidence_RealAgentCall (6.20s)
=== RUN   TestGatewayDiscovery
    gateway_test.go:43: Discovered 5 agents:
    gateway_test.go:45:   - postgres_database_agent
    gateway_test.go:45:   - k8s_agent
    gateway_test.go:45:   - sysadmin_agent
    gateway_test.go:45:   - incident_agent
    gateway_test.go:45:   - research_agent
--- PASS: TestGatewayDiscovery (0.00s)
=== RUN   TestGatewayHealthCheck
    gateway_test.go:85: Response (646 chars): ---
        ERROR — check_connection failed for host=localhost port=15432 dbname=testdb user=postgres password=testpass

        Connection refused. The PostgreSQL server may not be running, or the host/port in the...
--- PASS: TestGatewayHealthCheck (0.04s)
=== RUN   TestGatewayAIDiagnosis
    gateway_test.go:112: Sending diagnosis prompt to database agent...
    gateway_test.go:120: Agent response (1408 chars)
--- PASS: TestGatewayAIDiagnosis (5.01s)
=== RUN   TestGatewayQueryUnknownAgent
--- PASS: TestGatewayQueryUnknownAgent (0.00s)
=== RUN   TestGatewayIncidentBundle
    gateway_test.go:198: Creating incident bundle with callback: http://127.0.0.1:64850/callback
    gateway_test.go:211: Incident agent responded (800 chars)
    gateway_test.go:224: Warning: No callback received within 60s (may be expected if incident agent not configured)
--- PASS: TestGatewayIncidentBundle (64.14s)
=== RUN   TestGatewayResearch
    gateway_test.go:244: Sending research query: What is the latest stable version of PostgreSQL?
    gateway_test.go:252: Research response (1282 chars): I don't have the ability to search the web in real-time, so I can't tell you the absolute latest stable version available right now.

        However, based on my training data (current through April 2024), I can share what I know:

        **PostgreSQL versioning approach:**
        - PostgreSQL maintains multiple support...
--- PASS: TestGatewayResearch (3.55s)
=== RUN   TestGatewayResearchMissingQuery
--- PASS: TestGatewayResearchMissingQuery (0.00s)
=== RUN   TestSREBotWorkflow
=== RUN   TestSREBotWorkflow/Phase1_Discovery
    gateway_test.go:309: Found 5 agents
=== RUN   TestSREBotWorkflow/Phase2_HealthCheck
    gateway_test.go:333: Anomaly detected in health check response
=== RUN   TestSREBotWorkflow/Phase3_AIDiagnosis
    gateway_test.go:358: Diagnosis response (1381 chars)
--- PASS: TestSREBotWorkflow (5.97s)
    --- PASS: TestSREBotWorkflow/Phase1_Discovery (0.00s)
    --- PASS: TestSREBotWorkflow/Phase2_HealthCheck (0.04s)
    --- PASS: TestSREBotWorkflow/Phase3_AIDiagnosis (5.93s)
=== RUN   TestGatewayMetricsEndpoint
--- PASS: TestGatewayMetricsEndpoint (0.00s)
=== RUN   TestGovernance_GatewayInfoEndpoint
    governance_test.go:213: governance/info: events_total=41 chain_valid=true policy.enabled=true
--- PASS: TestGovernance_GatewayInfoEndpoint (0.01s)
=== RUN   TestGovernance_GatewayPoliciesEndpoint
    governance_test.go:246: governance/policies: enabled=true
--- PASS: TestGovernance_GatewayPoliciesEndpoint (0.00s)
=== RUN   TestGovernance_ChainIntegrityViaGateway
--- PASS: TestGovernance_ChainIntegrityViaGateway (0.01s)
=== RUN   TestGovernance_AuditdHealth
--- PASS: TestGovernance_AuditdHealth (0.00s)
=== RUN   TestGovernance_AuditdVerifyChain
    governance_test.go:304: verify: total_events=41 valid=true
--- PASS: TestGovernance_AuditdVerifyChain (0.00s)
=== RUN   TestGovernance_ApprovalLifecycle
    governance_test.go:335: created approval: apr_eab4991a
    governance_test.go:379: approval apr_eab4991a approved successfully
--- PASS: TestGovernance_ApprovalLifecycle (0.01s)
=== RUN   TestGovernance_AgentCallGeneratesAuditEvents
    governance_test.go:412: baseline events_total: 41
    governance_test.go:424: agent response (646 chars): ---
        ERROR — check_connection failed for host=localhost port=15432 dbname=testdb user=postgres password=testpass

        Connection refused. The PostgreSQL ...
    governance_test.go:439: post-call events_total: 44
--- PASS: TestGovernance_AgentCallGeneratesAuditEvents (2.03s)
=== RUN   TestGovernance_TraceIDCorrelation
    governance_test.go:492: X-Trace-ID: dt_ebc931bc-bf6
    governance_test.go:503: found 3 event(s) for trace_id=dt_ebc931bc-bf6
    governance_test.go:505:   event_type=gateway_request agent=<nil>
    governance_test.go:505:   event_type=tool_invoked agent=<nil>
    governance_test.go:505:   event_type=tool_execution agent=<nil>
--- PASS: TestGovernance_TraceIDCorrelation (2.04s)
=== RUN   TestGovernance_GatewayWithoutAuditdConfigured
    governance_test.go:528: auditd is reachable; degraded-mode test not applicable in full stack
--- SKIP: TestGovernance_GatewayWithoutAuditdConfigured (0.00s)
=== RUN   TestGovernance_FullStackSummary
    governance_test.go:558: === Governance Stack Summary ===
    governance_test.go:559: Gateway  (http://localhost:8080): reachable=true
    governance_test.go:560: Auditd   (http://localhost:1199): reachable=true
    governance_test.go:568: Audit chain: total_events=47 valid=true
    governance_test.go:572: Policy: enabled=true policies_count=17 rules_count=32
    governance_test.go:576: Approvals: pending=0 webhook=false email=false
    governance_test.go:588: Gateway policies: enabled=true
    governance_test.go:592: === End Summary ===
--- PASS: TestGovernance_FullStackSummary (0.02s)
=== RUN   TestGovernance_ExplainEndpoint
    governance_test.go:618: auditd explain response: map[decision:map[effect:deny message:No matching policy found policy_name:default rule_index:0] default_applied:true explanation:Access to database test-db for read: DENIED

        No policy matched this resource — default effect is deny.

        This resource has no tags, so no tag-based policy can match it.
        Add it to HELPDESK_INFRA_CONFIG with one of the following tag sets:

          • tags: [production]  → enables policy "production-database-protection"
          • tags: [development]  → enables policy "development-permissive"
         policies_evaluated:[map[matched:false policy_name:authenticated-write skip_reason:principal_mismatch] map[matched:false policy_name:authenticated-read skip_reason:principal_mismatch] map[matched:false policy_name:emergency-break-glass skip_reason:principal_mismatch] map[matched:false policy_name:emergency-dba-break-glass skip_reason:principal_mismatch] map[matched:false policy_name:fleet-runner-policy skip_reason:principal_mismatch] map[matched:false policy_name:pii-data-protection skip_reason:resource_mismatch] map[matched:false policy_name:critical-infra-write-guard skip_reason:resource_mismatch] map[matched:false policy_name:production-database-protection required_tags:[[production]] skip_reason:resource_mismatch] map[matched:false policy_name:k8s-system-protection skip_reason:resource_mismatch] map[matched:true policy_name:diagnostic-readonly-enforcement rules:[map[actions:[write destructive] effect:allow index:0 matched:false skip_reason:action_mismatch]]] map[matched:false policy_name:business-hours-freeze required_tags:[[production]] skip_reason:resource_mismatch] map[matched:false policy_name:restrict-terminate-connection skip_reason:resource_mismatch] map[matched:false policy_name:dba-privileges skip_reason:principal_mismatch] map[matched:false policy_name:no-terminate-for-automation skip_reason:principal_mismatch] map[matched:false policy_name:sre-staging-access skip_reason:principal_mismatch] map[matched:false policy_name:automated-services skip_reason:principal_mismatch] map[matched:false policy_name:development-permissive required_tags:[[development]] skip_reason:resource_mismatch]]]
    governance_test.go:640: gateway explain response: map[decision:map[effect:deny message:No matching policy found policy_name:default rule_index:0] default_applied:true explanation:Access to database test-db for read: DENIED

        No policy matched this resource — default effect is deny.

        This resource has no tags, so no tag-based policy can match it.
        Add it to HELPDESK_INFRA_CONFIG with one of the following tag sets:

          • tags: [production]  → enables policy "production-database-protection"
          • tags: [development]  → enables policy "development-permissive"
         policies_evaluated:[map[matched:false policy_name:authenticated-write skip_reason:principal_mismatch] map[matched:false policy_name:authenticated-read skip_reason:principal_mismatch] map[matched:false policy_name:emergency-break-glass skip_reason:principal_mismatch] map[matched:false policy_name:emergency-dba-break-glass skip_reason:principal_mismatch] map[matched:false policy_name:fleet-runner-policy skip_reason:principal_mismatch] map[matched:false policy_name:pii-data-protection skip_reason:resource_mismatch] map[matched:false policy_name:critical-infra-write-guard skip_reason:resource_mismatch] map[matched:false policy_name:production-database-protection required_tags:[[production]] skip_reason:resource_mismatch] map[matched:false policy_name:k8s-system-protection skip_reason:resource_mismatch] map[matched:true policy_name:diagnostic-readonly-enforcement rules:[map[actions:[write destructive] effect:allow index:0 matched:false skip_reason:action_mismatch]]] map[matched:false policy_name:business-hours-freeze required_tags:[[production]] skip_reason:resource_mismatch] map[matched:false policy_name:restrict-terminate-connection skip_reason:resource_mismatch] map[matched:false policy_name:dba-privileges skip_reason:principal_mismatch] map[matched:false policy_name:no-terminate-for-automation skip_reason:principal_mismatch] map[matched:false policy_name:sre-staging-access skip_reason:principal_mismatch] map[matched:false policy_name:automated-services skip_reason:principal_mismatch] map[matched:false policy_name:development-permissive required_tags:[[development]] skip_reason:resource_mismatch]]]
--- PASS: TestGovernance_ExplainEndpoint (0.01s)
=== RUN   TestGovernance_GetEvent_HasTrace
    governance_test.go:698: created event: e2e-trace-test-1778706069511454000
    governance_test.go:702: retrieved event: map[event_hash:7467283bade1359286024c3a29967ec65c10fd8f7539e4362023548ad4340a82 event_id:e2e-trace-test-1778706069511454000 event_type:policy_decision input:map[user_query:] policy_decision:map[action:write effect:deny explanation:Access DENIED: writes to production are prohibited by e2e-test-policy message:writes to production are prohibited policy_name:e2e-test-policy resource_name:e2e-prod-db resource_type:database trace:map[decision:map[Effect:deny PolicyName:e2e-test-policy] default_applied:false]] prev_hash:5a4c72b6c389ff21190c9bfe41e11ae857341dd9089d4d4cf26339fb97bead1c session:map[delegation_count:0 id:e2e-test-session started_at:0001-01-01T00:00:00Z] timestamp:2026-05-13T21:01:09.511457Z]
    governance_test.go:722: explanation: Access DENIED: writes to production are prohibited by e2e-test-policy
    governance_test.go:735: gateway proxy event_id: e2e-trace-test-1778706069511454000
--- PASS: TestGovernance_GetEvent_HasTrace (0.01s)
=== RUN   TestGovernance_GetEvent_AgentReasoning
    governance_test.go:771: created agent_reasoning event: e2e-reasoning-1778706069521354000
    governance_test.go:792: reasoning: The user wants connection stats. I will call get_active_connections first, then get_connection_stats.
    governance_test.go:793: tool_calls: [get_active_connections get_connection_stats]
    governance_test.go:807: gateway proxy retrieved agent_reasoning event_id: e2e-reasoning-1778706069521354000
--- PASS: TestGovernance_GetEvent_AgentReasoning (0.01s)
=== RUN   TestGovernance_ReasoningEventsInTrace
    governance_test.go:858: X-Trace-ID: dt_6c5af791-14c
    governance_test.go:865: total events for trace dt_6c5af791-14c: 3
    governance_test.go:870:   event_type=gateway_request
    governance_test.go:870:   event_type=tool_invoked
    governance_test.go:870:   event_type=tool_execution
    governance_test.go:883: WARNING: no agent_reasoning events found for trace dt_6c5af791-14c — the model may have responded with bare function calls (no text deliberation). Verify NewReasoningCallback is wired in agents/database/main.go.
--- PASS: TestGovernance_ReasoningEventsInTrace (3.03s)
=== RUN   TestGovernance_GetEvent_DelegationVerification
    governance_test.go:946: created delegation_verification event: e2e-dv-1778706072574052000
    governance_test.go:970: delegation_verification round-trip OK: mismatch=true agent=postgres_database_agent tools=[get_session_info]
    governance_test.go:996: journey found: trace_id=e2e-delver-1778706072569861000 outcome=unverified_claim tools_used=[] event_count=1 has_mismatch=true
    governance_test.go:1016: gateway proxy delegation_verification round-trip OK
--- PASS: TestGovernance_GetEvent_DelegationVerification (0.02s)
=== RUN   TestIdentityE2E_PolicyDecisionEvent_IdentityRoundTrip
    identity_test.go:124: created policy_decision event: e2e-ident-1778706072590378000
    identity_test.go:160: identity round-trip OK: user_id=alice@example.com purpose=diagnostic sensitivity=[pii]
--- PASS: TestIdentityE2E_PolicyDecisionEvent_IdentityRoundTrip (0.01s)
=== RUN   TestIdentityE2E_ServicePrincipal_RoundTrip
    identity_test.go:210: service principal round-trip OK: service=srebot auth_method=api_key
--- PASS: TestIdentityE2E_ServicePrincipal_RoundTrip (0.01s)
=== RUN   TestIdentityE2E_PolicyCheck_IdentityFieldsInStoredEvent
    identity_test.go:278: policy check → effect=allow event_id=pol_a86de488
    identity_test.go:303: identity fields in stored pol_* event: user_id=carol@example.com purpose=compliance sensitivity=[internal]
--- PASS: TestIdentityE2E_PolicyCheck_IdentityFieldsInStoredEvent (0.01s)
=== RUN   TestIdentityE2E_Explain_WithPurposeAndSensitivity
    identity_test.go:338: explain pii+diagnostic read: effect=allow policy=pii-data-protection
    identity_test.go:349: gateway explain pii+diagnostic read: effect=allow policy=pii-data-protection
--- PASS: TestIdentityE2E_Explain_WithPurposeAndSensitivity (0.01s)
=== RUN   TestIdentityE2E_XUserHeader_PropagatestoAuditTrail
    identity_test.go:412: X-Trace-ID: dt_53badbd9-017 — looking for user_id=e2e-alice@example.com in audit trail
    identity_test.go:421: found 3 audit event(s) for trace_id=dt_53badbd9-017
    identity_test.go:441: ✓ found user_id="e2e-alice@example.com" in session of gateway_request event gw_279d1029
--- PASS: TestIdentityE2E_XUserHeader_PropagatestoAuditTrail (3.03s)
=== RUN   TestIdentityE2E_AnonymousRequest_NoIdentityInTrace
    identity_test.go:506: anonymous call X-Trace-ID: dt_2e874481-e11
    identity_test.go:524: anonymous request: 3 event(s) found, none with unexpected user_id — OK
--- PASS: TestIdentityE2E_AnonymousRequest_NoIdentityInTrace (3.05s)
=== RUN   TestMultiAgentIncidentResponse
    multi_agent_test.go:32: E2E_ORCHESTRATOR_URL not set
--- SKIP: TestMultiAgentIncidentResponse (0.00s)
=== RUN   TestFaultInjectionE2E
    multi_agent_test.go:129: Test infrastructure not running (need testing/docker/docker-compose.yaml with pgloader)
--- SKIP: TestFaultInjectionE2E (0.06s)
=== RUN   TestOrchestratorDelegation
    orchestrator_test.go:22: E2E_ORCHESTRATOR_URL not set
--- SKIP: TestOrchestratorDelegation (0.00s)
=== RUN   TestOrchestratorCompoundPrompt
    orchestrator_test.go:87: E2E_ORCHESTRATOR_URL not set
--- SKIP: TestOrchestratorCompoundPrompt (0.00s)
=== RUN   TestDirectAgentCall
    orchestrator_test.go:142: Note: Direct A2A calls may return empty if agent requires specific session context
=== RUN   TestDirectAgentCall/database_agent_direct
    orchestrator_test.go:192: Response (1055 chars, 2.836228958s)
=== RUN   TestDirectAgentCall/k8s_agent_direct
    orchestrator_test.go:170: Agent URL or required config not set
--- PASS: TestDirectAgentCall (2.84s)
    --- PASS: TestDirectAgentCall/database_agent_direct (2.84s)
    --- SKIP: TestDirectAgentCall/k8s_agent_direct (0.00s)
=== RUN   TestPlaybooks_SystemPlaybooksSeededAtStartup
    playbooks_test.go:78: playbook list: total=13 system=13 series_found=map[pbs_checkpoint_bgwriter_triage:true pbs_connection_triage:true pbs_db_config_recovery:true pbs_db_pitr_recovery:true pbs_db_restart_action:true pbs_db_restart_triage:true pbs_k8s_pod_crash_triage:true pbs_replication_lag:true pbs_slow_query_triage:true pbs_sysadmin_docker_inspect:true pbs_vacuum_triage:true pbs_wal_disk_full:true pbs_wal_stale_slot:true]
--- PASS: TestPlaybooks_SystemPlaybooksSeededAtStartup (0.01s)
=== RUN   TestPlaybooks_SystemPlaybooksAreReadOnly
    playbooks_test.go:110: testing read-only protection on system playbook pb_26f8c911
    playbooks_test.go:132: system playbook read-only OK: PUT→400 DELETE→400
--- PASS: TestPlaybooks_SystemPlaybooksAreReadOnly (0.01s)
=== RUN   TestPlaybooks_CRUDLifecycle
    playbooks_test.go:173: created playbook: id=pb_63e55678 series=pbs_fe523a92
    playbooks_test.go:212: CRUD lifecycle OK: create→get→list→delete for playbook pb_63e55678
--- PASS: TestPlaybooks_CRUDLifecycle (0.01s)
=== RUN   TestPlaybooks_ActivateVersion
    playbooks_test.go:253: created v1=pb_2fac9a84 v2=pb_67eb7020 series=pbs_e401dae6
    playbooks_test.go:287: activate version OK: v2 is now active, v1 is inactive
--- PASS: TestPlaybooks_ActivateVersion (0.01s)
=== RUN   TestPlaybooks_ImportYAML
    playbooks_test.go:357: YAML import OK: confidence=1.0 source=imported name=E2E Import Test
--- PASS: TestPlaybooks_ImportYAML (0.01s)
=== RUN   TestPlaybooks_ListQueryParams
    playbooks_test.go:421: list params OK: default=13 no_system=0 series_with_inactive=2
--- PASS: TestPlaybooks_ListQueryParams (0.01s)
=== RUN   TestPlaybooks_RunFleetMode
    playbooks_test.go:461: fleet planner requires infrastructure config (HELPDESK_INFRA_CONFIG) — not configured in this e2e stack: POST /api/v1/fleet/playbooks/pb_eea2b465/run: HTTP 503: {"error":"fleet planner requires infrastructure config (HELPDESK_INFRA_CONFIG)"}
--- SKIP: TestPlaybooks_RunFleetMode (0.01s)
=== RUN   TestPlaybooks_RunRecording
    playbooks_test.go:519: using playbook id=pb_eea2b465 series=pbs_vacuum_triage
    playbooks_test.go:531: PlaybookRun returned error (expected for unconfigured stack): POST /api/v1/fleet/playbooks/pb_eea2b465/run: HTTP 503: {"error":"fleet planner requires infrastructure config (HELPDESK_INFRA_CONFIG)"}
    playbooks_test.go:543: run recording: count=2
    playbooks_test.go:554: stats: total_runs=2 series_id=pbs_vacuum_triage
--- PASS: TestPlaybooks_RunRecording (0.01s)
=== RUN   TestPlaybooks_InlineStatsInList
    playbooks_test.go:617: inline stats OK: playbook_id=pb_eea2b465 total_runs=3
--- PASS: TestPlaybooks_InlineStatsInList (0.01s)
=== RUN   TestPlaybooks_GetRunByID
    playbooks_test.go:662: fetching run_id=plr_d1b60ea9
    playbooks_test.go:678: GET run OK: run_id=plr_d1b60ea9 playbook_id=pb_26f8c911 outcome=escalated
--- PASS: TestPlaybooks_GetRunByID (13.36s)
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields/restart_triage_is_entry_point_agent
    playbooks_test.go:735: restart_triage: execution_mode=agent entry_point=true escalates_to=[pbs_db_config_recovery pbs_db_pitr_recovery pbs_sysadmin_docker_inspect]
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields/config_recovery_is_agent_with_evidence
    playbooks_test.go:754: config_recovery: escalates_to=[pbs_db_pitr_recovery] requires_evidence=[FATAL.*invalid value for parameter FATAL.*configuration file FATAL.*could not open file]
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields/pitr_recovery_is_agent_with_evidence
    playbooks_test.go:766: pitr_recovery: requires_evidence=[PANIC.*could not locate a valid checkpoint database files are incompatible with server invalid page.*could not read block]
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields/sysadmin_docker_inspect_is_seeded
    playbooks_test.go:780: sysadmin_docker_inspect: execution_mode=agent entry_point=false approval_mode=manual
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields/restart_triage_escalates_to_sysadmin_docker_inspect
=== RUN   TestPlaybooks_DBDownPlaybooksHaveAgentFields/operational_playbooks_are_fleet
--- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields (0.01s)
    --- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields/restart_triage_is_entry_point_agent (0.00s)
    --- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields/config_recovery_is_agent_with_evidence (0.00s)
    --- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields/pitr_recovery_is_agent_with_evidence (0.00s)
    --- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields/sysadmin_docker_inspect_is_seeded (0.00s)
    --- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields/restart_triage_escalates_to_sysadmin_docker_inspect (0.00s)
    --- PASS: TestPlaybooks_DBDownPlaybooksHaveAgentFields/operational_playbooks_are_fleet (0.00s)
=== RUN   TestPlaybooks_RunAgentMode
    playbooks_test.go:867: agent run OK: playbook_id=pb_c2d4a3f6 text_len=3182
--- PASS: TestPlaybooks_RunAgentMode (12.95s)
=== RUN   TestResearchAgentDiscovery
    research_agent_test.go:27: Research agent discovery test only relevant for Gemini models
--- SKIP: TestResearchAgentDiscovery (0.00s)
=== RUN   TestResearchAgentDirectQuery
    research_agent_test.go:70: Research agent only works with Gemini models
--- SKIP: TestResearchAgentDirectQuery (0.00s)
=== RUN   TestResearchAgentWebSearch
    research_agent_test.go:113: Research agent only works with Gemini models
--- SKIP: TestResearchAgentWebSearch (0.00s)
=== RUN   TestOrchestratorDelegationToResearch
    research_agent_test.go:150: E2E_ORCHESTRATOR_URL not set
--- SKIP: TestOrchestratorDelegationToResearch (0.00s)
=== RUN   TestGatewayResearchQuery
    research_agent_test.go:210: Research agent only works with Gemini models
--- SKIP: TestGatewayResearchQuery (0.00s)
=== RUN   TestRollback_PreState_SurvivesStoreRoundTrip
    rollback_test.go:92: created event: e2e-scale-1778706108001693000
    rollback_test.go:115: pre_state round-trip OK: map[deployment_name:api namespace:production previous_replicas:3]
--- PASS: TestRollback_PreState_SurvivesStoreRoundTrip (0.00s)
=== RUN   TestRollback_DerivePlan_OK
    rollback_test.go:127: event: e2e-scale-1778706108004907000
    rollback_test.go:133: plan: map[generated_at:2026-05-13T21:01:48.006897509Z inverse_op:map[agent:k8s args:map[deployment: namespace:production replicas:5] description:restore deployment/ in namespace production to 5 replica(s) tool:scale_deployment] original_event_id:e2e-scale-1778706108004907000 original_tool:scale_deployment original_trace_id:e2e-rbk-04907000 reversibility:yes]
--- PASS: TestRollback_DerivePlan_OK (0.00s)
=== RUN   TestRollback_DerivePlan_NotFound
--- PASS: TestRollback_DerivePlan_NotFound (0.00s)
=== RUN   TestRollback_InitiateLifecycle
    rollback_test.go:177: original event: e2e-scale-1778706108008662000
    rollback_test.go:198: rollback_id: rbk_c964f5dd
    rollback_test.go:238: rollback lifecycle OK: rbk_c964f5dd → pending_approval → cancelled
--- PASS: TestRollback_InitiateLifecycle (0.01s)
=== RUN   TestRollback_DryRun
--- PASS: TestRollback_DryRun (0.00s)
=== RUN   TestRollback_Duplicate_Returns409
--- PASS: TestRollback_Duplicate_Returns409 (0.01s)
=== RUN   TestDBAgentToolCallSummary
    tool_call_test.go:37: Sending direct A2A prompt to DB agent at http://localhost:1100
    tool_call_test.go:47: Response text (1505 chars, 4.481222959s): I'll check the database connection and get the PostgreSQL version for you.
        ---
        ERROR — check_connection failed for host=localhost port=15432 dbname=testdb user=postgres password=testpass

        Connection...
    tool_call_test.go:64: Tool calls observed (1):
    tool_call_test.go:66:   check_connection (success=false)
--- PASS: TestDBAgentToolCallSummary (4.48s)
=== RUN   TestUploads_CRUDLifecycle
    uploads_test.go:53: uploaded: id=ul_de175d9b filename=postgresql-e2e-1778706112519775000.log size=179
    uploads_test.go:81: upload CRUD OK: id=ul_de175d9b content_len=179
--- PASS: TestUploads_CRUDLifecycle (0.01s)
=== RUN   TestUploads_NotFound
--- PASS: TestUploads_NotFound (0.00s)
PASS
ok  	helpdesk/testing/e2e	132.524s

=== Test Summary ===
  Total:  52
  Passed: 52
  Failed: 0
Stopping full stack...
HELPDESK_IDENTITY_PROVIDER=none docker compose -f deploy/docker-compose/docker-compose.yaml down -v
[+] Running 10/10
 ✔ Container helpdesk-gateway-1         Removed                                                                                                                                                                                              0.2s
 ✔ Container helpdesk-k8s-agent-1       Removed                                                                                                                                                                                              0.4s
 ✔ Container helpdesk-incident-agent-1  Removed                                                                                                                                                                                              0.4s
 ✔ Container helpdesk-sysadmin-agent-1  Removed                                                                                                                                                                                              0.3s
 ✔ Container helpdesk-research-agent-1  Removed                                                                                                                                                                                              0.3s
 ✔ Container helpdesk-database-agent-1  Removed                                                                                                                                                                                              0.4s
 ✔ Container helpdesk-auditd-1          Removed                                                                                                                                                                                              0.2s
 ✔ Volume helpdesk_audit-data           Removed                                                                                                                                                                                              0.0s
 ✔ Volume helpdesk_incidents            Removed                                                                                                                                                                                              0.0s
 ✔ Network helpdesk_default             Removed                                                                                                                                                                                              0.1s
```

