# Build context must be the cassiopeia root (parent of helpdesk/) so that
# both helpdesk/ and ADK/ are accessible. Example:
#   docker build -t helpdesk:dev -f helpdesk/Dockerfile .
# Or via Docker Compose (see deploy/docker-compose/docker-compose.yaml).

# Stage 1: Build all helpdesk binaries.
FROM golang:1.25 AS builder

WORKDIR /src

# Copy the local ADK source (referenced by go.mod replace directive).
COPY ADK/github/adk-go /src/adk-go

# Copy helpdesk source.
COPY helpdesk /src/helpdesk

WORKDIR /src/helpdesk

# Rewrite the replace directive to use the in-container path.
RUN go mod edit -replace google.golang.org/adk=/src/adk-go

# Download dependencies and build all binaries.
RUN go mod download
RUN CGO_ENABLED=0 go build -o /out/database-agent  ./agents/database/
RUN CGO_ENABLED=0 go build -o /out/k8s-agent       ./agents/k8s/
RUN CGO_ENABLED=0 go build -o /out/incident-agent   ./agents/incident/
RUN CGO_ENABLED=0 go build -o /out/gateway          ./cmd/gateway/
RUN CGO_ENABLED=0 go build -o /out/helpdesk         ./cmd/helpdesk/
RUN CGO_ENABLED=0 go build -o /out/srebot           ./cmd/srebot/

# Stage 2: Runtime image with psql and kubectl.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    gnupg \
    lsb-release \
    && rm -rf /var/lib/apt/lists/*

# PostgreSQL 16 client.
RUN echo "deb http://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main" \
    > /etc/apt/sources.list.d/pgdg.list \
    && curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc \
    | gpg --dearmor -o /etc/apt/trusted.gpg.d/postgresql.gpg \
    && apt-get update \
    && apt-get install -y --no-install-recommends postgresql-client-16 \
    && rm -rf /var/lib/apt/lists/*

# kubectl.
RUN ARCH=$(dpkg --print-architecture) \
    && curl -fsSL "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/${ARCH}/kubectl" \
    -o /usr/local/bin/kubectl \
    && chmod +x /usr/local/bin/kubectl

# Copy binaries from builder.
COPY --from=builder /out/database-agent  /usr/local/bin/database-agent
COPY --from=builder /out/k8s-agent       /usr/local/bin/k8s-agent
COPY --from=builder /out/incident-agent  /usr/local/bin/incident-agent
COPY --from=builder /out/gateway         /usr/local/bin/gateway
COPY --from=builder /out/helpdesk        /usr/local/bin/helpdesk
COPY --from=builder /out/srebot          /usr/local/bin/srebot

# Incident bundles default directory.
RUN mkdir -p /data/incidents
ENV HELPDESK_INCIDENT_DIR=/data/incidents

# No default CMD — each service specifies its own command.
