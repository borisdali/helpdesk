# aiHelpDesk Fleet Management: Ad-Hoc job run sample

Fleet Management module is documented [here](FLEET.md).
Platform specific instructions are also available for running aiHelpDesk jobs directly on a [host/VM](../deploy/host/README.md#710-running-the-fleet-runner-fleet-runner), in [Docker containers](../deploy/docker-compose/README.md#38-running-the-fleet-runner-fleet-runner) and on [K8s](../deploy/helm/README.md#99-running-the-jobs-on-multiple-databases-via-fleet-managements-fleet-runner).

aiHelpDesk supports both scheduled as well as the ad-hoc jobs. The sample of creating and running the latter is presented below. In this example the job is created via a NL request through the aiHelpDesk client tool. It can be used as is for testing, but for production use we recommend taking it as a template, customizing it as you see fit, testing it on the lower tier environments, going through the normal peer review process and checking into a version control system before running it access your database fleet.

Sample run:

```
[boris@ /tmp/helpdesk/helpdesk-v0.7.0-deploy/]$ /tmp/helpdesk-client --user alice@example.com --plan-fleet-job "check status, uptime and load on all development databases excluding fault-test-db"
=== Fleet Job Plan ===

Planner notes:
  This job performs read-only health checks on all development-tagged servers except fault-test-db. It collects server status summary (version, uptime, cache hit ratio), detailed server info (start time, role, data directory), and connection statistics (total/active/idle connections and load per database). These three tools together provide comprehensive status, uptime, and load visibility. Targets: alloydb-on-vm and pg-cluster-minkube. Excluded: fault-test-db per explicit request.

Excluded (sensitivity): fault-test-db, pg-cluster-minkube2
WARNING: pg-cluster-minkube2 excluded due to [RESTRICTED] designation — contains sensitive data (pii, internal)


Job file written: check-status-uptime-and-load-on-development-databases.json

To submit: fleet-runner --job-file Check status, uptime and load on development databases.json
```

The generated JSON file can be inspected, adapted to your needs and checked in. In this example we just run it "as is": 

```
[boris@ /tmp/helpdesk/helpdesk-v0.7.0-deploy]$ cat helm/helpdesk/check-status-uptime-and-load-on-development-databases.json
{
  "name": "Check status, uptime and load on development databases",
  "change": {
    "steps": [
      {
        "agent": "database",
        "tool": "get_status_summary",
        "on_failure": "continue"
      },
      {
        "agent": "database",
        "tool": "get_server_info",
        "on_failure": "continue"
      },
      {
        "agent": "database",
        "tool": "get_connection_stats",
        "on_failure": "continue"
      }
    ]
  },
  "targets": {
    "tags": [
      "development"
    ],
    "exclude": [
      "fault-test-db"
    ]
  },
  "strategy": {
    "canary_count": 1,
    "wave_size": 0,
    "wave_pause_seconds": 0,
    "failure_threshold": 0.5,
    "dry_run": false,
    "count_partial_as_success": false
  },
  "plan_trace_id": "plan_faf7ab5f-928"
}
```

Since the generated JSON file resides locally, it needs to be uploaded to a ConfigMap, a one-off Job needs to be created with the job definition file mounted for the fleet-runner to pick it up. That's the job of the [`run-fleet-job.sh`](../scripts/README.md#run-fleet-jobsh) helper script:

```
[boris@ /tmp/helpdesk/helpdesk-v0.7.0-deploy]$ ./scripts/run-fleet-job.sh --api-key $(cat helm/helpdesk/.helpdesk-fleet-api-key) check-status-uptime-and-load-on-development-databases.json
[fleet-run] Detecting cluster configuration (namespace=helpdesk-system, release=helpdesk)...
[fleet-run] Using image: ghcr.io/borisdali/helpdesk:v0.7.0-8540626
[fleet-run] Creating job ConfigMap fleet-adhoc-job-1774386466...
configmap/fleet-adhoc-job-1774386466 created
[fleet-run] Creating Job fleet-adhoc-1774386466...
job.batch/fleet-adhoc-1774386466 created
[fleet-run] Waiting for pod to start...
[fleet-run] Streaming logs from pod fleet-adhoc-1774386466-cg868...
────────────────────────────────────────────────────────────────────────────
time=2026-03-24T21:07:46.722Z level=INFO msg="preflight check" server=alloydb-on-vm
time=2026-03-24T21:07:46.839Z level=INFO msg="preflight ok" server=alloydb-on-vm
time=2026-03-24T21:07:46.839Z level=INFO msg="preflight check" server=pg-cluster-minkube
time=2026-03-24T21:07:47.032Z level=INFO msg="preflight ok" server=pg-cluster-minkube
time=2026-03-24T21:07:47.088Z level=INFO msg="fleet job created" job_id=flj_74ced056
time=2026-03-24T21:07:47.088Z level=INFO msg="fleet: starting canary phase" job_id=flj_74ced056 servers=[alloydb-on-vm]
time=2026-03-24T21:07:47.335Z level=INFO msg="fleet: canary server ok" job_id=flj_74ced056 server=alloydb-on-vm
time=2026-03-24T21:07:47.335Z level=INFO msg="fleet: starting wave phase" job_id=flj_74ced056 waves=1
time=2026-03-24T21:07:47.335Z level=INFO msg="fleet: starting wave" job_id=flj_74ced056 wave=wave-1 servers=1
time=2026-03-24T21:07:47.623Z level=INFO msg="fleet: server ok" job_id=flj_74ced056 wave=wave-1 server=pg-cluster-minkube
time=2026-03-24T21:07:47.625Z level=INFO msg="fleet job completed" job_id=flj_74ced056 servers=2
────────────────────────────────────────────────────────────────────────────

════════════════════════════════════════════════════════════════════════════
  Fleet job results: flj_74ced056
════════════════════════════════════════════════════════════════════════════

SERVER              STATUS  VERSION  UPTIME   CONN    CACHE HIT%
──────              ──────  ───────  ──────   ────    ──────────
alloydb-on-vm       ok      PG 16.3  15d 21h  10/100  99.98
pg-cluster-minkube  ok      PG 16.2  15d 21h  8/100   99.99

[fleet-run] Fleet job completed successfully.
[fleet-run] Cleaning up...
```


The job execution can be tracked through the Fleet Management as well as the Audit and Journey modules. In particular, the aiHelpDesk Journey is the good starting point for forensic analysis:

```
[boris@ /tmp/helpdesk/helpdesk-v0.7.0-deploy]$ curl -s 'http://localhost:1199/v1/journeys?trace_id=tr_flj_74ced056'|jq .
[
  {
    "trace_id": "tr_flj_74ced056",
    "started_at": "2026-03-24T21:07:47.075223668Z",
    "ended_at": "2026-03-24T21:07:47.615499044Z",
    "duration_ms": 540,
    "user_id": "fleet-runner",
    "user_query": "fleet job: Check status, uptime and load on development databases",
    "purpose": "fleet_rollout",
    "purpose_note": "job_id=flj_74ced056 server=alloydb-on-vm stage=canary",
    "agent": "postgres_database_agent",
    "tools_used": [
      "get_status_summary",
      "get_status_summary",
    ],
    "outcome": "success",
    "event_count": 8,
    "origin": "direct_tool"
  }
]
```

The details of all 8 events summarized in a Journey can be further obtained from the Audit, but given that the operation was a Job, we can also interrogate the Fleet Management module directly:

```
[boris@ /tmp/helpdesk/helpdesk-v0.7.0-deploy]$ curl -s http://localhost:8080/api/v1/fleet/jobs/flj_74ced056 -H "X-User: alice@example.com"|jq
{
  "job_id": "flj_74ced056",
  "name": "Check status, uptime and load on development databases",
  "submitted_by": "fleet-runner",
  "submitted_at": "2026-03-24T21:07:47.032255752Z",
  "status": "completed",
  "job_def": "{\"name\":\"Check status, uptime and load on development databases\",\"change\":{\"steps\":[{\"agent\":\"database\",\"tool\":\"get_status_summary\",\"on_failure\":\"continue\"},{\"agent\":\"database\",\"tool\":\"get_server_info\",\"on_failure\":\"continue\"},{\"agent\":\"database\",\"tool\":\"get_connection_stats\",\"on_failure\":\"continue\"}]},\"targets\":{\"tags\":[\"development\"],\"exclude\":[\"fault-test-db\"]},\"strategy\":{\"canary_count\":1,\"wave_size\":0,\"wave_pause_seconds\":0,\"failure_threshold\":0.5,\"dry_run\":false,\"count_partial_as_success\":false},\"plan_trace_id\":\"plan_faf7ab5f-928\"}",
  "summary": "Applied 3 step(s) to 2 server(s).",
  "plan_trace_id": "plan_faf7ab5f-928",
  "created_at": "2026-03-24T21:07:47.072985502Z",
  "updated_at": "2026-03-24T21:07:47.624053835Z"
}
```

A single ad-hoc job with two "servers":

```
[boris@ /tmp/helpdesk/helpdesk-v0.7.0-deploy]$ curl -s http://localhost:8080/api/v1/fleet/jobs/flj_74ced056/servers -H "X-User: alice@example.com"|jq
[
  {
    "id": 4,
    "job_id": "flj_74ced056",
    "server_name": "alloydb-on-vm",
    "stage": "canary",
    "status": "success",
    "output": "-[ RECORD 1 ]-------+---------\ndatabase            | \ntotal_connections   | 9\nactive              | 0\nidle                | 0\nidle_in_transaction | 0\nwaiting_on_lock     | 0\nmax_connections     | 100\n-[ RECORD 2 ]-------+---------\ndatabase            | postgres\ntotal_connections   | 1\nactive              | 1\nidle                | 0\nidle_in_transaction | 0\nwaiting_on_lock     | 0\nmax_connections     | 100\n\n",
    "started_at": "0001-01-01T00:00:00Z",
    "finished_at": "2026-03-24T21:07:47.333837085Z"
  },
  {
    "id": 5,
    "job_id": "flj_74ced056",
    "server_name": "pg-cluster-minkube",
    "stage": "wave-1",
    "status": "success",
    "output": "-[ RECORD 1 ]-------+---------\ndatabase            | \ntotal_connections   | 6\nactive              | 0\nidle                | 0\nidle_in_transaction | 0\nwaiting_on_lock     | 0\nmax_connections     | 100\n-[ RECORD 2 ]-------+---------\ndatabase            | postgres\ntotal_connections   | 1\nactive              | 0\nidle                | 0\nidle_in_transaction | 0\nwaiting_on_lock     | 0\nmax_connections     | 100\n-[ RECORD 3 ]-------+---------\ndatabase            | app\ntotal_connections   | 1\nactive              | 1\nidle                | 0\nidle_in_transaction | 0\nwaiting_on_lock     | 0\nmax_connections     | 100\n\n",
    "started_at": "0001-01-01T00:00:00Z",
    "finished_at": "2026-03-24T21:07:47.622503669Z"
  }
]

```

... and the "steps" for the first server:

```
[boris@cassiopeia ~/cassiopeia/minikube]$ curl -s http://localhost:8080/api/v1/fleet/jobs/flj_74ced056/servers/alloydb-on-vm/steps -H "X-User: alice@example.com" |jq .
[
  {
    "id": 8,
    "job_id": "flj_74ced056",
    "server_name": "alloydb-on-vm",
    "step_index": 0,
    "tool": "get_status_summary",
    "status": "success",
    "output": "{\"status\" : \"ok\", \"version\" : \"PG 16.3\", \"uptime\" : \"15d 21h\", \"connections\" : 10, \"max_connections\" : 100, \"cache_hit_ratio\" : 99.98}",
    "started_at": "0001-01-01T00:00:00Z",
    "finished_at": "0001-01-01T00:00:00Z"
  },
  {
    "id": 9,
    "job_id": "flj_74ced056",
    "server_name": "alloydb-on-vm",
    "step_index": 1,
    "tool": "get_server_info",
    "status": "success",
    "output": "-[ RECORD 1 ]------+-----------------------------------------------------\nversion            | PostgreSQL 16.3 on aarch64-unknown-linux-gnu, 64-bit\nserver_started     | 2026-03-08 23:50:28.470494+00\nuptime             | 15 days 21:17:18.772603\ndata_directory     | /var/lib/postgresql/data\nconfig_file        | /var/lib/postgresql/data/postgresql.conf\ncurrent_db_size    | 13 MB\nrole               | primary\ntotal_connections  | 10\nactive_connections | 1\nmax_connections    | 100\n\n",
    "started_at": "0001-01-01T00:00:00Z",
    "finished_at": "0001-01-01T00:00:00Z"
  },
  {
    "id": 10,
    "job_id": "flj_74ced056",
    "server_name": "alloydb-on-vm",
    "step_index": 2,
    "tool": "get_connection_stats",
    "status": "success",
    "output": "-[ RECORD 1 ]-------+---------\ndatabase            | \ntotal_connections   | 9\nactive              | 0\nidle                | 0\nidle_in_transaction | 0\nwaiting_on_lock     | 0\nmax_connections     | 100\n-[ RECORD 2 ]-------+---------\ndatabase            | postgres\ntotal_connections   | 1\nactive              | 1\nidle                | 0\nidle_in_transaction | 0\nwaiting_on_lock     | 0\nmax_connections     | 100\n\n",
    "started_at": "0001-01-01T00:00:00Z",
    "finished_at": "0001-01-01T00:00:00Z"
  }
]
```

