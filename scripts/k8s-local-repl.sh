#!/usr/bin/env bash
#
# k8s-local-repl.sh - Run the helpdesk orchestrator locally while agents run in K8s
#
# This script works around the ADK REPL bug in containers by:
# 1. Port-forwarding all agent services from K8s to localhost
# 2. Running the orchestrator binary locally (where the REPL works correctly)
#
# Usage:
#   ./scripts/k8s-local-repl.sh [namespace]
#
# Prerequisites:
#   - kubectl configured with access to the cluster
#   - helpdesk binary in PATH or current directory
#   - HELPDESK_API_KEY environment variable set (or in .env file)

set -e

NAMESPACE="${1:-helpdesk-system}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log() { echo -e "${GREEN}[local-repl]${NC} $1"; }
warn() { echo -e "${YELLOW}[local-repl]${NC} $1"; }
err() { echo -e "${RED}[local-repl]${NC} $1" >&2; }

# Cleanup function
cleanup() {
    log "Stopping port-forwards..."
    kill $(jobs -p) 2>/dev/null || true
    wait 2>/dev/null || true
    log "Cleanup complete"
}
trap cleanup EXIT INT TERM

# Load .env if present
if [[ -f "$PROJECT_DIR/.env" ]]; then
    log "Loading $PROJECT_DIR/.env"
    set -a
    source "$PROJECT_DIR/.env"
    set +a
elif [[ -f "$PROJECT_DIR/deploy/docker-compose/.env" ]]; then
    log "Loading $PROJECT_DIR/deploy/docker-compose/.env"
    set -a
    source "$PROJECT_DIR/deploy/docker-compose/.env"
    set +a
fi

# Check required env vars
if [[ -z "$HELPDESK_API_KEY" ]]; then
    err "HELPDESK_API_KEY is not set. Export it or add to .env file."
    exit 1
fi

# Set defaults
export HELPDESK_MODEL_VENDOR="${HELPDESK_MODEL_VENDOR:-anthropic}"
export HELPDESK_MODEL_NAME="${HELPDESK_MODEL_NAME:-claude-haiku-4-5-20251001}"

# Find helpdesk binary
HELPDESK_BIN=""
for path in "./helpdesk" "$PROJECT_DIR/helpdesk" "$(which helpdesk 2>/dev/null)"; do
    if [[ -x "$path" ]]; then
        HELPDESK_BIN="$path"
        break
    fi
done

if [[ -z "$HELPDESK_BIN" ]]; then
    warn "helpdesk binary not found. Building..."
    (cd "$PROJECT_DIR" && go build -o helpdesk ./cmd/helpdesk/)
    HELPDESK_BIN="$PROJECT_DIR/helpdesk"
fi

log "Using binary: $HELPDESK_BIN"
log "Namespace: $NAMESPACE"
log "Model: $HELPDESK_MODEL_VENDOR/$HELPDESK_MODEL_NAME"

# Check if pods are running
log "Checking agent pods..."
if ! kubectl -n "$NAMESPACE" get pods | grep -q "Running"; then
    err "No running pods found in namespace $NAMESPACE"
    exit 1
fi

# Start port-forwards
log "Starting port-forwards..."

kubectl -n "$NAMESPACE" port-forward svc/helpdesk-database-agent 1100:1100 &
PF_DB=$!
sleep 0.5

kubectl -n "$NAMESPACE" port-forward svc/helpdesk-k8s-agent 1102:1102 &
PF_K8S=$!
sleep 0.5

kubectl -n "$NAMESPACE" port-forward svc/helpdesk-incident-agent 1104:1104 &
PF_INC=$!
sleep 0.5

# Verify port-forwards are running
sleep 1
for pid in $PF_DB $PF_K8S $PF_INC; do
    if ! kill -0 $pid 2>/dev/null; then
        err "Port-forward failed to start (PID $pid)"
        exit 1
    fi
done

log "Port-forwards active:"
log "  - Database agent: localhost:1100"
log "  - K8s agent:      localhost:1102"
log "  - Incident agent: localhost:1104"

# Set agent URLs for local orchestrator
export HELPDESK_DATABASE_AGENT_URL="http://localhost:1100"
export HELPDESK_K8S_AGENT_URL="http://localhost:1102"
export HELPDESK_INCIDENT_AGENT_URL="http://localhost:1104"

# Copy infrastructure config from ConfigMap if not present locally
if [[ -z "$HELPDESK_INFRA_CONFIG" ]]; then
    INFRA_TMP="/tmp/helpdesk-infrastructure.json"
    log "Fetching infrastructure config from ConfigMap..."
    if kubectl -n "$NAMESPACE" get configmap helpdesk-infrastructure -o jsonpath='{.data.infrastructure\.json}' > "$INFRA_TMP" 2>/dev/null; then
        export HELPDESK_INFRA_CONFIG="$INFRA_TMP"
        log "Infrastructure config: $INFRA_TMP"
    else
        warn "No infrastructure ConfigMap found, orchestrator will run without inventory"
    fi
fi

echo ""
log "Starting interactive orchestrator..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Run the orchestrator
exec "$HELPDESK_BIN"
