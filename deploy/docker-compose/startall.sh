#!/usr/bin/env bash
# startall.sh — Start all helpdesk services on a single host (no Docker).
#
# Prerequisites:
#   - psql (PostgreSQL 16+ client) and kubectl on PATH
#   - Environment variables set (or a .env file alongside this script):
#       HELPDESK_MODEL_VENDOR, HELPDESK_MODEL_NAME, HELPDESK_API_KEY
#   - Optionally: HELPDESK_INFRA_CONFIG pointing to an infrastructure.json
#
# Usage:
#   ./startall.sh              # start agents + gateway, then launch orchestrator
#   ./startall.sh --no-repl    # start agents + gateway only (headless)
#
# Logs go to /tmp/helpdesk-*.log. Stop everything with: ./startall.sh --stop

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Source .env if present.
if [[ -f "$SCRIPT_DIR/.env" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "$SCRIPT_DIR/.env"
    set +a
fi

# ---------------------------------------------------------------------------
# Validate
# ---------------------------------------------------------------------------
missing=()
[[ -z "${HELPDESK_MODEL_VENDOR:-}" ]] && missing+=(HELPDESK_MODEL_VENDOR)
[[ -z "${HELPDESK_MODEL_NAME:-}" ]]   && missing+=(HELPDESK_MODEL_NAME)
[[ -z "${HELPDESK_API_KEY:-}" ]]      && missing+=(HELPDESK_API_KEY)
if [[ ${#missing[@]} -gt 0 ]]; then
    echo "ERROR: missing required environment variables: ${missing[*]}" >&2
    echo "Set them in your shell or in $SCRIPT_DIR/.env" >&2
    exit 1
fi

AGENT_URLS="http://localhost:1100,http://localhost:1102,http://localhost:1104"

# Add research agent for Gemini models (GoogleSearch can't be combined with
# function declarations, so we need a dedicated agent for web search)
VENDOR_LC=$(echo "${HELPDESK_MODEL_VENDOR}" | tr '[:upper:]' '[:lower:]')
if [[ "$VENDOR_LC" == "gemini" || "$VENDOR_LC" == "google" ]]; then
    AGENT_URLS="${AGENT_URLS},http://localhost:1106"
fi

PIDS=()
LOGDIR="/tmp"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
cleanup() {
    echo ""
    echo "Stopping helpdesk services..."
    for pid in "${PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    wait 2>/dev/null || true
    echo "All services stopped."
}

start_bg() {
    local name="$1"; shift
    local log="$LOGDIR/helpdesk-${name}.log"
    "$@" > "$log" 2>&1 &
    PIDS+=($!)
    echo "  $name (pid $!) -> $log"
}

# ---------------------------------------------------------------------------
# --stop: kill any running helpdesk processes
# ---------------------------------------------------------------------------
if [[ "${1:-}" == "--stop" ]]; then
    for name in auditd database-agent k8s-agent incident-agent research-agent gateway auditor secbot; do
        pkill -f "helpdesk.*${name}\|${SCRIPT_DIR}/${name}" 2>/dev/null || true
    done
    echo "Sent stop signal to helpdesk services."
    exit 0
fi

trap cleanup EXIT INT TERM

# ---------------------------------------------------------------------------
# Start services
# ---------------------------------------------------------------------------
echo "Starting helpdesk services..."

# Start audit daemon first (AI Governance foundation)
if [[ -x "$SCRIPT_DIR/auditd" ]]; then
    HELPDESK_AUDIT_SOCKET="${HELPDESK_AUDIT_SOCKET:-/tmp/helpdesk-audit.sock}" \
        start_bg auditd "$SCRIPT_DIR/auditd"
    sleep 1
fi

# Configure agents to use audit daemon if running
AUDIT_ENV=()
if [[ -x "$SCRIPT_DIR/auditd" ]]; then
    AUDIT_ENV=(
        HELPDESK_AUDIT_ENABLED=true
        HELPDESK_AUDIT_URL=http://localhost:1199
    )
fi

# Resolve HELPDESK_POLICY_FILE: if set but the file doesn't exist, try the
# bundled policies.example.yaml in the same directory as this script.
if [[ -n "${HELPDESK_POLICY_FILE:-}" && ! -f "$HELPDESK_POLICY_FILE" ]]; then
    fallback="$SCRIPT_DIR/policies.example.yaml"
    if [[ -f "$fallback" ]]; then
        echo "WARN: HELPDESK_POLICY_FILE='$HELPDESK_POLICY_FILE' not found; falling back to $fallback" >&2
        HELPDESK_POLICY_FILE="$fallback"
    else
        echo "ERROR: HELPDESK_POLICY_FILE='$HELPDESK_POLICY_FILE' not found and no policies.example.yaml alongside script." >&2
        exit 1
    fi
fi

env "${AUDIT_ENV[@]}" start_bg database-agent "$SCRIPT_DIR/database-agent"
env "${AUDIT_ENV[@]}" start_bg k8s-agent      "$SCRIPT_DIR/k8s-agent"
env "${AUDIT_ENV[@]}" start_bg incident-agent "$SCRIPT_DIR/incident-agent"

# Start research agent for Gemini models
if [[ "$VENDOR_LC" == "gemini" || "$VENDOR_LC" == "google" ]]; then
    env "${AUDIT_ENV[@]}" start_bg research-agent "$SCRIPT_DIR/research-agent"
fi

# Give agents a moment to bind their ports.
sleep 2

env "${AUDIT_ENV[@]}" \
HELPDESK_AGENT_URLS="$AGENT_URLS" \
HELPDESK_GATEWAY_ADDR="0.0.0.0:8080" \
    start_bg gateway "$SCRIPT_DIR/gateway"

sleep 1
echo "Gateway listening on http://localhost:8080"

# Start optional governance components if --governance flag is set
if [[ "${1:-}" == "--governance" || "${2:-}" == "--governance" ]]; then
    echo ""
    echo "Starting governance components..."
    if [[ -x "$SCRIPT_DIR/auditor" ]]; then
        start_bg auditor "$SCRIPT_DIR/auditor" -socket="${HELPDESK_AUDIT_SOCKET:-/tmp/helpdesk-audit.sock}"
    fi
    if [[ -x "$SCRIPT_DIR/secbot" ]]; then
        start_bg secbot "$SCRIPT_DIR/secbot" -socket="${HELPDESK_AUDIT_SOCKET:-/tmp/helpdesk-audit.sock}" -gateway="http://localhost:8080"
    fi
fi
echo ""

# ---------------------------------------------------------------------------
# Orchestrator (interactive REPL) or headless mode
# ---------------------------------------------------------------------------
if [[ "${1:-}" == "--no-repl" ]]; then
    echo "Running headless (--no-repl). Press Ctrl-C to stop all services."
    wait
else
    echo "Launching interactive orchestrator (type 'exit' to quit)..."
    echo ""
    HELPDESK_AGENT_URLS="$AGENT_URLS" \
    HELPDESK_INFRA_CONFIG="${HELPDESK_INFRA_CONFIG:-}" \
        "$SCRIPT_DIR/helpdesk"
fi
