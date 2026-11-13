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
  "detail": "backup 20261112-135620F can be restored and the WAL from it to the end of the archive is continuous; the target is inside the archive. This is plausible, not certain: the archive's times are coarse and recovery can still run    out of WAL before the target",
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
Detail             : backup 20261112-135620F can be restored and the WAL from it to the end of the archive is continuous; the target is inside the archive. This is plausible, not certain: the archive's times are coarse and recovery can      still run out of WAL before the target
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





### Live restores: the budget gate, the ledger and the estimate

The plan above is R/O. To see the rest, the budget has to be actually spent by real restores, started the way an operator would start them (not by `faulttest`, whose runs never count).   
  
`scripts/demo-restore-cycle.sh` does one cycle against the pgBackRest fixture:  
 * a fresh full backup  
 * a row before a target time
 * the target, three rows after it
 * the plan
 * `pbs_pgbackrest_pitr_triage` through the Gateway
 * `helpdesk-client --pitr-approve`
 * a wait for the incident
 * a check of the rows and the report

The fixture's entry in the infra file was tightened so that the budget has something to say. The `rpo` is loosened to 1h with `warn`, so only the budget gates:

```json
"objectives": { "rpo": "1h", "enforcement": "warn", "availability_slo": 99.99, "availability_window": "30d", "availability_enforcement": "acknowledge" }
```

99.99% over 30 days is a budget of 4m 19s, shorter than the 30m `rto`. The Gateway and the SysAdmin Agent read the file at startup and were recreated.

#### The "second yes"

Ah, the "second yes" 😉. If you haven't read (or at least skimmed) [this piece](https://levelup.gitconnected.com/ai-driven-database-restore-is-now-rpo-aware-and-answers-this-question-how-much-did-we-lose-2712ae40defa), we recommend that you do. In a nutshell, it's an explicit ack a human is expected to provide, should they proceed through the already blown objective (RPO in particular in that piece, but as of v0.35 release that also includes the availability SLO).   

With that in mind, let's kick off the first cycle, approved **without** `--acknowledge-objective-breach` (`ACK_BREACH=no`). Now, the plan had already said what it would cost:

```
"budget": "0s of the 4m19s budget over 30d already spent, only 4m19s left 
          and this restore may take up to the rto of 30m: 
          it may use more than is left (a restore can finish sooner than its rto)"

"budget_gate": "acknowledge"
```

But here's the thing: the Gateway accepted the approval, but the restore tool did not:

```
refusing to restore — the downtime this restore causes may exceed the availability budget... 
  approve the gate again with acknowledge_objective_breach
```

and the instance was not stopped or affected in any other way (and in particular, the three "after" rows were still there).   
  
Two things we learned from it:

- The refusal comes from the **tool**, at restore time, not from the Gateway at approval.
- The gate was already used. Approving the same run again with the flag was refused by the Gateway: `HTTP 409: run "plr_9f9ad29f" is not in gate_pending state (outcome="transitioned")`. The way on is a new triage run. The refusal text said "approve the gate again", which was wrong and it now says to start the triage again.

The incident ended `needs_human`. Because its playbook is an outage-class one and nothing confirmed a recovery, the ledger lists it apart as **unconfirmed** (37 s), not charged:

```
Unconfirmed (outages not charged: their real downtime is unknown)
  inc_b8a32b5e   2026-11-12 17:14  needs_human            37s
```

#### Three restores

With the acknowledgement (the script adds it when the plan asks), three cycles. Each restore stops the serving container, restores the backup, restarts and `verify_restore_recovery` reports `verified`; the timeline goes 1 to 2, 3, 4. Each incident was `resolved` in about 110 seconds through the Gateway. The rows of each cycle are tagged and the check after each restore reads `before-<id>=1` with no `after-<id>` rows.

The ledger after the third:

```
Availability budget: pgbackrest-db
  Window   30d [database], as of 2026-11-12 19:30 UTC
  Budget   availability 99.99% over 30d allows 4m19s of downtime per window; rto 30m is longer than the whole budget: one recovery that takes the full rto breaks the SLO
  Spent    5m 43s in 3 outage incident(s), 132% of the budget
  Left     -1m 24s   ⚠ OVERSPENT: the budget is used up
           -2m 01s if the unconfirmed outages below count in full

Charged (outages that count against the budget)
  inc_120e0009   2026-11-12 18:27  resolved               2m 02s
  inc_eb659429   2026-11-12 19:20  resolved               1m 50s
  inc_49e8d89e   2026-11-12 19:28  resolved               1m 51s
```

#### The estimate learns

The plan's recovery estimate before and after, from the same server:

| Restores before the plan | `recovery_estimate.status` | What the plan assumed for the restore's duration |
|---|---|---|
| 0 | `no_history` | the declared `rto`, 30m |
| 1, 2 | `insufficient_history` (samples 1, 2) | the declared `rto` |
| 3 | `estimated`: median 1m30s, longest 1m41s | 101 s, the longest (`duration_basis: history_longest`) |

```
"basis": "3 resolved restores of this server in the last 90d (real incidents): median 1m30s, longest 1m41s (too few for a p90, which needs 10); 1 other run(s) did not recover or had not finished and are not counted"
"vs_rto_detail": "none of the 3 past restores took longer than the rto of 30m (the longest took 1m41s)"
```

The budget status of the plan made after that was `exceeds` (`-1m24s` left), with the gate `acknowledge`.

#### Two durations that look like a discrepancy and are not

The estimate says the longest restore took 1m41s and the ledger charged 2m02s, 1m50s and 1m51s for the same restores. They measure different things. The estimate is the **remediation hop**: from after the gate approval to completion (restore, restart, verification). The ledger charges the **whole incident**, from the start of the triage run to the end of the last run, so it includes the triage, the model's reasoning and the wait at the gate. The first is the recovery duration, the second is the closest the system has to downtime and neither is the downtime itself: the instance may have been down before aiHelpDesk was engaged.

