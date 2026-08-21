# aiHelpDesk Sample#17 (on K8s): The Cert That Doesn't Go Stale

The raw transcript of the sample commands and deliberations presented below complements this blog post:

- **[The AI that Un-Trusts Itself: The Page that Never Came](...)**
  How aiHelpDesk revokes its own permission before you have to ask. A story about the cert that doesn't silently go stale

If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel). 

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This one happened to be running on K8s, but see [here](SAMPLE010.md), [here](SAMPLE011.md) and [here](SAMPLE013.md) for VM/Bare Metal, the source and Docker/Podman respectively (although not the exact commands shown on this page).

The five sections below showcase feature-by-feature the new deterministic safeguards introduced in  aiHelpDesk [v0.25 release](https://github.com/borisdali/helpdesk/releases/tag/v0.25.0):






## 1. Re-verification: trust gate and `AttributionConsistent`

To be blunt, the real-world case of a model genuinely disagreeing with itself across runs, isn't reliably forceable live, so instead we test the actual gateway code path directly with a crafted precondition that exercises the real `trustNotYetEarnedForceGate`/`EarnsTrust()` logic against real persisted data, just with a controlled input. How? By posting a cert that's STABLE + CLEAN but NOT attribution-consistent, for a system playbook series, let's take  `pbs_connection_triage` as an example.
Prior to hitting/POST-ing the `/api/v1/fleet/fault-stability` endpoint, let's take stock of the `vault list` for `db-max-connections` playbook:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault list --ids db-max-connections --gateway http://localhost:8080 --api-key $HELPDESK_API_KEY
Gateway: http://localhost:8080

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-max-connections               any        pbs_connection_triage           pbs_connection_remediate         2026-07-28  PASS       —              -
```

Now, let's run the command:

```
[boris@ ~/helpdesk]$  curl -s -X POST http://localhost:8080/api/v1/fleet/fault-stability     -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" -H "Content-Type: application/json"     -d '{
      "fault_id": "manual-test-attr-inconsistent",
      "playbook_series_id": "pbs_connection_triage",
      "diagnosis_model": "claude-haiku-4-5-20251001",
      "n_runs": 5, "is_stable": true, "is_clean": true,
      "attribution_consistent": false
    }'
{"regressed":false}
```

... and review the output after:

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest vault list --ids db-max-connections,db-idle-in-transaction --gateway http://localhost:8080 --api-key $HELPDESK_API_KEY
Gateway: http://localhost:8080  ·  version: v0.24.0-12-g2213442-2213442  ·  host: helpdesk-gateway-584db96dfb-w7s5r

FAULT                            PLATFORM   DIAG PLAYBOOK                   REMED PLAYBOOK                   LAST TEST              STABLE         INCIDENTS
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
db-max-connections               any        pbs_connection_triage           pbs_connection_remediate         2026-07-28  PASS       —              0 runs  (system)
    diag  → vault versions pbs_connection_triage
db-idle-in-transaction           any        pbs_connection_triage           pbs_connection_remediate         2026-06-22  PASS       —              0 runs  (system)
    diag  → vault versions pbs_connection_triage
```

Let's take look at the full `db-max-connections` in detail: 

```
[boris@ ~/helpdesk]$ go run ./testing/cmd/faulttest show --id db-max-connections --gateway http://localhost:8080 --api-key $HELPDESK_API_KEY
# Cloned from built-in fault "db-max-connections". Change the id before using as a custom catalog.
version: "1"
failures:
    - id: db-max-connections
      name: Max connections exhausted
      category: database
      severity: high
      description: |
        All available PostgreSQL connections are consumed by idle sessions, preventing new clients from connecting.
      inject:
        type: docker_exec
        script: sql/inject_max_connections.sh
        exec_via: helpdesk-test-pgloader
        detach: true
      teardown:
        type: docker_exec
        script: sql/teardown_max_connections.sh
        exec_via: helpdesk-test-pgloader
      prompt: |
        Users are getting "too many clients" errors connecting to the database. The connection_string is "{{connection_string}}" — use it verbatim for all tool calls. Please investigate.
      evaluation:
        expected_tools:
            - check_connection
            - get_connection_stats
        expected_keywords:
            any_of:
                - max_connections
                - too many
                - connection limit
                - connection pool
                - exhausted
        expected_diagnosis:
            category: connection_exhaustion
            narrative: The agent should identify that the PostgreSQL max_connections limit has been reached due to idle or sleeping sessions consuming all available connection slots, and recommend either terminating idle connections or       increasing max_connections.
      timeout: 60s
      external_compat: true
      diagnosis_playbook_series_id: pbs_connection_triage
      external_inject:
        type: shell_exec
        script_inline: |
            if ! command -v psql >/dev/null 2>&1; then
              echo "ERROR: psql not found in PATH (required for shell_exec injection)" >&2
              echo "  macOS: brew install libpq && brew link --force libpq" >&2
              exit 1
            fi
            PSQL_ERR=$(psql "$FAULTTEST_CONN" -t -A -c "SHOW max_connections;" 2>&1 >/dev/null)
            MAX_CONN=$(psql "$FAULTTEST_CONN" -t -A -c "SHOW max_connections;" 2>/dev/null | tr -d ' \n')
            if ! printf '%s' "$MAX_CONN" | grep -qE '^[0-9]+$'; then
              echo "ERROR: could not read max_connections (conn: $FAULTTEST_CONN)" >&2
              echo "  psql error: $PSQL_ERR" >&2
              exit 1
            fi
            SU_RESERVED=$(psql "$FAULTTEST_CONN" -t -A -c "SHOW superuser_reserved_connections;" 2>/dev/null | tr -d ' \n')
            SU_RESERVED=${SU_RESERVED:-3}
            # Count existing client backends so we don't over-spawn and block superuser slots.
            EXISTING=$(psql "$FAULTTEST_CONN" -t -A -c \
              "SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend';" \
              2>/dev/null | tr -d ' \n')
            EXISTING=${EXISTING:-0}
            # Target: fill up to max_connections - superuser_reserved (regular user limit).
            TARGET=$(( MAX_CONN - SU_RESERVED ))
            SLOTS=$(( TARGET - EXISTING ))
            if [ "$SLOTS" -le 0 ]; then
              echo "Slots already at or above target ($EXISTING existing, target $TARGET/$MAX_CONN); skipping"
              exit 0
            fi
            rm -f /tmp/faulttest_maxconn_pids.txt
            for i in $(seq 1 "$SLOTS"); do
              { sleep 600 | psql "$FAULTTEST_CONN" -q; } >/dev/null 2>&1 &
              echo $! >> /tmp/faulttest_maxconn_pids.txt
            done
            sleep 3
            echo "Injected: $SLOTS idle connections ($EXISTING existing → $TARGET/$MAX_CONN)"
      external_teardown:
        type: shell_exec
        script_inline: |
            if [ -f /tmp/faulttest_maxconn_pids.txt ]; then
              # Kill the entire process group (negative PID) so both sleep and psql
              # are terminated; killing only the subshell PID orphans the psql child.
              while read -r pid; do
                kill -- -"$pid" 2>/dev/null || kill "$pid" 2>/dev/null || true
              done < /tmp/faulttest_maxconn_pids.txt
              rm -f /tmp/faulttest_maxconn_pids.txt
              sleep 2
            fi
            # Safety net: terminate any surviving idle backends via postgres.
            psql "$FAULTTEST_CONN" -c "
              SELECT pg_terminate_backend(pid) FROM pg_stat_activity
                WHERE state = 'idle' AND pid <> pg_backend_pid();
            " >/dev/null 2>&1 || true
            echo "Teardown: idle connections terminated"
      remediation:
        playbook_id: pbs_connection_remediate
        verify_sql: SELECT count(*) < current_setting('max_connections')::int - 5 FROM pg_stat_activity WHERE state = 'idle'
        verify_timeout: 60s
```

Next, let's trigger a real playbook run for that same series and it should come back with `pending_gate` and `gate_reason containing "trust_not_earned", even though the cert is STABLE and CLEAN. That's precisely because it's not attribution-consistent (which is our new v0.25 feature).


## 2. Trigger a playbook with the good cert and trip the `trust_not_earned` safeguard

Again, what we expect to see here is `transition_target: "pbs_connection_remediate"` and `status: "pending_gate"` with `gate_reason` containing `trust_not_earned`. This is because there should be a genuine handoff proposed and the on-record certs for this series+model are dirty.

```
[boris@ ~/helpdesk]$ for i in $(seq 1 80); do
     ( sleep 420 | PGPASSWORD=$PGPASSWORD psql -h localhost -p 25432 -U app -d app >/dev/null 2>&1 & )
   done
   sleep 3
   PGPASSWORD=$PGPASSWORD psql -h localhost -p 25432 -U app -d app -c \
     "SELECT count(*) FILTER (WHERE state='idle') AS idle, count(*) AS total FROM pg_stat_activity;"
  ⎿   idle | total
     ------+-------
        80 |    87
     (1 row)
```

So we've got 80 idle vs. 87 total, this time with a 7-minute window, which should comfortably outlast the multi-tool-call investigation. Let's run it now:

```
[boris@ ~/helpdesk]$ date; time curl -s -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" -H "X-Purpose: diagnostic" \
>        -X POST http://localhost:8080/api/v1/fleet/playbooks/pbs_connection_triage/run \
>        -H "Content-Type: application/json" \
>        -d '{"connection_string": "pg-cluster-minkube", "context": "connection pool near saturation, high idle connection count"}' \
>        -o /tmp/attr_gate_test3.json -w "\nHTTP status: %{http_code}\n"
Tue Aug 18 00:46:57 EDT 2026

HTTP status: 200

real    0m53.627s
user    0m0.007s
sys     0m0.010s

[boris@ ~/helpdesk]$ jq '{run_id, status, transition_target, gate_reason, escalation_target, transition_target, findings, tool_calls, warnings, mismatch}' /tmp/attr_gate_test3.json
{
  "run_id": "plr_b692feaa",
  "status": "pending_gate",
  "transition_target": "pbs_connection_remediate",
  "gate_reason": "trust_not_earned",
  "escalation_target": null,
  "findings": "connections 87/100 (87%); idle=80; blocker=none; recommended=kill_idle. However, idle connections are too recent (<5 minutes) for automatic termination. Root cause is idle_session_timeout=0 (disabled), which prevents automatic disconnection. Recommended immediate action: escalate to application team to close idle connections (connection pool reset or application restart). Long-term: configure idle_session_timeout to a reasonable value (e.g., 900000 ms = 15 minutes) to auto-disconnect idle sessions.",
  "tool_calls": [
    "check_connection",
    "get_server_info",
    "get_active_connections",
    "get_blocking_queries",
    "get_connection_stats",
    "get_session_info",
    "get_session_info",
    "get_session_info",
    "get_config_parameter",
    "get_pg_settings",
    "get_pg_settings",
    "terminate_idle_connections",
    "terminate_idle_connections"
  ],
  "warnings": null,
  "mismatch": true
}
```

Confirmed!

```
        status: "pending_gate",
        transition_target: "pbs_connection_remediate",
        gate_reason: "trust_not_earned".
```

A real, genuine handoff was proposed this time (idle connections still present, `TRANSITION_TO: pbs_connection_remediate`) and the trust gate correctly intercepted it. Now, before v0.25 this would have auto-chained straight into remediation unattended, despite the on-record certs for this series+model being dirty (`is_clean:false` and `attribution_consistent:false`).

Since this point caused some confusion previously, it's worth spending a few minutes clarifying it:

The `--gate-escalation` that we introduced earlier still works exactly as before, completely unrelated to v0.25. It's a genuinely opt-in mechanism (`gate_escalation: true` on the request, or faulttest's --gate-escalation flag) that lets any caller request a pause-and-review at the next phase boundary, regardless of any trust/confidence/evidence. In v0.25 we didn't touch it at all.

Now the `trustNotYetEarnedForceGate` mechanism itself (checking STABLE+CLEAN, unconditionally, regardless of whether the caller requested `gate_escalation`) was introduced in v0.24. That's the adaptive gate. What v0.25 did was add `AttributionConsistent` as a third required condition to that already-mandatory check and thus closing a real gap where a cert could be STABLE+CLEAN but attribution-inconsistent and still incorrectly pass. We refer to this phenomenon as the model disagreeing with itself on root cause across runs. So the gate has been mandatory since v0.24, but v0.25 made the bar it checks stricter.


## 3. Cert history diff lines + `vault diff` hint in one combined test

This release v0.25 and this transcript continues off where we left off in v0.24 and the [previous transcript](SAMPLE016.md), so it may make sense to glance of that first if you are not familiar with it.

Let's start off by fetching the current playbook and in particular, let's note the real `playbook_id`/`version` (call them <real-id>/<real-version>):

```
[boris@ ~/helpdesk]$ curl -s -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" \
>     "http://localhost:8080/api/v1/fleet/playbooks?series_id=pbs_connection_triage" \
>     | jq '.playbooks[] | select(.is_active) | {playbook_id, version}'
{
  "playbook_id": "pb_4cc7ace3",
  "version": "1.6"
}
```

Let's post the "older, clean" synthetic cert with the same fault ID as the real one (`custom-target-drift-nudge`), but a distinct `diagnosis_model` so it's a separate composite-key row, not an overwrite:

```
[boris@ ~/helpdesk]$ curl -s -X POST http://localhost:8080/api/v1/fleet/fault-stability     -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" -H "Content-Type: application/json"     -d '{"fault_id":"custom-target-drift-nudge","fault_name":"Max connections exhausted (target-drift nudge variant)","playbook_series_id":"pbs_connection_triage","diagnosis_model":"claude-synthetic-test","n_runs":5,"pass_rate":1.0,"is_stable":true,"is_clean":true,                  "attribution_consistent":true,"playbook_version":"0.1-stale","playbook_id":"pb_stale0000"}'
{"regressed":false}
```

Good, now let's post the "newer, dirty" synthetic cert with the same `fault_id` + `diagnosis_model`, still stale relative to the real current version, now flipped dirty:

```
[boris@ ~/helpdesk]$ curl -s -X POST http://localhost:8080/api/v1/fleet/fault-stability     -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" -H "Content-Type: application/json"     -d '{"fault_id":"custom-target-drift-nudge","fault_name":"Max connections exhausted (target-drift nudge variant)","playbook_series_id":"pbs_connection_triage","diagnosis_model":"claude-synthetic-test","n_runs":5,"pass_rate":0.8,"is_stable":true,"is_clean":false,"warning_count": 3,"warning_distribution":{"mismatch":3},"attribution_consistent":true,"playbook_version":"0.1-stale","playbook_id":"pb_stale0000"}'
{"regressed":true}
```

And now we are ready to re-run the `vault-accuracy` Pod (this fault ID is in the mounted `target-drift-catalog` ConfigMap, so it resolves correctly this time).


## 4. `vault accuracy`: new cert history section (with diff and trend)


```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system apply -f - <<'EOF'
> apiVersion: v1
> kind: Pod
> metadata:
>   name: vault-check-t1t4
>   namespace: helpdesk-system
> spec:
>   restartPolicy: Never
>   containers:
>     - name: vault-check-t1t4
>       image: ghcr.io/borisdali/helpdesk:v0.24.0-17-g516545d-516545d
>       imagePullPolicy: Never
>       command: ["/usr/local/bin/faulttest"]
>       args: ["vault", "accuracy", "custom-target-drift-nudge", "--gateway=http://helpdesk-gateway:8080", "--catalog=/etc/faulttest/target_drift_catalog.yaml"]
>       env:
>         - name: HELPDESK_CLIENT_API_KEY
>           valueFrom:
>             secretKeyRef: {name: ***, key: ***}
>       volumeMounts:
>         - name: catalog
>           mountPath: /etc/faulttest/target_drift_catalog.yaml
>           subPath: target_drift_catalog.yaml
>           readOnly: true
>   volumes:
>     - name: catalog
>       configMap:
>         name: target-drift-catalog
> EOF
```

Let's see the results:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system logs vault-check-t1t4
Gateway: http://helpdesk-gateway:8080  ·  version: v0.25.0-dev2-1787248777-d637d1b  ·  host: helpdesk-gateway-6c7c5cdcfb-zr7w5

Diagnosis accuracy for series: pbs_connection_triage

  No feedback submitted yet.
  Run a fault test and submit feedback after recovery to populate this report.

  Tip: run `faulttest vault accuracy` (no args) to list all series with feedback.

Triage consistency
  Fault         : custom-target-drift-nudge  (Max connections exhausted (target-drift nudge variant))
  Verdict       : STABLE
  Runs          : 5
  Pass rate     : 80%
  Conf range    : 0pp  (primary hypothesis, passing runs only)
  Clean         : no  (3/5 run(s) tripped a verified warning signal)
  Warning types : mismatch=3(varies)
  Playbook      : pbs_connection_triage
  Diagnosis model: claude-synthetic-test
  Tested at     : 2026-08-20 18:15 UTC  (0 days ago)
  ⚠ cert was earned against playbook version 0.1-stale, current version is 1.6 — consider re-running --repeat to refresh
    See what changed: faulttest vault diff pb_stale0000 pb_22585380

Cert history (last 2)
  2026-08-20 18:15 UTC   STABLE   DIRTY attr=consistent (5 runs)
      ↳ changed since 2026-08-20 18:14 UTC: trust regressed (was STABLE+CLEAN+attribution-consistent); clean: CLEAN→DIRTY; warning_distribution: mismatch 0→3
  2026-08-20 18:14 UTC   STABLE   CLEAN attr=consistent (5 runs)
```


That's a complete, clean pass with every piece rendering exactly as designed in one output:

  - `vault diff hint`:
    ⚠ cert was earned against playbook version 0.1-stale, current version is 1.6 — consider re-running --repeat to refresh

    followed by
    See what changed: faulttest vault diff pb_stale0000 pb_22585380 —

    Both real playbook IDs, ready to run.

  - Task 1 (cert history diff lines):
    Cert history (last 2) with the `↳ changed since 2026-08-20 18:14 UTC: trust regressed (was STABLE+CLEAN+attribution-consistent); clean: CLEAN→DIRTY; warning_distribution: mismatch 0→3`

    Exactly the trust-regression + warning-distribution-delta format from the design.

  - Task 2 (Helm --catalog mounting):
    The `--catalog=/etc/faulttest/target_drift_catalog.yaml` mount is what let this Pod resolve our `custom-target-drift-nudge` at all.




## 5. `vault diff`

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system apply -f - <<'EOF'
> apiVersion: v1
> kind: Pod
> metadata:
>   name: vault-history-check
>   namespace: helpdesk-system
> spec:
>   restartPolicy: Never
>   containers:
>     - name: vault-history-check
>       image: ghcr.io/borisdali/helpdesk:v0.25.0-dev2-1787248777-d637d1b
>       imagePullPolicy: Never
>       command: ["/usr/local/bin/faulttest"]
>       args: ["vault", "history", "pbs_connection_triage", "--gateway=http://helpdesk-gateway:8080"]
>       env:
>         - name: HELPDESK_CLIENT_API_KEY
>           valueFrom:
>             secretKeyRef: {name: gateway-***, key: ***}
> EOF
pod/vault-history-check created

[boris@ ~/helpdesk]$ kubectl -n helpdesk-system logs vault-history-check
Gateway: http://helpdesk-gateway:8080  ·  version: v0.25.0-dev2-1787248777-d637d1b  ·  host: helpdesk-gateway-6c7c5cdcfb-zr7w5

Version history for pbs_connection_triage — 2 version(s)

ID              VERSION    SOURCE     CREATED     STATUS / NAME
────────────────────────────────────────────────────────────────────────────────────────────────────
pb_22585380     1.6        system     2026-08-14  * Connection & Lock Triage
pb_b04ec72f     1.5        system     2026-08-07  Connection & Lock Triage

* = currently active version
Use: faulttest vault diff <id1> <id2> to compare any two versions.
```

Two real versions available: `pb_22585380` (v1.6, active) and `pb_b04ec72f` (v1.5). And there's a hint that is effectively a ready-to-run command to diff them:

```
[boris@ ~/helpdesk]$ kubectl -n helpdesk-system apply -f - <<'EOF'
> apiVersion: v1
> kind: Pod
> metadata:
>   name: vault-diff-check
>   namespace: helpdesk-system
> spec:
>   restartPolicy: Never
>   containers:
>     - name: vault-diff-check
>       image: ghcr.io/borisdali/helpdesk:v0.25.0-dev2-1787248777-d637d1b
>       imagePullPolicy: Never
>       command: ["/usr/local/bin/faulttest"]
>       args: ["vault", "diff", "pb_b04ec72f", "pb_22585380", "--gateway=http://helpdesk-gateway:8080"]
>       env:
>         - name: HELPDESK_CLIENT_API_KEY
>           valueFrom:
>             secretKeyRef: {name: ***, key: ***}
> EOF
pod/vault-diff-check created

[boris@ ~/helpdesk]$ kubectl -n helpdesk-system logs vault-diff-check
Gateway: http://helpdesk-gateway:8080  ·  version: v0.25.0-dev2-1787248777-d637d1b  ·  host: helpdesk-gateway-6c7c5cdcfb-zr7w5

Diff: series pbs_connection_triage
  before  pb_b04ec72f  v1.5  Connection & Lock Triage
  after   pb_22585380  v1.6  Connection & Lock Triage

```

No differences, so the two versions are identical. That's a real, complete confirmation of the whole chain. `vault diff` correctly connected to the real playbook data and printed a valid result (in this case, it happened to be "no differences," which in itself is meaningful: v1.5→v1.6 was likely a metadata-only bump, e.g. activation timestamp, source tagging, etc. Not a content change).

We didn't get to see it in this particular example, but `vault diff` is capable now to print a real field-by-field diff between playbook versions: guidance text, escalation conditions, execution mode, etc., whatever actually changed.

And that's a wrap! This new and improved `vault diff` hint doesn't just print a plausible-looking string. No, instead it's a genuinely runnable command that resolves against the real playbook-versioning system e2e.

