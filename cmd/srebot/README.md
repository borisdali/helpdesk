# aiHelpDesk: demo O11y watcher / SRE boot app

The goal of this demo app is to showcase iHelpDesk
self diagnostic and troubleshooting funcionality.

## Architecture

If an upstream agent or an app doesn't tlak A2A natively,
the way to send requests to iHelpDesk is via the REST/gRPC gateway
The gateway is a stateless HTTP endpoint that translates POST
/api/v1/incidents into an A2A tool call.

The demo app consists of five sequential phases, all called
via the gateway, where the normal run only triggers the first two:
the agents discovery and the database health check.

All five phases can be also triggered with the '-force' flag.

## Phase Descriptions

The phase descriptions are as follows:
```
  Phase 1 — Agent Discovery: GET /api/v1/agents to list available agents.
  Phase 2 — Health Check: POST /api/v1/db/check_connection with the connection string. If no anomaly keywords are found in the response, it reports "all clear" and exits (unless -force is set).
  Phase 3 — AI Diagnosis: POST /api/v1/query  →  DB agent investigates autonomously
  Phase 4 — Create Incident Bundle: Starts a callback HTTP server on :9090, then POST /api/v1/incidents with callback_url pointing back to itself.
  Phase 5 — Await Callback: Blocks until the incident agent's async callback arrives with the IncidentBundleResult payload, or times out after 120s.
```

Note in particular the phase 3, which sends a natural language
problem description, e.g.

 "Users are reporting errors connecting to the database.
  The connection_string is host=localhost... 
  Please investigate and report your findings."

 The DB agent then autonomously calls its tools (check_connection,
 get_active_connections, get_database_stats, etc.), analyzes the results,
 and returns a diagnostic summary. The SRE bot prints this diagnosis.

 The flag `-symptom` is designed to override the default symptom
 description sent to the AI agent
 (default: "Users are reporting database connectivity issues").


## Sample Run #1: ask aiHelpDesk for a Database status report

```
[boris@ ~/helpdesk]$ curl -s http://localhost:8080/api/v1/query -d '{"agent":"database","message":"Check the database health. The connection_string is `host=localhost port=15432 dbname=testdb user=postgres password=testpass`."}'| jq -r '.artifacts[0].parts'
I'll check the database health by starting with a connection test, then gathering key health metrics.
Great! The connection is successful. Now let me gather detailed health metrics.
## Database Health Check Summary ✅

**Connection:** ✅ Successful
- PostgreSQL 16.11 (Debian) on aarch64
- Server: 172.18.0.2:5432
- Database: testdb

---

### 📊 Database Status

| Metric | Value | Status |
|--------|-------|--------|
| **Database Size** | 7636 kB | Healthy |
| **In Recovery** | No | ✅ Ready |
| **Role** | Primary | ✅ Normal |
| **Encoding** | UTF8 | ✅ Standard |

---

### 🔌 Connection Health

| Metric | Value | Status |
|--------|-------|--------|
| **Total Connections** | 6 | ✅ Low (Max: 20) |
| **Active Connections** | 0 | ✅ Idle |
| **Idle in Transaction** | 0 | ✅ None |
| **Waiting on Locks** | 0 | ✅ No contention |
| **Blocking Locks** | None | ✅ Clear |

---

### 📈 Performance Metrics (testdb)

| Metric | Value | Status |
|--------|-------|--------|
| **Cache Hit Ratio** | 99.89% | ✅ Excellent |
| **Commits** | 1,343 | ✅ Normal |
| **Rollbacks** | 1 | ✅ Minimal |
| **Deadlocks** | 0 | ✅ None |
| **Conflicts** | 0 | ✅ None |
| **Rows Inserted** | 100,227 | ✅ Active usage |
| **Rows Deleted** | 90,086 | ✅ Active usage |

---

### ✅ Overall Assessment

**Your database is in EXCELLENT HEALTH:**
- No connection issues or lock contention
- Outstanding cache hit ratio (99.89%)
- No deadlocks or conflicts detected
- Low connection count with plenty of headroom
- Database properly configured and not in recovery mode
- Active DML operations (inserts, deletes) with minimal rollbacks

**No action required** — this database is running optimally!
```

## Sample Run #2: ask aiHelpDesk to troubleshoot

First, simulate a "too many clients" PostgreSQL error via
the iHelpDesk built-in Failure Testing Injection Framework:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest inject --id db-max-connections --conn "host=localhost port=15432 dbname=testdb user=postgres password=testpass"
time=2026-01-30T23:09:50.211-05:00 level=INFO msg="injecting failure" id=db-max-connections type=docker_exec
Failure injected: Max connections exhausted

Suggested prompt for the agent:
Users are getting "too many clients" errors connecting to the database. The connection_string is `host=localhost port=15432 dbname=testdb user=postgres password=testpass` — use it verbatim for all tool calls. Please investigate.


To tear down: faulttest teardown --id db-max-connections [same flags]
```

Confirm that indeed no new connections can be established:

```
[boris@ ~/helpdesk]$ PGPASSWORD=testpass psql -h localhost -p 15432 -d testdb -U postgres -c "SELECT 1"
psql: error: connection to server at "localhost" (::1), port 15432 failed: FATAL:  sorry, too many clients already
```

Finally, call the SRE bot asking the aiHelpDesk to explain
what's going on:

```
[boris@ ~/helpdesk]$ go run ./cmd/srebot/ -gateway http://localhost:8080 -conn "host=localhost port=15432 dbname=testdb user=postgres password=testpass" -symptom "Why am I getting 'too many clients' errors?"
[23:11:09] ── Phase 1: Agent Discovery ──────────────────────────
[23:11:09] GET /api/v1/agents
[23:11:09] Found 3 agents: incident_agent, postgres_database_agent, k8s_agent

[23:11:09] ── Phase 2: Health Check ─────────────────────────────
[23:11:09] POST /api/v1/db/check_connection
[23:11:12] Anomaly detected: "...---
ERROR — check_connection failed for host=lo..."

[23:11:12] ── Phase 3: AI Diagnosis ─────────────────────────────
[23:11:12] POST /api/v1/query  agent=database
[23:11:12] Prompt: "Why am I getting 'too many clients' errors? The connection_string is `host=localhost port=15432 dbname=testdb user=postg..."
[23:11:21] Agent response (2365 chars):
           ┌──────────────────────────────────────────────────────────────────────┐
           │ I'll investigate the "too many clients" error for you. Let me start  │
           │ by checking the connection and then examining your database          │
           │ configuration and connection statistics.                             │
           │ ---                                                                  │
           │ ERROR — check_connection failed for host=localhost port=15432      │
           │ dbname=testdb user=postgres password=testpass                        │
           │                                                                      │
           │ psql: error: connection to server at "localhost" (::1), port 15432   │
           │ failed: FATAL: sorry, too many clients already                       │
           │                                                                      │
           │ This means: The database has reached its maximum allowed concurrent  │
           │ connections and is rejecting new connection attempts.                │
           │ ---                                                                  │
           │                                                                      │
           │ ## Key Finding                                                       │
           │                                                                      │
           │ Your database is currently at or over its `max_connections` limit    │
           │ and cannot accept any new connections. This is a critical issue      │
           │ that prevents any further investigation using standard database      │
           │ queries.                                                             │
           │                                                                      │
           │ ## What's Happening                                                  │
           │                                                                      │
           │ The "too many clients" error occurs when the number of active        │
           │ connections reaches the `max_connections` configuration parameter.   │
           │ At this point, PostgreSQL rejects any new connection attempts,       │
           │ including diagnostic queries.                                        │
           │                                                                      │
           │ ## What You Need to Do                                               │
           │                                                                      │
           │ **Immediate Actions:**                                               │
           │ 1. **Identify and terminate idle/stuck connections** — You'll      │
           │ need to access the PostgreSQL server directly (via console, SSH, or  │
           │ a connection pool that still has available slots)                    │
           │ 2. **Check system processes** — Use `ps aux | grep postgres` on    │
           │ the server to see active PostgreSQL processes                        │
           │ 3. **Review connection pooler settings** — If using pgBouncer or   │
           │ PgPool-II, check for connection leaks or misconfiguration            │
           │ 4. **Check application connection handling** — Review your         │
           │ applications for:                                                    │
           │    - Connections not being returned to the pool                      │
           │    - Unhandled exceptions causing connection cleanup to be skipped   │
           │    - Long-running transactions holding connections open              │
           │                                                                      │
           │ **To resolve this locally:**                                         │
           │ - Connect directly to the PostgreSQL server console if possible      │
           │ - Run: `SELECT usename, application_name, state, query_start FROM    │
           │ pg_stat_activity ORDER BY query_start;`                              │
           │ - Terminate problematic connections with `SELECT                     │
           │ pg_terminate_backend(pid);`                                          │
           │                                                                      │
           │ **Long-term fixes:**                                                 │
           │ - Increase `max_connections` if your workload genuinely requires it  │
           │ (with adequate memory available)                                     │
           │ - Implement a connection pool (pgBouncer) to limit and manage        │
           │ connections efficiently                                              │
           │ - Implement connection timeouts to automatically close idle          │
           │ connections                                                          │
           │                                                                      │
           │ Would you like guidance on how to safely increase `max_connections`  │
           │ or implement connection pooling?                                     │
           └──────────────────────────────────────────────────────────────────────┘

[23:11:21] ── Phase 4: Create Incident Bundle ───────────────────
[23:11:21] POST /api/v1/incidents
[23:11:21]   infra_key:    srebot-demo
[23:11:21]   callback_url: http://192.168.1.151:9090/callback
[23:11:27] Incident agent responded (744 chars)

[23:11:27] ── Phase 5: Awaiting Callback ────────────────────────
[23:11:27] Listening on :9090 for POST /callback ...
[23:11:27] Callback received!
[23:11:27]   incident_id: d19a6ce8
[23:11:27]   bundle_path: /tmp/incident-d19a6ce8-20260130-231123.tar.gz
[23:11:27]   layers:      [database, os, storage]
[23:11:27]   errors:      16
[23:11:27]     - database/version.txt: psql failed: exit status 2
Output: psql: error: connection to server at "localhost" (::1), port 15432 failed: FATAL:  sorry, too many clients already
[23:11:27]     - database/databases.txt: psql failed: exit status 2
Output: psql: error: connection to server at "localhost" (::1), port 15432 failed: FATAL:  sorry, too many clients already
[23:11:27]     - database/active_connections.txt: psql failed: exit status 2
Output: psql: error: connection to server at "localhost" (::1), port 15432 failed: FATAL:  sorry, too many clients already
[23:11:27]     - database/connection_stats.txt: psql failed: exit status 2
Output: psql: error: connection to server at "localhost" (::1), port 15432 failed: FATAL:  sorry, too many clients already
[23:11:27]     - database/database_stats.txt: psql failed: exit status 2
Output: psql: error: connection to server at "localhost" (::1), port 15432 failed: FATAL:  sorry, too many clients already
[23:11:27]     - database/config_params.txt: psql failed: exit status 2
Output: psql: error: connection to server at "localhost" (::1), port 15432 failed: FATAL:  sorry, too many clients already
[23:11:27]     - database/replication_status.txt: psql failed: exit status 2
Output: psql: error: connection to server at "localhost" (::1), port 15432 failed: FATAL:  sorry, too many clients already
[23:11:27]     - database/locks.txt: psql failed: exit status 2
Output: psql: error: connection to server at "localhost" (::1), port 15432 failed: FATAL:  sorry, too many clients already
[23:11:27]     - database/table_stats.txt: psql failed: exit status 2
Output: psql: error: connection to server at "localhost" (::1), port 15432 failed: FATAL:  sorry, too many clients already
[23:11:27]     - os/top.txt: top failed: exit status 1
Output: invalid option or syntax: -b
top usage: top
		[-a | -d | -e | -c <mode>]
		[-F | -f]
		[-h]
		[-i <interval>]
		[-l <samples>]
		[-ncols <columns>]
		[-o <key>] [-O <secondaryKey>]
			keys: pid (default), command, cpu, cpu_me, cpu_others, csw,
				time, threads, ports, mregion, mem, rprvt, purg, vsize, vprvt,
				kprvt, kshrd, pgrp, ppid, state, uid, wq, faults, cow, user,
				msgsent, msgrecv, sysbsd, sysmach, pageins, boosts, instrs, cycles
		[-R | -r]
		[-S]
		[-s <delay>]
		[-n <nprocs>]
		[-stats <key(s)>]
		[-pid <processid>]
		[-user <username>]
		[-U <username>]
		[-u]
		[-W]
[23:11:27]     - os/ps.txt: ps failed: exit status 1
Output: ps: illegal option -- -
usage: ps [-AaCcEefhjlMmrSTvwXx] [-O fmt | -o fmt] [-G gid[,gid...]]
          [-u]
          [-p pid[,pid...]] [-t tty[,tty...]] [-U user[,user...]]
       ps [-L]
[23:11:27]     - os/free.txt: free failed: exec: "free": executable file not found in $PATH
Output: (no output)
[23:11:27]     - os/vmstat.txt: vmstat failed: exec: "vmstat": executable file not found in $PATH
Output: (no output)
[23:11:27]     - os/dmesg.txt: dmesg failed: exit status 1
Output: usage: sudo dmesg
[23:11:27]     - storage/lsblk.txt: lsblk failed: exec: "lsblk": executable file not found in $PATH
Output: (no output)
[23:11:27]     - storage/iostat.txt: iostat failed: exit status 1
Output: iostat: illegal option -- x
usage: iostat [-CUdIKoT?] [-c count] [-n devs]
	      [-w wait] [drives]
[23:11:27] Done.
```
