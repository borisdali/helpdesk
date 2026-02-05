# aiHepDesk Testing Strategy

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
