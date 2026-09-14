# aiHelpDesk Sample#21 (from the source): SQL forensics

The raw transcript of the sample commands and deliberations presented below complements this blog post:

- **[The Corruption That Wasn't There](https://itnext.io/the-corruption-that-wasnt-there-138085295c1b)**  
  A forensics walkthrough of aiHelpDesk's fourth fabrication detection layer, told through the real SQL queries that caught it

If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel). 

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This one happened to be running on Docker, but see [here](SAMPLE010.md), [here](SAMPLE011.md) and [here](SAMPLE017.md) for VM/Bare Metal, the source and K8s respectively (although not the exact commands shown on this page).

---

This a sample transcript of the SQLite forensics for the case of a bogus model's [claim of a corruption](https://itnext.io/the-corruption-that-wasnt-there-138085295c1b?postPublishedType=repub): 

> "FATAL:  terminating walreceiver due to timeout" and  
>         "invalid record length at 0/B000000: expected at least 24, got 0" 

See the blog post linked above for details of that story, but here are the SQL queries to investigate it (and refute the model's claim):


```
[boris@ ~/helpdesk]$ ll /tmp/helpdesk/audit.db
-rw-r--r--@ 1 boris  wheel  536576 Sep 10 20:46 /tmp/helpdesk/audit.db

[boris@ ~/helpdesk]$ which sqlite3
/usr/bin/sqlite3

[boris@ ~/helpdesk]$ sqlite3 -version
3.51.0 2025-06-12 13:14:41 f0ca7bba1c5e232e5d279fad6338121ab55af0c8c68c84cdfb18ba5114dcaapl (64-bit)

[boris@ ~/helpdesk]$ sqlite3 /tmp/helpdesk/audit.db ".tables"
approval_requests             govbot_runs
approval_sessions             playbook_run_steps
audit_events                  playbook_runs
fault_stability_cert          playbooks
fault_stability_cert_history  rollback_records
fleet_job_server_steps        run_evaluation
fleet_job_servers             run_feedback
fleet_jobs                    tool_results
fleet_rollback_records        uploads
```

Now, `audit_events` is the primary table holding the audit trail and `playbook_runs` is the key table to query for the real and fault-injected incidents that are viewed through the prism of playbook runs.

```
[boris@ ~/helpdesk]$ sqlite3 /tmp/helpdesk/audit.db ".schema audit_events"
CREATE TABLE audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT UNIQUE NOT NULL,
		timestamp TEXT NOT NULL,
		event_type TEXT NOT NULL,
		trace_id TEXT,
		parent_id TEXT,
		action_class TEXT,
		prev_hash TEXT,
		event_hash TEXT,
		session_id TEXT NOT NULL,
		session_agent TEXT,
		user_id TEXT,
		user_query TEXT,
		purpose TEXT,
		purpose_note TEXT,
		origin TEXT,
		tool_name TEXT,
		tool_json TEXT,
		approval_status TEXT,
		approval_json TEXT,
		decision_agent TEXT,
		decision_category TEXT,
		decision_confidence REAL,
		decision_json TEXT,
		outcome_status TEXT,
		outcome_error TEXT,
		outcome_duration_ms INTEGER,
		raw_json TEXT NOT NULL,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP
	);
CREATE INDEX idx_events_timestamp ON audit_events(timestamp);
CREATE INDEX idx_events_session ON audit_events(session_id);
CREATE INDEX idx_events_type ON audit_events(event_type);
CREATE INDEX idx_events_agent ON audit_events(decision_agent);
CREATE INDEX idx_events_trace ON audit_events(trace_id);
CREATE INDEX idx_events_parent ON audit_events(parent_id);
CREATE INDEX idx_events_action_class ON audit_events(action_class);
CREATE INDEX idx_events_tool ON audit_events(tool_name);
CREATE INDEX idx_events_approval ON audit_events(approval_status);

[boris@ ~/helpdesk]$ sqlite3 /tmp/helpdesk/audit.db ".schema playbook_runs"
CREATE TABLE playbook_runs (
    run_id             TEXT     NOT NULL PRIMARY KEY,
    playbook_id        TEXT     NOT NULL,
    series_id          TEXT     NOT NULL,
    execution_mode     TEXT     NOT NULL DEFAULT 'fleet',
    outcome            TEXT     NOT NULL DEFAULT 'unknown',
    escalated_to       TEXT     NOT NULL DEFAULT '',
    findings_summary   TEXT     NOT NULL DEFAULT '',
    diagnostic_report  TEXT     NOT NULL DEFAULT '',
    context_id         TEXT     NOT NULL DEFAULT '',
    operator           TEXT     NOT NULL DEFAULT '',
    started_at         DATETIME NOT NULL,
    completed_at       DATETIME NOT NULL DEFAULT ''
, transitioned_to TEXT NOT NULL DEFAULT '', connection_string TEXT NOT NULL DEFAULT '', trace_id TEXT NOT NULL DEFAULT '', agent_transcript TEXT NOT NULL DEFAULT '', prior_run_id TEXT NOT NULL DEFAULT '', trigger_context TEXT NOT NULL DEFAULT '', namespace TEXT NOT NULL DEFAULT '', purpose TEXT NOT NULL DEFAULT '', saw_signal_line INTEGER NOT NULL DEFAULT 0, gate_reason TEXT NOT NULL DEFAULT '');
CREATE INDEX idx_playbook_runs_series_time
    ON playbook_runs(series_id, started_at);
CREATE INDEX idx_playbook_runs_playbook
    ON playbook_runs(playbook_id);
CREATE INDEX idx_playbook_runs_trace
    ON playbook_runs(trace_id);
CREATE INDEX idx_playbook_runs_prior
    ON playbook_runs(prior_run_id);
```

Here's a sample investigation following the failure injection test that led to `⚠  UNVERIFIED EVIDENCE`:

```
  Run 2/3
time=2026-09-08T09:34:41.992-04:00 level=INFO msg="executing injection spec" type=sql phase=inject
time=2026-09-08T09:34:43.345-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-replica-disconnected series_id=pbs_replication_lag playbook_id=pb_ab6059a6 gateway=http://localhost:8080 agent-conn="host=localhost           port=15432 dbname=testdb user=postgres password=testpass"
  ⚠  UNVERIFIED EVIDENCE (primary, non-blocking): 1 quote(s) did not match any real tool output
         Replica has disconnected from primary; inactive replication slot is retaining WAL on primary — (slot
  Feedback submitted (triage/post_incident run_id=plr_aea03a63)
time=2026-09-08T09:35:27.952-04:00 level=INFO msg="executing injection spec" type=sql phase=teardown
  [PASS] score=100%
         [PRIMARY 95%] Replica has disconnected from primary; inactive replication slot is retaining WAL on primary
         [95%] Primary server's pg_hba.conf rejects replication connections from the replica's host without SSL encryption
         [REJECTED 5%] Replica connection dropped due to network transient that has not recovered — No active connection in pg_stat_replication means the connection is not present at all, not merely lagging; this is a complete disconnection, not a network transient.
         [REJECTED 0%] Replica container crashed or was forcibly terminated — Container is running with Status=running, exitcode=0, oomkilled=false; the process did not crash.
```

Note how the actual reason for unverified evidence is shown right below it on the next line. In this case it was a replica presumably disconnected from the primary with the replication slot that is still active... and it's used for the base of the primary model's hypothesis for what's going on here... except that this evidence is not mentioned anywhere in the tool's response. Which tools? Let's find out:

```
[boris@ ~/helpdesk]$ sqlite3 /tmp/helpdesk/audit.db "SELECT run_id, trace_id, started_at, completed_at FROM playbook_runs WHERE run_id IN ('plr_25737a2d','plr_5d7df113');" 
plr_25737a2d|faulttest-b1e52bf4-db-replica-disconnected-r2|2026-09-08 04:51:42|2026-09-08 04:52:19
plr_5d7df113|faulttest-b1e52bf4-db-replica-container-stopped-r3|2026-09-08 04:55:20|2026-09-08 04:56:31
```

Now let's pull the real `tool_execution` results for these two traces to see exactly what the raw tool output was:

```
[boris@ ~/helpdesk]$ sqlite3 -json /tmp/helpdesk/audit.db "SELECT timestamp, tool_name, tool_json FROM audit_events WHERE trace_id='faulttest-b1e52bf4-db-replica-disconnected-r2' AND event_type='tool_execution' ORDER BY timestamp;" 
[{"timestamp":"2026-09-08T04:51:44.910193000Z","tool_name":"check_connection","tool_json":"{\"name\":\"check_connection\",\"agent\":\"postgres_database_agent\",\"parameters\":{\"connection_string\":\"host=localhost port=15432 dbname
=testdb user=postgres password=***\"},\"raw_command\":\"SELECT version(), current_database(), current_user, inet_server_addr(), inet_server_port();\",\"result\":\"-[ RECORD 1 ]----+---------------------------------------------------
------------------------------------------------------------------------\\nversion          | PostgreSQL 16.14 (Debian 16.14-1.pgdg13+1) on aarch64-unknown-linux-gnu, compiled by gcc (Debian 14.2.0-19) 14.2.0, 64-bit\\ncurrent_datab

[boris@ ~/helpdesk]$ sqlite3 -json /tmp/helpdesk/audit.db "SELECT timestamp, tool_name, tool_json FROM audit_events WHERE trace_id='faulttest-b1e52bf4-db-replica-container-stopped-r3' AND event_type='tool_execution' AND tool_name='get_host_logs' ORDER BY timestamp;" | python3 -c "
   import json,sys
   data = json.load(sys.stdin)
   for row in data:
       tj = json.loads(row['tool_json'])
       print('---', row['timestamp'], '---')
       print(tj.get('result','')[-3000:])
   "
--- 2026-09-08T04:55:51.225558000Z ---
410 UTC [2665] FATAL:  could not connect to the primary server: connection to server at "postgres" (172.18.0.2), port 5432 failed: FATAL:  pg_hba.conf rejects replication connection for host "172.18.0.4", user "postgres", no encryption

[boris@ ~/helpdesk]$ sqlite3 -json /tmp/helpdesk/audit.db "SELECT tool_json FROM audit_events WHERE trace_id='faulttest-b1e52bf4-db-replica-container-stopped-r3' AND event_type='tool_execution' AND tool_name='get_host_logs' ORDER BY timestamp;" |
   python3 -c "
   import json,sys
   data = json.load(sys.stdin)
   for row in data:
       tj = json.loads(row['tool_json'])
       r = tj.get('result','')
       print(len(r), 'chars')
       print('contains shutdown:', 'database system is shut down' in r)
       print('contains fast shutdown:', 'received fast shutdown request' in r)
   "
8195 chars
contains shutdown: False
contains fast shutdown: False

[boris@ ~/helpdesk]$ sqlite3 -json /tmp/helpdesk/audit.db "SELECT run_id, playbook_id, series_id, started_at, completed_at, prior_run_id FROM playbook_runs WHERE trace_id='faulttest-b1e52bf4-db-replica-container-stopped-r3' ORDER BY started_at;" 
[{"run_id":"plr_5d7df113","playbook_id":"pb_ab6059a6","series_id":"pbs_replication_lag","started_at":"2026-09-08 04:55:20","completed_at":"2026-09-08 04:56:31","prior_run_id":""},
{"run_id":"plr_a70e9607","playbook_id":"pb_82ec005d","series_id":"pbs_sysadmin_replica_connectivity_triage","started_at":"2026-09-08 04:55:45","completed_at":"2026-09-08 04:56:02","prior_run_id":"plr_5d7df113"},
{"run_id":"plr_7646a96e","playbook_id":"pb_e6c0f0be","series_id":"pbs_replica_restart_action","started_at":"2026-09-08 04:56:03","completed_at":"2026-09-08 04:56:30","prior_run_id":"plr_a70e9607"}]

[boris@ ~/helpdesk]$ sqlite3 -json /tmp/helpdesk/audit.db "SELECT timestamp, tool_name FROM audit_events WHERE trace_id='faulttest-b1e52bf4-db-replica-container-stopped-r3' AND event_type='tool_execution' AND timestamp >= '2026-09-08T04:56:03' ORDER BY timestamp;" 
[{"timestamp":"2026-09-08T04:56:16.089774000Z","tool_name":"restart_container"},
{"timestamp":"2026-09-08T04:56:18.997643000Z","tool_name":"check_host"},
{"timestamp":"2026-09-08T04:56:21.484318000Z","tool_name":"get_host_logs"}]

[boris@ ~/helpdesk]$ sqlite3 /tmp/helpdesk/audit.db ".schema tool_results" 
CREATE TABLE tool_results (
    result_id   TEXT     NOT NULL PRIMARY KEY,
    server_name TEXT     NOT NULL,
    tool_name   TEXT     NOT NULL,
    tool_args   TEXT     NOT NULL DEFAULT '{}',
    output      TEXT     NOT NULL DEFAULT '',
    trace_id    TEXT     NOT NULL DEFAULT '',
    job_id      TEXT     NOT NULL DEFAULT '',
    recorded_by TEXT     NOT NULL DEFAULT '',
    recorded_at DATETIME NOT NULL,
    success     INTEGER  NOT NULL DEFAULT 1
);
CREATE INDEX idx_tool_results_server_tool_time
    ON tool_results(server_name, tool_name, recorded_at);
CREATE INDEX idx_tool_results_job
    ON tool_results(job_id);

[boris@ ~/helpdesk]$ sqlite3 /tmp/helpdesk/audit.db "SELECT tool_json FROM audit_events WHERE trace_id='faulttest-b1e52bf4-db-replica-disconnected-r2' AND event_type='tool_execution' AND tool_name='get_host_logs';" | python3 -c "
   import json,sys
   tj = json.loads(sys.stdin.read())
   r = tj.get('result','')
   import re
   m = re.search(r'FATAL:\s+pg_hba.conf rejects.*', r)
   print(m.group(0) if m else 'not found')
   "
=== Real log line (run2, db-replica-disconnected) ===
FATAL:  pg_hba.conf rejects replication connection for host "172.18.0.4", user "postgres", no encryption
```

OK, so the real log line quotes the host/user (host "172.18.0.4", user "postgres"), but the model's EVIDENCE dropped those inner quote marks (host 172.18.0.4, user postgres). Well, that's really minor infraction, barely worth mentioning compared to total and blunt fabrications we've seen in the lab. Here, we are looking at the same content, just different citation punctuation. Huh, big deal. 

This particular (and a dozen similar) issue is fixed, but that's how you can investigate any model's claim vs. what's really appears in a tool's output.

