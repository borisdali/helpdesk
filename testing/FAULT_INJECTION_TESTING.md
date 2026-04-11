# aiHelpDesk: Fault Injection Testing (Internal / Docker-compose)

> **Customer-facing guide:** If you want to validate aiHelpDesk agents against your own staging or canary database — without Docker or cluster access — see **[docs/FAULTTEST.md](../docs/FAULTTEST.md)**. That guide covers external (SQL-only) injection, SSH injection, remediation verification, and the policy safety guard.

This page covers the **internal engineering harness**: running the full fault catalog against the Docker-compose test stack, wiring faulttest into CI/CD, and developing or extending failure modes.

There's a curated list of failure modes, the injection mechanism and the way to inject failures
manually or automatically (e.g. as part of the CI/CD pipeline).
Once a failure occurs, use aiHelpDesk to see if it can rectify
a failure automatically or at least provide guidance on how
to proceed.

The catalog currently contains **27 failure modes** (16 database, 7 Kubernetes, 2 host, 2 compound) and is embedded into the `faulttest` binary at build time — the binary works without the source tree present. Customers can layer their own fault files on top via `--catalog`; see [docs/FAULTTEST.md](../docs/FAULTTEST.md#9-customer-fault-catalogs) for details. The sample log below predates several additions and shows an earlier count — it is kept for reference.

## Manual Testing: List available fault injection tests

```
  go run ./testing/cmd/faulttest list
```

The output now includes a `SOURCE` column (`builtin` for catalog entries, `custom` for entries added via `--catalog`). Pass `--source builtin` or `--source custom` to filter. Abridged sample log (see the [full log](FAULT_INJECTION_TESTING_SAMPLE.md) for details — note the sample predates the SOURCE column):

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest list
ID                             CATEGORY     SEVERITY   EXTERNAL SOURCE   NAME
----------------------------------------------------------------------------------------------------
db-max-connections             database     high       yes      builtin  Max connections exhausted
db-long-running-query          database     high       yes      builtin  Long-running query blocking
db-lock-contention             database     high       yes      builtin  Lock contention / deadlock
db-table-bloat                 database     medium     yes      builtin  Table bloat / dead tuples
db-high-cache-miss             database     medium     yes      builtin  High cache miss ratio
db-connection-refused          database     critical            builtin  Database connection refused
db-auth-failure                database     critical            builtin  Authentication failure
db-not-exist                   database     critical            builtin  Database does not exist
db-replication-lag             database     high       yes      builtin  Replication lag
db-idle-in-transaction         database     high       yes      builtin  Session stuck with uncommitted writes
db-terminate-direct-command    database     high       yes      builtin  Direct terminate — inspect-first check
k8s-crashloop                  kubernetes   critical            builtin  CrashLoopBackOff
k8s-pending                    kubernetes   critical            builtin  Pending pod (unschedulable)
k8s-image-pull                 kubernetes   critical            builtin  ImagePullBackOff
k8s-no-endpoints               kubernetes   high                builtin  Service with no endpoints
k8s-pvc-pending                kubernetes   critical            builtin  PVC pending (bad StorageClass)
k8s-oomkilled                  kubernetes   critical            builtin  OOMKilled
k8s-scale-to-zero              kubernetes   high                builtin  Deployment scaled to zero replicas
db-vacuum-needed               database     medium     yes      builtin  Tables needing vacuum (dead tuple bloat)
db-disk-pressure               database     medium     yes      builtin  Disk usage — large table growth
host-container-stopped         host         critical            builtin  Database container stopped
host-pg-crash                  host         critical            builtin  PostgreSQL process crash inside container
db-pg-hba-corrupt              database     critical            builtin  pg_hba.conf corrupted — all connections rejected
db-process-kill                database     critical            builtin  PostgreSQL postmaster killed (SIGKILL)
db-config-bad-param            database     high                builtin  postgresql.conf invalid parameter
compound-db-pod-crash          compound     critical            builtin  DB unreachable + pod crashing
compound-db-no-endpoints       compound     critical            builtin  DB timeout + no endpoints

Total: 27 failure modes
```

This is a good start because in this step we verify the
`faulttest` CLI is built properly and can read and parse the catalog
of failure modes successfully.


## Manual Testing: Start the test database (with replica for replication tests)

```
  docker compose \
    -f testing/docker/docker-compose.yaml \
    -f testing/docker/docker-compose.repl.yaml \
    up -d
```

> **Note:** `docker-compose.repl.yaml` adds a streaming replica on port 15433
> required by the `db-replication-lag` test.  If you only need database tests
> that don't involve replication, the base compose file is sufficient.



Sample log of running the above command:

```
[boris@ ~/helpdesk]$ docker compose -f testing/docker/docker-compose.yaml up -d
[+] Running 16/16
 ✔ postgres Pulled                                                                                                                                                                                                                          15.5s
   ✔ c52040205004 Pull complete                                                                                                                                                                                                             10.7s
   ✔ 43a5a9e2423c Pull complete                                                                                                                                                                                                              8.5s
 ✔ pgloader Pulled                                                                                                                                                                                                                          15.5s
   ✔ dd1cde76fb45 Pull complete                                                                                                                                                                                                              8.4s
   ✔ d637807aba98 Pull complete                                                                                                                                                                                                             10.4s
   ✔ 085035fb9611 Pull complete                                                                                                                                                                                                              8.4s
   ✔ 1f84dfb38d07 Pull complete                                                                                                                                                                                                              8.4s
   ✔ b281ae1a88da Pull complete                                                                                                                                                                                                              8.5s
   ✔ 9ef0fad1d65b Pull complete                                                                                                                                                                                                              8.5s
   ✔ 5b8592097c2e Pull complete                                                                                                                                                                                                              8.5s
   ✔ 7f59970af9fd Pull complete                                                                                                                                                                                                              8.5s
   ✔ c69e06bff6d2 Pull complete                                                                                                                                                                                                             13.6s
   ✔ 83d2335820b1 Pull complete                                                                                                                                                                                                              8.4s
   ✔ 8b1fea7561e1 Pull complete                                                                                                                                                                                                             10.5s
   ✔ 64a2748449a1 Pull complete                                                                                                                                                                                                              8.5s
[+] Running 4/4
 ✔ Network docker_default            Created                                                                                                                                                                                                 0.2s
 ✔ Volume "docker_pgdata"            Created                                                                                                                                                                                                 0.0s
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 6.2s
 ✔ Container helpdesk-test-pgloader  Started                                                                                                                                                                                                 5.9s

[boris@ ~/helpdesk]$ docker compose -f testing/docker/docker-compose.yaml ps
NAME                     IMAGE         COMMAND                  SERVICE    CREATED         STATUS                   PORTS
helpdesk-test-pg         postgres:16   "docker-entrypoint.s…"   postgres   8 minutes ago   Up 8 minutes (healthy)   0.0.0.0:15432->5432/tcp
helpdesk-test-pgloader   postgres:16   "sleep infinity"         pgloader   8 minutes ago   Up 8 minutes             5432/tcp

[boris@ ~/helpdesk]$ docker exec -ti helpdesk-test-pg /bin/bash
root@5d080375a8f4:/# ps -elfH
F S UID        PID  PPID  C PRI  NI ADDR SZ WCHAN  STIME TTY          TIME CMD
4 S root      3827     0  0  80   0 -  1796 do_wai 18:41 pts/0    00:00:00 /bin/bash
4 R root      3841  3827  0  80   0 -  2262 -      18:41 pts/0    00:00:00   ps -elfH
4 S postgres     1     0  0  80   0 - 27428 -      18:02 ?        00:00:00 postgres -c max_connections=20 -c shared_buffers=32MB -c log_statement=all
1 S postgres    62     1  0  80   0 - 27461 -      18:02 ?        00:00:00   postgres: checkpointer
1 S postgres    63     1  0  80   0 - 27462 -      18:02 ?        00:00:00   postgres: background writer
1 S postgres    65     1  0  80   0 - 27428 -      18:02 ?        00:00:00   postgres: walwriter
1 S postgres    66     1  0  80   0 - 27822 -      18:02 ?        00:00:00   postgres: autovacuum launcher
1 S postgres    67     1  0  80   0 - 27784 -      18:02 ?        00:00:00   postgres: logical replication launcher

root@5d080375a8f4:/# psql -U postgres
psql (16.11 (Debian 16.11-1.pgdg13+1))
Type "help" for help.

postgres-# \q
root@5d080375a8f4:/# exit
exit

[boris@ ~/helpdesk]$ PGPASSWORD=testpass psql -h localhost -p 15432 -d testdb -U postgres -c "SELECT 1"
 ?column?
----------
        1
(1 row)
```

## Starting the agents for faulttest

Kubernetes tests inject failures into the `helpdesk-test` namespace.  For the
k8s agent to access that namespace it needs to know about it (via
`HELPDESK_INFRA_CONFIG`) and a policy that permits it (via `HELPDESK_POLICY_FILE`).
Pre-built test versions of both files live under `testing/`:

```bash
# Database agent
HELPDESK_MODEL_VENDOR=anthropic \
HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001 \
HELPDESK_API_KEY=$HELPDESK_API_KEY \
HELPDESK_INFRA_CONFIG=testing/testing.infra.json \
HELPDESK_POLICY_FILE=testing/testing.policy.yaml \
HELPDESK_POLICY_ENABLED=true \
  go run ./agents/database &

# Kubernetes agent  (set HELPDESK_KUBE_CONTEXT to your local cluster context)
HELPDESK_MODEL_VENDOR=anthropic \
HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001 \
HELPDESK_API_KEY=$HELPDESK_API_KEY \
HELPDESK_INFRA_CONFIG=testing/testing.infra.json \
HELPDESK_POLICY_FILE=testing/testing.policy.yaml \
HELPDESK_POLICY_ENABLED=true \
  go run ./agents/k8s &
```

`testing/testing.infra.json` registers the `helpdesk-test` namespace as a
test-environment database.  `testing/testing.policy.yaml` grants unrestricted
read/write/destructive access to resources tagged `test`.  **Do not use these
files in production.**

## Manual Testing: Inject/teardown manually (no agents needed)

  ### Inject a failure#1: table bloat
There are 17 failure modes listed above. Here are a few examples of injecting some of these faults:

```
  go run ./testing/cmd/faulttest inject --id db-table-bloat --conn "host=localhost port=15432 dbname=testdb user=postgres password=testpass"
```

Sample log of running the above command:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest inject --id db-table-bloat --conn "host=localhost port=15432 dbname=testdb user=postgres password=testpass"
time=2026-01-30T12:49:39.902-05:00 level=INFO msg="injecting failure" id=db-table-bloat type=sql
Failure injected: Table bloat / dead tuples

Suggested prompt for the agent:
The database at host=localhost port=15432 dbname=testdb user=postgres password=testpass seems to be using more disk than expected and some queries are getting slower. Please investigate table health.


To tear down: faulttest teardown --id db-table-bloat [same flags]
```

  ### Feed the suggested prompt to aiHelpDesk:

```
[boris@ ~/helpdesk]$ date; HELPDESK_MODEL_VENDOR=anthropic HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001 HELPDESK_API_KEY=$HELPDESK_API_KEY HELPDESK_AGENT_URLS=http://localhost:1100,http://localhost:1102,http://localhost:1104  HELPDESK_INFRA_CONFIG=deploy/docker-compose/infrastructure.json go run ./cmd/helpdesk
Fri Jan 30 16:20:03 EST 2026
time=2026-01-30T16:20:04.489-05:00 level=INFO msg="discovering agent" url=http://localhost:1100
time=2026-01-30T16:20:04.504-05:00 level=INFO msg="discovered agent" name=postgres_database_agent url=http://localhost:1100
time=2026-01-30T16:20:04.504-05:00 level=INFO msg="discovering agent" url=http://localhost:1102
time=2026-01-30T16:20:04.518-05:00 level=INFO msg="discovered agent" name=k8s_agent url=http://localhost:1102
time=2026-01-30T16:20:04.518-05:00 level=INFO msg="discovering agent" url=http://localhost:1104
time=2026-01-30T16:20:04.536-05:00 level=INFO msg="discovered agent" name=incident_agent url=http://localhost:1104
time=2026-01-30T16:20:04.536-05:00 level=INFO msg="expected expert agents" agents="postgres_database_agent, k8s_agent, incident_agent"
time=2026-01-30T16:20:04.536-05:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-01-30T16:20:04.536-05:00 level=INFO msg="confirming agent availability" agent=postgres_database_agent url=http://localhost:1100
time=2026-01-30T16:20:04.537-05:00 level=INFO msg="agent available" agent=postgres_database_agent
time=2026-01-30T16:20:04.537-05:00 level=INFO msg="confirming agent availability" agent=k8s_agent url=http://localhost:1102
time=2026-01-30T16:20:04.537-05:00 level=INFO msg="agent available" agent=k8s_agent
time=2026-01-30T16:20:04.537-05:00 level=INFO msg="confirming agent availability" agent=incident_agent url=http://localhost:1104
time=2026-01-30T16:20:04.537-05:00 level=INFO msg="agent available" agent=incident_agent
time=2026-01-30T16:20:04.537-05:00 level=INFO msg="infrastructure config loaded" db_servers=3 k8s_clusters=1 vms=1
time=2026-01-30T16:20:04.537-05:00 level=INFO msg="orchestrator initialized" available_agents=3

User -> The database at host=localhost port=15432 dbname=testdb user=postgres password=testpass seems to be using more disk than expected and some queries are getting slower. Please investigate table health.

Agent -> I'll investigate the database at localhost:15432 to check table health and identify potential disk usage issues. Let me start by checking connectivity and then examining table statistics.I'll investigate the database health,         focusing on table statistics and disk usage. Let me start by checking connectivity and then examining the tables.I'll investigate the database health, focusing on table statistics and disk usage. Let me start by checking connectivity and     then examining the tables.Good! The database is accessible. Now let me examine the table statistics to identify which tables are consuming the most disk space and have unhealthy statistics.## Investigation Summary

I've identified **significant table health issues** that are causing your increased disk usage and slower queries:

### **Critical Finding: Severe Table Bloat** 🚨

**Table: `test_bloat_table`**
- **Total Size:** 25 MB (76% of your entire database)
- **Live Rows:** 10,000
- **Dead Rows:** 90,000 ⚠️
- **Dead Row Ratio:** 90% of the table is dead tuples!
- **Disk Waste:** ~22.5 MB of wasted space from dead rows

### Root Cause Analysis

The high number of dead rows (90,000) indicates:
1. **Deleted or updated rows** have not been cleaned up
2. **VACUUM has not been run** (or is insufficient) to reclaim space
3. **Query performance suffers** because PostgreSQL must scan through dead tuples

This explains:
- ✓ Higher disk usage than expected
- ✓ Slower query performance (more I/O scanning dead rows)
- ✓ High delete activity: 90,000 rows deleted in this workload

### Actionable Recommendations
**Immediate Actions (Priority 1):**

-- Run a full VACUUM to reclaim space
VACUUM FULL ANALYZE test_bloat_table;

-- Or for minimal downtime, use regular VACUUM multiple times
VACUUM ANALYZE test_bloat_table;

**Medium-term (Priority 2):**
```sql
-- Check and tune autovacuum settings
-- Consider lowering vacuum costs or increasing frequency for this table
ALTER TABLE test_bloat_table SET (
  autovacuum_vacuum_scale_factor = 0.01,
  autovacuum_analyze_scale_factor = 0.005
);

**Monitoring:**
- Monitor the dead row count and table size over time
- Consider enabling `log_autovacuum_min_duration = 0` to track vacuum operations

The database itself is healthy (99.95% cache hit ratio, no deadlocks), but this table bloat is the primary performance bottleneck.
```


  ### Run the aiHelpDesk recommended action and verify that it indeed worked:

```
[boris@ ~/helpdesk]$ PGPASSWORD=testpass psql -h localhost -p 15432 -d testdb -U postgres -c "SELECT relname, n_dead_tup, n_live_tup FROM pg_stat_user_tables WHERE relname = 'test_bloat_table'"
     relname      | n_dead_tup | n_live_tup
------------------+------------+------------
 test_bloat_table |      90000 |      10000
(1 row)

[boris@ ~/helpdesk]$ PGPASSWORD=testpass psql -h localhost -p 15432 -d testdb -U postgres -c "VACUUM FULL ANALYZE test_bloat_table"
VACUUM

[boris@ ~/helpdesk]$ PGPASSWORD=testpass psql -h localhost -p 15432 -d testdb -U postgres -c "SELECT relname, n_dead_tup, n_live_tup FROM pg_stat_user_tables WHERE relname = 'test_bloat_table'"
     relname      | n_dead_tup | n_live_tup
------------------+------------+------------
 test_bloat_table |          0 |      10000
(1 row)
```


  ### Inject a failure#2: too many client connections

Here's another example of injecting a fault, this time with the helpf of `pgloader`:

```
  docker compose -f testing/docker/docker-compose.yaml up -d pgloader
```

Sample log of running the above command:

```
[boris@ ~/helpdesk]$ docker compose -f testing/docker/docker-compose.yaml up -d pgloader
[+] Running 2/2
 ✔ Container helpdesk-test-pg        Healthy                                                                                                                                                                                                 0.5s
 ✔ Container helpdesk-test-pgloader  Running                                                                                                                                                                                                 0.0s

[boris@ ~/helpdesk]$ docker compose -f testing/docker/docker-compose.yaml ps
NAME                     IMAGE         COMMAND                  SERVICE    CREATED             STATUS                 PORTS
helpdesk-test-pg         postgres:16   "docker-entrypoint.s…"   postgres   5 hours ago         Up 5 hours (healthy)   0.0.0.0:15432->5432/tcp
helpdesk-test-pgloader   postgres:16   "sleep infinity"         pgloader   About an hour ago   Up About an hour       5432/tcp

[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest inject --id db-max-connections --conn "host=localhost port=15432 dbname=testdb user=postgres password=testpass"
time=2026-01-30T18:43:39.439-05:00 level=INFO msg="injecting failure" id=db-max-connections type=docker_exec
Failure injected: Max connections exhausted

Suggested prompt for the agent:
Users are getting "too many clients" errors connecting to the database. The connection_string is `host=localhost port=15432 dbname=testdb user=postgres password=testpass` — use it verbatim for all tool calls. Please investigate.


To tear down: faulttest teardown --id db-max-connections [same flags]

[boris@ ~/helpdesk]$ PGPASSWORD=testpass psql -h localhost -p 15432 -d testdb -U postgres -c "SELECT 1"
psql: error: connection to server at "localhost" (::1), port 15432 failed: FATAL:  sorry, too many clients already
```

  ### Feed the suggested prompt to aiHelpDesk:

```
[boris@ ~/helpdesk]$ date; HELPDESK_MODEL_VENDOR=anthropic HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001 HELPDESK_API_KEY=$HELPDESK_API_KEY HELPDESK_AGENT_URLS=http://localhost:1100,http://localhost:1102,http://localhost:1104  HELPDESK_INFRA_CONFIG=deploy/docker-compose/infrastructure.json go run ./cmd/helpdesk
Fri Jan 30 18:59:16 EST 2026
time=2026-01-30T18:59:16.492-05:00 level=INFO msg="discovering agent" url=http://localhost:1100
time=2026-01-30T18:59:16.497-05:00 level=INFO msg="discovered agent" name=postgres_database_agent url=http://localhost:1100
time=2026-01-30T18:59:16.497-05:00 level=INFO msg="discovering agent" url=http://localhost:1102
time=2026-01-30T18:59:16.498-05:00 level=INFO msg="discovered agent" name=k8s_agent url=http://localhost:1102                                                                                                                                     time=2026-01-30T18:59:16.498-05:00 level=INFO msg="discovering agent" url=http://localhost:1104
time=2026-01-30T18:59:16.500-05:00 level=INFO msg="discovered agent" name=incident_agent url=http://localhost:1104
time=2026-01-30T18:59:16.500-05:00 level=INFO msg="expected expert agents" agents="postgres_database_agent,sk8s_agent, incident_ngent"                                                                                                            time=2026-01-30T18:59:16.503-05:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-01-30T18:59:16.503-05:00 level=INFO msg="confirming agent availability" agent=postgres_database_agent url=http://localhost:1100
time=2026-01-30T18:59:16.503-05:00 level=INFO msg="agent available" agent=postgres_database_agent
time=2026-01-30T18:59:16.503-05:00 level=INFO msg="confirming agent availability" agent=k8s_agent url=http://localhost:1102
time=2026-01-30T18:59:16.503-05:00 level=INFO msg="agent available" agent=k8s_agent
time=2026-01-30T18:59:16.503-05:00 level=INFO msg="confirming agent availability" agent=incident_agent url=http://localhost:1104
time=2026-01-30T18:59:16.503-05:00 level=INFO msg="agent available" agent=incident_agent
time=2026-01-30T18:59:16.504-05:00 level=INFO msg="infrastructure config loaded" db_servers=3 k8s_clusters=1 vms=1
time=2026-01-30T18:59:16.504-05:00 level=INFO msg="orchestrator initialized" available_agents=3

User -> Users are getting "too many clients" errors connecting to the database. The connection_string is `host=localhost port=15432 dbname=testdb user=postgres password=testpass` — use it verbatim for all tool calls. Please investigate.

Agent -> **Incident bundle created successfully.**

**Incident ID:** `ebb2f437`
**Bundle Path:** `/tmp/incident-ebb2f437-20260130-185951.tar.gz`

**Key Findings:**

The database server at `localhost:15432` **is running and accessible**, but it has **reached its maximum connection limit**. New connections are being rejected with `FATAL: sorry, too many clients already`.

**Root Cause:** PostgreSQL's `max_connections` parameter has been exceeded. All available connection slots are occupied, preventing new clients from connecting.

**Recommended Actions:**

1. **Immediate relief:**
   - Identify and terminate idle connections on the server
   - Restart the PostgreSQL service (if safe in your environment) to free all connections
   - Implement a connection pooler (PgBouncer, pgPool-II) to manage connections more efficiently

2. **Long-term fix:**
   - Increase `max_connections` in `postgresql.conf` (currently at the limit)
   - Recalculate appropriate `max_connections` based on: `max_connections = (Available RAM / shared_buffers) × 0.25` or similar sizing
   - Review application connection handling for connection leaks
   - Monitor connection usage to prevent this situation recurring

3. **Diagnostic bundle:**
   - The incident bundle (ID: `ebb2f437`) has been created and contains OS, storage, and attempted database diagnostics
   - Share this with your database vendor or support team if needed

Once you increase `max_connections` or reduce active connections, users should be able to reconnect.
User ->
```

---

## Mutation Safeguard Verification

The database and K8s agents implement two-level post-mutation verification for
`terminate_connection`, `cancel_query`, `delete_pod`, `restart_deployment`, and
`scale_deployment`. The tests below verify that these safeguards surface a clear
failure message to the user instead of silently reporting success when the
underlying operation did not take effect.

### Layer 2 (Component) — Unit tests with sequence mocks

The fast path. These run as part of `go test ./...` with no external
dependencies:

```
go test ./agents/database/... -v -run "TestTerminateConnectionTool_Level|TestCancelQueryTool_Level"
go test ./agents/k8s/...     -v -run "TestDeletePodTool_Verification|TestRestartDeploymentTool_Verification|TestScaleDeploymentTool_Verification"
```

Each test uses a sequence mock that injects the failure condition at the
verification step (call #2 or #3), then asserts that the tool returns the
expected `TERMINATION FAILED` / `CANCELLATION FAILED` / `VERIFICATION FAILED` /
`VERIFICATION WARNING` message.

| Test | Safeguard triggered | Injected condition |
|---|---|---|
| `TestTerminateConnectionTool_Level1_ReturnedFalse` | DB Level 1 | `pg_terminate_backend` returns `f` |
| `TestTerminateConnectionTool_Level2_PidStillAlive` | DB Level 2 | `still_alive = 1` in verify query |
| `TestCancelQueryTool_Level1_ReturnedFalse` | DB Level 1 | `pg_cancel_backend` returns `f` |
| `TestCancelQueryTool_Level2_StillActive` | DB Level 2 | `state = active` in verify query |
| `TestDeletePodTool_VerificationWarning_PodStillTerminating` | K8s Level 2 | `kubectl get pod` exits 0 (pod still visible) |
| `TestRestartDeploymentTool_VerificationWarning_AnnotationMissing` | K8s Level 2 | `restartedAt` absent from annotations |
| `TestScaleDeploymentTool_VerificationFailed_WrongReplicas` | K8s Level 2 | `spec.replicas` doesn't match requested count |


### Layer 3/4 (Integration) — Real infrastructure scenarios

These require a running database or cluster. They confirm the safeguards fire
against real PostgreSQL and Kubernetes semantics, not just mock responses.

---

#### DB Level 1: non-superuser cannot terminate privileged session

**What it tests:** `pg_terminate_backend()` returns `false` when a role without
`pg_signal_backend` tries to terminate a superuser backend.

**Infrastructure needed:** the test Docker Compose stack
(`testing/docker/docker-compose.yaml`).

**Steps:**

```bash
# 1. Start the test database
docker compose -f testing/docker/docker-compose.yaml up -d

# 2. Inject the scenario (runs inside the pgloader container)
docker compose -f testing/docker/docker-compose.yaml exec pgloader \
    bash /testing/sql/inject_terminate_permission_denied.sh

# The script prints a restricted connection string, e.g.:
#   host=postgres port=5432 dbname=testdb user=helpdesk_restricted_test password=Restricted1!

# 3. Start the database agent and the orchestrator (or use gateway-repl.sh).
#    Connect the agent using the RESTRICTED connection string.

# 4. Ask the agent:
#    "Show me all active connections on host=postgres port=5432 dbname=testdb
#     user=helpdesk_restricted_test password=Restricted1!
#     and terminate the pg_sleep session."

# Expected: agent calls get_active_connections, identifies the sleeping PID,
# calls terminate_connection, and reports:
#   "TERMINATION FAILED: pg_terminate_backend(<pid>) returned false.
#    The backend may have already exited or this role lacks pg_signal_backend privilege."
# NOT: "✅ Connection terminated successfully."

# 5. Tear down
docker compose -f testing/docker/docker-compose.yaml exec pgloader \
    bash /testing/sql/teardown_terminate_permission_denied.sh
docker compose -f testing/docker/docker-compose.yaml down -v
```

---

#### K8s Level 2: pod stuck in Terminating due to blocking finalizer

**What it tests:** `kubectl delete pod` is accepted (exit 0) but the pod stays
in `Terminating` state because a finalizer is never cleared. The Level-2
verification check finds the pod still visible and returns `VERIFICATION WARNING`.

**Infrastructure needed:** any reachable Kubernetes cluster (kind, minikube, or
a real cluster).

**Steps:**

```bash
# 1. Create a pod with a blocking finalizer
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: stuck-terminating-test
  namespace: default
  finalizers:
    - helpdesk.io/test-block   # never cleared — blocks deletion
spec:
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
EOF

# Wait for the pod to be Running
kubectl wait pod stuck-terminating-test -n default --for=condition=Ready --timeout=60s

# 2. Ask the agent:
#    "Delete the pod stuck-terminating-test in namespace default."

# Expected: agent calls delete_pod, kubectl accepts the deletion, but the
# Level-2 check (kubectl get pod) finds the pod still present and returns:
#   "VERIFICATION WARNING: pod "stuck-terminating-test" still appears in namespace
#    "default" after deletion. It may still be in Terminating state."
# NOT: silent success.

# 3. Clean up — remove the finalizer so the pod can actually terminate
kubectl patch pod stuck-terminating-test -n default \
    -p '{"metadata":{"finalizers":[]}}' --type=merge
kubectl delete pod stuck-terminating-test -n default --ignore-not-found
```

---

#### DB Level 2: backend ignores SIGTERM (advanced / hard to reproduce)

`pg_terminate_backend()` sends SIGTERM to the backend process. In rare cases a
backend stuck in an uninterruptible kernel wait (e.g. waiting on NFS I/O or a
blocking system call) will not respond to SIGTERM, leaving the PID present in
`pg_stat_activity` even after `pg_terminate_backend` returns `true`.

This scenario is difficult to reproduce reliably in CI. The safeguard for it is
the Level-2 verify query (`SELECT count(*) AS still_alive FROM pg_stat_activity
WHERE pid = X`). To test the code path in isolation, use the unit test
`TestTerminateConnectionTool_Level2_PidStillAlive` which mocks the verify
response to `still_alive = 1`.
