# aiHelpDesk Sample#32 (host/VM): Drilled Recovery.

The raw transcript of the sample commands and deliberations presented below complements this blog post:

- **[Your AI Won’t Run Your Fire Drills, but It Will Check That You Did](https://itnext.io/your-ai-wont-run-your-fire-drills-but-it-will-check-that-you-did-c20156397868)**  
  A backup nobody has restored is a hope. Restore nobody has practiced is a first attempt on the worst day… billed in downtime against a budget you never measured


If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel).

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This sample transcript was created entirely for a database server running remotely on a host/VM, but see [here](SAMPLE027.md), [here](SAMPLE021.md) and [here](SAMPLE019.md) for K8s, the source and Docker sample transcripts respectively.

In more general terms [docs/PLATFORMS.md](../PLATFORMS.md) is the authoritative source for the supported platforms: where aiHelpDesk auditd, audit Postgres backend store, agents, pgBackRest repo, Gateway and the target database can run).

---

The goal of this transcript is simple: give a taste of the actual commands behind [aiHelpDesk v0.36](https://github.com/borisdali/helpdesk/releases/tag/v0.36.0) release: majorly enhanced restore plan visible via `helpdesk-client`, new `vault drills` command, overdue reminders, duplicate recording guard, ....

See [the ops guide](../RESTORE_OPS.md#11-set-the-objectives) and [the previous transcript](SAMPLE030.md#1-the-declaration-infrajson-inventory) for how to configure objectives, RPO, RTO and availability SLO.




##  1/ Inventory (aka infra config) setup

Yes, not just the source prod database server, but the clone should appear now in the inventory file too (remember aiHelpDesk refuses to work with random databases unless it's configured to run in [personal mode](../DEPLOYMENT_MODES.md#mode-1--personal--self-service), but in that case it's strictly R/O, no action of any kind (like a restore 😉) is allowed. See more on the clone registration [here](../RESTORE_OPS.md#22-register-the-clone).

And BTW, as a reminder, while you are adding the clone entry, don't forget to confirm that the prod db server has the proper objectives set, perhaps similar to these (no need to set them on a clone):

```
[boris@ ~/helpdesk]$ jq '.db_servers["pgbackrest-ubuntu-vm"].objectives' $HELPDESK_INFRA_CONFIG
{
  "rpo": "1h",
  "enforcement": "warn",
  "availability_slo": 99.95,
  "availability_window": "1h",
  "availability_enforcement": "warn",
  "drill_interval": "90d"
}
```


## 2/ Helper script to run a restore.

You probably have a way to create a clone of your production database server. If your databases are of a semi decent size, consider bribing your storage admin with a nice reuben sandwich to clone for you a volume that holds the prod database files to a new volume, mount it under a diff mount point and let your have fun with it. If not, pgBackRest may be your friend or similar means (yes, "cp", "cpio", "tar" and other small scale wizardry on a host can also do the trick). Either way, get a reliable and most importantly a reusable way of creating a clone. 

Why? Well, you'll be doing it more than once and in fact we strongly encourage you to do this periodically, [at least once quarter](../RESTORE_OPS.md#27-the-reminder), so creating a small automation script is not a half bad idea. Just for reference, here's ours, tested on a target database server hosted on a remote Ubuntu VM:

```
[boris@ ~/helpdesk]$ cat restore_drill_clone_linux.sh
#!/bin/bash
# One restore-drill cycle on the Ubuntu test VM, for the live run of v0.36's restore drills.
#
# Restores the stanza "main" backup into a SECOND PostgreSQL cluster on the same VM
# (cluster "drill", port 5433) with archiving off, starts it and waits until it is
# out of recovery. Prints the times to give `helpdesk-client --drill-record`.
#
# Run it from the laptop. The FIRST time as root (it creates the cluster and writes
# /etc/postgresql); every drill after that as the postgres user, with no sudo
# (the estimate needs three drills):
#   first:  scp drill-clone-vm.sh <admin>@<vm-host>:/tmp/ && ssh -t <admin>@<vm-host> 'sudo bash /tmp/drill-clone-vm.sh'
#   later:  ssh postgres@<vm-host> 'bash -s' < drill-clone-vm.sh | tee /tmp/drill-N.out
#   then:   eval "$(grep '^DRILL_' /tmp/drill-N.out)"
#
# Never keeps the source's archiving: the clone must not push WAL into the source's
# repository (docs/RESTORE_OPS.md section 2.1). Remove it afterwards with:
#   sudo pg_dropcluster --stop 16 drill
set -euo pipefail

PGV=16
CL=drill
PORT=5433
DATA="/var/lib/postgresql/${PGV}/${CL}"
CONF="/etc/postgresql/${PGV}/${CL}"
STANZA=main

# Runs a command as the postgres user, whether this script runs as root or as postgres.
as_pg() {
    if [ "$(id -u)" = 0 ]; then su postgres -c "$1"; else bash -c "$1"; fi
}

if ! pg_lsclusters -h | awk '{print $2}' | grep -qx "$CL"; then
    if [ "$(id -u)" != 0 ]; then
        echo "the cluster ${PGV}/${CL} does not exist yet: run this script once as root (it writes /etc/postgresql); after that it runs as the postgres user, with no sudo" >&2
        exit 1
    fi
    echo "creating cluster ${PGV}/${CL} on port ${PORT}"
    pg_createcluster "$PGV" "$CL" --port "$PORT"
    # Same client access as the source, so the laptop can reach the clone.
    install -o postgres -g postgres -m 640 "/etc/postgresql/${PGV}/main/pg_hba.conf" "${CONF}/pg_hba.conf"
    mkdir -p "${CONF}/conf.d"
    {
        echo "listen_addresses = '*'"
        echo "archive_mode = off"
    } > "${CONF}/conf.d/drill.conf"
    chown -R postgres:postgres "${CONF}/conf.d"
fi

pg_ctlcluster "$PGV" "$CL" stop 2>/dev/null || true

STARTED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
# --delta replaces whatever the cluster holds; --archive-mode=off is the point.
as_pg "pgbackrest --stanza=${STANZA} --archive-mode=off --delta --pg1-path=${DATA} restore"
pg_ctlcluster "$PGV" "$CL" start

ready=""
for _ in $(seq 1 300); do
    if [ "$(as_pg "psql -p ${PORT} -d postgres -Atc 'select pg_is_in_recovery()'" 2>/dev/null || true)" = "f" ]; then
        ready=yes
        break
    fi
    sleep 1
done
FINISHED=$(date -u +%Y-%m-%dT%H:%M:%SZ)

if [ -z "$ready" ]; then
    echo "the clone did not come out of recovery within 5 minutes; see /var/log/postgresql/postgresql-${PGV}-${CL}.log" >&2
    exit 1
fi

echo "clone is up on port ${PORT}, archive_mode=$(as_pg "psql -p ${PORT} -d postgres -Atc 'show archive_mode'")"
echo "DRILL_STARTED=${STARTED}"
echo "DRILL_FINISHED=${FINISHED}"
```


## 3/ Networking setup

Not just the actual production database server, but the clone too needs to be accessible from a remote machine running aiHelpDesk. That is, if the former runs on a default port of 5432 and the clone is on 5433, it doesn't hurt to check first that the appropriate paths/holes have been punched in the FWs along the route, e.g.

```
[boris@ ~/helpdesk]$ ssh -t $VM 'sudo ufw status verbose'
...
Status: active
Logging: on (low)
Default: deny (incoming), allow (outgoing), deny (routed)
New profiles: skip

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW IN    Anywhere
5432/tcp                   ALLOW IN    <your IP only>            # aiHelpDesk prod db server target
5433/tcp                   ALLOW IN    <your IP only>            # aiHelpDesk clone db server target
22/tcp (v6)                ALLOW IN    Anywhere (v6)
5432/tcp (v6)              ALLOW IN    Anywhere (v6) 
```

If the port that the clone is to run on doesn't appear in the list, punch a whole in your FW, perhaps using something similar to this (but better just buy your network admin a sandwitch and get them to do that for you):

```
[boris@ ~/helpdesk]$ ssh -t $VM 'sudo ufw allow from <your IP only> to any port 5433 proto tcp'
```

Re-run the previous `ufw status verbose` command to ensure your port (5433 in my example) is indeed open. Good, then proceed to verify it:

```
[boris@ ~/helpdesk]$ nc -vz -w 3 pgbackrest-vm.test 5432; nc -vz -w 3 pgbackrest-vm.test 5433
Connection to pgbackrest-vm.test port 5432 [tcp/postgresql] succeeded!
Connection to pgbackrest-vm.test port 5433 [tcp/pyrrho] succeeded!

[boris@ ~/helpdesk]$ for p in 5432 5433; do PGPASSWORD=testpass psql -h pgbackrest-vm.test -p $p -U postgres -d postgres -c "select system_identifier, pg_is_in_recovery() from pg_control_system(), (select 1) x" ;done
  system_identifier  | pg_is_in_recovery
---------------------+-------------------
 7694453216872244409 | f
(1 row)

  system_identifier  | pg_is_in_recovery
---------------------+-------------------
 7694453216872244409 | f
(1 row)
```

Cool, notice the same identifier on both lines and f on each. We are all set to fire off the restore drills. Let's run three of them in a row on a clone:


## 4/ Three restore drills on a clone hosted on a remote VM (with aiHelpDesk gear running on a host)

It's probably possible to run the script locally where aiHelpDesk gear is deployed, but dealing with the passwords and the sudo over the ssh is not my idea of fun, especially because the postgres ssh user likely has only a very narrow sudo grants (at least that's how I would set it up, see [setup-pgbackrest-vm.sh](../../testing/docker/setup-pgbackrest-vm.sh) for reference, basically just `systemctl` for the unit) and so it shouldn't have ia general sudo bash... and yet the script needs root to create the second db cluster. Anyway, this is not my idea of fun, so I just copied it to the VM and invoked it remotely 😉:

```
[boris@ ~/helpdesk]$ scp restore_drill_clone_linux.sh $VM:/tmp/vmsetup
restore_drill_clone_linux.sh                                                                                                                                                                                    100% 2997   559.5KB/s   00:00

[boris@ ~/helpdesk]$ ssh -t $VM 'sudo bash /tmp/vmsetup/restore_drill_clone_linux.sh'

creating cluster 16/drill on port 5433
Creating new PostgreSQL cluster 16/drill ...
/usr/lib/postgresql/16/bin/initdb -D /var/lib/postgresql/16/drill --auth-local peer --auth-host scram-sha-256 --no-instructions
The files belonging to this database system will be owned by user "postgres".
This user must also own the server process.

The database cluster will be initialized with locale "en_US.UTF-8".
The default database encoding has accordingly been set to "UTF8".
The default text search configuration will be set to "english".

Data page checksums are disabled.

fixing permissions on existing directory /var/lib/postgresql/16/drill ... ok
creating subdirectories ... ok
selecting dynamic shared memory implementation ... posix
selecting default max_connections ... 100
selecting default shared_buffers ... 128MB
selecting default time zone ... America/Toronto
creating configuration files ... ok
running bootstrap script ... ok
performing post-bootstrap initialization ... ok
syncing data to disk ... ok
Ver Cluster Port Status Owner    Data directory               Log file
16  drill   5433 down   postgres /var/lib/postgresql/16/drill /var/log/postgresql/postgresql-16-drill.log
Cluster is not running.
clone is up on port 5433, archive_mode=off
DRILL_STARTED=2026-11-20T01:57:49Z
DRILL_FINISHED=2026-11-20T01:57:52Z
```

W00t! Drill 1 worked. The clone came up on port 5433 with `archive_mode=off` and the restore plus start took 3 seconds. The "Cluster is not running" line is just the script's pre-restore stop finding nothing to stop. Run drills 2 and 3 the same way (just adjust the log file names accordingly).


## 5/ Let's record these drills

See [this doc](../RESTORE_OPS.md#24-record-the-drill) first and try these commands next:

```
[boris@ ~/helpdesk]$ go run ./cmd/helpdesk-client \
  --pitr-plan \
  --pitr-server pgbackrest-ubuntu-vm \
  --recovery-target-type time \
  --recovery-target 2026-11-20T03:52:40Z \
  --sysadmin-url $HELPDESK_SYSADMIN_URL

Plan status        : feasible
Detail             : backup 20261112-174614F can be restored and the WAL from it to the end of the archive is continuous. The instance is serving and the target is in WAL it has not archived yet: the restore flushes the archive first (a WAL
                     switch) and then checks this plan again, so it holds only once the WAL is in the repository. Restoring stops the running instance, which the gate must allow
Backup to restore  : 20261112-174614F
Recovery target    : 2026-11-20 03:52:40+00
Archive ends       : 2026-11-12T22:46:17Z
Will be discarded  : everything committed after 2026-11-20 03:52:40+00, up to the moment the instance is stopped, is discarded
Estimated loss     : 9m 02s of history discarded, up to the running instance's position at 2026-11-20T04:01:42Z; it grows until the instance is stopped. 0 transactions committed after the target would be discarded (counted from the WAL).
Not counted        : which of the discarded writes were wanted: everything after the target counts; the WAL volume (a time has no position until the WAL is read)
RPO check          : within: 9m 02s discarded, within an RPO of 1h 00m 00s (database)
Budget check       : within: 0s of the 4m19s budget over 30d already spent, 4m19s left: enough for a restore as long as this server's drills took (3s, the longest of 3 drills, demonstrated on a copy, not on this server)
Past restores      : no resolved restore of this server in the last 90d among real incidents: nothing to estimate from
Restore drills     : 3 restore drills of this server count (last 90d, pgBackRest, comparable size): median 3s, longest 3s (too few for a p90, which needs 10); not counted: 2 refused or unverifiable. The planned duration comes from these: a
                     restore demonstrated on a copy, not on this server
Drill reminder     : last verified drill today (limit 90 days)
Waiting, budget    : no open incident is in the ledger, so there is no time already spent to count (4m16s of margin, with a restore taken to need 3s). If this restore is run as part of an incident, that incident is charged from its start,
                     and if the instance is down the downtime is accruing regardless
Waiting, data      : the recovery target is fixed and the instance keeps taking writes, so the history a restore discards grows by a minute every minute: 9m 02s now, against an rpo of 1h 00m 00s; estimated, under current assumptions, in 50m
                     58s it would exceed it
Instance           : SERVING (the restore must stop it: --allow-stop-serving)
Archive flush      : required first (the target is in WAL not yet archived)
Plan digest        : 76082dbe1c16fb65

To approve exactly this plan:
  helpdesk-client --pitr-approve <run_id> --recovery-target-type time --recovery-target "2026-11-20T03:52:40Z" \
    --plan-digest 76082dbe1c16fb65 --acknowledge-data-loss --allow-stop-serving


[boris@ ~/helpdesk]$ curl -s -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" $HELPDESK_AUDIT_URL/v1/verify
{"valid":true,"total_events":88,"hashed_events":88,"legacy_events":0,"broken_at":-1,"first_event_id":"rdc_cf118eba","last_event_id":"rdc_13a838e9","last_hash":"e1c2a48cd7db940dfc549ac4ae3d3d72bae87fa2a9de1060a2114c291efd7339"}
```

Cool. This is the plan of a restore drills on a VM. Let's review what it shows in detail:

        - Drill estimate:
          Restore drills: 3 restore drills of this server count (last 90d, pgBackRest, comparable size): median 3s, longest 3s (too few for a p90, which needs 10).
          The planned duration comes from them, worded "demonstrated on a copy, not on this server". The budget line uses the same 3 s.

        - Rows not counted:
          Not counted: 2 refused or unverifiable.
          Those are your two early attempts, the policy denials. They stay on the record and never count, which is the intended behaviour.
          You didn't use --drill-supersedes, so they stand as separate rows.

        - Reminder: last verified drill today (limit 90 days).

        - Identifier read over SSH:
          No "System identifier not checked" line appeared, so the VM read worked. That was one of the live gaps.

        - Audit chain:
          /v1/verify gives valid: true over 88 events, 0 legacy and broken_at: -1, with the drill events included.

        - Plan digest and approve command:
          Unchanged and correct.

Now note that the RTO CHECK doesn't appear, because our source entry has no rto. It only has `rpo: 1h`, so that is correct behaviour, not a failure. Good.


## 6/ Duplicates guard:

Here's the original problem we discovered in the lab:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault drills \
  --target pgbackrest-ubuntu-vm \
  --audit-url $HELPDESK_AUDIT_URL \
  --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: dev  ·  host: <your host>

time=2026-11-20T09:01:19.103-05:00 level=INFO msg=--conn host=localhost
DRILL         WHEN      SOURCE <- CLONE                METHOD     TOOK      SIZE       STATUS
drl_a214133e  10h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        22.0 MiB   verified  DRILL-VM-4
drl_09e498a9  10h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        22.0 MiB   verified  DRILL-VM-3
drl_9f7cc354  10h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        22.0 MiB   verified  DRILL-VM-2
drl_aad337d6  12h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        22.0 MiB   verified  DRILL-VM-1
drl_46c5876d  12h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        -          unverifiable  DRILL-VM-1
drl_3666984d  12h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        -          unverifiable  DRILL-VM-1

6 drill(s), 2 of them refused or unverifiable and not counted.
A drill resets the reminder when verified. It also plans a restore only when it is a pgBackRest drill
of comparable size; see `helpdesk-client --pitr-plan` for what the plan rests on.
Open one with `faulttest vault drills <drill id>`.
```

Looks nice, isn't it? Not quite. Consider:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault drills drl_a214133e \
  --audit-url $HELPDESK_AUDIT_URL \
  --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: dev  ·  host: <your host>

time=2026-11-24T17:16:20.670-05:00 level=INFO msg=--conn host=localhost
Restore drill drl_a214133e  VERIFIED
  Source     pgbackrest-ubuntu-vm (pgbackrest-vm.test:5432, system 7694453216872244409)
  Clone      orders-clone (pgbackrest-vm.test:5433, system 7694453216872244409)
  Method     pgbackrest
  When       2026-11-20 03:54 to 03:54 UTC, 2026-11-20; took 3s (reported)
  Size       22.0 MiB measured on the clone
  Recorded   recorded 2026-11-20 04:43 UTC by gateway, ticket DRILL-VM-4
  Note       VM drill rto

  Checks
    PASS reachable
    PASS same_system: system identifier 7694453216872244409
    PASS not_the_source
    PASS archive_guard: archive_mode is off
    PASS consistent: out of recovery
    PASS size_class: clone 22.0 MiB, source 22.0 MiB
    PASS verify_sql

  This drill is verified: it resets the drill reminder.
  Trail: audit event evt_1ccdadf1; infra sha256:9844fb79e05eea2cd7a681baf89f1aedfeb74c2426b2ffb13da63d8f9d438580

[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault drills drl_09e498a9 \
  --audit-url $HELPDESK_AUDIT_URL \
  --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: dev  ·  host: <your host>

time=2026-11-24T17:16:39.509-05:00 level=INFO msg=--conn host=localhost
Restore drill drl_09e498a9  VERIFIED
  Source     pgbackrest-ubuntu-vm (pgbackrest-vm.test:5432, system 7694453216872244409)
  Clone      orders-clone (pgbackrest-vm.test:5433, system 7694453216872244409)
  Method     pgbackrest
  When       2026-11-20 03:54 to 03:54 UTC, 2026-11-20; took 3s (reported)
  Size       22.0 MiB measured on the clone
  Recorded   recorded 2026-11-20 03:55 UTC by gateway, ticket DRILL-VM-3
  Note       VM drill 3 of 3

  Checks
    PASS reachable
    PASS same_system: system identifier 7694453216872244409
    PASS not_the_source
    PASS archive_guard: archive_mode is off
    PASS consistent: out of recovery
    PASS size_class: clone 22.0 MiB, source 22.0 MiB
    PASS verify_sql

  This drill is verified: it resets the drill reminder.
  Trail: audit event evt_8af9b49d; infra sha256:9844fb79e05eea2cd7a681baf89f1aedfeb74c2426b2ffb13da63d8f9d438580
```

It may not be that easy to spot, but the "When" line is identical for both drills, which are supposed to be distinct. They are not. What happened here is that we recorded the same drill twice. Oops.

How do we prevent that Oops? Well, I'm glad you asked because that's exactly where the duplicate guard comes in :-)

What does it dow? Well, auditd now refuses with HTTP 409 a record that is the same restore as one already counted. That means the same source key, the same clone key and the same start and finish times, with a status of verified or `verified_with_warnings`. The message names the existing drill, for example already recorded as drill drl_...: ... would count it twice ...  or name it with supersedes drl_.... 

The gateway passes the 409 through and writes its usual failed-attempt event. The client prints the message.

To be sure, the guard doesn't block the re-recording with `--drill-supersedes <that drill>`. The old row stops counting, so the sample stays one. This is the intended way to re-record (aka re-record by design).

Here's how it works in practice:

```
[boris@ ~/helpdesk]$ echo "DRILL_STARTED=$DRILL_STARTED, DRILL_FINISHED=$DRILL_FINISHED"
DRILL_STARTED=2026-11-20T03:54:15Z, DRILL_FINISHED=2026-11-20T03:54:18Z

[boris@ ~/helpdesk]$ date; time go run ./cmd/helpdesk-client \
  --drill-record \
  --drill-source pgbackrest-ubuntu-vm \
  --drill-clone orders-clone \
  --drill-method pgbackrest \
  --drill-started "$DRILL_STARTED" \
  --drill-finished "$DRILL_FINISHED" \
  --drill-verify-sql "select count(*) > 0 from pg_class" \
  --ticket DRILL-VM-4 \
  --drill-note "VM drill rto"

Fri Nov 20 09:23:51 EST 2026
helpdesk-client: the gateway did not record the drill (HTTP 409): 
The audit service did not record the drill (409): 
This restore is already recorded as drill drl_a214133e: 
The same source, clone, start and finish. 
A drill is one restore, so  recording it again would count it twice. 
Restore again and record that, or to re-record this one name it with supersedes drl_a214133e
exit status 1

real    0m4.470s
user    0m0.196s
sys     0m0.288s

[boris@ ~/helpdesk]$ ssh -i $VMKEY postgres@pgbackrest-vm.test 'bash -s' < ~/helpdesk/restore_drill_clone_linux.sh | tee /tmp/drill5.out
clone is up on port 5433, archive_mode=off
DRILL_STARTED=2026-11-20T14:25:27Z
DRILL_FINISHED=2026-11-20T14:25:30Z

[boris@ ~/helpdesk]$ eval "$(grep '^DRILL_' /tmp/drill5.out)"; echo $DRILL_STARTED $DRILL_FINISHED
2026-11-20T14:25:27Z 2026-11-20T14:25:30Z

[boris@ ~/helpdesk]$ echo "DRILL_STARTED=$DRILL_STARTED, DRILL_FINISHED=$DRILL_FINISHED"
DRILL_STARTED=2026-11-20T14:25:27Z, DRILL_FINISHED=2026-11-20T14:25:30Z

[boris@ ~/helpdesk]$ date; time go run ./cmd/helpdesk-client \
  --drill-record \
  --drill-source pgbackrest-ubuntu-vm \
  --drill-clone orders-clone \
  --drill-method pgbackrest \
  --drill-started "$DRILL_STARTED" \
  --drill-finished "$DRILL_FINISHED" \
  --drill-verify-sql "select count(*) > 0 from pg_class" \
  --ticket DRILL-VM-5 \
  --drill-note "VM drill# 5"

Fri Nov 20 09:26:49 EST 2026
Restore drill of pgbackrest-ubuntu-vm on orders-clone: VERIFIED
  PASS reachable
  PASS same_system: system identifier 7694453216872244409
  PASS not_the_source
  PASS archive_guard: archive_mode is off
  PASS consistent: out of recovery
  PASS size_class: clone 22.0 MiB, source 22.0 MiB
  PASS verify_sql

Recorded as drl_cccbd12b (verified)
  ticket   DRILL-VM-5
  took     3s (reported)
  size     22.0 MiB measured on the clone
  counts   yes: a verified drill

real    0m3.885s
user    0m0.181s
sys     0m0.283s


[boris@ ~/helpdesk]$ date -u -v-5M +"%Y-%m-%dT%H:%M:%SZ"
2026-11-20T14:22:27Z

[boris@ ~/helpdesk]$ RECOVERY_TARGET=$(date -u -v-5M +"%Y-%m-%dT%H:%M:%SZ")
[boris@ ~/helpdesk]$ go run ./cmd/helpdesk-client \
  --pitr-plan \
  --pitr-server pgbackrest-ubuntu-vm \
  --recovery-target-type time \
  --recovery-target $RECOVERY_TARGET \
  --sysadmin-url $HELPDESK_SYSADMIN_URL

Plan status        : feasible
Detail             : backup 20261112-174614F can be restored and the WAL from it to the end of the archive is continuous. The instance is serving and the target is in WAL it has not archived yet: the restore flushes the archive first (a WAL
                     switch) and then checks this plan again, so it holds only once the WAL is in the repository. Restoring stops the running instance, which the gate must allow
Backup to restore  : 20261112-174614F
Recovery target    : 2026-11-20 14:23:16+00
Archive ends       : 2026-11-12T22:46:17Z
Will be discarded  : everything committed after 2026-11-20 14:23:16+00, up to the moment the instance is stopped, is discarded
Estimated loss     : 5m 05s of history discarded, up to the running instance's position at 2026-11-20T14:28:21Z; it grows until the instance is stopped. 0 transactions committed after the target would be discarded (counted from the WAL).
Not counted        : which of the discarded writes were wanted: everything after the target counts; the WAL volume (a time has no position until the WAL is read)
RPO check          : within: 5m 05s discarded, within an RPO of 1h 00m 00s (database)
Budget check       : within: 0s of the 4m19s budget over 30d already spent, 4m19s left: enough for a restore as long as this server's drills took (3s, the longest of 5 drills, demonstrated on a copy, not on this server)
Past restores      : no resolved restore of this server in the last 90d among real incidents: nothing to estimate from
Restore drills     : 5 restore drills of this server count (last 90d, pgBackRest, comparable size): median 3s, longest 3s (too few for a p90, which needs 10); not counted: 2 refused or unverifiable. The planned duration comes from these: a
                     restore demonstrated on a copy, not on this server
RTO CHECK (!)      : exceeds, by the drills: 5 of the 5 drills took longer than the rto of 2s (the longest took 3s)
Drill reminder     : last verified drill today (limit 90 days)
Waiting, budget    : no open incident is in the ledger, so there is no time already spent to count (4m16s of margin, with a restore taken to need 3s). If this restore is run as part of an incident, that incident is charged from its start,
                     and if the instance is down the downtime is accruing regardless
Waiting, data      : the recovery target is fixed and the instance keeps taking writes, so the history a restore discards grows by a minute every minute: 5m 05s now, against an rpo of 1h 00m 00s; estimated, under current assumptions, in 54m
                     55s it would exceed it
Instance           : SERVING (the restore must stop it: --allow-stop-serving)
Archive flush      : required first (the target is in WAL not yet archived)
Plan digest        : 3da996a9db69821c

To approve exactly this plan:
  helpdesk-client --pitr-approve <run_id> --recovery-target-type time --recovery-target "2026-11-20T14:23:16Z" \
    --plan-digest 3da996a9db69821c --acknowledge-data-loss --allow-stop-serving

[boris@ ~/helpdesk]$ curl -s -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" $HELPDESK_AUDIT_URL/v1/verify
{"valid":true,"total_events":122,"hashed_events":122,"legacy_events":0,"broken_at":-1,"first_event_id":"rdc_cf118eba","last_event_id":"rdc_08dcb5e2","last_hash":"b45d67df4afd6b3705edfcc01270a4f789f0d938d1747f820eee46284f7f0a04"}

[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault drills \
  --target pgbackrest-ubuntu-vm \
  --audit-url $HELPDESK_AUDIT_URL \
  --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: dev  ·  host: <your host>

time=2026-11-20T09:30:11.371-05:00 level=INFO msg=--conn host=localhost
DRILL         WHEN      SOURCE <- CLONE                METHOD     TOOK      SIZE       STATUS
drl_cccbd12b  just now  pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        22.0 MiB   verified  DRILL-VM-5
drl_a214133e  10h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        22.0 MiB   verified  DRILL-VM-4
drl_09e498a9  10h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        22.0 MiB   verified  DRILL-VM-3
drl_9f7cc354  10h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        22.0 MiB   verified  DRILL-VM-2
drl_aad337d6  12h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        22.0 MiB   verified  DRILL-VM-1
drl_46c5876d  12h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        -          unverifiable  DRILL-VM-1
drl_3666984d  12h ago   pgbackrest-ubuntu-vm <- orders-clone pgbackrest 3s        -          unverifiable  DRILL-VM-1

7 drill(s), 2 of them refused or unverifiable and not counted.
A drill resets the reminder when verified. It also plans a restore only when it is a pgBackRest drill
of comparable size; see `helpdesk-client --pitr-plan` for what the plan rests on.
Open one with `faulttest vault drills <drill id>`.
```

Works! The guard: the rerun of the drill 3 times was refused with the 409 naming `drl_a214133e`, which is the expected message. Good. The genuine fifth restore (14:25:27Z to 14:25:30Z) was accepted as `drl_cccbd12b`. The plan also shows the following:

  	- Drill estimate: 
	  5 restore drills of this server count, planned at 3 s, still "demonstrated on a copy".

  	- RTO CHECK (!): 
	  5 of the 5 drills took longer than the rto of 2s. The rto check fires live.

  	- Count: 
	  The 5 includes `drl_a214133e`, the duplicate recorded before the guard existed. Four are distinct restores. It still counts, and the guard doesn't touch old rows.

  	- Chain: 	
 	  /v1/verify is valid with 122 events.

  	- Rows not counted: 
	  The two policy-denial attempts, not counted: 2 refused or unverifiable.



## 7/ Overdue reminder:

Let's confirm that the overdue reminder works as designed: set `drill_interval` to "1m" just for testing, restart the SysAdmin Agent and the Gateway, wait and expect DRILL OVERDUE (!).

```
[boris@ ~/helpdesk]$ grep drill infrastructure.json
      "objectives": { "rpo": "1h", "enforcement": "warn", "availability_slo": 99.99, "availability_window": "30d", "availability_enforcement": "acknowledge", "rto": "2s", "drill_interval": "1m" }

[boris@ ~/helpdesk]$ go run ./cmd/helpdesk-client --pitr-plan --pitr-server pgbackrest-ubuntu-vm --recovery-target-type time --recovery-target $RECOVERY_TARGET --sysadmin-url $HELPDESK_SYSADMIN_URL
Plan status        : feasible
Detail             : backup 20261112-174614F can be restored and the WAL from it to the end of the archive is continuous. The instance is serving and the target is in WAL it has not archived yet: the restore flushes the archive first (a WAL
                     switch) and then checks this plan again, so it holds only once the WAL is in the repository. Restoring stops the running instance, which the gate must allow
Backup to restore  : 20261112-174614F
Recovery target    : 2026-11-20 14:23:16+00
Archive ends       : 2026-11-12T22:46:17Z
Will be discarded  : everything committed after 2026-11-20 14:23:16+00, up to the moment the instance is stopped, is discarded
Estimated loss     : 23m 58s of history discarded, up to the running instance's position at 2026-11-20T14:47:14Z; it grows until the instance is stopped. 0 transactions committed after the target would be discarded (counted from the WAL).
Not counted        : which of the discarded writes were wanted: everything after the target counts; the WAL volume (a time has no position until the WAL is read)
RPO check          : within: 23m 58s discarded, within an RPO of 1h 00m 00s (database)
Budget check       : within: 0s of the 4m19s budget over 30d already spent, 4m19s left: enough for a restore as long as this server's drills took (3s, the longest of 5 drills, demonstrated on a copy, not on this server)
Past restores      : no resolved restore of this server in the last 90d among real incidents: nothing to estimate from
Restore drills     : 5 restore drills of this server count (last 90d, pgBackRest, comparable size): median 3s, longest 3s (too few for a p90, which needs 10); not counted: 2 refused or unverifiable. The planned duration comes from these: a
                     restore demonstrated on a copy, not on this server
RTO CHECK (!)      : exceeds, by the drills: 5 of the 5 drills took longer than the rto of 2s (the longest took 3s)
DRILL OVERDUE (!)  : last verified drill today (limit 0 days). Record one with helpdesk-client --drill-record (see docs/RESTORE_OPS.md)
Waiting, budget    : no open incident is in the ledger, so there is no time already spent to count (4m16s of margin, with a restore taken to need 3s). If this restore is run as part of an incident, that incident is charged from its start,
                     and if the instance is down the downtime is accruing regardless
Waiting, data      : the recovery target is fixed and the instance keeps taking writes, so the history a restore discards grows by a minute every minute: 23m 58s now, against an rpo of 1h 00m 00s; estimated, under current assumptions, in 36m
                     02s it would exceed it
Instance           : SERVING (the restore must stop it: --allow-stop-serving)
Archive flush      : required first (the target is in WAL not yet archived)
Plan digest        : 3da996a9db69821c

To approve exactly this plan:
  helpdesk-client --pitr-approve <run_id> --recovery-target-type time --recovery-target "2026-11-20T14:23:16Z" \
    --plan-digest 3da996a9db69821c --acknowledge-data-loss --allow-stop-serving
```

The overdue reminder works! We set `drill_interval` to a minute and got DRILL OVERDUE (!): last verified drill today (limit 0 days). The state is correct. The last drill was about 20 minutes old and the limit was 1 minute (there was initially the problem with fraction of a day that showed "0 days", but it's fixed now). What the output also confirms:

        - Overdue fires and the reminder reads the drills: the sysadmin agent picked the 1-minute limit up from your infra file.
        - The earlier lines are unchanged: RTO CHECK (!) still fires, the budget line still uses the drills, and the plan digest is unchanged.
        - The drills still count: the five drills (four distinct restores plus the early duplicate) remain, so overdue is about age and not about counting.



