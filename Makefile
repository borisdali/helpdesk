# helpdesk release Makefile
#
# Usage:
#   make test                   Run all unit tests
#   make cover                  Run tests with coverage report (dist/coverage.html)
#   make image                  Build Docker image locally
#   make push                   Build multi-arch image and push to GHCR
#   make binaries               Cross-compile Go binaries (4 platforms)
#   make bundle                 Package deploy files for end-users
#   make build VERSION=v1.0.0   Binaries + bundle, no Docker push
#   make release VERSION=v1.0.0 All of the above (includes push)
#   make github-release VERSION=v1.0.0  release + create GitHub Release
#   make clean                  Remove dist/

VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GIT_SHA   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
# IMAGE_TAG is the immutable tag baked into deploy bundles and Helm charts.
# It embeds the git SHA so the same semver can be rebuilt without Kubernetes
# serving a stale cached image (imagePullPolicy: IfNotPresent won't skip a
# tag it has never seen before).
IMAGE_TAG := $(VERSION)-$(GIT_SHA)
IMAGE     ?= ghcr.io/borisdali/helpdesk
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
DIST      := dist
LDFLAGS   := -s -w -X helpdesk/internal/buildinfo.Version=$(IMAGE_TAG)

# binary:package pairs
BIN_PKGS := \
	database-agent:./agents/database/ \
	k8s-agent:./agents/k8s/ \
	sysadmin-agent:./agents/sysadmin/ \
	incident-agent:./agents/incident/ \
	research-agent:./agents/research/ \
	gateway:./cmd/gateway/ \
	helpdesk:./cmd/helpdesk/ \
	helpdesk-client:./cmd/helpdesk-client/ \
	srebot:./cmd/srebot/ \
	auditd:./cmd/auditd/ \
	auditor:./cmd/auditor/ \
	approvals:./cmd/approvals/ \
	secbot:./cmd/secbot/ \
	govbot:./cmd/govbot/ \
	govexplain:./cmd/govexplain/ \
	hashapikey:./cmd/hashapikey/ \
	fleet-runner:./cmd/fleet-runner/ \
	faulttest:./testing/cmd/faulttest/

.PHONY: test test-nocache cover test-governance cover-governance test-helm integration integration-k8s integration-governance faulttest faulttest-gateway faulttest-gateway-pgbackrest faulttest-pitr recertify e2e e2e-governance e2e-identity image push binaries bundle build release github-release clean hashapikey fleet-runner docs-tools

fleet-runner:
	go build -o fleet-runner ./cmd/fleet-runner/

# ---------------------------------------------------------------------------
# Tests and coverage
# ---------------------------------------------------------------------------

# Target for standard cached tests
test:
	-go test -v $(TESTARGS) ./... 2>&1 | tee $(TEST_LOG) | grep -E "^(ok |FAIL)"
	@$(SUMMARY_CMD) $(TEST_LOG)

# Target to force a fresh run by bypassing the cache
test-nocache:
	-go test -v --count=1 ./... 2>&1 | tee $(TEST_LOG) | grep -E "^(ok |FAIL)"
	@$(SUMMARY_CMD) $(TEST_LOG)

# Regenerate the per-tool reference pages in docs/tools/ from the agents' code.
# The same tests run in `make test` and CI in compare mode and fail when a page
# is stale or a tool lacks a capability or action class.
docs-tools:
	HELPDESK_UPDATE_TOOL_DOCS=1 go test -count=1 -run TestToolReference ./agents/...
	@# capabilities.md is built from the agent pages, so it must come second.
	HELPDESK_UPDATE_TOOL_DOCS=1 go test -count=1 -run TestCapabilityVocabularyDoc ./agentutil/toolref/
	@echo "Regenerated docs/tools/. Review and commit the changes."

# ---------------------------------------------------------------------------
# Helm chart template tests (requires helm in PATH; no Kubernetes cluster needed)
# ---------------------------------------------------------------------------
test-helm:
	go test -v ./testing/helm/...

cover:
	@mkdir -p $(DIST)
	go test -coverprofile=$(DIST)/coverage.out $$(go list ./... | xargs -I{} sh -c 'ls $$(go list -f "{{.Dir}}" {})/*_test.go 2>/dev/null && echo {}' | grep -v '_test\.go$$')
	go tool cover -func=$(DIST)/coverage.out
	go tool cover -html=$(DIST)/coverage.out -o $(DIST)/coverage.html
	@echo "Coverage report: $(DIST)/coverage.html"

# ---------------------------------------------------------------------------
# AI Governance unit tests (no infrastructure required)
# ---------------------------------------------------------------------------
test-governance:
	go test \
		./internal/audit/... \
		./internal/policy/... \
		./agentutil/... \
		./agents/database/... \
		./agents/k8s/... \
		./cmd/auditd/...

cover-governance:
	@mkdir -p $(DIST)
	go test -coverprofile=$(DIST)/coverage-governance.out \
		./internal/audit/... \
		./internal/policy/... \
		./agentutil/... \
		./agents/database/... \
		./agents/k8s/... \
		./cmd/auditd/...
	go tool cover -func=$(DIST)/coverage-governance.out
	go tool cover -html=$(DIST)/coverage-governance.out -o $(DIST)/coverage-governance.html
	@echo "Coverage report: $(DIST)/coverage-governance.html"

# ---------------------------------------------------------------------------
# AI Governance integration tests (no Docker required; builds auditd internally)
# ---------------------------------------------------------------------------
GOTEST_EXTRA ?=

integration-governance:
	go test -tags integration -timeout 300s -v $(GOTEST_EXTRA) ./testing/integration/governance/...

# ---------------------------------------------------------------------------
# Integration tests (requires Docker)
# ---------------------------------------------------------------------------
INTEGRATION_PKGS = ./testing/integration/... ./agents/database/... ./agents/k8s/... ./agents/sysadmin/...
TEST_LOG         = /tmp/helpdesk-test.log
INTEGRATION_LOG  = /tmp/helpdesk-integration.log
E2E_LOG          = /tmp/helpdesk-e2e.log
FAULTTEST_LOG    = /tmp/helpdesk-faulttest.log

# Shared awk program — append a log file path to invoke: $(SUMMARY_CMD) <logfile>
SUMMARY_CMD = awk '/^[[:space:]]*--- PASS:/{p++} /^[[:space:]]*--- FAIL:/{f++; n=$$0; sub(/^[[:space:]]*--- FAIL: /,"",n); fails=fails"\n    "n} END{printf "\n=== Test Summary ===\n  Total:  %d\n  Passed: %d\n  Failed: %d\n",p+f,p,f; if(f>0){print "  Failing tests:"fails}}'

integration:
	@echo "Starting test infrastructure..."
	docker compose -f testing/docker/docker-compose.yaml -f testing/docker/docker-compose.pgbackrest.yaml up -d --wait
	# Real initial backup, taken as an explicit step after the container
	# reports healthy — see init-pgbackrest.sh's own comment for why this
	# can't happen during container init. Without it,
	# TestIntegration_GetPgBackRestStatus_OverRealSSH's LastBackupTime check
	# would fail on every fresh (-v wiped) volume.
	./testing/docker/setup-pgbackrest-backup.sh
	@echo "Running integration tests..."
	# POSTGRES_TEST_DSN: dedicated postgres-auditd service above (port 15434),
	# never the fault-injection-target postgres service (port 15432) — see
	# testing/docker/docker-compose.yaml's own comment for why they must stay
	# separate. Exercises internal/audit's Postgres backend, otherwise never
	# run anywhere.
	-POSTGRES_TEST_DSN="postgres://postgres:auditdtestpass@localhost:15434/auditd_test?sslmode=disable" \
		go test -tags integration -timeout 900s -v $(GOTEST_EXTRA) $(INTEGRATION_PKGS) 2>&1 | tee $(INTEGRATION_LOG)
	@$(SUMMARY_CMD) $(INTEGRATION_LOG)
	@echo "Stopping test infrastructure..."
	docker compose -f testing/docker/docker-compose.yaml -f testing/docker/docker-compose.pgbackrest.yaml down -v

# ---------------------------------------------------------------------------
# Integration tests (requires Docker) - same as above, but with "nocache"
# ---------------------------------------------------------------------------
# Target to force a fresh run by bypassing the cache
integration-nocache:
	@echo "Starting test infrastructure..."
	docker compose -f testing/docker/docker-compose.yaml -f testing/docker/docker-compose.pgbackrest.yaml up -d --wait
	./testing/docker/setup-pgbackrest-backup.sh
	@echo "Running integration tests..."
	-POSTGRES_TEST_DSN="postgres://postgres:auditdtestpass@localhost:15434/auditd_test?sslmode=disable" \
		go test --count=1 -tags integration -timeout 900s -v $(INTEGRATION_PKGS) 2>&1 | tee $(INTEGRATION_LOG)
	@$(SUMMARY_CMD) $(INTEGRATION_LOG)
	@echo "Stopping test infrastructure..."
	docker compose -f testing/docker/docker-compose.yaml -f testing/docker/docker-compose.pgbackrest.yaml down -v

# ---------------------------------------------------------------------------
# K8s integration tests (requires a cluster; destructive to one namespace)
# ---------------------------------------------------------------------------
# In-cluster: the restore lifecycle tools run in the helpdesk stack's own
# sysadmin agent (its service account, RBAC, in-cluster kubectl), called
# through its POST /tool/{name} endpoint via a port-forward, against a real
# crash: the demo pod runs the stock postgres entrypoint, so a missing
# pg_control means CrashLoopBackOff. The tests crash and restore the
# database in a dedicated namespace that must end in "-destruct", never one a
# real or test database lives in (db holds the agents' own target). The
# target creates the namespace and the pod if needed, takes a fresh full
# backup and archives WAL past it (CHECKPOINT so the switch isn't a no-op,
# then pgbackrest check), as on any running server. Requires the stack
# deployed with testing/k8s/helm-values-pgbackrest.yaml (its
# pgbackrest-k8s-destruct entry). Not part of `make integration`, which only
# brings up Docker; there these tests skip.
#
#   make integration-k8s                       # context minikube, ns db-destruct
#   make integration-k8s K8S_CONTEXT=other K8S_DESTRUCT_NS=pg-destruct K8S_DESTRUCT_TARGET=...
# ---------------------------------------------------------------------------
K8S_CONTEXT         ?= minikube
K8S_DESTRUCT_NS     ?= db-destruct
K8S_DESTRUCT_TARGET ?= pgbackrest-k8s-destruct
K8S_HELPDESK_NS     ?= helpdesk-system
K8S_SYSADMIN_PORT   ?= 11103
# Bearer token for the sysadmin agent's /tool endpoint when the release enforces identity (empty = none).
K8S_SYSADMIN_API_KEY ?=
INTEGRATION_K8S_LOG = /tmp/helpdesk-integration-k8s.log

integration-k8s:
	@case "$(K8S_DESTRUCT_NS)" in *-destruct) ;; *) echo "Error: K8S_DESTRUCT_NS=$(K8S_DESTRUCT_NS) must end in -destruct (these tests crash and restore its database)"; exit 1;; esac
	kubectl --context $(K8S_CONTEXT) get namespace $(K8S_DESTRUCT_NS) >/dev/null 2>&1 || kubectl --context $(K8S_CONTEXT) create namespace $(K8S_DESTRUCT_NS)
	kubectl --context $(K8S_CONTEXT) apply -n $(K8S_DESTRUCT_NS) -f testing/k8s/pgbackrest-demo.yaml
	@# A pod a previous run left crash-looping (pg_control removed, then the run died
	@# before it could repair it) must not stop this one: the test harness repairs it
	@# from the existing backup before testing (it needs K8S_SYSADMIN_API_KEY on a
	@# release that enforces identity). So waiting for it is best-effort, and the fresh
	@# backup is taken only when the pod is up to take one.
	-kubectl --context $(K8S_CONTEXT) -n $(K8S_DESTRUCT_NS) rollout status statefulset/pgbackrest-demo --timeout=180s
	@if kubectl --context $(K8S_CONTEXT) -n $(K8S_DESTRUCT_NS) wait --for=condition=Ready pod/pgbackrest-demo-0 --timeout=20s; then \
		kubectl --context $(K8S_CONTEXT) -n $(K8S_DESTRUCT_NS) exec pgbackrest-demo-0 -- su postgres -c 'pgbackrest --stanza=main --type=full backup' 2>&1 | grep -v "WARN: environment contains invalid option"; \
		kubectl --context $(K8S_CONTEXT) -n $(K8S_DESTRUCT_NS) exec pgbackrest-demo-0 -- su postgres -c 'psql -d testdb -qc CHECKPOINT && pgbackrest --stanza=main check' 2>&1 | grep -v "WARN: environment contains invalid option"; \
	else \
		echo "pgbackrest-demo-0 is not ready: no fresh backup was taken; the test harness will repair it from the existing backup"; \
	fi
	@kubectl --context $(K8S_CONTEXT) -n $(K8S_HELPDESK_NS) port-forward svc/helpdesk-sysadmin-agent $(K8S_SYSADMIN_PORT):1103 >/tmp/helpdesk-k8s-sysadmin-pf.log 2>&1 & PF=$$!; \
		trap "kill $$PF 2>/dev/null" EXIT; sleep 3; \
		HELPDESK_K8S_SYSADMIN_URL=http://localhost:$(K8S_SYSADMIN_PORT) HELPDESK_K8S_SYSADMIN_TARGET=$(K8S_DESTRUCT_TARGET) HELPDESK_K8S_SYSADMIN_API_KEY=$(K8S_SYSADMIN_API_KEY) \
		HELPDESK_K8S_PGBACKREST_CONTEXT=$(K8S_CONTEXT) HELPDESK_K8S_PGBACKREST_NAMESPACE=$(K8S_DESTRUCT_NS) \
		go test --count=1 -tags integration -timeout 1800s -v -run 'K8s' ./agents/sysadmin/... 2>&1 | tee $(INTEGRATION_K8S_LOG); true
	@$(SUMMARY_CMD) $(INTEGRATION_K8S_LOG)

# ---------------------------------------------------------------------------
# Fault injection tests (requires Docker + agents + LLM API key)
# ---------------------------------------------------------------------------
# Required env vars (export before running):
#   FAULTTEST_DB_AGENT_URL       e.g. http://localhost:1102  (database agent)
#   FAULTTEST_SYSADMIN_AGENT_URL e.g. http://localhost:1103  (sysadmin agent)
#     └─ sysadmin MUST be started with HELPDESK_INFRA_CONFIG=testing/testing.infra.json
#        so it resolves the test container name (helpdesk-test-pg) correctly.
#   FAULTTEST_K8S_AGENT_URL      e.g. http://localhost:1104  (k8s agent, optional)
# ---------------------------------------------------------------------------
faulttest:
	@echo "Starting test infrastructure (primary + replica + pgBackRest)..."
	docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		-f testing/docker/docker-compose.pgbackrest.yaml \
		up -d --wait
	./testing/docker/setup-pgbackrest-backup.sh
	@echo "Running fault tests..."
	-FAULTTEST_REPLICA_CONN_STR="host=localhost port=15433 dbname=testdb user=postgres password=testpass" \
	go test -tags faulttest -timeout 3600s -v ./testing/faulttest/... 2>&1 | tee $(FAULTTEST_LOG)
	@$(SUMMARY_CMD) $(FAULTTEST_LOG)
	@echo "Stopping test infrastructure..."
	docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		-f testing/docker/docker-compose.pgbackrest.yaml \
		down -v

# Target to force a fresh run by bypassing the Go test cache
faulttest-nocache:
	@echo "Starting test infrastructure (primary + replica + pgBackRest)..."
	docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		-f testing/docker/docker-compose.pgbackrest.yaml \
		up -d --wait
	./testing/docker/setup-pgbackrest-backup.sh
	@echo "Running fault tests..."
	-FAULTTEST_REPLICA_CONN_STR="host=localhost port=15433 dbname=testdb user=postgres password=testpass" \
	go test --count=1 -tags faulttest -timeout 3600s -v ./testing/faulttest/... 2>&1 | tee $(FAULTTEST_LOG)
	@$(SUMMARY_CMD) $(FAULTTEST_LOG)
	@echo "Stopping test infrastructure..."
	docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		-f testing/docker/docker-compose.pgbackrest.yaml \
		down -v

# ---------------------------------------------------------------------------
# Fast fault suite: everything except k8s-node-memory-pressure, which alone
# needs up to ~25 minutes on a real node (it waits for an actual kubelet
# eviction event — see testing/catalog/failures.yaml). Use this for routine
# iteration; use faulttest/faulttest-nocache for full-suite correctness runs,
# or faulttest-memory-pressure to exercise just the slow one in isolation.
# ---------------------------------------------------------------------------
faulttest-fast:
	@echo "Starting test infrastructure (primary + replica + pgBackRest)..."
	docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		-f testing/docker/docker-compose.pgbackrest.yaml \
		up -d --wait
	./testing/docker/setup-pgbackrest-backup.sh
	@echo "Running fault tests (excluding k8s-node-memory-pressure)..."
	-FAULTTEST_REPLICA_CONN_STR="host=localhost port=15433 dbname=testdb user=postgres password=testpass" \
	FAULTTEST_EXCLUDE_IDS="k8s-node-memory-pressure" \
	go test -tags faulttest -timeout 2400s -v ./testing/faulttest/... 2>&1 | tee $(FAULTTEST_LOG)
	@$(SUMMARY_CMD) $(FAULTTEST_LOG)
	@echo "Stopping test infrastructure..."
	docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		-f testing/docker/docker-compose.pgbackrest.yaml \
		down -v

# Target to force a fresh run by bypassing the Go test cache
faulttest-fast-nocache:
	@echo "Starting test infrastructure (primary + replica + pgBackRest)..."
	docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		-f testing/docker/docker-compose.pgbackrest.yaml \
		up -d --wait
	./testing/docker/setup-pgbackrest-backup.sh
	@echo "Running fault tests (excluding k8s-node-memory-pressure)..."
	-FAULTTEST_REPLICA_CONN_STR="host=localhost port=15433 dbname=testdb user=postgres password=testpass" \
	FAULTTEST_EXCLUDE_IDS="k8s-node-memory-pressure" \
	go test --count=1 -tags faulttest -timeout 2400s -v ./testing/faulttest/... 2>&1 | tee $(FAULTTEST_LOG)
	@$(SUMMARY_CMD) $(FAULTTEST_LOG)
	@echo "Stopping test infrastructure..."
	docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		-f testing/docker/docker-compose.pgbackrest.yaml \
		down -v

# ---------------------------------------------------------------------------
# k8s-node-memory-pressure in isolation — the slow fault excluded above.
# Requires the target K8s cluster to have memory-based eviction-hard
# configured (minikube disables this by default — see the fault's own
# `prerequisites` text in testing/catalog/failures.yaml for the fix).
# ---------------------------------------------------------------------------
faulttest-memory-pressure:
	@echo "Starting test infrastructure (primary + replica)..."
	docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		up -d --wait
	@echo "Running k8s-node-memory-pressure..."
	-FAULTTEST_REPLICA_CONN_STR="host=localhost port=15433 dbname=testdb user=postgres password=testpass" \
	FAULTTEST_IDS="k8s-node-memory-pressure" \
	go test --count=1 -tags faulttest -timeout 2400s -v ./testing/faulttest/... 2>&1 | tee $(FAULTTEST_LOG)
	@$(SUMMARY_CMD) $(FAULTTEST_LOG)
	@echo "Stopping test infrastructure..."
	docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		down -v

# ---------------------------------------------------------------------------
# Fault injection tests via gateway playbooks (requires a live gateway)
# ---------------------------------------------------------------------------
# Routes diagnosis through gateway playbooks and runs remediation.
# The gateway must already be running; the test Postgres is started automatically.
#
# Required env vars (export before running):
#   FAULTTEST_GATEWAY_URL        e.g. http://localhost:8080  (gateway)
#   FAULTTEST_API_KEY            gateway API key (or HELPDESK_CLIENT_API_KEY)
#
# Quickest way to get a live gateway:
#   HELPDESK_IDENTITY_PROVIDER=none \
#   docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
# ---------------------------------------------------------------------------
faulttest-gateway:
	@if [ -z "$(FAULTTEST_GATEWAY_URL)" ]; then \
		echo "Error: FAULTTEST_GATEWAY_URL is not set"; \
		echo "  export FAULTTEST_GATEWAY_URL=http://localhost:8080"; \
		exit 1; \
	fi
	@if [ -z "$(FAULTTEST_API_KEY)" ]; then \
		echo "Error: FAULTTEST_API_KEY is not set"; \
		echo "  The gateway defaults to HELPDESK_IDENTITY_PROVIDER=static, which"; \
		echo "  requires a Bearer token on every request. users.example.yaml ships"; \
		echo "  a pre-hashed 'faulttest' service account for exactly this purpose:"; \
		echo "    export FAULTTEST_API_KEY=faulttest-api-key"; \
		exit 1; \
	fi
	@if ! curl -sf "$(FAULTTEST_GATEWAY_URL)/api/v1/agents" >/dev/null 2>&1; then \
		echo "Error: Gateway not reachable at $(FAULTTEST_GATEWAY_URL)"; \
		echo "  Start the full stack first:"; \
		echo "    docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait"; \
		exit 1; \
	fi
	@echo "Starting test database..."
	docker compose -f testing/docker/docker-compose.yaml up -d --wait
	@echo "Running fault tests via gateway ($(FAULTTEST_GATEWAY_URL))..."
	-FAULTTEST_VIA_GATEWAY=true \
	FAULTTEST_REMEDIATE=true \
	FAULTTEST_EXTERNAL=true \
	FAULTTEST_GATE_ESCALATION=true \
	FAULTTEST_API_KEY="$(FAULTTEST_API_KEY)" \
	FAULTTEST_CONN_STR="host=localhost port=15432 dbname=testdb user=postgres password=testpass" \
	FAULTTEST_AGENT_CONN_STR="faulttest-db" \
	go test -tags faulttest -timeout 3600s -v ./testing/faulttest/... 2>&1 | tee $(FAULTTEST_LOG)
	@$(SUMMARY_CMD) $(FAULTTEST_LOG)

# Target to force a fresh run by bypassing the Go test cache
faulttest-gateway-nocache:
	@if [ -z "$(FAULTTEST_GATEWAY_URL)" ]; then \
		echo "Error: FAULTTEST_GATEWAY_URL is not set"; \
		echo "  export FAULTTEST_GATEWAY_URL=http://localhost:8080"; \
		exit 1; \
	fi
	@if [ -z "$(FAULTTEST_API_KEY)" ]; then \
		echo "Error: FAULTTEST_API_KEY is not set"; \
		echo "  The gateway defaults to HELPDESK_IDENTITY_PROVIDER=static, which"; \
		echo "  requires a Bearer token on every request. users.example.yaml ships"; \
		echo "  a pre-hashed 'faulttest' service account for exactly this purpose:"; \
		echo "    export FAULTTEST_API_KEY=faulttest-api-key"; \
		exit 1; \
	fi
	@if ! curl -sf "$(FAULTTEST_GATEWAY_URL)/api/v1/agents" >/dev/null 2>&1; then \
		echo "Error: Gateway not reachable at $(FAULTTEST_GATEWAY_URL)"; \
		echo "  Start the full stack first:"; \
		echo "    docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait"; \
		exit 1; \
	fi
	@echo "Starting test database..."
	docker compose -f testing/docker/docker-compose.yaml up -d --wait
	@echo "Running fault tests via gateway ($(FAULTTEST_GATEWAY_URL))..."
	-FAULTTEST_VIA_GATEWAY=true \
	FAULTTEST_REMEDIATE=true \
	FAULTTEST_EXTERNAL=true \
	FAULTTEST_GATE_ESCALATION=true \
	FAULTTEST_API_KEY="$(FAULTTEST_API_KEY)" \
	FAULTTEST_CONN_STR="host=localhost port=15432 dbname=testdb user=postgres password=testpass" \
	FAULTTEST_AGENT_CONN_STR="faulttest-db" \
	go test --count=1 -tags faulttest -timeout 3600s -v ./testing/faulttest/... 2>&1 | tee $(FAULTTEST_LOG)
	@$(SUMMARY_CMD) $(FAULTTEST_LOG)

# ---------------------------------------------------------------------------
# pgBackRest faults via gateway playbooks (requires a live gateway)
# ---------------------------------------------------------------------------
# The pgBackRest faults are Docker-only, so faulttest-gateway (external_compat
# only) never runs them, and make faulttest (direct A2A) can't check what
# defines them: the playbook endings and the objective-evidence gate
# (expected_diagnosis.objective_evidence_signal is enforced only via the
# gateway). This target runs them the way they are meant to run:
#   1. health triage, diagnosis only: db-pgbackrest-repo-unreadable,
#      db-pgbackrest-wal-gap — the broken-chain case must stop at a human,
#      so nothing is approved past it;
#   2. data loss with remediation in force mode: db-pgdata-corrupted and
#      db-pgdata-corrupted-unarchived-tail — the gateway auto-chains triage →
#      restore_from_backup, and the tail fault's verify_sql checks the commits
#      that were only in pg_wal came back.
#
# Starts only the pgBackRest container and never tears anything down, so the
# auditd database of a locally running stack (:15434) is left alone.
#
# Required env vars: FAULTTEST_GATEWAY_URL, FAULTTEST_API_KEY (as above).
# The gateway's infrastructure.json must contain "pgbackrest-db"
# (testing/testing.infra.json has it).
# ---------------------------------------------------------------------------
PGBACKREST_CONN_STR = host=localhost port=15435 dbname=testdb user=postgres password=testpass

faulttest-gateway-pgbackrest:
	@if [ -z "$(FAULTTEST_GATEWAY_URL)" ] || [ -z "$(FAULTTEST_API_KEY)" ]; then \
		echo "Error: FAULTTEST_GATEWAY_URL and FAULTTEST_API_KEY must be set"; \
		echo "  export FAULTTEST_GATEWAY_URL=http://localhost:8080"; \
		echo "  export FAULTTEST_API_KEY=<gateway API key>"; \
		exit 1; \
	fi
	@if ! curl -sf -H "Authorization: Bearer $(FAULTTEST_API_KEY)" "$(FAULTTEST_GATEWAY_URL)/api/v1/agents" >/dev/null 2>&1; then \
		echo "Error: Gateway not reachable at $(FAULTTEST_GATEWAY_URL)"; \
		exit 1; \
	fi
	@echo "Starting pgBackRest test database..."
	docker compose -f testing/docker/docker-compose.yaml -f testing/docker/docker-compose.pgbackrest.yaml \
		up -d --wait postgres-pgbackrest
	./testing/docker/setup-pgbackrest-backup.sh
	@echo "Running pgBackRest health-triage faults via gateway (diagnosis only)..."
	-FAULTTEST_VIA_GATEWAY=true \
	FAULTTEST_IDS=db-pgbackrest-repo-unreadable,db-pgbackrest-wal-gap \
	FAULTTEST_CONN_STR="$(PGBACKREST_CONN_STR)" \
	go test --count=1 -tags faulttest -timeout 1200s -v -run TestFaultInjection ./testing/faulttest/... 2>&1 | tee $(FAULTTEST_LOG)
	@echo "Running db-pgdata-corrupted faults via gateway (restore remediation, force mode)..."
	-FAULTTEST_VIA_GATEWAY=true \
	FAULTTEST_REMEDIATE=true \
	FAULTTEST_IDS=db-pgdata-corrupted,db-pgdata-corrupted-unarchived-tail \
	FAULTTEST_CONN_STR="$(PGBACKREST_CONN_STR)" \
	go test --count=1 -tags faulttest -timeout 1200s -v -run TestFaultInjection ./testing/faulttest/... 2>&1 | tee -a $(FAULTTEST_LOG)
	@$(SUMMARY_CMD) $(FAULTTEST_LOG)

# Point-in-time recovery fault on the Docker pgBackRest fixture, through the
# gateway, with a live model. Restores (and stops) the running fixture, so run
# it alone. The agents must reach the fixture: use the host-process stack
# (see docs/PITR.md §8), not the Compose agents.
#   FAULTTEST_GATEWAY_URL        e.g. http://localhost:8080
#   FAULTTEST_API_KEY            gateway key
#   FAULTTEST_SYSADMIN_AGENT_URL e.g. http://localhost:1103
#   FAULTTEST_SYSADMIN_API_KEY   the agent's /tool key (defaults to FAULTTEST_API_KEY)
#   PITR_FAULT_IDS               default db-pitr-dropped-table
faulttest-pitr:
	@if [ -z "$(FAULTTEST_GATEWAY_URL)" ] || [ -z "$(FAULTTEST_API_KEY)" ] || [ -z "$(FAULTTEST_SYSADMIN_AGENT_URL)" ]; then \
		echo "Error: FAULTTEST_GATEWAY_URL, FAULTTEST_API_KEY and FAULTTEST_SYSADMIN_AGENT_URL must be set"; \
		exit 1; \
	fi
	@if ! curl -sf -H "Authorization: Bearer $(FAULTTEST_API_KEY)" "$(FAULTTEST_GATEWAY_URL)/api/v1/agents" >/dev/null 2>&1; then \
		echo "Error: Gateway not reachable at $(FAULTTEST_GATEWAY_URL)"; \
		exit 1; \
	fi
	@echo "Starting pgBackRest test database..."
	docker compose -f testing/docker/docker-compose.yaml -f testing/docker/docker-compose.pgbackrest.yaml \
		up -d --wait postgres-pgbackrest
	./testing/docker/setup-pgbackrest-backup.sh
	@echo "Running the point-in-time recovery fault via gateway..."
	-FAULTTEST_VIA_GATEWAY=true \
	FAULTTEST_REMEDIATE=true \
	FAULTTEST_GATE_ESCALATION=true \
	FAULTTEST_APPROVAL_MODE=force \
	FAULTTEST_SYSADMIN_AGENT_URL="$(FAULTTEST_SYSADMIN_AGENT_URL)" \
	FAULTTEST_SYSADMIN_API_KEY="$(if $(FAULTTEST_SYSADMIN_API_KEY),$(FAULTTEST_SYSADMIN_API_KEY),$(FAULTTEST_API_KEY))" \
	FAULTTEST_IDS=$(if $(PITR_FAULT_IDS),$(PITR_FAULT_IDS),db-pitr-dropped-table) \
	FAULTTEST_CONN_STR="$(PGBACKREST_CONN_STR)" \
	go test --count=1 -tags faulttest -timeout 1200s -v -run TestFaultInjection ./testing/faulttest/... 2>&1 | tee $(FAULTTEST_LOG)
	@$(SUMMARY_CMD) $(FAULTTEST_LOG)

# ---------------------------------------------------------------------------
# Stability certification — auto-certify all external-compatible faults
# ---------------------------------------------------------------------------
# Runs each fault N times via the gateway and posts a STABLE/UNSTABLE cert to
# auditd. Certs are visible in 'vault list' (STABLE column) and 'vault accuracy'.
# Writes one JSON report per fault (faulttest-{runID}-{faultID}.json) so results
# can be inspected individually without grepping a combined blob.
#
# Required:
#   FAULTTEST_GATEWAY_URL     e.g. http://localhost:8080
#
# Optional:
#   FAULTTEST_CONN_STR        injection DSN (default: local test DB started here)
#   FAULTTEST_AGENT_CONN_STR  alias sent to the agent (default: faulttest-db)
#   FAULTTEST_INFRA_CONFIG    path to infrastructure.json for safety-tag check
#   RECERTIFY_REPEAT          runs per fault (default: 5)
#   CATEGORIES                comma-separated categories to certify, e.g. database
#   FAULT_IDS                 comma-separated fault IDs (overrides CATEGORIES)
#
# Judge (optional — adds semantic scoring on top of keyword/tool scores):
#   RECERTIFY_JUDGE=1          enable LLM-as-judge
#   RECERTIFY_JUDGE_VENDOR     judge vendor, pinned independently of the
#                              diagnosis model under test: anthropic or google.
#                              Leave unset to fall back to HELPDESK_MODEL_VENDOR
#                              (the pre-existing behavior — same vendor as
#                              whatever the agent under test is running,
#                              which is almost never what you want for a real
#                              model-swap evaluation: the judge would swap
#                              right along with the model being judged).
#   RECERTIFY_JUDGE_MODEL      judge model (e.g. claude-haiku-4-5-20251001).
#                              Falls back to HELPDESK_MODEL_NAME when unset.
#   RECERTIFY_JUDGE_API_KEY    judge API key. Falls back to HELPDESK_API_KEY
#                              when unset — only sufficient if the judge
#                              vendor matches whichever key that is.
# ---------------------------------------------------------------------------
RECERTIFY_LOG    = /tmp/helpdesk-recertify.log
RECERTIFY_REPEAT ?= 5
FAULTTEST_CONN_STR ?= host=localhost port=15432 dbname=testdb user=postgres password=testpass
FAULTTEST_AGENT_CONN_STR ?= faulttest-db

recertify:
	@if [ -z "$(FAULTTEST_GATEWAY_URL)" ]; then \
		echo "Error: FAULTTEST_GATEWAY_URL is not set"; \
		echo "  export FAULTTEST_GATEWAY_URL=http://localhost:8080"; \
		exit 1; \
	fi
	@if ! curl -sf "$(FAULTTEST_GATEWAY_URL)/api/v1/agents" >/dev/null 2>&1; then \
		echo "Error: Gateway not reachable at $(FAULTTEST_GATEWAY_URL)"; \
		echo "  Start the full stack first:"; \
		echo "    HELPDESK_IDENTITY_PROVIDER=none \\"; \
		echo "    docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait"; \
		exit 1; \
	fi
	@echo "Starting test database..."
	docker compose -f testing/docker/docker-compose.yaml up -d --wait
	@echo "Certifying faults (repeat=$(RECERTIFY_REPEAT), gateway=$(FAULTTEST_GATEWAY_URL))..."
	-FAULTTEST_CONN_STR="$(FAULTTEST_CONN_STR)" \
	FAULTTEST_AGENT_CONN_STR="$(FAULTTEST_AGENT_CONN_STR)" \
	go run ./testing/cmd/faulttest run \
		--external \
		--via-gateway \
		--gateway "$(FAULTTEST_GATEWAY_URL)" \
		--repeat $(RECERTIFY_REPEAT) \
		--approval-mode force \
		--report-per-fault \
		$(if $(CATEGORIES),--categories "$(CATEGORIES)",) \
		$(if $(FAULT_IDS),--ids "$(FAULT_IDS)",) \
		$(if $(FAULTTEST_INFRA_CONFIG),--infra-config "$(FAULTTEST_INFRA_CONFIG)",) \
		$(if $(RECERTIFY_JUDGE),--judge,) \
		$(if $(RECERTIFY_JUDGE_VENDOR),--judge-vendor "$(RECERTIFY_JUDGE_VENDOR)",) \
		$(if $(RECERTIFY_JUDGE_MODEL),--judge-model "$(RECERTIFY_JUDGE_MODEL)",) \
		$(if $(RECERTIFY_JUDGE_API_KEY),--judge-api-key "$(RECERTIFY_JUDGE_API_KEY)",) \
		2>&1 | tee $(RECERTIFY_LOG)
	@echo ""
	@echo "Certification results:"
	@go run ./testing/cmd/faulttest vault list \
		--gateway "$(FAULTTEST_GATEWAY_URL)" \
		$(if $(CATEGORIES),--categories "$(CATEGORIES)",) \
		$(if $(FAULT_IDS),--ids "$(FAULT_IDS)",) \
		2>&1

# ---------------------------------------------------------------------------
# End-to-end tests (requires full stack + LLM API key)
# ---------------------------------------------------------------------------
e2e: image
	@echo "Tearing down any existing stack..."
	HELPDESK_IDENTITY_PROVIDER=none docker compose -f deploy/docker-compose/docker-compose.yaml down -v 2>/dev/null || true
	@echo "Starting full stack..."
	HELPDESK_IDENTITY_PROVIDER=none HELPDESK_BASE_URL=http://localhost:8080 docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
	@echo "Verifying localhost:8080 actually reaches this stack (not a stale process holding the port)..."
	@health="$$(curl -sf http://localhost:8080/health)"; \
	actual="$$(printf '%s' "$$health" | grep -o '"version":"[^"]*"' | cut -d'"' -f4)"; \
	if [ -z "$$actual" ] || [ "$$actual" != "$(IMAGE_TAG)" ]; then \
		echo "ERROR: localhost:8080 did not answer as this build (got version '$$actual', want '$(IMAGE_TAG)')."; \
		echo "  Something else is bound to :8080 -- a stale kubectl port-forward, another docker-compose project, etc. -- not this stack's gateway."; \
		echo "  On macOS/Docker Desktop this can happen silently: the container's own healthcheck (which docker compose --wait is watching) never touches the host's port 8080, so it reports Healthy even when the host port is actually owned by something else."; \
		echo "  Find the culprit with: lsof -i :8080  (or) ps aux | grep -i port-forward"; \
		exit 1; \
	fi
	@echo "Running E2E tests..."
	-E2E_USER=alice@example.com go test -tags e2e -timeout 300s -v ./testing/e2e/... 2>&1 | tee $(E2E_LOG)
	@$(SUMMARY_CMD) $(E2E_LOG)
	@echo "Stopping full stack..."
	HELPDESK_IDENTITY_PROVIDER=none docker compose -f deploy/docker-compose/docker-compose.yaml down -v

# ---------------------------------------------------------------------------
# AI Governance E2E tests (requires full stack; API key only for audit-trail tests)
# ---------------------------------------------------------------------------
e2e-governance: image
	@echo "Starting full stack..."
	HELPDESK_IDENTITY_PROVIDER=none docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
	@echo "Waiting for gateway to be ready..."
	@for i in $$(seq 1 30); do \
		curl -sf http://localhost:8080/api/v1/agents >/dev/null 2>&1 && echo "Gateway ready." && break; \
		echo "  waiting ($$i/30)..."; sleep 3; \
	done
	@echo "Running governance E2E tests..."
	-go test -tags e2e -timeout 300s -v -run TestGovernance ./testing/e2e/... 2>&1 | tee $(E2E_LOG)
	@$(SUMMARY_CMD) $(E2E_LOG)
	@echo "Stopping full stack..."
	HELPDESK_IDENTITY_PROVIDER=none docker compose -f deploy/docker-compose/docker-compose.yaml down -v

# ---------------------------------------------------------------------------
# Identity & Access E2E tests (requires full stack; API key only for gateway tests)
# ---------------------------------------------------------------------------
e2e-identity: image
	@echo "Starting full stack..."
	HELPDESK_IDENTITY_PROVIDER=none docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
	@echo "Waiting for gateway to be ready..."
	@for i in $$(seq 1 30); do \
		curl -sf http://localhost:8080/api/v1/agents >/dev/null 2>&1 && echo "Gateway ready." && break; \
		echo "  waiting ($$i/30)..."; sleep 3; \
	done
	@echo "Running Identity & Access E2E tests..."
	-go test -tags e2e -timeout 300s -v -run TestIdentityE2E ./testing/e2e/... 2>&1 | tee $(E2E_LOG)
	@$(SUMMARY_CMD) $(E2E_LOG)
	@echo "Stopping full stack..."
	HELPDESK_IDENTITY_PROVIDER=none docker compose -f deploy/docker-compose/docker-compose.yaml down -v

# ---------------------------------------------------------------------------
# Docker image (local, current arch)
# ---------------------------------------------------------------------------
image:
	docker build --load --build-arg VERSION=$(IMAGE_TAG) \
		-t $(IMAGE):$(IMAGE_TAG) \
		-t $(IMAGE):$(VERSION) \
		-t helpdesk:latest \
		-f Dockerfile ..

# ---------------------------------------------------------------------------
# Docker image (multi-arch, push to GHCR)
# ---------------------------------------------------------------------------
push:
	@if [ -n "$$(git status --porcelain Dockerfile)" ]; then \
		echo "ERROR: Dockerfile has uncommitted changes — commit before releasing"; exit 1; fi
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--provenance=false \
		--build-arg VERSION=$(IMAGE_TAG) \
		-t $(IMAGE):$(IMAGE_TAG) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		-f Dockerfile --push ..

# ---------------------------------------------------------------------------
# Cross-compiled Go binaries
# ---------------------------------------------------------------------------
binaries:
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		outdir=$(DIST)/helpdesk-$(VERSION)-$$os-$$arch; \
		echo "==> $$os/$$arch"; \
		mkdir -p $$outdir; \
		for pair in $(BIN_PKGS); do \
			bin=$${pair%%:*}; \
			pkg=$${pair#*:}; \
			echo "    $$bin ($$pkg)"; \
			GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
				go build -ldflags="$(LDFLAGS)" -o $$outdir/$$bin $$pkg || exit 1; \
		done; \
		cp deploy/host/startall.sh $$outdir/; \
		cp deploy/host/README.md $$outdir/; \
		cp deploy/host/.env.example $$outdir/; \
		cp deploy/docker-compose/infrastructure.json.example $$outdir/; \
		cp policies.example.yaml $$outdir/; \
		cp users.example.yaml $$outdir/; \
		cp agents.json $$outdir/; \
		cp agents/database/objective_evidence.yaml $$outdir/objective_evidence-database.yaml; \
		cp agents/k8s/objective_evidence.yaml $$outdir/objective_evidence-k8s.yaml; \
		cp agents/sysadmin/objective_evidence.yaml $$outdir/objective_evidence-sysadmin.yaml; \
		if [ "$$os" = "linux" ]; then \
			cp -r deploy/host/systemd $$outdir/systemd; \
			chmod +x $$outdir/systemd/install-systemd.sh; \
		fi; \
		tar -czf $(DIST)/helpdesk-$(VERSION)-$$os-$$arch.tar.gz \
			-C $(DIST) helpdesk-$(VERSION)-$$os-$$arch; \
		rm -rf $$outdir; \
	done
	@echo "Binaries in $(DIST)/"

# ---------------------------------------------------------------------------
# Deploy bundle (docker-compose + helm chart, no build sections)
# ---------------------------------------------------------------------------
bundle:
	@bundledir=$(DIST)/helpdesk-$(VERSION)-deploy; \
	mkdir -p $$bundledir/docker-compose $$bundledir/helm $$bundledir/host $$bundledir/scripts; \
	\
	echo "==> docker-compose files"; \
	cp deploy/docker-compose/.env.example $$bundledir/docker-compose/; \
	cp deploy/docker-compose/infrastructure.json.example $$bundledir/docker-compose/; \
	cp policies.example.yaml $$bundledir/docker-compose/; \
	cp users.example.yaml $$bundledir/docker-compose/; \
	cp agents/database/objective_evidence.yaml $$bundledir/docker-compose/objective_evidence-database.yaml; \
	cp agents/k8s/objective_evidence.yaml $$bundledir/docker-compose/objective_evidence-k8s.yaml; \
	cp agents/sysadmin/objective_evidence.yaml $$bundledir/docker-compose/objective_evidence-sysadmin.yaml; \
	sed -e '/^    build:/,/^      dockerfile:/d' \
	    -e 's|image: helpdesk:latest|image: $(IMAGE):$(VERSION)|' \
	    -e 's|../../agents/database/objective_evidence.yaml|./objective_evidence-database.yaml|' \
	    -e 's|../../agents/k8s/objective_evidence.yaml|./objective_evidence-k8s.yaml|' \
	    -e 's|../../agents/sysadmin/objective_evidence.yaml|./objective_evidence-sysadmin.yaml|' \
	    deploy/docker-compose/docker-compose.yaml \
	    > $$bundledir/docker-compose/docker-compose.yaml; \
	\
	echo "==> helm chart"; \
	cp -r deploy/helm/helpdesk $$bundledir/helm/; \
	cp -r deploy/helm/examples $$bundledir/helm/; \
	cp deploy/docker-compose/infrastructure.json.example $$bundledir/helm/; \
	cp policies.example.yaml $$bundledir/helm/; \
	cp users.example.yaml $$bundledir/helm/; \
	sed -i.bak \
	    -e 's|^  repository: helpdesk|  repository: $(IMAGE)|' \
	    -e 's|^  tag: latest|  tag: $(IMAGE_TAG)|' \
	    $$bundledir/helm/helpdesk/values.yaml; \
	rm -f $$bundledir/helm/helpdesk/values.yaml.bak; \
	\
	echo "==> host deploy files"; \
	cp deploy/host/startall.sh $$bundledir/host/; \
	cp deploy/host/README.md $$bundledir/host/; \
	cp deploy/host/.env.example $$bundledir/host/; \
	cp deploy/docker-compose/infrastructure.json.example $$bundledir/host/; \
	cp policies.example.yaml $$bundledir/host/; \
	cp agents/database/objective_evidence.yaml $$bundledir/host/objective_evidence-database.yaml; \
	cp agents/k8s/objective_evidence.yaml $$bundledir/host/objective_evidence-k8s.yaml; \
	cp agents/sysadmin/objective_evidence.yaml $$bundledir/host/objective_evidence-sysadmin.yaml; \
	cp -r deploy/host/systemd $$bundledir/host/systemd; \
	chmod +x $$bundledir/host/startall.sh $$bundledir/host/systemd/install-systemd.sh; \
	\
	echo "==> helper scripts"; \
	cp scripts/gateway-repl.sh $$bundledir/scripts/; \
	cp scripts/k8s-local-repl.sh $$bundledir/scripts/; \
	cp scripts/run-fleet-job.sh $$bundledir/scripts/; \
	cp scripts/show-fleet-job.sh $$bundledir/scripts/; \
	cp scripts/README.md $$bundledir/scripts/; \
	chmod +x $$bundledir/scripts/*.sh; \
	\
	tar -czf $(DIST)/helpdesk-$(VERSION)-deploy.tar.gz \
		-C $(DIST) helpdesk-$(VERSION)-deploy; \
	rm -rf $$bundledir
	@echo "Bundle: $(DIST)/helpdesk-$(VERSION)-deploy.tar.gz"

# ---------------------------------------------------------------------------
# Local build (binaries + bundle, no Docker push)
# ---------------------------------------------------------------------------
build: binaries bundle
	@echo ""
	@echo "Build $(VERSION) complete. Artifacts in $(DIST)/:"
	@ls -1 $(DIST)/

# ---------------------------------------------------------------------------
# Full release
# ---------------------------------------------------------------------------
release: push build
	@echo ""
	@echo "Release $(VERSION) complete. Artifacts in $(DIST)/:"
	@ls -1 $(DIST)/

# ---------------------------------------------------------------------------
# GitHub Release (requires gh CLI: https://cli.github.com)
# ---------------------------------------------------------------------------
github-release: release
	git tag -f $(VERSION)
	git push origin refs/tags/$(VERSION)
	gh release create $(VERSION) $(DIST)/*.tar.gz \
		--title "$(VERSION)" \
		--generate-notes

# ---------------------------------------------------------------------------
# Clean
# ---------------------------------------------------------------------------
clean:
	rm -rf $(DIST)
