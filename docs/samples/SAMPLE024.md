# aiHelpDesk Sample#24 (from source): Candary test probing if uncomitted tx can get lost during the restore.

This sample is different from most other aiHelpDesk samples that, for the most part, offer detailed transcript of commands, their output and our deliberations around them (in many cases to support our blog posts, see [here](https://aihelpdesk.biz)). The idea is to allow our customers and the community in general to reproduce our steps and see our logic and how we reach our conclusions.

This sample is different because there are no tokens used and no AI in it at all. It's pure Postgres (or at least our understanding of it) when it comes to not so happy path of restore and recovery from a backup. In particular, it shows our basic test of the theory that the Postgres doesn't appear to offer a native (at least in naming) separation of the "online" and "archived" WAL segments, similar to Oracle's oneline redo logs (typically multiplexed) and the archive redo logs (often also across multiple destinations). That, combined with the fact that pgBackRest likes to keep the `pg_wal` directory consistent with its view of the world as of the last backup, prompted a question if the latest, unarchived WAL segements that pgBackRest didn't get a chance to archive... may get lost.

So naturally, we built a test and discovered a few other interesting (and surprising!) things along the way too. Here's the skinny:

- **Yes, they may get lost.** 🤯   
  With archiving broken, two committed rows that lived only in `pg_wal` were gone after `restore_from_backup`. `pgbackrest --delta restore` emptied `pg_wal` completely and recovery stopped at the end of the archive.
- **The textbook fix works**  
  ... but with a catch: move `pg_wal` aside before the restore, put it back before startup. And sure, both rows came back. *But* the WAL we put back was never archived, so the new timeline depended on a segment the repo didn't have... which means that the *next* restore would have stopped short 🤯
- **The obvious fix for the catch doesn't work.**   
  Or at least it didn't reliably enough work for us. Marking those segments `.ready` for Postgres archiver looks right. Postgres deletes the marker during recovery and then the segment itself, as if it had been archived. So based on that experience, what we do now is pushing them with `pgbackrest archive-push` before startup.
- **The post-restore check had a blind spot too.**   
  Postgres logs every segment it restores from the archive, but nothing for the ones it reads from `pg_wal`. What this means is that a recovery that replayed the tail looked exactly like one that didn't. We couldn't tell them apart.

Each of these four surprising findgs were discvoered by running it in our lab, not by reading about it. Here are the details:

---

## 1. The question

Oracle keeps the most recent redo in the *online* redo logs and copies it to the *archived* ones and a complete, non-PITR recovery naturally needs both. Postgres has the same split... just without the names. The segment being written and any completed segment the archiver hasn't shipped yet (marked `.ready` in `pg_wal/archive_status`), exist only in `pg_wal`. If archiving lags or breaks, everything committed since lives there and nowhere else.

aiHelpDesk's `restore_from_backup` (v0.31) runs, after its safety checks:

```
PGDATA=$(grep '^pg1-path=' /etc/pgbackrest.conf | cut -d= -f2)  
rm -f "$PGDATA/postmaster.pid"  
pgbackrest --stanza=main --delta restore
```

`--delta` makes pgBackRest remove every file in PGDATA its backup manifest doesn't list. Does that include `pg_wal`? The [`db-pgdata-corrupted`](../BACKUP.md#3-restore-from-backup-after-data-loss) fault (a deleted `pg_control`) is exactly the case where it would hurt: the data directory is broken, but `pg_wal` is intact and full of committed work.

## 2. Canary test #1: the restore as it was

The Docker pgBackRest fixture (`helpdesk-test-pg-pgbackrest`), starting from a healthy state with archiving on:

```
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest psql -Atc "show archive_command"
pgbackrest --stanza=main archive-push %p
```

A fresh backup, so the test starts from a known archive end:

```
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest pgbackrest --stanza=main --type=full backup
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest pgbackrest --stanza=main info | tail -5
        full backup: 20261019-152555F
            timestamp start/stop: 2026-10-19 15:25:55+00 / 2026-10-19 15:25:58+00
            wal start/stop: 000000060000000000000008 / 000000060000000000000008
            database size: 29.5MB, database backup size: 29.5MB
            repo1: backup set size: 3.9MB, backup size: 3.9MB
```

Now break archiving and commit two canary rows: **A** followed by `pg_switch_wal()`, so it ends up in a completed segment that can't be archived (`.ready`) and **B** in the segment still open:

```
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest psql -Atc "alter system set archive_command = '/bin/false'" -c "select pg_reload_conf()"
ALTER SYSTEM
t
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest psql -At \
    -c "create table canary(id int primary key, where_ text, at timestamptz default now())" \
    -c "insert into canary values (1,'A: ready segment')" \
    -c "select pg_walfile_name(pg_current_wal_lsn())" \
    -c "select pg_switch_wal()" \
    -c "insert into canary values (2,'B: current segment')" \
    -c "select pg_walfile_name(pg_current_wal_lsn()), pg_current_wal_lsn()" \
    -c "select * from canary"

CREATE TABLE
INSERT 0 1
000000060000000000000009
0/9028398
INSERT 0 1
00000006000000000000000A|0/A0000E8
1|A: ready segment|2026-10-19 15:27:55.983514+00
2|B: current segment|2026-10-19 15:27:56.135477+00
```

Row A is in `…09`, row B in `…0A`. Neither reached the archive, which ends at `…08`:

```
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest ls -l /var/lib/postgresql/data/pg_wal/archive_status | grep -v history
-rw------- 1 postgres postgres 0 Oct 19 15:25 000000060000000000000007.done
-rw------- 1 postgres postgres 0 Oct 19 15:25 000000060000000000000008.00000060.backup.done
-rw------- 1 postgres postgres 0 Oct 19 15:25 000000060000000000000008.done
-rw------- 1 postgres postgres 0 Oct 19 15:27 000000060000000000000009.ready

[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest pgbackrest --stanza=main info | grep "wal archive"
        wal archive min/max (16): 000000010000000000000003/000000060000000000000008
```

The crash, exactly as the fault injects it: delete `pg_control`, restart so Postgres tries to start and can't:

```
[boris@ ~/helpdesk]$ docker exec helpdesk-test-pg-pgbackrest rm -f /var/lib/postgresql/data/global/pg_control
[boris@ ~/helpdesk]$ docker restart helpdesk-test-pg-pgbackrest
[boris@ ~/helpdesk]$ docker exec helpdesk-test-pg-pgbackrest pg_isready
/var/run/postgresql:5432 - no response
```

`pg_wal` before the restore, all five segments still there:

```
/var/lib/postgresql/data/pg_wal:
-rw------- 1 postgres postgres 16777216 Oct 19 15:25 000000060000000000000007
-rw-r----- 1 postgres postgres 16777216 Oct 19 15:25 000000060000000000000008
-rw------- 1 postgres postgres      370 Oct 19 15:25 000000060000000000000008.00000060.backup
-rw-r----- 1 postgres postgres 16777216 Oct 19 15:27 000000060000000000000009
-rw-r----- 1 postgres postgres 16777216 Oct 19 15:27 00000006000000000000000A
-rw-r----- 1 postgres postgres 16777216 Oct 18 21:28 00000006000000000000000B
drwx------ 2 postgres postgres     4096 Oct 19 15:27 archive_status
```

`restore_from_backup`'s own script, verbatim, then `pg_wal` again:

```
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest sh -c \
    'PGDATA=$(grep "^pg1-path=" /etc/pgbackrest.conf | cut -d= -f2); rm -f "$PGDATA/postmaster.pid"; pgbackrest --stanza=main --delta restore'

[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest ls -l /var/lib/postgresql/data/pg_wal /var/lib/postgresql/data/pg_wal/archive_status
/var/lib/postgresql/data/pg_wal:
total 4
drwx------ 2 postgres postgres 4096 Oct 19 15:29 archive_status

/var/lib/postgresql/data/pg_wal/archive_status:
total 0
```

Empty. Start it and look:

```
[boris@ ~/helpdesk]$ docker restart helpdesk-test-pg-pgbackrest
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest psql -At -c "select * from canary"
ERROR:  relation "canary" does not exist
LINE 1: select * from canary
                      ^
[boris@ ~/helpdesk]$ docker logs --since 2m helpdesk-test-pg-pgbackrest 2>&1 | grep -E "restored log file|redo done|selected new timeline|consistent" | grep -v history
2026-10-19 15:30:49.137 UTC [36] LOG:  restored log file "000000060000000000000008" from archive
2026-10-19 15:30:49.156 UTC [36] LOG:  consistent recovery state reached at 0/8000138
2026-10-19 15:30:49.159 UTC [36] LOG:  redo done at 0/8000138 system usage: CPU: user: 0.00 s, system: 0.00 s, elapsed: 0.00 s
2026-10-19 15:30:49.189 UTC [36] LOG:  restored log file "000000060000000000000008" from archive
2026-10-19 15:30:49.206 UTC [36] LOG:  selected new timeline ID: 7
```

Not only the rows, but the table itself is gone, since it was created after the backup. Recovery replayed the archive to its end (`…08`), found nothing more and promoted. Two committed tx that sat on disk the whole time, lost by the restore that was supposed to recover them 🤯


## 3. Canary test #2: the textbook fix

Our research shows that the Postgres standard documentation's advice for archive recovery is to copy any unarchived segments out of `pg_wal` first and put them back afterwards. OK, simple. 

Same setup again (now on timeline 7, fresh backup `20261019-153227F`, archive end `…0A`): row A in `…0B` (`.ready`), row B in the open `…0C`. Crash, then this time save `pg_wal` first:

```
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest sh -c \
    'cp -a /var/lib/postgresql/data/pg_wal /var/lib/postgresql/pg_wal.saved && ls /var/lib/postgresql/pg_wal.saved | grep -v history'
000000070000000000000009
00000007000000000000000A
00000007000000000000000A.00000028.backup
00000007000000000000000B
00000007000000000000000C
archive_status
```

The same restore, then copy the segment files back (not overwriting anything the restore put there) and start:

```
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest sh -c \
    'cd /var/lib/postgresql/pg_wal.saved && for f in [0-9A-F]*; do case $f in *.*) continue;; esac; cp -n "$f" /var/lib/postgresql/data/pg_wal/; done'
[boris@ ~/helpdesk]$ docker restart helpdesk-test-pg-pgbackrest
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest psql -At -c "select * from canary"
1|A: ready segment
2|B: current segment
[boris@ ~/helpdesk]$ docker logs --since 1m helpdesk-test-pg-pgbackrest 2>&1 | grep -E "restored log file|redo done|selected new timeline|consistent|invalid record" | grep -v history
2026-10-19 15:33:23.556 UTC [37] LOG:  restored log file "00000007000000000000000A" from archive
2026-10-19 15:33:23.573 UTC [37] LOG:  consistent recovery state reached at 0/A000138
2026-10-19 15:33:23.577 UTC [37] LOG:  invalid record length at 0/C000118: expected at least 24, got 0
2026-10-19 15:33:23.577 UTC [37] LOG:  redo done at 0/C0000E0 system usage: CPU: user: 0.00 s, system: 0.00 s, elapsed: 0.00 s
2026-10-19 15:33:23.584 UTC [37] LOG:  selected new timeline ID: 8
```

Good, both rows survived. Recovery restored the archive to its end (`…0A`), then, with nothing more in the archive, read `…0B` and `…0C` from `pg_wal` and stopped at the first empty record. `invalid record length … got 0` is not an error here: it is what the end of WAL looks like.

Here's something to note: the log does *not* say: there is no line for `…0B` or `…0C` (remember that finding for section 6).


## 4. Surprise #1: the hole we just made

Recovered, done? Look at the archive after the restore:

```
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest ls /var/lib/postgresql/data/pg_wal/archive_status /var/lib/pgbackrest/archive/main/16-1/0000000700000000/
/var/lib/postgresql/data/pg_wal/archive_status:
00000007000000000000000C.partial.done
00000007.history.done
00000008.history.done

/var/lib/pgbackrest/archive/main/16-1/0000000700000000/:
000000070000000000000009-eeca02584aee3c5c598fd5898c907b0f86b21616.gz
00000007000000000000000A.00000028.backup
00000007000000000000000A-5d40307f46381d26f955d40391abbe0c47c32a57.gz
00000007000000000000000C.partial-8179a4ba3300083b31d6f7d1db330f2e0698262e.gz
```

At promotion Postgres archived the last segment of the old timeline, `…0C`, as `.partial`.... but nobody seem to archived `…0B`: it came back through `pg_wal` with no `.ready` and archiving was back on only for new WAL.   
  
Timeline 8 now forks inside `…0C`, so replaying it from backup `20261019-153227F` needs `…0A`, `…0B` and then timeline 8. The repo has no `…0B`. So unless we are missing something obvious here... the **next** restore from this backup would stop at `…0A` and lose row A again, plus everything written on timeline 8 and aiHelpDesk's own WAL-continuity check would (correctly) refuse it as `broken` 🤯

The fix we came up with has two halves: put the tail back *and* archive what was put back.   


(Here's how we repaired the above broken fixture by hand):

```
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest sh -c \
    'cd /var/lib/postgresql/data && cp /var/lib/postgresql/pg_wal.saved/00000007000000000000000B pg_wal/ && pgbackrest --stanza=main archive-push pg_wal/00000007000000000000000B && echo pushed'
pushed
```


## 5. Building it and why it's built this way

`restore_from_backup` now does the whole sequence itself (`agents/sysadmin/waltail.go`). Each choice below exists because of something above or something that would otherwise go wrong:

| Choice | Why |
|---|---|
| **Move** the segments aside, don't copy | No extra disk space, at the moment a disk may be the problem. |
| Save to a directory **beside** PGDATA, not inside | `--delta` cleans anything inside PGDATA that its manifest doesn't list. |
| `set -e`: if the save fails, **don't restore** | The restore must never silently go back to losing the tail. A refusal is visible, while a loss isn't. |
| Copy back **before** startup | Recovery reads `pg_wal` only when the archive runs out, during the same startup. |
| Count only segments whose **first page header matches their name** | Postgres pre-creates future segments by renaming old ones (the fixture's `…0B` from Oct 18 above). They hold old content. Copying one back is harmless (recovery rejects it like any end of WAL), but archiving one would put garbage in the repo under a real segment's name. aiHelpDesk already decodes that header for the WAL-continuity check (`parseWALLongHeader`). |
| Count only the server's **own timeline**, **contiguous** from the archive end | That's the WAL recovery will actually reach. Segments past a gap are never replayed, so they aren't predicted or pushed either. |
| **Push** every tail segment past the archive end **except the newest** | Section 4: otherwise the new timeline depends on WAL only this host has. The newest is the one Postgres archives itself as `.partial` at promotion. |
| **Keep** the saved directory | It's the only other copy if recovery doesn't go as predicted or if the push fails. |
| Report it all in `pg_wal_tail` (`saved_to`, `replay_through_wal`, `pushed_to_archive`) | A human (and the remediation playbook's FINDINGS) needs to see that commits were recovered and where the copies are. |

## 6. Surprise #2: Postgres throws away the `.ready` marker

The first version didn't push. Huh? It marked the tail segments `.ready` in `archive_status`, so Postgres's own archiver would ship them once the server was up. That's the conventional mechanism and it would retry by itself if archiving was still broken. The new integration test (section 8) failed:

```
--- FAIL: TestIntegration_RestoreFromBackup_KeepsUnarchivedWAL (110.36s)
    restore_from_backup_integration_test.go:304: archive not continuous onto the new timeline 60s after startup:
    &{Status:broken Detail:WAL segment 000000080000000000000011 on timeline 8 is missing: recovery would stop at
    000000080000000000000010, although timeline 8's history shows WAL past it ... (queued: [000000080000000000000011])
```

The marker was there before startup and the test even asserted it. So we repeated it by hand and watched `archive_status`:

```
--- after restore+queue:
/var/lib/postgresql/data/pg_wal:
0000000A0000000000000011
0000000A0000000000000012
0000000A0000000000000013
0000000A0000000000000014
archive_status

/var/lib/postgresql/data/pg_wal/archive_status:
0000000A0000000000000013.ready
--- 3s after start:
0000000A0000000000000014.partial.done
0000000A.history.done
0000000B.history.done
```

Gone within 3 seconds of startup. Recovery still replayed `…13` (the canary rows were back), but after promotion, with no status file left, Postgres treated the segment as already archived and deleted it:

```
[boris@ ~/helpdesk]$ docker exec -u postgres helpdesk-test-pg-pgbackrest sh -c 'cd /var/lib/postgresql/data && pgbackrest --stanza=main archive-push pg_wal/0000000A0000000000000013'
2026-10-19 17:23:47.325 P00  ERROR: [055]: unable to open missing file '/var/lib/postgresql/data/pg_wal/0000000A0000000000000013' for read
```

Not in `pg_wal`, not in the repo 🤯. The only copy left was the saved directory, which is why `restore_from_backup` keeps it.

So what we do now is that the tail is pushed with `pgbackrest archive-push`, before startup, by `restore_from_backup` itself. A side benefit: if archiving is still broken (which is often why there was a tail in the first place), the push fails *now* and the tool says so in `pg_wal_tail.detail`, with the path to the saved copies, instead of a marker quietly being dropped later.

(And one more, smaller, somewhat amusing self-inflicted lesson along the way: the test's cleanup first did `ALTER SYSTEM RESET archive_command`. On this fixture `archive_command` itself lives in `postgresql.auto.conf`, so RESET turned archiving *off*. The cleanup now sets it back explicitly.)

## 7. Surprise #3: the post-restore check couldn't see the tail

v0.32 also added `verify_restore_recovery`: after the restart it reads Postgres's log and checks that recovery ended where `restore_from_backup` predicted it would and then compared the last segment *restored from the archive*. Now the section 3 shows why that's not good enough anymore: Postgres logs nothing for segments it reads from `pg_wal`. A recovery that replayed the tail and one that ignored it produce the same `restored log file` lines 🤯

So our solution here is that the prediction now carries the tail's end (`replay_through_wal`) and the segment size and the check also looks at which segment `redo done at <LSN>` falls in. That's the one line in the log that says where replay really ended. Stopping short of the tail is now a `mismatch` ("recovery stopped early in the `pg_wal` tail"), while a `verified` result reports `replayed_through_wal`.

Then the push from section 6 broke the check, in a clear, instructive way:

```
--- FAIL: TestIntegration_RestoreFromBackup_KeepsUnarchivedWAL (49.33s)
    restore_from_backup_integration_test.go:288: verify_restore_recovery = {Status:mismatch Detail:recovery went past the predicted end:
    the last segment restored was 0000000B0000000000000018, expected 0000000B0000000000000017 ...}
```

Now, once `…18` is pushed before startup, recovery restores it *from the archive* (it always tries the archive first). The archive end moved because of our own push. The prediction now uses the archive end as it is after the push. A check that compares against reality has to be told about every change *we* made to reality.

## 8. Made permanent

`TestIntegration_RestoreFromBackup_KeepsUnarchivedWAL` is the canary test, run through the real `restore_from_backup` code against the Docker fixture. It breaks archiving, commits both rows, crashes, restores, restarts and asserts:

- both canary rows are back
- `verify_restore_recovery` reports `verified`, with `replayed_through_wal` equal to the tail the restore predicted
- the archive is `continuous` onto the new timeline (the WAL-continuity check, the same one that gates every restore)

```
=== RUN   TestIntegration_RestoreFromBackup_KeepsUnarchivedWAL
--- PASS: TestIntegration_RestoreFromBackup_KeepsUnarchivedWAL (49.28s)
PASS
```

And, since a test that can't fail proves nothing: with the old one-line restore script put back, it fails.

```
--- FAIL: TestIntegration_RestoreFromBackup_KeepsUnarchivedWAL (37.18s)
    restore_from_backup_integration_test.go:267: pg_wal_tail = <nil>, want a saved dir, WAL to replay past the archive end
    0000000F000000000000001D and row A's segment pushed to the archive
```

All seven [SysAdmin Agent](../SYSADMIN_AGENT.md)'s integration tests pass with the change, including the original crash-and-restore test and the test that checks the WAL-continuity prediction against Postgres's real recovery.

See [BACKUP.md](../BACKUP.md#the-unarchived-pg_wal-tail-commits-on-disk-but-not-in-the-archive) for the reference description of the feature.
