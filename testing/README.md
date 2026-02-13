# aiHepDesk Testing Strategy

  ## TL;DR: as they say, a picture is worth a thousand words

```
[boris@ ~/helpdesk]$ make test
go test ./...
ok  	helpdesk/agents/database	(cached)
ok  	helpdesk/agents/incident	(cached)
ok  	helpdesk/agents/k8s	(cached)
ok  	helpdesk/agentutil	(cached)
ok  	helpdesk/cmd/gateway	(cached)
ok  	helpdesk/cmd/helpdesk	(cached)
ok  	helpdesk/cmd/srebot	(cached)
ok  	helpdesk/internal/discovery	(cached)
?   	helpdesk/internal/logging	[no test files]
?   	helpdesk/internal/model	[no test files]
ok  	helpdesk/prompts	(cached)
ok  	helpdesk/testing/cmd/faulttest	(cached)
ok  	helpdesk/testing/faultlib	(cached)
?   	helpdesk/testing/testutil	[no test files]


[boris@ ~/helpdesk]$ make integration
Starting test infrastructure...
docker compose -f testing/docker/docker-compose.yaml up -d --wait
[+] Running 4/4
 ✔ Network docker_default            Created                                                                                                                                                                                                 0.0s
 ✔ Volume "docker_pgdata"            Created                                                                                                                                                                                                 0.0s
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 6.4s
 ✔ Container helpdesk-test-pgloader  Healthy                                                                                                                                                                                                 6.3s
Running integration tests...
go test -tags integration -timeout 120s ./testing/integration/...
ok  	helpdesk/testing/integration	(cached)
Stopping test infrastructure...
docker compose -f testing/docker/docker-compose.yaml down -v
[+] Running 4/4
 ✔ Container helpdesk-test-pgloader  Removed                                                                                                                                                                                                10.2s
 ✔ Container helpdesk-test-pg        Removed                                                                                                                                                                                                 0.1s
 ✔ Volume docker_pgdata              Removed                                                                                                                                                                                                 0.0s
 ✔ Network docker_default            Removed                                                                                                                                                                                                 0.2s


[boris@ ~/helpdesk]$ make faulttest
Starting test infrastructure...
docker compose -f testing/docker/docker-compose.yaml up -d --wait
[+] Running 4/4
 ✔ Network docker_default            Created                                                                                                                                                                                                 0.0s
 ✔ Volume "docker_pgdata"            Created                                                                                                                                                                                                 0.0s
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 6.3s
 ✔ Container helpdesk-test-pgloader  Healthy                                                                                                                                                                                                 6.3s
Running fault tests...
go test -tags faulttest -timeout 600s -v ./testing/faulttest/...
SKIP: No agent URLs configured
Set FAULTTEST_DB_AGENT_URL, FAULTTEST_K8S_AGENT_URL, or FAULTTEST_ORCHESTRATOR_URL
ok  	helpdesk/testing/faulttest	0.332s
Stopping test infrastructure...
docker compose -f testing/docker/docker-compose.yaml down -v
[+] Running 4/4
 ✔ Container helpdesk-test-pgloader  Removed                                                                                                                                                                                                10.2s
 ✔ Container helpdesk-test-pg        Removed                                                                                                                                                                                                 0.1s
 ✔ Volume docker_pgdata              Removed                                                                                                                                                                                                 0.0s
 ✔ Network docker_default            Removed                                                                                                                                                                                                 0.2s


[boris@ ~/helpdesk]$ make e2e
Starting full stack...
docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
WARN[0000] Found orphan containers ([docker-compose-orchestrator-run-99b1ef4892c5 docker-compose-orchestrator-run-296f690d90bf docker-compose-orchestrator-run-6d987d8ddd8d docker-compose-orchestrator-run-c7adaf6e28cb docker-compose-orchestrator-run-512a3586c5d0 docker-compose-orchestrator-run-f64674e022dc docker-compose-orchestrator-run-ed295ecc6bbf docker-compose-orchestrator-run-626c276b2cbd docker-compose-orchestrator-run-51d7fe36fe77 docker-compose-orchestrator-run-b4b14664980c docker-compose-orchestrator-run-3c37c363f25e docker-compose-orchestrator-run-41c75715fe0a docker-compose-orchestrator-run-372c4614d7b2 docker-compose-orchestrator-run-b0bcea4f740a]) for this project. If you removed or renamed this service in your compose file, you can run this command with the --remove-orphans flag to clean it up.
[+] Running 6/6
 ✔ Network docker-compose_default             Created                                                                                                                                                                                        0.0s
 ✔ Volume "docker-compose_incidents"          Created                                                                                                                                                                                        0.0s
 ✔ Container docker-compose-database-agent-1  Healthy                                                                                                                                                                                        1.0s
 ✔ Container docker-compose-k8s-agent-1       Healthy                                                                                                                                                                                        1.0s
 ✔ Container docker-compose-incident-agent-1  Healthy                                                                                                                                                                                        1.0s
 ✔ Container docker-compose-gateway-1         Healthy                                                                                                                                                                                        0.9s
Running E2E tests...
go test -tags e2e -timeout 300s -v ./testing/e2e/...
=== RUN   TestGatewayDiscovery
    gateway_test.go:42: Discovered 3 agents:
    gateway_test.go:44:   - postgres_database_agent
    gateway_test.go:44:   - k8s_agent
    gateway_test.go:44:   - incident_agent
--- PASS: TestGatewayDiscovery (0.01s)
=== RUN   TestGatewayHealthCheck
    gateway_test.go:84: Response (1073 chars): ---
        ERROR — check_connection failed for host=localhost port=15432 dbname=testdb user=postgres password=testpass

        connection failed: Connection refused. The PostgreSQL server may not be running, or t...
--- PASS: TestGatewayHealthCheck (2.70s)
=== RUN   TestGatewayAIDiagnosis
    gateway_test.go:111: Sending diagnosis prompt to database agent...
    gateway_test.go:118: Agent response (1963 chars)
--- PASS: TestGatewayAIDiagnosis (6.13s)
=== RUN   TestGatewayQueryUnknownAgent
--- PASS: TestGatewayQueryUnknownAgent (0.00s)
=== RUN   TestGatewayIncidentBundle
    gateway_test.go:196: Creating incident bundle with callback: http://127.0.0.1:51055/callback
    gateway_test.go:208: Incident agent responded (700 chars)
    gateway_test.go:221: Warning: No callback received within 60s (may be expected if incident agent not configured)
--- PASS: TestGatewayIncidentBundle (63.77s)
=== RUN   TestSREBotWorkflow
=== RUN   TestSREBotWorkflow/Phase1_Discovery
    gateway_test.go:250: Found 3 agents
=== RUN   TestSREBotWorkflow/Phase2_HealthCheck
    gateway_test.go:274: Anomaly detected in health check response
=== RUN   TestSREBotWorkflow/Phase3_AIDiagnosis
    gateway_test.go:298: Diagnosis response (2307 chars)
--- PASS: TestSREBotWorkflow (8.00s)
    --- PASS: TestSREBotWorkflow/Phase1_Discovery (0.00s)
    --- PASS: TestSREBotWorkflow/Phase2_HealthCheck (2.62s)
    --- PASS: TestSREBotWorkflow/Phase3_AIDiagnosis (5.37s)
=== RUN   TestMultiAgentIncidentResponse
    multi_agent_test.go:32: E2E_ORCHESTRATOR_URL not set
--- SKIP: TestMultiAgentIncidentResponse (0.00s)
=== RUN   TestFaultInjectionE2E
    multi_agent_test.go:129: Test infrastructure not running (need testing/docker/docker-compose.yaml with pgloader)
--- SKIP: TestFaultInjectionE2E (0.16s)
=== RUN   TestOrchestratorDelegation
    orchestrator_test.go:21: E2E_ORCHESTRATOR_URL not set
--- SKIP: TestOrchestratorDelegation (0.00s)
=== RUN   TestOrchestratorCompoundPrompt
    orchestrator_test.go:86: E2E_ORCHESTRATOR_URL not set
--- SKIP: TestOrchestratorCompoundPrompt (0.00s)
=== RUN   TestDirectAgentCall
    orchestrator_test.go:141: Note: Direct A2A calls may return empty if agent requires specific session context
=== RUN   TestDirectAgentCall/database_agent_direct
    orchestrator_test.go:184: Response (0 chars, 4.69650775s)
    orchestrator_test.go:189: Warning: Agent returned empty response (may be expected for direct A2A without session)
    orchestrator_test.go:190: Empty response from direct A2A call - use gateway tests for reliable E2E testing
=== RUN   TestDirectAgentCall/k8s_agent_direct
    orchestrator_test.go:169: Agent URL or required config not set
--- PASS: TestDirectAgentCall (4.70s)
    --- SKIP: TestDirectAgentCall/database_agent_direct (4.70s)
    --- SKIP: TestDirectAgentCall/k8s_agent_direct (0.00s)
PASS
ok  	helpdesk/testing/e2e	85.983s
Stopping full stack...
docker compose -f deploy/docker-compose/docker-compose.yaml down -v
[+] Running 6/6
 ✔ Container docker-compose-gateway-1         Removed                                                                                                                                                                                        0.2s
 ✔ Container docker-compose-k8s-agent-1       Removed                                                                                                                                                                                        0.2s
 ✔ Container docker-compose-incident-agent-1  Removed                                                                                                                                                                                        0.1s
 ✔ Container docker-compose-database-agent-1  Removed                                                                                                                                                                                        0.1s
 ✔ Volume docker-compose_incidents            Removed                                                                                                                                                                                        0.0s
 ✔ Network docker-compose_default             Removed                                                                                                                                                                                        0.1s


```

aiHelpDesk offers a comprehensive testing strategy that is broken into five distinct layers as follows:

  ## Architecture & Testing Boundaries

```
  ┌─────────────────┬─────────────────────────────────────────┐
  │ Human Operator  │ Upstream Agent / O11y Watcher / SRE Bot │
  ├─────     ───────┤─────────      ──────────────────────────┤
  │ Orchestrator    │ Gateway (REST)                          │  ← Layer 5: E2E
  ├─────────────────┴─────────────────────────────────────────┤
  │ Common:  A2A protocol,  (JSON-RPC 2.0)                    │  ← Layer 4: Protocol
  ├───────────────────────────────────────────────────────────│
  │ Common:  DB Agent,  K8s Agent, Incident Agent             │  ← Layer 3: Integration
  ├───────────────────────────────────────────────────────────│
  │ Common:  psql,  kubectl, OS (storage, compute, network)   │  ← Layer 2: Tool exec
  ├───────────────────────────────────────────────────────────│
  │ Common:  LLM API (Claude / Gemini)                        │  ← External
  │          PostgreSQL, Kubernetes cluster                   │  ← External
  └───────────────────────────────────────────────────────────┘
```

  In this strategy each boundary is a test seam. Each of the five layers progressively require more infrastructure.

  ### Layer 1: Unit Tests

  Goal: Test all deterministic logic without any I/O. Run in `go test ./...` with zero external dependencies.

  Coverage target: 35-40% of statements.

  ### Layer 2: Component Tests (mock external commands)

  Goal: Test the tool functions (the ones that call `psql`, `kubectl`, OS commands) by mocking the command execution. No real PostgreSQL or K8s needed.

  Approach — command injection pattern:

  Instead of each agent's tool functions calling `exec.Command("psql", ...)` or `exec.Command("kubectl", ...)` directly, make them testable, extract the command runner into an interface:

```
  // In agents/database/tools.go (or a shared package):
  type CommandRunner interface {
      Run(ctx context.Context, name string, args ...string) (string, error)
  }

  type execRunner struct{}
  func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
      cmd := exec.CommandContext(ctx, name, args...)
      out, err := cmd.CombinedOutput()
      return string(out), err
  }
```

  Then in tests, provide a mock runner that returns canned output:

```
  type mockRunner struct {
      output string
      err    error
  }
  func (m mockRunner) Run(_ context.Context, _ string, _ ...string) (string, error) {
      return m.output, m.err
  }
```

  What these tests verify:
  - Correct arguments passed to `psql/kubectl`
  - Output parsing and formatting
  - Error diagnosis triggered on failure
  - Tool return values match expected format

  Coverage target: 50-60% of statements.


  ### Layer 3: Integration Tests (real infrastructure, no LLM)

  Goal: Test agents against real PostgreSQL and Kubernetes, but bypass the LLM. Send tool calls directly via A2A without the reasoning step.

  Infrastructure needed:
  - Docker Compose stack
  - Optional: kind cluster for K8s tests

  3a. Database agent integration tests:

```
  testing/integration/database_test.go
```

  Uses Go testing + build tag `//go:build` integration:

```
  //go:build integration

  func TestCheckConnection_RealDB(t *testing.T) {
      // Start agent, send A2A message: "Call the check_connection tool
      // with connection_string=host=localhost port=15432 ..."
      // Verify response contains "version" and "PostgreSQL 16"
  }
```

  Test cases:
  - `check_connection` → healthy DB returns version
  - `get_active_connections` → returns at least the test connection
  - `get_database_stats` → returns cache hit ratio
  - `get_table_stats` → returns tuple counts for test table
  - `check_connection with bad password` → returns auth failure diagnosis
  - `check_connection with stopped DB` → returns connection refused diagnosis

  3b. Gateway integration tests:

```
  testing/integration/gateway_test.go
```

  Start gateway + agents in-process or via Docker, send REST requests:

```
  func TestGateway_ListAgents(t *testing.T) {
      // GET /api/v1/agents → returns discovered agents
  }

  func TestGateway_QueryDBAgent(t *testing.T) {
      // POST /api/v1/query {agent: "database_agent", message: "..."}
      // Verify response has task_id, state, text
  }

  func TestGateway_DBTool(t *testing.T) {
      // POST /api/v1/db/check_connection {connection_string: "..."}
      // Verify response text
  }

  func TestGateway_UnknownAgent(t *testing.T) {
      // POST /api/v1/query {agent: "nonexistent"} → 400
  }
```

  3c. Incident bundle integration test:

```
  testing/integration/incident_test.go
```

  - Call `create_incident_bundle` with real DB connection + optional K8s context
  - Verify tarball is created, extract it, check `manifest.json`, verify layer files
  - Test callback: start local HTTP server, pass as `callback_url`, verify POST received

  3d. A2A protocol conformance tests:

```
  testing/integration/a2a_test.go
```

  - Verify each agent serves `/.well-known/agent-card.json` with correct schema
  - Verify POST /invoke with valid JSON-RPC returns well-formed Task/Message
  - Verify POST /invoke with malformed JSON returns JSON-RPC error
  - Verify discovery → invoke round-trip works for each agent

  3e. Research agent integration tests (Gemini only):

```
  testing/integration/research_agent_test.go
```

  - Verify agent card is served with correct name and description
  - Test basic queries work via A2A protocol
  - Test web search capability (GoogleSearch tool)
  - Note: These tests require a Gemini API key since the research agent uses GoogleSearch which only works with Gemini models

  Makefile target:

```
  integration:
      docker compose -f testing/docker/docker-compose.yaml up -d --wait
      go test -tags integration -timeout 120s ./testing/integration/...
      docker compose -f testing/docker/docker-compose.yaml down -v
```


  ### Layer 4: Fault Injection Tests (with the wired faulttest into go test)

  Goal: Run failure scenarios from failures.yaml (presently 17 of them, still missing SSL mismatch, DNS resolution failure, read-only replica queries, K8s RBAC denial, etc.) as Go tests, producing standard `go test` output alongside the existing JSON report.

  Approach: Create a Go test file that loads the catalog and runs each failure as a subtest:

```
  testing/faulttest/faulttest_test.go
```

```
  //go:build faulttest

  func TestFailures(t *testing.T) {
      catalog, _ := LoadCatalog("../catalog/failures.yaml")
      for _, f := range catalog.Failures {
          t.Run(f.ID, func(t *testing.T) {
              // inject → run → evaluate → teardown
              // t.Fatal on injection error
              // t.Errorf on eval failure
          })
      }
  }
```

  Benefits:
  - Each failure becomes a named subtest: TestFailures/db-max-connections
  - Can run a single failure: `go test -run TestFailures/db-lock-contention`
  - Integrates with CI test reporting
  - Parallelizable per-category (database failures can't run in parallel with each other, but database and K8s categories can)

  Makefile target:

```
  faulttest:
      docker compose -f testing/docker/docker-compose.yaml up -d --wait
      go test -tags faulttest -timeout 600s -v ./testing/faulttest/...
      docker compose -f testing/docker/docker-compose.yaml down -v
```

See [Fault Injection](FAULT_INJECTION_TESTING.md) for details of the aiHelpDesk fault injection mechanism.


  ### Layer 5: End-to-End Tests

  Goal: Test complete user-visible workflows. These involve the LLM, so results are non-deterministic. Use assertion relaxation (keyword matching, not exact string matching, see the evaluator logic for details).

  5a. SRE Bot workflow test:

```
  testing/e2e/srebot_test.go
```

  Start full stack (agents + gateway), run the SRE bot against it:
  - Phase 1: health check → verify agents listed
  - Phase 2: AI diagnosis → verify response mentions the injected fault
  - Phase 3: incident bundle → verify tarball created + callback received

  5b. Orchestrator delegation test:

  Test that the aiHelpDesk Orchestrator correctly routes to sub-agents:

```
  testing/e2e/orchestrator_test.go
```

  - Send `check the database at [conn_string]` → Orchestrator delegates to DB agent
  - Send `list pods in namespace X` → Orchestrator delegates to K8s agent
  - Send compound prompt → Orchestrator uses both agents

  Since these involve real LLM calls, they're expensive and non-deterministic. As such aiHelpDesk runs them gated behind a build tag and treats failures as warnings, not blockers:

```
  //go:build e2e

  func TestOrchestratorDelegation(t *testing.T) {
      if os.Getenv("HELPDESK_API_KEY") == "" {
          t.Skip("HELPDESK_API_KEY not set")
      }
      // ...
  }
```

  5c. Multi-agent incident response test:

  Inject a compound failure (e.g., `compound-db-pod-crash`), send to Orchestrator, verify it:
  1. Calls the database agent (gets `connection refused`)
  2. Calls the K8s agent (identifies `CrashLoopBackOff`)
  3. Synthesizes a root cause explanation
  4. Optionally creates an incident bundle

  This is the most realistic test — it validates the entire system including LLM reasoning quality.

  5d. Research agent E2E tests (Gemini only):

```
  testing/e2e/research_agent_test.go
```

  - Test gateway discovery of research agent
  - Test direct queries to research agent via A2A
  - Test web search capability for real-time information
  - Test orchestrator delegation to research agent for version/release questions
  - Test gateway routing to research agent
  - Note: These tests require Gemini models since GoogleSearch is only available on Gemini

  ### Build tags and make targets:

  Testing build tags and make targets:

```
  test:                             # Unit tests (no infra needed)
      go test ./...

  integration:                      # Real DB, no LLM
      docker compose -f testing/docker/docker-compose.yaml up -d --wait
      go test -tags integration -timeout 120s ./testing/integration/... || true
      docker compose -f testing/docker/docker-compose.yaml down -v

  faulttest:                        # Fault injection (real DB + agents + LLM)
      docker compose -f testing/docker/docker-compose.yaml up -d --wait
      go test -tags faulttest -timeout 600s -v ./testing/faulttest/... || true
      docker compose -f testing/docker/docker-compose.yaml down -v

  e2e:                              # Full stack (requires HELPDESK_API_KEY)
      docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
      go test -tags e2e -timeout 300s -v ./testing/e2e/... || true
      docker compose -f deploy/docker-compose/docker-compose.yaml down -v

  test-all: test integration faulttest
```

  ## Summary: aiHelpDesk Five-Layer Test Pyramid

```
                                  /\
                                 /  \     E2E (Layer 5)
                                / 5c \    LLM + full stack, non-deterministic
                               /──────\   ~5 tests, run manually or nightly
                              /  5a 5b \
                             /──────────\
                            /  Layer 4   \  Fault injection
                           / 17 scenarios \  Real DB + agents + LLM
                          /────────────────\  ~17 tests, run in CI weekly
                         /     Layer 3      \  Integration
                        /   DB + K8s + A2A   \  Real DB, no LLM
                       /──────────────────────\  ~20 tests, run in CI per-PR
                      /    Layer 2: Component  \  Mock commands
                     /     psql/kubectl mocked  \  ~30 tests, fast, no infra
                    /────────────────────────────\
                   /      Layer 1: Unit Tests     \  Pure logic
                  /  diagnosePsqlError, formatAge  \  ~80 tests, <2s
                 /──────────────────────────────────\
  ┌────────────────────┬─────────┬───────────────────────────┬─────────┬──────────────────┐
  │       Layer        │ # Tests │           Infra           │ Runtime │     Trigger      │
  ├────────────────────┼─────────┼───────────────────────────┼─────────┼──────────────────┤
  │ 1. Unit            │ ~80     │ None                      │ <2s     │ Every commit     │
  ├────────────────────┼─────────┼───────────────────────────┼─────────┼──────────────────┤
  │ 2. Component       │ ~30     │ None                      │ <5s     │ Every commit     │
  ├────────────────────┼─────────┼───────────────────────────┼─────────┼──────────────────┤
  │ 3. Integration     │ ~20     │ Docker (PostgreSQL)       │ ~30s    │ Per PR           │
  ├────────────────────┼─────────┼───────────────────────────┼─────────┼──────────────────┤
  │ 4. Fault injection │ 17      │ Docker + agents + LLM API │ ~15min  │ Weekly / release │
  ├────────────────────┼─────────┼───────────────────────────┼─────────┼──────────────────┤
  │ 5. E2E             │ ~5      │ Full stack + LLM API      │ ~5min   │ Manual / nightly │
  └────────────────────┴─────────┴───────────────────────────┴─────────┴──────────────────┘
```
