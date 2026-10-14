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

This sample transcript below is meant to showcase 

---

## 



```
[boris@ ~ /helpdesk]$ go run ./testing/cmd/faulttest vault incidents plr_42c04b12 --summary --gateway http://localhost:8080 --api-key gateway-api-key
Gateway: $HELPDESK_GATEWAY_URL  ·  version: dev  ·  host: <your-host>

time=2026-10-11T21:01:50.432-04:00 level=INFO msg=--conn host=localhost
time=2026-10-11T21:01:50.432-04:00 level=INFO msg=--agent-conn host=faulttest-db-local
INCIDENT plr_42c04b12 — resolved in 129s

1. TRIAGE        pbs_db_data_loss_triage → pbs_sysadmin_host_triage
   single_instance=true; backup_tool=pgbackrest; pitr_decided=false
2. ESCALATION 1  pbs_sysadmin_host_triage → pbs_pgbackrest_health_triage  [plr_958bf624]
   Data directory missing pg_control file; data_loss_restore_candidate=true; the database recovered successfully…
3. ESCALATION 2  pbs_pgbackrest_health_triage → pbs_pgbackrest_restore_remediate  [plr_ec769db2]
   pgBackRest stanza and repository are healthy (status_code=0); valid, error-free backup 20261009-232959F exist…
4. REMEDIATION   pbs_pgbackrest_restore_remediate (resolved)  [plr_bc0e3309]
   The pgBackRest backup 20261009-232959F is valid and the repo is healthy (status_code=0), but backup_stale=tru…

Overall: 20% (diagnosis 33% · remediation 0%)


[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault incidents plr_42c04b12 --gateway http://localhost:8080 --api-key gateway-api-key
Gateway: $HELPDESK_GATEWAY_URL  ·  version: dev  ·  host: <your-host>

time=2026-10-11T21:02:04.837-04:00 level=INFO msg=--conn host=localhost
time=2026-10-11T21:02:04.837-04:00 level=INFO msg=--agent-conn host=faulttest-db-local

════════════════════════════════════════════════════════════
INCIDENT plr_42c04b12
Started: 2026-10-12 00:57 UTC   Duration: 129s
Operator: alice@example.com
Origin: faulttest   Status: resolved
Attribution: UNKNOWN
Bundle: /tmp/incident-b073dbf3-20261011-205719.tar.gz
Draft playbook: pb_6315eeb4
════════════════════════════════════════════════════════════

── TRIAGE
Playbook:  pbs_db_data_loss_triage
Findings:  single_instance=true; backup_tool=pgbackrest; pitr_decided=false

── GATE
Decision:  approved by alice@example.com  at 00:57 UTC

── ESCALATION 1/2
Playbook:  pbs_sysadmin_host_triage   Outcome: transitioned
Transitioned to: pbs_pgbackrest_health_triage (still a diagnosis hop, not yet remediation)
Findings:  Data directory missing pg_control file;
           data_loss_restore_candidate=true; the database recovered successfully
           multiple times via pgBackRest archive recovery but the pg_control file
           is now missing, preventing startup; requires restore from pgBackRest
           backup to recover.
           ⚠ [Layer 3] unverified evidence (non-blocking) — Data directory pg_control file is missing; restore from pgBackRest backup required — postgres: could not find the database system\nExpected to find it in the directory
           ⚠ [Layer 3] unverified evidence (non-blocking) — Data directory pg_control file is missing; restore from pgBackRest backup required — ,\nbut could not open file

Hypotheses:
  [PRIMARY  95%] Data directory pg_control file is missing; restore from pgBackRest backup required
  [REJECTED  5%] Disk space exhaustion caused data directory corruption

── ESCALATION 2/2
Playbook:  pbs_pgbackrest_health_triage   Outcome: transitioned
Transitioned to: pbs_pgbackrest_restore_remediate (still a diagnosis hop, not yet remediation)
Findings:  pgBackRest stanza and repository are healthy (status_code=0); valid,
           error-free backup 20261009-232959F exists and is ready for restore to
           recover from the missing pg_control file data-loss incident.
           ⚠ [Layer 4] unconfirmed evidence — pgbackrest_backup_unhealthy

Hypotheses:
  [PRIMARY  100%] pgBackRest repository and stanza are healthy with a valid, error-free backup available for restore recovery
  [REJECTED  0%] Backup is too stale to use for recovery

── REMEDIATION
Playbook:  pbs_pgbackrest_restore_remediate   Outcome: resolved
Plan:      The pgBackRest backup 20261009-232959F is valid and the repo is
           healthy (status_code=0), but backup_stale=true prevents automated
           restore per Expert Guidance precondition rules; a human must authorize
           whether the stale backup's WAL coverage is acceptable for this
           data-loss recovery.
           ✓ [Layer 4] confirmed evidence — pgbackrest_backup_unhealthy

── EVALUATION
Score:         19%   (diagnosis 33%)
Diagnosis:     0.33 (LLM judge)

── POST-INCIDENT FEEDBACK
  triage:                      ✗ wrong (Agent correctly identified the symptom (connection refused, service not running) but failed to execute the expected diagnostic chain: did not call check_host to determine runtime=systemd or             read_pg_log_file to find the corruption signature, instead stopping at scope inference and deferring without demonstrating the systemd-specific triage path that distinguishes this scenario from Docker/Kubernetes cases.)

── JOURNEYS
  WHY = Incident narrative (this view)   WHAT = Audit trail (vault journeys)

  triage:                faulttest-fef1655c-db-pgdata-corrupted-vm
                         reasoning chain, hypothesis building
  escalation:1:          faulttest-fef1655c-db-pgdata-corrupted-vm-remed
                         intermediate hop — transitioned to pbs_pgbackrest_health_triage
  escalation:2:          tr_37845019-3cc
                         intermediate hop — transitioned to pbs_pgbackrest_restore_remediate
  remediation:           tr_eb83a5a6-045
                         tool calls, approvals, blast-radius decisions

  → vault journeys faulttest-fef1655c-db-pgdata-corrupted-vm
```
