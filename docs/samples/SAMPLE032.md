# aiHelpDesk Sample#32 (Docker): Drilled Recovery.

The raw transcript of the sample commands and deliberations presented below complements this blog post:

- **[Your AI Won’t Run Your Fire Drills, but It Will Check That You Did](https://itnext.io/your-ai-wont-run-your-fire-drills-but-it-will-check-that-you-did-c20156397868)**  
  A backup nobody has restored is a hope. Restore nobody has practiced is a first attempt on the worst day… billed in downtime against a budget you never measured


If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel).

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This sample transcript was created entirely for Docker, but see [here](SAMPLE027.md), [here](SAMPLE021.md) and [here](SAMPLE028.md) for K8s, the source and host/VM sample transcripts respectively.

In more general terms [docs/PLATFORMS.md](../PLATFORMS.md) is the authoritative source for the supported platforms: where aiHelpDesk auditd, audit Postgres backend store, agents, pgBackRest repo, Gateway and the target database can run).

---

The goal of this transcript is simple: give a taste of the actual commands behind [aiHelpDesk v0.36](https://github.com/borisdali/helpdesk/releases/tag/v0.36.0) release: ...

See [the ops guide](../RESTORE_OPS.md#11-set-the-objectives) and [the previous transcript](SAMPLE030.md#1-the-declaration-infrajson-inventory) for how to configure objectives, RPO, RTO and availability SLO.




## 




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


## ???

```
```

The drills plan the restore on the VM.
   The plan, the audit chain and the identifier read all behaved as designed.

  What the output shows:
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


## Duplicates guard:

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



## Overdue reminder:

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



