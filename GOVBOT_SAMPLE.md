
In the sample run below the `govbot` is invoked manually on-demand
from the source code, but please see [this](AIGOVERNANCE.md#84-running-govbot)
for options and as well as the platform specific instructions on how to run it
[directly on a host/VM](deploy/host#77-running-the-compliance-reporter-govbot),
in [a Docker container](deploy/docker-compose#37-running-the-compliance-reporter-govbot)
or [on K8s](deploy/helm#97-running-the-compliance-reporter-govbot):

```
[boris@ ~/helpdesk]$ go run ./cmd/govbot/ -gateway http://localhost:8080
[14:51:58] Gateway:   http://localhost:8080
[14:51:58] Since:     last 24h
[14:51:58] Webhook:   false
[14:51:58] Dry run:   false


[14:51:58] ── Phase 1: Governance Status ──────────────────────────
[14:51:58] Audit enabled:    true  (96 total events)
[14:51:58] Audit backend:    sqlite
[14:51:58] Chain valid:      true
[14:51:58] Last event:       2026-03-01T04:38:58Z
[14:51:58] Policy enabled:   true  (7 policies, 14 rules)
[14:51:58] Pending approvals: 0
[14:51:58] Approval notify:  webhook=false  email=false


[14:51:58] ── Phase 2: Policy Overview ────────────────────────────
[14:51:58]   [enabled] production-database-protection
[14:51:58]        Restrict operations on production databases
[14:51:58]        Resources: database
[14:51:58]        read                           → allow
[14:51:58]        write                          → allow  [requires approval, row limit]
[14:51:58]        destructive                    → deny
[14:51:58]   [enabled] k8s-system-protection
[14:51:58]        Protect Kubernetes system namespaces
[14:51:58]        Resources: kubernetes, kubernetes
[14:51:58]        read                           → allow
[14:51:58]        write, destructive             → deny
[14:51:58]   [enabled] business-hours-freeze
[14:51:58]        No changes during peak business hours
[14:51:58]        Resources: database, kubernetes
[14:51:58]        write, destructive             → deny  [time-based]
[14:51:58]   [enabled] dba-privileges
[14:51:58]        DBAs can perform write operations on any database
[14:51:58]        Resources: database
[14:51:58]        read, write                    → allow
[14:51:58]        destructive                    → allow  [requires approval]
[14:51:58]   [enabled] sre-staging-access
[14:51:58]        SRE team has full access to staging environment
[14:51:58]        Resources: database, kubernetes
[14:51:58]        read, write, destructive       → allow
[14:51:58]   [enabled] automated-services
[14:51:58]        Restrict automated service accounts
[14:51:58]        Resources: database, kubernetes
[14:51:58]        read                           → allow
[14:51:58]        write                          → allow  [requires approval, row limit]
[14:51:58]        destructive                    → deny
[14:51:58]   [enabled] development-permissive
[14:51:58]        Development environments are permissive
[14:51:58]        Resources: database, kubernetes
[14:51:58]        read, write                    → allow
[14:51:58]        destructive                    → allow


[14:51:58] ── Phase 3: Audit Activity (last 24h) ──────────────────
[14:51:58] Events fetched:   50
[14:51:58]   agent_reasoning                8
[14:51:58]   delegation_decision            8
[14:51:58]   policy_decision                12
[14:51:58]   tool_execution                 22


[14:51:58] ── Phase 4: Policy Decision Analysis ───────────────────
[14:51:58] Resource                                   allow    deny  req_apr  no_match
[14:51:58] ────────────────────────────────────────────────────────────────────────
[14:51:58] database/alloydb-on-vm                        11       1       0       0
[14:51:58] ────────────────────────────────────────────────────────────────────────
[14:51:58] TOTAL                                         11       1       0       0

[14:51:58] Blocked request details (1):
[14:51:58]   [DENY]  03-01 04:29:25  action=destructive   database/alloydb-on-vm
[14:51:58]     trace:   tr_a4f3c48d-d11  ← natural-language query (POST /api/v1/query)
[14:51:58]     session: tr_a4f3c48d-d11
[14:51:58]     policy:  prod-policy
[14:51:58]     message: destructive ops blocked on production


[14:51:58] ── Phase 5: Agent Enforcement Coverage ─────────────────
[14:51:58] Traces with tool executions: 3
[14:51:58]   Controlled (policy checked): 3
[14:51:58]   Uncontrolled (no policy):    0
[14:51:58]   Origin breakdown:
[14:51:58]     tr_*    3 trace(s)  ← natural-language query (POST /api/v1/query)

[14:51:58] Policy decisions in window:  12  (+ 0 unattributable)
[14:51:58]   via agents:                  12  (100%)
[14:51:58]   via direct (chk_*):          0  (0%)


[14:51:58] ── Phase 6: Pending Approvals ──────────────────────────
[14:51:58] No pending approvals


[14:51:58] ── Phase 7: Chain Integrity ────────────────────────────
[14:51:58] Chain status:  ✓ VALID
[14:51:58] Total events:  96


[14:51:58] ── Phase 8: Mutation Activity (last 24h) ───────────────
[14:51:58] Total mutations:  1  (previous 24h: 0)

[14:51:58] By class:
[14:51:58]   write:          0
[14:51:58]   destructive:    1

[14:51:58] By tool:
[14:51:58]   terminate_connection           1

[14:51:58] Hourly breakdown (UTC, 00–23):
[14:51:58]     0  1  2  3  4  5  6  7  8  9 10 11 12 13 14 15 16 17 18 19 20 21 22 23
[14:51:58]     0  0  0  0  1  0  0  0  0  0  0  0  0  0  0  0  0  0  0  0  0  0  0  0

[14:51:58] By user:
[14:51:58]   boris                          1


[14:51:58] ── Phase 9: Compliance Summary ─────────────────────────
[14:51:58] Overall status: ✗ ALERTS
[14:51:58] ALERTS (1):
[14:51:58]   [ALERT] 1 request(s) were denied by policy — review blocked request details above

[14:51:58] Done.
exit status 2
```

As a reminder, the exit status of 2 means that there are alerts present (see
[here](AIGOVERNANCE.md#82-exit-codes) for the full status legend).


