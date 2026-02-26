# helpdesk release Makefile
#
# Usage:
#   make test                   Run all unit tests
#   make cover                  Run tests with coverage report (dist/coverage.html)
#   make image                  Build Docker image locally
#   make push                   Build multi-arch image and push to GHCR
#   make binaries               Cross-compile Go binaries (4 platforms)
#   make bundle                 Package deploy files for end-users
#   make release VERSION=v1.0.0 All of the above
#   make github-release VERSION=v1.0.0  release + create GitHub Release
#   make clean                  Remove dist/

VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE     ?= ghcr.io/borisdali/helpdesk
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
DIST      := dist
LDFLAGS   := -s -w

# binary:package pairs
BIN_PKGS := \
	database-agent:./agents/database/ \
	k8s-agent:./agents/k8s/ \
	incident-agent:./agents/incident/ \
	research-agent:./agents/research/ \
	gateway:./cmd/gateway/ \
	helpdesk:./cmd/helpdesk/ \
	srebot:./cmd/srebot/ \
	auditd:./cmd/auditd/ \
	auditor:./cmd/auditor/ \
	approvals:./cmd/approvals/ \
	secbot:./cmd/secbot/ \
	govbot:./cmd/govbot/ \
	govexplain:./cmd/govexplain/

.PHONY: test cover test-governance cover-governance test-helm integration integration-governance faulttest e2e e2e-governance image push binaries bundle release github-release clean

# ---------------------------------------------------------------------------
# Tests and coverage
# ---------------------------------------------------------------------------
test:
	go test ./...

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
		./cmd/auditd/...

cover-governance:
	@mkdir -p $(DIST)
	go test -coverprofile=$(DIST)/coverage-governance.out \
		./internal/audit/... \
		./internal/policy/... \
		./agentutil/... \
		./cmd/auditd/...
	go tool cover -func=$(DIST)/coverage-governance.out
	go tool cover -html=$(DIST)/coverage-governance.out -o $(DIST)/coverage-governance.html
	@echo "Coverage report: $(DIST)/coverage-governance.html"

# ---------------------------------------------------------------------------
# AI Governance integration tests (no Docker required; builds auditd internally)
# ---------------------------------------------------------------------------
integration-governance:
	go test -tags integration -timeout 120s ./testing/integration/governance/...

# ---------------------------------------------------------------------------
# Integration tests (requires Docker)
# ---------------------------------------------------------------------------
integration:
	@echo "Starting test infrastructure..."
	docker compose -f testing/docker/docker-compose.yaml up -d --wait
	@echo "Running integration tests..."
	-go test -tags integration -timeout 120s ./testing/integration/...
	@echo "Stopping test infrastructure..."
	docker compose -f testing/docker/docker-compose.yaml down -v

# ---------------------------------------------------------------------------
# Fault injection tests (requires Docker + agents + LLM API key)
# ---------------------------------------------------------------------------
faulttest:
	@echo "Starting test infrastructure..."
	docker compose -f testing/docker/docker-compose.yaml up -d --wait
	@echo "Running fault tests..."
	-go test -tags faulttest -timeout 600s -v ./testing/faulttest/...
	@echo "Stopping test infrastructure..."
	docker compose -f testing/docker/docker-compose.yaml down -v

# ---------------------------------------------------------------------------
# End-to-end tests (requires full stack + LLM API key)
# ---------------------------------------------------------------------------
e2e:
	@echo "Starting full stack..."
	docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
	@echo "Running E2E tests..."
	-go test -tags e2e -timeout 300s -v ./testing/e2e/...
	@echo "Stopping full stack..."
	docker compose -f deploy/docker-compose/docker-compose.yaml down -v

# ---------------------------------------------------------------------------
# AI Governance E2E tests (requires full stack; API key only for audit-trail tests)
# ---------------------------------------------------------------------------
e2e-governance: image
	@echo "Starting full stack..."
	docker compose -f deploy/docker-compose/docker-compose.yaml up -d --wait
	@echo "Waiting for gateway to be ready..."
	@for i in $$(seq 1 30); do \
		curl -sf http://localhost:8080/api/v1/agents >/dev/null 2>&1 && echo "Gateway ready." && break; \
		echo "  waiting ($$i/30)..."; sleep 3; \
	done
	@echo "Running governance E2E tests..."
	-go test -tags e2e -timeout 300s -v -run TestGovernance ./testing/e2e/...
	@echo "Stopping full stack..."
	docker compose -f deploy/docker-compose/docker-compose.yaml down -v

# ---------------------------------------------------------------------------
# Docker image (local, current arch)
# ---------------------------------------------------------------------------
image:
	docker build --load -t $(IMAGE):$(VERSION) -t helpdesk:latest -f Dockerfile ..

# ---------------------------------------------------------------------------
# Docker image (multi-arch, push to GHCR)
# ---------------------------------------------------------------------------
push:
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--provenance=false \
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
		cp deploy/docker-compose/.env.example $$outdir/; \
		cp deploy/docker-compose/infrastructure.json.example $$outdir/; \
		cp policies.example.yaml $$outdir/; \
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
	mkdir -p $$bundledir/docker-compose $$bundledir/helm $$bundledir/scripts; \
	\
	echo "==> docker-compose files"; \
	cp deploy/docker-compose/.env.example $$bundledir/docker-compose/; \
	cp deploy/docker-compose/infrastructure.json.example $$bundledir/docker-compose/; \
	cp policies.example.yaml $$bundledir/docker-compose/; \
	sed -e '/^    build:/,/^      dockerfile:/d' \
	    -e 's|image: helpdesk:latest|image: $(IMAGE):$(VERSION)|' \
	    deploy/docker-compose/docker-compose.yaml \
	    > $$bundledir/docker-compose/docker-compose.yaml; \
	\
	echo "==> helm chart"; \
	cp -r deploy/helm/helpdesk $$bundledir/helm/; \
	cp deploy/docker-compose/infrastructure.json.example $$bundledir/helm/; \
	cp policies.example.yaml $$bundledir/helm/; \
	sed -i.bak \
	    -e 's|^  repository: helpdesk|  repository: $(IMAGE)|' \
	    -e 's|^  tag: latest|  tag: $(VERSION)|' \
	    $$bundledir/helm/helpdesk/values.yaml; \
	rm -f $$bundledir/helm/helpdesk/values.yaml.bak; \
	\
	echo "==> helper scripts"; \
	cp scripts/gateway-repl.sh $$bundledir/scripts/; \
	cp scripts/k8s-local-repl.sh $$bundledir/scripts/; \
	cp scripts/README.md $$bundledir/scripts/; \
	chmod +x $$bundledir/scripts/*.sh; \
	\
	tar -czf $(DIST)/helpdesk-$(VERSION)-deploy.tar.gz \
		-C $(DIST) helpdesk-$(VERSION)-deploy; \
	rm -rf $$bundledir
	@echo "Bundle: $(DIST)/helpdesk-$(VERSION)-deploy.tar.gz"

# ---------------------------------------------------------------------------
# Full release
# ---------------------------------------------------------------------------
release: push binaries bundle
	@echo ""
	@echo "Release $(VERSION) complete. Artifacts in $(DIST)/:"
	@ls -1 $(DIST)/

# ---------------------------------------------------------------------------
# GitHub Release (requires gh CLI: https://cli.github.com)
# ---------------------------------------------------------------------------
github-release: release
	git tag -f $(VERSION)
	git push origin $(VERSION)
	gh release create $(VERSION) $(DIST)/*.tar.gz \
		--title "$(VERSION)" \
		--generate-notes

# ---------------------------------------------------------------------------
# Clean
# ---------------------------------------------------------------------------
clean:
	rm -rf $(DIST)
