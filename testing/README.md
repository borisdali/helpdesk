# aiHelpDesk: Testing

This page outlines the Failure Testing Framework included
with aiHelpDesk. There's a list of currated failure modes,
the injection mechanism and the way to inject failures
manually or automatically (e.g. as part of the CI/CD pipeline).
Once a failure occurs, use aiHelpDesk to see if it can rectify
a failure automatically or at least provide guidance on how
to proceed.

## Manual: List Failure Modes

This is a good start as it because in this step we verify the
faulttest CLI is built properly and can read and parse the catalog
of failure modes successfully.

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest list
ID                             CATEGORY     SEVERITY   NAME
--------------------------------------------------------------------------------
db-max-connections             database     high       Max connections exhausted
db-long-running-query          database     high       Long-running query blocking
db-lock-contention             database     high       Lock contention / deadlock
db-table-bloat                 database     medium     Table bloat / dead tuples
db-high-cache-miss             database     medium     High cache miss ratio
db-connection-refused          database     critical   Database connection refused
db-auth-failure                database     critical   Authentication failure
db-not-exist                   database     critical   Database does not exist
db-replication-lag             database     high       Replication lag
k8s-crashloop                  kubernetes   critical   CrashLoopBackOff
k8s-pending                    kubernetes   critical   Pending pod (unschedulable)
k8s-image-pull                 kubernetes   critical   ImagePullBackOff
k8s-no-endpoints               kubernetes   high       Service with no endpoints
k8s-pvc-pending                kubernetes   critical   PVC pending (bad StorageClass)
k8s-oomkilled                  kubernetes   critical   OOMKilled
compound-db-pod-crash          compound     critical   DB unreachable + pod crashing
compound-db-no-endpoints       compound     critical   DB timeout + no endpoints

Total: 17 failure modes
```

## Manual: Start the test database

```
  2. Start the test database
  docker compose -f testing/docker/docker-compose.yaml up -d

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

  3. Test inject/teardown manually (no agents needed)

  # Inject a failure
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest inject --id db-table-bloat --conn "host=localhost port=15432 dbname=testdb user=postgres password=testpass"
time=2026-01-30T12:49:39.902-05:00 level=INFO msg="injecting failure" id=db-table-bloat type=sql
Failure injected: Table bloat / dead tuples

Suggested prompt for the agent:
The database at host=localhost port=15432 dbname=testdb user=postgres password=testpass seems to be using more disk than expected and some queries are getting slower. Please investigate table health.


To tear down: faulttest teardown --id db-table-bloat [same flags]
```

Let's feed the suggested prompt to aiHelpDesk:

```
[boris@cassiopeia ~/cassiopeia/helpdesk]$ date; HELPDESK_MODEL_VENDOR=anthropic HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001 HELPDESK_API_KEY=$(cat ../llm/boris_claude_console_onboarding_api_key) HELPDESK_AGENT_URLS=http://localhost:1100,htt
p://localhost:1102,http://localhost:1104  HELPDESK_INFRA_CONFIG=infrastructure.json go run ./cmd/helpdesk
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
```

**Monitoring:**
- Monitor the dead row count and table size over time
- Consider enabling `log_autovacuum_min_duration = 0` to track vacuum operations

The database itself is healthy (99.95% cache hit ratio, no deadlocks), but this table bloat is the primary performance bottleneck.
```


  # Run the recommended action and verify that it took effect:

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
