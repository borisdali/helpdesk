# aiHelpDesk Governance Bot (aka `govbot`) sample report

In the sample run below the `govbot` is invoked manually on-demand
from the source code, but please see [this](AIGOVERNANCE.md#84-running-govbot)
for options and as well as the platform specific instructions on how to run it
[directly on a host/VM](deploy/host#77-running-the-compliance-reporter-govbot),
in [a Docker container](deploy/docker-compose#37-running-the-compliance-reporter-govbot)
or [on K8s](deploy/helm#97-running-the-compliance-reporter-govbot):

```
[boris@ ~/helpdesk]$ go run ./cmd/govbot/ -gateway http://localhost:8080
[09:44:13] Gateway:   http://localhost:8080
[09:44:13] Since:     last 24h
[09:44:13] Webhook:   false
[09:44:13] Dry run:   false
[09:44:13] History:   disabled


[09:44:13] ── Phase 1: Governance Status ──────────────────────────
[09:44:13] Audit enabled:    true  (413 total events)
[09:44:13] Audit backend:    sqlite
[09:44:13] Chain valid:      true
[09:44:13] Last event:       2026-03-15T02:41:16Z
[09:44:13] Policy enabled:   true  (11 policies, 22 rules)
[09:44:13] Pending approvals: 0
[09:44:13] Approval notify:  webhook=false  email=false


[09:44:13] ── Phase 2: Policy Overview ────────────────────────────
[09:44:13]   [enabled] emergency-break-glass
[09:44:13]        SRE on-call can perform any action with emergency purpose (fully audited)
[09:44:13]        Resources: database, kubernetes
[09:44:13]        read, write, destructive       → allow
[09:44:13]   [enabled] pii-data-protection
[09:44:13]        Restrict access to resources containing personally identifiable information
[09:44:13]        Resources: database
[09:44:13]        read                           → allow
[09:44:13]        write                          → allow  [requires approval]
[09:44:13]        destructive                    → deny
[09:44:13]   [enabled] critical-infra-write-guard
[09:44:13]        Writes to critical resources require explicit maintenance or emergency purpose
[09:44:13]        Resources: database, kubernetes
[09:44:13]        read                           → allow
[09:44:13]        write                          → allow  [requires approval]
[09:44:13]        destructive                    → deny
[09:44:13]   [enabled] production-database-protection
[09:44:13]        Restrict operations on production databases
[09:44:13]        Resources: database
[09:44:13]        read                           → allow
[09:44:13]        write                          → allow  [requires approval, row limit]
[09:44:13]        destructive                    → deny
[09:44:13]   [enabled] k8s-system-protection
[09:44:13]        Protect Kubernetes system namespaces
[09:44:13]        Resources: kubernetes, kubernetes
[09:44:13]        read                           → allow
[09:44:13]        write, destructive             → deny
[09:44:13]   [enabled] diagnostic-readonly-enforcement
[09:44:13]        Requests with diagnostic purpose are restricted to read-only operations
[09:44:13]        Resources: database, kubernetes
[09:44:13]        write, destructive             → deny
[09:44:13]   [enabled] business-hours-freeze
[09:44:13]        No changes during peak business hours
[09:44:13]        Resources: database, kubernetes
[09:44:13]        write, destructive             → deny  [time-based]
[09:44:13]   [enabled] dba-privileges
[09:44:13]        DBAs can perform write operations on any database
[09:44:13]        Resources: database
[09:44:13]        read, write                    → allow
[09:44:13]        destructive                    → allow  [requires approval]
[09:44:13]   [enabled] sre-staging-access
[09:44:13]        SRE team has full access to staging environment
[09:44:13]        Resources: database, kubernetes
[09:44:13]        read, write, destructive       → allow
[09:44:13]   [enabled] automated-services
[09:44:13]        Restrict automated service accounts
[09:44:13]        Resources: database, kubernetes
[09:44:13]        read                           → allow
[09:44:13]        write                          → allow  [requires approval, row limit]
[09:44:13]        destructive                    → deny
[09:44:13]   [enabled] development-permissive
[09:44:13]        Development environments are permissive
[09:44:13]        Resources: database, kubernetes
[09:44:13]        read, write                    → allow
[09:44:13]        destructive                    → allow


[09:44:13] ── Phase 3: Audit Activity (last 24h) ──────────────────
[09:44:13] Events fetched:   19
[09:44:13]   gateway_request                6
[09:44:13]   policy_decision                13


[09:44:13] ── Phase 4: Policy Decision Analysis ───────────────────
[09:44:13] Resource                                   allow    deny  req_apr  no_match
[09:44:13] ────────────────────────────────────────────────────────────────────────
[09:44:13] database/mydb                                  0       5       2       2 ⚠
[09:44:13] database/prod-customers                        3       2       1       0
[09:44:13] ────────────────────────────────────────────────────────────────────────
[09:44:13] TOTAL                                          3       7       3       2

[09:44:13] Blocked request details (10):
[09:44:13]   [DENY]  03-15 02:20:09  action=read          database/mydb
[09:44:13]     trace:   chk_bab3ec01  ← direct governance check (POST /v1/governance/check)
[09:44:13]     session: chk_bab3ec01
[09:44:13]     policy:  default
[09:44:13]     message: No matching policy found
[09:44:13]   [DENY]  03-15 02:12:51  action=read          database/mydb
[09:44:13]     trace:   chk_019db352  ← direct governance check (POST /v1/governance/check)
[09:44:13]     session: chk_019db352
[09:44:13]     policy:  default
[09:44:13]     message: No matching policy found
[09:44:13]   [REQUIRE_APPROVAL]  03-15 01:13:57  action=write         database/mydb
[09:44:13]     trace:   chk_62f1b1e0  ← direct governance check (POST /v1/governance/check)
[09:44:13]     session: chk_62f1b1e0
[09:44:13]     policy:  pii-data-protection
[09:44:13]     message: Write access to PII databases requires approval and a declared purpose.
[09:44:13]   [REQUIRE_APPROVAL]  03-15 01:13:48  action=write         database/mydb
[09:44:13]     trace:   chk_d7dcffdd  ← direct governance check (POST /v1/governance/check)
[09:44:13]     session: chk_d7dcffdd
[09:44:13]     policy:  pii-data-protection
[09:44:13]     message: Write access to PII databases requires approval and a declared purpose.
[09:44:13]   [DENY]  03-15 01:10:09  action=write         database/mydb
[09:44:13]     trace:   chk_bc555112  ← direct governance check (POST /v1/governance/check)
[09:44:13]     session: chk_bc555112
[09:44:13]     policy:  diagnostic-readonly-enforcement
[09:44:13]     message: Diagnostic purpose is read-only. Use 'remediation' purpose for write operations.
[09:44:13]   [DENY]  03-15 01:05:40  action=write         database/mydb
[09:44:13]     trace:   chk_5a50f64a  ← direct governance check (POST /v1/governance/check)
[09:44:13]     session: chk_5a50f64a
[09:44:13]     policy:  diagnostic-readonly-enforcement
[09:44:13]     message: Diagnostic purpose is read-only. Use 'remediation' purpose for write operations.
[09:44:13]   [DENY]  03-15 00:58:14  action=write         database/mydb
[09:44:13]     trace:   chk_7d94b982  ← direct governance check (POST /v1/governance/check)
[09:44:13]     session: chk_7d94b982
[09:44:13]     policy:  diagnostic-readonly-enforcement
[09:44:13]     message: Purpose "diagnostic" is in the blocked list [diagnostic]
[09:44:13]   [REQUIRE_APPROVAL]  03-14 17:30:51  action=write         database/prod-customers
[09:44:13]     trace:   chk_29ae732e  ← direct governance check (POST /v1/governance/check)
[09:44:13]     session: chk_29ae732e
[09:44:13]     policy:  pii-data-protection
[09:44:13]     message: Write access to PII databases requires approval and a declared purpose.
[09:44:13]   [DENY]  03-14 17:29:25  action=write         database/prod-customers
[09:44:13]     trace:   chk_e36e08b0  ← direct governance check (POST /v1/governance/check)
[09:44:13]     session: chk_e36e08b0
[09:44:13]     policy:  pii-data-protection
[09:44:13]     message: Purpose "diagnostic" is not in the allowed list [remediation compliance emergency]
[09:44:13]   [DENY]  03-14 17:17:36  action=write         database/prod-customers
[09:44:13]     trace:   tr-manual-1773508642  ← unknown origin (external or pre-dating prefix scheme)
[09:44:13]     session: tr-manual-1773508642
[09:44:13]     policy:  diagnostic-readonly-enforcement
[09:44:13]     message: Diagnostic purpose is read-only. Use 'remediation' purpose for write operations.


[09:44:13] ── Phase 5: Agent Enforcement Coverage ─────────────────
[09:44:13] Traces with tool executions: 0
[09:44:13]   Controlled (policy checked): 0
[09:44:13]   Uncontrolled (no policy):    0

[09:44:13] Policy decisions in window:  13  (+ 0 unattributable)
[09:44:13]   via agents:                  2  (16%)
[09:44:13]   via direct (chk_*):          11  (84%)


[09:44:13] ── Phase 6: Pending Approvals ──────────────────────────
[09:44:13] No pending approvals


[09:44:13] ── Phase 7: Chain Integrity ────────────────────────────
[09:44:13] Chain status:  ✓ VALID
[09:44:13] Total events:  413


[09:44:13] ── Phase 8: Mutation Activity (last 24h) ───────────────
[09:44:13] Total mutations:  0  (previous 24h: 4,  -100%)
[09:44:13] No write or destructive tool executions in this window


[09:44:13] ── Phase 9: Policy Coverage Analysis ───────────────────
[09:44:13] Note: reflects database + k8s agents only (incident + research not instrumented)

[09:44:13] All 5 resource-action pair(s) fully covered ✓

[09:44:13] Dead policy rules (policy exists but no invocations observed):
[09:44:13]   ⚠ WARN  policy "emergency-break-glass" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   ⚠ WARN  policy "critical-infra-write-guard" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   ⚠ WARN  policy "k8s-system-protection" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   ⚠ WARN  policy "k8s-system-protection" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   ⚠ WARN  policy "diagnostic-readonly-enforcement" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   ⚠ WARN  policy "business-hours-freeze" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   ⚠ WARN  policy "sre-staging-access" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   ⚠ WARN  policy "automated-services" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   ⚠ WARN  policy "development-permissive" covers resource type "kubernetes" — no tool_invoked events in window


[09:44:13] ── Phase 10: Identity Coverage ─────────────────────────
[09:44:13] Checks what fraction of policy decisions carry verified identity (user_id/service).

[09:44:13] Policy decisions with identity:  11 / 13  (84%)
[09:44:13]   ⚠ WARN  some requests are reaching the policy engine without identity

[09:44:13] Policy decisions with purpose:   10 / 13  (76%)
[09:44:13]   Write/destructive with purpose: 10 / 10  (100%)
[09:44:13]   ✓  purpose propagation for write/destructive looks healthy


[09:44:13] ── Phase 11: Purpose Coverage ──────────────────────────
[09:44:13] Checks declared purposes on sensitive and write/destructive operations.

[09:44:13] Purpose breakdown:
[09:44:13]   diagnostic           2 decision(s)
[09:44:13]   emergency            2 decision(s)
[09:44:13]   remediation          6 decision(s)

[09:44:13] Sensitive resource decisions:       6 total
[09:44:13]   Without declared purpose:         0
[09:44:13]   ✓  all sensitive resource accesses have a declared purpose


[09:44:13] ── Phase 12: Compliance Summary ────────────────────────
[09:44:13] Overall status: ✗ ALERTS
[09:44:13] ALERTS (1):
[09:44:13]   [ALERT] 7 request(s) were denied by policy — review blocked request details above
[09:44:13] Warnings (11):
[09:44:13]   [WARN]  2 policy decisions matched no rule (policy_name=default) — likely missing tags in infrastructure config
[09:44:13]   [WARN]  84% of policy decisions originate from direct (chk_*) calls, not agents — agents may not be using centralized enforcement (HELPDESK_AUDIT_URL)
[09:44:13]   [WARN]  policy "emergency-break-glass" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   [WARN]  policy "critical-infra-write-guard" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   [WARN]  policy "k8s-system-protection" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   [WARN]  policy "k8s-system-protection" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   [WARN]  policy "diagnostic-readonly-enforcement" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   [WARN]  policy "business-hours-freeze" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   [WARN]  policy "sre-staging-access" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   [WARN]  policy "automated-services" covers resource type "kubernetes" — no tool_invoked events in window
[09:44:13]   [WARN]  policy "development-permissive" covers resource type "kubernetes" — no tool_invoked events in window

[09:44:13] Done.
exit status 2
[boris@cassiopeia ~/cassiopeia/helpdesk]$ go run ./cmd/govbot/ -gateway http://localhost:8080
[09:53:57] Gateway:   http://localhost:8080
[09:53:57] Since:     last 24h
[09:53:57] Webhook:   false
[09:53:57] Dry run:   false
[09:53:57] History:   disabled


[09:53:57] ── Phase 1: Governance Status ──────────────────────────
[09:53:57] Audit enabled:    true  (413 total events)
[09:53:57] Audit backend:    sqlite
[09:53:57] Chain valid:      true
[09:53:57] Last event:       2026-03-15T02:41:16Z
[09:53:57] Policy enabled:   true  (11 policies, 22 rules)
[09:53:57] Pending approvals: 0
[09:53:57] Approval notify:  webhook=false  email=false


[09:53:57] ── Phase 2: Policy Overview ────────────────────────────
[09:53:57]   [enabled] emergency-break-glass
[09:53:57]        SRE on-call can perform any action with emergency purpose (fully audited)
[09:53:57]        Resources: database, kubernetes
[09:53:57]        read, write, destructive       → allow
[09:53:57]   [enabled] pii-data-protection
[09:53:57]        Restrict access to resources containing personally identifiable information
[09:53:57]        Resources: database
[09:53:57]        read                           → allow
[09:53:57]        write                          → allow  [requires approval]
[09:53:57]        destructive                    → deny
[09:53:57]   [enabled] critical-infra-write-guard
[09:53:57]        Writes to critical resources require explicit maintenance or emergency purpose
[09:53:57]        Resources: database, kubernetes
[09:53:57]        read                           → allow
[09:53:57]        write                          → allow  [requires approval]
[09:53:57]        destructive                    → deny
[09:53:57]   [enabled] production-database-protection
[09:53:57]        Restrict operations on production databases
[09:53:57]        Resources: database
[09:53:57]        read                           → allow
[09:53:57]        write                          → allow  [requires approval, row limit]
[09:53:57]        destructive                    → deny
[09:53:57]   [enabled] k8s-system-protection
[09:53:57]        Protect Kubernetes system namespaces
[09:53:57]        Resources: kubernetes, kubernetes
[09:53:57]        read                           → allow
[09:53:57]        write, destructive             → deny
[09:53:57]   [enabled] diagnostic-readonly-enforcement
[09:53:57]        Requests with diagnostic purpose are restricted to read-only operations
[09:53:57]        Resources: database, kubernetes
[09:53:57]        write, destructive             → deny
[09:53:57]   [enabled] business-hours-freeze
[09:53:57]        No changes during peak business hours
[09:53:57]        Resources: database, kubernetes
[09:53:57]        write, destructive             → deny  [time-based]
[09:53:57]   [enabled] dba-privileges
[09:53:57]        DBAs can perform write operations on any database
[09:53:57]        Resources: database
[09:53:57]        read, write                    → allow
[09:53:57]        destructive                    → allow  [requires approval]
[09:53:57]   [enabled] sre-staging-access
[09:53:57]        SRE team has full access to staging environment
[09:53:57]        Resources: database, kubernetes
[09:53:57]        read, write, destructive       → allow
[09:53:57]   [enabled] automated-services
[09:53:57]        Restrict automated service accounts
[09:53:57]        Resources: database, kubernetes
[09:53:57]        read                           → allow
[09:53:57]        write                          → allow  [requires approval, row limit]
[09:53:57]        destructive                    → deny
[09:53:57]   [enabled] development-permissive
[09:53:57]        Development environments are permissive
[09:53:57]        Resources: database, kubernetes
[09:53:57]        read, write                    → allow
[09:53:57]        destructive                    → allow


[09:53:57] ── Phase 3: Audit Activity (last 24h) ──────────────────
[09:53:57] Events fetched:   19
[09:53:57]   gateway_request                6
[09:53:57]   policy_decision                13


[09:53:57] ── Phase 4: Policy Decision Analysis ───────────────────
[09:53:57] Resource                                   allow    deny  req_apr  no_match
[09:53:57] ────────────────────────────────────────────────────────────────────────
[09:53:57] database/mydb                                  0       5       2       2 ⚠
[09:53:57] database/prod-customers                        3       2       1       0
[09:53:57] ────────────────────────────────────────────────────────────────────────
[09:53:57] TOTAL                                          3       7       3       2

[09:53:57] Blocked request details (10):
[09:53:57]   [DENY]  03-15 02:20:09  action=read          database/mydb
[09:53:57]     trace:   chk_bab3ec01  ← direct governance check (POST /v1/governance/check)
[09:53:57]     session: chk_bab3ec01
[09:53:57]     policy:  default
[09:53:57]     message: No matching policy found
[09:53:57]   [DENY]  03-15 02:12:51  action=read          database/mydb
[09:53:57]     trace:   chk_019db352  ← direct governance check (POST /v1/governance/check)
[09:53:57]     session: chk_019db352
[09:53:57]     policy:  default
[09:53:57]     message: No matching policy found
[09:53:57]   [REQUIRE_APPROVAL]  03-15 01:13:57  action=write         database/mydb
[09:53:57]     trace:   chk_62f1b1e0  ← direct governance check (POST /v1/governance/check)
[09:53:57]     session: chk_62f1b1e0
[09:53:57]     policy:  pii-data-protection
[09:53:57]     message: Write access to PII databases requires approval and a declared purpose.
[09:53:57]   [REQUIRE_APPROVAL]  03-15 01:13:48  action=write         database/mydb
[09:53:57]     trace:   chk_d7dcffdd  ← direct governance check (POST /v1/governance/check)
[09:53:57]     session: chk_d7dcffdd
[09:53:57]     policy:  pii-data-protection
[09:53:57]     message: Write access to PII databases requires approval and a declared purpose.
[09:53:57]   [DENY]  03-15 01:10:09  action=write         database/mydb
[09:53:57]     trace:   chk_bc555112  ← direct governance check (POST /v1/governance/check)
[09:53:57]     session: chk_bc555112
[09:53:57]     policy:  diagnostic-readonly-enforcement
[09:53:57]     message: Diagnostic purpose is read-only. Use 'remediation' purpose for write operations.
[09:53:57]   [DENY]  03-15 01:05:40  action=write         database/mydb
[09:53:57]     trace:   chk_5a50f64a  ← direct governance check (POST /v1/governance/check)
[09:53:57]     session: chk_5a50f64a
[09:53:57]     policy:  diagnostic-readonly-enforcement
[09:53:57]     message: Diagnostic purpose is read-only. Use 'remediation' purpose for write operations.
[09:53:57]   [DENY]  03-15 00:58:14  action=write         database/mydb
[09:53:57]     trace:   chk_7d94b982  ← direct governance check (POST /v1/governance/check)
[09:53:57]     session: chk_7d94b982
[09:53:57]     policy:  diagnostic-readonly-enforcement
[09:53:57]     message: Purpose "diagnostic" is in the blocked list [diagnostic]
[09:53:57]   [REQUIRE_APPROVAL]  03-14 17:30:51  action=write         database/prod-customers
[09:53:57]     trace:   chk_29ae732e  ← direct governance check (POST /v1/governance/check)
[09:53:57]     session: chk_29ae732e
[09:53:57]     policy:  pii-data-protection
[09:53:57]     message: Write access to PII databases requires approval and a declared purpose.
[09:53:57]   [DENY]  03-14 17:29:25  action=write         database/prod-customers
[09:53:57]     trace:   chk_e36e08b0  ← direct governance check (POST /v1/governance/check)
[09:53:57]     session: chk_e36e08b0
[09:53:57]     policy:  pii-data-protection
[09:53:57]     message: Purpose "diagnostic" is not in the allowed list [remediation compliance emergency]
[09:53:57]   [DENY]  03-14 17:17:36  action=write         database/prod-customers
[09:53:57]     trace:   tr-manual-1773508642  ← unknown origin (external or pre-dating prefix scheme)
[09:53:57]     session: tr-manual-1773508642
[09:53:57]     policy:  diagnostic-readonly-enforcement
[09:53:57]     message: Diagnostic purpose is read-only. Use 'remediation' purpose for write operations.


[09:53:57] ── Phase 5: Agent Enforcement Coverage ─────────────────
[09:53:57] Traces with tool executions: 0
[09:53:57]   Controlled (policy checked): 0
[09:53:57]   Uncontrolled (no policy):    0

[09:53:57] Policy decisions in window:  13  (+ 0 unattributable)
[09:53:57]   via agents:                  2  (16%)
[09:53:57]   via direct (chk_*):          11  (84%)


[09:53:57] ── Phase 6: Pending Approvals ──────────────────────────
[09:53:57] No pending approvals


[09:53:57] ── Phase 7: Chain Integrity ────────────────────────────
[09:53:57] Chain status:  ✓ VALID
[09:53:57] Total events:  413


[09:53:57] ── Phase 8: Mutation Activity (last 24h) ───────────────
[09:53:57] Total mutations:  0  (previous 24h: 4,  -100%)
[09:53:57] No write or destructive tool executions in this window


[09:53:57] ── Phase 9: Policy Coverage Analysis ───────────────────
[09:53:57] Note: reflects database + k8s agents only (incident + research not instrumented)

[09:53:57] All 5 resource-action pair(s) fully covered ✓

[09:53:57] Dead policy rules (policy exists but no invocations observed):
[09:53:57]   ⚠ WARN  policy "emergency-break-glass" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   ⚠ WARN  policy "critical-infra-write-guard" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   ⚠ WARN  policy "k8s-system-protection" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   ⚠ WARN  policy "k8s-system-protection" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   ⚠ WARN  policy "diagnostic-readonly-enforcement" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   ⚠ WARN  policy "business-hours-freeze" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   ⚠ WARN  policy "sre-staging-access" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   ⚠ WARN  policy "automated-services" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   ⚠ WARN  policy "development-permissive" covers resource type "kubernetes" — no tool_invoked events in window


[09:53:57] ── Phase 10: Identity Coverage ─────────────────────────
[09:53:57] Checks what fraction of policy decisions carry verified identity (user_id/service).

[09:53:57] Policy decisions with identity:  11 / 13  (84%)
[09:53:57]   ⚠ WARN  some requests are reaching the policy engine without identity

[09:53:57] Policy decisions with purpose:   10 / 13  (76%)
[09:53:57]   Write/destructive with purpose: 10 / 10  (100%)
[09:53:57]   ✓  purpose propagation for write/destructive looks healthy


[09:53:57] ── Phase 11: Purpose Coverage ──────────────────────────
[09:53:57] Checks declared purposes on sensitive and write/destructive operations.

[09:53:57] Purpose breakdown:
[09:53:57]   diagnostic           2 decision(s)
[09:53:57]   emergency            2 decision(s)
[09:53:57]   remediation          6 decision(s)

[09:53:57] Sensitive resource decisions:       6 total
[09:53:57]   Without declared purpose:         0
[09:53:57]   ✓  all sensitive resource accesses have a declared purpose


[09:53:57] ── Phase 12: Compliance Summary ────────────────────────
[09:53:57] Overall status: ✗ ALERTS
[09:53:57] ALERTS (1):
[09:53:57]   [ALERT] 7 request(s) were denied by policy — review blocked request details above
[09:53:57] Warnings (11):
[09:53:57]   [WARN]  2 policy decisions matched no rule (policy_name=default) — likely missing tags in infrastructure config
[09:53:57]   [WARN]  84% of policy decisions originate from direct (chk_*) calls, not agents — agents may not be using centralized enforcement (HELPDESK_AUDIT_URL)
[09:53:57]   [WARN]  policy "emergency-break-glass" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   [WARN]  policy "critical-infra-write-guard" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   [WARN]  policy "k8s-system-protection" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   [WARN]  policy "k8s-system-protection" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   [WARN]  policy "diagnostic-readonly-enforcement" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   [WARN]  policy "business-hours-freeze" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   [WARN]  policy "sre-staging-access" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   [WARN]  policy "automated-services" covers resource type "kubernetes" — no tool_invoked events in window
[09:53:57]   [WARN]  policy "development-permissive" covers resource type "kubernetes" — no tool_invoked events in window

[09:53:57] Done.
exit status 2
```

As a reminder, the exit status of 2 means that there are alerts present (see
[here](AIGOVERNANCE.md#82-exit-codes) for the full status legend).


