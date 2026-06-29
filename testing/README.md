# aiHepDesk Testing Strategy

  ## TL;DR: as they say, a picture is worth a thousand words

```
[boris@ ~/helpdesk]$ make test-nocache
go test -v --count=1 ./... 2>&1 | tee /tmp/helpdesk-test.log | grep -E "^(ok |FAIL)"
ok  	helpdesk/agents/database	0.287s
ok  	helpdesk/agents/incident	0.517s
ok  	helpdesk/agents/k8s	0.595s
ok  	helpdesk/agents/sysadmin	0.777s
ok  	helpdesk/agentutil	9.661s
ok  	helpdesk/agentutil/retryutil	1.525s
ok  	helpdesk/cmd/auditd	2.454s
ok  	helpdesk/cmd/auditor	2.048s
ok  	helpdesk/cmd/fleet-runner	2.252s
ok  	helpdesk/cmd/gateway	1.856s
ok  	helpdesk/cmd/govbot	2.046s
ok  	helpdesk/cmd/helpdesk	2.074s
ok  	helpdesk/cmd/helpdesk-client	1.764s
ok  	helpdesk/cmd/srebot	2.085s
ok  	helpdesk/internal/audit	2.523s
ok  	helpdesk/internal/authz	1.406s
ok  	helpdesk/internal/client	1.771s
ok  	helpdesk/internal/decisions	1.848s
ok  	helpdesk/internal/discovery	1.398s
ok  	helpdesk/internal/identity	2.536s
ok  	helpdesk/internal/infra	1.521s
ok  	helpdesk/internal/policy	1.347s
ok  	helpdesk/internal/toolregistry	1.279s
ok  	helpdesk/playbooks	1.337s
ok  	helpdesk/prompts	1.024s
ok  	helpdesk/testing/cmd/faulttest	2.901s
ok  	helpdesk/testing/faultlib	1.600s
ok  	helpdesk/testing/helm	1.408s
ok  	helpdesk/testing/testutil	0.940s

=== Test Summary ===
  Total:  2161
  Passed: 2161
  Failed: 0
```

Other than the basic unit tests, the other tests are (much) longer (in time it takes to finish them and in the rather verbose output), so they are presented in separate sample log files. Check out a sample integration test run [here](INTEGRATION_SAMPLE.md), a sample e2e test run [here](E2E_SAMPLE.md) and a sample fault injection run [here](FAULT_INJECTION_TESTING_SAMPLE.md).


  ## Architecture & Testing Boundaries

aiHelpDesk offers a comprehensive testing strategy that is broken into five distinct layers as follows:

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


  ### Layer 4a: Fault Injection Tests — Internal (Docker-compose, no gateway)

  Goal: Run failure scenarios from failures.yaml (currently 32: 19 database, 8 Kubernetes, 3 host, 2 compound) as Go tests, producing standard `go test` output alongside the existing JSON report.

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

See [Fault Injection](FAULT_INJECTION_TESTING.md) for the internal Docker-compose harness details. For the customer-facing external injection guide (SQL-only, SSH, remediation), see [docs/FAULTTEST.md](../docs/FAULTTEST.md).

  ### Layer 4b: Faulttest Gateway Tests — External (live gateway + real infra)

  Goal: Run the subset of faults marked `external_compat: true` against a live gateway and real target infrastructure (staging database, SSH-accessible host). This validates the full agent→gateway→playbook→remediation pipeline, including step approval, audit journeys, and per-version vault metrics. It is the only layer that exercises the complete Vault learning signal end-to-end.

  Infrastructure needed:
  - Running gateway (`HELPDESK_GATEWAY_URL`, `HELPDESK_API_KEY`)
  - Staging target database (tagged `test` or `chaos` in `infrastructure.json`)
  - SSH access for host-level injection (`--ssh-key`, `--ssh-user`, `--ssh-host`)

  Command:

```bash
  faulttest run --external \
    --gateway $HELPDESK_GATEWAY_URL \
    --api-key  $HELPDESK_API_KEY \
    --infra-config /absolute/path/to/infrastructure.json \
    --remediate \
    --approval-mode force    # or omit for interactive gate feedback
```

  What this layer validates that Layer 4a cannot:
  - Gateway playbook resolution and version tracking (`playbook_runs`, `playbook_run_steps`)
  - Step approval gate (Decision Hub polling, `--emit-and-wait`)
  - Audit journeys: diagnostic and remediation traces linked correctly
  - Vault metrics after remediation: step count, recovery time, eval scores
  - `vault list` per-version trend and `vault versions` APPROACH OK column

  Makefile target:

```
  faulttest-gateway:
      faulttest run --external \
        --gateway $(HELPDESK_GATEWAY_URL) --api-key $(HELPDESK_API_KEY) \
        --infra-config $(HELPDESK_INFRA_CONFIG) \
        --remediate --approval-mode force
```

  Trigger: manually before release; optionally nightly on staging.

  ### Layer 4c: Stability Re-certification (`make recertify`)

  Goal: After a playbook version update (via `vault suggest-update` + activation), re-run stability certification for affected series to confirm the new version meets the STABLE threshold (≥80% pass rate, ≤30pp confidence spread). This closes the Vault improvement loop: suggest-update → activate → recertify → STABLE cert.

  Command:

```bash
  # Re-certify a single series after a version update:
  faulttest run db-wal-stale-slot --repeat 5 \
    --gateway $HELPDESK_GATEWAY_URL \
    --api-key  $HELPDESK_API_KEY \
    --infra-config /absolute/path/to/infrastructure.json

  # Then check the result:
  faulttest vault accuracy db-wal-stale-slot \
    --gateway $HELPDESK_GATEWAY_URL --api-key $HELPDESK_API_KEY
```

  Makefile target (fleet-wide, run after any batch of version activations):

```
  recertify:
      faulttest run --external --repeat 5 \
        --gateway $(HELPDESK_GATEWAY_URL) --api-key $(HELPDESK_API_KEY) \
        --infra-config $(HELPDESK_INFRA_CONFIG)
```

  Trigger: manually after `vault suggest-update` cycles; before release to confirm no regressions in promoted playbook versions.


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
  test:                             # Layer 1+2: unit + component (no infra)
      go test ./...

  test-nocache:                     # Same with cache busted — use before committing
      go test -v --count=1 ./...

  integration:                      # Layer 3: real DB, no LLM
      docker compose -f testing/docker/docker-compose.yaml up -d --wait
      go test -tags integration -timeout 120s ./testing/integration/... || true
      docker compose -f testing/docker/docker-compose.yaml down -v

  faulttest:                        # Layer 4a: fault injection (Docker + agents + LLM)
      docker compose -f testing/docker/docker-compose.yaml up -d --wait
      go test -tags faulttest -timeout 600s -v ./testing/faulttest/... || true
      docker compose -f testing/docker/docker-compose.yaml down -v

  faulttest-gateway:                # Layer 4b: external faults against live gateway
      faulttest run --external \
        --gateway $(HELPDESK_GATEWAY_URL) --api-key $(HELPDESK_API_KEY) \
        --infra-config $(HELPDESK_INFRA_CONFIG) \
        --remediate --approval-mode force

  recertify:                        # Layer 4c: re-run stability certs post-activation
      faulttest run --external --repeat 5 \
        --gateway $(HELPDESK_GATEWAY_URL) --api-key $(HELPDESK_API_KEY) \
        --infra-config $(HELPDESK_INFRA_CONFIG)

  e2e:                              # Layer 5: full stack (requires HELPDESK_API_KEY)
      docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
      go test -tags e2e -timeout 300s -v ./testing/e2e/... || true
      docker compose -f deploy/docker-compose/docker-compose.yaml down -v

  test-all: test integration faulttest     # Layers 1–4a; no live infra needed
```

  ## Summary: aiHelpDesk Testing Pyramid

```
                                    /\
                                   /  \     E2E (Layer 5)
                                  / 5c \    LLM + full stack, non-deterministic
                                 /──────\   ~5 tests, run manually or nightly
                                /  5a 5b \
                               /──────────\
                              / Layer 4c   \  Stability re-certification (recertify)
                             / --repeat N   \  Vault flywheel closer; run post-activation
                            /────────────────\
                           /   Layer 4b       \  Faulttest gateway (external)
                          / live gateway+infra \  Full Vault pipeline; run pre-release
                         /──────────────────────\
                        /   Layer 4a             \  Fault injection (internal)
                       / 32 scenarios             \  Docker + agents + LLM; weekly
                      /────────────────────────────\
                     /     Layer 3                  \  Integration
                    /   DB + K8s + A2A               \  Real DB, no LLM; per PR
                   /──────────────────────────────────\
                  /    Layer 2: Component              \  Mock commands
                 /     psql/kubectl mocked              \  ~30 tests, fast, no infra
                /────────────────────────────────────────\
               /      Layer 1: Unit Tests                 \  Pure logic
              /  diagnosePsqlError, formatAge              \  ~1800 tests, <5s
             /──────────────────────────────────────────────\
  ┌─────────────────────┬─────────┬──────────────────────────────┬──────────┬──────────────────────┐
  │        Layer        │ # Tests │             Infra            │ Runtime  │       Trigger        │
  ├─────────────────────┼─────────┼──────────────────────────────┼──────────┼──────────────────────┤
  │ 1. Unit             │ ~1800   │ None                         │ <5s      │ Every commit (CI)    │
  ├─────────────────────┼─────────┼──────────────────────────────┼──────────┼──────────────────────┤
  │ 2. Component        │ ~30     │ None                         │ <5s      │ Every commit (CI)    │
  ├─────────────────────┼─────────┼──────────────────────────────┼──────────┼──────────────────────┤
  │ 3. Integration      │ ~20     │ Docker (PostgreSQL)          │ ~30s     │ Per PR (CI)          │
  ├─────────────────────┼─────────┼──────────────────────────────┼──────────┼──────────────────────┤
  │ 4a. Fault injection │ 32      │ Docker + agents + LLM API    │ ~15min   │ Weekly / release     │
  ├─────────────────────┼─────────┼──────────────────────────────┼──────────┼──────────────────────┤
  │ 4b. Faulttest GW    │ 11      │ Live gateway + staging infra │ ~20min   │ Pre-release / nightly│
  ├─────────────────────┼─────────┼──────────────────────────────┼──────────┼──────────────────────┤
  │ 4c. Recertify       │ per-PB  │ Live gateway + staging infra │ varies   │ Post suggest-update  │
  ├─────────────────────┼─────────┼──────────────────────────────┼──────────┼──────────────────────┤
  │ 5. E2E              │ ~5      │ Full stack + LLM API         │ ~5min    │ Manual / nightly     │
  └─────────────────────┴─────────┴──────────────────────────────┴──────────┴──────────────────────┘
```

  **Why each layer exists and what it cannot delegate downward:**

  | Layer | What it catches that lower layers miss |
  |-------|----------------------------------------|
  | 1–2 (unit/component) | Logic bugs, parser errors, mock-injectable command failures — zero infra cost, runs in CI on every commit |
  | 3 (integration) | Real PostgreSQL behavior, A2A protocol conformance, agent startup wiring |
  | 4a (fault injection) | LLM reasoning quality, full inject→diagnose→evaluate cycle, agent behavior under real faults |
  | 4b (faulttest gateway) | Gateway playbook resolution, step approval gate, audit journeys, Vault metrics pipeline — the only layer that validates the full learning signal |
  | 4c (recertify) | Playbook version regressions after suggest-update; confirms STABLE cert holds on promoted versions |
  | 5 (E2E) | Multi-agent delegation, orchestrator routing, non-deterministic end-to-end user flows |

  **What CI currently gates on every PR (Layers 1–3):**

```yaml
  # .github/workflows/ci.yml
  - Unit tests:                go test ./... (all packages, ~1800 tests)
  - Helm tests:                make test-helm
  - Governance integration:    make integration-governance
  - Integration tests:         make integration
```

  Layers 4a–5 require live infrastructure and LLM API keys that are not available in the GitHub Actions runner. They are run manually or on a schedule against staging. The pre-release checklist should include 4a + 4b + any 4c recertifications triggered by version activations in the release.
