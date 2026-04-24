#!/usr/bin/env bash
# install-systemd.sh — Install aiHelpDesk as systemd services.
#
# Run as root (or with sudo) on the target host after placing the helpdesk
# binaries in /opt/helpdesk.
#
# Usage:
#   sudo ./install-systemd.sh [--governance] [--research] [--enable-only]
#
#   --governance   Also install auditor + secbot (governance components).
#                  Required when HELPDESK_OPERATING_MODE=fix or readonly-governed.
#   --research     Also install research-agent (Gemini / web-search only).
#   --enable-only  Skip daemon-reload and service start; just enable the units
#                  so they start on next boot.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
UNIT_DIR="/etc/systemd/system"
INSTALL_DIR="/opt/helpdesk"
DATA_DIR="/var/lib/helpdesk"
HELPDESK_USER="helpdesk"
HELPDESK_GROUP="helpdesk"

GOVERNANCE=false
RESEARCH=false
ENABLE_ONLY=false

for arg in "$@"; do
    case "$arg" in
        --governance)   GOVERNANCE=true ;;
        --research)     RESEARCH=true ;;
        --enable-only)  ENABLE_ONLY=true ;;
        --help|-h)
            sed -n '3,12p' "$0" | sed 's/^# //'
            exit 0
            ;;
        *)
            echo "ERROR: unknown argument: $arg" >&2
            exit 1
            ;;
    esac
done

if [[ $EUID -ne 0 ]]; then
    echo "ERROR: this script must be run as root (use sudo)" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# 1. Create helpdesk user and directories
# ---------------------------------------------------------------------------
echo "==> Creating user and directories..."

if ! id -u "$HELPDESK_USER" &>/dev/null; then
    useradd --system --no-create-home --shell /usr/sbin/nologin \
            --home-dir "$INSTALL_DIR" "$HELPDESK_USER"
    echo "    Created system user: $HELPDESK_USER"
else
    echo "    User $HELPDESK_USER already exists — skipping."
fi

install -d -o "$HELPDESK_USER" -g "$HELPDESK_GROUP" -m 0755 "$INSTALL_DIR"
install -d -o "$HELPDESK_USER" -g "$HELPDESK_GROUP" -m 0750 "$DATA_DIR"

# ---------------------------------------------------------------------------
# 2. Create .env stub if not present
# ---------------------------------------------------------------------------
ENV_FILE="$INSTALL_DIR/.env"
if [[ ! -f "$ENV_FILE" ]]; then
    echo "==> Creating $ENV_FILE stub (edit before starting services)..."
    cat > "$ENV_FILE" <<'ENVEOF'
# aiHelpDesk environment configuration
# Copy from deploy/host/.env.example and fill in your values.
# Required:
HELPDESK_MODEL_VENDOR=anthropic
HELPDESK_MODEL_NAME=claude-sonnet-4-6
HELPDESK_API_KEY=

# Optional: path to your infrastructure.json
# HELPDESK_INFRA_CONFIG=/opt/helpdesk/infrastructure.json

# Optional: operating mode (readonly | fix | readonly-governed)
# HELPDESK_OPERATING_MODE=readonly
ENVEOF
    chown "$HELPDESK_USER:$HELPDESK_GROUP" "$ENV_FILE"
    chmod 0640 "$ENV_FILE"
    echo "    Created stub. Edit $ENV_FILE before starting services."
fi

# ---------------------------------------------------------------------------
# 3. Install unit files
# ---------------------------------------------------------------------------
echo "==> Installing systemd unit files to $UNIT_DIR..."

CORE_UNITS=(
    helpdesk-auditd.service
    helpdesk-database-agent.service
    helpdesk-k8s-agent.service
    helpdesk-sysadmin-agent.service
    helpdesk-incident-agent.service
    helpdesk-gateway.service
    helpdesk.target
)

OPTIONAL_UNITS=()
$GOVERNANCE && OPTIONAL_UNITS+=(helpdesk-auditor.service helpdesk-secbot.service)
$RESEARCH   && OPTIONAL_UNITS+=(helpdesk-research-agent.service)

ALL_UNITS=("${CORE_UNITS[@]}" "${OPTIONAL_UNITS[@]}")

for unit in "${ALL_UNITS[@]}"; do
    src="$SCRIPT_DIR/$unit"
    if [[ ! -f "$src" ]]; then
        echo "ERROR: unit file not found: $src" >&2
        exit 1
    fi
    install -m 0644 "$src" "$UNIT_DIR/$unit"
    echo "    $unit"
done

# ---------------------------------------------------------------------------
# 4. Reload systemd
# ---------------------------------------------------------------------------
echo "==> Reloading systemd daemon..."
systemctl daemon-reload

# ---------------------------------------------------------------------------
# 5. Enable units
# ---------------------------------------------------------------------------
echo "==> Enabling units..."
systemctl enable "${CORE_UNITS[@]}"
$GOVERNANCE && systemctl enable helpdesk-auditor.service helpdesk-secbot.service
$RESEARCH   && systemctl enable helpdesk-research-agent.service

# ---------------------------------------------------------------------------
# 6. Start (unless --enable-only)
# ---------------------------------------------------------------------------
if [[ "$ENABLE_ONLY" == "true" ]]; then
    echo ""
    echo "Units enabled. They will start on next boot."
    echo "To start now: sudo systemctl start helpdesk.target"
else
    echo "==> Starting helpdesk.target..."
    systemctl start helpdesk.target
    sleep 2
    echo ""
    echo "Service status:"
    systemctl status helpdesk-gateway.service --no-pager -l || true
fi

echo ""
echo "Done. Useful commands:"
echo "  sudo systemctl status helpdesk.target"
echo "  sudo journalctl -u helpdesk-gateway.service -f"
echo "  sudo systemctl stop helpdesk.target"
echo "  sudo systemctl restart helpdesk-database-agent.service"
