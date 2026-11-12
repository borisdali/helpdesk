# aiHelpDesk Sample#31 (all): What a restore would cost, in downtime and in data, before anyone approves it (availability budget, ledger, recovery-time estimate)

This sample is an expanded version of [Sample#30](SAMPLE030.md). If that sample transcript peaked your interest, you may appreciate this expanded version because it provides details on all three supported platforms: on Docker, host/VM and K8s:


## Docker:

The setup:

```
[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec alloydb-omni sh -c 'printf "[global]\nrepo1-path=/var/lib/pgbackrest\nrepo1-retention-full=2\n\n[main]\npg1-path=/var/lib/postgresql/data\n" > /etc/         pgbackrest.conf'

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec alloydb-omni sh -c 'ls -l /etc/pgbackrest.conf'
-rw-r--r-- 1 root root 105 Nov 12 13:49 /etc/pgbackrest.conf

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec alloydb-omni sh -c 'cat /etc/pgbackrest.conf'
[global]
repo1-path=/var/lib/pgbackrest
repo1-retention-full=2

[main]
pg1-path=/var/lib/postgresql/data

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec alloydb-omni sh -c 'mkdir -p /var/lib/pgbackrest && chown postgres:postgres /var/lib/pgbackrest'
[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec alloydb-omni sh -c 'ls -ld /var/lib/pgbackrest'
drwxr-xr-x 2 postgres postgres 4096 Nov 12 13:50 /var/lib/pgbackrest

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec -u postgres alloydb-omni pgbackrest --stanza=main --log-level-console=info stanza-create
2026-11-12 13:51:26.821 P00   WARN: unable to open log file '/var/log/pgbackrest/main-stanza-create.log': No such file or directory
                                    NOTE: process will continue without log file.
2026-11-12 13:51:26.825 P00   INFO: stanza-create command begin 2.55.1: --exec-id=73545-4a3bddc3 --log-level-console=info --log-level-file=off --pg1-path=/var/lib/postgresql/data --repo1-path=/var/lib/pgbackrest --stanza=main
2026-11-12 13:51:26.953 P00   INFO: stanza-create for stanza 'main' on repo1
2026-11-12 13:51:26.978 P00   INFO: stanza-create command end: completed successfully (178ms)

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec alloydb-omni psql -U postgres -c "ALTER SYSTEM SET archive_mode = on" -c "ALTER SYSTEM SET archive_command = 'pgbackrest --stanza=main archive-push %p'"
ALTER SYSTEM
ALTER SYSTEM

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker restart alloydb-omni
alloydb-omni

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec alloydb-omni psql -U postgres -c "show archive_mode"
 archive_mode
--------------
 on
(1 row)

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec -u postgres alloydb-omni pgbackrest --stanza=main --log-level-console=info check
2026-11-12 13:53:10.104 P00   INFO: check command begin 2.55.1: --exec-id=75-b77e0554 --log-level-console=info --pg1-path=/var/lib/postgresql/data --repo1-path=/var/lib/pgbackrest --stanza=main
2026-11-12 13:53:10.152 P00   INFO: check repo1 configuration (primary)
2026-11-12 13:53:10.177 P00   INFO: check repo1 archive for WAL (primary)
2026-11-12 13:53:10.485 P00   INFO: WAL segment 000000010000000000000031 successfully archived to '/var/lib/pgbackrest/archive/main/17-1/0000000100000000/000000010000000000000031-01acdc1e3530ef4d6ea2c9483c18baee50d8c029.gz' on repo1
2026-11-12 13:53:10.485 P00   INFO: check command end: completed successfully (383ms)

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec -u postgres alloydb-omni pgbackrest --stanza=main --type=full --log-level-console=info backup
2026-11-12 13:53:22.475 P00   WARN: unable to open log file '/var/log/pgbackrest/main-backup.log': No such file or directory
                                    NOTE: process will continue without log file.
2026-11-12 13:53:22.475 P00   INFO: backup command begin 2.55.1: --exec-id=85-25219c4b --log-level-console=info --log-level-file=off --pg1-path=/var/lib/postgresql/data --repo1-path=/var/lib/pgbackrest --repo1-retention-full=2 --             stanza=main --type=full
2026-11-12 13:53:22.497 P00   INFO: execute non-exclusive backup start: backup begins after the next regular checkpoint completes
2026-11-12 13:55:35.134 P00   INFO: backup start archive = 000000010000000000000033, lsn = 0/33000028
2026-11-12 13:55:35.134 P00   INFO: check archive for prior segment 000000010000000000000032
2026-11-12 13:55:38.775 P00   INFO: execute non-exclusive backup stop and wait for all WAL segments to archive
2026-11-12 13:55:38.816 P00   INFO: backup stop archive = 000000010000000000000033, lsn = 0/33262980
2026-11-12 13:55:38.830 P00   INFO: check archive for segment(s) 000000010000000000000033:000000010000000000000033
2026-11-12 13:55:39.478 P00   INFO: new backup label = 20261112-135322F
2026-11-12 13:55:39.508 P00   INFO: full backup size = 76.0MB, file total = 2322
2026-11-12 13:55:39.508 P00   INFO: backup command end: completed successfully (137034ms)
2026-11-12 13:55:39.508 P00   WARN: unable to open log file '/var/log/pgbackrest/main-expire.log': No such file or directory
                                    NOTE: process will continue without log file.
2026-11-12 13:55:39.508 P00   INFO: expire command begin 2.55.1: --exec-id=85-25219c4b --log-level-console=info --log-level-file=off --repo1-path=/var/lib/pgbackrest --repo1-retention-full=2 --stanza=main
2026-11-12 13:55:39.509 P00   INFO: expire command end: completed successfully (1ms)

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec alloydb-omni sh -c 'ls -ld /var/lib/pgbackrest'
drwxr-xr-x 4 postgres postgres 4096 Nov 12 13:51 /var/lib/pgbackrest

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec -u postgres alloydb-omni pgbackrest --stanza=main --type=full --log-level-console=info info
stanza: main
    status: ok
    cipher: none

    db (current)
        wal archive min/max (17): 000000010000000000000033/000000010000000000000035

        full backup: 20261112-135322F
            timestamp start/stop: 2026-11-12 13:53:22+00 / 2026-11-12 13:55:38+00
            wal start/stop: 000000010000000000000033 / 000000010000000000000033
            database size: 76.0MB, database backup size: 76.0MB
            repo1: backup set size: 8.2MB, backup size: 8.2MB

        full backup: 20261112-135620F
            timestamp start/stop: 2026-11-12 13:56:20+00 / 2026-11-12 13:57:08+00
            wal start/stop: 000000010000000000000035 / 000000010000000000000035
            database size: 76.0MB, database backup size: 76.0MB
            repo1: backup set size: 8.2MB, backup size: 8.2MB

```


```
[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ grep -m1 -A16 alloydb-on-vm infrastructure.json
    "alloydb-on-vm": {
      "name": "AlloyDB Omni Database running in a Docker container",
      "connection_string": "host=host.docker.internal port=5432 dbname=postgres user=postgres",
      "password_env": "DEV_DB_PASSWORD",
      "vm_name": "local-vm-docker",
      "container_name": "alloydb-omni",
      "tags": [
        "development",
        "chaos"
      ],
      "approval_override_roles": [
        "dba_lead",
        "oncall_senior"
      ],
      "environment": "prod",
      "objectives": { "rpo": "1s", "enforcement": "acknowledge", "availability_slo": 99.9, "availability_window": "30d" }
    },

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec alloydb-omni psql -U postgres -c "create table if not exists walcount_marker(id serial, at timestamptz default now())"
CREATE TABLE
[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ TARGET=$(date -u +%Y-%m-%dT%H:%M:%SZ); echo $TARGET
2026-11-12T14:07:23Z
[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ sleep 5
[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ for i in 1 2 3 4 5; do docker exec alloydb-omni psql -U postgres -c "insert into walcount_marker default values"; done
INSERT 0 1
INSERT 0 1
INSERT 0 1
INSERT 0 1
INSERT 0 1

[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker exec alloydb-omni psql -U postgres -c "select pg_switch_wal()"
 pg_switch_wal
---------------
 0/360246B8
(1 row)
```

And here's the plan:

```
[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ curl -s -X POST "$HELPDESK_SYSADMIN_URL/tool/plan_point_in_time_recovery" \
  -H "Authorization: Bearer $HELPDESK_SYSADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"trace_id":"tr_pg17","purpose":"diagnostic","args":{"target":"alloydb-on-vm","stanza":"main","recovery_target_type":"time","recovery_target_value":"'$TARGET'"}}' \
  | jq -r .output | jq '{status, detail, data_loss_estimate, availability_budget}'
{
  "status": "feasible",
  "detail": "backup 20261112-135620F can be restored and the WAL from it to the end of the archive is continuous; the target is inside the archive. This is plausible, not certain: the archive's times are coarse, and recovery can still run    out of WAL before the target",
  "data_loss_estimate": {
    "basis": "live_instance",
    "as_of": "2026-11-12T14:10:09Z",
    "to": "2026-11-12T14:10:09Z",
    "window_seconds": 166,
    "upper_bound": false,
    "transactions": {
      "status": "measured",
      "commits": 5,
      "first_commit_at": "2026-11-12T14:08:09Z",
      "last_commit_at": "2026-11-12T14:08:09Z",
      "first_commit_lsn": "0/36024480",
      "last_commit_lsn": "0/36024640",
      "segments_read": 2,
      "read_millis": 214,
      "decoder": "pg_waldump (PostgreSQL) 17.7",
      "basis": "counted by decoding the WAL with pg_waldump from segment 000000010000000000000036, the first to reach the repository at or after the target time: a commit later than the target cannot be in one that arrived before it;         recovery stops before the first commit or abort later than the target time (LSN 0/36024480); read up to the running instance's own position; it grows until the instance is stopped"
    },
    "unmeasured": [
      "which of the discarded writes were wanted: everything after the target counts",
      "the WAL volume (a time has no position until the WAL is read)"
    ],
    "summary": "2m 46s of history discarded, up to the running instance's position at 2026-11-12T14:10:09Z; it grows until the instance is stopped. 5 transactions committed after the target would be discarded (counted from the WAL).",
    "objective": {
      "status": "exceeds",
      "rpo_seconds": 1,
      "source": "database",
      "environment": "prod",
      "detail": "2m 46s discarded against an RPO of 1s (database): exceeds it by 2m 45s",
      "enforcement": "acknowledge",
      "gate": "acknowledge"
    }
  },
  "availability_budget": {
    "status": "within",
    "detail": "0s of the 43m12s budget over 30d already spent, 43m12s left: enough for a restore that takes up to the rto of 30m",
    "budget_seconds": 2592,
    "remaining_seconds": 2592,
    "remaining_worst_case_seconds": 2592,
    "rto_seconds": 1800,
    "window_source": "database",
    "enforcement": "warn",
    "duration_seconds": 1800,
    "duration_basis": "declared_rto",
    "recovery_estimate": {
      "status": "no_history",
      "samples": 0,
      "window_seconds": 7776000,
      "basis": "no resolved restore of this server in the last 90d among real incidents: nothing to estimate from",
      "rto_seconds": 1800
    }
  }
}
```

If you find that JSON a bit unwiedly... yeah, we won't be surprised if it offends your taste buds as it's not that easy to read, which where a CLI comes in handy (especially on other platforms, but on Docker too):

```
[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ docker compose --profile interactive run --rm \
>     -e HELPDESK_SYSADMIN_URL=http://sysadmin-agent:1103 \
>     -e HELPDESK_SYSADMIN_API_KEY=$HELPDESK_SYSADMIN_KEY \
>     helpdesk-client --pitr-plan --pitr-server alloydb-on-vm \
>     --recovery-target-type time --recovery-target $TARGET
[+] Creating 7/7
 ✔ Container helpdesk-auditd-1          Running                                                                                                                                                                                              0.0s
 ✔ Container helpdesk-research-agent-1  Running                                                                                                                                                                                              0.0s
 ✔ Container helpdesk-incident-agent-1  Running                                                                                                                                                                                              0.0s
 ✔ Container helpdesk-sysadmin-agent-1  Running                                                                                                                                                                                              0.0s
 ✔ Container helpdesk-k8s-agent-1       Running                                                                                                                                                                                              0.0s
 ✔ Container helpdesk-database-agent-1  Running                                                                                                                                                                                              0.0s
 ✔ Container helpdesk-gateway-1         Running                                                                                                                                                                                              0.0s
[+] Running 1/1
 ✔ Container helpdesk-auditd-1  Healthy                                                                                                                                                                                                      1.0s
Plan status        : feasible
Detail             : backup 20261112-135620F can be restored and the WAL from it to the end of the archive is continuous; the target is inside the archive. This is plausible, not certain: the archive's times are coarse, and recovery can      still run out of WAL before the target
Backup to restore  : 20261112-135620F
Recovery target    : 2026-11-12 14:07:23+00
Archive ends       : 2026-11-12T14:08:42Z
Will be discarded  : everything committed after 2026-11-12 14:07:23+00, up to the moment the instance is stopped, is discarded
Estimated loss     : 27m 00s of history discarded, up to the running instance's position at 2026-11-12T14:34:23Z; it grows until the instance is stopped. 5 transactions committed after the target would be discarded (counted from the WAL).
Not counted        : which of the discarded writes were wanted: everything after the target counts; the WAL volume (a time has no position until the WAL is read)
RPO CHECK (!)      : exceeds: 27m 00s discarded against an RPO of 1s (database): exceeds it by 26m 59s
Approving needs    : --acknowledge-objective-breach (this database's enforcement is acknowledge)
Budget check       : within: 0s of the 43m12s budget over 30d already spent, 43m12s left: enough for a restore that takes up to the rto of 30m
Past restores      : no resolved restore of this server in the last 90d among real incidents: nothing to estimate from
Instance           : SERVING (the restore must stop it: --allow-stop-serving)
Plan digest        : b90b851df7ea22ba

To approve exactly this plan:
  helpdesk-client --pitr-approve <run_id> --recovery-target-type time --recovery-target "2026-11-12T14:07:23Z" \
    --plan-digest b90b851df7ea22ba --acknowledge-data-loss --allow-stop-serving --acknowledge-objective-breach
```

### Tier B

### Objective Exposure

```
[boris@ /tmp/helpdesk/helpdesk-v0.35.0-deploy/docker-compose]$ curl -s -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" "$HELPDESK_GATEWAY_URL/api/v1/fleet/playbooks" \
  | jq -r '(.playbooks // .) | map(select(.series_id=="pbs_pgbackrest_objective_exposure_triage")) | .[] | [.version, .playbook_id, .is_active] | @tsv'
1.4     pb_af1929f8     true
```

