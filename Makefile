# helpdesk release Makefile
#
# Usage:
#   make image                  Build Docker image locally
#   make push                   Build multi-arch image and push to GHCR
#   make binaries               Cross-compile Go binaries (4 platforms)
#   make bundle                 Package deploy files for end-users
#   make release VERSION=v1.0.0 All of the above
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
	gateway:./cmd/gateway/ \
	helpdesk:./cmd/helpdesk/ \
	srebot:./cmd/srebot/

.PHONY: image push binaries bundle release clean

# ---------------------------------------------------------------------------
# Docker image (local, current arch)
# ---------------------------------------------------------------------------
image:
	docker build -t $(IMAGE):$(VERSION) -f Dockerfile ..

# ---------------------------------------------------------------------------
# Docker image (multi-arch, push to GHCR)
# ---------------------------------------------------------------------------
push:
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
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
	mkdir -p $$bundledir/docker-compose $$bundledir/helm; \
	\
	echo "==> docker-compose files"; \
	cp deploy/docker-compose/.env.example $$bundledir/docker-compose/; \
	cp deploy/docker-compose/infrastructure.json.example $$bundledir/docker-compose/; \
	sed -e '/^    build:/,/^      dockerfile:/d' \
	    -e 's|image: helpdesk:latest|image: $(IMAGE):$(VERSION)|' \
	    deploy/docker-compose/docker-compose.yaml \
	    > $$bundledir/docker-compose/docker-compose.yaml; \
	\
	echo "==> helm chart"; \
	cp -r deploy/helm/helpdesk $$bundledir/helm/; \
	sed -i.bak \
	    -e 's|^  repository: helpdesk|  repository: $(IMAGE)|' \
	    -e 's|^  tag: latest|  tag: $(VERSION)|' \
	    $$bundledir/helm/helpdesk/values.yaml; \
	rm -f $$bundledir/helm/helpdesk/values.yaml.bak; \
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
# Clean
# ---------------------------------------------------------------------------
clean:
	rm -rf $(DIST)
