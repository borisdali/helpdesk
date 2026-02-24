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

# Resolve HELPDESK_POLICY_FILE before starting any service so that auditd and
# agents all see the same resolved path.
# Relative paths are interpreted relative to SCRIPT_DIR (where .env lives).
# Skip resolution and validation entirely when policy enforcement is explicitly
# disabled — the file path is irrelevant in that case.
if [[ -n "${HELPDESK_POLICY_FILE:-}" && "${HELPDESK_POLICY_ENABLED:-}" != "false" ]]; then
    if [[ "$HELPDESK_POLICY_FILE" != /* ]]; then
        HELPDESK_POLICY_FILE="$SCRIPT_DIR/$HELPDESK_POLICY_FILE"
    fi
    if [[ ! -f "$HELPDESK_POLICY_FILE" ]]; then
        fallback="$SCRIPT_DIR/policies.example.yaml"
        if [[ -f "$fallback" ]]; then
            echo "WARN: HELPDESK_POLICY_FILE='$HELPDESK_POLICY_FILE' not found; falling back to $fallback" >&2
            HELPDESK_POLICY_FILE="$fallback"
        else
            echo "ERROR: HELPDESK_POLICY_FILE='$HELPDESK_POLICY_FILE' not found and no policies.example.yaml alongside script." >&2
            exit 1
        fi
    fi
fi

# Infer HELPDESK_POLICY_ENABLED from HELPDESK_POLICY_FILE when not set explicitly
# (backward compat — operators should set HELPDESK_POLICY_ENABLED=true in .env explicitly).
if [[ -z "${HELPDESK_POLICY_ENABLED:-}" && -n "${HELPDESK_POLICY_FILE:-}" ]]; then
    HELPDESK_POLICY_ENABLED="true"
fi
POLICY_ENABLED="${HELPDESK_POLICY_ENABLED:-false}"

# If policy enforcement is enabled but no file was resolved, fall back to the
# bundled example or fail early rather than crashing later with an obscure error.
if [[ "$POLICY_ENABLED" == "true" && -z "${HELPDESK_POLICY_FILE:-}" ]]; then
    fallback="$SCRIPT_DIR/policies.example.yaml"
    if [[ -f "$fallback" ]]; then
        echo "WARN: HELPDESK_POLICY_ENABLED=true but HELPDESK_POLICY_FILE not set; using $fallback" >&2
        HELPDESK_POLICY_FILE="$fallback"
    else
        echo "ERROR: HELPDESK_POLICY_ENABLED=true but HELPDESK_POLICY_FILE is not set and no policies.example.yaml found alongside startall.sh." >&2
        exit 1
    fi
fi

# Configure agents to use audit daemon if running.
# HELPDESK_AUDIT_ENABLED defaults to true when auditd is present, but can be
# overridden to "false" in .env or the shell to disable audit logging entirely.
AUDIT_ENABLED=""
AUDIT_URL=""
if [[ -x "$SCRIPT_DIR/auditd" ]]; then
    AUDIT_ENABLED="${HELPDESK_AUDIT_ENABLED:-true}"
    AUDIT_URL="http://localhost:1199"
fi

# Start audit daemon first (AI Governance foundation)
if [[ -x "$SCRIPT_DIR/auditd" ]]; then
    HELPDESK_AUDIT_SOCKET="${HELPDESK_AUDIT_SOCKET:-/tmp/helpdesk-audit.sock}" \
    HELPDESK_POLICY_ENABLED="$POLICY_ENABLED" \
        start_bg auditd "$SCRIPT_DIR/auditd"
    sleep 1
fi

HELPDESK_AUDIT_ENABLED="$AUDIT_ENABLED" HELPDESK_AUDIT_URL="$AUDIT_URL" HELPDESK_POLICY_ENABLED="$POLICY_ENABLED" start_bg database-agent "$SCRIPT_DIR/database-agent"
HELPDESK_AUDIT_ENABLED="$AUDIT_ENABLED" HELPDESK_AUDIT_URL="$AUDIT_URL" HELPDESK_POLICY_ENABLED="$POLICY_ENABLED" start_bg k8s-agent      "$SCRIPT_DIR/k8s-agent"
HELPDESK_AUDIT_ENABLED="$AUDIT_ENABLED" HELPDESK_AUDIT_URL="$AUDIT_URL" HELPDESK_POLICY_ENABLED="$POLICY_ENABLED" start_bg incident-agent "$SCRIPT_DIR/incident-agent"

# Start research agent for Gemini models
if [[ "$VENDOR_LC" == "gemini" || "$VENDOR_LC" == "google" ]]; then
    HELPDESK_AUDIT_ENABLED="$AUDIT_ENABLED" HELPDESK_AUDIT_URL="$AUDIT_URL" HELPDESK_POLICY_ENABLED="$POLICY_ENABLED" start_bg research-agent "$SCRIPT_DIR/research-agent"
fi

# Give agents a moment to bind their ports.
sleep 2

HELPDESK_AUDIT_ENABLED="$AUDIT_ENABLED" HELPDESK_AUDIT_URL="$AUDIT_URL" \
HELPDESK_POLICY_ENABLED="$POLICY_ENABLED" \
HELPDESK_AGENT_URLS="$AGENT_URLS" \
HELPDESK_GATEWAY_ADDR="0.0.0.0:8080" \
    start_bg gateway "$SCRIPT_DIR/gateway"

sleep 1
echo "Gateway listening on http://localhost:8080"
if [[ "$AUDIT_ENABLED" == "true" ]]; then
    echo "Auditing: enabled  ($AUDIT_URL)"
else
    echo "Auditing: disabled"
fi
if [[ "$POLICY_ENABLED" == "true" ]]; then
    echo "Policy:   enabled  (${HELPDESK_POLICY_FILE:-})"
else
    echo "Policy:   disabled"
fi

# Start optional governance components if --governance flag is set
if [[ "${1:-}" == "--governance" || "${2:-}" == "--governance" ]]; then
    echo ""
    echo "Starting governance components..."
    if [[ -x "$SCRIPT_DIR/auditor" ]]; then
        AUDITOR_ARGS=(-socket="${HELPDESK_AUDIT_SOCKET:-/tmp/helpdesk-audit.sock}")
        if [[ "${HELPDESK_AUDITOR_LOG_ALL:-true}" == "true" ]]; then
            AUDITOR_ARGS+=(-log-all)
        fi
        start_bg auditor "$SCRIPT_DIR/auditor" "${AUDITOR_ARGS[@]}"
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
    HELPDESK_AUDIT_ENABLED="$AUDIT_ENABLED" \
    HELPDESK_AUDIT_URL="$AUDIT_URL" \
    HELPDESK_POLICY_ENABLED="$POLICY_ENABLED" \
    HELPDESK_INFRA_CONFIG="${HELPDESK_INFRA_CONFIG:-}" \
        "$SCRIPT_DIR/helpdesk"
fi
