# aiHelpDesk Sample#23 (on Host/VM): Restore / Recovery time!

The raw transcript of the sample commands and deliberations presented below complements this blog post:

- **[Your DBA’s Scariest Command Now Runs Itself](https://itnext.io/your-dbas-scariest-command-now-runs-itself-e4c6d161d590)**  
  rm -rf $PGDATA, anyone? Letting AI run the fully automated in-place restore was the one line we didn’t let AI cross. Until now.
  
  
If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel).

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This sample transcript for databases running directly on a host/VM/Bare Metal. See [here](SAMPLE022.md), [here](SAMPLE021.md) and [here](SAMPLE019.md) for K8s, the source and container (Docker/Podman) sample transcripts respectively.

This sample transcript below is meant to showcase an automated restore of the [`db-pgdata-corrupted`](../BACKUP.md#3-restore-from-backup-after-data-loss) fault that simulates a failed database that requires a restore and recovery:

---

## Fault Injection Test for deleted `pg_control` file

First off, if you don't have a fresh backup (you absolutely should!), let's take a fresh one (this is a test system, no regular scheduled backups, only on demand):

```
[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64]$ ssh -i ./pgbackrest_test_client_key postgres@pgbackrest-vm.test 'pgbackrest --stanza=main --type=full backup'
[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64]$ echo $?
0
```

... and kick off the failure injection test that `db-pg-data-corrupted` fault (see [here](../BACKUP.md#3-restore-from-backup-after-data-loss) for more details):

```
[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64]$ date; time ./faulttest run \
   --ids db-pgdata-corrupted-vm \
   --remediate \
   --via-gateway \
   --gateway $HELPDESK_GATEWAY_URL \
   --api-key $HELPDESK_API_KEY \
   --infra-config ~/infrastructure.json \
   --conn "host=pgbackrest-vm.test port=5432 dbname=postgres user=postgres password=xxx" \
   --agent-conn pgbackrest-ubuntu-vm \
   --ssh-host postgres@pgbackrest-vm.test \
   --ssh-port 22 \
   --ssh-key ./pgbackrest_test_client_key \
   --approval-mode force \
   --gate-escalation \
   --operator alice@example.com \
   --users-file users.example.yaml \
   --judge \
   --judge-vendor anthropic \
   --judge-model $HELPDESK_MODEL_NAME \
   --judge-api-key $HELPDESK_VENDOR_API_KEY \
   --external=false

Wed Oct 14 22:40:57 EDT 2026
time=2026-10-14T22:40:57.739-04:00 level=INFO msg=--conn host=pgbackrest-vm.test
time=2026-10-14T22:40:57.740-04:00 level=INFO msg=--agent-conn alias=pgbackrest-ubuntu-vm host=pgbackrest-vm.test
time=2026-10-14T22:40:57.743-04:00 level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
time=2026-10-14T22:40:57.743-04:00 level=INFO msg="LLM diagnosis judge enabled" vendor=anthropic model=claude-haiku-4-5-20251001

--- Testing: Data directory corrupted — restore from latest backup (real host/VM) (db-pgdata-corrupted-vm) ---
time=2026-10-14T22:40:57.743-04:00 level=INFO msg="executing injection spec" type=ssh_exec phase=inject
time=2026-10-14T22:40:58.459-04:00 level=INFO msg="ssh_exec completed" target=postgres@pgbackrest-vm.test output="Job for postgresql@16-main.service failed because the service did not take the steps required by its unit configuration.\nSee   \"systemctl status postgresql@16-main.service\" and \"journalctl -xeu postgresql@16-main.service\" for details."
time=2026-10-14T22:40:59.003-04:00 level=INFO msg="sending prompt to agent via playbook" failure=db-pgdata-corrupted-vm series_id=pbs_db_data_loss_triage playbook_id=pb_c656d062 gateway=http://localhost:8080 agent-conn="host=pgbackrest-vm.   test port=5432 dbname=postgres user=postgres password=xxx"
time=2026-10-14T22:41:09.471-04:00 level=WARN msg="gateway warning" failure=db-pgdata-corrupted-vm warning="expected evidence pattern not found in provided context: \"PANIC.*could not locate a valid checkpoint\""
time=2026-10-14T22:41:09.472-04:00 level=WARN msg="gateway warning" failure=db-pgdata-corrupted-vm warning="expected evidence pattern not found in provided context: \"database files are incompatible with server\""
time=2026-10-14T22:41:09.472-04:00 level=WARN msg="gateway warning" failure=db-pgdata-corrupted-vm warning="expected evidence pattern not found in provided context: \"invalid page.*could not read block\""
time=2026-10-14T22:41:09.472-04:00 level=WARN msg="gateway warning" failure=db-pgdata-corrupted-vm warning="expected evidence pattern not found in provided context: \"could not find the database system\""
time=2026-10-14T22:41:09.473-04:00 level=WARN msg="tool evidence using text-based detection; structured tool call data unavailable" failure=db-pgdata-corrupted-vm reason="agent did not emit tool_call_summary DataPart (gateway path or non-ADK agent)"
time=2026-10-14T22:41:11.264-04:00 level=INFO msg="gate pending: operator review required" failure=db-pgdata-corrupted-vm run_id=plr_d0ae3c05 escalation_target=pbs_sysadmin_host_triage

════════════════════════════════════════════════════════════════
  INFORMED GATE — review before remediation
════════════════════════════════════════════════════════════════

  Next playbook     : pbs_sysadmin_host_triage
  Findings          : single_instance=true; backup_tool=pgbackrest; pitr_decided=false
  Remediation plan  : Sysadmin — Host/Container Inspection (manual approval)
                      Inspect a PostgreSQL instance reported as unreachable, whether it runs
                      in a Docker/Podman container, under systemd directly on a host/VM, or
                      (handed off further) in a Kubernetes pod. Determine whether it exited
                      cleanly (manual stop / graceful shutdown) or crashed (OOM kill, panic,
                      unexpected exit), revise the prior DB agent's root cause hypothesis if
                      necessary, and provide evidence-based remediation guidance.

time=2026-10-14T22:41:11.265-04:00 level=INFO msg="force-approving gate" run_id=plr_d0ae3c05
time=2026-10-14T22:42:26.535-04:00 level=INFO msg="waitForChildRunComplete: watching child run" child=plr_6f576534
time=2026-10-14T22:42:26.928-04:00 level=INFO msg="waitForChildRunComplete: child run hit a second gate mid-chain, force-approving" child=plr_6f576534
time=2026-10-14T22:42:43.452-04:00 level=INFO msg="waitForChildRunComplete: watching child run" child=plr_b8006df1
time=2026-10-14T22:42:43.909-04:00 level=INFO msg="waitForChildRunComplete: child run hit a second gate mid-chain, force-approving" child=plr_b8006df1
time=2026-10-14T22:43:17.293-04:00 level=INFO msg="waitForChildRunComplete: watching child run" child=plr_e07a89cf
time=2026-10-14T22:43:17.393-04:00 level=INFO msg="waitForChildRunComplete: child run complete" child=plr_e07a89cf outcome=resolved

  Post-incident feedback (optional, but recommended):
    Was the triage diagnosis correct? [y/n/skip]: y
    Root cause: not specified?
  Feedback submitted (triage/post_incident run_id=plr_d0ae3c05)
    Was the remediation approach appropriate? [y/n/skip]: y
    Remediation approach notes (optional): lgtm, looks like a reasonable fix
  Feedback submitted (remediation/post_incident run_id=plr_d0ae3c05)
  Feedback submitted (remediation/post_incident run_id=plr_e07a89cf)
Remediation: RECOVERED in 0.1s (score: 100%)
Incident plr_d0ae3c05 — resolved in 0.1s
  Remediation: pbs_sysadmin_host_triage
  Narrative  : GET http://localhost:8080/api/v1/incidents/plr_d0ae3c05
Vault: draft saved → pb_a1a6095b (activate with 'faulttest vault list')
  Feedback submitted (triage/post_incident run_id=plr_d0ae3c05)
time=2026-10-14T23:06:23.485-04:00 level=INFO msg="executing injection spec" type=ssh_exec phase=teardown
time=2026-10-14T23:06:26.392-04:00 level=INFO msg="ssh_exec completed" target=postgres@pgbackrest-vm.test output=""
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% judge=100%)
Remediation Result:  [PASS] score=100% (0.1s, playbook)
Overall Result:      [PASS] score=100%

=== Fault Test Report: a98bfc91 ===

[PASS] Data directory corrupted — restore from latest backup (real host/VM) (db-pgdata-corrupted-vm) - score: 100% [tool evidence: text match] [judge: 100%]
       Keywords: 100% | Tools: 100% | Judge: 100%
       Remediation: 100% (0.1s, playbook) | Overall: 100%
       Reasoning: "Agent correctly identifies systemd hosting context, recognizes inability to read systemd journal directly, properly escalates to sysadmin agent per playbook Step 1, avoids premature connection attempt, and explicitly       acknowledges the need for sysadmin's read_pg_log_file capability to confirm corruption signature before proceeding to pgBackRest triage—demonstrating full understanding of the expected diagnosis chain for real systemd targets."

--- Summary ---
Total: 1 | Passed: 1 | Failed: 0 | Rate: 100%
  database: 1/1 (100%)

Note: 1 fault(s) used text-based tool evidence scoring (agent did not emit
structured tool call data). These scores may be less reliable. For precise
tool evidence, point --db-agent directly at the agent A2A URL rather than
the gateway, or use an ADK-based agent.

LLM judge scored diagnosis for 1 fault(s). Weights: tool*0.40 + judge*0.40 + keyword*0.20.

Report written to ./faulttest-a98bfc91.json
--- BEGIN FAULTTEST REPORT JSON ---
{
  "id": "a98bfc91",
  "timestamp": "2026-10-15T03:06:26Z",
  "results": [
    {
      "failure_id": "db-pgdata-corrupted-vm",
      "failure_name": "Data directory corrupted — restore from latest backup (real host/VM)",
      "category": "database",
      "score": 1,
      "passed": true,
      "run_id": "plr_d0ae3c05",
      "keyword_pass": true,
      "diagnosis_pass": true,
      "tool_evidence": true,
      "ordering_pass": true,
      "tool_evidence_mode": "text_fallback",
      "response_text": "I'm beginning a triage of the unavailable PostgreSQL instance at pgbackrest-vm.test:5432. Following the playbook, I'll first confirm the failure type and scope from infrastructure context before attempting any         connections.\n\n## Step 1 — Confirm Failure Type and Scope (No Connection Attempt Yet)\n\nExamining the Known Infrastructure:\n- **Server**: `pgbackrest-ubuntu-vm (pgBackRest Real Ubuntu VM)` \n- **Hosting**: VM: Local Machine\n- **          Service**: `systemd: postgresql@16-main`\n- **Connection string**: `pgbackrest-vm.test:5432/postgres`\n\nFrom the infrastructure context alone, I can determine:\n- **single_instance**: TRUE — this is a standalone PostgreSQL instance with no  replicas listed in the Known Infrastructure section.\n- **backup_tool**: pgBackRest (the VM is explicitly named \"pgBackRest Real Ubuntu VM\" and the context indicates pgBackRest tooling).\n- **pitr_decided**: FALSE — this is the first pass; no arbitrary point-in-time restore has been decided yet.\n\n**Critical limitation**: This server is managed by **systemd on a bare Ubuntu VM**, not Kubernetes. I have no tool of my own to read systemd journal logs, container logs, or service diagnostics. The `get_pod_logs` function is K8s-only and will not work here. To confirm the actual failure signature (corruption vs. crash vs. WAL loss), I require the sysadmin agent that can read the systemd journal and service logs         directly.\n\nPer the playbook's Step 1 final instruction: **\"Do NOT attempt to connect to the database yourself before emitting this hand-off\"** and **\"Even if a connection attempt already failed or timed out... the FINDINGS + ESCALATE_TO hand-off above is still the entire correct response.\"**\n\n---",
      "duration": "11.012123s",
      "keyword_score": 1,
      "tool_score": 1,
      "diagnosis_score": 1,
      "judge_reasoning": "Agent correctly identifies systemd hosting context, recognizes inability to read systemd journal directly, properly escalates to sysadmin agent per playbook Step 1, avoids premature connection attempt, and           explicitly acknowledges the need for sysadmin's read_pg_log_file capability to confirm corruption signature before proceeding to pgBackRest triage—demonstrating full understanding of the expected diagnosis chain for real systemd targets.",
      "judge_model": "claude-haiku-4-5-20251001",
      "gateway_warnings": [
        "expected evidence pattern not found in provided context: \"PANIC.*could not locate a valid checkpoint\"",
        "expected evidence pattern not found in provided context: \"database files are incompatible with server\"",
        "expected evidence pattern not found in provided context: \"invalid page.*could not read block\"",
        "expected evidence pattern not found in provided context: \"could not find the database system\""
      ],
      "remediation_attempted": true,
      "remediation_passed": true,
      "recovery_time_seconds": 0.106366208,
      "remediation_score": 1,
      "remediation_method": "playbook",
      "overall_score": 1
    }
  ],
  "summary": {
    "total": 1,
    "passed": 1,
    "failed": 0,
    "pass_rate": 1,
    "categories": {
      "database": {
        "total": 1,
        "passed": 1,
        "rate": 1
      }
    }
  }
}
--- END FAULTTEST REPORT JSON ---
Evaluation scores posted to auditd: 1/1 runs

real    25m29.311s
user    0m0.108s
sys     0m0.177s
```

Let's get the summary of the run first:

```
[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64]$ ./faulttest vault incidents \
   plr_d0ae3c05 \
   --summary \
   --gateway $HELPDESK_GATEWAY_URL \
   --api-key $HELPDESK_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.31.0-bfec14e  ·  host: <your-host>

INCIDENT plr_d0ae3c05 — resolved in 138s

1. TRIAGE        pbs_db_data_loss_triage → pbs_sysadmin_host_triage
   single_instance=true; backup_tool=pgbackrest; pitr_decided=false
2. ESCALATION 1  pbs_sysadmin_host_triage → pbs_pgbackrest_health_triage  [plr_6f576534]
   The database cannot start because pg_control is missing from the data directory; logs show repeated PANIC on …
3. ESCALATION 2  pbs_pgbackrest_health_triage → pbs_pgbackrest_restore_remediate  [plr_b8006df1]
   pgBackRest stanza health confirmed: status_code=0, backup_stale=false, last_backup_label=20261014-223603F, la…
4. REMEDIATION   pbs_pgbackrest_restore_remediate (resolved)  [plr_e07a89cf]
   The pgBackRest restore from backup label 20261014-223603F (timestamp 2026-10-15T02:36:05Z) completed successf…

Overall: 100% (diagnosis 100% · remediation 100%)
```

Nice, all four hops as expected! Let's get the full narrative (just drop the `--summary` bit):

```
[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64]$ ./faulttest vault incidents \
   plr_d0ae3c05 \
   --gateway $HELPDESK_GATEWAY_URL \
   --api-key $HELPDESK_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.31.0-bfec14e  ·  host: <your-host>

════════════════════════════════════════════════════════════
INCIDENT plr_d0ae3c05
Started: 2026-10-15 02:40 UTC   Duration: 138s
Operator: alice@example.com
Origin: faulttest   Status: resolved
Attribution: UNKNOWN
Bundle: incident-d1c5a995-20261014-224116.tar.gz
Draft playbook: pb_a1a6095b
════════════════════════════════════════════════════════════

── TRIAGE
Playbook:  pbs_db_data_loss_triage
Findings:  single_instance=true; backup_tool=pgbackrest; pitr_decided=false

── GATE
Decision:  approved by alice@example.com  at 02:41 UTC

── ESCALATION 1/2
Playbook:  pbs_sysadmin_host_triage   Outcome: transitioned
Transitioned to: pbs_pgbackrest_health_triage (still a diagnosis hop, not yet remediation)
Findings:  The database cannot start because pg_control is missing from the data
           directory; logs show repeated PANIC on missing pg_control during
           shutdown, followed by failed start attempts with the same error; this
           qualifies for pgBackRest restore since single_instance=true,
           backup_tool=pgbackrest, and no deeper damage is evident.
           ⚠ [Layer 3] unverified evidence (non-blocking) — Missing pg_control file in data directory — database requires restore from pgBackRest backup — postgres: could not open file

Hypotheses:
  [PRIMARY  98%] Missing pg_control file in data directory — database requires restore from pgBackRest backup
  [REJECTED  2%] Data directory deleted by external process during testing

── ESCALATION 2/2
Playbook:  pbs_pgbackrest_health_triage   Outcome: transitioned
Transitioned to: pbs_pgbackrest_restore_remediate (still a diagnosis hop, not yet remediation)
Findings:  pgBackRest stanza health confirmed: status_code=0, backup_stale=false,
           last_backup_label=20261014-223603F,
           last_backup_time=2026-10-15T02:36:05Z; prior triage identified
           pg_control missing from data directory as data-loss candidate
           requiring restore from this healthy backup.
           ⚠ [Layer 3] unverified evidence (non-blocking) — pgBackRest backup repository is healthy and contains a recent, non-stale full backup suitable for restore remediation — , last_backup_time=

Hypotheses:
  [PRIMARY  95%] pgBackRest backup repository is healthy and contains a recent, non-stale full backup suitable for restore remediation

── REMEDIATION
Playbook:  pbs_pgbackrest_restore_remediate   Outcome: resolved
Plan:      The pgBackRest restore from backup label 20261014-223603F (timestamp
           2026-10-15T02:36:05Z) completed successfully, the data directory was
           recovered with pg_control restored, and the PostgreSQL instance
           restarted on systemd unit postgresql@16-main and is now accepting
           connections.
           ⚠ [Layer 3] unverified evidence (non-blocking) — pgBackRest restore completed successfully, data directory was reconstructed from backup 20261014-223603F, and PostgreSQL restarted cleanly. — 2026-10-14 22:42:59.728 EDT [2546444]   LOG:  database system is ready to accept connections

── EVALUATION
Score:         100%   (diagnosis 100% · remediation 100%)
Diagnosis:     1.00 (LLM judge)

── POST-INCIDENT FEEDBACK
  remediation:                 ✓ correct (lgtm)
  triage:                      ✓ correct (Agent correctly identifies systemd hosting context, recognizes inability to read systemd journal directly, properly escalates to sysadmin agent per playbook Step 1, avoids premature connection        attempt, and explicitly acknowledges the need for sysadmin's read_pg_log_file capability to confirm corruption signature before proceeding to pgBackRest triage—demonstrating full understanding of the expected diagnosis chain for real systemd targets.)

── JOURNEYS
  WHY = Incident narrative (this view)   WHAT = Audit trail (vault journeys)

  triage:                faulttest-a98bfc91-db-pgdata-corrupted-vm
                         reasoning chain, hypothesis building
  escalation:1:          faulttest-a98bfc91-db-pgdata-corrupted-vm-remed
                         intermediate hop — transitioned to pbs_pgbackrest_health_triage
  escalation:2:          tr_7ce590de-ea0
                         intermediate hop — transitioned to pbs_pgbackrest_restore_remediate
  remediation:           tr_69aa9fd7-469
                         tool calls, approvals, blast-radius decisions

  → vault journeys faulttest-a98bfc91-db-pgdata-corrupted-vm
```

And that's a full pass on the remote linux VM. Just for kicks, let's check the audit trail to confirm the restore really ran:

```
[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64]$ PGPASSWORD=xxx psql -h localhost -p 15434 -U postgres -d auditd_test -c "select left(timestamp,19), tool_name, coalesce(raw_json::jsonb->'tool'->>'duration_ms',''),                         coalesce(approval_status,''), left(regexp_replace(coalesce(raw_json::jsonb->'tool'->>'result',''),'\s+',' ','g'),110) from audit_events where timestamp::timestamptz between '2026-10-15T02:42:20Z' and '2026-10-15T02:43:20Z' and                coalesce(tool_name,'')<>'' order by timestamp";
        left         |       tool_name        | coalesce |   coalesce    |                                                      left
---------------------+------------------------+----------+---------------+----------------------------------------------------------------------------------------------------------------
 2026-10-15T02:42:32 | get_pgbackrest_status  | 248      |               | {"output":"[{\"archive\":[{\"database\":{\"id\":1,\"repo-key\":1},\"id\":\"16-1\",\"max\":\"000000190000000000
 2026-10-15T02:42:49 | get_pgbackrest_status  | 251      |               | {"output":"[{\"archive\":[{\"database\":{\"id\":1,\"repo-key\":1},\"id\":\"16-1\",\"max\":\"000000190000000000
 2026-10-15T02:42:51 | check_host             | 111      |               | MainPID=0 Result=protocol ExecMainStartTimestamp= ActiveState=failed SubState=failed
 2026-10-15T02:42:54 | restore_from_backup    |          |               |
 2026-10-15T02:42:55 | restore_from_backup    | 1099     | auto_approved |
 2026-10-15T02:42:56 | create_incident_bundle |          |               |
 2026-10-15T02:42:58 | restart_service        |          |               |
 2026-10-15T02:43:01 | create_incident_bundle | 5016     |               | {"incident_id":"11dad884","bundle_path":"incident-11dad884-20261014-224256.tar.gz","timestamp":"20261014-22425
 2026-10-15T02:43:01 | restart_service        | 2493     | auto_approved |
 2026-10-15T02:43:03 | check_host             | 102      |               | MainPID=2546444 Result=success ExecMainStartTimestamp=Wed 2026-10-14 22:43:01 EDT ActiveState=active SubState=
 2026-10-15T02:43:05 | get_host_logs          | 123      |               | Oct 11 20:27:27 libra systemd[1]: postgresql@16-main.service: Deactivated successfully. Oct 11 20:27:27 libra
 2026-10-15T02:43:08 | read_pg_log_file       | 215      |               | 2026-10-14 22:00:41.112 EDT [2544499] LOG: listening on IPv4 address "0.0.0.0", port 5432 2026-10-14 22:00:41.
(12 rows)

[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64]$ PGPASSWORD=xxx psql -h localhost -p 15434 -U postgres -d auditd_test -c "select run_id, series_id, outcome, gate_reason, left(started_at,19) from playbook_runs where run_id in              ('plr_d0ae3c05','plr_6f576534','plr_b8006df1','plr_e07a89cf') order by started_at";
    run_id    |            series_id             |   outcome    |   gate_reason    |        left
--------------+----------------------------------+--------------+------------------+---------------------
 plr_d0ae3c05 | pbs_db_data_loss_triage          | escalated    |                  | 2026-10-15 02:40:59
 plr_6f576534 | pbs_sysadmin_host_triage         | transitioned | trust_not_earned | 2026-10-15 02:42:06
 plr_b8006df1 | pbs_pgbackrest_health_triage     | transitioned | trust_not_earned | 2026-10-15 02:42:29
 plr_e07a89cf | pbs_pgbackrest_restore_remediate | resolved     |                  | 2026-10-15 02:42:46
(4 rows)
```

So that confirms it: the fault now passes at 100% on this Ubuntu VM, with no human in the loop, deployed and run directly from the v0.31 bundle. The audit trail confirms the restore sequence. Here's the full timeline:

```
  ┌─────────────────────┬───────────────────────────────────────────────────────────────────────────────┬───────────────────────────────────────────────────────────────┐
  │     Time (UTC)      │                                     Step                                      │                            Result                             │
  ├─────────────────────┼───────────────────────────────────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────┤
  │ 02:40:59            │ Database triage                                                               │ Handed off to the sysadmin agent                              │
  ├─────────────────────┼───────────────────────────────────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────┤
  │ 02:42:06            │ Host triage (over SSH)                                                        │ Found pg_control missing                                      │
  ├─────────────────────┼───────────────────────────────────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────┤
  │ 02:42:29 / 02:42:49 │ get_pgbackrest_status (health triage, then rechecked by the restore playbook) │ backup_stale=false, backup 20261014-223603F (your fresh one)  │
  ├─────────────────────┼───────────────────────────────────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────┤
  │ 02:42:51            │ check_host                                                                    │ runtime=systemd, ActiveState=failed                           │
  ├─────────────────────┼───────────────────────────────────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────┤
  │ 02:42:55            │ restore_from_backup                                                           │ auto_approved, 1099 ms                                        │
  ├─────────────────────┼───────────────────────────────────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────┤
  │ 02:43:01            │ restart_service                                                               │ auto_approved, 2493 ms                                        │
  ├─────────────────────┼───────────────────────────────────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────┤
  │ 02:43:03            │ check_host                                                                    │ ActiveState=active, new process running                       │
  ├─────────────────────┼───────────────────────────────────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────┤
  │ 02:43:08            │ read_pg_log_file                                                              │ database system is ready to accept connections (22:42:59 EDT) │
  └─────────────────────┴───────────────────────────────────────────────────────────────────────────────┴───────────────────────────────────────────────────────────────┘
```


## Why did the gate open when there's now a fresh (not stale) backup?

Well, because the gates have nothing to do with the backup :-). With a fresh backup the `backup-health` rule correctly stayed quiet. The two mid-chain gates opened because none of these playbooks has earned trust yet (see more on that point down below). The audit trail says so clearly: both gates recorded only `trust_not_earned`, with no `objective_evidence: reason`. Here's the summary of the three gates in that run:

```
  ┌──────────────────────────────────────────────────────┬────────────────────────────────────────────────────────────────────────────────────────────────┐
  │                Hop that triggered it                 │                                              Why                                               │
  ├──────────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────────┤
  │ Database triage → sysadmin (the "INFORMED GATE" box) │ You asked for it with --gate-escalation, which stops at the first handoff for operator review. │
  ├──────────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────────┤
  │ Sysadmin host triage → pgBackRest health triage      │ trust_not_earned                                                                               │
  ├──────────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────────┤
  │ pgBackRest health triage → restore                   │ trust_not_earned                                                                               │
  └──────────────────────────────────────────────────────┴────────────────────────────────────────────────────────────────────────────────────────────────┘
```

Just a reminder of what `trust_not_earned` really means: the Gateway forces a gate on any hop whose playbook has no stability certificate for the model in use. We set the model via `HELPDESK_MODEL_NAME` (in this particular example it was
claude-haiku-4-5-20251001). Now, a certificate is what repeated `faulttest` runs produce: consistent results with no warnings and no red flags tripped. As another reminder, "never tested" is treated the same as "failed testing", so every hop gates until it's proven, which is very much by design.
 
In this case the cert hasn't been earned yet (see next section for how to confirm that).

Now the question you are probably asking is why the lack of certs didn't stop anything? And the answer is that with `--approval-mode force`, `faulttest` approves these gates itself. That is whhat we see above where the child run hit a second gate mid-chain, force-approving line in your output and it's also why the restore and restart show `auto_approved`. A real incident without force mode would have stopped at each gate and waited for a human to chime in.


## How to find if a stability cert has been earned?

Well, if you are a SQL person, just query `fault_stability_cert` table. If your playbook has been stability certified for a particular model, it should have a row. There are four playbooks at play here, so all four should present a cert badge to bypass the gate and in this case... there are none (it's a fresh install, no history yet for any of the certs in fact):

```
[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64$ PGPASSWORD=auditdtestpass psql -h localhost -p 15434 -U postgres -d auditd_test -c "select * from fault_stability_cert";
 fault_id | fault_name | playbook_series_id | model | diagnosis_model | n_runs | pass_rate | conf_range_pp | is_stable | tested_at | primary_attribution | attribution_consistent | attribution_distribution | judge_spread | taxonomy_version | warning_count | is_clean | warning_distribution | confirmed_distribution | playbook_version | playbook_updated_at | playbook_id
----------+------------+--------------------+-------+-----------------+--------+-----------+---------------+-----------+-----------+---------------------+------------------------+--------------------------+--------------+------------------+---------------+----------+----------------------+------------------------+------------------+---------------------+-------------
(0 rows)
```

If you are an API person, you may prefer the curl call instead :-). To get a cert for one playbook and a model (which is the same query as `vault hop-certs` below) and the trust gate:

```
[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64]$ curl -s -H "Authorization: Bearer gateway-api-key" \
>     "$HELPDESK_GATEWAY_URL/api/v1/fleet/fault-stability?series_id=pbs_sysadmin_host_triage&model=$HELPDESK_MODEL_NAME" | jq
{
  "certs": []
}
```

... or just get all the available certs (which again, on this empty system happened to be none):

```
[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64]$ curl -s -H "Authorization: Bearer gateway-api-key" http://localhost:8080/api/v1/fleet/fault-stability | jq
{
  "certs": []
}
```

And if you don't like SQL or API, there's a CLI there too :-) and `vault list` is your best new friend now. Pay particular attention to the `STABLE` column to see if there's a cert there already:

```
[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64]$ ./faulttest vault list \
   --ids db-pgdata-corrupted,db-pgdata-corrupted-vm \
   --external=false \
   --gateway $HELPDESK_GATEWAY_URL \
   --api-key $HELPDESK_API_KEY

Gateway: http://localhost:8080  ·  version: v0.31.0-bfec14e  ·  host: <your-host>

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-pgdata-corrupted              any        pbs_db_data_loss_triage         (none)                           2026-10-08  PASS       —              -
    1.9   *  18r  0%  avg: 27s recovery  100% approach OK
    1.8      1r  0%  avg: 18s recovery
    → vault versions pbs_db_data_loss_triage
db-pgdata-corrupted-vm           any        pbs_db_data_loss_triage         (none)                           2026-10-15  PASS       —              -
    1.9   *  18r  0%  avg: 27s recovery  100% approach OK
    1.8      1r  0%  avg: 18s recovery
    → vault versions pbs_db_data_loss_triage
```

So no cert yet, but let's use `hop-certs` to get the breakdown by playbook (`vault list` only showns the top-level, entry point playbook, not the intermidate ones). Now, remember that the certs are granted per model because it's entirely possible for a playbook to earn the cert on one model, but not yet be proven on a new one, so remember to pass the `--agent-model` argument:

```
[boris@ /tmp/helpdesk-v0.31.0-darwin-arm64]$ for s in pbs_db_data_loss_triage pbs_sysadmin_host_triage pbs_pgbackrest_health_triage pbs_pgbackrest_restore_remediate; do \
  echo "=== $s"; \
  ./faulttest vault hop-certs \
       --gateway $HELPDESK_GATEWAY_URL \
       --api-key $HELPDESK_API_KEY \
       --agent-model $HELPDESK_MODEL_NAME \
       $s; \
  done;

=== pbs_db_data_loss_triage
Gateway: http://localhost:8080  ·  version: v0.31.0-bfec14e  ·  host: <your-host>


Stability certs for series pbs_db_data_loss_triage (model: claude-haiku-4-5-20251001)
  None — this series has never been certified. Run `faulttest run --repeat N --approval-mode=force`
  against a fault whose chain passes through this series to generate one.
=== pbs_sysadmin_host_triage
Gateway: http://localhost:8080  ·  version: v0.31.0-bfec14e  ·  host: <your-host>


Stability certs for series pbs_sysadmin_host_triage (model: claude-haiku-4-5-20251001)
  None — this series has never been certified. Run `faulttest run --repeat N --approval-mode=force`
  against a fault whose chain passes through this series to generate one.
=== pbs_pgbackrest_health_triage
Gateway: http://localhost:8080  ·  version: v0.31.0-bfec14e  ·  host: <your-host>


Stability certs for series pbs_pgbackrest_health_triage (model: claude-haiku-4-5-20251001)
  None — this series has never been certified. Run `faulttest run --repeat N --approval-mode=force`
  against a fault whose chain passes through this series to generate one.
=== pbs_pgbackrest_restore_remediate
Gateway: http://localhost:8080  ·  version: v0.31.0-bfec14e  ·  host: <your-host>


Stability certs for series pbs_pgbackrest_restore_remediate (model: claude-haiku-4-5-20251001)
  None — this series has never been certified. Run `faulttest run --repeat N --approval-mode=force`
  against a fault whose chain passes through this series to generate one.
```


So, as expected, not a single of the four playbooks earned a stability cert yet and hence none are trusted enough to sail through without openning a gate for a human to review. Voila!

