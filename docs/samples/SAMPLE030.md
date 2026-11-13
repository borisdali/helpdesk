# aiHelpDesk Sample#30 (from source, on host/VM): What a restore would cost, in downtime and in data, before anyone approves it (availability budget, ledger, recovery-time estimate)

This sample is similar to [Sample#29](SAMPLE029.md): there is no AI in it. The plan is `plan_point_in_time_recovery` called directly on the SysAdmin Agent with `curl` (read-only, nothing is restored) and the ledger is read from the Gateway. What follows is what we declared, what came back and, as importantly, what this run could not show.

Here is the skinny:

- One declaration (`availability_slo: 99.9` over `30d`) turns into a downtime **budget** of 43m12s. The same declaration plus the `rto` says how many worst-case recoveries fit in it: **one**.
- A restore plan now carries four answers side by side: can it be done, how much data it throws away (down to a transaction count), whether the declared `rpo` is exceeded and whether the budget can afford the downtime.
- The **ledger** shows how much of the budget recorded incidents have already used. Here: nothing. Four read-only checks ran against this server and cost it nothing, because they are classified as having no availability impact.
- The recovery-time **estimate** has no history to learn from, says so and falls back to the declared `rto` instead of inventing a number.
- **Not shown:** a charged outage and an estimate built from real restores. Both need real restore incidents. They are covered by unit and contract tests only.

## 1. The declaration (`infra.json` inventory)

The database server entry in the local infra file (the `environment` is `prod`, which supplies `rto: 30m`):

```
[boris@ ~/helpdesk]$ grep -m1 availability_slo -B1 infrastructure.json
      "environment": "prod",
      "objectives": { "rpo": "1s", "enforcement": "acknowledge", "availability_slo": 99.9, "availability_window": "30d" }

-- no RTO at the db_server level, but it comes from the "environment": "prod"

[boris@ ~/helpdesk]$ grep -A6 environments infrastructure.json
  "environments": {
    "prod": {
      "description": "demo",
      "objectives": {
        "rpo": "15m",
        "rto": "30m"
      }
```

Objectives resolve per field, database server first, then env, then the defaults ([OBJECTIVES.md](../OBJECTIVES.md#5-reading-it-in-code)). Here `rpo`, `enforcement` and the availability fields come from the server, `rto` from `prod`. The Gateway and the SysAdmin Agent were restarted after the edit, since both read the file at startup.

## 2. The plan

A restore target five minutes in the past, on the live VM:

```bash
[boris@ ~/helpdesk]$ TARGET=$(date -u -v-5M +%Y-%m-%dT%H:%M:%SZ)     # 2026-11-12T00:45:31Z
[boris@ ~/helpdesk]$ curl -s -X POST "$HELPDESK_SYSADMIN_URL/tool/plan_point_in_time_recovery" \
  -H "Authorization: Bearer $HELPDESK_SYSADMIN_KEY" -H "Content-Type: application/json" \
  -d '{"trace_id":"tr_budget_demo2","purpose":"diagnostic","args":{"target":"pgbackrest-ubuntu-vm","stanza":"main",
       "recovery_target_type":"time","recovery_target_value":"'$TARGET'"}}' | jq -r .output | jq .
```

The fields that matter, trimmed from the full output (`…` marks what was cut):

```json
{
  "status": "feasible",
  "detail": "backup 20261111-121609F can be restored and the WAL from it to the end of the archive is continuous. The instance is serving and the target is in WAL it has not archived yet: the restore flushes the archive first (a WAL switch) and then checks this plan again, … Restoring stops the running instance, which the gate must allow",
  "backup_label": "20261111-121609F",
  "data_loss_estimate": {
    "basis": "live_instance",
    "window_seconds": 315,
    "transactions": { "status": "measured", "commits": 0, "segments_read": 1, "read_millis": 106 },
    "summary": "5m 15s of history discarded, up to the running instance's position at 2026-11-12T00:50:46Z; it grows until the instance is stopped. 0 transactions committed after the target would be discarded (counted from the WAL).",
    "objective": {
      "status": "exceeds",
      "detail": "5m 15s discarded against an RPO of 1s (database): exceeds it by 5m 14s",
      "gate": "acknowledge"
    }
  },
  "availability_budget": {
    "status": "within",
    "detail": "0s of the 43m12s budget over 30d already spent, 43m12s left: enough for a restore that takes up to the rto of 30m",
    "budget_seconds": 2592,
    "remaining_seconds": 2592,
    "duration_seconds": 1800,
    "duration_basis": "declared_rto",
    "enforcement": "warn",
    "recovery_estimate": {
      "status": "no_history",
      "samples": 0,
      "basis": "no resolved restore of this server in the last 90d among real incidents: nothing to estimate from"
    }
  },
  "plan_digest": "39aab49f0b84df32"
}
```

Reading it:

- **Data**  
  5m 15s of history would be discarded, up to the instance's position when the plan was made and that grows until the instance is stopped. The transaction count is **0**, because nothing was committed after the target here, but see the five-commit case is in [Sample#29](SAMPLE029.md).
- **`rpo`**   
  The declared `rpo` of 1s is exceeded by 5m 14s, so the data-loss gate is `acknowledge`: a person has to accept that before a restore (A 1s `rpo` is deliberately tight, so the demo shows the gate).

- **Budget**   
  `within`: the budget is 2592 s (43m12s), nothing is spent and a restore as long as the whole `rto` of 30m fits.

- **The budget's enforcement is its own setting**   
  It says `warn` although the server declares `acknowledge`: that `acknowledge` is the `rpo` enforcement. The budget has `availability_enforcement`, which defaults to `warn`, so a breach of the budget would be reported but would not stop a restore until that is set.

- **The estimate has nothing to go on**   
  With no resolved restore of this server in the last 90 days among real incidents it says `no_history` and the plan uses the declared `rto` (`duration_basis: declared_rto`). It does not guess.

## 3. The ledger

The same server, read as text:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault budget pgbackrest-ubuntu-vm \
  --gateway $HELPDESK_GATEWAY_URL 
  --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: dev  ·  host: <your host>

Availability budget: pgbackrest-ubuntu-vm
  Window   30d [database], as of 2026-11-12 01:22 UTC
  Budget   availability 99.9% over 30d allows 43m12s of downtime per window; rto 30m uses 69% of it: tolerates 1 recovery per window at the rto
  Spent    0s in 0 outage incident(s), 0% of the budget
  Left     43m 12s

Not charged: 4 no availability impact (1m 03s)

Duration is from detection to recovery: each incident runs from the start of its entry playbook run to the end of its last run, so it includes the wait for approvals and begins when aiHelpDesk was engaged, not when the failure began.
```

The same data comes from `GET /api/v1/availability-ledger?database=pgbackrest-ubuntu-vm`, where the four are listed individually:

```bash
[boris@ ~/helpdesk]$ curl -s "$HELPDESK_GATEWAY_URL/api/v1/availability-ledger?database=pgbackrest-ubuntu-vm" \
  -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" \
  | jq -r '"window \(.window_seconds/86400)d  budget \(.budget_seconds)s  spent \(.charged_seconds)s  left \(.remaining_seconds)s",
	"\(.budget_detail)",
	(.none[] | "none: \(.incident_id)  \(.status)  \(.duration_seconds)s  \(.detected_at)")'

window 30d  budget 2592s  spent 0s  left 2592s
availability 99.9% over 30d allows 43m12s of downtime per window; rto 30m uses 69% of it: tolerates 1 recovery per window at the rto
none: inc_e93edc0b  needs_human  20s  2026-11-11T22:01:46Z
none: inc_4274974a  needs_human  16s  2026-11-11T22:20:15Z
none: inc_4c6dd1aa  needs_human  14s  2026-11-11T22:28:18Z
none: inc_1957703f  needs_human  13s  2026-11-11T22:46:11Z
```

Those four incidents are runs of the R/O [objective-exposure](../OBJECTIVES.md#56-is-the-backup-posture-enough-for-the-objectives) check against this server, the real ones from building it. Each was recorded `needs_human` (the archive was older than the 1s `rpo`) and each took a few seconds. They sit in the **not charged** list because that playbook is declared to have no [availability impact](../OBJECTIVES.md#51-which-incidents-spend-the-availability-budget). Without that declaration, four open `needs_human` incidents would have shown up as unconfirmed outages against the budget.

## 4. What this run did not show

- **A charged outage.** The ledger has no outage incident, so the `Spent`, `Left` and `unconfirmed` lines have no live example here. Charging needs a real outage incident, which means a real restore through the Gateway and the failure injetion tests are deliberately excluded to avoid tainting the budget (so a `faulttest` injection never counts).

- **An estimate from real restores**   
  It needs at least three resolved restores of the same server from real incidents. This server has none, so only the `no_history` fallback ran.

- **The budget as a gate**   
  With `warn` nothing blocks a restore. The path where an exhausted budget requires an acknowledgement was not run live.

Each of these is covered by unit and integration tests (see [testing pyramid](../../testing/README.md#summary-aihelpdesk-testing-pyramid), which is a weaker kind of evidence than a run, but the list above is what to look for in the real restore.

## 5. Where to go from here

- The model: [OBJECTIVES.md §5](../OBJECTIVES.md#5-reading-it-in-code), with the budget ([§5.2](../OBJECTIVES.md#52-the-availability-budget)), the ledger ([§5.3](../OBJECTIVES.md#53-the-availability-ledger)) and the estimate ([§5.5](../OBJECTIVES.md#55-the-recovery-time-estimate)).
- Running the plan: [PITR.md](../PITR.md#4-operating-it).
- The transaction count: [Sample#29](SAMPLE029.md).
