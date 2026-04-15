# aiHelpDesk Fault Injection Testing: sample runs

See the detailed documentation on aiHelpDesk Fault Injection Testing [here](FAULTTEST.md) and the overall aiHelpDesk Testing approach/pyramid [here](../testing/README.md#summary-aihelpdesk-five-layer-test-pyramid).

What's presented below are the three sample runs of external/embedded Fault Injection Testing of aiHelpDesk deployed directly on a host/VM, in Docker/Podman containers and on K8s:

## Host/VM sample run

See the platform deployment specifics of running aiHelpDesk Fault Injection Tests directly on a host/VM [here](../deploy/host/README.md#8-fault-injection-testing-faulttest) and start the host/VM aiHelpDesk stack first (download the appropriate binaries; in the example below the stack runs on Mac ARM64 Apple Silicon):

```
[boris@ /tmp/helpdesk/helpdesk-v0.10.0-darwin-arm64]$ ./startall.sh --services-only --governance
Starting helpdesk services...
  auditd (pid 7649) -> /tmp/helpdesk-auditd.log
  database-agent (pid 7651) -> /tmp/helpdesk-database-agent.log
  k8s-agent (pid 7652) -> /tmp/helpdesk-k8s-agent.log
  sysadmin-agent (pid 7653) -> /tmp/helpdesk-sysadmin-agent.log
  incident-agent (pid 7654) -> /tmp/helpdesk-incident-agent.log
  gateway (pid 7658) -> /tmp/helpdesk-gateway.log
Gateway listening on http://localhost:8080
Auditing: enabled  (http://localhost:1199)
Policy:   enabled  (/tmp/helpdesk/helpdesk-v0.10.0-darwin-arm64/./policies.yaml)
Mode:     fix

Starting governance components...
  auditor (pid 7660) -> /tmp/helpdesk-auditor.log
  secbot (pid 7661) -> /tmp/helpdesk-secbot.log

Running headless (--services-only). Press Ctrl-C to stop all services.
```

Run the test:

```
[boris@ /tmp/helpdesk/helpdesk-v0.10.0-darwin-arm64]$ ./faulttest run   --conn "alloydb-on-vm"   --db-agent http://localhost:8080   --api-key gateway-api-key --infra-config infrastructure.json

--- Testing: Max connections exhausted (db-max-connections) ---
time=2026-04-15T10:33:07.791-04:00 level=INFO msg="injecting failure" id=db-max-connections type=sql mode=external
time=2026-04-15T10:33:07.881-04:00 level=INFO msg="sending prompt to agent" failure=db-max-connections category=database agent=http://localhost:8080 prompt_len=173
time=2026-04-15T10:33:07.885-04:00 level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T10:33:22.945-04:00 level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-max-connections reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T10:33:22.945-04:00 level=INFO msg="tearing down failure" id=db-max-connections type=sql
Result: [PASS] score=85%

--- Testing: Long-running query blocking (db-long-running-query) ---
time=2026-04-15T10:33:22.989-04:00 level=INFO msg="injecting failure" id=db-long-running-query type=sql mode=external
time=2026-04-15T10:38:23.066-04:00 level=INFO msg="sending prompt to agent" failure=db-long-running-query category=database agent=http://localhost:8080 prompt_len=172
time=2026-04-15T10:38:23.066-04:00 level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T10:38:55.387-04:00 level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-long-running-query reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T10:38:55.387-04:00 level=INFO msg="tearing down failure" id=db-long-running-query type=sql
Result: [PASS] score=100%

--- Testing: Lock contention / deadlock (db-lock-contention) ---
time=2026-04-15T10:38:55.456-04:00 level=INFO msg="injecting failure" id=db-lock-contention type=shell_exec mode=external
time=2026-04-15T10:38:56.526-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: ACCESS EXCLUSIVE lock held by pid=8835"
time=2026-04-15T10:38:56.526-04:00 level=INFO msg="sending prompt to agent" failure=db-lock-contention category=database agent=http://localhost:8080 prompt_len=171
time=2026-04-15T10:38:56.526-04:00 level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T10:39:19.313-04:00 level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-lock-contention reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T10:39:19.313-04:00 level=INFO msg="tearing down failure" id=db-lock-contention type=shell_exec
time=2026-04-15T10:48:55.565-04:00 level=INFO msg="shell_exec completed" output="DROP TABLE"
Result: [PASS] score=90%

--- Testing: Table bloat / dead tuples (db-table-bloat) ---
time=2026-04-15T10:48:55.566-04:00 level=INFO msg="injecting failure" id=db-table-bloat type=sql mode=external
time=2026-04-15T10:48:56.412-04:00 level=INFO msg="sending prompt to agent" failure=db-table-bloat category=database agent=http://localhost:8080 prompt_len=206
time=2026-04-15T10:48:56.412-04:00 level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T10:49:10.113-04:00 level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-table-bloat reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T10:49:10.113-04:00 level=INFO msg="tearing down failure" id=db-table-bloat type=sql
Result: [PASS] score=100%

--- Testing: High cache miss ratio (db-high-cache-miss) ---
time=2026-04-15T10:49:10.161-04:00 level=INFO msg="injecting failure" id=db-high-cache-miss type=sql mode=external
time=2026-04-15T10:49:10.706-04:00 level=INFO msg="sending prompt to agent" failure=db-high-cache-miss category=database agent=http://localhost:8080 prompt_len=172
time=2026-04-15T10:49:10.706-04:00 level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T10:49:35.360-04:00 level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-high-cache-miss reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T10:49:35.360-04:00 level=INFO msg="tearing down failure" id=db-high-cache-miss type=sql
Result: [PASS] score=90%

--- Testing: Replication lag (db-replication-lag) ---
time=2026-04-15T10:49:35.462-04:00 level=INFO msg="injecting failure" id=db-replication-lag type=sql mode=internal
time=2026-04-15T10:49:35.471-04:00 level=ERROR msg="injection failed" id=db-replication-lag err="psql: exit status 2\npsql: error: connection to server on socket \"/tmp/.s.PGSQL.5432\" failed: No such file or directory\n\tIs the server running locally and accepting connections on that socket?\n"

--- Testing: Session stuck with uncommitted writes (db-idle-in-transaction) ---
time=2026-04-15T10:49:35.471-04:00 level=INFO msg="injecting failure" id=db-idle-in-transaction type=shell_exec mode=external
time=2026-04-15T10:49:36.529-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: idle-in-transaction session (pid=9199)"
time=2026-04-15T10:49:36.529-04:00 level=INFO msg="sending prompt to agent" failure=db-idle-in-transaction category=database agent=http://localhost:8080 prompt_len=331
time=2026-04-15T10:49:36.529-04:00 level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T10:49:52.574-04:00 level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-idle-in-transaction reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T10:49:52.574-04:00 level=INFO msg="tearing down failure" id=db-idle-in-transaction type=shell_exec
time=2026-04-15T10:49:53.700-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
Result: [PASS] score=86%

--- Testing: Direct terminate — inspect-first check (db-terminate-direct-command) ---
time=2026-04-15T10:49:53.700-04:00 level=INFO msg="injecting failure" id=db-terminate-direct-command type=shell_exec mode=external
time=2026-04-15T10:49:54.770-04:00 level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: idle-in-transaction session (pid=9228)"
time=2026-04-15T10:49:54.770-04:00 level=INFO msg="sending prompt to agent" failure=db-terminate-direct-command category=database agent=http://localhost:8080 prompt_len=223
time=2026-04-15T10:49:54.770-04:00 level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T10:50:08.953-04:00 level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-terminate-direct-command reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T10:50:08.953-04:00 level=INFO msg="tearing down failure" id=db-terminate-direct-command type=shell_exec
time=2026-04-15T10:50:10.036-04:00 level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
Result: [PASS] score=71%

--- Testing: Tables needing vacuum (dead tuple bloat) (db-vacuum-needed) ---
time=2026-04-15T10:50:10.037-04:00 level=INFO msg="injecting failure" id=db-vacuum-needed type=sql mode=internal
time=2026-04-15T10:50:10.085-04:00 level=INFO msg="sending prompt to agent" failure=db-vacuum-needed category=database agent=http://localhost:8080 prompt_len=217
time=2026-04-15T10:50:10.085-04:00 level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T10:50:22.096-04:00 level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-vacuum-needed reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T10:50:22.096-04:00 level=INFO msg="tearing down failure" id=db-vacuum-needed type=sql
Result: [PASS] score=80%

--- Testing: Disk usage — large table growth (db-disk-pressure) ---
time=2026-04-15T10:50:22.199-04:00 level=INFO msg="injecting failure" id=db-disk-pressure type=sql mode=internal
time=2026-04-15T10:50:22.376-04:00 level=INFO msg="sending prompt to agent" failure=db-disk-pressure category=database agent=http://localhost:8080 prompt_len=205
time=2026-04-15T10:50:22.376-04:00 level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T10:50:31.784-04:00 level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-disk-pressure reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T10:50:31.784-04:00 level=INFO msg="tearing down failure" id=db-disk-pressure type=sql
Result: [PASS] score=80%

=== Fault Test Report: 8bd5d394 ===

[PASS] Max connections exhausted (db-max-connections) - score: 85% [tool evidence: text match]
[PASS] Long-running query blocking (db-long-running-query) - score: 100% [tool evidence: text match]
[PASS] Lock contention / deadlock (db-lock-contention) - score: 90% [tool evidence: text match]
[PASS] Table bloat / dead tuples (db-table-bloat) - score: 100% [tool evidence: text match]
[PASS] High cache miss ratio (db-high-cache-miss) - score: 90% [tool evidence: text match]
[FAIL] Replication lag (db-replication-lag) - score: 0%
       Keywords: x | Diagnosis: x | Tools: x | Error: injection failed: psql: exit status 2
psql: error: connection to server on socket "/tmp/.s.PGSQL.5432" failed: No such file or directory
	Is the server running locally and accepting connections on that socket?

[PASS] Session stuck with uncommitted writes (db-idle-in-transaction) - score: 86% [tool evidence: text match]
[PASS] Direct terminate — inspect-first check (db-terminate-direct-command) - score: 71% [tool evidence: text match]
[PASS] Tables needing vacuum (dead tuple bloat) (db-vacuum-needed) - score: 80% [tool evidence: text match]
[PASS] Disk usage — large table growth (db-disk-pressure) - score: 80% [tool evidence: text match]

--- Summary ---
Total: 10 | Passed: 9 | Failed: 1 | Rate: 90%
  database: 9/10 (90%)

Note: 9 fault(s) used text-based tool evidence scoring (agent did not emit
structured tool call data). These scores may be less reliable. For precise
tool evidence, point --db-agent directly at the agent A2A URL rather than
the gateway, or use an ADK-based agent.

Report written to faulttest-8bd5d394.json
```

Check out the detailed results report:

```
[boris@ /tmp/helpdesk/helpdesk-v0.10.0-darwin-arm64]$ jq '.results[] | select(.failure_id == "db-disk-pressure")' faulttest-8bd5d394.json
{
  "failure_id": "db-disk-pressure",
  "failure_name": "Disk usage — large table growth",
  "category": "database",
  "score": 0.8,
  "passed": true,
  "keyword_pass": true,
  "diagnosis_pass": true,
  "tool_evidence": false,
  "tool_evidence_mode": "text_fallback",
  "response_text": "I'll help you identify which databases and tables are consuming the most storage on the AlloyDB Omni server.\n## Disk Space Analysis — AlloyDB Omni Server\n\n### Database Summary\nThe server has **3 databases** totaling   approximately **61 MB**:\n\n| Database | Size | Owner |\n|----------|------|-------|\n| `postgres` | 33 MB | postgres |\n| `alloydbadmin` | 15 MB | alloydbadmin |\n| `alloydbmetadata` | 13 MB | alloydbadmin |\n\n### Storage Consumption by
Table\n\nThe **`postgres` database** is the largest, and the storage analysis reveals:\n\n| Schema | Table | Total Size | Table Data | Indexes |\n|--------|-------|-----------|-----------|---------|\n| `public` | **disk_pressure_data** | **
21 MB** | **21 MB** | 344 kB |\n| `google_ml` | models | 112 kB | 8 kB | 104 kB |\n| `google_ml` | model_family_info | 112 kB | 8 kB | 104 kB |\n| `google_ml` | auth_info | 16 kB | 0 kB | 16 kB |\n\n### Key Findings\n\n1. **Primary Space     Consumer**: The `public.disk_pressure_data` table in the `postgres` database accounts for **~21 MB**, representing approximately **35% of the entire database server**.\n\n2. **Database Breakdown**:\n   - `postgres` database = 33 MB (54% of   total)\n   - `alloydbadmin` database = 15 MB (25% of total)\n   - `alloydbmetadata` database = 13 MB (21% of total)\n\n3. **Index Overhead**: The `disk_pressure_data` table has relatively small indexes (344 kB), so most of the space is       actual data.\n\n### Recommendations\n\nGiven the low disk space alert:\n\n1. **Investigate `disk_pressure_data`** — Check if this table contains:\n   - Time-series or historical data that could be archived\n   - Test/temporary data that can  be deleted\n   - Data requiring partitioning\n\n2. **Verify the table purpose** — Query it to understand its content and access patterns\n\n3. **Consider retention policies** — Implement data cleanup, partitioning by date, or compression if
the data is historical\n\n4. **Check available disk space** — Let me know the total disk capacity to determine urgency\n\nWould you like me to examine the `disk_pressure_data` table more closely (row count, last modified times, schema        details) or check for tables eligible for vacuuming to reclaim space?",
  "duration": "9.384806213s"
}

[boris@ /tmp/helpdesk/helpdesk-v0.10.0-darwin-arm64]$ jq -r '.results[] | select(.failure_id == "db-disk-pressure") | .response_text' faulttest-8bd5d394.json
I'll help you identify which databases and tables are consuming the most storage on the AlloyDB Omni server.
## Disk Space Analysis — AlloyDB Omni Server

### Database Summary
The server has **3 databases** totaling approximately **61 MB**:

| Database | Size | Owner |
|----------|------|-------|
| `postgres` | 33 MB | postgres |
| `alloydbadmin` | 15 MB | alloydbadmin |
| `alloydbmetadata` | 13 MB | alloydbadmin |

### Storage Consumption by Table

The **`postgres` database** is the largest, and the storage analysis reveals:

| Schema | Table | Total Size | Table Data | Indexes |
|--------|-------|-----------|-----------|---------|
| `public` | **disk_pressure_data** | **21 MB** | **21 MB** | 344 kB |
| `google_ml` | models | 112 kB | 8 kB | 104 kB |
| `google_ml` | model_family_info | 112 kB | 8 kB | 104 kB |
| `google_ml` | auth_info | 16 kB | 0 kB | 16 kB |

### Key Findings

1. **Primary Space Consumer**: The `public.disk_pressure_data` table in the `postgres` database accounts for **~21 MB**, representing approximately **35% of the entire database server**.

2. **Database Breakdown**:
   - `postgres` database = 33 MB (54% of total)
   - `alloydbadmin` database = 15 MB (25% of total)
   - `alloydbmetadata` database = 13 MB (21% of total)

3. **Index Overhead**: The `disk_pressure_data` table has relatively small indexes (344 kB), so most of the space is actual data.

### Recommendations

Given the low disk space alert:

1. **Investigate `disk_pressure_data`** — Check if this table contains:
   - Time-series or historical data that could be archived
   - Test/temporary data that can be deleted
   - Data requiring partitioning

2. **Verify the table purpose** — Query it to understand its content and access patterns

3. **Consider retention policies** — Implement data cleanup, partitioning by date, or compression if the data is historical

4. **Check available disk space** — Let me know the total disk capacity to determine urgency

Would you like me to examine the `disk_pressure_data` table more closely (row count, last modified times, schema details) or check for tables eligible for vacuuming to reclaim space?
```


## Docker/Podman sample run

See the platform deployment specifics of running aiHelpDesk Fault Injection Tests directly in Docker/Podman containers [here](../deploy/docker-compose/README.md#5-fault-injection-testing-faulttest). Note how a password, the infra.json and the output/results JSON are passed/mounted to the `faulttest` container:

```
[boris@ /tmp/helpdesk/helpdesk-v0.10.0-deploy/docker-compose]$ date; \
  time DEV_DB_PASSWORD=ChangeMe123 docker run --rm  \
  --network helpdesk_default \
  -v "$(pwd)/infrastructure.json:/infrastructure.json:ro" \
  -v "$(pwd):/output" \
  -w /output \
  -e DEV_DB_PASSWORD \
  ghcr.io/borisdali/helpdesk:latest \
  faulttest run \
  --conn "alloydb-on-vm" \
  --db-agent http://gateway:8080 \
  --api-key gateway-api-key \
  --infra-config /infrastructure.json

Tue Apr 14 17:55:38 EDT 2026

--- Testing: Max connections exhausted (db-max-connections) ---
time=2026-04-14T21:55:39.426Z level=INFO msg="injecting failure" id=db-max-connections type=sql mode=external
time=2026-04-14T21:55:39.632Z level=INFO msg="sending prompt to agent" failure=db-max-connections category=database agent=http://database-agent:1100 prompt_len=173
time=2026-04-14T21:55:58.001Z level=INFO msg="tearing down failure" id=db-max-connections type=sql
time=2026-04-14T21:55:58.112Z level=INFO msg="injecting failure" id=db-long-running-query type=sql mode=external
Result: [PASS] score=85%

--- Testing: Long-running query blocking (db-long-running-query) ---
time=2026-04-14T22:00:58.250Z level=INFO msg="sending prompt to agent" failure=db-long-running-query category=database agent=http://database-agent:1100 prompt_len=172
time=2026-04-14T22:01:18.856Z level=INFO msg="tearing down failure" id=db-long-running-query type=sql
time=2026-04-14T22:01:18.898Z level=INFO msg="injecting failure" id=db-lock-contention type=shell_exec mode=external
Result: [PASS] score=100%

--- Testing: Lock contention / deadlock (db-lock-contention) ---
time=2026-04-14T22:01:19.954Z level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: ACCESS EXCLUSIVE lock held by pid=22"
time=2026-04-14T22:01:19.954Z level=INFO msg="sending prompt to agent" failure=db-lock-contention category=database agent=http://database-agent:1100 prompt_len=171
time=2026-04-14T22:01:47.069Z level=INFO msg="tearing down failure" id=db-lock-contention type=shell_exec
Result: [PASS] score=100%

--- Testing: Table bloat / dead tuples (db-table-bloat) ---
time=2026-04-14T22:01:47.126Z level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
time=2026-04-14T22:01:47.126Z level=INFO msg="injecting failure" id=db-table-bloat type=sql mode=external
time=2026-04-14T22:01:47.816Z level=INFO msg="sending prompt to agent" failure=db-table-bloat category=database agent=http://database-agent:1100 prompt_len=206
time=2026-04-14T22:02:11.289Z level=INFO msg="tearing down failure" id=db-table-bloat type=sql
Result: [PASS] score=100%

--- Testing: High cache miss ratio (db-high-cache-miss) ---
time=2026-04-14T22:02:11.351Z level=INFO msg="injecting failure" id=db-high-cache-miss type=sql mode=external
time=2026-04-14T22:02:11.765Z level=INFO msg="sending prompt to agent" failure=db-high-cache-miss category=database agent=http://database-agent:1100 prompt_len=172
time=2026-04-14T22:02:41.846Z level=INFO msg="tearing down failure" id=db-high-cache-miss type=sql
Result: [PASS] score=100%

--- Testing: Replication lag (db-replication-lag) ---
Result: [SKIP] replica connection not configured (pass --replica-conn)

--- Testing: Session stuck with uncommitted writes (db-idle-in-transaction) ---
time=2026-04-14T22:02:41.932Z level=WARN msg="skipping fault: requires --replica-conn" id=db-replication-lag
time=2026-04-14T22:02:41.932Z level=INFO msg="injecting failure" id=db-idle-in-transaction type=shell_exec mode=external
time=2026-04-14T22:02:42.975Z level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: idle-in-transaction session (pid=38)"
time=2026-04-14T22:02:42.975Z level=INFO msg="sending prompt to agent" failure=db-idle-in-transaction category=database agent=http://database-agent:1100 prompt_len=331
time=2026-04-14T22:02:58.285Z level=INFO msg="tearing down failure" id=db-idle-in-transaction type=shell_exec
time=2026-04-14T22:02:59.322Z level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
time=2026-04-14T22:02:59.322Z level=INFO msg="injecting failure" id=db-terminate-direct-command type=shell_exec mode=external
Result: [PASS] score=93%

--- Testing: Direct terminate — inspect-first check (db-terminate-direct-command) ---
time=2026-04-14T22:03:00.355Z level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: idle-in-transaction session (pid=51)"
time=2026-04-14T22:03:00.355Z level=INFO msg="sending prompt to agent" failure=db-terminate-direct-command category=database agent=http://database-agent:1100 prompt_len=223
time=2026-04-14T22:03:13.746Z level=INFO msg="tearing down failure" id=db-terminate-direct-command type=shell_exec
time=2026-04-14T22:03:14.816Z level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n(0 rows)\n\nDROP TABLE"
time=2026-04-14T22:03:14.817Z level=INFO msg="injecting failure" id=db-vacuum-needed type=sql mode=internal
Result: [PASS] score=85%

--- Testing: Tables needing vacuum (dead tuple bloat) (db-vacuum-needed) ---
time=2026-04-14T22:03:14.861Z level=INFO msg="sending prompt to agent" failure=db-vacuum-needed category=database agent=http://database-agent:1100 prompt_len=217
time=2026-04-14T22:03:28.584Z level=INFO msg="tearing down failure" id=db-vacuum-needed type=sql
Result: [PASS] score=85%

--- Testing: Disk usage — large table growth (db-disk-pressure) ---
time=2026-04-14T22:03:28.648Z level=INFO msg="injecting failure" id=db-disk-pressure type=sql mode=internal
time=2026-04-14T22:03:28.835Z level=INFO msg="sending prompt to agent" failure=db-disk-pressure category=database agent=http://database-agent:1100 prompt_len=205
time=2026-04-14T22:03:38.663Z level=INFO msg="tearing down failure" id=db-disk-pressure type=sql
Result: [PASS] score=100%

=== Fault Test Report: f58bdd41 ===

[PASS] Max connections exhausted (db-max-connections) - score: 85%
[PASS] Long-running query blocking (db-long-running-query) - score: 100%
[PASS] Lock contention / deadlock (db-lock-contention) - score: 100%
[PASS] Table bloat / dead tuples (db-table-bloat) - score: 100%
[PASS] High cache miss ratio (db-high-cache-miss) - score: 100%
[PASS] Session stuck with uncommitted writes (db-idle-in-transaction) - score: 93%
[PASS] Direct terminate — inspect-first check (db-terminate-direct-command) - score: 85%
[PASS] Tables needing vacuum (dead tuple bloat) (db-vacuum-needed) - score: 85%
[PASS] Disk usage — large table growth (db-disk-pressure) - score: 100%

--- Summary ---
Total: 9 | Passed: 9 | Failed: 0 | Rate: 100%
  database: 9/9 (100%)

Report written to faulttest-f58bdd41.json

real    8m0.977s
user    0m0.041s
sys     0m0.070s

```

And review the JSON file with the results, similar to the host run above.

## K8s sample run

See the platform deployment specifics of running aiHelpDesk Fault Injection Testing directly on a host/VM [here](../deploy/helm/README.md#10-fault-injection-testing-faulttest). The special thing about launching the Fault Injection Test on K8s is that it's easier to run it as a job because the secret with the password and the config map with the infra.json are already available and easy to access from inside a job:

```
kubectl -n helpdesk-system apply -f - <<'EOF'
apiVersion: batch/v1
kind: Job
metadata:
  name: faulttest
  namespace: helpdesk-system
spec:
  ttlSecondsAfterFinished: 300
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: faulttest
        image: ghcr.io/borisdali/helpdesk:latest
        args:
          - faulttest
          - run
          - --conn=alloydb-on-vm
          - --db-agent=http://helpdesk-gateway:8080
          - --api-key=$(HELPDESK_CLIENT_API_KEY)
          - --infra-config=/etc/helpdesk/infrastructure.json
        env:
        - name: HELPDESK_CLIENT_API_KEY
          valueFrom:
            secretKeyRef:
              name: <your-gateway.clientAPIKeySecret value>  # same Secret the gateway uses
              key: api-key
        - name: HOME
          value: /home/helpdesk             # so psql finds /home/helpdesk/.pgpass
        volumeMounts:
        - name: infra-config
          mountPath: /etc/helpdesk/infrastructure.json
          subPath: infrastructure.json
          readOnly: true
        - name: pgpass
          mountPath: /home/helpdesk/.pgpass
          subPath: .pgpass
          readOnly: true
      volumes:
      - name: infra-config
        configMap:
          name: helpdesk-config             # ConfigMap name from Step 1
      - name: pgpass
        secret:
          secretName: pgpass                # same Secret the database-agent uses
          defaultMode: 0600
EOF
```

Monitor the run:

```
[boris@ /tmp/helpdesk/helpdesk-v0.10.0-deploy/helm/helpdesk]$ k -nhelpdesk-system logs -f job/faulttest

--- Testing: Max connections exhausted (db-max-connections) ---
time=2026-04-15T01:24:54.794Z level=INFO msg="injecting failure" id=db-max-connections type=sql mode=external
time=2026-04-15T01:24:54.955Z level=INFO msg="sending prompt to agent" failure=db-max-connections category=database agent=http://helpdesk-gateway:8080 prompt_len=173
time=2026-04-15T01:24:54.957Z level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T01:25:20.617Z level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-max-connections reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T01:25:20.617Z level=INFO msg="tearing down failure" id=db-max-connections type=sql
Result: [PASS] score=100%

--- Testing: Long-running query blocking (db-long-running-query) ---
time=2026-04-15T01:25:20.647Z level=INFO msg="injecting failure" id=db-long-running-query type=sql mode=external
time=2026-04-15T01:30:20.758Z level=INFO msg="sending prompt to agent" failure=db-long-running-query category=database agent=http://helpdesk-gateway:8080 prompt_len=172
time=2026-04-15T01:30:20.759Z level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T01:30:50.395Z level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-long-running-query reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK       agent)"
time=2026-04-15T01:30:50.395Z level=INFO msg="tearing down failure" id=db-long-running-query type=sql
time=2026-04-15T01:30:50.422Z level=INFO msg="injecting failure" id=db-lock-contention type=shell_exec mode=external
Result: [PASS] score=100%

--- Testing: Lock contention / deadlock (db-lock-contention) ---
time=2026-04-15T01:30:51.482Z level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: ACCESS EXCLUSIVE lock held by pid=19"
time=2026-04-15T01:30:51.483Z level=INFO msg="sending prompt to agent" failure=db-lock-contention category=database agent=http://helpdesk-gateway:8080 prompt_len=171
time=2026-04-15T01:30:51.483Z level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T01:31:20.549Z level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-lock-contention reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T01:31:20.549Z level=INFO msg="tearing down failure" id=db-lock-contention type=shell_exec
Result: [PASS] score=90%
time=2026-04-15T01:31:20.612Z level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
time=2026-04-15T01:31:20.612Z level=INFO msg="injecting failure" id=db-table-bloat type=sql mode=external

--- Testing: Table bloat / dead tuples (db-table-bloat) ---
time=2026-04-15T01:31:22.431Z level=INFO msg="sending prompt to agent" failure=db-table-bloat category=database agent=http://helpdesk-gateway:8080 prompt_len=206
time=2026-04-15T01:31:22.431Z level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T01:31:36.751Z level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-table-bloat reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T01:31:36.752Z level=INFO msg="tearing down failure" id=db-table-bloat type=sql
time=2026-04-15T01:31:36.829Z level=INFO msg="injecting failure" id=db-high-cache-miss type=sql mode=external
Result: [PASS] score=100%

--- Testing: High cache miss ratio (db-high-cache-miss) ---
time=2026-04-15T01:31:37.829Z level=INFO msg="sending prompt to agent" failure=db-high-cache-miss category=database agent=http://helpdesk-gateway:8080 prompt_len=172
time=2026-04-15T01:31:37.829Z level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T01:32:12.468Z level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-high-cache-miss reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T01:32:12.468Z level=INFO msg="tearing down failure" id=db-high-cache-miss type=sql
Result: [PASS] score=90%

--- Testing: Replication lag (db-replication-lag) ---
Result: [SKIP] replica connection not configured (pass --replica-conn)

--- Testing: Session stuck with uncommitted writes (db-idle-in-transaction) ---
time=2026-04-15T01:32:12.526Z level=WARN msg="skipping fault: requires --replica-conn" id=db-replication-lag
time=2026-04-15T01:32:12.526Z level=INFO msg="injecting failure" id=db-idle-in-transaction type=shell_exec mode=external
time=2026-04-15T01:32:13.578Z level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: idle-in-transaction session (pid=35)"
time=2026-04-15T01:32:13.578Z level=INFO msg="sending prompt to agent" failure=db-idle-in-transaction category=database agent=http://helpdesk-gateway:8080 prompt_len=331
time=2026-04-15T01:32:13.578Z level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T01:32:30.580Z level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-idle-in-transaction reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK      agent)"
time=2026-04-15T01:32:30.580Z level=INFO msg="tearing down failure" id=db-idle-in-transaction type=shell_exec
time=2026-04-15T01:32:31.659Z level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
time=2026-04-15T01:32:31.659Z level=INFO msg="injecting failure" id=db-terminate-direct-command type=shell_exec mode=external
Result: [PASS] score=86%

--- Testing: Direct terminate — inspect-first check (db-terminate-direct-command) ---
time=2026-04-15T01:32:32.805Z level=INFO msg="shell_exec completed" output="CREATE TABLE\nINSERT 0 1\nInjected: idle-in-transaction session (pid=49)"
time=2026-04-15T01:32:32.806Z level=INFO msg="sending prompt to agent" failure=db-terminate-direct-command category=database agent=http://helpdesk-gateway:8080 prompt_len=223
time=2026-04-15T01:32:32.806Z level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T01:32:51.833Z level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-terminate-direct-command reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T01:32:51.833Z level=INFO msg="tearing down failure" id=db-terminate-direct-command type=shell_exec
time=2026-04-15T01:32:52.994Z level=INFO msg="shell_exec completed" output="pg_terminate_backend \n----------------------\n t\n(1 row)\n\nDROP TABLE"
time=2026-04-15T01:32:52.994Z level=INFO msg="injecting failure" id=db-vacuum-needed type=sql mode=internal
Result: [PASS] score=86%

--- Testing: Tables needing vacuum (dead tuple bloat) (db-vacuum-needed) ---
time=2026-04-15T01:32:53.054Z level=INFO msg="sending prompt to agent" failure=db-vacuum-needed category=database agent=http://helpdesk-gateway:8080 prompt_len=217
time=2026-04-15T01:32:53.054Z level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T01:33:12.519Z level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-vacuum-needed reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T01:33:12.520Z level=INFO msg="tearing down failure" id=db-vacuum-needed type=sql
Result: [PASS] score=80%

--- Testing: Disk usage — large table growth (db-disk-pressure) ---
time=2026-04-15T01:33:12.552Z level=INFO msg="injecting failure" id=db-disk-pressure type=sql mode=internal
time=2026-04-15T01:33:12.878Z level=INFO msg="sending prompt to agent" failure=db-disk-pressure category=database agent=http://helpdesk-gateway:8080 prompt_len=205
time=2026-04-15T01:33:12.878Z level=INFO msg="using gateway REST API" agent_name=database purpose=diagnostic
time=2026-04-15T01:33:26.398Z level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-disk-pressure reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-04-15T01:33:26.400Z level=INFO msg="tearing down failure" id=db-disk-pressure type=sql
Result: [PASS] score=80%

=== Fault Test Report: b7ea876d ===

[PASS] Max connections exhausted (db-max-connections) - score: 100% [tool evidence: text match]
[PASS] Long-running query blocking (db-long-running-query) - score: 100% [tool evidence: text match]
[PASS] Lock contention / deadlock (db-lock-contention) - score: 90% [tool evidence: text match]
[PASS] Table bloat / dead tuples (db-table-bloat) - score: 100% [tool evidence: text match]
[PASS] High cache miss ratio (db-high-cache-miss) - score: 90% [tool evidence: text match]
[PASS] Session stuck with uncommitted writes (db-idle-in-transaction) - score: 86% [tool evidence: text match]
[PASS] Direct terminate — inspect-first check (db-terminate-direct-command) - score: 86% [tool evidence: text match]
[PASS] Tables needing vacuum (dead tuple bloat) (db-vacuum-needed) - score: 80% [tool evidence: text match]
[PASS] Disk usage — large table growth (db-disk-pressure) - score: 80% [tool evidence: text match]

--- Summary ---
Total: 9 | Passed: 9 | Failed: 0 | Rate: 100%
  database: 9/9 (100%)

Note: 9 fault(s) used text-based tool evidence scoring (agent did not emit
structured tool call data). These scores may be less reliable. For precise
tool evidence, point --db-agent directly at the agent A2A URL rather than
the gateway, or use an ADK-based agent.

Report written to faulttest-b7ea876d.json
```

And finally review the JSON file with the results, similar to the host and Docker/Podman container runs.
