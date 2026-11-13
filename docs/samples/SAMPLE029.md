# aiHelpDesk Sample#29 (from source, on host/VM and K8s): Canary runs for counting the transactions a restore would throw away (exact data loss via `pg_waldump`).

This sample is similar to [Sample#24](SAMPLE024.md): there is no AI in it at all. No model chooses anything, no tokens are spent. The canary runs are plain postgres (or at least our understanding of it) and the three platform runs at the end call the SysAdmin Agent's `plan_point_in_time_recovery` tool directly with `curl`. What follows is what we expected, what the real WAL said instead and what we changed because of it.

The question came out of [v0.34 transcript](SAMPLE028.md). Before a PITR the plan says how much *history* it would discard ("27m 51s"), but a human deciding at the gate wants a number of a different kind (well, may be both): **how many transactions would be lost?** The former, time, gives a bound. It cannot give a count, because a quiet minute and a busy minute are not the same loss. The only place those transactions are written down exactly is the WAL and luckily `pg_waldump` can read it 😉.

Here is the skinny:

- **The timestamp in `pg_waldump`'s output is in the zone of whoever runs it, as an abbreviation** 🤯  
  The same three commits printed `EST` or `UTC` depending on the process's `TZ`. An abbreviation is not something you can parse safely (err.. because `EST` is also an Australian zone for instance), so we always run it with `TZ=UTC` and refuse to read any abbreviation we don't recognise.
- **A DDL commit line is 1245 bytes**  
  Invalidation messages make it far longer than a normal one (38 bytes for a plain commit on PG18). Any reader with a line limit or a `cut -c`, loses the timestamp at its start or breaks on the rest.
- **A WAL file in `pg_wal` can be junk**  
  Yeah, that also caught us by surprise. Postgres recycles old segments and names them for *future* segments. They hold no WAL. `pg_waldump` on one says "could not find a valid record". So the read must never go past the instance's own current position. Crikey 😥
- **The "error" at the end of a live segment is the normal end**  
  Unbounded, `pg_waldump` exits 1 with `invalid record length ... expected at least 24, got 0`. That one message appears to mean "the WAL stops here". Anything else is a real failure.
- **WAL belongs to a timeline**  
  Segments from before a timeline began don't exist on that timeline. A straight range across a restore fails.
- **A killed `pg_waldump` leaves a half-written last line**  
  It basically lost our "how it ended" marker... err, but luckily a test with a real script found.
- **Not every commit is in the WAL yet**  
  A transaction that took an xid, but wrote nothing else... commits asynchronously. Until the WAL writer flushes it, it is not there to count (which for our tests meant replacing `select txid_current()` with the real table inserts with autocommit).

Beyond the skinny, aka details:

---

## 1. The question and the plan

Postgres recovery stops at a target and everything after it is discarded. For a *time* target the rule is precise: recovery stops **before the first commit or abort record, in WAL order, whose timestamp is later than the target**. Everything after that record is lost, including a later commit that carries an earlier timestamp. For an *LSN* target it applies the first record whose start is at or after the LSN and then stops.

So the loss is the commits from that stopping point to the end of the WAL. To count them we need to:

1. find the segment the stopping point is in (the first archived segment that reached the repository at or after the target time),
2. decode from there to the end of what the instance has written,
3. apply the stop rule to what `pg_waldump` printed.

Step 2 has to run where the WAL is: inside a container, over SSH on a VM or through `kubectl exec` in a Pod. And it needs the right binary: the file format changes with every postgres major version, so `pg_waldump` must match the server's.

Before writing any of that, we ran a canary on the real VM to see what `pg_waldump` really does there.

## 2. Canary test#1: is the binary there and can it read a pgBackRest archive segment?

Run by hand on the Ubuntu VM (postgres 16.15, the pgBackRest repo from the v0.34 runs). Note what is *not* the case first:

```
### binary
-rwxr-xr-x 1 root root 105496 Aug 11 17:37 /usr/lib/postgresql/16/bin/pg_waldump
pg_waldump: not on PATH for postgres
pg_waldump (PostgreSQL) 16.15 (Ubuntu 16.15-1.pgdg24.04+2)
```

`pg_waldump` is in the server package, but at `/usr/lib/postgresql/16/bin`, which is not on the `postgres` user's `PATH`. The version matches the server (16.15), which is what we need. So the runner looks in `/usr/lib/postgresql/N/bin` (and `/usr/pgsql-N/bin` on RHEL, which we have not run) by the server's major version and only then on `PATH`.

`pgbackrest archive-get` works as the `postgres` user and gave us the newest archived segment as a file:

```
segment: 000000330000000000000042
archive-get exit: 0
-rw-r----- 1 postgres postgres 16777216 Nov 10 22:27 000000330000000000000042
```

The commit record, the first surprise (the long invalidation list is cut here with `…`):

```
rmgr: Transaction len (rec/tot):   1245/  1245, tx:        784, lsn: 0/420087A0, prev 0/42008768, desc: COMMIT 2026-11-09 12:18:25.702361 EST; rels: base/5/81924 base/5/81927 base/5/81928 base/5/81929; dropped stats: …; inval msgs: catcache 32 catcache 7 catcache 6 … relcache 81924 snapshot 2608
```

Two things in one line. It is **1245 bytes**, because this is the `DROP` of a table (the commit carries every invalidation). And the time reads `EST`.

The last probe in canary 1 was a segment straight from `pg_wal`, "second newest, so it is complete":

```
file: 000000330000000000000044
pg_waldump: error: could not find a valid record after 0/44000000
exit: 1
```

We took it for a read error at first. It is not: `…44` is a *recycled* file that postgres named for a segment it has not written yet and it holds nothing. Reading anything past the instance's current position is reading garbage, so the planner is never allowed to.

## 3. Canary test#2: the zone, the live segment, a directory of segments

Run on the VM the next day. It made three tiny commits a second apart and looked at the same records under the host's zone and with `TZ=UTC`:

```
-- host zone:
rmgr: Transaction len (rec/tot):     34/    34, tx:        785, lsn: 0/43000148, prev 0/43000110, desc: COMMIT 2026-11-10 23:40:24.510687 EST
-- TZ=UTC:
rmgr: Transaction len (rec/tot):     34/    34, tx:        785, lsn: 0/43000148, prev 0/43000110, desc: COMMIT 2026-11-11 04:40:24.510687 UTC
```

The clock on the VM at the time was `2026-11-11T04:40:24` UTC. With `TZ=UTC` the time matches the second. So every run sets `TZ=UTC` and the parser accepts `UTC` or a numeric offset only. Any other abbreviation is *counted* (the commit is real) but its time is not read and it is never guessed.

Then the live segment, bounded by the current LSN and not:

```
-- bounded (-e 0/430001F8): exit 0, lines: 3
-- unbounded: exit 1, lines: 3
stderr:
pg_waldump: error: error in WAL record at 0/430001D0: invalid record length at 0/430001F8: expected at least 24, got 0
```

Same three records either way. The only difference is the exit code and what looks like a failure is the *normal end of WAL*. That one message, "expected at least 24, got 0", is the only error we treat as a normal end. Everything else is a failed read. And because the instance's position is known (`pg_current_wal_lsn()`), the runner passes it as `-e` and gets exit 0.

The last part of canary 2 tried a directory of segments with `-p DIR START END` and failed, usefully:

```
archive-get 000000330000000000000040: exit 1
archive-get 000000330000000000000041: exit 1
archive-get 000000330000000000000042: exit 0
pg_waldump: error: could not locate WAL file "000000330000000000000040"
```

The repo's archive spans `0000002C…` to `…33…42`: **timelines**. This repository has been restored many times and the newest timeline `0x33` starts at segment `0x42`, so `…40` and `…41` do not exist on it. A read across a restore has to be planned one timeline at a time, from the timeline path the WAL-continuity check already computes. The first canary 2 attempt did not time the read over many segments for this reason and we still haven't measured a busy one (see the end).

## 4. What got built

The pieces, each with tests built from the real captures above (`agents/sysadmin/`):

- **A parser for the lines**, with the real captures as fixtures: `TestParseWALCommits_CapturedDDLCommitInTheHostsOwnZone`, `…CapturedLiveSegmentInUTC`, `…LinesFarLongerThanAnyBuffer`.
- **A planner for what to read:** the start segment from the repository arrival times, per-timeline ranges, a cap of 16 segments after which the answer says **"at least N"** instead of pretending it is exact.
- **The stop rule**, applied to what was read (`TestCountDiscarded_TimeTarget` and the LSN variant).
- **A script** that runs through the same path as every other maintenance command (`docker exec`, `kubectl exec`, `sh -c` over SSH or locally), finds the binary by `PG_VERSION`, fetches the segments into a temporary directory, runs `pg_waldump` with `TZ=UTC` and a bound and prints markers that say how each range ended.
- **A kill switch**, `HELPDESK_PITR_COUNT_TRANSACTIONS=off` and a hard rule that a count that cannot be made says why and is never "nothing lost".

The plan now says, for example: *"5 transactions committed after the target would be discarded (counted from the WAL)"* and the decision record keeps the count, the basis and the decoder's version, hashed with the rest of the record (see [AUDIT.md](../AUDIT.md)).

## 5. What the tests found that the canaries did not

**The marker that was lost**   
The test that runs the real generated script with a fake `pg_waldump` killed it mid-line with `timeout`. A killed process leaves no trailing newline, so the script's `range_rc` marker was glued to the end of the last line and the reader never saw it. A range that timed out looked like one that had no result. The script now writes a newline before the marker and the test pins it.

**The record that wasn't written yet**   
The integration test against the real WAL (`TestIntegration_CountsTransactionsFromRealWAL`, against the pgBackRest fixture: real `pg_waldump`, real `archive-get`) first used `SELECT txid_current()` to make commits and was flaky. That statement takes an xid but writes no other WAL, so the commit is **asynchronous**, even with `synchronous_commit=on`: the record may not be flushed when `pg_current_wal_lsn()` is read. The test now writes a row. The behaviour is a real property of the count: only written WAL is counted. It is documented in [PITR.md](../PITR.md#4-operating-it). It stayed stable over eight consecutive runs.

**A rename that would have read as zero**   
The parser counted `COMMIT` and `ABORT` lines. If a future Postgres renamed one of those descriptions, it would have found no commits and reported an **exact zero**: the worst wrong answer. Now any Transaction record that is neither understood nor in the short list of known harmless kinds (`ASSIGNMENT`, `INVALIDATIONS`, `PREPARE`) is counted as *unread* and a read that saw records but understood none is reported as unavailable (`TestParseWALCommits_AnUnfamiliarDescriptionIsNotTakenForNothing`).

## 6. How stable is the format we read?

A fair question. We started with the Postgres 16 on Linux, but two different things can change:

- the **WAL file format**, which changes with every major version. That is why the binary must match the server's major (the script picks it by `PG_VERSION`). A mismatch is a failed read that says so, never a wrong count.
- the **text `pg_waldump` prints**, which is all we parse.

Next we compared the text on the one other server we had at hand, a Postgres 18.6 in the Docker container that holds the audit store (read-only, nothing written). It printed the same line layout, real `ABORT` records, `UTC` under `TZ=UTC` and the same bounded/unbounded behaviour. Compare the 18.6 lines (`desc: COMMIT 2026-11-09 15:06:34.941088 UTC`, 38 bytes) with 16.15's above. The capture is kept as a fixture (`TestParseWALCommits_CapturedPG18`). So the text was identical across 16.14 (Debian), 16.15 (Ubuntu) and 18.6 (Debian). An no, we have **not** compared 13, 14 or 15 - anybody? (We did run 17.7 in a container later: section 7.)

## 7. The same plan on three platforms

The runs below are `plan_point_in_time_recovery` called directly on the SysAdmin Agent's `/tool` endpoint with `curl`, read-only: nothing is restored. On each target we made five commits *after* a target time, then asked for the plan for that time, so the right answer is **5**.

On the **Ubuntu VM** (the in-cluster agent reaching it over SSH), the plan reported:

```json
{
  "status": "feasible",
  "data_loss_estimate": {
    "summary": "27m 51s of history discarded, up to the running instance's position at 2026-11-11T17:52:23Z; it grows until the instance is stopped. 5 transactions committed after the target would be discarded (counted from the WAL).",
    "transactions": {
      "status": "measured",
      "commits": 5,
      "first_commit_at": "2026-11-11T17:25:25Z",
      "last_commit_at": "2026-11-11T17:25:25Z",
      "first_commit_lsn": "0/45019588",
      "last_commit_lsn": "0/45019728",
      "segments_read": 3,
      "read_millis": 181,
      "decoder": "pg_waldump (PostgreSQL) 16.15 (Ubuntu 16.15-1.pgdg24.04+2)"
    }
  }
}
```

(The `basis` field, which explains in a sentence how the count was made, is left out here.) The target was `2026-11-11T17:24:32Z`; the five inserts ran just after it. The independent check is the LSN: the five commits end at `0/45019728` and the `pg_switch_wal()` the VM prep ran right after the inserts had returned a position just past it (`0/450197A0`).


On a **K8s Pod** (`pgbackrest-demo-0`, `kubectl exec`, Postgres 16.14 on a Debian image), the same procedure:

```
TARGET=2026-11-11T19:08:21Z
...
$K su postgres -c 'psql -d testdb -Atc "select pg_switch_wal()"'
0/9017720
```

```json
"transactions": {
  "status": "measured",
  "commits": 5,
  "first_commit_at": "2026-11-11T19:08:37Z",
  "last_commit_at": "2026-11-11T19:08:37Z",
  "first_commit_lsn": "0/90174D0",
  "last_commit_lsn": "0/90176A8",
  "segments_read": 2,
  "read_millis": 1167,
  "decoder": "pg_waldump (PostgreSQL) 16.14 (Debian 16.14-1.pgdg13+1)"
}
```

Five commits again and the last one at `0/90176A8`, just before the `0/9017720` the switch returned. And on the **Docker fixture**, the integration test above reads real segments through `docker exec` and gets the same: commits before the target are read and not counted, the ones after are and commits that exist only in the running instance's `pg_wal` (never archived) are counted by the live read and not by an archive-only one.

| Platform | `pg_waldump` | Segments | Read | Count |
|---|---|---|---|---|
| Docker fixture (integration test) | 16, Debian | 1–2 | 0.1–0.6 s, nearly empty fixture | exact |
| Ubuntu VM over SSH | 16.15 | 3 | 181 ms | 5, exact |
| K8s Pod through `kubectl exec` | 16.14 | 2 | 1167 ms | 5, exact |
| Docker container (AlloyDB Omni) through the Compose stack | 17.7 | 2 | 214 ms | 5, exact |

The last row is the first time the count ran on PostgreSQL 17. The container had no archiving, so for the test we added a pgBackRest config, created a stanza, switched `archive_mode` on (a restart) and took a full backup. Then the same five commits after a target time (`14:07:23Z`, commits at `14:08:09Z`) and the plan: `feasible`, `decoder: pg_waldump (PostgreSQL) 17.7`, the five commit LSNs `0/36024480` to `0/36024640` just before the `0/360246B8` the switch returned. Exact, but we did not diff 17's raw lines against 16's the way we did for 18.6, so this says the parser handled 17.7, not that every line is identical.

We did not investigate why the Pod's read was slower (`kubectl exec`, an image on another node and the storage are all candidates).

## 8. What this does (and doesn't) tell you?

- **Exact for written WAL, from a serving instance read to its own position** 
  If the instance is down or the read hit the 16-segment cap, the answer is "at least N" and it says which.

- **Informational**   
  Enforcement of the RPO still goes by time. The count sits next to it in the plan, in the decision record and in `faulttest vault incidents` and never refuses anything on its own.

- **A commit is not a row and not a person's work**   
  Well, that's obvious, but worth stating for the record: a thousand commits can be one batch job. It tells you how many transactions, not how much they mattered. That part is up to you.

- **Not run**   
  A prepared transaction's commit line (the reader handles the form from Postgres source, not from a capture), RHEL's `/usr/pgsql-N/bin`, Postgres 13/14/15/17, an operator-managed or cloud database (no binary to run, so the plan says it did not count) and how long a read takes over many *busy* segments: every number above is a quiet server.

See [PITR.md](../PITR.md#4-operating-it) for the reference description, [OBJECTIVES.md](../OBJECTIVES.md#63-what-this-depends-on-and-what-could-mislead) for where it sits next to the objectives and [PLATFORMS.md](../PLATFORMS.md) for what the count needs on each platform.
