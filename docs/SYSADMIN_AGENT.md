# aiHelpDesk SysAdmin Agent

The **SysAdmin agent** is the host-level layer of aiHelpDesk. Where the database agent speaks `psql` and the Kubernetes agent speaks `kubectl` or `client-go`, the SysAdmin agent operates at the OS and container-runtime level of the machines running your database services.

Its primary role is **diagnostic**: it can check whether a container or systemd service is running, retrieve recent logs, inspect disk and memory pressure, and — when a playbook with the right permission tier authorises it — restart the container or service. All restart operations are policy-checked and fully audited.

The SysAdmin agent is the execution backend for `execution_mode: agent_approve` and `execution_mode: agent_auto` playbooks (see [Playbooks](PLAYBOOKS.md)). The three Database Down playbooks currently run in `execution_mode: agent` (read-only diagnostic), which routes to the database agent. Future versions will promote those playbooks to `agent_approve` mode, where the SysAdmin agent performs the investigation **and** executes the approved remediation.

---

## Table of Contents

1. [Port and startup](#1-port-and-startup)
2. [Infrastructure config — the `host` block](#2-infrastructure-config--the-host-block)
3. [Server ID resolution](#3-server-id-resolution)
4. [Tools](#4-tools)
5. [Container runtime dispatch](#5-container-runtime-dispatch)
6. [Remediation permission model](#6-remediation-permission-model)
7. [Agent card and fleet taxonomy](#7-agent-card-and-fleet-taxonomy)
8. [System prompt and output format](#8-system-prompt-and-output-format)
9. [Fault injection test](#9-fault-injection-test)
10. [Integration with playbooks](#10-integration-with-playbooks)

---

## 1. Port and startup

Default port: **1103**

```bash
# Requires HELPDESK_INFRA_CONFIG — the agent needs the host block to resolve server IDs
HELPDESK_INFRA_CONFIG=infrastructure.json go run ./agents/sysadmin/
```

To override the listen address:

```bash
HELPDESK_AGENT_ADDR="0.0.0.0:1103" HELPDESK_INFRA_CONFIG=infrastructure.json go run ./agents/sysadmin/
```

Add the SysAdmin agent to Gateway discovery:

```bash
HELPDESK_AGENT_URLS="http://localhost:1100,http://localhost:1102,http://localhost:1103,http://localhost:1104" \
  go run ./cmd/gateway/
```

The agent is also reachable via the Gateway's agent alias `"sysadmin"` or `"host"` in `POST /api/v1/query`:

```bash
curl -X POST http://localhost:8080/api/v1/query \
  -H "Content-Type: application/json" \
  -d '{"agent": "host", "message": "Check the status of the prod-db server."}'
```

---

## 2. Infrastructure config — VM and DBServer fields

The SysAdmin agent requires each database server to reference a VM entry in `infrastructure.json`. The **container runtime** (`docker`, `podman`, or empty for systemd) belongs on the **VM**, because it is a machine-level property shared by all databases on that host. The **process handle** (`container_name` or `systemd_unit`) belongs on the **db_server**, because it is specific to that database instance.

```json
{
  "db_servers": {
    "prod-db": {
      "name": "Production Database",
      "connection_string": "host=db1.example.com port=5432 dbname=prod user=admin",
      "k8s_cluster":   "global-prod",
      "k8s_namespace": "database",
      "tags": ["production", "critical"]
    },
    "staging-db": {
      "name": "Staging Database",
      "connection_string": "...",
      "vm_name":        "staging-vm",
      "container_name": "postgres-staging"
    },
    "bare-metal-db": {
      "name": "Bare Metal Database",
      "connection_string": "...",
      "vm_name":      "bare-metal-vm",
      "systemd_unit": "postgresql-16"
    }
  },

  "k8s_clusters": {
    "global-prod": {
      "name":    "Global Corp Production Cluster",
      "context": "global-prod-cluster"
    }
  },

  "vms": {
    "staging-vm": {
      "name":    "Staging DB Host",
      "address": "staging.example.com",
      "runtime": "podman"
    },
    "bare-metal-vm": {
      "name":    "Bare Metal DB Host",
      "address": "bare.example.com",
      "runtime": ""
    }
  }
}
```

`k8s_cluster` and `vm_name` are mutually exclusive on a db_server — a database runs either on Kubernetes or on a VM, never both. The SysAdmin agent only operates on VM-hosted databases; `prod-db` above is K8s-hosted and is not reachable by SysAdmin tools (the K8s agent handles it instead).

### DB server fields for SysAdmin operations

| Field | Type | Description |
|---|---|---|
| `vm_name` | string | **Required for SysAdmin.** Key in the `vms` map. Mutually exclusive with `k8s_cluster` — set one or the other, never both. |
| `container_name` | string | The container name or ID to target. Required when the VM's `runtime` is `docker` or `podman`. |
| `systemd_unit` | string | The systemd service unit name (e.g. `"postgresql-16"`). Required when the VM's `runtime` is `""`. |

### VM fields

| Field | Type | Description |
|---|---|---|
| `name` | string | Human-readable VM name. |
| `address` | string | Hostname or IP address of the machine. |
| `runtime` | string | Container runtime: `"docker"`, `"podman"`, or `""` (systemd/direct). Applies to all databases on this VM. |

Exactly one of `container_name` or `systemd_unit` must be set on the db_server, matching the VM's `runtime`. If the VM has `runtime: "docker"` or `runtime: "podman"`, `container_name` is required. If the VM has `runtime: ""`, `systemd_unit` is required.

---

## 3. Server ID resolution

Unlike the database agent (which takes a `connection_string`) or the K8s agent (which takes a `context`), the SysAdmin agent takes a **server ID** — the key in the `db_servers` map of your infrastructure config.

All seven tools accept a `target` argument that is the server ID. The agent resolves it by traversing `db_servers → vm_name → vms` at call time:

```
check_host(target="prod-db")
    │
    └─ resolveHost("prod-db")
           │
           └─ infraConfig.DBServers["prod-db"]
                  │  .VMName → infraConfig.VMs["prod-vm"]
                  │              .Runtime = "docker"
                  │  .ContainerName = "postgres"
                  │
                  ├─ Runtime="docker"   → docker inspect postgres
                  └─ Runtime=""         → systemctl status postgresql-16
```

If the server ID is not found, has no `vm_name`, or the referenced VM is not defined, the tool returns an error immediately without executing any system command.

The agent is instructed to use the server's friendly name or the server ID from the user's prompt — never to invent or guess connection strings or container names, which are resolved internally.

---

## 4. Tools

### 4.1 R/O tools (action class: `read`)

These five tools never modify system state. They do not require policy pre-checks and are always permitted in any execution mode.

#### `check_host`

Inspect the running state of the database process on the host.

```
target   string   required — server ID from infrastructure config
```

Returns:

```json
{
  "server_id": "prod-db",
  "runtime":   "docker",
  "status":    "running",
  "details":   "Status: Up 3 days, healthy"
}
```

`status` values: `running`, `stopped`, `restarting`, `error`, `unknown`. For systemd hosts, `running` maps to `active (running)`, `stopped` to `inactive` or `failed`.

`details` contains the raw `docker inspect` status string or `systemctl status` active state for further diagnosis.

#### `get_host_logs`

Retrieve recent log output from the database container or service.

```
target   string   required — server ID
lines    int      optional — number of log lines to return (default: 100)
since    string   optional — relative time filter, e.g. "30m", "2h", "1d"
```

For container runtimes, calls `docker logs --tail N [--since T]`. For systemd, calls `journalctl -u <unit> -n N [--since T]`. Returns the raw log output — the agent reads it for error patterns without further parsing.

This is the primary tool for diagnosing why a database failed to start. PostgreSQL startup errors (`FATAL`, `PANIC`, `invalid value for parameter`) appear here.

#### `check_disk`

Report disk usage on the host where the database process runs.

```
target   string   required — server ID
```

For container runtimes, executes `df -h` inside the container. For systemd hosts, executes `df -h` on the host directly. Returns raw `df` output. The agent looks for filesystems above 80% usage, particularly the data directory mount.

#### `check_memory`

Report memory usage on the host.

```
target   string   required — server ID
```

For container runtimes, executes `free -h` inside the container (or reads `/proc/meminfo`). For systemd hosts, executes `free -h`. Returns raw output. The agent looks for low available memory that could cause OOM kills or swap pressure.

#### `read_pg_log_file`

Read the PostgreSQL log file directly from inside the container, pod, or host process — without a live database connection.

```
target    string   required — server ID
lines     int      optional — number of tail lines to return (default: 200)
filter    string   optional — case-insensitive substring filter applied to each line
log_dir   string   optional — override default log directory (default: /var/lib/postgresql/data/log)
```

**How it works:**

1. Lists files in `log_dir` sorted by modification time (`ls -t`) and selects the most-recently-modified file.
2. Reads the tail of that file via `tail -n <lines>`.
3. If `filter` is set, returns only lines containing the filter string (case-insensitive).

The exec path depends on the server's runtime:

| Runtime | Command |
|---|---|
| `docker` | `docker exec <container> sh -c "ls -t <log_dir> | head -1"` then `docker exec <container> tail -n N <file>` |
| `podman` | Same as docker with `podman` binary |
| `kubectl` (k8s) | Two-step: `kubectl get pod -l <selector> -n <ns>` to resolve pod name, then `kubectl exec <pod> -n <ns> -- tail ...` |
| systemd (host) | Reads the log file directly on the local filesystem |

**Key distinction from DB agent's `read_pg_log`:** The DB agent tool uses `pg_read_file()` SQL and requires a live Postgres connection. This tool uses process exec (`docker exec` / `kubectl exec`) and works when Postgres is completely down — including after a crash, failed startup, or OOM kill.

Returns:

```json
{
  "server_id":     "prod-db",
  "runtime":       "docker",
  "lines_returned": 47,
  "logs":          "2026-04-05 03:12:44 UTC [1] PANIC: could not locate a valid checkpoint record\n..."
}
```

The agent uses this tool when `get_host_logs` shows a non-zero exit code or crash signal but lacks PostgreSQL-level detail (e.g., the container stdout only shows the kernel kill message, not the Postgres `FATAL`/`PANIC` that preceded it).

---

### 4.2 Mutation tools (action class: `destructive`)

Both restart tools require a policy pre-check before execution. The pre-check enforces the operating mode (`readonly` → denied, `fix` → allowed subject to rules), evaluates tag-based policy rules against the server's tags and sensitivity classes, and writes an approval request if human confirmation is required.

Every successful restart call is recorded in the audit log via `RecordToolCall` with timing, server ID, and runtime details.

#### `restart_container`

Restart the database container using the configured container runtime.

```
target   string   required — server ID
```

Executes `docker restart <container_name>` (or `podman restart`). Returns:

```json
{
  "server_id": "prod-db",
  "runtime":   "docker",
  "target":    "postgres",
  "success":   true,
  "output":    "postgres"
}
```

The agent **never** calls this tool without first calling `check_host` and `get_host_logs` to confirm the container is actually stopped and that the stop was not caused by a condition that a restart would not fix (e.g. disk full, corrupt data directory).

#### `restart_service`

Restart the database systemd service.

```
target   string   required — server ID
```

Executes `systemctl restart <systemd_unit>`. Returns the same shape as `restart_container` with `runtime: "systemd"`.

Same pre-investigation requirement as `restart_container`.

---

## 5. Container runtime dispatch

`containerRuntimeBin()` selects the execution backend at call time:

| `container_runtime` | Binary used | Commands |
|---|---|---|
| `"docker"` | `docker` | `docker inspect`, `docker logs`, `docker exec`, `docker restart` |
| `"podman"` | `podman` | Same commands — Podman is Docker-compatible |
| `""` (empty) | host directly | `systemctl`, `journalctl`, `df`, `free` |

The binary must be present in the `PATH` of the user running the SysAdmin agent process. For Docker and Podman, the user must have permission to call the Docker socket or Podman socket without `sudo`.

If the binary is not found, `check_host` returns `status: error` with a details message explaining the missing binary. This is treated as a configuration error, not a database failure — the agent is instructed to report it as such rather than diagnosing a database problem.

---

## 6. Remediation permission model

The SysAdmin agent introduces a three-tier remediation model for playbooks:

| Tier | `execution_mode` | What happens |
|---|---|---|
| Diagnostic only | `agent` | Routed to the **database agent** for R/O investigation. SysAdmin agent is not involved. All DB-down playbooks currently use this mode. |
| Human-approved restart | `agent_approve` | Routed to the **SysAdmin agent**. Agent investigates (R/O tools), forms a recommendation, and emits a restart proposal. The Gateway creates an approval request before executing the mutation. The operator approves or rejects via the approvals API. |
| Auto-restart (whitelisted) | `agent_auto` | Routed to the **SysAdmin agent**. The agent may call `restart_container` or `restart_service` without per-call human approval, but **only for tools listed in the playbook's `permitted_tools` field**. Policy rules still apply. |

### `auto_remediation_eligible` flag

The `restart_container` and `restart_service` tools carry the `auto_remediation_eligible: true` flag in their `ToolEntry` records. This flag is set in the agent card via the `auto_remediation:true` skill tag and is surfaced in `GET /api/v1/tools`.

The flag marks a tool as safe for autonomous execution when:
1. The playbook's `execution_mode` is `agent_auto`, AND
2. The tool is listed in the playbook's `permitted_tools`, AND
3. The policy engine permits it

Tools without `auto_remediation_eligible` are never called without an approval gate, regardless of `execution_mode`.

The five R/O tools (`check_host`, `get_host_logs`, `check_disk`, `check_memory`, `read_pg_log_file`) do not have this flag — they are always permitted without approval.

### Policy interaction

Both restart tools call `policyEnforcer.CheckTool` with `ActionDestructive` before executing. The enforcement path is identical to `terminate_connection` and `restart_deployment`: operating mode is checked first, then tag-based rules, then blast-radius bounds if configured.

Policy denials surface as `---\nERROR — Policy denied: <reason>` in the tool response, which the fleet-runner and Gateway both treat as a hard failure.

---

## 7. Agent card and Fleet taxonomy

The SysAdmin agent registers its skills in the agent card with the following taxonomy tags:

| Skill | Tags | `fleet_eligible` | `auto_remediation_eligible` |
|---|---|---|---|
| `check_host` | `"host"`, `"cap:connectivity"` | no | no |
| `get_host_logs` | `"host"`, `"cap:logs"` | no | no |
| `check_disk` | `"host"`, `"cap:disk"` | no | no |
| `check_memory` | `"host"`, `"cap:memory"` | no | no |
| `read_pg_log_file` | `"host"`, `"postgres"`, `"logs"`, `"diagnostics"` | no | no |
| `restart_container` | `"host"`, `"auto_remediation:true"` | no | **yes** |
| `restart_service` | `"host"`, `"auto_remediation:true"` | no | **yes** |

The SysAdmin tools are not fleet-eligible — they are not included in fleet job plans because they operate on a single server's process, not on a fleet of database targets. They are invoked exclusively via agentic playbook sessions.

The tool registry exposes all seven tools under `GET /api/v1/tools`. To list only auto-remediation-eligible tools:

```bash
curl -s http://localhost:8080/api/v1/tools \
  | jq '[.[] | select(.auto_remediation_eligible == true) | .name]'
# ["restart_container", "restart_service"]
```

---

## 8. System prompt and output format

The SysAdmin agent's system prompt (`prompts/sysadmin.txt`) instructs it to:

1. Always call `check_host` first to confirm the container/service state before any other action
2. If `exitcode=0`: report a clean stop — do not speculate about disk or OOM without evidence
3. If `exitcode>0` or `oomkilled=true`: call `get_host_logs` to read the failure reason from logs
4. If `get_host_logs` lacks PostgreSQL-level detail (e.g. shows only a kernel signal, not the Postgres error): call `read_pg_log_file` to read the PostgreSQL log file directly from inside the container
5. Call `check_disk` only when logs contain explicit evidence of disk exhaustion ("No space left on device", "disk full")
6. Call `check_memory` only when `oomkilled=true`
7. **Never** recommend restarting when:
   - Disk is full (a restart will fail immediately)
   - Logs show data directory corruption or `PANIC` on data files
   - Logs show `invalid_page` or storage-level errors
   In these cases, escalate to a human DBA
5. Present any restart recommendation as an explicit proposal, not an action taken, unless operating in `agent_auto` mode with the tool in `permitted_tools`

### Output format

The agent appends a structured signal at the end of its response:

```
FINDINGS: <one-sentence diagnosis and recommendation>
ESCALATE_TO: <series_id>    # optional — only when the issue is beyond a restart
```

These lines are parsed by the Gateway identically to how they are parsed in agent-mode database playbooks — `FINDINGS:` sets `outcome=resolved` and `findings_summary`; `ESCALATE_TO:` sets `outcome=escalated`. See [Structured escalation signal](PLAYBOOKS.md#structured-escalation-signal) for the full parsing specification.

The agent is also instructed to emit `ROOT_CAUSE:` and `ACTION_TAKEN:` lines for completeness, which appear in the visible `text` returned to the operator but are not parsed by the Gateway.

---

## 9. Fault injection test

The fault catalog (`testing/catalog/failures.yaml`) includes two `host` category tests:

### `host-container-stopped`

**What it tests:** The SysAdmin agent's ability to identify a stopped container and report the failure without recommending an inappropriate restart.

**Inject:** `docker compose stop postgres` — stops the `helpdesk-test-pg` container, leaving it in exited state.

**Teardown:** `docker compose start postgres`

**Prompt:** `"The database server 'faulttest-db' is not responding. Please check the host-level status of the server and investigate why the database process is not running."`

**Expected tools:** `check_host`, `get_host_logs`

**Expected keywords:** `stopped`, `exited`, `container`, `not running`, or `offline`

**Expected diagnosis category:** `container_stopped`

---

### `host-pg-crash`

**What it tests:** The SysAdmin agent's ability to diagnose a PostgreSQL process crash where the container is still alive but Postgres is not running. Specifically verifies that the agent calls `read_pg_log_file` to inspect the PostgreSQL log file (not just container stdout) when the container logs show a signal/crash rather than a clean stop.

**Inject:** `docker_exec` — runs `kill -ABRT $(head -1 /var/run/postgresql/postmaster.pid)` inside the container, sending SIGABRT to the Postgres postmaster. The container stays running; Postgres exits with a non-zero code.

**Teardown:** `docker compose start postgres` — restarts the container after the test.

**Prompt:** `"The database server 'faulttest-db' is not responding. The container appears to still be running but the database process may have crashed. Please investigate and report what happened."`

**Expected tools:** `check_host`, `get_host_logs`, `read_pg_log_file`

**Expected keywords:** `crashed`, `crash`, `fatal`, `panic`, `aborted`, `signal`, `killed`, or `terminated`

**Expected diagnosis category:** `process_crash`

**Timeout:** 90s (longer than `host-container-stopped` to allow the agent time to call all three tools)

---

To run the host fault tests:

```bash
go run ./testing/cmd/faulttest run \
  --sysadmin-agent http://localhost:1103 \
  --categories host
```

The `testing/testing.infra.json` file is pre-configured for `faulttest-db` with a `vm_name` pointing to the `faulttest-vm` entry:

```json
"faulttest-db": {
  "vm_name":        "faulttest-vm",
  "container_name": "helpdesk-test-pg",
  ...
},

"vms": {
  "faulttest-vm": {
    "name":    "Fault Test VM",
    "address": "localhost",
    "runtime": "docker"
  }
}
```

This is the only infrastructure change required to run the host fault test in the standard test stack.

---

## 10. Integration with playbooks

The SysAdmin agent is currently invoked by playbooks with `execution_mode: agent` (read-only investigation routed to the database agent). Direct SysAdmin agent invocation is the next step and will use `execution_mode: agent_approve`.

The three Database Down system playbooks are planned to migrate as follows:

| Series | Current mode | Planned mode | SysAdmin involvement |
|---|---|---|---|
| `pbs_db_restart_triage` | `agent` (db agent) | `agent_approve` | Agent investigates with R/O tools, proposes `restart_container`/`restart_service`, operator approves |
| `pbs_db_config_recovery` | `agent` (db agent) | `agent_approve` | Same — config fix is a human operation; agent provides the exact change |
| `pbs_db_pitr_recovery` | `agent` (db agent) | `agent` (db agent, unchanged) | PITR recovery always requires human DBA — restart is never appropriate |

The `agent_auto` mode (for non-production or pre-approved restart targets) will use `permitted_tools: ["restart_container"]` to scope autonomous execution to the restart tool only, keeping `check_disk` and `check_memory` as required pre-checks in the agent's system prompt rather than policy.

See [Playbooks](PLAYBOOKS.md) for the full `execution_mode` specification and the approval API.
