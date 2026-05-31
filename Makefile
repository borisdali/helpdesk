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

.PHONY: test test-nocache cover test-governance cover-governance test-helm integration integration-governance faulttest faulttest-gateway e2e e2e-governance e2e-identity image push binaries bundle build release github-release clean hashapikey fleet-runner

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
integration-governance:
	go test -tags integration -timeout 120s -v ./testing/integration/governance/...

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
	docker compose -f testing/docker/docker-compose.yaml up -d --wait
	@echo "Running integration tests..."
	-go test -tags integration -timeout 120s -v $(INTEGRATION_PKGS) 2>&1 | tee $(INTEGRATION_LOG)
	@$(SUMMARY_CMD) $(INTEGRATION_LOG)
	@echo "Stopping test infrastructure..."
	docker compose -f testing/docker/docker-compose.yaml down -v

# ---------------------------------------------------------------------------
# Integration tests (requires Docker) - same as above, but with "nocache"
# ---------------------------------------------------------------------------
# Target to force a fresh run by bypassing the cache
integration-nocache:
	@echo "Starting test infrastructure..."
	docker compose -f testing/docker/docker-compose.yaml up -d --wait
	@echo "Running integration tests..."
	-go test --count=1 -tags integration -timeout 120s -v $(INTEGRATION_PKGS) 2>&1 | tee $(INTEGRATION_LOG)
	@$(SUMMARY_CMD) $(INTEGRATION_LOG)
	@echo "Stopping test infrastructure..."
	docker compose -f testing/docker/docker-compose.yaml down -v

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
	@echo "Starting test infrastructure (primary + replica)..."
	docker compose \
		-f testing/docker/docker-compose.yaml \
		-f testing/docker/docker-compose.repl.yaml \
		up -d --wait
	@echo "Running fault tests..."
	-FAULTTEST_REPLICA_CONN_STR="host=localhost port=15433 dbname=testdb user=postgres password=testpass" \
	go test -tags faulttest -timeout 1000s -v ./testing/faulttest/... 2>&1 | tee $(FAULTTEST_LOG)
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
	@echo "Starting test database..."
	docker compose -f testing/docker/docker-compose.yaml up -d --wait
	@echo "Running fault tests via gateway ($(FAULTTEST_GATEWAY_URL))..."
	-FAULTTEST_VIA_GATEWAY=true \
	FAULTTEST_REMEDIATE=true \
	FAULTTEST_EXTERNAL=true \
	FAULTTEST_CONN_STR="host=localhost port=15432 dbname=testdb user=postgres password=testpass" \
	FAULTTEST_AGENT_CONN_STR="faulttest-db" \
	go test -tags faulttest -timeout 1800s -v ./testing/faulttest/... 2>&1 | tee $(FAULTTEST_LOG)
	@$(SUMMARY_CMD) $(FAULTTEST_LOG)

# ---------------------------------------------------------------------------
# End-to-end tests (requires full stack + LLM API key)
# ---------------------------------------------------------------------------
e2e: image
	@echo "Starting full stack..."
	HELPDESK_IDENTITY_PROVIDER=none docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
	@echo "Running E2E tests..."
	-go test -tags e2e -timeout 300s -v ./testing/e2e/... 2>&1 | tee $(E2E_LOG)
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
	sed -e '/^    build:/,/^      dockerfile:/d' \
	    -e 's|image: helpdesk:latest|image: $(IMAGE):$(VERSION)|' \
	    deploy/docker-compose/docker-compose.yaml \
	    > $$bundledir/docker-compose/docker-compose.yaml; \
	\
	echo "==> helm chart"; \
	cp -r deploy/helm/helpdesk $$bundledir/helm/; \
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
