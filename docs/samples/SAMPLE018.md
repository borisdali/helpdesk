# aiHelpDesk Sample#18 (on Docker): Every Hop Earns Its Own Trust (No More Free Passes Mid-Chain)

The raw transcript of the sample commands and deliberations presented below complements this blog post:

- **[The Last One That Still Needed Him](https://itnext.io/the-last-one-that-still-needed-him-14af8f324776)**
  Why the one hop Michael still checked out of habit finally stopped needing him. Part three of a series on trust that's earned and now, finally, complete.

If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel). 

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This one happened to be running on Docker, but see [here](SAMPLE010.md), [here](SAMPLE011.md) and [here](SAMPLE017.md) for VM/Bare Metal, the source and K8s respectively (although not the exact commands shown on this page).

In this transcript we pick the most important part of the aiHelpDesk [v0.26 release](https://github.com/borisdali/helpdesk/releases/tag/v0.26.0) release, which is making the intermidate hop playbooks to earn their own certs and demonstrating the power of the newly released [`vault hop-certs`](../VAULT.md#vault-hop-certs) CLI command, so... let's run the faulttest:


## 0. Back to the Basics

This section is likely redundant at this stage, but just in case that this is the first aiHelpDesk sample transcript you stumbled upon for Docker/Podman, here are the basics in a nutshell:

```
[boris@ /tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose]$ docker compose --profile governance down -v --remove-orphans
[+] Running 13/13
 ✔ Container helpdesk-secbot-1          Removed                                                                                                                                                                                              0.3s
 ✔ Container helpdesk-auditor-1         Removed                                                                                                                                                                                              0.2s
 ✔ Container helpdesk-govbot-1          Removed                                                                                                                                                                                              0.1s
 ✔ Container helpdesk-gateway-1         Removed                                                                                                                                                                                              0.2s
 ✔ Container helpdesk-k8s-agent-1       Removed                                                                                                                                                                                              0.4s
 ✔ Container helpdesk-database-agent-1  Removed                                                                                                                                                                                              0.5s
 ✔ Container helpdesk-research-agent-1  Removed                                                                                                                                                                                              0.5s
 ✔ Container helpdesk-sysadmin-agent-1  Removed                                                                                                                                                                                              0.4s
 ✔ Container helpdesk-incident-agent-1  Removed                                                                                                                                                                                              0.6s
 ✔ Container helpdesk-auditd-1          Removed                                                                                                                                                                                              0.3s
 ✔ Volume helpdesk_audit-data           Removed                                                                                                                                                                                              0.0s
 ✔ Volume helpdesk_incidents            Removed                                                                                                                                                                                              0.0s
 ✔ Network helpdesk_default             Removed                                                                                                                                                                                              0.2s

[boris@ /tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose]$ docker compose --profile governance up -d --wait
[+] Running 13/13
 ✔ Network helpdesk_default             Created                                                                                                                                                                                              0.0s
 ✔ Volume "helpdesk_incidents"          Created                                                                                                                                                                                              0.0s
 ✔ Volume "helpdesk_audit-data"         Created                                                                                                                                                                                              0.0s
 ✔ Container helpdesk-auditd-1          Healthy                                                                                                                                                                                             20.1s
 ✔ Container helpdesk-auditor-1         Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-sysadmin-agent-1  Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-research-agent-1  Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-incident-agent-1  Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-k8s-agent-1       Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-database-agent-1  Healthy                                                                                                                                                                                             20.0s
 ✔ Container helpdesk-gateway-1         Healthy                                                                                                                                                                                             19.8s
 ✔ Container helpdesk-govbot-1          Healthy                                                                                                                                                                                             19.4s
 ✔ Container helpdesk-secbot-1          Healthy                                                                                                                                                                                             19.4s

[boris@ /tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose]$ docker compose ps
NAME                        IMAGE                                COMMAND                  SERVICE          CREATED              STATUS                    PORTS
helpdesk-auditd-1           ghcr.io/borisdali/helpdesk:v0.26.0   "/bin/sh -c 'sleep 4…"   auditd           About a minute ago   Up 57 seconds (healthy)   0.0.0.0:1199->1199/tcp
helpdesk-auditor-1          ghcr.io/borisdali/helpdesk:v0.26.0   "/usr/local/bin/audi…"   auditor          About a minute ago   Up 51 seconds
helpdesk-database-agent-1   ghcr.io/borisdali/helpdesk:v0.26.0   "/usr/local/bin/data…"   database-agent   About a minute ago   Up 51 seconds             0.0.0.0:1100->1100/tcp
helpdesk-gateway-1          ghcr.io/borisdali/helpdesk:v0.26.0   "/usr/local/bin/gate…"   gateway          59 seconds ago       Up 51 seconds (healthy)   0.0.0.0:8080->8080/tcp
helpdesk-incident-agent-1   ghcr.io/borisdali/helpdesk:v0.26.0   "/usr/local/bin/inci…"   incident-agent   About a minute ago   Up 51 seconds             0.0.0.0:1104->1104/tcp
helpdesk-k8s-agent-1        ghcr.io/borisdali/helpdesk:v0.26.0   "/usr/local/bin/k8s-…"   k8s-agent        About a minute ago   Up 51 seconds             0.0.0.0:1102->1102/tcp
helpdesk-research-agent-1   ghcr.io/borisdali/helpdesk:v0.26.0   "/usr/local/bin/rese…"   research-agent   About a minute ago   Up 51 seconds             0.0.0.0:1106->1106/tcp
helpdesk-secbot-1           ghcr.io/borisdali/helpdesk:v0.26.0   "/usr/local/bin/secb…"   secbot           59 seconds ago       Up 40 seconds             0.0.0.0:9091->9091/tcp
helpdesk-sysadmin-agent-1   ghcr.io/borisdali/helpdesk:v0.26.0   "/usr/local/bin/sysa…"   sysadmin-agent   About a minute ago   Up 51 seconds             0.0.0.0:1103->1103/tcp
```

The [SysAdmin Agent](../SYSADMIN_AGENT.md) is one of the key agents for this 3-hop certs, so let's quickly check the log:

```
[boris@ /tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose]$ docker compose logs sysadmin-agent
sysadmin-agent-1  | time=2026-08-30T20:51:44.659Z level=INFO msg="agent starting" component=sysadmin_agent operating_mode=fix
sysadmin-agent-1  | time=2026-08-30T20:51:44.660Z level=INFO msg="infrastructure config loaded" databases=7 db_keys="alloydb-on-vm, alloydb-on-vm-local, faulttest-db, faulttest-db-local, pg-cluster-minkube, pg-cluster-minkube-local, test-db"
sysadmin-agent-1  | time=2026-08-30T20:51:44.660Z level=INFO msg="agent audit logging enabled (remote)" url=http://auditd:1199
sysadmin-agent-1  | time=2026-08-30T20:51:44.660Z level=INFO msg="tool auditing enabled" session_id=sysadmin_e9c74394
sysadmin-agent-1  | time=2026-08-30T20:51:46.305Z level=INFO msg="policy enforcement enabled (remote check mode)" url=http://auditd:1199
sysadmin-agent-1  | time=2026-08-30T20:51:46.305Z level=INFO msg="approval workflow enabled" audit_url=http://auditd:1199 timeout=5m0s
sysadmin-agent-1  | time=2026-08-30T20:51:46.305Z level=INFO msg=governance audit=true policy=true approval=true
sysadmin-agent-1  | time=2026-08-30T20:51:46.305Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001
sysadmin-agent-1  | time=2026-08-30T20:51:46.323Z level=INFO msg="agent inbound auth enabled" users_file=/etc/helpdesk/users.yaml
sysadmin-agent-1  | time=2026-08-30T20:51:46.323Z level=INFO msg="direct tool dispatch enabled" agent=sysadmin_agent tools=8
sysadmin-agent-1  | time=2026-08-30T20:51:46.323Z level=INFO msg="starting A2A server with tracing" agent=sysadmin_agent url=http://sysadmin-agent:1103 card=http://sysadmin-agent:1103/.well-known/agent-card.json
```

Good clean, no errors, no warnings.

See the previous sample transcripts (starting with [this sample](SAMPLE006.md)) for more details on this if the above was too quick of a whirlwind tour.


## 1. Run the fault injection testing with `--repeat N`

```
[boris@ /tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose]$ date; time docker run --rm --network helpdesk_default \
>     -v /var/run/docker.sock:/var/run/docker.sock \
>     -v "/helpdesk/testing:/testing-docker/testing" \
>     -v "/tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose/infrastructure.json:/infrastructure.json" \
>     -v "/tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose/.faulttest-history:/root/.faulttest" \
>     -w /testing-docker \
>     ghcr.io/borisdali/helpdesk:v0.26.0 \
>     faulttest run --ids host-container-stopped --repeat 3 --approval-mode=force \
>     --via-gateway --gateway http://gateway:8080 --api-key $HELPDESK_CLIENT_API_KEY \
>     --judge-api-key "$HELPDESK_API_KEY" \
>     --conn faulttest-db --infra-config /infrastructure.json \
>     --agent-model claude-haiku-4-5-20251001 --operator alice@example.com
```

A couple of comments on the parameters used in the above command:

  --ids is the parameter that we use to run just a single fault injection test, `host-container-stopped` in this case.

  --conn takes a full raw DSN or an alias that aiHelpDesk `faulttest` resolves by consulting the infra.json file (see next parameter).

  --infra-config points at this staging directory's copy of the infra.json that is mapped from a host (see the "-v" mapping above).

Here's how the output of the above command looks like:

```
Sun Aug 30 21:48:12 EDT 2026
time=2026-08-31T01:48:13.085Z level=INFO msg=--conn alias=faulttest-db host=host.docker.internal

--- Testing: Database container stopped (host-container-stopped) — 3 runs ---

  Run 1/3
time=2026-08-31T01:48:13.101Z level=INFO msg="executing injection spec" type=docker phase=inject
time=2026-08-31T01:48:14.273Z level=INFO msg="sending prompt to agent via playbook" failure=host-container-stopped series_id=pbs_db_restart_triage playbook_id=pb_2c320d01 gateway=http://gateway:8080 agent-conn="host=host.docker.internal port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_8224f649)
time=2026-08-31T01:49:18.115Z level=INFO msg="executing injection spec" type=docker phase=teardown
  [PASS] score=100%
         [PRIMARY 100%] Container requires restart authorization from write-capable context
         [95%] PostgreSQL process inside the Docker container 'helpdesk-test-pg' is not running (stopped, crashed, or failed to start)
         [95%] Container stopped cleanly via external signal or orchestrator command, no crash or error
         [REJECTED 5%] Network/firewall issue preventing connectivity to the container — Both IPv4 and IPv6 paths show connection refused rather than timeout, indicating the issue is local (container not listening), not a network routing problem.
         [REJECTED 5%] Silent data corruption or unlogged crash at startup — No corruption indicators ("invalid page", "checksum failure") found in logs; startup messages show normal initialization sequence
         [REJECTED 0%] The container failed to start after previous graceful shutdown — Prior findings confirm clean exit with exitcode=0 and no corruption; no evidence of startup failure.

  Run 2/3
time=2026-08-31T01:49:18.379Z level=INFO msg="executing injection spec" type=docker phase=inject
time=2026-08-31T01:49:29.378Z level=INFO msg="sending prompt to agent via playbook" failure=host-container-stopped series_id=pbs_db_restart_triage playbook_id=pb_2c320d01 gateway=http://gateway:8080 agent-conn="host=host.docker.internal port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_dee4cd54)
time=2026-08-31T01:50:36.705Z level=INFO msg="executing injection spec" type=docker phase=teardown
  [PASS] score=100%
         [PRIMARY 100%] PostgreSQL process inside the helpdesk-test-pg Docker container is not running or not listening on port 15432
         [100%] Container was killed by external SIGKILL (exit code 137) while database was running normally; restart will recover the service
         [95%] Container was shut down cleanly by an external signal (docker kill, docker stop timeout, or orchestrator SIGKILL) while the database was running and accepting connections
         [REJECTED 10%] Container network misconfiguration preventing connection — Connection refused on both IPv4 and IPv6 addresses indicates a local listening failure, not a routing issue
         [REJECTED 5%] Database experienced a sudden crash that triggered automatic container kill — The PostgreSQL log file shows clean startup, successful recovery, and readiness; if the database had crashed, PostgreSQL would have logged PANIC, FATAL, or an assertion failure before exit; the absence of these messages combined with oomkilled=false rules out a crash

  Run 3/3
time=2026-08-31T01:50:36.868Z level=INFO msg="executing injection spec" type=docker phase=inject
time=2026-08-31T01:50:37.684Z level=INFO msg="sending prompt to agent via playbook" failure=host-container-stopped series_id=pbs_db_restart_triage playbook_id=pb_2c320d01 gateway=http://gateway:8080 agent-conn="host=host.docker.internal port=15432 dbname=testdb user=postgres password=testpass"
  Feedback submitted (triage/post_incident run_id=plr_e5ba2d47)
time=2026-08-31T01:51:45.173Z level=INFO msg="executing injection spec" type=docker phase=teardown
  [PASS] score=100%
         [PRIMARY 100%] Policy restrictions prevent diagnostic-purpose write operations from executing the restart. — This is not a root-cause hypothesis — it is a constraint; the actual root cause is the clean shutdown confirmed by prior investigation.
         [95%] PostgreSQL process in docker container has stopped or crashed, preventing TCP connections on port 15432
         [95%] Container was stopped cleanly via explicit shutdown request (SIGTERM/pg_ctl stop), not a crash or infrastructure failure
         [95%] Container exited cleanly following an explicit fast shutdown request and requires a restart to restore service; no infrastructure failures detected.
         [REJECTED 5%] Firewall or network configuration blocking port 15432 — Connection refused on both IPv4 (192.168.65.254) and IPv6 (fdc4:f303:9324::254) makes dual-stack firewall block unlikely; more consistent with process not listening
         [REJECTED 0%] Container crashed due to OOM or kernel kill — check_host explicitly reports oomkilled=false and exitcode=0, and no OOM messages appear in logs
time=2026-08-31T01:51:45.647Z level=INFO msg="using model" vendor=anthropic model=claude-haiku-4-5-20251001

  Stability report (3 runs):
    Pass rate:    3/3 (100%)
    Confidence:   min=100% max=100% range=0pp mean=100%  (H1, passing runs only)
    Verdict:      STABLE
    Attribution:  clean-shutdown (3/3)  consistent: yes  [taxonomy 1.1]
  ────────────────────────────────────────────────────────────────
    Warnings:     0/3 run(s) tripped a verified warning signal
    Clean:        yes
time=2026-08-31T01:51:48.586Z level=INFO msg="fault stability cert posted" fault_id=host-container-stopped verdict=STABLE clean=CLEAN warning_count=0 n_runs=3
time=2026-08-31T01:51:49.361Z level=INFO msg="fault stability cert posted" fault_id=host-container-stopped::hop:pbs_db_restart_action verdict=STABLE clean=CLEAN warning_count=0 n_runs=3
time=2026-08-31T01:51:49.930Z level=INFO msg="fault stability cert posted" fault_id=host-container-stopped::hop:pbs_sysadmin_docker_inspect verdict=STABLE clean=CLEAN warning_count=0 n_runs=3

=== Fault Test Report: fb0f4603 ===

[PASS] Database container stopped (host-container-stopped) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Database container stopped (host-container-stopped) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]
[PASS] Database container stopped (host-container-stopped) - score: 100%
       Keywords: 100% | Tools: 100% | Category: 100% [no judge — add --judge for semantic scoring]

--- Summary ---
Total: 3 | Passed: 3 | Failed: 0 | Rate: 100%
  host: 3/3 (100%)

Report written to ./faulttest-fb0f4603.json
```

All 3 runs passed at 100%, and the full 3-hop chain (DB triage → `pbs_sysadmin_docker_inspect` → `pbs_db_restart_action`) posted CLEAN on all three stability certs. This is the real e2e confirmation that the containerized `faulttest` (using the v0.26.0 image's new Docker CLI) can drive a `type: docker` fault against a real compose stack through the Gateway, not just a bare `docker compose stop/start` smoke test. Note:

          Stability report (3 runs): Pass rate 3/3 (100%), Verdict: STABLE, Clean: yes
          fault_stability_cert posted: host-container-stopped → CLEAN
          fault_stability_cert posted: host-container-stopped::hop:pbs_db_restart_action → CLEAN
          fault_stability_cert posted: host-container-stopped::hop:pbs_sysadmin_docker_inspect → CLEAN


## 2. `vault hop-certs`

```
[boris@ /tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose]$ for series in pbs_db_restart_triage pbs_sysadmin_docker_inspect pbs_db_restart_action; do
     docker run --rm --network helpdesk_default
       ghcr.io/borisdali/helpdesk:v0.26.0
       faulttest vault hop-certs "$series"
       --gateway $HELPDESK_GATEWAY_URL --api-key $HELPDESK_CLIENT_API_KEY
       --agent-model claude-haiku-4-5-20251001;
   done
Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.26.0-5-g6359322-6359322  ·  host: fc83ef73048e


Stability certs for series pbs_db_restart_triage (model: claude-haiku-4-5-20251001)

  Fault         : host-container-stopped  (Database container stopped)
  Trust         : EARNED
  Verdict       : STABLE / CLEAN
  Runs          : 3  (pass rate 100%)
  Attribution   : clean-shutdown  consistent: true
  Tested at     : 2026-08-31T01:51:48Z
Gateway: http://gateway:8080  ·  version: v0.26.0-5-g6359322-6359322  ·  host: fc83ef73048e


Stability certs for series pbs_sysadmin_docker_inspect (model: claude-haiku-4-5-20251001)

  Fault         : host-container-stopped::hop:pbs_sysadmin_docker_inspect  (Database container stopped (hop: pbs_sysadmin_docker_inspect))
  Trust         : EARNED
  Verdict       : STABLE / CLEAN
  Runs          : 3  (pass rate 100%)
  Attribution   : resolved  consistent: true
  Tested at     : 2026-08-31T01:51:49Z
Gateway: http://gateway:8080  ·  version: v0.26.0-5-g6359322-6359322  ·  host: fc83ef73048e


Stability certs for series pbs_db_restart_action (model: claude-haiku-4-5-20251001)

  Fault         : host-container-stopped::hop:pbs_db_restart_action  (Database container stopped (hop: pbs_db_restart_action))
  Trust         : EARNED
  Verdict       : STABLE / CLEAN
  Runs          : 3  (pass rate 100%)
  Attribution   : __remediation__  consistent: true
  Tested at     : 2026-08-31T01:51:49Z


[boris@ /tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose]$ docker run --rm --network helpdesk_default \
    -v "/helpdesk/testing:/testing-docker/testing" \
    -v "/tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    ghcr.io/borisdali/helpdesk:v0.26.0 \
    faulttest vault list --ids host-container-stopped --gateway $HELPDESK_GATEWAY_URL --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.26.0-5-g6359322-6359322  ·  host: fc83ef73048e

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
host-container-stopped           docker/vm  pbs_db_restart_triage           (none)                           2026-08-31  PASS       STABLE(3) attr=clean-shutdown -
    → vault versions pbs_db_restart_triage
```

Nice! Note how the top-level cert is marked as EARNED/STABLE/CLEAN with Attribution: `clean-shutdown consistent: true`, both hop certs EARNED and the `vault list` showing the real LAST TEST: 2026-08-31 PASS with the `attr=clean-shutdown` inline. That closes out the whole chain with a fully-verified, fully-attributed 3-hop stability cert set.

(Yes, it's been brought to our attention already that `vault hop-certs` doesn't yet support --ids parameter, so we had to run the for loop for now. We'll fix this shortly)


## 3. `vault versions` showing SUCCESS = 0%

Just one last note because it caused confusion in the past:

```
[boris@ /tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose]$ docker run --rm --network helpdesk_default \
    -v "/helpdesk/testing:/testing-docker/testing" \
    -v "/tmp/helpdesk/helpdesk-v0.26.0-deploy/docker-compose/.faulttest-history:/root/.faulttest" \
    -w /testing-docker \
    ghcr.io/borisdali/helpdesk:v0.26.0 \
    faulttest vault versions pbs_db_restart_triage --gateway $HELPDESK_GATEWAY_URL --api-key $HELPDESK_CLIENT_API_KEY

Gateway: $HELPDESK_GATEWAY_URL  ·  version: v0.26.0-5-g6359322-6359322  ·  host: fc83ef73048e

Version stats for pbs_db_restart_triage

VERSION     RUNS    SUCCESS%   AVG STEPS   AVG TIME    AVG DIAG   AVG REMED  APPROACH OK  JUDGE VERDICT
────────────────────────────────────────────────────────────────────────────────────────────────────
1.7 *       12      0%         –           57s         100%       –          –          –
  id=pb_2c320d01

* = currently active   SUCCESS% = resolved + transitioned
id/from lines show playbook_id and the run that generated that version
```

Yes, `vault versions` shows SUCCESS= 0% for `pbs_db_restart_triage` playbook despite all 12 historical runs (including your 3 just now) scoring 100% on the `faulttest` eval.

This is by design and totally expected because that's a different metric that counts playbook run outcomes, be it resolved or transitioned and not the `faulttest` eval scores. Now every one of these runs correctly diagnosed the fault (100% eval score and that's what earned the cert), but all of them ended in a policy denial on `restart_container` (because the `purpose=diagnostic` is blocked from destructive ops) rather than an actual resolved outcome, since nothing here has `oncall_senior`/`dba_lead` write authorization. So this 0% here means "the container was never actually restarted," which is correct and expected for a diagnostic-purpose `faulttest` run.

So to summarize: the stability cert (EARNED/CLEAN) and this success-rate column are answering two very different questions: "is the diagnosis trustworthy" vs "did this run actually fix the problem."


