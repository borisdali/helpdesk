#!/bin/bash
# One-time setup for a REAL Linux host/VM (Ubuntu/Debian or RHEL/Red Hat) to
# exercise db-pgdata-corrupted-vm (testing/catalog/failures.yaml) — the
# host/VM counterpart to the Docker-based db-pgdata-corrupted fault,
# reached over real SSH instead of docker exec.
#
# This project does not own or provision the VM itself (unlike every other
# test fixture, which is docker-compose managed) — you supply a real VM,
# run this script against it once, then point faulttest/infra config at it.
# See the Context section of the plan this came from for why: Docker-faking
# genuine systemd (needed for check_host/restart_service/get_host_logs to
# behave like a real host) is a real reliability risk not worth taking when
# a real host/VM is the actual goal.
#
# Usage (run from wherever you have SSH access to the VM — not on the VM
# itself unless you're already logged in there):
#   ssh -p <port> <user>@<vm-host> 'sudo bash -s' < testing/docker/setup-pgbackrest-vm.sh
#
# Requires: SSH access with sudo (or already root) on the VM. Idempotent —
# safe to re-run.
set -euo pipefail

STANZA="main"

# ── 1. Detect distro family ─────────────────────────────────────────────
if [ -f /etc/debian_version ]; then
    FAMILY="debian"
elif [ -f /etc/redhat-release ]; then
    FAMILY="rhel"
else
    echo "Unsupported distro — this script supports Debian/Ubuntu and RHEL/Red Hat only." >&2
    echo "See /etc/os-release for what this VM actually is, and extend this script's" >&2
    echo "package-install section for it rather than guessing." >&2
    exit 1
fi
echo "Detected distro family: $FAMILY"

# ── 2. Install PostgreSQL + pgBackRest + sshd ───────────────────────────
# PG_VERSION/PG_UNIT/PGDATA below are what THIS script's own apt/dnf install
# produces on a stock, just-provisioned VM — confirmed against the real
# package names each distro family ships, not assumed. If your VM already
# has PostgreSQL installed a different way, skip this section and adjust
# the printed testing.infra.json entry at the end to match your own setup.
case "$FAMILY" in
debian)
    PG_VERSION="16"
    PG_UNIT="postgresql@${PG_VERSION}-main" # Debian/Ubuntu: per-cluster unit; the bare
                                             # "postgresql.service" is a meta-unit covering
                                             # every registered cluster, not just this one
    PGDATA="/var/lib/postgresql/${PG_VERSION}/main"
    PG_LOG_DIR="/var/log/postgresql"
    SSH_UNIT="ssh" # Debian/Ubuntu's openssh-server ships the real unit as
                   # ssh.service; sshd.service is only an alias symlink, and
                   # systemd refuses `enable` on an alias name directly
                   # ("Refusing to operate on alias name or linked unit
                   # file") — found live on Ubuntu 24.04 (noble).
    apt-get update
    apt-get install -y curl ca-certificates gnupg openssh-server sudo
    # PGDG repo: Debian/Ubuntu's own default repos often lag the version
    # this project's other fixtures use (postgres:16) — pin to the real
    # PGDG packaging instead of whatever the OS default happens to carry.
    install -d /usr/share/postgresql-common/pgdg
    curl -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc \
        https://www.postgresql.org/media/keys/ACCC4CF8.asc
    . /etc/os-release
    echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt ${VERSION_CODENAME}-pgdg main" \
        >/etc/apt/sources.list.d/pgdg.list
    apt-get update
    apt-get install -y "postgresql-${PG_VERSION}" pgbackrest
    ;;
rhel)
    PG_VERSION="16"
    PG_UNIT="postgresql-${PG_VERSION}"
    PGDATA="/var/lib/pgsql/${PG_VERSION}/data"
    PG_LOG_DIR="${PGDATA}/log"
    SSH_UNIT="sshd" # RHEL/Red Hat's openssh-server unit is genuinely named
                    # sshd.service, not an alias — no equivalent issue here.
    dnf install -y openssh-server sudo
    dnf install -y "https://download.postgresql.org/pub/repos/yum/reporpms/EL-$(rpm -E %rhel)-x86_64/pgdg-redhat-repo-latest.noarch.rpm" || true
    dnf -qy module disable postgresql || true
    dnf install -y "postgresql${PG_VERSION}-server" pgbackrest
    "/usr/pgsql-${PG_VERSION}/bin/postgresql-${PG_VERSION}-setup" initdb
    ;;
esac

systemctl enable --now "$SSH_UNIT"
systemctl enable --now "$PG_UNIT"

# ── 3. Authorize the project's existing test SSH key ────────────────────
# Reuses the same key pair Dockerfile.pgbackrest already authorizes for its
# own SSH-dispatch testing, rather than generating a new one — swap in your
# own key here if you'd rather not share it with the Docker fixtures.
id postgres >/dev/null 2>&1 || useradd -r -m -s /bin/bash postgres
install -d -m 700 -o postgres -g postgres ~postgres/.ssh
if [ -f "$(dirname "$0")/ssh/pgbackrest_test_client_key.pub" ]; then
    cat "$(dirname "$0")/ssh/pgbackrest_test_client_key.pub" >>~postgres/.ssh/authorized_keys
else
    echo "NOTE: pgbackrest_test_client_key.pub not found next to this script —" >&2
    echo "copy it to the VM and append it to ~postgres/.ssh/authorized_keys yourself," >&2
    echo "or authorize your own key instead." >&2
fi
chown postgres:postgres ~postgres/.ssh/authorized_keys 2>/dev/null || true
chmod 600 ~postgres/.ssh/authorized_keys 2>/dev/null || true

# ── 4. Passwordless sudo for exactly the commands dispatch needs ────────
# Both agents/sysadmin's own SSH dispatch (restart_service/check_host) and
# db-pgdata-corrupted-vm's own fault-injection script need to run
# `systemctl restart/show <unit>` as this user. Scoped narrowly — not full
# sudo — matching how a real ops team would actually grant this.
#
# The `show` grant uses `--property=*` rather than a bare unit name: sudo
# matches a command's FULL literal argument list, and checkHostImpl
# (agents/sysadmin/tools.go) actually calls
# `systemctl show --property=ActiveState,SubState,Result,MainPID,ExecMainStartTimestamp <unit>`,
# not a bare `systemctl show <unit>` — found live (2026-10-08) when a grant
# written without the flag silently never matched, failing with "a password
# is required" instead of running. The exact property list could still
# change later; `--property=*` absorbs that instead of re-breaking.
cat >/etc/sudoers.d/pgbackrest-test-postgres <<EOF
postgres ALL=(root) NOPASSWD: /usr/bin/systemctl restart ${PG_UNIT}, /usr/bin/systemctl show --property=* ${PG_UNIT}
EOF
chmod 440 /etc/sudoers.d/pgbackrest-test-postgres

# ── 5. pgBackRest stanza + initial backup ───────────────────────────────
mkdir -p /var/lib/pgbackrest
chown postgres:postgres /var/lib/pgbackrest
cat >/etc/pgbackrest.conf <<EOF
[global]
repo1-path=/var/lib/pgbackrest
repo1-retention-full=2

[${STANZA}]
pg1-path=${PGDATA}
EOF
sudo -u postgres psql -v ON_ERROR_STOP=1 -c \
    "ALTER SYSTEM SET archive_mode = on;" 2>/dev/null || true
sudo -u postgres psql -v ON_ERROR_STOP=1 -c \
    "ALTER SYSTEM SET archive_command = 'pgbackrest --stanza=${STANZA} archive-push %p';"
sudo -u postgres psql -v ON_ERROR_STOP=1 -c "SELECT pg_reload_conf();"
systemctl restart "$PG_UNIT"
sudo -u postgres pgbackrest --stanza="$STANZA" --log-level-console=info stanza-create
sudo -u postgres pgbackrest --stanza="$STANZA" --type=full --log-level-console=info backup

# ── 6. Print the testing.infra.json entry to add ────────────────────────
echo
echo "=== Add this to your testing.infra.json (adjust address/ssh_port/ssh_user/ssh_key_path) ==="
cat <<EOF
  "db_servers": {
    "pgbackrest-vm": {
      "name": "pgBackRest Real VM Target",
      "connection_string": "host=<VM-ADDRESS> port=5432 dbname=postgres user=postgres",
      "vm_name": "pgbackrest-real-vm",
      "systemd_unit": "${PG_UNIT}",
      "pg_log_dir": "${PG_LOG_DIR}",
      "tags": ["test"]
    }
  },
  "vms": {
    "pgbackrest-real-vm": {
      "name": "pgBackRest Real VM",
      "address": "<VM-ADDRESS>",
      "ssh_user": "postgres",
      "ssh_port": 22,
      "ssh_key_path": "testing/docker/ssh/pgbackrest_test_client_key"
    }
  }
EOF
echo "==========================================================================="
echo "Setup complete. Stanza: $STANZA. Unit: $PG_UNIT. PGDATA: $PGDATA. Logs: $PG_LOG_DIR"
